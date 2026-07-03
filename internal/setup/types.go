package setup

import (
	"context"
	"time"
)

const (
	SchemaVersion = 1

	ProjectStatusDraft      = "draft"
	ProjectStatusPreviewed  = "previewed"
	ProjectStatusConfirmed  = "confirmed"
	ProjectStatusQueued     = "queued"
	ProjectStatusApplying   = "applying"
	ProjectStatusSucceeded  = "succeeded"
	ProjectStatusFailed     = "failed"
	ProjectStatusRolledBack = "rolled_back"
	ProjectStatusCancelled  = "cancelled"

	ResourceTypeRole           = "role"
	ResourceTypeCategory       = "category"
	ResourceTypeChannel        = "channel"
	ResourceTypeTicketPanel    = "ticket_panel"
	ResourceTypeOnboardingFlow = "onboarding_flow"
	ResourceTypeStarterMessage = "starter_message"
	ResourceTypePandaConfig    = "panda_config"

	PlanActionCreate = "create"
	PlanActionUpdate = "update"
	PlanActionReuse  = "reuse"
	PlanActionSkip   = "skip"
	PlanActionDelete = "delete"

	JobKindApplySetup = "setup.apply"

	TicketStatusOpen     = "open"
	TicketStatusClaimed  = "claimed"
	TicketStatusWaiting  = "waiting"
	TicketStatusClosed   = "closed"
	TicketStatusReopened = "reopened"
	TicketStatusArchived = "archived"

	OnboardingStatusInProgress = "in_progress"
	OnboardingStatusCompleted  = "completed"
	OnboardingStatusPaused     = "paused"
)

type Template struct {
	ID                string                   `json:"id"`
	SchemaVersion     int                      `json:"schema_version"`
	TemplateVersion   int                      `json:"template_version"`
	Name              string                   `json:"name"`
	Description       string                   `json:"description"`
	ReleaseState      string                   `json:"release_state"`
	DefaultVariables  map[string]string        `json:"default_variables"`
	EditableVariables []TemplateVariable       `json:"editable_variables,omitempty"`
	FeatureIDs        []string                 `json:"feature_ids,omitempty"`
	Roles             []RoleTemplate           `json:"roles,omitempty"`
	Categories        []CategoryTemplate       `json:"categories,omitempty"`
	Channels          []ChannelTemplate        `json:"channels,omitempty"`
	Panda             PandaConfigTemplate      `json:"panda,omitempty"`
	TicketPanels      []TicketPanelTemplate    `json:"ticket_panels,omitempty"`
	OnboardingFlows   []OnboardingFlowTemplate `json:"onboarding_flows,omitempty"`
	Automations       []AutomationTemplate     `json:"automations,omitempty"`
}

type TemplateVariable struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type,omitempty"`
	Required    bool     `json:"required"`
	Options     []string `json:"options,omitempty"`
}

type RoleTemplate struct {
	Alias       string   `json:"alias"`
	Name        string   `json:"name"`
	Color       string   `json:"color,omitempty"`
	Hoist       bool     `json:"hoist,omitempty"`
	Mentionable bool     `json:"mentionable,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	Position    int      `json:"position,omitempty"`
	Profile     string   `json:"profile,omitempty"`
}

type CategoryTemplate struct {
	Alias      string              `json:"alias"`
	Name       string              `json:"name"`
	Position   int                 `json:"position,omitempty"`
	Overwrites []OverwriteTemplate `json:"overwrites,omitempty"`
}

type ChannelTemplate struct {
	Alias           string              `json:"alias"`
	Type            string              `json:"type"`
	Name            string              `json:"name"`
	ParentAlias     string              `json:"parent_alias,omitempty"`
	Topic           string              `json:"topic,omitempty"`
	SlowmodeSeconds int                 `json:"slowmode_seconds,omitempty"`
	NSFW            bool                `json:"nsfw,omitempty"`
	Position        int                 `json:"position,omitempty"`
	Overwrites      []OverwriteTemplate `json:"overwrites,omitempty"`
	StarterMessages []StarterMessage    `json:"starter_messages,omitempty"`
	Bitrate         int                 `json:"bitrate,omitempty"`
	UserLimit       int                 `json:"user_limit,omitempty"`
	Guidelines      string              `json:"guidelines,omitempty"`
	Tags            []string            `json:"tags,omitempty"`
}

type OverwriteTemplate struct {
	TargetAlias string   `json:"target_alias"`
	TargetType  string   `json:"target_type"`
	Allow       []string `json:"allow,omitempty"`
	Deny        []string `json:"deny,omitempty"`
}

type StarterMessage struct {
	Alias   string `json:"alias,omitempty"`
	Content string `json:"content"`
	Pin     bool   `json:"pin,omitempty"`
}

type PandaConfigTemplate struct {
	PromptOverlay string            `json:"prompt_overlay,omitempty"`
	ChannelRules  map[string]string `json:"channel_rules,omitempty"`
	RoleProfiles  map[string]string `json:"role_profiles,omitempty"`
	ToolAccess    []ToolAccessRule  `json:"tool_access,omitempty"`
	Budgets       []BudgetRule      `json:"budgets,omitempty"`
}

type ToolAccessRule struct {
	ToolName  string `json:"tool_name"`
	RoleAlias string `json:"role_alias,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	Rule      string `json:"rule"`
}

type BudgetRule struct {
	Scope         string `json:"scope"`
	SubjectAlias  string `json:"subject_alias,omitempty"`
	Limit         int    `json:"limit"`
	WindowSeconds int    `json:"window_seconds"`
}

type TicketPanelTemplate struct {
	Alias               string                     `json:"alias"`
	PanelChannelAlias   string                     `json:"panel_channel_alias"`
	Title               string                     `json:"title"`
	Body                string                     `json:"body"`
	Departments         []TicketDepartmentTemplate `json:"departments"`
	StaffRoleAliases    []string                   `json:"staff_role_aliases,omitempty"`
	TargetCategoryAlias string                     `json:"target_category_alias,omitempty"`
	ThreadMode          bool                       `json:"thread_mode,omitempty"`
	Tags                []string                   `json:"tags,omitempty"`
	TranscriptPolicy    string                     `json:"transcript_policy,omitempty"`
}

type TicketDepartmentTemplate struct {
	ID               string   `json:"id"`
	Label            string   `json:"label"`
	Description      string   `json:"description,omitempty"`
	StaffRoleAliases []string `json:"staff_role_aliases,omitempty"`
	InitialPriority  string   `json:"initial_priority,omitempty"`
}

type OnboardingFlowTemplate struct {
	Alias               string                   `json:"alias"`
	WelcomeChannelAlias string                   `json:"welcome_channel_alias"`
	RulesChannelAlias   string                   `json:"rules_channel_alias,omitempty"`
	VerifiedRoleAlias   string                   `json:"verified_role_alias"`
	NewcomerRoleAlias   string                   `json:"newcomer_role_alias,omitempty"`
	VerificationMode    string                   `json:"verification_mode"`
	IntroPrompt         string                   `json:"intro_prompt,omitempty"`
	CompletionMessage   string                   `json:"completion_message,omitempty"`
	Steps               []OnboardingStepTemplate `json:"steps,omitempty"`
}

type OnboardingStepTemplate struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	Prompt        string   `json:"prompt"`
	Required      bool     `json:"required"`
	RoleAliases   []string `json:"role_aliases,omitempty"`
	MinSelections int      `json:"min_selections,omitempty"`
	MaxSelections int      `json:"max_selections,omitempty"`
}

type AutomationTemplate struct {
	Alias   string         `json:"alias"`
	Name    string         `json:"name"`
	Enabled bool           `json:"enabled"`
	Spec    map[string]any `json:"spec"`
}

type Variables map[string]string

type GuildSnapshot struct {
	Roles    []RoleState    `json:"roles"`
	Channels []ChannelState `json:"channels"`
}

type RoleState struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Color       int      `json:"color,omitempty"`
	Hoist       bool     `json:"hoist,omitempty"`
	Mentionable bool     `json:"mentionable,omitempty"`
	Managed     bool     `json:"managed,omitempty"`
	Position    int      `json:"position,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
}

type ChannelState struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	ParentID string `json:"parent_id,omitempty"`
	Position int    `json:"position,omitempty"`
}

type Preview struct {
	ProjectID    string         `json:"project_id,omitempty"`
	TemplateID   string         `json:"template_id"`
	TemplateName string         `json:"template_name"`
	Summary      PreviewSummary `json:"summary"`
	Groups       []PreviewGroup `json:"groups"`
	Warnings     []string       `json:"warnings,omitempty"`
	Blocked      bool           `json:"blocked"`
	Plan         []PlanStep     `json:"plan"`
	GeneratedAt  time.Time      `json:"generated_at"`
}

type PreviewSummary struct {
	Roles            int `json:"roles"`
	Categories       int `json:"categories"`
	Channels         int `json:"channels"`
	TicketPanels     int `json:"ticket_panels"`
	OnboardingFlows  int `json:"onboarding_flows"`
	StarterMessages  int `json:"starter_messages"`
	PandaConfigItems int `json:"panda_config_items"`
	Creates          int `json:"creates"`
	Updates          int `json:"updates"`
	Reuses           int `json:"reuses"`
	Skips            int `json:"skips"`
}

type PreviewGroup struct {
	Name  string             `json:"name"`
	Items []PreviewGroupItem `json:"items"`
}

type PreviewGroupItem struct {
	Action      string   `json:"action"`
	Type        string   `json:"type"`
	Alias       string   `json:"alias"`
	Name        string   `json:"name"`
	ObjectID    string   `json:"object_id,omitempty"`
	Description string   `json:"description,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

type PlanStep struct {
	ID           string         `json:"id"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	Alias        string         `json:"alias"`
	Name         string         `json:"name"`
	ObjectID     string         `json:"object_id,omitempty"`
	DependsOn    []string       `json:"depends_on,omitempty"`
	Hash         string         `json:"hash"`
	Payload      map[string]any `json:"payload,omitempty"`
	Warnings     []string       `json:"warnings,omitempty"`
}

type ApplyProgress struct {
	Total       int       `json:"total"`
	Completed   int       `json:"completed"`
	CurrentStep string    `json:"current_step,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ApplyResult struct {
	ProjectID string            `json:"project_id"`
	Status    string            `json:"status"`
	Summary   PreviewSummary    `json:"summary"`
	Resources []AppliedResource `json:"resources,omitempty"`
	Warnings  []string          `json:"warnings,omitempty"`
	Error     string            `json:"error,omitempty"`
}

type AppliedResource struct {
	Alias      string `json:"alias"`
	Type       string `json:"type"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	Action     string `json:"action"`
	DiscordURL string `json:"discord_url,omitempty"`
}

type SetupRequest struct {
	GuildID             string            `json:"guild_id"`
	ActorID             string            `json:"actor_id"`
	ChannelID           string            `json:"channel_id,omitempty"`
	TemplateID          string            `json:"template_id"`
	Variables           map[string]string `json:"variables,omitempty"`
	SourceInstallIntent string            `json:"source_install_intent,omitempty"`
	Confirm             bool              `json:"confirm,omitempty"`
	DryRun              bool              `json:"dry_run,omitempty"`
}

type TicketDepartment struct {
	ID              string   `json:"id"`
	Label           string   `json:"label"`
	Description     string   `json:"description,omitempty"`
	StaffRoleIDs    []string `json:"staff_role_ids,omitempty"`
	InitialPriority string   `json:"initial_priority,omitempty"`
}

type ComponentRequest struct {
	CustomID  string            `json:"custom_id"`
	GuildID   string            `json:"guild_id"`
	ChannelID string            `json:"channel_id"`
	MessageID string            `json:"message_id,omitempty"`
	UserID    string            `json:"user_id"`
	RoleIDs   []string          `json:"role_ids,omitempty"`
	IsAdmin   bool              `json:"is_admin,omitempty"`
	Values    []string          `json:"values,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
}

type ComponentResponse struct {
	Content    string             `json:"content"`
	Ephemeral  bool               `json:"ephemeral"`
	Title      string             `json:"title,omitempty"`
	Accent     string             `json:"accent,omitempty"`
	Components []MessageComponent `json:"components,omitempty"`
	Modal      *ComponentModal    `json:"modal,omitempty"`
}

type ComponentModal struct {
	ID     string                `json:"id"`
	Title  string                `json:"title"`
	Fields []ComponentModalField `json:"fields"`
}

type ComponentModalField struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder,omitempty"`
	Value       string `json:"value,omitempty"`
	Required    bool   `json:"required"`
	Paragraph   bool   `json:"paragraph,omitempty"`
	MaxLength   int    `json:"max_length,omitempty"`
}

type DiscordAdapter interface {
	Snapshot(ctx context.Context, guildID string) (GuildSnapshot, error)
	CreateRole(ctx context.Context, request RoleApplyRequest) (DiscordResource, error)
	UpdateRole(ctx context.Context, request RoleApplyRequest) (DiscordResource, error)
	DeleteRole(ctx context.Context, guildID, roleID, reason string) error
	MoveRoles(ctx context.Context, guildID string, positions []PositionUpdate, reason string) error
	CreateChannel(ctx context.Context, request ChannelApplyRequest) (DiscordResource, error)
	UpdateChannel(ctx context.Context, request ChannelApplyRequest) (DiscordResource, error)
	DeleteChannel(ctx context.Context, channelID, reason string) error
	MoveChannels(ctx context.Context, guildID string, positions []PositionUpdate, reason string) error
	SendMessage(ctx context.Context, request MessageApplyRequest) (DiscordResource, error)
	CreateTicketChannel(ctx context.Context, request TicketChannelRequest) (DiscordResource, error)
	AddTicketParticipant(ctx context.Context, guildID, channelID, userID, reason string) error
	RemoveTicketParticipant(ctx context.Context, guildID, channelID, userID, reason string) error
	ExportTranscript(ctx context.Context, channelID string, limit int) (map[string]any, error)
	AddMemberRole(ctx context.Context, guildID, userID, roleID, reason string) error
	RemoveMemberRole(ctx context.Context, guildID, userID, roleID, reason string) error
}

type DiscordResource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

type RoleApplyRequest struct {
	GuildID     string
	RoleID      string
	Name        string
	Color       int
	Hoist       bool
	Mentionable bool
	Permissions []string
	Position    int
	Reason      string
}

type ChannelApplyRequest struct {
	GuildID         string
	ChannelID       string
	Type            string
	Name            string
	Topic           string
	ParentID        string
	Position        int
	NSFW            bool
	SlowmodeSeconds int
	Bitrate         int
	UserLimit       int
	Overwrites      []ResolvedOverwrite
	Reason          string
}

type ResolvedOverwrite struct {
	TargetID   string   `json:"target_id"`
	TargetType string   `json:"target_type"`
	Allow      []string `json:"allow,omitempty"`
	Deny       []string `json:"deny,omitempty"`
}

type PositionUpdate struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
	ParentID string `json:"parent_id,omitempty"`
}

type MessageApplyRequest struct {
	ChannelID  string
	Content    string
	Reason     string
	Components []MessageComponent
}

type MessageComponent struct {
	Type        string                   `json:"type"`
	Label       string                   `json:"label,omitempty"`
	CustomID    string                   `json:"custom_id"`
	Style       string                   `json:"style,omitempty"`
	Placeholder string                   `json:"placeholder,omitempty"`
	MinValues   int                      `json:"min_values,omitempty"`
	MaxValues   int                      `json:"max_values,omitempty"`
	Options     []MessageComponentOption `json:"options,omitempty"`
}

type MessageComponentOption struct {
	Label       string `json:"label"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
	Default     bool   `json:"default,omitempty"`
}

type TicketChannelRequest struct {
	GuildID         string
	Name            string
	CategoryID      string
	RequesterUserID string
	StaffRoleIDs    []string
	ObserverUserIDs []string
	Topic           string
	StarterMessage  string
	Reason          string
}
