package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	InstallIntentStatusPending  = "pending"
	InstallIntentStatusConsumed = "consumed"
	InstallIntentStatusExpired  = "expired"
	InstallIntentStatusCanceled = "canceled"
)

var (
	ErrInstallIntentUnavailable = errors.New("install intent is not pending")
	ErrInstallIntentExpired     = errors.New("install intent is expired")
)

type GuildFeatureState struct {
	FeatureID             string
	Enabled               bool
	SourceInstallIntentID string
	EnabledByUserID       string
	UpdatedAt             time.Time
}

type FeatureRepository struct {
	db *gorm.DB
}

func NewFeatureRepository(db *gorm.DB) *FeatureRepository {
	return &FeatureRepository{db: db}
}

func (r *FeatureRepository) CreateInstallIntent(ctx context.Context, intent store.InstallIntent) (store.InstallIntent, error) {
	now := time.Now().UTC()
	if intent.CreatedAt.IsZero() {
		intent.CreatedAt = now
	}
	if intent.UpdatedAt.IsZero() {
		intent.UpdatedAt = now
	}
	if strings.TrimSpace(intent.Status) == "" {
		intent.Status = InstallIntentStatusPending
	}
	err := r.db.WithContext(ctx).Create(&intent).Error
	return intent, err
}

func (r *FeatureRepository) GetInstallIntentByStateHash(ctx context.Context, stateHash string) (store.InstallIntent, bool, error) {
	var intent store.InstallIntent
	err := r.db.WithContext(ctx).Where("state_hash = ?", strings.TrimSpace(stateHash)).First(&intent).Error
	if err == nil {
		return intent, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.InstallIntent{}, false, nil
	}
	return store.InstallIntent{}, false, err
}

func (r *FeatureRepository) ConsumeInstallIntent(ctx context.Context, stateHash, guildID, installerUserID, grantedPermissionsJSON, grantedScopesJSON string, now time.Time) (store.InstallIntent, error) {
	stateHash = strings.TrimSpace(stateHash)
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	var consumed store.InstallIntent
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		intent, err := consumeInstallIntentTx(tx, stateHash, guildID, installerUserID, grantedPermissionsJSON, grantedScopesJSON, now)
		if err != nil {
			return err
		}
		consumed = intent
		return nil
	})
	if errors.Is(err, ErrInstallIntentExpired) {
		_ = markInstallIntentExpired(ctx, r.db, stateHash, now)
	}
	return consumed, err
}

func (r *FeatureRepository) ConsumeInstallIntentAndSetGuildFeatures(ctx context.Context, stateHash, guildID, installerUserID, grantedPermissionsJSON, grantedScopesJSON string, now time.Time) (store.InstallIntent, error) {
	stateHash = strings.TrimSpace(stateHash)
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	var consumed store.InstallIntent
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		intent, err := consumeInstallIntentTx(tx, stateHash, guildID, installerUserID, grantedPermissionsJSON, grantedScopesJSON, now)
		if err != nil {
			return err
		}
		featureIDs := stringSliceFromJSON(intent.ExpandedFeatureIDs)
		if len(featureIDs) == 0 {
			return errors.New("install intent has no expanded feature set")
		}
		if err := setGuildFeaturesTx(tx, guildID, featureIDs, intent.IntentID, installerUserID, now); err != nil {
			return err
		}
		consumed = intent
		return nil
	})
	if errors.Is(err, ErrInstallIntentExpired) {
		_ = markInstallIntentExpired(ctx, r.db, stateHash, now)
	}
	return consumed, err
}

func (r *FeatureRepository) SetGuildFeatures(ctx context.Context, guildID string, featureIDs []string, sourceInstallIntentID, actorID string, now time.Time) error {
	return r.SetGuildFeatureStates(ctx, guildID, featureIDs, nil, sourceInstallIntentID, actorID, now)
}

func (r *FeatureRepository) SetGuildFeatureStates(ctx context.Context, guildID string, enabledFeatureIDs []string, disabledFeatureIDs []string, sourceInstallIntentID, actorID string, now time.Time) error {
	guildID = strings.TrimSpace(guildID)
	if guildID == "" {
		return errors.New("guild_id is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return setGuildFeatureStatesTx(tx, guildID, enabledFeatureIDs, disabledFeatureIDs, sourceInstallIntentID, actorID, now)
	})
}

func consumeInstallIntentTx(tx *gorm.DB, stateHash, guildID, installerUserID, grantedPermissionsJSON, grantedScopesJSON string, now time.Time) (store.InstallIntent, error) {
	var intent store.InstallIntent
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state_hash = ?", stateHash).First(&intent).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.InstallIntent{}, ErrNotFound
	}
	if err != nil {
		return store.InstallIntent{}, err
	}
	if intent.Status != InstallIntentStatusPending {
		return store.InstallIntent{}, ErrInstallIntentUnavailable
	}
	if !intent.ExpiresAt.IsZero() && !intent.ExpiresAt.After(now) {
		_ = tx.Model(&intent).Updates(map[string]any{
			"status":     InstallIntentStatusExpired,
			"updated_at": now,
		}).Error
		return store.InstallIntent{}, ErrInstallIntentExpired
	}
	updates := map[string]any{
		"status":                      InstallIntentStatusConsumed,
		"guild_id":                    strings.TrimSpace(guildID),
		"installer_user_id":           strings.TrimSpace(installerUserID),
		"granted_discord_permissions": strings.TrimSpace(grantedPermissionsJSON),
		"granted_scopes":              strings.TrimSpace(grantedScopesJSON),
		"consumed_at":                 now,
		"updated_at":                  now,
	}
	if updates["granted_discord_permissions"] == "" {
		updates["granted_discord_permissions"] = "[]"
	}
	if updates["granted_scopes"] == "" {
		updates["granted_scopes"] = "[]"
	}
	if err := tx.Model(&intent).Updates(updates).Error; err != nil {
		return store.InstallIntent{}, err
	}
	var consumed store.InstallIntent
	if err := tx.Where("intent_id = ?", intent.IntentID).First(&consumed).Error; err != nil {
		return store.InstallIntent{}, err
	}
	return consumed, nil
}

func markInstallIntentExpired(ctx context.Context, db *gorm.DB, stateHash string, now time.Time) error {
	return db.WithContext(ctx).Model(&store.InstallIntent{}).
		Where("state_hash = ? AND status = ?", stateHash, InstallIntentStatusPending).
		Updates(map[string]any{
			"status":     InstallIntentStatusExpired,
			"updated_at": now,
		}).Error
}

func setGuildFeaturesTx(tx *gorm.DB, guildID string, featureIDs []string, sourceInstallIntentID, actorID string, now time.Time) error {
	return setGuildFeatureStatesTx(tx, guildID, featureIDs, nil, sourceInstallIntentID, actorID, now)
}

func setGuildFeatureStatesTx(tx *gorm.DB, guildID string, enabledFeatureIDs []string, disabledFeatureIDs []string, sourceInstallIntentID, actorID string, now time.Time) error {
	enabled := map[string]struct{}{}
	for _, featureID := range enabledFeatureIDs {
		featureID = strings.TrimSpace(featureID)
		if featureID != "" {
			enabled[featureID] = struct{}{}
		}
	}
	disabled := map[string]struct{}{}
	for _, featureID := range disabledFeatureIDs {
		featureID = strings.TrimSpace(featureID)
		if featureID == "" {
			continue
		}
		if _, alsoEnabled := enabled[featureID]; alsoEnabled {
			continue
		}
		disabled[featureID] = struct{}{}
	}
	if err := tx.Model(&store.GuildFeature{}).
		Where("guild_id = ?", guildID).
		Updates(map[string]any{"enabled": false, "updated_at": now}).Error; err != nil {
		return err
	}
	for featureID := range enabled {
		row := store.GuildFeature{
			GuildID:               guildID,
			FeatureID:             featureID,
			Enabled:               true,
			SourceInstallIntentID: strings.TrimSpace(sourceInstallIntentID),
			EnabledByUserID:       strings.TrimSpace(actorID),
			CreatedAt:             now,
			UpdatedAt:             now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "guild_id"}, {Name: "feature_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"enabled":                  true,
				"source_install_intent_id": row.SourceInstallIntentID,
				"enabled_by_user_id":       row.EnabledByUserID,
				"updated_at":               now,
			}),
		}).Create(&row).Error; err != nil {
			return err
		}
	}
	for featureID := range disabled {
		row := map[string]any{
			"guild_id":                 guildID,
			"feature_id":               featureID,
			"enabled":                  false,
			"source_install_intent_id": strings.TrimSpace(sourceInstallIntentID),
			"enabled_by_user_id":       strings.TrimSpace(actorID),
			"created_at":               now,
			"updated_at":               now,
		}
		if err := tx.Model(&store.GuildFeature{}).Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "guild_id"}, {Name: "feature_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"enabled":                  false,
				"source_install_intent_id": row["source_install_intent_id"],
				"enabled_by_user_id":       row["enabled_by_user_id"],
				"updated_at":               now,
			}),
		}).Create(row).Error; err != nil {
			return err
		}
	}
	return nil
}

func stringSliceFromJSON(raw string) []string {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	result := values[:0]
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func (r *FeatureRepository) EnabledFeatureSet(ctx context.Context, guildID string) (map[string]struct{}, error) {
	var rows []store.GuildFeature
	err := r.db.WithContext(ctx).Where("guild_id = ? AND enabled = ?", strings.TrimSpace(guildID), true).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := map[string]struct{}{}
	for _, row := range rows {
		featureID := strings.TrimSpace(row.FeatureID)
		if featureID != "" {
			result[featureID] = struct{}{}
		}
	}
	return result, nil
}

func (r *FeatureRepository) ListGuildFeatures(ctx context.Context, guildID string) ([]GuildFeatureState, error) {
	var rows []store.GuildFeature
	err := r.db.WithContext(ctx).Where("guild_id = ?", strings.TrimSpace(guildID)).Order("feature_id ASC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	states := make([]GuildFeatureState, 0, len(rows))
	for _, row := range rows {
		states = append(states, GuildFeatureState{
			FeatureID:             row.FeatureID,
			Enabled:               row.Enabled,
			SourceInstallIntentID: row.SourceInstallIntentID,
			EnabledByUserID:       row.EnabledByUserID,
			UpdatedAt:             row.UpdatedAt,
		})
	}
	return states, nil
}
