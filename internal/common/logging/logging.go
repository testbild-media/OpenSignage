// Package logging sets up structured JSON logs with size-based rotation.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// RotatingFile is an io.Writer that rotates at MaxBytes and keeps Keep old files.
type RotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	f        *os.File
	size     int64
}

// NewRotatingFile opens (or creates) path for appending.
func NewRotatingFile(path string, maxBytes int64, keep int) (*RotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	r := &RotatingFile{path: path, maxBytes: maxBytes, keep: keep}
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
