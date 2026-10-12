package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"time"

	"opensignage/internal/agent/clocksync"
	"opensignage/internal/agent/state"
	"opensignage/internal/agent/wire"
)

// Clock measurement: synchronous playback computes the slide from the
// time, so the screens of a zone need the same time to a few milliseconds. While connected
// the agent measures its offset to the server every minute with a burst of round trips (the
// shortest one wins); without the server the LAN time exchange takes over.
const (
	clockEvery     = time.Minute
	clockBurst     = 5
	clockBurstGap  = 100 * time.Millisecond
	clockBurstWait = 2 * time.Second
	// A sample with a longer round trip is not trusted (busy link).
	clockMaxRTT = 500 * time.Millisecond
	// clock.json is written only when the offset moved this much (SD cards).
	clockSaveStep = 100 * time.Millisecond
)

type clockState struct {
	burst      *clocksync.Burst
	seq        uint64
	lastServer time.Time // last accepted server measurement
	lastPeer   time.Time // last accepted measurement against the LAN leader
	source     string
	rtt        time.Duration
	leader     string
	saved      time.Duration // offset last written to clock.json
}

// clockLoop measures against the server after connecting and then every minute.
func (a *Agent) clockLoop(ctx context.Context) {
	t := time.NewTicker(clockEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.clockKick:
		case <-t.C:
		}
		if a.link == nil || !a.link.Connected() {
			continue
		}
		a.measureServer(ctx)
	}
}

func (a *Agent) kickClock() {
	select {
	case a.clockKick <- struct{}{}:
	default:
	}
}

func (a *Agent) measureServer(ctx context.Context) {
	burst := clocksync.NewBurst()
	a.mu.Lock()
	a.clock.burst = burst
	a.mu.Unlock()
	for i := 0; i < clockBurst; i++ {
		a.mu.Lock()
		a.clock.seq++
		seq := a.clock.seq
		now := time.Now()
		burst.Sent(seq, now)
		a.mu.Unlock()
		a.link.Send(wire.TTimePing, "", wire.TimePing{Seq: seq, T0: now.UnixMicro()})
		select {
		case <-ctx.Done():
			return
		case <-time.After(clockBurstGap):
		}
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(clockBurstWait):
	}
	a.mu.Lock()
	s, ok := burst.Result()
	if a.clock.burst == burst {
		a.clock.burst = nil
	}
	a.mu.Unlock()
	if !ok || s.RTT > clockMaxRTT {
		slog.Debug("clock: no usable server measurement", "answers", ok, "rtt", s.RTT)
		return
	}
	a.applyClock(s, clocksync.SourceServer, "")
}

func (a *Agent) onTimePong(raw json.RawMessage) {
	var p wire.TimePong
	if err := json.Unmarshal(raw, &p); err != nil {
		slog.Warn("invalid time.pong", "err", err)
		return
	}
	at := time.Now()
	a.mu.Lock()
	if a.clock.burst != nil {
		a.clock.burst.Answer(p.Seq, time.UnixMicro(p.Server), at)
	}
	a.mu.Unlock()
}

// applyClock takes a new offset (server or LAN leader). The player follows through its
// config (clockOffsetMs); the schedule uses it beyond 2 s (applyOffset).
func (a *Agent) applyClock(s clocksync.Sample, source, leader string) {
	now := time.Now()
	a.mu.Lock()
	prev := a.offset
	a.offset = s.Offset
	a.clockServer, a.clockLocal = now.Add(s.Offset), now // reference for the drift check
	a.clock.source, a.clock.rtt, a.clock.leader = source, s.RTT, leader
	if source == clocksync.SourceServer {
		a.clock.lastServer = now
	} else {
		a.clock.lastPeer = now
	}
	save := absDur(s.Offset-a.clock.saved) >= clockSaveStep
	if save {
		a.clock.saved = s.Offset
	}
	a.mu.Unlock()
	if d := absDur(s.Offset - prev); d >= 50*time.Millisecond {
		slog.Info("clock corrected", "source", source, "leader", leader, "offset", s.Offset.String(), "change", d.String(), "rtt", s.RTT.String())
	}
	if save {
		if err := a.store.SaveClock(state.Clock{OffsetMS: s.Offset.Milliseconds(), MeasuredAt: now.UTC().Format(time.RFC3339)}); err != nil {
			slog.Error("save clock.json failed", "err", err)
		}
	}
	a.applyOffset(s.Offset)
	if absDur(s.Offset-prev) >= time.Millisecond {
		a.refreshPlayer()
	}
}

// serverClockAge is the time since the last server measurement (large: never).
func (a *Agent) serverClockAge() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.clock.lastServer.IsZero() {
		return math.MaxInt64
	}
	return time.Since(a.clock.lastServer)
}

func (a *Agent) clockInfo() *wire.ClockInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	info := &wire.ClockInfo{Source: clocksync.SourceLocal, OffsetMS: float64(a.offset.Microseconds()) / 1000}
	last := a.clock.lastServer
	switch {
	case !a.clock.lastServer.IsZero() && time.Since(a.clock.lastServer) < clocksync.OfflineAfter:
		info.Source = clocksync.SourceServer
	case !a.clock.lastPeer.IsZero() && time.Since(a.clock.lastPeer) < clocksync.OfflineAfter:
		info.Source, info.Leader, last = clocksync.SourcePeer, a.clock.leader, a.clock.lastPeer
	}
	if info.Source != clocksync.SourceLocal {
		info.RTTMS = float64(a.clock.rtt.Microseconds()) / 1000
	}
	if !last.IsZero() {
		info.AgeS = int64(time.Since(last).Seconds())
	}
	return info
}

// clockPeers is the LAN time exchange of this agent.
func (a *Agent) clockPeers() *clocksync.Peers {
	return &clocksync.Peers{Port: a.cfg.ClockPort, Self: clocksync.Self{
		DeviceID: func() string { return a.hardwareID },
		Zone: func() (string, string) {
			a.mu.Lock()
			defer a.mu.Unlock()
			if c := a.device.Config; c != nil {
				return c.ZoneID, c.ClockKey
			}
			return "", ""
		},
		Now: func() time.Time {
			a.mu.Lock()
			defer a.mu.Unlock()
			return time.Now().Add(a.offset)
		},
		ServerAge: a.serverClockAge,
		Apply:     func(s clocksync.Sample, leader string) { a.applyClock(s, clocksync.SourcePeer, leader) },
	}}
}
