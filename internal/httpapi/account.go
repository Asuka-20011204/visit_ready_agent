package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"visitready/internal/auth"
)

func isAccountAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/auth/")
}

func (h *handler) accountRoute(w http.ResponseWriter, r *http.Request, requestID string) {
	if h.accounts == nil {
		writeError(w, http.StatusNotFound, "not_found", "请求的资源不存在。", requestID)
		return
	}
	if isStateChanging(r.Method) && !hasSameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross_site_request", "已拒绝来自其他站点的账户操作。", requestID)
		return
	}
	if isStateChanging(r.Method) && !h.allowRun(w, r, requestID) {
		return
	}
	switch r.URL.Path {
	case "/api/v1/auth/register":
		h.registerAccount(w, r, requestID)
	case "/api/v1/auth/login":
		h.loginAccount(w, r, requestID)
	case "/api/v1/auth/logout":
		h.logoutAccount(w, r, requestID)
	case "/api/v1/auth/me":
		h.currentAccount(w, r, requestID)
	default:
		writeError(w, http.StatusNotFound, "not_found", "请求的资源不存在。", requestID)
	}
}

func (h *handler) registerAccount(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost, requestID)
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeRequest(w, r, &input, requestID) {
		return
	}
	account, err := h.accounts.Register(r.Context(), input.Email, input.Password)
	if err != nil {
		code := "invalid_registration"
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrEmailExists) {
			code, status = "email_already_registered", http.StatusConflict
		}
		writeError(w, status, code, "无法创建账户，请检查邮箱和密码。", requestID)
		return
	}
	token, _, err := h.accounts.Login(r.Context(), input.Email, input.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "account_session_failed", "账户已创建，但登录会话未能建立。", requestID)
		return
	}
	h.setAuthCookie(w, token)
	writeJSON(w, http.StatusCreated, struct {
		Success   bool         `json:"success"`
		Data      auth.Account `json:"data"`
		RequestID string       `json:"request_id"`
	}{true, account, requestID})
}

func (h *handler) loginAccount(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost, requestID)
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeRequest(w, r, &input, requestID) {
		return
	}
	token, account, err := h.accounts.Login(r.Context(), input.Email, input.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "邮箱或密码不正确。", requestID)
		return
	}
	h.setAuthCookie(w, token)
	writeJSON(w, http.StatusOK, struct {
		Success   bool         `json:"success"`
		Data      auth.Account `json:"data"`
		RequestID string       `json:"request_id"`
	}{true, account, requestID})
}

func (h *handler) logoutAccount(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost, requestID)
		return
	}
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		if err := h.accounts.Logout(r.Context(), cookie.Value); err != nil && !errors.Is(err, context.Canceled) {
			writeError(w, http.StatusInternalServerError, "logout_failed", "退出登录失败，请稍后重试。", requestID)
			return
		}
	}
	h.clearAuthCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) currentAccount(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet, requestID)
		return
	}
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "account_required", "请先登录账户。", requestID)
		return
	}
	account, err := h.accounts.Authenticate(r.Context(), cookie.Value)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "account_required", "登录已失效，请重新登录。", requestID)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Success   bool         `json:"success"`
		Data      auth.Account `json:"data"`
		RequestID string       `json:"request_id"`
	}{true, account, requestID})
}

func (h *handler) setAuthCookie(w http.ResponseWriter, token string) {
	secure := h.secureCookies
	maxAge := int((7 * 24 * time.Hour) / time.Second)
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookieName, Value: token, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

func (h *handler) clearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: h.secureCookies, SameSite: http.SameSiteLaxMode})
}
