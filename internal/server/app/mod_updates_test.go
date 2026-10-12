package app

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"opensignage/internal/common/release"
)

// TestUpdatesModuleIsWired checks the updates module inside the real application:
// routes, roles, CSRF, trusted keys, bundle upload (raw body) incl. a path traversal
// attempt, and the overview.
func TestUpdatesModuleIsWired(t *testing.T) {
	srv, ts := newTestServer(t)
	c := newClient(t, ts)
	if code, out := c.do("POST", "/api/v1/setup", map[string]any{"username": "admin", "displayName": "Admin",
		"password": "secret123", "passwordConfirm": "secret123"}); code != 200 {
		t.Fatalf("setup: %d %v", code, out)
	}
	code, out := c.do("GET", "/api/v1/updates", nil)
	if code != 200 || out["version"] == nil || out["serverUpdate"] == nil {
		t.Fatalf("overview: %d %v", code, out)
	}

	// trusted keys
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	if code, out := c.do("POST", "/api/v1/updates/keys", map[string]any{"key": "osk1:nope"}); code != 422 || errCode(out) != "key_invalid" {
		t.Fatalf("invalid key: %d %v", code, out)
	}
	code, out = c.do("POST", "/api/v1/updates/keys", map[string]any{"key": release.FormatPublicKey(pub)})
	if code != 200 {
		t.Fatalf("add key: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/updates/keys", map[string]any{"key": release.FormatPublicKey(pub)}); code != 409 || errCode(out) != "key_exists" {
		t.Fatalf("duplicate key: %d %v", code, out)
	}
	var builtinID, customID string
	for _, k := range out["keys"].([]any) {
		k := k.(map[string]any)
		if k["builtin"] == true {
			builtinID = k["id"].(string)
		} else {
			customID = k["id"].(string)
		}
	}
	if code, out := c.do("DELETE", "/api/v1/updates/keys/"+builtinID, nil); code != 409 || errCode(out) != "key_builtin" {
		t.Fatalf("remove builtin: %d %v", code, out)
	}

	// signed release on disk → bundle
	dir := t.TempDir()
	pkg := []byte("package content")
	name := "opensignage-7.0.0-linux-amd64.tar.gz"
	_ = os.WriteFile(filepath.Join(dir, name), pkg, 0o644)
	sum, size, _ := release.FileSHA256(filepath.Join(dir, name))
	info := release.Info{Version: "7.0.0", Date: "2026-10-11T00:00:00Z", Notes: "notes",
		Packages: []release.Package{{OS: "linux", Arch: "amd64", File: name, SHA256: sum, Size: size}}}
	ib, _ := json.Marshal(info)
	_ = os.WriteFile(filepath.Join(dir, release.InfoFile), ib, 0o644)
	isum, _, _ := release.FileSHA256(filepath.Join(dir, release.InfoFile))
	sums := release.FormatSums(map[string]string{name: sum, release.InfoFile: isum})
	_ = os.WriteFile(filepath.Join(dir, release.SumsFile), sums, 0o644)
	_ = os.WriteFile(filepath.Join(dir, release.SigFile), []byte(release.Sign(priv, sums)), 0o644)

	// path traversal inside the bundle is refused and writes nothing outside
	evil := tarOf(t, dir, map[string]string{"../../escape.txt": "x"})
	if code, out := c.rawUpload("POST", "/api/v1/updates/upload", evil); code != 422 || errCode(out) != "bundle_invalid" {
		t.Fatalf("traversal bundle: %d %v", code, out)
	}
	if _, err := os.Stat(filepath.Join(srv.App.Cfg.DataDir, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("traversal wrote a file")
	}
	// without CSRF token the upload is refused
	saved := c.csrf
	c.csrf = ""
	if code, _ := c.rawUpload("POST", "/api/v1/updates/upload", tarOf(t, dir, nil)); code != 403 {
		t.Fatalf("upload without csrf: %d", code)
	}
	c.csrf = saved
	if code, out := c.rawUpload("POST", "/api/v1/updates/upload", tarOf(t, dir, nil)); code != 200 || out["version"] != "7.0.0" {
		t.Fatalf("upload: %d %v", code, out)
	}
	_, out = c.do("GET", "/api/v1/updates", nil)
	rels := out["releases"].([]any)
	if len(rels) != 1 || rels[0].(map[string]any)["state"] != "ready" || rels[0].(map[string]any)["source"] != "upload" {
		t.Fatalf("releases: %v", rels)
	}
	// removing the key that signed it is possible; later verifications fail
	if code, out := c.do("DELETE", "/api/v1/updates/keys/"+customID, nil); code != 200 {
		t.Fatalf("remove key: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/updates/server/install", map[string]any{"version": "7.0.0"}); code != 409 {
		t.Fatalf("install in a dev run must be refused: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/updates/clients", nil); code != 200 || out["devices"] == nil {
		t.Fatalf("clients: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/v1/updates/clients/start", map[string]any{}); code != 409 || errCode(out) != "nothing_to_update" {
		t.Fatalf("start without devices: %d %v", code, out)
	}
	if code, out := c.do("GET", "/api/v1/updates/runs", nil); code != 200 || out["runs"] == nil {
		t.Fatalf("runs: %d %v", code, out)
	}
	anon := newClient(t, ts)
	if code, _ := anon.do("GET", "/api/v1/updates", nil); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
}

func (c *client) rawUpload(method, path string, body []byte) (int, map[string]any) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-tar")
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	return res.StatusCode, out
}

func tarOf(t *testing.T, dir string, extra map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range extra {
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))})
		_, _ = tw.Write([]byte(content))
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		_ = tw.WriteHeader(&tar.Header{Name: e.Name(), Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(b))})
		_, _ = tw.Write(b)
	}
	_ = tw.Close()
	return buf.Bytes()
}
