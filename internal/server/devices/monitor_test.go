package devices_test

import (
	"strings"
	"testing"
	"time"

	"opensignage/internal/common/version"
	"opensignage/internal/contracts"
	"opensignage/internal/server/devices"
	"opensignage/internal/server/devices/devtest"

	"github.com/coder/websocket"
)

func (e *env) device(id string) map[string]any {
	e.t.Helper()
	code, out := e.admin.do("GET", "/api/v1/devices/"+id, nil)
	if code != 200 {
		e.t.Fatalf("device %s: %d %v", id, code, out)
	}
	return out
}

func (e *env) alerts() []map[string]any {
	e.t.Helper()
	code, out := e.admin.do("GET", "/api/v1/alerts", nil)
	if code != 200 {
		e.t.Fatalf("alerts: %d %v", code, out)
	}
	var list []map[string]any
	for _, x := range out["alerts"].([]any) {
		list = append(list, x.(map[string]any))
	}
	return list
}

func applied(d map[string]any, key string) string {
	a, _ := d["applied"].(map[string]any)
	s, _ := a[key].(string)
	return s
}

func alertKinds(list []any) []string {
	var out []string
	for _, x := range list {
		out = append(out, x.(map[string]any)["kind"].(string))
	}
	return out
}

// newStatus is a status as the current agent sends it.
func newStatus(free uint64, appliedCfg contracts.AppliedConfig) map[string]any {
	return map[string]any{
		"version": version.Version, "os": "linux", "arch": "arm64", "player": "ok", "mode": "playing",
		"cpu": 12.5, "ramUsed": 512 << 20, "ramTotal": 2048 << 20, "temperature": 51.2, "uptime": 3600,
		"diskTotal": 1000, "diskFree": free, "cacheBytes": 300, "logsBytes": 20,
		"activeSource": map[string]any{"kind": "playlist", "playlistId": "pl-evening", "ruleId": "r1"},
		"display": map[string]any{"output": "HDMI-1", "width": 1920, "height": 1080, "refresh": 60, "rotation": 0,
			"modes": []any{
				map[string]any{"width": 1920, "height": 1080, "refresh": []float64{60, 50, 59.94}, "preferred": 60, "current": 60},
				map[string]any{"width": 1280, "height": 720, "refresh": []float64{60}},
				map[string]any{"width": 10, "height": 10, "refresh": []float64{60}}, // nonsense: dropped
			}},
		"applied": appliedCfg,
		"caps":    []string{contracts.CapCleanCachePreview},
		"ips":     []string{"10.0.0.7"},
	}
}

// TestMonitoring: the device view carries the modes the screen reports, the playlist
// name on screen and the device's problems; low free space raises a warning (< 20 %) or
// a critical alert (< 10 %) with the time it was first seen; an older agent without the
// new fields shows nothing instead of wrong data.
func TestMonitoring(t *testing.T) {
	e := newEnv(t)
	devtest.Zone(t, e.db(), "z1", "Bar")
	devtest.Exec(t, e.db(), `INSERT INTO playlists(id, name, draft, created_at, updated_at) VALUES ('pl-evening', 'Evening menu', '{}', ?, ?)`,
		"2026-10-11T10:00:00Z", "2026-10-11T10:00:00Z")
	id, tok := e.pair("hw-mon", "z1")
	h := hello("hw-mon")
	a := e.connect(tok, &h)
	a.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)

	// An older agent: no modes, no applied config, no logs size.
	a.send(contracts.MsgStatus, map[string]any{"version": version.Version, "player": "ok", "mode": "playing",
		"diskTotal": 1000, "diskFree": 500, "ips": []string{"10.0.0.7"}})
	eventually(t, 2*time.Second, func() bool {
		m, _ := e.device(id)["metrics"].(map[string]any)
		return m["diskFree"] == float64(500)
	})
	d := e.device(id)
	if modes := d["displayModes"].([]any); len(modes) != 0 {
		t.Fatalf("older agent: modes %v", modes)
	}
	if applied(d, "display") != devices.ApplyUnknown || applied(d, "blackout") != devices.ApplyConfirmed {
		t.Fatalf("older agent: applied %v", d["applied"])
	}
	if al := d["alerts"].([]any); len(al) != 0 {
		t.Fatalf("older agent without problems: %v", al)
	}

	// A current agent with 15 % free space.
	a.send(contracts.MsgStatus, newStatus(150, contracts.AppliedConfig{}))
	eventually(t, 2*time.Second, func() bool { return len(e.device(id)["displayModes"].([]any)) > 0 })
	d = e.device(id)
	modes := d["displayModes"].([]any)
	if len(modes) != 2 || !strings.Contains(toJSON(modes[0]), `"refresh":[60,50,59.94]`) || !strings.Contains(toJSON(modes[0]), `"preferred":60`) {
		t.Fatalf("modes: %v", modes)
	}
	if c := d["activeContent"].(map[string]any); c["kind"] != "playlist" || c["playlistName"] != "Evening menu" {
		t.Fatalf("active content: %v", c)
	}
	if applied(d, "display") != devices.ApplyConfirmed || applied(d, "blackout") != devices.ApplyConfirmed {
		t.Fatalf("applied: %v", d["applied"])
	}
	al := d["alerts"].([]any)
	if len(al) != 1 {
		t.Fatalf("alerts: %v", al)
	}
	low := al[0].(map[string]any)
	if low["kind"] != devices.AlertDiskLow || low["severity"] != devices.SeverityWarning || low["detail"] != "15.00" || low["since"] == "" {
		t.Fatalf("disk warning: %v", low)
	}
	since := low["since"]

	// 5 % free: critical, still the same "since"; a crashed player comes first as well.
	st := newStatus(50, contracts.AppliedConfig{})
	st["player"] = "crashed"
	a.send(contracts.MsgStatus, st)
	eventually(t, 2*time.Second, func() bool { return len(e.alerts()) == 2 })
	list := e.alerts()
	for _, x := range list {
		if x["severity"] != devices.SeverityCritical {
			t.Fatalf("expected critical alerts only: %v", list)
		}
		if x["kind"] == devices.AlertDiskCritical && (x["since"] != since || x["detail"] != "5.00") {
			t.Fatalf("disk critical: %v (since %v)", x, since)
		}
		if x["deviceName"] != "Screen hw-mon" || x["zoneName"] != "Bar" {
			t.Fatalf("alert names: %v", x)
		}
	}
	if kinds := alertKinds(e.device(id)["alerts"].([]any)); len(kinds) != 2 {
		t.Fatalf("device alerts: %v", kinds)
	}

	// Enough space and a running player again: no problems.
	a.send(contracts.MsgStatus, newStatus(600, contracts.AppliedConfig{}))
	eventually(t, 2*time.Second, func() bool { return len(e.alerts()) == 0 })

	// A problem of a second device shows up with its id.
	devtest.Zone(t, e.db(), "z2", "Terrace")
	id2, tok2 := e.pair("hw-mon2", "z2")
	h2 := hello("hw-mon2")
	b := e.connect(tok2, &h2)
	b.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)
	b.send(contracts.MsgStatus, newStatus(10, contracts.AppliedConfig{}))
	eventually(t, 2*time.Second, func() bool { return len(e.alerts()) == 1 })
	if e.alerts()[0]["deviceId"] != id2 {
		t.Fatalf("alerts: %v", e.alerts())
	}
}

// TestConfigConfirmation: blackout and display changes are pending until the device
// reports them applied, time out without an answer and show a failure with the device's
// reason; a mode set in the client WebGUI is shown as such.
func TestConfigConfirmation(t *testing.T) {
	e := newEnv(t, func(m *devices.Module) { m.ActionTimeout = 400 * time.Millisecond })
	devtest.Zone(t, e.db(), "z1", "Bar")
	id, tok := e.pair("hw-conf", "z1")
	h := hello("hw-conf")
	a := e.connect(tok, &h)
	a.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)
	a.send(contracts.MsgStatus, newStatus(600, contracts.AppliedConfig{}))
	eventually(t, 2*time.Second, func() bool { return applied(e.device(id), "display") == devices.ApplyConfirmed })

	// Blackout: pending until the device confirms.
	if code, out := e.admin.do("POST", "/api/v1/devices/"+id+"/blackout", map[string]any{"on": true}); code != 200 || applied(out, "blackout") != devices.ApplyPending {
		t.Fatalf("blackout: %d %v", code, out["applied"])
	}
	a.waitFor(contracts.MsgDeviceConfig, 2*time.Second, func(m contracts.LinkMessage) bool {
		return decode[contracts.DeviceConfig](t, m.P).Blackout
	})
	a.send(contracts.MsgStatus, newStatus(600, contracts.AppliedConfig{Blackout: true}))
	eventually(t, 2*time.Second, func() bool { return applied(e.device(id), "blackout") == devices.ApplyConfirmed })

	// Display: pending, then timeout when the device does not confirm in time.
	want := contracts.DisplayConfig{Width: 1280, Height: 720, Refresh: 60}
	code, out := e.admin.do("PATCH", "/api/v1/devices/"+id, map[string]any{"config": map[string]any{"display": want}})
	if code != 200 || applied(out, "display") != devices.ApplyPending {
		t.Fatalf("display change: %d %v", code, out["applied"])
	}
	eventually(t, 3*time.Second, func() bool { return applied(e.device(id), "display") == devices.ApplyTimeout })

	// The device reports a failure with its reason.
	a.send(contracts.MsgStatus, newStatus(600, contracts.AppliedConfig{Blackout: true, Display: want,
		DisplayError: "mode 1280x720@60.00 is not reported by the screen"}))
	eventually(t, 2*time.Second, func() bool { return applied(e.device(id), "display") == devices.ApplyFailed })
	if s := e.device(id)["applied"].(map[string]any)["displayError"]; !strings.Contains(s.(string), "not reported") {
		t.Fatalf("failure reason: %v", s)
	}
	// Applied after all.
	a.send(contracts.MsgStatus, newStatus(600, contracts.AppliedConfig{Blackout: true, Display: want, DisplayMode: want}))
	eventually(t, 2*time.Second, func() bool { return applied(e.device(id), "display") == devices.ApplyConfirmed })
	// Changed in the client WebGUI afterwards.
	local := contracts.DisplayConfig{Width: 1920, Height: 1080, Refresh: 50}
	a.send(contracts.MsgStatus, newStatus(600, contracts.AppliedConfig{Blackout: true, Display: want, DisplayMode: local}))
	eventually(t, 2*time.Second, func() bool { return applied(e.device(id), "display") == devices.ApplyLocal })
	if m := e.device(id)["applied"].(map[string]any)["displayMode"].(map[string]any); m["width"] != float64(1920) || m["refresh"] != float64(50) {
		t.Fatalf("local mode: %v", m)
	}
}

// TestCleanCachePreview: the preview is a dry run of the clean_cache action, sent only
// to agents that report the capability (an older agent would clean at once).
func TestCleanCachePreview(t *testing.T) {
	e := newEnv(t)
	devtest.Zone(t, e.db(), "z1", "Bar")
	id, tok := e.pair("hw-clean", "z1")
	h := hello("hw-clean")
	a := e.connect(tok, &h)
	a.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)

	preview := map[string]any{"name": "clean_cache", "params": map[string]any{"dryRun": true}}
	a.send(contracts.MsgStatus, map[string]any{"version": version.Version, "player": "ok"})
	eventually(t, 2*time.Second, func() bool {
		m, _ := e.device(id)["metrics"].(map[string]any)
		return m["player"] == "ok"
	})
	if code, out := e.admin.do("POST", "/api/v1/devices/"+id+"/actions", preview); code != 409 || errCode(out) != "action_not_supported" {
		t.Fatalf("preview on an older agent: %d %v", code, out)
	}
	a.quiet(200*time.Millisecond, contracts.MsgAction)
	if code, _ := e.admin.do("POST", "/api/v1/devices/"+id+"/actions", map[string]any{"name": "clean_cache", "params": map[string]any{"dryRun": "yes"}}); code != 422 {
		t.Fatalf("invalid dryRun: %d", code)
	}

	a.send(contracts.MsgStatus, newStatus(600, contracts.AppliedConfig{}))
	eventually(t, 2*time.Second, func() bool { return len(e.device(id)["displayModes"].([]any)) > 0 })
	code, out := e.admin.do("POST", "/api/v1/devices/"+id+"/actions", preview)
	if code != 200 {
		t.Fatalf("preview: %d %v", code, out)
	}
	rid := out["requestId"].(string)
	m := a.waitFor(contracts.MsgAction, 2*time.Second, nil)
	if act := decode[contracts.Action](t, m.P); act.Name != "clean_cache" || !strings.Contains(string(act.Params), `"dryRun":true`) {
		t.Fatalf("preview action: %+v", act)
	}
	a.send(contracts.MsgActionResult, map[string]any{"id": rid, "ok": true,
		"data": contracts.CacheCleanup{DryRun: true, Deleted: 3, FreedBytes: 4096, Partial: 1, PartialBytes: 1024, CacheBytes: 2048}})
	eventually(t, 2*time.Second, func() bool {
		_, s := e.admin.do("GET", "/api/v1/devices/"+id+"/actions/"+rid, nil)
		return s["state"] == "ok" && strings.Contains(toJSON(s["data"]), `"freedBytes":4096`)
	})
}

// TestPlasmaRegeneration: plasma_regeneration needs {"on": bool} and an agent that knows the
// action; it is forwarded with exactly that parameter and audited with the state.
func TestPlasmaRegeneration(t *testing.T) {
	e := newEnv(t)
	devtest.Zone(t, e.db(), "z1", "Bar")
	id, tok := e.pair("hw-regen", "z1")
	h := hello("hw-regen")
	a := e.connect(tok, &h)
	a.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)

	on := map[string]any{"name": "plasma_regeneration", "params": map[string]any{"on": true}}
	a.send(contracts.MsgStatus, map[string]any{"version": version.Version, "player": "ok"})
	eventually(t, 2*time.Second, func() bool {
		m, _ := e.device(id)["metrics"].(map[string]any)
		return m["player"] == "ok"
	})
	if code, out := e.admin.do("POST", "/api/v1/devices/"+id+"/actions", on); code != 409 || errCode(out) != "action_not_supported" {
		t.Fatalf("regeneration on an older agent: %d %v", code, out)
	}
	st := newStatus(600, contracts.AppliedConfig{})
	st["caps"] = []string{contracts.CapCleanCachePreview, contracts.CapRegeneration}
	a.send(contracts.MsgStatus, st)
	eventually(t, 2*time.Second, func() bool { return len(e.device(id)["displayModes"].([]any)) > 0 })
	for _, bad := range []map[string]any{
		{"name": "plasma_regeneration"},
		{"name": "plasma_regeneration", "params": map[string]any{}},
		{"name": "plasma_regeneration", "params": map[string]any{"on": "yes"}},
	} {
		if code, _ := e.admin.do("POST", "/api/v1/devices/"+id+"/actions", bad); code != 422 {
			t.Fatalf("invalid params %v: %d", bad, code)
		}
	}
	a.quiet(200*time.Millisecond, contracts.MsgAction)
	code, out := e.admin.do("POST", "/api/v1/devices/"+id+"/actions",
		map[string]any{"name": "plasma_regeneration", "params": map[string]any{"on": true, "extra": 1}})
	if code != 200 {
		t.Fatalf("regeneration: %d %v", code, out)
	}
	m := a.waitFor(contracts.MsgAction, 2*time.Second, nil)
	if act := decode[contracts.Action](t, m.P); act.Name != "plasma_regeneration" || string(act.Params) != `{"on":true}` {
		t.Fatalf("regeneration action: %+v %s", act, act.Params)
	}
	var audited string
	if err := e.db().R.QueryRow(`SELECT new_value FROM audit_log WHERE action = 'device.action' ORDER BY id DESC LIMIT 1`).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(audited, `"on":true`) || !strings.Contains(audited, `"plasma_regeneration"`) {
		t.Fatalf("audit %s", audited)
	}
}

// TestAlertAcknowledgement: an acknowledged problem no longer counts as open; it ends when
// the server sees the problem gone, so the problem shows again when it comes back. Only
// current problems can be acknowledged; "show again" ends it by hand.
func TestAlertAcknowledgement(t *testing.T) {
	e := newEnv(t)
	devtest.Zone(t, e.db(), "z1", "Bar")
	id, tok := e.pair("hw-ack", "z1")
	h := hello("hw-ack")
	a := e.connect(tok, &h)
	a.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)

	status := func(underVoltage bool, last string) {
		st := newStatus(600, contracts.AppliedConfig{})
		st["power"] = map[string]any{"underVoltage": underVoltage, "events": 2, "last": last}
		a.send(contracts.MsgStatus, st)
	}
	alerts := func() (int, map[string]any) {
		code, out := e.admin.do("GET", "/api/v1/alerts", nil)
		if code != 200 {
			t.Fatalf("alerts: %d %v", code, out)
		}
		for _, x := range out["alerts"].([]any) {
			if al := x.(map[string]any); al["kind"] == "power_low" {
				return int(out["count"].(float64)), al
			}
		}
		return int(out["count"].(float64)), nil
	}
	acks := func() int {
		var n int
		if err := e.db().R.QueryRow(`SELECT COUNT(*) FROM alert_acks WHERE device_id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	ack := map[string]any{"deviceId": id, "kind": "power_low"}

	status(true, time.Now().UTC().Format(time.RFC3339))
	eventually(t, 2*time.Second, func() bool { _, al := alerts(); return al != nil })
	if n, al := alerts(); n != 1 || al["ack"] != nil {
		t.Fatalf("open problem: count %d, %v", n, al)
	}
	if code, out := e.admin.do("POST", "/api/v1/alerts/ack", map[string]any{"deviceId": id, "kind": "disk_low"}); code != 409 || errCode(out) != "alert_not_active" {
		t.Fatalf("ack of a problem the device does not have: %d %v", code, out)
	}
	if code, _ := e.admin.do("POST", "/api/v1/alerts/ack", map[string]any{"deviceId": id, "kind": "coffee_empty"}); code != 422 {
		t.Fatalf("unknown kind: %d", code)
	}
	if code, out := e.admin.do("POST", "/api/v1/alerts/ack", ack); code != 200 || out["by"] == "" || out["at"] == "" {
		t.Fatalf("ack: %d %v", code, out)
	}
	n, al := alerts()
	if n != 0 || al == nil || al["ack"] == nil {
		t.Fatalf("acknowledged: count %d, %v", n, al)
	}
	if d := e.device(id); len(d["alerts"].([]any)) != 1 || d["alerts"].([]any)[0].(map[string]any)["ack"] == nil {
		t.Fatalf("device view: %v", d["alerts"])
	}

	// Still under-voltage: the acknowledgement stays.
	status(true, time.Now().UTC().Format(time.RFC3339))
	time.Sleep(200 * time.Millisecond)
	if n, _ := alerts(); n != 0 || acks() != 1 {
		t.Fatalf("problem lasts: count %d, acks %d", n, acks())
	}
	// Gone (last event long ago): the acknowledgement ends.
	status(false, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	eventually(t, 2*time.Second, func() bool { return acks() == 0 })
	if n, al := alerts(); n != 0 || al != nil {
		t.Fatalf("problem gone: count %d, %v", n, al)
	}
	// Back again: open, not acknowledged.
	status(true, time.Now().UTC().Format(time.RFC3339))
	eventually(t, 2*time.Second, func() bool { _, al := alerts(); return al != nil })
	if n, al := alerts(); n != 1 || al["ack"] != nil {
		t.Fatalf("problem back: count %d, %v", n, al)
	}
	// Acknowledged and shown again by hand.
	if code, _ := e.admin.do("POST", "/api/v1/alerts/ack", ack); code != 200 {
		t.Fatalf("ack again: %d", code)
	}
	if code, _ := e.admin.do("POST", "/api/v1/alerts/unack", ack); code != 200 {
		t.Fatalf("unack: %d", code)
	}
	if n, al := alerts(); n != 1 || al["ack"] != nil || acks() != 0 {
		t.Fatalf("shown again: count %d, %v", n, al)
	}
	var audited int
	if err := e.db().R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action IN ('alert.acknowledged', 'alert.unacknowledged') AND object_id = ?`, id).Scan(&audited); err != nil || audited != 3 {
		t.Fatalf("audit entries: %d %v", audited, err)
	}
	// Deleting the device removes its acknowledgements (foreign key).
	if code, _ := e.admin.do("POST", "/api/v1/alerts/ack", ack); code != 200 {
		t.Fatal("ack before delete")
	}
	if code, _ := e.admin.do("DELETE", "/api/v1/devices/"+id, nil); code != 204 {
		t.Fatalf("delete device: %d", code)
	}
	if acks() != 0 {
		t.Fatal("acknowledgements of a deleted device must go")
	}
}

// An acknowledged offline problem ends when the device connects again: the next time it
// goes offline the problem is open again.
func TestOfflineAcknowledgementEndsOnReconnect(t *testing.T) {
	e := newEnv(t, func(m *devices.Module) { m.OfflineAfter = 50 * time.Millisecond })
	devtest.Zone(t, e.db(), "z1", "Bar")
	id, tok := e.pair("hw-off", "z1")
	h := hello("hw-off")
	a := e.connect(tok, &h)
	a.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)
	goOffline := func(a *agent) {
		_ = a.ws.Close(websocket.StatusGoingAway, "bye")
		<-a.done
		eventually(t, 2*time.Second, func() bool { return !e.mod.Online(id) })
		eventually(t, 2*time.Second, func() bool { return e.offlineAlerts()[id] })
	}
	openCount := func() int {
		_, out := e.admin.do("GET", "/api/v1/alerts", nil)
		return int(out["count"].(float64))
	}
	goOffline(a)
	if code, out := e.admin.do("POST", "/api/v1/alerts/ack", map[string]any{"deviceId": id, "kind": "device_offline"}); code != 200 {
		t.Fatalf("ack offline: %d %v", code, out)
	}
	if n := openCount(); n != 0 {
		t.Fatalf("acknowledged offline problem counted: %d", n)
	}
	b := e.connect(tok, &h)
	b.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)
	goOffline(b)
	if n := openCount(); n != 1 {
		t.Fatalf("offline again after a reconnect must be open: %d", n)
	}
}
