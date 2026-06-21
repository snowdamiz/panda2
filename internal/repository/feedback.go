package repository

import (
	"context"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

type FeedbackRepository struct {
	db *gorm.DB
}

type FeedbackSummaryRow struct {
	Rating string
	Count  int64
}

func NewFeedbackRepository(db *gorm.DB) *FeedbackRepository {
	return &FeedbackRepository{db: db}
}

func (r *FeedbackRepository) CreateTarget(ctx context.Context, target store.FeedbackTarget) (store.FeedbackTarget, error) {
	target.GuildID = strings.TrimSpace(target.GuildID)
	target.ChannelID = strings.TrimSpace(target.ChannelID)
	target.UserID = strings.TrimSpace(target.UserID)
	target.Command = firstNonEmpty(strings.TrimSpace(target.Command), "assistant")
	target.Model = strings.TrimSpace(target.Model)
	target.Metadata = firstNonEmpty(target.Metadata, "{}")
	if target.CreatedAt.IsZero() {
		target.CreatedAt = time.Now().UTC()
	}
	err := r.db.WithContext(ctx).Create(&target).Error
	return target, err
}

func (r *FeedbackRepository) Target(ctx context.Context, id uint) (store.FeedbackTarget, bool, error) {
	var target store.FeedbackTarget
	err := r.db.WithContext(ctx).First(&target, id).Error
	if err == nil {
		return target, true, nil
	}
	if err == gorm.ErrRecordNotFound {
		return store.FeedbackTarget{}, false, nil
	}
	return store.FeedbackTarget{}, false, err
}

func (r *FeedbackRepository) Record(ctx context.Context, event store.FeedbackEvent) (store.FeedbackEvent, error) {
	now := time.Now().UTC()
	event.Rating = strings.ToLower(strings.TrimSpace(event.Rating))
	event.Reason = strings.TrimSpace(event.Reason)
	event.CreatedAt = firstTime(event.CreatedAt, now)
	event.UpdatedAt = firstTime(event.UpdatedAt, now)
	err := r.db.WithContext(ctx).Exec(`
		INSERT INTO feedback_events(target_id, guild_id, user_id, rating, reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(target_id, user_id) DO UPDATE SET
			rating = excluded.rating,
			reason = excluded.reason,
			updated_at = excluded.updated_at
	`, event.TargetID, event.GuildID, event.UserID, event.Rating, event.Reason, event.CreatedAt, event.UpdatedAt).Error
	return event, err
}

func (r *FeedbackRepository) Summary(ctx context.Context, guildID string, since time.Time) ([]FeedbackSummaryRow, error) {
	query := r.db.WithContext(ctx).
		Model(&store.FeedbackEvent{}).
		Select("rating, count(*) as count").
		Where("guild_id = ?", guildID)
	if !since.IsZero() {
		query = query.Where("created_at >= ?", since.UTC())
	}
	var rows []FeedbackSummaryRow
	err := query.Group("rating").Order("count DESC, rating ASC").Scan(&rows).Error
	return rows, err
}
