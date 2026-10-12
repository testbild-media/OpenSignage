package devices_test

import (
	"testing"
	"time"

	"opensignage/internal/server/core"
	"opensignage/internal/server/devices"
	"opensignage/internal/server/devices/devtest"
)

// TestPairingCapPerSource: one source address holds at most MaxPairingsPerIP open requests
// of different hardware; re-requests of known hardware and claimed requests do not count.
func TestPairingCapPerSource(t *testing.T) {
	e := newEnv(t)
	var reqs []string
	for _, hw := range []string{"hw-1", "hw-2", "hw-3", "hw-4", "hw-5"} {
		reqs = append(reqs, e.requestPairing(hw).PIN)
	}
	code, out := e.devJSON("POST", "/device/v1/pair/request", "", map[string]any{"hardwareId": "hw-6", "hostname": "x"})
	if code != 429 || errCode(out) != "pairing_busy" {
		t.Fatalf("sixth hardware from the same source must be refused: %d %v", code, out)
	}
	// A re-request of known hardware replaces its own request and keeps the PIN.
	if again := e.requestPairing("hw-3"); again.PIN != reqs[2] {
		t.Fatalf("re-request must keep the PIN: %s vs %s", again.PIN, reqs[2])
	}
	var ip string
	if err := e.db().R.QueryRow(`SELECT source_ip FROM pairing_requests WHERE hardware_id = 'hw-3'`).Scan(&ip); err != nil || ip != "127.0.0.1" {
		t.Fatalf("source address stored: %q %v", ip, err)
	}
	// A claimed request is no longer open: the source has room again.
	if code, out := e.claim(reqs[0], "Bar", ""); code != 200 {
		t.Fatalf("claim: %d %v", code, out)
	}
	e.requestPairing("hw-6")
}

// TestPairingGlobalCapEvictsOldest: beyond MaxOpenPairings the oldest open request makes
// room instead of new devices being refused.
func TestPairingGlobalCapEvictsOldest(t *testing.T) {
	e := newEnv(t, func(m *devices.Module) { m.MaxOpenPairings, m.MaxPairingsPerIP = 3, 100 })
	a := e.requestPairing("hw-a")
	b := e.requestPairing("hw-b")
	c := e.requestPairing("hw-c")
	d := e.requestPairing("hw-d") // evicts hw-a
	if _, st := e.pairStatus(a.PairingID, a.Secret); st.State != "expired" {
		t.Fatalf("oldest request must be evicted: %+v", st)
	}
	for _, r := range []struct{ id, secret string }{{b.PairingID, b.Secret}, {c.PairingID, c.Secret}, {d.PairingID, d.Secret}} {
		if _, st := e.pairStatus(r.id, r.secret); st.State != "pending" {
			t.Fatalf("newer requests stay open: %+v", st)
		}
	}
	// Replacing an own request at the cap evicts nobody.
	if again := e.requestPairing("hw-b"); again.PIN != b.PIN {
		t.Fatalf("re-request keeps the PIN: %+v", again)
	}
	if _, st := e.pairStatus(c.PairingID, c.Secret); st.State != "pending" {
		t.Fatalf("re-request must not evict others: %+v", st)
	}
	var open int
	if err := e.db().R.QueryRow(`SELECT COUNT(*) FROM pairing_requests WHERE device_id IS NULL`).Scan(&open); err != nil || open != 3 {
		t.Fatalf("open requests: %d %v", open, err)
	}
}

// TestRebuildRetriedAfterTransientError: a deployment rebuild that fails (here: a table
// that is briefly unavailable) is tried again instead of waiting for the next change.
func TestRebuildRetriedAfterTransientError(t *testing.T) {
	e := newEnv(t, func(m *devices.Module) { m.DeployRetry = 300 * time.Millisecond })
	d := e.db()
	devtest.Exec(t, d, `ALTER TABLE deployment_blobs RENAME TO deployment_blobs_away`)
	c := devtest.SeedContent(t, d, e.srv.App.Cfg.DataDir)
	e.srv.App.Bus.Publish(core.Event{Topic: core.TopicContent, Kind: "test"})
	time.Sleep(150 * time.Millisecond)
	count := func() int {
		var n int
		if err := d.R.QueryRow(`SELECT COUNT(*) FROM deployments WHERE zone_id = ?`, c.Z1).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("the build must have failed while the table was away: %d deployments", n)
	}
	devtest.Exec(t, d, `ALTER TABLE deployment_blobs_away RENAME TO deployment_blobs`)
	deadline := time.Now().Add(5 * time.Second)
	for count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the failed rebuild was never retried")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestLivePushRetriedAfterTransientError: a live push whose state computation fails is
// retried for the same zones.
func TestLivePushRetriedAfterTransientError(t *testing.T) {
	e := newEnv(t, func(m *devices.Module) { m.DeployRetry = 300 * time.Millisecond })
	d := e.db()
	c := devtest.SeedContent(t, d, e.srv.App.Cfg.DataDir)
	sub := e.srv.App.Bus.Subscribe(1024)
	defer sub.Close()
	pushed := func() bool {
		for {
			select {
			case ev := <-sub.C:
				if ev.Topic == core.TopicLive && ev.Kind == "pushed" && ev.ZoneID == c.Z1 {
					return true
				}
			default:
				return false
			}
		}
	}
	devtest.Exec(t, d, `ALTER TABLE zone_product_status RENAME TO zone_product_status_away`)
	e.srv.App.Bus.Publish(core.Event{Topic: core.TopicLive, Kind: "test", ZoneID: c.Z1})
	time.Sleep(150 * time.Millisecond)
	if pushed() {
		t.Fatal("the push must have failed while the table was away")
	}
	devtest.Exec(t, d, `ALTER TABLE zone_product_status_away RENAME TO zone_product_status`)
	deadline := time.Now().Add(5 * time.Second)
	for !pushed() {
		if time.Now().After(deadline) {
			t.Fatal("the failed live push was never retried")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
