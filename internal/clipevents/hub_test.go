package clipevents

import (
	"sync"
	"testing"
)

func TestHubDeliversToSubscribers(t *testing.T) {
	hub := NewHub()
	a, cancelA := hub.Subscribe("user-1")
	defer cancelA()
	b, cancelB := hub.Subscribe("user-1")
	defer cancelB()

	hub.PublishClipCreated("user-1", "clip-1")

	for i, ch := range []<-chan Event{a, b} {
		select {
		case ev := <-ch:
			if ev.Type != ClipCreated || ev.ClipID != "clip-1" || ev.UserID != "user-1" {
				t.Fatalf("subscriber %d got unexpected event %+v", i, ev)
			}
		default:
			t.Fatalf("subscriber %d received no event", i)
		}
	}
}

func TestHubScopesByUser(t *testing.T) {
	hub := NewHub()
	other, cancel := hub.Subscribe("user-2")
	defer cancel()

	hub.PublishClipCreated("user-1", "clip-1")

	select {
	case ev := <-other:
		t.Fatalf("unrelated user received event %+v", ev)
	default:
	}
}

func TestHubCancelStopsDelivery(t *testing.T) {
	hub := NewHub()
	ch, cancel := hub.Subscribe("user-1")
	cancel()
	// A second cancel must be a no-op, never panic.
	cancel()

	hub.PublishClipDeleted("user-1", "clip-1")

	select {
	case ev := <-ch:
		t.Fatalf("cancelled subscriber received event %+v", ev)
	default:
	}
}

func TestHubPublishIsNonBlockingWhenFull(t *testing.T) {
	hub := NewHub()
	_, cancel := hub.Subscribe("user-1")
	defer cancel()

	// Far more than the buffer; a full subscriber must never stall the publisher.
	for i := 0; i < subscriberBuffer*4; i++ {
		hub.PublishClipCreated("user-1", "clip")
	}
}

func TestHubConcurrentPublishSubscribe(t *testing.T) {
	hub := NewHub()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, cancel := hub.Subscribe("user-1")
			hub.PublishClipCreated("user-1", "clip")
			<-ch
			cancel()
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			hub.PublishClipDeleted("user-1", "clip")
		}()
	}
	wg.Wait()
}
