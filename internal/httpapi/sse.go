package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"visitready/internal/agent"
	"visitready/internal/domain"
)

type bufferedResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: make(http.Header)}
}

func (w *bufferedResponse) Header() http.Header { return w.header }

func (w *bufferedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *bufferedResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

func wantsProgressStream(r *http.Request) bool {
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") || r.Method != http.MethodPost {
		return false
	}
	return r.URL.Path == "/api/v1/sessions" || strings.HasSuffix(r.URL.Path, "/clarifications") || strings.HasSuffix(r.URL.Path, "/retry")
}

func (h *handler) serveProgressStream(w http.ResponseWriter, r *http.Request, requestID string) {
	// HTTP/1.x cannot reliably read a request body after response headers have
	// been flushed. Buffer only the already bounded API payload before opening
	// the event stream, then let the normal strict decoder enforce the limit.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "请求内容读取失败，请重试。", requestID)
		return
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	var writeMu sync.Mutex
	writeEvent := func(name string, data any) {
		encoded, err := json.Marshal(data)
		if err != nil {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_, _ = w.Write([]byte("event: " + name + "\ndata: "))
		_, _ = w.Write(encoded)
		_, _ = w.Write([]byte("\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	writeEvent("connected", map[string]string{"request_id": requestID})
	ctx := agent.WithProgress(r.Context(), func(progress domain.AgentProgress) { writeEvent("progress", progress) })
	buffered := newBufferedResponse()
	h.route(buffered, r.WithContext(ctx), requestID)
	status := buffered.status
	if status == 0 {
		status = http.StatusOK
	}
	payload := json.RawMessage(buffered.body.Bytes())
	if !json.Valid(payload) {
		payload = json.RawMessage(`null`)
	}
	writeEvent("result", struct {
		Status  int             `json:"status"`
		Payload json.RawMessage `json:"payload"`
	}{Status: status, Payload: payload})
}
