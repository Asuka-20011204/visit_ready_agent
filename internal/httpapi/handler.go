package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"visitready/internal/agent"
	"visitready/internal/domain"
	"visitready/internal/exporter"
	"visitready/internal/session"
)

const maxRequestBytes = 32 << 10

const (
	defaultWorkflowTimeout = 45 * time.Second
	defaultConcurrentRuns  = 4
	defaultRequestsMinute  = 12
)

type Runner interface {
	Start(ctx context.Context, input string, allowWebSearch bool) (domain.Session, error)
	Resume(ctx context.Context, item domain.Session, answer string) (domain.Session, error)
	Confirm(item domain.Session, visitGoal string) (domain.Session, error)
}

type Store interface {
	Create(item domain.Session) error
	Get(id string, now time.Time) (domain.Session, error)
	Replace(item domain.Session) error
}

type Config struct {
	Runner            Runner
	Store             Store
	Now               func() time.Time
	WorkflowTimeout   time.Duration
	MaxConcurrentRuns int
	RequestsPerMinute int
}

type handler struct {
	runner          Runner
	store           Store
	now             func() time.Time
	workflowTimeout time.Duration
	runSlots        chan struct{}
	limiter         *clientLimiter
	locks           keyedLocks
}

type successResponse struct {
	Success   bool           `json:"success"`
	Data      domain.Session `json:"data"`
	RequestID string         `json:"request_id"`
}

type errorResponse struct {
	Success bool `json:"success"`
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

func New(cfg Config) (http.Handler, error) {
	if cfg.Runner == nil {
		return nil, errors.New("runner is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("session store is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.WorkflowTimeout <= 0 {
		cfg.WorkflowTimeout = defaultWorkflowTimeout
	}
	if cfg.MaxConcurrentRuns <= 0 {
		cfg.MaxConcurrentRuns = defaultConcurrentRuns
	}
	if cfg.RequestsPerMinute <= 0 {
		cfg.RequestsPerMinute = defaultRequestsMinute
	}
	return &handler{
		runner: cfg.Runner, store: cfg.Store, now: cfg.Now,
		workflowTimeout: cfg.WorkflowTimeout,
		runSlots:        make(chan struct{}, cfg.MaxConcurrentRuns),
		limiter:         newClientLimiter(cfg.RequestsPerMinute),
	}, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := newRequestID()
	setSecurityHeaders(w, requestID)

	switch {
	case r.URL.Path == "/healthz":
		h.health(w, r, requestID)
	case r.URL.Path == "/api/v1/sessions":
		h.create(w, r, requestID)
	case strings.HasPrefix(r.URL.Path, "/api/v1/sessions/"):
		h.sessionRoute(w, r, requestID)
	default:
		writeError(w, http.StatusNotFound, "not_found", "请求的资源不存在。", requestID)
	}
}

func (h *handler) health(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet, requestID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "request_id": requestID})
}

func (h *handler) create(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost, requestID)
		return
	}
	var input struct {
		Input            string `json:"input"`
		AllowWebSearch   bool   `json:"allow_web_search"`
		PrivacyConfirmed bool   `json:"privacy_confirmed"`
	}
	if !decodeRequest(w, r, &input, requestID) {
		return
	}
	if !input.PrivacyConfirmed {
		writeError(w, http.StatusBadRequest, "privacy_confirmation_required", "请先确认已移除姓名、电话、证件号和详细地址。", requestID)
		return
	}
	if !h.allowRun(w, r, requestID) {
		return
	}
	ctx, release, ok := h.beginRun(w, r, requestID)
	if !ok {
		return
	}
	defer release()
	item, err := h.runner.Start(ctx, input.Input, input.AllowWebSearch)
	if err != nil {
		writeRunnerError(w, err, requestID)
		return
	}
	if err := h.store.Create(item); err != nil {
		if errors.Is(err, session.ErrCapacity) {
			writeError(w, http.StatusServiceUnavailable, "session_capacity_reached", "当前会话较多，请稍后重试。", requestID)
			return
		}
		writeError(w, http.StatusInternalServerError, "session_store_failed", "暂时无法保存本次会话，请稍后重试。", requestID)
		return
	}
	writeJSON(w, http.StatusCreated, successResponse{Success: true, Data: item, RequestID: requestID})
}

func (h *handler) sessionRoute(w http.ResponseWriter, r *http.Request, requestID string) {
	remainder := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	parts := strings.Split(strings.Trim(remainder, "/"), "/")
	if len(parts) < 1 || len(parts) > 2 || !validSessionID(parts[0]) {
		writeError(w, http.StatusNotFound, "not_found", "请求的资源不存在。", requestID)
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		h.get(w, r, requestID, id)
		return
	}
	switch parts[1] {
	case "clarifications":
		h.clarify(w, r, requestID, id)
	case "confirm":
		h.confirm(w, r, requestID, id)
	case "export":
		h.export(w, r, requestID, id)
	default:
		writeError(w, http.StatusNotFound, "not_found", "请求的资源不存在。", requestID)
	}
}

func (h *handler) get(w http.ResponseWriter, r *http.Request, requestID, id string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet, requestID)
		return
	}
	item, ok := h.loadSession(w, requestID, id)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, successResponse{Success: true, Data: item, RequestID: requestID})
}

func (h *handler) clarify(w http.ResponseWriter, r *http.Request, requestID, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost, requestID)
		return
	}
	var input struct {
		Answer string `json:"answer"`
	}
	if !decodeRequest(w, r, &input, requestID) {
		return
	}
	if !h.allowRun(w, r, requestID) {
		return
	}
	unlock := h.locks.lock(id)
	defer unlock()
	item, ok := h.loadSession(w, requestID, id)
	if !ok {
		return
	}
	ctx, release, ok := h.beginRun(w, r, requestID)
	if !ok {
		return
	}
	defer release()
	next, err := h.runner.Resume(ctx, item, input.Answer)
	if err != nil {
		writeRunnerError(w, err, requestID)
		return
	}
	if err := h.store.Replace(next); err != nil {
		h.writeReplaceError(w, err, requestID)
		return
	}
	writeJSON(w, http.StatusOK, successResponse{Success: true, Data: next, RequestID: requestID})
}

func (h *handler) confirm(w http.ResponseWriter, r *http.Request, requestID, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost, requestID)
		return
	}
	var input struct {
		VisitGoal string `json:"visit_goal"`
	}
	if !decodeRequest(w, r, &input, requestID) {
		return
	}
	unlock := h.locks.lock(id)
	defer unlock()
	item, ok := h.loadSession(w, requestID, id)
	if !ok {
		return
	}
	next, err := h.runner.Confirm(item, input.VisitGoal)
	if err != nil {
		writeRunnerError(w, err, requestID)
		return
	}
	if err := h.store.Replace(next); err != nil {
		h.writeReplaceError(w, err, requestID)
		return
	}
	writeJSON(w, http.StatusOK, successResponse{Success: true, Data: next, RequestID: requestID})
}

func (h *handler) export(w http.ResponseWriter, r *http.Request, requestID, id string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet, requestID)
		return
	}
	item, ok := h.loadSession(w, requestID, id)
	if !ok {
		return
	}
	data, err := exporter.Markdown(item)
	if err != nil {
		writeError(w, http.StatusConflict, "confirmation_required", "确认核对后才能导出。", requestID)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="visit-ready.md"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *handler) loadSession(w http.ResponseWriter, requestID, id string) (domain.Session, bool) {
	item, err := h.store.Get(id, h.now())
	if err != nil {
		if errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrExpired) {
			writeError(w, http.StatusNotFound, "session_not_found", "会话不存在或已过期，请重新开始。", requestID)
			return domain.Session{}, false
		}
		writeError(w, http.StatusInternalServerError, "session_store_failed", "暂时无法读取本次会话。", requestID)
		return domain.Session{}, false
	}
	return item, true
}

func (h *handler) allowRun(w http.ResponseWriter, r *http.Request, requestID string) bool {
	if !h.limiter.allow(clientAddress(r), h.now()) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后再试。", requestID)
		return false
	}
	return true
}

func (h *handler) beginRun(w http.ResponseWriter, r *http.Request, requestID string) (context.Context, func(), bool) {
	select {
	case h.runSlots <- struct{}{}:
		ctx, cancel := context.WithTimeout(r.Context(), h.workflowTimeout)
		return ctx, func() {
			cancel()
			<-h.runSlots
		}, true
	default:
		writeError(w, http.StatusServiceUnavailable, "agent_busy", "Agent 正在处理其他请求，请稍后重试。", requestID)
		return nil, nil, false
	}
}

func (h *handler) writeReplaceError(w http.ResponseWriter, err error, requestID string) {
	if errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrExpired) {
		writeError(w, http.StatusGone, "session_expired", "会话已过期，请重新开始。", requestID)
		return
	}
	writeError(w, http.StatusInternalServerError, "session_store_failed", "暂时无法更新本次会话，请稍后重试。", requestID)
}

func decodeRequest(w http.ResponseWriter, r *http.Request, target any, requestID string) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "content_type_required", "请求必须使用 application/json。", requestID)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "请求内容不能超过 32 KiB。", requestID)
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "请求 JSON 无效或包含未知字段。", requestID)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json", "请求只能包含一个 JSON 对象。", requestID)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message, requestID string) {
	response := errorResponse{Success: false, RequestID: requestID}
	response.Error.Code = code
	response.Error.Message = message
	writeJSON(w, status, response)
}

func methodNotAllowed(w http.ResponseWriter, allow, requestID string) {
	w.Header().Set("Allow", allow)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "该地址不支持当前请求方法。", requestID)
}

func setSecurityHeaders(w http.ResponseWriter, requestID string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Request-ID", requestID)
}

func newRequestID() string {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return "request-unavailable"
	}
	return hex.EncodeToString(data)
}

func validSessionID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, char := range id {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func userInputError(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "personal identifiers"):
		return "输入中可能包含姓名、电话、邮箱、证件号或详细地址，请脱敏后重试。"
	case strings.Contains(message, "characters"):
		return "请输入规定长度内的脱敏健康描述。"
	default:
		return "暂时无法处理该输入，请检查内容后重试。"
	}
}

func writeRunnerError(w http.ResponseWriter, err error, requestID string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "agent_timeout", "AI 服务响应超时，请稍后重试。", requestID)
	case errors.Is(err, agent.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_input", userInputError(err), requestID)
	case errors.Is(err, agent.ErrInvalidState):
		writeError(w, http.StatusConflict, "invalid_session_state", "当前会话状态不支持该操作。", requestID)
	default:
		writeError(w, http.StatusBadGateway, "agent_upstream_failed", "AI 服务暂时不可用，请稍后重试。", requestID)
	}
}
