package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"visitready/internal/agent"
	"visitready/internal/auth"
	"visitready/internal/domain"
	"visitready/internal/exporter"
	"visitready/internal/guard"
	"visitready/internal/session"
)

const maxRequestBytes = 32 << 10

const (
	defaultWorkflowTimeout = 45 * time.Second
	defaultConcurrentRuns  = 4
	defaultRequestsMinute  = 12
	defaultEmergencyMinute = 60
)

type Runner interface {
	Start(ctx context.Context, input string, allowWebSearch bool) (domain.Session, error)
	Resume(ctx context.Context, item domain.Session, answer string) (domain.Session, error)
	Confirm(item domain.Session, visitGoal string) (domain.Session, error)
}

type retryRunner interface {
	Retry(ctx context.Context, item domain.Session) (domain.Session, error)
}

type Store interface {
	Create(item domain.Session) error
	Get(id string, now time.Time) (domain.Session, error)
	Replace(item domain.Session) error
	ReplaceIfStatus(item domain.Session, expected domain.AgentStatus) (domain.Session, error)
	Delete(id string) error
	ListByOwner(ownerHash string, now time.Time, limit int) ([]domain.Session, error)
}

type contextStore interface {
	CreateContext(context.Context, domain.Session) error
	ReplaceIfStatusContext(context.Context, domain.Session, domain.AgentStatus) (domain.Session, error)
	DeleteContext(context.Context, string) error
}

type Config struct {
	Runner                     Runner
	Store                      Store
	Logger                     *slog.Logger
	Now                        func() time.Time
	WorkflowTimeout            time.Duration
	MaxConcurrentRuns          int
	RequestsPerMinute          int
	EmergencyRequestsPerMinute int
	RequireDeviceAuth          bool
	Accounts                   *auth.Service
	RequireAccountAuth         bool
	SecureCookies              bool
}

type handler struct {
	runner             Runner
	store              Store
	logger             *slog.Logger
	now                func() time.Time
	workflowTimeout    time.Duration
	runSlots           chan struct{}
	limiter            *clientLimiter
	emergencyLimiter   *clientLimiter
	locks              keyedLocks
	requireDeviceAuth  bool
	accounts           *auth.Service
	requireAccountAuth bool
	secureCookies      bool
}

func (h *handler) createSession(ctx context.Context, item domain.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store, ok := h.store.(contextStore); ok {
		return store.CreateContext(ctx, item)
	}
	return h.store.Create(item)
}

func (h *handler) replaceSession(ctx context.Context, item domain.Session, expected domain.AgentStatus) (domain.Session, error) {
	if err := ctx.Err(); err != nil {
		return domain.Session{}, err
	}
	if store, ok := h.store.(contextStore); ok {
		return store.ReplaceIfStatusContext(ctx, item, expected)
	}
	return h.store.ReplaceIfStatus(item, expected)
}

func (h *handler) deleteSession(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store, ok := h.store.(contextStore); ok {
		return store.DeleteContext(ctx, id)
	}
	return h.store.Delete(id)
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

type recoverableErrorResponse struct {
	Success bool           `json:"success"`
	Data    domain.Session `json:"data"`
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
	if cfg.RequireAccountAuth && cfg.Accounts == nil {
		return nil, errors.New("account service is required when account authentication is enabled")
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
	if cfg.EmergencyRequestsPerMinute <= 0 {
		cfg.EmergencyRequestsPerMinute = defaultEmergencyMinute
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &handler{
		runner: cfg.Runner, store: cfg.Store, logger: cfg.Logger, now: cfg.Now,
		workflowTimeout:    cfg.WorkflowTimeout,
		runSlots:           make(chan struct{}, cfg.MaxConcurrentRuns),
		limiter:            newClientLimiter(cfg.RequestsPerMinute),
		emergencyLimiter:   newClientLimiter(cfg.EmergencyRequestsPerMinute),
		requireDeviceAuth:  cfg.RequireDeviceAuth,
		accounts:           cfg.Accounts,
		requireAccountAuth: cfg.RequireAccountAuth,
		secureCookies:      cfg.SecureCookies,
	}, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := newRequestID()
	setSecurityHeaders(w, requestID)
	if isAccountAPIPath(r.URL.Path) {
		h.accountRoute(w, r, requestID)
		return
	}
	if h.requireAccountAuth && isSessionAPIPath(r.URL.Path) {
		var ok bool
		r, ok = h.secureAccountSessionRequest(w, r, requestID)
		if !ok {
			return
		}
	} else if h.requireDeviceAuth && isSessionAPIPath(r.URL.Path) {
		var ok bool
		r, ok = secureSessionRequest(w, r, requestID)
		if !ok {
			return
		}
	}
	if wantsProgressStream(r) {
		h.serveProgressStream(w, r, requestID)
		return
	}
	h.route(w, r, requestID)
}

func (h *handler) route(w http.ResponseWriter, r *http.Request, requestID string) {
	switch {
	case r.URL.Path == "/livez":
		h.liveness(w, r, requestID)
	case r.URL.Path == "/readyz":
		h.readiness(w, r, requestID)
	case r.URL.Path == "/healthz":
		h.readiness(w, r, requestID)
	case r.URL.Path == "/api/v1/sessions":
		if r.Method == http.MethodGet {
			h.list(w, r, requestID)
		} else {
			h.create(w, r, requestID)
		}
	case strings.HasPrefix(r.URL.Path, "/api/v1/sessions/"):
		h.sessionRoute(w, r, requestID)
	default:
		writeError(w, http.StatusNotFound, "not_found", "请求的资源不存在。", requestID)
	}
}

type sessionSummary struct {
	ID        string             `json:"id"`
	Status    domain.AgentStatus `json:"status"`
	VisitGoal string             `json:"visit_goal,omitempty"`
	CreatedAt time.Time          `json:"created_at"`
	ExpiresAt time.Time          `json:"expires_at"`
}

func (h *handler) list(w http.ResponseWriter, r *http.Request, requestID string) {
	items, err := h.store.ListByOwner(sessionOwnerFromContext(r.Context()), h.now(), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_store_failed", "暂时无法读取往期会话。", requestID)
		return
	}
	summaries := make([]sessionSummary, 0, len(items))
	for _, item := range items {
		summaries = append(summaries, sessionSummary{ID: item.ID, Status: item.Status, VisitGoal: item.VisitGoal, CreatedAt: item.CreatedAt, ExpiresAt: item.ExpiresAt})
	}
	writeJSON(w, http.StatusOK, struct {
		Success   bool             `json:"success"`
		Data      []sessionSummary `json:"data"`
		RequestID string           `json:"request_id"`
	}{Success: true, Data: summaries, RequestID: requestID})
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
	emergency := len(guard.DetectEmergencySignals(input.Input)) > 0
	if len(guard.ScanPII(input.Input)) > 0 && !emergency {
		writeError(w, http.StatusBadRequest, "direct_identifier_detected", "描述中可能包含姓名、电话、证件号或详细地址，请移除后重试。", requestID)
		return
	}
	ctx := r.Context()
	release := func() {}
	if emergency {
		if !h.emergencyLimiter.allow(clientAddress(r), h.now()) {
			writeError(w, http.StatusTooManyRequests, "emergency_rate_limited", guard.EmergencyEscalationMessage+" 请求过于频繁，本次摘要未保存。", requestID)
			return
		}
		if !h.limiter.allow(clientAddress(r), h.now()) {
			writeError(w, http.StatusTooManyRequests, "emergency_not_saved", guard.EmergencyEscalationMessage+" 已达到会话保存频率上限，本次摘要未保存。", requestID)
			return
		}
	} else {
		if !h.allowRun(w, r, requestID) {
			return
		}
		var ok bool
		ctx, release, ok = h.beginRun(w, r, requestID)
		if !ok {
			return
		}
	}
	defer release()
	item, err := h.runner.Start(ctx, input.Input, input.AllowWebSearch)
	if h.requireDeviceAuth || h.requireAccountAuth {
		item.OwnerHash = sessionOwnerFromContext(r.Context())
	}
	if err != nil {
		h.logRequestCancel(r, requestID, "start")
		if errors.Is(r.Context().Err(), context.Canceled) {
			return
		}
		if h.storeCreatedFailure(r.Context(), w, item, err, requestID) {
			return
		}
		h.writeRunnerError(w, err, requestID)
		return
	}
	if ctx.Err() != nil {
		h.logRequestCancel(r, requestID, "start_commit")
		if !errors.Is(r.Context().Err(), context.Canceled) {
			h.writeRunnerError(w, ctx.Err(), requestID)
		}
		return
	}
	if err := h.createSession(ctx, item); err != nil {
		if errors.Is(err, context.Canceled) {
			h.logRequestCancel(r, requestID, "start_store")
			return
		}
		if emergency {
			writeError(w, http.StatusServiceUnavailable, "emergency_not_saved", guard.EmergencyEscalationMessage+" 本次摘要未能保存。", requestID)
			return
		}
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
		if r.Method == http.MethodDelete {
			h.delete(w, r, requestID, id)
		} else {
			h.get(w, r, requestID, id)
		}
		return
	}
	switch parts[1] {
	case "clarifications":
		h.clarify(w, r, requestID, id)
	case "confirm":
		h.confirm(w, r, requestID, id)
	case "retry":
		h.retry(w, r, requestID, id)
	case "export":
		h.export(w, r, requestID, id)
	default:
		writeError(w, http.StatusNotFound, "not_found", "请求的资源不存在。", requestID)
	}
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request, requestID, id string) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w, http.MethodDelete, requestID)
		return
	}
	unlock := h.locks.lock(id)
	defer unlock()
	if _, ok := h.loadSession(w, r, requestID, id); !ok {
		return
	}
	if err := h.deleteSession(r.Context(), id); err != nil {
		if errors.Is(err, context.Canceled) {
			h.logRequestCancel(r, requestID, "delete_store")
			return
		}
		if errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrExpired) {
			writeError(w, http.StatusNotFound, "session_not_found", "会话不存在或已过期。", requestID)
			return
		}
		writeError(w, http.StatusInternalServerError, "session_delete_failed", "暂时无法删除这次会话，请稍后重试。", requestID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request, requestID, id string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet, requestID)
		return
	}
	item, ok := h.loadSession(w, r, requestID, id)
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
	directSignals := guard.DetectEmergencySignals(input.Answer)
	item, err := h.getOwnedSession(r, id)
	if err != nil {
		if len(directSignals) > 0 {
			writeError(w, http.StatusServiceUnavailable, "emergency_session_unavailable", guard.EmergencyEscalationMessage+" 本次摘要未能保存。", requestID)
			return
		}
		h.writeLoadError(w, err, requestID)
		return
	}
	if item.Status == domain.StatusEmergency {
		if len(directSignals) > 0 {
			writeJSON(w, http.StatusOK, successResponse{Success: true, Data: item, RequestID: requestID})
			return
		}
		writeError(w, http.StatusConflict, "invalid_session_state", guard.EmergencyEscalationMessage+" 紧急安全处理已结束本次 Agent 流程。", requestID)
		return
	}
	if item.Status != domain.StatusWaitingClarification {
		message := "当前会话不在等待补充状态，无法继续追问。"
		if len(directSignals) > 0 {
			message = guard.EmergencyEscalationMessage + " " + message
		}
		writeError(w, http.StatusConflict, "invalid_session_state", message, requestID)
		return
	}
	if len(directSignals) > 0 {
		h.transitionToEmergency(w, r, requestID, item, directSignals)
		return
	}
	questions := append([]domain.Question(nil), item.ClarificationPrompts...)
	if len(questions) == 0 {
		for _, question := range item.ClarificationQuestions {
			questions = append(questions, domain.Question{Text: question})
		}
	}
	emergencySignals := guard.DetectEmergencyAnswer(input.Answer, questions)
	if len(emergencySignals) > 0 {
		h.transitionToEmergency(w, r, requestID, item, emergencySignals)
		return
	}
	if !h.allowRun(w, r, requestID) {
		return
	}
	unlock := h.locks.lock(id)
	defer unlock()
	item, ok := h.loadSession(w, r, requestID, id)
	if !ok {
		return
	}
	if item.Status == domain.StatusEmergency {
		writeJSON(w, http.StatusOK, successResponse{Success: true, Data: item, RequestID: requestID})
		return
	}
	if item.Status != domain.StatusWaitingClarification {
		writeError(w, http.StatusConflict, "session_state_changed", "会话已在另一请求中更新，请重新查看后再继续。", requestID)
		return
	}
	ctx, release, ok := h.beginRun(w, r, requestID)
	if !ok {
		return
	}
	defer release()
	next, err := h.runner.Resume(ctx, item, input.Answer)
	if err != nil {
		h.logRequestCancel(r, requestID, "resume")
		if errors.Is(r.Context().Err(), context.Canceled) {
			return
		}
		if h.replaceFailedRun(r.Context(), w, next, item.Status, err, requestID) {
			return
		}
		h.writeRunnerError(w, err, requestID)
		return
	}
	if ctx.Err() != nil {
		h.logRequestCancel(r, requestID, "resume_commit")
		if !errors.Is(r.Context().Err(), context.Canceled) {
			h.writeRunnerError(w, ctx.Err(), requestID)
		}
		return
	}
	stored, err := h.replaceSession(ctx, next, item.Status)
	if errors.Is(err, context.Canceled) {
		h.logRequestCancel(r, requestID, "resume_store")
		return
	}
	if errors.Is(err, session.ErrStateChanged) {
		if stored.Status == domain.StatusEmergency {
			writeJSON(w, http.StatusOK, successResponse{Success: true, Data: stored, RequestID: requestID})
			return
		}
		writeError(w, http.StatusConflict, "session_state_changed", "会话已在另一请求中更新，请重新查看后再继续。", requestID)
		return
	}
	if err != nil {
		if next.Status == domain.StatusEmergency {
			writeError(w, http.StatusServiceUnavailable, "emergency_not_saved", guard.EmergencyEscalationMessage+" 本次摘要未能保存。", requestID)
			return
		}
		h.writeReplaceError(w, err, requestID)
		return
	}
	writeJSON(w, http.StatusOK, successResponse{Success: true, Data: stored, RequestID: requestID})
}

func (h *handler) retry(w http.ResponseWriter, r *http.Request, requestID, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost, requestID)
		return
	}
	runner, ok := h.runner.(retryRunner)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "recovery_unavailable", "当前服务未启用会话恢复。", requestID)
		return
	}
	unlock := h.locks.lock(id)
	defer unlock()
	item, ok := h.loadSession(w, r, requestID, id)
	if !ok {
		return
	}
	if item.Status != domain.StatusFailed || item.Failure == nil || !item.Failure.Retryable {
		writeError(w, http.StatusConflict, "retry_not_available", "当前会话没有可恢复的处理任务。", requestID)
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
	next, runErr := runner.Retry(ctx, item)
	if runErr != nil {
		h.logRequestCancel(r, requestID, "retry")
		if errors.Is(r.Context().Err(), context.Canceled) {
			return
		}
		if h.replaceFailedRun(r.Context(), w, next, domain.StatusFailed, runErr, requestID) {
			return
		}
		h.writeRunnerError(w, runErr, requestID)
		return
	}
	if ctx.Err() != nil {
		h.logRequestCancel(r, requestID, "retry_commit")
		if !errors.Is(r.Context().Err(), context.Canceled) {
			h.writeRunnerError(w, ctx.Err(), requestID)
		}
		return
	}
	stored, err := h.replaceSession(ctx, next, domain.StatusFailed)
	if errors.Is(err, context.Canceled) {
		h.logRequestCancel(r, requestID, "retry_store")
		return
	}
	if errors.Is(err, session.ErrStateChanged) {
		writeError(w, http.StatusConflict, "session_state_changed", "会话已在另一请求中恢复，请重新查看。", requestID)
		return
	}
	if err != nil {
		h.writeReplaceError(w, err, requestID)
		return
	}
	writeJSON(w, http.StatusOK, successResponse{Success: true, Data: stored, RequestID: requestID})
}

func (h *handler) storeCreatedFailure(ctx context.Context, w http.ResponseWriter, item domain.Session, runErr error, requestID string) bool {
	if !isRecoverableRun(item, runErr) {
		return false
	}
	if !item.Failure.Retryable && item.Failure.Code != domain.FailureExhausted {
		writeError(w, http.StatusBadGateway, domain.FailurePermanent, "本次处理未能完成，可以重试或换一种方式描述。", requestID)
		return true
	}
	if err := h.createSession(ctx, item); err != nil {
		if errors.Is(err, context.Canceled) {
			return true
		}
		if errors.Is(err, session.ErrCapacity) {
			writeError(w, http.StatusServiceUnavailable, "session_capacity_reached", "当前会话较多，请稍后重试。", requestID)
			return true
		}
		h.writeReplaceError(w, err, requestID)
		return true
	}
	h.writeRecoverableError(w, item, requestID)
	return true
}

func (h *handler) replaceFailedRun(ctx context.Context, w http.ResponseWriter, item domain.Session, expected domain.AgentStatus, runErr error, requestID string) bool {
	if !isRecoverableRun(item, runErr) {
		return false
	}
	stored, err := h.replaceSession(ctx, item, expected)
	if errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, session.ErrStateChanged) {
		writeError(w, http.StatusConflict, "session_state_changed", "会话已在另一请求中更新，请重新查看。", requestID)
		return true
	}
	if err != nil {
		h.writeReplaceError(w, err, requestID)
		return true
	}
	h.writeRecoverableError(w, stored, requestID)
	return true
}

func isRecoverableRun(item domain.Session, err error) bool {
	return errors.Is(err, agent.ErrUpstream) && item.ID != "" && item.Status == domain.StatusFailed && item.Failure != nil
}

func (h *handler) writeRecoverableError(w http.ResponseWriter, item domain.Session, requestID string) {
	response := recoverableErrorResponse{Success: false, Data: item, RequestID: requestID}
	switch {
	case item.Failure.Retryable:
		response.Error.Code = "agent_retryable_failure"
		w.Header().Set("Retry-After", "1")
	case item.Failure.Code == domain.FailureExhausted:
		response.Error.Code = domain.FailureExhausted
	default:
		response.Error.Code = domain.FailurePermanent
	}
	response.Error.Message = item.Failure.Message
	writeJSON(w, http.StatusServiceUnavailable, response)
}

func (h *handler) transitionToEmergency(w http.ResponseWriter, r *http.Request, requestID string, item domain.Session, signals []domain.RiskSignal) {
	if !h.emergencyLimiter.allow(clientAddress(r), h.now()) {
		writeError(w, http.StatusTooManyRequests, "emergency_rate_limited", guard.EmergencyEscalationMessage+" 请求过于频繁，本次摘要未保存。", requestID)
		return
	}
	next := h.emergencySession(item, signals)
	stored, err := h.replaceSession(r.Context(), next, domain.StatusWaitingClarification)
	if errors.Is(err, context.Canceled) {
		h.logRequestCancel(r, requestID, "emergency_store")
		return
	}
	if errors.Is(err, session.ErrStateChanged) {
		if stored.Status == domain.StatusEmergency {
			writeJSON(w, http.StatusOK, successResponse{Success: true, Data: stored, RequestID: requestID})
			return
		}
		writeError(w, http.StatusConflict, "session_state_changed", guard.EmergencyEscalationMessage+" 会话已在另一请求中更新，本次摘要未保存。", requestID)
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "emergency_not_saved", guard.EmergencyEscalationMessage+" 本次摘要未能保存。", requestID)
		return
	}
	writeJSON(w, http.StatusOK, successResponse{Success: true, Data: stored, RequestID: requestID})
}

func (h *handler) emergencySession(item domain.Session, signals []domain.RiskSignal) domain.Session {
	item.Status = domain.StatusEmergency
	item.RawInput = ""
	item.Clarification = ""
	item.ClarificationTurns = nil
	item.EmergencyMessage = guard.EmergencyEscalationMessage
	item.RiskSignals = append([]domain.RiskSignal(nil), signals...)
	item.ClarificationQuestions = nil
	item.ClarificationPrompts = nil
	item.Questions = nil
	item.ActionItems = nil
	item.Sources = nil
	item.Events = append(item.Events, domain.AgentEvent{
		Step: "emergency", Status: "completed", Message: "已触发紧急安全处理，停止后续 Agent 步骤", CreatedAt: h.now(),
	})
	return item
}

func (h *handler) confirm(w http.ResponseWriter, r *http.Request, requestID, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost, requestID)
		return
	}
	var input struct {
		VisitGoal            string `json:"visit_goal"`
		FactsAcknowledged    bool   `json:"facts_acknowledged"`
		FactsDigest          string `json:"facts_digest"`
		InsightsAcknowledged bool   `json:"insights_acknowledged"`
		ReviewDigest         string `json:"review_digest"`
	}
	if !decodeRequest(w, r, &input, requestID) {
		return
	}
	unlock := h.locks.lock(id)
	defer unlock()
	item, ok := h.loadSession(w, r, requestID, id)
	if !ok {
		return
	}
	if !input.FactsAcknowledged || input.FactsDigest != domain.FactReviewDigest(item.Facts) {
		writeError(w, http.StatusConflict, "fact_review_required", "请核对当前版本的全部事实后再确认。", requestID)
		return
	}
	if !input.InsightsAcknowledged || input.ReviewDigest != domain.ReviewDigest(item) {
		writeError(w, http.StatusConflict, "insight_review_required", "请核对当前版本的症状画像、安全提示和准备建议后再确认。", requestID)
		return
	}
	next, err := h.runner.Confirm(item, input.VisitGoal)
	if err != nil {
		h.writeRunnerError(w, err, requestID)
		return
	}
	if r.Context().Err() != nil {
		h.logRequestCancel(r, requestID, "confirm_commit")
		return
	}
	stored, err := h.replaceSession(r.Context(), next, item.Status)
	if errors.Is(err, context.Canceled) {
		h.logRequestCancel(r, requestID, "confirm_store")
		return
	}
	if errors.Is(err, session.ErrStateChanged) {
		if stored.Status == domain.StatusEmergency {
			writeError(w, http.StatusConflict, "invalid_session_state", guard.EmergencyEscalationMessage+" 紧急安全处理已结束本次 Agent 流程。", requestID)
			return
		}
		writeError(w, http.StatusConflict, "session_state_changed", "会话已在另一请求中更新，请重新查看后再确认。", requestID)
		return
	}
	if err != nil {
		h.writeReplaceError(w, err, requestID)
		return
	}
	writeJSON(w, http.StatusOK, successResponse{Success: true, Data: stored, RequestID: requestID})
}

func (h *handler) export(w http.ResponseWriter, r *http.Request, requestID, id string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet, requestID)
		return
	}
	item, ok := h.loadSession(w, r, requestID, id)
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
