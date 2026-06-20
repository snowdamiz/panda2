package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/assistant"
	"github.com/sn0w/panda2/internal/config"
	contextsvc "github.com/sn0w/panda2/internal/context"
	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/memory"
	"github.com/sn0w/panda2/internal/ops"
	"github.com/sn0w/panda2/internal/queue"
	"github.com/sn0w/panda2/internal/ratelimit"
	"github.com/sn0w/panda2/internal/repository"
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

func newTestRouter(t *testing.T, client *fakeLLM, limit int) *Router {
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
	memoryService := memory.NewService(knowledge)
	conversations := repository.NewConversationRepository(db.DB)
	jobs := repository.NewJobRepository(db.DB)
	worker := queue.NewWorker(jobs, "test-worker")
	opsService := ops.NewService(config.Config{DataDir: t.TempDir()}, db, configs, jobs, worker)
	assistantService := assistant.NewService(client, usage, configs, memoryService, conversations, "openrouter/auto", nil)
	adminService := admin.NewService(configs, usage, audit, memoryService, access, budgets, nil, "openrouter/auto", members)
	registry, err := tools.NewDefaultRegistry()
	if err != nil {
		t.Fatalf("tool registry: %v", err)
	}
	assistantService.WithToolExecutor(tools.NewExecutor(registry, memoryService, configs).WithAdminOperations(adminService))
	return NewRouter(
		adminService,
		assistantService,
		opsService,
		ratelimit.New(limit, time.Minute),
	)
}

func TestRouterPing(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	response := router.Handle(context.Background(), Request{Command: "ping", UserID: "user-1"})
	if response.Content != "pong" || !response.Ephemeral {
		t.Fatalf("unexpected ping response: %+v", response)
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
	for _, want := range []string{
		"**Admin commands**",
		"- `/admin badge role:@Role` - treat that Discord role as Panda admins.",
		"- `/admin tool action:list` - show role-specific native and composed tool grants.",
		"- `/admin tool action:add tool_name:<tool> role:@Role` - allow a role to use a specific tool.",
		"- `/admin tool action:remove tool_name:<tool> role:@Role` - remove that tool grant.",
		"- `/admin model model:<slug> fallback_models:<slug,slug> temperature:<0-2> max_response_tokens:<64-4000> tool_policy:<policy> dry_run:<true|false>` - update model routing and the server-wide tool ceiling.",
		"- Tool policy choices: `off`, `read_only`, `assistive`, `admin_only`, `moderator`, `write_confirmed`, `owner_ops`.",
		"- `/admin prompt prompt:<text> dry_run:<true|false>` - set server instructions; omit `prompt` to open the modal.",
		"- `/admin audit` - show recent privileged changes.",
		"- `/admin enable dry_run:<true|false>` - allow Panda to answer again.",
		"- `/admin disable confirm:<confirmation> dry_run:<true|false>` - pause Panda after confirmation.",
		"- `/admin soul soul:<text> dry_run:<true|false>` - set Panda's personality and tone; omit `soul` to open the modal.",
		"**Moderator tools**",
		"**Composed tools**",
		"- `/tool draft request:<description> dry_run:<true|false>` - draft a composed tool from natural language.",
		"- `/tool draft spec_json:<json> dry_run:<true|false>` - draft from a complete composed-tool spec.",
		"- Draft helpers: `role_id`, `role_name`, `channel_id`, `channel_name`, `welcome_text`, `model`.",
		"- `/tool approve tool:<name> version:<n> confirm:<confirmation>` - approve and enable a version.",
		"- `/tool list` - list composed tools for this server.",
		"- `/tool show tool:<name>` - inspect versions and recent runs.",
		"- `/tool pause|resume|disable|archive tool:<name> dry_run:<true|false>` - change tool status.",
		"- `/tool run tool:<name> input_json:<object>` - run an approved composed tool.",
		"- `/tool simulate tool:<name> input_json:<object>` - run with dry-run writes.",
		"- `/tool export tool:<name>` - export the approved spec JSON.",
		"- `/tool rollback tool:<name> version:<n> confirm:<confirmation>` - roll back to an approved version.",
		"Moderation guidance",
	} {
		if !strings.Contains(response.Content, want) {
			t.Fatalf("admin help response missing %q:\n%s", want, response.Content)
		}
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
	for _, want := range []string{
		"**Moderator tools**",
		"- `/admin soul soul:<text> dry_run:<true|false>` - set Panda's personality and tone; omit `soul` to open the modal.",
		"Moderation guidance",
	} {
		if !strings.Contains(response.Content, want) {
			t.Fatalf("moderator help response missing %q:\n%s", want, response.Content)
		}
	}
	for _, hidden := range []string{
		"`/admin badge`",
		"`/admin model`",
		"Composed tools",
		"`/tool draft`",
		"Role/channel access",
	} {
		if strings.Contains(response.Content, hidden) {
			t.Fatalf("moderator help should not include %q:\n%s", hidden, response.Content)
		}
	}
}

func TestAdminBadgeRequiresAdmin(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	response := router.Handle(context.Background(), Request{Command: "admin", Subcommand: "badge", GuildID: "guild-1", UserID: "user-1"})
	if !response.Ephemeral || response.Content == "" {
		t.Fatalf("expected denial response, got %+v", response)
	}
}

func TestAdminBadgeConfiguresDelegatedAdminRole(t *testing.T) {
	ctx := context.Background()
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 5)
	badge := router.Handle(ctx, Request{
		Command:      "admin",
		Subcommand:   "badge",
		GuildID:      "guild-1",
		UserID:       "owner",
		IsGuildAdmin: true,
		Options: map[string]string{
			"badge_role_id":   "role-mod",
			"badge_role_name": "MOD",
		},
	})
	if !badge.Ephemeral || !strings.Contains(badge.Content, "Admin badge set to `MOD` (`role-mod`)") {
		t.Fatalf("expected badge command to configure admin badge, got %+v", badge)
	}

	help := router.Handle(ctx, Request{Command: "help", GuildID: "guild-1", UserID: "mod", RoleIDs: []string{"role-mod"}})
	for _, want := range []string{"**Admin commands**", "**Moderator tools**", "Role/channel access"} {
		if !strings.Contains(help.Content, want) {
			t.Fatalf("expected admin badge help to include %q:\n%s", want, help.Content)
		}
	}

	model := router.Handle(ctx, Request{
		Command:    "admin",
		Subcommand: "model",
		GuildID:    "guild-1",
		UserID:     "mod",
		RoleIDs:    []string{"role-mod"},
		Options:    map[string]string{"model": "provider/new"},
	})
	if !strings.Contains(model.Content, "Model settings updated") {
		t.Fatalf("expected admin badge to allow admin model update, got %+v", model)
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

func TestAdminModelAffectsAsk(t *testing.T) {
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
	if !strings.Contains(model.Content, "anthropic/claude-fixture") {
		t.Fatalf("unexpected model response: %+v", model)
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
	if len(client.requests) != 1 || client.requests[0].Model != "anthropic/claude-fixture" {
		t.Fatalf("expected ask to use configured model, got %+v", client.requests)
	}
}

func TestAdminModelSetsRuntimeOptions(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "ok"}}
	router := newTestRouter(t, client, 5)

	model := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "model",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"model":               "provider/primary",
			"fallback_models":     "provider/fallback-a, provider/fallback-b",
			"temperature":         "0.7",
			"max_response_tokens": "1234",
			"tool_policy":         "read_only",
		},
	})
	if !strings.Contains(model.Content, "2 fallback") || !strings.Contains(model.Content, "0.70") || !strings.Contains(model.Content, "1234") || !strings.Contains(model.Content, "read_only") {
		t.Fatalf("unexpected model settings response: %+v", model)
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
	if request.Model != "provider/primary" || request.Temperature != 0.7 || request.MaxTokens != 1234 {
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

func TestAdminDryRunDoesNotMutateState(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "ok"}}
	router := newTestRouter(t, client, 20)

	model := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "model",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"model": "provider/new", "dry_run": "true"},
	})
	if !strings.Contains(model.Content, "Dry run") || !strings.Contains(model.Content, "provider/new") {
		t.Fatalf("expected model dry run response, got %+v", model)
	}
	ask := router.Handle(context.Background(), Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"question": "hi"},
	})
	if ask.Content != "ok" || len(client.requests) != 1 || client.requests[0].Model != "openrouter/auto" {
		t.Fatalf("dry-run model change should not affect ask request, response=%+v requests=%+v", ask, client.requests)
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

	base.UserID = "other-admin"
	if _, ok := RequestFromConfirmationID(id, base); ok {
		t.Fatal("confirmation id should be scoped to the original user")
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

func TestLLMToolConfirmationRendersButton(t *testing.T) {
	client := &fakeLLM{responses: []llm.ChatResponse{
		{
			Model:   "fixture/model",
			Content: "",
			ToolCalls: []llm.ToolCall{{
				ID:   "call-remove-channel",
				Type: "function",
				Function: llm.ToolCallFunction{
					Name:      "panda_manage_channel_rule",
					Arguments: `{"action":"remove","channel_id":"channel-1"}`,
				},
			}},
		},
		{Model: "fixture/model", Content: "I found the channel rule and prepared the removal."},
	}}
	router := newTestRouter(t, client, 20)
	model := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "model",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"tool_policy": "write_confirmed"},
	})
	if !strings.Contains(model.Content, "write_confirmed") {
		t.Fatalf("expected tool policy update, got %+v", model)
	}

	response := router.Handle(context.Background(), Request{
		Command:      "chat",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"question": "Panda remove the channel rule for channel-1"},
	})
	if response.Confirmation == nil || !response.Confirmation.Danger || !strings.Contains(response.Content, "confirmation button") {
		t.Fatalf("expected LLM-triggered confirmation response, got %+v", response)
	}
	request, ok := RequestFromToolConfirmationID(response.Confirmation.ID, Request{UserID: "admin"})
	if !ok || request.Action != toolActionChannelRuleRemove || request.Options["channel_id"] != "channel-1" {
		t.Fatalf("unexpected rendered confirmation id: request=%+v ok=%t", request, ok)
	}
	if len(client.requests) != 2 || len(client.requests[0].Tools) == 0 {
		t.Fatalf("expected tool-enabled first model request, got %+v", client.requests)
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
	if response.Content != "chat fixture" {
		t.Fatalf("unexpected chat response: %+v", response)
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
		{Content: `{"respond":true,"prompt":"continue"}`},
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

func TestNaturalMessageDoesNotRespondWhenTriggerDeclines(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: `{"respond":false,"prompt":""}`}}
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
	audit := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "audit",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if !strings.Contains(audit.Content, "context.read") {
		t.Fatalf("expected context read audit event, got %+v", audit)
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
	router := newTestRouter(t, client, 5)

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

	completed := router.HandleBackgroundTask(context.Background(), *response.Background)
	if completed.Content != "background summary" {
		t.Fatalf("expected completed background summary, got %+v", completed)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one LLM request after background execution, got %+v", client.requests)
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
