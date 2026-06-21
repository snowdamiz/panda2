package alerts

import (
	"context"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/composed"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

type fakeAlertDelivery struct {
	deliveries []Delivery
}

func (f *fakeAlertDelivery) SendAlert(_ context.Context, delivery Delivery) error {
	f.deliveries = append(f.deliveries, delivery)
	return nil
}

func TestAlertPackDeliversAndBatchesCooldown(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	delivery := &fakeAlertDelivery{}
	rules := repository.NewAlertRuleRepository(db.DB)
	service := NewService(rules).WithDeliverySender(delivery)
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	if _, err := service.Enable(ctx, "guild-1", "admin-1", PackSecurity, "channel-1", time.Hour); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	service.HandleDiscordEvent(ctx, store.DiscordEvent{
		GuildID:   "guild-1",
		EventType: composed.EventWebhooksUpdated,
		Summary:   "Webhook changed",
	})
	if len(delivery.deliveries) != 1 {
		t.Fatalf("expected first alert delivery, got %+v", delivery.deliveries)
	}
	service.HandleDiscordEvent(ctx, store.DiscordEvent{
		GuildID:   "guild-1",
		EventType: composed.EventWebhooksUpdated,
		Summary:   "Webhook changed again",
	})
	if len(delivery.deliveries) != 1 {
		t.Fatalf("second event inside cooldown should batch, got %+v", delivery.deliveries)
	}
	list, err := service.List(ctx, "guild-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].PendingCount != 1 {
		t.Fatalf("expected one pending batched alert, got %+v", list)
	}
}
