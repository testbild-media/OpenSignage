package updates

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"opensignage/internal/common/release"
	"opensignage/internal/common/version"
	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/devices"
)

// fakeLink stands in for the devices module.
type fakeLink struct {
	mu     sync.Mutex
	online map[string]bool
	offers []string
	last   map[string]contracts.UpdateOffer
}

func newFakeLink() *fakeLink {
	return &fakeLink{online: map[string]bool{}, last: map[string]contracts.UpdateOffer{}}
}

func (f *fakeLink) SendUpdateOffer(id string, o contracts.UpdateOffer) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.online[id] {
		return false
	}
	f.offers = append(f.offers, id)
	f.last[id] = o
	return true
}

func (f *fakeLink) Online(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online[id]
}

func (f *fakeLink) taken() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.offers
	f.offers = nil
	return out
}

type rolloutEnv struct {
	m    *Module
	link *fakeLink
}

// newRolloutEnv seeds zones Z1 (d1, d2), Z2 (d3) and zone-less devices d4, d5, all
// paired with an old version and linux/amd64, plus the client packages of the server
// version.
func newRolloutEnv(t *testing.T) *rolloutEnv {
	t.Helper()
	m := newTestModule(t)
	link := newFakeLink()
	m.link = link
	key := newKey(t)
	trust(m, key)
	readyRelease(t, m, version.Version, key, map[string][]byte{"linux/amd64": []byte("amd64 package"), "linux/arm64": []byte("arm64 package")})
	now := db.Now()
	for _, z := range []string{"z1", "z2"} {
		mustExec(t, m, `INSERT INTO zones(id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`, z, strings.ToUpper(z), now, now)
	}
	for _, d := range []struct{ id, zone string }{{"d1", "z1"}, {"d2", "z1"}, {"d3", "z2"}, {"d4", ""}, {"d5", ""}} {
		mustExec(t, m, `INSERT INTO devices(id, hardware_id, name, zone_id, token_hash, config, version, created_at, updated_at)
			VALUES (?, ?, ?, NULLIF(?, ''), 'hash', '{}', '0.9.0', ?, ?)`, d.id, "hw-"+d.id, "Screen "+d.id, d.zone, now, now)
		if err := m.storePlatform(context.Background(), d.id, "linux", "amd64", false); err != nil {
			t.Fatal(err)
		}
		link.online[d.id] = true
	}
	return &rolloutEnv{m: m, link: link}
}

// assertOnePerZone checks the core invariant: never two updating devices in one zone.
func (e *rolloutEnv) assertOnePerZone(t *testing.T) {
	t.Helper()
	rows, err := e.m.app.DB.R.Query(`SELECT COALESCE(d.zone_id, ''), COUNT(*) FROM device_updates du JOIN devices d ON d.id = du.device_id
		WHERE du.state IN ('sent', 'downloading', 'installing', 'installed') GROUP BY COALESCE(d.zone_id, '')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var zone string
		var n int
		_ = rows.Scan(&zone, &n)
		if n > 1 {
			t.Fatalf("zone %q has %d devices updating at once", zone, n)
		}
	}
}

func (e *rolloutEnv) state(t *testing.T, run, dev string) string {
	t.Helper()
	var s string
	if err := e.m.app.DB.R.QueryRow(`SELECT state FROM device_updates WHERE run_id = ? AND device_id = ?`, run, dev).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *rolloutEnv) comeBack(t *testing.T, dev string) {
	t.Helper()
	mustExec(t, e.m, `UPDATE devices SET version = ? WHERE id = ?`, version.Version, dev)
}

func TestRolloutOnePerZoneAndStopOnFailure(t *testing.T) {
	e := newRolloutEnv(t)
	ctx := context.Background()
	p := &core.Principal{UserID: "u1", Username: "admin", Role: core.RoleAdmin}
	run, err := e.m.startRollout(ctx, p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.startRollout(ctx, p, "", ""); !isCode(err, "rollout_running") {
		t.Fatalf("second rollout: %v", err)
	}
	e.m.rolloutTick(ctx)
	e.assertOnePerZone(t)
	if got := strings.Join(e.link.taken(), ","); got != "d4,d1,d3" {
		t.Fatalf("first wave %q (one per zone, zone-less counts as one group)", got)
	}
	o := e.link.last["d1"]
	if o.Version != version.Version || o.SHA256 == "" || o.Size != int64(len("amd64 package")) ||
		!strings.HasPrefix(o.URL, "/device/v1/update/package?") || !strings.Contains(o.URL, "arch=amd64") {
		t.Fatalf("offer %+v", o)
	}
	e.m.rolloutTick(ctx)
	if got := e.link.taken(); len(got) != 0 {
		t.Fatalf("nothing may be offered while the zones are busy: %v", got)
	}
	// d1 reports progress, then comes back with the new version → d2 is next
	e.m.UpdateProgress("d1", contracts.UpdateProgress{Version: version.Version, State: "installing"})
	if s := e.state(t, run, "d1"); s != duInstalling {
		t.Fatalf("d1 %s", s)
	}
	e.comeBack(t, "d1")
	e.m.rolloutTick(ctx)
	e.assertOnePerZone(t)
	if s := e.state(t, run, "d1"); s != duDone {
		t.Fatalf("d1 %s", s)
	}
	if got := strings.Join(e.link.taken(), ","); got != "d2" {
		t.Fatalf("second wave %q", got)
	}
	// d4 (zone-less) fails → the rollout stops, d5 is never offered
	e.m.UpdateProgress("d4", contracts.UpdateProgress{Version: version.Version, State: "rolled_back", Error: "health check failed"})
	if r, _ := e.m.getRun(ctx, run); r.State != runFailed || !strings.Contains(r.Error, "Screen d4") {
		t.Fatalf("run %+v", r)
	}
	if s := e.state(t, run, "d5"); s != duCancelled {
		t.Fatalf("d5 %s", s)
	}
	e.m.rolloutTick(ctx)
	if got := e.link.taken(); len(got) != 0 {
		t.Fatalf("a failed rollout must not offer anything: %v", got)
	}
	// devices that were already updating still finish
	e.comeBack(t, "d2")
	e.m.rolloutTick(ctx)
	if s := e.state(t, run, "d2"); s != duDone {
		t.Fatalf("d2 %s", s)
	}
}

func TestRolloutTimeoutStopsAndOfflineIsSkipped(t *testing.T) {
	e := newRolloutEnv(t)
	ctx := context.Background()
	e.link.online["d3"] = false // offline during the rollout: skipped, not a failure
	run, err := e.m.startRollout(ctx, nil, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	e.m.rolloutTick(ctx)
	if s := e.state(t, run, "d3"); s != duOffline {
		t.Fatalf("d3 %s", s)
	}
	if r, _ := e.m.getRun(ctx, run); r.State != runRunning {
		t.Fatalf("an offline device must not stop the rollout: %+v", r)
	}
	// d1 never comes back
	mustExec(t, e.m, `UPDATE device_updates SET sent_at = ? WHERE device_id = 'd1'`, time.Now().Add(-11*time.Minute).UTC().Format(time.RFC3339Nano))
	e.m.rolloutTick(ctx)
	if s := e.state(t, run, "d1"); s != duFailed {
		t.Fatalf("d1 %s", s)
	}
	r, _ := e.m.getRun(ctx, run)
	if r.State != runFailed || !strings.Contains(r.Error, codeTimeout) {
		t.Fatalf("run %+v", r)
	}
	if s := e.state(t, run, "d2"); s != duCancelled {
		t.Fatalf("d2 %s", s)
	}
}

func TestRolloutCompletesAndStop(t *testing.T) {
	e := newRolloutEnv(t)
	ctx := context.Background()
	run, _ := e.m.startRollout(ctx, nil, "", "test")
	for i := 0; i < 5; i++ {
		e.m.rolloutTick(ctx)
		e.assertOnePerZone(t)
		for _, d := range e.link.taken() {
			e.comeBack(t, d)
		}
	}
	e.m.rolloutTick(ctx)
	if r, _ := e.m.getRun(ctx, run); r.State != runDone {
		t.Fatalf("run %+v", r)
	}
	if _, err := e.m.startRollout(ctx, nil, "", "test"); !isCode(err, "nothing_to_update") {
		t.Fatalf("all aligned: %v", err)
	}
	// stop: waiting devices are cancelled
	mustExec(t, e.m, `UPDATE devices SET version = '0.9.0'`)
	run2, _ := e.m.startRollout(ctx, nil, "", "test")
	e.m.endRollout(ctx, run2, runStopped, "", nil, "")
	e.m.rolloutTick(ctx)
	if got := e.link.taken(); len(got) != 0 {
		t.Fatalf("stopped rollout offered %v", got)
	}
	if s := e.state(t, run2, "d5"); s != duCancelled {
		t.Fatalf("d5 %s", s)
	}
}

func TestAlignAndAutoAlign(t *testing.T) {
	e := newRolloutEnv(t)
	ctx := context.Background()
	p := &core.Principal{UserID: "u1", Username: "admin", Role: core.RoleAdmin}
	// unknown platform → clear error
	mustExec(t, e.m, `DELETE FROM device_platforms WHERE device_id = 'd5'`)
	if _, err := e.m.align(ctx, "d5", p, "", "", true); !isCode(err, "device_platform_unknown") {
		t.Fatalf("d5: %v", err)
	}
	e.link.online["d4"] = false
	if _, err := e.m.align(ctx, "d4", p, "", "", true); !isCode(err, "device_offline") {
		t.Fatalf("d4: %v", err)
	}
	run, err := e.m.align(ctx, "d1", p, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	// a second device joins the same run (same zone → waits)
	if run2, err := e.m.align(ctx, "d2", p, "", "", true); err != nil || run2 != run {
		t.Fatalf("join: %v %s %s", err, run, run2)
	}
	e.m.rolloutTick(ctx)
	e.assertOnePerZone(t)
	if got := strings.Join(e.link.taken(), ","); got != "d1" {
		t.Fatalf("offers %q", got)
	}
	e.comeBack(t, "d1")
	if _, err := e.m.align(ctx, "d1", p, "", "", true); !isCode(err, "already_aligned") {
		t.Fatalf("aligned device: %v", err)
	}

	// claim → align on connect, even without the auto-align setting
	if err := e.m.storePlatform(ctx, "d3", "linux", "amd64", true); err != nil {
		t.Fatal(err)
	}
	e.m.onOnline(ctx, "d3")
	var n int
	_ = e.m.app.DB.R.QueryRow(`SELECT COUNT(*) FROM device_updates WHERE device_id = 'd3' AND run_id = ?`, run).Scan(&n)
	if n != 1 {
		t.Fatalf("claimed device not queued")
	}
	// auto-align is off: a mismatching device that connects is left alone
	e.m.onOnline(ctx, "d4")
	_ = e.m.app.DB.R.QueryRow(`SELECT COUNT(*) FROM device_updates WHERE device_id = 'd4'`).Scan(&n)
	if n != 0 {
		t.Fatalf("device aligned without consent")
	}
	// with the setting it is aligned, but never again automatically after a failure
	s := e.m.app.Settings.Get()
	s.AutoAlignVersions = true
	e.m.app.Settings.Apply(s)
	e.link.online["d4"] = true
	e.m.onOnline(ctx, "d4")
	_ = e.m.app.DB.R.QueryRow(`SELECT COUNT(*) FROM device_updates WHERE device_id = 'd4'`).Scan(&n)
	if n != 1 {
		t.Fatalf("auto-align did not queue d4")
	}
	mustExec(t, e.m, `UPDATE device_updates SET state = 'failed' WHERE device_id = 'd4'`)
	mustExec(t, e.m, `UPDATE update_runs SET state = 'failed'`)
	e.m.onOnline(ctx, "d4")
	_ = e.m.app.DB.R.QueryRow(`SELECT COUNT(*) FROM device_updates WHERE device_id = 'd4'`).Scan(&n)
	if n != 1 {
		t.Fatalf("a failed device must not be retried automatically")
	}
}

func TestDevicePackageProvider(t *testing.T) {
	e := newRolloutEnv(t)
	ctx := context.Background()
	pkg, err := e.m.DevicePackage(ctx, version.Version, "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 64)
	n, _ := pkg.Content.Read(b)
	pkg.Content.Close()
	if string(b[:n]) != "amd64 package" || !strings.HasSuffix(pkg.Name, "linux-amd64.tar.gz") {
		t.Fatalf("package %q %s", b[:n], pkg.Name)
	}
	for _, c := range [][3]string{{version.Version, "linux", "armv7"}, {"1.2.3", "linux", "amd64"}, {"../x", "linux", "amd64"}} {
		if _, err := e.m.DevicePackage(ctx, c[0], c[1], c[2]); err != devices.ErrNoPackage {
			t.Errorf("%v: %v", c, err)
		}
	}
	if pkg, err := e.m.DevicePackage(ctx, version.Version, "linux", "x86_64"); err != nil {
		t.Errorf("x86_64 must map to amd64: %v", err)
	} else {
		pkg.Content.Close()
	}
	if _, err := e.m.DevicePackage(ctx, version.Version, "linux", "arm"); err != devices.ErrNoPackage {
		t.Errorf("GOARCH arm maps to armv7 (not in this release): %v", err)
	}
}

// Devices get the signed metadata byte for byte, so they can verify it with their keys.
func TestDeviceReleaseMetadata(t *testing.T) {
	e := newRolloutEnv(t)
	ctx := context.Background()
	rel, err := e.m.DeviceRelease(ctx, version.Version)
	if err != nil {
		t.Fatal(err)
	}
	dir := e.m.releaseDir(version.Version)
	for name, got := range map[string][]byte{release.InfoFile: rel.Info, release.SumsFile: rel.Sums, release.SigFile: rel.Sig} {
		if want, _ := os.ReadFile(filepath.Join(dir, name)); len(got) == 0 || string(got) != string(want) {
			t.Errorf("%s differs from the release file", name)
		}
	}
	if err := release.VerifySignature(rel.Sums, string(rel.Sig), e.m.trustedKeys()); err != nil {
		t.Fatalf("signature: %v", err)
	}
	for _, v := range []string{"1.2.3", "../x", version.Version + "+x", "v" + version.Version} {
		if _, err := e.m.DeviceRelease(ctx, v); err != devices.ErrNoPackage {
			t.Errorf("%q: %v", v, err)
		}
	}
	mustExec(t, e.m, `UPDATE releases SET state = 'failed'`)
	if _, err := e.m.DeviceRelease(ctx, version.Version); err != devices.ErrNoPackage {
		t.Errorf("a release that is not ready must not be served: %v", err)
	}
}

// After a server update the screens are still reconnecting: the requested client rollout
// waits for the screens seen before the update instead of skipping them as offline.
func TestRolloutAfterServerUpdateWaitsForReconnect(t *testing.T) {
	e := newRolloutEnv(t)
	ctx := context.Background()
	since := time.Now()
	mustExec(t, e.m, `UPDATE devices SET last_seen_at = ?`, since.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"))
	mustExec(t, e.m, `UPDATE devices SET last_seen_at = '2020-01-01T00:00:00.000000000Z' WHERE id = 'd5'`) // long gone: not awaited
	e.link.mu.Lock()
	for id := range e.link.online {
		e.link.online[id] = false
	}
	e.link.mu.Unlock()
	e.m.ReconnectGrace = 30 * time.Second

	done := make(chan struct{})
	go func() {
		e.m.rolloutAfterServerUpdate(ctx, "test", since)
		close(done)
	}()
	time.Sleep(1500 * time.Millisecond)
	if run, err := e.m.activeClientRun(ctx); err != nil || run != nil {
		t.Fatalf("rollout started while screens reconnect: %+v %v", run, err)
	}
	e.link.mu.Lock()
	for _, id := range []string{"d1", "d2", "d3", "d4"} {
		e.link.online[id] = true
	}
	e.link.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("rollout did not start after the screens reconnected")
	}
	run, err := e.m.activeClientRun(ctx)
	if err != nil || run == nil {
		t.Fatalf("no rollout: %+v %v", run, err)
	}
	if s := e.state(t, run.ID, "d1"); s == duOffline {
		t.Fatalf("d1 skipped as offline")
	}
}
