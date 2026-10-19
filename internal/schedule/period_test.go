package schedule

import (
	"testing"

	"opensignage/internal/contracts"
)

// A window limited to a period of dates: a happy hour on one Saturday only (2026-11-07 is
// a Saturday).
func TestPeriod(t *testing.T) {
	loc := berlin(t)
	hh := contracts.Window{Days: []int{6}, Start: "17:00", End: "19:00", From: "2026-11-07", Until: "2026-11-07"}
	for at0, want := range map[string]bool{
		"2026-11-07 18:00": true,
		"2026-10-31 18:00": false, // the Saturday before
		"2026-11-14 18:00": false, // the Saturday after
		"2026-11-07 19:00": false, // end is exclusive
	} {
		if got := WindowActive(hh, at(loc, at0)); got != want {
			t.Errorf("%s: %v", at0, got)
		}
	}
	// Across midnight the start day counts: the night of the 7th runs into the 8th.
	night := contracts.Window{Days: []int{6}, Start: "22:00", End: "02:00", Until: "2026-11-07"}
	if !WindowActive(night, at(loc, "2026-11-08 01:00")) {
		t.Error("the night of the last day runs past midnight")
	}
	night.From, night.Until = "2026-11-08", ""
	if WindowActive(night, at(loc, "2026-11-08 01:00")) {
		t.Error("an occurrence that started before the period does not count")
	}
	if !WindowActive(night, at(loc, "2026-11-14 23:00")) {
		t.Error("open end")
	}
	// Open start.
	if !WindowActive(contracts.Window{Days: []int{6}, Start: "17:00", End: "19:00", Until: "2026-11-07"}, at(loc, "2026-01-03 18:00")) {
		t.Error("open start")
	}
}

func TestEvaluatePeriod(t *testing.T) {
	loc := berlin(t)
	eur := contracts.Money{Amount: 300, Currency: "EUR"}
	rules := []contracts.Rule{
		{ID: "base", Scope: contracts.ScopeDefault, ZoneID: "z", PlaylistID: "menu", Enabled: true},
		{ID: "hh", Scope: contracts.ScopeZone, ZoneID: "z", Enabled: true, PlaylistID: "happy",
			Window: &contracts.Window{Days: []int{6}, Start: "17:00", End: "19:00", From: "2026-11-07", Until: "2026-11-07"},
			Prices: []contracts.PriceOverride{{ProductID: "beer", Price: eur}}},
	}
	in := Input{Rules: rules, ZoneID: "z", Location: loc}
	in.Now = at(loc, "2026-11-07 17:30")
	r := Evaluate(in)
	if r.PlaylistID != "happy" || r.Prices["beer"] != eur || r.Since == nil || !r.Since.Equal(at(loc, "2026-11-07 17:00")) {
		t.Fatalf("in the period: %+v", r)
	}
	in.Now = at(loc, "2026-11-14 17:30")
	if r := Evaluate(in); r.PlaylistID != "menu" || len(r.Prices) != 0 {
		t.Fatalf("a week later: %+v", r)
	}
}

func TestOverlapsWithPeriods(t *testing.T) {
	sat := func(from, until string) contracts.Window {
		return contracts.Window{Days: []int{6}, Start: "17:00", End: "19:00", From: from, Until: until}
	}
	for name, c := range map[string]struct {
		a, b contracts.Window
		want bool
	}{
		"same day":                  {sat("2026-11-07", "2026-11-07"), sat("2026-11-07", "2026-11-07"), true},
		"different weeks":           {sat("2026-11-07", "2026-11-07"), sat("2026-11-14", "2026-11-14"), false},
		"every week vs. one day":    {sat("", ""), sat("2026-11-14", "2026-11-14"), true},
		"periods apart":             {sat("", "2026-10-31"), sat("2026-11-10", ""), false},
		"open ends meet":            {sat("2026-11-01", ""), sat("2026-11-03", ""), true},
		"long shared stretch":       {sat("2026-01-01", "2026-12-31"), sat("2026-06-01", "2026-08-31"), true},
		"weekly pattern apart":      {sat("2026-11-07", "2026-11-07"), contracts.Window{Days: []int{6}, Start: "20:00", End: "21:00"}, false},
		"invalid date never counts": {sat("2026-13-01", ""), sat("", ""), false},
	} {
		if got := Overlaps(c.a, c.b); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
		if got := Overlaps(c.b, c.a); got != c.want {
			t.Errorf("%s (swapped): %v", name, got)
		}
	}
	// Across midnight: the night of Saturday the 7th (until 02:00) meets a Sunday window
	// from 01:00 that starts on the 8th.
	night := contracts.Window{Days: []int{6}, Start: "22:00", End: "02:00", Until: "2026-11-07"}
	sunday := contracts.Window{Days: []int{7}, Start: "01:00", End: "03:00", From: "2026-11-08"}
	if !Overlaps(night, sunday) || !Overlaps(sunday, night) {
		t.Error("the night of the 7th overlaps the 8th")
	}
	sunday.From = "2026-11-15"
	if Overlaps(night, sunday) {
		t.Error("a week later it does not")
	}
}

func TestValidatePeriod(t *testing.T) {
	rule := func(w contracts.Window) contracts.Rule {
		return contracts.Rule{Name: "HH", Scope: contracts.ScopeGlobal, Window: &w, PlaylistID: "p"}
	}
	for name, c := range map[string]struct {
		w    contracts.Window
		want string // field: reason, "" = valid
	}{
		"one saturday":      {contracts.Window{Days: []int{6}, Start: "17:00", End: "19:00", From: "2026-11-07", Until: "2026-11-07"}, ""},
		"open end":          {contracts.Window{Days: []int{6}, Start: "17:00", End: "19:00", From: "2026-11-07"}, ""},
		"bad date":          {contracts.Window{Days: []int{6}, Start: "17:00", End: "19:00", From: "2026-11-7"}, "window.from: invalid"},
		"bad until":         {contracts.Window{Days: []int{6}, Start: "17:00", End: "19:00", Until: "2026-02-30"}, "window.until: invalid"},
		"until before from": {contracts.Window{Days: []int{6}, Start: "17:00", End: "19:00", From: "2026-11-07", Until: "2026-11-06"}, "window.until: before_from"},
		"no weekday inside": {contracts.Window{Days: []int{1}, Start: "17:00", End: "19:00", From: "2026-11-07", Until: "2026-11-08"}, "window.days: no_day_in_range"},
		"a whole week":      {contracts.Window{Days: []int{1}, Start: "17:00", End: "19:00", From: "2026-11-07", Until: "2026-11-13"}, ""},
	} {
		r := rule(c.w)
		err := r.Validate()
		got := ""
		if fe, ok := err.(*contracts.FieldError); ok {
			got = fe.Field + ": " + fe.Code
		} else if err != nil {
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	if contracts.ISOWeekdayOfDay(mustDay(t, "2026-11-07")) != 6 || contracts.ISOWeekdayOfDay(mustDay(t, "1970-01-01")) != 4 ||
		contracts.ISOWeekdayOfDay(mustDay(t, "1969-12-29")) != 1 {
		t.Error("weekday of a day number")
	}
}

func mustDay(t *testing.T, s string) int {
	t.Helper()
	d, err := contracts.DayNumber(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
