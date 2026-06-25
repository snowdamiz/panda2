package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/composed"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
	toolsvc "github.com/sn0w/panda2/internal/tools"
)

func (s *Service) ManageSchedule(ctx context.Context, request toolsvc.ScheduleManagementRequest) (any, error) {
	if s == nil || s.schedules == nil {
		return nil, fmt.Errorf("schedule manager is not configured")
	}
	action := strings.ToLower(strings.TrimSpace(request.Action))
	if action == "" {
		return nil, fmt.Errorf("action is required")
	}
	if strings.TrimSpace(request.GuildID) == "" {
		return nil, fmt.Errorf("guild_id is required")
	}
	if !request.Access.HasAnyPermission(admin.PermissionToolComposeInvoke) {
		return nil, fmt.Errorf("missing permission %s for schedule management", admin.PermissionToolComposeInvoke)
	}
	switch action {
	case "list":
		ownerUserID := ""
		if !scheduleCanManageAll(request.Access) {
			ownerUserID = strings.TrimSpace(request.ActorID)
		}
		schedules, err := s.List(ctx, request.GuildID, ownerUserID, KindComposed, request.IncludeDisabled, 25)
		if err != nil {
			return nil, err
		}
		if request.ToolName != "" {
			schedules = filterSchedulesByToolName(schedules, request.ToolName)
		}
		return map[string]any{"result": map[string]any{
			"action":    "list",
			"kind":      KindComposed,
			"tool_name": strings.TrimSpace(request.ToolName),
			"schedules": schedulePayloads(schedules),
			"count":     len(schedules),
		}}, nil
	case "create":
		return s.manageScheduleCreate(ctx, request)
	case "cancel":
		return s.manageScheduleCancel(ctx, request)
	default:
		return nil, fmt.Errorf("action must be create, list, or cancel")
	}
}

func (s *Service) manageScheduleCreate(ctx context.Context, request toolsvc.ScheduleManagementRequest) (any, error) {
	toolName := strings.TrimSpace(request.ToolName)
	if toolName == "" {
		return nil, fmt.Errorf("tool_name is required")
	}
	if s.composed == nil {
		return nil, fmt.Errorf("composed scheduler is not configured")
	}
	allowed, err := s.composed.CanInvoke(ctx, request.GuildID, toolName, request.Access, composed.InvocationScheduled)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("approved composed tool %q is not schedulable or is not available to this caller", toolName)
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
		"tool_name":        toolName,
		"next_run_at":      next.UTC().Format(time.RFC3339),
		"interval_seconds": int(interval.Seconds()),
		"input":            request.Input,
	}
	if request.DryRun {
		return map[string]any{"result": map[string]any{
			"dry_run": true,
			"action":  "schedule.create",
			"preview": preview,
		}}, nil
	}
	schedule, err := s.CreateComposed(ctx, CreateComposedRequest{
		GuildID:     request.GuildID,
		ChannelID:   request.ChannelID,
		OwnerUserID: request.ActorID,
		ToolName:    toolName,
		Input:       request.Input,
		NextRunAt:   next,
		Interval:    interval,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": map[string]any{
		"action":   "create",
		"schedule": schedulePayload(schedule),
	}}, nil
}

func (s *Service) manageScheduleCancel(ctx context.Context, request toolsvc.ScheduleManagementRequest) (any, error) {
	schedule, err := s.scheduleToCancel(ctx, request)
	if err != nil {
		return nil, err
	}
	preview := schedulePayload(schedule)
	if request.DryRun {
		return map[string]any{"result": map[string]any{
			"dry_run": true,
			"action":  "schedule.cancel",
			"preview": preview,
		}}, nil
	}
	if err := s.Cancel(ctx, request.GuildID, request.ActorID, schedule.ID); err != nil {
		return nil, err
	}
	return map[string]any{"result": map[string]any{
		"action":   "cancel",
		"schedule": preview,
	}}, nil
}

func (s *Service) scheduleToCancel(ctx context.Context, request toolsvc.ScheduleManagementRequest) (store.Schedule, error) {
	if request.ScheduleID > 0 {
		schedule, ok, err := s.Get(ctx, request.GuildID, request.ScheduleID)
		if err != nil {
			return store.Schedule{}, err
		}
		if !ok {
			return store.Schedule{}, repository.ErrNotFound
		}
		if schedule.Kind != KindComposed {
			return store.Schedule{}, fmt.Errorf("schedule %d is not a composed tool run", request.ScheduleID)
		}
		if !scheduleCanManageAll(request.Access) && schedule.OwnerUserID != request.ActorID {
			return store.Schedule{}, fmt.Errorf("schedule %d is not owned by this caller", request.ScheduleID)
		}
		return schedule, nil
	}
	toolName := strings.TrimSpace(request.ToolName)
	if toolName == "" {
		return store.Schedule{}, fmt.Errorf("schedule_id or tool_name is required")
	}
	ownerUserID := ""
	if !scheduleCanManageAll(request.Access) {
		ownerUserID = strings.TrimSpace(request.ActorID)
	}
	schedules, err := s.List(ctx, request.GuildID, ownerUserID, KindComposed, false, 25)
	if err != nil {
		return store.Schedule{}, err
	}
	matches := filterSchedulesByToolName(schedules, toolName)
	switch len(matches) {
	case 0:
		return store.Schedule{}, fmt.Errorf("no active composed schedule for %q was found", toolName)
	case 1:
		return matches[0], nil
	default:
		return store.Schedule{}, fmt.Errorf("multiple active schedules match %q; cancel by schedule_id", toolName)
	}
}

func scheduleCanManageAll(access toolsvc.ToolAccess) bool {
	return access.HasAnyPermission(
		admin.PermissionAdminConfigRead,
		admin.PermissionAdminConfigWrite,
		admin.PermissionAdminUsageRead,
		admin.PermissionAdminAuditRead,
		admin.PermissionAdminMemoryManage,
		admin.PermissionOwnerOps,
	)
}

func schedulePayloads(schedules []store.Schedule) []map[string]any {
	payloads := make([]map[string]any, 0, len(schedules))
	for _, schedule := range schedules {
		payloads = append(payloads, schedulePayload(schedule))
	}
	return payloads
}

func schedulePayload(schedule store.Schedule) map[string]any {
	payload := map[string]any{
		"schedule_id":      schedule.ID,
		"kind":             schedule.Kind,
		"status":           schedule.Status,
		"disabled":         schedule.Disabled,
		"next_run_at":      schedule.NextRunAt.UTC().Format(time.RFC3339),
		"schedule_type":    schedule.ScheduleType,
		"interval_seconds": schedule.IntervalSeconds,
		"last_status":      schedule.LastStatus,
	}
	if toolName := scheduleToolName(schedule); toolName != "" {
		payload["tool_name"] = toolName
	}
	return payload
}

func filterSchedulesByToolName(schedules []store.Schedule, toolName string) []store.Schedule {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return schedules
	}
	matches := make([]store.Schedule, 0, len(schedules))
	for _, schedule := range schedules {
		if strings.EqualFold(scheduleToolName(schedule), toolName) {
			matches = append(matches, schedule)
		}
	}
	return matches
}

func scheduleToolName(schedule store.Schedule) string {
	if schedule.Kind != KindComposed {
		return ""
	}
	var payload ComposedPayload
	if err := json.Unmarshal([]byte(schedule.Payload), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.ToolName)
}
