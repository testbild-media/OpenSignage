// Package ratelimit implements a bounded login throttle with growing lockouts.
package ratelimit

import (
	"sync"
	"time"
)

type entry struct {
	failures    int
	lockedUntil time.Time
	lastSeen    time.Time
}

// Throttle tracks failures per key. After Free failures, each further failure locks
// the key for an exponentially growing time (capped at Max). Memory is bounded.
type Throttle struct {
	mu      sync.Mutex
	m       map[string]*entry
	Free    int
	Base    time.Duration
	Max     time.Duration
	MaxKeys int
	now     func() time.Time
}

// New creates a throttle: 5 free attempts, then 2 s, 4 s … up to 15 min.
func New() *Throttle {
	return &Throttle{m: map[string]*entry{}, Free: 5, Base: 2 * time.Second, Max: 15 * time.Minute, MaxKeys: 10000, now: time.Now}
}

// Check returns how long the key is still locked (0 = allowed).
func (t *Throttle) Check(key string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.m[key]
	if e == nil {
		return 0
	}
	if d := e.lockedUntil.Sub(t.now()); d > 0 {
		return d
	}
	return 0
}

// Fail records a failed attempt.
func (t *Throttle) Fail(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	e := t.m[key]
	if e == nil {
		if len(t.m) >= t.MaxKeys {
			t.evict(now)
		}
		e = &entry{}
		t.m[key] = e
	}
	e.failures++
	e.lastSeen = now
	if over := e.failures - t.Free; over > 0 {
		d := t.Base << min(over-1, 20)
		if d > t.Max || d <= 0 {
			d = t.Max
		}
		e.lockedUntil = now.Add(d)
	}
}

// Success clears the key.
func (t *Throttle) Success(key string) {
	t.mu.Lock()
	delete(t.m, key)
	t.mu.Unlock()
}

func (t *Throttle) evict(now time.Time) {
	// Drop entries idle for more than Max first; if still full, drop the oldest.
	var oldestKey string
	var oldest time.Time
	for k, e := range t.m {
		if now.Sub(e.lastSeen) > t.Max {
			delete(t.m, k)
			continue
		}
		if oldestKey == "" || e.lastSeen.Before(oldest) {
			oldestKey, oldest = k, e.lastSeen
		}
	}
	if len(t.m) >= t.MaxKeys && oldestKey != "" {
		delete(t.m, oldestKey)
	}
}
