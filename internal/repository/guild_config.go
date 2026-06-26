package repository

import (
	"context"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

type GuildConfigRepository struct {
	db *gorm.DB
}

func NewGuildConfigRepository(db *gorm.DB) *GuildConfigRepository {
	return &GuildConfigRepository{db: db}
}

func (r *GuildConfigRepository) EnsureDefault(ctx context.Context, guildID string) (store.GuildConfig, error) {
	now := time.Now().UTC()
	config := store.GuildConfig{
		GuildID:           guildID,
		Temperature:       0.3,
		MaxResponseTokens: 900,
		ToolPolicy:        "admin_only",
		AssistantEnabled:  true,
		MemoryEnabled:     true,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		existing, ok, err := findGuildConfigByGuild(tx, guildID)
		if err != nil {
			return err
		}
		if ok {
			config = existing
			return nil
		}
		return tx.Create(&config).Error
	})
	return config, err
}

func (r *GuildConfigRepository) Get(ctx context.Context, guildID string) (store.GuildConfig, bool, error) {
	return findGuildConfigByGuild(r.db.WithContext(ctx), guildID)
}

func (r *GuildConfigRepository) UpdateBehaviorSettings(ctx context.Context, guildID string, values map[string]any) (store.GuildConfig, error) {
	values["updated_at"] = time.Now().UTC()
	return r.update(ctx, guildID, values)
}

func (r *GuildConfigRepository) UpdatePrompt(ctx context.Context, guildID, prompt string) (store.GuildConfig, error) {
	return r.update(ctx, guildID, map[string]any{
		"system_prompt_overlay": prompt,
		"updated_at":            time.Now().UTC(),
	})
}

func (r *GuildConfigRepository) UpdateSoul(ctx context.Context, guildID, soul string) (store.GuildConfig, error) {
	return r.update(ctx, guildID, map[string]any{
		"agent_soul": soul,
		"updated_at": time.Now().UTC(),
	})
}

func (r *GuildConfigRepository) SetAssistantEnabled(ctx context.Context, guildID string, enabled bool) (store.GuildConfig, error) {
	return r.update(ctx, guildID, map[string]any{
		"assistant_enabled": enabled,
		"updated_at":        time.Now().UTC(),
	})
}

func (r *GuildConfigRepository) SetMemoryEnabled(ctx context.Context, guildID string, enabled bool) (store.GuildConfig, error) {
	return r.update(ctx, guildID, map[string]any{
		"memory_enabled": enabled,
		"updated_at":     time.Now().UTC(),
	})
}

func (r *GuildConfigRepository) SetAssistantTimeoutUntil(ctx context.Context, guildID string, until time.Time, actor string) (store.GuildConfig, error) {
	until = until.UTC()
	return r.update(ctx, guildID, map[string]any{
		"assistant_timeout_until": &until,
		"assistant_timeout_by":    strings.TrimSpace(actor),
		"updated_at":              time.Now().UTC(),
	})
}

func (r *GuildConfigRepository) ClearAssistantTimeout(ctx context.Context, guildID string) (store.GuildConfig, error) {
	return r.update(ctx, guildID, map[string]any{
		"assistant_timeout_until": nil,
		"assistant_timeout_by":    "",
		"updated_at":              time.Now().UTC(),
	})
}

func (r *GuildConfigRepository) ResolveAssistantTimeout(ctx context.Context, guildID string, now time.Time) (store.GuildConfig, bool, error) {
	config, ok, err := r.Get(ctx, guildID)
	if err != nil || !ok {
		return config, false, err
	}
	if !AssistantTimeoutActive(config, now) {
		if config.AssistantTimeoutUntil != nil {
			config, err = r.ClearAssistantTimeout(ctx, guildID)
		}
		return config, false, err
	}
	return config, true, nil
}

func AssistantTimeoutActive(config store.GuildConfig, now time.Time) bool {
	if config.AssistantTimeoutUntil == nil {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now.UTC().Before(config.AssistantTimeoutUntil.UTC())
}

func (r *GuildConfigRepository) update(ctx context.Context, guildID string, values map[string]any) (store.GuildConfig, error) {
	var config store.GuildConfig
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		existing, ok, err := findGuildConfigByGuild(tx, guildID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		config = existing
		if err := tx.Model(&config).Updates(values).Error; err != nil {
			return err
		}
		return tx.Where("guild_id = ?", guildID).First(&config).Error
	})
	return config, err
}

func (r *GuildConfigRepository) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&store.GuildConfig{}).Count(&count).Error
	return count, err
}

func findGuildConfigByGuild(tx *gorm.DB, guildID string) (store.GuildConfig, bool, error) {
	var config store.GuildConfig
	result := tx.Where("guild_id = ?", guildID).Limit(1).Find(&config)
	if result.Error != nil {
		return store.GuildConfig{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return store.GuildConfig{}, false, nil
	}
	return config, true, nil
}
