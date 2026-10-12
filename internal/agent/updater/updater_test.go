package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"opensignage/internal/agent/wire"
	"opensignage/internal/common/release"
)

// makePackage builds a release tar.gz like scripts/build.sh + pack.py do.
func makePackage(t *testing.T, ver string, extra map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	top := "opensignage-" + ver + "-linux-amd64/"
	files := map[string]string{
		"bin/opensignage-agent":  "#!/bin/sh\necho opensignage-agent " + ver + "\n",
		"bin/opensignage-server": "server",
		"share/fonts/a.woff2":    strings.Repeat("F", 70000), // makes the package big enough to interrupt
		"VERSION":                ver + "\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	tw.WriteHeader(&tar.Header{Name: top, Typeflag: tar.TypeDir, Mode: 0o755})
	for _, d := range []string{"bin/", "share/", "share/fonts/"} {
		tw.WriteHeader(&tar.Header{Name: top + d, Typeflag: tar.TypeDir, Mode: 0o755})
	}
	for name, content := range files {
		mode := int64(0o644)
		if strings.HasPrefix(name, "bin/") {
			mode = 0o755
		}
		tw.WriteHeader(&tar.Header{Name: top + name, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(content))})
		tw.Write([]byte(content))
	}
	tw.Close()
	gz.Close()
	// Incompressible tail so the archive is large enough for an interrupted transfer.
	data := buf.Bytes()
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])
}

// install creates installDir/releases/<v> for each version and points current at cur.
func install(t *testing.T, cur string, versions ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, v := range versions {
		bin := filepath.Join(dir, "releases", v, "bin")
		os.MkdirAll(bin, 0o755)
		os.WriteFile(filepath.Join(bin, "opensignage-agent"), []byte("old "+v), 0o755)
	}
	if err := SetLink(dir, "current", "releases/"+cur); err != nil {
		t.Fatal(err)
	}
	return dir
}

type harness struct {
	mu       sync.Mutex
	reports  []wire.UpdateProgress
	exits    []string
	updating []bool
}

func (h *harness) report(p wire.UpdateProgress) bool {
	h.mu.Lock()
	h.reports = append(h.reports, p)
	h.mu.Unlock()
	return true
}

func (h *harness) last() wire.UpdateProgress {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.reports) == 0 {
		return wire.UpdateProgress{}
	}
	return h.reports[len(h.reports)-1]
}

func (h *harness) states() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var s []string
	for _, r := range h.reports {
		if len(s) == 0 || s[len(s)-1] != r.State {
			s = append(s, r.State)
		}
	}
	return strings.Join(s, ",")
}

func (h *harness) exited() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.exits)
}

// testKey signs the test releases; newUpdater trusts it.
var testKey = func() ed25519.PrivateKey {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return priv
}()

func pubOf(k ed25519.PrivateKey) string {
	return release.FormatPublicKey(k.Public().(ed25519.PublicKey))
}

// signedRelease builds the release metadata of ver with one linux/amd64 package (pkg),
// signed with key, as the server serves it (GET /device/v1/update/release).
func signedRelease(ver string, pkg []byte, key ed25519.PrivateKey) *wire.UpdateRelease {
	sum := sha256.Sum256(pkg)
	name := "opensignage-" + ver + "-linux-amd64.tar.gz"
	info, _ := json.Marshal(release.Info{Version: ver, Date: "2026-10-11",
		Packages: []release.Package{{OS: "linux", Arch: "amd64", File: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(pkg))}}})
	infoSum := sha256.Sum256(info)
	sums := release.FormatSums(map[string]string{name: hex.EncodeToString(sum[:]), release.InfoFile: hex.EncodeToString(infoSum[:])})
	return &wire.UpdateRelease{Version: ver, Info: info, Sums: sums, Sig: []byte(release.Sign(key, sums))}
}

func newUpdater(h *harness, dataDir, installDir, ver string, get Getter) *Updater {
	return New(Options{
		DataDir: dataDir, InstallDir: installDir, Version: ver, OS: "linux", Arch: "amd64", Server: get,
		TrustedKeys: []string{pubOf(testKey)},
		FreeSpace:   func(string) (uint64, error) { return 1 << 40, nil },
		CheckBinary: func(path, want string) error {
			b, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(b), "opensignage-agent "+want) {
				return fmt.Errorf("bad binary %q", b)
			}
			return nil
		},
		Report:      h.report,
		SetUpdating: func(b bool) { h.mu.Lock(); h.updating = append(h.updating, b); h.mu.Unlock() },
		Exit:        func(r string) { h.mu.Lock(); h.exits = append(h.exits, r); h.mu.Unlock() },
	})
}

// packageServer serves the signed release metadata (nil: 404 like a server without it)
// and the update package with Range support.
type packageServer struct {
	srv    *httptest.Server
	data   []byte
	meta   *wire.UpdateRelease
	ranges []string     // Range header of every package request
	cut    atomic.Int32 // number of requests to interrupt
	mu     sync.Mutex
}

func newPackageServer(t *testing.T, data []byte, meta *wire.UpdateRelease) *packageServer {
	p := &packageServer{data: data, meta: meta}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/device/v1/update/release" && p.meta != nil {
			_ = json.NewEncoder(w).Encode(p.meta)
			return
		}
		if r.URL.Path != "/device/v1/update/package" || r.URL.Query().Get("arch") != "amd64" {
			http.NotFound(w, r)
			return
		}
		p.mu.Lock()
		p.ranges = append(p.ranges, r.Header.Get("Range"))
		p.mu.Unlock()
		if p.cut.Add(-1) >= 0 {
			w.Header().Set("Content-Length", fmt.Sprint(len(p.data)))
			w.WriteHeader(http.StatusOK)
			w.Write(p.data[:len(p.data)/2])
			return // connection ends early
		}
		http.ServeContent(w, r, "pkg.tar.gz", time.Time{}, bytes.NewReader(p.data))
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *packageServer) packageRequests() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.ranges)
}

func (p *packageServer) get(ctx context.Context, target string, hdr http.Header) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.srv.URL+target, nil)
	for k, v := range hdr {
		req.Header[k] = v
	}
	return http.DefaultClient.Do(req)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestOfferResumeAndSwitch(t *testing.T) {
	data, sum := makePackage(t, "1.1.0", nil)
	ps := newPackageServer(t, data, signedRelease("1.1.0", data, testKey))
	ps.cut.Store(1)
	h := &harness{}
	dataDir := t.TempDir()
	inst := install(t, "1.0.0", "1.0.0", "0.9.0")
	SetLink(inst, "previous", "releases/0.9.0")
	u := newUpdater(h, dataDir, inst, "1.0.0", ps.get)
	offer := wire.UpdateOffer{Version: "1.1.0", SHA256: sum, Size: int64(len(data))}

	u.Offer(offer)
	waitFor(t, "download failure", func() bool { return h.last().State == PhaseFailed })
	if h.last().Error != ErrDownload {
		t.Fatalf("expected download_failed, got %+v", h.last())
	}
	if fi, err := os.Stat(filepath.Join(dataDir, "updates", "1.1.0.part")); err != nil || fi.Size() != int64(len(data)/2) {
		t.Fatalf("partial download must be kept: %v", err)
	}
	if cur, _ := ReadLink(inst, "current"); cur != "releases/1.0.0" {
		t.Fatalf("current changed after a failed download: %s", cur)
	}

	u.Offer(offer) // a network error may be retried at once and resumes
	waitFor(t, "exit for restart", func() bool { return h.exited() == 1 })
	ps.mu.Lock()
	lastRange := ps.ranges[len(ps.ranges)-1]
	ps.mu.Unlock()
	if lastRange != fmt.Sprintf("bytes=%d-", len(data)/2) {
		t.Fatalf("download did not resume: %q", lastRange)
	}
	if !strings.Contains(h.states(), "downloading,verifying,installing,restarting") {
		t.Fatalf("progress states: %s", h.states())
	}
	if cur, _ := ReadLink(inst, "current"); cur != "releases/1.1.0" {
		t.Fatalf("current = %s", cur)
	}
	if prev, _ := ReadLink(inst, "previous"); prev != "releases/1.0.0" {
		t.Fatalf("previous = %s", prev)
	}
	st, err := LoadState(dataDir)
	if err != nil || st == nil || st.From != "1.0.0" || st.To != "1.1.0" || st.Attempts != 0 || st.Phase != "" {
		t.Fatalf("state %+v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(inst, "releases", "1.1.0", "bin", "opensignage-server")); !os.IsNotExist(err) {
		t.Fatal("the server binary must not be installed on clients")
	}
	if b, _ := os.ReadFile(filepath.Join(inst, "releases", "1.1.0", "share", "fonts", "a.woff2")); len(b) != 70000 {
		t.Fatal("fonts not extracted")
	}
	for _, p := range []string{"1.1.0.part", "1.1.0.tar.gz"} {
		if _, err := os.Stat(filepath.Join(dataDir, "updates", p)); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed", p)
		}
	}
	if !slices.Contains(h.updating, true) {
		t.Fatal("player must show the updating screen")
	}
}

func TestChecksumMismatchNoSwitch(t *testing.T) {
	data, sum := makePackage(t, "1.1.0", nil)
	// The server delivers other bytes (same size) than the release key signed.
	evil := bytes.Repeat([]byte("E"), len(data))
	ps := newPackageServer(t, evil, signedRelease("1.1.0", data, testKey))
	h := &harness{}
	dataDir := t.TempDir()
	inst := install(t, "1.0.0", "1.0.0")
	u := newUpdater(h, dataDir, inst, "1.0.0", ps.get)
	u.Offer(wire.UpdateOffer{Version: "1.1.0", SHA256: sum, Size: int64(len(data))})
	waitFor(t, "failure", func() bool { return h.last().State == PhaseFailed })
	if h.last().Error != ErrChecksum {
		t.Fatalf("got %+v", h.last())
	}
	if cur, _ := ReadLink(inst, "current"); cur != "releases/1.0.0" {
		t.Fatalf("current changed: %s", cur)
	}
	if _, err := os.Stat(filepath.Join(inst, "releases", "1.1.0")); !os.IsNotExist(err) {
		t.Fatal("no release dir for an unverified package")
	}
	if st, _ := LoadState(dataDir); st != nil {
		t.Fatal("no state file for a failed update")
	}
	if h.exited() != 0 {
		t.Fatal("must not restart")
	}
	// The same broken version is not retried automatically right away.
	u.Offer(wire.UpdateOffer{Version: "1.1.0", SHA256: sum, Size: int64(len(data))})
	if h.last().Error != ErrRecentlyFailed {
		t.Fatalf("expected recently_failed, got %+v", h.last())
	}
}

// A server offer is installed only with release metadata signed by a key the agent
// trusts itself, for exactly the offered version, and only when the offer matches the
// signed checksum and size. Otherwise nothing is downloaded or switched.
func TestServerOfferNeedsSignedRelease(t *testing.T) {
	data, sum := makePackage(t, "1.1.0", nil)
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	offer := wire.UpdateOffer{Version: "1.1.0", SHA256: sum, Size: int64(len(data))}
	unsigned := *signedRelease("1.1.0", data, testKey)
	unsigned.Sig = nil
	// release.json swapped for one naming the attacker's package: not covered by the
	// signed SHA256SUMS.
	swapped := *signedRelease("1.1.0", data, testKey)
	swapped.Info = signedRelease("1.1.0", []byte("evil package"), testKey).Info
	cases := []struct {
		name  string
		meta  *wire.UpdateRelease
		offer wire.UpdateOffer
		want  string
	}{
		{"untrusted key", signedRelease("1.1.0", data, otherKey), offer, ErrSignature},
		{"missing signature", &unsigned, offer, ErrSignature},
		{"no signed metadata (older server)", nil, offer, ErrSignature},
		{"release.json not signed", &swapped, offer, ErrSignature},
		{"offer checksum differs", signedRelease("1.1.0", data, testKey),
			wire.UpdateOffer{Version: "1.1.0", SHA256: strings.Repeat("0", 64), Size: int64(len(data))}, ErrSignature},
		{"offer size differs", signedRelease("1.1.0", data, testKey),
			wire.UpdateOffer{Version: "1.1.0", SHA256: sum, Size: int64(len(data)) + 1}, ErrSignature},
		{"signed release of another version", signedRelease("1.2.0", data, testKey), offer, ErrPackageInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := newPackageServer(t, data, c.meta)
			h := &harness{}
			inst := install(t, "1.0.0", "1.0.0")
			u := newUpdater(h, t.TempDir(), inst, "1.0.0", ps.get)
			u.Offer(c.offer)
			waitFor(t, "refusal", func() bool { return h.last().State == PhaseFailed })
			if p := h.last(); p.Error != c.want || p.Version != "1.1.0" {
				t.Fatalf("want %s, got %+v", c.want, p)
			}
			if n := ps.packageRequests(); n != 0 {
				t.Fatalf("the package must not be downloaded without a valid signature (%d requests)", n)
			}
			if cur, _ := ReadLink(inst, "current"); cur != "releases/1.0.0" {
				t.Fatalf("current changed: %s", cur)
			}
			if h.exited() != 0 {
				t.Fatal("must not restart")
			}
		})
	}

	// A key from the device's own configuration (OS_TRUSTED_KEYS) extends the trust.
	ps := newPackageServer(t, data, signedRelease("1.1.0", data, otherKey))
	h := &harness{}
	inst := install(t, "1.0.0", "1.0.0")
	u := newUpdater(h, t.TempDir(), inst, "1.0.0", ps.get)
	u.o.TrustedKeys = append(u.o.TrustedKeys, pubOf(otherKey))
	u.Offer(offer)
	waitFor(t, "switch", func() bool { return h.exited() == 1 || h.last().State == PhaseFailed })
	if cur, _ := ReadLink(inst, "current"); cur != "releases/1.1.0" {
		t.Fatalf("release signed by a configured key must install: %+v", h.last())
	}
}

func TestInvalidVersionsRefused(t *testing.T) {
	h := &harness{}
	inst := install(t, "1.0.0", "1.0.0")
	u := newUpdater(h, t.TempDir(), inst, "1.0.0", nil)
	for _, v := range []string{"../1.0.0", "1.1.0+../../x", "v1.1.0", " 1.1.0", "1.1.0/x"} {
		u.Offer(wire.UpdateOffer{Version: v, SHA256: strings.Repeat("a", 64), Size: 10})
		if p := h.last(); p.State != PhaseFailed || p.Error != ErrInvalidOffer {
			t.Errorf("%q: %+v", v, p)
		}
	}
	u.o.Internet = func(context.Context, string, http.Header) (*http.Response, error) { return nil, fmt.Errorf("offline") }
	if err := u.GitHubAvailable("1.1.0+a/b", "owner/repo"); err == nil {
		t.Error("GitHub fallback for an invalid version")
	}
	// A manipulated state file never makes the rollback leave the release directory.
	dataDir := t.TempDir()
	os.MkdirAll(filepath.Join(dataDir, "updates"), 0o750)
	saveState(dataDir, &State{From: "../../etc", To: "1.1.0"})
	if _, err := Rollback(dataDir, inst); err == nil {
		t.Error("rollback to an invalid version must fail")
	}
}

func TestBrokenBinaryNoSwitch(t *testing.T) {
	data, sum := makePackage(t, "1.1.0", map[string]string{"bin/opensignage-agent": "garbage"})
	ps := newPackageServer(t, data, signedRelease("1.1.0", data, testKey))
	h := &harness{}
	inst := install(t, "1.0.0", "1.0.0")
	u := newUpdater(h, t.TempDir(), inst, "1.0.0", ps.get)
	u.Offer(wire.UpdateOffer{Version: "1.1.0", SHA256: sum, Size: int64(len(data))})
	waitFor(t, "failure", func() bool { return h.last().State == PhaseFailed })
	if h.last().Error != ErrPackageInvalid {
		t.Fatalf("got %+v", h.last())
	}
	if cur, _ := ReadLink(inst, "current"); cur != "releases/1.0.0" {
		t.Fatalf("current changed: %s", cur)
	}
	if h.updating[len(h.updating)-1] {
		t.Fatal("updating screen must end after a failure")
	}
}

// switched simulates the state right after an update A → B.
func switched(t *testing.T, attempts int) (dataDir, inst string) {
	dataDir = t.TempDir()
	inst = install(t, "2.0.0", "1.0.0", "2.0.0", "0.5.0")
	SetLink(inst, "previous", "releases/1.0.0")
	os.MkdirAll(filepath.Join(dataDir, "updates"), 0o750)
	saveState(dataDir, &State{From: "1.0.0", To: "2.0.0", Attempts: attempts, Source: "server", StartedAt: time.Now().UTC().Format(time.RFC3339)})
	return dataDir, inst
}

func TestHealthyNewVersionReportsDone(t *testing.T) {
	dataDir, inst := switched(t, 0)
	h := &harness{}
	u := newUpdater(h, dataDir, inst, "2.0.0", nil)
	if u.Startup() {
		t.Fatal("no rollback expected")
	}
	if st, _ := LoadState(dataDir); st.Attempts != 1 {
		t.Fatalf("attempts not counted: %+v", st)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go u.Run(ctx)
	u.MarkConnected()
	if h.last().State == PhaseDone {
		t.Fatal("not healthy without a player ping")
	}
	u.MarkPing()
	if p := h.last(); p.State != PhaseDone || p.Version != "2.0.0" {
		t.Fatalf("expected done, got %+v", p)
	}
	if st, _ := LoadState(dataDir); st != nil {
		t.Fatal("state must be removed after success")
	}
	// Old releases are cleaned up, current and previous stay.
	for v, keep := range map[string]bool{"1.0.0": true, "2.0.0": true, "0.5.0": false} {
		_, err := os.Stat(filepath.Join(inst, "releases", v))
		if keep != (err == nil) {
			t.Errorf("release %s kept=%v", v, err == nil)
		}
	}
}

func TestHealthTimeoutRollsBack(t *testing.T) {
	dataDir, inst := switched(t, 0)
	h := &harness{}
	u := newUpdater(h, dataDir, inst, "2.0.0", nil)
	u.o.HealthTimeout = 100 * time.Millisecond
	u.Startup()
	u.MarkConnected() // connected, but the player never pings
	done := make(chan struct{})
	go func() { u.Run(context.Background()); close(done) }()
	<-done
	if h.exited() != 1 {
		t.Fatal("rollback must exit for a restart")
	}
	if cur, _ := ReadLink(inst, "current"); cur != "releases/1.0.0" {
		t.Fatalf("current = %s", cur)
	}
	st, _ := LoadState(dataDir)
	if st == nil || st.Phase != PhaseRolledBack || st.Error != ErrHealthTimeout {
		t.Fatalf("state %+v", st)
	}
	// The old version reports the rollback on its next connection.
	h2 := &harness{}
	old := newUpdater(h2, dataDir, inst, "1.0.0", nil)
	old.Startup()
	if len(h2.reports) != 0 {
		t.Fatal("report only when connected")
	}
	old.MarkConnected()
	if p := h2.last(); p.State != PhaseRolledBack || p.Version != "2.0.0" || p.Error != ErrHealthTimeout {
		t.Fatalf("rollback report %+v", p)
	}
	if st, _ := LoadState(dataDir); st != nil {
		t.Fatal("state must be removed once reported")
	}
	// The failed version is not re-installed automatically right away.
	old.Offer(wire.UpdateOffer{Version: "2.0.0", SHA256: strings.Repeat("a", 64), Size: 10})
	if p := h2.last(); p.Error != ErrRecentlyFailed {
		t.Fatalf("expected recently_failed, got %+v", p)
	}
}

func TestTooManyStartsRollsBack(t *testing.T) {
	dataDir, inst := switched(t, 2)
	h := &harness{}
	u := newUpdater(h, dataDir, inst, "2.0.0", nil)
	if !u.Startup() {
		t.Fatal("third start must roll back")
	}
	if cur, _ := ReadLink(inst, "current"); cur != "releases/1.0.0" {
		t.Fatalf("current = %s", cur)
	}
	if st, _ := LoadState(dataDir); st.Error != ErrTooManyStarts {
		t.Fatalf("state %+v", st)
	}
}

func TestRollbackCommandAfterCrashLoop(t *testing.T) {
	dataDir, inst := switched(t, 1)
	msg, err := Rollback(dataDir, inst)
	if err != nil || !strings.Contains(msg, "rolled back") {
		t.Fatalf("%s %v", msg, err)
	}
	if cur, _ := ReadLink(inst, "current"); cur != "releases/1.0.0" {
		t.Fatalf("current = %s", cur)
	}
	if msg, _ := Rollback(dataDir, inst); msg != "nothing to roll back" {
		t.Fatalf("second run: %s", msg)
	}
	h := &harness{}
	old := newUpdater(h, dataDir, inst, "1.0.0", nil)
	old.Startup()
	old.MarkConnected()
	if p := h.last(); p.State != PhaseRolledBack || p.Error != ErrCrashLoop {
		t.Fatalf("report %+v", p)
	}
	if msg, _ := Rollback(t.TempDir(), inst); msg != "nothing to roll back" {
		t.Fatal("no state: nothing to do")
	}
}

func TestGitHubFallback(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	data, sum := makePackage(t, "3.0.0", nil)
	pkgName := "opensignage-3.0.0-linux-amd64.tar.gz"
	info, _ := json.Marshal(release.Info{Version: "3.0.0", Date: "2026-10-11",
		Packages: []release.Package{{OS: "linux", Arch: "amd64", File: pkgName, SHA256: sum, Size: int64(len(data))}}})
	infoSum := sha256.Sum256(info)
	sums := release.FormatSums(map[string]string{pkgName: sum, release.InfoFile: hex.EncodeToString(infoSum[:])})
	sig := release.Sign(priv, sums)
	files := map[string][]byte{release.SumsFile: sums, release.SigFile: []byte(sig), release.InfoFile: info, pkgName: data}
	var requested []string
	var mu sync.Mutex
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requested = append(requested, r.URL.Path)
		mu.Unlock()
		name, ok := strings.CutPrefix(r.URL.Path, "/owner/repo/releases/download/v3.0.0/")
		if b, found := files[name]; ok && found {
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
			return
		}
		http.NotFound(w, r)
	}))
	defer gh.Close()
	get := func(ctx context.Context, target string, hdr http.Header) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		for k, v := range hdr {
			req.Header[k] = v
		}
		return http.DefaultClient.Do(req)
	}

	run := func(trusted string) (*harness, string) {
		h := &harness{}
		inst := install(t, "2.0.0", "2.0.0")
		u := newUpdater(h, t.TempDir(), inst, "2.0.0", nil)
		u.o.Internet, u.o.GitHubBase, u.o.TrustedKeys = get, gh.URL, []string{trusted}
		if err := u.GitHubAvailable("2.0.0", "owner/repo"); err == nil {
			t.Fatal("the fallback must not be offered for the running version")
		}
		if err := u.GitHubAvailable("3.0.0", "owner/../x"); err == nil {
			t.Fatal("invalid repo must be refused")
		}
		if err := u.StartGitHub("3.0.0", "owner/repo"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "github update end", func() bool { return h.exited() == 1 || h.last().State == PhaseFailed })
		return h, inst
	}

	h, inst := run(release.FormatPublicKey(pub))
	if h.exited() != 1 {
		t.Fatalf("github update failed: %+v", h.last())
	}
	if cur, _ := ReadLink(inst, "current"); cur != "releases/3.0.0" {
		t.Fatalf("current = %s", cur)
	}

	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	h, inst = run(release.FormatPublicKey(otherPub))
	if p := h.last(); p.State != PhaseFailed || p.Error != ErrSignature {
		t.Fatalf("untrusted signature must fail: %+v", p)
	}
	if cur, _ := ReadLink(inst, "current"); cur != "releases/2.0.0" {
		t.Fatalf("current changed: %s", cur)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range requested[len(requested)-2:] {
		if strings.HasSuffix(p, ".tar.gz") {
			t.Fatal("the package must not be downloaded before the signature is verified")
		}
	}
}

func TestExtractRejectsEscapes(t *testing.T) {
	for _, name := range []string{"top/../../evil", "/abs", "top/bin/../../evil"} {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
		tw.Write([]byte("x"))
		tw.Close()
		gz.Close()
		p := filepath.Join(t.TempDir(), "p.tar.gz")
		os.WriteFile(p, buf.Bytes(), 0o644)
		if err := Extract(p, filepath.Join(t.TempDir(), "out"), 1<<20); err == nil {
			t.Errorf("%q must be refused", name)
		}
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "top/bin/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	tw.Close()
	gz.Close()
	p := filepath.Join(t.TempDir(), "p.tar.gz")
	os.WriteFile(p, buf.Bytes(), 0o644)
	if err := Extract(p, filepath.Join(t.TempDir(), "out"), 1<<20); err == nil {
		t.Error("symlinks must be refused")
	}
}
