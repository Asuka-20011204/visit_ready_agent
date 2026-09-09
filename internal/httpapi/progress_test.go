package httpapi_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"visitready/internal/agent"
	"visitready/internal/domain"
	"visitready/internal/httpapi"
	"visitready/internal/session"
)

type progressRunner struct{ fakeRunner }

func (r *progressRunner) Start(ctx context.Context, input string, search bool) (domain.Session, error) {
	agent.ReportProgress(ctx, domain.AgentProgress{Node: "extract_facts", Status: "running", Message: "正在提取事实"})
	item, err := r.fakeRunner.Start(ctx, input, search)
	agent.ReportProgress(ctx, domain.AgentProgress{Node: "extract_facts", Status: "completed", Message: "事实提取完成", DurationMS: 12})
	return item, err
}

func TestSessionCreateStreamsProgressAndFinalResult(t *testing.T) {
	handler, err := httpapi.New(httpapi.Config{Runner: &progressRunner{}, Store: session.NewMemoryStore(), Now: time.Now, RequireDeviceAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewBufferString(`{"input":"这是一段长度足够且已经脱敏的健康情况描述。"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(httpapi.DeviceKeyHeader, authTestDeviceKey)
	request.Header.Set("Accept", "text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream response = %d %#v", response.Code, response.Header())
	}
	body := response.Body.String()
	for _, marker := range []string{"event: connected", "event: progress", `"node":"extract_facts"`, `"duration_ms":12`, "event: result", `"status":201`} {
		if !strings.Contains(body, marker) {
			t.Errorf("SSE body missing %q: %s", marker, body)
		}
	}
}

func TestSessionCreateStreamReadsBodyBeforeFlushingHeaders(t *testing.T) {
	handler, err := httpapi.New(httpapi.Config{Runner: &progressRunner{}, Store: session.NewMemoryStore(), Now: time.Now, RequireDeviceAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/sessions", bytes.NewBufferString(`{"input":"这是一段长度足够且已经脱敏的健康情况描述。"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(httpapi.DeviceKeyHeader, authTestDeviceKey)
	request.Header.Set("Accept", "text/event-stream")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"status":201`)) {
		t.Fatalf("network SSE did not process request body: %s", body)
	}
}
