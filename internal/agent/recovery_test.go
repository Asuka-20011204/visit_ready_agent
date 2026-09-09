package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"visitready/internal/agent"
	"visitready/internal/domain"
)

type recoveryLLM struct {
	extractErrors []error
	extraction    domain.Extraction
	inputs        []string
}

type temporaryError struct{ error }

func (temporaryError) Retryable() bool { return true }

func (f *recoveryLLM) Extract(_ context.Context, input string) (domain.Extraction, error) {
	f.inputs = append(f.inputs, input)
	index := len(f.inputs) - 1
	if index < len(f.extractErrors) && f.extractErrors[index] != nil {
		return domain.Extraction{}, f.extractErrors[index]
	}
	return f.extraction, nil
}

func (f *recoveryLLM) GenerateQuestions(context.Context, domain.QuestionInput) (domain.QuestionSet, error) {
	return domain.QuestionSet{Questions: []domain.Question{{Text: "就诊时我还需要补充哪些相关信息？"}}}, nil
}

func TestRunnerStartFailureCanRetryWithoutResubmittingInput(t *testing.T) {
	input := "最近一周反复心悸，每天约两次，每次持续十分钟，想整理就诊前需要说明的信息。"
	model := &recoveryLLM{
		extractErrors: []error{temporaryError{errors.New("temporary model failure")}},
		extraction: domain.Extraction{
			VisitGoal: "整理心悸情况",
			Facts:     []domain.Fact{{Category: "symptom", Content: "反复心悸", SourceQuote: "反复心悸"}},
		},
	}
	runner := newRecoveryRunner(t, model)

	failed, err := runner.Start(context.Background(), input, false)
	if !errors.Is(err, agent.ErrUpstream) {
		t.Fatalf("Start() error = %v, want ErrUpstream", err)
	}
	if failed.ID == "" || failed.Status != domain.StatusFailed || failed.Failure == nil {
		t.Fatalf("failed session = %#v", failed)
	}
	if !failed.Failure.Retryable || failed.Failure.Attempts != 1 {
		t.Fatalf("failure = %#v", failed.Failure)
	}
	encoded, marshalErr := json.Marshal(failed)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), input) || strings.Contains(string(encoded), "temporary model failure") {
		t.Fatalf("public failed session leaked private data: %s", encoded)
	}

	recovered, err := runner.Retry(context.Background(), failed)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if recovered.Status != domain.StatusWaitingReview || recovered.Failure != nil {
		t.Fatalf("recovered session = %#v", recovered)
	}
	if len(model.inputs) != 2 || model.inputs[1] != input {
		t.Fatalf("retry inputs = %#v", model.inputs)
	}
}

func TestRunnerResumeFailureRetainsAnswerForRetryExactlyOnce(t *testing.T) {
	input := "最近一周反复心悸，想进一步整理发作时长和频率，方便就诊时向医生说明。"
	answer := "每天大约两次，每次持续十分钟。"
	model := &recoveryLLM{
		extractErrors: []error{nil, temporaryError{errors.New("temporary model failure")}},
		extraction: domain.Extraction{
			VisitGoal:            "整理心悸情况",
			Facts:                []domain.Fact{{Category: "symptom", Content: "反复心悸", SourceQuote: "反复心悸"}},
			ClarificationPrompts: []domain.Question{{Text: "每次心悸持续多久？", Category: "duration"}},
		},
	}
	runner := newRecoveryRunner(t, model)
	waiting, err := runner.Start(context.Background(), input, false)
	if err != nil || waiting.Status != domain.StatusWaitingClarification {
		t.Fatalf("Start() = %#v, %v", waiting, err)
	}
	model.extraction.ClarificationPrompts = nil

	failed, err := runner.Resume(context.Background(), waiting, answer)
	if !errors.Is(err, agent.ErrUpstream) || failed.Status != domain.StatusFailed {
		t.Fatalf("Resume() = %#v, %v", failed, err)
	}
	if failed.ClarificationCount != 1 || len(failed.ClarificationTurns) != 1 {
		t.Fatalf("failed clarification state = %#v", failed)
	}

	recovered, err := runner.Retry(context.Background(), failed)
	if err != nil || recovered.Status != domain.StatusWaitingReview {
		t.Fatalf("Retry() = %#v, %v", recovered, err)
	}
	lastInput := model.inputs[len(model.inputs)-1]
	if strings.Count(lastInput, answer) != 1 {
		t.Fatalf("retry input should contain answer once: %q", lastInput)
	}
}

func TestRunnerStopsManualRecoveryAfterThreeFailedAttempts(t *testing.T) {
	model := &recoveryLLM{extractErrors: []error{
		temporaryError{errors.New("failure one")}, temporaryError{errors.New("failure two")}, temporaryError{errors.New("failure three")}, temporaryError{errors.New("must not run")},
	}}
	runner := newRecoveryRunner(t, model)

	failed, err := runner.Start(context.Background(), "最近几天反复头晕，每次持续数分钟，希望整理就诊前需要补充的重点信息。", false)
	if !errors.Is(err, agent.ErrUpstream) {
		t.Fatal(err)
	}
	for attempt := 2; attempt <= 3; attempt++ {
		failed, err = runner.Retry(context.Background(), failed)
		if !errors.Is(err, agent.ErrUpstream) || failed.Failure == nil || failed.Failure.Attempts != attempt {
			t.Fatalf("attempt %d = %#v, %v", attempt, failed, err)
		}
	}
	if failed.Failure.Retryable {
		t.Fatalf("third failure should disable retry: %#v", failed.Failure)
	}
	if _, err = runner.Retry(context.Background(), failed); !errors.Is(err, agent.ErrInvalidState) {
		t.Fatalf("Retry() after limit error = %v", err)
	}
	if len(model.inputs) != 3 {
		t.Fatalf("extract calls = %d, want 3", len(model.inputs))
	}
}

func TestRunnerDoesNotOfferRecoveryForCancellationOrPermanentOutputFailure(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "caller canceled", err: context.Canceled},
		{name: "invalid model output", err: errors.New("schema validation failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &recoveryLLM{extractErrors: []error{test.err}}
			runner := newRecoveryRunner(t, model)
			failed, err := runner.Start(context.Background(), "最近几天反复头晕，每次持续数分钟，希望整理就诊前需要补充的重点信息。", false)
			if err == nil {
				t.Fatal("Start() error = nil")
			}
			if errors.Is(test.err, context.Canceled) {
				if failed.ID != "" {
					t.Fatalf("canceled run returned persistable session: %#v", failed)
				}
				return
			}
			if failed.Status != domain.StatusFailed || failed.Failure == nil || failed.Failure.Retryable {
				t.Fatalf("permanent failure = %#v", failed)
			}
			if failed.Failure.Code != domain.FailurePermanent {
				t.Fatalf("permanent failure code = %q", failed.Failure.Code)
			}
		})
	}
}

func newRecoveryRunner(t *testing.T, model agent.LLMClient) *agent.Runner {
	t.Helper()
	runner, err := agent.NewRunner(agent.Config{LLM: model, SessionTTL: time.Hour, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}
