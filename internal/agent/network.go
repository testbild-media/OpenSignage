package agent

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"opensignage/internal/agent/netcfg"
	"opensignage/internal/agent/netmgr"
	"opensignage/internal/agent/safe"
	"opensignage/internal/agent/webgui"
	"opensignage/internal/agent/wire"
	"opensignage/internal/common/tlsutil"
)

// networkState is the part of the agent behind the network settings of the client WebGUI.
type networkState struct {
	mgr     *netmgr.Manager
	applyMu sync.Mutex // one change at a time
	mu      sync.Mutex
	lastErr string
	// errLed: a login was already led to the network page because of lastErr.
	errLed bool
	// applying is closed when the change being applied is done (nil = none): a login right
	// after "apply" – before the change took effect – waits for it and then confirms it.
	applying chan struct{}
}

func (a *Agent) netMgr() *netmgr.Manager {
	if a.net.mgr == nil {
		a.net.mgr = &netmgr.Manager{}
	}
	return a.net.mgr
}

// NetworkView implements webgui.NetworkBackend.
func (a *Agent) NetworkView(ctx context.Context) webgui.NetworkView {
	m := a.netMgr()
	st := m.Status(ctx)
	v := webgui.NetworkView{Status: st, Pending: m.Pending(), Configured: map[string]*netcfg.Config{}}
	v.Available = m.HelperError(ctx) == nil
	for _, it := range st.Interfaces {
		v.Configured[it.Name] = m.Configured(it.Name)
	}
	if v.Available {
		if hints, err := m.Firmware(ctx); err == nil {
			for _, h := range hints {
				v.Firmware = append(v.Firmware, webgui.FirmwareHint{File: h.File, Package: h.Package})
			}
		}
	}
	v.Country = st.Country
	if v.Country == "" {
		v.Country = countryOfZone(a.sched.Location().String())
	}
	a.net.mu.Lock()
	v.LastError = a.net.lastErr
	a.net.mu.Unlock()
	return v
}

// ScanWifi implements webgui.NetworkBackend: with the radio rules in effect, before the
// first network with those of the time zone (5 GHz networks need them).
func (a *Agent) ScanWifi(ctx context.Context, iface string) ([]netcfg.AP, error) {
	m := a.netMgr()
	country := m.Status(ctx).Country
	if country == "" {
		country = countryOfZone(a.sched.Location().String())
	}
	return m.Scan(ctx, iface, country)
}

// ApplyNetwork implements webgui.NetworkBackend: the change is applied shortly after the
// answer (it may cut the connection of the browser that asked for it).
func (a *Agent) ApplyNetwork(c netcfg.Config) error {
	check := c
	if c.Static != nil {
		s := *c.Static
		check.Static = &s
	}
	if err := check.Validate(); err != nil {
		return err
	}
	if err := a.netMgr().HelperError(context.Background()); err != nil {
		return err
	}
	done := make(chan struct{})
	a.net.mu.Lock()
	a.net.lastErr, a.net.errLed = "", false
	a.net.applying = done
	a.net.mu.Unlock()
	safe.Go("network-apply", func() {
		a.applyNetwork(c, done)
		// New addresses: show them at once on the screen, and again when DHCP was slow.
		for _, wait := range []time.Duration{0, 10 * time.Second, 30 * time.Second} {
			time.Sleep(wait)
			a.refreshIPs()
		}
	})
	return nil
}

// applyNetwork applies one change (one at a time) and closes done when it took effect.
func (a *Agent) applyNetwork(c netcfg.Config, done chan struct{}) {
	a.net.applyMu.Lock()
	defer a.net.applyMu.Unlock()
	defer func() {
		a.net.mu.Lock()
		if a.net.applying == done {
			a.net.applying = nil
		}
		a.net.mu.Unlock()
		close(done)
	}()
	time.Sleep(1500 * time.Millisecond) // the answer reaches the browser first
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	slog.Info("applying network change", "interface", c.Interface, "kind", c.Kind, "method", c.Method, "disabled", c.Disabled)
	if err := a.netMgr().Apply(ctx, c); err != nil {
		slog.Error("network change failed, the previous settings are active again", "interface", c.Interface, "err", err)
		a.net.mu.Lock()
		a.net.lastErr = err.Error()
		a.net.mu.Unlock()
	}
}

// NetworkPending implements webgui.NetworkBackend.
func (a *Agent) NetworkPending() bool {
	a.net.mu.Lock()
	applying := a.net.applying != nil
	a.net.mu.Unlock()
	return applying || a.netMgr().Pending() != nil
}

// NetworkFailed implements webgui.NetworkBackend: true once after a change failed.
func (a *Agent) NetworkFailed() bool {
	a.net.mu.Lock()
	defer a.net.mu.Unlock()
	if a.net.lastErr == "" || a.net.errLed {
		return false
	}
	a.net.errLed = true
	return true
}

// ConfirmNetwork implements webgui.NetworkBackend.
func (a *Agent) ConfirmNetwork(ctx context.Context) error {
	a.net.mu.Lock()
	done := a.net.applying
	a.net.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if a.netMgr().Pending() == nil {
		return webgui.ErrNothingToConfirm
	}
	err := a.netMgr().Confirm(ctx)
	a.refreshPlayer()
	return err
}

// InstallFirmware implements webgui.NetworkBackend.
func (a *Agent) InstallFirmware(ctx context.Context, pkg string) error {
	return a.netMgr().InstallFirmware(ctx, pkg)
}

// refreshIPs reads the addresses now and updates the screen when they changed.
func (a *Agent) refreshIPs() {
	ips := tlsutil.InterfaceIPs(false)
	a.mu.Lock()
	changed := strings.Join(ips, ",") != strings.Join(a.ips, ",")
	a.ips = ips
	a.mu.Unlock()
	if changed {
		slog.Info("ip addresses changed", "ips", ips)
	}
	a.refreshPlayer()
}

// networkNoticeLocked is the text on the screen while a network change waits for the
// confirmation (nil = none). Must hold a.mu.
func (a *Agent) networkNoticeLocked(lang string) *string {
	if a.netMgr().Pending() == nil {
		return nil
	}
	var urls []string
	for _, ip := range a.ips {
		if a4 := net.ParseIP(ip); a4 != nil && a4.To4() != nil {
			urls = append(urls, "https://"+net.JoinHostPort(ip, strconv.Itoa(a.cfg.WebGUIPort)))
		}
	}
	if len(urls) == 0 {
		urls = append(urls, a.webguiURLLocked())
	}
	text := a.i18n.T(lang, "client.notice.networkPending", "urls", strings.Join(urls, "  ·  "))
	return &text
}

// networkCleanup runs once at start: after a reboot without confirmation it restores the
// system files an unconfirmed change touched.
func (a *Agent) networkCleanup(ctx context.Context) {
	if err := a.netMgr().Cleanup(ctx); err != nil && !errors.Is(err, netmgr.ErrHelperMissing) {
		slog.Error("network cleanup after reboot failed", "err", err)
	}
}

// networkReport is the network part of the status report.
func (a *Agent) networkReport(ctx context.Context) *wire.StatusNetwork {
	m := a.netMgr()
	st := m.Status(ctx)
	out := &wire.StatusNetwork{Interfaces: []wire.StatusNetIface{}, Pending: m.Pending() != nil}
	for _, it := range st.Interfaces {
		out.Interfaces = append(out.Interfaces, wire.StatusNetIface{Name: it.Name, Kind: it.Kind,
			Connected: it.Carrier && (it.Kind == netcfg.KindEthernet || it.SSID != ""), IPv4: it.IPv4, Method: it.Method,
			SpeedMbps: it.SpeedMbps, SSID: it.SSID, SignalDBM: it.SignalDBM, FreqMHz: it.FreqMHz})
	}
	return out
}

// countryOfZone is the country of a time zone from the system's zone table ("" = unknown).
func countryOfZone(zone string) string {
	if zone == "" || zone == "UTC" || zone == "Local" {
		return ""
	}
	for _, f := range []string{"/usr/share/zoneinfo/zone.tab", "/usr/share/zoneinfo/zone1970.tab"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if c := zoneCountry(string(b), zone); c != "" {
			return c
		}
	}
	return ""
}

func zoneCountry(table, zone string) string {
	for _, line := range strings.Split(table, "\n") {
		f := strings.Split(line, "\t")
		if len(f) >= 3 && !strings.HasPrefix(line, "#") && f[2] == zone {
			c := strings.Split(f[0], ",")[0]
			if netcfg.ValidCountry(c) {
				return c
			}
		}
	}
	return ""
}
