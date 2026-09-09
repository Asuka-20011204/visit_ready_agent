package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"visitready/internal/agent"
	"visitready/internal/auth"
	"visitready/internal/config"
	"visitready/internal/demo"
	"visitready/internal/httpapi"
	"visitready/internal/httplog"
	"visitready/internal/llm"
	"visitready/internal/search"
	"visitready/internal/session"
	"visitready/internal/webui"
)

var trustedDomains = []string{
	"who.int",
	"nhc.gov.cn",
	"gov.cn",
	"medlineplus.gov",
	"cdc.gov",
	"fda.gov",
}

func main() {
	logger := newLogger("info")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, os.LookupEnv, logger); err != nil {
		logger.Error("server stopped with error", "error", err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, lookup config.LookupEnv, logger *slog.Logger) error {
	cfg, err := config.Load(lookup)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	return run(ctx, cfg, newLogger(cfg.LogLevel))
}

// newLogger builds a JSON slog logger whose level follows the validated
// LOG_LEVEL config value.
func newLogger(levelName string) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(levelName)) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	handler, store, err := buildHandler(cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Warn("session store close failed", "error", err)
		}
	}()
	server := newHTTPServer(cfg, handler)
	cleanupCtx, stopCleanup := context.WithCancel(ctx)
	cleanupDone := startCleanupLoop(cleanupCtx, store, cfg.CleanupInterval, logger)
	defer func() {
		stopCleanup()
		<-cleanupDone
	}()

	serveErrors := make(chan error, 1)
	go func() {
		logger.Info("HTTP server listening",
			"address", cfg.Addr,
			"mode", cfg.Mode,
			"search_enabled", cfg.SearchEnabled(),
			"session_store", cfg.SessionStore,
			"upstream_timeout", cfg.UpstreamTimeout.String(),
			"workflow_timeout", cfg.WorkflowTimeout.String(),
		)
		serveErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("HTTP server shutdown started")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	logger.Info("HTTP server shutdown completed")
	return nil
}

func buildHandler(cfg config.Config, logger *slog.Logger) (http.Handler, session.HealthStore, error) {
	if logger == nil {
		return nil, nil, errors.New("logger is required")
	}
	upstreamClient := &http.Client{
		Timeout:   cfg.UpstreamTimeout,
		Transport: httplog.Transport(http.DefaultTransport, logger),
	}
	llmClient, err := buildLLMClient(cfg, upstreamClient, logger)
	if err != nil {
		return nil, nil, err
	}
	searchClient, err := buildSearchClient(cfg, upstreamClient)
	if err != nil {
		return nil, nil, err
	}

	runner, err := agent.NewRunner(agent.Config{
		LLM:            llmClient,
		Search:         searchClient,
		AllowedDomains: trustedDomains,
		SessionTTL:     cfg.SessionTTL,
	})
	if err != nil {
		return nil, nil, err
	}
	store, err := buildSessionStore(cfg)
	if err != nil {
		return nil, nil, err
	}
	var accounts *auth.Service
	var accountStore *auth.MySQLStore
	if cfg.AuthMode == config.AuthModeRequired {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		accountStore, err = auth.OpenMySQLStore(ctx, cfg.MySQLDSN)
		if err != nil {
			_ = store.Close()
			return nil, nil, err
		}
		accounts = auth.NewService(accountStore)
	}
	api, err := httpapi.New(httpapi.Config{
		Runner:             runner,
		Store:              store,
		Logger:             logger,
		WorkflowTimeout:    cfg.WorkflowTimeout,
		RequireDeviceAuth:  cfg.AuthMode == config.AuthModeDisabled,
		RequireAccountAuth: cfg.AuthMode == config.AuthModeRequired,
		Accounts:           accounts,
		SecureCookies:      cfg.CookieSecure,
	})
	if err != nil {
		if accountStore != nil {
			_ = accountStore.Close()
		}
		_ = store.Close()
		return nil, nil, err
	}
	handler, err := webui.New(api, cfg.Mode, cfg.SessionTTL)
	if err != nil {
		return nil, nil, err
	}
	if accountStore != nil {
		return httplog.AccessLog(handler, logger), combinedStore{HealthStore: store, accountStore: accountStore}, nil
	}
	return httplog.AccessLog(handler, logger), store, nil
}

type combinedStore struct {
	session.HealthStore
	accountStore *auth.MySQLStore
}

func (s combinedStore) Close() error {
	first := s.HealthStore.Close()
	second := s.accountStore.Close()
	if first != nil {
		return first
	}
	return second
}

func buildSessionStore(cfg config.Config) (session.HealthStore, error) {
	if cfg.SessionStore != config.SessionStoreMySQL {
		return session.NewMemoryStore(), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := session.OpenMySQLStore(ctx, cfg.MySQLDSN, cfg.SessionEncryptionKey, 1024)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func buildLLMClient(cfg config.Config, client *http.Client, logger *slog.Logger) (agent.LLMClient, error) {
	if cfg.Mode == config.ModeDemo {
		logger.Warn("demo mode active; deterministic client is in use", "ai_called", false)
		return demo.Client{}, nil
	}
	result, err := llm.NewOpenAICompatibleClient(cfg.LLMEndpoint, cfg.LLMAPIKey, cfg.LLMModel, client, logger)
	if err != nil {
		return nil, err
	}
	logger.Info("live AI client configured", "ai_called", false)
	return result, nil
}

func buildSearchClient(cfg config.Config, client *http.Client) (agent.SearchClient, error) {
	if cfg.Mode == config.ModeDemo || !cfg.SearchEnabled() {
		return nil, nil
	}
	return search.NewBochaClient(cfg.BochaEndpoint, cfg.BochaAPIKey, client)
}

func newHTTPServer(cfg config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
}

func startCleanupLoop(ctx context.Context, store session.Store, interval time.Duration, logger *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				if removed := store.DeleteExpired(now); removed > 0 {
					logger.Info("expired sessions removed", "count", removed)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return done
}
