package repository

import (
	"context"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

type AttachmentRepository struct {
	db *gorm.DB
}

func NewAttachmentRepository(db *gorm.DB) *AttachmentRepository {
	return &AttachmentRepository{db: db}
}

func (r *AttachmentRepository) Record(ctx context.Context, attachment store.Attachment) (store.Attachment, error) {
	now := time.Now().UTC()
	attachment.CreatedAt = firstTime(attachment.CreatedAt, now)
	attachment.UpdatedAt = firstTime(attachment.UpdatedAt, now)
	return attachment, r.db.WithContext(ctx).Create(&attachment).Error
}

func (r *AttachmentRepository) Get(ctx context.Context, guildID string, id uint) (store.Attachment, error) {
	var attachment store.Attachment
	err := r.db.WithContext(ctx).Where("guild_id = ? AND id = ?", guildID, id).First(&attachment).Error
	if err == nil {
		return attachment, nil
	}
	if err == gorm.ErrRecordNotFound {
		return store.Attachment{}, ErrNotFound
	}
	return store.Attachment{}, err
}

func (r *AttachmentRepository) DueForCleanup(ctx context.Context, now time.Time, limit int) ([]store.Attachment, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var attachments []store.Attachment
	err := r.db.WithContext(ctx).
		Where("cleanup_after IS NOT NULL AND cleanup_after <= ? AND cleanup_done_at IS NULL", now.UTC()).
		Order("cleanup_after ASC, id ASC").
		Limit(limit).
		Find(&attachments).Error
	return attachments, err
}

func (r *AttachmentRepository) MarkCleanupDone(ctx context.Context, id uint, now time.Time) error {
	return r.db.WithContext(ctx).
		Model(&store.Attachment{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"cleanup_done_at": now.UTC(),
			"temp_path":       "",
			"updated_at":      now.UTC(),
		}).Error
}
