package repository

import (
	"context"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

const defaultDiscordEventRetention = 7 * 24 * time.Hour

type DiscordEventRepository struct {
	db *gorm.DB
}

type DiscordEventFilter struct {
	GuildID   string
	ChannelID string
	EventType string
	Limit     int
}

type DiscordActivityCount struct {
	EventType string
	Count     int
}

type DiscordActivityFilter struct {
	GuildID          string
	ChannelID        string
	Since            time.Time
	ExcludeUserID    string
	ExcludeAuthorBot bool
	EventTypes       []string
}

func NewDiscordEventRepository(db *gorm.DB) *DiscordEventRepository {
	return &DiscordEventRepository{db: db}
}

func (r *DiscordEventRepository) Record(ctx context.Context, event store.DiscordEvent) (store.DiscordEvent, error) {
	now := time.Now().UTC()
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	if event.ExpiresAt == nil {
		expiresAt := event.CreatedAt.Add(defaultDiscordEventRetention)
		event.ExpiresAt = &expiresAt
	}
	err := r.db.WithContext(ctx).Create(&event).Error
	return event, err
}

func (r *DiscordEventRepository) Recent(ctx context.Context, filter DiscordEventFilter) ([]store.DiscordEvent, error) {
	limit := clampLimit(filter.Limit, 25, 100)
	query := r.db.WithContext(ctx).Where("guild_id = ?", filter.GuildID)
	if filter.ChannelID != "" {
		query = query.Where("channel_id = ?", filter.ChannelID)
	}
	if filter.EventType != "" {
		query = query.Where("event_type = ?", filter.EventType)
	}

	var events []store.DiscordEvent
	err := query.Order("created_at DESC, id DESC").Limit(limit).Find(&events).Error
	return events, err
}

func (r *DiscordEventRepository) ActivityCounts(ctx context.Context, guildID, channelID string, since time.Time, limit int) ([]DiscordActivityCount, error) {
	query := r.db.WithContext(ctx).
		Model(&store.DiscordEvent{}).
		Select("event_type, count(*) as count").
		Where("guild_id = ?", guildID)
	if channelID != "" {
		query = query.Where("channel_id = ?", channelID)
	}
	if !since.IsZero() {
		query = query.Where("created_at >= ?", since)
	}

	var rows []DiscordActivityCount
	err := query.Group("event_type").
		Order("count DESC, event_type ASC").
		Limit(clampLimit(limit, 10, 50)).
		Scan(&rows).Error
	return rows, err
}

func (r *DiscordEventRepository) CountActivity(ctx context.Context, filter DiscordActivityFilter) (int64, error) {
	query := r.db.WithContext(ctx).Model(&store.DiscordEvent{}).
		Where("guild_id = ?", filter.GuildID)
	if filter.ChannelID != "" {
		query = query.Where("channel_id = ?", filter.ChannelID)
	}
	if !filter.Since.IsZero() {
		query = query.Where("created_at > ?", filter.Since.UTC())
	}
	if filter.ExcludeUserID != "" {
		query = query.Where("user_id <> ?", filter.ExcludeUserID)
	}
	if len(filter.EventTypes) > 0 {
		query = query.Where("event_type IN ?", filter.EventTypes)
	}
	if filter.ExcludeAuthorBot {
		query = query.Where("(metadata NOT LIKE ? OR metadata = '')", `%"author_bot":"true"%`)
	}
	var count int64
	err := query.Count(&count).Error
	return count, err
}

func (r *DiscordEventRepository) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("expires_at IS NOT NULL AND expires_at <= ?", now.UTC()).
		Delete(&store.DiscordEvent{})
	return result.RowsAffected, result.Error
}

func clampLimit(value, fallback, maxValue int) int {
	if value <= 0 {
		return fallback
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
