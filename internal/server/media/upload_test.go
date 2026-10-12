package media

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseContentRange(t *testing.T) {
	cases := []struct {
		in         string
		start, end int64
		total      int64
		query      bool
		bad        bool
	}{
		{"bytes 0-99/1000", 0, 99, 1000, false, false},
		{"bytes 100-199/1000", 100, 199, 1000, false, false},
		{"bytes */1000", 0, 0, 1000, true, false},
		{"bytes 5-4/10", 0, 0, 0, false, true},
		{"bytes -5/10", 0, 0, 0, false, true},
		{"bytes 0-9/0", 0, 0, 0, false, true},
		{"items 0-9/10", 0, 0, 0, false, true},
		{"", 0, 0, 0, false, true},
		{"bytes 0-9", 0, 0, 0, false, true},
		{"bytes a-b/10", 0, 0, 0, false, true},
	}
	for _, c := range cases {
		s, e, tot, q, err := parseContentRange(c.in)
		if (err != nil) != c.bad {
			t.Errorf("%q: err=%v want bad=%v", c.in, err, c.bad)
			continue
		}
		if !c.bad && (s != c.start || e != c.end || tot != c.total || q != c.query) {
			t.Errorf("%q: got %d %d %d %v", c.in, s, e, tot, q)
		}
	}
}

func TestChunkedUploadResume(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	data := pngBytes(40, 30, 1)
	if len(data) < 250 {
		t.Fatalf("test image too small: %d", len(data))
	}
	code, out := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "../../etc/Logo.png", "size": len(data)})
	if code != 200 {
		t.Fatalf("create: %d %v", code, out)
	}
	id := out["uploadId"].(string)
	if out["filename"] != "Logo.png" {
		t.Fatalf("filename must be sanitized, got %v", out["filename"])
	}

	// first chunk
	if code, out = c.putChunk(id, data, 0, 99); code != 200 || out["received"].(float64) != 100 || out["complete"] != false {
		t.Fatalf("chunk 1: %d %v", code, out)
	}
	// wrong offset (client believes more was received): 409 with the real offset
	code, out = c.putChunk(id, data, 200, 299)
	if code != 409 || errCode(out) != "upload_offset" || errParam(out, "received").(float64) != 100 {
		t.Fatalf("offset mismatch must be 409 upload_offset: %d %v", code, out)
	}
	// duplicate of an already stored chunk is also an offset error
	if code, out = c.putChunk(id, data, 0, 99); code != 409 || errCode(out) != "upload_offset" {
		t.Fatalf("replayed chunk: %d %v", code, out)
	}
	// resume: ask the server where we are
	code, out = c.do("GET", "/api/v1/media/uploads/"+id, nil)
	if code != 200 || out["received"].(float64) != 100 {
		t.Fatalf("status: %d %v", code, out)
	}
	// a server restart loses the in-memory hash: it must be rebuilt from the file
	h.m.upMu.Lock()
	h.m.upHashes = map[string]*upHash{}
	h.m.upMu.Unlock()

	start := 100
	var final map[string]any
	for start < len(data) {
		end := min(start+120, len(data)) - 1
		code, out = c.putChunk(id, data, start, end)
		if code != 200 {
			t.Fatalf("resumed chunk %d-%d: %d %v", start, end, code, out)
		}
		final, start = out, end+1
	}
	if final["complete"] != true {
		t.Fatalf("expected completion: %v", final)
	}
	asset := final["asset"].(map[string]any)
	if asset["status"] != "processing" || asset["kind"] != "image" || asset["revision"].(float64) != 1 || asset["name"] != "Logo.png" {
		t.Fatalf("unexpected asset: %v", asset)
	}
	if asset["sha256"] != sha(data) {
		t.Fatalf("sha mismatch: %v vs %s", asset["sha256"], sha(data))
	}
	got, err := os.ReadFile(h.m.BlobPath(sha(data)))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("blob content differs: %v", err)
	}
	if h.count(`SELECT COUNT(*) FROM uploads`) != 0 || h.count(`SELECT COUNT(*) FROM media_jobs WHERE state = 'queued'`) != 1 {
		t.Fatal("upload row must be gone and a job queued")
	}
	if _, err := os.Stat(h.m.partPath(id)); !os.IsNotExist(err) {
		t.Fatalf("part file must be gone: %v", err)
	}
	if h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'media.uploaded'`) != 1 {
		t.Fatal("upload must be audited")
	}
}

func TestUploadFinalizeRetryWithQueryRange(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	data := pngBytes(20, 20, 2)
	_, out := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "a.png", "size": len(data)})
	id := out["uploadId"].(string)
	// "bytes */N" on an incomplete upload reports progress, never finishes
	res, _ := c.req("PUT", "/api/v1/media/uploads/"+id, nil, map[string]string{"Content-Range": "bytes */" + strconv.Itoa(len(data))})
	if res.StatusCode != 200 {
		t.Fatalf("query: %d", res.StatusCode)
	}
	if code, out := c.putChunk(id, data, 0, len(data)-1); code != 200 || out["complete"] != true {
		t.Fatalf("single chunk upload: %d %v", code, out)
	}
	// unknown upload
	if code, _ := c.putChunk(id, data, 0, 9); code != 404 {
		t.Fatalf("finished upload must be gone: %d", code)
	}
}

func timeAgo(hours int) time.Time { return time.Now().Add(-time.Duration(hours) * time.Hour) }

func TestUploadLimits(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	if code, out := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "x.png", "size": maxFileSize + 1}); code != 413 || errCode(out) != "file_too_large" {
		t.Fatalf("too large: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "x.png", "size": 0}); code != 422 {
		t.Fatalf("zero size: %d %v", code, out)
	}
	if code, _ := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "..", "size": 10}); code != 422 {
		t.Fatalf("bad filename: %d", code)
	}
	if code, _ := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "x.png", "size": 10, "folderId": "nope"}); code != 422 {
		t.Fatalf("unknown folder: %d", code)
	}
	// chunk larger than 16 MB is refused before anything is read
	_, out := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "big.mp4", "size": 40 << 20})
	id := out["uploadId"].(string)
	res, b := c.req("PUT", "/api/v1/media/uploads/"+id, bytes.NewReader(nil), map[string]string{"Content-Range": "bytes 0-17825791/41943040"})
	if res.StatusCode != 413 || !strings.Contains(string(b), "chunk_too_large") {
		t.Fatalf("chunk limit: %d %s", res.StatusCode, b)
	}
	// users with role "user" cannot upload
	u := h.login("user")
	if code, _ := u.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "x.png", "size": 10}); code != 403 {
		t.Fatalf("role user must not upload: %d", code)
	}
	// another operator cannot touch someone else's upload
	o2 := h.login("operator")
	if code, _ := o2.do("GET", "/api/v1/media/uploads/"+id, nil); code != 404 {
		t.Fatalf("foreign upload: %d", code)
	}
}

func TestDiskFullRejectedBeforeUpload(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	h.m.diskFn = func(string) (uint64, uint64, error) { return 100 << 30, 1 << 30, nil } // 1 GiB free
	// 400 MiB * 3 + 500 MiB reserve = 1.7 GiB > 1 GiB free
	code, out := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "movie.mp4", "size": 400 << 20})
	if code != 507 || errCode(out) != "disk_full" {
		t.Fatalf("expected 507 disk_full, got %d %v", code, out)
	}
	if need := errParam(out, "needed").(float64); int64(need) != 400<<20*3+500<<20 {
		t.Fatalf("needed param: %v", need)
	}
	if free := errParam(out, "free").(float64); int64(free) != 1<<30 {
		t.Fatalf("free param: %v", free)
	}
	if h.count(`SELECT COUNT(*) FROM uploads`) != 0 {
		t.Fatal("nothing may be stored when rejected")
	}
	// 150 MiB * 3 + 500 MiB = 950 MiB < 1 GiB: accepted
	if code, out := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "clip.mp4", "size": 150 << 20}); code != 200 {
		t.Fatalf("should fit: %d %v", code, out)
	}
	// that open upload now reserves its remaining bytes (874 MiB left): another one no longer fits
	if code, out := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "clip2.mp4", "size": 150 << 20}); code != 507 {
		t.Fatalf("reservation of open uploads: %d %v", code, out)
	}
}

func TestSniffingRejectsRenamedExecutable(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	exe := append([]byte("MZ\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00\xff\xff\x00\x00"), bytes.Repeat([]byte{0}, 600)...)
	code, out := c.upload("holiday.jpg", exe, nil, nil)
	if code != 415 || errCode(out) != "unsupported_type" {
		t.Fatalf("renamed exe must be 415: %d %v", code, out)
	}
	if h.count(`SELECT COUNT(*) FROM media_assets`) != 0 || h.count(`SELECT COUNT(*) FROM uploads`) != 0 || len(h.blobFiles()) != 0 {
		t.Fatal("rejected upload must leave nothing behind")
	}
	// the real content wins over the name: a PNG called .exe is fine
	a := c.mustUpload("setup.exe", pngBytes(8, 8, 3), nil)
	if a["kind"] != "image" {
		t.Fatalf("kind: %v", a["kind"])
	}
	// HTML and text are refused too
	if code, out := c.upload("page.png", []byte("<html><body>"+strings.Repeat("x", 600)+"</body></html>"), nil, nil); code != 415 {
		t.Fatalf("html: %d %v", code, out)
	}
}

func TestSVGUploadSafety(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	good := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" width="120" height="80" viewBox="0 0 120 80"><rect width="120" height="80" fill="#f00"/></svg>`)
	a := c.mustUpload("logo.svg", good, nil)
	if a["kind"] != "image" || a["mime"] != "image/svg+xml" {
		t.Fatalf("svg asset: %v", a)
	}
	h.runJobs()
	if st, e := h.assetStatus(a["id"].(string)); st != "ready" {
		t.Fatalf("svg must be ready: %s %s", st, e)
	}
	d := c.mustOK("GET", "/api/v1/media/"+a["id"].(string), nil)
	meta := d["meta"].(map[string]any)
	if meta["width"].(float64) != 120 || meta["height"].(float64) != 80 {
		t.Fatalf("svg size meta: %v", meta)
	}
	// served with a CSP that forbids scripts
	res, _ := c.req("GET", "/api/v1/media/"+a["id"].(string)+"/file", nil, nil)
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Security-Policy"), "script-src 'none'") || res.Header.Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("svg headers: %d %v", res.StatusCode, res.Header)
	}
	bad := map[string]string{
		"script":   `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"handler":  `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`,
		"jsurl":    `<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"><rect/></a></svg>`,
		"jsentity": `<svg xmlns="http://www.w3.org/2000/svg"><a href="&#106;avascript:alert(1)"><rect/></a></svg>`,
		"external": `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><image xlink:href="http://evil.example/x.png"/></svg>`,
		"cssurl":   `<svg xmlns="http://www.w3.org/2000/svg"><style>rect{fill:url(https://evil.example/a)}</style></svg>`,
		"foreign":  `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><div/></foreignObject></svg>`,
	}
	for name, doc := range bad {
		code, out := c.upload(name+".svg", []byte(doc), nil, nil)
		if code != 422 || errCode(out) != "svg_unsafe" {
			t.Errorf("%s: expected 422 svg_unsafe, got %d %v", name, code, out)
		}
	}
	if h.count(`SELECT COUNT(*) FROM media_assets`) != 1 {
		t.Fatal("only the safe svg may exist")
	}
}

func TestIdenticalFilesShareOneBlob(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	data := pngBytes(30, 30, 4)
	a1 := c.mustUpload("one.png", data, nil)
	a2 := c.mustUpload("two.png", data, nil)
	if a1["id"] == a2["id"] {
		t.Fatal("two uploads are two assets")
	}
	if a1["sha256"] != a2["sha256"] {
		t.Fatal("same content must have the same hash")
	}
	if h.count(`SELECT COUNT(*) FROM blobs`) != 1 || len(h.blobFiles()) != 1 {
		t.Fatalf("one blob expected, rows=%d files=%d", h.count(`SELECT COUNT(*) FROM blobs`), len(h.blobFiles()))
	}
}

func TestConcurrentIngestOfIdenticalContent(t *testing.T) {
	h := newHarness(t)
	data := bytes.Repeat([]byte("concurrent-ingest-"), 5000)
	src := filepath.Join(h.dir, "src.bin")
	if err := os.WriteFile(src, data, 0o640); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	shas := make(chan string, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, size, _, err := h.m.IngestFile(context.Background(), src)
			if err != nil {
				errs <- err
				return
			}
			if size != int64(len(data)) {
				errs <- os.ErrInvalid
			}
			shas <- s
		}()
	}
	wg.Wait()
	close(errs)
	close(shas)
	for err := range errs {
		t.Fatal(err)
	}
	for s := range shas {
		if s != sha(data) {
			t.Fatalf("hash %s", s)
		}
	}
	if h.count(`SELECT COUNT(*) FROM blobs`) != 1 || len(h.blobFiles()) != 1 {
		t.Fatalf("expected a single blob, rows=%d files=%v", h.count(`SELECT COUNT(*) FROM blobs`), h.blobFiles())
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("IngestFile must leave the source untouched")
	}
	if entries, _ := os.ReadDir(h.m.tmpDir("ingest")); len(entries) != 0 {
		t.Fatalf("staging files left behind: %d", len(entries))
	}
}

func TestConcurrentUploadsOfIdenticalFile(t *testing.T) {
	h := newHarness(t)
	data := pngBytes(50, 50, 9)
	var wg sync.WaitGroup
	errs := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := h.login("operator")
			if code, out := c.upload("same.png", data, nil, nil); code != 200 || out["complete"] != true {
				errs <- "upload failed"
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	if h.count(`SELECT COUNT(*) FROM media_assets`) != 8 || h.count(`SELECT COUNT(*) FROM blobs`) != 1 || len(h.blobFiles()) != 1 {
		t.Fatalf("assets=%d blobs=%d files=%d", h.count(`SELECT COUNT(*) FROM media_assets`), h.count(`SELECT COUNT(*) FROM blobs`), len(h.blobFiles()))
	}
}

func TestBlobPathRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"", "../../etc/passwd", "ABCDEF" + strings.Repeat("0", 58), strings.Repeat("g", 64), strings.Repeat("a", 63)} {
		if p := BlobPath("/data", bad); p != "" {
			t.Errorf("BlobPath(%q) = %q, want empty", bad, p)
		}
	}
	good := strings.Repeat("ab", 32)
	if p := filepath.ToSlash(BlobPath("/data", good)); p != "/data/blobs/sha256/ab/"+good {
		t.Errorf("BlobPath: %s", p)
	}
}

func TestExpiredUploadsAreCleaned(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	data := pngBytes(20, 20, 5)
	_, out := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "late.png", "size": len(data)})
	id := out["uploadId"].(string)
	if code, _ := c.putChunk(id, data, 0, 49); code != 200 {
		t.Fatal("chunk")
	}
	h.exec(`UPDATE uploads SET expires_at = '2000-01-01T00:00:00Z'`)
	// a stray part file without a row, older than an hour
	stray := h.m.partPath("stray-id")
	if err := os.WriteFile(stray, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	old := timeAgo(2)
	_ = os.Chtimes(stray, old, old)

	h.m.cleanupUploads(context.Background())
	if h.count(`SELECT COUNT(*) FROM uploads`) != 0 {
		t.Fatal("expired upload row must be removed")
	}
	if _, err := os.Stat(h.m.partPath(id)); !os.IsNotExist(err) {
		t.Fatal("expired part file must be removed")
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatal("stray part file must be removed")
	}
}

func TestAbortUpload(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	_, out := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "x.png", "size": 1000})
	id := out["uploadId"].(string)
	if code, _ := c.do("DELETE", "/api/v1/media/uploads/"+id, nil); code != 204 {
		t.Fatalf("abort: %d", code)
	}
	if code, _ := c.do("GET", "/api/v1/media/uploads/"+id, nil); code != 404 {
		t.Fatalf("aborted upload must be gone: %d", code)
	}
}
