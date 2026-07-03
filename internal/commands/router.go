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
	"github.com/sn0w/panda2/internal/generated"
	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/music"
	"github.com/sn0w/panda2/internal/ops"
	"github.com/sn0w/panda2/internal/polls"
	"github.com/sn0w/panda2/internal/ratelimit"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/runtimecontrol"
	"github.com/sn0w/panda2/internal/scheduler"
	"github.com/sn0w/panda2/internal/security"
	setupsvc "github.com/sn0w/panda2/internal/setup"
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
	serverSetup ServerSetupManager
	tools       *toolsvc.Executor
	rateLimit   *ratelimit.Limiter
	features    *features.Service
	install     FeatureInstallIntentCreator
	runtime     *runtimecontrol.Service
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

type ServerSetupManager interface {
	Confirm(ctx context.Context, projectID, actorID string, enqueue bool) (store.GuildSetupProject, error)
	RollbackProject(ctx context.Context, projectID, actorID string) (setupsvc.ApplyResult, error)
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
	"**Billing key entry**\n" +
	"- Use `/billing action:activate api_key:<key>` for one-time activation keys so secrets stay out of normal chat.\n\n" +
	"**Music**\n" +
	"- Say `Panda play <song> in <voice channel>`, or join a voice channel and say `Panda play <song>`.\n" +
	"- Natural controls: `skip and play <song>`, `pause`, `resume`, `skip`, `stop`, `queue`, `clear queue`, `now playing`.\n\n" +
	"**Message actions**\n" +
	"- Use **Explain with Panda** or **Summarize with Panda** from a message's **Apps** menu.\n" +
	"- Ask Panda in chat to create polls, reminders, schedules, and setup changes; write actions use confirmation buttons."

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

func (r *Router) WithServerSetupManager(manager ServerSetupManager) *Router {
	r.serverSetup = manager
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

func (r *Router) WithRuntimeStatus(service *runtimecontrol.Service) *Router {
	r.runtime = service
	return r
}

func (r *Router) CommitResponseUsage(ctx context.Context, response Response) error {
	if r == nil || r.billing == nil {
		return nil
	}
	return r.finishResponseUsage(ctx, response, true, map[string]struct{}{})
}

func (r *Router) ReleaseResponseUsage(ctx context.Context, response Response) error {
	if r == nil || r.billing == nil {
		return nil
	}
	return r.finishResponseUsage(ctx, response, false, map[string]struct{}{})
}

func (r *Router) finishResponseUsage(ctx context.Context, response Response, commit bool, seen map[string]struct{}) error {
	var errs []error
	for _, reservation := range response.UsageReservations {
		id := strings.TrimSpace(reservation.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		var err error
		if commit {
			err = r.billing.CommitUsage(ctx, reservation)
		} else {
			err = r.billing.ReleaseUsage(ctx, reservation)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	for _, followup := range response.Followups {
		if err := r.finishResponseUsage(ctx, followup, commit, seen); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *Router) Handle(ctx context.Context, request Request) Response {
	if !quietModeExempt(request) && r.quietModeActive(ctx, request) {
		return Response{}
	}
	if !maintenanceExempt(request) {
		if response := r.maintenanceResponse(ctx); response.Content != "" {
			return response
		}
	}
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
		builder.WriteString("\n\n**Admin setup through chat**\n")
		builder.WriteString("- Ask Panda to set admin/moderator users or roles, channel restrictions, tool access, prompt, or personality.\n")
		builder.WriteString("- Panda can inspect settings, ask for missing choices, and prepare confirmations in chat.\n")
		if access.soul {
			builder.WriteString("- Personality/tone changes can be brainstormed first, then saved when you explicitly ask.\n")
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
	return r.HandleNaturalMessageStream(ctx, request, nil)
}

func (r *Router) HandleNaturalMessageStream(ctx context.Context, request Request, onRespond func()) Response {
	response, err := r.handleNaturalMessageStream(ctx, request, onRespond, nil, nil)
	if err != nil {
		return assistantError(err)
	}
	return response
}

func (r *Router) HandleNaturalMessageStreamWithToolStart(ctx context.Context, request Request, onRespond func(), onToolStart func(string)) Response {
	response, err := r.handleNaturalMessageStream(ctx, request, onRespond, onToolStart, nil)
	if err != nil {
		return assistantError(err)
	}
	return response
}

func (r *Router) HandleNaturalMessageStreamWithToolProgress(ctx context.Context, request Request, onRespond func(), onToolStart func(string), onToolProgress func(string, string)) Response {
	response, err := r.handleNaturalMessageStream(ctx, request, onRespond, onToolStart, onToolProgress)
	if err != nil {
		return assistantError(err)
	}
	return response
}

func (r *Router) handleNaturalMessageStream(ctx context.Context, request Request, onRespond func(), onToolStart func(string), onToolProgress func(string, string)) (Response, error) {
	message := strings.TrimSpace(firstNonEmpty(request.Options["message"], request.Options["question"]))
	if message == "" {
		return Response{}, nil
	}
	slog.Info("natural message router received",
		slog.String("guild_id", request.GuildID),
		slog.String("channel_id", request.ChannelID),
		slog.String("request_id", request.RequestID),
		slog.String("user_id", request.UserID),
		slog.Bool("bot_mentioned", truthyOption(request.Options["bot_mentioned"])),
		slog.String("reply_message_id", request.Options["reply_message_id"]),
		slog.Int("image_ref_count", len(request.ImageReferences)),
		slog.Any("image_ref_ids", commandImageReferenceIDs(request.ImageReferences)),
	)
	if r.quietModeActive(ctx, request) {
		return Response{}, nil
	}
	if response := r.maintenanceResponse(ctx); response.Content != "" {
		return response, nil
	}
	if !r.canHandleNaturalMessage(ctx, request) {
		return Response{}, nil
	}
	if denied := r.checkAIUsageAvailable(ctx, request); denied.Content != "" {
		return denied, nil
	}
	request.Command = "chat"
	if request.Options == nil {
		request.Options = map[string]string{}
	}
	request.Options["question"] = message
	return r.handleChatModeWithOptionsResult(ctx, request, chatModeOptions{
		threaded:        false,
		allowSoulWriter: true,
		naturalMessage:  true,
		onRespond:       onRespond,
		onToolStart:     onToolStart,
		onToolProgress:  onToolProgress,
	})
}

func (r *Router) canHandleNaturalMessage(ctx context.Context, request Request) bool {
	if r.admin == nil {
		slog.Warn("natural message access unavailable",
			slog.String("reason", "admin_service_missing"),
			slog.String("guild_id", request.GuildID),
			slog.String("channel_id", request.ChannelID),
			slog.String("request_id", request.RequestID),
			slog.String("user_id", request.UserID),
		)
		return false
	}
	if denied := r.ensureFeatureEnabled(ctx, request, features.AssistantChat); denied.Content != "" {
		return false
	}
	accessRequest := assistantAccessRequest(request)
	allowed, err := r.admin.CanUseAssistant(ctx, accessRequest)
	if err != nil {
		slog.Warn("natural message assistant access lookup failed",
			slog.Any("err", err),
			slog.String("guild_id", request.GuildID),
			slog.String("channel_id", request.ChannelID),
			slog.String("request_id", request.RequestID),
			slog.String("user_id", request.UserID),
		)
		return false
	}
	if !allowed && !r.canWriteSoul(ctx, request) {
		return false
	}
	denied, err := r.naturalChatDenied(ctx, request)
	if err != nil {
		slog.Warn("natural message chat access lookup failed",
			slog.Any("err", err),
			slog.String("guild_id", request.GuildID),
			slog.String("channel_id", request.ChannelID),
			slog.String("request_id", request.RequestID),
			slog.String("user_id", request.UserID),
		)
		return false
	}
	return !denied
}

func (r *Router) canWriteSoul(ctx context.Context, request Request) bool {
	if r.admin == nil {
		return false
	}
	allowed, err := r.admin.CanWriteSoul(ctx, assistantAccessRequest(request))
	return err == nil && allowed
}

func (r *Router) naturalChatDenied(ctx context.Context, request Request) (bool, error) {
	if r == nil || r.admin == nil || strings.TrimSpace(request.GuildID) == "" {
		return false, nil
	}
	accessRequest := assistantAccessRequest(request)
	hasControl, err := r.admin.HasGuildControl(ctx, accessRequest)
	if err != nil || hasControl {
		return false, err
	}
	access, err := r.admin.ToolUserRoleAccess(ctx, request.GuildID, request.UserID, request.RoleIDs)
	if err != nil {
		return false, err
	}
	for _, toolName := range access.DeniedTools {
		if strings.EqualFold(strings.TrimSpace(toolName), toolsvc.ToolNamePandaChat) {
			return true, nil
		}
	}
	return false, nil
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
		return Response{Content: fmt.Sprintf("Health: sqlite=%s discord=%s shards=%s ai_service=%s image_service=%s queued_jobs=%d guild_configs=%d draining=%t incident=%t data_dir=`%s`.", health.SQLite, health.Discord, health.Shards, health.AIService, health.ImageService, health.QueuedJobs, health.ConfiguredGuildCount, health.Draining, health.Incident, health.DataDir), Ephemeral: true}
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
				fmt.Sprintf("- Pack: `%s`", entitlement.Pack.DisplayName),
				fmt.Sprintf("- Credit account: `%s`", entitlement.Status),
				fmt.Sprintf("- Credits: `%s`", billing.FormatCredits(entitlement.AvailableCredits, entitlement.ReservedCredits)),
				fmt.Sprintf("- Retention: `%d day(s)`", entitlement.RetentionDays),
			)
		} else if errors.Is(err, billing.ErrNoCreditAccount) {
			lines = append(lines, "- Credit account: `none`")
		} else {
			lines = append(lines, "- Credit account: `lookup failed`")
		}
	}
	if r.data != nil {
		if summary, err := r.data.Summary(ctx, request.GuildID); err == nil {
			lines = append(lines,
				fmt.Sprintf("- Knowledge documents: `%d`", summary.KnowledgeDocuments),
				fmt.Sprintf("- Conversation records: `%d conversations / %d messages`", summary.Conversations, summary.Messages),
				fmt.Sprintf("- Memory consent records: `%d`", summary.MemoryConsentRecords),
				fmt.Sprintf("- Billing records: `%d account(s) / %d historical billing record(s)`", summary.CustomerAccounts, summary.Subscriptions),
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
	default:
		return Response{Content: "Billing action must be `status` or `activate`.", Ephemeral: true, Presentation: Presentation{Title: "Unknown billing action", Accent: AccentWarning}}
	}

	entitlement, err := r.billing.Resolve(ctx, request.GuildID)
	if err != nil && !errors.Is(err, billing.ErrNoCreditAccount) {
		return Response{Content: "Billing status could not be loaded.", Ephemeral: true, Presentation: Presentation{Title: "Billing lookup failed", Accent: AccentWarning}}
	}
	if errors.Is(err, billing.ErrNoCreditAccount) {
		content := "No Panda credit account is active for this server."
		content += "\nBuy credits from the Panda landing page, then run `/billing action:activate api_key:<key>`."
		return Response{Content: content, Ephemeral: true, Presentation: Presentation{Title: "No active credit account", Accent: AccentWarning}}
	}
	content := entitlement.SummaryText()
	if entitlement.UpgradeURL != "" {
		content += "\n\nBuy credits on the Panda landing page, then activate with `/billing action:activate api_key:<key>`."
		return Response{
			Content:      content,
			Ephemeral:    true,
			Presentation: Presentation{Title: "Panda Billing", Accent: AccentInfo},
			Actions:      []Action{{Label: "Open Panda pricing", URL: entitlement.UpgradeURL}},
		}
	}
	content += "\n\nBuy another pack on the Panda landing page, then activate it with `/billing action:activate api_key:<key>`."
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
		Content:      fmt.Sprintf("Panda %s is active for this server through %s.", result.Entitlement.Pack.DisplayName, result.Entitlement.PeriodEnd.Format("2006-01-02")),
		Ephemeral:    true,
		Presentation: Presentation{Title: "Pack activated", Accent: AccentSuccess},
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

func renderDataSummary(summary repository.GuildDataSummary) string {
	lines := []string{
		"Safe Panda data export summary:",
		fmt.Sprintf("- Guild ID: `%s`", summary.GuildID),
		fmt.Sprintf("- Knowledge: `%d document(s), %d chunk(s), %s`", summary.KnowledgeDocuments, summary.KnowledgeChunks, formatDataBytes(summary.KnowledgeStorageBytes)),
		fmt.Sprintf("- Conversations: `%d conversation(s), %d message metadata row(s), %d Discord event(s), %d attachment record(s)`", summary.Conversations, summary.Messages, summary.DiscordEvents, summary.Attachments),
		fmt.Sprintf("- Memory consent records: `%d`", summary.MemoryConsentRecords),
		fmt.Sprintf("- Billing: `%d account(s), %d historical billing record(s), %d payment event(s)`", summary.CustomerAccounts, summary.Subscriptions, summary.InvoicePaymentEvents),
		fmt.Sprintf("- Usage and cost: `%d historical usage period(s), %d historical reservation(s), %d cost ledger event(s)`", summary.UsagePeriods, summary.UsageReservations, summary.CostLedgerEvents),
		fmt.Sprintf("- Automations: `%d schedule(s), %d alert rule(s), %d composed tool(s), %d composed run(s)`", summary.Schedules, summary.AlertRules, summary.ComposedTools, summary.ComposedToolRuns),
		fmt.Sprintf("- Music: `%d queue item(s), %d playlist(s)`", summary.MusicQueueItems, summary.MusicPlaylists),
		"- Raw prompts/messages and provider diagnostics are not included in this Discord export.",
	}
	if summary.CurrentSubscriptionPlan != "" || summary.CurrentSubscriptionState != "" {
		lines = append(lines, fmt.Sprintf("- Historical account state: `%s / %s`", firstNonEmpty(summary.CurrentSubscriptionPlan, "none"), firstNonEmpty(summary.CurrentSubscriptionState, "none")))
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
	case "user", "member":
		return r.handleAdminUserProfile(ctx, request)
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
	case "quiet", "quiet-mode", "quiet_mode", "timeout":
		return r.handleAdminQuietMode(ctx, request)
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

func roleDisplay(_ string, roleName string) string {
	roleName = strings.TrimSpace(roleName)
	if roleName == "" {
		return "the selected role"
	}
	return discordDisplayLabel(roleName)
}

func (r *Router) roleDisplay(ctx context.Context, request Request, roleID, roleName string) string {
	if r != nil && r.tools != nil {
		return r.tools.DiscordRoleDisplay(ctx, request.GuildID, request.UserID, request.RequestID, roleID, roleName)
	}
	return roleDisplay(roleID, roleName)
}

func (r *Router) userDisplay(ctx context.Context, request Request, userID, username string) string {
	if r != nil && r.tools != nil {
		return r.tools.DiscordUserDisplay(ctx, request.GuildID, request.UserID, request.RequestID, userID, username)
	}
	return userMention(userID, username)
}

func (r *Router) channelDisplay(ctx context.Context, request Request, channelID, channelName string) string {
	if r != nil && r.tools != nil {
		return r.tools.DiscordChannelDisplay(ctx, request.GuildID, request.UserID, request.RequestID, channelID, channelName)
	}
	return channelDisplay(channelID, channelName)
}

func discordDisplayLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return "`" + strings.ReplaceAll(value, "`", "'") + "`"
}

func (r *Router) handleAdminRoleProfile(ctx context.Context, request Request) Response {
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "list")))
	switch action {
	case "list", "":
		roles, err := r.admin.ListRolePermissions(ctx, request.GuildID)
		if err != nil {
			return Response{Content: "Role profile lookup failed.", Ephemeral: true}
		}
		return Response{Content: r.renderRoleProfiles(ctx, request, roles), Ephemeral: true}
	case "set", "add":
		if denied := r.ensureGuildControl(ctx, request, "Only the Panda owner, server owner or administrator, or the current Panda admin role or user can set Panda role profiles."); denied.Content != "" {
			return denied
		}
		profile, roleID, roleName, response := roleProfileOptions(request)
		if response.Content != "" {
			return response
		}
		if _, err := r.admin.ApplyRoleProfile(ctx, request.GuildID, request.UserID, roleID, profile); err != nil {
			return Response{Content: "Role profile could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("%s is now a Panda %s role.", r.roleDisplay(ctx, request, roleID, roleName), admin.RoleProfileLabel(profile)), Ephemeral: true}
	case "remove", "unset":
		if denied := r.ensureGuildControl(ctx, request, "Only the Panda owner, server owner or administrator, or the current Panda admin role or user can remove Panda role profiles."); denied.Content != "" {
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
		return Response{Content: fmt.Sprintf("Removed the Panda %s profile from %s.", admin.RoleProfileLabel(profile), r.roleDisplay(ctx, request, roleID, roleName)), Ephemeral: true}
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

func (r *Router) renderRoleProfiles(ctx context.Context, request Request, roles []store.GuildRole) string {
	adminRoles := roleIDsForPermission(roles, admin.PermissionAdminBadge)
	moderatorRoles := roleIDsForPermission(roles, admin.PermissionModerationUse)
	var builder strings.Builder
	builder.WriteString("Panda role profiles:\n")
	builder.WriteString(r.roleProfileLine(ctx, request, "admin", adminRoles))
	builder.WriteString("\n")
	builder.WriteString(r.roleProfileLine(ctx, request, "moderator", moderatorRoles))
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

func (r *Router) roleProfileLine(ctx context.Context, request Request, profile string, roleIDs []string) string {
	if len(roleIDs) == 0 {
		return fmt.Sprintf("- %s: not configured", profile)
	}
	values := make([]string, 0, len(roleIDs))
	for _, roleID := range roleIDs {
		values = append(values, r.roleDisplay(ctx, request, roleID, ""))
	}
	return fmt.Sprintf("- %s: %s", profile, strings.Join(values, ", "))
}

func (r *Router) handleAdminUserProfile(ctx context.Context, request Request) Response {
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "list")))
	switch action {
	case "list", "":
		users, err := r.admin.ListUserPermissions(ctx, request.GuildID)
		if err != nil {
			return Response{Content: "User profile lookup failed.", Ephemeral: true}
		}
		return Response{Content: r.renderUserProfiles(ctx, request, users), Ephemeral: true}
	case "set", "add":
		if denied := r.ensureGuildControl(ctx, request, "Only the Panda owner, server owner or administrator, or the current Panda admin role or user can set Panda user profiles."); denied.Content != "" {
			return denied
		}
		profile, userID, username, response := userProfileOptions(request)
		if response.Content != "" {
			return response
		}
		if _, err := r.admin.ApplyUserProfile(ctx, request.GuildID, request.UserID, userID, profile); err != nil {
			return Response{Content: "User profile could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("%s is now a Panda %s user.", r.userDisplay(ctx, request, userID, username), admin.RoleProfileLabel(profile)), Ephemeral: true}
	case "remove", "unset":
		if denied := r.ensureGuildControl(ctx, request, "Only the Panda owner, server owner or administrator, or the current Panda admin role or user can remove Panda user profiles."); denied.Content != "" {
			return denied
		}
		profile, userID, username, response := userProfileOptions(request)
		if response.Content != "" {
			return response
		}
		if err := r.admin.RemoveUserProfile(ctx, request.GuildID, request.UserID, userID, profile); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return Response{Content: "That user profile was not configured for this user.", Ephemeral: true}
			}
			return Response{Content: "User profile could not be removed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed the Panda %s profile from %s.", admin.RoleProfileLabel(profile), r.userDisplay(ctx, request, userID, username)), Ephemeral: true}
	default:
		return Response{Content: "`action` must be `list`, `set`, or `remove`.", Ephemeral: true}
	}
}

func userProfileOptions(request Request) (string, string, string, Response) {
	profile, ok := admin.NormalizeRoleProfile(request.Options["profile"])
	if !ok {
		return "", "", "", Response{Content: "`profile` must be `admin` or `moderator`.", Ephemeral: true}
	}
	userID := normalizeDiscordUserID(firstNonEmpty(request.Options["member_user_id"], firstNonEmpty(request.Options["user_id"], firstNonEmpty(request.Options["member"], request.Options["user"]))))
	if userID == "" {
		return "", "", "", Response{Content: "Choose a Discord user.", Ephemeral: true}
	}
	return profile, userID, firstNonEmpty(request.Options["member_user_name"], request.Options["user_name"]), Response{}
}

func (r *Router) renderUserProfiles(ctx context.Context, request Request, users []store.GuildUserPermission) string {
	adminUsers := userIDsForPermission(users, admin.PermissionAdminBadge)
	moderatorUsers := userIDsForPermission(users, admin.PermissionModerationUse)
	var builder strings.Builder
	builder.WriteString("Panda user profiles:\n")
	builder.WriteString(r.userProfileLine(ctx, request, "admin", adminUsers))
	builder.WriteString("\n")
	builder.WriteString(r.userProfileLine(ctx, request, "moderator", moderatorUsers))
	builder.WriteString("\n\nModerator users include `assistant.use` and `moderation.use`.")
	return builder.String()
}

func userIDsForPermission(users []store.GuildUserPermission, permission string) []string {
	seen := map[string]struct{}{}
	var ids []string
	for _, user := range users {
		if user.Permission != permission {
			continue
		}
		if _, ok := seen[user.UserID]; ok {
			continue
		}
		seen[user.UserID] = struct{}{}
		ids = append(ids, user.UserID)
	}
	sort.Strings(ids)
	return ids
}

func (r *Router) userProfileLine(ctx context.Context, request Request, profile string, userIDs []string) string {
	if len(userIDs) == 0 {
		return fmt.Sprintf("- %s: not configured", profile)
	}
	values := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		values = append(values, r.userDisplay(ctx, request, userID, ""))
	}
	return fmt.Sprintf("- %s: %s", profile, strings.Join(values, ", "))
}

func (r *Router) handleAdminMemberRole(ctx context.Context, request Request) Response {
	if denied := r.ensureGuildControl(ctx, request, "Only the Panda owner, server owner or administrator, or the current Panda admin role or user can assign Discord roles."); denied.Content != "" {
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
		return Response{Content: fmt.Sprintf("Assigned %s to %s.", r.roleDisplay(ctx, request, memberRequest.RoleID, request.Options["role_name"]), r.userDisplay(ctx, request, memberRequest.UserID, request.Options["member_user_name"])), Ephemeral: true}
	case "remove", "unassign", "unset":
		memberRequest.Reason = "Panda admin member-role remove"
		if err := r.memberRoles.RemoveMemberRole(ctx, memberRequest); err != nil {
			return Response{Content: "Discord role could not be removed. Check Panda's Manage Roles permission and role hierarchy.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed %s from %s.", r.roleDisplay(ctx, request, memberRequest.RoleID, request.Options["role_name"]), r.userDisplay(ctx, request, memberRequest.UserID, request.Options["member_user_name"])), Ephemeral: true}
	default:
		return Response{Content: "`action` must be `add` or `remove`.", Ephemeral: true}
	}
}

func memberRoleOptions(request Request) (MemberRoleRequest, Response) {
	userID := normalizeDiscordUserID(firstNonEmpty(request.Options["member_user_id"], firstNonEmpty(request.Options["user_id"], request.Options["user"])))
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
	username = strings.TrimSpace(username)
	if username == "" {
		return "the selected user"
	}
	return discordDisplayLabel(username)
}

func normalizeDiscordUserID(value string) string {
	value = strings.TrimSpace(value)
	trimmed := strings.TrimPrefix(value, "<@")
	trimmed = strings.TrimPrefix(trimmed, "!")
	trimmed = strings.TrimSuffix(trimmed, ">")
	if trimmed == "" {
		return ""
	}
	for _, char := range trimmed {
		if char < '0' || char > '9' {
			return value
		}
	}
	return trimmed
}

func (r *Router) handleAdminToolAccess(ctx context.Context, request Request) Response {
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "list")))
	toolName := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["tool_name"], request.Options["tool"])))
	roleID := strings.TrimSpace(firstNonEmpty(request.Options["role_id"], request.Options["role"]))
	userID := normalizeDiscordUserID(firstNonEmpty(request.Options["member_user_id"], firstNonEmpty(request.Options["user_id"], firstNonEmpty(request.Options["member"], request.Options["user"]))))
	targetType := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["target_type"], request.Options["subject_type"])))
	if targetType == "member" {
		targetType = "user"
	}
	switch action {
	case "list", "":
		rules, err := r.admin.ListToolAccess(ctx, request.GuildID)
		if err != nil {
			return Response{Content: "Tool access lookup failed.", Ephemeral: true}
		}
		if len(rules) == 0 {
			return Response{Content: "No user- or role-specific tool access rules are configured. Native tools use their normal permission policy; composed tools are admin-only until a role or user is allowed.", Ephemeral: true}
		}
		lines := []string{"Tool access rules:"}
		for _, rule := range rules {
			lines = append(lines, fmt.Sprintf("- `%s` %s -> %s", rule.ToolName, rule.Rule, r.toolAccessRuleTargetDisplay(ctx, request, rule)))
		}
		return Response{Content: strings.Join(lines, "\n"), Ephemeral: true}
	case "add", "allow":
		if toolName == "" {
			return Response{Content: "Provide `tool_name` and a `role` or `user` to allow tool access.", Ephemeral: true}
		}
		target, denied := adminToolTarget(roleID, userID, targetType)
		if denied.Content != "" {
			return denied
		}
		if target.kind == "user" {
			toolUser, err := r.admin.AddToolUser(ctx, request.GuildID, request.UserID, toolName, target.id)
			if err != nil {
				return toolAccessWriteError(err)
			}
			if toolNameIsPandaChat(toolUser.ToolName) {
				return Response{Content: fmt.Sprintf("Panda can reply to %s.", r.userDisplay(ctx, request, toolUser.UserID, firstNonEmpty(request.Options["member_user_name"], request.Options["user_name"]))), Ephemeral: true}
			}
			return Response{Content: fmt.Sprintf("Allowed %s to use `%s`.", r.userDisplay(ctx, request, toolUser.UserID, firstNonEmpty(request.Options["member_user_name"], request.Options["user_name"])), toolUser.ToolName), Ephemeral: true}
		}
		toolRole, err := r.admin.AddToolRole(ctx, request.GuildID, request.UserID, toolName, target.id)
		if err != nil {
			return toolAccessWriteError(err)
		}
		if toolNameIsPandaChat(toolRole.ToolName) {
			return Response{Content: fmt.Sprintf("Panda can reply to %s.", r.roleDisplay(ctx, request, toolRole.RoleID, request.Options["role_name"])), Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Allowed %s to use `%s`.", r.roleDisplay(ctx, request, toolRole.RoleID, request.Options["role_name"]), toolRole.ToolName), Ephemeral: true}
	case "deny", "block", "disallow", "disable":
		if toolName == "" {
			return Response{Content: "Provide `tool_name` and a `role` or `user` to deny tool access.", Ephemeral: true}
		}
		target, denied := adminToolTarget(roleID, userID, targetType)
		if denied.Content != "" {
			return denied
		}
		if target.kind == "user" {
			toolUser, err := r.admin.DenyToolUser(ctx, request.GuildID, request.UserID, toolName, target.id)
			if err != nil {
				return toolAccessWriteError(err)
			}
			if toolNameIsPandaChat(toolUser.ToolName) {
				return Response{Content: fmt.Sprintf("Panda will not reply to %s.", r.userDisplay(ctx, request, toolUser.UserID, firstNonEmpty(request.Options["member_user_name"], request.Options["user_name"]))), Ephemeral: true}
			}
			return Response{Content: fmt.Sprintf("Denied %s from `%s`.", r.userDisplay(ctx, request, toolUser.UserID, firstNonEmpty(request.Options["member_user_name"], request.Options["user_name"])), toolUser.ToolName), Ephemeral: true}
		}
		toolRole, err := r.admin.DenyToolRole(ctx, request.GuildID, request.UserID, toolName, target.id)
		if err != nil {
			return toolAccessWriteError(err)
		}
		if toolNameIsPandaChat(toolRole.ToolName) {
			return Response{Content: fmt.Sprintf("Panda will not reply to %s.", r.roleDisplay(ctx, request, toolRole.RoleID, request.Options["role_name"])), Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Denied %s from `%s`.", r.roleDisplay(ctx, request, toolRole.RoleID, request.Options["role_name"]), toolRole.ToolName), Ephemeral: true}
	case "remove":
		if toolName == "" {
			return Response{Content: "Provide `tool_name` and a `role` or `user` to remove tool access.", Ephemeral: true}
		}
		target, denied := adminToolTarget(roleID, userID, targetType)
		if denied.Content != "" {
			return denied
		}
		if target.kind == "user" {
			if err := r.admin.RemoveToolUser(ctx, request.GuildID, request.UserID, toolName, target.id); err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return Response{Content: "That tool access rule was not found.", Ephemeral: true}
				}
				return Response{Content: "Tool access could not be removed.", Ephemeral: true}
			}
			if toolNameIsPandaChat(toolName) {
				return Response{Content: fmt.Sprintf("Panda can reply to %s again.", r.userDisplay(ctx, request, target.id, firstNonEmpty(request.Options["member_user_name"], request.Options["user_name"]))), Ephemeral: true}
			}
			return Response{Content: fmt.Sprintf("Removed %s from `%s`.", r.userDisplay(ctx, request, target.id, firstNonEmpty(request.Options["member_user_name"], request.Options["user_name"])), toolName), Ephemeral: true}
		}
		if err := r.admin.RemoveToolRole(ctx, request.GuildID, request.UserID, toolName, target.id); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return Response{Content: "That tool access rule was not found.", Ephemeral: true}
			}
			return Response{Content: "Tool access could not be removed.", Ephemeral: true}
		}
		if toolNameIsPandaChat(toolName) {
			return Response{Content: fmt.Sprintf("Panda can reply to %s again.", r.roleDisplay(ctx, request, target.id, request.Options["role_name"])), Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed %s from `%s`.", r.roleDisplay(ctx, request, target.id, request.Options["role_name"]), toolName), Ephemeral: true}
	case "open", "public", "everyone", "allow_everyone":
		toolNames, permissions, err := r.adminToolAccessOpenTargets(toolName, firstNonEmpty(request.Options["tool_group"], request.Options["group"]))
		if err != nil {
			return Response{Content: err.Error(), Ephemeral: true}
		}
		var removedPermissionRules int64
		var removedToolRules int64
		for _, permission := range permissions {
			result, err := r.admin.ClearPermissionAccess(ctx, request.GuildID, request.UserID, permission)
			if err != nil {
				return Response{Content: "Permission access could not be opened.", Ephemeral: true}
			}
			removedPermissionRules += result.RemovedRoleRules + result.RemovedUserRules
		}
		for _, toolName := range toolNames {
			result, err := r.admin.ClearToolAccess(ctx, request.GuildID, request.UserID, toolName)
			if err != nil {
				return Response{Content: "Tool access could not be opened.", Ephemeral: true}
			}
			removedToolRules += result.RemovedRoleRules + result.RemovedUserRules
		}
		return Response{Content: fmt.Sprintf("Opened `%s` to everyone. Cleared %d permission rule(s) and %d tool access rule(s).", strings.Join(toolNames, "`, `"), removedPermissionRules, removedToolRules), Ephemeral: true}
	default:
		return Response{Content: "`action` must be `list`, `add`, `deny`, `remove`, or `open`.", Ephemeral: true}
	}
}

type adminToolAccessTarget struct {
	kind string
	id   string
}

func adminToolTarget(roleID, userID, targetType string) (adminToolAccessTarget, Response) {
	roleID = strings.TrimSpace(roleID)
	userID = strings.TrimSpace(userID)
	switch targetType {
	case "role":
		if roleID == "" {
			return adminToolAccessTarget{}, Response{Content: "Provide `role` for role-specific tool access.", Ephemeral: true}
		}
		return adminToolAccessTarget{kind: "role", id: roleID}, Response{}
	case "user":
		if userID == "" {
			return adminToolAccessTarget{}, Response{Content: "Provide `user` for user-specific tool access.", Ephemeral: true}
		}
		return adminToolAccessTarget{kind: "user", id: userID}, Response{}
	case "":
		if roleID != "" && userID != "" {
			return adminToolAccessTarget{}, Response{Content: "Provide either `role` or `user` for tool access, not both.", Ephemeral: true}
		}
		if userID != "" {
			return adminToolAccessTarget{kind: "user", id: userID}, Response{}
		}
		if roleID != "" {
			return adminToolAccessTarget{kind: "role", id: roleID}, Response{}
		}
		return adminToolAccessTarget{}, Response{Content: "Provide a `role` or `user` for tool access.", Ephemeral: true}
	default:
		return adminToolAccessTarget{}, Response{Content: "`target_type` must be `role` or `user`.", Ephemeral: true}
	}
}

func splitConfirmationCSV(value string) []string {
	fields := strings.Split(value, ",")
	result := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		result = append(result, field)
	}
	return result
}

func (r *Router) adminToolAccessOpenTargets(toolName, group string) ([]string, []string, error) {
	if r.tools == nil {
		return nil, nil, fmt.Errorf("Tool access resolver is not configured.")
	}
	return r.tools.ToolAccessOpenTargets(toolName, group)
}

func toolAccessWriteError(err error) Response {
	if errors.Is(err, admin.ErrToolAccessEveryoneRole) {
		return Response{Content: err.Error(), Ephemeral: true}
	}
	return Response{Content: "Tool access could not be saved.", Ephemeral: true}
}

func toolNameIsPandaChat(toolName string) bool {
	return strings.EqualFold(strings.TrimSpace(toolName), toolsvc.ToolNamePandaChat)
}

func (r *Router) toolAccessRuleTargetDisplay(ctx context.Context, request Request, rule admin.ToolAccessRule) string {
	switch strings.ToLower(strings.TrimSpace(rule.SubjectType)) {
	case "user", "member":
		return r.userDisplay(ctx, request, rule.SubjectID, "")
	case "role":
		return r.roleDisplay(ctx, request, rule.SubjectID, "")
	default:
		return "the selected target"
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
		return Response{Content: r.renderChannelRules(ctx, request, rules), Ephemeral: true}
	case "allow", "add":
		if channelID == "" {
			return Response{Content: "Choose a `channel` to allow Panda assistant use there.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("Panda assistant use would be allowed in %s.", r.channelDisplay(ctx, request, channelID, channelName))
		}
		rule, err := r.admin.SetChannelRule(ctx, request.GuildID, request.UserID, channelID, "allow")
		if err != nil {
			return Response{Content: "Channel access rule could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Allowed Panda assistant use in %s. Because an allow rule exists, other channels need their own allow rule unless the user is an admin.", r.channelDisplay(ctx, request, rule.ChannelID, channelName)), Ephemeral: true}
	case "deny", "block":
		if channelID == "" {
			return Response{Content: "Choose a `channel` to deny Panda assistant use there.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("Panda assistant use would be denied in %s.", r.channelDisplay(ctx, request, channelID, channelName))
		}
		rule, err := r.admin.SetChannelRule(ctx, request.GuildID, request.UserID, channelID, "deny")
		if err != nil {
			return Response{Content: "Channel access rule could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Denied Panda assistant use in %s.", r.channelDisplay(ctx, request, rule.ChannelID, channelName)), Ephemeral: true}
	case "remove", "clear":
		if channelID == "" {
			return Response{Content: "Choose a `channel` to remove from Panda channel access rules.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("The Panda channel access rule for %s would be removed.", r.channelDisplay(ctx, request, channelID, channelName))
		}
		if err := r.admin.RemoveChannelRule(ctx, request.GuildID, request.UserID, channelID); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return Response{Content: "That channel access rule was not found.", Ephemeral: true}
			}
			return Response{Content: "Channel access rule could not be removed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed Panda channel access rule for %s.", r.channelDisplay(ctx, request, channelID, channelName)), Ephemeral: true}
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
		return discordDisplayLabel("#" + channelName)
	}
	return "the selected channel"
}

func (r *Router) renderChannelRules(ctx context.Context, request Request, rules []store.GuildChannelRule) string {
	if len(rules) == 0 {
		return "No channel access rules are configured. Panda assistant use is available in every channel where Discord permissions allow it."
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
		lines = append(lines, fmt.Sprintf("- `%s` %s", rule.Rule, r.channelDisplay(ctx, request, rule.ChannelID, "")))
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

func (r *Router) handleAdminQuietMode(ctx context.Context, request Request) Response {
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "status")))
	now := time.Now().UTC()
	switch action {
	case "status", "get", "show", "":
		status, err := r.admin.QuietModeStatus(ctx, request.GuildID, now)
		if err != nil {
			return Response{Content: "Quiet mode status lookup failed.", Ephemeral: true}
		}
		return Response{Content: renderQuietModeStatus(status), Ephemeral: true}
	case "set", "enable", "start", "timeout", "pause":
		duration, response := adminQuietModeDuration(request.Options)
		if response.Content != "" {
			return response
		}
		until := now.Add(duration)
		if dryRunRequested(request) {
			return dryRunResponse("quiet mode would be active for %s, until `%s`.", durationSecondsDisplayLabel(int64(duration/time.Second)), until.Format(time.RFC3339))
		}
		status, err := r.admin.SetQuietModeUntil(ctx, request.GuildID, request.UserID, until, now)
		if err != nil {
			return Response{Content: "Quiet mode could not be started.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Panda quiet mode is active for %s, until `%s`.", durationSecondsDisplayLabel(int64(status.Remaining/time.Second)), quietModeUntilLabel(status)), Ephemeral: true, Presentation: Presentation{Title: "Quiet mode started", Accent: AccentWarning}}
	case "clear", "disable", "stop", "resume", "cancel":
		if dryRunRequested(request) {
			return dryRunResponse("quiet mode would be cleared.")
		}
		if _, err := r.admin.ClearQuietMode(ctx, request.GuildID, request.UserID, now); err != nil {
			return Response{Content: "Quiet mode could not be cleared.", Ephemeral: true}
		}
		return Response{Content: "Panda quiet mode is cleared.", Ephemeral: true, Presentation: Presentation{Title: "Quiet mode cleared", Accent: AccentSuccess}}
	default:
		return Response{Content: "`action` must be `status`, `set`, or `clear`.", Ephemeral: true}
	}
}

func adminQuietModeDuration(options map[string]string) (time.Duration, Response) {
	if seconds, err := positiveInt64Option(options["duration_seconds"]); err == nil {
		return time.Duration(seconds) * time.Second, Response{}
	}
	raw := strings.TrimSpace(firstNonEmpty(options["duration"], options["for"]))
	if raw == "" {
		return 0, Response{Content: "Provide `duration_seconds` or `duration` for quiet mode.", Ephemeral: true}
	}
	duration, err := time.ParseDuration(raw)
	if err != nil || duration <= 0 {
		return 0, Response{Content: "`duration` must be a positive Go duration like `30m` or `2h`.", Ephemeral: true}
	}
	return duration.Round(time.Second), Response{}
}

func renderQuietModeStatus(status admin.QuietModeStatus) string {
	if !status.Active {
		return "Panda quiet mode is not active."
	}
	return fmt.Sprintf("Panda quiet mode is active for %s, until `%s`.", durationSecondsDisplayLabel(int64(status.Remaining/time.Second)), quietModeUntilLabel(status))
}

func quietModeUntilLabel(status admin.QuietModeStatus) string {
	if status.Until == nil {
		return "the configured timeout"
	}
	return status.Until.UTC().Format(time.RFC3339)
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
		BypassSafety:                 r.safetyBypassAllowed(ctx, request),
		AllowedPermissions:           r.allowedToolPermissions(ctx, request),
		AllowedTools:                 toolFilter.allowed,
		DeniedTools:                  toolFilter.denied,
		RestrictedTools:              toolFilter.restricted,
		EnabledFeatures:              enabledFeatures,
		ImageReferences:              generated.CloneImageReferences(request.ImageReferences),
		FeatureGateActive:            featureGateActive,
		RequireExplicitComposedTools: toolFilter.requireExplicitComposed,
	})
	if err != nil {
		r.releaseAIUsage(ctx, reservation)
		return assistantError(err)
	}
	if answer.Silent {
		r.releaseAIUsage(ctx, reservation)
		return Response{}
	}
	if !assistantAnswerHasPayload(answer) {
		r.releaseAIUsage(ctx, reservation)
		return Response{Content: "Panda returned an empty response. Please try again.", Ephemeral: true}
	}
	r.commitAIUsage(ctx, reservation, answer.Usage)
	return r.responseFromAssistantAnswer(ctx, request, answer, "", "")
}

func (r *Router) handleChat(ctx context.Context, request Request) Response {
	return r.handleChatMode(ctx, request, !truthyOption(request.Options[selectionRequestOption]))
}

func (r *Router) handleChatMode(ctx context.Context, request Request, threaded bool) Response {
	return r.handleChatModeWithAccess(ctx, request, threaded, false)
}

type chatModeOptions struct {
	threaded        bool
	allowSoulWriter bool
	naturalMessage  bool
	onRespond       func()
	onToolStart     func(string)
	onToolProgress  func(string, string)
}

func (r *Router) handleChatModeWithAccess(ctx context.Context, request Request, threaded bool, allowSoulWriter bool) Response {
	return r.handleChatModeWithOptions(ctx, request, chatModeOptions{threaded: threaded, allowSoulWriter: allowSoulWriter})
}

func (r *Router) handleChatModeWithOptions(ctx context.Context, request Request, options chatModeOptions) Response {
	response, err := r.handleChatModeWithOptionsResult(ctx, request, options)
	if err != nil {
		return assistantError(err)
	}
	return response
}

func (r *Router) handleChatModeWithOptionsResult(ctx context.Context, request Request, options chatModeOptions) (Response, error) {
	question := strings.TrimSpace(request.Options["question"])
	if question == "" {
		return Response{Content: "Please include a message.", Ephemeral: true}, nil
	}
	if denied := r.ensureAssistantAllowed(ctx, request); denied.Content != "" {
		if !options.allowSoulWriter || !r.canWriteSoul(ctx, request) {
			return denied, nil
		}
	}
	if options.threaded && r.threads != nil && request.GuildID != "" {
		if denied := r.ensureThreadsAllowed(ctx, request); denied.Content != "" {
			return denied, nil
		}
	}
	if limited := r.allowUser(request.UserID); limited.Content != "" {
		return limited, nil
	}
	if denied := r.ensureBudgetAvailable(ctx, request); denied.Content != "" {
		return denied, nil
	}
	reservation, denied := r.beginAIUsage(ctx, request)
	if denied.Content != "" {
		return denied, nil
	}

	chatChannelID := request.ChannelID
	threadID := ""
	threadName := ""
	if options.threaded && r.threads != nil && request.GuildID != "" {
		thread, err := r.threads.EnsureChatThread(ctx, ThreadRequest{
			GuildID:   request.GuildID,
			ChannelID: request.ChannelID,
			UserID:    request.UserID,
			Title:     chatThreadTitle(question),
		})
		if err != nil {
			r.releaseAIUsage(ctx, reservation)
			return Response{Content: "I could not create a chat thread here. Please check my thread permissions.", Ephemeral: true}, nil
		}
		chatChannelID = thread.ID
		threadID = thread.ID
		threadName = thread.Name
	}

	toolFilter := r.toolFilter(ctx, request)
	enabledFeatures, featureGateActive := r.featureSetForAccess(ctx, request.GuildID)
	allowedPermissions := r.allowedToolPermissions(ctx, request)
	invocationContext := r.invocationContext(ctx, request)
	askRequest := assistant.AskRequest{
		RequestID:                    request.RequestID,
		GuildID:                      request.GuildID,
		UserID:                       request.UserID,
		ChannelID:                    chatChannelID,
		VoiceChannelID:               request.VoiceChannelID,
		ThreadID:                     threadID,
		Question:                     question,
		InvocationContext:            invocationContext,
		ReplyContent:                 request.Options["reply_text"],
		ReplyMessageID:               request.Options["reply_message_id"],
		ReplyAuthorIsBot:             truthyOption(request.Options["reply_author_is_bot"]),
		ReplyAuthorIsCurrentUser:     truthyOption(request.Options["reply_author_is_current_user"]),
		BotMentioned:                 truthyOption(request.Options["bot_mentioned"]),
		RoleIDs:                      request.RoleIDs,
		IsGuildAdmin:                 request.IsGuildAdmin,
		IsOwner:                      request.IsOwner,
		BypassSafety:                 r.safetyBypassAllowed(ctx, request),
		AllowedPermissions:           allowedPermissions,
		AllowedTools:                 toolFilter.allowed,
		DeniedTools:                  toolFilter.denied,
		RestrictedTools:              toolFilter.restricted,
		EnabledFeatures:              enabledFeatures,
		ImageReferences:              generated.CloneImageReferences(request.ImageReferences),
		FeatureGateActive:            featureGateActive,
		RequireExplicitComposedTools: toolFilter.requireExplicitComposed,
	}
	if options.naturalMessage || len(request.ImageReferences) > 0 {
		_, imagePermissionAllowed := allowedPermissions[admin.PermissionAssistantImageGeneration]
		slog.Info("assistant chat request prepared",
			slog.String("guild_id", request.GuildID),
			slog.String("channel_id", chatChannelID),
			slog.String("request_id", request.RequestID),
			slog.String("user_id", request.UserID),
			slog.Bool("natural_message", options.naturalMessage),
			slog.String("reply_message_id", request.Options["reply_message_id"]),
			slog.Int("image_ref_count", len(request.ImageReferences)),
			slog.Any("image_ref_ids", commandImageReferenceIDs(request.ImageReferences)),
			slog.Bool("feature_gate_active", featureGateActive),
			slog.Bool("image_feature_enabled", features.Has(enabledFeatures, features.ImageGeneration)),
			slog.Bool("image_permission_allowed", imagePermissionAllowed),
			slog.Any("allowed_permissions", permissionNames(allowedPermissions)),
			slog.Any("enabled_features", permissionNames(enabledFeatures)),
			slog.Any("allowed_tools", permissionNames(toolFilter.allowed)),
			slog.Any("denied_tools", permissionNames(toolFilter.denied)),
			slog.Any("restricted_tools", permissionNames(toolFilter.restricted)),
		)
	}
	var answer assistant.AskResponse
	var err error
	if options.naturalMessage {
		answer, err = r.assistant.ChatNaturalMessageWithToolStart(ctx, askRequest, options.onRespond, options.onToolStart, options.onToolProgress)
	} else {
		answer, err = r.assistant.Chat(ctx, askRequest)
	}
	if err != nil {
		r.releaseAIUsage(ctx, reservation)
		return assistantError(err), nil
	}
	if answer.Silent {
		if options.naturalMessage {
			r.commitRoutingUsage(ctx, request, reservation)
		} else {
			r.releaseAIUsage(ctx, reservation)
		}
		return Response{}, nil
	}
	if !assistantAnswerHasPayload(answer) {
		r.releaseAIUsage(ctx, reservation)
		return Response{Content: "Panda returned an empty response. Please try again.", Ephemeral: true}, nil
	}
	r.commitAIUsage(ctx, reservation, answer.Usage)
	return r.responseFromAssistantAnswer(ctx, request, answer, threadID, threadName), nil
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
		BypassSafety:                 r.safetyBypassAllowed(ctx, request),
		AllowedPermissions:           permissionNames(r.allowedToolPermissions(ctx, request)),
		AllowedTools:                 permissionNames(toolFilter.allowed),
		DeniedTools:                  permissionNames(toolFilter.denied),
		RestrictedTools:              permissionNames(toolFilter.restricted),
		EnabledFeatures:              permissionNames(enabledFeatures),
		ImageReferences:              generated.CloneImageReferences(request.ImageReferences),
		FeatureGateActive:            featureGateActive,
		RequireExplicitComposedTools: toolFilter.requireExplicitComposed,
	}
	if shouldBackgroundTask(request, input) {
		safety, err := r.assistant.CheckTaskSafety(ctx, assistantTaskRequestFromBackgroundTask(task, enabledFeatures, featureGateActive))
		if err != nil {
			return assistantError(err)
		}
		if safety.Silent {
			return Response{}
		}
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
		RequestID:    task.RequestID,
		Command:      task.Command,
		GuildID:      task.GuildID,
		ChannelID:    task.ChannelID,
		UserID:       task.UserID,
		RoleIDs:      append([]string(nil), task.RoleIDs...),
		IsGuildAdmin: task.IsGuildAdmin,
		IsOwner:      task.IsOwner,
	}
	if r.quietModeActive(ctx, request) {
		return Response{}
	}
	if response := r.maintenanceResponse(ctx); response.Content != "" {
		return response
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
	answer, err := r.assistant.CompleteTask(ctx, assistantTaskRequestFromBackgroundTask(task, enabledFeatures, featureGateActive))
	if err != nil {
		r.releaseAIUsage(ctx, reservation)
		return assistantError(err)
	}
	if answer.Silent {
		r.releaseAIUsage(ctx, reservation)
		return Response{}
	}
	if !assistantAnswerHasPayload(answer) {
		r.releaseAIUsage(ctx, reservation)
		return Response{Content: "Panda returned an empty response. Please try again.", Ephemeral: true}
	}
	r.commitAIUsage(ctx, reservation, answer.Usage)
	return r.responseFromAssistantAnswer(ctx, Request{
		RequestID: task.RequestID,
		Command:   task.Command,
		GuildID:   task.GuildID,
		ChannelID: task.ChannelID,
		UserID:    task.UserID,
	}, answer, "", "")
}

func assistantTaskRequestFromBackgroundTask(task BackgroundTask, enabledFeatures map[string]struct{}, featureGateActive bool) assistant.TaskRequest {
	return assistant.TaskRequest{
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
		BypassSafety:                 task.BypassSafety,
		AllowedPermissions:           permissionsFromNames(task.AllowedPermissions),
		AllowedTools:                 permissionsFromNames(task.AllowedTools),
		DeniedTools:                  permissionsFromNames(task.DeniedTools),
		RestrictedTools:              permissionsFromNames(task.RestrictedTools),
		EnabledFeatures:              enabledFeatures,
		ImageReferences:              generated.CloneImageReferences(task.ImageReferences),
		FeatureGateActive:            featureGateActive,
		RequireExplicitComposedTools: task.RequireExplicitComposedTools,
	}
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
		return Response{Content: fmt.Sprintf("Granted `%s` to %s.", permission, r.roleDisplay(ctx, request.Request, roleID, request.Options["role_display"])), Ephemeral: true}
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
		return Response{Content: fmt.Sprintf("Removed `%s` from %s.", permission, r.roleDisplay(ctx, request.Request, roleID, request.Options["role_display"])), Ephemeral: true}
	case toolActionRoleProfileAdd:
		if denied := r.ensureGuildControl(ctx, request.Request, "Only the Panda owner, server owner or administrator, or the current Panda admin role or user can set Panda role profiles."); denied.Content != "" {
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
		return Response{Content: fmt.Sprintf("%s is now a Panda %s role.", r.roleDisplay(ctx, request.Request, roleID, request.Options["role_display"]), admin.RoleProfileLabel(profile)), Ephemeral: true}
	case toolActionRoleProfileRemove:
		if denied := r.ensureGuildControl(ctx, request.Request, "Only the Panda owner, server owner or administrator, or the current Panda admin role or user can remove Panda role profiles."); denied.Content != "" {
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
		return Response{Content: fmt.Sprintf("Removed the Panda %s profile from %s.", admin.RoleProfileLabel(profile), r.roleDisplay(ctx, request.Request, roleID, request.Options["role_display"])), Ephemeral: true}
	case toolActionUserPermissionAdd:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage user permissions."); denied.Content != "" {
			return denied
		}
		userID := normalizeDiscordUserID(request.Options["user_id"])
		permission := strings.TrimSpace(request.Options["permission"])
		if userID == "" || !admin.IsUserPermissionNameAllowed(permission) {
			return Response{Content: "That user-permission confirmation is invalid.", Ephemeral: true}
		}
		if _, err := r.admin.AddUserPermission(ctx, request.Request.GuildID, request.Request.UserID, userID, permission); err != nil {
			return Response{Content: "User permission could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Granted `%s` to %s.", permission, r.userDisplay(ctx, request.Request, userID, request.Options["user_display"])), Ephemeral: true}
	case toolActionUserPermissionRemove:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage user permissions."); denied.Content != "" {
			return denied
		}
		userID := normalizeDiscordUserID(request.Options["user_id"])
		permission := strings.TrimSpace(request.Options["permission"])
		if userID == "" || !admin.IsUserPermissionNameAllowed(permission) {
			return Response{Content: "That user-permission confirmation is invalid.", Ephemeral: true}
		}
		if err := r.admin.RemoveUserPermission(ctx, request.Request.GuildID, request.Request.UserID, userID, permission); err != nil {
			return toolConfirmationError(err, "User permission could not be removed.", "That user permission was not found.")
		}
		return Response{Content: fmt.Sprintf("Removed `%s` from %s.", permission, r.userDisplay(ctx, request.Request, userID, request.Options["user_display"])), Ephemeral: true}
	case toolActionUserProfileAdd:
		if denied := r.ensureGuildControl(ctx, request.Request, "Only the Panda owner, server owner or administrator, or the current Panda admin role or user can set Panda user profiles."); denied.Content != "" {
			return denied
		}
		userID := normalizeDiscordUserID(request.Options["user_id"])
		profile, ok := admin.NormalizeRoleProfile(request.Options["profile"])
		if userID == "" || !ok {
			return Response{Content: "That user-profile confirmation is invalid.", Ephemeral: true}
		}
		if _, err := r.admin.ApplyUserProfile(ctx, request.Request.GuildID, request.Request.UserID, userID, profile); err != nil {
			return Response{Content: "User profile could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("%s is now a Panda %s user.", r.userDisplay(ctx, request.Request, userID, request.Options["user_display"]), admin.RoleProfileLabel(profile)), Ephemeral: true}
	case toolActionUserProfileRemove:
		if denied := r.ensureGuildControl(ctx, request.Request, "Only the Panda owner, server owner or administrator, or the current Panda admin role or user can remove Panda user profiles."); denied.Content != "" {
			return denied
		}
		userID := normalizeDiscordUserID(request.Options["user_id"])
		profile, ok := admin.NormalizeRoleProfile(request.Options["profile"])
		if userID == "" || !ok {
			return Response{Content: "That user-profile confirmation is invalid.", Ephemeral: true}
		}
		if err := r.admin.RemoveUserProfile(ctx, request.Request.GuildID, request.Request.UserID, userID, profile); err != nil {
			return toolConfirmationError(err, "User profile could not be removed.", "That user profile was not configured for this user.")
		}
		return Response{Content: fmt.Sprintf("Removed the Panda %s profile from %s.", admin.RoleProfileLabel(profile), r.userDisplay(ctx, request.Request, userID, request.Options["user_display"])), Ephemeral: true}
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
		return Response{Content: fmt.Sprintf("Created Discord role `%s`.", role.Name), Ephemeral: true}
	case toolActionDiscordPollCreate:
		return r.handleDiscordPollConfirmation(ctx, request)
	case toolActionDiscordWriteExecute:
		return r.handleDiscordWriteConfirmation(ctx, request)
	case toolActionServerSetupApply:
		return r.handleServerSetupApplyConfirmation(ctx, request)
	case toolActionServerSetupRollback:
		return r.handleServerSetupRollbackConfirmation(ctx, request)
	case toolActionMemberRoleAdd, toolActionMemberRoleRemove:
		if denied := r.ensureGuildControl(ctx, request.Request, "Only the Panda owner, server owner or administrator, or the current Panda admin role or user can assign Discord roles."); denied.Content != "" {
			return denied
		}
		if r.memberRoles == nil {
			return Response{Content: "Discord role assignment is not configured for this runtime.", Ephemeral: true}
		}
		memberRequest := MemberRoleRequest{
			GuildID: request.Request.GuildID,
			UserID:  normalizeDiscordUserID(request.Options["user_id"]),
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
			return Response{Content: fmt.Sprintf("Assigned %s to %s.", r.roleDisplay(ctx, request.Request, memberRequest.RoleID, request.Options["role_display"]), r.userDisplay(ctx, request.Request, memberRequest.UserID, request.Options["user_display"])), Ephemeral: true}
		}
		memberRequest.Reason = "Panda natural-language member-role remove"
		if err := r.memberRoles.RemoveMemberRole(ctx, memberRequest); err != nil {
			return Response{Content: "Discord role could not be removed. Check Panda's Manage Roles permission and role hierarchy.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed %s from %s.", r.roleDisplay(ctx, request.Request, memberRequest.RoleID, request.Options["role_display"]), r.userDisplay(ctx, request.Request, memberRequest.UserID, request.Options["user_display"])), Ephemeral: true}
	case toolActionToolAccessAdd, toolActionToolAccessRemove, toolActionToolAccessDeny:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage tool access."); denied.Content != "" {
			return denied
		}
		toolName := strings.TrimSpace(request.Options["tool_name"])
		roleID := strings.TrimSpace(request.Options["role_id"])
		userID := strings.TrimSpace(request.Options["user_id"])
		if toolName == "" || (roleID == "" && userID == "") || (roleID != "" && userID != "") {
			return Response{Content: "That tool-access confirmation is invalid.", Ephemeral: true}
		}
		if request.Action == toolActionToolAccessAdd {
			if userID != "" {
				if _, err := r.admin.AddToolUser(ctx, request.Request.GuildID, request.Request.UserID, toolName, userID); err != nil {
					return toolAccessWriteError(err)
				}
				if toolNameIsPandaChat(toolName) {
					return Response{Content: fmt.Sprintf("Panda can reply to %s.", r.userDisplay(ctx, request.Request, userID, request.Options["user_display"])), Ephemeral: true}
				}
				return Response{Content: fmt.Sprintf("Allowed %s to use `%s`.", r.userDisplay(ctx, request.Request, userID, request.Options["user_display"]), toolName), Ephemeral: true}
			}
			if _, err := r.admin.AddToolRole(ctx, request.Request.GuildID, request.Request.UserID, toolName, roleID); err != nil {
				return toolAccessWriteError(err)
			}
			if toolNameIsPandaChat(toolName) {
				return Response{Content: fmt.Sprintf("Panda can reply to %s.", r.roleDisplay(ctx, request.Request, roleID, request.Options["role_display"])), Ephemeral: true}
			}
			return Response{Content: fmt.Sprintf("Allowed %s to use `%s`.", r.roleDisplay(ctx, request.Request, roleID, request.Options["role_display"]), toolName), Ephemeral: true}
		}
		if request.Action == toolActionToolAccessDeny {
			if userID != "" {
				if _, err := r.admin.DenyToolUser(ctx, request.Request.GuildID, request.Request.UserID, toolName, userID); err != nil {
					return toolAccessWriteError(err)
				}
				if toolNameIsPandaChat(toolName) {
					return Response{Content: fmt.Sprintf("Panda will not reply to %s.", r.userDisplay(ctx, request.Request, userID, request.Options["user_display"])), Ephemeral: true}
				}
				return Response{Content: fmt.Sprintf("Denied %s from `%s`.", r.userDisplay(ctx, request.Request, userID, request.Options["user_display"]), toolName), Ephemeral: true}
			}
			if _, err := r.admin.DenyToolRole(ctx, request.Request.GuildID, request.Request.UserID, toolName, roleID); err != nil {
				return toolAccessWriteError(err)
			}
			if toolNameIsPandaChat(toolName) {
				return Response{Content: fmt.Sprintf("Panda will not reply to %s.", r.roleDisplay(ctx, request.Request, roleID, request.Options["role_display"])), Ephemeral: true}
			}
			return Response{Content: fmt.Sprintf("Denied %s from `%s`.", r.roleDisplay(ctx, request.Request, roleID, request.Options["role_display"]), toolName), Ephemeral: true}
		}
		if userID != "" {
			if err := r.admin.RemoveToolUser(ctx, request.Request.GuildID, request.Request.UserID, toolName, userID); err != nil {
				return toolConfirmationError(err, "Tool access could not be removed.", "That tool access rule was not found.")
			}
			if toolNameIsPandaChat(toolName) {
				return Response{Content: fmt.Sprintf("Panda can reply to %s again.", r.userDisplay(ctx, request.Request, userID, request.Options["user_display"])), Ephemeral: true}
			}
			return Response{Content: fmt.Sprintf("Removed %s from `%s`.", r.userDisplay(ctx, request.Request, userID, request.Options["user_display"]), toolName), Ephemeral: true}
		}
		if err := r.admin.RemoveToolRole(ctx, request.Request.GuildID, request.Request.UserID, toolName, roleID); err != nil {
			return toolConfirmationError(err, "Tool access could not be removed.", "That tool access rule was not found.")
		}
		if toolNameIsPandaChat(toolName) {
			return Response{Content: fmt.Sprintf("Panda can reply to %s again.", r.roleDisplay(ctx, request.Request, roleID, request.Options["role_display"])), Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed %s from `%s`.", r.roleDisplay(ctx, request.Request, roleID, request.Options["role_display"]), toolName), Ephemeral: true}
	case toolActionToolAccessOpen:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage tool access."); denied.Content != "" {
			return denied
		}
		toolNames := splitConfirmationCSV(request.Options["tool_names"])
		permissions := splitConfirmationCSV(request.Options["permissions"])
		if len(toolNames) == 0 {
			return Response{Content: "That tool-access confirmation is invalid.", Ephemeral: true}
		}
		var removedPermissionRules int64
		var removedToolRules int64
		for _, permission := range permissions {
			result, err := r.admin.ClearPermissionAccess(ctx, request.Request.GuildID, request.Request.UserID, permission)
			if err != nil {
				return Response{Content: "Permission access could not be opened.", Ephemeral: true}
			}
			removedPermissionRules += result.RemovedRoleRules + result.RemovedUserRules
		}
		for _, toolName := range toolNames {
			result, err := r.admin.ClearToolAccess(ctx, request.Request.GuildID, request.Request.UserID, toolName)
			if err != nil {
				return Response{Content: "Tool access could not be opened.", Ephemeral: true}
			}
			removedToolRules += result.RemovedRoleRules + result.RemovedUserRules
		}
		return Response{Content: fmt.Sprintf("Opened `%s` to everyone. Cleared %d permission rule(s) and %d tool access rule(s).", strings.Join(toolNames, "`, `"), removedPermissionRules, removedToolRules), Ephemeral: true}
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
		return Response{Content: fmt.Sprintf("Set `%s` channel access rule for %s.", saved.Rule, r.channelDisplay(ctx, request.Request, saved.ChannelID, request.Options["channel_display"])), Ephemeral: true}
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
		return Response{Content: fmt.Sprintf("Removed channel access rule for %s.", r.channelDisplay(ctx, request.Request, channelID, request.Options["channel_display"])), Ephemeral: true}
	case toolActionQuietModeSet, toolActionQuietModeClear:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage quiet mode."); denied.Content != "" {
			return denied
		}
		return r.handleQuietModeConfirmation(ctx, request)
	case toolActionSafetyTimeout, toolActionSafetyStrikeRemove, toolActionSafetyClear:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage safety state."); denied.Content != "" {
			return denied
		}
		return r.handleSafetyConfirmation(ctx, request)
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
		exposure := r.composedExposureLine(ctx, request.Request, result.Tool)
		return Response{Content: renderComposedApprovalContent(result.Tool, result.Version, composedApprovalSummary(result), exposure), Ephemeral: true}
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
	case toolActionComposedToolDelete:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanApproveComposedTool, "You do not have permission to permanently delete composed tools."); denied.Content != "" {
			return denied
		}
		if r.composed == nil {
			return Response{Content: "Composed tools are not configured for this runtime.", Ephemeral: true}
		}
		toolName := strings.TrimSpace(request.Options["tool_name"])
		if toolName == "" {
			return Response{Content: "That composed-tool delete confirmation is invalid.", Ephemeral: true}
		}
		tool, err := r.composed.Delete(ctx, request.Request.GuildID, toolName, request.Request.UserID)
		if err != nil {
			return toolConfirmationError(err, "Composed tool could not be permanently deleted.", "That composed tool was not found.")
		}
		return Response{Content: fmt.Sprintf("Permanently deleted `%s` and its versions, runs, and dedupe records.", tool.Name), Ephemeral: true}
	case toolActionOwnerOpsDrain,
		toolActionOwnerOpsResume,
		toolActionOwnerOpsIncidentEnable,
		toolActionOwnerOpsIncidentDisable:
		return r.handleOwnerOpsConfirmation(ctx, request)
	default:
		return Response{Content: "That confirmation is no longer supported.", Ephemeral: true}
	}
}

func (r *Router) handleOwnerOpsConfirmation(ctx context.Context, request ToolConfirmationRequest) Response {
	if r.ops == nil {
		return Response{Content: "Owner operations are not configured for this runtime.", Ephemeral: true}
	}
	if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanUseOwnerOps, "Only a bot owner can use owner operations."); denied.Content != "" {
		return denied
	}
	switch request.Action {
	case toolActionOwnerOpsDrain:
		r.ops.Drain()
		return Response{Content: "Queue worker is draining and will not claim new jobs.", Ephemeral: true}
	case toolActionOwnerOpsResume:
		r.ops.Resume()
		return Response{Content: "Queue worker resumed job processing.", Ephemeral: true}
	case toolActionOwnerOpsIncidentEnable:
		r.ops.EnableIncident()
		return Response{Content: "Incident mode enabled.", Ephemeral: true}
	case toolActionOwnerOpsIncidentDisable:
		r.ops.DisableIncident()
		return Response{Content: "Incident mode disabled.", Ephemeral: true}
	default:
		return Response{Content: "That owner-ops confirmation is invalid.", Ephemeral: true}
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
		toolActionUserPermissionAdd,
		toolActionUserPermissionRemove,
		toolActionUserProfileAdd,
		toolActionUserProfileRemove,
		toolActionToolAccessAdd,
		toolActionToolAccessRemove,
		toolActionToolAccessDeny,
		toolActionToolAccessOpen,
		toolActionChannelRuleSet,
		toolActionChannelRuleRemove,
		toolActionQuietModeSet,
		toolActionQuietModeClear,
		toolActionSafetyTimeout,
		toolActionSafetyStrikeRemove,
		toolActionSafetyClear:
		if action == toolActionQuietModeSet || action == toolActionQuietModeClear {
			return features.AdminSetup
		}
		return features.AdminAccessControl
	case toolActionDiscordRoleCreate,
		toolActionMemberRoleAdd,
		toolActionMemberRoleRemove:
		return features.DiscordRoleManagement
	case toolActionDiscordPollCreate:
		return features.Polls
	case toolActionServerSetupApply,
		toolActionServerSetupRollback:
		return features.AdminSetup
	case toolActionComposedToolApprove,
		toolActionComposedToolRollback,
		toolActionComposedToolDelete:
		return features.ComposedTools
	case toolActionOwnerOpsDrain,
		toolActionOwnerOpsResume,
		toolActionOwnerOpsIncidentEnable,
		toolActionOwnerOpsIncidentDisable:
		return features.OwnerOps
	default:
		return ""
	}
}

func composedApprovalSummary(result composed.DraftResult) composed.ApprovalSummary {
	summary := composed.ApprovalSummary{
		Purpose:             result.Spec.Description,
		NativeTools:         append([]string(nil), result.Validation.NativeTools...),
		WriteActions:        append([]string(nil), result.Validation.Writes...),
		RiskLevel:           result.Validation.RiskLevel,
		RiskReasons:         append([]string(nil), result.Validation.Warnings...),
		RequiresApproval:    result.Spec.Safety.RequiresApproval,
		WriteConfirmation:   result.Spec.Safety.RequiresConfirmationOnWrite,
		MaxNestedDepth:      result.Spec.Safety.MaxNestedDepth,
		CooldownSeconds:     result.Spec.Safety.CooldownSeconds,
		MaxRunsPerHour:      result.Spec.Safety.MaxRunsPerHour,
		DedupeWindowSeconds: result.Spec.Safety.DedupeWindowSeconds,
	}
	for _, invocation := range result.Spec.Invocations {
		if invocation.Enabled != nil && !*invocation.Enabled {
			continue
		}
		summary.InvocationModes = append(summary.InvocationModes, invocation.Type)
		switch invocation.Type {
		case composed.InvocationEvent:
			trigger := "event " + firstNonEmpty(invocation.EventType, "unspecified")
			if len(invocation.Filters) > 0 {
				trigger += " with filters"
			}
			summary.TriggerSummary = append(summary.TriggerSummary, trigger)
		case composed.InvocationScheduled:
			summary.TriggerSummary = append(summary.TriggerSummary, "scheduled")
		default:
			summary.TriggerSummary = append(summary.TriggerSummary, invocation.Type)
		}
	}
	for _, step := range result.Spec.Steps {
		if step.Type != composed.StepToolCall {
			continue
		}
		for _, key := range []string{"channel_id", "voice_channel_id", "role_id", "user_id"} {
			if value := strings.TrimSpace(fmt.Sprint(step.Arguments[key])); value != "" && value != "<nil>" {
				summary.TargetSummary = append(summary.TargetSummary, step.Tool+" "+key+"="+value)
			}
		}
	}
	return summary
}

func renderComposedApprovalContent(toolName string, version int, summary composed.ApprovalSummary, exposure string) string {
	lines := []string{
		fmt.Sprintf("Approved `%s` version %d.", toolName, version),
		fmt.Sprintf("Risk: `%s`.", firstNonEmpty(summary.RiskLevel, "unknown")),
	}
	if summary.Purpose != "" {
		lines = append(lines, "Purpose: "+summary.Purpose)
	}
	lines = append(lines,
		"Triggers: "+displayList(summary.TriggerSummary),
		"Native tools: "+displayList(summary.NativeTools),
		"Writes: "+displayList(summary.WriteActions),
		"Targets: "+displayList(summary.TargetSummary),
		fmt.Sprintf("Safety: approval=%t write_confirmation=%t max_depth=%d cooldown=%ds max_runs_per_hour=%d dedupe=%ds.",
			summary.RequiresApproval,
			summary.WriteConfirmation,
			summary.MaxNestedDepth,
			summary.CooldownSeconds,
			summary.MaxRunsPerHour,
			summary.DedupeWindowSeconds,
		),
		exposure,
	)
	return strings.Join(lines, "\n")
}

func (r *Router) composedExposureLine(ctx context.Context, request Request, toolName string) string {
	if r == nil || r.admin == nil || strings.TrimSpace(request.GuildID) == "" {
		return "Exposure: enabled; tool-access rules could not be inspected."
	}
	rules, err := r.admin.ListToolAccess(ctx, request.GuildID)
	if err != nil {
		return "Exposure: enabled; tool-access lookup failed."
	}
	matches := 0
	for _, rule := range rules {
		if strings.EqualFold(strings.TrimSpace(rule.ToolName), strings.TrimSpace(toolName)) {
			matches++
		}
	}
	if matches == 0 {
		return "Exposure: enabled but private for regular callers. Next actions: keep private, allow a role, allow a user, or open to everyone if policy permits."
	}
	return fmt.Sprintf("Exposure: enabled with %d explicit tool-access rule(s).", matches)
}

func displayList(values []string) string {
	values = distinctDisplayStrings(values)
	if len(values) == 0 {
		return "none"
	}
	return "`" + strings.Join(values, "`, `") + "`"
}

func distinctDisplayStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
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

func (r *Router) handleDiscordWriteConfirmation(ctx context.Context, request ToolConfirmationRequest) Response {
	if r.tools == nil {
		return Response{Content: "Discord write tools are not configured for this runtime.", Ephemeral: true}
	}
	toolName := strings.TrimSpace(request.Options["tool_name"])
	rawArguments := strings.TrimSpace(request.Options["arguments_json"])
	if toolName == "" || rawArguments == "" {
		return Response{Content: "That Discord write confirmation is invalid.", Ephemeral: true}
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(rawArguments), &arguments); err != nil || arguments == nil {
		return Response{Content: "That Discord write confirmation is invalid.", Ephemeral: true}
	}
	data, err := json.Marshal(arguments)
	if err != nil {
		return Response{Content: "That Discord write confirmation is invalid.", Ephemeral: true}
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
			ID:   "confirmed-discord-write",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      toolName,
				Arguments: string(data),
			},
		},
	})
	if err != nil {
		return Response{Content: "Discord write could not be completed: " + err.Error(), Ephemeral: true, Presentation: Presentation{Title: "Discord write failed", Accent: AccentWarning}}
	}
	if message := toolExecutionErrorMessage(result.Message.Content); message != "" {
		return Response{Content: "Discord write could not be completed: " + message, Ephemeral: true, Presentation: Presentation{Title: "Discord write failed", Accent: AccentWarning}}
	}
	return Response{Content: fmt.Sprintf("Completed `%s`.", toolName), Ephemeral: true, Presentation: Presentation{Title: "Discord write completed", Accent: AccentSuccess}}
}

func (r *Router) handleServerSetupApplyConfirmation(ctx context.Context, request ToolConfirmationRequest) Response {
	if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to apply server setup."); denied.Content != "" {
		return denied
	}
	if r.serverSetup == nil {
		return Response{Content: "Server setup is not configured for this runtime.", Ephemeral: true, Presentation: Presentation{Title: "Setup unavailable", Accent: AccentWarning}}
	}
	projectID := strings.TrimSpace(request.Options["project_id"])
	if projectID == "" {
		return Response{Content: "That setup confirmation is invalid.", Ephemeral: true, Presentation: Presentation{Title: "Setup confirmation invalid", Accent: AccentWarning}}
	}
	project, err := r.serverSetup.Confirm(ctx, projectID, request.Request.UserID, true)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return Response{Content: "That setup project was not found.", Ephemeral: true, Presentation: Presentation{Title: "Setup not found", Accent: AccentWarning}}
		}
		message := "Setup could not be queued."
		if strings.TrimSpace(err.Error()) != "" {
			message += " " + err.Error()
		}
		return Response{Content: message, Ephemeral: true, Presentation: Presentation{Title: "Setup not queued", Accent: AccentWarning}}
	}
	status := firstNonEmpty(project.Status, "confirmed")
	if status == "queued" {
		return Response{Content: fmt.Sprintf("Setup project `%s` is queued. Panda will apply it in the background.", project.ID), Ephemeral: true, Presentation: Presentation{Title: "Setup queued", Accent: AccentSuccess}}
	}
	return Response{Content: fmt.Sprintf("Setup project `%s` is confirmed. Apply it from the setup status screen once the worker is available.", project.ID), Ephemeral: true, Presentation: Presentation{Title: "Setup confirmed", Accent: AccentInfo}}
}

func (r *Router) handleServerSetupRollbackConfirmation(ctx context.Context, request ToolConfirmationRequest) Response {
	if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to roll back server setup."); denied.Content != "" {
		return denied
	}
	if r.serverSetup == nil {
		return Response{Content: "Server setup is not configured for this runtime.", Ephemeral: true, Presentation: Presentation{Title: "Setup unavailable", Accent: AccentWarning}}
	}
	projectID := strings.TrimSpace(request.Options["project_id"])
	if projectID == "" {
		return Response{Content: "That setup rollback confirmation is invalid.", Ephemeral: true, Presentation: Presentation{Title: "Setup confirmation invalid", Accent: AccentWarning}}
	}
	result, err := r.serverSetup.RollbackProject(ctx, projectID, request.Request.UserID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return Response{Content: "That setup project was not found.", Ephemeral: true, Presentation: Presentation{Title: "Setup not found", Accent: AccentWarning}}
		}
		message := "Setup rollback could not be completed."
		if strings.TrimSpace(err.Error()) != "" {
			message += " " + err.Error()
		}
		return Response{Content: message, Ephemeral: true, Presentation: Presentation{Title: "Setup rollback failed", Accent: AccentWarning}}
	}
	content := fmt.Sprintf("Setup project `%s` was rolled back.", result.ProjectID)
	if len(result.Warnings) > 0 {
		content += " Some items need manual cleanup; check setup status for details."
	}
	return Response{Content: content, Ephemeral: true, Presentation: Presentation{Title: "Setup rolled back", Accent: AccentSuccess}}
}

func (r *Router) handleQuietModeConfirmation(ctx context.Context, request ToolConfirmationRequest) Response {
	if r.tools == nil {
		return Response{Content: "Quiet mode management is not configured for this runtime.", Ephemeral: true}
	}
	arguments := map[string]any{"action": "clear"}
	if request.Action == toolActionQuietModeSet {
		durationSeconds, err := positiveInt64Option(request.Options["duration_seconds"])
		if err != nil {
			return Response{Content: "That quiet-mode confirmation is invalid.", Ephemeral: true}
		}
		arguments["action"] = "set"
		arguments["duration_seconds"] = durationSeconds
	}
	data, err := json.Marshal(arguments)
	if err != nil {
		return Response{Content: "That quiet-mode confirmation is invalid.", Ephemeral: true}
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
			ID:   "confirmed-quiet-mode",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_quiet_mode",
				Arguments: string(data),
			},
		},
	})
	if err != nil {
		return Response{Content: "Quiet mode could not be updated.", Ephemeral: true}
	}
	if message := toolExecutionErrorMessage(result.Message.Content); message != "" {
		return Response{Content: "Quiet mode could not be updated: " + message, Ephemeral: true}
	}
	if request.Action == toolActionQuietModeSet {
		durationSeconds, _ := positiveInt64Option(request.Options["duration_seconds"])
		timeoutUntil := quietModeTimeoutUntilFromToolContent(result.Message.Content)
		content := fmt.Sprintf("Panda is taking a server-wide timeout for %s.", durationSecondsDisplayLabel(durationSeconds))
		if timeoutUntil != "" {
			content += fmt.Sprintf(" Quiet mode expires at `%s`.", timeoutUntil)
		}
		return Response{Content: content, Ephemeral: true, Presentation: Presentation{Title: "Quiet mode started", Accent: AccentWarning}}
	}
	return Response{Content: "Panda quiet mode is cleared.", Ephemeral: true, Presentation: Presentation{Title: "Quiet mode cleared", Accent: AccentSuccess}}
}

func quietModeTimeoutUntilFromToolContent(content string) string {
	var payload struct {
		Result struct {
			TimeoutUntil string `json:"timeout_until"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Result.TimeoutUntil)
}

func (r *Router) handleSafetyConfirmation(ctx context.Context, request ToolConfirmationRequest) Response {
	if r.tools == nil {
		return Response{Content: "Safety state management is not configured for this runtime.", Ephemeral: true}
	}
	userID := normalizeDiscordUserID(request.Options["user_id"])
	if userID == "" {
		return Response{Content: "That safety confirmation is invalid.", Ephemeral: true}
	}
	arguments := map[string]any{
		"action":  "clear",
		"user_id": userID,
	}
	count := 0
	if request.Action == toolActionSafetyStrikeRemove {
		count = intOption(request.Options["count"], 1)
		if count <= 0 || count > 100 {
			return Response{Content: "That safety confirmation is invalid.", Ephemeral: true}
		}
		arguments["action"] = "remove"
		arguments["count"] = count
	} else if request.Action == toolActionSafetyTimeout {
		durationSeconds, err := positiveInt64Option(request.Options["duration_seconds"])
		if err != nil {
			return Response{Content: "That safety confirmation is invalid.", Ephemeral: true}
		}
		arguments["action"] = "timeout"
		arguments["duration_seconds"] = durationSeconds
	}
	data, err := json.Marshal(arguments)
	if err != nil {
		return Response{Content: "That safety confirmation is invalid.", Ephemeral: true}
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
			ID:   "confirmed-safety",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "panda_manage_safety",
				Arguments: string(data),
			},
		},
	})
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return Response{Content: "That user does not have safety strike state to remove.", Ephemeral: true}
		}
		return Response{Content: "Safety state could not be updated.", Ephemeral: true}
	}
	if message := toolExecutionErrorMessage(result.Message.Content); message != "" {
		return Response{Content: "Safety state could not be updated: " + message, Ephemeral: true}
	}
	if request.Action == toolActionSafetyTimeout {
		durationSeconds, _ := positiveInt64Option(request.Options["duration_seconds"])
		return Response{Content: fmt.Sprintf("Timed out %s from Panda for %s.", r.userDisplay(ctx, request.Request, userID, request.Options["user_display"]), durationSecondsDisplayLabel(durationSeconds)), Ephemeral: true}
	}
	if request.Action == toolActionSafetyClear {
		return Response{Content: fmt.Sprintf("Cleared safety strikes and timeout state for %s.", r.userDisplay(ctx, request.Request, userID, request.Options["user_display"])), Ephemeral: true}
	}
	return Response{Content: fmt.Sprintf("Removed %d safety strike(s) from %s.", count, r.userDisplay(ctx, request.Request, userID, request.Options["user_display"])), Ephemeral: true}
}

func positiveInt64Option(raw string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("positive integer is required")
	}
	return value, nil
}

func durationSecondsDisplayLabel(seconds int64) string {
	if seconds <= 0 {
		return "the requested duration"
	}
	duration := (time.Duration(seconds) * time.Second).Round(time.Second)
	units := []struct {
		name string
		size time.Duration
	}{
		{"week", 7 * 24 * time.Hour},
		{"day", 24 * time.Hour},
		{"hour", time.Hour},
		{"minute", time.Minute},
		{"second", time.Second},
	}
	for _, unit := range units {
		if duration >= unit.size && duration%unit.size == 0 {
			count := int64(duration / unit.size)
			if count == 1 {
				return "1 " + unit.name
			}
			return fmt.Sprintf("%d %ss", count, unit.name)
		}
	}
	return duration.String()
}

func toolExecutionErrorMessage(content string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &payload); err != nil {
		return ""
	}
	if value, ok := payload["error"]; ok && value != nil {
		return strings.TrimSpace(fmt.Sprint(value))
	}
	return ""
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

func (r *Router) maintenanceResponse(ctx context.Context) Response {
	if r == nil || r.runtime == nil {
		return Response{}
	}
	status, err := r.runtime.Status(ctx)
	if err != nil {
		slog.Warn("runtime status lookup failed", slog.Any("err", err))
		return Response{
			Content:   "Panda maintenance status could not be checked. Please try again later.",
			Ephemeral: true,
			Presentation: Presentation{
				Title:  "Maintenance status unavailable",
				Accent: AccentWarning,
			},
		}
	}
	if !status.Disabled {
		return Response{}
	}
	return Response{
		Content: status.EffectiveMessage,
		Presentation: Presentation{
			Title:  "Maintenance in progress",
			Accent: AccentInfo,
		},
	}
}

func maintenanceExempt(request Request) bool {
	return strings.EqualFold(strings.TrimSpace(request.Command), "ops") && request.IsOwner
}

func (r *Router) quietModeActive(ctx context.Context, request Request) bool {
	if r == nil || r.admin == nil || strings.TrimSpace(request.GuildID) == "" {
		return false
	}
	if r.quietModeBypassAllowed(ctx, request) {
		return false
	}
	status, err := r.admin.QuietModeStatus(ctx, request.GuildID, time.Now().UTC())
	if err != nil {
		slog.Warn("quiet mode status lookup failed",
			slog.Any("err", err),
			slog.String("guild_id", request.GuildID),
			slog.String("channel_id", request.ChannelID),
			slog.String("request_id", request.RequestID),
			slog.String("user_id", request.UserID),
		)
		return false
	}
	return status.Active
}

func (r *Router) quietModeBypassAllowed(ctx context.Context, request Request) bool {
	if request.IsOwner || request.IsGuildAdmin {
		return true
	}
	allowed, err := r.admin.HasGuildControl(ctx, assistantAccessRequest(request))
	if err != nil {
		slog.Warn("quiet mode bypass permission lookup failed",
			slog.Any("err", err),
			slog.String("guild_id", request.GuildID),
			slog.String("channel_id", request.ChannelID),
			slog.String("request_id", request.RequestID),
			slog.String("user_id", request.UserID),
		)
		return false
	}
	return allowed
}

func quietModeExempt(request Request) bool {
	if !strings.EqualFold(strings.TrimSpace(request.Command), "admin") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(request.Subcommand)) {
	case "quiet", "quiet-mode", "quiet_mode", "timeout":
		return true
	default:
		return false
	}
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

func (r *Router) safetyBypassAllowed(ctx context.Context, request Request) bool {
	if request.IsOwner || request.IsGuildAdmin {
		return true
	}
	if r == nil || r.admin == nil || strings.TrimSpace(request.GuildID) == "" {
		return false
	}
	allowed, err := r.admin.HasGuildControl(ctx, assistantAccessRequest(request))
	if err != nil {
		slog.Warn("safety bypass permission lookup failed",
			slog.Any("err", err),
			slog.String("guild_id", request.GuildID),
			slog.String("user_id", request.UserID),
		)
		return false
	}
	return allowed
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
	denied                  map[string]struct{}
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
	roles, err := r.admin.ToolUserRoleAccess(ctx, request.GuildID, request.UserID, request.RoleIDs)
	if err != nil {
		return toolFilter{allowed: map[string]struct{}{}, restricted: map[string]struct{}{}, requireExplicitComposed: true}
	}
	return toolFilter{
		allowed:                 namesToSet(roles.AllowedTools),
		denied:                  namesToSet(roles.DeniedTools),
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
		DeniedTools:                  filter.denied,
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
	if featureEnabled(features.ImageGeneration) {
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantImageGeneration, r.admin.CanUseImageGeneration)
	}
	if featureEnabled(features.YouTubeClipping) {
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantYouTubeClipping, r.admin.CanUseYouTubeClipping)
	}
	if featureEnabled(features.Knowledge) {
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantMemoryRead, r.admin.CanReadMemory)
		r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminMemoryManage, r.admin.CanManageMemory)
	}
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantWebSearch, r.admin.CanUseWebSearch)
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
		return map[string]struct{}{features.WebSearch: {}}, true
	}
	enabled[features.WebSearch] = struct{}{}
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

func commandImageReferenceIDs(references []generated.ImageReference) []string {
	ids := make([]string, 0, len(references))
	for _, reference := range references {
		id := strings.TrimSpace(reference.ID)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
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
	quote, err := r.billing.QuoteAction(billing.ActionQuoteRequest{Action: billing.ActionAssistantModelRound, RequestID: request.RequestID})
	if err != nil {
		return billingErrorResponse(err)
	}
	entitlement, err := r.billing.Resolve(ctx, request.GuildID)
	if err == nil {
		if !entitlement.CanUsePaidFeatures || entitlement.ReadOnly {
			err = billing.ErrReadOnly
		} else if entitlement.AvailableCredits < quote.MaxCredits {
			err = billing.CreditError{
				Action:           quote.Action,
				Used:             entitlement.Pack.Credits - entitlement.AvailableCredits,
				Reserved:         entitlement.ReservedCredits,
				Limit:            entitlement.Pack.Credits,
				Pack:             entitlement.Pack.Pack,
				RequiredCredits:  quote.MaxCredits,
				AvailableCredits: entitlement.AvailableCredits,
				UpgradeURL:       entitlement.UpgradeURL,
			}
		}
	}
	return billingErrorResponse(err)
}

func (r *Router) beginAIUsage(ctx context.Context, request Request) (billing.Reservation, Response) {
	if r.billing == nil || request.GuildID == "" {
		return billing.Reservation{}, Response{}
	}
	quote, err := r.billing.QuoteAction(billing.ActionQuoteRequest{
		Action:    billing.ActionAssistantModelRound,
		RequestID: request.RequestID,
	})
	if err != nil {
		return billing.Reservation{}, billingErrorResponse(err)
	}
	reservation, err := r.billing.BeginCreditUsage(ctx, request.GuildID, quote)
	return reservation, billingErrorResponse(err)
}

func (r *Router) commitAIUsage(ctx context.Context, reservation billing.Reservation, usage llm.Usage) {
	if r.billing != nil && reservation.ID != "" {
		final := billing.CreditUsageFinal{}
		if usage.PromptTokens > 0 || usage.CompletionTokens > 0 {
			if quote, err := r.billing.QuoteAction(billing.ActionQuoteRequest{
				Action:       billing.ActionAssistantModelRound,
				RequestID:    reservation.RequestID,
				InputTokens:  usage.PromptTokens,
				OutputTokens: usage.CompletionTokens,
				Metadata: map[string]any{
					"prompt_tokens":     usage.PromptTokens,
					"completion_tokens": usage.CompletionTokens,
					"total_tokens":      usage.TotalTokens,
				},
			}); err == nil {
				final.Credits = quote.ExpectedCredits
			}
		}
		_ = r.billing.CommitCreditUsage(ctx, reservation, final)
	}
}

func (r *Router) releaseAIUsage(ctx context.Context, reservation billing.Reservation) {
	if r.billing != nil && reservation.ID != "" {
		_ = r.billing.ReleaseUsage(ctx, reservation)
	}
}

func (r *Router) commitRoutingUsage(ctx context.Context, request Request, assistantReservation billing.Reservation) {
	if r.billing == nil || strings.TrimSpace(request.GuildID) == "" {
		return
	}
	r.releaseAIUsage(ctx, assistantReservation)
	quote, err := r.billing.QuoteAction(billing.ActionQuoteRequest{
		Action:    billing.ActionRoutingCheck,
		RequestID: request.RequestID,
	})
	if err != nil {
		return
	}
	reservation, err := r.billing.BeginCreditUsage(ctx, request.GuildID, quote)
	if err != nil {
		return
	}
	_ = r.billing.CommitCreditUsage(ctx, reservation, billing.CreditUsageFinal{Credits: quote.ExpectedCredits})
}

func billingErrorResponse(err error) Response {
	if err == nil {
		return Response{}
	}
	var creditErr billing.CreditError
	if errors.As(err, &creditErr) {
		content := fmt.Sprintf("This action needs %d credits for %s, but this server only has %d credits available.", creditErr.RequiredCredits, billing.ActionLabel(creditErr.Action), creditErr.AvailableCredits)
		content += "\nThe billing owner can run `/billing` to buy credits, then activate a verified SOL payment with `/billing action:activate api_key:<key>`."
		return Response{Content: content, Ephemeral: true, Presentation: Presentation{Title: "Credits depleted", Accent: AccentWarning}}
	}
	if errors.Is(err, billing.ErrNoCreditAccount) {
		return Response{Content: "This server does not have an active Panda credit account. Use `/billing` for status and the SOL purchase link.", Ephemeral: true, Presentation: Presentation{Title: "Credits required", Accent: AccentWarning}}
	}
	if errors.Is(err, billing.ErrReadOnly) {
		return Response{Content: "This server's Panda credit account is read-only. Billing, help, export/delete, and support access remain available.", Ephemeral: true, Presentation: Presentation{Title: "Credit account read-only", Accent: AccentWarning}}
	}
	return Response{Content: "Billing status could not be checked. Please try again later.", Ephemeral: true, Presentation: Presentation{Title: "Billing check failed", Accent: AccentWarning}}
}

func billingErrorResponseIfBilling(err error) (Response, bool) {
	if err == nil {
		return Response{}, false
	}
	var creditErr billing.CreditError
	if errors.As(err, &creditErr) || errors.Is(err, billing.ErrNoCreditAccount) || errors.Is(err, billing.ErrReadOnly) {
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
		if model, task, ok := assistant.FailedModel(err); ok {
			slog.Warn("assistant request failed", slog.String("model", model), slog.String("task", task), slog.Any("err", err))
		} else {
			slog.Warn("assistant request failed", slog.Any("err", err))
		}
		return Response{Content: "Panda is having trouble with AI responses right now. Please try again later.", Ephemeral: true, Presentation: Presentation{Title: "AI response failed", Accent: AccentDanger}}
	}
}

func (r *Router) responseFromAssistantAnswer(ctx context.Context, request Request, answer assistant.AskResponse, threadID, threadName string) Response {
	response := Response{
		Content:           answer.Content,
		ThreadID:          threadID,
		ThreadName:        threadName,
		GeneratedFiles:    generated.CloneFiles(answer.GeneratedFiles),
		UsageReservations: append([]billing.Reservation(nil), answer.UsageReservations...),
		Usage:             answer.Usage,
	}
	if answer.Card != nil {
		cardResponse := responseFromAssistantCard(request.UserID, answer.Card)
		if answer.Card.Standalone && strings.TrimSpace(answer.Content) != "" {
			response = cardResponse
			response.ThreadID = threadID
			response.ThreadName = threadName
			response.GeneratedFiles = generated.CloneFiles(answer.GeneratedFiles)
			response.UsageReservations = append([]billing.Reservation(nil), answer.UsageReservations...)
			if followupContent := standaloneCardFollowupContent(answer.Content); followupContent != "" && len(cardResponse.MediaItems) == 0 {
				response.Followups = append(response.Followups, Response{Content: followupContent})
			}
		} else {
			if len(cardResponse.MediaItems) > 0 {
				response.Content = cardResponse.Content
			} else {
				response.Content = firstNonEmpty(response.Content, answer.Card.Content)
			}
			response.Presentation = cardResponse.Presentation
			response.MediaItems = cardResponse.MediaItems
			response.Actions = cardResponse.Actions
			response.Selection = cardResponse.Selection
		}
	}
	pendingConfirmations := answerConfirmations(answer)
	if confirmations := ToolConfirmationsFromAssistant(request.UserID, pendingConfirmations); len(confirmations) > 0 {
		response.Confirmations = confirmations
		response.Confirmation = &response.Confirmations[0]
		response.Content = stripRawConfirmationPayload(response.Content)
		response.Content = appendConfirmationNotices(response.Content, confirmationSummaries(pendingConfirmations), len(confirmations))
		response.Presentation = Presentation{Title: "Confirmation required", Accent: AccentWarning}
		if confirmationsContainDanger(confirmations) {
			response.Presentation.Accent = AccentDanger
		}
		return response
	}
	if r.feedback != nil {
		if len(response.Followups) > 0 {
			followup := &response.Followups[len(response.Followups)-1]
			r.attachAssistantFeedback(ctx, request, answer, threadID, followup, followup.Content)
		} else {
			r.attachAssistantFeedback(ctx, request, answer, threadID, &response, response.Content)
		}
	}
	return response
}

func standaloneCardFollowupContent(content string) string {
	content = strings.TrimSpace(content)
	if content == "" || placeholderOnlyAssistantContent(content) || assistant.ResponseContentLooksInternalArtifact(content) {
		return ""
	}
	return content
}

func placeholderOnlyAssistantContent(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return true
	}
	trimmed = strings.Trim(trimmed, "`*_~")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return true
	}
	return strings.Trim(trimmed, ".\u2026 \t\r\n") == ""
}

func responseFromAssistantCard(userID string, card *assistant.ToolCard) Response {
	if card == nil {
		return Response{}
	}
	content := card.Content
	presentation := presentationFromAssistantCard(card)
	mediaItems := mediaItemsFromAssistantCard(card.MediaItems)
	if len(mediaItems) > 0 {
		content = ""
		presentation.Title = ""
		presentation.URL = ""
		presentation.Fields = nil
	}
	return Response{
		Content:      content,
		Presentation: presentation,
		MediaItems:   mediaItems,
		Actions:      actionsFromAssistantCard(card),
		Selection:    selectionFromAssistantCard(userID, card.Selection),
	}
}

func (r *Router) attachAssistantFeedback(ctx context.Context, request Request, answer assistant.AskResponse, threadID string, response *Response, content string) {
	if r == nil || r.feedback == nil || response == nil || !assistantFeedbackEligible(answer, content) {
		return
	}
	target, err := r.feedback.CreateTarget(ctx, feedback.TargetRequest{
		GuildID:   request.GuildID,
		ChannelID: request.ChannelID,
		UserID:    request.UserID,
		Command:   firstNonEmpty(request.Command, "assistant"),
		Content:   content,
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

func answerConfirmations(answer assistant.AskResponse) []assistant.InteractionConfirmation {
	if len(answer.Confirmations) > 0 {
		return answer.Confirmations
	}
	if answer.Confirmation == nil {
		return nil
	}
	return []assistant.InteractionConfirmation{*answer.Confirmation}
}

func assistantAnswerHasPayload(answer assistant.AskResponse) bool {
	if answer.Terminal {
		return true
	}
	if strings.TrimSpace(answer.Content) != "" || len(answerConfirmations(answer)) > 0 || len(answer.GeneratedFiles) > 0 {
		return true
	}
	card := answer.Card
	if card == nil {
		return false
	}
	return strings.TrimSpace(card.Content) != "" ||
		strings.TrimSpace(card.Title) != "" ||
		strings.TrimSpace(card.URL) != "" ||
		len(card.Fields) > 0 ||
		len(card.MediaItems) > 0 ||
		len(card.Actions) > 0 ||
		card.Selection != nil
}

func confirmationSummaries(confirmations []assistant.InteractionConfirmation) []string {
	summaries := make([]string, 0, len(confirmations))
	seen := map[string]struct{}{}
	for _, confirmation := range confirmations {
		summary := strings.TrimSpace(confirmation.Summary)
		if summary == "" {
			continue
		}
		if _, ok := seen[summary]; ok {
			continue
		}
		seen[summary] = struct{}{}
		summaries = append(summaries, summary)
	}
	return summaries
}

func confirmationsContainDanger(confirmations []Confirmation) bool {
	for _, confirmation := range confirmations {
		if confirmation.Danger {
			return true
		}
	}
	return false
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

func mediaItemsFromAssistantCard(items []assistant.ToolCardMediaItem) []MediaItem {
	result := make([]MediaItem, 0, len(items))
	for _, item := range items {
		result = append(result, MediaItem{
			Title:        item.Title,
			Description:  item.Description,
			URL:          item.URL,
			ThumbnailURL: item.ThumbnailURL,
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

func selectionFromAssistantCard(userID string, selection *assistant.ToolCardSelection) *Selection {
	if selection == nil {
		return nil
	}
	options := make([]SelectionOption, 0, len(selection.Options))
	for _, option := range selection.Options {
		options = append(options, SelectionOption{
			Label:          option.Label,
			Description:    option.Description,
			Value:          option.Value,
			URL:            option.URL,
			ThumbnailURL:   option.ThumbnailURL,
			Command:        option.Command,
			Prompt:         option.Prompt,
			VoiceChannelID: option.VoiceChannelID,
		})
	}
	return PrepareSelectionForUser(userID, &Selection{
		Placeholder: selection.Placeholder,
		Options:     options,
	})
}

func appendConfirmationNotice(content, summary string) string {
	return appendConfirmationNotices(content, []string{summary}, 1)
}

func appendConfirmationNotices(content string, summaries []string, confirmationCount int) string {
	content = strings.TrimSpace(content)
	for _, summary := range summaries {
		summary = strings.TrimSpace(summary)
		if summary == "" || strings.Contains(content, summary) {
			continue
		}
		if content != "" {
			content += "\n\n"
		}
		content += summary
	}
	if content != "" {
		content += "\n\n"
	}
	if confirmationCount > 1 {
		return content + "Press each confirmation button you want to apply."
	}
	return content + "Press the confirmation button to continue."
}

func stripRawConfirmationPayload(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") || !strings.Contains(trimmed, "confirmation_required") {
		return content
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return content
	}
	if payloadHasConfirmationRequired(payload) {
		return ""
	}
	return content
}

func payloadHasConfirmationRequired(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "confirmation_required" && jsonTruthy(child) {
				return true
			}
			if payloadHasConfirmationRequired(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if payloadHasConfirmationRequired(child) {
				return true
			}
		}
	}
	return false
}

func jsonTruthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return truthyOption(typed)
	default:
		return false
	}
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
