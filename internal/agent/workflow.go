package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/compose"

	"visitready/internal/domain"
	"visitready/internal/guard"
)

const (
	nodeExtract   = "extract_facts"
	nodeEvidence  = "verify_evidence"
	nodeSearch    = "trusted_search"
	nodeQuestions = "generate_questions"
	nodeGuard     = "output_guard"
)

func (r *Runner) buildWorkflow() (compose.Runnable[workflowState, workflowState], error) {
	graph := compose.NewGraph[workflowState, workflowState]()
	nodes := []struct {
		name string
		fn   func(context.Context, workflowState) (workflowState, error)
	}{
		{name: nodeExtract, fn: r.extractNode},
		{name: nodeEvidence, fn: r.evidenceNode},
		{name: nodeSearch, fn: r.searchNode},
		{name: nodeQuestions, fn: r.questionNode},
		{name: nodeGuard, fn: r.outputGuardNode},
	}
	for _, node := range nodes {
		if err := graph.AddLambdaNode(node.name, compose.InvokableLambda(node.fn)); err != nil {
			return nil, fmt.Errorf("add %s node: %w", node.name, err)
		}
	}
	edges := [][2]string{
		{compose.START, nodeExtract},
		{nodeExtract, nodeEvidence},
		{nodeEvidence, nodeSearch},
		{nodeSearch, nodeQuestions},
		{nodeQuestions, nodeGuard},
		{nodeGuard, compose.END},
	}
	for _, edge := range edges {
		if err := graph.AddEdge(edge[0], edge[1]); err != nil {
			return nil, fmt.Errorf("add edge %s to %s: %w", edge[0], edge[1], err)
		}
	}
	return graph.Compile(context.Background(), compose.WithMaxRunSteps(6), compose.WithGraphName("VisitReadyAgent"))
}

func (r *Runner) extractNode(ctx context.Context, state workflowState) (workflowState, error) {
	next := cloneWorkflowState(state)
	next.Session.Status = domain.StatusExtracting
	next.Session = r.addEvent(next.Session, "extract", "running", "正在提取用户明确提供的事实")

	input := next.Session.RawInput
	if next.Session.Clarification != "" {
		input += "\n补充信息：" + next.Session.Clarification
	}
	extraction, err := r.llm.Extract(ctx, input)
	if err != nil {
		return workflowState{}, fmt.Errorf("extract facts: %w", err)
	}
	next.Session.VisitGoal = strings.TrimSpace(extraction.VisitGoal)
	next.Session.Facts = append([]domain.Fact(nil), extraction.Facts...)
	next.Session.MissingFields = append([]string(nil), extraction.MissingFields...)
	next.Session.ClarificationQuestions = append([]string(nil), extraction.ClarificationQuestions...)
	next.SearchQueries = append([]string(nil), extraction.SearchQueries...)
	next.Session = r.addEvent(next.Session, "extract", "completed", fmt.Sprintf("已提取 %d 项候选事实", len(extraction.Facts)))
	return next, nil
}

func (r *Runner) evidenceNode(_ context.Context, state workflowState) (workflowState, error) {
	next := cloneWorkflowState(state)
	next.Session.Status = domain.StatusValidating
	input := next.Session.RawInput
	if next.Session.Clarification != "" {
		input += "\n" + next.Session.Clarification
	}
	accepted, rejected := guard.PartitionGroundedFacts(input, next.Session.Facts)
	if len(accepted) == 0 {
		return workflowState{}, fmt.Errorf("all %d extracted facts failed evidence validation", len(rejected))
	}
	next.Session.Facts = accepted
	next.Session.RejectedFactCount += len(rejected)
	for index := range next.Session.Facts {
		// The quote has passed exact evidence validation. Using it as display content
		// prevents a model paraphrase from introducing an unsupported medical claim.
		next.Session.Facts[index].Content = strings.TrimSpace(next.Session.Facts[index].SourceQuote)
		next.Session.Facts[index].TimeLabel = ""
	}
	if guard.ContainsMedicalOverreach(next.Session.VisitGoal) {
		next.Session.VisitGoal = "整理本次情况并向医生说明"
	}
	safeQuestions := make([]string, 0, len(next.Session.ClarificationQuestions))
	for _, question := range next.Session.ClarificationQuestions {
		question = strings.TrimSpace(question)
		if guard.IsSafeQuestion(question) {
			safeQuestions = append(safeQuestions, question)
		}
	}
	next.Session.ClarificationQuestions = safeQuestions
	next.Session = r.addEvent(next.Session, "evidence", "completed", fmt.Sprintf("%d 项事实通过原文校验", len(accepted)))

	if next.Session.ClarificationCount == 0 && len(next.Session.ClarificationQuestions) > 0 {
		next.Session.Status = domain.StatusWaitingClarification
		next.Session = r.addEvent(next.Session, "clarification", "waiting", "需要用户补充一轮信息")
	}
	return next, nil
}

func (r *Runner) searchNode(ctx context.Context, state workflowState) (workflowState, error) {
	next := cloneWorkflowState(state)
	if next.Session.Status == domain.StatusWaitingClarification {
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

	sources := make([]domain.Source, 0, 8)
	seen := make(map[string]struct{})
	hadError := false
	for _, rawQuery := range next.SearchQueries {
		query := guard.GeneralizeSearchQuery(rawQuery, 2)
		if len([]rune(query)) < 2 || len(guard.ScanPII(query)) > 0 {
			continue
		}
		results, err := r.search.Search(ctx, query, r.allowedDomains)
		if err != nil {
			hadError = true
			continue
		}
		for _, source := range results {
			if _, exists := seen[source.URL]; exists || !guard.IsAllowedSourceURL(source.URL, r.allowedDomains) {
				continue
			}
			seen[source.URL] = struct{}{}
			sources = append(sources, source)
			if len(sources) == 8 {
				break
			}
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
	if next.Session.Status == domain.StatusWaitingClarification {
		return next, nil
	}
	next.Session.Status = domain.StatusGenerating
	result, err := r.llm.GenerateQuestions(ctx, domain.QuestionInput{
		VisitGoal:     next.Session.VisitGoal,
		Facts:         append([]domain.Fact(nil), next.Session.Facts...),
		MissingFields: append([]string(nil), next.Session.MissingFields...),
		Sources:       append([]domain.Source(nil), next.Session.Sources...),
	})
	if err != nil {
		next.Session.Questions = []domain.Question{{Text: "我还需要向您补充哪些与本次就诊有关的信息？"}}
		next.Session = r.addEvent(next.Session, "questions", "degraded", "问题生成失败，已使用安全的默认问题")
		return next, nil
	}
	next.Session.Questions = append([]domain.Question(nil), result.Questions...)
	next.Session = r.addEvent(next.Session, "questions", "completed", fmt.Sprintf("已生成 %d 个就诊沟通问题", len(result.Questions)))
	return next, nil
}

func (r *Runner) outputGuardNode(_ context.Context, state workflowState) (workflowState, error) {
	next := cloneWorkflowState(state)
	if next.Session.Status == domain.StatusWaitingClarification {
		return next, nil
	}
	allowedURLs := make(map[string]struct{}, len(next.Session.Sources))
	for _, source := range next.Session.Sources {
		allowedURLs[source.URL] = struct{}{}
	}
	filtered := make([]domain.Question, 0, len(next.Session.Questions))
	for _, question := range next.Session.Questions {
		question.Text = strings.TrimSpace(question.Text)
		if !guard.IsSafeQuestion(question.Text) {
			continue
		}
		if _, ok := allowedURLs[question.SourceURL]; !ok {
			question.SourceURL = ""
		}
		filtered = append(filtered, question)
	}
	if len(filtered) == 0 {
		filtered = []domain.Question{{Text: "我还需要向您补充哪些与本次就诊有关的信息？"}}
	}
	next.Session.Questions = filtered
	next.Session.Status = domain.StatusWaitingReview
	next.Session = r.addEvent(next.Session, "guard", "completed", "安全边界检查完成，等待用户核对")
	return next, nil
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
	}
}
