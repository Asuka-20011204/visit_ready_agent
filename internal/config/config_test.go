package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsToDemoWithSafeTimeouts(t *testing.T) {
	cfg, err := Load(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Mode != ModeDemo {
		t.Fatalf("Mode = %q, want %q", cfg.Mode, ModeDemo)
	}
	if cfg.Addr != ":8080" {
		t.Fatalf("Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.SessionTTL != 30*time.Minute {
		t.Fatalf("SessionTTL = %v, want 30m", cfg.SessionTTL)
	}
	for name, duration := range map[string]time.Duration{
		"ReadHeaderTimeout": cfg.ReadHeaderTimeout,
		"ReadTimeout":       cfg.ReadTimeout,
		"WriteTimeout":      cfg.WriteTimeout,
		"IdleTimeout":       cfg.IdleTimeout,
		"ShutdownTimeout":   cfg.ShutdownTimeout,
		"UpstreamTimeout":   cfg.UpstreamTimeout,
		"CleanupInterval":   cfg.CleanupInterval,
	} {
		if duration <= 0 {
			t.Errorf("%s = %v, want positive", name, duration)
		}
	}
	if cfg.TavilyEndpoint != "https://api.tavily.com/search" {
		t.Fatalf("TavilyEndpoint = %q", cfg.TavilyEndpoint)
	}
}

func TestLoadLiveRequiresLLMConfiguration(t *testing.T) {
	lookup := envLookup(map[string]string{"APP_MODE": "live"})

	_, err := Load(lookup)
	if err == nil {
		t.Fatal("Load() error = nil, want missing LLM fields")
	}
	for _, field := range []string{"LLM_ENDPOINT", "LLM_API_KEY", "LLM_MODEL"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error %q does not mention %s", err, field)
		}
	}
}

func TestLoadLiveAcceptsCompleteConfiguration(t *testing.T) {
	cfg, err := Load(envLookup(map[string]string{
		"APP_MODE":       "live",
		"LLM_ENDPOINT":   "https://example.com/v1/chat/completions",
		"LLM_API_KEY":    "test-secret",
		"LLM_MODEL":      "test-model",
		"TAVILY_API_KEY": "search-secret",
		"SESSION_TTL":    "45m",
		"APP_ADDR":       "127.0.0.1:9090",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Mode != ModeLive || cfg.SessionTTL != 45*time.Minute {
		t.Fatalf("unexpected config: mode=%q ttl=%v", cfg.Mode, cfg.SessionTTL)
	}
	if !cfg.SearchEnabled() {
		t.Fatal("SearchEnabled() = false, want true")
	}
}

func TestLoadRejectsInvalidModeAddressAndDuration(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "mode", env: map[string]string{"APP_MODE": "staging"}},
		{name: "address", env: map[string]string{"APP_ADDR": "not-an-address"}},
		{name: "zero duration", env: map[string]string{"SESSION_TTL": "0s"}},
		{name: "invalid duration", env: map[string]string{"UPSTREAM_TIMEOUT": "eventually"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Load(envLookup(tt.env)); err == nil {
				t.Fatal("Load() error = nil, want validation error")
			}
		})
	}
}

func TestConfigStringDoesNotExposeSecrets(t *testing.T) {
	cfg := Config{Mode: ModeLive, Addr: ":8080", LLMAPIKey: "llm-secret", TavilyAPIKey: "search-secret"}
	got := cfg.String()
	if strings.Contains(got, "llm-secret") || strings.Contains(got, "search-secret") {
		t.Fatalf("String() exposed a secret: %q", got)
	}
}

func envLookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
