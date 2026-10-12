// Package kiosk starts and supervises Chromium as a child process: fixed
// kiosk flags, a CDP connection for reload/screenshots/health, and a watchdog that
// restarts the browser when it exits, stops sending player pings, stops answering
// CDP, uses too much memory or lost its GPU (gpu.go). Restarts back off 2, 4, 8 … 300 s (reset after 10
// minutes of stable operation). Every restart path re-creates the CDP connection.
package kiosk

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"opensignage/internal/agent/safe"
	"opensignage/internal/agent/sysinfo"
	"opensignage/internal/agent/wire"
)

// Options configure the supervisor.
type Options struct {
	Chromium    func() string // browser binary ("" = not installed)
	UserDataDir string
	Display     string
	URL         string // http://127.0.0.1:7600/
	// HWVideo reports whether hardware video decoding is enabled (device config resolved
	// with HWVideoEnabled).
	HWVideo func() bool
	// VideoAccel selects the Chromium flags for hardware decoding (DetectVideoAccel).
	VideoAccel VideoAccel
	// DisplayReady is called when X is available, before and after each start, to
	// re-apply the saved display mode.
	DisplayReady func(ctx context.Context)
	// XAvailable reports whether the X server is up (default: socket check).
	XAvailable func() bool
	// WindowSize returns the screen size for the browser window (0 = unknown). Without
	// a window manager --kiosk alone does not cover the whole screen.
	WindowSize func() (w, h int)
	// OnStateChange is called (outside the supervisor lock) when the player state changes,
	// so the agent can report it at once instead of with the next 30 s status.
	OnStateChange func(state string)
	// OnGPUChange is called when the GPU state changes (GPU()).
	OnGPUChange func()
	// GPUProbe asks Chromium for its GPU state (default: CDP SystemInfo).
	GPUProbe GPUProbe

	PingTimeout      time.Duration // 30 s
	FirstPingGrace   time.Duration // 90 s after start
	CheckInterval    time.Duration // 5 s
	CDPTimeout       time.Duration // 30 s to find DevToolsActivePort
	MinBackoff       time.Duration // 2 s
	MaxBackoff       time.Duration // 300 s
	StableAfter      time.Duration // 10 min
	MemLimitPercent  float64       // 60
	GPUCheckInterval time.Duration // 30 s
	GPUSettle        time.Duration // 60 s after start before a software state counts
	ExtraArgs        []string
}

// Supervisor runs Chromium.
type Supervisor struct {
	o Options

	mu        sync.Mutex
	state     string
	lastPing  time.Time
	item      string
	cdp       *CDP
	restarts  int
	lastError string
	startedAt time.Time
	winW      int
	winH      int
	running   bool

	// fit sets Chromium's window to the screen size (xwindow.go; nil = not needed).
	fit func(w, h int) (xFitResult, error)

	gpu         GPUInfo
	gpuEver     bool        // the player had the GPU in this agent session
	gpuRestarts []time.Time // restarts because the GPU was lost (gpuRestartWindow)

	restartReq chan string
}

// New creates a supervisor.
func New(o Options) *Supervisor {
	def := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	def(&o.PingTimeout, 30*time.Second)
	def(&o.FirstPingGrace, 90*time.Second)
	def(&o.CheckInterval, 5*time.Second)
	def(&o.CDPTimeout, 30*time.Second)
	def(&o.MinBackoff, 2*time.Second)
	def(&o.MaxBackoff, 300*time.Second)
	def(&o.StableAfter, 10*time.Minute)
	def(&o.GPUCheckInterval, 30*time.Second)
	def(&o.GPUSettle, time.Minute)
	if o.GPUProbe == nil {
		o.GPUProbe = cdpGPUProbe(o.UserDataDir)
	}
	if o.MemLimitPercent == 0 {
		o.MemLimitPercent = 60
	}
	if o.XAvailable == nil {
		display := o.Display
		o.XAvailable = func() bool { return displayReady(display) }
	}
	if o.HWVideo == nil {
		o.HWVideo = func() bool { return false }
	}
	s := &Supervisor{o: o, state: wire.PlayerStarting, restartReq: make(chan string, 1)}
	if fitWindowSupported && o.WindowSize != nil {
		display := o.Display
		s.fit = func(w, h int) (xFitResult, error) { return xFitWindows(display, w, h) }
	}
	return s
}

// Args returns the Chromium command line.
func (s *Supervisor) Args() []string {
	args := []string{
		"--kiosk", "--app=" + s.o.URL,
		"--noerrdialogs", "--disable-infobars", "--no-first-run", "--no-default-browser-check",
		"--disable-translate", "--disable-features=Translate,MediaRouter,OptimizationHints,AutofillServerCommunication",
		"--disable-component-update", "--disable-background-networking", "--disable-sync",
		"--password-store=basic", "--autoplay-policy=no-user-gesture-required",
		"--check-for-update-interval=31536000", "--disable-session-crashed-bubble",
		"--overscroll-history-navigation=0", "--disable-pinch",
		"--user-data-dir=" + s.o.UserDataDir, "--remote-debugging-port=0",
		"--remote-allow-origins=http://127.0.0.1",
		"--disk-cache-size=52428800",
		// The player only loads from the agent on loopback. Everything else goes to a dead
		// proxy, so Chromium's own services (push messaging, safe browsing, DNS over HTTPS,
		// variations …) never reach the internet – no cloud traffic from screens.
		"--proxy-server=http://127.0.0.1:9", "--proxy-bypass-list=127.0.0.1;localhost;[::1]",
		"--disable-domain-reliability", "--disable-client-side-phishing-detection", "--no-pings",
		"--disable-breakpad", "--metrics-recording-only", "--disable-default-apps",
		"--disable-component-extensions-with-background-pages",
		// Signage never runs "in the background": no timer or renderer throttling.
		"--disable-background-timer-throttling", "--disable-renderer-backgrounding",
		"--disable-backgrounding-occluded-windows",
	}
	if platformFlags != "" {
		args = append(args, platformFlags)
	}
	s.mu.Lock()
	w, h := s.winW, s.winH
	s.mu.Unlock()
	if w > 0 && h > 0 {
		args = append(args, "--window-position=0,0", fmt.Sprintf("--window-size=%d,%d", w, h))
	}
	args = append(args, hwVideoArgs(s.o.HWVideo(), s.o.VideoAccel)...)
	return append(args, s.o.ExtraArgs...)
}

// Heartbeat records a player ping.
func (s *Supervisor) Heartbeat(p wire.PlayerPing) {
	s.mu.Lock()
	s.lastPing = time.Now()
	s.item = p.Item
	up := s.state == wire.PlayerStarting
	if up {
		s.state = wire.PlayerOK
	}
	s.mu.Unlock()
	if up {
		slog.Info("player is up")
		s.notify(wire.PlayerOK)
	}
}

func (s *Supervisor) notify(state string) {
	if s.o.OnStateChange != nil {
		s.o.OnStateChange(state)
	}
}

// Status returns the player state, restart count, last error and current item.
func (s *Supervisor) Status() (state string, restarts int, lastError, item string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.restarts, s.lastError, s.item
}

// CheckWindowSize restarts Chromium when the screen size differs from the size its
// window was opened with (display mode changed).
func (s *Supervisor) CheckWindowSize(w, h int) {
	s.mu.Lock()
	changed := s.running && w > 0 && h > 0 && (w != s.winW || h != s.winH)
	s.mu.Unlock()
	if changed {
		slog.Info("screen size changed, restarting the player", "width", w, "height", h)
		s.Restart("screen size changed")
	}
}

// Restart asks for a Chromium restart (remote action restart_client, hwVideo change).
func (s *Supervisor) Restart(reason string) {
	select {
	case s.restartReq <- reason:
	default:
	}
}

func (s *Supervisor) currentCDP() (*CDP, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cdp == nil || s.cdp.Closed() {
		return nil, errors.New("player not connected (no CDP session)")
	}
	return s.cdp, nil
}

// Reload reloads the player page via CDP.
func (s *Supervisor) Reload(ctx context.Context) error {
	c, err := s.currentCDP()
	if err != nil {
		return err
	}
	return c.Reload(ctx)
}

// Screenshot captures the player.
func (s *Supervisor) Screenshot(ctx context.Context) ([]byte, error) {
	c, err := s.currentCDP()
	if err != nil {
		return nil, err
	}
	return c.Screenshot(ctx)
}

func (s *Supervisor) setState(state, errText string) {
	s.mu.Lock()
	changed := s.state != state
	s.state = state
	if errText != "" {
		s.lastError = errText
	}
	s.mu.Unlock()
	if changed {
		s.notify(state)
	}
}

func (s *Supervisor) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	case <-s.restartReq: // a manual restart skips the wait
		return true
	}
}

// Run supervises Chromium until ctx ends.
func (s *Supervisor) Run(ctx context.Context) {
	backoff := s.o.MinBackoff
	waitingForX := false
	for ctx.Err() == nil {
		bin := s.o.Chromium()
		if bin == "" {
			s.setState(wire.PlayerCrashed, "chromium_not_found")
			slog.Error("chromium not found, cannot start the player", "retryIn", backoff.String())
			if !s.sleep(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, s.o.MaxBackoff)
			continue
		}
		if !s.o.XAvailable() {
			if !waitingForX {
				slog.Info("waiting for the X server", "display", s.o.Display)
				waitingForX = true
			}
			s.setState(wire.PlayerStarting, "")
			if !s.sleep(ctx, 2*time.Second) {
				return
			}
			continue
		}
		waitingForX = false
		if s.o.DisplayReady != nil {
			s.o.DisplayReady(ctx)
		}
		started := time.Now()
		reason := s.runOnce(ctx, bin)
		if ctx.Err() != nil {
			s.setState(wire.PlayerRestarting, "")
			return
		}
		s.mu.Lock()
		s.restarts++
		s.mu.Unlock()
		if time.Since(started) >= s.o.StableAfter {
			backoff = s.o.MinBackoff
		}
		if strings.HasPrefix(reason, "manual") || reason == reasonGPULost {
			slog.Info("restarting player", "reason", reason)
			s.setState(wire.PlayerRestarting, "")
			continue
		}
		slog.Warn("player stopped, restarting after backoff", "reason", reason, "backoff", backoff.String())
		s.setState(wire.PlayerCrashed, reason)
		if !s.sleep(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, s.o.MaxBackoff)
		s.setState(wire.PlayerRestarting, "")
	}
}

// ring keeps the last lines of Chromium's output for crash reports.
type ring struct {
	mu    sync.Mutex
	lines []string
}

func (r *ring) consume(rd io.Reader) {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		r.mu.Lock()
		r.lines = append(r.lines, sc.Text())
		if len(r.lines) > 30 {
			r.lines = r.lines[len(r.lines)-30:]
		}
		r.mu.Unlock()
	}
}

func (r *ring) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

// runOnce starts Chromium, watches it and returns why it ended. The process tree and
// the CDP connection are always torn down before returning.
func (s *Supervisor) runOnce(ctx context.Context, bin string) (reason string) {
	if err := PrepareProfile(s.o.UserDataDir); err != nil {
		slog.Warn("chromium profile preparation failed", "err", err)
	}
	if s.o.WindowSize != nil {
		w, h := s.o.WindowSize()
		s.mu.Lock()
		s.winW, s.winH = w, h
		s.mu.Unlock()
	}
	cmd := exec.Command(bin, s.Args()...)
	cmd.Env = append(os.Environ(), "DISPLAY="+s.o.Display)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	out := &ring{}
	safe.Go("chromium-output", func() { out.consume(pr) })
	prepareCmd(cmd)
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return "start_failed: " + err.Error()
	}
	exited := make(chan struct{})
	var waitErr error
	safe.Go("chromium-wait", func() {
		waitErr = cmd.Wait()
		_ = pw.Close()
		close(exited)
	})
	s.mu.Lock()
	changed := s.state != wire.PlayerStarting
	s.state = wire.PlayerStarting
	s.startedAt = time.Now()
	s.lastPing = time.Time{}
	s.running = true
	s.mu.Unlock()
	if changed {
		s.notify(wire.PlayerStarting)
	}
	slog.Info("chromium started", "pid", cmd.Process.Pid, "bin", bin)

	defer func() {
		s.mu.Lock()
		c := s.cdp
		s.cdp = nil
		s.running = false
		s.mu.Unlock()
		if c != nil {
			c.Close()
		}
		killTree(cmd.Process, exited)
		<-exited
		if !strings.HasPrefix(reason, "manual") && reason != "shutdown" && reason != reasonGPULost {
			if tail := out.String(); tail != "" {
				slog.Warn("chromium output before stop", "reason", reason, "exit", fmt.Sprint(waitErr), "output", tail)
			}
		}
	}()

	connectCDP := func() error {
		c, err := s.connectCDP(ctx, exited)
		if err != nil {
			return err
		}
		s.mu.Lock()
		old := s.cdp
		s.cdp = c
		s.mu.Unlock()
		if old != nil {
			old.Close()
		}
		return nil
	}
	if err := connectCDP(); err != nil {
		select {
		case <-exited:
			return "exited during start"
		default:
		}
		slog.Warn("cdp connection failed", "err", err)
	} else if s.o.DisplayReady != nil {
		s.o.DisplayReady(ctx)
	}

	// Window fit: every check until the window is found (at most 2 minutes), then once a
	// minute in case Chromium sets its own size again.
	fitted := false
	var lastFit time.Time
	fit := func() {
		s.mu.Lock()
		w, h := s.winW, s.winH
		s.mu.Unlock()
		if s.fit == nil || w <= 0 || h <= 0 {
			fitted = true
			return
		}
		lastFit = time.Now()
		r, err := s.fit(w, h)
		if err != nil {
			slog.Debug("browser window fit failed", "err", err)
			return
		}
		if r.Fixed > 0 {
			slog.Info("browser window fitted to the screen", "width", w, "height", h)
		}
		if r.Fixed > 0 || r.Exact > 0 {
			fitted = true
		}
	}
	fit()

	ticker := time.NewTicker(s.o.CheckInterval)
	defer ticker.Stop()
	cdpFailures := 0
	lastMem := time.Now()
	memStrikes := 0
	var gpu gpuRun
	s.startGPURun()
	for {
		select {
		case <-ctx.Done():
			return "shutdown"
		case <-exited:
			return fmt.Sprintf("exited (%v)", waitErr)
		case r := <-s.restartReq:
			return "manual: " + r
		case <-ticker.C:
		}
		s.mu.Lock()
		startedAt, lastPing := s.startedAt, s.lastPing
		s.mu.Unlock()
		if (!fitted && time.Since(startedAt) < 2*time.Minute) || time.Since(lastFit) >= time.Minute {
			fit()
		}
		switch {
		case lastPing.IsZero() && time.Since(startedAt) > s.o.FirstPingGrace:
			return "no player ping since start"
		case !lastPing.IsZero() && time.Since(lastPing) > s.o.PingTimeout:
			return "player ping timeout"
		}
		// CDP health; a lost session is re-created, three failures in a row restart.
		c, err := s.currentCDP()
		if err != nil {
			err = connectCDP()
		} else {
			hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = c.Health(hctx)
			cancel()
		}
		if err != nil {
			cdpFailures++
			slog.Warn("cdp health check failed", "failures", cdpFailures, "err", err)
			if cdpFailures >= 3 {
				return "cdp unresponsive"
			}
		} else {
			cdpFailures = 0
		}
		if time.Since(lastMem) >= 30*time.Second {
			lastMem = time.Now()
			if mem, ok := sysinfo.ReadMemory(); ok {
				if memoryOver(sysinfo.ProcessTreePSS(cmd.Process.Pid), mem, s.o.MemLimitPercent) {
					memStrikes++
				} else {
					memStrikes = 0
				}
				// Three checks in a row (~90 s): short peaks (video start, transitions) are fine.
				if memStrikes >= 3 {
					slog.Warn("chromium memory limit exceeded", "pss", sysinfo.ProcessTreePSS(cmd.Process.Pid), "available", mem.Available, "total", mem.Total)
					return "memory limit exceeded"
				}
			}
		}
		s.trackGPUProcess(&gpu, cmd.Process.Pid)
		if cdpFailures == 0 {
			if r := s.checkGPU(ctx, &gpu, startedAt); r != "" {
				return r
			}
		}
	}
}

func (s *Supervisor) connectCDP(ctx context.Context, exited <-chan struct{}) (*CDP, error) {
	deadline := time.Now().Add(s.o.CDPTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-exited:
			return nil, errors.New("chromium exited")
		default:
		}
		port, err := ReadDevToolsPort(s.o.UserDataDir)
		if err == nil {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			var u string
			u, err = PageWebSocketURL(cctx, port, s.o.URL)
			if err == nil {
				var c *CDP
				c, err = DialCDP(cctx, u)
				cancel()
				if err == nil {
					slog.Info("cdp connected", "port", port)
					return c, nil
				}
			} else {
				cancel()
			}
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("no devtools endpoint in %s: %w", filepath.Base(s.o.UserDataDir), lastErr)
}

// memoryOver reports a memory problem of the player: its real use (PSS) is above the limit,
// or the system is about to run out of memory (less than 5 % available) while the player
// holds a large share of it.
func memoryOver(pss uint64, mem sysinfo.Memory, limitPercent float64) bool {
	if mem.Total == 0 {
		return false
	}
	if float64(pss) > float64(mem.Total)*limitPercent/100 {
		return true
	}
	return mem.Available < mem.Total/20 && pss > mem.Total/4
}
