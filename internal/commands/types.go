package commands

import "github.com/sn0w/panda2/internal/polls"

type Request struct {
	RequestID      string
	Command        string
	Subcommand     string
	Options        map[string]string
	GuildID        string
	ChannelID      string
	VoiceChannelID string
	UserID         string
	RoleIDs        []string
	IsGuildAdmin   bool
	IsOwner        bool
}

type Response struct {
	Content      string
	Ephemeral    bool
	ThreadID     string
	ThreadName   string
	Presentation Presentation
	Actions      []Action
	Confirmation *Confirmation
	Modal        *Modal
	Background   *BackgroundTask
	Poll         *polls.Poll
}

type Accent string

const (
	AccentDefault Accent = ""
	AccentInfo    Accent = "info"
	AccentSuccess Accent = "success"
	AccentWarning Accent = "warning"
	AccentDanger  Accent = "danger"
	AccentMusic   Accent = "music"
)

type Presentation struct {
	Title  string
	Accent Accent
	URL    string
	Footer string
	Fields []Field
}

type Field struct {
	Name   string
	Value  string
	Inline bool
}

type Action struct {
	Label string
	URL   string
}

type Confirmation struct {
	ID           string
	ConfirmLabel string
	CancelID     string
	CancelLabel  string
	Danger       bool
}

type Modal struct {
	ID     string
	Title  string
	Inputs []ModalInput
}

type ModalInput struct {
	ID          string
	Label       string
	Placeholder string
	Value       string
	Required    bool
	MaxLength   int
	Paragraph   bool
}

type BackgroundTask struct {
	RequestID                    string   `json:"request_id,omitempty"`
	GuildID                      string   `json:"guild_id"`
	UserID                       string   `json:"user_id"`
	ChannelID                    string   `json:"channel_id"`
	Command                      string   `json:"command"`
	Input                        string   `json:"input"`
	InvocationContext            string   `json:"invocation_context,omitempty"`
	Tone                         string   `json:"tone,omitempty"`
	Language                     string   `json:"language,omitempty"`
	Detail                       string   `json:"detail,omitempty"`
	AllowedPermissions           []string `json:"allowed_permissions,omitempty"`
	AllowedTools                 []string `json:"allowed_tools,omitempty"`
	RestrictedTools              []string `json:"restricted_tools,omitempty"`
	RequireExplicitComposedTools bool     `json:"require_explicit_composed_tools,omitempty"`
}

type ThreadRequest struct {
	GuildID   string
	ChannelID string
	UserID    string
	Title     string
}

type Thread struct {
	ID      string
	Name    string
	Created bool
}

type MemberRoleRequest struct {
	GuildID string
	UserID  string
	RoleID  string
	ActorID string
	Reason  string
}
