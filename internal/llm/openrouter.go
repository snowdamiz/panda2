package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type OpenRouterClient struct {
	apiKey                 string
	baseURL                string
	appURL                 string
	appTitle               string
	providerOrder          []string
	allowProviderFallbacks bool
	client                 *http.Client
	maxRetries             int
	retryDelay             time.Duration
	circuitBreaker         circuitBreaker
}

type OpenRouterConfig struct {
	APIKey                         string
	BaseURL                        string
	AppURL                         string
	AppTitle                       string
	ProviderOrder                  []string
	AllowProviderFallbacks         bool
	Timeout                        time.Duration
	MaxRetries                     int
	RetryDelay                     time.Duration
	CircuitBreakerFailureThreshold int
	CircuitBreakerCooldown         time.Duration
}

var ErrCircuitOpen = errors.New("openrouter circuit breaker is open")

func NewOpenRouterClient(cfg OpenRouterConfig) *OpenRouterClient {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	retryDelay := cfg.RetryDelay
	if retryDelay == 0 {
		retryDelay = 250 * time.Millisecond
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	circuitCooldown := cfg.CircuitBreakerCooldown
	if circuitCooldown == 0 {
		circuitCooldown = 30 * time.Second
	}
	failureThreshold := cfg.CircuitBreakerFailureThreshold
	if failureThreshold == 0 {
		failureThreshold = 5
	}
	return &OpenRouterClient{
		apiKey:                 strings.TrimSpace(cfg.APIKey),
		baseURL:                baseURL,
		appURL:                 strings.TrimSpace(cfg.AppURL),
		appTitle:               strings.TrimSpace(cfg.AppTitle),
		providerOrder:          normalizeStringList(cfg.ProviderOrder),
		allowProviderFallbacks: cfg.AllowProviderFallbacks,
		client:                 &http.Client{Timeout: timeout},
		maxRetries:             cfg.MaxRetries,
		retryDelay:             retryDelay,
		circuitBreaker: circuitBreaker{
			failureThreshold: failureThreshold,
			cooldown:         circuitCooldown,
		},
	}
}

func (c *OpenRouterClient) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	if c.apiKey == "" {
		return ChatResponse{}, ErrNotConfigured
	}
	if len(request.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("chat request requires at least one message")
	}

	payload := c.chatCompletionPayload(request)
	body, err := json.Marshal(payload)
	if err != nil {
		return ChatResponse{}, err
	}
	slog.Info("openrouter chat request built",
		slog.String("model", request.Model),
		slog.Int("message_count", len(request.Messages)),
		slog.Int("tool_count", len(request.Tools)),
		slog.Bool("has_response_format", request.ResponseFormat != nil),
		slog.Any("provider_order", payload.ProviderOrder()),
		slog.Bool("provider_allow_fallbacks", payload.ProviderAllowFallbacks()),
		slog.Bool("provider_require_parameters", payload.Provider != nil && payload.Provider.RequireParameters),
		slog.Bool("structured_outputs", payload.StructuredOutputs != nil && *payload.StructuredOutputs),
		slog.Bool("tool_choice_present", request.ToolChoice != nil),
	)

	data, err := c.post(ctx, "/chat/completions", body)
	if err != nil {
		return ChatResponse{}, err
	}

	var decoded chatCompletionResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return ChatResponse{}, err
	}
	if len(decoded.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("openrouter response did not include choices")
	}
	return ChatResponse{
		ID:        decoded.ID,
		Model:     firstNonEmpty(decoded.Model, request.Model),
		Content:   decoded.Choices[0].Message.Content,
		ToolCalls: decoded.Choices[0].Message.ToolCalls,
		Usage: Usage{
			PromptTokens:     decoded.Usage.PromptTokens,
			CompletionTokens: decoded.Usage.CompletionTokens,
			TotalTokens:      decoded.Usage.TotalTokens,
		},
	}, nil
}

func (c *OpenRouterClient) StreamChat(ctx context.Context, request ChatRequest, onDelta ChatStreamHandler) (ChatResponse, error) {
	if c.apiKey == "" {
		return ChatResponse{}, ErrNotConfigured
	}
	if len(request.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("chat request requires at least one message")
	}

	payload := c.chatCompletionPayload(request)
	payload.Stream = true
	body, err := json.Marshal(payload)
	if err != nil {
		return ChatResponse{}, err
	}
	slog.Info("openrouter streaming chat request built",
		slog.String("model", request.Model),
		slog.Int("message_count", len(request.Messages)),
		slog.Int("tool_count", len(request.Tools)),
		slog.Bool("has_response_format", request.ResponseFormat != nil),
		slog.Any("provider_order", payload.ProviderOrder()),
		slog.Bool("provider_allow_fallbacks", payload.ProviderAllowFallbacks()),
		slog.Bool("provider_require_parameters", payload.Provider != nil && payload.Provider.RequireParameters),
		slog.Bool("structured_outputs", payload.StructuredOutputs != nil && *payload.StructuredOutputs),
		slog.Bool("tool_choice_present", request.ToolChoice != nil),
	)

	builder := chatStreamBuilder{fallbackModel: request.Model}
	err = c.postStream(ctx, "/chat/completions", body, func(data []byte) error {
		delta, err := builder.Append(data)
		if err != nil {
			return err
		}
		if onDelta == nil || (delta.Content == "" && !delta.HasToolCall) {
			return nil
		}
		return onDelta(delta)
	})
	if err != nil {
		return ChatResponse{}, err
	}
	return builder.Response(), nil
}

func (c *OpenRouterClient) chatCompletionPayload(request ChatRequest) chatCompletionRequest {
	payload := chatCompletionRequest{
		Model:          request.Model,
		Messages:       request.Messages,
		Tools:          request.Tools,
		ToolChoice:     request.ToolChoice,
		ResponseFormat: request.ResponseFormat,
		Temperature:    request.Temperature,
		MaxTokens:      request.MaxTokens,
	}
	payload.Provider = c.providerPreferences(request)
	if request.ResponseFormat != nil && request.ResponseFormat.Type == "json_schema" {
		structuredOutputs := true
		payload.StructuredOutputs = &structuredOutputs
	}
	return payload
}

func (c *OpenRouterClient) providerPreferences(request ChatRequest) *providerPreferences {
	requireParameters := len(request.Tools) > 0 || request.ToolChoice != nil || request.ResponseFormat != nil
	order := normalizeStringList(c.providerOrder)
	if len(order) == 0 && !requireParameters {
		return nil
	}
	allowFallbacks := c.allowProviderFallbacks
	return &providerPreferences{
		Order:             order,
		RequireParameters: requireParameters,
		AllowFallbacks:    &allowFallbacks,
	}
}

func (c *OpenRouterClient) Embed(ctx context.Context, request EmbeddingRequest) (EmbeddingResponse, error) {
	if c.apiKey == "" {
		return EmbeddingResponse{}, ErrNotConfigured
	}
	if request.Model == "" {
		return EmbeddingResponse{}, fmt.Errorf("embedding request requires a model")
	}
	if len(request.Input) == 0 {
		return EmbeddingResponse{}, fmt.Errorf("embedding request requires input")
	}

	payload := embeddingRequest{
		Model:      request.Model,
		Input:      request.Input,
		Dimensions: request.Dimensions,
		InputType:  request.InputType,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return EmbeddingResponse{}, err
	}

	data, err := c.post(ctx, "/embeddings", body)
	if err != nil {
		return EmbeddingResponse{}, err
	}

	var decoded embeddingResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return EmbeddingResponse{}, err
	}
	embeddings := make([]Embedding, 0, len(decoded.Data))
	for _, item := range decoded.Data {
		embeddings = append(embeddings, Embedding{Index: item.Index, Vector: item.Embedding})
	}
	return EmbeddingResponse{
		ID:         decoded.ID,
		Model:      firstNonEmpty(decoded.Model, request.Model),
		Embeddings: embeddings,
		Usage: Usage{
			PromptTokens: decoded.Usage.PromptTokens,
			TotalTokens:  decoded.Usage.TotalTokens,
		},
	}, nil
}

func (c *OpenRouterClient) ListModels(ctx context.Context) ([]Model, error) {
	data, err := c.get(ctx, "/models")
	if err != nil {
		return nil, err
	}
	var decoded modelsResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(decoded.Data))
	for _, item := range decoded.Data {
		models = append(models, Model{
			ID:            item.ID,
			CanonicalSlug: item.CanonicalSlug,
			Name:          item.Name,
			ContextLength: item.ContextLength,
		})
	}
	return models, nil
}

func (c *OpenRouterClient) ValidateModel(ctx context.Context, slug string) (bool, error) {
	if strings.TrimSpace(slug) == "openrouter/auto" {
		return true, nil
	}
	models, err := c.ListModels(ctx)
	if err != nil {
		return false, err
	}
	for _, model := range models {
		if model.ID == slug || model.CanonicalSlug == slug {
			return true, nil
		}
	}
	return false, nil
}

func (c *OpenRouterClient) post(ctx context.Context, path string, body []byte) ([]byte, error) {
	if err := c.circuitBreaker.allow(); err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Content-Type", "application/json")
		if c.appURL != "" {
			req.Header.Set("HTTP-Referer", c.appURL)
		}
		if c.appTitle != "" {
			req.Header.Set("X-OpenRouter-Title", c.appTitle)
		}

		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil || attempt == c.maxRetries {
				c.circuitBreaker.recordFailure()
				return nil, err
			}
			if waitErr := c.waitBeforeRetry(ctx); waitErr != nil {
				return nil, waitErr
			}
			continue
		}

		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			c.circuitBreaker.recordSuccess()
			return data, nil
		}

		lastErr = parseOpenRouterError(resp.StatusCode, data)
		if !retryableStatus(resp.StatusCode) || attempt == c.maxRetries {
			if retryableStatus(resp.StatusCode) {
				c.circuitBreaker.recordFailure()
			}
			return nil, lastErr
		}
		if waitErr := c.waitBeforeRetry(ctx); waitErr != nil {
			return nil, waitErr
		}
	}
	return nil, lastErr
}

func (c *OpenRouterClient) postStream(ctx context.Context, path string, body []byte, handleEvent func([]byte) error) error {
	if err := c.circuitBreaker.allow(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.appURL != "" {
		req.Header.Set("HTTP-Referer", c.appURL)
	}
	if c.appTitle != "" {
		req.Header.Set("X-OpenRouter-Title", c.appTitle)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		c.circuitBreaker.recordFailure()
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if readErr != nil {
			return readErr
		}
		streamErr := parseOpenRouterError(resp.StatusCode, data)
		if retryableStatus(resp.StatusCode) {
			c.circuitBreaker.recordFailure()
		}
		return streamErr
	}

	if err := readServerSentEvents(resp.Body, handleEvent); err != nil {
		c.circuitBreaker.recordFailure()
		return err
	}
	c.circuitBreaker.recordSuccess()
	return nil
}

func readServerSentEvents(reader io.Reader, handleEvent func([]byte) error) error {
	buffered := bufio.NewReader(reader)
	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = nil
		if data == "" || data == "[DONE]" {
			return nil
		}
		return handleEvent([]byte(data))
	}

	for {
		line, err := buffered.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if flushErr := flush(); flushErr != nil {
					return flushErr
				}
			case strings.HasPrefix(line, ":"):
			case strings.HasPrefix(line, "data:"):
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return flush()
			}
			return err
		}
	}
}

func (c *OpenRouterClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if c.appURL != "" {
		req.Header.Set("HTTP-Referer", c.appURL)
	}
	if c.appTitle != "" {
		req.Header.Set("X-OpenRouter-Title", c.appTitle)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseOpenRouterError(resp.StatusCode, data)
	}
	return data, nil
}

func (c *OpenRouterClient) waitBeforeRetry(ctx context.Context) error {
	if c.retryDelay <= 0 {
		return nil
	}
	timer := time.NewTimer(c.retryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryableStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func IsRetryable(err error) bool {
	var openRouterErr Error
	if errors.As(err, &openRouterErr) {
		return retryableStatus(openRouterErr.StatusCode)
	}
	return false
}

type circuitBreaker struct {
	mu               sync.Mutex
	failureThreshold int
	cooldown         time.Duration
	failures         int
	openedUntil      time.Time
}

func (b *circuitBreaker) allow() error {
	if b.failureThreshold <= 0 || b.cooldown <= 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	if b.openedUntil.IsZero() || !now.Before(b.openedUntil) {
		return nil
	}
	return CircuitOpenError{RetryAfter: b.openedUntil.Sub(now).Round(time.Millisecond)}
}

func (b *circuitBreaker) recordSuccess() {
	if b.failureThreshold <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.openedUntil = time.Time{}
}

func (b *circuitBreaker) recordFailure() {
	if b.failureThreshold <= 0 || b.cooldown <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.failures >= b.failureThreshold {
		b.openedUntil = time.Now().Add(b.cooldown)
	}
}

type chatCompletionRequest struct {
	Model             string               `json:"model"`
	Messages          []Message            `json:"messages"`
	Tools             []Tool               `json:"tools,omitempty"`
	ToolChoice        *ToolChoice          `json:"tool_choice,omitempty"`
	ResponseFormat    *ResponseFormat      `json:"response_format,omitempty"`
	StructuredOutputs *bool                `json:"structured_outputs,omitempty"`
	Provider          *providerPreferences `json:"provider,omitempty"`
	Temperature       float64              `json:"temperature,omitempty"`
	MaxTokens         int                  `json:"max_tokens,omitempty"`
	Stream            bool                 `json:"stream,omitempty"`
}

type providerPreferences struct {
	Order             []string `json:"order,omitempty"`
	RequireParameters bool     `json:"require_parameters,omitempty"`
	AllowFallbacks    *bool    `json:"allow_fallbacks,omitempty"`
}

func (p *chatCompletionRequest) ProviderOrder() []string {
	if p.Provider == nil {
		return nil
	}
	return append([]string(nil), p.Provider.Order...)
}

func (p *chatCompletionRequest) ProviderAllowFallbacks() bool {
	return p.Provider != nil && p.Provider.AllowFallbacks != nil && *p.Provider.AllowFallbacks
}

type chatCompletionResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type chatCompletionStreamChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content   string                `json:"content"`
			ToolCalls []streamToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type streamToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatStreamBuilder struct {
	id            string
	model         string
	fallbackModel string
	content       strings.Builder
	toolCalls     map[int]*streamToolCallBuilder
	usage         Usage
}

type streamToolCallBuilder struct {
	id        string
	callType  string
	name      strings.Builder
	arguments strings.Builder
}

func (b *chatStreamBuilder) Append(data []byte) (ChatStreamDelta, error) {
	var chunk chatCompletionStreamChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return ChatStreamDelta{}, err
	}
	if chunk.ID != "" {
		b.id = chunk.ID
	}
	if chunk.Model != "" {
		b.model = chunk.Model
	}
	if chunk.Usage != nil {
		b.usage = Usage{
			PromptTokens:     chunk.Usage.PromptTokens,
			CompletionTokens: chunk.Usage.CompletionTokens,
			TotalTokens:      chunk.Usage.TotalTokens,
		}
	}

	var delta ChatStreamDelta
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			b.content.WriteString(choice.Delta.Content)
			delta.Content += choice.Delta.Content
		}
		if len(choice.Delta.ToolCalls) == 0 {
			continue
		}
		delta.HasToolCall = true
		if b.toolCalls == nil {
			b.toolCalls = map[int]*streamToolCallBuilder{}
		}
		for _, streamed := range choice.Delta.ToolCalls {
			call := b.toolCalls[streamed.Index]
			if call == nil {
				call = &streamToolCallBuilder{}
				b.toolCalls[streamed.Index] = call
			}
			if streamed.ID != "" {
				call.id = streamed.ID
			}
			if streamed.Type != "" {
				call.callType = streamed.Type
			}
			if streamed.Function.Name != "" {
				call.name.WriteString(streamed.Function.Name)
			}
			if streamed.Function.Arguments != "" {
				call.arguments.WriteString(streamed.Function.Arguments)
			}
		}
	}
	return delta, nil
}

func (b *chatStreamBuilder) Response() ChatResponse {
	return ChatResponse{
		ID:        b.id,
		Model:     firstNonEmpty(b.model, b.fallbackModel),
		Content:   b.content.String(),
		ToolCalls: b.completeToolCalls(),
		Usage:     b.usage,
	}
}

func (b *chatStreamBuilder) completeToolCalls() []ToolCall {
	if len(b.toolCalls) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(b.toolCalls))
	for index := range b.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	result := make([]ToolCall, 0, len(indexes))
	for _, index := range indexes {
		call := b.toolCalls[index]
		result = append(result, ToolCall{
			ID:   call.id,
			Type: firstNonEmpty(call.callType, "function"),
			Function: ToolCallFunction{
				Name:      call.name.String(),
				Arguments: call.arguments.String(),
			},
		})
	}
	return result
}

type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
	InputType  string   `json:"input_type,omitempty"`
}

type embeddingResponse struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Data  []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

type modelsResponse struct {
	Data []struct {
		ID            string `json:"id"`
		CanonicalSlug string `json:"canonical_slug"`
		Name          string `json:"name"`
		ContextLength int    `json:"context_length"`
	} `json:"data"`
}

type Error struct {
	StatusCode int
	Code       string
	Message    string
}

type CircuitOpenError struct {
	RetryAfter time.Duration
}

func (e CircuitOpenError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%s; retry after %s", ErrCircuitOpen.Error(), e.RetryAfter)
	}
	return ErrCircuitOpen.Error()
}

func (e CircuitOpenError) Is(target error) bool {
	return target == ErrCircuitOpen
}

func (e Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("openrouter error %d (%s): %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("openrouter error %d: %s", e.StatusCode, e.Message)
}

func parseOpenRouterError(statusCode int, data []byte) error {
	var decoded struct {
		Error struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &decoded); err == nil && decoded.Error.Message != "" {
		return Error{
			StatusCode: statusCode,
			Code:       fmt.Sprint(decoded.Error.Code),
			Message:    decoded.Error.Message,
		}
	}
	message := strings.TrimSpace(string(data))
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return Error{StatusCode: statusCode, Message: message}
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func normalizeStringList(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}
