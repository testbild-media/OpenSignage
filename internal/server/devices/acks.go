package devices

import (
	"context"
	"database/sql"
	"net/http"
	"sync"
	"time"

	"opensignage/internal/common/ids"
	"opensignage/internal/server/core"
	"opensignage/internal/server/httpx"
)

// Acknowledged problems: a user confirms a device problem, e.g. the under-voltage of a
// Raspberry Pi on a weak power supply. It then no longer counts in the sidebar marker, on
// the dashboard and in the device list. The acknowledgement lasts as long as the problem:
// when the server sees the problem gone it deletes the acknowledgement, so the problem is
// shown again when it comes back. Stored in alert_acks (survives server restarts).

// AlertAck is who acknowledged a problem and when.
type AlertAck struct {
	At string `json:"at"`
	By string `json:"by"`
}

var errAlertNotActive = httpx.E(http.StatusConflict, "alert_not_active", "the device does not have this problem (any more)")

// statusAlertKinds are the problems a status report decides about.
var statusAlertKinds = []string{AlertPlayerCrashed, AlertDiskLow, AlertDiskCritical, AlertVersionMismatch,
	AlertPowerLow, AlertVideoFallback, AlertGPULost}

type ackStore struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]map[string]AlertAck // device → kind → acknowledgement
}

func newAckStore() *ackStore { return &ackStore{m: map[string]map[string]AlertAck{}} }

// loadAcksLocked reads all acknowledgements once (again after an error). Must hold acks.mu.
func (m *Module) loadAcksLocked() bool {
	s := m.acks
	if s.loaded {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT device_id, kind, acked_at, username FROM alert_acks`)
	if err != nil {
		m.log.Error("devices: loading acknowledged problems", "err", err)
		return false
	}
	defer rows.Close()
	all := map[string]map[string]AlertAck{}
	for rows.Next() {
		var dev, kind string
		var a AlertAck
		if err := rows.Scan(&dev, &kind, &a.At, &a.By); err != nil {
			m.log.Error("devices: loading acknowledged problems", "err", err)
			return false
		}
		if all[dev] == nil {
			all[dev] = map[string]AlertAck{}
		}
		all[dev][kind] = a
	}
	if err := rows.Err(); err != nil {
		m.log.Error("devices: loading acknowledged problems", "err", err)
		return false
	}
	s.m, s.loaded = all, true
	return true
}

// ackOf returns the acknowledgement of a device problem (nil when none).
func (m *Module) ackOf(deviceID, kind string) *AlertAck {
	s := m.acks
	s.mu.Lock()
	defer s.mu.Unlock()
	if !m.loadAcksLocked() {
		return nil
	}
	if a, ok := s.m[deviceID][kind]; ok {
		return &a
	}
	return nil
}

// endAcks deletes the acknowledgements of problems that are gone. kinds are the problems
// the caller's observation decides about; active tells whether one holds now.
func (m *Module) endAcks(ctx context.Context, deviceID string, kinds []string, active func(kind string) bool) {
	s := m.acks
	s.mu.Lock()
	defer s.mu.Unlock()
	if !m.loadAcksLocked() || len(s.m[deviceID]) == 0 {
		return
	}
	for _, k := range kinds {
		if _, ok := s.m[deviceID][k]; !ok || active(k) {
			continue
		}
		if _, err := m.app.DB.W.ExecContext(ctx, `DELETE FROM alert_acks WHERE device_id = ? AND kind = ?`, deviceID, k); err != nil {
			m.log.Error("devices: ending an acknowledgement", "device", deviceID, "kind", k, "err", err)
			continue
		}
		delete(s.m[deviceID], k)
		m.log.Info("devices: problem gone, its acknowledgement ended", "device", deviceID, "kind", k)
	}
	if len(s.m[deviceID]) == 0 {
		delete(s.m, deviceID)
	}
}

// forgetAcks drops all acknowledgements of a device (deleted or paired anew).
func (m *Module) forgetAcks(ctx context.Context, deviceID string) {
	s := m.acks
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := m.app.DB.W.ExecContext(ctx, `DELETE FROM alert_acks WHERE device_id = ?`, deviceID); err != nil {
		m.log.Error("devices: forgetting acknowledgements", "device", deviceID, "err", err)
	}
	delete(s.m, deviceID)
}

type ackReq struct {
	DeviceID string `json:"deviceId"`
	Kind     string `json:"kind"`
}

// ackDevice loads the device of an acknowledgement request (visible to the user only).
func (m *Module) ackDevice(w http.ResponseWriter, r *http.Request, p *core.Principal) (*deviceRow, ackReq, error) {
	var req ackReq
	if err := httpx.Decode(w, r, 4<<10, &req); err != nil {
		return nil, req, err
	}
	if !ids.Valid(req.DeviceID) {
		return nil, req, httpx.Invalid("deviceId", "invalid")
	}
	if _, ok := alertSeverity[req.Kind]; !ok {
		return nil, req, httpx.Invalid("kind", "invalid_value")
	}
	row, err := m.loadDevice(r.Context(), m.app.DB.R, req.DeviceID)
	if err != nil {
		return nil, req, err
	}
	if row == nil || !visible(p, row.ZoneID) {
		return nil, req, errDeviceNotFound
	}
	return row, req, nil
}

// ackAlert acknowledges a current problem of a device (POST /api/v1/alerts/ack).
func (m *Module) ackAlert(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	row, req, err := m.ackDevice(w, r, p)
	if err != nil {
		return err
	}
	vs, err := m.views(r.Context(), []*deviceRow{row})
	if err != nil {
		return err
	}
	active := false
	for _, a := range vs[0].Alerts {
		active = active || a.Kind == req.Kind
	}
	if !active {
		return errAlertNotActive
	}
	ack := AlertAck{At: ts(time.Now()), By: p.Username}
	s := m.acks
	s.mu.Lock()
	defer s.mu.Unlock()
	if !m.loadAcksLocked() {
		return errInternal
	}
	if err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO alert_acks(device_id, kind, acked_at, user_id, username) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(device_id, kind) DO UPDATE SET acked_at = excluded.acked_at, user_id = excluded.user_id, username = excluded.username`,
			row.ID, req.Kind, ack.At, p.UserID, p.Username); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "alert.acknowledged", ObjectType: "device",
			ObjectID: row.ID, ObjectName: row.Name, ZoneID: row.ZoneID, New: map[string]any{"kind": req.Kind}})
	}); err != nil {
		return err
	}
	if s.m[row.ID] == nil {
		s.m[row.ID] = map[string]AlertAck{}
	}
	s.m[row.ID][req.Kind] = ack
	m.publish(EvAlertAck, row.ID, row.ZoneID, map[string]any{"kind": req.Kind, "ack": true})
	return httpx.OK(w, ack)
}

// unackAlert shows an acknowledged problem again (POST /api/v1/alerts/unack).
func (m *Module) unackAlert(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	row, req, err := m.ackDevice(w, r, p)
	if err != nil {
		return err
	}
	s := m.acks
	s.mu.Lock()
	defer s.mu.Unlock()
	if !m.loadAcksLocked() {
		return errInternal
	}
	if _, ok := s.m[row.ID][req.Kind]; !ok {
		return httpx.OK(w, map[string]any{})
	}
	if err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM alert_acks WHERE device_id = ? AND kind = ?`, row.ID, req.Kind); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "alert.unacknowledged", ObjectType: "device",
			ObjectID: row.ID, ObjectName: row.Name, ZoneID: row.ZoneID, Old: map[string]any{"kind": req.Kind}})
	}); err != nil {
		return err
	}
	delete(s.m[row.ID], req.Kind)
	if len(s.m[row.ID]) == 0 {
		delete(s.m, row.ID)
	}
	m.publish(EvAlertAck, row.ID, row.ZoneID, map[string]any{"kind": req.Kind, "ack": false})
	return httpx.OK(w, map[string]any{})
}
