package updates

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// asServiceUser makes the root check see an unprivileged user (CI may run as root).
func asServiceUser(t *testing.T) {
	t.Helper()
	old := geteuid
	geteuid = func() int { return 1000 }
	t.Cleanup(func() { geteuid = old })
}

// emulateFlock stands in for flock where the system has none (the lock is a no-op
// there): the first open file that takes the lock holds it until it is closed. Linux
// tests use the real flock.
func emulateFlock(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "linux" {
		return
	}
	var mu sync.Mutex
	var holder *os.File
	old := tryLock
	tryLock = func(f *os.File) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if holder != nil && holder != f {
			if _, err := holder.Stat(); err == nil {
				return false, nil // still open
			}
		}
		holder = f
		return true, nil
	}
	t.Cleanup(func() { tryLock = old })
}

func shortLockWait(t *testing.T, d time.Duration) {
	t.Helper()
	old, oldPoll := rollbackLockWait, lockPoll
	rollbackLockWait, lockPoll = d, 20*time.Millisecond
	t.Cleanup(func() { rollbackLockWait, lockPoll = old, oldPoll })
}

// A running server (lock held) is never rolled back under its feet: the rollback waits
// and gives up when the server keeps running, and proceeds once the server process ended.
func TestRollbackWaitsForRunningServer(t *testing.T) {
	asServiceUser(t)
	emulateFlock(t)
	e := newInstallEnv(t, "1.0.0", "1.1.0")
	e.install(t, "1.1.0")
	setMarker(t, e.dbPath, "migrating")
	newDir := filepath.Join(e.root, "releases", "1.1.0")
	serve, err := LockDataDir(context.Background(), e.dataDir, "serve", time.Second, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	defer serve.Release()

	shortLockWait(t, 300*time.Millisecond)
	var out bytes.Buffer
	err = RollbackCommand(e.dataDir, e.root, e.links, &out, quietLog())
	if !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "serve, pid") {
		t.Fatalf("rollback must give up while the server holds the lock: %v", err)
	}
	if st, _ := LoadState(e.dataDir); st == nil || st.State != statePending {
		t.Fatalf("state changed: %+v", st)
	}
	if marker(t, e.dbPath) != "migrating" || e.link(t, "current") != newDir {
		t.Fatalf("a refused rollback must not change anything")
	}

	shortLockWait(t, 10*time.Second)
	time.AfterFunc(500*time.Millisecond, serve.Release) // the server process ends
	start := time.Now()
	if err := RollbackCommand(e.dataDir, e.root, e.links, &out, quietLog()); err != nil {
		t.Fatalf("rollback after the server ended: %v", err)
	}
	if time.Since(start) < 400*time.Millisecond {
		t.Fatalf("the rollback did not wait for the server")
	}
	if marker(t, e.dbPath) != "before-update" || e.link(t, "current") != e.oldDir {
		t.Fatalf("rollback incomplete")
	}
}

// serve waits (bounded) while a rollback holds the lock.
func TestServeWaitsForRollback(t *testing.T) {
	emulateFlock(t)
	old := lockPoll
	lockPoll = 20 * time.Millisecond
	t.Cleanup(func() { lockPoll = old })
	dir := t.TempDir()
	ctx := context.Background()
	rb, err := LockDataDir(ctx, dir, "rollback", time.Second, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan *DataLock, 1)
	go func() {
		l, err := LockDataDir(ctx, dir, "serve", 10*time.Second, quietLog())
		if err != nil {
			t.Error(err)
		}
		got <- l
	}()
	select {
	case <-got:
		t.Fatal("serve must wait while a rollback works")
	case <-time.After(300 * time.Millisecond):
	}
	rb.Release()
	var serve *DataLock
	select {
	case serve = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not get the lock after the rollback")
	}
	defer serve.Release()
	// The wait is bounded.
	if _, err := LockDataDir(ctx, dir, "serve", 100*time.Millisecond, quietLog()); !errors.Is(err, ErrLocked) {
		t.Fatalf("second server: %v", err)
	}
}

// A server process that waited while a rollback undid its version must not open (and
// migrate) the restored database.
func TestStartupAfterRollbackWhileWaiting(t *testing.T) {
	asServiceUser(t)
	e := newInstallEnv(t, "1.0.0", "1.1.0")
	e.install(t, "1.1.0")
	old := processStart
	processStart = time.Now() // started before the rollback finished
	t.Cleanup(func() { processStart = old })
	var out bytes.Buffer
	if err := RollbackCommand(e.dataDir, e.root, e.links, &out, quietLog()); err != nil {
		t.Fatal(err)
	}
	selfTest := func(context.Context) error { t.Error("the rolled back version must not run its self-test"); return nil }
	if err := Startup(context.Background(), e.dataDir, "1.1.0", e.links, selfTest, quietLog()); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("startup must stop: %v", err)
	}
	// A later start of that version (e.g. a manual re-install) is not affected.
	processStart = time.Now().Add(2 * time.Second)
	if err := Startup(context.Background(), e.dataDir, "1.1.0", e.links, selfTest, quietLog()); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackRefusesRoot(t *testing.T) {
	e := newInstallEnv(t, "1.0.0", "1.1.0")
	e.install(t, "1.1.0")
	setMarker(t, e.dbPath, "migrated")
	old := geteuid
	geteuid = func() int { return 0 }
	t.Cleanup(func() { geteuid = old })
	var out bytes.Buffer
	err := RollbackCommand(e.dataDir, e.root, e.links, &out, quietLog())
	if !errors.Is(err, ErrRunAsRoot) || !strings.Contains(err.Error(), "sudo -u opensignage") {
		t.Fatalf("root must be refused with a hint: %v", err)
	}
	if marker(t, e.dbPath) != "migrated" || e.link(t, "current") == e.oldDir {
		t.Fatalf("nothing may change")
	}
}

// The rollback only touches the configured database, backups in the data directory and
// releases of its own installation, whatever the (service user writable) state says.
func TestRollbackChecksStatePaths(t *testing.T) {
	asServiceUser(t)
	cases := map[string]func(e *installEnv, st *State) string{
		"foreign database":     func(e *installEnv, st *State) string { st.DBPath = filepath.Join(e.root, "other.db"); return e.root },
		"foreign release root": func(e *installEnv, st *State) string { st.Root = filepath.Dir(e.root); return e.root },
		"other installation":   func(e *installEnv, st *State) string { return filepath.Join(e.root, "x") },
		"not installed":        func(e *installEnv, st *State) string { return "" },
		"backup outside":       func(e *installEnv, st *State) string { st.Backup = filepath.Join(e.dataDir, "evil.db"); return e.root },
		"backup traversal": func(e *installEnv, st *State) string {
			st.Backup = filepath.Join(e.dataDir, "backups", "..", "..", "x.db")
			return e.root
		},
		"release outside": func(e *installEnv, st *State) string { st.FromDir = filepath.Join(e.root, "evil"); return e.root },
		"release traversal": func(e *installEnv, st *State) string {
			st.FromDir = filepath.Join(e.root, "releases", "..")
			return e.root
		},
		"older release outside": func(e *installEnv, st *State) string { st.PrevBefore = e.dataDir; return e.root },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := newInstallEnv(t, "1.0.0", "1.1.0")
			st := e.install(t, "1.1.0")
			setMarker(t, e.dbPath, "migrated")
			root := mutate(e, st)
			if err := saveState(e.dataDir, st); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := RollbackCommand(e.dataDir, root, e.links, &out, quietLog()); err == nil {
				t.Fatal("a state pointing outside the installation must be refused")
			}
			if marker(t, e.dbPath) != "migrated" || e.link(t, "current") == e.oldDir {
				t.Fatalf("nothing may change")
			}
		})
	}
}

func TestReleaseRoot(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "releases", "1.0.0", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(bin, "opensignage-server")
	_ = os.WriteFile(exe, []byte("x"), 0o755)
	want, _ := filepath.EvalSymlinks(root)
	if got, err := ReleaseRoot(exe); err != nil || got != want {
		t.Fatalf("root %q %v, want %q", got, err, want)
	}
	other := filepath.Join(root, "opensignage-server")
	_ = os.WriteFile(other, []byte("x"), 0o755)
	if _, err := ReleaseRoot(other); err == nil {
		t.Fatal("a binary outside the release layout has no release root")
	}
}
