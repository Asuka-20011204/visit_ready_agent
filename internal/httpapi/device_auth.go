package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"

	"visitready/internal/auth"
	"visitready/internal/domain"
	"visitready/internal/session"
)

const DeviceKeyHeader = "X-VisitReady-Device-Key"

type deviceOwnerContextKey struct{}
type accountOwnerContextKey struct{}

func secureSessionRequest(w http.ResponseWriter, r *http.Request, requestID string) (*http.Request, bool) {
	rawKey := strings.TrimSpace(r.Header.Get(DeviceKeyHeader))
	decoded, err := base64.RawURLEncoding.DecodeString(rawKey)
	if err != nil || len(decoded) != 32 {
		writeError(w, http.StatusUnauthorized, "device_key_required", "缺少有效的设备访问密钥，请刷新页面后重试。", requestID)
		return r, false
	}
	if isStateChanging(r.Method) && !hasSameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross_site_request", "已拒绝来自其他站点的会话操作。", requestID)
		return r, false
	}
	digest := sha256.Sum256(decoded)
	ctx := context.WithValue(r.Context(), deviceOwnerContextKey{}, hex.EncodeToString(digest[:]))
	return r.WithContext(ctx), true
}

func isSessionAPIPath(path string) bool {
	return path == "/api/v1/sessions" || strings.HasPrefix(path, "/api/v1/sessions/")
}

func isStateChanging(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func hasSameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host) && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func deviceOwnerFromContext(ctx context.Context) string {
	owner, _ := ctx.Value(deviceOwnerContextKey{}).(string)
	return owner
}

func sessionOwnerFromContext(ctx context.Context) string {
	if owner, ok := ctx.Value(accountOwnerContextKey{}).(string); ok && owner != "" {
		return owner
	}
	return deviceOwnerFromContext(ctx)
}

func (h *handler) secureAccountSessionRequest(w http.ResponseWriter, r *http.Request, requestID string) (*http.Request, bool) {
	if h.accounts == nil {
		writeError(w, http.StatusUnauthorized, "account_required", "请先登录账户。", requestID)
		return r, false
	}
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "account_required", "请先登录账户。", requestID)
		return r, false
	}
	account, err := h.accounts.Authenticate(r.Context(), cookie.Value)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "account_required", "登录已失效，请重新登录。", requestID)
		return r, false
	}
	if isStateChanging(r.Method) && !hasSameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross_site_request", "已拒绝来自其他站点的会话操作。", requestID)
		return r, false
	}
	ctx := context.WithValue(r.Context(), accountOwnerContextKey{}, auth.OwnerHash(account.ID))
	ctx = context.WithValue(ctx, accountContextKey{}, account)
	return r.WithContext(ctx), true
}

type accountContextKey struct{}

func ownsSession(item domain.Session, owner string) bool {
	if item.OwnerHash == "" || owner == "" || len(item.OwnerHash) != len(owner) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(item.OwnerHash), []byte(owner)) == 1
}

func (h *handler) getOwnedSession(r *http.Request, id string) (domain.Session, error) {
	item, err := h.store.Get(id, h.now())
	if err != nil {
		return domain.Session{}, err
	}
	if !h.requireDeviceAuth && !h.requireAccountAuth {
		return item, nil
	}
	if !ownsSession(item, sessionOwnerFromContext(r.Context())) {
		return domain.Session{}, session.ErrNotFound
	}
	return item, nil
}
