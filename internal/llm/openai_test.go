package llm_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"

	"visitready/internal/domain"
	"visitready/internal/llm"
)

func TestOpenAICompatibleClientExtractsStructuredFacts(t *testing.T) {
	var captured struct {
		Model     string `json:"model"`
		Stream    bool   `json:"stream"`
		MaxTokens int    `json:"max_tokens"`
		Thinking  struct {
			Type string `json:"type"`
		} `json:"thinking"`
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
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"visit_goal\":\"了解咳嗽原因\",\"facts\":[{\"category\":\"symptom\",\"content\":\"咳嗽三天\",\"source_quote\":\"三天前开始咳嗽\"}],\"symptom_profiles\":[{\"name\":\"咳嗽\",\"onset\":\"三天前\",\"source_quote\":\"三天前开始咳嗽\"}],\"timeline\":[{\"time_label\":\"三天前\",\"event\":\"开始咳嗽\",\"source_quote\":\"三天前开始咳嗽\"}],\"risk_signals\":[],\"missing_fields\":[],\"clarification_questions\":[],\"search_queries\":[\"咳嗽 就诊准备\"]}"}}]}`))
	}))
	defer server.Close()

	client, err := llm.NewOpenAICompatibleClient(server.URL, "llm-key", "test-model", server.Client(), nil)
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
	if len(result.SymptomProfiles) != 1 || len(result.Timeline) != 1 {
		t.Fatalf("structured assessment missing: %#v", result)
	}
	if captured.Model != "test-model" {
		t.Fatalf("model = %q", captured.Model)
	}
	if captured.Stream {
		t.Fatal("stream = true, want explicit non-streaming response")
	}
	if captured.MaxTokens != 3000 {
		t.Fatalf("max_tokens = %d, want 3000 for extraction", captured.MaxTokens)
	}
	if captured.Thinking.Type != "disabled" {
		t.Fatalf("thinking.type = %q, want disabled for structured extraction", captured.Thinking.Type)
	}
	joined := captured.Messages[0].Content + captured.Messages[1].Content
	if !strings.Contains(joined, "不得诊断") || !strings.Contains(joined, "<patient_input>") {
		t.Fatalf("prompt does not contain safety boundary and data delimiter: %q", joined)
	}
}

func TestOpenAICompatibleClientRetriesOneEmptyJSONResponse(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"visit_goal\":\"准备就诊\",\"facts\":[{\"category\":\"symptom\",\"source_quote\":\"三天前开始咳嗽\"}],\"missing_fields\":[],\"clarification_prompts\":[],\"search_queries\":[]}"}}]}`))
	}))
	defer server.Close()

	client, err := llm.NewOpenAICompatibleClient(server.URL, "key", "model", server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Extract(context.Background(), "三天前开始咳嗽")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if calls != 2 || len(result.Facts) != 1 || result.Facts[0].Content != "三天前开始咳嗽" {
		t.Fatalf("calls = %d, extraction = %#v", calls, result)
	}
}

func TestOpenAICompatibleClientRejectsInvalidModelJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"not json"}}]}`))
	}))
	defer server.Close()

	client, err := llm.NewOpenAICompatibleClient(server.URL, "llm-key", "test-model", server.Client(), nil)
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	if _, err := client.Extract(context.Background(), "三天前开始咳嗽"); err == nil {
		t.Fatal("Extract() error = nil, want invalid JSON error")
	}
}

func TestOpenAICompatibleClientGeneratesGroundedQuestions(t *testing.T) {
	var captured struct {
		MaxTokens int `json:"max_tokens"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"questions\":[{\"text\":\"咳嗽在一天中的什么时段最明显？\",\"source_url\":\"https://www.who.int/health-topics/cough\",\"reason\":\"帮助医生了解症状规律\",\"priority\":\"high\",\"category\":\"timeline\"}],\"action_items\":[{\"title\":\"记录咳嗽变化\",\"detail\":\"记录出现时段和持续时间\",\"reason\":\"帮助医生了解症状规律\",\"priority\":\"high\",\"category\":\"tracking\"}]}"}}]}`))
	}))
	defer server.Close()

	client, err := llm.NewOpenAICompatibleClient(server.URL, "llm-key", "test-model", server.Client(), nil)
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
	if len(result.Questions) != 1 || result.Questions[0].SourceURL == "" || result.Questions[0].Reason == "" || result.Questions[0].Priority != domain.PriorityHigh {
		t.Fatalf("GenerateQuestions() = %#v", result)
	}
	if len(result.ActionItems) != 1 || result.ActionItems[0].Category != "tracking" {
		t.Fatalf("GenerateQuestions() action items = %#v", result.ActionItems)
	}
	if captured.MaxTokens != 1000 {
		t.Fatalf("max_tokens = %d, want 1000 for question generation", captured.MaxTokens)
	}
}

func TestOpenAICompatibleClientRejectsUnsafeRiskAndIncompleteQuestions(t *testing.T) {
	unsafeOptionalExtractions := []string{
		`{"visit_goal":"准备就诊","facts":[{"category":"symptom","content":"心悸","source_quote":"心悸"}],"risk_signals":[{"priority":"urgent","title":"需要尽快评估","evidence":"胸痛","guidance":"立即就医","source_quote":"胸痛"}],"missing_fields":[],"clarification_questions":[],"search_queries":[]}`,
		`{"visit_goal":"准备就诊","facts":[{"category":"symptom","content":"心悸","source_quote":"心悸"}],"risk_signals":[{"priority":"critical","title":"风险","evidence":"心悸","guidance":"立即就医","source_quote":"心悸"}],"missing_fields":[],"clarification_questions":[],"search_queries":[]}`,
	}
	for _, content := range unsafeOptionalExtractions {
		client, closeServer := clientReturning(t, content)
		result, err := client.Extract(context.Background(), "最近反复心悸，需要准备门诊沟通信息。当前没有提供其他症状。")
		closeServer()
		if err != nil {
			t.Fatalf("Extract() rejected valid facts because of an unsafe optional risk: %v", err)
		}
		if len(result.RiskSignals) != 0 {
			t.Fatalf("unsafe risk signal survived filtering: %#v", result.RiskSignals)
		}
	}

	unsafeMissingField := `{"visit_goal":"准备就诊","facts":[{"category":"symptom","content":"心悸","source_quote":"心悸"}],"risk_signals":[],"missing_fields":["你确定是甲状腺疾病，需要自行停药"],"clarification_questions":[],"search_queries":[]}`
	client, closeServer := clientReturning(t, unsafeMissingField)
	result, err := client.Extract(context.Background(), "最近反复心悸，需要准备门诊沟通信息。当前没有提供其他症状。")
	closeServer()
	if err != nil {
		t.Fatalf("Extract() rejected valid facts because of an unsafe missing field: %v", err)
	}
	if len(result.MissingFieldItems) != 0 {
		t.Fatalf("unsafe missing field survived filtering: %#v", result.MissingFieldItems)
	}

	for _, content := range []string{
		`{"questions":[{"text":"何时开始？","priority":"high","category":"timeline"}]}`,
		`{"questions":[{"text":"何时开始？","reason":"了解病程","priority":"critical","category":"timeline"}]}`,
	} {
		client, closeServer := clientReturning(t, content)
		if _, err := client.GenerateQuestions(context.Background(), domain.QuestionInput{}); err == nil {
			closeServer()
			t.Fatalf("GenerateQuestions() accepted incomplete question: %s", content)
		}
		closeServer()
	}
}

func TestOpenAICompatibleClientDropsUngroundedFactInsteadOfFailing(t *testing.T) {
	content := `{"visit_goal":"准备说明胃部不适","facts":[{"category":"symptom","content":"饭后半小时胃痛","source_quote":"饭后半小时"},{"category":"symptom","content":"编造","source_quote":"模型编造的原文"}],"missing_fields":[],"clarification_questions":[],"search_queries":[]}`
	client, closeServer := clientReturning(t, content)
	defer closeServer()

	result, err := client.Extract(context.Background(), "吃完饭以后胃会隐隐地疼，饭后半小时左右开始。")
	if err != nil {
		t.Fatalf("Extract() failed instead of dropping the ungrounded fact: %v", err)
	}
	if len(result.Facts) != 1 || result.Facts[0].SourceQuote != "饭后半小时" {
		t.Fatalf("ungrounded fact was not dropped: %#v", result.Facts)
	}
}

func TestOpenAICompatibleClientIgnoresUnknownJSONFields(t *testing.T) {
	content := `{"visit_goal":"目标","facts":[{"category":"symptom","content":"咳嗽","source_quote":"咳嗽"}],"missing_fields":[],"clarification_questions":[],"search_queries":[],"model_extra_field":123,"vendor":{"nested":true}}`
	client, closeServer := clientReturning(t, content)
	defer closeServer()
	if _, err := client.Extract(context.Background(), "我已经咳嗽三天。"); err != nil {
		t.Fatalf("Extract() rejected unknown fields: %v", err)
	}
}

func TestExtractModelOutputFailureIsRetryable(t *testing.T) {
	client, closeServer := clientReturning(t, "not json")
	defer closeServer()
	_, err := client.Extract(context.Background(), "三天前开始咳嗽")
	if err == nil {
		t.Fatal("Extract() error = nil, want decode error")
	}
	var retryable interface{ Retryable() bool }
	if !errors.As(err, &retryable) || !retryable.Retryable() {
		t.Fatalf("model decode failure is not marked retryable: %v", err)
	}
}

func TestOpenAICompatibleClientDropsInvalidOptionalClinicalStructures(t *testing.T) {
	content := `{"visit_goal":"准备说明心悸","facts":[{"category":"symptom","content":"最近反复心悸","source_quote":"最近反复心悸"}],"symptom_profiles":[{"name":"心悸","source_quote":"模型编造的原文"}],"timeline":[{"time_label":"","event":"开始心悸","source_quote":"最近反复心悸"}],"risk_signals":[],"missing_fields":[],"clarification_questions":["心悸每次持续多久？"],"clarification_prompts":[{"text":"心悸每次持续多久？","reason":"补充信息","priority":"high","category":"unsupported"}],"search_queries":[]}`
	client, closeServer := clientReturning(t, content)
	defer closeServer()

	result, err := client.Extract(context.Background(), "最近反复心悸，需要准备门诊沟通信息。")
	if err != nil {
		t.Fatalf("Extract() rejected valid facts because optional structures were invalid: %v", err)
	}
	if len(result.Facts) != 1 || len(result.SymptomProfiles) != 0 || len(result.Timeline) != 0 || len(result.ClarificationPrompts) != 0 || len(result.ClarificationQuestions) != 0 {
		t.Fatalf("optional structures were not filtered: %#v", result)
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
		{name: "userinfo", endpoint: "https://secret@api.example/v1/chat/completions", key: "key", model: "model", client: http.DefaultClient},
		{name: "query", endpoint: "https://api.example/v1/chat/completions?token=secret", key: "key", model: "model", client: http.DefaultClient},
		{name: "fragment", endpoint: "https://api.example/v1/chat/completions#secret", key: "key", model: "model", client: http.DefaultClient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := llm.NewOpenAICompatibleClient(tt.endpoint, tt.key, tt.model, tt.client, nil); err == nil {
				t.Fatal("NewOpenAICompatibleClient() error = nil")
			}
		})
	}
}

func TestOpenAICompatibleClientDoesNotLogRawErrors(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	client, err := llm.NewOpenAICompatibleClient("http://127.0.0.1:1/v1/chat/completions", "key", "model", http.DefaultClient, logger)
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	_, _ = client.Extract(context.Background(), "这是一段足够长的脱敏健康情况描述，用于验证日志不会泄漏请求地址。")
	if strings.Contains(logs.String(), "127.0.0.1") || strings.Contains(logs.String(), "/v1/chat/completions") {
		t.Fatalf("logs exposed the request URL: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"error_type":"request_failed"`) {
		t.Fatalf("logs did not contain the sanitized error type: %s", logs.String())
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
			client, _ := llm.NewOpenAICompatibleClient(server.URL, "key", "model", server.Client(), nil)
			if _, err := client.Extract(context.Background(), "三天前开始咳嗽"); err == nil {
				t.Fatal("Extract() error = nil")
			}
		})
	}
}

func TestOpenAICompatibleClientRetriesOneTransientProviderFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"visit_goal\":\"准备就诊\",\"facts\":[{\"category\":\"symptom\",\"source_quote\":\"三天前开始咳嗽\"}],\"missing_fields\":[],\"clarification_prompts\":[],\"search_queries\":[]}"}}]}`))
	}))
	defer server.Close()

	client, err := llm.NewOpenAICompatibleClient(server.URL, "key", "model", server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Extract(context.Background(), "三天前开始咳嗽"); err != nil {
		t.Fatalf("Extract() after transient retry error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("provider calls = %d, want 2", calls)
	}
}

func TestOpenAICompatibleClientRetriesInternalServerError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"visit_goal\":\"准备就诊\",\"facts\":[{\"category\":\"symptom\",\"source_quote\":\"三天前开始咳嗽\"}],\"missing_fields\":[],\"clarification_prompts\":[],\"search_queries\":[]}"}}]}`))
	}))
	defer server.Close()
	client, _ := llm.NewOpenAICompatibleClient(server.URL, "key", "model", server.Client(), nil)
	if _, err := client.Extract(context.Background(), "三天前开始咳嗽"); err != nil {
		t.Fatalf("Extract() after 500 retry error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("provider calls = %d, want 2", calls)
	}
}

func TestOpenAICompatibleClientOpensCircuitAfterRepeatedTransientFailures(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := llm.NewOpenAICompatibleClient(server.URL, "key", "model", server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := client.Extract(context.Background(), "三天前开始咳嗽并且晚上加重，希望整理后就医"); err == nil {
			t.Fatal("Extract() error = nil")
		}
	}
	callsBeforeOpenRequest := calls
	_, err = client.Extract(context.Background(), "三天前开始咳嗽并且晚上加重，希望整理后就医")
	if err == nil || !strings.Contains(err.Error(), "circuit") {
		t.Fatalf("open-circuit error = %v", err)
	}
	if calls != callsBeforeOpenRequest {
		t.Fatalf("open circuit called provider: before=%d after=%d", callsBeforeOpenRequest, calls)
	}
}

func TestOpenAICompatibleClientDoesNotRetryPermanentProviderFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	client, _ := llm.NewOpenAICompatibleClient(server.URL, "key", "model", server.Client(), nil)
	if _, err := client.Extract(context.Background(), "三天前开始咳嗽"); err == nil {
		t.Fatal("Extract() error = nil")
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
}

func TestOpenAICompatibleClientClassifiesNetworkRetries(t *testing.T) {
	tests := []struct {
		name      string
		firstErr  error
		wantCalls int
		wantError bool
	}{
		{
			name:      "TLS certificate failure is permanent",
			firstErr:  &url.Error{Op: "Post", URL: "https://api.example/v1/chat/completions", Err: x509.UnknownAuthorityError{}},
			wantCalls: 1,
			wantError: true,
		},
		{
			name:      "DNS not found is permanent",
			firstErr:  &net.DNSError{Err: "no such host", Name: "missing.example", IsNotFound: true},
			wantCalls: 1,
			wantError: true,
		},
		{
			name:      "network timeout is transient",
			firstErr:  timeoutError{},
			wantCalls: 2,
		},
		{
			name:      "temporary DNS failure is transient",
			firstErr:  &net.DNSError{Err: "temporary failure", Name: "api.example", IsTemporary: true},
			wantCalls: 2,
		},
		{
			name:      "connection reset is transient",
			firstErr:  &net.OpError{Op: "write", Net: "tcp", Err: syscall.ECONNRESET},
			wantCalls: 2,
		},
		{
			name:      "unexpected EOF is transient",
			firstErr:  io.ErrUnexpectedEOF,
			wantCalls: 2,
		},
		{
			name:      "EOF is transient",
			firstErr:  io.EOF,
			wantCalls: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return nil, tt.firstErr
				}
				return validExtractionResponse(), nil
			})}
			model, err := llm.NewOpenAICompatibleClient("https://api.example/v1/chat/completions", "key", "model", client, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Extract(context.Background(), "三天前开始咳嗽，目前没有服用任何药物。")
			if (err != nil) != tt.wantError {
				t.Fatalf("Extract() error = %v, wantError %v", err, tt.wantError)
			}
			if calls != tt.wantCalls {
				t.Fatalf("provider calls = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}

func TestOpenAICompatibleClientRejectsInvalidExtractionShapes(t *testing.T) {
	responses := []string{
		`{"visit_goal":"目标","facts":[],"missing_fields":[],"clarification_questions":[],"search_queries":[]}`,
		`{"visit_goal":"目标","facts":[{"category":"symptom","content":"咳嗽","source_quote":""}],"missing_fields":[],"clarification_questions":[],"search_queries":[]}`,
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

func TestOpenAICompatibleClientTruncatesOverfullStructures(t *testing.T) {
	content := `{"visit_goal":"目标","facts":[{"category":"symptom","content":"咳嗽","source_quote":"咳嗽"}],"missing_fields":[],"clarification_questions":["1","2","3","4"],"search_queries":["a","b","c"]}`
	client, closeServer := clientReturning(t, content)
	defer closeServer()
	result, err := client.Extract(context.Background(), "我已经咳嗽三天，目前没有服用药物。")
	if err != nil {
		t.Fatalf("Extract() rejected overfull structures instead of truncating: %v", err)
	}
	if len(result.ClarificationQuestions) > 3 {
		t.Fatalf("clarification questions were not truncated: %#v", result.ClarificationQuestions)
	}
	if len(result.SearchQueries) > 2 {
		t.Fatalf("search queries were not truncated: %#v", result.SearchQueries)
	}
}

func TestOpenAICompatibleClientAcceptsFieldSpecificClarificationCategories(t *testing.T) {
	for _, category := range []string{"duration", "frequency", "severity", "pattern", "trigger", "measurement", "associated_symptom", "medication_history", "allergy", "missing_detail", "symptom_detail"} {
		t.Run(category, func(t *testing.T) {
			content := `{"visit_goal":"准备就诊","facts":[{"category":"symptom","content":"心悸","source_quote":"心悸"}],"missing_fields":["补充细节"],"clarification_questions":["心悸还需要补充哪些细节？"],"clarification_prompts":[{"text":"心悸还需要补充哪些细节？","reason":"完善症状画像","priority":"high","category":"` + category + `"}],"search_queries":[]}`
			client, closeServer := clientReturning(t, content)
			defer closeServer()
			if _, err := client.Extract(context.Background(), "最近反复心悸，需要准备门诊沟通信息。"); err != nil {
				t.Fatalf("Extract() rejected category %q: %v", category, err)
			}
		})
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
	client, err := llm.NewOpenAICompatibleClient(server.URL, "key", "model", server.Client(), nil)
	if err != nil {
		server.Close()
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	return client, server.Close
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "network timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func validExtractionResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"choices":[{"message":{"content":"{\"visit_goal\":\"准备就诊\",\"facts\":[{\"category\":\"symptom\",\"source_quote\":\"三天前开始咳嗽\"}],\"missing_fields\":[],\"clarification_prompts\":[],\"search_queries\":[]}"}}]}`,
		)),
	}
}
