package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"visitready/internal/domain"
	"visitready/internal/guard"
)

const (
	maxCompletionBytes  = 1 << 20
	extractionMaxTokens = 1400
	questionsMaxTokens  = 1000
)

const (
	maxProviderAttempts = 2
	defaultRetryDelay   = 150 * time.Millisecond
	maxRetryDelay       = 750 * time.Millisecond
	circuitFailureLimit = 3
	circuitCooldown     = 15 * time.Second
)

var errEmptyCompletion = errors.New("completion response contained empty content")

type OpenAICompatibleClient struct {
	endpoint          string
	apiKey            string
	model             string
	httpClient        *http.Client
	logger            *slog.Logger
	circuitMu         sync.Mutex
	transientFailures int
	circuitOpenUntil  time.Time
	now               func() time.Time
}

type completionError struct {
	err       error
	retryable bool
}

func (e completionError) Error() string   { return e.err.Error() }
func (e completionError) Unwrap() error   { return e.err }
func (e completionError) Retryable() bool { return e.retryable }

func markCompletionError(err error, retryable bool) error {
	return completionError{err: err, retryable: retryable}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type completionRequest struct {
	Model          string            `json:"model"`
	Messages       []chatMessage     `json:"messages"`
	Temperature    float64           `json:"temperature"`
	Stream         bool              `json:"stream"`
	ResponseFormat map[string]string `json:"response_format"`
	MaxTokens      int               `json:"max_tokens"`
	Thinking       map[string]string `json:"thinking"`
}

type completionResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

func NewOpenAICompatibleClient(endpoint, apiKey, model string, client *http.Client, logger *slog.Logger) (*OpenAICompatibleClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" {
		return nil, errors.New("invalid LLM endpoint")
	}
	if parsed.Scheme != "https" && !isLoopback(parsed.Hostname()) {
		return nil, errors.New("LLM endpoint must use HTTPS")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("LLM endpoint must not contain userinfo, query, or fragment")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("LLM API key is required")
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("LLM model is required")
	}
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	return &OpenAICompatibleClient{
		endpoint:   endpoint,
		apiKey:     apiKey,
		model:      model,
		httpClient: client,
		logger:     logger,
		now:        time.Now,
	}, nil
}

// modelOutputError marks a failure caused by the model returning output the
// deterministic boundary could not decode or validate. These are retryable:
// the same input commonly succeeds on a later attempt, and the fault is the
// model's output, never the user's description.
type modelOutputError struct{ err error }

func (e modelOutputError) Error() string   { return e.err.Error() }
func (e modelOutputError) Unwrap() error   { return e.err }
func (e modelOutputError) Retryable() bool { return true }

func (c *OpenAICompatibleClient) Extract(ctx context.Context, input string) (domain.Extraction, error) {
	userPrompt := "<patient_input>\n" + input + "\n</patient_input>"
	started := time.Now()
	content, err := c.completeStructuredJSON(ctx, extractionSystemPrompt, userPrompt, extractionMaxTokens)
	c.logCall("extract_facts", started, err)
	if err != nil {
		return domain.Extraction{}, fmt.Errorf("extract visit facts: %w", err)
	}

	var result domain.Extraction
	if err := decodeStrictJSON(content, &result); err != nil {
		return domain.Extraction{}, modelOutputError{fmt.Errorf("decode extraction: %w", err)}
	}
	result = sanitizeOptionalExtraction(input, result)
	if err := validateExtraction(input, result); err != nil {
		return domain.Extraction{}, modelOutputError{err}
	}
	return result, nil
}

func sanitizeOptionalExtraction(input string, result domain.Extraction) domain.Extraction {
	hadStructuredPrompts := len(result.ClarificationPrompts) > 0
	// Facts use the same drop-based policy as profiles and timeline: an
	// ungrounded or empty fact is discarded instead of failing the whole
	// extraction, because the model can misquote a source on a single pass.
	facts := make([]domain.Fact, 0, len(result.Facts))
	for _, fact := range result.Facts {
		sourceQuote := strings.TrimSpace(fact.SourceQuote)
		if strings.TrimSpace(fact.Category) == "" || sourceQuote == "" || !groundedQuote(input, sourceQuote) {
			continue
		}
		content := strings.TrimSpace(fact.Content)
		if !groundedValue(sourceQuote, content) {
			content = sourceQuote
		}
		fact.Content = content
		fact.SourceQuote = sourceQuote
		facts = append(facts, fact)
	}
	result.Facts = facts
	if len(result.Facts) > 30 {
		result.Facts = result.Facts[:30]
	}

	profiles := make([]domain.SymptomProfile, 0, len(result.SymptomProfiles))
	for _, profile := range result.SymptomProfiles {
		if strings.TrimSpace(profile.Name) == "" || !groundedQuote(input, profile.SourceQuote) {
			continue
		}
		evidence := make([]string, 0, len(profile.EvidenceQuotes))
		for _, quote := range profile.EvidenceQuotes {
			if groundedQuote(input, quote) {
				evidence = append(evidence, quote)
			}
		}
		profile.EvidenceQuotes = evidence
		profiles = append(profiles, profile)
	}
	result.SymptomProfiles = profiles

	timeline := make([]domain.TimelineEvent, 0, len(result.Timeline))
	for _, event := range result.Timeline {
		timeLabel := strings.TrimSpace(event.TimeLabel)
		if timeLabel == "" || strings.TrimSpace(event.Event) == "" || !groundedQuote(input, event.SourceQuote) || !strings.Contains(event.SourceQuote, timeLabel) {
			continue
		}
		timeline = append(timeline, event)
	}
	result.Timeline = timeline

	risks := make([]domain.RiskSignal, 0, len(result.RiskSignals))
	for _, signal := range result.RiskSignals {
		if !validPriority(signal.Priority) || strings.TrimSpace(signal.Title) == "" || strings.TrimSpace(signal.Evidence) == "" || strings.TrimSpace(signal.Guidance) == "" || !groundedQuote(input, signal.SourceQuote) {
			continue
		}
		risks = append(risks, signal)
	}
	result.RiskSignals = risks

	prompts := make([]domain.Question, 0, len(result.ClarificationPrompts))
	for _, prompt := range result.ClarificationPrompts {
		if validateQuestion(prompt) == nil {
			prompts = append(prompts, prompt)
		}
	}
	result.ClarificationPrompts = prompts
	if len(result.ClarificationPrompts) > 3 {
		result.ClarificationPrompts = result.ClarificationPrompts[:3]
	}
	if hadStructuredPrompts {
		result.ClarificationQuestions = make([]string, 0, len(result.ClarificationPrompts))
		for _, prompt := range result.ClarificationPrompts {
			result.ClarificationQuestions = append(result.ClarificationQuestions, prompt.Text)
		}
	}
	if len(result.ClarificationQuestions) > 3 {
		result.ClarificationQuestions = result.ClarificationQuestions[:3]
	}

	items := make([]domain.MissingField, 0, len(result.MissingFieldItems))
	seenItems := make(map[string]struct{}, len(result.MissingFieldItems))
	for _, item := range result.MissingFieldItems {
		item.Field = strings.TrimSpace(item.Field)
		if item.Field == "" || len([]rune(item.Field)) > 160 || guard.ContainsMedicalOverreach(item.Field) || len(guard.ScanPII(item.Field)) > 0 {
			continue
		}
		if !validMissingFieldCategory(item.Category) {
			item.Category = "other"
		}
		key := strings.Join(strings.Fields(item.Category+"\x1f"+item.Field), " ")
		if _, exists := seenItems[key]; exists {
			continue
		}
		seenItems[key] = struct{}{}
		items = append(items, item)
	}
	result.MissingFieldItems = items
	if len(result.MissingFieldItems) > 20 {
		result.MissingFieldItems = result.MissingFieldItems[:20]
	}
	if len(result.MissingFields) > 20 {
		result.MissingFields = result.MissingFields[:20]
	}
	if len(result.SearchQueries) > 2 {
		result.SearchQueries = result.SearchQueries[:2]
	}
	result.Medications = sanitizeGlobals(input, result.Medications, 20)
	result.Allergies = sanitizeGlobals(input, result.Allergies, 20)
	result.ChronicConditions = sanitizeGlobals(input, result.ChronicConditions, 20)
	result.TraumaHistory = sanitizeGlobals(input, result.TraumaHistory, 20)
	result.DeniedConditions = sanitizeGlobals(input, result.DeniedConditions, 30)
	result.Measurements = sanitizeGlobals(input, result.Measurements, 20)
	result.Tests = sanitizeGlobals(input, result.Tests, 20)
	return result
}

// sanitizeGlobals keeps only global entries whose source_quote is verbatim in
// the input, normalizes the value to the quote when it is not grounded, and
// deduplicates and caps the list.
func sanitizeGlobals(input string, items []domain.ExtractedGlobal, max int) []domain.ExtractedGlobal {
	result := make([]domain.ExtractedGlobal, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		quote := strings.TrimSpace(item.SourceQuote)
		if quote == "" || !groundedQuote(input, quote) {
			continue
		}
		value := strings.TrimSpace(item.Value)
		if value == "" || !groundedValue(quote, value) {
			value = quote
		}
		key := strings.Join(strings.Fields(item.Category+"\x1f"+value), " ")
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		item.Value = value
		item.SourceQuote = quote
		result = append(result, item)
		if len(result) == max {
			break
		}
	}
	return result
}

func (c *OpenAICompatibleClient) GenerateQuestions(ctx context.Context, input domain.QuestionInput) (domain.QuestionSet, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return domain.QuestionSet{}, fmt.Errorf("encode question input: %w", err)
	}
	userPrompt := "<verified_context>\n" + string(payload) + "\n</verified_context>"
	started := time.Now()
	content, err := c.completeStructuredJSON(ctx, questionSystemPrompt, userPrompt, questionsMaxTokens)
	c.logCall("generate_questions", started, err)
	if err != nil {
		return domain.QuestionSet{}, fmt.Errorf("generate visit questions: %w", err)
	}

	var result domain.QuestionSet
	if err := decodeStrictJSON(content, &result); err != nil {
		return domain.QuestionSet{}, fmt.Errorf("decode questions: %w", err)
	}
	if len(result.Questions) == 0 || len(result.Questions) > 6 {
		return domain.QuestionSet{}, errors.New("question response must contain 1 to 6 questions")
	}
	for _, question := range result.Questions {
		if err := validateQuestion(question); err != nil {
			return domain.QuestionSet{}, err
		}
	}
	if len(result.ActionItems) > 5 {
		return domain.QuestionSet{}, errors.New("question response contains too many action items")
	}
	for _, item := range result.ActionItems {
		if strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Detail) == "" || strings.TrimSpace(item.Reason) == "" || !validPriority(item.Priority) || !validActionCategory(item.Category) {
			return domain.QuestionSet{}, errors.New("action item is incomplete or invalid")
		}
	}
	return result, nil
}

// logCall records the end-to-end outcome of one workflow LLM call,
// including the time spent waiting for and reading the provider response.
func (c *OpenAICompatibleClient) logCall(step string, started time.Time, err error) {
	if c.logger == nil {
		return
	}
	attrs := []any{"step", step, "duration_ms", time.Since(started).Milliseconds()}
	if err != nil {
		c.logger.Warn("llm_call_failed", append(attrs, "error_type", classifyCallError(err))...)
		return
	}
	c.logger.Debug("llm_call_ok", attrs...)
}

func classifyCallError(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return "network_timeout"
		}
		return "request_failed"
	}
}

func (c *OpenAICompatibleClient) completeJSON(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) ([]byte, error) {
	if err := c.checkCircuit(ctx); err != nil {
		return nil, err
	}
	body, err := json.Marshal(completionRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature:    0.1,
		Stream:         false,
		ResponseFormat: map[string]string{"type": "json_object"},
		MaxTokens:      maxTokens,
		Thinking:       map[string]string{"type": "disabled"},
	})
	if err != nil {
		return nil, fmt.Errorf("encode completion request: %w", err)
	}

	for attempt := 0; attempt < maxProviderAttempts; attempt++ {
		content, retryAfter, retryable, err := c.doCompletionRequest(ctx, body)
		if err == nil {
			c.recordProviderSuccess()
			return content, nil
		}
		if !retryable || attempt+1 >= maxProviderAttempts {
			if retryable && ctx.Err() == nil {
				c.recordTransientFailure()
			}
			return nil, err
		}
		if err := waitForRetry(ctx, jitterRetryDelay(retryAfter)); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("completion provider attempts exhausted")
}

func (c *OpenAICompatibleClient) checkCircuit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.circuitMu.Lock()
	defer c.circuitMu.Unlock()
	now := c.now()
	if c.circuitOpenUntil.IsZero() || !now.Before(c.circuitOpenUntil) {
		if !c.circuitOpenUntil.IsZero() {
			c.circuitOpenUntil = time.Time{}
			c.transientFailures = 0
		}
		return nil
	}
	return markCompletionError(errors.New("completion provider circuit is open"), true)
}

func (c *OpenAICompatibleClient) recordProviderSuccess() {
	c.circuitMu.Lock()
	c.transientFailures = 0
	c.circuitOpenUntil = time.Time{}
	c.circuitMu.Unlock()
}

func (c *OpenAICompatibleClient) recordTransientFailure() {
	c.circuitMu.Lock()
	defer c.circuitMu.Unlock()
	c.transientFailures++
	if c.transientFailures >= circuitFailureLimit {
		c.circuitOpenUntil = c.now().Add(circuitCooldown)
	}
}

func (c *OpenAICompatibleClient) doCompletionRequest(ctx context.Context, body []byte) ([]byte, time.Duration, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, false, markCompletionError(fmt.Errorf("create completion request: %w", err), false)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		retryable := isRetryableNetworkError(ctx, err)
		return nil, defaultRetryDelay, retryable, markCompletionError(fmt.Errorf("call completion API: %w", err), retryable)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		retryable := isRetryableStatus(resp.StatusCode)
		return nil, retryDelay(resp.Header.Get("Retry-After")), retryable, markCompletionError(fmt.Errorf("completion provider returned status %d", resp.StatusCode), retryable)
	}

	var decoded completionResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxCompletionBytes))
	if err := decoder.Decode(&decoded); err != nil {
		return nil, 0, false, markCompletionError(fmt.Errorf("decode completion response: %w", err), false)
	}
	if len(decoded.Choices) == 0 {
		return nil, 0, false, markCompletionError(errors.New("completion response did not contain choices"), false)
	}
	if strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		return nil, 0, false, markCompletionError(errEmptyCompletion, false)
	}
	return []byte(stripCodeFence(decoded.Choices[0].Message.Content)), 0, false, nil
}

func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func isRetryableNetworkError(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		if dnsError.IsNotFound {
			return false
		}
		return dnsError.IsTimeout || dnsError.IsTemporary
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	for _, transient := range []error{
		syscall.ECONNABORTED,
		syscall.ECONNREFUSED,
		syscall.ECONNRESET,
		syscall.EHOSTUNREACH,
		syscall.ENETUNREACH,
		syscall.EPIPE,
		syscall.ETIMEDOUT,
	} {
		if errors.Is(err, transient) {
			return true
		}
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func retryDelay(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err == nil && seconds >= 0 {
		return min(time.Duration(seconds)*time.Second, maxRetryDelay)
	}
	return defaultRetryDelay
}

func jitterRetryDelay(delay time.Duration) time.Duration {
	delay = min(delay, maxRetryDelay)
	room := maxRetryDelay - delay
	maxJitter := min(delay/4, room)
	if maxJitter <= 0 {
		return delay
	}
	return delay + time.Duration(rand.Int64N(int64(maxJitter)+1))
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(min(delay, maxRetryDelay))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *OpenAICompatibleClient) completeStructuredJSON(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) ([]byte, error) {
	content, err := c.completeJSON(ctx, systemPrompt, userPrompt, maxTokens)
	if !errors.Is(err, errEmptyCompletion) {
		return content, err
	}
	retryPrompt := userPrompt + "\n直接返回完整 JSON 对象，不要返回空内容。"
	return c.completeJSON(ctx, systemPrompt, retryPrompt, maxTokens)
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("response contains multiple JSON values")
		}
		return err
	}
	return nil
}

func validateExtraction(input string, result domain.Extraction) error {
	// Item-level shape, grounding, safety and count constraints are enforced by
	// sanitizeOptionalExtraction (drop/truncate). The only genuine failure is
	// producing no usable fact at all.
	if len(result.Facts) == 0 {
		return errors.New("extraction must contain at least one fact")
	}
	return nil
}

func validateQuestion(question domain.Question) error {
	if strings.TrimSpace(question.Text) == "" || strings.TrimSpace(question.Reason) == "" {
		return errors.New("question requires text and reason")
	}
	if !validPriority(question.Priority) || !validQuestionCategory(question.Category) {
		return errors.New("question contains an invalid priority or category")
	}
	return nil
}

func groundedQuote(input, quote string) bool {
	quote = strings.TrimSpace(quote)
	return quote != "" && strings.Contains(input, quote)
}

func groundedValue(quote, value string) bool {
	quote = strings.Join(strings.Fields(strings.TrimSpace(quote)), "")
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), "")
	if value == "" {
		return false
	}
	if strings.Contains(quote, value) {
		return true
	}
	valueRunes := []rune(value)
	if len(valueRunes) < 3 {
		return false
	}
	valueIndex := 0
	for _, char := range []rune(quote) {
		if valueIndex < len(valueRunes) && char == valueRunes[valueIndex] {
			valueIndex++
		}
	}
	return valueIndex == len(valueRunes)
}

func validPriority(priority domain.Priority) bool {
	switch priority {
	case domain.PriorityUrgent, domain.PriorityHigh, domain.PriorityNormal:
		return true
	default:
		return false
	}
}

func validQuestionCategory(category string) bool {
	switch category {
	case "safety", "timeline", "symptom", "medication", "test", "visit",
		"duration", "frequency", "severity", "pattern", "trigger", "measurement",
		"associated_symptom", "medication_history", "allergy", "missing_detail", "symptom_detail":
		return true
	default:
		return false
	}
}

// validMissingFieldCategory shares its slot categories with the agent's
// write-back table so every classified gap is either writable or explicitly
// unclassified (other).
func validMissingFieldCategory(category string) bool {
	switch category {
	case "", "other", "onset", "duration", "frequency", "severity", "pattern", "trigger",
		"associated", "medication", "allergy", "history", "safety", "measurement", "test":
		return true
	default:
		return false
	}
}

func validActionCategory(category string) bool {
	switch category {
	case "safety", "tracking", "medication_history", "records", "visit":
		return true
	default:
		return false
	}
}

func stripCodeFence(value string) string {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 3 || !strings.HasPrefix(lines[len(lines)-1], "```") {
		return trimmed
	}
	return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
