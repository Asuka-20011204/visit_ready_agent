package agent

import (
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
