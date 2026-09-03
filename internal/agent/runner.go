package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/compose"

	"visitready/internal/domain"
	"visitready/internal/guard"
)

const (
	minInputRunes = 20
	maxInputRunes = 6000
)

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
}

type Runner struct {
	llm            LLMClient
	search         SearchClient
	allowedDomains []string
	sessionTTL     time.Duration
	now            func() time.Time
	workflow       compose.Runnable[workflowState, workflowState]
}

type workflowState struct {
	Session       domain.Session
	SearchQueries []string
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
	if err := validateUserText(input, minInputRunes, maxInputRunes); err != nil {
		return domain.Session{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if findings := guard.ScanPII(input); len(findings) > 0 {
		return domain.Session{}, fmt.Errorf("%w: input contains direct personal identifiers: %s", ErrInvalidInput, strings.Join(findings, ", "))
	}

	sessionID, err := newSessionID()
	if err != nil {
		return domain.Session{}, err
	}
	now := r.now()
	session := domain.Session{
		ID:             sessionID,
		Status:         domain.StatusNew,
		RawInput:       input,
		AllowWebSearch: allowWebSearch,
		CreatedAt:      now,
		ExpiresAt:      now.Add(r.sessionTTL),
	}
	result, err := r.workflow.Invoke(ctx, workflowState{Session: session})
	if err != nil {
		return domain.Session{}, fmt.Errorf("%w: run agent: %w", ErrUpstream, err)
	}
	return result.Session, nil
}

func (r *Runner) Resume(ctx context.Context, session domain.Session, answer string) (domain.Session, error) {
	if session.Status != domain.StatusWaitingClarification {
		return domain.Session{}, fmt.Errorf("%w: session is not waiting for clarification", ErrInvalidState)
	}
	answer = strings.TrimSpace(answer)
	if err := validateUserText(answer, 2, 2000); err != nil {
		return domain.Session{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if findings := guard.ScanPII(answer); len(findings) > 0 {
		return domain.Session{}, fmt.Errorf("%w: clarification contains direct personal identifiers: %s", ErrInvalidInput, strings.Join(findings, ", "))
	}

	next := cloneSession(session)
	next.Clarification = answer
	next.ClarificationCount++
	next.ClarificationQuestions = nil
	result, err := r.workflow.Invoke(ctx, workflowState{Session: next})
	if err != nil {
		return domain.Session{}, fmt.Errorf("%w: resume agent: %w", ErrUpstream, err)
	}
	return result.Session, nil
}

func (r *Runner) Confirm(session domain.Session, visitGoal string) (domain.Session, error) {
	if session.Status != domain.StatusWaitingReview {
		return domain.Session{}, fmt.Errorf("%w: session is not waiting for review", ErrInvalidState)
	}
	visitGoal = strings.TrimSpace(visitGoal)
	if visitGoal != "" {
		if len([]rune(visitGoal)) > 240 || guard.ContainsMedicalOverreach(visitGoal) {
			return domain.Session{}, fmt.Errorf("%w: visit goal is invalid", ErrInvalidInput)
		}
		session.VisitGoal = visitGoal
	}
	next := cloneSession(session)
	next.Status = domain.StatusCompleted
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
	cloned.Facts = append([]domain.Fact(nil), source.Facts...)
	cloned.MissingFields = append([]string(nil), source.MissingFields...)
	cloned.ClarificationQuestions = append([]string(nil), source.ClarificationQuestions...)
	cloned.Questions = append([]domain.Question(nil), source.Questions...)
	cloned.Sources = append([]domain.Source(nil), source.Sources...)
	cloned.Events = append([]domain.AgentEvent(nil), source.Events...)
	return cloned
}
