package devices_test

import (
	"testing"
	"time"

	"opensignage/internal/contracts"
	"opensignage/internal/server/devices/devtest"
)

// The zone preview lists the real devices of the zone with their group, place, size and
// connection; a display group without device is shown as a place of its own.
func TestZonePreviewDevices(t *testing.T) {
	e := newEnv(t)
	d := e.db()
	devtest.Zone(t, d, "z1", "Bar")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	devtest.Exec(t, d, `INSERT INTO display_groups(id, zone_id, name, sort, created_at, updated_at) VALUES ('g1', 'z1', 'Wand', 1, ?, ?)`, now, now)
	devtest.Exec(t, d, `INSERT INTO display_groups(id, zone_id, name, sort, created_at, updated_at) VALUES ('g2', 'z1', 'Leer', 2, ?, ?)`, now, now)
	wallID, _ := e.pair("hw-wall", "z1")
	barID, barTok := e.pair("hw-bar", "z1")
	if code, out := e.admin.do("PATCH", "/api/v1/devices/"+wallID, map[string]any{"groupId": "g1", "position": 2,
		"config": map[string]any{"display": map[string]any{"width": 1920, "height": 1080, "rotation": 90}}}); code != 200 {
		t.Fatalf("place: %d %v", code, out)
	}
	h := hello("hw-bar")
	a := e.connect(barTok, &h)
	a.expect(contracts.MsgWelcome, contracts.MsgDeviceConfig)

	var screens []any
	eventually(t, 3*time.Second, func() bool {
		code, out := e.admin.do("GET", "/api/v1/zones/z1/preview", nil)
		if code != 200 {
			t.Fatalf("preview: %d %v", code, out)
		}
		screens, _ = out["screens"].([]any)
		return len(screens) == 3 && screens[1].(map[string]any)["online"] == true
	})
	at := func(i int) map[string]any { return screens[i].(map[string]any) }
	wall, bar, empty := at(0), at(1), at(2)
	if wall["deviceId"] != wallID || wall["groupName"] != "Wand" || wall["position"] != float64(2) || wall["online"] != false ||
		wall["width"] != float64(1080) || wall["height"] != float64(1920) {
		t.Fatalf("wall screen (rotated display mode): %v", wall)
	}
	if bar["deviceId"] != barID || bar["groupId"] != nil || bar["position"] != float64(2) || bar["scaleMode"] != "contain" {
		t.Fatalf("bar screen: %v", bar)
	}
	if empty["deviceId"] != nil || empty["groupName"] != "Leer" || empty["kind"] != "cleared" {
		t.Fatalf("group without device: %v", empty)
	}
}
