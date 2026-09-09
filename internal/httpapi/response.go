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

	"visitready/internal/agent"
	"visitready/internal/domain"
	"visitready/internal/session"
)

func (h *handler) loadSession(w http.ResponseWriter, r *http.Request, requestID, id string) (domain.Session, bool) {
	item, err := h.getOwnedSession(r, id)
	if err != nil {
		h.writeLoadError(w, err, requestID)
		return domain.Session{}, false
	}
	return item, true
}

func (h *handler) writeLoadError(w http.ResponseWriter, err error, requestID string) {
	if errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrExpired) {
		writeError(w, http.StatusNotFound, "session_not_found", "会话不存在或已过期，请重新开始。", requestID)
		return
	}
	writeError(w, http.StatusInternalServerError, "session_store_failed", "暂时无法读取本次会话。", requestID)
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

func (h *handler) logRequestCancel(r *http.Request, requestID, phase string) {
	if cause := r.Context().Err(); cause != nil {
		h.logger.Warn("request_context_canceled",
			"phase", phase,
			"request_err", cause,
			"request_id", requestID,
			"hint", "client disconnected while the workflow was running",
		)
	}
}

func (h *handler) writeRunnerError(w http.ResponseWriter, err error, requestID string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "agent_timeout", "AI 服务响应超时，请稍后重试。", requestID)
	case errors.Is(err, agent.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_input", userInputError(err), requestID)
	case errors.Is(err, agent.ErrInvalidState):
		writeError(w, http.StatusConflict, "invalid_session_state", "当前会话状态不支持该操作。", requestID)
	default:
		h.logger.Error("agent workflow failed",
			"error", err,
			"request_id", requestID,
			"code", "agent_upstream_failed",
		)
		writeError(w, http.StatusBadGateway, "agent_upstream_failed", "AI 服务暂时不可用，请稍后重试。", requestID)
	}
}
