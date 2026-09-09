package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"visitready/internal/agent"
	"visitready/internal/evaluation"
	"visitready/internal/llm"
)

const defaultCases = "internal/evaluation/testdata/visit_cases.json"

func main() {
	if err := run(context.Background()); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "evaluation failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	endpoint := firstEnv("EVAL_LLM_ENDPOINT", "LLM_ENDPOINT")
	apiKey := firstEnv("EVAL_LLM_API_KEY", "LLM_API_KEY")
	model := firstEnv("EVAL_LLM_MODEL", "LLM_MODEL")
	if endpoint == "" || apiKey == "" || model == "" {
		return errors.New("EVAL_LLM_ENDPOINT, EVAL_LLM_API_KEY, and EVAL_LLM_MODEL are required")
	}
	casesPath := strings.TrimSpace(os.Getenv("EVAL_CASES"))
	if casesPath == "" {
		casesPath = defaultCases
	}
	cases, err := evaluation.LoadCases(casesPath)
	if err != nil {
		return err
	}
	httpClient := &http.Client{Timeout: 45 * time.Second}
	modelClient, err := llm.NewOpenAICompatibleClient(endpoint, apiKey, model, httpClient, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	runner, err := agent.NewRunner(agent.Config{LLM: modelClient, SessionTTL: time.Hour})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()
	report := evaluation.Run(ctx, runner, cases, time.Now)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return err
	}
	if report.FactRecall < 0.85 || report.FactPrecision < 0.95 || report.GroundingRate < 0.95 || report.EmergencyAccuracy < 1 || report.DuplicateFacts > 0 || report.P95Milliseconds > 45000 {
		return fmt.Errorf("quality gate failed: precision=%.3f recall=%.3f grounding=%.3f emergency=%.3f duplicates=%d p95_ms=%d", report.FactPrecision, report.FactRecall, report.GroundingRate, report.EmergencyAccuracy, report.DuplicateFacts, report.P95Milliseconds)
	}
	return nil
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}
