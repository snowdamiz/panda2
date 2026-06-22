package scheduler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/queue"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
	toolsvc "github.com/sn0w/panda2/internal/tools"
)

type fakeDeliverySender struct {
	deliveries []Delivery
	err        error
}

func (f *fakeDeliverySender) SendScheduledMessage(_ context.Context, delivery Delivery) error {
	f.deliveries = append(f.deliveries, delivery)
	return f.err
}

func newSchedulerTestService(t *testing.T, now time.Time) (*Service, *store.Store, *queue.Worker, *fakeDeliverySender) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	schedules := repository.NewScheduleRepository(db.DB)
	jobs := repository.NewJobRepository(db.DB)
	delivery := &fakeDeliverySender{}
	service := NewService(schedules, jobs).WithDeliverySender(delivery)
	service.SetClock(func() time.Time { return now })
	worker := queue.NewWorker(jobs, "scheduler-test")
	worker.SetClock(func() time.Time { return now })
	worker.Register(JobKind, service.HandleJob)
	return service, db, worker, delivery
}

func TestOneTimeReminderRunsAndCompletes(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service, db, worker, delivery := newSchedulerTestService(t, now)
	defer db.Close()
	ctx := context.Background()

	schedule, err := service.CreateReminder(ctx, CreateReminderRequest{
		GuildID:     "guild-1",
		ChannelID:   "channel-1",
		OwnerUserID: "user-1",
		TargetType:  TargetUser,
		TargetID:    "user-1",
		Message:     "stand up",
		NextRunAt:   now,
	})
	if err != nil {
		t.Fatalf("CreateReminder: %v", err)
	}
	if err := service.ClaimAndEnqueue(ctx, 10); err != nil {
		t.Fatalf("ClaimAndEnqueue: %v", err)
	}
	worked, err := worker.WorkOnce(ctx, JobKind)
	if err != nil || !worked {
		t.Fatalf("WorkOnce worked=%t err=%v", worked, err)
	}
	if len(delivery.deliveries) != 1 || delivery.deliveries[0].Message != "stand up" {
		t.Fatalf("unexpected deliveries: %+v", delivery.deliveries)
	}
	var saved store.Schedule
	if err := db.DB.First(&saved, schedule.ID).Error; err != nil {
		t.Fatalf("lookup schedule: %v", err)
	}
	if !saved.Disabled || saved.Status != repository.ScheduleStatusCompleted || saved.LastStatus != repository.ScheduleLastSucceeded {
		t.Fatalf("expected completed one-time schedule, got %+v", saved)
	}
}

func TestRecurringReminderAdvancesAfterSuccess(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service, db, worker, _ := newSchedulerTestService(t, now)
	defer db.Close()
	ctx := context.Background()

	schedule, err := service.CreateReminder(ctx, CreateReminderRequest{
		GuildID:     "guild-1",
		ChannelID:   "channel-1",
		OwnerUserID: "user-1",
		TargetType:  TargetUser,
		TargetID:    "user-1",
		Message:     "weekly review",
		NextRunAt:   now,
		Interval:    7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("CreateReminder: %v", err)
	}
	if err := service.ClaimAndEnqueue(ctx, 10); err != nil {
		t.Fatalf("ClaimAndEnqueue: %v", err)
	}
	if worked, err := worker.WorkOnce(ctx, JobKind); err != nil || !worked {
		t.Fatalf("WorkOnce worked=%t err=%v", worked, err)
	}
	var saved store.Schedule
	if err := db.DB.First(&saved, schedule.ID).Error; err != nil {
		t.Fatalf("lookup schedule: %v", err)
	}
	if saved.Disabled || !saved.NextRunAt.After(now) || saved.ScheduleType != ScheduleRecurring {
		t.Fatalf("expected advanced recurring schedule, got %+v", saved)
	}
}

func TestParseTimeOptionsAcceptsNaturalInValue(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	next, interval, err := ParseTimeOptions(map[string]string{
		"in":    "10 minutes",
		"every": "daily",
	}, now)
	if err != nil {
		t.Fatalf("ParseTimeOptions: %v", err)
	}
	if !next.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("unexpected next run: %s", next)
	}
	if interval != 24*time.Hour {
		t.Fatalf("unexpected interval: %s", interval)
	}
}

func TestManageReminderCreatesPersonalReminder(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service, db, _, _ := newSchedulerTestService(t, now)
	defer db.Close()
	ctx := context.Background()

	result, err := service.ManageReminder(ctx, toolsvc.ReminderManagementRequest{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		ActorID:   "user-1",
		RequestID: "message-1",
		Action:    "create",
		Message:   "stand up",
		In:        "10 minutes",
		Target:    "me",
	})
	if err != nil {
		t.Fatalf("ManageReminder: %v", err)
	}
	root, ok := result.(map[string]any)
	payloadResult, ok := root["result"].(map[string]any)
	if !ok || payloadResult["action"] != "create" {
		t.Fatalf("unexpected reminder result: %+v", result)
	}
	reminders, err := service.List(ctx, "guild-1", "user-1", KindReminder, false, 10)
	if err != nil {
		t.Fatalf("List reminders: %v", err)
	}
	if len(reminders) != 1 {
		t.Fatalf("expected one reminder, got %+v", reminders)
	}
	reminder := reminders[0]
	if reminder.OwnerUserID != "user-1" || reminder.TargetType != TargetUser || reminder.TargetID != "user-1" || !reminder.NextRunAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("unexpected reminder schedule: %+v", reminder)
	}
	var payload ReminderPayload
	if err := json.Unmarshal([]byte(reminder.Payload), &payload); err != nil {
		t.Fatalf("decode reminder payload: %v", err)
	}
	if payload.Message != "stand up" || payload.SourceMessageID != "message-1" {
		t.Fatalf("unexpected reminder payload: %+v", payload)
	}
}

func TestManageReminderRejectsPublicReminderWithoutConfirmation(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service, db, _, _ := newSchedulerTestService(t, now)
	defer db.Close()

	_, err := service.ManageReminder(context.Background(), toolsvc.ReminderManagementRequest{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		ActorID:   "user-1",
		Action:    "create",
		Message:   "stand up together",
		In:        "10 minutes",
		Target:    "channel",
	})
	if err == nil || !strings.Contains(err.Error(), "confirmation button") {
		t.Fatalf("expected public reminder confirmation error, got %v", err)
	}
}
