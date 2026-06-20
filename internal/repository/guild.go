package repository

import (
	"context"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

const (
	GuildInstallStatusActive = "active"
	GuildInstallStatusDenied = "denied"
	GuildInstallStatusLeft   = "left"
)

type GuildRepository struct {
	db *gorm.DB
}

type GuildInstall struct {
	GuildID           string
	Name              string
	OwnerUserID       string
	InstalledByUserID string
	Locale            string
	AuthorizedAt      time.Time
}

func NewGuildRepository(db *gorm.DB) *GuildRepository {
	return &GuildRepository{db: db}
}

func (r *GuildRepository) RecordAuthorizedInstall(ctx context.Context, install GuildInstall) (store.Guild, error) {
	return r.upsertInstall(ctx, install, GuildInstallStatusActive, nil)
}

func (r *GuildRepository) RecordDeniedInstall(ctx context.Context, install GuildInstall) (store.Guild, error) {
	return r.upsertInstall(ctx, install, GuildInstallStatusDenied, nil)
}

func (r *GuildRepository) MarkLeft(ctx context.Context, guildID string) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).
		Model(&store.Guild{}).
		Where("guild_id = ?", strings.TrimSpace(guildID)).
		Updates(map[string]any{
			"install_status": GuildInstallStatusLeft,
			"left_at":        &now,
			"updated_at":     now,
		}).Error
}

func (r *GuildRepository) Get(ctx context.Context, guildID string) (store.Guild, bool, error) {
	var guild store.Guild
	err := r.db.WithContext(ctx).Where("guild_id = ?", strings.TrimSpace(guildID)).First(&guild).Error
	if err == nil {
		return guild, true, nil
	}
	if err == gorm.ErrRecordNotFound {
		return store.Guild{}, false, nil
	}
	return store.Guild{}, false, err
}

func (r *GuildRepository) upsertInstall(ctx context.Context, install GuildInstall, status string, leftAt *time.Time) (store.Guild, error) {
	now := time.Now().UTC()
	if install.AuthorizedAt.IsZero() {
		install.AuthorizedAt = now
	}
	guildID := strings.TrimSpace(install.GuildID)
	guild := store.Guild{
		GuildID:           guildID,
		Name:              strings.TrimSpace(install.Name),
		InstallStatus:     status,
		OwnerUserID:       strings.TrimSpace(install.OwnerUserID),
		InstalledByUserID: strings.TrimSpace(install.InstalledByUserID),
		Locale:            strings.TrimSpace(install.Locale),
		JoinedAt:          install.AuthorizedAt.UTC(),
		LeftAt:            leftAt,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing store.Guild
		err := tx.Where("guild_id = ?", guildID).First(&existing).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		if err == gorm.ErrRecordNotFound {
			return tx.Create(&guild).Error
		}

		updates := map[string]any{
			"name":                 guild.Name,
			"install_status":       status,
			"owner_user_id":        guild.OwnerUserID,
			"installed_by_user_id": guild.InstalledByUserID,
			"locale":               guild.Locale,
			"joined_at":            guild.JoinedAt,
			"left_at":              leftAt,
			"updated_at":           now,
		}
		if err := tx.Model(&existing).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Where("guild_id = ?", guildID).First(&guild).Error
	})
	return guild, err
}
