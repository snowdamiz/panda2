package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenRunsMigrationsAndPragmas(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}

	var foreignKeys int
	if err := store.DB.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error; err != nil {
		t.Fatalf("foreign key pragma query failed: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("expected foreign_keys=1, got %d", foreignKeys)
	}

	var count int64
	if err := store.DB.Table("schema_migrations").Count(&count).Error; err != nil {
		t.Fatalf("schema migration count failed: %v", err)
	}
	if count != 12 {
		t.Fatalf("expected twelve migrations, got %d", count)
	}

	var tableCount int64
	if err := store.DB.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = 'knowledge_fts'").Scan(&tableCount).Error; err != nil {
		t.Fatalf("knowledge search table lookup failed: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("expected knowledge search table, got %d", tableCount)
	}

	if err := store.DB.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = 'discord_events'").Scan(&tableCount).Error; err != nil {
		t.Fatalf("discord events table lookup failed: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("expected discord events table, got %d", tableCount)
	}

	for _, column := range []string{"owner_user_id", "installed_by_user_id"} {
		if err := store.DB.Raw("SELECT COUNT(*) FROM pragma_table_info('guilds') WHERE name = ?", column).Scan(&tableCount).Error; err != nil {
			t.Fatalf("%s column lookup failed: %v", column, err)
		}
		if tableCount != 1 {
			t.Fatalf("expected guilds.%s column, got %d", column, tableCount)
		}
	}

	for _, table := range []string{"composed_tools", "composed_tool_versions", "composed_tool_runs", "composed_tool_dedupes"} {
		if err := store.DB.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = ?", table).Scan(&tableCount).Error; err != nil {
			t.Fatalf("%s table lookup failed: %v", table, err)
		}
		if tableCount != 1 {
			t.Fatalf("expected %s table, got %d", table, tableCount)
		}
	}

	if err := store.DB.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = 'guild_tool_roles'").Scan(&tableCount).Error; err != nil {
		t.Fatalf("guild tool roles table lookup failed: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("expected guild_tool_roles table, got %d", tableCount)
	}
}

func TestBackupCreatesRestorableSQLiteFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.db")
	backupPath := filepath.Join(dir, "backup.db")

	source, err := Open(ctx, sourcePath)
	if err != nil {
		t.Fatalf("Open source: %v", err)
	}
	if err := source.DB.Exec(`INSERT INTO guild_configs (guild_id, default_model, created_at, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, "guild-1", "openrouter/auto").Error; err != nil {
		t.Fatalf("insert fixture: %v", err)
	}
	if err := source.Backup(ctx, backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := source.Optimize(ctx); err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	_ = source.Close()

	backup, err := Open(ctx, backupPath)
	if err != nil {
		t.Fatalf("Open backup: %v", err)
	}
	defer backup.Close()

	var count int64
	if err := backup.DB.Table("guild_configs").Where("guild_id = ?", "guild-1").Count(&count).Error; err != nil {
		t.Fatalf("count backup guild config: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected backed-up guild config, got %d", count)
	}
}
