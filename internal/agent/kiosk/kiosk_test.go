package kiosk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"opensignage/internal/agent/sysinfo"
	"opensignage/internal/agent/wire"
)

// TestMain lets the test binary act as a fake Chromium: it writes DevToolsActivePort
// into --user-data-dir and then waits (or exits at once).
func TestMain(m *testing.M) {
	if mode := os.Getenv("OS_FAKE_CHROMIUM"); mode != "" {
		if mode == "exit" {
			os.Exit(3)
		}
		for _, a := range os.Args[1:] {
			if dir, ok := strings.CutPrefix(a, "--user-data-dir="); ok {
				os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte(os.Getenv("OS_FAKE_CDP_PORT")+"\n/devtools/browser/x\n"), 0o600)
			}
		}
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeCDP answers /json/list and every CDP call with an empty result.
type fakeCDP struct {
	srv   *httptest.Server
	conns atomic.Int32
	calls atomic.Int32
}

func newFakeCDP(t *testing.T) *fakeCDP {
	f := &fakeCDP{}
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"type":"page","url":"http://127.0.0.1:7600/","webSocketDebuggerUrl":"ws://%s/devtools/page/1"}]`, r.Host)
	})
	mux.HandleFunc("/devtools/page/1", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		f.conns.Add(1)
		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var req struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
			}
			json.Unmarshal(data, &req)
			f.calls.Add(1)
			result := `{}`
			if req.Method == "Page.captureScreenshot" {
				result = `{"data":"/9j/"}`
			}
			c.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`{"id":%d,"result":%s}`, req.ID, result)))
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCDP) port() string {
	return f.srv.URL[strings.LastIndexByte(f.srv.URL, ':')+1:]
}

func newTestSupervisor(t *testing.T, mode string, cdp *fakeCDP, grace time.Duration) *Supervisor {
	t.Setenv("OS_FAKE_CHROMIUM", mode)
	if cdp != nil {
		t.Setenv("OS_FAKE_CDP_PORT", cdp.port())
	}
	exe, _ := os.Executable()
	return New(Options{
		Chromium:       func() string { return exe },
		UserDataDir:    t.TempDir(),
		Display:        ":0",
		URL:            "http://127.0.0.1:7600/",
		XAvailable:     func() bool { return true },
		CheckInterval:  50 * time.Millisecond,
		FirstPingGrace: grace,
		PingTimeout:    grace,
		MinBackoff:     20 * time.Millisecond,
		MaxBackoff:     80 * time.Millisecond,
		CDPTimeout:     5 * time.Second,
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestCrashLoopBacksOff(t *testing.T) {
	s := newTestSupervisor(t, "exit", nil, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitFor(t, "restarts", func() bool { _, n, _, _ := s.Status(); return n >= 3 })
	cancel()
	<-done
	state, _, lastErr, _ := s.Status()
	if lastErr == "" || (state != wire.PlayerCrashed && state != wire.PlayerRestarting && state != wire.PlayerStarting) {
		t.Fatalf("state %s, err %q", state, lastErr)
	}
}

func TestEveryRestartRecreatesCDP(t *testing.T) {
	cdp := newFakeCDP(t)
	s := newTestSupervisor(t, "stay", cdp, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitFor(t, "first cdp session", func() bool { return s.Reload(context.Background()) == nil })
	s.Heartbeat(wire.PlayerPing{Item: "x"})
	if st, _, _, _ := s.Status(); st != wire.PlayerOK {
		t.Fatalf("state after ping: %s", st)
	}
	if n := cdp.conns.Load(); n != 1 {
		t.Fatalf("cdp connections = %d", n)
	}
	// Manual restart path.
	s.Restart("test")
	waitFor(t, "second cdp session", func() bool { return cdp.conns.Load() == 2 && s.Reload(context.Background()) == nil })
	shot, err := s.Screenshot(context.Background())
	if err != nil || len(shot) == 0 {
		t.Fatalf("screenshot: %v", err)
	}
	// Watchdog path: pings stop → restart → a new CDP session again.
	s.o.PingTimeout = 100 * time.Millisecond
	s.Heartbeat(wire.PlayerPing{})
	waitFor(t, "watchdog restart with new cdp", func() bool { return cdp.conns.Load() >= 3 && s.Reload(context.Background()) == nil })
	if _, n, _, _ := s.Status(); n < 2 {
		t.Fatalf("restarts = %d", n)
	}
}

func TestArgsAndProfile(t *testing.T) {
	dir := t.TempDir()
	hw := false
	s := New(Options{UserDataDir: dir, URL: "http://127.0.0.1:7600/", HWVideo: func() bool { return hw }})
	args := strings.Join(s.Args(), " ")
	for _, want := range []string{"--kiosk", "--app=http://127.0.0.1:7600/", "--remote-debugging-port=0", "--disable-accelerated-video-decode",
		"--user-data-dir=" + dir, "--autoplay-policy=no-user-gesture-required", "--remote-allow-origins=http://127.0.0.1"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %s", want)
		}
	}
	hw = true
	if strings.Contains(strings.Join(s.Args(), " "), "--disable-accelerated-video-decode") {
		t.Error("hwVideo=on must enable accelerated decode")
	}
	os.MkdirAll(filepath.Join(dir, "Default", "Sessions"), 0o700)
	os.WriteFile(filepath.Join(dir, "Default", "Preferences"), []byte(`{"profile":{"exit_type":"Crashed","exited_cleanly":false},"x":1}`), 0o600)
	os.WriteFile(filepath.Join(dir, "SingletonLock"), []byte("x"), 0o600)
	if err := PrepareProfile(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Default", "Sessions")); !os.IsNotExist(err) {
		t.Error("sessions must be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "SingletonLock")); !os.IsNotExist(err) {
		t.Error("stale lock must be removed")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "Default", "Preferences"))
	if !strings.Contains(string(b), `"exit_type":"Normal"`) || !strings.Contains(string(b), `"x":1`) {
		t.Errorf("preferences: %s", b)
	}
}

// Screens never talk to the internet: everything but loopback goes to a dead proxy and DNS
// over HTTPS is switched off in the profile (other Local State keys are kept).
func TestNoInternetTrafficFromChromium(t *testing.T) {
	s := New(Options{URL: "http://127.0.0.1:7600/", UserDataDir: t.TempDir()})
	args := strings.Join(s.Args(), " ")
	for _, want := range []string{"--proxy-server=http://127.0.0.1:9", "--proxy-bypass-list=127.0.0.1;localhost;[::1]", "--disable-background-networking"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %s in %s", want, args)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Local State"), []byte(`{"browser":{"x":1},"dns_over_https":{"mode":"automatic"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareProfile(dir); err != nil {
		t.Fatal(err)
	}
	var st map[string]map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "Local State"))
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	if st["dns_over_https"]["mode"] != "off" || st["browser"]["x"] != float64(1) {
		t.Fatalf("local state %s", b)
	}
	fresh := t.TempDir()
	if err := PrepareProfile(fresh); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(fresh, "Local State")); err != nil || !strings.Contains(string(b), `"mode":"off"`) {
		t.Fatalf("fresh profile: %s %v", b, err)
	}
}

// The memory watchdog uses the real (proportional) use and real memory pressure, never the
// RSS sum, which counts Chromium's shared memory once per process.
func TestMemoryOver(t *testing.T) {
	const mb = 1 << 20
	mem := sysinfo.Memory{Total: 2000 * mb, Available: 1300 * mb}
	if memoryOver(570*mb, mem, 60) {
		t.Fatal("570 MB of 2 GB with plenty available is fine")
	}
	if !memoryOver(1300*mb, mem, 60) {
		t.Fatal("above 60 % of RAM is too much")
	}
	tight := sysinfo.Memory{Total: 1000 * mb, Available: 30 * mb}
	if !memoryOver(400*mb, tight, 60) {
		t.Fatal("system nearly out of memory while the player holds 40 %")
	}
	if memoryOver(100*mb, tight, 60) {
		t.Fatal("someone else uses the memory: restarting the player would not help")
	}
}

func TestGPUModeOf(t *testing.T) {
	for in, want := range map[string]string{"enabled": GPUHardware, "enabled_on": GPUHardware, "disabled_software": GPUSoftware,
		"unavailable_software": GPUSoftware, "": ""} {
		if got := gpuModeOf(map[string]string{"gpu_compositing": in}); got != want {
			t.Errorf("gpuModeOf(%q) = %q, want %q", in, got, want)
		}
	}
	if gpuModeOf(nil) != "" {
		t.Error("no feature status = unknown")
	}
}

// A player that had the GPU and falls back to software rendering is restarted at once (not
// as a crash); after gpuRestartLimit restarts within the window it keeps running and reports
// the GPU as lost. GPU process restarts are counted.
func TestGPULostRestartsPlayer(t *testing.T) {
	cdp := newFakeCDP(t)
	s := newTestSupervisor(t, "stay", cdp, time.Minute)
	var mode atomic.Value
	mode.Store(GPUHardware)
	var pid atomic.Int32
	pid.Store(100)
	var changes atomic.Int32
	s.o.GPUCheckInterval, s.o.GPUSettle = 20*time.Millisecond, 0
	s.o.GPUProbe = func(context.Context) (string, int, error) { return mode.Load().(string), int(pid.Load()), nil }
	s.o.OnGPUChange = func() { changes.Add(1) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitFor(t, "hardware", func() bool { return s.GPU().Mode == GPUHardware })
	pid.Store(101) // the GPU process was restarted by Chromium
	waitFor(t, "gpu crash counted", func() bool { return s.GPU().Crashes == 1 })
	mode.Store(GPUSoftware) // Chromium gave up the GPU
	waitFor(t, "limit reached", func() bool { return s.GPU().Mode == GPULost })
	g := s.GPU()
	if g.Recoveries != gpuRestartLimit || g.LostAt.IsZero() || cdp.conns.Load() < int32(gpuRestartLimit+1) {
		t.Fatalf("gpu %+v, cdp sessions %d", g, cdp.conns.Load())
	}
	if st, _, lastErr, _ := s.Status(); st == wire.PlayerCrashed || lastErr != "" {
		t.Fatalf("a GPU restart is no crash: %s %q", st, lastErr)
	}
	conns := cdp.conns.Load()
	time.Sleep(200 * time.Millisecond)
	if cdp.conns.Load() != conns {
		t.Fatal("no further restarts within the window")
	}
	if changes.Load() == 0 {
		t.Fatal("GPU changes are reported")
	}
}

// A device that never had the GPU (a VM) is left alone.
func TestGPUNeverHadIsLeftAlone(t *testing.T) {
	cdp := newFakeCDP(t)
	s := newTestSupervisor(t, "stay", cdp, time.Minute)
	s.o.GPUCheckInterval, s.o.GPUSettle = 20*time.Millisecond, 0
	s.o.GPUProbe = func(context.Context) (string, int, error) { return GPUSoftware, 7, nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, "software", func() bool { return s.GPU().Mode == GPUSoftware })
	time.Sleep(200 * time.Millisecond)
	if g := s.GPU(); g.Recoveries != 0 || cdp.conns.Load() != 1 {
		t.Fatalf("gpu %+v, cdp sessions %d", g, cdp.conns.Load())
	}
}
