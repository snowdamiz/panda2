package repository

import (
	"context"
	"errors"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

type JobRepository struct {
	db *gorm.DB
}

func NewJobRepository(db *gorm.DB) *JobRepository {
	return &JobRepository{db: db}
}

func (r *JobRepository) Enqueue(ctx context.Context, job store.Job) (store.Job, error) {
	now := time.Now().UTC()
	job.Status = firstNonEmpty(job.Status, "queued")
	if job.MaxAttempts == 0 {
		job.MaxAttempts = 3
	}
	if job.RunAfter.IsZero() {
		job.RunAfter = now
	}
	job.CreatedAt = firstTime(job.CreatedAt, now)
	job.UpdatedAt = firstTime(job.UpdatedAt, now)
	return job, r.db.WithContext(ctx).Create(&job).Error
}

func (r *JobRepository) ClaimNext(ctx context.Context, kind, workerID string, lease time.Duration, now time.Time) (store.Job, bool, error) {
	if lease <= 0 {
		lease = time.Minute
	}
	now = now.UTC()
	var claimed store.Job

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Where("status = ? AND run_after <= ?", "queued", now)
		if kind != "" {
			query = query.Where("kind = ?", kind)
		}

		var job store.Job
		err := query.Order("run_after ASC, id ASC").First(&job).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		leaseUntil := now.Add(lease)
		result := tx.Model(&store.Job{}).
			Where("id = ? AND status = ?", job.ID, "queued").
			Updates(map[string]any{
				"status":           "running",
				"lock_owner":       workerID,
				"lease_expires_at": leaseUntil,
				"attempts":         job.Attempts + 1,
				"updated_at":       now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return tx.First(&claimed, job.ID).Error
	})
	if errors.Is(err, ErrNotFound) {
		return store.Job{}, false, nil
	}
	return claimed, err == nil, err
}

func (r *JobRepository) Complete(ctx context.Context, jobID uint, now time.Time) error {
	return r.db.WithContext(ctx).Model(&store.Job{}).
		Where("id = ?", jobID).
		Updates(map[string]any{
			"status":           "succeeded",
			"lock_owner":       "",
			"lease_expires_at": nil,
			"last_error":       "",
			"updated_at":       now.UTC(),
		}).Error
}

func (r *JobRepository) Fail(ctx context.Context, jobID uint, message string, retryAfter time.Duration, now time.Time) error {
	now = now.UTC()
	var job store.Job
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&job, jobID).Error; err != nil {
			return err
		}

		status := "failed"
		runAfter := job.RunAfter
		if job.Attempts < job.MaxAttempts {
			status = "queued"
			runAfter = now.Add(retryAfter)
		}

		return tx.Model(&store.Job{}).Where("id = ?", jobID).Updates(map[string]any{
			"status":           status,
			"lock_owner":       "",
			"lease_expires_at": nil,
			"last_error":       message,
			"run_after":        runAfter,
			"updated_at":       now,
		}).Error
	})
}

func (r *JobRepository) QueueDepth(ctx context.Context, kind string) (int64, error) {
	query := r.db.WithContext(ctx).Model(&store.Job{}).Where("status = ?", "queued")
	if kind != "" {
		query = query.Where("kind = ?", kind)
	}
	var count int64
	err := query.Count(&count).Error
	return count, err
}
