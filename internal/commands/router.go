package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/assistant"
	contextsvc "github.com/sn0w/panda2/internal/context"
	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/memory"
	"github.com/sn0w/panda2/internal/moderation"
	"github.com/sn0w/panda2/internal/ops"
	"github.com/sn0w/panda2/internal/ratelimit"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/security"
	"github.com/sn0w/panda2/internal/store"
)

type Router struct {
	admin       *admin.Service
	assistant   *assistant.Service
	moderation  *moderation.Service
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

func NewRouter(adminService *admin.Service, assistantService *assistant.Service, moderationService *moderation.Service, opsService *ops.Service, limiter *ratelimit.Limiter) *Router {
	return &Router{admin: adminService, assistant: assistantService, moderation: moderationService, ops: opsService, rateLimit: limiter}
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

func (r *Router) Handle(ctx context.Context, request Request) Response {
	switch strings.ToLower(request.Command) {
	case "ping":
		return Response{Content: "pong", Ephemeral: true}
	case "help":
		return Response{Content: "Talk naturally in Discord with the word `Panda`, like `Panda is this true?`; Panda uses the model to decide whether to answer. Fallbacks: `/search-memory`, `/memory-consent`, and message context menus for explain/summarize. Admins can use `/admin setup`, `/admin model`, `/admin usage`, `/admin limits`, `/admin prompt`, `/admin memory`, `/admin roles`, `/admin channels`, `/admin audit`, `/admin enable`, and `/admin disable`.", Ephemeral: true}
	case "admin":
		return r.handleAdmin(ctx, request)
	case "ops":
		return r.handleOps(ctx, request)
	case "mod":
		return r.handleMod(ctx, request)
	case "memory-consent":
		return r.handleMemoryConsent(ctx, request)
	case "ask":
		return r.handleAsk(ctx, request, "ask")
	case "chat":
		return r.handleChat(ctx, request)
	case "summarize", "explain", "rewrite", "translate":
		return r.handleTask(ctx, request)
	case "search-memory":
		return r.handleSearchMemory(ctx, request)
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

func (r *Router) handleMod(ctx context.Context, request Request) Response {
	if request.GuildID == "" {
		return Response{Content: "Moderator helpers must be used inside a Discord server.", Ephemeral: true}
	}
	allowed, err := r.admin.CanUseModeration(ctx, admin.AssistantAccessRequest{
		GuildID:      request.GuildID,
		ChannelID:    request.ChannelID,
		RoleIDs:      request.RoleIDs,
		IsGuildAdmin: request.IsGuildAdmin,
		IsOwner:      request.IsOwner,
	})
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: "You do not have permission to use moderator helpers.", Ephemeral: true}
	}

	subcommand := strings.ToLower(request.Subcommand)
	contextText := strings.TrimSpace(request.Options["text"])
	if subcommand == "history" {
		var response Response
		contextText, response = r.moderationHistoryContext(ctx, request, contextText)
		if response.Content != "" {
			return response
		}
	} else if contextText == "" {
		return Response{Content: "Please include moderation context text.", Ephemeral: true}
	}
	if limited := r.allowUser(request.UserID); limited.Content != "" {
		return limited
	}
	if denied := r.ensureBudgetAvailable(ctx, request); denied.Content != "" {
		return denied
	}

	modRequest := moderation.Request{
		GuildID:   request.GuildID,
		UserID:    request.UserID,
		ChannelID: request.ChannelID,
		Context:   contextText,
		SubjectID: request.Options["subject_id"],
		Tone:      request.Options["tone"],
	}
	var answer assistant.AskResponse
	switch subcommand {
	case "triage":
		answer, err = r.moderation.Triage(ctx, modRequest)
	case "note":
		answer, err = r.moderation.DraftNote(ctx, modRequest)
	case "slowmode":
		answer, err = r.moderation.RecommendSlowmode(ctx, modRequest)
	case "cleanup":
		answer, err = r.moderation.RecommendCleanup(ctx, modRequest)
	case "history":
		answer, err = r.moderation.SummarizeUserHistory(ctx, modRequest)
	default:
		return Response{Content: "Unknown moderator helper.", Ephemeral: true}
	}
	if err != nil {
		return assistantError(err)
	}
	r.admin.RecordModerationAudit(ctx, request.GuildID, request.UserID, "moderation."+strings.ToLower(request.Subcommand), request.Options["subject_id"])
	return Response{Content: answer.Content, Ephemeral: true}
}

func (r *Router) moderationHistoryContext(ctx context.Context, request Request, note string) (string, Response) {
	subjectID := strings.TrimSpace(request.Options["subject_id"])
	if subjectID == "" {
		return "", Response{Content: "Please include a `subject_id` for history summaries.", Ephemeral: true}
	}
	if r.context == nil {
		return "", Response{Content: "Discord context fetching is not configured for this runtime.", Ephemeral: true}
	}
	packed, err := r.context.RecentUserMessagesContext(ctx, contextsvc.ChannelRef{
		GuildID:   request.GuildID,
		ChannelID: request.ChannelID,
	}, subjectID, intOption(request.Options["recent_limit"], 50))
	if err != nil {
		return "", Response{Content: "Discord context could not be fetched.", Ephemeral: true}
	}
	if len(packed.Citations) == 0 {
		return "", Response{Content: "No recent visible messages were found for that subject in this channel.", Ephemeral: true}
	}
	r.admin.RecordSensitiveReadAudit(ctx, request.GuildID, request.UserID, "moderation_user_history", subjectID, map[string]string{
		"command":      "mod.history",
		"source_count": strconv.Itoa(len(packed.Citations)),
	})
	if strings.TrimSpace(note) != "" {
		return "Moderator-provided context:\n" + note + "\n\nRecent subject history:\n" + packed.Text, Response{}
	}
	return packed.Text, Response{}
}

func (r *Router) handleMemoryConsent(ctx context.Context, request Request) Response {
	if request.GuildID == "" {
		return Response{Content: "Memory consent must be managed inside a Discord server.", Ephemeral: true}
	}
	action := strings.ToLower(strings.TrimSpace(request.Options["action"]))
	if action == "" {
		action = "status"
	}
	switch action {
	case "enable":
		if _, err := r.admin.SetMemoryConsent(ctx, request.GuildID, request.UserID, true); err != nil {
			return Response{Content: "Memory consent could not be updated.", Ephemeral: true}
		}
		return Response{Content: "User-specific memory consent is enabled. Panda may use future user-memory features for you in this server.", Ephemeral: true}
	case "disable":
		if _, err := r.admin.SetMemoryConsent(ctx, request.GuildID, request.UserID, false); err != nil {
			return Response{Content: "Memory consent could not be updated.", Ephemeral: true}
		}
		return Response{Content: "User-specific memory consent is disabled. Panda will not use user-memory features for you in this server.", Ephemeral: true}
	case "status":
		consent, err := r.admin.MemoryConsent(ctx, request.GuildID, request.UserID)
		if err != nil {
			return Response{Content: "Memory consent could not be read.", Ephemeral: true}
		}
		if consent {
			return Response{Content: "User-specific memory consent is enabled for this server.", Ephemeral: true}
		}
		return Response{Content: "User-specific memory consent is disabled for this server.", Ephemeral: true}
	default:
		return Response{Content: "Use memory consent action `status`, `enable`, or `disable`.", Ephemeral: true}
	}
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
	case "usage":
		return r.handleAdminUsage(ctx, request)
	case "prompt":
		return r.handleAdminPrompt(ctx, request)
	case "memory":
		return r.handleAdminMemory(ctx, request)
	case "roles":
		return r.handleAdminRoles(ctx, request)
	case "channels":
		return r.handleAdminChannels(ctx, request)
	case "limits":
		return r.handleAdminLimits(ctx, request)
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

func (r *Router) handleAdminUsage(ctx context.Context, request Request) Response {
	report, err := r.admin.UsageReport(ctx, request.GuildID, usageWindow(request.Options["window"]), request.Options["by"], 5)
	if err != nil {
		return Response{Content: "Usage lookup failed.", Ephemeral: true}
	}
	return Response{Content: renderUsageReport(report), Ephemeral: true}
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

func (r *Router) handleAdminMemory(ctx context.Context, request Request) Response {
	action := strings.ToLower(strings.TrimSpace(request.Options["action"]))
	switch action {
	case "enable":
		if dryRunRequested(request) {
			return dryRunResponse("server knowledge retrieval would be enabled.")
		}
		_, err := r.admin.SetMemoryEnabled(ctx, request.GuildID, request.UserID, true)
		if err != nil {
			return adminMemoryError(err)
		}
		return Response{Content: "Server knowledge retrieval is enabled.", Ephemeral: true}
	case "disable":
		if dryRunRequested(request) {
			return dryRunResponse("server knowledge retrieval would be disabled.")
		}
		_, err := r.admin.SetMemoryEnabled(ctx, request.GuildID, request.UserID, false)
		if err != nil {
			return adminMemoryError(err)
		}
		return Response{Content: "Server knowledge retrieval is disabled.", Ephemeral: true}
	case "add":
		if dryRunRequested(request) {
			if strings.TrimSpace(request.Options["title"]) == "" || strings.TrimSpace(request.Options["content"]) == "" {
				return Response{Content: "Knowledge document could not be saved.", Ephemeral: true}
			}
			return dryRunResponse("knowledge document `%s` would be saved.", strings.TrimSpace(request.Options["title"]))
		}
		document, err := r.admin.AddMemoryDocument(ctx, memory.AddDocumentRequest{
			GuildID:   request.GuildID,
			Title:     request.Options["title"],
			Content:   request.Options["content"],
			CreatedBy: request.UserID,
			Source:    "admin",
		})
		if err != nil {
			return Response{Content: "Knowledge document could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Knowledge document `%s` saved with id `%d`.", document.Title, document.ID), Ephemeral: true}
	case "search":
		if strings.TrimSpace(request.Options["query"]) != "" {
			r.admin.RecordSensitiveReadAudit(ctx, request.GuildID, request.UserID, "knowledge_search", request.GuildID, map[string]string{
				"command": "admin.memory.search",
			})
		}
		return renderMemorySearch(ctx, r.admin.SearchMemory, request)
	case "list", "export":
		documents, err := r.admin.ListMemoryDocuments(ctx, request.GuildID, 10)
		if err != nil {
			return Response{Content: "Knowledge documents could not be listed.", Ephemeral: true}
		}
		return Response{Content: renderDocuments(documents), Ephemeral: true}
	case "delete":
		id, err := strconv.ParseUint(strings.TrimSpace(request.Options["document_id"]), 10, 64)
		if err != nil || id == 0 {
			return Response{Content: "Provide a numeric `document_id` to delete.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("knowledge document `%d` would be deleted and removed from server search.", id)
		}
		confirmationID := memoryDeleteConfirmationID(request.UserID, strconv.FormatUint(id, 10))
		if !confirmed(request, confirmationID) {
			return destructiveConfirmation(confirmationID, "Delete document", fmt.Sprintf("This will permanently delete knowledge document `%d` and remove it from server search.", id))
		}
		if err := r.admin.DeleteMemoryDocument(ctx, request.GuildID, request.UserID, uint(id)); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return Response{Content: "No matching knowledge document was found.", Ephemeral: true}
			}
			return Response{Content: "Knowledge document could not be deleted.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Knowledge document `%d` deleted.", id), Ephemeral: true}
	default:
		return Response{Content: "Use memory action `enable`, `disable`, `add`, `search`, `list`, `export`, or `delete`.", Ephemeral: true}
	}
}

func (r *Router) handleAdminRoles(ctx context.Context, request Request) Response {
	action := strings.ToLower(strings.TrimSpace(request.Options["action"]))
	permission := firstNonEmpty(request.Options["permission"], admin.PermissionAssistantUse)
	switch action {
	case "choose":
		roleID := strings.TrimSpace(request.Options["role_id"])
		if roleID == "" {
			return Response{Content: "Provide a `role_id` to configure.", Ephemeral: true}
		}
		return rolePermissionSelectResponse(request.UserID, roleID)
	case "add":
		roleID := strings.TrimSpace(request.Options["role_id"])
		if roleID == "" {
			return Response{Content: "Provide a `role_id` to add.", Ephemeral: true}
		}
		if !admin.IsPermissionNameAllowed(permission) {
			return Response{Content: "Role permission could not be saved.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("role `%s` would be granted `%s`.", roleID, permission)
		}
		role, err := r.admin.AddRolePermission(ctx, request.GuildID, request.UserID, roleID, permission)
		if err != nil {
			return Response{Content: "Role permission could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Role `%s` can now use `%s`.", role.RoleID, role.Permission), Ephemeral: true}
	case "remove":
		roleID := strings.TrimSpace(request.Options["role_id"])
		if roleID == "" {
			return Response{Content: "Provide a `role_id` to remove.", Ephemeral: true}
		}
		if !admin.IsPermissionNameAllowed(permission) {
			return Response{Content: "Role permission could not be removed.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("`%s` would be removed from role `%s`.", permission, roleID)
		}
		confirmationID := roleRemoveConfirmationID(request.UserID, roleID, permission)
		if !confirmed(request, confirmationID) {
			return destructiveConfirmation(confirmationID, "Remove role access", fmt.Sprintf("This will remove `%s` from role `%s`.", permission, roleID))
		}
		if err := r.admin.RemoveRolePermission(ctx, request.GuildID, request.UserID, roleID, permission); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return Response{Content: "No matching role permission was found.", Ephemeral: true}
			}
			return Response{Content: "Role permission could not be removed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Role `%s` no longer has `%s`.", roleID, permission), Ephemeral: true}
	case "list":
		roles, err := r.admin.ListRolePermissions(ctx, request.GuildID)
		if err != nil {
			return Response{Content: "Role permissions could not be listed.", Ephemeral: true}
		}
		return Response{Content: renderRoles(roles), Ephemeral: true}
	default:
		return Response{Content: "Use role action `add`, `choose`, `remove`, or `list`.", Ephemeral: true}
	}
}

func (r *Router) handleAdminChannels(ctx context.Context, request Request) Response {
	action := strings.ToLower(strings.TrimSpace(request.Options["action"]))
	switch action {
	case "allow", "deny":
		channelID := strings.TrimSpace(request.Options["channel_id"])
		if channelID == "" {
			return Response{Content: "Provide a `channel_id` to configure.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("channel `%s` would be set to `%s`.", channelID, action)
		}
		rule, err := r.admin.SetChannelRule(ctx, request.GuildID, request.UserID, channelID, action)
		if err != nil {
			return Response{Content: "Channel rule could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Channel `%s` is now `%s`.", rule.ChannelID, rule.Rule), Ephemeral: true}
	case "remove":
		channelID := strings.TrimSpace(request.Options["channel_id"])
		if channelID == "" {
			return Response{Content: "Provide a `channel_id` to remove.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("the allow/deny rule for channel `%s` would be removed.", channelID)
		}
		confirmationID := channelRemoveConfirmationID(request.UserID, channelID)
		if !confirmed(request, confirmationID) {
			return destructiveConfirmation(confirmationID, "Remove channel rule", fmt.Sprintf("This will remove the allow/deny rule for channel `%s`.", channelID))
		}
		if err := r.admin.RemoveChannelRule(ctx, request.GuildID, request.UserID, channelID); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return Response{Content: "No matching channel rule was found.", Ephemeral: true}
			}
			return Response{Content: "Channel rule could not be removed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Channel `%s` rule removed.", channelID), Ephemeral: true}
	case "list":
		rules, err := r.admin.ListChannelRules(ctx, request.GuildID)
		if err != nil {
			return Response{Content: "Channel rules could not be listed.", Ephemeral: true}
		}
		return Response{Content: renderChannelRules(rules), Ephemeral: true}
	default:
		return Response{Content: "Use channel action `allow`, `deny`, `remove`, or `list`.", Ephemeral: true}
	}
}

func (r *Router) handleAdminLimits(ctx context.Context, request Request) Response {
	action := strings.ToLower(strings.TrimSpace(request.Options["action"]))
	scope := strings.ToLower(strings.TrimSpace(request.Options["scope"]))
	subjectID := strings.TrimSpace(request.Options["subject_id"])
	switch action {
	case "set":
		limitCount, err := strconv.Atoi(strings.TrimSpace(request.Options["limit"]))
		if err != nil || limitCount <= 0 {
			return Response{Content: "Provide a positive numeric `limit`.", Ephemeral: true}
		}
		window, err := time.ParseDuration(firstNonEmpty(request.Options["window"], "1h"))
		if err != nil || window <= 0 {
			return Response{Content: "Provide a valid positive `window`, such as `1h` or `24h`.", Ephemeral: true}
		}
		if scope == repository.BudgetScopeGlobal && !request.IsOwner {
			return Response{Content: "Only a bot owner can set global limits.", Ephemeral: true}
		}
		if scope == repository.BudgetScopeGuild && subjectID == "" {
			subjectID = request.GuildID
		}
		if !validBudgetScope(scope) {
			return Response{Content: "Budget limit could not be saved.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("limit would be set: `%s` `%s` = %d requests per %s.", scope, subjectID, limitCount, window.String())
		}
		saved, err := r.admin.SetBudgetLimit(ctx, request.GuildID, request.UserID, store.BudgetLimit{
			Scope:         scope,
			SubjectID:     subjectID,
			Limit:         limitCount,
			WindowSeconds: int(window.Seconds()),
		})
		if err != nil {
			return Response{Content: "Budget limit could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Limit set: `%s` `%s` = %d requests per %s.", saved.Scope, saved.SubjectID, saved.Limit, (time.Duration(saved.WindowSeconds) * time.Second).String()), Ephemeral: true}
	case "remove":
		if scope == repository.BudgetScopeGuild && subjectID == "" {
			subjectID = request.GuildID
		}
		if scope == repository.BudgetScopeGlobal && !request.IsOwner {
			return Response{Content: "Only a bot owner can remove global limits.", Ephemeral: true}
		}
		if !validBudgetScope(scope) {
			return Response{Content: "Budget limit could not be removed.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("the `%s` budget limit for `%s` would be removed.", scope, subjectID)
		}
		confirmationID := limitRemoveConfirmationID(request.UserID, scope, subjectID)
		if !confirmed(request, confirmationID) {
			return destructiveConfirmation(confirmationID, "Remove limit", fmt.Sprintf("This will remove the `%s` budget limit for `%s`.", scope, subjectID))
		}
		if err := r.admin.RemoveBudgetLimit(ctx, request.GuildID, request.UserID, scope, subjectID); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return Response{Content: "No matching budget limit was found.", Ephemeral: true}
			}
			return Response{Content: "Budget limit could not be removed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Limit removed: `%s` `%s`.", scope, subjectID), Ephemeral: true}
	case "list":
		limits, err := r.admin.ListBudgetLimits(ctx, request.GuildID)
		if err != nil {
			return Response{Content: "Budget limits could not be listed.", Ephemeral: true}
		}
		return Response{Content: renderBudgetLimits(limits), Ephemeral: true}
	default:
		return Response{Content: "Use limit action `set`, `remove`, or `list`.", Ephemeral: true}
	}
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
		GuildID:   request.GuildID,
		UserID:    request.UserID,
		ChannelID: request.ChannelID,
		Question:  question,
	})
	if err != nil {
		return assistantError(err)
	}
	if strings.TrimSpace(answer.Content) == "" {
		return Response{Content: "The model returned an empty response.", Ephemeral: true}
	}
	return Response{Content: answer.Content}
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
		GuildID:   request.GuildID,
		UserID:    request.UserID,
		ChannelID: chatChannelID,
		ThreadID:  threadID,
		Question:  question,
	})
	if err != nil {
		return assistantError(err)
	}
	return Response{Content: answer.Content, ThreadID: threadID, ThreadName: threadName}
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
		GuildID:   request.GuildID,
		UserID:    request.UserID,
		ChannelID: request.ChannelID,
		Command:   request.Command,
		Input:     input,
		Tone:      request.Options["tone"],
		Language:  request.Options["language"],
		Detail:    request.Options["detail"],
	}
	if shouldBackgroundTask(request, input) {
		return Response{Content: "Queued long summary. The result will replace this response when it is ready.", Background: &task}
	}
	return r.HandleBackgroundTask(ctx, task)
}

func (r *Router) HandleBackgroundTask(ctx context.Context, task BackgroundTask) Response {
	answer, err := r.assistant.CompleteTask(ctx, assistant.TaskRequest{
		GuildID:   task.GuildID,
		UserID:    task.UserID,
		ChannelID: task.ChannelID,
		Command:   task.Command,
		Input:     task.Input,
		Tone:      task.Tone,
		Language:  task.Language,
		Detail:    task.Detail,
	})
	if err != nil {
		return assistantError(err)
	}
	return Response{Content: answer.Content}
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

func (r *Router) handleSearchMemory(ctx context.Context, request Request) Response {
	if denied := r.ensureAssistantAllowed(ctx, request); denied.Content != "" {
		return denied
	}
	enabled, err := r.admin.MemoryEnabled(ctx, request.GuildID)
	if err != nil {
		return Response{Content: "Memory setting lookup failed.", Ephemeral: true}
	}
	if !enabled {
		return Response{Content: "Server knowledge retrieval is disabled.", Ephemeral: true}
	}
	if denied := r.ensureMemoryReadAllowed(ctx, request); denied.Content != "" {
		return denied
	}
	if strings.TrimSpace(request.Options["query"]) != "" {
		r.admin.RecordSensitiveReadAudit(ctx, request.GuildID, request.UserID, "knowledge_search", request.GuildID, map[string]string{
			"command": "search-memory",
		})
	}
	return renderMemorySearch(ctx, r.admin.SearchMemory, request)
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

func adminMemoryError(err error) Response {
	if errors.Is(err, repository.ErrNotFound) {
		return Response{Content: "Run `/admin setup` before changing memory settings.", Ephemeral: true}
	}
	return Response{Content: "Memory setting update failed.", Ephemeral: true}
}

func renderMemorySearch(ctx context.Context, search func(context.Context, string, string, int) ([]repository.KnowledgeSearchResult, error), request Request) Response {
	query := strings.TrimSpace(request.Options["query"])
	if query == "" {
		return Response{Content: "Please include a search query.", Ephemeral: true}
	}
	results, err := search(ctx, request.GuildID, query, 5)
	if err != nil {
		return Response{Content: "Memory search failed.", Ephemeral: true}
	}
	if len(results) == 0 {
		return Response{Content: "No matching server knowledge found.", Ephemeral: true}
	}

	var builder strings.Builder
	builder.WriteString("Server knowledge matches:\n")
	for _, result := range results {
		fmt.Fprintf(&builder, "- `%d` %s: %s\n", result.DocumentID, result.Title, strings.TrimSpace(result.Snippet))
	}
	return Response{Content: security.SafeDiscordContent(builder.String()), Ephemeral: true}
}

func renderDocuments(documents []store.KnowledgeDocument) string {
	if len(documents) == 0 {
		return "No knowledge documents saved yet."
	}
	var builder strings.Builder
	builder.WriteString("Knowledge documents:\n")
	for _, document := range documents {
		fmt.Fprintf(&builder, "- `%d` %s\n", document.ID, document.Title)
	}
	return security.SafeDiscordContent(builder.String())
}

func renderAudit(events []store.AuditEvent) string {
	var builder strings.Builder
	builder.WriteString("Recent audit events:\n")
	for _, event := range events {
		fmt.Fprintf(&builder, "- %s by `%s` at %s\n", event.Action, event.ActorID, event.CreatedAt.UTC().Format(time.RFC3339))
	}
	return security.SafeDiscordContent(builder.String())
}

func renderUsageReport(report admin.UsageReport) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Usage: %d requests (%d succeeded, %d failed), %d total tokens.",
		report.Summary.TotalRequests,
		report.Summary.Successful,
		report.Summary.Failed,
		report.Summary.TotalTokens,
	)
	if len(report.Breakdown) == 0 {
		return builder.String()
	}
	fmt.Fprintf(&builder, "\nTop %s usage:\n", report.Dimension)
	for _, row := range report.Breakdown {
		fmt.Fprintf(&builder, "- `%s`: %d requests, %d tokens, %d failed\n", row.Label, row.TotalRequests, row.TotalTokens, row.Failed)
	}
	return security.SafeDiscordContent(builder.String())
}

func renderRoles(roles []store.GuildRole) string {
	if len(roles) == 0 {
		return "No role permissions configured."
	}
	var builder strings.Builder
	builder.WriteString("Role permissions:\n")
	for _, role := range roles {
		fmt.Fprintf(&builder, "- `%s`: %s\n", role.RoleID, role.Permission)
	}
	return security.SafeDiscordContent(builder.String())
}

func renderChannelRules(rules []store.GuildChannelRule) string {
	if len(rules) == 0 {
		return "No channel rules configured."
	}
	var builder strings.Builder
	builder.WriteString("Channel rules:\n")
	for _, rule := range rules {
		fmt.Fprintf(&builder, "- `%s`: %s\n", rule.ChannelID, rule.Rule)
	}
	return security.SafeDiscordContent(builder.String())
}

func renderBudgetLimits(limits []store.BudgetLimit) string {
	if len(limits) == 0 {
		return "No budget limits configured."
	}
	var builder strings.Builder
	builder.WriteString("Budget limits:\n")
	for _, limit := range limits {
		fmt.Fprintf(&builder, "- `%s` `%s`: %d requests per %s\n", limit.Scope, limit.SubjectID, limit.Limit, (time.Duration(limit.WindowSeconds) * time.Second).String())
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

func usageWindow(value string) time.Duration {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "all":
		return 0
	case "week", "7d":
		return 7 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
