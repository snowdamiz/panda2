package repository

import (
	"context"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

type AuditRepository struct {
	db *gorm.DB
}

func NewAuditRepository(db *gorm.DB) *AuditRepository {
	return &AuditRepository{db: db}
}

func (r *AuditRepository) Record(ctx context.Context, event store.AuditEvent) error {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	return r.db.WithContext(ctx).Create(&event).Error
}

func (r *AuditRepository) Recent(ctx context.Context, guildID string, limit int) ([]store.AuditEvent, error) {
	if limit <= 0 || limit > 25 {
		limit = 10
	}

	var events []store.AuditEvent
	err := r.db.WithContext(ctx).
		Where("guild_id = ?", guildID).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Find(&events).Error
	return events, err
}
