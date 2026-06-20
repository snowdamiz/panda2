package commands

import (
	"strings"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/repository"
)

const (
	confirmationPrefix   = "p2c"
	ConfirmationCancelID = "p2c:cancel"

	confirmationOpAdminDisable  = "ad"
	confirmationOpMemoryDelete  = "md"
	confirmationOpRoleRemove    = "rr"
	confirmationOpChannelRemove = "cr"
	confirmationOpLimitRemove   = "lr"
)

func adminDisableConfirmationID(userID string) string {
	return confirmationID(confirmationOpAdminDisable, userID)
}

func memoryDeleteConfirmationID(userID, documentID string) string {
	return confirmationID(confirmationOpMemoryDelete, userID, documentID)
}

func roleRemoveConfirmationID(userID, roleID, permission string) string {
	return confirmationID(confirmationOpRoleRemove, userID, roleID, permission)
}

func channelRemoveConfirmationID(userID, channelID string) string {
	return confirmationID(confirmationOpChannelRemove, userID, channelID)
}

func limitRemoveConfirmationID(userID, scope, subjectID string) string {
	return confirmationID(confirmationOpLimitRemove, userID, scope, subjectID)
}

func confirmationID(op, userID string, args ...string) string {
	parts := []string{confirmationPrefix, op, cleanConfirmationPart(userID)}
	for _, arg := range args {
		parts = append(parts, cleanConfirmationPart(arg))
	}
	return strings.Join(parts, ":")
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

// RequestFromConfirmationID converts a button custom id into the same command request
// the slash command would have produced after confirmation.
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
	case confirmationOpMemoryDelete:
		if len(parts) != 4 {
			return Request{}, false
		}
		base.Subcommand = "memory"
		base.Options["action"] = "delete"
		base.Options["document_id"] = parts[3]
	case confirmationOpRoleRemove:
		if len(parts) != 5 {
			return Request{}, false
		}
		base.Subcommand = "roles"
		base.Options["action"] = "remove"
		base.Options["role_id"] = parts[3]
		base.Options["permission"] = firstNonEmpty(parts[4], admin.PermissionAssistantUse)
	case confirmationOpChannelRemove:
		if len(parts) != 4 {
			return Request{}, false
		}
		base.Subcommand = "channels"
		base.Options["action"] = "remove"
		base.Options["channel_id"] = parts[3]
	case confirmationOpLimitRemove:
		if len(parts) != 5 {
			return Request{}, false
		}
		base.Subcommand = "limits"
		base.Options["action"] = "remove"
		base.Options["scope"] = firstNonEmpty(parts[3], repository.BudgetScopeGuild)
		base.Options["subject_id"] = parts[4]
	default:
		return Request{}, false
	}

	return base, true
}
