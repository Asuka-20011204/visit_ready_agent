package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"visitready/internal/config"
	"visitready/internal/domain"
	"visitready/internal/session"
)

func TestBuildHandlerDemoServesHealthAndLabelsMode(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler, _, err := buildHandler(config.Config{
		Mode:            config.ModeDemo,
		SessionTTL:      30 * time.Minute,
		UpstreamTimeout: 5 * time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("buildHandler() error = %v", err)
	}

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d", health.Code)
	}

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", page.Code)
	}
	if !strings.Contains(page.Body.String(), "离线演示") {
		t.Fatal("demo page does not visibly label demo mode")
	}
	if !strings.Contains(logs.String(), `"ai_called":false`) {
		t.Fatalf("demo startup log does not state AI was not called: %s", logs.String())
	}
}

func TestNewHTTPServerAppliesAllTimeouts(t *testing.T) {
	cfg := config.Config{
		Addr:              "127.0.0.1:0",
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       3 * time.Second,
		WriteTimeout:      4 * time.Second,
		IdleTimeout:       5 * time.Second,
	}
	server := newHTTPServer(cfg, http.NotFoundHandler())

	if server.Addr != cfg.Addr || server.ReadHeaderTimeout != cfg.ReadHeaderTimeout ||
		server.ReadTimeout != cfg.ReadTimeout || server.WriteTimeout != cfg.WriteTimeout ||
		server.IdleTimeout != cfg.IdleTimeout {
		t.Fatalf("server timeouts do not match config: %#v", server)
	}
}

func TestCleanupLoopDeletesExpiredSessions(t *testing.T) {
	store := session.NewMemoryStore()
	if err := store.Create(domain.Session{ID: "expired", ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs bytes.Buffer
	done := startCleanupLoop(ctx, store, 5*time.Millisecond, slog.New(slog.NewJSONHandler(&logs, nil)))
	time.Sleep(25 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup loop did not stop after cancellation")
	}
	if _, err := store.Get("expired", time.Now()); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound after cleanup", err)
	}
}

func TestStartupLogContainsNoConfiguredSecrets(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	cfg := config.Config{
		Mode:            config.ModeDemo,
		LLMAPIKey:       "do-not-log-llm-key",
		TavilyAPIKey:    "do-not-log-search-key",
		TavilyEndpoint:  "https://api.tavily.com/search",
		SessionTTL:      30 * time.Minute,
		UpstreamTimeout: time.Second,
	}
	if _, _, err := buildHandler(cfg, logger); err != nil {
		t.Fatalf("buildHandler() error = %v", err)
	}
	if strings.Contains(output.String(), cfg.LLMAPIKey) || strings.Contains(output.String(), cfg.TavilyAPIKey) {
		t.Fatalf("startup logs exposed secrets: %s", output.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var value map[string]any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatalf("log is not structured JSON: %q: %v", line, err)
		}
	}
}

func TestBuildHandlerLiveUsesConfiguredAIClient(t *testing.T) {
	cfg := validConfig()
	cfg.Mode = config.ModeLive
	cfg.LLMEndpoint = "http://127.0.0.1:9999/v1/chat/completions"
	cfg.LLMAPIKey = "test-only-key"
	cfg.LLMModel = "test-model"

	handler, _, err := buildHandler(cfg, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("buildHandler() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d", response.Code)
	}
}

func TestBuildHandlerRejectsInvalidProviderEndpoints(t *testing.T) {
	tests := []struct {
		name   string
		change func(*config.Config)
	}{
		{
			name: "LLM",
			change: func(cfg *config.Config) {
				cfg.Mode = config.ModeLive
				cfg.LLMEndpoint = "http://public.example/v1/chat/completions"
				cfg.LLMAPIKey = "test-only-key"
				cfg.LLMModel = "test-model"
			},
		},
		{
			name: "Tavily",
			change: func(cfg *config.Config) {
				cfg.Mode = config.ModeLive
				cfg.LLMEndpoint = "http://127.0.0.1:9999/v1/chat/completions"
				cfg.LLMAPIKey = "test-only-key"
				cfg.LLMModel = "test-model"
				cfg.TavilyEndpoint = "http://public.example/search"
				cfg.TavilyAPIKey = "test-only-key"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.change(&cfg)
			if _, _, err := buildHandler(cfg, slog.Default()); err == nil {
				t.Fatal("buildHandler() error = nil, want insecure endpoint rejection")
			}
		})
	}
}

func TestDemoModeNeverBuildsNetworkSearchClient(t *testing.T) {
	cfg := validConfig()
	cfg.TavilyEndpoint = "http://public.example/search"
	cfg.TavilyAPIKey = "configured-but-must-not-be-used"
	client, err := buildSearchClient(cfg, http.DefaultClient)
	if err != nil || client != nil {
		t.Fatalf("buildSearchClient() = %#v, %v", client, err)
	}
}

func TestBuildHandlerRequiresLogger(t *testing.T) {
	if _, _, err := buildHandler(validConfig(), nil); err == nil {
		t.Fatal("buildHandler() error = nil, want logger validation error")
	}
}

func TestRunStopsGracefullyWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, validConfig(), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	}()
	time.Sleep(25 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("run() did not stop after cancellation")
	}
}

func TestRunReturnsListenError(t *testing.T) {
	cfg := validConfig()
	cfg.Addr = "127.0.0.1:invalid"
	err := run(context.Background(), cfg, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err == nil {
		t.Fatal("run() error = nil, want listen failure")
	}
}

func TestExecuteRejectsInvalidEnvironment(t *testing.T) {
	err := execute(context.Background(), func(key string) (string, bool) {
		if key == "APP_MODE" {
			return "live", true
		}
		return "", false
	}, slog.Default())
	if err == nil || !strings.Contains(err.Error(), "load configuration") {
		t.Fatalf("execute() error = %v, want configuration error", err)
	}
}

func validConfig() config.Config {
	return config.Config{
		Mode:              config.ModeDemo,
		Addr:              "127.0.0.1:0",
		TavilyEndpoint:    "https://api.tavily.com/search",
		SessionTTL:        30 * time.Minute,
		CleanupInterval:   10 * time.Millisecond,
		UpstreamTimeout:   time.Second,
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       time.Second,
		WriteTimeout:      time.Second,
		IdleTimeout:       time.Second,
		ShutdownTimeout:   time.Second,
	}
}
