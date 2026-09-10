package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/cloudwego/eino/compose"

	"visitready/internal/domain"
	"visitready/internal/guard"
)

const (
	nodeExtract           = "extract_facts"
	nodeValidateExtract   = "validate_extraction"
	nodeEvidence          = "verify_evidence"
	nodeEmergency         = "emergency_escalation"
	nodeSearch            = "trusted_search"
	nodeQuestions         = "generate_questions"
	nodeValidateQuestions = "validate_questions"
	nodeGuard             = "output_guard"
)

func (r *Runner) buildWorkflow() (compose.Runnable[workflowState, workflowState], error) {
	graph := compose.NewGraph[workflowState, workflowState]()
	nodes := []struct {
		name string
		fn   func(context.Context, workflowState) (workflowState, error)
	}{
		{name: nodeExtract, fn: r.extractNode},
		{name: nodeValidateExtract, fn: r.extractionValidatorNode},
		{name: nodeEvidence, fn: r.evidenceNode},
		{name: nodeEmergency, fn: r.emergencyEscalationNode},
		{name: nodeSearch, fn: r.searchNode},
		{name: nodeQuestions, fn: r.questionNode},
		{name: nodeValidateQuestions, fn: r.questionValidatorNode},
		{name: nodeGuard, fn: r.outputGuardNode},
	}
	for _, node := range nodes {
		if err := graph.AddLambdaNode(node.name, compose.InvokableLambda(r.instrumentNode(node.name, node.fn))); err != nil {
			return nil, fmt.Errorf("add %s node: %w", node.name, err)
		}
	}
	edges := [][2]string{
		{compose.START, nodeExtract},
		{nodeExtract, nodeValidateExtract},
		{nodeValidateExtract, nodeEvidence},
		{nodeEvidence, nodeEmergency},
		{nodeEmergency, nodeSearch},
		{nodeSearch, nodeQuestions},
		{nodeQuestions, nodeValidateQuestions},
		{nodeValidateQuestions, nodeGuard},
		{nodeGuard, compose.END},
	}
	for _, edge := range edges {
		if err := graph.AddEdge(edge[0], edge[1]); err != nil {
			return nil, fmt.Errorf("add edge %s to %s: %w", edge[0], edge[1], err)
		}
	}
	return graph.Compile(context.Background(), compose.WithMaxRunSteps(9), compose.WithGraphName("VisitReadyAgent"))
}

func (r *Runner) extractNode(ctx context.Context, state workflowState) (workflowState, error) {
	next := cloneWorkflowState(state)
	next.Session.Status = domain.StatusExtracting
	next.Session = r.addEvent(next.Session, "extract", "running", "正在提取用户明确提供的事实")
	if next.Session.ClarificationCount > 0 {
		next.priorFacts = append([]domain.Fact(nil), next.Session.Facts...)
		next.priorProfiles = cloneSymptomProfiles(next.Session.SymptomProfiles)
		next.priorTimeline = append([]domain.TimelineEvent(nil), next.Session.Timeline...)
		next.priorMissing = append([]string(nil), next.Session.MissingFields...)
		next.priorMissingItems = append([]domain.MissingField(nil), next.Session.MissingFieldItems...)
		next.priorGoal = next.Session.VisitGoal
	}

	input := extractionInput(next.Session)
	extraction, err := r.llm.Extract(ctx, input)
	if err != nil {
		// Once facts are already confirmed, a failed re-extraction must not fail
		// the run: keep the confirmed facts and let the deterministic slot
		// write-back still apply this round's answer. Only cancellation aborts.
		if len(next.priorFacts) > 0 && !errors.Is(err, context.Canceled) {
			next.Session = r.addEvent(next.Session, "extract", "degraded", "本轮重新提取未完成，已保留已确认信息")
			return next, nil
		}
		return workflowState{}, fmt.Errorf("extract facts: %w", err)
	}
	next.Session.VisitGoal = strings.TrimSpace(extraction.VisitGoal)
	next.Session.Facts = append([]domain.Fact(nil), extraction.Facts...)
	next.Session.SymptomProfiles = cloneSymptomProfiles(extraction.SymptomProfiles)
	next.Session.Timeline = append([]domain.TimelineEvent(nil), extraction.Timeline...)
	next.Session.RiskSignals = append([]domain.RiskSignal(nil), extraction.RiskSignals...)
	next.Session.MissingFieldItems = append([]domain.MissingField(nil), extraction.MissingFieldItems...)
	next.Session.MissingFields = missingFieldLabels(next.Session.MissingFieldItems, extraction.MissingFields)
	next.Session = applyGlobals(next.Session, extraction)
	next.Session.ClarificationQuestions = append([]string(nil), extraction.ClarificationQuestions...)
	next.Session.ClarificationPrompts = append([]domain.Question(nil), extraction.ClarificationPrompts...)
	next.Session.Uncertainties = append([]domain.Uncertainty(nil), extraction.Uncertainties...)
	next.Session.Contradictions = append([]domain.Contradiction(nil), extraction.Contradictions...)
	next.Session.ConversationSummary = extraction.ConversationSummary
	next.SearchQueries = append([]string(nil), extraction.SearchQueries...)
	next.Session = r.addEvent(next.Session, "extract", "completed", fmt.Sprintf("已提取 %d 项候选事实", len(extraction.Facts)))
	return next, nil
}

func (r *Runner) extractionValidatorNode(_ context.Context, state workflowState) (workflowState, error) {
	next := cloneWorkflowState(state)
	validated, rejected := validateExtraction(evidenceInput(next.Session), domain.Extraction{
		VisitGoal: next.Session.VisitGoal, Facts: next.Session.Facts,
		SymptomProfiles: next.Session.SymptomProfiles, Timeline: next.Session.Timeline,
		RiskSignals: next.Session.RiskSignals, MissingFields: next.Session.MissingFields,
		MissingFieldItems:      next.Session.MissingFieldItems,
		ClarificationQuestions: next.Session.ClarificationQuestions,
		ClarificationPrompts:   next.Session.ClarificationPrompts,
		SearchQueries:          next.SearchQueries, Uncertainties: next.Session.Uncertainties,
		Contradictions: next.Session.Contradictions, ConversationSummary: next.Session.ConversationSummary,
	}, next.Session.ClarificationTurns)
	if len(validated.Facts) == 0 && len(next.priorFacts) == 0 {
		return workflowState{}, fmt.Errorf("all %d extracted facts failed evidence validation", rejected)
	}
	next.Session.Facts = mergeGroundedFacts(next.priorFacts, validated.Facts)
	next.Session = deriveGlobalsFromFacts(next.Session, next.Session.Facts)
	next.Session.SymptomProfiles = mergeSymptomProfiles(next.priorProfiles, validated.SymptomProfiles, latestTurnCorrectedSlots(next.Session.ClarificationTurns))
	next.Session.SymptomProfiles = mergeClarificationSlots(next.Session.SymptomProfiles, next.Session.ClarificationTurns)
	next.Session.Timeline = mergeTimeline(next.priorTimeline, validated.Timeline)
	if mergedItems := mergeMissingFieldItems(next.priorMissingItems, validated.MissingFieldItems); len(mergedItems) > 0 {
		next.Session.MissingFieldItems = reconcileMissingFieldItems(mergedItems, next.Session.SymptomProfiles, next.Session)
		next.Session.MissingFields = missingFieldLabels(next.Session.MissingFieldItems, nil)
	} else {
		missing := append(append([]string(nil), next.priorMissing...), validated.MissingFields...)
		next.Session.MissingFields = reconcileMissingFields(missing, next.Session.SymptomProfiles, next.Session)
	}
	if goal := strings.TrimSpace(validated.VisitGoal); goal != "" {
		next.Session.VisitGoal = goal
	} else if next.priorGoal != "" {
		next.Session.VisitGoal = next.priorGoal
	}
	next.Session.RiskSignals = validated.RiskSignals
	next.Session.RejectedFactCount += rejected
	if len(validated.Facts) == 0 {
		next.Session = r.addEvent(next.Session, "validator", "degraded", "本轮提取未通过原文校验，已保留已确认事实")
		return next, nil
	}
	next.Session = r.addEvent(next.Session, "validator", "completed", "模型提取结果已通过独立边界校验")
	return next, nil
}

func (r *Runner) evidenceNode(_ context.Context, state workflowState) (workflowState, error) {
	next := cloneWorkflowState(state)
	next.Session.Status = domain.StatusValidating
	summary, uncertainties, contradictions := deriveConversationSignals(next.Session)
	if summary.Intent == "" {
		summary.Intent = next.Session.VisitGoal
	}
	next.Session.ConversationSummary = summary
	next.Session.Uncertainties = uncertainties
	next.Session.Contradictions = contradictions
	next.Session.InterviewState = deriveInterviewState(next.Session, uncertainties, contradictions)
	hasContradictions := len(unresolvedContradictions(contradictions)) > 0

	safePrompts := make([]domain.Question, 0, len(next.Session.ClarificationPrompts)+len(next.Session.ClarificationQuestions))
	seenPrompts := make(map[string]struct{})
	for _, turn := range next.Session.ClarificationTurns {
		for _, asked := range turn.Questions {
			seenPrompts[normalizeFactEvidence(asked.Text)] = struct{}{}
		}
	}
	for _, prompt := range next.Session.ClarificationPrompts {
		prompt = sanitizeQuestion(prompt)
		if !isSafeClarificationPrompt(prompt) {
			continue
		}
		key := normalizeFactEvidence(prompt.Text)
		if _, exists := seenPrompts[key]; exists {
			continue
		}
		seenPrompts[key] = struct{}{}
		safePrompts = append(safePrompts, prompt)
	}
	for _, question := range next.Session.ClarificationQuestions {
		question = strings.TrimSpace(question)
		key := normalizeFactEvidence(question)
		if _, exists := seenPrompts[key]; isSafeClarificationPrompt(domain.Question{Text: question}) && !exists {
			safePrompts = append(safePrompts, domain.Question{Text: question, Priority: domain.PriorityNormal})
			seenPrompts[key] = struct{}{}
		}
	}
	for _, contradiction := range unresolvedContradictions(next.Session.Contradictions) {
		prompt := domain.Question{Text: contradiction.ClarifyingQuestion, Reason: "Agent 发现两段回答指向不同信息，先确认原话再生成最终摘要", Priority: contradiction.Priority, Category: "missing_detail"}
		key := normalizeFactEvidence(prompt.Text)
		if _, exists := seenPrompts[key]; !exists && isSafeClarificationPrompt(prompt) {
			safePrompts = append(safePrompts, prompt)
			seenPrompts[key] = struct{}{}
		}
	}
	for _, prompt := range missingFieldPrompts(next.Session) {
		if prompt.Text == "" || !isSafeClarificationPrompt(prompt) {
			continue
		}
		key := normalizeFactEvidence(prompt.Text)
		if _, exists := seenPrompts[key]; exists {
			continue
		}
		seenPrompts[key] = struct{}{}
		safePrompts = append(safePrompts, prompt)
	}
	safePrompts = prioritizeQuestions(safePrompts, 3)
	next.Session.ClarificationPrompts = safePrompts
	next.Session.ClarificationQuestions = make([]string, 0, len(safePrompts))
	for _, prompt := range safePrompts {
		next.Session.ClarificationQuestions = append(next.Session.ClarificationQuestions, prompt.Text)
	}
	next.Session = r.addEvent(next.Session, "evidence", "completed", fmt.Sprintf("%d 项事实通过原文校验", len(next.Session.Facts)))

	if next.Session.ClarificationCount < maxClarificationRounds && len(next.Session.ClarificationQuestions) > 0 {
		if hasContradictions {
			next.Session.Status = domain.StatusWaitingClarification
			next.Session = r.addEvent(next.Session, "clarification", "waiting", "发现不同说法，需要用户确认后继续")
		} else {
			next.Session.Status = domain.StatusWaitingClarification
			next.Session = r.addEvent(next.Session, "clarification", "waiting", "需要用户补充一轮信息")
		}
	}
	return next, nil
}

// missingFieldPrompts builds deterministic follow-up questions. Categorized
// fields drive the question directly; legacy free-text fields fall back to
// keyword inference only when no categorized fields exist.
func missingFieldPrompts(session domain.Session) []domain.Question {
	prompts := make([]domain.Question, 0, len(session.MissingFieldItems)+len(session.MissingFields))
	if len(session.MissingFieldItems) > 0 {
		for _, item := range session.MissingFieldItems {
			prompts = append(prompts, clarificationPromptForMissingField(item.Field, item.Category, session))
		}
		return prompts
	}
	for _, field := range session.MissingFields {
		prompts = append(prompts, clarificationPromptForMissingField(field, "", session))
	}
	return prompts
}

func clarificationPromptForMissingField(field, category string, session domain.Session) domain.Question {
	priority := uncertaintyPriority(field)
	subject := missingFieldSubject(field, session.SymptomProfiles)
	switch missingFieldSlot(field, category) {
	case "duration":
		if subject == "" {
			return domain.Question{}
		}
		return domain.Question{Text: subject + "每次大约持续多久？", Reason: "补全仍未确认的持续时间", Priority: priority, Category: "duration"}
	case "frequency":
		if subject == "" {
			return domain.Question{}
		}
		return domain.Question{Text: subject + "大概多久发作一次？", Reason: "补全仍未确认的发生频率", Priority: priority, Category: "frequency"}
	case "onset":
		if subject == "" {
			return domain.Question{}
		}
		return domain.Question{Text: subject + "大约从什么时候开始？", Reason: "补全仍未确认的开始时间", Priority: priority, Category: "timeline"}
	case "trigger":
		if subject == "" {
			return domain.Question{}
		}
		return domain.Question{Text: subject + "在什么情况下更明显，怎样会缓解？", Reason: "补全仍未确认的诱因或缓解因素", Priority: priority, Category: "trigger"}
	case "severity":
		if subject == "" {
			return domain.Question{}
		}
		return domain.Question{Text: subject + "目前对日常活动有什么影响？", Reason: "补全仍未确认的日常影响", Priority: priority, Category: "severity"}
	case "associated":
		if subject == "" {
			return domain.Question{}
		}
		return domain.Question{Text: subject + "出现时还伴有哪些不适？", Reason: "补全仍未确认的伴随表现", Priority: priority, Category: "associated_symptom"}
	}
	switch category {
	case "medication":
		return domain.Question{Text: "目前正在服用哪些药物或保健品？", Reason: "补全仍未确认的用药信息", Priority: priority, Category: "medication"}
	case "allergy":
		return domain.Question{Text: "对哪些药物、食物或环境因素过敏？", Reason: "补全仍未确认的过敏信息", Priority: priority, Category: "allergy"}
	case "history":
		return domain.Question{Text: "既往有哪些需要向医生说明的疾病、手术或外伤？", Reason: "补全仍未确认的既往情况", Priority: priority, Category: "history"}
	case "measurement":
		return domain.Question{Text: "测量过哪些项目，具体数值是多少？", Reason: "补全仍未确认的测量数值", Priority: priority, Category: "measurement"}
	case "test":
		return domain.Question{Text: "做过哪些检查，结果如何？", Reason: "补全仍未确认的检查情况", Priority: priority, Category: "test"}
	case "safety":
		return domain.Question{Text: missingFieldQuestionText(field, "安全相关"), Reason: "补全仍未确认的安全相关信息", Priority: priority, Category: "safety"}
	}
	return domain.Question{}
}

func missingFieldQuestionText(field, fallback string) string {
	field = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(field), "？?"))
	if field == "" {
		return "关于" + fallback + "，还需要补充哪些信息？"
	}
	return field + "？"
}

// missingFieldSlot resolves a field to its profile slot, preferring the
// extraction-provided category over keyword inference. Global and unclassified
// categories never fall back to inference.
func missingFieldSlot(field, category string) string {
	switch category {
	case "onset":
		return "onset"
	case "duration":
		return "duration"
	case "frequency":
		return "frequency"
	case "trigger":
		return "trigger"
	case "severity":
		return "severity"
	case "associated":
		return "associated"
	case "medication", "allergy", "history", "safety", "measurement", "test", "other":
		return ""
	default:
		return profileSlotForMissingField(field)
	}
}

func missingFieldSubject(field string, profiles []domain.SymptomProfile) string {
	matches := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		if profile.Name != "" && strings.Contains(field, profile.Name) {
			matches = append(matches, profile.Name)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	if len(matches) == 0 && len(profiles) == 1 {
		return profiles[0].Name
	}
	return ""
}

func (r *Runner) emergencyEscalationNode(_ context.Context, state workflowState) (workflowState, error) {
	next := cloneWorkflowState(state)
	for _, signal := range next.Session.RiskSignals {
		if signal.Priority == domain.PriorityUrgent {
			next.Session = r.escalateEmergency(next.Session, next.Session.RiskSignals)
			return next, nil
		}
	}
	return next, nil
}

func extractionInput(session domain.Session) string {
	var input strings.Builder
	input.WriteString(session.RawInput)
	if len(session.ClarificationTurns) == 0 {
		if session.Clarification != "" {
			input.WriteString("\n补充信息：")
			input.WriteString(session.Clarification)
		}
		return input.String()
	}
	for index, turn := range session.ClarificationTurns {
		fmt.Fprintf(&input, "\n第%d轮追问：\n", index+1)
		for questionIndex, question := range turn.Questions {
			fmt.Fprintf(&input, "%d. %s\n", questionIndex+1, question.Text)
		}
		input.WriteString("用户回答：")
		input.WriteString(turn.Answer)
	}
	return input.String()
}

func evidenceInput(session domain.Session) string {
	var input strings.Builder
	input.WriteString(session.RawInput)
	for _, turn := range session.ClarificationTurns {
		input.WriteByte('\n')
		input.WriteString(turn.Answer)
	}
	if len(session.ClarificationTurns) == 0 && session.Clarification != "" {
		input.WriteByte('\n')
		input.WriteString(session.Clarification)
	}
	return input.String()
}

func prioritizeQuestions(questions []domain.Question, limit int) []domain.Question {
	result := make([]domain.Question, 0, min(limit, len(questions)))
	for _, priority := range []domain.Priority{domain.PriorityUrgent, domain.PriorityHigh, domain.PriorityNormal} {
		for _, question := range questions {
			if question.Priority != priority {
				continue
			}
			result = append(result, question)
			if len(result) == limit {
				return result
			}
		}
	}
	return result
}

func (r *Runner) searchNode(ctx context.Context, state workflowState) (workflowState, error) {
	next := cloneWorkflowState(state)
	if next.Session.Status == domain.StatusWaitingClarification || next.Session.Status == domain.StatusEmergency {
		return next, nil
	}
	if !next.Session.AllowWebSearch {
		next.Session = r.addEvent(next.Session, "search", "skipped", "用户未启用联网资料")
		return next, nil
	}
	next.Session.Status = domain.StatusSearching
	if r.search == nil || len(r.allowedDomains) == 0 {
		next.Session = r.addEvent(next.Session, "search", "degraded", "联网资料服务未配置")
		return next, nil
	}

	queries := make([]string, 0, len(next.SearchQueries))
	seenQueries := make(map[string]struct{}, len(next.SearchQueries))
	for _, rawQuery := range next.SearchQueries {
		query := guard.GeneralizeSearchQuery(rawQuery, 2)
		if len([]rune(query)) < 2 || len(guard.ScanPII(query)) > 0 {
			continue
		}
		if _, exists := seenQueries[query]; exists {
			continue
		}
		seenQueries[query] = struct{}{}
		queries = append(queries, query)
	}

	type searchResult struct {
		sources []domain.Source
		err     error
	}
	results := make([]searchResult, len(queries))
	var searches sync.WaitGroup
	for index, query := range queries {
		searches.Add(1)
		go func(index int, query string) {
			defer searches.Done()
			results[index].sources, results[index].err = r.searchWithCache(ctx, query)
		}(index, query)
	}
	searches.Wait()

	sources := make([]domain.Source, 0, 8)
	seenSources := make(map[string]struct{})
	hadError := false
	for _, result := range results {
		if result.err != nil {
			hadError = true
			continue
		}
		for _, source := range result.sources {
			if _, exists := seenSources[source.URL]; exists || !guard.IsAllowedSourceURL(source.URL, r.allowedDomains) {
				continue
			}
			seenSources[source.URL] = struct{}{}
			sources = append(sources, source)
			if len(sources) == 8 {
				break
			}
		}
		if len(sources) == 8 {
			break
		}
	}
	next.Session.Sources = sources
	if hadError {
		next.Session = r.addEvent(next.Session, "search", "degraded", fmt.Sprintf("联网检索部分失败，保留 %d 条可信来源", len(sources)))
	} else {
		next.Session = r.addEvent(next.Session, "search", "completed", fmt.Sprintf("已找到 %d 条可信来源", len(sources)))
	}
	return next, nil
}

func (r *Runner) questionNode(ctx context.Context, state workflowState) (workflowState, error) {
	next := cloneWorkflowState(state)
	if next.Session.Status == domain.StatusWaitingClarification || next.Session.Status == domain.StatusEmergency {
		return next, nil
	}
	next.Session.Status = domain.StatusGenerating
	result, err := r.llm.GenerateQuestions(ctx, domain.QuestionInput{
		VisitGoal:       next.Session.VisitGoal,
		Facts:           append([]domain.Fact(nil), next.Session.Facts...),
		SymptomProfiles: cloneSymptomProfiles(next.Session.SymptomProfiles),
		Timeline:        append([]domain.TimelineEvent(nil), next.Session.Timeline...),
		RiskSignals:     append([]domain.RiskSignal(nil), next.Session.RiskSignals...),
		MissingFields:   append([]string(nil), next.Session.MissingFields...),
		Sources:         append([]domain.Source(nil), next.Session.Sources...),
		Uncertainties:   append([]domain.Uncertainty(nil), next.Session.Uncertainties...),
		Contradictions:  append([]domain.Contradiction(nil), next.Session.Contradictions...),
	})
	if err != nil {
		fallback := fallbackQuestionSet(next.Session)
		next.Session.Questions = fallback.Questions
		next.Session.ActionItems = fallback.ActionItems
		next.Session = r.addEvent(next.Session, "questions", "degraded", "问题生成失败，已使用安全的默认问题")
		return next, nil
	}
	next.Session.Questions = append([]domain.Question(nil), result.Questions...)
	next.Session.ActionItems = append([]domain.ActionItem(nil), result.ActionItems...)
	next.Session = r.addEvent(next.Session, "questions", "completed", fmt.Sprintf("已生成 %d 个就诊沟通问题", len(result.Questions)))
	return next, nil
}

func (r *Runner) questionValidatorNode(_ context.Context, state workflowState) (workflowState, error) {
	next := cloneWorkflowState(state)
	if next.Session.Status == domain.StatusWaitingClarification || next.Session.Status == domain.StatusEmergency {
		return next, nil
	}
	validated := validateQuestionSet(domain.QuestionSet{Questions: next.Session.Questions, ActionItems: next.Session.ActionItems})
	next.Session.Questions = filterRedundantDoctorQuestions(validated.Questions, next.Session.SymptomProfiles)
	next.Session.ActionItems = validated.ActionItems
	next.Session = r.addEvent(next.Session, "validator", "completed", "模型生成内容已通过独立边界校验")
	return next, nil
}

func filterRedundantDoctorQuestions(questions []domain.Question, profiles []domain.SymptomProfile) []domain.Question {
	filtered := make([]domain.Question, 0, len(questions))
	for _, question := range questions {
		if questionTargetsFilledSlot(question, profiles) {
			continue
		}
		filtered = append(filtered, question)
	}
	return filtered
}

func questionTargetsFilledSlot(question domain.Question, profiles []domain.SymptomProfile) bool {
	text := question.Text
	targeted := make(map[string]struct{})
	for _, profile := range profiles {
		if profile.Name != "" && strings.Contains(text, profile.Name) {
			targeted[normalizeFactEvidence(profile.Name)] = struct{}{}
		}
	}
	field := profileSlotForQuestion(question)
	if field == "" {
		return false
	}
	for _, profile := range profiles {
		if len(targeted) > 0 {
			if _, ok := targeted[normalizeFactEvidence(profile.Name)]; !ok {
				continue
			}
		}
		if !profileSlotFilled(profile, field) {
			return false
		}
	}
	return len(profiles) > 0
}

func profileSlotForQuestion(question domain.Question) string {
	text := question.Text
	switch {
	case question.Category == "duration" || strings.Contains(text, "持续多久") || strings.Contains(text, "持续多长"):
		return "duration"
	case question.Category == "frequency" || strings.Contains(text, "发作几次") || strings.Contains(text, "频率"):
		return "frequency"
	case question.Category == "timeline" || strings.Contains(text, "什么时候开始") || strings.Contains(text, "开始时间"):
		return "onset"
	case question.Category == "trigger" || strings.Contains(text, "什么情况下") || strings.Contains(text, "诱因"):
		return "trigger"
	default:
		return ""
	}
}

func profileSlotFilled(profile domain.SymptomProfile, field string) bool {
	switch field {
	case "duration":
		return strings.TrimSpace(profile.Duration) != ""
	case "frequency":
		return strings.TrimSpace(profile.Frequency) != ""
	case "onset":
		return strings.TrimSpace(profile.Onset) != ""
	case "trigger":
		return strings.TrimSpace(profile.Trigger) != "" || strings.TrimSpace(profile.RelievingFactors) != ""
	case "severity":
		return strings.TrimSpace(profile.Severity) != ""
	case "associated":
		return len(profile.AssociatedSymptoms) > 0
	default:
		return false
	}
}

func fallbackQuestionSet(session domain.Session) domain.QuestionSet {
	subject := symptomSubject(session)
	return domain.QuestionSet{
		Questions: []domain.Question{{
			Text: "我还需要向您补充哪些与本次就诊有关的信息？", Reason: "请医生指出当前资料中的重要空缺", Priority: domain.PriorityNormal, Category: "completeness",
		}},
		ActionItems: []domain.ActionItem{
			{Title: "记录" + subject + "变化", Detail: trackingDetail(session), Reason: "帮助医生理解症状的发生模式", Priority: domain.PriorityHigh, Category: "tracking"},
			{Title: "整理用药与过敏信息", Detail: "列出正在使用的药物、保健品和已知过敏情况", Reason: "减少就诊沟通遗漏", Priority: domain.PriorityNormal, Category: "medication_history"},
			{Title: "携带已有资料", Detail: "准备与本次情况相关的既往检查或就诊记录", Reason: "便于医生结合已有信息安排下一步", Priority: domain.PriorityNormal, Category: "records"},
		},
	}
}

func symptomSubject(session domain.Session) string {
	symptoms := make([]string, 0, 3)
	for _, profile := range session.SymptomProfiles {
		symptoms = append(symptoms, profile.Name)
		if len(symptoms) == 3 {
			break
		}
	}
	subject := "症状"
	if len(symptoms) > 0 {
		subject = strings.Join(symptoms, "、")
	}
	return subject
}

func trackingDetail(session domain.Session) string {
	type detailRule struct {
		label string
		terms []string
	}
	rules := []detailRule{
		{label: "开始时间", terms: []string{"开始", "起病"}},
		{label: "每次持续时长", terms: []string{"持续"}},
		{label: "发作频率", terms: []string{"频率", "次数"}},
		{label: "严重程度和日常影响", terms: []string{"程度", "严重", "影响"}},
		{label: "出现时的活动或诱因", terms: []string{"诱因", "活动", "体位"}},
		{label: "缓解因素", terms: []string{"缓解"}},
		{label: "伴随表现", terms: []string{"伴随"}},
		{label: "已有测量数值", terms: []string{"体温", "心率", "血压", "数值", "测量"}},
	}
	missing := strings.Join(session.MissingFields, " ")
	priorities := make([]string, 0, 5)
	for _, rule := range rules {
		if hasAny(missing, rule.terms...) {
			priorities = append(priorities, rule.label)
		}
		if len(priorities) == 5 {
			break
		}
	}
	if len(priorities) == 0 {
		return "记录每次出现的时间、持续时长、频率、当时活动和伴随表现"
	}
	return "优先补充尚未明确的" + strings.Join(priorities, "、") + "；每次出现时一并记录当时活动和伴随表现"
}

func (r *Runner) outputGuardNode(_ context.Context, state workflowState) (workflowState, error) {
	next := cloneWorkflowState(state)
	if next.Session.Status == domain.StatusWaitingClarification || next.Session.Status == domain.StatusEmergency {
		return next, nil
	}
	allowedURLs := make(map[string]struct{}, len(next.Session.Sources))
	for _, source := range next.Session.Sources {
		allowedURLs[source.URL] = struct{}{}
	}
	filtered := make([]domain.Question, 0, len(next.Session.Questions))
	for _, question := range next.Session.Questions {
		question = sanitizeQuestion(question)
		if !isSafeHealthQuestion(question.Text) {
			continue
		}
		if _, ok := allowedURLs[question.SourceURL]; !ok {
			question.SourceURL = ""
		}
		filtered = append(filtered, question)
	}
	if len(filtered) == 0 {
		filtered = fallbackQuestionSet(next.Session).Questions
	}
	next.Session.Questions = prioritizeQuestions(filtered, len(filtered))
	next.Session.ActionItems = safeActionItems(next.Session, next.Session.ActionItems)
	if len(next.Session.ActionItems) == 0 {
		next.Session.ActionItems = fallbackQuestionSet(next.Session).ActionItems
	}
	next.Session.Status = domain.StatusWaitingReview
	next.Session = r.addEvent(next.Session, "guard", "completed", "安全边界检查完成，等待用户核对")
	return next, nil
}

func isSafeClarificationPrompt(prompt domain.Question) bool {
	if prompt.Category != "" {
		switch prompt.Category {
		case "safety", "duration", "frequency", "trigger", "measurement", "timeline", "severity", "pattern", "associated_symptom", "history", "medication", "medication_history", "allergy", "test", "visit", "symptom", "missing_detail", "symptom_detail":
		default:
			return false
		}
	}
	return isSafeHealthQuestion(prompt.Text)
}

func isSafeHealthQuestion(text string) bool {
	if !guard.IsSafeQuestion(text) || hasAny(text, "密码", "验证码", "银行卡", "卡号", "账户", "账号", "资产", "收入", "工资", "信用卡", "支付", "转账", "身份证", "姓名", "电话", "住址", "地址", "邮箱", "微信", "QQ", "社交账号") {
		return false
	}
	return hasAny(text, "症状", "发作", "不适", "体温", "心率", "血压", "数值", "持续", "频率", "诱发", "缓解", "胸", "呼吸", "晕", "意识", "检查", "药", "过敏", "病史", "疾病", "疼", "痛", "头", "咳", "睡眠", "体重", "饮食", "活动", "情绪", "时间", "变化", "记录", "医生", "就诊", "排便", "月经")
}

func mergeGroundedFacts(prior, current []domain.Fact) []domain.Fact {
	return deduplicateFacts(append(append([]domain.Fact(nil), prior...), current...))
}

func reconcileMissingFields(fields []string, profiles []domain.SymptomProfile, session domain.Session) []string {
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" || missingFieldCovered(field, "", profiles, session) {
			continue
		}
		result = append(result, field)
	}
	return safeMissingFields(result)
}

func missingFieldLabels(items []domain.MissingField, legacy []string) []string {
	if len(items) > 0 {
		labels := make([]string, 0, len(items))
		for _, item := range items {
			if field := strings.TrimSpace(item.Field); field != "" {
				labels = append(labels, field)
			}
		}
		return labels
	}
	return append([]string(nil), legacy...)
}

func mergeMissingFieldItems(prior, current []domain.MissingField) []domain.MissingField {
	merged := append([]domain.MissingField(nil), prior...)
	byField := make(map[string]int, len(merged))
	for index, item := range merged {
		byField[normalizeFactEvidence(item.Field)] = index
	}
	for _, item := range current {
		key := normalizeFactEvidence(item.Field)
		if key == "" {
			continue
		}
		if index, exists := byField[key]; exists {
			if merged[index].Category == "" && item.Category != "" {
				merged[index].Category = item.Category
			}
			continue
		}
		byField[key] = len(merged)
		merged = append(merged, item)
	}
	return merged
}

func reconcileMissingFieldItems(items []domain.MissingField, profiles []domain.SymptomProfile, session domain.Session) []domain.MissingField {
	result := make([]domain.MissingField, 0, len(items))
	for _, item := range items {
		if missingFieldCovered(item.Field, item.Category, profiles, session) {
			continue
		}
		result = append(result, item)
	}
	return result
}

// clarificationSlotSpec is the deterministic write-back path for one question
// category. Every category the missing-field loop can produce must have a spec;
// see TestEveryMissingFieldCategoryIsMergeable.
type clarificationSlotSpec struct {
	symptom string // profile slot: duration / frequency / onset / trigger / severity / pattern / associated
	global  string // session slot: medications / allergies / history / safety
	valueRe *regexp.Regexp
}

func clarificationSlotSpecFor(category string) (clarificationSlotSpec, bool) {
	switch category {
	case "duration":
		return clarificationSlotSpec{symptom: "duration", valueRe: clarificationDurationValuePattern}, true
	case "frequency":
		return clarificationSlotSpec{symptom: "frequency", valueRe: frequencyValidationPattern}, true
	case "timeline":
		return clarificationSlotSpec{symptom: "onset", valueRe: onsetValidationPattern}, true
	case "trigger":
		return clarificationSlotSpec{symptom: "trigger"}, true
	case "severity":
		return clarificationSlotSpec{symptom: "severity"}, true
	case "pattern":
		return clarificationSlotSpec{symptom: "pattern"}, true
	case "associated_symptom", "symptom_detail", "associated":
		return clarificationSlotSpec{symptom: "associated"}, true
	case "medication", "medication_history":
		return clarificationSlotSpec{global: "medications"}, true
	case "allergy":
		return clarificationSlotSpec{global: "allergies"}, true
	case "history":
		return clarificationSlotSpec{global: "history"}, true
	case "measurement":
		return clarificationSlotSpec{global: "measurements"}, true
	case "test":
		return clarificationSlotSpec{global: "tests"}, true
	case "safety":
		return clarificationSlotSpec{global: "safety"}, true
	default:
		return clarificationSlotSpec{}, false
	}
}

func clarificationSlotValue(category, answer string) (string, string) {
	spec, ok := clarificationSlotSpecFor(category)
	if !ok || spec.symptom == "" {
		return "", ""
	}
	answer = strings.TrimSpace(answer)
	if answer == "" || isSkipAnswer(answer) || isUncertainAnswer(answer) {
		return "", ""
	}
	if spec.valueRe != nil {
		if match := spec.valueRe.FindString(answer); match != "" {
			return spec.symptom, strings.TrimSpace(match)
		}
		return "", ""
	}
	return spec.symptom, clipFreeTextValue(answer)
}

// clarificationSlotValues returns every profile slot an answer deterministically
// fills. The trigger question asks about both aggravating and relieving factors,
// so its answer is split into two slots.
func clarificationSlotValues(category, answer string) map[string]string {
	return clarificationSlotValuesScoped(category, answer, false)
}

// clarificationSlotValuesScoped is the same extraction, but in scoped mode a
// value that does not match its slot's canonical shape falls back to the
// clauses that cue that slot instead of the whole answer. mergeClarificationSlots
// uses scoped mode when one answer addresses several questions, so a free-text
// slot cannot absorb another question's clause.
func clarificationSlotValuesScoped(category, answer string, scoped bool) map[string]string {
	spec, ok := clarificationSlotSpecFor(category)
	if !ok || spec.symptom == "" {
		return nil
	}
	answer = strings.TrimSpace(answer)
	if answer == "" || isSkipAnswer(answer) || isUncertainAnswer(answer) {
		return nil
	}
	if spec.valueRe != nil {
		if match := spec.valueRe.FindString(answer); match != "" {
			return map[string]string{spec.symptom: strings.TrimSpace(match)}
		}
		// Temporal slots are canonical-or-empty: an answer outside the shared
		// vocabulary is left empty and never guessed from the surrounding
		// sentence, so a frequency answer cannot leak into onset or duration.
		return nil
	}
	if spec.symptom == "trigger" {
		triggerValue, relievingValue := splitTriggerAndRelieving(answer)
		values := make(map[string]string, 2)
		if triggerValue != "" {
			values["trigger"] = triggerValue
		}
		if relievingValue != "" {
			values["relieving"] = relievingValue
		}
		return values
	}
	if scoped {
		if value := cueScopedValue(spec.symptom, answer); value != "" {
			return map[string]string{spec.symptom: value}
		}
		return nil
	}
	return map[string]string{spec.symptom: clipFreeTextValue(answer)}
}

// cueScopedValue keeps only the answer clauses that cue a free-text slot, so a
// multi-question answer cannot push another question's clause into this slot.
// An answer with no matching clause yields an empty value.
func cueScopedValue(slot, answer string) string {
	cues := slotValueCues(slot)
	if len(cues) == 0 {
		return clipFreeTextValue(answer)
	}
	kept := make([]string, 0, 3)
	for _, clause := range splitAnswerClauses(answer) {
		if hasAny(clause, cues...) {
			kept = append(kept, clause)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return clipRunes(strings.Join(kept, "，"), 200)
}

// slotValueCues are the phrases that mark a clause as belonging to a given
// free-text slot. They are only used to prevent one free-text slot from
// absorbing another question's clause in a multi-question answer; temporal
// slots use the canonical vocabulary above instead.
func slotValueCues(slot string) []string {
	switch slot {
	case "severity":
		return []string{"影响", "程度", "严重", "剧烈", "厉害", "受不了", "无法", "没法", "不影响", "轻微", "特别", "非常"}
	case "pattern":
		return []string{"规律", "固定", "白天", "晚上", "夜间", "早晨", "早上", "下午", "间断", "持续", "一阵", "一直", "每天", "总是"}
	case "associated":
		return []string{"伴随", "伴有", "还有", "同时", "以及", "并且", "加上", "另外", "也"}
	default:
		return nil
	}
}

func clipFreeTextValue(answer string) string {
	clauses := splitAnswerClauses(answer)
	limit := min(3, len(clauses))
	return clipRunes(strings.Join(clauses[:limit], "，"), 200)
}

func splitTriggerAndRelieving(answer string) (string, string) {
	triggerClauses := make([]string, 0, 2)
	relievingClauses := make([]string, 0, 2)
	for _, clause := range splitAnswerClauses(answer) {
		switch {
		case hasAny(clause, "缓解", "好转", "减轻", "舒服", "改善", "恢复", "休息", "热敷", "按摩", "冰敷", "消失", "甩一甩", "活动活动"):
			relievingClauses = append(relievingClauses, clause)
		case hasAny(clause, "明显", "加重", "诱发", "出现", "发作", "活动", "姿势", "鼠标", "打字", "压", "抬", "转", "弯", "时", "后"):
			triggerClauses = append(triggerClauses, clause)
		default:
			triggerClauses = append(triggerClauses, clause)
		}
	}
	return clipRunes(strings.Join(triggerClauses, "，"), 160), clipRunes(strings.Join(relievingClauses, "，"), 160)
}

func splitAnswerClauses(answer string) []string {
	clauses := strings.FieldsFunc(answer, func(char rune) bool {
		return strings.ContainsRune("。！？!?；;，,、\n", char)
	})
	result := make([]string, 0, len(clauses))
	for _, clause := range clauses {
		if clause = strings.TrimSpace(clause); clause != "" {
			result = append(result, clause)
		}
	}
	return result
}

func clipRunes(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}

func mergeClarificationSlots(profiles []domain.SymptomProfile, turns []domain.ClarificationTurn) []domain.SymptomProfile {
	merged := cloneSymptomProfiles(profiles)
	for _, turn := range turns {
		scoped := len(turn.Questions) > 1
		for _, question := range turn.Questions {
			values := clarificationSlotValuesScoped(question.Category, turn.Answer, scoped)
			if len(values) == 0 {
				continue
			}
			targets := clarificationTargets(question.Text, merged)
			if len(targets) == 0 {
				continue
			}
			correction := containsAnyPhrase(turn.Answer, "更正", "为准", "说错了", "记错了")
			for _, index := range targets {
				for field, value := range values {
					if field == "associated" {
						if correction {
							merged[index].AssociatedSymptoms = nil
						}
						value = clipRunes(value, 120)
						if len(merged[index].AssociatedSymptoms) < 4 && !symptomCoveredByExisting(merged[index].AssociatedSymptoms, value) {
							merged[index].AssociatedSymptoms = appendUniqueStrings(merged[index].AssociatedSymptoms, []string{value})
						}
						continue
					}
					if !correction && strings.TrimSpace(profileFieldValue(merged[index], field)) != "" {
						continue
					}
					setProfileSlot(&merged[index], field, value)
				}
				merged[index].EvidenceQuotes = appendUniqueStrings(merged[index].EvidenceQuotes, shortestEvidenceClauses(turn.Answer, values))
			}
		}
	}
	return merged
}

func shortestEvidenceClauses(answer string, values map[string]string) []string {
	clauses := splitAnswerClauses(answer)
	result := make([]string, 0, len(values))
	for _, value := range values {
		shortest := ""
		for _, clause := range clauses {
			if isGroundedValue(clause, value) && (shortest == "" || len([]rune(clause)) < len([]rune(shortest))) {
				shortest = clause
			}
		}
		if shortest == "" {
			shortest = clipRunes(answer, 200)
		}
		result = append(result, strings.TrimSpace(shortest))
	}
	return result
}

func profileFieldValue(profile domain.SymptomProfile, field string) string {
	switch field {
	case "duration":
		return profile.Duration
	case "frequency":
		return profile.Frequency
	case "onset":
		return profile.Onset
	case "trigger":
		return profile.Trigger
	case "relieving":
		return profile.RelievingFactors
	case "severity":
		return profile.Severity
	case "pattern":
		return profile.Pattern
	default:
		return ""
	}
}

// applyGlobals unions the model's structured medication / allergy / history /
// test entries into the session slots. The model emits these from the full
// context (initial input plus every clarification answer), so the previous
// keyword parsing of free text is no longer needed.
func applyGlobals(session domain.Session, extraction domain.Extraction) domain.Session {
	session.Medications = appendUniqueStrings(session.Medications, globalValues(extraction.Medications))
	session.Allergies = appendUniqueStrings(session.Allergies, globalValues(extraction.Allergies))
	session.ChronicConditions = appendUniqueStrings(session.ChronicConditions, globalValues(extraction.ChronicConditions))
	session.TraumaHistory = appendUniqueStrings(session.TraumaHistory, globalValues(extraction.TraumaHistory))
	session.Measurements = appendUniqueStrings(session.Measurements, globalValues(extraction.Measurements))
	session.Tests = appendUniqueStrings(session.Tests, globalValues(extraction.Tests))
	for _, denied := range extraction.DeniedConditions {
		session.DeniedConditions = appendUniqueStrings(session.DeniedConditions, []string{"否认" + denialLabel(denied.Category) + "：" + denied.Value})
	}
	return session
}

func globalValues(items []domain.ExtractedGlobal) []string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		if value := strings.TrimSpace(item.Value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func denialLabel(category string) string {
	switch category {
	case "medication":
		return "用药"
	case "allergy":
		return "过敏"
	case "history":
		return "既往疾病"
	case "safety":
		return "安全相关"
	default:
		return "既往情况"
	}
}

// deriveGlobalsFromFacts unions the medication / allergy / history / test facts
// into the session slots, splitting explicit denials from positive entries. This
// is a deterministic complement to the model's structured global output, so the
// slots stay populated even when the model only expresses these as facts.
func deriveGlobalsFromFacts(session domain.Session, facts []domain.Fact) domain.Session {
	for _, fact := range facts {
		content := strings.TrimSpace(fact.Content)
		if content == "" {
			continue
		}
		switch fact.Category {
		case "medication":
			if isDenialFact(content) {
				session.DeniedConditions = appendUniqueStrings(session.DeniedConditions, []string{"否认用药：" + content})
			} else {
				session.Medications = appendUniqueStrings(session.Medications, []string{content})
			}
		case "allergy":
			if isDenialFact(content) {
				session.DeniedConditions = appendUniqueStrings(session.DeniedConditions, []string{"否认过敏：" + content})
			} else {
				session.Allergies = appendUniqueStrings(session.Allergies, []string{content})
			}
		case "history":
			if isDenialFact(content) {
				session.DeniedConditions = appendUniqueStrings(session.DeniedConditions, []string{"否认既往疾病：" + content})
			} else if hasAny(content, "外伤", "骨折", "手术", "摔伤", "扭伤") {
				session.TraumaHistory = appendUniqueStrings(session.TraumaHistory, []string{content})
			} else {
				session.ChronicConditions = appendUniqueStrings(session.ChronicConditions, []string{content})
			}
		case "test":
			if isDenialFact(content) {
				session.DeniedConditions = appendUniqueStrings(session.DeniedConditions, []string{"否认检查：" + content})
			} else {
				session.Tests = appendUniqueStrings(session.Tests, []string{content})
			}
		}
	}
	return session
}

// isDenialFact classifies a fact by its leading clause, so "就是前几年查出
// 幽门螺杆菌，没再复查" is a positive history entry (its negation is only a
// trailing note) while "以前没有胃病" is a denial.
func isDenialFact(content string) bool {
	first := strings.TrimSpace(content)
	if index := strings.IndexAny(first, "，,。；;"); index >= 0 {
		first = first[:index]
	}
	return clauseDenies(strings.TrimSpace(first))
}

// symptomCoveredByExisting avoids duplicating an extracted symptom with a
// longer phrase that already contains it (e.g. "头晕" already covers
// "发作时还伴有头晕").
func symptomCoveredByExisting(existing []string, value string) bool {
	normalized := normalizeFactEvidence(value)
	for _, symptom := range existing {
		key := normalizeFactEvidence(symptom)
		if key == "" || len([]rune(key)) < 2 {
			continue
		}
		if strings.Contains(normalized, key) || strings.Contains(key, normalized) {
			return true
		}
	}
	return false
}

func clauseDenies(clause string) bool {
	return containsAnyPhrase(clause, "否认", "都没有", "都没有过", "从没有", "从来没", "没有", "没", "无", "未", "不", "正常", "差不多", "没吃", "没用", "没在", "没再", "不过敏", "没过敏")
}

func clarificationTargets(question string, profiles []domain.SymptomProfile) []int {
	result := make([]int, 0, len(profiles))
	for index, profile := range profiles {
		if profile.Name != "" && strings.Contains(question, profile.Name) {
			result = append(result, index)
		}
	}
	if len(result) > 1 {
		return nil
	}
	if len(result) == 0 && len(profiles) == 1 {
		return []int{0}
	}
	return result
}

func setProfileSlot(profile *domain.SymptomProfile, field, value string) {
	switch field {
	case "duration":
		profile.Duration = value
	case "frequency":
		profile.Frequency = value
	case "onset":
		profile.Onset = value
	case "trigger":
		profile.Trigger = value
	case "relieving":
		profile.RelievingFactors = value
	case "severity":
		profile.Severity = value
	case "pattern":
		profile.Pattern = value
	}
}

func missingFieldCovered(field, category string, profiles []domain.SymptomProfile, session domain.Session) bool {
	slot := missingFieldSlot(field, category)
	if slot == "" {
		switch category {
		case "medication":
			return len(session.Medications) > 0 || deniedLabelPresent(session, "用药")
		case "allergy":
			return len(session.Allergies) > 0 || deniedLabelPresent(session, "过敏")
		case "history":
			return len(session.ChronicConditions) > 0 || len(session.TraumaHistory) > 0 || deniedLabelPresent(session, "既往疾病")
		case "safety":
			return len(session.SafetyNotes) > 0 || deniedLabelPresent(session, "安全相关")
		case "measurement":
			return len(session.Measurements) > 0 || deniedLabelPresent(session, "测量")
		case "test":
			return len(session.Tests) > 0 || deniedLabelPresent(session, "检查")
		default:
			return globalFieldCovered(field, session)
		}
	}
	targeted := make(map[string]struct{})
	for _, profile := range profiles {
		if profile.Name != "" && strings.Contains(field, profile.Name) {
			targeted[normalizeFactEvidence(profile.Name)] = struct{}{}
		}
	}
	for _, profile := range profiles {
		if len(targeted) > 0 {
			if _, ok := targeted[normalizeFactEvidence(profile.Name)]; !ok {
				continue
			}
		}
		if !profileSlotFilled(profile, slot) {
			return false
		}
		if strings.Contains(field, "缓解") || strings.Contains(field, "减轻") {
			if strings.TrimSpace(profile.RelievingFactors) == "" {
				return false
			}
		}
		if strings.Contains(field, "程度") || strings.Contains(field, "严重") || strings.Contains(field, "影响") {
			if strings.TrimSpace(profile.Severity) == "" {
				return false
			}
		}
		if strings.Contains(field, "伴随") || strings.Contains(field, "相关症状") {
			if len(profile.AssociatedSymptoms) == 0 {
				return false
			}
		}
	}
	return len(profiles) > 0
}

// globalFieldCovered treats medication / allergy / history fields as covered
// once the user answered them, including explicit denials such as "没有糖尿病".
func globalFieldCovered(field string, session domain.Session) bool {
	switch {
	case hasAny(field, "药"):
		return len(session.Medications) > 0 || deniedLabelPresent(session, "用药")
	case hasAny(field, "过敏"):
		return len(session.Allergies) > 0 || deniedLabelPresent(session, "过敏")
	case hasAny(field, "史", "既往", "糖尿病", "甲状腺", "关节炎", "外伤", "慢性病", "高血压", "疾病", "手术"):
		return len(session.ChronicConditions) > 0 || len(session.TraumaHistory) > 0 || deniedLabelPresent(session, "既往疾病")
	default:
		return false
	}
}

func deniedLabelPresent(session domain.Session, label string) bool {
	prefix := "否认" + label
	for _, entry := range session.DeniedConditions {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func profileSlotForMissingField(field string) string {
	switch {
	case strings.Contains(field, "开始") || strings.Contains(field, "起病"):
		return "onset"
	case strings.Contains(field, "持续"):
		return "duration"
	case strings.Contains(field, "频率") || strings.Contains(field, "次数"):
		return "frequency"
	case strings.Contains(field, "诱因") || strings.Contains(field, "加重") || strings.Contains(field, "缓解") || strings.Contains(field, "减轻") ||
		strings.Contains(field, "休息") || strings.Contains(field, "热敷") || strings.Contains(field, "护腕") || strings.Contains(field, "按摩") || strings.Contains(field, "冰敷"):
		return "trigger"
	case strings.Contains(field, "程度") || strings.Contains(field, "严重") || strings.Contains(field, "影响"):
		return "severity"
	case strings.Contains(field, "伴随") || strings.Contains(field, "相关症状"):
		return "associated"
	default:
		return ""
	}
}

// latestTurnCorrectedSlots returns the profile slots the user explicitly
// corrected this round. A correction only authorizes overwriting the slots the
// latest turn actually asked about, so "更正一下持续时间" cannot overwrite an
// unrelated onset the re-extraction happened to move.
func latestTurnCorrectedSlots(turns []domain.ClarificationTurn) map[string]bool {
	result := make(map[string]bool)
	if len(turns) == 0 {
		return result
	}
	last := turns[len(turns)-1]
	if !containsAnyPhrase(last.Answer, "更正", "为准", "说错了", "记错了") {
		return result
	}
	for _, question := range last.Questions {
		switch question.Category {
		case "duration":
			result["duration"] = true
		case "frequency":
			result["frequency"] = true
		case "timeline":
			result["onset"] = true
		case "trigger":
			result["trigger"] = true
			result["relieving"] = true
		case "severity":
			result["severity"] = true
		case "pattern":
			result["pattern"] = true
		case "associated_symptom", "symptom_detail", "associated", "missing_detail":
			result["associated"] = true
		}
	}
	return result
}

func mergeSymptomProfiles(prior, current []domain.SymptomProfile, corrected map[string]bool) []domain.SymptomProfile {
	merged := cloneSymptomProfiles(prior)
	byName := make(map[string]int, len(merged))
	for index, profile := range merged {
		byName[normalizeFactEvidence(profile.Name)] = index
	}
	for _, incoming := range current {
		key := normalizeFactEvidence(incoming.Name)
		if key == "" {
			continue
		}
		if index, exists := byName[key]; exists {
			merged[index] = mergeSymptomProfile(merged[index], incoming, corrected)
			continue
		}
		byName[key] = len(merged)
		merged = append(merged, incoming)
	}
	return merged
}

func mergeSymptomProfile(prior, incoming domain.SymptomProfile, corrected map[string]bool) domain.SymptomProfile {
	merged := prior
	if strings.TrimSpace(incoming.Name) != "" {
		merged.Name = incoming.Name
	}
	if strings.TrimSpace(merged.SourceQuote) == "" && strings.TrimSpace(incoming.SourceQuote) != "" {
		merged.SourceQuote = incoming.SourceQuote
	} else if normalizeFactEvidence(incoming.SourceQuote) != normalizeFactEvidence(merged.SourceQuote) {
		merged.EvidenceQuotes = appendUniqueStrings(merged.EvidenceQuotes, []string{incoming.SourceQuote})
	}
	// Re-extraction may misassign a value to another slot; once a slot has a
	// value it stays unless the user explicitly corrected that slot this round.
	slots := []struct {
		ptr      *string
		name     string
		incoming string
	}{
		{&merged.Onset, "onset", incoming.Onset},
		{&merged.Duration, "duration", incoming.Duration},
		{&merged.Frequency, "frequency", incoming.Frequency},
		{&merged.Severity, "severity", incoming.Severity},
		{&merged.Pattern, "pattern", incoming.Pattern},
		{&merged.Trigger, "trigger", incoming.Trigger},
		{&merged.RelievingFactors, "relieving", incoming.RelievingFactors},
	}
	for _, slot := range slots {
		if strings.TrimSpace(slot.incoming) == "" {
			continue
		}
		if !corrected[slot.name] && strings.TrimSpace(*slot.ptr) != "" {
			continue
		}
		*slot.ptr = slot.incoming
	}
	merged.AssociatedSymptoms = appendUniqueStrings(merged.AssociatedSymptoms, incoming.AssociatedSymptoms)
	merged.EvidenceQuotes = appendUniqueStrings(merged.EvidenceQuotes, incoming.EvidenceQuotes)
	return merged
}

func appendUniqueStrings(prior, incoming []string) []string {
	result := append([]string(nil), prior...)
	seen := make(map[string]struct{}, len(result))
	for _, value := range result {
		seen[normalizeFactEvidence(value)] = struct{}{}
	}
	for _, value := range incoming {
		value = strings.TrimSpace(value)
		key := normalizeFactEvidence(value)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func mergeTimeline(prior, current []domain.TimelineEvent) []domain.TimelineEvent {
	merged := append(append([]domain.TimelineEvent(nil), prior...), current...)
	result := make([]domain.TimelineEvent, 0, len(merged))
	seen := make(map[string]struct{}, len(merged))
	for _, event := range merged {
		key := normalizeFactEvidence(event.TimeLabel + "\x1f" + event.Event + "\x1f" + event.SourceQuote)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, event)
	}
	return result
}

func deduplicateFacts(facts []domain.Fact) []domain.Fact {
	result := make([]domain.Fact, 0, len(facts))
	seen := make(map[string]struct{}, len(facts))
	for _, fact := range facts {
		if isClarificationControlAnswer(fact.SourceQuote) {
			continue
		}
		key := normalizeFactEvidence(fact.Content + "\x1f" + fact.SourceQuote)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, fact)
	}
	return result
}

func isClarificationControlAnswer(value string) bool {
	value = strings.TrimSpace(strings.TrimPrefix(value, "补充信息："))
	for _, phrase := range []string{"跳过", "不清楚", "不知道", "无法提供", "记不清", "暂不回答"} {
		if strings.HasPrefix(value, phrase) {
			return true
		}
	}
	return false
}

func normalizeFactEvidence(value string) string {
	return strings.ToLower(strings.Map(func(char rune) rune {
		if unicode.IsSpace(char) || unicode.IsPunct(char) {
			return -1
		}
		return char
	}, value))
}

func groundedSymptomProfiles(input string, profiles []domain.SymptomProfile, turns []domain.ClarificationTurn) []domain.SymptomProfile {
	result := make([]domain.SymptomProfile, 0, len(profiles))
	usedQuotes := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		profile.Name = strings.TrimSpace(profile.Name)
		profile.SourceQuote = strings.TrimSpace(profile.SourceQuote)
		if profile.Name == "" || !isGroundedQuote(input, profile.SourceQuote) {
			continue
		}
		if !isGroundedProfileName(profile.SourceQuote, profile.Name) {
			quoteKey := normalizeFactEvidence(profile.SourceQuote)
			if _, duplicate := usedQuotes[quoteKey]; duplicate {
				// The same quote already produced a profile; this one carries a
				// wrong name and is a duplicate, not a real second symptom.
				continue
			}
			// A grounded quote with an ungrounded name is a paraphrase (e.g.
			// 胃这里会隐隐地疼 -> 胃痛). Keep the profile under the verbatim
			// quote as its name instead of dropping the whole symptom.
			profile.Name = profile.SourceQuote
		}
		usedQuotes[normalizeFactEvidence(profile.SourceQuote)] = struct{}{}
		evidenceQuotes := make([]string, 0, len(profile.EvidenceQuotes))
		for _, quote := range profile.EvidenceQuotes {
			quote = strings.TrimSpace(quote)
			if quote != "" && isGroundedQuote(input, quote) {
				evidenceQuotes = append(evidenceQuotes, quote)
			}
		}
		profile.EvidenceQuotes = evidenceQuotes
		profile.Onset = typedProfileValue(groundedProfileValue(profile, profile.Onset, "timeline", turns, len(profiles) == 1), onsetValidationPattern)
		profile.Duration = typedProfileValue(groundedProfileValue(profile, profile.Duration, "duration", turns, len(profiles) == 1), clarificationDurationValuePattern)
		profile.Frequency = typedProfileValue(groundedProfileValue(profile, profile.Frequency, "frequency", turns, len(profiles) == 1), frequencyValidationPattern)
		profile.Severity = groundedProfileValue(profile, profile.Severity, "severity", turns, len(profiles) == 1)
		profile.Pattern = groundedProfileValue(profile, profile.Pattern, "pattern", turns, len(profiles) == 1)
		profile.Trigger = groundedProfileValue(profile, profile.Trigger, "trigger", turns, len(profiles) == 1)
		profile.RelievingFactors = groundedProfileValue(profile, profile.RelievingFactors, "trigger", turns, len(profiles) == 1)
		// A slot value must point in the right direction: a relieving factor in
		// the trigger slot (or vice versa) is moved instead of being dropped.
		if triggerValue, relievingValue := splitTriggerAndRelieving(profile.Trigger); triggerValue == "" && relievingValue != "" {
			if strings.TrimSpace(profile.RelievingFactors) == "" {
				profile.RelievingFactors = relievingValue
			}
			profile.Trigger = ""
		}
		if triggerValue, relievingValue := splitTriggerAndRelieving(profile.RelievingFactors); relievingValue == "" && triggerValue != "" {
			if strings.TrimSpace(profile.Trigger) == "" {
				profile.Trigger = triggerValue
			}
			profile.RelievingFactors = ""
		}
		associated := make([]string, 0, len(profile.AssociatedSymptoms))
		for _, symptom := range profile.AssociatedSymptoms {
			if value := groundedProfileValue(profile, symptom, "associated_symptom", turns, len(profiles) == 1); value != "" {
				associated = append(associated, value)
			}
		}
		profile.AssociatedSymptoms = associated
		result = append(result, profile)
	}
	return result
}

// typedProfileValue keeps only the part of a grounded value that matches its
// slot shape (e.g. "三周" in a duration slot is dropped entirely, and
// "从三个月前开始" is trimmed to "三个月前"). An empty slot reads as unknown,
// which is safer than carrying a misassigned value into the visit summary.
func typedProfileValue(value string, pattern *regexp.Regexp) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.TrimSpace(pattern.FindString(value))
}

func groundedProfileValue(profile domain.SymptomProfile, value, category string, turns []domain.ClarificationTurn, allowImplicitSubject bool) string {
	if grounded := groundedValue(profile.SourceQuote, value); grounded != "" {
		return grounded
	}
	value = strings.TrimSpace(value)
	groundedInAdditionalEvidence := false
	for _, quote := range profile.EvidenceQuotes {
		if isGroundedValue(quote, value) {
			groundedInAdditionalEvidence = true
			if evidenceLinksValueToProfile(quote, value, profile.Name) || strings.Contains(normalizeFactEvidence(quote), normalizeFactEvidence(profile.Name)) {
				return value
			}
			break
		}
	}
	if !groundedInAdditionalEvidence {
		return ""
	}
	answerContainsValue := false
	for _, turn := range turns {
		if valueIsUncertain(turn.Answer, value) {
			return ""
		}
		if isGroundedValue(turn.Answer, value) {
			answerContainsValue = true
			break
		}
	}
	if !answerContainsValue {
		return ""
	}
	for _, turn := range turns {
		for _, question := range turn.Questions {
			if question.Category == category && (allowImplicitSubject || strings.Contains(question.Text, profile.Name)) {
				return value
			}
		}
	}
	return ""
}

func valueIsUncertain(answer, value string) bool {
	for _, pivot := range []string{"但是", "不过", "然而", "但"} {
		answer = strings.ReplaceAll(answer, pivot, "。")
	}
	for _, clause := range strings.FieldsFunc(answer, func(char rune) bool {
		return strings.ContainsRune("。！？!?；;，,\n", char)
	}) {
		if strings.Contains(clause, value) {
			return isUncertainAnswer(clause)
		}
	}
	return false
}

func evidenceLinksValueToProfile(quote, value, profileName string) bool {
	if hasAny(quote, "这些情况", "这些症状", "上述情况", "上述症状", "以上情况", "以上症状") {
		return true
	}
	for _, clause := range strings.FieldsFunc(quote, func(char rune) bool {
		return strings.ContainsRune("。！？!?；;，,、\n", char)
	}) {
		if isGroundedValue(clause, value) && isGroundedValue(clause, profileName) {
			return true
		}
	}
	return false
}

func groundedTimeline(input string, timeline []domain.TimelineEvent) []domain.TimelineEvent {
	result := make([]domain.TimelineEvent, 0, len(timeline))
	for _, event := range timeline {
		event.SourceQuote = strings.TrimSpace(event.SourceQuote)
		if !isGroundedQuote(input, event.SourceQuote) {
			continue
		}
		event.TimeLabel = groundedValue(event.SourceQuote, event.TimeLabel)
		event.Event = groundedValue(event.SourceQuote, event.Event)
		if event.Event == "" {
			event.Event = event.SourceQuote
		}
		result = append(result, event)
	}
	return result
}

func groundedValue(quote, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !isGroundedValueWithVariants(quote, value) {
		return ""
	}
	return value
}

func isGroundedValueWithVariants(quote, value string) bool {
	if isGroundedValue(quote, value) {
		return true
	}
	for _, suffix := range []string{"前", "来"} {
		if strings.HasSuffix(value, suffix) && isGroundedValue(quote, strings.TrimSuffix(value, suffix)) {
			return true
		}
	}
	return false
}

func isGroundedValue(quote, value string) bool {
	normalizedValue := normalizeFactEvidence(value)
	return len([]rune(normalizedValue)) >= 1 && strings.Contains(normalizeFactEvidence(quote), normalizedValue)
}

func isGroundedProfileName(quote, name string) bool {
	if isGroundedValue(quote, name) {
		return true
	}
	normalizedName := []rune(normalizeFactEvidence(name))
	normalizedQuote := normalizeFactEvidence(quote)
	if len(normalizedName) < 3 {
		return false
	}
	nameIndex := 0
	for _, char := range []rune(normalizedQuote) {
		if nameIndex < len(normalizedName) && char == normalizedName[nameIndex] {
			nameIndex++
		}
	}
	return nameIndex == len(normalizedName)
}

func hasAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func isGroundedQuote(input, quote string) bool {
	accepted, _ := guard.PartitionGroundedFacts(input, []domain.Fact{{SourceQuote: quote}})
	return len(accepted) == 1
}

func sanitizeQuestion(question domain.Question) domain.Question {
	question.Text = strings.TrimSpace(question.Text)
	question.Reason = strings.TrimSpace(question.Reason)
	question.Category = strings.TrimSpace(question.Category)
	question.Priority = normalizePriority(question.Priority)
	if len([]rune(question.Reason)) > 240 || guard.ContainsMedicalOverreach(question.Reason) || len(guard.ScanPII(question.Reason)) > 0 {
		question.Reason = ""
	}
	return question
}

func safeMissingFields(fields []string) []string {
	result := make([]string, 0, min(len(fields), 20))
	seen := make(map[string]struct{})
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" || len([]rune(field)) > 160 || guard.ContainsMedicalOverreach(field) || len(guard.ScanPII(field)) > 0 {
			continue
		}
		key := normalizeFactEvidence(field)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, field)
		if len(result) == 20 {
			break
		}
	}
	return result
}

func safeActionItems(session domain.Session, items []domain.ActionItem) []domain.ActionItem {
	requested := map[string]bool{"tracking": true, "medication_history": true, "records": true}
	for _, item := range items {
		category := strings.TrimSpace(item.Category)
		if isAllowedActionCategory(category) {
			requested[category] = true
		}
	}
	if len(session.RiskSignals) > 0 {
		requested["safety"] = true
	} else {
		delete(requested, "safety")
	}
	result := make([]domain.ActionItem, 0, len(requested))
	for _, category := range []string{"safety", "tracking", "medication_history", "records", "visit"} {
		if requested[category] {
			result = append(result, deterministicActionItem(session, category))
		}
	}
	return result
}

func deterministicActionItem(session domain.Session, category string) domain.ActionItem {
	switch category {
	case "safety":
		return domain.ActionItem{Title: "优先处理安全信号", Detail: session.RiskSignals[0].Guidance, Reason: "已明确提到可能影响就医紧迫程度的伴随表现", Priority: domain.PriorityUrgent, Category: category}
	case "medication_history":
		return domain.ActionItem{Title: "整理用药与过敏信息", Detail: "列出正在使用的药物、保健品及已知过敏情况，不自行调整用药", Reason: "帮助医生了解可能影响检查与处置安排的信息", Priority: domain.PriorityNormal, Category: category}
	case "records":
		return domain.ActionItem{Title: "准备已有资料", Detail: "携带与本次情况相关的既往检查、测量记录和病历资料", Reason: "减少遗漏并让就诊信息更连贯", Priority: domain.PriorityNormal, Category: category}
	case "visit":
		return domain.ActionItem{Title: "确认就诊安排", Detail: "提前确认就诊时间、科室和已有资料的携带方式", Reason: "减少到院后的重复准备", Priority: domain.PriorityNormal, Category: category}
	default:
		return domain.ActionItem{Title: "记录" + symptomSubject(session) + "变化", Detail: trackingDetail(session), Reason: "连续的客观记录更利于就诊沟通", Priority: domain.PriorityHigh, Category: "tracking"}
	}
}

func isAllowedActionCategory(category string) bool {
	switch category {
	case "tracking", "medication_history", "records", "safety", "visit":
		return true
	default:
		return false
	}
}

func normalizePriority(priority domain.Priority) domain.Priority {
	switch priority {
	case domain.PriorityUrgent, domain.PriorityHigh, domain.PriorityNormal:
		return priority
	default:
		return domain.PriorityNormal
	}
}

func (r *Runner) addEvent(session domain.Session, step, status, message string) domain.Session {
	next := cloneSession(session)
	next.Events = append(next.Events, domain.AgentEvent{Step: step, Status: status, Message: message, CreatedAt: r.now()})
	return next
}

func cloneWorkflowState(source workflowState) workflowState {
	return workflowState{
		Session:           cloneSession(source.Session),
		SearchQueries:     append([]string(nil), source.SearchQueries...),
		priorFacts:        append([]domain.Fact(nil), source.priorFacts...),
		priorProfiles:     cloneSymptomProfiles(source.priorProfiles),
		priorTimeline:     append([]domain.TimelineEvent(nil), source.priorTimeline...),
		priorMissing:      append([]string(nil), source.priorMissing...),
		priorMissingItems: append([]domain.MissingField(nil), source.priorMissingItems...),
		priorGoal:         source.priorGoal,
	}
}
