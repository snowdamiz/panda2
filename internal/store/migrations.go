package store

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

type Migration struct {
	Version int
	Name    string
	SQL     []string
}

var migrations = []Migration{
	{
		Version: 1,
		Name:    "initial_core_tables",
		SQL: []string{
			`CREATE TABLE IF NOT EXISTS schema_migrations (
				version INTEGER PRIMARY KEY,
				name TEXT NOT NULL,
				applied_at DATETIME NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS guild_configs (
				guild_id TEXT PRIMARY KEY,
				default_model TEXT NOT NULL,
				system_prompt_overlay TEXT NOT NULL DEFAULT '',
				assistant_enabled INTEGER NOT NULL DEFAULT 1,
				memory_enabled INTEGER NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS usage_events (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT,
				user_id TEXT,
				channel_id TEXT,
				command TEXT NOT NULL,
				model TEXT NOT NULL DEFAULT '',
				prompt_tokens INTEGER NOT NULL DEFAULT 0,
				completion_tokens INTEGER NOT NULL DEFAULT 0,
				total_tokens INTEGER NOT NULL DEFAULT 0,
				success INTEGER NOT NULL,
				error_code TEXT NOT NULL DEFAULT '',
				latency_ms INTEGER NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_events_guild_id ON usage_events(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_events_user_id ON usage_events(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_events_channel_id ON usage_events(channel_id)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_events_command ON usage_events(command)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_events_created_at ON usage_events(created_at)`,
		},
	},
	{
		Version: 2,
		Name:    "v1_operational_tables",
		SQL: []string{
			`CREATE TABLE IF NOT EXISTS guilds (
				guild_id TEXT PRIMARY KEY,
				name TEXT NOT NULL DEFAULT '',
				install_status TEXT NOT NULL DEFAULT 'active',
				locale TEXT NOT NULL DEFAULT '',
				feature_flags TEXT NOT NULL DEFAULT '',
				joined_at DATETIME NOT NULL,
				left_at DATETIME,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS guild_roles (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				role_id TEXT NOT NULL,
				permission TEXT NOT NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(guild_id, role_id, permission)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_roles_guild_id ON guild_roles(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_roles_role_id ON guild_roles(role_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_roles_permission ON guild_roles(permission)`,
			`CREATE TABLE IF NOT EXISTS users (
				user_id TEXT PRIMARY KEY,
				username TEXT NOT NULL DEFAULT '',
				global_opt TEXT NOT NULL DEFAULT '',
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS guild_members (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				user_id TEXT NOT NULL,
				memory_consent INTEGER NOT NULL DEFAULT 0,
				assistant_allowed INTEGER NOT NULL DEFAULT 1,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(guild_id, user_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_members_guild_id ON guild_members(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_members_user_id ON guild_members(user_id)`,
			`CREATE TABLE IF NOT EXISTS conversations (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				channel_id TEXT NOT NULL,
				thread_id TEXT NOT NULL DEFAULT '',
				owner_user_id TEXT NOT NULL,
				title TEXT NOT NULL DEFAULT '',
				status TEXT NOT NULL DEFAULT 'active',
				retention_days INTEGER NOT NULL DEFAULT 30,
				last_message_at DATETIME NOT NULL,
				expires_at DATETIME,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_conversations_guild_id ON conversations(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_conversations_channel_id ON conversations(channel_id)`,
			`CREATE INDEX IF NOT EXISTS idx_conversations_thread_id ON conversations(thread_id)`,
			`CREATE INDEX IF NOT EXISTS idx_conversations_owner_user_id ON conversations(owner_user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_conversations_status ON conversations(status)`,
			`CREATE INDEX IF NOT EXISTS idx_conversations_last_message_at ON conversations(last_message_at)`,
			`CREATE INDEX IF NOT EXISTS idx_conversations_expires_at ON conversations(expires_at)`,
			`CREATE TABLE IF NOT EXISTS messages (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				conversation_id INTEGER NOT NULL,
				guild_id TEXT NOT NULL,
				channel_id TEXT NOT NULL,
				user_id TEXT NOT NULL,
				discord_message_id TEXT NOT NULL DEFAULT '',
				role TEXT NOT NULL,
				content_hash TEXT NOT NULL DEFAULT '',
				content_preview TEXT NOT NULL DEFAULT '',
				model TEXT NOT NULL DEFAULT '',
				prompt_tokens INTEGER NOT NULL DEFAULT 0,
				completion_tokens INTEGER NOT NULL DEFAULT 0,
				total_tokens INTEGER NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL,
				FOREIGN KEY(conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_conversation_id ON messages(conversation_id)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_guild_id ON messages(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_channel_id ON messages(channel_id)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_user_id ON messages(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_discord_message_id ON messages(discord_message_id)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_role ON messages(role)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_created_at ON messages(created_at)`,
			`CREATE TABLE IF NOT EXISTS knowledge_documents (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				title TEXT NOT NULL,
				source TEXT NOT NULL DEFAULT 'admin',
				created_by TEXT NOT NULL DEFAULT '',
				enabled INTEGER NOT NULL DEFAULT 1,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_knowledge_documents_guild_id ON knowledge_documents(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_knowledge_documents_created_by ON knowledge_documents(created_by)`,
			`CREATE TABLE IF NOT EXISTS knowledge_chunks (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				document_id INTEGER NOT NULL,
				guild_id TEXT NOT NULL,
				ordinal INTEGER NOT NULL,
				content TEXT NOT NULL,
				content_hash TEXT NOT NULL,
				created_at DATETIME NOT NULL,
				UNIQUE(document_id, ordinal),
				FOREIGN KEY(document_id) REFERENCES knowledge_documents(id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_document_id ON knowledge_chunks(document_id)`,
			`CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_guild_id ON knowledge_chunks(guild_id)`,
			`CREATE VIRTUAL TABLE IF NOT EXISTS knowledge_fts USING fts5(
				guild_id UNINDEXED,
				document_id UNINDEXED,
				chunk_id UNINDEXED,
				title,
				content,
				tokenize = 'porter unicode61'
			)`,
			`CREATE TABLE IF NOT EXISTS knowledge_embeddings (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				chunk_id INTEGER NOT NULL,
				model TEXT NOT NULL,
				vector TEXT NOT NULL,
				created_at DATETIME NOT NULL,
				FOREIGN KEY(chunk_id) REFERENCES knowledge_chunks(id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_knowledge_embeddings_chunk_id ON knowledge_embeddings(chunk_id)`,
			`CREATE INDEX IF NOT EXISTS idx_knowledge_embeddings_model ON knowledge_embeddings(model)`,
			`CREATE TABLE IF NOT EXISTS attachments (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				channel_id TEXT NOT NULL,
				message_id TEXT NOT NULL,
				filename TEXT NOT NULL,
				content_type TEXT NOT NULL DEFAULT '',
				size_bytes INTEGER NOT NULL DEFAULT 0,
				extracted_text TEXT NOT NULL DEFAULT '',
				temp_path TEXT NOT NULL DEFAULT '',
				cleanup_after DATETIME,
				cleanup_done_at DATETIME,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_attachments_guild_id ON attachments(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_attachments_channel_id ON attachments(channel_id)`,
			`CREATE INDEX IF NOT EXISTS idx_attachments_message_id ON attachments(message_id)`,
			`CREATE INDEX IF NOT EXISTS idx_attachments_cleanup_after ON attachments(cleanup_after)`,
			`CREATE TABLE IF NOT EXISTS rate_limit_buckets (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				scope TEXT NOT NULL,
				bucket_key TEXT NOT NULL,
				count INTEGER NOT NULL DEFAULT 0,
				limit_count INTEGER NOT NULL DEFAULT 0,
				window_start DATETIME NOT NULL,
				window_end DATETIME NOT NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(scope, bucket_key, window_start)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_rate_limit_buckets_scope ON rate_limit_buckets(scope)`,
			`CREATE INDEX IF NOT EXISTS idx_rate_limit_buckets_bucket_key ON rate_limit_buckets(bucket_key)`,
			`CREATE INDEX IF NOT EXISTS idx_rate_limit_buckets_window_start ON rate_limit_buckets(window_start)`,
			`CREATE INDEX IF NOT EXISTS idx_rate_limit_buckets_window_end ON rate_limit_buckets(window_end)`,
			`CREATE TABLE IF NOT EXISTS audit_events (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				actor_id TEXT NOT NULL,
				action TEXT NOT NULL,
				target_type TEXT NOT NULL DEFAULT '',
				target_id TEXT NOT NULL DEFAULT '',
				metadata TEXT NOT NULL DEFAULT '',
				created_at DATETIME NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_audit_events_guild_id ON audit_events(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_audit_events_actor_id ON audit_events(actor_id)`,
			`CREATE INDEX IF NOT EXISTS idx_audit_events_action ON audit_events(action)`,
			`CREATE INDEX IF NOT EXISTS idx_audit_events_created_at ON audit_events(created_at)`,
			`CREATE TABLE IF NOT EXISTS jobs (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				kind TEXT NOT NULL,
				status TEXT NOT NULL DEFAULT 'queued',
				guild_id TEXT NOT NULL DEFAULT '',
				payload TEXT NOT NULL DEFAULT '',
				attempts INTEGER NOT NULL DEFAULT 0,
				max_attempts INTEGER NOT NULL DEFAULT 3,
				lock_owner TEXT NOT NULL DEFAULT '',
				lease_expires_at DATETIME,
				last_error TEXT NOT NULL DEFAULT '',
				run_after DATETIME NOT NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_jobs_kind ON jobs(kind)`,
			`CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status)`,
			`CREATE INDEX IF NOT EXISTS idx_jobs_guild_id ON jobs(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_jobs_lock_owner ON jobs(lock_owner)`,
			`CREATE INDEX IF NOT EXISTS idx_jobs_lease_expires_at ON jobs(lease_expires_at)`,
			`CREATE INDEX IF NOT EXISTS idx_jobs_run_after ON jobs(run_after)`,
		},
	},
	{
		Version: 3,
		Name:    "guild_access_rules",
		SQL: []string{
			`CREATE TABLE IF NOT EXISTS guild_channel_rules (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				channel_id TEXT NOT NULL,
				rule TEXT NOT NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(guild_id, channel_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_channel_rules_guild_id ON guild_channel_rules(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_channel_rules_channel_id ON guild_channel_rules(channel_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_channel_rules_rule ON guild_channel_rules(rule)`,
		},
	},
	{
		Version: 4,
		Name:    "budget_limits",
		SQL: []string{
			`CREATE TABLE IF NOT EXISTS budget_limits (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL DEFAULT '',
				scope TEXT NOT NULL,
				subject_id TEXT NOT NULL DEFAULT '',
				limit_count INTEGER NOT NULL,
				window_seconds INTEGER NOT NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(guild_id, scope, subject_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_budget_limits_guild_id ON budget_limits(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_budget_limits_scope ON budget_limits(scope)`,
			`CREATE INDEX IF NOT EXISTS idx_budget_limits_subject_id ON budget_limits(subject_id)`,
		},
	},
	{
		Version: 5,
		Name:    "guild_model_runtime_settings",
		SQL: []string{
			`ALTER TABLE guild_configs ADD COLUMN fallback_models TEXT NOT NULL DEFAULT '[]'`,
			`ALTER TABLE guild_configs ADD COLUMN temperature REAL NOT NULL DEFAULT 0.3`,
			`ALTER TABLE guild_configs ADD COLUMN max_response_tokens INTEGER NOT NULL DEFAULT 900`,
			`ALTER TABLE guild_configs ADD COLUMN tool_policy TEXT NOT NULL DEFAULT 'off'`,
		},
	},
	{
		Version: 6,
		Name:    "knowledge_embedding_uniqueness",
		SQL: []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_knowledge_embeddings_chunk_model ON knowledge_embeddings(chunk_id, model)`,
		},
	},
}

func RunMigrations(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at DATETIME NOT NULL
		)`).Error; err != nil {
			return err
		}

		for _, migration := range migrations {
			var count int64
			if err := tx.Table("schema_migrations").Where("version = ?", migration.Version).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
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

func execMigrationStatement(tx *gorm.DB, statement string) error {
	if isKnowledgeFTS5Statement(statement) && !sqliteSupportsFTS5(tx) {
		return createFallbackKnowledgeSearchTable(tx)
	}

	err := tx.Exec(statement).Error
	if err == nil {
		return nil
	}
	if isKnowledgeFTS5Statement(statement) && strings.Contains(strings.ToLower(err.Error()), "no such module: fts5") {
		return createFallbackKnowledgeSearchTable(tx)
	}
	return err
}

func isKnowledgeFTS5Statement(statement string) bool {
	normalized := strings.ToLower(strings.TrimSpace(statement))
	return strings.HasPrefix(normalized, "create virtual table") && strings.Contains(normalized, "knowledge_fts")
}

func sqliteSupportsFTS5(tx *gorm.DB) bool {
	var options []struct {
		CompileOptions string `gorm:"column:compile_options"`
	}
	if err := tx.Raw("PRAGMA compile_options").Scan(&options).Error; err != nil {
		return false
	}
	for _, option := range options {
		if strings.EqualFold(option.CompileOptions, "ENABLE_FTS5") {
			return true
		}
	}
	return false
}

func createFallbackKnowledgeSearchTable(tx *gorm.DB) error {
	if err := tx.Exec(`CREATE TABLE IF NOT EXISTS knowledge_fts (
		guild_id TEXT NOT NULL,
		document_id INTEGER NOT NULL,
		chunk_id INTEGER NOT NULL,
		title TEXT NOT NULL,
		content TEXT NOT NULL
	)`).Error; err != nil {
		return err
	}
	if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_knowledge_fts_fallback_guild_id ON knowledge_fts(guild_id)`).Error; err != nil {
		return err
	}
	return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_knowledge_fts_fallback_document_id ON knowledge_fts(document_id)`).Error
}
