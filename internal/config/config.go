package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	ModeDemo = "demo"
	ModeLive = "live"
)

const defaultTavilyEndpoint = "https://api.tavily.com/search"

type LookupEnv func(string) (string, bool)

type Config struct {
	Mode              string
	Addr              string
	LLMEndpoint       string
	LLMAPIKey         string
	LLMModel          string
	TavilyEndpoint    string
	TavilyAPIKey      string
	SessionTTL        time.Duration
	CleanupInterval   time.Duration
	UpstreamTimeout   time.Duration
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

func Load(lookup LookupEnv) (Config, error) {
	if lookup == nil {
		return Config{}, errors.New("environment lookup is required")
	}

	cfg := Config{
		Mode:              valueOrDefault(lookup, "APP_MODE", ModeDemo),
		Addr:              valueOrDefault(lookup, "APP_ADDR", ":8080"),
		LLMEndpoint:       value(lookup, "LLM_ENDPOINT"),
		LLMAPIKey:         value(lookup, "LLM_API_KEY"),
		LLMModel:          value(lookup, "LLM_MODEL"),
		TavilyEndpoint:    valueOrDefault(lookup, "TAVILY_ENDPOINT", defaultTavilyEndpoint),
		TavilyAPIKey:      value(lookup, "TAVILY_API_KEY"),
		SessionTTL:        30 * time.Minute,
		CleanupInterval:   time.Minute,
		UpstreamTimeout:   25 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
		ShutdownTimeout:   55 * time.Second,
	}

	durations := []struct {
		name   string
		target *time.Duration
	}{
		{name: "SESSION_TTL", target: &cfg.SessionTTL},
		{name: "SESSION_CLEANUP_INTERVAL", target: &cfg.CleanupInterval},
		{name: "UPSTREAM_TIMEOUT", target: &cfg.UpstreamTimeout},
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
	return strings.TrimSpace(c.TavilyAPIKey) != ""
}

func (c Config) String() string {
	return fmt.Sprintf("mode=%s addr=%s search_enabled=%t", c.Mode, c.Addr, c.SearchEnabled())
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
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
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
