package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	ModeDemo           = "demo"
	ModeLive           = "live"
	SessionStoreMemory = "memory"
	SessionStoreMySQL  = "mysql"
	AuthModeRequired   = "required"
	AuthModeDisabled   = "disabled"
)

const defaultBochaEndpoint = "https://api.bochaai.com/v1/web-search"
const maxWorkflowTimeout = 45 * time.Second

type LookupEnv func(string) (string, bool)

type Config struct {
	Mode                 string
	Addr                 string
	LLMEndpoint          string
	LLMAPIKey            string
	LLMModel             string
	BochaEndpoint        string
	BochaAPIKey          string
	SessionStore         string
	MySQLDSN             string
	SessionEncryptionKey []byte
	AuthMode             string
	CookieSecure         bool
	LogLevel             string
	SessionTTL           time.Duration
	CleanupInterval      time.Duration
	UpstreamTimeout      time.Duration
	WorkflowTimeout      time.Duration
	ReadHeaderTimeout    time.Duration
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	IdleTimeout          time.Duration
	ShutdownTimeout      time.Duration
}

var validLogLevels = map[string]bool{
	"debug": true, "info": true, "warn": true, "error": true,
}

func Load(lookup LookupEnv) (Config, error) {
	if lookup == nil {
		return Config{}, errors.New("environment lookup is required")
	}

	cfg := Config{
		Mode:              valueOrDefault(lookup, "APP_MODE", ModeDemo),
		Addr:              valueOrDefault(lookup, "APP_ADDR", "127.0.0.1:8080"),
		LLMEndpoint:       value(lookup, "LLM_ENDPOINT"),
		LLMAPIKey:         value(lookup, "LLM_API_KEY"),
		LLMModel:          value(lookup, "LLM_MODEL"),
		BochaEndpoint:     valueOrDefault(lookup, "BOCHA_ENDPOINT", defaultBochaEndpoint),
		BochaAPIKey:       value(lookup, "BOCHA_API_KEY"),
		SessionStore:      valueOrDefault(lookup, "SESSION_STORE", SessionStoreMemory),
		MySQLDSN:          value(lookup, "MYSQL_DSN"),
		LogLevel:          valueOrDefault(lookup, "LOG_LEVEL", "info"),
		SessionTTL:        24 * time.Hour,
		CleanupInterval:   time.Minute,
		UpstreamTimeout:   25 * time.Second,
		WorkflowTimeout:   45 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
		ShutdownTimeout:   2 * time.Minute,
	}
	if cfg.Mode == ModeLive {
		cfg.AuthMode = valueOrDefault(lookup, "AUTH_MODE", AuthModeRequired)
		cookieSecure, err := loadBool(lookup, "AUTH_COOKIE_SECURE", true)
		if err != nil {
			return Config{}, err
		}
		cfg.CookieSecure = cookieSecure
	} else {
		cfg.AuthMode = valueOrDefault(lookup, "AUTH_MODE", AuthModeDisabled)
		cookieSecure, err := loadBool(lookup, "AUTH_COOKIE_SECURE", false)
		if err != nil {
			return Config{}, err
		}
		cfg.CookieSecure = cookieSecure
	}
	if rawKey := value(lookup, "SESSION_ENCRYPTION_KEY"); rawKey != "" {
		key, err := decodeEncryptionKey(rawKey)
		if err != nil {
			return Config{}, err
		}
		cfg.SessionEncryptionKey = key
	}

	durations := []struct {
		name   string
		target *time.Duration
	}{
		{name: "SESSION_TTL", target: &cfg.SessionTTL},
		{name: "SESSION_CLEANUP_INTERVAL", target: &cfg.CleanupInterval},
		{name: "UPSTREAM_TIMEOUT", target: &cfg.UpstreamTimeout},
		{name: "WORKFLOW_TIMEOUT", target: &cfg.WorkflowTimeout},
		{name: "HTTP_READ_HEADER_TIMEOUT", target: &cfg.ReadHeaderTimeout},
		{name: "HTTP_READ_TIMEOUT", target: &cfg.ReadTimeout},
		{name: "HTTP_WRITE_TIMEOUT", target: &cfg.WriteTimeout},
		{name: "HTTP_IDLE_TIMEOUT", target: &cfg.IdleTimeout},
		{name: "HTTP_SHUTDOWN_TIMEOUT", target: &cfg.ShutdownTimeout},
	}
	for _, item := range durations {
		if err := loadDuration(lookup, item.name, item.target); err != nil {
			return Config{}, err
		}
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) SearchEnabled() bool {
	return strings.TrimSpace(c.BochaAPIKey) != ""
}

func (c Config) String() string {
	return fmt.Sprintf("mode=%s addr=%s session_store=%s search_enabled=%t", c.Mode, c.Addr, c.SessionStore, c.SearchEnabled())
}

func (c Config) validate() error {
	var problems []string
	if c.Mode != ModeDemo && c.Mode != ModeLive {
		problems = append(problems, "APP_MODE must be demo or live")
	}
	if err := validateAddress(c.Addr); err != nil {
		problems = append(problems, "APP_ADDR: "+err.Error())
	}
	if c.Mode == ModeLive {
		for name, field := range map[string]string{
			"LLM_ENDPOINT": c.LLMEndpoint,
			"LLM_API_KEY":  c.LLMAPIKey,
			"LLM_MODEL":    c.LLMModel,
		} {
			if strings.TrimSpace(field) == "" {
				problems = append(problems, name+" is required in live mode")
			}
		}
	}
	if !validLogLevels[strings.ToLower(strings.TrimSpace(c.LogLevel))] {
		problems = append(problems, "LOG_LEVEL must be one of debug, info, warn, error")
	}
	if c.SessionStore != SessionStoreMemory && c.SessionStore != SessionStoreMySQL {
		problems = append(problems, "SESSION_STORE must be memory or mysql")
	}
	if c.AuthMode != AuthModeRequired && c.AuthMode != AuthModeDisabled {
		problems = append(problems, "AUTH_MODE must be required or disabled")
	}
	if c.Mode == ModeLive && c.AuthMode != AuthModeRequired {
		problems = append(problems, "AUTH_MODE must be required in live mode")
	}
	if c.Mode == ModeLive && c.SessionStore != SessionStoreMySQL {
		problems = append(problems, "SESSION_STORE=mysql is required in live mode")
	}
	if c.SessionStore == SessionStoreMySQL && strings.TrimSpace(c.MySQLDSN) == "" {
		problems = append(problems, "MYSQL_DSN is required when SESSION_STORE=mysql")
	}
	if c.SessionStore == SessionStoreMySQL && len(c.SessionEncryptionKey) != 32 {
		problems = append(problems, "SESSION_ENCRYPTION_KEY must be base64url for exactly 32 bytes when SESSION_STORE=mysql")
	}
	if c.WriteTimeout < c.WorkflowTimeout {
		problems = append(problems, "HTTP_WRITE_TIMEOUT must be at least WORKFLOW_TIMEOUT")
	}
	if c.ShutdownTimeout < c.WorkflowTimeout {
		problems = append(problems, "HTTP_SHUTDOWN_TIMEOUT must be at least WORKFLOW_TIMEOUT")
	}
	if c.WorkflowTimeout > maxWorkflowTimeout {
		problems = append(problems, "WORKFLOW_TIMEOUT must not exceed 45s")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func loadBool(lookup LookupEnv, name string, fallback bool) (bool, error) {
	raw, exists := lookup(name)
	if !exists || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

func decodeEncryptionKey(raw string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		decoded, err := encoding.DecodeString(raw)
		if err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, errors.New("SESSION_ENCRYPTION_KEY must be base64url for exactly 32 bytes")
}

func validateAddress(address string) error {
	_, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return errors.New("must be a host:port address")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

func loadDuration(lookup LookupEnv, name string, target *time.Duration) error {
	raw, exists := lookup(name)
	if !exists || strings.TrimSpace(raw) == "" {
		return nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || parsed <= 0 {
		return fmt.Errorf("%s must be a positive Go duration", name)
	}
	*target = parsed
	return nil
}

func value(lookup LookupEnv, name string) string {
	result, _ := lookup(name)
	return strings.TrimSpace(result)
}

func valueOrDefault(lookup LookupEnv, name, fallback string) string {
	result := value(lookup, name)
	if result == "" {
		return fallback
	}
	return result
}
