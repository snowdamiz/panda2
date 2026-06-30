package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

const (
	clipListDefaultLimit = 100
	clipListMaxLimit     = 500
)

type ClipUsageReservation struct {
	Reserved bool
	Used     int
	Limit    int
}

// ClipRepository persists and queries YouTube clips by their creating user.
type ClipRepository struct {
	db *gorm.DB
}

func NewClipRepository(db *gorm.DB) *ClipRepository {
	return &ClipRepository{db: db}
}

// Create stores a clip record.
func (r *ClipRepository) Create(ctx context.Context, clip store.YoutubeClip) error {
	return r.db.WithContext(ctx).Create(&clip).Error
}

// ReserveDailyUsage records one accepted clip-generation video request for a
// user if the user has not reached limit on usageDate. The insert is one SQL
// statement so concurrent callers share the same database-enforced gate.
func (r *ClipRepository) ReserveDailyUsage(ctx context.Context, usage store.YoutubeClipUsage, limit int) (ClipUsageReservation, error) {
	usage.UserID = strings.TrimSpace(usage.UserID)
	usage.GuildID = strings.TrimSpace(usage.GuildID)
	usage.RequestID = strings.TrimSpace(usage.RequestID)
	usage.UsageDate = strings.TrimSpace(usage.UsageDate)
	usage.ID = strings.TrimSpace(usage.ID)
	if usage.UserID == "" {
		return ClipUsageReservation{}, errors.New("clip usage user_id is required")
	}
	if usage.ID == "" {
		return ClipUsageReservation{}, errors.New("clip usage id is required")
	}
	if usage.UsageDate == "" {
		usage.UsageDate = YoutubeClipUsageDate(time.Now().UTC())
	}
	if limit <= 0 {
		return ClipUsageReservation{Reserved: true, Used: 0, Limit: limit}, nil
	}
	now := usage.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
		usage.CreatedAt = now
	}
	if usage.RequestID != "" {
		var existing int64
		err := r.db.WithContext(ctx).Table("youtube_clip_usages").
			Where("user_id = ? AND request_id = ?", usage.UserID, usage.RequestID).
			Count(&existing).Error
		if err != nil {
			return ClipUsageReservation{}, err
		}
		if existing > 0 {
			used, err := r.CountDailyUsage(ctx, usage.UserID, usage.UsageDate)
			if err != nil {
				return ClipUsageReservation{}, err
			}
			return ClipUsageReservation{Reserved: true, Used: used, Limit: limit}, nil
		}
	}
	result := r.db.WithContext(ctx).Exec(`
		INSERT INTO youtube_clip_usages (id, user_id, guild_id, request_id, usage_date, created_at)
		SELECT ?, ?, ?, ?, ?, ?
		WHERE (
			SELECT COUNT(*)
			FROM youtube_clip_usages
			WHERE user_id = ? AND usage_date = ?
		) < ?`,
		usage.ID, usage.UserID, usage.GuildID, usage.RequestID, usage.UsageDate, usage.CreatedAt,
		usage.UserID, usage.UsageDate, limit,
	)
	if result.Error != nil {
		if usage.RequestID != "" && strings.Contains(strings.ToLower(result.Error.Error()), "unique") {
			used, err := r.CountDailyUsage(ctx, usage.UserID, usage.UsageDate)
			if err != nil {
				return ClipUsageReservation{}, err
			}
			return ClipUsageReservation{Reserved: true, Used: used, Limit: limit}, nil
		}
		return ClipUsageReservation{}, result.Error
	}
	used, err := r.CountDailyUsage(ctx, usage.UserID, usage.UsageDate)
	if err != nil {
		return ClipUsageReservation{}, err
	}
	return ClipUsageReservation{Reserved: result.RowsAffected > 0, Used: used, Limit: limit}, nil
}

func (r *ClipRepository) CountDailyUsage(ctx context.Context, userID, usageDate string) (int, error) {
	userID = strings.TrimSpace(userID)
	usageDate = strings.TrimSpace(usageDate)
	if userID == "" || usageDate == "" {
		return 0, nil
	}
	var count int64
	err := r.db.WithContext(ctx).Table("youtube_clip_usages").
		Where("user_id = ? AND usage_date = ?", userID, usageDate).
		Count(&count).Error
	if err != nil {
		return 0, err
	}
	return int(count), nil
}

func YoutubeClipUsageDate(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// ListByUser returns the clips created by userID, newest first.
func (r *ClipRepository) ListByUser(ctx context.Context, userID string, limit, offset int) ([]store.YoutubeClip, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = clipListDefaultLimit
	}
	if limit > clipListMaxLimit {
		limit = clipListMaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	var clips []store.YoutubeClip
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&clips).Error
	if err != nil {
		return nil, err
	}
	return clips, nil
}

// GetByIDForUser returns the clip if it exists and belongs to userID.
func (r *ClipRepository) GetByIDForUser(ctx context.Context, id, userID string) (store.YoutubeClip, bool, error) {
	return findClipForUser(r.db.WithContext(ctx), id, userID)
}

// DeleteByIDForUser deletes the clip if it belongs to userID, returning the
// deleted row so the caller can clean up the underlying objects. ErrNotFound is
// returned when no matching clip exists.
func (r *ClipRepository) DeleteByIDForUser(ctx context.Context, id, userID string) (store.YoutubeClip, error) {
	var deleted store.YoutubeClip
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		clip, ok, err := findClipForUser(tx, id, userID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		if err := tx.Where("id = ? AND user_id = ?", clip.ID, clip.UserID).Delete(&store.YoutubeClip{}).Error; err != nil {
			return err
		}
		deleted = clip
		return nil
	})
	if err != nil {
		return store.YoutubeClip{}, err
	}
	return deleted, nil
}

func findClipForUser(tx *gorm.DB, id, userID string) (store.YoutubeClip, bool, error) {
	id = strings.TrimSpace(id)
	userID = strings.TrimSpace(userID)
	if id == "" || userID == "" {
		return store.YoutubeClip{}, false, nil
	}
	var clip store.YoutubeClip
	result := tx.Where("id = ? AND user_id = ?", id, userID).Limit(1).Find(&clip)
	if result.Error != nil {
		return store.YoutubeClip{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return store.YoutubeClip{}, false, nil
	}
	return clip, true, nil
}
