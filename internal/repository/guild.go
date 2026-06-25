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
	return r.upsertInstall(ctx, install, GuildInstallStatusActive, nil, true)
}

func (r *GuildRepository) RecordObservedInstall(ctx context.Context, install GuildInstall) (store.Guild, error) {
	return r.upsertInstall(ctx, install, GuildInstallStatusActive, nil, false)
}

func (r *GuildRepository) Get(ctx context.Context, guildID string) (store.Guild, bool, error) {
	return findGuildByID(r.db.WithContext(ctx), guildID)
}

func (r *GuildRepository) upsertInstall(ctx context.Context, install GuildInstall, status string, leftAt *time.Time, overwriteInstaller bool) (store.Guild, error) {
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
		existing, ok, err := findGuildByID(tx, guildID)
		if err != nil {
			return err
		}
		if !ok {
			return tx.Create(&guild).Error
		}

		updates := map[string]any{
			"name":           guild.Name,
			"install_status": status,
			"owner_user_id":  guild.OwnerUserID,
			"locale":         guild.Locale,
			"joined_at":      guild.JoinedAt,
			"left_at":        leftAt,
			"updated_at":     now,
		}
		if strings.TrimSpace(guild.InstalledByUserID) != "" && (overwriteInstaller || strings.TrimSpace(existing.InstalledByUserID) == "") {
			updates["installed_by_user_id"] = guild.InstalledByUserID
		}
		if err := tx.Model(&existing).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Where("guild_id = ?", guildID).First(&guild).Error
	})
	return guild, err
}

func findGuildByID(tx *gorm.DB, guildID string) (store.Guild, bool, error) {
	var guild store.Guild
	result := tx.Where("guild_id = ?", strings.TrimSpace(guildID)).Limit(1).Find(&guild)
	if result.Error != nil {
		return store.Guild{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return store.Guild{}, false, nil
	}
	return guild, true, nil
}
