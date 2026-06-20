package commands

import (
	"strings"

	"github.com/sn0w/panda2/internal/assistant"
)

const (
	toolConfirmationPrefix = "p2t"

	toolConfirmationOpKnowledgeDelete      = "kd"
	toolConfirmationOpBudgetLimitRemove    = "br"
	toolConfirmationOpRolePermissionRemove = "rr"
	toolConfirmationOpChannelRuleRemove    = "cr"
	toolConfirmationOpComposedToolApprove  = "ca"
	toolConfirmationOpComposedToolRollback = "cb"
	toolConfirmationEmptyValue             = "_"
	toolActionKnowledgeDelete              = "knowledge.delete"
	toolActionBudgetLimitRemove            = "budget_limit.remove"
	toolActionRolePermissionRemove         = "role_permission.remove"
	toolActionChannelRuleRemove            = "channel_rule.remove"
	toolActionComposedToolApprove          = "composed_tool.approve"
	toolActionComposedToolRollback         = "composed_tool.rollback"
)

type ToolConfirmationRequest struct {
	Request Request
	Action  string
	Options map[string]string
}

func ToolConfirmationFromAssistant(userID string, pending *assistant.InteractionConfirmation) *Confirmation {
	if pending == nil {
		return nil
	}
	id := toolConfirmationID(userID, pending.Action, pending.Arguments)
	if id == "" {
		return nil
	}
	label := strings.TrimSpace(pending.ConfirmLabel)
	if label == "" {
		label = "Confirm"
	}
	return &Confirmation{
		ID:           id,
		ConfirmLabel: label,
		CancelID:     ConfirmationCancelID,
		CancelLabel:  "Cancel",
		Danger:       pending.Danger,
	}
}

func RequestFromToolConfirmationID(id string, base Request) (ToolConfirmationRequest, bool) {
	parts := strings.Split(id, ":")
	if len(parts) < 4 || parts[0] != toolConfirmationPrefix || parts[2] != cleanConfirmationPart(base.UserID) {
		return ToolConfirmationRequest{}, false
	}
	request := ToolConfirmationRequest{
		Request: base,
		Options: map[string]string{},
	}
	switch parts[1] {
	case toolConfirmationOpKnowledgeDelete:
		if len(parts) != 4 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionKnowledgeDelete
		request.Options["document_id"] = decodeToolConfirmationPart(parts[3])
	case toolConfirmationOpBudgetLimitRemove:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionBudgetLimitRemove
		request.Options["scope"] = decodeToolConfirmationPart(parts[3])
		request.Options["subject_id"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpRolePermissionRemove:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionRolePermissionRemove
		request.Options["role_id"] = decodeToolConfirmationPart(parts[3])
		request.Options["permission"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpChannelRuleRemove:
		if len(parts) != 4 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionChannelRuleRemove
		request.Options["channel_id"] = decodeToolConfirmationPart(parts[3])
	case toolConfirmationOpComposedToolApprove:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionComposedToolApprove
		request.Options["tool_name"] = decodeToolConfirmationPart(parts[3])
		request.Options["version"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpComposedToolRollback:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionComposedToolRollback
		request.Options["tool_name"] = decodeToolConfirmationPart(parts[3])
		request.Options["version"] = decodeToolConfirmationPart(parts[4])
	default:
		return ToolConfirmationRequest{}, false
	}
	return request, true
}

func toolConfirmationID(userID, action string, arguments map[string]string) string {
	prefix := []string{toolConfirmationPrefix, "", cleanConfirmationPart(userID)}
	switch action {
	case toolActionKnowledgeDelete:
		if strings.TrimSpace(arguments["document_id"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpKnowledgeDelete
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["document_id"])), ":")
	case toolActionBudgetLimitRemove:
		if strings.TrimSpace(arguments["scope"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpBudgetLimitRemove
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["scope"]), encodeToolConfirmationPart(arguments["subject_id"])), ":")
	case toolActionRolePermissionRemove:
		if strings.TrimSpace(arguments["role_id"]) == "" || strings.TrimSpace(arguments["permission"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpRolePermissionRemove
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["role_id"]), encodeToolConfirmationPart(arguments["permission"])), ":")
	case toolActionChannelRuleRemove:
		if strings.TrimSpace(arguments["channel_id"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpChannelRuleRemove
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["channel_id"])), ":")
	case toolActionComposedToolApprove:
		if strings.TrimSpace(arguments["tool_name"]) == "" || strings.TrimSpace(arguments["version"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpComposedToolApprove
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["tool_name"]), encodeToolConfirmationPart(arguments["version"])), ":")
	case toolActionComposedToolRollback:
		if strings.TrimSpace(arguments["tool_name"]) == "" || strings.TrimSpace(arguments["version"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpComposedToolRollback
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["tool_name"]), encodeToolConfirmationPart(arguments["version"])), ":")
	default:
		return ""
	}
}

func encodeToolConfirmationPart(value string) string {
	value = cleanConfirmationPart(value)
	if value == "" {
		return toolConfirmationEmptyValue
	}
	return value
}

func decodeToolConfirmationPart(value string) string {
	if value == toolConfirmationEmptyValue {
		return ""
	}
	return value
}
