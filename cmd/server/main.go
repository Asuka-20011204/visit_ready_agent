package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"visitready/internal/agent"
	"visitready/internal/config"
	"visitready/internal/demo"
	"visitready/internal/httpapi"
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
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
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
	return run(ctx, cfg, logger)
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	handler, store, err := buildHandler(cfg, logger)
	if err != nil {
		return err
	}
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

func buildHandler(cfg config.Config, logger *slog.Logger) (http.Handler, *session.MemoryStore, error) {
	if logger == nil {
		return nil, nil, errors.New("logger is required")
	}
	upstreamClient := &http.Client{Timeout: cfg.UpstreamTimeout}
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
	store := session.NewMemoryStore()
	api, err := httpapi.New(httpapi.Config{Runner: runner, Store: store})
	if err != nil {
		return nil, nil, err
	}
	handler, err := webui.New(api, cfg.Mode)
	if err != nil {
		return nil, nil, err
	}
	return handler, store, nil
}

func buildLLMClient(cfg config.Config, client *http.Client, logger *slog.Logger) (agent.LLMClient, error) {
	if cfg.Mode == config.ModeDemo {
		logger.Warn("demo mode active; deterministic client is in use", "ai_called", false)
		return demo.Client{}, nil
	}
	result, err := llm.NewOpenAICompatibleClient(cfg.LLMEndpoint, cfg.LLMAPIKey, cfg.LLMModel, client)
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
	return search.NewTavilyClient(cfg.TavilyEndpoint, cfg.TavilyAPIKey, client)
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

func startCleanupLoop(ctx context.Context, store *session.MemoryStore, interval time.Duration, logger *slog.Logger) <-chan struct{} {
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
