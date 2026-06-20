package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/admin"
	contextsvc "github.com/sn0w/panda2/internal/context"
	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

func TestDefaultRegistryDefinitionsAreValid(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 70 {
		t.Fatalf("expected full Discord tool surface plus assistant tools, got %d", len(definitions))
	}
	for _, definition := range definitions {
		if definition.Timeout <= 0 {
			t.Fatalf("tool %s missing timeout", definition.Name)
		}
		if definition.RequiredPermission == "" {
			t.Fatalf("tool %s missing permission", definition.Name)
		}
		if definition.ToolClass == "" {
			t.Fatalf("tool %s missing class", definition.Name)
		}
	}
}

func TestRegistryRejectsDuplicateTool(t *testing.T) {
	definition := Definition{
		Name:               "duplicate",
		Description:        "test",
		RequiredPermission: admin.PermissionAssistantUse,
		ToolClass:          ToolClassWorkflow,
		InputSchema:        objectSchema("input"),
		OutputSchema:       objectSchema("output"),
		Timeout:            time.Second,
	}
	registry, err := NewRegistry(definition)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if err := registry.Register(definition); err == nil {
		t.Fatal("expected duplicate registration to fail")
	}
}

func TestOpenRouterToolsFiltersByPermission(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	tools := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy:      ToolPolicyReadOnly,
		Permissions: map[string]struct{}{admin.PermissionAssistantUse: {}},
	})
	names := toolNames(tools)
	if !names["discord_fetch_message"] || names["generate_workflow_json"] {
		t.Fatalf("unexpected read-only tools: %+v", names)
	}
	for _, tool := range tools {
		if tool.Type != "function" || tool.Function.Name == "" || len(tool.Function.Parameters) == 0 {
			t.Fatalf("unexpected OpenRouter tool: %+v", tool)
		}
		if strings.Contains(tool.Function.Name, ".") {
			t.Fatalf("wire tool name should be provider-safe: %s", tool.Function.Name)
		}
	}
}

func TestMustGetUnknownTool(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	_, err = registry.MustGet("missing")
	if !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("expected ErrUnknownTool, got %v", err)
	}
	definition, err := registry.MustGet("discord_fetch_message")
	if err != nil {
		t.Fatalf("wire-name lookup failed: %v", err)
	}
	if definition.Name != "discord.fetch_message" {
		t.Fatalf("unexpected wire lookup definition: %+v", definition)
	}
}

type fakeKnowledgeSearch struct{}

func (fakeKnowledgeSearch) Search(context.Context, string, string, int) ([]repository.KnowledgeSearchResult, error) {
	return []repository.KnowledgeSearchResult{{
		DocumentID: 7,
		ChunkID:    9,
		Title:      "Deploy notes",
		Snippet:    "Deploys happen Friday",
		Content:    "Deploys happen Friday after review.",
	}}, nil
}

type fakeConfigReader struct{}

func (fakeConfigReader) Get(context.Context, string) (store.GuildConfig, bool, error) {
	return store.GuildConfig{GuildID: "guild-1", DefaultModel: "provider/model", AssistantEnabled: true, MemoryEnabled: true, ToolPolicy: "read_only", MaxResponseTokens: 900}, true, nil
}

type fakeToolContextReader struct{}

func (fakeToolContextReader) MessageContext(context.Context, contextsvc.MessageRef) (contextsvc.PackedContext, error) {
	return contextsvc.PackedContext{
		Text: "Fetched Discord context.\n\n[S1] message_id=message-1 author_id=user-1\nDeploy window moved.",
		Citations: []contextsvc.Citation{{
			Label:     "S1",
			GuildID:   "guild-1",
			ChannelID: "channel-1",
			MessageID: "message-1",
			URL:       "https://discord.com/channels/guild-1/channel-1/message-1",
		}},
	}, nil
}

func (fakeToolContextReader) RecentMessagesContext(context.Context, contextsvc.ChannelRef, int) (contextsvc.PackedContext, error) {
	return contextsvc.PackedContext{Text: "recent context", Citations: []contextsvc.Citation{{Label: "S1", MessageID: "message-2"}}}, nil
}

type fakeToolAttachmentReader struct{}

func (fakeToolAttachmentReader) Get(context.Context, string, uint) (store.Attachment, error) {
	return store.Attachment{ID: 42, Filename: "notes.txt", ExtractedText: "Deploy after review. sk-123456789012"}, nil
}

type fakeDiscordProvider struct {
	calls int
}

func (f *fakeDiscordProvider) ExecuteDiscordTool(context.Context, DiscordToolRequest) (any, error) {
	f.calls++
	return map[string]any{"executed": true}, nil
}

type fakeAuditRecorder struct {
	events []store.AuditEvent
}

func (f *fakeAuditRecorder) Record(_ context.Context, event store.AuditEvent) error {
	f.events = append(f.events, event)
	return nil
}

func TestExecutorRunsKnowledgeSearchTool(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, fakeKnowledgeSearch{}, fakeConfigReader{})
	message, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		Access:  testAccess(ToolPolicyReadOnly, admin.PermissionAssistantMemoryRead),
		Call: llm.ToolCall{
			ID:   "call-1",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "search_knowledge",
				Arguments: `{"query":"deploys","limit":"1"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if message.Role != "tool" || message.ToolCallID != "call-1" || !strings.Contains(message.Content, "Deploy notes") {
		t.Fatalf("unexpected tool message: %+v", message)
	}
}

func TestExecutorRunsFetchMessageTool(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, nil).WithContextReader(fakeToolContextReader{})
	message, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		Access:  testAccess(ToolPolicyReadOnly, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-message",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "discord_fetch_message",
				Arguments: `{"channel_id":"channel-1","message_id":"message-1"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if message.Role != "tool" || !strings.Contains(message.Content, "Deploy window moved") || !strings.Contains(message.Content, "message-1") {
		t.Fatalf("unexpected fetch message result: %+v", message)
	}
}

func TestExecutorAuditsToolCallsWithTargetIDs(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	audit := &fakeAuditRecorder{}
	executor := NewExecutor(registry, nil, nil).WithContextReader(fakeToolContextReader{}).WithAuditRecorder(audit)
	_, err = executor.Execute(context.Background(), ExecutionRequest{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		ActorID:   "user-1",
		RequestID: "request-1",
		Access:    testAccess(ToolPolicyReadOnly, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-message",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "discord_fetch_message",
				Arguments: `{"channel_id":"channel-1","message_id":"message-1","purpose":"quote sk-123456789012"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("expected one audit event, got %d", len(audit.events))
	}
	var metadata map[string]string
	if err := json.Unmarshal([]byte(audit.events[0].Metadata), &metadata); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if metadata["tool"] != "discord.fetch_message" || !strings.Contains(metadata["target_ids"], "message-1") {
		t.Fatalf("audit metadata missing tool/target ids: %+v", metadata)
	}
	if strings.Contains(metadata["arguments"], "sk-123456789012") {
		t.Fatalf("audit arguments were not redacted: %+v", metadata)
	}
}

func TestExecutorRunsAttachmentSummaryTool(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, nil).WithAttachmentReader(fakeToolAttachmentReader{})
	message, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		Access:  testAccess(ToolPolicyReadOnly, admin.PermissionAssistantAttachments),
		Call: llm.ToolCall{
			ID:   "call-attachment",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "summarize_text_file",
				Arguments: `{"attachment_id":"42","detail":"brief"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if message.Role != "tool" || !strings.Contains(message.Content, "notes.txt") || !strings.Contains(message.Content, "Deploy after review") || strings.Contains(message.Content, "sk-123456789012") {
		t.Fatalf("unexpected attachment summary result: %+v", message)
	}
}

func TestExecutorWriteToolRequiresDryRunOrConfirmation(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	provider := &fakeDiscordProvider{}
	executor := NewExecutor(registry, nil, nil).WithDiscordToolProvider(provider)
	access := testAccess(ToolPolicyWriteConfirmed, admin.PermissionAssistantUse)
	message, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		Access:  access,
		Call: llm.ToolCall{
			ID:   "call-send",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "discord_send_message",
				Arguments: `{"channel_id":"channel-1","content":"hello @everyone","dry_run":true}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute dry run: %v", err)
	}
	if !strings.Contains(message.Content, `"dry_run":true`) || provider.calls != 0 {
		t.Fatalf("dry run should preview without provider execution: message=%+v calls=%d", message, provider.calls)
	}

	message, err = executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		Access:  access,
		Call: llm.ToolCall{
			ID:   "call-send",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "discord_send_message",
				Arguments: `{"channel_id":"channel-1","content":"hello"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute confirmation request: %v", err)
	}
	if !strings.Contains(message.Content, `"confirmation_required":true`) || provider.calls != 0 {
		t.Fatalf("write should require confirmation without provider execution: message=%+v calls=%d", message, provider.calls)
	}
}

func TestExecutorRunsWorkflowJSONTool(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, nil)
	message, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		Access:  testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-workflow",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "generate_workflow_json",
				Arguments: `{"workflow":"summarize","inputs":{"message_id":"123"}}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if message.Role != "tool" || message.ToolCallID != "call-workflow" || !strings.Contains(message.Content, `"workflow":"summarize"`) || !strings.Contains(message.Content, `"dry_run":true`) {
		t.Fatalf("unexpected workflow tool message: %+v", message)
	}
}

func TestExecutorRunsModeratorNoteTool(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, nil)
	message, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		Access:  testAccess(ToolPolicyModerator, admin.PermissionModerationUse),
		Call: llm.ToolCall{
			ID:   "call-note",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "draft_moderator_note",
				Arguments: `{"context":"User cooled down after warning.","tone":"calm"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if message.Role != "tool" || !strings.Contains(message.Content, "human review") || !strings.Contains(message.Content, "calm tone") {
		t.Fatalf("unexpected moderator note message: %+v", message)
	}
}

func TestExecutorFiltersExecutableToolsByPermission(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, fakeKnowledgeSearch{}, fakeConfigReader{})
	available := executor.OpenRouterTools(testAccess(ToolPolicyOwnerOps, admin.PermissionAssistantMemoryRead, admin.PermissionAdminConfigRead))
	names := map[string]bool{}
	for _, tool := range available {
		names[tool.Function.Name] = true
	}
	if !names["search_knowledge"] || !names["read_config"] || names["discord_fetch_message"] {
		t.Fatalf("unexpected executable tools: %+v", names)
	}

	assistantTools := executor.OpenRouterTools(testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse))
	names = map[string]bool{}
	for _, tool := range assistantTools {
		names[tool.Function.Name] = true
	}
	if !names["generate_workflow_json"] || names["discord_fetch_message"] || names["discord_fetch_messages"] {
		t.Fatalf("unexpected assistant executable tools: %+v", names)
	}

	executor = NewExecutor(registry, nil, nil).WithContextReader(fakeToolContextReader{}).WithAttachmentReader(fakeToolAttachmentReader{})
	assistantTools = executor.OpenRouterTools(testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse, admin.PermissionAssistantAttachments))
	names = map[string]bool{}
	for _, tool := range assistantTools {
		names[tool.Function.Name] = true
	}
	if !names["discord_fetch_message"] || !names["discord_fetch_messages"] || !names["summarize_text_file"] || !names["generate_workflow_json"] {
		t.Fatalf("expected configured context and attachment tools, got %+v", names)
	}
}

func testAccess(policy string, permissions ...string) ToolAccess {
	values := map[string]struct{}{}
	for _, permission := range permissions {
		values[permission] = struct{}{}
	}
	return ToolAccess{Policy: policy, Permissions: values}
}

func toolNames(tools []llm.Tool) map[string]bool {
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Function.Name] = true
	}
	return names
}
