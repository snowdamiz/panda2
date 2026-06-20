package commands

import "strings"

const (
	modalPrefix       = "p2m"
	modalOpPrompt     = "prompt"
	modalOpSoul       = "soul"
	ModalPromptInput  = "prompt"
	ModalSoulInput    = "soul"
	maxPromptModalLen = 4000
)

func promptModalID(userID string) string {
	return strings.Join([]string{modalPrefix, modalOpPrompt, cleanConfirmationPart(userID)}, ":")
}

func soulModalID(userID string) string {
	return strings.Join([]string{modalPrefix, modalOpSoul, cleanConfirmationPart(userID)}, ":")
}

func promptModalResponse(userID string) Response {
	return instructionModalResponse(promptModalID(userID), "Server Prompt", ModalPromptInput, "Prompt", "Server-specific assistant instructions")
}

func soulModalResponse(userID string) Response {
	return instructionModalResponse(soulModalID(userID), "Agent Soul", ModalSoulInput, "Soul", "Personality, style, and response voice")
}

func instructionModalResponse(id, title, inputID, label, placeholder string) Response {
	return Response{
		Ephemeral: true,
		Modal: &Modal{
			ID:    id,
			Title: title,
			Inputs: []ModalInput{
				{
					ID:          inputID,
					Label:       label,
					Placeholder: placeholder,
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
	switch parts[1] {
	case modalOpPrompt:
		prompt := strings.TrimSpace(values[ModalPromptInput])
		if prompt == "" {
			return Request{}, false
		}
		base.Command = "admin"
		base.Subcommand = "prompt"
		base.Options = map[string]string{"prompt": prompt}
		return base, true
	case modalOpSoul:
		soul := strings.TrimSpace(values[ModalSoulInput])
		if soul == "" {
			return Request{}, false
		}
		base.Command = "admin"
		base.Subcommand = "soul"
		base.Options = map[string]string{"soul": soul}
		return base, true
	default:
		return Request{}, false
	}
}
