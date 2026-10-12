// Package agent wires the client service together: server link, sync, scheduler,
// loopback player server, kiosk supervisor, display provider and client WebGUI. It owns
// the in-memory view of the state files and derives everything the player shows from
// it (one snapshot, sent as diffs).
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"opensignage/internal/agent/config"
	"opensignage/internal/agent/display"
	"opensignage/internal/agent/kiosk"
	"opensignage/internal/agent/link"
	"opensignage/internal/agent/player"
	"opensignage/internal/agent/safe"
	"opensignage/internal/agent/scheduler"
	"opensignage/internal/agent/state"
	"opensignage/internal/agent/syncer"
	"opensignage/internal/agent/sysinfo"
	"opensignage/internal/agent/updater"
	"opensignage/internal/agent/wire"
	"opensignage/internal/common/ids"
	"opensignage/internal/common/logging"
	"opensignage/internal/common/release"
	"opensignage/internal/common/tlsutil"
	"opensignage/internal/common/version"
	"opensignage/locales"
)

// Options allow tests to replace platform parts.
type Options struct {
	Display  display.Provider // nil = X11 on Linux, none elsewhere
	NoKiosk  bool             // do not supervise Chromium (tests, headless)
	Logs     *logging.RotatingFile
	Now      func() time.Time
	Reboot   func() error // nil = sudo -n systemctl reboot
	Hostname string
}

// Agent is the running client service.
type Agent struct {
	cfg     *config.Config
	opts    Options
	store   *state.Store
	i18n    *locales.Bundle
	started time.Time

	display display.Provider
	hub     *player.Hub
	sync    *syncer.Syncer
	sched   *scheduler.Scheduler
	link    *link.Link
	kiosk   *kiosk.Supervisor
	updater *updater.Updater
	exitc   chan string
	player  *player.Server
	cpu     sysinfo.CPU
	power   *sysinfo.Power

	prefs      atomic.Pointer[syncer.Prefs]
	hwVideo    atomic.Bool      // hardware decoding in use (setHWVideo)
	videoAccel kiosk.VideoAccel // hardware decoding path of this device
	hw         hwVideoState

	mu          sync.Mutex
	server      state.Server
	device      state.Device
	liveRaw     json.RawMessage
	liveHeader  wire.LiveHeader
	linkState   link.State
	welcome     *wire.Welcome
	clockServer time.Time // server time at the last welcome
	clockLocal  time.Time // local time (monotonic) at the last welcome
	offset      time.Duration
	clock       clockState    // measurements for synchronous playback (clock.go)
	clockKick   chan struct{} // measure against the server now (after connecting)
	hardwareID  string
	ips         []string
	displayErr  string
	updating    bool
	// regen: plasma regeneration (white noise) is on. Volatile on purpose: it ends when
	// switched off, with a player restart or when the agent restarts.
	regen bool
	// State of the display setting last received from the server (reported as
	// status.applied so the admin sees a change as pending until it is applied).
	srvDisplayBusy bool
	srvDisplayErr  string
}

// New loads the state and builds all components.
func New(cfg *config.Config, opts Options) (*Agent, error) {
	st, err := state.New(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	bundle, err := locales.NewBundle()
	if err != nil {
		return nil, fmt.Errorf("translations: %w", err)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Hostname == "" {
		opts.Hostname, _ = os.Hostname()
	}
	a := &Agent{cfg: cfg, opts: opts, store: st, i18n: bundle, started: time.Now(), hub: player.NewHub(), power: sysinfo.NewPower(),
		clockKick: make(chan struct{}, 1)}
	if a.server, err = st.LoadServer(); err != nil {
		return nil, fmt.Errorf("server.json: %w", err)
	}
	if a.device, err = st.LoadDevice(); err != nil {
		slog.Error("device.json unreadable, using defaults", "err", err)
		a.device = state.Device{}
	}
	a.hardwareID = a.loadHardwareID()
	if live, err := st.LoadLive(); err != nil {
		slog.Error("live.json unreadable", "err", err)
	} else if len(live.State) > 0 {
		var h wire.LiveHeader
		if json.Unmarshal(live.State, &h) == nil {
			a.liveRaw, a.liveHeader = live.State, h
		}
	}
	if c, err := st.LoadClock(); err == nil {
		a.offset = time.Duration(c.OffsetMS) * time.Millisecond
	}
	a.ips = tlsutil.InterfaceIPs(false)

	a.display = opts.Display
	if a.display == nil {
		if runtime.GOOS == "linux" {
			a.display = display.NewX11(cfg.Display)
		} else {
			a.display = display.None{}
		}
	}
	a.updatePrefs(context.Background(), false)
	a.videoAccel = kiosk.DetectVideoAccel()
	hwMode := "auto"
	if a.device.Config != nil {
		hwMode = a.device.Config.HWVideo
	}
	a.setHWVideo(hwMode)
	slog.Info("hardware video decoding", "setting", hwMode, "on", a.hwVideo.Load(), "path", string(a.videoAccel))

	a.link = link.New(a, cfg.DiscoveryPort)
	a.sync = syncer.New(syncer.Options{
		Store:  st,
		Client: a.link,
		Prefs:  func() syncer.Prefs { return *a.prefs.Load() },
		FreeSpace: func(dir string) (uint64, error) {
			_, free, err := sysinfo.Disk(dir)
			return free, err
		},
		OnProgress: func(p wire.DeployProgress) { a.link.Send(wire.TDeployProgress, "", p) },
		OnChange:   a.contentChanged,
	})
	a.sched = scheduler.New(nil, func(scheduler.Output) { a.scheduleChanged() })
	a.applyOffset(a.offset)
	a.player = &player.Server{Port: cfg.PlayerPort, ShareDir: cfg.ShareDir, Files: a.sync, Hub: a.hub,
		OnPing: func(p wire.PlayerPing) {
			if a.kiosk != nil {
				a.kiosk.Heartbeat(p)
			}
			a.updater.MarkPing()
		},
		OnLog: a.playerLog}
	if !opts.NoKiosk {
		a.kiosk = kiosk.New(kiosk.Options{
			Chromium: func() string {
				if cfg.Chromium != "" {
					return cfg.Chromium
				}
				return config.DetectChromium()
			},
			UserDataDir:  cfg.DataDir + string(os.PathSeparator) + "chromium",
			Display:      cfg.Display,
			URL:          fmt.Sprintf("http://127.0.0.1:%d/", cfg.PlayerPort),
			HWVideo:      a.hwVideo.Load,
			VideoAccel:   a.videoAccel,
			DisplayReady: a.applySavedDisplay,
			WindowSize:   a.screenSize,
			OnStateChange: func(state string) {
				if state == wire.PlayerCrashed {
					a.playerCrashed(time.Now())
				}
				if a.link != nil && a.link.Connected() {
					a.link.Send(wire.TStatus, "", a.Status())
				}
			},
			// GPU lost or back: the admin sees it at once (gpu_lost alert).
			OnGPUChange: func() {
				if a.link != nil && a.link.Connected() {
					a.link.Send(wire.TStatus, "", a.Status())
				}
			},
		})
	}
	a.exitc = make(chan string, 1)
	a.updater = updater.New(updater.Options{
		DataDir: cfg.DataDir, InstallDir: cfg.InstallDir, Version: version.Version,
		OS: runtime.GOOS, Arch: release.Arch(runtime.GOARCH, ""),
		Server: a.link.Get, Internet: updater.InternetGetter(),
		// Built-in keys plus the device's own configuration; never keys of the server.
		TrustedKeys: append(release.BuiltinKeys(), cfg.TrustedKeys...),
		FreeSpace: func(dir string) (uint64, error) {
			_, free, err := sysinfo.Disk(dir)
			return free, err
		},
		Report:      func(p wire.UpdateProgress) bool { return a.link.Send(wire.TUpdateProgress, "", p) },
		SetUpdating: a.setUpdating,
		Exit:        a.requestExit,
	})
	a.updater.Startup() // may request an immediate exit (rollback)
	// A manual address that differs from the paired one (installer re-run with
	// --server) means: pair with the new server.
	if a.server.Paired() {
		if m := a.ManualAddress(); m != "" && m != a.server.Address {
			slog.Warn("configured server address differs from the paired server, re-pairing", "configured", m, "paired", a.server.Address)
			if err := a.forgetServer(); err != nil {
				return nil, err
			}
		}
	}
	a.contentChanged()
	return a, nil
}

// Store gives the WebGUI access to the state files.
func (a *Agent) Store() *state.Store { return a.store }

// I18n returns the translation bundle.
func (a *Agent) I18n() *locales.Bundle { return a.i18n }

// Config returns the configuration.
func (a *Agent) Config() *config.Config { return a.cfg }

// Hub returns the player hub (tests).
func (a *Agent) Hub() *player.Hub { return a.hub }

// Syncer returns the syncer (tests).
func (a *Agent) Syncer() *syncer.Syncer { return a.sync }

// PlayerHandler returns the loopback HTTP handler (tests).
func (a *Agent) PlayerHandler() http.Handler { return a.player.Handler() }

func (a *Agent) loadHardwareID() string {
	if b, err := os.ReadFile("/etc/machine-id"); err == nil {
		if id := strings.TrimSpace(string(b)); len(id) >= 16 {
			sum := sha256.Sum256([]byte("opensignage-hardware-id:" + id)) // never send the raw machine-id
			return hex.EncodeToString(sum[:16])
		}
	}
	if a.device.HardwareID != "" {
		return a.device.HardwareID
	}
	id := ids.Token(16)
	d, err := a.store.UpdateDevice(func(d *state.Device) { d.HardwareID = id })
	if err != nil {
		slog.Error("persisting generated hardware id failed", "err", err)
	} else {
		a.device = d
	}
	return id
}

// requestExit ends Run cleanly; systemd then starts the binary behind `current`.
func (a *Agent) requestExit(reason string) {
	select {
	case a.exitc <- reason:
	default:
	}
}

func (a *Agent) setUpdating(on bool) {
	a.mu.Lock()
	a.updating = on
	a.mu.Unlock()
	a.refreshPlayer()
}

// Regeneration reports whether plasma regeneration is on.
func (a *Agent) Regeneration() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.regen
}

// SetRegeneration switches plasma regeneration (full-screen white noise against burn-in)
// on or off. source is logged: "webgui" or "server".
func (a *Agent) SetRegeneration(on bool, source string) {
	a.mu.Lock()
	changed := a.regen != on
	a.regen = on
	a.mu.Unlock()
	if !changed {
		return
	}
	slog.Info("plasma regeneration", "on", on, "source", source)
	a.refreshPlayer()
	a.sendStatus()
}

// Updater returns the updater (tests).
func (a *Agent) Updater() *updater.Updater { return a.updater }

// Run starts every component and blocks until ctx ends or a restart is requested
// (update installed, rollback).
func (a *Agent) Run(ctx context.Context, web interface{ Run(context.Context) error }) error {
	select {
	case r := <-a.exitc:
		slog.Warn("agent exiting before start", "reason", r)
		return nil
	default:
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)
	safe.Go("player-server", func() {
		if err := a.player.Run(ctx); err != nil {
			errc <- err
		}
	})
	if web != nil {
		safe.Go("webgui", func() {
			if err := web.Run(ctx); err != nil {
				errc <- err
			}
		})
	}
	safe.Go("power", func() { a.power.Run(ctx) })
	safe.Loop(ctx, "link", a.link.Run)
	safe.Loop(ctx, "sync", a.sync.Run)
	safe.Loop(ctx, "scheduler", a.sched.Run)
	safe.Loop(ctx, "housekeeping", a.housekeeping)
	safe.Loop(ctx, "clock", a.clockLoop)
	peers := a.clockPeers()
	safe.Loop(ctx, "clock-lan", func(ctx context.Context) {
		if err := peers.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("LAN time exchange unavailable, retrying", "port", a.cfg.ClockPort, "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Minute):
			}
		}
	})
	safe.Go("updater", func() { a.updater.Run(ctx) })
	if a.kiosk != nil {
		safe.Loop(ctx, "kiosk", a.kiosk.Run)
	} else if a.kioskWanted() {
		slog.Warn("kiosk disabled")
	}
	slog.Info("agent started", "version", version.Version, "data", a.cfg.DataDir, "player", a.cfg.PlayerPort,
		"webgui", a.cfg.WebGUIPort, "paired", a.server.Paired(), "display", a.display.Name())
	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	case r := <-a.exitc:
		slog.Info("agent exiting for a restart", "reason", r)
		cancel()
		time.Sleep(2500 * time.Millisecond) // Chromium stops, close frames go out
		return nil
	}
	time.Sleep(300 * time.Millisecond) // let the WebSocket close frames go out
	return nil
}

func (a *Agent) kioskWanted() bool { return runtime.GOOS == "linux" }

// screenSize is the size the content sees (after rotation); 0 when unknown.
func (a *Agent) screenSize() (int, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if info, ok := display.Info(ctx, a.display); ok {
		return info.Width, info.Height
	}
	return 0, 0
}

// housekeeping refreshes IP addresses and re-checks the clock correction.
func (a *Agent) housekeeping(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ips := tlsutil.InterfaceIPs(false)
		a.mu.Lock()
		changed := strings.Join(ips, ",") != strings.Join(a.ips, ",")
		a.ips = ips
		var off time.Duration
		haveRef := !a.clockLocal.IsZero()
		if haveRef {
			// Wall-clock jumps (NTP, RTC-less boot) do not affect the monotonic delta.
			off = a.clockServer.Add(time.Since(a.clockLocal)).Sub(time.Now())
		}
		cur := a.offset
		a.mu.Unlock()
		if haveRef && absDur(off-cur) > time.Second {
			slog.Info("system clock moved, updating server time correction", "offset", off.String())
			a.setOffset(off)
		}
		if changed {
			slog.Info("ip addresses changed", "ips", ips)
			a.refreshPlayer()
		}
	}
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// ---------------------------------------------------------------- derived state

// lang is the device language: a local choice in the client WebGUI wins, otherwise the
// server's default language, otherwise English.
func (a *Agent) lang() string {
	if a.device.Language != "" && locales.Supported(a.device.Language) {
		return a.device.Language
	}
	if c := a.device.Config; c != nil && locales.Supported(c.Language) {
		return c.Language
	}
	return locales.Default
}

// T translates a client namespace key in the device language.
func (a *Agent) T(key string, params ...any) string {
	a.mu.Lock()
	l := a.lang()
	a.mu.Unlock()
	return a.i18n.T(l, "client."+key, params...)
}

// WebGUIURL is the address of the client WebGUI (first IPv4 address).
func (a *Agent) WebGUIURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.webguiURLLocked()
}

func (a *Agent) webguiURLLocked() string {
	host := a.opts.Hostname
	if len(a.ips) > 0 {
		host = a.ips[0]
	}
	return "https://" + net.JoinHostPort(host, strconv.Itoa(a.cfg.WebGUIPort)) + "/"
}

func (a *Agent) contentChanged() {
	dep, active := a.sync.Current()
	a.mu.Lock()
	closed := a.liveHeader.Zone.State == "CLOSED"
	var closedSince time.Time
	if closed {
		closedSince, _ = time.Parse(time.RFC3339Nano, a.liveHeader.Zone.StateChangedAt) // unknown: midnight
	}
	group := ""
	if c := a.device.Config; c != nil {
		group = c.GroupID
	}
	a.mu.Unlock()
	var m *wire.Manifest
	if active != nil {
		m = active.M
	}
	a.sched.SetInputs(scheduler.Inputs{Manifest: m, Cleared: dep.Cleared, ZoneClosed: closed, GroupID: group, ClosedSince: closedSince})
	a.refreshPlayer()
}

func (a *Agent) scheduleChanged() {
	a.refreshPlayer()
	if a.link.Connected() {
		a.link.Send(wire.TStatus, "", a.Status())
	}
}

// ModeKind returns the current player mode.
func (a *Agent) ModeKind() string { return a.snapshot().Mode.Kind }

func (a *Agent) refreshPlayer() { a.hub.Set(a.snapshot()) }

func (a *Agent) snapshot() player.Snapshot {
	dep, active := a.sync.Current()
	out := a.sched.Current()
	tz := a.sched.Location().String()
	a.mu.Lock()
	defer a.mu.Unlock()
	lang := a.lang()
	cfg := a.device.Config
	mode := wire.PlayerMode{IPs: append([]string{}, a.ips...), WebGUIURL: a.webguiURLLocked(), Language: lang}
	if cfg != nil {
		mode.DeviceName = cfg.Name
	}
	if notice := a.linkNoticeLocked(lang); notice != "" {
		mode.Notice = notice
	}
	switch {
	case a.updating:
		mode.Kind = wire.ModeUpdating
	case a.regen: // before pairing and blackout: it was switched on explicitly
		mode.Kind = wire.ModeRegeneration
	case !a.server.Paired():
		ls := a.linkState
		if ls.Phase == link.PhasePairing && ls.Pairing != nil {
			mode.Kind = wire.ModePairing
			mode.PIN = ls.Pairing.PIN
			mode.Fingerprint = tlsutil.ShortFingerprint(ls.Pairing.Fingerprint)
			mode.ServerAddress = ls.Pairing.Address
		} else {
			mode.Kind = wire.ModeSearching
			mode.ServerAddress = a.manualAddressLocked()
		}
	case cfg != nil && cfg.Blackout:
		mode.Kind = wire.ModeBlackout
	case cfg != nil && cfg.ZoneID == "":
		// Before "cleared": the server clears the content of a device without a zone, and
		// the standby screen tells the installer what is missing.
		mode.Kind = wire.ModeStandby
	case dep.Cleared:
		mode.Kind = wire.ModeCleared
	case active == nil:
		mode.Kind = wire.ModeStandby
	case out.Source.Kind == wire.SourceCleared:
		mode.Kind = wire.ModeCleared
	default:
		mode.Kind = wire.ModePlaying
	}
	if a.server.Paired() {
		mode.ServerAddress = a.server.Address
		mode.Fingerprint = tlsutil.ShortFingerprint(a.server.Fingerprint)
	}

	pc := wire.PlayerConfig{ScaleMode: "contain", Perf: a.prefs.Load().Perf, ShowIPOverlay: true, Timezone: tz,
		ClockOffsetMS: a.offset.Milliseconds()}
	if cfg != nil {
		if cfg.ScaleMode != "" {
			pc.ScaleMode = cfg.ScaleMode
		}
		pc.PlasmaShift = cfg.PlasmaShift
		pc.ShowIPOverlay = cfg.ShowIPOverlay
	}
	bundle := wire.PlayerBundle{}
	if active != nil {
		bundle = wire.PlayerBundle{Hash: active.Ref.Hash, Manifest: active.Raw}
		if bundle.Hash == "" {
			bundle.Hash = active.M.Hash
		}
	}
	src := wire.PlayerSource{Reason: out.Reason, Position: 1}
	if out.Source.PlaylistID != "" {
		id := out.Source.PlaylistID
		src.PlaylistID = &id
	}
	if !out.Since.IsZero() {
		ms := out.Since.UnixMilli()
		src.Anchor = &ms
	}
	if cfg != nil && cfg.Position > 0 {
		src.Position = cfg.Position
	}
	live := wire.PlayerLive{State: a.liveRaw, Prices: out.Prices, Ticker: out.Ticker}
	if len(live.State) == 0 {
		live.State = json.RawMessage("null")
	}
	vn := wire.VersionNotice{}
	if a.welcome != nil && !a.welcome.VersionMatch {
		text := a.i18n.T(lang, "client.notice.versionMismatch")
		vn.Text = &text
	}
	return player.Snapshot{Mode: mode, Config: pc, Bundle: bundle, Source: src, Live: live, VersionNotice: vn}
}

func (a *Agent) linkNoticeLocked(lang string) string {
	if a.linkState.Phase != link.PhaseError || a.linkState.Error == "" {
		return ""
	}
	return a.i18n.T(lang, "client.notice."+a.linkState.Error)
}

// ---------------------------------------------------------------- clock

func (a *Agent) applyOffset(off time.Duration) {
	if absDur(off) <= 2*time.Second {
		off = 0
	}
	a.sched.SetOffset(off)
}

func (a *Agent) setOffset(off time.Duration) {
	a.mu.Lock()
	a.offset = off
	a.mu.Unlock()
	if err := a.store.SaveClock(state.Clock{OffsetMS: off.Milliseconds(), MeasuredAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		slog.Error("save clock.json failed", "err", err)
	}
	a.applyOffset(off)
	a.refreshPlayer()
}

// ---------------------------------------------------------------- preferences

// updatePrefs recomputes perf + screen width; the syncer re-selects variants on change.
func (a *Agent) updatePrefs(ctx context.Context, notify bool) {
	a.mu.Lock()
	perf := "auto"
	if a.device.Config != nil && a.device.Config.Perf != "" {
		perf = a.device.Config.Perf
	}
	a.mu.Unlock()
	if perf == "auto" {
		perf = sysinfo.AutoPerf()
	}
	width := 1920
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if info, ok := display.Info(dctx, a.display); ok && info.Width > 0 {
		width = info.Width
	}
	cancel()
	p := syncer.Prefs{Perf: perf, ScreenWidth: width}
	old := a.prefs.Swap(&p)
	if notify && (old == nil || *old != p) && a.sync != nil {
		slog.Info("content preferences changed", "perf", p.Perf, "screenWidth", p.ScreenWidth)
		a.sync.PrefsChanged()
		a.refreshPlayer()
	}
}

// ---------------------------------------------------------------- server address

// ManualAddress implements link.Handler.
func (a *Agent) ManualAddress() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.manualAddressLocked()
}

func (a *Agent) manualAddressLocked() string {
	if a.server.AddressSetByUser {
		return a.server.ManualAddress
	}
	return a.cfg.Server
}

// forgetServer wipes credentials and all server data (device settings stay).
func (a *Agent) forgetServer() error {
	err := a.sync.Reset(a.store.WipeServerData)
	a.mu.Lock()
	a.server = state.Server{ManualAddress: a.server.ManualAddress, AddressSetByUser: a.server.AddressSetByUser}
	a.liveRaw, a.liveHeader, a.welcome = nil, wire.LiveHeader{}, nil
	a.offset, a.clockLocal = 0, time.Time{}
	if d, derr := a.store.LoadDevice(); derr == nil {
		a.device = d
	}
	srv := a.server
	a.mu.Unlock()
	if serr := a.store.SaveServer(srv); serr != nil {
		err = errors.Join(err, serr)
	}
	a.setHWVideo("auto")
	a.applyOffset(0)
	a.contentChanged()
	return err
}

// SetServerAddress sets a manual address ("" = automatic discovery). A paired device keeps
// its pairing and content: it uses its token at the new address only when the server there
// presents the pinned identity (the same server under another address, e.g. a VPN or a new
// IP). Another server is refused like any certificate change; "Pair again" switches servers.
func (a *Agent) SetServerAddress(addr string) error {
	if addr != "" {
		n, err := config.NormalizeAddress(addr)
		if err != nil {
			return err
		}
		addr = n
	}
	a.mu.Lock()
	a.server.ManualAddress, a.server.AddressSetByUser = addr, true
	paired := a.server.Paired()
	if paired && addr != "" {
		a.server.Address = addr
	}
	srv := a.server
	a.mu.Unlock()
	if !paired {
		slog.Info("server address changed in the client WebGUI", "address", addr)
		err := a.forgetServer()
		a.link.Reset()
		return err
	}
	slog.Info("server address changed in the client WebGUI, keeping the pairing", "address", addr)
	err := a.store.SaveServer(srv)
	a.link.Reset()
	return err
}

// Repair forgets the server and requests a new PIN.
func (a *Agent) Repair() error {
	slog.Info("re-pairing requested in the client WebGUI")
	err := a.forgetServer()
	a.link.Reset()
	return err
}

// FactoryReset also forgets device settings, the address choice and the password.
func (a *Agent) FactoryReset() error {
	slog.Warn("factory reset requested in the client WebGUI")
	err := a.sync.Reset(a.store.FactoryReset)
	a.mu.Lock()
	a.server = state.Server{}
	a.device = state.Device{HardwareID: a.device.HardwareID}
	a.liveRaw, a.liveHeader, a.welcome = nil, wire.LiveHeader{}, nil
	a.offset, a.clockLocal = 0, time.Time{}
	a.mu.Unlock()
	a.applyOffset(0)
	a.contentChanged()
	a.link.Reset()
	return err
}
