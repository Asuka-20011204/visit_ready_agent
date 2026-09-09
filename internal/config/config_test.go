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
	if cfg.Addr != "127.0.0.1:8080" {
		t.Fatalf("Addr = %q, want loopback default", cfg.Addr)
	}
	if cfg.SessionStore != SessionStoreMemory || cfg.MySQLDSN != "" {
		t.Fatalf("default session store = %q, dsn present = %t", cfg.SessionStore, cfg.MySQLDSN != "")
	}
	if cfg.SessionTTL != 24*time.Hour {
		t.Fatalf("SessionTTL = %v, want 24h", cfg.SessionTTL)
	}
	for name, duration := range map[string]time.Duration{
		"ReadHeaderTimeout": cfg.ReadHeaderTimeout,
		"ReadTimeout":       cfg.ReadTimeout,
		"WriteTimeout":      cfg.WriteTimeout,
		"IdleTimeout":       cfg.IdleTimeout,
		"ShutdownTimeout":   cfg.ShutdownTimeout,
		"UpstreamTimeout":   cfg.UpstreamTimeout,
		"WorkflowTimeout":   cfg.WorkflowTimeout,
		"CleanupInterval":   cfg.CleanupInterval,
	} {
		if duration <= 0 {
			t.Errorf("%s = %v, want positive", name, duration)
		}
	}
	if cfg.BochaEndpoint != "https://api.bochaai.com/v1/web-search" {
		t.Fatalf("BochaEndpoint = %q", cfg.BochaEndpoint)
	}
}

func TestLoadRequiresDSNForMySQLSessionStore(t *testing.T) {
	_, err := Load(envLookup(map[string]string{"SESSION_STORE": "mysql"}))
	if err == nil || !strings.Contains(err.Error(), "MYSQL_DSN") {
		t.Fatalf("Load() error = %v, want missing MYSQL_DSN", err)
	}

	cfg, err := Load(envLookup(map[string]string{
		"SESSION_STORE":          "mysql",
		"MYSQL_DSN":              "visitready:secret@tcp(127.0.0.1:3306)/visitready?parseTime=true",
		"SESSION_ENCRYPTION_KEY": "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI",
	}))
	if err != nil {
		t.Fatalf("Load() MySQL error = %v", err)
	}
	if cfg.SessionStore != SessionStoreMySQL || cfg.MySQLDSN == "" {
		t.Fatalf("MySQL config = %#v", cfg)
	}
	if strings.Contains(cfg.String(), "secret") {
		t.Fatalf("Config.String() exposed MySQL credentials: %q", cfg.String())
	}
}

func TestLoadRequiresValidEncryptionKeyForMySQL(t *testing.T) {
	base := map[string]string{
		"SESSION_STORE": "mysql",
		"MYSQL_DSN":     "visitready:secret@tcp(127.0.0.1:3306)/visitready?parseTime=true",
	}
	for _, key := range []string{"", "too-short", "bm90LXRoaXJ0eS10d28tYnl0ZXM"} {
		base["SESSION_ENCRYPTION_KEY"] = key
		if _, err := Load(envLookup(base)); err == nil || !strings.Contains(err.Error(), "SESSION_ENCRYPTION_KEY") {
			t.Fatalf("Load() with key %q error = %v, want encryption key validation", key, err)
		}
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
		"APP_MODE":               "live",
		"LLM_ENDPOINT":           "https://example.com/v1/chat/completions",
		"LLM_API_KEY":            "test-secret",
		"LLM_MODEL":              "test-model",
		"SESSION_STORE":          "mysql",
		"MYSQL_DSN":              "visitready:secret@tcp(127.0.0.1:3306)/visitready?parseTime=true",
		"SESSION_ENCRYPTION_KEY": "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI",
		"BOCHA_API_KEY":          "search-secret",
		"SESSION_TTL":            "45m",
		"WORKFLOW_TIMEOUT":       "45s",
		"LOG_LEVEL":              "debug",
		"APP_ADDR":               "127.0.0.1:9090",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Mode != ModeLive || cfg.SessionTTL != 45*time.Minute {
		t.Fatalf("unexpected config: mode=%q ttl=%v", cfg.Mode, cfg.SessionTTL)
	}
	if cfg.WorkflowTimeout != 45*time.Second || cfg.LogLevel != "debug" {
		t.Fatalf("unexpected config: workflow=%v log_level=%q", cfg.WorkflowTimeout, cfg.LogLevel)
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
		{name: "invalid log level", env: map[string]string{"LOG_LEVEL": "verbose"}},
		{name: "invalid session store", env: map[string]string{"SESSION_STORE": "postgres"}},
		{name: "invalid secure cookie", env: map[string]string{"AUTH_COOKIE_SECURE": "sometimes"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Load(envLookup(tt.env)); err == nil {
				t.Fatal("Load() error = nil, want validation error")
			}
		})
	}
}

func TestLoadRejectsHTTPWriteTimeoutShorterThanWorkflow(t *testing.T) {
	_, err := Load(envLookup(map[string]string{
		"WORKFLOW_TIMEOUT":   "90s",
		"HTTP_WRITE_TIMEOUT": "60s",
	}))
	if err == nil || !strings.Contains(err.Error(), "HTTP_WRITE_TIMEOUT") {
		t.Fatalf("Load() error = %v, want write timeout validation", err)
	}
}

func TestLoadRejectsWorkflowTimeoutAboveBrowserBudget(t *testing.T) {
	_, err := Load(envLookup(map[string]string{
		"WORKFLOW_TIMEOUT":      "46s",
		"HTTP_WRITE_TIMEOUT":    "60s",
		"HTTP_SHUTDOWN_TIMEOUT": "60s",
	}))
	if err == nil || !strings.Contains(err.Error(), "must not exceed 45s") {
		t.Fatalf("Load() error = %v, want workflow timeout upper bound", err)
	}
}

func TestConfigStringDoesNotExposeSecrets(t *testing.T) {
	cfg := Config{Mode: ModeLive, Addr: ":8080", LLMAPIKey: "llm-secret", BochaAPIKey: "search-secret"}
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
