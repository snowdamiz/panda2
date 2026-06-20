package repository

import (
	"context"
	"errors"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

type AccessRepository struct {
	db *gorm.DB
}

func NewAccessRepository(db *gorm.DB) *AccessRepository {
	return &AccessRepository{db: db}
}

func (r *AccessRepository) AddRolePermission(ctx context.Context, guildID, roleID, permission string) (store.GuildRole, error) {
	now := time.Now().UTC()
	var role store.GuildRole
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("guild_id = ? AND role_id = ? AND permission = ?", guildID, roleID, permission).First(&role).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		role = store.GuildRole{
			GuildID:    guildID,
			RoleID:     roleID,
			Permission: permission,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		return tx.Create(&role).Error
	})
	return role, err
}

func (r *AccessRepository) RemoveRolePermission(ctx context.Context, guildID, roleID, permission string) error {
	result := r.db.WithContext(ctx).
		Where("guild_id = ? AND role_id = ? AND permission = ?", guildID, roleID, permission).
		Delete(&store.GuildRole{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *AccessRepository) ListRolePermissions(ctx context.Context, guildID string) ([]store.GuildRole, error) {
	var roles []store.GuildRole
	err := r.db.WithContext(ctx).
		Where("guild_id = ?", guildID).
		Order("permission ASC, role_id ASC").
		Find(&roles).Error
	return roles, err
}

func (r *AccessRepository) HasPermissionMappings(ctx context.Context, guildID, permission string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&store.GuildRole{}).
		Where("guild_id = ? AND permission = ?", guildID, permission).
		Count(&count).Error
	return count > 0, err
}

func (r *AccessRepository) AnyRoleHasPermission(ctx context.Context, guildID string, roleIDs []string, permission string) (bool, error) {
	if len(roleIDs) == 0 {
		return false, nil
	}
	var count int64
	err := r.db.WithContext(ctx).
		Model(&store.GuildRole{}).
		Where("guild_id = ? AND permission = ? AND role_id IN ?", guildID, permission, roleIDs).
		Count(&count).Error
	return count > 0, err
}

func (r *AccessRepository) SetChannelRule(ctx context.Context, guildID, channelID, rule string) (store.GuildChannelRule, error) {
	now := time.Now().UTC()
	var channelRule store.GuildChannelRule
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("guild_id = ? AND channel_id = ?", guildID, channelID).First(&channelRule).Error
		if err == nil {
			if err := tx.Model(&channelRule).Updates(map[string]any{"rule": rule, "updated_at": now}).Error; err != nil {
				return err
			}
			return tx.First(&channelRule, channelRule.ID).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		channelRule = store.GuildChannelRule{
			GuildID:   guildID,
			ChannelID: channelID,
			Rule:      rule,
			CreatedAt: now,
			UpdatedAt: now,
		}
		return tx.Create(&channelRule).Error
	})
	return channelRule, err
}

func (r *AccessRepository) RemoveChannelRule(ctx context.Context, guildID, channelID string) error {
	result := r.db.WithContext(ctx).
		Where("guild_id = ? AND channel_id = ?", guildID, channelID).
		Delete(&store.GuildChannelRule{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *AccessRepository) ListChannelRules(ctx context.Context, guildID string) ([]store.GuildChannelRule, error) {
	var rules []store.GuildChannelRule
	err := r.db.WithContext(ctx).
		Where("guild_id = ?", guildID).
		Order("rule ASC, channel_id ASC").
		Find(&rules).Error
	return rules, err
}

func (r *AccessRepository) ChannelRule(ctx context.Context, guildID, channelID string) (store.GuildChannelRule, bool, error) {
	var rule store.GuildChannelRule
	err := r.db.WithContext(ctx).Where("guild_id = ? AND channel_id = ?", guildID, channelID).First(&rule).Error
	if err == nil {
		return rule, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.GuildChannelRule{}, false, nil
	}
	return store.GuildChannelRule{}, false, err
}

func (r *AccessRepository) HasChannelAllowRules(ctx context.Context, guildID string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&store.GuildChannelRule{}).
		Where("guild_id = ? AND rule = ?", guildID, "allow").
		Count(&count).Error
	return count > 0, err
}
