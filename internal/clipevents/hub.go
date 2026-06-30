// Package clipevents provides a tiny in-process pub/sub hub that fans out
// per-user clip lifecycle events. The clip pipeline (executor) publishes when a
// clip is created and the portal API publishes when one is deleted; the portal
// WebSocket endpoint subscribes per signed-in user and forwards the events to
// the browser so the clips library stays live without polling.
package clipevents

import "sync"

// Event kinds.
const (
	ClipCreated = "clip.created"
	ClipDeleted = "clip.deleted"
)

// Event is a single clip lifecycle notification scoped to one user.
type Event struct {
	Type   string
	UserID string
	ClipID string
}

// subscriberBuffer is how many events a single subscriber can fall behind
// before further events for it are dropped. Clip events are rare (a clip
// generation or a manual delete), so a small buffer is ample.
const subscriberBuffer = 16

// Hub fans events out to the live subscribers of each user. It is safe for
// concurrent use.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[*subscription]struct{}
}

type subscription struct {
	ch chan Event
}

// NewHub returns an empty hub ready for use.
func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[*subscription]struct{})}
}

// Subscribe registers a listener for userID's events and returns the receive
// channel plus a cancel func that unregisters it. The channel is never closed,
// so a still-pending Publish can never panic on a closed channel; callers stop
// listening by abandoning it after calling cancel.
func (h *Hub) Subscribe(userID string) (<-chan Event, func()) {
	sub := &subscription{ch: make(chan Event, subscriberBuffer)}
	h.mu.Lock()
	set := h.subs[userID]
	if set == nil {
		set = make(map[*subscription]struct{})
		h.subs[userID] = set
	}
	set[sub] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			if set := h.subs[userID]; set != nil {
				delete(set, sub)
				if len(set) == 0 {
					delete(h.subs, userID)
				}
			}
			h.mu.Unlock()
		})
	}
	return sub.ch, cancel
}

// Publish delivers ev to every current subscriber of ev.UserID. Delivery is
// non-blocking: a subscriber whose buffer is full simply misses the event
// (its next reconnect/resync reconciles), so a slow consumer never stalls the
// publisher.
func (h *Hub) Publish(ev Event) {
	if ev.UserID == "" {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for sub := range h.subs[ev.UserID] {
		select {
		case sub.ch <- ev:
		default:
		}
	}
}

// PublishClipCreated announces that clipID now exists for userID.
func (h *Hub) PublishClipCreated(userID, clipID string) {
	h.Publish(Event{Type: ClipCreated, UserID: userID, ClipID: clipID})
}

// PublishClipDeleted announces that clipID was removed for userID.
func (h *Hub) PublishClipDeleted(userID, clipID string) {
	h.Publish(Event{Type: ClipDeleted, UserID: userID, ClipID: clipID})
}
