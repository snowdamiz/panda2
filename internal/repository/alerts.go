package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

type AlertRuleRepository struct {
	db *gorm.DB
}

func NewAlertRuleRepository(db *gorm.DB) *AlertRuleRepository {
	return &AlertRuleRepository{db: db}
}

func (r *AlertRuleRepository) Enable(ctx context.Context, rule store.AlertRule) (store.AlertRule, error) {
	now := time.Now().UTC()
	rule.GuildID = strings.TrimSpace(rule.GuildID)
	rule.Pack = strings.ToLower(strings.TrimSpace(rule.Pack))
	rule.ChannelID = strings.TrimSpace(rule.ChannelID)
	if rule.CooldownSeconds <= 0 {
		rule.CooldownSeconds = 300
	}
	rule.Enabled = true
	rule.CreatedAt = firstTime(rule.CreatedAt, now)
	rule.UpdatedAt = firstTime(rule.UpdatedAt, now)

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing store.AlertRule
		err := tx.Where("guild_id = ? AND pack = ?", rule.GuildID, rule.Pack).First(&existing).Error
		if err == nil {
			updates := map[string]any{
				"channel_id":       rule.ChannelID,
				"enabled":          true,
				"cooldown_seconds": rule.CooldownSeconds,
				"pending_count":    0,
				"created_by":       firstNonEmpty(rule.CreatedBy, existing.CreatedBy),
				"updated_at":       now,
			}
			if err := tx.Model(&existing).Updates(updates).Error; err != nil {
				return err
			}
			rule = existing
			return tx.First(&rule, existing.ID).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(&rule).Error
	})
	return rule, err
}

func (r *AlertRuleRepository) Disable(ctx context.Context, guildID, pack string) error {
	result := r.db.WithContext(ctx).Model(&store.AlertRule{}).
		Where("guild_id = ? AND pack = ?", guildID, strings.ToLower(strings.TrimSpace(pack))).
		Updates(map[string]any{
			"enabled":    false,
			"updated_at": time.Now().UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *AlertRuleRepository) List(ctx context.Context, guildID string) ([]store.AlertRule, error) {
	var rules []store.AlertRule
	err := r.db.WithContext(ctx).
		Where("guild_id = ?", guildID).
		Order("pack ASC").
		Find(&rules).Error
	return rules, err
}

func (r *AlertRuleRepository) Enabled(ctx context.Context, guildID string) ([]store.AlertRule, error) {
	var rules []store.AlertRule
	err := r.db.WithContext(ctx).
		Where("guild_id = ? AND enabled = ?", guildID, true).
		Order("pack ASC").
		Find(&rules).Error
	return rules, err
}

func (r *AlertRuleRepository) IncrementPending(ctx context.Context, id uint, now time.Time) error {
	return r.db.WithContext(ctx).Model(&store.AlertRule{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"pending_count": gorm.Expr("pending_count + 1"),
			"updated_at":    now.UTC(),
		}).Error
}

func (r *AlertRuleRepository) MarkSent(ctx context.Context, id uint, now time.Time) error {
	now = now.UTC()
	return r.db.WithContext(ctx).Model(&store.AlertRule{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"pending_count": 0,
			"last_sent_at":  now,
			"updated_at":    now,
		}).Error
}
