package devices

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"opensignage/internal/contracts"
	"opensignage/internal/server/devices/deploy"
)

// Config is the per-device configuration stored in devices.config.
type Config struct {
	ScaleMode     string                  `json:"scaleMode"` // contain | cover | none
	Blackout      bool                    `json:"blackout"`
	Display       contracts.DisplayConfig `json:"display"`
	Perf          string                  `json:"perf"`    // auto | low | standard | high
	HWVideo       string                  `json:"hwVideo"` // auto | on | off
	PlasmaShift   bool                    `json:"plasmaShift"`
	ShowIPOverlay bool                    `json:"showIpOverlay"`
}

// defaultConfig is the configuration of a newly claimed device.
func defaultConfig(scaleMode string) Config {
	if !validScaleMode(scaleMode) {
		scaleMode = "contain"
	}
	return Config{ScaleMode: scaleMode, Perf: "auto", HWVideo: "auto", ShowIPOverlay: true}
}

func parseConfig(raw string) Config {
	c := defaultConfig("contain")
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			c = defaultConfig("contain")
		}
	}
	if !validScaleMode(c.ScaleMode) {
		c.ScaleMode = "contain"
	}
	if !validPerf(c.Perf) {
		c.Perf = "auto"
	}
	if !validHWVideo(c.HWVideo) {
		c.HWVideo = "auto"
	}
	return c
}

func validScaleMode(s string) bool { return s == "contain" || s == "cover" || s == "none" }
func validPerf(s string) bool {
	return s == "auto" || s == "low" || s == "standard" || s == "high"
}
func validHWVideo(s string) bool { return s == "auto" || s == "on" || s == "off" }

func validDisplay(d contracts.DisplayConfig) (string, bool) {
	switch {
	case d.Width == 0 && d.Height != 0, d.Width != 0 && d.Height == 0:
		return "display", false
	case d.Width != 0 && (d.Width < 320 || d.Width > 7680):
		return "display.width", false
	case d.Height != 0 && (d.Height < 200 || d.Height > 4320):
		return "display.height", false
	case d.Refresh < 0 || d.Refresh > 240:
		return "display.refresh", false
	}
	switch d.Rotation {
	case 0, 90, 180, 270:
	default:
		return "display.rotation", false
	}
	return "", true
}

// deviceRow is a devices row joined with its zone name.
type deviceRow struct {
	ID                   string
	HardwareID           string
	Name                 string
	ZoneID               string
	ZoneName             string
	Paired               bool
	tokenHash            string
	Config               Config
	PairedAt             string
	LastSeenAt           string
	LastStatus           json.RawMessage
	Version              string
	IPs                  []string
	ActiveDeploymentID   string
	PreviousDeploymentID string
	ActiveSource         json.RawMessage
	CreatedAt            string
	UpdatedAt            string
	// GroupID/GroupName: the display group in the zone ("" = none); Position the place in
	// the group or among the screens without group (0 = none).
	GroupID   string
	GroupName string
	Position  int
	// ClockKey of the zone (LAN time exchange).
	ClockKey string
}

const deviceSelect = `SELECT d.id, d.hardware_id, d.name, COALESCE(d.zone_id, ''), COALESCE(z.name, ''), COALESCE(d.token_hash, ''),
	d.config, COALESCE(d.paired_at, ''), COALESCE(d.last_seen_at, ''), d.last_status, d.version, d.ips,
	COALESCE(d.active_deployment_id, ''), COALESCE(d.previous_deployment_id, ''), d.active_source, d.created_at, d.updated_at,
	COALESCE(d.group_id, ''), COALESCE(g.name, ''), COALESCE(d.position, 0), COALESCE(z.clock_key, '')
	FROM devices d LEFT JOIN zones z ON z.id = d.zone_id LEFT JOIN display_groups g ON g.id = d.group_id`

type rowsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func scanDevices(rows *sql.Rows) ([]*deviceRow, error) {
	defer rows.Close()
	var out []*deviceRow
	for rows.Next() {
		var d deviceRow
		var cfg, status, ips, src string
		if err := rows.Scan(&d.ID, &d.HardwareID, &d.Name, &d.ZoneID, &d.ZoneName, &d.tokenHash, &cfg, &d.PairedAt,
			&d.LastSeenAt, &status, &d.Version, &ips, &d.ActiveDeploymentID, &d.PreviousDeploymentID, &src,
			&d.CreatedAt, &d.UpdatedAt, &d.GroupID, &d.GroupName, &d.Position, &d.ClockKey); err != nil {
			return nil, err
		}
		d.Paired = d.tokenHash != ""
		d.Config = parseConfig(cfg)
		d.LastStatus = jsonOr(status, "{}")
		d.ActiveSource = jsonOr(src, "{}")
		if err := json.Unmarshal([]byte(ips), &d.IPs); err != nil || d.IPs == nil {
			d.IPs = []string{}
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

func jsonOr(s, def string) json.RawMessage {
	if json.Valid([]byte(s)) && strings.TrimSpace(s) != "" {
		return json.RawMessage(s)
	}
	return json.RawMessage(def)
}

func (m *Module) loadDevice(ctx context.Context, q rowsQuerier, id string) (*deviceRow, error) {
	rows, err := q.QueryContext(ctx, deviceSelect+` WHERE d.id = ?`, id)
	if err != nil {
		return nil, err
	}
	list, err := scanDevices(rows)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return list[0], nil
}

func (m *Module) loadDevicesByID(ctx context.Context, ids []string) (map[string]*deviceRow, error) {
	out := map[string]*deviceRow{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := m.app.DB.R.QueryContext(ctx, deviceSelect+` WHERE d.id IN (`+strings.Repeat("?,", len(ids)-1)+`?)`, args...)
	if err != nil {
		return nil, err
	}
	list, err := scanDevices(rows)
	if err != nil {
		return nil, err
	}
	for _, d := range list {
		out[d.ID] = d
	}
	return out, nil
}

// ---------------------------------------------------------------- device auth

type authDev struct {
	ID, HardwareID, Name, ZoneID, Version, TokenHash string
}

func hashToken(tok string) string {
	s := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(s[:])
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// deviceByToken resolves a bearer token (stored as SHA-256 only). nil = revoked/unknown.
func (m *Module) deviceByToken(ctx context.Context, tok string) (*authDev, error) {
	if tok == "" || len(tok) > 256 {
		return nil, nil
	}
	h := hashToken(tok)
	var d authDev
	err := m.app.DB.R.QueryRowContext(ctx, `SELECT id, hardware_id, name, COALESCE(zone_id, ''), version, token_hash
		FROM devices WHERE token_hash = ?`, h).Scan(&d.ID, &d.HardwareID, &d.Name, &d.ZoneID, &d.Version, &d.TokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// authDevice authenticates a device REST request.
func (m *Module) authDevice(r *http.Request) (*authDev, error) {
	tok := bearerToken(r)
	if tok == "" {
		return nil, errNoToken
	}
	d, err := m.deviceByToken(r.Context(), tok)
	if err != nil {
		return nil, errInternal.Wrap(err)
	}
	if d == nil {
		return nil, errRevoked
	}
	return d, nil
}

// ---------------------------------------------------------------- misc helpers

// ts formats a time for storage (fixed width, sortable, RFC 3339).
func ts(t time.Time) string { return t.UTC().Format(deploy.TimeFormat) }

func parseTS(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ipLimiter is a bounded fixed-window request counter per client IP.
type ipLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	max    int
	m      map[string]*ipWindow
}

type ipWindow struct {
	start time.Time
	n     int
}

func newIPLimiter(limit int, window time.Duration, max int) *ipLimiter {
	return &ipLimiter{limit: limit, window: window, max: max, m: map[string]*ipWindow{}}
}

func (l *ipLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.m[ip]
	if w == nil || now.Sub(w.start) >= l.window {
		if w == nil && len(l.m) >= l.max {
			for k, v := range l.m {
				if now.Sub(v.start) >= l.window {
					delete(l.m, k)
				}
			}
			if len(l.m) >= l.max {
				return false
			}
		}
		l.m[ip] = &ipWindow{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= l.limit
}
