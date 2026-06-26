package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

type UserSafetyRepository struct {
	db *gorm.DB
}

type UserSafetyStatus struct {
	State    store.UserSafetyState
	TimedOut bool
}

func NewUserSafetyRepository(db *gorm.DB) *UserSafetyRepository {
	return &UserSafetyRepository{db: db}
}

func (r *UserSafetyRepository) Status(ctx context.Context, guildID, userID string, now time.Time) (UserSafetyStatus, error) {
	guildID, userID = normalizeSafetyKey(guildID, userID)
	if userID == "" {
		return UserSafetyStatus{}, fmt.Errorf("user id is required")
	}
	now = normalizedSafetyTime(now)

	var status UserSafetyStatus
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, ok, err := findUserSafetyState(tx, guildID, userID)
		if err != nil || !ok {
			return err
		}
		status.State = state
		if safetyTimeoutActive(state, now) {
			status.TimedOut = true
			return nil
		}
		if state.TimeoutUntil == nil {
			return nil
		}
		status.State, err = clearExpiredUserSafetyTimeout(tx, state, now)
		return err
	})
	return status, err
}

func (r *UserSafetyRepository) AddStrike(ctx context.Context, guildID, userID string, threshold int, timeoutDuration time.Duration, now time.Time) (UserSafetyStatus, error) {
	guildID, userID = normalizeSafetyKey(guildID, userID)
	if userID == "" {
		return UserSafetyStatus{}, fmt.Errorf("user id is required")
	}
	if threshold <= 0 {
		return UserSafetyStatus{}, fmt.Errorf("strike threshold must be positive")
	}
	if timeoutDuration <= 0 {
		return UserSafetyStatus{}, fmt.Errorf("timeout duration must be positive")
	}
	now = normalizedSafetyTime(now)

	var status UserSafetyStatus
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, ok, err := findUserSafetyState(tx, guildID, userID)
		if err != nil {
			return err
		}
		if ok && safetyTimeoutActive(state, now) {
			status = UserSafetyStatus{State: state, TimedOut: true}
			return nil
		}
		if ok && state.TimeoutUntil != nil {
			state, err = clearExpiredUserSafetyTimeout(tx, state, now)
			if err != nil {
				return err
			}
		}
		if !ok {
			state = store.UserSafetyState{
				GuildID:   guildID,
				UserID:    userID,
				CreatedAt: now,
				UpdatedAt: now,
			}
		}

		lastStrikeAt := now
		state.ActiveStrikes++
		state.TotalStrikes++
		state.LastStrikeAt = &lastStrikeAt
		state.UpdatedAt = now
		if state.ActiveStrikes >= threshold {
			timeoutUntil := now.Add(timeoutDuration)
			state.ActiveStrikes = 0
			state.TimeoutUntil = &timeoutUntil
		} else {
			state.TimeoutUntil = nil
		}

		if !ok {
			if err := tx.Create(&state).Error; err != nil {
				return err
			}
		} else if err := tx.Model(&state).Updates(map[string]any{
			"active_strikes": state.ActiveStrikes,
			"total_strikes":  state.TotalStrikes,
			"last_strike_at": state.LastStrikeAt,
			"timeout_until":  state.TimeoutUntil,
			"updated_at":     state.UpdatedAt,
		}).Error; err != nil {
			return err
		}
		state, _, err = findUserSafetyState(tx, guildID, userID)
		if err != nil {
			return err
		}
		status = UserSafetyStatus{State: state, TimedOut: safetyTimeoutActive(state, now)}
		return nil
	})
	return status, err
}

func clearExpiredUserSafetyTimeout(tx *gorm.DB, state store.UserSafetyState, now time.Time) (store.UserSafetyState, error) {
	if err := tx.Model(&state).Updates(map[string]any{
		"active_strikes": 0,
		"timeout_until":  nil,
		"updated_at":     now,
	}).Error; err != nil {
		return store.UserSafetyState{}, err
	}
	cleared, _, err := findUserSafetyState(tx, state.GuildID, state.UserID)
	return cleared, err
}

func findUserSafetyState(tx *gorm.DB, guildID, userID string) (store.UserSafetyState, bool, error) {
	var state store.UserSafetyState
	result := tx.Where("guild_id = ? AND user_id = ?", guildID, userID).Limit(1).Find(&state)
	if result.Error != nil {
		return store.UserSafetyState{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return store.UserSafetyState{}, false, nil
	}
	return state, true, nil
}

func safetyTimeoutActive(state store.UserSafetyState, now time.Time) bool {
	return state.TimeoutUntil != nil && now.Before(state.TimeoutUntil.UTC())
}

func normalizedSafetyTime(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now.UTC()
}

func normalizeSafetyKey(guildID, userID string) (string, string) {
	return strings.TrimSpace(guildID), strings.TrimSpace(userID)
}
