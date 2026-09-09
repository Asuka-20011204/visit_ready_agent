package agent_test

import (
	"context"
	"testing"

	"visitready/internal/domain"
)

func TestRunnerCachesOnlySuccessfulGeneralizedSearches(t *testing.T) {
	input := "最近持续咳嗽并且晚上头痛，希望整理这些表现后去门诊沟通。"
	extraction := domain.Extraction{
		VisitGoal:     "准备门诊沟通",
		Facts:         []domain.Fact{{Category: "symptom", Content: "咳嗽和头痛", SourceQuote: "持续咳嗽并且晚上头痛"}},
		SearchQueries: []string{"咳嗽 头痛 就诊准备"},
	}
	model := &fakeLLM{
		extractions: []domain.Extraction{extraction, extraction},
		questions:   domain.QuestionSet{Questions: []domain.Question{{Text: "我还需要向您补充哪些症状变化？"}}},
	}
	searcher := &fakeSearch{sources: []domain.Source{{Title: "公开资料", URL: "https://who.int/advice", Domain: "who.int"}}}
	runner := newRunner(t, model, searcher)
	for range 2 {
		if _, err := runner.Start(context.Background(), input, true); err != nil {
			t.Fatal(err)
		}
	}
	if len(searcher.queries) != 1 {
		t.Fatalf("provider search calls = %d, want 1", len(searcher.queries))
	}
}
