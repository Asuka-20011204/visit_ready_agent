package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"visitready/internal/domain"
	"visitready/internal/llm"
)

func TestOpenAICompatibleClientExtractsStructuredFacts(t *testing.T) {
	var captured struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer llm-key" {
			t.Fatalf("Authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"visit_goal\":\"了解咳嗽原因\",\"facts\":[{\"category\":\"symptom\",\"content\":\"咳嗽三天\",\"source_quote\":\"三天前开始咳嗽\"}],\"missing_fields\":[],\"clarification_questions\":[],\"search_queries\":[\"咳嗽 就诊准备\"]}"}}]}`))
	}))
	defer server.Close()

	client, err := llm.NewOpenAICompatibleClient(server.URL, "llm-key", "test-model", server.Client())
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}

	result, err := client.Extract(context.Background(), "三天前开始咳嗽")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if result.VisitGoal != "了解咳嗽原因" || len(result.Facts) != 1 {
		t.Fatalf("Extract() = %#v", result)
	}
	if captured.Model != "test-model" {
		t.Fatalf("model = %q", captured.Model)
	}
	joined := captured.Messages[0].Content + captured.Messages[1].Content
	if !strings.Contains(joined, "不得诊断") || !strings.Contains(joined, "<patient_input>") {
		t.Fatalf("prompt does not contain safety boundary and data delimiter: %q", joined)
	}
}

func TestOpenAICompatibleClientRejectsInvalidModelJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"not json"}}]}`))
	}))
	defer server.Close()

	client, err := llm.NewOpenAICompatibleClient(server.URL, "llm-key", "test-model", server.Client())
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	if _, err := client.Extract(context.Background(), "三天前开始咳嗽"); err == nil {
		t.Fatal("Extract() error = nil, want invalid JSON error")
	}
}

func TestOpenAICompatibleClientGeneratesGroundedQuestions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"questions\":[{\"text\":\"我是否需要记录咳嗽出现的具体时段？\",\"source_url\":\"https://www.who.int/health-topics/cough\"}]}"}}]}`))
	}))
	defer server.Close()

	client, err := llm.NewOpenAICompatibleClient(server.URL, "llm-key", "test-model", server.Client())
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	result, err := client.GenerateQuestions(context.Background(), domain.QuestionInput{
		VisitGoal: "了解咳嗽原因",
		Facts:     []domain.Fact{{Content: "咳嗽三天", SourceQuote: "三天前开始咳嗽"}},
		Sources:   []domain.Source{{Title: "WHO advice", URL: "https://www.who.int/health-topics/cough", Domain: "who.int"}},
	})
	if err != nil {
		t.Fatalf("GenerateQuestions() error = %v", err)
	}
	if len(result.Questions) != 1 || result.Questions[0].SourceURL == "" {
		t.Fatalf("GenerateQuestions() = %#v", result)
	}
}

func TestNewOpenAICompatibleClientRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		key      string
		model    string
		client   *http.Client
	}{
		{name: "invalid endpoint", endpoint: "://", key: "key", model: "model", client: http.DefaultClient},
		{name: "insecure endpoint", endpoint: "http://public.example/v1/chat/completions", key: "key", model: "model", client: http.DefaultClient},
		{name: "missing key", endpoint: "https://api.example/v1/chat/completions", model: "model", client: http.DefaultClient},
		{name: "missing model", endpoint: "https://api.example/v1/chat/completions", key: "key", client: http.DefaultClient},
		{name: "missing client", endpoint: "https://api.example/v1/chat/completions", key: "key", model: "model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := llm.NewOpenAICompatibleClient(tt.endpoint, tt.key, tt.model, tt.client); err == nil {
				t.Fatal("NewOpenAICompatibleClient() error = nil")
			}
		})
	}
}

func TestOpenAICompatibleClientMapsProviderResponseErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "upstream status", status: http.StatusTooManyRequests, body: `rate limited`},
		{name: "malformed envelope", status: http.StatusOK, body: `not-json`},
		{name: "empty choices", status: http.StatusOK, body: `{"choices":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client, _ := llm.NewOpenAICompatibleClient(server.URL, "key", "model", server.Client())
			if _, err := client.Extract(context.Background(), "三天前开始咳嗽"); err == nil {
				t.Fatal("Extract() error = nil")
			}
		})
	}
}

func TestOpenAICompatibleClientRejectsInvalidExtractionShapes(t *testing.T) {
	responses := []string{
		`{"visit_goal":"目标","facts":[],"missing_fields":[],"clarification_questions":[],"search_queries":[]}`,
		`{"visit_goal":"目标","facts":[{"category":"symptom","content":"咳嗽","source_quote":""}],"missing_fields":[],"clarification_questions":[],"search_queries":[]}`,
		`{"visit_goal":"目标","facts":[{"category":"symptom","content":"咳嗽","source_quote":"咳嗽"}],"missing_fields":[],"clarification_questions":["1","2","3","4"],"search_queries":[]}`,
		`{"visit_goal":"目标","facts":[{"category":"symptom","content":"咳嗽","source_quote":"咳嗽"}],"missing_fields":[],"clarification_questions":[],"search_queries":["1","2","3"]}`,
		`{"visit_goal":"目标","facts":[{"category":"symptom","content":"咳嗽","source_quote":"咳嗽"}],"missing_fields":[],"clarification_questions":[],"search_queries":[]} {}`,
	}
	for index, content := range responses {
		client, closeServer := clientReturning(t, content)
		if _, err := client.Extract(context.Background(), "我已经咳嗽三天，目前没有服用药物。"); err == nil {
			closeServer()
			t.Fatalf("response %d error = nil", index)
		}
		closeServer()
	}
}

func TestOpenAICompatibleClientStripsCodeFence(t *testing.T) {
	content := "```json\n{\"visit_goal\":\"目标\",\"facts\":[{\"category\":\"symptom\",\"content\":\"咳嗽\",\"source_quote\":\"咳嗽\"}],\"missing_fields\":[],\"clarification_questions\":[],\"search_queries\":[]}\n```"
	client, closeServer := clientReturning(t, content)
	defer closeServer()
	if _, err := client.Extract(context.Background(), "我已经咳嗽三天，目前没有服用药物。"); err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
}

func TestOpenAICompatibleClientRejectsInvalidQuestionCount(t *testing.T) {
	for _, content := range []string{`{"questions":[]}`, `{"questions":[{},{},{},{},{},{},{}]}`} {
		client, closeServer := clientReturning(t, content)
		if _, err := client.GenerateQuestions(context.Background(), domain.QuestionInput{}); err == nil {
			closeServer()
			t.Fatal("GenerateQuestions() error = nil")
		}
		closeServer()
	}
}

func clientReturning(t *testing.T, content string) (*llm.OpenAICompatibleClient, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}}})
	}))
	client, err := llm.NewOpenAICompatibleClient(server.URL, "key", "model", server.Client())
	if err != nil {
		server.Close()
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	return client, server.Close
}
