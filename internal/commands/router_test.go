package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/assistant"
	"github.com/sn0w/panda2/internal/billing"
	"github.com/sn0w/panda2/internal/composed"
	"github.com/sn0w/panda2/internal/config"
	contextsvc "github.com/sn0w/panda2/internal/context"
	"github.com/sn0w/panda2/internal/features"
	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/memory"
	"github.com/sn0w/panda2/internal/ops"
	"github.com/sn0w/panda2/internal/queue"
	"github.com/sn0w/panda2/internal/ratelimit"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/scheduler"
	"github.com/sn0w/panda2/internal/store"
	"github.com/sn0w/panda2/internal/tools"
)

type fakeLLM struct {
	response  llm.ChatResponse
	responses []llm.ChatResponse
	err       error
	requests  []llm.ChatRequest
}

type fakeContextProvider struct {
	messages []contextsvc.Message
}

type fakeThreadManager struct {
	thread Thread
	err    error
	calls  []ThreadRequest
}

type fakeMemberRoleManager struct {
	adds    []MemberRoleRequest
	removes []MemberRoleRequest
	err     error
}

type fakeDiscordRoleManager struct {
	creates []DiscordRoleRequest
	role    DiscordRole
	err     error
}

type fakeToolMusicManager struct {
	requests []tools.MusicManagementRequest
}

type fakeFeatureInstallCreator struct {
	requests []FeatureInstallIntentRequest
	result   FeatureInstallIntentResult
	err      error
}

type fakeCommandDiscordProvider struct {
	requests []tools.DiscordToolRequest
}

type fakeAttachmentReader struct {
	attachment store.Attachment
	err        error
}

func (f fakeAttachmentReader) Get(context.Context, string, uint) (store.Attachment, error) {
	if f.err != nil {
		return store.Attachment{}, f.err
	}
	return f.attachment, nil
}

func (f *fakeThreadManager) EnsureChatThread(_ context.Context, request ThreadRequest) (Thread, error) {
	f.calls = append(f.calls, request)
	if f.err != nil {
		return Thread{}, f.err
	}
	return f.thread, nil
}

func (f *fakeMemberRoleManager) AddMemberRole(_ context.Context, request MemberRoleRequest) error {
	f.adds = append(f.adds, request)
	return f.err
}

func (f *fakeMemberRoleManager) RemoveMemberRole(_ context.Context, request MemberRoleRequest) error {
	f.removes = append(f.removes, request)
	return f.err
}

func (f *fakeDiscordRoleManager) CreateRole(_ context.Context, request DiscordRoleRequest) (DiscordRole, error) {
	f.creates = append(f.creates, request)
	if f.err != nil {
		return DiscordRole{}, f.err
	}
	if f.role.ID != "" || f.role.Name != "" {
		return f.role, nil
	}
	return DiscordRole{ID: "role-created", Name: request.Name}, nil
}

func (f *fakeToolMusicManager) ManageMusic(_ context.Context, request tools.MusicManagementRequest) (any, error) {
	f.requests = append(f.requests, request)
	return map[string]any{"result": map[string]any{
		"action":  request.Action,
		"query":   request.Query,
		"title":   "Now playing",
		"content": "music handled",
		"url":     "https://example.com/track",
		"fields": []map[string]any{
			{"name": "Duration", "value": "3:12", "inline": true},
		},
		"actions": []map[string]string{
			{"label": "Open track", "url": "https://example.com/track"},
		},
	}}, nil
}

func (f *fakeFeatureInstallCreator) CreateFeatureInstallIntent(_ context.Context, request FeatureInstallIntentRequest) (FeatureInstallIntentResult, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return FeatureInstallIntentResult{}, f.err
	}
	if f.result.AuthorizeURL != "" {
		return f.result, nil
	}
	return FeatureInstallIntentResult{
		AuthorizeURL: "https://discord.com/oauth2/authorize?state=test",
		ExpiresAt:    time.Now().UTC().Add(30 * time.Minute),
		Selection: features.Selection{
			ExpandedFeatureIDs:          append([]string(nil), request.FeatureIDs...),
			DiscordPermissionNames:      []string{"VIEW_CHANNEL"},
			DiscordPermissionBitfield:   "1024",
			DiscordPermissionBitfield64: 1024,
		},
	}, nil
}

func (f *fakeCommandDiscordProvider) ExecuteDiscordTool(_ context.Context, request tools.DiscordToolRequest) (any, error) {
	f.requests = append(f.requests, request)
	if request.ToolName == "discord.create_poll" {
		return map[string]any{
			"created":    true,
			"channel_id": request.Arguments["channel_id"],
			"poll":       request.Arguments,
		}, nil
	}
	return map[string]any{
		"tool":      request.ToolName,
		"arguments": request.Arguments,
		"dry_run":   request.DryRun,
	}, nil
}

func (f fakeContextProvider) FetchMessage(_ context.Context, ref contextsvc.MessageRef) (contextsvc.Message, error) {
	for _, message := range f.messages {
		if message.MessageID == ref.MessageID {
			return message, nil
		}
	}
	return contextsvc.Message{}, repository.ErrNotFound
}

func (f fakeContextProvider) FetchRecentMessages(_ context.Context, ref contextsvc.ChannelRef, limit int) ([]contextsvc.Message, error) {
	var result []contextsvc.Message
	for _, message := range f.messages {
		if message.GuildID == ref.GuildID && message.ChannelID == ref.ChannelID {
			result = append(result, message)
		}
	}
	if len(result) > limit {
		result = result[len(result)-limit:]
	}
	return result, nil
}

func (f *fakeLLM) Chat(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	f.requests = append(f.requests, request)
	if len(f.responses) > 0 {
		response := f.responses[0]
		f.responses = f.responses[1:]
		return response, nil
	}
	return f.response, f.err
}

func joinRequestMessages(request llm.ChatRequest) string {
	var builder strings.Builder
	for _, message := range request.Messages {
		builder.WriteString(message.Role)
		builder.WriteString(":")
		builder.WriteString(message.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

func requestToolNames(request llm.ChatRequest) map[string]bool {
	names := map[string]bool{}
	for _, tool := range request.Tools {
		names[tool.Function.Name] = true
	}
	return names
}

func newTestRouter(t *testing.T, client *fakeLLM, limit int, configureExecutor ...func(*tools.Executor)) *Router {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	configs := repository.NewGuildConfigRepository(db.DB)
	usage := repository.NewUsageRepository(db.DB)
	audit := repository.NewAuditRepository(db.DB)
	knowledge := repository.NewKnowledgeRepository(db.DB)
	access := repository.NewAccessRepository(db.DB)
	budgets := repository.NewBudgetRepository(db.DB)
	members := repository.NewMemberRepository(db.DB)
	schedules := repository.NewScheduleRepository(db.DB)
	memoryService := memory.NewService(knowledge)
	conversations := repository.NewConversationRepository(db.DB)
	jobs := repository.NewJobRepository(db.DB)
	worker := queue.NewWorker(jobs, "test-worker")
	opsService := ops.NewService(config.Config{DataDir: t.TempDir()}, db, configs, jobs, worker)
	assistantService := assistant.NewService(client, usage, configs, memoryService, conversations, "openrouter/auto", "", nil)
	adminService := admin.NewService(configs, usage, audit, memoryService, access, budgets, members)
	registry, err := tools.NewDefaultRegistry()
	if err != nil {
		t.Fatalf("tool registry: %v", err)
	}
	toolExecutor := tools.NewExecutor(registry, memoryService, configs).
		WithAdminOperations(adminService).
		WithOpsManager(opsService)
	composedService := composed.NewService(repository.NewComposedToolRepository(db.DB), registry, toolExecutor, client, "openrouter/auto")
	toolExecutor.WithDynamicToolProvider(composedService)
	toolExecutor.WithComposedToolManager(composedService)
	schedulerService := scheduler.NewService(schedules, jobs).WithComposedService(composedService)
	toolExecutor.WithScheduleManager(schedulerService)
	toolExecutor.WithReminderManager(schedulerService)
	toolExecutor.WithMusicManager(&fakeToolMusicManager{})
	for _, configure := range configureExecutor {
		configure(toolExecutor)
	}
	assistantService.WithToolExecutor(toolExecutor)
	return NewRouter(
		adminService,
		assistantService,
		opsService,
		ratelimit.New(limit, time.Minute),
	).WithComposedService(composedService).WithScheduler(schedulerService).WithDataRepository(repository.NewGuildDataRepository(db.DB)).WithToolExecutor(toolExecutor)
}

func attachTestBilling(t *testing.T, router *Router, guildID string) (*billing.Service, billing.Entitlement) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "billing.db"))
	if err != nil {
		t.Fatalf("open billing store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service := billing.NewService(repository.NewBillingRepository(db.DB), billing.Config{})
	entitlement, err := service.EnsureTrial(ctx, billing.TrialSeed{
		GuildID:            guildID,
		BillingOwnerUserID: "owner-1",
		AuthorizedAt:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("EnsureTrial: %v", err)
	}
	router.WithBilling(service)
	return service, entitlement
}

func attachFeatureService(t *testing.T, router *Router) *repository.FeatureRepository {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open feature store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := repository.NewFeatureRepository(db.DB)
	router.WithFeatureService(features.NewService(repo))
	return repo
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func seedScheduledComposedTool(t *testing.T, router *Router, guildID, actorID, name string) {
	t.Helper()
	spec := strings.ReplaceAll(`{
		"schema_version": 1,
		"name": "TOOL_NAME",
		"description": "Fixture scheduled composed tool.",
		"input_schema": {"type": "object", "properties": {}, "additionalProperties": true},
		"output_schema": {"type": "object", "properties": {}, "additionalProperties": true},
		"runner": {"type": "deterministic", "max_tokens": 100, "tool_allowlist": []},
		"invocations": [{"type": "scheduled"}],
		"safety": {
			"requires_approval": false,
			"requires_confirmation_on_write": false,
			"max_nested_depth": 2,
			"cooldown_seconds": 0,
			"max_runs_per_hour": 20,
			"dedupe_window_seconds": 0
		}
	}`, "TOOL_NAME", name)
	result, err := router.composed.Draft(context.Background(), composed.DraftRequest{
		GuildID:  guildID,
		ActorID:  actorID,
		SpecJSON: spec,
	})
	if err != nil {
		t.Fatalf("draft scheduled composed tool: %v", err)
	}
	if _, err := router.composed.Approve(context.Background(), guildID, name, result.Version, actorID); err != nil {
		t.Fatalf("approve scheduled composed tool: %v", err)
	}
}

func memberWelcomeSpecJSON() string {
	return `{
		"schema_version": 1,
		"name": "member_welcome",
		"description": "Welcomes new members in the bot test channel.",
		"input_schema": {"type":"object","additionalProperties":false,"properties":{"user_id":{"type":"string"},"username":{"type":"string"}},"required":["user_id"]},
		"output_schema": {"type":"object","additionalProperties":false,"properties":{"sent":{"type":"boolean"},"message_id":{"type":"string"}},"required":["sent"]},
		"runner": {"type":"deterministic","system_prompt":"Send only the approved welcome message.","temperature":0.2,"max_tokens":300,"tool_allowlist":["discord.send_message"]},
		"steps": [{"id":"send_message","type":"tool_call","tool":"discord.send_message","arguments":{"channel_id":"1517943356074889276","content_template":"Welcome <@{{user_id}}>!","allowed_mentions":{"users":true,"roles":false,"everyone":false}}}],
		"invocations": [{"type":"event","event_type":"guild.member.joined"},{"type":"chat_tool"}],
		"safety": {"requires_approval":true,"requires_confirmation_on_write":false,"max_nested_depth":2,"cooldown_seconds":30,"max_runs_per_hour":20,"dedupe_window_seconds":300}
	}`
}

func TestRouterPing(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	response := router.Handle(context.Background(), Request{Command: "ping", UserID: "user-1"})
	if response.Content != "pong" || !response.Ephemeral {
		t.Fatalf("unexpected ping response: %+v", response)
	}
}

func TestHandlePollCreatesNativePollResponse(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	response := router.Handle(context.Background(), Request{
		Command: "poll",
		Options: map[string]string{
			"question":          "Where should lunch be?",
			"answers":           "Tacos | Pizza | Sushi",
			"duration_hours":    "12",
			"allow_multiselect": "true",
		},
	})
	if response.Poll == nil {
		t.Fatalf("expected poll response, got %+v", response)
	}
	if response.Poll.Question != "Where should lunch be?" || len(response.Poll.Answers) != 3 || response.Poll.DurationHours != 12 || !response.Poll.AllowMultiselect {
		t.Fatalf("unexpected poll response: %+v", response.Poll)
	}
	if response.Content != "" || response.Ephemeral {
		t.Fatalf("native poll response should not add text or be ephemeral: %+v", response)
	}
}

func TestHandlePollRejectsInvalidPoll(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	response := router.Handle(context.Background(), Request{
		Command: "poll",
		Options: map[string]string{
			"question": "Lunch?",
			"answers":  "Only one",
		},
	})
	if response.Poll != nil || !response.Ephemeral || !strings.Contains(response.Content, "at least 2 answers") {
		t.Fatalf("expected validation response, got %+v", response)
	}
}

func TestRouterHelpHidesElevatedGuidanceFromRegularUsers(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	response := router.Handle(context.Background(), Request{Command: "help", UserID: "user-1"})
	if !response.Ephemeral {
		t.Fatalf("expected help response to be ephemeral: %+v", response)
	}
	for _, want := range []string{
		"### Panda Help",
		"**Chat naturally**",
		"- Mention `Panda` in a normal message: `Panda is this true?`",
		"**Message actions**",
		"**Good things to ask**",
	} {
		if !strings.Contains(response.Content, want) {
			t.Fatalf("help response missing %q:\n%s", want, response.Content)
		}
	}
	for _, hidden := range []string{
		"Admin commands",
		"`/admin",
		"tool_policy",
		"action:add",
		"Composed tools",
		"`/tool",
		"Moderator tools",
		"Moderation guidance",
		"Role/channel access",
	} {
		if strings.Contains(response.Content, hidden) {
			t.Fatalf("regular help should not include %q:\n%s", hidden, response.Content)
		}
	}
}

func TestRouterHelpShowsAdminGuidanceToGuildAdmins(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	response := router.Handle(context.Background(), Request{Command: "help", GuildID: "guild-1", UserID: "admin", IsGuildAdmin: true})
	if !response.Ephemeral {
		t.Fatalf("expected help response to be ephemeral: %+v", response)
	}
	for _, want := range []string{"**Admin setup through chat**", "confirmation", "**Moderator tools**", "**Composed tools**", "Role/channel access"} {
		if !strings.Contains(response.Content, want) {
			t.Fatalf("admin help should include %q:\n%s", want, response.Content)
		}
	}
	for _, hidden := range []string{"**Admin slash fallback**", "`/admin", "tool_policy"} {
		if strings.Contains(response.Content, hidden) {
			t.Fatalf("admin help should not include slash fallback %q:\n%s", hidden, response.Content)
		}
	}
	if len(response.Content) > discordMessageContentLimit {
		t.Fatalf("help must fit Discord content limit: %d > %d\n%s", len(response.Content), discordMessageContentLimit, response.Content)
	}
}

func TestRouterHelpShowsOnlyAllowedElevatedGuidanceToModerators(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 5)
	if _, err := router.admin.AddRolePermission(ctx, "guild-1", "admin", "role-mod", admin.PermissionModerationUse); err != nil {
		t.Fatalf("AddRolePermission: %v", err)
	}

	response := router.Handle(ctx, Request{Command: "help", GuildID: "guild-1", UserID: "mod", RoleIDs: []string{"role-mod"}})
	if !response.Ephemeral {
		t.Fatalf("expected help response to be ephemeral: %+v", response)
	}
	for _, want := range []string{"**Moderator tools**", "Moderation guidance"} {
		if !strings.Contains(response.Content, want) {
			t.Fatalf("moderator help should include %q:\n%s", want, response.Content)
		}
	}
	for _, hidden := range []string{
		"`/admin role`",
		"`/admin behavior`",
		"`/admin soul`",
		"Composed tools",
		"`/tool draft`",
		"Role/channel access",
	} {
		if strings.Contains(response.Content, hidden) {
			t.Fatalf("moderator help should not include %q:\n%s", hidden, response.Content)
		}
	}
}

func TestRouterHelpShowsOnlyAllowedComposedToolGuidance(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 5)
	if _, err := router.admin.AddRolePermission(ctx, "guild-1", "admin", "role-runner", admin.PermissionToolComposeInvoke); err != nil {
		t.Fatalf("AddRolePermission: %v", err)
	}

	response := router.Handle(ctx, Request{Command: "help", GuildID: "guild-1", UserID: "runner", RoleIDs: []string{"role-runner"}})
	if !response.Ephemeral {
		t.Fatalf("expected help response to be ephemeral: %+v", response)
	}
	for _, want := range []string{"**Composed tools**", "run/simulate/schedule/list/cancel approved tools"} {
		if !strings.Contains(response.Content, want) {
			t.Fatalf("invoke-only help should include %q:\n%s", want, response.Content)
		}
	}
	for _, hidden := range []string{
		"draft or preview",
		"approve, pause, resume",
		"export the approved spec",
		"`/tool",
	} {
		if strings.Contains(response.Content, hidden) {
			t.Fatalf("invoke-only help should not include %q:\n%s", hidden, response.Content)
		}
	}
}

func TestSupportBundleRedactsRawContent(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 5)
	if _, err := router.admin.AddMemoryDocument(ctx, memory.AddDocumentRequest{
		GuildID:   "guild-1",
		Title:     "Launch secret",
		Content:   "raw content that should not appear in support",
		CreatedBy: "admin",
	}); err != nil {
		t.Fatalf("AddMemoryDocument: %v", err)
	}

	response := router.Handle(ctx, Request{
		Command:      "support",
		RequestID:    "req-1",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "Support bundle") || !strings.Contains(response.Content, "Knowledge documents: `1`") {
		t.Fatalf("unexpected support bundle: %+v", response)
	}
	for _, leaked := range []string{"raw content", "Launch secret", "provider/model"} {
		if strings.Contains(response.Content, leaked) {
			t.Fatalf("support bundle leaked %q:\n%s", leaked, response.Content)
		}
	}
}

func TestDataExportAndConfirmedDelete(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 5)
	if _, err := router.admin.AddMemoryDocument(ctx, memory.AddDocumentRequest{
		GuildID:   "guild-1",
		Title:     "Refund policy",
		Content:   "Refunds are available within 14 days with a receipt.",
		CreatedBy: "admin",
	}); err != nil {
		t.Fatalf("AddMemoryDocument: %v", err)
	}

	export := router.Handle(ctx, Request{
		Command:      "data",
		Subcommand:   "export",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if !export.Ephemeral || !strings.Contains(export.Content, "Safe Panda data export summary") || !strings.Contains(export.Content, "1 document") {
		t.Fatalf("unexpected data export: %+v", export)
	}
	if strings.Contains(export.Content, "Refunds are available") {
		t.Fatalf("data export should not include raw knowledge content:\n%s", export.Content)
	}

	pending := router.Handle(ctx, Request{
		Command:      "data",
		Subcommand:   "delete",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"scope": "knowledge"},
	})
	confirmationID := requireConfirmation(t, pending)
	confirmedDelete := router.Handle(ctx, Request{
		Command:      "data",
		Subcommand:   "delete",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"scope": "knowledge", "confirm": confirmationID},
	})
	if !confirmedDelete.Ephemeral || !strings.Contains(confirmedDelete.Content, "Deleted Panda data rows") || confirmedDelete.Confirmation != nil {
		t.Fatalf("expected confirmed data deletion, got %+v", confirmedDelete)
	}

	after := router.Handle(ctx, Request{
		Command:      "data",
		Subcommand:   "export",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if !strings.Contains(after.Content, "0 document") {
		t.Fatalf("expected knowledge export to be empty after confirmed deletion:\n%s", after.Content)
	}
}

func TestRestoredAdminOpsAndScheduleCommandsUseTheirRuntimeGates(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	adminStatus := router.Handle(context.Background(), Request{
		Command:      "admin",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if !adminStatus.Ephemeral || !strings.Contains(adminStatus.Content, "Admin status") {
		t.Fatalf("expected restored admin status, got %+v", adminStatus)
	}

	opsDenied := router.Handle(context.Background(), Request{
		Command:      "ops",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if !opsDenied.Ephemeral || !strings.Contains(opsDenied.Content, "Only a bot owner") {
		t.Fatalf("expected owner gate for ops, got %+v", opsDenied)
	}

	schedule := router.Handle(context.Background(), Request{
		Command:      "schedule",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if !schedule.Ephemeral || strings.Contains(schedule.Content, "disabled") {
		t.Fatalf("expected schedule command to be restored behind normal validation, got %+v", schedule)
	}
}

func TestAdminRoleProfileRequiresGuildControl(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	response := router.Handle(context.Background(), Request{
		Command:    "admin",
		Subcommand: "role",
		GuildID:    "guild-1",
		UserID:     "user-1",
		Options:    map[string]string{"action": "set", "profile": "admin", "role_id": "role-admin"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "Only the Panda owner") {
		t.Fatalf("expected denial response, got %+v", response)
	}
}

func TestAdminRoleProfileConfiguresDelegatedAdminRole(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 5)
	profile := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "role",
		GuildID:      "guild-1",
		UserID:       "owner",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":    "set",
			"profile":   "admin",
			"role_id":   "role-mod",
			"role_name": "MOD",
		},
	})
	if !profile.Ephemeral || !strings.Contains(profile.Content, "`MOD` (`role-mod`) is now a Panda admin role") {
		t.Fatalf("expected role profile command to configure admin role, got %+v", profile)
	}

	help := router.Handle(ctx, Request{Command: "help", GuildID: "guild-1", UserID: "mod", RoleIDs: []string{"role-mod"}})
	for _, want := range []string{"**Admin setup through chat**", "**Moderator tools**", "Role/channel access"} {
		if !strings.Contains(help.Content, want) {
			t.Fatalf("expected admin role profile help to include %q:\n%s", want, help.Content)
		}
	}
	if strings.Contains(help.Content, "**Admin slash fallback**") || strings.Contains(help.Content, "`/admin") {
		t.Fatalf("admin role profile help should not include slash fallback:\n%s", help.Content)
	}

	behavior := router.Handle(ctx, Request{
		Command:    "admin",
		Subcommand: "behavior",
		GuildID:    "guild-1",
		UserID:     "mod",
		RoleIDs:    []string{"role-mod"},
		Options:    map[string]string{"tool_policy": "read_only"},
	})
	if !strings.Contains(behavior.Content, "Behavior settings updated") {
		t.Fatalf("expected admin role profile to allow behavior update, got %+v", behavior)
	}
}

func TestAdminChannelAccessAllowListsAssistantUse(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 5)

	empty := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "channel",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "list"},
	})
	if !empty.Ephemeral || !strings.Contains(empty.Content, "No channel access rules") {
		t.Fatalf("expected empty channel rule list, got %+v", empty)
	}

	allowed := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "channel",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":       "allow",
			"channel_id":   "channel-allowed",
			"channel_name": "panda",
		},
	})
	if !allowed.Ephemeral || !strings.Contains(allowed.Content, "Allowed Panda assistant use") || !strings.Contains(allowed.Content, "#panda") {
		t.Fatalf("expected channel allow response, got %+v", allowed)
	}

	denied := router.Handle(ctx, Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-other",
		UserID:    "user-1",
		Options:   map[string]string{"question": "hi"},
	})
	if !denied.Ephemeral || !strings.Contains(denied.Content, "permission") {
		t.Fatalf("expected non-allowed channel denial, got %+v", denied)
	}

	answer := router.Handle(ctx, Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-allowed",
		UserID:    "user-1",
		Options:   map[string]string{"question": "hi"},
	})
	if answer.Content != "ok" {
		t.Fatalf("expected allowed channel answer, got %+v", answer)
	}

	list := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "channel",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "list"},
	})
	if !strings.Contains(list.Content, "allow-list active") || !strings.Contains(list.Content, "channel-allowed") {
		t.Fatalf("expected allow-list details, got %+v", list)
	}

	removed := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "channel",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "remove", "channel_id": "channel-allowed"},
	})
	if !removed.Ephemeral || !strings.Contains(removed.Content, "Removed Panda channel access rule") {
		t.Fatalf("expected channel remove response, got %+v", removed)
	}
}

func TestAdminRoleProfileConfiguresModeratorRole(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 5)
	response := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "role",
		GuildID:      "guild-1",
		UserID:       "owner",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":    "set",
			"profile":   "mod",
			"role_id":   "role-pickle",
			"role_name": "Pickle",
		},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "`Pickle` (`role-pickle`) is now a Panda moderator role") {
		t.Fatalf("expected moderator role profile response, got %+v", response)
	}
	allowed, err := router.admin.CanUseModeration(ctx, admin.AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"role-pickle"}})
	if err != nil || !allowed {
		t.Fatalf("expected Pickle role to grant moderation, allowed=%t err=%v", allowed, err)
	}
	list := router.Handle(ctx, Request{Command: "admin", Subcommand: "role", GuildID: "guild-1", UserID: "owner", IsGuildAdmin: true, Options: map[string]string{"action": "list"}})
	if !strings.Contains(list.Content, "moderator: `role-pickle`") {
		t.Fatalf("expected role profile list to include moderator role, got %+v", list)
	}
}

func TestAdminRoleProfileCanReuseSameRoleForAdminAndModerator(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 5)
	ownerRequest := Request{
		Command:      "admin",
		Subcommand:   "role",
		GuildID:      "guild-1",
		UserID:       "owner",
		IsGuildAdmin: true,
	}

	adminRequest := ownerRequest
	adminRequest.Options = map[string]string{"action": "set", "profile": "admin", "role_id": "role-staff"}
	adminResponse := router.Handle(ctx, adminRequest)
	if !adminResponse.Ephemeral || !strings.Contains(adminResponse.Content, "`role-staff` is now a Panda admin role") {
		t.Fatalf("expected admin role profile response, got %+v", adminResponse)
	}

	modRequest := ownerRequest
	modRequest.Options = map[string]string{"action": "set", "profile": "moderator", "role_id": "role-staff"}
	modResponse := router.Handle(ctx, modRequest)
	if !modResponse.Ephemeral || !strings.Contains(modResponse.Content, "`role-staff` is now a Panda moderator role") {
		t.Fatalf("expected moderator role profile response, got %+v", modResponse)
	}

	listRequest := ownerRequest
	listRequest.Options = map[string]string{"action": "list"}
	list := router.Handle(ctx, listRequest)
	for _, want := range []string{"admin: `role-staff`", "moderator: `role-staff`"} {
		if !strings.Contains(list.Content, want) {
			t.Fatalf("expected combined role profile list to include %q, got %+v", want, list)
		}
	}

	staffAccess := admin.AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"role-staff"}}
	for name, check := range map[string]func(context.Context, admin.AssistantAccessRequest) (bool, error){
		"config write":   router.admin.CanWriteConfig,
		"moderation use": router.admin.CanUseModeration,
	} {
		allowed, err := check(ctx, staffAccess)
		if err != nil || !allowed {
			t.Fatalf("expected combined role to allow %s, allowed=%t err=%v", name, allowed, err)
		}
	}

	removeAdminRequest := ownerRequest
	removeAdminRequest.Options = map[string]string{"action": "remove", "profile": "admin", "role_id": "role-staff"}
	removeAdmin := router.Handle(ctx, removeAdminRequest)
	if !removeAdmin.Ephemeral || !strings.Contains(removeAdmin.Content, "Removed the Panda admin profile") {
		t.Fatalf("expected admin profile removal response, got %+v", removeAdmin)
	}
	allowed, err := router.admin.CanWriteConfig(ctx, staffAccess)
	if err != nil || allowed {
		t.Fatalf("expected removed admin profile to stop config write, allowed=%t err=%v", allowed, err)
	}
	allowed, err = router.admin.CanUseModeration(ctx, staffAccess)
	if err != nil || !allowed {
		t.Fatalf("expected moderator profile to keep working after admin removal, allowed=%t err=%v", allowed, err)
	}
}

func TestAdminMemberRoleAssignsDiscordRole(t *testing.T) {
	ctx := context.Background()
	manager := &fakeMemberRoleManager{}
	router := newTestRouter(t, &fakeLLM{}, 5).WithMemberRoleManager(manager)

	response := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "member-role",
		GuildID:      "guild-1",
		UserID:       "owner",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":           "add",
			"member_user_id":   "user-target",
			"member_user_name": "Snow",
			"role_id":          "role-pickle",
			"role_name":        "Pickle",
		},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "Assigned `Pickle` (`role-pickle`) to `Snow` (`user-target`)") {
		t.Fatalf("unexpected member-role response: %+v", response)
	}
	if len(manager.adds) != 1 || manager.adds[0].GuildID != "guild-1" || manager.adds[0].UserID != "user-target" || manager.adds[0].RoleID != "role-pickle" {
		t.Fatalf("unexpected member role calls: %+v", manager.adds)
	}
}

func TestAdminToolConfiguresRoleToolAccess(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 5)

	add := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "tool",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":    "add",
			"tool_name": "web.search",
			"role_id":   "role-search",
			"role_name": "Searchers",
		},
	})
	if !add.Ephemeral || !strings.Contains(add.Content, "Allowed `Searchers` (`role-search`) to use `web.search`") {
		t.Fatalf("unexpected add response: %+v", add)
	}

	list := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "tool",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "list"},
	})
	if !strings.Contains(list.Content, "`web.search` -> `role-search`") {
		t.Fatalf("unexpected list response: %+v", list)
	}

	remove := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "tool",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":    "remove",
			"tool_name": "web.search",
			"role_id":   "role-search",
		},
	})
	if !strings.Contains(remove.Content, "Removed `role-search` from `web.search`") {
		t.Fatalf("unexpected remove response: %+v", remove)
	}
}

func TestAskUsesFakeLLM(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Model: "fixture/model", Content: "fixture answer"}}, 5)
	response := router.Handle(context.Background(), Request{
		Command:   "ask",
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"question": "What is up?"},
	})
	if response.Content != "fixture answer" {
		t.Fatalf("unexpected ask response: %+v", response)
	}
}

func TestAskHandlesMissingOpenRouterKey(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{err: llm.ErrNotConfigured}, 5)
	response := router.Handle(context.Background(), Request{
		Command: "ask",
		UserID:  "user-1",
		Options: map[string]string{"question": "hi"},
	})
	if !response.Ephemeral || response.Content == "" {
		t.Fatalf("expected configuration error response, got %+v", response)
	}
}

func TestAssistantErrorLogsFailedModelDetails(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(previous)

	response := assistantError(&assistant.ModelRequestError{
		Task:  "response",
		Model: "inception/mercury-2",
		Err:   errors.New("openrouter error 404"),
	})
	if !response.Ephemeral || response.Content == "" {
		t.Fatalf("expected generic assistant failure response, got %+v", response)
	}
	logged := logs.String()
	if !strings.Contains(logged, "assistant request failed") || !strings.Contains(logged, "model=inception/mercury-2") || !strings.Contains(logged, "task=response") {
		t.Fatalf("expected failed model details in log, got %q", logged)
	}
}

func TestAskRateLimit(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 1)
	request := Request{Command: "ask", UserID: "user-1", Options: map[string]string{"question": "hi"}}
	first := router.Handle(context.Background(), request)
	if first.Content != "ok" {
		t.Fatalf("unexpected first response: %+v", first)
	}
	second := router.Handle(context.Background(), request)
	if !second.Ephemeral {
		t.Fatalf("expected rate-limit response to be ephemeral: %+v", second)
	}
}

func TestAdminModelCommandIsLegacyAndDoesNotAffectAsk(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "ok"}}
	router := newTestRouter(t, client, 5)

	model := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "model",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"model": "anthropic/claude-fixture"},
	})
	if !strings.Contains(model.Content, "Unknown admin command") || strings.Contains(model.Content, "anthropic/claude-fixture") {
		t.Fatalf("expected legacy model command to be rejected without echoing slug, got %+v", model)
	}

	response := router.Handle(context.Background(), Request{
		Command:   "ask",
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"question": "hi"},
	})
	if response.Content != "ok" {
		t.Fatalf("unexpected ask response: %+v", response)
	}
	if len(client.requests) != 1 || client.requests[0].Model != "openrouter/auto" {
		t.Fatalf("expected ask to use operator model, got %+v", client.requests)
	}
}

func TestAdminBehaviorSetsRuntimeOptions(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "ok"}}
	router := newTestRouter(t, client, 5)

	behavior := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "behavior",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"temperature":         "0.7",
			"max_response_tokens": "1234",
			"tool_policy":         "read_only",
		},
	})
	if !strings.Contains(behavior.Content, "Behavior settings updated") || !strings.Contains(behavior.Content, "read_only") || strings.Contains(behavior.Content, "provider/") {
		t.Fatalf("unexpected behavior settings response: %+v", behavior)
	}

	response := router.Handle(context.Background(), Request{
		Command:   "ask",
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"question": "hi"},
	})
	if response.Content != "ok" {
		t.Fatalf("unexpected ask response: %+v", response)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one LLM request, got %d", len(client.requests))
	}
	request := client.requests[0]
	if request.Model != "openrouter/auto" || request.Temperature != 0.7 || request.MaxTokens != 1234 {
		t.Fatalf("unexpected LLM settings: %+v", request)
	}
}

func TestAdminDisableRequiresConfirmation(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 20)

	request := Request{Command: "admin", Subcommand: "disable", GuildID: "guild-1", UserID: "admin", IsGuildAdmin: true}
	pending := router.Handle(context.Background(), request)
	confirmationID := requireConfirmation(t, pending)
	allowed := router.Handle(context.Background(), Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"question": "hi"},
	})
	if allowed.Content != "ok" {
		t.Fatalf("unconfirmed disable should leave assistant enabled, got %+v", allowed)
	}

	request.Options = map[string]string{"confirm": confirmationID}
	disabled := router.Handle(context.Background(), request)
	if !strings.Contains(disabled.Content, "disabled") {
		t.Fatalf("expected confirmed disable, got %+v", disabled)
	}
	blocked := router.Handle(context.Background(), Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"question": "hi"},
	})
	if !blocked.Ephemeral || !strings.Contains(blocked.Content, "disabled") {
		t.Fatalf("confirmed disable should block assistant, got %+v", blocked)
	}
}

func TestAdminFeatureEnableWithoutNewDiscordPermissionsActivatesImmediately(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 20)
	repo := attachFeatureService(t, router)
	if err := repo.SetGuildFeatures(ctx, "guild-1", []string{features.AssistantChat}, "test", "admin", time.Now().UTC()); err != nil {
		t.Fatalf("SetGuildFeatures: %v", err)
	}

	response := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "feature",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "enable", "feature_id": features.WebSearch},
	})
	if !strings.Contains(response.Content, "Enabled features") || len(response.Actions) != 0 {
		t.Fatalf("expected immediate feature enable, got %+v", response)
	}
	enabled, err := repo.EnabledFeatureSet(ctx, "guild-1")
	if err != nil {
		t.Fatalf("EnabledFeatureSet: %v", err)
	}
	if !features.Has(enabled, features.WebSearch) || !features.Has(enabled, features.AssistantChat) {
		t.Fatalf("expected assistant and web search features enabled, got %+v", enabled)
	}
}

func TestAdminFeatureEnableWithNewDiscordPermissionsCreatesReauthorizationIntent(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 20)
	repo := attachFeatureService(t, router)
	if err := repo.SetGuildFeatures(ctx, "guild-1", []string{features.AdminSetup}, "test", "admin", time.Now().UTC()); err != nil {
		t.Fatalf("SetGuildFeatures: %v", err)
	}
	creator := &fakeFeatureInstallCreator{result: FeatureInstallIntentResult{
		AuthorizeURL: "https://discord.com/oauth2/authorize?state=reauth",
		ExpiresAt:    time.Now().UTC().Add(30 * time.Minute),
		Selection: features.Selection{
			ExpandedFeatureIDs:        []string{features.AdminSetup, features.DiscordRoleManagement},
			DiscordPermissionNames:    []string{"MANAGE_ROLES", "MANAGE_NICKNAMES", "VIEW_CHANNEL"},
			DiscordPermissionBitfield: "123",
		},
	}}
	router.WithFeatureInstallIntents(creator)

	response := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "feature",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "enable", "feature_id": features.DiscordRoleManagement},
	})
	if !strings.Contains(response.Content, "Reauthorization is required") || len(response.Actions) != 1 {
		t.Fatalf("expected reauthorization response, got %+v", response)
	}
	if len(creator.requests) != 1 {
		t.Fatalf("expected one install intent request, got %d", len(creator.requests))
	}
	if !containsString(creator.requests[0].FeatureIDs, features.AdminSetup) || !containsString(creator.requests[0].FeatureIDs, features.DiscordRoleManagement) {
		t.Fatalf("reauthorization should include current and requested features, got %+v", creator.requests[0].FeatureIDs)
	}
	enabled, err := repo.EnabledFeatureSet(ctx, "guild-1")
	if err != nil {
		t.Fatalf("EnabledFeatureSet: %v", err)
	}
	if features.Has(enabled, features.DiscordRoleManagement) {
		t.Fatalf("feature requiring new Discord permissions should not activate before callback, got %+v", enabled)
	}
}

func TestAdminFeatureDisableRemovesDependentFeatures(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 20)
	repo := attachFeatureService(t, router)
	if err := repo.SetGuildFeatures(ctx, "guild-1", []string{features.AssistantChat, features.Polls, features.Reminders}, "test", "admin", time.Now().UTC()); err != nil {
		t.Fatalf("SetGuildFeatures: %v", err)
	}

	response := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "feature",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "disable", "feature_id": features.AssistantChat},
	})
	if !strings.Contains(response.Content, "Disabled") {
		t.Fatalf("expected disable response, got %+v", response)
	}
	enabled, err := repo.EnabledFeatureSet(ctx, "guild-1")
	if err != nil {
		t.Fatalf("EnabledFeatureSet: %v", err)
	}
	if len(enabled) != 0 {
		t.Fatalf("disabling assistant_chat should remove dependent features, got %+v", enabled)
	}
}

func TestAdminDryRunDoesNotMutateState(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "ok"}}
	router := newTestRouter(t, client, 20)

	behavior := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "behavior",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"answer_length": "detailed", "dry_run": "true"},
	})
	if !strings.Contains(behavior.Content, "Dry run") || !strings.Contains(behavior.Content, "detailed") || strings.Contains(behavior.Content, "provider/") {
		t.Fatalf("expected behavior dry run response, got %+v", behavior)
	}
	ask := router.Handle(context.Background(), Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"question": "hi"},
	})
	if ask.Content != "ok" || len(client.requests) != 1 || client.requests[0].MaxTokens != 900 {
		t.Fatalf("dry-run behavior change should not affect ask request, response=%+v requests=%+v", ask, client.requests)
	}

	disabled := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "disable",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"dry_run": "true"},
	})
	if !strings.Contains(disabled.Content, "Dry run") || disabled.Confirmation != nil {
		t.Fatalf("expected disable dry run without confirmation, got %+v", disabled)
	}
	ask = router.Handle(context.Background(), Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"question": "hi"},
	})
	if ask.Content != "ok" {
		t.Fatalf("dry-run disable should leave assistant enabled, got %+v", ask)
	}

}

func TestAdminPromptWithoutTextUsesModal(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 20)

	prompt := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "prompt",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{},
	})
	if !prompt.Ephemeral || prompt.Modal == nil || prompt.Modal.ID == "" || len(prompt.Modal.Inputs) != 1 {
		t.Fatalf("expected prompt modal response, got %+v", prompt)
	}

	request, ok := RequestFromModalID(prompt.Modal.ID, map[string]string{ModalPromptInput: "Keep answers short."}, Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if !ok {
		t.Fatal("expected prompt modal to parse")
	}
	updated := router.Handle(context.Background(), request)
	if !strings.Contains(updated.Content, "Server prompt updated") {
		t.Fatalf("expected prompt update from modal, got %+v", updated)
	}

	_, ok = RequestFromModalID(prompt.Modal.ID, map[string]string{ModalPromptInput: "Nope"}, Request{UserID: "other-admin"})
	if ok {
		t.Fatal("prompt modal should be scoped to the original user")
	}
	_, ok = RequestFromModalID(prompt.Modal.ID, map[string]string{ModalPromptInput: ""}, Request{UserID: "admin"})
	if ok {
		t.Fatal("prompt modal should reject blank prompt text")
	}
}

func TestAdminSoulUpdatesSystemPrompt(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "ok"}}
	router := newTestRouter(t, client, 20)

	soul := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "soul",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"soul": "Be crisp, warm, and lightly irreverent."},
	})
	if !strings.Contains(soul.Content, "Agent soul updated") {
		t.Fatalf("expected soul update response, got %+v", soul)
	}

	response := router.Handle(context.Background(), Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"question": "hi"},
	})
	if response.Content != "ok" {
		t.Fatalf("unexpected ask response: %+v", response)
	}
	if len(client.requests) != 1 || !strings.Contains(joinRequestMessages(client.requests[0]), "lightly irreverent") {
		t.Fatalf("agent soul missing from ask request: %+v", client.requests)
	}
}

func TestAdminSoulWithoutTextUsesModal(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 20)

	soul := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "soul",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{},
	})
	if !soul.Ephemeral || soul.Modal == nil || soul.Modal.ID == "" || len(soul.Modal.Inputs) != 1 {
		t.Fatalf("expected soul modal response, got %+v", soul)
	}

	request, ok := RequestFromModalID(soul.Modal.ID, map[string]string{ModalSoulInput: "Make answers gentler."}, Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if !ok {
		t.Fatal("expected soul modal to parse")
	}
	updated := router.Handle(context.Background(), request)
	if !strings.Contains(updated.Content, "Agent soul updated") {
		t.Fatalf("expected soul update from modal, got %+v", updated)
	}

	_, ok = RequestFromModalID(soul.Modal.ID, map[string]string{ModalSoulInput: "Nope"}, Request{UserID: "other-admin"})
	if ok {
		t.Fatal("soul modal should be scoped to the original user")
	}
	_, ok = RequestFromModalID(soul.Modal.ID, map[string]string{ModalSoulInput: ""}, Request{UserID: "admin"})
	if ok {
		t.Fatal("soul modal should reject blank soul text")
	}
}

func TestConfirmationIDRestoresCommandRequest(t *testing.T) {
	base := Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	}
	id := adminDisableConfirmationID("admin")
	request, ok := RequestFromConfirmationID(id, base)
	if !ok {
		t.Fatal("expected confirmation id to parse")
	}
	if request.Command != "admin" || request.Subcommand != "disable" || request.Options["confirm"] != id {
		t.Fatalf("unexpected request from confirmation id: %+v", request)
	}

	dataID := dataDeleteConfirmationID("admin", "knowledge")
	request, ok = RequestFromConfirmationID(dataID, base)
	if !ok {
		t.Fatal("expected data deletion confirmation id to parse")
	}
	if request.Command != "data" || request.Subcommand != "delete" || request.Options["confirm"] != dataID || request.Options["scope"] != "knowledge" {
		t.Fatalf("unexpected data request from confirmation id: %+v", request)
	}

	base.UserID = "other-admin"
	if _, ok := RequestFromConfirmationID(id, base); ok {
		t.Fatal("confirmation id should be scoped to the original user")
	}
	if _, ok := RequestFromConfirmationID(dataID, base); ok {
		t.Fatal("data confirmation id should be scoped to the original user")
	}
}

func TestToolConfirmationIDRestoresScopedRequest(t *testing.T) {
	confirmation := ToolConfirmationFromAssistant("admin", &assistant.InteractionConfirmation{
		Action:       toolActionChannelRuleRemove,
		Arguments:    map[string]string{"channel_id": "channel-1"},
		ConfirmLabel: "Remove rule",
		Danger:       true,
	})
	if confirmation == nil || confirmation.ID == "" {
		t.Fatalf("expected tool confirmation id, got %+v", confirmation)
	}

	request, ok := RequestFromToolConfirmationID(confirmation.ID, Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if !ok {
		t.Fatal("expected tool confirmation id to parse")
	}
	if request.Action != toolActionChannelRuleRemove || request.Options["channel_id"] != "channel-1" {
		t.Fatalf("unexpected request from tool confirmation id: %+v", request)
	}
	if _, ok := RequestFromToolConfirmationID(confirmation.ID, Request{UserID: "other-admin"}); ok {
		t.Fatal("tool confirmation id should be scoped to the original user")
	}

	approve := ToolConfirmationFromAssistant("admin", &assistant.InteractionConfirmation{
		Action:       toolActionComposedToolApprove,
		Arguments:    map[string]string{"tool_name": "member_welcome", "version": "2"},
		ConfirmLabel: "Approve tool",
		Danger:       true,
	})
	if approve == nil || approve.ID == "" {
		t.Fatalf("expected composed tool confirmation id, got %+v", approve)
	}
	request, ok = RequestFromToolConfirmationID(approve.ID, Request{UserID: "admin"})
	if !ok || request.Action != toolActionComposedToolApprove || request.Options["tool_name"] != "member_welcome" || request.Options["version"] != "2" {
		t.Fatalf("unexpected composed confirmation request: request=%+v ok=%t", request, ok)
	}

	budget := ToolConfirmationFromAssistant("admin", &assistant.InteractionConfirmation{
		Action:       toolActionBudgetLimitSet,
		Arguments:    map[string]string{"scope": "guild", "subject_id": "guild-1", "limit": "12", "window_seconds": "3600"},
		ConfirmLabel: "Set limit",
		Danger:       true,
	})
	if budget == nil || budget.ID == "" {
		t.Fatalf("expected budget confirmation id, got %+v", budget)
	}
	request, ok = RequestFromToolConfirmationID(budget.ID, Request{UserID: "admin"})
	if !ok || request.Action != toolActionBudgetLimitSet || request.Options["limit"] != "12" || request.Options["window_seconds"] != "3600" {
		t.Fatalf("unexpected budget confirmation request: request=%+v ok=%t", request, ok)
	}
}

func TestToolConfirmationUsesPendingStoreForLongRoleName(t *testing.T) {
	userID := "123456789012345678"
	longName := strings.Repeat("a", 100)
	confirmation := ToolConfirmationFromAssistant(userID, &assistant.InteractionConfirmation{
		Action:       toolActionDiscordRoleCreate,
		Arguments:    map[string]string{"name": longName},
		ConfirmLabel: "Create role",
		Danger:       true,
	})
	if confirmation == nil || confirmation.ID == "" {
		t.Fatalf("expected role confirmation id, got %+v", confirmation)
	}
	if len(confirmation.ID) > 100 {
		t.Fatalf("confirmation id exceeds Discord component limit: len=%d id=%q", len(confirmation.ID), confirmation.ID)
	}
	if _, ok := RequestFromToolConfirmationID(confirmation.ID, Request{UserID: "other-admin"}); ok {
		t.Fatal("pending confirmation id should be scoped to the original user")
	}
	request, ok := RequestFromToolConfirmationID(confirmation.ID, Request{UserID: userID})
	if !ok || request.Action != toolActionDiscordRoleCreate || request.Options["name"] != longName {
		t.Fatalf("unexpected role confirmation request: request=%+v ok=%t", request, ok)
	}
}

func TestHandleToolConfirmationExecutesGenericDiscordWrite(t *testing.T) {
	provider := &fakeCommandDiscordProvider{}
	router := newTestRouter(t, &fakeLLM{}, 20, func(executor *tools.Executor) {
		executor.WithDiscordToolProvider(provider)
	})
	confirmation := ToolConfirmationFromAssistant("admin", &assistant.InteractionConfirmation{
		Action: toolActionDiscordWriteExecute,
		Arguments: map[string]string{
			"tool_name":      "discord.send_message",
			"arguments_json": `{"channel_id":"channel-1","content":"hello"}`,
		},
		ConfirmLabel: "Confirm write",
		Danger:       true,
	})
	if confirmation == nil || confirmation.ID == "" {
		t.Fatalf("expected generic Discord write confirmation, got %+v", confirmation)
	}
	confirmationRequest, ok := RequestFromToolConfirmationID(confirmation.ID, Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if !ok {
		t.Fatal("expected generic Discord write confirmation id to parse")
	}

	response := router.HandleToolConfirmation(context.Background(), confirmationRequest)
	if !strings.Contains(response.Content, "Completed `discord.send_message`.") || response.Presentation.Accent != AccentSuccess {
		t.Fatalf("unexpected generic Discord write response: %+v", response)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("expected one confirmed Discord write, got %d", len(provider.requests))
	}
	request := provider.requests[0]
	if request.ToolName != "discord.send_message" || request.GuildID != "guild-1" || request.ChannelID != "channel-1" || request.ActorID != "admin" {
		t.Fatalf("unexpected confirmed Discord write request: %+v", request)
	}
	if request.Arguments["content"] != "hello" {
		t.Fatalf("confirmed write lost original arguments: %+v", request.Arguments)
	}
}

func TestHandleToolConfirmationRemovesChannelRule(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 20)
	if _, err := router.admin.SetChannelRule(context.Background(), "guild-1", "admin", "channel-1", "deny"); err != nil {
		t.Fatalf("seed channel rule: %v", err)
	}

	response := router.HandleToolConfirmation(context.Background(), ToolConfirmationRequest{
		Request: Request{
			GuildID:      "guild-1",
			ChannelID:    "channel-1",
			UserID:       "admin",
			IsGuildAdmin: true,
		},
		Action:  toolActionChannelRuleRemove,
		Options: map[string]string{"channel_id": "channel-1"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "Removed channel access rule") || response.Confirmation != nil {
		t.Fatalf("unexpected tool confirmation response: %+v", response)
	}
	rules, err := router.admin.ListChannelRules(context.Background(), "guild-1")
	if err != nil {
		t.Fatalf("list channel rules: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected channel rule to be removed, got %+v", rules)
	}
}

func TestHandleToolConfirmationSetsChannelRule(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 20)

	response := router.HandleToolConfirmation(context.Background(), ToolConfirmationRequest{
		Request: Request{
			GuildID:      "guild-1",
			ChannelID:    "channel-1",
			UserID:       "admin",
			IsGuildAdmin: true,
		},
		Action:  toolActionChannelRuleSet,
		Options: map[string]string{"channel_id": "channel-2", "rule": "allow"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "Set `allow` channel access rule") || response.Confirmation != nil {
		t.Fatalf("unexpected tool confirmation response: %+v", response)
	}
	rules, err := router.admin.ListChannelRules(context.Background(), "guild-1")
	if err != nil {
		t.Fatalf("list channel rules: %v", err)
	}
	if len(rules) != 1 || rules[0].ChannelID != "channel-2" || rules[0].Rule != "allow" {
		t.Fatalf("unexpected channel rules: %+v", rules)
	}
}

func TestHandleToolConfirmationAppliesRoleProfile(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 20)

	response := router.HandleToolConfirmation(context.Background(), ToolConfirmationRequest{
		Request: Request{
			GuildID:      "guild-1",
			UserID:       "admin",
			IsGuildAdmin: true,
		},
		Action:  toolActionRoleProfileAdd,
		Options: map[string]string{"role_id": "role-pickle", "profile": "moderator"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "Panda moderator role") {
		t.Fatalf("unexpected role profile confirmation response: %+v", response)
	}
	allowed, err := router.admin.CanUseModeration(context.Background(), admin.AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"role-pickle"}})
	if err != nil || !allowed {
		t.Fatalf("expected confirmed role profile to grant moderation, allowed=%t err=%v", allowed, err)
	}
}

func TestHandleToolConfirmationAssignsMemberRole(t *testing.T) {
	manager := &fakeMemberRoleManager{}
	router := newTestRouter(t, &fakeLLM{}, 20).WithMemberRoleManager(manager)

	response := router.HandleToolConfirmation(context.Background(), ToolConfirmationRequest{
		Request: Request{
			GuildID:      "guild-1",
			UserID:       "admin",
			IsGuildAdmin: true,
		},
		Action:  toolActionMemberRoleAdd,
		Options: map[string]string{"user_id": "user-target", "role_id": "role-pickle"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "Assigned role `role-pickle` to user `user-target`") {
		t.Fatalf("unexpected member role confirmation response: %+v", response)
	}
	if len(manager.adds) != 1 || manager.adds[0].UserID != "user-target" || manager.adds[0].RoleID != "role-pickle" {
		t.Fatalf("unexpected member role manager calls: %+v", manager.adds)
	}
}

func TestNaturalDiscordRoleCreateRendersConfirmationThroughAgentTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"create a new role called test","tool_name":"panda_manage_discord_role"}`},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-role-create",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_discord_role",
				Arguments: `{"action":"create","name":"test"}`,
			},
		}}},
		{Content: "Prepared Discord role `test`."},
	}}
	router := newTestRouter(t, client, 20)

	response := router.HandleNaturalMessage(context.Background(), Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"message": "panda create a new role called 'test'", "bot_mentioned": "true"},
	})
	if response.Confirmation == nil || !response.Confirmation.Danger || !strings.Contains(response.Content, "role `test`") {
		t.Fatalf("expected role creation confirmation, got %+v", response)
	}
	confirmationRequest, ok := RequestFromToolConfirmationID(response.Confirmation.ID, Request{UserID: "admin"})
	if !ok || confirmationRequest.Action != toolActionDiscordRoleCreate || confirmationRequest.Options["name"] != "test" {
		t.Fatalf("unexpected role confirmation id: request=%+v ok=%t", confirmationRequest, ok)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected classifier, role tool call, and final response, got %d request(s)", len(client.requests))
	}
	if !requestToolNames(client.requests[1])["panda_manage_discord_role"] {
		t.Fatalf("expected Discord role manager tool for natural role request, got %+v", requestToolNames(client.requests[1]))
	}
	if len(client.requests[1].Tools) != 1 {
		t.Fatalf("expected preferred role creation workflow to be the only exposed tool, got %+v", requestToolNames(client.requests[1]))
	}
}

func TestNaturalMemberRoleAssignmentRendersConfirmationThroughAgentTool(t *testing.T) {
	manager := &fakeMemberRoleManager{}
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"assign role role-pickle to user user-target","tool_name":"panda_manage_member_role"}`},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-member-role",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_member_role",
				Arguments: `{"action":"add","user_id":"user-target","role_id":"role-pickle"}`,
			},
		}}},
		{Content: "Prepared role assignment."},
	}}
	router := newTestRouter(t, client, 20, func(executor *tools.Executor) {
		executor.WithDiscordToolProvider(&fakeCommandDiscordProvider{})
	}).WithMemberRoleManager(manager)

	response := router.HandleNaturalMessage(context.Background(), Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"message": "panda assign role role-pickle to user user-target", "bot_mentioned": "true"},
	})
	if response.Confirmation == nil || response.Confirmation.ConfirmLabel != "Assign role" {
		t.Fatalf("expected member role confirmation, got %+v", response)
	}
	confirmationRequest, ok := RequestFromToolConfirmationID(response.Confirmation.ID, Request{UserID: "admin"})
	if !ok || confirmationRequest.Action != toolActionMemberRoleAdd || confirmationRequest.Options["user_id"] != "user-target" || confirmationRequest.Options["role_id"] != "role-pickle" {
		t.Fatalf("unexpected member role confirmation id: request=%+v ok=%t", confirmationRequest, ok)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected classifier, member-role tool call, and final response, got %d request(s)", len(client.requests))
	}
	if names := requestToolNames(client.requests[1]); len(names) != 1 || !names["panda_manage_member_role"] {
		t.Fatalf("expected preferred member-role workflow to be the only exposed tool, got %+v", names)
	}
}

func TestNaturalComposedScheduleCreatesThroughAgentTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"schedule welcome_builder in 10 minutes with input topic standup","tool_name":"panda_manage_schedule"}`},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-schedule-create",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_schedule",
				Arguments: `{"action":"create","tool_name":"welcome_builder","when":"in 10 minutes","input":{"topic":"standup"}}`,
			},
		}}},
		{Content: "Scheduled `welcome_builder`."},
	}}
	router := newTestRouter(t, client, 20)
	seedScheduledComposedTool(t, router, "guild-1", "admin", "welcome_builder")

	response := router.HandleNaturalMessage(context.Background(), Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"message":       `Panda schedule welcome_builder in 10 minutes with input {"topic":"standup"}`,
			"bot_mentioned": "true",
		},
	})
	if response.Content != "Scheduled `welcome_builder`." {
		t.Fatalf("expected final schedule response, got %+v", response)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected classifier, schedule tool call, and final response, got %d request(s)", len(client.requests))
	}
	if !requestToolNames(client.requests[1])["panda_manage_schedule"] {
		t.Fatalf("expected schedule manager tool to be available to admin natural chat, got %+v", requestToolNames(client.requests[1]))
	}
	if len(client.requests[1].Tools) != 1 {
		t.Fatalf("expected preferred schedule workflow to be the only exposed tool, got %+v", requestToolNames(client.requests[1]))
	}
	schedules, err := router.scheduler.List(context.Background(), "guild-1", "", scheduler.KindComposed, false, 25)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(schedules) != 1 || schedules[0].Kind != scheduler.KindComposed {
		t.Fatalf("expected one composed schedule, got %+v", schedules)
	}
	var payload scheduler.ComposedPayload
	if err := json.Unmarshal([]byte(schedules[0].Payload), &payload); err != nil {
		t.Fatalf("decode composed payload: %v", err)
	}
	if payload.ToolName != "welcome_builder" || payload.Input["topic"] != "standup" {
		t.Fatalf("unexpected composed payload: %+v", payload)
	}
	if !strings.Contains(joinRequestMessages(client.requests[2]), `"schedule_id"`) || !strings.Contains(joinRequestMessages(client.requests[2]), `"welcome_builder"`) {
		t.Fatalf("expected schedule tool result in final chat request, got:\n%s", joinRequestMessages(client.requests[2]))
	}
}

func TestNaturalComposedScheduleListsAndCancelsThroughAgentTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"list composed schedules for welcome_builder","tool_name":"panda_manage_schedule"}`},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-schedule-list",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_schedule",
				Arguments: `{"action":"list","tool_name":"welcome_builder"}`,
			},
		}}},
		{Content: "There is one scheduled `welcome_builder` run."},
		{Content: `{"respond":true,"prompt":"cancel scheduled composed tool welcome_builder","tool_name":"panda_manage_schedule"}`},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-schedule-cancel",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_schedule",
				Arguments: `{"action":"cancel","tool_name":"welcome_builder"}`,
			},
		}}},
		{Content: "Cancelled the scheduled `welcome_builder` run."},
	}}
	router := newTestRouter(t, client, 20)
	seedScheduledComposedTool(t, router, "guild-1", "admin", "welcome_builder")
	created, err := router.scheduler.CreateComposed(context.Background(), scheduler.CreateComposedRequest{
		GuildID:     "guild-1",
		ChannelID:   "channel-1",
		OwnerUserID: "admin",
		ToolName:    "welcome_builder",
		NextRunAt:   time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create composed schedule: %v", err)
	}

	listResponse := router.HandleNaturalMessage(context.Background(), Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"message": "Panda what composed tool schedules are set for welcome_builder?", "bot_mentioned": "true"},
	})
	if listResponse.Content != "There is one scheduled `welcome_builder` run." {
		t.Fatalf("expected model-rendered schedule list, got %+v", listResponse)
	}
	if !requestToolNames(client.requests[1])["panda_manage_schedule"] {
		t.Fatalf("expected schedule manager tool for list request, got %+v", requestToolNames(client.requests[1]))
	}
	if len(client.requests[1].Tools) != 1 {
		t.Fatalf("expected preferred schedule list workflow to be the only exposed tool, got %+v", requestToolNames(client.requests[1]))
	}
	listMessages := joinRequestMessages(client.requests[2])
	if !strings.Contains(listMessages, `"count":1`) || !strings.Contains(listMessages, `"welcome_builder"`) {
		t.Fatalf("expected schedule list tool result in final chat request, got:\n%s", listMessages)
	}

	cancelResponse := router.HandleNaturalMessage(context.Background(), Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"message": "Panda remove scheduled composed tool welcome_builder", "bot_mentioned": "true"},
	})
	if cancelResponse.Content != "Cancelled the scheduled `welcome_builder` run." {
		t.Fatalf("expected model-rendered schedule cancellation, got %+v", cancelResponse)
	}
	if !requestToolNames(client.requests[4])["panda_manage_schedule"] {
		t.Fatalf("expected schedule manager tool for cancel request, got %+v", requestToolNames(client.requests[4]))
	}
	if len(client.requests[4].Tools) != 1 {
		t.Fatalf("expected preferred schedule cancel workflow to be the only exposed tool, got %+v", requestToolNames(client.requests[4]))
	}
	cancelMessages := joinRequestMessages(client.requests[5])
	if !strings.Contains(cancelMessages, `"action":"cancel"`) || !strings.Contains(cancelMessages, `"schedule_id":`+createdIDString(created.ID)) {
		t.Fatalf("expected schedule cancellation tool result in final chat request, got:\n%s", cancelMessages)
	}
	active, err := router.scheduler.List(context.Background(), "guild-1", "", scheduler.KindComposed, false, 25)
	if err != nil {
		t.Fatalf("list active schedules: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("expected no active composed schedules after cancellation, got %+v", active)
	}
	if len(client.requests) != 6 {
		t.Fatalf("expected classifier/chat/final for list and cancel, got %d request(s)", len(client.requests))
	}
}

func createdIDString(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}

func TestHandleToolConfirmationCreatesDiscordRole(t *testing.T) {
	manager := &fakeDiscordRoleManager{role: DiscordRole{ID: "role-test", Name: "test"}}
	router := newTestRouter(t, &fakeLLM{}, 20).WithDiscordRoleManager(manager)

	response := router.HandleToolConfirmation(context.Background(), ToolConfirmationRequest{
		Request: Request{
			GuildID:      "guild-1",
			UserID:       "admin",
			IsGuildAdmin: true,
		},
		Action:  toolActionDiscordRoleCreate,
		Options: map[string]string{"name": "test"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "Created Discord role `test` (`role-test`)") {
		t.Fatalf("unexpected role creation response: %+v", response)
	}
	if len(manager.creates) != 1 || manager.creates[0].Name != "test" || manager.creates[0].GuildID != "guild-1" {
		t.Fatalf("unexpected role manager calls: %+v", manager.creates)
	}
}

func TestHandleToolConfirmationExplainsDiscordRoleSetupFailure(t *testing.T) {
	manager := &fakeDiscordRoleManager{err: ErrDiscordRoleSetup}
	router := newTestRouter(t, &fakeLLM{}, 20).WithDiscordRoleManager(manager)

	response := router.HandleToolConfirmation(context.Background(), ToolConfirmationRequest{
		Request: Request{
			GuildID:      "guild-1",
			UserID:       "admin",
			IsGuildAdmin: true,
		},
		Action:  toolActionDiscordRoleCreate,
		Options: map[string]string{"name": "test"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "Manage Roles") || !strings.Contains(response.Content, "move Panda's bot role") || !strings.Contains(response.Content, "try again") {
		t.Fatalf("expected setup guidance, got %+v", response)
	}
}

func TestLLMToolConfirmationRendersButton(t *testing.T) {
	const channelID = "100000000000000123"
	client := &fakeLLM{responses: []llm.ChatResponse{
		{
			Model:   "fixture/model",
			Content: "",
			ToolCalls: []llm.ToolCall{{
				ID:   "call-remove-channel",
				Type: "function",
				Function: llm.ToolCallFunction{
					Name:      "panda_manage_channel_rule",
					Arguments: `{"action":"remove","channel_id":"100000000000000123"}`,
				},
			}},
		},
		{Model: "fixture/model", Content: "I found the channel rule and prepared the removal."},
	}}
	router := newTestRouter(t, client, 20)
	behavior := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "behavior",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"tool_policy": "write_confirmed"},
	})
	if !strings.Contains(behavior.Content, "write_confirmed") {
		t.Fatalf("expected tool policy update, got %+v", behavior)
	}

	response := router.Handle(context.Background(), Request{
		Command:      "chat",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"question": "Panda remove the channel rule for " + channelID},
	})
	if response.Confirmation == nil || !response.Confirmation.Danger || !strings.Contains(response.Content, "confirmation button") {
		t.Fatalf("expected LLM-triggered confirmation response, got %+v", response)
	}
	request, ok := RequestFromToolConfirmationID(response.Confirmation.ID, Request{UserID: "admin"})
	if !ok || request.Action != toolActionChannelRuleRemove || request.Options["channel_id"] != channelID {
		t.Fatalf("unexpected rendered confirmation id: request=%+v ok=%t", request, ok)
	}
	if len(client.requests) != 2 || len(client.requests[0].Tools) == 0 {
		t.Fatalf("expected tool-enabled first model request, got %+v", client.requests)
	}
}

func TestLLMToolConfirmationsRenderMultipleButtons(t *testing.T) {
	const (
		channelID = "100000000000000123"
		roleID    = "100000000000000456"
	)
	client := &fakeLLM{responses: []llm.ChatResponse{
		{
			Model:   "fixture/model",
			Content: "",
			ToolCalls: []llm.ToolCall{{
				ID:   "call-allow-channel",
				Type: "function",
				Function: llm.ToolCallFunction{
					Name:      "panda_manage_channel_rule",
					Arguments: `{"action":"allow","channel_id":"` + channelID + `"}`,
				},
			}},
		},
		{
			Model:   "fixture/model",
			Content: "",
			ToolCalls: []llm.ToolCall{{
				ID:   "call-set-mod-role",
				Type: "function",
				Function: llm.ToolCallFunction{
					Name:      "panda_manage_role_permission",
					Arguments: `{"action":"add","role_id":"` + roleID + `","profile":"moderator"}`,
				},
			}},
		},
		{Model: "fixture/model", Content: "I prepared the channel rule and moderator role changes."},
	}}
	router := newTestRouter(t, client, 20)
	behavior := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "behavior",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"tool_policy": "write_confirmed"},
	})
	if !strings.Contains(behavior.Content, "write_confirmed") {
		t.Fatalf("expected tool policy update, got %+v", behavior)
	}

	response := router.Handle(context.Background(), Request{
		Command:      "chat",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"question": "Panda allow " + channelID + " and make " + roleID + " a moderator role"},
	})
	if response.Confirmation == nil || len(response.Confirmations) != 2 {
		t.Fatalf("expected two LLM-triggered confirmations, got %+v", response)
	}
	if !strings.Contains(response.Content, "Press each confirmation button") ||
		!strings.Contains(response.Content, "channel access rule") ||
		!strings.Contains(response.Content, "moderator") {
		t.Fatalf("expected plural confirmation copy, got %q", response.Content)
	}

	confirmed := map[string]ToolConfirmationRequest{}
	for _, confirmation := range response.Confirmations {
		request, ok := RequestFromToolConfirmationID(confirmation.ID, Request{UserID: "admin"})
		if !ok {
			t.Fatalf("confirmation id did not parse: %+v", confirmation)
		}
		confirmed[request.Action] = request
	}
	channelRequest, ok := confirmed[toolActionChannelRuleSet]
	if !ok || channelRequest.Options["channel_id"] != channelID || channelRequest.Options["rule"] != "allow" {
		t.Fatalf("unexpected channel-rule confirmation: %+v", confirmed)
	}
	roleRequest, ok := confirmed[toolActionRoleProfileAdd]
	if !ok || roleRequest.Options["role_id"] != roleID || roleRequest.Options["profile"] != "moderator" {
		t.Fatalf("unexpected role-profile confirmation: %+v", confirmed)
	}
	if len(client.requests) != 3 || len(client.requests[2].Messages) == 0 {
		t.Fatalf("expected initial and final LLM requests, got %+v", client.requests)
	}
	finalMessages := joinRequestMessages(client.requests[2])
	if !strings.Contains(finalMessages, `"action":"channel_rule.set"`) || !strings.Contains(finalMessages, `"action":"role_profile.add"`) {
		t.Fatalf("final request should include both tool results, got %s", finalMessages)
	}
}

func requireConfirmation(t *testing.T, response Response) string {
	t.Helper()
	if !response.Ephemeral || response.Confirmation == nil || response.Confirmation.ID == "" || !response.Confirmation.Danger {
		t.Fatalf("expected dangerous confirmation response, got %+v", response)
	}
	if !strings.Contains(response.Content, "confirmation button") {
		t.Fatalf("expected confirmation copy, got %+v", response)
	}
	return response.Confirmation.ID
}

func TestChatUsesAssistantService(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "chat fixture"}}, 5)
	response := router.Handle(context.Background(), Request{
		Command:   "chat",
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"question": "continue"},
	})
	if response.Content != "chat fixture" || response.Presentation.Title != "" {
		t.Fatalf("unexpected chat response: %+v", response)
	}
}

func TestChatEmptyAssistantResponseReleasesBillingReservation(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Model: "fixture/model"}}, 20)
	billingService, entitlement := attachTestBilling(t, router, "guild-1")

	response := router.handleChatModeWithAccess(ctx, Request{
		Command:      "chat",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"question": "hello"},
	}, false, false)
	if !response.Ephemeral || !strings.Contains(response.Content, "empty response") {
		t.Fatalf("expected empty response guard, got %+v", response)
	}
	reservation, err := billingService.BeginUsage(ctx, "guild-1", billing.MetricAIResponse, int64(entitlement.Plan.AIResponses))
	if err != nil {
		t.Fatalf("empty chat response should release billing reservation: %v", err)
	}
	if err := billingService.ReleaseUsage(ctx, reservation); err != nil {
		t.Fatalf("release verification reservation: %v", err)
	}
}

func TestBackgroundTaskEmptyAssistantResponseReleasesBillingReservation(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Model: "fixture/model"}}, 20)
	billingService, entitlement := attachTestBilling(t, router, "guild-1")

	response := router.HandleBackgroundTask(ctx, BackgroundTask{
		RequestID: "task-empty",
		Command:   "summarize",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "admin",
		Input:     "summarize this",
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "empty response") {
		t.Fatalf("expected empty response guard, got %+v", response)
	}
	reservation, err := billingService.BeginUsage(ctx, "guild-1", billing.MetricAIResponse, int64(entitlement.Plan.AIResponses))
	if err != nil {
		t.Fatalf("empty background response should release billing reservation: %v", err)
	}
	if err := billingService.ReleaseUsage(ctx, reservation); err != nil {
		t.Fatalf("release verification reservation: %v", err)
	}
}

func TestAssistantFeedbackEligible(t *testing.T) {
	longAnswer := strings.Repeat("This is a substantial assistant answer with enough context to review. ", 9)
	cases := []struct {
		name    string
		answer  assistant.AskResponse
		content string
		want    bool
	}{
		{
			name:    "short plain answer",
			content: "Queued that song.",
		},
		{
			name:    "long plain answer",
			content: longAnswer,
			want:    true,
		},
		{
			name:    "music card",
			answer:  assistant.AskResponse{Card: &assistant.ToolCard{Title: "Now playing", Accent: "music"}},
			content: longAnswer,
		},
		{
			name:    "web search",
			answer:  assistant.AskResponse{UsedWebSearch: true},
			content: "Current result with source links.",
			want:    true,
		},
		{
			name:    "empty web search",
			answer:  assistant.AskResponse{UsedWebSearch: true},
			content: "   ",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := assistantFeedbackEligible(tt.answer, tt.content); got != tt.want {
				t.Fatalf("assistantFeedbackEligible() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestChatAddsRecentInvocationContext(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "chat fixture"}}
	router := newTestRouter(t, client, 5).WithContextService(contextsvc.NewService(fakeContextProvider{messages: []contextsvc.Message{
		{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "old", AuthorID: "user-old", Content: "old unrelated chatter", CreatedAt: time.Now().UTC().Add(-3 * time.Minute)},
		{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "recent", AuthorID: "user-2", Content: "recent deploy context", CreatedAt: time.Now().UTC().Add(-90 * time.Second)},
	}}))

	response := router.Handle(context.Background(), Request{
		Command:   "chat",
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"question": "what changed?"},
	})
	if response.Content != "chat fixture" {
		t.Fatalf("unexpected chat response: %+v", response)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one LLM request, got %d", len(client.requests))
	}
	joined := joinRequestMessages(client.requests[0])
	if !strings.Contains(joined, "recent deploy context") || strings.Contains(joined, "old unrelated chatter") {
		t.Fatalf("expected recent invocation context only, got:\n%s", joined)
	}
	if !strings.Contains(joined, "ignore messages that are unrelated") {
		t.Fatalf("expected relevance instruction in invocation context, got:\n%s", joined)
	}
}

func TestChatUsesThreadManagerWhenAvailable(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "chat fixture"}}
	threadManager := &fakeThreadManager{thread: Thread{ID: "thread-1", Name: "Panda: continue", Created: true}}
	router := newTestRouter(t, client, 5).WithThreadManager(threadManager)

	response := router.Handle(context.Background(), Request{
		Command:   "chat",
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"question": "continue"},
	})
	if response.Content != "chat fixture" || response.ThreadID != "thread-1" {
		t.Fatalf("unexpected chat response: %+v", response)
	}
	if len(threadManager.calls) != 1 || !strings.HasPrefix(threadManager.calls[0].Title, "Panda:") {
		t.Fatalf("expected chat thread request, got %+v", threadManager.calls)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one LLM request, got %d", len(client.requests))
	}
	if client.requests[0].Model == "" {
		t.Fatalf("expected assistant request to be made: %+v", client.requests[0])
	}
}

func TestNaturalMessageUsesInlineChat(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"continue","tool_name":""}`},
		{Content: "chat fixture"},
	}}
	threadManager := &fakeThreadManager{thread: Thread{ID: "thread-1", Name: "Panda: continue", Created: true}}
	router := newTestRouter(t, client, 5).WithThreadManager(threadManager)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"message": "Panda continue", "bot_mentioned": "true"},
	})
	if response.Content != "chat fixture" || response.ThreadID != "" {
		t.Fatalf("unexpected natural message response: %+v", response)
	}
	if len(threadManager.calls) != 0 {
		t.Fatalf("natural messages should not create chat threads, got %+v", threadManager.calls)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected trigger and chat LLM requests, got %d", len(client.requests))
	}
	if !strings.Contains(joinRequestMessages(client.requests[0]), "Bot mentioned: true") {
		t.Fatalf("expected trigger request to include mention metadata: %+v", client.requests[0])
	}
}

func TestNaturalMessageSetsSoulThroughAgentTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"set your soul to Be crystalline and kind.","tool_name":""}`},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-soul-set",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_soul",
				Arguments: `{"action":"set","soul":"Be crystalline and kind."}`,
			},
		}}},
		{Content: "Agent soul updated."},
		{Content: "ok"},
	}}
	router := newTestRouter(t, client, 20)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:       "admin",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		IsGuildAdmin: true,
		Options: map[string]string{
			"message": "Panda set your soul to Be crystalline and kind.",
		},
	})
	if response.Content != "Agent soul updated." {
		t.Fatalf("expected model-rendered soul update, got %+v", response)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected classifier, soul tool call, and final response, got %d request(s)", len(client.requests))
	}
	if !requestToolNames(client.requests[1])["panda_manage_soul"] {
		t.Fatalf("expected soul management tool for natural soul update, got %+v", requestToolNames(client.requests[1]))
	}

	ask := router.Handle(context.Background(), Request{
		Command:   "ask",
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"question": "hi"},
	})
	if ask.Content != "ok" {
		t.Fatalf("unexpected ask response: %+v", ask)
	}
	if len(client.requests) != 4 || !strings.Contains(joinRequestMessages(client.requests[3]), "crystalline and kind") {
		t.Fatalf("agent soul missing from ask request: %+v", client.requests)
	}
}

func TestNaturalMessageSetsPromptThroughAgentTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"set your server instructions to Prefer moderator context before answering.","tool_name":"panda_manage_prompt"}`},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-prompt-set",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_prompt",
				Arguments: `{"action":"set","prompt":"Prefer moderator context before answering."}`,
			},
		}}},
		{Content: "Server prompt updated."},
		{Content: "ok"},
	}}
	router := newTestRouter(t, client, 20)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:       "admin",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		IsGuildAdmin: true,
		Options: map[string]string{
			"message": "Panda set your server instructions to Prefer moderator context before answering.",
		},
	})
	if response.Content != "Server prompt updated." {
		t.Fatalf("expected model-rendered prompt update, got %+v", response)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected classifier, prompt tool call, and final response, got %d request(s)", len(client.requests))
	}
	if names := requestToolNames(client.requests[1]); len(names) != 1 || !names["panda_manage_prompt"] {
		t.Fatalf("expected only prompt management tool for natural prompt update, got %+v", names)
	}

	ask := router.Handle(context.Background(), Request{
		Command:   "ask",
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"question": "hi"},
	})
	if ask.Content != "ok" {
		t.Fatalf("unexpected ask response: %+v", ask)
	}
	if len(client.requests) != 4 || !strings.Contains(joinRequestMessages(client.requests[3]), "Prefer moderator context before answering.") {
		t.Fatalf("server instructions missing from ask request: %+v", client.requests)
	}
}

func TestNaturalMessageDraftsEventAutomationThroughAgentTool(t *testing.T) {
	const channelID = "100000000000000123"
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"draft an automation for new role announcements","tool_name":""}`},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-composed-draft",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_composed_tool",
				Arguments: `{"action":"draft","request":"When a new role is created, post a short announcement in the target channel.","channel_id":"` + channelID + `"}`,
			},
		}}},
		{Content: `{
		"schema_version": 1,
		"name": "role_announcement",
		"description": "Posts an announcement when a Discord role is created.",
		"input_schema": {"type":"object","additionalProperties":false,"properties":{"role_id":{"type":"string"},"name":{"type":"string"}},"required":["role_id"]},
		"output_schema": {"type":"object","additionalProperties":false,"properties":{"sent":{"type":"boolean"},"message_id":{"type":"string"}},"required":["sent"]},
		"runner": {"type":"deterministic","system_prompt":"Post only the approved role announcement.","temperature":0.2,"max_tokens":300,"tool_allowlist":["discord.send_message"]},
		"steps": [{"id":"send_message","type":"tool_call","tool":"discord.send_message","arguments":{"channel_name":"bot-test","content_template":"A new role was created: {{name}} ({{role_id}}).","allowed_mentions":{"users":false,"roles":false,"everyone":false}}}],
		"invocations": [{"type":"event","event_type":"role_create"},{"type":"chat_tool"}],
		"safety": {"requires_approval":true,"requires_confirmation_on_write":false,"max_nested_depth":2,"cooldown_seconds":30,"max_runs_per_hour":20,"dedupe_window_seconds":300}
	}`},
		{Content: "Drafted `role_announcement` version 1 with `role_create` trigger."},
	}}
	router := newTestRouter(t, client, 20)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:       "admin",
		GuildID:      "guild-1",
		ChannelID:    "source-channel",
		IsGuildAdmin: true,
		Options: map[string]string{
			"message": "panda when a new role is created, post a short announcement in <#" + channelID + ">",
		},
	})
	if response.Confirmation == nil || !strings.Contains(response.Content, "Drafted `role_announcement` version 1") || !strings.Contains(response.Content, "`role_create`") {
		t.Fatalf("expected natural automation draft confirmation, got %+v", response)
	}
	if len(client.requests) != 4 {
		t.Fatalf("expected classifier, composed-tool call, draft LLM, and final response, got %d LLM request(s)", len(client.requests))
	}
	if !requestToolNames(client.requests[1])["panda_manage_composed_tool"] {
		t.Fatalf("expected composed tool manager to be available to admin natural chat, got %+v", requestToolNames(client.requests[1]))
	}
	if !strings.Contains(joinRequestMessages(client.requests[2]), "When a new role is created") {
		t.Fatalf("expected draft request to include automation instruction, got:\n%s", joinRequestMessages(client.requests[2]))
	}

	confirmationRequest, ok := RequestFromToolConfirmationID(response.Confirmation.ID, Request{
		UserID:       "admin",
		GuildID:      "guild-1",
		ChannelID:    "source-channel",
		IsGuildAdmin: true,
	})
	if !ok || confirmationRequest.Action != toolActionComposedToolApprove || confirmationRequest.Options["tool_name"] != "role_announcement" {
		t.Fatalf("unexpected automation confirmation request: request=%+v ok=%t", confirmationRequest, ok)
	}
	approved := router.HandleToolConfirmation(context.Background(), confirmationRequest)
	if !strings.Contains(approved.Content, "Approved `role_announcement` version 1") {
		t.Fatalf("expected composed automation approval, got %+v", approved)
	}
	spec, ok, err := router.composed.ExportSpec(context.Background(), "guild-1", "role_announcement")
	if err != nil || !ok {
		t.Fatalf("export approved spec: ok=%t err=%v", ok, err)
	}
	if got := spec.Steps[0].Arguments["channel_id"]; got != channelID {
		t.Fatalf("expected channel mention to resolve to channel_id %s, got %+v", channelID, got)
	}
}

func TestNaturalMessageDraftsEveryTimeEventAutomationThroughAgentTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"draft a member welcome automation","tool_name":"panda_manage_composed_tool"}`},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-composed-draft",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_composed_tool",
				Arguments: `{"action":"draft","request":"Every time a new user enters the discord server, mention them in a welcome message in channel ID 1517943356074889276."}`,
			},
		}}},
		{Content: memberWelcomeSpecJSON()},
		{Content: "Drafted `member_welcome` version 1 with `guild.member.joined` trigger."},
	}}
	router := newTestRouter(t, client, 20)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:       "admin",
		GuildID:      "guild-1",
		ChannelID:    "source-channel",
		IsGuildAdmin: true,
		Options: map[string]string{
			"message":       "panda every time a new user enters the discord server mention them in a welcome message in <#1517943356074889276>",
			"bot_mentioned": "true",
		},
	})
	if response.Confirmation == nil || !strings.Contains(response.Content, "Drafted `member_welcome` version 1") || !strings.Contains(response.Content, "`guild.member.joined`") {
		t.Fatalf("expected natural every-time automation draft confirmation, got %+v", response)
	}
	if strings.Contains(response.Content, "<tool_call>") {
		t.Fatalf("raw tool-call markup leaked into automation draft response: %q", response.Content)
	}
	if len(client.requests) != 4 {
		t.Fatalf("expected classifier, composed-tool call, draft LLM, and final response, got %d LLM request(s)", len(client.requests))
	}
	if names := requestToolNames(client.requests[1]); len(names) != 1 || !names["panda_manage_composed_tool"] {
		t.Fatalf("expected preferred composed tool manager to be the only natural chat tool, got %+v", names)
	}
	if !strings.Contains(joinRequestMessages(client.requests[2]), "Every time a new user enters") {
		t.Fatalf("expected draft request to include every-time instruction, got:\n%s", joinRequestMessages(client.requests[2]))
	}
}

func TestNaturalMessageRejectsTextToolCallMarkupForComposedTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"please draft the requested composed automation","tool_name":""}`},
		{Content: `<tool_call>panda_manage_composed_tool
<arg_key>action</arg_key>
<arg_value>draft</arg_value>
<arg_key>request</arg_key>
<arg_value>Every time a new user enters the discord server, mention them in a welcome message in channel ID 1517943356074889276 with a funny greeting</arg_value>
</tool_call>`},
	}}
	router := newTestRouter(t, client, 20)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:       "admin",
		GuildID:      "guild-1",
		ChannelID:    "source-channel",
		IsGuildAdmin: true,
		Options: map[string]string{
			"message":       "panda please handle this admin request",
			"bot_mentioned": "true",
		},
	})
	if response.Confirmation != nil {
		t.Fatalf("text tool-call markup should not create a confirmation, got %+v", response.Confirmation)
	}
	if strings.Contains(response.Content, "<tool_call>") || strings.Contains(response.Content, "panda_manage_composed_tool") {
		t.Fatalf("raw text tool-call markup leaked into response: %q", response.Content)
	}
	if !strings.Contains(response.Content, "not available") || !strings.Contains(response.Content, "did not take any action") {
		t.Fatalf("expected unavailable tool response, got %+v", response)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected classifier and natural chat request only, got %d", len(client.requests))
	}
	if !requestToolNames(client.requests[1])["panda_manage_composed_tool"] {
		t.Fatalf("expected composed tool manager to be available to admin natural chat, got %+v", requestToolNames(client.requests[1]))
	}
}

func TestNaturalMessageCreatesNativePollThroughAgentTool(t *testing.T) {
	discordProvider := &fakeCommandDiscordProvider{}
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"make a poll about fable 5 versus gpt 5.6","tool_name":"discord_create_poll"}`},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-poll-create",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "discord_create_poll",
				Arguments: `{"channel_id":"channel-1","question":"What will be better: fable 5 or gpt 5.6?","answers":["fable 5","gpt 5.6"],"dry_run":true}`,
			},
		}}},
		{Content: "Prepared the poll."},
	}}
	router := newTestRouter(t, client, 5, func(executor *tools.Executor) {
		executor.WithDiscordToolProvider(discordProvider)
	})

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:       "admin",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		IsGuildAdmin: true,
		Options: map[string]string{
			"message":       "panda make a poll. what will be better fable 5 or gpt 5.6",
			"bot_mentioned": "true",
		},
	})
	if response.Confirmation == nil || response.Confirmation.ConfirmLabel != "Send poll" {
		t.Fatalf("expected poll send confirmation, got %+v", response)
	}
	if !strings.Contains(response.Content, "Panda prepared a native Discord poll") || response.Poll != nil {
		t.Fatalf("expected poll confirmation response, got %+v", response)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected classifier, poll tool call, and final response, got %d request(s)", len(client.requests))
	}
	if !requestToolNames(client.requests[1])["discord_create_poll"] {
		t.Fatalf("expected Discord poll tool for natural poll request, got %+v", requestToolNames(client.requests[1]))
	}
	if len(client.requests[1].Tools) != 1 {
		t.Fatalf("expected preferred poll workflow to be the only exposed tool, got %+v", requestToolNames(client.requests[1]))
	}
	if !strings.Contains(joinRequestMessages(client.requests[2]), "What will be better") {
		t.Fatalf("expected poll tool result in final chat request, got:\n%s", joinRequestMessages(client.requests[2]))
	}

	confirmationRequest, ok := RequestFromToolConfirmationID(response.Confirmation.ID, Request{
		RequestID:    "interaction-1",
		UserID:       "admin",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		IsGuildAdmin: true,
	})
	if !ok {
		t.Fatalf("expected confirmation id to decode: %s", response.Confirmation.ID)
	}
	confirmed := router.HandleToolConfirmation(context.Background(), confirmationRequest)
	if confirmed.Content != "Sent the poll." {
		t.Fatalf("expected confirmed poll send, got %+v", confirmed)
	}
	if len(discordProvider.requests) != 1 || discordProvider.requests[0].ToolName != "discord.create_poll" {
		t.Fatalf("expected confirmed poll provider call, got %+v", discordProvider.requests)
	}
	if got := discordProvider.requests[0].Arguments["question"]; got != "What will be better: fable 5 or gpt 5.6?" {
		t.Fatalf("unexpected confirmed poll arguments: %+v", discordProvider.requests[0].Arguments)
	}
}

func TestNaturalMessageCreatesReminderThroughAgentTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"remind me in 10 minutes to stand up","tool_name":"panda_manage_reminder"}`},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-reminder-create",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_reminder",
				Arguments: `{"action":"create","message":"stand up","when":"in 10 minutes"}`,
			},
		}}},
		{Content: "Reminder created."},
	}}
	router := newTestRouter(t, client, 5)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options: map[string]string{
			"message":       "panda remind me in 10 minutes to stand up",
			"bot_mentioned": "true",
		},
	})
	if response.Content != "Reminder created." {
		t.Fatalf("expected model-rendered reminder response, got %+v", response)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected classifier, reminder tool call, and final response, got %d request(s)", len(client.requests))
	}
	if !requestToolNames(client.requests[1])["panda_manage_reminder"] {
		t.Fatalf("expected reminder tool for natural reminder request, got %+v", requestToolNames(client.requests[1]))
	}
	if len(client.requests[1].Tools) != 1 {
		t.Fatalf("expected preferred reminder workflow to be the only exposed tool, got %+v", requestToolNames(client.requests[1]))
	}
	schedules, err := router.scheduler.List(context.Background(), "guild-1", "user-1", scheduler.KindReminder, false, 25)
	if err != nil {
		t.Fatalf("list reminders: %v", err)
	}
	if len(schedules) != 1 || schedules[0].Kind != scheduler.KindReminder {
		t.Fatalf("expected one reminder, got %+v", schedules)
	}
}

func TestNaturalMessageManagesMusicThroughAgentTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"play ocean drive","tool_name":"panda_manage_music"}`},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-music-play",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_music",
				Arguments: `{"action":"play","query":"ocean drive"}`,
			},
		}}},
		{Content: "Queued ocean drive."},
	}}
	router := newTestRouter(t, client, 5)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:         "user-1",
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		VoiceChannelID: "voice-1",
		Options: map[string]string{
			"message":       "panda play ocean drive",
			"bot_mentioned": "true",
		},
	})
	if response.Content != "music handled" || response.Presentation.Title != "Now playing" || response.Presentation.Accent != AccentMusic {
		t.Fatalf("expected structured music card response, got %+v", response)
	}
	if response.Presentation.URL != "https://example.com/track" || len(response.Presentation.Fields) != 1 || len(response.Actions) != 1 {
		t.Fatalf("expected music card details, got %+v", response)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected classifier, music tool call, and final response, got %d request(s)", len(client.requests))
	}
	if names := requestToolNames(client.requests[1]); len(names) != 1 || !names["panda_manage_music"] {
		t.Fatalf("expected only music tool for natural music request, got %+v", names)
	}
	if !strings.Contains(joinRequestMessages(client.requests[1]), "natural-message classifier selected the exposed `panda_manage_music` workflow") {
		t.Fatalf("expected music tool instruction for natural music request, got:\n%s", joinRequestMessages(client.requests[1]))
	}
	if !strings.Contains(joinRequestMessages(client.requests[2]), "music handled") {
		t.Fatalf("expected music tool result in final chat request, got:\n%s", joinRequestMessages(client.requests[2]))
	}
}

func TestNaturalMessageExposesMusicWhenToolPolicyOff(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"play passport by mgk","tool_name":"panda_manage_music"}`},
		{Content: "music tool unavailable"},
	}}
	router := newTestRouter(t, client, 5)
	if _, err := router.admin.ConfigureBehavior(context.Background(), "guild-1", "admin", admin.BehaviorSettings{ToolPolicy: tools.ToolPolicyOff, ToolPolicySet: true}); err != nil {
		t.Fatalf("ConfigureBehavior: %v", err)
	}

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:         "user-1",
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		VoiceChannelID: "voice-1",
		Options: map[string]string{
			"message":       "panda play passport by mgk",
			"bot_mentioned": "true",
		},
	})
	if response.Content != "music tool unavailable" {
		t.Fatalf("unexpected natural message response: %+v", response)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected classifier and chat request, got %d request(s)", len(client.requests))
	}
	if names := requestToolNames(client.requests[1]); len(names) != 1 || !names["panda_manage_music"] {
		t.Fatalf("expected only music tool with policy off, got %+v", names)
	}
	if !strings.Contains(joinRequestMessages(client.requests[1]), "natural-message classifier selected the exposed `panda_manage_music` workflow") {
		t.Fatalf("expected music tool instruction with policy off, got:\n%s", joinRequestMessages(client.requests[1]))
	}
}

func TestNaturalMessageSoulWriterCanBrainstormWithoutAssistantUse(t *testing.T) {
	ctx := context.Background()
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"let's brainstorm your soul before setting it","tool_name":""}`},
		{Content: "Let's shape a few options."},
	}}
	router := newTestRouter(t, client, 20)
	if _, err := router.admin.AddRolePermission(ctx, "guild-1", "admin", "role-chat", admin.PermissionAssistantUse); err != nil {
		t.Fatalf("AddRolePermission assistant use: %v", err)
	}
	if _, err := router.admin.AddRolePermission(ctx, "guild-1", "admin", "role-soul", admin.PermissionAssistantSoulWrite); err != nil {
		t.Fatalf("AddRolePermission soul write: %v", err)
	}

	response := router.HandleNaturalMessage(ctx, Request{
		UserID:    "soul-writer",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		RoleIDs:   []string{"role-soul"},
		Options: map[string]string{
			"message": "Panda let's brainstorm your soul before setting it",
		},
	})
	if response.Content != "Let's shape a few options." {
		t.Fatalf("unexpected natural soul brainstorm response: %+v", response)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected trigger and chat LLM requests, got %d", len(client.requests))
	}
	if !requestToolNames(client.requests[1])["panda_manage_soul"] {
		t.Fatalf("expected soul management tool for delegated soul writer, got %+v", client.requests[1].Tools)
	}
}

func TestNaturalMessagePassesReplyContextToChat(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"give me the full list by tool name","tool_name":""}`},
		{Content: "chat fixture"},
	}}
	router := newTestRouter(t, client, 5)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		RequestID: "message-current",
		Options: map[string]string{
			"message":             "can you give me a full list of these tools by tool name",
			"reply_text":          "Here's what I can do in this server: Reading / Info; Writing / Actions.",
			"reply_message_id":    "message-replied-to",
			"reply_author_is_bot": "true",
		},
	})
	if response.Content != "chat fixture" {
		t.Fatalf("unexpected natural message response: %+v", response)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected trigger and chat LLM requests, got %d", len(client.requests))
	}
	chatMessages := joinRequestMessages(client.requests[1])
	for _, want := range []string{"message-current", "message-replied-to", "Reading / Info", "Writing / Actions"} {
		if !strings.Contains(chatMessages, want) {
			t.Fatalf("expected chat request to preserve reply context %q, got:\n%s", want, chatMessages)
		}
	}
}

func TestNaturalMessageAdminGetsManagementToolsWhenPolicyOff(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"what can you do","tool_name":""}`},
		{Content: "chat fixture"},
	}}
	router := newTestRouter(t, client, 5)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:       "admin",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		IsGuildAdmin: true,
		Options:      map[string]string{"message": "Panda what can you do?", "bot_mentioned": "true"},
	})
	if response.Content != "chat fixture" {
		t.Fatalf("unexpected natural message response: %+v", response)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected trigger and chat LLM requests, got %d", len(client.requests))
	}
	names := requestToolNames(client.requests[1])
	for _, want := range []string{"panda_list_tools", "read_config", "panda_manage_soul", "panda_manage_prompt", "panda_manage_tool_access", "panda_manage_composed_tool", "panda_manage_channel_rule", "generate_workflow_json"} {
		if !names[want] {
			t.Fatalf("expected %s in admin natural-message tools, got %+v", want, names)
		}
	}
	if names["discord_send_message"] {
		t.Fatalf("discord_send_message should need Discord provider runtime wiring, got %+v", names)
	}
}

func TestNaturalMessageClassifiesTrailingPandaMention(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"what can you do","tool_name":""}`},
		{Content: "chat fixture"},
	}}
	router := newTestRouter(t, client, 5)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"message": "what can you do panda"},
	})
	if response.Content != "chat fixture" {
		t.Fatalf("unexpected natural message response: %+v", response)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected trigger and chat LLM requests, got %d", len(client.requests))
	}
	if !strings.Contains(joinRequestMessages(client.requests[0]), "what can you do panda") {
		t.Fatalf("expected exact message in classifier request, got:\n%s", joinRequestMessages(client.requests[0]))
	}
}

func TestGuildControlDoesNotGrantOwnerOpsTools(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)

	adminPermissions := router.allowedToolPermissions(context.Background(), Request{
		UserID:       "admin",
		GuildID:      "guild-1",
		IsGuildAdmin: true,
	})
	if _, ok := adminPermissions[admin.PermissionAdminConfigWrite]; !ok {
		t.Fatalf("expected guild admin to receive admin config permission: %+v", adminPermissions)
	}
	if _, ok := adminPermissions[admin.PermissionOwnerOps]; ok {
		t.Fatalf("guild admin must not receive owner ops permission: %+v", adminPermissions)
	}

	ownerPermissions := router.allowedToolPermissions(context.Background(), Request{
		UserID:  "owner",
		GuildID: "guild-1",
		IsOwner: true,
	})
	if _, ok := ownerPermissions[admin.PermissionOwnerOps]; !ok {
		t.Fatalf("expected bot owner to receive owner ops permission: %+v", ownerPermissions)
	}
}

func TestNaturalOwnerOpsUsesConfirmationButton(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `{"respond":true,"prompt":"drain the queue worker","tool_name":"panda_manage_ops"}`},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-ops-drain",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_ops",
				Arguments: `{"action":"drain"}`,
			},
		}}},
		{Content: "Prepared the worker drain."},
	}}
	router := newTestRouter(t, client, 5)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:       "owner",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		IsOwner:      true,
		IsGuildAdmin: true,
		Options:      map[string]string{"message": "Panda drain the queue worker.", "bot_mentioned": "true"},
	})
	if response.Confirmation == nil || !response.Confirmation.Danger || response.Confirmation.ConfirmLabel != "Drain worker" {
		t.Fatalf("expected owner ops confirmation, got %+v", response)
	}
	if names := requestToolNames(client.requests[1]); len(names) != 1 || !names["panda_manage_ops"] {
		t.Fatalf("expected only owner ops tool for natural owner ops, got %+v", names)
	}
	confirmationRequest, ok := RequestFromToolConfirmationID(response.Confirmation.ID, Request{
		UserID:       "owner",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		IsOwner:      true,
		IsGuildAdmin: true,
	})
	if !ok || confirmationRequest.Action != toolActionOwnerOpsDrain {
		t.Fatalf("expected owner ops confirmation request, got ok=%t request=%+v", ok, confirmationRequest)
	}
	confirmed := router.HandleToolConfirmation(context.Background(), confirmationRequest)
	if !confirmed.Ephemeral || !strings.Contains(confirmed.Content, "draining") {
		t.Fatalf("expected confirmed worker drain, got %+v", confirmed)
	}
}

func TestRegularUserGetsWebSearchPermissionByDefault(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)

	permissions := router.allowedToolPermissions(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		RoleIDs:   []string{"member"},
	})
	if _, ok := permissions[admin.PermissionAssistantWebSearch]; !ok {
		t.Fatalf("regular users should get web search permission by default: %+v", permissions)
	}
	if _, ok := permissions[admin.PermissionAdminConfigWrite]; ok {
		t.Fatalf("regular users should not get admin config write by default: %+v", permissions)
	}
}

func TestNaturalMessageDoesNotRespondWhenTriggerDeclines(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: `{"respond":false,"prompt":"","tool_name":""}`}}
	router := newTestRouter(t, client, 5)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"message": "ambient channel chatter"},
	})
	if response.Content != "" {
		t.Fatalf("expected no response, got %+v", response)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected only trigger LLM request, got %d", len(client.requests))
	}
}

func TestNaturalMessageStaysSilentWhenTriggerParsingFailsForDirectAddress(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `**Decision** yes, respond`},
		{Content: `**Still invalid**`},
	}}
	router := newTestRouter(t, client, 5)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"message": "hey panda what can you do?"},
	})
	if response.Content != "" {
		t.Fatalf("expected no response after failed classifier parse, got %+v", response)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected trigger request and trigger retry only, got %d", len(client.requests))
	}
}

func TestNaturalMessageStaysSilentWhenTriggerParsingFailsForAmbientMessage(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: `**Decision** yes, respond`}}
	router := newTestRouter(t, client, 5)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"message": "ambient channel chatter"},
	})
	if response.Content == "" {
		return
	}
	t.Fatalf("expected no response, got %+v", response)
}

func TestNaturalMessageStaysSilentWhenTriggerParsingFailsForPandaTopic(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: `**Decision** yes, respond`}}
	router := newTestRouter(t, client, 5)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"message": "red-panda facts are neat"},
	})
	if response.Content != "" {
		t.Fatalf("expected no response, got %+v", response)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected trigger LLM request and retry, got %d", len(client.requests))
	}
}

func TestChatReportsThreadCreationFailure(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "unused"}}
	router := newTestRouter(t, client, 5).WithThreadManager(&fakeThreadManager{err: errors.New("missing permission")})

	response := router.Handle(context.Background(), Request{
		Command:   "chat",
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"question": "continue"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "thread permissions") {
		t.Fatalf("expected thread failure response, got %+v", response)
	}
	if len(client.requests) != 0 {
		t.Fatalf("thread failure should not call LLM, got %d requests", len(client.requests))
	}
}

func TestThreadPermissionGatesThreadMode(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "chat fixture"}}
	threadManager := &fakeThreadManager{thread: Thread{ID: "thread-1", Name: "Panda chat", Created: true}}
	router := newTestRouter(t, client, 5).WithThreadManager(threadManager)
	if _, err := router.admin.AddRolePermission(context.Background(), "guild-1", "admin", "thread-role", admin.PermissionAssistantUseThreads); err != nil {
		t.Fatalf("add thread role permission: %v", err)
	}

	denied := router.Handle(context.Background(), Request{
		Command:   "chat",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		RoleIDs:   []string{"other-role"},
		Options:   map[string]string{"question": "continue"},
	})
	if !denied.Ephemeral || !strings.Contains(denied.Content, "thread mode") {
		t.Fatalf("expected thread permission denial, got %+v", denied)
	}

	allowed := router.Handle(context.Background(), Request{
		Command:   "chat",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		RoleIDs:   []string{"thread-role"},
		Options:   map[string]string{"question": "continue"},
	})
	if allowed.Content != "chat fixture" || allowed.ThreadID != "thread-1" {
		t.Fatalf("expected threaded chat response, got %+v", allowed)
	}
}

func TestExplainCanFetchReferencedMessageContext(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "explained"}}
	router := newTestRouter(t, client, 5).WithContextService(contextsvc.NewService(fakeContextProvider{messages: []contextsvc.Message{{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		MessageID: "message-1",
		AuthorID:  "user-1",
		Content:   "The deploy window moved to Friday.",
	}}}))

	response := router.Handle(context.Background(), Request{
		Command:   "explain",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-2",
		Options:   map[string]string{"message_id": "message-1"},
	})
	if response.Content != "explained" {
		t.Fatalf("unexpected explain response: %+v", response)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one LLM request, got %d", len(client.requests))
	}
	joined := joinLLMMessages(client.requests[0].Messages)
	if !strings.Contains(joined, "[S1]") || !strings.Contains(joined, "message_id=message-1") {
		t.Fatalf("expected cited context in LLM request, got %s", joined)
	}
}

func TestSummarizeCanUseExtractedAttachment(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "summary"}}
	router := newTestRouter(t, client, 5).WithAttachmentReader(fakeAttachmentReader{attachment: store.Attachment{
		ID:            42,
		GuildID:       "guild-1",
		Filename:      "notes.md",
		ExtractedText: "Deploy after review.",
	}})

	response := router.Handle(context.Background(), Request{
		Command:   "summarize",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"attachment_id": "42"},
	})
	if response.Content != "summary" {
		t.Fatalf("unexpected summarize response: %+v", response)
	}
	if len(client.requests) != 1 || !strings.Contains(joinLLMMessages(client.requests[0].Messages), "Deploy after review.") {
		t.Fatalf("expected extracted attachment text in LLM request: %+v", client.requests)
	}
}

func TestTaskAddsRecentInvocationContext(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "rewritten"}}
	router := newTestRouter(t, client, 5).WithContextService(contextsvc.NewService(fakeContextProvider{messages: []contextsvc.Message{
		{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "old", AuthorID: "user-old", Content: "old unrelated chatter", CreatedAt: time.Now().UTC().Add(-3 * time.Minute)},
		{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "recent", AuthorID: "user-2", Content: "recent release note context", CreatedAt: time.Now().UTC().Add(-90 * time.Second)},
	}}))

	response := router.Handle(context.Background(), Request{
		Command:   "rewrite",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"text": "make this clearer"},
	})
	if response.Content != "rewritten" {
		t.Fatalf("unexpected rewrite response: %+v", response)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one LLM request, got %d", len(client.requests))
	}
	joined := joinLLMMessages(client.requests[0].Messages)
	if !strings.Contains(joined, "recent release note context") || strings.Contains(joined, "old unrelated chatter") {
		t.Fatalf("expected recent invocation context only, got:\n%s", joined)
	}
}

func TestSummarizeAttachmentRequiresConfiguredReader(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	response := router.Handle(context.Background(), Request{
		Command:   "summarize",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"attachment_id": "42"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "Attachment lookup") {
		t.Fatalf("expected attachment lookup response, got %+v", response)
	}
}

func TestAttachmentPermissionGatesExtractedAttachmentContext(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "summary"}}
	router := newTestRouter(t, client, 5).WithAttachmentReader(fakeAttachmentReader{attachment: store.Attachment{
		ID:            42,
		GuildID:       "guild-1",
		Filename:      "notes.md",
		ExtractedText: "Deploy after review.",
	}})
	if _, err := router.admin.AddRolePermission(context.Background(), "guild-1", "admin", "attachment-role", admin.PermissionAssistantAttachments); err != nil {
		t.Fatalf("add attachment role permission: %v", err)
	}

	denied := router.Handle(context.Background(), Request{
		Command:   "summarize",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		RoleIDs:   []string{"other-role"},
		Options:   map[string]string{"attachment_id": "42"},
	})
	if !denied.Ephemeral || !strings.Contains(denied.Content, "attachment context") {
		t.Fatalf("expected attachment permission denial, got %+v", denied)
	}

	allowed := router.Handle(context.Background(), Request{
		Command:   "summarize",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		RoleIDs:   []string{"attachment-role"},
		Options:   map[string]string{"attachment_id": "42"},
	})
	if allowed.Content != "summary" {
		t.Fatalf("expected allowed attachment summary, got %+v", allowed)
	}
}

func TestSummarizeWithoutTextReportsMissingContextProvider(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "unused"}}, 5)
	response := router.Handle(context.Background(), Request{
		Command:   "summarize",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"recent_limit": "5"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "context fetching") {
		t.Fatalf("expected missing context provider response, got %+v", response)
	}
}

func TestLongSummarizeCanPrepareBackgroundTask(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "background summary"}}
	router := newTestRouter(t, client, 5).WithContextService(contextsvc.NewService(fakeContextProvider{messages: []contextsvc.Message{
		{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "recent", AuthorID: "user-2", Content: "recent async context", CreatedAt: time.Now().UTC().Add(-30 * time.Second)},
	}}))

	response := router.Handle(context.Background(), Request{
		Command:   "summarize",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options: map[string]string{
			"_async": "true",
			"text":   strings.Repeat("long summary input ", 190),
		},
	})
	if response.Background == nil || !strings.Contains(response.Content, "Queued long summary") {
		t.Fatalf("expected background task response, got %+v", response)
	}
	if len(client.requests) != 0 {
		t.Fatalf("preparing background task should not call LLM, got %+v", client.requests)
	}
	if !strings.Contains(response.Background.InvocationContext, "recent async context") {
		t.Fatalf("expected background task to preserve invocation context, got %+v", response.Background)
	}

	completed := router.HandleBackgroundTask(context.Background(), *response.Background)
	if completed.Content != "background summary" {
		t.Fatalf("expected completed background summary, got %+v", completed)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one LLM request after background execution, got %+v", client.requests)
	}
	if !strings.Contains(joinLLMMessages(client.requests[0].Messages), "recent async context") {
		t.Fatalf("expected background task context in LLM request, got %+v", client.requests[0])
	}
}

func TestRolePermissionGatesAssistantUse(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 5)
	if _, err := router.admin.AddRolePermission(context.Background(), "guild-1", "admin", "role-allowed", admin.PermissionAssistantUse); err != nil {
		t.Fatalf("add assistant role permission: %v", err)
	}

	denied := router.Handle(context.Background(), Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		RoleIDs:   []string{"role-other"},
		Options:   map[string]string{"question": "hi"},
	})
	if !denied.Ephemeral || !strings.Contains(denied.Content, "permission") {
		t.Fatalf("expected permission denial, got %+v", denied)
	}

	allowed := router.Handle(context.Background(), Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		RoleIDs:   []string{"role-allowed"},
		Options:   map[string]string{"question": "hi"},
	})
	if allowed.Content != "ok" {
		t.Fatalf("expected allowed response, got %+v", allowed)
	}
}

func TestChannelDenyGatesAssistantUse(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 5)
	if _, err := router.admin.SetChannelRule(context.Background(), "guild-1", "admin", "channel-1", "deny"); err != nil {
		t.Fatalf("set channel deny rule: %v", err)
	}

	response := router.Handle(context.Background(), Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"question": "hi"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "permission") {
		t.Fatalf("expected channel denial, got %+v", response)
	}
}

func TestOpsHealthRequiresOwner(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	denied := router.Handle(context.Background(), Request{Command: "ops", Subcommand: "health", UserID: "user-1"})
	if !denied.Ephemeral || !strings.Contains(denied.Content, "owner") {
		t.Fatalf("expected owner denial, got %+v", denied)
	}

	allowed := router.Handle(context.Background(), Request{Command: "ops", Subcommand: "health", UserID: "owner", IsOwner: true})
	if !allowed.Ephemeral || !strings.Contains(allowed.Content, "sqlite=ok") || !strings.Contains(allowed.Content, "shards=disabled") {
		t.Fatalf("expected health response, got %+v", allowed)
	}
}

func TestOpsDrainAndIncident(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	drain := router.Handle(context.Background(), Request{Command: "ops", Subcommand: "drain", UserID: "owner", IsOwner: true})
	if !strings.Contains(drain.Content, "draining") {
		t.Fatalf("unexpected drain response: %+v", drain)
	}
	incident := router.Handle(context.Background(), Request{
		Command:    "ops",
		Subcommand: "incident",
		UserID:     "owner",
		IsOwner:    true,
		Options:    map[string]string{"action": "enable"},
	})
	if !strings.Contains(incident.Content, "enabled") {
		t.Fatalf("unexpected incident response: %+v", incident)
	}
	health := router.Handle(context.Background(), Request{Command: "ops", Subcommand: "health", UserID: "owner", IsOwner: true})
	if !strings.Contains(health.Content, "draining=true") || !strings.Contains(health.Content, "incident=true") {
		t.Fatalf("expected health to show ops state, got %+v", health)
	}
}

func TestAdminLimitDeniesSecondAsk(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "ok"}}
	router := newTestRouter(t, client, 10)

	if _, err := router.admin.SetBudgetLimit(context.Background(), "guild-1", "admin", store.BudgetLimit{
		Scope:         repository.BudgetScopeUser,
		SubjectID:     "user-1",
		Limit:         1,
		WindowSeconds: int(time.Hour.Seconds()),
	}); err != nil {
		t.Fatalf("set budget limit: %v", err)
	}

	request := Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"question": "hi"},
	}
	first := router.Handle(context.Background(), request)
	if first.Content != "ok" {
		t.Fatalf("expected first request to pass, got %+v", first)
	}
	second := router.Handle(context.Background(), request)
	if !second.Ephemeral || !strings.Contains(second.Content, "budget is exhausted") {
		t.Fatalf("expected budget denial, got %+v", second)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected only one LLM request, got %d", len(client.requests))
	}
}

func joinLLMMessages(messages []llm.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n")
}
