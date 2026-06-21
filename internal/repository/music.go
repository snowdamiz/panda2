package repository

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

type MusicRepository struct {
	db *gorm.DB
}

func NewMusicRepository(db *gorm.DB) *MusicRepository {
	return &MusicRepository{db: db}
}

func (r *MusicRepository) ReplaceQueue(ctx context.Context, guildID string, items []store.MusicQueueItem) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("guild_id = ?", guildID).Delete(&store.MusicQueueItem{}).Error; err != nil {
			return err
		}
		for index := range items {
			items[index].GuildID = guildID
			items[index].Position = index + 1
			items[index].CreatedAt = firstTime(items[index].CreatedAt, now)
			items[index].UpdatedAt = now
			if err := tx.Create(&items[index]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *MusicRepository) Queue(ctx context.Context, guildID string) ([]store.MusicQueueItem, error) {
	var items []store.MusicQueueItem
	err := r.db.WithContext(ctx).
		Where("guild_id = ?", guildID).
		Order("position ASC, id ASC").
		Find(&items).Error
	return items, err
}

func (r *MusicRepository) ClearQueue(ctx context.Context, guildID string) error {
	return r.db.WithContext(ctx).Where("guild_id = ?", guildID).Delete(&store.MusicQueueItem{}).Error
}

func (r *MusicRepository) EnsureSettings(ctx context.Context, guildID string) (store.MusicSettings, error) {
	now := time.Now().UTC()
	settings := store.MusicSettings{
		GuildID:           guildID,
		LoopMode:          "off",
		DefaultVolume:     100,
		VoteSkipThreshold: 0.5,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing store.MusicSettings
		err := tx.Where("guild_id = ?", guildID).First(&existing).Error
		if err == nil {
			settings = existing
			return nil
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		return tx.Create(&settings).Error
	})
	return settings, err
}

func (r *MusicRepository) UpdateSettings(ctx context.Context, guildID string, values map[string]any) (store.MusicSettings, error) {
	now := time.Now().UTC()
	if _, err := r.EnsureSettings(ctx, guildID); err != nil {
		return store.MusicSettings{}, err
	}
	for key, value := range values {
		if text, ok := value.(string); ok {
			values[key] = strings.TrimSpace(text)
		}
	}
	values["updated_at"] = now
	var settings store.MusicSettings
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&store.MusicSettings{}).Where("guild_id = ?", guildID).Updates(values).Error; err != nil {
			return err
		}
		return tx.Where("guild_id = ?", guildID).First(&settings).Error
	})
	return settings, err
}

func (r *MusicRepository) SavePlaylist(ctx context.Context, playlist store.MusicPlaylist) (store.MusicPlaylist, error) {
	now := time.Now().UTC()
	playlist.GuildID = strings.TrimSpace(playlist.GuildID)
	playlist.Name = strings.ToLower(strings.TrimSpace(playlist.Name))
	playlist.TracksJSON = firstNonEmpty(playlist.TracksJSON, "[]")
	playlist.CreatedAt = firstTime(playlist.CreatedAt, now)
	playlist.UpdatedAt = now
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing store.MusicPlaylist
		err := tx.Where("guild_id = ? AND name = ?", playlist.GuildID, playlist.Name).First(&existing).Error
		if err == nil {
			if err := tx.Model(&existing).Updates(map[string]any{
				"tracks_json": playlist.TracksJSON,
				"created_by":  firstNonEmpty(playlist.CreatedBy, existing.CreatedBy),
				"updated_at":  now,
			}).Error; err != nil {
				return err
			}
			playlist = existing
			return tx.First(&playlist, existing.ID).Error
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		return tx.Create(&playlist).Error
	})
	return playlist, err
}

func (r *MusicRepository) Playlist(ctx context.Context, guildID, name string) (store.MusicPlaylist, bool, error) {
	var playlist store.MusicPlaylist
	err := r.db.WithContext(ctx).
		Where("guild_id = ? AND name = ?", guildID, strings.ToLower(strings.TrimSpace(name))).
		First(&playlist).Error
	if err == nil {
		return playlist, true, nil
	}
	if err == gorm.ErrRecordNotFound {
		return store.MusicPlaylist{}, false, nil
	}
	return store.MusicPlaylist{}, false, err
}

func (r *MusicRepository) Playlists(ctx context.Context, guildID string, limit int) ([]store.MusicPlaylist, error) {
	var playlists []store.MusicPlaylist
	err := r.db.WithContext(ctx).
		Where("guild_id = ?", guildID).
		Order("name ASC").
		Limit(clampLimit(limit, 25, 100)).
		Find(&playlists).Error
	return playlists, err
}

func MarshalPlaylistTracks(items []store.MusicQueueItem) (string, error) {
	data, err := json.Marshal(items)
	if err != nil {
		return "[]", err
	}
	return string(data), nil
}

func UnmarshalPlaylistTracks(raw string) ([]store.MusicQueueItem, error) {
	var items []store.MusicQueueItem
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	err := json.Unmarshal([]byte(raw), &items)
	return items, err
}
