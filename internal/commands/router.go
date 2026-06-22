package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/alerts"
	"github.com/sn0w/panda2/internal/assistant"
	"github.com/sn0w/panda2/internal/billing"
	"github.com/sn0w/panda2/internal/composed"
	contextsvc "github.com/sn0w/panda2/internal/context"
	"github.com/sn0w/panda2/internal/features"
	"github.com/sn0w/panda2/internal/feedback"
	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/music"
	"github.com/sn0w/panda2/internal/ops"
	"github.com/sn0w/panda2/internal/polls"
	"github.com/sn0w/panda2/internal/ratelimit"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/scheduler"
	"github.com/sn0w/panda2/internal/security"
	"github.com/sn0w/panda2/internal/store"
	"github.com/sn0w/panda2/internal/textutil"
	toolsvc "github.com/sn0w/panda2/internal/tools"
)

type Router struct {
	admin       *admin.Service
	assistant   *assistant.Service
	composed    *composed.Service
	context     *contextsvc.Service
	threads     ThreadManager
	memberRoles MemberRoleManager
	roles       DiscordRoleManager
	attachments AttachmentReader
	ops         *ops.Service
	music       MusicService
	feedback    *feedback.Service
	scheduler   *scheduler.Service
	alerts      *alerts.Service
	billing     *billing.Service
	data        *repository.GuildDataRepository
	setup       SetupChecker
	tools       *toolsvc.Executor
	rateLimit   *ratelimit.Limiter
	features    *features.Service
	install     FeatureInstallIntentCreator
}

type ThreadManager interface {
	EnsureChatThread(ctx context.Context, request ThreadRequest) (Thread, error)
}

type MemberRoleManager interface {
	AddMemberRole(ctx context.Context, request MemberRoleRequest) error
	RemoveMemberRole(ctx context.Context, request MemberRoleRequest) error
}

type DiscordRoleManager interface {
	CreateRole(ctx context.Context, request DiscordRoleRequest) (DiscordRole, error)
}

type AttachmentReader interface {
	Get(ctx context.Context, guildID string, id uint) (store.Attachment, error)
}

type MusicService interface {
	Handle(ctx context.Context, request music.Request) (music.Response, error)
}

type SetupChecker interface {
	CheckSetup(ctx context.Context, guildID, channelID string) (SetupCheckResult, error)
}

type SetupCheckResult struct {
	DiscordConfigured bool
	Connected         bool
	Warnings          []string
}

type helpAccess struct {
	config      bool
	soul        bool
	moderation  bool
	toolDraft   bool
	toolApprove bool
	toolInvoke  bool
	toolAudit   bool
}

func (access helpAccess) elevated() bool {
	return access.config || access.soul || access.moderation || access.composedTools()
}

func (access helpAccess) composedTools() bool {
	return access.toolDraft || access.toolApprove || access.toolInvoke || access.toolAudit
}

const baseHelpMessage = "### Panda Help\n\n" +
	"**Chat naturally**\n" +
	"- Mention `Panda` in a normal message: `Panda is this true?`\n" +
	"- Casual mentions may not trigger a reply.\n\n" +
	"**Music**\n" +
	"- Join a voice channel, then say `Panda play <song>`.\n" +
	"- Natural controls: `pause`, `resume`, `skip`, `stop`, `queue`, `clear queue`, `now playing`.\n\n" +
	"**Message actions**\n" +
	"- Use **Explain with Panda** or **Summarize with Panda** from a message's **Apps** menu.\n" +
	"- `/poll question:<text> answers:<answer | answer | answer>` creates a native Discord poll."

const regularHelpMessage = baseHelpMessage + "\n\n" +
	"**Good things to ask**\n" +
	"- Questions about the conversation\n" +
	"- Summaries, rewrites, and explanations\n" +
	"- Help thinking through an idea or decision"

const (
	pandaRepositoryURL = "https://github.com/snowdamiz/panda2"
	pandaSetupURL      = "https://github.com/snowdamiz/panda2#local-development"
	pandaCommandsURL   = "https://github.com/snowdamiz/panda2#commands"
)

const discordMessageContentLimit = 2000
const invocationContextWindow = 2 * time.Minute
const invocationContextLimit = 50
const feedbackMinimumPlainTextRunes = 500

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

func (r *Router) WithMemberRoleManager(memberRoles MemberRoleManager) *Router {
	r.memberRoles = memberRoles
	return r
}

func (r *Router) WithDiscordRoleManager(roles DiscordRoleManager) *Router {
	r.roles = roles
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

func (r *Router) WithMusicService(musicService MusicService) *Router {
	r.music = musicService
	return r
}

func (r *Router) WithFeedbackService(feedbackService *feedback.Service) *Router {
	r.feedback = feedbackService
	return r
}

func (r *Router) WithScheduler(schedulerService *scheduler.Service) *Router {
	r.scheduler = schedulerService
	return r
}

func (r *Router) WithAlertService(alertService *alerts.Service) *Router {
	r.alerts = alertService
	return r
}

func (r *Router) WithBilling(billingService *billing.Service) *Router {
	r.billing = billingService
	return r
}

func (r *Router) WithDataRepository(dataRepository *repository.GuildDataRepository) *Router {
	r.data = dataRepository
	return r
}

func (r *Router) WithSetupChecker(checker SetupChecker) *Router {
	r.setup = checker
	return r
}

func (r *Router) WithToolExecutor(executor *toolsvc.Executor) *Router {
	r.tools = executor
	return r
}

func (r *Router) WithFeatureService(featureService *features.Service) *Router {
	r.features = featureService
	return r
}

func (r *Router) WithFeatureInstallIntents(creator FeatureInstallIntentCreator) *Router {
	r.install = creator
	return r
}

func (r *Router) Handle(ctx context.Context, request Request) Response {
	switch strings.ToLower(request.Command) {
	case "ping":
		return Response{Content: "pong", Ephemeral: true, Presentation: Presentation{Title: "Panda is online", Accent: AccentSuccess}}
	case "help":
		return r.handleHelp(ctx, request)
	case "poll":
		if denied := r.ensureFeatureEnabled(ctx, request, features.Polls); denied.Content != "" {
			return denied
		}
		return r.handlePoll(request)
	case "admin":
		return r.handleAdmin(ctx, request)
	case "ops":
		if denied := r.ensureFeatureEnabled(ctx, request, features.OwnerOps); denied.Content != "" {
			return denied
		}
		return r.handleOps(ctx, request)
	case "schedule", "schedules":
		if denied := r.ensureFeatureEnabled(ctx, request, features.ComposedTools); denied.Content != "" {
			return denied
		}
		return r.handleSchedule(ctx, request)
	case "billing":
		return r.handleBilling(ctx, request)
	case "support":
		return r.handleSupport(ctx, request)
	case "data":
		return r.handleData(ctx, request)
	case "reminder", "reminders":
		if denied := r.ensureFeatureEnabled(ctx, request, features.Reminders); denied.Content != "" {
			return denied
		}
		return r.handleReminder(ctx, request)
	case "ask":
		return r.handleAsk(ctx, request, "ask")
	case "chat":
		return r.handleChat(ctx, request)
	case "summarize", "explain", "rewrite", "translate":
		return r.handleTask(ctx, request)
	default:
		return Response{Content: "Unknown command.", Ephemeral: true, Presentation: Presentation{Title: "Unknown command", Accent: AccentWarning}}
	}
}

func (r *Router) handleHelp(ctx context.Context, request Request) Response {
	return Response{
		Content:   r.helpMessage(ctx, request),
		Ephemeral: true,
		Presentation: Presentation{
			Title:  "Panda Help",
			Accent: AccentInfo,
			Footer: "Hosted Discord assistant",
		},
		Actions: []Action{
			{Label: "Commands", URL: pandaCommandsURL},
			{Label: "Setup", URL: pandaSetupURL},
			{Label: "Repository", URL: pandaRepositoryURL},
		},
	}
}

func (r *Router) helpMessage(ctx context.Context, request Request) string {
	access := r.helpAccess(ctx, request)
	if access.elevated() {
		return elevatedHelpMessage(access)
	}
	return regularHelpMessage
}

func (r *Router) handlePoll(request Request) Response {
	question := strings.TrimSpace(request.Options["question"])
	answers := polls.ParseAnswers(request.Options["answers"])
	poll, err := polls.New(question, answers, intOption(request.Options["duration_hours"], 0), truthyOption(request.Options["allow_multiselect"]))
	if err != nil {
		return Response{
			Content:   "Poll could not be created: " + err.Error(),
			Ephemeral: true,
			Presentation: Presentation{
				Title:  "Poll not created",
				Accent: AccentWarning,
			},
		}
	}
	return Response{Poll: &poll}
}

func (r *Router) helpAccess(ctx context.Context, request Request) helpAccess {
	if r.admin == nil {
		return helpAccess{}
	}
	accessRequest := assistantAccessRequest(request)
	allowed := func(check func(context.Context, admin.AssistantAccessRequest) (bool, error)) bool {
		ok, err := check(ctx, accessRequest)
		return err == nil && ok
	}
	return helpAccess{
		config:      allowed(r.admin.CanWriteConfig),
		soul:        allowed(r.admin.CanWriteSoul),
		moderation:  allowed(r.admin.CanUseModeration),
		toolDraft:   allowed(r.admin.CanDraftComposedTool),
		toolApprove: allowed(r.admin.CanApproveComposedTool),
		toolInvoke:  allowed(r.admin.CanInvokeComposedTool),
		toolAudit:   allowed(r.admin.CanAuditComposedTool),
	}
}

func elevatedHelpMessage(access helpAccess) string {
	var builder strings.Builder
	builder.WriteString(baseHelpMessage)

	if access.moderation {
		builder.WriteString("\n\n**Moderator tools**\n")
		builder.WriteString("- Ask Panda for moderation guidance, review help, and action drafts in chat.\n")
	}

	if access.config || access.soul {
		builder.WriteString("\n\n**Admin commands**\n")
		if access.config {
			builder.WriteString("- `/admin role action:list|set|remove profile:admin|moderator role:@Role` - Panda role profiles\n")
			builder.WriteString("- `/admin member-role action:add|remove user:@User role:@Role` - assign Discord roles to users\n")
			builder.WriteString("- `/admin tool action:list|add|remove tool_name:<tool> role:@Role` - role tool grants\n")
			builder.WriteString("- `/admin channel action:list|allow|deny|remove channel:#Channel`\n")
			builder.WriteString("- `/admin behavior answer_length:brief|standard|detailed tool_policy:<policy>` - response length and tool access\n")
			builder.WriteString("- `/admin billing` or `/billing` - plan, renewal, and quota status\n")
			builder.WriteString("- Policies: `off`, `read_only`, `assistive`, `admin_only`, `moderator`, `write_confirmed`, `owner_ops`\n")
			builder.WriteString("- `/admin prompt prompt:<text> dry_run:<bool>` - server instructions; omit `prompt` for modal\n")
			builder.WriteString("- `/admin audit` - recent privileged changes\n")
			builder.WriteString("- `/admin enable dry_run:<bool>` / `/admin disable confirm:<token> dry_run:<bool>`\n")
		}
		if access.soul {
			builder.WriteString("- `/admin soul soul:<text>` - personality/tone; natural chat can brainstorm/set\n")
		}
	}

	if access.composedTools() {
		builder.WriteString("\n\n**Composed tools**\n")
		if access.toolDraft {
			builder.WriteString("- Ask Panda to draft or preview new server tools.\n")
		}
		if access.toolAudit {
			builder.WriteString("- Ask Panda to list/show tools or export approved spec JSON.\n")
		}
		if access.toolApprove {
			builder.WriteString("- Ask Panda to approve, pause, resume, disable, archive, or roll back tools. Approval/rollback use buttons.\n")
		}
		if access.toolInvoke {
			builder.WriteString("- Ask Panda to run/simulate/schedule/list/cancel approved tools.\n")
		}
	}

	if access.config || access.moderation {
		builder.WriteString("\n\n**Also available through Panda chat/tools**\n")
		if access.config {
			builder.WriteString("- Usage and limits\n")
			builder.WriteString("- Server knowledge\n")
			builder.WriteString("- Role/channel access\n")
			builder.WriteString("- Memory consent\n")
		}
		if access.moderation {
			builder.WriteString("- Moderation guidance\n")
		}
	}

	return strings.TrimRight(builder.String(), "\n")
}

func (r *Router) HandleNaturalMessage(ctx context.Context, request Request) Response {
	message := strings.TrimSpace(firstNonEmpty(request.Options["message"], request.Options["question"]))
	if message == "" {
		return Response{}
	}
	if !r.canHandleNaturalMessage(ctx, request) {
		return Response{}
	}
	if denied := r.checkAIUsageAvailable(ctx, request); denied.Content != "" {
		return denied
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
	if err != nil {
		slog.Warn("natural message classification failed", slog.Any("err", err), slog.String("guild_id", request.GuildID), slog.String("channel_id", request.ChannelID), slog.String("request_id", request.RequestID))
		return Response{}
	}
	if !decision.Respond {
		return Response{}
	}
	request.Command = "chat"
	if request.Options == nil {
		request.Options = map[string]string{}
	}
	request.Options["question"] = decision.Prompt
	request.PreferredTool = decision.ToolName
	return r.handleChatModeWithAccess(ctx, request, false, true)
}

func (r *Router) canHandleNaturalMessage(ctx context.Context, request Request) bool {
	if r.admin == nil {
		return false
	}
	if denied := r.ensureFeatureEnabled(ctx, request, features.AssistantChat); denied.Content != "" {
		return false
	}
	accessRequest := assistantAccessRequest(request)
	allowed, err := r.admin.CanUseAssistant(ctx, accessRequest)
	if err != nil {
		return false
	}
	if allowed {
		return true
	}
	return r.canWriteSoul(ctx, request)
}

func (r *Router) canWriteSoul(ctx context.Context, request Request) bool {
	if r.admin == nil {
		return false
	}
	allowed, err := r.admin.CanWriteSoul(ctx, assistantAccessRequest(request))
	return err == nil && allowed
}

func (r *Router) handleOps(ctx context.Context, request Request) Response {
	if r.ops == nil {
		return Response{Content: "Ops commands are not configured for this runtime.", Ephemeral: true}
	}
	if !request.IsOwner {
		return Response{Content: "Only a bot owner can use ops commands.", Ephemeral: true}
	}
	switch strings.ToLower(request.Subcommand) {
	case "health":
		health, err := r.ops.Health(ctx)
		if err != nil {
			return Response{Content: "Ops health check failed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Health: sqlite=%s discord=%s shards=%s ai_service=%s queued_jobs=%d guild_configs=%d draining=%t incident=%t data_dir=`%s`.", health.SQLite, health.Discord, health.Shards, health.AIService, health.QueuedJobs, health.ConfiguredGuildCount, health.Draining, health.Incident, health.DataDir), Ephemeral: true}
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

func (r *Router) handleSupport(ctx context.Context, request Request) Response {
	if request.GuildID == "" {
		return Response{Content: "Support is managed per Discord server. Run this inside the server where Panda is installed.", Ephemeral: true}
	}
	lines := []string{
		"Support bundle (safe to share with Panda support):",
		"- Guild ID: `" + request.GuildID + "`",
		"- User ID: `" + firstNonEmpty(request.UserID, "unknown") + "`",
		"- Request ID: `" + firstNonEmpty(request.RequestID, "not provided") + "`",
		"- Raw prompts/messages: not included",
	}
	if r.billing != nil {
		if entitlement, err := r.billing.Resolve(ctx, request.GuildID); err == nil {
			lines = append(lines,
				fmt.Sprintf("- Plan: `%s`", entitlement.Plan.DisplayName),
				fmt.Sprintf("- Subscription: `%s`", entitlement.Status),
				fmt.Sprintf("- AI responses: `%s`", entitlement.UsageLine(billing.MetricAIResponse)),
				fmt.Sprintf("- Web searches: `%s`", entitlement.UsageLine(billing.MetricWebSearch)),
				fmt.Sprintf("- Knowledge storage: `%s`", entitlement.UsageLine(billing.MetricKnowledgeStorageByte)),
			)
		} else if errors.Is(err, billing.ErrNoSubscription) {
			lines = append(lines, "- Subscription: `none`")
		} else {
			lines = append(lines, "- Subscription: `lookup failed`")
		}
	}
	if r.data != nil {
		if summary, err := r.data.Summary(ctx, request.GuildID); err == nil {
			lines = append(lines,
				fmt.Sprintf("- Knowledge documents: `%d`", summary.KnowledgeDocuments),
				fmt.Sprintf("- Conversation records: `%d conversations / %d messages`", summary.Conversations, summary.Messages),
				fmt.Sprintf("- Memory consent records: `%d`", summary.MemoryConsentRecords),
				fmt.Sprintf("- Billing records: `%d account(s) / %d subscription(s)`", summary.CustomerAccounts, summary.Subscriptions),
			)
		}
	}
	if r.setup != nil {
		if setup, err := r.setup.CheckSetup(ctx, request.GuildID, request.ChannelID); err == nil {
			lines = append(lines, fmt.Sprintf("- Discord connected: `%t`", setup.Connected))
			if len(setup.Warnings) > 0 {
				lines = append(lines, fmt.Sprintf("- Setup warnings: `%d`", len(setup.Warnings)))
			}
		}
	}
	return Response{Content: strings.Join(lines, "\n"), Ephemeral: true, Presentation: Presentation{Title: "Panda Support", Accent: AccentInfo}}
}

func (r *Router) handleData(ctx context.Context, request Request) Response {
	if request.GuildID == "" {
		return Response{Content: "Data commands must be run inside a Discord server.", Ephemeral: true}
	}
	if r.data == nil {
		return Response{Content: "Data export and deletion are not configured for this runtime.", Ephemeral: true, Presentation: Presentation{Title: "Data unavailable", Accent: AccentWarning}}
	}
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Subcommand, request.Options["action"])))
	if action == "" {
		action = "export"
	}
	switch action {
	case "export":
		if denied := r.ensureFeatureEnabled(ctx, request, features.AdminSetup); denied.Content != "" {
			return denied
		}
		if denied := r.ensureDataPermission(ctx, request, false); denied.Content != "" {
			return denied
		}
		summary, err := r.data.Summary(ctx, request.GuildID)
		if err != nil {
			return Response{Content: "Data export could not be generated.", Ephemeral: true}
		}
		return Response{Content: renderDataSummary(summary), Ephemeral: true, Presentation: Presentation{Title: "Panda Data Export", Accent: AccentInfo}}
	case "delete":
		if denied := r.ensureFeatureEnabled(ctx, request, features.AdminAccessControl); denied.Content != "" {
			return denied
		}
		if denied := r.ensureDataPermission(ctx, request, true); denied.Content != "" {
			return denied
		}
		scope, ok := repository.NormalizeDataScope(request.Options["scope"])
		if !ok {
			return Response{Content: "`scope` must be `knowledge`, `memory`, `conversations`, `billing`, or `all`.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			summary, err := r.data.Summary(ctx, request.GuildID)
			if err != nil {
				return Response{Content: "Data deletion preview could not be generated.", Ephemeral: true}
			}
			return Response{Content: fmt.Sprintf("Data deletion dry run for scope `%s`:\n%s", scope, renderDataSummary(summary)), Ephemeral: true, Presentation: Presentation{Title: "Data deletion preview", Accent: AccentWarning}}
		}
		confirmationID := dataDeleteConfirmationID(request.UserID, scope)
		if !confirmed(request, confirmationID) {
			return destructiveConfirmation(confirmationID, "Delete Panda data", fmt.Sprintf("This deletes `%s` Panda data for this server except audit logs.", scope))
		}
		summary, err := r.data.Delete(ctx, request.GuildID, scope)
		if err != nil {
			return Response{Content: "Data deletion could not be completed.", Ephemeral: true, Presentation: Presentation{Title: "Data deletion failed", Accent: AccentDanger}}
		}
		return Response{Content: renderDataDeletion(summary), Ephemeral: true, Presentation: Presentation{Title: "Data deleted", Accent: AccentDanger}}
	default:
		return Response{Content: "Data action must be `export` or `delete`.", Ephemeral: true}
	}
}

func (r *Router) ensureDataPermission(ctx context.Context, request Request, write bool) Response {
	if request.IsOwner || request.IsGuildAdmin {
		return Response{}
	}
	if r.admin == nil {
		return Response{Content: "You do not have permission to manage Panda data.", Ephemeral: true}
	}
	check := r.admin.CanReadConfig
	denial := "You do not have permission to export Panda data."
	if write {
		check = r.admin.CanWriteConfig
		denial = "You do not have permission to delete Panda data."
	}
	allowed, err := check(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: denial, Ephemeral: true}
	}
	return Response{}
}

func (r *Router) handleBilling(ctx context.Context, request Request) Response {
	if request.GuildID == "" {
		return Response{Content: "Billing is managed per Discord server.", Ephemeral: true}
	}
	if r.billing == nil {
		return Response{Content: "Billing is not configured for this runtime.", Ephemeral: true, Presentation: Presentation{Title: "Billing unavailable", Accent: AccentWarning}}
	}
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Subcommand, request.Options["action"])))
	switch action {
	case "", "status":
	case "activate":
		return r.handleBillingActivate(ctx, request)
	case "revoke":
		return r.handleBillingRevoke(ctx, request)
	default:
		return Response{Content: "Billing action must be `status`, `activate`, or `revoke`.", Ephemeral: true, Presentation: Presentation{Title: "Unknown billing action", Accent: AccentWarning}}
	}

	entitlement, err := r.billing.Resolve(ctx, request.GuildID)
	if err != nil && !errors.Is(err, billing.ErrNoSubscription) {
		return Response{Content: "Billing status could not be loaded.", Ephemeral: true, Presentation: Presentation{Title: "Billing lookup failed", Accent: AccentWarning}}
	}
	if errors.Is(err, billing.ErrNoSubscription) {
		content := "No Panda subscription is active for this server."
		content += "\nStart a SOL purchase from the Panda landing page, then run `/billing action:activate api_key:<key>`."
		return Response{Content: content, Ephemeral: true, Presentation: Presentation{Title: "No active subscription", Accent: AccentWarning}}
	}
	content := entitlement.SummaryText()
	if entitlement.UpgradeURL != "" {
		content += "\n\nPurchase or renew on the Panda landing page, then activate with `/billing action:activate api_key:<key>`."
		return Response{
			Content:      content,
			Ephemeral:    true,
			Presentation: Presentation{Title: "Panda Billing", Accent: AccentInfo},
			Actions:      []Action{{Label: "Open Panda pricing", URL: entitlement.UpgradeURL}},
		}
	}
	content += "\n\nPurchase or renew on the Panda landing page, then activate with `/billing action:activate api_key:<key>`."
	return Response{Content: content, Ephemeral: true, Presentation: Presentation{Title: "Panda Billing", Accent: AccentInfo}}
}

func (r *Router) handleBillingActivate(ctx context.Context, request Request) Response {
	apiKey := strings.TrimSpace(request.Options["api_key"])
	if apiKey == "" {
		return Response{Content: "Paste the one-time activation key from the Panda landing page: `/billing action:activate api_key:<key>`.", Ephemeral: true, Presentation: Presentation{Title: "Activation key required", Accent: AccentWarning}}
	}
	result, err := r.billing.ActivateWithAPIKey(ctx, billing.ActivateAPIKeyRequest{
		GuildID:         request.GuildID,
		ActorUserID:     request.UserID,
		ActorIsOperator: request.IsOwner,
		ActorCanClaim:   request.IsGuildAdmin,
		APIKey:          apiKey,
	})
	if err != nil {
		return billingActivationErrorResponse(err)
	}
	return Response{
		Content:      fmt.Sprintf("Panda %s is active for this server through %s.", result.Entitlement.Plan.DisplayName, result.Entitlement.PeriodEnd.Format("2006-01-02")),
		Ephemeral:    true,
		Presentation: Presentation{Title: "Plan activated", Accent: AccentSuccess},
	}
}

func (r *Router) handleBillingRevoke(ctx context.Context, request Request) Response {
	if !request.IsOwner {
		return Response{Content: "Only Panda operators can revoke activation keys.", Ephemeral: true, Presentation: Presentation{Title: "Operator required", Accent: AccentWarning}}
	}
	orderID := strings.TrimSpace(request.Options["order_id"])
	if orderID == "" {
		return Response{Content: "Provide the SOL payment order ID to revoke its unused activation key.", Ephemeral: true, Presentation: Presentation{Title: "Order ID required", Accent: AccentWarning}}
	}
	revocation, err := r.billing.RevokeActivationAPIKey(ctx, billing.RevokeActivationAPIKeyRequest{
		PaymentOrderID:  orderID,
		ActorUserID:     request.UserID,
		ActorIsOperator: true,
		Reason:          "operator command",
	})
	if err != nil {
		return billingRevocationErrorResponse(err)
	}
	return Response{
		Content:      fmt.Sprintf("Activation key `%s...` for Panda %s order `%s` was revoked.", revocation.Prefix, revocation.Order.DisplayName, revocation.Order.OrderID),
		Ephemeral:    true,
		Presentation: Presentation{Title: "Activation key revoked", Accent: AccentSuccess},
	}
}

func billingActivationErrorResponse(err error) Response {
	switch {
	case errors.Is(err, billing.ErrBillingAccess):
		return Response{Content: "Only the current billing owner, a guild admin claiming an unclaimed server, or a Panda operator can activate billing for this server.", Ephemeral: true, Presentation: Presentation{Title: "Billing owner required", Accent: AccentWarning}}
	case errors.Is(err, billing.ErrActivationKeyExpired):
		return Response{Content: "That activation key has expired. Create a fresh SOL payment order from the Panda landing page or contact support.", Ephemeral: true, Presentation: Presentation{Title: "Activation key expired", Accent: AccentWarning}}
	case errors.Is(err, billing.ErrActivationKeyConsumed):
		return Response{Content: "That activation key has already been used.", Ephemeral: true, Presentation: Presentation{Title: "Activation key used", Accent: AccentWarning}}
	case errors.Is(err, billing.ErrActivationKeyRevoked):
		return Response{Content: "That activation key is no longer available. Contact Panda support with your order ID.", Ephemeral: true, Presentation: Presentation{Title: "Activation key revoked", Accent: AccentWarning}}
	case errors.Is(err, billing.ErrActivationKeyInvalid):
		return Response{Content: "That activation key could not be validated for this server.", Ephemeral: true, Presentation: Presentation{Title: "Activation failed", Accent: AccentWarning}}
	case errors.Is(err, billing.ErrSolPaymentOrderNotVerified):
		return Response{Content: "The SOL payment for that activation key is not verified yet.", Ephemeral: true, Presentation: Presentation{Title: "Payment not verified", Accent: AccentWarning}}
	default:
		return Response{Content: "Billing activation could not be completed. Please try again later or contact Panda support.", Ephemeral: true, Presentation: Presentation{Title: "Activation failed", Accent: AccentWarning}}
	}
}

func billingRevocationErrorResponse(err error) Response {
	switch {
	case errors.Is(err, billing.ErrBillingAccess):
		return Response{Content: "Only Panda operators can revoke activation keys.", Ephemeral: true, Presentation: Presentation{Title: "Operator required", Accent: AccentWarning}}
	case errors.Is(err, billing.ErrActivationKeyConsumed):
		return Response{Content: "That activation key has already been consumed and cannot be revoked.", Ephemeral: true, Presentation: Presentation{Title: "Key already consumed", Accent: AccentWarning}}
	case errors.Is(err, billing.ErrActivationKeyRevoked):
		return Response{Content: "That activation key is already revoked.", Ephemeral: true, Presentation: Presentation{Title: "Key already revoked", Accent: AccentWarning}}
	case errors.Is(err, billing.ErrActivationKeyExpired):
		return Response{Content: "That activation key is already expired.", Ephemeral: true, Presentation: Presentation{Title: "Key expired", Accent: AccentWarning}}
	case errors.Is(err, billing.ErrActivationKeyInvalid), errors.Is(err, billing.ErrSolPaymentOrderNotFound):
		return Response{Content: "No unused activation key was found for that payment order.", Ephemeral: true, Presentation: Presentation{Title: "Key not found", Accent: AccentWarning}}
	default:
		return Response{Content: "Activation key revocation could not be completed.", Ephemeral: true, Presentation: Presentation{Title: "Revocation failed", Accent: AccentWarning}}
	}
}

func renderDataSummary(summary repository.GuildDataSummary) string {
	lines := []string{
		"Safe Panda data export summary:",
		fmt.Sprintf("- Guild ID: `%s`", summary.GuildID),
		fmt.Sprintf("- Knowledge: `%d document(s), %d chunk(s), %s`", summary.KnowledgeDocuments, summary.KnowledgeChunks, formatDataBytes(summary.KnowledgeStorageBytes)),
		fmt.Sprintf("- Conversations: `%d conversation(s), %d message metadata row(s), %d Discord event(s), %d attachment record(s)`", summary.Conversations, summary.Messages, summary.DiscordEvents, summary.Attachments),
		fmt.Sprintf("- Memory consent records: `%d`", summary.MemoryConsentRecords),
		fmt.Sprintf("- Billing: `%d account(s), %d subscription(s), %d invoice/payment event(s)`", summary.CustomerAccounts, summary.Subscriptions, summary.InvoicePaymentEvents),
		fmt.Sprintf("- Usage and cost: `%d usage period(s), %d reservation(s), %d cost ledger event(s)`", summary.UsagePeriods, summary.UsageReservations, summary.CostLedgerEvents),
		fmt.Sprintf("- Automations: `%d schedule(s), %d alert rule(s), %d composed tool(s), %d composed run(s)`", summary.Schedules, summary.AlertRules, summary.ComposedTools, summary.ComposedToolRuns),
		fmt.Sprintf("- Music: `%d queue item(s), %d playlist(s)`", summary.MusicQueueItems, summary.MusicPlaylists),
		"- Raw prompts/messages and provider diagnostics are not included in this Discord export.",
	}
	if summary.CurrentSubscriptionPlan != "" || summary.CurrentSubscriptionState != "" {
		lines = append(lines, fmt.Sprintf("- Current subscription: `%s / %s`", firstNonEmpty(summary.CurrentSubscriptionPlan, "none"), firstNonEmpty(summary.CurrentSubscriptionState, "none")))
	}
	return strings.Join(lines, "\n")
}

func renderDataDeletion(summary repository.GuildDataSummary) string {
	keys := repository.SortedDeletionKeys(summary.Deleted)
	if len(keys) == 0 {
		return "No matching Panda data rows were found for deletion."
	}
	lines := []string{"Deleted Panda data rows:"}
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("- `%s`: `%d`", key, summary.Deleted[key]))
	}
	lines = append(lines, "Audit logs were retained.")
	return strings.Join(lines, "\n")
}

func formatDataBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := int64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTPE"[exp])
}

func (r *Router) handleAdmin(ctx context.Context, request Request) Response {
	if request.GuildID == "" {
		return Response{Content: "Admin commands must be run inside a Discord server.", Ephemeral: true}
	}
	if r.admin == nil {
		return Response{Content: "Admin commands are not configured for this runtime.", Ephemeral: true}
	}

	subcommand := strings.ToLower(request.Subcommand)
	if subcommand == "" {
		subcommand = "status"
	}
	if denied := r.ensureFeatureEnabled(ctx, request, adminFeatureForSubcommand(subcommand)); denied.Content != "" {
		return denied
	}
	if subcommand == "feature" || subcommand == "features" {
		return r.handleAdminFeatures(ctx, request)
	}
	if !request.IsGuildAdmin && !request.IsOwner {
		if subcommand == "soul" {
			allowed, err := r.admin.CanWriteSoul(ctx, assistantAccessRequest(request))
			if err != nil {
				return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
			}
			if !allowed {
				return Response{Content: "Only the Panda owner, server owner or administrator, moderator, creator, or delegated soul writer can update Panda's soul.", Ephemeral: true}
			}
		} else {
			check, denial := r.adminPermissionForSubcommand(subcommand)
			allowed, err := check(ctx, assistantAccessRequest(request))
			if err != nil {
				return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
			}
			if !allowed {
				return Response{Content: denial, Ephemeral: true}
			}
		}
	}

	switch subcommand {
	case "role":
		return r.handleAdminRoleProfile(ctx, request)
	case "member-role", "member_role":
		return r.handleAdminMemberRole(ctx, request)
	case "tool":
		return r.handleAdminToolAccess(ctx, request)
	case "channel", "channels":
		return r.handleAdminChannelAccess(ctx, request)
	case "behavior":
		return r.handleAdminBehavior(ctx, request)
	case "prompt":
		return r.handleAdminPrompt(ctx, request)
	case "soul":
		return r.handleAdminSoul(ctx, request)
	case "audit":
		return r.handleAdminAudit(ctx, request)
	case "status":
		return r.handleAdminStatus(ctx, request)
	case "billing":
		return r.handleBilling(ctx, request)
	case "setup":
		return r.handleAdminSetup(ctx, request)
	case "alerts", "alert":
		return r.handleAdminAlerts(ctx, request)
	case "feedback":
		return r.handleAdminFeedback(ctx, request)
	case "music":
		return r.handleAdminMusic(ctx, request)
	case "enable":
		return r.handleAdminToggle(ctx, request, true)
	case "disable":
		return r.handleAdminToggle(ctx, request, false)
	default:
		return Response{Content: "Unknown admin command.", Ephemeral: true}
	}
}

func roleDisplay(roleID, roleName string) string {
	roleID = strings.TrimSpace(roleID)
	roleName = strings.TrimSpace(roleName)
	if roleName == "" {
		return fmt.Sprintf("`%s`", roleID)
	}
	return fmt.Sprintf("`%s` (`%s`)", roleName, roleID)
}

func (r *Router) handleAdminRoleProfile(ctx context.Context, request Request) Response {
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "list")))
	switch action {
	case "list", "":
		roles, err := r.admin.ListRolePermissions(ctx, request.GuildID)
		if err != nil {
			return Response{Content: "Role profile lookup failed.", Ephemeral: true}
		}
		return Response{Content: renderRoleProfiles(roles), Ephemeral: true}
	case "set", "add":
		if denied := r.ensureGuildControl(ctx, request, "Only the Panda owner, server owner or administrator, or the current Panda admin role can set Panda role profiles."); denied.Content != "" {
			return denied
		}
		profile, roleID, roleName, response := roleProfileOptions(request)
		if response.Content != "" {
			return response
		}
		if _, err := r.admin.ApplyRoleProfile(ctx, request.GuildID, request.UserID, roleID, profile); err != nil {
			return Response{Content: "Role profile could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("%s is now a Panda %s role.", roleDisplay(roleID, roleName), admin.RoleProfileLabel(profile)), Ephemeral: true}
	case "remove", "unset":
		if denied := r.ensureGuildControl(ctx, request, "Only the Panda owner, server owner or administrator, or the current Panda admin role can remove Panda role profiles."); denied.Content != "" {
			return denied
		}
		profile, roleID, roleName, response := roleProfileOptions(request)
		if response.Content != "" {
			return response
		}
		if err := r.admin.RemoveRoleProfile(ctx, request.GuildID, request.UserID, roleID, profile); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return Response{Content: "That role profile was not configured for this role.", Ephemeral: true}
			}
			return Response{Content: "Role profile could not be removed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed the Panda %s profile from %s.", admin.RoleProfileLabel(profile), roleDisplay(roleID, roleName)), Ephemeral: true}
	default:
		return Response{Content: "`action` must be `list`, `set`, or `remove`.", Ephemeral: true}
	}
}

func roleProfileOptions(request Request) (string, string, string, Response) {
	profile, ok := admin.NormalizeRoleProfile(request.Options["profile"])
	if !ok {
		return "", "", "", Response{Content: "`profile` must be `admin` or `moderator`.", Ephemeral: true}
	}
	roleID := strings.TrimSpace(firstNonEmpty(request.Options["role_id"], request.Options["role"]))
	if roleID == "" {
		return "", "", "", Response{Content: "Choose a Discord role.", Ephemeral: true}
	}
	return profile, roleID, request.Options["role_name"], Response{}
}

func renderRoleProfiles(roles []store.GuildRole) string {
	adminRoles := roleIDsForPermission(roles, admin.PermissionAdminBadge)
	moderatorRoles := roleIDsForPermission(roles, admin.PermissionModerationUse)
	var builder strings.Builder
	builder.WriteString("Panda role profiles:\n")
	builder.WriteString(roleProfileLine("admin", adminRoles))
	builder.WriteString("\n")
	builder.WriteString(roleProfileLine("moderator", moderatorRoles))
	builder.WriteString("\n\nModerator roles include `assistant.use` and `moderation.use`.")
	return builder.String()
}

func roleIDsForPermission(roles []store.GuildRole, permission string) []string {
	seen := map[string]struct{}{}
	var ids []string
	for _, role := range roles {
		if role.Permission != permission {
			continue
		}
		if _, ok := seen[role.RoleID]; ok {
			continue
		}
		seen[role.RoleID] = struct{}{}
		ids = append(ids, role.RoleID)
	}
	sort.Strings(ids)
	return ids
}

func roleProfileLine(profile string, roleIDs []string) string {
	if len(roleIDs) == 0 {
		return fmt.Sprintf("- %s: not configured", profile)
	}
	values := make([]string, 0, len(roleIDs))
	for _, roleID := range roleIDs {
		values = append(values, fmt.Sprintf("`%s`", roleID))
	}
	return fmt.Sprintf("- %s: %s", profile, strings.Join(values, ", "))
}

func (r *Router) handleAdminMemberRole(ctx context.Context, request Request) Response {
	if denied := r.ensureGuildControl(ctx, request, "Only the Panda owner, server owner or administrator, or the current Panda admin role can assign Discord roles."); denied.Content != "" {
		return denied
	}
	if r.memberRoles == nil {
		return Response{Content: "Discord role assignment is not configured for this runtime.", Ephemeral: true}
	}
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "add")))
	memberRequest, response := memberRoleOptions(request)
	if response.Content != "" {
		return response
	}
	switch action {
	case "add", "assign", "set":
		memberRequest.Reason = "Panda admin member-role add"
		if err := r.memberRoles.AddMemberRole(ctx, memberRequest); err != nil {
			return Response{Content: "Discord role could not be assigned. Check Panda's Manage Roles permission and role hierarchy.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Assigned %s to %s.", roleDisplay(memberRequest.RoleID, request.Options["role_name"]), userMention(memberRequest.UserID, request.Options["member_user_name"])), Ephemeral: true}
	case "remove", "unassign", "unset":
		memberRequest.Reason = "Panda admin member-role remove"
		if err := r.memberRoles.RemoveMemberRole(ctx, memberRequest); err != nil {
			return Response{Content: "Discord role could not be removed. Check Panda's Manage Roles permission and role hierarchy.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed %s from %s.", roleDisplay(memberRequest.RoleID, request.Options["role_name"]), userMention(memberRequest.UserID, request.Options["member_user_name"])), Ephemeral: true}
	default:
		return Response{Content: "`action` must be `add` or `remove`.", Ephemeral: true}
	}
}

func memberRoleOptions(request Request) (MemberRoleRequest, Response) {
	userID := strings.TrimSpace(firstNonEmpty(request.Options["member_user_id"], firstNonEmpty(request.Options["user_id"], request.Options["user"])))
	roleID := strings.TrimSpace(firstNonEmpty(request.Options["role_id"], request.Options["role"]))
	if userID == "" {
		return MemberRoleRequest{}, Response{Content: "Choose a Discord user.", Ephemeral: true}
	}
	if roleID == "" {
		return MemberRoleRequest{}, Response{Content: "Choose a Discord role.", Ephemeral: true}
	}
	return MemberRoleRequest{GuildID: request.GuildID, UserID: userID, RoleID: roleID, ActorID: request.UserID}, Response{}
}

func userMention(userID, username string) string {
	userID = strings.TrimSpace(userID)
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Sprintf("`%s`", userID)
	}
	return fmt.Sprintf("`%s` (`%s`)", username, userID)
}

func (r *Router) handleAdminToolAccess(ctx context.Context, request Request) Response {
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "list")))
	toolName := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["tool_name"], request.Options["tool"])))
	roleID := strings.TrimSpace(firstNonEmpty(request.Options["role_id"], request.Options["role"]))
	switch action {
	case "list", "":
		roles, err := r.admin.ListToolRoles(ctx, request.GuildID)
		if err != nil {
			return Response{Content: "Tool access lookup failed.", Ephemeral: true}
		}
		if len(roles) == 0 {
			return Response{Content: "No role-specific tool access rules are configured. Native tools use their normal permission policy; composed tools are admin-only until a role is allowed.", Ephemeral: true}
		}
		lines := []string{"Tool access rules:"}
		for _, role := range roles {
			lines = append(lines, fmt.Sprintf("- `%s` -> `%s`", role.ToolName, role.RoleID))
		}
		return Response{Content: strings.Join(lines, "\n"), Ephemeral: true}
	case "add", "allow":
		if toolName == "" || roleID == "" {
			return Response{Content: "Provide `tool_name` and `role` to allow a role to use a tool.", Ephemeral: true}
		}
		toolRole, err := r.admin.AddToolRole(ctx, request.GuildID, request.UserID, toolName, roleID)
		if err != nil {
			return Response{Content: "Tool access could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Allowed %s to use `%s`.", roleDisplay(toolRole.RoleID, request.Options["role_name"]), toolRole.ToolName), Ephemeral: true}
	case "remove", "deny":
		if toolName == "" || roleID == "" {
			return Response{Content: "Provide `tool_name` and `role` to remove tool access.", Ephemeral: true}
		}
		if err := r.admin.RemoveToolRole(ctx, request.GuildID, request.UserID, toolName, roleID); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return Response{Content: "That tool access rule was not found.", Ephemeral: true}
			}
			return Response{Content: "Tool access could not be removed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed %s from `%s`.", roleDisplay(roleID, request.Options["role_name"]), toolName), Ephemeral: true}
	default:
		return Response{Content: "`action` must be `list`, `add`, or `remove`.", Ephemeral: true}
	}
}

func (r *Router) handleAdminChannelAccess(ctx context.Context, request Request) Response {
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "list")))
	channelID, channelName := channelOptions(request)
	switch action {
	case "list", "":
		rules, err := r.admin.ListChannelRules(ctx, request.GuildID)
		if err != nil {
			return Response{Content: "Channel access lookup failed.", Ephemeral: true}
		}
		return Response{Content: renderChannelRules(rules), Ephemeral: true}
	case "allow", "add":
		if channelID == "" {
			return Response{Content: "Choose a `channel` to allow Panda assistant use there.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("Panda assistant use would be allowed in %s.", channelDisplay(channelID, channelName))
		}
		rule, err := r.admin.SetChannelRule(ctx, request.GuildID, request.UserID, channelID, "allow")
		if err != nil {
			return Response{Content: "Channel access rule could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Allowed Panda assistant use in %s. Because an allow rule exists, other channels need their own allow rule unless the user is an admin.", channelDisplay(rule.ChannelID, channelName)), Ephemeral: true}
	case "deny", "block":
		if channelID == "" {
			return Response{Content: "Choose a `channel` to deny Panda assistant use there.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("Panda assistant use would be denied in %s.", channelDisplay(channelID, channelName))
		}
		rule, err := r.admin.SetChannelRule(ctx, request.GuildID, request.UserID, channelID, "deny")
		if err != nil {
			return Response{Content: "Channel access rule could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Denied Panda assistant use in %s.", channelDisplay(rule.ChannelID, channelName)), Ephemeral: true}
	case "remove", "clear":
		if channelID == "" {
			return Response{Content: "Choose a `channel` to remove from Panda channel access rules.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("The Panda channel access rule for %s would be removed.", channelDisplay(channelID, channelName))
		}
		if err := r.admin.RemoveChannelRule(ctx, request.GuildID, request.UserID, channelID); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return Response{Content: "That channel access rule was not found.", Ephemeral: true}
			}
			return Response{Content: "Channel access rule could not be removed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed Panda channel access rule for %s.", channelDisplay(channelID, channelName)), Ephemeral: true}
	default:
		return Response{Content: "`action` must be `list`, `allow`, `deny`, or `remove`.", Ephemeral: true}
	}
}

func channelOptions(request Request) (string, string) {
	channelID := normalizeChannelID(firstNonEmpty(request.Options["channel_id"], request.Options["channel"]))
	channelName := strings.TrimPrefix(strings.TrimSpace(request.Options["channel_name"]), "#")
	return channelID, channelName
}

func normalizeChannelID(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "<#") && strings.HasSuffix(value, ">") {
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "<#"), ">"))
	}
	return value
}

func channelDisplay(channelID, channelName string) string {
	channelID = normalizeChannelID(channelID)
	channelName = strings.TrimPrefix(strings.TrimSpace(channelName), "#")
	if channelName != "" {
		return fmt.Sprintf("`#%s` (`%s`)", channelName, channelID)
	}
	return fmt.Sprintf("`%s`", channelID)
}

func renderChannelRules(rules []store.GuildChannelRule) string {
	if len(rules) == 0 {
		return "No channel access rules are configured. Panda assistant use is available in any channel where Discord permissions allow it."
	}
	hasAllow := false
	for _, rule := range rules {
		if rule.Rule == "allow" {
			hasAllow = true
			break
		}
	}
	header := "Channel access rules:"
	if hasAllow {
		header = "Channel access rules (allow-list active):"
	}
	lines := []string{header}
	for _, rule := range rules {
		lines = append(lines, fmt.Sprintf("- `%s` %s", rule.Rule, channelDisplay(rule.ChannelID, "")))
	}
	if hasAllow {
		lines = append(lines, "Only allowed channels can use Panda assistant features unless the user is an admin.")
	}
	return strings.Join(lines, "\n")
}

func (r *Router) handleAdminBehavior(ctx context.Context, request Request) Response {
	settings, parseErr := behaviorSettingsFromOptions(request.Options)
	if parseErr != nil {
		return Response{Content: parseErr.Error(), Ephemeral: true}
	}
	if dryRunRequested(request) {
		return dryRunResponse("behavior settings would be updated. %s", renderBehaviorSettingsDryRun(settings))
	}
	config, err := r.admin.ConfigureBehavior(ctx, request.GuildID, request.UserID, settings)
	if err != nil {
		return Response{Content: "Behavior update failed.", Ephemeral: true}
	}
	return Response{Content: fmt.Sprintf("Behavior settings updated. Answer length `%s`, tool policy `%s`.", answerLengthFromMaxTokens(config.MaxResponseTokens), config.ToolPolicy), Ephemeral: true}
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
		return Response{Content: "Prompt update failed.", Ephemeral: true}
	}
	return Response{Content: fmt.Sprintf("Server prompt updated (%d characters).", len(config.SystemPromptOverlay)), Ephemeral: true}
}

func (r *Router) handleAdminSoul(ctx context.Context, request Request) Response {
	soul := strings.TrimSpace(request.Options["soul"])
	if dryRunRequested(request) {
		return dryRunResponse("agent soul would be updated (%d characters).", len(soul))
	}
	if soul == "" {
		return soulModalResponse(request.UserID)
	}
	config, err := r.admin.SetSoul(ctx, request.GuildID, request.UserID, soul)
	if err != nil {
		return Response{Content: "Soul update failed.", Ephemeral: true}
	}
	return Response{Content: fmt.Sprintf("Agent soul updated (%d characters).", len(config.AgentSoul)), Ephemeral: true}
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

func (r *Router) invocationContext(ctx context.Context, request Request) string {
	if r.context == nil || strings.TrimSpace(request.GuildID) == "" || strings.TrimSpace(request.ChannelID) == "" {
		return ""
	}
	packed, err := r.context.RecentMessagesSinceContext(ctx, contextsvc.ChannelRef{
		GuildID:   request.GuildID,
		ChannelID: request.ChannelID,
	}, invocationContextLimit, time.Now().UTC().Add(-invocationContextWindow))
	if err != nil {
		slog.Warn("invocation context fetch failed", slog.Any("err", err), slog.String("guild_id", request.GuildID), slog.String("channel_id", request.ChannelID), slog.String("request_id", request.RequestID))
		return ""
	}
	if strings.TrimSpace(packed.Text) == "" || len(packed.Citations) == 0 {
		return ""
	}
	return packed.Text
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
	reservation, denied := r.beginAIUsage(ctx, request)
	if denied.Content != "" {
		return denied
	}

	toolFilter := r.toolFilter(ctx, request)
	enabledFeatures, featureGateActive := r.featureSetForAccess(ctx, request.GuildID)
	invocationContext := r.invocationContext(ctx, request)
	answer, err := r.assistant.Ask(ctx, assistant.AskRequest{
		RequestID:                    request.RequestID,
		GuildID:                      request.GuildID,
		UserID:                       request.UserID,
		ChannelID:                    request.ChannelID,
		VoiceChannelID:               request.VoiceChannelID,
		Question:                     question,
		InvocationContext:            invocationContext,
		RoleIDs:                      request.RoleIDs,
		IsGuildAdmin:                 request.IsGuildAdmin,
		IsOwner:                      request.IsOwner,
		AllowedPermissions:           r.allowedToolPermissions(ctx, request),
		AllowedTools:                 toolFilter.allowed,
		RestrictedTools:              toolFilter.restricted,
		EnabledFeatures:              enabledFeatures,
		FeatureGateActive:            featureGateActive,
		RequireExplicitComposedTools: toolFilter.requireExplicitComposed,
	})
	if err != nil {
		r.releaseAIUsage(ctx, reservation)
		return assistantError(err)
	}
	if strings.TrimSpace(answer.Content) == "" {
		r.releaseAIUsage(ctx, reservation)
		return Response{Content: "Panda returned an empty response. Please try again.", Ephemeral: true}
	}
	r.commitAIUsage(ctx, reservation)
	return r.responseFromAssistantAnswer(ctx, request, answer, "", "")
}

func (r *Router) handleChat(ctx context.Context, request Request) Response {
	return r.handleChatMode(ctx, request, true)
}

func (r *Router) handleChatMode(ctx context.Context, request Request, threaded bool) Response {
	return r.handleChatModeWithAccess(ctx, request, threaded, false)
}

func (r *Router) handleChatModeWithAccess(ctx context.Context, request Request, threaded bool, allowSoulWriter bool) Response {
	question := strings.TrimSpace(request.Options["question"])
	if question == "" {
		return Response{Content: "Please include a message.", Ephemeral: true}
	}
	if denied := r.ensureAssistantAllowed(ctx, request); denied.Content != "" {
		if !allowSoulWriter || !r.canWriteSoul(ctx, request) {
			return denied
		}
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
	reservation, denied := r.beginAIUsage(ctx, request)
	if denied.Content != "" {
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
			r.releaseAIUsage(ctx, reservation)
			return Response{Content: "I could not create a chat thread here. Please check my thread permissions.", Ephemeral: true}
		}
		chatChannelID = thread.ID
		threadID = thread.ID
		threadName = thread.Name
	}

	toolFilter := r.toolFilter(ctx, request)
	enabledFeatures, featureGateActive := r.featureSetForAccess(ctx, request.GuildID)
	invocationContext := r.invocationContext(ctx, request)
	answer, err := r.assistant.Chat(ctx, assistant.AskRequest{
		RequestID:                    request.RequestID,
		GuildID:                      request.GuildID,
		UserID:                       request.UserID,
		ChannelID:                    chatChannelID,
		VoiceChannelID:               request.VoiceChannelID,
		ThreadID:                     threadID,
		Question:                     question,
		PreferredTool:                request.PreferredTool,
		InvocationContext:            invocationContext,
		ReplyContent:                 request.Options["reply_text"],
		ReplyMessageID:               request.Options["reply_message_id"],
		ReplyAuthorIsBot:             truthyOption(request.Options["reply_author_is_bot"]),
		RoleIDs:                      request.RoleIDs,
		IsGuildAdmin:                 request.IsGuildAdmin,
		IsOwner:                      request.IsOwner,
		AllowedPermissions:           r.allowedToolPermissions(ctx, request),
		AllowedTools:                 toolFilter.allowed,
		RestrictedTools:              toolFilter.restricted,
		EnabledFeatures:              enabledFeatures,
		FeatureGateActive:            featureGateActive,
		RequireExplicitComposedTools: toolFilter.requireExplicitComposed,
	})
	if err != nil {
		r.releaseAIUsage(ctx, reservation)
		return assistantError(err)
	}
	r.commitAIUsage(ctx, reservation)
	return r.responseFromAssistantAnswer(ctx, request, answer, threadID, threadName)
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

	toolFilter := r.toolFilter(ctx, request)
	enabledFeatures, featureGateActive := r.featureSetForAccess(ctx, request.GuildID)
	invocationContext := r.invocationContext(ctx, request)
	task := BackgroundTask{
		RequestID:                    request.RequestID,
		GuildID:                      request.GuildID,
		UserID:                       request.UserID,
		ChannelID:                    request.ChannelID,
		VoiceChannelID:               request.VoiceChannelID,
		Command:                      request.Command,
		Input:                        input,
		InvocationContext:            invocationContext,
		Tone:                         request.Options["tone"],
		Language:                     request.Options["language"],
		Detail:                       request.Options["detail"],
		RoleIDs:                      request.RoleIDs,
		IsGuildAdmin:                 request.IsGuildAdmin,
		IsOwner:                      request.IsOwner,
		AllowedPermissions:           permissionNames(r.allowedToolPermissions(ctx, request)),
		AllowedTools:                 permissionNames(toolFilter.allowed),
		RestrictedTools:              permissionNames(toolFilter.restricted),
		EnabledFeatures:              permissionNames(enabledFeatures),
		FeatureGateActive:            featureGateActive,
		RequireExplicitComposedTools: toolFilter.requireExplicitComposed,
	}
	if shouldBackgroundTask(request, input) {
		if denied := r.checkAIUsageAvailable(ctx, request); denied.Content != "" {
			return denied
		}
		return Response{
			Content:      "Queued long summary. The result will replace this response when it is ready.",
			Presentation: Presentation{Title: "Summary queued", Accent: AccentInfo},
			Background:   &task,
		}
	}
	return r.HandleBackgroundTask(ctx, task)
}

func (r *Router) HandleBackgroundTask(ctx context.Context, task BackgroundTask) Response {
	request := Request{
		RequestID: task.RequestID,
		Command:   task.Command,
		GuildID:   task.GuildID,
		ChannelID: task.ChannelID,
		UserID:    task.UserID,
	}
	if denied := r.ensureFeatureEnabled(ctx, request, features.AssistantChat); denied.Content != "" {
		return denied
	}
	reservation, denied := r.beginAIUsage(ctx, Request{
		RequestID: task.RequestID,
		Command:   task.Command,
		GuildID:   task.GuildID,
		ChannelID: task.ChannelID,
		UserID:    task.UserID,
	})
	if denied.Content != "" {
		return denied
	}
	enabledFeatures, featureGateActive := r.featureSetForAccess(ctx, task.GuildID)
	if !featureGateActive && task.FeatureGateActive {
		enabledFeatures = permissionsFromNames(task.EnabledFeatures)
		featureGateActive = true
	}
	answer, err := r.assistant.CompleteTask(ctx, assistant.TaskRequest{
		RequestID:                    task.RequestID,
		GuildID:                      task.GuildID,
		UserID:                       task.UserID,
		ChannelID:                    task.ChannelID,
		VoiceChannelID:               task.VoiceChannelID,
		Command:                      task.Command,
		Input:                        task.Input,
		InvocationContext:            task.InvocationContext,
		Tone:                         task.Tone,
		Language:                     task.Language,
		Detail:                       task.Detail,
		RoleIDs:                      task.RoleIDs,
		IsGuildAdmin:                 task.IsGuildAdmin,
		IsOwner:                      task.IsOwner,
		AllowedPermissions:           permissionsFromNames(task.AllowedPermissions),
		AllowedTools:                 permissionsFromNames(task.AllowedTools),
		RestrictedTools:              permissionsFromNames(task.RestrictedTools),
		EnabledFeatures:              enabledFeatures,
		FeatureGateActive:            featureGateActive,
		RequireExplicitComposedTools: task.RequireExplicitComposedTools,
	})
	if err != nil {
		r.releaseAIUsage(ctx, reservation)
		return assistantError(err)
	}
	r.commitAIUsage(ctx, reservation)
	return r.responseFromAssistantAnswer(ctx, Request{
		RequestID: task.RequestID,
		Command:   task.Command,
		GuildID:   task.GuildID,
		ChannelID: task.ChannelID,
		UserID:    task.UserID,
	}, answer, "", "")
}

func (r *Router) HandleToolConfirmation(ctx context.Context, request ToolConfirmationRequest) Response {
	if request.Request.GuildID == "" {
		return Response{Content: "This confirmation must be used inside a Discord server.", Ephemeral: true}
	}
	if denied := r.ensureFeatureEnabled(ctx, request.Request, toolConfirmationFeature(request.Action)); denied.Content != "" {
		return denied
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
	case toolActionBudgetLimitSet:
		scope := strings.ToLower(strings.TrimSpace(request.Options["scope"]))
		if !validBudgetScope(scope) {
			return Response{Content: "That budget-limit confirmation is invalid.", Ephemeral: true}
		}
		if scope == repository.BudgetScopeGlobal {
			if !request.Request.IsOwner {
				return Response{Content: "Only a bot owner can set global limits.", Ephemeral: true}
			}
		} else if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage limits."); denied.Content != "" {
			return denied
		}
		subjectID := strings.TrimSpace(request.Options["subject_id"])
		if scope == repository.BudgetScopeGuild && subjectID == "" {
			subjectID = request.Request.GuildID
		}
		limit := intOption(request.Options["limit"], 0)
		windowSeconds := intOption(request.Options["window_seconds"], 0)
		if limit <= 0 || windowSeconds <= 0 {
			return Response{Content: "That budget-limit confirmation is invalid.", Ephemeral: true}
		}
		saved, err := r.admin.SetBudgetLimit(ctx, request.Request.GuildID, request.Request.UserID, store.BudgetLimit{
			Scope:         scope,
			SubjectID:     subjectID,
			Limit:         limit,
			WindowSeconds: windowSeconds,
		})
		if err != nil {
			return Response{Content: "Budget limit could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Set `%s` budget limit for `%s` to %d request(s) per %d seconds.", saved.Scope, firstNonEmpty(saved.SubjectID, "global"), saved.Limit, saved.WindowSeconds), Ephemeral: true}
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
	case toolActionRolePermissionAdd:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage role permissions."); denied.Content != "" {
			return denied
		}
		roleID := strings.TrimSpace(request.Options["role_id"])
		permission := strings.TrimSpace(request.Options["permission"])
		if roleID == "" || !admin.IsPermissionNameAllowed(permission) {
			return Response{Content: "That role-permission confirmation is invalid.", Ephemeral: true}
		}
		if _, err := r.admin.AddRolePermission(ctx, request.Request.GuildID, request.Request.UserID, roleID, permission); err != nil {
			return Response{Content: "Role permission could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Granted `%s` to role `%s`.", permission, roleID), Ephemeral: true}
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
	case toolActionRoleProfileAdd:
		if denied := r.ensureGuildControl(ctx, request.Request, "Only the Panda owner, server owner or administrator, or the current Panda admin role can set Panda role profiles."); denied.Content != "" {
			return denied
		}
		roleID := strings.TrimSpace(request.Options["role_id"])
		profile, ok := admin.NormalizeRoleProfile(request.Options["profile"])
		if roleID == "" || !ok {
			return Response{Content: "That role-profile confirmation is invalid.", Ephemeral: true}
		}
		if _, err := r.admin.ApplyRoleProfile(ctx, request.Request.GuildID, request.Request.UserID, roleID, profile); err != nil {
			return Response{Content: "Role profile could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Role `%s` is now a Panda %s role.", roleID, admin.RoleProfileLabel(profile)), Ephemeral: true}
	case toolActionRoleProfileRemove:
		if denied := r.ensureGuildControl(ctx, request.Request, "Only the Panda owner, server owner or administrator, or the current Panda admin role can remove Panda role profiles."); denied.Content != "" {
			return denied
		}
		roleID := strings.TrimSpace(request.Options["role_id"])
		profile, ok := admin.NormalizeRoleProfile(request.Options["profile"])
		if roleID == "" || !ok {
			return Response{Content: "That role-profile confirmation is invalid.", Ephemeral: true}
		}
		if err := r.admin.RemoveRoleProfile(ctx, request.Request.GuildID, request.Request.UserID, roleID, profile); err != nil {
			return toolConfirmationError(err, "Role profile could not be removed.", "That role profile was not configured for this role.")
		}
		return Response{Content: fmt.Sprintf("Removed the Panda %s profile from role `%s`.", admin.RoleProfileLabel(profile), roleID), Ephemeral: true}
	case toolActionDiscordRoleCreate:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to create Discord roles."); denied.Content != "" {
			return denied
		}
		if r.roles == nil {
			return Response{Content: "Discord role creation is not configured for this runtime.", Ephemeral: true}
		}
		name := strings.TrimSpace(request.Options["name"])
		if name == "" {
			return Response{Content: "That role-creation confirmation is invalid.", Ephemeral: true}
		}
		role, err := r.roles.CreateRole(ctx, DiscordRoleRequest{
			GuildID: request.Request.GuildID,
			Name:    name,
			ActorID: request.Request.UserID,
			Reason:  "Panda natural-language role creation",
		})
		if err != nil {
			return discordRoleCreateErrorResponse(err)
		}
		return Response{Content: fmt.Sprintf("Created Discord role `%s` (`%s`).", role.Name, role.ID), Ephemeral: true}
	case toolActionDiscordPollCreate:
		return r.handleDiscordPollConfirmation(ctx, request)
	case toolActionMemberRoleAdd, toolActionMemberRoleRemove:
		if denied := r.ensureGuildControl(ctx, request.Request, "Only the Panda owner, server owner or administrator, or the current Panda admin role can assign Discord roles."); denied.Content != "" {
			return denied
		}
		if r.memberRoles == nil {
			return Response{Content: "Discord role assignment is not configured for this runtime.", Ephemeral: true}
		}
		memberRequest := MemberRoleRequest{
			GuildID: request.Request.GuildID,
			UserID:  strings.TrimSpace(request.Options["user_id"]),
			RoleID:  strings.TrimSpace(request.Options["role_id"]),
			ActorID: request.Request.UserID,
		}
		if memberRequest.UserID == "" || memberRequest.RoleID == "" {
			return Response{Content: "That member-role confirmation is invalid.", Ephemeral: true}
		}
		if request.Action == toolActionMemberRoleAdd {
			memberRequest.Reason = "Panda natural-language member-role add"
			if err := r.memberRoles.AddMemberRole(ctx, memberRequest); err != nil {
				return Response{Content: "Discord role could not be assigned. Check Panda's Manage Roles permission and role hierarchy.", Ephemeral: true}
			}
			return Response{Content: fmt.Sprintf("Assigned role `%s` to user `%s`.", memberRequest.RoleID, memberRequest.UserID), Ephemeral: true}
		}
		memberRequest.Reason = "Panda natural-language member-role remove"
		if err := r.memberRoles.RemoveMemberRole(ctx, memberRequest); err != nil {
			return Response{Content: "Discord role could not be removed. Check Panda's Manage Roles permission and role hierarchy.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed role `%s` from user `%s`.", memberRequest.RoleID, memberRequest.UserID), Ephemeral: true}
	case toolActionToolAccessAdd, toolActionToolAccessRemove:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage tool access."); denied.Content != "" {
			return denied
		}
		toolName := strings.TrimSpace(request.Options["tool_name"])
		roleID := strings.TrimSpace(request.Options["role_id"])
		if toolName == "" || roleID == "" {
			return Response{Content: "That tool-access confirmation is invalid.", Ephemeral: true}
		}
		if request.Action == toolActionToolAccessAdd {
			if _, err := r.admin.AddToolRole(ctx, request.Request.GuildID, request.Request.UserID, toolName, roleID); err != nil {
				return Response{Content: "Tool access could not be saved.", Ephemeral: true}
			}
			return Response{Content: fmt.Sprintf("Allowed `%s` for role `%s`.", toolName, roleID), Ephemeral: true}
		}
		if err := r.admin.RemoveToolRole(ctx, request.Request.GuildID, request.Request.UserID, toolName, roleID); err != nil {
			return toolConfirmationError(err, "Tool access could not be removed.", "That tool access rule was not found.")
		}
		return Response{Content: fmt.Sprintf("Removed `%s` access for role `%s`.", toolName, roleID), Ephemeral: true}
	case toolActionChannelRuleSet:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage channel rules."); denied.Content != "" {
			return denied
		}
		channelID := strings.TrimSpace(request.Options["channel_id"])
		rule := strings.ToLower(strings.TrimSpace(request.Options["rule"]))
		if channelID == "" || (rule != "allow" && rule != "deny") {
			return Response{Content: "That channel-rule confirmation is invalid.", Ephemeral: true}
		}
		saved, err := r.admin.SetChannelRule(ctx, request.Request.GuildID, request.Request.UserID, channelID, rule)
		if err != nil {
			return Response{Content: "Channel rule could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Set `%s` channel access rule for `%s`.", saved.Rule, saved.ChannelID), Ephemeral: true}
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
	case toolActionComposedToolApprove:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanApproveComposedTool, "You do not have permission to approve composed tools."); denied.Content != "" {
			return denied
		}
		if r.composed == nil {
			return Response{Content: "Composed tools are not configured for this runtime.", Ephemeral: true}
		}
		toolName := strings.TrimSpace(request.Options["tool_name"])
		version := intOption(request.Options["version"], 1)
		if toolName == "" || version <= 0 {
			return Response{Content: "That composed-tool approval confirmation is invalid.", Ephemeral: true}
		}
		result, err := r.composed.Approve(ctx, request.Request.GuildID, toolName, version, request.Request.UserID)
		if err != nil {
			return toolConfirmationError(err, "Composed tool could not be approved.", "That composed tool version was not found.")
		}
		return Response{Content: fmt.Sprintf("Approved `%s` version %d. Risk: `%s`.", result.Tool, result.Version, result.Validation.RiskLevel), Ephemeral: true}
	case toolActionComposedToolRollback:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanApproveComposedTool, "You do not have permission to roll back composed tools."); denied.Content != "" {
			return denied
		}
		if r.composed == nil {
			return Response{Content: "Composed tools are not configured for this runtime.", Ephemeral: true}
		}
		toolName := strings.TrimSpace(request.Options["tool_name"])
		version := intOption(request.Options["version"], 0)
		if toolName == "" || version <= 0 {
			return Response{Content: "That composed-tool rollback confirmation is invalid.", Ephemeral: true}
		}
		result, err := r.composed.Rollback(ctx, request.Request.GuildID, toolName, version, request.Request.UserID)
		if err != nil {
			return toolConfirmationError(err, "Composed tool could not be rolled back.", "That approved composed tool version was not found.")
		}
		return Response{Content: fmt.Sprintf("Rolled `%s` back to version %d.", result.Tool, result.Version), Ephemeral: true}
	default:
		return Response{Content: "That confirmation is no longer supported.", Ephemeral: true}
	}
}

func toolConfirmationFeature(action string) string {
	switch action {
	case toolActionKnowledgeDelete:
		return features.Knowledge
	case toolActionBudgetLimitSet,
		toolActionBudgetLimitRemove,
		toolActionRolePermissionAdd,
		toolActionRolePermissionRemove,
		toolActionRoleProfileAdd,
		toolActionRoleProfileRemove,
		toolActionToolAccessAdd,
		toolActionToolAccessRemove,
		toolActionChannelRuleSet,
		toolActionChannelRuleRemove:
		return features.AdminAccessControl
	case toolActionDiscordRoleCreate,
		toolActionMemberRoleAdd,
		toolActionMemberRoleRemove:
		return features.DiscordRoleManagement
	case toolActionDiscordPollCreate:
		return features.Polls
	case toolActionComposedToolApprove,
		toolActionComposedToolRollback:
		return features.ComposedTools
	default:
		return ""
	}
}

func (r *Router) handleDiscordPollConfirmation(ctx context.Context, request ToolConfirmationRequest) Response {
	if r.tools == nil {
		return Response{Content: "Discord poll creation is not configured for this runtime.", Ephemeral: true}
	}
	arguments, err := discordPollConfirmationArguments(request.Options)
	if err != nil {
		return Response{Content: "That poll confirmation is invalid.", Ephemeral: true}
	}
	result, err := r.tools.Execute(ctx, toolsvc.ExecutionRequest{
		GuildID:              request.Request.GuildID,
		ChannelID:            request.Request.ChannelID,
		ActorID:              request.Request.UserID,
		RequestID:            request.Request.RequestID,
		InvocationType:       "tool_confirmation",
		RoleIDs:              append([]string(nil), request.Request.RoleIDs...),
		IsGuildAdmin:         request.Request.IsGuildAdmin,
		IsOwner:              request.Request.IsOwner,
		Access:               r.toolAccess(ctx, request.Request, toolsvc.ToolPolicyWriteConfirmed),
		AllowConfirmedWrites: true,
		Call: llm.ToolCall{
			ID:   "confirmed-discord-poll",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "discord_create_poll",
				Arguments: arguments,
			},
		},
	})
	if err != nil {
		return Response{Content: "Poll could not be sent: " + err.Error(), Ephemeral: true, Presentation: Presentation{Title: "Poll not sent", Accent: AccentWarning}}
	}
	if !strings.Contains(result.Message.Content, `"created":true`) {
		return Response{Content: "Poll could not be sent.", Ephemeral: true, Presentation: Presentation{Title: "Poll not sent", Accent: AccentWarning}}
	}
	return Response{Content: "Sent the poll.", Presentation: Presentation{Title: "Poll sent", Accent: AccentSuccess}}
}

func discordPollConfirmationArguments(options map[string]string) (string, error) {
	channelID := strings.TrimSpace(options["channel_id"])
	question := strings.TrimSpace(options["question"])
	rawAnswers := strings.TrimSpace(options["answers_json"])
	if channelID == "" || question == "" || rawAnswers == "" {
		return "", fmt.Errorf("missing poll fields")
	}
	var answers any
	if err := json.Unmarshal([]byte(rawAnswers), &answers); err != nil {
		return "", err
	}
	arguments := map[string]any{
		"channel_id": channelID,
		"question":   question,
		"answers":    answers,
	}
	if raw := strings.TrimSpace(options["duration_hours"]); raw != "" {
		durationHours, err := strconv.Atoi(raw)
		if err != nil {
			return "", err
		}
		arguments["duration_hours"] = durationHours
	}
	if raw := strings.TrimSpace(options["allow_multiselect"]); raw != "" {
		arguments["allow_multiselect"] = truthyOption(raw)
	}
	if content := strings.TrimSpace(options["content"]); content != "" {
		arguments["content"] = content
	}
	data, err := json.Marshal(arguments)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (r *Router) HandleFeedback(ctx context.Context, request FeedbackRequest) Response {
	if r.feedback == nil {
		return Response{Content: "Feedback is not configured for this runtime.", Ephemeral: true}
	}
	if err := r.feedback.Record(ctx, request.TargetID, request.Request.GuildID, request.Request.UserID, request.Rating, ""); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return Response{Content: "That answer feedback target is no longer available.", Ephemeral: true, Presentation: Presentation{Title: "Feedback unavailable", Accent: AccentWarning}}
		}
		return Response{Content: "Feedback could not be recorded.", Ephemeral: true, Presentation: Presentation{Title: "Feedback failed", Accent: AccentWarning}}
	}
	return Response{Content: "Feedback recorded. Thank you.", Ephemeral: true, Presentation: Presentation{Title: "Feedback recorded", Accent: AccentSuccess}}
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
	if denied := r.ensureFeatureEnabled(ctx, request, features.AssistantChat); denied.Content != "" {
		return denied
	}
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
	if denied := r.ensureFeatureEnabled(ctx, request, features.Threads); denied.Content != "" {
		return denied
	}
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
	if denied := r.ensureFeatureEnabled(ctx, request, features.Attachments); denied.Content != "" {
		return denied
	}
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
	if denied := r.ensureFeatureEnabled(ctx, request, features.Knowledge); denied.Content != "" {
		return denied
	}
	allowed, err := r.admin.CanReadMemory(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: "You do not have permission to search server knowledge.", Ephemeral: true}
	}
	return Response{}
}

func (r *Router) ensureFeatureEnabled(ctx context.Context, request Request, featureID string) Response {
	featureID = strings.TrimSpace(featureID)
	if featureID == "" || r.features == nil {
		return Response{}
	}
	if featureID == features.OwnerOps && request.IsOwner && strings.TrimSpace(request.GuildID) == "" {
		return Response{}
	}
	enabled, err := r.features.Enabled(ctx, request.GuildID, featureID)
	if err != nil {
		slog.Warn("feature lookup failed", slog.Any("err", err), slog.String("guild_id", request.GuildID), slog.String("feature_id", featureID), slog.String("request_id", request.RequestID))
		return Response{Content: "Feature status could not be checked. Please try again later.", Ephemeral: true, Presentation: Presentation{Title: "Feature lookup failed", Accent: AccentWarning}}
	}
	if enabled {
		return Response{}
	}
	return disabledFeatureResponse(featureID)
}

func disabledFeatureResponse(featureID string) Response {
	label := featureLabel(featureID)
	return Response{
		Content:   fmt.Sprintf("The `%s` feature is not enabled for this server. A server admin can enable it from Panda setup and reauthorize if Discord permissions are needed.", label),
		Ephemeral: true,
		Presentation: Presentation{
			Title:  "Feature not enabled",
			Accent: AccentWarning,
		},
	}
}

func featureLabel(featureID string) string {
	feature, ok := features.Lookup(featureID)
	if !ok || strings.TrimSpace(feature.Label) == "" {
		return strings.TrimSpace(featureID)
	}
	return feature.Label
}

func (r *Router) ensureGuildControl(ctx context.Context, request Request, denial string) Response {
	allowed, err := r.admin.HasGuildControl(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: denial, Ephemeral: true}
	}
	return Response{}
}

func assistantAccessRequest(request Request) admin.AssistantAccessRequest {
	return admin.AssistantAccessRequest{
		GuildID:      request.GuildID,
		ChannelID:    request.ChannelID,
		UserID:       request.UserID,
		RoleIDs:      request.RoleIDs,
		IsGuildAdmin: request.IsGuildAdmin,
		IsOwner:      request.IsOwner,
	}
}

type toolFilter struct {
	allowed                 map[string]struct{}
	restricted              map[string]struct{}
	requireExplicitComposed bool
}

func (r *Router) toolFilter(ctx context.Context, request Request) toolFilter {
	if r.admin == nil || request.GuildID == "" {
		return toolFilter{}
	}
	accessRequest := assistantAccessRequest(request)
	hasControl, err := r.admin.HasGuildControl(ctx, accessRequest)
	if err != nil || hasControl {
		return toolFilter{}
	}
	roles, err := r.admin.ToolRoleAccess(ctx, request.GuildID, request.RoleIDs)
	if err != nil {
		return toolFilter{allowed: map[string]struct{}{}, restricted: map[string]struct{}{}, requireExplicitComposed: true}
	}
	return toolFilter{
		allowed:                 namesToSet(roles.AllowedTools),
		restricted:              namesToSet(roles.RestrictedTools),
		requireExplicitComposed: true,
	}
}

func (r *Router) toolAccess(ctx context.Context, request Request, policy string) toolsvc.ToolAccess {
	filter := r.toolFilter(ctx, request)
	enabledFeatures, featureGateActive := r.featureSetForAccess(ctx, request.GuildID)
	return toolsvc.ToolAccess{
		Policy:                       policy,
		Permissions:                  r.allowedToolPermissions(ctx, request),
		AllowedTools:                 filter.allowed,
		RestrictedTools:              filter.restricted,
		EnabledFeatures:              enabledFeatures,
		FeatureGateActive:            featureGateActive,
		RequireExplicitComposedTools: filter.requireExplicitComposed,
	}
}

func (r *Router) allowedToolPermissions(ctx context.Context, request Request) map[string]struct{} {
	permissions := map[string]struct{}{}
	if r.admin == nil {
		return permissions
	}
	enabledFeatures, featureGateActive := r.featureSetForAccess(ctx, request.GuildID)
	featureEnabled := func(featureID string) bool {
		return !featureGateActive || features.Has(enabledFeatures, featureID)
	}
	if featureEnabled(features.AssistantChat) {
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantUse, r.admin.CanUseAssistant)
	}
	if featureEnabled(features.Threads) {
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantUseThreads, r.admin.CanUseThreads)
	}
	if featureEnabled(features.Attachments) {
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantAttachments, r.admin.CanUseAttachments)
	}
	if featureEnabled(features.Knowledge) {
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantMemoryRead, r.admin.CanReadMemory)
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminMemoryManage, r.admin.CanManageMemory)
	}
	if featureEnabled(features.WebSearch) {
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantWebSearch, r.admin.CanUseWebSearch)
	}
	if featureEnabled(features.AdminSetup) || featureEnabled(features.AdminAccessControl) || featureEnabled(features.AdminAudit) {
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminConfigRead, r.admin.CanReadConfig)
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminConfigWrite, r.admin.CanWriteConfig)
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantSoulWrite, r.admin.CanWriteSoul)
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminUsageRead, r.admin.CanReadUsage)
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminAuditRead, r.admin.CanReadAudit)
	}
	if featureEnabled(features.ComposedTools) {
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionToolComposeDraft, r.admin.CanDraftComposedTool)
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionToolComposeApprove, r.admin.CanApproveComposedTool)
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionToolComposeInvoke, r.admin.CanInvokeComposedTool)
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionToolComposeAudit, r.admin.CanAuditComposedTool)
	}
	if featureEnabled(features.ModerationAssist) {
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionModerationUse, r.admin.CanUseModeration)
	}
	if featureEnabled(features.OwnerOps) || (request.IsOwner && strings.TrimSpace(request.GuildID) == "") {
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionOwnerOps, r.admin.CanUseOwnerOps)
	}
	return permissions
}

func (r *Router) featureSetForAccess(ctx context.Context, guildID string) (map[string]struct{}, bool) {
	if r.features == nil || strings.TrimSpace(guildID) == "" {
		return map[string]struct{}{}, false
	}
	enabled, err := r.features.EnabledSet(ctx, guildID)
	if err != nil {
		slog.Warn("feature set lookup failed", slog.Any("err", err), slog.String("guild_id", guildID))
		return map[string]struct{}{}, true
	}
	return enabled, true
}

func namesToSet(names []string) map[string]struct{} {
	values := map[string]struct{}{}
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			values[name] = struct{}{}
		}
	}
	return values
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

func (r *Router) checkAIUsageAvailable(ctx context.Context, request Request) Response {
	if r.billing == nil || request.GuildID == "" {
		return Response{}
	}
	_, err := r.billing.Check(ctx, request.GuildID, billing.MetricAIResponse, 1)
	return billingErrorResponse(err)
}

func (r *Router) beginAIUsage(ctx context.Context, request Request) (billing.Reservation, Response) {
	if r.billing == nil || request.GuildID == "" {
		return billing.Reservation{}, Response{}
	}
	reservation, err := r.billing.BeginUsage(ctx, request.GuildID, billing.MetricAIResponse, 1)
	return reservation, billingErrorResponse(err)
}

func (r *Router) commitAIUsage(ctx context.Context, reservation billing.Reservation) {
	if r.billing != nil && reservation.ID != "" {
		_ = r.billing.CommitUsage(ctx, reservation)
	}
}

func (r *Router) releaseAIUsage(ctx context.Context, reservation billing.Reservation) {
	if r.billing != nil && reservation.ID != "" {
		_ = r.billing.ReleaseUsage(ctx, reservation)
	}
}

func billingErrorResponse(err error) Response {
	if err == nil {
		return Response{}
	}
	var quotaErr billing.QuotaError
	if errors.As(err, &quotaErr) {
		content := fmt.Sprintf("This server has used its included %s for the current billing period.", billing.MetricLabel(quotaErr.Metric))
		content += "\nThe billing owner can run `/billing` for the Panda pricing link, then activate a verified SOL purchase with `/billing action:activate api_key:<key>`."
		return Response{Content: content, Ephemeral: true, Presentation: Presentation{Title: "Usage limit reached", Accent: AccentWarning}}
	}
	if errors.Is(err, billing.ErrNoSubscription) {
		return Response{Content: "This server does not have an active Panda subscription. Use `/billing` for status and the SOL purchase link.", Ephemeral: true, Presentation: Presentation{Title: "Subscription required", Accent: AccentWarning}}
	}
	if errors.Is(err, billing.ErrReadOnly) {
		return Response{Content: "This server's Panda subscription is read-only. Billing, help, export/delete, and support access remain available.", Ephemeral: true, Presentation: Presentation{Title: "Subscription read-only", Accent: AccentWarning}}
	}
	return Response{Content: "Billing status could not be checked. Please try again later.", Ephemeral: true, Presentation: Presentation{Title: "Billing check failed", Accent: AccentWarning}}
}

func billingErrorResponseIfBilling(err error) (Response, bool) {
	if err == nil {
		return Response{}, false
	}
	var quotaErr billing.QuotaError
	if errors.As(err, &quotaErr) || errors.Is(err, billing.ErrNoSubscription) || errors.Is(err, billing.ErrReadOnly) {
		return billingErrorResponse(err), true
	}
	return Response{}, false
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
		return Response{Content: "I cannot answer yet because Panda's AI service is not configured.", Ephemeral: true, Presentation: Presentation{Title: "Assistant not configured", Accent: AccentWarning}}
	case errors.Is(err, assistant.ErrAssistantDisabled):
		return Response{Content: "Assistant responses are disabled for this server.", Ephemeral: true, Presentation: Presentation{Title: "Assistant disabled", Accent: AccentWarning}}
	default:
		slog.Warn("assistant request failed", slog.Any("err", err))
		return Response{Content: "Panda is having trouble with AI responses right now. Please try again later.", Ephemeral: true, Presentation: Presentation{Title: "AI response failed", Accent: AccentDanger}}
	}
}

func (r *Router) responseFromAssistantAnswer(ctx context.Context, request Request, answer assistant.AskResponse, threadID, threadName string) Response {
	response := Response{
		Content:    answer.Content,
		ThreadID:   threadID,
		ThreadName: threadName,
	}
	if answer.Card != nil {
		response.Content = firstNonEmpty(answer.Card.Content, response.Content)
		response.Presentation = presentationFromAssistantCard(answer.Card)
		response.Actions = actionsFromAssistantCard(answer.Card)
	}
	if confirmation := ToolConfirmationFromAssistant(request.UserID, answer.Confirmation); confirmation != nil {
		response.Confirmation = confirmation
		response.Content = appendConfirmationNotice(response.Content, answer.Confirmation.Summary)
		response.Presentation = Presentation{Title: "Confirmation required", Accent: AccentWarning}
		if confirmation.Danger {
			response.Presentation.Accent = AccentDanger
		}
		return response
	}
	if r.feedback != nil && assistantFeedbackEligible(answer, response.Content) {
		target, err := r.feedback.CreateTarget(ctx, feedback.TargetRequest{
			GuildID:   request.GuildID,
			ChannelID: request.ChannelID,
			UserID:    request.UserID,
			Command:   firstNonEmpty(request.Command, "assistant"),
			Content:   response.Content,
			Metadata: map[string]string{
				"request_id":      request.RequestID,
				"thread_id":       threadID,
				"used_web_search": strconv.FormatBool(answer.UsedWebSearch),
			},
		})
		if err == nil && target.ID != 0 {
			response.Feedback = &FeedbackControls{TargetID: target.ID}
		}
	}
	return response
}

func assistantFeedbackEligible(answer assistant.AskResponse, content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	if answer.UsedWebSearch {
		return true
	}
	if answer.Card != nil {
		return false
	}
	return utf8.RuneCountInString(content) >= feedbackMinimumPlainTextRunes
}

func presentationFromAssistantCard(card *assistant.ToolCard) Presentation {
	if card == nil {
		return Presentation{}
	}
	return Presentation{
		Title:  card.Title,
		Accent: assistantCardAccent(card.Accent),
		URL:    card.URL,
		Fields: fieldsFromAssistantCard(card.Fields),
	}
}

func assistantCardAccent(accent string) Accent {
	switch strings.ToLower(strings.TrimSpace(accent)) {
	case "music":
		return AccentMusic
	case "success":
		return AccentSuccess
	case "warning":
		return AccentWarning
	case "danger":
		return AccentDanger
	case "info":
		return AccentInfo
	default:
		return AccentDefault
	}
}

func fieldsFromAssistantCard(fields []assistant.ToolCardField) []Field {
	result := make([]Field, 0, len(fields))
	for _, field := range fields {
		result = append(result, Field{
			Name:   field.Name,
			Value:  field.Value,
			Inline: field.Inline,
		})
	}
	return result
}

func actionsFromAssistantCard(card *assistant.ToolCard) []Action {
	if card == nil {
		return nil
	}
	actions := make([]Action, 0, len(card.Actions))
	for _, action := range card.Actions {
		actions = append(actions, Action{
			Label: action.Label,
			URL:   action.URL,
		})
	}
	return actions
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

func discordRoleCreateErrorResponse(err error) Response {
	if errors.Is(err, ErrDiscordRoleSetup) {
		return Response{
			Content:      "Discord role could not be created. Give Panda's bot role `Manage Roles`, move Panda's bot role high enough in the server role list, then try again.",
			Ephemeral:    true,
			Presentation: Presentation{Title: "Role setup required", Accent: AccentWarning},
		}
	}
	return Response{Content: "Discord role could not be created. Please try again later.", Ephemeral: true}
}

func renderAudit(events []store.AuditEvent) string {
	var builder strings.Builder
	builder.WriteString("Recent audit events:\n")
	for _, event := range events {
		fmt.Fprintf(&builder, "- %s by `%s` at %s\n", event.Action, event.ActorID, event.CreatedAt.UTC().Format(time.RFC3339))
	}
	return security.SafeDiscordContent(builder.String())
}

func behaviorSettingsFromOptions(options map[string]string) (admin.BehaviorSettings, error) {
	settings := admin.BehaviorSettings{}

	if raw := strings.TrimSpace(options["temperature"]); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return admin.BehaviorSettings{}, fmt.Errorf("Provide a numeric `temperature` between 0 and 2.")
		}
		settings.Temperature = value
		settings.TemperatureSet = true
	}

	if raw := strings.TrimSpace(options["answer_length"]); raw != "" {
		settings.AnswerLength = raw
		settings.AnswerLengthSet = true
	}

	if raw := firstNonEmpty(options["max_response_tokens"], options["max_tokens"]); strings.TrimSpace(raw) != "" {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return admin.BehaviorSettings{}, fmt.Errorf("Provide a numeric `max_response_tokens` value.")
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

func renderBehaviorSettingsDryRun(settings admin.BehaviorSettings) string {
	var parts []string
	if settings.TemperatureSet {
		parts = append(parts, fmt.Sprintf("temperature %.2f", settings.Temperature))
	}
	if settings.AnswerLengthSet {
		parts = append(parts, fmt.Sprintf("answer length `%s`", settings.AnswerLength))
	}
	if settings.MaxResponseTokensSet {
		parts = append(parts, fmt.Sprintf("max response %d tokens", settings.MaxResponseTokens))
	}
	if settings.ToolPolicySet {
		parts = append(parts, fmt.Sprintf("tool policy `%s`", settings.ToolPolicy))
	}
	if len(parts) == 0 {
		return "No behavior setting changes were provided."
	}
	return strings.Join(parts, ", ") + "."
}

func answerLengthFromMaxTokens(tokens int) string {
	switch {
	case tokens <= 500:
		return "brief"
	case tokens >= 1600:
		return "detailed"
	default:
		return "standard"
	}
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
	return Response{Content: "Dry run: " + fmt.Sprintf(format, args...), Ephemeral: true, Presentation: Presentation{Title: "Dry run", Accent: AccentInfo}}
}

func validBudgetScope(scope string) bool {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case repository.BudgetScopeGlobal, repository.BudgetScopeGuild, repository.BudgetScopeUser, repository.BudgetScopeChannel:
		return true
	default:
		return false
	}
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
		title = textutil.Truncate(title, 72, "")
	}
	return "Panda: " + title
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
