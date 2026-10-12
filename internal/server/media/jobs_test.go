package media

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"opensignage/internal/server/core"
)

// mp4Bytes is just enough of an ISO-BMFF header to be detected as video/mp4.
func mp4Bytes(tag string) []byte {
	return append([]byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2mp41"), []byte(tag+strings.Repeat("-", 400))...)
}

func (h *harness) variantNames(assetID string, rev int) []string {
	h.t.Helper()
	rows, err := h.app.DB.R.Query(`SELECT variant FROM media_variants WHERE asset_id = ? AND revision = ? ORDER BY variant`, assetID, rev)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		out = append(out, v)
	}
	return out
}

func TestJobProducesVariantsAndMetadata(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	sub := h.app.Bus.Subscribe(256)
	defer sub.Close()
	h.tc.probe = Probe{HasVideo: true, VideoCodec: "mjpeg", PixFmt: "yuvj420p", Width: 4000, Height: 3000}
	a := c.mustUpload("photo.jpg", jpegBytes(64, 48), nil)
	id := a["id"].(string)
	if a["status"] != "processing" {
		t.Fatalf("status: %v", a["status"])
	}
	if h.runJobs() != 1 {
		t.Fatal("one job expected")
	}
	if st, e := h.assetStatus(id); st != "ready" || e != "" {
		t.Fatalf("status %s %s", st, e)
	}
	// 4000×3000: downscaled orig (> 3840), i1920, i512 and the thumbnail
	got := strings.Join(h.variantNames(id, 1), ",")
	if got != "i1920,i512,orig,thumb" {
		t.Fatalf("variants: %s", got)
	}
	d := c.mustOK("GET", "/api/v1/media/"+id, nil)
	meta := d["meta"].(map[string]any)
	if meta["width"].(float64) != 4000 || meta["height"].(float64) != 3000 || meta["videoCodec"] != "mjpeg" {
		t.Fatalf("meta: %v", meta)
	}
	if d["job"].(map[string]any)["state"] != "done" || len(d["variantInfo"].([]any)) != 4 {
		t.Fatalf("details: %v", d)
	}
	// every ffmpeg call is non-interactive and carries a scale filter that never upscales
	for _, run := range h.tc.runs {
		joined := strings.Join(run, " ")
		if !strings.Contains(joined, "-nostdin") || !strings.Contains(joined, "force_original_aspect_ratio=decrease") {
			t.Fatalf("ffmpeg args: %s", joined)
		}
	}
	// a progress event for "ready" was published
	ready := false
	deadline := time.After(time.Second)
	for !ready {
		select {
		case e := <-sub.C:
			if e.Topic == core.TopicMedia && e.Kind == "progress" && e.ID == id {
				if m, ok := e.Data.(map[string]any); ok && m["status"] == "ready" {
					ready = true
				}
			}
		case <-deadline:
			t.Fatal("no ready event")
		}
	}
	// ResolveVariant: fallbacks and the downscaled original
	ref, err := ResolveVariant(context.Background(), h.app.DB.R, id, 0, "orig")
	if err != nil || ref.Variant != "orig" {
		t.Fatalf("orig: %+v %v", ref, err)
	}
	if ref2, err := ResolveVariant(context.Background(), h.app.DB.R, id, 0, "v720"); !errors.Is(err, ErrNoVariant) {
		t.Fatalf("v720 on an image: %+v %v", ref2, err)
	}
}

func TestSmallImageGetsOnlyThumbnail(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	a := c.mustUpload("small.png", pngBytes(64, 48, 1), nil)
	h.runJobs()
	if got := strings.Join(h.variantNames(a["id"].(string), 1), ","); got != "thumb" {
		t.Fatalf("variants: %s", got)
	}
	// ResolveVariant falls back to the original for variants that were not needed
	ref, err := ResolveVariant(context.Background(), h.app.DB.R, a["id"].(string), 0, "i512")
	if err != nil || ref.Variant != "orig" || ref.SHA256 != a["sha256"] {
		t.Fatalf("fallback: %+v %v", ref, err)
	}
	// and the file endpoint serves thumbnails with range and cache headers
	res, body := c.req("GET", "/api/v1/media/"+a["id"].(string)+"/file?variant=thumb&rev=1", nil, nil)
	if res.StatusCode != 200 || !strings.HasPrefix(string(body), "\xff\xd8\xff") {
		t.Fatalf("thumb: %d", res.StatusCode)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "private, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control with rev: %q", cc)
	}
	etag := res.Header.Get("ETag")
	if etag == "" {
		t.Fatal("ETag")
	}
	res, _ = c.req("GET", "/api/v1/media/"+a["id"].(string)+"/file?variant=thumb&rev=1", nil, map[string]string{"If-None-Match": etag})
	if res.StatusCode != 304 {
		t.Fatalf("conditional request: %d", res.StatusCode)
	}
	res, body = c.req("GET", "/api/v1/media/"+a["id"].(string)+"/file", nil, map[string]string{"Range": "bytes=0-9"})
	if res.StatusCode != 206 || len(body) != 10 || res.Header.Get("Content-Range") == "" {
		t.Fatalf("range: %d %d", res.StatusCode, len(body))
	}
	if cc := res.Header.Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Fatalf("no rev → must not be immutable: %q", cc)
	}
	if res, _ = c.req("GET", "/api/v1/media/"+a["id"].(string)+"/file?variant=bogus", nil, nil); res.StatusCode != 422 {
		t.Fatalf("bad variant: %d", res.StatusCode)
	}
	if res, _ = c.req("GET", "/api/v1/media/"+a["id"].(string)+"/file?variant=v1080", nil, nil); res.StatusCode != 404 {
		t.Fatalf("missing variant: %d", res.StatusCode)
	}
	if res, _ = c.req("GET", "/api/v1/media/nope/file", nil, nil); res.StatusCode != 404 {
		t.Fatalf("unknown asset: %d", res.StatusCode)
	}
	// role "user" must not read arbitrary media, neither files nor thumbnails nor upload state
	u := h.login("user")
	for _, path := range []string{"/file", "/file?variant=thumb", "/file?variant=display"} {
		if res, _ = u.req("GET", "/api/v1/media/"+a["id"].(string)+path, nil, nil); res.StatusCode != 403 {
			t.Fatalf("user role must not read %s: %d", path, res.StatusCode)
		}
	}
	// nor list or upload
	if code, _ := u.do("GET", "/api/v1/media", nil); code != 403 {
		t.Fatalf("user listing: %d", code)
	}
	// a blob that vanished from disk is a clean 404, not a 500
	h.exec(`DELETE FROM media_variants`)
	h.exec(`INSERT OR IGNORE INTO blobs(sha256, size, mime, created_at) VALUES (?, 1, 'image/png', 'x')`, strings.Repeat("0", 64))
	h.exec(`UPDATE media_assets SET blob_sha256 = ? WHERE id = ?`, strings.Repeat("0", 64), a["id"])
	h.exec(`UPDATE media_revisions SET blob_sha256 = ? WHERE asset_id = ?`, strings.Repeat("0", 64), a["id"])
	res, body = c.req("GET", "/api/v1/media/"+a["id"].(string)+"/file", nil, nil)
	if res.StatusCode != 404 || !strings.Contains(string(body), "file_missing") {
		t.Fatalf("missing blob: %d %s", res.StatusCode, body)
	}
}

func TestFailedJobIsNotRetriedAndCanBeReprocessed(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	h.tc.runErr = errors.New("ffmpeg failed: moov atom not found")
	a := c.mustUpload("broken.png", pngBytes(64, 48, 2), nil)
	id := a["id"].(string)

	if h.runJobs() != 1 {
		t.Fatal("the job runs once")
	}
	st, e := h.assetStatus(id)
	if st != "failed" || !strings.HasPrefix(e, "ffmpeg_failed:") || !strings.Contains(e, "moov atom") {
		t.Fatalf("asset must carry the error: %s %q", st, e)
	}
	var js, je string
	_ = h.app.DB.R.QueryRow(`SELECT state, error FROM media_jobs WHERE asset_id = ?`, id).Scan(&js, &je)
	if js != "failed" || je != e {
		t.Fatalf("job: %s %q", js, je)
	}
	runs := h.tc.runCount()
	if h.runJobs() != 0 || h.tc.runCount() != runs {
		t.Fatal("a failed job must NOT be retried automatically")
	}
	if h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'media.processing_failed'`) != 1 {
		t.Fatal("failure is audited")
	}

	// the user fixes the cause and re-queues
	h.tc.runErr = nil
	out := c.mustOK("POST", "/api/v1/media/"+id+"/reprocess", nil)
	if out["status"] != "processing" {
		t.Fatalf("reprocess: %v", out)
	}
	if code, o := c.do("POST", "/api/v1/media/"+id+"/reprocess", nil); code != 409 || errCode(o) != "job_active" {
		t.Fatalf("double reprocess: %d %v", code, o)
	}
	if h.runJobs() != 1 {
		t.Fatal("reprocess queues exactly one job")
	}
	if st, e := h.assetStatus(id); st != "ready" || e != "" {
		t.Fatalf("after reprocess: %s %s", st, e)
	}
	if h.count(`SELECT COUNT(*) FROM media_jobs WHERE asset_id = ?`, id) != 1 {
		t.Fatal("old job rows of the revision are replaced")
	}
	if code, _ := c.do("POST", "/api/v1/media/nope/reprocess", nil); code != 404 {
		t.Fatalf("unknown asset: %d", code)
	}
}

func TestProbeFailureAndMissingFFmpeg(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	h.tc.probeErr = errors.New("ffprobe is not installed (ffprobe)")
	a := c.mustUpload("x.png", pngBytes(32, 32, 3), nil)
	h.runJobs()
	st, e := h.assetStatus(a["id"].(string))
	if st != "failed" || !strings.HasPrefix(e, "probe_failed:") || !strings.Contains(e, "not installed") {
		t.Fatalf("%s %q", st, e)
	}
}

func TestJobDiskGuard(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	a := c.mustUpload("x.png", pngBytes(32, 32, 4), nil)
	h.m.diskFn = func(string) (uint64, uint64, error) { return 10 << 30, 100 << 20, nil } // 100 MiB free < 500 MiB reserve
	h.runJobs()
	st, e := h.assetStatus(a["id"].(string))
	if st != "failed" || !strings.HasPrefix(e, "disk_full:") {
		t.Fatalf("job must fail up front with disk_full: %s %q", st, e)
	}
	if h.tc.runCount() != 0 {
		t.Fatal("ffmpeg must not start when the disk is too full")
	}
	// space is back: reprocess works
	h.m.diskFn = func(string) (uint64, uint64, error) { return 10 << 30, 5 << 30, nil }
	c.mustOK("POST", "/api/v1/media/"+a["id"].(string)+"/reprocess", nil)
	h.runJobs()
	if st, _ := h.assetStatus(a["id"].(string)); st != "ready" {
		t.Fatalf("after freeing space: %s", st)
	}
}

func TestRunningJobsReserveDiskSpace(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	h.m.diskFn = func(string) (uint64, uint64, error) { return 10 << 30, 2 << 30, nil }
	a := c.mustUpload("x.png", pngBytes(32, 32, 5), nil)
	h.exec(`UPDATE media_jobs SET state = 'running', reserved_bytes = ? WHERE asset_id = ?`, int64(1500<<20), a["id"])
	// 2 GiB free − 1.5 GiB reserved = 512 MiB: a 100 MiB upload needs 800 MiB
	if code, out := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "y.mp4", "size": 100 << 20}); code != 507 || errCode(out) != "disk_full" {
		t.Fatalf("reservation must count: %d %v", code, out)
	}
}

func TestInterruptedJobsAreRequeuedOrFailed(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	a1 := c.mustUpload("a.png", pngBytes(32, 32, 6), nil)["id"].(string)
	a2 := c.mustUpload("b.png", pngBytes(32, 32, 7), nil)["id"].(string)
	h.exec(`UPDATE media_jobs SET state = 'running', attempts = 1, reserved_bytes = 5 WHERE asset_id = ?`, a1)
	h.exec(`UPDATE media_jobs SET state = 'running', attempts = 3, reserved_bytes = 5 WHERE asset_id = ?`, a2)
	h.m.requeueInterrupted(context.Background())
	var s1, s2 string
	_ = h.app.DB.R.QueryRow(`SELECT state FROM media_jobs WHERE asset_id = ?`, a1).Scan(&s1)
	_ = h.app.DB.R.QueryRow(`SELECT state FROM media_jobs WHERE asset_id = ?`, a2).Scan(&s2)
	if s1 != "queued" || s2 != "failed" {
		t.Fatalf("restart handling: %s %s", s1, s2)
	}
	if st, _ := h.assetStatus(a2); st != "failed" {
		t.Fatalf("asset of the poison job: %s", st)
	}
	if h.count(`SELECT COUNT(*) FROM media_jobs WHERE reserved_bytes <> 0`) != 0 {
		t.Fatal("reservations are released")
	}
	if h.runJobs() != 1 {
		t.Fatal("the requeued job runs")
	}
}

func TestPanickingTranscoderIsContained(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	h.m.tc = panicTC{}
	a := c.mustUpload("x.png", pngBytes(32, 32, 8), nil)["id"].(string)
	h.runJobs()
	if st, e := h.assetStatus(a); st != "failed" || e == "" {
		t.Fatalf("panic must become a failed job: %s %q", st, e)
	}
}

type panicTC struct{}

func (panicTC) Probe(context.Context, string) (Probe, error)       { panic("boom") }
func (panicTC) ProbeRotation(context.Context, string) (int, error) { panic("boom") }
func (panicTC) Run(context.Context, []string, func(int64)) error   { panic("boom") }

func TestReplaceKeepsIDAndBumpsRevision(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	sub := h.app.Bus.Subscribe(256)
	defer sub.Close()
	first := pngBytes(64, 48, 1)
	a := c.mustUpload("logo.png", first, nil)
	id := a["id"].(string)
	h.runJobs()
	if got := strings.Join(h.variantNames(id, 1), ","); got != "thumb" {
		t.Fatalf("rev 1 variants: %s", got)
	}

	second := pngBytes(64, 48, 2)
	code, out := c.upload("new-logo.png", second, nil, &id)
	if code != 200 || out["complete"] != true {
		t.Fatalf("replace: %d %v", code, out)
	}
	asset := out["asset"].(map[string]any)
	if asset["id"] != id || asset["revision"].(float64) != 2 || asset["status"] != "processing" || asset["name"] != "logo.png" || asset["sha256"] != sha(second) {
		t.Fatalf("replaced asset: %v", asset)
	}
	if !waitTopic(sub, core.TopicContent, time.Second) {
		t.Fatal("replace must publish a content event")
	}
	// while revision 2 is processing, the old thumbnail keeps being served
	res, _ := c.req("GET", "/api/v1/media/"+id+"/file?variant=thumb", nil, nil)
	if res.StatusCode != 200 {
		t.Fatalf("old thumbnail during processing: %d", res.StatusCode)
	}
	h.runJobs()
	if st, _ := h.assetStatus(id); st != "ready" {
		t.Fatal("rev 2 ready")
	}
	if len(h.variantNames(id, 1)) != 1 || len(h.variantNames(id, 2)) != 1 {
		t.Fatal("variants are kept per revision")
	}
	d := c.mustOK("GET", "/api/v1/media/"+id, nil)
	revs := d["revisions"].([]any)
	if len(revs) != 2 || revs[0].(map[string]any)["revision"].(float64) != 2 || revs[0].(map[string]any)["current"] != true {
		t.Fatalf("revisions: %v", revs)
	}
	// ?rev= addresses a specific revision
	r1, b1 := c.req("GET", "/api/v1/media/"+id+"/file?rev=1", nil, nil)
	r2, b2 := c.req("GET", "/api/v1/media/"+id+"/file?rev=2", nil, nil)
	if r1.StatusCode != 200 || r2.StatusCode != 200 || sha(b1) != sha(first) || sha(b2) != sha(second) {
		t.Fatal("revision addressing")
	}
	if r1.Header.Get("ETag") == r2.Header.Get("ETag") {
		t.Fatal("different revisions, different validators")
	}
	// identical content: no new revision
	code, out = c.upload("again.png", second, nil, &id)
	if code != 200 || out["asset"].(map[string]any)["revision"].(float64) != 2 {
		t.Fatalf("identical replacement: %d %v", code, out)
	}
	if h.count(`SELECT COUNT(*) FROM media_revisions WHERE asset_id = ?`, id) != 2 {
		t.Fatal("no duplicate revision")
	}
	// another media kind is refused
	code, out = c.upload("clip.mp4", mp4Bytes("x"), nil, &id)
	if code != 409 || errCode(out) != "kind_mismatch" {
		t.Fatalf("kind mismatch: %d %v", code, out)
	}
	if h.count(`SELECT COUNT(*) FROM uploads`) != 0 {
		t.Fatal("a refused replacement must not leave the upload behind")
	}
	// replacing something in the trash or unknown is refused
	c.mustOK("DELETE", "/api/v1/media/"+id, nil)
	if code, _ = c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "x.png", "size": 10, "replaceAssetId": id}); code != 422 {
		t.Fatalf("replace trashed asset: %d", code)
	}
	if h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'media.replaced'`) != 1 {
		t.Fatal("replace is audited")
	}
}

func TestSupersededJobIsSkipped(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	a := c.mustUpload("a.png", pngBytes(64, 48, 1), nil)["id"].(string)
	// the replacement arrives before the first job ran
	code, _ := c.upload("b.png", pngBytes(64, 48, 2), nil, &a)
	if code != 200 {
		t.Fatal("replace")
	}
	if h.runJobs() != 2 {
		t.Fatal("both jobs are claimed")
	}
	if h.count(`SELECT COUNT(*) FROM media_jobs WHERE state = 'done'`) != 2 {
		t.Fatal("both done")
	}
	if len(h.variantNames(a, 1)) != 0 || len(h.variantNames(a, 2)) != 1 {
		t.Fatalf("only the current revision is processed: %v %v", h.variantNames(a, 1), h.variantNames(a, 2))
	}
}

func TestVideoPipelineWithFakeFFmpeg(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	h.tc.probe = Probe{Container: "matroska,webm", HasVideo: true, VideoCodec: "hevc", PixFmt: "yuv420p10le", Width: 3840, Height: 2160, FPS: 60,
		HasAudio: true, AudioCodec: "opus", DurationMs: 8000}
	a := c.mustUpload("promo.mp4", mp4Bytes("promo"), nil)
	id := a["id"].(string)
	if a["kind"] != "video" {
		t.Fatalf("kind: %v", a["kind"])
	}
	h.runJobs()
	if got := strings.Join(h.variantNames(id, 1), ","); got != "poster,thumb,v1080,v1080h,v720" {
		t.Fatalf("variants: %s", got)
	}
	var v1080, v1080h, v720 string
	for _, r := range h.tc.runs {
		switch {
		case strings.HasSuffix(r[len(r)-1], "v1080.mp4"):
			v1080 = strings.Join(r, " ")
		case strings.HasSuffix(r[len(r)-1], "v1080h.mp4"):
			v1080h = strings.Join(r, " ")
		case strings.HasSuffix(r[len(r)-1], "v720.mp4"):
			v720 = strings.Join(r, " ")
		}
	}
	// Frame rates: v1080 keeps the 60 fps (level 4.2), v1080h (Raspberry Pi 4) and the v720
	// proxy have 30 fps.
	for _, want := range []string{"libx264", "yuv420p", "+faststart", "aac", "128k", "-level:v 4.2", "-nostdin"} {
		if !strings.Contains(v1080, want) {
			t.Errorf("v1080 command misses %q: %s", want, v1080)
		}
	}
	if strings.Contains(v1080, "fps=") || !strings.Contains(v720, "fps=30.000") || !strings.Contains(v1080h, "fps=30.000") || !strings.Contains(v1080h, "min(iw,1920)") {
		t.Errorf("frame rates: v1080 %s / v1080h %s / v720 %s", v1080, v1080h, v720)
	}
	// the same file again reuses the finished variants instead of transcoding twice
	runs := h.tc.runCount()
	b := c.mustUpload("promo-copy.mp4", mp4Bytes("promo"), nil)
	h.runJobs()
	if h.tc.runCount() != runs {
		t.Fatal("an identical upload must not be transcoded again")
	}
	if got := strings.Join(h.variantNames(b["id"].(string), 1), ","); got != "poster,thumb,v1080,v1080h,v720" {
		t.Fatalf("twin variants: %s", got)
	}
	if st, _ := h.assetStatus(b["id"].(string)); st != "ready" {
		t.Fatal("twin ready")
	}
}

// Videos processed before v1080h existed get it once at the next start, and a twin without it
// is not copied. Slower or smaller videos and cleaned-up ones are left alone.
func TestHalfRateVariantForOlderVideos(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	h.tc.probe = Probe{Container: "mov,mp4,m4a,3gp,3g2,mj2", HasVideo: true, VideoCodec: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080,
		FPS: 50, Level: 42, DurationMs: 4000}
	old := c.mustUpload("fast.mp4", mp4Bytes("fast"), nil)["id"].(string)
	h.runJobs()
	h.exec(`DELETE FROM media_variants WHERE asset_id = ? AND variant = 'v1080h'`, old) // as made by an older version

	runs := h.tc.runCount()
	twin := c.mustUpload("fast-copy.mp4", mp4Bytes("fast"), nil)["id"].(string)
	h.runJobs()
	if h.tc.runCount() == runs {
		t.Fatal("a twin without v1080h must not be copied")
	}
	if got := strings.Join(h.variantNames(twin, 1), ","); got != "poster,thumb,v1080,v1080h,v720" {
		t.Fatalf("twin variants: %s", got)
	}

	// not touched: 30 fps, 720p at 60 fps (the proxy is enough), cleaned-up proxy, trash
	upload := func(name string) string { // the fake probe answers for the job that runs now
		id := c.mustUpload(name, mp4Bytes(name), nil)["id"].(string)
		h.runJobs()
		return id
	}
	h.tc.probe.FPS = 30
	slow := upload("slow.mp4")
	h.tc.probe.FPS, h.tc.probe.Width, h.tc.probe.Height = 60, 1280, 720
	small := upload("small.mp4")
	h.tc.probe.Width, h.tc.probe.Height = 1920, 1080
	cleaned := upload("cleaned.mp4")
	trashed := upload("trashed.mp4")
	h.exec(`DELETE FROM media_variants WHERE asset_id IN (?, ?) AND variant = 'v1080h'`, cleaned, trashed)
	h.exec(`DELETE FROM media_variants WHERE asset_id = ? AND variant = 'v720'`, cleaned)
	h.exec(`UPDATE media_assets SET trashed_at = ? WHERE id = ?`, "2026-10-12T00:00:00Z", trashed)

	h.m.queueHalfRate(context.Background())
	if st, _ := h.assetStatus(old); st != "processing" {
		t.Fatalf("old 50 fps video is processed again: %s", st)
	}
	if n := h.count(`SELECT COUNT(*) FROM media_jobs WHERE state = 'queued'`); n != 1 {
		t.Fatalf("exactly one video is queued, got %d", n)
	}
	h.runJobs()
	if st, _ := h.assetStatus(old); st != "ready" {
		t.Fatalf("old video ready again: %s", st)
	}
	if got := strings.Join(h.variantNames(old, 1), ","); got != "poster,thumb,v1080,v1080h,v720" {
		t.Fatalf("old video variants: %s", got)
	}
	for id, want := range map[string]string{slow: "poster,thumb,v1080,v720", small: "poster,thumb,v1080,v720", cleaned: "poster,thumb,v1080"} {
		if got := strings.Join(h.variantNames(id, 1), ","); got != want {
			t.Errorf("%s: %s, want %s", id, got, want)
		}
	}
	h.m.queueHalfRate(context.Background()) // the next start finds nothing
	if n := h.count(`SELECT COUNT(*) FROM media_jobs WHERE state = 'queued'`); n != 0 {
		t.Fatalf("nothing to do at the next start, got %d jobs", n)
	}
}

func TestAudioOnlyContainerIsReclassified(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	h.tc.probe = Probe{Container: "mov,mp4,m4a,3gp,3g2,mj2", HasAudio: true, AudioCodec: "mp3", DurationMs: 3000}
	a := c.mustUpload("jingle.mp4", mp4Bytes("jingle"), nil)
	id := a["id"].(string)
	if a["kind"] != "video" {
		t.Fatalf("by content an mp4 is video until probed: %v", a["kind"])
	}
	h.runJobs()
	var kind string
	_ = h.app.DB.R.QueryRow(`SELECT kind FROM media_assets WHERE id = ?`, id).Scan(&kind)
	if kind != "audio" {
		t.Fatalf("kind after probe: %s", kind)
	}
	// MP3 is used as it is: the "a" variant is the original blob, no ffmpeg run
	if h.tc.runCount() != 0 {
		t.Fatal("no transcode for mp3")
	}
	ref, err := ResolveVariant(context.Background(), h.app.DB.R, id, 0, "a")
	if err != nil || ref.SHA256 != a["sha256"] {
		t.Fatalf("audio variant: %+v %v", ref, err)
	}
}
