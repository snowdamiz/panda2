package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/assistant"
	"github.com/sn0w/panda2/internal/composed"
	contextsvc "github.com/sn0w/panda2/internal/context"
	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/ops"
	"github.com/sn0w/panda2/internal/ratelimit"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/security"
	"github.com/sn0w/panda2/internal/store"
)

type Router struct {
	admin       *admin.Service
	assistant   *assistant.Service
	composed    *composed.Service
	context     *contextsvc.Service
	threads     ThreadManager
	attachments AttachmentReader
	ops         *ops.Service
	rateLimit   *ratelimit.Limiter
}

type ThreadManager interface {
	EnsureChatThread(ctx context.Context, request ThreadRequest) (Thread, error)
}

type AttachmentReader interface {
	Get(ctx context.Context, guildID string, id uint) (store.Attachment, error)
}

func NewRouter(adminService *admin.Service, assistantService *assistant.Service, opsService *ops.Service, limiter *ratelimit.Limiter) *Router {
	return &Router{admin: adminService, assistant: assistantService, ops: opsService, rateLimit: limiter}
}

func (r *Router) WithContextService(contextService *contextsvc.Service) *Router {
	r.context = contextService
	return r
}

func (r *Router) WithThreadManager(threadManager ThreadManager) *Router {
	r.threads = threadManager
	return r
}

func (r *Router) WithAttachmentReader(attachmentReader AttachmentReader) *Router {
	r.attachments = attachmentReader
	return r
}

func (r *Router) WithComposedService(composedService *composed.Service) *Router {
	r.composed = composedService
	return r
}

func (r *Router) Handle(ctx context.Context, request Request) Response {
	switch strings.ToLower(request.Command) {
	case "ping":
		return Response{Content: "pong", Ephemeral: true}
	case "help":
		return Response{Content: "Talk naturally in Discord with the word `Panda`, like `Panda is this true?`; Panda uses the model to decide whether to answer. Message context menus can explain or summarize. Admins can use `/admin setup`, `/admin model`, `/admin prompt`, `/admin audit`, `/admin enable`, and `/admin disable`; usage, limits, server knowledge, role/channel access, memory consent, and moderation guidance are handled through Panda chat/tools.", Ephemeral: true}
	case "admin":
		return r.handleAdmin(ctx, request)
	case "ops":
		return r.handleOps(ctx, request)
	case "tool":
		return r.handleTool(ctx, request)
	case "ask":
		return r.handleAsk(ctx, request, "ask")
	case "chat":
		return r.handleChat(ctx, request)
	case "summarize", "explain", "rewrite", "translate":
		return r.handleTask(ctx, request)
	default:
		return Response{Content: "Unknown command.", Ephemeral: true}
	}
}

func (r *Router) HandleNaturalMessage(ctx context.Context, request Request) Response {
	message := strings.TrimSpace(firstNonEmpty(request.Options["message"], request.Options["question"]))
	if message == "" {
		return Response{}
	}
	if denied := r.ensureAssistantAllowed(ctx, request); denied.Content != "" {
		return Response{}
	}
	decision, err := r.assistant.ClassifyNaturalMessage(ctx, assistant.NaturalMessageRequest{
		GuildID:          request.GuildID,
		UserID:           request.UserID,
		ChannelID:        request.ChannelID,
		Content:          message,
		BotMentioned:     truthyOption(request.Options["bot_mentioned"]),
		ReplyContent:     request.Options["reply_text"],
		ReplyMessageID:   request.Options["reply_message_id"],
		ReplyAuthorIsBot: truthyOption(request.Options["reply_author_is_bot"]),
	})
	if err != nil || !decision.Respond {
		return Response{}
	}
	request.Command = "chat"
	if request.Options == nil {
		request.Options = map[string]string{}
	}
	request.Options["question"] = decision.Prompt
	return r.handleChatMode(ctx, request, false)
}

func (r *Router) handleOps(ctx context.Context, request Request) Response {
	if !request.IsOwner {
		return Response{Content: "Only a bot owner can use ops commands.", Ephemeral: true}
	}
	switch strings.ToLower(request.Subcommand) {
	case "health":
		health, err := r.ops.Health(ctx)
		if err != nil {
			return Response{Content: "Ops health check failed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Health: sqlite=%s discord=%s shards=%s openrouter=%s queued_jobs=%d guild_configs=%d draining=%t incident=%t data_dir=`%s`.", health.SQLite, health.Discord, health.Shards, health.OpenRouter, health.QueuedJobs, health.ConfiguredGuildCount, health.Draining, health.Incident, health.DataDir), Ephemeral: true}
	case "guilds":
		health, err := r.ops.Health(ctx)
		if err != nil {
			return Response{Content: "Guild lookup failed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Configured guilds: %d.", health.ConfiguredGuildCount), Ephemeral: true}
	case "drain":
		r.ops.Drain()
		return Response{Content: "Queue worker is draining and will not claim new jobs.", Ephemeral: true}
	case "resume":
		r.ops.Resume()
		return Response{Content: "Queue worker resumed job processing.", Ephemeral: true}
	case "incident":
		action := strings.ToLower(strings.TrimSpace(request.Options["action"]))
		switch action {
		case "enable":
			r.ops.EnableIncident()
			return Response{Content: "Incident mode enabled.", Ephemeral: true}
		case "disable":
			r.ops.DisableIncident()
			return Response{Content: "Incident mode disabled.", Ephemeral: true}
		default:
			health, err := r.ops.Health(ctx)
			if err != nil {
				return Response{Content: "Incident status lookup failed.", Ephemeral: true}
			}
			return Response{Content: fmt.Sprintf("Incident mode: %t.", health.Incident), Ephemeral: true}
		}
	case "reload":
		if err := r.ops.Reload(ctx); err != nil {
			return Response{Content: "Runtime config reload check failed.", Ephemeral: true}
		}
		return Response{Content: "Runtime config reload check passed.", Ephemeral: true}
	default:
		return Response{Content: "Unknown ops command.", Ephemeral: true}
	}
}

func (r *Router) handleAdmin(ctx context.Context, request Request) Response {
	if !request.IsGuildAdmin && !request.IsOwner {
		return Response{Content: "Only a server administrator can use admin commands.", Ephemeral: true}
	}
	if request.GuildID == "" {
		return Response{Content: "Admin commands must be run inside a Discord server.", Ephemeral: true}
	}

	switch strings.ToLower(request.Subcommand) {
	case "setup":
		return r.handleAdminSetup(ctx, request)
	case "model":
		return r.handleAdminModel(ctx, request)
	case "prompt":
		return r.handleAdminPrompt(ctx, request)
	case "audit":
		return r.handleAdminAudit(ctx, request)
	case "enable":
		return r.handleAdminToggle(ctx, request, true)
	case "disable":
		return r.handleAdminToggle(ctx, request, false)
	default:
		return Response{Content: "Unknown admin command.", Ephemeral: true}
	}
}

func (r *Router) handleAdminSetup(ctx context.Context, request Request) Response {
	config, err := r.admin.SetupGuild(ctx, request.GuildID, request.UserID)
	if err != nil {
		return Response{Content: "Setup failed. Please check the bot logs.", Ephemeral: true}
	}
	if config.AssistantEnabled {
		return Response{Content: fmt.Sprintf("Setup complete. Default model: `%s`.", config.DefaultModel), Ephemeral: true}
	}
	return Response{Content: "Setup complete. Assistant responses are currently disabled.", Ephemeral: true}
}

func (r *Router) handleAdminModel(ctx context.Context, request Request) Response {
	settings, parseErr := modelSettingsFromOptions(request.Options)
	if parseErr != nil {
		return Response{Content: parseErr.Error(), Ephemeral: true}
	}
	if dryRunRequested(request) {
		return dryRunResponse("model settings would be updated. %s", renderModelSettingsDryRun(settings))
	}
	config, err := r.admin.ConfigureModel(ctx, request.GuildID, request.UserID, settings)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return Response{Content: "Run `/admin setup` before changing the model.", Ephemeral: true}
		}
		return Response{Content: "Model update failed.", Ephemeral: true}
	}
	return Response{Content: fmt.Sprintf("Model settings updated. Default `%s`, %d fallback model(s), temperature %.2f, max response %d tokens, tool policy `%s`.", config.DefaultModel, fallbackModelCount(config.FallbackModels), config.Temperature, config.MaxResponseTokens, config.ToolPolicy), Ephemeral: true}
}

func (r *Router) handleAdminPrompt(ctx context.Context, request Request) Response {
	prompt := strings.TrimSpace(request.Options["prompt"])
	if dryRunRequested(request) {
		return dryRunResponse("server prompt would be updated (%d characters).", len(prompt))
	}
	if prompt == "" {
		return promptModalResponse(request.UserID)
	}
	config, err := r.admin.SetPrompt(ctx, request.GuildID, request.UserID, prompt)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return Response{Content: "Run `/admin setup` before changing the prompt.", Ephemeral: true}
		}
		return Response{Content: "Prompt update failed.", Ephemeral: true}
	}
	return Response{Content: fmt.Sprintf("Server prompt updated (%d characters).", len(config.SystemPromptOverlay)), Ephemeral: true}
}

func (r *Router) handleAdminToggle(ctx context.Context, request Request, enabled bool) Response {
	if dryRunRequested(request) {
		if enabled {
			return dryRunResponse("assistant responses would be enabled for this server.")
		}
		return dryRunResponse("assistant responses would be disabled for this server.")
	}
	if !enabled {
		confirmationID := adminDisableConfirmationID(request.UserID)
		if !confirmed(request, confirmationID) {
			return destructiveConfirmation(confirmationID, "Disable assistant", "This will pause assistant responses for this server.")
		}
	}
	_, err := r.admin.SetAssistantEnabled(ctx, request.GuildID, request.UserID, enabled)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return Response{Content: "Run `/admin setup` before changing assistant status.", Ephemeral: true}
		}
		return Response{Content: "Assistant status update failed.", Ephemeral: true}
	}
	if enabled {
		return Response{Content: "Assistant responses are enabled.", Ephemeral: true}
	}
	return Response{Content: "Assistant responses are disabled.", Ephemeral: true}
}

func (r *Router) handleAdminAudit(ctx context.Context, request Request) Response {
	events, err := r.admin.RecentAudit(ctx, request.GuildID, 10)
	if err != nil {
		return Response{Content: "Audit lookup failed.", Ephemeral: true}
	}
	if len(events) == 0 {
		return Response{Content: "No audit events recorded yet.", Ephemeral: true}
	}
	return Response{Content: renderAudit(events), Ephemeral: true}
}

func (r *Router) handleAsk(ctx context.Context, request Request, command string) Response {
	question := strings.TrimSpace(request.Options["question"])
	if question == "" {
		return Response{Content: "Please include a question.", Ephemeral: true}
	}
	if denied := r.ensureAssistantAllowed(ctx, request); denied.Content != "" {
		return denied
	}
	if limited := r.allowUser(request.UserID); limited.Content != "" {
		return limited
	}
	if denied := r.ensureBudgetAvailable(ctx, request); denied.Content != "" {
		return denied
	}

	answer, err := r.assistant.Ask(ctx, assistant.AskRequest{
		RequestID:          request.RequestID,
		GuildID:            request.GuildID,
		UserID:             request.UserID,
		ChannelID:          request.ChannelID,
		Question:           question,
		AllowedPermissions: r.allowedToolPermissions(ctx, request),
	})
	if err != nil {
		return assistantError(err)
	}
	if strings.TrimSpace(answer.Content) == "" {
		return Response{Content: "The model returned an empty response.", Ephemeral: true}
	}
	return responseFromAssistantAnswer(request.UserID, answer, "", "")
}

func (r *Router) handleChat(ctx context.Context, request Request) Response {
	return r.handleChatMode(ctx, request, true)
}

func (r *Router) handleChatMode(ctx context.Context, request Request, threaded bool) Response {
	question := strings.TrimSpace(request.Options["question"])
	if question == "" {
		return Response{Content: "Please include a message.", Ephemeral: true}
	}
	if denied := r.ensureAssistantAllowed(ctx, request); denied.Content != "" {
		return denied
	}
	if threaded && r.threads != nil && request.GuildID != "" {
		if denied := r.ensureThreadsAllowed(ctx, request); denied.Content != "" {
			return denied
		}
	}
	if limited := r.allowUser(request.UserID); limited.Content != "" {
		return limited
	}
	if denied := r.ensureBudgetAvailable(ctx, request); denied.Content != "" {
		return denied
	}

	chatChannelID := request.ChannelID
	threadID := ""
	threadName := ""
	if threaded && r.threads != nil && request.GuildID != "" {
		thread, err := r.threads.EnsureChatThread(ctx, ThreadRequest{
			GuildID:   request.GuildID,
			ChannelID: request.ChannelID,
			UserID:    request.UserID,
			Title:     chatThreadTitle(question),
		})
		if err != nil {
			return Response{Content: "I could not create a chat thread here. Please check my thread permissions.", Ephemeral: true}
		}
		chatChannelID = thread.ID
		threadID = thread.ID
		threadName = thread.Name
	}

	answer, err := r.assistant.Chat(ctx, assistant.AskRequest{
		RequestID:          request.RequestID,
		GuildID:            request.GuildID,
		UserID:             request.UserID,
		ChannelID:          chatChannelID,
		ThreadID:           threadID,
		Question:           question,
		AllowedPermissions: r.allowedToolPermissions(ctx, request),
	})
	if err != nil {
		return assistantError(err)
	}
	return responseFromAssistantAnswer(request.UserID, answer, threadID, threadName)
}

func (r *Router) handleTask(ctx context.Context, request Request) Response {
	if denied := r.ensureAssistantAllowed(ctx, request); denied.Content != "" {
		return denied
	}
	input, contextError := r.taskInput(ctx, request)
	if contextError.Content != "" {
		return contextError
	}
	if limited := r.allowUser(request.UserID); limited.Content != "" {
		return limited
	}
	if denied := r.ensureBudgetAvailable(ctx, request); denied.Content != "" {
		return denied
	}

	task := BackgroundTask{
		RequestID:          request.RequestID,
		GuildID:            request.GuildID,
		UserID:             request.UserID,
		ChannelID:          request.ChannelID,
		Command:            request.Command,
		Input:              input,
		Tone:               request.Options["tone"],
		Language:           request.Options["language"],
		Detail:             request.Options["detail"],
		AllowedPermissions: permissionNames(r.allowedToolPermissions(ctx, request)),
	}
	if shouldBackgroundTask(request, input) {
		return Response{Content: "Queued long summary. The result will replace this response when it is ready.", Background: &task}
	}
	return r.HandleBackgroundTask(ctx, task)
}

func (r *Router) HandleBackgroundTask(ctx context.Context, task BackgroundTask) Response {
	answer, err := r.assistant.CompleteTask(ctx, assistant.TaskRequest{
		RequestID:          task.RequestID,
		GuildID:            task.GuildID,
		UserID:             task.UserID,
		ChannelID:          task.ChannelID,
		Command:            task.Command,
		Input:              task.Input,
		Tone:               task.Tone,
		Language:           task.Language,
		Detail:             task.Detail,
		AllowedPermissions: permissionsFromNames(task.AllowedPermissions),
	})
	if err != nil {
		return assistantError(err)
	}
	return responseFromAssistantAnswer(task.UserID, answer, "", "")
}

func (r *Router) HandleToolConfirmation(ctx context.Context, request ToolConfirmationRequest) Response {
	if request.Request.GuildID == "" {
		return Response{Content: "This confirmation must be used inside a Discord server.", Ephemeral: true}
	}
	switch request.Action {
	case toolActionKnowledgeDelete:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanManageMemory, "You do not have permission to manage server knowledge."); denied.Content != "" {
			return denied
		}
		documentID, err := strconv.ParseUint(strings.TrimSpace(request.Options["document_id"]), 10, 64)
		if err != nil || documentID == 0 {
			return Response{Content: "That knowledge deletion confirmation is invalid.", Ephemeral: true}
		}
		if err := r.admin.DeleteMemoryDocument(ctx, request.Request.GuildID, request.Request.UserID, uint(documentID)); err != nil {
			return toolConfirmationError(err, "Knowledge document could not be deleted.", "That knowledge document was not found.")
		}
		return Response{Content: fmt.Sprintf("Deleted knowledge document `%d`.", documentID), Ephemeral: true}
	case toolActionBudgetLimitRemove:
		scope := strings.ToLower(strings.TrimSpace(request.Options["scope"]))
		if !validBudgetScope(scope) {
			return Response{Content: "That budget-limit confirmation is invalid.", Ephemeral: true}
		}
		if scope == repository.BudgetScopeGlobal {
			if !request.Request.IsOwner {
				return Response{Content: "Only a bot owner can remove global limits.", Ephemeral: true}
			}
		} else if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage limits."); denied.Content != "" {
			return denied
		}
		subjectID := strings.TrimSpace(request.Options["subject_id"])
		if scope == repository.BudgetScopeGuild && subjectID == "" {
			subjectID = request.Request.GuildID
		}
		if err := r.admin.RemoveBudgetLimit(ctx, request.Request.GuildID, request.Request.UserID, scope, subjectID); err != nil {
			return toolConfirmationError(err, "Budget limit could not be removed.", "That budget limit was not found.")
		}
		return Response{Content: fmt.Sprintf("Removed `%s` budget limit for `%s`.", scope, firstNonEmpty(subjectID, "global")), Ephemeral: true}
	case toolActionRolePermissionRemove:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage role permissions."); denied.Content != "" {
			return denied
		}
		roleID := strings.TrimSpace(request.Options["role_id"])
		permission := strings.TrimSpace(request.Options["permission"])
		if roleID == "" || !admin.IsPermissionNameAllowed(permission) {
			return Response{Content: "That role-permission confirmation is invalid.", Ephemeral: true}
		}
		if err := r.admin.RemoveRolePermission(ctx, request.Request.GuildID, request.Request.UserID, roleID, permission); err != nil {
			return toolConfirmationError(err, "Role permission could not be removed.", "That role permission was not found.")
		}
		return Response{Content: fmt.Sprintf("Removed `%s` from role `%s`.", permission, roleID), Ephemeral: true}
	case toolActionChannelRuleRemove:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage channel rules."); denied.Content != "" {
			return denied
		}
		channelID := strings.TrimSpace(request.Options["channel_id"])
		if channelID == "" {
			return Response{Content: "That channel-rule confirmation is invalid.", Ephemeral: true}
		}
		if err := r.admin.RemoveChannelRule(ctx, request.Request.GuildID, request.Request.UserID, channelID); err != nil {
			return toolConfirmationError(err, "Channel rule could not be removed.", "That channel rule was not found.")
		}
		return Response{Content: fmt.Sprintf("Removed channel access rule for `%s`.", channelID), Ephemeral: true}
	default:
		return Response{Content: "That confirmation is no longer supported.", Ephemeral: true}
	}
}

func (r *Router) taskInput(ctx context.Context, request Request) (string, Response) {
	input := strings.TrimSpace(firstNonEmpty(request.Options["text"], request.Options["question"]))
	if input != "" {
		return input, Response{}
	}
	if attachmentID := strings.TrimSpace(request.Options["attachment_id"]); attachmentID != "" {
		return r.attachmentInput(ctx, request, attachmentID)
	}
	if !hasContextOptions(request.Options) {
		return "", Response{Content: "Please include text, an `attachment_id`, a `message_id`, or a `recent_limit` to work with.", Ephemeral: true}
	}
	if r.context == nil {
		return "", Response{Content: "Discord context fetching is not configured for this runtime.", Ephemeral: true}
	}

	var packed contextsvc.PackedContext
	var err error
	targetType := "discord_recent_messages"
	targetID := request.ChannelID
	if messageID := strings.TrimSpace(request.Options["message_id"]); messageID != "" {
		targetType = "discord_message"
		targetID = messageID
		packed, err = r.context.MessageContext(ctx, contextsvc.MessageRef{
			GuildID:   request.GuildID,
			ChannelID: request.ChannelID,
			MessageID: messageID,
		})
	} else {
		limit := intOption(request.Options["recent_limit"], 10)
		packed, err = r.context.RecentMessagesContext(ctx, contextsvc.ChannelRef{
			GuildID:   request.GuildID,
			ChannelID: request.ChannelID,
		}, limit)
	}
	if err != nil {
		return "", Response{Content: "Discord context could not be fetched.", Ephemeral: true}
	}
	if strings.TrimSpace(packed.Text) == "" {
		return "", Response{Content: "No Discord context was available.", Ephemeral: true}
	}
	r.admin.RecordSensitiveReadAudit(ctx, request.GuildID, request.UserID, targetType, targetID, map[string]string{
		"command":      request.Command,
		"source_count": strconv.Itoa(len(packed.Citations)),
	})
	return packed.Text, Response{}
}

func (r *Router) attachmentInput(ctx context.Context, request Request, rawID string) (string, Response) {
	if r.attachments == nil {
		return "", Response{Content: "Attachment lookup is not configured for this runtime.", Ephemeral: true}
	}
	if denied := r.ensureAttachmentsAllowed(ctx, request); denied.Content != "" {
		return "", denied
	}
	id, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil || id == 0 {
		return "", Response{Content: "Provide a numeric `attachment_id`.", Ephemeral: true}
	}
	attachment, err := r.attachments.Get(ctx, request.GuildID, uint(id))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return "", Response{Content: "No matching extracted attachment was found.", Ephemeral: true}
		}
		return "", Response{Content: "Attachment lookup failed.", Ephemeral: true}
	}
	if strings.TrimSpace(attachment.ExtractedText) == "" {
		return "", Response{Content: "That attachment does not have extracted text.", Ephemeral: true}
	}
	r.admin.RecordSensitiveReadAudit(ctx, request.GuildID, request.UserID, "attachment", strconv.FormatUint(uint64(attachment.ID), 10), map[string]string{
		"command": request.Command,
	})
	return fmt.Sprintf("Extracted attachment `%s` (id %d):\n\n%s", attachment.Filename, attachment.ID, attachment.ExtractedText), Response{}
}

func (r *Router) ensureAssistantAllowed(ctx context.Context, request Request) Response {
	allowed, err := r.admin.CanUseAssistant(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: "You do not have permission to use Panda here.", Ephemeral: true}
	}
	return Response{}
}

func (r *Router) ensureThreadsAllowed(ctx context.Context, request Request) Response {
	allowed, err := r.admin.CanUseThreads(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: "You do not have permission to use Panda thread mode.", Ephemeral: true}
	}
	return Response{}
}

func (r *Router) ensureAttachmentsAllowed(ctx context.Context, request Request) Response {
	allowed, err := r.admin.CanUseAttachments(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: "You do not have permission to use Panda attachment context.", Ephemeral: true}
	}
	return Response{}
}

func (r *Router) ensureMemoryReadAllowed(ctx context.Context, request Request) Response {
	allowed, err := r.admin.CanReadMemory(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: "You do not have permission to search server knowledge.", Ephemeral: true}
	}
	return Response{}
}

func assistantAccessRequest(request Request) admin.AssistantAccessRequest {
	return admin.AssistantAccessRequest{
		GuildID:      request.GuildID,
		ChannelID:    request.ChannelID,
		RoleIDs:      request.RoleIDs,
		IsGuildAdmin: request.IsGuildAdmin,
		IsOwner:      request.IsOwner,
	}
}

func (r *Router) allowedToolPermissions(ctx context.Context, request Request) map[string]struct{} {
	permissions := map[string]struct{}{}
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantUse, r.admin.CanUseAssistant)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantAttachments, r.admin.CanUseAttachments)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantMemoryRead, r.admin.CanReadMemory)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionModerationUse, r.admin.CanUseModeration)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminConfigRead, r.admin.CanReadConfig)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminConfigWrite, r.admin.CanWriteConfig)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminUsageRead, r.admin.CanReadUsage)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminAuditRead, r.admin.CanReadAudit)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminMemoryManage, r.admin.CanManageMemory)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionToolComposeDraft, r.admin.CanDraftComposedTool)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionToolComposeApprove, r.admin.CanApproveComposedTool)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionToolComposeInvoke, r.admin.CanInvokeComposedTool)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionToolComposeAudit, r.admin.CanAuditComposedTool)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionOwnerOps, r.admin.CanUseOwnerOps)
	return permissions
}

func (r *Router) addPermissionIfAllowed(ctx context.Context, request Request, permissions map[string]struct{}, permission string, check func(context.Context, admin.AssistantAccessRequest) (bool, error)) {
	allowed, err := check(ctx, assistantAccessRequest(request))
	if err == nil && allowed {
		permissions[permission] = struct{}{}
	}
}

func permissionNames(permissions map[string]struct{}) []string {
	if len(permissions) == 0 {
		return nil
	}
	names := make([]string, 0, len(permissions))
	for permission := range permissions {
		names = append(names, permission)
	}
	sort.Strings(names)
	return names
}

func permissionsFromNames(names []string) map[string]struct{} {
	permissions := map[string]struct{}{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			permissions[name] = struct{}{}
		}
	}
	return permissions
}

func (r *Router) ensureBudgetAvailable(ctx context.Context, request Request) Response {
	denial, denied, err := r.admin.ConsumeBudget(ctx, repository.BudgetCheckRequest{
		GuildID:   request.GuildID,
		UserID:    request.UserID,
		ChannelID: request.ChannelID,
		Now:       time.Now().UTC(),
	})
	if err != nil {
		return Response{Content: "Budget lookup failed. Please try again later.", Ephemeral: true}
	}
	if denied {
		return Response{Content: fmt.Sprintf("This `%s` budget is exhausted. Try again in %s.", denial.Scope, denial.RetryAfter.Round(time.Second)), Ephemeral: true}
	}
	return Response{}
}

func (r *Router) allowUser(userID string) Response {
	if ok, retryAfter := r.rateLimit.Allow(userID); !ok {
		return Response{Content: fmt.Sprintf("You are sending requests too quickly. Try again in %s.", retryAfter.Round(time.Second)), Ephemeral: true}
	}
	return Response{}
}

func assistantError(err error) Response {
	switch {
	case errors.Is(err, llm.ErrNotConfigured):
		return Response{Content: "I cannot answer yet because `OPENROUTER_API_KEY` is not configured.", Ephemeral: true}
	case errors.Is(err, assistant.ErrAssistantDisabled):
		return Response{Content: "Assistant responses are disabled for this server.", Ephemeral: true}
	default:
		return Response{Content: "The model request failed. Please try again later.", Ephemeral: true}
	}
}

func responseFromAssistantAnswer(userID string, answer assistant.AskResponse, threadID, threadName string) Response {
	response := Response{Content: answer.Content, ThreadID: threadID, ThreadName: threadName}
	if confirmation := ToolConfirmationFromAssistant(userID, answer.Confirmation); confirmation != nil {
		response.Confirmation = confirmation
		response.Content = appendConfirmationNotice(response.Content, answer.Confirmation.Summary)
	}
	return response
}

func appendConfirmationNotice(content, summary string) string {
	content = strings.TrimSpace(content)
	summary = strings.TrimSpace(summary)
	if summary != "" && !strings.Contains(content, summary) {
		if content != "" {
			content += "\n\n"
		}
		content += summary
	}
	if content != "" {
		content += "\n\n"
	}
	return content + "Press the confirmation button to continue."
}

func (r *Router) ensureToolConfirmationPermission(ctx context.Context, request Request, check func(context.Context, admin.AssistantAccessRequest) (bool, error), denial string) Response {
	allowed, err := check(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: denial, Ephemeral: true}
	}
	return Response{}
}

func toolConfirmationError(err error, fallback, notFound string) Response {
	if errors.Is(err, repository.ErrNotFound) {
		return Response{Content: notFound, Ephemeral: true}
	}
	return Response{Content: fallback, Ephemeral: true}
}

func renderAudit(events []store.AuditEvent) string {
	var builder strings.Builder
	builder.WriteString("Recent audit events:\n")
	for _, event := range events {
		fmt.Fprintf(&builder, "- %s by `%s` at %s\n", event.Action, event.ActorID, event.CreatedAt.UTC().Format(time.RFC3339))
	}
	return security.SafeDiscordContent(builder.String())
}

func modelSettingsFromOptions(options map[string]string) (admin.ModelSettings, error) {
	settings := admin.ModelSettings{DefaultModel: strings.TrimSpace(options["model"])}

	if raw, ok := options["fallback_models"]; ok {
		settings.FallbackModelsSet = true
		settings.FallbackModels = csvValues(raw)
	}

	if raw := strings.TrimSpace(options["temperature"]); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return admin.ModelSettings{}, fmt.Errorf("Provide a numeric `temperature` between 0 and 2.")
		}
		settings.Temperature = value
		settings.TemperatureSet = true
	}

	if raw := firstNonEmpty(options["max_response_tokens"], options["max_tokens"]); strings.TrimSpace(raw) != "" {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return admin.ModelSettings{}, fmt.Errorf("Provide a numeric `max_response_tokens` value.")
		}
		settings.MaxResponseTokens = value
		settings.MaxResponseTokensSet = true
	}

	if raw, ok := options["tool_policy"]; ok {
		settings.ToolPolicy = strings.TrimSpace(raw)
		settings.ToolPolicySet = true
	}

	return settings, nil
}

func renderModelSettingsDryRun(settings admin.ModelSettings) string {
	var parts []string
	if strings.TrimSpace(settings.DefaultModel) != "" {
		parts = append(parts, fmt.Sprintf("default `%s`", settings.DefaultModel))
	}
	if settings.FallbackModelsSet {
		parts = append(parts, fmt.Sprintf("%d fallback model(s)", len(settings.FallbackModels)))
	}
	if settings.TemperatureSet {
		parts = append(parts, fmt.Sprintf("temperature %.2f", settings.Temperature))
	}
	if settings.MaxResponseTokensSet {
		parts = append(parts, fmt.Sprintf("max response %d tokens", settings.MaxResponseTokens))
	}
	if settings.ToolPolicySet {
		parts = append(parts, fmt.Sprintf("tool policy `%s`", settings.ToolPolicy))
	}
	if len(parts) == 0 {
		return "No model setting changes were provided."
	}
	return strings.Join(parts, ", ") + "."
}

func dryRunRequested(request Request) bool {
	return truthyOption(request.Options["dry_run"])
}

func truthyOption(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "y":
		return true
	default:
		return false
	}
}

func dryRunResponse(format string, args ...any) Response {
	return Response{Content: "Dry run: " + fmt.Sprintf(format, args...), Ephemeral: true}
}

func validBudgetScope(scope string) bool {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case repository.BudgetScopeGlobal, repository.BudgetScopeGuild, repository.BudgetScopeUser, repository.BudgetScopeChannel:
		return true
	default:
		return false
	}
}

func csvValues(value string) []string {
	parts := strings.Split(value, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			values = append(values, part)
		}
	}
	return values
}

func fallbackModelCount(value string) int {
	var models []string
	if err := json.Unmarshal([]byte(value), &models); err != nil {
		return 0
	}
	return len(models)
}

func hasContextOptions(options map[string]string) bool {
	return strings.TrimSpace(options["message_id"]) != "" || strings.TrimSpace(options["recent_limit"]) != ""
}

func intOption(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

func shouldBackgroundTask(request Request, input string) bool {
	if strings.ToLower(strings.TrimSpace(request.Command)) != "summarize" {
		return false
	}
	if strings.ToLower(strings.TrimSpace(request.Options["_async"])) != "true" {
		return false
	}
	if len(input) >= 3000 {
		return true
	}
	return intOption(request.Options["recent_limit"], 0) >= 25
}

func chatThreadTitle(question string) string {
	title := strings.TrimSpace(question)
	if title == "" {
		return "Panda chat"
	}
	title = strings.ReplaceAll(title, "\n", " ")
	if len(title) > 72 {
		title = strings.TrimSpace(title[:72])
	}
	return "Panda: " + title
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
