package schedule

import (
	"testing"
	"time"

	"opensignage/internal/contracts"
)

func berlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func at(loc *time.Location, s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		panic(err)
	}
	return t
}

func eur(c int64) contracts.Money { return contracts.Money{Amount: c, Currency: "EUR"} }

// 2026-10-12 is a Monday.
func TestWindowActiveMidnight(t *testing.T) {
	loc := berlin(t)
	w := contracts.Window{Days: []int{5}, Start: "22:00", End: "02:00"} // Friday night
	cases := map[string]bool{
		"2026-10-16 21:59": false,
		"2026-10-16 22:00": true,
		"2026-10-17 01:59": true, // Saturday early morning belongs to Friday's window
		"2026-10-17 02:00": false,
		"2026-10-17 22:30": false, // Saturday evening: not configured
	}
	for s, want := range cases {
		if got := WindowActive(w, at(loc, s)); got != want {
			t.Errorf("%s: got %v want %v", s, got, want)
		}
	}
	full := contracts.Window{Days: []int{6}, Start: "00:00", End: "00:00"}
	if !WindowActive(full, at(loc, "2026-10-17 23:59")) || WindowActive(full, at(loc, "2026-10-18 00:00")) {
		t.Error("24h window wrong")
	}
	// Sunday -> Monday wrap.
	sun := contracts.Window{Days: []int{7}, Start: "23:00", End: "01:00"}
	if !WindowActive(sun, at(loc, "2026-10-19 00:30")) {
		t.Error("sunday window should cover monday 00:30")
	}
}

func TestEvaluatePriority(t *testing.T) {
	loc := berlin(t)
	rules := []contracts.Rule{
		{ID: "def", Scope: contracts.ScopeDefault, ZoneID: "z1", PlaylistID: "pDefault", Enabled: true},
		{ID: "glob", Scope: contracts.ScopeGlobal, Window: &contracts.Window{Days: []int{1, 2, 3, 4, 5, 6, 7}, Start: "17:00", End: "19:00"},
			PlaylistID: "pHappy", Prices: []contracts.PriceOverride{{ProductID: "beer", Price: eur(300)}}, Enabled: true},
		{ID: "zone", Scope: contracts.ScopeZone, ZoneID: "z1", Window: &contracts.Window{Days: []int{1}, Start: "18:00", End: "18:30"},
			Prices: []contracts.PriceOverride{{ProductID: "beer", Price: eur(250)}}, Enabled: true}, // price-only
		{ID: "other", Scope: contracts.ScopeZone, ZoneID: "z2", Window: &contracts.Window{Days: []int{1}, Start: "00:00", End: "00:00"},
			PlaylistID: "pOther", Enabled: true},
	}
	base := Input{Rules: rules, ZoneID: "z1", Location: loc}

	in := base
	in.Now = at(loc, "2026-10-12 12:00")
	r := Evaluate(in)
	if r.Kind != KindRule || r.PlaylistID != "pDefault" || len(r.Prices) != 0 {
		t.Fatalf("noon: %+v", r)
	}

	in.Now = at(loc, "2026-10-12 17:30")
	r = Evaluate(in)
	if r.PlaylistID != "pHappy" || r.Prices["beer"].Amount != 300 {
		t.Fatalf("17:30: %+v", r)
	}

	// Price-only zone rule wins prices but keeps the global playlist.
	in.Now = at(loc, "2026-10-12 18:10")
	r = Evaluate(in)
	if r.PlaylistID != "pHappy" || r.RuleID != "glob" || r.Prices["beer"].Amount != 250 {
		t.Fatalf("18:10: %+v", r)
	}

	// CLOSED with closed playlist beats everything; prices still apply.
	in.ZoneClosed, in.ClosedPlaylistID = true, "pClosed"
	r = Evaluate(in)
	if r.Kind != KindClosed || r.PlaylistID != "pClosed" || r.Prices["beer"].Amount != 250 {
		t.Fatalf("closed: %+v", r)
	}
	// CLOSED without closed playlist: normal content continues.
	in.ClosedPlaylistID = ""
	if r = Evaluate(in); r.PlaylistID != "pHappy" {
		t.Fatalf("closed without playlist: %+v", r)
	}
}

func TestEvaluateSkipsUnavailableAndDisabled(t *testing.T) {
	loc := berlin(t)
	rules := []contracts.Rule{
		{ID: "def", Scope: contracts.ScopeDefault, ZoneID: "z1", PlaylistID: "gone", Enabled: true,
			Prices: []contracts.PriceOverride{{ProductID: "beer", Price: eur(1)}}},
		{ID: "off", Scope: contracts.ScopeGlobal, Window: &contracts.Window{Days: []int{1}, Start: "00:00", End: "00:00"}, PlaylistID: "p", Enabled: false},
	}
	r := Evaluate(Input{Rules: rules, ZoneID: "z1", Location: loc, Now: at(loc, "2026-10-12 10:00"),
		PlaylistAvailable: func(id string) bool { return id != "gone" }})
	// A rule changes each thing for itself – its unavailable playlist does not
	// count (nothing to show), its prices still apply; the disabled rule does nothing.
	if r.Kind != KindCleared || r.PlaylistID != "" || r.Prices["beer"] != eur(1) || len(r.ActiveRuleIDs) != 1 {
		t.Fatalf("expected cleared content with the rule's prices, got %+v", r)
	}
}

func TestEvaluateDST(t *testing.T) {
	loc := berlin(t)
	// 2026-10-25 03:00 CEST -> 02:00 CET. A 17:00 window must still be 17:00 local.
	w := contracts.Window{Days: []int{7}, Start: "17:00", End: "18:00"}
	utc := time.Date(2026, 10, 25, 16, 30, 0, 0, time.UTC) // 17:30 CET
	if !WindowActive(w, utc.In(loc)) {
		t.Fatal("window must follow local wall clock after DST change")
	}
}

func TestOverlaps(t *testing.T) {
	a := contracts.Window{Days: []int{5}, Start: "22:00", End: "02:00"}
	b := contracts.Window{Days: []int{6}, Start: "01:00", End: "03:00"}
	c := contracts.Window{Days: []int{6}, Start: "02:00", End: "03:00"}
	d := contracts.Window{Days: []int{7}, Start: "23:00", End: "01:00"}
	e := contracts.Window{Days: []int{1}, Start: "00:30", End: "02:00"}
	if !Overlaps(a, b) {
		t.Error("a/b overlap")
	}
	if Overlaps(a, c) {
		t.Error("a/c touch only")
	}
	if !Overlaps(d, e) {
		t.Error("sunday->monday wrap overlap")
	}
}

func TestRuleValidate(t *testing.T) {
	ok := contracts.Rule{Name: "x", Scope: contracts.ScopeZone, ZoneID: "z", PlaylistID: "p",
		Window: &contracts.Window{Days: []int{1}, Start: "08:00", End: "09:00"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []contracts.Rule{
		{Name: "x", Scope: contracts.ScopeDefault, ZoneID: "z", PlaylistID: "p", Window: ok.Window},
		{Name: "x", Scope: contracts.ScopeGlobal, PlaylistID: "p"},
		{Name: "x", Scope: contracts.ScopeZone, ZoneID: "z", Window: ok.Window},
		{Name: "x", Scope: contracts.ScopeZone, ZoneID: "z", PlaylistID: "p", Window: &contracts.Window{Days: []int{8}, Start: "08:00", End: "09:00"}},
		{Name: "x", Scope: contracts.ScopeZone, ZoneID: "z", PlaylistID: "p", Window: &contracts.Window{Days: []int{1}, Start: "24:00", End: "09:00"}},
	}
	for i, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("case %d should fail", i)
		}
	}
}

// Tickers of rules: the first active rule with a ticker shows it, in the
// same order as playlists (zone window, global window, default). A ticker-only rule leaves
// playlist and prices of the other rules alone; while the zone is CLOSED there is none.
func TestEvaluateTicker(t *testing.T) {
	loc := berlin(t)
	happy := &contracts.Window{Days: []int{1, 2, 3, 4, 5, 6, 7}, Start: "17:00", End: "19:00"}
	rules := []contracts.Rule{
		{ID: "def", Scope: contracts.ScopeDefault, ZoneID: "z1", PlaylistID: "pDefault", Enabled: true,
			Ticker: &contracts.RuleTicker{Text: "Welcome"}},
		{ID: "hh", Scope: contracts.ScopeZone, ZoneID: "z1", Window: happy, Enabled: true,
			Prices: []contracts.PriceOverride{{ProductID: "beer", Price: eur(300)}},
			Ticker: &contracts.RuleTicker{Text: "Happy Hour – Beer 3 €"}},
		{ID: "glob", Scope: contracts.ScopeGlobal, Window: happy, Enabled: true, Ticker: &contracts.RuleTicker{Text: "Global"}},
	}
	in := Input{Rules: rules, ZoneID: "z1", Location: loc}

	in.Now = at(loc, "2026-10-12 18:00")
	r := Evaluate(in)
	if r.Ticker == nil || r.Ticker.Text != "Happy Hour – Beer 3 €" || r.TickerRuleID != "hh" {
		t.Fatalf("happy hour: %+v %q", r.Ticker, r.TickerRuleID)
	}
	if r.PlaylistID != "pDefault" || r.Prices["beer"] != eur(300) {
		t.Fatalf("a ticker/price rule leaves the playlist of the default: %+v", r)
	}
	in.Now = at(loc, "2026-10-12 20:00")
	if r = Evaluate(in); r.Ticker == nil || r.Ticker.Text != "Welcome" || r.TickerRuleID != "def" {
		t.Fatalf("outside the window the default's ticker: %+v", r.Ticker)
	}
	// Disabled rules show nothing; without any ticker rule there is none.
	rules[1].Enabled = false
	in.Now = at(loc, "2026-10-12 18:00")
	if r = Evaluate(in); r.Ticker == nil || r.Ticker.Text != "Global" {
		t.Fatalf("zone rule disabled: the global ticker: %+v", r.Ticker)
	}
	rules[0].Ticker, rules[2].Ticker = nil, nil
	if r = Evaluate(in); r.Ticker != nil {
		t.Fatalf("no ticker rule active: %+v", r.Ticker)
	}
	// CLOSED: the closed content is shown as it is.
	rules[1].Enabled = true
	in.ZoneClosed, in.ClosedPlaylistID = true, "pClosed"
	if r = Evaluate(in); r.Ticker != nil || r.Kind != KindClosed {
		t.Fatalf("closed zone must not show a rule ticker: %+v", r)
	}
}
