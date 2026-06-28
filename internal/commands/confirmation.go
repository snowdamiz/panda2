package commands

import "strings"

const (
	confirmationPrefix   = "p2c"
	confirmationOpCancel = "cancel"

	confirmationOpAdminDisable = "ad"
	confirmationOpDataDelete   = "dd"
)

func adminDisableConfirmationID(userID string) string {
	return confirmationID(confirmationOpAdminDisable, userID)
}

func dataDeleteConfirmationID(userID, scope string) string {
	return strings.Join([]string{confirmationPrefix, confirmationOpDataDelete, cleanConfirmationPart(userID), cleanConfirmationPart(scope)}, ":")
}

func confirmationID(op, userID string) string {
	return strings.Join([]string{confirmationPrefix, op, cleanConfirmationPart(userID)}, ":")
}

func ConfirmationCancelID(userID string) string {
	return strings.Join([]string{confirmationPrefix, confirmationOpCancel, cleanConfirmationPart(userID)}, ":")
}

func ConfirmationCancelIDForConfirmation(id string) string {
	parts := strings.Split(id, ":")
	if len(parts) < 3 || parts[0] == "" || strings.TrimSpace(parts[2]) == "" {
		return ""
	}
	return ConfirmationCancelID(parts[2])
}

func IsConfirmationCancelID(id string, base Request) bool {
	parts := strings.Split(id, ":")
	return len(parts) == 3 &&
		parts[0] == confirmationPrefix &&
		parts[1] == confirmationOpCancel &&
		parts[2] == cleanConfirmationPart(base.UserID)
}

func cleanConfirmationPart(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, ":", "_")
	return value
}

func destructiveConfirmation(id, label, summary string) Response {
	return Response{
		Content:   summary + "\n\nPress the confirmation button to continue.",
		Ephemeral: true,
		Presentation: Presentation{
			Title:  "Confirmation required",
			Accent: AccentWarning,
		},
		Confirmation: &Confirmation{
			ID:           id,
			ConfirmLabel: label,
			CancelLabel:  "Cancel",
			Danger:       true,
		},
	}
}

func confirmed(request Request, id string) bool {
	return strings.TrimSpace(request.Options["confirm"]) == id
}

// RequestFromConfirmationID converts a button custom id into the command request
// the confirmation flow expects.
func RequestFromConfirmationID(id string, base Request) (Request, bool) {
	parts := strings.Split(id, ":")
	if len(parts) < 3 || parts[0] != confirmationPrefix || parts[2] != cleanConfirmationPart(base.UserID) {
		return Request{}, false
	}

	base.Command = "admin"
	base.Options = map[string]string{
		"confirm": id,
	}

	switch parts[1] {
	case confirmationOpAdminDisable:
		if len(parts) != 3 {
			return Request{}, false
		}
		base.Subcommand = "disable"
	case confirmationOpDataDelete:
		if len(parts) != 4 {
			return Request{}, false
		}
		base.Command = "data"
		base.Subcommand = "delete"
		base.Options["scope"] = parts[3]
	default:
		return Request{}, false
	}

	return base, true
}
