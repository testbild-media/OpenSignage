package sysinfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	writeProc(t, dir, "a", "agent.log", strings.Repeat("x", 100))
	writeProc(t, dir, "a", "agent.log.1", strings.Repeat("y", 23))
	writeProc(t, dir, "b", "deeper.log", "zz")
	if got := DirSize(dir); got != 125 {
		t.Fatalf("DirSize = %d, want 125", got)
	}
	if got := DirSize(filepath.Join(dir, "missing")); got != 0 {
		t.Fatalf("missing dir = %d", got)
	}
}

func writeProc(t *testing.T, root, pid, name, content string) {
	t.Helper()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// PSS of the whole process tree; a process without smaps_rollup counts with its RSS.
func TestProcessTreePSS(t *testing.T) {
	root := t.TempDir()
	old := procRoot
	procRoot = root
	defer func() { procRoot = old }()
	writeProc(t, root, "10", "stat", "10 (chromium) S 1 10 10 0 -1")
	writeProc(t, root, "10", "smaps_rollup", "00400000-7ffd [rollup]\nRss:  900 kB\nPss:  100 kB\n")
	writeProc(t, root, "10", "status", "Name:\tchromium\nVmRSS:\t900 kB\n")
	writeProc(t, root, "11", "stat", "11 (chromium renderer) S 10 10 10 0 -1")
	writeProc(t, root, "11", "status", "Name:\tchromium\nVmRSS:\t50 kB\n")
	writeProc(t, root, "12", "stat", "12 (other) S 1 12 12 0 -1")
	writeProc(t, root, "12", "smaps_rollup", "Pss:  7000 kB\n")
	if got, want := ProcessTreePSS(10), uint64(150*1024); got != want {
		t.Fatalf("PSS %d, want %d", got, want)
	}
	if got, want := ProcessTreeRSS(10), uint64(950*1024); got != want {
		t.Fatalf("RSS %d, want %d", got, want)
	}
}

// The Raspberry Pi under-voltage alarm (hwmon "rpi_volt") is found, read and counted on
// rising edges; other platforms report nothing.
func TestPowerUnderVoltage(t *testing.T) {
	root := t.TempDir()
	old := sysRoot
	sysRoot = root
	defer func() { sysRoot = old }()
	if NewPower().Available() {
		t.Fatal("no hwmon: not available")
	}
	dir := filepath.Join(root, "class", "hwmon", "hwmon1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "name"), []byte("rpi_volt\n"), 0o644)
	alarm := filepath.Join(dir, "in0_lcrit_alarm")
	os.WriteFile(alarm, []byte("0\n"), 0o644)
	p := NewPower()
	if !p.Available() {
		t.Fatal("rpi_volt not found")
	}
	t0 := time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC)
	p.Check(t0)
	for i, v := range []string{"1", "1", "0", "1"} {
		os.WriteFile(alarm, []byte(v+"\n"), 0o644)
		p.Check(t0.Add(time.Duration(i+1) * 2 * time.Second))
	}
	st, ok := p.State()
	if !ok || !st.UnderVoltage || st.Events != 2 || !st.Last.Equal(t0.Add(8*time.Second)) {
		t.Fatalf("state %+v", st)
	}
}
