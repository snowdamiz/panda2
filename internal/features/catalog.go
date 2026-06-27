package features

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/sn0w/panda2/internal/admin"
)

const (
	AssistantChat         = "assistant_chat"
	Threads               = "threads"
	Polls                 = "polls"
	Reminders             = "reminders"
	Music                 = "music"
	Knowledge             = "knowledge"
	Attachments           = "attachments"
	WebSearch             = "web_search"
	ImageGeneration       = "image_generation"
	AdminSetup            = "admin_setup"
	AdminAccessControl    = "admin_access_control"
	AdminAudit            = "admin_audit"
	ComposedTools         = "composed_tools"
	DiscordMessages       = "discord_messages"
	DiscordMessageActions = "discord_message_actions"
	DiscordRoleManagement = "discord_role_management"
	DiscordChannelTools   = "discord_channel_tools"
	DiscordWebhooks       = "discord_webhooks"
	DiscordInvitesEvents  = "discord_invites_events"
	ModerationAssist      = "moderation_assist"
	OwnerOps              = "owner_ops"
)

var (
	ErrUnknownFeature       = errors.New("unknown feature")
	ErrInternalFeature      = errors.New("feature is not selectable in public install")
	ErrUnknownPermission    = errors.New("unknown Discord permission")
	defaultInstallPresetIDs = []string{AssistantChat, Threads, Polls, Reminders, WebSearch, ImageGeneration, Knowledge, Attachments, Music, AdminSetup, AdminAccessControl, AdminAudit, ComposedTools, DiscordMessages}
	defaultInstallScopes    = []string{"bot", "applications.commands", "identify", "guilds"}
)

type Feature struct {
	ID                    string   `json:"id"`
	Label                 string   `json:"label"`
	Description           string   `json:"description"`
	DiscordPermissions    []string `json:"discord_permissions"`
	PandaPermissions      []string `json:"panda_permissions"`
	ToolNames             []string `json:"tool_names,omitempty"`
	RequiresGatewayIntent bool     `json:"requires_gateway_intent"`
	ConsumesPlanQuota     bool     `json:"consumes_plan_quota"`
	RequiresConfirmation  bool     `json:"requires_confirmation"`
	Public                bool     `json:"public"`
	Dependencies          []string `json:"dependencies,omitempty"`
}

type Selection struct {
	SelectedFeatureIDs          []string `json:"selected_feature_ids"`
	ExpandedFeatureIDs          []string `json:"expanded_feature_ids"`
	DiscordPermissionNames      []string `json:"discord_permission_names"`
	DiscordPermissionBitfield   string   `json:"discord_permission_bitfield"`
	DiscordPermissionBitfield64 int64    `json:"-"`
	Scopes                      []string `json:"scopes"`
}

func Catalog() []Feature {
	return cloneFeatures(catalog)
}

func PublicCatalog() []Feature {
	result := []Feature{}
	for _, feature := range catalog {
		if feature.Public {
			result = append(result, cloneFeature(feature))
		}
	}
	return result
}

func DefaultInstallPreset() []string {
	return append([]string(nil), defaultInstallPresetIDs...)
}

func DefaultInstallScopes() []string {
	return append([]string(nil), defaultInstallScopes...)
}

func Lookup(id string) (Feature, bool) {
	feature, ok := catalogByID[normalizeID(id)]
	if !ok {
		return Feature{}, false
	}
	return cloneFeature(feature), true
}

func Calculate(selected []string, publicOnly bool) (Selection, error) {
	normalized, err := Normalize(selected, publicOnly)
	if err != nil {
		return Selection{}, err
	}
	expanded, err := Expand(normalized, publicOnly)
	if err != nil {
		return Selection{}, err
	}
	permissions, err := PermissionNamesForFeatures(expanded)
	if err != nil {
		return Selection{}, err
	}
	bits, err := PermissionBitfield(permissions)
	if err != nil {
		return Selection{}, err
	}
	return Selection{
		SelectedFeatureIDs:          normalized,
		ExpandedFeatureIDs:          expanded,
		DiscordPermissionNames:      permissions,
		DiscordPermissionBitfield:   strconv.FormatInt(bits, 10),
		DiscordPermissionBitfield64: bits,
		Scopes:                      DefaultInstallScopes(),
	}, nil
}

func Normalize(ids []string, publicOnly bool) ([]string, error) {
	seen := map[string]struct{}{}
	result := []string{}
	for _, id := range ids {
		id = normalizeID(id)
		if id == "" {
			continue
		}
		feature, ok := catalogByID[id]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknownFeature, id)
		}
		if publicOnly && !feature.Public {
			return nil, fmt.Errorf("%w: %s", ErrInternalFeature, id)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("at least one feature is required")
	}
	sort.Strings(result)
	return result, nil
}

func Expand(ids []string, publicOnly bool) ([]string, error) {
	expanded := map[string]struct{}{}
	var visit func(string) error
	visit = func(id string) error {
		id = normalizeID(id)
		if _, ok := expanded[id]; ok {
			return nil
		}
		feature, ok := catalogByID[id]
		if !ok {
			return fmt.Errorf("%w: %s", ErrUnknownFeature, id)
		}
		if publicOnly && !feature.Public {
			return fmt.Errorf("%w: %s", ErrInternalFeature, id)
		}
		expanded[id] = struct{}{}
		for _, dependency := range feature.Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		return nil
	}
	for _, id := range ids {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	result := make([]string, 0, len(expanded))
	for id := range expanded {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func PermissionNamesForFeatures(ids []string) ([]string, error) {
	seen := map[string]struct{}{}
	for _, id := range ids {
		feature, ok := catalogByID[normalizeID(id)]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknownFeature, id)
		}
		for _, permission := range feature.DiscordPermissions {
			permission = normalizePermissionName(permission)
			if permission == "" {
				continue
			}
			if _, ok := permissionValues[permission]; !ok {
				return nil, fmt.Errorf("%w: %s", ErrUnknownPermission, permission)
			}
			seen[permission] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for permission := range seen {
		result = append(result, permission)
	}
	sort.Strings(result)
	return result, nil
}

func PermissionBitfield(names []string) (int64, error) {
	var permissions disgoDiscord.Permissions
	for _, name := range names {
		name = normalizePermissionName(name)
		if name == "" {
			continue
		}
		value, ok := permissionValues[name]
		if !ok {
			return 0, fmt.Errorf("%w: %s", ErrUnknownPermission, name)
		}
		permissions = permissions.Add(value)
	}
	return int64(permissions), nil
}

func PermissionNamesFromBitfield(bitfield string) []string {
	value, err := strconv.ParseInt(strings.TrimSpace(bitfield), 10, 64)
	if err != nil || value <= 0 {
		return nil
	}
	permissions := disgoDiscord.Permissions(value)
	result := []string{}
	for name, permission := range permissionValues {
		if permissions.Has(permission) {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func PermissionFriendlyName(name string) string {
	name = normalizePermissionName(name)
	if friendly, ok := permissionLabels[name]; ok {
		return friendly
	}
	return strings.ReplaceAll(strings.ToLower(name), "_", " ")
}

func FeatureSet(ids []string) map[string]struct{} {
	result := map[string]struct{}{}
	for _, id := range ids {
		if id = normalizeID(id); id != "" {
			result[id] = struct{}{}
		}
	}
	return result
}

func Has(set map[string]struct{}, id string) bool {
	if len(set) == 0 {
		return false
	}
	_, ok := set[normalizeID(id)]
	return ok
}

func normalizeID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

func normalizePermissionName(name string) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, " ", "_")
	return name
}

func cloneFeatures(features []Feature) []Feature {
	result := make([]Feature, 0, len(features))
	for _, feature := range features {
		result = append(result, cloneFeature(feature))
	}
	return result
}

func cloneFeature(feature Feature) Feature {
	feature.DiscordPermissions = append([]string(nil), feature.DiscordPermissions...)
	feature.PandaPermissions = append([]string(nil), feature.PandaPermissions...)
	feature.ToolNames = append([]string(nil), feature.ToolNames...)
	feature.Dependencies = append([]string(nil), feature.Dependencies...)
	return feature
}

var catalog = []Feature{
	{
		ID:                 AssistantChat,
		Label:              "Assistant chat",
		Description:        "Natural messages plus explain, summarize, rewrite, translate, and YouTube video summary workflows.",
		DiscordPermissions: []string{"VIEW_CHANNEL", "SEND_MESSAGES", "READ_MESSAGE_HISTORY", "EMBED_LINKS"},
		PandaPermissions:   []string{admin.PermissionAssistantUse},
		ToolNames:          []string{"panda.summarize_youtube"},
		ConsumesPlanQuota:  true,
		Public:             true,
	},
	{
		ID:                 Threads,
		Label:              "Chat threads",
		Description:        "Dedicated Discord threads for longer conversations.",
		DiscordPermissions: []string{"VIEW_CHANNEL", "SEND_MESSAGES", "CREATE_PUBLIC_THREADS", "SEND_MESSAGES_IN_THREADS", "MANAGE_THREADS"},
		PandaPermissions:   []string{admin.PermissionAssistantUseThreads},
		ToolNames:          []string{"discord.create_thread", "discord.rename_thread", "discord.archive_thread", "discord.add_thread_member", "discord.remove_thread_member"},
		Public:             true,
		Dependencies:       []string{AssistantChat},
	},
	{
		ID:                 Polls,
		Label:              "Polls",
		Description:        "Native Discord polls from confirmed assistant actions.",
		DiscordPermissions: []string{"VIEW_CHANNEL", "SEND_MESSAGES", "SEND_POLLS", "READ_MESSAGE_HISTORY"},
		PandaPermissions:   []string{admin.PermissionAssistantUse},
		ToolNames:          []string{"discord.create_poll", "discord.get_poll_answer_voters", "discord.end_poll"},
		Public:             true,
		Dependencies:       []string{AssistantChat},
	},
	{
		ID:                 Reminders,
		Label:              "Reminders",
		Description:        "User, channel, role, and follow-up reminders.",
		DiscordPermissions: []string{"VIEW_CHANNEL", "SEND_MESSAGES"},
		PandaPermissions:   []string{admin.PermissionAssistantUse},
		ToolNames:          []string{"panda.manage_reminder"},
		ConsumesPlanQuota:  true,
		Public:             true,
		Dependencies:       []string{AssistantChat},
	},
	{
		ID:                 Music,
		Label:              "Music",
		Description:        "Voice playback, queue, playlist, and music settings.",
		DiscordPermissions: []string{"VIEW_CHANNEL", "SEND_MESSAGES", "CONNECT", "SPEAK", "USE_VAD"},
		PandaPermissions:   []string{admin.PermissionAssistantUse},
		ToolNames:          []string{"panda.manage_music"},
		ConsumesPlanQuota:  true,
		Public:             true,
		Dependencies:       []string{AssistantChat},
	},
	{
		ID:                Knowledge,
		Label:             "Knowledge",
		Description:       "Server knowledge search, citations, and admin-managed memory.",
		PandaPermissions:  []string{admin.PermissionAssistantMemoryRead, admin.PermissionAdminMemoryManage},
		ToolNames:         []string{"search_knowledge", "panda.manage_knowledge"},
		ConsumesPlanQuota: true,
		Public:            true,
		Dependencies:      []string{AssistantChat},
	},
	{
		ID:                 Attachments,
		Label:              "Attachments",
		Description:        "Summarize safe uploaded text files and extracted attachment context.",
		DiscordPermissions: []string{"VIEW_CHANNEL", "READ_MESSAGE_HISTORY"},
		PandaPermissions:   []string{admin.PermissionAssistantAttachments},
		ToolNames:          []string{"summarize_text_file"},
		Public:             true,
		Dependencies:       []string{AssistantChat},
	},
	{
		ID:                WebSearch,
		Label:             "Web search",
		Description:       "Current public web answers with source links.",
		PandaPermissions:  []string{admin.PermissionAssistantWebSearch},
		ToolNames:         []string{"web.search"},
		ConsumesPlanQuota: true,
		Public:            true,
		Dependencies:      []string{AssistantChat},
	},
	{
		ID:                 ImageGeneration,
		Label:              "Image generation",
		Description:        "Understand attached images and generate image files as Panda responses when users ask for visual output.",
		DiscordPermissions: []string{"VIEW_CHANNEL", "SEND_MESSAGES", "ATTACH_FILES"},
		PandaPermissions:   []string{admin.PermissionAssistantImageGeneration},
		ToolNames:          []string{"panda.generate_image", "panda.inspect_image"},
		ConsumesPlanQuota:  true,
		Public:             true,
		Dependencies:       []string{AssistantChat},
	},
	{
		ID:               AdminSetup,
		Label:            "Admin setup",
		Description:      "Setup checklist, behavior, prompts, soul, billing status, feedback, alerts, and support-oriented admin views.",
		PandaPermissions: []string{admin.PermissionAdminConfigRead, admin.PermissionAdminConfigWrite, admin.PermissionAssistantSoulWrite, admin.PermissionAdminUsageRead},
		ToolNames:        []string{"read_config", "panda.manage_soul", "panda.manage_prompt"},
		Public:           true,
	},
	{
		ID:               AdminAccessControl,
		Label:            "Access controls",
		Description:      "Panda user and role profiles, channel rules, user and role tool access, and budget limits.",
		PandaPermissions: []string{admin.PermissionAdminConfigRead, admin.PermissionAdminConfigWrite},
		ToolNames:        []string{"panda.manage_role_permission", "panda.manage_user_permission", "panda.manage_channel_rule", "panda.manage_tool_access", "panda.manage_budget_limit"},
		Public:           true,
		Dependencies:     []string{AdminSetup},
	},
	{
		ID:               AdminAudit,
		Label:            "Audit and usage",
		Description:      "Audit history and usage reporting for privileged changes.",
		PandaPermissions: []string{admin.PermissionAdminAuditRead, admin.PermissionAdminUsageRead},
		ToolNames:        []string{"panda.usage_report"},
		Public:           true,
		Dependencies:     []string{AdminSetup},
	},
	{
		ID:                   ComposedTools,
		Label:                "Composed tools",
		Description:          "Draft, approve, run, schedule, and audit server automations.",
		PandaPermissions:     []string{admin.PermissionToolComposeDraft, admin.PermissionToolComposeApprove, admin.PermissionToolComposeInvoke, admin.PermissionToolComposeAudit},
		ToolNames:            []string{"generate_workflow_json", "panda.manage_composed_tool", "panda.manage_schedule"},
		ConsumesPlanQuota:    true,
		RequiresConfirmation: true,
		Public:               true,
		Dependencies:         []string{AdminSetup},
	},
	{
		ID:                   DiscordMessages,
		Label:                "Server channel messages",
		Description:          "Send, reply, and edit Panda messages in regular server channels after confirmation. This is not for DMs.",
		DiscordPermissions:   []string{"VIEW_CHANNEL", "SEND_MESSAGES", "READ_MESSAGE_HISTORY"},
		PandaPermissions:     []string{admin.PermissionAssistantUse},
		ToolNames:            []string{"discord.send_message", "discord.reply_message", "discord.edit_own_message"},
		RequiresConfirmation: true,
		Public:               true,
		Dependencies:         []string{AssistantChat},
	},
	{
		ID:                   DiscordMessageActions,
		Label:                "Server message management",
		Description:          "Delete Panda messages, pin or unpin messages, and manage Panda reactions in server channels after confirmation.",
		DiscordPermissions:   []string{"VIEW_CHANNEL", "MANAGE_MESSAGES", "PIN_MESSAGES", "ADD_REACTIONS"},
		PandaPermissions:     []string{admin.PermissionAssistantUse},
		ToolNames:            []string{"discord.delete_own_message", "discord.pin_message", "discord.unpin_message", "discord.add_reaction", "discord.remove_own_reaction"},
		RequiresConfirmation: true,
		Public:               true,
		Dependencies:         []string{AssistantChat},
	},
	{
		ID:                   DiscordRoleManagement,
		Label:                "Discord role management",
		Description:          "Create roles, assign or remove member roles, and update member nicknames after confirmation.",
		DiscordPermissions:   []string{"VIEW_CHANNEL", "MANAGE_ROLES", "MANAGE_NICKNAMES"},
		PandaPermissions:     []string{admin.PermissionAdminConfigWrite},
		ToolNames:            []string{"discord.create_role", "discord.add_member_role", "discord.remove_member_role", "discord.set_member_nick", "panda.manage_discord_role", "panda.manage_member_role"},
		RequiresConfirmation: true,
		Public:               true,
		Dependencies:         []string{AdminSetup},
	},
	{
		ID:                   DiscordChannelTools,
		Label:                "Discord channel management",
		Description:          "Confirmed channel permission edits, slowmode changes, and thread lifecycle writes.",
		DiscordPermissions:   []string{"VIEW_CHANNEL", "MANAGE_CHANNELS", "MANAGE_THREADS", "CREATE_PRIVATE_THREADS"},
		PandaPermissions:     []string{admin.PermissionAdminConfigWrite},
		ToolNames:            []string{"discord.modify_channel_permissions", "discord.set_channel_slowmode", "discord.lock_thread"},
		RequiresConfirmation: true,
		Public:               true,
		Dependencies:         []string{AdminSetup},
	},
	{
		ID:                   DiscordWebhooks,
		Label:                "Discord webhooks",
		Description:          "List, create, update, and delete channel webhooks.",
		DiscordPermissions:   []string{"VIEW_CHANNEL", "MANAGE_WEBHOOKS"},
		PandaPermissions:     []string{admin.PermissionAdminConfigWrite},
		ToolNames:            []string{"discord.list_webhooks", "discord.create_webhook", "discord.update_webhook", "discord.delete_webhook"},
		RequiresConfirmation: true,
		Public:               true,
		Dependencies:         []string{AdminSetup},
	},
	{
		ID:                   DiscordInvitesEvents,
		Label:                "Invites and events",
		Description:          "Manage invites and scheduled events after confirmation.",
		DiscordPermissions:   []string{"VIEW_CHANNEL", "CREATE_INSTANT_INVITE", "MANAGE_EVENTS"},
		PandaPermissions:     []string{admin.PermissionAdminConfigWrite},
		ToolNames:            []string{"discord.get_invite", "discord.list_invites", "discord.create_invite", "discord.delete_invite", "discord.create_scheduled_event", "discord.update_scheduled_event", "discord.delete_scheduled_event"},
		RequiresConfirmation: true,
		Public:               true,
		Dependencies:         []string{AdminSetup},
	},
	{
		ID:                   ModerationAssist,
		Label:                "Moderation assistance",
		Description:          "Moderation drafts plus confirmed message cleanup, timeouts, kicks, bans, and automod rules.",
		DiscordPermissions:   []string{"VIEW_CHANNEL", "READ_MESSAGE_HISTORY", "MANAGE_MESSAGES", "MODERATE_MEMBERS", "KICK_MEMBERS", "BAN_MEMBERS", "MANAGE_GUILD"},
		PandaPermissions:     []string{admin.PermissionModerationUse},
		ToolNames:            []string{"draft_moderator_note", "discord.timeout_member", "discord.remove_timeout", "discord.kick_member", "discord.ban_member", "discord.unban_member", "discord.bulk_ban_members", "discord.create_auto_moderation_rule", "discord.update_auto_moderation_rule", "discord.delete_auto_moderation_rule", "discord.list_auto_moderation_rules", "discord.list_bans", "discord.get_audit_logs"},
		RequiresConfirmation: true,
		Public:               true,
		Dependencies:         []string{AssistantChat},
	},
	{
		ID:               OwnerOps,
		Label:            "Owner operations",
		Description:      "Bot-owner operational health, guild count, incident, drain, resume, and reload controls from internal operator surfaces.",
		PandaPermissions: []string{admin.PermissionOwnerOps},
		Public:           false,
	},
}

var catalogByID = func() map[string]Feature {
	result := map[string]Feature{}
	for _, feature := range catalog {
		result[feature.ID] = feature
	}
	return result
}()

var permissionValues = map[string]disgoDiscord.Permissions{
	"CREATE_INSTANT_INVITE":    disgoDiscord.PermissionCreateInstantInvite,
	"KICK_MEMBERS":             disgoDiscord.PermissionKickMembers,
	"BAN_MEMBERS":              disgoDiscord.PermissionBanMembers,
	"ADMINISTRATOR":            disgoDiscord.PermissionAdministrator,
	"MANAGE_CHANNELS":          disgoDiscord.PermissionManageChannels,
	"MANAGE_GUILD":             disgoDiscord.PermissionManageGuild,
	"ADD_REACTIONS":            disgoDiscord.PermissionAddReactions,
	"VIEW_AUDIT_LOG":           disgoDiscord.PermissionViewAuditLog,
	"VIEW_CHANNEL":             disgoDiscord.PermissionViewChannel,
	"SEND_MESSAGES":            disgoDiscord.PermissionSendMessages,
	"MANAGE_MESSAGES":          disgoDiscord.PermissionManageMessages,
	"EMBED_LINKS":              disgoDiscord.PermissionEmbedLinks,
	"ATTACH_FILES":             disgoDiscord.PermissionAttachFiles,
	"READ_MESSAGE_HISTORY":     disgoDiscord.PermissionReadMessageHistory,
	"CONNECT":                  disgoDiscord.PermissionConnect,
	"SPEAK":                    disgoDiscord.PermissionSpeak,
	"USE_VAD":                  disgoDiscord.PermissionUseVAD,
	"MANAGE_NICKNAMES":         disgoDiscord.PermissionManageNicknames,
	"MANAGE_ROLES":             disgoDiscord.PermissionManageRoles,
	"MANAGE_WEBHOOKS":          disgoDiscord.PermissionManageWebhooks,
	"MANAGE_EVENTS":            disgoDiscord.PermissionManageEvents,
	"MANAGE_THREADS":           disgoDiscord.PermissionManageThreads,
	"CREATE_PUBLIC_THREADS":    disgoDiscord.PermissionCreatePublicThreads,
	"CREATE_PRIVATE_THREADS":   disgoDiscord.PermissionCreatePrivateThreads,
	"SEND_MESSAGES_IN_THREADS": disgoDiscord.PermissionSendMessagesInThreads,
	"MODERATE_MEMBERS":         disgoDiscord.PermissionModerateMembers,
	"SEND_POLLS":               disgoDiscord.PermissionSendPolls,
	"PIN_MESSAGES":             disgoDiscord.PermissionPinMessages,
}

var permissionLabels = map[string]string{
	"CREATE_INSTANT_INVITE":    "Create Instant Invite",
	"KICK_MEMBERS":             "Kick Members",
	"BAN_MEMBERS":              "Ban Members",
	"ADMINISTRATOR":            "Administrator",
	"MANAGE_CHANNELS":          "Manage Channels",
	"MANAGE_GUILD":             "Manage Server",
	"ADD_REACTIONS":            "Add Reactions",
	"VIEW_AUDIT_LOG":           "View Audit Log",
	"VIEW_CHANNEL":             "View Channels",
	"SEND_MESSAGES":            "Send Messages",
	"MANAGE_MESSAGES":          "Manage Messages",
	"EMBED_LINKS":              "Embed Links",
	"ATTACH_FILES":             "Attach Files",
	"READ_MESSAGE_HISTORY":     "Read Message History",
	"CONNECT":                  "Connect",
	"SPEAK":                    "Speak",
	"USE_VAD":                  "Use Voice Activity",
	"MANAGE_NICKNAMES":         "Manage Nicknames",
	"MANAGE_ROLES":             "Manage Roles",
	"MANAGE_WEBHOOKS":          "Manage Webhooks",
	"MANAGE_EVENTS":            "Manage Events",
	"MANAGE_THREADS":           "Manage Threads",
	"CREATE_PUBLIC_THREADS":    "Create Public Threads",
	"CREATE_PRIVATE_THREADS":   "Create Private Threads",
	"SEND_MESSAGES_IN_THREADS": "Send Messages in Threads",
	"MODERATE_MEMBERS":         "Moderate Members",
	"SEND_POLLS":               "Create Polls",
	"PIN_MESSAGES":             "Pin Messages",
}
