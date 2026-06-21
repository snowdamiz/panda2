package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/alerts"
	"github.com/sn0w/panda2/internal/composed"
	"github.com/sn0w/panda2/internal/music"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/scheduler"
	"github.com/sn0w/panda2/internal/store"
	toolsvc "github.com/sn0w/panda2/internal/tools"
)

type MusicAdminService interface {
	ConfigureSettings(ctx context.Context, guildID string, update music.SettingsUpdate) (music.SettingsSnapshot, error)
}

func (r *Router) adminPermissionForSubcommand(subcommand string) (func(context.Context, admin.AssistantAccessRequest) (bool, error), string) {
	switch subcommand {
	case "status":
		return r.admin.CanReadConfig, "You do not have permission to read Panda admin status."
	case "audit":
		return r.admin.CanReadAudit, "You do not have permission to read Panda audit events."
	case "feedback":
		return r.admin.CanReadUsage, "You do not have permission to read Panda feedback trends."
	default:
		return r.admin.CanWriteConfig, "Only the Panda owner, server owner or administrator, or a delegated config role can use that admin command."
	}
}

func (r *Router) handleReminder(ctx context.Context, request Request) Response {
	if request.GuildID == "" {
		return Response{Content: "Reminders must be used inside a Discord server.", Ephemeral: true}
	}
	if r.scheduler == nil {
		return Response{Content: "Reminders are not configured for this runtime.", Ephemeral: true}
	}
	if denied := r.ensureAssistantAllowed(ctx, request); denied.Content != "" {
		return denied
	}
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "create")))
	switch action {
	case "create", "add":
		return r.createReminder(ctx, request, false)
	case "list":
		return r.listReminders(ctx, request)
	case "cancel", "delete":
		return r.cancelSchedule(ctx, request)
	case "complete", "done":
		return r.completeSchedule(ctx, request)
	case "snooze":
		return r.snoozeSchedule(ctx, request)
	default:
		return Response{Content: "`action` must be `create`, `list`, `cancel`, `complete`, or `snooze`.", Ephemeral: true}
	}
}

func (r *Router) handleSchedule(ctx context.Context, request Request) Response {
	if request.GuildID == "" {
		return Response{Content: "Schedules must be used inside a Discord server.", Ephemeral: true}
	}
	if r.scheduler == nil {
		return Response{Content: "Scheduling is not configured for this runtime.", Ephemeral: true}
	}
	allowed, err := r.admin.CanInvokeComposedTool(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: "You do not have permission to schedule composed tools.", Ephemeral: true}
	}
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "list")))
	switch action {
	case "list":
		schedules, err := r.scheduler.List(ctx, request.GuildID, "", scheduler.KindComposed, false, 25)
		if err != nil {
			return Response{Content: "Schedule lookup failed.", Ephemeral: true}
		}
		return Response{Content: renderSchedules("Composed schedules", schedules), Ephemeral: true}
	case "create", "add":
		toolName := strings.TrimSpace(request.Options["tool_name"])
		if toolName == "" {
			return Response{Content: "`tool_name` is required.", Ephemeral: true}
		}
		if denied := r.ensureComposedScheduleTargetAllowed(ctx, request, toolName); denied.Content != "" {
			return denied
		}
		next, interval, err := parseScheduleTime(request.Options, time.Now().UTC())
		if err != nil {
			return Response{Content: err.Error(), Ephemeral: true}
		}
		input := map[string]any{}
		if raw := strings.TrimSpace(request.Options["input_json"]); raw != "" {
			if err := json.Unmarshal([]byte(raw), &input); err != nil {
				return Response{Content: "`input_json` must be a JSON object.", Ephemeral: true}
			}
		}
		if dryRunRequested(request) {
			return dryRunResponse("composed tool `%s` would be scheduled for %s.", toolName, next.Format(time.RFC3339))
		}
		schedule, err := r.scheduler.CreateComposed(ctx, scheduler.CreateComposedRequest{
			GuildID:     request.GuildID,
			ChannelID:   request.ChannelID,
			OwnerUserID: request.UserID,
			ToolName:    toolName,
			Input:       input,
			NextRunAt:   next,
			Interval:    interval,
		})
		if err != nil {
			return Response{Content: "Composed schedule could not be created.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Scheduled `%s` as schedule `%d` for %s.", toolName, schedule.ID, schedule.NextRunAt.Format(time.RFC3339)), Ephemeral: true}
	case "cancel":
		return r.cancelSchedule(ctx, request)
	default:
		return Response{Content: "`action` must be `list`, `create`, or `cancel`.", Ephemeral: true}
	}
}

func (r *Router) createReminder(ctx context.Context, request Request, followUp bool) Response {
	message := strings.TrimSpace(firstNonEmpty(request.Options["text"], request.Options["message"]))
	if message == "" {
		return Response{Content: "`text` is required.", Ephemeral: true}
	}
	next, interval, err := parseScheduleTime(request.Options, time.Now().UTC())
	if err != nil {
		return Response{Content: err.Error(), Ephemeral: true}
	}
	targetType, targetID := reminderTarget(request)
	if (targetType == scheduler.TargetChannel || targetType == scheduler.TargetRole) && !truthyOption(request.Options["confirm_public"]) {
		return Response{Content: "Public or role-targeted reminders require confirmation. Set `confirm_public` to true after checking the target and reminder text.", Ephemeral: true, Presentation: Presentation{Title: "Confirmation required", Accent: AccentWarning}}
	}
	if dryRunRequested(request) {
		return dryRunResponse("reminder would run at %s for `%s`.", next.Format(time.RFC3339), targetType)
	}
	schedule, err := r.scheduler.CreateReminder(ctx, scheduler.CreateReminderRequest{
		GuildID:         request.GuildID,
		ChannelID:       request.ChannelID,
		OwnerUserID:     request.UserID,
		TargetType:      targetType,
		TargetID:        targetID,
		Message:         message,
		NextRunAt:       next,
		Interval:        interval,
		SourceMessageID: firstNonEmpty(request.Options["reply_message_id"], request.RequestID),
		FollowUp:        followUp,
	})
	if err != nil {
		return Response{Content: "Reminder could not be created.", Ephemeral: true}
	}
	return Response{Content: fmt.Sprintf("Created %s `%d` for %s.", schedule.Kind, schedule.ID, schedule.NextRunAt.Format(time.RFC3339)), Ephemeral: true, Presentation: Presentation{Title: "Reminder created", Accent: AccentSuccess}}
}

func (r *Router) listReminders(ctx context.Context, request Request) Response {
	schedules, err := r.scheduler.List(ctx, request.GuildID, request.UserID, "", false, 25)
	if err != nil {
		return Response{Content: "Reminder lookup failed.", Ephemeral: true}
	}
	return Response{Content: renderSchedules("Your reminders and follow-ups", schedules), Ephemeral: true}
}

func (r *Router) cancelSchedule(ctx context.Context, request Request) Response {
	id := uintOption(request.Options["id"])
	if id == 0 {
		return Response{Content: "`id` is required.", Ephemeral: true}
	}
	if denied := r.ensureScheduleMutationAllowed(ctx, request, id); denied.Content != "" {
		return denied
	}
	if err := r.scheduler.Cancel(ctx, request.GuildID, request.UserID, id); err != nil {
		return scheduleMutationError(err, "Schedule could not be cancelled.")
	}
	return Response{Content: fmt.Sprintf("Cancelled schedule `%d`.", id), Ephemeral: true}
}

func (r *Router) completeSchedule(ctx context.Context, request Request) Response {
	id := uintOption(request.Options["id"])
	if id == 0 {
		return Response{Content: "`id` is required.", Ephemeral: true}
	}
	if denied := r.ensureScheduleMutationAllowed(ctx, request, id); denied.Content != "" {
		return denied
	}
	if err := r.scheduler.Complete(ctx, request.GuildID, request.UserID, id); err != nil {
		return scheduleMutationError(err, "Schedule could not be completed.")
	}
	return Response{Content: fmt.Sprintf("Completed schedule `%d`.", id), Ephemeral: true}
}

func (r *Router) snoozeSchedule(ctx context.Context, request Request) Response {
	id := uintOption(request.Options["id"])
	if id == 0 {
		return Response{Content: "`id` is required.", Ephemeral: true}
	}
	if denied := r.ensureScheduleMutationAllowed(ctx, request, id); denied.Content != "" {
		return denied
	}
	next, _, err := parseScheduleTime(request.Options, time.Now().UTC())
	if err != nil {
		return Response{Content: err.Error(), Ephemeral: true}
	}
	if err := r.scheduler.Snooze(ctx, request.GuildID, request.UserID, id, next); err != nil {
		return scheduleMutationError(err, "Schedule could not be snoozed.")
	}
	return Response{Content: fmt.Sprintf("Snoozed schedule `%d` until %s.", id, next.Format(time.RFC3339)), Ephemeral: true}
}

func (r *Router) ensureScheduleMutationAllowed(ctx context.Context, request Request, id uint) Response {
	schedule, ok, err := r.scheduler.Get(ctx, request.GuildID, id)
	if err != nil {
		return Response{Content: "Schedule lookup failed.", Ephemeral: true}
	}
	if !ok {
		return Response{Content: "That schedule was not found.", Ephemeral: true}
	}
	if schedule.OwnerUserID == request.UserID {
		return Response{}
	}
	hasControl, err := r.admin.HasGuildControl(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if hasControl {
		return Response{}
	}
	return Response{Content: "You can only manage your own schedules unless you have Panda admin access.", Ephemeral: true}
}

func (r *Router) ensureComposedScheduleTargetAllowed(ctx context.Context, request Request, toolName string) Response {
	if r.composed == nil {
		return Response{Content: "Composed tools are not configured for this runtime.", Ephemeral: true}
	}
	allowed, err := r.composed.CanInvoke(ctx, request.GuildID, toolName, r.toolAccess(ctx, request, toolsvc.ToolPolicyWriteConfirmed), composed.InvocationScheduled)
	if err != nil {
		return Response{Content: "Composed tool lookup failed.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: fmt.Sprintf("I could not find an approved composed tool named `%s` that can be scheduled, or you do not have access to it.", toolName), Ephemeral: true}
	}
	return Response{}
}

func (r *Router) handleAdminStatus(ctx context.Context, request Request) Response {
	config, err := r.admin.Config(ctx, request.GuildID)
	if err != nil {
		return Response{Content: "Status lookup failed.", Ephemeral: true}
	}
	roles, _ := r.admin.ListRolePermissions(ctx, request.GuildID)
	channelRules, _ := r.admin.ListChannelRules(ctx, request.GuildID)
	toolRoles, _ := r.admin.ListToolRoles(ctx, request.GuildID)
	budgets, _ := r.admin.ListBudgetLimits(ctx, request.GuildID)
	var queueDepth int64
	openRouterStatus := "unknown"
	discordStatus := "unknown"
	if r.ops != nil {
		if health, err := r.ops.Health(ctx); err == nil {
			queueDepth = health.QueuedJobs
			openRouterStatus = health.OpenRouter
			discordStatus = health.Discord
		}
	}
	var scheduleStats repository.ScheduleStats
	if r.scheduler != nil {
		scheduleStats, _ = r.scheduler.Stats(ctx, request.GuildID)
	}
	alertRules := []store.AlertRule{}
	if r.alerts != nil {
		alertRules, _ = r.alerts.List(ctx, request.GuildID)
	}
	lines := []string{
		"Admin status:",
		fmt.Sprintf("- assistant: `%t`", config.AssistantEnabled),
		fmt.Sprintf("- memory: `%t`", config.MemoryEnabled),
		fmt.Sprintf("- default model: `%s`", config.DefaultModel),
		fmt.Sprintf("- fallback models: `%d`", fallbackModelCount(config.FallbackModels)),
		fmt.Sprintf("- tool policy: `%s`", config.ToolPolicy),
		fmt.Sprintf("- discord: `%s`", discordStatus),
		fmt.Sprintf("- openrouter: `%s`", openRouterStatus),
		fmt.Sprintf("- role mappings: `%d`", len(roles)),
		fmt.Sprintf("- tool role grants: `%d`", len(toolRoles)),
		fmt.Sprintf("- channel rules: `%d`", len(channelRules)),
		fmt.Sprintf("- budget limits: `%d`", len(budgets)),
		fmt.Sprintf("- queued jobs: `%d`", queueDepth),
		fmt.Sprintf("- schedules: active `%d`, paused `%d`, failed `%d`", scheduleStats.Active, scheduleStats.Paused, scheduleStats.FailedRuns),
		fmt.Sprintf("- alert packs: `%d` configured", len(alertRules)),
	}
	warnings := adminStatusWarnings(config, roles, channelRules, scheduleStats, alertRules)
	if r.setup != nil {
		if runtime, err := r.setup.CheckSetup(ctx, request.GuildID, request.ChannelID); err == nil {
			if !runtime.DiscordConfigured {
				warnings = append(warnings, "Discord credentials are not configured.")
			}
			if !runtime.Connected {
				warnings = append(warnings, "Discord gateway is not connected.")
			}
			warnings = append(warnings, runtime.Warnings...)
		}
	}
	if len(warnings) > 0 {
		lines = append(lines, "\nActionable warnings:")
		for _, warning := range warnings {
			lines = append(lines, "- "+warning)
		}
	}
	return Response{Content: strings.Join(lines, "\n"), Ephemeral: true, Presentation: Presentation{Title: "Admin status", Accent: AccentInfo}}
}

func (r *Router) handleAdminSetup(ctx context.Context, request Request) Response {
	dryRun := dryRunRequested(request)
	var actions []string
	if roleID := strings.TrimSpace(firstNonEmpty(request.Options["admin_role_id"], request.Options["role_id"])); roleID != "" {
		actions = append(actions, "admin role")
		if !dryRun {
			if _, err := r.admin.ApplyRoleProfile(ctx, request.GuildID, request.UserID, roleID, admin.RoleProfileAdmin); err != nil {
				return Response{Content: "Admin role could not be configured.", Ephemeral: true}
			}
		}
	}
	if roleID := strings.TrimSpace(request.Options["moderator_role_id"]); roleID != "" {
		actions = append(actions, "moderator role")
		if !dryRun {
			if _, err := r.admin.ApplyRoleProfile(ctx, request.GuildID, request.UserID, roleID, admin.RoleProfileModerator); err != nil {
				return Response{Content: "Moderator role could not be configured.", Ephemeral: true}
			}
		}
	}
	if channelID := strings.TrimSpace(request.Options["channel_id"]); channelID != "" {
		actions = append(actions, "allowed channel")
		if !dryRun {
			if _, err := r.admin.SetChannelRule(ctx, request.GuildID, request.UserID, channelID, "allow"); err != nil {
				return Response{Content: "Default channel could not be configured.", Ephemeral: true}
			}
		}
	}
	status := r.handleAdminStatus(ctx, request)
	prefix := "Setup checklist"
	if len(actions) > 0 {
		prefix += " (" + strings.Join(actions, ", ") + ")"
	}
	if dryRun {
		prefix += " dry run"
	} else if len(actions) > 0 {
		prefix += " saved"
	}
	status.Content = prefix + ":\n" + status.Content
	status.Presentation.Title = "Setup"
	return status
}

func (r *Router) handleAdminAlerts(ctx context.Context, request Request) Response {
	if r.alerts == nil {
		return Response{Content: "Alert packs are not configured for this runtime.", Ephemeral: true}
	}
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "list")))
	pack := firstNonEmpty(request.Options["pack"], alerts.PackSecurity)
	switch action {
	case "preview":
		return Response{Content: renderAlertPreviews(r.alerts.Previews()), Ephemeral: true}
	case "list":
		rules, err := r.alerts.List(ctx, request.GuildID)
		if err != nil {
			return Response{Content: "Alert pack lookup failed.", Ephemeral: true}
		}
		return Response{Content: renderAlertRules(rules), Ephemeral: true}
	case "enable":
		channelID := strings.TrimSpace(firstNonEmpty(request.Options["channel_id"], request.ChannelID))
		if channelID == "" {
			return Response{Content: "Choose a channel for alerts.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("alert pack `%s` would post to `%s`.", pack, channelID)
		}
		rule, err := r.alerts.Enable(ctx, request.GuildID, request.UserID, pack, channelID, 5*time.Minute)
		if err != nil {
			return Response{Content: "Alert pack could not be enabled.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Enabled `%s` alerts in `%s`.", rule.Pack, rule.ChannelID), Ephemeral: true}
	case "disable":
		if err := r.alerts.Disable(ctx, request.GuildID, request.UserID, pack); err != nil {
			if err == repository.ErrNotFound {
				return Response{Content: "That alert pack was not configured.", Ephemeral: true}
			}
			return Response{Content: "Alert pack could not be disabled.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Disabled `%s` alerts.", pack), Ephemeral: true}
	case "test":
		if err := r.alerts.Test(ctx, request.GuildID, pack); err != nil {
			return Response{Content: "Alert test could not be sent. Enable the pack first.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Sent `%s` alert test.", pack), Ephemeral: true}
	default:
		return Response{Content: "`action` must be `preview`, `list`, `enable`, `disable`, or `test`.", Ephemeral: true}
	}
}

func (r *Router) handleAdminFeedback(ctx context.Context, request Request) Response {
	if r.feedback == nil {
		return Response{Content: "Feedback is not configured for this runtime.", Ephemeral: true}
	}
	summary, err := r.feedback.Summary(ctx, request.GuildID, 30*24*time.Hour)
	if err != nil {
		return Response{Content: "Feedback lookup failed.", Ephemeral: true}
	}
	if len(summary.Rows) == 0 {
		return Response{Content: "No feedback recorded in the last 30 days.", Ephemeral: true}
	}
	lines := []string{"Feedback in the last 30 days:"}
	for _, row := range summary.Rows {
		lines = append(lines, fmt.Sprintf("- `%s`: %d", row.Rating, row.Count))
	}
	return Response{Content: strings.Join(lines, "\n"), Ephemeral: true}
}

func (r *Router) handleAdminMusic(ctx context.Context, request Request) Response {
	adminMusic, ok := r.music.(MusicAdminService)
	if !ok {
		return Response{Content: "Music settings are not configured for this runtime.", Ephemeral: true}
	}
	update := music.SettingsUpdate{}
	if raw := strings.TrimSpace(request.Options["loop_mode"]); raw != "" {
		update.LoopMode = raw
	}
	if raw := strings.TrimSpace(request.Options["default_volume"]); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return Response{Content: "`default_volume` must be a number.", Ephemeral: true}
		}
		update.DefaultVolume = value
		update.DefaultVolumeSet = true
	}
	if _, ok := request.Options["dj_role_id"]; ok {
		update.DJRoleID = strings.TrimSpace(request.Options["dj_role_id"])
		update.DJRoleSet = true
	}
	if raw := strings.TrimSpace(request.Options["vote_skip_threshold"]); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return Response{Content: "`vote_skip_threshold` must be a number between 0 and 1.", Ephemeral: true}
		}
		update.VoteSkipThreshold = value
		update.VoteSkipThresholdSet = true
	}
	if dryRunRequested(request) {
		return dryRunResponse("music settings would be updated.")
	}
	settings, err := adminMusic.ConfigureSettings(ctx, request.GuildID, update)
	if err != nil {
		return Response{Content: "Music settings could not be updated: " + err.Error(), Ephemeral: true}
	}
	return Response{Content: fmt.Sprintf("Music settings: loop `%s`, volume `%d%%`, DJ role `%s`, vote skip threshold `%.2f`.", settings.LoopMode, settings.DefaultVolume, firstNonEmpty(settings.DJRoleID, "not configured"), settings.VoteSkipThreshold), Ephemeral: true}
}

func parseScheduleTime(options map[string]string, now time.Time) (time.Time, time.Duration, error) {
	return scheduler.ParseTimeOptions(options, now)
}

func reminderTarget(request Request) (string, string) {
	target := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["target"], "me")))
	switch target {
	case "us", "channel", "public":
		return scheduler.TargetChannel, request.ChannelID
	case "role":
		return scheduler.TargetRole, strings.TrimSpace(firstNonEmpty(request.Options["role_id"], request.Options["role"]))
	default:
		return scheduler.TargetUser, request.UserID
	}
}

func renderSchedules(title string, schedules []store.Schedule) string {
	if len(schedules) == 0 {
		return title + ":\n- none"
	}
	lines := []string{title + ":"}
	for _, schedule := range schedules {
		recurring := ""
		if schedule.ScheduleType == scheduler.ScheduleRecurring {
			recurring = fmt.Sprintf(" every %s", (time.Duration(schedule.IntervalSeconds) * time.Second).String())
		}
		lines = append(lines, fmt.Sprintf("- `%d` %s %s%s (%s)", schedule.ID, scheduleSubject(schedule), schedule.NextRunAt.Format(time.RFC3339), recurring, firstNonEmpty(schedule.LastStatus, schedule.Status)))
	}
	return strings.Join(lines, "\n")
}

func scheduleSubject(schedule store.Schedule) string {
	switch schedule.Kind {
	case scheduler.KindComposed:
		if toolName := composedScheduleToolName(schedule); toolName != "" {
			return fmt.Sprintf("composed `%s`", toolName)
		}
	case scheduler.KindReminder, scheduler.KindFollowUp:
		if schedule.Title != "" {
			return fmt.Sprintf("`%s` %s", schedule.Kind, schedule.Title)
		}
	}
	return fmt.Sprintf("`%s`", schedule.Kind)
}

func composedScheduleToolName(schedule store.Schedule) string {
	if schedule.Kind != scheduler.KindComposed {
		return ""
	}
	var payload scheduler.ComposedPayload
	if err := json.Unmarshal([]byte(schedule.Payload), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.ToolName)
}

func scheduleMutationError(err error, fallback string) Response {
	if err == repository.ErrNotFound {
		return Response{Content: "That schedule was not found.", Ephemeral: true}
	}
	return Response{Content: fallback, Ephemeral: true}
}

func adminStatusWarnings(config store.GuildConfig, roles []store.GuildRole, channelRules []store.GuildChannelRule, stats repository.ScheduleStats, alerts []store.AlertRule) []string {
	var warnings []string
	if !config.AssistantEnabled {
		warnings = append(warnings, "Assistant responses are disabled.")
	}
	if config.DefaultModel == "" {
		warnings = append(warnings, "No default model is configured.")
	}
	if len(roleIDsForPermission(roles, admin.PermissionAdminBadge)) == 0 {
		warnings = append(warnings, "No Panda admin role is configured; only server admins/owners can manage Panda.")
	}
	hasAllow := false
	for _, rule := range channelRules {
		if rule.Rule == "allow" {
			hasAllow = true
			break
		}
	}
	if !hasAllow {
		warnings = append(warnings, "No allow-listed assistant channels are configured.")
	}
	if stats.FailedRuns > 0 {
		warnings = append(warnings, fmt.Sprintf("%d schedule(s) have recent failed runs.", stats.FailedRuns))
	}
	if len(alerts) == 0 {
		warnings = append(warnings, "No recommended alert packs are enabled.")
	}
	return warnings
}

func renderAlertPreviews(previews []alerts.Preview) string {
	lines := []string{"Alert pack previews:"}
	for _, preview := range previews {
		lines = append(lines, fmt.Sprintf("- `%s` risk `%s`: %s (%d event types)", preview.Pack, preview.Risk, preview.Description, len(preview.EventTypes)))
	}
	return strings.Join(lines, "\n")
}

func renderAlertRules(rules []store.AlertRule) string {
	if len(rules) == 0 {
		return "No alert packs configured."
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Pack < rules[j].Pack })
	lines := []string{"Alert packs:"}
	for _, rule := range rules {
		lines = append(lines, fmt.Sprintf("- `%s`: enabled `%t`, channel `%s`, cooldown `%ds`", rule.Pack, rule.Enabled, rule.ChannelID, rule.CooldownSeconds))
	}
	return strings.Join(lines, "\n")
}

func uintOption(raw string) uint {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}
	return uint(value)
}
