package devices

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"opensignage/internal/common/ids"
	"opensignage/internal/common/version"
	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
	"opensignage/internal/server/devices/deploy"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

// DeviceView is the API representation of a device.
type DeviceView struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	HardwareID string  `json:"hardwareId"`
	ZoneID     *string `json:"zoneId"`
	ZoneName   string  `json:"zoneName"`
	// GroupID/GroupName: display group in the zone (null = none); Position: the place in
	// the group or among the screens without group (0 = none).
	GroupID              *string         `json:"groupId"`
	GroupName            string          `json:"groupName"`
	Position             int             `json:"position"`
	Paired               bool            `json:"paired"`
	Online               bool            `json:"online"`
	Status               string          `json:"status"` // online | offline | unpaired
	Version              string          `json:"version"`
	VersionMatch         bool            `json:"versionMatch"`
	LastSeenAt           *string         `json:"lastSeenAt"`
	PairedAt             *string         `json:"pairedAt"`
	IPs                  []string        `json:"ips"`
	Config               Config          `json:"config"`
	Metrics              json.RawMessage `json:"metrics"`
	ActiveDeploymentID   *string         `json:"activeDeploymentId"`
	PreviousDeploymentID *string         `json:"previousDeploymentId"`
	ActiveSource         json.RawMessage `json:"activeSource"`
	// ActiveContent is the active source with the playlist name resolved.
	ActiveContent ContentView `json:"activeContent"`
	Sync          SyncView    `json:"sync"`
	// DisplayModes are the modes the screen reports (empty: none reported, e.g. an older
	// agent or no display control).
	DisplayModes []contracts.DisplayModeGroup `json:"displayModes"`
	// Applied tells whether the device confirmed the stored blackout and display setting.
	Applied AppliedView `json:"applied"`
	// Alerts are the device's current problems (the same as in GET /api/v1/alerts).
	Alerts       []Alert `json:"alerts"`
	ScreenshotAt *string `json:"screenshotAt"`
	CreatedAt    string  `json:"createdAt"`
	UpdatedAt    string  `json:"updatedAt"`
}

// SyncView is the rollout state of the zone's current target on this device.
type SyncView struct {
	// active | pending | preparing | ready | failed | cleared | no_zone | version_mismatch | unpaired
	State        string `json:"state"`
	DeploymentID string `json:"deploymentId,omitempty"`
	Done         int64  `json:"done"`
	Total        int64  `json:"total"`
	Error        string `json:"error,omitempty"`
	UpdatedAt    string `json:"updatedAt,omitempty"`
}

type progressRow struct {
	state, errText, updatedAt string
	done, total               int64
}

func versionMatches(v string) bool { return v == "" || v == version.Version }

func (m *Module) screenshotPath(deviceID string) string {
	return filepath.Join(m.app.Cfg.DataDir, "screenshots", deviceID+".jpg")
}

// views builds API views with the online flag and rollout state (batched queries).
func (m *Module) views(ctx context.Context, rows []*deviceRow) ([]DeviceView, error) {
	targets, err := deploy.Targets(ctx, m.app.DB.R)
	if err != nil {
		return nil, err
	}
	progress := map[[2]string]progressRow{}
	lastProgress := map[string]progressRow{} // newest progress per device (alerts)
	if len(rows) > 0 {
		q, args := `SELECT device_id, deployment_id, state, done, total, error, updated_at FROM device_deployments`, []any{}
		if len(rows) == 1 {
			q, args = q+` WHERE device_id = ?`, append(args, rows[0].ID)
		}
		pr, err := m.app.DB.R.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		for pr.Next() {
			var dev, dep string
			var p progressRow
			if err := pr.Scan(&dev, &dep, &p.state, &p.done, &p.total, &p.errText, &p.updatedAt); err != nil {
				pr.Close()
				return nil, err
			}
			progress[[2]string{dev, dep}] = p
			if lp, ok := lastProgress[dev]; !ok || p.updatedAt >= lp.updatedAt { // fixed-width timestamps
				lastProgress[dev] = p
			}
		}
		pr.Close()
		if err := pr.Err(); err != nil {
			return nil, err
		}
	}
	var playlistIDs []string
	seenPL := map[string]bool{}
	for _, d := range rows {
		if id := activeSource(d.ActiveSource).PlaylistID; id != "" && !seenPL[id] {
			seenPL[id] = true
			playlistIDs = append(playlistIDs, id)
		}
	}
	names, err := m.playlistNames(ctx, playlistIDs)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]DeviceView, 0, len(rows))
	for _, d := range rows {
		c := m.hub.get(d.ID)
		var connectedAt time.Time
		if c != nil {
			connectedAt = c.connectedAt
		}
		st := parseStatus(d.LastStatus)
		src := activeSource(d.ActiveSource)
		v := DeviceView{ID: d.ID, Name: d.Name, HardwareID: d.HardwareID, ZoneID: strPtr(d.ZoneID), ZoneName: d.ZoneName,
			GroupID: strPtr(d.GroupID), GroupName: d.GroupName, Position: d.Position,
			Paired: d.Paired, Online: c != nil, Version: d.Version, VersionMatch: versionMatches(d.Version),
			LastSeenAt: strPtr(d.LastSeenAt), PairedAt: strPtr(d.PairedAt), IPs: d.IPs, Config: d.Config,
			Metrics: d.LastStatus, ActiveDeploymentID: strPtr(d.ActiveDeploymentID),
			PreviousDeploymentID: strPtr(d.PreviousDeploymentID), ActiveSource: d.ActiveSource,
			ActiveContent: ContentView{Kind: src.Kind, PlaylistID: src.PlaylistID, PlaylistName: names[src.PlaylistID]},
			DisplayModes:  displayModes(st), Applied: m.applied(d, st, d.LastStatus, connectedAt, now),
			CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
		if v.DisplayModes == nil {
			v.DisplayModes = []contracts.DisplayModeGroup{}
		}
		var lp *progressRow
		if p, ok := lastProgress[d.ID]; ok {
			lp = &p
		}
		v.Alerts = m.deviceAlerts(d, v.Online, lp, now)
		if v.Alerts == nil {
			v.Alerts = []Alert{}
		}
		switch {
		case !d.Paired:
			v.Status = "unpaired"
		case v.Online:
			v.Status = "online"
		default:
			v.Status = "offline"
		}
		t, has := targets[d.ZoneID]
		switch {
		case !d.Paired:
			v.Sync.State = "unpaired"
		case d.ZoneID == "":
			v.Sync.State = deploy.ReasonNoZone
		case !v.VersionMatch:
			v.Sync.State = "version_mismatch"
		case !has:
			v.Sync.State = deploy.KindCleared
		default:
			v.Sync.DeploymentID = t.ID
			p, ok := progress[[2]string{d.ID, t.ID}]
			switch {
			case d.ActiveDeploymentID == t.ID:
				v.Sync.State = "active"
			case ok:
				v.Sync.State = p.state
			default:
				v.Sync.State = "pending"
			}
			if ok {
				v.Sync.Done, v.Sync.Total, v.Sync.Error, v.Sync.UpdatedAt = p.done, p.total, p.errText, p.updatedAt
			}
		}
		if fi, err := os.Stat(m.screenshotPath(d.ID)); err == nil {
			s := fi.ModTime().UTC().Format(time.RFC3339)
			v.ScreenshotAt = &s
		}
		out = append(out, v)
	}
	return out, nil
}

func (m *Module) writeDevice(w http.ResponseWriter, r *http.Request, id string) error {
	row, err := m.loadDevice(r.Context(), m.app.DB.R, id)
	if err != nil {
		return err
	}
	if row == nil {
		return errDeviceNotFound
	}
	vs, err := m.views(r.Context(), []*deviceRow{row})
	if err != nil {
		return err
	}
	return httpx.OK(w, vs[0])
}

// deviceAccess wraps handlers for /devices/{id}/…: operators limited to zones reach only
// the displays of their zones; control needs the displays right and edit access to the zone.
func (m *Module) deviceAccess(control bool, h core.AuthedHandler) core.AuthedHandler {
	return func(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
		if !p.Unrestricted() {
			row, err := m.loadDevice(r.Context(), m.app.DB.R, r.PathValue("id"))
			if err != nil {
				return err
			}
			if row == nil || !visible(p, row.ZoneID) {
				return errDeviceNotFound
			}
			if control && !p.CanControlDevice(row.ZoneID) {
				return httpx.ErrForbidden
			}
		}
		return h(w, r, p)
	}
}

// visible reports whether p may see the device (users and operators limited to zones: only
// devices of their zones).
func visible(p *core.Principal, zoneID string) bool {
	if p.Unrestricted() {
		return true
	}
	return p.CanViewZone(zoneID)
}

func (m *Module) list(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	rows, err := m.app.DB.R.QueryContext(r.Context(), deviceSelect+` ORDER BY d.name COLLATE NOCASE, d.id`)
	if err != nil {
		return err
	}
	all, err := scanDevices(rows)
	if err != nil {
		return err
	}
	var mine []*deviceRow
	for _, d := range all {
		if visible(p, d.ZoneID) {
			mine = append(mine, d)
		}
	}
	vs, err := m.views(r.Context(), mine)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"devices": vs, "serverVersion": version.Version})
}

func (m *Module) get(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	row, err := m.loadDevice(r.Context(), m.app.DB.R, r.PathValue("id"))
	if err != nil {
		return err
	}
	if row == nil || !visible(p, row.ZoneID) {
		return errDeviceNotFound
	}
	vs, err := m.views(r.Context(), []*deviceRow{row})
	if err != nil {
		return err
	}
	return httpx.OK(w, vs[0])
}

type configPatch struct {
	ScaleMode     *string                  `json:"scaleMode"`
	Display       *contracts.DisplayConfig `json:"display"`
	Perf          *string                  `json:"perf"`
	HWVideo       *string                  `json:"hwVideo"`
	PlasmaShift   *bool                    `json:"plasmaShift"`
	ShowIPOverlay *bool                    `json:"showIpOverlay"`
}

type patchReq struct {
	Name   *string         `json:"name"`
	ZoneID json.RawMessage `json:"zoneId"` // absent = unchanged, null = no zone
	// GroupID: absent = unchanged (a zone change leaves the group), null = no group.
	GroupID json.RawMessage `json:"groupId"`
	// Position: absent = unchanged, or the next free place after a zone or group change.
	Position *int         `json:"position"`
	Config   *configPatch `json:"config"`
}

// MaxPosition bounds the place of a screen in its group.
const MaxPosition = 99

type deviceSnapshot struct {
	Name     string `json:"name"`
	ZoneID   string `json:"zoneId"`
	GroupID  string `json:"groupId,omitempty"`
	Position int    `json:"position,omitempty"`
	Config   Config `json:"config"`
}

// optString decodes an optional string field: nil = absent, "" = null.
func optString(raw json.RawMessage, field string) (*string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if string(raw) == "null" {
		empty := ""
		return &empty, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, httpx.Invalid(field, "invalid")
	}
	s = strings.TrimSpace(s)
	return &s, nil
}

// patch changes name, zone and config. Changes reach a connected device at once
// (device.config; a zone change also sends the new live state and deployment target).
func (m *Module) patch(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req patchReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	var name *string
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || utf8.RuneCountInString(n) > 100 || !printable(n, 400) {
			return httpx.Invalid("name", "invalid")
		}
		name = &n
	}
	zone, err := optString(req.ZoneID, "zoneId")
	if err != nil {
		return err
	}
	group, err := optString(req.GroupID, "groupId")
	if err != nil {
		return err
	}
	if req.Position != nil && (*req.Position < 1 || *req.Position > MaxPosition) {
		return httpx.Invalid("position", "out_of_range")
	}
	if c := req.Config; c != nil {
		switch {
		case c.ScaleMode != nil && !validScaleMode(*c.ScaleMode):
			return httpx.Invalid("config.scaleMode", "invalid")
		case c.Perf != nil && !validPerf(*c.Perf):
			return httpx.Invalid("config.perf", "invalid")
		case c.HWVideo != nil && !validHWVideo(*c.HWVideo):
			return httpx.Invalid("config.hwVideo", "invalid")
		}
		if c.Display != nil {
			if field, ok := validDisplay(*c.Display); !ok {
				return httpx.Invalid("config."+field, "invalid")
			}
		}
	}
	var before, after deviceSnapshot
	err = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := m.loadDevice(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if row == nil {
			return errDeviceNotFound
		}
		before = deviceSnapshot{Name: row.Name, ZoneID: row.ZoneID, GroupID: row.GroupID, Position: row.Position, Config: row.Config}
		after = before
		if name != nil {
			after.Name = *name
		}
		if zone != nil {
			if *zone != "" && *zone != row.ZoneID {
				var n int
				if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM zones WHERE id = ?`, *zone).Scan(&n); err != nil {
					return err
				}
				if n == 0 {
					return httpx.Invalid("zoneId", "unknown_zone")
				}
			}
			after.ZoneID = *zone
		}
		if err := placeDevice(r.Context(), tx, id, &before, &after, group, req.Position); err != nil {
			return err
		}
		if c := req.Config; c != nil {
			if c.ScaleMode != nil {
				after.Config.ScaleMode = *c.ScaleMode
			}
			if c.Display != nil {
				after.Config.Display = *c.Display
			}
			if c.Perf != nil {
				after.Config.Perf = *c.Perf
			}
			if c.HWVideo != nil {
				after.Config.HWVideo = *c.HWVideo
			}
			if c.PlasmaShift != nil {
				after.Config.PlasmaShift = *c.PlasmaShift
			}
			if c.ShowIPOverlay != nil {
				after.Config.ShowIPOverlay = *c.ShowIPOverlay
			}
		}
		cfg, err := json.Marshal(after.Config)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE devices SET name = ?, zone_id = NULLIF(?, ''), group_id = NULLIF(?, ''),
			position = NULLIF(?, 0), config = ?, updated_at = ? WHERE id = ?`,
			after.Name, after.ZoneID, after.GroupID, after.Position, string(cfg), ts(time.Now()), id); err != nil {
			return err
		}
		zoneForAudit := after.ZoneID
		if zoneForAudit == "" {
			zoneForAudit = before.ZoneID
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "device.updated", ObjectType: "device",
			ObjectID: id, ObjectName: after.Name, ZoneID: zoneForAudit, Old: before, New: after})
	})
	if err != nil {
		return err
	}
	m.cfgChanges.mark(id, false, before.Config.Display != after.Config.Display, time.Now())
	if c := m.hub.get(id); c != nil {
		m.syncConn(r.Context(), c, false)
	}
	m.publish(EvUpdated, id, after.ZoneID, map[string]any{"zoneChanged": before.ZoneID != after.ZoneID})
	return m.writeDevice(w, r, id)
}

// placeDevice applies a group and place change: a zone change leaves the group; a zone or
// group change without an explicit place proposes the next free one, which then stays until
// the user changes it. Screens without zone have neither.
func placeDevice(ctx context.Context, tx *sql.Tx, id string, before, after *deviceSnapshot, group *string, position *int) error {
	if after.ZoneID != before.ZoneID {
		after.GroupID = ""
	}
	if group != nil {
		if *group != "" {
			if after.ZoneID == "" {
				return httpx.Invalid("groupId", "no_zone")
			}
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM display_groups WHERE id = ? AND zone_id = ?`, *group, after.ZoneID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return httpx.Invalid("groupId", "unknown_group")
			}
		}
		after.GroupID = *group
	}
	switch {
	case after.ZoneID == "":
		after.GroupID, after.Position = "", 0
	case position != nil:
		after.Position = *position
	case after.ZoneID != before.ZoneID || after.GroupID != before.GroupID || after.Position == 0:
		p, err := modkit.NextPosition(ctx, tx, after.ZoneID, after.GroupID, id)
		if err != nil {
			return err
		}
		after.Position = p
	}
	return nil
}

type blackoutReq struct {
	On *bool `json:"on"`
}

// blackout switches a screen dark or back on (allowed for `user` in its zones).
func (m *Module) blackout(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req blackoutReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.On == nil {
		return httpx.Invalid("on", "required")
	}
	var zoneID string
	changed := false
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := m.loadDevice(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if row == nil {
			return errDeviceNotFound
		}
		if !visible(p, row.ZoneID) {
			return httpx.ErrForbidden // only users can fail this: the device is not in one of their zones
		}
		zoneID = row.ZoneID
		cfg := row.Config
		old := cfg.Blackout
		changed = old != *req.On
		cfg.Blackout = *req.On
		b, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE devices SET config = ?, updated_at = ? WHERE id = ?`, string(b), ts(time.Now()), id); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "device.blackout", ObjectType: "device",
			ObjectID: id, ObjectName: row.Name, ZoneID: row.ZoneID, Old: map[string]bool{"blackout": old}, New: map[string]bool{"blackout": *req.On}})
	})
	if err != nil {
		return err
	}
	m.cfgChanges.mark(id, changed, false, time.Now())
	if c := m.hub.get(id); c != nil {
		m.syncConn(r.Context(), c, false)
	}
	m.publish(EvBlackout, id, zoneID, map[string]bool{"on": *req.On})
	return m.writeDevice(w, r, id)
}

// repair revokes the token: the device returns to PIN mode and is re-claimed as the same
// row (same hardware id).
func (m *Module) repair(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var zoneID string
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := m.loadDevice(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if row == nil {
			return errDeviceNotFound
		}
		zoneID = row.ZoneID
		if _, err := tx.ExecContext(r.Context(), `UPDATE devices SET token_hash = NULL, paired_at = NULL, updated_at = ? WHERE id = ?`, ts(time.Now()), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM pairing_requests WHERE hardware_id = ? OR device_id = ?`, row.HardwareID, id); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "device.repair", ObjectType: "device",
			ObjectID: id, ObjectName: row.Name, ZoneID: row.ZoneID})
	})
	if err != nil {
		return err
	}
	if c := m.hub.get(id); c != nil {
		c.revoke()
	}
	m.actions.forgetDevice(id)
	m.since.forget(id)
	m.cfgChanges.forget(id)
	m.forgetAcks(r.Context(), id)
	m.publish(EvRepaired, id, zoneID, nil)
	return m.writeDevice(w, r, id)
}

func (m *Module) delete(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var zoneID string
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := m.loadDevice(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if row == nil {
			return errDeviceNotFound
		}
		zoneID = row.ZoneID
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM devices WHERE id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM pairing_requests WHERE hardware_id = ? OR device_id = ?`, row.HardwareID, id); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "device.deleted", ObjectType: "device",
			ObjectID: id, ObjectName: row.Name, ZoneID: row.ZoneID,
			Old: deviceSnapshot{Name: row.Name, ZoneID: row.ZoneID, Config: row.Config}})
	})
	if err != nil {
		return err
	}
	if c := m.hub.get(id); c != nil {
		c.revoke()
	}
	m.actions.forgetDevice(id)
	m.since.forget(id)
	m.cfgChanges.forget(id)
	m.forgetAcks(r.Context(), id)
	if ids.Valid(id) {
		if err := os.Remove(m.screenshotPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.log.Error("devices: removing screenshot", "device", id, "err", err)
		}
	}
	m.publish(EvDeleted, id, zoneID, nil)
	return httpx.NoContent(w)
}

func (m *Module) screenshot(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	id := r.PathValue("id")
	if !ids.Valid(id) {
		return errNoScreenshot
	}
	f, err := os.Open(m.screenshotPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return errNoScreenshot
	}
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", fi.ModTime(), f)
	return nil
}

// alerts lists problems of paired devices (unpaired ones never count), critical ones
// first.
func (m *Module) alerts(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	rows, err := m.app.DB.R.QueryContext(r.Context(), deviceSelect+` WHERE d.token_hash IS NOT NULL ORDER BY d.name COLLATE NOCASE, d.id`)
	if err != nil {
		return err
	}
	devs, err := scanDevices(rows)
	if err != nil {
		return err
	}
	lastProgress := map[string]progressRow{}
	pr, err := m.app.DB.R.QueryContext(r.Context(), `SELECT device_id, state, error, updated_at FROM device_deployments ORDER BY device_id, updated_at`)
	if err != nil {
		return err
	}
	for pr.Next() {
		var dev string
		var p progressRow
		if err := pr.Scan(&dev, &p.state, &p.errText, &p.updatedAt); err != nil {
			pr.Close()
			return err
		}
		lastProgress[dev] = p // ordered by time: the last one wins
	}
	pr.Close()
	if err := pr.Err(); err != nil {
		return err
	}
	now := time.Now()
	out := []Alert{}
	for _, d := range devs {
		if !visible(p, d.ZoneID) {
			continue
		}
		var lp *progressRow
		if x, ok := lastProgress[d.ID]; ok {
			lp = &x
		}
		out = append(out, m.deviceAlerts(d, m.hub.get(d.ID) != nil, lp, now)...)
	}
	rank := func(a Alert) int {
		if a.Severity == SeverityCritical {
			return 0
		}
		return 1
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i].Kind < out[j].Kind
	})
	open := 0 // acknowledged problems do not count
	for _, a := range out {
		if a.Ack == nil {
			open++
		}
	}
	return httpx.OK(w, map[string]any{"alerts": out, "count": open})
}
