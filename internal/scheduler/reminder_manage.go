package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
	toolsvc "github.com/sn0w/panda2/internal/tools"
)

func (s *Service) ManageReminder(ctx context.Context, request toolsvc.ReminderManagementRequest) (any, error) {
	if s == nil || s.schedules == nil {
		return nil, fmt.Errorf("reminder manager is not configured")
	}
	action := strings.ToLower(strings.TrimSpace(request.Action))
	if action == "" {
		return nil, fmt.Errorf("action is required")
	}
	if strings.TrimSpace(request.GuildID) == "" {
		return nil, fmt.Errorf("guild_id is required")
	}
	switch action {
	case "list":
		return s.manageReminderList(ctx, request)
	case "create":
		return s.manageReminderCreate(ctx, request)
	case "cancel", "complete", "snooze":
		return s.manageReminderMutation(ctx, request, action)
	default:
		return nil, fmt.Errorf("action must be create, list, cancel, complete, or snooze")
	}
}

func (s *Service) manageReminderList(ctx context.Context, request toolsvc.ReminderManagementRequest) (any, error) {
	reminders, err := s.List(ctx, request.GuildID, request.ActorID, KindReminder, request.IncludeDisabled, 25)
	if err != nil {
		return nil, err
	}
	followUps, err := s.List(ctx, request.GuildID, request.ActorID, KindFollowUp, request.IncludeDisabled, 25)
	if err != nil {
		return nil, err
	}
	schedules := append(reminders, followUps...)
	sort.Slice(schedules, func(i, j int) bool {
		if schedules[i].NextRunAt.Equal(schedules[j].NextRunAt) {
			return schedules[i].ID < schedules[j].ID
		}
		return schedules[i].NextRunAt.Before(schedules[j].NextRunAt)
	})
	if len(schedules) > 25 {
		schedules = schedules[:25]
	}
	return map[string]any{"result": map[string]any{
		"action":    "list",
		"schedules": reminderPayloads(schedules),
		"count":     len(schedules),
	}}, nil
}

func (s *Service) manageReminderCreate(ctx context.Context, request toolsvc.ReminderManagementRequest) (any, error) {
	message := strings.TrimSpace(request.Message)
	if message == "" && request.FollowUp {
		message = "Follow up: nobody answered this yet."
	}
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}
	targetType, targetID, public, err := reminderToolTarget(request)
	if err != nil {
		return nil, err
	}
	if public {
		return nil, fmt.Errorf("public, channel, or role reminders require explicit confirmation; use the /reminder command with confirm_public after checking the target and text")
	}
	next, interval, err := ParseTimeOptions(map[string]string{
		"when":  request.When,
		"in":    request.In,
		"every": request.Every,
	}, s.now().UTC())
	if err != nil {
		return nil, err
	}
	preview := map[string]any{
		"message":          message,
		"target_type":      targetType,
		"target_id":        targetID,
		"next_run_at":      next.UTC().Format(time.RFC3339),
		"interval_seconds": int(interval.Seconds()),
	}
	if request.DryRun {
		return map[string]any{"result": map[string]any{
			"dry_run": true,
			"action":  "reminder.create",
			"preview": preview,
		}}, nil
	}
	schedule, err := s.CreateReminder(ctx, CreateReminderRequest{
		GuildID:         request.GuildID,
		ChannelID:       request.ChannelID,
		OwnerUserID:     request.ActorID,
		TargetType:      targetType,
		TargetID:        targetID,
		Message:         message,
		NextRunAt:       next,
		Interval:        interval,
		SourceMessageID: request.RequestID,
		FollowUp:        request.FollowUp,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": map[string]any{
		"action":   "create",
		"schedule": reminderPayload(schedule),
	}}, nil
}

func (s *Service) manageReminderMutation(ctx context.Context, request toolsvc.ReminderManagementRequest, action string) (any, error) {
	schedule, err := s.reminderScheduleForMutation(ctx, request)
	if err != nil {
		return nil, err
	}
	preview := reminderPayload(schedule)
	if request.DryRun {
		return map[string]any{"result": map[string]any{
			"dry_run": true,
			"action":  "reminder." + action,
			"preview": preview,
		}}, nil
	}
	switch action {
	case "cancel":
		err = s.Cancel(ctx, request.GuildID, request.ActorID, schedule.ID)
	case "complete":
		err = s.Complete(ctx, request.GuildID, request.ActorID, schedule.ID)
	case "snooze":
		var next time.Time
		next, _, err = ParseTimeOptions(map[string]string{
			"when": request.When,
			"in":   request.In,
		}, s.now().UTC())
		if err == nil {
			err = s.Snooze(ctx, request.GuildID, request.ActorID, schedule.ID, next)
			preview["next_run_at"] = next.UTC().Format(time.RFC3339)
		}
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": map[string]any{
		"action":   action,
		"schedule": preview,
	}}, nil
}

func (s *Service) reminderScheduleForMutation(ctx context.Context, request toolsvc.ReminderManagementRequest) (store.Schedule, error) {
	if request.ScheduleID == 0 {
		return store.Schedule{}, fmt.Errorf("schedule_id is required")
	}
	schedule, ok, err := s.Get(ctx, request.GuildID, request.ScheduleID)
	if err != nil {
		return store.Schedule{}, err
	}
	if !ok {
		return store.Schedule{}, repository.ErrNotFound
	}
	if schedule.Kind != KindReminder && schedule.Kind != KindFollowUp {
		return store.Schedule{}, fmt.Errorf("schedule %d is not a reminder", request.ScheduleID)
	}
	if schedule.OwnerUserID != request.ActorID && !reminderToolCanManageOthers(request.Access) {
		return store.Schedule{}, fmt.Errorf("you can only manage your own reminders")
	}
	return schedule, nil
}

func reminderToolTarget(request toolsvc.ReminderManagementRequest) (string, string, bool, error) {
	target := strings.ToLower(strings.TrimSpace(request.Target))
	targetID := strings.TrimSpace(request.TargetID)
	switch target {
	case "", "me", "myself", "user":
		if targetID == "" {
			targetID = request.ActorID
		}
		if targetID != request.ActorID {
			return TargetUser, targetID, true, nil
		}
		return TargetUser, targetID, false, nil
	case "channel", "this channel", "public", "us":
		if targetID == "" {
			targetID = request.ChannelID
		}
		return TargetChannel, targetID, true, nil
	case "role":
		if targetID == "" {
			return "", "", false, fmt.Errorf("target_id is required for role reminders")
		}
		return TargetRole, targetID, true, nil
	default:
		return "", "", false, fmt.Errorf("target must be me, user, channel, or role")
	}
}

func reminderToolCanManageOthers(access toolsvc.ToolAccess) bool {
	return access.HasAnyPermission(
		admin.PermissionAdminConfigWrite,
		admin.PermissionOwnerOps,
	)
}

func reminderPayloads(schedules []store.Schedule) []map[string]any {
	payloads := make([]map[string]any, 0, len(schedules))
	for _, schedule := range schedules {
		payloads = append(payloads, reminderPayload(schedule))
	}
	return payloads
}

func reminderPayload(schedule store.Schedule) map[string]any {
	payload := schedulePayload(schedule)
	if schedule.Kind != KindReminder && schedule.Kind != KindFollowUp {
		return payload
	}
	var reminder ReminderPayload
	if err := json.Unmarshal([]byte(schedule.Payload), &reminder); err == nil {
		payload["message"] = reminder.Message
	}
	return payload
}
