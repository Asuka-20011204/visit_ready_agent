package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

type cancellationRaceRunner struct {
	started chan struct{}
	release chan struct{}
}

type cancellationAwareStore struct {
	*session.MemoryStore
	createStarted      chan struct{}
	legacyCreateCalled bool
}

func (s *cancellationAwareStore) Create(item domain.Session) error {
	s.legacyCreateCalled = true
	return s.MemoryStore.Create(item)
}

func (s *cancellationAwareStore) CreateContext(ctx context.Context, _ domain.Session) error {
	close(s.createStarted)
	<-ctx.Done()
	return ctx.Err()
}

func (r *cancellationRaceRunner) Start(context.Context, string, bool) (domain.Session, error) {
	close(r.started)
	<-r.release
	return domain.Session{ID: "must-not-persist", Status: domain.StatusWaitingReview, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (*cancellationRaceRunner) Resume(context.Context, domain.Session, string) (domain.Session, error) {
	return domain.Session{}, agent.ErrInvalidState
}

func (*cancellationRaceRunner) Confirm(item domain.Session, _ string) (domain.Session, error) {
	return item, nil
}

type selectiveBlockingRunner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type blockingResumeRunner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingResumeRunner) Start(_ context.Context, _ string, _ bool) (domain.Session, error) {
	return domain.Session{}, agent.ErrInvalidState
}

func (b *blockingResumeRunner) Resume(ctx context.Context, item domain.Session, _ string) (domain.Session, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
		item.Status = domain.StatusWaitingReview
		return item, nil
	case <-ctx.Done():
		return domain.Session{}, ctx.Err()
	}
}

func (b *blockingResumeRunner) Confirm(item domain.Session, _ string) (domain.Session, error) {
	return item, nil
}

type failingReplaceStore struct{ *session.MemoryStore }

func (f failingReplaceStore) Replace(domain.Session) error { return session.ErrExpired }

func (f failingReplaceStore) ReplaceIfStatus(domain.Session, domain.AgentStatus) (domain.Session, error) {
	return domain.Session{}, session.ErrExpired
}

func (f failingReplaceStore) ReplaceIfStatusContext(context.Context, domain.Session, domain.AgentStatus) (domain.Session, error) {
	return domain.Session{}, session.ErrExpired
}

type readinessStore struct {
	*session.MemoryStore
	pingErr    error
	pingCalled int
}

func (s *readinessStore) Ready(context.Context) error {
	s.pingCalled++
	return s.pingErr
}

type blockingConfirmRunner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingConfirmRunner) Start(_ context.Context, _ string, _ bool) (domain.Session, error) {
	return domain.Session{}, agent.ErrInvalidState
}

func (b *blockingConfirmRunner) Resume(_ context.Context, item domain.Session, _ string) (domain.Session, error) {
	return item, nil
}

func (b *blockingConfirmRunner) Confirm(item domain.Session, _ string) (domain.Session, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	item.Status = domain.StatusCompleted
	return item, nil
}

type interleavingStore struct {
	*session.MemoryStore
	ordinaryReplaceStarted chan struct{}
	emergencyWritten       chan struct{}
	ordinaryOnce           sync.Once
	emergencyOnce          sync.Once
}

func (s *interleavingStore) Replace(item domain.Session) error {
	if item.Status == domain.StatusEmergency {
		err := s.MemoryStore.Replace(item)
		s.emergencyOnce.Do(func() { close(s.emergencyWritten) })
		return err
	}
	s.ordinaryOnce.Do(func() { close(s.ordinaryReplaceStarted) })
	<-s.emergencyWritten
	return s.MemoryStore.Replace(item)
}

func (s *interleavingStore) ReplaceIfStatus(item domain.Session, expected domain.AgentStatus) (domain.Session, error) {
	if item.Status == domain.StatusEmergency {
		current, err := s.MemoryStore.ReplaceIfStatus(item, expected)
		s.emergencyOnce.Do(func() { close(s.emergencyWritten) })
		return current, err
	}
	s.ordinaryOnce.Do(func() { close(s.ordinaryReplaceStarted) })
	<-s.emergencyWritten
	return s.MemoryStore.ReplaceIfStatus(item, expected)
}

func (s *interleavingStore) ReplaceIfStatusContext(ctx context.Context, item domain.Session, expected domain.AgentStatus) (domain.Session, error) {
	if err := ctx.Err(); err != nil {
		return domain.Session{}, err
	}
	return s.ReplaceIfStatus(item, expected)
}

func (b *selectiveBlockingRunner) Start(ctx context.Context, input string, _ bool) (domain.Session, error) {
	if strings.Contains(input, "胸痛") {
		return domain.Session{ID: "emergency", Status: domain.StatusEmergency, EmergencyMessage: agent.EmergencyEscalationMessage, ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
		return domain.Session{ID: "normal", Status: domain.StatusWaitingReview, ExpiresAt: time.Now().Add(time.Hour)}, nil
	case <-ctx.Done():
		return domain.Session{}, ctx.Err()
	}
}

func (b *selectiveBlockingRunner) Resume(_ context.Context, item domain.Session, _ string) (domain.Session, error) {
	item.Status = domain.StatusEmergency
	item.EmergencyMessage = agent.EmergencyEscalationMessage
	return item, nil
}

func (b *selectiveBlockingRunner) Confirm(item domain.Session, _ string) (domain.Session, error) {
	return item, nil
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

func TestCreateSessionDoesNotRequireRepeatedPrivacyConfirmation(t *testing.T) {
	handler, runner := newHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"input":"这是一段长度足够的脱敏健康信息，用于测试创建会话。","privacy_confirmed":false}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || !runner.started {
		t.Fatalf("status = %d, started = %v, body = %s", response.Code, runner.started, response.Body.String())
	}
	assertSecurityHeaders(t, response)
}

func TestCreateSessionRejectsDirectIdentifiersBeforeRunner(t *testing.T) {
	handler, runner := newHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"input":"我叫张三，最近持续心悸和出汗，想准备就诊沟通。"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || runner.started {
		t.Fatalf("status = %d, started = %v, body = %s", response.Code, runner.started, response.Body.String())
	}
	assertErrorCode(t, response.Body.Bytes(), "direct_identifier_detected")
}

func TestCreateSessionLetsEmergencyGuidancePreemptPIIRejection(t *testing.T) {
	store := session.NewMemoryStore()
	modelRunner := &emergencyRunner{}
	handler, err := httpapi.New(httpapi.Config{Runner: modelRunner, Store: store, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	response := serve(handler, http.MethodPost, "/api/v1/sessions", `{"input":"我叫张三，现在胸痛。"}`)
	if response.Code != http.StatusCreated || !modelRunner.started {
		t.Fatalf("status = %d, started = %v, body = %s", response.Code, modelRunner.started, response.Body.String())
	}
}

type emergencyRunner struct{ started bool }

func (r *emergencyRunner) Start(_ context.Context, _ string, _ bool) (domain.Session, error) {
	r.started = true
	return domain.Session{ID: "emergency", Status: domain.StatusEmergency, EmergencyMessage: agent.EmergencyEscalationMessage, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (*emergencyRunner) Resume(context.Context, domain.Session, string) (domain.Session, error) {
	return domain.Session{}, agent.ErrInvalidState
}

func (*emergencyRunner) Confirm(domain.Session, string) (domain.Session, error) {
	return domain.Session{}, agent.ErrInvalidState
}

func TestSessionLifecycleAndExport(t *testing.T) {
	runner := &fakeRunner{}
	store := session.NewMemoryStore()
	handler, err := httpapi.New(httpapi.Config{Runner: runner, Store: store, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	createBody := `{"input":"这是一段长度足够的脱敏健康信息，用于测试创建会话。","allow_web_search":true,"privacy_confirmed":true}`
	create := serve(handler, http.MethodPost, "/api/v1/sessions", createBody)
	if create.Code != http.StatusCreated || !runner.started {
		t.Fatalf("create status = %d, body = %s", create.Code, create.Body.String())
	}
	if strings.Contains(create.Body.String(), "脱敏健康信息") {
		t.Fatal("create response leaked raw input")
	}
	created, err := store.Get("session-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	created.Status = domain.StatusWaitingClarification
	if err := store.Replace(created); err != nil {
		t.Fatal(err)
	}

	get := serve(handler, http.MethodGet, "/api/v1/sessions/session-1", "")
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", get.Code, get.Body.String())
	}

	clarify := serve(handler, http.MethodPost, "/api/v1/sessions/session-1/clarifications", `{"answer":"补充回答"}`)
	if clarify.Code != http.StatusOK || !runner.resumed {
		t.Fatalf("clarify status = %d, body = %s", clarify.Code, clarify.Body.String())
	}
	var clarified struct {
		Data domain.Session `json:"data"`
	}
	if err := json.Unmarshal(clarify.Body.Bytes(), &clarified); err != nil {
		t.Fatalf("decode clarification response: %v", err)
	}

	digest := domain.FactReviewDigest(clarified.Data.Facts)
	reviewDigest := domain.ReviewDigest(clarified.Data)
	confirmBody := `{"visit_goal":"向医生说明咳嗽变化","facts_acknowledged":true,"facts_digest":"` + digest + `","insights_acknowledged":true,"review_digest":"` + reviewDigest + `"}`
	confirm := serve(handler, http.MethodPost, "/api/v1/sessions/session-1/confirm", confirmBody)
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

func TestConfirmRequiresAcknowledgedCurrentFacts(t *testing.T) {
	handler, runner := newHandler(t)
	body := `{"input":"这是一段长度足够的脱敏健康信息，用于测试人工核对。","privacy_confirmed":true}`
	if response := serve(handler, http.MethodPost, "/api/v1/sessions", body); response.Code != http.StatusCreated {
		t.Fatalf("create status = %d", response.Code)
	}

	for _, confirmation := range []string{
		`{"visit_goal":"说明症状"}`,
		`{"visit_goal":"说明症状","facts_acknowledged":true,"facts_digest":"stale"}`,
	} {
		runner.confirmed = false
		response := serve(handler, http.MethodPost, "/api/v1/sessions/session-1/confirm", confirmation)
		if response.Code != http.StatusConflict || runner.confirmed {
			t.Fatalf("confirm status = %d, runner confirmed = %v, body = %s", response.Code, runner.confirmed, response.Body.String())
		}
		assertErrorCode(t, response.Body.Bytes(), "fact_review_required")
	}
}

func TestConfirmRequiresAcknowledgedCurrentInsights(t *testing.T) {
	handler, runner := newHandler(t)
	body := `{"input":"这是一段长度足够的脱敏健康信息，用于测试结构化分析核对。","privacy_confirmed":true}`
	created := serve(handler, http.MethodPost, "/api/v1/sessions", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d", created.Code)
	}
	var responseBody struct {
		Data domain.Session `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &responseBody); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	factsDigest := domain.FactReviewDigest(responseBody.Data.Facts)
	reviewDigest := domain.ReviewDigest(responseBody.Data)

	for _, confirmation := range []string{
		`{"visit_goal":"说明症状","facts_acknowledged":true,"facts_digest":"` + factsDigest + `"}`,
		`{"visit_goal":"说明症状","facts_acknowledged":true,"facts_digest":"` + factsDigest + `","insights_acknowledged":true,"review_digest":"stale"}`,
	} {
		runner.confirmed = false
		response := serve(handler, http.MethodPost, "/api/v1/sessions/session-1/confirm", confirmation)
		if response.Code != http.StatusConflict || runner.confirmed {
			t.Fatalf("confirm status = %d, runner confirmed = %v, body = %s", response.Code, runner.confirmed, response.Body.String())
		}
		assertErrorCode(t, response.Body.Bytes(), "insight_review_required")
	}

	valid := `{"visit_goal":"说明症状","facts_acknowledged":true,"facts_digest":"` + factsDigest + `","insights_acknowledged":true,"review_digest":"` + reviewDigest + `"}`
	if response := serve(handler, http.MethodPost, "/api/v1/sessions/session-1/confirm", valid); response.Code != http.StatusOK || !runner.confirmed {
		t.Fatalf("valid confirm status = %d, runner confirmed = %v, body = %s", response.Code, runner.confirmed, response.Body.String())
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

func TestHealthEndpointsSeparateLivenessAndReadiness(t *testing.T) {
	store := &readinessStore{MemoryStore: session.NewMemoryStore(), pingErr: errors.New("private database failure")}
	handler, err := httpapi.New(httpapi.Config{Runner: &fakeRunner{}, Store: store, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}

	live := serve(handler, http.MethodGet, "/livez", "")
	if live.Code != http.StatusOK || store.pingCalled != 0 {
		t.Fatalf("live status = %d, ping calls = %d, body = %s", live.Code, store.pingCalled, live.Body.String())
	}
	assertSecurityHeaders(t, live)

	for _, path := range []string{"/readyz", "/healthz"} {
		response := serve(handler, http.MethodGet, path, "")
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, body = %s", path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "private database failure") {
			t.Fatalf("%s leaked dependency error: %s", path, response.Body.String())
		}
		assertSecurityHeaders(t, response)
	}
	if store.pingCalled != 2 {
		t.Fatalf("readiness ping calls = %d, want 2", store.pingCalled)
	}

	store.pingErr = nil
	ready := serve(handler, http.MethodGet, "/readyz", "")
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"session_store":"ok"`) {
		t.Fatalf("ready status = %d, body = %s", ready.Code, ready.Body.String())
	}
}

func TestReadinessReflectsSessionCapacity(t *testing.T) {
	store := session.NewMemoryStoreWithLimit(1)
	handler, err := httpapi.New(httpapi.Config{Runner: &fakeRunner{}, Store: store, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	item := domain.Session{ID: "at-capacity", Status: domain.StatusWaitingReview, ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Create(item); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/readyz", "/healthz"} {
		assertHealthState(t, serve(handler, http.MethodGet, path, ""), http.StatusOK, "ready", "at_capacity", false)
	}
	if err := store.Delete(item.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/readyz", "/healthz"} {
		assertHealthState(t, serve(handler, http.MethodGet, path, ""), http.StatusOK, "ready", "ok", true)
	}
}

func assertHealthState(t *testing.T, response *httptest.ResponseRecorder, wantCode int, wantStatus, wantStore string, wantAccepting bool) {
	t.Helper()
	var payload struct {
		Status               string            `json:"status"`
		Components           map[string]string `json:"components"`
		AcceptingNewSessions *bool             `json:"accepting_new_sessions"`
		RequestID            string            `json:"request_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode health response: %v; body = %s", err, response.Body.String())
	}
	if response.Code != wantCode || payload.Status != wantStatus || payload.Components["session_store"] != wantStore {
		t.Fatalf("health response = code %d, payload %#v", response.Code, payload)
	}
	if payload.AcceptingNewSessions == nil || *payload.AcceptingNewSessions != wantAccepting || payload.RequestID == "" {
		t.Fatalf("health metadata = %#v", payload)
	}
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

func TestEmergencyCreateKeepsGuidanceWhenSessionPersistenceIsRateLimited(t *testing.T) {
	runner := &fakeRunner{}
	handler, err := httpapi.New(httpapi.Config{
		Runner: runner, Store: session.NewMemoryStore(), Now: time.Now, RequestsPerMinute: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	normal := `{"input":"这是一段长度足够的脱敏健康信息，用于测试请求限速。"}`
	if response := serve(handler, http.MethodPost, "/api/v1/sessions", normal); response.Code != http.StatusCreated {
		t.Fatalf("normal status = %d, body = %s", response.Code, response.Body.String())
	}
	emergency := serve(handler, http.MethodPost, "/api/v1/sessions", `{"input":"我现在胸痛并且呼吸困难，症状正在加重。"}`)
	if emergency.Code != http.StatusTooManyRequests || !strings.Contains(emergency.Body.String(), agent.EmergencyEscalationMessage) {
		t.Fatalf("emergency guidance was hidden by persistence limit: %d %s", emergency.Code, emergency.Body.String())
	}
	assertErrorCode(t, emergency.Body.Bytes(), "emergency_not_saved")
}

func TestContextualEmergencyClarificationBypassesRateLimit(t *testing.T) {
	store := session.NewMemoryStore()
	now := time.Now()
	if err := store.Create(domain.Session{
		ID: "safety-session", Status: domain.StatusWaitingClarification, ExpiresAt: now.Add(time.Hour),
		ClarificationPrompts: []domain.Question{{Text: "现在是否有胸痛或呼吸困难，并且正在加重？", Category: "safety"}},
	}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	handler, err := httpapi.New(httpapi.Config{Runner: runner, Store: store, Now: func() time.Time { return now }, RequestsPerMinute: 1})
	if err != nil {
		t.Fatal(err)
	}
	if response := serve(handler, http.MethodPost, "/api/v1/sessions", `{"input":"这是一段长度足够的脱敏健康信息，用于消耗普通请求额度。"}`); response.Code != http.StatusCreated {
		t.Fatalf("normal status = %d, body = %s", response.Code, response.Body.String())
	}
	response := serve(handler, http.MethodPost, "/api/v1/sessions/safety-session/clarifications", `{"answer":"有，而且越来越严重"}`)
	if response.Code != http.StatusOK || runner.resumed {
		t.Fatalf("contextual emergency status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestDirectEmergencyClarificationBypassesSameSessionLock(t *testing.T) {
	store := session.NewMemoryStore()
	now := time.Now()
	if err := store.Create(domain.Session{ID: "contended", Status: domain.StatusWaitingClarification, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	runner := &blockingResumeRunner{started: make(chan struct{}), release: make(chan struct{})}
	handler, err := httpapi.New(httpapi.Config{Runner: runner, Store: store, Now: func() time.Time { return now }, WorkflowTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- serve(handler, http.MethodPost, "/api/v1/sessions/contended/clarifications", `{"answer":"每次持续五分钟"}`)
	}()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("ordinary clarification did not acquire the session lock")
	}
	emergencyDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		emergencyDone <- serve(handler, http.MethodPost, "/api/v1/sessions/contended/clarifications", `{"answer":"我现在胸痛并且呼吸困难"}`)
	}()
	select {
	case response := <-emergencyDone:
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), agent.EmergencyEscalationMessage) {
			t.Fatalf("emergency status = %d, body = %s", response.Code, response.Body.String())
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("emergency clarification waited for the same-session lock")
	}
	close(runner.release)
	first := <-firstDone
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"status":"emergency"`) {
		t.Fatalf("ordinary completion overwrote emergency state: %d %s", first.Code, first.Body.String())
	}
}

func TestEmergencyClarificationRejectsSessionsThatAreNotAwaitingClarification(t *testing.T) {
	tests := []struct {
		name   string
		status domain.AgentStatus
	}{
		{name: "waiting review", status: domain.StatusWaitingReview},
		{name: "completed", status: domain.StatusCompleted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := session.NewMemoryStore()
			now := time.Now()
			item := domain.Session{
				ID: "protected-session", Status: test.status, ExpiresAt: now.Add(time.Hour),
				Questions:   []domain.Question{{Text: "保留的问题"}},
				ActionItems: []domain.ActionItem{{Title: "保留的行动项"}},
				Sources:     []domain.Source{{Title: "保留的来源"}},
			}
			if err := store.Create(item); err != nil {
				t.Fatal(err)
			}
			handler, err := httpapi.New(httpapi.Config{Runner: &fakeRunner{}, Store: store, Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}

			response := serve(handler, http.MethodPost, "/api/v1/sessions/protected-session/clarifications", `{"answer":"我现在胸痛"}`)
			if response.Code != http.StatusConflict {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			assertErrorCode(t, response.Body.Bytes(), "invalid_session_state")
			if !strings.Contains(response.Body.String(), agent.EmergencyEscalationMessage) {
				t.Fatalf("emergency guidance missing from rejected transition: %s", response.Body.String())
			}
			stored, err := store.Get(item.ID, now)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != test.status || len(stored.Questions) != 1 || len(stored.ActionItems) != 1 || len(stored.Sources) != 1 {
				t.Fatalf("protected session was changed: %#v", stored)
			}
		})
	}
}

func TestEmergencyClarificationKeepsGuidanceWhenSessionIsMissing(t *testing.T) {
	handler, _ := newHandler(t)
	response := serve(handler, http.MethodPost, "/api/v1/sessions/missing/clarifications", `{"answer":"我现在胸痛"}`)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), agent.EmergencyEscalationMessage) {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertErrorCode(t, response.Body.Bytes(), "emergency_session_unavailable")
}

func TestEmergencyClarificationCannotBeOverwrittenAfterOrdinaryRead(t *testing.T) {
	base := session.NewMemoryStore()
	now := time.Now()
	if err := base.Create(domain.Session{ID: "write-race", Status: domain.StatusWaitingClarification, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	store := &interleavingStore{
		MemoryStore:            base,
		ordinaryReplaceStarted: make(chan struct{}),
		emergencyWritten:       make(chan struct{}),
	}
	runner := &blockingResumeRunner{started: make(chan struct{}), release: make(chan struct{})}
	handler, err := httpapi.New(httpapi.Config{Runner: runner, Store: store, Now: func() time.Time { return now }, WorkflowTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}

	ordinaryDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		ordinaryDone <- serve(handler, http.MethodPost, "/api/v1/sessions/write-race/clarifications", `{"answer":"每次持续五分钟"}`)
	}()
	<-runner.started
	close(runner.release)
	select {
	case <-store.ordinaryReplaceStarted:
	case <-time.After(time.Second):
		t.Fatal("ordinary clarification did not reach its write-back")
	}

	emergency := serve(handler, http.MethodPost, "/api/v1/sessions/write-race/clarifications", `{"answer":"我现在胸痛并且呼吸困难"}`)
	if emergency.Code != http.StatusOK || !strings.Contains(emergency.Body.String(), agent.EmergencyEscalationMessage) {
		t.Fatalf("emergency status = %d, body = %s", emergency.Code, emergency.Body.String())
	}
	ordinary := <-ordinaryDone
	if ordinary.Code != http.StatusOK || !strings.Contains(ordinary.Body.String(), `"status":"emergency"`) {
		t.Fatalf("ordinary completion overwrote emergency state: %d %s", ordinary.Code, ordinary.Body.String())
	}
	stored, err := base.Get("write-race", now)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.StatusEmergency {
		t.Fatalf("stored status = %q, want emergency", stored.Status)
	}
}

func TestEmergencyClarificationUsesSeparateAbuseLimit(t *testing.T) {
	store := session.NewMemoryStore()
	now := time.Now()
	for _, id := range []string{"emergency-one", "emergency-two"} {
		if err := store.Create(domain.Session{ID: id, Status: domain.StatusWaitingClarification, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := httpapi.New(httpapi.Config{
		Runner: &fakeRunner{}, Store: store, Now: func() time.Time { return now },
		RequestsPerMinute: 10, EmergencyRequestsPerMinute: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	answer := `{"answer":"我现在胸痛并且呼吸困难"}`
	if first := serve(handler, http.MethodPost, "/api/v1/sessions/emergency-one/clarifications", answer); first.Code != http.StatusOK {
		t.Fatalf("first emergency status = %d, body = %s", first.Code, first.Body.String())
	}
	second := serve(handler, http.MethodPost, "/api/v1/sessions/emergency-two/clarifications", answer)
	if second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), agent.EmergencyEscalationMessage) {
		t.Fatalf("limited emergency status = %d, body = %s", second.Code, second.Body.String())
	}
	assertErrorCode(t, second.Body.Bytes(), "emergency_rate_limited")
	stored, err := store.Get("emergency-two", now)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status == domain.StatusEmergency {
		t.Fatal("rate-limited emergency clarification was persisted")
	}
}

func TestEmergencyClarificationKeepsGuidanceWhenReplaceFails(t *testing.T) {
	base := session.NewMemoryStore()
	now := time.Now()
	if err := base.Create(domain.Session{ID: "replace-fails", Status: domain.StatusWaitingClarification, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.New(httpapi.Config{Runner: &fakeRunner{}, Store: failingReplaceStore{MemoryStore: base}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	response := serve(handler, http.MethodPost, "/api/v1/sessions/replace-fails/clarifications", `{"answer":"我现在胸痛并且呼吸困难"}`)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), agent.EmergencyEscalationMessage) {
		t.Fatalf("replace failure status = %d, body = %s", response.Code, response.Body.String())
	}
	assertErrorCode(t, response.Body.Bytes(), "emergency_not_saved")
}

func TestEmergencyClarificationCannotInterruptConcurrentConfirm(t *testing.T) {
	store := session.NewMemoryStore()
	now := time.Now()
	item := domain.Session{ID: "confirm-race", Status: domain.StatusWaitingReview, ExpiresAt: now.Add(time.Hour)}
	if err := store.Create(item); err != nil {
		t.Fatal(err)
	}
	runner := &blockingConfirmRunner{started: make(chan struct{}), release: make(chan struct{})}
	handler, err := httpapi.New(httpapi.Config{Runner: runner, Store: store, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	confirmBody := `{"facts_acknowledged":true,"facts_digest":"` + domain.FactReviewDigest(item.Facts) + `","insights_acknowledged":true,"review_digest":"` + domain.ReviewDigest(item) + `"}`
	confirmDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		confirmDone <- serve(handler, http.MethodPost, "/api/v1/sessions/confirm-race/confirm", confirmBody)
	}()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("confirm did not reach the blocking runner")
	}
	emergency := serve(handler, http.MethodPost, "/api/v1/sessions/confirm-race/clarifications", `{"answer":"我现在胸痛"}`)
	if emergency.Code != http.StatusConflict {
		t.Fatalf("emergency status = %d, body = %s", emergency.Code, emergency.Body.String())
	}
	assertErrorCode(t, emergency.Body.Bytes(), "invalid_session_state")
	close(runner.release)
	confirmed := <-confirmDone
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirm race status = %d, body = %s", confirmed.Code, confirmed.Body.String())
	}
	stored, err := store.Get(item.ID, now)
	if err != nil || stored.Status != domain.StatusCompleted {
		t.Fatalf("stored session = %#v, err = %v", stored, err)
	}
}

func TestEmergencyClarificationIsIdempotentAndRateLimitedAcrossSessions(t *testing.T) {
	store := session.NewMemoryStore()
	now := time.Now()
	for _, id := range []string{"urgent-one", "urgent-two"} {
		if err := store.Create(domain.Session{ID: id, Status: domain.StatusWaitingClarification, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := httpapi.New(httpapi.Config{
		Runner: &fakeRunner{}, Store: store, Now: func() time.Time { return now }, EmergencyRequestsPerMinute: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := serve(handler, http.MethodPost, "/api/v1/sessions/urgent-one/clarifications", `{"answer":"我现在胸痛"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
	repeated := serve(handler, http.MethodPost, "/api/v1/sessions/urgent-one/clarifications", `{"answer":"我现在胸痛"}`)
	if repeated.Code != http.StatusOK {
		t.Fatalf("idempotent status = %d, body = %s", repeated.Code, repeated.Body.String())
	}
	var firstBody, repeatedBody struct {
		Data domain.Session `json:"data"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &firstBody)
	_ = json.Unmarshal(repeated.Body.Bytes(), &repeatedBody)
	if len(firstBody.Data.Events) != len(repeatedBody.Data.Events) {
		t.Fatalf("repeated emergency appended events: first=%d repeated=%d", len(firstBody.Data.Events), len(repeatedBody.Data.Events))
	}
	limited := serve(handler, http.MethodPost, "/api/v1/sessions/urgent-two/clarifications", `{"answer":"我现在胸痛"}`)
	if limited.Code != http.StatusTooManyRequests || !strings.Contains(limited.Body.String(), agent.EmergencyEscalationMessage) {
		t.Fatalf("limited status = %d, body = %s", limited.Code, limited.Body.String())
	}
}

func TestEmergencyCreateHasSeparateAbuseLimitThatKeepsGuidanceVisible(t *testing.T) {
	handler, err := httpapi.New(httpapi.Config{
		Runner: &emergencyRunner{}, Store: session.NewMemoryStore(), Now: time.Now,
		RequestsPerMinute: 1, EmergencyRequestsPerMinute: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"input":"我现在胸痛并且呼吸困难，症状正在加重。"}`
	if first := serve(handler, http.MethodPost, "/api/v1/sessions", body); first.Code != http.StatusCreated {
		t.Fatalf("first emergency status = %d, body = %s", first.Code, first.Body.String())
	}
	second := serve(handler, http.MethodPost, "/api/v1/sessions", body)
	if second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), agent.EmergencyEscalationMessage) {
		t.Fatalf("limited emergency status = %d, body = %s", second.Code, second.Body.String())
	}
	assertErrorCode(t, second.Body.Bytes(), "emergency_rate_limited")
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

func TestCanceledCreateDoesNotPersistSuccessfulLateResult(t *testing.T) {
	runner := &cancellationRaceRunner{started: make(chan struct{}), release: make(chan struct{})}
	store := session.NewMemoryStore()
	handler, err := httpapi.New(httpapi.Config{Runner: runner, Store: store, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"input":"这是一段长度足够的脱敏健康信息，用于测试取消竞态。"}`)).WithContext(requestContext)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	<-runner.started
	cancel()
	close(runner.release)
	<-done
	if _, err := store.Get("must-not-persist", time.Now()); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("late successful result was persisted after cancellation: %v", err)
	}
}

func TestCanceledCreateInterruptsContextAwareStoreCommit(t *testing.T) {
	store := &cancellationAwareStore{MemoryStore: session.NewMemoryStore(), createStarted: make(chan struct{})}
	handler, err := httpapi.New(httpapi.Config{Runner: &fakeRunner{}, Store: store, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"input":"这是一段长度足够的脱敏健康信息，用于测试存储提交取消。"}`)).WithContext(requestContext)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()

	<-store.createStarted
	cancel()
	<-done
	if store.legacyCreateCalled {
		t.Fatal("handler bypassed context-aware store create")
	}
	if _, err := store.Get("session-1", time.Now()); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("canceled store commit persisted a session: %v", err)
	}
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
		{http.MethodGet, "/api/v1/sessions", http.StatusOK},
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

func TestHandlerPermanentlyDeletesSession(t *testing.T) {
	store := session.NewMemoryStore()
	item := domain.Session{ID: "delete-session", Status: domain.StatusWaitingReview, ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Create(item); err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.New(httpapi.Config{Runner: &fakeRunner{}, Store: store, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}

	response := serve(handler, http.MethodDelete, "/api/v1/sessions/delete-session", "")
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("delete status = %d, body = %s", response.Code, response.Body.String())
	}
	if response := serve(handler, http.MethodGet, "/api/v1/sessions/delete-session", ""); response.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, body = %s", response.Code, response.Body.String())
	}
	if response := serve(handler, http.MethodDelete, "/api/v1/sessions/delete-session", ""); response.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestEmergencySessionCanBeFetchedAndExportedButNotConfirmed(t *testing.T) {
	store := session.NewMemoryStore()
	now := time.Now()
	item := domain.Session{
		ID: "emergency-session", Status: domain.StatusEmergency,
		EmergencyMessage: agent.EmergencyEscalationMessage,
		RiskSignals:      []domain.RiskSignal{{Priority: domain.PriorityUrgent, Evidence: "胸痛", SourceQuote: "胸痛"}},
		ExpiresAt:        now.Add(time.Hour),
	}
	if err := store.Create(item); err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.New(httpapi.Config{Runner: &fakeRunner{}, Store: store, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if response := serve(handler, http.MethodGet, "/api/v1/sessions/emergency-session", ""); response.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", response.Code, response.Body.String())
	}
	if response := serve(handler, http.MethodGet, "/api/v1/sessions/emergency-session/export", ""); response.Code != http.StatusOK {
		t.Fatalf("export status = %d, body = %s", response.Code, response.Body.String())
	}
	if response := serve(handler, http.MethodPost, "/api/v1/sessions/emergency-session/confirm", `{}`); response.Code != http.StatusConflict {
		t.Fatalf("confirm status = %d, body = %s", response.Code, response.Body.String())
	}
	if response := serve(handler, http.MethodPost, "/api/v1/sessions/emergency-session/clarifications", `{"answer":"继续"}`); response.Code != http.StatusConflict {
		t.Fatalf("clarification status = %d, body = %s", response.Code, response.Body.String())
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
	emergencyStore := session.NewMemoryStoreWithLimit(1)
	_ = emergencyStore.Create(domain.Session{ID: "existing", ExpiresAt: time.Now().Add(time.Hour)})
	emergencyHandler, err := httpapi.New(httpapi.Config{Runner: &emergencyRunner{}, Store: emergencyStore, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	response = serve(emergencyHandler, http.MethodPost, "/api/v1/sessions", `{"input":"我现在胸痛并且呼吸困难，症状正在加重。"}`)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), agent.EmergencyEscalationMessage) {
		t.Fatalf("emergency capacity response = %d, body = %s", response.Code, response.Body.String())
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

func TestEmergencyCreateBypassesBusyRunSlot(t *testing.T) {
	runner := &selectiveBlockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	handler, err := httpapi.New(httpapi.Config{
		Runner: runner, Store: session.NewMemoryStore(), Now: time.Now,
		MaxConcurrentRuns: 1, RequestsPerMinute: 10, WorkflowTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	normalBody := `{"input":"这是一段长度足够的脱敏健康信息，用于占用并发运行槽。"}`
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- serve(handler, http.MethodPost, "/api/v1/sessions", normalBody) }()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("normal run did not start")
	}
	emergency := serve(handler, http.MethodPost, "/api/v1/sessions", `{"input":"我现在胸痛并且呼吸困难，症状正在加重。"}`)
	if emergency.Code != http.StatusCreated {
		t.Fatalf("emergency status = %d, body = %s", emergency.Code, emergency.Body.String())
	}
	close(runner.release)
	if first := <-firstDone; first.Code != http.StatusCreated {
		t.Fatalf("normal status = %d, body = %s", first.Code, first.Body.String())
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
