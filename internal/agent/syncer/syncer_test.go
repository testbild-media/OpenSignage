package syncer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"opensignage/internal/agent/state"
	"opensignage/internal/agent/wire"
)

type blobDef struct {
	data []byte
	sha  string
}

func newBlob(t *testing.T, n int) blobDef {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return blobDef{data: b, sha: hex.EncodeToString(sum[:])}
}

func manifestJSON(t *testing.T, id string, assets ...wire.ManifestAsset) []byte {
	t.Helper()
	m := wire.Manifest{ID: id, ZoneID: "zone-1", Hash: "h-" + id, Timezone: "Europe/Berlin", Assets: assets,
		Playlists: []wire.Playlist{{ID: "pl-" + id, Name: "P"}}}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func asset(id string, b blobDef) wire.ManifestAsset {
	return wire.ManifestAsset{ID: id, Revision: 1, Kind: wire.KindImage, SHA256: b.sha, Size: int64(len(b.data)),
		MIME: "image/png", Variant: wire.VariantOrig, URL: "/device/v1/blobs/" + b.sha}
}

// fakeServer serves manifests and blobs (with Range via http.ServeContent).
type fakeServer struct {
	mu        sync.Mutex
	manifests map[string][]byte
	blobs     map[string][]byte
	ranges    []string
	hook      func(w http.ResponseWriter, r *http.Request, sha string) bool // true = handled
	srv       *httptest.Server
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{manifests: map[string][]byte{}, blobs: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /device/v1/deployments/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		m, ok := f.manifests[r.PathValue("id")]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(m)
	})
	mux.HandleFunc("GET /device/v1/blobs/{sha}", func(w http.ResponseWriter, r *http.Request) {
		sha := r.PathValue("sha")
		f.mu.Lock()
		f.ranges = append(f.ranges, r.Header.Get("Range"))
		data, ok := f.blobs[sha]
		hook := f.hook
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if hook != nil && hook(w, r, sha) {
			return
		}
		http.ServeContent(w, r, sha, time.Time{}, bytes.NewReader(data))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) add(id string, m []byte, blobs ...blobDef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manifests[id] = m
	for _, b := range blobs {
		f.blobs[b.sha] = b.data
	}
}

func (f *fakeServer) Get(ctx context.Context, target string, hdr http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.srv.URL+target, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	return f.srv.Client().Do(req)
}

type recorder struct {
	mu       sync.Mutex
	progress []wire.DeployProgress
	changes  int
}

func (r *recorder) onProgress(p wire.DeployProgress) {
	r.mu.Lock()
	r.progress = append(r.progress, p)
	r.mu.Unlock()
}

func (r *recorder) last() wire.DeployProgress {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.progress) == 0 {
		return wire.DeployProgress{}
	}
	return r.progress[len(r.progress)-1]
}

func newSyncer(t *testing.T, st *state.Store, c Client, rec *recorder, free uint64) *Syncer {
	return New(Options{
		Store:      st,
		Client:     c,
		FreeSpace:  func(string) (uint64, error) { return free, nil },
		OnProgress: rec.onProgress,
		OnChange:   func() { rec.mu.Lock(); rec.changes++; rec.mu.Unlock() },
	})
}

func newStore(t *testing.T) *state.Store {
	st, err := state.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// activate makes deployment id active directly (test setup).
func activate(t *testing.T, st *state.Store, id string, m []byte, blobs ...blobDef) {
	t.Helper()
	if err := st.SaveManifest(id, m); err != nil {
		t.Fatal(err)
	}
	for _, b := range blobs {
		os.WriteFile(st.BlobPath(b.sha), b.data, 0o640)
	}
	if err := st.SaveDeployment(state.Deployment{Active: &state.Ref{ID: id, Hash: "h-" + id}}); err != nil {
		t.Fatal(err)
	}
}

const enough = 100 << 30

func TestResumeAfterInterruptedDownload(t *testing.T) {
	st := newStore(t)
	srv := newFakeServer(t)
	b := newBlob(t, 1<<20)
	srv.add("dep-b", manifestJSON(t, "dep-b", asset("a1", b)), b)
	var calls atomic.Int32
	srv.hook = func(w http.ResponseWriter, r *http.Request, sha string) bool {
		if calls.Add(1) == 1 { // first request: connection drops after 300 KB
			w.Header().Set("Content-Length", "1048576")
			w.WriteHeader(http.StatusOK)
			w.Write(b.data[:300<<10])
			return true
		}
		return false
	}
	rec := &recorder{}
	s := newSyncer(t, st, srv, rec, enough)
	s.Announce("dep-b", "h-dep-b")
	s.Step(context.Background())
	if p := rec.last(); p.State != wire.DeployFailed || p.Error != ErrDownload {
		t.Fatalf("expected failed download, got %+v", p)
	}
	if fi, err := os.Stat(st.PartPath(b.sha)); err != nil || fi.Size() != 300<<10 {
		t.Fatalf("partial file should be kept: %v %v", fi, err)
	}
	// An agent restart in between must resume from the .part file as well.
	s = newSyncer(t, st, srv, rec, enough)
	s.SyncNow()
	s.Step(context.Background())
	if p := rec.last(); p.State != wire.DeployActive {
		t.Fatalf("expected active, got %+v", p)
	}
	srv.mu.Lock()
	last := srv.ranges[len(srv.ranges)-1]
	srv.mu.Unlock()
	if last != "bytes=307200-" {
		t.Fatalf("download did not resume with Range, got %q", last)
	}
	got, err := os.ReadFile(st.BlobPath(b.sha))
	if err != nil || !bytes.Equal(got, b.data) {
		t.Fatal("blob content wrong after resume")
	}
	if _, err := os.Stat(st.PartPath(b.sha)); !os.IsNotExist(err) {
		t.Fatal("part file must be gone")
	}
	dep, l := s.Current()
	if dep.Active == nil || dep.Active.ID != "dep-b" || dep.Pending != nil || l == nil {
		t.Fatalf("not switched: %+v", dep)
	}
}

func TestChecksumMismatchRetries(t *testing.T) {
	st := newStore(t)
	srv := newFakeServer(t)
	b := newBlob(t, 64<<10)
	srv.add("dep-c", manifestJSON(t, "dep-c", asset("a1", b)), b)
	var calls atomic.Int32
	srv.hook = func(w http.ResponseWriter, r *http.Request, sha string) bool {
		if calls.Add(1) == 1 {
			bad := bytes.Clone(b.data)
			bad[100] ^= 0xff
			w.Write(bad)
			return true
		}
		return false
	}
	rec := &recorder{}
	s := newSyncer(t, st, srv, rec, enough)
	s.Announce("dep-c", "h-dep-c")
	s.Step(context.Background())
	if p := rec.last(); p.State != wire.DeployActive {
		t.Fatalf("expected active after retry, got %+v", p)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 requests, got %d", calls.Load())
	}
	srv.mu.Lock()
	second := srv.ranges[1]
	srv.mu.Unlock()
	if second != "" {
		t.Fatalf("retry after mismatch must start from zero, got Range %q", second)
	}
}

func TestPersistentChecksumMismatchFails(t *testing.T) {
	st := newStore(t)
	srv := newFakeServer(t)
	b := newBlob(t, 4<<10)
	srv.add("dep-x", manifestJSON(t, "dep-x", asset("a1", b)), b)
	srv.hook = func(w http.ResponseWriter, r *http.Request, sha string) bool {
		w.Write(make([]byte, 4<<10))
		return true
	}
	rec := &recorder{}
	s := newSyncer(t, st, srv, rec, enough)
	s.Announce("dep-x", "h-dep-x")
	s.Step(context.Background())
	if p := rec.last(); p.State != wire.DeployFailed || p.Error != ErrChecksum {
		t.Fatalf("expected checksum failure, got %+v", p)
	}
	if _, err := os.Stat(st.BlobPath(b.sha)); !os.IsNotExist(err) {
		t.Fatal("corrupt blob must not be stored")
	}
	// Backoff: an immediate second step does nothing.
	n := len(rec.progress)
	s.Step(context.Background())
	if len(rec.progress) != n {
		t.Fatal("retry must wait for the backoff (no hot loop)")
	}
}

func TestTwoPhaseSwitchNeverExposesPartialState(t *testing.T) {
	st := newStore(t)
	srv := newFakeServer(t)
	a := newBlob(t, 10<<10)
	b := newBlob(t, 256<<10)
	activate(t, st, "dep-a", manifestJSON(t, "dep-a", asset("img", a)), a)
	srv.add("dep-b", manifestJSON(t, "dep-b", asset("img", b)), b)
	release := make(chan struct{})
	started := make(chan struct{})
	srv.hook = func(w http.ResponseWriter, r *http.Request, sha string) bool {
		w.Header().Set("Content-Length", "262144")
		w.WriteHeader(http.StatusOK)
		w.Write(b.data[:1000])
		w.(http.Flusher).Flush()
		close(started)
		<-release
		w.Write(b.data[1000:])
		return true
	}
	rec := &recorder{}
	s := newSyncer(t, st, srv, rec, enough)
	s.Announce("dep-b", "h-dep-b")
	done := make(chan struct{})
	go func() { s.Step(context.Background()); close(done) }()
	<-started
	// Mid-download: old content fully active, new one only pending.
	dep, l := s.Current()
	if dep.Active == nil || dep.Active.ID != "dep-a" || l == nil || l.Ref.ID != "dep-a" {
		t.Fatalf("active changed during download: %+v", dep)
	}
	onDisk, _ := st.LoadDeployment()
	if onDisk.Active.ID != "dep-a" || onDisk.Pending == nil || onDisk.Pending.ID != "dep-b" {
		t.Fatalf("deployment.json during download: %+v", onDisk)
	}
	if p, _, sha, ok := s.AssetFile("img"); !ok || sha != a.sha || p == "" {
		t.Fatal("player must still get the old asset")
	}
	rec.mu.Lock()
	changes := rec.changes
	rec.mu.Unlock()
	if changes != 0 {
		t.Fatal("no activation may be signalled before the download completes")
	}
	close(release)
	<-done
	dep, l = s.Current()
	if dep.Active.ID != "dep-b" || dep.Previous == nil || dep.Previous.ID != "dep-a" || dep.Pending != nil || l.Ref.ID != "dep-b" {
		t.Fatalf("switch wrong: %+v", dep)
	}
	if _, _, sha, ok := s.AssetFile("img"); !ok || sha != b.sha {
		t.Fatal("new asset not served after switch")
	}
	// Previous content stays cached as fallback.
	if _, err := os.Stat(st.BlobPath(a.sha)); err != nil {
		t.Fatal("previous blob must be kept")
	}
}

func TestDiskFullKeepsOldContent(t *testing.T) {
	st := newStore(t)
	srv := newFakeServer(t)
	a := newBlob(t, 1<<10)
	b := newBlob(t, 1<<20)
	activate(t, st, "dep-a", manifestJSON(t, "dep-a", asset("img", a)), a)
	srv.add("dep-b", manifestJSON(t, "dep-b", asset("img2", b)), b)
	rec := &recorder{}
	s := newSyncer(t, st, srv, rec, 150<<20) // 150 MB free < 1 MB + 10% + 200 MB
	s.Announce("dep-b", "h-dep-b")
	s.Step(context.Background())
	p := rec.last()
	if p.State != wire.DeployFailed || p.Error != ErrDiskFull || p.ID != "dep-b" {
		t.Fatalf("expected disk_full, got %+v", p)
	}
	dep, l := s.Current()
	if dep.Active == nil || dep.Active.ID != "dep-a" || l.Ref.ID != "dep-a" {
		t.Fatal("old content must stay active")
	}
	if _, err := os.Stat(st.PartPath(b.sha)); !os.IsNotExist(err) {
		t.Fatal("nothing may be downloaded when the disk is too full")
	}
	srv.mu.Lock()
	n := len(srv.ranges)
	srv.mu.Unlock()
	if n != 0 {
		t.Fatal("no blob request expected")
	}
	if info := s.Info(); info.State != wire.DeployFailed || info.Error != ErrDiskFull {
		t.Fatalf("status must show disk_full: %+v", info)
	}
}

func TestCacheGCProtectsKeptDeployments(t *testing.T) {
	st := newStore(t)
	a, b, c, orphan := newBlob(t, 100), newBlob(t, 200), newBlob(t, 300), newBlob(t, 400)
	cPart := newBlob(t, 500)
	st.SaveManifest("dep-a", manifestJSON(t, "dep-a", asset("x", a)))
	st.SaveManifest("dep-b", manifestJSON(t, "dep-b", asset("y", b)))
	st.SaveManifest("dep-c", manifestJSON(t, "dep-c", asset("z", c), asset("w", cPart)))
	st.SaveManifest("dep-old", manifestJSON(t, "dep-old", asset("o", orphan)))
	for _, bl := range []blobDef{a, b, c, orphan} {
		os.WriteFile(st.BlobPath(bl.sha), bl.data, 0o640)
	}
	os.WriteFile(st.PartPath(cPart.sha), cPart.data[:10], 0o640)
	os.WriteFile(st.PartPath(orphan.sha), orphan.data[:10], 0o640)
	os.WriteFile(st.BlobPath("not-a-sha"), []byte("junk"), 0o640)
	st.SaveDeployment(state.Deployment{
		Active:   &state.Ref{ID: "dep-a", Hash: "h-dep-a"},
		Previous: &state.Ref{ID: "dep-b", Hash: "h-dep-b"},
		Pending:  &state.Ref{ID: "dep-c", Hash: "h-dep-c"},
	})
	s := newSyncer(t, st, nil, &recorder{}, enough)
	// The preview reports exactly what the cleanup deletes, and deletes nothing.
	pre, err := s.PreviewCleanCache()
	if err != nil {
		t.Fatal(err)
	}
	if !pre.DryRun || pre.Deleted != 3 || pre.FreedBytes != 414 || pre.Partial != 1 || pre.PartialBytes != 10 || pre.CacheBytes != 600 {
		t.Fatalf("preview %+v", pre)
	}
	for _, still := range []string{st.BlobPath(orphan.sha), st.PartPath(orphan.sha), st.BlobPath("not-a-sha")} {
		if _, err := os.Stat(still); err != nil {
			t.Fatalf("preview deleted %s", still)
		}
	}
	res, err := s.CleanCache()
	if err != nil {
		t.Fatal(err)
	}
	if res.DryRun || res.Deleted != pre.Deleted || res.FreedBytes != pre.FreedBytes || res.CacheBytes != pre.CacheBytes {
		t.Fatalf("cleanup %+v differs from its preview %+v", res, pre)
	}
	for _, keep := range []string{st.BlobPath(a.sha), st.BlobPath(b.sha), st.BlobPath(c.sha), st.PartPath(cPart.sha)} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s must be kept", keep)
		}
	}
	for _, gone := range []string{st.BlobPath(orphan.sha), st.PartPath(orphan.sha), st.BlobPath("not-a-sha")} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s must be deleted", gone)
		}
	}
	if res.Deleted != 3 || res.CacheBytes != 600 {
		t.Fatalf("result %+v", res)
	}
}

func TestGCRefusesWithUnreadableManifest(t *testing.T) {
	st := newStore(t)
	a := newBlob(t, 100)
	os.WriteFile(st.BlobPath(a.sha), a.data, 0o640)
	os.WriteFile(st.ManifestPath("dep-a"), []byte("{broken"), 0o640)
	st.SaveDeployment(state.Deployment{Active: &state.Ref{ID: "dep-a"}})
	s := newSyncer(t, st, nil, &recorder{}, enough)
	if _, err := s.CleanCache(); err == nil {
		t.Fatal("GC must refuse when a kept manifest cannot be read")
	}
	if _, err := os.Stat(st.BlobPath(a.sha)); err != nil {
		t.Fatal("blob must survive")
	}
}

func TestClearedTarget(t *testing.T) {
	st := newStore(t)
	a := newBlob(t, 100)
	activate(t, st, "dep-a", manifestJSON(t, "dep-a", asset("x", a)), a)
	rec := &recorder{}
	s := newSyncer(t, st, nil, rec, enough)
	s.Clear("zone_deleted")
	s.Step(context.Background())
	dep, l := s.Current()
	if !dep.Cleared || dep.Active != nil || l != nil || dep.Previous == nil || dep.Previous.ID != "dep-a" {
		t.Fatalf("cleared state wrong: %+v", dep)
	}
	onDisk, _ := st.LoadDeployment()
	if !onDisk.Cleared {
		t.Fatal("cleared must be persisted")
	}
	// The previous content stays cached as fallback.
	if _, err := os.Stat(st.BlobPath(a.sha)); err != nil {
		t.Fatal("previous blob must be kept")
	}
	// A restart keeps the cleared target.
	s2 := newSyncer(t, st, nil, rec, enough)
	if d, _ := s2.Current(); !d.Cleared {
		t.Fatal("cleared lost after restart")
	}
}

func TestManifestHashMismatchRejected(t *testing.T) {
	st := newStore(t)
	srv := newFakeServer(t)
	b := newBlob(t, 100)
	srv.add("dep-h", manifestJSON(t, "dep-h", asset("x", b)), b)
	rec := &recorder{}
	s := newSyncer(t, st, srv, rec, enough)
	s.Announce("dep-h", "other-hash")
	s.Step(context.Background())
	if p := rec.last(); p.Error != ErrHashMismatch {
		t.Fatalf("got %+v", p)
	}
}

func TestSelectVariants(t *testing.T) {
	sha := func(c string) string { return strings.Repeat(c, 64) }
	m := &wire.Manifest{
		Products: wire.ManifestProducts{ImageAssetIDs: []string{"prod"}},
		Assets: []wire.ManifestAsset{
			{ID: "vid", Kind: "video", Variant: "orig", SHA256: sha("1")},
			{ID: "vid", Kind: "video", Variant: "v1080", SHA256: sha("2")},
			{ID: "vid", Kind: "video", Variant: "v720", SHA256: sha("3")},
			{ID: "fast", Kind: "video", Variant: "v1080", SHA256: sha("9")},
			{ID: "fast", Kind: "video", Variant: "v1080h", SHA256: sha("a")},
			{ID: "fast", Kind: "video", Variant: "v720", SHA256: sha("b")},
			{ID: "img", Kind: "image", Variant: "orig", SHA256: sha("4")},
			{ID: "img", Kind: "image", Variant: "i1920", SHA256: sha("5")},
			{ID: "prod", Kind: "image", Variant: "orig", SHA256: sha("6")},
			{ID: "prod", Kind: "image", Variant: "i512", SHA256: sha("7")},
			{ID: "only", Kind: "video", Variant: "orig", SHA256: sha("8")},
			{ID: "bad", Kind: "image", Variant: "orig", SHA256: "../../etc/passwd"},
		},
	}
	low := Select(m, Prefs{Perf: "low", ScreenWidth: 1280})
	std := Select(m, Prefs{Perf: "standard", ScreenWidth: 3840})
	high := Select(m, Prefs{Perf: "high", ScreenWidth: 3840})
	check := func(name string, got Choice, variant string) {
		t.Helper()
		if got.Asset.Variant != variant {
			t.Errorf("%s: got %s, want %s", name, got.Asset.Variant, variant)
		}
	}
	check("video low", low["vid"], "v720")
	check("video std", std["vid"], "v1080")
	check("video high", high["vid"], "v1080")
	// Above 30 fps the Raspberry Pi 4 (standard) plays v1080h, x86 (high) v1080
	check("fast video low", low["fast"], "v720")
	check("fast video std", std["fast"], "v1080h")
	check("fast video high", high["fast"], "v1080")
	check("image small screen", low["img"], "i1920")
	check("image 4k screen", std["img"], "orig")
	check("product", std["prod"], "i512")
	check("video fallback", low["only"], "orig")
	if _, ok := low["bad"]; ok {
		t.Error("invalid sha must be ignored")
	}
	if n := len(Needed(m, Prefs{Perf: "low", ScreenWidth: 1280})); n != 5 {
		t.Errorf("needed = %d, want one blob per asset (5)", n)
	}
}
