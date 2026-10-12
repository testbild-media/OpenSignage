package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"opensignage/internal/server/config"
)

// testImage encodes a small image whose pixels depend on seed (distinct content per file).
func testImage(t *testing.T, format string, seed int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 24, 16))
	for i := range img.Pix {
		img.Pix[i] = uint8(i*seed + seed)
	}
	img.Set(0, 0, color.RGBA{R: uint8(seed), A: 255})
	var buf bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&buf, img)
	case "jpeg":
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	case "gif":
		err = gif.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// upload stores a file through the resumable upload API and returns the asset id.
func (c *client) upload(filename string, data []byte, folderID string) string {
	c.t.Helper()
	body := map[string]any{"filename": filename, "size": len(data)}
	if folderID != "" {
		body["folderId"] = folderID
	}
	code, out := c.do("POST", "/api/v1/media/uploads", body)
	if code != 200 {
		c.t.Fatalf("create upload %s: %d %v", filename, code, out)
	}
	req, _ := http.NewRequest("PUT", c.base+"/api/v1/media/uploads/"+out["uploadId"].(string), bytes.NewReader(data))
	req.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data)))
	req.Header.Set("X-CSRF-Token", c.csrf)
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), `"complete":true`) {
		c.t.Fatalf("upload %s: %d %s", filename, res.StatusCode, b)
	}
	out = map[string]any{}
	_ = json.Unmarshal(b, &out)
	return asStr(asMap(out["asset"])["id"])
}

// download fetches a file with the client's session.
func (c *client) download(p string) ([]byte, http.Header) {
	c.t.Helper()
	req, _ := http.NewRequest("GET", c.base+p, nil)
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		c.t.Fatalf("GET %s: %d %s", p, res.StatusCode, b)
	}
	return b, res.Header
}

// zipFiles reads a ZIP into name → content and checks every entry name is a clean,
// relative, portable path.
func zipFiles(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("media ZIP unreadable: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		n := f.Name
		if n == "" || strings.HasPrefix(n, "/") || strings.ContainsAny(n, "\\:*?\"<>|") || path.Clean(n) != n ||
			strings.HasPrefix(n, "../") || !utf8.ValidString(n) || strings.HasSuffix(n, "/") {
			t.Errorf("unsafe ZIP entry name %q", n)
		}
		for _, seg := range strings.Split(n, "/") {
			if seg == "" || seg == "." || seg == ".." || strings.TrimSpace(seg) != seg || strings.HasSuffix(seg, ".") {
				t.Errorf("bad path segment %q in %q", seg, n)
			}
		}
		if f.NonUTF8 {
			t.Errorf("entry %q not flagged as UTF-8", n)
		}
		if _, dup := out[strings.ToLower(n)]; dup {
			t.Errorf("duplicate ZIP entry %q", n)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("entry %q: %v", n, err)
		}
		out[n] = b
	}
	return out
}

func sortedKeys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestMediaZipAndFullRestore covers the backups with real media: the media ZIP contains the
// original files with their folder structure and sane, unique names; a full backup
// restored onto a fresh server brings the media (and their content) back.
func TestMediaZipAndFullRestore(t *testing.T) {
	_, ts := newTestServer(t)
	c := newClient(t, ts)
	if code, out := c.do("POST", "/api/v1/setup", map[string]any{"username": "admin", "displayName": "Admin",
		"password": "secret123", "passwordConfirm": "secret123"}); code != 200 {
		t.Fatalf("setup: %d %v", code, out)
	}
	code, out := c.do("POST", "/api/v1/media/folders", map[string]any{"name": "Menüs"})
	if code != 200 {
		t.Fatalf("folder: %d %v", code, out)
	}
	menus := out["id"].(string)
	code, out = c.do("POST", "/api/v1/media/folders", map[string]any{"name": "Drinks: hot", "parentId": menus})
	if code != 200 {
		t.Fatalf("subfolder: %d %v", code, out)
	}
	drinks := out["id"].(string)

	files := map[string][]byte{} // expected ZIP name → content
	byID := map[string][]byte{}  // asset id → content (not trashed)
	add := func(zipName, uploadName, format string, seed int, folder string) string {
		data := testImage(t, format, seed)
		id := c.upload(uploadName, data, folder)
		if id == "" {
			t.Fatalf("upload %s: no asset id", uploadName)
		}
		if zipName != "" {
			files[zipName] = data
			byID[id] = data
		}
		return id
	}
	// Same names (also differing in case only) get unique, numbered entries, in name and
	// upload order.
	add("Logo.png", "Logo.png", "png", 1, "")
	add("logo (3).PNG", "logo.PNG", "png", 2, "")
	add("Logo (2).png", "Logo.png", "png", 3, "")
	add("Menüs/Speisekarte Sommer.jpg", "Speisekarte Sommer.jpg", "jpeg", 4, menus)
	add("Menüs/Drinks_ hot/Kaffee.gif", "Kaffee.gif", "gif", 5, drinks)
	add("Menüs/Drinks_ hot/Tee.jpeg", "Tee.jpeg", "jpeg", 6, drinks)
	poster := add("Poster.png", "poster-final.png", "png", 7, "")
	old := add("", "Old.png", "png", 8, "")
	// Renamed without extension: the ZIP name still gets one.
	if code, out := c.do("PATCH", "/api/v1/media/"+poster, map[string]any{"name": "Poster"}); code != 200 {
		t.Fatalf("rename: %d %v", code, out)
	}
	// Trashed media is not part of the ZIP.
	if code, _ := c.do("DELETE", "/api/v1/media/"+old, nil); code != 200 && code != 204 {
		t.Fatalf("trash: %d", code)
	}

	data, hdr := c.download("/api/v1/backup/media.zip")
	if hdr.Get("Content-Type") != "application/zip" || !strings.Contains(hdr.Get("Content-Disposition"), `attachment; filename="opensignage-media-`) ||
		!strings.HasSuffix(hdr.Get("Content-Disposition"), `.zip"`) {
		t.Fatalf("media ZIP headers: %v", hdr)
	}
	got := zipFiles(t, data)
	if !equalKeys(got, files) {
		t.Fatalf("ZIP entries\n got %q\nwant %q", sortedKeys(got), sortedKeys(files))
	}
	for name, want := range files {
		if !bytes.Equal(got[name], want) {
			t.Errorf("%s: content differs from the uploaded original (%d vs %d bytes)", name, len(got[name]), len(want))
		}
	}

	// Full backup → fresh server → restore → restart: media and content are back.
	full, _ := c.download("/api/v1/backup/full")
	cfg := &config.Config{DataDir: t.TempDir(), HTTPPort: 80, HTTPSPort: 443, DevicePort: 7443, DiscoveryPort: 7444}
	fresh, err := New(context.Background(), cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	fts := httptest.NewUnstartedServer(fresh.WebMux)
	fts.StartTLS()
	fc := newClient(t, fts)
	if code, out := fc.do("POST", "/api/v1/setup", map[string]any{"username": "someone", "displayName": "S",
		"password": "secret123", "passwordConfirm": "secret123"}); code != 200 {
		t.Fatalf("setup fresh: %d %v", code, out)
	}
	if code, out := fc.do("GET", "/api/v1/media?folder=root", nil); code != 200 || len(asList(out["assets"])) != 0 {
		t.Fatalf("fresh server has media: %d %v", code, out)
	}
	if code, out := fc.rawUpload("POST", "/api/v1/backup/restore", full); code != 200 || out["staged"] != true || asNum(out["blobs"]) < float64(len(files)) {
		t.Fatalf("restore: %d %v", code, out)
	}
	fts.Close()
	_ = fresh.App.DB.Close()

	restarted, err := New(context.Background(), cfg, slog.Default()) // applies the staged restore
	if err != nil {
		t.Fatal(err)
	}
	rts := httptest.NewUnstartedServer(restarted.WebMux)
	rts.StartTLS()
	t.Cleanup(func() { rts.Close(); _ = restarted.App.DB.Close() })
	rc := newClient(t, rts)
	userLogin(t, rc, "admin", "secret123")
	if code, out := rc.do("GET", "/api/v1/media", nil); code != 200 || len(asList(out["assets"])) != len(byID) {
		t.Fatalf("media after restore: %d %v", code, out)
	}
	for id, want := range byID {
		if b, _ := rc.download("/api/v1/media/" + id + "/file"); !bytes.Equal(b, want) {
			t.Errorf("restored asset %s differs", id)
		}
	}
	data, _ = rc.download("/api/v1/backup/media.zip")
	restored := zipFiles(t, data)
	if !equalKeys(restored, files) {
		t.Fatalf("media after restore\n got %q\nwant %q", sortedKeys(restored), sortedKeys(files))
	}
	for name, want := range files {
		if !bytes.Equal(restored[name], want) {
			t.Errorf("restored %s: content differs", name)
		}
	}
}

func equalKeys(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}
