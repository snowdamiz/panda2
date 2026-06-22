package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/billing"
	"github.com/sn0w/panda2/internal/composed"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/security"
	"github.com/sn0w/panda2/internal/store"
)

const (
	JobKind = "schedule.execute"

	KindReminder = "reminder"
	KindFollowUp = "follow_up"
	KindComposed = "composed"

	ScheduleOnce      = "once"
	ScheduleRecurring = "recurring"

	TargetUser    = "user"
	TargetChannel = "channel"
	TargetRole    = "role"
)

type AuditRecorder interface {
	Record(ctx context.Context, event store.AuditEvent) error
}

type DeliverySender interface {
	SendScheduledMessage(ctx context.Context, delivery Delivery) error
}

type DiscordActivityCounter interface {
	CountActivity(ctx context.Context, filter repository.DiscordActivityFilter) (int64, error)
}

type Service struct {
	schedules *repository.ScheduleRepository
	jobs      *repository.JobRepository
	composed  *composed.Service
	events    DiscordActivityCounter
	delivery  DeliverySender
	audit     AuditRecorder
	billing   *billing.Service
	now       func() time.Time
}

type ReminderPayload struct {
	Message         string    `json:"message"`
	SourceMessageID string    `json:"source_message_id,omitempty"`
	WatchChannelID  string    `json:"watch_channel_id,omitempty"`
	WatchAfter      time.Time `json:"watch_after,omitempty"`
}

type ComposedPayload struct {
	ToolName       string         `json:"tool_name"`
	InvocationType string         `json:"invocation_type,omitempty"`
	Input          map[string]any `json:"input,omitempty"`
}

type JobPayload struct {
	ScheduleID uint      `json:"schedule_id"`
	RunAt      time.Time `json:"run_at"`
}

type CreateReminderRequest struct {
	GuildID         string
	ChannelID       string
	OwnerUserID     string
	TargetType      string
	TargetID        string
	Message         string
	Title           string
	NextRunAt       time.Time
	Interval        time.Duration
	SourceMessageID string
	FollowUp        bool
}

type CreateComposedRequest struct {
	GuildID     string
	ChannelID   string
	OwnerUserID string
	ToolName    string
	Input       map[string]any
	Title       string
	NextRunAt   time.Time
	Interval    time.Duration
}

type Delivery struct {
	ScheduleID uint
	GuildID    string
	ChannelID  string
	TargetType string
	TargetID   string
	Title      string
	Message    string
}

func NewService(schedules *repository.ScheduleRepository, jobs *repository.JobRepository) *Service {
	return &Service{
		schedules: schedules,
		jobs:      jobs,
		now:       time.Now,
	}
}

func (s *Service) WithComposedService(composedService *composed.Service) *Service {
	s.composed = composedService
	return s
}

func (s *Service) WithDiscordEvents(events DiscordActivityCounter) *Service {
	s.events = events
	return s
}

func (s *Service) WithDeliverySender(sender DeliverySender) *Service {
	s.delivery = sender
	return s
}

func (s *Service) WithAuditRecorder(recorder AuditRecorder) *Service {
	s.audit = recorder
	return s
}

func (s *Service) WithBilling(billingService *billing.Service) *Service {
	s.billing = billingService
	return s
}

func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *Service) CreateReminder(ctx context.Context, request CreateReminderRequest) (store.Schedule, error) {
	if err := validateReminderRequest(request); err != nil {
		return store.Schedule{}, err
	}
	if err := s.ensureScheduleCapacity(ctx, request.GuildID); err != nil {
		return store.Schedule{}, err
	}
	now := s.now().UTC()
	payload := ReminderPayload{
		Message:         security.RedactSecrets(strings.TrimSpace(request.Message)),
		SourceMessageID: strings.TrimSpace(request.SourceMessageID),
		WatchAfter:      now,
	}
	if request.FollowUp {
		payload.WatchChannelID = firstNonEmpty(strings.TrimSpace(request.ChannelID), strings.TrimSpace(request.TargetID))
	}
	kind := KindReminder
	if request.FollowUp {
		kind = KindFollowUp
	}
	scheduleType := ScheduleOnce
	intervalSeconds := 0
	if request.Interval > 0 {
		scheduleType = ScheduleRecurring
		intervalSeconds = int(request.Interval.Seconds())
	}
	schedule, err := s.schedules.Create(ctx, store.Schedule{
		GuildID:         request.GuildID,
		ChannelID:       request.ChannelID,
		OwnerUserID:     request.OwnerUserID,
		Kind:            kind,
		Status:          repository.ScheduleStatusActive,
		Title:           firstNonEmpty(request.Title, defaultReminderTitle(payload.Message, request.FollowUp)),
		TargetType:      firstNonEmpty(request.TargetType, TargetUser),
		TargetID:        firstNonEmpty(request.TargetID, request.OwnerUserID),
		ScheduleType:    scheduleType,
		IntervalSeconds: intervalSeconds,
		Payload:         mustJSON(payload),
		NextRunAt:       request.NextRunAt.UTC(),
		DedupeKey:       scheduleDedupeKey(request.GuildID, kind, request.OwnerUserID, payload.Message, request.NextRunAt),
	})
	if err == nil {
		s.recordAudit(ctx, request.GuildID, request.OwnerUserID, "schedule.create", kind, strconv.FormatUint(uint64(schedule.ID), 10), map[string]string{
			"schedule_type": scheduleType,
			"target_type":   schedule.TargetType,
		})
	}
	return schedule, err
}

func (s *Service) CreateComposed(ctx context.Context, request CreateComposedRequest) (store.Schedule, error) {
	if strings.TrimSpace(request.GuildID) == "" || strings.TrimSpace(request.ToolName) == "" {
		return store.Schedule{}, fmt.Errorf("guild_id and tool_name are required")
	}
	next := request.NextRunAt.UTC()
	if next.IsZero() {
		return store.Schedule{}, fmt.Errorf("next run time is required")
	}
	if err := s.ensureScheduleCapacity(ctx, request.GuildID); err != nil {
		return store.Schedule{}, err
	}
	scheduleType := ScheduleOnce
	intervalSeconds := 0
	if request.Interval > 0 {
		scheduleType = ScheduleRecurring
		intervalSeconds = int(request.Interval.Seconds())
	}
	payload := ComposedPayload{
		ToolName:       strings.TrimSpace(request.ToolName),
		InvocationType: composed.InvocationScheduled,
		Input:          request.Input,
	}
	if payload.Input == nil {
		payload.Input = map[string]any{}
	}
	schedule, err := s.schedules.Create(ctx, store.Schedule{
		GuildID:         request.GuildID,
		ChannelID:       request.ChannelID,
		OwnerUserID:     request.OwnerUserID,
		Kind:            KindComposed,
		Status:          repository.ScheduleStatusActive,
		Title:           firstNonEmpty(request.Title, "Run "+payload.ToolName),
		TargetType:      TargetChannel,
		TargetID:        request.ChannelID,
		ScheduleType:    scheduleType,
		IntervalSeconds: intervalSeconds,
		Payload:         mustJSON(payload),
		NextRunAt:       next,
		DedupeKey:       scheduleDedupeKey(request.GuildID, KindComposed, request.OwnerUserID, payload.ToolName, next),
	})
	if err == nil {
		s.recordAudit(ctx, request.GuildID, request.OwnerUserID, "schedule.create", KindComposed, strconv.FormatUint(uint64(schedule.ID), 10), map[string]string{
			"tool_name":     payload.ToolName,
			"schedule_type": scheduleType,
		})
	}
	return schedule, err
}

func (s *Service) ensureScheduleCapacity(ctx context.Context, guildID string) error {
	if s.billing == nil {
		return nil
	}
	entitlement, err := s.billing.Resolve(ctx, guildID)
	if err != nil {
		return err
	}
	if !entitlement.CanUsePaidFeatures || entitlement.ReadOnly {
		return billing.ErrReadOnly
	}
	stats, err := s.schedules.Stats(ctx, guildID)
	if err != nil {
		return err
	}
	limit := int64(entitlement.Plan.Schedules)
	if limit >= 0 && stats.Active >= limit {
		return billing.QuotaError{
			Metric:     billing.MetricScheduledRun,
			Used:       stats.Active,
			Limit:      limit,
			Plan:       entitlement.Plan.Plan,
			UpgradeURL: entitlement.UpgradeURL,
		}
	}
	return nil
}

func (s *Service) List(ctx context.Context, guildID, ownerUserID, kind string, includeDisabled bool, limit int) ([]store.Schedule, error) {
	return s.schedules.List(ctx, guildID, ownerUserID, kind, includeDisabled, limit)
}

func (s *Service) Get(ctx context.Context, guildID string, id uint) (store.Schedule, bool, error) {
	return s.schedules.Get(ctx, guildID, id)
}

func (s *Service) Cancel(ctx context.Context, guildID, actorID string, id uint) error {
	if err := s.schedules.SetDisabled(ctx, guildID, id, true, s.now()); err != nil {
		return err
	}
	s.recordAudit(ctx, guildID, actorID, "schedule.cancel", "schedule", strconv.FormatUint(uint64(id), 10), nil)
	return nil
}

func (s *Service) Complete(ctx context.Context, guildID, actorID string, id uint) error {
	now := s.now().UTC()
	if _, ok, err := s.schedules.Get(ctx, guildID, id); err != nil {
		return err
	} else if !ok {
		return repository.ErrNotFound
	}
	if err := s.schedules.MarkFinished(ctx, id, repository.ScheduleLastSkipped, "completed by user", nil, true, now); err != nil {
		return err
	}
	s.recordAudit(ctx, guildID, actorID, "schedule.complete", "schedule", strconv.FormatUint(uint64(id), 10), nil)
	return nil
}

func (s *Service) Snooze(ctx context.Context, guildID, actorID string, id uint, nextRunAt time.Time) error {
	if err := s.schedules.Snooze(ctx, guildID, id, nextRunAt, s.now()); err != nil {
		return err
	}
	s.recordAudit(ctx, guildID, actorID, "schedule.snooze", "schedule", strconv.FormatUint(uint64(id), 10), map[string]string{
		"next_run_at": nextRunAt.UTC().Format(time.RFC3339),
	})
	return nil
}

func (s *Service) Stats(ctx context.Context, guildID string) (repository.ScheduleStats, error) {
	return s.schedules.Stats(ctx, guildID)
}

func (s *Service) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.ClaimAndEnqueue(ctx, 25); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) ClaimAndEnqueue(ctx context.Context, limit int) error {
	if s == nil || s.schedules == nil || s.jobs == nil {
		return nil
	}
	now := s.now().UTC()
	due, err := s.schedules.ClaimDue(ctx, now, limit, 3*time.Minute)
	if err != nil {
		return err
	}
	for _, schedule := range due {
		payload := mustJSON(JobPayload{ScheduleID: schedule.ID, RunAt: now})
		job, err := s.jobs.Enqueue(ctx, store.Job{
			Kind:        JobKind,
			GuildID:     schedule.GuildID,
			Payload:     payload,
			MaxAttempts: 3,
			RunAfter:    now,
		})
		if err != nil {
			_ = s.schedules.MarkFailed(ctx, schedule.ID, err.Error(), now, true)
			continue
		}
		_ = s.schedules.AttachJob(ctx, schedule.ID, job.ID, now)
	}
	return nil
}

func (s *Service) HandleJob(ctx context.Context, job store.Job) error {
	var payload JobPayload
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return err
	}
	schedule, ok, err := s.schedules.Get(ctx, job.GuildID, payload.ScheduleID)
	if err != nil {
		return err
	}
	if !ok || schedule.Disabled || schedule.Status != repository.ScheduleStatusActive {
		return nil
	}
	now := s.now().UTC()
	if err := s.schedules.MarkRunning(ctx, schedule.ID, now); err != nil {
		return err
	}
	status, runErr := s.executeSchedule(ctx, schedule, now)
	if runErr != nil {
		releaseLock := job.Attempts >= job.MaxAttempts
		_ = s.schedules.MarkFailed(ctx, schedule.ID, runErr.Error(), now, releaseLock)
		return runErr
	}
	nextRunAt, disabled := nextScheduleRun(schedule, now)
	message := ""
	if status == repository.ScheduleLastSkipped {
		message = "skipped because follow-up activity resolved the reminder"
	}
	if err := s.schedules.MarkFinished(ctx, schedule.ID, status, message, nextRunAt, disabled, now); err != nil {
		return err
	}
	s.recordAudit(ctx, schedule.GuildID, schedule.OwnerUserID, "schedule."+status, schedule.Kind, strconv.FormatUint(uint64(schedule.ID), 10), nil)
	return nil
}

func (s *Service) executeSchedule(ctx context.Context, schedule store.Schedule, now time.Time) (string, error) {
	switch schedule.Kind {
	case KindReminder:
		return s.withScheduledRunQuota(ctx, schedule, func() (string, error) {
			return repository.ScheduleLastSucceeded, s.deliverReminder(ctx, schedule)
		})
	case KindFollowUp:
		resolved, err := s.followUpResolved(ctx, schedule, now)
		if err != nil {
			return repository.ScheduleLastFailed, err
		}
		if resolved {
			return repository.ScheduleLastSkipped, nil
		}
		return s.withScheduledRunQuota(ctx, schedule, func() (string, error) {
			return repository.ScheduleLastSucceeded, s.deliverReminder(ctx, schedule)
		})
	case KindComposed:
		if s.composed == nil {
			return repository.ScheduleLastSkipped, nil
		}
		return repository.ScheduleLastSucceeded, s.runComposed(ctx, schedule)
	default:
		return repository.ScheduleLastFailed, fmt.Errorf("unsupported schedule kind %q", schedule.Kind)
	}
}

func (s *Service) withScheduledRunQuota(ctx context.Context, schedule store.Schedule, run func() (string, error)) (string, error) {
	if s.billing == nil {
		return run()
	}
	reservation, err := s.billing.BeginUsage(ctx, schedule.GuildID, billing.MetricScheduledRun, 1)
	if err != nil {
		return repository.ScheduleLastFailed, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = s.billing.ReleaseUsage(context.Background(), reservation)
		}
	}()
	status, err := run()
	if err != nil {
		return status, err
	}
	_ = s.billing.CommitUsage(ctx, reservation)
	committed = true
	return status, nil
}

func (s *Service) deliverReminder(ctx context.Context, schedule store.Schedule) error {
	if s.delivery == nil {
		return errors.New("scheduled message delivery is not configured")
	}
	var payload ReminderPayload
	if err := json.Unmarshal([]byte(schedule.Payload), &payload); err != nil {
		return err
	}
	message := strings.TrimSpace(payload.Message)
	if message == "" {
		return fmt.Errorf("reminder message is empty")
	}
	return s.delivery.SendScheduledMessage(ctx, Delivery{
		ScheduleID: schedule.ID,
		GuildID:    schedule.GuildID,
		ChannelID:  schedule.ChannelID,
		TargetType: firstNonEmpty(schedule.TargetType, TargetUser),
		TargetID:   firstNonEmpty(schedule.TargetID, schedule.OwnerUserID),
		Title:      schedule.Title,
		Message:    message,
	})
}

func (s *Service) followUpResolved(ctx context.Context, schedule store.Schedule, now time.Time) (bool, error) {
	if s.events == nil {
		return false, nil
	}
	var payload ReminderPayload
	if err := json.Unmarshal([]byte(schedule.Payload), &payload); err != nil {
		return false, err
	}
	watchChannelID := firstNonEmpty(payload.WatchChannelID, schedule.ChannelID)
	since := payload.WatchAfter
	if since.IsZero() {
		since = schedule.CreatedAt
	}
	count, err := s.events.CountActivity(ctx, repository.DiscordActivityFilter{
		GuildID:          schedule.GuildID,
		ChannelID:        watchChannelID,
		Since:            since,
		ExcludeUserID:    schedule.OwnerUserID,
		ExcludeAuthorBot: true,
		EventTypes:       []string{"message_create"},
	})
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Service) runComposed(ctx context.Context, schedule store.Schedule) error {
	if s.composed == nil {
		return errors.New("composed scheduler is not configured")
	}
	var payload ComposedPayload
	if err := json.Unmarshal([]byte(schedule.Payload), &payload); err != nil {
		return err
	}
	if payload.Input == nil {
		payload.Input = map[string]any{}
	}
	_, err := s.composed.Run(ctx, composed.RunRequest{
		GuildID:           schedule.GuildID,
		ToolName:          payload.ToolName,
		InvocationType:    firstNonEmpty(payload.InvocationType, composed.InvocationScheduled),
		InvokingUserID:    schedule.OwnerUserID,
		TriggeringEventID: fmt.Sprintf("schedule:%d", schedule.ID),
		Input:             payload.Input,
	})
	return err
}

func validateReminderRequest(request CreateReminderRequest) error {
	if strings.TrimSpace(request.GuildID) == "" {
		return fmt.Errorf("guild_id is required")
	}
	if strings.TrimSpace(request.ChannelID) == "" {
		return fmt.Errorf("channel_id is required")
	}
	if strings.TrimSpace(request.OwnerUserID) == "" {
		return fmt.Errorf("owner_user_id is required")
	}
	if strings.TrimSpace(request.Message) == "" {
		return fmt.Errorf("reminder message is required")
	}
	if request.NextRunAt.IsZero() {
		return fmt.Errorf("next run time is required")
	}
	switch firstNonEmpty(request.TargetType, TargetUser) {
	case TargetUser, TargetChannel, TargetRole:
		return nil
	default:
		return fmt.Errorf("target_type must be user, channel, or role")
	}
}

func nextScheduleRun(schedule store.Schedule, now time.Time) (*time.Time, bool) {
	if schedule.ScheduleType != ScheduleRecurring || schedule.IntervalSeconds <= 0 {
		return nil, true
	}
	next := schedule.NextRunAt.UTC()
	interval := time.Duration(schedule.IntervalSeconds) * time.Second
	if next.IsZero() {
		next = now.Add(interval)
	}
	for !next.After(now) {
		next = next.Add(interval)
	}
	return &next, false
}

func defaultReminderTitle(message string, followUp bool) string {
	if followUp {
		return "Follow-up"
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "Reminder"
	}
	if len([]rune(message)) <= 48 {
		return "Reminder: " + message
	}
	runes := []rune(message)
	return "Reminder: " + string(runes[:45]) + "..."
}

func scheduleDedupeKey(parts ...any) string {
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		values = append(values, fmt.Sprint(part))
	}
	return strings.Join(values, "|")
}

func (s *Service) recordAudit(ctx context.Context, guildID, actorID, action, targetType, targetID string, meta map[string]string) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Metadata:   mustJSON(meta),
	})
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
