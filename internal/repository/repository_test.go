package repository

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sn0w/panda2/internal/store"
)

func TestGuildConfigEnsureDefaultIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewGuildConfigRepository(db.DB)
	config, err := repo.EnsureDefault(ctx, "guild-1", "openrouter/auto")
	if err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if config.DefaultModel != "openrouter/auto" {
		t.Fatalf("unexpected model %q", config.DefaultModel)
	}
	if config.ToolPolicy != "admin_only" || !config.MemoryEnabled {
		t.Fatalf("unexpected defaults: tool_policy=%q memory_enabled=%t", config.ToolPolicy, config.MemoryEnabled)
	}

	again, err := repo.EnsureDefault(ctx, "guild-1", "different/model")
	if err != nil {
		t.Fatalf("EnsureDefault again: %v", err)
	}
	if again.DefaultModel != "openrouter/auto" {
		t.Fatalf("expected existing model to remain, got %q", again.DefaultModel)
	}
}

func TestUsageRecord(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewUsageRepository(db.DB)
	if err := repo.Record(ctx, store.UsageEvent{UserID: "user-1", Command: "ask", Success: true}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	count, err := repo.CountByUser(ctx, "user-1")
	if err != nil {
		t.Fatalf("CountByUser: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one usage event, got %d", count)
	}
}

func TestUsageBreakdownByModel(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewUsageRepository(db.DB)
	events := []store.UsageEvent{
		{GuildID: "guild-1", UserID: "user-1", ChannelID: "channel-1", Command: "ask", Model: "model-a", TotalTokens: 10, Success: true},
		{GuildID: "guild-1", UserID: "user-2", ChannelID: "channel-1", Command: "chat", Model: "model-a", TotalTokens: 20, Success: false},
		{GuildID: "guild-1", UserID: "user-2", ChannelID: "channel-2", Command: "ask", Model: "model-b", TotalTokens: 5, Success: true},
	}
	for _, event := range events {
		if err := repo.Record(ctx, event); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	rows, err := repo.BreakdownByGuild(ctx, "guild-1", time.Time{}, "model", 5)
	if err != nil {
		t.Fatalf("BreakdownByGuild: %v", err)
	}
	if len(rows) != 2 || rows[0].Label != "model-a" || rows[0].TotalRequests != 2 || rows[0].Failed != 1 || rows[0].TotalTokens != 30 {
		t.Fatalf("unexpected breakdown rows: %+v", rows)
	}
	if _, err := repo.BreakdownByGuild(ctx, "guild-1", time.Time{}, "model; drop table usage_events", 5); err == nil {
		t.Fatal("expected unsupported dimension to fail")
	}
}

func TestMemberMemoryConsentDefaultsFalseAndUpdates(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewMemberRepository(db.DB)
	consent, err := repo.MemoryConsent(ctx, "guild-1", "user-1")
	if err != nil {
		t.Fatalf("MemoryConsent: %v", err)
	}
	if consent {
		t.Fatal("memory consent should default to false")
	}

	member, err := repo.SetMemoryConsent(ctx, "guild-1", "user-1", true)
	if err != nil {
		t.Fatalf("SetMemoryConsent true: %v", err)
	}
	if !member.MemoryConsent || !member.AssistantAllowed {
		t.Fatalf("unexpected member after enabling consent: %+v", member)
	}
	consent, err = repo.MemoryConsent(ctx, "guild-1", "user-1")
	if err != nil || !consent {
		t.Fatalf("expected enabled consent, got %t err=%v", consent, err)
	}

	member, err = repo.SetMemoryConsent(ctx, "guild-1", "user-1", false)
	if err != nil {
		t.Fatalf("SetMemoryConsent false: %v", err)
	}
	if member.MemoryConsent {
		t.Fatalf("expected disabled consent, got %+v", member)
	}
}

func TestKnowledgeSearchUsesLocalIndex(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewKnowledgeRepository(db.DB)
	document, err := repo.AddDocument(ctx, store.KnowledgeDocument{
		GuildID:   "guild-1",
		Title:     "Refund policy",
		CreatedBy: "admin",
	}, "Refunds are available within 14 days when a receipt is provided.")
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}

	results, err := repo.Search(ctx, "guild-1", "receipt refunds", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].DocumentID != document.ID || results[0].Title != "Refund policy" {
		t.Fatalf("unexpected result: %+v", results[0])
	}
}

func TestKnowledgeChunksDoNotSplitUTF8Runes(t *testing.T) {
	chunks := splitKnowledgeChunks("x" + strings.Repeat("界", 500))
	if len(chunks) < 2 {
		t.Fatalf("expected content to split into multiple chunks, got %d", len(chunks))
	}
	for _, chunk := range chunks {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk is not valid UTF-8: %q", chunk)
		}
	}
}

func TestAttachmentGetByGuild(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewAttachmentRepository(db.DB)
	attachment, err := repo.Record(ctx, store.Attachment{
		GuildID:       "guild-1",
		ChannelID:     "channel-1",
		MessageID:     "message-1",
		Filename:      "notes.md",
		ExtractedText: "Deploy after review.",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	found, err := repo.Get(ctx, "guild-1", attachment.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found.Filename != "notes.md" || found.ExtractedText == "" {
		t.Fatalf("unexpected attachment: %+v", found)
	}
	if _, err := repo.Get(ctx, "other-guild", attachment.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for other guild, got %v", err)
	}
}

func TestConversationRecentMessagesStayChronological(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewConversationRepository(db.DB)
	conversation, err := repo.GetOrCreateActive(ctx, ConversationKey{
		GuildID:     "guild-1",
		ChannelID:   "channel-1",
		OwnerUserID: "user-1",
		Title:       "question",
	})
	if err != nil {
		t.Fatalf("GetOrCreateActive: %v", err)
	}
	for _, content := range []string{"one", "two", "three"} {
		if err := repo.AppendMessage(ctx, store.AssistantMessage{
			ConversationID: conversation.ID,
			GuildID:        "guild-1",
			ChannelID:      "channel-1",
			UserID:         "user-1",
			Role:           "user",
			ContentPreview: content,
		}); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	messages, err := repo.RecentMessages(ctx, conversation.ID, 2)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if got := []string{messages[0].ContentPreview, messages[1].ContentPreview}; got[0] != "two" || got[1] != "three" {
		t.Fatalf("unexpected recent messages: %v", got)
	}
}

func TestJobClaimAndRetry(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewJobRepository(db.DB)
	now := time.Now().UTC().Add(time.Hour)
	job, err := repo.Enqueue(ctx, store.Job{Kind: "summarize", GuildID: "guild-1", Payload: "{}"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	claimed, ok, err := repo.ClaimNext(ctx, "summarize", "worker-1", time.Minute, now)
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if !ok || claimed.ID != job.ID || claimed.Attempts != 1 || claimed.LockOwner != "worker-1" {
		t.Fatalf("unexpected claimed job: ok=%v job=%+v", ok, claimed)
	}
	if err := repo.Fail(ctx, claimed.ID, "temporary", 5*time.Minute, now); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	depth, err := repo.QueueDepth(ctx, "summarize")
	if err != nil {
		t.Fatalf("QueueDepth: %v", err)
	}
	if depth != 1 {
		t.Fatalf("expected retried job in queue, got depth %d", depth)
	}
}

func TestJobClaimReclaimsExpiredLease(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	repo := NewJobRepository(db.DB)
	job, err := repo.Enqueue(ctx, store.Job{Kind: "summarize", GuildID: "guild-1", Payload: "{}", MaxAttempts: 2, RunAfter: now.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, ok, err := repo.ClaimNext(ctx, "summarize", "worker-1", time.Minute, now)
	if err != nil || !ok {
		t.Fatalf("first ClaimNext ok=%t err=%v", ok, err)
	}
	if claimed.ID != job.ID || claimed.Attempts != 1 {
		t.Fatalf("unexpected first claim: %+v", claimed)
	}

	if _, ok, err := repo.ClaimNext(ctx, "summarize", "worker-2", time.Minute, now.Add(30*time.Second)); err != nil || ok {
		t.Fatalf("active lease should not be reclaimed: ok=%t err=%v", ok, err)
	}

	reclaimed, ok, err := repo.ClaimNext(ctx, "summarize", "worker-2", time.Minute, now.Add(2*time.Minute))
	if err != nil || !ok {
		t.Fatalf("expired ClaimNext ok=%t err=%v", ok, err)
	}
	if reclaimed.ID != job.ID || reclaimed.Attempts != 2 || reclaimed.LockOwner != "worker-2" {
		t.Fatalf("unexpected reclaimed job: %+v", reclaimed)
	}
}

func TestJobClaimFailsExpiredLeaseAfterMaxAttempts(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	repo := NewJobRepository(db.DB)
	job, err := repo.Enqueue(ctx, store.Job{Kind: "summarize", GuildID: "guild-1", Payload: "{}", MaxAttempts: 1, RunAfter: now.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, ok, err := repo.ClaimNext(ctx, "summarize", "worker-1", time.Minute, now); err != nil || !ok {
		t.Fatalf("first ClaimNext ok=%t err=%v", ok, err)
	}
	if _, ok, err := repo.ClaimNext(ctx, "summarize", "worker-2", time.Minute, now.Add(2*time.Minute)); err != nil || ok {
		t.Fatalf("maxed expired lease should not be reclaimed: ok=%t err=%v", ok, err)
	}

	var saved store.Job
	if err := db.DB.First(&saved, job.ID).Error; err != nil {
		t.Fatalf("lookup job: %v", err)
	}
	if saved.Status != "failed" || !strings.Contains(saved.LastError, "lease expired") || saved.LockOwner != "" || saved.LeaseExpiresAt != nil {
		t.Fatalf("expected failed expired job, got %+v", saved)
	}
}

func TestBudgetCheckAndConsumeUsesDurableWindow(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewBudgetRepository(db.DB)
	if _, err := repo.SetLimit(ctx, store.BudgetLimit{
		GuildID:       "guild-1",
		Scope:         BudgetScopeUser,
		SubjectID:     "user-1",
		Limit:         1,
		WindowSeconds: 3600,
	}); err != nil {
		t.Fatalf("SetLimit: %v", err)
	}

	now := time.Date(2026, 6, 19, 12, 30, 0, 0, time.UTC)
	request := BudgetCheckRequest{GuildID: "guild-1", UserID: "user-1", ChannelID: "channel-1", Now: now}
	if _, denied, err := repo.CheckAndConsume(ctx, request); err != nil || denied {
		t.Fatalf("first CheckAndConsume denied=%v err=%v", denied, err)
	}
	denial, denied, err := repo.CheckAndConsume(ctx, request)
	if err != nil {
		t.Fatalf("second CheckAndConsume: %v", err)
	}
	if !denied || denial.Scope != BudgetScopeUser {
		t.Fatalf("expected user budget denial, got denied=%v denial=%+v", denied, denial)
	}
	if denial.RetryAfter != 30*time.Minute {
		t.Fatalf("expected 30m retry, got %s", denial.RetryAfter)
	}
}

func TestComposedToolApprovedVersionsAreImmutable(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewComposedToolRepository(db.DB)
	record, err := repo.CreateDraft(ctx, store.ComposedTool{
		GuildID:   "guild-1",
		ToolID:    "guild-1:member_welcome",
		Name:      "member_welcome",
		Status:    "pending_approval",
		CreatedBy: "moderator-1",
	}, store.ComposedToolVersion{
		SpecJSON:           `{"schema_version":1}`,
		ValidationJSON:     `{"valid":true}`,
		ToolDefinitionJSON: `{"type":"function"}`,
		CreatedBy:          "moderator-1",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if _, err := repo.ApproveVersion(ctx, "guild-1", "member_welcome", 1, "admin-1"); err != nil {
		t.Fatalf("ApproveVersion: %v", err)
	}
	if err := repo.UpdateDraftVersion(ctx, record.Version.ID, `{"mutated":true}`, `{}`, `{}`); err == nil {
		t.Fatal("expected approved version mutation to fail")
	}

	next, err := repo.AddDraftVersion(ctx, record.Tool.ID, store.ComposedToolVersion{
		SpecJSON:           `{"schema_version":1,"name":"member_welcome"}`,
		ValidationJSON:     `{"valid":true}`,
		ToolDefinitionJSON: `{"type":"function"}`,
		CreatedBy:          "moderator-1",
	})
	if err != nil {
		t.Fatalf("AddDraftVersion: %v", err)
	}
	if next.VersionNumber != 2 {
		t.Fatalf("expected version 2, got %d", next.VersionNumber)
	}
	tool, ok, err := repo.GetByName(ctx, "guild-1", "member_welcome")
	if err != nil || !ok {
		t.Fatalf("GetByName: ok=%t err=%v", ok, err)
	}
	if tool.Status != "enabled" {
		t.Fatalf("new draft version should not disable current approved tool, got %s", tool.Status)
	}
}
