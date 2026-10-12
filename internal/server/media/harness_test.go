package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"opensignage/internal/common/ids"
	"opensignage/internal/server/config"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
)

// harness wires the media module to a real SQLite database, a real router (roles, CSRF)
// and an HTTP server, without the rest of the application.
type harness struct {
	t   *testing.T
	m   *Module
	app *core.App
	ts  *httptest.Server
	dir string
	tc  *fakeTC
}

type client struct {
	h      *harness
	cookie string
	csrf   string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	return newHarnessAt(t, dir, dir)
}

// newHarnessAt keeps the database in dbDir and everything else (blobs, tmp) in dataDir,
// so tests can put the data directory on a tiny file system.
func newHarnessAt(t *testing.T, dataDir, dbDir string) *harness {
	t.Helper()
	dir := dataDir
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(dbDir, "db", "opensignage.db"))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := core.LoadSettings(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DataDir: dir, ShareDir: filepath.Join(dir, "share"), FFmpeg: "ffmpeg", FFprobe: "ffprobe"}
	a := &core.App{Cfg: cfg, DB: d, Log: slog.Default(), Settings: settings, Bus: core.NewBus(), Sessions: core.NewSessions(d)}
	m := New(a)
	m.gcGrace = 0
	h := &harness{t: t, m: m, app: a, dir: dir, tc: &fakeTC{}}
	m.tc = h.tc
	mux := http.NewServeMux()
	m.Routes(core.NewRouter(mux, a.Sessions))
	h.ts = httptest.NewServer(mux)
	t.Cleanup(func() { h.ts.Close(); _ = d.Close() })
	return h
}

func (h *harness) login(role string) *client {
	h.t.Helper()
	id := ids.New()
	name := role + "-" + id[len(id)-6:]
	var cookie, csrf string
	err := h.app.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		now := db.Now()
		if _, err := tx.Exec(`INSERT INTO users(id, username, display_name, password_hash, role, active, created_at, updated_at) VALUES (?, ?, ?, 'x', ?, 1, ?, ?)`,
			id, name, name, role, now, now); err != nil {
			return err
		}
		var err error
		cookie, csrf, err = h.app.Sessions.Create(context.Background(), tx, id, "127.0.0.1", "test")
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return &client{h: h, cookie: cookie, csrf: csrf}
}

func (c *client) req(method, path string, body io.Reader, hdr map[string]string) (*http.Response, []byte) {
	c.h.t.Helper()
	req, err := http.NewRequest(method, c.h.ts.URL+path, body)
	if err != nil {
		c.h.t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: core.CookieName, Value: c.cookie})
	req.Header.Set("X-CSRF-Token", c.csrf)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

// do sends a JSON request and returns status and decoded body.
func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.h.t.Helper()
	var rd io.Reader
	hdr := map[string]string{}
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
		hdr["Content-Type"] = "application/json"
	}
	res, b := c.req(method, path, rd, hdr)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return res.StatusCode, out
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func errParam(out map[string]any, key string) any {
	e, _ := out["error"].(map[string]any)
	p, _ := e["params"].(map[string]any)
	return p[key]
}

func (c *client) mustOK(method, path string, body any) map[string]any {
	c.h.t.Helper()
	code, out := c.do(method, path, body)
	if code != 200 && code != 204 {
		c.h.t.Fatalf("%s %s: status %d: %v", method, path, code, out)
	}
	return out
}

// uploadResult is what the final chunk returns.
func (c *client) upload(name string, data []byte, folderID *string, replaceID *string) (int, map[string]any) {
	c.h.t.Helper()
	body := map[string]any{"filename": name, "size": len(data)}
	if folderID != nil {
		body["folderId"] = *folderID
	}
	if replaceID != nil {
		body["replaceAssetId"] = *replaceID
	}
	code, out := c.do("POST", "/api/v1/media/uploads", body)
	if code != 200 {
		return code, out
	}
	id := out["uploadId"].(string)
	chunk := 700
	for off := 0; off < len(data); off += chunk {
		end := min(off+chunk, len(data))
		code, out = c.putChunk(id, data, off, end-1)
		if code != 200 {
			return code, out
		}
	}
	return code, out
}

func (c *client) putChunk(uploadID string, data []byte, start, end int) (int, map[string]any) {
	c.h.t.Helper()
	res, b := c.req("PUT", "/api/v1/media/uploads/"+uploadID, bytes.NewReader(data[start:end+1]),
		map[string]string{"Content-Range": fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)), "Content-Type": "application/octet-stream"})
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return res.StatusCode, out
}

func (c *client) mustUpload(name string, data []byte, folderID *string) map[string]any {
	c.h.t.Helper()
	code, out := c.upload(name, data, folderID, nil)
	if code != 200 || out["complete"] != true {
		c.h.t.Fatalf("upload %s: %d %v", name, code, out)
	}
	return out["asset"].(map[string]any)
}

func (c *client) mkFolder(name string, parent *string) string {
	c.h.t.Helper()
	body := map[string]any{"name": name}
	if parent != nil {
		body["parentId"] = *parent
	}
	return c.mustOK("POST", "/api/v1/media/folders", body)["id"].(string)
}

// ---- test data --------------------------------------------------------------------------

func pngBytes(w, h int, seed uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(int64(seed) + 1)) // noise keeps the file from compressing to nothing
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(rng.Intn(256)), uint8(rng.Intn(256)), seed, 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func jpegBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	return buf.Bytes()
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ---- fake transcoder ----------------------------------------------------------------------

// fakeTC replaces ffmpeg/ffprobe: probes return a canned result, runs write a small file.
type fakeTC struct {
	mu       sync.Mutex
	probe    Probe
	probeErr error
	runErr   error
	runs     [][]string
	onRun    func(n int) // called (without the lock) at the start of the n-th ffmpeg run
}

func (f *fakeTC) Probe(_ context.Context, _ string) (Probe, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.probeErr != nil {
		return Probe{}, f.probeErr
	}
	p := f.probe
	if p == (Probe{}) {
		p = Probe{HasVideo: true, VideoCodec: "png", Width: 64, Height: 48, PixFmt: "rgba"}
	}
	return p, nil
}

func (f *fakeTC) ProbeRotation(context.Context, string) (int, error) { return 0, nil }

func (f *fakeTC) Run(_ context.Context, args []string, _ func(int64)) error {
	f.mu.Lock()
	f.runs = append(f.runs, args)
	n, runErr, hook := len(f.runs), f.runErr, f.onRun
	f.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	if runErr != nil {
		return runErr
	}
	out, in := args[len(args)-1], ""
	for i, a := range args {
		if a == "-i" && i+1 < len(args) {
			in = filepath.Base(args[i+1])
		}
	}
	// the output depends on source and variant, like real transcodes do
	return os.WriteFile(out, append([]byte("\xff\xd8\xff\xe0fake-"), []byte(in+"/"+filepath.Base(out))...), 0o640)
}

func (f *fakeTC) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runs)
}

// ---- database helpers -----------------------------------------------------------------------

func (h *harness) count(query string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.app.DB.R.QueryRow(query, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) exec(query string, args ...any) {
	h.t.Helper()
	if _, err := h.app.DB.W.Exec(query, args...); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) blobFiles() []string {
	var out []string
	_ = filepath.WalkDir(filepath.Join(h.dir, "blobs"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// runJobs processes queued jobs until the queue is empty.
func (h *harness) runJobs() int {
	h.t.Helper()
	n := 0
	for {
		did, err := h.m.processNext(context.Background())
		if err != nil {
			h.t.Fatal(err)
		}
		if !did {
			return n
		}
		n++
	}
}

func (h *harness) assetStatus(id string) (string, string) {
	h.t.Helper()
	var st, e string
	if err := h.app.DB.R.QueryRow(`SELECT status, error FROM media_assets WHERE id = ?`, id).Scan(&st, &e); err != nil {
		h.t.Fatal(err)
	}
	return st, e
}
