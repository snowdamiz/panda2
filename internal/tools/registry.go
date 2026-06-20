package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/llm"
)

type RedactionPolicy string

const (
	RedactNone    RedactionPolicy = "none"
	RedactSecrets RedactionPolicy = "secrets"
	RedactContent RedactionPolicy = "content"
)

type AuditPolicy string

const (
	AuditNone      AuditPolicy = "none"
	AuditOnUse     AuditPolicy = "on_use"
	AuditSensitive AuditPolicy = "sensitive"
)

type ToolClass string

const (
	ToolClassDiscordRead     ToolClass = "discord_read"
	ToolClassDiscordWrite    ToolClass = "discord_write"
	ToolClassModerationWrite ToolClass = "moderation_write"
	ToolClassAdminRead       ToolClass = "admin_read"
	ToolClassAdminWrite      ToolClass = "admin_write"
	ToolClassMemory          ToolClass = "memory"
	ToolClassWorkflow        ToolClass = "workflow"
	ToolClassOwnerOps        ToolClass = "owner_ops"
)

const (
	ToolPolicyOff            = "off"
	ToolPolicyReadOnly       = "read_only"
	ToolPolicyAssistive      = "assistive"
	ToolPolicyAdminOnly      = "admin_only"
	ToolPolicyModerator      = "moderator"
	ToolPolicyWriteConfirmed = "write_confirmed"
	ToolPolicyOwnerOps       = "owner_ops"
)

type ToolAccess struct {
	Policy      string
	Permissions map[string]struct{}
}

type Definition struct {
	Name                  string
	WireName              string
	Description           string
	RequiredPermission    string
	ToolClass             ToolClass
	InputSchema           json.RawMessage
	OutputSchema          json.RawMessage
	Timeout               time.Duration
	Redaction             RedactionPolicy
	Audit                 AuditPolicy
	IncludeInModelContext bool
	RequiresConfirmation  bool
	SupportsDryRun        bool
	MaxLimit              int
	DiscordPermissions    []string
}

type Registry struct {
	definitions map[string]Definition
}

var ErrUnknownTool = errors.New("unknown tool")

func NewRegistry(definitions ...Definition) (*Registry, error) {
	registry := &Registry{definitions: map[string]Definition{}}
	for _, definition := range definitions {
		if err := registry.Register(definition); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func NewDefaultRegistry() (*Registry, error) {
	return NewRegistry(DefaultDefinitions()...)
}

func (r *Registry) Register(definition Definition) error {
	if definition.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	if definition.RequiredPermission == "" {
		return fmt.Errorf("tool %s requires a permission", definition.Name)
	}
	if definition.ToolClass == "" {
		return fmt.Errorf("tool %s requires a class", definition.Name)
	}
	if len(definition.InputSchema) == 0 || !json.Valid(definition.InputSchema) {
		return fmt.Errorf("tool %s input schema must be valid JSON", definition.Name)
	}
	if len(definition.OutputSchema) == 0 || !json.Valid(definition.OutputSchema) {
		return fmt.Errorf("tool %s output schema must be valid JSON", definition.Name)
	}
	if definition.Timeout <= 0 {
		return fmt.Errorf("tool %s requires a positive timeout", definition.Name)
	}
	if _, exists := r.definitions[definition.Name]; exists {
		return fmt.Errorf("tool %s already registered", definition.Name)
	}
	r.definitions[definition.Name] = definition
	return nil
}

func (r *Registry) Get(name string) (Definition, bool) {
	definition, ok := r.definitions[name]
	if ok {
		return definition, true
	}
	for _, definition := range r.definitions {
		if definition.ModelName() == name {
			return definition, true
		}
	}
	return Definition{}, false
}

func (r *Registry) MustGet(name string) (Definition, error) {
	definition, ok := r.Get(name)
	if !ok {
		return Definition{}, ErrUnknownTool
	}
	return definition, nil
}

func (r *Registry) Definitions() []Definition {
	definitions := make([]Definition, 0, len(r.definitions))
	for _, definition := range r.definitions {
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool {
		return definitions[i].Name < definitions[j].Name
	})
	return definitions
}

func (r *Registry) OpenRouterTools(permissions map[string]struct{}) []llm.Tool {
	return r.OpenRouterToolsForAccess(ToolAccess{
		Policy:      ToolPolicyAssistive,
		Permissions: permissions,
	})
}

func (r *Registry) OpenRouterToolsForAccess(access ToolAccess) []llm.Tool {
	var result []llm.Tool
	for _, definition := range r.Definitions() {
		if definition.AvailableTo(access) {
			result = append(result, definition.OpenRouterTool())
		}
	}
	return result
}

func (d Definition) ModelName() string {
	if strings.TrimSpace(d.WireName) != "" {
		return strings.TrimSpace(d.WireName)
	}
	return strings.NewReplacer(".", "_").Replace(d.Name)
}

func (d Definition) OpenRouterTool() llm.Tool {
	return llm.Tool{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        d.ModelName(),
			Description: d.Description,
			Parameters:  d.InputSchema,
		},
	}
}

func (d Definition) AvailableTo(access ToolAccess) bool {
	if _, ok := access.Permissions[d.RequiredPermission]; !ok {
		return false
	}
	switch normalizeToolPolicy(access.Policy) {
	case ToolPolicyOff:
		return false
	case ToolPolicyReadOnly:
		return d.ToolClass == ToolClassDiscordRead || d.ToolClass == ToolClassMemory
	case ToolPolicyAssistive:
		return d.ToolClass == ToolClassDiscordRead ||
			d.ToolClass == ToolClassMemory ||
			d.ToolClass == ToolClassWorkflow ||
			(d.ToolClass == ToolClassModerationWrite && d.RequiresConfirmation)
	case ToolPolicyAdminOnly:
		return d.ToolClass == ToolClassAdminRead || d.ToolClass == ToolClassDiscordRead
	case ToolPolicyModerator:
		return d.ToolClass == ToolClassDiscordRead ||
			d.ToolClass == ToolClassMemory ||
			d.ToolClass == ToolClassWorkflow ||
			d.ToolClass == ToolClassModerationWrite
	case ToolPolicyWriteConfirmed:
		return d.ToolClass == ToolClassDiscordRead ||
			d.ToolClass == ToolClassMemory ||
			d.ToolClass == ToolClassWorkflow ||
			d.ToolClass == ToolClassAdminWrite ||
			d.ToolClass == ToolClassDiscordWrite ||
			d.ToolClass == ToolClassModerationWrite
	case ToolPolicyOwnerOps:
		return d.ToolClass == ToolClassOwnerOps ||
			d.ToolClass == ToolClassAdminRead ||
			d.ToolClass == ToolClassAdminWrite ||
			d.ToolClass == ToolClassDiscordRead ||
			d.ToolClass == ToolClassDiscordWrite ||
			d.ToolClass == ToolClassModerationWrite ||
			d.ToolClass == ToolClassMemory ||
			d.ToolClass == ToolClassWorkflow
	default:
		return false
	}
}

func normalizeToolPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case ToolPolicyReadOnly:
		return ToolPolicyReadOnly
	case ToolPolicyAssistive:
		return ToolPolicyAssistive
	case ToolPolicyAdminOnly:
		return ToolPolicyAdminOnly
	case ToolPolicyModerator:
		return ToolPolicyModerator
	case ToolPolicyWriteConfirmed:
		return ToolPolicyWriteConfirmed
	case ToolPolicyOwnerOps:
		return ToolPolicyOwnerOps
	default:
		return ToolPolicyOff
	}
}

func DefaultDefinitions() []Definition {
	definitions := []Definition{
		discordRead("discord.fetch_message", "Fetch one Discord message the bot can see, returning content and citation metadata.", []string{"channel_id", "message_id"}, 2*time.Second, 1, "VIEW_CHANNEL", "READ_MESSAGE_HISTORY"),
		discordRead("discord.fetch_messages", "Fetch bounded Discord channel or thread history the bot can see.", []string{"channel_id"}, 3*time.Second, 100, "VIEW_CHANNEL", "READ_MESSAGE_HISTORY"),
		discordRead("discord.fetch_thread_context", "Fetch bounded thread context including parent/starter metadata when available.", []string{"thread_id"}, 3*time.Second, 100, "VIEW_CHANNEL", "READ_MESSAGE_HISTORY"),
		discordRead("discord.fetch_reply_chain", "Walk the referenced-message chain for one message and return cited context.", []string{"channel_id", "message_id"}, 3*time.Second, 10, "VIEW_CHANNEL", "READ_MESSAGE_HISTORY"),
		discordRead("discord.list_pins", "List pinned messages in a channel for canonical channel context.", []string{"channel_id"}, 3*time.Second, 50, "VIEW_CHANNEL", "READ_MESSAGE_HISTORY"),
		auditRead("discord.search_messages", "Search guild messages using Discord search when available; broad history access is admin-gated.", []string{"guild_id", "query"}, 5*time.Second, 25, "VIEW_CHANNEL", "READ_MESSAGE_HISTORY"),

		discordRead("discord.get_guild", "Read summary metadata for the current Discord guild.", []string{}, 2*time.Second, 1, "VIEW_CHANNEL"),
		discordRead("discord.list_channels", "List guild channels with IDs, names, types, parents, and positions.", []string{}, 3*time.Second, 100, "VIEW_CHANNEL"),
		discordRead("discord.get_channel", "Read one channel's summary metadata.", []string{"channel_id"}, 2*time.Second, 1, "VIEW_CHANNEL"),
		discordRead("discord.list_active_threads", "List active guild threads visible to Panda.", []string{}, 3*time.Second, 100, "VIEW_CHANNEL"),
		discordRead("discord.list_archived_threads", "List archived threads for a channel.", []string{"channel_id"}, 3*time.Second, 100, "VIEW_CHANNEL", "READ_MESSAGE_HISTORY"),
		discordRead("discord.list_roles", "List guild roles with IDs, names, positions, and key flags.", []string{}, 3*time.Second, 250, "VIEW_CHANNEL"),
		discordRead("discord.get_role", "Read one role's summary metadata.", []string{"role_id"}, 2*time.Second, 1, "VIEW_CHANNEL"),
		discordRead("discord.get_member", "Read one guild member summary.", []string{"user_id"}, 3*time.Second, 1, "VIEW_CHANNEL"),
		adminRead("discord.list_members", "List guild members with a hard cap; requires privileged member access in Discord.", []string{}, 4*time.Second, 100, "VIEW_CHANNEL"),
		adminRead("discord.list_bans", "List guild bans. This is an elevated administrative read.", []string{}, 4*time.Second, 100, "BAN_MEMBERS"),
		adminRead("discord.get_invite", "Read one invite by code.", []string{"code"}, 3*time.Second, 1, "MANAGE_GUILD"),
		adminRead("discord.list_invites", "List guild or channel invites. This is an elevated administrative read.", []string{}, 4*time.Second, 100, "MANAGE_GUILD"),
		adminRead("discord.list_webhooks", "List guild or channel webhooks. This is an elevated administrative read.", []string{}, 4*time.Second, 100, "MANAGE_WEBHOOKS"),
		discordRead("discord.list_scheduled_events", "List guild scheduled events.", []string{}, 3*time.Second, 100, "VIEW_CHANNEL"),
		auditRead("discord.get_audit_logs", "Read Discord audit log entries. This is an elevated administrative read.", []string{}, 4*time.Second, 50, "VIEW_AUDIT_LOG"),
		adminRead("discord.list_auto_moderation_rules", "List guild auto-moderation rules. This is an elevated administrative read.", []string{}, 4*time.Second, 100, "MANAGE_GUILD"),
		discordRead("discord.list_emojis", "List guild emojis.", []string{}, 3*time.Second, 100, "VIEW_CHANNEL"),
		discordRead("discord.list_stickers", "List guild stickers.", []string{}, 3*time.Second, 100, "VIEW_CHANNEL"),
		discordRead("discord.list_soundboard_sounds", "List guild soundboard sounds.", []string{}, 3*time.Second, 100, "VIEW_CHANNEL"),
		discordRead("discord.recent_events", "Read Panda's bounded local Discord event cache.", []string{}, 2*time.Second, 100),
		discordRead("discord.channel_activity_summary", "Summarize recent cached Discord activity for one channel.", []string{"channel_id"}, 2*time.Second, 100),

		discordWrite("discord.send_message", "Dry-run or confirmed send of a Discord message with broad mentions suppressed by default.", []string{"channel_id", "content"}, "SEND_MESSAGES"),
		discordWrite("discord.reply_message", "Dry-run or confirmed reply to a visible Discord message with reply mention disabled by default.", []string{"channel_id", "message_id", "content"}, "SEND_MESSAGES", "READ_MESSAGE_HISTORY"),
		discordWrite("discord.edit_own_message", "Dry-run or confirmed edit of a Panda-authored message only.", []string{"channel_id", "message_id", "content"}, "SEND_MESSAGES"),
		discordWrite("discord.delete_own_message", "Dry-run or confirmed delete of a Panda-authored message only.", []string{"channel_id", "message_id"}, "MANAGE_MESSAGES"),
		discordWrite("discord.add_reaction", "Dry-run or confirmed add reaction to a visible message.", []string{"channel_id", "message_id", "emoji"}, "ADD_REACTIONS", "READ_MESSAGE_HISTORY"),
		discordWrite("discord.remove_own_reaction", "Dry-run or confirmed remove Panda's own reaction from a message.", []string{"channel_id", "message_id", "emoji"}, "READ_MESSAGE_HISTORY"),
		discordWrite("discord.create_thread", "Dry-run or confirmed thread creation.", []string{"channel_id", "name"}, "CREATE_PUBLIC_THREADS"),
		discordWrite("discord.rename_thread", "Dry-run or confirmed thread rename.", []string{"thread_id", "name"}, "MANAGE_THREADS"),
		discordWrite("discord.archive_thread", "Dry-run or confirmed thread archive/unarchive.", []string{"thread_id"}, "MANAGE_THREADS"),
		discordWrite("discord.add_thread_member", "Dry-run or confirmed add a member to a thread.", []string{"thread_id", "user_id"}, "MANAGE_THREADS"),
		discordWrite("discord.remove_thread_member", "Dry-run or confirmed remove a member from a thread.", []string{"thread_id", "user_id"}, "MANAGE_THREADS"),
		discordWrite("discord.pin_message", "Dry-run or confirmed pin of a visible message.", []string{"channel_id", "message_id"}, "PIN_MESSAGES"),
		discordWrite("discord.unpin_message", "Dry-run or confirmed unpin of a visible message.", []string{"channel_id", "message_id"}, "PIN_MESSAGES"),

		moderationWrite("discord.timeout_member", "Dry-run or confirmed timeout for a guild member.", []string{"user_id", "duration", "reason"}, "MODERATE_MEMBERS"),
		moderationWrite("discord.remove_timeout", "Dry-run or confirmed timeout removal for a guild member.", []string{"user_id", "reason"}, "MODERATE_MEMBERS"),
		moderationWrite("discord.kick_member", "Dry-run or confirmed kick of a guild member.", []string{"user_id", "reason"}, "KICK_MEMBERS"),
		moderationWrite("discord.ban_member", "Dry-run or confirmed ban of a guild member.", []string{"user_id", "reason"}, "BAN_MEMBERS"),
		moderationWrite("discord.unban_member", "Dry-run or confirmed unban of a user.", []string{"user_id", "reason"}, "BAN_MEMBERS"),
		moderationWrite("discord.bulk_ban_members", "Dry-run or confirmed bulk ban with per-target results and hard caps.", []string{"user_ids", "reason"}, "BAN_MEMBERS"),
		moderationWrite("discord.add_member_role", "Dry-run or confirmed add a role to a member.", []string{"user_id", "role_id", "reason"}, "MANAGE_ROLES"),
		moderationWrite("discord.remove_member_role", "Dry-run or confirmed remove a role from a member.", []string{"user_id", "role_id", "reason"}, "MANAGE_ROLES"),
		moderationWrite("discord.set_member_nick", "Dry-run or confirmed set a member nickname.", []string{"user_id", "nick", "reason"}, "MANAGE_NICKNAMES"),
		moderationWrite("discord.delete_message", "Dry-run or confirmed delete of one message.", []string{"channel_id", "message_id", "reason"}, "MANAGE_MESSAGES"),
		moderationWrite("discord.bulk_delete_messages", "Dry-run or confirmed bulk message delete with a conservative hard cap.", []string{"channel_id", "message_ids", "reason"}, "MANAGE_MESSAGES"),
		moderationWrite("discord.set_channel_slowmode", "Dry-run or confirmed channel slowmode update.", []string{"channel_id", "seconds", "reason"}, "MANAGE_CHANNELS"),
		moderationWrite("discord.lock_thread", "Dry-run or confirmed thread lock/unlock.", []string{"thread_id", "locked", "reason"}, "MANAGE_THREADS"),
		adminWrite("discord.modify_channel_permissions", "Dry-run or confirmed channel permission overwrite update.", []string{"channel_id", "overwrite_id", "reason"}, "MANAGE_CHANNELS"),
		adminWrite("discord.create_auto_moderation_rule", "Dry-run or confirmed auto-moderation rule creation.", []string{"name", "reason"}, "MANAGE_GUILD"),
		adminWrite("discord.update_auto_moderation_rule", "Dry-run or confirmed auto-moderation rule update.", []string{"rule_id", "reason"}, "MANAGE_GUILD"),
		adminWrite("discord.delete_auto_moderation_rule", "Dry-run or confirmed auto-moderation rule deletion.", []string{"rule_id", "reason"}, "MANAGE_GUILD"),
		adminWrite("discord.create_invite", "Dry-run or confirmed invite creation.", []string{"channel_id", "reason"}, "CREATE_INSTANT_INVITE"),
		adminWrite("discord.delete_invite", "Dry-run or confirmed invite deletion.", []string{"code", "reason"}, "MANAGE_GUILD"),
		adminWrite("discord.create_webhook", "Dry-run or confirmed webhook creation.", []string{"channel_id", "name", "reason"}, "MANAGE_WEBHOOKS"),
		adminWrite("discord.update_webhook", "Dry-run or confirmed webhook update.", []string{"webhook_id", "reason"}, "MANAGE_WEBHOOKS"),
		adminWrite("discord.delete_webhook", "Dry-run or confirmed webhook deletion.", []string{"webhook_id", "reason"}, "MANAGE_WEBHOOKS"),
		adminWrite("discord.create_scheduled_event", "Dry-run or confirmed scheduled event creation.", []string{"name", "reason"}, "MANAGE_EVENTS"),
		adminWrite("discord.update_scheduled_event", "Dry-run or confirmed scheduled event update.", []string{"event_id", "reason"}, "MANAGE_EVENTS"),
		adminWrite("discord.delete_scheduled_event", "Dry-run or confirmed scheduled event deletion.", []string{"event_id", "reason"}, "MANAGE_EVENTS"),

		{
			Name:                  "search_knowledge",
			Description:           "Search admin-managed guild knowledge.",
			RequiredPermission:    admin.PermissionAssistantMemoryRead,
			ToolClass:             ToolClassMemory,
			InputSchema:           objectSchema("query", "limit"),
			OutputSchema:          objectSchema("results"),
			Timeout:               2 * time.Second,
			Redaction:             RedactSecrets,
			Audit:                 AuditOnUse,
			IncludeInModelContext: true,
		},
		{
			Name:                  "summarize_text_file",
			Description:           "Summarize extracted text from a safe uploaded file.",
			RequiredPermission:    admin.PermissionAssistantAttachments,
			ToolClass:             ToolClassDiscordRead,
			InputSchema:           objectSchema("attachment_id", "detail"),
			OutputSchema:          objectSchema("summary"),
			Timeout:               10 * time.Second,
			Redaction:             RedactContent,
			Audit:                 AuditOnUse,
			IncludeInModelContext: true,
		},
		{
			Name:                  "draft_moderator_note",
			Description:           "Create a non-destructive draft moderator note from provided context.",
			RequiredPermission:    admin.PermissionModerationUse,
			ToolClass:             ToolClassModerationWrite,
			InputSchema:           objectSchema("context", "tone"),
			OutputSchema:          objectSchema("draft"),
			Timeout:               5 * time.Second,
			Redaction:             RedactSecrets,
			Audit:                 AuditOnUse,
			IncludeInModelContext: false,
		},
		{
			Name:                  "read_config",
			Description:           "Read bot configuration visible to the current user.",
			RequiredPermission:    admin.PermissionAdminConfigRead,
			ToolClass:             ToolClassAdminRead,
			InputSchema:           objectSchema("guild_id"),
			OutputSchema:          objectSchema("config"),
			Timeout:               time.Second,
			Redaction:             RedactSecrets,
			Audit:                 AuditOnUse,
			IncludeInModelContext: true,
		},
		{
			Name:                  "manage_memory_consent",
			Description:           "Read or update the current user's memory consent for this guild.",
			RequiredPermission:    admin.PermissionAssistantUse,
			ToolClass:             ToolClassWorkflow,
			InputSchema:           actionSchema([]string{"action"}, "action", "dry_run"),
			OutputSchema:          objectSchema("result"),
			Timeout:               time.Second,
			Redaction:             RedactSecrets,
			Audit:                 AuditOnUse,
			IncludeInModelContext: true,
		},
		{
			Name:                  "panda.usage_report",
			Description:           "Read Panda usage totals and top usage breakdowns for the current guild.",
			RequiredPermission:    admin.PermissionAdminUsageRead,
			ToolClass:             ToolClassAdminRead,
			InputSchema:           actionSchema(nil, "window", "by", "limit"),
			OutputSchema:          objectSchema("summary", "breakdown"),
			Timeout:               2 * time.Second,
			Redaction:             RedactSecrets,
			Audit:                 AuditOnUse,
			IncludeInModelContext: true,
		},
		{
			Name:                  "panda.manage_budget_limit",
			Description:           "List, set, or prepare removal of Panda request budget windows.",
			RequiredPermission:    admin.PermissionAdminConfigWrite,
			ToolClass:             ToolClassAdminWrite,
			InputSchema:           actionSchema([]string{"action"}, "action", "scope", "subject_id", "limit", "window", "dry_run"),
			OutputSchema:          objectSchema("result"),
			Timeout:               2 * time.Second,
			Redaction:             RedactSecrets,
			Audit:                 AuditOnUse,
			IncludeInModelContext: true,
			RequiresConfirmation:  true,
			SupportsDryRun:        true,
		},
		{
			Name:                  "panda.manage_knowledge",
			Description:           "Enable, disable, add, search, list, export, or prepare deletion of Panda server knowledge.",
			RequiredPermission:    admin.PermissionAdminMemoryManage,
			ToolClass:             ToolClassAdminWrite,
			InputSchema:           actionSchema([]string{"action"}, "action", "title", "content", "query", "document_id", "limit", "dry_run"),
			OutputSchema:          objectSchema("result"),
			Timeout:               4 * time.Second,
			Redaction:             RedactContent,
			Audit:                 AuditOnUse,
			IncludeInModelContext: true,
			RequiresConfirmation:  true,
			SupportsDryRun:        true,
		},
		{
			Name:                  "panda.manage_role_permission",
			Description:           "List, add, or prepare removal of Panda role permissions.",
			RequiredPermission:    admin.PermissionAdminConfigWrite,
			ToolClass:             ToolClassAdminWrite,
			InputSchema:           actionSchema([]string{"action"}, "action", "role_id", "permission", "dry_run"),
			OutputSchema:          objectSchema("result"),
			Timeout:               2 * time.Second,
			Redaction:             RedactSecrets,
			Audit:                 AuditOnUse,
			IncludeInModelContext: true,
			RequiresConfirmation:  true,
			SupportsDryRun:        true,
		},
		{
			Name:                  "panda.manage_channel_rule",
			Description:           "List, allow, deny, or prepare removal of Panda channel allow/deny rules.",
			RequiredPermission:    admin.PermissionAdminConfigWrite,
			ToolClass:             ToolClassAdminWrite,
			InputSchema:           actionSchema([]string{"action"}, "action", "channel_id", "dry_run"),
			OutputSchema:          objectSchema("result"),
			Timeout:               2 * time.Second,
			Redaction:             RedactSecrets,
			Audit:                 AuditOnUse,
			IncludeInModelContext: true,
			RequiresConfirmation:  true,
			SupportsDryRun:        true,
		},
		{
			Name:                  "generate_workflow_json",
			Description:           "Generate structured JSON for command workflows without taking action.",
			RequiredPermission:    admin.PermissionAssistantUse,
			ToolClass:             ToolClassWorkflow,
			InputSchema:           objectSchema("workflow", "inputs"),
			OutputSchema:          objectSchema("json"),
			Timeout:               2 * time.Second,
			Redaction:             RedactSecrets,
			Audit:                 AuditNone,
			IncludeInModelContext: true,
		},
	}
	return definitions
}

func actionSchema(required []string, names ...string) json.RawMessage {
	properties := map[string]any{}
	for _, name := range names {
		switch name {
		case "dry_run":
			properties[name] = map[string]string{"type": "boolean"}
		case "limit":
			properties[name] = map[string]any{"type": "integer", "minimum": 1}
		default:
			properties[name] = map[string]string{"type": "string"}
		}
	}
	return schemaWithProperties(required, properties)
}

func objectSchema(required ...string) json.RawMessage {
	properties := map[string]any{}
	for _, name := range required {
		properties[name] = map[string]string{"type": "string"}
	}
	return schemaWithProperties(required, properties)
}

func toolInputSchema(required []string) json.RawMessage {
	properties := map[string]any{}
	for _, name := range required {
		properties[name] = map[string]string{"type": "string"}
	}
	properties["dry_run"] = map[string]string{"type": "boolean"}
	properties["purpose"] = map[string]string{"type": "string"}
	properties["limit"] = map[string]any{"type": "integer", "minimum": 1}
	properties["before"] = map[string]string{"type": "string"}
	properties["after"] = map[string]string{"type": "string"}
	properties["around"] = map[string]string{"type": "string"}
	properties["include_author_ids"] = map[string]string{"type": "boolean"}
	properties["include_attachments"] = map[string]string{"type": "boolean"}
	properties["reason"] = map[string]string{"type": "string"}
	return schemaWithProperties(required, properties)
}

func schemaWithProperties(required []string, properties map[string]any) json.RawMessage {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             required,
	}
	data, _ := json.Marshal(schema)
	return data
}

func discordRead(name, description string, required []string, timeout time.Duration, maxLimit int, permissions ...string) Definition {
	return Definition{
		Name:                  name,
		Description:           description,
		RequiredPermission:    admin.PermissionAssistantUse,
		ToolClass:             ToolClassDiscordRead,
		InputSchema:           toolInputSchema(required),
		OutputSchema:          objectSchema("result"),
		Timeout:               timeout,
		Redaction:             RedactContent,
		Audit:                 AuditSensitive,
		IncludeInModelContext: true,
		MaxLimit:              maxLimit,
		DiscordPermissions:    permissions,
	}
}

func adminRead(name, description string, required []string, timeout time.Duration, maxLimit int, permissions ...string) Definition {
	definition := discordRead(name, description, required, timeout, maxLimit, permissions...)
	definition.RequiredPermission = admin.PermissionAdminConfigRead
	definition.ToolClass = ToolClassAdminRead
	definition.Redaction = RedactSecrets
	return definition
}

func auditRead(name, description string, required []string, timeout time.Duration, maxLimit int, permissions ...string) Definition {
	definition := adminRead(name, description, required, timeout, maxLimit, permissions...)
	definition.RequiredPermission = admin.PermissionAdminAuditRead
	return definition
}

func discordWrite(name, description string, required []string, permissions ...string) Definition {
	return Definition{
		Name:                  name,
		Description:           description,
		RequiredPermission:    admin.PermissionAssistantUse,
		ToolClass:             ToolClassDiscordWrite,
		InputSchema:           toolInputSchema(required),
		OutputSchema:          objectSchema("result"),
		Timeout:               5 * time.Second,
		Redaction:             RedactContent,
		Audit:                 AuditSensitive,
		RequiresConfirmation:  true,
		SupportsDryRun:        true,
		MaxLimit:              1,
		DiscordPermissions:    permissions,
		IncludeInModelContext: true,
	}
}

func moderationWrite(name, description string, required []string, permissions ...string) Definition {
	definition := discordWrite(name, description, required, permissions...)
	definition.RequiredPermission = admin.PermissionModerationUse
	definition.ToolClass = ToolClassModerationWrite
	return definition
}

func adminWrite(name, description string, required []string, permissions ...string) Definition {
	definition := discordWrite(name, description, required, permissions...)
	definition.RequiredPermission = admin.PermissionAdminConfigWrite
	definition.ToolClass = ToolClassAdminWrite
	return definition
}
