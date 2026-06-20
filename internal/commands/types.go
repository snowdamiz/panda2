package commands

type Request struct {
	RequestID    string
	Command      string
	Subcommand   string
	Options      map[string]string
	GuildID      string
	ChannelID    string
	UserID       string
	RoleIDs      []string
	IsGuildAdmin bool
	IsOwner      bool
}

type Response struct {
	Content      string
	Ephemeral    bool
	ThreadID     string
	ThreadName   string
	Confirmation *Confirmation
	Modal        *Modal
	Background   *BackgroundTask
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
