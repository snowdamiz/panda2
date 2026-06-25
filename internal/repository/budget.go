package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

const (
	BudgetScopeGlobal  = "global"
	BudgetScopeGuild   = "guild"
	BudgetScopeUser    = "user"
	BudgetScopeChannel = "channel"
)

type BudgetRepository struct {
	db *gorm.DB
}

type BudgetCheckRequest struct {
	GuildID   string
	UserID    string
	ChannelID string
	Now       time.Time
}

type BudgetDenial struct {
	Scope      string
	SubjectID  string
	RetryAfter time.Duration
}

func NewBudgetRepository(db *gorm.DB) *BudgetRepository {
	return &BudgetRepository{db: db}
}

func (r *BudgetRepository) SetLimit(ctx context.Context, limit store.BudgetLimit) (store.BudgetLimit, error) {
	limit.Scope = normalizeBudgetScope(limit.Scope)
	limit.SubjectID = strings.TrimSpace(limit.SubjectID)
	if err := validateBudgetLimit(limit); err != nil {
		return store.BudgetLimit{}, err
	}

	now := time.Now().UTC()
	var saved store.BudgetLimit
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Where("guild_id = ? AND scope = ? AND subject_id = ?", limit.GuildID, limit.Scope, limit.SubjectID).
			Limit(1).
			Find(&saved)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			if err := tx.Model(&saved).Updates(map[string]any{
				"limit_count":    limit.Limit,
				"window_seconds": limit.WindowSeconds,
				"updated_at":     now,
			}).Error; err != nil {
				return err
			}
			return tx.First(&saved, saved.ID).Error
		}
		limit.CreatedAt = now
		limit.UpdatedAt = now
		if err := tx.Create(&limit).Error; err != nil {
			return err
		}
		saved = limit
		return nil
	})
	return saved, err
}

func (r *BudgetRepository) RemoveLimit(ctx context.Context, guildID, scope, subjectID string) error {
	result := r.db.WithContext(ctx).
		Where("guild_id = ? AND scope = ? AND subject_id = ?", guildID, normalizeBudgetScope(scope), strings.TrimSpace(subjectID)).
		Delete(&store.BudgetLimit{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *BudgetRepository) ListLimits(ctx context.Context, guildID string) ([]store.BudgetLimit, error) {
	var limits []store.BudgetLimit
	err := r.db.WithContext(ctx).
		Where("guild_id = ? OR scope = ?", guildID, BudgetScopeGlobal).
		Order("scope ASC, subject_id ASC").
		Find(&limits).Error
	return limits, err
}

func (r *BudgetRepository) CheckAndConsume(ctx context.Context, request BudgetCheckRequest) (BudgetDenial, bool, error) {
	now := request.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var denial BudgetDenial
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		limits, err := applicableBudgetLimits(tx, request)
		if err != nil || len(limits) == 0 {
			return err
		}

		for _, limit := range limits {
			windowStart, windowEnd := budgetWindow(now, time.Duration(limit.WindowSeconds)*time.Second)
			key := budgetBucketKey(limit)
			var bucket store.RateLimitBucket
			result := tx.Where("scope = ? AND bucket_key = ? AND window_start = ?", "budget:"+limit.Scope, key, windowStart).
				Limit(1).
				Find(&bucket)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 && bucket.Count >= limit.Limit {
				denial = BudgetDenial{Scope: limit.Scope, SubjectID: limit.SubjectID, RetryAfter: windowEnd.Sub(now)}
				return nil
			}
		}

		for _, limit := range limits {
			windowStart, windowEnd := budgetWindow(now, time.Duration(limit.WindowSeconds)*time.Second)
			key := budgetBucketKey(limit)
			var bucket store.RateLimitBucket
			result := tx.Where("scope = ? AND bucket_key = ? AND window_start = ?", "budget:"+limit.Scope, key, windowStart).
				Limit(1).
				Find(&bucket)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				bucket = store.RateLimitBucket{
					Scope:       "budget:" + limit.Scope,
					BucketKey:   key,
					Count:       1,
					Limit:       limit.Limit,
					WindowStart: windowStart,
					WindowEnd:   windowEnd,
					CreatedAt:   now,
					UpdatedAt:   now,
				}
				if err := tx.Create(&bucket).Error; err != nil {
					return err
				}
				continue
			}
			if err := tx.Model(&bucket).Updates(map[string]any{
				"count":       bucket.Count + 1,
				"limit_count": limit.Limit,
				"window_end":  windowEnd,
				"updated_at":  now,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return denial, denial.Scope != "", err
}

func applicableBudgetLimits(tx *gorm.DB, request BudgetCheckRequest) ([]store.BudgetLimit, error) {
	var limits []store.BudgetLimit
	err := tx.Where(`
		(scope = ? AND guild_id = '' AND subject_id = '')
		OR (scope = ? AND guild_id = ? AND subject_id = ?)
		OR (scope = ? AND guild_id = ? AND subject_id = ?)
		OR (scope = ? AND guild_id = ? AND subject_id = ?)
	`,
		BudgetScopeGlobal,
		BudgetScopeGuild, request.GuildID, request.GuildID,
		BudgetScopeUser, request.GuildID, request.UserID,
		BudgetScopeChannel, request.GuildID, request.ChannelID,
	).Find(&limits).Error
	return limits, err
}

func budgetWindow(now time.Time, window time.Duration) (time.Time, time.Time) {
	if window <= 0 {
		window = time.Hour
	}
	unix := now.Unix()
	seconds := int64(window.Seconds())
	start := time.Unix(unix-(unix%seconds), 0).UTC()
	return start, start.Add(window)
}

func budgetBucketKey(limit store.BudgetLimit) string {
	if limit.Scope == BudgetScopeGlobal {
		return "global"
	}
	return limit.GuildID + ":" + limit.SubjectID
}

func normalizeBudgetScope(scope string) string {
	return strings.ToLower(strings.TrimSpace(scope))
}

func validateBudgetLimit(limit store.BudgetLimit) error {
	switch limit.Scope {
	case BudgetScopeGlobal:
		if limit.GuildID != "" || limit.SubjectID != "" {
			return fmt.Errorf("global budget must not include guild or subject")
		}
	case BudgetScopeGuild:
		if limit.GuildID == "" || limit.SubjectID == "" {
			return fmt.Errorf("guild budget requires guild_id and subject_id")
		}
	case BudgetScopeUser, BudgetScopeChannel:
		if limit.GuildID == "" || limit.SubjectID == "" {
			return fmt.Errorf("%s budget requires guild_id and subject_id", limit.Scope)
		}
	default:
		return fmt.Errorf("unsupported budget scope %q", limit.Scope)
	}
	if limit.Limit <= 0 {
		return fmt.Errorf("budget limit must be greater than zero")
	}
	if limit.WindowSeconds <= 0 {
		return fmt.Errorf("budget window must be greater than zero")
	}
	return nil
}
