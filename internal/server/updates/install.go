package updates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"opensignage/internal/common/ids"
	"opensignage/internal/common/release"
	"opensignage/internal/common/version"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
)

var errUpgradeFrom = httpx.E(http.StatusConflict, "upgrade_from_too_old", "the running version is too old for a direct update")

type installReq struct {
	Version        string `json:"version"`
	RolloutClients *bool  `json:"rolloutClients"`
}

// installHandler starts the server update (admin click after a confirm dialog). The
// work runs in the background; progress arrives as `updates` events, then the server
// restarts into the new version.
func (m *Module) installHandler(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req installReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if !validVersion(req.Version) {
		return httpx.Invalid("version", "invalid")
	}
	pkgPath, err := m.checkInstallable(r.Context(), req.Version)
	if err != nil {
		return err
	}
	if err := m.begin("install", req.Version, p); err != nil {
		return err
	}
	runID := ids.New()
	ip := clientIP(r)
	rollout := req.RolloutClients == nil || *req.RolloutClients
	// The run row and the audit entry are committed before the database backup, so a
	// rolled back database still knows about this update.
	err = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO update_runs(id, kind, version, state, started_by, started_at) VALUES (?, ?, ?, ?, ?, ?)`,
			runID, kindServer, req.Version, runRunning, p.Username, db.Now()); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, ip, core.AuditEntry{Action: "update.server_started", ObjectType: "update_run", ObjectID: runID,
			ObjectName: req.Version, Old: map[string]any{"version": version.Version},
			New: map[string]any{"version": req.Version, "rolloutClients": rollout}})
	})
	if err != nil {
		m.end()
		return err
	}
	m.mu.Lock()
	if m.op != nil {
		m.op.RunID = runID
	}
	m.mu.Unlock()
	m.publish("server", runID, map[string]any{"state": runRunning, "version": req.Version})
	go m.runInstall(runID, req.Version, pkgPath, p, ip, rollout)
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"runId": runID, "version": req.Version})
	return nil
}

// checkInstallable returns the server package of a ready release or the reason why
// this server cannot install it.
func (m *Module) checkInstallable(ctx context.Context, v string) (string, error) {
	if m.updater.Layout == nil {
		e := errNotInstalled
		if m.updater.LayoutErr != nil {
			e = e.With("detail", m.updater.LayoutErr.Error())
		}
		return "", e
	}
	if st, err := LoadState(m.app.Cfg.DataDir); err == nil && st != nil && st.State == statePending {
		return "", errUpdateInProgress.With("version", st.To)
	}
	if version.CompareStrings(v, version.Version) <= 0 {
		return "", errNotNewer.With("version", v).With("current", version.Version)
	}
	row, err := m.getRelease(ctx, v)
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", errReleaseNotFound
	}
	if row.State != relReady {
		return "", errReleaseNotReady.With("version", v)
	}
	info, err := readInfo(m.releaseDir(v))
	if err != nil {
		return "", errReleaseInvalid.With("detail", err.Error())
	}
	if info.MinUpgradeFrom != "" && version.CompareStrings(version.Version, info.MinUpgradeFrom) < 0 {
		return "", errUpgradeFrom.With("min", info.MinUpgradeFrom).With("current", version.Version)
	}
	goos, arch := serverPlatform()
	pkg, ok := info.PackageFor(goos, arch)
	if !ok {
		return "", errNoServerPackage.With("platform", goos+"/"+arch)
	}
	var dbSize int64
	if st, err := os.Stat(m.updater.DBPath); err == nil {
		dbSize = st.Size()
	}
	// backup + extracted release (package roughly doubles when unpacked)
	if err := checkDisk(m.app.Cfg.DataDir, dbSize+pkg.Size); err != nil {
		return "", err
	}
	if err := checkDisk(m.updater.Layout.Root, pkg.Size*3); err != nil {
		return "", err
	}
	return filepath.Join(m.releaseDir(v), pkg.File), nil
}

func (m *Module) runInstall(runID, to, pkgPath string, p *core.Principal, ip string, rollout bool) {
	ctx := m.ctx()
	switched := false
	var err error
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("installation crashed: %v", rec)
		}
		if switched {
			return // the process ends; the operation stays active until then
		}
		m.failRun(runID, err, p, ip, "update.server_failed")
		m.end()
	}()
	m.progress("verifying", 0, 0)
	if _, verr := release.VerifyDir(m.releaseDir(to), m.trustedKeys(), false); verr != nil {
		err = verifyErr(verr)
		return
	}
	var st *State
	st, err = m.updater.Install(ctx, InstallRequest{To: to, Package: pkgPath, RunID: runID, StartedBy: p.Username,
		RolloutClients: rollout, Backup: m.app.DB.Backup, Progress: func(step string) { m.progress(step, 0, 0) }})
	if err != nil {
		return
	}
	switched = true
	m.log.Info("updates: release switched, restarting into the new version", "from", st.From, "to", st.To, "backup", st.Backup)
	m.audit(context.Background(), p, ip, core.AuditEntry{Action: "update.server_switched", ObjectType: "update_run", ObjectID: runID,
		ObjectName: to, New: map[string]any{"from": st.From, "to": st.To, "backup": st.Backup}})
	m.progress("restarting", 0, 0)
	m.publish("server", runID, map[string]any{"state": "restarting", "version": to})
	time.Sleep(m.RestartDelay)
	m.exit(0)
}

// failRun marks a server run failed (nothing was switched).
func (m *Module) failRun(runID string, err error, p *core.Principal, ip, action string) {
	if err == nil {
		err = errors.New("unknown error")
	}
	m.log.Error("updates: server update failed", "run", runID, "err", err)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	txErr := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err2 := tx.ExecContext(ctx, `UPDATE update_runs SET state = ?, error = ?, finished_at = ? WHERE id = ?`,
			runFailed, encodeErr(err), db.Now(), runID); err2 != nil {
			return err2
		}
		return core.Audit(ctx, tx, p, ip, core.AuditEntry{Action: action, ObjectType: "update_run", ObjectID: runID,
			New: map[string]any{"error": err.Error()}})
	})
	if txErr != nil {
		m.log.Error("updates: storing failed run", "err", txErr)
	}
	m.publish("server", runID, map[string]any{"state": runFailed, "error": encodeErr(err)})
}

// finishServerUpdate records the outcome of a server update when the server starts
// (called from Run, i.e. after the listeners are bound).
func (m *Module) finishServerUpdate(ctx context.Context) {
	dataDir := m.app.Cfg.DataDir
	st, err := LoadState(dataDir)
	if err != nil {
		m.log.Error("updates: update state unreadable", "err", err)
		return
	}
	if st == nil {
		return
	}
	own := version.Version
	finish := func(state, errText, action string) {
		err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE update_runs SET state = ?, error = ?, finished_at = ? WHERE id = ?`,
				state, errText, db.Now(), st.RunID); err != nil {
				return err
			}
			return core.Audit(ctx, tx, nil, "", core.AuditEntry{Action: action, ObjectType: "update_run", ObjectID: st.RunID,
				ObjectName: st.To, New: map[string]any{"from": st.From, "to": st.To, "attempts": st.Attempts, "error": st.Error}})
		})
		if err != nil {
			m.log.Error("updates: recording update result", "err", err)
			return
		}
		if err := clearState(dataDir); err != nil {
			m.log.Error("updates: removing update state", "err", err)
		}
		m.publish("server", st.RunID, map[string]any{"state": state, "version": own})
	}
	switch {
	case st.State == statePending && st.To == own && st.SelfTestOK:
		m.log.Info("updates: server update completed", "from", st.From, "to", st.To, "attempts", st.Attempts)
		finish(runDone, "", "update.server_done")
		m.cleanupReleases()
		if st.RolloutClients {
			since, err := time.Parse(time.RFC3339, st.StartedAt)
			if err != nil {
				since = time.Now()
			}
			go m.rolloutAfterServerUpdate(ctx, st.StartedBy, since)
		}
	case st.State == statePending && st.To == own:
		m.log.Warn("updates: update state pending without self-test; leaving it for the next start")
	case st.State == stateRolledBack:
		m.log.Error("updates: the server update was rolled back", "from", st.From, "to", st.To, "reason", st.Error)
		finish(runRolledBack, codeErr("server_update_rolled_back", st.Error), "update.server_rolled_back")
		if err := m.setReleaseError(ctx, st.To, codeErr("server_update_rolled_back", st.Error)); err != nil {
			m.log.Error("updates: marking release", "err", err)
		}
		m.cleanupReleases() // the failed release (its package stays in the update store)
	case st.State == statePending && st.From == own:
		// The links were never switched (or were switched back by hand): the restart
		// into the new version did not happen.
		m.log.Warn("updates: unfinished server update found, cancelling it", "to", st.To)
		st.Error = "interrupted before the restart"
		finish(runFailed, codeErr("update_interrupted", st.Error), "update.server_failed")
	default:
		m.log.Warn("updates: stale update state removed", "from", st.From, "to", st.To, "state", st.State, "running", own)
		if err := clearState(dataDir); err != nil {
			m.log.Error("updates: removing update state", "err", err)
		}
	}
}

// rolloutAfterServerUpdate starts the client rollout requested with a server update. Right
// after the restart every screen is still reconnecting and would be skipped as offline, so
// it first waits (at most ReconnectGrace) until the screens seen shortly before the update
// are connected again.
func (m *Module) rolloutAfterServerUpdate(ctx context.Context, startedBy string, since time.Time) {
	deadline := time.Now().Add(m.ReconnectGrace)
	for time.Now().Before(deadline) {
		waiting, err := m.screensReconnecting(ctx, since.Add(-10*time.Minute))
		if err != nil {
			m.log.Warn("updates: waiting for screens to reconnect", "err", err)
			break
		}
		if waiting == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	if _, err := m.startRollout(ctx, nil, "", startedBy); err != nil && !isCode(err, errNothingToUpdate.Code) {
		m.log.Warn("updates: client rollout after the server update not started", "err", err)
	}
}

// screensReconnecting counts paired screens seen since the given time that are not
// connected (yet).
func (m *Module) screensReconnecting(ctx context.Context, seenSince time.Time) (int, error) {
	devs, err := m.devices(ctx, `WHERE d.token_hash IS NOT NULL AND d.last_seen_at >= ?`, seenSince.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")) // the devices module's fixed-width format
	if err != nil {
		return 0, err
	}
	link := m.deviceLink()
	n := 0
	for _, d := range devs {
		if link == nil || !link.Online(d.ID) {
			n++
		}
	}
	return n, nil
}

func (m *Module) setReleaseError(ctx context.Context, v, errText string) error {
	_, err := m.app.DB.W.ExecContext(ctx, `UPDATE releases SET error = ?, updated_at = ? WHERE version = ?`, errText, db.Now(), v)
	return err
}

// cleanupReleases removes installed releases other than current and previous (bounded
// disk use after many updates).
func (m *Module) cleanupReleases() {
	l := m.updater.Layout
	if l == nil {
		return
	}
	keep := map[string]bool{}
	for _, link := range []string{l.currentLink(), l.previousLink()} {
		if t, err := l.Links.Read(link); err == nil {
			keep[filepath.Base(t)] = true
		}
	}
	entries, err := os.ReadDir(l.releasesDir())
	if err != nil {
		m.log.Warn("updates: listing releases", "err", err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || keep[name] {
			continue
		}
		if validVersion(name) || filepath.Ext(name) == ".tmp" && validVersion(name[:len(name)-4]) {
			if err := os.RemoveAll(filepath.Join(l.releasesDir(), name)); err != nil {
				m.log.Warn("updates: removing old release", "release", name, "err", err)
			} else {
				m.log.Info("updates: removed old release", "release", name)
			}
		}
	}
}
