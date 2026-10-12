package updates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"opensignage/internal/common/release"
	"opensignage/internal/server/db"
)

// Release states (releases.state).
const (
	relAvailable   = "available"
	relDownloading = "downloading"
	relReady       = "ready" // downloaded (or uploaded) and verified
	relFailed      = "failed"
)

// Run kinds and states (update_runs).
const (
	kindServer  = "server"
	kindClients = "clients"

	runRunning    = "running"
	runDone       = "done"
	runFailed     = "failed"
	runRolledBack = "rolled_back"
	runStopped    = "stopped"
)

// Device update states (device_updates.state).
const (
	duWaiting     = "waiting"
	duSent        = "sent"
	duDownloading = "downloading"
	duInstalling  = "installing"
	duInstalled   = "installed" // agent reported done, waiting for the reconnect
	duDone        = "done"
	duFailed      = "failed"
	duRolledBack  = "rolled_back"
	duOffline     = "offline"   // device was offline when its turn came (skipped)
	duSkipped     = "skipped"   // no package / unknown platform
	duCancelled   = "cancelled" // rollout stopped or failed before its turn
)

func inFlight(state string) bool {
	switch state {
	case duSent, duDownloading, duInstalling, duInstalled:
		return true
	}
	return false
}

type releaseRow struct {
	Version     string
	Source      string
	Notes       string
	Size        int64
	PublishedAt string
	State       string
	Error       string
	Path        string
	UpdatedAt   string
}

const releaseCols = `version, source, notes, size, published_at, state, error, path, updated_at`

func scanRelease(sc interface{ Scan(...any) error }) (releaseRow, error) {
	var r releaseRow
	err := sc.Scan(&r.Version, &r.Source, &r.Notes, &r.Size, &r.PublishedAt, &r.State, &r.Error, &r.Path, &r.UpdatedAt)
	return r, err
}

func (m *Module) getRelease(ctx context.Context, v string) (*releaseRow, error) {
	r, err := scanRelease(m.app.DB.R.QueryRowContext(ctx, `SELECT `+releaseCols+` FROM releases WHERE version = ?`, v))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (m *Module) listReleases(ctx context.Context) ([]releaseRow, error) {
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT `+releaseCols+` FROM releases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []releaseRow
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (m *Module) setReleaseState(ctx context.Context, v, state, errText, dir string) error {
	_, err := m.app.DB.W.ExecContext(ctx, `UPDATE releases SET state = ?, error = ?, path = CASE WHEN ? = '' THEN path ELSE ? END,
		updated_at = ? WHERE version = ?`, state, errText, dir, dir, db.Now(), v)
	return err
}

// readyInfo returns release.json of a downloaded and verified release.
func (m *Module) readyInfo(ctx context.Context, v string) (*release.Info, error) {
	r, err := m.getRelease(ctx, v)
	if err != nil {
		return nil, err
	}
	if r == nil || r.State != relReady {
		return nil, nil
	}
	return readInfo(m.releaseDir(v))
}

func readInfo(dir string) (*release.Info, error) {
	var info release.Info
	b, err := readSmall(dir, release.InfoFile)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, err
	}
	return &info, info.Validate()
}

type runRow struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Version    string `json:"version"`
	State      string `json:"state"`
	Error      string `json:"error"`
	StartedBy  string `json:"startedBy"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
}

const runCols = `id, kind, version, state, error, started_by, started_at, COALESCE(finished_at, '')`

func scanRun(sc interface{ Scan(...any) error }) (runRow, error) {
	var r runRow
	err := sc.Scan(&r.ID, &r.Kind, &r.Version, &r.State, &r.Error, &r.StartedBy, &r.StartedAt, &r.FinishedAt)
	return r, err
}

func (m *Module) getRun(ctx context.Context, id string) (*runRow, error) {
	r, err := scanRun(m.app.DB.R.QueryRowContext(ctx, `SELECT `+runCols+` FROM update_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// activeClientRun returns the running client rollout (at most one).
func (m *Module) activeClientRun(ctx context.Context) (*runRow, error) {
	r, err := scanRun(m.app.DB.R.QueryRowContext(ctx, `SELECT `+runCols+` FROM update_runs
		WHERE kind = ? AND state = ? ORDER BY started_at DESC LIMIT 1`, kindClients, runRunning))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (m *Module) listRuns(ctx context.Context, limit int) ([]runRow, error) {
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT `+runCols+` FROM update_runs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []runRow{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type devUpdate struct {
	RunID       string `json:"runId"`
	DeviceID    string `json:"deviceId"`
	State       string `json:"state"`
	Error       string `json:"error"`
	UpdatedAt   string `json:"updatedAt"`
	SentAt      string `json:"sentAt"`
	FromVersion string `json:"fromVersion"`
	Version     string `json:"version"` // target (from the run)
	RunState    string `json:"-"`
}

const duCols = `du.run_id, du.device_id, du.state, du.error, du.updated_at, COALESCE(du.sent_at, ''), du.from_version, r.version, r.state`

func scanDU(sc interface{ Scan(...any) error }) (devUpdate, error) {
	var d devUpdate
	err := sc.Scan(&d.RunID, &d.DeviceID, &d.State, &d.Error, &d.UpdatedAt, &d.SentAt, &d.FromVersion, &d.Version, &d.RunState)
	return d, err
}

func (m *Module) queryDU(ctx context.Context, where string, args ...any) ([]devUpdate, error) {
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT `+duCols+` FROM device_updates du JOIN update_runs r ON r.id = du.run_id `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []devUpdate
	for rows.Next() {
		d, err := scanDU(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// inFlightUpdates returns all device updates that wait for the device (any run).
func (m *Module) inFlightUpdates(ctx context.Context) ([]devUpdate, error) {
	return m.queryDU(ctx, `WHERE du.state IN (?, ?, ?, ?) ORDER BY du.updated_at`, duSent, duDownloading, duInstalling, duInstalled)
}

// latestUpdates returns the newest device update per device.
func (m *Module) latestUpdates(ctx context.Context) (map[string]devUpdate, error) {
	list, err := m.queryDU(ctx, `WHERE du.rowid IN (SELECT rowid FROM device_updates d2 WHERE d2.device_id = du.device_id
		ORDER BY d2.updated_at DESC LIMIT 1)`)
	if err != nil {
		return nil, err
	}
	out := make(map[string]devUpdate, len(list))
	for _, d := range list {
		out[d.DeviceID] = d
	}
	return out, nil
}

func (m *Module) setDU(ctx context.Context, tx *sql.Tx, runID, deviceID, state, errText string, sent bool) error {
	now := db.Now()
	q := `UPDATE device_updates SET state = ?, error = ?, updated_at = ? WHERE run_id = ? AND device_id = ?`
	args := []any{state, errText, now, runID, deviceID}
	if sent {
		q = `UPDATE device_updates SET state = ?, error = ?, updated_at = ?, sent_at = ? WHERE run_id = ? AND device_id = ?`
		args = []any{state, errText, now, now, runID, deviceID}
	}
	var err error
	if tx != nil {
		_, err = tx.ExecContext(ctx, q, args...)
	} else {
		_, err = m.app.DB.W.ExecContext(ctx, q, args...)
	}
	return err
}

// devInfo is what the rollout needs to know about a device.
type devInfo struct {
	ID       string
	Name     string
	ZoneID   string
	ZoneName string
	Version  string
	Paired   bool
	OS       string
	Arch     string
	Align    bool // align_on_connect (claimed, not yet aligned)
}

func (m *Module) devices(ctx context.Context, where string, args ...any) ([]devInfo, error) {
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT d.id, d.name, COALESCE(d.zone_id, ''), COALESCE(z.name, ''), d.version,
			d.token_hash IS NOT NULL, COALESCE(p.os, ''), COALESCE(p.arch, ''), COALESCE(p.align_on_connect, 0), d.last_status
		FROM devices d LEFT JOIN zones z ON z.id = d.zone_id LEFT JOIN device_platforms p ON p.device_id = d.id `+where+`
		ORDER BY COALESCE(z.name, ''), d.name, d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []devInfo
	for rows.Next() {
		var d devInfo
		var status string
		if err := rows.Scan(&d.ID, &d.Name, &d.ZoneID, &d.ZoneName, &d.Version, &d.Paired, &d.OS, &d.Arch, &d.Align, &status); err != nil {
			return nil, err
		}
		if d.OS == "" || d.Arch == "" {
			// Agents may also report their platform in the status message.
			var st struct {
				OS   string `json:"os"`
				Arch string `json:"arch"`
			}
			if json.Unmarshal([]byte(status), &st) == nil && st.OS != "" && st.Arch != "" {
				d.OS, d.Arch = st.OS, st.Arch
			}
		}
		d.Arch = normArch(d.Arch)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (m *Module) device(ctx context.Context, id string) (*devInfo, error) {
	list, err := m.devices(ctx, `WHERE d.id = ?`, id)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// normArch maps GOARCH names reported by agents to package architectures.
func normArch(a string) string {
	a = strings.ToLower(strings.TrimSpace(a))
	switch a {
	case "arm", "armhf", "armv7l":
		return "armv7"
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	}
	return a
}

// storePlatform remembers a device's platform; the claim flag requests aligning the
// device to the server version as soon as it connects.
func (m *Module) storePlatform(ctx context.Context, deviceID, goos, arch string, claimed bool) error {
	_, err := m.app.DB.W.ExecContext(ctx, `INSERT INTO device_platforms(device_id, os, arch, align_on_connect, updated_at)
		SELECT ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM devices WHERE id = ?)
		ON CONFLICT(device_id) DO UPDATE SET
			os = CASE WHEN excluded.os <> '' THEN excluded.os ELSE device_platforms.os END,
			arch = CASE WHEN excluded.arch <> '' THEN excluded.arch ELSE device_platforms.arch END,
			align_on_connect = MAX(device_platforms.align_on_connect, excluded.align_on_connect),
			updated_at = excluded.updated_at`,
		deviceID, goos, normArch(arch), db.Bool(claimed), db.Now(), deviceID)
	return err
}

func (m *Module) clearAlignFlag(ctx context.Context, deviceID string) error {
	_, err := m.app.DB.W.ExecContext(ctx, `UPDATE device_platforms SET align_on_connect = 0, updated_at = ? WHERE device_id = ? AND align_on_connect = 1`,
		db.Now(), deviceID)
	return err
}

// platformFromPairing reads os/arch from the device's claimed pairing request (kept for
// 24 h after the claim).
func (m *Module) platformFromPairing(ctx context.Context, deviceID string) (goos, arch string, err error) {
	var info string
	err = m.app.DB.R.QueryRowContext(ctx, `SELECT info FROM pairing_requests WHERE device_id = ? ORDER BY created_at DESC LIMIT 1`, deviceID).Scan(&info)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	var pi struct {
		OS   string `json:"os"`
		Arch string `json:"arch"`
	}
	if err := json.Unmarshal([]byte(info), &pi); err != nil {
		return "", "", nil
	}
	return pi.OS, normArch(pi.Arch), nil
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
