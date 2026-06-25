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
					memory_enabled INTEGER NOT NULL DEFAULT 1,
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
			`ALTER TABLE guild_configs ADD COLUMN tool_policy TEXT NOT NULL DEFAULT 'admin_only'`,
		},
	},
	{
		Version: 6,
		Name:    "knowledge_embedding_uniqueness",
		SQL: []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_knowledge_embeddings_chunk_model ON knowledge_embeddings(chunk_id, model)`,
		},
	},
	{
		Version: 7,
		Name:    "discord_recent_events",
		SQL: []string{
			`CREATE TABLE IF NOT EXISTS discord_events (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				channel_id TEXT NOT NULL DEFAULT '',
				user_id TEXT NOT NULL DEFAULT '',
				message_id TEXT NOT NULL DEFAULT '',
				event_type TEXT NOT NULL,
				summary TEXT NOT NULL DEFAULT '',
				metadata TEXT NOT NULL DEFAULT '',
				content_preview TEXT NOT NULL DEFAULT '',
				created_at DATETIME NOT NULL,
				expires_at DATETIME
			)`,
			`CREATE INDEX IF NOT EXISTS idx_discord_events_guild_id ON discord_events(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_discord_events_channel_id ON discord_events(channel_id)`,
			`CREATE INDEX IF NOT EXISTS idx_discord_events_user_id ON discord_events(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_discord_events_message_id ON discord_events(message_id)`,
			`CREATE INDEX IF NOT EXISTS idx_discord_events_event_type ON discord_events(event_type)`,
			`CREATE INDEX IF NOT EXISTS idx_discord_events_created_at ON discord_events(created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_discord_events_expires_at ON discord_events(expires_at)`,
		},
	},
	{
		Version: 8,
		Name:    "composed_tools",
		SQL: []string{
			`CREATE TABLE IF NOT EXISTS composed_tools (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				tool_id TEXT NOT NULL,
				current_version_id INTEGER,
				name TEXT NOT NULL,
				status TEXT NOT NULL DEFAULT 'draft',
				visibility TEXT NOT NULL DEFAULT 'guild',
				created_by TEXT NOT NULL,
				approved_by TEXT NOT NULL DEFAULT '',
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(tool_id),
				UNIQUE(guild_id, name)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tools_guild_id ON composed_tools(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tools_current_version_id ON composed_tools(current_version_id)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tools_name ON composed_tools(name)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tools_status ON composed_tools(status)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tools_visibility ON composed_tools(visibility)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tools_created_by ON composed_tools(created_by)`,
			`CREATE TABLE IF NOT EXISTS composed_tool_versions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				composed_tool_id INTEGER NOT NULL,
				version_number INTEGER NOT NULL,
				spec_json TEXT NOT NULL,
				validation_json TEXT NOT NULL DEFAULT '{}',
				tool_definition_json TEXT NOT NULL DEFAULT '{}',
				created_by TEXT NOT NULL,
				approved_by TEXT NOT NULL DEFAULT '',
				approved_at DATETIME,
				created_at DATETIME NOT NULL,
				UNIQUE(composed_tool_id, version_number),
				FOREIGN KEY(composed_tool_id) REFERENCES composed_tools(id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_versions_composed_tool_id ON composed_tool_versions(composed_tool_id)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_versions_created_by ON composed_tool_versions(created_by)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_versions_approved_by ON composed_tool_versions(approved_by)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_versions_approved_at ON composed_tool_versions(approved_at)`,
			`CREATE TABLE IF NOT EXISTS composed_tool_runs (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				composed_tool_id INTEGER NOT NULL,
				version_id INTEGER NOT NULL,
				guild_id TEXT NOT NULL,
				invocation_type TEXT NOT NULL,
				invoking_user_id TEXT NOT NULL DEFAULT '',
				triggering_event_id TEXT NOT NULL DEFAULT '',
				status TEXT NOT NULL DEFAULT 'queued',
				attempt_count INTEGER NOT NULL DEFAULT 0,
				model TEXT NOT NULL DEFAULT '',
				input_json TEXT NOT NULL DEFAULT '{}',
				output_json TEXT NOT NULL DEFAULT '{}',
				transcript_json TEXT NOT NULL DEFAULT '[]',
				error TEXT NOT NULL DEFAULT '',
				started_at DATETIME,
				finished_at DATETIME,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				FOREIGN KEY(composed_tool_id) REFERENCES composed_tools(id) ON DELETE CASCADE,
				FOREIGN KEY(version_id) REFERENCES composed_tool_versions(id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_runs_composed_tool_id ON composed_tool_runs(composed_tool_id)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_runs_version_id ON composed_tool_runs(version_id)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_runs_guild_id ON composed_tool_runs(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_runs_invocation_type ON composed_tool_runs(invocation_type)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_runs_invoking_user_id ON composed_tool_runs(invoking_user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_runs_triggering_event_id ON composed_tool_runs(triggering_event_id)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_runs_status ON composed_tool_runs(status)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_runs_started_at ON composed_tool_runs(started_at)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_runs_finished_at ON composed_tool_runs(finished_at)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_runs_created_at ON composed_tool_runs(created_at)`,
			`CREATE TABLE IF NOT EXISTS composed_tool_dedupes (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				composed_tool_id INTEGER NOT NULL,
				invocation_fingerprint TEXT NOT NULL,
				expires_at DATETIME NOT NULL,
				created_at DATETIME NOT NULL,
				UNIQUE(composed_tool_id, invocation_fingerprint),
				FOREIGN KEY(composed_tool_id) REFERENCES composed_tools(id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_dedupes_composed_tool_id ON composed_tool_dedupes(composed_tool_id)`,
			`CREATE INDEX IF NOT EXISTS idx_composed_tool_dedupes_expires_at ON composed_tool_dedupes(expires_at)`,
		},
	},
	{
		Version: 9,
		Name:    "agent_soul",
		SQL: []string{
			`ALTER TABLE guild_configs ADD COLUMN agent_soul TEXT NOT NULL DEFAULT ''`,
		},
	},
	{
		Version: 10,
		Name:    "guild_install_owner_metadata",
		SQL: []string{
			`ALTER TABLE guilds ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE guilds ADD COLUMN installed_by_user_id TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS idx_guilds_owner_user_id ON guilds(owner_user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guilds_installed_by_user_id ON guilds(installed_by_user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guilds_install_status ON guilds(install_status)`,
		},
	},
	{
		Version: 11,
		Name:    "guild_tool_role_access",
		SQL: []string{
			`CREATE TABLE IF NOT EXISTS guild_tool_roles (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				tool_name TEXT NOT NULL,
				role_id TEXT NOT NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(guild_id, tool_name, role_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_tool_roles_guild_id ON guild_tool_roles(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_tool_roles_tool_name ON guild_tool_roles(tool_name)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_tool_roles_role_id ON guild_tool_roles(role_id)`,
		},
	},
	{
		Version: 12,
		Name:    "admin_only_tools_memory_on_defaults",
		SQL: []string{
			`UPDATE guild_configs
					SET tool_policy = CASE WHEN tool_policy = 'off' THEN 'admin_only' ELSE tool_policy END,
						memory_enabled = CASE WHEN memory_enabled = 0 THEN 1 ELSE memory_enabled END,
						updated_at = CURRENT_TIMESTAMP
					WHERE created_at = updated_at
						AND (tool_policy = 'off' OR memory_enabled = 0)`,
		},
	},
	{
		Version: 15,
		Name:    "bot_usefulness_layer",
		SQL: []string{
			`ALTER TABLE knowledge_documents ADD COLUMN confidence REAL NOT NULL DEFAULT 1`,
			`ALTER TABLE knowledge_documents ADD COLUMN reason_saved TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE knowledge_documents ADD COLUMN source_metadata TEXT NOT NULL DEFAULT '{}'`,
			`ALTER TABLE knowledge_documents ADD COLUMN expires_at DATETIME`,
			`CREATE INDEX IF NOT EXISTS idx_knowledge_documents_expires_at ON knowledge_documents(expires_at)`,
			`CREATE TABLE IF NOT EXISTS schedules (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				channel_id TEXT NOT NULL,
				owner_user_id TEXT NOT NULL,
				kind TEXT NOT NULL,
				status TEXT NOT NULL DEFAULT 'active',
				title TEXT NOT NULL DEFAULT '',
				target_type TEXT NOT NULL DEFAULT 'channel',
				target_id TEXT NOT NULL DEFAULT '',
				schedule_type TEXT NOT NULL DEFAULT 'once',
				timezone TEXT NOT NULL DEFAULT 'UTC',
				interval_seconds INTEGER NOT NULL DEFAULT 0,
				payload TEXT NOT NULL DEFAULT '{}',
				dedupe_key TEXT NOT NULL DEFAULT '',
				next_run_at DATETIME NOT NULL,
				last_run_at DATETIME,
				last_status TEXT NOT NULL DEFAULT '',
				last_error TEXT NOT NULL DEFAULT '',
				last_job_id INTEGER NOT NULL DEFAULT 0,
				run_count INTEGER NOT NULL DEFAULT 0,
				disabled INTEGER NOT NULL DEFAULT 0,
				locked_until DATETIME,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_schedules_guild_id ON schedules(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_schedules_channel_id ON schedules(channel_id)`,
			`CREATE INDEX IF NOT EXISTS idx_schedules_owner_user_id ON schedules(owner_user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_schedules_kind ON schedules(kind)`,
			`CREATE INDEX IF NOT EXISTS idx_schedules_status ON schedules(status)`,
			`CREATE INDEX IF NOT EXISTS idx_schedules_target_type ON schedules(target_type)`,
			`CREATE INDEX IF NOT EXISTS idx_schedules_target_id ON schedules(target_id)`,
			`CREATE INDEX IF NOT EXISTS idx_schedules_schedule_type ON schedules(schedule_type)`,
			`CREATE INDEX IF NOT EXISTS idx_schedules_dedupe_key ON schedules(dedupe_key)`,
			`CREATE INDEX IF NOT EXISTS idx_schedules_next_run_at ON schedules(next_run_at)`,
			`CREATE INDEX IF NOT EXISTS idx_schedules_last_run_at ON schedules(last_run_at)`,
			`CREATE INDEX IF NOT EXISTS idx_schedules_last_status ON schedules(last_status)`,
			`CREATE INDEX IF NOT EXISTS idx_schedules_last_job_id ON schedules(last_job_id)`,
			`CREATE INDEX IF NOT EXISTS idx_schedules_disabled ON schedules(disabled)`,
			`CREATE INDEX IF NOT EXISTS idx_schedules_locked_until ON schedules(locked_until)`,
			`CREATE TABLE IF NOT EXISTS alert_rules (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				pack TEXT NOT NULL,
				channel_id TEXT NOT NULL,
				enabled INTEGER NOT NULL DEFAULT 1,
				cooldown_seconds INTEGER NOT NULL DEFAULT 300,
				pending_count INTEGER NOT NULL DEFAULT 0,
				last_sent_at DATETIME,
				created_by TEXT NOT NULL DEFAULT '',
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(guild_id, pack)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_alert_rules_guild_id ON alert_rules(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_alert_rules_pack ON alert_rules(pack)`,
			`CREATE INDEX IF NOT EXISTS idx_alert_rules_channel_id ON alert_rules(channel_id)`,
			`CREATE INDEX IF NOT EXISTS idx_alert_rules_enabled ON alert_rules(enabled)`,
			`CREATE INDEX IF NOT EXISTS idx_alert_rules_last_sent_at ON alert_rules(last_sent_at)`,
			`CREATE INDEX IF NOT EXISTS idx_alert_rules_created_by ON alert_rules(created_by)`,
			`CREATE TABLE IF NOT EXISTS feedback_targets (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				channel_id TEXT NOT NULL,
				user_id TEXT NOT NULL,
				command TEXT NOT NULL,
				model TEXT NOT NULL DEFAULT '',
				content_hash TEXT NOT NULL DEFAULT '',
				metadata TEXT NOT NULL DEFAULT '{}',
				created_at DATETIME NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_feedback_targets_guild_id ON feedback_targets(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_feedback_targets_channel_id ON feedback_targets(channel_id)`,
			`CREATE INDEX IF NOT EXISTS idx_feedback_targets_user_id ON feedback_targets(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_feedback_targets_command ON feedback_targets(command)`,
			`CREATE INDEX IF NOT EXISTS idx_feedback_targets_model ON feedback_targets(model)`,
			`CREATE INDEX IF NOT EXISTS idx_feedback_targets_content_hash ON feedback_targets(content_hash)`,
			`CREATE INDEX IF NOT EXISTS idx_feedback_targets_created_at ON feedback_targets(created_at)`,
			`CREATE TABLE IF NOT EXISTS feedback_events (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				target_id INTEGER NOT NULL,
				guild_id TEXT NOT NULL,
				user_id TEXT NOT NULL,
				rating TEXT NOT NULL,
				reason TEXT NOT NULL DEFAULT '',
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(target_id, user_id),
				FOREIGN KEY(target_id) REFERENCES feedback_targets(id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_feedback_events_target_id ON feedback_events(target_id)`,
			`CREATE INDEX IF NOT EXISTS idx_feedback_events_guild_id ON feedback_events(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_feedback_events_user_id ON feedback_events(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_feedback_events_rating ON feedback_events(rating)`,
			`CREATE TABLE IF NOT EXISTS music_queue_items (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				position INTEGER NOT NULL,
				track_id TEXT NOT NULL DEFAULT '',
				query TEXT NOT NULL DEFAULT '',
				title TEXT NOT NULL DEFAULT '',
				url TEXT NOT NULL DEFAULT '',
				uploader TEXT NOT NULL DEFAULT '',
				duration_ms INTEGER NOT NULL DEFAULT 0,
				requested_by TEXT NOT NULL DEFAULT '',
				text_channel_id TEXT NOT NULL DEFAULT '',
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(guild_id, position)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_music_queue_items_guild_id ON music_queue_items(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_music_queue_items_position ON music_queue_items(position)`,
			`CREATE INDEX IF NOT EXISTS idx_music_queue_items_requested_by ON music_queue_items(requested_by)`,
			`CREATE INDEX IF NOT EXISTS idx_music_queue_items_text_channel_id ON music_queue_items(text_channel_id)`,
			`CREATE TABLE IF NOT EXISTS music_settings (
				guild_id TEXT PRIMARY KEY,
				loop_mode TEXT NOT NULL DEFAULT 'off',
				default_volume INTEGER NOT NULL DEFAULT 100,
				dj_role_id TEXT NOT NULL DEFAULT '',
				vote_skip_threshold REAL NOT NULL DEFAULT 0.5,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS music_playlists (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				name TEXT NOT NULL,
				created_by TEXT NOT NULL DEFAULT '',
				tracks_json TEXT NOT NULL DEFAULT '[]',
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(guild_id, name)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_music_playlists_guild_id ON music_playlists(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_music_playlists_created_by ON music_playlists(created_by)`,
		},
	},
	{
		Version: 16,
		Name:    "guild_classifier_model",
		SQL: []string{
			`ALTER TABLE guild_configs ADD COLUMN classifier_model TEXT NOT NULL DEFAULT ''`,
		},
	},
	{
		Version: 17,
		Name:    "saas_billing_entitlements_and_model_secrecy",
		SQL: []string{
			`CREATE TABLE IF NOT EXISTS customer_accounts (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				billing_owner_user_id TEXT NOT NULL,
				email TEXT NOT NULL DEFAULT '',
				tax_country TEXT NOT NULL DEFAULT '',
				support_contact TEXT NOT NULL DEFAULT '',
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(guild_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_customer_accounts_billing_owner_user_id ON customer_accounts(billing_owner_user_id)`,
			`CREATE TABLE IF NOT EXISTS guild_subscriptions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				customer_account_id INTEGER NOT NULL DEFAULT 0,
				plan TEXT NOT NULL,
				status TEXT NOT NULL,
				grace_state TEXT NOT NULL,
				payment_provider TEXT NOT NULL DEFAULT 'trial',
				external_subscription_id TEXT NOT NULL DEFAULT '',
				external_entitlement_id TEXT NOT NULL DEFAULT '',
				billing_owner_user_id TEXT NOT NULL DEFAULT '',
				current_period_start DATETIME NOT NULL,
				current_period_end DATETIME NOT NULL,
				trial_ends_at DATETIME,
				cancel_at_period_end INTEGER NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(guild_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_subscriptions_guild_id ON guild_subscriptions(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_subscriptions_customer_account_id ON guild_subscriptions(customer_account_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_subscriptions_plan ON guild_subscriptions(plan)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_subscriptions_status ON guild_subscriptions(status)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_subscriptions_grace_state ON guild_subscriptions(grace_state)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_subscriptions_payment_provider ON guild_subscriptions(payment_provider)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_subscriptions_external_subscription_id ON guild_subscriptions(external_subscription_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_subscriptions_external_entitlement_id ON guild_subscriptions(external_entitlement_id)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_guild_subscriptions_external_subscription_present ON guild_subscriptions(external_subscription_id) WHERE external_subscription_id <> ''`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_guild_subscriptions_external_entitlement_present ON guild_subscriptions(external_entitlement_id) WHERE external_entitlement_id <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_guild_subscriptions_billing_owner_user_id ON guild_subscriptions(billing_owner_user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_subscriptions_current_period_start ON guild_subscriptions(current_period_start)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_subscriptions_current_period_end ON guild_subscriptions(current_period_end)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_subscriptions_trial_ends_at ON guild_subscriptions(trial_ends_at)`,
			`CREATE TABLE IF NOT EXISTS entitlement_snapshots (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				subscription_id INTEGER NOT NULL,
				plan TEXT NOT NULL,
				status TEXT NOT NULL,
				grace_state TEXT NOT NULL,
				ai_responses_limit INTEGER NOT NULL DEFAULT 0,
				web_searches_limit INTEGER NOT NULL DEFAULT 0,
				knowledge_storage_bytes_limit INTEGER NOT NULL DEFAULT 0,
				schedules_limit INTEGER NOT NULL DEFAULT 0,
				retention_days INTEGER NOT NULL DEFAULT 0,
				music_enabled INTEGER NOT NULL DEFAULT 0,
				premium_tools_enabled INTEGER NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL,
				expires_at DATETIME
			)`,
			`CREATE INDEX IF NOT EXISTS idx_entitlement_snapshots_guild_id ON entitlement_snapshots(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_entitlement_snapshots_subscription_id ON entitlement_snapshots(subscription_id)`,
			`CREATE INDEX IF NOT EXISTS idx_entitlement_snapshots_plan ON entitlement_snapshots(plan)`,
			`CREATE INDEX IF NOT EXISTS idx_entitlement_snapshots_status ON entitlement_snapshots(status)`,
			`CREATE INDEX IF NOT EXISTS idx_entitlement_snapshots_grace_state ON entitlement_snapshots(grace_state)`,
			`CREATE INDEX IF NOT EXISTS idx_entitlement_snapshots_created_at ON entitlement_snapshots(created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_entitlement_snapshots_expires_at ON entitlement_snapshots(expires_at)`,
			`CREATE TABLE IF NOT EXISTS invoice_payment_events (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				provider TEXT NOT NULL,
				external_id TEXT NOT NULL,
				guild_id TEXT NOT NULL DEFAULT '',
				subscription_id INTEGER NOT NULL DEFAULT 0,
				amount_cents INTEGER NOT NULL DEFAULT 0,
				amount_lamports INTEGER NOT NULL DEFAULT 0,
				currency TEXT NOT NULL DEFAULT 'usd',
				status TEXT NOT NULL,
				idempotency_key TEXT NOT NULL,
				raw_payload TEXT NOT NULL DEFAULT '{}',
				created_at DATETIME NOT NULL,
				UNIQUE(idempotency_key)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_invoice_payment_events_provider ON invoice_payment_events(provider)`,
			`CREATE INDEX IF NOT EXISTS idx_invoice_payment_events_external_id ON invoice_payment_events(external_id)`,
			`CREATE INDEX IF NOT EXISTS idx_invoice_payment_events_guild_id ON invoice_payment_events(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_invoice_payment_events_subscription_id ON invoice_payment_events(subscription_id)`,
			`CREATE INDEX IF NOT EXISTS idx_invoice_payment_events_status ON invoice_payment_events(status)`,
			`CREATE INDEX IF NOT EXISTS idx_invoice_payment_events_created_at ON invoice_payment_events(created_at)`,
			`CREATE TABLE IF NOT EXISTS usage_periods (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				subscription_id INTEGER NOT NULL,
				plan TEXT NOT NULL,
				period_start DATETIME NOT NULL,
				period_end DATETIME NOT NULL,
				ai_responses_consumed INTEGER NOT NULL DEFAULT 0,
				ai_responses_reserved INTEGER NOT NULL DEFAULT 0,
				web_searches_consumed INTEGER NOT NULL DEFAULT 0,
				web_searches_reserved INTEGER NOT NULL DEFAULT 0,
				knowledge_storage_bytes_consumed INTEGER NOT NULL DEFAULT 0,
				knowledge_storage_bytes_reserved INTEGER NOT NULL DEFAULT 0,
				scheduled_runs_consumed INTEGER NOT NULL DEFAULT 0,
				scheduled_runs_reserved INTEGER NOT NULL DEFAULT 0,
				music_playback_minutes_consumed INTEGER NOT NULL DEFAULT 0,
				music_playback_minutes_reserved INTEGER NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(guild_id, period_start, period_end)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_periods_guild_id ON usage_periods(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_periods_subscription_id ON usage_periods(subscription_id)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_periods_plan ON usage_periods(plan)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_periods_period_start ON usage_periods(period_start)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_periods_period_end ON usage_periods(period_end)`,
			`CREATE TABLE IF NOT EXISTS usage_reservations (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				reservation_id TEXT NOT NULL,
				guild_id TEXT NOT NULL,
				subscription_id INTEGER NOT NULL,
				usage_period_id INTEGER NOT NULL,
				metric TEXT NOT NULL,
				units INTEGER NOT NULL,
				status TEXT NOT NULL,
				expires_at DATETIME NOT NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(reservation_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_reservations_guild_id ON usage_reservations(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_reservations_subscription_id ON usage_reservations(subscription_id)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_reservations_usage_period_id ON usage_reservations(usage_period_id)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_reservations_metric ON usage_reservations(metric)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_reservations_status ON usage_reservations(status)`,
			`CREATE INDEX IF NOT EXISTS idx_usage_reservations_expires_at ON usage_reservations(expires_at)`,
			`CREATE TABLE IF NOT EXISTS cost_ledger_events (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL DEFAULT '',
				request_id TEXT NOT NULL DEFAULT '',
				source TEXT NOT NULL,
				operation TEXT NOT NULL,
				command TEXT NOT NULL DEFAULT '',
				provider TEXT NOT NULL DEFAULT '',
				model TEXT NOT NULL DEFAULT '',
				prompt_tokens INTEGER NOT NULL DEFAULT 0,
				completion_tokens INTEGER NOT NULL DEFAULT 0,
				cached_input_tokens INTEGER NOT NULL DEFAULT 0,
				total_tokens INTEGER NOT NULL DEFAULT 0,
				estimated_cost_micros INTEGER NOT NULL DEFAULT 0,
				final_cost_micros INTEGER NOT NULL DEFAULT 0,
				success INTEGER NOT NULL DEFAULT 0,
				error_code TEXT NOT NULL DEFAULT '',
				created_at DATETIME NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_cost_ledger_events_guild_id ON cost_ledger_events(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_cost_ledger_events_request_id ON cost_ledger_events(request_id)`,
			`CREATE INDEX IF NOT EXISTS idx_cost_ledger_events_source ON cost_ledger_events(source)`,
			`CREATE INDEX IF NOT EXISTS idx_cost_ledger_events_operation ON cost_ledger_events(operation)`,
			`CREATE INDEX IF NOT EXISTS idx_cost_ledger_events_command ON cost_ledger_events(command)`,
			`CREATE INDEX IF NOT EXISTS idx_cost_ledger_events_provider ON cost_ledger_events(provider)`,
			`CREATE INDEX IF NOT EXISTS idx_cost_ledger_events_model ON cost_ledger_events(model)`,
			`CREATE INDEX IF NOT EXISTS idx_cost_ledger_events_created_at ON cost_ledger_events(created_at)`,
			`DROP INDEX IF EXISTS idx_feedback_targets_model`,
			`ALTER TABLE guild_configs DROP COLUMN default_model`,
			`ALTER TABLE guild_configs DROP COLUMN classifier_model`,
			`ALTER TABLE guild_configs DROP COLUMN fallback_models`,
			`ALTER TABLE usage_events DROP COLUMN model`,
			`ALTER TABLE messages DROP COLUMN model`,
			`ALTER TABLE composed_tool_runs DROP COLUMN model`,
			`ALTER TABLE feedback_targets DROP COLUMN model`,
		},
	},
	{
		Version: 18,
		Name:    "billing_customer_identity_cleanup",
		SQL: []string{
			`SELECT 1`,
		},
	},
	{
		Version: 19,
		Name:    "remove_quota_packs",
		SQL: []string{
			`DROP TABLE IF EXISTS quota_packs`,
		},
	},
	{
		Version: 20,
		Name:    "sol_only_payments_and_activation_keys",
		SQL: []string{
			`DROP INDEX IF EXISTS idx_customer_accounts_stripe_customer_present`,
			`ALTER TABLE customer_accounts DROP COLUMN stripe_customer_id`,
			`ALTER TABLE invoice_payment_events ADD COLUMN amount_lamports INTEGER NOT NULL DEFAULT 0`,
			`CREATE TABLE IF NOT EXISTS sol_payment_orders (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				order_id TEXT NOT NULL,
				guild_id TEXT NOT NULL,
				billing_owner_user_id TEXT NOT NULL DEFAULT '',
				support_email TEXT NOT NULL DEFAULT '',
				plan TEXT NOT NULL,
				expected_lamports INTEGER NOT NULL,
				destination_wallet TEXT NOT NULL,
				reference TEXT NOT NULL,
				status TEXT NOT NULL,
				cluster TEXT NOT NULL,
				confirmation_threshold TEXT NOT NULL,
				verified_transaction_signature TEXT NOT NULL DEFAULT '',
				verified_at DATETIME,
				activation_key_revealed_at DATETIME,
				activated_at DATETIME,
				expires_at DATETIME NOT NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(order_id),
				UNIQUE(reference)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_orders_guild_id ON sol_payment_orders(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_orders_billing_owner_user_id ON sol_payment_orders(billing_owner_user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_orders_plan ON sol_payment_orders(plan)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_orders_destination_wallet ON sol_payment_orders(destination_wallet)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_orders_status ON sol_payment_orders(status)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_orders_cluster ON sol_payment_orders(cluster)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_sol_payment_orders_verified_signature ON sol_payment_orders(verified_transaction_signature) WHERE verified_transaction_signature <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_orders_verified_at ON sol_payment_orders(verified_at)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_orders_activation_key_revealed_at ON sol_payment_orders(activation_key_revealed_at)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_orders_activated_at ON sol_payment_orders(activated_at)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_orders_expires_at ON sol_payment_orders(expires_at)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_orders_created_at ON sol_payment_orders(created_at)`,
			`CREATE TABLE IF NOT EXISTS sol_payment_transactions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				signature TEXT NOT NULL,
				order_id TEXT NOT NULL,
				guild_id TEXT NOT NULL,
				payer_wallet TEXT NOT NULL DEFAULT '',
				destination_wallet TEXT NOT NULL DEFAULT '',
				reference TEXT NOT NULL DEFAULT '',
				amount_lamports INTEGER NOT NULL DEFAULT 0,
				confirmation_status TEXT NOT NULL DEFAULT '',
				status TEXT NOT NULL,
				error_message TEXT NOT NULL DEFAULT '',
				raw_payload TEXT NOT NULL DEFAULT '{}',
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(signature)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_transactions_order_id ON sol_payment_transactions(order_id)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_transactions_guild_id ON sol_payment_transactions(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_transactions_payer_wallet ON sol_payment_transactions(payer_wallet)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_transactions_destination_wallet ON sol_payment_transactions(destination_wallet)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_transactions_reference ON sol_payment_transactions(reference)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_transactions_confirmation_status ON sol_payment_transactions(confirmation_status)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_transactions_status ON sol_payment_transactions(status)`,
			`CREATE INDEX IF NOT EXISTS idx_sol_payment_transactions_created_at ON sol_payment_transactions(created_at)`,
			`CREATE TABLE IF NOT EXISTS activation_api_keys (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				key_id TEXT NOT NULL,
				key_hash TEXT NOT NULL,
				key_prefix TEXT NOT NULL,
				payment_order_id TEXT NOT NULL,
				guild_id TEXT NOT NULL,
				plan TEXT NOT NULL,
				status TEXT NOT NULL,
				expires_at DATETIME NOT NULL,
				consumed_at DATETIME,
				consumed_by_discord_user_id TEXT NOT NULL DEFAULT '',
				revoked_at DATETIME,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(key_id),
				UNIQUE(key_hash),
				UNIQUE(payment_order_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_activation_api_keys_key_prefix ON activation_api_keys(key_prefix)`,
			`CREATE INDEX IF NOT EXISTS idx_activation_api_keys_guild_id ON activation_api_keys(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_activation_api_keys_plan ON activation_api_keys(plan)`,
			`CREATE INDEX IF NOT EXISTS idx_activation_api_keys_status ON activation_api_keys(status)`,
			`CREATE INDEX IF NOT EXISTS idx_activation_api_keys_expires_at ON activation_api_keys(expires_at)`,
			`CREATE INDEX IF NOT EXISTS idx_activation_api_keys_consumed_at ON activation_api_keys(consumed_at)`,
			`CREATE INDEX IF NOT EXISTS idx_activation_api_keys_consumed_by_discord_user_id ON activation_api_keys(consumed_by_discord_user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_activation_api_keys_revoked_at ON activation_api_keys(revoked_at)`,
			`CREATE INDEX IF NOT EXISTS idx_activation_api_keys_created_at ON activation_api_keys(created_at)`,
		},
	},
	{
		Version: 21,
		Name:    "neutral_billing_orders_and_coupons",
		SQL: []string{
			`CREATE TABLE IF NOT EXISTS billing_orders (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				order_id TEXT NOT NULL,
				guild_id TEXT NOT NULL,
				billing_owner_user_id TEXT NOT NULL DEFAULT '',
				support_email TEXT NOT NULL DEFAULT '',
				plan TEXT NOT NULL,
				provider TEXT NOT NULL DEFAULT 'sol',
				list_lamports INTEGER NOT NULL,
				discount_lamports INTEGER NOT NULL DEFAULT 0,
				due_lamports INTEGER NOT NULL,
				coupon_id TEXT NOT NULL DEFAULT '',
				coupon_prefix TEXT NOT NULL DEFAULT '',
				destination_wallet TEXT NOT NULL DEFAULT '',
				reference TEXT NOT NULL,
				status TEXT NOT NULL,
				cluster TEXT NOT NULL DEFAULT '',
				confirmation_threshold TEXT NOT NULL DEFAULT '',
				verified_transaction_signature TEXT NOT NULL DEFAULT '',
				verified_at DATETIME,
				activation_key_revealed_at DATETIME,
				activated_at DATETIME,
				expires_at DATETIME NOT NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(order_id),
				UNIQUE(reference)
			)`,
			`INSERT OR IGNORE INTO billing_orders (
				order_id,
				guild_id,
				billing_owner_user_id,
				support_email,
				plan,
				provider,
				list_lamports,
				discount_lamports,
				due_lamports,
				destination_wallet,
				reference,
				status,
				cluster,
				confirmation_threshold,
				verified_transaction_signature,
				verified_at,
				activation_key_revealed_at,
				activated_at,
				expires_at,
				created_at,
				updated_at
			)
			SELECT
				order_id,
				guild_id,
				billing_owner_user_id,
				support_email,
				plan,
				'sol',
				expected_lamports,
				0,
				expected_lamports,
				destination_wallet,
				reference,
				status,
				cluster,
				confirmation_threshold,
				verified_transaction_signature,
				verified_at,
				activation_key_revealed_at,
				activated_at,
				expires_at,
				created_at,
				updated_at
			FROM sol_payment_orders`,
			`CREATE INDEX IF NOT EXISTS idx_billing_orders_guild_id ON billing_orders(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_orders_billing_owner_user_id ON billing_orders(billing_owner_user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_orders_plan ON billing_orders(plan)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_orders_provider ON billing_orders(provider)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_orders_coupon_id ON billing_orders(coupon_id)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_orders_coupon_prefix ON billing_orders(coupon_prefix)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_orders_destination_wallet ON billing_orders(destination_wallet)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_orders_status ON billing_orders(status)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_orders_cluster ON billing_orders(cluster)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_orders_verified_signature ON billing_orders(verified_transaction_signature) WHERE verified_transaction_signature <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_billing_orders_verified_at ON billing_orders(verified_at)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_orders_activation_key_revealed_at ON billing_orders(activation_key_revealed_at)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_orders_activated_at ON billing_orders(activated_at)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_orders_expires_at ON billing_orders(expires_at)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_orders_created_at ON billing_orders(created_at)`,
			`ALTER TABLE activation_api_keys RENAME COLUMN payment_order_id TO billing_order_id`,
			`CREATE INDEX IF NOT EXISTS idx_activation_api_keys_billing_order_id ON activation_api_keys(billing_order_id)`,
			`CREATE TABLE IF NOT EXISTS billing_coupons (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				coupon_id TEXT NOT NULL,
				code_hash TEXT NOT NULL,
				code_prefix TEXT NOT NULL,
				plan TEXT NOT NULL,
				discount_lamports INTEGER NOT NULL,
				max_redemptions INTEGER NOT NULL DEFAULT 0,
				status TEXT NOT NULL,
				owner_note TEXT NOT NULL DEFAULT '',
				created_by_user_id TEXT NOT NULL DEFAULT '',
				expires_at DATETIME,
				revoked_at DATETIME,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(coupon_id),
				UNIQUE(code_hash)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupons_code_prefix ON billing_coupons(code_prefix)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupons_plan ON billing_coupons(plan)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupons_status ON billing_coupons(status)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupons_created_by_user_id ON billing_coupons(created_by_user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupons_expires_at ON billing_coupons(expires_at)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupons_revoked_at ON billing_coupons(revoked_at)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupons_created_at ON billing_coupons(created_at)`,
			`CREATE TABLE IF NOT EXISTS billing_coupon_redemptions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				redemption_id TEXT NOT NULL,
				coupon_id TEXT NOT NULL,
				order_id TEXT NOT NULL,
				guild_id TEXT NOT NULL,
				billing_owner_user_id TEXT NOT NULL DEFAULT '',
				plan TEXT NOT NULL,
				list_lamports INTEGER NOT NULL,
				discount_lamports INTEGER NOT NULL,
				due_lamports INTEGER NOT NULL,
				status TEXT NOT NULL,
				expires_at DATETIME NOT NULL,
				consumed_at DATETIME,
				released_at DATETIME,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(redemption_id),
				UNIQUE(order_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupon_redemptions_coupon_id ON billing_coupon_redemptions(coupon_id)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupon_redemptions_guild_id ON billing_coupon_redemptions(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupon_redemptions_billing_owner_user_id ON billing_coupon_redemptions(billing_owner_user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupon_redemptions_plan ON billing_coupon_redemptions(plan)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupon_redemptions_status ON billing_coupon_redemptions(status)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupon_redemptions_expires_at ON billing_coupon_redemptions(expires_at)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupon_redemptions_consumed_at ON billing_coupon_redemptions(consumed_at)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupon_redemptions_released_at ON billing_coupon_redemptions(released_at)`,
			`CREATE INDEX IF NOT EXISTS idx_billing_coupon_redemptions_created_at ON billing_coupon_redemptions(created_at)`,
			`DROP TABLE IF EXISTS sol_payment_orders`,
		},
	},
	{
		Version: 22,
		Name:    "install_intents_and_guild_features",
		SQL: []string{
			`CREATE TABLE IF NOT EXISTS install_intents (
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
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_install_intents_state_hash ON install_intents(state_hash)`,
			`CREATE INDEX IF NOT EXISTS idx_install_intents_source ON install_intents(source)`,
			`CREATE INDEX IF NOT EXISTS idx_install_intents_status ON install_intents(status)`,
			`CREATE INDEX IF NOT EXISTS idx_install_intents_guild_id ON install_intents(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_install_intents_installer_user_id ON install_intents(installer_user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_install_intents_expires_at ON install_intents(expires_at)`,
			`CREATE TABLE IF NOT EXISTS guild_features (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				feature_id TEXT NOT NULL,
				enabled INTEGER NOT NULL DEFAULT 1,
				source_install_intent_id TEXT NOT NULL DEFAULT '',
				enabled_by_user_id TEXT NOT NULL DEFAULT '',
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(guild_id, feature_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_features_guild_id ON guild_features(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_features_feature_id ON guild_features(feature_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_features_enabled ON guild_features(enabled)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_features_source_install_intent_id ON guild_features(source_install_intent_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_features_enabled_by_user_id ON guild_features(enabled_by_user_id)`,
			`INSERT OR IGNORE INTO guild_features (guild_id, feature_id, enabled, source_install_intent_id, enabled_by_user_id, created_at, updated_at)
				SELECT guild_id, 'assistant_chat', 1, 'migration:default_preset', installed_by_user_id, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM guilds WHERE install_status = 'active'`,
			`INSERT OR IGNORE INTO guild_features (guild_id, feature_id, enabled, source_install_intent_id, enabled_by_user_id, created_at, updated_at)
				SELECT guild_id, 'polls', 1, 'migration:default_preset', installed_by_user_id, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM guilds WHERE install_status = 'active'`,
			`INSERT OR IGNORE INTO guild_features (guild_id, feature_id, enabled, source_install_intent_id, enabled_by_user_id, created_at, updated_at)
				SELECT guild_id, 'reminders', 1, 'migration:default_preset', installed_by_user_id, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM guilds WHERE install_status = 'active'`,
			`INSERT OR IGNORE INTO guild_features (guild_id, feature_id, enabled, source_install_intent_id, enabled_by_user_id, created_at, updated_at)
				SELECT guild_id, 'web_search', 1, 'migration:default_preset', installed_by_user_id, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM guilds WHERE install_status = 'active'`,
			`INSERT OR IGNORE INTO guild_features (guild_id, feature_id, enabled, source_install_intent_id, enabled_by_user_id, created_at, updated_at)
				SELECT guild_id, 'knowledge', 1, 'migration:default_preset', installed_by_user_id, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM guilds WHERE install_status = 'active'`,
			`INSERT OR IGNORE INTO guild_features (guild_id, feature_id, enabled, source_install_intent_id, enabled_by_user_id, created_at, updated_at)
				SELECT guild_id, 'attachments', 1, 'migration:default_preset', installed_by_user_id, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM guilds WHERE install_status = 'active'`,
			`INSERT OR IGNORE INTO guild_features (guild_id, feature_id, enabled, source_install_intent_id, enabled_by_user_id, created_at, updated_at)
				SELECT guild_id, 'music', 1, 'migration:default_preset', installed_by_user_id, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM guilds WHERE install_status = 'active'`,
			`INSERT INTO audit_events (guild_id, actor_id, action, target_type, target_id, metadata, created_at)
				SELECT guild_id, installed_by_user_id, 'guild_features.backfill', 'guild', guild_id,
					'{"source":"migration:default_preset","features":["assistant_chat","polls","reminders","web_search","knowledge","attachments","music"]}',
					CURRENT_TIMESTAMP
				FROM guilds
				WHERE install_status = 'active'`,
			`ALTER TABLE guilds DROP COLUMN feature_flags`,
		},
	},
	{
		Version: 23,
		Name:    "default_server_channel_messages",
		SQL: []string{
			`INSERT OR IGNORE INTO guild_features (guild_id, feature_id, enabled, source_install_intent_id, enabled_by_user_id, created_at, updated_at)
				SELECT DISTINCT guild_id, 'discord_messages', 1, 'migration:default_preset', enabled_by_user_id, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
				FROM guild_features
				WHERE source_install_intent_id = 'migration:default_preset'
					AND enabled = 1`,
			`INSERT INTO audit_events (guild_id, actor_id, action, target_type, target_id, metadata, created_at)
				SELECT DISTINCT guild_id, enabled_by_user_id, 'guild_features.default_enabled', 'guild', guild_id,
					'{"source":"migration:default_preset","features":["discord_messages"]}',
					CURRENT_TIMESTAMP
				FROM guild_features
				WHERE source_install_intent_id = 'migration:default_preset'
					AND feature_id = 'discord_messages'
					AND enabled = 1`,
		},
	},
	{
		Version: 24,
		Name:    "default_server_channel_messages_for_landing_defaults",
		SQL: []string{
			`INSERT INTO audit_events (guild_id, actor_id, action, target_type, target_id, metadata, created_at)
				SELECT intent.guild_id, intent.installer_user_id, 'guild_features.default_enabled', 'guild', intent.guild_id,
					'{"source":"migration:landing_default_preset","features":["discord_messages"]}',
					CURRENT_TIMESTAMP
				FROM (
					SELECT guild_id, MIN(installer_user_id) AS installer_user_id
					FROM install_intents
					WHERE status = 'consumed'
						AND guild_id <> ''
						AND expanded_feature_ids = '["admin_access_control","admin_audit","admin_setup","assistant_chat","attachments","composed_tools","knowledge","music","polls","reminders","threads","web_search"]'
					GROUP BY guild_id
				) intent
				WHERE intent.guild_id <> ''
					AND NOT EXISTS (
						SELECT 1 FROM guild_features existing
						WHERE existing.guild_id = intent.guild_id
							AND existing.feature_id = 'discord_messages'
					)`,
			`INSERT OR IGNORE INTO guild_features (guild_id, feature_id, enabled, source_install_intent_id, enabled_by_user_id, created_at, updated_at)
				SELECT intent.guild_id, 'discord_messages', 1, intent.intent_id, intent.installer_user_id, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
				FROM (
					SELECT guild_id, MIN(intent_id) AS intent_id, MIN(installer_user_id) AS installer_user_id
					FROM install_intents
					WHERE status = 'consumed'
						AND guild_id <> ''
						AND expanded_feature_ids = '["admin_access_control","admin_audit","admin_setup","assistant_chat","attachments","composed_tools","knowledge","music","polls","reminders","threads","web_search"]'
					GROUP BY guild_id
				) intent
				WHERE intent.guild_id <> ''`,
		},
	},
	{
		Version: 28,
		Name:    "active_install_trial_backfill",
		SQL: []string{
			`INSERT INTO audit_events (guild_id, actor_id, action, target_type, target_id, metadata, created_at)
				SELECT guild_id, installed_by_user_id, 'billing.trial_backfilled', 'guild', guild_id,
					'{"source":"migration:active_install_trial_backfill","plan":"trial","status":"trialing"}',
					CURRENT_TIMESTAMP
				FROM guilds
				WHERE guild_id <> ''
					AND install_status = 'active'
					AND left_at IS NULL
					AND NOT EXISTS (
						SELECT 1 FROM guild_subscriptions existing
						WHERE existing.guild_id = guilds.guild_id
					)`,
			`INSERT OR IGNORE INTO customer_accounts (
				guild_id,
				billing_owner_user_id,
				email,
				tax_country,
				support_contact,
				created_at,
				updated_at
			)
				SELECT guild_id, installed_by_user_id, '', '', '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
				FROM guilds
				WHERE guild_id <> ''
					AND install_status = 'active'
					AND left_at IS NULL
					AND NOT EXISTS (
						SELECT 1 FROM guild_subscriptions existing
						WHERE existing.guild_id = guilds.guild_id
					)`,
			`INSERT OR IGNORE INTO guild_subscriptions (
				guild_id,
				customer_account_id,
				plan,
				status,
				grace_state,
				payment_provider,
				external_subscription_id,
				external_entitlement_id,
				billing_owner_user_id,
				current_period_start,
				current_period_end,
				trial_ends_at,
				cancel_at_period_end,
				created_at,
				updated_at
			)
				SELECT
					guilds.guild_id,
					COALESCE(customer_accounts.id, 0),
					'trial',
					'trialing',
					'trialing',
					'trial',
					'',
					'',
					COALESCE(NULLIF(customer_accounts.billing_owner_user_id, ''), guilds.installed_by_user_id),
					CURRENT_TIMESTAMP,
					datetime(CURRENT_TIMESTAMP, '+14 days'),
					datetime(CURRENT_TIMESTAMP, '+14 days'),
					0,
					CURRENT_TIMESTAMP,
					CURRENT_TIMESTAMP
				FROM guilds
				LEFT JOIN customer_accounts ON customer_accounts.guild_id = guilds.guild_id
				WHERE guilds.guild_id <> ''
					AND guilds.install_status = 'active'
					AND guilds.left_at IS NULL
					AND NOT EXISTS (
						SELECT 1 FROM guild_subscriptions existing
						WHERE existing.guild_id = guilds.guild_id
					)`,
			`INSERT INTO entitlement_snapshots (
				guild_id,
				subscription_id,
				plan,
				status,
				grace_state,
				ai_responses_limit,
				web_searches_limit,
				knowledge_storage_bytes_limit,
				schedules_limit,
				retention_days,
				music_enabled,
				premium_tools_enabled,
				created_at,
				expires_at
			)
				SELECT
					subscriptions.guild_id,
					subscriptions.id,
					'trial',
					'trialing',
					'trialing',
					250,
					20,
					26214400,
					3,
					14,
					1,
					1,
					CURRENT_TIMESTAMP,
					NULL
				FROM guild_subscriptions subscriptions
				INNER JOIN guilds ON guilds.guild_id = subscriptions.guild_id
				WHERE guilds.install_status = 'active'
					AND guilds.left_at IS NULL
					AND subscriptions.plan = 'trial'
					AND subscriptions.status = 'trialing'
					AND subscriptions.payment_provider = 'trial'
					AND NOT EXISTS (
						SELECT 1 FROM entitlement_snapshots existing
						WHERE existing.subscription_id = subscriptions.id
			)`,
		},
	},
	{
		Version: 29,
		Name:    "remove_legacy_tool_policy_off",
		SQL: []string{
			`CREATE TABLE guild_configs_rebuilt (
				guild_id TEXT PRIMARY KEY,
				temperature REAL NOT NULL DEFAULT 0.3,
				max_response_tokens INTEGER NOT NULL DEFAULT 900,
				tool_policy TEXT NOT NULL DEFAULT 'admin_only',
				system_prompt_overlay TEXT NOT NULL DEFAULT '',
				agent_soul TEXT NOT NULL DEFAULT '',
				assistant_enabled INTEGER NOT NULL DEFAULT 1,
				memory_enabled INTEGER NOT NULL DEFAULT 1,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`,
			`INSERT INTO guild_configs_rebuilt (
				guild_id,
				temperature,
				max_response_tokens,
				tool_policy,
				system_prompt_overlay,
				agent_soul,
				assistant_enabled,
				memory_enabled,
				created_at,
				updated_at
			)
				SELECT
					guild_id,
					temperature,
					max_response_tokens,
					CASE WHEN tool_policy = 'off' THEN 'admin_only' ELSE tool_policy END,
					system_prompt_overlay,
					agent_soul,
					assistant_enabled,
					CASE WHEN memory_enabled = 0 THEN 1 ELSE memory_enabled END,
					created_at,
					CURRENT_TIMESTAMP
				FROM guild_configs`,
			`DROP TABLE guild_configs`,
			`ALTER TABLE guild_configs_rebuilt RENAME TO guild_configs`,
		},
	},
	{
		Version: 30,
		Name:    "guild_user_permissions",
		SQL: []string{
			`CREATE TABLE IF NOT EXISTS guild_user_permissions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				guild_id TEXT NOT NULL,
				user_id TEXT NOT NULL,
				permission TEXT NOT NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				UNIQUE(guild_id, user_id, permission)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_user_permissions_guild_id ON guild_user_permissions(guild_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_user_permissions_user_id ON guild_user_permissions(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_guild_user_permissions_permission ON guild_user_permissions(permission)`,
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
	if isAddColumnStatement(statement) && isDuplicateColumnError(err) {
		return nil
	}
	if isDropColumnStatement(statement) && isMissingColumnError(err) {
		return nil
	}
	if isKnowledgeFTS5Statement(statement) && strings.Contains(strings.ToLower(err.Error()), "no such module: fts5") {
		return createFallbackKnowledgeSearchTable(tx)
	}
	return err
}

func isAddColumnStatement(statement string) bool {
	normalized := strings.ToLower(strings.TrimSpace(statement))
	return strings.HasPrefix(normalized, "alter table ") && strings.Contains(normalized, " add column ")
}

func isDropColumnStatement(statement string) bool {
	normalized := strings.ToLower(strings.TrimSpace(statement))
	return strings.HasPrefix(normalized, "alter table ") && strings.Contains(normalized, " drop column ")
}

func isDuplicateColumnError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

func isMissingColumnError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such column") || strings.Contains(message, "has no column named")
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
