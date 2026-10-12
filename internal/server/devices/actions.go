package devices

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"opensignage/internal/common/ids"
	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
	"opensignage/internal/server/httpx"
)

// Remote action names. Blackout is a device setting with its own endpoint because the
// `user` role may use it.
var actionNames = map[string]bool{"sync_now": true, "reload_player": true, "restart_client": true,
	"reboot_device": true, "clean_cache": true, "screenshot": true, "plasma_regeneration": true}

// Action states.
const (
	ActionPending = "pending"
	ActionOK      = "ok"
	ActionFailed  = "failed"
	ActionTimeout = "timeout"
)

const (
	actionRetention   = 5 * time.Minute
	actionsMax        = 1000
	actionsPerDevice  = 20
	plannedOfflineFor = 5 * time.Minute
)

// ActionView is the API representation of a remote action.
type ActionView struct {
	ID        string          `json:"requestId"`
	DeviceID  string          `json:"deviceId"`
	Name      string          `json:"name"`
	State     string          `json:"state"`
	Error     string          `json:"error,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt string          `json:"createdAt"`
	DoneAt    string          `json:"doneAt,omitempty"`
}

type action struct {
	ActionView
	created time.Time
	done    time.Time
}

// actionStore keeps actions in memory: bounded count, 5 minute retention after they
// finish, pending ones time out.
type actionStore struct {
	mu sync.Mutex
	m  map[string]*action
}

func newActionStore() *actionStore { return &actionStore{m: map[string]*action{}} }

func (s *actionStore) create(deviceID, name string, now time.Time) (*ActionView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	pending := 0
	for _, a := range s.m {
		if a.DeviceID == deviceID && a.State == ActionPending {
			pending++
		}
	}
	if pending >= actionsPerDevice {
		return nil, false
	}
	if len(s.m) >= actionsMax {
		// Evict the oldest finished actions first; refuse when all are pending.
		var finished []*action
		for _, a := range s.m {
			if a.State != ActionPending {
				finished = append(finished, a)
			}
		}
		sort.Slice(finished, func(i, j int) bool { return finished[i].done.Before(finished[j].done) })
		for i := 0; len(s.m) >= actionsMax && i < len(finished); i++ {
			delete(s.m, finished[i].ID)
		}
		if len(s.m) >= actionsMax {
			return nil, false
		}
	}
	a := &action{ActionView: ActionView{ID: ids.New(), DeviceID: deviceID, Name: name, State: ActionPending,
		CreatedAt: now.UTC().Format(time.RFC3339Nano)}, created: now}
	s.m[a.ID] = a
	v := a.ActionView
	return &v, true
}

func (s *actionStore) get(id string, now time.Time, timeout time.Duration) (ActionView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.m[id]
	if a == nil {
		return ActionView{}, false
	}
	s.expireLocked(a, now, timeout)
	return a.ActionView, true
}

func (s *actionStore) finishLocked(a *action, state, errText string, data json.RawMessage, now time.Time) {
	a.State, a.Error, a.Data = state, errText, data
	a.done = now
	a.DoneAt = now.UTC().Format(time.RFC3339Nano)
}

func (s *actionStore) expireLocked(a *action, now time.Time, timeout time.Duration) bool {
	if a.State == ActionPending && now.Sub(a.created) >= timeout {
		s.finishLocked(a, ActionTimeout, "", nil, now)
		return true
	}
	return false
}

// result applies an action.result from the device that owns the action. A successful
// screenshot result waits for the upload (which completes the action).
func (s *actionStore) result(deviceID string, r contracts.ActionResult) (ActionView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.m[r.ID]
	if a == nil || a.DeviceID != deviceID || a.State != ActionPending {
		return ActionView{}, false
	}
	now := time.Now()
	switch {
	case !r.OK:
		errText := r.Error
		if errText == "" {
			errText = "failed"
		}
		s.finishLocked(a, ActionFailed, errText, r.Data, now)
	case a.Name == "screenshot":
		return ActionView{}, false
	default:
		s.finishLocked(a, ActionOK, "", r.Data, now)
	}
	return a.ActionView, true
}

// completeScreenshot marks a pending screenshot action done after the upload.
func (s *actionStore) completeScreenshot(deviceID, id string, data json.RawMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.m[id]
	if a == nil || a.DeviceID != deviceID || a.Name != "screenshot" || a.State != ActionPending {
		return false
	}
	s.finishLocked(a, ActionOK, "", data, time.Now())
	return true
}

func (s *actionStore) pendingScreenshot(deviceID, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.m[id]
	return a != nil && a.DeviceID == deviceID && a.Name == "screenshot" && a.State == ActionPending
}

func (s *actionStore) fail(id, errText string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.m[id]; a != nil && a.State == ActionPending {
		s.finishLocked(a, ActionFailed, errText, nil, time.Now())
	}
}

func (s *actionStore) forgetDevice(deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, a := range s.m {
		if a.DeviceID == deviceID {
			delete(s.m, id)
		}
	}
}

// sweep times out pending actions and drops expired finished ones. It returns the
// actions that just timed out.
func (s *actionStore) sweep(now time.Time, timeout time.Duration) []ActionView {
	s.mu.Lock()
	defer s.mu.Unlock()
	var changed []ActionView
	for _, a := range s.m {
		if s.expireLocked(a, now, timeout) {
			changed = append(changed, a.ActionView)
		}
	}
	s.pruneLocked(now)
	return changed
}

func (s *actionStore) pruneLocked(now time.Time) {
	for id, a := range s.m {
		if a.State != ActionPending && now.Sub(a.done) > actionRetention {
			delete(s.m, id)
		}
	}
}

// ---------------------------------------------------------------- HTTP

type actionReq struct {
	Name   string          `json:"name"`
	Params json.RawMessage `json:"params"`
}

func (m *Module) postAction(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req actionReq
	if err := httpx.Decode(w, r, 16<<10, &req); err != nil {
		return err
	}
	if !actionNames[req.Name] {
		return httpx.Invalid("name", "unknown_action")
	}
	params := json.RawMessage(nil)
	if len(req.Params) > 0 && string(req.Params) != "null" {
		var obj map[string]any
		if err := json.Unmarshal(req.Params, &obj); err != nil || obj == nil {
			return httpx.Invalid("params", "invalid")
		}
		params = req.Params
	}
	row, err := m.loadDevice(r.Context(), m.app.DB.R, id)
	if err != nil {
		return err
	}
	if row == nil {
		return errDeviceNotFound
	}
	if req.Name == "clean_cache" && params != nil {
		var cp struct {
			DryRun *bool `json:"dryRun"`
		}
		if err := json.Unmarshal(params, &cp); err != nil {
			return httpx.Invalid("params", "invalid")
		}
		// An older agent ignores dryRun and would clean at once: a preview is only sent to
		// agents that report the capability.
		if cp.DryRun != nil && *cp.DryRun && !hasCap(parseStatus(row.LastStatus), contracts.CapCleanCachePreview) {
			return errActionUnsupported
		}
	}
	audit := map[string]any{"name": req.Name}
	if req.Name == "plasma_regeneration" {
		// {"on": true|false}: white noise until switched off or the client restarts.
		var rp struct {
			On *bool `json:"on"`
		}
		if params == nil || json.Unmarshal(params, &rp) != nil || rp.On == nil {
			return httpx.Invalid("params", "invalid")
		}
		if !hasCap(parseStatus(row.LastStatus), contracts.CapRegeneration) {
			return errActionUnsupported
		}
		params, _ = json.Marshal(map[string]bool{"on": *rp.On})
		audit["on"] = *rp.On
	}
	c := m.hub.get(id)
	if c == nil {
		return errDeviceOffline
	}
	a, ok := m.actions.create(id, req.Name, time.Now())
	if !ok {
		return errActionsBusy
	}
	if req.Name == "screenshot" {
		params, _ = json.Marshal(map[string]string{"upload": "/device/v1/screenshots/" + a.ID})
	}
	b, err := encode(contracts.MsgAction, a.ID, contracts.Action{ID: a.ID, Name: req.Name, Params: params})
	if err != nil {
		m.actions.fail(a.ID, "encode")
		return err
	}
	// Audited before it is sent: every action that reaches a device is in the log.
	if err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "device.action", ObjectType: "device",
			ObjectID: id, ObjectName: row.Name, ZoneID: row.ZoneID, New: withRequest(audit, a.ID)})
	}); err != nil {
		m.actions.fail(a.ID, "audit_failed")
		return err
	}
	if !c.enqueue(b) {
		m.actions.fail(a.ID, "send_failed")
		return errDeviceOffline
	}
	switch req.Name {
	case "sync_now":
		m.pushDeploy(r.Context(), c, false)
	case "reboot_device", "restart_client":
		m.planOffline(id, m.PlannedOffline)
	}
	m.publish(EvAction, id, row.ZoneID, map[string]any{"requestId": a.ID, "state": a.State, "name": req.Name})
	return httpx.OK(w, map[string]any{"requestId": a.ID})
}

func (m *Module) getAction(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	a, ok := m.actions.get(r.PathValue("requestId"), time.Now(), m.ActionTimeout)
	if !ok || a.DeviceID != r.PathValue("id") {
		return errActionNotFound
	}
	return httpx.OK(w, a)
}

// withRequest adds the request id to an action's audit details.
func withRequest(m map[string]any, requestID string) map[string]any {
	m["requestId"] = requestID
	return m
}
