package composed

import (
	"context"
	"strings"
	"testing"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
	"github.com/sn0w/panda2/internal/tools"
)

type fakeDiscordToolProvider struct {
	calls []tools.DiscordToolRequest
}

func (f *fakeDiscordToolProvider) ExecuteDiscordTool(_ context.Context, request tools.DiscordToolRequest) (any, error) {
	f.calls = append(f.calls, request)
	return map[string]any{
		"sent":       true,
		"message_id": "message-1",
		"channel_id": request.Arguments["channel_id"],
	}, nil
}

func newComposedTestService(t *testing.T) (*Service, *fakeDiscordToolProvider) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	registry, err := tools.NewDefaultRegistry()
	if err != nil {
		t.Fatalf("tool registry: %v", err)
	}
	provider := &fakeDiscordToolProvider{}
	executor := tools.NewExecutor(registry, nil, nil).WithDiscordToolProvider(provider)
	service := NewService(repository.NewComposedToolRepository(db.DB), registry, executor, nil, "openrouter/auto")
	return service, provider
}

func TestBuilderWelcomeDraftApprovalAdvertiseAndRun(t *testing.T) {
	ctx := context.Background()
	service, provider := newComposedTestService(t)

	draft, err := service.Draft(ctx, DraftRequest{
		GuildID:   "guild-1",
		ActorID:   "moderator-1",
		Text:      "Create a builder welcome tool",
		RoleID:    "role-builder",
		ChannelID: "channel-general",
	})
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if draft.Tool != "builder_welcome" || draft.Version != 1 || draft.Validation.RiskLevel != "high" {
		t.Fatalf("unexpected draft: %+v", draft)
	}

	beforeApproval, err := service.OpenRouterTools(ctx, tools.DynamicToolListRequest{
		GuildID:        "guild-1",
		InvocationType: InvocationChatTool,
		Access: tools.ToolAccess{
			Policy:                       tools.ToolPolicyAssistive,
			Permissions:                  map[string]struct{}{admin.PermissionToolComposeInvoke: {}},
			AllowedTools:                 map[string]struct{}{"builder_welcome": {}},
			RequireExplicitComposedTools: true,
		},
	})
	if err != nil {
		t.Fatalf("OpenRouterTools before approval: %v", err)
	}
	if len(beforeApproval) != 0 {
		t.Fatalf("draft tool should not be advertised before approval: %+v", beforeApproval)
	}

	if _, err := service.Approve(ctx, "guild-1", "builder_welcome", 1, "admin-1"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	hidden, err := service.OpenRouterTools(ctx, tools.DynamicToolListRequest{
		GuildID:        "guild-1",
		InvocationType: InvocationChatTool,
		Access: tools.ToolAccess{
			Policy:                       tools.ToolPolicyAssistive,
			Permissions:                  map[string]struct{}{admin.PermissionToolComposeInvoke: {}},
			RequireExplicitComposedTools: true,
		},
	})
	if err != nil {
		t.Fatalf("OpenRouterTools hidden: %v", err)
	}
	if len(hidden) != 0 {
		t.Fatalf("composed tool should require an explicit tool role grant: %+v", hidden)
	}
	advertised, err := service.OpenRouterTools(ctx, tools.DynamicToolListRequest{
		GuildID:        "guild-1",
		InvocationType: InvocationChatTool,
		Access: tools.ToolAccess{
			Policy:                       tools.ToolPolicyAssistive,
			Permissions:                  map[string]struct{}{admin.PermissionToolComposeInvoke: {}},
			AllowedTools:                 map[string]struct{}{"builder_welcome": {}},
			RequireExplicitComposedTools: true,
		},
	})
	if err != nil {
		t.Fatalf("OpenRouterTools: %v", err)
	}
	if len(advertised) != 1 || advertised[0].Function.Name != "builder_welcome" {
		t.Fatalf("approved tool was not advertised: %+v", advertised)
	}

	run, err := service.Run(ctx, RunRequest{
		GuildID:        "guild-1",
		ToolName:       "builder_welcome",
		InvocationType: InvocationEvent,
		Input:          map[string]any{"user_id": "user-1", "role_id": "role-builder"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != RunSucceeded || run.Output["sent"] != true || run.Output["message_id"] != "message-1" {
		t.Fatalf("unexpected run result: %+v", run)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("expected one Discord call, got %d", len(provider.calls))
	}
	call := provider.calls[0]
	if call.ToolName != "discord.send_message" || call.Arguments["channel_id"] != "channel-general" || !strings.Contains(call.Arguments["content"].(string), "<@user-1>") {
		t.Fatalf("unexpected Discord call: %+v", call)
	}
}

func TestComposedToolUsingAdminNativeToolRequiresAdminAccess(t *testing.T) {
	ctx := context.Background()
	service, _ := newComposedTestService(t)
	spec := NormalizeSpec(Spec{
		SchemaVersion: 1,
		Name:          "config_reader",
		Description:   "Read Panda config",
		InputSchema:   rawObjectSchema(nil, map[string]string{}),
		OutputSchema:  rawObjectSchema([]string{"ok"}, map[string]string{"ok": "boolean"}),
		Runner: RunnerSpec{
			Type:          RunnerDeterministic,
			ToolAllowlist: []string{"read_config"},
		},
		Steps: []StepSpec{{
			ID:        "read",
			Type:      StepToolCall,
			Tool:      "read_config",
			Arguments: map[string]any{"guild_id": "guild-1"},
			OutputKey: "config",
		}},
		Invocations: []InvocationSpec{{Type: InvocationChatTool}},
		Safety:      SafetySpec{MaxNestedDepth: 2},
	})
	if _, err := service.Draft(ctx, DraftRequest{GuildID: "guild-1", ActorID: "admin-1", SpecJSON: mustJSON(spec)}); err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if _, err := service.Approve(ctx, "guild-1", "config_reader", 1, "admin-1"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	regular, err := service.OpenRouterTools(ctx, tools.DynamicToolListRequest{
		GuildID:        "guild-1",
		InvocationType: InvocationChatTool,
		Access: tools.ToolAccess{
			Policy:                       tools.ToolPolicyWriteConfirmed,
			Permissions:                  map[string]struct{}{admin.PermissionToolComposeInvoke: {}},
			AllowedTools:                 map[string]struct{}{"config_reader": {}},
			RequireExplicitComposedTools: true,
		},
	})
	if err != nil {
		t.Fatalf("OpenRouterTools regular: %v", err)
	}
	if len(regular) != 0 {
		t.Fatalf("admin-native composed tool should stay hidden from regular access: %+v", regular)
	}

	elevated, err := service.OpenRouterTools(ctx, tools.DynamicToolListRequest{
		GuildID:        "guild-1",
		InvocationType: InvocationChatTool,
		Access: tools.ToolAccess{
			Policy:      tools.ToolPolicyWriteConfirmed,
			Permissions: map[string]struct{}{admin.PermissionToolComposeInvoke: {}, admin.PermissionAdminConfigRead: {}},
		},
	})
	if err != nil {
		t.Fatalf("OpenRouterTools elevated: %v", err)
	}
	if len(elevated) != 1 || elevated[0].Function.Name != "config_reader" {
		t.Fatalf("expected elevated access to see config_reader, got %+v", elevated)
	}
}

func TestEventJobMatchesApprovedRoleAddedInvocation(t *testing.T) {
	ctx := context.Background()
	service, provider := newComposedTestService(t)
	if _, err := service.Draft(ctx, DraftRequest{
		GuildID:   "guild-1",
		ActorID:   "moderator-1",
		Text:      "Create a builder welcome tool",
		RoleID:    "role-builder",
		ChannelID: "channel-general",
	}); err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if _, err := service.Approve(ctx, "guild-1", "builder_welcome", 1, "admin-1"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	payload := EventJobPayload{
		GuildID:   "guild-1",
		EventID:   "event-1",
		EventType: "guild.member.role_added",
		UserID:    "user-1",
		Metadata:  map[string]string{"role_id": "role-builder"},
	}
	if err := service.HandleEventJob(ctx, store.Job{Payload: mustJSON(payload)}); err != nil {
		t.Fatalf("HandleEventJob: %v", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("expected matching event to invoke tool, got %d calls", len(provider.calls))
	}
	if err := service.HandleEventJob(ctx, store.Job{Payload: mustJSON(payload)}); err != nil {
		t.Fatalf("HandleEventJob duplicate: %v", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("duplicate event should be deduped, got %d calls", len(provider.calls))
	}
}

func TestValidateSpecRejectsUnsafeWrites(t *testing.T) {
	registry, err := tools.NewDefaultRegistry()
	if err != nil {
		t.Fatalf("tool registry: %v", err)
	}
	spec := NormalizeSpec(Spec{
		SchemaVersion: 1,
		Name:          "unsafe_welcome",
		Description:   "Unsafe welcome",
		InputSchema:   rawObjectSchema([]string{"user_id"}, map[string]string{"user_id": "string"}),
		OutputSchema:  rawObjectSchema([]string{"sent"}, map[string]string{"sent": "boolean"}),
		Runner: RunnerSpec{
			Type:          RunnerDeterministic,
			ToolAllowlist: []string{"discord.send_message"},
		},
		Steps: []StepSpec{{
			ID:   "send",
			Type: StepToolCall,
			Tool: "discord.send_message",
			Arguments: map[string]any{
				"channel_id":       "channel-1",
				"content_template": "hi @everyone",
				"allowed_mentions": map[string]any{"everyone": true},
			},
		}},
		Invocations: []InvocationSpec{{Type: InvocationChatTool}},
		Safety:      SafetySpec{MaxNestedDepth: 2},
	})
	report := ValidateSpec(spec, registry)
	if report.Valid {
		t.Fatalf("expected unsafe mention spec to be rejected: %+v", report)
	}

	spec.Steps[0].Tool = "discord.ban_member"
	spec.Runner.ToolAllowlist = []string{"discord.ban_member"}
	spec.Steps[0].Arguments = map[string]any{"user_id": "user-1", "reason": "test"}
	report = ValidateSpec(spec, registry)
	if report.Valid || !strings.Contains(strings.Join(report.Errors, " "), "not available") {
		t.Fatalf("expected unsupported write to be rejected: %+v", report)
	}
}

func TestPolicyAwareModNoteDraftUsesNonDestructiveAllowlist(t *testing.T) {
	ctx := context.Background()
	service, _ := newComposedTestService(t)
	draft, err := service.PreviewDraft(ctx, DraftRequest{
		GuildID: "guild-1",
		ActorID: "moderator-1",
		Text:    "Create a policy-aware mod note tool that takes a message link",
	})
	if err != nil {
		t.Fatalf("PreviewDraft: %v", err)
	}
	if draft.Spec.Name != "policy_mod_note" || draft.Spec.Runner.Type != RunnerAgentic {
		t.Fatalf("unexpected mod note spec: %+v", draft.Spec)
	}
	if draft.Validation.RiskLevel != "medium" || len(draft.Validation.Writes) != 0 {
		t.Fatalf("draft note should not be treated as an unattended write: %+v", draft.Validation)
	}
	names := strings.Join(draft.Validation.NativeTools, ",")
	for _, required := range []string{"discord.fetch_message", "search_knowledge", "draft_moderator_note"} {
		if !strings.Contains(names, required) {
			t.Fatalf("expected allowlist to contain %s: %+v", required, draft.Validation.NativeTools)
		}
	}
}
