package commands

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/sn0w/panda2/internal/assistant"
)

const (
	toolConfirmationPrefix = "p2t"
	toolConfirmationTTL    = 30 * time.Minute

	toolConfirmationOpKnowledgeDelete      = "kd"
	toolConfirmationOpBudgetLimitSet       = "bs"
	toolConfirmationOpBudgetLimitRemove    = "br"
	toolConfirmationOpRolePermissionAdd    = "rs"
	toolConfirmationOpRolePermissionRemove = "rr"
	toolConfirmationOpRoleProfileAdd       = "ra"
	toolConfirmationOpRoleProfileRemove    = "rp"
	toolConfirmationOpUserPermissionAdd    = "us"
	toolConfirmationOpUserPermissionRemove = "ur"
	toolConfirmationOpUserProfileAdd       = "ua"
	toolConfirmationOpUserProfileRemove    = "up"
	toolConfirmationOpDiscordRoleCreate    = "rc"
	toolConfirmationOpMemberRoleAdd        = "ma"
	toolConfirmationOpMemberRoleRemove     = "mr"
	toolConfirmationOpToolAccessAdd        = "ta"
	toolConfirmationOpToolAccessRemove     = "tr"
	toolConfirmationOpToolAccessDeny       = "td"
	toolConfirmationOpToolUserAccessAdd    = "tu"
	toolConfirmationOpToolUserAccessRemove = "tv"
	toolConfirmationOpToolUserAccessDeny   = "tw"
	toolConfirmationOpChannelRuleSet       = "cs"
	toolConfirmationOpChannelRuleRemove    = "cr"
	toolConfirmationOpComposedToolApprove  = "ca"
	toolConfirmationOpComposedToolRollback = "cb"
	toolConfirmationOpDiscordPollCreate    = "pc"
	toolConfirmationEmptyValue             = "_"
	toolActionKnowledgeDelete              = "knowledge.delete"
	toolActionBudgetLimitSet               = "budget_limit.set"
	toolActionBudgetLimitRemove            = "budget_limit.remove"
	toolActionRolePermissionAdd            = "role_permission.add"
	toolActionRolePermissionRemove         = "role_permission.remove"
	toolActionRoleProfileAdd               = "role_profile.add"
	toolActionRoleProfileRemove            = "role_profile.remove"
	toolActionUserPermissionAdd            = "user_permission.add"
	toolActionUserPermissionRemove         = "user_permission.remove"
	toolActionUserProfileAdd               = "user_profile.add"
	toolActionUserProfileRemove            = "user_profile.remove"
	toolActionDiscordRoleCreate            = "discord_role.create"
	toolActionMemberRoleAdd                = "member_role.add"
	toolActionMemberRoleRemove             = "member_role.remove"
	toolActionToolAccessAdd                = "tool_access.add"
	toolActionToolAccessRemove             = "tool_access.remove"
	toolActionToolAccessDeny               = "tool_access.deny"
	toolActionToolAccessOpen               = "tool_access.open"
	toolActionChannelRuleSet               = "channel_rule.set"
	toolActionChannelRuleRemove            = "channel_rule.remove"
	toolActionComposedToolApprove          = "composed_tool.approve"
	toolActionComposedToolRollback         = "composed_tool.rollback"
	toolActionDiscordPollCreate            = "discord_poll.create"
	toolActionDiscordWriteExecute          = "discord_write.execute"
	toolActionOwnerOpsDrain                = "owner_ops.drain"
	toolActionOwnerOpsResume               = "owner_ops.resume"
	toolActionOwnerOpsIncidentEnable       = "owner_ops.incident_enable"
	toolActionOwnerOpsIncidentDisable      = "owner_ops.incident_disable"
)

type ToolConfirmationRequest struct {
	Request Request
	Action  string
	Options map[string]string
}

type pendingToolConfirmation struct {
	userID    string
	action    string
	options   map[string]string
	expiresAt time.Time
}

type pendingToolConfirmationStore struct {
	mu    sync.Mutex
	items map[string]pendingToolConfirmation
}

var pendingToolConfirmations = &pendingToolConfirmationStore{items: map[string]pendingToolConfirmation{}}

func ToolConfirmationFromAssistant(userID string, pending *assistant.InteractionConfirmation) *Confirmation {
	if pending == nil {
		return nil
	}
	if pendingToolConfirmationAction(pending.Action) {
		return confirmationFromPendingTool(userID, pending)
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

func ToolConfirmationsFromAssistant(userID string, pending []assistant.InteractionConfirmation) []Confirmation {
	confirmations := make([]Confirmation, 0, len(pending))
	seen := map[string]struct{}{}
	for index := range pending {
		confirmation := ToolConfirmationFromAssistant(userID, &pending[index])
		if confirmation == nil || strings.TrimSpace(confirmation.ID) == "" {
			continue
		}
		if _, ok := seen[confirmation.ID]; ok {
			continue
		}
		seen[confirmation.ID] = struct{}{}
		confirmations = append(confirmations, *confirmation)
	}
	return confirmations
}

func ownerOpsConfirmationAction(action string) bool {
	switch action {
	case toolActionOwnerOpsDrain,
		toolActionOwnerOpsResume,
		toolActionOwnerOpsIncidentEnable,
		toolActionOwnerOpsIncidentDisable:
		return true
	default:
		return false
	}
}

func pendingToolConfirmationAction(action string) bool {
	switch action {
	case toolActionDiscordPollCreate,
		toolActionDiscordRoleCreate,
		toolActionToolAccessOpen,
		toolActionDiscordWriteExecute:
		return true
	default:
		return ownerOpsConfirmationAction(action)
	}
}

func confirmationFromPendingTool(userID string, pending *assistant.InteractionConfirmation) *Confirmation {
	id := pendingToolConfirmations.put(userID, pending.Action, pending.Arguments)
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
	if pending, ok := pendingToolConfirmations.take(id, base.UserID); ok {
		request.Action = pending.action
		request.Options = pending.options
		return request, true
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
	case toolConfirmationOpUserPermissionAdd:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionUserPermissionAdd
		request.Options["user_id"] = decodeToolConfirmationPart(parts[3])
		request.Options["permission"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpUserPermissionRemove:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionUserPermissionRemove
		request.Options["user_id"] = decodeToolConfirmationPart(parts[3])
		request.Options["permission"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpUserProfileRemove:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionUserProfileRemove
		request.Options["user_id"] = decodeToolConfirmationPart(parts[3])
		request.Options["profile"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpUserProfileAdd:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionUserProfileAdd
		request.Options["user_id"] = decodeToolConfirmationPart(parts[3])
		request.Options["profile"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpDiscordRoleCreate:
		if len(parts) != 4 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionDiscordRoleCreate
		request.Options["name"] = decodeToolConfirmationPart(parts[3])
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
	case toolConfirmationOpToolAccessDeny:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionToolAccessDeny
		request.Options["tool_name"] = decodeToolConfirmationPart(parts[3])
		request.Options["role_id"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpToolUserAccessAdd:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionToolAccessAdd
		request.Options["tool_name"] = decodeToolConfirmationPart(parts[3])
		request.Options["user_id"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpToolUserAccessRemove:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionToolAccessRemove
		request.Options["tool_name"] = decodeToolConfirmationPart(parts[3])
		request.Options["user_id"] = decodeToolConfirmationPart(parts[4])
	case toolConfirmationOpToolUserAccessDeny:
		if len(parts) != 5 {
			return ToolConfirmationRequest{}, false
		}
		request.Action = toolActionToolAccessDeny
		request.Options["tool_name"] = decodeToolConfirmationPart(parts[3])
		request.Options["user_id"] = decodeToolConfirmationPart(parts[4])
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

func (s *pendingToolConfirmationStore) put(userID, action string, options map[string]string) string {
	token, ok := randomConfirmationToken()
	if !ok {
		return ""
	}
	id := strings.Join([]string{toolConfirmationPrefix, toolConfirmationOpDiscordPollCreate, cleanConfirmationPart(userID), token}, ":")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	s.items[id] = pendingToolConfirmation{
		userID:    cleanConfirmationPart(userID),
		action:    action,
		options:   cloneConfirmationOptions(options),
		expiresAt: time.Now().Add(toolConfirmationTTL),
	}
	return id
}

func (s *pendingToolConfirmationStore) take(id, userID string) (pendingToolConfirmation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.pruneLocked(now)
	item, ok := s.items[id]
	if !ok {
		return pendingToolConfirmation{}, false
	}
	if now.After(item.expiresAt) {
		delete(s.items, id)
		return pendingToolConfirmation{}, false
	}
	if item.userID != cleanConfirmationPart(userID) {
		return pendingToolConfirmation{}, false
	}
	delete(s.items, id)
	item.options = cloneConfirmationOptions(item.options)
	return item, true
}

func (s *pendingToolConfirmationStore) pruneLocked(now time.Time) {
	for id, item := range s.items {
		if now.After(item.expiresAt) {
			delete(s.items, id)
		}
	}
}

func randomConfirmationToken() (string, bool) {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", false
	}
	return hex.EncodeToString(bytes[:]), true
}

func cloneConfirmationOptions(options map[string]string) map[string]string {
	clone := map[string]string{}
	for key, value := range options {
		clone[key] = value
	}
	return clone
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
	case toolActionUserPermissionAdd:
		if strings.TrimSpace(arguments["user_id"]) == "" || strings.TrimSpace(arguments["permission"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpUserPermissionAdd
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["user_id"]), encodeToolConfirmationPart(arguments["permission"])), ":")
	case toolActionUserPermissionRemove:
		if strings.TrimSpace(arguments["user_id"]) == "" || strings.TrimSpace(arguments["permission"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpUserPermissionRemove
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["user_id"]), encodeToolConfirmationPart(arguments["permission"])), ":")
	case toolActionUserProfileRemove:
		if strings.TrimSpace(arguments["user_id"]) == "" || strings.TrimSpace(arguments["profile"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpUserProfileRemove
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["user_id"]), encodeToolConfirmationPart(arguments["profile"])), ":")
	case toolActionUserProfileAdd:
		if strings.TrimSpace(arguments["user_id"]) == "" || strings.TrimSpace(arguments["profile"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpUserProfileAdd
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["user_id"]), encodeToolConfirmationPart(arguments["profile"])), ":")
	case toolActionDiscordRoleCreate:
		if strings.TrimSpace(arguments["name"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpDiscordRoleCreate
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["name"])), ":")
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
		if strings.TrimSpace(arguments["tool_name"]) == "" {
			return ""
		}
		if userID := strings.TrimSpace(arguments["user_id"]); userID != "" {
			prefix[1] = toolConfirmationOpToolUserAccessAdd
			return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["tool_name"]), encodeToolConfirmationPart(userID)), ":")
		}
		if strings.TrimSpace(arguments["role_id"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpToolAccessAdd
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["tool_name"]), encodeToolConfirmationPart(arguments["role_id"])), ":")
	case toolActionToolAccessRemove:
		if strings.TrimSpace(arguments["tool_name"]) == "" {
			return ""
		}
		if userID := strings.TrimSpace(arguments["user_id"]); userID != "" {
			prefix[1] = toolConfirmationOpToolUserAccessRemove
			return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["tool_name"]), encodeToolConfirmationPart(userID)), ":")
		}
		if strings.TrimSpace(arguments["role_id"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpToolAccessRemove
		return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["tool_name"]), encodeToolConfirmationPart(arguments["role_id"])), ":")
	case toolActionToolAccessDeny:
		if strings.TrimSpace(arguments["tool_name"]) == "" {
			return ""
		}
		if userID := strings.TrimSpace(arguments["user_id"]); userID != "" {
			prefix[1] = toolConfirmationOpToolUserAccessDeny
			return strings.Join(append(prefix, encodeToolConfirmationPart(arguments["tool_name"]), encodeToolConfirmationPart(userID)), ":")
		}
		if strings.TrimSpace(arguments["role_id"]) == "" {
			return ""
		}
		prefix[1] = toolConfirmationOpToolAccessDeny
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
