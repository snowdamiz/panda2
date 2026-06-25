package repository

import (
	"context"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"github.com/sn0w/panda2/internal/textutil"
	"gorm.io/gorm"
)

const conversationPreviewLimit = 600

type ConversationRepository struct {
	db *gorm.DB
}

type ConversationKey struct {
	GuildID     string
	ChannelID   string
	ThreadID    string
	OwnerUserID string
	Title       string
}

func NewConversationRepository(db *gorm.DB) *ConversationRepository {
	return &ConversationRepository{db: db}
}

func (r *ConversationRepository) GetOrCreateActive(ctx context.Context, key ConversationKey) (store.Conversation, error) {
	now := time.Now().UTC()
	var conversation store.Conversation

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Where("guild_id = ? AND owner_user_id = ? AND status = ?", key.GuildID, key.OwnerUserID, "active")
		if key.ThreadID != "" {
			query = query.Where("thread_id = ?", key.ThreadID)
		} else {
			query = query.Where("channel_id = ? AND thread_id = ''", key.ChannelID)
		}

		result := query.Order("updated_at DESC").Limit(1).Find(&conversation)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			return nil
		}

		expiresAt := now.Add(30 * 24 * time.Hour)
		conversation = store.Conversation{
			GuildID:       key.GuildID,
			ChannelID:     key.ChannelID,
			ThreadID:      key.ThreadID,
			OwnerUserID:   key.OwnerUserID,
			Title:         firstNonEmpty(key.Title, "Panda chat"),
			Status:        "active",
			RetentionDays: 30,
			LastMessageAt: now,
			ExpiresAt:     &expiresAt,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		return tx.Create(&conversation).Error
	})
	return conversation, err
}

func (r *ConversationRepository) AppendMessage(ctx context.Context, message store.AssistantMessage) error {
	now := time.Now().UTC()
	if message.CreatedAt.IsZero() {
		message.CreatedAt = now
	}
	message.ContentPreview = preview(message.ContentPreview)
	if message.ContentHash == "" && message.ContentPreview != "" {
		message.ContentHash = contentHash(message.ContentPreview)
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		expiresAt := now.Add(30 * 24 * time.Hour)
		return tx.Model(&store.Conversation{}).
			Where("id = ?", message.ConversationID).
			Updates(map[string]any{
				"last_message_at": now,
				"expires_at":      expiresAt,
				"updated_at":      now,
			}).Error
	})
}

func (r *ConversationRepository) RecentMessages(ctx context.Context, conversationID uint, limit int) ([]store.AssistantMessage, error) {
	if limit <= 0 || limit > 20 {
		limit = 10
	}

	var newest []store.AssistantMessage
	if err := r.db.WithContext(ctx).
		Where("conversation_id = ?", conversationID).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Find(&newest).Error; err != nil {
		return nil, err
	}

	for i, j := 0, len(newest)-1; i < j; i, j = i+1, j-1 {
		newest[i], newest[j] = newest[j], newest[i]
	}
	return newest, nil
}

func (r *ConversationRepository) Expired(ctx context.Context, now time.Time, limit int) ([]store.Conversation, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var conversations []store.Conversation
	err := r.db.WithContext(ctx).
		Where("expires_at IS NOT NULL AND expires_at <= ? AND status = ?", now, "active").
		Order("expires_at ASC").
		Limit(limit).
		Find(&conversations).Error
	return conversations, err
}

func (r *ConversationRepository) CloseExpired(ctx context.Context, now time.Time) (int64, error) {
	result := r.db.WithContext(ctx).
		Model(&store.Conversation{}).
		Where("expires_at IS NOT NULL AND expires_at <= ? AND status = ?", now, "active").
		Updates(map[string]any{"status": "expired", "updated_at": now.UTC()})
	return result.RowsAffected, result.Error
}

func preview(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= conversationPreviewLimit {
		return value
	}
	return textutil.Truncate(value, conversationPreviewLimit, "...")
}
