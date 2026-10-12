package devices

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"opensignage/internal/common/ids"
	"opensignage/internal/common/tlsutil"
	"opensignage/internal/common/version"
	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

const pairingValidity = 24 * time.Hour

// pairingInfo is stored in pairing_requests.info.
type pairingInfo struct {
	Hostname string          `json:"hostname"`
	Name     string          `json:"name,omitempty"`
	Version  string          `json:"version"`
	OS       string          `json:"os"`
	Arch     string          `json:"arch"`
	IPs      []string        `json:"ips"`
	Screen   json.RawMessage `json:"screen,omitempty"`
}

func printable(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validatePairRequest(req *contracts.PairRequest) error {
	req.HardwareID = strings.TrimSpace(req.HardwareID)
	switch {
	case req.HardwareID == "" || !printable(req.HardwareID, 128):
		return httpx.Invalid("hardwareId", "invalid")
	case !printable(req.Hostname, 100):
		return httpx.Invalid("hostname", "invalid")
	case !printable(req.Name, 400) || utf8.RuneCountInString(req.Name) > 100: // like device names
		return httpx.Invalid("name", "invalid")
	case !printable(req.Version, 64):
		return httpx.Invalid("version", "invalid")
	case !printable(req.OS, 32):
		return httpx.Invalid("os", "invalid")
	case !printable(req.Arch, 32):
		return httpx.Invalid("arch", "invalid")
	case len(req.Screen) > 4096:
		return httpx.Invalid("screen", "too_large")
	}
	ips, err := cleanIPs(req.IPs)
	if err != nil {
		return httpx.Invalid("ips", "invalid")
	}
	req.IPs = ips
	if len(req.Screen) > 0 && string(req.Screen) != "null" {
		canon, err := contracts.CanonicalizeJSON(req.Screen)
		if err != nil {
			return httpx.Invalid("screen", "invalid")
		}
		req.Screen = canon
	} else {
		req.Screen = nil
	}
	return nil
}

// randomPIN returns a uniformly distributed 6-digit PIN.
func randomPIN() string {
	var b [4]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			panic("crypto/rand failed: " + err.Error())
		}
		n := binary.BigEndian.Uint32(b[:])
		if n < 4294000000 { // largest multiple of 10^6 below 2^32: no modulo bias
			return fmt.Sprintf("%06d", n%1000000)
		}
	}
}

// pairRequest handles POST /device/v1/pair/request. A new request of the same hardware
// replaces its open request and keeps the PIN shown on screen. Open requests are bounded:
// at most MaxPairingsPerIP per source address (further hardware from there is refused), and
// at most MaxOpenPairings in total, where the oldest open request makes room for a new one
// (a flood can never lock out real players; an evicted agent simply requests again).
func (m *Module) pairRequest(w http.ResponseWriter, r *http.Request) error {
	ip := httpx.ClientIP(r)
	if !m.pairRL.allow(ip, time.Now()) {
		return errPairingBusy
	}
	var req contracts.PairRequest
	if err := decodeDevice(w, r, 64<<10, &req); err != nil {
		return err
	}
	if err := validatePairRequest(&req); err != nil {
		return err
	}
	info, err := json.Marshal(pairingInfo{Hostname: req.Hostname, Name: strings.TrimSpace(req.Name), Version: req.Version, OS: req.OS, Arch: req.Arch,
		IPs: req.IPs, Screen: req.Screen})
	if err != nil {
		return err
	}
	now := time.Now()
	secret := ids.Token(32)
	resp := contracts.PairResponse{PairingID: ids.New(), Secret: secret, ExpiresAt: ts(now.Add(pairingValidity))}
	err = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM pairing_requests WHERE expires_at <= ?`, ts(now)); err != nil {
			return err
		}
		var fromIP int
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM pairing_requests
			WHERE source_ip = ? AND device_id IS NULL AND hardware_id <> ?`, ip, req.HardwareID).Scan(&fromIP); err != nil {
			return err
		}
		if fromIP >= m.MaxPairingsPerIP {
			return errPairingBusy
		}
		var pin string
		err := tx.QueryRowContext(r.Context(), `SELECT pin FROM pairing_requests WHERE hardware_id = ? AND device_id IS NULL
			ORDER BY created_at DESC LIMIT 1`, req.HardwareID).Scan(&pin)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM pairing_requests WHERE hardware_id = ? AND device_id IS NULL`, req.HardwareID); err != nil {
			return err
		}
		var open int
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM pairing_requests WHERE device_id IS NULL`).Scan(&open); err != nil {
			return err
		}
		if evict := open - m.MaxOpenPairings + 1; evict > 0 {
			res, err := tx.ExecContext(r.Context(), `DELETE FROM pairing_requests WHERE id IN (
				SELECT id FROM pairing_requests WHERE device_id IS NULL ORDER BY created_at, id LIMIT ?)`, evict)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			m.log.Warn("pairing: too many open requests, oldest evicted", "evicted", n, "remote", ip)
		}
		if pin == "" {
			for i := 0; i < 100 && pin == ""; i++ {
				cand := randomPIN()
				var taken int
				if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM pairing_requests WHERE pin = ? AND device_id IS NULL`, cand).Scan(&taken); err != nil {
					return err
				}
				if taken == 0 {
					pin = cand
				}
			}
			if pin == "" {
				return errPairingBusy
			}
		}
		resp.PIN = pin
		_, err = tx.ExecContext(r.Context(), `INSERT INTO pairing_requests(id, pin, hardware_id, info, secret_hash, created_at, expires_at, source_ip)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, resp.PairingID, pin, req.HardwareID, string(info), hashToken(secret), ts(now), resp.ExpiresAt, ip)
		return err
	})
	if err != nil {
		return err
	}
	m.log.Info("pairing requested", "hostname", req.Hostname, "version", req.Version, "remote", ip)
	m.publish(EvPairing, "", "", map[string]any{"pairingId": resp.PairingID})
	return httpx.OK(w, resp)
}

// pairStatus handles GET /device/v1/pair/status?id=&secret=. The token is handed out
// exactly once (cleared in the same transaction that reads it).
func (m *Module) pairStatus(w http.ResponseWriter, r *http.Request) error {
	id := r.URL.Query().Get("id")
	secret := r.URL.Query().Get("secret")
	if id == "" || secret == "" || len(id) > 64 || len(secret) > 256 {
		return httpx.ErrBadRequest
	}
	var out contracts.PairStatus
	var deviceName, zoneID string
	delivered := false
	// Polls are read-only; only the one poll that hands out the token writes.
	read := func(q interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}) (deviceID string, token sql.NullString, err error) {
		var secretHash, expires string
		err = q.QueryRowContext(r.Context(), `SELECT secret_hash, expires_at, COALESCE(device_id, ''), token
			FROM pairing_requests WHERE id = ?`, id).Scan(&secretHash, &expires, &deviceID, &token)
		if errors.Is(err, sql.ErrNoRows) {
			out = contracts.PairStatus{State: "expired"}
			return "", token, nil
		}
		if err != nil {
			return "", token, err
		}
		if subtle.ConstantTimeCompare([]byte(hashToken(secret)), []byte(secretHash)) != 1 {
			return "", token, errPairingSecret
		}
		switch exp, ok := parseTS(expires); {
		case deviceID != "":
			out = contracts.PairStatus{State: "claimed", DeviceID: deviceID}
		case !ok || !time.Now().Before(exp):
			out = contracts.PairStatus{State: "expired"}
		default:
			out = contracts.PairStatus{State: "pending"}
		}
		return deviceID, token, nil
	}
	_, token, err := read(m.app.DB.R)
	if err != nil {
		return err
	}
	if out.State == "claimed" && token.Valid && token.String != "" {
		err = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
			deviceID, token, err := read(tx)
			if err != nil || out.State != "claimed" || !token.Valid || token.String == "" {
				return err
			}
			res, err := tx.ExecContext(r.Context(), `UPDATE pairing_requests SET token = NULL WHERE id = ? AND token IS NOT NULL`, id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return nil
			}
			out.Token = token.String
			delivered = true
			if err := tx.QueryRowContext(r.Context(), `SELECT name, COALESCE(zone_id, '') FROM devices WHERE id = ?`, deviceID).
				Scan(&deviceName, &zoneID); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			return core.Audit(r.Context(), tx, nil, httpx.ClientIP(r), core.AuditEntry{Action: "device.paired",
				ObjectType: "device", ObjectID: deviceID, ObjectName: deviceName, ZoneID: zoneID})
		})
		if err != nil {
			return err
		}
	}
	if delivered {
		m.log.Info("pairing completed, token delivered", "device", out.DeviceID)
		m.publish(EvPairing, out.DeviceID, zoneID, map[string]any{"delivered": true})
	}
	return httpx.OK(w, out)
}

func (m *Module) cleanupPairing(ctx context.Context) {
	if _, err := m.app.DB.W.ExecContext(ctx, `DELETE FROM pairing_requests WHERE expires_at <= ?`, ts(time.Now())); err != nil && ctx.Err() == nil {
		m.log.Error("pairing: cleanup", "err", err)
	}
}

// PairingView is an open pairing request in the admin UI.
type PairingView struct {
	ID              string          `json:"id"`
	PIN             string          `json:"pin"`
	HardwareID      string          `json:"hardwareId"`
	Hostname        string          `json:"hostname"`
	Name            string          `json:"name,omitempty"` // given at installation
	Version         string          `json:"version"`
	VersionMatch    bool            `json:"versionMatch"`
	OS              string          `json:"os"`
	Arch            string          `json:"arch"`
	IPs             []string        `json:"ips"`
	Screen          json.RawMessage `json:"screen,omitempty"`
	CreatedAt       string          `json:"createdAt"`
	ExpiresAt       string          `json:"expiresAt"`
	AgeSeconds      int64           `json:"ageSeconds"`
	KnownDeviceID   string          `json:"knownDeviceId,omitempty"`
	KnownDeviceName string          `json:"knownDeviceName,omitempty"`
}

// dismissPairing handles DELETE /api/v1/devices/pairing/{id}: hides an open request from the
// list (an unknown screen nobody wants to take over). The screen keeps its PIN, so it can
// still be taken over by PIN; it shows up again when it asks anew (restart or expiry).
func (m *Module) dismissPairing(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var info string
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(r.Context(), `SELECT info FROM pairing_requests WHERE id = ? AND device_id IS NULL`, id).Scan(&info); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return httpx.ErrNotFound
			}
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE pairing_requests SET dismissed_at = ? WHERE id = ?`, ts(time.Now()), id); err != nil {
			return err
		}
		var pi pairingInfo
		_ = json.Unmarshal([]byte(info), &pi)
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "device.pairing_dismissed",
			ObjectType: "pairing", ObjectID: id, ObjectName: pi.Hostname})
	})
	if err != nil {
		return err
	}
	m.publish(EvPairing, "", "", map[string]any{"pairingId": id, "dismissed": true})
	return httpx.NoContent(w)
}

func (m *Module) pairingList(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	now := time.Now()
	rows, err := m.app.DB.R.QueryContext(r.Context(), `SELECT p.id, p.pin, p.hardware_id, p.info, p.created_at, p.expires_at,
			COALESCE(d.id, ''), COALESCE(d.name, '')
		FROM pairing_requests p LEFT JOIN devices d ON d.hardware_id = p.hardware_id
		WHERE p.device_id IS NULL AND p.dismissed_at IS NULL AND p.expires_at > ? ORDER BY p.created_at DESC LIMIT 500`, ts(now))
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []PairingView{}
	for rows.Next() {
		var v PairingView
		var info string
		if err := rows.Scan(&v.ID, &v.PIN, &v.HardwareID, &info, &v.CreatedAt, &v.ExpiresAt, &v.KnownDeviceID, &v.KnownDeviceName); err != nil {
			return err
		}
		var pi pairingInfo
		if err := json.Unmarshal([]byte(info), &pi); err != nil {
			m.log.Error("pairing: unreadable info", "pairing", v.ID, "err", err)
		}
		v.Hostname, v.Name, v.Version, v.OS, v.Arch, v.Screen = pi.Hostname, pi.Name, pi.Version, pi.OS, pi.Arch, pi.Screen
		v.IPs = pi.IPs
		if v.IPs == nil {
			v.IPs = []string{}
		}
		v.VersionMatch = v.Version == version.Version
		if t, ok := parseTS(v.CreatedAt); ok {
			v.AgeSeconds = int64(now.Sub(t).Seconds())
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	fp := ""
	if m.app.DeviceID != nil && m.app.DeviceID.Cert != nil {
		fp = tlsutil.ShortFingerprint(tlsutil.SPKIFingerprint(m.app.DeviceID.Cert))
	}
	return httpx.OK(w, map[string]any{"pairingRequests": out, "fingerprint": fp, "serverVersion": version.Version})
}

type claimReq struct {
	PIN       string  `json:"pin"`
	Name      string  `json:"name"`
	ZoneID    *string `json:"zoneId"`
	ScaleMode *string `json:"scaleMode"`
}

// claim adopts the device behind an open PIN. The same hardware id always maps to the
// same device row (re-pair keeps history and settings; a new token replaces the old).
func (m *Module) claim(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req claimReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	req.PIN = strings.TrimSpace(req.PIN)
	if len(req.PIN) != 6 || strings.Trim(req.PIN, "0123456789") != "" {
		return httpx.Invalid("pin", "invalid")
	}
	name := strings.TrimSpace(req.Name)
	if utf8.RuneCountInString(name) > 100 || !printable(name, 400) {
		return httpx.Invalid("name", "invalid")
	}
	scale := m.app.Settings.Get().DeviceDefaults.ScaleMode
	if req.ScaleMode != nil {
		if !validScaleMode(*req.ScaleMode) {
			return httpx.Invalid("scaleMode", "invalid")
		}
		scale = *req.ScaleMode
	}
	zoneID := ""
	if req.ZoneID != nil {
		zoneID = strings.TrimSpace(*req.ZoneID)
	}
	token := ids.Token(32)
	now := time.Now()
	var deviceID string
	rePaired := false
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var pairID, hw, info string
		err := tx.QueryRowContext(r.Context(), `SELECT id, hardware_id, info FROM pairing_requests
			WHERE pin = ? AND device_id IS NULL AND expires_at > ? ORDER BY created_at DESC LIMIT 1`, req.PIN, ts(now)).Scan(&pairID, &hw, &info)
		if errors.Is(err, sql.ErrNoRows) {
			return errPairingNotFound
		}
		if err != nil {
			return err
		}
		if zoneID != "" {
			var n int
			if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM zones WHERE id = ?`, zoneID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return httpx.Invalid("zoneId", "unknown_zone")
			}
		}
		var pi pairingInfo
		if err := json.Unmarshal([]byte(info), &pi); err != nil {
			m.log.Error("pairing: unreadable info", "pairing", pairID, "err", err)
		}
		if name == "" {
			name = pi.Name // given at installation
		}
		if name == "" {
			name = strings.TrimSpace(pi.Hostname)
		}
		if name == "" {
			name = "Player"
		}
		ips, _ := json.Marshal(nonNil(pi.IPs))
		var existingID, existingCfg, existingZone string
		err = tx.QueryRowContext(r.Context(), `SELECT id, config, COALESCE(zone_id, '') FROM devices WHERE hardware_id = ?`, hw).
			Scan(&existingID, &existingCfg, &existingZone)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var cfg Config
		if existingID != "" {
			rePaired = true
			deviceID = existingID
			cfg = parseConfig(existingCfg)
			cfg.ScaleMode = scale
			cfgJSON, _ := json.Marshal(cfg)
			if _, err := tx.ExecContext(r.Context(), `UPDATE devices SET name = ?, zone_id = NULLIF(?, ''), token_hash = ?, config = ?,
				paired_at = ?, version = ?, ips = ?, updated_at = ? WHERE id = ?`,
				name, zoneID, hashToken(token), string(cfgJSON), ts(now), pi.Version, string(ips), ts(now), deviceID); err != nil {
				return err
			}
			if zoneID != existingZone { // another zone: no group, the next free place there
				if err := placeClaimed(r.Context(), tx, deviceID, zoneID); err != nil {
					return err
				}
			}
		} else {
			deviceID = ids.New()
			cfg = defaultConfig(scale)
			cfgJSON, _ := json.Marshal(cfg)
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO devices(id, hardware_id, name, zone_id, token_hash, config, paired_at,
				version, ips, created_at, updated_at) VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?)`,
				deviceID, hw, name, zoneID, hashToken(token), string(cfgJSON), ts(now), pi.Version, string(ips), ts(now), ts(now)); err != nil {
				return err
			}
			if err := placeClaimed(r.Context(), tx, deviceID, zoneID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE pairing_requests SET device_id = ?, token = ?, expires_at = ? WHERE id = ?`,
			deviceID, token, ts(now.Add(pairingValidity)), pairID); err != nil {
			return err
		}
		// Older requests of this hardware (including undelivered tokens) are void now.
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM pairing_requests WHERE hardware_id = ? AND id <> ?`, hw, pairID); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "device.claimed", ObjectType: "device",
			ObjectID: deviceID, ObjectName: name, ZoneID: zoneID, New: map[string]any{"name": name, "zoneId": zoneID,
				"scaleMode": cfg.ScaleMode, "rePaired": rePaired, "hardwareId": hw, "version": pi.Version}})
	})
	if err != nil {
		return err
	}
	// The previous token (if any) is invalid now: drop a connection that still uses it.
	if c := m.hub.get(deviceID); c != nil {
		c.revoke()
	}
	m.publish(EvClaimed, deviceID, zoneID, map[string]any{"rePaired": rePaired})
	return m.writeDevice(w, r, deviceID)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// placeClaimed gives a claimed device the next free place among the screens without group
// of its zone; without zone it has neither group nor place.
func placeClaimed(ctx context.Context, tx *sql.Tx, deviceID, zoneID string) error {
	pos := 0
	if zoneID != "" {
		p, err := modkit.NextPosition(ctx, tx, zoneID, "", deviceID)
		if err != nil {
			return err
		}
		pos = p
	}
	_, err := tx.ExecContext(ctx, `UPDATE devices SET group_id = NULL, position = NULLIF(?, 0) WHERE id = ?`, pos, deviceID)
	return err
}
