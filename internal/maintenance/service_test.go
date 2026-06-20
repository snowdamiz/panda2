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
	service := NewService(conversations, attachments, db)

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

	stats, err := service.Cleanup(ctx, now)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if stats.ExpiredConversations != 1 || stats.CleanedAttachments != 1 {
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
