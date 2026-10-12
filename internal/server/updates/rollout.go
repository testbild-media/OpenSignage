package updates

// Rolling client update: the server offers each device the package for its
// OS/architecture through the device link. At most ONE device per zone updates at a
// time (devices without a zone count as one group), so a zone never goes dark
// completely. A device is done when it reconnects with the target version; failure,
// rollback or no return within DeviceTimeout stops the rollout.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"opensignage/internal/common/ids"
	"opensignage/internal/common/release"
	"opensignage/internal/common/version"
	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/devices"
	"opensignage/internal/server/httpx"
)

// DevicePackage implements devices.PackageProvider (GET /device/v1/update/package).
// Only packages of downloaded and verified releases are served.
func (m *Module) DevicePackage(ctx context.Context, ver, goos, arch string) (*devices.Package, error) {
	if !validVersion(ver) {
		return nil, devices.ErrNoPackage
	}
	info, err := m.readyInfo(ctx, ver)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, devices.ErrNoPackage
	}
	pkg, ok := info.PackageFor(goos, normArch(arch))
	if !ok {
		return nil, devices.ErrNoPackage
	}
	f, err := os.Open(filepath.Join(m.releaseDir(ver), pkg.File))
	if os.IsNotExist(err) {
		return nil, devices.ErrNoPackage
	}
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &devices.Package{Name: pkg.File, ModTime: st.ModTime(), Content: f}, nil
}

// DeviceRelease implements devices.PackageProvider (GET /device/v1/update/release): the
// signed metadata of a downloaded and verified release, byte for byte. Devices verify the
// signature with their own keys (built-in + their configuration); keys added in the
// server's settings do not extend what devices trust.
func (m *Module) DeviceRelease(ctx context.Context, ver string) (*contracts.UpdateRelease, error) {
	if !validVersion(ver) {
		return nil, devices.ErrNoPackage
	}
	r, err := m.getRelease(ctx, ver)
	if err != nil {
		return nil, err
	}
	if r == nil || r.State != relReady {
		return nil, devices.ErrNoPackage
	}
	out := &contracts.UpdateRelease{Version: ver}
	dir := m.releaseDir(ver)
	for _, f := range []struct {
		name string
		dst  *[]byte
	}{{release.InfoFile, &out.Info}, {release.SumsFile, &out.Sums}, {release.SigFile, &out.Sig}} {
		b, err := readSmall(dir, f.name)
		if errors.Is(err, os.ErrNotExist) {
			return nil, devices.ErrNoPackage
		}
		if err != nil {
			return nil, err
		}
		*f.dst = b
	}
	return out, nil
}

// offerFor builds the update.offer for a platform.
func (m *Module) offerFor(ctx context.Context, ver, goos, arch string) (contracts.UpdateOffer, error) {
	if goos == "" || arch == "" {
		return contracts.UpdateOffer{}, errPlatformUnknown
	}
	info, err := m.readyInfo(ctx, ver)
	if err != nil {
		return contracts.UpdateOffer{}, err
	}
	if info == nil {
		return contracts.UpdateOffer{}, errNoPackage.With("version", ver).With("platform", goos+"/"+arch)
	}
	pkg, ok := info.PackageFor(goos, normArch(arch))
	if !ok {
		return contracts.UpdateOffer{}, errNoPackage.With("version", ver).With("platform", goos+"/"+arch)
	}
	q := url.Values{"version": {ver}, "os": {goos}, "arch": {normArch(arch)}}
	return contracts.UpdateOffer{Version: ver, URL: "/device/v1/update/package?" + q.Encode(), SHA256: pkg.SHA256, Size: pkg.Size}, nil
}

// UpdateProgress implements devices.UpdateListener (update.progress from an agent).
func (m *Module) UpdateProgress(deviceID string, p contracts.UpdateProgress) {
	defer func() {
		if rec := recover(); rec != nil {
			m.log.Error("updates: panic handling update.progress", "device", deviceID, "panic", fmt.Sprint(rec))
		}
	}()
	ctx, cancel := context.WithTimeout(m.ctx(), 10*time.Second)
	defer cancel()
	m.rolloutMu.Lock()
	defer m.rolloutMu.Unlock()
	list, err := m.queryDU(ctx, `WHERE du.device_id = ? AND r.version = ? AND du.state IN (?, ?, ?, ?) ORDER BY du.updated_at DESC LIMIT 1`,
		deviceID, p.Version, duSent, duDownloading, duInstalling, duInstalled)
	if err != nil {
		m.log.Error("updates: reading device update", "device", deviceID, "err", err)
		return
	}
	if len(list) == 0 {
		m.log.Info("updates: progress for an update this server did not offer", "device", deviceID, "version", p.Version, "state", p.State)
		return
	}
	du := list[0]
	var state, errText string
	switch p.State {
	case "downloading":
		state = duDownloading
	case "verifying", "installing", "restarting", "health_check":
		// The agent's finer phases between download and its health check.
		state = duInstalling
	case "done":
		state = duInstalled
	case "failed":
		state, errText = duFailed, codeErr(firstNonEmpty(p.Error, "update_failed"), p.Error)
	case "rolled_back":
		state, errText = duRolledBack, codeErr(firstNonEmpty(p.Error, "update_rolled_back"), p.Error)
	default:
		m.log.Info("updates: unknown update.progress state ignored", "device", deviceID, "state", p.State)
		return
	}
	if err := m.setDU(ctx, nil, du.RunID, deviceID, state, errText, false); err != nil {
		m.log.Error("updates: storing device progress", "device", deviceID, "err", err)
		return
	}
	m.publish("device", deviceID, map[string]any{"runId": du.RunID, "state": state, "error": errText})
	if (state == duFailed || state == duRolledBack) && du.RunState == runRunning {
		name := deviceID
		if d, err := m.device(ctx, deviceID); err == nil && d != nil {
			name = d.Name
		}
		m.failRollout(ctx, du.RunID, codeErrP(codeRolloutFailed, fmt.Sprintf("%s: %s", name, firstNonEmpty(p.Error, p.State)), map[string]any{"device": name}))
	}
	m.triggerRollout()
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// rolloutTick advances all device updates: detects returns with the new version and
// timeouts, then offers the update to the next devices (one per zone).
func (m *Module) rolloutTick(ctx context.Context) {
	m.rolloutMu.Lock()
	defer m.rolloutMu.Unlock()
	link := m.deviceLink()
	now := time.Now()
	flying, err := m.inFlightUpdates(ctx)
	if err != nil {
		m.log.Error("updates: rollout: reading device updates", "err", err)
		return
	}
	run, err := m.activeClientRun(ctx)
	if err != nil {
		m.log.Error("updates: rollout: reading run", "err", err)
		return
	}
	if len(flying) == 0 && run == nil {
		return
	}
	devs, err := m.devices(ctx, "")
	if err != nil {
		m.log.Error("updates: rollout: listing devices", "err", err)
		return
	}
	byID := make(map[string]devInfo, len(devs))
	for _, d := range devs {
		byID[d.ID] = d
	}
	online := func(id string) bool { return link != nil && link.Online(id) }

	// 1. devices that are updating
	busy := map[string]bool{} // zone → a device of it is updating
	for _, du := range flying {
		d, exists := byID[du.DeviceID]
		switch {
		case !exists || !d.Paired:
			m.finishDU(ctx, du, duCancelled, codeErr(codeDeleted, ""))
		case d.Version == du.Version && online(du.DeviceID):
			m.finishDU(ctx, du, duDone, "")
			m.log.Info("updates: device updated", "device", d.Name, "version", du.Version)
		case now.Sub(parseTime(firstNonEmpty(du.SentAt, du.UpdatedAt))) > m.DeviceTimeout:
			msg := codeErr(codeTimeout, fmt.Sprintf("no return with version %s within %s", du.Version, m.DeviceTimeout))
			m.finishDU(ctx, du, duFailed, msg)
			if du.RunState == runRunning {
				m.failRollout(ctx, du.RunID, codeErrP(codeRolloutFailed, fmt.Sprintf("%s: %s", d.Name, codeTimeout), map[string]any{"device": d.Name}))
			}
		default:
			busy[d.ZoneID] = true
		}
	}

	// 2. the active rollout (re-read: a failure above may have ended it)
	if run, err = m.activeClientRun(ctx); err != nil || run == nil {
		if err != nil {
			m.log.Error("updates: rollout: reading run", "err", err)
		}
		return
	}
	rows, err := m.queryDU(ctx, `WHERE du.run_id = ? ORDER BY du.rowid`, run.ID)
	if err != nil {
		m.log.Error("updates: rollout: reading devices of the run", "err", err)
		return
	}
	pending := 0
	for _, du := range rows {
		if inFlight(du.State) {
			pending++
			continue
		}
		if du.State != duWaiting {
			continue
		}
		d, exists := byID[du.DeviceID]
		switch {
		case !exists || !d.Paired:
			m.finishDU(ctx, du, duCancelled, codeErr(codeDeleted, ""))
			continue
		case d.Version == run.Version:
			m.finishDU(ctx, du, duDone, "") // aligned in the meantime
			continue
		case busy[d.ZoneID]:
			pending++
			continue
		case !online(d.ID):
			m.finishDU(ctx, du, duOffline, codeErr(codeOffline, ""))
			continue
		}
		offer, err := m.offerFor(ctx, run.Version, d.OS, d.Arch)
		if err != nil {
			m.finishDU(ctx, du, duSkipped, encodeErr(err))
			continue
		}
		if !link.SendUpdateOffer(d.ID, offer) {
			m.finishDU(ctx, du, duOffline, codeErr(codeOffline, ""))
			continue
		}
		if err := m.setDU(ctx, nil, run.ID, d.ID, duSent, "", true); err != nil {
			m.log.Error("updates: rollout: storing offer", "device", d.ID, "err", err)
		}
		m.log.Info("updates: update offered", "device", d.Name, "zone", d.ZoneName, "version", run.Version, "package", offer.URL)
		m.publish("device", d.ID, map[string]any{"runId": run.ID, "state": duSent})
		busy[d.ZoneID] = true
		pending++
	}
	if pending == 0 {
		m.completeRollout(ctx, run)
	}
}

func (m *Module) finishDU(ctx context.Context, du devUpdate, state, errText string) {
	if err := m.setDU(ctx, nil, du.RunID, du.DeviceID, state, errText, false); err != nil {
		m.log.Error("updates: storing device update", "device", du.DeviceID, "err", err)
		return
	}
	m.publish("device", du.DeviceID, map[string]any{"runId": du.RunID, "state": state, "error": errText})
}

// completeRollout ends a run whose devices are all finished.
func (m *Module) completeRollout(ctx context.Context, run *runRow) {
	var failed int
	if err := m.app.DB.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM device_updates WHERE run_id = ? AND state IN (?, ?)`,
		run.ID, duFailed, duRolledBack).Scan(&failed); err != nil {
		m.log.Error("updates: rollout summary", "err", err)
		return
	}
	state := runDone
	if failed > 0 {
		state = runFailed
	}
	err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE update_runs SET state = ?, finished_at = ? WHERE id = ? AND state = ?`, state, db.Now(), run.ID, runRunning)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		return core.Audit(ctx, tx, nil, "", core.AuditEntry{Action: "update.clients_" + state, ObjectType: "update_run", ObjectID: run.ID, ObjectName: run.Version})
	})
	if err != nil {
		m.log.Error("updates: finishing rollout", "err", err)
		return
	}
	m.log.Info("updates: client rollout finished", "run", run.ID, "state", state)
	m.publish("rollout", run.ID, map[string]any{"state": state})
}

// failRollout stops a run after a device failed: the waiting devices are cancelled
// (one failure stops the rollout).
func (m *Module) failRollout(ctx context.Context, runID, errText string) {
	m.endRollout(ctx, runID, runFailed, errText, nil, "")
}

func (m *Module) endRollout(ctx context.Context, runID, state, errText string, p *core.Principal, ip string) {
	cancelled := codeErr(codeStopped, "")
	if state == runFailed {
		cancelled = codeErr(codeCancelled, "")
	}
	err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE update_runs SET state = ?, error = ?, finished_at = ? WHERE id = ? AND state = ?`,
			state, errText, db.Now(), runID, runRunning)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE device_updates SET state = ?, error = ?, updated_at = ? WHERE run_id = ? AND state = ?`,
			duCancelled, cancelled, db.Now(), runID, duWaiting); err != nil {
			return err
		}
		return core.Audit(ctx, tx, p, ip, core.AuditEntry{Action: "update.clients_" + state, ObjectType: "update_run", ObjectID: runID,
			New: map[string]any{"error": errText}})
	})
	if err != nil {
		m.log.Error("updates: ending rollout", "run", runID, "err", err)
		return
	}
	if state == runFailed {
		m.log.Warn("updates: client rollout stopped after a failure", "run", runID, "error", errText)
	}
	m.publish("rollout", runID, map[string]any{"state": state, "error": errText})
}

// startRollout creates a client run for all paired devices whose version differs from
// the server version.
func (m *Module) startRollout(ctx context.Context, p *core.Principal, ip, startedBy string) (string, error) {
	m.rolloutMu.Lock()
	defer m.rolloutMu.Unlock()
	if run, err := m.activeClientRun(ctx); err != nil {
		return "", err
	} else if run != nil {
		return "", errRolloutRunning.With("runId", run.ID)
	}
	target := version.Version
	devs, err := m.devices(ctx, `WHERE d.token_hash IS NOT NULL AND d.version <> ?`, target)
	if err != nil {
		return "", err
	}
	if len(devs) == 0 {
		return "", errNothingToUpdate
	}
	if info, err := m.readyInfo(ctx, target); err != nil {
		return "", err
	} else if info == nil {
		return "", errNoClientPackages.With("version", target)
	}
	runID := ids.New()
	if p != nil {
		startedBy = p.Username
	}
	err = m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO update_runs(id, kind, version, state, started_by, started_at) VALUES (?, ?, ?, ?, ?, ?)`,
			runID, kindClients, target, runRunning, startedBy, db.Now()); err != nil {
			return err
		}
		for _, d := range devs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO device_updates(run_id, device_id, state, updated_at, from_version) VALUES (?, ?, ?, ?, ?)`,
				runID, d.ID, duWaiting, db.Now(), d.Version); err != nil {
				return err
			}
		}
		return core.Audit(ctx, tx, p, ip, core.AuditEntry{Action: "update.clients_started", ObjectType: "update_run", ObjectID: runID,
			ObjectName: target, New: map[string]any{"version": target, "devices": len(devs), "startedBy": startedBy}})
	})
	if err != nil {
		return "", err
	}
	m.log.Info("updates: client rollout started", "run", runID, "version", target, "devices", len(devs))
	m.publish("rollout", runID, map[string]any{"state": runRunning})
	m.triggerRollout()
	return runID, nil
}

// align brings one device to the server version (also a downgrade). It joins the
// running rollout or starts a run for this device.
func (m *Module) align(ctx context.Context, deviceID string, p *core.Principal, ip, startedBy string, manual bool) (string, error) {
	m.rolloutMu.Lock()
	defer m.rolloutMu.Unlock()
	d, err := m.device(ctx, deviceID)
	if err != nil {
		return "", err
	}
	if d == nil {
		return "", errDeviceNotFound
	}
	if !d.Paired {
		return "", errDeviceNotPaired
	}
	target := version.Version
	if d.Version == target {
		return "", errAlreadyAligned
	}
	if manual {
		if link := m.deviceLink(); link == nil || !link.Online(d.ID) {
			return "", errDeviceOffline
		}
	}
	if _, err := m.offerFor(ctx, target, d.OS, d.Arch); err != nil {
		return "", err
	}
	if p != nil {
		startedBy = p.Username
	}
	run, err := m.activeClientRun(ctx)
	if err != nil {
		return "", err
	}
	runID := ""
	err = m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		if run != nil {
			runID = run.ID
			var state string
			err := tx.QueryRowContext(ctx, `SELECT state FROM device_updates WHERE run_id = ? AND device_id = ?`, runID, d.ID).Scan(&state)
			switch {
			case err == sql.ErrNoRows:
				if _, err := tx.ExecContext(ctx, `INSERT INTO device_updates(run_id, device_id, state, updated_at, from_version) VALUES (?, ?, ?, ?, ?)`,
					runID, d.ID, duWaiting, db.Now(), d.Version); err != nil {
					return err
				}
			case err != nil:
				return err
			case state == duWaiting || inFlight(state):
				return nil // already queued
			default:
				if _, err := tx.ExecContext(ctx, `UPDATE device_updates SET state = ?, error = '', updated_at = ?, sent_at = NULL, from_version = ?
					WHERE run_id = ? AND device_id = ?`, duWaiting, db.Now(), d.Version, runID, d.ID); err != nil {
					return err
				}
			}
		} else {
			runID = ids.New()
			if _, err := tx.ExecContext(ctx, `INSERT INTO update_runs(id, kind, version, state, started_by, started_at) VALUES (?, ?, ?, ?, ?, ?)`,
				runID, kindClients, target, runRunning, startedBy, db.Now()); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO device_updates(run_id, device_id, state, updated_at, from_version) VALUES (?, ?, ?, ?, ?)`,
				runID, d.ID, duWaiting, db.Now(), d.Version); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE device_platforms SET align_on_connect = 0 WHERE device_id = ?`, d.ID); err != nil {
			return err
		}
		return core.Audit(ctx, tx, p, ip, core.AuditEntry{Action: "update.device_align", ObjectType: "device", ObjectID: d.ID,
			ObjectName: d.Name, ZoneID: d.ZoneID, Old: map[string]any{"version": d.Version},
			New: map[string]any{"version": target, "runId": runID, "startedBy": startedBy}})
	})
	if err != nil {
		return "", err
	}
	m.log.Info("updates: device queued for alignment", "device", d.Name, "from", d.Version, "to", target, "by", startedBy)
	m.publish("rollout", runID, map[string]any{"state": runRunning, "deviceId": d.ID})
	m.triggerRollout()
	return runID, nil
}

// onOnline aligns a mismatching device automatically after a claim (the claim is the
// consent, decision A.5) or when the admin enabled "align automatically". A device whose
// last update to this version failed is not retried automatically.
func (m *Module) onOnline(ctx context.Context, deviceID string) {
	d, err := m.device(ctx, deviceID)
	if err != nil || d == nil || !d.Paired {
		if err != nil {
			m.log.Error("updates: reading device", "device", deviceID, "err", err)
		}
		return
	}
	if d.OS == "" || d.Arch == "" {
		m.ensurePlatform(ctx, deviceID, false)
		if d, err = m.device(ctx, deviceID); err != nil || d == nil {
			return
		}
	}
	target := version.Version
	if d.Version == target {
		if d.Align {
			if err := m.clearAlignFlag(ctx, deviceID); err != nil {
				m.log.Error("updates: clearing align flag", "err", err)
			}
		}
		return
	}
	if !d.Align && !m.app.Settings.Get().AutoAlignVersions {
		return
	}
	var lastFailed int
	if err := m.app.DB.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM device_updates du JOIN update_runs r ON r.id = du.run_id
		WHERE du.device_id = ? AND r.version = ? AND du.state IN (?, ?)
		AND du.rowid = (SELECT rowid FROM device_updates d2 WHERE d2.device_id = du.device_id ORDER BY d2.updated_at DESC LIMIT 1)`,
		deviceID, target, duFailed, duRolledBack).Scan(&lastFailed); err != nil {
		m.log.Error("updates: reading device update history", "err", err)
		return
	}
	if lastFailed > 0 {
		m.log.Info("updates: not aligning automatically after a failed update", "device", d.Name)
		return
	}
	reason := "auto"
	if d.Align {
		reason = "claim"
	}
	if _, err := m.align(ctx, deviceID, nil, "", reason, false); err != nil && !isCode(err, errAlreadyAligned.Code) {
		m.log.Warn("updates: automatic alignment not possible", "device", d.Name, "err", err)
		m.publish("device", deviceID, map[string]any{"state": "align_failed", "error": encodeErr(err)})
	}
}

// ---------------------------------------------------------------- handlers

type clientView struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	ZoneID      string     `json:"zoneId"`
	ZoneName    string     `json:"zoneName"`
	Version     string     `json:"version"`
	Match       bool       `json:"versionMatch"`
	Online      bool       `json:"online"`
	Platform    string     `json:"platform"`
	Package     bool       `json:"packageAvailable"`
	AlignQueued bool       `json:"alignOnConnect"`
	Update      *devUpdate `json:"update"`
}

func (m *Module) clientsHandler(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	ctx := r.Context()
	devs, err := m.devices(ctx, `WHERE d.token_hash IS NOT NULL`)
	if err != nil {
		return err
	}
	latest, err := m.latestUpdates(ctx)
	if err != nil {
		return err
	}
	info, err := m.readyInfo(ctx, version.Version)
	if err != nil {
		return err
	}
	link := m.deviceLink()
	out := make([]clientView, 0, len(devs))
	for _, d := range devs {
		v := clientView{ID: d.ID, Name: d.Name, ZoneID: d.ZoneID, ZoneName: d.ZoneName, Version: d.Version,
			Match: d.Version == version.Version, Online: link != nil && link.Online(d.ID), AlignQueued: d.Align}
		if d.OS != "" && d.Arch != "" {
			v.Platform = d.OS + "/" + d.Arch
			if info != nil {
				_, v.Package = info.PackageFor(d.OS, d.Arch)
			}
		}
		if du, ok := latest[d.ID]; ok {
			du := du
			v.Update = &du
		}
		out = append(out, v)
	}
	run, err := m.activeClientRun(ctx)
	if err != nil {
		return err
	}
	if run == nil {
		runs, err := m.queryRuns(ctx, `WHERE kind = ? ORDER BY started_at DESC LIMIT 1`, kindClients)
		if err != nil {
			return err
		}
		if len(runs) > 0 {
			run = &runs[0]
		}
	}
	return httpx.OK(w, map[string]any{"serverVersion": version.Version, "packagesAvailable": info != nil,
		"run": run, "devices": out, "timeoutMinutes": int(m.DeviceTimeout / time.Minute)})
}

func (m *Module) queryRuns(ctx context.Context, where string, args ...any) ([]runRow, error) {
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT `+runCols+` FROM update_runs `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []runRow
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (m *Module) startHandler(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	runID, err := m.startRollout(r.Context(), p, clientIP(r), "")
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"runId": runID})
}

func (m *Module) stopHandler(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	m.rolloutMu.Lock()
	defer m.rolloutMu.Unlock()
	run, err := m.activeClientRun(r.Context())
	if err != nil {
		return err
	}
	if run == nil {
		return errNoRollout
	}
	m.endRollout(r.Context(), run.ID, runStopped, "", p, clientIP(r))
	return httpx.OK(w, map[string]any{"runId": run.ID})
}

func (m *Module) alignHandler(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	runID, err := m.align(r.Context(), r.PathValue("id"), p, clientIP(r), "", true)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"runId": runID})
}

type runView struct {
	runRow
	Devices map[string]int `json:"devices"` // state → count (client runs)
}

func (m *Module) runsHandler(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	limit := httpx.QueryInt(r, "limit", 50, 1, 200)
	runs, err := m.listRuns(r.Context(), limit)
	if err != nil {
		return err
	}
	counts := map[string]map[string]int{}
	rows, err := m.app.DB.R.QueryContext(r.Context(), `SELECT run_id, state, COUNT(*) FROM device_updates
		WHERE run_id IN (SELECT id FROM update_runs ORDER BY started_at DESC LIMIT ?) GROUP BY run_id, state`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, state string
		var n int
		if err := rows.Scan(&id, &state, &n); err != nil {
			return err
		}
		if counts[id] == nil {
			counts[id] = map[string]int{}
		}
		counts[id][state] = n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	out := make([]runView, 0, len(runs))
	for _, run := range runs {
		c := counts[run.ID]
		if c == nil {
			c = map[string]int{}
		}
		out = append(out, runView{runRow: run, Devices: c})
	}
	return httpx.OK(w, map[string]any{"runs": out})
}
