package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenRouterChatSendsExpectedRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected auth header %q", got)
		}
		if got := r.Header.Get("HTTP-Referer"); got != "https://example.test" {
			t.Fatalf("unexpected referer %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var rawPayload map[string]any
		if err := json.Unmarshal(body, &rawPayload); err != nil {
			t.Fatalf("decode raw request: %v", err)
		}
		if _, ok := rawPayload["parallel_tool_calls"]; ok {
			t.Fatalf("tool requests should not send unsupported parallel_tool_calls: %s", string(body))
		}
		if _, ok := rawPayload["tool_choice"]; ok {
			t.Fatalf("tool requests should not force tool_choice: %s", string(body))
		}
		var payload chatCompletionRequest
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.Model != "openrouter/auto" {
			t.Fatalf("unexpected model %q", payload.Model)
		}
		if len(payload.Tools) != 1 || payload.Tools[0].Function.Name != "fixture_tool" {
			t.Fatalf("unexpected tools payload: %+v", payload.Tools)
		}
		if payload.Provider == nil || !payload.Provider.RequireParameters || payload.Provider.AllowFallbacks == nil || *payload.Provider.AllowFallbacks {
			t.Fatalf("tool requests should require provider parameter support and disable provider fallback: %+v", payload.Provider)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "gen-1",
			"model": "provider/model",
			"choices": []map[string]any{{
				"message": map[string]string{"role": "assistant", "content": "hello from fixture"},
			}},
			"usage": map[string]int{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7},
		})
	}))
	defer server.Close()

	client := NewOpenRouterClient(OpenRouterConfig{
		APIKey:   "test-key",
		BaseURL:  server.URL,
		AppURL:   "https://example.test",
		AppTitle: "Panda Tests",
	})
	response, err := client.Chat(context.Background(), ChatRequest{
		Model:    "openrouter/auto",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Tools: []Tool{{
			Type: "function",
			Function: ToolFunction{
				Name:        "fixture_tool",
				Description: "fixture",
				Parameters:  json.RawMessage(`{"type":"object"}`),
			},
		}},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if response.Content != "hello from fixture" {
		t.Fatalf("unexpected content %q", response.Content)
	}
	if response.Usage.TotalTokens != 7 {
		t.Fatalf("unexpected total tokens %d", response.Usage.TotalTokens)
	}
}

func TestOpenRouterChatSendsConfiguredProviderRouting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.Provider == nil {
			t.Fatal("expected provider routing")
		}
		if len(payload.Provider.Order) != 1 || payload.Provider.Order[0] != "cerebras" {
			t.Fatalf("unexpected provider order: %+v", payload.Provider)
		}
		if payload.Provider.RequireParameters {
			t.Fatalf("plain chat request should not require parameters: %+v", payload.Provider)
		}
		if payload.Provider.AllowFallbacks == nil || *payload.Provider.AllowFallbacks {
			t.Fatalf("expected provider fallbacks disabled: %+v", payload.Provider)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "gen-provider",
			"model": "openai/gpt-oss-120b",
			"choices": []map[string]any{{
				"message": map[string]string{"role": "assistant", "content": "routed"},
			}},
		})
	}))
	defer server.Close()

	client := NewOpenRouterClient(OpenRouterConfig{
		APIKey:        "key",
		BaseURL:       server.URL,
		ProviderOrder: []string{"cerebras", "cerebras"},
	})
	response, err := client.Chat(context.Background(), ChatRequest{
		Model:    "openai/gpt-oss-120b",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if response.Content != "routed" {
		t.Fatalf("unexpected content %q", response.Content)
	}
}

func TestOpenRouterChatParsesToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "gen-tools",
			"model": "provider/model",
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "",
					"tool_calls": []map[string]any{{
						"id":   "call-1",
						"type": "function",
						"function": map[string]string{
							"name":      "search_knowledge",
							"arguments": `{"query":"deploys","limit":"1"}`,
						},
					}, {
						"id":   "call-2",
						"type": "function",
						"function": map[string]string{
							"name":      "read_config",
							"arguments": `{}`,
						},
					}},
				},
			}},
		})
	}))
	defer server.Close()

	client := NewOpenRouterClient(OpenRouterConfig{APIKey: "key", BaseURL: server.URL})
	response, err := client.Chat(context.Background(), ChatRequest{Model: "openrouter/auto", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if len(response.ToolCalls) != 2 || response.ToolCalls[0].Function.Name != "search_knowledge" || response.ToolCalls[1].Function.Name != "read_config" {
		t.Fatalf("unexpected tool calls: %+v", response.ToolCalls)
	}
}

func TestOpenRouterChatSendsResponseFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.ResponseFormat == nil || payload.ResponseFormat.Type != "json_object" {
			t.Fatalf("expected JSON response format, got %+v", payload.ResponseFormat)
		}
		if payload.Provider == nil || !payload.Provider.RequireParameters || payload.Provider.AllowFallbacks == nil || *payload.Provider.AllowFallbacks {
			t.Fatalf("structured response requests should require provider parameter support and disable provider fallback: %+v", payload.Provider)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "gen-json",
			"model": "provider/model",
			"choices": []map[string]any{{
				"message": map[string]string{"role": "assistant", "content": `{"ok":true}`},
			}},
		})
	}))
	defer server.Close()

	client := NewOpenRouterClient(OpenRouterConfig{APIKey: "key", BaseURL: server.URL})
	response, err := client.Chat(context.Background(), ChatRequest{
		Model:          "openrouter/auto",
		Messages:       []Message{{Role: "user", Content: "return JSON"}},
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if response.Content != `{"ok":true}` {
		t.Fatalf("unexpected content %q", response.Content)
	}
}

func TestOpenRouterChatSendsStructuredResponseFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.ResponseFormat == nil || payload.ResponseFormat.Type != "json_schema" {
			t.Fatalf("expected schema response format, got %+v", payload.ResponseFormat)
		}
		if payload.ResponseFormat.JSONSchema == nil || payload.ResponseFormat.JSONSchema.Name != "decision" || !payload.ResponseFormat.JSONSchema.Strict {
			t.Fatalf("expected strict JSON schema, got %+v", payload.ResponseFormat.JSONSchema)
		}
		if payload.StructuredOutputs == nil || !*payload.StructuredOutputs {
			t.Fatalf("expected structured_outputs flag, got %+v", payload.StructuredOutputs)
		}
		if payload.Provider == nil || !payload.Provider.RequireParameters || payload.Provider.AllowFallbacks == nil || *payload.Provider.AllowFallbacks {
			t.Fatalf("structured response requests should require provider parameter support and disable provider fallback: %+v", payload.Provider)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "gen-schema",
			"model": "provider/model",
			"choices": []map[string]any{{
				"message": map[string]string{"role": "assistant", "content": `{"respond":true,"prompt":"play music"}`},
			}},
		})
	}))
	defer server.Close()

	client := NewOpenRouterClient(OpenRouterConfig{APIKey: "key", BaseURL: server.URL})
	response, err := client.Chat(context.Background(), ChatRequest{
		Model:    "openrouter/auto",
		Messages: []Message{{Role: "user", Content: "return JSON"}},
		ResponseFormat: &ResponseFormat{
			Type: "json_schema",
			JSONSchema: &ResponseFormatSchema{
				Name:   "decision",
				Strict: true,
				Schema: json.RawMessage(`{"type":"object","properties":{"respond":{"type":"boolean"},"prompt":{"type":"string"}},"required":["respond","prompt"],"additionalProperties":false}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if response.Content != `{"respond":true,"prompt":"play music"}` {
		t.Fatalf("unexpected content %q", response.Content)
	}
}

func TestOpenRouterChatWithoutKey(t *testing.T) {
	client := NewOpenRouterClient(OpenRouterConfig{BaseURL: "http://127.0.0.1"})
	_, err := client.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestOpenRouterErrorParsesMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "rate_limit", "message": "slow down"},
		})
	}))
	defer server.Close()

	client := NewOpenRouterClient(OpenRouterConfig{APIKey: "key", BaseURL: server.URL})
	_, err := client.Chat(context.Background(), ChatRequest{Model: "openrouter/auto", Messages: []Message{{Role: "user", Content: "hi"}}})
	var openRouterErr Error
	if !errors.As(err, &openRouterErr) {
		t.Fatalf("expected OpenRouter error, got %T %v", err, err)
	}
	if openRouterErr.StatusCode != http.StatusTooManyRequests || openRouterErr.Code != "rate_limit" {
		t.Fatalf("unexpected parsed error: %+v", openRouterErr)
	}
}

func TestOpenRouterEmbedSendsExpectedRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected auth header %q", got)
		}
		var payload embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.Model != "openai/text-embedding-3-small" || len(payload.Input) != 2 {
			t.Fatalf("unexpected embedding payload: %+v", payload)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "emb-1",
			"model": "openai/text-embedding-3-small",
			"data": []map[string]any{
				{"index": 0, "embedding": []float64{0.1, 0.2}},
				{"index": 1, "embedding": []float64{0.3, 0.4}},
			},
			"usage": map[string]int{"prompt_tokens": 8, "total_tokens": 8},
		})
	}))
	defer server.Close()

	client := NewOpenRouterClient(OpenRouterConfig{APIKey: "test-key", BaseURL: server.URL})
	response, err := client.Embed(context.Background(), EmbeddingRequest{
		Model: "openai/text-embedding-3-small",
		Input: []string{"alpha", "beta"},
	})
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if len(response.Embeddings) != 2 || response.Embeddings[1].Vector[0] != 0.3 {
		t.Fatalf("unexpected embeddings: %+v", response.Embeddings)
	}
	if response.Usage.TotalTokens != 8 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
}

func TestOpenRouterChatRetriesTransientErrors(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": "unavailable", "message": "try later"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "gen-2",
			"model": "provider/model",
			"choices": []map[string]any{{
				"message": map[string]string{"role": "assistant", "content": "recovered"},
			}},
		})
	}))
	defer server.Close()

	client := NewOpenRouterClient(OpenRouterConfig{
		APIKey:     "key",
		BaseURL:    server.URL,
		MaxRetries: 1,
		RetryDelay: time.Millisecond,
	})
	response, err := client.Chat(context.Background(), ChatRequest{Model: "openrouter/auto", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if response.Content != "recovered" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected two attempts, got %d", attempts.Load())
	}
}

func TestOpenRouterCircuitBreakerOpensAndRecovers(t *testing.T) {
	var attempts atomic.Int32
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": "unavailable", "message": "try later"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "gen-3",
			"model": "provider/model",
			"choices": []map[string]any{{
				"message": map[string]string{"role": "assistant", "content": "recovered"},
			}},
		})
	}))
	defer server.Close()

	client := NewOpenRouterClient(OpenRouterConfig{
		APIKey:                         "key",
		BaseURL:                        server.URL,
		MaxRetries:                     0,
		RetryDelay:                     time.Millisecond,
		CircuitBreakerFailureThreshold: 2,
		CircuitBreakerCooldown:         20 * time.Millisecond,
	})
	request := ChatRequest{Model: "openrouter/auto", Messages: []Message{{Role: "user", Content: "hi"}}}
	for i := 0; i < 2; i++ {
		if _, err := client.Chat(context.Background(), request); err == nil {
			t.Fatal("expected transient error")
		}
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected two upstream attempts before open circuit, got %d", attempts.Load())
	}

	_, err := client.Chat(context.Background(), request)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected circuit-open error, got %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("circuit-open call should not reach upstream, got %d attempts", attempts.Load())
	}

	healthy.Store(true)
	time.Sleep(25 * time.Millisecond)
	response, err := client.Chat(context.Background(), request)
	if err != nil {
		t.Fatalf("expected circuit to recover: %v", err)
	}
	if response.Content != "recovered" {
		t.Fatalf("unexpected recovery response: %+v", response)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected one recovery attempt, got %d", attempts.Load())
	}
}

func TestOpenRouterListAndValidateModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"id":             "provider/model-a",
				"canonical_slug": "provider/model-a",
				"name":           "Model A",
				"context_length": 8192,
			}},
		})
	}))
	defer server.Close()

	client := NewOpenRouterClient(OpenRouterConfig{BaseURL: server.URL})
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels returned error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "provider/model-a" || models[0].ContextLength != 8192 {
		t.Fatalf("unexpected models: %+v", models)
	}
	ok, err := client.ValidateModel(context.Background(), "provider/model-a")
	if err != nil || !ok {
		t.Fatalf("expected model to validate, ok=%v err=%v", ok, err)
	}
	ok, err = client.ValidateModel(context.Background(), "provider/missing")
	if err != nil || ok {
		t.Fatalf("expected missing model to fail validation, ok=%v err=%v", ok, err)
	}
	ok, err = client.ValidateModel(context.Background(), "openrouter/auto")
	if err != nil || !ok {
		t.Fatalf("expected openrouter/auto to validate, ok=%v err=%v", ok, err)
	}
}
