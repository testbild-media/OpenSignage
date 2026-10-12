package media

// Tests against the real ffmpeg/ffprobe binaries. They are skipped when the tools are not
// installed (the default on a plain CI box); on a development or test machine they run the
// complete pipeline: upload → probe → variants → stored blobs.

import (
	"bytes"
	"context"
	"encoding/binary"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type realTools struct{ ffmpeg, ffprobe string }

func requireFFmpeg(t *testing.T) realTools {
	t.Helper()
	ff, err1 := exec.LookPath("ffmpeg")
	fp, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	return realTools{ff, fp}
}

// gen runs ffmpeg to create a test file; it skips the test if an encoder is missing.
func (r realTools) gen(t *testing.T, out string, args ...string) []byte {
	t.Helper()
	full := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)
	full = append(full, out)
	cmd := exec.Command(r.ffmpeg, full...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot generate %s with this ffmpeg build: %v: %s", filepath.Base(out), err, strings.TrimSpace(string(b)))
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (h *harness) useRealTools(r realTools) {
	h.m.tc = &execTranscoder{ffmpeg: r.ffmpeg, ffprobe: r.ffprobe}
}

// probeVariant probes a stored variant of an asset.
func (h *harness) probeVariant(assetID, variant string) (Probe, BlobRef) {
	h.t.Helper()
	ref, err := ResolveVariant(context.Background(), h.app.DB.R, assetID, 0, variant)
	if err != nil || ref.Variant != variant {
		h.t.Fatalf("variant %s of %s: %+v %v", variant, assetID, ref, err)
	}
	p, err := h.m.tc.Probe(context.Background(), h.m.BlobPath(ref.SHA256))
	if err != nil {
		h.t.Fatalf("probe %s: %v", variant, err)
	}
	return p, ref
}

func (h *harness) jobError(assetID string) string {
	var e string
	_ = h.app.DB.R.QueryRow(`SELECT error FROM media_jobs WHERE asset_id = ?`, assetID).Scan(&e)
	return e
}

func TestRealFFmpegImages(t *testing.T) {
	r := requireFFmpeg(t)
	h := newHarness(t)
	h.useRealTools(r)
	c := h.login("operator")
	dir := t.TempDir()

	// huge PNG: downscaled original, 1920 and 512 variants, thumbnail
	big := r.gen(t, filepath.Join(dir, "big.png"), "-f", "lavfi", "-i", "testsrc2=size=4000x2000", "-frames:v", "1")
	a := c.mustUpload("big.png", big, nil)
	id := a["id"].(string)
	h.runJobs()
	if st, e := h.assetStatus(id); st != "ready" {
		t.Fatalf("big.png: %s %s %s", st, e, h.jobError(id))
	}
	for variant, want := range map[string][2]int{"orig": {3840, 1920}, "i1920": {1920, 960}, "i512": {512, 256}, "thumb": {320, 160}} {
		p, ref := h.probeVariant(id, variant)
		if p.Width != want[0] || p.Height != want[1] {
			t.Errorf("%s: %dx%d, want %dx%d", variant, p.Width, p.Height, want[0], want[1])
		}
		if variant == "thumb" && ref.Mime != "image/jpeg" || variant != "thumb" && ref.Mime != "image/png" {
			t.Errorf("%s mime %s", variant, ref.Mime)
		}
	}
	d := c.mustOK("GET", "/api/v1/media/"+id, nil)
	if m := d["meta"].(map[string]any); m["width"].(float64) != 4000 || m["height"].(float64) != 2000 || m["mime"] != "image/png" {
		t.Fatalf("meta: %v", m)
	}
	// the uploaded original is kept untouched in the revision
	res, body := c.req("GET", "/api/v1/media/"+id+"/file?variant=orig&rev=1", nil, nil)
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	if sha(body) == sha(big) {
		t.Fatal("variant=orig of a huge image is the downscaled one")
	}
	if got := h.count(`SELECT COUNT(*) FROM media_revisions WHERE blob_sha256 = ?`, sha(big)); got != 1 {
		t.Fatal("the uploaded original stays in the revision")
	}

	// JPEG photo: no downscaled original, thumbnails stay JPEG
	photo := r.gen(t, filepath.Join(dir, "photo.jpg"), "-f", "lavfi", "-i", "testsrc2=size=2400x1600", "-frames:v", "1", "-q:v", "2")
	pa := c.mustUpload("photo.jpg", photo, nil)["id"].(string)
	h.runJobs()
	if got := strings.Join(h.variantNames(pa, 1), ","); got != "i1920,i512,thumb" {
		t.Fatalf("photo variants: %s", got)
	}
	if p, ref := h.probeVariant(pa, "i1920"); p.Width != 1920 || p.Height != 1280 || ref.Mime != "image/jpeg" {
		t.Errorf("i1920: %dx%d %s", p.Width, p.Height, ref.Mime)
	}

	// a transparent PNG: variants keep alpha, the thumbnail is composited onto grey
	alpha := r.gen(t, filepath.Join(dir, "alpha.png"), "-f", "lavfi", "-i", "color=c=red@0.5:s=640x360,format=rgba", "-frames:v", "1")
	aa := c.mustUpload("alpha.png", alpha, nil)["id"].(string)
	h.runJobs()
	if st, e := h.assetStatus(aa); st != "ready" {
		t.Fatalf("alpha: %s %s %s", st, e, h.jobError(aa))
	}
	_, ref := h.probeVariant(aa, "thumb")
	tb, err := os.ReadFile(h.m.BlobPath(ref.SHA256))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(tb))
	if err != nil {
		t.Fatal(err)
	}
	r8, g8, b8, _ := img.At(img.Bounds().Dx()/2, img.Bounds().Dy()/2).RGBA()
	if r8>>8 < 150 || g8>>8 < 30 || g8>>8 > 110 || b8>>8 > 110 {
		t.Errorf("alpha thumbnail should be red blended with grey, got %d %d %d", r8>>8, g8>>8, b8>>8)
	}

	// an animated GIF keeps its original (no re-encode), only a thumbnail is added
	gif := r.gen(t, filepath.Join(dir, "anim.gif"), "-f", "lavfi", "-i", "testsrc2=size=200x100:rate=5:duration=1")
	gid := c.mustUpload("anim.gif", gif, nil)["id"].(string)
	h.runJobs()
	if st, e := h.assetStatus(gid); st != "ready" {
		t.Fatalf("gif: %s %s %s", st, e, h.jobError(gid))
	}
	if got := strings.Join(h.variantNames(gid, 1), ","); got != "thumb" {
		t.Fatalf("gif variants: %s", got)
	}
	if m := c.mustOK("GET", "/api/v1/media/"+gid, nil)["meta"].(map[string]any); m["animated"] != true || m["width"].(float64) != 200 {
		t.Fatalf("gif meta: %v", m)
	}

	// a broken image fails cleanly with a message
	bad := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x42}, 500)...)
	ba := c.mustUpload("bad.png", bad, nil)["id"].(string)
	h.runJobs()
	if st, e := h.assetStatus(ba); st != "failed" || e == "" {
		t.Fatalf("corrupt png: %s %q", st, e)
	}
}

func TestRealFFmpegHonoursExifOrientation(t *testing.T) {
	r := requireFFmpeg(t)
	h := newHarness(t)
	h.useRealTools(r)
	c := h.login("operator")
	dir := t.TempDir()
	base := r.gen(t, filepath.Join(dir, "base.jpg"), "-f", "lavfi", "-i", "testsrc2=size=1000x500", "-frames:v", "1", "-q:v", "3")
	// insert an EXIF APP1 segment with Orientation = 6 (rotate 90° clockwise) after SOI
	var tiff bytes.Buffer
	tiff.WriteString("MM\x00\x2a\x00\x00\x00\x08")
	_ = binary.Write(&tiff, binary.BigEndian, uint16(1))
	_ = binary.Write(&tiff, binary.BigEndian, uint16(0x0112))
	_ = binary.Write(&tiff, binary.BigEndian, uint16(3))
	_ = binary.Write(&tiff, binary.BigEndian, uint32(1))
	_ = binary.Write(&tiff, binary.BigEndian, uint16(6))
	tiff.Write([]byte{0, 0, 0, 0, 0, 0})
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	seg := append([]byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}, payload...)
	rotated := append(append(append([]byte{}, base[:2]...), seg...), base[2:]...)
	if _, err := jpeg.DecodeConfig(bytes.NewReader(rotated)); err != nil {
		t.Fatalf("test jpeg: %v", err)
	}

	id := c.mustUpload("phone.jpg", rotated, nil)["id"].(string)
	h.runJobs()
	if st, e := h.assetStatus(id); st != "ready" {
		t.Fatalf("%s %s", st, e)
	}
	m := c.mustOK("GET", "/api/v1/media/"+id, nil)["meta"].(map[string]any)
	if m["width"].(float64) != 500 || m["height"].(float64) != 1000 {
		t.Fatalf("displayed size must be 500x1000 (portrait), meta: %v", m)
	}
	p, _ := h.probeVariant(id, "i512")
	if p.Width >= p.Height {
		t.Fatalf("i512 must be portrait like the photo is displayed, got %dx%d", p.Width, p.Height)
	}
}

func TestRealFFmpegVideo(t *testing.T) {
	r := requireFFmpeg(t)
	h := newHarness(t)
	h.useRealTools(r)
	c := h.login("operator")
	dir := t.TempDir()
	sine := []string{"-f", "lavfi", "-i", "sine=frequency=440:duration=2"}

	// conforming source (H.264 yuv420p ≤1080p ≤30fps): remuxed, not re-encoded
	clip := r.gen(t, filepath.Join(dir, "clip.mp4"), append(append([]string{"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25:duration=2"}, sine...),
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest")...)
	a := c.mustUpload("clip.mp4", clip, nil)
	id := a["id"].(string)
	if a["kind"] != "video" {
		t.Fatalf("kind %v", a["kind"])
	}
	h.runJobs()
	if st, e := h.assetStatus(id); st != "ready" {
		t.Fatalf("clip: %s %s %s", st, e, h.jobError(id))
	}
	if got := strings.Join(h.variantNames(id, 1), ","); got != "poster,thumb,v1080" {
		t.Fatalf("clip variants: %s", got)
	}
	p, ref := h.probeVariant(id, "v1080")
	if p.VideoCodec != "h264" || p.Width != 640 || p.Height != 360 || p.AudioCodec != "aac" || p.PixFmt != "yuv420p" || ref.Mime != "video/mp4" {
		t.Errorf("v1080: %+v %s", p, ref.Mime)
	}
	if !moovBeforeMdat(t, h.m.BlobPath(ref.SHA256)) {
		t.Error("v1080 must be faststart (moov before mdat)")
	}
	if p, ref := h.probeVariant(id, "poster"); p.Width != 640 || ref.Mime != "image/jpeg" {
		t.Errorf("poster: %+v", p)
	}
	if p, _ := h.probeVariant(id, "thumb"); p.Width != 320 || p.Height != 180 {
		t.Errorf("thumb: %dx%d", p.Width, p.Height)
	}
	d := c.mustOK("GET", "/api/v1/media/"+id, nil)["meta"].(map[string]any)
	if d["durationMs"].(float64) < 1900 || d["durationMs"].(float64) > 2300 || d["videoCodec"] != "h264" || d["hasAudio"] != true {
		t.Fatalf("meta: %v", d)
	}
	// 1080p @ 50 fps: v1080 keeps 50 fps (H.264 level 4.2 is copied), the
	// v720 proxy has 25 fps
	fast := r.gen(t, filepath.Join(dir, "fast.mp4"), "-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=50:duration=1",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p")
	fid := c.mustUpload("fast.mp4", fast, nil)["id"].(string)
	h.runJobs()
	if st, e := h.assetStatus(fid); st != "ready" {
		t.Fatalf("fast: %s %s %s", st, e, h.jobError(fid))
	}
	p, _ = h.probeVariant(fid, "v1080")
	if p.VideoCodec != "h264" || p.PixFmt != "yuv420p" || p.Width != 1920 || p.Height != 1080 || p.FPS < 49.9 || p.FPS > 50.1 {
		t.Errorf("fast v1080: %+v", p)
	}
	if p, _ = h.probeVariant(fid, "v720"); p.Width != 1280 || p.Height != 720 || p.FPS < 24.9 || p.FPS > 25.1 || p.Profile != "High" {
		t.Errorf("fast v720: %+v", p)
	}
	if q, _ := h.probeVariant(fid, "v1080h"); q.VideoCodec != "h264" || q.Width != 1920 || q.Height != 1080 || q.FPS < 24.9 || q.FPS > 25.1 || q.Level > 41 {
		t.Errorf("fast v1080h (Raspberry Pi 4): %+v", q)
	}
	if p.HasAudio {
		t.Error("silent source stays silent")
	}

	// MPEG-4 in Matroska (no H.264): transcoded, odd size gets even dimensions
	mkv := r.gen(t, filepath.Join(dir, "odd.mkv"), append([]string{"-f", "lavfi", "-i", "testsrc2=size=853x479:rate=25:duration=1"}, sine...)...)
	mid := c.mustUpload("odd.mkv", mkv, nil)
	if mid["kind"] != "video" {
		t.Fatalf("mkv kind: %v", mid["kind"])
	}
	h.runJobs()
	if st, e := h.assetStatus(mid["id"].(string)); st != "ready" {
		t.Fatalf("mkv: %s %s %s", st, e, h.jobError(mid["id"].(string)))
	}
	if p, _ = h.probeVariant(mid["id"].(string), "v1080"); p.VideoCodec != "h264" || p.Width%2 != 0 || p.Height%2 != 0 || p.AudioCodec != "aac" {
		t.Errorf("mkv v1080: %+v", p)
	}

	// a file that merely starts like an MP4 fails with a readable message
	junk := append([]byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2mp41"), bytes.Repeat([]byte{0x07}, 2000)...)
	jid := c.mustUpload("junk.mp4", junk, nil)["id"].(string)
	h.runJobs()
	st, e := h.assetStatus(jid)
	if st != "failed" || !strings.HasPrefix(e, "probe_failed:") {
		t.Fatalf("junk mp4: %s %q", st, e)
	}
}

// moovBeforeMdat reports whether the movie box precedes the media data (faststart).
func moovBeforeMdat(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var off int64
	for i := 0; i < 20; i++ {
		var hdr [8]byte
		if _, err := f.ReadAt(hdr[:], off); err != nil {
			return false
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		switch string(hdr[4:]) {
		case "moov":
			return true
		case "mdat":
			return false
		}
		if size < 8 {
			return false
		}
		off += size
	}
	return false
}

func TestRealFFmpegAudio(t *testing.T) {
	r := requireFFmpeg(t)
	h := newHarness(t)
	h.useRealTools(r)
	c := h.login("operator")
	dir := t.TempDir()

	wav := r.gen(t, filepath.Join(dir, "tone.wav"), "-f", "lavfi", "-i", "sine=frequency=880:duration=2")
	a := c.mustUpload("tone.wav", wav, nil)
	id := a["id"].(string)
	if a["kind"] != "audio" {
		t.Fatalf("kind %v", a["kind"])
	}
	h.runJobs()
	if st, e := h.assetStatus(id); st != "ready" {
		t.Fatalf("wav: %s %s %s", st, e, h.jobError(id))
	}
	p, ref := h.probeVariant(id, "a")
	if p.AudioCodec != "aac" || ref.Mime != "audio/mp4" || p.HasVideo {
		t.Errorf("wav → m4a: %+v %s", p, ref.Mime)
	}
	if got := strings.Join(h.variantNames(id, 1), ","); got != "a" {
		t.Errorf("audio variants: %s", got)
	}

	mp3 := r.gen(t, filepath.Join(dir, "tone.mp3"), "-f", "lavfi", "-i", "sine=frequency=330:duration=2", "-c:a", "libmp3lame")
	mid := c.mustUpload("tone.mp3", mp3, nil)
	h.runJobs()
	ref2, err := ResolveVariant(context.Background(), h.app.DB.R, mid["id"].(string), 0, "a")
	if err != nil || ref2.SHA256 != mid["sha256"] {
		t.Errorf("mp3 is used as it is: %+v %v", ref2, err)
	}
}
