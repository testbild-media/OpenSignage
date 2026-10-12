package core

import (
	"log/slog"
	"sync"
)

// Event topics. Payloads are small and JSON-serializable; subscribers re-read state.
const (
	TopicContent  = "content"  // presentations, playlists, media, fonts changed → rebuild deployments
	TopicZones    = "zones"    // zone settings, schedules
	TopicLive     = "live"     // live state of a zone changed (ZoneID set, "" = all)
	TopicDevices  = "devices"  // device list/status changed
	TopicProducts = "products" // product master data
	TopicMedia    = "media"    // media processing progress
	TopicUpdates  = "updates"  // update/rollout progress
	TopicSettings = "settings"
	TopicUsers    = "users"
)

// Event is published on the bus.
type Event struct {
	Topic  string `json:"topic"`
	Kind   string `json:"kind,omitempty"`
	ID     string `json:"id,omitempty"`
	ZoneID string `json:"zoneId,omitempty"`
	Data   any    `json:"data,omitempty"`
}

// Subscription receives events. Overflow is set when events were dropped because the
// subscriber was too slow; it must then resync completely.
type Subscription struct {
	C        chan Event
	bus      *Bus
	mu       sync.Mutex
	overflow bool
}

// TakeOverflow returns and clears the overflow flag.
func (s *Subscription) TakeOverflow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.overflow
	s.overflow = false
	return o
}

// Close unsubscribes.
func (s *Subscription) Close() { s.bus.unsubscribe(s) }

// Bus is a non-blocking in-process pub/sub with bounded queues.
type Bus struct {
	mu   sync.RWMutex
	subs map[*Subscription]struct{}
}

// NewBus creates a bus.
func NewBus() *Bus { return &Bus{subs: map[*Subscription]struct{}{}} }

// Subscribe registers a subscriber with a queue of the given size.
func (b *Bus) Subscribe(size int) *Subscription {
	s := &Subscription{C: make(chan Event, size), bus: b}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

func (b *Bus) unsubscribe(s *Subscription) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
}

// Publish delivers e to all subscribers without ever blocking the publisher.
func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		select {
		case s.C <- e:
		default:
			s.mu.Lock()
			s.overflow = true
			s.mu.Unlock()
			slog.Debug("event dropped for slow subscriber", "topic", e.Topic)
		}
	}
}
