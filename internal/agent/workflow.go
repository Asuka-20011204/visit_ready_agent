package agent

import (
	"context"
	"fmt"
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
		next.priorGoal = next.Session.VisitGoal
	}

	input := extractionInput(next.Session)
	extraction, err := r.llm.Extract(ctx, input)
	if err != nil {
		if len(next.priorFacts) > 0 && !isRetryableRunError(err) {
			next.Session = r.addEvent(next.Session, "extract", "degraded", "本轮提取未能形成新的可核对事实，已保留已确认信息")
			return next, nil
		}
		return workflowState{}, fmt.Errorf("extract facts: %w", err)
	}
	next.Session.VisitGoal = strings.TrimSpace(extraction.VisitGoal)
	next.Session.Facts = append([]domain.Fact(nil), extraction.Facts...)
	next.Session.SymptomProfiles = cloneSymptomProfiles(extraction.SymptomProfiles)
	next.Session.Timeline = append([]domain.TimelineEvent(nil), extraction.Timeline...)
	next.Session.RiskSignals = append([]domain.RiskSignal(nil), extraction.RiskSignals...)
	next.Session.MissingFields = safeMissingFields(extraction.MissingFields)
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
		ClarificationQuestions: next.Session.ClarificationQuestions,
		ClarificationPrompts:   next.Session.ClarificationPrompts,
		SearchQueries:          next.SearchQueries, Uncertainties: next.Session.Uncertainties,
		Contradictions: next.Session.Contradictions, ConversationSummary: next.Session.ConversationSummary,
	}, next.Session.ClarificationTurns)
	if len(validated.Facts) == 0 && len(next.priorFacts) == 0 {
		return workflowState{}, fmt.Errorf("all %d extracted facts failed evidence validation", rejected)
	}
	next.Session.Facts = mergeGroundedFacts(next.priorFacts, validated.Facts)
	next.Session.SymptomProfiles = mergeSymptomProfiles(next.priorProfiles, validated.SymptomProfiles)
	next.Session.SymptomProfiles = mergeClarificationSlots(next.Session.SymptomProfiles, next.Session.ClarificationTurns)
	next.Session.Timeline = mergeTimeline(next.priorTimeline, validated.Timeline)
	missing := append(append([]string(nil), next.priorMissing...), validated.MissingFields...)
	next.Session.MissingFields = reconcileMissingFields(missing, next.Session.SymptomProfiles)
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
	hasContradictions := len(contradictions) > 0

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
	for _, contradiction := range next.Session.Contradictions {
		prompt := domain.Question{Text: contradiction.ClarifyingQuestion, Reason: "Agent 发现两段回答指向不同信息，先确认原话再生成最终摘要", Priority: contradiction.Priority, Category: "missing_detail"}
		key := normalizeFactEvidence(prompt.Text)
		if _, exists := seenPrompts[key]; !exists && isSafeClarificationPrompt(prompt) {
			safePrompts = append(safePrompts, prompt)
			seenPrompts[key] = struct{}{}
		}
	}
	if next.Session.ClarificationCount > 0 {
		for _, field := range next.Session.MissingFields {
			prompt := clarificationPromptForMissingField(field, next.Session)
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
	}
	safePrompts = prioritizeQuestions(safePrompts, 3)
	next.Session.ClarificationPrompts = safePrompts
	next.Session.ClarificationQuestions = make([]string, 0, len(safePrompts))
	for _, prompt := range safePrompts {
		next.Session.ClarificationQuestions = append(next.Session.ClarificationQuestions, prompt.Text)
	}
	next.Session = r.addEvent(next.Session, "evidence", "completed", fmt.Sprintf("%d 项事实通过原文校验", len(next.Session.Facts)))

	if hasContradictions && len(next.Session.ClarificationQuestions) > 0 {
		next.Session.Status = domain.StatusWaitingClarification
		next.Session = r.addEvent(next.Session, "clarification", "waiting", "发现不同说法，需要用户确认后继续")
	} else if next.Session.ClarificationCount < maxClarificationRounds && len(next.Session.ClarificationQuestions) > 0 {
		next.Session.Status = domain.StatusWaitingClarification
		next.Session = r.addEvent(next.Session, "clarification", "waiting", "需要用户补充一轮信息")
	}
	return next, nil
}

func clarificationPromptForMissingField(field string, session domain.Session) domain.Question {
	subject := missingFieldSubject(field, session.SymptomProfiles)
	if subject == "" {
		return domain.Question{}
	}
	priority := uncertaintyPriority(field)
	switch profileSlotForMissingField(field) {
	case "duration":
		return domain.Question{Text: subject + "每次大约持续多久？", Reason: "补全仍未确认的持续时间", Priority: priority, Category: "duration"}
	case "frequency":
		return domain.Question{Text: subject + "一天或一周大约出现几次？", Reason: "补全仍未确认的发生频率", Priority: priority, Category: "frequency"}
	case "onset":
		return domain.Question{Text: subject + "大约从什么时候开始？", Reason: "补全仍未确认的开始时间", Priority: priority, Category: "timeline"}
	case "trigger":
		return domain.Question{Text: subject + "在什么情况下更明显，怎样会缓解？", Reason: "补全仍未确认的诱因或缓解因素", Priority: priority, Category: "trigger"}
	case "severity":
		return domain.Question{Text: subject + "目前对日常活动有什么影响？", Reason: "补全仍未确认的日常影响", Priority: priority, Category: "severity"}
	case "associated":
		return domain.Question{Text: subject + "出现时还伴有哪些不适？", Reason: "补全仍未确认的伴随表现", Priority: priority, Category: "associated_symptom"}
	default:
		return domain.Question{}
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

func reconcileMissingFields(fields []string, profiles []domain.SymptomProfile) []string {
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" || missingFieldCovered(field, profiles) {
			continue
		}
		result = append(result, field)
	}
	return safeMissingFields(result)
}

func mergeClarificationSlots(profiles []domain.SymptomProfile, turns []domain.ClarificationTurn) []domain.SymptomProfile {
	merged := cloneSymptomProfiles(profiles)
	for _, turn := range turns {
		for _, question := range turn.Questions {
			field, value := clarificationSlotValue(question.Category, turn.Answer)
			if field == "" || value == "" || valueIsUncertain(turn.Answer, value) {
				continue
			}
			targets := clarificationTargets(question.Text, merged)
			for _, index := range targets {
				if !profileSlotFilled(merged[index], field) || containsAnyPhrase(turn.Answer, "更正", "为准", "说错了", "记错了") {
					setProfileSlot(&merged[index], field, value)
				}
				merged[index].EvidenceQuotes = appendUniqueStrings(merged[index].EvidenceQuotes, []string{turn.Answer})
			}
		}
	}
	return merged
}

func clarificationSlotValue(category, answer string) (string, string) {
	switch category {
	case "duration":
		return "duration", strings.TrimSpace(clarificationDurationValuePattern.FindString(answer))
	case "frequency":
		return "frequency", strings.TrimSpace(frequencyValuePattern.FindString(answer))
	case "timeline":
		return "onset", strings.TrimSpace(onsetValuePattern.FindString(answer))
	default:
		return "", ""
	}
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
	}
}

func missingFieldCovered(field string, profiles []domain.SymptomProfile) bool {
	targeted := make(map[string]struct{})
	for _, profile := range profiles {
		if profile.Name != "" && strings.Contains(field, profile.Name) {
			targeted[normalizeFactEvidence(profile.Name)] = struct{}{}
		}
	}
	slot := profileSlotForMissingField(field)
	if slot == "" {
		return false
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

func profileSlotForMissingField(field string) string {
	switch {
	case strings.Contains(field, "开始") || strings.Contains(field, "起病"):
		return "onset"
	case strings.Contains(field, "持续"):
		return "duration"
	case strings.Contains(field, "频率") || strings.Contains(field, "次数"):
		return "frequency"
	case strings.Contains(field, "诱因") || strings.Contains(field, "加重") || strings.Contains(field, "缓解") || strings.Contains(field, "减轻"):
		return "trigger"
	case strings.Contains(field, "程度") || strings.Contains(field, "严重") || strings.Contains(field, "影响"):
		return "severity"
	case strings.Contains(field, "伴随") || strings.Contains(field, "相关症状"):
		return "associated"
	default:
		return ""
	}
}

func mergeSymptomProfiles(prior, current []domain.SymptomProfile) []domain.SymptomProfile {
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
			merged[index] = mergeSymptomProfile(merged[index], incoming)
			continue
		}
		byName[key] = len(merged)
		merged = append(merged, incoming)
	}
	return merged
}

func mergeSymptomProfile(prior, incoming domain.SymptomProfile) domain.SymptomProfile {
	merged := prior
	if strings.TrimSpace(incoming.Name) != "" {
		merged.Name = incoming.Name
	}
	if strings.TrimSpace(merged.SourceQuote) == "" && strings.TrimSpace(incoming.SourceQuote) != "" {
		merged.SourceQuote = incoming.SourceQuote
	} else if normalizeFactEvidence(incoming.SourceQuote) != normalizeFactEvidence(merged.SourceQuote) {
		merged.EvidenceQuotes = appendUniqueStrings(merged.EvidenceQuotes, []string{incoming.SourceQuote})
	}
	for field, value := range map[*string]string{
		&merged.Onset: incoming.Onset, &merged.Duration: incoming.Duration, &merged.Frequency: incoming.Frequency,
		&merged.Severity: incoming.Severity, &merged.Pattern: incoming.Pattern, &merged.Trigger: incoming.Trigger,
		&merged.RelievingFactors: incoming.RelievingFactors,
	} {
		if strings.TrimSpace(value) != "" {
			*field = value
		}
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
		key := normalizeFactEvidence(event.SourceQuote)
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
		key := normalizeFactEvidence(fact.SourceQuote)
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
	for _, profile := range profiles {
		profile.Name = strings.TrimSpace(profile.Name)
		profile.SourceQuote = strings.TrimSpace(profile.SourceQuote)
		if profile.Name == "" || !isGroundedQuote(input, profile.SourceQuote) || !isGroundedProfileName(profile.SourceQuote, profile.Name) {
			continue
		}
		evidenceQuotes := make([]string, 0, len(profile.EvidenceQuotes))
		for _, quote := range profile.EvidenceQuotes {
			quote = strings.TrimSpace(quote)
			if quote != "" && isGroundedQuote(input, quote) {
				evidenceQuotes = append(evidenceQuotes, quote)
			}
		}
		profile.EvidenceQuotes = evidenceQuotes
		profile.Onset = groundedProfileValue(profile, profile.Onset, "timeline", turns, len(profiles) == 1)
		profile.Duration = groundedProfileValue(profile, profile.Duration, "duration", turns, len(profiles) == 1)
		profile.Frequency = groundedProfileValue(profile, profile.Frequency, "frequency", turns, len(profiles) == 1)
		profile.Severity = groundedProfileValue(profile, profile.Severity, "severity", turns, len(profiles) == 1)
		profile.Pattern = groundedProfileValue(profile, profile.Pattern, "pattern", turns, len(profiles) == 1)
		profile.Trigger = groundedProfileValue(profile, profile.Trigger, "trigger", turns, len(profiles) == 1)
		profile.RelievingFactors = groundedProfileValue(profile, profile.RelievingFactors, "trigger", turns, len(profiles) == 1)
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
		Session:       cloneSession(source.Session),
		SearchQueries: append([]string(nil), source.SearchQueries...),
		priorFacts:    append([]domain.Fact(nil), source.priorFacts...),
		priorProfiles: cloneSymptomProfiles(source.priorProfiles),
		priorTimeline: append([]domain.TimelineEvent(nil), source.priorTimeline...),
		priorMissing:  append([]string(nil), source.priorMissing...),
		priorGoal:     source.priorGoal,
	}
}
