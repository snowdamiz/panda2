package commands

import "strings"

const (
	confirmationPrefix   = "p2c"
	ConfirmationCancelID = "p2c:cancel"

	confirmationOpAdminDisable = "ad"
)

func adminDisableConfirmationID(userID string) string {
	return confirmationID(confirmationOpAdminDisable, userID)
}

func confirmationID(op, userID string) string {
	return strings.Join([]string{confirmationPrefix, op, cleanConfirmationPart(userID)}, ":")
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
		Confirmation: &Confirmation{
			ID:           id,
			ConfirmLabel: label,
			CancelID:     ConfirmationCancelID,
			CancelLabel:  "Cancel",
			Danger:       true,
		},
	}
}

func confirmed(request Request, id string) bool {
	return strings.TrimSpace(request.Options["confirm"]) == id
}

// RequestFromConfirmationID converts a button custom id into the command request
// the slash confirmation would have produced.
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
	default:
		return Request{}, false
	}

	return base, true
}
