package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/billing"
	contextsvc "github.com/sn0w/panda2/internal/context"
	"github.com/sn0w/panda2/internal/features"
	"github.com/sn0w/panda2/internal/generated"
	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/memory"
	"github.com/sn0w/panda2/internal/pandainfo"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
	"github.com/sn0w/panda2/internal/websearch"
	"github.com/sn0w/panda2/internal/youtube"
)

func TestDefaultRegistryDefinitionsAreValid(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	definitions := registry.Definitions()
	if len(definitions) < 80 {
		t.Fatalf("expected restored feature-gated tool surface, got %d", len(definitions))
	}
	restored := map[string]bool{
		"discord.create_role":          true,
		"discord.add_reaction":         true,
		"discord.send_message":         true,
		"discord.add_member_role":      true,
		"discord.remove_member_role":   true,
		"discord.timeout_member":       true,
		"discord.get_audit_logs":       true,
		"panda.manage_role_permission": true,
		"panda.manage_user_permission": true,
		"panda.manage_member_role":     true,
		"panda.manage_discord_role":    true,
		"panda.manage_tool_access":     true,
		"panda.manage_prompt":          true,
		"panda.manage_quiet_mode":      true,
		"panda.manage_safety":          true,
		"panda.manage_ops":             true,
		"panda.manage_composed_tool":   true,
		"panda.manage_schedule":        true,
		"read_config":                  true,
		"draft_moderator_note":         true,
	}
	for _, definition := range definitions {
		delete(restored, definition.Name)
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
	for name := range restored {
		t.Fatalf("expected restored tool %s to be registered", name)
	}
}

func TestAdminSetupToolSchemasExposeNaturalLanguageFields(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	assertToolSchemaContains := func(toolName string, fields ...string) {
		t.Helper()
		definition, ok := registry.Get(toolName)
		if !ok {
			t.Fatalf("tool %s not registered", toolName)
		}
		schema := string(definition.InputSchema)
		for _, field := range fields {
			if !strings.Contains(schema, `"`+field+`"`) {
				t.Fatalf("tool %s schema missing %q: %s", toolName, field, schema)
			}
		}
	}

	assertToolSchemaContains("panda.manage_role_permission", "profile", "role_name", "role")
	assertToolSchemaContains("panda.manage_user_permission", "profile", "user_id", "member_user_id")
	assertToolSchemaContains("panda.manage_channel_rule", "channel_name", "channel")
	assertToolSchemaContains("panda.manage_tool_access", "tool_name", "tool_group", "role_name", "role", "target_type", "user_id", "member_user_id")
	assertToolSchemaContains("panda.manage_safety", "action", "user_id", "member", "limit")
	assertToolSchemaContains("panda.manage_prompt", "prompt", "instructions")
	assertToolSchemaContains("panda.manage_quiet_mode", "duration_seconds", "duration", "until", "status", "set", "clear")
	assertToolSchemaContains("panda.manage_soul", "soul", "enum", "status", "set", "update")
	assertToolSchemaContains("panda.manage_music", "search", "voice_channel_id", "voice_channel_name", "voice_channel", "vc")
	assertToolSchemaContains("panda.summarize_youtube", "query", "url", "title", "detail", "language")
	assertToolSchemaContains("panda.clip_youtube", "query", "url", "instructions", "aspect_ratio", "9:16", "layout_instructions", "captions", "caption_instructions", "language")
	assertToolSchemaContains("panda.search_youtube", "query", "title", "video", "limit", "purpose", "clip", "instructions", "source", "channel_uploads", "channel_url", "handle", "sort_by", "upload_date", "date_after", "date_before", "uploaded_after")
	assertToolSchemaContains("panda.manage_composed_tool", "voice_channel_id", "voice_channel_name", "voice_channel")
}

func TestComposedToolManagerTimeoutAllowsNaturalDraftLLM(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	definition, ok := registry.Get("panda.manage_composed_tool")
	if !ok {
		t.Fatal("panda.manage_composed_tool not registered")
	}
	if definition.Timeout <= 75*time.Second {
		t.Fatalf("composed tool manager timeout %s must exceed the nested OpenRouter chat timeout", definition.Timeout)
	}
}

func TestYouTubeClipCaptionInstructionsMentionRandomStyledCaptions(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	definition, ok := registry.Get("panda.clip_youtube")
	if !ok {
		t.Fatal("panda.clip_youtube not registered")
	}
	schema := string(definition.InputSchema)
	for _, want := range []string{"random styled captions", "randomized caption style"} {
		if !strings.Contains(schema, want) || !strings.Contains(definition.Description, want) {
			t.Fatalf("expected clip tool schema and description to mention %q, schema=%s description=%s", want, schema, definition.Description)
		}
	}
}

func TestYouTubeClipInstructionsAreOptional(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	definition, ok := registry.Get("panda.clip_youtube")
	if !ok {
		t.Fatal("panda.clip_youtube not registered")
	}
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
		t.Fatalf("decode clip schema: %v", err)
	}
	for _, field := range schema.Required {
		if field == "instructions" {
			t.Fatalf("clip instructions should be optional, required=%+v schema=%s", schema.Required, string(definition.InputSchema))
		}
	}
	if _, ok := schema.Properties["instructions"]; !ok {
		t.Fatalf("clip instructions should remain available as optional guidance: %s", string(definition.InputSchema))
	}
	if !strings.Contains(definition.Description, "default viral clip preset") {
		t.Fatalf("clip tool description should tell the model to use the default viral preset without guidance: %q", definition.Description)
	}
}

func TestImageGenerationSchemaMatchesOpenRouterImageModelSettings(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	definition, ok := registry.Get("panda.generate_image")
	if !ok {
		t.Fatal("panda.generate_image not registered")
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	for _, want := range []string{"prompt", "reference_image_ids", "reference_usage", "caption", "aspect_ratio", "resolution", "count", "filename_hint"} {
		if _, ok := schema.Properties[want]; !ok {
			t.Fatalf("image generation schema missing field %q: %s", want, string(definition.InputSchema))
		}
	}
	for _, legacy := range []string{"size", "quality", "output_format", "transparent_background"} {
		if _, ok := schema.Properties[legacy]; ok {
			t.Fatalf("image generation schema should not expose unsupported legacy field %q: %s", legacy, string(definition.InputSchema))
		}
	}
}

func TestChannelAwareToolDescriptionsPreferDiscordChannelLookup(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	for _, toolName := range []string{"discord.list_channels", "panda.manage_music", "panda.manage_composed_tool"} {
		definition, ok := registry.Get(toolName)
		if !ok {
			t.Fatalf("tool %s not registered", toolName)
		}
		if !strings.Contains(definition.Description, "discord.list_channels") {
			t.Fatalf("tool %s description should point the model at channel lookup: %q", toolName, definition.Description)
		}
	}
	for _, toolName := range []string{"panda.manage_music", "panda.manage_composed_tool"} {
		definition, ok := registry.Get(toolName)
		if !ok {
			t.Fatalf("tool %s not registered", toolName)
		}
		schema := string(definition.InputSchema)
		if !strings.Contains(schema, "Prefer resolving plain") || !strings.Contains(schema, "voice/stage") {
			t.Fatalf("tool %s schema missing named VC lookup guidance: %s", toolName, schema)
		}
	}
	composed, ok := registry.Get("panda.manage_composed_tool")
	if !ok {
		t.Fatal("panda.manage_composed_tool not registered")
	}
	composedSchema := string(composed.InputSchema)
	if !strings.Contains(composed.Description, "Use archive for reversible removal") ||
		!strings.Contains(composed.Description, "Use delete only when the user explicitly asks for permanent deletion") ||
		!strings.Contains(composed.Description, "Use list for explicit questions about installed, existing, current, default, or pre-built composed tools") ||
		!strings.Contains(composedSchema, "Use archive for reversible removal") ||
		!strings.Contains(composedSchema, "Use delete only when the user explicitly asks for permanent deletion") ||
		!strings.Contains(composedSchema, "Use list for exact inventory questions about installed/current/existing/default/pre-built composed tools") {
		t.Fatalf("composed tool should steer inventory listing, removals, and permanent deletion, description=%q schema=%s", composed.Description, composedSchema)
	}
	music, ok := registry.Get("panda.manage_music")
	if !ok {
		t.Fatal("panda.manage_music not registered")
	}
	if !strings.Contains(string(music.InputSchema), "suggestions") {
		t.Fatalf("music schema should explain ok=false suggestions, got %s", string(music.InputSchema))
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
	if !names["discord_fetch_message"] || names["panda_list_tools"] || names["generate_workflow_json"] {
		t.Fatalf("unexpected read-only tools: %+v", names)
	}
	writeTools := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy:      ToolPolicyWriteConfirmed,
		Permissions: map[string]struct{}{admin.PermissionAssistantUse: {}},
	})
	writeNames := toolNames(writeTools)
	for _, want := range []string{"discord_send_message", "discord_create_poll", "discord_add_reaction"} {
		if !writeNames[want] {
			t.Fatalf("write tool %s should be exposed behind write_confirmed policy, got %+v", want, writeNames)
		}
	}
	if writeNames["discord_create_thread"] {
		t.Fatalf("thread creation should require thread permission, got %+v", writeNames)
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
		t.Fatalf("thread creation should be exposed with thread permission, got %+v", threadNames)
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

func TestPandaAboutToolIsCallableForAssistantUsers(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	definition, ok := registry.Get("panda.about")
	if !ok {
		t.Fatal("panda.about not registered")
	}
	if definition.ModelName() != "panda_about" || !definition.IncludeInModelContext || !definition.BypassToolPolicy || !definition.TerminalCard {
		t.Fatalf("unexpected panda.about definition: %+v", definition)
	}
	available := NewExecutor(registry, nil, nil).OpenRouterTools(testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse))
	if !toolNames(available)["panda_about"] {
		t.Fatalf("panda.about should be exposed to assistant users, got %+v", toolNames(available))
	}
}

func TestPandaAboutToolReturnsCardWithButtons(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	result, err := NewExecutor(registry, nil, nil).Execute(context.Background(), ExecutionRequest{
		Access: testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-about",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_about",
				Arguments: `{}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute panda.about: %v", err)
	}
	var payload struct {
		Result struct {
			Title   string `json:"title"`
			Content string `json:"content"`
			Actions []struct {
				Label string `json:"label"`
				URL   string `json:"url"`
			} `json:"actions"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(result.Message.Content), &payload); err != nil {
		t.Fatalf("decode tool payload: %v\n%s", err, result.Message.Content)
	}
	if !strings.Contains(payload.Result.Title, "Discord-native assistant") ||
		!strings.Contains(payload.Result.Content, "I'm open source") ||
		!strings.Contains(payload.Result.Content, "Created by "+pandainfo.CreatorHandle) ||
		strings.Contains(payload.Result.Content, pandainfo.RepositoryURL) ||
		strings.Contains(payload.Result.Content, pandainfo.CreatorURL) {
		t.Fatalf("unexpected about content: %+v", payload.Result)
	}
	if len(payload.Result.Actions) != 2 ||
		payload.Result.Actions[0].Label != "Github" ||
		payload.Result.Actions[0].URL != pandainfo.RepositoryURL ||
		payload.Result.Actions[1].Label != "X" ||
		payload.Result.Actions[1].URL != pandainfo.CreatorURL {
		t.Fatalf("unexpected about actions: %+v", payload.Result.Actions)
	}
}

func TestOpenRouterToolsNormalizesLegacyOffPolicyToAdminOnly(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	tools := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy: "off",
		Permissions: map[string]struct{}{
			admin.PermissionAssistantUse:       {},
			admin.PermissionAdminConfigRead:    {},
			admin.PermissionAdminConfigWrite:   {},
			admin.PermissionAssistantWebSearch: {},
		},
	})
	names := toolNames(tools)
	if names["panda_list_tools"] {
		t.Fatalf("metadata inventory should not be exposed to the response model, got %+v", names)
	}
	for _, want := range []string{"read_config", "panda_manage_tool_access", "web_search"} {
		if !names[want] {
			t.Fatalf("legacy off policy should normalize to admin_only and expose %s to admins, got %+v", want, names)
		}
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
			admin.PermissionAssistantUse:             {},
			admin.PermissionAssistantWebSearch:       {},
			admin.PermissionAssistantImageGeneration: {},
		},
	})
	regularNames := toolNames(regularTools)
	if !regularNames["web_search"] || !regularNames["panda_generate_image"] || !regularNames["panda_inspect_image"] || regularNames["panda_list_tools"] || regularNames["discord_fetch_message"] {
		t.Fatalf("admin_only should expose web search and image media but hide metadata inventory and broader tools from regular users, got %+v", regularNames)
	}

	adminTools := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy: ToolPolicyAdminOnly,
		Permissions: map[string]struct{}{
			admin.PermissionAssistantUse:       {},
			admin.PermissionAssistantWebSearch: {},
			admin.PermissionAdminConfigRead:    {},
			admin.PermissionAdminConfigWrite:   {},
		},
	})
	adminNames := toolNames(adminTools)
	if !adminNames["web_search"] || !adminNames["read_config"] || !adminNames["panda_manage_tool_access"] {
		t.Fatalf("admin_only should expose admin-scoped tool access to admins, got %+v", adminNames)
	}
}

func TestOpenRouterToolsTreatWebSearchAsDefaultFeature(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}

	available := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy: ToolPolicyAdminOnly,
		Permissions: map[string]struct{}{
			admin.PermissionAssistantUse:       {},
			admin.PermissionAssistantWebSearch: {},
		},
		EnabledFeatures: map[string]struct{}{
			features.AssistantChat: {},
		},
		FeatureGateActive: true,
	})
	if !toolNames(available)["web_search"] {
		t.Fatalf("web search should remain available when feature gate omits web_search: %+v", toolNames(available))
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
	if !toolNames(tools)["draft_moderator_note"] {
		t.Fatalf("draft_moderator_note should be exposed for moderation access")
	}
}

func TestOpenRouterToolsLegacyOffPolicyDoesNotSuppressAdminTools(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	tools := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy: "off",
		Permissions: map[string]struct{}{
			admin.PermissionAssistantUse:     {},
			admin.PermissionAdminAuditRead:   {},
			admin.PermissionAdminConfigRead:  {},
			admin.PermissionAdminConfigWrite: {},
			admin.PermissionAdminUsageRead:   {},
		},
	})
	names := toolNames(tools)
	if names["panda_list_tools"] {
		t.Fatalf("metadata inventory should not be exposed under legacy off/admin_only policy, got %+v", names)
	}
	for _, want := range []string{"read_config", "panda_manage_tool_access", "panda_manage_channel_rule", "panda_usage_report", "generate_workflow_json", "discord_modify_channel_permissions", "discord_send_message"} {
		if !names[want] {
			t.Fatalf("legacy off policy should normalize to admin_only and expose %s to admins, got %+v", want, names)
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

	denied := registry.OpenRouterToolsForAccess(ToolAccess{
		Policy:       ToolPolicyReadOnly,
		Permissions:  map[string]struct{}{admin.PermissionAssistantWebSearch: {}},
		AllowedTools: map[string]struct{}{"web.search": {}},
		DeniedTools:  map[string]struct{}{"web.search": {}},
	})
	if toolNames(denied)["web_search"] {
		t.Fatalf("web_search should be hidden when explicitly denied, even if also allowed: %+v", toolNames(denied))
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

func TestRestoredAdminToolsAreKnown(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	for _, name := range []string{
		"read_config",
		"discord_create_role",
		"discord_add_reaction",
		"discord_send_message",
		"panda_manage_role_permission",
		"panda_manage_user_permission",
		"panda_manage_member_role",
		"panda_manage_discord_role",
		"panda_manage_tool_access",
		"panda_manage_composed_tool",
		"panda_manage_schedule",
		"draft_moderator_note",
	} {
		if _, err := registry.MustGet(name); err != nil {
			t.Fatalf("expected restored tool %s to be registered, got %v", name, err)
		}
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
	return store.GuildConfig{GuildID: "guild-1", AssistantEnabled: true, MemoryEnabled: true, ToolPolicy: "read_only", MaxResponseTokens: 900}, true, nil
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

type fakeImageGenerator struct {
	configured bool
	response   llm.ImageGenerationResponse
	err        error
	requests   []llm.ImageGenerationRequest
}

func (f *fakeImageGenerator) Configured() bool {
	return f.configured
}

func (f *fakeImageGenerator) Generate(_ context.Context, request llm.ImageGenerationRequest) (llm.ImageGenerationResponse, error) {
	f.requests = append(f.requests, request)
	return f.response, f.err
}

type fakeImageAnalyzer struct {
	configured bool
	response   llm.ImageAnalysisResponse
	err        error
	requests   []llm.ImageAnalysisRequest
}

func (f *fakeImageAnalyzer) Configured() bool {
	return f.configured
}

func (f *fakeImageAnalyzer) Analyze(_ context.Context, request llm.ImageAnalysisRequest) (llm.ImageAnalysisResponse, error) {
	f.requests = append(f.requests, request)
	return f.response, f.err
}

type fakeGIFFrameExtractor struct {
	frame generated.File
	err   error
	calls []generated.ImageReference
}

func (f *fakeGIFFrameExtractor) ExtractGIFFrame(_ context.Context, reference generated.ImageReference) (generated.File, error) {
	f.calls = append(f.calls, reference)
	if f.err != nil {
		return generated.File{}, f.err
	}
	return f.frame, nil
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

type fakeScheduleManager struct {
	requests []ScheduleManagementRequest
}

func (f *fakeScheduleManager) ManageSchedule(_ context.Context, request ScheduleManagementRequest) (any, error) {
	f.requests = append(f.requests, request)
	return map[string]any{
		"result": map[string]any{
			"action":    request.Action,
			"tool_name": request.ToolName,
			"dry_run":   request.DryRun,
		},
	}, nil
}

type fakeReminderManager struct {
	requests []ReminderManagementRequest
}

func (f *fakeReminderManager) ManageReminder(_ context.Context, request ReminderManagementRequest) (any, error) {
	f.requests = append(f.requests, request)
	return map[string]any{
		"result": map[string]any{
			"action":  request.Action,
			"message": request.Message,
			"dry_run": request.DryRun,
		},
	}, nil
}

type fakeMusicManager struct {
	requests []MusicManagementRequest
}

func (f *fakeMusicManager) ManageMusic(_ context.Context, request MusicManagementRequest) (any, error) {
	f.requests = append(f.requests, request)
	return map[string]any{
		"result": map[string]any{
			"action": request.Action,
			"query":  request.Query,
		},
	}, nil
}

type fakeYouTubeSummarizer struct {
	configured     bool
	clipConfigured bool
	requests       []youtube.SummaryRequest
	searches       []youtube.SearchRequest
	clipRequests   []youtube.ClipRequest
	candidates     []youtube.VideoCandidate
	result         youtube.SummaryResult
	clipResult     youtube.ClipResult
	err            error
	searchErr      error
	clipErr        error
}

func (f *fakeYouTubeSummarizer) Configured() bool {
	return f.configured
}

func (f *fakeYouTubeSummarizer) Summarize(_ context.Context, request youtube.SummaryRequest) (youtube.SummaryResult, error) {
	f.requests = append(f.requests, request)
	return f.result, f.err
}

func (f *fakeYouTubeSummarizer) Search(_ context.Context, request youtube.SearchRequest) ([]youtube.VideoCandidate, error) {
	f.searches = append(f.searches, request)
	return f.candidates, f.searchErr
}

func (f *fakeYouTubeSummarizer) ClipConfigured() bool {
	return f.configured && f.clipConfigured
}

func (f *fakeYouTubeSummarizer) Clip(_ context.Context, request youtube.ClipRequest) (youtube.ClipResult, error) {
	f.clipRequests = append(f.clipRequests, request)
	return f.clipResult, f.clipErr
}

type fakeClipRepository struct {
	clips        []store.YoutubeClip
	usages       []store.YoutubeClipUsage
	reservation  repository.ClipUsageReservation
	reserveErr   error
	createErr    error
	reserveLimit int
}

func (f *fakeClipRepository) Create(_ context.Context, clip store.YoutubeClip) error {
	f.clips = append(f.clips, clip)
	return f.createErr
}

func (f *fakeClipRepository) ReserveDailyUsage(_ context.Context, usage store.YoutubeClipUsage, limit int) (repository.ClipUsageReservation, error) {
	f.usages = append(f.usages, usage)
	f.reserveLimit = limit
	return f.reservation, f.reserveErr
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
	return admin.NewService(configs, usage, audit, memoryService, access, budgets, members)
}

func newToolSafetyRepository(t *testing.T) (*store.Store, *repository.UserSafetyRepository) {
	t.Helper()
	ctx := context.Background()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "safety.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	return dataStore, repository.NewUserSafetyRepository(dataStore.DB)
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
				Arguments: `{"action":"draft","request":"Create a member-join automation","voice_channel_name":"bot-test","dry_run":true}`,
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
	if request.Action != "draft" || request.Text != "Create a member-join automation" || request.VoiceChannelName != "bot-test" || !request.DryRun {
		t.Fatalf("unexpected composed manager request: %+v", request)
	}
	if !strings.Contains(result.Message.Content, `"action":"draft"`) || !strings.Contains(result.Message.Content, `"dry_run":true`) {
		t.Fatalf("unexpected composed manager result: %+v", result)
	}
}

func TestExecutorArchivesComposedToolForRemoveAlias(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	manager := &fakeComposedManager{}
	executor := NewExecutor(registry, nil, nil).WithComposedToolManager(manager)
	_, err = executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionToolComposeApprove),
		Call: llm.ToolCall{
			ID:   "call-composed-manager",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_composed_tool",
				Arguments: `{"action":"remove","tool_name":"play_song_on_voice_join"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(manager.requests) != 1 || manager.requests[0].Action != "archive" || manager.requests[0].ToolName != "play_song_on_voice_join" {
		t.Fatalf("expected remove alias to archive composed tool, got %+v", manager.requests)
	}

	_, err = executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionToolComposeApprove),
		Call: llm.ToolCall{
			ID:   "call-composed-manager-delete",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_composed_tool",
				Arguments: `{"action":"permanent_delete","tool_name":"play_song_on_voice_join"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute permanent delete: %v", err)
	}
	if len(manager.requests) != 2 || manager.requests[1].Action != "delete" || manager.requests[1].ToolName != "play_song_on_voice_join" {
		t.Fatalf("expected explicit permanent delete alias to delete composed tool, got %+v", manager.requests)
	}
}

func TestExecutorRunsScheduleManager(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	manager := &fakeScheduleManager{}
	executor := NewExecutor(registry, nil, nil).WithScheduleManager(manager)
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		ActorID:   "admin",
		Access:    testAccess(ToolPolicyWriteConfirmed, admin.PermissionToolComposeInvoke),
		Call: llm.ToolCall{
			ID:   "call-schedule-manager",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_schedule",
				Arguments: `{"action":"schedule","tool_name":"welcome_builder","when":"in 10 minutes","every":"daily","input":{"topic":"standup"},"dry_run":true}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(manager.requests) != 1 {
		t.Fatalf("expected schedule manager request, got %d", len(manager.requests))
	}
	request := manager.requests[0]
	if request.Action != "create" || request.ToolName != "welcome_builder" || request.When != "in 10 minutes" || request.Every != "daily" || !request.DryRun {
		t.Fatalf("unexpected schedule manager request: %+v", request)
	}
	if request.Input["topic"] != "standup" {
		t.Fatalf("unexpected schedule input: %+v", request.Input)
	}
	if !strings.Contains(result.Message.Content, `"action":"create"`) || !strings.Contains(result.Message.Content, `"dry_run":true`) {
		t.Fatalf("unexpected schedule manager result: %+v", result)
	}
}

func TestExecutorExposesScheduleManagerToInvokeAccess(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	withoutManager := NewExecutor(registry, nil, nil)
	if toolNames(withoutManager.OpenRouterTools(testAccess(ToolPolicyWriteConfirmed, admin.PermissionToolComposeInvoke)))["panda_manage_schedule"] {
		t.Fatalf("schedule manager tool should be hidden when no manager is configured")
	}
	withManager := NewExecutor(registry, nil, nil).WithScheduleManager(&fakeScheduleManager{})
	if !toolNames(withManager.OpenRouterTools(testAccess(ToolPolicyWriteConfirmed, admin.PermissionToolComposeInvoke)))["panda_manage_schedule"] {
		t.Fatalf("schedule manager tool should be available to composed-tool invoke access")
	}
}

func TestExecutorRunsReminderManager(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	manager := &fakeReminderManager{}
	executor := NewExecutor(registry, nil, nil).WithReminderManager(manager)
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		ActorID:   "user-1",
		RequestID: "message-1",
		Access:    testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-reminder-manager",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_reminder",
				Arguments: `{"action":"remind","message":"stand up","in":"10 minutes","target":"me","follow_up":true,"dry_run":true}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(manager.requests) != 1 {
		t.Fatalf("expected reminder manager request, got %d", len(manager.requests))
	}
	request := manager.requests[0]
	if request.Action != "create" || request.Message != "stand up" || request.In != "10 minutes" || request.Target != "me" || !request.FollowUp || !request.DryRun {
		t.Fatalf("unexpected reminder manager request: %+v", request)
	}
	if request.GuildID != "guild-1" || request.ChannelID != "channel-1" || request.ActorID != "user-1" || request.RequestID != "message-1" {
		t.Fatalf("missing reminder execution context: %+v", request)
	}
	if !strings.Contains(result.Message.Content, `"action":"create"`) || !strings.Contains(result.Message.Content, `"dry_run":true`) {
		t.Fatalf("unexpected reminder manager result: %+v", result)
	}
}

func TestExecutorExposesReminderManagerToAssistantUse(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse)
	withoutManager := NewExecutor(registry, nil, nil)
	if toolNames(withoutManager.OpenRouterTools(access))["panda_manage_reminder"] {
		t.Fatalf("reminder manager tool should be hidden when no manager is configured")
	}
	withManager := NewExecutor(registry, nil, nil).WithReminderManager(&fakeReminderManager{})
	if !toolNames(withManager.OpenRouterTools(access))["panda_manage_reminder"] {
		t.Fatalf("reminder manager tool should be available to assistant use")
	}
}

func TestExecutorRunsMusicManager(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	manager := &fakeMusicManager{}
	executor := NewExecutor(registry, nil, nil).WithMusicManager(manager)
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "text-1",
		VoiceChannelID: "voice-1",
		ActorID:        "user-1",
		RoleIDs:        []string{"role-dj"},
		IsGuildAdmin:   true,
		Access:         testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-music-manager",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_music",
				Arguments: `{"action":"add","song":"lofi rain","mode":"all","position":2,"to":1,"volume":45}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(manager.requests) != 1 {
		t.Fatalf("expected music manager request, got %d", len(manager.requests))
	}
	request := manager.requests[0]
	if request.Action != "play" || request.Query != "lofi rain" || request.Mode != "all" || request.Position != 2 || request.To != 1 || request.Volume != 45 {
		t.Fatalf("unexpected music manager request: %+v", request)
	}
	if request.VoiceChannelID != "voice-1" || len(request.RoleIDs) != 1 || request.RoleIDs[0] != "role-dj" || !request.IsGuildAdmin {
		t.Fatalf("missing music execution context: %+v", request)
	}
	if !strings.Contains(result.Message.Content, `"action":"play"`) || !strings.Contains(result.Message.Content, `"query":"lofi rain"`) {
		t.Fatalf("unexpected music manager result: %+v", result)
	}
}

func TestExecutorRunsMusicManagerWithExplicitVoiceChannelID(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	manager := &fakeMusicManager{}
	executor := NewExecutor(registry, nil, nil).WithMusicManager(manager)

	_, err = executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "text-1",
		VoiceChannelID: "",
		ActorID:        "user-1",
		Access:         testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-music-manager",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_music",
				Arguments: `{"action":"play","query":"ocean drive","voice_channel_id":"<#100000000000000222>"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(manager.requests) != 1 {
		t.Fatalf("expected music manager request, got %d", len(manager.requests))
	}
	if manager.requests[0].VoiceChannelID != "100000000000000222" {
		t.Fatalf("expected explicit voice channel target, got %+v", manager.requests[0])
	}
}

func TestExecutorRunsMusicManagerWithExplicitVoiceChannelName(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	manager := &fakeMusicManager{}
	provider := &fakeDiscordProvider{result: map[string]any{"channels": []map[string]any{
		{"id": "100000000000000111", "name": "Lounge", "type": "0"},
		{"id": "100000000000000222", "name": "Lounge", "type": "2"},
		{"id": "100000000000000333", "name": "Stage", "type": "13"},
	}}}
	executor := NewExecutor(registry, nil, nil).
		WithDiscordToolProvider(provider).
		WithMusicManager(manager)

	_, err = executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "text-1",
		VoiceChannelID: "100000000000000999",
		ActorID:        "user-1",
		Access:         testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-music-manager",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_music",
				Arguments: `{"action":"play","query":"ocean drive","voice_channel_name":"lounge"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(manager.requests) != 1 {
		t.Fatalf("expected music manager request, got %d", len(manager.requests))
	}
	if manager.requests[0].VoiceChannelID != "100000000000000222" {
		t.Fatalf("expected resolved voice channel target, got %+v", manager.requests[0])
	}
	if len(provider.requests) != 1 || provider.requests[0].ToolName != "discord.list_channels" {
		t.Fatalf("expected Discord channel lookup, got %+v", provider.requests)
	}
}

func TestExecutorAllowsTrialMusicPlayback(t *testing.T) {
	ctx := context.Background()
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "billing.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	billingService := billing.NewService(repository.NewBillingRepository(dataStore.DB), billing.Config{})
	trialStart := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	billingService.SetClock(func() time.Time { return trialStart.Add(time.Hour) })
	if _, err := billingService.EnsureTrial(ctx, billing.TrialSeed{
		GuildID:            "guild-1",
		BillingOwnerUserID: "owner-1",
		AuthorizedAt:       trialStart,
	}); err != nil {
		t.Fatalf("EnsureTrial: %v", err)
	}

	manager := &fakeMusicManager{}
	executor := NewExecutor(registry, nil, nil).WithMusicManager(manager).WithBilling(billingService)
	_, err = executor.Execute(ctx, ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "text-1",
		VoiceChannelID: "voice-1",
		ActorID:        "user-1",
		Access:         testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-trial-music",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_music",
				Arguments: `{"action":"play","query":"fill my pockets by mgk"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("trial music playback should be allowed, got %v", err)
	}
	if len(manager.requests) != 1 || manager.requests[0].Action != "play" {
		t.Fatalf("expected music manager to receive trial playback request, got %+v", manager.requests)
	}
}

func TestExecutorExposesMusicManagerToAssistantUse(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse)
	withoutManager := NewExecutor(registry, nil, nil)
	if toolNames(withoutManager.OpenRouterTools(access))["panda_manage_music"] {
		t.Fatalf("music manager tool should be hidden when no manager is configured")
	}
	withManager := NewExecutor(registry, nil, nil).WithMusicManager(&fakeMusicManager{})
	if !toolNames(withManager.OpenRouterTools(access))["panda_manage_music"] {
		t.Fatalf("music manager tool should be available to assistant use")
	}
}

func TestExecutorRunsYouTubeSummarizer(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	summarizer := &fakeYouTubeSummarizer{
		configured: true,
		result: youtube.SummaryResult{
			Title:         "Deep Dive",
			URL:           "https://www.youtube.com/watch?v=deep",
			Uploader:      "Teacher",
			Duration:      12 * time.Minute,
			ResolvedQuery: "deep dive",
			Transcript:    "First point.\n\nSecond point.",
			ChunkCount:    2,
		},
	}
	executor := NewExecutor(registry, nil, nil).WithYouTubeSummarizer(summarizer)
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:   "guild-1",
		ChannelID: "text-1",
		ActorID:   "user-1",
		Access:    testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-youtube",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_summarize_youtube",
				Arguments: `{"query":"deep dive","detail":"detailed","language":"en"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(summarizer.requests) != 1 {
		t.Fatalf("expected one YouTube summary request, got %d", len(summarizer.requests))
	}
	request := summarizer.requests[0]
	if request.Query != "deep dive" || request.Detail != "detailed" || request.Language != "en" {
		t.Fatalf("unexpected YouTube request: %+v", request)
	}
	content := result.Message.Content
	for _, want := range []string{`"title":"Deep Dive"`, `"plain_text_transcript":"First point.\n\nSecond point."`, `"chunk_count":2`, `"transcript_truncated":false`} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected result content to contain %s, got %s", want, content)
		}
	}
	if len(result.SourceLinks) != 1 || result.SourceLinks[0].URL != "https://www.youtube.com/watch?v=deep" {
		t.Fatalf("expected YouTube source link, got %+v", result.SourceLinks)
	}
}

func TestExecutorRunsYouTubeClipper(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	summarizer := &fakeYouTubeSummarizer{
		configured:     true,
		clipConfigured: true,
		clipResult: youtube.ClipResult{
			Title:                  "Deep Dive",
			URL:                    "https://www.youtube.com/watch?v=deep",
			Uploader:               "Teacher",
			Duration:               12 * time.Minute,
			TranscriptSegmentCount: 4,
			Clips: []youtube.RenderedClip{
				{
					Rank:               1,
					Title:              "Best explanation",
					Type:               "spliced",
					WatchURL:           "https://cdn.example.test/clips/guild-1/request-1/01-best-explanation.mp4",
					ObjectKey:          "clips/guild-1/request-1/01-best-explanation.mp4",
					ThumbnailURL:       "https://cdn.example.test/clips/guild-1/request-1/01-best-explanation.jpg",
					ThumbnailObjectKey: "clips/guild-1/request-1/01-best-explanation.jpg",
					Duration:           33 * time.Second,
					SourceStartSeconds: 42,
					SourceEndSeconds:   90,
					Segments: []youtube.RenderedClipSegment{
						{StartSeconds: 42, EndSeconds: 58, Duration: 16 * time.Second, Transcript: "First point."},
						{StartSeconds: 73, EndSeconds: 90, Duration: 17 * time.Second, Transcript: "Second point."},
					},
					Reason:                   "This segment answers the requested question.",
					Confidence:               0.91,
					ViralityScore:            82,
					HookScore:                80,
					RetentionScore:           83,
					ShareabilityScore:        81,
					DurationPolicy:           "target_30_45",
					ExceptionReason:          "",
					OutputSizeBytes:          12345,
					AspectRatio:              "9:16",
					LayoutMode:               "stacked_regions",
					CompositionReason:        "Main content and facecam are stacked for vertical viewing.",
					CompositionConfidence:    0.87,
					CaptionRendered:          true,
					CaptionMode:              "burned_in",
					CaptionStylePreset:       "opus_bold",
					CaptionStyleSource:       "user_specified",
					CaptionAnimation:         "slide_up",
					CaptionTimingQuality:     "word",
					CaptionConfidence:        0.88,
					CaptionReason:            "Bottom captions avoid the webcam.",
					CaptionFontFamily:        "inter",
					CaptionFontColor:         "white",
					CaptionHighlightColor:    "yellow",
					CaptionBorderColor:       "green",
					CaptionBorderThickness:   "medium",
					CaptionBackgroundColor:   "transparent",
					CaptionBackgroundOpacity: 0,
				},
				{
					Rank:               2,
					Title:              "Short answer",
					Type:               "continuous",
					WatchURL:           "https://cdn.example.test/clips/guild-1/request-1/02-short-answer.mp4",
					ObjectKey:          "clips/guild-1/request-1/02-short-answer.mp4",
					ThumbnailURL:       "https://cdn.example.test/clips/guild-1/request-1/02-short-answer.jpg",
					ThumbnailObjectKey: "clips/guild-1/request-1/02-short-answer.jpg",
					Duration:           15 * time.Second,
					SourceStartSeconds: 50,
					SourceEndSeconds:   65,
					Segments: []youtube.RenderedClipSegment{
						{StartSeconds: 50, EndSeconds: 65, Duration: 15 * time.Second, Transcript: "Short answer."},
					},
					Reason:                   "This is a tighter soundbite.",
					Confidence:               0.82,
					ViralityScore:            76,
					HookScore:                78,
					RetentionScore:           75,
					ShareabilityScore:        77,
					DurationPolicy:           "short_exception",
					ExceptionReason:          "Standalone answer works as a quick clip.",
					OutputSizeBytes:          6789,
					CaptionRendered:          true,
					CaptionMode:              "burned_in",
					CaptionStylePreset:       "opus_bold",
					CaptionStyleSource:       "user_specified",
					CaptionAnimation:         "pop",
					CaptionTimingQuality:     "word",
					CaptionConfidence:        0.83,
					CaptionReason:            "Captions stay in a safe lower band.",
					CaptionFontFamily:        "inter",
					CaptionFontColor:         "white",
					CaptionHighlightColor:    "yellow",
					CaptionBorderColor:       "green",
					CaptionBorderThickness:   "medium",
					CaptionBackgroundColor:   "transparent",
					CaptionBackgroundOpacity: 0,
				},
			},
		},
	}
	executor := NewExecutor(registry, nil, nil).WithYouTubeSummarizer(summarizer)
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:   "guild-1",
		ChannelID: "text-1",
		ActorID:   "user-1",
		RequestID: "request-1",
		Access:    testAccess(ToolPolicyAssistive, admin.PermissionAssistantYouTubeClipping),
		Call: llm.ToolCall{
			ID:   "call-youtube-clip",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_clip_youtube",
				Arguments: `{"query":"https://www.youtube.com/watch?v=deep","instructions":"clip the best explanation","aspect_ratio":"9:16","layout_instructions":"keep the webcam visible","captions":"on","caption_instructions":"big captions at the top; do not cover the webcam","language":"en"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(summarizer.clipRequests) != 1 {
		t.Fatalf("expected one YouTube clip request, got %d", len(summarizer.clipRequests))
	}
	request := summarizer.clipRequests[0]
	if request.Query != "https://www.youtube.com/watch?v=deep" || request.Instructions != "clip the best explanation" || request.AspectRatio != "9:16" || request.LayoutInstructions != "keep the webcam visible" || request.Captions != "on" || request.CaptionInstructions != "big captions at the top; do not cover the webcam" || request.Language != "en" || request.GuildID != "guild-1" || request.RequestID != "request-1" {
		t.Fatalf("unexpected YouTube clip request: %+v", request)
	}
	if !result.Terminal {
		t.Fatalf("expected YouTube clip result to be terminal")
	}
	content := result.Message.Content
	for _, want := range []string{`"terminal":true`, `"content":"1. [Best explanation]`, `"media":[`, `"thumbnail_url":"https://cdn.example.test/clips/guild-1/request-1/01-best-explanation.jpg"`, `"thumbnail_object_key":"[redacted]"`, `"actions":[`, `"label":"1. Best explanation"`, `"clip_count":2`, `"clips":[`, `"type":"spliced"`, `"watch_url":"https://cdn.example.test/clips/guild-1/request-1/01-best-explanation.mp4"`, `"source_start_seconds":42`, `"source_end_seconds":90`, `"segments":[`, `"start_seconds":42`, `"end_seconds":58`, `"transcript_segment_count":4`, `"virality_score":82`, `"aspect_ratio":"9:16"`, `"layout_mode":"stacked_regions"`, `"caption_rendered":true`, `"caption_mode":"burned_in"`, `"caption_style_source":"user_specified"`, `"caption_animation":"slide_up"`, `"caption_timing_quality":"word"`, `"caption_reason":"Bottom captions avoid the webcam."`, `"caption_font_family":"inter"`, `"caption_font_color":"white"`, `"caption_border_color":"green"`, `"caption_border_thickness":"medium"`} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected clip result content to contain %s, got %s", want, content)
		}
	}
	for _, leaked := range []string{"plain_text_transcript"} {
		if strings.Contains(content, leaked) {
			t.Fatalf("clip tool message leaked transcript detail %q: %s", leaked, content)
		}
	}
}

func TestExecutorBlocksYouTubeClipperAfterDailyBetaLimit(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	summarizer := &fakeYouTubeSummarizer{
		configured:     true,
		clipConfigured: true,
	}
	clips := &fakeClipRepository{
		reservation: repository.ClipUsageReservation{Reserved: false, Used: 3, Limit: 3},
	}
	executor := NewExecutor(registry, nil, nil).
		WithYouTubeSummarizer(summarizer).
		WithClipRepository(clips)
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:   "guild-1",
		ChannelID: "text-1",
		ActorID:   "user-1",
		RequestID: "request-limit",
		Access:    testAccess(ToolPolicyAssistive, admin.PermissionAssistantYouTubeClipping),
		Call: llm.ToolCall{
			ID:   "call-youtube-clip",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_clip_youtube",
				Arguments: `{"query":"https://www.youtube.com/watch?v=deep"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(summarizer.clipRequests) != 0 {
		t.Fatalf("clipper should not run after quota is exhausted, got %d request(s)", len(summarizer.clipRequests))
	}
	if len(clips.usages) != 1 || clips.usages[0].UserID != "user-1" || clips.usages[0].GuildID != "guild-1" || clips.usages[0].RequestID != "request-limit" || clips.reserveLimit != youtubeClipDailyBetaLimit {
		t.Fatalf("unexpected quota reservation: usages=%+v limit=%d", clips.usages, clips.reserveLimit)
	}
	if !result.Terminal {
		t.Fatalf("expected quota response to be terminal")
	}
	for _, want := range []string{`"title":"Daily clip limit reached"`, `"accent":"warning"`, `"clip_daily_limit":3`, `"clip_daily_used":3`, "Clip generation is in beta"} {
		if !strings.Contains(result.Message.Content, want) {
			t.Fatalf("expected quota content to contain %s, got %s", want, result.Message.Content)
		}
	}
}

func TestExecutorSkipsYouTubeClipDailyBetaLimitForBotOwner(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	summarizer := &fakeYouTubeSummarizer{
		configured:     true,
		clipConfigured: true,
		clipResult: youtube.ClipResult{
			Title:    "Deep Dive",
			URL:      "https://www.youtube.com/watch?v=deep",
			Uploader: "Teacher",
			Duration: time.Minute,
			Clips: []youtube.RenderedClip{{
				Rank:               1,
				Title:              "Best Moment",
				Type:               "continuous",
				WatchURL:           "https://cdn.example.test/clips/guild-1/request-owner/01-best-moment.mp4",
				ObjectKey:          "clips/guild-1/request-owner/01-best-moment.mp4",
				Duration:           20 * time.Second,
				SourceStartSeconds: 10,
				SourceEndSeconds:   30,
				Segments: []youtube.RenderedClipSegment{{
					StartSeconds: 10,
					EndSeconds:   30,
					Duration:     20 * time.Second,
					Transcript:   "This is the best moment.",
				}},
				Reason:         "Strong standalone hook.",
				Confidence:     0.8,
				ViralityScore:  82,
				DurationPolicy: "short_exception",
			}},
		},
	}
	clips := &fakeClipRepository{
		reservation: repository.ClipUsageReservation{Reserved: false, Used: 3, Limit: 3},
	}
	executor := NewExecutor(registry, nil, nil).
		WithYouTubeSummarizer(summarizer).
		WithClipRepository(clips)
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:   "guild-1",
		ChannelID: "text-1",
		ActorID:   "owner-1",
		RequestID: "request-owner",
		IsOwner:   true,
		Access:    testAccess(ToolPolicyAssistive, admin.PermissionAssistantYouTubeClipping),
		Call: llm.ToolCall{
			ID:   "call-youtube-clip",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_clip_youtube",
				Arguments: `{"query":"https://www.youtube.com/watch?v=deep"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(clips.usages) != 0 {
		t.Fatalf("bot owner should not reserve daily clip usage, got %+v", clips.usages)
	}
	if len(summarizer.clipRequests) != 1 {
		t.Fatalf("expected owner clip request to run, got %d request(s)", len(summarizer.clipRequests))
	}
	if !result.Terminal || strings.Contains(result.Message.Content, "Daily clip limit reached") {
		t.Fatalf("expected owner clip result instead of limit response, got terminal=%t content=%s", result.Terminal, result.Message.Content)
	}
}

func TestExecutorRunsYouTubeClipperWithoutGuidance(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	summarizer := &fakeYouTubeSummarizer{
		configured:     true,
		clipConfigured: true,
		clipResult: youtube.ClipResult{
			Title:    "Deep Dive",
			URL:      "https://www.youtube.com/watch?v=deep",
			Uploader: "Teacher",
			Duration: time.Minute,
			Clips: []youtube.RenderedClip{
				{
					Rank:               1,
					Title:              "Best Moment",
					Type:               "continuous",
					WatchURL:           "https://cdn.example.test/clips/guild-1/request-1/01-best-moment.mp4",
					ObjectKey:          "clips/guild-1/request-1/01-best-moment.mp4",
					ThumbnailURL:       "https://cdn.example.test/clips/guild-1/request-1/01-best-moment.jpg",
					Duration:           20 * time.Second,
					SourceStartSeconds: 10,
					SourceEndSeconds:   30,
					Segments: []youtube.RenderedClipSegment{{
						StartSeconds: 10,
						EndSeconds:   30,
						Duration:     20 * time.Second,
						Transcript:   "This is the best moment.",
					}},
					Reason:            "Strong standalone hook.",
					Confidence:        0.8,
					ViralityScore:     82,
					HookScore:         85,
					RetentionScore:    80,
					ShareabilityScore: 81,
					DurationPolicy:    "short_exception",
				},
				{
					Rank:               2,
					Title:              "Second Moment",
					Type:               "continuous",
					WatchURL:           "https://cdn.example.test/clips/guild-1/request-1/02-second-moment.mp4",
					ObjectKey:          "clips/guild-1/request-1/02-second-moment.mp4",
					Duration:           15 * time.Second,
					SourceStartSeconds: 35,
					SourceEndSeconds:   50,
					Segments: []youtube.RenderedClipSegment{{
						StartSeconds: 35,
						EndSeconds:   50,
						Duration:     15 * time.Second,
						Transcript:   "Another strong moment.",
					}},
					Reason:            "Second standalone hook.",
					Confidence:        0.76,
					ViralityScore:     78,
					HookScore:         80,
					RetentionScore:    76,
					ShareabilityScore: 77,
					DurationPolicy:    "short_exception",
				},
			},
		},
	}
	executor := NewExecutor(registry, nil, nil).WithYouTubeSummarizer(summarizer)
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:   "guild-1",
		ChannelID: "text-1",
		ActorID:   "user-1",
		RequestID: "request-1",
		Access:    testAccess(ToolPolicyAssistive, admin.PermissionAssistantYouTubeClipping),
		Call: llm.ToolCall{
			ID:   "call-youtube-clip",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_clip_youtube",
				Arguments: `{"query":"https://www.youtube.com/watch?v=deep"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(summarizer.clipRequests) != 1 {
		t.Fatalf("expected one YouTube clip request, got %d", len(summarizer.clipRequests))
	}
	request := summarizer.clipRequests[0]
	if request.Query != "https://www.youtube.com/watch?v=deep" || request.Instructions != "" || request.GuildID != "guild-1" || request.RequestID != "request-1" {
		t.Fatalf("unexpected YouTube clip request: %+v", request)
	}
	if !result.Terminal {
		t.Fatalf("expected YouTube clip result without guidance to be terminal")
	}
	if !strings.Contains(result.Message.Content, `"clip_count":2`) || !strings.Contains(result.Message.Content, `"terminal":true`) || !strings.Contains(result.Message.Content, `"watch_url":"https://cdn.example.test/clips/guild-1/request-1/01-best-moment.mp4"`) || !strings.Contains(result.Message.Content, `"watch_url":"https://cdn.example.test/clips/guild-1/request-1/02-second-moment.mp4"`) {
		t.Fatalf("expected clip result content, got %s", result.Message.Content)
	}
}

func TestExecutorSearchesYouTubeForSelection(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	summarizer := &fakeYouTubeSummarizer{
		configured: true,
		candidates: []youtube.VideoCandidate{
			{
				Title:        "First Result",
				URL:          "https://www.youtube.com/watch?v=one",
				Uploader:     "Creator",
				ThumbnailURL: "https://i.ytimg.com/vi/one/hqdefault.jpg",
				Duration:     2*time.Minute + 4*time.Second,
				UploadDate:   time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC),
			},
			{
				Title:    "Second Result",
				URL:      "https://www.youtube.com/watch?v=two",
				Uploader: "Other Creator",
			},
		},
	}
	executor := NewExecutor(registry, nil, nil).WithYouTubeSummarizer(summarizer)
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:   "guild-1",
		ChannelID: "text-1",
		ActorID:   "user-1",
		Access:    testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-youtube-search",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_search_youtube",
				Arguments: `{"query":"deep dive","limit":1,"source":"channel_uploads","channel_url":"https://www.youtube.com/@creator","handle":"@creator","sort_by":"upload_date","date_after":"2026-01-01","date_before":"2026-02-01"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Terminal {
		t.Fatalf("expected YouTube search selection result to be terminal")
	}
	if result.Payload == nil {
		t.Fatalf("expected YouTube search selection to preserve structured payload")
	}
	if len(summarizer.searches) != 1 ||
		summarizer.searches[0].Query != "deep dive" ||
		summarizer.searches[0].Limit != 3 ||
		summarizer.searches[0].Source != "channel_uploads" ||
		summarizer.searches[0].ChannelURL != "https://www.youtube.com/@creator" ||
		summarizer.searches[0].Handle != "@creator" ||
		summarizer.searches[0].SortBy != "upload_date" ||
		summarizer.searches[0].DateAfter != "2026-01-01" ||
		summarizer.searches[0].DateBefore != "2026-02-01" {
		t.Fatalf("unexpected YouTube search request: %+v", summarizer.searches)
	}
	var payload struct {
		Result struct {
			Title     string `json:"title"`
			Terminal  bool   `json:"terminal"`
			Selection struct {
				Options []struct {
					Label        string `json:"label"`
					Description  string `json:"description"`
					URL          string `json:"url"`
					ThumbnailURL string `json:"thumbnail_url"`
					Prompt       string `json:"prompt"`
				} `json:"options"`
			} `json:"selection"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(result.Message.Content), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v\n%s", err, result.Message.Content)
	}
	if payload.Result.Title != "Choose a YouTube video" || !payload.Result.Terminal || len(payload.Result.Selection.Options) != 2 {
		t.Fatalf("unexpected selection payload: %+v", payload.Result)
	}
	first := payload.Result.Selection.Options[0]
	if first.Label != "First Result" || first.URL != "https://www.youtube.com/watch?v=one" || first.ThumbnailURL == "" || !strings.Contains(first.Prompt, "https://www.youtube.com/watch?v=one") || !strings.Contains(first.Description, "2026-01-30") {
		t.Fatalf("unexpected first option: %+v", first)
	}
}

func TestExecutorSearchesYouTubeForClipSelection(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	summarizer := &fakeYouTubeSummarizer{
		configured: true,
		candidates: []youtube.VideoCandidate{{
			Title: "First Result",
			URL:   "https://www.youtube.com/watch?v=one",
		}},
	}
	executor := NewExecutor(registry, nil, nil).WithYouTubeSummarizer(summarizer)
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:   "guild-1",
		ChannelID: "text-1",
		ActorID:   "user-1",
		Access:    testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-youtube-clip-search",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_search_youtube",
				Arguments: `{"query":"deep dive","purpose":"clip","instructions":"clip the part about structured outputs"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Result struct {
			Content   string `json:"content"`
			Selection struct {
				Options []struct {
					Prompt string `json:"prompt"`
				} `json:"options"`
			} `json:"selection"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(result.Message.Content), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v\n%s", err, result.Message.Content)
	}
	if !strings.Contains(payload.Result.Content, "clip") {
		t.Fatalf("expected clip selection content, got %q", payload.Result.Content)
	}
	if len(payload.Result.Selection.Options) != 1 || !strings.Contains(payload.Result.Selection.Options[0].Prompt, "Clip this exact YouTube video") || !strings.Contains(payload.Result.Selection.Options[0].Prompt, "structured outputs") {
		t.Fatalf("unexpected clip selection prompt: %+v", payload.Result.Selection.Options)
	}
}

func TestExecutorExposesYouTubeSummarizerOnlyWhenConfigured(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantUse)
	withoutSummarizer := NewExecutor(registry, nil, nil)
	if names := toolNames(withoutSummarizer.OpenRouterTools(access)); names["panda_summarize_youtube"] || names["panda_search_youtube"] {
		t.Fatalf("youtube summary tool should be hidden when no summarizer is configured")
	}
	withUnconfiguredSummarizer := NewExecutor(registry, nil, nil).WithYouTubeSummarizer(&fakeYouTubeSummarizer{})
	if names := toolNames(withUnconfiguredSummarizer.OpenRouterTools(access)); names["panda_summarize_youtube"] || names["panda_search_youtube"] {
		t.Fatalf("youtube summary tool should be hidden when Lemonfox is not configured")
	}
	withSummarizer := NewExecutor(registry, nil, nil).WithYouTubeSummarizer(&fakeYouTubeSummarizer{configured: true})
	names := toolNames(withSummarizer.OpenRouterTools(access))
	if !names["panda_summarize_youtube"] || !names["panda_search_youtube"] {
		t.Fatalf("youtube summary/search tools should be available to assistant users, got %+v", names)
	}
}

func TestExecutorExposesYouTubeClipperOnlyWhenConfigured(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantYouTubeClipping)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.YouTubeClipping: {}}

	withoutClipper := NewExecutor(registry, nil, nil).WithYouTubeSummarizer(&fakeYouTubeSummarizer{configured: true})
	if names := toolNames(withoutClipper.OpenRouterTools(access)); names["panda_clip_youtube"] {
		t.Fatalf("youtube clip tool should be hidden when clip runtime is unconfigured")
	}
	withClipper := NewExecutor(registry, nil, nil).WithYouTubeSummarizer(&fakeYouTubeSummarizer{configured: true, clipConfigured: true})
	if names := toolNames(withClipper.OpenRouterTools(access)); !names["panda_clip_youtube"] {
		t.Fatalf("youtube clip tool should be available with feature, permission, and runtime configured, got %+v", names)
	}
}

func TestExecutorExposesImageMediaOnlyWhenConfigured(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantImageGeneration)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.ImageGeneration: {}}

	withoutGenerator := NewExecutor(registry, nil, nil)
	if names := toolNames(withoutGenerator.OpenRouterTools(access)); names["panda_generate_image"] || names["panda_inspect_image"] {
		t.Fatalf("image media tools should be hidden when no image runtime is configured, got %+v", names)
	}
	withUnconfiguredGenerator := NewExecutor(registry, nil, nil).WithImageGenerator(&fakeImageGenerator{})
	if names := toolNames(withUnconfiguredGenerator.OpenRouterTools(access)); names["panda_generate_image"] || names["panda_inspect_image"] {
		t.Fatalf("image media tools should be hidden when the image runtime is unconfigured, got %+v", names)
	}
	withGenerator := NewExecutor(registry, nil, nil).WithImageGenerator(&fakeImageGenerator{configured: true})
	if names := toolNames(withGenerator.OpenRouterTools(access)); !names["panda_generate_image"] || names["panda_inspect_image"] {
		t.Fatalf("image generation tool should be available with feature, permission, and runtime configured")
	}
	withAnalyzer := NewExecutor(registry, nil, nil).WithImageAnalyzer(&fakeImageAnalyzer{configured: true})
	if names := toolNames(withAnalyzer.OpenRouterTools(access)); names["panda_generate_image"] || !names["panda_inspect_image"] {
		t.Fatalf("image inspection tool should be independently available with analyzer runtime configured, got %+v", names)
	}
	withBoth := NewExecutor(registry, nil, nil).
		WithImageGenerator(&fakeImageGenerator{configured: true}).
		WithImageAnalyzer(&fakeImageAnalyzer{configured: true})
	if names := toolNames(withBoth.OpenRouterTools(access)); !names["panda_generate_image"] || !names["panda_inspect_image"] {
		t.Fatalf("both image media tools should be available with both runtimes configured, got %+v", names)
	}
}

func TestExecutorGenerateImageCarriesFilesWithoutLeakingBytes(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	imageBytes := []byte("secret-image-bytes")
	generator := &fakeImageGenerator{
		configured: true,
		response: llm.ImageGenerationResponse{
			Model: "provider/image-model",
			Images: []llm.GeneratedImage{{
				Bytes:    imageBytes,
				MIMEType: "image/png",
			}},
		},
	}
	executor := NewExecutor(registry, nil, nil).WithImageGenerator(generator)
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantImageGeneration)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.ImageGeneration: {}}
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		ActorID:        "user-1",
		RequestID:      "request-1",
		InvocationType: "chat_tool",
		ImageReferences: []generated.ImageReference{{
			ID:       "current:100",
			Filename: "reference.png",
			MIMEType: "image/png",
			URL:      "https://cdn.example.test/reference.png",
		}},
		Access: access,
		Call: llm.ToolCall{
			ID:   "call-image",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda.generate_image",
				Arguments: `{"prompt":"pixel panda icon","reference_image_ids":["current:100"],"caption":"Panda icon","resolution":"1K","filename_hint":"panda icon"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(generator.requests) != 1 || generator.requests[0].Prompt != "pixel panda icon" || generator.requests[0].Resolution != "1K" {
		t.Fatalf("unexpected generator requests: %+v", generator.requests)
	}
	if len(generator.requests[0].InputReferences) != 1 || generator.requests[0].InputReferences[0].URL != "https://cdn.example.test/reference.png" {
		t.Fatalf("expected selected input reference, got %+v", generator.requests[0].InputReferences)
	}
	if len(result.GeneratedFiles) != 1 {
		t.Fatalf("expected generated file, got %+v", result.GeneratedFiles)
	}
	if result.GeneratedFiles[0].Filename != "panda-icon.png" || string(result.GeneratedFiles[0].Data) != string(imageBytes) {
		t.Fatalf("unexpected generated file: %+v", result.GeneratedFiles[0])
	}
	if strings.Contains(result.Message.Content, string(imageBytes)) || strings.Contains(result.Message.Content, base64.StdEncoding.EncodeToString(imageBytes)) {
		t.Fatalf("tool message leaked image bytes: %s", result.Message.Content)
	}
	if !strings.Contains(result.Message.Content, `"generated":true`) || !strings.Contains(result.Message.Content, `"filename":"panda-icon.png"`) {
		t.Fatalf("tool message should expose compact metadata, got %s", result.Message.Content)
	}
}

func TestExecutorGenerateImageExtractsFrameForGIFReferences(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	generator := &fakeImageGenerator{
		configured: true,
		response: llm.ImageGenerationResponse{
			Images: []llm.GeneratedImage{{
				Bytes:    []byte("generated-image"),
				MIMEType: "image/png",
			}},
		},
	}
	extractor := &fakeGIFFrameExtractor{frame: generated.File{MIMEType: "image/png", Data: []byte("gif-frame")}}
	executor := NewExecutor(registry, nil, nil).
		WithImageGenerator(generator).
		WithGIFFrameExtractor(extractor)
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantImageGeneration)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.ImageGeneration: {}}

	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		ActorID:        "user-1",
		RequestID:      "request-1",
		InvocationType: "chat_tool",
		ImageReferences: []generated.ImageReference{{
			ID:       "reply:gif",
			Filename: "reaction.gif",
			MIMEType: "image/gif",
			URL:      "https://cdn.example.test/reaction.gif",
		}},
		Access: access,
		Call: llm.ToolCall{
			ID:   "call-image",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda.generate_image",
				Arguments: `{"prompt":"make a meme","reference_image_ids":["reply:gif"],"filename_hint":"meme"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(extractor.calls) != 1 || extractor.calls[0].ID != "reply:gif" {
		t.Fatalf("expected gif extractor to receive selected reference, got %+v", extractor.calls)
	}
	if len(generator.requests) != 1 || len(generator.requests[0].InputReferences) != 1 {
		t.Fatalf("expected one generator request with one input reference, got %+v", generator.requests)
	}
	wantDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("gif-frame"))
	if got := generator.requests[0].InputReferences[0].URL; got != wantDataURL {
		t.Fatalf("expected extracted frame data URL, got %q", got)
	}
	if strings.Contains(generator.requests[0].InputReferences[0].URL, "reaction.gif") {
		t.Fatalf("generator should not receive original GIF URL: %+v", generator.requests[0].InputReferences)
	}
	if !strings.Contains(result.Message.Content, `"generated":true`) {
		t.Fatalf("expected generated payload, got %s", result.Message.Content)
	}
}

func TestExecutorGenerateImageExtractsFrameForVideoBackedGIFReferences(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	generator := &fakeImageGenerator{
		configured: true,
		response: llm.ImageGenerationResponse{
			Images: []llm.GeneratedImage{{
				Bytes:    []byte("generated-image"),
				MIMEType: "image/png",
			}},
		},
	}
	extractor := &fakeGIFFrameExtractor{frame: generated.File{MIMEType: "image/png", Data: []byte("video-frame")}}
	executor := NewExecutor(registry, nil, nil).
		WithImageGenerator(generator).
		WithGIFFrameExtractor(extractor)
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantImageGeneration)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.ImageGeneration: {}}

	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		ActorID:        "user-1",
		RequestID:      "request-1",
		InvocationType: "chat_tool",
		ImageReferences: []generated.ImageReference{{
			ID:       "reply_embed_1",
			Filename: "reaction.mp4",
			MIMEType: "video/mp4",
			URL:      "https://media.tenor.example/reaction.mp4",
		}},
		Access: access,
		Call: llm.ToolCall{
			ID:   "call-image",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda.generate_image",
				Arguments: `{"prompt":"make a meme","reference_image_ids":["reply_embed_1"],"filename_hint":"meme"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(extractor.calls) != 1 || extractor.calls[0].ID != "reply_embed_1" {
		t.Fatalf("expected frame extractor to receive selected video reference, got %+v", extractor.calls)
	}
	wantDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("video-frame"))
	if len(generator.requests) != 1 || len(generator.requests[0].InputReferences) != 1 || generator.requests[0].InputReferences[0].URL != wantDataURL {
		t.Fatalf("expected generated request to use extracted frame, got %+v", generator.requests)
	}
	if !strings.Contains(result.Message.Content, `"generated":true`) {
		t.Fatalf("expected generated payload, got %s", result.Message.Content)
	}
}

func TestExecutorGenerateImageRejectsPromptWithInternalRoutingMetadata(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	generator := &fakeImageGenerator{configured: true}
	executor := NewExecutor(registry, nil, nil).WithImageGenerator(generator)
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantImageGeneration)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.ImageGeneration: {}}

	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		ActorID:        "user-1",
		RequestID:      "request-1",
		InvocationType: "chat_tool",
		ImageReferences: []generated.ImageReference{{
			ID:       "reply_embed_1",
			Filename: "reaction.mp4",
			MIMEType: "video/mp4",
			URL:      "https://media.tenor.example/reaction.mp4",
		}},
		Access: access,
		Call: llm.ToolCall{
			ID:   "call-image",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda.generate_image",
				Arguments: `{"prompt":"Make a meme about a video-backed Discord GIF and the still PNG frame extraction.","reference_image_ids":["reply_embed_1"],"filename_hint":"meme"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(generator.requests) != 0 {
		t.Fatalf("prompt with internal routing metadata should not reach provider, got %+v", generator.requests)
	}
	if !strings.Contains(result.Message.Content, `"provider_status":"invalid_request"`) || !strings.Contains(result.Message.Content, `"retryable":true`) {
		t.Fatalf("expected retryable invalid prompt payload, got %s", result.Message.Content)
	}
}

func TestExecutorGenerateImageRejectsPromptWithAssistantRequestMeta(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	generator := &fakeImageGenerator{configured: true}
	executor := NewExecutor(registry, nil, nil).WithImageGenerator(generator)
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantImageGeneration)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.ImageGeneration: {}}

	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		ActorID:        "user-1",
		RequestID:      "request-1",
		InvocationType: "chat_tool",
		ImageReferences: []generated.ImageReference{{
			ID:       "reply:100",
			Filename: "cat.png",
			MIMEType: "image/png",
			URL:      "https://cdn.example.test/cat.png",
		}},
		Access: access,
		Call: llm.ToolCall{
			ID:   "call-image",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda.generate_image",
				Arguments: `{"prompt":"A meme with top text \"WHEN YOU ASK PANDA TO MAKE A MEME\" and bottom text \"AND IT ACTUALLY DOES!\"","reference_image_ids":["reply:100"],"reference_usage":"use_available","filename_hint":"meme"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(generator.requests) != 0 {
		t.Fatalf("prompt with assistant request meta should not reach provider, got %+v", generator.requests)
	}
	for _, want := range []string{`"provider_status":"invalid_request"`, `"retryable":true`, "clean visual prompt", "referenced image content"} {
		if !strings.Contains(result.Message.Content, want) {
			t.Fatalf("expected retryable assistant-meta prompt payload to contain %q, got %s", want, result.Message.Content)
		}
	}
	if strings.Contains(result.Message.Content, "https://cdn.example.test") || strings.Contains(result.Message.Content, "cat.png") {
		t.Fatalf("prompt repair payload leaked media metadata: %s", result.Message.Content)
	}
}

func TestExecutorGenerateImageRequiresReferenceDecisionWhenRefsAvailable(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	generator := &fakeImageGenerator{configured: true}
	executor := NewExecutor(registry, nil, nil).WithImageGenerator(generator)
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantImageGeneration)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.ImageGeneration: {}}

	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		ActorID:        "user-1",
		RequestID:      "request-1",
		InvocationType: "chat_tool",
		ImageReferences: []generated.ImageReference{{
			ID:       "reply:100",
			Filename: "cat.png",
			MIMEType: "image/png",
			URL:      "https://cdn.example.test/cat.png",
		}},
		Access: access,
		Call: llm.ToolCall{
			ID:   "call-image",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda.generate_image",
				Arguments: `{"prompt":"Create a humorous meme from the referenced image.","filename_hint":"meme"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(generator.requests) != 0 {
		t.Fatalf("missing image reference decision should fail before provider call, got %+v", generator.requests)
	}
	for _, want := range []string{`"provider_status":"invalid_request"`, `"retryable":true`, `"reference_decision_required":true`, `"reply:100"`, "reference_image_ids", "intentionally_unrelated"} {
		if !strings.Contains(result.Message.Content, want) {
			t.Fatalf("expected missing-reference repair payload to contain %q, got %s", want, result.Message.Content)
		}
	}
	if strings.Contains(result.Message.Content, "https://cdn.example.test") || strings.Contains(result.Message.Content, "cat.png") {
		t.Fatalf("reference repair payload leaked media metadata: %s", result.Message.Content)
	}
}

func TestExecutorGenerateImageAllowsExplicitUnrelatedGenerationWhenRefsAvailable(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	generator := &fakeImageGenerator{
		configured: true,
		response: llm.ImageGenerationResponse{
			Images: []llm.GeneratedImage{{
				Bytes:    []byte("image-bytes"),
				MIMEType: "image/png",
			}},
		},
	}
	executor := NewExecutor(registry, nil, nil).WithImageGenerator(generator)
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantImageGeneration)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.ImageGeneration: {}}

	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		ActorID:        "user-1",
		RequestID:      "request-1",
		InvocationType: "chat_tool",
		ImageReferences: []generated.ImageReference{{
			ID:       "reply:100",
			Filename: "cat.png",
			MIMEType: "image/png",
			URL:      "https://cdn.example.test/cat.png",
		}},
		Access: access,
		Call: llm.ToolCall{
			ID:   "call-image",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda.generate_image",
				Arguments: `{"prompt":"Create an original panda meme unrelated to the attached image.","reference_usage":"intentionally_unrelated","filename_hint":"meme"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(generator.requests) != 1 {
		t.Fatalf("expected explicit unrelated generation to reach provider, got %+v", generator.requests)
	}
	if len(generator.requests[0].InputReferences) != 0 {
		t.Fatalf("explicit unrelated generation should not pass input references, got %+v", generator.requests[0].InputReferences)
	}
	if !strings.Contains(result.Message.Content, `"generated":true`) {
		t.Fatalf("expected generated payload, got %s", result.Message.Content)
	}
}

func TestExecutorGenerateImageRejectsGIFReferenceWithoutExtractor(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	generator := &fakeImageGenerator{configured: true}
	executor := NewExecutor(registry, nil, nil).WithImageGenerator(generator)
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantImageGeneration)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.ImageGeneration: {}}

	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		ActorID:        "user-1",
		RequestID:      "request-1",
		InvocationType: "chat_tool",
		ImageReferences: []generated.ImageReference{{
			ID:       "reply:gif",
			Filename: "reaction.gif",
			MIMEType: "image/gif",
			URL:      "https://cdn.example.test/reaction.gif",
		}},
		Access: access,
		Call: llm.ToolCall{
			ID:   "call-image",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda.generate_image",
				Arguments: `{"prompt":"make a meme","reference_image_ids":["reply:gif"]}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(generator.requests) != 0 {
		t.Fatalf("GIF without extractor should fail before provider call, got %+v", generator.requests)
	}
	if !strings.Contains(result.Message.Content, `"provider_status":"invalid_request"`) || !strings.Contains(result.Message.Content, "needs preprocessing") {
		t.Fatalf("expected safe GIF extraction failure, got %s", result.Message.Content)
	}
}

func TestExecutorGenerateImageRejectsUnknownReferenceID(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	generator := &fakeImageGenerator{configured: true}
	executor := NewExecutor(registry, nil, nil).WithImageGenerator(generator)
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantImageGeneration)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.ImageGeneration: {}}
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		ActorID:        "user-1",
		RequestID:      "request-1",
		InvocationType: "chat_tool",
		Access:         access,
		Call: llm.ToolCall{
			ID:   "call-image",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda.generate_image",
				Arguments: `{"prompt":"pixel panda icon","reference_image_ids":["current:missing"]}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(generator.requests) != 0 {
		t.Fatalf("unknown references should fail before provider call, got %+v", generator.requests)
	}
	if !strings.Contains(result.Message.Content, `"provider_status":"invalid_request"`) || !strings.Contains(result.Message.Content, "attach the image again") {
		t.Fatalf("expected safe invalid reference response, got %s", result.Message.Content)
	}
}

func TestExecutorGenerateImageDoesNotForwardLegacyUnsupportedSettings(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	generator := &fakeImageGenerator{
		configured: true,
		response: llm.ImageGenerationResponse{
			Images: []llm.GeneratedImage{{
				Bytes:    []byte("image-bytes"),
				MIMEType: "image/png",
			}},
		},
	}
	executor := NewExecutor(registry, nil, nil).WithImageGenerator(generator)
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantImageGeneration)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.ImageGeneration: {}}
	_, err = executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		ActorID:        "user-1",
		RequestID:      "request-1",
		InvocationType: "chat_tool",
		Access:         access,
		Call: llm.ToolCall{
			ID:   "call-image",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda.generate_image",
				Arguments: `{"prompt":"random meme","quality":"high","output_format":"png","transparent_background":true,"size":"1024x1024"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(generator.requests) != 1 {
		t.Fatalf("expected generator call, got %+v", generator.requests)
	}
	request := generator.requests[0]
	if request.Quality != "" || request.OutputFormat != "" || request.TransparentBackground || request.Size != "" {
		t.Fatalf("legacy unsupported settings should not be forwarded: %+v", request)
	}
}

func TestExecutorInspectImageUsesSelectedReferences(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	analyzer := &fakeImageAnalyzer{
		configured: true,
		response: llm.ImageAnalysisResponse{
			Model:   "provider/image-model",
			Content: "The image shows a small panda icon.",
			Usage:   llm.ImageUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
	}
	executor := NewExecutor(registry, nil, nil).WithImageAnalyzer(analyzer)
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantImageGeneration)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.ImageGeneration: {}}
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		ActorID:        "user-1",
		RequestID:      "request-1",
		InvocationType: "chat_tool",
		ImageReferences: []generated.ImageReference{{
			ID:       "current:100",
			Filename: "reference.png",
			MIMEType: "image/png",
			URL:      "https://cdn.example.test/reference.png",
		}},
		Access: access,
		Call: llm.ToolCall{
			ID:   "call-inspect",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda.inspect_image",
				Arguments: `{"question":"What is in this image?","reference_image_ids":["current:100"],"detail":"brief"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(analyzer.requests) != 1 {
		t.Fatalf("expected one analyzer request, got %+v", analyzer.requests)
	}
	if len(analyzer.requests[0].InputReferences) != 1 || analyzer.requests[0].InputReferences[0].URL != "https://cdn.example.test/reference.png" {
		t.Fatalf("expected selected input reference, got %+v", analyzer.requests[0].InputReferences)
	}
	if !strings.Contains(analyzer.requests[0].Prompt, "What is in this image?") || analyzer.requests[0].MaxTokens != 300 {
		t.Fatalf("unexpected analyzer prompt/options: %+v", analyzer.requests[0])
	}
	if strings.Contains(result.Message.Content, "https://cdn.example.test/reference.png") {
		t.Fatalf("tool message leaked image reference URL: %s", result.Message.Content)
	}
	if !strings.Contains(result.Message.Content, `"analyzed":true`) || !strings.Contains(result.Message.Content, "small panda icon") {
		t.Fatalf("tool message should expose safe analysis, got %s", result.Message.Content)
	}
}

func TestExecutorInspectImageExtractsFrameForGIFReferences(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	analyzer := &fakeImageAnalyzer{
		configured: true,
		response: llm.ImageAnalysisResponse{
			Model:   "provider/image-model",
			Content: "The extracted frame shows a surprised cat.",
		},
	}
	extractor := &fakeGIFFrameExtractor{frame: generated.File{MIMEType: "image/png", Data: []byte("gif-frame")}}
	executor := NewExecutor(registry, nil, nil).
		WithImageAnalyzer(analyzer).
		WithGIFFrameExtractor(extractor)
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantImageGeneration)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.ImageGeneration: {}}

	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		ActorID:        "user-1",
		RequestID:      "request-1",
		InvocationType: "chat_tool",
		ImageReferences: []generated.ImageReference{{
			ID:       "reply:gif",
			Filename: "reaction.gif",
			MIMEType: "image/gif",
			URL:      "https://cdn.example.test/reaction.gif",
		}},
		Access: access,
		Call: llm.ToolCall{
			ID:   "call-inspect",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda.inspect_image",
				Arguments: `{"question":"What is in this image?","reference_image_ids":["reply:gif"],"detail":"brief"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(extractor.calls) != 1 || extractor.calls[0].ID != "reply:gif" {
		t.Fatalf("expected gif extractor call, got %+v", extractor.calls)
	}
	if len(analyzer.requests) != 1 || len(analyzer.requests[0].InputReferences) != 1 {
		t.Fatalf("expected one analyzer request with one input reference, got %+v", analyzer.requests)
	}
	wantDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("gif-frame"))
	if got := analyzer.requests[0].InputReferences[0].URL; got != wantDataURL {
		t.Fatalf("expected extracted frame data URL, got %q", got)
	}
	if strings.Contains(result.Message.Content, "reaction.gif") || strings.Contains(result.Message.Content, wantDataURL) {
		t.Fatalf("tool message leaked GIF input details: %s", result.Message.Content)
	}
	if !strings.Contains(result.Message.Content, `"analyzed":true`) || !strings.Contains(result.Message.Content, "surprised cat") {
		t.Fatalf("expected analysis payload, got %s", result.Message.Content)
	}
}

func TestExecutorInspectImageRejectsUnknownReferenceID(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	analyzer := &fakeImageAnalyzer{configured: true}
	executor := NewExecutor(registry, nil, nil).WithImageAnalyzer(analyzer)
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantImageGeneration)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.ImageGeneration: {}}
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		ActorID:        "user-1",
		RequestID:      "request-1",
		InvocationType: "chat_tool",
		Access:         access,
		Call: llm.ToolCall{
			ID:   "call-inspect",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda.inspect_image",
				Arguments: `{"question":"What is in this image?","reference_image_ids":["current:missing"]}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(analyzer.requests) != 0 {
		t.Fatalf("unknown references should fail before provider call, got %+v", analyzer.requests)
	}
	if !strings.Contains(result.Message.Content, `"provider_status":"invalid_request"`) || !strings.Contains(result.Message.Content, "attach the image again") {
		t.Fatalf("expected safe invalid reference response, got %s", result.Message.Content)
	}
}

func TestExecutorGenerateImageReturnsSafePolicyFailure(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	generator := &fakeImageGenerator{
		configured: true,
		err: llm.ImageGenerationError{
			Status:  llm.ImageProviderStatusPolicyBlocked,
			Message: "I could not generate that image because the request was blocked by the image provider's safety policy. Try a safer revision.",
		},
	}
	executor := NewExecutor(registry, nil, nil).WithImageGenerator(generator)
	access := testAccess(ToolPolicyAssistive, admin.PermissionAssistantImageGeneration)
	access.FeatureGateActive = true
	access.EnabledFeatures = map[string]struct{}{features.ImageGeneration: {}}
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		ActorID:        "user-1",
		RequestID:      "request-1",
		InvocationType: "chat_tool",
		Access:         access,
		Call: llm.ToolCall{
			ID:   "call-image-policy",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda.generate_image",
				Arguments: `{"prompt":"unsafe visual prompt"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.GeneratedFiles) != 0 {
		t.Fatalf("policy failures should not carry files: %+v", result.GeneratedFiles)
	}
	if !strings.Contains(result.Message.Content, `"provider_status":"policy_blocked"`) ||
		!strings.Contains(result.Message.Content, "safer revision") {
		t.Fatalf("expected safe policy response, got %s", result.Message.Content)
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
	if !toolNames(tools)["panda_manage_composed_tool"] {
		t.Fatalf("invoke-only access should expose composed tool runner, got %+v", toolNames(tools))
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
	if message.Role != "tool" || message.ToolCallID != "call-web" || !strings.Contains(message.Content, "sqlite.org") || strings.Contains(strings.ToLower(message.Content), "brave") {
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

	longContent := strings.Repeat("x", 600)
	result, err = executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		Access:  access,
		Call: llm.ToolCall{
			ID:   "call-send",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "discord_send_message",
				Arguments: `{"channel_id":"channel-1","content":"` + longContent + `"}`,
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
	if strings.Contains(message.Content, longContent) {
		t.Fatalf("tool transcript should not include full confirmed content: %s", message.Content)
	}
	if result.Confirmation == nil || result.Confirmation.Action != "discord_write.execute" {
		t.Fatalf("expected generic Discord write confirmation, got %+v", result.Confirmation)
	}
	if result.Confirmation.Arguments["tool_name"] != "discord.send_message" || !strings.Contains(result.Confirmation.Arguments["arguments_json"], longContent) {
		t.Fatalf("confirmation should preserve full tool arguments, got %+v", result.Confirmation.Arguments)
	}

	_, err = executor.Execute(context.Background(), ExecutionRequest{
		GuildID:              "guild-1",
		Access:               access,
		AllowConfirmedWrites: true,
		Call: llm.ToolCall{
			ID:   "call-send-confirmed",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "discord_send_message",
				Arguments: result.Confirmation.Arguments["arguments_json"],
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute confirmed write: %v", err)
	}
	if provider.calls != 1 || provider.requests[0].ToolName != "discord.send_message" || provider.requests[0].Arguments["content"] != longContent {
		t.Fatalf("confirmed write should execute original tool arguments: calls=%d requests=%+v", provider.calls, provider.requests)
	}
}

func TestExecutorRawCreateRoleRendersConfirmationArtifact(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	provider := &fakeDiscordProvider{}
	executor := NewExecutor(registry, nil, nil).WithDiscordToolProvider(provider)
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-create-role",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "discord_create_role",
				Arguments: `{"name":"test"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute create role: %v", err)
	}
	if result.Confirmation == nil || result.Confirmation.Action != "discord_role.create" || result.Confirmation.Arguments["name"] != "test" {
		t.Fatalf("expected create-role confirmation artifact, got %+v", result.Confirmation)
	}
	if provider.calls != 0 {
		t.Fatalf("raw create role should not call provider before confirmation, got %d call(s)", provider.calls)
	}
}

func TestExecutorCreatePollRendersConfirmationArtifact(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	provider := &fakeDiscordProvider{}
	executor := NewExecutor(registry, nil, nil).WithDiscordToolProvider(provider)
	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAssistantUse),
		Call: llm.ToolCall{
			ID:   "call-create-poll",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "discord_create_poll",
				Arguments: `{"channel_id":"channel-1","question":"Pick one?","answers":["Red","Blue"],"dry_run":true}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute create poll: %v", err)
	}
	if result.Confirmation == nil || result.Confirmation.Action != "discord_poll.create" || result.Confirmation.Arguments["question"] != "Pick one?" {
		t.Fatalf("expected poll confirmation artifact, got %+v", result.Confirmation)
	}
	if !strings.Contains(result.Confirmation.Arguments["answers_json"], "Red") || result.Confirmation.Danger {
		t.Fatalf("unexpected poll confirmation artifact: %+v", result.Confirmation)
	}
	if provider.calls != 0 {
		t.Fatalf("raw create poll should not call provider before confirmation, got %d call(s)", provider.calls)
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
	confirmedPermissions := strings.Join(provider.requests[0].Permissions, ",")
	if !strings.Contains(confirmedPermissions, "CREATE_PRIVATE_THREADS") || strings.Contains(confirmedPermissions, "CREATE_PUBLIC_THREADS") {
		t.Fatalf("confirmed private thread should use private permission, got %+v", provider.requests[0].Permissions)
	}
}

func TestExecutorReadsUserSafetyStatus(t *testing.T) {
	ctx := context.Background()
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	_, safety := newToolSafetyRepository(t)
	if _, err := safety.AddStrike(ctx, "guild-1", "100000000000000222", 3, 10*time.Minute, time.Date(2026, time.June, 25, 18, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("AddStrike: %v", err)
	}
	executor := NewExecutor(registry, nil, nil).WithUserSafetyRepository(safety)
	access := testAccess(ToolPolicyAdminOnly, admin.PermissionAdminAuditRead)
	names := toolNames(executor.OpenRouterTools(access))
	if !names["panda_manage_safety"] {
		t.Fatalf("expected safety status tool to be exposed, got %+v", names)
	}
	moderatorNames := toolNames(executor.OpenRouterTools(testAccess(ToolPolicyModerator, admin.PermissionModerationUse)))
	if !moderatorNames["panda_manage_safety"] {
		t.Fatalf("expected safety status tool to be exposed for moderators, got %+v", moderatorNames)
	}

	result, err := executor.Execute(ctx, ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin-1",
		Access:  access,
		Call: llm.ToolCall{
			ID:   "call-safety-status",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_safety",
				Arguments: `{"action":"status","user":"<@100000000000000222>"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute safety status: %v", err)
	}
	for _, want := range []string{`"user_id":"100000000000000222"`, `"active_strikes":1`, `"total_strikes":1`, `"timed_out":false`} {
		if !strings.Contains(result.Message.Content, want) {
			t.Fatalf("expected safety status payload %s, got %s", want, result.Message.Content)
		}
	}

	missing, err := executor.Execute(ctx, ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin-1",
		Access:  access,
		Call: llm.ToolCall{
			ID:   "call-missing-safety-status",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_safety",
				Arguments: `{"action":"status","user_id":"100000000000000333"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute missing safety status: %v", err)
	}
	for _, want := range []string{`"user_id":"100000000000000333"`, `"active_strikes":0`, `"total_strikes":0`} {
		if !strings.Contains(missing.Message.Content, want) {
			t.Fatalf("expected empty safety status payload %s, got %s", want, missing.Message.Content)
		}
	}
}

func TestExecutorPreparesAndConfirmsUserSafetyRemoval(t *testing.T) {
	ctx := context.Background()
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	_, safety := newToolSafetyRepository(t)
	if _, err := safety.AddStrike(ctx, "guild-1", "100000000000000222", 3, 10*time.Minute, time.Date(2026, time.June, 25, 18, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("AddStrike: %v", err)
	}
	executor := NewExecutor(registry, nil, nil).WithUserSafetyRepository(safety)

	readOnly, err := executor.Execute(ctx, ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin-1",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminAuditRead),
		Call: llm.ToolCall{
			ID:   "call-read-only-safety-remove",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_safety",
				Arguments: `{"action":"remove","user_id":"100000000000000222"}`,
			},
		},
	})
	if err != nil || readOnly.Confirmation != nil || !strings.Contains(readOnly.Message.Content, admin.PermissionAdminConfigWrite) {
		t.Fatalf("expected safety removal to require admin config write, result=%+v err=%v", readOnly, err)
	}

	prepared, err := executor.Execute(ctx, ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin-1",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-safety-remove",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_safety",
				Arguments: `{"action":"remove","user":"<@100000000000000222>"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Prepare safety remove: %v", err)
	}
	if prepared.Confirmation == nil || prepared.Confirmation.Action != "safety.remove" || prepared.Confirmation.Arguments["user_id"] != "100000000000000222" || prepared.Confirmation.Arguments["count"] != "1" {
		t.Fatalf("expected safety removal confirmation, got %+v", prepared.Confirmation)
	}
	status, err := safety.Status(ctx, "guild-1", "100000000000000222", time.Now().UTC())
	if err != nil {
		t.Fatalf("Status before confirmed remove: %v", err)
	}
	if status.State.ActiveStrikes != 1 || status.State.TotalStrikes != 1 {
		t.Fatalf("prepare should not mutate safety state, got %+v", status)
	}

	confirmed, err := executor.Execute(ctx, ExecutionRequest{
		GuildID:              "guild-1",
		ActorID:              "admin-1",
		Access:               testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		AllowConfirmedWrites: true,
		Call: llm.ToolCall{
			ID:   "call-confirmed-safety-remove",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_safety",
				Arguments: `{"action":"remove","user_id":"100000000000000222"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Confirmed safety remove: %v", err)
	}
	for _, want := range []string{`"removed_strikes":1`, `"active_strikes":0`, `"total_strikes":0`} {
		if !strings.Contains(confirmed.Message.Content, want) {
			t.Fatalf("expected confirmed safety payload %s, got %s", want, confirmed.Message.Content)
		}
	}
}

func TestExecutorPreparesAndConfirmsManualUserSafetyTimeout(t *testing.T) {
	ctx := context.Background()
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	_, safety := newToolSafetyRepository(t)
	executor := NewExecutor(registry, nil, nil).WithUserSafetyRepository(safety)

	prepared, err := executor.Execute(ctx, ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin-1",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-safety-timeout",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_safety",
				Arguments: `{"action":"timeout","user_id":"100000000000000222","duration":"30 minutes"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Prepare safety timeout: %v", err)
	}
	if prepared.Confirmation == nil || prepared.Confirmation.Action != "safety.timeout" || prepared.Confirmation.Arguments["user_id"] != "100000000000000222" || prepared.Confirmation.Arguments["duration_seconds"] != "1800" {
		t.Fatalf("expected safety timeout confirmation, got %+v", prepared.Confirmation)
	}

	confirmed, err := executor.Execute(ctx, ExecutionRequest{
		GuildID:              "guild-1",
		ActorID:              "admin-1",
		Access:               testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		AllowConfirmedWrites: true,
		Call: llm.ToolCall{
			ID:   "call-confirmed-safety-timeout",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_safety",
				Arguments: `{"action":"timeout","user_id":"100000000000000222","duration_seconds":1800}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Confirmed safety timeout: %v", err)
	}
	for _, want := range []string{`"action":"safety.timeout"`, `"duration_seconds":1800`, `"timed_out":true`} {
		if !strings.Contains(confirmed.Message.Content, want) {
			t.Fatalf("expected confirmed safety timeout payload %s, got %s", want, confirmed.Message.Content)
		}
	}
	status, err := safety.Status(ctx, "guild-1", "100000000000000222", time.Now().UTC())
	if err != nil {
		t.Fatalf("Status after confirmed timeout: %v", err)
	}
	if !status.TimedOut || status.State.TimeoutUntil == nil {
		t.Fatalf("expected safety timeout to be active, got %+v", status)
	}
}

func TestExecutorPreparesConfirmsAndClearsQuietMode(t *testing.T) {
	ctx := context.Background()
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	adminOps := newToolAdminService(t)
	executor := NewExecutor(registry, nil, nil).WithAdminOperations(adminOps)
	access := testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite)

	prepared, err := executor.Execute(ctx, ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin-1",
		Access:  access,
		Call: llm.ToolCall{
			ID:   "call-quiet-set",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_quiet_mode",
				Arguments: `{"action":"set","duration":"30 minutes"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Prepare quiet mode: %v", err)
	}
	if prepared.Confirmation == nil || prepared.Confirmation.Action != "quiet_mode.set" || prepared.Confirmation.Arguments["duration_seconds"] != "1800" || strings.TrimSpace(prepared.Confirmation.Arguments["timeout_until"]) == "" {
		t.Fatalf("expected quiet mode confirmation, got %+v", prepared.Confirmation)
	}
	status, err := adminOps.QuietModeStatus(ctx, "guild-1", time.Now().UTC())
	if err != nil {
		t.Fatalf("QuietModeStatus before confirmation: %v", err)
	}
	if status.Active {
		t.Fatalf("prepare should not activate quiet mode, got %+v", status)
	}

	confirmed, err := executor.Execute(ctx, ExecutionRequest{
		GuildID:              "guild-1",
		ActorID:              "admin-1",
		Access:               access,
		AllowConfirmedWrites: true,
		Call: llm.ToolCall{
			ID:   "call-confirmed-quiet-set",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_quiet_mode",
				Arguments: `{"action":"set","duration_seconds":1800}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Confirm quiet mode: %v", err)
	}
	for _, want := range []string{`"action":"quiet_mode.set"`, `"active":true`, `"remaining_seconds"`} {
		if !strings.Contains(confirmed.Message.Content, want) {
			t.Fatalf("expected confirmed quiet payload %s, got %s", want, confirmed.Message.Content)
		}
	}
	status, err = adminOps.QuietModeStatus(ctx, "guild-1", time.Now().UTC())
	if err != nil {
		t.Fatalf("QuietModeStatus after confirmation: %v", err)
	}
	if !status.Active || status.Until == nil {
		t.Fatalf("expected quiet mode to be active, got %+v", status)
	}

	clearPreview, err := executor.Execute(ctx, ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin-1",
		Access:  access,
		Call: llm.ToolCall{
			ID:   "call-quiet-clear",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_quiet_mode",
				Arguments: `{"action":"clear"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Prepare quiet clear: %v", err)
	}
	if clearPreview.Confirmation == nil || clearPreview.Confirmation.Action != "quiet_mode.clear" {
		t.Fatalf("expected quiet clear confirmation, got %+v", clearPreview.Confirmation)
	}

	_, err = executor.Execute(ctx, ExecutionRequest{
		GuildID:              "guild-1",
		ActorID:              "admin-1",
		Access:               access,
		AllowConfirmedWrites: true,
		Call: llm.ToolCall{
			ID:   "call-confirmed-quiet-clear",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_quiet_mode",
				Arguments: `{"action":"clear"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Confirm quiet clear: %v", err)
	}
	status, err = adminOps.QuietModeStatus(ctx, "guild-1", time.Now().UTC())
	if err != nil {
		t.Fatalf("QuietModeStatus after clear: %v", err)
	}
	if status.Active || status.Until != nil {
		t.Fatalf("expected quiet mode to be clear, got %+v", status)
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

	prompt, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-prompt",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_prompt",
				Arguments: `{"action":"set","prompt":"Prefer release-check context before answering."}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute prompt update: %v", err)
	}
	if !strings.Contains(prompt.Message.Content, "release-check context") {
		t.Fatalf("unexpected prompt result: %+v", prompt)
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
	if !strings.Contains(toolAccess.Message.Content, `"confirmation_required":true`) || !strings.Contains(toolAccess.Message.Content, "web.search") || strings.Contains(toolAccess.Message.Content, "role-search") {
		t.Fatalf("unexpected tool access confirmation result: %+v", toolAccess)
	}
	if toolAccess.Confirmation == nil || toolAccess.Confirmation.Action != "tool_access.add" || toolAccess.Confirmation.Arguments["tool_name"] != "web.search" || toolAccess.Confirmation.Arguments["role_id"] != "role-search" {
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
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
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

	userProfile, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-user-profile",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_user_permission",
				Arguments: `{"action":"add","profile":"admin","user":"<@!100000000000000888>"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute user profile: %v", err)
	}
	if userProfile.Confirmation == nil || userProfile.Confirmation.Action != "user_profile.add" || userProfile.Confirmation.Arguments["profile"] != "admin" || userProfile.Confirmation.Arguments["user_id"] != "100000000000000888" {
		t.Fatalf("expected user profile confirmation, got %+v", userProfile.Confirmation)
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
				Arguments: `{"action":"add","tool_name":"web.search","role_name":"Pickle"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute role-name tool access: %v", err)
	}
	if toolAccess.Confirmation == nil || toolAccess.Confirmation.Action != "tool_access.add" || toolAccess.Confirmation.Arguments["tool_name"] != "web.search" || toolAccess.Confirmation.Arguments["role_id"] != "100000000000000777" {
		t.Fatalf("expected resolved tool access confirmation, got %+v", toolAccess.Confirmation)
	}

	userToolAccess, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-user-tool-access",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_tool_access",
				Arguments: `{"action":"remove","tool_name":"panda.generate_image","user_id":"100000000000000888"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute user tool access: %v", err)
	}
	if userToolAccess.Confirmation == nil || userToolAccess.Confirmation.Action != "tool_access.remove" || userToolAccess.Confirmation.Arguments["tool_name"] != "panda.generate_image" || userToolAccess.Confirmation.Arguments["user_id"] != "100000000000000888" {
		t.Fatalf("expected user tool access confirmation, got %+v", userToolAccess.Confirmation)
	}
	if strings.Contains(userToolAccess.Confirmation.Summary, "role `") {
		t.Fatalf("user-targeted tool access summary should not say role: %+v", userToolAccess.Confirmation)
	}

	userNameProvider := &fakeDiscordProvider{result: map[string]any{"members": []map[string]any{{
		"user": map[string]any{
			"id":          "100000000000000999",
			"username":    "xer0",
			"global_name": "xer0",
			"effective":   "xer0",
		},
	}}}}
	userNameExecutor := NewExecutor(registry, nil, nil).
		WithAdminOperations(newToolAdminService(t)).
		WithDiscordToolProvider(userNameProvider)
	denyNamedUser, err := userNameExecutor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-deny-named-user-tool-access",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_tool_access",
				Arguments: `{"action":"deny","tool_name":"image generation tool","user":"@xer0"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute named-user deny tool access: %v", err)
	}
	if denyNamedUser.Confirmation == nil || denyNamedUser.Confirmation.Action != "tool_access.deny" {
		t.Fatalf("expected deny tool access confirmation, got %+v", denyNamedUser.Confirmation)
	}
	if denyNamedUser.Confirmation.Arguments["tool_name"] != "panda.generate_image" || denyNamedUser.Confirmation.Arguments["user_id"] != "100000000000000999" {
		t.Fatalf("expected image generation alias and resolved user ID, got %+v", denyNamedUser.Confirmation)
	}
	if len(userNameProvider.requests) != 1 || userNameProvider.requests[0].ToolName != "discord.list_members" {
		t.Fatalf("expected Discord member lookup, got %+v", userNameProvider.requests)
	}

	openImageTools, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-open-image-tools",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_tool_access",
				Arguments: `{"action":"open","tool_group":"image_tools"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute open image tool access: %v", err)
	}
	if openImageTools.Confirmation == nil || openImageTools.Confirmation.Action != "tool_access.open" {
		t.Fatalf("expected open tool access confirmation, got %+v", openImageTools.Confirmation)
	}
	if toolNames := openImageTools.Confirmation.Arguments["tool_names"]; !strings.Contains(toolNames, "panda.generate_image") || !strings.Contains(toolNames, "panda.inspect_image") {
		t.Fatalf("expected image tool group to expand to both native tools, got %+v", openImageTools.Confirmation)
	}
	if permissions := openImageTools.Confirmation.Arguments["permissions"]; permissions != admin.PermissionAssistantImageGeneration {
		t.Fatalf("expected image permission to be cleared by open action, got %+v", openImageTools.Confirmation)
	}
	if strings.Contains(openImageTools.Confirmation.Summary, "@everyone") || strings.Contains(openImageTools.Confirmation.Summary, "role `") {
		t.Fatalf("open-to-everyone summary should not model everyone as a role: %+v", openImageTools.Confirmation)
	}

	openWebSearch, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-open-web-search",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_tool_access",
				Arguments: `{"action":"open","tool_name":"web_search"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute open web tool access: %v", err)
	}
	if openWebSearch.Confirmation == nil || openWebSearch.Confirmation.Action != "tool_access.open" {
		t.Fatalf("expected open web tool access confirmation, got %+v", openWebSearch.Confirmation)
	}
	if openWebSearch.Confirmation.Arguments["tool_names"] != "web.search" || openWebSearch.Confirmation.Arguments["permissions"] != admin.PermissionAssistantWebSearch {
		t.Fatalf("expected registered native web tool to clear its required permission, got %+v", openWebSearch.Confirmation)
	}

	openComposedTool, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-open-composed-tool",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_tool_access",
				Arguments: `{"action":"open","tool_name":"welcome_builder"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute open composed tool access: %v", err)
	}
	if openComposedTool.Confirmation == nil || openComposedTool.Confirmation.Action != "tool_access.open" {
		t.Fatalf("expected open composed tool access confirmation, got %+v", openComposedTool.Confirmation)
	}
	if openComposedTool.Confirmation.Arguments["tool_names"] != "welcome_builder" || openComposedTool.Confirmation.Arguments["permissions"] != "" {
		t.Fatalf("expected custom tool open to clear only tool allowlist rows, got %+v", openComposedTool.Confirmation)
	}

	invalidEveryoneGrant, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-invalid-everyone-tool-access",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_tool_access",
				Arguments: `{"action":"add","tool_name":"panda.generate_image","role_id":"guild-1"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute invalid @everyone tool access: %v", err)
	}
	if invalidEveryoneGrant.Confirmation != nil || !strings.Contains(invalidEveryoneGrant.Message.Content, admin.ErrToolAccessEveryoneRole.Error()) {
		t.Fatalf("expected @everyone role target to be rejected, got %+v", invalidEveryoneGrant)
	}

	memberRole, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
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

	callsBeforeRoleCreate := provider.calls
	discordRole, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-discord-role",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_discord_role",
				Arguments: `{"action":"create","name":"test"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute Discord role create: %v", err)
	}
	if discordRole.Confirmation == nil || discordRole.Confirmation.Action != "discord_role.create" || discordRole.Confirmation.Arguments["name"] != "test" {
		t.Fatalf("expected Discord role confirmation, got %+v", discordRole)
	}
	if provider.calls != callsBeforeRoleCreate {
		t.Fatalf("Discord role creation preparation should not call provider, calls before=%d after=%d", callsBeforeRoleCreate, provider.calls)
	}
}

func TestExecutorAdminRemovalToolsRequireConfirmation(t *testing.T) {
	const channelID = "100000000000000123"
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
				Arguments: `{"action":"remove","channel_id":"100000000000000123"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute channel remove: %v", err)
	}
	message := result.Message
	if !strings.Contains(message.Content, `"confirmation_required":true`) || strings.Contains(message.Content, channelID) {
		t.Fatalf("expected confirmation preview, got %+v", message)
	}
	if result.Confirmation == nil || result.Confirmation.Action != "channel_rule.remove" || result.Confirmation.Arguments["channel_id"] != channelID {
		t.Fatalf("expected channel-rule confirmation artifact, got %+v", result.Confirmation)
	}
}

func TestExecutorChannelRuleResolvesChannelName(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	provider := &fakeDiscordProvider{result: map[string]any{"channels": []map[string]any{{
		"id":   "100000000000000888",
		"name": "panda",
	}}}}
	executor := NewExecutor(registry, nil, nil).
		WithAdminOperations(newToolAdminService(t)).
		WithDiscordToolProvider(provider)

	result, err := executor.Execute(context.Background(), ExecutionRequest{
		GuildID: "guild-1",
		ActorID: "admin",
		Access:  testAccess(ToolPolicyWriteConfirmed, admin.PermissionAdminConfigWrite),
		Call: llm.ToolCall{
			ID:   "call-channel-allow",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_channel_rule",
				Arguments: `{"action":"allow","channel_name":"panda"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute channel allow: %v", err)
	}
	if result.Confirmation == nil || result.Confirmation.Action != "channel_rule.set" || result.Confirmation.Arguments["channel_id"] != "100000000000000888" || result.Confirmation.Arguments["rule"] != "allow" {
		t.Fatalf("expected resolved channel-rule confirmation, got %+v", result.Confirmation)
	}
	if len(provider.requests) != 1 || provider.requests[0].ToolName != "discord.list_channels" {
		t.Fatalf("expected channel lookup request, got %+v", provider.requests)
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
			name:           "user permission add",
			tool:           "panda_manage_user_permission",
			arguments:      `{"action":"add","user_id":"100000000000000002","permission":"admin.badge"}`,
			expectedAction: "user_permission.add",
		},
		{
			name:           "channel rule allow",
			tool:           "panda_manage_channel_rule",
			arguments:      `{"action":"allow","channel_id":"100000000000000123"}`,
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
				Name:        "member_welcome",
				Description: "Welcomes new members.",
				Parameters:  objectSchema("user_id"),
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
	for _, want := range []string{`"native_name":"discord.fetch_message"`, `"name":"discord_channel_activity_summary"`, `"name":"panda_list_tools"`, `"kind":"composed"`, `"name":"member_welcome"`, `"input_schema"`} {
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
	for _, want := range []string{`"presentation":"capabilities"`, `"name":"answer_from_visible_discord_context"`, `"label":"Look up server context"`} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected normal user tool listing to contain %s, got %s", want, content)
		}
	}
	for _, hidden := range []string{"current_capabilities", "discord_fetch_message", "native_name", "input_schema", "read_config", "panda_manage_tool_access", "discord_modify_channel_permissions", "admin.config.write", "admin_read", "admin_write"} {
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
	for _, want := range []string{`"name":"panda_list_tools"`, `"name":"discord_fetch_message"`, `"name":"read_config"`, `"name":"panda_manage_tool_access"`} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected admin tool listing to contain %s, got %s", want, content)
		}
	}
	for _, hidden := range []string{"admin_tools_hidden", "super secret admin tools"} {
		if strings.Contains(content, hidden) {
			t.Fatalf("admin tool listing should not include hidden-admin marker %q: %s", hidden, content)
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

	assistantTools = executor.OpenRouterTools(ToolAccess{
		Policy: ToolPolicyAssistive,
		Permissions: map[string]struct{}{
			admin.PermissionAssistantUse:       {},
			admin.PermissionAssistantWebSearch: {},
		},
		EnabledFeatures: map[string]struct{}{
			features.AssistantChat: {},
		},
		FeatureGateActive: true,
	})
	names = map[string]bool{}
	for _, tool := range assistantTools {
		names[tool.Function.Name] = true
	}
	if !names["web_search"] {
		t.Fatalf("expected configured web search tool even when feature gate omits web_search, got %+v", names)
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
