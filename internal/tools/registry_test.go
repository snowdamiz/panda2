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
	"github.com/sn0w/panda2/internal/memory"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
	"github.com/sn0w/panda2/internal/websearch"
)

func TestDefaultRegistryDefinitionsAreValid(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 80 {
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
	if !names["discord_fetch_message"] || !names["panda_list_tools"] || names["generate_workflow_json"] {
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

func TestOpenRouterToolsHonorsRoleToolRestrictions(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}

	restricted := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy:          ToolPolicyReadOnly,
		Permissions:     map[string]struct{}{admin.PermissionAssistantWebSearch: {}},
		RestrictedTools: map[string]struct{}{"web.search": {}},
	})
	if toolNames(restricted)["web_search"] {
		t.Fatalf("web_search should be hidden when restricted to another role: %+v", toolNames(restricted))
	}

	allowed := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy:          ToolPolicyReadOnly,
		Permissions:     map[string]struct{}{admin.PermissionAssistantWebSearch: {}},
		AllowedTools:    map[string]struct{}{"web.search": {}},
		RestrictedTools: map[string]struct{}{"web.search": {}},
	})
	if !toolNames(allowed)["web_search"] {
		t.Fatalf("web_search should be visible to an allowed role: %+v", toolNames(allowed))
	}

	adminOnly := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy:       ToolPolicyOwnerOps,
		Permissions:  map[string]struct{}{admin.PermissionAssistantUse: {}},
		AllowedTools: map[string]struct{}{"read_config": {}},
	})
	if toolNames(adminOnly)["read_config"] {
		t.Fatalf("tool allowlist should not grant admin permissions: %+v", toolNames(adminOnly))
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

type fakeWebSearch struct{}

func (fakeWebSearch) Search(context.Context, websearch.Request) (websearch.Response, error) {
	return websearch.Response{
		Provider:             "brave_search",
		Query:                "sqlite release",
		MoreResultsAvailable: true,
		Results: []websearch.Result{{
			Title:         "SQLite Release History",
			URL:           "https://sqlite.org/changes.html",
			Description:   "Recent SQLite releases.",
			Source:        "SQLite",
			ExtraSnippets: []string{"Version details"},
		}},
	}, nil
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

type fakeDynamicProvider struct {
	tools []llm.Tool
}

func (f fakeDynamicProvider) OpenRouterTools(context.Context, DynamicToolListRequest) ([]llm.Tool, error) {
	return f.tools, nil
}

func (f fakeDynamicProvider) ExecuteDynamicTool(context.Context, DynamicExecutionRequest) (ExecutionResult, error) {
	return ExecutionResult{}, ErrUnknownTool
}

func newToolAdminService(t *testing.T) *admin.Service {
	t.Helper()
	ctx := context.Background()
	dataStore, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })

	configs := repository.NewGuildConfigRepository(dataStore.DB)
	usage := repository.NewUsageRepository(dataStore.DB)
	audit := repository.NewAuditRepository(dataStore.DB)
	memoryService := memory.NewService(repository.NewKnowledgeRepository(dataStore.DB))
	access := repository.NewAccessRepository(dataStore.DB)
	budgets := repository.NewBudgetRepository(dataStore.DB)
	members := repository.NewMemberRepository(dataStore.DB)
	return admin.NewService(configs, usage, audit, memoryService, access, budgets, nil, "openrouter/auto", members)
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
	result, err := executor.Execute(context.Background(), ExecutionRequest{
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
	message := result.Message
	if message.Role != "tool" || message.ToolCallID != "call-1" || !strings.Contains(message.Content, "Deploy notes") {
		t.Fatalf("unexpected tool message: %+v", message)
	}
}

func TestExecutorRunsWebSearchTool(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, nil).WithWebSearcher(fakeWebSearch{})
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		Access:  testAccess(ToolPolicyReadOnly, admin.PermissionAssistantWebSearch),
		Call: llm.ToolCall{
			ID:   "call-web",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "web_search",
				Arguments: `{"query":"sqlite release","limit":1,"freshness":"pw"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	message := result.Message
	if message.Role != "tool" || message.ToolCallID != "call-web" || !strings.Contains(message.Content, "sqlite.org") || !strings.Contains(message.Content, "brave_search") {
		t.Fatalf("unexpected tool message: %+v", message)
	}
}

func TestExecutorRunsFetchMessageTool(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, nil).WithContextReader(fakeToolContextReader{})
	result, err := executor.Execute(context.Background(), ExecutionRequest{
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
	message := result.Message
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
	result, err := executor.Execute(context.Background(), ExecutionRequest{
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
	message := result.Message
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
	result, err := executor.Execute(context.Background(), ExecutionRequest{
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
	message := result.Message
	if !strings.Contains(message.Content, `"dry_run":true`) || provider.calls != 0 {
		t.Fatalf("dry run should preview without provider execution: message=%+v calls=%d", message, provider.calls)
	}

	result, err = executor.Execute(context.Background(), ExecutionRequest{
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
	message = result.Message
	if !strings.Contains(message.Content, `"confirmation_required":true`) || provider.calls != 0 {
		t.Fatalf("write should require confirmation without provider execution: message=%+v calls=%d", message, provider.calls)
	}
}

func TestExecutorRunsAdminServiceTools(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	adminOps := newToolAdminService(t)
	executor := NewExecutor(registry, nil, nil).WithAdminOperations(adminOps)

	consent, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "user-1",
		Access:  testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-consent",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "manage_memory_consent",
				Arguments: `{"action":"enable"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute consent: %v", err)
	}
	if !strings.Contains(consent.Message.Content, `"enabled":true`) {
		t.Fatalf("unexpected consent result: %+v", consent)
	}

	soul, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAssistantSoulWrite),
		Call: llm.ToolCall{
			ID:   "call-soul",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_soul",
				Arguments: `{"action":"set","soul":"Answer with dry wit and crisp bullets."}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute soul update: %v", err)
	}
	if !strings.Contains(soul.Message.Content, "dry wit") {
		t.Fatalf("unexpected soul result: %+v", soul)
	}

	added, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminMemoryManage),
		Call: llm.ToolCall{
			ID:   "call-knowledge-add",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_knowledge",
				Arguments: `{"action":"add","title":"Deploy notes","content":"Deploys happen Friday after review."}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute knowledge add: %v", err)
	}
	if !strings.Contains(added.Message.Content, "Deploy notes") {
		t.Fatalf("unexpected knowledge add result: %+v", added)
	}

	search, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminMemoryManage),
		Call: llm.ToolCall{
			ID:   "call-knowledge-search",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_knowledge",
				Arguments: `{"action":"search","query":"Friday deploys"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute knowledge search: %v", err)
	}
	if !strings.Contains(search.Message.Content, "Deploy notes") {
		t.Fatalf("unexpected knowledge search result: %+v", search)
	}

	toolAccess, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-tool-access",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_tool_access",
				Arguments: `{"action":"add","tool_name":"web.search","role_id":"role-search"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute tool access add: %v", err)
	}
	if !strings.Contains(toolAccess.Message.Content, "web.search") || !strings.Contains(toolAccess.Message.Content, "role-search") {
		t.Fatalf("unexpected tool access result: %+v", toolAccess)
	}
}

func TestExecutorAdminRemovalToolsRequireConfirmation(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, nil).WithAdminOperations(newToolAdminService(t))
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-channel-remove",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_channel_rule",
				Arguments: `{"action":"remove","channel_id":"channel-1"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute channel remove: %v", err)
	}
	message := result.Message
	if !strings.Contains(message.Content, `"confirmation_required":true`) || !strings.Contains(message.Content, "channel-1") {
		t.Fatalf("expected confirmation preview, got %+v", message)
	}
	if result.Confirmation == nil || result.Confirmation.Action != "channel_rule.remove" || result.Confirmation.Arguments["channel_id"] != "channel-1" {
		t.Fatalf("expected channel-rule confirmation artifact, got %+v", result.Confirmation)
	}
}

func TestExecutorRunsWorkflowJSONTool(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, nil)
	result, err := executor.Execute(context.Background(), ExecutionRequest{
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
	message := result.Message
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
	result, err := executor.Execute(context.Background(), ExecutionRequest{
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
	message := result.Message
	if message.Role != "tool" || !strings.Contains(message.Content, "human review") || !strings.Contains(message.Content, "calm tone") {
		t.Fatalf("unexpected moderator note message: %+v", message)
	}
}

func TestExecutorListsNativeAndComposedTools(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, nil).
		WithContextReader(fakeToolContextReader{}).
		WithDynamicToolProvider(fakeDynamicProvider{tools: []llm.Tool{{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "builder_welcome",
				Description: "Welcomes builders.",
				Parameters:  objectSchema("user_id", "role_id"),
			},
		}}})
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ActorID:        "user-1",
		InvocationType: "chat_tool",
		Access:         testAccess(ToolPolicyReadOnly, admin.PermissionAssistantUse, admin.PermissionToolComposeInvoke),
		Call: llm.ToolCall{
			ID:   "call-list",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_list_tools",
				Arguments: `{"include_schemas":true}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	content := result.Message.Content
	for _, want := range []string{`"native_name":"discord.fetch_message"`, `"name":"panda_list_tools"`, `"kind":"composed"`, `"name":"builder_welcome"`, `"input_schema"`} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected tool listing to contain %s, got %s", want, content)
		}
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

	executor = NewExecutor(registry, nil, nil).WithContextReader(fakeToolContextReader{}).WithAttachmentReader(fakeToolAttachmentReader{}).WithWebSearcher(fakeWebSearch{})
	assistantTools = executor.OpenRouterTools(testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse, admin.PermissionAssistantAttachments))
	names = map[string]bool{}
	for _, tool := range assistantTools {
		names[tool.Function.Name] = true
	}
	if !names["discord_fetch_message"] || !names["discord_fetch_messages"] || !names["summarize_text_file"] || !names["generate_workflow_json"] || names["web_search"] {
		t.Fatalf("expected configured context and attachment tools, got %+v", names)
	}

	assistantTools = executor.OpenRouterTools(testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse, admin.PermissionAssistantWebSearch))
	names = map[string]bool{}
	for _, tool := range assistantTools {
		names[tool.Function.Name] = true
	}
	if !names["web_search"] || !names["discord_fetch_message"] || !names["generate_workflow_json"] {
		t.Fatalf("expected configured web search tool, got %+v", names)
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
