package player

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"opensignage/internal/agent/wire"
)

type files struct{ dir string }

func (f files) AssetFile(id string) (string, string, string, bool) {
	if id != "img" {
		return "", "", "", false
	}
	return filepath.Join(f.dir, "blob"), "image/png", strings.Repeat("a", 64), true
}

func (f files) FontFile(string) (string, string, bool) { return "", "", false }

func TestOnlyLoopbackClients(t *testing.T) {
	s := &Server{Hub: NewHub()}
	h := s.Handler()
	for _, tc := range []struct{ remote, host string }{
		{"10.1.2.3:5555", "127.0.0.1:7600"},
		{"192.168.0.10:1", "127.0.0.1:7600"},
		{"127.0.0.1:5555", "evil.example:7600"}, // DNS rebinding
	} {
		r := httptest.NewRequest("GET", "/ws", nil)
		r.RemoteAddr, r.Host = tc.remote, tc.host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%+v: status %d, want 403", tc, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr, r.Host = "127.0.0.1:5555", "127.0.0.1:7600"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "OpenSignage") {
		t.Fatalf("fallback page: %d", w.Code)
	}
}

func readMsg(t *testing.T, c *websocket.Conn) wire.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var env wire.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestWebSocketSnapshotAndDiffs(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "blob"), []byte("0123456789"), 0o640)
	pings := make(chan wire.PlayerPing, 1)
	hub := NewHub()
	s := &Server{Hub: hub, Files: files{dir}, OnPing: func(p wire.PlayerPing) { pings <- p }}
	srv := httptest.NewServer(s.Handler()) // listens on 127.0.0.1
	defer srv.Close()

	plID := "pl-1"
	hub.Set(Snapshot{
		Mode:   wire.PlayerMode{Kind: wire.ModePlaying, IPs: []string{"10.0.0.2"}, WebGUIURL: "https://10.0.0.2:8443/"},
		Config: wire.PlayerConfig{ScaleMode: "contain", Perf: "standard", ShowIPOverlay: true},
		Bundle: wire.PlayerBundle{Hash: "h1", Manifest: json.RawMessage(`{"id":"d1"}`)},
		Source: wire.PlayerSource{PlaylistID: &plID, Reason: "rule"},
	})
	c, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	var types []string
	for i := 0; i < 6; i++ {
		types = append(types, readMsg(t, c).T)
	}
	if strings.Join(types, ",") != "mode,config,bundle,source,live,versionNotice" {
		t.Fatalf("snapshot order: %v", types)
	}
	// Only the changed part is sent.
	hub.Set(Snapshot{
		Mode:   wire.PlayerMode{Kind: wire.ModeBlackout, IPs: []string{"10.0.0.2"}, WebGUIURL: "https://10.0.0.2:8443/"},
		Config: wire.PlayerConfig{ScaleMode: "contain", Perf: "standard", ShowIPOverlay: true},
		Bundle: wire.PlayerBundle{Hash: "h1", Manifest: json.RawMessage(`{"id":"d1"}`)},
		Source: wire.PlayerSource{PlaylistID: &plID, Reason: "rule"},
	})
	if env := readMsg(t, c); env.T != wire.TMode || !strings.Contains(string(env.P), "blackout") {
		t.Fatalf("expected mode change, got %s %s", env.T, env.P)
	}
	hub.Cmd("reload")
	if env := readMsg(t, c); env.T != wire.TCmd {
		t.Fatalf("expected cmd, got %s", env.T)
	}
	msg, _ := wire.Marshal(wire.TPing, "", wire.PlayerPing{Heap: 1234, Item: "slide-1"})
	if err := c.Write(context.Background(), websocket.MessageText, msg); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-pings:
		if p.Heap != 1234 || p.Item != "slide-1" {
			t.Fatalf("ping %+v", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ping not delivered")
	}

	// Assets support Range and carry the SHA-256 as ETag.
	req, _ := http.NewRequest("GET", srv.URL+"/assets/img", nil)
	req.Header.Set("Range", "bytes=2-4")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(body) != "234" || resp.Header.Get("ETag") == "" {
		t.Fatalf("range: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if resp, _ := http.Get(srv.URL + "/assets/unknown"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown asset: %d", resp.StatusCode)
	}
}

func TestCrossOriginWebSocketRefused(t *testing.T) {
	s := &Server{Hub: NewHub()}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	_, resp, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"http://evil.example"}}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin websocket must be refused: %v", err)
	}
}

// Bundled fonts: manifest.json is revalidated (an update can add families), font files are
// cached (their URLs carry the content hash).
func TestBundledFontCaching(t *testing.T) {
	share := t.TempDir()
	if err := os.MkdirAll(filepath.Join(share, "fonts", "barlow"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"manifest.json": `{"version":1}`, "barlow/Barlow-Regular.woff2": "wOF2"} {
		if err := os.WriteFile(filepath.Join(share, "fonts", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{ShareDir: share}
	for url, want := range map[string]string{
		"/fonts/bundled/manifest.json":                         "no-cache",
		"/fonts/bundled/barlow/Barlow-Regular.woff2?v=0123abc": "public, max-age=86400",
	} {
		r := httptest.NewRequest("GET", url, nil)
		r.RemoteAddr, r.Host = "127.0.0.1:50000", "127.0.0.1:7600"
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != want {
			t.Errorf("%s: %d %q, want %q", url, w.Code, w.Header().Get("Cache-Control"), want)
		}
	}
}
