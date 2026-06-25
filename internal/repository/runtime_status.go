package repository

import (
	"context"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

const RuntimeStatusGlobalKey = "global"

type RuntimeStatusRepository struct {
	db *gorm.DB
}

func NewRuntimeStatusRepository(db *gorm.DB) *RuntimeStatusRepository {
	return &RuntimeStatusRepository{db: db}
}

func (r *RuntimeStatusRepository) Get(ctx context.Context) (store.RuntimeStatus, error) {
	return r.ensure(ctx)
}

func (r *RuntimeStatusRepository) Update(ctx context.Context, disabled bool, message, actor string) (store.RuntimeStatus, error) {
	var status store.RuntimeStatus
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		existing, err := ensureRuntimeStatus(tx, now)
		if err != nil {
			return err
		}
		status = existing
		updates := map[string]any{
			"disabled":   disabled,
			"message":    strings.TrimSpace(message),
			"updated_by": strings.TrimSpace(actor),
			"updated_at": now,
		}
		if err := tx.Model(&status).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Where("key = ?", RuntimeStatusGlobalKey).First(&status).Error
	})
	return status, err
}

func (r *RuntimeStatusRepository) ensure(ctx context.Context) (store.RuntimeStatus, error) {
	return ensureRuntimeStatus(r.db.WithContext(ctx), time.Now().UTC())
}

func ensureRuntimeStatus(tx *gorm.DB, now time.Time) (store.RuntimeStatus, error) {
	var status store.RuntimeStatus
	result := tx.Where("key = ?", RuntimeStatusGlobalKey).Limit(1).Find(&status)
	if result.Error != nil {
		return store.RuntimeStatus{}, result.Error
	}
	if result.RowsAffected > 0 {
		return status, nil
	}
	status = store.RuntimeStatus{
		Key:       RuntimeStatusGlobalKey,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := tx.Create(&status).Error; err != nil {
		return store.RuntimeStatus{}, err
	}
	return status, nil
}
