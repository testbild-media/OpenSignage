package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
	"unicode/utf8"

	"opensignage/internal/agent/display"
	"opensignage/internal/agent/link"
	"opensignage/internal/agent/safe"
	"opensignage/internal/agent/state"
	"opensignage/internal/agent/sysinfo"
	"opensignage/internal/agent/wire"
	"opensignage/internal/common/release"
	"opensignage/internal/common/version"
)

// ---------------------------------------------------------------- link.Handler

// Credentials implements link.Handler.
func (a *Agent) Credentials() state.Server {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.server
}

// PairRequest implements link.Handler.
func (a *Agent) PairRequest() wire.PairRequest {
	req := wire.PairRequest{HardwareID: a.hardwareID, Hostname: a.opts.Hostname, Name: a.cfg.PlayerName,
		Version: version.Version, OS: runtime.GOOS, Arch: runtime.GOARCH}
	a.mu.Lock()
	req.IPs = append([]string{}, a.ips...)
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if info, ok := display.Info(ctx, a.display); ok {
		req.Screen = wire.Screen{Width: info.Width, Height: info.Height}
	}
	return req
}

// Hello implements link.Handler.
func (a *Agent) Hello() wire.Hello {
	dep, _ := a.sync.Current()
	h := wire.Hello{Version: version.Version, Protocol: version.Protocol, HardwareID: a.hardwareID,
		ActiveSource: a.sched.Current().Source}
	if dep.Active != nil && !dep.Cleared {
		h.ActiveDeploymentID = dep.Active.ID
	}
	if dep.Previous != nil {
		h.PreviousDeploymentID = dep.Previous.ID
	}
	a.mu.Lock()
	h.IPs = append([]string{}, a.ips...)
	a.mu.Unlock()
	return h
}

// Status implements link.Handler.
func (a *Agent) Status() wire.Status {
	dep, _ := a.sync.Current()
	s := wire.Status{
		Version:      version.Version,
		OS:           runtime.GOOS,
		Arch:         release.Arch(runtime.GOARCH, ""),
		ActiveSource: a.sched.Current().Source,
		Mode:         a.ModeKind(),
		CPU:          a.cpu.Percent(),
		CacheBytes:   a.sync.CacheBytes(),
		Sync:         a.sync.Info(),
		Uptime:       int64(time.Since(a.started).Seconds()),
		Player:       wire.PlayerCrashed,
	}
	if dep.Active != nil && !dep.Cleared {
		s.ActiveDeploymentID = dep.Active.ID
	}
	if mem, ok := sysinfo.ReadMemory(); ok {
		s.RAMTotal, s.RAMUsed = mem.Total, mem.Used()
	}
	if t, ok := sysinfo.Temperature(); ok {
		s.Temperature = &t
	}
	if p, ok := a.power.State(); ok {
		s.Power = &wire.PowerInfo{UnderVoltage: p.UnderVoltage, Events: p.Events}
		if !p.Last.IsZero() {
			s.Power.Last = p.Last.UTC().Format(time.RFC3339)
		}
	}
	s.Video = a.videoStatus()
	s.Clock = a.clockInfo()
	if total, free, err := sysinfo.Disk(a.cfg.DataDir); err == nil {
		s.DiskTotal, s.DiskFree = total, free
	}
	s.LogsBytes = sysinfo.DirSize(a.logDir())
	if a.kiosk != nil {
		s.Player, s.PlayerRestarts, _, s.PlayerItem = a.kiosk.Status()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if info, ok := display.Report(ctx, a.display); ok {
		s.Display = info
	}
	cancel()
	a.mu.Lock()
	s.IPs = append([]string{}, a.ips...)
	s.Applied = a.appliedLocked()
	a.mu.Unlock()
	if us := a.updater.Status(); us.Phase != "" && us.Phase != "idle" {
		s.Update = &wire.UpdateInfo{Phase: us.Phase, Version: us.Version, Error: us.Error, Done: us.Done, Total: us.Total}
	}
	s.Caps = []string{wire.CapCleanCachePreview, wire.CapRegeneration}
	s.Model = sysinfo.Model()
	return s
}

// appliedLocked describes what the agent made of the last device.config (nil before the
// first one). Must hold a.mu.
func (a *Agent) appliedLocked() *wire.AppliedConfig {
	c := a.device.Config
	if c == nil {
		return nil
	}
	return &wire.AppliedConfig{Blackout: c.Blackout, Display: c.Display, DisplayMode: a.device.Display,
		DisplayBusy: a.srvDisplayBusy, DisplayError: a.srvDisplayErr}
}

// logDir is the directory of the agent's log files.
func (a *Agent) logDir() string {
	if a.opts.Logs != nil {
		if files := a.opts.Logs.Files(); len(files) > 0 {
			return filepath.Dir(files[0])
		}
	}
	return filepath.Join(a.cfg.DataDir, "logs")
}

// sendStatus reports the status at once (after a config change was applied), in the
// background: reading the display state may take a moment.
func (a *Agent) sendStatus() {
	safe.Go("status", func() {
		if a.link.Connected() {
			a.link.Send(wire.TStatus, "", a.Status())
		}
	})
}

// OnPaired implements link.Handler: wipe everything of the previous server, keep the
// device settings, store the new credentials.
func (a *Agent) OnPaired(address, fingerprint, deviceID, token, serverID string) error {
	err := a.sync.Reset(a.store.WipeServerData)
	if err != nil {
		slog.Error("wiping previous server data failed", "err", err)
	}
	a.mu.Lock()
	a.server = state.Server{Address: address, Fingerprint: fingerprint, DeviceID: deviceID, Token: token, ServerID: serverID,
		PairedAt: time.Now().UTC().Format(time.RFC3339), ManualAddress: a.server.ManualAddress, AddressSetByUser: a.server.AddressSetByUser}
	a.liveRaw, a.liveHeader, a.welcome = nil, wire.LiveHeader{}, nil
	a.offset, a.clockLocal = 0, time.Time{}
	if d, derr := a.store.LoadDevice(); derr == nil {
		a.device = d
	}
	srv := a.server
	a.mu.Unlock()
	a.setHWVideo("auto")
	a.applyOffset(0)
	if err := a.store.SaveServer(srv); err != nil {
		return fmt.Errorf("save server.json: %w", err)
	}
	a.contentChanged()
	return nil
}

// OnRevoked implements link.Handler.
func (a *Agent) OnRevoked() {
	if err := a.forgetServer(); err != nil {
		slog.Error("wipe after revocation incomplete", "err", err)
	}
}

// OnAddressChanged implements link.Handler.
func (a *Agent) OnAddressChanged(address string) {
	a.mu.Lock()
	a.server.Address = address
	srv := a.server
	a.mu.Unlock()
	if err := a.store.SaveServer(srv); err != nil {
		slog.Error("save server.json failed", "err", err)
	}
	a.refreshPlayer()
}

// OnState implements link.Handler.
func (a *Agent) OnState(s link.State) {
	a.mu.Lock()
	a.linkState = s
	a.mu.Unlock()
	a.refreshPlayer()
}

// LinkState returns the server link state.
func (a *Agent) LinkState() link.State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.linkState
}

// OnConnected implements link.Handler: report the current sync state again so the
// admin UI never shows stale data.
func (a *Agent) OnConnected() {
	if p, ok := a.sync.LastProgress(); ok {
		a.link.Send(wire.TDeployProgress, "", p)
	}
	a.updater.MarkConnected()
}

// OnDisconnected implements link.Handler.
func (a *Agent) OnDisconnected() { a.updater.MarkDisconnected() }

// OnMessage implements link.Handler.
func (a *Agent) OnMessage(env wire.Envelope) {
	defer safe.Recover("message " + env.T)
	switch env.T {
	case wire.TWelcome:
		var w wire.Welcome
		if err := json.Unmarshal(env.P, &w); err != nil {
			slog.Warn("invalid welcome", "err", err)
			return
		}
		a.onWelcome(w)
		a.kickClock() // precise measurement right after connecting
	case wire.TTimePong:
		a.onTimePong(env.P)
	case wire.TDeviceConfig:
		var c wire.DeviceConfig
		if err := json.Unmarshal(env.P, &c); err != nil {
			slog.Warn("invalid device.config", "err", err)
			return
		}
		a.onDeviceConfig(c)
	case wire.TLiveState:
		a.onLiveState(env.P)
	case wire.TDeployAvailable:
		var d wire.DeployAvailable
		if err := json.Unmarshal(env.P, &d); err != nil || d.ID == "" {
			slog.Warn("invalid deploy.available", "err", err)
			return
		}
		a.sync.Announce(d.ID, d.Hash)
	case wire.TDeployCleared:
		var d wire.DeployCleared
		_ = json.Unmarshal(env.P, &d)
		a.sync.Clear(d.Reason)
	case wire.TAction:
		var act wire.Action
		if err := json.Unmarshal(env.P, &act); err != nil {
			slog.Warn("invalid action", "err", err)
			return
		}
		if act.ID == "" {
			act.ID = env.ID
		}
		safe.Go("action "+act.Name, func() { a.runAction(act) })
	case wire.TUpdateOffer:
		var u wire.UpdateOffer
		if err := json.Unmarshal(env.P, &u); err != nil {
			slog.Warn("invalid update.offer", "err", err)
			return
		}
		slog.Info("update offered", "version", u.Version, "size", u.Size)
		a.updater.Offer(u)
	default:
		slog.Debug("unknown message type ignored", "type", env.T)
	}
}

func (a *Agent) onWelcome(w wire.Welcome) {
	if !w.VersionMatch {
		slog.Warn("agent version differs from the server", "agent", version.Version, "server", w.ServerVersion)
	}
	a.mu.Lock()
	a.welcome = &w
	a.mu.Unlock()
	if st, err := time.Parse(time.RFC3339Nano, w.ServerTime); err == nil {
		now := time.Now()
		a.mu.Lock()
		a.clockServer, a.clockLocal = st, now
		a.mu.Unlock()
		off := st.Sub(now)
		if absDur(off) > 2*time.Second {
			slog.Warn("system clock differs from the server, correcting schedule time", "offset", off.String())
		}
		a.setOffset(off)
	}
	a.refreshPlayer()
}

func (a *Agent) onDeviceConfig(c wire.DeviceConfig) {
	var applyDisplay *wire.Display
	var prevDisplay wire.Display
	a.mu.Lock()
	blackoutChanged := a.device.Config == nil || a.device.Config.Blackout != c.Blackout
	a.mu.Unlock()
	d, err := a.store.UpdateDevice(func(d *state.Device) {
		d.Config = &c
		// The server's display setting wins only when it changed since the last
		// config (an admin edit); the default "auto" sent after pairing never
		// overrides a mode set locally (display settings survive re-pairing).
		if d.LastServerDisplay != nil && *d.LastServerDisplay == c.Display {
			return
		}
		first := d.LastServerDisplay == nil
		sd := c.Display
		d.LastServerDisplay = &sd
		if first && sd == (wire.Display{}) {
			return
		}
		prevDisplay = d.Display
		d.Display = sd
		applyDisplay = &sd
	})
	if err != nil {
		slog.Error("save device.json failed", "err", err)
	}
	a.mu.Lock()
	a.device = d
	a.mu.Unlock()
	a.setHWVideo(c.HWVideo)
	if applyDisplay != nil {
		disp := *applyDisplay
		a.mu.Lock()
		a.srvDisplayBusy, a.srvDisplayErr = true, ""
		a.mu.Unlock()
		safe.Go("apply-display", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := a.applyDisplay(ctx, disp)
			if err != nil {
				slog.Error("display mode from server could not be applied, keeping the previous one", "err", err)
				if _, err := a.store.UpdateDevice(func(d *state.Device) { d.Display = prevDisplay }); err != nil {
					slog.Error("save device.json failed", "err", err)
				}
			}
			a.mu.Lock()
			a.srvDisplayBusy, a.srvDisplayErr = false, ""
			if err != nil {
				a.device.Display = prevDisplay
				a.srvDisplayErr = truncateText(err.Error(), 500)
			}
			a.mu.Unlock()
			a.updatePrefs(context.Background(), true)
			a.sendStatus() // the admin sees the change confirmed (or failed) at once
		})
	} else {
		a.updatePrefs(context.Background(), true)
	}
	a.contentChanged() // the display group decides the playlist; refreshes the player
	if blackoutChanged || applyDisplay != nil {
		a.sendStatus()
	}
}

// truncateText shortens s to at most n bytes on a rune boundary.
func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func (a *Agent) onLiveState(raw json.RawMessage) {
	var h wire.LiveHeader
	if err := json.Unmarshal(raw, &h); err != nil {
		slog.Warn("invalid live.state", "err", err)
		return
	}
	stored := append(json.RawMessage{}, raw...)
	if err := a.store.SaveLive(state.Live{ReceivedAt: time.Now().UTC().Format(time.RFC3339), State: stored}); err != nil {
		slog.Error("save live.json failed", "err", err)
	}
	a.mu.Lock()
	a.liveRaw, a.liveHeader = stored, h
	a.mu.Unlock()
	a.contentChanged()
}

// ---------------------------------------------------------------- display

// applySavedDisplay re-applies the stored mode (X became available, Chromium started).
func (a *Agent) applySavedDisplay(ctx context.Context) {
	a.mu.Lock()
	d := a.device.Display
	a.mu.Unlock()
	if d == (wire.Display{}) {
		return
	}
	if err := a.applyDisplay(ctx, d); err != nil && !errors.Is(err, display.ErrUnavailable) {
		slog.Error("re-applying saved display mode failed", "err", err)
	}
}

func (a *Agent) applyDisplay(ctx context.Context, d wire.Display) error {
	err := a.display.Apply(ctx, d)
	a.mu.Lock()
	if err != nil {
		a.displayErr = err.Error()
	} else {
		a.displayErr = ""
	}
	a.mu.Unlock()
	if err == nil && a.kiosk != nil {
		a.kiosk.CheckWindowSize(a.screenSize())
	}
	return err
}

// DisplayOutputs lists outputs and modes for the WebGUI.
func (a *Agent) DisplayOutputs(ctx context.Context) ([]display.Output, error) {
	return a.display.Outputs(ctx)
}

// DisplaySetting returns the saved display mode and the last error.
func (a *Agent) DisplaySetting() (wire.Display, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.device.Display, a.displayErr
}

// SetDisplay applies a mode chosen in the client WebGUI and persists it on success.
func (a *Agent) SetDisplay(ctx context.Context, d wire.Display) error {
	if err := display.Validate(d); err != nil {
		return err
	}
	if err := a.applyDisplay(ctx, d); err != nil {
		return err
	}
	dev, err := a.store.UpdateDevice(func(dev *state.Device) { dev.Display = d })
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.device = dev
	a.srvDisplayErr = "" // a local choice replaces a failed server setting
	a.mu.Unlock()
	slog.Info("display mode set in the client WebGUI", "mode", fmt.Sprintf("%dx%d@%.2f rot %d", d.Width, d.Height, d.Refresh, d.Rotation))
	a.updatePrefs(ctx, true)
	a.sendStatus()
	return nil
}

// ---------------------------------------------------------------- device info for the WebGUI

// DeviceInfo returns device settings and server facts.
type DeviceInfo struct {
	Server        state.Server
	Device        state.Device
	ManualAddress string
	ConfigServer  string // OS_SERVER from agent.env
	Welcome       *wire.Welcome
	ZoneName      string
	HardwareID    string
	Offset        time.Duration
}

// Info returns a copy of the device facts.
func (a *Agent) Info() DeviceInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	info := DeviceInfo{Server: a.server, Device: a.device, ManualAddress: a.manualAddressLocked(), ConfigServer: a.cfg.Server,
		ZoneName: a.liveHeader.Zone.Name, HardwareID: a.hardwareID, Offset: a.offset}
	if a.welcome != nil {
		w := *a.welcome
		info.Welcome = &w
	}
	info.Server.Token = "" // never leaves the agent
	return info
}

// CentralPasswordHash returns the WebGUI password hash managed by the server ("" = local).
func (a *Agent) CentralPasswordHash() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.device.Config != nil {
		return a.device.Config.WebGUIPasswordHash
	}
	return ""
}

// SetLanguage stores the device language (player screens, notices).
func (a *Agent) SetLanguage(lang string) {
	d, err := a.store.UpdateDevice(func(d *state.Device) { d.Language = lang })
	if err != nil {
		slog.Error("save device.json failed", "err", err)
		return
	}
	a.mu.Lock()
	a.device = d
	a.mu.Unlock()
	a.refreshPlayer()
}

// PlayerStatus returns the kiosk state.
func (a *Agent) PlayerStatus() (state string, restarts int, lastError, item string) {
	if a.kiosk == nil {
		return "disabled", 0, "", ""
	}
	return a.kiosk.Status()
}

// ---------------------------------------------------------------- actions

// RestartPlayer restarts Chromium.
func (a *Agent) RestartPlayer() error {
	a.SetRegeneration(false, "restart") // a restarted player starts without regeneration
	if a.kiosk == nil {
		return errors.New("player_not_running")
	}
	a.kiosk.Restart("requested")
	return nil
}

// RebootAllowed checks the minimal sudo rule installed by the installer.
func (a *Agent) RebootAllowed() error {
	if a.opts.Reboot != nil {
		return nil
	}
	if runtime.GOOS != "linux" {
		return errors.New("not_supported")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "sudo", "-n", "-l", "/usr/bin/systemctl", "reboot").CombinedOutput(); err != nil {
		return fmt.Errorf("reboot_not_permitted: %s", bytes.TrimSpace(out))
	}
	return nil
}

// Reboot restarts the device after a short delay (so the answer can go out first).
func (a *Agent) Reboot() error {
	if err := a.RebootAllowed(); err != nil {
		return err
	}
	safe.Go("reboot", func() {
		time.Sleep(2 * time.Second)
		slog.Warn("rebooting device")
		var err error
		if a.opts.Reboot != nil {
			err = a.opts.Reboot()
		} else {
			var out []byte
			out, err = exec.Command("sudo", "-n", "/usr/bin/systemctl", "reboot").CombinedOutput()
			if err != nil {
				err = fmt.Errorf("%w: %s", err, bytes.TrimSpace(out))
			}
		}
		if err != nil {
			slog.Error("reboot failed", "err", err)
		}
	})
	return nil
}

func (a *Agent) runAction(act wire.Action) {
	slog.Info("remote action", "action", act.Name, "id", act.ID)
	res := wire.ActionResult{ID: act.ID, OK: true}
	fail := func(err error) {
		res.OK, res.Error = false, err.Error()
	}
	switch act.Name {
	case wire.ActionSyncNow:
		a.sync.SyncNow()
	case wire.ActionReloadPlayer:
		if a.kiosk == nil {
			fail(errors.New("player_not_running"))
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := a.kiosk.Reload(ctx)
		cancel()
		if err != nil {
			a.hub.Cmd("reload") // the player app can reload itself even without CDP
			fail(err)
		}
	case wire.ActionRestartClient:
		if err := a.RestartPlayer(); err != nil {
			fail(err)
		}
	case wire.ActionRebootDevice:
		if err := a.Reboot(); err != nil {
			fail(err)
		}
	case wire.ActionCleanCache:
		var p wire.CleanCacheParams
		if len(act.Params) > 0 {
			if err := json.Unmarshal(act.Params, &p); err != nil {
				fail(errors.New("invalid_params"))
				break
			}
		}
		clean := a.sync.CleanCache
		if p.DryRun {
			clean = a.sync.PreviewCleanCache // show what would be deleted first
		}
		r, err := clean()
		if err != nil {
			fail(err)
		} else {
			res.Data = r
			if !p.DryRun {
				a.sendStatus() // new cache size at once
			}
		}
	case wire.ActionRegeneration:
		var p wire.RegenerationParams
		if err := json.Unmarshal(act.Params, &p); err != nil || p.On == nil {
			fail(errors.New("invalid_params"))
			break
		}
		a.SetRegeneration(*p.On, "server")
	case wire.ActionScreenshot:
		n, err := a.screenshot(act)
		if err != nil {
			fail(err)
		} else {
			res.Data = map[string]any{"bytes": n}
		}
	default:
		res.OK, res.Error = false, "unknown_action"
	}
	if !res.OK {
		slog.Warn("remote action failed", "action", act.Name, "err", res.Error)
	}
	a.link.Send(wire.TActionResult, "", res)
}

func (a *Agent) screenshot(act wire.Action) (int, error) {
	if a.kiosk == nil {
		return 0, errors.New("player_not_running")
	}
	var p struct {
		RequestID string `json:"requestId"`
	}
	_ = json.Unmarshal(act.Params, &p)
	if p.RequestID == "" {
		p.RequestID = act.ID
	}
	if !state.ValidID(p.RequestID) {
		return 0, errors.New("invalid_request_id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	img, err := a.kiosk.Screenshot(ctx)
	if err != nil {
		return 0, err
	}
	resp, err := a.link.Do(ctx, http.MethodPost, "/device/v1/screenshots/"+p.RequestID,
		http.Header{"Content-Type": {"image/jpeg"}}, bytes.NewReader(img))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("upload failed: HTTP %d", resp.StatusCode)
	}
	return len(img), nil
}

// Screenshot captures the player (diagnostics, tests on hardware).
func (a *Agent) Screenshot(ctx context.Context) ([]byte, error) {
	if a.kiosk == nil {
		return nil, errors.New("player_not_running")
	}
	return a.kiosk.Screenshot(ctx)
}
