package app

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"testing"
)

// TestMediaModuleIsWired checks the media module inside the real application: route
// registration (no pattern conflicts with other modules), sessions, roles, CSRF, and a
// complete resumable upload.
func TestMediaModuleIsWired(t *testing.T) {
	_, ts := newTestServer(t)
	c := newClient(t, ts)
	if code, out := c.do("POST", "/api/v1/setup", map[string]any{"username": "admin", "displayName": "Admin",
		"password": "secret123", "passwordConfirm": "secret123"}); code != 200 {
		t.Fatalf("setup: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/media?folder=root", nil); code != 200 || len(out["assets"].([]any)) != 0 {
		t.Fatalf("empty library: %d %v", code, out)
	}

	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for i := range img.Pix {
		img.Pix[i] = uint8(i * 7)
	}
	img.Set(1, 1, color.White)
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	data := buf.Bytes()

	code, out := c.do("POST", "/api/v1/media/uploads", map[string]any{"filename": "pixel.png", "size": len(data)})
	if code != 200 {
		t.Fatalf("create upload: %d %v", code, out)
	}
	id := out["uploadId"].(string)
	req, _ := http.NewRequest("PUT", c.base+"/api/v1/media/uploads/"+id, bytes.NewReader(data))
	req.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data)))
	req.Header.Set("X-CSRF-Token", c.csrf)
	res, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("put: %d %s", res.StatusCode, body)
	}

	code, out = c.do("GET", "/api/v1/media?folder=root", nil)
	if code != 200 || len(out["assets"].([]any)) != 1 {
		t.Fatalf("listing: %d %v", code, out)
	}
	asset := out["assets"].([]any)[0].(map[string]any)
	if asset["name"] != "pixel.png" || asset["kind"] != "image" {
		t.Fatalf("asset: %v", asset)
	}
	req, _ = http.NewRequest("GET", c.base+"/api/v1/media/"+asset["id"].(string)+"/file", nil)
	res, err = c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !bytes.Equal(got, data) {
		t.Fatalf("download: %d", res.StatusCode)
	}
	// Renamed without an extension: the download still gets the one of its content.
	if code, out := c.do("PATCH", "/api/v1/media/"+asset["id"].(string), map[string]any{"name": "Red Bull"}); code != 200 {
		t.Fatalf("rename: %d %v", code, out)
	}
	req, _ = http.NewRequest("GET", c.base+"/api/v1/media/"+asset["id"].(string)+"/file?download=1", nil)
	res, err = c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if cd := res.Header.Get("Content-Disposition"); cd != "attachment; filename*=UTF-8''Red%20Bull.png" || !bytes.Equal(got, data) {
		t.Fatalf("download name: %q (%d bytes)", cd, len(got))
	}
	if code, out := c.do("GET", "/api/v1/storage", nil); code != 200 || out["disk"] == nil || out["breakdown"] == nil {
		t.Fatalf("storage: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/fonts", nil); code != 200 || out["fonts"] == nil {
		t.Fatalf("fonts: %d %v", code, out)
	}
	// anonymous access is refused
	anon := newClient(t, ts)
	if code, _ := anon.do("GET", "/api/v1/media", nil); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
}
