// Package schedule evaluates time schedule rules. The exact same code runs on the server
// (admin view, deployment planning) and on every client (offline switching), so both
// always agree on which playlist, which prices and which ticker are active.
package schedule

import (
	"sort"
	"time"

	"opensignage/internal/contracts"
)

// Result kinds.
const (
	KindClosed  = "closed"  // zone CLOSED with its closed playlist
	KindRule    = "rule"    // a rule supplies the playlist
	KindCleared = "cleared" // nothing to show: the screen is cleared
)

// Input for one evaluation.
type Input struct {
	Rules            []contracts.Rule
	ZoneID           string
	ZoneClosed       bool
	ClosedPlaylistID string
	// GroupID is the display group of the screen ("" = none);
	// GroupClosedPlaylistID its own closed playlist ("" = the zone's).
	GroupID               string
	GroupClosedPlaylistID string
	// ClosedSince is when the zone was closed (anchor of the closed content; zero = midnight).
	ClosedSince time.Time
	Now         time.Time
	Location    *time.Location
	// PlaylistAvailable reports whether a playlist can be played (exists, published,
	// not archived; on clients: contained in the active deployment).
	PlaylistAvailable func(id string) bool
}

// Result of an evaluation.
type Result struct {
	Kind          string                     `json:"kind"`
	PlaylistID    string                     `json:"playlistId,omitempty"`
	RuleID        string                     `json:"ruleId,omitempty"`
	ActiveRuleIDs []string                   `json:"activeRuleIds"`
	Prices        map[string]contracts.Money `json:"prices"`
	// Ticker of the first active rule that has one (nil when none, and while the zone is
	// CLOSED: the closed content is shown as it is). TickerRuleID names that rule.
	Ticker       *contracts.RuleTicker `json:"ticker,omitempty"`
	TickerRuleID string                `json:"tickerRuleId,omitempty"`
	// TickerHidden: the first rule with a ticker setting hides it (TickerRuleID names it).
	TickerHidden bool `json:"tickerHidden,omitempty"`
	// Since anchors synchronous playback: the start of the time window of
	// the rule that supplies the playlist, midnight for a default rule, the moment the zone
	// was closed for closed content. Every screen plays position (now - Since) mod length.
	Since *time.Time `json:"since,omitempty"`
}

// Evaluate resolves the effective playlist and prices (ARCHITECTURE §5.4).
func Evaluate(in Input) Result {
	loc := in.Location
	if loc == nil {
		loc = time.UTC
	}
	local := in.Now.In(loc)
	available := in.PlaylistAvailable
	if available == nil {
		available = func(string) bool { return true }
	}

	// Each rule changes only what it sets; a playlist that is not available (deleted,
	// archived, not in the deployment) does not count, the rest of the rule still applies.
	var zoneWin, globalWin, defaults []contracts.Rule
	since := map[string]time.Time{}
	for _, r := range in.Rules {
		if !r.Enabled {
			continue
		}
		switch r.Scope {
		case contracts.ScopeZone:
			if r.ZoneID != in.ZoneID || r.Window == nil {
				continue
			}
			if at, ok := Occurrence(*r.Window, local); ok {
				zoneWin = append(zoneWin, r)
				since[r.ID] = at
			}
		case contracts.ScopeGlobal:
			if r.Window == nil {
				continue
			}
			if at, ok := Occurrence(*r.Window, local); ok {
				globalWin = append(globalWin, r)
				since[r.ID] = at
			}
		case contracts.ScopeDefault:
			if r.ZoneID == in.ZoneID {
				defaults = append(defaults, r)
				since[r.ID] = midnight(local)
			}
		}
	}
	// Deterministic order inside a tier (overlaps are rejected on save, but data from
	// older states must still evaluate identically everywhere).
	for _, tier := range [][]contracts.Rule{zoneWin, globalWin, defaults} {
		sort.Slice(tier, func(i, j int) bool { return tier[i].ID < tier[j].ID })
	}
	ordered := make([]contracts.Rule, 0, len(zoneWin)+len(globalWin)+len(defaults))
	ordered = append(ordered, zoneWin...)
	ordered = append(ordered, globalWin...)
	ordered = append(ordered, defaults...)

	res := Result{Kind: KindCleared, ActiveRuleIDs: []string{}, Prices: map[string]contracts.Money{}}
	for _, r := range ordered {
		res.ActiveRuleIDs = append(res.ActiveRuleIDs, r.ID)
		for _, p := range r.Prices {
			if p.ProductID == "" {
				continue // category price that was not expanded (ExpandPrices)
			}
			if _, set := res.Prices[p.ProductID]; !set {
				res.Prices[p.ProductID] = p.Price
			}
		}
	}

	if !in.ZoneClosed {
		for _, r := range ordered {
			if r.Ticker != nil { // same order as playlists: zone window, global window, default
				res.TickerRuleID = r.ID
				if r.Ticker.Hide {
					res.TickerHidden = true
				} else {
					t := *r.Ticker
					res.Ticker = &t
				}
				break
			}
		}
	}
	if in.ZoneClosed {
		closed := in.GroupClosedPlaylistID
		if closed == "" || !available(closed) {
			closed = in.ClosedPlaylistID
		}
		if closed != "" && available(closed) {
			res.Kind = KindClosed
			res.PlaylistID = closed
			at := in.ClosedSince
			if at.IsZero() {
				at = midnight(local)
			}
			res.Since = &at
			return res
		}
	}
	for _, r := range ordered {
		if id := PlaylistFor(r, in.GroupID, available); id != "" { // price-only rules leave the content as it is
			res.Kind = KindRule
			res.PlaylistID = id
			res.RuleID = r.ID
			at := since[r.ID]
			res.Since = &at
			return res
		}
	}
	return res
}

// PlaylistFor is the playlist a rule sets for a screen of the display group ("" = no
// group): its group's own playlist, else the playlist of all screens; "" when the rule sets
// none for it (or only unavailable ones).
func PlaylistFor(r contracts.Rule, groupID string, available func(string) bool) string {
	want := groupID
	if want == "" {
		want = contracts.NoGroupID
	}
	for _, g := range r.GroupPlaylists {
		if g.GroupID == want && available(g.PlaylistID) {
			return g.PlaylistID
		}
	}
	if r.PlaylistID != "" && available(r.PlaylistID) {
		return r.PlaylistID
	}
	return ""
}

// Changes lists what a rule sets: "playlist" (all screens),
// "playlist:<group>", "price:<product>", "price:category:<category>", "ticker". Rules whose windows overlap may not
// change the same thing.
func Changes(r contracts.Rule) []string {
	var out []string
	if r.PlaylistID != "" {
		out = append(out, "playlist")
	}
	for _, g := range r.GroupPlaylists {
		out = append(out, "playlist:"+g.GroupID)
	}
	for _, p := range r.Prices {
		if p.CategoryID != "" {
			out = append(out, "price:category:"+p.CategoryID)
		} else {
			out = append(out, "price:"+p.ProductID)
		}
	}
	if r.Ticker != nil {
		out = append(out, "ticker")
	}
	return out
}

// SharedChange returns the first thing both rules change ("" = none).
func SharedChange(a, b contracts.Rule) string {
	have := map[string]bool{}
	for _, c := range Changes(a) {
		have[c] = true
	}
	for _, c := range Changes(b) {
		if have[c] {
			return c
		}
	}
	return ""
}

func midnight(local time.Time) time.Time {
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
}

// Occurrence returns the start of the occurrence of the window that contains the local
// wall-clock time: the same day, or the day before for a window across midnight. An
// occurrence counts only when it starts on a day of the window's period (From/Until).
func Occurrence(w contracts.Window, local time.Time) (time.Time, bool) {
	start, err1 := contracts.ParseHHMM(w.Start)
	end, err2 := contracts.ParseHHMM(w.End)
	if err1 != nil || err2 != nil {
		return time.Time{}, false
	}
	today := midnight(local)
	yesterday := today.AddDate(0, 0, -1)
	at := func(d time.Time) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), start/60, start%60, 0, 0, local.Location())
	}
	m := local.Hour()*60 + local.Minute()
	if start < end {
		if hasDay(w.Days, isoWeekday(local)) && m >= start && m < end && InPeriod(w, today) {
			return at(today), true
		}
		return time.Time{}, false
	}
	// Crosses midnight (end == start: 24 hours).
	if hasDay(w.Days, isoWeekday(local)) && m >= start && InPeriod(w, today) {
		return at(today), true
	}
	if hasDay(w.Days, isoWeekday(yesterday)) && m < end && InPeriod(w, yesterday) {
		return at(yesterday), true
	}
	return time.Time{}, false
}

// WindowActive reports whether the window contains the given local wall-clock time.
func WindowActive(w contracts.Window, local time.Time) bool {
	_, ok := Occurrence(w, local)
	return ok
}

// InPeriod reports whether an occurrence starting on the given day (its date counts, the
// time is ignored) lies in the window's period.
func InPeriod(w contracts.Window, day time.Time) bool {
	d := day.Format("2006-01-02")
	return (w.From == "" || d >= w.From) && (w.Until == "" || d <= w.Until)
}

const minutesPerWeek = 7 * 24 * 60

// Overlaps reports whether two windows share at least one minute: in the weekly pattern
// and – for windows with a period – on actual dates.
func Overlaps(a, b contracts.Window) bool {
	weekly := false
	for _, x := range intervals(a) {
		for _, y := range intervals(b) {
			if x[0] < y[1] && y[0] < x[1] {
				weekly = true
			}
		}
	}
	if !weekly {
		return false
	}
	if a.From == "" && a.Until == "" && b.From == "" && b.Until == "" {
		return true
	}
	return datedOverlap(a, b)
}

// datedOverlap compares the occurrences of two windows (whose weekly patterns overlap) on
// the days both periods share. When they share an open-ended stretch or more than two
// weeks, every weekday combination occurs and the weekly overlap is real.
func datedOverlap(a, b contracts.Window) bool {
	fromA, untilA, okA := periodDays(a)
	fromB, untilB, okB := periodDays(b)
	if !okA || !okB {
		return false // invalid dates never apply
	}
	lo, hi := max(fromA, fromB), min(untilA, untilB)
	if lo == openFrom || hi == openUntil || hi-lo > 14 {
		return hi >= lo-1
	}
	startA, lenA := span(a)
	startB, lenB := span(b)
	valid := func(w contracts.Window, from, until, day int) bool {
		return day >= from && day <= until && hasDay(w.Days, contracts.ISOWeekdayOfDay(day))
	}
	// Occurrences that start more than a day apart cannot overlap (a window is at most 24 h).
	for x := lo - 1; x <= hi+1; x++ {
		if !valid(a, fromA, untilA, x) {
			continue
		}
		for y := x - 1; y <= x+1; y++ {
			if !valid(b, fromB, untilB, y) {
				continue
			}
			sa, sb := x*24*60+startA, y*24*60+startB
			if sa < sb+lenB && sb < sa+lenA {
				return true
			}
		}
	}
	return false
}

const (
	openFrom  = -1 << 30
	openUntil = 1 << 30
)

// periodDays returns the period of a window as day numbers (open ends as openFrom/openUntil).
func periodDays(w contracts.Window) (from, until int, ok bool) {
	from, until = openFrom, openUntil
	if w.From != "" {
		d, err := contracts.DayNumber(w.From)
		if err != nil {
			return 0, 0, false
		}
		from = d
	}
	if w.Until != "" {
		d, err := contracts.DayNumber(w.Until)
		if err != nil {
			return 0, 0, false
		}
		until = d
	}
	return from, until, true
}

// span returns the start minute and the length of a window (end <= start wraps).
func span(w contracts.Window) (start, length int) {
	start, _ = contracts.ParseHHMM(w.Start)
	end, _ := contracts.ParseHHMM(w.End)
	length = end - start
	if length <= 0 {
		length += 24 * 60
	}
	return start, length
}

// intervals expands a window into half-open minute-of-week intervals without wrap.
func intervals(w contracts.Window) [][2]int {
	start, err1 := contracts.ParseHHMM(w.Start)
	end, err2 := contracts.ParseHHMM(w.End)
	if err1 != nil || err2 != nil {
		return nil
	}
	length := end - start
	if length <= 0 {
		length += 24 * 60
	}
	var out [][2]int
	for _, d := range w.Days {
		s := (d-1)*24*60 + start
		e := s + length
		if e <= minutesPerWeek {
			out = append(out, [2]int{s, e})
		} else {
			out = append(out, [2]int{s, minutesPerWeek}, [2]int{0, e - minutesPerWeek})
		}
	}
	return out
}

func isoWeekday(t time.Time) int {
	wd := int(t.Weekday())
	if wd == 0 {
		return 7
	}
	return wd
}

func hasDay(days []int, d int) bool {
	for _, x := range days {
		if x == d {
			return true
		}
	}
	return false
}

// NextMinute returns the duration until the next full minute of t (plus a small margin),
// so evaluations happen right after every possible window boundary.
func NextMinute(t time.Time) time.Duration {
	next := t.Truncate(time.Minute).Add(time.Minute)
	return next.Sub(t) + 50*time.Millisecond
}
