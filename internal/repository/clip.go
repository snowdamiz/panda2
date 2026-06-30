package repository

import (
	"context"
	"strings"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

const (
	clipListDefaultLimit = 100
	clipListMaxLimit     = 500
)

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
