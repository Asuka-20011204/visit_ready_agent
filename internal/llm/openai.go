package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"visitready/internal/domain"
)

const maxCompletionBytes = 1 << 20

type OpenAICompatibleClient struct {
	endpoint   string
	apiKey     string
	model      string
	httpClient *http.Client
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type completionRequest struct {
	Model          string            `json:"model"`
	Messages       []chatMessage     `json:"messages"`
	Temperature    float64           `json:"temperature"`
	ResponseFormat map[string]string `json:"response_format"`
}

type completionResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

func NewOpenAICompatibleClient(endpoint, apiKey, model string, client *http.Client) (*OpenAICompatibleClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" {
		return nil, errors.New("invalid LLM endpoint")
	}
	if parsed.Scheme != "https" && !isLoopback(parsed.Hostname()) {
		return nil, errors.New("LLM endpoint must use HTTPS")
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
	}, nil
}

func (c *OpenAICompatibleClient) Extract(ctx context.Context, input string) (domain.Extraction, error) {
	userPrompt := "<patient_input>\n" + input + "\n</patient_input>"
	content, err := c.completeJSON(ctx, extractionSystemPrompt, userPrompt)
	if err != nil {
		return domain.Extraction{}, fmt.Errorf("extract visit facts: %w", err)
	}

	var result domain.Extraction
	if err := decodeStrictJSON(content, &result); err != nil {
		return domain.Extraction{}, fmt.Errorf("decode extraction: %w", err)
	}
	if err := validateExtraction(result); err != nil {
		return domain.Extraction{}, err
	}
	return result, nil
}

func (c *OpenAICompatibleClient) GenerateQuestions(ctx context.Context, input domain.QuestionInput) (domain.QuestionSet, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return domain.QuestionSet{}, fmt.Errorf("encode question input: %w", err)
	}
	userPrompt := "<verified_context>\n" + string(payload) + "\n</verified_context>"
	content, err := c.completeJSON(ctx, questionSystemPrompt, userPrompt)
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
	return result, nil
}

func (c *OpenAICompatibleClient) completeJSON(ctx context.Context, systemPrompt, userPrompt string) ([]byte, error) {
	body, err := json.Marshal(completionRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature:    0.1,
		ResponseFormat: map[string]string{"type": "json_object"},
	})
	if err != nil {
		return nil, fmt.Errorf("encode completion request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create completion request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call completion API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("completion provider returned status %d", resp.StatusCode)
	}

	var decoded completionResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxCompletionBytes))
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode completion response: %w", err)
	}
	if len(decoded.Choices) == 0 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		return nil, errors.New("completion response did not contain content")
	}
	return []byte(stripCodeFence(decoded.Choices[0].Message.Content)), nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
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

func validateExtraction(result domain.Extraction) error {
	if len(result.Facts) == 0 || len(result.Facts) > 30 {
		return errors.New("extraction must contain 1 to 30 facts")
	}
	if len(result.ClarificationQuestions) > 3 {
		return errors.New("extraction contains too many clarification questions")
	}
	if len(result.SearchQueries) > 2 {
		return errors.New("extraction contains too many search queries")
	}
	for _, fact := range result.Facts {
		if strings.TrimSpace(fact.Category) == "" || strings.TrimSpace(fact.Content) == "" || strings.TrimSpace(fact.SourceQuote) == "" {
			return errors.New("each fact requires category, content, and source_quote")
		}
	}
	return nil
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
