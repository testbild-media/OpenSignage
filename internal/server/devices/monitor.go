package devices

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"opensignage/internal/common/version"
	"opensignage/internal/contracts"
)

// Monitoring: problems per device, free-space levels, the state of blackout and display
// changes until the device confirms them, the display modes the screen reports and the
// name of the playlist on screen.

// Alert kinds.
const (
	AlertOffline         = "device_offline" // (warning)
	AlertPlayerCrashed   = "player_crashed"
	AlertSyncFailed      = "sync_failed"
	AlertVersionMismatch = "version_mismatch"
	AlertDiskLow         = "disk_low"       // < 20 % free (warning)
	AlertDiskCritical    = "disk_critical"  // < 10 % free (critical)
	AlertPowerLow        = "power_low"      // under-voltage now or within powerRecent (warning)
	AlertVideoFallback   = "video_fallback" // "auto" switched to software decoding after decode errors (warning)
	AlertGPULost         = "gpu_lost"       // the player lost the GPU and restarting it did not help (warning)
	AlertUpdateFailed    = "update_failed"  // the last update to the server version failed or was rolled back (warning)
)

// powerRecent keeps a power warning visible after a short dip (the CPU was throttled).
const powerRecent = 15 * time.Minute

// Alert severities.
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
)

// Free-space thresholds in percent (the same rules as on the server).
const (
	diskWarnPercent     = 20
	diskCriticalPercent = 10
)

// Alert is a problem shown on every admin page.
type Alert struct {
	Kind       string `json:"kind"`     // device_offline | player_crashed | sync_failed | version_mismatch | update_failed | disk_low | disk_critical | power_low | video_fallback | gpu_lost
	Severity   string `json:"severity"` // critical | warning
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
	ZoneID     string `json:"zoneId,omitempty"`
	ZoneName   string `json:"zoneName,omitempty"`
	Since      string `json:"since,omitempty"`
	// Detail: the player state, the sync error, the device version, the error code of a
	// failed update or the free share in percent ("8.53") depending on the kind.
	Detail string `json:"detail,omitempty"`
	// Version: the version a failed update was to install (update_failed).
	Version string `json:"version,omitempty"`
	// Ack: a user acknowledged the problem; it does not count as open until it is gone
	// and comes back (acks.go).
	Ack *AlertAck `json:"ack,omitempty"`
}

// An offline device is a warning: screens are often switched off at night or outside
// opening hours, and that is no problem.
var alertSeverity = map[string]string{AlertOffline: SeverityWarning, AlertPlayerCrashed: SeverityCritical,
	AlertDiskCritical: SeverityCritical, AlertSyncFailed: SeverityWarning, AlertVersionMismatch: SeverityWarning,
	AlertDiskLow: SeverityWarning, AlertPowerLow: SeverityWarning, AlertVideoFallback: SeverityWarning, AlertGPULost: SeverityWarning,
	AlertUpdateFailed: SeverityWarning}

// updateFailure is the last update of a device when it failed or was rolled back.
type updateFailure struct {
	version, code, at string
}

// updateFailures reads the newest update of every device (or of one) and keeps those that
// failed or were rolled back. A failed update shows as a problem of its own – "version
// differs" alone does not tell that the device tried and returned to its old version.
func (m *Module) updateFailures(ctx context.Context, deviceID string) (map[string]updateFailure, error) {
	q, args := `SELECT du.device_id, r.version, du.state, du.error, du.updated_at
		FROM device_updates du JOIN update_runs r ON r.id = du.run_id`, []any{}
	if deviceID != "" {
		q, args = q+` WHERE du.device_id = ?`, append(args, deviceID)
	}
	rows, err := m.app.DB.R.QueryContext(ctx, q+` ORDER BY du.device_id, du.updated_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]updateFailure{}
	for rows.Next() {
		var dev, ver, state, errText, at string
		if err := rows.Scan(&dev, &ver, &state, &errText, &at); err != nil {
			return nil, err
		}
		if state != "failed" && state != "rolled_back" {
			delete(out, dev) // a later update replaces an earlier failure
			continue
		}
		var e struct {
			Code string `json:"code"`
		}
		if json.Unmarshal([]byte(errText), &e) != nil || e.Code == "" {
			e.Code = "update_" + state // update_failed | update_rolled_back
		}
		out[dev] = updateFailure{version: ver, code: e.Code, at: at}
	}
	return out, rows.Err()
}

// gpuLost reports a player that lost the GPU and runs in software although restarts were
// tried (agent self-healing, kiosk/gpu.go).
func gpuLost(st contracts.DeviceStatus) bool {
	return st.Video != nil && st.Video.GPU == "lost"
}

// videoFallback reports that the device switched hardware video decoding off by itself.
func videoFallback(st contracts.DeviceStatus) bool {
	return st.Video != nil && st.Video.Fallback != ""
}

// powerLow reports a weak power supply: under-voltage now or a recent event.
func powerLow(st contracts.DeviceStatus, now time.Time) bool {
	if st.Power == nil {
		return false
	}
	if st.Power.UnderVoltage {
		return true
	}
	t, err := time.Parse(time.RFC3339, st.Power.Last)
	return err == nil && now.Sub(t) < powerRecent
}

// diskFreePercent returns the free share of the client's disk (false when unknown).
func diskFreePercent(st contracts.DeviceStatus) (float64, bool) {
	if st.DiskTotal == 0 {
		return 0, false
	}
	free := min(st.DiskFree, st.DiskTotal)
	return float64(free) * 100 / float64(st.DiskTotal), true
}

// diskAlert maps the free share to an alert kind ("" = enough space).
func diskAlert(st contracts.DeviceStatus) (kind string, pct float64) {
	pct, ok := diskFreePercent(st)
	switch {
	case !ok:
		return "", 0
	case pct < diskCriticalPercent:
		return AlertDiskCritical, pct
	case pct < diskWarnPercent:
		return AlertDiskLow, pct
	}
	return "", pct
}

func playerCrashed(st contracts.DeviceStatus) bool {
	return st.Player != "" && st.Player != "ok" && st.Player != "starting"
}

func parseStatus(raw json.RawMessage) contracts.DeviceStatus {
	var st contracts.DeviceStatus
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &st) // validated when stored; a broken old row is just empty
	}
	return st
}

// ---------------------------------------------------------------- since tracking

// sinceTracker remembers when the server first saw a condition of a device (in memory:
// after a server restart "since" starts again with the first report).
type sinceTracker struct {
	mu sync.Mutex
	m  map[string]map[string]time.Time // device → condition → since
}

func newSinceTracker() *sinceTracker { return &sinceTracker{m: map[string]map[string]time.Time{}} }

// observe records whether a condition holds now.
func (s *sinceTracker) observe(deviceID, cond string, active bool, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conds := s.m[deviceID]
	if !active {
		if conds != nil {
			delete(conds, cond)
			if len(conds) == 0 {
				delete(s.m, deviceID)
			}
		}
		return
	}
	if conds == nil {
		if len(s.m) >= 20000 {
			s.m = map[string]map[string]time.Time{} // bounded; only the "since" texts are lost
		}
		conds = map[string]time.Time{}
		s.m[deviceID] = conds
	}
	if _, ok := conds[cond]; !ok {
		conds[cond] = now
	}
}

func (s *sinceTracker) since(deviceID, cond string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.m[deviceID][cond]; ok {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

func (s *sinceTracker) forget(deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, deviceID)
}

// observeStatus updates the conditions derived from a status report and ends the
// acknowledgements of problems that are gone.
func (m *Module) observeStatus(ctx context.Context, deviceID string, st contracts.DeviceStatus, now time.Time) {
	m.since.observe(deviceID, AlertPlayerCrashed, playerCrashed(st), now)
	kind, _ := diskAlert(st)
	m.endAcks(ctx, deviceID, statusAlertKinds, func(k string) bool {
		switch k {
		case AlertPlayerCrashed:
			return playerCrashed(st)
		case AlertDiskLow, AlertDiskCritical:
			return k == kind
		case AlertVersionMismatch:
			return st.Version == "" || !versionMatches(st.Version)
		case AlertPowerLow:
			return powerLow(st, now)
		case AlertVideoFallback:
			return videoFallback(st)
		case AlertGPULost:
			return gpuLost(st)
		}
		return true
	})
	m.since.observe(deviceID, "disk", kind != "", now) // one "since" for both levels
	if st.Version != "" {
		m.since.observe(deviceID, AlertVersionMismatch, !versionMatches(st.Version), now)
	}
	m.since.observe(deviceID, AlertPowerLow, powerLow(st, now), now)
	m.since.observe(deviceID, AlertVideoFallback, videoFallback(st), now)
	m.since.observe(deviceID, AlertGPULost, gpuLost(st), now)
}

// deviceAlerts lists the problems of one paired device. lp is its newest deployment
// progress (nil when none), uf its last update when that failed (nil when not).
func (m *Module) deviceAlerts(d *deviceRow, online bool, lp *progressRow, uf *updateFailure, now time.Time) []Alert {
	if !d.Paired {
		return nil // devices that are not paired never count
	}
	var out []Alert
	add := func(kind, since, detail string) {
		out = append(out, Alert{Kind: kind, Severity: alertSeverity[kind], DeviceID: d.ID, DeviceName: d.Name,
			ZoneID: d.ZoneID, ZoneName: d.ZoneName, Since: since, Detail: detail})
	}
	st := parseStatus(d.LastStatus)
	if !online {
		since := d.LastSeenAt
		if since == "" {
			since = d.PairedAt
		}
		if t, ok := parseTS(since); ok && now.Sub(t) > m.OfflineAfter && !m.expectedOffline(d.ID, now) {
			add(AlertOffline, since, "")
		}
	} else if playerCrashed(st) {
		add(AlertPlayerCrashed, m.since.since(d.ID, AlertPlayerCrashed), st.Player)
	}
	if lp != nil && lp.state == "failed" {
		add(AlertSyncFailed, lp.updatedAt, lp.errText)
	}
	if !versionMatches(d.Version) {
		// A failed update counts while it was the update to this server version; it tells
		// why the version differs, so it comes first.
		if uf != nil && uf.version == version.Version {
			add(AlertUpdateFailed, uf.at, uf.code)
			out[len(out)-1].Version = uf.version
		}
		add(AlertVersionMismatch, m.since.since(d.ID, AlertVersionMismatch), d.Version)
	}
	if kind, pct := diskAlert(st); kind != "" {
		add(kind, m.since.since(d.ID, "disk"), strconv.FormatFloat(math.Round(pct*100)/100, 'f', 2, 64))
	}
	if online && powerLow(st, now) {
		add(AlertPowerLow, m.since.since(d.ID, AlertPowerLow), strconv.Itoa(st.Power.Events))
	}
	if online && videoFallback(st) {
		since := st.Video.FallbackAt
		if since == "" {
			since = m.since.since(d.ID, AlertVideoFallback)
		}
		add(AlertVideoFallback, since, st.Video.Fallback)
	}
	if online && gpuLost(st) {
		since := st.Video.GPULostAt
		if since == "" {
			since = m.since.since(d.ID, AlertGPULost)
		}
		add(AlertGPULost, since, strconv.Itoa(st.Video.GPURecoveries))
	}
	for i := range out {
		out[i].Ack = m.ackOf(d.ID, out[i].Kind)
	}
	return out
}

// ---------------------------------------------------------------- pending config

// Config change states reported in AppliedView.
const (
	ApplyConfirmed = "confirmed" // the device reports the stored setting as applied
	ApplyPending   = "pending"   // not confirmed yet (offline devices: when they connect)
	ApplyTimeout   = "timeout"   // online, but not confirmed within ActionTimeout
	ApplyFailed    = "failed"    // the device could not apply the display mode
	ApplyLocal     = "local"     // a mode chosen in the client WebGUI is in effect
	ApplyUnknown   = "unknown"   // the device does not report it (older agent, no status yet)
)

// AppliedView tells whether the device applied the stored blackout and display setting
// (pending until the device answers or a timeout applies).
type AppliedView struct {
	Blackout     string                   `json:"blackout"`
	Display      string                   `json:"display"`
	DisplayError string                   `json:"displayError,omitempty"`
	DisplayMode  *contracts.DisplayConfig `json:"displayMode,omitempty"` // in effect on the device (local)
}

// cfgChanges remembers when blackout and display were last changed per device (in
// memory: the time base of the timeout state).
type cfgChanges struct {
	mu sync.Mutex
	m  map[string]cfgChange
}

type cfgChange struct{ blackout, display time.Time }

func newCfgChanges() *cfgChanges { return &cfgChanges{m: map[string]cfgChange{}} }

func (c *cfgChanges) mark(deviceID string, blackout, display bool, now time.Time) {
	if !blackout && !display {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= 20000 {
		c.m = map[string]cfgChange{}
	}
	ch := c.m[deviceID]
	if blackout {
		ch.blackout = now
	}
	if display {
		ch.display = now
	}
	c.m[deviceID] = ch
}

func (c *cfgChanges) get(deviceID string) cfgChange {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[deviceID]
}

func (c *cfgChanges) forget(deviceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, deviceID)
}

// applied compares the stored config with what the device reports. connectedAt is the
// start of the current connection (zero when offline): a change made while the device was
// offline gets the full timeout after it connects.
func (m *Module) applied(d *deviceRow, st contracts.DeviceStatus, rawStatus json.RawMessage, connectedAt, now time.Time) AppliedView {
	v := AppliedView{Blackout: ApplyUnknown, Display: ApplyUnknown}
	if !d.Paired {
		return v
	}
	ch := m.cfgChanges.get(d.ID)
	timedOut := func(changed time.Time) bool {
		if connectedAt.IsZero() || changed.IsZero() {
			return false
		}
		if connectedAt.After(changed) {
			changed = connectedAt
		}
		return now.Sub(changed) >= m.ActionTimeout
	}
	pending := func(changed time.Time) string {
		if timedOut(changed) {
			return ApplyTimeout
		}
		return ApplyPending
	}
	want := d.Config
	if a := st.Applied; a != nil {
		if a.Blackout == want.Blackout {
			v.Blackout = ApplyConfirmed
		} else {
			v.Blackout = pending(ch.blackout)
		}
		switch {
		case a.Display != want.Display || a.DisplayBusy:
			v.Display = pending(ch.display)
		case a.DisplayMode == want.Display:
			v.Display = ApplyConfirmed
		case a.DisplayError != "":
			v.Display, v.DisplayError = ApplyFailed, a.DisplayError
		default:
			mode := a.DisplayMode
			v.Display, v.DisplayMode = ApplyLocal, &mode
		}
		return v
	}
	// Older agents: the player mode in the status shows a blackout; the display mode
	// cannot be told apart from a local choice.
	var old struct {
		Mode string `json:"mode"`
	}
	if json.Unmarshal(rawStatus, &old) == nil && old.Mode != "" {
		if (old.Mode == "blackout") == want.Blackout {
			v.Blackout = ApplyConfirmed
		} else {
			v.Blackout = pending(ch.blackout)
		}
	}
	return v
}

// ---------------------------------------------------------------- display modes

// Bounds of the mode list taken from a status report.
const (
	maxModeGroups   = 64
	maxRatesPerMode = 16
)

// displayModes returns the sanitized mode list of a status report (nil when the agent
// reports none).
func displayModes(st contracts.DeviceStatus) []contracts.DisplayModeGroup {
	if st.Display == nil || len(st.Display.Modes) == 0 {
		return nil
	}
	out := make([]contracts.DisplayModeGroup, 0, min(len(st.Display.Modes), maxModeGroups))
	seen := map[[2]int]bool{}
	for _, g := range st.Display.Modes {
		if len(out) >= maxModeGroups {
			break
		}
		key := [2]int{g.Width, g.Height}
		if g.Width < 320 || g.Width > 7680 || g.Height < 200 || g.Height > 4320 || seen[key] {
			continue
		}
		seen[key] = true
		rates := make([]float64, 0, min(len(g.Refresh), maxRatesPerMode))
		for _, r := range g.Refresh {
			if r > 0 && r <= 240 && len(rates) < maxRatesPerMode {
				rates = append(rates, r)
			}
		}
		if len(rates) == 0 {
			continue
		}
		clean := contracts.DisplayModeGroup{Width: g.Width, Height: g.Height, Refresh: rates}
		if g.Preferred > 0 && g.Preferred <= 240 {
			clean.Preferred = g.Preferred
		}
		if g.Current > 0 && g.Current <= 240 {
			clean.Current = g.Current
		}
		out = append(out, clean)
	}
	return out
}

// ---------------------------------------------------------------- active content

// ContentView is what the screen shows, with the playlist name resolved on the server.
type ContentView struct {
	Kind         string `json:"kind"` // playlist | closed | cleared
	PlaylistID   string `json:"playlistId,omitempty"`
	PlaylistName string `json:"playlistName,omitempty"`
}

func activeSource(raw json.RawMessage) contracts.ActiveSource {
	var src contracts.ActiveSource
	_ = json.Unmarshal(raw, &src)
	return src
}

// playlistNames resolves playlist ids (archived playlists keep their name).
func (m *Module) playlistNames(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT id, name FROM playlists WHERE id IN (`+strings.Repeat("?,", len(ids)-1)+`?)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func hasCap(st contracts.DeviceStatus, c string) bool {
	for _, x := range st.Caps {
		if x == c {
			return true
		}
	}
	return false
}
