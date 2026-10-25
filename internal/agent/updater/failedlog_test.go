package updater

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFailedRunLog(t *testing.T) {
	since := time.Date(2026, 10, 24, 13, 13, 0, 0, time.UTC)
	prev := []byte(`{"time":"2026-10-24T13:10:00Z","level":"INFO","msg":"old version running"}
{"time":"2026-10-24T13:13:22Z","level":"INFO","msg":"started after update","to":"1.4.6"}
{"time":"2026-10-24T13:13:30Z","level":"ERROR","msg":"agent start failed","err":"listen tcp :8443: bind: address already in use"}
`)
	crash := filepath.Join(t.TempDir(), "crash.log")
	if err := os.WriteFile(crash, []byte("panic: boom\n\ngoroutine 1 [running]:\nmain.main()\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	got := FailedRunLog(prev, crash, since)
	if strings.Contains(got, "old version running") || !strings.Contains(got, "INFO started after update to=1.4.6") ||
		!strings.Contains(got, "address already in use") || !strings.Contains(got, "--- crash ---\npanic: boom") {
		t.Fatalf("log:\n%s", got)
	}
	// Crash output from before the update does not belong to it.
	old := since.Add(-time.Hour)
	if err := os.Chtimes(crash, old, old); err != nil {
		t.Fatal(err)
	}
	if got := FailedRunLog(prev, crash, since); strings.Contains(got, "crash") {
		t.Fatalf("old crash output included:\n%s", got)
	}
	// Lines without a time after the start: the last lines anyway; at most 12 KiB.
	if got := FailedRunLog(prev, "", since.Add(24*time.Hour)); !strings.Contains(got, "old version running") {
		t.Fatalf("fallback to the last lines:\n%s", got)
	}
	long := []byte(strings.Repeat("x"+strings.Repeat("y", 300)+"\n", 200))
	if got := FailedRunLog(long, "", time.Time{}); len(got) > failedLogMax || !strings.HasPrefix(got, "x") {
		t.Fatalf("not bounded: %d bytes", len(got))
	}
}
