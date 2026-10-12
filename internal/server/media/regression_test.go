package media

// Regression tests for defects found in review: collector vs running jobs, deployment
// rebuild events, ready_revision semantics, cleanup safety, file access role, upload lock
// handling, plus hardening of the external tool calls.

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"opensignage/internal/server/core"
)

func bigPhotoProbe() Probe {
	return Probe{HasVideo: true, VideoCodec: "mjpeg", PixFmt: "yuvj420p", Width: 4000, Height: 3000}
}

func (h *harness) variantRows(assetID string) map[string]string {
	h.t.Helper()
	rows, err := h.app.DB.R.Query(`SELECT revision || '/' || variant, blob_sha256 FROM media_variants WHERE asset_id = ?`, assetID)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, s string
		_ = rows.Scan(&k, &s)
		out[k] = s
	}
	return out
}

func (h *harness) readyRevision(id string) int {
	h.t.Helper()
	return h.count(`SELECT ready_revision FROM media_assets WHERE id = ?`, id)
}

// ---- 1. the collector must not eat the results of a running job -------------------------

func TestCollectorDuringRunningJobKeepsFinishedVariants(t *testing.T) {
	h := newHarness(t) // grace period 0: every unreferenced blob is fair game
	c := h.login("operator")
	h.tc.probe = bigPhotoProbe() // orig, i1920, i512, thumb
	a := c.mustUpload("photo.jpg", jpegBytes(64, 48), nil)["id"].(string)

	var duringRuns, beforeFinalize int
	h.tc.onRun = func(n int) {
		if n >= 2 { // a later transcode is running: the first outputs are finished
			rep, err := h.m.CollectGarbage(context.Background(), false)
			if err != nil {
				t.Errorf("gc during transcode: %v", err)
			}
			duringRuns += rep.Blobs
		}
	}
	h.m.beforeFinalize = func() { // everything is stored, the final transaction has not run yet
		rep, err := h.m.CollectGarbage(context.Background(), false)
		if err != nil {
			t.Errorf("gc before finalize: %v", err)
		}
		beforeFinalize += rep.Blobs
	}
	h.runJobs()

	if st, e := h.assetStatus(a); st != "ready" {
		t.Fatalf("collector must not break the job: %s %q", st, e)
	}
	rows := h.variantRows(a)
	if len(rows) != 4 {
		t.Fatalf("variants: %v", rows)
	}
	for k, s := range rows {
		if !h.blobExists(s) || h.count(`SELECT COUNT(*) FROM blobs WHERE sha256 = ?`, s) != 1 {
			t.Errorf("variant %s lost its blob", k)
		}
	}
	if beforeFinalize != 0 {
		t.Errorf("the collector deleted %d pinned blobs before the final transaction", beforeFinalize)
	}
	// the pins are released afterwards
	h.m.pinMu.Lock()
	n := len(h.m.pins)
	h.m.pinMu.Unlock()
	if n != 0 {
		t.Errorf("pins leaked: %d", n)
	}
	// and a failing job releases its pins too, so its garbage can be collected
	h.tc.onRun = nil
	h.m.beforeFinalize = func() { panic("simulated crash before the final transaction") }
	b := c.mustUpload("photo2.jpg", jpegBytes(65, 49), nil)["id"].(string)
	h.runJobs()
	if st, _ := h.assetStatus(b); st != "failed" {
		t.Fatalf("crashed job: %s", st)
	}
	h.m.beforeFinalize = nil
	h.m.pinMu.Lock()
	n = len(h.m.pins)
	h.m.pinMu.Unlock()
	if n != 0 {
		t.Errorf("pins leaked after a crash: %d", n)
	}
	if rep, _ := h.m.CollectGarbage(context.Background(), false); rep.Blobs == 0 {
		t.Error("the stored variants of the crashed job are garbage and must be collectable")
	}
}

// ---- 2. every finished job triggers a deployment rebuild ----------------------------------

func TestEveryFinishedJobPublishesContent(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	sub := h.app.Bus.Subscribe(256)
	defer sub.Close()
	drain := func() {
		for {
			select {
			case <-sub.C:
			default:
				return
			}
		}
	}
	a := c.mustUpload("first.png", pngBytes(64, 48, 1), nil)["id"].(string)
	drain()
	h.runJobs()
	if !waitTopic(sub, core.TopicContent, time.Second) {
		t.Fatal("a newly processed asset (revision 1) must publish a content event: its variants now exist")
	}
	drain()
	c.mustOK("POST", "/api/v1/media/"+a+"/reprocess", nil)
	drain()
	h.runJobs()
	if !waitTopic(sub, core.TopicContent, time.Second) {
		t.Fatal("a finished reprocess must publish a content event")
	}
	// a failing job does not claim that anything shippable changed for a first revision
	h.tc.runErr = os.ErrInvalid
	b := c.mustUpload("second.png", pngBytes(64, 48, 2), nil)["id"].(string)
	drain()
	h.runJobs()
	if st, _ := h.assetStatus(b); st != "failed" {
		t.Fatal("failure expected")
	}
}

// ---- 3. ready_revision ------------------------------------------------------------------

func TestReadyRevisionLifecycle(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	first, second := pngBytes(64, 48, 1), pngBytes(64, 48, 2)
	a := c.mustUpload("logo.png", first, nil)["id"].(string)
	if h.readyRevision(a) != 0 {
		t.Fatal("nothing is ready before the first job ran")
	}
	h.runJobs()
	if h.readyRevision(a) != 1 {
		t.Fatalf("ready_revision after the first job: %d", h.readyRevision(a))
	}
	d := c.mustOK("GET", "/api/v1/media/"+a, nil)
	if d["readyRevision"].(float64) != 1 {
		t.Fatalf("API exposes readyRevision: %v", d["readyRevision"])
	}

	// reprocess: processing again, but the ready revision stays usable
	c.mustOK("POST", "/api/v1/media/"+a+"/reprocess", nil)
	if st, _ := h.assetStatus(a); st != "processing" || h.readyRevision(a) != 1 {
		t.Fatalf("reprocess: status %s ready_revision %d", st, h.readyRevision(a))
	}
	if res, _ := c.req("GET", "/api/v1/media/"+a+"/file?variant=thumb", nil, nil); res.StatusCode != 200 {
		t.Fatalf("thumbnail during reprocess: %d", res.StatusCode)
	}
	h.runJobs()

	// replacement: revision 2 processing, revision 1 remains the ready one
	if code, out := c.upload("logo.png", second, nil, &a); code != 200 {
		t.Fatalf("replace: %d %v", code, out)
	}
	if st, _ := h.assetStatus(a); st != "processing" || h.readyRevision(a) != 1 || h.count(`SELECT revision FROM media_assets WHERE id = ?`, a) != 2 {
		t.Fatalf("after replace: status %s ready_revision %d", st, h.readyRevision(a))
	}
	// derived variants follow the ready revision, the upload itself is addressed exactly
	ref, err := ResolveVariant(context.Background(), h.app.DB.R, a, 0, "thumb")
	if err != nil || ref.Revision != 1 {
		t.Fatalf("thumb while processing: %+v %v", ref, err)
	}
	if ref, err = ResolveVariant(context.Background(), h.app.DB.R, a, 0, "orig"); err != nil || ref.Revision != 2 || ref.SHA256 != sha(second) {
		t.Fatalf("orig is the newest upload: %+v %v", ref, err)
	}

	// old-revision cleanup must keep the revision screens use, even though it is not current
	pv := c.mustOK("POST", "/api/v1/media/old-revisions/delete?preview=1", map[string]any{"ids": []string{a}})
	if pv["items"].(float64) != 0 || pv["assets"].(float64) != 0 {
		t.Fatalf("revision 1 is the ready revision and not deletable: %v", pv)
	}
	c.mustOK("POST", "/api/v1/media/old-revisions/delete", map[string]any{"ids": []string{a}})
	if h.count(`SELECT COUNT(*) FROM media_revisions WHERE asset_id = ? AND revision = 1`, a) != 1 || len(h.variantNames(a, 1)) == 0 {
		t.Fatal("ready revision and its variants must survive the cleanup")
	}
	old := c.mustOK("GET", "/api/v1/media/unused", nil)["oldRevisions"].([]any)
	if len(old) != 0 {
		t.Fatalf("the ready revision is not listed as old: %v", old)
	}

	// the replacement fails: the ready revision still holds
	h.tc.runErr = os.ErrInvalid
	h.runJobs()
	if st, _ := h.assetStatus(a); st != "failed" || h.readyRevision(a) != 1 {
		t.Fatalf("failed replacement: %s ready_revision %d", st, h.readyRevision(a))
	}
	// ... and when it succeeds the ready revision moves on and revision 1 becomes old
	h.tc.runErr = nil
	c.mustOK("POST", "/api/v1/media/"+a+"/reprocess", nil)
	h.runJobs()
	if st, _ := h.assetStatus(a); st != "ready" || h.readyRevision(a) != 2 {
		t.Fatalf("after success: %s ready_revision %d", st, h.readyRevision(a))
	}
	c.mustOK("POST", "/api/v1/media/old-revisions/delete", map[string]any{"ids": []string{a}})
	if h.count(`SELECT COUNT(*) FROM media_revisions WHERE asset_id = ?`, a) != 1 {
		t.Fatal("revision 1 is deletable now")
	}
}

func TestReadyRevisionMigrationBackfill(t *testing.T) {
	h := newHarness(t)
	h.seedAsset("done", h.seedBlob(data(30, "done")))
	h.seedAsset("redo", h.seedBlob(data(30, "redo")))
	h.seedAsset("new", h.seedBlob(data(30, "new")))
	h.exec(`UPDATE media_assets SET revision = 3, status = 'ready' WHERE id = 'done'`)
	// legacy rows as an older server left them: status/revision only
	h.exec(`UPDATE media_assets SET revision = 2, status = 'processing' WHERE id = 'redo'`)
	h.exec(`INSERT INTO media_variants(asset_id, revision, variant, blob_sha256, created_at) SELECT 'redo', 1, 'thumb', blob_sha256, 'x' FROM media_assets WHERE id = 'redo'`)
	h.exec(`UPDATE media_assets SET revision = 1, status = 'processing' WHERE id = 'new'`)
	h.exec(`UPDATE media_assets SET ready_revision = 0`)
	sqlText, err := os.ReadFile(filepath.Join("..", "db", "migrations", "0003_media.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range strings.Split(string(sqlText), ";") {
		if strings.Contains(stmt, "ALTER TABLE") || !strings.Contains(stmt, "UPDATE") {
			continue // the column already exists in the test database
		}
		h.exec(stmt[strings.Index(stmt, "UPDATE"):])
	}
	if got := [3]int{h.readyRevision("done"), h.readyRevision("redo"), h.readyRevision("new")}; got != [3]int{3, 1, 0} {
		t.Fatalf("backfill: %v", got)
	}
}

// ---- 4. variant cleanup keeps what is needed to play -----------------------------------------

func TestVariantCleanupKeepsPlayableVariantsAndPublishesContent(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	h.tc.probe = bigPhotoProbe()
	img := c.mustUpload("huge.jpg", jpegBytes(64, 48), nil)["id"].(string)
	h.runJobs()
	if got := strings.Join(h.variantNames(img, 1), ","); got != "i1920,i512,orig,thumb" {
		t.Fatalf("variants: %s", got)
	}
	h.tc.probe = Probe{Container: "mov,mp4", HasVideo: true, VideoCodec: "hevc", PixFmt: "yuv420p", Width: 1920, Height: 1080, FPS: 25, DurationMs: 3000}
	vid := c.mustUpload("clip.mp4", mp4Bytes("clip"), nil)["id"].(string)
	h.runJobs()
	if got := strings.Join(h.variantNames(vid, 1), ","); got != "poster,thumb,v1080,v720" {
		t.Fatalf("variants: %s", got)
	}

	sub := h.app.Bus.Subscribe(64)
	defer sub.Close()
	out := c.mustOK("POST", "/api/v1/media/unused/variants/delete", map[string]any{"ids": []string{img, vid}})
	if out["assets"].(float64) != 2 {
		t.Fatalf("cleanup: %v", out)
	}
	if got := strings.Join(h.variantNames(img, 1), ","); got != "orig,thumb" {
		t.Fatalf("the downscaled original must survive: %s", got)
	}
	if got := strings.Join(h.variantNames(vid, 1), ","); got != "thumb,v1080" {
		t.Fatalf("what is needed to play stays: %s", got)
	}
	for _, id := range []string{img, vid} {
		if st, _ := h.assetStatus(id); st != "ready" || h.readyRevision(id) != 1 {
			t.Fatalf("%s stays usable: %s", id, st)
		}
		if ref, err := ResolveVariant(context.Background(), h.app.DB.R, id, 0, "display"); err != nil || ref.Variant == "" {
			t.Fatalf("display variant of %s: %+v %v", id, ref, err)
		}
	}
	if !waitTopic(sub, core.TopicContent, time.Second) {
		t.Fatal("a cleanup must publish a content event")
	}

	// old revisions: also a content event
	h.tc.probe = Probe{}
	p := c.mustUpload("logo.png", pngBytes(64, 48, 3), nil)["id"].(string)
	h.runJobs()
	c.upload("logo.png", pngBytes(64, 48, 4), nil, &p)
	h.runJobs()
	for len(sub.C) > 0 {
		<-sub.C
	}
	c.mustOK("POST", "/api/v1/media/old-revisions/delete", map[string]any{"ids": []string{p}})
	if !waitTopic(sub, core.TopicContent, time.Second) {
		t.Fatal("deleting old revisions must publish a content event")
	}
}

// ---- 5. role for files ------------------------------------------------------------------------

func TestMediaFilesRequireOperator(t *testing.T) {
	h := newHarness(t)
	op := h.login("operator")
	user := h.login("user")
	admin := h.login("admin")
	a := op.mustUpload("secret.png", pngBytes(32, 32, 1), nil)["id"].(string)
	h.runJobs()
	for _, path := range []string{"/file", "/file?variant=thumb", "/file?variant=display", "/file?variant=orig&rev=1"} {
		if res, _ := user.req("GET", "/api/v1/media/"+a+path, nil, nil); res.StatusCode != 403 {
			t.Errorf("role user %s: %d, want 403", path, res.StatusCode)
		}
		for name, c := range map[string]*client{"operator": op, "admin": admin} {
			if res, _ := c.req("GET", "/api/v1/media/"+a+path, nil, nil); res.StatusCode != 200 {
				t.Errorf("%s %s: %d, want 200", name, path, res.StatusCode)
			}
		}
	}
	if res, _ := user.req("GET", "/api/v1/media/uploads/x", nil, nil); res.StatusCode != 403 {
		t.Errorf("upload status as user: %d", res.StatusCode)
	}
	// custom fonts stay readable for every logged-in role
	code, face := admin.fontUpload(goodFont("Public Font"), "f.ttf", fakeSFNT("\x00\x01\x00\x00", 50))
	if code != 200 {
		t.Fatalf("font upload: %d %v", code, face)
	}
	if res, _ := user.req("GET", "/fonts/custom/"+face["id"].(string), nil, nil); res.StatusCode != 200 {
		t.Errorf("font file as user: %d", res.StatusCode)
	}
	if code, _ := user.do("GET", "/api/v1/fonts", nil); code != 200 {
		t.Errorf("font list as user: %d", code)
	}
}

// ---- 6. upload locks ----------------------------------------------------------------------------

func (h *harness) lockCount() int {
	h.m.upMu.Lock()
	defer h.m.upMu.Unlock()
	return len(h.m.upLocks)
}

func TestUploadLocksAreOnlyCreatedForRealOwnedUploads(t *testing.T) {
	h := newHarness(t)
	a := h.login("operator")
	b := h.login("operator")
	data := pngBytes(30, 30, 5)

	// made-up ids create no state at all
	for i := 0; i < 200; i++ {
		id := "made-up-" + strconv.Itoa(i)
		if code, _ := a.putChunk(id, data, 0, 99); code != 404 {
			t.Fatalf("unknown upload: %d", code)
		}
		if code, _ := a.do("DELETE", "/api/v1/media/uploads/"+id, nil); code != 404 {
			t.Fatalf("unknown abort: %d", code)
		}
	}
	if n := h.lockCount(); n != 0 {
		t.Fatalf("made-up ids must not grow the lock map: %d entries", n)
	}

	_, out := a.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "mine.png", "size": len(data)})
	id := out["uploadId"].(string)
	if code, _ := a.putChunk(id, data, 0, 99); code != 200 {
		t.Fatal("first chunk")
	}
	if h.lockCount() != 1 {
		t.Fatalf("one open upload, one lock: %d", h.lockCount())
	}
	// another user probing the id must neither learn anything nor disturb the state
	for i := 0; i < 5; i++ {
		if code, _ := b.putChunk(id, data, 100, 199); code != 404 {
			t.Fatalf("foreign upload: %d", code)
		}
		if code, _ := b.do("DELETE", "/api/v1/media/uploads/"+id, nil); code != 404 {
			t.Fatalf("foreign abort: %d", code)
		}
	}
	if h.lockCount() != 1 {
		t.Fatalf("a foreign user must not drop the owner's lock: %d", h.lockCount())
	}
	h.m.upMu.Lock()
	_, hashKept := h.m.upHashes[id]
	h.m.upMu.Unlock()
	if !hashKept {
		t.Fatal("a foreign user must not drop the owner's hash state")
	}

	// a request whose context is already cancelled must not tear the state down either
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("PUT", "/api/v1/media/uploads/"+id, strings.NewReader("x")).WithContext(ctx)
	req.SetPathValue("id", id)
	req.Header.Set("Content-Range", "bytes 100-100/"+strconv.Itoa(len(data)))
	owner := &core.Principal{UserID: h.ownerOf(id), Role: core.RoleOperator}
	if err := h.m.putChunk(httptest.NewRecorder(), req, owner); err == nil {
		t.Fatal("a cancelled request must fail")
	}
	if h.lockCount() != 1 {
		t.Fatalf("cancelled request dropped the lock: %d", h.lockCount())
	}

	// the owner finishes normally and the entry disappears with the upload
	if code, out := a.putChunk(id, data, 100, len(data)-1); code != 200 || out["complete"] != true {
		t.Fatalf("finish: %d %v", code, out)
	}
	if h.lockCount() != 0 {
		t.Fatalf("lock must go with the finished upload: %d", h.lockCount())
	}
}

func (h *harness) ownerOf(uploadID string) string {
	h.t.Helper()
	var u string
	if err := h.app.DB.R.QueryRow(`SELECT user_id FROM uploads WHERE id = ?`, uploadID).Scan(&u); err != nil {
		h.t.Fatal(err)
	}
	return u
}

func TestOpenUploadLimitIsEnforcedAtomically(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	const tries = maxOpenUpload + 40
	codes := make([]int, tries)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "f.png", "size": 10})
		}()
	}
	wg.Wait()
	ok, limited := 0, 0
	for _, code := range codes {
		switch code {
		case 200:
			ok++
		case 429:
			limited++
		}
	}
	if ok != maxOpenUpload || limited != 40 {
		t.Fatalf("parallel creates must stop exactly at the limit: %d ok, %d limited", ok, limited)
	}
	if h.count(`SELECT COUNT(*) FROM uploads`) != maxOpenUpload {
		t.Fatal("rows")
	}
	entries, _ := os.ReadDir(h.m.tmpDir("uploads"))
	if len(entries) != maxOpenUpload {
		t.Fatalf("rejected creates must not leave part files behind: %d", len(entries))
	}
}

// ---- plausible items --------------------------------------------------------------------------------

// blockOnceTC blocks the first ffmpeg run until its context is cancelled.
type blockOnceTC struct {
	fakeTC
	once    sync.Once
	started chan struct{}
	ended   chan error
}

func (b *blockOnceTC) Run(ctx context.Context, args []string, p func(int64)) error {
	first := false
	b.once.Do(func() { first = true; close(b.started) })
	if first {
		<-ctx.Done()
		b.ended <- ctx.Err()
		return ctx.Err()
	}
	return b.fakeTC.Run(ctx, args, p)
}

func TestReplacementCancelsTheObsoleteRunningJob(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	tc := &blockOnceTC{started: make(chan struct{}), ended: make(chan error, 1)}
	h.m.tc = tc
	a := c.mustUpload("v1.png", pngBytes(64, 48, 1), nil)["id"].(string)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.m.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	select {
	case <-tc.started:
	case <-time.After(5 * time.Second):
		t.Fatal("job did not start")
	}
	if code, out := c.upload("v2.png", pngBytes(64, 48, 2), nil, &a); code != 200 {
		t.Fatalf("replace: %d %v", code, out)
	}
	select {
	case err := <-tc.ended:
		if err == nil {
			t.Fatal("the obsolete ffmpeg run must be cancelled")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the obsolete job keeps running after the replacement")
	}
	// the replacement is processed; the obsolete job is closed as superseded, not failed
	waitFor(t, "revision 2 ready", 5*time.Second, func() bool {
		st, _ := h.assetStatus(a)
		return st == "ready" && h.readyRevision(a) == 2
	})
	if h.count(`SELECT COUNT(*) FROM media_jobs WHERE state = 'failed'`) != 0 {
		t.Fatal("cancelling an obsolete job is not a failure")
	}
	var state, e string
	_ = h.app.DB.R.QueryRow(`SELECT state, error FROM media_jobs WHERE revision = 1`).Scan(&state, &e)
	if state != "done" || !strings.Contains(e, "superseded") {
		t.Fatalf("obsolete job: %s %q", state, e)
	}
	if h.count(`SELECT COUNT(*) FROM media_jobs WHERE reserved_bytes <> 0`) != 0 {
		t.Fatal("reservation of the cancelled job must be released")
	}
}

func TestMaintenanceSweepsStaleFontUploads(t *testing.T) {
	h := newHarness(t)
	dir := h.m.tmpDir("fonts")
	_ = os.MkdirAll(dir, 0o750)
	stale, fresh := filepath.Join(dir, "font-stale.part"), filepath.Join(dir, "font-fresh.part")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-3 * time.Hour)
	_ = os.Chtimes(stale, old, old)
	h.m.cleanupUploads(context.Background())
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale font upload must be swept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a fresh font upload must stay")
	}
}

func TestExternalToolsMayOnlyOpenLocalFiles(t *testing.T) {
	join := func(a []string) string { return " " + strings.Join(a, " ") + " " }
	for name, args := range map[string][]string{
		"ffmpeg":          ffmpegArgs("/in", "/out.mp4", outSpec{Pre: []string{"-ss", "1"}, Args: []string{"-an"}}),
		"ffprobe":         probeArgs("/in"),
		"ffprobe frame":   rotationArgs("/in"),
		"ffmpeg no extra": ffmpegArgs("/in", "/out.jpg", outSpec{}),
	} {
		j := join(args)
		if !strings.Contains(j, " -protocol_whitelist file,pipe ") {
			t.Errorf("%s lacks the protocol whitelist: %s", name, j)
		}
		wl, in := strings.Index(j, "-protocol_whitelist"), strings.Index(j, " -i ")
		if name == "ffmpeg" && wl > in {
			t.Errorf("the whitelist must precede -i, it is an input option: %s", j)
		}
	}
}
