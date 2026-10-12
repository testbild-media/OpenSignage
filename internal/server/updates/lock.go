package updates

// Data directory lock. systemd < 254 ignores RestartMode=direct: then every crash of the
// server also starts the recovery unit (OnFailure → `rollback`) while systemd already
// restarts the server. Without coordination the rollback could restore the database
// while the restarted version migrates it. Therefore `serve` holds an exclusive lock on
// <DataDir>/server.lock for its whole lifetime (taken before anything touches the
// database) and `rollback` holds it while it works. Linux only (flock); on other systems
// the lock always succeeds.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LockFile is the name of the lock file in the data directory.
const LockFile = "server.lock"

// ServeLockWait is how long `serve` waits for a running rollback before it gives up.
const ServeLockWait = 10 * time.Minute

// Tunables (tests shorten them).
var (
	rollbackLockWait = 2 * time.Minute // a running server keeps the lock: give up then
	lockPoll         = 250 * time.Millisecond
	lockLogEvery     = 15 * time.Second
	tryLock          = flockTry // replaced in tests on systems without flock
)

// ErrLocked means another process held the data directory lock for the whole wait.
var ErrLocked = errors.New("the data directory is locked by another opensignage-server process")

// DataLock is a held data directory lock.
type DataLock struct{ f *os.File }

// LockDataDir takes the exclusive data directory lock for role ("serve", "rollback"). While
// another process holds it, it waits up to wait (logging every few seconds) and then
// returns an error wrapping ErrLocked.
func LockDataDir(ctx context.Context, dataDir, role string, wait time.Duration, log *slog.Logger) (*DataLock, error) {
	path := filepath.Join(dataDir, LockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, fmt.Errorf("opening the lock file: %w", err)
	}
	start := time.Now()
	var lastLog time.Time
	for {
		ok, err := tryLock(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		if ok {
			break
		}
		waited := time.Since(start)
		if waited >= wait {
			_ = f.Close()
			return nil, fmt.Errorf("%w (%s) for more than %s", ErrLocked, lockHolder(path), wait)
		}
		if time.Since(lastLog) >= lockLogEvery {
			log.Warn("waiting for the data directory lock", "file", path, "holder", lockHolder(path),
				"waited", waited.Round(time.Second).String(), "max", wait.String())
			lastLog = time.Now()
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
	// Name the holder for the messages of a process that has to wait.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(fmt.Sprintf("%s, pid %d\n", role, os.Getpid())), 0)
	}
	return &DataLock{f: f}, nil
}

// Release gives the lock up (ending the process does the same).
func (l *DataLock) Release() {
	if l != nil && l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

// lockHolder describes the holder noted in the lock file.
func lockHolder(path string) string {
	b, err := os.ReadFile(path)
	if s := strings.TrimSpace(string(b)); err == nil && s != "" && len(s) < 100 {
		return "held by " + s
	}
	return "holder unknown"
}
