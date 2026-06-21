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
	if len(definitions) != 82 {
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
		assertSchemaRequiredIsArray(t, definition.Name, "input", definition.InputSchema)
		assertSchemaRequiredIsArray(t, definition.Name, "output", definition.OutputSchema)
	}
}

func assertSchemaRequiredIsArray(t *testing.T, toolName, schemaName string, schema json.RawMessage) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(schema, &fields); err != nil {
		t.Fatalf("tool %s %s schema is invalid JSON: %v", toolName, schemaName, err)
	}
	required, ok := fields["required"]
	if !ok {
		t.Fatalf("tool %s %s schema missing required array", toolName, schemaName)
	}
	if string(required) == "null" {
		t.Fatalf("tool %s %s schema required must be an array, got null", toolName, schemaName)
	}
	var requiredFields []string
	if err := json.Unmarshal(required, &requiredFields); err != nil {
		t.Fatalf("tool %s %s schema required must be an array: %v", toolName, schemaName, err)
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
	writeTools := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy:      ToolPolicyWriteConfirmed,
		Permissions: map[string]struct{}{admin.PermissionAssistantUse: {}},
	})
	writeNames := toolNames(writeTools)
	if !writeNames["discord_send_message"] || writeNames["discord_create_thread"] {
		t.Fatalf("plain assistant use should not expose thread creation, got %+v", writeNames)
	}
	threadTools := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy: ToolPolicyWriteConfirmed,
		Permissions: map[string]struct{}{
			admin.PermissionAssistantUse:        {},
			admin.PermissionAssistantUseThreads: {},
		},
	})
	threadNames := toolNames(threadTools)
	if !threadNames["discord_create_thread"] {
		t.Fatalf("thread permission should expose thread creation, got %+v", threadNames)
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

func TestOpenRouterToolsKeepsMetadataAvailableWhenPolicyOff(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	tools := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy:      ToolPolicyOff,
		Permissions: map[string]struct{}{admin.PermissionAssistantUse: {}},
	})
	names := toolNames(tools)
	if !names["panda_list_tools"] {
		t.Fatalf("expected metadata list tool under off policy, got %+v", names)
	}
	if names["discord_fetch_message"] || names["generate_workflow_json"] {
		t.Fatalf("off policy should still hide action tools, got %+v", names)
	}
}

func TestOpenRouterToolsAdminOnlyGatesRegularUsers(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}

	regularTools := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy: ToolPolicyAdminOnly,
		Permissions: map[string]struct{}{
			admin.PermissionAssistantUse:       {},
			admin.PermissionAssistantWebSearch: {},
		},
	})
	regularNames := toolNames(regularTools)
	if !regularNames["panda_list_tools"] || !regularNames["web_search"] || regularNames["discord_fetch_message"] {
		t.Fatalf("admin_only should expose metadata and web search to regular users, got %+v", regularNames)
	}

	adminTools := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy: ToolPolicyAdminOnly,
		Permissions: map[string]struct{}{
			admin.PermissionAssistantUse:       {},
			admin.PermissionAssistantWebSearch: {},
			admin.PermissionAdminConfigRead:    {},
		},
	})
	adminNames := toolNames(adminTools)
	if !adminNames["web_search"] || !adminNames["read_config"] {
		t.Fatalf("admin_only should allow admin-scoped tool access, got %+v", adminNames)
	}
}

func TestOpenRouterToolsHonorsIncludeInModelContext(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	tools := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy:      ToolPolicyModerator,
		Permissions: map[string]struct{}{admin.PermissionModerationUse: {}},
	})
	if toolNames(tools)["draft_moderator_note"] {
		t.Fatalf("draft_moderator_note should not be exposed to the model tool list")
	}
}

func TestOpenRouterToolsKeepsBotAdminManagementAvailableWhenPolicyOff(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	tools := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy: ToolPolicyOff,
		Permissions: map[string]struct{}{
			admin.PermissionAssistantUse:     {},
			admin.PermissionAdminConfigRead:  {},
			admin.PermissionAdminConfigWrite: {},
			admin.PermissionAdminUsageRead:   {},
		},
	})
	names := toolNames(tools)
	for _, want := range []string{"panda_list_tools", "read_config", "panda_manage_tool_access", "panda_manage_channel_rule", "panda_usage_report"} {
		if !names[want] {
			t.Fatalf("expected %s under off policy for admins, got %+v", want, names)
		}
	}
	for _, hidden := range []string{"generate_workflow_json", "discord_modify_channel_permissions", "discord_send_message"} {
		if names[hidden] {
			t.Fatalf("expected %s to stay hidden under off policy, got %+v", hidden, names)
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

func TestReadConfigCannotReadOtherGuildsWithoutOwnerOps(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, fakeConfigReaderByGuild{
		"guild-1": {GuildID: "guild-1", DefaultModel: "model-one", AssistantEnabled: true, MemoryEnabled: true, ToolPolicy: ToolPolicyAdminOnly},
		"guild-2": {GuildID: "guild-2", DefaultModel: "model-two", AssistantEnabled: true, MemoryEnabled: true, ToolPolicy: ToolPolicyAdminOnly},
	})

	denied, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyOff, admin.PermissionAdminConfigRead),
		Call: llm.ToolCall{
			ID:   "call-read-config-denied",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "read_config",
				Arguments: `{"guild_id":"guild-2"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute read_config denied: %v", err)
	}
	if !strings.Contains(denied.Message.Content, "current guild") || strings.Contains(denied.Message.Content, "model-two") {
		t.Fatalf("expected cross-guild read denial, got %+v", denied.Message)
	}

	allowed, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "owner",
		Access:  testAccess(ToolPolicyOwnerOps, admin.PermissionAdminConfigRead, admin.PermissionOwnerOps),
		Call: llm.ToolCall{
			ID:   "call-read-config-owner",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "read_config",
				Arguments: `{"guild_id":"guild-2"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute read_config owner: %v", err)
	}
	if !strings.Contains(allowed.Message.Content, `"guild_id":"guild-2"`) || !strings.Contains(allowed.Message.Content, "model-two") {
		t.Fatalf("expected owner cross-guild read, got %+v", allowed.Message)
	}
}

func TestDiscordReadToolsAreScopedToCurrentChannelForNormalUsers(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, nil).WithContextReader(fakeToolContextReader{})

	denied, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		ActorID:   "user-1",
		Access:    testAccess(ToolPolicyReadOnly, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-cross-channel",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "discord_fetch_message",
				Arguments: `{"channel_id":"channel-2","message_id":"message-1"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute denied Discord read: %v", err)
	}
	if !strings.Contains(denied.Message.Content, "outside the current channel") {
		t.Fatalf("expected cross-channel denial, got %+v", denied.Message)
	}

	allowed, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		ActorID:   "user-1",
		Access:    testAccess(ToolPolicyReadOnly, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-current-channel",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "discord_fetch_message",
				Arguments: `{"channel_id":"channel-1","message_id":"message-1"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute current-channel Discord read: %v", err)
	}
	if !strings.Contains(allowed.Message.Content, "Fetched Discord context") {
		t.Fatalf("expected current-channel read, got %+v", allowed.Message)
	}

	adminRead, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		ActorID:   "admin",
		Access:    testAccess(ToolPolicyReadOnly, admin.PermissionAssistantUse, admin.PermissionAdminConfigRead),
		Call: llm.ToolCall{
			ID:   "call-admin-cross-channel",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "discord_fetch_message",
				Arguments: `{"channel_id":"channel-2","message_id":"message-1"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute admin Discord read: %v", err)
	}
	if !strings.Contains(adminRead.Message.Content, "Fetched Discord context") {
		t.Fatalf("expected admin cross-channel read, got %+v", adminRead.Message)
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

type fakeConfigReaderByGuild map[string]store.GuildConfig

func (f fakeConfigReaderByGuild) Get(_ context.Context, guildID string) (store.GuildConfig, bool, error) {
	config, ok := f[guildID]
	return config, ok, nil
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
	calls    int
	requests []DiscordToolRequest
	result   any
}

func (f *fakeDiscordProvider) ExecuteDiscordTool(_ context.Context, request DiscordToolRequest) (any, error) {
	f.calls++
	f.requests = append(f.requests, request)
	if f.result != nil {
		return f.result, nil
	}
	return map[string]any{"executed": true}, nil
}

type fakeDynamicProvider struct {
	tools  []llm.Tool
	result ExecutionResult
	err    error
}

func (f fakeDynamicProvider) OpenRouterTools(context.Context, DynamicToolListRequest) ([]llm.Tool, error) {
	return f.tools, nil
}

func (f fakeDynamicProvider) ExecuteDynamicTool(context.Context, DynamicExecutionRequest) (ExecutionResult, error) {
	if f.result.Message.Role != "" || f.err != nil {
		return f.result, f.err
	}
	return ExecutionResult{}, ErrUnknownTool
}

type fakeComposedManager struct {
	requests []ComposedToolManagementRequest
}

func (f *fakeComposedManager) ManageComposedTool(_ context.Context, request ComposedToolManagementRequest) (any, error) {
	f.requests = append(f.requests, request)
	return map[string]any{
		"result": map[string]any{
			"action":    request.Action,
			"tool_name": request.ToolName,
			"text":      request.Text,
			"dry_run":   request.DryRun,
		},
	}, nil
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

func TestExecutorRunsComposedToolManager(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	manager := &fakeComposedManager{}
	executor := NewExecutor(registry, nil, nil).WithComposedToolManager(manager)
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionToolComposeDraft),
		Call: llm.ToolCall{
			ID:   "call-composed-manager",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_composed_tool",
				Arguments: `{"action":"draft","request":"Create a welcome tool","dry_run":true}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(manager.requests) != 1 {
		t.Fatalf("expected composed manager request, got %d", len(manager.requests))
	}
	request := manager.requests[0]
	if request.Action != "draft" || request.Text != "Create a welcome tool" || !request.DryRun {
		t.Fatalf("unexpected composed manager request: %+v", request)
	}
	if !strings.Contains(result.Message.Content, `"action":"draft"`) || !strings.Contains(result.Message.Content, `"dry_run":true`) {
		t.Fatalf("unexpected composed manager result: %+v", result)
	}
}

func TestExecutorRedactsDynamicToolResults(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	secret := "sk-abcdefghijklmnopqrstuvwxyz123456"
	executor := NewExecutor(registry, nil, nil).WithDynamicToolProvider(fakeDynamicProvider{
		result: ExecutionResult{Message: llm.Message{
			Role:       "tool",
			ToolCallID: "call-dynamic",
			Content:    `{"secret":"` + secret + `"}`,
		}},
	})
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		Access:  testAccess(ToolPolicyReadOnly, admin.PermissionToolComposeInvoke),
		Call: llm.ToolCall{
			ID:   "call-dynamic",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "custom_dynamic_tool",
				Arguments: `{}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(result.Message.Content, secret) || !strings.Contains(result.Message.Content, "[redacted]") {
		t.Fatalf("dynamic tool result was not redacted: %+v", result)
	}
}

func TestExecutorComposedManagerChecksActionPermission(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	manager := &fakeComposedManager{}
	executor := NewExecutor(registry, nil, nil).WithComposedToolManager(manager)
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionToolComposeDraft),
		Call: llm.ToolCall{
			ID:   "call-composed-approve",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_composed_tool",
				Arguments: `{"action":"approve","tool_name":"welcome","version":1}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(manager.requests) != 0 {
		t.Fatalf("approval should not reach manager without approve permission: %+v", manager.requests)
	}
	if !strings.Contains(result.Message.Content, "missing permission") || !strings.Contains(result.Message.Content, admin.PermissionToolComposeApprove) {
		t.Fatalf("unexpected permission error result: %+v", result)
	}
}

func TestExecutorHidesComposedManagerFromInvokeOnlyAccess(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, nil).WithComposedToolManager(&fakeComposedManager{})
	tools := executor.OpenRouterTools(testAccess(ToolPolicyWriteConfirmed, admin.PermissionToolComposeInvoke))
	if toolNames(tools)["panda_manage_composed_tool"] {
		t.Fatalf("invoke-only access should use approved composed tools directly, got %+v", toolNames(tools))
	}
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
	if len(result.SourceLinks) != 1 || result.SourceLinks[0].Title != "SQLite Release History" || result.SourceLinks[0].URL != "https://sqlite.org/changes.html" {
		t.Fatalf("unexpected web source links: %+v", result.SourceLinks)
	}
}

func TestExecutorRunsFetchMessageTool(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, nil).WithContextReader(fakeToolContextReader{})
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Access:    testAccess(ToolPolicyReadOnly, admin.PermissionAssistantUse),
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

func TestExecutorCreatePrivateThreadUsesPrivateThreadPermission(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	provider := &fakeDiscordProvider{}
	executor := NewExecutor(registry, nil, nil).WithDiscordToolProvider(provider)
	access := testAccess(ToolPolicyWriteConfirmed, admin.PermissionAssistantUse, admin.PermissionAssistantUseThreads)

	preview, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		Access:  access,
		Call: llm.ToolCall{
			ID:   "call-private-thread-preview",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "discord_create_thread",
				Arguments: `{"channel_id":"channel-1","name":"Support follow-up","private":true,"dry_run":true}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute preview: %v", err)
	}
	if !strings.Contains(preview.Message.Content, "CREATE_PRIVATE_THREADS") || strings.Contains(preview.Message.Content, "CREATE_PUBLIC_THREADS") {
		t.Fatalf("private thread preview should use private thread permission: %s", preview.Message.Content)
	}

	_, err = executor.Execute(context.Background(), ExecutionRequest{
		GuildID:              "guild-1",
		Access:               access,
		AllowConfirmedWrites: true,
		Call: llm.ToolCall{
			ID:   "call-private-thread-confirmed",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "discord_create_thread",
				Arguments: `{"channel_id":"channel-1","name":"Support follow-up","private":true}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute confirmed: %v", err)
	}
	if provider.calls != 1 || len(provider.requests) != 1 {
		t.Fatalf("expected one provider call, calls=%d requests=%d", provider.calls, len(provider.requests))
	}
	if strings.Join(provider.requests[0].Permissions, ",") != "CREATE_PRIVATE_THREADS" {
		t.Fatalf("confirmed private thread should use private permission, got %+v", provider.requests[0].Permissions)
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
	if !strings.Contains(toolAccess.Message.Content, `"confirmation_required":true`) || !strings.Contains(toolAccess.Message.Content, "web.search") || !strings.Contains(toolAccess.Message.Content, "role-search") {
		t.Fatalf("unexpected tool access confirmation result: %+v", toolAccess)
	}
	if toolAccess.Confirmation == nil || toolAccess.Confirmation.Action != "tool_access.add" || toolAccess.Confirmation.Arguments["tool_name"] != "web.search" {
		t.Fatalf("expected tool access confirmation, got %+v", toolAccess.Confirmation)
	}
}

func TestExecutorRoleProfileAndMemberRoleToolsRequireConfirmation(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	provider := &fakeDiscordProvider{result: map[string]any{"roles": []map[string]any{{
		"id":   "100000000000000777",
		"name": "Pickle",
	}}}}
	executor := NewExecutor(registry, nil, nil).
		WithAdminOperations(newToolAdminService(t)).
		WithDiscordToolProvider(provider)

	profile, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyOff, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-profile",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_role_permission",
				Arguments: `{"action":"add","profile":"moderator","role_name":"Pickle"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute role profile: %v", err)
	}
	if profile.Confirmation == nil || profile.Confirmation.Action != "role_profile.add" || profile.Confirmation.Arguments["profile"] != "moderator" || profile.Confirmation.Arguments["role_id"] != "100000000000000777" {
		t.Fatalf("expected role profile confirmation, got %+v", profile)
	}

	memberRole, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyOff, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-member-role",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_member_role",
				Arguments: `{"action":"add","user_id":"user-target","role_name":"Pickle"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute member role: %v", err)
	}
	if memberRole.Confirmation == nil || memberRole.Confirmation.Action != "member_role.add" || memberRole.Confirmation.Arguments["user_id"] != "user-target" || memberRole.Confirmation.Arguments["role_id"] != "100000000000000777" {
		t.Fatalf("expected member role confirmation, got %+v", memberRole)
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

func TestExecutorAdminMutationToolsRequireConfirmation(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, nil).WithAdminOperations(newToolAdminService(t))

	for _, tc := range []struct {
		name           string
		tool           string
		arguments      string
		expectedAction string
	}{
		{
			name:           "budget set",
			tool:           "panda_manage_budget_limit",
			arguments:      `{"action":"set","scope":"guild","limit":12,"window":"1h"}`,
			expectedAction: "budget_limit.set",
		},
		{
			name:           "role permission add",
			tool:           "panda_manage_role_permission",
			arguments:      `{"action":"add","role_id":"100000000000000001","permission":"assistant.web_search"}`,
			expectedAction: "role_permission.add",
		},
		{
			name:           "channel rule allow",
			tool:           "panda_manage_channel_rule",
			arguments:      `{"action":"allow","channel_id":"channel-1"}`,
			expectedAction: "channel_rule.set",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := executor.Execute(context.Background(), ExecutionRequest{
				GuildID: "guild-1",
				ActorID: "admin",
				Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
				Call: llm.ToolCall{
					ID:   "call-" + strings.ReplaceAll(tc.name, " ", "-"),
					Type: "function",
					Function: llm.ToolCallFunction{
						Name:      tc.tool,
						Arguments: tc.arguments,
					},
				},
			})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !strings.Contains(result.Message.Content, `"confirmation_required":true`) {
				t.Fatalf("expected confirmation payload, got %+v", result.Message)
			}
			if result.Confirmation == nil || result.Confirmation.Action != tc.expectedAction {
				t.Fatalf("expected %s confirmation, got %+v", tc.expectedAction, result.Confirmation)
			}
		})
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
		WithDiscordToolProvider(&fakeDiscordProvider{}).
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
		Access:         testAccess(ToolPolicyReadOnly, admin.PermissionAssistantUse, admin.PermissionAdminConfigRead, admin.PermissionToolComposeInvoke),
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
	for _, want := range []string{`"native_name":"discord.fetch_message"`, `"name":"discord_channel_activity_summary"`, `"name":"panda_list_tools"`, `"kind":"composed"`, `"name":"builder_welcome"`, `"input_schema"`} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected tool listing to contain %s, got %s", want, content)
		}
	}
	if strings.Contains(content, "[redacted]") {
		t.Fatalf("tool names should not be redacted in list_tools output: %s", content)
	}
}

func TestExecutorListToolsHidesAdminToolsFromNormalUsers(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, fakeConfigReader{}).
		WithDiscordToolProvider(&fakeDiscordProvider{}).
		WithAdminOperations(newToolAdminService(t))

	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "user-1",
		Access:  testAccess(ToolPolicyReadOnly, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-list-normal",
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
	for _, want := range []string{`"presentation":"capabilities"`, `"name":"answer_from_visible_discord_context"`, `"name":"current_capabilities"`, `"admin_tools_hidden":true`, "Additional admin-only tools exist"} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected normal user tool listing to contain %s, got %s", want, content)
		}
	}
	for _, hidden := range []string{"discord_fetch_message", "native_name", "input_schema", "read_config", "panda_manage_tool_access", "discord_modify_channel_permissions", "admin.config.write", "admin_read", "admin_write"} {
		if strings.Contains(content, hidden) {
			t.Fatalf("normal user tool listing leaked admin detail %q: %s", hidden, content)
		}
	}
}

func TestExecutorListToolsShowsAdminToolsToAdmins(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	executor := NewExecutor(registry, nil, fakeConfigReader{}).
		WithDiscordToolProvider(&fakeDiscordProvider{}).
		WithAdminOperations(newToolAdminService(t))

	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access: testAccess(
			ToolPolicyWriteConfirmed,
			admin.PermissionAssistantUse,
			admin.PermissionAdminConfigRead,
			admin.PermissionAdminConfigWrite,
			admin.PermissionAdminUsageRead,
		),
		Call: llm.ToolCall{
			ID:   "call-list-admin",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_list_tools",
				Arguments: `{}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	content := result.Message.Content
	for _, want := range []string{`"name":"read_config"`, `"name":"panda_manage_tool_access"`, `"tool_class":"admin_write"`} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected admin tool listing to contain %s, got %s", want, content)
		}
	}
	if strings.Contains(content, "admin_tools_hidden") || strings.Contains(content, "super secret admin tools") {
		t.Fatalf("admin tool listing should not include hidden-admin notice: %s", content)
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
