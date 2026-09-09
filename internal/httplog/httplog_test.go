package httplog

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessLogRecordsRequestAndCorrelatesRequestID(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "req-123")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "unavailable")
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
	AccessLog(inner, logger).ServeHTTP(httptest.NewRecorder(), request)

	line := strings.TrimSpace(logs.String())
	if !strings.Contains(line, `"method":"POST"`) ||
		!strings.Contains(line, `"path":"/api/v1/sessions"`) ||
		!strings.Contains(line, `"status":503`) ||
		!strings.Contains(line, `"request_id":"req-123"`) {
		t.Fatalf("access log line is incomplete: %s", line)
	}
}

func TestAccessLogOmitsStaticAssets(t *testing.T) {
	for _, path := range []string{"/assets/app.js", "/app.js", "/sparkles.svg", "/styles.css", "/sw.js"} {
		t.Run(path, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			request := httptest.NewRequest(http.MethodGet, path, nil)
			AccessLog(inner, logger).ServeHTTP(httptest.NewRecorder(), request)

			if logs.Len() != 0 {
				t.Fatalf("static asset request %s was logged: %s", path, logs.String())
			}
		})
	}
}

func TestAccessLogKeepsMainDocumentAndAPI(t *testing.T) {
	for _, path := range []string{"/", "/healthz", "/api/v1/sessions"} {
		t.Run(path, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			request := httptest.NewRequest(http.MethodGet, path, nil)
			AccessLog(inner, logger).ServeHTTP(httptest.NewRecorder(), request)

			if !strings.Contains(logs.String(), `"path":"`+path+`"`) {
				t.Fatalf("expected %s to be logged, got: %s", path, logs.String())
			}
		})
	}
}

func TestAccessLogRedactsSessionCapabilityFromPath(t *testing.T) {
	const sessionID = "0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		path string
		want string
	}{
		{path: "/api/v1/sessions/" + sessionID, want: "/api/v1/sessions/:id"},
		{path: "/api/v1/sessions/" + sessionID + "/export", want: "/api/v1/sessions/:id/export"},
		{path: "/api/v1/sessions/dummy/" + sessionID, want: "/api/v1/sessions/:id/:action"},
	} {
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		AccessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}), logger).ServeHTTP(httptest.NewRecorder(), request)

		line := logs.String()
		if !strings.Contains(line, `"path":"`+test.want+`"`) || strings.Contains(line, sessionID) {
			t.Fatalf("session path was not redacted: %s", line)
		}
	}
}

func TestTransportLogsBodyClosedBeforeEOF(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 1024))
	}))
	defer upstream.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := &http.Client{Transport: Transport(nil, logger)}
	resp, err := client.Get(upstream.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `"status":"closed_before_eof"`) {
		t.Fatalf("early close was not logged: %s", logs.String())
	}
}

func TestAccessLogPreservesFlusherCapability(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("wrapped ResponseWriter does not implement http.Flusher")
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush() error = %v", err)
		}
	})
	AccessLog(inner, slog.New(slog.NewTextHandler(io.Discard, nil))).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
}

func TestTransportLogsSuccessAndNeverLogsQuerySecrets(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := &http.Client{Transport: Transport(nil, logger)}
	resp, err := client.Get(upstream.URL + "/v1/chat/completions?secret=should-not-appear")
	if err != nil {
		t.Fatalf("GET() error = %v", err)
	}
	defer resp.Body.Close()

	line := strings.TrimSpace(logs.String())
	if !strings.Contains(line, `"path":"/v1/chat/completions"`) {
		t.Fatalf("transport log is missing path: %s", line)
	}
	if strings.Contains(line, "secret") || strings.Contains(line, "should-not-appear") {
		t.Fatalf("transport log leaked query content: %s", line)
	}
}

func TestTransportLogsResponseBodyCompletionSeparately(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := &http.Client{Transport: Transport(nil, logger)}
	resp, err := client.Get(upstream.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatalf("GET() error = %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	line := logs.String()
	if !strings.Contains(line, "upstream_response_headers") || !strings.Contains(line, "upstream_response_read") || !strings.Contains(line, `"status":"complete"`) {
		t.Fatalf("response phases were not logged separately: %s", line)
	}
}

func TestTransportLogsUpstreamHTTPErrorsAsWarnings(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	client := &http.Client{Transport: Transport(nil, logger)}
	resp, err := client.Get(upstream.URL + "/search")
	if err != nil {
		t.Fatalf("GET() error = %v", err)
	}
	defer resp.Body.Close()

	line := strings.TrimSpace(logs.String())
	if !strings.Contains(line, `"level":"WARN"`) || !strings.Contains(line, `"status":502`) {
		t.Fatalf("expected a WARN line with status 502, got: %s", line)
	}
}

func TestTransportLogsNetworkFailuresAsWarnings(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	client := &http.Client{Transport: Transport(nil, logger)}
	_, err := client.Get("http://127.0.0.1:1/unreachable")
	if err == nil {
		t.Fatal("GET() error = nil, want connection failure")
	}

	line := strings.TrimSpace(logs.String())
	if !strings.Contains(line, `"level":"WARN"`) || !strings.Contains(line, "upstream_call_failed") {
		t.Fatalf("expected a WARN line for the failed call, got: %s", line)
	}
}
