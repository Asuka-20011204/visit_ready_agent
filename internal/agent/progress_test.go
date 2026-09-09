package agent_test

import (
	"context"
	"testing"

	"visitready/internal/agent"
	"visitready/internal/domain"
)

func TestRunnerReportsRealNodeProgressAndDurations(t *testing.T) {
	input := "最近三天反复心悸和手脚出汗，希望整理清楚后去门诊沟通。"
	model := &fakeLLM{
		extractions: []domain.Extraction{{
			VisitGoal: "准备门诊沟通",
			Facts:     []domain.Fact{{Category: "symptom", Content: "反复心悸和手脚出汗", SourceQuote: "反复心悸和手脚出汗"}},
		}},
		questions: domain.QuestionSet{Questions: []domain.Question{{Text: "我还需要向您补充哪些与这些症状有关的信息？"}}},
	}
	runner := newRunner(t, model, nil)
	var progress []domain.AgentProgress
	ctx := agent.WithProgress(context.Background(), func(event domain.AgentProgress) { progress = append(progress, event) })

	session, err := runner.Start(ctx, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(progress) < 4 {
		t.Fatalf("progress events = %#v", progress)
	}
	if progress[0].Status != "running" || progress[0].Node != "extract_facts" {
		t.Fatalf("first progress = %#v", progress[0])
	}
	if len(session.NodeTimings) == 0 {
		t.Fatal("completed session has no node timings")
	}
	for _, timing := range session.NodeTimings {
		if timing.DurationMS < 0 || timing.CompletedAt.IsZero() {
			t.Fatalf("invalid node timing: %#v", timing)
		}
	}
}
