// Package updates implements the release and update system (ARCHITECTURE §5.9): update
// check against the GitHub releases API of the configured repository, download with
// resume and signature verification, offline bundle upload, the server self-update with
// automatic rollback, and the rolling client update through the device link
// (update.offer / update.progress).
//
// Ownership (ARCHITECTURE §7): releases, update_runs, device_updates, device_platforms
// and the trusted keys in the settings. Events: topic `updates`.
package updates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"opensignage/internal/common/atomicfile"
	"opensignage/internal/common/release"
	"opensignage/internal/common/version"
	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/devices"
	"opensignage/internal/server/httpx"
)

// DeviceLink is the part of the devices module the rollout uses (fakes in tests).
type DeviceLink interface {
	SendUpdateOffer(deviceID string, offer contracts.UpdateOffer) bool
	Online(deviceID string) bool
}

// Operation is the long running download, upload or server installation (one at a time).
type Operation struct {
	Kind      string `json:"kind"` // download | upload | install
	Version   string `json:"version"`
	Step      string `json:"step"`
	Done      int64  `json:"done"`
	Total     int64  `json:"total"`
	StartedAt string `json:"startedAt"`
	StartedBy string `json:"startedBy"`
	RunID     string `json:"runId,omitempty"`
}

// Module is the updates module.
type Module struct {
	app     *core.App
	log     *slog.Logger
	gh      *ghClient
	updater *Updater

	linkMu sync.Mutex
	link   DeviceLink

	// exit ends the process after the release links were switched (0 → graceful
	// shutdown, systemd restarts the new version). Tests replace it.
	exit func(code int)

	mu       sync.Mutex
	op       *Operation
	lastPub  time.Time
	checking bool
	baseCtx  context.Context

	rolloutMu sync.Mutex // serializes all rollout state changes
	kick      chan struct{}

	// Tunables (tests shorten them).
	DeviceTimeout time.Duration // a device must come back with the new version within this time
	// ReconnectGrace is how long the client rollout after a server update waits for the
	// screens that were connected before the update to reconnect.
	ReconnectGrace time.Duration
	TickInterval   time.Duration
	CheckInterval  time.Duration // periodic update check (hint only)
	CheckDelay     time.Duration // first periodic check after start
	RestartDelay   time.Duration // lets the HTTP response leave before the restart
}

var (
	_ devices.PackageProvider = (*Module)(nil)
	_ devices.UpdateListener  = (*Module)(nil)
)

// New creates the module and plugs it into the devices module.
func New(a *core.App) *Module {
	log := a.Log
	if log == nil {
		log = slog.Default()
	}
	m := &Module{
		app:            a,
		log:            log.With("module", "updates"),
		gh:             newGHClient(),
		exit:           defaultExit,
		baseCtx:        context.Background(),
		kick:           make(chan struct{}, 1),
		DeviceTimeout:  10 * time.Minute,
		ReconnectGrace: 2 * time.Minute,
		TickInterval:   2 * time.Second,
		CheckInterval:  24 * time.Hour,
		CheckDelay:     time.Minute,
		RestartDelay:   time.Second,
	}
	m.updater = &Updater{DataDir: a.Cfg.DataDir, DBPath: dbPath(a.Cfg.DataDir), ConfigFile: a.Cfg.File,
		Version: version.Version, CheckBinary: checkBinary}
	if exe, err := os.Executable(); err != nil {
		m.updater.LayoutErr = err
	} else if l, err := DetectLayout(exe, Symlinks{}); err != nil {
		m.updater.LayoutErr = err
	} else {
		m.updater.Layout = l
	}
	m.attachDevices()
	return m
}

func dbPath(dataDir string) string { return filepath.Join(dataDir, "db", "opensignage.db") }

// attachDevices registers the package provider and progress listener with the devices
// module (if it exists in this build).
func (m *Module) attachDevices() {
	d := devices.For(m.app)
	if d == nil {
		return
	}
	m.linkMu.Lock()
	already := m.link != nil
	if !already {
		m.link = d
	}
	m.linkMu.Unlock()
	if !already {
		d.SetPackageProvider(m)
		d.SetUpdateListener(m)
	}
}

func (m *Module) deviceLink() DeviceLink {
	m.linkMu.Lock()
	defer m.linkMu.Unlock()
	return m.link
}

// defaultExit ends the process. Code 0 asks for a graceful shutdown (SIGTERM to
// ourselves → the server closes connections and the database, then exits 0).
func defaultExit(code int) {
	if code == 0 {
		if p, err := os.FindProcess(os.Getpid()); err == nil && p.Signal(syscall.SIGTERM) == nil {
			time.AfterFunc(30*time.Second, func() { os.Exit(0) })
			return
		}
	}
	os.Exit(code)
}

// Routes registers the WebGUI API (443). All update actions are admin only.
func (m *Module) Routes(rt *core.Router) {
	rt.Role("GET /api/v1/updates", core.RoleAdmin, m.overview)
	rt.Role("POST /api/v1/updates/check", core.RoleAdmin, m.checkHandler)
	rt.Role("POST /api/v1/updates/releases/{version}/download", core.RoleAdmin, m.downloadHandler)
	rt.Role("DELETE /api/v1/updates/releases/{version}", core.RoleAdmin, m.deleteRelease)
	rt.Role("POST /api/v1/updates/upload", core.RoleAdmin, m.uploadHandler)
	rt.Role("POST /api/v1/updates/server/install", core.RoleAdmin, m.installHandler)
	rt.Role("GET /api/v1/updates/clients", core.RoleAdmin, m.clientsHandler)
	rt.Role("POST /api/v1/updates/clients/start", core.RoleAdmin, m.startHandler)
	rt.Role("POST /api/v1/updates/clients/stop", core.RoleAdmin, m.stopHandler)
	rt.Role("POST /api/v1/devices/{id}/align", core.RoleAdmin, m.alignHandler)
	rt.Role("GET /api/v1/updates/runs", core.RoleAdmin, m.runsHandler)
	rt.Role("GET /api/v1/updates/keys", core.RoleAdmin, m.keysHandler)
	rt.Role("POST /api/v1/updates/keys", core.RoleAdmin, m.addKeyHandler)
	rt.Role("DELETE /api/v1/updates/keys/{id}", core.RoleAdmin, m.removeKeyHandler)
}

// Run finalizes a server update, recovers interrupted work, runs the rollout scheduler
// and the periodic update check. The app restarts it after a panic.
func (m *Module) Run(ctx context.Context) {
	m.mu.Lock()
	m.baseCtx = ctx
	m.mu.Unlock()
	m.attachDevices()
	m.finishServerUpdate(ctx)
	m.recoverInterrupted(ctx)
	m.backfillPlatforms(ctx)

	sub := m.app.Bus.Subscribe(256)
	defer sub.Close()
	tick := time.NewTicker(m.TickInterval)
	defer tick.Stop()
	check := time.NewTimer(m.nextCheckIn(time.Now()))
	defer check.Stop()
	m.safe("rollout", func() { m.rolloutTick(ctx) })
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-sub.C:
			m.safe("event", func() { m.onEvent(ctx, e) })
		case <-m.kick:
			m.safe("rollout", func() { m.rolloutTick(ctx) })
		case <-tick.C:
			m.safe("rollout", func() { m.rolloutTick(ctx) })
		case <-check.C:
			m.goSafe("periodic check", func() { m.periodicCheck(ctx) })
			check.Reset(m.CheckInterval)
		}
	}
}

// safe runs f and contains panics (one bad event never stops the scheduler).
func (m *Module) safe(what string, f func()) {
	defer func() {
		if p := recover(); p != nil {
			m.log.Error("updates: panic", "in", what, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
		}
	}()
	f()
}

func (m *Module) goSafe(what string, f func()) { go m.safe(what, f) }

// ctx returns the module's base context (server lifetime) for background work.
func (m *Module) ctx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.baseCtx
}

func (m *Module) triggerRollout() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *Module) publish(kind, id string, data any) {
	m.app.Bus.Publish(core.Event{Topic: core.TopicUpdates, Kind: kind, ID: id, Data: data})
}

func (m *Module) updatesDir() string         { return filepath.Join(m.app.Cfg.DataDir, "updates") }
func (m *Module) releaseDir(v string) string { return filepath.Join(m.updatesDir(), v) }

// serverPlatform is the OS/architecture of this server's package.
func serverPlatform() (string, string) { return runtime.GOOS, release.Arch(runtime.GOARCH, "") }

// trustedKeys = built-in keys + keys added by the admin. They decide what this server
// accepts; devices verify releases with their own keys (built-in + OS_TRUSTED_KEYS).
func (m *Module) trustedKeys() []string {
	return append(release.BuiltinKeys(), m.app.Settings.Get().TrustedKeys...)
}

// repo is the effective update source.
func (m *Module) repo() string {
	if r := m.app.Settings.Get().UpdateRepo; r != "" {
		return r
	}
	return m.app.Cfg.UpdateRepo
}

// ---------------------------------------------------------------- operations

// begin starts the single long operation; errBusy when another one runs.
func (m *Module) begin(kind, ver string, p *core.Principal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.op != nil {
		return errBusy.With("operation", m.op.Kind).With("version", m.op.Version)
	}
	if st, err := LoadState(m.app.Cfg.DataDir); err == nil && st != nil && st.State == statePending {
		return errUpdateInProgress.With("version", st.To)
	}
	by := "system"
	if p != nil {
		by = p.Username
	}
	m.op = &Operation{Kind: kind, Version: ver, StartedAt: time.Now().UTC().Format(time.RFC3339), StartedBy: by}
	m.lastPub = time.Time{}
	return nil
}

func (m *Module) end() {
	m.mu.Lock()
	op := m.op
	m.op = nil
	m.mu.Unlock()
	if op != nil {
		m.publish("operation", op.Version, map[string]any{"kind": op.Kind, "state": "finished"})
	}
}

// progress updates the operation and publishes at most every 500 ms (always on step
// changes).
func (m *Module) progress(step string, done, total int64) {
	m.mu.Lock()
	if m.op == nil {
		m.mu.Unlock()
		return
	}
	stepChanged := m.op.Step != step
	m.op.Step, m.op.Done, m.op.Total = step, done, total
	now := time.Now()
	if !stepChanged && now.Sub(m.lastPub) < 500*time.Millisecond && done < total {
		m.mu.Unlock()
		return
	}
	m.lastPub = now
	op := *m.op
	m.mu.Unlock()
	m.publish("operation", op.Version, op)
}

func (m *Module) currentOp() *Operation {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.op == nil {
		return nil
	}
	c := *m.op
	return &c
}

// ---------------------------------------------------------------- startup recovery

// recoverInterrupted marks downloads that were cut off by a restart as failed (their
// partial files stay; the next download resumes them).
func (m *Module) recoverInterrupted(ctx context.Context) {
	if m.currentOp() != nil {
		return // Run restarted after a panic while an operation is still active
	}
	if _, err := m.app.DB.W.ExecContext(ctx, `UPDATE releases SET state = ?, error = ?, updated_at = ? WHERE state = ?`,
		relFailed, codeErr("download_interrupted", "server restarted during the download"), db.Now(), relDownloading); err != nil {
		m.log.Error("updates: recovering interrupted downloads", "err", err)
	}
}

// backfillPlatforms copies os/arch from pairing requests for devices without a
// platform record.
func (m *Module) backfillPlatforms(ctx context.Context) {
	list, err := m.devices(ctx, `WHERE d.token_hash IS NOT NULL AND (p.device_id IS NULL OR p.os = '' OR p.arch = '')`)
	if err != nil {
		m.log.Error("updates: listing devices", "err", err)
		return
	}
	for _, d := range list {
		m.ensurePlatform(ctx, d.ID, false)
	}
}

func (m *Module) ensurePlatform(ctx context.Context, deviceID string, claimed bool) {
	goos, arch, err := m.platformFromPairing(ctx, deviceID)
	if err != nil {
		m.log.Error("updates: reading pairing request", "device", deviceID, "err", err)
		return
	}
	if goos == "" && arch == "" && !claimed {
		return
	}
	if err := m.storePlatform(ctx, deviceID, goos, arch, claimed); err != nil {
		m.log.Error("updates: storing device platform", "device", deviceID, "err", err)
	}
}

// onEvent reacts to device events: platform capture and alignment after a claim,
// auto-alignment of mismatching devices, rollout progress on reconnects.
func (m *Module) onEvent(ctx context.Context, e core.Event) {
	if e.Topic == "*" {
		m.triggerRollout()
		return
	}
	if e.Topic != core.TopicDevices || e.ID == "" {
		return
	}
	switch e.Kind {
	case devices.EvClaimed:
		m.ensurePlatform(ctx, e.ID, true)
		m.triggerRollout()
	case devices.EvOnline:
		m.onOnline(ctx, e.ID)
		m.triggerRollout()
	case devices.EvOffline, devices.EvDeleted, devices.EvUpdated, devices.EvRepaired:
		m.triggerRollout()
	}
}

// ---------------------------------------------------------------- audit helper

func (m *Module) audit(ctx context.Context, p *core.Principal, ip string, e core.AuditEntry) {
	err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error { return core.Audit(ctx, tx, p, ip, e) })
	if err != nil {
		m.log.Error("updates: writing audit entry", "action", e.Action, "err", err)
	}
}

func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	return httpx.ClientIP(r)
}

// readSmall reads a small metadata file of a release directory.
func readSmall(dir, name string) ([]byte, error) {
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxMetaBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxMetaBytes {
		return nil, fmt.Errorf("%s too large", name)
	}
	return b, nil
}

// checkState is persisted so the page shows the last check after a restart and the
// periodic check keeps its 24 h rhythm.
type checkState struct {
	LastCheckAt string       `json:"lastCheckAt"`
	LastError   *storedError `json:"lastError"`
	Latest      string       `json:"latest"`
}

func (m *Module) checkFile() string { return filepath.Join(m.updatesDir(), "check.json") }

func (m *Module) loadCheck() checkState {
	var c checkState
	if err := atomicfile.ReadJSON(m.checkFile(), &c); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.log.Warn("updates: check state unreadable", "err", err)
	}
	return c
}

func (m *Module) saveCheck(c checkState) {
	if err := atomicfile.WriteJSON(m.checkFile(), c, 0o640); err != nil {
		m.log.Error("updates: storing check state", "err", err)
	}
}
