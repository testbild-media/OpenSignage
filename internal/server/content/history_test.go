package content

import (
	"slices"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)

// hv builds a history: one entry per (minutes after t0, publisher).
func hv(entries ...any) []histVersion {
	var out []histVersion
	for i := 0; i < len(entries); i += 2 {
		out = append(out, histVersion{Version: int64(len(out) + 1), At: t0.Add(time.Duration(entries[i].(int)) * time.Minute), By: entries[i+1].(string)})
	}
	return out
}

func TestPhaseStarts(t *testing.T) {
	// 1-3 one phase (gaps under 60 min chain up), 4 after a 60 min pause, 5 by someone else,
	// 6 by the first person again
	vs := hv(0, "anna", 50, "anna", 100, "anna", 160, "anna", 161, "ben", 170, "anna")
	if got, want := phaseStarts(vs), []int64{1, 1, 1, 4, 5, 6}; !slices.Equal(got, want) {
		t.Fatalf("phases %v, want %v", got, want)
	}
}

func TestThinOutKeepsEverythingForTenDays(t *testing.T) {
	vs := hv(0, "anna", 5, "anna", 10, "anna", 20, "anna")
	now := vs[3].At.Add(thinAfter - time.Minute)
	if drop := thinOut(vs, 4, now); len(drop) != 0 {
		t.Fatalf("nothing is thinned out within %v: %v", thinAfter, drop)
	}
	// exactly thinAfter after the last publish of the phase: only the last version stays
	if drop := thinOut(vs, 4, vs[3].At.Add(thinAfter)); !slices.Equal(drop, []int64{1, 2, 3}) {
		t.Fatalf("drop %v", drop)
	}
}

func TestThinOutKeepsNamedPublishedAndPhaseEnds(t *testing.T) {
	// phase A: 1-4 (2 named), phase B: 5-7 (6 published), phase C: 8-9 recent
	vs := hv(0, "anna", 10, "anna", 20, "anna", 30, "anna", 600, "anna", 610, "anna", 620, "anna", 20000, "anna", 20010, "anna")
	vs[1].Label = "Summer menu"
	now := t0.Add(20010*time.Minute + time.Hour)
	if drop := thinOut(vs, 6, now); !slices.Equal(drop, []int64{1, 3, 5}) {
		t.Fatalf("drop %v, want [1 3 5]", drop)
	}
}

func TestThinOutKeepsTheNewestPhases(t *testing.T) {
	// keepPhases+2 phases of two versions each (other publisher every phase), all recent
	var entries []any
	for p := 0; p < keepPhases+2; p++ {
		by := []string{"anna", "ben"}[p%2]
		entries = append(entries, p*10, by, p*10+1, by)
	}
	vs := hv(entries...)
	vs[0].Label = "Opening" // named: stays although its phase is too old
	now := vs[len(vs)-1].At
	drop := thinOut(vs, vs[len(vs)-1].Version, now)
	if !slices.Equal(drop, []int64{2, 3, 4}) {
		t.Fatalf("drop %v, want the two oldest phases without the named version", drop)
	}
}

func TestThinOutLimitDropsOldestIntermediateVersions(t *testing.T) {
	var entries []any
	for i := 0; i < maxVersions+30; i++ {
		entries = append(entries, i, "anna") // one long phase, recent
	}
	vs := hv(entries...)
	last := vs[len(vs)-1]
	drop := thinOut(vs, last.Version, last.At)
	if len(drop) != 30 || drop[0] != 1 || drop[29] != 30 {
		t.Fatalf("the 30 oldest versions go: %v", drop)
	}
	if slices.Contains(drop, last.Version) {
		t.Fatal("the newest version always stays")
	}
}
