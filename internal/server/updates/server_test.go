package updates

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opensignage/internal/common/version"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
)

// installEnv is an installed server (release layout + live database) in temp dirs.
type installEnv struct {
	root, dataDir, dbPath, oldDir, cfgFile string
	links                                  fileLinks
	u                                      *Updater
	pkg                                    string
}

func newInstallEnv(t *testing.T, from, to string) *installEnv {
	t.Helper()
	base := t.TempDir()
	e := &installEnv{root: filepath.Join(base, "opt"), dataDir: filepath.Join(base, "data")}
	e.dbPath = filepath.Join(e.dataDir, "db", "opensignage.db")
	e.oldDir = filepath.Join(e.root, "releases", from)
	if err := os.MkdirAll(filepath.Join(e.oldDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(e.oldDir, "bin", "opensignage-server"), []byte("old"), 0o755)
	if err := e.links.Set(filepath.Join(e.root, "current"), e.oldDir); err != nil {
		t.Fatal(err)
	}
	e.cfgFile = filepath.Join(base, "server.env")
	_ = os.WriteFile(e.cfgFile, []byte("OS_LOG_LEVEL=info\n"), 0o644)
	setMarker(t, e.dbPath, "before-update")
	e.pkg = filepath.Join(base, "pkg.tar.gz")
	_ = os.WriteFile(e.pkg, tgz(t, "opensignage-"+to+"-linux-amd64", map[string]string{
		"bin/opensignage-server": "new server", "bin/opensignage-agent": "new agent", "share/fonts/manifest.json": "{}", "VERSION": to}), 0o644)
	e.u = &Updater{DataDir: e.dataDir, DBPath: e.dbPath, ConfigFile: e.cfgFile, Version: from,
		Layout: &Layout{Root: e.root, Links: e.links},
		CheckBinary: func(_ context.Context, bin, want string) error {
			if b, err := os.ReadFile(bin); err != nil || string(b) != "new server" || want != to {
				return errors.New("unexpected binary")
			}
			return nil
		}}
	return e
}

// setMarker writes a marker value into the live database (stands for "the data").
func setMarker(t *testing.T, path, v string) {
	t.Helper()
	d, err := db.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.W.Exec(`INSERT INTO settings(key, value, updated_at) VALUES ('marker', ?, '') ON CONFLICT(key) DO UPDATE SET value = excluded.value`, v); err != nil {
		t.Fatal(err)
	}
}

func marker(t *testing.T, path string) string {
	t.Helper()
	d, err := db.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var v string
	_ = d.R.QueryRow(`SELECT value FROM settings WHERE key = 'marker'`).Scan(&v)
	return v
}

func (e *installEnv) install(t *testing.T, to string) *State {
	t.Helper()
	d, err := db.Open(context.Background(), e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var steps []string
	st, err := e.u.Install(context.Background(), InstallRequest{To: to, Package: e.pkg, RunID: "run-1", StartedBy: "admin",
		Backup: d.Backup, Progress: func(s string) { steps = append(steps, s) }})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if strings.Join(steps, ",") != "backup,extract,check,switch" {
		t.Fatalf("steps %v", steps)
	}
	return st
}

func (e *installEnv) link(t *testing.T, name string) string {
	t.Helper()
	v, err := e.links.Read(filepath.Join(e.root, name))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestServerUpdateSuccess(t *testing.T) {
	e := newInstallEnv(t, "1.0.0", "1.1.0")
	st := e.install(t, "1.1.0")
	newDir := filepath.Join(e.root, "releases", "1.1.0")
	if e.link(t, "current") != newDir || e.link(t, "previous") != e.oldDir {
		t.Fatalf("links: current=%s previous=%s", e.link(t, "current"), e.link(t, "previous"))
	}
	for _, f := range []string{"bin/opensignage-server", "bin/opensignage-agent", "share/fonts/manifest.json", "VERSION"} {
		if _, err := os.Stat(filepath.Join(newDir, f)); err != nil {
			t.Fatalf("release file %s: %v", f, err)
		}
	}
	if st.State != statePending || st.From != "1.0.0" || st.To != "1.1.0" || marker(t, st.Backup) != "before-update" {
		t.Fatalf("state %+v", st)
	}
	if b, err := os.ReadFile(st.ConfigBackup); err != nil || string(b) != "OS_LOG_LEVEL=info\n" {
		t.Fatalf("config backup: %v", err)
	}
	// the new version starts: migrations changed the data, the self-test passes
	setMarker(t, e.dbPath, "migrated")
	if err := Startup(context.Background(), e.dataDir, "1.1.0", e.links, func(context.Context) error { return nil }, quietLog()); err != nil {
		t.Fatalf("startup: %v", err)
	}
	got, _ := LoadState(e.dataDir)
	if got == nil || !got.SelfTestOK || got.Attempts != 1 || got.State != statePending {
		t.Fatalf("state after startup %+v", got)
	}
	if marker(t, e.dbPath) != "migrated" || e.link(t, "current") != newDir {
		t.Fatalf("a successful start must keep data and release")
	}
	// another version starting (e.g. the old one) ignores the pending state
	if err := Startup(context.Background(), e.dataDir, "1.0.0", e.links, func(context.Context) error { return errors.New("x") }, quietLog()); err != nil {
		t.Fatal(err)
	}
}

func TestServerUpdateSelfTestFailureRollsBack(t *testing.T) {
	e := newInstallEnv(t, "1.0.0", "1.1.0")
	prev := filepath.Join(e.root, "releases", "0.9.0")
	_ = os.MkdirAll(prev, 0o755)
	_ = e.links.Set(filepath.Join(e.root, "previous"), prev)
	e.install(t, "1.1.0")
	setMarker(t, e.dbPath, "migrated-by-broken-version")
	_ = os.WriteFile(e.dbPath+"-wal", []byte("stale wal of the new version"), 0o640)

	err := Startup(context.Background(), e.dataDir, "1.1.0", e.links, func(context.Context) error {
		return errors.New("web interface missing")
	}, quietLog())
	if err == nil || !strings.Contains(err.Error(), "web interface missing") {
		t.Fatalf("startup must fail: %v", err)
	}
	if got := marker(t, e.dbPath); got != "before-update" {
		t.Fatalf("database not restored: %q", got)
	}
	if e.link(t, "current") != e.oldDir || e.link(t, "previous") != prev {
		t.Fatalf("links not restored: current=%s previous=%s", e.link(t, "current"), e.link(t, "previous"))
	}
	st, _ := LoadState(e.dataDir)
	if st == nil || st.State != stateRolledBack || !strings.Contains(st.Error, "self-test failed") {
		t.Fatalf("state %+v", st)
	}
	// the restarted old version leaves the recorded result alone (the module finalizes it)
	if err := Startup(context.Background(), e.dataDir, "1.0.0", e.links, func(context.Context) error { return nil }, quietLog()); err != nil {
		t.Fatal(err)
	}
}

func TestServerUpdateTooManyStartsRollBack(t *testing.T) {
	e := newInstallEnv(t, "1.0.0", "1.1.0")
	e.install(t, "1.1.0")
	ok := func(context.Context) error { return nil }
	// the new version passes the self-test but crashes later twice (never finalized)
	for i := 1; i <= MaxStartAttempts; i++ {
		if err := Startup(context.Background(), e.dataDir, "1.1.0", e.links, ok, quietLog()); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	err := Startup(context.Background(), e.dataDir, "1.1.0", e.links, ok, quietLog())
	if err == nil || !strings.Contains(err.Error(), "attempts") {
		t.Fatalf("third start must roll back: %v", err)
	}
	if e.link(t, "current") != e.oldDir || e.link(t, "previous") != "" {
		t.Fatalf("links: current=%s previous=%s", e.link(t, "current"), e.link(t, "previous"))
	}
}

func TestRollbackCommandAfterCrash(t *testing.T) {
	asServiceUser(t)
	e := newInstallEnv(t, "1.0.0", "1.1.0")
	var out bytes.Buffer
	if err := RollbackCommand(e.dataDir, e.root, e.links, &out, quietLog()); !errors.Is(err, ErrNothingToRollBack) {
		t.Fatalf("without update state the command must fail: %v", err)
	}
	e.install(t, "1.1.0")
	setMarker(t, e.dbPath, "half-migrated") // the new version crashed while migrating
	if err := RollbackCommand(e.dataDir, e.root, e.links, &out, quietLog()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if marker(t, e.dbPath) != "before-update" || e.link(t, "current") != e.oldDir {
		t.Fatalf("rollback incomplete")
	}
	if !strings.Contains(out.String(), "rolled back from 1.1.0 to 1.0.0") {
		t.Fatalf("output %q", out.String())
	}
	// systemd may run the unit again before the old version started: one restart is
	// requested (exit 0), after that the command reports nothing to do (exit ≠ 0).
	if err := RollbackCommand(e.dataDir, e.root, e.links, &out, quietLog()); err != nil {
		t.Fatalf("first repeat must restart the restored version: %v", err)
	}
	if err := RollbackCommand(e.dataDir, e.root, e.links, &out, quietLog()); !errors.Is(err, ErrNothingToRollBack) {
		t.Fatalf("further runs must report nothing to do (non-zero exit): %v", err)
	}
	if marker(t, e.dbPath) != "before-update" || e.link(t, "current") != e.oldDir {
		t.Fatalf("repeated runs must not change anything")
	}
}

func TestInstallFailureLeavesEverythingAlone(t *testing.T) {
	e := newInstallEnv(t, "1.0.0", "1.1.0")
	e.u.CheckBinary = func(context.Context, string, string) error { return errors.New("exec format error") }
	d, err := db.Open(context.Background(), e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_, err = e.u.Install(context.Background(), InstallRequest{To: "1.1.0", Package: e.pkg, Backup: d.Backup})
	if err == nil || !strings.Contains(err.Error(), "does not run") {
		t.Fatalf("want binary check error, got %v", err)
	}
	if e.link(t, "current") != e.oldDir || e.link(t, "previous") != "" {
		t.Fatalf("links changed")
	}
	if st, _ := LoadState(e.dataDir); st != nil {
		t.Fatalf("state left behind")
	}
	if _, err := os.Stat(filepath.Join(e.root, "releases", "1.1.0")); !os.IsNotExist(err) {
		t.Fatalf("half installed release left behind")
	}
}

func TestExtractPackageRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	for i, name := range []string{"top/../../evil", "/abs/evil", "../evil", "evil-at-top-level"} {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
		_, _ = tw.Write([]byte("x"))
		_ = tw.Close()
		_ = gz.Close()
		p := filepath.Join(dir, fmt.Sprintf("p%d.tar.gz", i))
		_ = os.WriteFile(p, buf.Bytes(), 0o644)
		if err := extractPackage(p, filepath.Join(dir, "out", fmt.Sprint(i))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "evil")); !os.IsNotExist(err) {
		t.Fatalf("file written outside the target")
	}
}

// TestModuleInstallAndFinish runs the admin flow through the module: install request,
// switch, exit 0; then the "new" process finalizes the run.
func TestModuleInstallAndFinish(t *testing.T) {
	m := newTestModule(t)
	key := newKey(t)
	trust(m, key)
	to := "99.0.0"
	readyRelease(t, m, to, key, map[string][]byte{
		strings.Join(serverPlatformPair(), "/"): tgz(t, "opensignage-"+to, map[string]string{"bin/opensignage-server": "srv"}),
		"linux/arm64":                           []byte("other arch")})
	root := filepath.Join(t.TempDir(), "opt")
	cur := filepath.Join(root, "releases", version.Version)
	_ = os.MkdirAll(cur, 0o755)
	links := fileLinks{}
	_ = links.Set(filepath.Join(root, "current"), cur)
	m.updater.Layout, m.updater.LayoutErr = &Layout{Root: root, Links: links}, nil
	m.updater.CheckBinary = nil
	exited := make(chan int, 1)
	m.exit = func(code int) { exited <- code }

	p := &core.Principal{UserID: "u1", Username: "admin", Role: core.RoleAdmin}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/updates/server/install", strings.NewReader(`{"version":"`+to+`","rolloutClients":false}`))
	if err := m.installHandler(rec, req, p); err != nil || rec.Code != http.StatusAccepted {
		t.Fatalf("install: %v %d %s", err, rec.Code, rec.Body)
	}
	var resp struct{ RunID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit code %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no restart")
	}
	if got, _ := links.Read(filepath.Join(root, "current")); got != filepath.Join(root, "releases", to) {
		t.Fatalf("current → %s", got)
	}
	// a second install while one is pending is refused
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"version":"`+to+`"}`))
	if err := m.installHandler(rec, req, p); err == nil {
		t.Fatalf("second install must be refused")
	}

	// "new process": pretend this binary is the new version that passed its self-test
	st, _ := LoadState(m.app.Cfg.DataDir)
	st.To, st.SelfTestOK, st.Attempts = version.Version, true, 1
	if err := saveState(m.app.Cfg.DataDir, st); err != nil {
		t.Fatal(err)
	}
	m.finishServerUpdate(context.Background())
	run, _ := m.getRun(context.Background(), resp.RunID)
	if run == nil || run.State != runDone || run.FinishedAt == "" {
		t.Fatalf("run %+v", run)
	}
	if st, _ := LoadState(m.app.Cfg.DataDir); st != nil {
		t.Fatalf("state not removed")
	}
	var n int
	_ = m.app.DB.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action IN ('update.server_started', 'update.server_switched', 'update.server_done')`).Scan(&n)
	if n != 3 {
		t.Fatalf("audit entries: %d", n)
	}
}

func TestModuleRecordsRollback(t *testing.T) {
	m := newTestModule(t)
	mustExec(t, m, `INSERT INTO update_runs(id, kind, version, state, started_by, started_at) VALUES ('r1', 'server', '99.0.0', 'running', 'admin', ?)`, db.Now())
	if err := saveState(m.app.Cfg.DataDir, &State{State: stateRolledBack, From: version.Version, To: "99.0.0", RunID: "r1",
		Error: "self-test failed: boom"}); err != nil {
		t.Fatal(err)
	}
	m.finishServerUpdate(context.Background())
	run, _ := m.getRun(context.Background(), "r1")
	if run.State != runRolledBack || !strings.Contains(run.Error, "boom") {
		t.Fatalf("run %+v", run)
	}
	if st, _ := LoadState(m.app.Cfg.DataDir); st != nil {
		t.Fatalf("state not removed")
	}
}

func serverPlatformPair() []string {
	goos, arch := serverPlatform()
	return []string{goos, arch}
}
