package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"visitready/internal/auth"
	"visitready/internal/httpapi"
	"visitready/internal/session"
)

func TestAccountAuthenticationOwnsPublicSessions(t *testing.T) {
	api, err := httpapi.New(httpapi.Config{
		Runner:             &fakeRunner{},
		Store:              session.NewMemoryStore(),
		Accounts:           auth.NewService(auth.NewMemoryStore()),
		Now:                time.Now,
		RequireAccountAuth: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	firstCookie := registerAccount(t, api, "first@example.com")
	created := cookieRequest(api, firstCookie, http.MethodPost, "/api/v1/sessions", `{"input":"这是一段长度足够且已经脱敏的健康情况描述。"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}

	secondCookie := registerAccount(t, api, "second@example.com")
	other := cookieRequest(api, secondCookie, http.MethodGet, "/api/v1/sessions/session-1", "")
	if other.Code != http.StatusNotFound {
		t.Fatalf("cross-account read status=%d body=%s", other.Code, other.Body.String())
	}
	owned := cookieRequest(api, firstCookie, http.MethodGet, "/api/v1/sessions/session-1", "")
	if owned.Code != http.StatusOK {
		t.Fatalf("owned read status=%d body=%s", owned.Code, owned.Body.String())
	}

	loggedOut := cookieRequest(api, firstCookie, http.MethodPost, "/api/v1/auth/logout", "")
	if loggedOut.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d body=%s", loggedOut.Code, loggedOut.Body.String())
	}
	denied := cookieRequest(api, firstCookie, http.MethodGet, "/api/v1/sessions", "")
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("logout did not revoke session: status=%d body=%s", denied.Code, denied.Body.String())
	}
}

func TestAccountEndpointsRejectInvalidCredentials(t *testing.T) {
	api, err := httpapi.New(httpapi.Config{
		Runner:             &fakeRunner{},
		Store:              session.NewMemoryStore(),
		Accounts:           auth.NewService(auth.NewMemoryStore()),
		RequireAccountAuth: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	register := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"email":"bad","password":"too short"}`))
	register.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, register)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid registration status=%d body=%s", response.Code, response.Body.String())
	}
	assertErrorCode(t, response.Body.Bytes(), "invalid_registration")
}

func TestAccountCookieHasBrowserProtectionFlags(t *testing.T) {
	api, err := httpapi.New(httpapi.Config{
		Runner:             &fakeRunner{},
		Store:              session.NewMemoryStore(),
		Accounts:           auth.NewService(auth.NewMemoryStore()),
		RequireAccountAuth: true,
		SecureCookies:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"email":"cookie@example.com","password":"correct horse battery staple"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie=%#v", cookies)
	}
}

func TestAccountLoginIsRateLimited(t *testing.T) {
	accounts := auth.NewService(auth.NewMemoryStore())
	if _, err := accounts.Register(t.Context(), "limited@example.com", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	api, err := httpapi.New(httpapi.Config{
		Runner:             &fakeRunner{},
		Store:              session.NewMemoryStore(),
		Accounts:           accounts,
		RequireAccountAuth: true,
		RequestsPerMinute:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"limited@example.com","password":"correct horse battery staple"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if attempt == 0 && response.Code != http.StatusOK {
			t.Fatalf("first login status=%d", response.Code)
		}
		if attempt == 1 && response.Code != http.StatusTooManyRequests {
			t.Fatalf("second login status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func registerAccount(t *testing.T, api http.Handler, email string) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"email":"`+email+`","password":"correct horse battery staple"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies=%#v", cookies)
	}
	return cookies[0]
}

func cookieRequest(api http.Handler, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(cookie)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	return response
}
