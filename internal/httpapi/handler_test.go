package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"visitready/internal/agent"
	"visitready/internal/domain"
	"visitready/internal/httpapi"
	"visitready/internal/session"
)

type fakeRunner struct {
	started    bool
	resumed    bool
	confirmed  bool
	startErr   error
	resumeErr  error
	confirmErr error
}

type timeoutRunner struct{}

func (timeoutRunner) Start(ctx context.Context, _ string, _ bool) (domain.Session, error) {
	<-ctx.Done()
	return domain.Session{}, ctx.Err()
}

func (timeoutRunner) Resume(ctx context.Context, _ domain.Session, _ string) (domain.Session, error) {
	<-ctx.Done()
	return domain.Session{}, ctx.Err()
}

func (timeoutRunner) Confirm(item domain.Session, _ string) (domain.Session, error) {
	return item, nil
}

func (f *fakeRunner) Start(_ context.Context, input string, allowWebSearch bool) (domain.Session, error) {
	f.started = true
	if f.startErr != nil {
		return domain.Session{}, f.startErr
	}
	return domain.Session{
		ID: "session-1", Status: domain.StatusWaitingReview, AllowWebSearch: allowWebSearch,
		VisitGoal: "整理情况", Facts: []domain.Fact{{Content: "咳嗽三天", SourceQuote: "咳嗽三天"}},
		ExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

func (f *fakeRunner) Resume(_ context.Context, item domain.Session, answer string) (domain.Session, error) {
	f.resumed = true
	if f.resumeErr != nil {
		return domain.Session{}, f.resumeErr
	}
	item.Status = domain.StatusWaitingReview
	item.Questions = []domain.Question{{Text: "是否需要进一步检查？"}}
	return item, nil
}

func (f *fakeRunner) Confirm(item domain.Session, visitGoal string) (domain.Session, error) {
	f.confirmed = true
	if f.confirmErr != nil {
		return domain.Session{}, f.confirmErr
	}
	item.Status = domain.StatusCompleted
	item.VisitGoal = visitGoal
	return item, nil
}

type blockingRunner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingRunner) Start(ctx context.Context, _ string, _ bool) (domain.Session, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
		return domain.Session{ID: "blocked", Status: domain.StatusWaitingReview, ExpiresAt: time.Now().Add(time.Hour)}, nil
	case <-ctx.Done():
		return domain.Session{}, ctx.Err()
	}
}

func (b *blockingRunner) Resume(ctx context.Context, item domain.Session, _ string) (domain.Session, error) {
	return b.Start(ctx, "", false)
}

func (b *blockingRunner) Confirm(item domain.Session, _ string) (domain.Session, error) {
	return item, nil
}

func TestCreateSessionRequiresPrivacyConfirmation(t *testing.T) {
	handler, runner := newHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"input":"这是一段长度足够的脱敏健康信息，用于测试创建会话。","privacy_confirmed":false}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || runner.started {
		t.Fatalf("status = %d, started = %v, body = %s", response.Code, runner.started, response.Body.String())
	}
	assertSecurityHeaders(t, response)
	assertErrorCode(t, response.Body.Bytes(), "privacy_confirmation_required")
}

func TestSessionLifecycleAndExport(t *testing.T) {
	handler, runner := newHandler(t)
	createBody := `{"input":"这是一段长度足够的脱敏健康信息，用于测试创建会话。","allow_web_search":true,"privacy_confirmed":true}`
	create := serve(handler, http.MethodPost, "/api/v1/sessions", createBody)
	if create.Code != http.StatusCreated || !runner.started {
		t.Fatalf("create status = %d, body = %s", create.Code, create.Body.String())
	}
	if strings.Contains(create.Body.String(), "脱敏健康信息") {
		t.Fatal("create response leaked raw input")
	}

	get := serve(handler, http.MethodGet, "/api/v1/sessions/session-1", "")
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", get.Code, get.Body.String())
	}

	clarify := serve(handler, http.MethodPost, "/api/v1/sessions/session-1/clarifications", `{"answer":"补充回答"}`)
	if clarify.Code != http.StatusOK || !runner.resumed {
		t.Fatalf("clarify status = %d, body = %s", clarify.Code, clarify.Body.String())
	}

	confirm := serve(handler, http.MethodPost, "/api/v1/sessions/session-1/confirm", `{"visit_goal":"向医生说明咳嗽变化"}`)
	if confirm.Code != http.StatusOK || !runner.confirmed {
		t.Fatalf("confirm status = %d, body = %s", confirm.Code, confirm.Body.String())
	}

	export := serve(handler, http.MethodGet, "/api/v1/sessions/session-1/export", "")
	if export.Code != http.StatusOK || !strings.Contains(export.Body.String(), "我的诊前沟通清单") {
		t.Fatalf("export status = %d, body = %s", export.Code, export.Body.String())
	}
	if disposition := export.Header().Get("Content-Disposition"); !strings.Contains(disposition, "visit-ready.md") {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
}

func TestRejectsUnknownJSONAndOversizedBody(t *testing.T) {
	handler, _ := newHandler(t)
	unknown := serve(handler, http.MethodPost, "/api/v1/sessions", `{"input":"这是一段长度足够的脱敏健康信息，用于测试创建会话。","privacy_confirmed":true,"extra":1}`)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", unknown.Code)
	}

	large := map[string]any{"input": strings.Repeat("a", 33<<10), "privacy_confirmed": true}
	payload, _ := json.Marshal(large)
	response := serveBytes(handler, http.MethodPost, "/api/v1/sessions", payload)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status = %d", response.Code)
	}
}

func TestHealthzDoesNotCallDependencies(t *testing.T) {
	handler, runner := newHandler(t)
	response := serve(handler, http.MethodGet, "/healthz", "")
	if response.Code != http.StatusOK || runner.started {
		t.Fatalf("health status = %d, body = %s", response.Code, response.Body.String())
	}
	assertSecurityHeaders(t, response)
}

func TestCreateSessionIsRateLimited(t *testing.T) {
	runner := &fakeRunner{}
	handler, err := httpapi.New(httpapi.Config{
		Runner: runner, Store: session.NewMemoryStore(), Now: time.Now, RequestsPerMinute: 1,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	body := `{"input":"这是一段长度足够的脱敏健康信息，用于测试请求限速。","privacy_confirmed":true}`
	if response := serve(handler, http.MethodPost, "/api/v1/sessions", body); response.Code != http.StatusCreated {
		t.Fatalf("first status = %d", response.Code)
	}
	response := serve(handler, http.MethodPost, "/api/v1/sessions", body)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, body = %s", response.Code, response.Body.String())
	}
	assertErrorCode(t, response.Body.Bytes(), "rate_limited")
}

func TestWorkflowDeadlineMapsToGatewayTimeout(t *testing.T) {
	handler, err := httpapi.New(httpapi.Config{
		Runner: timeoutRunner{}, Store: session.NewMemoryStore(), Now: time.Now,
		WorkflowTimeout: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	body := `{"input":"这是一段长度足够的脱敏健康信息，用于测试超时处理。","privacy_confirmed":true}`
	response := serve(handler, http.MethodPost, "/api/v1/sessions", body)
	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertErrorCode(t, response.Body.Bytes(), "agent_timeout")
}

func TestHandlerRejectsInvalidConstructionMethodsAndPaths(t *testing.T) {
	if _, err := httpapi.New(httpapi.Config{Store: session.NewMemoryStore()}); err == nil {
		t.Fatal("New() without runner error = nil")
	}
	if _, err := httpapi.New(httpapi.Config{Runner: &fakeRunner{}}); err == nil {
		t.Fatal("New() without store error = nil")
	}
	handler, _ := newHandler(t)
	for _, test := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/sessions", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/v1/sessions/missing", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/sessions/missing/clarifications", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/sessions/missing/confirm", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/v1/sessions/missing/export", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/sessions/bad$id", http.StatusNotFound},
		{http.MethodGet, "/api/v1/sessions/id/unknown", http.StatusNotFound},
		{http.MethodGet, "/missing", http.StatusNotFound},
	} {
		response := serve(handler, test.method, test.path, "")
		if response.Code != test.want {
			t.Fatalf("%s %s status = %d, want %d", test.method, test.path, response.Code, test.want)
		}
	}
}

func TestHandlerMapsMissingPendingAndUpstreamStates(t *testing.T) {
	handler, runner := newHandler(t)
	if response := serve(handler, http.MethodGet, "/api/v1/sessions/missing", ""); response.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", response.Code)
	}

	body := `{"input":"这是一段长度足够的脱敏健康信息，用于测试服务错误。","privacy_confirmed":true}`
	created := serve(handler, http.MethodPost, "/api/v1/sessions", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d", created.Code)
	}
	if response := serve(handler, http.MethodGet, "/api/v1/sessions/session-1/export", ""); response.Code != http.StatusConflict {
		t.Fatalf("pending export status = %d", response.Code)
	}
	runner.confirmErr = agent.ErrInvalidState
	if response := serve(handler, http.MethodPost, "/api/v1/sessions/session-1/confirm", `{}`); response.Code != http.StatusConflict {
		t.Fatalf("invalid state status = %d", response.Code)
	}

	failing := &fakeRunner{startErr: agent.ErrUpstream}
	failingHandler, err := httpapi.New(httpapi.Config{Runner: failing, Store: session.NewMemoryStore(), Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	if response := serve(failingHandler, http.MethodPost, "/api/v1/sessions", body); response.Code != http.StatusBadGateway {
		t.Fatalf("upstream status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestHandlerRejectsUnsupportedContentTypeAndCapacity(t *testing.T) {
	handler, _ := newHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("content type status = %d", response.Code)
	}

	store := session.NewMemoryStoreWithLimit(1)
	_ = store.Create(domain.Session{ID: "existing", ExpiresAt: time.Now().Add(time.Hour)})
	capacityHandler, err := httpapi.New(httpapi.Config{Runner: &fakeRunner{}, Store: store, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"input":"这是一段长度足够的脱敏健康信息，用于测试容量限制。","privacy_confirmed":true}`
	if response := serve(capacityHandler, http.MethodPost, "/api/v1/sessions", body); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("capacity status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestHandlerLimitsConcurrentAgentRuns(t *testing.T) {
	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	handler, err := httpapi.New(httpapi.Config{
		Runner: runner, Store: session.NewMemoryStore(), Now: time.Now,
		MaxConcurrentRuns: 1, RequestsPerMinute: 10, WorkflowTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"input":"这是一段长度足够的脱敏健康信息，用于测试并发限制。","privacy_confirmed":true}`
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- serve(handler, http.MethodPost, "/api/v1/sessions", body) }()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("first run did not start")
	}
	second := serve(handler, http.MethodPost, "/api/v1/sessions", body)
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d, body = %s", second.Code, second.Body.String())
	}
	close(runner.release)
	if first := <-firstDone; first.Code != http.StatusCreated {
		t.Fatalf("first status = %d", first.Code)
	}
}

func newHandler(t *testing.T) (http.Handler, *fakeRunner) {
	t.Helper()
	runner := &fakeRunner{}
	handler, err := httpapi.New(httpapi.Config{
		Runner: runner,
		Store:  session.NewMemoryStore(),
		Now:    time.Now,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler, runner
}

func serve(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	return serveBytes(handler, method, path, []byte(body))
}

func serveBytes(handler http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertSecurityHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %#v", response.Header())
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing X-Request-ID")
	}
}

func assertErrorCode(t *testing.T, body []byte, want string) {
	t.Helper()
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Error.Code != want {
		t.Fatalf("error code = %q", response.Error.Code)
	}
}
