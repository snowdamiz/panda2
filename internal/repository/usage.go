package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

type UsageRepository struct {
	db *gorm.DB
}

type UsageSummary struct {
	TotalRequests    int64
	Successful       int64
	Failed           int64
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

type UsageBreakdownRow struct {
	Label         string
	TotalRequests int64
	Successful    int64
	Failed        int64
	TotalTokens   int64
}

func NewUsageRepository(db *gorm.DB) *UsageRepository {
	return &UsageRepository{db: db}
}

func (r *UsageRepository) Record(ctx context.Context, event store.UsageEvent) error {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	return r.db.WithContext(ctx).Create(&event).Error
}

func (r *UsageRepository) CountByUser(ctx context.Context, userID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&store.UsageEvent{}).Where("user_id = ?", userID).Count(&count).Error
	return count, err
}

func (r *UsageRepository) SummaryByGuild(ctx context.Context, guildID string, since time.Time) (UsageSummary, error) {
	var summary UsageSummary
	query := r.db.WithContext(ctx).Model(&store.UsageEvent{}).Where("guild_id = ?", guildID)
	if !since.IsZero() {
		query = query.Where("created_at >= ?", since)
	}
	err := query.Select(`
		COUNT(*) AS total_requests,
		COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END), 0) AS successful,
		COALESCE(SUM(CASE WHEN success THEN 0 ELSE 1 END), 0) AS failed,
		COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
		COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
		COALESCE(SUM(total_tokens), 0) AS total_tokens
	`).Scan(&summary).Error
	return summary, err
}

func (r *UsageRepository) BreakdownByGuild(ctx context.Context, guildID string, since time.Time, dimension string, limit int) ([]UsageBreakdownRow, error) {
	column, ok := usageBreakdownColumns[strings.ToLower(strings.TrimSpace(dimension))]
	if !ok {
		return nil, fmt.Errorf("unsupported usage breakdown %q", dimension)
	}
	if limit <= 0 || limit > 25 {
		limit = 10
	}

	query := r.db.WithContext(ctx).Model(&store.UsageEvent{}).Where("guild_id = ?", guildID)
	if !since.IsZero() {
		query = query.Where("created_at >= ?", since)
	}
	var rows []UsageBreakdownRow
	err := query.Select(fmt.Sprintf(`
		COALESCE(NULLIF(%s, ''), '(none)') AS label,
		COUNT(*) AS total_requests,
		COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END), 0) AS successful,
		COALESCE(SUM(CASE WHEN success THEN 0 ELSE 1 END), 0) AS failed,
		COALESCE(SUM(total_tokens), 0) AS total_tokens
	`, column)).
		Group("label").
		Order("total_requests DESC, total_tokens DESC, label ASC").
		Limit(limit).
		Scan(&rows).Error
	return rows, err
}

var usageBreakdownColumns = map[string]string{
	"user":    "user_id",
	"channel": "channel_id",
	"command": "command",
	"model":   "model",
}
