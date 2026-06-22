package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

const (
	DataScopeKnowledge     = "knowledge"
	DataScopeMemory        = "memory"
	DataScopeConversations = "conversations"
	DataScopeBilling       = "billing"
	DataScopeAll           = "all"
)

type GuildDataRepository struct {
	db *gorm.DB
}

type GuildDataSummary struct {
	GuildID                  string           `json:"guild_id"`
	KnowledgeDocuments       int64            `json:"knowledge_documents"`
	KnowledgeChunks          int64            `json:"knowledge_chunks"`
	KnowledgeStorageBytes    int64            `json:"knowledge_storage_bytes"`
	Conversations            int64            `json:"conversations"`
	Messages                 int64            `json:"messages"`
	DiscordEvents            int64            `json:"discord_events"`
	Attachments              int64            `json:"attachments"`
	MemoryConsentRecords     int64            `json:"memory_consent_records"`
	CustomerAccounts         int64            `json:"customer_accounts"`
	Subscriptions            int64            `json:"subscriptions"`
	UsagePeriods             int64            `json:"usage_periods"`
	UsageReservations        int64            `json:"usage_reservations"`
	InvoicePaymentEvents     int64            `json:"invoice_payment_events"`
	CostLedgerEvents         int64            `json:"cost_ledger_events"`
	Schedules                int64            `json:"schedules"`
	AlertRules               int64            `json:"alert_rules"`
	ComposedTools            int64            `json:"composed_tools"`
	ComposedToolRuns         int64            `json:"composed_tool_runs"`
	MusicQueueItems          int64            `json:"music_queue_items"`
	MusicPlaylists           int64            `json:"music_playlists"`
	CurrentSubscriptionPlan  string           `json:"current_subscription_plan,omitempty"`
	CurrentSubscriptionState string           `json:"current_subscription_state,omitempty"`
	Deleted                  map[string]int64 `json:"deleted,omitempty"`
}

func NewGuildDataRepository(db *gorm.DB) *GuildDataRepository {
	return &GuildDataRepository{db: db}
}

func NormalizeDataScope(scope string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(scope))
	switch normalized {
	case "", DataScopeKnowledge:
		return DataScopeKnowledge, true
	case DataScopeMemory, DataScopeConversations, DataScopeBilling, DataScopeAll:
		return normalized, true
	default:
		return "", false
	}
}

func (r *GuildDataRepository) Summary(ctx context.Context, guildID string) (GuildDataSummary, error) {
	guildID = strings.TrimSpace(guildID)
	if guildID == "" {
		return GuildDataSummary{}, fmt.Errorf("guild_id is required")
	}
	summary := GuildDataSummary{GuildID: guildID}
	var err error
	count := func(name string, model any, query string, args ...any) int64 {
		if err != nil {
			return 0
		}
		var value int64
		err = r.db.WithContext(ctx).Model(model).Where(query, args...).Count(&value).Error
		return value
	}

	summary.KnowledgeDocuments = count("knowledge_documents", &store.KnowledgeDocument{}, "guild_id = ?", guildID)
	summary.KnowledgeChunks = count("knowledge_chunks", &store.KnowledgeChunk{}, "guild_id = ?", guildID)
	summary.Conversations = count("conversations", &store.Conversation{}, "guild_id = ?", guildID)
	summary.Messages = count("messages", &store.AssistantMessage{}, "guild_id = ?", guildID)
	summary.DiscordEvents = count("discord_events", &store.DiscordEvent{}, "guild_id = ?", guildID)
	summary.Attachments = count("attachments", &store.Attachment{}, "guild_id = ?", guildID)
	summary.MemoryConsentRecords = count("guild_members", &store.GuildMember{}, "guild_id = ?", guildID)
	summary.CustomerAccounts = count("customer_accounts", &store.CustomerAccount{}, "guild_id = ?", guildID)
	summary.Subscriptions = count("guild_subscriptions", &store.GuildSubscription{}, "guild_id = ?", guildID)
	summary.UsagePeriods = count("usage_periods", &store.UsagePeriod{}, "guild_id = ?", guildID)
	summary.UsageReservations = count("usage_reservations", &store.UsageReservation{}, "guild_id = ?", guildID)
	summary.InvoicePaymentEvents = count("invoice_payment_events", &store.InvoicePaymentEvent{}, "guild_id = ?", guildID)
	summary.CostLedgerEvents = count("cost_ledger_events", &store.CostLedgerEvent{}, "guild_id = ?", guildID)
	summary.Schedules = count("schedules", &store.Schedule{}, "guild_id = ?", guildID)
	summary.AlertRules = count("alert_rules", &store.AlertRule{}, "guild_id = ?", guildID)
	summary.ComposedTools = count("composed_tools", &store.ComposedTool{}, "guild_id = ?", guildID)
	summary.ComposedToolRuns = count("composed_tool_runs", &store.ComposedToolRun{}, "guild_id = ?", guildID)
	summary.MusicQueueItems = count("music_queue_items", &store.MusicQueueItem{}, "guild_id = ?", guildID)
	summary.MusicPlaylists = count("music_playlists", &store.MusicPlaylist{}, "guild_id = ?", guildID)
	if err != nil {
		return GuildDataSummary{}, err
	}
	if err := r.db.WithContext(ctx).Raw(`
		SELECT COALESCE(SUM(length(CAST(chunks.content AS BLOB))), 0)
		FROM knowledge_chunks AS chunks
		INNER JOIN knowledge_documents AS documents ON documents.id = chunks.document_id
		WHERE chunks.guild_id = ? AND documents.enabled = 1
	`, guildID).Scan(&summary.KnowledgeStorageBytes).Error; err != nil {
		return GuildDataSummary{}, err
	}
	var subscription store.GuildSubscription
	if err := r.db.WithContext(ctx).Where("guild_id = ?", guildID).First(&subscription).Error; err == nil {
		summary.CurrentSubscriptionPlan = subscription.Plan
		summary.CurrentSubscriptionState = subscription.Status
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return GuildDataSummary{}, err
	}
	return summary, nil
}

func (r *GuildDataRepository) Delete(ctx context.Context, guildID, scope string) (GuildDataSummary, error) {
	guildID = strings.TrimSpace(guildID)
	if guildID == "" {
		return GuildDataSummary{}, fmt.Errorf("guild_id is required")
	}
	scope, ok := NormalizeDataScope(scope)
	if !ok {
		return GuildDataSummary{}, fmt.Errorf("unsupported data scope")
	}
	deleted := map[string]int64{}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		switch scope {
		case DataScopeKnowledge:
			return deleteKnowledgeData(tx, guildID, deleted)
		case DataScopeMemory:
			return deleteMemoryData(tx, guildID, deleted)
		case DataScopeConversations:
			return deleteConversationData(tx, guildID, deleted)
		case DataScopeBilling:
			return deleteBillingData(tx, guildID, deleted)
		case DataScopeAll:
			if err := deleteKnowledgeData(tx, guildID, deleted); err != nil {
				return err
			}
			if err := deleteMemoryData(tx, guildID, deleted); err != nil {
				return err
			}
			if err := deleteConversationData(tx, guildID, deleted); err != nil {
				return err
			}
			if err := deleteOperationalGuildData(tx, guildID, deleted); err != nil {
				return err
			}
			return deleteBillingData(tx, guildID, deleted)
		default:
			return fmt.Errorf("unsupported data scope")
		}
	})
	if err != nil {
		return GuildDataSummary{}, err
	}
	summary, err := r.Summary(ctx, guildID)
	if err != nil {
		return GuildDataSummary{}, err
	}
	summary.Deleted = deleted
	return summary, nil
}

func deleteKnowledgeData(tx *gorm.DB, guildID string, deleted map[string]int64) error {
	if err := execDelete(tx, deleted, "knowledge_embeddings", `DELETE FROM knowledge_embeddings WHERE chunk_id IN (SELECT id FROM knowledge_chunks WHERE guild_id = ?)`, guildID); err != nil {
		return err
	}
	if err := execDelete(tx, deleted, "knowledge_fts", `DELETE FROM knowledge_fts WHERE guild_id = ?`, guildID); err != nil {
		return err
	}
	if err := deleteModel(tx, deleted, "knowledge_chunks", &store.KnowledgeChunk{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	return deleteModel(tx, deleted, "knowledge_documents", &store.KnowledgeDocument{}, "guild_id = ?", guildID)
}

func deleteMemoryData(tx *gorm.DB, guildID string, deleted map[string]int64) error {
	return deleteModel(tx, deleted, "guild_members", &store.GuildMember{}, "guild_id = ?", guildID)
}

func deleteConversationData(tx *gorm.DB, guildID string, deleted map[string]int64) error {
	if err := deleteModel(tx, deleted, "messages", &store.AssistantMessage{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	if err := deleteModel(tx, deleted, "conversations", &store.Conversation{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	if err := deleteModel(tx, deleted, "discord_events", &store.DiscordEvent{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	if err := deleteModel(tx, deleted, "attachments", &store.Attachment{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	if err := deleteModel(tx, deleted, "feedback_events", &store.FeedbackEvent{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	return deleteModel(tx, deleted, "feedback_targets", &store.FeedbackTarget{}, "guild_id = ?", guildID)
}

func deleteOperationalGuildData(tx *gorm.DB, guildID string, deleted map[string]int64) error {
	if err := deleteModel(tx, deleted, "schedules", &store.Schedule{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	if err := deleteModel(tx, deleted, "alert_rules", &store.AlertRule{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	if err := deleteModel(tx, deleted, "music_queue_items", &store.MusicQueueItem{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	if err := deleteModel(tx, deleted, "music_settings", &store.MusicSettings{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	if err := deleteModel(tx, deleted, "music_playlists", &store.MusicPlaylist{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	if err := deleteModel(tx, deleted, "jobs", &store.Job{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	return deleteComposedData(tx, guildID, deleted)
}

func deleteComposedData(tx *gorm.DB, guildID string, deleted map[string]int64) error {
	if err := execDelete(tx, deleted, "composed_tool_dedupes", `DELETE FROM composed_tool_dedupes WHERE composed_tool_id IN (SELECT id FROM composed_tools WHERE guild_id = ?)`, guildID); err != nil {
		return err
	}
	if err := execDelete(tx, deleted, "composed_tool_runs", `DELETE FROM composed_tool_runs WHERE guild_id = ?`, guildID); err != nil {
		return err
	}
	if err := execDelete(tx, deleted, "composed_tool_versions", `DELETE FROM composed_tool_versions WHERE composed_tool_id IN (SELECT id FROM composed_tools WHERE guild_id = ?)`, guildID); err != nil {
		return err
	}
	return deleteModel(tx, deleted, "composed_tools", &store.ComposedTool{}, "guild_id = ?", guildID)
}

func deleteBillingData(tx *gorm.DB, guildID string, deleted map[string]int64) error {
	if err := deleteModel(tx, deleted, "cost_ledger_events", &store.CostLedgerEvent{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	if err := deleteModel(tx, deleted, "usage_reservations", &store.UsageReservation{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	if err := deleteModel(tx, deleted, "usage_periods", &store.UsagePeriod{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	if err := deleteModel(tx, deleted, "invoice_payment_events", &store.InvoicePaymentEvent{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	if err := deleteModel(tx, deleted, "entitlement_snapshots", &store.EntitlementSnapshot{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	if err := deleteModel(tx, deleted, "guild_subscriptions", &store.GuildSubscription{}, "guild_id = ?", guildID); err != nil {
		return err
	}
	return deleteModel(tx, deleted, "customer_accounts", &store.CustomerAccount{}, "guild_id = ?", guildID)
}

func deleteModel(tx *gorm.DB, deleted map[string]int64, name string, model any, query string, args ...any) error {
	result := tx.Where(query, args...).Delete(model)
	if result.Error != nil {
		return result.Error
	}
	deleted[name] += result.RowsAffected
	return nil
}

func execDelete(tx *gorm.DB, deleted map[string]int64, name, query string, args ...any) error {
	result := tx.Exec(query, args...)
	if result.Error != nil {
		return result.Error
	}
	deleted[name] += result.RowsAffected
	return nil
}

func SortedDeletionKeys(deleted map[string]int64) []string {
	keys := make([]string, 0, len(deleted))
	for key, value := range deleted {
		if value > 0 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
