package devices_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"opensignage/internal/contracts"
	"opensignage/internal/server/devices"
	"opensignage/internal/server/devices/devtest"
)

// postAction starts a remote action and returns its request id.
func (e *env) postAction(id, name string) string {
	e.t.Helper()
	code, out := e.admin.do("POST", "/api/v1/devices/"+id+"/actions", map[string]any{"name": name})
	if code != 200 {
		e.t.Fatalf("action %s: %d %v", name, code, out)
	}
	return out["requestId"].(string)
}

func (e *env) actionState(id, rid string) string {
	_, out := e.admin.do("GET", "/api/v1/devices/"+id+"/actions/"+rid, nil)
	s, _ := out["state"].(string)
	return s
}

// offlineAlerts returns the ids of devices with a device_offline alert.
func (e *env) offlineAlerts() map[string]bool {
	e.t.Helper()
	code, out := e.admin.do("GET", "/api/v1/alerts", nil)
	if code != 200 {
		e.t.Fatalf("alerts: %d %v", code, out)
	}
	ids := map[string]bool{}
	for _, x := range out["alerts"].([]any) {
		a := x.(map[string]any)
		if a["kind"] == "device_offline" {
			ids[a["deviceId"].(string)] = true
		}
	}
	return ids
}

// TestSyncNowPushesDeployment covers the "Sync" action: it reaches the device and the zone's
// current deployment is offered again even though nothing changed (a normal rebuild skips
// identical offers), so a device that lost or doubts its content re-checks it.
func TestSyncNowPushesDeployment(t *testing.T) {
	e := newEnv(t)
	c := devtest.SeedContent(t, e.db(), e.srv.App.Cfg.DataDir)
	if err := e.mod.RebuildNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	id, tok := e.pair("hw-sync", c.Z1)
	h := hello("hw-sync")
	a := e.connect(tok, &h)
	ms := a.expect(contracts.MsgWelcome, contracts.MsgDeviceConfig, contracts.MsgLiveState, contracts.MsgDeployAvailable)
	first := decode[contracts.DeployAvailable](t, ms[3].P)

	// Without an action, an unchanged target is not sent again.
	if err := e.mod.RebuildNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.quiet(300*time.Millisecond, contracts.MsgDeployAvailable)

	rid := e.postAction(id, "sync_now")
	var gotAction, gotDeploy bool
	deadline := time.Now().Add(3 * time.Second)
	for !(gotAction && gotDeploy) {
		m, ok := a.next(time.Until(deadline))
		if !ok {
			t.Fatalf("sync_now: action %v, deployment %v", gotAction, gotDeploy)
		}
		switch m.T {
		case contracts.MsgAction:
			act := decode[contracts.Action](t, m.P)
			if act.ID != rid || act.Name != "sync_now" {
				t.Fatalf("action message: %+v", act)
			}
			gotAction = true
		case contracts.MsgDeployAvailable:
			if d := decode[contracts.DeployAvailable](t, m.P); d.ID != first.ID || d.Hash != first.Hash {
				t.Fatalf("sync offered %+v, want current deployment %+v", d, first)
			}
			gotDeploy = true
		}
	}
	if s := e.actionState(id, rid); s != "pending" {
		t.Fatalf("before the device answers: %q", s)
	}
	a.send(contracts.MsgActionResult, map[string]any{"id": rid, "ok": true})
	eventually(t, 2*time.Second, func() bool { return e.actionState(id, rid) == "ok" })

	// The action is audited with its name.
	_, audit := e.admin.do("GET", "/api/v1/audit?action=device.action", nil)
	if !strings.Contains(toJSON(audit), "sync_now") {
		t.Fatalf("sync_now not audited: %v", audit)
	}

	// A device without zone gets the explicit "cleared" target on sync.
	if code, _ := e.admin.do("PATCH", "/api/v1/devices/"+id, map[string]any{"zoneId": nil}); code != 200 {
		t.Fatal("remove zone")
	}
	a.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)
	e.postAction(id, "sync_now")
	cl := a.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)
	if decode[contracts.DeployCleared](t, cl.P).Reason != "no_zone" {
		t.Fatalf("sync without zone: %s", cl.P)
	}
}

// TestRebootPlansOfflineWindow covers the device reboot: after reboot_device (and
// restart_client) the device is expected to go offline, so no offline alert fires within
// the planned window; an unplanned disconnect and a device that never returns are
// reported.
func TestRebootPlansOfflineWindow(t *testing.T) {
	e := newEnv(t, func(m *devices.Module) {
		m.OfflineAfter = 50 * time.Millisecond
		m.PlannedOffline = 3 * time.Second
	})
	devtest.Zone(t, e.db(), "z1", "Bar")
	idR, tokR := e.pair("hw-reboot", "z1")
	idS, tokS := e.pair("hw-restart", "z1")
	idC, tokC := e.pair("hw-crash", "z1")
	agents := map[string]*agent{}
	for id, tok := range map[string]string{idR: tokR, idS: tokS, idC: tokC} {
		hw := map[string]string{idR: "hw-reboot", idS: "hw-restart", idC: "hw-crash"}[id]
		h := hello(hw)
		agents[id] = e.connect(tok, &h)
		agents[id].waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)
	}

	for id, name := range map[string]string{idR: "reboot_device", idS: "restart_client"} {
		a := agents[id]
		rid := e.postAction(id, name)
		m := a.waitFor(contracts.MsgAction, 3*time.Second, nil)
		if act := decode[contracts.Action](t, m.P); act.ID != rid || act.Name != name {
			t.Fatalf("action message: %+v", act)
		}
		// The device confirms and goes away (reboot).
		a.send(contracts.MsgActionResult, map[string]any{"id": rid, "ok": true})
		eventually(t, 2*time.Second, func() bool { return e.actionState(id, rid) == "ok" })
	}
	for _, a := range agents {
		_ = a.ws.Close(websocket.StatusGoingAway, "bye")
		<-a.done
	}
	eventually(t, 2*time.Second, func() bool { return !e.mod.Online(idR) && !e.mod.Online(idS) && !e.mod.Online(idC) })
	time.Sleep(150 * time.Millisecond) // well past OfflineAfter

	alerts := e.offlineAlerts()
	if !alerts[idC] {
		t.Fatalf("unplanned disconnect must raise an offline alert: %v", alerts)
	}
	if alerts[idR] || alerts[idS] {
		t.Fatalf("planned reboot/restart must not raise an offline alert: %v", alerts)
	}

	// Back online in time: no alert, and the device is online again.
	hS := hello("hw-restart")
	back := e.connect(tokS, &hS)
	back.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)
	if !e.mod.Online(idS) || e.offlineAlerts()[idS] {
		t.Fatal("restarted device must be online without alert")
	}

	// A device that does not come back is reported once the planned window is over.
	eventually(t, 6*time.Second, func() bool { return e.offlineAlerts()[idR] })
}
