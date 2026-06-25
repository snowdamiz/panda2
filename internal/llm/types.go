package llm

import (
	"context"
	"encoding/json"
	"errors"
)

var ErrNotConfigured = errors.New("openrouter api key is not configured")

type Client interface {
	Chat(ctx context.Context, request ChatRequest) (ChatResponse, error)
}

type StreamingClient interface {
	StreamChat(ctx context.Context, request ChatRequest, onDelta ChatStreamHandler) (ChatResponse, error)
}

type ChatStreamHandler func(ChatStreamDelta) error

type ChatStreamDelta struct {
	Content     string
	HasToolCall bool
}

type Embedder interface {
	Embed(ctx context.Context, request EmbeddingRequest) (EmbeddingResponse, error)
}

type ModelLister interface {
	ListModels(ctx context.Context) ([]Model, error)
	ValidateModel(ctx context.Context, slug string) (bool, error)
}

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ChatRequest struct {
	Model          string
	Messages       []Message
	Tools          []Tool
	ResponseFormat *ResponseFormat
	Temperature    float64
	MaxTokens      int
}

type ResponseFormat struct {
	Type       string                `json:"type"`
	JSONSchema *ResponseFormatSchema `json:"json_schema,omitempty"`
}

type ResponseFormatSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict,omitempty"`
	Schema json.RawMessage `json:"schema"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatResponse struct {
	ID        string
	Model     string
	Content   string
	ToolCalls []ToolCall
	Usage     Usage
}

type EmbeddingRequest struct {
	Model      string
	Input      []string
	Dimensions int
	InputType  string
}

type Embedding struct {
	Index  int
	Vector []float64
}

type EmbeddingResponse struct {
	ID         string
	Model      string
	Embeddings []Embedding
	Usage      Usage
}

type Model struct {
	ID            string
	CanonicalSlug string
	Name          string
	ContextLength int
}

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}
