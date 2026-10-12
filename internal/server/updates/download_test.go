package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"opensignage/internal/common/release"
)

func TestSelectReleases(t *testing.T) {
	rel := func(tag string, pre, draft bool) ghRelease {
		return ghRelease{TagName: tag, Prerelease: pre, Draft: draft, Body: "notes " + tag,
			Assets: []ghAsset{{Name: "a.tar.gz", Size: 100}, {Name: "opensignage-x-bundle.tar", Size: 1000}, {Name: "release.json", Size: 5}}}
	}
	list := []ghRelease{
		rel("v1.2.0", false, false),
		rel("v1.10.0", false, false), // numeric, not lexical
		rel("v1.11.0", false, true),  // draft: never
		rel("v1.12.0-beta.1", true, false),
		rel("v1.13.0-rc.1", false, false), // SemVer pre-release without the GitHub flag
		rel("v1.1.0", false, false),
		rel("not-a-version", false, false),
		rel("v1.0.0-../../x", false, false), // path-unsafe: ignored
	}
	cases := []struct {
		current, best string
		same          bool
	}{
		{"1.1.0", "1.10.0", true},             // stable only for a stable server
		{"1.10.0", "", true},                  // nothing newer that is stable
		{"1.2.0-dev.1", "1.13.0-rc.1", false}, // pre-release server: pre-releases count
		{"1.13.0-rc.1", "", true},
		{"2.0.0", "", false},
	}
	for _, c := range cases {
		best, same := selectReleases(list, c.current)
		got := ""
		if best != nil {
			got = best.Version
		}
		if got != c.best || (same != nil) != c.same {
			t.Errorf("current %s: best %q same %v, want %q %v", c.current, got, same != nil, c.best, c.same)
		}
		if best != nil && best.Size != 105 {
			t.Errorf("size must exclude the bundle: %d", best.Size)
		}
	}
}

func TestGitHubRateLimitIsHonored(t *testing.T) {
	var calls atomic.Int32
	reset := time.Now().Add(30 * time.Minute).Unix()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "OpenSignage/") {
			t.Errorf("user agent %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset))
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()
	m := newTestModule(t)
	m.gh.base = ts.URL
	if _, err := m.check(context.Background()); !isCode(err, "github_rate_limited") {
		t.Fatalf("want rate limit error, got %v", err)
	}
	if _, err := m.check(context.Background()); !isCode(err, "github_rate_limited") {
		t.Fatalf("second check: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("GitHub must not be asked again before the reset, calls=%d", calls.Load())
	}
	if c := m.loadCheck(); c.LastError == nil || c.LastError.Code != "github_rate_limited" || c.LastCheckAt == "" {
		t.Fatalf("last error not stored: %+v", c)
	}
}

func TestCheckOfflineStaysQuiet(t *testing.T) {
	m := newTestModule(t)
	m.gh.base = "http://127.0.0.1:1" // nothing listens
	m.periodicCheck(context.Background())
	c := m.loadCheck()
	if c.LastError == nil || c.LastError.Code != "github_unreachable" {
		t.Fatalf("offline check must record the error: %+v", c)
	}
	var n int
	_ = m.app.DB.R.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n)
	if n != 0 {
		t.Fatalf("periodic checks must not write audit entries")
	}
}

// fakeGitHub serves the releases API and the assets of releases on disk. The first
// request of each package is cut off half way (resume test).
type fakeGitHub struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	releases []ghRelease
	files    map[string]string // asset name → path
	ranges   []string
	served   map[string]int64
	cut      map[string]bool
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t, files: map[string]string{}, served: map[string]int64{}, cut: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// use points the module at the fake API and accepts downloads from the fake's host only
// (instead of GitHub's hosts).
func (f *fakeGitHub) use(m *Module) {
	m.gh.base = f.srv.URL
	m.gh.allowed = func(u *url.URL) bool { return u.Scheme == "http" && u.Host == f.srv.Listener.Addr().String() }
}

func (f *fakeGitHub) add(tag, dir string, prerelease bool) {
	entries, _ := os.ReadDir(dir)
	r := ghRelease{TagName: tag, Prerelease: prerelease, Body: "Fixes\nMore fixes", PublishedAt: "2026-10-11T10:00:00Z"}
	for _, e := range entries {
		st, _ := e.Info()
		f.files[e.Name()] = filepath.Join(dir, e.Name())
		r.Assets = append(r.Assets, ghAsset{Name: e.Name(), Size: st.Size(), URL: f.srv.URL + "/download/" + e.Name()})
	}
	f.releases = append(f.releases, r)
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/repos/owner/repo/releases") {
		_ = json.NewEncoder(w).Encode(f.releases)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/download/")
	p, ok := f.files[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, _ := os.ReadFile(p)
	f.mu.Lock()
	if rg := r.Header.Get("Range"); rg != "" {
		f.ranges = append(f.ranges, name+" "+rg)
	}
	first := !f.cut[name] && strings.HasSuffix(name, ".tar.gz")
	f.cut[name] = true
	f.mu.Unlock()
	if first {
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data[:len(data)/2])
		w.(http.Flusher).Flush()
		f.mu.Lock()
		f.served[name] += int64(len(data) / 2)
		f.mu.Unlock()
		panic(http.ErrAbortHandler) // connection drops in the middle of the download
	}
	cw := &countWriter{ResponseWriter: w}
	http.ServeContent(cw, r, name, time.Time{}, strings.NewReader(string(data)))
	f.mu.Lock()
	f.served[name] += cw.n
	f.mu.Unlock()
}

type countWriter struct {
	http.ResponseWriter
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	c.n += int64(n)
	return n, err
}

func TestDownloadResumesAndVerifies(t *testing.T) {
	m := newTestModule(t)
	key := newKey(t)
	trust(m, key)
	gh := newFakeGitHub(t)
	gh.use(m)
	big := strings.Repeat("0123456789abcdef", 64<<10) // 1 MiB
	src := filepath.Join(t.TempDir(), "rel")
	info := writeRelease(t, src, "9.0.0", key, map[string][]byte{
		"linux/amd64": []byte("amd64:" + big), "linux/arm64": []byte("arm64:" + big), "linux/armv7": []byte("armv7:" + big)})
	gh.add("v9.0.0", src, false)

	best, err := m.check(context.Background())
	if err != nil || best == nil || best.Version != "9.0.0" {
		t.Fatalf("check: %v %+v", err, best)
	}
	row, _ := m.getRelease(context.Background(), "9.0.0")
	if row == nil || row.State != relAvailable || row.Notes != "Fixes\nMore fixes" || row.Size == 0 {
		t.Fatalf("release row: %+v", row)
	}
	if err := m.downloadRelease(context.Background(), "9.0.0"); err != nil {
		t.Fatalf("download: %v", err)
	}
	if _, err := release.VerifyDir(m.releaseDir("9.0.0"), m.trustedKeys(), false); err != nil {
		t.Fatalf("downloaded release does not verify: %v", err)
	}
	for _, p := range info.Packages {
		half := p.Size / 2
		want := fmt.Sprintf("%s bytes=%d-", p.File, half)
		found := false
		for _, r := range gh.ranges {
			found = found || r == want
		}
		if !found {
			t.Errorf("no resume request %q in %v", want, gh.ranges)
		}
		if got := gh.served[p.File]; got != p.Size {
			t.Errorf("%s: %d bytes transferred for %d (resume must not start over)", p.File, got, p.Size)
		}
		if _, err := os.Stat(filepath.Join(m.releaseDir("9.0.0"), p.File+".part")); !os.IsNotExist(err) {
			t.Errorf("partial file left behind")
		}
	}
	// the endpoint flow marks the release ready
	m.finishDownload("9.0.0", nil, nil, "")
	if row, _ := m.getRelease(context.Background(), "9.0.0"); row.State != relReady {
		t.Fatalf("state %s", row.State)
	}
}

func TestDownloadRejectsWrongSignature(t *testing.T) {
	m := newTestModule(t)
	trusted, attacker := newKey(t), newKey(t)
	trust(m, trusted)
	gh := newFakeGitHub(t)
	gh.use(m)
	src := filepath.Join(t.TempDir(), "rel")
	writeRelease(t, src, "9.0.0", attacker, map[string][]byte{"linux/amd64": []byte("evil")})
	gh.add("v9.0.0", src, false)
	if _, err := m.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := m.downloadRelease(context.Background(), "9.0.0")
	if !isCode(err, "release_signature_invalid") {
		t.Fatalf("want signature error, got %v", err)
	}
	for name := range gh.served {
		if strings.HasSuffix(name, ".tar.gz") {
			t.Fatalf("packages of an untrusted release must not be downloaded")
		}
	}
	m.finishDownload("9.0.0", err, nil, "")
	if _, err := os.Stat(m.releaseDir("9.0.0")); !os.IsNotExist(err) {
		t.Fatalf("files of an untrusted release must be removed")
	}
	row, _ := m.getRelease(context.Background(), "9.0.0")
	if row.State != relFailed || !strings.Contains(row.Error, "release_signature_invalid") {
		t.Fatalf("row: %+v", row)
	}
}

func TestDownloadRejectsTamperedPackage(t *testing.T) {
	m := newTestModule(t)
	key := newKey(t)
	trust(m, key)
	gh := newFakeGitHub(t)
	gh.use(m)
	src := filepath.Join(t.TempDir(), "rel")
	writeRelease(t, src, "9.0.0", key, map[string][]byte{"linux/amd64": []byte("good content")})
	_ = os.WriteFile(filepath.Join(src, "opensignage-9.0.0-linux-amd64.tar.gz"), []byte("evil content"), 0o644) // same size
	gh.add("v9.0.0", src, false)
	if _, err := m.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := m.downloadRelease(context.Background(), "9.0.0")
	if !isCode(err, "package_checksum_mismatch") {
		t.Fatalf("want checksum error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.releaseDir("9.0.0"), "opensignage-9.0.0-linux-amd64.tar.gz")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tampered package must not be kept")
	}
}

func TestGitHubURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://github.com/o/r/releases/download/v1.0.0/release.json":          true,
		"https://objects.githubusercontent.com/github-production-release-asset": true,
		"https://release-assets.githubusercontent.com/x":                        true,
		"https://api.github.com/repos/o/r/releases":                             true,
		"https://GitHub.com:443/o/r":                                            true,
		"http://github.com/o/r":                                                 false,
		"https://github.com.evil.example/x":                                     false,
		"https://evilgithubusercontent.com/x":                                   false,
		"https://user:pw@github.com/x":                                          false,
		"https://github.com:8443/x":                                             false,
		"https://127.0.0.1/x":                                                   false,
		"file:///etc/passwd":                                                    false,
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if githubURL(u) != want {
			t.Errorf("%s: %v", raw, !want)
		}
	}
}

// Asset URLs come from the API answer: anything that is not a GitHub download host is
// refused, also as a redirect target, before a byte is requested from it.
func TestDownloadOnlyFromGitHub(t *testing.T) {
	m := newTestModule(t)
	key := newKey(t)
	trust(m, key)
	gh := newFakeGitHub(t)
	gh.use(m)
	m.gh.allowed = githubURL // the fake's asset URLs are plain http on 127.0.0.1
	src := filepath.Join(t.TempDir(), "rel")
	writeRelease(t, src, "9.0.0", key, map[string][]byte{"linux/amd64": []byte("pkg")})
	gh.add("v9.0.0", src, false)
	if _, err := m.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.downloadRelease(context.Background(), "9.0.0"); !isCode(err, "download_failed") || !strings.Contains(encodeErr(err), errForeignURL.Error()) {
		t.Fatalf("want a refused download, got %v", err)
	}
	if len(gh.served) != 0 {
		t.Fatalf("nothing may be requested from a foreign host: %v", gh.served)
	}

	// A permitted host that redirects elsewhere: the redirect is not followed.
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer other.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	defer redir.Close()
	m.gh.allowed = func(u *url.URL) bool { return u.Host == redir.Listener.Addr().String() }
	err := m.fetchWithRetry(context.Background(), redir.URL+"/release.json", filepath.Join(t.TempDir(), "release.json"), -1, "", nil)
	if !isCode(err, "download_failed") || hits.Load() != 0 {
		t.Fatalf("redirect to a foreign host must be refused: %v (hits %d)", err, hits.Load())
	}
}
