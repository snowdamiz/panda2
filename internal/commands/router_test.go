package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
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
	"github.com/sn0w/panda2/internal/generated"
	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/memory"
	"github.com/sn0w/panda2/internal/ops"
	"github.com/sn0w/panda2/internal/pandainfo"
	"github.com/sn0w/panda2/internal/queue"
	"github.com/sn0w/panda2/internal/ratelimit"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/runtimecontrol"
	"github.com/sn0w/panda2/internal/scheduler"
	"github.com/sn0w/panda2/internal/store"
	"github.com/sn0w/panda2/internal/tools"
	"github.com/sn0w/panda2/internal/websearch"
	"github.com/sn0w/panda2/internal/youtube"
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

type fakeCommandWebSearch struct {
	response websearch.Response
	err      error
}

type fakeCommandYouTubeSummarizer struct {
	candidates []youtube.VideoCandidate
	err        error
	searches   []youtube.SearchRequest
}

type fakeFeatureInstallCreator struct {
	requests []FeatureInstallIntentRequest
	result   FeatureInstallIntentResult
	err      error
}

type fakeCommandDiscordProvider struct {
	requests []tools.DiscordToolRequest
	result   any
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
	title := "Now playing"
	content := "music handled"
	url := "https://example.com/track"
	if request.Action == "skip" {
		title = "Track skipped"
		content = "skipped current track"
		url = ""
	}
	if request.Action == "play" && strings.TrimSpace(request.Query) != "" {
		content = "playing " + request.Query
	}
	if request.Action == "skip_play" && strings.TrimSpace(request.Query) != "" {
		title = "Track replaced"
		content = "playing " + request.Query
	}
	return map[string]any{"result": map[string]any{
		"action":  request.Action,
		"query":   request.Query,
		"title":   title,
		"content": content,
		"url":     url,
		"fields": []map[string]any{
			{"name": "Duration", "value": "3:12", "inline": true},
		},
		"actions": []map[string]string{
			{"label": "Open track", "url": "https://example.com/track"},
		},
	}}, nil
}

func (f fakeCommandWebSearch) Search(context.Context, websearch.Request) (websearch.Response, error) {
	return f.response, f.err
}

func (f *fakeCommandYouTubeSummarizer) Configured() bool {
	return true
}

func (f *fakeCommandYouTubeSummarizer) Search(_ context.Context, request youtube.SearchRequest) ([]youtube.VideoCandidate, error) {
	f.searches = append(f.searches, request)
	if f.err != nil {
		return nil, f.err
	}
	return f.candidates, nil
}

func (f *fakeCommandYouTubeSummarizer) Summarize(context.Context, youtube.SummaryRequest) (youtube.SummaryResult, error) {
	return youtube.SummaryResult{
		Title:      "Fixture video",
		URL:        "https://www.youtube.com/watch?v=fixture",
		Transcript: "fixture transcript",
	}, nil
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
	if f.result != nil {
		return f.result, nil
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

func (f *fakeLLM) StreamChat(ctx context.Context, request llm.ChatRequest, onDelta llm.ChatStreamHandler) (llm.ChatResponse, error) {
	response, err := f.Chat(ctx, request)
	if err != nil {
		return llm.ChatResponse{}, err
	}
	if onDelta != nil {
		if response.Content != "" {
			if err := onDelta(llm.ChatStreamDelta{Content: response.Content}); err != nil {
				return llm.ChatResponse{}, err
			}
		}
		if len(response.ToolCalls) > 0 {
			if err := onDelta(llm.ChatStreamDelta{HasToolCall: true}); err != nil {
				return llm.ChatResponse{}, err
			}
		}
	}
	return response, nil
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
	router, _ := newTestRouterWithDeps(t, client, limit, configureExecutor...)
	return router
}

type testRouterDeps struct {
	guilds  *repository.GuildRepository
	configs *repository.GuildConfigRepository
}

func newTestRouterWithDeps(t *testing.T, client *fakeLLM, limit int, configureExecutor ...func(*tools.Executor)) (*Router, testRouterDeps) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	guilds := repository.NewGuildRepository(db.DB)
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
	assistantService := assistant.NewService(client, usage, configs, memoryService, conversations, "openrouter/auto", nil)
	adminService := admin.NewService(configs, usage, audit, memoryService, access, budgets, members).
		WithGuildRepository(guilds)
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
	router := NewRouter(
		adminService,
		assistantService,
		opsService,
		ratelimit.New(limit, time.Minute),
	).WithComposedService(composedService).WithScheduler(schedulerService).WithDataRepository(repository.NewGuildDataRepository(db.DB)).WithToolExecutor(toolExecutor)
	return router, testRouterDeps{guilds: guilds, configs: configs}
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

func attachRuntimeStatus(t *testing.T, router *Router) *runtimecontrol.Service {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "_runtime?mode=memory&cache=shared"
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open runtime store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service := runtimecontrol.NewService(repository.NewRuntimeStatusRepository(db.DB))
	router.WithRuntimeStatus(service)
	return service
}

func newCommandSafetyRepository(t *testing.T) *repository.UserSafetyRepository {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "safety.db"))
	if err != nil {
		t.Fatalf("open safety store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return repository.NewUserSafetyRepository(db.DB)
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

func voiceRickrollSpecJSON() string {
	return `{
		"schema_version": 1,
		"name": "voice_rickroll",
		"description": "Plays Rick Astley when the configured member enters the configured voice channel.",
		"input_schema": {"type":"object","additionalProperties":false,"properties":{"user_id":{"type":"string"},"channel_id":{"type":"string"}},"required":["user_id","channel_id"]},
		"output_schema": {"type":"object","additionalProperties":false,"properties":{"action":{"type":"string"},"content":{"type":"string"}},"required":["action"]},
		"runner": {"type":"deterministic","system_prompt":"Play only the approved song in the triggering voice channel.","temperature":0.2,"max_tokens":300,"tool_allowlist":["panda.manage_music"]},
		"steps": [{"id":"play_rickroll","type":"tool_call","tool":"panda.manage_music","arguments":{"action":"play","query":"Rick Astley - Never Gonna Give You Up","voice_channel_id":"{{channel_id}}"}}],
		"invocations": [{"type":"event","event_type":"voice_state_update","filters":{"channel_id":"100000000000000222","user_id":"100000000000000777"}},{"type":"chat_tool"}],
		"safety": {"requires_approval":true,"requires_confirmation_on_write":false,"max_nested_depth":2,"cooldown_seconds":30,"max_runs_per_hour":10,"dedupe_window_seconds":300}
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
	if !profile.Ephemeral || !strings.Contains(profile.Content, "`MOD` is now a Panda admin role") || strings.Contains(profile.Content, "role-mod") {
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

func TestAdminUserProfileConfiguresDelegatedAdminUser(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 5)
	profile := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "user",
		GuildID:      "guild-1",
		UserID:       "owner",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":           "set",
			"profile":          "admin",
			"member_user_id":   "user-mod",
			"member_user_name": "Mod Person",
		},
	})
	if !profile.Ephemeral || !strings.Contains(profile.Content, "`Mod Person` is now a Panda admin user") || strings.Contains(profile.Content, "user-mod") {
		t.Fatalf("expected user profile command to configure admin user, got %+v", profile)
	}

	help := router.Handle(ctx, Request{Command: "help", GuildID: "guild-1", UserID: "user-mod"})
	if !strings.Contains(help.Content, "**Admin setup through chat**") {
		t.Fatalf("expected admin user profile help to include admin setup:\n%s", help.Content)
	}

	behavior := router.Handle(ctx, Request{
		Command:    "admin",
		Subcommand: "behavior",
		GuildID:    "guild-1",
		UserID:     "user-mod",
		Options:    map[string]string{"tool_policy": "read_only"},
	})
	if !strings.Contains(behavior.Content, "Behavior settings updated") {
		t.Fatalf("expected admin user profile to allow behavior update, got %+v", behavior)
	}

	list := router.Handle(ctx, Request{Command: "admin", Subcommand: "user", GuildID: "guild-1", UserID: "owner", IsGuildAdmin: true, Options: map[string]string{"action": "list"}})
	if !strings.Contains(list.Content, "admin: the selected user") || strings.Contains(list.Content, "user-mod") {
		t.Fatalf("expected user profile list to include delegated admin user, got %+v", list)
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
	if !strings.Contains(list.Content, "allow-list active") || strings.Contains(list.Content, "channel-allowed") {
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

func TestAdminSetupLeavesChannelAccessOpenByDefault(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 5)

	setup := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "setup",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"channel_id": "channel-default"},
	})
	if !setup.Ephemeral || strings.Contains(setup.Content, "allowed channel") || strings.Contains(setup.Content, "No allow-listed") {
		t.Fatalf("setup should not seed or require channel allow rules, got %+v", setup)
	}
	rules, err := router.admin.ListChannelRules(ctx, "guild-1")
	if err != nil {
		t.Fatalf("ListChannelRules: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected setup to leave channel access unrestricted, got %+v", rules)
	}

	allowed := router.Handle(ctx, Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-other",
		UserID:    "user-1",
		Options:   map[string]string{"question": "hi"},
	})
	if allowed.Content != "ok" {
		t.Fatalf("expected unrestricted channel to work by default, got %+v", allowed)
	}
}

func TestAdminStatusWarnsWhenChannelAllowListIsActive(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 5)

	open := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "status",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if strings.Contains(open.Content, "No allow-listed") || strings.Contains(open.Content, "allow-list mode is active") {
		t.Fatalf("open default channel access should not warn, got %+v", open)
	}

	if _, err := router.admin.SetChannelRule(ctx, "guild-1", "admin", "channel-allowed", "allow"); err != nil {
		t.Fatalf("SetChannelRule: %v", err)
	}
	restricted := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "status",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if !strings.Contains(restricted.Content, "Channel allow-list mode is active") {
		t.Fatalf("expected allow-list status warning, got %+v", restricted)
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
	if !response.Ephemeral || !strings.Contains(response.Content, "`Pickle` is now a Panda moderator role") || strings.Contains(response.Content, "role-pickle") {
		t.Fatalf("expected moderator role profile response, got %+v", response)
	}
	allowed, err := router.admin.CanUseModeration(ctx, admin.AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"role-pickle"}})
	if err != nil || !allowed {
		t.Fatalf("expected Pickle role to grant moderation, allowed=%t err=%v", allowed, err)
	}
	list := router.Handle(ctx, Request{Command: "admin", Subcommand: "role", GuildID: "guild-1", UserID: "owner", IsGuildAdmin: true, Options: map[string]string{"action": "list"}})
	if !strings.Contains(list.Content, "moderator: the selected role") || strings.Contains(list.Content, "role-pickle") {
		t.Fatalf("expected role profile list to include moderator role, got %+v", list)
	}
}

func TestSafetyBypassAllowedForDelegatedPandaAdmin(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 5)
	if _, err := router.admin.AddUserPermission(ctx, "guild-1", "admin", "user-admin", admin.PermissionAdminBadge); err != nil {
		t.Fatalf("AddUserPermission: %v", err)
	}

	if !router.safetyBypassAllowed(ctx, Request{GuildID: "guild-1", UserID: "user-admin"}) {
		t.Fatal("expected direct Panda admin user permission to bypass safety")
	}
	if router.safetyBypassAllowed(ctx, Request{GuildID: "guild-1", UserID: "user-regular"}) {
		t.Fatal("regular user should not bypass safety")
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
	if !adminResponse.Ephemeral || !strings.Contains(adminResponse.Content, "the selected role is now a Panda admin role") || strings.Contains(adminResponse.Content, "role-staff") {
		t.Fatalf("expected admin role profile response, got %+v", adminResponse)
	}

	modRequest := ownerRequest
	modRequest.Options = map[string]string{"action": "set", "profile": "moderator", "role_id": "role-staff"}
	modResponse := router.Handle(ctx, modRequest)
	if !modResponse.Ephemeral || !strings.Contains(modResponse.Content, "the selected role is now a Panda moderator role") || strings.Contains(modResponse.Content, "role-staff") {
		t.Fatalf("expected moderator role profile response, got %+v", modResponse)
	}

	listRequest := ownerRequest
	listRequest.Options = map[string]string{"action": "list"}
	list := router.Handle(ctx, listRequest)
	for _, want := range []string{"admin: the selected role", "moderator: the selected role"} {
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
	if !response.Ephemeral || !strings.Contains(response.Content, "Assigned `Pickle` to `Snow`") || strings.Contains(response.Content, "role-pickle") || strings.Contains(response.Content, "user-target") {
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
	if !add.Ephemeral || !strings.Contains(add.Content, "Allowed `Searchers` to use `web.search`") || strings.Contains(add.Content, "role-search") {
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
	if !strings.Contains(list.Content, "`web.search` allow -> the selected role") || strings.Contains(list.Content, "role-search") {
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
	if !strings.Contains(remove.Content, "Removed the selected role from `web.search`") || strings.Contains(remove.Content, "role-search") {
		t.Fatalf("unexpected remove response: %+v", remove)
	}
}

func TestAdminToolRejectsEveryoneRoleAndOpensTools(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 5)

	rejected := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "tool",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":    "add",
			"tool_name": "panda.generate_image",
			"role_id":   "guild-1",
		},
	})
	if !rejected.Ephemeral || !strings.Contains(rejected.Content, "tool access cannot target @everyone as a role") {
		t.Fatalf("unexpected @everyone rejection response: %+v", rejected)
	}

	if _, err := router.admin.AddRolePermission(ctx, "guild-1", "admin", "role-image", admin.PermissionAssistantImageGeneration); err != nil {
		t.Fatalf("AddRolePermission: %v", err)
	}
	if _, err := router.admin.AddToolRole(ctx, "guild-1", "admin", "panda.generate_image", "role-image"); err != nil {
		t.Fatalf("AddToolRole: %v", err)
	}
	opened := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "tool",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":     "open",
			"tool_group": "image_tools",
		},
	})
	if !opened.Ephemeral || !strings.Contains(opened.Content, "Opened `panda.generate_image`, `panda.inspect_image` to everyone") {
		t.Fatalf("unexpected open response: %+v", opened)
	}

	if _, err := router.admin.AddRolePermission(ctx, "guild-1", "admin", "role-search", admin.PermissionAssistantWebSearch); err != nil {
		t.Fatalf("AddRolePermission web search: %v", err)
	}
	if _, err := router.admin.AddToolRole(ctx, "guild-1", "admin", "web.search", "role-search"); err != nil {
		t.Fatalf("AddToolRole web search: %v", err)
	}
	openedWeb := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "tool",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":    "open",
			"tool_name": "web_search",
		},
	})
	if !openedWeb.Ephemeral || !strings.Contains(openedWeb.Content, "Opened `web.search` to everyone") {
		t.Fatalf("unexpected web open response: %+v", openedWeb)
	}
	if !strings.Contains(openedWeb.Content, "Cleared 1 permission rule(s) and 1 tool access rule(s)") {
		t.Fatalf("expected web open to clear permission and tool restrictions: %+v", openedWeb)
	}

	if _, err := router.admin.AddToolRole(ctx, "guild-1", "admin", "welcome_builder", "role-builder"); err != nil {
		t.Fatalf("AddToolRole composed: %v", err)
	}
	openedComposed := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "tool",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":    "open",
			"tool_name": "welcome_builder",
		},
	})
	if !openedComposed.Ephemeral || !strings.Contains(openedComposed.Content, "Opened `welcome_builder` to everyone") {
		t.Fatalf("unexpected composed open response: %+v", openedComposed)
	}
	if !strings.Contains(openedComposed.Content, "Cleared 0 permission rule(s) and 1 tool access rule(s)") {
		t.Fatalf("expected composed open to clear only tool restrictions: %+v", openedComposed)
	}
}

func TestAdminToolConfiguresUserToolAccess(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 5)

	add := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "tool",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":           "add",
			"tool_name":        "panda.generate_image",
			"member_user_id":   "user-artist",
			"member_user_name": "Artist",
		},
	})
	if !add.Ephemeral || !strings.Contains(add.Content, "Allowed `Artist` to use `panda.generate_image`") || strings.Contains(add.Content, "user-artist") {
		t.Fatalf("unexpected user add response: %+v", add)
	}

	list := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "tool",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "list"},
	})
	if !strings.Contains(list.Content, "`panda.generate_image` allow -> the selected user") || strings.Contains(list.Content, "user-artist") {
		t.Fatalf("unexpected user list response: %+v", list)
	}

	remove := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "tool",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":    "remove",
			"tool_name": "panda.generate_image",
			"user_id":   "user-artist",
		},
	})
	if !strings.Contains(remove.Content, "Removed the selected user from `panda.generate_image`") || strings.Contains(remove.Content, "user-artist") {
		t.Fatalf("unexpected user remove response: %+v", remove)
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

func TestHandleToolConfirmationAppliesUserProfile(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 20)

	response := router.HandleToolConfirmation(context.Background(), ToolConfirmationRequest{
		Request: Request{
			GuildID:      "guild-1",
			UserID:       "admin",
			IsGuildAdmin: true,
		},
		Action:  toolActionUserProfileAdd,
		Options: map[string]string{"user_id": "user-pickle", "profile": "admin"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "Panda admin user") {
		t.Fatalf("unexpected user profile confirmation response: %+v", response)
	}
	allowed, err := router.admin.CanWriteConfig(context.Background(), admin.AssistantAccessRequest{GuildID: "guild-1", UserID: "user-pickle"})
	if err != nil || !allowed {
		t.Fatalf("expected confirmed user profile to grant admin control, allowed=%t err=%v", allowed, err)
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
	if !response.Ephemeral || !strings.Contains(response.Content, "Assigned the selected role to the selected user") || strings.Contains(response.Content, "role-pickle") || strings.Contains(response.Content, "user-target") {
		t.Fatalf("unexpected member role confirmation response: %+v", response)
	}
	if len(manager.adds) != 1 || manager.adds[0].UserID != "user-target" || manager.adds[0].RoleID != "role-pickle" {
		t.Fatalf("unexpected member role manager calls: %+v", manager.adds)
	}
}

func TestHandleToolConfirmationAppliesUserToolAccess(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 20)

	response := router.HandleToolConfirmation(context.Background(), ToolConfirmationRequest{
		Request: Request{
			GuildID:      "guild-1",
			UserID:       "admin",
			IsGuildAdmin: true,
		},
		Action:  toolActionToolAccessAdd,
		Options: map[string]string{"tool_name": "panda.generate_image", "user_id": "user-artist"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "Allowed the selected user to use `panda.generate_image`") || strings.Contains(response.Content, "user-artist") {
		t.Fatalf("unexpected user tool access confirmation response: %+v", response)
	}

	access, err := router.admin.ToolUserRoleAccess(context.Background(), "guild-1", "user-artist", nil)
	if err != nil {
		t.Fatalf("ToolUserRoleAccess: %v", err)
	}
	if len(access.AllowedTools) != 1 || access.AllowedTools[0] != "panda.generate_image" {
		t.Fatalf("expected user tool access, got %+v", access)
	}
}

func TestHandleToolConfirmationDeniesUserToolAccessWithoutExistingAllow(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 20)

	response := router.HandleToolConfirmation(context.Background(), ToolConfirmationRequest{
		Request: Request{
			GuildID:      "guild-1",
			UserID:       "admin",
			IsGuildAdmin: true,
		},
		Action:  toolActionToolAccessDeny,
		Options: map[string]string{"tool_name": "panda.generate_image", "user_id": "user-xer0"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "Denied the selected user from `panda.generate_image`") || strings.Contains(response.Content, "user-xer0") {
		t.Fatalf("unexpected user tool deny confirmation response: %+v", response)
	}

	access, err := router.admin.ToolUserRoleAccess(context.Background(), "guild-1", "user-xer0", nil)
	if err != nil {
		t.Fatalf("ToolUserRoleAccess: %v", err)
	}
	if len(access.AllowedTools) != 0 || len(access.DeniedTools) != 1 || access.DeniedTools[0] != "panda.generate_image" {
		t.Fatalf("expected direct user deny without allow, got %+v", access)
	}
}

func TestHandleToolConfirmationOpensImageToolAccess(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 20)

	if _, err := router.admin.AddRolePermission(ctx, "guild-1", "admin", "role-image", admin.PermissionAssistantImageGeneration); err != nil {
		t.Fatalf("AddRolePermission: %v", err)
	}
	if _, err := router.admin.AddUserPermission(ctx, "guild-1", "admin", "user-image", admin.PermissionAssistantImageGeneration); err != nil {
		t.Fatalf("AddUserPermission: %v", err)
	}
	if _, err := router.admin.AddToolRole(ctx, "guild-1", "admin", "panda.generate_image", "role-image"); err != nil {
		t.Fatalf("AddToolRole generate: %v", err)
	}
	if _, err := router.admin.AddToolUser(ctx, "guild-1", "admin", "panda.inspect_image", "user-image"); err != nil {
		t.Fatalf("AddToolUser inspect: %v", err)
	}

	response := router.HandleToolConfirmation(ctx, ToolConfirmationRequest{
		Request: Request{
			GuildID:      "guild-1",
			UserID:       "admin",
			IsGuildAdmin: true,
		},
		Action: toolActionToolAccessOpen,
		Options: map[string]string{
			"tool_names":  "panda.generate_image,panda.inspect_image",
			"permissions": admin.PermissionAssistantImageGeneration,
		},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "Opened `panda.generate_image`, `panda.inspect_image` to everyone") {
		t.Fatalf("unexpected open tool access confirmation response: %+v", response)
	}
	if !strings.Contains(response.Content, "Cleared 2 permission rule(s) and 2 tool access rule(s)") {
		t.Fatalf("unexpected clear counts: %+v", response)
	}
	rolePermissions, err := router.admin.ListRolePermissions(ctx, "guild-1")
	if err != nil {
		t.Fatalf("ListRolePermissions: %v", err)
	}
	userPermissions, err := router.admin.ListUserPermissions(ctx, "guild-1")
	if err != nil {
		t.Fatalf("ListUserPermissions: %v", err)
	}
	toolRules, err := router.admin.ListToolAccess(ctx, "guild-1")
	if err != nil {
		t.Fatalf("ListToolAccess: %v", err)
	}
	if len(rolePermissions) != 0 || len(userPermissions) != 0 || len(toolRules) != 0 {
		t.Fatalf("expected open image access to clear gates, roles=%+v users=%+v tools=%+v", rolePermissions, userPermissions, toolRules)
	}
}

func TestNaturalDiscordRoleCreateRendersConfirmationThroughAgentTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
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
	if len(client.requests) != 2 {
		t.Fatalf("expected streamed role tool call and final response, got %d request(s)", len(client.requests))
	}
	if !requestToolNames(client.requests[0])["panda_manage_discord_role"] {
		t.Fatalf("expected Discord role manager tool for natural role request, got %+v", requestToolNames(client.requests[0]))
	}
}

func TestNaturalMemberRoleAssignmentRendersConfirmationThroughAgentTool(t *testing.T) {
	manager := &fakeMemberRoleManager{}
	client := &fakeLLM{responses: []llm.ChatResponse{
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
	if len(client.requests) != 2 {
		t.Fatalf("expected streamed member-role tool call and final response, got %d request(s)", len(client.requests))
	}
	if names := requestToolNames(client.requests[0]); !names["panda_manage_member_role"] {
		t.Fatalf("expected member-role workflow to be available, got %+v", names)
	}
}

func TestNaturalUserToolAccessRendersConfirmationThroughAgentTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-tool-user-access",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_tool_access",
				Arguments: `{"action":"remove","tool_name":"panda.generate_image","user_id":"user-target"}`,
			},
		}}},
		{Content: "Prepared user tool access removal."},
	}}
	router := newTestRouter(t, client, 20)

	response := router.HandleNaturalMessage(context.Background(), Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"message": "panda remove user-target from image tool access", "bot_mentioned": "true"},
	})
	if response.Confirmation == nil || response.Confirmation.ConfirmLabel != "Remove panda.generate_image" {
		t.Fatalf("expected user tool access confirmation, got %+v", response)
	}
	confirmationRequest, ok := RequestFromToolConfirmationID(response.Confirmation.ID, Request{UserID: "admin"})
	if !ok || confirmationRequest.Action != toolActionToolAccessRemove || confirmationRequest.Options["tool_name"] != "panda.generate_image" || confirmationRequest.Options["user_id"] != "user-target" {
		t.Fatalf("unexpected user tool access confirmation id: request=%+v ok=%t", confirmationRequest, ok)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected streamed user tool access call and final response, got %d", len(client.requests))
	}
	if names := requestToolNames(client.requests[0]); !names["panda_manage_tool_access"] {
		t.Fatalf("expected tool access workflow to be available, got %+v", names)
	}
}

func TestNaturalSafetyStrikeRemovalRendersConfirmationThroughAgentTool(t *testing.T) {
	ctx := context.Background()
	var safety *repository.UserSafetyRepository
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-safety-remove",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_safety",
				Arguments: `{"action":"remove","user":"<@100000000000000222>"}`,
			},
		}}},
		{Content: "Prepared safety strike removal."},
	}}
	router := newTestRouter(t, client, 20, func(executor *tools.Executor) {
		safety = newCommandSafetyRepository(t)
		executor.WithUserSafetyRepository(safety)
	})
	if _, err := safety.AddStrike(ctx, "guild-1", "100000000000000222", 3, 10*time.Minute, time.Date(2026, time.June, 25, 19, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed strike: %v", err)
	}

	response := router.HandleNaturalMessage(ctx, Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"message": "panda remove the strike from @xer0", "bot_mentioned": "true"},
	})
	if response.Confirmation == nil || response.Confirmation.ConfirmLabel != "Remove strike" {
		t.Fatalf("expected safety strike confirmation, got %+v", response)
	}
	confirmationRequest, ok := RequestFromToolConfirmationID(response.Confirmation.ID, Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if !ok || confirmationRequest.Action != toolActionSafetyStrikeRemove || confirmationRequest.Options["user_id"] != "100000000000000222" || confirmationRequest.Options["count"] != "1" {
		t.Fatalf("unexpected safety confirmation id: request=%+v ok=%t", confirmationRequest, ok)
	}
	status, err := safety.Status(ctx, "guild-1", "100000000000000222", time.Now().UTC())
	if err != nil {
		t.Fatalf("status before confirmation: %v", err)
	}
	if status.State.ActiveStrikes != 1 || status.State.TotalStrikes != 1 {
		t.Fatalf("natural confirmation preparation should not mutate safety state, got %+v", status)
	}

	confirmed := router.HandleToolConfirmation(ctx, confirmationRequest)
	if !confirmed.Ephemeral || !strings.Contains(confirmed.Content, "Removed 1 safety strike") {
		t.Fatalf("unexpected safety confirmation response: %+v", confirmed)
	}
	status, err = safety.Status(ctx, "guild-1", "100000000000000222", time.Now().UTC())
	if err != nil {
		t.Fatalf("status after confirmation: %v", err)
	}
	if status.State.ActiveStrikes != 0 || status.State.TotalStrikes != 0 {
		t.Fatalf("expected confirmed removal to clear strike counts, got %+v", status)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected streamed safety tool call and final response, got %d request(s)", len(client.requests))
	}
	if names := requestToolNames(client.requests[0]); !names["panda_manage_safety"] {
		t.Fatalf("expected safety management tool to be available, got %+v", names)
	}
}

func TestNaturalManualSafetyTimeoutRendersConfirmationThroughAgentTool(t *testing.T) {
	ctx := context.Background()
	var safety *repository.UserSafetyRepository
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-safety-timeout",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_safety",
				Arguments: `{"action":"timeout","user":"<@100000000000000222>","duration":"30 minutes"}`,
			},
		}}},
		{Content: "Prepared Panda timeout."},
	}}
	router := newTestRouter(t, client, 20, func(executor *tools.Executor) {
		safety = newCommandSafetyRepository(t)
		executor.WithUserSafetyRepository(safety)
	})

	response := router.HandleNaturalMessage(ctx, Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"message": "panda timeout @xer0 for 30 minutes", "bot_mentioned": "true"},
	})
	if response.Confirmation == nil || response.Confirmation.ConfirmLabel != "Timeout from Panda" {
		t.Fatalf("expected safety timeout confirmation, got %+v", response)
	}
	confirmationRequest, ok := RequestFromToolConfirmationID(response.Confirmation.ID, Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if !ok || confirmationRequest.Action != toolActionSafetyTimeout || confirmationRequest.Options["user_id"] != "100000000000000222" || confirmationRequest.Options["duration_seconds"] != "1800" {
		t.Fatalf("unexpected safety timeout confirmation id: request=%+v ok=%t", confirmationRequest, ok)
	}
	status, err := safety.Status(ctx, "guild-1", "100000000000000222", time.Now().UTC())
	if err != nil {
		t.Fatalf("status before confirmation: %v", err)
	}
	if status.TimedOut || status.State.TimeoutUntil != nil {
		t.Fatalf("natural confirmation preparation should not timeout the user, got %+v", status)
	}

	confirmed := router.HandleToolConfirmation(ctx, confirmationRequest)
	if !confirmed.Ephemeral || !strings.Contains(confirmed.Content, "Timed out") || !strings.Contains(confirmed.Content, "30 minutes") {
		t.Fatalf("unexpected safety timeout confirmation response: %+v", confirmed)
	}
	status, err = safety.Status(ctx, "guild-1", "100000000000000222", time.Now().UTC())
	if err != nil {
		t.Fatalf("status after confirmation: %v", err)
	}
	if !status.TimedOut || status.State.TimeoutUntil == nil {
		t.Fatalf("expected confirmed timeout to be active, got %+v", status)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected streamed safety tool call and final response, got %d request(s)", len(client.requests))
	}
	if names := requestToolNames(client.requests[0]); !names["panda_manage_safety"] || names["discord_timeout_member"] {
		t.Fatalf("expected safety management tool path without Discord timeout, got %+v", names)
	}
}

func TestNaturalDenyNamedUserToolAccessResolvesMember(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-deny-tool-user-access",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_tool_access",
				Arguments: `{"action":"deny","tool_name":"image generation tool","user":"@xer0"}`,
			},
		}}},
		{Content: "Prepared user tool access denial."},
	}}
	provider := &fakeCommandDiscordProvider{
		result: map[string]any{"members": []map[string]any{{
			"user": map[string]any{
				"id":          "100000000000000999",
				"username":    "xer0",
				"global_name": "xer0",
				"effective":   "xer0",
			},
		}}},
	}
	router := newTestRouter(t, client, 20, func(executor *tools.Executor) {
		executor.WithDiscordToolProvider(provider)
	})

	response := router.HandleNaturalMessage(context.Background(), Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"message": "panda dont allow @xer0 to use image generation tool", "bot_mentioned": "true"},
	})
	if response.Confirmation == nil || response.Confirmation.ConfirmLabel != "Deny panda.generate_image" {
		t.Fatalf("expected deny tool access confirmation, got %+v", response)
	}
	confirmationRequest, ok := RequestFromToolConfirmationID(response.Confirmation.ID, Request{UserID: "admin"})
	if !ok || confirmationRequest.Action != toolActionToolAccessDeny || confirmationRequest.Options["tool_name"] != "panda.generate_image" || confirmationRequest.Options["user_id"] != "100000000000000999" {
		t.Fatalf("unexpected deny tool access confirmation id: request=%+v ok=%t", confirmationRequest, ok)
	}
	if len(provider.requests) != 1 || provider.requests[0].ToolName != "discord.list_members" {
		t.Fatalf("expected Discord member lookup, got %+v", provider.requests)
	}
}

func TestNaturalDenyNamedUserChatAccessResolvesMember(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-deny-chat-user-access",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_tool_access",
				Arguments: `{"action":"deny","tool_name":"interacting","user":"@xer0"}`,
			},
		}}},
		{Content: "Prepared user chat denial."},
	}}
	provider := &fakeCommandDiscordProvider{
		result: map[string]any{"members": []map[string]any{{
			"user": map[string]any{
				"id":          "100000000000000999",
				"username":    "xer0",
				"global_name": "xer0",
				"effective":   "xer0",
			},
		}}},
	}
	router := newTestRouter(t, client, 20, func(executor *tools.Executor) {
		executor.WithDiscordToolProvider(provider)
	})

	response := router.HandleNaturalMessage(context.Background(), Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"message": "panda don't interact with @xer0", "bot_mentioned": "true"},
	})
	if response.Confirmation == nil || response.Confirmation.ConfirmLabel != "Stop replying" {
		t.Fatalf("expected deny chat access confirmation, got %+v", response)
	}
	confirmationRequest, ok := RequestFromToolConfirmationID(response.Confirmation.ID, Request{UserID: "admin"})
	if !ok || confirmationRequest.Action != toolActionToolAccessDeny || confirmationRequest.Options["tool_name"] != tools.ToolNamePandaChat || confirmationRequest.Options["user_id"] != "100000000000000999" {
		t.Fatalf("unexpected deny chat access confirmation id: request=%+v ok=%t", confirmationRequest, ok)
	}
	if len(provider.requests) != 1 || provider.requests[0].ToolName != "discord.list_members" {
		t.Fatalf("expected Discord member lookup, got %+v", provider.requests)
	}
}

func TestNaturalOpenImageToolAccessRendersConfirmationThroughAgentTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-open-image-tool-access",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_tool_access",
				Arguments: `{"action":"open","tool_group":"image_tools"}`,
			},
		}}},
		{Content: "Prepared opening image tools to everyone."},
	}}
	router := newTestRouter(t, client, 20)

	response := router.HandleNaturalMessage(context.Background(), Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"message": "panda allow everyone to be able to use image tools", "bot_mentioned": "true"},
	})
	if response.Confirmation == nil || response.Confirmation.ConfirmLabel != "Open tool access" {
		t.Fatalf("expected open tool access confirmation, got %+v", response)
	}
	confirmationRequest, ok := RequestFromToolConfirmationID(response.Confirmation.ID, Request{UserID: "admin"})
	if !ok || confirmationRequest.Action != toolActionToolAccessOpen {
		t.Fatalf("unexpected open tool access confirmation id: request=%+v ok=%t", confirmationRequest, ok)
	}
	if toolNames := confirmationRequest.Options["tool_names"]; !strings.Contains(toolNames, "panda.generate_image") || !strings.Contains(toolNames, "panda.inspect_image") {
		t.Fatalf("expected image tool group expansion in confirmation options, got %+v", confirmationRequest.Options)
	}
	if confirmationRequest.Options["permissions"] != admin.PermissionAssistantImageGeneration {
		t.Fatalf("expected image permission in confirmation options, got %+v", confirmationRequest.Options)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected streamed open tool access call and final response, got %d", len(client.requests))
	}
	if names := requestToolNames(client.requests[0]); !names["panda_manage_tool_access"] {
		t.Fatalf("expected tool access workflow to be available, got %+v", names)
	}
}

func TestNaturalComposedScheduleCreatesThroughAgentTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
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
	if len(client.requests) != 2 {
		t.Fatalf("expected streamed schedule tool call and final response, got %d request(s)", len(client.requests))
	}
	if !requestToolNames(client.requests[0])["panda_manage_schedule"] {
		t.Fatalf("expected schedule manager tool to be available to admin natural chat, got %+v", requestToolNames(client.requests[0]))
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
	if !strings.Contains(joinRequestMessages(client.requests[1]), `"schedule_id"`) || !strings.Contains(joinRequestMessages(client.requests[1]), `"welcome_builder"`) {
		t.Fatalf("expected schedule tool result in final chat request, got:\n%s", joinRequestMessages(client.requests[1]))
	}
}

func TestNaturalComposedScheduleListsAndCancelsThroughAgentTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-schedule-list",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_schedule",
				Arguments: `{"action":"list","tool_name":"welcome_builder"}`,
			},
		}}},
		{Content: "There is one scheduled `welcome_builder` run."},
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
	if !requestToolNames(client.requests[0])["panda_manage_schedule"] {
		t.Fatalf("expected schedule manager tool for list request, got %+v", requestToolNames(client.requests[0]))
	}
	listMessages := joinRequestMessages(client.requests[1])
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
	if !requestToolNames(client.requests[2])["panda_manage_schedule"] {
		t.Fatalf("expected schedule manager tool for cancel request, got %+v", requestToolNames(client.requests[2]))
	}
	cancelMessages := joinRequestMessages(client.requests[3])
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
	if len(client.requests) != 4 {
		t.Fatalf("expected streamed tool/final requests for list and cancel, got %d request(s)", len(client.requests))
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
	if !response.Ephemeral || !strings.Contains(response.Content, "Created Discord role `test`") || strings.Contains(response.Content, "role-test") {
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
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-allow-channel",
					Type: "function",
					Function: llm.ToolCallFunction{
						Name:      "panda_manage_channel_rule",
						Arguments: `{"action":"allow","channel_id":"` + channelID + `"}`,
					},
				},
				{
					ID:   "call-set-mod-role",
					Type: "function",
					Function: llm.ToolCallFunction{
						Name:      "panda_manage_role_permission",
						Arguments: `{"action":"add","role_id":"` + roleID + `","profile":"moderator"}`,
					},
				},
			},
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
	if len(client.requests) != 2 || len(client.requests[1].Messages) == 0 {
		t.Fatalf("expected initial and final LLM requests, got %+v", client.requests)
	}
	finalMessages := joinRequestMessages(client.requests[1])
	if !strings.Contains(finalMessages, `"action":"channel_rule.set"`) || !strings.Contains(finalMessages, `"action":"role_profile.add"`) {
		t.Fatalf("final request should include both tool results, got %s", finalMessages)
	}
}

func TestLLMToolAccessRemovalConfirmationsNameEachTool(t *testing.T) {
	const userID = "100000000000000999"
	client := &fakeLLM{responses: []llm.ChatResponse{
		{
			Model:   "fixture/model",
			Content: "",
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-remove-chat-access",
					Type: "function",
					Function: llm.ToolCallFunction{
						Name:      "panda_manage_tool_access",
						Arguments: `{"action":"remove","tool_name":"panda.chat","user_id":"` + userID + `"}`,
					},
				},
				{
					ID:   "call-remove-image-access",
					Type: "function",
					Function: llm.ToolCallFunction{
						Name:      "panda_manage_tool_access",
						Arguments: `{"action":"remove","tool_name":"panda.generate_image","user_id":"` + userID + `"}`,
					},
				},
			},
		},
		{Model: "fixture/model", Content: "I prepared the tool access removals."},
	}}
	router := newTestRouter(t, client, 20)

	response := router.HandleNaturalMessage(context.Background(), Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"message": "remove all of these restrictions for that user", "bot_mentioned": "true"},
	})
	if response.Confirmation == nil || len(response.Confirmations) != 2 {
		t.Fatalf("expected two tool access confirmations, got %+v", response)
	}
	labels := map[string]bool{}
	requestsByTool := map[string]ToolConfirmationRequest{}
	for _, confirmation := range response.Confirmations {
		labels[confirmation.ConfirmLabel] = true
		request, ok := RequestFromToolConfirmationID(confirmation.ID, Request{UserID: "admin"})
		if !ok {
			t.Fatalf("confirmation id did not parse: %+v", confirmation)
		}
		requestsByTool[request.Options["tool_name"]] = request
	}
	if !labels["Resume replies"] || !labels["Remove panda.generate_image"] {
		t.Fatalf("expected tool-specific confirmation labels, got %+v", response.Confirmations)
	}
	for _, toolName := range []string{"panda.chat", "panda.generate_image"} {
		request, ok := requestsByTool[toolName]
		if !ok || request.Action != toolActionToolAccessRemove || request.Options["user_id"] != userID {
			t.Fatalf("unexpected confirmation request for %s: %+v", toolName, requestsByTool)
		}
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

func TestAssistantStandaloneCardWithEmptyContentDoesNotCreateFollowup(t *testing.T) {
	router := &Router{}
	response := router.responseFromAssistantAnswer(context.Background(), Request{}, assistant.AskResponse{
		Card: &assistant.ToolCard{
			Title:      "Connected to voice",
			Content:    "Joined <#100000000000000222> and started **track**.",
			Accent:     "music",
			Standalone: true,
		},
	}, "", "")

	if response.Content != "Joined <#100000000000000222> and started **track**." {
		t.Fatalf("expected card content as primary embed description, got %+v", response)
	}
	if response.Presentation.Title != "Connected to voice" || response.Presentation.Accent != AccentMusic {
		t.Fatalf("expected music card presentation, got %+v", response.Presentation)
	}
	if len(response.Followups) != 0 {
		t.Fatalf("empty standalone card prose should not create a followup, got %+v", response.Followups)
	}
}

func TestAssistantStandaloneCardWithPlaceholderContentDoesNotCreateFollowup(t *testing.T) {
	router := &Router{}
	response := router.responseFromAssistantAnswer(context.Background(), Request{}, assistant.AskResponse{
		Content: "...",
		Card: &assistant.ToolCard{
			Title:      "Music request failed",
			Content:    "I couldn't complete that music request. Please try again.",
			Accent:     "warning",
			Standalone: true,
		},
	}, "", "")

	if response.Content != "I couldn't complete that music request. Please try again." {
		t.Fatalf("expected card content as primary embed description, got %+v", response)
	}
	if response.Presentation.Title != "Music request failed" || response.Presentation.Accent != AccentWarning {
		t.Fatalf("expected warning music failure presentation, got %+v", response.Presentation)
	}
	if len(response.Followups) != 0 {
		t.Fatalf("placeholder standalone card prose should not create a followup, got %+v", response.Followups)
	}
}

func TestAssistantStandaloneCardWithCardMarkupDoesNotCreateFollowup(t *testing.T) {
	router := &Router{}
	response := router.responseFromAssistantAnswer(context.Background(), Request{}, assistant.AskResponse{
		Content: `<card>{
"title":"Track queued",
"content":"Queued **Edward Maya & Vika Jigulina - Stereo Love** at position 3."
}`,
		Card: &assistant.ToolCard{
			Title:      "Track queued",
			Content:    "Queued **Edward Maya & Vika Jigulina - Stereo Love** at position 3.",
			Accent:     "music",
			Standalone: true,
		},
	}, "", "")

	if response.Content != "Queued **Edward Maya & Vika Jigulina - Stereo Love** at position 3." {
		t.Fatalf("expected card content as primary embed description, got %+v", response)
	}
	if response.Presentation.Title != "Track queued" || response.Presentation.Accent != AccentMusic {
		t.Fatalf("expected music card presentation, got %+v", response.Presentation)
	}
	if len(response.Followups) != 0 {
		t.Fatalf("raw card markup should not create a followup, got %+v", response.Followups)
	}
}

func TestAssistantResponseWithGeneratedFileIsPayload(t *testing.T) {
	router := &Router{}
	answer := assistant.AskResponse{
		GeneratedFiles: []generated.File{{
			Filename: "panda-icon.png",
			MIMEType: "image/png",
			Data:     []byte("image-bytes"),
			AltText:  "Panda icon",
		}},
		UsageReservations: []billing.Reservation{{ID: "reservation-1", GuildID: "guild-1", Metric: billing.MetricImageGeneration, Units: 1}},
	}
	if !assistantAnswerHasPayload(answer) {
		t.Fatal("generated-file-only assistant response should count as payload")
	}
	response := router.responseFromAssistantAnswer(context.Background(), Request{}, answer, "", "")
	if len(response.GeneratedFiles) != 1 || response.GeneratedFiles[0].Filename != "panda-icon.png" {
		t.Fatalf("expected generated file on command response, got %+v", response.GeneratedFiles)
	}
	if len(response.UsageReservations) != 1 || response.UsageReservations[0].ID != "reservation-1" {
		t.Fatalf("expected usage reservation on command response, got %+v", response.UsageReservations)
	}
	answer.GeneratedFiles[0].Data[0] = 'X'
	if string(response.GeneratedFiles[0].Data) != "image-bytes" {
		t.Fatalf("response should clone generated file bytes, got %q", string(response.GeneratedFiles[0].Data))
	}
}

func TestRouterFinalizesImageUsageReservations(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "fixture"}}, 20)
	billingService, _ := attachTestBilling(t, router, "guild-1")

	commitReservation, err := billingService.BeginUsage(ctx, "guild-1", billing.MetricImageGeneration, 1)
	if err != nil {
		t.Fatalf("BeginUsage commit reservation: %v", err)
	}
	if err := router.CommitResponseUsage(ctx, Response{UsageReservations: []billing.Reservation{commitReservation}}); err != nil {
		t.Fatalf("CommitResponseUsage: %v", err)
	}
	entitlement, err := billingService.Resolve(ctx, "guild-1")
	if err != nil {
		t.Fatalf("Resolve committed usage: %v", err)
	}
	if entitlement.Usage.ImageGenerationsConsumed != 1 || entitlement.Usage.ImageGenerationsReserved != 0 {
		t.Fatalf("expected one committed image generation, got %+v", entitlement.Usage)
	}

	releaseReservation, err := billingService.BeginUsage(ctx, "guild-1", billing.MetricImageGeneration, 1)
	if err != nil {
		t.Fatalf("BeginUsage release reservation: %v", err)
	}
	if err := router.ReleaseResponseUsage(ctx, Response{UsageReservations: []billing.Reservation{releaseReservation}}); err != nil {
		t.Fatalf("ReleaseResponseUsage: %v", err)
	}
	entitlement, err = billingService.Resolve(ctx, "guild-1")
	if err != nil {
		t.Fatalf("Resolve released usage: %v", err)
	}
	if entitlement.Usage.ImageGenerationsConsumed != 1 || entitlement.Usage.ImageGenerationsReserved != 0 {
		t.Fatalf("expected released reservation to leave usage unchanged, got %+v", entitlement.Usage)
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

func TestSelectionChatDoesNotCreateThread(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "selection handled"}}
	threadManager := &fakeThreadManager{thread: Thread{ID: "thread-1", Name: "Panda: selected", Created: true}}
	router := newTestRouter(t, client, 5).WithThreadManager(threadManager)
	selection := PrepareSelectionForUser("user-1", &Selection{
		Options: []SelectionOption{{
			Label:   "Selected Video",
			Value:   "video_1",
			Command: "chat",
			Prompt:  "Summarize this exact YouTube video: https://www.youtube.com/watch?v=selected",
		}},
	})
	if selection == nil {
		t.Fatal("expected prepared selection")
	}
	request, ok := RequestFromSelectionID(selection.ID, []string{"video_1"}, Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
	})
	if !ok {
		t.Fatal("expected selection request")
	}

	response := router.Handle(context.Background(), request)
	if response.Content != "selection handled" || response.ThreadID != "" {
		t.Fatalf("unexpected selection response: %+v", response)
	}
	if len(threadManager.calls) != 0 {
		t.Fatalf("selection chat should not create a thread, got %+v", threadManager.calls)
	}
	if len(client.requests) != 1 || !strings.Contains(joinRequestMessages(client.requests[0]), "Summarize this exact YouTube video") {
		t.Fatalf("expected selected prompt to reach assistant, got %+v", client.requests)
	}
}

func TestNaturalMessageUsesInlineChat(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: "<panda_respond>\nchat fixture"},
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
	if len(client.requests) != 1 {
		t.Fatalf("expected one streamed natural chat request, got %d", len(client.requests))
	}
	if joined := joinRequestMessages(client.requests[0]); !strings.Contains(joined, "Bot mentioned: true") || !strings.Contains(joined, "Natural Discord response gate") {
		t.Fatalf("expected natural chat request to include mention metadata and response gate, got:\n%s", joined)
	}
}

func TestNaturalMessageEllipticalSummonUsesRecentConversationContext(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: "<panda_respond>\nI can answer from the recent conversation context."},
	}}
	now := time.Now().UTC()
	router := newTestRouter(t, client, 5).WithContextService(contextsvc.NewService(fakeContextProvider{messages: []contextsvc.Message{
		{
			GuildID:   "guild-1",
			ChannelID: "channel-1",
			MessageID: "message-prior",
			AuthorID:  "user-2",
			Content:   "How are you feeling about the new Panda bot? I like it, but do you think it can understand what we meant from earlier messages?",
			CreatedAt: now.Add(-30 * time.Second),
		},
	}}))

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		RequestID: "message-current",
		Options: map[string]string{
			"message": "Idk lets see, panda can you?",
		},
	})
	if response.Content != "I can answer from the recent conversation context." {
		t.Fatalf("unexpected natural message response: %+v", response)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one streamed natural chat request, got %d", len(client.requests))
	}
	joined := joinRequestMessages(client.requests[0])
	for _, want := range []string{
		"understand what we meant from earlier messages",
		"latest user message is short or elliptical",
		"use relevant recent Discord context",
		"Do not replace a context-resolved request with a generic capability overview",
		"Do not replace it with a generic capability rundown",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected contextual summon instruction/content %q, got:\n%s", want, joined)
		}
	}
}

func TestNaturalMessageSetsSoulThroughAgentTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
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
	if len(client.requests) != 2 {
		t.Fatalf("expected streamed soul tool call and final response, got %d request(s)", len(client.requests))
	}
	if !requestToolNames(client.requests[0])["panda_manage_soul"] {
		t.Fatalf("expected soul management tool for natural soul update, got %+v", requestToolNames(client.requests[0]))
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
	if len(client.requests) != 3 || !strings.Contains(joinRequestMessages(client.requests[2]), "crystalline and kind") {
		t.Fatalf("agent soul missing from ask request: %+v", client.requests)
	}
}

func TestNaturalMessageRefusesSoulUpdateForRegularUser(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: "<panda_respond>\nI can't update my soul for you."},
	}}
	router := newTestRouter(t, client, 20)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options: map[string]string{
			"message": "Panda update your soul to be crystalline and kind.",
		},
	})
	if response.Content != "I can't update my soul for you." {
		t.Fatalf("expected refusal response, got %+v", response)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one streamed natural chat request, got %d", len(client.requests))
	}
	if requestToolNames(client.requests[0])["panda_manage_soul"] {
		t.Fatalf("regular user should not receive soul management tool, got %+v", client.requests[0].Tools)
	}
	joined := joinRequestMessages(client.requests[0])
	for _, want := range []string{
		"Soul/personality persistence is not available to this caller",
		"respond that Panda can't update its soul for them",
		"Do not imply the soul was changed",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("natural soul refusal prompt missing %q:\n%s", want, joined)
		}
	}
}

func TestNaturalMessageSetsPromptThroughAgentTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
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
	if len(client.requests) != 2 {
		t.Fatalf("expected streamed prompt tool call and final response, got %d request(s)", len(client.requests))
	}
	if names := requestToolNames(client.requests[0]); !names["panda_manage_prompt"] {
		t.Fatalf("expected prompt management tool for natural prompt update, got %+v", names)
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
	if len(client.requests) != 3 || !strings.Contains(joinRequestMessages(client.requests[2]), "Prefer moderator context before answering.") {
		t.Fatalf("server instructions missing from ask request: %+v", client.requests)
	}
}

func TestNaturalMessageDraftsEventAutomationThroughAgentTool(t *testing.T) {
	const channelID = "100000000000000123"
	client := &fakeLLM{responses: []llm.ChatResponse{
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
	if len(client.requests) != 3 {
		t.Fatalf("expected streamed composed-tool call, draft LLM, and final response, got %d LLM request(s)", len(client.requests))
	}
	if !requestToolNames(client.requests[0])["panda_manage_composed_tool"] {
		t.Fatalf("expected composed tool manager to be available to admin natural chat, got %+v", requestToolNames(client.requests[0]))
	}
	if !strings.Contains(joinRequestMessages(client.requests[1]), "When a new role is created") {
		t.Fatalf("expected draft request to include automation instruction, got:\n%s", joinRequestMessages(client.requests[1]))
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
	if len(client.requests) != 3 {
		t.Fatalf("expected streamed composed-tool call, draft LLM, and final response, got %d LLM request(s)", len(client.requests))
	}
	if names := requestToolNames(client.requests[0]); !names["panda_manage_composed_tool"] {
		t.Fatalf("expected composed tool manager to be available, got %+v", names)
	}
	if !strings.Contains(joinRequestMessages(client.requests[1]), "Every time a new user enters") {
		t.Fatalf("expected draft request to include every-time instruction, got:\n%s", joinRequestMessages(client.requests[1]))
	}
}

func TestNaturalMessageDraftsVoiceMusicAutomationWithApprovalCard(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-composed-draft",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_composed_tool",
				Arguments: `{"action":"draft","request":"Every time <@100000000000000777> enters bot-test vc, play Rick Astley - Never Gonna Give You Up.","voice_channel_name":"bot-test"}`,
			},
		}}},
		{Content: voiceRickrollSpecJSON()},
		{Content: `{"result":{"confirmation_required":true,"spec":{"schema_version":1,"runner":{"tool_allowlist":["panda.manage_music"]}}}}`},
	}}
	router := newTestRouter(t, client, 20)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:       "admin",
		GuildID:      "guild-1",
		ChannelID:    "source-channel",
		IsGuildAdmin: true,
		Options: map[string]string{
			"message":       "panda every time <@100000000000000777> enters bot-test vc play the rick roll song",
			"bot_mentioned": "true",
		},
	})
	if response.Confirmation == nil || response.Confirmation.ConfirmLabel != "Approve tool" {
		t.Fatalf("expected voice automation approval confirmation, got %+v", response)
	}
	if !strings.Contains(response.Content, "Press the confirmation button") || !strings.Contains(response.Content, "voice_rickroll") {
		t.Fatalf("expected confirmation copy for voice automation, got %q", response.Content)
	}
	for _, leaked := range []string{`"result"`, "schema_version", "tool_allowlist", "confirmation_required"} {
		if strings.Contains(response.Content, leaked) {
			t.Fatalf("raw confirmation payload leaked into response content: %q", response.Content)
		}
	}
	confirmationRequest, ok := RequestFromToolConfirmationID(response.Confirmation.ID, Request{
		UserID:       "admin",
		GuildID:      "guild-1",
		ChannelID:    "source-channel",
		IsGuildAdmin: true,
	})
	if !ok || confirmationRequest.Action != toolActionComposedToolApprove || confirmationRequest.Options["tool_name"] != "voice_rickroll" {
		t.Fatalf("unexpected voice automation confirmation request: request=%+v ok=%t", confirmationRequest, ok)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected streamed composed-tool call, draft LLM, and final response, got %d LLM request(s)", len(client.requests))
	}
	if !strings.Contains(joinRequestMessages(client.requests[1]), "bot-test vc") {
		t.Fatalf("expected draft request to include voice automation instruction, got:\n%s", joinRequestMessages(client.requests[1]))
	}
	finalMessages := joinRequestMessages(client.requests[2])
	for _, leaked := range []string{`"spec"`, "schema_version", "tool_allowlist", "input_schema", "output_schema"} {
		if strings.Contains(finalMessages, leaked) {
			t.Fatalf("full confirmation payload leaked into final model request: %q", finalMessages)
		}
	}
	if !strings.Contains(finalMessages, "A Discord confirmation card was prepared") || !strings.Contains(finalMessages, "Panda prepared approval") {
		t.Fatalf("expected sanitized confirmation tool message in final model request, got:\n%s", finalMessages)
	}
}

func TestNaturalMessageCreatesNativePollThroughAgentTool(t *testing.T) {
	discordProvider := &fakeCommandDiscordProvider{}
	client := &fakeLLM{responses: []llm.ChatResponse{
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
	if len(client.requests) != 2 {
		t.Fatalf("expected streamed poll tool call and final response, got %d request(s)", len(client.requests))
	}
	if !requestToolNames(client.requests[0])["discord_create_poll"] {
		t.Fatalf("expected Discord poll tool for natural poll request, got %+v", requestToolNames(client.requests[0]))
	}
	if !strings.Contains(joinRequestMessages(client.requests[1]), "What will be better") {
		t.Fatalf("expected poll tool result in final chat request, got:\n%s", joinRequestMessages(client.requests[1]))
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
	if len(client.requests) != 2 {
		t.Fatalf("expected streamed reminder tool call and final response, got %d request(s)", len(client.requests))
	}
	if !requestToolNames(client.requests[0])["panda_manage_reminder"] {
		t.Fatalf("expected reminder tool for natural reminder request, got %+v", requestToolNames(client.requests[0]))
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
	if response.Content != "playing ocean drive" || response.Presentation.Title != "Now playing" || response.Presentation.Accent != AccentMusic {
		t.Fatalf("expected structured music card response, got %+v", response)
	}
	if response.Presentation.URL != "https://example.com/track" || len(response.Presentation.Fields) != 1 || len(response.Actions) != 1 {
		t.Fatalf("expected music card details, got %+v", response)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected streamed music tool call and final response, got %d request(s)", len(client.requests))
	}
	if names := requestToolNames(client.requests[0]); !names["panda_manage_music"] {
		t.Fatalf("expected music tool for natural music request, got %+v", names)
	}
	if !strings.Contains(joinRequestMessages(client.requests[1]), "playing ocean drive") {
		t.Fatalf("expected music tool result in final chat request, got:\n%s", joinRequestMessages(client.requests[1]))
	}
}

func TestNaturalMessageRendersPandaAboutCardWithLinkButtons(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-about",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_about",
				Arguments: `{}`,
			},
		}}},
	}}
	router := newTestRouter(t, client, 5)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options: map[string]string{
			"message":       "panda tell me about yourself",
			"bot_mentioned": "true",
		},
	})
	if response.Presentation.Title != "I'm Panda, a Discord-native assistant." || response.Presentation.Accent != AccentInfo {
		t.Fatalf("expected about card presentation, got %+v", response.Presentation)
	}
	if !strings.Contains(response.Content, "server knowledge") ||
		!strings.Contains(response.Content, "I'm open source") ||
		!strings.Contains(response.Content, "Created by "+pandainfo.CreatorHandle) ||
		strings.Contains(response.Content, pandainfo.RepositoryURL) ||
		strings.Contains(response.Content, pandainfo.CreatorURL) {
		t.Fatalf("unexpected about card content: %q", response.Content)
	}
	if len(response.Actions) != 2 ||
		response.Actions[0].Label != "Github" ||
		response.Actions[0].URL != pandainfo.RepositoryURL ||
		response.Actions[1].Label != "X" ||
		response.Actions[1].URL != pandainfo.CreatorURL {
		t.Fatalf("expected Github and X link actions, got %+v", response.Actions)
	}
	if len(response.Followups) != 0 {
		t.Fatalf("about card should not produce a second prose message, got %+v", response.Followups)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected only the model-selected about tool request, got %d", len(client.requests))
	}
	if names := requestToolNames(client.requests[0]); !names["panda_about"] {
		t.Fatalf("expected about tool to be exposed, got %+v", names)
	}
}

func TestNaturalMessageRendersYouTubeSearchSelectionCard(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-youtube-search",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_search_youtube",
				Arguments: `{"query":"GPT-5.6 Sol Is Here","limit":3}`,
			},
		}}},
		{Content: "<|assistant_output|>"},
	}}
	youtubeSearch := &fakeCommandYouTubeSummarizer{
		candidates: []youtube.VideoCandidate{
			{
				Title:        "GPT-5.6 Sol Is Here, BUT You Can't Use It!",
				URL:          "https://www.youtube.com/watch?v=one",
				Uploader:     "Universe of AI",
				ThumbnailURL: "https://i.ytimg.com/vi/one/hqdefault.jpg",
				Duration:     10*time.Minute + 3*time.Second,
			},
			{
				Title:        "GPT-5.6 Sol Rumors Explained",
				URL:          "https://www.youtube.com/watch?v=two",
				Uploader:     "Other Channel",
				ThumbnailURL: "https://i.ytimg.com/vi/two/hqdefault.jpg",
			},
		},
	}
	router := newTestRouter(t, client, 5, func(executor *tools.Executor) {
		executor.WithYouTubeSummarizer(youtubeSearch)
	})

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options: map[string]string{
			"message": "panda summarize GPT-5.6 Sol Is Here by universe of ai video show me video options to choose from",
		},
	})

	if response.Presentation.Title != "Choose a YouTube video" || response.Presentation.Accent != AccentInfo {
		t.Fatalf("expected YouTube selection card presentation, got %+v", response)
	}
	if response.Selection == nil || len(response.Selection.Options) != 2 {
		t.Fatalf("expected selectable YouTube choices, got %+v", response.Selection)
	}
	first := response.Selection.Options[0]
	if first.Label != "GPT-5.6 Sol Is Here, BUT You Can't Use It!" ||
		first.URL != "https://www.youtube.com/watch?v=one" ||
		first.ThumbnailURL == "" {
		t.Fatalf("unexpected first selection option: %+v", first)
	}
	if len(client.requests) != 1 {
		t.Fatalf("terminal YouTube selection should not request final model prose, got %d request(s)", len(client.requests))
	}
	if len(youtubeSearch.searches) != 1 || youtubeSearch.searches[0].Query != "GPT-5.6 Sol Is Here" {
		t.Fatalf("unexpected YouTube search request: %+v", youtubeSearch.searches)
	}
}

func TestNaturalMessageRendersLatestYouTubeSearchSelectionCard(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-youtube-search",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_search_youtube",
				Arguments: `{"query":"Fireship","limit":3,"source":"channel_uploads","sort_by":"upload_date"}`,
			},
		}}},
	}}
	youtubeSearch := &fakeCommandYouTubeSummarizer{
		candidates: []youtube.VideoCandidate{
			{
				Title:        "Newest Fireship Upload",
				URL:          "https://www.youtube.com/watch?v=newest",
				Uploader:     "Fireship",
				ThumbnailURL: "https://i.ytimg.com/vi/newest/hqdefault.jpg",
				UploadDate:   time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC),
			},
			{
				Title:      "Previous Fireship Upload",
				URL:        "https://www.youtube.com/watch?v=previous",
				Uploader:   "Fireship",
				UploadDate: time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			},
		},
	}
	router := newTestRouter(t, client, 5, func(executor *tools.Executor) {
		executor.WithYouTubeSummarizer(youtubeSearch)
	})

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options: map[string]string{
			"message": "panda summarize the latest video on the Fireship channel",
		},
	})

	if response.Presentation.Title != "Choose a YouTube video" || response.Selection == nil || len(response.Selection.Options) != 2 {
		t.Fatalf("expected latest YouTube selection card, got %+v", response)
	}
	if len(youtubeSearch.searches) != 1 ||
		youtubeSearch.searches[0].Query != "Fireship" ||
		youtubeSearch.searches[0].Source != "channel_uploads" ||
		youtubeSearch.searches[0].SortBy != "upload_date" {
		t.Fatalf("expected date-sorted YouTube search request, got %+v", youtubeSearch.searches)
	}
	if first := response.Selection.Options[0]; first.Label != "Newest Fireship Upload" || !strings.Contains(first.Description, "2026-06-20") || first.ThumbnailURL == "" {
		t.Fatalf("unexpected latest selection option: %+v", first)
	}
}

func TestNaturalMessageRetriesPlainYouTubeOptionsWithSearchTool(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: "<panda_respond>\nHere are the video options I found for **GPT-5.6 Sol Is Here**:\n\n- **Universe of AI - 9:07** - GPT-5.6 Sol Is Here, BUT You Can't Use It!\nWatch on YouTube\n\nPick the one you'd like summarized."},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-youtube-search",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_search_youtube",
				Arguments: `{"query":"GPT-5.6 Sol Is Here","limit":1}`,
			},
		}}},
	}}
	youtubeSearch := &fakeCommandYouTubeSummarizer{
		candidates: []youtube.VideoCandidate{
			{Title: "First video", URL: "https://www.youtube.com/watch?v=one", ThumbnailURL: "https://i.ytimg.com/vi/one/hqdefault.jpg"},
			{Title: "Second video", URL: "https://www.youtube.com/watch?v=two", ThumbnailURL: "https://i.ytimg.com/vi/two/hqdefault.jpg"},
			{Title: "Third video", URL: "https://www.youtube.com/watch?v=three", ThumbnailURL: "https://i.ytimg.com/vi/three/hqdefault.jpg"},
		},
	}
	router := newTestRouter(t, client, 5, func(executor *tools.Executor) {
		executor.WithYouTubeSummarizer(youtubeSearch)
	})

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options: map[string]string{
			"message": "panda summarize GPT-5.6 Sol Is Here by universe of ai video show me video options to choose from",
		},
	})

	if response.Presentation.Title != "Choose a YouTube video" || response.Selection == nil || len(response.Selection.Options) != 3 {
		t.Fatalf("expected retried YouTube selection card with three choices, got %+v", response)
	}
	if strings.Contains(response.Content, "Watch on YouTube") || strings.Contains(response.Content, "Here are the video options") {
		t.Fatalf("plain prose options should not survive retry, got %q", response.Content)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected initial prose attempt plus retry tool request, got %d request(s)", len(client.requests))
	}
	if len(youtubeSearch.searches) != 1 || youtubeSearch.searches[0].Limit != 3 {
		t.Fatalf("expected search retry to request three choices, got %+v", youtubeSearch.searches)
	}
}

func TestNaturalMessageYouTubeSearchFailureRendersWarningCard(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-youtube-search",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_search_youtube",
				Arguments: `{"query":"rare video","limit":3}`,
			},
		}}},
		{Content: "<|assistant_output|>"},
	}}
	router := newTestRouter(t, client, 5, func(executor *tools.Executor) {
		executor.WithYouTubeSummarizer(&fakeCommandYouTubeSummarizer{err: errors.New("yt-dlp search failed")})
	})

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options: map[string]string{
			"message": "panda summarize rare video show me options",
		},
	})

	if response.Presentation.Title != "YouTube search failed" || response.Presentation.Accent != AccentWarning {
		t.Fatalf("expected YouTube warning card, got %+v", response)
	}
	if !strings.Contains(response.Content, "yt-dlp search failed") || strings.Contains(response.Content, "<|assistant_output|>") {
		t.Fatalf("expected safe warning content without provider marker, got %q", response.Content)
	}
	if len(client.requests) != 1 {
		t.Fatalf("failed terminal YouTube card should not request final model prose, got %d request(s)", len(client.requests))
	}
}

func TestNaturalMessagePreservesRemainingAnswerAfterMusicCard(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-music-play",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_music",
				Arguments: `{"action":"play","query":"fill my pockets by mgk","voice_channel_id":"100000000000000222"}`,
			},
		}}},
		{Content: "SpaceX is privately held, so there is no public SpaceX stock ticker or live public stock price."},
	}}
	router := newTestRouter(t, client, 5, func(executor *tools.Executor) {
		executor.WithWebSearcher(fakeCommandWebSearch{response: websearch.Response{
			Provider: "brave_search",
			Query:    "SpaceX stock price",
			Results:  []websearch.Result{{Title: "SpaceX stock", URL: "https://example.com/spacex", Description: "SpaceX is privately held."}},
		}})
	})

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:         "user-1",
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		VoiceChannelID: "100000000000000222",
		Options: map[string]string{
			"message":       "panda join bot-test vc and play fill my pockets by mgk, also find latest spacex stock price",
			"bot_mentioned": "true",
		},
	})
	if response.Content != "playing fill my pockets by mgk" || response.Presentation.Title != "Now playing" || response.Presentation.Accent != AccentMusic {
		t.Fatalf("expected primary response to stay the music card, got %+v", response)
	}
	if len(response.Followups) != 1 {
		t.Fatalf("expected remaining answer to render as a followup, got %+v", response.Followups)
	}
	if followup := response.Followups[0]; !strings.Contains(followup.Content, "SpaceX is privately held") || strings.Contains(followup.Content, "playing fill my pockets") || followup.Presentation.Title != "" {
		t.Fatalf("expected plain remaining-answer followup, got %+v", followup)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected music tool and final response requests, got %d", len(client.requests))
	}
	finalRequest := joinRequestMessages(client.requests[1])
	if !strings.Contains(finalRequest, "if any independent part remains unresolved") || !strings.Contains(finalRequest, "web/search/current-information") {
		t.Fatalf("expected final request to remind model about remaining tool work, got:\n%s", finalRequest)
	}
}

func TestNaturalMessagePreservesShortRemainingAnswerAfterMusicCard(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-music-stop",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_music",
				Arguments: `{"action":"stop"}`,
			},
		}}},
		{Content: "No composed tools are available in this server right now."},
	}}
	router := newTestRouter(t, client, 5)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:         "user-1",
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		VoiceChannelID: "voice-1",
		Options: map[string]string{
			"message":       "panda stop playing song and tell me which composed tools you have",
			"bot_mentioned": "true",
		},
	})
	if response.Content != "music handled" || response.Presentation.Title != "Now playing" || response.Presentation.Accent != AccentMusic {
		t.Fatalf("expected primary response to stay the music card, got %+v", response)
	}
	if len(response.Followups) != 1 {
		t.Fatalf("expected short remaining answer to render as a followup, got %+v", response.Followups)
	}
	if followup := response.Followups[0]; !strings.Contains(followup.Content, "No composed tools are available") || followup.Presentation.Title != "" {
		t.Fatalf("expected plain composed-tools followup, got %+v", followup)
	}
}

func TestNaturalMessageSplitsMusicCardFromWebSearchAnswer(t *testing.T) {
	sourceURL := "https://example.com/spacex-stock"
	musicManager := &fakeToolMusicManager{}
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-music-play",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_music",
				Arguments: `{"action":"play","query":"fill my pockets by mgk"}`,
			},
		}}},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-web-stock",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "web_search",
				Arguments: `{"query":"SpaceX stock price","limit":1}`,
			},
		}}},
		{Content: "<panda_respond>\nSpaceX is privately held, so there is no public SpaceX stock ticker [web_search\u20205] ."},
	}}
	router := newTestRouter(t, client, 5, func(executor *tools.Executor) {
		executor.WithMusicManager(musicManager)
		executor.WithWebSearcher(fakeCommandWebSearch{response: websearch.Response{
			Provider: "brave_search",
			Query:    "SpaceX stock price",
			Results: []websearch.Result{{
				Title:       "SpaceX Stock",
				URL:         sourceURL,
				Description: "SpaceX is privately held.",
				Source:      "Example",
			}},
		}})
	})

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:         "user-1",
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		VoiceChannelID: "voice-1",
		Options: map[string]string{
			"message":       "panda play fill my pockets by mgk, also look up the price of spacex stock",
			"bot_mentioned": "true",
		},
	})
	if response.Content != "playing fill my pockets by mgk" || response.Presentation.Title != "Now playing" || response.Presentation.Accent != AccentMusic {
		t.Fatalf("expected primary response to be the music card only, got %+v", response)
	}
	if strings.Contains(response.Content, "SpaceX") {
		t.Fatalf("web answer should not be merged into the music card content: %+v", response)
	}
	if len(response.Followups) != 1 {
		t.Fatalf("expected one plain followup answer, got %+v", response.Followups)
	}
	followup := response.Followups[0]
	if followup.Presentation.Title != "" || followup.Presentation.Accent != AccentDefault {
		t.Fatalf("expected followup to render as plain text, got %+v", followup)
	}
	if !strings.Contains(followup.Content, "SpaceX is privately held") || !strings.Contains(followup.Content, sourceURL) {
		t.Fatalf("expected web-search answer with source in followup, got %q", followup.Content)
	}
	if strings.Contains(followup.Content, "<panda_respond>") || strings.Contains(followup.Content, "web_search\u2020") || strings.Contains(followup.Content, " .") {
		t.Fatalf("followup leaked model formatting artifacts: %q", followup.Content)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected music tool, web search tool, and final response requests, got %d", len(client.requests))
	}
	finalRequest := joinRequestMessages(client.requests[2])
	if !strings.Contains(finalRequest, "may be rendered as a Discord card") || !strings.Contains(finalRequest, "Do not repeat") {
		t.Fatalf("expected final request to tell the model not to repeat card status, got:\n%s", finalRequest)
	}
}

func TestNaturalMessageSelfReplyWakeRunsRepliedToCombinedRequest(t *testing.T) {
	sourceURL := "https://example.com/spacex-stock"
	musicManager := &fakeToolMusicManager{}
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-music-play",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_music",
				Arguments: `{"action":"play","query":"fill my pockets by mgk"}`,
			},
		}}},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-web-stock",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "web_search",
				Arguments: `{"query":"SpaceX stock price","limit":1}`,
			},
		}}},
		{Content: "SpaceX is privately held, so there is no public SpaceX stock ticker."},
	}}
	router := newTestRouter(t, client, 5, func(executor *tools.Executor) {
		executor.WithMusicManager(musicManager)
		executor.WithWebSearcher(fakeCommandWebSearch{response: websearch.Response{
			Provider: "brave_search",
			Query:    "SpaceX stock price",
			Results: []websearch.Result{{
				Title:       "SpaceX Stock",
				URL:         sourceURL,
				Description: "SpaceX is privately held.",
				Source:      "Example",
			}},
		}})
	})

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:         "user-1",
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		VoiceChannelID: "voice-1",
		Options: map[string]string{
			"message":                      "panda",
			"reply_text":                   "join bot-test vc and play fill my pockets by mgk, also tell me spacex stock price",
			"reply_message_id":             "message-replied-to",
			"reply_author_is_current_user": "true",
		},
	})
	if response.Content != "playing fill my pockets by mgk" || response.Presentation.Title != "Now playing" || response.Presentation.Accent != AccentMusic {
		t.Fatalf("expected primary response to be the music card, got %+v", response)
	}
	if len(response.Followups) != 1 || !strings.Contains(response.Followups[0].Content, "SpaceX is privately held") || !strings.Contains(response.Followups[0].Content, sourceURL) {
		t.Fatalf("expected stock answer followup with source, got %+v", response.Followups)
	}
	if len(musicManager.requests) != 1 || musicManager.requests[0].Action != "play" || musicManager.requests[0].Query != "fill my pockets by mgk" {
		t.Fatalf("expected replied-to music request to run, got %+v", musicManager.requests)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected music tool, web tool, and final answer requests, got %d", len(client.requests))
	}
	firstLastMessage := client.requests[0].Messages[len(client.requests[0].Messages)-1]
	if firstLastMessage.Role != "user" {
		t.Fatalf("expected first request to end with resolved user prompt, got %+v", firstLastMessage)
	}
	if strings.TrimSpace(firstLastMessage.Content) == "panda" {
		t.Fatalf("self-reply wake should not send only the wake word as the active prompt")
	}
	firstRequest := joinRequestMessages(client.requests[0])
	for _, want := range []string{
		"Replied-to author is current user: true",
		"handle the replied-to message as the actual request",
		"Current Discord message content:\npanda",
		"reply to the current user's own prior Discord message",
		"Resolve the active user request from both messages",
		"join bot-test vc and play fill my pockets by mgk",
		"spacex stock price",
	} {
		if !strings.Contains(firstRequest, want) {
			t.Fatalf("expected first request to include %q, got:\n%s", want, firstRequest)
		}
	}
}

func TestNaturalMessageOtherUserReplyWakeRunsRepliedToCombinedRequest(t *testing.T) {
	sourceURL := "https://example.com/spacex-stock"
	musicManager := &fakeToolMusicManager{}
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-music-play",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_music",
				Arguments: `{"action":"play","query":"fill my pockets by mgk"}`,
			},
		}}},
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-web-stock",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "web_search",
				Arguments: `{"query":"SpaceX stock price","limit":1}`,
			},
		}}},
		{Content: "SpaceX is privately held, so there is no public SpaceX stock ticker."},
	}}
	router := newTestRouter(t, client, 5, func(executor *tools.Executor) {
		executor.WithMusicManager(musicManager)
		executor.WithWebSearcher(fakeCommandWebSearch{response: websearch.Response{
			Provider: "brave_search",
			Query:    "SpaceX stock price",
			Results: []websearch.Result{{
				Title:       "SpaceX Stock",
				URL:         sourceURL,
				Description: "SpaceX is privately held.",
				Source:      "Example",
			}},
		}})
	})

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:         "user-2",
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		VoiceChannelID: "voice-1",
		Options: map[string]string{
			"message":          "panda",
			"reply_text":       "join bot-test vc and play fill my pockets by mgk, also tell me spacex stock price",
			"reply_message_id": "message-replied-to",
		},
	})
	if response.Content != "playing fill my pockets by mgk" || response.Presentation.Title != "Now playing" || response.Presentation.Accent != AccentMusic {
		t.Fatalf("expected primary response to be the music card, got %+v", response)
	}
	if len(response.Followups) != 1 || !strings.Contains(response.Followups[0].Content, "SpaceX is privately held") || !strings.Contains(response.Followups[0].Content, sourceURL) {
		t.Fatalf("expected stock answer followup with source, got %+v", response.Followups)
	}
	if len(musicManager.requests) != 1 || musicManager.requests[0].Action != "play" || musicManager.requests[0].Query != "fill my pockets by mgk" {
		t.Fatalf("expected replied-to music request to run, got %+v", musicManager.requests)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected music tool, web tool, and final answer requests, got %d", len(client.requests))
	}
	firstLastMessage := client.requests[0].Messages[len(client.requests[0].Messages)-1]
	if firstLastMessage.Role != "user" {
		t.Fatalf("expected first request to end with resolved user prompt, got %+v", firstLastMessage)
	}
	if strings.TrimSpace(firstLastMessage.Content) == "panda" {
		t.Fatalf("reply wake should not send only the wake word as the active prompt")
	}
	firstRequest := joinRequestMessages(client.requests[0])
	for _, want := range []string{
		"Replied-to author is current user: false",
		"handle the replied-to non-Panda message as the actual request",
		"Do not answer with a generic capability overview",
		"Current Discord message content:\npanda",
		"reply to another user's prior Discord message",
		"Resolve the active user request from both messages",
		"join bot-test vc and play fill my pockets by mgk",
		"spacex stock price",
	} {
		if !strings.Contains(firstRequest, want) {
			t.Fatalf("expected first request to include %q, got:\n%s", want, firstRequest)
		}
	}
}

func TestNaturalMessageMusicCanTargetVoiceChannelFromText(t *testing.T) {
	musicManager := &fakeToolMusicManager{}
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-music-play",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_music",
				Arguments: `{"action":"play","query":"ocean drive","voice_channel_id":"100000000000000222"}`,
			},
		}}},
		{Content: "Queued ocean drive."},
	}}
	router := newTestRouter(t, client, 5, func(executor *tools.Executor) {
		executor.WithMusicManager(musicManager)
	})

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options: map[string]string{
			"message":       "panda play ocean drive in <#100000000000000222>",
			"bot_mentioned": "true",
		},
	})
	if response.Content != "playing ocean drive" || response.Presentation.Title != "Now playing" || response.Presentation.Accent != AccentMusic {
		t.Fatalf("expected structured music card response, got %+v", response)
	}
	if len(musicManager.requests) != 1 {
		t.Fatalf("expected one music request, got %+v", musicManager.requests)
	}
	if musicManager.requests[0].VoiceChannelID != "100000000000000222" {
		t.Fatalf("expected targeted voice channel without caller voice state, got %+v", musicManager.requests[0])
	}
}

func TestNaturalMessageRunsMultipleMusicToolsInOneTurn(t *testing.T) {
	musicManager := &fakeToolMusicManager{}
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{
			{
				ID:   "call-skip-current",
				Type: "function",
				Function: llm.ToolCallFunction{
					Name:      "panda_manage_music",
					Arguments: `{"action":"skip"}`,
				},
			},
			{
				ID:   "call-play-next",
				Type: "function",
				Function: llm.ToolCallFunction{
					Name:      "panda_manage_music",
					Arguments: `{"action":"play","query":"bmxxing by mgk"}`,
				},
			},
		}},
		{Content: "Skipped the current song and started bmxxing."},
	}}
	router := newTestRouter(t, client, 5, func(executor *tools.Executor) {
		executor.WithMusicManager(musicManager)
	})

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:         "user-1",
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		VoiceChannelID: "voice-1",
		Options: map[string]string{
			"message":       "panda skip this song and play bmxxing by mgk",
			"bot_mentioned": "true",
		},
	})
	if response.Content != "playing bmxxing by mgk" || response.Presentation.Title != "Now playing" || response.Presentation.Accent != AccentMusic {
		t.Fatalf("expected final play card after skip and play, got %+v", response)
	}
	if len(musicManager.requests) != 2 {
		t.Fatalf("expected skip and play music requests, got %+v", musicManager.requests)
	}
	if musicManager.requests[0].Action != "skip" || musicManager.requests[1].Action != "play" || musicManager.requests[1].Query != "bmxxing by mgk" {
		t.Fatalf("music requests were not executed in order: %+v", musicManager.requests)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected streamed tool batch and final response, got %d request(s)", len(client.requests))
	}
	finalMessages := joinRequestMessages(client.requests[1])
	for _, want := range []string{"Track skipped", "Now playing", "bmxxing by mgk"} {
		if !strings.Contains(finalMessages, want) {
			t.Fatalf("expected final chat request to include %s from both music tools, got:\n%s", want, finalMessages)
		}
	}
}

func TestNaturalMessageUsesAtomicSkipPlayMusicTool(t *testing.T) {
	musicManager := &fakeToolMusicManager{}
	client := &fakeLLM{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:   "call-skip-play",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_music",
				Arguments: `{"action":"skip_play","query":"bmxxing by mgk"}`,
			},
		}}},
		{Content: "Skipped the current song and started bmxxing."},
	}}
	router := newTestRouter(t, client, 5, func(executor *tools.Executor) {
		executor.WithMusicManager(musicManager)
	})

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:         "user-1",
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		VoiceChannelID: "voice-1",
		Options: map[string]string{
			"message":       "panda skip this song and play bmxxing by mgk",
			"bot_mentioned": "true",
		},
	})
	if response.Content != "playing bmxxing by mgk" || response.Presentation.Title != "Track replaced" || response.Presentation.Accent != AccentMusic {
		t.Fatalf("expected atomic skip_play music card, got %+v", response)
	}
	if len(musicManager.requests) != 1 {
		t.Fatalf("expected one atomic music request, got %+v", musicManager.requests)
	}
	if musicManager.requests[0].Action != "skip_play" || musicManager.requests[0].Query != "bmxxing by mgk" {
		t.Fatalf("unexpected atomic music request: %+v", musicManager.requests)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected tool request and final response request, got %d", len(client.requests))
	}
}

func TestNaturalMessageSoulWriterCanBrainstormWithoutAssistantUse(t *testing.T) {
	ctx := context.Background()
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: "<panda_respond>\nLet's shape a few options."},
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
	if len(client.requests) != 1 {
		t.Fatalf("expected one streamed natural chat request, got %d", len(client.requests))
	}
	if !requestToolNames(client.requests[0])["panda_manage_soul"] {
		t.Fatalf("expected soul management tool for delegated soul writer, got %+v", client.requests[0].Tools)
	}
}

func TestNaturalMessagePassesReplyContextToChat(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: "<panda_respond>\nchat fixture"},
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
	if len(client.requests) != 1 {
		t.Fatalf("expected one streamed natural chat request, got %d", len(client.requests))
	}
	chatMessages := joinRequestMessages(client.requests[0])
	for _, want := range []string{"message-current", "message-replied-to", "Reading / Info", "Writing / Actions"} {
		if !strings.Contains(chatMessages, want) {
			t.Fatalf("expected chat request to preserve reply context %q, got:\n%s", want, chatMessages)
		}
	}
}

func TestNaturalMessageAdminGetsManagementToolsWhenPolicyOff(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: "<panda_respond>\nchat fixture"},
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
	if len(client.requests) != 1 {
		t.Fatalf("expected one streamed natural chat request, got %d", len(client.requests))
	}
	names := requestToolNames(client.requests[0])
	for _, want := range []string{"read_config", "panda_manage_soul", "panda_manage_prompt", "panda_manage_tool_access", "panda_manage_composed_tool", "panda_manage_channel_rule", "generate_workflow_json"} {
		if !names[want] {
			t.Fatalf("expected %s in admin natural-message tools, got %+v", want, names)
		}
	}
	if names["panda_list_tools"] {
		t.Fatalf("list-tools meta tool should not be exposed to response model, got %+v", names)
	}
	if names["discord_send_message"] {
		t.Fatalf("discord_send_message should need Discord provider runtime wiring, got %+v", names)
	}
}

func TestNaturalMessageStoredGuildOwnerGetsManagementToolsWhenPolicyOff(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: "<panda_respond>\nchat fixture"},
	}}
	router, deps := newTestRouterWithDeps(t, client, 5)
	ctx := context.Background()
	if _, err := deps.guilds.RecordAuthorizedInstall(ctx, repository.GuildInstall{
		GuildID:           "guild-1",
		Name:              "Test Guild",
		OwnerUserID:       "owner-1",
		InstalledByUserID: "installer-1",
		AuthorizedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordAuthorizedInstall: %v", err)
	}

	response := router.HandleNaturalMessage(ctx, Request{
		UserID:    "owner-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"message": "Panda what can you do?", "bot_mentioned": "true"},
	})
	if response.Content != "chat fixture" {
		t.Fatalf("unexpected natural message response: %+v", response)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one streamed natural chat request, got %d", len(client.requests))
	}
	names := requestToolNames(client.requests[0])
	for _, want := range []string{"read_config", "panda_manage_soul", "panda_manage_prompt", "panda_manage_tool_access", "panda_manage_composed_tool", "panda_manage_channel_rule", "generate_workflow_json"} {
		if !names[want] {
			t.Fatalf("expected stored guild owner to receive %s in natural-message tools, got %+v", want, names)
		}
	}
	if names["panda_list_tools"] {
		t.Fatalf("list-tools meta tool should not be exposed to response model, got %+v", names)
	}
	capabilityContext := joinRequestMessages(client.requests[0])
	if !strings.Contains(capabilityContext, "Admin setup (caller has admin access)") {
		t.Fatalf("expected owner/admin capability context, got:\n%s", capabilityContext)
	}
	if strings.Contains(capabilityContext, "Show the tool inventory") {
		t.Fatalf("capability context should not teach the model to expose tool inventory, got:\n%s", capabilityContext)
	}
}

func TestNaturalMessageStoredGuildOwnerGetsFeatureGatedManagementTools(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: "<panda_respond>\nchat fixture"},
	}}
	router, deps := newTestRouterWithDeps(t, client, 5)
	featureRepo := attachFeatureService(t, router)
	ctx := context.Background()
	if _, err := deps.guilds.RecordAuthorizedInstall(ctx, repository.GuildInstall{
		GuildID:           "guild-1",
		Name:              "Test Guild",
		OwnerUserID:       "owner-1",
		InstalledByUserID: "installer-1",
		AuthorizedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordAuthorizedInstall: %v", err)
	}
	if err := featureRepo.SetGuildFeatures(ctx, "guild-1", features.DefaultInstallPreset(), "intent-1", "owner-1", time.Now().UTC()); err != nil {
		t.Fatalf("SetGuildFeatures: %v", err)
	}

	response := router.HandleNaturalMessage(ctx, Request{
		UserID:    "owner-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"message": "Panda what can you do?", "bot_mentioned": "true"},
	})
	if response.Content != "chat fixture" {
		t.Fatalf("unexpected natural message response: %+v", response)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one streamed natural chat request, got %d", len(client.requests))
	}
	names := requestToolNames(client.requests[0])
	for _, want := range []string{"read_config", "panda_manage_soul", "panda_manage_prompt", "panda_manage_tool_access", "panda_manage_composed_tool", "panda_manage_channel_rule", "generate_workflow_json", "panda_manage_music", "panda_manage_reminder"} {
		if !names[want] {
			t.Fatalf("expected feature-gated stored guild owner to receive %s, got %+v", want, names)
		}
	}
	if names["panda_list_tools"] {
		t.Fatalf("list-tools meta tool should not be exposed to response model, got %+v", names)
	}
	capabilityContext := joinRequestMessages(client.requests[0])
	for _, forbidden := range []string{"Show the tool inventory", "I can help with three things", "`panda_list_tools`"} {
		if strings.Contains(capabilityContext, forbidden) {
			t.Fatalf("capability context should not include %q, got:\n%s", forbidden, capabilityContext)
		}
	}
	if !strings.Contains(capabilityContext, "Admin setup (caller has admin access)") {
		t.Fatalf("expected owner/admin capability context, got:\n%s", capabilityContext)
	}
}

func TestNaturalMessageHandlesTrailingPandaMention(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: "<panda_respond>\nchat fixture"},
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
	if len(client.requests) != 1 {
		t.Fatalf("expected one streamed natural chat request, got %d", len(client.requests))
	}
	if !strings.Contains(joinRequestMessages(client.requests[0]), "what can you do panda") {
		t.Fatalf("expected exact message in natural chat request, got:\n%s", joinRequestMessages(client.requests[0]))
	}
}

func TestNaturalCapabilityMessageUsesToolInventoryWorkflow(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: "<panda_respond>\nQuick overview: I can help with reminders, music, and workflow setup here."},
	}}
	router := newTestRouter(t, client, 5)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"message": "Panda what can you do?", "bot_mentioned": "true"},
	})
	if response.Content != "Quick overview: I can help with reminders, music, and workflow setup here." {
		t.Fatalf("unexpected natural message response: %+v", response)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one streamed direct capability response, got %d", len(client.requests))
	}
	if names := requestToolNames(client.requests[0]); names["panda_list_tools"] {
		t.Fatalf("list-tools meta tool should not be exposed to response model, got %+v", names)
	}
	capabilityContext := joinRequestMessages(client.requests[0])
	for _, want := range []string{"current user-scoped capability overview", "Do not present internal listing/debug helpers", "Mention exact function/tool names only when the user explicitly asks"} {
		if !strings.Contains(capabilityContext, want) {
			t.Fatalf("expected capability context %s in response request, got:\n%s", want, capabilityContext)
		}
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
	if names := requestToolNames(client.requests[0]); !names["panda_manage_ops"] {
		t.Fatalf("expected owner ops tool for natural owner ops, got %+v", names)
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

func TestRegularUserGetsWebSearchPermissionWhenFeatureGateOmitsWebSearch(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 5)
	repo := attachFeatureService(t, router)
	if err := repo.SetGuildFeatures(ctx, "guild-1", []string{features.AssistantChat}, "test", "admin", time.Now().UTC()); err != nil {
		t.Fatalf("SetGuildFeatures: %v", err)
	}

	permissions := router.allowedToolPermissions(ctx, Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		RoleIDs:   []string{"member"},
	})
	if _, ok := permissions[admin.PermissionAssistantWebSearch]; !ok {
		t.Fatalf("web search should be available by default even when guild features omit it: %+v", permissions)
	}

	enabled, active := router.featureSetForAccess(ctx, "guild-1")
	if !active || !features.Has(enabled, features.WebSearch) {
		t.Fatalf("web search should be injected into access feature set, active=%t enabled=%+v", active, enabled)
	}
}

func TestRegularUserGetsYouTubeClippingPermissionByDefaultUntilAdminDisables(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 5)
	repo := attachFeatureService(t, router)
	if err := repo.SetGuildFeatures(ctx, "guild-1", []string{features.AssistantChat}, "test", "admin", time.Now().UTC()); err != nil {
		t.Fatalf("SetGuildFeatures: %v", err)
	}

	permissions := router.allowedToolPermissions(ctx, Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		RoleIDs:   []string{"member"},
	})
	if _, ok := permissions[admin.PermissionAssistantYouTubeClipping]; !ok {
		t.Fatalf("youtube clipping should be available by default even when guild features omit it: %+v", permissions)
	}
	enabled, active := router.featureSetForAccess(ctx, "guild-1")
	if !active || !features.Has(enabled, features.YouTubeClipping) {
		t.Fatalf("youtube clipping should be injected into access feature set, active=%t enabled=%+v", active, enabled)
	}

	response := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "feature",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "disable", "feature_id": features.YouTubeClipping},
	})
	if !strings.Contains(response.Content, "Disabled") {
		t.Fatalf("expected admin disable to succeed, got %+v", response)
	}
	permissions = router.allowedToolPermissions(ctx, Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		RoleIDs:   []string{"member"},
	})
	if _, ok := permissions[admin.PermissionAssistantYouTubeClipping]; ok {
		t.Fatalf("youtube clipping should not be available after explicit disable: %+v", permissions)
	}
	enabled, active = router.featureSetForAccess(ctx, "guild-1")
	if !active || features.Has(enabled, features.YouTubeClipping) {
		t.Fatalf("youtube clipping should be absent after explicit disable, active=%t enabled=%+v", active, enabled)
	}
}

func TestRegularUserGetsImageGenerationPermissionWhenFeatureEnabled(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 5)
	repo := attachFeatureService(t, router)
	if err := repo.SetGuildFeatures(ctx, "guild-1", []string{features.AssistantChat, features.ImageGeneration}, "test", "admin", time.Now().UTC()); err != nil {
		t.Fatalf("SetGuildFeatures: %v", err)
	}

	permissions := router.allowedToolPermissions(ctx, Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		RoleIDs:   []string{"member"},
	})
	if _, ok := permissions[admin.PermissionAssistantImageGeneration]; !ok {
		t.Fatalf("regular users should get image generation when the guild feature is enabled: %+v", permissions)
	}
}

func TestNaturalMessageDoesNotRespondWhenGateDeclines(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "<panda_ignore>"}}
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
		t.Fatalf("expected only streamed natural chat request, got %d", len(client.requests))
	}
}

func TestNaturalMessageDoesNotReachLLMWhenUserDeniedChatAccess(t *testing.T) {
	ctx := context.Background()
	client := &fakeLLM{response: llm.ChatResponse{Content: "<panda_respond>\nYo, I'm here."}}
	router := newTestRouter(t, client, 5)
	if _, err := router.admin.DenyToolUser(ctx, "guild-1", "admin", tools.ToolNamePandaChat, "user-xer0"); err != nil {
		t.Fatalf("DenyToolUser: %v", err)
	}

	response := router.HandleNaturalMessage(ctx, Request{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-xer0",
		Options:   map[string]string{"message": "yo panda", "bot_mentioned": "true"},
	})
	if response.Content != "" || response.Confirmation != nil || len(response.Followups) != 0 || response.Poll != nil {
		t.Fatalf("expected denied natural chat to stay silent, got %+v", response)
	}
	if len(client.requests) != 0 {
		t.Fatalf("denied natural chat should not call the LLM, got %d request(s)", len(client.requests))
	}
}

func TestQuietModeShortCircuitsUserFacingResponses(t *testing.T) {
	ctx := context.Background()
	client := &fakeLLM{response: llm.ChatResponse{Content: "model should not answer"}}
	router := newTestRouter(t, client, 5)
	now := time.Now().UTC()
	if _, err := router.admin.SetQuietModeUntil(ctx, "guild-1", "admin-1", now.Add(30*time.Minute), now); err != nil {
		t.Fatalf("SetQuietModeUntil: %v", err)
	}

	command := router.Handle(ctx, Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"question": "hi panda"},
	})
	if command.Content != "" || command.Confirmation != nil || len(command.Followups) != 0 || command.Poll != nil {
		t.Fatalf("expected quiet command to stay silent, got %+v", command)
	}

	natural := router.HandleNaturalMessage(ctx, Request{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"message": "Panda can you help?", "bot_mentioned": "true"},
	})
	if natural.Content != "" || natural.Confirmation != nil || len(natural.Followups) != 0 || natural.Poll != nil {
		t.Fatalf("expected quiet natural message to stay silent, got %+v", natural)
	}

	background := router.HandleBackgroundTask(ctx, BackgroundTask{
		RequestID: "request-1",
		Command:   "summarize",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Input:     "long text",
	})
	if background.Content != "" || background.Confirmation != nil || len(background.Followups) != 0 || background.Poll != nil {
		t.Fatalf("expected quiet background task to stay silent, got %+v", background)
	}
	if len(client.requests) != 0 {
		t.Fatalf("quiet mode should not call LLM, got %d requests", len(client.requests))
	}
}

func TestQuietModeDoesNotAffectAdminPrivilegedUsers(t *testing.T) {
	ctx := context.Background()
	client := &fakeLLM{response: llm.ChatResponse{Content: "<panda_respond>\nAdmin answer."}}
	router := newTestRouter(t, client, 5)
	now := time.Now().UTC()
	if _, err := router.admin.SetQuietModeUntil(ctx, "guild-1", "admin-1", now.Add(30*time.Minute), now); err != nil {
		t.Fatalf("SetQuietModeUntil: %v", err)
	}

	discordAdmin := router.HandleNaturalMessage(ctx, Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "discord-admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"message": "Panda can you help?", "bot_mentioned": "true"},
	})
	if !strings.Contains(discordAdmin.Content, "Admin answer") {
		t.Fatalf("expected Discord admin to bypass quiet mode, got %+v", discordAdmin)
	}

	if _, err := router.admin.ApplyUserProfile(ctx, "guild-1", "admin-1", "panda-admin", admin.RoleProfileAdmin); err != nil {
		t.Fatalf("ApplyUserProfile: %v", err)
	}
	pandaAdmin := router.HandleNaturalMessage(ctx, Request{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "panda-admin",
		Options:   map[string]string{"message": "Panda still around?", "bot_mentioned": "true"},
	})
	if !strings.Contains(pandaAdmin.Content, "Admin answer") {
		t.Fatalf("expected delegated Panda admin to bypass quiet mode, got %+v", pandaAdmin)
	}

	background := router.HandleBackgroundTask(ctx, BackgroundTask{
		RequestID:    "request-admin-bg",
		Command:      "summarize",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "discord-admin",
		IsGuildAdmin: true,
		Input:        "long text",
	})
	if !strings.Contains(background.Content, "Admin answer") {
		t.Fatalf("expected admin background task to bypass quiet mode, got %+v", background)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected admin requests to reach LLM, got %d", len(client.requests))
	}
}

func TestQuietModeAllowsAdminQuietClearEscapeHatch(t *testing.T) {
	ctx := context.Background()
	client := &fakeLLM{response: llm.ChatResponse{Content: "<panda_respond>\nBack online."}}
	router := newTestRouter(t, client, 5)
	now := time.Now().UTC()
	if _, err := router.admin.SetQuietModeUntil(ctx, "guild-1", "admin-1", now.Add(30*time.Minute), now); err != nil {
		t.Fatalf("SetQuietModeUntil: %v", err)
	}

	cleared := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "quiet",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin-1",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "clear"},
	})
	if !cleared.Ephemeral || !strings.Contains(cleared.Content, "cleared") {
		t.Fatalf("expected admin quiet clear response, got %+v", cleared)
	}

	response := router.HandleNaturalMessage(ctx, Request{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"message": "Panda are you back?", "bot_mentioned": "true"},
	})
	if response.Content == "" {
		t.Fatalf("expected natural message after clear to reach assistant, got %+v", response)
	}
}

func TestExpiredQuietModeDoesNotShortCircuit(t *testing.T) {
	ctx := context.Background()
	client := &fakeLLM{response: llm.ChatResponse{Content: "<panda_respond>\nI'm awake."}}
	router, deps := newTestRouterWithDeps(t, client, 5)
	if _, err := deps.configs.EnsureDefault(ctx, "guild-1"); err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if _, err := deps.configs.SetAssistantTimeoutUntil(ctx, "guild-1", time.Now().UTC().Add(-time.Minute), "admin-1"); err != nil {
		t.Fatalf("SetAssistantTimeoutUntil: %v", err)
	}

	response := router.HandleNaturalMessage(ctx, Request{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"message": "Panda are you awake?", "bot_mentioned": "true"},
	})
	if response.Content == "" {
		t.Fatalf("expected expired quiet mode to allow response, got %+v", response)
	}
	config, ok, err := deps.configs.Get(ctx, "guild-1")
	if err != nil || !ok {
		t.Fatalf("Get config: ok=%t err=%v", ok, err)
	}
	if config.AssistantTimeoutUntil != nil {
		t.Fatalf("expected expired quiet timeout to be cleared, got %+v", config.AssistantTimeoutUntil)
	}
}

func TestMaintenanceModeShortCircuitsUserFacingResponses(t *testing.T) {
	ctx := context.Background()
	client := &fakeLLM{response: llm.ChatResponse{Content: "model should not answer"}}
	router := newTestRouter(t, client, 5)
	service := attachRuntimeStatus(t, router)
	if _, err := service.SetStatus(ctx, runtimecontrol.SetStatusRequest{
		Disabled: true,
		Message:  "Panda is sleeping for maintenance.",
		Actor:    "test",
	}); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	command := router.Handle(ctx, Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"question": "hi"},
	})
	if command.Content != "Panda is sleeping for maintenance." {
		t.Fatalf("expected maintenance command response, got %+v", command)
	}

	natural := router.HandleNaturalMessage(ctx, Request{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"message": "Panda can you help?", "bot_mentioned": "true"},
	})
	if natural.Content != "Panda is sleeping for maintenance." {
		t.Fatalf("expected maintenance natural response, got %+v", natural)
	}

	background := router.HandleBackgroundTask(ctx, BackgroundTask{
		RequestID: "request-1",
		Command:   "summarize",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Input:     "long text",
	})
	if background.Content != "Panda is sleeping for maintenance." {
		t.Fatalf("expected maintenance background response, got %+v", background)
	}
	if len(client.requests) != 0 {
		t.Fatalf("maintenance mode should not call LLM, got %d requests", len(client.requests))
	}
}

func TestMaintenanceModeLeavesOwnerOpsAvailable(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{}, 5)
	service := attachRuntimeStatus(t, router)
	if _, err := service.SetStatus(ctx, runtimecontrol.SetStatusRequest{Disabled: true, Actor: "test"}); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	response := router.Handle(ctx, Request{Command: "ops", Subcommand: "health", UserID: "owner", IsOwner: true})
	if !response.Ephemeral || !strings.Contains(response.Content, "sqlite=ok") {
		t.Fatalf("expected owner ops to stay available, got %+v", response)
	}
}

func TestNaturalMessageAboutPandaDeclineDoesNotStartResponse(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "<panda_ignore>"}}
	router := newTestRouter(t, client, 5)
	respondStarted := 0

	response := router.HandleNaturalMessageStream(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options: map[string]string{
			"message":       "how are you guys feeling about the new panda bot",
			"bot_mentioned": "true",
		},
	}, func() {
		respondStarted++
	})
	if response.Content != "" {
		t.Fatalf("expected no response, got %+v", response)
	}
	if respondStarted != 0 {
		t.Fatalf("declined about-Panda message should not start response indicator, got %d", respondStarted)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected only streamed natural chat request, got %d", len(client.requests))
	}
	joined := joinRequestMessages(client.requests[0])
	for _, want := range []string{
		"talking about Panda instead of to Panda",
		"The grammatical addressee must be Panda/the bot/the assistant",
		"how are you guys feeling about the new panda bot",
		"Panda is the topic, not the addressee",
		"Bot mention is a wake signal",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected natural gate to include %q, got:\n%s", want, joined)
		}
	}
}

func TestNaturalMessageRendersRetryableAssistantErrorOnce(t *testing.T) {
	retryErr := llm.Error{StatusCode: http.StatusTooManyRequests, Code: "rate_limit", Message: "slow down"}
	request := Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options: map[string]string{
			"message":       "panda tell me about yourself",
			"bot_mentioned": "true",
		},
	}
	router := newTestRouter(t, &fakeLLM{err: retryErr}, 5)

	response := router.HandleNaturalMessage(context.Background(), request)
	if response.Presentation.Title != "AI response failed" || !strings.Contains(response.Content, "try again later") {
		t.Fatalf("natural handler should render one visible error card, got %+v", response)
	}
}

func TestNaturalMessageStaysSilentWhenGateOutputIsMalformedForDirectAddress(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{Content: `**Decision** yes, respond`},
	}}
	router := newTestRouter(t, client, 5)

	response := router.HandleNaturalMessage(context.Background(), Request{
		UserID:    "user-1",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		Options:   map[string]string{"message": "hey panda what can you do?"},
	})
	if response.Content != "" {
		t.Fatalf("expected no response after malformed gate output, got %+v", response)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected only streamed natural chat request, got %d", len(client.requests))
	}
}

func TestNaturalMessageStaysSilentWhenGateOutputIsMalformedForAmbientMessage(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: `**Decision** yes, respond`}}
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
		t.Fatalf("expected only streamed natural chat request, got %d", len(client.requests))
	}
}

func TestNaturalMessageStaysSilentWhenGateOutputIsMalformedForPandaTopic(t *testing.T) {
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
	if len(client.requests) != 1 {
		t.Fatalf("expected only streamed natural chat request, got %d", len(client.requests))
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
