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
	"github.com/sn0w/panda2/internal/billing"
	"github.com/sn0w/panda2/internal/composed"
	"github.com/sn0w/panda2/internal/features"
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
	case "status", "billing":
		return r.admin.CanReadConfig, "You do not have permission to read Panda admin status."
	case "audit":
		return r.admin.CanReadAudit, "You do not have permission to read Panda audit events."
	case "feedback":
		return r.admin.CanReadUsage, "You do not have permission to read Panda feedback trends."
	default:
		return r.admin.CanWriteConfig, "Only the Panda owner, server owner or administrator, or a delegated config role or user can use that admin command."
	}
}

func adminFeatureForSubcommand(subcommand string) string {
	switch strings.ToLower(strings.TrimSpace(subcommand)) {
	case "feature", "features":
		return ""
	case "", "status", "setup", "behavior", "prompt", "soul", "billing", "enable", "disable", "quiet", "quiet-mode", "quiet_mode", "timeout", "alerts", "alert":
		return features.AdminSetup
	case "role", "user", "member", "tool", "channel", "channels":
		return features.AdminAccessControl
	case "member-role", "member_role":
		return features.DiscordRoleManagement
	case "audit", "feedback":
		return features.AdminAudit
	case "music":
		return features.Music
	default:
		return features.AdminSetup
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
			if response, ok := billingErrorResponseIfBilling(err); ok {
				return response
			}
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
		return Response{Content: "Public or role-targeted reminders require confirmation. Ask Panda in chat to prepare the reminder, then approve the confirmation button after checking the target and text.", Ephemeral: true, Presentation: Presentation{Title: "Confirmation required", Accent: AccentWarning}}
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
		if response, ok := billingErrorResponseIfBilling(err); ok {
			return response
		}
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
	users, _ := r.admin.ListUserPermissions(ctx, request.GuildID)
	channelRules, _ := r.admin.ListChannelRules(ctx, request.GuildID)
	toolRoles, _ := r.admin.ListToolRoles(ctx, request.GuildID)
	budgets, _ := r.admin.ListBudgetLimits(ctx, request.GuildID)
	var queueDepth int64
	aiServiceStatus := "unknown"
	imageServiceStatus := "unknown"
	discordStatus := "unknown"
	if r.ops != nil {
		if health, err := r.ops.Health(ctx); err == nil {
			queueDepth = health.QueuedJobs
			aiServiceStatus = health.AIService
			imageServiceStatus = health.ImageService
			discordStatus = health.Discord
		}
	}
	billingStatus := "not configured"
	creditBalance := "not available"
	retention := 0
	if r.billing != nil {
		if entitlement, err := r.billing.Resolve(ctx, request.GuildID); err == nil {
			billingStatus = fmt.Sprintf("%s (%s)", entitlement.Pack.DisplayName, entitlement.Status)
			creditBalance = billing.FormatCredits(entitlement.AvailableCredits, entitlement.ReservedCredits)
			retention = entitlement.RetentionDays
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
	featureStatus := "feature store not configured"
	featurePermissions := "not available"
	if r.features != nil {
		states, err := r.features.Status(ctx, request.GuildID)
		if err != nil {
			featureStatus = "lookup failed"
			featurePermissions = "lookup failed"
		} else {
			enabledIDs := enabledFeatureIDs(states)
			featureStatus = renderEnabledFeatureLabels(enabledIDs)
			if permissions, err := features.PermissionNamesForFeatures(enabledIDs); err == nil {
				bitfield, _ := features.PermissionBitfield(permissions)
				featurePermissions = fmt.Sprintf("%s (bitfield `%d`)", strings.Join(permissions, ", "), bitfield)
				if len(permissions) == 0 {
					featurePermissions = "none"
				}
			}
		}
	}
	lines := []string{
		"Admin status:",
		fmt.Sprintf("- pack: `%s`", billingStatus),
		fmt.Sprintf("- credits: `%s`", creditBalance),
		fmt.Sprintf("- retention: `%d days`", retention),
		fmt.Sprintf("- assistant: `%t`", config.AssistantEnabled),
		fmt.Sprintf("- memory: `%t`", config.MemoryEnabled),
		fmt.Sprintf("- answer length: `%s`", answerLengthFromMaxTokens(config.MaxResponseTokens)),
		fmt.Sprintf("- tool policy: `%s`", config.ToolPolicy),
		fmt.Sprintf("- discord: `%s`", discordStatus),
		fmt.Sprintf("- AI service: `%s`", aiServiceStatus),
		fmt.Sprintf("- image service: `%s`", imageServiceStatus),
		fmt.Sprintf("- role mappings: `%d`", len(roles)),
		fmt.Sprintf("- user mappings: `%d`", len(users)),
		fmt.Sprintf("- tool role grants: `%d`", len(toolRoles)),
		fmt.Sprintf("- channel rules: `%d`", len(channelRules)),
		fmt.Sprintf("- budget limits: `%d`", len(budgets)),
		fmt.Sprintf("- enabled features: %s", featureStatus),
		fmt.Sprintf("- enabled feature Discord permissions: %s", featurePermissions),
		fmt.Sprintf("- queued jobs: `%d`", queueDepth),
		fmt.Sprintf("- schedules: active `%d`, paused `%d`, failed `%d`", scheduleStats.Active, scheduleStats.Paused, scheduleStats.FailedRuns),
		fmt.Sprintf("- alert packs: `%d` configured", len(alertRules)),
	}
	warnings := adminStatusWarnings(config, roles, users, channelRules, scheduleStats, alertRules)
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

func enabledFeatureIDs(states []repository.GuildFeatureState) []string {
	ids := make([]string, 0, len(states))
	for _, state := range states {
		if state.Enabled {
			ids = append(ids, state.FeatureID)
		}
	}
	sort.Strings(ids)
	return ids
}

func renderEnabledFeatureLabels(ids []string) string {
	if len(ids) == 0 {
		return "`none`"
	}
	labels := make([]string, 0, len(ids))
	for _, id := range ids {
		feature, ok := features.Lookup(id)
		if !ok || strings.TrimSpace(feature.Label) == "" {
			labels = append(labels, "`"+id+"`")
			continue
		}
		labels = append(labels, "`"+feature.Label+"`")
	}
	return strings.Join(labels, ", ")
}

func (r *Router) handleAdminFeatures(ctx context.Context, request Request) Response {
	if r.features == nil {
		return Response{Content: "Feature management is not configured for this runtime.", Ephemeral: true}
	}
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "list")))
	if denied := r.ensureAdminFeaturePermission(ctx, request, action); denied.Content != "" {
		return denied
	}
	switch action {
	case "list", "status":
		states, err := r.features.Status(ctx, request.GuildID)
		if err != nil {
			return Response{Content: "Feature status lookup failed.", Ephemeral: true}
		}
		return Response{Content: renderAdminFeatureStatus(states), Ephemeral: true, Presentation: Presentation{Title: "Feature status", Accent: AccentInfo}}
	case "enable", "add":
		return r.handleAdminFeatureEnable(ctx, request)
	case "disable", "remove":
		return r.handleAdminFeatureDisable(ctx, request)
	case "reauthorize", "reauth":
		return r.handleAdminFeatureReauthorize(ctx, request)
	default:
		return Response{Content: "Feature action must be `list`, `enable`, `disable`, or `reauthorize`.", Ephemeral: true}
	}
}

func (r *Router) ensureAdminFeaturePermission(ctx context.Context, request Request, action string) Response {
	if request.IsGuildAdmin || request.IsOwner {
		return Response{}
	}
	check := r.admin.CanReadConfig
	denial := "You do not have permission to view Panda features."
	switch action {
	case "enable", "add", "disable", "remove", "reauthorize", "reauth":
		check = r.admin.CanWriteConfig
		denial = "You do not have permission to manage Panda features."
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

func (r *Router) handleAdminFeatureEnable(ctx context.Context, request Request) Response {
	requested, err := requestedPublicFeatureIDs(request)
	if err != nil {
		return Response{Content: err.Error(), Ephemeral: true}
	}
	current, err := r.currentEnabledFeatureIDs(ctx, request.GuildID)
	if err != nil {
		return Response{Content: "Feature status lookup failed.", Ephemeral: true}
	}
	target, err := expandedFeatureUnion(current, requested)
	if err != nil {
		return Response{Content: "Feature selection could not be expanded.", Ephemeral: true}
	}
	newPermissions := newDiscordPermissions(current, target)
	if dryRunRequested(request) {
		return Response{Content: renderFeatureEnablePreview(target, newPermissions), Ephemeral: true, Presentation: Presentation{Title: "Feature enable preview", Accent: AccentInfo}}
	}
	if len(newPermissions) == 0 {
		if err := r.features.ReplaceGuildFeatures(ctx, request.GuildID, target, "admin_feature_change", request.UserID, time.Now().UTC()); err != nil {
			return Response{Content: "Feature update failed.", Ephemeral: true}
		}
		r.admin.RecordFeatureAudit(ctx, request.GuildID, request.UserID, "guild_features.enable", map[string]any{
			"requested_features": requested,
			"enabled_features":   target,
			"reauthorization":    false,
		})
		return Response{Content: fmt.Sprintf("Enabled features: %s.", renderEnabledFeatureLabels(target)), Ephemeral: true, Presentation: Presentation{Title: "Features enabled", Accent: AccentSuccess}}
	}
	result, response := r.createFeatureReauthorizationIntent(ctx, request, target, requested, "enable")
	if response.Content != "" {
		return response
	}
	return Response{
		Content:   fmt.Sprintf("Reauthorization is required before enabling %s. New Discord permissions requested: %s. The feature set will activate after Discord redirects back to Panda.", renderEnabledFeatureLabels(requested), renderPermissionNames(newPermissions)),
		Ephemeral: true,
		Presentation: Presentation{
			Title:  "Reauthorization required",
			Accent: AccentWarning,
			URL:    result.AuthorizeURL,
		},
		Actions: []Action{{Label: "Reauthorize Panda", URL: result.AuthorizeURL}},
	}
}

func (r *Router) handleAdminFeatureDisable(ctx context.Context, request Request) Response {
	requested, err := requestedPublicFeatureIDs(request)
	if err != nil {
		return Response{Content: err.Error(), Ephemeral: true}
	}
	current, err := r.currentEnabledFeatureIDs(ctx, request.GuildID)
	if err != nil {
		return Response{Content: "Feature status lookup failed.", Ephemeral: true}
	}
	target, removed := disableFeaturesAndDependents(current, requested)
	if len(removed) == 0 {
		return Response{Content: "None of the requested features are enabled.", Ephemeral: true}
	}
	if dryRunRequested(request) {
		return Response{Content: fmt.Sprintf("Would disable %s. Remaining enabled features: %s.", renderEnabledFeatureLabels(removed), renderEnabledFeatureLabels(target)), Ephemeral: true, Presentation: Presentation{Title: "Feature disable preview", Accent: AccentWarning}}
	}
	if err := r.features.ReplaceGuildFeatures(ctx, request.GuildID, target, "admin_feature_change", request.UserID, time.Now().UTC()); err != nil {
		return Response{Content: "Feature update failed.", Ephemeral: true}
	}
	r.admin.RecordFeatureAudit(ctx, request.GuildID, request.UserID, "guild_features.disable", map[string]any{
		"requested_features": requested,
		"disabled_features":  removed,
		"enabled_features":   target,
	})
	return Response{Content: fmt.Sprintf("Disabled %s. Remaining enabled features: %s.", renderEnabledFeatureLabels(removed), renderEnabledFeatureLabels(target)), Ephemeral: true, Presentation: Presentation{Title: "Features disabled", Accent: AccentSuccess}}
}

func (r *Router) handleAdminFeatureReauthorize(ctx context.Context, request Request) Response {
	current, err := r.currentEnabledFeatureIDs(ctx, request.GuildID)
	if err != nil {
		return Response{Content: "Feature status lookup failed.", Ephemeral: true}
	}
	if len(current) == 0 {
		return Response{Content: "No features are currently enabled for this server.", Ephemeral: true}
	}
	result, response := r.createFeatureReauthorizationIntent(ctx, request, current, nil, "reauthorize")
	if response.Content != "" {
		return response
	}
	permissions, _ := features.PermissionNamesForFeatures(result.Selection.ExpandedFeatureIDs)
	return Response{
		Content:   fmt.Sprintf("Created a fresh Discord reauthorization link for the enabled feature set. Requested Discord permissions: %s.", renderPermissionNames(permissions)),
		Ephemeral: true,
		Presentation: Presentation{
			Title:  "Reauthorization link ready",
			Accent: AccentInfo,
			URL:    result.AuthorizeURL,
		},
		Actions: []Action{{Label: "Reauthorize Panda", URL: result.AuthorizeURL}},
	}
}

func (r *Router) createFeatureReauthorizationIntent(ctx context.Context, request Request, targetFeatureIDs, requestedFeatureIDs []string, action string) (FeatureInstallIntentResult, Response) {
	if r.install == nil {
		return FeatureInstallIntentResult{}, Response{Content: "Feature reauthorization is not configured for this runtime.", Ephemeral: true}
	}
	result, err := r.install.CreateFeatureInstallIntent(ctx, FeatureInstallIntentRequest{
		FeatureIDs: targetFeatureIDs,
		Source:     "admin_reauthorization",
		Metadata: map[string]any{
			"guild_id":           request.GuildID,
			"actor_id":           request.UserID,
			"action":             action,
			"requested_features": requestedFeatureIDs,
		},
	})
	if err != nil {
		return FeatureInstallIntentResult{}, Response{Content: "Feature reauthorization link could not be created.", Ephemeral: true}
	}
	r.admin.RecordFeatureAudit(ctx, request.GuildID, request.UserID, "guild_features.reauthorization_created", map[string]any{
		"requested_features":  requestedFeatureIDs,
		"target_features":     result.Selection.ExpandedFeatureIDs,
		"discord_permissions": result.Selection.DiscordPermissionNames,
		"permission_bitfield": result.Selection.DiscordPermissionBitfield,
		"expires_at":          result.ExpiresAt.Format(time.RFC3339),
	})
	return result, Response{}
}

func (r *Router) currentEnabledFeatureIDs(ctx context.Context, guildID string) ([]string, error) {
	states, err := r.features.Status(ctx, guildID)
	if err != nil {
		return nil, err
	}
	return enabledFeatureIDs(states), nil
}

func requestedPublicFeatureIDs(request Request) ([]string, error) {
	raw := strings.TrimSpace(firstNonEmpty(request.Options["feature_id"], request.Options["feature"]))
	if raw == "" {
		return nil, fmt.Errorf("`feature_id` is required")
	}
	ids := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t'
	})
	return features.Normalize(ids, true)
}

func expandedFeatureUnion(current, additions []string) ([]string, error) {
	merged := append([]string{}, current...)
	merged = append(merged, additions...)
	return features.Expand(uniqueFeatureIDs(merged), false)
}

func uniqueFeatureIDs(ids []string) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func newDiscordPermissions(current, target []string) []string {
	currentPermissions, _ := features.PermissionNamesForFeatures(current)
	targetPermissions, _ := features.PermissionNamesForFeatures(target)
	currentSet := permissionsFromNames(currentPermissions)
	result := []string{}
	for _, permission := range targetPermissions {
		if _, ok := currentSet[permission]; !ok {
			result = append(result, permission)
		}
	}
	sort.Strings(result)
	return result
}

func disableFeaturesAndDependents(current, disabled []string) ([]string, []string) {
	disabledSet := features.FeatureSet(disabled)
	currentSet := features.FeatureSet(current)
	remaining := []string{}
	removed := []string{}
	for _, id := range uniqueFeatureIDs(current) {
		if _, ok := disabledSet[id]; ok || featureDependsOnAny(id, disabledSet, map[string]bool{}) {
			removed = append(removed, id)
			continue
		}
		if _, ok := currentSet[id]; ok {
			remaining = append(remaining, id)
		}
	}
	sort.Strings(remaining)
	sort.Strings(removed)
	return remaining, removed
}

func featureDependsOnAny(featureID string, disabled map[string]struct{}, visiting map[string]bool) bool {
	feature, ok := features.Lookup(featureID)
	if !ok {
		return false
	}
	if visiting[feature.ID] {
		return false
	}
	visiting[feature.ID] = true
	defer delete(visiting, feature.ID)
	for _, dependency := range feature.Dependencies {
		dependency = strings.ToLower(strings.TrimSpace(dependency))
		if _, ok := disabled[dependency]; ok {
			return true
		}
		if featureDependsOnAny(dependency, disabled, visiting) {
			return true
		}
	}
	return false
}

func renderAdminFeatureStatus(states []repository.GuildFeatureState) string {
	enabled := features.FeatureSet(enabledFeatureIDs(states))
	lines := []string{"Feature status:"}
	for _, feature := range features.Catalog() {
		if !feature.Public && !features.Has(enabled, feature.ID) {
			continue
		}
		status := "disabled"
		if features.Has(enabled, feature.ID) {
			status = "enabled"
		}
		permissions := renderPermissionNames(feature.DiscordPermissions)
		lines = append(lines, fmt.Sprintf("- `%s` (`%s`): `%s`; Discord permissions: %s", feature.Label, feature.ID, status, permissions))
	}
	enabledIDs := enabledFeatureIDs(states)
	permissionNames, _ := features.PermissionNamesForFeatures(enabledIDs)
	bitfield, _ := features.PermissionBitfield(permissionNames)
	lines = append(lines, fmt.Sprintf("\nEnabled permission bitfield: `%d`.", bitfield))
	lines = append(lines, "Ask Panda to reauthorize enabled features to generate a fresh Discord permission link.")
	return strings.Join(lines, "\n")
}

func renderFeatureEnablePreview(target, newPermissions []string) string {
	if len(newPermissions) == 0 {
		return fmt.Sprintf("Would enable features immediately: %s. No new Discord permissions are required.", renderEnabledFeatureLabels(target))
	}
	return fmt.Sprintf("Would request reauthorization for %s. New Discord permissions: %s.", renderEnabledFeatureLabels(target), renderPermissionNames(newPermissions))
}

func renderPermissionNames(names []string) string {
	if len(names) == 0 {
		return "`none`"
	}
	friendly := make([]string, 0, len(names))
	for _, name := range names {
		friendly = append(friendly, "`"+features.PermissionFriendlyName(name)+"`")
	}
	sort.Strings(friendly)
	return strings.Join(friendly, ", ")
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

func adminStatusWarnings(config store.GuildConfig, roles []store.GuildRole, users []store.GuildUserPermission, channelRules []store.GuildChannelRule, stats repository.ScheduleStats, alerts []store.AlertRule) []string {
	var warnings []string
	if !config.AssistantEnabled {
		warnings = append(warnings, "Assistant responses are disabled.")
	}
	if repository.AssistantTimeoutActive(config, time.Now().UTC()) {
		warnings = append(warnings, fmt.Sprintf("Quiet mode is active until %s.", config.AssistantTimeoutUntil.UTC().Format(time.RFC3339)))
	}
	if len(roleIDsForPermission(roles, admin.PermissionAdminBadge)) == 0 && len(userIDsForPermission(users, admin.PermissionAdminBadge)) == 0 {
		warnings = append(warnings, "No Panda admin role or user is configured; only server admins/owners can manage Panda.")
	}
	hasAllow := false
	for _, rule := range channelRules {
		if rule.Rule == "allow" {
			hasAllow = true
			break
		}
	}
	if hasAllow {
		warnings = append(warnings, "Channel allow-list mode is active; non-admin assistant use is limited to allowed channels.")
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
