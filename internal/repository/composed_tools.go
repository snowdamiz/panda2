package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ComposedToolRepository struct {
	db *gorm.DB
}

type ComposedToolRecord struct {
	Tool    store.ComposedTool
	Version store.ComposedToolVersion
}

func NewComposedToolRepository(db *gorm.DB) *ComposedToolRepository {
	return &ComposedToolRepository{db: db}
}

func (r *ComposedToolRepository) CreateDraft(ctx context.Context, tool store.ComposedTool, version store.ComposedToolVersion) (ComposedToolRecord, error) {
	now := time.Now().UTC()
	tool.Status = firstNonEmpty(tool.Status, "draft")
	tool.Visibility = firstNonEmpty(tool.Visibility, "guild")
	tool.CreatedAt = firstTime(tool.CreatedAt, now)
	tool.UpdatedAt = firstTime(tool.UpdatedAt, now)
	version.VersionNumber = firstPositive(version.VersionNumber, 1)
	version.CreatedAt = firstTime(version.CreatedAt, now)

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&tool).Error; err != nil {
			return err
		}
		version.ComposedToolID = tool.ID
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		return nil
	})
	return ComposedToolRecord{Tool: tool, Version: version}, err
}

func (r *ComposedToolRepository) AddDraftVersion(ctx context.Context, toolID uint, version store.ComposedToolVersion) (store.ComposedToolVersion, error) {
	now := time.Now().UTC()
	version.CreatedAt = firstTime(version.CreatedAt, now)
	var created store.ComposedToolVersion
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var latest store.ComposedToolVersion
		err := tx.Where("composed_tool_id = ?", toolID).Order("version_number DESC").First(&latest).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		version.ComposedToolID = toolID
		version.VersionNumber = latest.VersionNumber + 1
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		created = version
		return tx.Model(&store.ComposedTool{}).
			Where("id = ?", toolID).
			Update("updated_at", now).Error
	})
	return created, err
}

func (r *ComposedToolRepository) UpdateDraftVersion(ctx context.Context, versionID uint, specJSON, validationJSON, definitionJSON string) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var version store.ComposedToolVersion
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&version, versionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if version.ApprovedAt != nil {
			return fmt.Errorf("approved composed-tool versions are immutable")
		}
		if err := tx.Model(&store.ComposedToolVersion{}).
			Where("id = ?", versionID).
			Updates(map[string]any{
				"spec_json":            specJSON,
				"validation_json":      firstNonEmpty(validationJSON, "{}"),
				"tool_definition_json": firstNonEmpty(definitionJSON, "{}"),
			}).Error; err != nil {
			return err
		}
		return tx.Model(&store.ComposedTool{}).
			Where("id = ?", version.ComposedToolID).
			Update("updated_at", now).Error
	})
}

func (r *ComposedToolRepository) ApproveVersion(ctx context.Context, guildID, name string, versionNumber int, actorID string) (ComposedToolRecord, error) {
	now := time.Now().UTC()
	var record ComposedToolRecord
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var tool store.ComposedTool
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("guild_id = ? AND name = ?", guildID, name).
			First(&tool).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		var version store.ComposedToolVersion
		if err := tx.Where("composed_tool_id = ? AND version_number = ?", tool.ID, versionNumber).
			First(&version).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if version.ApprovedAt != nil {
			return fmt.Errorf("composed-tool version %d is already approved", version.VersionNumber)
		}
		if err := tx.Model(&version).Updates(map[string]any{
			"approved_by": actorID,
			"approved_at": now,
		}).Error; err != nil {
			return err
		}
		if err := tx.First(&version, version.ID).Error; err != nil {
			return err
		}
		if err := tx.Model(&tool).Updates(map[string]any{
			"current_version_id": version.ID,
			"status":             "enabled",
			"approved_by":        actorID,
			"updated_at":         now,
		}).Error; err != nil {
			return err
		}
		if err := tx.First(&tool, tool.ID).Error; err != nil {
			return err
		}
		record = ComposedToolRecord{Tool: tool, Version: version}
		return nil
	})
	return record, err
}

func (r *ComposedToolRepository) Rollback(ctx context.Context, guildID, name string, versionNumber int, actorID string) (ComposedToolRecord, error) {
	now := time.Now().UTC()
	var record ComposedToolRecord
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var tool store.ComposedTool
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("guild_id = ? AND name = ?", guildID, name).
			First(&tool).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		var version store.ComposedToolVersion
		if err := tx.Where("composed_tool_id = ? AND version_number = ? AND approved_at IS NOT NULL", tool.ID, versionNumber).
			First(&version).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if err := tx.Model(&tool).Updates(map[string]any{
			"current_version_id": version.ID,
			"status":             "enabled",
			"approved_by":        actorID,
			"updated_at":         now,
		}).Error; err != nil {
			return err
		}
		if err := tx.First(&tool, tool.ID).Error; err != nil {
			return err
		}
		record = ComposedToolRecord{Tool: tool, Version: version}
		return nil
	})
	return record, err
}

func (r *ComposedToolRepository) SetStatus(ctx context.Context, guildID, name, status, actorID string) (store.ComposedTool, error) {
	now := time.Now().UTC()
	updates := map[string]any{"status": status, "updated_at": now}
	if strings.TrimSpace(actorID) != "" && status == "enabled" {
		updates["approved_by"] = actorID
	}
	var tool store.ComposedTool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("guild_id = ? AND name = ?", guildID, name).First(&tool).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if err := tx.Model(&tool).Updates(updates).Error; err != nil {
			return err
		}
		return tx.First(&tool, tool.ID).Error
	})
	return tool, err
}

func (r *ComposedToolRepository) DeleteByName(ctx context.Context, guildID, name string) (store.ComposedTool, error) {
	var tool store.ComposedTool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("guild_id = ? AND name = ?", guildID, name).
			First(&tool).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if err := tx.Where("composed_tool_id = ?", tool.ID).Delete(&store.ComposedToolDedupe{}).Error; err != nil {
			return err
		}
		if err := tx.Where("composed_tool_id = ?", tool.ID).Delete(&store.ComposedToolRun{}).Error; err != nil {
			return err
		}
		if err := tx.Where("composed_tool_id = ?", tool.ID).Delete(&store.ComposedToolVersion{}).Error; err != nil {
			return err
		}
		result := tx.Delete(&tool)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
	return tool, err
}

func (r *ComposedToolRepository) GetByName(ctx context.Context, guildID, name string) (store.ComposedTool, bool, error) {
	var tool store.ComposedTool
	err := r.db.WithContext(ctx).Where("guild_id = ? AND name = ?", guildID, name).First(&tool).Error
	if err == nil {
		return tool, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.ComposedTool{}, false, nil
	}
	return store.ComposedTool{}, false, err
}

func (r *ComposedToolRepository) GetCurrent(ctx context.Context, guildID, name string) (ComposedToolRecord, bool, error) {
	tool, ok, err := r.GetByName(ctx, guildID, name)
	if err != nil || !ok || tool.CurrentVersionID == nil {
		return ComposedToolRecord{}, false, err
	}
	var version store.ComposedToolVersion
	err = r.db.WithContext(ctx).First(&version, *tool.CurrentVersionID).Error
	if err == nil {
		return ComposedToolRecord{Tool: tool, Version: version}, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ComposedToolRecord{}, false, nil
	}
	return ComposedToolRecord{}, false, err
}

func (r *ComposedToolRepository) ListByGuild(ctx context.Context, guildID string) ([]store.ComposedTool, error) {
	var tools []store.ComposedTool
	err := r.db.WithContext(ctx).
		Where("guild_id = ?", guildID).
		Order("name ASC").
		Find(&tools).Error
	return tools, err
}

func (r *ComposedToolRepository) ListEnabledWithVersions(ctx context.Context, guildID string) ([]ComposedToolRecord, error) {
	var tools []store.ComposedTool
	if err := r.db.WithContext(ctx).
		Where("guild_id = ? AND status = ? AND current_version_id IS NOT NULL", guildID, "enabled").
		Order("name ASC").
		Find(&tools).Error; err != nil {
		return nil, err
	}
	records := make([]ComposedToolRecord, 0, len(tools))
	for _, tool := range tools {
		var version store.ComposedToolVersion
		if err := r.db.WithContext(ctx).First(&version, *tool.CurrentVersionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		records = append(records, ComposedToolRecord{Tool: tool, Version: version})
	}
	return records, nil
}

func (r *ComposedToolRepository) Versions(ctx context.Context, toolID uint) ([]store.ComposedToolVersion, error) {
	var versions []store.ComposedToolVersion
	err := r.db.WithContext(ctx).
		Where("composed_tool_id = ?", toolID).
		Order("version_number DESC").
		Find(&versions).Error
	return versions, err
}

func (r *ComposedToolRepository) CreateRun(ctx context.Context, run store.ComposedToolRun) (store.ComposedToolRun, error) {
	now := time.Now().UTC()
	run.Status = firstNonEmpty(run.Status, "queued")
	run.InputJSON = firstNonEmpty(run.InputJSON, "{}")
	run.OutputJSON = firstNonEmpty(run.OutputJSON, "{}")
	run.TranscriptJSON = firstNonEmpty(run.TranscriptJSON, "[]")
	run.CreatedAt = firstTime(run.CreatedAt, now)
	run.UpdatedAt = firstTime(run.UpdatedAt, now)
	err := r.db.WithContext(ctx).Create(&run).Error
	return run, err
}

func (r *ComposedToolRepository) FinishRun(ctx context.Context, runID uint, status, outputJSON, transcriptJSON, message string, now time.Time) error {
	now = now.UTC()
	return r.db.WithContext(ctx).Model(&store.ComposedToolRun{}).
		Where("id = ?", runID).
		Updates(map[string]any{
			"status":          status,
			"output_json":     firstNonEmpty(outputJSON, "{}"),
			"transcript_json": firstNonEmpty(transcriptJSON, "[]"),
			"error":           strings.TrimSpace(message),
			"finished_at":     now,
			"updated_at":      now,
		}).Error
}

func (r *ComposedToolRepository) StartRun(ctx context.Context, runID uint, now time.Time) error {
	now = now.UTC()
	return r.db.WithContext(ctx).Model(&store.ComposedToolRun{}).
		Where("id = ?", runID).
		Updates(map[string]any{
			"status":        "running",
			"attempt_count": gorm.Expr("attempt_count + 1"),
			"started_at":    now,
			"updated_at":    now,
		}).Error
}

func (r *ComposedToolRepository) RecentRuns(ctx context.Context, guildID, name string, limit int) ([]store.ComposedToolRun, error) {
	limit = clampLimit(limit, 10, 100)
	query := r.db.WithContext(ctx).Model(&store.ComposedToolRun{}).
		Joins("JOIN composed_tools ON composed_tools.id = composed_tool_runs.composed_tool_id").
		Where("composed_tool_runs.guild_id = ?", guildID)
	if strings.TrimSpace(name) != "" {
		query = query.Where("composed_tools.name = ?", name)
	}
	var runs []store.ComposedToolRun
	err := query.Order("composed_tool_runs.created_at DESC, composed_tool_runs.id DESC").Limit(limit).Find(&runs).Error
	return runs, err
}

func (r *ComposedToolRepository) CountRunsSince(ctx context.Context, toolID uint, since time.Time) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&store.ComposedToolRun{}).
		Where("composed_tool_id = ? AND created_at >= ?", toolID, since.UTC()).
		Count(&count).Error
	return count, err
}

func (r *ComposedToolRepository) CountConsecutiveFailures(ctx context.Context, toolID uint, limit int) (int, error) {
	var runs []store.ComposedToolRun
	err := r.db.WithContext(ctx).
		Where("composed_tool_id = ?", toolID).
		Order("created_at DESC, id DESC").
		Limit(clampLimit(limit, 3, 20)).
		Find(&runs).Error
	if err != nil {
		return 0, err
	}
	failures := 0
	for _, run := range runs {
		if run.Status != "failed" && run.Status != "blocked" {
			break
		}
		failures++
	}
	return failures, nil
}

func (r *ComposedToolRepository) LastFinishedRun(ctx context.Context, toolID uint) (store.ComposedToolRun, bool, error) {
	var run store.ComposedToolRun
	err := r.db.WithContext(ctx).
		Where("composed_tool_id = ? AND finished_at IS NOT NULL", toolID).
		Order("finished_at DESC, id DESC").
		First(&run).Error
	if err == nil {
		return run, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.ComposedToolRun{}, false, nil
	}
	return store.ComposedToolRun{}, false, err
}

func (r *ComposedToolRepository) TryDedupe(ctx context.Context, toolID uint, fingerprint string, expiresAt time.Time) (bool, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return true, nil
	}
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("expires_at <= ?", now).Delete(&store.ComposedToolDedupe{}).Error; err != nil {
			return err
		}
		record := store.ComposedToolDedupe{
			ComposedToolID:        toolID,
			InvocationFingerprint: fingerprint,
			ExpiresAt:             expiresAt.UTC(),
			CreatedAt:             now,
		}
		return tx.Create(&record).Error
	})
	if err == nil {
		return true, nil
	}
	if isUniqueConstraintError(err) {
		return false, nil
	}
	return false, err
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	value := strings.ToLower(err.Error())
	return strings.Contains(value, "unique constraint") || strings.Contains(value, "duplicate")
}

func firstPositive(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
