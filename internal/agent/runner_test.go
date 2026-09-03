package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"visitready/internal/agent"
	"visitready/internal/domain"
)

type fakeLLM struct {
	extractions []domain.Extraction
	questions   domain.QuestionSet
	extractCall int
	questionIn  domain.QuestionInput
}

func (f *fakeLLM) Extract(_ context.Context, _ string) (domain.Extraction, error) {
	if f.extractCall >= len(f.extractions) {
		return domain.Extraction{}, errors.New("unexpected extract call")
	}
	result := f.extractions[f.extractCall]
	f.extractCall++
	return result, nil
}

func (f *fakeLLM) GenerateQuestions(_ context.Context, input domain.QuestionInput) (domain.QuestionSet, error) {
	f.questionIn = input
	return f.questions, nil
}

type fakeSearch struct {
	queries []string
	sources []domain.Source
	err     error
}

func (f *fakeSearch) Search(_ context.Context, query string, _ []string) ([]domain.Source, error) {
	f.queries = append(f.queries, query)
	return append([]domain.Source(nil), f.sources...), f.err
}

func TestRunnerPausesForClarificationAndResumes(t *testing.T) {
	model := &fakeLLM{
		extractions: []domain.Extraction{
			{
				VisitGoal:              "整理咳嗽情况",
				Facts:                  []domain.Fact{{Category: "symptom", Content: "咳嗽三天", SourceQuote: "三天前开始咳嗽"}},
				MissingFields:          []string{"是否发热"},
				ClarificationQuestions: []string{"是否测量过体温？"},
			},
			{
				VisitGoal:     "整理咳嗽情况",
				Facts:         []domain.Fact{{Category: "symptom", Content: "咳嗽三天", SourceQuote: "三天前开始咳嗽"}, {Category: "temperature", Content: "体温37.2度", SourceQuote: "体温37.2度"}},
				SearchQueries: []string{"咳嗽 就诊前准备"},
			},
		},
		questions: domain.QuestionSet{Questions: []domain.Question{{Text: "我还需要做哪些检查准备？"}}},
	}
	searcher := &fakeSearch{sources: []domain.Source{{Title: "WHO", URL: "https://www.who.int/example", Domain: "who.int"}}}
	runner := newRunner(t, model, searcher)

	session, err := runner.Start(context.Background(), "三天前开始咳嗽，晚上明显，目前没有服药。", true)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if session.Status != domain.StatusWaitingClarification {
		t.Fatalf("status = %q", session.Status)
	}
	if len(searcher.queries) != 0 {
		t.Fatalf("search called before clarification: %v", searcher.queries)
	}

	session, err = runner.Resume(context.Background(), session, "测量过，体温37.2度。")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if session.Status != domain.StatusWaitingReview {
		t.Fatalf("status after resume = %q", session.Status)
	}
	if len(session.Facts) != 2 || len(session.Sources) != 1 || len(session.Questions) != 1 {
		t.Fatalf("resumed session = %#v", session)
	}
	if model.extractCall != 2 {
		t.Fatalf("extract calls = %d", model.extractCall)
	}
}

func TestRunnerSanitizesQueriesAndDegradesWhenSearchFails(t *testing.T) {
	model := &fakeLLM{
		extractions: []domain.Extraction{{
			VisitGoal:     "整理头痛情况",
			Facts:         []domain.Fact{{Category: "symptom", Content: "头痛两天", SourceQuote: "头痛两天"}},
			SearchQueries: []string{"我叫王小明 手机13800138000 头痛 就诊准备"},
		}},
		questions: domain.QuestionSet{Questions: []domain.Question{{Text: "需要记录哪些诱发因素？"}}},
	}
	searcher := &fakeSearch{err: errors.New("provider unavailable")}
	runner := newRunner(t, model, searcher)

	session, err := runner.Start(context.Background(), "头痛两天，休息后稍有缓解，没有服药，也没有记录其他异常。", true)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if session.Status != domain.StatusWaitingReview {
		t.Fatalf("status = %q", session.Status)
	}
	if len(searcher.queries) != 1 || strings.Contains(searcher.queries[0], "13800138000") || strings.Contains(searcher.queries[0], "王小明") {
		t.Fatalf("queries leaked PII: %v", searcher.queries)
	}
	if !hasEvent(session.Events, "search", "degraded") {
		t.Fatalf("events do not record degraded search: %#v", session.Events)
	}
}

func TestRunnerFiltersMedicalOverreachAndRequiresReviewBeforeCompletion(t *testing.T) {
	model := &fakeLLM{
		extractions: []domain.Extraction{{
			VisitGoal: "整理胃痛情况",
			Facts:     []domain.Fact{{Category: "symptom", Content: "胃痛一天", SourceQuote: "胃痛一天"}},
		}},
		questions: domain.QuestionSet{Questions: []domain.Question{
			{Text: "建议停用目前药物"},
			{Text: "就诊时需要说明哪些饮食变化？"},
		}},
	}
	runner := newRunner(t, model, &fakeSearch{})

	session, err := runner.Start(context.Background(), "胃痛一天，饭后更明显，正在服用医生开的药。", false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(session.Questions) != 1 || strings.Contains(session.Questions[0].Text, "停用") {
		t.Fatalf("questions = %#v", session.Questions)
	}
	completed, err := runner.Confirm(session, "向医生说明胃痛变化")
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if completed.Status != domain.StatusCompleted || completed.VisitGoal != "向医生说明胃痛变化" {
		t.Fatalf("completed = %#v", completed)
	}
}

func TestRunnerGroundsFactContentAndFiltersUnsafeClarification(t *testing.T) {
	model := &fakeLLM{
		extractions: []domain.Extraction{{
			VisitGoal: "诊断为肺炎",
			Facts: []domain.Fact{{
				Category: "symptom", Content: "诊断为肺炎", SourceQuote: "咳嗽三天",
			}},
			ClarificationQuestions: []string{"建议服用抗生素", "是否测量过体温？"},
		}},
	}
	runner := newRunner(t, model, &fakeSearch{})

	session, err := runner.Start(context.Background(), "咳嗽三天，晚上加重，目前还没有测量体温。", false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if session.Status != domain.StatusWaitingClarification {
		t.Fatalf("status = %q", session.Status)
	}
	if len(session.Facts) != 1 || session.Facts[0].Content != "咳嗽三天" {
		t.Fatalf("facts = %#v", session.Facts)
	}
	if session.VisitGoal == "诊断为肺炎" {
		t.Fatalf("unsafe visit goal was retained: %q", session.VisitGoal)
	}
	if len(session.ClarificationQuestions) != 1 || strings.Contains(session.ClarificationQuestions[0], "服用") {
		t.Fatalf("clarification questions = %#v", session.ClarificationQuestions)
	}
}

func TestRunnerClassifiesInputAndUpstreamErrors(t *testing.T) {
	runner := newRunner(t, &fakeLLM{}, &fakeSearch{})
	if _, err := runner.Start(context.Background(), "太短", false); !errors.Is(err, agent.ErrInvalidInput) {
		t.Fatalf("short input error = %v", err)
	}
	if _, err := runner.Start(context.Background(), "这是一段足够长但模型会失败的脱敏健康情况描述。", false); !errors.Is(err, agent.ErrUpstream) {
		t.Fatalf("upstream error = %v", err)
	}
}

func newRunner(t *testing.T, model *fakeLLM, searcher *fakeSearch) *agent.Runner {
	t.Helper()
	fixedNow := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	runner, err := agent.NewRunner(agent.Config{
		LLM:            model,
		Search:         searcher,
		AllowedDomains: []string{"who.int"},
		SessionTTL:     30 * time.Minute,
		Now:            func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	return runner
}

func hasEvent(events []domain.AgentEvent, step, status string) bool {
	for _, event := range events {
		if event.Step == step && event.Status == status {
			return true
		}
	}
	return false
}
