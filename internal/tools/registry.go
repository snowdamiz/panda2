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
	ToolClassWebRead         ToolClass = "web_read"
	ToolClassWorkflow        ToolClass = "workflow"
	ToolClassMetadata        ToolClass = "metadata"
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
	Policy                       string
	Permissions                  map[string]struct{}
	AllowedTools                 map[string]struct{}
	RestrictedTools              map[string]struct{}
	RequireExplicitComposedTools bool
}

type Definition struct {
	Name                  string
	WireName              string
	Description           string
	RequiredPermission    string
	AlternatePermissions  []string
	ToolClass             ToolClass
	InputSchema           json.RawMessage
	OutputSchema          json.RawMessage
	Timeout               time.Duration
	Redaction             RedactionPolicy
	Audit                 AuditPolicy
	IncludeInModelContext bool
	RequiresConfirmation  bool
	SupportsDryRun        bool
	BypassToolPolicy      bool
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
		if !definition.IncludeInModelContext {
			continue
		}
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
	if !access.HasAnyPermission(append([]string{d.RequiredPermission}, d.AlternatePermissions...)...) {
		return false
	}
	if !access.AllowsDefinition(d) {
		return false
	}
	if d.BypassToolPolicy {
		return true
	}
	switch normalizeToolPolicy(access.Policy) {
	case ToolPolicyOff:
		return false
	case ToolPolicyReadOnly:
		return d.ToolClass == ToolClassDiscordRead || d.ToolClass == ToolClassMemory || d.ToolClass == ToolClassWebRead || d.ToolClass == ToolClassMetadata
	case ToolPolicyAssistive:
		return d.ToolClass == ToolClassDiscordRead ||
			d.ToolClass == ToolClassMemory ||
			d.ToolClass == ToolClassWebRead ||
			d.ToolClass == ToolClassWorkflow ||
			d.ToolClass == ToolClassMetadata ||
			(d.ToolClass == ToolClassModerationWrite && d.RequiresConfirmation)
	case ToolPolicyAdminOnly:
		return d.ToolClass == ToolClassWebRead || (hasAdminPolicyAccess(access) && d.ToolClass != ToolClassOwnerOps)
	case ToolPolicyModerator:
		return d.ToolClass == ToolClassDiscordRead ||
			d.ToolClass == ToolClassMemory ||
			d.ToolClass == ToolClassWebRead ||
			d.ToolClass == ToolClassWorkflow ||
			d.ToolClass == ToolClassMetadata ||
			d.ToolClass == ToolClassModerationWrite
	case ToolPolicyWriteConfirmed:
		return d.ToolClass == ToolClassDiscordRead ||
			d.ToolClass == ToolClassMemory ||
			d.ToolClass == ToolClassWebRead ||
			d.ToolClass == ToolClassWorkflow ||
			d.ToolClass == ToolClassMetadata ||
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
			d.ToolClass == ToolClassWebRead ||
			d.ToolClass == ToolClassWorkflow ||
			d.ToolClass == ToolClassMetadata
	default:
		return false
	}
}

func hasAdminPolicyAccess(access ToolAccess) bool {
	return access.HasAnyPermission(
		admin.PermissionAdminConfigRead,
		admin.PermissionAdminConfigWrite,
		admin.PermissionAdminUsageRead,
		admin.PermissionAdminAuditRead,
		admin.PermissionAdminMemoryManage,
		admin.PermissionOwnerOps,
	)
}

func (access ToolAccess) AllowsDefinition(definition Definition) bool {
	return access.allowsTool(false, definition.Name, definition.ModelName())
}

func (access ToolAccess) AllowsComposedTool(names ...string) bool {
	return access.allowsTool(access.RequireExplicitComposedTools, names...)
}

func (access ToolAccess) HasAnyPermission(permissions ...string) bool {
	for _, permission := range permissions {
		permission = strings.TrimSpace(permission)
		if permission == "" {
			continue
		}
		if _, ok := access.Permissions[permission]; ok {
			return true
		}
	}
	return false
}

func (access ToolAccess) allowsTool(requireExplicit bool, names ...string) bool {
	if len(access.RestrictedTools) == 0 && !requireExplicit {
		return true
	}
	restricted := requireExplicit
	for _, name := range names {
		normalized := normalizeToolName(name)
		if normalized == "" {
			continue
		}
		if _, ok := access.AllowedTools[normalized]; ok {
			return true
		}
		if _, ok := access.RestrictedTools[normalized]; ok {
			restricted = true
		}
	}
	return !restricted
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

func normalizeToolName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func DefaultDefinitions() []Definition {
	return []Definition{
		discordRead("discord.fetch_message", "Fetch one Discord message the bot can see, returning content and citation metadata.", []string{"channel_id", "message_id"}, 2*time.Second, 1, "VIEW_CHANNEL", "READ_MESSAGE_HISTORY"),
		discordRead("discord.fetch_messages", "Fetch bounded Discord channel or thread history the bot can see.", []string{"channel_id"}, 3*time.Second, 100, "VIEW_CHANNEL", "READ_MESSAGE_HISTORY"),
		discordRead("discord.fetch_thread_context", "Fetch bounded thread context including parent/starter metadata when available.", []string{"thread_id"}, 3*time.Second, 100, "VIEW_CHANNEL", "READ_MESSAGE_HISTORY"),
		discordRead("discord.fetch_reply_chain", "Walk the referenced-message chain for one message and return cited context.", []string{"channel_id", "message_id"}, 3*time.Second, 10, "VIEW_CHANNEL", "READ_MESSAGE_HISTORY"),
		discordRead("discord.list_pins", "List pinned messages in a channel for canonical channel context.", []string{"channel_id"}, 3*time.Second, 50, "VIEW_CHANNEL", "READ_MESSAGE_HISTORY"),
		discordRead("discord.get_guild", "Read summary metadata for the current Discord guild.", []string{}, 2*time.Second, 1, "VIEW_CHANNEL"),
		discordRead("discord.list_channels", "List guild channels with IDs, names, types, parents, and positions.", []string{}, 3*time.Second, 100, "VIEW_CHANNEL"),
		discordRead("discord.get_channel", "Read one channel's summary metadata.", []string{"channel_id"}, 2*time.Second, 1, "VIEW_CHANNEL"),
		discordRead("discord.list_active_threads", "List active guild threads visible to Panda.", []string{}, 3*time.Second, 100, "VIEW_CHANNEL"),
		discordRead("discord.list_archived_threads", "List archived threads for a channel.", []string{"channel_id"}, 3*time.Second, 100, "VIEW_CHANNEL", "READ_MESSAGE_HISTORY"),
		discordRead("discord.list_roles", "List guild roles with IDs, names, positions, and key flags.", []string{}, 3*time.Second, 250, "VIEW_CHANNEL"),
		discordRead("discord.get_role", "Read one role's summary metadata.", []string{"role_id"}, 2*time.Second, 1, "VIEW_CHANNEL"),
		discordRead("discord.get_member", "Read one guild member summary.", []string{"user_id"}, 3*time.Second, 1, "VIEW_CHANNEL"),
		discordRead("discord.list_scheduled_events", "List guild scheduled events.", []string{}, 3*time.Second, 100, "VIEW_CHANNEL"),
		discordRead("discord.list_emojis", "List guild emojis.", []string{}, 3*time.Second, 100, "VIEW_CHANNEL"),
		discordRead("discord.list_stickers", "List guild stickers.", []string{}, 3*time.Second, 100, "VIEW_CHANNEL"),
		discordRead("discord.list_soundboard_sounds", "List guild soundboard sounds.", []string{}, 3*time.Second, 100, "VIEW_CHANNEL"),
		discordRead("discord.recent_events", "Read Panda's bounded local Discord event cache.", []string{}, 2*time.Second, 100),
		discordRead("discord.channel_activity_summary", "Summarize recent cached Discord activity for one channel.", []string{"channel_id"}, 2*time.Second, 100),
		discordRead("discord.get_poll_answer_voters", "List users who voted for one answer in a native Discord poll.", []string{"channel_id", "message_id", "answer_id"}, 3*time.Second, 100, "VIEW_CHANNEL", "READ_MESSAGE_HISTORY"),
		{
			Name:                  "search_knowledge",
			Description:           "Search guild knowledge.",
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
			Name:                  "web.search",
			Description:           "Search the public web with Brave Search and return ranked URLs, titles, and snippets for current-information answers. Final answers based on this tool should include source links.",
			RequiredPermission:    admin.PermissionAssistantWebSearch,
			ToolClass:             ToolClassWebRead,
			InputSchema:           webSearchSchema(),
			OutputSchema:          objectSchema("results"),
			Timeout:               8 * time.Second,
			Redaction:             RedactContent,
			Audit:                 AuditSensitive,
			IncludeInModelContext: true,
			MaxLimit:              20,
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
			Name:                  "panda.manage_reminder",
			Description:           "Create, list, cancel, complete, or snooze the user's reminders from natural-language reminder requests. Use this for reminders and follow-ups that should notify the user later.",
			RequiredPermission:    admin.PermissionAssistantUse,
			ToolClass:             ToolClassWorkflow,
			InputSchema:           reminderManagementSchema(),
			OutputSchema:          objectSchema("result"),
			Timeout:               5 * time.Second,
			Redaction:             RedactContent,
			Audit:                 AuditOnUse,
			IncludeInModelContext: true,
			SupportsDryRun:        true,
			BypassToolPolicy:      true,
		},
		{
			Name:                  "panda.manage_music",
			Description:           "Play music, inspect the queue, and control playback from natural-language music requests. Use this for requests like play, pause, resume, skip, stop, queue, now playing, loop, shuffle, playlist, and volume.",
			RequiredPermission:    admin.PermissionAssistantUse,
			ToolClass:             ToolClassWorkflow,
			InputSchema:           musicManagementSchema(),
			OutputSchema:          objectSchema("result"),
			Timeout:               20 * time.Second,
			Redaction:             RedactContent,
			Audit:                 AuditOnUse,
			IncludeInModelContext: true,
			BypassToolPolicy:      true,
		},
		{
			Name:                  "panda.list_tools",
			Description:           "Call this before answering questions about what tools or capabilities Panda has. It lists callable tools in the current guild and channel context.",
			RequiredPermission:    admin.PermissionAssistantUse,
			ToolClass:             ToolClassMetadata,
			InputSchema:           toolListSchema(),
			OutputSchema:          objectSchema("tools"),
			Timeout:               time.Second,
			Redaction:             RedactNone,
			Audit:                 AuditNone,
			IncludeInModelContext: true,
			BypassToolPolicy:      true,
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

func toolListSchema() json.RawMessage {
	return schemaWithProperties(nil, map[string]any{
		"kind":            map[string]string{"type": "string", "description": "Optional filter: native, composed, or all."},
		"include_schemas": map[string]string{"type": "boolean", "description": "Include input schemas in the listing."},
	})
}

func composedToolManagementSchema() json.RawMessage {
	return schemaWithProperties([]string{"action"}, map[string]any{
		"action": map[string]any{
			"type":        "string",
			"description": "Action: preview, draft, list, show, approve, pause, resume, disable, archive, run, simulate, export, or rollback.",
		},
		"tool_name":    map[string]string{"type": "string", "description": "Composed tool name."},
		"tool":         map[string]string{"type": "string", "description": "Alias for tool_name."},
		"version":      map[string]any{"type": "integer", "minimum": 1},
		"request":      map[string]string{"type": "string", "description": "Natural-language composed-tool or automation request for draft/preview. Use this when an admin asks Panda to set up a new event-triggered workflow."},
		"description":  map[string]string{"type": "string", "description": "Alias for request."},
		"spec_json":    map[string]string{"type": "string", "description": "Complete composed-tool spec JSON for draft/preview."},
		"role_id":      map[string]string{"type": "string"},
		"role_name":    map[string]string{"type": "string", "description": "Role name to resolve for role-triggered automations."},
		"channel_id":   map[string]string{"type": "string"},
		"channel_name": map[string]string{"type": "string", "description": "Channel name to resolve for message-sending automations."},
		"welcome_text": map[string]string{"type": "string", "description": "Optional message template for welcome-style automations."},
		"input":        map[string]string{"type": "object", "description": "Input object for run/simulate."},
		"input_json":   map[string]string{"type": "string", "description": "JSON object input for run/simulate."},
		"dry_run":      map[string]string{"type": "boolean"},
	})
}

func scheduleManagementSchema() json.RawMessage {
	return schemaWithProperties([]string{"action"}, map[string]any{
		"action": map[string]any{
			"type":        "string",
			"description": "Action: create, list, or cancel.",
		},
		"tool_name": map[string]string{"type": "string", "description": "Approved composed tool name for create, filtered list, or cancel-by-tool."},
		"tool":      map[string]string{"type": "string", "description": "Alias for tool_name."},
		"schedule_id": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"description": "Schedule id for cancel.",
		},
		"id": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"description": "Alias for schedule_id.",
		},
		"when": map[string]string{"type": "string", "description": "When to run, such as RFC3339, 'in 10 minutes', 'tomorrow', or 'every friday'."},
		"in":   map[string]string{"type": "string", "description": "Duration until run, such as '10m' or '2h'."},
		"every": map[string]string{
			"type":        "string",
			"description": "Optional repeat interval such as '24h', 'daily', 'weekly', or 'every day'.",
		},
		"input":            map[string]string{"type": "object", "description": "Input object for the scheduled composed tool run."},
		"input_json":       map[string]string{"type": "string", "description": "JSON object input for the scheduled composed tool run."},
		"include_disabled": map[string]string{"type": "boolean", "description": "Include disabled schedules when listing."},
		"dry_run":          map[string]string{"type": "boolean"},
	})
}

func reminderManagementSchema() json.RawMessage {
	return schemaWithProperties([]string{"action"}, map[string]any{
		"action": map[string]any{
			"type":        "string",
			"description": "Action: create, list, cancel, complete, or snooze.",
		},
		"schedule_id": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"description": "Reminder schedule id for cancel, complete, or snooze.",
		},
		"id": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"description": "Alias for schedule_id.",
		},
		"message": map[string]string{"type": "string", "description": "Reminder text for create."},
		"text":    map[string]string{"type": "string", "description": "Alias for message."},
		"when":    map[string]string{"type": "string", "description": "When to run, such as RFC3339, 'in 10 minutes', 'tomorrow', or 'every friday'."},
		"in":      map[string]string{"type": "string", "description": "Duration until run, such as '10m', '2h', or '10 minutes'."},
		"every": map[string]string{
			"type":        "string",
			"description": "Optional repeat interval such as '24h', 'daily', 'weekly', or 'every day'.",
		},
		"target":           map[string]string{"type": "string", "description": "Target for create: me, user, channel, or role. Defaults to me."},
		"target_id":        map[string]string{"type": "string", "description": "Target user/channel/role id when target is not me."},
		"user_id":          map[string]string{"type": "string", "description": "Alias for target_id when target is user."},
		"channel_id":       map[string]string{"type": "string", "description": "Alias for target_id when target is channel."},
		"role_id":          map[string]string{"type": "string", "description": "Alias for target_id when target is role."},
		"follow_up":        map[string]string{"type": "boolean", "description": "Create a follow-up reminder tied to the current conversation, for requests like follow up if nobody answers."},
		"include_disabled": map[string]string{"type": "boolean", "description": "Include disabled/completed reminders when listing."},
		"dry_run":          map[string]string{"type": "boolean"},
	})
}

func musicManagementSchema() json.RawMessage {
	return schemaWithProperties([]string{"action"}, map[string]any{
		"action": map[string]any{
			"type":        "string",
			"description": "Action: play, pause, resume, skip, stop, queue, clear, now, controls, loop, shuffle, remove, move, vote_skip, settings, or playlist.",
		},
		"query":    map[string]string{"type": "string", "description": "Song/search query for play."},
		"song":     map[string]string{"type": "string", "description": "Alias for query."},
		"track":    map[string]string{"type": "string", "description": "Alias for query."},
		"mode":     map[string]string{"type": "string", "description": "Mode for loop or playlist actions, such as off/track/queue or save/load/list."},
		"name":     map[string]string{"type": "string", "description": "Playlist name for playlist actions."},
		"position": map[string]any{"type": "integer", "minimum": 1, "description": "Queue position for remove or move."},
		"to":       map[string]any{"type": "integer", "minimum": 1, "description": "Destination queue position for move."},
		"volume":   map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "Default music volume for settings."},
	})
}

func webSearchSchema() json.RawMessage {
	return schemaWithProperties([]string{"query"}, map[string]any{
		"query":          map[string]string{"type": "string", "description": "Public web search query. Use search operators like site: or filetype: inside this string when useful."},
		"limit":          map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "description": "Maximum web results to return."},
		"offset":         map[string]any{"type": "integer", "minimum": 0, "maximum": 9, "description": "Zero-based result page offset for pagination."},
		"country":        map[string]string{"type": "string", "description": "Two-letter result country code, default US."},
		"search_lang":    map[string]string{"type": "string", "description": "Search result language code, default en."},
		"ui_lang":        map[string]string{"type": "string", "description": "Response UI language code, default en-US."},
		"safesearch":     map[string]any{"type": "string", "enum": []string{"off", "moderate", "strict"}, "description": "Adult content filtering level."},
		"freshness":      map[string]string{"type": "string", "description": "Optional recency filter: pd, pw, pm, py, or YYYY-MM-DDtoYYYY-MM-DD."},
		"extra_snippets": map[string]string{"type": "boolean", "description": "Request additional snippets per result when available. Defaults to true."},
	})
}

func toolInputSchema(required []string) json.RawMessage {
	properties := map[string]any{}
	for _, name := range required {
		properties[name] = toolInputProperty(name)
	}
	for _, name := range []string{
		"dry_run", "purpose", "limit", "before", "after", "around",
		"include_author_ids", "include_attachments", "allowed_mentions",
		"content", "name", "emoji", "duration", "duration_hours",
		"delete_message_duration", "delete_message_seconds", "nick", "seconds", "locked", "private",
		"archived", "auto_archive_duration", "overwrite_type", "allow",
		"deny", "rule_json", "event_json", "starts_at", "ends_at",
		"description", "entity_type", "location", "status", "max_age",
		"max_uses", "temporary", "unique", "enabled", "channel_ids",
		"author_ids", "user_ids", "message_ids", "role_ids", "webhook_id",
		"keyword_filter", "custom_message", "reason", "answer_emojis",
		"allow_multiselect", "answer_id",
	} {
		if _, exists := properties[name]; !exists {
			properties[name] = toolInputProperty(name)
		}
	}
	return schemaWithProperties(required, properties)
}

func toolInputProperty(name string) any {
	switch name {
	case "dry_run", "include_author_ids", "include_attachments", "locked", "private", "archived", "temporary", "unique", "enabled", "allow_multiselect":
		return map[string]string{"type": "boolean"}
	case "limit", "seconds", "delete_message_seconds", "max_age", "max_uses", "auto_archive_duration", "duration_hours", "answer_id":
		return map[string]any{"type": "integer", "minimum": 0}
	case "answers":
		return map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "minItems": 2, "maxItems": 10}
	case "allowed_mentions", "rule_json", "event_json":
		return map[string]string{"type": "object"}
	case "channel_ids", "author_ids", "user_ids", "message_ids", "role_ids", "keyword_filter", "answer_emojis":
		return map[string]any{"type": "array", "items": map[string]string{"type": "string"}}
	default:
		return map[string]string{"type": "string"}
	}
}

func schemaWithProperties(required []string, properties map[string]any) json.RawMessage {
	if required == nil {
		required = []string{}
	}
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

func threadWrite(name, description string, required []string, permissions ...string) Definition {
	definition := discordWrite(name, description, required, permissions...)
	definition.RequiredPermission = admin.PermissionAssistantUseThreads
	return definition
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
