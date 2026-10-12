package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"opensignage/internal/agent/wire"
	"opensignage/internal/contracts"
)

// fakeClock fires After channels when Advance passes their deadline.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
	added   chan struct{}
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t, added: make(chan struct{}, 100)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, waiter{at: c.now.Add(d), ch: ch})
	c.mu.Unlock()
	c.added <- struct{}{}
	return ch
}

// Advance moves time forward and fires due timers.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var keep []waiter
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
	c.mu.Unlock()
}

func manifest() *wire.Manifest {
	return &wire.Manifest{
		ID: "dep", ZoneID: "z1", Timezone: "Europe/Berlin", ClosedPlaylistID: "closed",
		Playlists: []wire.Playlist{{ID: "default"}, {ID: "happy"}, {ID: "closed"}},
		Rules: []contracts.Rule{
			{ID: "r-default", Scope: "default", ZoneID: "z1", PlaylistID: "default", Enabled: true},
			{ID: "r-happy", Scope: "zone", ZoneID: "z1", PlaylistID: "happy", Enabled: true,
				Window: &contracts.Window{Days: []int{1, 2, 3, 4, 5, 6, 7}, Start: "17:00", End: "18:00"},
				Prices: []contracts.PriceOverride{{ProductID: "beer", Price: contracts.Money{Amount: 250, Currency: "EUR"}}}},
			{ID: "r-missing", Scope: "global", PlaylistID: "deleted", Enabled: true,
				Window: &contracts.Window{Days: []int{1, 2, 3, 4, 5, 6, 7}, Start: "00:00", End: "00:00"}},
		},
	}
}

func TestSourceChangesAtBoundaries(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	clk := newFakeClock(time.Date(2026, 10, 12, 16, 59, 30, 0, berlin))
	var mu sync.Mutex
	var got []Output
	s := New(clk, func(o Output) { mu.Lock(); got = append(got, o); mu.Unlock() })
	s.SetInputs(Inputs{Manifest: manifest()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	waitTimer := func() {
		t.Helper()
		select {
		case <-clk.added:
		case <-time.After(2 * time.Second):
			t.Fatal("scheduler did not arm a timer")
		}
	}
	last := func() Output {
		mu.Lock()
		defer mu.Unlock()
		return got[len(got)-1]
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(got)
	}
	waitTimer()
	if o := last(); o.Source.PlaylistID != "default" || o.Source.Kind != wire.SourcePlaylist || len(o.Prices) != 0 {
		t.Fatalf("before window: %+v", o)
	}
	n := count()
	clk.Advance(31 * time.Second) // 17:00:01 → window active
	waitTimer()
	if count() != n+1 {
		t.Fatalf("expected exactly one change at the boundary, got %d", count()-n)
	}
	o := last()
	if o.Source.PlaylistID != "happy" || o.Source.RuleID != "r-happy" || o.Prices["beer"].Amount != 250 {
		t.Fatalf("in window: %+v", o)
	}
	// No change inside the window: no new emission.
	n = count()
	for i := 0; i < 4; i++ {
		clk.Advance(15 * time.Second)
		waitTimer()
	}
	if count() != n {
		t.Fatal("unchanged result must not be re-emitted")
	}
	clk.Advance(59 * time.Minute) // 18:00:xx → back to default
	waitTimer()
	if o := last(); o.Source.PlaylistID != "default" || len(o.Prices) != 0 {
		t.Fatalf("after window: %+v", o)
	}
}

func TestClosedAndClearedAndOffset(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	clk := newFakeClock(time.Date(2026, 10, 12, 12, 0, 0, 0, berlin))
	s := New(clk, nil)
	if o := s.SetInputs(Inputs{Manifest: manifest(), ZoneClosed: true}); o.Source.Kind != wire.SourceClosed || o.Source.PlaylistID != "closed" {
		t.Fatalf("closed: %+v", o)
	}
	if o := s.SetInputs(Inputs{Cleared: true, Manifest: manifest()}); o.Source.Kind != wire.SourceCleared || o.Reason != "cleared" {
		t.Fatalf("cleared: %+v", o)
	}
	if o := s.SetInputs(Inputs{}); o.Source.Kind != wire.SourceCleared || o.Reason != "no_deployment" {
		t.Fatalf("none: %+v", o)
	}
	s.SetInputs(Inputs{Manifest: manifest()})
	// Device clock 5 hours behind the server: the offset puts us into the window.
	s.SetOffset(5 * time.Hour)
	if o := s.Current(); o.Source.PlaylistID != "happy" {
		t.Fatalf("offset not applied: %+v", o)
	}
}

// The ticker of the active rule reaches the player with its style; a manifest of an older
// server without a style gets the default template; a style change is an output change.
func TestRuleTickerOutput(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	clk := newFakeClock(time.Date(2026, 10, 12, 17, 30, 0, 0, berlin))
	s := New(clk, nil)
	m := manifest()
	big := contracts.DefaultTickerTemplate()
	big.Size = 60
	m.Rules[1].Ticker = &contracts.RuleTicker{Text: "Happy Hour", Template: &big}
	o := s.SetInputs(Inputs{Manifest: m})
	if o.Ticker == nil || o.Ticker.Text != "Happy Hour" || o.Ticker.RuleID != "r-happy" || o.Ticker.Template.Size != 60 {
		t.Fatalf("ticker in the window: %+v", o.Ticker)
	}
	m2 := manifest()
	m2.Rules[1].Ticker = &contracts.RuleTicker{Text: "Happy Hour"} // no style: older server
	o2 := s.SetInputs(Inputs{Manifest: m2})
	if o2.Ticker == nil || o2.Ticker.Template != contracts.DefaultTickerTemplate() {
		t.Fatalf("default template expected: %+v", o2.Ticker)
	}
	if o.Equal(o2) {
		t.Fatal("a different ticker style must count as a change")
	}
	clk.Advance(time.Hour) // 18:30, after the window
	if o3 := s.SetInputs(Inputs{Manifest: m2}); o3.Ticker != nil {
		t.Fatalf("after the window no ticker: %+v", o3.Ticker)
	}
}

// The device's display group chooses its playlist and closed playlist; the
// output carries the anchor of the synchronous playback.
func TestGroupPlaylistAndAnchor(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	clk := newFakeClock(time.Date(2026, 10, 12, 12, 30, 0, 0, berlin))
	s := New(clk, nil)
	m := manifest()
	m.Playlists = append(m.Playlists, wire.Playlist{ID: "menu"}, wire.Playlist{ID: "menu-closed"})
	m.Groups = []contracts.ManifestGroup{{ID: "wall", Name: "Wand", ClosedPlaylistID: "menu-closed"}}
	m.Rules[0].GroupPlaylists = []contracts.GroupPlaylist{{GroupID: "wall", PlaylistID: "menu"}}
	o := s.SetInputs(Inputs{Manifest: m, GroupID: "wall"})
	if o.Source.PlaylistID != "menu" || !o.Since.Equal(time.Date(2026, 10, 12, 0, 0, 0, 0, berlin)) {
		t.Fatalf("group of the default rule, anchored at midnight: %+v", o)
	}
	if o := s.SetInputs(Inputs{Manifest: m}); o.Source.PlaylistID != "default" {
		t.Fatalf("screens without group: %+v", o)
	}
	clk.Advance(5 * time.Hour) // 17:30: the window sets the playlist of all screens, the group included
	o = s.SetInputs(Inputs{Manifest: m, GroupID: "wall"})
	if o.Source.PlaylistID != "happy" || !o.Since.Equal(time.Date(2026, 10, 12, 17, 0, 0, 0, berlin)) {
		t.Fatalf("window for all screens, anchored at its start: %+v", o)
	}
	closedAt := time.Date(2026, 10, 12, 17, 10, 0, 0, berlin)
	o = s.SetInputs(Inputs{Manifest: m, GroupID: "wall", ZoneClosed: true, ClosedSince: closedAt})
	if o.Source.Kind != wire.SourceClosed || o.Source.PlaylistID != "menu-closed" || !o.Since.Equal(closedAt) {
		t.Fatalf("closed group: %+v", o)
	}
	prev := s.Current()
	if o := s.SetInputs(Inputs{Manifest: m, GroupID: "wall", ZoneClosed: true, ClosedSince: closedAt.Add(time.Minute)}); o.Equal(prev) {
		t.Fatal("a new anchor is a change (the player restarts the timeline)")
	}
}
