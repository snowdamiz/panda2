package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

func TestCleanupExpiresConversationsAndAttachments(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "panda.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	conversations := repository.NewConversationRepository(db.DB)
	attachments := repository.NewAttachmentRepository(db.DB)
	composedTools := repository.NewComposedToolRepository(db.DB)
	service := NewService(conversations, attachments, db).WithComposedTools(composedTools)

	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	conversation, err := conversations.GetOrCreateActive(ctx, repository.ConversationKey{
		GuildID:     "guild-1",
		ChannelID:   "channel-1",
		OwnerUserID: "user-1",
	})
	if err != nil {
		t.Fatalf("GetOrCreateActive: %v", err)
	}
	expiredAt := now.Add(-time.Minute)
	if err := db.DB.Model(&store.Conversation{}).Where("id = ?", conversation.ID).Updates(map[string]any{"expires_at": expiredAt}).Error; err != nil {
		t.Fatalf("expire fixture conversation: %v", err)
	}

	tempPath := filepath.Join(dir, "attachment.txt")
	if err := os.WriteFile(tempPath, []byte("temporary"), 0o600); err != nil {
		t.Fatalf("write temp attachment: %v", err)
	}
	cleanupAfter := now.Add(-time.Minute)
	attachment, err := attachments.Record(ctx, store.Attachment{
		GuildID:      "guild-1",
		ChannelID:    "channel-1",
		MessageID:    "message-1",
		Filename:     "attachment.txt",
		TempPath:     tempPath,
		CleanupAfter: &cleanupAfter,
	})
	if err != nil {
		t.Fatalf("record attachment: %v", err)
	}
	tool := store.ComposedTool{
		GuildID:    "guild-1",
		ToolID:     "guild-1:cleanup_tool",
		Name:       "cleanup_tool",
		Status:     "enabled",
		Visibility: "guild",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := db.DB.Create(&tool).Error; err != nil {
		t.Fatalf("create composed tool: %v", err)
	}
	version := store.ComposedToolVersion{
		ComposedToolID:     tool.ID,
		VersionNumber:      1,
		SpecJSON:           "{}",
		ValidationJSON:     "{}",
		ToolDefinitionJSON: "{}",
		CreatedAt:          now,
	}
	if err := db.DB.Create(&version).Error; err != nil {
		t.Fatalf("create composed version: %v", err)
	}
	oldRun := store.ComposedToolRun{
		ComposedToolID: tool.ID,
		VersionID:      version.ID,
		GuildID:        "guild-1",
		InvocationType: "scheduled",
		Status:         "succeeded",
		CreatedAt:      now.Add(-31 * 24 * time.Hour),
		UpdatedAt:      now.Add(-31 * 24 * time.Hour),
	}
	freshRun := oldRun
	freshRun.CreatedAt = now.Add(-time.Hour)
	freshRun.UpdatedAt = freshRun.CreatedAt
	if err := db.DB.Create(&oldRun).Error; err != nil {
		t.Fatalf("create old composed run: %v", err)
	}
	if err := db.DB.Create(&freshRun).Error; err != nil {
		t.Fatalf("create fresh composed run: %v", err)
	}
	if err := db.DB.Create(&store.ComposedToolDedupe{
		ComposedToolID:        tool.ID,
		InvocationFingerprint: "old",
		ExpiresAt:             now.Add(-time.Minute),
		CreatedAt:             now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("create old dedupe: %v", err)
	}

	stats, err := service.Cleanup(ctx, now)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if stats.ExpiredConversations != 1 || stats.CleanedAttachments != 1 || stats.DeletedComposedRuns != 1 || stats.DeletedComposedDedupe != 1 {
		t.Fatalf("unexpected cleanup stats: %+v", stats)
	}
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("expected temp file removed, stat err=%v", err)
	}

	var savedConversation store.Conversation
	if err := db.DB.First(&savedConversation, conversation.ID).Error; err != nil {
		t.Fatalf("lookup conversation: %v", err)
	}
	if savedConversation.Status != "expired" {
		t.Fatalf("expected expired conversation, got %+v", savedConversation)
	}

	var savedAttachment store.Attachment
	if err := db.DB.First(&savedAttachment, attachment.ID).Error; err != nil {
		t.Fatalf("lookup attachment: %v", err)
	}
	if savedAttachment.CleanupDoneAt == nil || savedAttachment.TempPath != "" {
		t.Fatalf("expected cleaned attachment, got %+v", savedAttachment)
	}
}
