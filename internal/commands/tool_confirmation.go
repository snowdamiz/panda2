package commands

import (
	"strings"

	"github.com/sn0w/panda2/internal/assistant"
)

const (
	toolConfirmationPrefix = "p2t"

	toolConfirmationOpKnowledgeDelete      = "kd"
	toolConfirmationOpBudgetLimitSet       = "bs"
	toolConfirmationOpBudgetLimitRemove    = "br"
	toolConfirmationOpRolePermissionAdd    = "rs"
	toolConfirmationOpRolePermissionRemove = "rr"
	toolConfirmationOpRoleProfileAdd       = "ra"
	toolConfirmationOpRoleProfileRemove    = "rp"
	toolConfirmationOpMemberRoleAdd        = "ma"
	toolConfirmationOpMemberRoleRemove     = "mr"
	toolConfirmationOpToolAccessAdd        = "ta"
	toolConfirmationOpToolAccessRemove     = "tr"
	toolConfirmationOpChannelRuleSet       = "cs"
	toolConfirmationOpChannelRuleRemove    = "cr"
	toolConfirmationOpComposedToolApprove  = "ca"
	toolConfirmationOpComposedToolRollback = "cb"
	toolConfirmationEmptyValue             = "_"
	toolActionKnowledgeDelete              = "knowledge.delete"
	toolActionBudgetLimitSet               = "budget_limit.set"
	toolActionBudgetLimitRemove            = "budget_limit.remove"
	toolActionRolePermissionAdd            = "role_permission.add"
	toolActionRolePermissionRemove         = "role_permission.remove"
	toolActionRoleProfileAdd               = "role_profile.add"
	toolActionRoleProfileRemove            = "role_profile.remove"
	toolActionMemberRoleAdd                = "member_role.add"
	toolActionMemberRoleRemove             = "member_role.remove"
	toolActionToolAccessAdd                = "tool_access.add"
	toolActionToolAccessRemove             = "tool_access.remove"
	toolActionChannelRuleSet               = "channel_rule.set"
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
	case toolConfirmationOpBudgetLimitSet:
		if len(parts) != 7 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionBudgetLimitSet
		request.Options["scope"] = decodeToolConfirmationPart(parts[3])
		request.Options["subject_id"] = decodeToolConfirmationPart(parts[4])
		request.Options["limit"] = decodeToolConfirmationPart(parts[5])
		request.Options["window_seconds"] = decodeToolConfirmationPart(parts[6])
	case toolConfirmationOpBudgetLimitRemove:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionBudgetLimitRemove
		request.Options["scope"] = decodeToolConfirmationPart(parts[3])
		request.Options["subject_id"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpRolePermissionAdd:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionRolePermissionAdd
		request.Options["role_id"] = decodeToolConfirmationPart(parts[3])
		request.Options["permission"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpRolePermissionRemove:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionRolePermissionRemove
		request.Options["role_id"] = decodeToolConfirmationPart(parts[3])
		request.Options["permission"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpRoleProfileRemove:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionRoleProfileRemove
		request.Options["role_id"] = decodeToolConfirmationPart(parts[3])
		request.Options["profile"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpRoleProfileAdd:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionRoleProfileAdd
		request.Options["role_id"] = decodeToolConfirmationPart(parts[3])
		request.Options["profile"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpMemberRoleAdd:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionMemberRoleAdd
		request.Options["user_id"] = decodeToolConfirmationPart(parts[3])
		request.Options["role_id"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpMemberRoleRemove:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionMemberRoleRemove
		request.Options["user_id"] = decodeToolConfirmationPart(parts[3])
		request.Options["role_id"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpToolAccessAdd:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionToolAccessAdd
		request.Options["tool_name"] = decodeToolConfirmationPart(parts[3])
		request.Options["role_id"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpToolAccessRemove:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionToolAccessRemove
		request.Options["tool_name"] = decodeToolConfirmationPart(parts[3])
		request.Options["role_id"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpChannelRuleSet:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionChannelRuleSet
		request.Options["channel_id"] = decodeToolConfirmationPart(parts[3])
		request.Options["rule"] = decodeToolConfirmationPart(parts[4])
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
	case toolActionBudgetLimitSet:
		if strings.TrimSpace(arguments["scope"]) == "" || strings.TrimSpace(arguments["limit"]) == "" || strings.TrimSpace(arguments["window_seconds"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpBudgetLimitSet
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["scope"]), encodeToolConfirmationPart(arguments["subject_id"]), encodeToolConfirmationPart(arguments["limit"]), encodeToolConfirmationPart(arguments["window_seconds"])), ":")
	case toolActionBudgetLimitRemove:
		if strings.TrimSpace(arguments["scope"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpBudgetLimitRemove
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["scope"]), encodeToolConfirmationPart(arguments["subject_id"])), ":")
	case toolActionRolePermissionAdd:
		if strings.TrimSpace(arguments["role_id"]) == "" || strings.TrimSpace(arguments["permission"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpRolePermissionAdd
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["role_id"]), encodeToolConfirmationPart(arguments["permission"])), ":")
	case toolActionRolePermissionRemove:
		if strings.TrimSpace(arguments["role_id"]) == "" || strings.TrimSpace(arguments["permission"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpRolePermissionRemove
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["role_id"]), encodeToolConfirmationPart(arguments["permission"])), ":")
	case toolActionRoleProfileRemove:
		if strings.TrimSpace(arguments["role_id"]) == "" || strings.TrimSpace(arguments["profile"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpRoleProfileRemove
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["role_id"]), encodeToolConfirmationPart(arguments["profile"])), ":")
	case toolActionRoleProfileAdd:
		if strings.TrimSpace(arguments["role_id"]) == "" || strings.TrimSpace(arguments["profile"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpRoleProfileAdd
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["role_id"]), encodeToolConfirmationPart(arguments["profile"])), ":")
	case toolActionMemberRoleAdd:
		if strings.TrimSpace(arguments["user_id"]) == "" || strings.TrimSpace(arguments["role_id"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpMemberRoleAdd
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["user_id"]), encodeToolConfirmationPart(arguments["role_id"])), ":")
	case toolActionMemberRoleRemove:
		if strings.TrimSpace(arguments["user_id"]) == "" || strings.TrimSpace(arguments["role_id"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpMemberRoleRemove
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["user_id"]), encodeToolConfirmationPart(arguments["role_id"])), ":")
	case toolActionToolAccessAdd:
		if strings.TrimSpace(arguments["tool_name"]) == "" || strings.TrimSpace(arguments["role_id"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpToolAccessAdd
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["tool_name"]), encodeToolConfirmationPart(arguments["role_id"])), ":")
	case toolActionToolAccessRemove:
		if strings.TrimSpace(arguments["tool_name"]) == "" || strings.TrimSpace(arguments["role_id"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpToolAccessRemove
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["tool_name"]), encodeToolConfirmationPart(arguments["role_id"])), ":")
	case toolActionChannelRuleSet:
		if strings.TrimSpace(arguments["channel_id"]) == "" || strings.TrimSpace(arguments["rule"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpChannelRuleSet
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["channel_id"]), encodeToolConfirmationPart(arguments["rule"])), ":")
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
