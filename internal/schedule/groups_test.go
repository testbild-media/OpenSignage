package schedule

import (
	"testing"

	"opensignage/internal/contracts"
)

// Display groups, rules that change single things, the ticker hidden by a
// rule, closed content per group and the anchor of synchronous playback.
func TestEvaluateGroupsAndAnchors(t *testing.T) {
	loc := berlin(t)
	w := func(days []int, start, end string) *contracts.Window {
		return &contracts.Window{Days: days, Start: start, End: end}
	}
	rules := []contracts.Rule{
		{ID: "std", Scope: contracts.ScopeDefault, ZoneID: "z", Enabled: true, PlaylistID: "werbung",
			GroupPlaylists: []contracts.GroupPlaylist{{GroupID: "menu", PlaylistID: "speisekarte"}},
			Ticker:         &contracts.RuleTicker{Text: "Willkommen"}},
		{ID: "lunch", Scope: contracts.ScopeZone, ZoneID: "z", Enabled: true, Window: w([]int{1, 2, 3, 4, 5}, "11:00", "14:00"),
			GroupPlaylists: []contracts.GroupPlaylist{{GroupID: "menu", PlaylistID: "mittag"}},
			Prices:         []contracts.PriceOverride{{ProductID: "dish", Price: eur(890)}},
			Ticker:         &contracts.RuleTicker{Hide: true}},
		{ID: "night", Scope: contracts.ScopeZone, ZoneID: "z", Enabled: true, Window: w([]int{1}, "22:00", "02:00"),
			PlaylistID: "nacht", GroupPlaylists: []contracts.GroupPlaylist{{GroupID: contracts.NoGroupID, PlaylistID: "lounge"}}},
	}
	ev := func(now, group string) Result {
		return Evaluate(Input{Rules: rules, ZoneID: "z", GroupID: group, Location: loc, Now: at(loc, now)})
	}
	// 10:00 Monday: default rule – the menu group its own playlist, every other screen
	// "werbung"; the ticker of the default rule; anchor = midnight.
	if r := ev("2026-10-12 10:00", "menu"); r.PlaylistID != "speisekarte" || r.Since == nil || !r.Since.Equal(at(loc, "2026-10-12 00:00")) {
		t.Fatalf("10:00 menu: %+v", r)
	}
	if r := ev("2026-10-12 10:00", ""); r.PlaylistID != "werbung" || r.Ticker == nil || r.Ticker.Text != "Willkommen" {
		t.Fatalf("10:00 other: %+v", r)
	}
	// 12:30: lunch changes only the menu group (anchor 11:00), its price and hides the
	// ticker; the other screens keep "werbung" from the default rule.
	r := ev("2026-10-12 12:30", "menu")
	if r.PlaylistID != "mittag" || r.RuleID != "lunch" || !r.Since.Equal(at(loc, "2026-10-12 11:00")) {
		t.Fatalf("12:30 menu: %+v", r)
	}
	if r.Ticker != nil || !r.TickerHidden || r.TickerRuleID != "lunch" || r.Prices["dish"] != eur(890) {
		t.Fatalf("12:30 ticker/prices: %+v", r)
	}
	if r := ev("2026-10-12 12:30", "bar"); r.PlaylistID != "werbung" || r.RuleID != "std" {
		t.Fatalf("12:30 other group falls back to all screens of the default: %+v", r)
	}
	// 01:00 Tuesday: the Monday night window (across midnight) – screens without group get
	// "lounge", groups without own entry "nacht"; anchor = Monday 22:00.
	if r := ev("2026-10-13 01:00", ""); r.PlaylistID != "lounge" || !r.Since.Equal(at(loc, "2026-10-12 22:00")) {
		t.Fatalf("night ungrouped: %+v", r)
	}
	if r := ev("2026-10-13 01:00", "menu"); r.PlaylistID != "nacht" {
		t.Fatalf("night menu (all screens of the night rule beat the default's group playlist): %+v", r)
	}
	// Closed: the group's own closed playlist, anchored at the moment of closing.
	closedAt := at(loc, "2026-10-12 23:10")
	r = Evaluate(Input{Rules: rules, ZoneID: "z", GroupID: "menu", ZoneClosed: true, ClosedPlaylistID: "zu",
		GroupClosedPlaylistID: "zu-menu", ClosedSince: closedAt, Location: loc, Now: at(loc, "2026-10-12 23:30")})
	if r.Kind != KindClosed || r.PlaylistID != "zu-menu" || !r.Since.Equal(closedAt) || r.Ticker != nil {
		t.Fatalf("closed menu: %+v", r)
	}
	r = Evaluate(Input{Rules: rules, ZoneID: "z", ZoneClosed: true, ClosedPlaylistID: "zu", Location: loc, Now: at(loc, "2026-10-12 23:30")})
	if r.PlaylistID != "zu" || !r.Since.Equal(at(loc, "2026-10-12 00:00")) {
		t.Fatalf("closed other, no closing time known: %+v", r)
	}
}

func TestChangesAndWindowStart(t *testing.T) {
	a := contracts.Rule{PlaylistID: "x", Prices: []contracts.PriceOverride{{ProductID: "beer"}}}
	b := contracts.Rule{GroupPlaylists: []contracts.GroupPlaylist{{GroupID: "menu", PlaylistID: "y"}}, Ticker: &contracts.RuleTicker{Text: "t"}}
	c := contracts.Rule{Prices: []contracts.PriceOverride{{ProductID: "beer"}}}
	if got := SharedChange(a, b); got != "" {
		t.Fatalf("all screens vs. one group and ticker change different things: %q", got)
	}
	if got := SharedChange(a, c); got != "price:beer" {
		t.Fatalf("same price: %q", got)
	}
	loc := berlin(t)
	win := contracts.Window{Days: []int{5}, Start: "20:00", End: "20:00"} // Friday 24 h
	if got := WindowStart(win, at(loc, "2026-10-17 08:00")); !got.Equal(at(loc, "2026-10-16 20:00")) {
		t.Fatalf("24 h window from Friday 20:00, Saturday morning: %v", got)
	}
	if got := WindowStart(win, at(loc, "2026-10-16 21:00")); !got.Equal(at(loc, "2026-10-16 20:00")) {
		t.Fatalf("same evening: %v", got)
	}
}
