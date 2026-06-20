package commands

import (
	"strings"

	"github.com/sn0w/panda2/internal/admin"
)

const (
	selectPrefix           = "p2s"
	selectOpRolePermission = "rp"
)

func rolePermissionSelectID(userID, roleID string) string {
	return strings.Join([]string{selectPrefix, selectOpRolePermission, cleanConfirmationPart(userID), cleanConfirmationPart(roleID)}, ":")
}

func rolePermissionSelectResponse(userID, roleID string) Response {
	return Response{
		Content:   "Choose the permission to grant this role.",
		Ephemeral: true,
		Select: &Select{
			ID:          rolePermissionSelectID(userID, roleID),
			Placeholder: "Choose permission",
			Options:     permissionSelectOptions(),
		},
	}
}

func RequestFromSelectID(id string, values []string, base Request) (Request, bool) {
	parts := strings.Split(id, ":")
	if len(parts) != 4 || parts[0] != selectPrefix || parts[2] != cleanConfirmationPart(base.UserID) {
		return Request{}, false
	}
	if parts[1] != selectOpRolePermission || len(values) != 1 || !admin.IsPermissionNameAllowed(values[0]) {
		return Request{}, false
	}

	base.Command = "admin"
	base.Subcommand = "roles"
	base.Options = map[string]string{
		"action":     "add",
		"role_id":    parts[3],
		"permission": values[0],
	}
	return base, true
}

func permissionSelectOptions() []SelectOption {
	return []SelectOption{
		{Label: "Assistant Use", Value: admin.PermissionAssistantUse, Description: "Ask Panda in allowed channels"},
		{Label: "Thread Mode", Value: admin.PermissionAssistantUseThreads, Description: "Start assistant chat threads"},
		{Label: "Attachments", Value: admin.PermissionAssistantAttachments, Description: "Use extracted attachment context"},
		{Label: "Memory Read", Value: admin.PermissionAssistantMemoryRead, Description: "Search server knowledge"},
		{Label: "Memory Write", Value: admin.PermissionAssistantMemoryWrite, Description: "Manage knowledge inputs"},
		{Label: "Moderation Use", Value: admin.PermissionModerationUse, Description: "Use moderation helper commands"},
		{Label: "Config Read", Value: admin.PermissionAdminConfigRead, Description: "Read admin configuration"},
		{Label: "Config Write", Value: admin.PermissionAdminConfigWrite, Description: "Change admin configuration"},
		{Label: "Usage Read", Value: admin.PermissionAdminUsageRead, Description: "View usage reports"},
		{Label: "Audit Read", Value: admin.PermissionAdminAuditRead, Description: "View audit events"},
		{Label: "Memory Manage", Value: admin.PermissionAdminMemoryManage, Description: "Manage server knowledge"},
	}
}
