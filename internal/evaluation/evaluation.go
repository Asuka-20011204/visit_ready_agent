package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"visitready/internal/domain"
)

type Runner interface {
	Start(context.Context, string, bool) (domain.Session, error)
}

type Case struct {
	Name          string   `json:"name"`
	Input         string   `json:"input"`
	ExpectedFacts []string `json:"expected_facts"`
	Emergency     bool     `json:"emergency"`
}

type CaseResult struct {
	Name           string `json:"name"`
	DurationMS     int64  `json:"duration_ms"`
	EmergencyOK    bool   `json:"emergency_ok"`
	ExpectedFacts  int    `json:"expected_facts"`
	MatchedFacts   int    `json:"matched_facts"`
	ProducedFacts  int    `json:"produced_facts"`
	GroundedFacts  int    `json:"grounded_facts"`
	DuplicateFacts int    `json:"duplicate_facts"`
	Error          string `json:"error,omitempty"`
}

type Report struct {
	GeneratedAt       time.Time    `json:"generated_at"`
	CaseCount         int          `json:"case_count"`
	FactPrecision     float64      `json:"fact_precision"`
	FactRecall        float64      `json:"fact_recall"`
	GroundingRate     float64      `json:"grounding_rate"`
	EmergencyAccuracy float64      `json:"emergency_accuracy"`
	DuplicateFacts    int          `json:"duplicate_facts"`
	P50Milliseconds   int64        `json:"p50_ms"`
	P95Milliseconds   int64        `json:"p95_ms"`
	Cases             []CaseResult `json:"cases"`
}

func LoadCases(path string) ([]Case, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read evaluation cases: %w", err)
	}
	var cases []Case
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, fmt.Errorf("decode evaluation cases: %w", err)
	}
	if len(cases) < 20 {
		return nil, fmt.Errorf("evaluation corpus has %d cases; at least 20 are required", len(cases))
	}
	for index, item := range cases {
		if strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.Input) == "" {
			return nil, fmt.Errorf("evaluation case %d requires name and input", index)
		}
	}
	return cases, nil
}

func Run(ctx context.Context, runner Runner, cases []Case, now func() time.Time) Report {
	if now == nil {
		now = time.Now
	}
	report := Report{GeneratedAt: now(), CaseCount: len(cases), Cases: make([]CaseResult, 0, len(cases))}
	var expected, matched, produced, grounded, emergencyCorrect int
	latencies := make([]int64, 0, len(cases))
	for _, item := range cases {
		started := time.Now()
		session, err := runner.Start(ctx, item.Input, false)
		duration := time.Since(started).Milliseconds()
		result := scoreCase(item, session, duration, err)
		report.Cases = append(report.Cases, result)
		expected += result.ExpectedFacts
		matched += result.MatchedFacts
		produced += result.ProducedFacts
		grounded += result.GroundedFacts
		report.DuplicateFacts += result.DuplicateFacts
		if result.EmergencyOK {
			emergencyCorrect++
		}
		latencies = append(latencies, duration)
	}
	report.FactRecall = ratio(matched, expected)
	report.FactPrecision = ratio(grounded, produced)
	report.GroundingRate = ratio(grounded, produced)
	report.EmergencyAccuracy = ratio(emergencyCorrect, len(cases))
	report.P50Milliseconds = percentile(latencies, 0.50)
	report.P95Milliseconds = percentile(latencies, 0.95)
	return report
}

func scoreCase(item Case, session domain.Session, duration int64, runErr error) CaseResult {
	result := CaseResult{Name: item.Name, DurationMS: duration, ExpectedFacts: len(item.ExpectedFacts), ProducedFacts: len(session.Facts)}
	if runErr != nil {
		result.Error = runErr.Error()
	}
	result.EmergencyOK = (session.Status == domain.StatusEmergency) == item.Emergency
	combinedFacts := make([]string, 0, len(session.Facts))
	seen := make(map[string]struct{})
	for _, fact := range session.Facts {
		combinedFacts = append(combinedFacts, fact.Content+" "+fact.SourceQuote)
		key := normalize(fact.Content)
		if _, exists := seen[key]; exists && key != "" {
			result.DuplicateFacts++
		}
		seen[key] = struct{}{}
		if strings.Contains(normalize(item.Input), normalize(fact.SourceQuote)) && normalize(fact.SourceQuote) != "" {
			result.GroundedFacts++
		}
	}
	joined := normalize(strings.Join(combinedFacts, " "))
	for _, expected := range item.ExpectedFacts {
		if strings.Contains(joined, normalize(expected)) {
			result.MatchedFacts++
		}
	}
	return result
}

func normalize(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 1
	}
	return float64(numerator) / float64(denominator)
}

func percentile(values []int64, fraction float64) int64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := int(float64(len(ordered)-1)*fraction + 0.5)
	return ordered[index]
}
