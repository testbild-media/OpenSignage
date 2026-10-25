package devices_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"opensignage/internal/common/version"
	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
	"opensignage/internal/server/devices"
	"opensignage/internal/server/devices/devtest"
)

func TestPairingFlow(t *testing.T) {
	e := newEnv(t)
	devtest.Zone(t, e.db(), "z1", "Bar")

	r1 := e.requestPairing("hw-1")
	if len(r1.PIN) != 6 || strings.Trim(r1.PIN, "0123456789") != "" || r1.Secret == "" {
		t.Fatalf("pair response: %+v", r1)
	}
	if code, st := e.pairStatus(r1.PairingID, r1.Secret); code != 200 || st.State != "pending" || st.Token != "" {
		t.Fatalf("pending: %d %+v", code, st)
	}
	if code, _ := e.pairStatus(r1.PairingID, "wrong-secret"); code != 403 {
		t.Fatalf("wrong secret must be 403, got %d", code)
	}
	// A re-request of the same hardware keeps the PIN on screen and replaces the request.
	r1b := e.requestPairing("hw-1")
	if r1b.PIN != r1.PIN || r1b.PairingID == r1.PairingID {
		t.Fatalf("re-request: %+v vs %+v", r1b, r1)
	}
	if _, st := e.pairStatus(r1.PairingID, r1.Secret); st.State != "expired" {
		t.Fatalf("replaced request must report expired: %+v", st)
	}
	// PINs are unique among open requests.
	r2 := e.requestPairing("hw-2")
	if r2.PIN == r1.PIN {
		t.Fatal("PIN collision between open requests")
	}
	code, out := e.admin.do("GET", "/api/v1/devices/pairing", nil)
	if code != 200 || !strings.Contains(toJSON(out), r1.PIN) || !strings.Contains(toJSON(out), "host-hw-1") || out["fingerprint"] == "" {
		t.Fatalf("pairing list: %d %v", code, out)
	}

	code, dev := e.claim(r1b.PIN, "Bar left", "z1")
	if code != 200 {
		t.Fatalf("claim: %d %v", code, dev)
	}
	deviceID := dev["id"].(string)
	cfg := dev["config"].(map[string]any)
	if dev["zoneId"] != "z1" || cfg["scaleMode"] != "contain" || cfg["perf"] != "auto" || cfg["hwVideo"] != "auto" ||
		cfg["showIpOverlay"] != true || cfg["plasmaShift"] != false || dev["paired"] != true {
		t.Fatalf("claimed device: %v", dev)
	}
	if code, out := e.claim(r1b.PIN, "again", ""); code != 404 || errCode(out) != "pairing_not_found" {
		t.Fatalf("claim twice: %d %v", code, out)
	}
	code, st := e.pairStatus(r1b.PairingID, r1b.Secret)
	if code != 200 || st.State != "claimed" || st.DeviceID != deviceID || len(st.Token) != 64 {
		t.Fatalf("claimed status: %d %+v", code, st)
	}
	token := st.Token
	if _, st := e.pairStatus(r1b.PairingID, r1b.Secret); st.State != "claimed" || st.Token != "" {
		t.Fatalf("token must be delivered exactly once: %+v", st)
	}
	if code, out := e.devJSON("GET", "/device/v1/deployments/none", token, nil); code != 404 || errCode(out) != "deployment_not_found" {
		t.Fatalf("token must authenticate: %d %v", code, out)
	}
	if code, out := e.devJSON("GET", "/device/v1/deployments/none", "", nil); code != 401 || errCode(out) != "unauthorized" {
		t.Fatalf("missing token: %d %v", code, out)
	}

	// Re-pair of the same hardware: same device row, old token revoked.
	pr := e.requestPairing("hw-1")
	code, again := e.claim(pr.PIN, "", "")
	if code != 200 || again["id"] != deviceID || again["name"] != "host-hw-1" {
		t.Fatalf("re-pair must reuse the device row: %d %v", code, again)
	}
	_, st2 := e.pairStatus(pr.PairingID, pr.Secret)
	if st2.DeviceID != deviceID || st2.Token == "" || st2.Token == token {
		t.Fatalf("re-pair status: %+v", st2)
	}
	if code, out := e.devJSON("GET", "/device/v1/deployments/none", token, nil); code != 401 || errCode(out) != "revoked" {
		t.Fatalf("old token must be revoked: %d %v", code, out)
	}
	var n int
	if err := e.db().R.QueryRow(`SELECT COUNT(*) FROM devices WHERE hardware_id = 'hw-1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("devices for hw-1: %d %v", n, err)
	}

	// Expiry.
	r3 := e.requestPairing("hw-3")
	devtest.Exec(t, e.db(), `UPDATE pairing_requests SET expires_at = '2020-01-01T00:00:00.000000000Z' WHERE id = ?`, r3.PairingID)
	if _, st := e.pairStatus(r3.PairingID, r3.Secret); st.State != "expired" {
		t.Fatalf("expired: %+v", st)
	}
	if code, out := e.claim(r3.PIN, "x", ""); code != 404 || errCode(out) != "pairing_not_found" {
		t.Fatalf("claim of expired PIN: %d %v", code, out)
	}
	if code, _ := e.claim("12345", "x", ""); code != 422 {
		t.Fatalf("invalid pin format: %d", code)
	}
	if code, out := e.claim(r2.PIN, "x", "no-such-zone"); code != 422 || !strings.Contains(toJSON(out), "unknown_zone") {
		t.Fatalf("unknown zone: %d %v", code, out)
	}
	if code, _ := e.devJSON("POST", "/device/v1/pair/request", "", map[string]any{"hardwareId": ""}); code != 422 {
		t.Fatalf("empty hardware id: %d", code)
	}
	// Audit trail.
	code, audit := e.admin.do("GET", "/api/v1/audit?action=device.", nil)
	if code != 200 || !strings.Contains(toJSON(audit), "device.claimed") || !strings.Contains(toJSON(audit), "device.paired") {
		t.Fatalf("audit: %v", audit)
	}
}

func TestRevokedToken(t *testing.T) {
	e := newEnv(t)
	devtest.Zone(t, e.db(), "z1", "Bar")
	id, tok := e.pair("hw-r", "z1")
	h := hello("hw-r")
	a := e.connect(tok, &h)
	a.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)

	if code, out := e.admin.do("POST", "/api/v1/devices/"+id+"/repair", nil); code != 200 || out["paired"] != false {
		t.Fatalf("repair: %d %v", code, out)
	}
	a.waitFor(contracts.MsgRevoked, 3*time.Second, nil)
	if st := a.closed(5 * time.Second); st != contracts.CloseRevoked {
		t.Fatalf("close status %d, want 4401", st)
	}
	if code, out := e.devJSON("GET", "/device/v1/blobs/"+strings.Repeat("a", 64), tok, nil); code != 401 || errCode(out) != "revoked" {
		t.Fatalf("REST with revoked token: %d %v", code, out)
	}
	b := e.connect(tok, &h)
	b.waitFor(contracts.MsgRevoked, 3*time.Second, nil)
	if st := b.closed(5 * time.Second); st != contracts.CloseRevoked {
		t.Fatalf("new link with revoked token: %d", st)
	}
	if _, res, err := e.dial(""); err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("link without token must be 401: %v %v", err, res)
	}
	// Same row stays (re-pair reuses it).
	if code, out := e.admin.do("GET", "/api/v1/devices/"+id, nil); code != 200 || out["paired"] != false || out["status"] != "unpaired" {
		t.Fatalf("device after repair: %d %v", code, out)
	}
	// Delete also revokes a live connection.
	id2, tok2 := e.pair("hw-d", "z1")
	h2 := hello("hw-d")
	c := e.connect(tok2, &h2)
	c.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)
	if code, _ := e.admin.do("DELETE", "/api/v1/devices/"+id2, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if st := c.closed(5 * time.Second); st != contracts.CloseRevoked {
		t.Fatalf("deleted device link: %d", st)
	}
	if code, _ := e.admin.do("GET", "/api/v1/devices/"+id2, nil); code != 404 {
		t.Fatalf("deleted device still visible: %d", code)
	}
}

func TestHandshakeSnapshotAndCleared(t *testing.T) {
	e := newEnv(t)
	devtest.Zone(t, e.db(), "z-empty", "Empty")
	id, tok := e.pair("hw-h", "z-empty")
	h := hello("hw-h")
	a := e.connect(tok, &h)
	ms := a.expect(contracts.MsgWelcome, contracts.MsgDeviceConfig, contracts.MsgLiveState, contracts.MsgDeployCleared)
	w := decode[contracts.Welcome](t, ms[0].P)
	if w.DeviceID != id || !w.VersionMatch || w.Protocol != 1 || w.ServerTime == "" {
		t.Fatalf("welcome: %+v", w)
	}
	cfg := decode[contracts.DeviceConfig](t, ms[1].P)
	if cfg.ZoneID != "z-empty" || cfg.ScaleMode != "contain" || !cfg.ShowIPOverlay || cfg.Name != "Screen hw-h" || cfg.Perf != "auto" {
		t.Fatalf("config: %+v", cfg)
	}
	ls := decode[contracts.LiveState](t, ms[2].P)
	if ls.Zone.ID != "z-empty" || ls.Zone.State != "OPEN" || ls.Ticker.Template != contracts.DefaultTickerTemplate() || ls.Products == nil {
		t.Fatalf("live: %+v", ls)
	}
	if cl := decode[contracts.DeployCleared](t, ms[3].P); cl.Reason != "no_content" {
		t.Fatalf("cleared: %+v", cl)
	}
	// Every connection re-confirms the target; the older connection is replaced.
	b := e.connect(tok, &h)
	b.expect(contracts.MsgWelcome, contracts.MsgDeviceConfig, contracts.MsgLiveState, contracts.MsgDeployCleared)
	if st := a.closed(5 * time.Second); st != contracts.CloseReplaced {
		t.Fatalf("older connection: %d", st)
	}
	if code, out := e.admin.do("GET", "/api/v1/devices/"+id, nil); code != 200 || out["online"] != true || out["status"] != "online" {
		t.Fatalf("online flag: %v", out)
	}

	// Version mismatch: live state and config yes, deployments never.
	id2, tok2 := e.pair("hw-old", "z-empty")
	h2 := hello("hw-old")
	h2.Version = "0.0.1"
	c := e.connect(tok2, &h2)
	ms = c.expect(contracts.MsgWelcome, contracts.MsgDeviceConfig, contracts.MsgLiveState)
	if decode[contracts.Welcome](t, ms[0].P).VersionMatch {
		t.Fatal("versionMatch must be false")
	}
	c.quiet(300*time.Millisecond, contracts.MsgDeployCleared, contracts.MsgDeployAvailable)
	code, out := e.admin.do("GET", "/api/v1/alerts", nil)
	if code != 200 || !strings.Contains(toJSON(out), `"kind":"version_mismatch"`) || !strings.Contains(toJSON(out), id2) {
		t.Fatalf("alerts: %v", out)
	}
	if code, b, _ := e.devDo("GET", "/device/v1/deployments/x", tok2, nil, nil); code != 409 || !bytes.Contains(b, []byte("version_mismatch")) {
		t.Fatalf("manifest for mismatched agent: %d %s", code, b)
	}
}

func TestHelloTimeoutAndBadHello(t *testing.T) {
	e := newEnv(t, func(m *devices.Module) { m.HelloTimeout = 200 * time.Millisecond })
	devtest.Zone(t, e.db(), "z1", "Bar")
	_, tok := e.pair("hw-t", "z1")
	a := e.connect(tok, nil)
	if st := a.closed(5 * time.Second); st != websocket.StatusPolicyViolation {
		t.Fatalf("hello timeout: %d", st)
	}
	b := e.connect(tok, nil)
	b.send(contracts.MsgStatus, map[string]any{})
	if st := b.closed(5 * time.Second); st != websocket.StatusPolicyViolation {
		t.Fatalf("status before hello: %d", st)
	}
}

func TestHeartbeatDropsDeadLink(t *testing.T) {
	e := newEnv(t, func(m *devices.Module) { m.PingInterval = 50 * time.Millisecond })
	devtest.Zone(t, e.db(), "z1", "Bar")
	id, tok := e.pair("hw-dead", "z1")
	ws, _, err := e.dial(tok)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	h, _ := json.Marshal(hello("hw-dead"))
	msg, _ := json.Marshal(contracts.LinkMessage{T: contracts.MsgHello, P: h})
	if err := ws.Write(context.Background(), websocket.MessageText, msg); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, func() bool { return e.mod.Online(id) })
	// This client never reads, so pings are never answered: the server drops it.
	eventually(t, 5*time.Second, func() bool { return !e.mod.Online(id) })
	var lastSeen string
	if err := e.db().R.QueryRow(`SELECT COALESCE(last_seen_at, '') FROM devices WHERE id = ?`, id).Scan(&lastSeen); err != nil || lastSeen == "" {
		t.Fatalf("last seen must be stored on disconnect: %q %v", lastSeen, err)
	}
}

func TestGarbageDoesNotAffectOthers(t *testing.T) {
	e := newEnv(t)
	d := e.db()
	devtest.Zone(t, d, "z1", "Bar")
	devtest.Exec(t, d, `INSERT INTO categories(id, name, created_at, updated_at) VALUES ('c', 'Drinks', 'x', 'x')`)
	devtest.Exec(t, d, `INSERT INTO products(id, category_id, name, type, price_amount, created_at, updated_at) VALUES ('p1', 'c', 'Beer', 'drink', 450, 'x', 'x')`)
	_, tokA := e.pair("hw-a", "z1")
	idB, tokB := e.pair("hw-b", "z1")
	_, tokC := e.pair("hw-c", "z1")
	ha, hb, hc := hello("hw-a"), hello("hw-b"), hello("hw-c")
	a, b, c := e.connect(tokA, &ha), e.connect(tokB, &hb), e.connect(tokC, &hc)
	for _, x := range []*agent{a, b, c} {
		x.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)
	}

	// A: binary, malformed JSON, unknown type (ignored), bad payloads, then an oversized frame.
	_ = a.write(websocket.MessageBinary, []byte{0, 1, 2, 3})
	_ = a.write(websocket.MessageText, []byte("{not json"))
	_ = a.write(websocket.MessageText, []byte(`{"t":"future.thing","p":{"x":1}}`))
	_ = a.write(websocket.MessageText, []byte(`{"t":"deploy.progress","p":{"id":"x","state":"exploded"}}`))
	_ = a.write(websocket.MessageText, []byte(`{"t":"status","p":[1,2,3]}`))
	_ = a.write(websocket.MessageText, []byte(`{"t":"hello","p":{"version":""}}`))
	_ = a.write(websocket.MessageText, []byte(`{"t":"action.result","p":{"id":"unknown","ok":true}}`))
	_ = a.write(websocket.MessageText, bytes.Repeat([]byte("x"), 1<<20+100))
	if st := a.closed(5 * time.Second); st != websocket.StatusMessageTooBig {
		t.Fatalf("oversized frame: close %d", st)
	}
	// C: more than 20 invalid messages within a minute → 1008.
	for i := 0; i < 25; i++ {
		if err := c.write(websocket.MessageText, []byte(fmt.Sprintf("garbage %d", i))); err != nil {
			break
		}
	}
	if st := c.closed(5 * time.Second); st != websocket.StatusPolicyViolation {
		t.Fatalf("invalid flood: close %d", st)
	}

	// B is unaffected: a live change arrives in well under a second.
	devtest.Exec(t, d, `UPDATE products SET status = 'SOLD_OUT' WHERE id = 'p1'`)
	start := time.Now()
	e.srv.App.Bus.Publish(core.Event{Topic: core.TopicLive, ZoneID: "z1"})
	b.waitFor(contracts.MsgLiveState, 2*time.Second, func(m contracts.LinkMessage) bool {
		ls := decode[contracts.LiveState](t, m.P)
		return len(ls.Products) == 1 && ls.Products[0].Status == "SOLD_OUT"
	})
	if el := time.Since(start); el > time.Second {
		t.Fatalf("live push took %v", el)
	}
	b.send(contracts.MsgStatus, map[string]any{"player": "ok", "cpu": 12.5, "ips": []string{"10.0.0.9"}})
	eventually(t, 2*time.Second, func() bool {
		var st, ips string
		_ = d.R.QueryRow(`SELECT last_status, ips FROM devices WHERE id = ?`, idB).Scan(&st, &ips)
		return strings.Contains(st, `"cpu":12.5`) && strings.Contains(ips, "10.0.0.9")
	})
	if code, out := e.admin.do("GET", "/api/v1/devices", nil); code != 200 || len(out["devices"].([]any)) != 3 {
		t.Fatalf("server must keep working: %d %v", code, out)
	}
}

func TestDeploymentsAndBlobAccess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := devtest.SeedContent(t, e.db(), e.srv.App.Cfg.DataDir)
	if err := e.mod.RebuildNow(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	countDeployments := func() int {
		_ = e.db().R.QueryRow(`SELECT COUNT(*) FROM deployments`).Scan(&count)
		return count
	}
	if n := countDeployments(); n != 2 {
		t.Fatalf("deployments after build: %d", n)
	}
	if err := e.mod.RebuildNow(ctx); err != nil || countDeployments() != 2 {
		t.Fatalf("rebuild without changes must not add deployments: %d %v", count, err)
	}
	id1, tok1 := e.pair("hw-z1", c.Z1)
	_, tok2 := e.pair("hw-z2", c.Z2)
	h := hello("hw-z1")
	a := e.connect(tok1, &h)
	ms := a.expect(contracts.MsgWelcome, contracts.MsgDeviceConfig, contracts.MsgLiveState, contracts.MsgDeployAvailable)
	avail := decode[contracts.DeployAvailable](t, ms[3].P)

	code, body, _ := e.devDo("GET", "/device/v1/deployments/"+avail.ID, tok1, nil, nil)
	if code != 200 {
		t.Fatalf("manifest: %d %s", code, body)
	}
	man := decode[contracts.Manifest](t, body)
	if hash, _ := man.CanonicalHash(); man.ID != avail.ID || man.Hash != avail.Hash || hash != avail.Hash || man.ZoneID != c.Z1 {
		t.Fatalf("manifest identity: %s %s %s", man.ID, man.Hash, hash)
	}
	if code, _, _ := e.devDo("GET", "/device/v1/deployments/"+avail.ID, tok2, nil, nil); code != 404 {
		t.Fatalf("foreign zone manifest: %d", code)
	}

	video := c.Blobs["video-orig"]
	code, body, hdr := e.devDo("GET", "/device/v1/blobs/"+video, tok1, nil, nil)
	if code != 200 || string(body) != "blob:video-orig" || hdr.Get("Content-Type") != "video/mp4" {
		t.Fatalf("blob: %d %q %v", code, body, hdr)
	}
	code, body, _ = e.devDo("GET", "/device/v1/blobs/"+video, tok1, nil, map[string]string{"Range": "bytes=5-9"})
	if code != 206 || string(body) != "video" {
		t.Fatalf("range: %d %q", code, body)
	}
	for name, tc := range map[string]struct {
		sha, tok string
	}{
		"foreign blob":         {c.Foreign, tok1},
		"other zone's blob":    {video, tok2},
		"zone 2 blob in zone1": {c.Blobs["z2-orig"], tok1},
		"thumbnail":            {c.Blobs["video-thumb"], tok1},
		"invalid hash":         {"../../etc/passwd", tok1},
	} {
		if code, b, _ := e.devDo("GET", "/device/v1/blobs/"+tc.sha, tc.tok, nil, nil); code != 404 {
			t.Fatalf("%s must be 404, got %d %s", name, code, b)
		}
	}
	if code, _, _ := e.devDo("GET", "/device/v1/blobs/"+c.Blobs["z2-orig"], tok2, nil, nil); code != 200 {
		t.Fatalf("zone 2 own blob: %d", code)
	}

	// Progress reports drive the rollout state; a failure raises an alert.
	a.send(contracts.MsgDeployProgress, contracts.DeployProgress{ID: avail.ID, State: "failed", Done: 3, Total: 10, Error: "disk full"})
	eventually(t, 2*time.Second, func() bool {
		_, out := e.admin.do("GET", "/api/v1/alerts", nil)
		return strings.Contains(toJSON(out), `"kind":"sync_failed"`)
	})
	a.send(contracts.MsgDeployProgress, contracts.DeployProgress{ID: avail.ID, State: "active", Done: 10, Total: 10})
	eventually(t, 2*time.Second, func() bool {
		_, out := e.admin.do("GET", "/api/v1/devices/"+id1, nil)
		sync, _ := out["sync"].(map[string]any)
		return sync["state"] == "active" && out["activeDeploymentId"] == avail.ID
	})
	if _, out := e.admin.do("GET", "/api/v1/alerts", nil); strings.Contains(toJSON(out), "sync_failed") {
		t.Fatalf("alert must clear after success: %v", out)
	}

	// Deleting the playable content clears the zone: the device is told at once.
	devtest.Exec(t, e.db(), `UPDATE zones SET closed_playlist_id = NULL WHERE id = ?`, c.Z1)
	devtest.Exec(t, e.db(), `UPDATE playlists SET archived_at = 'x' WHERE id = ?`, c.P1)
	e.srv.App.Bus.Publish(core.Event{Topic: core.TopicContent, Kind: "deleted"})
	cl := a.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)
	if decode[contracts.DeployCleared](t, cl.P).Reason != "no_content" {
		t.Fatalf("cleared payload: %s", cl.P)
	}
	// The cleared target is confirmed on reconnect, too.
	b := e.connect(tok1, &h)
	b.expect(contracts.MsgWelcome, contracts.MsgDeviceConfig, contracts.MsgLiveState, contracts.MsgDeployCleared)
	// Blobs of retained deployments stay downloadable (device may still need them).
	if code, _, _ := e.devDo("GET", "/device/v1/blobs/"+video, tok1, nil, nil); code != 200 {
		t.Fatalf("blob of previous deployment: %d", code)
	}
}

func TestActions(t *testing.T) {
	e := newEnv(t, func(m *devices.Module) { m.ActionTimeout = 300 * time.Millisecond })
	devtest.Zone(t, e.db(), "z1", "Bar")
	id, tok := e.pair("hw-act", "z1")
	h := hello("hw-act")
	a := e.connect(tok, &h)
	a.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)

	action := func(name string) (string, contracts.Action) {
		t.Helper()
		code, out := e.admin.do("POST", "/api/v1/devices/"+id+"/actions", map[string]any{"name": name})
		if code != 200 {
			t.Fatalf("action %s: %d %v", name, code, out)
		}
		rid := out["requestId"].(string)
		m := a.waitFor(contracts.MsgAction, 3*time.Second, nil)
		act := decode[contracts.Action](t, m.P)
		if act.ID != rid || act.Name != name || m.ID != rid {
			t.Fatalf("action message: %+v", act)
		}
		return rid, act
	}
	state := func(rid string) map[string]any {
		_, out := e.admin.do("GET", "/api/v1/devices/"+id+"/actions/"+rid, nil)
		return out
	}

	rid, _ := action("reload_player")
	if s := state(rid); s["state"] != "pending" {
		t.Fatalf("new action: %v", s)
	}
	eventually(t, 3*time.Second, func() bool { return state(rid)["state"] == "timeout" })

	rid, _ = action("clean_cache")
	a.send(contracts.MsgActionResult, map[string]any{"id": rid, "ok": true, "data": map[string]int{"freedBytes": 1234}})
	eventually(t, 2*time.Second, func() bool {
		s := state(rid)
		return s["state"] == "ok" && strings.Contains(toJSON(s), "1234")
	})
	rid, _ = action("restart_client")
	a.send(contracts.MsgActionResult, map[string]any{"id": rid, "ok": false, "error": "systemd unavailable"})
	eventually(t, 2*time.Second, func() bool { return state(rid)["state"] == "failed" })

	rid, act := action("screenshot")
	if !strings.Contains(string(act.Params), "/device/v1/screenshots/"+rid) {
		t.Fatalf("screenshot params: %s", act.Params)
	}
	if code, _, _ := e.devDo("POST", "/device/v1/screenshots/"+rid, tok, strings.NewReader("PNG data"), nil); code != 422 {
		t.Fatalf("non-JPEG upload: %d", code)
	}
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{7}, 1000)...)
	if code, b, _ := e.devDo("POST", "/device/v1/screenshots/"+rid, tok, bytes.NewReader(jpeg), map[string]string{"Content-Type": "image/jpeg"}); code != 204 {
		t.Fatalf("upload: %d %s", code, b)
	}
	if s := state(rid); s["state"] != "ok" {
		t.Fatalf("screenshot action: %v", s)
	}
	code, img, hdr := e.admin.raw("GET", "/api/v1/devices/"+id+"/screenshot", nil)
	if code != 200 || !bytes.Equal(img, jpeg) || hdr.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("screenshot download: %d %d bytes", code, len(img))
	}
	if code, _, _ := e.devDo("POST", "/device/v1/screenshots/"+rid, tok, bytes.NewReader(jpeg), nil); code != 404 {
		t.Fatalf("upload for finished action: %d", code)
	}
	if code, out := e.admin.do("POST", "/api/v1/devices/"+id+"/actions", map[string]any{"name": "format_disk"}); code != 422 {
		t.Fatalf("unknown action: %d %v", code, out)
	}
	if code, _ := e.admin.do("GET", "/api/v1/devices/"+id+"/actions/"+devicesFakeID, nil); code != 404 {
		t.Fatalf("unknown request id: %d", code)
	}
	_ = a.ws.Close(websocket.StatusNormalClosure, "bye")
	<-a.done
	eventually(t, 2*time.Second, func() bool { return !e.mod.Online(id) })
	if code, out := e.admin.do("POST", "/api/v1/devices/"+id+"/actions", map[string]any{"name": "sync_now"}); code != 409 || errCode(out) != "device_offline" {
		t.Fatalf("offline device: %d %v", code, out)
	}
}

const devicesFakeID = "01920000-0000-7000-8000-00000000dead"

func TestConfigZoneAndBlackout(t *testing.T) {
	e := newEnv(t)
	d := e.db()
	devtest.Zone(t, d, "z1", "Bar")
	devtest.Zone(t, d, "z2", "Terrace")
	id, tok := e.pair("hw-cfg", "z1")
	h := hello("hw-cfg")
	a := e.connect(tok, &h)
	a.waitFor(contracts.MsgDeployCleared, 3*time.Second, nil)

	code, out := e.admin.do("PATCH", "/api/v1/devices/"+id, map[string]any{"name": "Left screen", "config": map[string]any{
		"scaleMode": "cover", "perf": "low", "hwVideo": "off", "plasmaShift": true, "showIpOverlay": false,
		"display": map[string]any{"width": 1366, "height": 768, "refresh": 60, "rotation": 90, "custom": true}}})
	if code != 200 {
		t.Fatalf("patch: %d %v", code, out)
	}
	m := a.waitFor(contracts.MsgDeviceConfig, 2*time.Second, nil)
	cfg := decode[contracts.DeviceConfig](t, m.P)
	if cfg.Name != "Left screen" || cfg.ScaleMode != "cover" || cfg.Perf != "low" || cfg.HWVideo != "off" || !cfg.PlasmaShift ||
		cfg.ShowIPOverlay || cfg.Display.Width != 1366 || cfg.Display.Rotation != 90 || !cfg.Display.Custom {
		t.Fatalf("pushed config: %+v", cfg)
	}
	// Zone change: new live state immediately.
	if code, out := e.admin.do("PATCH", "/api/v1/devices/"+id, map[string]any{"zoneId": "z2"}); code != 200 || out["zoneId"] != "z2" {
		t.Fatalf("zone change: %d %v", code, out)
	}
	a.waitFor(contracts.MsgLiveState, 2*time.Second, func(m contracts.LinkMessage) bool {
		return decode[contracts.LiveState](t, m.P).Zone.ID == "z2"
	})
	// No zone: empty live state and an explicit cleared target.
	if code, _ := e.admin.do("PATCH", "/api/v1/devices/"+id, map[string]any{"zoneId": nil}); code != 200 {
		t.Fatalf("remove zone: %d", code)
	}
	cl := a.waitFor(contracts.MsgDeployCleared, 2*time.Second, nil)
	if decode[contracts.DeployCleared](t, cl.P).Reason != "no_zone" {
		t.Fatalf("no zone target: %s", cl.P)
	}
	for _, bad := range []map[string]any{
		{"config": map[string]any{"scaleMode": "stretch"}},
		{"config": map[string]any{"display": map[string]any{"width": 1920, "height": 1080, "rotation": 45}}},
		{"zoneId": "missing"},
		{"name": ""},
		{"unknown": 1},
	} {
		if code, _ := e.admin.do("PATCH", "/api/v1/devices/"+id, bad); code != 422 && code != 400 {
			t.Fatalf("invalid patch %v accepted: %d", bad, code)
		}
	}
	if code, _ := e.admin.do("PATCH", "/api/v1/devices/"+id, map[string]any{"zoneId": "z2"}); code != 200 {
		t.Fatal("zone back")
	}

	// Blackout is screen control for operators: users of the zone are refused, operators
	// and admins switch it.
	mkUser := func(name string, zones []string) *client {
		if code, out := e.admin.do("POST", "/api/v1/users", map[string]any{"username": name, "displayName": name, "role": "user",
			"password": "secret123", "passwordConfirm": "secret123", "active": true, "zoneIds": zones}); code != 200 {
			t.Fatalf("create user: %d %v", code, out)
		}
		u := e.newClient()
		if code, _ := u.do("POST", "/api/v1/auth/login", map[string]any{"username": name, "password": "secret123"}); code != 200 {
			t.Fatal("login")
		}
		return u
	}
	terrace := mkUser("terrace", []string{"z2"})
	bar := mkUser("bar", []string{"z1"})
	if code, _ := terrace.do("POST", "/api/v1/devices/"+id+"/blackout", map[string]any{"on": true}); code != 403 {
		t.Fatalf("user of the zone must not black out screens: %d", code)
	}
	if code, out := e.admin.do("POST", "/api/v1/devices/"+id+"/blackout", map[string]any{"on": true}); code != 200 {
		t.Fatalf("admin blackout: %d %v", code, out)
	}
	a.waitFor(contracts.MsgDeviceConfig, 2*time.Second, func(m contracts.LinkMessage) bool {
		return decode[contracts.DeviceConfig](t, m.P).Blackout
	})
	if code, _ := bar.do("POST", "/api/v1/devices/"+id+"/blackout", map[string]any{"on": false}); code != 403 {
		t.Fatalf("user of another zone: %d", code)
	}
	if code, _ := bar.do("PATCH", "/api/v1/devices/"+id, map[string]any{"name": "x"}); code != 403 {
		t.Fatalf("user must not patch: %d", code)
	}
	_, list := bar.do("GET", "/api/v1/devices", nil)
	if n := len(list["devices"].([]any)); n != 0 {
		t.Fatalf("user sees devices of other zones: %d", n)
	}
	_, list = terrace.do("GET", "/api/v1/devices", nil)
	if n := len(list["devices"].([]any)); n != 1 {
		t.Fatalf("user of the zone sees %d devices", n)
	}
	if code, out := terrace.do("GET", "/api/v1/alerts", nil); code != 200 || out["count"] == nil {
		t.Fatalf("alerts for users: %d %v", code, out)
	}
	// Reconnect: blackout survives in the snapshot.
	b := e.connect(tok, &h)
	ms := b.expect(contracts.MsgWelcome, contracts.MsgDeviceConfig)
	if !decode[contracts.DeviceConfig](t, ms[1].P).Blackout {
		t.Fatal("blackout must persist")
	}
}

func TestOfflineAlert(t *testing.T) {
	e := newEnv(t)
	devtest.Zone(t, e.db(), "z1", "Bar")
	id, _ := e.pair("hw-off", "z1")
	devtest.Exec(t, e.db(), `UPDATE devices SET last_seen_at = '2026-01-01T00:00:00.000000000Z' WHERE id = ?`, id)
	idU, _ := e.pair("hw-unpaired", "z1")
	devtest.Exec(t, e.db(), `UPDATE devices SET last_seen_at = '2026-01-01T00:00:00.000000000Z' WHERE id = ?`, idU)
	if code, _ := e.admin.do("POST", "/api/v1/devices/"+idU+"/repair", nil); code != 200 {
		t.Fatal("repair")
	}
	code, out := e.admin.do("GET", "/api/v1/alerts", nil)
	if code != 200 || out["count"] != float64(1) || !strings.Contains(toJSON(out), `"kind":"device_offline"`) || !strings.Contains(toJSON(out), id) {
		t.Fatalf("alerts: %v", out)
	}
	// Offline is a warning (screens switched off at night are no problem).
	if a := out["alerts"].([]any)[0].(map[string]any); a["severity"] != "warning" {
		t.Fatalf("offline severity: %v", a)
	}
}

// Without the updates module (no package provider) the device endpoint answers no_package.
func TestUpdatePackageWithoutProvider(t *testing.T) {
	e := newEnv(t)
	devtest.Zone(t, e.db(), "z1", "Bar")
	_, tok := e.pair("hw-up", "z1")
	if code, out := e.devJSON("GET", "/device/v1/update/package?version=1.0.0&os=linux&arch=arm64", tok, nil); code != 404 || errCode(out) != "no_package" {
		t.Fatalf("update package: %d %v", code, out)
	}
	if code, out := e.devJSON("GET", "/device/v1/update/release?version=1.0.0", tok, nil); code != 404 || errCode(out) != "no_package" {
		t.Fatalf("update release: %d %v", code, out)
	}
	for _, v := range []string{"..%2F1.0.0", "1.0.0%2Bx", "v1.0.0", ""} {
		if code, _ := e.devJSON("GET", "/device/v1/update/release?version="+v, tok, nil); code != 400 {
			t.Errorf("version %q: %d", v, code)
		}
	}
	if code, _ := e.devJSON("GET", "/device/v1/update/release?version=1.0.0", "wrong-token", nil); code != 401 {
		t.Errorf("the release metadata needs the device token: %d", code)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// An operator hides an unknown screen's request: it leaves the list, the screen keeps its
// PIN (pending, still claimable by PIN) and appears again when it asks anew.
func TestDismissPairing(t *testing.T) {
	e := newEnv(t)
	devtest.Zone(t, e.db(), "z1", "Bar")
	r := e.requestPairing("hw-x")
	if code, _ := e.admin.do("DELETE", "/api/v1/devices/pairing/"+r.PairingID, nil); code != 204 {
		t.Fatalf("dismiss: %d", code)
	}
	if _, out := e.admin.do("GET", "/api/v1/devices/pairing", nil); strings.Contains(toJSON(out), r.PIN) {
		t.Fatalf("dismissed request still listed: %v", out)
	}
	if _, st := e.pairStatus(r.PairingID, r.Secret); st.State != "pending" {
		t.Fatalf("the screen keeps waiting: %+v", st)
	}
	if code, _ := e.admin.do("DELETE", "/api/v1/devices/pairing/unknown-id", nil); code != 404 {
		t.Fatalf("unknown request: %d", code)
	}
	again := e.requestPairing("hw-x") // restart of the screen
	if _, out := e.admin.do("GET", "/api/v1/devices/pairing", nil); !strings.Contains(toJSON(out), again.PIN) {
		t.Fatalf("a new request is listed again: %v", out)
	}
}

// The name given at installation (OS_PLAYER_NAME) shows in the pairing list and becomes the
// device name when the admin keeps the proposal; a name that no device may have is refused.
func TestPairingProposesTheInstallationName(t *testing.T) {
	e := newEnv(t)
	code, out := e.devJSON("POST", "/device/v1/pair/request", "", map[string]any{"hardwareId": "hw-n", "hostname": "osp-theke-sued",
		"name": "Theke Süd", "version": version.Version, "os": "linux", "arch": "arm64", "ips": []string{"10.0.0.8"}})
	if code != 200 {
		t.Fatalf("pair request: %d %v", code, out)
	}
	pin := out["pin"].(string)
	if code, list := e.admin.do("GET", "/api/v1/devices/pairing", nil); code != 200 || !strings.Contains(toJSON(list), `"name":"Theke Süd"`) {
		t.Fatalf("pairing list: %d %v", code, list)
	}
	code, dev := e.claim(pin, "", "")
	if code != 200 || dev["name"] != "Theke Süd" {
		t.Fatalf("claim without a name: %d %v", code, dev)
	}
	code, out = e.devJSON("POST", "/device/v1/pair/request", "", map[string]any{"hardwareId": "hw-x", "hostname": "h",
		"name": strings.Repeat("n", 101), "version": version.Version, "os": "linux", "arch": "arm64"})
	if code != 422 {
		t.Fatalf("a name over 100 characters must be refused: %d %v", code, out)
	}
}

// Display groups: the device config carries group, place and the zone's clock key; the
// server answers time pings at once with its time.
func TestGroupPlaceClockKeyAndTimePing(t *testing.T) {
	e := newEnv(t)
	d := e.db()
	devtest.Zone(t, d, "z1", "Bar")
	devtest.Exec(t, d, `UPDATE zones SET clock_key = 'aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899' WHERE id = 'z1'`)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	devtest.Exec(t, d, `INSERT INTO display_groups(id, zone_id, name, created_at, updated_at) VALUES ('g1', 'z1', 'Wand', ?, ?)`, now, now)
	id, tok := e.pair("hw-sync", "z1")
	h := hello("hw-sync")
	a := e.connect(tok, &h)
	ms := a.expect(contracts.MsgWelcome, contracts.MsgDeviceConfig)
	cfg := decode[contracts.DeviceConfig](t, ms[1].P)
	if cfg.ClockKey == "" || cfg.Position != 1 || cfg.GroupID != "" {
		t.Fatalf("config after pairing: %+v", cfg)
	}
	if code, out := e.admin.do("PATCH", "/api/v1/devices/"+id, map[string]any{"groupId": "g1", "position": 3}); code != 200 || out["groupName"] != "Wand" {
		t.Fatalf("place: %d %v", code, out)
	}
	m := a.waitFor(contracts.MsgDeviceConfig, 2*time.Second, func(m contracts.LinkMessage) bool {
		return decode[contracts.DeviceConfig](t, m.P).GroupID == "g1"
	})
	if c := decode[contracts.DeviceConfig](t, m.P); c.Position != 3 {
		t.Fatalf("pushed place: %+v", c)
	}
	before := time.Now()
	a.send(contracts.MsgTimePing, contracts.TimePing{Seq: 42, T0: 123})
	pong := decode[contracts.TimePong](t, a.waitFor(contracts.MsgTimePong, 2*time.Second, nil).P)
	if pong.Seq != 42 || pong.T0 != 123 || pong.Server < before.UnixMicro() || pong.Server > time.Now().UnixMicro() {
		t.Fatalf("pong: %+v", pong)
	}
}

// A device whose update to the server version failed or rolled back has a problem of its
// own (with the version and the reason) – "version differs" alone does not say that it
// tried and returned to its old version.
func TestUpdateFailedAlert(t *testing.T) {
	e := newEnv(t)
	devtest.Zone(t, e.db(), "z1", "Bar")
	id, _ := e.pair("hw-upd", "z1")
	devtest.Exec(t, e.db(), `UPDATE devices SET version = '0.0.1' WHERE id = ?`, id)
	run := func(runID, ver, state, errText, at string) {
		devtest.Exec(t, e.db(), `INSERT INTO update_runs(id, kind, version, state, started_at) VALUES (?, 'clients', ?, 'done', ?)`, runID, ver, at)
		devtest.Exec(t, e.db(), `INSERT INTO device_updates(run_id, device_id, state, error, updated_at) VALUES (?, ?, ?, ?, ?)`, runID, id, state, errText, at)
	}
	alert := func() map[string]any {
		t.Helper()
		code, out := e.admin.do("GET", "/api/v1/alerts", nil)
		if code != 200 {
			t.Fatalf("alerts: %d %v", code, out)
		}
		for _, a := range out["alerts"].([]any) {
			if m := a.(map[string]any); m["kind"] == "update_failed" {
				return m
			}
		}
		return nil
	}
	if alert() != nil {
		t.Fatal("no update yet: no problem")
	}
	// An earlier version that failed does not count.
	run("r1", "0.0.2", "failed", `{"code":"download_failed"}`, "2026-10-24T10:00:00.000000000Z")
	if alert() != nil {
		t.Fatal("a failure of another version is not this problem")
	}
	run("r2", version.Version, "rolled_back", `{"code":"crash_loop","message":"crash_loop","params":{"log":"ERROR agent start failed"}}`, "2026-10-24T15:46:18.000000000Z")
	a := alert()
	if a == nil || a["detail"] != "crash_loop" || a["version"] != version.Version || a["severity"] != "warning" || a["deviceId"] != id ||
		a["log"] != "ERROR agent start failed" {
		t.Fatalf("update failed: %v", a)
	}
	if _, view := e.admin.do("GET", "/api/v1/devices/"+id, nil); !strings.Contains(toJSON(view), `"kind":"update_failed"`) {
		t.Fatalf("device view without the problem: %v", view["alerts"])
	}
	// A later attempt that is still running (or done) replaces the failure.
	run("r3", version.Version, "sent", "", "2026-10-24T16:00:00.000000000Z")
	if alert() != nil {
		t.Fatal("a newer attempt replaces the failure")
	}
}
