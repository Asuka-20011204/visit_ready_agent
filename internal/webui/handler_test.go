package webui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"visitready/internal/webui"
)

func TestHandlerRendersDemoDisclosureAndSecurityHeaders(t *testing.T) {
	handler := newHandler(t, "demo")
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "离线演示 · 未调用 AI") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if csp := response.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "object-src 'none'") {
		t.Fatalf("CSP = %q", csp)
	}
}

func TestHandlerServesEmbeddedAssetsAndDelegatesAPI(t *testing.T) {
	handler := newHandler(t, "live")

	assetRequest := httptest.NewRequest(http.MethodGet, "/assets/styles.css", nil)
	assetResponse := httptest.NewRecorder()
	handler.ServeHTTP(assetResponse, assetRequest)
	if assetResponse.Code != http.StatusOK || !strings.Contains(assetResponse.Body.String(), "--green") {
		t.Fatalf("asset status = %d", assetResponse.Code)
	}

	apiRequest := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	apiResponse := httptest.NewRecorder()
	handler.ServeHTTP(apiResponse, apiRequest)
	if apiResponse.Body.String() != "api" {
		t.Fatalf("API response = %q", apiResponse.Body.String())
	}
}

func TestAppIgnoresResponsesFromBeforeWorkspaceReset(t *testing.T) {
	handler := newHandler(t, "live")
	request := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	script := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("asset status = %d", response.Code)
	}
	if !strings.Contains(script, "let workspaceGeneration = 0;") ||
		!strings.Contains(script, "workspaceGeneration += 1;") {
		t.Fatal("app.js does not invalidate pre-reset requests with a workspace generation")
	}
	if count := strings.Count(script, "generation !== workspaceGeneration"); count < 3 {
		t.Fatalf("app.js has %d stale-response guards, want at least 3", count)
	}
}

func TestHandlerRejectsInvalidConstructionAndRoutes(t *testing.T) {
	if _, err := webui.New(nil, "demo"); err == nil {
		t.Fatal("New(nil) error = nil")
	}
	handler := newHandler(t, "live")
	for _, test := range []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodPost, path: "/", want: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/missing", want: http.StatusNotFound},
		{method: http.MethodHead, path: "/", want: http.StatusOK},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("%s %s status = %d", test.method, test.path, response.Code)
		}
	}
}

func newHandler(t *testing.T, mode string) http.Handler {
	t.Helper()
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("api"))
	})
	handler, err := webui.New(api, mode)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}
