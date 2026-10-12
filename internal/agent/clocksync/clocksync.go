// Package clocksync keeps the screens of a zone on one clock: synchronous
// playback computes the slide from the time, so every screen needs the same time to a few
// milliseconds. With a server the agent measures its offset to the server with round trips
// over the device link (the sample with the shortest round trip wins). Without a server the
// agents of a zone agree on one time leader in the LAN (a device that still has the server
// time, else the lowest device id); the others follow it with the same round-trip method.
// Only time is exchanged, never content; packets are authenticated with the zone's clock key.
package clocksync

import (
	"time"
)

// Sample is one round-trip measurement: the offset to add to the local clock and the
// round trip it was measured with (smaller = more accurate; the error is at most RTT/2).
type Sample struct {
	Offset time.Duration
	RTT    time.Duration
}

// FromExchange computes a sample from a request sent at t0 (local, with monotonic
// reading), the remote time when it answered, and the answer received at t3 (local).
func FromExchange(t0, remote, t3 time.Time) Sample {
	rtt := t3.Sub(t0) // monotonic: wall-clock jumps do not matter
	if rtt < 0 {
		rtt = 0
	}
	// The remote time belongs to the middle of the round trip.
	mid := t3.Add(-rtt / 2)
	return Sample{Offset: remote.Sub(mid.Round(0)), RTT: rtt}
}

// Best returns the sample with the shortest round trip.
func Best(samples []Sample) (Sample, bool) {
	if len(samples) == 0 {
		return Sample{}, false
	}
	best := samples[0]
	for _, s := range samples[1:] {
		if s.RTT < best.RTT {
			best = s
		}
	}
	return best, true
}

// Burst collects the answers of one measurement burst (requests by sequence number).
type Burst struct {
	sent    map[uint64]time.Time
	samples []Sample
}

// NewBurst starts an empty burst.
func NewBurst() *Burst { return &Burst{sent: map[uint64]time.Time{}} }

// Sent records request seq sent at t0.
func (b *Burst) Sent(seq uint64, t0 time.Time) { b.sent[seq] = t0 }

// Answer records the answer to seq (remote time) received at t3; unknown or repeated
// sequence numbers are ignored (false).
func (b *Burst) Answer(seq uint64, remote, t3 time.Time) bool {
	t0, ok := b.sent[seq]
	if !ok {
		return false
	}
	delete(b.sent, seq)
	b.samples = append(b.samples, FromExchange(t0, remote, t3))
	return true
}

// Done reports whether every request was answered.
func (b *Burst) Done() bool { return len(b.sent) == 0 }

// Result is the best sample so far.
func (b *Burst) Result() (Sample, bool) { return Best(b.samples) }
