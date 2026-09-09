// Package httplog provides thin HTTP instrumentation helpers used by the
// server: per-request access logs and per-upstream-call transport logs.
// Log lines never include query strings, authorization headers, or bodies.
package httplog

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// AccessLog wraps next and logs one structured line per handled request.
// Static asset requests are omitted so the log stays focused on API traffic.
func AccessLog(next http.Handler, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if isSkippablePath(r.URL.Path) {
			return
		}
		logger.Info("http_request",
			"method", r.Method,
			"path", accessLogPath(r.URL.Path),
			"status", rec.statusCode(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", rec.Header().Get("X-Request-ID"),
		)
	})
}

func accessLogPath(path string) string {
	const sessionsPath = "/api/v1/sessions"
	if path == sessionsPath || !strings.HasPrefix(path, sessionsPath+"/") {
		return path
	}
	remainder := strings.TrimPrefix(path, sessionsPath+"/")
	parts := strings.Split(remainder, "/")
	if len(parts) == 0 || parts[0] == "" {
		return sessionsPath
	}
	if len(parts) == 1 {
		return sessionsPath + "/:id"
	}
	switch parts[1] {
	case "clarifications", "confirm", "export":
		return sessionsPath + "/:id/" + parts[1]
	default:
		return sessionsPath + "/:id/:action"
	}
}

// Transport wraps base (falling back to http.DefaultTransport) and logs every
// upstream call with its duration. Only host and path are logged; query
// strings are intentionally omitted because they may carry secrets.
func Transport(base http.RoundTripper, logger *slog.Logger) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if logger == nil {
		logger = slog.Default()
	}
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		start := time.Now()
		resp, err := base.RoundTrip(req)
		attrs := []any{
			"method", req.Method,
			"host", req.URL.Host,
			"path", req.URL.Path,
		}
		switch {
		case err != nil:
			logger.Warn("upstream_call_failed", append(attrs, "duration_ms", time.Since(start).Milliseconds(), "error", err)...)
		case resp.StatusCode >= 400:
			logger.Warn("upstream_call_http_error", append(attrs, "duration_ms", time.Since(start).Milliseconds(), "status", resp.StatusCode)...)
		default:
			logger.Debug("upstream_response_headers", append(attrs, "duration_ms", time.Since(start).Milliseconds(), "status", resp.StatusCode)...)
			if resp.Body != nil {
				resp.Body = &loggedBody{ReadCloser: resp.Body, started: start, attrs: attrs, logger: logger}
			}
		}
		return resp, err
	})
}

type loggedBody struct {
	io.ReadCloser
	started time.Time
	attrs   []any
	logger  *slog.Logger
	bytes   int
	logged  bool
	mu      sync.Mutex
}

func (b *loggedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bytes += n
	if err != nil && !b.logged {
		b.logged = true
		attrs := append(append([]any(nil), b.attrs...), "duration_ms", time.Since(b.started).Milliseconds(), "bytes", b.bytes)
		if err == io.EOF {
			b.logger.Debug("upstream_response_read", append(attrs, "status", "complete")...)
		} else {
			b.logger.Warn("upstream_response_read_failed", append(attrs, "error", err)...)
		}
	}
	return n, err
}

func (b *loggedBody) Close() error {
	err := b.ReadCloser.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.logged {
		b.logged = true
		attrs := append(append([]any(nil), b.attrs...), "duration_ms", time.Since(b.started).Milliseconds(), "bytes", b.bytes)
		if err != nil {
			b.logger.Warn("upstream_response_close_failed", append(attrs, "error", err)...)
		} else {
			b.logger.Debug("upstream_response_read", append(attrs, "status", "closed_before_eof")...)
		}
	}
	return err
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// statusRecorder captures the response status while still behaving as the
// original ResponseWriter.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) Flush() {
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func isSkippablePath(path string) bool {
	// Keep the main document, health checks, and API traffic visible.
	if path == "/" || path == "/healthz" || path == "/livez" || path == "/readyz" || strings.HasPrefix(path, "/api") {
		return false
	}
	if strings.HasPrefix(path, "/assets/") || strings.HasPrefix(path, "/favicon") || path == "/robots.txt" {
		return true
	}
	// Root-served static files carry a file extension in the last segment
	// (for example /app.js or /shield-check.svg).
	if slash := strings.LastIndex(path, "/"); slash >= 0 {
		return strings.Contains(path[slash+1:], ".")
	}
	return false
}
