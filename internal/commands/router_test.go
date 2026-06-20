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
	"github.com/sn0w/panda2/internal/moderation"
	"github.com/sn0w/panda2/internal/ops"
	"github.com/sn0w/panda2/internal/queue"
	"github.com/sn0w/panda2/internal/ratelimit"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
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
	return NewRouter(
		admin.NewService(configs, usage, audit, memoryService, access, budgets, nil, "openrouter/auto", members),
		assistantService,
		moderation.NewService(assistantService),
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

func TestAdminSetupRequiresAdmin(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	response := router.Handle(context.Background(), Request{Command: "admin", Subcommand: "setup", GuildID: "guild-1", UserID: "user-1"})
	if !response.Ephemeral || response.Content == "" {
		t.Fatalf("expected denial response, got %+v", response)
	}
}

func TestAdminSetupCreatesConfig(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	response := router.Handle(context.Background(), Request{Command: "admin", Subcommand: "setup", GuildID: "guild-1", UserID: "admin", IsGuildAdmin: true})
	if !response.Ephemeral || response.Content == "" {
		t.Fatalf("expected setup response, got %+v", response)
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
		t.Fatalf("expected setup response, got %+v", response)
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

	setup := router.Handle(context.Background(), Request{Command: "admin", Subcommand: "setup", GuildID: "guild-1", UserID: "admin", IsGuildAdmin: true})
	if setup.Content == "" {
		t.Fatalf("expected setup response")
	}
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
	_ = router.Handle(context.Background(), Request{Command: "admin", Subcommand: "setup", GuildID: "guild-1", UserID: "admin", IsGuildAdmin: true})

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

func TestAdminUsageBreaksDownByModel(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Model: "fixture/model", Content: "ok", Usage: llm.Usage{TotalTokens: 12}}}
	router := newTestRouter(t, client, 10)

	for i := 0; i < 2; i++ {
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
	}
	usage := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "usage",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"by": "model", "window": "all"},
	})
	if !usage.Ephemeral || !strings.Contains(usage.Content, "Top model usage") || !strings.Contains(usage.Content, "fixture/model") || !strings.Contains(usage.Content, "24 total tokens") {
		t.Fatalf("unexpected usage response: %+v", usage)
	}
}

func TestAdminMemoryDeleteRequiresConfirmation(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	_ = router.Handle(context.Background(), Request{Command: "admin", Subcommand: "setup", GuildID: "guild-1", UserID: "admin", IsGuildAdmin: true})
	_ = router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "memory",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":  "add",
			"title":   "Deploy notes",
			"content": "Production deploys happen on Fridays after review.",
		},
	})

	request := Request{
		Command:      "admin",
		Subcommand:   "memory",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "delete", "document_id": "1"},
	}
	pending := router.Handle(context.Background(), request)
	confirmationID := requireConfirmation(t, pending)
	list := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "memory",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "list"},
	})
	if !strings.Contains(list.Content, "Deploy notes") {
		t.Fatalf("unconfirmed delete should leave document in place, got %+v", list)
	}

	request.Options["confirm"] = confirmationID
	deleted := router.Handle(context.Background(), request)
	if !strings.Contains(deleted.Content, "deleted") {
		t.Fatalf("expected confirmed delete, got %+v", deleted)
	}
	list = router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "memory",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "list"},
	})
	if !strings.Contains(list.Content, "No knowledge documents") {
		t.Fatalf("confirmed delete should remove document, got %+v", list)
	}
}

func TestAdminRemovalsRequireConfirmation(t *testing.T) {
	t.Run("role", func(t *testing.T) {
		router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 20)
		_ = router.Handle(context.Background(), Request{
			Command:      "admin",
			Subcommand:   "roles",
			GuildID:      "guild-1",
			UserID:       "admin",
			IsGuildAdmin: true,
			Options:      map[string]string{"action": "add", "role_id": "role-allowed"},
		})
		_ = router.Handle(context.Background(), Request{
			Command:      "admin",
			Subcommand:   "roles",
			GuildID:      "guild-1",
			UserID:       "admin",
			IsGuildAdmin: true,
			Options:      map[string]string{"action": "add", "role_id": "role-other"},
		})

		request := Request{
			Command:      "admin",
			Subcommand:   "roles",
			GuildID:      "guild-1",
			UserID:       "admin",
			IsGuildAdmin: true,
			Options:      map[string]string{"action": "remove", "role_id": "role-allowed"},
		}
		pending := router.Handle(context.Background(), request)
		confirmationID := requireConfirmation(t, pending)
		allowed := router.Handle(context.Background(), Request{
			Command:   "ask",
			GuildID:   "guild-1",
			ChannelID: "channel-1",
			UserID:    "user-1",
			RoleIDs:   []string{"role-allowed"},
			Options:   map[string]string{"question": "hi"},
		})
		if allowed.Content != "ok" {
			t.Fatalf("unconfirmed role removal should leave access in place, got %+v", allowed)
		}

		request.Options["confirm"] = confirmationID
		removed := router.Handle(context.Background(), request)
		if !strings.Contains(removed.Content, "no longer has") {
			t.Fatalf("expected confirmed role removal, got %+v", removed)
		}
		denied := router.Handle(context.Background(), Request{
			Command:   "ask",
			GuildID:   "guild-1",
			ChannelID: "channel-1",
			UserID:    "user-1",
			RoleIDs:   []string{"role-allowed"},
			Options:   map[string]string{"question": "hi"},
		})
		if !denied.Ephemeral || !strings.Contains(denied.Content, "permission") {
			t.Fatalf("confirmed role removal should remove access, got %+v", denied)
		}
	})

	t.Run("channel", func(t *testing.T) {
		router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 20)
		_ = router.Handle(context.Background(), Request{
			Command:      "admin",
			Subcommand:   "channels",
			GuildID:      "guild-1",
			UserID:       "admin",
			IsGuildAdmin: true,
			Options:      map[string]string{"action": "deny", "channel_id": "channel-1"},
		})

		request := Request{
			Command:      "admin",
			Subcommand:   "channels",
			GuildID:      "guild-1",
			UserID:       "admin",
			IsGuildAdmin: true,
			Options:      map[string]string{"action": "remove", "channel_id": "channel-1"},
		}
		pending := router.Handle(context.Background(), request)
		confirmationID := requireConfirmation(t, pending)
		denied := router.Handle(context.Background(), Request{
			Command:   "ask",
			GuildID:   "guild-1",
			ChannelID: "channel-1",
			UserID:    "user-1",
			Options:   map[string]string{"question": "hi"},
		})
		if !denied.Ephemeral || !strings.Contains(denied.Content, "permission") {
			t.Fatalf("unconfirmed channel removal should leave deny in place, got %+v", denied)
		}

		request.Options["confirm"] = confirmationID
		removed := router.Handle(context.Background(), request)
		if !strings.Contains(removed.Content, "rule removed") {
			t.Fatalf("expected confirmed channel removal, got %+v", removed)
		}
		allowed := router.Handle(context.Background(), Request{
			Command:   "ask",
			GuildID:   "guild-1",
			ChannelID: "channel-1",
			UserID:    "user-1",
			Options:   map[string]string{"question": "hi"},
		})
		if allowed.Content != "ok" {
			t.Fatalf("confirmed channel removal should allow requests, got %+v", allowed)
		}
	})

	t.Run("limit", func(t *testing.T) {
		router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 20)
		_ = router.Handle(context.Background(), Request{
			Command:      "admin",
			Subcommand:   "limits",
			GuildID:      "guild-1",
			UserID:       "admin",
			IsGuildAdmin: true,
			Options: map[string]string{
				"action":     "set",
				"scope":      "user",
				"subject_id": "user-1",
				"limit":      "1",
				"window":     "1h",
			},
		})

		request := Request{
			Command:      "admin",
			Subcommand:   "limits",
			GuildID:      "guild-1",
			UserID:       "admin",
			IsGuildAdmin: true,
			Options:      map[string]string{"action": "remove", "scope": "user", "subject_id": "user-1"},
		}
		pending := router.Handle(context.Background(), request)
		confirmationID := requireConfirmation(t, pending)
		first := router.Handle(context.Background(), Request{
			Command:   "ask",
			GuildID:   "guild-1",
			ChannelID: "channel-1",
			UserID:    "user-1",
			Options:   map[string]string{"question": "hi"},
		})
		if first.Content != "ok" {
			t.Fatalf("expected first request to pass, got %+v", first)
		}
		blocked := router.Handle(context.Background(), Request{
			Command:   "ask",
			GuildID:   "guild-1",
			ChannelID: "channel-1",
			UserID:    "user-1",
			Options:   map[string]string{"question": "hi"},
		})
		if !blocked.Ephemeral || !strings.Contains(blocked.Content, "budget is exhausted") {
			t.Fatalf("unconfirmed limit removal should leave budget in place, got %+v", blocked)
		}

		request.Options["confirm"] = confirmationID
		removed := router.Handle(context.Background(), request)
		if !strings.Contains(removed.Content, "Limit removed") {
			t.Fatalf("expected confirmed limit removal, got %+v", removed)
		}
		allowed := router.Handle(context.Background(), Request{
			Command:   "ask",
			GuildID:   "guild-1",
			ChannelID: "channel-1",
			UserID:    "user-1",
			Options:   map[string]string{"question": "hi"},
		})
		if allowed.Content != "ok" {
			t.Fatalf("confirmed limit removal should restore requests, got %+v", allowed)
		}
	})
}

func TestAdminDisableRequiresConfirmation(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "ok"}}, 20)
	_ = router.Handle(context.Background(), Request{Command: "admin", Subcommand: "setup", GuildID: "guild-1", UserID: "admin", IsGuildAdmin: true})

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
	_ = router.Handle(context.Background(), Request{Command: "admin", Subcommand: "setup", GuildID: "guild-1", UserID: "admin", IsGuildAdmin: true})

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

	role := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "roles",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "add", "role_id": "role-dry", "dry_run": "true"},
	})
	if !strings.Contains(role.Content, "Dry run") {
		t.Fatalf("expected role dry run response, got %+v", role)
	}
	roles := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "roles",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "list"},
	})
	if strings.Contains(roles.Content, "role-dry") {
		t.Fatalf("dry-run role add should not persist role, got %+v", roles)
	}

	channel := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "channels",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "deny", "channel_id": "channel-1", "dry_run": "true"},
	})
	if !strings.Contains(channel.Content, "Dry run") {
		t.Fatalf("expected channel dry run response, got %+v", channel)
	}
	ask = router.Handle(context.Background(), Request{
		Command:   "ask",
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "user-1",
		Options:   map[string]string{"question": "hi"},
	})
	if ask.Content != "ok" {
		t.Fatalf("dry-run channel deny should not block assistant, got %+v", ask)
	}

	limit := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "limits",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":     "set",
			"scope":      "user",
			"subject_id": "user-1",
			"limit":      "1",
			"window":     "1h",
			"dry_run":    "true",
		},
	})
	if !strings.Contains(limit.Content, "Dry run") {
		t.Fatalf("expected limit dry run response, got %+v", limit)
	}
	for i := 0; i < 2; i++ {
		ask = router.Handle(context.Background(), Request{
			Command:   "ask",
			GuildID:   "guild-1",
			ChannelID: "channel-1",
			UserID:    "user-1",
			Options:   map[string]string{"question": "hi"},
		})
		if ask.Content != "ok" {
			t.Fatalf("dry-run limit set should not create budget, got %+v", ask)
		}
	}

	memory := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "memory",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "add", "title": "Dry document", "content": "preview", "dry_run": "true"},
	})
	if !strings.Contains(memory.Content, "Dry run") {
		t.Fatalf("expected memory dry run response, got %+v", memory)
	}
	documents := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "memory",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "list"},
	})
	if strings.Contains(documents.Content, "Dry document") {
		t.Fatalf("dry-run memory add should not persist document, got %+v", documents)
	}
}

func TestGlobalLimitRemoveRequiresOwner(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 20)
	set := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "limits",
		GuildID:      "guild-1",
		UserID:       "owner",
		IsGuildAdmin: true,
		IsOwner:      true,
		Options: map[string]string{
			"action":  "set",
			"scope":   "global",
			"limit":   "1",
			"window":  "1h",
			"confirm": "unused",
		},
	})
	if !strings.Contains(set.Content, "Limit set") {
		t.Fatalf("expected owner to set global limit, got %+v", set)
	}

	remove := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "limits",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "remove", "scope": "global"},
	})
	if !remove.Ephemeral || !strings.Contains(remove.Content, "Only a bot owner") {
		t.Fatalf("expected global limit remove owner denial, got %+v", remove)
	}
}

func TestAdminRoleChooseUsesPermissionSelect(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 20)
	choose := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "roles",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "choose", "role_id": "mod-role"},
	})
	if !choose.Ephemeral || choose.Select == nil || choose.Select.ID == "" || len(choose.Select.Options) == 0 {
		t.Fatalf("expected permission select response, got %+v", choose)
	}

	request, ok := RequestFromSelectID(choose.Select.ID, []string{admin.PermissionModerationUse}, Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	})
	if !ok {
		t.Fatal("expected role permission select to parse")
	}
	added := router.Handle(context.Background(), request)
	if !strings.Contains(added.Content, admin.PermissionModerationUse) {
		t.Fatalf("expected selected permission to be saved, got %+v", added)
	}
	roles := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "roles",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "list"},
	})
	if !strings.Contains(roles.Content, "mod-role") || !strings.Contains(roles.Content, admin.PermissionModerationUse) {
		t.Fatalf("expected selected role permission in list, got %+v", roles)
	}

	_, ok = RequestFromSelectID(choose.Select.ID, []string{admin.PermissionModerationUse}, Request{UserID: "other-admin"})
	if ok {
		t.Fatal("role permission select should be scoped to the original user")
	}
	_, ok = RequestFromSelectID(choose.Select.ID, []string{"not.allowed"}, Request{UserID: "admin"})
	if ok {
		t.Fatal("role permission select should reject unsupported permissions")
	}
}

func TestAdminPromptWithoutTextUsesModal(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 20)
	_ = router.Handle(context.Background(), Request{Command: "admin", Subcommand: "setup", GuildID: "guild-1", UserID: "admin", IsGuildAdmin: true})

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

func TestConfirmationIDRestoresCommandRequest(t *testing.T) {
	base := Request{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
	}
	id := roleRemoveConfirmationID("admin", "role-1", admin.PermissionModerationUse)
	request, ok := RequestFromConfirmationID(id, base)
	if !ok {
		t.Fatal("expected confirmation id to parse")
	}
	if request.Command != "admin" || request.Subcommand != "roles" || request.Options["action"] != "remove" || request.Options["role_id"] != "role-1" || request.Options["permission"] != admin.PermissionModerationUse || request.Options["confirm"] != id {
		t.Fatalf("unexpected request from confirmation id: %+v", request)
	}

	base.UserID = "other-admin"
	if _, ok := RequestFromConfirmationID(id, base); ok {
		t.Fatal("confirmation id should be scoped to the original user")
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

func TestMemoryAddAndSearch(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	_ = router.Handle(context.Background(), Request{Command: "admin", Subcommand: "setup", GuildID: "guild-1", UserID: "admin", IsGuildAdmin: true})

	add := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "memory",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":  "add",
			"title":   "Deploy notes",
			"content": "Production deploys happen on Fridays after review.",
		},
	})
	if !strings.Contains(add.Content, "Deploy notes") {
		t.Fatalf("unexpected add response: %+v", add)
	}
	_ = router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "memory",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "enable"},
	})

	search := router.Handle(context.Background(), Request{
		Command: "search-memory",
		GuildID: "guild-1",
		UserID:  "user-1",
		Options: map[string]string{"query": "Friday deploys"},
	})
	if !search.Ephemeral || !strings.Contains(search.Content, "Deploy notes") {
		t.Fatalf("unexpected search response: %+v", search)
	}
}

func TestMemoryReadPermissionGatesSearchMemory(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	_ = router.Handle(context.Background(), Request{Command: "admin", Subcommand: "setup", GuildID: "guild-1", UserID: "admin", IsGuildAdmin: true})
	_ = router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "memory",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":  "add",
			"title":   "Deploy notes",
			"content": "Production deploys happen on Fridays after review.",
		},
	})
	_ = router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "memory",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "enable"},
	})
	_ = router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "roles",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":     "add",
			"role_id":    "memory-role",
			"permission": admin.PermissionAssistantMemoryRead,
		},
	})

	denied := router.Handle(context.Background(), Request{
		Command: "search-memory",
		GuildID: "guild-1",
		UserID:  "user-1",
		RoleIDs: []string{"other-role"},
		Options: map[string]string{"query": "Friday deploys"},
	})
	if !denied.Ephemeral || !strings.Contains(denied.Content, "server knowledge") {
		t.Fatalf("expected memory permission denial, got %+v", denied)
	}

	allowed := router.Handle(context.Background(), Request{
		Command: "search-memory",
		GuildID: "guild-1",
		UserID:  "user-1",
		RoleIDs: []string{"memory-role"},
		Options: map[string]string{"query": "Friday deploys"},
	})
	if !allowed.Ephemeral || !strings.Contains(allowed.Content, "Deploy notes") {
		t.Fatalf("expected memory search result, got %+v", allowed)
	}
}

func TestSearchMemoryRequiresEnabledMemory(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)
	_ = router.Handle(context.Background(), Request{Command: "admin", Subcommand: "setup", GuildID: "guild-1", UserID: "admin", IsGuildAdmin: true})

	response := router.Handle(context.Background(), Request{
		Command: "search-memory",
		GuildID: "guild-1",
		UserID:  "user-1",
		Options: map[string]string{"query": "deploys"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "disabled") {
		t.Fatalf("expected disabled memory response, got %+v", response)
	}
}

func TestMemoryConsentCommandManagesUserConsent(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{}, 5)

	dm := router.Handle(context.Background(), Request{
		Command: "memory-consent",
		UserID:  "user-1",
		Options: map[string]string{"action": "status"},
	})
	if !dm.Ephemeral || !strings.Contains(dm.Content, "inside a Discord server") {
		t.Fatalf("expected guild-only response, got %+v", dm)
	}

	status := router.Handle(context.Background(), Request{
		Command: "memory-consent",
		GuildID: "guild-1",
		UserID:  "user-1",
		Options: map[string]string{"action": "status"},
	})
	if !status.Ephemeral || !strings.Contains(status.Content, "disabled") {
		t.Fatalf("expected disabled default consent, got %+v", status)
	}

	enabled := router.Handle(context.Background(), Request{
		Command: "memory-consent",
		GuildID: "guild-1",
		UserID:  "user-1",
		Options: map[string]string{"action": "enable"},
	})
	if !strings.Contains(enabled.Content, "enabled") {
		t.Fatalf("expected enabled consent response, got %+v", enabled)
	}
	status = router.Handle(context.Background(), Request{
		Command: "memory-consent",
		GuildID: "guild-1",
		UserID:  "user-1",
		Options: map[string]string{"action": "status"},
	})
	if !strings.Contains(status.Content, "enabled") {
		t.Fatalf("expected enabled status, got %+v", status)
	}

	disabled := router.Handle(context.Background(), Request{
		Command: "memory-consent",
		GuildID: "guild-1",
		UserID:  "user-1",
		Options: map[string]string{"action": "disable"},
	})
	if !strings.Contains(disabled.Content, "disabled") {
		t.Fatalf("expected disabled consent response, got %+v", disabled)
	}
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
	add := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "roles",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":     "add",
			"role_id":    "thread-role",
			"permission": admin.PermissionAssistantUseThreads,
		},
	})
	if !strings.Contains(add.Content, "thread-role") {
		t.Fatalf("unexpected role response: %+v", add)
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
	_ = router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "roles",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":     "add",
			"role_id":    "attachment-role",
			"permission": admin.PermissionAssistantAttachments,
		},
	})

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
	add := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "roles",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "add", "role_id": "role-allowed"},
	})
	if !strings.Contains(add.Content, "role-allowed") {
		t.Fatalf("unexpected role add response: %+v", add)
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
	deny := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "channels",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"action": "deny", "channel_id": "channel-1"},
	})
	if !strings.Contains(deny.Content, "deny") {
		t.Fatalf("unexpected channel deny response: %+v", deny)
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

	set := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "limits",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":     "set",
			"scope":      "user",
			"subject_id": "user-1",
			"limit":      "1",
			"window":     "1h",
		},
	})
	if !strings.Contains(set.Content, "Limit set") {
		t.Fatalf("unexpected set response: %+v", set)
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

func TestModHelperRequiresPermission(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "mod ok"}}, 5)
	response := router.Handle(context.Background(), Request{
		Command:    "mod",
		Subcommand: "triage",
		GuildID:    "guild-1",
		ChannelID:  "channel-1",
		UserID:     "user-1",
		Options:    map[string]string{"text": "heated argument in general"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "permission") {
		t.Fatalf("expected moderation permission denial, got %+v", response)
	}
}

func TestModHelperAllowsAdminAndAudits(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "suggestion only"}}
	router := newTestRouter(t, client, 5)
	response := router.Handle(context.Background(), Request{
		Command:      "mod",
		Subcommand:   "note",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"text":       "user cooled down after warning",
			"subject_id": "user-1",
		},
	})
	if !response.Ephemeral || response.Content != "suggestion only" {
		t.Fatalf("unexpected moderation response: %+v", response)
	}
	if len(client.requests) != 1 || client.requests[0].Messages[len(client.requests[0].Messages)-1].Content == "" {
		t.Fatalf("expected moderation LLM request, got %+v", client.requests)
	}
}

func TestModHelperAllowsMappedRole(t *testing.T) {
	router := newTestRouter(t, &fakeLLM{response: llm.ChatResponse{Content: "slowmode suggestion"}}, 5)
	add := router.Handle(context.Background(), Request{
		Command:      "admin",
		Subcommand:   "roles",
		GuildID:      "guild-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"action":     "add",
			"role_id":    "mod-role",
			"permission": "moderation.use",
		},
	})
	if !strings.Contains(add.Content, "mod-role") {
		t.Fatalf("unexpected role response: %+v", add)
	}

	response := router.Handle(context.Background(), Request{
		Command:    "mod",
		Subcommand: "slowmode",
		GuildID:    "guild-1",
		ChannelID:  "channel-1",
		UserID:     "moderator",
		RoleIDs:    []string{"mod-role"},
		Options:    map[string]string{"text": "many users are posting too quickly"},
	})
	if response.Content != "slowmode suggestion" {
		t.Fatalf("expected mapped role moderation response, got %+v", response)
	}
}

func TestModHistorySummarizesOnlySubjectMessages(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "history summary"}}
	router := newTestRouter(t, client, 5).WithContextService(contextsvc.NewService(fakeContextProvider{messages: []contextsvc.Message{
		{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "message-1", AuthorID: "subject-1", Content: "visible subject message"},
		{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "message-2", AuthorID: "user-2", Content: "other user noise"},
		{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "message-3", AuthorID: "subject-1", Content: "second subject message"},
	}}))

	response := router.Handle(context.Background(), Request{
		Command:      "mod",
		Subcommand:   "history",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options: map[string]string{
			"subject_id":   "subject-1",
			"recent_limit": "5",
			"text":         "check for repeated escalation",
		},
	})
	if response.Content != "history summary" {
		t.Fatalf("unexpected history response: %+v", response)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one LLM request, got %+v", client.requests)
	}
	joined := joinLLMMessages(client.requests[0].Messages)
	if !strings.Contains(joined, "Subject user id: subject-1") || !strings.Contains(joined, "visible subject message") || !strings.Contains(joined, "second subject message") {
		t.Fatalf("expected subject history in LLM request, got %s", joined)
	}
	if !strings.Contains(joined, "Moderator-provided context") || !strings.Contains(joined, "check for repeated escalation") {
		t.Fatalf("expected moderator note in LLM request, got %s", joined)
	}
	if strings.Contains(joined, "other user noise") {
		t.Fatalf("non-subject message leaked into LLM request: %s", joined)
	}
}

func TestModHistoryRequiresSubjectID(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "history summary"}}
	router := newTestRouter(t, client, 5).WithContextService(contextsvc.NewService(fakeContextProvider{}))

	response := router.Handle(context.Background(), Request{
		Command:      "mod",
		Subcommand:   "history",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"recent_limit": "5"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "subject_id") {
		t.Fatalf("expected subject_id validation, got %+v", response)
	}
	if len(client.requests) != 0 {
		t.Fatalf("history validation should not call LLM, got %+v", client.requests)
	}
}

func TestModHistoryReportsNoVisibleMessages(t *testing.T) {
	client := &fakeLLM{response: llm.ChatResponse{Content: "history summary"}}
	router := newTestRouter(t, client, 5).WithContextService(contextsvc.NewService(fakeContextProvider{messages: []contextsvc.Message{
		{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "message-1", AuthorID: "user-2", Content: "other user noise"},
	}}))

	response := router.Handle(context.Background(), Request{
		Command:      "mod",
		Subcommand:   "history",
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		UserID:       "admin",
		IsGuildAdmin: true,
		Options:      map[string]string{"subject_id": "subject-1"},
	})
	if !response.Ephemeral || !strings.Contains(response.Content, "No recent visible messages") {
		t.Fatalf("expected empty history response, got %+v", response)
	}
	if len(client.requests) != 0 {
		t.Fatalf("empty history should not call LLM, got %+v", client.requests)
	}
}

func joinLLMMessages(messages []llm.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n")
}
