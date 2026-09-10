package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/cloudwego/eino/compose"

	"visitready/internal/domain"
	"visitready/internal/guard"
)

const (
	minInputRunes          = 20
	maxInputRunes          = 6000
	maxClarificationRounds = 4
	maxRecoveryAttempts    = 3
)

const EmergencyEscalationMessage = guard.EmergencyEscalationMessage

var (
	ErrInvalidInput = errors.New("invalid agent input")
	ErrInvalidState = errors.New("invalid agent state")
	ErrUpstream     = errors.New("agent upstream failure")
)

type LLMClient interface {
	Extract(ctx context.Context, input string) (domain.Extraction, error)
	GenerateQuestions(ctx context.Context, input domain.QuestionInput) (domain.QuestionSet, error)
}

type SearchClient interface {
	Search(ctx context.Context, query string, allowedDomains []string) ([]domain.Source, error)
}

type Config struct {
	LLM            LLMClient
	Search         SearchClient
	AllowedDomains []string
	SessionTTL     time.Duration
	Now            func() time.Time
	SearchCacheTTL time.Duration
}

type Runner struct {
	llm            LLMClient
	search         SearchClient
	allowedDomains []string
	sessionTTL     time.Duration
	now            func() time.Time
	workflow       compose.Runnable[workflowState, workflowState]
	searchCache    *searchCache
}

type workflowState struct {
	Session           domain.Session
	SearchQueries     []string
	priorFacts        []domain.Fact
	priorProfiles     []domain.SymptomProfile
	priorTimeline     []domain.TimelineEvent
	priorMissing      []string
	priorMissingItems []domain.MissingField
	priorGoal         string
}

func NewRunner(cfg Config) (*Runner, error) {
	if cfg.LLM == nil {
		return nil, errors.New("LLM client is required")
	}
	if cfg.SessionTTL <= 0 {
		return nil, errors.New("session TTL must be positive")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	runner := &Runner{
		llm:            cfg.LLM,
		search:         cfg.Search,
		allowedDomains: append([]string(nil), cfg.AllowedDomains...),
		sessionTTL:     cfg.SessionTTL,
		now:            cfg.Now,
		searchCache:    newSearchCache(cfg.SearchCacheTTL),
	}
	workflow, err := runner.buildWorkflow()
	if err != nil {
		return nil, fmt.Errorf("compile agent workflow: %w", err)
	}
	runner.workflow = workflow
	return runner, nil
}

func (r *Runner) Start(ctx context.Context, input string, allowWebSearch bool) (domain.Session, error) {
	input = strings.TrimSpace(input)
	emergencySignals := guard.DetectEmergencySignals(input)
	if len(emergencySignals) == 0 {
		if err := validateUserText(input, minInputRunes, maxInputRunes); err != nil {
			return domain.Session{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		if findings := guard.ScanPII(input); len(findings) > 0 {
			return domain.Session{}, fmt.Errorf("%w: input contains direct personal identifiers: %s", ErrInvalidInput, strings.Join(findings, ", "))
		}
	}

	sessionID, err := newSessionID()
	if err != nil {
		return domain.Session{}, err
	}
	now := r.now()
	session := domain.Session{
		ID:             sessionID,
		Revision:       1,
		Status:         domain.StatusNew,
		RawInput:       input,
		AllowWebSearch: allowWebSearch,
		CreatedAt:      now,
		ExpiresAt:      now.Add(r.sessionTTL),
	}
	if len(emergencySignals) > 0 {
		// Emergency sessions retain only minimal matched terms, never the raw
		// input, so urgent guidance is not blocked by accidental identifiers.
		session.RawInput = ""
		return r.escalateEmergency(session, emergencySignals), nil
	}
	result, err := r.workflow.Invoke(ctx, workflowState{Session: session})
	if err != nil {
		return r.handleRunFailure(session, 1, "run agent", err)
	}
	return result.Session, nil
}

func (r *Runner) Resume(ctx context.Context, session domain.Session, answer string) (domain.Session, error) {
	if session.Status != domain.StatusWaitingClarification {
		return domain.Session{}, fmt.Errorf("%w: session is not waiting for clarification", ErrInvalidState)
	}
	answer = strings.TrimSpace(answer)
	questions := append([]domain.Question(nil), session.ClarificationPrompts...)
	if len(questions) == 0 {
		for _, question := range session.ClarificationQuestions {
			questions = append(questions, domain.Question{Text: question})
		}
	}
	emergencySignals := guard.DetectEmergencySignals(answer)
	if len(emergencySignals) == 0 {
		emergencySignals = guard.DetectEmergencyAnswer(answer, questions)
	}
	if len(emergencySignals) == 0 {
		if err := validateUserText(answer, 2, 2000); err != nil {
			return domain.Session{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		if findings := guard.ScanPII(answer); len(findings) > 0 {
			return domain.Session{}, fmt.Errorf("%w: clarification contains direct personal identifiers: %s", ErrInvalidInput, strings.Join(findings, ", "))
		}
		if isBareSensitiveNumber(answer) {
			return domain.Session{}, fmt.Errorf("%w: clarification appears to contain a sensitive numeric credential", ErrInvalidInput)
		}
	}

	next := cloneSession(session)
	if len(emergencySignals) > 0 {
		next.ClarificationCount++
		return r.escalateEmergency(next, emergencySignals), nil
	}
	next.ClarificationTurns = append(next.ClarificationTurns, domain.ClarificationTurn{Questions: questions, Answer: answer})
	if next.Clarification == "" {
		next.Clarification = answer
	} else {
		next.Clarification += "\n" + answer
	}
	next.ClarificationCount++
	if shouldSkipClarification(answer) {
		next.ClarificationCount = maxClarificationRounds
	}
	next.ClarificationQuestions = nil
	next.ClarificationPrompts = nil
	result, err := r.workflow.Invoke(ctx, workflowState{Session: next})
	if err != nil {
		return r.handleRunFailure(next, 1, "resume agent", err)
	}
	return result.Session, nil
}

func (r *Runner) Retry(ctx context.Context, session domain.Session) (domain.Session, error) {
	if session.Status != domain.StatusFailed || session.Failure == nil || !session.Failure.Retryable || session.Failure.Attempts >= maxRecoveryAttempts {
		return domain.Session{}, fmt.Errorf("%w: session cannot be retried", ErrInvalidState)
	}
	attempts := session.Failure.Attempts + 1
	next := cloneSession(session)
	next.Failure = nil
	next = r.addEvent(next, "recovery", "running", "正在从已保存的上下文重新处理")
	result, err := r.workflow.Invoke(ctx, workflowState{Session: next})
	if err != nil {
		return r.handleRunFailure(next, attempts, "retry agent", err)
	}
	result.Session.Failure = nil
	result.Session = r.addEvent(result.Session, "recovery", "completed", "已从保存的上下文恢复处理")
	return result.Session, nil
}

func (r *Runner) handleRunFailure(session domain.Session, attempts int, operation string, err error) (domain.Session, error) {
	wrapped := fmt.Errorf("%w: %s: %w", ErrUpstream, operation, err)
	if errors.Is(err, context.Canceled) {
		return domain.Session{}, wrapped
	}
	return r.failedSession(session, attempts, isRetryableRunError(err)), wrapped
}

func isRetryableRunError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var retryable interface{ Retryable() bool }
	return errors.As(err, &retryable) && retryable.Retryable()
}

func (r *Runner) failedSession(session domain.Session, attempts int, retryable bool) domain.Session {
	next := cloneSession(session)
	next.Status = domain.StatusFailed
	code := domain.FailureTemporary
	message := "AI 暂时没有完成处理，本次内容已安全保留。"
	canRetry := retryable && attempts < maxRecoveryAttempts
	if !retryable {
		code = domain.FailurePermanent
		message = "AI 返回的结果未通过安全校验，请重新开始。"
	} else if attempts >= maxRecoveryAttempts {
		code = domain.FailureExhausted
		message = "本次会话已达到自动恢复上限，请开始新的诊前准备。"
	}
	next.Failure = &domain.RunFailure{
		Code:      code,
		Message:   message,
		Retryable: canRetry,
		Attempts:  attempts,
		FailedAt:  r.now(),
	}
	next = r.addEvent(next, "recovery", "failed", "本次处理未完成，已保存可恢复状态")
	return next
}

func (r *Runner) escalateEmergency(session domain.Session, signals []domain.RiskSignal) domain.Session {
	next := cloneSession(session)
	next.RawInput = ""
	next.Clarification = ""
	next.ClarificationTurns = nil
	next.Status = domain.StatusEmergency
	next.EmergencyMessage = EmergencyEscalationMessage
	next.RiskSignals = append([]domain.RiskSignal(nil), signals...)
	next.ClarificationQuestions = nil
	next.ClarificationPrompts = nil
	next.Questions = nil
	next.ActionItems = nil
	next.Sources = nil
	return r.addEvent(next, "emergency", "completed", "已触发紧急安全处理，停止后续 Agent 步骤")
}

func isBareSensitiveNumber(value string) bool {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) < 6 || len(runes) > 19 {
		return false
	}
	for _, char := range runes {
		if !unicode.IsDigit(char) {
			return false
		}
	}
	return true
}

func shouldSkipClarification(answer string) bool {
	normalized := strings.TrimSpace(strings.Trim(answer, "。！？!?；;，,"))
	if strings.Contains(normalized, "跳过剩余") || strings.Contains(normalized, "不再追问") {
		return true
	}
	for _, phrase := range []string{"跳过", "不清楚", "不知道", "无法提供", "记不清", "暂不回答"} {
		if normalized == phrase || normalized == "全部"+phrase || normalized == "这些都"+phrase {
			return true
		}
	}
	return false
}

func (r *Runner) Confirm(session domain.Session, visitGoal string) (domain.Session, error) {
	if session.Status != domain.StatusWaitingReview {
		return domain.Session{}, fmt.Errorf("%w: session is not waiting for review", ErrInvalidState)
	}
	visitGoal = strings.TrimSpace(visitGoal)
	if visitGoal != "" {
		if len([]rune(visitGoal)) > 240 || guard.ContainsMedicalOverreach(visitGoal) || len(guard.ScanPII(visitGoal)) > 0 {
			return domain.Session{}, fmt.Errorf("%w: visit goal is invalid", ErrInvalidInput)
		}
		session.VisitGoal = visitGoal
	}
	next := cloneSession(session)
	next.Status = domain.StatusCompleted
	next.RawInput = ""
	next.Clarification = ""
	next.ClarificationTurns = nil
	next.Events = append(next.Events, domain.AgentEvent{
		Step: "review", Status: "completed", Message: "用户已确认就诊摘要", CreatedAt: r.now(),
	})
	return next, nil
}

func validateUserText(input string, minRunes, maxRunes int) error {
	length := len([]rune(input))
	if length < minRunes || length > maxRunes {
		return fmt.Errorf("input must contain %d to %d characters", minRunes, maxRunes)
	}
	return nil
}

func newSessionID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func cloneSession(source domain.Session) domain.Session {
	cloned := source
	if source.Failure != nil {
		failure := *source.Failure
		cloned.Failure = &failure
	}
	cloned.Facts = append([]domain.Fact(nil), source.Facts...)
	cloned.SymptomProfiles = cloneSymptomProfiles(source.SymptomProfiles)
	cloned.Timeline = append([]domain.TimelineEvent(nil), source.Timeline...)
	cloned.RiskSignals = append([]domain.RiskSignal(nil), source.RiskSignals...)
	cloned.MissingFields = append([]string(nil), source.MissingFields...)
	cloned.MissingFieldItems = append([]domain.MissingField(nil), source.MissingFieldItems...)
	cloned.ClarificationQuestions = append([]string(nil), source.ClarificationQuestions...)
	cloned.ClarificationPrompts = append([]domain.Question(nil), source.ClarificationPrompts...)
	cloned.ClarificationTurns = cloneClarificationTurns(source.ClarificationTurns)
	cloned.Questions = append([]domain.Question(nil), source.Questions...)
	cloned.ActionItems = append([]domain.ActionItem(nil), source.ActionItems...)
	cloned.Sources = append([]domain.Source(nil), source.Sources...)
	cloned.Uncertainties = append([]domain.Uncertainty(nil), source.Uncertainties...)
	cloned.Contradictions = append([]domain.Contradiction(nil), source.Contradictions...)
	cloned.Medications = append([]string(nil), source.Medications...)
	cloned.Allergies = append([]string(nil), source.Allergies...)
	cloned.ChronicConditions = append([]string(nil), source.ChronicConditions...)
	cloned.TraumaHistory = append([]string(nil), source.TraumaHistory...)
	cloned.SafetyNotes = append([]string(nil), source.SafetyNotes...)
	cloned.DeniedConditions = append([]string(nil), source.DeniedConditions...)
	cloned.Measurements = append([]string(nil), source.Measurements...)
	cloned.Tests = append([]string(nil), source.Tests...)
	cloned.ConversationSummary = source.ConversationSummary
	cloned.ConversationSummary.Confirmed = append([]string(nil), source.ConversationSummary.Confirmed...)
	cloned.ConversationSummary.OpenThreads = append([]string(nil), source.ConversationSummary.OpenThreads...)
	cloned.Events = append([]domain.AgentEvent(nil), source.Events...)
	cloned.NodeTimings = append([]domain.NodeTiming(nil), source.NodeTimings...)
	return cloned
}

func cloneClarificationTurns(source []domain.ClarificationTurn) []domain.ClarificationTurn {
	cloned := append([]domain.ClarificationTurn(nil), source...)
	for index := range cloned {
		cloned[index].Questions = append([]domain.Question(nil), source[index].Questions...)
	}
	return cloned
}

func cloneSymptomProfiles(source []domain.SymptomProfile) []domain.SymptomProfile {
	cloned := append([]domain.SymptomProfile(nil), source...)
	for index := range cloned {
		cloned[index].AssociatedSymptoms = append([]string(nil), source[index].AssociatedSymptoms...)
		cloned[index].EvidenceQuotes = append([]string(nil), source[index].EvidenceQuotes...)
	}
	return cloned
}
