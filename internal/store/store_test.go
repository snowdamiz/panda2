package store

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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
	if count != int64(len(migrations)) {
		t.Fatalf("expected %d migrations, got %d", len(migrations), count)
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

	for _, column := range []string{"assistant_timeout_until", "assistant_timeout_by"} {
		if err := store.DB.Raw("SELECT COUNT(*) FROM pragma_table_info('guild_configs') WHERE name = ?", column).Scan(&tableCount).Error; err != nil {
			t.Fatalf("%s column lookup failed: %v", column, err)
		}
		if tableCount != 1 {
			t.Fatalf("expected guild_configs.%s column, got %d", column, tableCount)
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

	if err := store.DB.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = 'guild_tool_users'").Scan(&tableCount).Error; err != nil {
		t.Fatalf("guild tool users table lookup failed: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("expected guild_tool_users table, got %d", tableCount)
	}

	if err := store.DB.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = 'guild_user_permissions'").Scan(&tableCount).Error; err != nil {
		t.Fatalf("guild user permissions table lookup failed: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("expected guild_user_permissions table, got %d", tableCount)
	}

	for _, table := range []string{"install_intents", "guild_features"} {
		if err := store.DB.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = ?", table).Scan(&tableCount).Error; err != nil {
			t.Fatalf("%s table lookup failed: %v", table, err)
		}
		if tableCount != 1 {
			t.Fatalf("expected %s table, got %d", table, tableCount)
		}
	}

	if err := store.DB.Raw("SELECT COUNT(*) FROM pragma_table_info('guilds') WHERE name = 'feature_flags'").Scan(&tableCount).Error; err != nil {
		t.Fatalf("feature_flags column lookup failed: %v", err)
	}
	if tableCount != 0 {
		t.Fatalf("expected guilds.feature_flags to be removed, got %d", tableCount)
	}

	for _, table := range []string{"schedules", "alert_rules", "feedback_targets", "music_queue_items", "music_playlists", "runtime_statuses", "youtube_clips", "youtube_clip_usages"} {
		if err := store.DB.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = ?", table).Scan(&tableCount).Error; err != nil {
			t.Fatalf("%s table lookup failed: %v", table, err)
		}
		if tableCount != 1 {
			t.Fatalf("expected %s table, got %d", table, tableCount)
		}
	}

}

func TestOpenHardensSQLiteFilePermissions(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "panda.db")
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()

	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat sqlite file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("expected sqlite file mode 0600, got %o", got)
	}
}

func TestExecMigrationStatementSkipsIdempotentAlterWithoutWarnings(t *testing.T) {
	var logs bytes.Buffer
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "idempotent-alter.db")), &gorm.Config{
		Logger: logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Warn}),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE customer_accounts (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE invoice_payment_events (id INTEGER PRIMARY KEY, amount_lamports INTEGER NOT NULL DEFAULT 0)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("seed table: %v", err)
		}
	}
	for _, statement := range []string{
		`ALTER TABLE customer_accounts DROP COLUMN stripe_customer_id`,
		`ALTER TABLE invoice_payment_events ADD COLUMN amount_lamports INTEGER NOT NULL DEFAULT 0`,
	} {
		if err := execMigrationStatement(db, statement); err != nil {
			t.Fatalf("exec idempotent alter %q: %v", statement, err)
		}
	}
	logOutput := logs.String()
	if strings.Contains(logOutput, "no such column") || strings.Contains(logOutput, "duplicate column name") {
		t.Fatalf("expected idempotent alter statements to avoid GORM warnings, got logs:\n%s", logOutput)
	}
}

func TestOpenRunsUsefulnessMigrationWhenLegacyVersionsExist(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	if err := db.Exec(`CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for _, row := range []SchemaMigration{
		{Version: 13, Name: "guild_onboarding_state"},
		{Version: 14, Name: "guided_onboarding_sessions"},
	} {
		if err := db.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, CURRENT_TIMESTAMP)`, row.Version, row.Name).Error; err != nil {
			t.Fatalf("seed legacy migration %d: %v", row.Version, err)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("seed sql db: %v", err)
	}
	_ = sqlDB.Close()

	opened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open legacy db: %v", err)
	}
	defer opened.Close()

	for _, table := range []string{"schedules", "alert_rules", "feedback_targets", "music_queue_items", "music_playlists"} {
		var tableCount int64
		if err := opened.DB.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = ?", table).Scan(&tableCount).Error; err != nil {
			t.Fatalf("%s table lookup failed: %v", table, err)
		}
		if tableCount != 1 {
			t.Fatalf("expected %s table, got %d", table, tableCount)
		}
	}
	var count int64
	if err := opened.DB.Table("schema_migrations").Where("version = ? AND name = ?", 16, "guild_classifier_model").Count(&count).Error; err != nil {
		t.Fatalf("lookup classifier model migration: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected guild_classifier_model migration at version 16, got %d", count)
	}
}

func TestDefaultChannelMessagesMigrationOnlyBackfillsDefaultPresetGuilds(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "features.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	if err := db.Exec(`CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for _, migration := range migrations {
		if migration.Version == 23 {
			continue
		}
		if err := db.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, CURRENT_TIMESTAMP)`, migration.Version, migration.Name).Error; err != nil {
			t.Fatalf("seed migration %d: %v", migration.Version, err)
		}
	}
	if err := db.Exec(`CREATE TABLE guild_features (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		guild_id TEXT NOT NULL,
		feature_id TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		source_install_intent_id TEXT NOT NULL DEFAULT '',
		enabled_by_user_id TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		UNIQUE(guild_id, feature_id)
	)`).Error; err != nil {
		t.Fatalf("create guild_features: %v", err)
	}
	if err := db.Exec(`CREATE TABLE audit_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		guild_id TEXT NOT NULL,
		actor_id TEXT NOT NULL,
		action TEXT NOT NULL,
		target_type TEXT NOT NULL,
		target_id TEXT NOT NULL,
		metadata TEXT NOT NULL,
		created_at DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create audit_events: %v", err)
	}
	for _, row := range []struct {
		guildID string
		source  string
		enabled int
	}{
		{guildID: "legacy-default", source: "migration:default_preset", enabled: 1},
		{guildID: "custom-install", source: "install-intent-1", enabled: 1},
		{guildID: "legacy-disabled", source: "migration:default_preset", enabled: 0},
	} {
		if err := db.Exec(`INSERT INTO guild_features (guild_id, feature_id, enabled, source_install_intent_id, enabled_by_user_id, created_at, updated_at)
			VALUES (?, 'assistant_chat', ?, ?, 'installer-1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, row.guildID, row.enabled, row.source).Error; err != nil {
			t.Fatalf("seed guild feature for %s: %v", row.guildID, err)
		}
	}

	if err := RunMigrations(db); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	var count int64
	if err := db.Table("guild_features").Where("guild_id = ? AND feature_id = ? AND enabled = ?", "legacy-default", "discord_messages", true).Count(&count).Error; err != nil {
		t.Fatalf("query default guild feature: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected default-preset guild to receive discord_messages, got %d", count)
	}
	if err := db.Table("guild_features").Where("guild_id <> ? AND feature_id = ?", "legacy-default", "discord_messages").Count(&count).Error; err != nil {
		t.Fatalf("query non-default guild features: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected custom or disabled guilds to stay unchanged, got %d discord_messages rows", count)
	}
}

func TestLandingDefaultChannelMessagesMigrationBackfillsOldInstallIntents(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "landing-features.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	if err := db.Exec(`CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for _, migration := range migrations {
		if migration.Version == 24 {
			continue
		}
		if err := db.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, CURRENT_TIMESTAMP)`, migration.Version, migration.Name).Error; err != nil {
			t.Fatalf("seed migration %d: %v", migration.Version, err)
		}
	}
	if err := db.Exec(`CREATE TABLE install_intents (
		intent_id TEXT PRIMARY KEY,
		state_hash TEXT NOT NULL,
		selected_feature_ids TEXT NOT NULL DEFAULT '[]',
		expanded_feature_ids TEXT NOT NULL DEFAULT '[]',
		requested_discord_permissions TEXT NOT NULL DEFAULT '[]',
		requested_permission_bitfield TEXT NOT NULL DEFAULT '0',
		granted_discord_permissions TEXT NOT NULL DEFAULT '[]',
		granted_scopes TEXT NOT NULL DEFAULT '[]',
		source TEXT NOT NULL DEFAULT '',
		desired_plan TEXT NOT NULL DEFAULT '',
		referrer TEXT NOT NULL DEFAULT '',
		campaign TEXT NOT NULL DEFAULT '',
		installer_session_metadata TEXT NOT NULL DEFAULT '{}',
		status TEXT NOT NULL DEFAULT 'pending',
		guild_id TEXT NOT NULL DEFAULT '',
		installer_user_id TEXT NOT NULL DEFAULT '',
		expires_at DATETIME NOT NULL,
		consumed_at DATETIME,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create install_intents: %v", err)
	}
	if err := db.Exec(`CREATE TABLE guild_features (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		guild_id TEXT NOT NULL,
		feature_id TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		source_install_intent_id TEXT NOT NULL DEFAULT '',
		enabled_by_user_id TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		UNIQUE(guild_id, feature_id)
	)`).Error; err != nil {
		t.Fatalf("create guild_features: %v", err)
	}
	if err := db.Exec(`CREATE TABLE audit_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		guild_id TEXT NOT NULL,
		actor_id TEXT NOT NULL,
		action TEXT NOT NULL,
		target_type TEXT NOT NULL,
		target_id TEXT NOT NULL,
		metadata TEXT NOT NULL,
		created_at DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create audit_events: %v", err)
	}

	oldLandingDefault := `["admin_access_control","admin_audit","admin_setup","assistant_chat","attachments","composed_tools","knowledge","music","polls","reminders","threads","web_search"]`
	if err := db.Exec(`INSERT INTO install_intents (
		intent_id, state_hash, selected_feature_ids, expanded_feature_ids, requested_discord_permissions,
		requested_permission_bitfield, granted_discord_permissions, granted_scopes, source, status,
		guild_id, installer_user_id, expires_at, consumed_at, created_at, updated_at
	) VALUES (
		'intent-old-default', 'state-old-default', ?, ?, '[]',
		'0', '[]', '[]', 'landing', 'consumed',
		'guild-old-default', 'installer-1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
	)`, oldLandingDefault, oldLandingDefault).Error; err != nil {
		t.Fatalf("seed old landing install intent: %v", err)
	}
	if err := db.Exec(`INSERT INTO install_intents (
		intent_id, state_hash, selected_feature_ids, expanded_feature_ids, requested_discord_permissions,
		requested_permission_bitfield, granted_discord_permissions, granted_scopes, source, status,
		guild_id, installer_user_id, expires_at, consumed_at, created_at, updated_at
	) VALUES (
		'intent-old-default-2', 'state-old-default-2', ?, ?, '[]',
		'0', '[]', '[]', 'landing', 'consumed',
		'guild-old-default', 'installer-1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
	)`, oldLandingDefault, oldLandingDefault).Error; err != nil {
		t.Fatalf("seed duplicate old landing install intent: %v", err)
	}
	if err := db.Exec(`INSERT INTO install_intents (
		intent_id, state_hash, selected_feature_ids, expanded_feature_ids, requested_discord_permissions,
		requested_permission_bitfield, granted_discord_permissions, granted_scopes, source, status,
		guild_id, installer_user_id, expires_at, consumed_at, created_at, updated_at
	) VALUES (
		'intent-custom', 'state-custom', '["assistant_chat"]', '["assistant_chat"]', '[]',
		'0', '[]', '[]', 'landing', 'consumed',
		'guild-custom', 'installer-2', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatalf("seed custom install intent: %v", err)
	}
	for _, row := range []struct {
		guildID  string
		intentID string
	}{
		{guildID: "guild-old-default", intentID: "intent-old-default"},
		{guildID: "guild-custom", intentID: "intent-custom"},
	} {
		if err := db.Exec(`INSERT INTO guild_features (guild_id, feature_id, enabled, source_install_intent_id, enabled_by_user_id, created_at, updated_at)
			VALUES (?, 'assistant_chat', 1, ?, 'installer-1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, row.guildID, row.intentID).Error; err != nil {
			t.Fatalf("seed guild feature for %s: %v", row.guildID, err)
		}
	}

	if err := RunMigrations(db); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	var count int64
	if err := db.Table("guild_features").Where("guild_id = ? AND feature_id = ? AND enabled = ?", "guild-old-default", "discord_messages", true).Count(&count).Error; err != nil {
		t.Fatalf("query old default guild feature: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected old landing default guild to receive discord_messages, got %d", count)
	}
	if err := db.Table("guild_features").Where("guild_id = ? AND feature_id = ?", "guild-custom", "discord_messages").Count(&count).Error; err != nil {
		t.Fatalf("query custom guild feature: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected custom guild to stay unchanged, got %d discord_messages rows", count)
	}
	if err := db.Table("audit_events").Where("guild_id = ? AND action = ?", "guild-old-default", "guild_features.default_enabled").Count(&count).Error; err != nil {
		t.Fatalf("query audit event: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one audit event for old landing default backfill, got %d", count)
	}
}

func TestActiveInstallTrialBackfillCreatesFreeTrialSubscriptions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "trial-backfill.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	if err := runMigrationsBeforeVersion(db, 28); err != nil {
		t.Fatalf("seed prior migrations: %v", err)
	}
	now := time.Date(2026, 6, 24, 19, 0, 0, 0, time.UTC)
	rows := []struct {
		guildID     string
		status      string
		installerID string
		leftAt      *time.Time
	}{
		{guildID: "guild-needs-trial", status: "active", installerID: "installer-1"},
		{guildID: "guild-paid", status: "active", installerID: "installer-2"},
		{guildID: "guild-left", status: "active", installerID: "installer-3", leftAt: &now},
		{guildID: "guild-inactive", status: "inactive", installerID: "installer-4"},
	}
	for _, row := range rows {
		if err := db.Exec(`INSERT INTO guilds (
			guild_id, name, install_status, owner_user_id, installed_by_user_id, locale, joined_at, left_at, created_at, updated_at
		) VALUES (?, ?, ?, 'owner-1', ?, 'en-US', ?, ?, ?, ?)`,
			row.guildID, row.guildID, row.status, row.installerID, now, row.leftAt, now, now).Error; err != nil {
			t.Fatalf("seed guild %s: %v", row.guildID, err)
		}
	}
	if err := db.Exec(`INSERT INTO guild_subscriptions (
		guild_id, customer_account_id, plan, status, grace_state, payment_provider,
		external_subscription_id, external_entitlement_id, billing_owner_user_id,
		current_period_start, current_period_end, trial_ends_at, cancel_at_period_end, created_at, updated_at
	) VALUES (
		'guild-paid', 0, 'plus', 'active', 'active', 'sol',
		'sub-1', 'ent-1', 'installer-2',
		?, ?, NULL, 0, ?, ?
	)`, now, now.Add(30*24*time.Hour), now, now).Error; err != nil {
		t.Fatalf("seed paid subscription: %v", err)
	}

	if err := RunMigrations(db); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	var trial struct {
		Plan               string
		Status             string
		GraceState         string
		PaymentProvider    string
		BillingOwnerUserID string
	}
	if err := db.Raw(`SELECT plan, status, grace_state, payment_provider, billing_owner_user_id
		FROM guild_subscriptions WHERE guild_id = ?`, "guild-needs-trial").Scan(&trial).Error; err != nil {
		t.Fatalf("query backfilled subscription: %v", err)
	}
	if trial.Plan != "trial" || trial.Status != "trialing" || trial.GraceState != "trialing" || trial.PaymentProvider != "trial" || trial.BillingOwnerUserID != "installer-1" {
		t.Fatalf("unexpected backfilled trial: %+v", trial)
	}

	var count int64
	if err := db.Table("customer_accounts").Where("guild_id = ? AND billing_owner_user_id = ?", "guild-needs-trial", "installer-1").Count(&count).Error; err != nil {
		t.Fatalf("query customer account: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected backfilled customer account, got %d", count)
	}
	if err := db.Table("entitlement_snapshots").Where("guild_id = ? AND plan = ? AND status = ? AND ai_responses_limit = ? AND music_enabled = ? AND premium_tools_enabled = ?", "guild-needs-trial", "trial", "trialing", 250, true, true).Count(&count).Error; err != nil {
		t.Fatalf("query entitlement snapshot: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected trial entitlement snapshot, got %d", count)
	}
	if err := db.Table("audit_events").Where("guild_id = ? AND action = ?", "guild-needs-trial", "billing.trial_backfilled").Count(&count).Error; err != nil {
		t.Fatalf("query audit event: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one trial backfill audit event, got %d", count)
	}
	if err := db.Table("guild_subscriptions").Where("guild_id = ? AND plan = ?", "guild-paid", "plus").Count(&count).Error; err != nil {
		t.Fatalf("query paid subscription: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected paid subscription to remain untouched, got %d", count)
	}
	if err := db.Table("guild_subscriptions").Where("guild_id IN ?", []string{"guild-left", "guild-inactive"}).Count(&count).Error; err != nil {
		t.Fatalf("query skipped guilds: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected inactive installs to stay without subscriptions, got %d", count)
	}
}

func TestImageGenerationDefaultMigrationBackfillsExistingAssistantGuilds(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "image-generation-backfill.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	if err := runMigrationsBeforeVersion(db, 32); err != nil {
		t.Fatalf("seed prior migrations: %v", err)
	}
	now := time.Date(2026, 6, 25, 18, 0, 0, 0, time.UTC)
	leftAt := now.Add(-time.Hour)
	guilds := []struct {
		guildID     string
		status      string
		installerID string
		leftAt      *time.Time
	}{
		{guildID: "guild-default", status: "active", installerID: "installer-1"},
		{guildID: "guild-custom", status: "active", installerID: "installer-2"},
		{guildID: "guild-disabled", status: "active", installerID: "installer-3"},
		{guildID: "guild-no-assistant", status: "active", installerID: "installer-4"},
		{guildID: "guild-left", status: "active", installerID: "installer-5", leftAt: &leftAt},
		{guildID: "guild-inactive", status: "inactive", installerID: "installer-6"},
	}
	for _, guild := range guilds {
		if err := db.Exec(`INSERT INTO guilds (
			guild_id, name, install_status, owner_user_id, installed_by_user_id, locale, joined_at, left_at, created_at, updated_at
		) VALUES (?, ?, ?, 'owner-1', ?, 'en-US', ?, ?, ?, ?)`,
			guild.guildID, guild.guildID, guild.status, guild.installerID, now, guild.leftAt, now, now).Error; err != nil {
			t.Fatalf("seed guild %s: %v", guild.guildID, err)
		}
	}
	featureRows := []struct {
		guildID   string
		featureID string
		enabled   int
		source    string
		actor     string
	}{
		{guildID: "guild-default", featureID: "assistant_chat", enabled: 1, source: "migration:default_preset", actor: "installer-1"},
		{guildID: "guild-custom", featureID: "assistant_chat", enabled: 1, source: "intent-custom", actor: "installer-2"},
		{guildID: "guild-disabled", featureID: "assistant_chat", enabled: 1, source: "intent-disabled", actor: "installer-3"},
		{guildID: "guild-disabled", featureID: "image_generation", enabled: 0, source: "admin-disabled", actor: "installer-3"},
		{guildID: "guild-no-assistant", featureID: "web_search", enabled: 1, source: "intent-no-assistant", actor: "installer-4"},
		{guildID: "guild-left", featureID: "assistant_chat", enabled: 1, source: "intent-left", actor: "installer-5"},
		{guildID: "guild-inactive", featureID: "assistant_chat", enabled: 1, source: "intent-inactive", actor: "installer-6"},
	}
	for _, row := range featureRows {
		if err := db.Exec(`INSERT INTO guild_features (guild_id, feature_id, enabled, source_install_intent_id, enabled_by_user_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, row.guildID, row.featureID, row.enabled, row.source, row.actor, now, now).Error; err != nil {
			t.Fatalf("seed feature %s/%s: %v", row.guildID, row.featureID, err)
		}
	}

	if err := RunMigrations(db); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	var count int64
	for _, guildID := range []string{"guild-default", "guild-custom"} {
		if err := db.Table("guild_features").Where("guild_id = ? AND feature_id = ? AND enabled = ?", guildID, "image_generation", true).Count(&count).Error; err != nil {
			t.Fatalf("query backfilled image feature for %s: %v", guildID, err)
		}
		if count != 1 {
			t.Fatalf("expected %s to receive image_generation, got %d", guildID, count)
		}
	}
	if err := db.Table("guild_features").Where("guild_id = ? AND feature_id = ? AND enabled = ?", "guild-disabled", "image_generation", false).Count(&count).Error; err != nil {
		t.Fatalf("query explicitly disabled image feature: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected explicitly disabled image_generation row to stay disabled, got %d", count)
	}
	if err := db.Table("guild_features").Where("guild_id IN ? AND feature_id = ?", []string{"guild-no-assistant", "guild-left", "guild-inactive"}, "image_generation").Count(&count).Error; err != nil {
		t.Fatalf("query skipped guilds: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected guilds without active assistant installs to stay unchanged, got %d image rows", count)
	}
	if err := db.Table("audit_events").Where("action = ? AND guild_id IN ?", "guild_features.default_enabled", []string{"guild-default", "guild-custom"}).Count(&count).Error; err != nil {
		t.Fatalf("query backfill audit events: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected one audit event per backfilled guild, got %d", count)
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
	if err := source.DB.Exec(`INSERT INTO guild_configs (guild_id, created_at, updated_at) VALUES (?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, "guild-1").Error; err != nil {
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

func runMigrationsBeforeVersion(db *gorm.DB, version int) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at DATETIME NOT NULL
		)`).Error; err != nil {
			return err
		}
		for _, migration := range migrations {
			if migration.Version >= version {
				continue
			}
			for _, statement := range migration.SQL {
				if err := execMigrationStatement(tx, statement); err != nil {
					return err
				}
			}
			if err := tx.Create(&SchemaMigration{
				Version:   migration.Version,
				Name:      migration.Name,
				AppliedAt: time.Now().UTC(),
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
