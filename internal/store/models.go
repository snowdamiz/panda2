package store

import "time"

type SchemaMigration struct {
	Version   int       `gorm:"primaryKey"`
	Name      string    `gorm:"not null"`
	AppliedAt time.Time `gorm:"not null"`
}

type GuildConfig struct {
	GuildID             string    `gorm:"primaryKey;size:32"`
	DefaultModel        string    `gorm:"not null"`
	FallbackModels      string    `gorm:"not null;default:'[]'"`
	Temperature         float64   `gorm:"not null;default:0.3"`
	MaxResponseTokens   int       `gorm:"not null;default:900"`
	ToolPolicy          string    `gorm:"not null;default:'admin_only'"`
	SystemPromptOverlay string    `gorm:"not null;default:''"`
	AgentSoul           string    `gorm:"not null;default:''"`
	AssistantEnabled    bool      `gorm:"not null;default:true"`
	MemoryEnabled       bool      `gorm:"not null;default:true"`
	CreatedAt           time.Time `gorm:"not null"`
	UpdatedAt           time.Time `gorm:"not null"`
}

type UsageEvent struct {
	ID               uint      `gorm:"primaryKey"`
	GuildID          string    `gorm:"index;size:32"`
	UserID           string    `gorm:"index;size:32"`
	ChannelID        string    `gorm:"index;size:32"`
	Command          string    `gorm:"index;not null"`
	Model            string    `gorm:"not null;default:''"`
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
	FeatureFlags      string    `gorm:"not null;default:''"`
	JoinedAt          time.Time `gorm:"not null"`
	LeftAt            *time.Time
	CreatedAt         time.Time `gorm:"not null"`
	UpdatedAt         time.Time `gorm:"not null"`
}

type GuildRole struct {
	ID         uint      `gorm:"primaryKey"`
	GuildID    string    `gorm:"index;not null;size:32"`
	RoleID     string    `gorm:"index;not null;size:32"`
	Permission string    `gorm:"index;not null"`
	CreatedAt  time.Time `gorm:"not null"`
	UpdatedAt  time.Time `gorm:"not null"`
}

type GuildToolRole struct {
	ID        uint      `gorm:"primaryKey"`
	GuildID   string    `gorm:"index;not null;size:32"`
	ToolName  string    `gorm:"index;not null;size:128"`
	RoleID    string    `gorm:"index;not null;size:32"`
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
	Model            string    `gorm:"not null;default:''"`
	PromptTokens     int       `gorm:"not null;default:0"`
	CompletionTokens int       `gorm:"not null;default:0"`
	TotalTokens      int       `gorm:"not null;default:0"`
	CreatedAt        time.Time `gorm:"index;not null"`
}

func (AssistantMessage) TableName() string {
	return "messages"
}

type KnowledgeDocument struct {
	ID        uint      `gorm:"primaryKey"`
	GuildID   string    `gorm:"index;not null;size:32"`
	Title     string    `gorm:"not null"`
	Source    string    `gorm:"not null;default:'admin'"`
	CreatedBy string    `gorm:"index;not null;default:'';size:32"`
	Enabled   bool      `gorm:"not null;default:true"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
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
	Model             string     `gorm:"not null;default:''"`
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
