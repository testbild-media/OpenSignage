package updates

// Server update state machine (ARCHITECTURE §5.9):
//
//	old version (admin click):  backup DB (VACUUM INTO) + config copy → extract package into
//	    <root>/releases/<to> → check the new binary runs → write update-state.json
//	    (pending) → previous → old release, current → new release → exit 0
//	systemd restarts current = new version:
//	    Startup: attempts++ → (attempts > 2 → rollback) → migrations + self-test
//	        failure → restore DB backup, current → old release, state rolled_back, exit ≠ 0
//	        success → selfTestOk; the module finalizes once the listeners are up
//	        (update_runs done, audit, state removed, optional client rollout)
//	new version crashes before Startup (systemd start limit) → OnFailure runs
//	    `<previous>/bin/opensignage-server rollback` = RollbackCommand (same restore)
//	old version starts after a rollback: records update_runs rolled_back, removes state.
//
// `serve` and `rollback` hold the data directory lock (lock.go), so a rollback never
// replaces the database while a server process uses it.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"opensignage/internal/common/atomicfile"
)

// State of a server update, persisted in <DataDir>/updates/update-state.json.
type State struct {
	State          string `json:"state"` // pending | rolled_back
	From           string `json:"from"`
	To             string `json:"to"`
	RunID          string `json:"runId"`
	StartedBy      string `json:"startedBy"`
	StartedAt      string `json:"startedAt"`
	Backup         string `json:"backup"`       // database copy taken before the switch
	ConfigBackup   string `json:"configBackup"` // copy of the config file ("" = none)
	DBPath         string `json:"dbPath"`
	Root           string `json:"root"`       // release root (/opt/opensignage)
	FromDir        string `json:"fromDir"`    // release that was current before
	ToDir          string `json:"toDir"`      // release that is being started
	PrevBefore     string `json:"prevBefore"` // target of "previous" before the switch
	Attempts       int    `json:"attempts"`   // starts of the new version
	SelfTestOK     bool   `json:"selfTestOk"`
	RolloutClients bool   `json:"rolloutClients"`
	Error          string `json:"error,omitempty"`
	FinishedAt     string `json:"finishedAt,omitempty"`
	// RestartRequested: the rollback unit already restarted the restored version once.
	RestartRequested bool `json:"restartRequested,omitempty"`
}

const (
	statePending    = "pending"
	stateRolledBack = "rolled_back"

	// MaxStartAttempts is how often the new version may start without completing the
	// self-test before it rolls back by itself.
	MaxStartAttempts = 2

	maxPackageBytes   = 2 << 30
	maxPackageEntries = 100000
	keepPreUpdateDBs  = 3
)

// StatePath returns the update state file.
func StatePath(dataDir string) string { return filepath.Join(dataDir, "updates", "update-state.json") }

// LoadState reads the update state; (nil, nil) when there is none.
func LoadState(dataDir string) (*State, error) {
	var st State
	err := atomicfile.ReadJSON(StatePath(dataDir), &st)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func saveState(dataDir string, st *State) error {
	return atomicfile.WriteJSON(StatePath(dataDir), st, 0o640)
}

func clearState(dataDir string) error { return atomicfile.Remove(StatePath(dataDir)) }

// Updater installs server releases into the release layout.
type Updater struct {
	DataDir    string
	DBPath     string
	ConfigFile string
	Version    string  // running version
	Layout     *Layout // nil: not installed in the release layout (see LayoutErr)
	LayoutErr  error
	Now        func() time.Time
	// CheckBinary verifies that the extracted server binary runs on this machine and
	// reports the expected version.
	CheckBinary func(ctx context.Context, bin, want string) error
}

// InstallRequest describes one server update.
type InstallRequest struct {
	To             string
	Package        string // path of the verified package archive
	RunID          string
	StartedBy      string
	RolloutClients bool
	// Backup writes a consistent copy of the live database to dest (VACUUM INTO).
	Backup   func(ctx context.Context, dest string) error
	Progress func(step string)
}

// Install prepares the switch to req.To. On success the links point to the new
// release and the caller must end the process (exit 0) so systemd starts it. On error
// nothing was switched and the state file is removed.
func (u *Updater) Install(ctx context.Context, req InstallRequest) (*State, error) {
	l := u.Layout
	if l == nil {
		return nil, errNotInstalled
	}
	step := func(s string) {
		if req.Progress != nil {
			req.Progress(s)
		}
	}
	cur, err := l.Links.Read(l.currentLink())
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", l.currentLink(), err)
	}
	prevBefore, err := l.Links.Read(l.previousLink())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", l.previousLink(), err)
	}
	toDir := l.releaseDir(req.To)
	if sameDir(toDir, cur) {
		return nil, fmt.Errorf("release %s is already current", req.To)
	}
	if prevBefore != "" && sameDir(prevBefore, toDir) {
		prevBefore = "" // re-installing the previous release; after a rollback there is no older one
	}
	now := u.now()
	stamp := now.UTC().Format("20060102T150405Z")
	backups := filepath.Join(u.DataDir, "backups")
	if err := os.MkdirAll(backups, 0o750); err != nil {
		return nil, err
	}
	step("backup")
	dbBackup := filepath.Join(backups, fmt.Sprintf("pre-update-%s-%s.db", u.Version, stamp))
	if err := req.Backup(ctx, dbBackup); err != nil {
		return nil, fmt.Errorf("database backup: %w", err)
	}
	cfgBackup := ""
	if u.ConfigFile != "" {
		cfgBackup = filepath.Join(backups, fmt.Sprintf("pre-update-%s-%s.env", u.Version, stamp))
		if err := copyFile(u.ConfigFile, cfgBackup, 0o640); err != nil {
			_ = os.Remove(dbBackup)
			return nil, fmt.Errorf("config backup: %w", err)
		}
	}
	pruneBackups(backups, keepPreUpdateDBs)

	step("extract")
	tmp := toDir + ".tmp"
	_ = os.RemoveAll(tmp)
	fail := func(err error) (*State, error) {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	if err := extractPackage(req.Package, tmp); err != nil {
		return fail(fmt.Errorf("extracting package: %w", err))
	}
	if err := os.WriteFile(filepath.Join(tmp, "VERSION"), []byte(req.To+"\n"), 0o644); err != nil {
		return fail(err)
	}
	bin := filepath.Join(tmp, "bin", "opensignage-server")
	if _, err := os.Stat(bin); err != nil {
		return fail(fmt.Errorf("package has no server binary: %w", err))
	}
	if u.CheckBinary != nil {
		step("check")
		if err := u.CheckBinary(ctx, bin, req.To); err != nil {
			return fail(fmt.Errorf("new server binary does not run on this machine: %w", err))
		}
	}
	if err := os.RemoveAll(toDir); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, toDir); err != nil {
		return fail(err)
	}

	st := &State{State: statePending, From: u.Version, To: req.To, RunID: req.RunID, StartedBy: req.StartedBy,
		StartedAt: now.UTC().Format(time.RFC3339), Backup: dbBackup, ConfigBackup: cfgBackup, DBPath: u.DBPath,
		Root: l.Root, FromDir: cur, ToDir: toDir, PrevBefore: prevBefore, RolloutClients: req.RolloutClients}
	if err := saveState(u.DataDir, st); err != nil {
		return nil, fmt.Errorf("writing update state: %w", err)
	}
	step("switch")
	if err := l.Links.Set(l.previousLink(), cur); err != nil {
		_ = clearState(u.DataDir)
		return nil, fmt.Errorf("switching previous: %w", err)
	}
	if err := l.Links.Set(l.currentLink(), toDir); err != nil {
		restorePrevious(l.Links, l.previousLink(), prevBefore)
		_ = clearState(u.DataDir)
		return nil, fmt.Errorf("switching current: %w", err)
	}
	return st, nil
}

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func restorePrevious(links Links, link, target string) {
	if target != "" {
		_ = links.Set(link, target)
		return
	}
	_ = links.Remove(link)
}

// Startup runs before the server opens its database. When this version was just
// installed by an update it counts the start, runs the self-test and rolls back on
// failure. A returned error must end the process with a non-zero exit code; systemd
// then starts the previous version (the links already point to it again).
func Startup(ctx context.Context, dataDir, version string, links Links, selfTest func(context.Context) error, log *slog.Logger) error {
	if forcedStartupCrash != "" {
		panic(forcedStartupCrash)
	}
	st, err := LoadState(dataDir)
	if err != nil {
		bad := StatePath(dataDir) + ".corrupt"
		log.Error("update state unreadable, moved aside", "err", err, "file", bad)
		_ = os.Rename(StatePath(dataDir), bad)
		return nil
	}
	if st != nil && st.State == stateRolledBack && st.To == version && rolledBackSince(st, processStart) {
		// This process was started before a rollback switched the links back and waited
		// for the data directory lock meanwhile: it must not open (and migrate) the
		// restored database. systemd then starts the restored release.
		return fmt.Errorf("version %s was rolled back to %s while this process waited; exiting so the restored version starts", st.To, st.From)
	}
	if st == nil || st.State != statePending || st.To != version {
		return nil
	}
	st.Attempts++
	if err := saveState(dataDir, st); err != nil {
		log.Error("update: storing start attempt", "err", err)
	}
	log.Info("update: starting the new version", "from", st.From, "to", st.To, "attempt", st.Attempts)
	if st.Attempts > MaxStartAttempts {
		reason := fmt.Sprintf("version %s did not finish starting after %d attempts", st.To, MaxStartAttempts)
		if err := rollback(dataDir, st, links, reason, log); err != nil {
			return fmt.Errorf("%s; rollback failed: %w", reason, err)
		}
		return fmt.Errorf("%s; rolled back to %s", reason, st.From)
	}
	if err := selfTest(ctx); err != nil {
		reason := "self-test failed: " + err.Error()
		log.Error("update: self-test of the new version failed, rolling back", "err", err, "to", st.From)
		if rbErr := rollback(dataDir, st, links, reason, log); rbErr != nil {
			return fmt.Errorf("%s; rollback failed: %w", reason, rbErr)
		}
		return fmt.Errorf("update to %s rolled back to %s: %s", st.To, st.From, reason)
	}
	st.SelfTestOK = true
	if err := saveState(dataDir, st); err != nil {
		return fmt.Errorf("storing self-test result: %w", err)
	}
	log.Info("update: self-test passed", "version", version)
	return nil
}

// processStart is (about) when this process started (tests change it).
var processStart = time.Now()

// rolledBackSince reports whether the rollback recorded in st finished at or after t
// (FinishedAt has second precision).
func rolledBackSince(st *State, t time.Time) bool {
	finished, err := time.Parse(time.RFC3339, st.FinishedAt)
	return err == nil && !finished.Before(t.Truncate(time.Second))
}

// rollback restores the database backup and the previous links, then marks the state
// rolled_back (the old version records the result when it starts).
func rollback(dataDir string, st *State, links Links, reason string, log *slog.Logger) error {
	var errs []error
	if st.Backup != "" && st.DBPath != "" {
		if err := restoreDB(st.Backup, st.DBPath); err != nil {
			errs = append(errs, fmt.Errorf("restoring database: %w", err))
		} else {
			log.Info("update: database restored from backup", "backup", st.Backup)
		}
	}
	if st.Root != "" && st.FromDir != "" {
		if err := links.Set(filepath.Join(st.Root, "current"), st.FromDir); err != nil {
			errs = append(errs, fmt.Errorf("switching current back: %w", err))
		}
		restorePrevious(links, filepath.Join(st.Root, "previous"), st.PrevBefore)
	}
	st.State = stateRolledBack
	st.Error = reason
	if len(errs) > 0 {
		st.Error += " (" + errors.Join(errs...).Error() + ")"
	}
	st.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	if err := saveState(dataDir, st); err != nil {
		errs = append(errs, fmt.Errorf("writing update state: %w", err))
	}
	return errors.Join(errs...)
}

// ErrNothingToRollBack is returned by RollbackCommand when there is no pending update.
// The command then exits non-zero so the rollback unit does not restart the same
// (crashing) release again.
var ErrNothingToRollBack = errors.New("no pending update, nothing was rolled back")

// ErrRunAsRoot is returned when `opensignage-server rollback` runs as root.
var ErrRunAsRoot = errors.New("refusing to run as root: the update state and the backups are files of the service user; " +
	"run the rollback as that user, e.g. sudo -u opensignage /opt/opensignage/previous/bin/opensignage-server rollback")

// geteuid is replaced in tests.
var geteuid = os.Geteuid

// RollbackCommand implements `opensignage-server rollback`, run as the service user by
// the PREVIOUS release's binary (systemd OnFailure) when the new version keeps crashing
// before it could check itself. It needs no root rights (the service user owns the
// release root and the data directory) and refuses to run as root, because everything it
// reads can be written by the service user. root is the release root of the running
// binary (ReleaseRoot); the update state must name exactly this installation. It holds
// the data directory lock while it works, so it never runs alongside a server.
func RollbackCommand(dataDir, root string, links Links, out io.Writer, log *slog.Logger) error {
	if geteuid() == 0 {
		return ErrRunAsRoot
	}
	if _, err := os.Stat(StatePath(dataDir)); errors.Is(err, os.ErrNotExist) {
		return ErrNothingToRollBack // also after every crash on systemd < 254: no lock wait
	}
	lock, err := LockDataDir(context.Background(), dataDir, "rollback", rollbackLockWait, log)
	if errors.Is(err, ErrLocked) {
		return fmt.Errorf("a server process is running (%w); nothing was rolled back", err)
	}
	if err != nil {
		return err
	}
	defer lock.Release()
	st, err := LoadState(dataDir)
	if err != nil {
		return err
	}
	switch {
	case st == nil:
		return ErrNothingToRollBack
	case st.State == stateRolledBack && !st.RestartRequested:
		// The new version already rolled itself back, but the restored version has not
		// started yet (it removes the state when it does). With systemd's default
		// RestartMode=normal this unit also runs after that self-rollback; when the
		// start limit was reached at the same moment, only this restart brings the
		// server back. Once only: a crashing old version must not loop.
		st.RestartRequested = true
		if err := saveState(dataDir, st); err != nil {
			return err
		}
		fmt.Fprintf(out, "update to %s was already rolled back; starting %s again\n", st.To, st.From)
		return nil
	case st.State == stateRolledBack:
		return fmt.Errorf("%w (update to %s was already rolled back to %s)", ErrNothingToRollBack, st.To, st.From)
	}
	if err := checkRollbackState(st, dataDir, root); err != nil {
		return err
	}
	reason := fmt.Sprintf("version %s did not start; rolled back by version %s", st.To, st.From)
	if err := rollback(dataDir, st, links, reason, log); err != nil {
		return err
	}
	fmt.Fprintf(out, "rolled back from %s to %s\n", st.To, st.From)
	return nil
}

// checkRollbackState refuses an update state that points outside this installation. The
// rollback replaces the configured database with a backup from <DataDir>/backups and
// switches the links of the release root of the running binary to releases in it;
// nothing else.
func checkRollbackState(st *State, dataDir, root string) error {
	inDir := func(p, dir string) bool { return p != "" && filepath.Dir(filepath.Clean(p)) == filepath.Clean(dir) }
	releases := filepath.Join(root, "releases")
	backups := filepath.Join(dataDir, "backups")
	switch {
	case root == "":
		return errors.New("rollback: this binary is not part of an installed release (<root>/releases/<version>/bin)")
	case filepath.Clean(st.Root) != filepath.Clean(root):
		return fmt.Errorf("rollback: the update state names the release root %q, this installation is %q", st.Root, root)
	case filepath.Clean(st.DBPath) != filepath.Clean(dbPath(dataDir)):
		return fmt.Errorf("rollback: the update state names the database %q, the configured one is %q", st.DBPath, dbPath(dataDir))
	case !inDir(st.Backup, backups):
		return fmt.Errorf("rollback: the database backup %q is not in %s", st.Backup, backups)
	case !inDir(st.FromDir, releases):
		return fmt.Errorf("rollback: the previous release %q is not in %s", st.FromDir, releases)
	case st.PrevBefore != "" && !inDir(st.PrevBefore, releases):
		return fmt.Errorf("rollback: the older release %q is not in %s", st.PrevBefore, releases)
	}
	return nil
}

// restoreDB replaces the live database with the backup. The server must not use the
// database (it is stopped or has not opened it). WAL and shared memory files of the
// replaced database are removed, otherwise SQLite would apply them to the backup.
func restoreDB(backup, live string) error {
	if _, err := os.Stat(backup); err != nil {
		return err
	}
	tmp := live + ".restore-tmp"
	if err := copyFile(backup, tmp, 0o640); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := atomicfile.Remove(live + suffix); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := atomicfile.Rename(tmp, live); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	atomicfile.SyncDir(filepath.Dir(live))
	return nil
}

// copyFile copies src to dst (fsynced).
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// pruneBackups keeps the newest keep pre-update database backups (and their config
// copies).
func pruneBackups(dir string, keep int) {
	dbs, _ := filepath.Glob(filepath.Join(dir, "pre-update-*.db"))
	if len(dbs) <= keep {
		return
	}
	sort.Slice(dbs, func(i, j int) bool { return modTime(dbs[i]).After(modTime(dbs[j])) })
	for _, p := range dbs[keep:] {
		_ = os.Remove(p)
		_ = os.Remove(strings.TrimSuffix(p, ".db") + ".env")
	}
}

func modTime(p string) time.Time {
	if st, err := os.Stat(p); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

// extractPackage unpacks a release package (tar.gz with one top directory) into dest,
// stripping the top directory. Only regular files and directories are accepted; paths
// escaping dest are refused.
func extractPackage(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	for n := 0; ; n++ {
		if n > maxPackageEntries {
			return errors.New("too many entries")
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		clean := path.Clean(strings.TrimPrefix(filepath.ToSlash(h.Name), "./"))
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("unexpected entry %q", h.Name)
		}
		_, rel, nested := strings.Cut(clean, "/")
		if !nested || rel == "" {
			if h.Typeflag == tar.TypeDir {
				continue // the top directory itself
			}
			return fmt.Errorf("entry %q is outside the package directory", h.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if total > maxPackageBytes {
				return errors.New("package too large")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.FileInfo().Mode().Perm()&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, io.LimitReader(tr, h.Size))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported entry type in %q", h.Name)
		}
	}
}

// checkBinary runs `<bin> version` and expects the version in the output.
func checkBinary(ctx context.Context, bin, want string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	// "opensignage-server <version> (<commit>)"
	if f := strings.Fields(string(out)); len(f) < 2 || f[1] != want {
		return fmt.Errorf("binary reports %q, expected version %s", strings.TrimSpace(string(out)), want)
	}
	return nil
}
