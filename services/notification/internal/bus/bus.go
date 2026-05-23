// In-process publish/subscribe for real-time SSE delivery.
//
// One bus per service instance. The Notify handler publishes a single
// FeedItem keyed by (tenant, recipient) after committing its tx; every
// SSE subscriber on that key gets a non-blocking write to its channel.
//
// This is intentionally an in-memory implementation — it doesn't
// survive a service restart and doesn't fan out across multiple
// notification-service instances. Production deployments that need
// horizontal scale should swap this for Postgres LISTEN/NOTIFY or
// Redis pub/sub (same interface). The SSE handler keeps a safety-poll
// fallback so a missed push only delays a notification by ~30s.

package bus

import (
	"sync"

	"github.com/google/uuid"

	"github.com/nexussacco/notification/internal/domain"
)

// Key identifies a subscriber. Either UserID or CounterpartyID is set —
// matches the recipient model on the notifications table.
type Key struct {
	TenantID uuid.UUID
	UserID   uuid.UUID
	CounterpartyID uuid.UUID
}

type Bus struct {
	mu   sync.RWMutex
	subs map[Key]map[uuid.UUID]chan *domain.FeedItem
}

func New() *Bus {
	return &Bus{subs: map[Key]map[uuid.UUID]chan *domain.FeedItem{}}
}

// Subscribe returns a buffered channel and an unsubscribe func. The
// caller MUST defer unsubscribe — leaking subscriptions leaks
// goroutines that block on the channel.
func (b *Bus) Subscribe(key Key) (<-chan *domain.FeedItem, func()) {
	ch := make(chan *domain.FeedItem, 32)
	id := uuid.New()
	b.mu.Lock()
	if b.subs[key] == nil {
		b.subs[key] = map[uuid.UUID]chan *domain.FeedItem{}
	}
	b.subs[key][id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if m, ok := b.subs[key]; ok {
			if c, ok := m[id]; ok {
				delete(m, id)
				close(c)
			}
			if len(m) == 0 {
				delete(b.subs, key)
			}
		}
		b.mu.Unlock()
	}
}

// Publish delivers item to every subscriber matching key. Drops on a
// full channel to keep the publisher non-blocking — subscribers that
// fall behind miss the realtime push but will pick the row up on the
// next safety poll.
func (b *Bus) Publish(key Key, item *domain.FeedItem) {
	b.mu.RLock()
	subs := b.subs[key]
	chans := make([]chan *domain.FeedItem, 0, len(subs))
	for _, ch := range subs {
		chans = append(chans, ch)
	}
	b.mu.RUnlock()
	for _, ch := range chans {
		select {
		case ch <- item:
		default:
		}
	}
}

// SubscriberCount reports the current number of attached subscribers.
// Used for the health/metrics endpoint.
func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	for _, m := range b.subs {
		n += len(m)
	}
	return n
}
