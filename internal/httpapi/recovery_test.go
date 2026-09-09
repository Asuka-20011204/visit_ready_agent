package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"visitready/internal/agent"
	"visitready/internal/domain"
	"visitready/internal/httpapi"
	"visitready/internal/session"
)

type recoverableRunner struct {
	retryCalls int
}

type permanentFailureRunner struct{}

func (permanentFailureRunner) Start(context.Context, string, bool) (domain.Session, error) {
	item := failedAPISession("permanent-failure", 1, false)
	item.Failure.Code = "agent_processing_failed"
	item.Failure.Message = "AI 返回的结果未通过安全校验，请重新开始。"
	return item, agent.ErrUpstream
}

func (permanentFailureRunner) Resume(context.Context, domain.Session, string) (domain.Session, error) {
	return domain.Session{}, agent.ErrInvalidState
}

func (permanentFailureRunner) Confirm(item domain.Session, _ string) (domain.Session, error) {
	return item, nil
}

func TestPermanentCreateFailureIsNotPersistedAsExhaustedRecovery(t *testing.T) {
	store := session.NewMemoryStore()
	handler, err := httpapi.New(httpapi.Config{Runner: permanentFailureRunner{}, Store: store, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}

	response := serve(handler, http.MethodPost, "/api/v1/sessions", `{"input":"最近一周反复心悸，每天约两次，每次十分钟，希望整理就诊前信息。"}`)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("permanent failure status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data  *domain.Session `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data != nil || payload.Error.Code != domain.FailurePermanent {
		t.Fatalf("permanent failure response = %#v", payload)
	}
	if _, err := store.Get("permanent-failure", time.Now()); err != session.ErrNotFound {
		t.Fatalf("permanent failure was persisted: %v", err)
	}
}

func (*recoverableRunner) Start(context.Context, string, bool) (domain.Session, error) {
	return failedAPISession("recoverable-session", 1, true), agent.ErrUpstream
}

func (*recoverableRunner) Resume(context.Context, domain.Session, string) (domain.Session, error) {
	return domain.Session{}, agent.ErrInvalidState
}

func (*recoverableRunner) Confirm(item domain.Session, _ string) (domain.Session, error) {
	return item, nil
}

func (r *recoverableRunner) Retry(_ context.Context, item domain.Session) (domain.Session, error) {
	r.retryCalls++
	item.Status = domain.StatusWaitingReview
	item.Failure = nil
	item.Facts = []domain.Fact{{Content: "反复心悸", SourceQuote: "反复心悸"}}
	return item, nil
}

func TestRecoverableCreateFailureIsPersistedAndCanBeRetried(t *testing.T) {
	runner := &recoverableRunner{}
	store := session.NewMemoryStore()
	handler, err := httpapi.New(httpapi.Config{Runner: runner, Store: store, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}

	created := serve(handler, http.MethodPost, "/api/v1/sessions", `{"input":"最近一周反复心悸，每天约两次，每次十分钟，希望整理就诊前信息。"}`)
	if created.Code != http.StatusServiceUnavailable {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var failure struct {
		Success bool           `json:"success"`
		Data    domain.Session `json:"data"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Success || failure.Error.Code != "agent_retryable_failure" || failure.Data.Status != domain.StatusFailed {
		t.Fatalf("failure response = %#v", failure)
	}
	stored, err := store.Get(failure.Data.ID, time.Now())
	if err != nil || stored.Status != domain.StatusFailed {
		t.Fatalf("stored failed session = %#v, %v", stored, err)
	}

	retried := serve(handler, http.MethodPost, "/api/v1/sessions/recoverable-session/retry", "")
	if retried.Code != http.StatusOK {
		t.Fatalf("retry status = %d, body = %s", retried.Code, retried.Body.String())
	}
	if runner.retryCalls != 1 {
		t.Fatalf("retry calls = %d", runner.retryCalls)
	}
	stored, err = store.Get(failure.Data.ID, time.Now())
	if err != nil || stored.Status != domain.StatusWaitingReview || stored.Failure != nil {
		t.Fatalf("recovered stored session = %#v, %v", stored, err)
	}
}

type recoverableResumeRunner struct {
	recoverableRunner
}

func (*recoverableResumeRunner) Resume(_ context.Context, item domain.Session, answer string) (domain.Session, error) {
	item.Status = domain.StatusFailed
	item.Clarification = answer
	item.ClarificationCount++
	item.Failure = &domain.RunFailure{
		Code: "agent_temporarily_unavailable", Message: "AI 暂时没有完成处理，本次内容已安全保留。",
		Retryable: true, Attempts: 1, FailedAt: time.Now(),
	}
	return item, agent.ErrUpstream
}

func TestRecoverableClarificationFailureReplacesWaitingSession(t *testing.T) {
	runner := &recoverableResumeRunner{}
	store := session.NewMemoryStore()
	waiting := domain.Session{
		ID: "clarification-fails", Status: domain.StatusWaitingClarification,
		ClarificationPrompts: []domain.Question{{Text: "每次持续多久？", Category: "duration"}},
		ExpiresAt:            time.Now().Add(time.Hour),
	}
	if err := store.Create(waiting); err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.New(httpapi.Config{Runner: runner, Store: store, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}

	response := serve(handler, http.MethodPost, "/api/v1/sessions/clarification-fails/clarifications", `{"answer":"每次大约十分钟。"}`)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("clarification status = %d, body = %s", response.Code, response.Body.String())
	}
	stored, err := store.Get(waiting.ID, time.Now())
	if err != nil || stored.Status != domain.StatusFailed || stored.Clarification != "每次大约十分钟。" {
		t.Fatalf("stored failed clarification = %#v, %v", stored, err)
	}
}

func TestRetryRejectsNonFailedOrExhaustedSession(t *testing.T) {
	runner := &recoverableRunner{}
	store := session.NewMemoryStore()
	for _, item := range []domain.Session{
		{ID: "waiting", Status: domain.StatusWaitingReview, ExpiresAt: time.Now().Add(time.Hour)},
		failedAPISession("exhausted", 3, false),
	} {
		if err := store.Create(item); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := httpapi.New(httpapi.Config{Runner: runner, Store: store, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"waiting", "exhausted"} {
		response := serve(handler, http.MethodPost, "/api/v1/sessions/"+id+"/retry", "")
		if response.Code != http.StatusConflict {
			t.Fatalf("retry %s status = %d, body = %s", id, response.Code, response.Body.String())
		}
	}
	if runner.retryCalls != 0 {
		t.Fatalf("retry calls = %d", runner.retryCalls)
	}
}

type failingRecoveryRunner struct{}

func (failingRecoveryRunner) Start(context.Context, string, bool) (domain.Session, error) {
	return domain.Session{}, agent.ErrInvalidState
}

type permanentRecoveryRunner struct{ failingRecoveryRunner }

func (permanentRecoveryRunner) Retry(_ context.Context, item domain.Session) (domain.Session, error) {
	item.Failure.Attempts++
	item.Failure.Retryable = false
	item.Failure.Code = domain.FailurePermanent
	item.Failure.Message = "AI 返回的结果未通过安全校验，请重新开始。"
	return item, agent.ErrUpstream
}

func TestExistingRecoveryCanBecomePermanentTerminalFailure(t *testing.T) {
	store := session.NewMemoryStore()
	item := failedAPISession("retry-permanent", 1, true)
	if err := store.Create(item); err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.New(httpapi.Config{Runner: permanentRecoveryRunner{}, Store: store, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}

	response := serve(handler, http.MethodPost, "/api/v1/sessions/retry-permanent/retry", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("retry status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.Error.Code != domain.FailurePermanent {
		t.Fatalf("permanent retry error = %#v, %v", payload.Error, err)
	}
	stored, err := store.Get(item.ID, time.Now())
	if err != nil || stored.Failure == nil || stored.Failure.Retryable || stored.Failure.Code != domain.FailurePermanent {
		t.Fatalf("stored permanent retry = %#v, %v", stored, err)
	}
}
func (failingRecoveryRunner) Resume(context.Context, domain.Session, string) (domain.Session, error) {
	return domain.Session{}, agent.ErrInvalidState
}
func (failingRecoveryRunner) Confirm(item domain.Session, _ string) (domain.Session, error) {
	return item, nil
}
func (failingRecoveryRunner) Retry(_ context.Context, item domain.Session) (domain.Session, error) {
	item.Failure.Attempts++
	item.Failure.Retryable = false
	item.Failure.Code = domain.FailureExhausted
	item.Failure.Message = "本次会话已达到自动恢复上限，请开始新的诊前准备。"
	return item, agent.ErrUpstream
}

func TestFailedRetrySnapshotIsAtomicallyUpdated(t *testing.T) {
	store := session.NewMemoryStore()
	item := failedAPISession("retry-fails", 2, true)
	if err := store.Create(item); err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.New(httpapi.Config{Runner: failingRecoveryRunner{}, Store: store, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}

	response := serve(handler, http.MethodPost, "/api/v1/sessions/retry-fails/retry", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("retry status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.Error.Code != "agent_recovery_exhausted" {
		t.Fatalf("retry error = %#v, %v", payload.Error, err)
	}
	if response.Header().Get("Retry-After") != "" {
		t.Fatalf("exhausted response advertised another retry: %q", response.Header().Get("Retry-After"))
	}
	stored, err := store.Get(item.ID, time.Now())
	if err != nil || stored.Failure == nil || stored.Failure.Attempts != 3 || stored.Failure.Retryable {
		t.Fatalf("stored retry failure = %#v, %v", stored, err)
	}
}

func failedAPISession(id string, attempts int, retryable bool) domain.Session {
	return domain.Session{
		ID: id, Status: domain.StatusFailed, RawInput: "private health text", ExpiresAt: time.Now().Add(time.Hour),
		Failure: &domain.RunFailure{Code: "agent_temporarily_unavailable", Message: "AI 暂时没有完成处理，本次内容已安全保留。", Retryable: retryable, Attempts: attempts, FailedAt: time.Now()},
	}
}
