// Package updater installs new agent versions side by side (ARCHITECTURE §5.9): verify
// the signed release metadata with the agent's own trusted keys, download the release
// package with Range resume, verify its SHA-256 against the signed checksum, extract into
// releases/<v>, switch the `current` symlink atomically (keeping `previous`), and exit
// so systemd starts the new binary. The new version must prove itself healthy (server
// welcome + player ping within 5 minutes) or it rolls back to the previous release; the
// old version then reports `rolled_back`.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"opensignage/internal/agent/safe"
	"opensignage/internal/agent/wire"
	"opensignage/internal/common/atomicfile"
	"opensignage/internal/common/version"
)

// Phases reported in update.progress and shown in the client WebGUI.
const (
	PhaseIdle        = "idle"
	PhaseDownloading = "downloading"
	PhaseVerifying   = "verifying"
	PhaseInstalling  = "installing"
	PhaseRestarting  = "restarting"
	PhaseHealthCheck = "health_check"
	PhaseDone        = "done"
	PhaseFailed      = "failed"
	PhaseRolledBack  = "rolled_back"
)

// Error codes.
const (
	ErrNotInstalled     = "not_installed"
	ErrInvalidOffer     = "invalid_offer"
	ErrBusy             = "update_in_progress"
	ErrRecentlyFailed   = "recently_failed"
	ErrDiskFull         = "disk_full"
	ErrDownload         = "download_failed"
	ErrChecksum         = "checksum_mismatch"
	ErrPackageInvalid   = "package_invalid"
	ErrStorage          = "storage_error"
	ErrHealthTimeout    = "health_check_timeout"
	ErrTooManyStarts    = "too_many_start_attempts"
	ErrCrashLoop        = "crash_loop"
	ErrNoRollbackTarget = "rollback_impossible"
	ErrSignature        = "signature_invalid"
	ErrNoPackage        = "no_package"
)

// maxReleaseMeta bounds the signed release metadata answer of the server (release.json,
// SHA256SUMS and the signature, base64 in JSON).
const maxReleaseMeta = 16 << 20

// Getter performs a GET (with optional Range header) for a URL or server path.
type Getter func(ctx context.Context, target string, hdr http.Header) (*http.Response, error)

// Options configure the updater.
type Options struct {
	DataDir    string // agent data dir; working files live in <DataDir>/updates
	InstallDir string // contains releases/, current, previous; "" = updates unsupported
	Version    string // running version
	OS, Arch   string // package platform (linux, amd64|arm64|armv7)
	Server     Getter // pinned device channel (update.offer)
	Internet   Getter // plain HTTPS for the GitHub fallback (nil = disabled)
	FreeSpace  func(dir string) (uint64, error)
	// CheckBinary runs the extracted binary ("<bin> version") before switching.
	CheckBinary func(path, version string) error
	// Report sends update.progress to the server; false when not connected.
	Report func(wire.UpdateProgress) bool
	// SetUpdating switches the player into the "updating" screen.
	SetUpdating func(bool)
	// Exit ends the agent cleanly so systemd starts the binary behind `current`.
	Exit func(reason string)

	// TrustedKeys verify every release (server offers and the GitHub fallback): the
	// built-in keys plus OS_TRUSTED_KEYS of the agent configuration, never keys a server
	// sends.
	TrustedKeys   []string
	GitHubBase    string // https://github.com
	HealthTimeout time.Duration
	MaxAttempts   int           // starts of a new version before it rolls back (2)
	RetryAfter    time.Duration // a failed version is not retried automatically before (30 min)
	Now           func() time.Time
}

// State is <data>/updates/update-state.json.
type State struct {
	From      string `json:"from"`
	To        string `json:"to"`
	StartedAt string `json:"startedAt"`
	Attempts  int    `json:"attempts"`
	Source    string `json:"source"`          // server | github
	Phase     string `json:"phase,omitempty"` // "" = switched, health pending; rolled_back
	Error     string `json:"error,omitempty"`
}

// Status is the updater state for the WebGUI and the status report.
type Status struct {
	Phase     string    `json:"phase"`
	Version   string    `json:"version,omitempty"`
	Source    string    `json:"source,omitempty"`
	Done      int64     `json:"done,omitempty"`
	Total     int64     `json:"total,omitempty"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Updater coordinates downloads, the switch, the health check and rollbacks.
type Updater struct {
	o Options

	mu            sync.Mutex
	st            Status
	busy          bool
	healthPending bool
	healthOK      chan struct{}
	connected     bool
	pinged        bool
	pending       *wire.UpdateProgress // rolled_back report for the next connection
	failedVersion string
	failedAt      time.Time
}

// New creates an updater.
func New(o Options) *Updater {
	if o.HealthTimeout == 0 {
		o.HealthTimeout = 5 * time.Minute
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = 2
	}
	if o.RetryAfter == 0 {
		o.RetryAfter = 30 * time.Minute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.GitHubBase == "" {
		o.GitHubBase = "https://github.com"
	}
	if o.CheckBinary == nil {
		o.CheckBinary = CheckBinary
	}
	if o.Report == nil {
		o.Report = func(wire.UpdateProgress) bool { return false }
	}
	if o.SetUpdating == nil {
		o.SetUpdating = func(bool) {}
	}
	if o.Exit == nil {
		o.Exit = func(string) {}
	}
	return &Updater{o: o, st: Status{Phase: PhaseIdle, UpdatedAt: o.Now()}, healthOK: make(chan struct{})}
}

func (u *Updater) dir() string       { return filepath.Join(u.o.DataDir, "updates") }
func (u *Updater) statePath() string { return filepath.Join(u.dir(), "update-state.json") }

// Status returns the current state.
func (u *Updater) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.st
}

func (u *Updater) set(phase, ver, source string, done, total int64, errCode string) {
	u.mu.Lock()
	u.st = Status{Phase: phase, Version: ver, Source: source, Done: done, Total: total, Error: errCode, UpdatedAt: u.o.Now()}
	u.mu.Unlock()
}

func (u *Updater) report(phase, ver, source string, done, total int64, errCode string) {
	u.set(phase, ver, source, done, total, errCode)
	u.o.Report(wire.UpdateProgress{Version: ver, State: phase, Error: errCode, Done: done, Total: total})
}

// LoadState reads update-state.json (nil when absent).
func LoadState(dataDir string) (*State, error) {
	var st State
	err := atomicfile.ReadJSON(filepath.Join(dataDir, "updates", "update-state.json"), &st)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func saveState(dataDir string, st *State) error {
	return atomicfile.WriteJSON(filepath.Join(dataDir, "updates", "update-state.json"), st, 0o640)
}

func removeState(dataDir string) {
	if err := atomicfile.Remove(filepath.Join(dataDir, "updates", "update-state.json")); err != nil {
		slog.Warn("removing update-state.json failed", "err", err)
	}
}

// ---------------------------------------------------------------- start of a version

// Startup evaluates update-state.json once at agent start. It returns true when the
// agent must exit immediately (rollback performed).
func (u *Updater) Startup() bool {
	st, err := LoadState(u.o.DataDir)
	if err != nil {
		slog.Error("update-state.json unreadable, ignoring", "err", err)
		removeState(u.o.DataDir)
		return false
	}
	if st == nil {
		return false
	}
	switch {
	case st.To == u.o.Version && st.Phase == "":
		st.Attempts++
		if err := saveState(u.o.DataDir, st); err != nil {
			slog.Error("saving update-state.json failed", "err", err)
		}
		if st.Attempts > u.o.MaxAttempts {
			slog.Error("new version did not become healthy after several starts, rolling back", "version", st.To, "attempts", st.Attempts)
			return u.rollback(st, ErrTooManyStarts)
		}
		slog.Info("started after update, checking health", "from", st.From, "to", st.To, "attempt", st.Attempts)
		u.mu.Lock()
		u.healthPending = true
		u.mu.Unlock()
		u.set(PhaseHealthCheck, st.To, st.Source, 0, 0, "")
	case st.From == u.o.Version:
		reason := st.Error
		if st.Phase != PhaseRolledBack || reason == "" {
			reason = ErrCrashLoop // the new version never got far enough to write a note
		}
		slog.Warn("running the previous version after a failed update", "failed", st.To, "reason", reason)
		p := wire.UpdateProgress{Version: st.To, State: PhaseRolledBack, Error: reason}
		u.mu.Lock()
		u.pending = &p
		u.failedVersion, u.failedAt = st.To, u.o.Now()
		u.mu.Unlock()
		u.set(PhaseRolledBack, st.To, st.Source, 0, 0, reason)
	default:
		slog.Warn("stale update state removed", "from", st.From, "to", st.To, "running", u.o.Version)
		removeState(u.o.DataDir)
	}
	return false
}

// Run supervises the health check of a freshly installed version until ctx ends.
func (u *Updater) Run(ctx context.Context) {
	u.mu.Lock()
	pending := u.healthPending
	u.mu.Unlock()
	if !pending {
		return
	}
	t := time.NewTimer(u.o.HealthTimeout)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-u.healthOK:
	case <-t.C:
		u.mu.Lock()
		still := u.healthPending
		u.mu.Unlock()
		if !still {
			return
		}
		st, err := LoadState(u.o.DataDir)
		if err != nil || st == nil {
			return
		}
		slog.Error("new version not healthy in time, rolling back", "version", u.o.Version, "timeout", u.o.HealthTimeout.String())
		u.rollback(st, ErrHealthTimeout)
	}
}

// MarkConnected is called after the server's welcome.
func (u *Updater) MarkConnected() {
	u.mu.Lock()
	u.connected = true
	pending := u.pending
	u.mu.Unlock()
	if pending != nil && u.o.Report(*pending) {
		slog.Info("rollback reported to the server", "version", pending.Version)
		u.mu.Lock()
		u.pending = nil
		u.mu.Unlock()
		removeState(u.o.DataDir)
	}
	u.checkHealthy()
}

// MarkDisconnected is called when the device link drops.
func (u *Updater) MarkDisconnected() {
	u.mu.Lock()
	u.connected = false
	u.mu.Unlock()
}

// MarkPing is called for every player heartbeat.
func (u *Updater) MarkPing() {
	u.mu.Lock()
	first := !u.pinged
	u.pinged = true
	u.mu.Unlock()
	if first {
		u.checkHealthy()
	}
}

func (u *Updater) checkHealthy() {
	u.mu.Lock()
	ok := u.healthPending && u.connected && u.pinged
	if ok {
		u.healthPending = false
	}
	u.mu.Unlock()
	if !ok {
		return
	}
	st, _ := LoadState(u.o.DataDir)
	source := ""
	if st != nil {
		source = st.Source
	}
	slog.Info("update healthy", "version", u.o.Version)
	u.report(PhaseDone, u.o.Version, source, 0, 0, "")
	removeState(u.o.DataDir)
	close(u.healthOK)
	if u.o.InstallDir != "" {
		u.cleanupReleases()
	}
}

// rollback switches `current` back to the release the update came from and exits.
func (u *Updater) rollback(st *State, reason string) bool {
	if err := switchBack(u.o.InstallDir, st); err != nil {
		slog.Error("rollback impossible, staying on this version", "err", err)
		removeState(u.o.DataDir)
		u.mu.Lock()
		u.healthPending = false
		u.mu.Unlock()
		u.report(PhaseFailed, st.To, st.Source, 0, 0, ErrNoRollbackTarget)
		return false
	}
	st.Phase, st.Error = PhaseRolledBack, reason
	if err := saveState(u.o.DataDir, st); err != nil {
		slog.Error("saving update-state.json failed", "err", err)
	}
	u.mu.Lock()
	u.healthPending = false
	u.mu.Unlock()
	u.set(PhaseRolledBack, st.To, st.Source, 0, 0, reason)
	u.o.Exit("rollback to " + st.From)
	return true
}

// switchBack points current (and previous) at the release the update came from.
func switchBack(installDir string, st *State) error {
	if installDir == "" {
		return errors.New("install dir unknown")
	}
	if !version.Valid(st.From) {
		return fmt.Errorf("invalid previous version %q in update-state.json", st.From)
	}
	target := filepath.ToSlash(filepath.Join("releases", st.From))
	if _, err := os.Stat(filepath.Join(installDir, target, "bin", "opensignage-agent")); err != nil {
		return fmt.Errorf("previous release %s missing: %w", st.From, err)
	}
	if err := SetLink(installDir, "current", target); err != nil {
		return err
	}
	if err := SetLink(installDir, "previous", target); err != nil {
		slog.Warn("updating the previous link failed", "err", err)
	}
	slog.Warn("rolled back", "to", st.From, "failed", st.To)
	return nil
}

// Rollback is the `opensignage-agent rollback` command run by systemd (OnFailure) with
// the previous binary when the new one keeps crashing.
func Rollback(dataDir, installDir string) (string, error) {
	st, err := LoadState(dataDir)
	if err != nil {
		return "", err
	}
	if st == nil || st.Phase == PhaseRolledBack {
		return "nothing to roll back", nil
	}
	if err := switchBack(installDir, st); err != nil {
		return "", err
	}
	st.Phase, st.Error = PhaseRolledBack, ErrCrashLoop
	if err := saveState(dataDir, st); err != nil {
		return "", err
	}
	return fmt.Sprintf("rolled back from %s to %s", st.To, st.From), nil
}

// ---------------------------------------------------------------- symlinks

// ReadLink returns the target of installDir/name (relative, slash separated).
func ReadLink(installDir, name string) (string, error) {
	t, err := readlink(filepath.Join(installDir, name))
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(t) {
		if rel, err := filepath.Rel(installDir, t); err == nil && !strings.HasPrefix(rel, "..") {
			t = rel
		}
	}
	return filepath.ToSlash(t), nil
}

// SetLink atomically points installDir/name at target (symlink + rename).
func SetLink(installDir, name, target string) error {
	path := filepath.Join(installDir, name)
	tmp := path + ".new"
	_ = os.Remove(tmp)
	if err := symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	atomicfile.SyncDir(installDir)
	return nil
}

// cleanupReleases keeps only the releases behind `current` and `previous`.
func (u *Updater) cleanupReleases() {
	keep := map[string]bool{}
	for _, n := range []string{"current", "previous"} {
		if t, err := ReadLink(u.o.InstallDir, n); err == nil {
			keep[filepath.Base(t)] = true
		}
	}
	if len(keep) == 0 {
		return
	}
	entries, err := os.ReadDir(filepath.Join(u.o.InstallDir, "releases"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		p := filepath.Join(u.o.InstallDir, "releases", e.Name())
		if err := os.RemoveAll(p); err != nil {
			slog.Warn("removing old release failed", "dir", p, "err", err)
		} else {
			slog.Info("old release removed", "release", e.Name())
		}
	}
}

// ---------------------------------------------------------------- offers

// source describes where a package comes from.
type source struct {
	name    string // server | github
	version string
	url     string
	sha256  string
	size    int64
	get     Getter
}

func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

// Offer handles update.offer from the server. The offer alone is never trusted: the
// package checksum and size come from the signed release metadata of the server
// (resolveServer), and the offer must match them.
func (u *Updater) Offer(o wire.UpdateOffer) {
	if !version.Valid(o.Version) || !validSHA(o.SHA256) || o.Size <= 0 {
		slog.Warn("invalid update offer ignored", "version", o.Version)
		u.o.Report(wire.UpdateProgress{Version: o.Version, State: PhaseFailed, Error: ErrInvalidOffer})
		return
	}
	u.mu.Lock()
	healthPending := u.healthPending
	recent := u.failedVersion == o.Version && u.o.Now().Sub(u.failedAt) < u.o.RetryAfter
	u.mu.Unlock()
	if o.Version == u.o.Version {
		if !healthPending {
			u.o.Report(wire.UpdateProgress{Version: o.Version, State: PhaseDone})
		}
		return
	}
	if recent {
		slog.Warn("update offer for a version that just failed, not retrying yet", "version", o.Version)
		u.o.Report(wire.UpdateProgress{Version: o.Version, State: PhaseFailed, Error: ErrRecentlyFailed})
		return
	}
	pkgURL := o.URL
	if pkgURL == "" {
		pkgURL = fmt.Sprintf("/device/v1/update/package?version=%s&os=%s&arch=%s", o.Version, u.o.OS, u.o.Arch)
	}
	u.start("server", o.Version, func(ctx context.Context) (source, string, error) { return u.resolveServer(ctx, o, pkgURL) })
}

// start runs resolve (verified package source) and the installation in the background.
func (u *Updater) start(name, ver string, resolve func(context.Context) (source, string, error)) bool {
	u.mu.Lock()
	if u.busy || u.healthPending {
		u.mu.Unlock()
		slog.Warn("update already running", "requested", ver)
		u.o.Report(wire.UpdateProgress{Version: ver, State: PhaseFailed, Error: ErrBusy})
		return false
	}
	u.busy = true
	u.mu.Unlock()
	safe.Go("update "+ver, func() {
		defer func() {
			u.mu.Lock()
			u.busy = false
			u.mu.Unlock()
		}()
		ctx := context.Background()
		src, code, err := resolve(ctx)
		if err != nil {
			u.fail(source{name: name, version: ver}, code, err)
			return
		}
		u.install(ctx, src)
	})
	return true
}

// resolveServer fetches the signed release metadata of the offered version from the
// server and verifies it with the agent's own trusted keys. Checksum and size of the
// package come from the signed data; an offer that differs is refused, so a server (or
// whoever controls it) can never make the agent install a package the release key did
// not sign.
func (u *Updater) resolveServer(ctx context.Context, o wire.UpdateOffer, pkgURL string) (source, string, error) {
	if u.o.Server == nil {
		return source{}, ErrDownload, errors.New("no download source")
	}
	resp, err := u.o.Server(ctx, "/device/v1/update/release?"+url.Values{"version": {o.Version}}.Encode(), nil)
	if err != nil {
		return source{}, ErrDownload, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return source{}, ErrSignature, fmt.Errorf("the server provides no signed release metadata for %s (HTTP 404): the package cannot be verified", o.Version)
	default:
		return source{}, ErrDownload, fmt.Errorf("release metadata: HTTP %d", resp.StatusCode)
	}
	var meta wire.UpdateRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseMeta)).Decode(&meta); err != nil {
		return source{}, ErrSignature, fmt.Errorf("release metadata unreadable: %w", err)
	}
	pkg, code, err := u.verifyRelease(meta.Sums, meta.Sig, meta.Info, o.Version)
	if err != nil {
		return source{}, code, err
	}
	if o.SHA256 != pkg.SHA256 || o.Size != pkg.Size {
		return source{}, ErrSignature, fmt.Errorf("offer (sha256 %s, %d bytes) does not match the signed release (sha256 %s, %d bytes)",
			o.SHA256, o.Size, pkg.SHA256, pkg.Size)
	}
	slog.Info("release signature verified", "version", o.Version, "package", pkg.File)
	return source{name: "server", version: o.Version, url: pkgURL, sha256: pkg.SHA256, size: pkg.Size, get: u.o.Server}, "", nil
}

func (u *Updater) fail(src source, code string, err error) {
	slog.Error("update failed", "version", src.version, "source", src.name, "error", code, "err", err)
	u.o.SetUpdating(false)
	switch code {
	case ErrChecksum, ErrPackageInvalid, ErrSignature:
		// A broken package is not retried automatically for a while (no hot loop);
		// network errors may be retried at once (the download resumes).
		u.mu.Lock()
		u.failedVersion, u.failedAt = src.version, u.o.Now()
		u.mu.Unlock()
	}
	u.report(PhaseFailed, src.version, src.name, 0, 0, code)
}

func (u *Updater) install(ctx context.Context, src source) {
	slog.Info("update started", "from", u.o.Version, "to", src.version, "source", src.name, "size", src.size)
	if !version.Valid(src.version) { // names files and directories below
		u.fail(src, ErrInvalidOffer, fmt.Errorf("invalid version %q", src.version))
		return
	}
	if u.o.InstallDir == "" {
		u.fail(src, ErrNotInstalled, errors.New("not running from an installed release"))
		return
	}
	curTarget, err := ReadLink(u.o.InstallDir, "current")
	if err != nil {
		u.fail(src, ErrNotInstalled, err)
		return
	}
	if err := os.MkdirAll(u.dir(), 0o750); err != nil {
		u.fail(src, ErrStorage, err)
		return
	}
	if u.o.FreeSpace != nil {
		for dir, need := range map[string]uint64{u.dir(): uint64(src.size) + 50<<20, u.o.InstallDir: 3*uint64(src.size) + 100<<20} {
			if free, err := u.o.FreeSpace(dir); err == nil && free < need {
				u.fail(src, ErrDiskFull, fmt.Errorf("%s: %d bytes free, %d needed", dir, free, need))
				return
			}
		}
	}

	// 1. Download (resumable).
	part := filepath.Join(u.dir(), src.version+".part")
	last := time.Time{}
	progress := func(done int64) {
		if now := u.o.Now(); now.Sub(last) >= time.Second || done == src.size {
			last = now
			u.report(PhaseDownloading, src.version, src.name, done, src.size, "")
		}
	}
	u.report(PhaseDownloading, src.version, src.name, 0, src.size, "")
	sum, err := download(ctx, src.get, src.url, part, src.size, progress)
	if err != nil {
		u.fail(src, ErrDownload, err)
		return
	}

	// 2. Verify.
	u.report(PhaseVerifying, src.version, src.name, src.size, src.size, "")
	if sum != src.sha256 {
		_ = atomicfile.Remove(part)
		u.fail(src, ErrChecksum, fmt.Errorf("sha256 %s, expected %s", sum, src.sha256))
		return
	}
	pkg := filepath.Join(u.dir(), src.version+".tar.gz")
	if err := atomicfile.Rename(part, pkg); err != nil {
		u.fail(src, ErrStorage, err)
		return
	}
	defer func() { _ = atomicfile.Remove(pkg) }()

	// 3. Install side by side.
	u.report(PhaseInstalling, src.version, src.name, src.size, src.size, "")
	u.o.SetUpdating(true)
	relDir := filepath.Join(u.o.InstallDir, "releases")
	tmp := filepath.Join(relDir, src.version+".tmp")
	final := filepath.Join(relDir, src.version)
	if filepath.Base(curTarget) == src.version {
		u.fail(src, ErrPackageInvalid, errors.New("target release is the running one"))
		return
	}
	_ = os.RemoveAll(tmp)
	if err := Extract(pkg, tmp, 2<<30); err != nil {
		_ = os.RemoveAll(tmp)
		u.fail(src, ErrPackageInvalid, err)
		return
	}
	bin := filepath.Join(tmp, "bin", "opensignage-agent")
	if err := u.o.CheckBinary(bin, src.version); err != nil {
		_ = os.RemoveAll(tmp)
		u.fail(src, ErrPackageInvalid, err)
		return
	}
	_ = os.RemoveAll(final)
	if err := os.Rename(tmp, final); err != nil {
		_ = os.RemoveAll(tmp)
		u.fail(src, ErrStorage, err)
		return
	}
	atomicfile.SyncDir(relDir)

	// 4. Remember, then switch atomically (previous → running release).
	st := &State{From: u.o.Version, To: src.version, StartedAt: u.o.Now().UTC().Format(time.RFC3339), Source: src.name}
	if err := saveState(u.o.DataDir, st); err != nil {
		u.fail(src, ErrStorage, err)
		return
	}
	if err := SetLink(u.o.InstallDir, "previous", curTarget); err != nil {
		removeState(u.o.DataDir)
		u.fail(src, ErrStorage, err)
		return
	}
	if err := SetLink(u.o.InstallDir, "current", filepath.ToSlash(filepath.Join("releases", src.version))); err != nil {
		removeState(u.o.DataDir)
		u.fail(src, ErrStorage, err)
		return
	}
	slog.Info("update installed, restarting", "from", u.o.Version, "to", src.version)
	u.report(PhaseRestarting, src.version, src.name, src.size, src.size, "")
	time.Sleep(1500 * time.Millisecond) // let the progress message and the player screen go out
	u.o.Exit("update to " + src.version)
}

// download fetches url into part, resuming an existing partial file, and returns the
// SHA-256 of the complete file.
func download(ctx context.Context, get Getter, url, part string, size int64, progress func(int64)) (string, error) {
	if get == nil {
		return "", errors.New("no download source")
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	offset := st.Size()
	if offset > size {
		offset = 0
		if err := f.Truncate(0); err != nil {
			return "", err
		}
	}
	h := sha256.New()
	if offset > 0 {
		if _, err := io.Copy(h, io.NewSectionReader(f, 0, offset)); err != nil {
			return "", err
		}
	}
	if offset < size {
		var hdr http.Header
		if offset > 0 {
			hdr = http.Header{"Range": {fmt.Sprintf("bytes=%d-", offset)}}
		}
		resp, err := get(ctx, url, hdr)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusPartialContent:
			var start int64 = -1
			fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-", &start)
			if start != offset {
				return "", fmt.Errorf("unexpected content range %q", resp.Header.Get("Content-Range"))
			}
		case http.StatusOK:
			if offset > 0 {
				offset = 0
				h.Reset()
				if err := f.Truncate(0); err != nil {
					return "", err
				}
			}
		case http.StatusNotFound:
			return "", fmt.Errorf("package not found (HTTP 404)")
		default:
			return "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return "", err
		}
		w := &progressWriter{w: io.MultiWriter(f, h), n: offset, cb: progress}
		if _, err := io.Copy(w, io.LimitReader(resp.Body, size-offset)); err != nil {
			_ = f.Sync()
			return "", err // keep the partial file for resume
		}
		offset = w.n
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if offset != size {
		return "", fmt.Errorf("incomplete download: %d of %d bytes", offset, size)
	}
	if progress != nil {
		progress(size)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type progressWriter struct {
	w  io.Writer
	n  int64
	cb func(int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	if p.cb != nil {
		p.cb(p.n)
	}
	return n, err
}

// CheckBinary runs "<bin> version" and requires the expected version in the output.
func CheckBinary(bin, want string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("new binary does not run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[1] != want {
		return fmt.Errorf("package contains version %q, expected %s", strings.TrimSpace(string(out)), want)
	}
	return nil
}
