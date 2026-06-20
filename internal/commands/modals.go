package commands

import "strings"

const (
	modalPrefix       = "p2m"
	modalOpPrompt     = "prompt"
	ModalPromptInput  = "prompt"
	maxPromptModalLen = 4000
)

func promptModalID(userID string) string {
	return strings.Join([]string{modalPrefix, modalOpPrompt, cleanConfirmationPart(userID)}, ":")
}

func promptModalResponse(userID string) Response {
	return Response{
		Ephemeral: true,
		Modal: &Modal{
			ID:    promptModalID(userID),
			Title: "Server Prompt",
			Inputs: []ModalInput{
				{
					ID:          ModalPromptInput,
					Label:       "Prompt",
					Placeholder: "Server-specific assistant instructions",
					Required:    true,
					MaxLength:   maxPromptModalLen,
					Paragraph:   true,
				},
			},
		},
	}
}

func RequestFromModalID(id string, values map[string]string, base Request) (Request, bool) {
	parts := strings.Split(id, ":")
	if len(parts) != 3 || parts[0] != modalPrefix || parts[2] != cleanConfirmationPart(base.UserID) {
		return Request{}, false
	}
	if parts[1] != modalOpPrompt {
		return Request{}, false
	}
	prompt := strings.TrimSpace(values[ModalPromptInput])
	if prompt == "" {
		return Request{}, false
	}

	base.Command = "admin"
	base.Subcommand = "prompt"
	base.Options = map[string]string{"prompt": prompt}
	return base, true
}
