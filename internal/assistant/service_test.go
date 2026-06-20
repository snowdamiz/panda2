package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/memory"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
	"github.com/sn0w/panda2/internal/tools"
)

type fakeClient struct {
	response         llm.ChatResponse
	err              error
	responses        []llm.ChatResponse
	responsesByModel map[string]llm.ChatResponse
	errorsByModel    map[string]error
	requests         []llm.ChatRequest
}

func (f *fakeClient) Chat(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	f.requests = append(f.requests, request)
	if len(f.responses) > 0 {
		response := f.responses[0]
		f.responses = f.responses[1:]
		return response, nil
	}
	if err, ok := f.errorsByModel[request.Model]; ok {
		return llm.ChatResponse{}, err
	}
	if response, ok := f.responsesByModel[request.Model]; ok {
		return response, nil
	}
	return f.response, f.err
}

func newTestService(t *testing.T, client *fakeClient) (*Service, *store.Store) {
	t.Helper()
	db, err := store.Open(context.Background(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	configs := repository.NewGuildConfigRepository(db.DB)
	usage := repository.NewUsageRepository(db.DB)
	knowledge := repository.NewKnowledgeRepository(db.DB)
	memoryService := memory.NewService(knowledge)
	conversations := repository.NewConversationRepository(db.DB)
	return NewService(client, usage, configs, memoryService, conversations, "openrouter/auto", nil), db
}

func TestAskUsesGuildPromptAndMemory(t *testing.T) {
	ctx := context.Background()
	client := &fakeClient{response: llm.ChatResponse{Model: "fixture/model", Content: "Use @everyone key sk-123456789012"}}
	service, db := newTestService(t, client)
	configs := repository.NewGuildConfigRepository(db.DB)
	knowledge := repository.NewKnowledgeRepository(db.DB)

	if _, err := configs.EnsureDefault(ctx, "guild-1", "openrouter/auto"); err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if _, err := configs.UpdatePrompt(ctx, "guild-1", "Prefer short answers."); err != nil {
		t.Fatalf("UpdatePrompt: %v", err)
	}
	if _, err := configs.SetMemoryEnabled(ctx, "guild-1", true); err != nil {
		t.Fatalf("SetMemoryEnabled: %v", err)
	}
	if _, err := knowledge.AddDocument(ctx, store.KnowledgeDocument{GuildID: "guild-1", Title: "Deploy notes"}, "Deploys happen on Fridays after review."); err != nil {
		t.Fatalf("AddDocument: %v", err)
	}

	response, err := service.Ask(ctx, AskRequest{GuildID: "guild-1", UserID: "user-1", ChannelID: "channel-1", Question: "When do deploys happen?"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if strings.Contains(response.Content, "@everyone") || strings.Contains(response.Content, "sk-123456789012") {
		t.Fatalf("response was not sanitized: %q", response.Content)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one LLM request, got %d", len(client.requests))
	}
	joined := joinMessages(client.requests[0].Messages)
	if !strings.Contains(joined, "Prefer short answers.") {
		t.Fatalf("guild prompt missing from request: %s", joined)
	}
	if !strings.Contains(joined, "Deploys happen on Fridays") {
		t.Fatalf("memory context missing from request: %s", joined)
	}
}

func TestAskRespectsDisabledGuild(t *testing.T) {
	ctx := context.Background()
	client := &fakeClient{response: llm.ChatResponse{Content: "nope"}}
	service, db := newTestService(t, client)
	configs := repository.NewGuildConfigRepository(db.DB)

	if _, err := configs.EnsureDefault(ctx, "guild-1", "openrouter/auto"); err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if _, err := configs.SetAssistantEnabled(ctx, "guild-1", false); err != nil {
		t.Fatalf("SetAssistantEnabled: %v", err)
	}

	_, err := service.Ask(ctx, AskRequest{GuildID: "guild-1", UserID: "user-1", Question: "hi"})
	if !errors.Is(err, ErrAssistantDisabled) {
		t.Fatalf("expected ErrAssistantDisabled, got %v", err)
	}
	if len(client.requests) != 0 {
		t.Fatalf("disabled guild should not call LLM")
	}
}

func TestClassifyNaturalMessageUsesLLMDecision(t *testing.T) {
	ctx := context.Background()
	client := &fakeClient{response: llm.ChatResponse{Model: "fixture/model", Content: `{"respond":true,"prompt":"Is the deploy window Friday?"}`}}
	service, _ := newTestService(t, client)

	decision, err := service.ClassifyNaturalMessage(ctx, NaturalMessageRequest{
		GuildID:        "guild-1",
		UserID:         "user-1",
		ChannelID:      "channel-1",
		Content:        "Panda is the deploy window Friday?",
		BotMentioned:   true,
		ReplyContent:   "The deploy window moved to Friday.",
		ReplyMessageID: "message-1",
	})
	if err != nil {
		t.Fatalf("ClassifyNaturalMessage: %v", err)
	}
	if !decision.Respond || decision.Prompt != "Is the deploy window Friday?" {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one trigger request, got %d", len(client.requests))
	}
	joined := joinMessages(client.requests[0].Messages)
	if !strings.Contains(joined, "Bot mentioned: true") || !strings.Contains(joined, "Reply context") {
		t.Fatalf("trigger metadata missing from request: %s", joined)
	}
}

func TestClassifyNaturalMessageCanDecline(t *testing.T) {
	ctx := context.Background()
	client := &fakeClient{response: llm.ChatResponse{Content: `{"respond":false,"prompt":""}`}}
	service, _ := newTestService(t, client)

	decision, err := service.ClassifyNaturalMessage(ctx, NaturalMessageRequest{
		GuildID:   "guild-1",
		UserID:    "user-1",
		ChannelID: "channel-1",
		Content:   "ambient channel chatter",
	})
	if err != nil {
		t.Fatalf("ClassifyNaturalMessage: %v", err)
	}
	if decision.Respond || decision.Prompt != "" {
		t.Fatalf("expected declined decision, got %+v", decision)
	}
}

func TestChatPersistsConversationMessages(t *testing.T) {
	ctx := context.Background()
	client := &fakeClient{response: llm.ChatResponse{Model: "fixture/model", Content: "chat answer"}}
	service, db := newTestService(t, client)

	response, err := service.Chat(ctx, AskRequest{GuildID: "guild-1", ChannelID: "channel-1", UserID: "user-1", Question: "hello"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if response.Content != "chat answer" {
		t.Fatalf("unexpected response %q", response.Content)
	}

	var count int64
	if err := db.DB.Table("messages").Count(&count).Error; err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected user and assistant messages, got %d", count)
	}
}

func TestAskFallsBackToConfiguredModelOnTransientFailure(t *testing.T) {
	ctx := context.Background()
	client := &fakeClient{
		errorsByModel: map[string]error{
			"provider/primary": llm.Error{StatusCode: 503, Code: "upstream_unavailable", Message: "provider unavailable"},
		},
		responsesByModel: map[string]llm.ChatResponse{
			"provider/fallback": {Model: "provider/fallback", Content: "fallback answer"},
		},
	}
	service, db := newTestService(t, client)
	configs := repository.NewGuildConfigRepository(db.DB)
	if _, err := configs.EnsureDefault(ctx, "guild-1", "provider/primary"); err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if _, err := configs.UpdateModelSettings(ctx, "guild-1", map[string]any{"fallback_models": `["provider/fallback"]`}); err != nil {
		t.Fatalf("UpdateModelSettings: %v", err)
	}

	response, err := service.Ask(ctx, AskRequest{GuildID: "guild-1", UserID: "user-1", ChannelID: "channel-1", Question: "hi"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if response.Content != "fallback answer" {
		t.Fatalf("unexpected response %q", response.Content)
	}
	if len(client.requests) != 2 || client.requests[0].Model != "provider/primary" || client.requests[1].Model != "provider/fallback" {
		t.Fatalf("unexpected model sequence: %+v", client.requests)
	}
}

func TestAskDoesNotFallbackOnNonRetryableFailure(t *testing.T) {
	ctx := context.Background()
	client := &fakeClient{
		errorsByModel: map[string]error{
			"provider/primary": llm.Error{StatusCode: 400, Code: "bad_request", Message: "bad request"},
		},
		responsesByModel: map[string]llm.ChatResponse{
			"provider/fallback": {Content: "should not be used"},
		},
	}
	service, db := newTestService(t, client)
	configs := repository.NewGuildConfigRepository(db.DB)
	if _, err := configs.EnsureDefault(ctx, "guild-1", "provider/primary"); err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if _, err := configs.UpdateModelSettings(ctx, "guild-1", map[string]any{"fallback_models": `["provider/fallback"]`}); err != nil {
		t.Fatalf("UpdateModelSettings: %v", err)
	}

	_, err := service.Ask(ctx, AskRequest{GuildID: "guild-1", UserID: "user-1", ChannelID: "channel-1", Question: "hi"})
	if err == nil {
		t.Fatal("expected non-retryable error")
	}
	if len(client.requests) != 1 || client.requests[0].Model != "provider/primary" {
		t.Fatalf("fallback should not have been used: %+v", client.requests)
	}
}

func TestAskExecutesKnowledgeSearchTool(t *testing.T) {
	ctx := context.Background()
	client := &fakeClient{responses: []llm.ChatResponse{
		{
			Model: "fixture/model",
			ToolCalls: []llm.ToolCall{{
				ID:   "call-1",
				Type: "function",
				Function: llm.ToolCallFunction{
					Name:      "search_knowledge",
					Arguments: `{"query":"deploys","limit":"1"}`,
				},
			}},
		},
		{Model: "fixture/model", Content: "Deploys happen Friday."},
	}}
	service, db := newTestService(t, client)
	configs := repository.NewGuildConfigRepository(db.DB)
	knowledge := repository.NewKnowledgeRepository(db.DB)
	memoryService := memory.NewService(knowledge)
	registry, err := tools.NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	service.WithToolExecutor(tools.NewExecutor(registry, memoryService, configs))

	if _, err := configs.EnsureDefault(ctx, "guild-1", "openrouter/auto"); err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if _, err := configs.SetMemoryEnabled(ctx, "guild-1", true); err != nil {
		t.Fatalf("SetMemoryEnabled: %v", err)
	}
	if _, err := configs.UpdateModelSettings(ctx, "guild-1", map[string]any{"tool_policy": "read_only"}); err != nil {
		t.Fatalf("UpdateModelSettings: %v", err)
	}
	if _, err := knowledge.AddDocument(ctx, store.KnowledgeDocument{GuildID: "guild-1", Title: "Deploy notes"}, "Deploys happen Friday after review."); err != nil {
		t.Fatalf("AddDocument: %v", err)
	}

	response, err := service.Ask(ctx, AskRequest{
		GuildID:   "guild-1",
		UserID:    "user-1",
		ChannelID: "channel-1",
		Question:  "When are deploys?",
		AllowedPermissions: map[string]struct{}{
			"assistant.memory.read": {},
		},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if response.Content != "Deploys happen Friday." {
		t.Fatalf("unexpected response: %+v", response)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected initial and final LLM requests, got %d", len(client.requests))
	}
	if len(client.requests[0].Tools) == 0 || client.requests[0].Tools[0].Function.Name != "search_knowledge" {
		t.Fatalf("expected search_knowledge tool in first request: %+v", client.requests[0].Tools)
	}
	finalMessages := joinMessages(client.requests[1].Messages)
	if !strings.Contains(finalMessages, "Deploy notes") || !strings.Contains(finalMessages, "call-1") {
		t.Fatalf("expected tool result in final request: %s", finalMessages)
	}
}

func joinMessages(messages []llm.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		parts = append(parts, message.Content, message.ToolCallID)
	}
	return strings.Join(parts, "\n")
}
