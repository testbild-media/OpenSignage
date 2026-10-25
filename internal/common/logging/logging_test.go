package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The end of the previous run is kept at start, before this run writes anything.
func TestPreviousTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.log")
	var old strings.Builder
	for i := 0; i < 5000; i++ { // > 64 KiB
		old.WriteString(`{"time":"2026-10-24T13:13:22Z","level":"INFO","msg":"line"}` + "\n")
	}
	old.WriteString(`{"time":"2026-10-24T13:14:00Z","level":"ERROR","msg":"last of the previous run"}` + "\n")
	if err := os.WriteFile(path, []byte(old.String()), 0o640); err != nil {
		t.Fatal(err)
	}
	r, err := NewRotatingFile(path, 10<<20, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.Write([]byte(`{"msg":"this run"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	prev := string(r.PreviousTail())
	if len(prev) > previousTailBytes || !strings.HasPrefix(prev, "{") || !strings.HasSuffix(prev, "last of the previous run\"}\n") ||
		strings.Contains(prev, "this run") {
		t.Fatalf("previous tail: %d bytes, starts %q", len(prev), prev[:min(40, len(prev))])
	}
	fresh, err := NewRotatingFile(filepath.Join(dir, "new.log"), 10<<20, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if fresh.PreviousTail() != nil {
		t.Fatal("a new file has no previous run")
	}
}

func TestCompactLines(t *testing.T) {
	log := `{"time":"2026-10-24T13:10:00.5Z","level":"INFO","msg":"before the update"}
{"time":"2026-10-24T13:13:22.123456789Z","level":"INFO","msg":"started after update","from":"1.4.5","to":"1.4.6","attempt":1}
panic: runtime error: index out of range

{"time":"2026-10-24T13:13:30Z","level":"ERROR","msg":"agent start failed","err":{"code":"x"}}
`
	since := time.Date(2026, 10, 24, 13, 13, 0, 0, time.UTC)
	got := CompactLines([]byte(log), since, 10)
	want := []string{
		"2026-10-24 13:13:22 INFO started after update attempt=1 from=1.4.5 to=1.4.6",
		"panic: runtime error: index out of range",
		`2026-10-24 13:13:30 ERROR agent start failed err={"code":"x"}`,
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if got := CompactLines([]byte(log), time.Time{}, 2); len(got) != 2 || !strings.HasPrefix(got[0], "panic:") {
		t.Fatalf("last two: %q", got)
	}
}

func TestOpenCrashFileStartsAnewWhenLarge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "crash.log")
	f, err := OpenCrashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(strings.Repeat("x", crashFileMax+1))
	f.Close()
	f, err = OpenCrashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("fresh")
	f.Close()
	if b, _ := os.ReadFile(path); string(b) != "fresh" {
		t.Fatalf("crash file not started anew: %d bytes", len(b))
	}
}
