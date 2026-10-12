// Package safe runs goroutines that can never take the agent down: panics are
// recovered and logged, long-running loops are restarted with a delay.
package safe

import (
	"context"
	"log"
	"log/slog"
	"runtime/debug"
	"time"
)

// Go runs fn in a goroutine and logs a panic instead of crashing.
func Go(name string, fn func()) {
	go func() {
		defer Recover(name)
		fn()
	}()
}

// Recover logs a panic; use as `defer safe.Recover("name")`.
func Recover(name string) {
	if r := recover(); r != nil {
		slog.Error("panic recovered", "in", name, "panic", r, "stack", string(debug.Stack()))
	}
}

// HTTPErrorLog routes net/http server errors (TLS handshake noise, broken pipes) into
// slog at debug level instead of stderr.
func HTTPErrorLog() *log.Logger {
	return slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug)
}

// Loop runs fn until ctx ends. If fn panics or returns early it is restarted after a
// growing delay (1 s … 30 s), so a persistent bug cannot become a hot loop.
func Loop(ctx context.Context, name string, fn func(ctx context.Context)) {
	go func() {
		delay := time.Second
		for ctx.Err() == nil {
			started := time.Now()
			func() {
				defer Recover(name)
				fn(ctx)
			}()
			if ctx.Err() != nil {
				return
			}
			if time.Since(started) > time.Minute {
				delay = time.Second
			}
			slog.Warn("background loop stopped, restarting", "loop", name, "in", delay.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			delay = min(delay*2, 30*time.Second)
		}
	}()
}
