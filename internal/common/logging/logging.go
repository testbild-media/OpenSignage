// Package logging sets up structured JSON logs with size-based rotation.
package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// previousTailBytes is how much of the log of the previous run is kept at start: enough
// for the last lines of a version that failed and was rolled back.
const previousTailBytes = 64 << 10

// crashFileMax: a crash file above this size starts anew.
const crashFileMax = 1 << 20

// RotatingFile is an io.Writer that rotates at MaxBytes and keeps Keep old files.
type RotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	f        *os.File
	size     int64
	previous []byte
}

// NewRotatingFile opens (or creates) path for appending.
func NewRotatingFile(path string, maxBytes int64, keep int) (*RotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	r := &RotatingFile{path: path, maxBytes: maxBytes, keep: keep, previous: readTail(path, previousTailBytes)}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size+int64(len(p)) > r.maxBytes && r.size > 0 {
		r.rotate()
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *RotatingFile) rotate() {
	_ = r.f.Close()
	r.f = nil
	for i := r.keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	_ = os.Rename(r.path, r.path+".1")
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep+1))
	if err := r.open(); err != nil {
		fmt.Fprintf(os.Stderr, "log rotation failed: %v\n", err)
	}
}

// Close closes the current file.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// PreviousTail is the end of the log as it was when this process started – the last lines
// of the previous run, at most 64 KiB, starting at a line.
func (r *RotatingFile) PreviousTail() []byte { return r.previous }

// readTail reads the last n bytes of a file from the first full line on (nil: no file).
func readTail(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	off := max(st.Size()-n, 0)
	b := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(b, off); err != nil && !errors.Is(err, io.EOF) {
		return nil
	}
	if off > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return b
}

// ReadTail is the end of a file (at most n bytes, from the first full line on).
func ReadTail(path string, n int64) []byte { return readTail(path, n) }

// OpenCrashFile opens the file that receives the fatal crash output (panics) of this
// process in addition to stderr – stderr only reaches the system journal, which nobody
// can read without access to the device. A file above 1 MiB starts anew.
func OpenCrashFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	flag := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if st, err := os.Stat(path); err == nil && st.Size() > crashFileMax {
		flag |= os.O_TRUNC
	}
	return os.OpenFile(path, flag, 0o640)
}

// CompactLines turns JSON log lines into short text lines ("2006-01-02 15:04:05 ERROR msg
// key=value …") and returns the last max of them. JSON lines older than since are left
// out (zero: none); other lines (e.g. crash output) stay as they are.
func CompactLines(b []byte, since time.Time, max int) []string {
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &m) != nil {
			out = append(out, line)
			continue
		}
		var sb strings.Builder
		if ts, ok := m["time"].(string); ok {
			t, err := time.Parse(time.RFC3339Nano, ts)
			if err == nil && !since.IsZero() && t.Before(since) {
				continue
			}
			if err == nil {
				sb.WriteString(t.Format("2006-01-02 15:04:05"))
			} else {
				sb.WriteString(ts)
			}
		}
		for _, k := range []string{"level", "msg"} {
			if v, ok := m[k]; ok {
				if sb.Len() > 0 {
					sb.WriteByte(' ')
				}
				fmt.Fprint(&sb, v)
			}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			if k != "time" && k != "level" && k != "msg" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := m[k]
			if s, ok := v.(string); ok {
				fmt.Fprintf(&sb, " %s=%s", k, s)
			} else if j, err := json.Marshal(v); err == nil {
				fmt.Fprintf(&sb, " %s=%s", k, j)
			}
		}
		out = append(out, sb.String())
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

// Files returns the current log file and its rotated siblings, newest first.
func (r *RotatingFile) Files() []string {
	out := []string{r.path}
	for i := 1; i <= r.keep; i++ {
		p := fmt.Sprintf("%s.%d", r.path, i)
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// ParseLevel converts "debug|info|warn|error".
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("invalid log level %q", s)
}

// Setup creates a JSON logger writing to stderr and to a rotating file in dir.
// The returned RotatingFile may be used to list/download logs.
func Setup(dir, name string, level slog.Level) (*slog.Logger, *RotatingFile, error) {
	rf, err := NewRotatingFile(filepath.Join(dir, name+".log"), 10<<20, 5)
	if err != nil {
		return nil, nil, err
	}
	w := io.MultiWriter(os.Stderr, rf)
	logger := slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	return logger, rf, nil
}
