// Package scheduler evaluates the time schedule rules of the active deployment locally
// (shared evaluator internal/schedule) at every minute boundary, every 15 s as a safety
// net against clock jumps, and whenever an input changes. Changes are reported only
// when the result (playlist, rule or prices) actually differs.
package scheduler

import (
	"context"
	"log/slog"
	"maps"
	"sync"
	"time"

	"opensignage/internal/agent/wire"
	"opensignage/internal/contracts"
	"opensignage/internal/schedule"
)

// Clock abstracts time for tests.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// RealClock is the system clock.
var RealClock Clock = realClock{}

// Inputs of an evaluation.
type Inputs struct {
	Manifest   *wire.Manifest // nil: nothing deployed
	Cleared    bool           // explicit cleared target
	ZoneClosed bool           // live state zone CLOSED
	// GroupID is the device's display group ("" = none); ClosedSince when the zone was
	// closed (zero = unknown).
	GroupID     string
	ClosedSince time.Time
}

// Output is the evaluated source.
type Output struct {
	Source wire.ActiveSource
	Reason string // closed | rule | cleared | no_rule | no_deployment
	Prices map[string]contracts.Money
	Ticker *wire.PlayerTicker // ticker of the active rule (nil = none)
	// Since anchors the synchronous playback of the playlist (zero = none).
	Since time.Time
}

// Equal compares two outputs.
func (o Output) Equal(p Output) bool {
	sameTicker := (o.Ticker == nil) == (p.Ticker == nil) && (o.Ticker == nil || *o.Ticker == *p.Ticker)
	return o.Source == p.Source && o.Reason == p.Reason && maps.Equal(o.Prices, p.Prices) && sameTicker && o.Since.Equal(p.Since)
}

// Scheduler holds the inputs and the last output.
type Scheduler struct {
	clock    Clock
	onChange func(Output)

	mu       sync.Mutex
	in       Inputs
	offset   time.Duration
	out      Output
	have     bool
	kick     chan struct{}
	location *time.Location
}

// New creates a scheduler; onChange is called (outside locks) when the output changes.
func New(clock Clock, onChange func(Output)) *Scheduler {
	if clock == nil {
		clock = RealClock
	}
	return &Scheduler{clock: clock, onChange: onChange, kick: make(chan struct{}, 1),
		out: Output{Source: wire.ActiveSource{Kind: wire.SourceCleared}, Reason: "no_deployment", Prices: map[string]contracts.Money{}}}
}

// Now returns the corrected time (system time + server offset).
func (s *Scheduler) Now() time.Time {
	s.mu.Lock()
	off := s.offset
	s.mu.Unlock()
	return s.clock.Now().Add(off)
}

// SetOffset sets the server clock correction (applied as given; the caller decides
// the threshold).
func (s *Scheduler) SetOffset(d time.Duration) {
	s.mu.Lock()
	s.offset = d
	s.mu.Unlock()
	s.Evaluate()
}

// SetInputs replaces the inputs and evaluates immediately.
func (s *Scheduler) SetInputs(in Inputs) Output {
	s.mu.Lock()
	s.in = in
	s.location = nil
	if in.Manifest != nil && in.Manifest.Timezone != "" {
		loc, err := time.LoadLocation(in.Manifest.Timezone)
		if err != nil {
			slog.Error("unknown timezone in manifest, evaluating in UTC", "tz", in.Manifest.Timezone, "err", err)
		} else {
			s.location = loc
		}
	}
	s.mu.Unlock()
	return s.Evaluate()
}

// Current returns the last output.
func (s *Scheduler) Current() Output {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out
}

// Location returns the evaluation time zone (UTC when unknown).
func (s *Scheduler) Location() *time.Location {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.location == nil {
		return time.UTC
	}
	return s.location
}

// Evaluate computes the output for the current time and reports a change.
func (s *Scheduler) Evaluate() Output {
	s.mu.Lock()
	now := s.clock.Now().Add(s.offset)
	out := compute(s.in, s.location, now)
	changed := !s.have || !out.Equal(s.out)
	s.out, s.have = out, true
	s.mu.Unlock()
	if changed {
		slog.Info("schedule result", "kind", out.Source.Kind, "playlist", out.Source.PlaylistID,
			"rule", out.Source.RuleID, "reason", out.Reason, "prices", len(out.Prices))
		if s.onChange != nil {
			s.onChange(out)
		}
	}
	return out
}

func compute(in Inputs, loc *time.Location, now time.Time) Output {
	out := Output{Source: wire.ActiveSource{Kind: wire.SourceCleared}, Prices: map[string]contracts.Money{}}
	switch {
	case in.Cleared:
		out.Reason = "cleared"
		return out
	case in.Manifest == nil:
		out.Reason = "no_deployment"
		return out
	}
	m := in.Manifest
	playlists := make(map[string]bool, len(m.Playlists))
	for _, p := range m.Playlists {
		playlists[p.ID] = true
	}
	groupClosed := ""
	for _, g := range m.Groups {
		if g.ID == in.GroupID {
			groupClosed = g.ClosedPlaylistID
		}
	}
	res := schedule.Evaluate(schedule.Input{
		Rules:                 m.Rules,
		ZoneID:                m.ZoneID,
		GroupID:               in.GroupID,
		ZoneClosed:            in.ZoneClosed,
		ClosedPlaylistID:      m.ClosedPlaylistID,
		GroupClosedPlaylistID: groupClosed,
		ClosedSince:           in.ClosedSince,
		Now:                   now,
		Location:              loc,
		PlaylistAvailable:     func(id string) bool { return playlists[id] },
	})
	if res.Since != nil {
		out.Since = *res.Since
	}
	if res.Prices != nil {
		out.Prices = res.Prices
	}
	if t := res.Ticker; t != nil {
		tpl := contracts.DefaultTickerTemplate() // manifests of older servers carry no style
		if t.Template != nil {
			tpl = *t.Template
		}
		out.Ticker = &wire.PlayerTicker{Text: t.Text, Template: tpl, RuleID: res.TickerRuleID}
	}
	switch res.Kind {
	case schedule.KindClosed:
		out.Source = wire.ActiveSource{Kind: wire.SourceClosed, PlaylistID: res.PlaylistID}
		out.Reason = "closed"
	case schedule.KindRule:
		out.Source = wire.ActiveSource{Kind: wire.SourcePlaylist, PlaylistID: res.PlaylistID, RuleID: res.RuleID}
		out.Reason = "rule"
	default:
		out.Reason = "no_rule"
	}
	return out
}

// Kick requests an evaluation from the Run loop.
func (s *Scheduler) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run evaluates at every minute boundary of the corrected clock (window boundaries
// are whole minutes) and at least every 15 s, until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	for {
		s.Evaluate()
		wait := schedule.NextMinute(s.Now())
		if wait > 15*time.Second {
			wait = 15 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		case <-s.clock.After(wait):
		}
	}
}
