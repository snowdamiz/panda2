package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ScheduleStatusActive    = "active"
	ScheduleStatusPaused    = "paused"
	ScheduleStatusCompleted = "completed"

	ScheduleLastQueued    = "queued"
	ScheduleLastRunning   = "running"
	ScheduleLastSucceeded = "succeeded"
	ScheduleLastSkipped   = "skipped"
	ScheduleLastFailed    = "failed"
)

type ScheduleRepository struct {
	db *gorm.DB
}

type ScheduleStats struct {
	Active     int64
	Paused     int64
	Completed  int64
	FailedRuns int64
}

func NewScheduleRepository(db *gorm.DB) *ScheduleRepository {
	return &ScheduleRepository{db: db}
}

func (r *ScheduleRepository) Create(ctx context.Context, schedule store.Schedule) (store.Schedule, error) {
	now := time.Now().UTC()
	schedule.GuildID = strings.TrimSpace(schedule.GuildID)
	schedule.ChannelID = strings.TrimSpace(schedule.ChannelID)
	schedule.OwnerUserID = strings.TrimSpace(schedule.OwnerUserID)
	schedule.Kind = strings.TrimSpace(schedule.Kind)
	schedule.Status = firstNonEmpty(schedule.Status, ScheduleStatusActive)
	schedule.TargetType = firstNonEmpty(schedule.TargetType, "channel")
	schedule.TargetID = strings.TrimSpace(schedule.TargetID)
	schedule.ScheduleType = firstNonEmpty(schedule.ScheduleType, "once")
	schedule.Timezone = firstNonEmpty(schedule.Timezone, "UTC")
	schedule.Payload = firstNonEmpty(schedule.Payload, "{}")
	schedule.CreatedAt = firstTime(schedule.CreatedAt, now)
	schedule.UpdatedAt = firstTime(schedule.UpdatedAt, now)
	if schedule.NextRunAt.IsZero() {
		schedule.NextRunAt = now
	}
	err := r.db.WithContext(ctx).Create(&schedule).Error
	return schedule, err
}

func (r *ScheduleRepository) Get(ctx context.Context, guildID string, id uint) (store.Schedule, bool, error) {
	var schedule store.Schedule
	err := r.db.WithContext(ctx).Where("guild_id = ? AND id = ?", guildID, id).First(&schedule).Error
	if err == nil {
		return schedule, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.Schedule{}, false, nil
	}
	return store.Schedule{}, false, err
}

func (r *ScheduleRepository) List(ctx context.Context, guildID, ownerUserID, kind string, includeDisabled bool, limit int) ([]store.Schedule, error) {
	query := r.db.WithContext(ctx).Where("guild_id = ?", guildID)
	if strings.TrimSpace(ownerUserID) != "" {
		query = query.Where("owner_user_id = ?", strings.TrimSpace(ownerUserID))
	}
	if strings.TrimSpace(kind) != "" {
		query = query.Where("kind = ?", strings.TrimSpace(kind))
	}
	if !includeDisabled {
		query = query.Where("disabled = ?", false)
	}
	var schedules []store.Schedule
	err := query.Order("next_run_at ASC, id ASC").Limit(clampLimit(limit, 25, 100)).Find(&schedules).Error
	return schedules, err
}

func (r *ScheduleRepository) ClaimDue(ctx context.Context, now time.Time, limit int, lockFor time.Duration) ([]store.Schedule, error) {
	if lockFor <= 0 {
		lockFor = 2 * time.Minute
	}
	now = now.UTC()
	lockedUntil := now.Add(lockFor)
	limit = clampLimit(limit, 25, 100)
	var schedules []store.Schedule
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var due []store.Schedule
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("disabled = ? AND status = ? AND next_run_at <= ? AND (locked_until IS NULL OR locked_until <= ?)", false, ScheduleStatusActive, now, now).
			Order("next_run_at ASC, id ASC").
			Limit(limit).
			Find(&due).Error; err != nil {
			return err
		}
		for _, schedule := range due {
			result := tx.Model(&store.Schedule{}).
				Where("id = ? AND (locked_until IS NULL OR locked_until <= ?)", schedule.ID, now).
				Updates(map[string]any{
					"locked_until": lockedUntil,
					"last_status":  ScheduleLastQueued,
					"last_error":   "",
					"updated_at":   now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			schedule.LockedUntil = &lockedUntil
			schedule.LastStatus = ScheduleLastQueued
			schedule.LastError = ""
			schedules = append(schedules, schedule)
		}
		return nil
	})
	return schedules, err
}

func (r *ScheduleRepository) AttachJob(ctx context.Context, scheduleID uint, jobID uint, now time.Time) error {
	return r.db.WithContext(ctx).Model(&store.Schedule{}).
		Where("id = ?", scheduleID).
		Updates(map[string]any{
			"last_job_id": jobID,
			"updated_at":  now.UTC(),
		}).Error
}

func (r *ScheduleRepository) MarkRunning(ctx context.Context, scheduleID uint, now time.Time) error {
	now = now.UTC()
	return r.db.WithContext(ctx).Model(&store.Schedule{}).
		Where("id = ?", scheduleID).
		Updates(map[string]any{
			"last_run_at": now,
			"last_status": ScheduleLastRunning,
			"updated_at":  now,
		}).Error
}

func (r *ScheduleRepository) MarkFinished(ctx context.Context, scheduleID uint, status, message string, nextRunAt *time.Time, disabled bool, now time.Time) error {
	now = now.UTC()
	updates := map[string]any{
		"last_status":  firstNonEmpty(status, ScheduleLastSucceeded),
		"last_error":   strings.TrimSpace(message),
		"locked_until": nil,
		"run_count":    gorm.Expr("run_count + 1"),
		"disabled":     disabled,
		"updated_at":   now,
	}
	if nextRunAt != nil {
		updates["next_run_at"] = nextRunAt.UTC()
	}
	if disabled && nextRunAt == nil {
		updates["status"] = ScheduleStatusCompleted
	}
	return r.db.WithContext(ctx).Model(&store.Schedule{}).
		Where("id = ?", scheduleID).
		Updates(updates).Error
}

func (r *ScheduleRepository) MarkFailed(ctx context.Context, scheduleID uint, message string, now time.Time, releaseLock bool) error {
	updates := map[string]any{
		"last_status": ScheduleLastFailed,
		"last_error":  strings.TrimSpace(message),
		"updated_at":  now.UTC(),
	}
	if releaseLock {
		updates["locked_until"] = nil
	}
	return r.db.WithContext(ctx).Model(&store.Schedule{}).
		Where("id = ?", scheduleID).
		Updates(updates).Error
}

func (r *ScheduleRepository) SetDisabled(ctx context.Context, guildID string, id uint, disabled bool, now time.Time) error {
	status := ScheduleStatusActive
	if disabled {
		status = ScheduleStatusPaused
	}
	result := r.db.WithContext(ctx).Model(&store.Schedule{}).
		Where("guild_id = ? AND id = ?", guildID, id).
		Updates(map[string]any{
			"disabled":     disabled,
			"status":       status,
			"locked_until": nil,
			"updated_at":   now.UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *ScheduleRepository) Snooze(ctx context.Context, guildID string, id uint, nextRunAt time.Time, now time.Time) error {
	result := r.db.WithContext(ctx).Model(&store.Schedule{}).
		Where("guild_id = ? AND id = ?", guildID, id).
		Updates(map[string]any{
			"next_run_at":  nextRunAt.UTC(),
			"disabled":     false,
			"status":       ScheduleStatusActive,
			"locked_until": nil,
			"updated_at":   now.UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *ScheduleRepository) Stats(ctx context.Context, guildID string) (ScheduleStats, error) {
	var stats ScheduleStats
	base := r.db.WithContext(ctx).Model(&store.Schedule{}).Where("guild_id = ?", guildID)
	if err := base.Where("disabled = ? AND status = ?", false, ScheduleStatusActive).Count(&stats.Active).Error; err != nil {
		return ScheduleStats{}, err
	}
	if err := base.Where("status = ?", ScheduleStatusPaused).Count(&stats.Paused).Error; err != nil {
		return ScheduleStats{}, err
	}
	if err := base.Where("status = ?", ScheduleStatusCompleted).Count(&stats.Completed).Error; err != nil {
		return ScheduleStats{}, err
	}
	err := base.Where("last_status = ?", ScheduleLastFailed).Count(&stats.FailedRuns).Error
	return stats, err
}
