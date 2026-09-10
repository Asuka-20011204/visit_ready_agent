package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"visitready/internal/domain"
)

// A permanent failure must not claim the user's description was unsafe; a
// model-output or system fault is not the user's input.
func TestFailedSessionPermanentMessageIsNeutral(t *testing.T) {
	r := &Runner{now: func() time.Time { return time.Time{} }}
	session := r.failedSession(domain.Session{}, 1, false)
	if session.Failure == nil || session.Failure.Code != domain.FailurePermanent {
		t.Fatalf("failure = %#v", session.Failure)
	}
	message := session.Failure.Message
	if strings.Contains(message, "安全校验") || strings.Contains(message, "调整描述") {
		t.Fatalf("permanent failure message misattributes the cause to the user: %q", message)
	}
}

type retryableTestError struct{}

func (retryableTestError) Error() string   { return "retryable model output" }
func (retryableTestError) Retryable() bool { return true }

func TestIsRetryableRunErrorContract(t *testing.T) {
	if !isRetryableRunError(retryableTestError{}) {
		t.Fatal("error implementing Retryable() was not recognized")
	}
	if isRetryableRunError(fmt.Errorf("plain error")) {
		t.Fatal("plain error was misclassified as retryable")
	}
	wrapped := fmt.Errorf("wrapped: %w", retryableTestError{})
	if !isRetryableRunError(wrapped) {
		t.Fatal("wrapped retryable error was not recognized through errors.As")
	}
}

type failingExtractLLM struct{ err error }

func (f failingExtractLLM) Extract(context.Context, string) (domain.Extraction, error) {
	return domain.Extraction{}, f.err
}
func (f failingExtractLLM) GenerateQuestions(context.Context, domain.QuestionInput) (domain.QuestionSet, error) {
	return domain.QuestionSet{}, nil
}

// A re-extraction failure on a later round must degrade to the confirmed facts,
// not fail the run and burn the retry budget.
func TestExtractNodeDegradesOnReextractFailure(t *testing.T) {
	r := &Runner{llm: failingExtractLLM{err: retryableTestError{}}, now: func() time.Time { return time.Time{} }}
	state := workflowState{Session: domain.Session{
		ClarificationCount: 1,
		Facts:              []domain.Fact{{Category: "symptom", Content: "饭后胃痛", SourceQuote: "饭后胃痛"}},
	}}
	next, err := r.extractNode(context.Background(), state)
	if err != nil {
		t.Fatalf("extractNode failed instead of degrading on a retryable re-extract error: %v", err)
	}
	if len(next.Session.Facts) != 1 || next.Session.Facts[0].SourceQuote != "饭后胃痛" {
		t.Fatalf("confirmed facts were lost during degrade: %#v", next.Session.Facts)
	}
}
