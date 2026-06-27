package composed

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

const (
	StatusDraft           = "draft"
	StatusPendingApproval = "pending_approval"
	StatusEnabled         = "enabled"
	StatusPaused          = "paused"
	StatusDisabled        = "disabled"
	StatusArchived        = "archived"

	VisibilityGuild  = "guild"
	VisibilityHidden = "hidden"

	RunnerDeterministic = "deterministic"
	RunnerAgentic       = "agentic"
	RunnerHybrid        = "hybrid"

	StepToolCall         = "tool_call"
	StepComposedToolCall = "composed_tool_call"

	InvocationChatTool       = "chat_tool"
	InvocationSlashCommand   = "slash_command"
	InvocationMessageContext = "message_context"
	InvocationScheduled      = "scheduled"
	InvocationEvent          = "event"
	InvocationNestedTool     = "nested_tool"
	InvocationManual         = "manual"

	RunQueued      = "queued"
	RunRunning     = "running"
	RunSucceeded   = "succeeded"
	RunFailed      = "failed"
	RunSkipped     = "skipped"
	RunBlocked     = "blocked"
	RunRateLimited = "rate_limited"
	RunDeduped     = "deduped"

	IssueSeverityError   = "error"
	IssueSeverityWarning = "warning"
	IssueSeverityInfo    = "info"

	HealthHealthy                 = "healthy"
	HealthHiddenByAccess          = "hidden_by_access"
	HealthFeatureDisabled         = "feature_disabled"
	HealthInvalidSpec             = "invalid_spec"
	HealthMissingNativeTool       = "missing_native_tool"
	HealthUnresolvedDiscordTarget = "unresolved_discord_target"
	HealthCyclicDependency        = "cyclic_dependency"
	HealthRateLimited             = "rate_limited"
	HealthPausedAfterFailures     = "paused_after_failures"
	HealthPaused                  = "paused"
	HealthBlocked                 = "blocked"

	EventJobKind = "composed_tool.event"

	EventGuildMemberJoined      = "guild.member.joined"
	EventGuildMemberRoleAdded   = "guild.member.role_added"
	EventGuildMemberRoleRemoved = "guild.member.role_removed"
	EventVoiceStateUpdated      = "voice_state_update"

	EventMessageUpdated        = "message_update"
	EventMessageDeleted        = "message_delete"
	EventReactionAdded         = "reaction_add"
	EventReactionRemoved       = "reaction_remove"
	EventReactionsRemovedAll   = "reaction_remove_all"
	EventReactionEmojiRemoved  = "reaction_remove_emoji"
	EventPollVoteAdded         = "poll_vote_add"
	EventPollVoteRemoved       = "poll_vote_remove"
	EventChannelCreated        = "channel_create"
	EventChannelUpdated        = "channel_update"
	EventChannelDeleted        = "channel_delete"
	EventChannelPinsUpdated    = "channel_pins_update"
	EventThreadCreated         = "thread_create"
	EventThreadUpdated         = "thread_update"
	EventThreadDeleted         = "thread_delete"
	EventThreadMemberUpdated   = "thread_member_update"
	EventRoleCreated           = "role_create"
	EventRoleUpdated           = "role_update"
	EventRoleDeleted           = "role_delete"
	EventGuildBan              = "guild_ban"
	EventGuildUnban            = "guild_unban"
	EventInviteCreated         = "invite_create"
	EventInviteDeleted         = "invite_delete"
	EventWebhooksUpdated       = "webhooks_update"
	EventAutoModerationCreated = "auto_moderation_rule_create"
	EventAutoModerationUpdated = "auto_moderation_rule_update"
	EventAutoModerationDeleted = "auto_moderation_rule_delete"
	EventAutoModerationAction  = "auto_moderation_action"
	EventScheduledCreated      = "scheduled_event_create"
	EventScheduledUpdated      = "scheduled_event_update"
	EventScheduledDeleted      = "scheduled_event_delete"
	EventScheduledUserAdded    = "scheduled_event_user_add"
	EventScheduledUserRemoved  = "scheduled_event_user_remove"
)

var supportedEventTypes = map[string]struct{}{
	EventGuildMemberJoined:      {},
	EventGuildMemberRoleAdded:   {},
	EventGuildMemberRoleRemoved: {},
	EventVoiceStateUpdated:      {},
	EventMessageUpdated:         {},
	EventMessageDeleted:         {},
	EventReactionAdded:          {},
	EventReactionRemoved:        {},
	EventReactionsRemovedAll:    {},
	EventReactionEmojiRemoved:   {},
	EventPollVoteAdded:          {},
	EventPollVoteRemoved:        {},
	EventChannelCreated:         {},
	EventChannelUpdated:         {},
	EventChannelDeleted:         {},
	EventChannelPinsUpdated:     {},
	EventThreadCreated:          {},
	EventThreadUpdated:          {},
	EventThreadDeleted:          {},
	EventThreadMemberUpdated:    {},
	EventRoleCreated:            {},
	EventRoleUpdated:            {},
	EventRoleDeleted:            {},
	EventGuildBan:               {},
	EventGuildUnban:             {},
	EventInviteCreated:          {},
	EventInviteDeleted:          {},
	EventWebhooksUpdated:        {},
	EventAutoModerationCreated:  {},
	EventAutoModerationUpdated:  {},
	EventAutoModerationDeleted:  {},
	EventAutoModerationAction:   {},
	EventScheduledCreated:       {},
	EventScheduledUpdated:       {},
	EventScheduledDeleted:       {},
	EventScheduledUserAdded:     {},
	EventScheduledUserRemoved:   {},
}

func SupportsEventType(eventType string) bool {
	_, ok := supportedEventTypes[strings.TrimSpace(eventType)]
	return ok
}

func SupportedEventTypes() []string {
	types := make([]string, 0, len(supportedEventTypes))
	for eventType := range supportedEventTypes {
		types = append(types, eventType)
	}
	sort.Strings(types)
	return types
}

type Spec struct {
	SchemaVersion int              `json:"schema_version"`
	Name          string           `json:"name"`
	Description   string           `json:"description"`
	InputSchema   json.RawMessage  `json:"input_schema"`
	OutputSchema  json.RawMessage  `json:"output_schema"`
	Runner        RunnerSpec       `json:"runner"`
	Steps         []StepSpec       `json:"steps,omitempty"`
	Invocations   []InvocationSpec `json:"invocations"`
	Safety        SafetySpec       `json:"safety"`
}

type RunnerSpec struct {
	Type                  string   `json:"type"`
	SystemPrompt          string   `json:"system_prompt"`
	Temperature           float64  `json:"temperature"`
	MaxTokens             int      `json:"max_tokens"`
	ToolAllowlist         []string `json:"tool_allowlist"`
	ComposedToolAllowlist []string `json:"composed_tool_allowlist,omitempty"`
}

type StepSpec struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
	OutputKey string         `json:"output_key,omitempty"`
}

type InvocationSpec struct {
	Type               string            `json:"type"`
	Enabled            *bool             `json:"enabled,omitempty"`
	EventType          string            `json:"event_type,omitempty"`
	Filters            map[string]string `json:"filters,omitempty"`
	InputMapping       map[string]string `json:"input_mapping,omitempty"`
	RequiredPermission string            `json:"required_permission,omitempty"`
	Cron               string            `json:"cron,omitempty"`
}

type SafetySpec struct {
	RequiresApproval            bool `json:"requires_approval"`
	RequiresConfirmationOnWrite bool `json:"requires_confirmation_on_write"`
	MaxNestedDepth              int  `json:"max_nested_depth"`
	CooldownSeconds             int  `json:"cooldown_seconds"`
	MaxRunsPerHour              int  `json:"max_runs_per_hour"`
	DedupeWindowSeconds         int  `json:"dedupe_window_seconds"`
}

type ValidationReport struct {
	Valid       bool              `json:"valid"`
	RiskLevel   string            `json:"risk_level"`
	Issues      []ValidationIssue `json:"issues,omitempty"`
	Errors      []string          `json:"errors,omitempty"`
	Warnings    []string          `json:"warnings,omitempty"`
	NativeTools []string          `json:"native_tools,omitempty"`
	Writes      []string          `json:"writes,omitempty"`
}

type ValidationIssue struct {
	Code         string `json:"code"`
	Severity     string `json:"severity"`
	Message      string `json:"message"`
	SuggestedFix string `json:"suggested_fix,omitempty"`
}

type HealthReport struct {
	State         string            `json:"state"`
	Visible       bool              `json:"visible"`
	Reasons       []string          `json:"reasons,omitempty"`
	Issues        []ValidationIssue `json:"issues,omitempty"`
	LastRunID     uint              `json:"last_run_id,omitempty"`
	LastRunStatus string            `json:"last_run_status,omitempty"`
}

type ExposureSummary struct {
	State                  string   `json:"state"`
	CallableByRequester    bool     `json:"callable_by_requester"`
	RequiresExplicitGrant  bool     `json:"requires_explicit_grant"`
	RecommendedNextActions []string `json:"recommended_next_actions,omitempty"`
	Explanation            string   `json:"explanation,omitempty"`
}

type ApprovalSummary struct {
	Purpose             string         `json:"purpose"`
	InvocationModes     []string       `json:"invocation_modes"`
	TriggerSummary      []string       `json:"trigger_summary,omitempty"`
	TargetSummary       []string       `json:"target_summary,omitempty"`
	NativeTools         []string       `json:"native_tools,omitempty"`
	ComposedTools       []string       `json:"composed_tools,omitempty"`
	WriteActions        []string       `json:"write_actions,omitempty"`
	DiscordPermissions  []string       `json:"discord_permissions,omitempty"`
	SafetyLimits        map[string]any `json:"safety_limits"`
	RiskLevel           string         `json:"risk_level"`
	RiskReasons         []string       `json:"risk_reasons,omitempty"`
	RequiresApproval    bool           `json:"requires_approval"`
	WriteConfirmation   bool           `json:"write_confirmation"`
	MaxNestedDepth      int            `json:"max_nested_depth"`
	CooldownSeconds     int            `json:"cooldown_seconds"`
	MaxRunsPerHour      int            `json:"max_runs_per_hour"`
	DedupeWindowSeconds int            `json:"dedupe_window_seconds"`
}

type EventJobPayload struct {
	GuildID   string            `json:"guild_id"`
	EventID   string            `json:"event_id"`
	EventType string            `json:"event_type"`
	UserID    string            `json:"user_id,omitempty"`
	ChannelID string            `json:"channel_id,omitempty"`
	MessageID string            `json:"message_id,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at,omitempty"`
}

type RunRequest struct {
	GuildID           string
	ToolName          string
	InvocationType    string
	InvokingUserID    string
	TriggeringEventID string
	Input             map[string]any
	NestedDepth       int
	DryRun            bool
	EnabledFeatures   map[string]struct{}
	FeatureGateActive bool
}

type RunResult struct {
	RunID      uint
	Status     string
	Output     map[string]any
	Transcript []TranscriptEntry
	Error      string
}

type TranscriptEntry struct {
	StepID       string         `json:"step_id,omitempty"`
	Tool         string         `json:"tool"`
	Arguments    map[string]any `json:"arguments,omitempty"`
	Result       any            `json:"result,omitempty"`
	Error        string         `json:"error,omitempty"`
	NestedRunID  uint           `json:"nested_run_id,omitempty"`
	ElapsedMS    int64          `json:"elapsed_ms,omitempty"`
	Confirmation bool           `json:"confirmation_required,omitempty"`
}

type DraftRequest struct {
	GuildID          string
	ActorID          string
	Text             string
	SpecJSON         string
	RoleID           string
	RoleName         string
	ChannelID        string
	ChannelName      string
	VoiceChannelID   string
	VoiceChannelName string
	SourceChannelID  string
	WelcomeText      string
}

type DraftResult struct {
	Tool       string
	Version    int
	Spec       Spec
	Validation ValidationReport
}

func invocationEnabled(invocation InvocationSpec) bool {
	return invocation.Enabled == nil || *invocation.Enabled
}
