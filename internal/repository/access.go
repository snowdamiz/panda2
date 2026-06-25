package repository

import (
	"context"
	"errors"
	"sort"
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

func (r *AccessRepository) SetRolePermission(ctx context.Context, guildID, roleID, permission string) (store.GuildRole, error) {
	now := time.Now().UTC()
	var role store.GuildRole
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("guild_id = ? AND permission = ? AND role_id <> ?", guildID, permission, roleID).Delete(&store.GuildRole{}).Error; err != nil {
			return err
		}
		err := tx.Where("guild_id = ? AND role_id = ? AND permission = ?", guildID, roleID, permission).First(&role).Error
		if err == nil {
			if err := tx.Model(&role).Update("updated_at", now).Error; err != nil {
				return err
			}
			return tx.First(&role, role.ID).Error
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

func (r *AccessRepository) RemoveRolePermissionMappings(ctx context.Context, guildID, permission string) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("guild_id = ? AND permission = ?", guildID, permission).
		Delete(&store.GuildRole{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
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

func (r *AccessRepository) AddUserPermission(ctx context.Context, guildID, userID, permission string) (store.GuildUserPermission, error) {
	now := time.Now().UTC()
	var userPermission store.GuildUserPermission
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("guild_id = ? AND user_id = ? AND permission = ?", guildID, userID, permission).First(&userPermission).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		userPermission = store.GuildUserPermission{
			GuildID:    guildID,
			UserID:     userID,
			Permission: permission,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		return tx.Create(&userPermission).Error
	})
	return userPermission, err
}

func (r *AccessRepository) RemoveUserPermission(ctx context.Context, guildID, userID, permission string) error {
	result := r.db.WithContext(ctx).
		Where("guild_id = ? AND user_id = ? AND permission = ?", guildID, userID, permission).
		Delete(&store.GuildUserPermission{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *AccessRepository) RemoveUserPermissionMappings(ctx context.Context, guildID, permission string) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("guild_id = ? AND permission = ?", guildID, permission).
		Delete(&store.GuildUserPermission{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func (r *AccessRepository) ListUserPermissions(ctx context.Context, guildID string) ([]store.GuildUserPermission, error) {
	var users []store.GuildUserPermission
	err := r.db.WithContext(ctx).
		Where("guild_id = ?", guildID).
		Order("permission ASC, user_id ASC").
		Find(&users).Error
	return users, err
}

func (r *AccessRepository) HasUserPermissionMappings(ctx context.Context, guildID, permission string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&store.GuildUserPermission{}).
		Where("guild_id = ? AND permission = ?", guildID, permission).
		Count(&count).Error
	return count > 0, err
}

func (r *AccessRepository) UserHasPermission(ctx context.Context, guildID, userID, permission string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	var count int64
	err := r.db.WithContext(ctx).
		Model(&store.GuildUserPermission{}).
		Where("guild_id = ? AND user_id = ? AND permission = ?", guildID, userID, permission).
		Count(&count).Error
	return count > 0, err
}

func (r *AccessRepository) AddToolRole(ctx context.Context, guildID, toolName, roleID string) (store.GuildToolRole, error) {
	return r.setToolRoleRule(ctx, guildID, toolName, roleID, "allow")
}

func (r *AccessRepository) DenyToolRole(ctx context.Context, guildID, toolName, roleID string) (store.GuildToolRole, error) {
	return r.setToolRoleRule(ctx, guildID, toolName, roleID, "deny")
}

func (r *AccessRepository) setToolRoleRule(ctx context.Context, guildID, toolName, roleID, rule string) (store.GuildToolRole, error) {
	now := time.Now().UTC()
	var toolRole store.GuildToolRole
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("guild_id = ? AND tool_name = ? AND role_id = ?", guildID, toolName, roleID).First(&toolRole).Error
		if err == nil {
			if err := tx.Model(&toolRole).Updates(map[string]any{"rule": rule, "updated_at": now}).Error; err != nil {
				return err
			}
			toolRole.Rule = rule
			toolRole.UpdatedAt = now
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		toolRole = store.GuildToolRole{
			GuildID:   guildID,
			ToolName:  toolName,
			RoleID:    roleID,
			Rule:      rule,
			CreatedAt: now,
			UpdatedAt: now,
		}
		return tx.Create(&toolRole).Error
	})
	return toolRole, err
}

func (r *AccessRepository) RemoveToolRole(ctx context.Context, guildID, toolName, roleID string) error {
	result := r.db.WithContext(ctx).
		Where("guild_id = ? AND tool_name = ? AND role_id = ?", guildID, toolName, roleID).
		Delete(&store.GuildToolRole{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *AccessRepository) RemoveToolRolesByTool(ctx context.Context, guildID, toolName string) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("guild_id = ? AND tool_name = ?", guildID, toolName).
		Delete(&store.GuildToolRole{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func (r *AccessRepository) ListToolRoles(ctx context.Context, guildID string) ([]store.GuildToolRole, error) {
	var roles []store.GuildToolRole
	err := r.db.WithContext(ctx).
		Where("guild_id = ?", guildID).
		Order("tool_name ASC, rule ASC, role_id ASC").
		Find(&roles).Error
	return roles, err
}

func (r *AccessRepository) AddToolUser(ctx context.Context, guildID, toolName, userID string) (store.GuildToolUser, error) {
	return r.setToolUserRule(ctx, guildID, toolName, userID, "allow")
}

func (r *AccessRepository) DenyToolUser(ctx context.Context, guildID, toolName, userID string) (store.GuildToolUser, error) {
	return r.setToolUserRule(ctx, guildID, toolName, userID, "deny")
}

func (r *AccessRepository) setToolUserRule(ctx context.Context, guildID, toolName, userID, rule string) (store.GuildToolUser, error) {
	now := time.Now().UTC()
	var toolUser store.GuildToolUser
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("guild_id = ? AND tool_name = ? AND user_id = ?", guildID, toolName, userID).First(&toolUser).Error
		if err == nil {
			if err := tx.Model(&toolUser).Updates(map[string]any{"rule": rule, "updated_at": now}).Error; err != nil {
				return err
			}
			toolUser.Rule = rule
			toolUser.UpdatedAt = now
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		toolUser = store.GuildToolUser{
			GuildID:   guildID,
			ToolName:  toolName,
			UserID:    userID,
			Rule:      rule,
			CreatedAt: now,
			UpdatedAt: now,
		}
		return tx.Create(&toolUser).Error
	})
	return toolUser, err
}

func (r *AccessRepository) RemoveToolUser(ctx context.Context, guildID, toolName, userID string) error {
	result := r.db.WithContext(ctx).
		Where("guild_id = ? AND tool_name = ? AND user_id = ?", guildID, toolName, userID).
		Delete(&store.GuildToolUser{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *AccessRepository) RemoveToolUsersByTool(ctx context.Context, guildID, toolName string) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("guild_id = ? AND tool_name = ?", guildID, toolName).
		Delete(&store.GuildToolUser{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func (r *AccessRepository) ListToolUsers(ctx context.Context, guildID string) ([]store.GuildToolUser, error) {
	var users []store.GuildToolUser
	err := r.db.WithContext(ctx).
		Where("guild_id = ?", guildID).
		Order("tool_name ASC, rule ASC, user_id ASC").
		Find(&users).Error
	return users, err
}

func (r *AccessRepository) RestrictedToolNames(ctx context.Context, guildID string) ([]string, error) {
	var roleNames []string
	if err := r.db.WithContext(ctx).
		Model(&store.GuildToolRole{}).
		Where("guild_id = ? AND rule = ?", guildID, "allow").
		Distinct("tool_name").
		Order("tool_name ASC").
		Pluck("tool_name", &roleNames).Error; err != nil {
		return nil, err
	}
	var userNames []string
	if err := r.db.WithContext(ctx).
		Model(&store.GuildToolUser{}).
		Where("guild_id = ? AND rule = ?", guildID, "allow").
		Distinct("tool_name").
		Order("tool_name ASC").
		Pluck("tool_name", &userNames).Error; err != nil {
		return nil, err
	}
	return distinctSortedToolNames(roleNames, userNames), nil
}

func (r *AccessRepository) ToolNamesForRoles(ctx context.Context, guildID string, roleIDs []string) ([]string, error) {
	if len(roleIDs) == 0 {
		return nil, nil
	}
	var names []string
	err := r.db.WithContext(ctx).
		Model(&store.GuildToolRole{}).
		Where("guild_id = ? AND rule = ? AND role_id IN ?", guildID, "allow", roleIDs).
		Distinct("tool_name").
		Order("tool_name ASC").
		Pluck("tool_name", &names).Error
	return names, err
}

func (r *AccessRepository) DeniedToolNamesForRoles(ctx context.Context, guildID string, roleIDs []string) ([]string, error) {
	if len(roleIDs) == 0 {
		return nil, nil
	}
	var names []string
	err := r.db.WithContext(ctx).
		Model(&store.GuildToolRole{}).
		Where("guild_id = ? AND rule = ? AND role_id IN ?", guildID, "deny", roleIDs).
		Distinct("tool_name").
		Order("tool_name ASC").
		Pluck("tool_name", &names).Error
	return names, err
}

func (r *AccessRepository) ToolNamesForUser(ctx context.Context, guildID, userID string) ([]string, error) {
	if userID == "" {
		return nil, nil
	}
	var names []string
	err := r.db.WithContext(ctx).
		Model(&store.GuildToolUser{}).
		Where("guild_id = ? AND rule = ? AND user_id = ?", guildID, "allow", userID).
		Distinct("tool_name").
		Order("tool_name ASC").
		Pluck("tool_name", &names).Error
	return names, err
}

func (r *AccessRepository) DeniedToolNamesForUser(ctx context.Context, guildID, userID string) ([]string, error) {
	if userID == "" {
		return nil, nil
	}
	var names []string
	err := r.db.WithContext(ctx).
		Model(&store.GuildToolUser{}).
		Where("guild_id = ? AND rule = ? AND user_id = ?", guildID, "deny", userID).
		Distinct("tool_name").
		Order("tool_name ASC").
		Pluck("tool_name", &names).Error
	return names, err
}

func distinctSortedToolNames(groups ...[]string) []string {
	seen := map[string]struct{}{}
	for _, group := range groups {
		for _, name := range group {
			if name == "" {
				continue
			}
			seen[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
