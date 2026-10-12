package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"opensignage/internal/agent/config"
	"opensignage/internal/agent/display"
	"opensignage/internal/agent/kiosk"
	"opensignage/internal/agent/state"
	"opensignage/internal/agent/wire"
	"opensignage/internal/common/tlsutil"
	"opensignage/internal/contracts"
)

// fakeServer is an in-process device channel (pairing, WS, manifests, blobs).
type fakeServer struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	received []wire.Envelope
	conn     *websocket.Conn
	manifest []byte
	blob     []byte
	blobSHA  string
	claimed  bool
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{t: t, blob: []byte("PNG-DATA-0123456789")}
	sum := sha256.Sum256(f.blob)
	f.blobSHA = hex.EncodeToString(sum[:])
	m := wire.Manifest{ID: "dep-1", ZoneID: "zone-1", Hash: "hash-1", Timezone: "Europe/Berlin",
		Rules:     []contracts.Rule{{ID: "r1", Scope: "default", ZoneID: "zone-1", PlaylistID: "pl-1", Enabled: true}},
		Playlists: []wire.Playlist{{ID: "pl-1", Name: "Main"}},
		Assets: []wire.ManifestAsset{{ID: "img-1", Kind: "image", Variant: "orig", SHA256: f.blobSHA, Size: int64(len(f.blob)),
			MIME: "image/png", URL: "/device/v1/blobs/" + f.blobSHA}}}
	f.manifest, _ = json.Marshal(m)
	pair, err := tlsutil.EnsureSelfSigned(t.TempDir(), "device", "fake", 1)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /device/v1/pair/request", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(wire.PairResponse{PairingID: "p1", PIN: "424242", Secret: "s",
			ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	})
	mux.HandleFunc("GET /device/v1/pair/status", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		claimed := f.claimed
		f.mu.Unlock()
		if !claimed {
			json.NewEncoder(w).Encode(wire.PairStatus{State: wire.PairPending})
			return
		}
		json.NewEncoder(w).Encode(wire.PairStatus{State: wire.PairClaimed, DeviceID: "dev-1", Token: "tok"})
	})
	auth := func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer tok" }
	mux.HandleFunc("GET /device/v1/deployments/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) || r.PathValue("id") != "dep-1" {
			http.NotFound(w, r)
			return
		}
		w.Write(f.manifest)
	})
	mux.HandleFunc("GET /device/v1/blobs/{sha}", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) || r.PathValue("sha") != f.blobSHA {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.blob))
	})
	mux.HandleFunc("GET /device/v1/ws", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "revoked", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.SetReadLimit(1 << 20)
		f.mu.Lock()
		f.conn = c
		f.mu.Unlock()
		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var env wire.Envelope
			json.Unmarshal(data, &env)
			f.mu.Lock()
			f.received = append(f.received, env)
			f.mu.Unlock()
			if env.T == wire.THello {
				f.send(wire.TWelcome, wire.Welcome{DeviceID: "dev-1", ServerVersion: "9.9.9", Protocol: 1,
					ServerTime: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), VersionMatch: false})
				f.send(wire.TDeviceConfig, wire.DeviceConfig{Name: "Bar left", ZoneID: "zone-1", ScaleMode: "cover", Perf: "low", HWVideo: "off"})
				f.send(wire.TLiveState, map[string]any{"zone": map[string]any{"id": "zone-1", "name": "Bar", "state": "OPEN"},
					"products": []any{}, "categories": []any{}, "serverTime": time.Now().UTC().Format(time.RFC3339)})
				f.send(wire.TDeployAvailable, wire.DeployAvailable{ID: "dep-1", Hash: "hash-1"})
			}
		}
	})
	f.srv = httptest.NewUnstartedServer(mux)
	f.srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair.TLS}}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) send(t string, p any) {
	msg, _ := wire.Marshal(t, "", p)
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	if c != nil {
		c.Write(context.Background(), websocket.MessageText, msg)
	}
}

func (f *fakeServer) got(t string) []wire.Envelope {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []wire.Envelope
	for _, e := range f.received {
		if e.T == t {
			out = append(out, e)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestPairSyncAndPlayEndToEnd(t *testing.T) {
	srv := newFakeServer(t)
	dataDir := t.TempDir()
	st, _ := state.New(dataDir)
	// Leftovers of a previous server and a locally chosen display mode.
	kept := wire.Display{Width: 1280, Height: 720, Refresh: 60}
	st.SaveDevice(state.Device{Display: kept, Language: "de"})
	st.SaveDeployment(state.Deployment{Active: &state.Ref{ID: "old"}})
	st.SaveManifest("old", []byte(`{"id":"old"}`))
	stale := strings.Repeat("e", 64)
	os.WriteFile(st.BlobPath(stale), []byte("old"), 0o640)
	st.SaveLive(state.Live{State: json.RawMessage(`{"zone":{"id":"old","name":"Old","state":"CLOSED"}}`)})

	cfg := &config.Config{DataDir: dataDir, Server: strings.TrimPrefix(srv.srv.URL, "https://"), PlayerPort: 0, WebGUIPort: 0, DiscoveryPort: 0}
	fd := display.NewFake()
	a, err := New(cfg, Options{Display: fd, NoKiosk: true, Hostname: "player-test"})
	if err != nil {
		t.Fatal(err)
	}
	a.link.PollInterval = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, nil) }()
	defer func() { cancel(); <-done }()

	waitFor(t, "PIN on the player", func() bool { return strings.Contains(string(a.hub.Last(wire.TMode)), `"pin":"424242"`) })
	mode := string(a.hub.Last(wire.TMode))
	if !strings.Contains(mode, `"kind":"pairing"`) || !strings.Contains(mode, `"language":"de"`) {
		t.Fatalf("mode %s", mode)
	}
	srv.mu.Lock()
	srv.claimed = true
	srv.mu.Unlock()

	waitFor(t, "content playing", func() bool {
		return strings.Contains(string(a.hub.Last(wire.TMode)), `"kind":"playing"`) && strings.Contains(string(a.hub.Last(wire.TSource)), `"pl-1"`)
	})
	// Pairing wiped the previous server's data but kept device.json settings.
	if _, err := os.Stat(st.BlobPath(stale)); !os.IsNotExist(err) {
		t.Fatal("stale blob must be wiped on pairing")
	}
	if _, err := os.Stat(st.ManifestPath("old")); !os.IsNotExist(err) {
		t.Fatal("stale manifest must be wiped on pairing")
	}
	dev, _ := st.LoadDevice()
	if dev.Display != kept || dev.Language != "de" {
		t.Fatalf("device settings lost: %+v", dev)
	}
	if dev.Config == nil || dev.Config.Name != "Bar left" {
		t.Fatalf("device config not stored: %+v", dev.Config)
	}
	srvState, _ := st.LoadServer()
	if srvState.Token != "tok" || srvState.Fingerprint == "" {
		t.Fatalf("credentials %+v", srvState)
	}
	// The player got everything: bundle, live state, config, version notice.
	if b := string(a.hub.Last(wire.TBundle)); !strings.Contains(b, `"hash":"hash-1"`) || !strings.Contains(b, `"dep-1"`) {
		t.Fatalf("bundle %s", b)
	}
	if l := string(a.hub.Last(wire.TLive)); !strings.Contains(l, `"name":"Bar"`) {
		t.Fatalf("live %s", l)
	}
	if c := string(a.hub.Last(wire.TConfig)); !strings.Contains(c, `"scaleMode":"cover"`) || !strings.Contains(c, `"perf":"low"`) {
		t.Fatalf("config %s", c)
	}
	if v := string(a.hub.Last(wire.TVersionNotice)); !strings.Contains(v, "Version passt nicht") {
		t.Fatalf("version notice %s", v)
	}
	if c := string(a.hub.Last(wire.TConfig)); !strings.Contains(c, `"clockOffsetMs":3`) { // ~1 h server offset
		t.Fatalf("clock offset not applied: %s", c)
	}
	// The cached asset is served on the loopback server.
	p, _, sha, ok := a.sync.AssetFile("img-1")
	if !ok || sha != srv.blobSHA {
		t.Fatal("asset not cached")
	}
	if b, _ := os.ReadFile(p); !bytes.Equal(b, srv.blob) {
		t.Fatal("asset content wrong")
	}
	// The server saw hello first, then progress up to active, and a status with the
	// active source.
	waitFor(t, "deploy.progress active", func() bool {
		for _, e := range srv.got(wire.TDeployProgress) {
			if strings.Contains(string(e.P), `"state":"active"`) {
				return true
			}
		}
		return false
	})
	srv.mu.Lock()
	first := srv.received[0].T
	srv.mu.Unlock()
	if first != wire.THello {
		t.Fatalf("first message %s", first)
	}
	waitFor(t, "status with active source", func() bool {
		for _, e := range srv.got(wire.TStatus) {
			if strings.Contains(string(e.P), `"playlistId":"pl-1"`) && strings.Contains(string(e.P), `"activeDeploymentId":"dep-1"`) {
				return true
			}
		}
		return false
	})

	// Remote actions: unknown → error, clean_cache → ok.
	srv.send(wire.TAction, wire.Action{ID: "a1", Name: "make_coffee"})
	srv.send(wire.TAction, wire.Action{ID: "a2", Name: wire.ActionCleanCache})
	srv.send(wire.TAction, wire.Action{ID: "a3", Name: wire.ActionCleanCache, Params: json.RawMessage(`{"dryRun":true}`)})
	waitFor(t, "action results", func() bool { return len(srv.got(wire.TActionResult)) >= 3 })
	for _, e := range srv.got(wire.TActionResult) {
		var r wire.ActionResult
		json.Unmarshal(e.P, &r)
		switch r.ID {
		case "a1":
			if r.OK || r.Error != "unknown_action" {
				t.Fatalf("unknown action result %+v", r)
			}
		case "a2":
			if !r.OK {
				t.Fatalf("clean_cache result %+v", r)
			}
		case "a3":
			if b, _ := json.Marshal(r.Data); !r.OK || !strings.Contains(string(b), `"dryRun":true`) {
				t.Fatalf("clean_cache preview result %+v", r)
			}
		}
	}
	// Zone CLOSED without a closed playlist keeps the default; blackout from config.
	srv.send(wire.TDeviceConfig, wire.DeviceConfig{Name: "Bar left", ZoneID: "zone-1", Blackout: true})
	waitFor(t, "blackout", func() bool { return strings.Contains(string(a.hub.Last(wire.TMode)), `"kind":"blackout"`) })
	// Plasma regeneration from the server: white noise even over a blackout, reported in
	// the status; invalid parameters are refused; switched off it is blackout again.
	srv.send(wire.TAction, wire.Action{ID: "r0", Name: wire.ActionRegeneration, Params: json.RawMessage(`{}`)})
	srv.send(wire.TAction, wire.Action{ID: "r1", Name: wire.ActionRegeneration, Params: json.RawMessage(`{"on":true}`)})
	waitFor(t, "regeneration", func() bool { return strings.Contains(string(a.hub.Last(wire.TMode)), `"kind":"regeneration"`) })
	waitFor(t, "regeneration results", func() bool {
		var r0, r1 bool
		for _, e := range srv.got(wire.TActionResult) {
			var r wire.ActionResult
			json.Unmarshal(e.P, &r)
			r0 = r0 || (r.ID == "r0" && !r.OK && r.Error == "invalid_params")
			r1 = r1 || (r.ID == "r1" && r.OK)
		}
		return r0 && r1
	})
	waitFor(t, "status mode regeneration", func() bool {
		for _, e := range srv.got(wire.TStatus) {
			if strings.Contains(string(e.P), `"mode":"regeneration"`) {
				return true
			}
		}
		return false
	})
	srv.send(wire.TAction, wire.Action{ID: "r2", Name: wire.ActionRegeneration, Params: json.RawMessage(`{"on":false}`)})
	waitFor(t, "blackout after regeneration", func() bool { return strings.Contains(string(a.hub.Last(wire.TMode)), `"kind":"blackout"`) })
	// deploy.cleared → cleared mode, persisted.
	srv.send(wire.TDeviceConfig, wire.DeviceConfig{Name: "Bar left", ZoneID: "zone-1"})
	srv.send(wire.TDeployCleared, wire.DeployCleared{Reason: "test"})
	waitFor(t, "cleared", func() bool { return strings.Contains(string(a.hub.Last(wire.TMode)), `"kind":"cleared"`) })
	if d, _ := st.LoadDeployment(); !d.Cleared || d.Previous == nil || d.Previous.ID != "dep-1" {
		t.Fatalf("cleared not persisted: %+v", d)
	}
	if d := fd.Applied; len(d) != 0 {
		t.Fatalf("default server display must not override the local mode: %+v", d)
	}
	// Without a zone the content is cleared too, but the screen shows the standby screen
	// ("no zone assigned") instead of black; with a zone again it is cleared (black).
	srv.send(wire.TDeviceConfig, wire.DeviceConfig{Name: "Bar left"})
	waitFor(t, "standby without zone", func() bool { return strings.Contains(string(a.hub.Last(wire.TMode)), `"kind":"standby"`) })
	srv.send(wire.TDeviceConfig, wire.DeviceConfig{Name: "Bar left", ZoneID: "zone-1"})
	waitFor(t, "cleared with zone", func() bool { return strings.Contains(string(a.hub.Last(wire.TMode)), `"kind":"cleared"`) })

	// The same server under another address (client WebGUI, e.g. a VPN address): pairing,
	// token and content stay, the agent reconnects there with its token.
	hellos := len(srv.got(wire.THello))
	port := srv.srv.URL[strings.LastIndex(srv.srv.URL, ":")+1:]
	if err := a.SetServerAddress("localhost:" + port); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reconnect at the new address", func() bool { return len(srv.got(wire.THello)) > hellos })
	if c := a.Credentials(); !c.Paired() || c.Token != "tok" || c.Address != "localhost:"+port {
		t.Fatalf("pairing lost on address change: %+v", c)
	}
	if _, err := os.Stat(st.ManifestPath("dep-1")); err != nil {
		t.Fatalf("content wiped on address change: %v", err)
	}
	if strings.Contains(string(a.hub.Last(wire.TMode)), `"kind":"pairing"`) {
		t.Fatal("no new PIN for the same server")
	}
}

func TestOfflineStartShowsLastContent(t *testing.T) {
	dataDir := t.TempDir()
	st, _ := state.New(dataDir)
	blob := []byte("cached")
	sum := sha256.Sum256(blob)
	sha := hex.EncodeToString(sum[:])
	m := wire.Manifest{ID: "dep-9", ZoneID: "z", Hash: "h9", Timezone: "UTC",
		Rules:     []contracts.Rule{{ID: "r", Scope: "default", ZoneID: "z", PlaylistID: "p", Enabled: true}},
		Playlists: []wire.Playlist{{ID: "p"}},
		Assets:    []wire.ManifestAsset{{ID: "a", Kind: "image", Variant: "orig", SHA256: sha, Size: int64(len(blob))}}}
	raw, _ := json.Marshal(m)
	st.SaveManifest("dep-9", raw)
	os.WriteFile(st.BlobPath(sha), blob, 0o640)
	st.SaveDeployment(state.Deployment{Active: &state.Ref{ID: "dep-9", Hash: "h9"}})
	st.SaveServer(state.Server{Address: "127.0.0.1:1", Fingerprint: "ff", Token: "t", DeviceID: "d"})
	st.SaveDevice(state.Device{Config: &wire.DeviceConfig{Name: "n", ZoneID: "z"}})
	st.SaveLive(state.Live{State: json.RawMessage(`{"zone":{"id":"z","name":"Z","state":"OPEN"}}`)})
	a, err := New(&config.Config{DataDir: dataDir, Server: "127.0.0.1:1"}, Options{Display: display.NewFake(), NoKiosk: true})
	if err != nil {
		t.Fatal(err)
	}
	a.refreshPlayer()
	if !strings.Contains(string(a.hub.Last(wire.TMode)), `"kind":"playing"`) || !strings.Contains(string(a.hub.Last(wire.TSource)), `"p"`) ||
		!strings.Contains(string(a.hub.Last(wire.TLive)), `"Z"`) {
		t.Fatalf("offline start must show the last content immediately: %s %s", a.hub.Last(wire.TMode), a.hub.Last(wire.TSource))
	}
}

// TestStatusReportsAppliedConfig covers the status report on the agent side: the
// status carries the modes the screen offers, the log size, the preview capability and
// what the agent made of the server's blackout and display setting (applied, failed with
// the reason, kept the previous mode), so the admin sees a change confirmed or failed.
func TestStatusReportsAppliedConfig(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "logs"), 0o750); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dataDir, "logs", "agent.log"), bytes.Repeat([]byte("l"), 123), 0o640)
	fd := display.NewFake()
	a, err := New(&config.Config{DataDir: dataDir, Server: "127.0.0.1:1"}, Options{Display: fd, NoKiosk: true})
	if err != nil {
		t.Fatal(err)
	}
	s := a.Status()
	if s.Applied != nil {
		t.Fatalf("no config yet, applied = %+v", s.Applied)
	}
	if s.LogsBytes != 123 || len(s.Caps) != 2 || s.Caps[0] != wire.CapCleanCachePreview || s.Caps[1] != wire.CapRegeneration {
		t.Fatalf("logs %d, caps %v", s.LogsBytes, s.Caps)
	}
	if len(s.Display.Modes) != 2 || s.Display.Modes[0].Width != 1920 || s.Display.Modes[0].Current != 60 {
		t.Fatalf("display modes %+v", s.Display.Modes)
	}

	want := wire.Display{Width: 1280, Height: 720, Refresh: 60}
	a.onDeviceConfig(wire.DeviceConfig{Name: "n", Blackout: true, Display: want})
	waitFor(t, "display applied", func() bool {
		ap := a.Status().Applied
		return ap != nil && !ap.DisplayBusy && ap.DisplayMode == want
	})
	if ap := a.Status().Applied; !ap.Blackout || ap.Display != want || ap.DisplayError != "" {
		t.Fatalf("applied %+v", ap)
	}

	fd.Fail = errors.New("xrandr: cannot set mode")
	other := wire.Display{Width: 1920, Height: 1080, Refresh: 60}
	a.onDeviceConfig(wire.DeviceConfig{Name: "n", Display: other})
	waitFor(t, "display failure reported", func() bool {
		ap := a.Status().Applied
		return ap != nil && !ap.DisplayBusy && ap.DisplayError != ""
	})
	ap := a.Status().Applied
	if ap.Blackout || ap.Display != other || ap.DisplayMode != want || !strings.Contains(ap.DisplayError, "cannot set mode") {
		t.Fatalf("failed apply %+v", ap)
	}
	if dev, _ := a.store.LoadDevice(); dev.Display != want {
		t.Fatalf("previous mode must be kept: %+v", dev.Display)
	}
}

// The device setting auto | on | off is resolved per platform: "auto" decodes in hardware
// wherever a working path is detected (Raspberry Pi, Intel/AMD with VA-API + Vulkan), "off"
// always wins.
func TestHWVideoSettingResolvedPerDevice(t *testing.T) {
	a, err := New(&config.Config{DataDir: t.TempDir(), Server: "127.0.0.1:1"}, Options{Display: display.NewFake(), NoKiosk: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		accel kiosk.VideoAccel
		mode  string
		want  bool
	}{
		{kiosk.AccelV4L2, "auto", true},
		{kiosk.AccelV4L2, "off", false},
		{kiosk.AccelV4L2, "on", true},
		{kiosk.AccelVAAPI, "auto", true},
		{kiosk.AccelVAAPI, "on", true},
		{kiosk.AccelVAAPI, "off", false},
		{kiosk.AccelNone, "auto", false},
		{kiosk.AccelNone, "on", true},
		{kiosk.AccelNone, "off", false},
	} {
		a.videoAccel = c.accel
		a.onDeviceConfig(wire.DeviceConfig{Name: "n", HWVideo: c.mode})
		if got := a.hwVideo.Load(); got != c.want {
			t.Errorf("%q with %q: hardware decoding %v", c.mode, c.accel, got)
		}
	}
}

// Repeated decode failures with hardware decoding from "auto" switch the session to software
// decoding and show up in the status; an explicit "on" is never overridden.
func TestAutoHWVideoFallsBackAfterDecodeErrors(t *testing.T) {
	a, err := New(&config.Config{DataDir: t.TempDir(), Server: "127.0.0.1:1"}, Options{Display: display.NewFake(), NoKiosk: true})
	if err != nil {
		t.Fatal(err)
	}
	a.videoAccel = kiosk.AccelV4L2
	a.onDeviceConfig(wire.DeviceConfig{Name: "n", HWVideo: "auto"})
	decodeErr := wire.PlayerLog{Level: "warn", Msg: "media failed: media_error_3"}
	a.playerLog(decodeErr)
	a.playerLog(wire.PlayerLog{Level: "warn", Msg: "media failed: stalled"})
	if v := a.Status().Video; v == nil || !v.HW || v.Path != "v4l2" || v.Fallback != "" {
		t.Fatalf("one failure must not switch: %+v", v)
	}
	a.playerLog(decodeErr)
	if v := a.Status().Video; v.HW || v.Fallback != FallbackDecodeErrors || v.FallbackAt == "" {
		t.Fatalf("after repeated decode errors: %+v", v)
	}
	a.onDeviceConfig(wire.DeviceConfig{Name: "n", HWVideo: "auto"})
	if a.hwVideo.Load() {
		t.Fatal("the fallback lasts for the session")
	}
	a.onDeviceConfig(wire.DeviceConfig{Name: "n", HWVideo: "on"})
	a.playerLog(decodeErr)
	a.playerLog(decodeErr)
	if !a.hwVideo.Load() {
		t.Fatal("an explicit on is never overridden")
	}
	a.onDeviceConfig(wire.DeviceConfig{Name: "n", HWVideo: "auto"})
	if a.hwVideo.Load() {
		t.Fatal("back to auto: still software in this session")
	}
}

// Intel/AMD: "auto" decodes in hardware (Vulkan compositing); three player crashes within
// ten minutes switch the session to software. Crashes on the Pi path or with "on" do not.
func TestAutoHWVideoOnX86FallsBackAfterCrashes(t *testing.T) {
	a, err := New(&config.Config{DataDir: t.TempDir(), Server: "127.0.0.1:1"}, Options{Display: display.NewFake(), NoKiosk: true})
	if err != nil {
		t.Fatal(err)
	}
	a.videoAccel = kiosk.AccelVAAPI
	a.onDeviceConfig(wire.DeviceConfig{Name: "n", HWVideo: "auto"})
	if v := a.Status().Video; v == nil || !v.HW || v.Path != "vaapi-vulkan" {
		t.Fatalf("auto on Intel/AMD must decode in hardware: %+v", v)
	}
	t0 := time.Now()
	a.playerCrashed(t0.Add(-20 * time.Minute)) // too long ago
	a.playerCrashed(t0)
	a.playerCrashed(t0.Add(time.Minute))
	if !a.hwVideo.Load() {
		t.Fatal("two recent crashes must not switch")
	}
	a.playerCrashed(t0.Add(2 * time.Minute))
	if v := a.Status().Video; v.HW || v.Fallback != FallbackCrashes || v.FallbackAt == "" {
		t.Fatalf("after three crashes: %+v", v)
	}

	b, err := New(&config.Config{DataDir: t.TempDir(), Server: "127.0.0.1:1"}, Options{Display: display.NewFake(), NoKiosk: true})
	if err != nil {
		t.Fatal(err)
	}
	b.videoAccel = kiosk.AccelVAAPI
	b.onDeviceConfig(wire.DeviceConfig{Name: "n", HWVideo: "on"})
	for i := range 5 {
		b.playerCrashed(t0.Add(time.Duration(i) * time.Second))
	}
	if !b.hwVideo.Load() {
		t.Fatal("an explicit on is never overridden")
	}
	b.videoAccel = kiosk.AccelV4L2
	b.onDeviceConfig(wire.DeviceConfig{Name: "n", HWVideo: "auto"})
	for i := range 5 {
		b.playerCrashed(t0.Add(time.Duration(i) * time.Second))
	}
	if !b.hwVideo.Load() {
		t.Fatal("crashes on the Pi path do not switch (rendering does not change there)")
	}
}
