package store

import "time"

type SchemaMigration struct {
	Version   int       `gorm:"primaryKey"`
	Name      string    `gorm:"not null"`
	AppliedAt time.Time `gorm:"not null"`
}

type GuildConfig struct {
	GuildID               string     `gorm:"primaryKey;size:32"`
	Temperature           float64    `gorm:"not null;default:0.3"`
	MaxResponseTokens     int        `gorm:"not null;default:900"`
	ToolPolicy            string     `gorm:"not null;default:'admin_only'"`
	SystemPromptOverlay   string     `gorm:"not null;default:''"`
	AgentSoul             string     `gorm:"not null;default:''"`
	AssistantEnabled      bool       `gorm:"not null;default:true"`
	MemoryEnabled         bool       `gorm:"not null;default:true"`
	AssistantTimeoutUntil *time.Time `gorm:"index"`
	AssistantTimeoutBy    string     `gorm:"not null;default:'';size:32"`
	CreatedAt             time.Time  `gorm:"not null"`
	UpdatedAt             time.Time  `gorm:"not null"`
}

type UsageEvent struct {
	ID               uint      `gorm:"primaryKey"`
	GuildID          string    `gorm:"index;size:32"`
	UserID           string    `gorm:"index;size:32"`
	ChannelID        string    `gorm:"index;size:32"`
	Command          string    `gorm:"index;not null"`
	PromptTokens     int       `gorm:"not null;default:0"`
	CompletionTokens int       `gorm:"not null;default:0"`
	TotalTokens      int       `gorm:"not null;default:0"`
	Success          bool      `gorm:"not null"`
	ErrorCode        string    `gorm:"not null;default:''"`
	LatencyMS        int64     `gorm:"not null;default:0"`
	CreatedAt        time.Time `gorm:"index;not null"`
}

type Guild struct {
	GuildID           string    `gorm:"primaryKey;size:32"`
	Name              string    `gorm:"not null;default:''"`
	InstallStatus     string    `gorm:"not null;default:'active'"`
	OwnerUserID       string    `gorm:"not null;default:'';size:32"`
	InstalledByUserID string    `gorm:"not null;default:'';size:32"`
	Locale            string    `gorm:"not null;default:''"`
	JoinedAt          time.Time `gorm:"not null"`
	LeftAt            *time.Time
	CreatedAt         time.Time `gorm:"not null"`
	UpdatedAt         time.Time `gorm:"not null"`
}

type InstallIntent struct {
	IntentID                    string    `gorm:"primaryKey;size:64"`
	StateHash                   string    `gorm:"uniqueIndex;not null;size:96"`
	SelectedFeatureIDs          string    `gorm:"not null;default:'[]'"`
	ExpandedFeatureIDs          string    `gorm:"not null;default:'[]'"`
	RequestedDiscordPermissions string    `gorm:"not null;default:'[]'"`
	RequestedPermissionBitfield string    `gorm:"not null;default:'0'"`
	GrantedDiscordPermissions   string    `gorm:"not null;default:'[]'"`
	GrantedScopes               string    `gorm:"not null;default:'[]'"`
	Source                      string    `gorm:"index;not null;default:'';size:64"`
	DesiredPlan                 string    `gorm:"not null;default:'';size:64"`
	Referrer                    string    `gorm:"not null;default:''"`
	Campaign                    string    `gorm:"not null;default:'';size:128"`
	InstallerSessionMetadata    string    `gorm:"not null;default:'{}'"`
	Status                      string    `gorm:"index;not null;default:'pending';size:32"`
	GuildID                     string    `gorm:"index;not null;default:'';size:32"`
	InstallerUserID             string    `gorm:"index;not null;default:'';size:32"`
	ExpiresAt                   time.Time `gorm:"index;not null"`
	ConsumedAt                  *time.Time
	CreatedAt                   time.Time `gorm:"not null"`
	UpdatedAt                   time.Time `gorm:"not null"`
}

type GuildFeature struct {
	ID                    uint      `gorm:"primaryKey"`
	GuildID               string    `gorm:"uniqueIndex:idx_guild_features_guild_feature;index;not null;size:32"`
	FeatureID             string    `gorm:"uniqueIndex:idx_guild_features_guild_feature;index;not null;size:64"`
	Enabled               bool      `gorm:"index;not null;default:true"`
	SourceInstallIntentID string    `gorm:"index;not null;default:'';size:64"`
	EnabledByUserID       string    `gorm:"index;not null;default:'';size:32"`
	CreatedAt             time.Time `gorm:"not null"`
	UpdatedAt             time.Time `gorm:"not null"`
}

type GuildRole struct {
	ID         uint      `gorm:"primaryKey"`
	GuildID    string    `gorm:"index;not null;size:32"`
	RoleID     string    `gorm:"index;not null;size:32"`
	Permission string    `gorm:"index;not null"`
	CreatedAt  time.Time `gorm:"not null"`
	UpdatedAt  time.Time `gorm:"not null"`
}

type GuildUserPermission struct {
	ID         uint      `gorm:"primaryKey"`
	GuildID    string    `gorm:"index;not null;size:32"`
	UserID     string    `gorm:"index;not null;size:32"`
	Permission string    `gorm:"index;not null"`
	CreatedAt  time.Time `gorm:"not null"`
	UpdatedAt  time.Time `gorm:"not null"`
}

type GuildToolRole struct {
	ID        uint      `gorm:"primaryKey"`
	GuildID   string    `gorm:"index;not null;size:32"`
	ToolName  string    `gorm:"index;not null;size:128"`
	RoleID    string    `gorm:"index;not null;size:32"`
	Rule      string    `gorm:"index;not null;default:'allow';size:16"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

type GuildToolUser struct {
	ID        uint      `gorm:"primaryKey"`
	GuildID   string    `gorm:"index;not null;size:32"`
	ToolName  string    `gorm:"index;not null;size:128"`
	UserID    string    `gorm:"index;not null;size:32"`
	Rule      string    `gorm:"index;not null;default:'allow';size:16"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

type GuildChannelRule struct {
	ID        uint      `gorm:"primaryKey"`
	GuildID   string    `gorm:"index;not null;size:32"`
	ChannelID string    `gorm:"index;not null;size:32"`
	Rule      string    `gorm:"index;not null"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

type User struct {
	UserID    string    `gorm:"primaryKey;size:32"`
	Username  string    `gorm:"not null;default:''"`
	GlobalOpt string    `gorm:"not null;default:''"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

type UserSafetyState struct {
	ID            uint       `gorm:"primaryKey"`
	GuildID       string     `gorm:"uniqueIndex:idx_user_safety_states_guild_user;index;not null;default:'';size:32"`
	UserID        string     `gorm:"uniqueIndex:idx_user_safety_states_guild_user;index;not null;size:32"`
	ActiveStrikes int        `gorm:"not null;default:0"`
	TotalStrikes  int        `gorm:"not null;default:0"`
	LastStrikeAt  *time.Time `gorm:"index"`
	TimeoutUntil  *time.Time `gorm:"index"`
	CreatedAt     time.Time  `gorm:"not null"`
	UpdatedAt     time.Time  `gorm:"not null"`
}

type GuildMember struct {
	ID               uint      `gorm:"primaryKey"`
	GuildID          string    `gorm:"index;not null;size:32"`
	UserID           string    `gorm:"index;not null;size:32"`
	MemoryConsent    bool      `gorm:"not null;default:false"`
	AssistantAllowed bool      `gorm:"not null;default:true"`
	CreatedAt        time.Time `gorm:"not null"`
	UpdatedAt        time.Time `gorm:"not null"`
}

type Conversation struct {
	ID            uint       `gorm:"primaryKey"`
	GuildID       string     `gorm:"index;not null;size:32"`
	ChannelID     string     `gorm:"index;not null;size:32"`
	ThreadID      string     `gorm:"index;not null;default:'';size:32"`
	OwnerUserID   string     `gorm:"index;not null;size:32"`
	Title         string     `gorm:"not null;default:''"`
	Status        string     `gorm:"index;not null;default:'active'"`
	RetentionDays int        `gorm:"not null;default:30"`
	LastMessageAt time.Time  `gorm:"index;not null"`
	ExpiresAt     *time.Time `gorm:"index"`
	CreatedAt     time.Time  `gorm:"not null"`
	UpdatedAt     time.Time  `gorm:"not null"`
}

type AssistantMessage struct {
	ID               uint      `gorm:"primaryKey"`
	ConversationID   uint      `gorm:"index;not null"`
	GuildID          string    `gorm:"index;not null;size:32"`
	ChannelID        string    `gorm:"index;not null;size:32"`
	UserID           string    `gorm:"index;not null;size:32"`
	DiscordMessageID string    `gorm:"index;not null;default:'';size:32"`
	Role             string    `gorm:"index;not null"`
	ContentHash      string    `gorm:"not null;default:''"`
	ContentPreview   string    `gorm:"not null;default:''"`
	PromptTokens     int       `gorm:"not null;default:0"`
	CompletionTokens int       `gorm:"not null;default:0"`
	TotalTokens      int       `gorm:"not null;default:0"`
	CreatedAt        time.Time `gorm:"index;not null"`
}

func (AssistantMessage) TableName() string {
	return "messages"
}

type KnowledgeDocument struct {
	ID             uint       `gorm:"primaryKey"`
	GuildID        string     `gorm:"index;not null;size:32"`
	Title          string     `gorm:"not null"`
	Source         string     `gorm:"not null;default:'admin'"`
	CreatedBy      string     `gorm:"index;not null;default:'';size:32"`
	Enabled        bool       `gorm:"not null;default:true"`
	Confidence     float64    `gorm:"not null;default:1"`
	ReasonSaved    string     `gorm:"not null;default:''"`
	SourceMetadata string     `gorm:"not null;default:'{}'"`
	ExpiresAt      *time.Time `gorm:"index"`
	CreatedAt      time.Time  `gorm:"not null"`
	UpdatedAt      time.Time  `gorm:"not null"`
}

type KnowledgeChunk struct {
	ID          uint      `gorm:"primaryKey"`
	DocumentID  uint      `gorm:"index;not null"`
	GuildID     string    `gorm:"index;not null;size:32"`
	Ordinal     int       `gorm:"not null"`
	Content     string    `gorm:"not null"`
	ContentHash string    `gorm:"not null"`
	CreatedAt   time.Time `gorm:"not null"`
}

type KnowledgeEmbedding struct {
	ID        uint      `gorm:"primaryKey"`
	ChunkID   uint      `gorm:"index;not null"`
	Model     string    `gorm:"index;not null"`
	Vector    string    `gorm:"not null"`
	CreatedAt time.Time `gorm:"not null"`
}

type Attachment struct {
	ID            uint       `gorm:"primaryKey"`
	GuildID       string     `gorm:"index;not null;size:32"`
	ChannelID     string     `gorm:"index;not null;size:32"`
	MessageID     string     `gorm:"index;not null;size:32"`
	Filename      string     `gorm:"not null"`
	ContentType   string     `gorm:"not null;default:''"`
	SizeBytes     int64      `gorm:"not null;default:0"`
	ExtractedText string     `gorm:"not null;default:''"`
	TempPath      string     `gorm:"not null;default:''"`
	CleanupAfter  *time.Time `gorm:"index"`
	CleanupDoneAt *time.Time
	CreatedAt     time.Time `gorm:"not null"`
	UpdatedAt     time.Time `gorm:"not null"`
}

type DiscordEvent struct {
	ID             uint       `gorm:"primaryKey"`
	GuildID        string     `gorm:"index;not null;size:32"`
	ChannelID      string     `gorm:"index;not null;default:'';size:32"`
	UserID         string     `gorm:"index;not null;default:'';size:32"`
	MessageID      string     `gorm:"index;not null;default:'';size:32"`
	EventType      string     `gorm:"index;not null"`
	Summary        string     `gorm:"not null;default:''"`
	Metadata       string     `gorm:"not null;default:''"`
	ContentPreview string     `gorm:"not null;default:''"`
	CreatedAt      time.Time  `gorm:"index;not null"`
	ExpiresAt      *time.Time `gorm:"index"`
}

type RateLimitBucket struct {
	ID          uint      `gorm:"primaryKey"`
	Scope       string    `gorm:"index;not null"`
	BucketKey   string    `gorm:"index;not null"`
	Count       int       `gorm:"not null;default:0"`
	Limit       int       `gorm:"column:limit_count;not null;default:0"`
	WindowStart time.Time `gorm:"index;not null"`
	WindowEnd   time.Time `gorm:"index;not null"`
	CreatedAt   time.Time `gorm:"not null"`
	UpdatedAt   time.Time `gorm:"not null"`
}

type BudgetLimit struct {
	ID            uint      `gorm:"primaryKey"`
	GuildID       string    `gorm:"index;not null;default:'';size:32"`
	Scope         string    `gorm:"index;not null"`
	SubjectID     string    `gorm:"index;not null;default:'';size:64"`
	Limit         int       `gorm:"column:limit_count;not null"`
	WindowSeconds int       `gorm:"not null"`
	CreatedAt     time.Time `gorm:"not null"`
	UpdatedAt     time.Time `gorm:"not null"`
}

type AuditEvent struct {
	ID         uint      `gorm:"primaryKey"`
	GuildID    string    `gorm:"index;not null;size:32"`
	ActorID    string    `gorm:"index;not null;size:32"`
	Action     string    `gorm:"index;not null"`
	TargetType string    `gorm:"not null;default:''"`
	TargetID   string    `gorm:"not null;default:''"`
	Metadata   string    `gorm:"not null;default:''"`
	CreatedAt  time.Time `gorm:"index;not null"`
}

type RuntimeStatus struct {
	Key       string    `gorm:"primaryKey;size:32"`
	Disabled  bool      `gorm:"not null;default:false"`
	Message   string    `gorm:"not null;default:'';size:512"`
	UpdatedBy string    `gorm:"not null;default:'';size:96"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

type Job struct {
	ID             uint       `gorm:"primaryKey"`
	Kind           string     `gorm:"index;not null"`
	Status         string     `gorm:"index;not null;default:'queued'"`
	GuildID        string     `gorm:"index;not null;default:'';size:32"`
	Payload        string     `gorm:"not null;default:''"`
	Attempts       int        `gorm:"not null;default:0"`
	MaxAttempts    int        `gorm:"not null;default:3"`
	LockOwner      string     `gorm:"index;not null;default:''"`
	LeaseExpiresAt *time.Time `gorm:"index"`
	LastError      string     `gorm:"not null;default:''"`
	RunAfter       time.Time  `gorm:"index;not null"`
	CreatedAt      time.Time  `gorm:"not null"`
	UpdatedAt      time.Time  `gorm:"not null"`
}

type ComposedTool struct {
	ID               uint      `gorm:"primaryKey"`
	GuildID          string    `gorm:"index;not null;size:32"`
	ToolID           string    `gorm:"uniqueIndex;not null;size:64"`
	CurrentVersionID *uint     `gorm:"index"`
	Name             string    `gorm:"index;not null;size:80"`
	Status           string    `gorm:"index;not null;default:'draft'"`
	Visibility       string    `gorm:"index;not null;default:'guild'"`
	CreatedBy        string    `gorm:"index;not null;size:32"`
	ApprovedBy       string    `gorm:"index;not null;default:'';size:32"`
	CreatedAt        time.Time `gorm:"not null"`
	UpdatedAt        time.Time `gorm:"not null"`
}

type ComposedToolVersion struct {
	ID                 uint       `gorm:"primaryKey"`
	ComposedToolID     uint       `gorm:"uniqueIndex:idx_composed_tool_versions_tool_version;index;not null"`
	VersionNumber      int        `gorm:"uniqueIndex:idx_composed_tool_versions_tool_version;not null"`
	SpecJSON           string     `gorm:"not null"`
	ValidationJSON     string     `gorm:"not null;default:'{}'"`
	ToolDefinitionJSON string     `gorm:"not null;default:'{}'"`
	CreatedBy          string     `gorm:"index;not null;size:32"`
	ApprovedBy         string     `gorm:"index;not null;default:'';size:32"`
	ApprovedAt         *time.Time `gorm:"index"`
	CreatedAt          time.Time  `gorm:"not null"`
}

type ComposedToolRun struct {
	ID                uint       `gorm:"primaryKey"`
	ComposedToolID    uint       `gorm:"index;not null"`
	VersionID         uint       `gorm:"index;not null"`
	GuildID           string     `gorm:"index;not null;size:32"`
	InvocationType    string     `gorm:"index;not null"`
	InvokingUserID    string     `gorm:"index;not null;default:'';size:32"`
	TriggeringEventID string     `gorm:"index;not null;default:'';size:64"`
	Status            string     `gorm:"index;not null;default:'queued'"`
	AttemptCount      int        `gorm:"not null;default:0"`
	InputJSON         string     `gorm:"not null;default:'{}'"`
	OutputJSON        string     `gorm:"not null;default:'{}'"`
	TranscriptJSON    string     `gorm:"not null;default:'[]'"`
	Error             string     `gorm:"not null;default:''"`
	StartedAt         *time.Time `gorm:"index"`
	FinishedAt        *time.Time `gorm:"index"`
	CreatedAt         time.Time  `gorm:"index;not null"`
	UpdatedAt         time.Time  `gorm:"not null"`
}

type ComposedToolDedupe struct {
	ID                    uint      `gorm:"primaryKey"`
	ComposedToolID        uint      `gorm:"uniqueIndex:idx_composed_tool_dedupes_tool_fingerprint;index;not null"`
	InvocationFingerprint string    `gorm:"uniqueIndex:idx_composed_tool_dedupes_tool_fingerprint;not null;size:128"`
	ExpiresAt             time.Time `gorm:"index;not null"`
	CreatedAt             time.Time `gorm:"not null"`
}

type Schedule struct {
	ID              uint       `gorm:"primaryKey"`
	GuildID         string     `gorm:"index;not null;size:32"`
	ChannelID       string     `gorm:"index;not null;size:32"`
	OwnerUserID     string     `gorm:"index;not null;size:32"`
	Kind            string     `gorm:"index;not null;size:32"`
	Status          string     `gorm:"index;not null;default:'active';size:32"`
	Title           string     `gorm:"not null;default:''"`
	TargetType      string     `gorm:"index;not null;default:'channel';size:32"`
	TargetID        string     `gorm:"index;not null;default:'';size:64"`
	ScheduleType    string     `gorm:"index;not null;default:'once';size:32"`
	Timezone        string     `gorm:"not null;default:'UTC';size:64"`
	IntervalSeconds int        `gorm:"not null;default:0"`
	Payload         string     `gorm:"not null;default:'{}'"`
	DedupeKey       string     `gorm:"index;not null;default:'';size:128"`
	NextRunAt       time.Time  `gorm:"index;not null"`
	LastRunAt       *time.Time `gorm:"index"`
	LastStatus      string     `gorm:"index;not null;default:'';size:32"`
	LastError       string     `gorm:"not null;default:''"`
	LastJobID       uint       `gorm:"index;not null;default:0"`
	RunCount        int        `gorm:"not null;default:0"`
	Disabled        bool       `gorm:"index;not null;default:false"`
	LockedUntil     *time.Time `gorm:"index"`
	CreatedAt       time.Time  `gorm:"not null"`
	UpdatedAt       time.Time  `gorm:"not null"`
}

type AlertRule struct {
	ID              uint       `gorm:"primaryKey"`
	GuildID         string     `gorm:"index;not null;size:32"`
	Pack            string     `gorm:"index;not null;size:64"`
	ChannelID       string     `gorm:"index;not null;size:32"`
	Enabled         bool       `gorm:"index;not null;default:true"`
	CooldownSeconds int        `gorm:"not null;default:300"`
	PendingCount    int        `gorm:"not null;default:0"`
	LastSentAt      *time.Time `gorm:"index"`
	CreatedBy       string     `gorm:"index;not null;default:'';size:32"`
	CreatedAt       time.Time  `gorm:"not null"`
	UpdatedAt       time.Time  `gorm:"not null"`
}

type FeedbackTarget struct {
	ID          uint      `gorm:"primaryKey"`
	GuildID     string    `gorm:"index;not null;size:32"`
	ChannelID   string    `gorm:"index;not null;size:32"`
	UserID      string    `gorm:"index;not null;size:32"`
	Command     string    `gorm:"index;not null;size:64"`
	ContentHash string    `gorm:"index;not null;default:'';size:128"`
	Metadata    string    `gorm:"not null;default:'{}'"`
	CreatedAt   time.Time `gorm:"index;not null"`
}

type FeedbackEvent struct {
	ID        uint      `gorm:"primaryKey"`
	TargetID  uint      `gorm:"uniqueIndex:idx_feedback_events_target_user;index;not null"`
	GuildID   string    `gorm:"index;not null;size:32"`
	UserID    string    `gorm:"uniqueIndex:idx_feedback_events_target_user;index;not null;size:32"`
	Rating    string    `gorm:"index;not null;size:32"`
	Reason    string    `gorm:"not null;default:''"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

type MusicQueueItem struct {
	ID            uint      `gorm:"primaryKey"`
	GuildID       string    `gorm:"index;not null;size:32"`
	Position      int       `gorm:"index;not null"`
	TrackID       string    `gorm:"not null;default:'';size:128"`
	Query         string    `gorm:"not null;default:''"`
	Title         string    `gorm:"not null;default:''"`
	URL           string    `gorm:"not null;default:''"`
	Uploader      string    `gorm:"not null;default:''"`
	DurationMS    int64     `gorm:"not null;default:0"`
	RequestedBy   string    `gorm:"index;not null;default:'';size:32"`
	TextChannelID string    `gorm:"index;not null;default:'';size:32"`
	CreatedAt     time.Time `gorm:"not null"`
	UpdatedAt     time.Time `gorm:"not null"`
}

type MusicSettings struct {
	GuildID           string    `gorm:"primaryKey;size:32"`
	LoopMode          string    `gorm:"not null;default:'off';size:32"`
	DefaultVolume     int       `gorm:"not null;default:100"`
	DJRoleID          string    `gorm:"not null;default:'';size:32"`
	VoteSkipThreshold float64   `gorm:"not null;default:0.5"`
	CreatedAt         time.Time `gorm:"not null"`
	UpdatedAt         time.Time `gorm:"not null"`
}

type MusicPlaylist struct {
	ID         uint      `gorm:"primaryKey"`
	GuildID    string    `gorm:"uniqueIndex:idx_music_playlists_guild_name;index;not null;size:32"`
	Name       string    `gorm:"uniqueIndex:idx_music_playlists_guild_name;not null;size:80"`
	CreatedBy  string    `gorm:"index;not null;default:'';size:32"`
	TracksJSON string    `gorm:"not null;default:'[]'"`
	CreatedAt  time.Time `gorm:"not null"`
	UpdatedAt  time.Time `gorm:"not null"`
}

type CustomerAccount struct {
	ID                 uint      `gorm:"primaryKey"`
	GuildID            string    `gorm:"uniqueIndex;not null;size:32"`
	BillingOwnerUserID string    `gorm:"index;not null;size:32"`
	Email              string    `gorm:"not null;default:'';size:320"`
	TaxCountry         string    `gorm:"not null;default:'';size:2"`
	SupportContact     string    `gorm:"not null;default:'';size:320"`
	CreatedAt          time.Time `gorm:"not null"`
	UpdatedAt          time.Time `gorm:"not null"`
}

type GuildSubscription struct {
	ID                     uint       `gorm:"primaryKey"`
	GuildID                string     `gorm:"uniqueIndex:idx_guild_subscriptions_active;index;not null;size:32"`
	CustomerAccountID      uint       `gorm:"index;not null;default:0"`
	Plan                   string     `gorm:"index;not null;size:32"`
	Status                 string     `gorm:"index;not null;size:32"`
	GraceState             string     `gorm:"index;not null;size:32"`
	PaymentProvider        string     `gorm:"index;not null;default:'trial';size:32"`
	ExternalSubscriptionID string     `gorm:"index;not null;default:'';size:128"`
	ExternalEntitlementID  string     `gorm:"index;not null;default:'';size:128"`
	BillingOwnerUserID     string     `gorm:"index;not null;default:'';size:32"`
	CurrentPeriodStart     time.Time  `gorm:"index;not null"`
	CurrentPeriodEnd       time.Time  `gorm:"index;not null"`
	TrialEndsAt            *time.Time `gorm:"index"`
	CancelAtPeriodEnd      bool       `gorm:"not null;default:false"`
	CreatedAt              time.Time  `gorm:"not null"`
	UpdatedAt              time.Time  `gorm:"not null"`
}

type EntitlementSnapshot struct {
	ID                         uint       `gorm:"primaryKey"`
	GuildID                    string     `gorm:"index;not null;size:32"`
	SubscriptionID             uint       `gorm:"index;not null"`
	Plan                       string     `gorm:"index;not null;size:32"`
	Status                     string     `gorm:"index;not null;size:32"`
	GraceState                 string     `gorm:"index;not null;size:32"`
	AIResponsesLimit           int        `gorm:"not null;default:0"`
	WebSearchesLimit           int        `gorm:"not null;default:0"`
	ImageGenerationsLimit      int        `gorm:"not null;default:0"`
	KnowledgeStorageBytesLimit int64      `gorm:"not null;default:0"`
	SchedulesLimit             int        `gorm:"not null;default:0"`
	RetentionDays              int        `gorm:"not null;default:0"`
	MusicEnabled               bool       `gorm:"not null;default:false"`
	PremiumToolsEnabled        bool       `gorm:"not null;default:false"`
	CreatedAt                  time.Time  `gorm:"index;not null"`
	ExpiresAt                  *time.Time `gorm:"index"`
}

type InvoicePaymentEvent struct {
	ID             uint      `gorm:"primaryKey"`
	Provider       string    `gorm:"index;not null;size:32"`
	ExternalID     string    `gorm:"index;not null;size:128"`
	GuildID        string    `gorm:"index;not null;default:'';size:32"`
	SubscriptionID uint      `gorm:"index;not null;default:0"`
	AmountCents    int64     `gorm:"not null;default:0"`
	AmountLamports int64     `gorm:"not null;default:0"`
	Currency       string    `gorm:"not null;default:'usd';size:8"`
	Status         string    `gorm:"index;not null;size:32"`
	IdempotencyKey string    `gorm:"uniqueIndex;not null;size:160"`
	RawPayload     string    `gorm:"not null;default:'{}'"`
	CreatedAt      time.Time `gorm:"index;not null"`
}

type BillingOrder struct {
	ID                           uint       `gorm:"primaryKey"`
	OrderID                      string     `gorm:"uniqueIndex;not null;size:64"`
	GuildID                      string     `gorm:"index;not null;size:32"`
	BillingOwnerUserID           string     `gorm:"index;not null;default:'';size:32"`
	SupportEmail                 string     `gorm:"not null;default:'';size:320"`
	Plan                         string     `gorm:"index;not null;size:32"`
	Provider                     string     `gorm:"index;not null;default:'sol';size:32"`
	ListLamports                 int64      `gorm:"not null"`
	DiscountLamports             int64      `gorm:"not null;default:0"`
	DueLamports                  int64      `gorm:"not null"`
	CouponID                     string     `gorm:"index;not null;default:'';size:64"`
	CouponPrefix                 string     `gorm:"index;not null;default:'';size:24"`
	DestinationWallet            string     `gorm:"index;not null;size:64"`
	Reference                    string     `gorm:"uniqueIndex;not null;size:96"`
	Status                       string     `gorm:"index;not null;size:32"`
	Cluster                      string     `gorm:"index;not null;size:32"`
	ConfirmationThreshold        string     `gorm:"not null;size:16"`
	VerifiedTransactionSignature string     `gorm:"uniqueIndex:idx_billing_orders_verified_signature,where:verified_transaction_signature <> '';not null;default:'';size:128"`
	VerifiedAt                   *time.Time `gorm:"index"`
	ActivationKeyRevealedAt      *time.Time `gorm:"index"`
	ActivatedAt                  *time.Time `gorm:"index"`
	ExpiresAt                    time.Time  `gorm:"index;not null"`
	CreatedAt                    time.Time  `gorm:"index;not null"`
	UpdatedAt                    time.Time  `gorm:"not null"`
}

type BillingCoupon struct {
	ID               uint       `gorm:"primaryKey"`
	CouponID         string     `gorm:"uniqueIndex;not null;size:64"`
	CodeHash         string     `gorm:"uniqueIndex;not null;size:96"`
	CodePrefix       string     `gorm:"index;not null;size:24"`
	Plan             string     `gorm:"index;not null;size:32"`
	DiscountLamports int64      `gorm:"not null"`
	MaxRedemptions   int        `gorm:"not null;default:0"`
	Status           string     `gorm:"index;not null;size:32"`
	OwnerNote        string     `gorm:"not null;default:'';size:512"`
	CreatedByUserID  string     `gorm:"index;not null;default:'';size:32"`
	ExpiresAt        *time.Time `gorm:"index"`
	RevokedAt        *time.Time `gorm:"index"`
	CreatedAt        time.Time  `gorm:"index;not null"`
	UpdatedAt        time.Time  `gorm:"not null"`
}

type BillingCouponRedemption struct {
	ID                 uint       `gorm:"primaryKey"`
	RedemptionID       string     `gorm:"uniqueIndex;not null;size:64"`
	CouponID           string     `gorm:"index;not null;size:64"`
	OrderID            string     `gorm:"uniqueIndex;not null;size:64"`
	GuildID            string     `gorm:"index;not null;size:32"`
	BillingOwnerUserID string     `gorm:"index;not null;default:'';size:32"`
	Plan               string     `gorm:"index;not null;size:32"`
	ListLamports       int64      `gorm:"not null"`
	DiscountLamports   int64      `gorm:"not null"`
	DueLamports        int64      `gorm:"not null"`
	Status             string     `gorm:"index;not null;size:32"`
	ExpiresAt          time.Time  `gorm:"index;not null"`
	ConsumedAt         *time.Time `gorm:"index"`
	ReleasedAt         *time.Time `gorm:"index"`
	CreatedAt          time.Time  `gorm:"index;not null"`
	UpdatedAt          time.Time  `gorm:"not null"`
}

type SolPaymentTransaction struct {
	ID                 uint      `gorm:"primaryKey"`
	Signature          string    `gorm:"uniqueIndex;not null;size:128"`
	OrderID            string    `gorm:"index;not null;size:64"`
	GuildID            string    `gorm:"index;not null;size:32"`
	PayerWallet        string    `gorm:"index;not null;default:'';size:64"`
	DestinationWallet  string    `gorm:"index;not null;default:'';size:64"`
	Reference          string    `gorm:"index;not null;default:'';size:96"`
	AmountLamports     int64     `gorm:"not null;default:0"`
	ConfirmationStatus string    `gorm:"index;not null;default:'';size:16"`
	Status             string    `gorm:"index;not null;size:32"`
	ErrorMessage       string    `gorm:"not null;default:'';size:512"`
	RawPayload         string    `gorm:"not null;default:'{}'"`
	CreatedAt          time.Time `gorm:"index;not null"`
	UpdatedAt          time.Time `gorm:"not null"`
}

type ActivationAPIKey struct {
	ID                      uint       `gorm:"primaryKey"`
	KeyID                   string     `gorm:"uniqueIndex;not null;size:64"`
	KeyHash                 string     `gorm:"uniqueIndex;not null;size:96"`
	KeyPrefix               string     `gorm:"index;not null;size:24"`
	BillingOrderID          string     `gorm:"uniqueIndex;not null;size:64"`
	GuildID                 string     `gorm:"index;not null;size:32"`
	Plan                    string     `gorm:"index;not null;size:32"`
	Status                  string     `gorm:"index;not null;size:32"`
	ExpiresAt               time.Time  `gorm:"index;not null"`
	ConsumedAt              *time.Time `gorm:"index"`
	ConsumedByDiscordUserID string     `gorm:"index;not null;default:'';size:32"`
	RevokedAt               *time.Time `gorm:"index"`
	CreatedAt               time.Time  `gorm:"index;not null"`
	UpdatedAt               time.Time  `gorm:"not null"`
}

type UsagePeriod struct {
	ID                            uint      `gorm:"primaryKey"`
	GuildID                       string    `gorm:"uniqueIndex:idx_usage_periods_guild_window;index;not null;size:32"`
	SubscriptionID                uint      `gorm:"index;not null"`
	Plan                          string    `gorm:"index;not null;size:32"`
	PeriodStart                   time.Time `gorm:"uniqueIndex:idx_usage_periods_guild_window;index;not null"`
	PeriodEnd                     time.Time `gorm:"uniqueIndex:idx_usage_periods_guild_window;index;not null"`
	AIResponsesConsumed           int       `gorm:"not null;default:0"`
	AIResponsesReserved           int       `gorm:"not null;default:0"`
	WebSearchesConsumed           int       `gorm:"not null;default:0"`
	WebSearchesReserved           int       `gorm:"not null;default:0"`
	ImageGenerationsConsumed      int       `gorm:"not null;default:0"`
	ImageGenerationsReserved      int       `gorm:"not null;default:0"`
	KnowledgeStorageBytesConsumed int64     `gorm:"not null;default:0"`
	KnowledgeStorageBytesReserved int64     `gorm:"not null;default:0"`
	ScheduledRunsConsumed         int       `gorm:"not null;default:0"`
	ScheduledRunsReserved         int       `gorm:"not null;default:0"`
	MusicPlaybackMinutesConsumed  int       `gorm:"not null;default:0"`
	MusicPlaybackMinutesReserved  int       `gorm:"not null;default:0"`
	CreatedAt                     time.Time `gorm:"not null"`
	UpdatedAt                     time.Time `gorm:"not null"`
}

type UsageReservation struct {
	ID             uint      `gorm:"primaryKey"`
	ReservationID  string    `gorm:"uniqueIndex;not null;size:64"`
	GuildID        string    `gorm:"index;not null;size:32"`
	SubscriptionID uint      `gorm:"index;not null"`
	UsagePeriodID  uint      `gorm:"index;not null"`
	Metric         string    `gorm:"index;not null;size:32"`
	Units          int64     `gorm:"not null"`
	Status         string    `gorm:"index;not null;size:32"`
	ExpiresAt      time.Time `gorm:"index;not null"`
	CreatedAt      time.Time `gorm:"not null"`
	UpdatedAt      time.Time `gorm:"not null"`
}

type CostLedgerEvent struct {
	ID                  uint      `gorm:"primaryKey"`
	GuildID             string    `gorm:"index;not null;default:'';size:32"`
	RequestID           string    `gorm:"index;not null;default:'';size:64"`
	Source              string    `gorm:"index;not null;size:64"`
	Operation           string    `gorm:"index;not null;size:64"`
	Command             string    `gorm:"index;not null;default:'';size:64"`
	Provider            string    `gorm:"index;not null;default:'';size:64"`
	Model               string    `gorm:"index;not null;default:'';size:160"`
	PromptTokens        int       `gorm:"not null;default:0"`
	CompletionTokens    int       `gorm:"not null;default:0"`
	CachedInputTokens   int       `gorm:"not null;default:0"`
	TotalTokens         int       `gorm:"not null;default:0"`
	EstimatedCostMicros int64     `gorm:"not null;default:0"`
	FinalCostMicros     int64     `gorm:"not null;default:0"`
	Success             bool      `gorm:"not null;default:false"`
	ErrorCode           string    `gorm:"not null;default:'';size:64"`
	CreatedAt           time.Time `gorm:"index;not null"`
}
