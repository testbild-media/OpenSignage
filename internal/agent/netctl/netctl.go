// Package netctl is the root helper behind the network settings of the client WebGUI
// (cmd/opensignage-netctl, run by the agent with sudo). It accepts only fixed commands and
// validates every input again:
//
//   - apply: a change takes effect at once but only until the next reboot (files under
//     /run); the device keeps its previous settings on disk. A change that does not come up
//     is undone at once (the interface would have no address until the next reboot).
//   - confirm: makes the pending change permanent (after a new login to the WebGUI).
//   - cleanup: after a reboot without confirmation, restores system files an unconfirmed
//     change touched (DNS).
//   - scan, firmware, install-firmware: wireless networks around, missing network firmware.
//
// Debian configures its network with ifupdown (/etc/network/interfaces, wpa_supplicant,
// dhcpcd), Raspberry Pi OS with NetworkManager; both are handled.
package netctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"encoding/json"

	"opensignage/internal/agent/netcfg"
	"opensignage/internal/common/ids"
)

// Helper performs the commands on a system (Root "" = the real one; tests use a directory).
type Helper struct {
	Root string
	// Run executes a system program (nil = exec with a timeout).
	Run func(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error)
	Now func() time.Time
	// Sleep waits (scans); nil = time.Sleep.
	Sleep func(time.Duration)
}

// Error is a failure with the field and reason the WebGUI translates.
type Error = netcfg.InputError

func (h *Helper) p(path string) string {
	if h.Root == "" {
		return path
	}
	return filepath.Join(h.Root, filepath.FromSlash(path))
}

func (h *Helper) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Helper) run(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	if h.Run != nil {
		return h.Run(ctx, timeout, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, netcfg.Tool(name), args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C", "DEBIAN_FRONTEND=noninteractive"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		var lines []string
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if !strings.HasPrefix(l, "Hint: ") { // nmcli: "use journalctl …"
				lines = append(lines, l)
			}
		}
		msg := strings.Join(lines, " ")
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
	}
	return out, nil
}

func (h *Helper) sleep(d time.Duration) {
	if h.Sleep != nil {
		h.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (h *Helper) env() netcfg.Env { return netcfg.Env{Root: h.Root} }

func (h *Helper) write(path string, data string, mode os.FileMode) error {
	full := h.p(path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp := full + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, full)
}

func (h *Helper) read(path string) string {
	b, err := os.ReadFile(h.p(path))
	if err != nil {
		return ""
	}
	return string(b)
}

func (h *Helper) exists(path string) bool {
	_, err := os.Stat(h.p(path))
	return err == nil
}

func (h *Helper) remove(path string) { _ = os.Remove(h.p(path)) }

func pendingSummary(iface string) string   { return netcfg.RunDir + "/" + iface + ".json" }
func pendingWpa(iface string) string       { return netcfg.RunDir + "/wpa-" + iface + ".conf" }
func pendingStanza(iface string) string    { return netcfg.RunDir + "/interfaces-" + iface }
func upFrom(iface string) string           { return netcfg.RunDir + "/up-" + iface }
func confirmedSummary(iface string) string { return netcfg.EtcDir + "/" + iface + ".json" }

func stanzaFile(iface string) string { return "/etc/network/interfaces.d/opensignage-" + iface }

const (
	nmSystem = "/etc/NetworkManager/system-connections"
	nmRun    = "/run/NetworkManager/system-connections"
)

func nmPendingFile(iface string) string {
	return nmRun + "/opensignage-pending-" + iface + ".nmconnection"
}

// ---------------------------------------------------------------- apply

// Apply validates the configuration and activates it until the next reboot.
func (h *Helper) Apply(ctx context.Context, c netcfg.Config) error {
	// Work on copies: validation and the stored passphrase change the configuration.
	if c.Wifi != nil {
		w := *c.Wifi
		c.Wifi = &w
	}
	if c.Static != nil {
		s := *c.Static
		s.DNS = append([]string(nil), c.Static.DNS...)
		c.Static = &s
	}
	if err := c.Validate(); err != nil {
		return err
	}
	base := "/sys/class/net/" + c.Interface
	if !h.exists(base+"/device") || h.read(base+"/type") == "" {
		return &Error{Field: "interface", Reason: "unknown"}
	}
	wireless := h.exists(base+"/wireless") || h.exists(base+"/phy80211")
	if (c.Kind == netcfg.KindWifi) != wireless {
		return &Error{Field: "kind", Reason: "invalid"}
	}
	backend := h.env().DetectBackend()
	if backend == "" {
		return &Error{Field: "backend", Reason: "unsupported"}
	}
	if c.Kind == netcfg.KindWifi && !c.Disabled && !c.Wifi.Open && c.Wifi.Passphrase == "" {
		c.Wifi.Passphrase = h.storedPassphrase(backend, c.Interface, c.Wifi.SSID)
		if c.Wifi.Passphrase == "" {
			return &Error{Field: "passphrase", Reason: "required"}
		}
	}
	if c.Kind == netcfg.KindWifi && !c.Disabled {
		h.radioOn(ctx, backend, c.Interface)
		_, _ = h.run(ctx, 10*time.Second, "iw", "reg", "set", c.Wifi.Country)
	}
	var applyErr error
	switch backend {
	case netcfg.BackendIfupdown:
		applyErr = h.applyIfupdown(ctx, c)
	case netcfg.BackendNM:
		applyErr = h.applyNM(ctx, c)
	}
	if applyErr != nil {
		h.revert(ctx, backend, c)
		return applyErr
	}
	if err := h.write(pendingSummary(c.Interface), mustJSON(c.Summary()), 0o644); err != nil {
		return err
	}
	return h.markPending(c.Interface)
}

// revert undoes a change that did not come up: the interface returns to its saved settings
// (those on disk – also when an earlier change was still waiting for its confirmation).
func (h *Helper) revert(ctx context.Context, backend string, c netcfg.Config) {
	iface := c.Interface
	switch backend {
	case netcfg.BackendIfupdown:
		_, _ = h.run(ctx, 60*time.Second, "ifdown", "-i", pendingStanza(iface), iface)
		if c.Kind == netcfg.KindWifi {
			_, _ = h.run(ctx, 10*time.Second, "wpa_cli", "-i", iface, "terminate")
		}
		_, _ = h.run(ctx, 10*time.Second, "ip", "-4", "addr", "flush", "dev", iface, "scope", "global")
		for _, f := range []string{pendingStanza(iface), pendingWpa(iface), upFrom(iface)} {
			h.remove(f)
		}
	case netcfg.BackendNM:
		h.remove(nmPendingFile(iface))
		_, _ = h.run(ctx, 30*time.Second, "nmcli", "connection", "reload")
	}
	h.remove(pendingSummary(iface))
	if p := netcfg.ReadPending(h.p(netcfg.PendingFile)); p != nil {
		var rest []string
		for _, i := range p.Interfaces {
			if i != iface {
				rest = append(rest, i)
			}
		}
		p.Interfaces = rest
		if len(rest) == 0 {
			h.remove(netcfg.PendingFile)
		} else {
			_ = h.write(netcfg.PendingFile, mustJSON(p), 0o644)
		}
	}
	// DNS: the saved files when nothing else waits, else the servers of what is left.
	if !h.exists(netcfg.PendingFile) {
		_ = h.restoreUnconfirmed()
	} else {
		_ = h.updateDNS(nil)
	}
	switch backend {
	case netcfg.BackendIfupdown:
		_, _ = h.run(ctx, 90*time.Second, "ifup", iface)
	case netcfg.BackendNM:
		_, _ = h.run(ctx, 90*time.Second, "nmcli", "--wait", "60", "device", "connect", iface)
	}
}

// storedPassphrase is the passphrase of the network as stored (pending first): an unchanged
// network keeps its password without showing it.
func (h *Helper) storedPassphrase(backend, iface, ssid string) string {
	if backend == netcfg.BackendNM {
		for _, f := range []string{nmPendingFile(iface), nmSystem + "/opensignage-" + iface + ".nmconnection"} {
			if p := netcfg.KeyfilePassphrase(h.read(f), ssid); p != "" {
				return p
			}
		}
		return ""
	}
	for _, f := range []string{pendingWpa(iface), confirmedWpa(iface)} {
		if p := netcfg.WpaPassphrase(h.read(f), ssid); p != "" {
			return p
		}
	}
	return ""
}

// radioOn switches the radio of the interface on: the rfkill soft block (Raspberry Pi OS
// blocks it until a country is set) and NetworkManager's own Wi-Fi switch (Raspberry Pi OS
// stores it switched off). Then it waits until NetworkManager can use the device – before
// that, a scan finds nothing.
func (h *Helper) radioOn(ctx context.Context, backend, iface string) {
	matches, _ := filepath.Glob(h.p("/sys/class/net/" + iface + "/phy80211/rfkill*"))
	for _, m := range matches {
		_ = os.WriteFile(filepath.Join(m, "soft"), []byte("0"), 0o644)
	}
	if backend != netcfg.BackendNM {
		return
	}
	if out, _ := h.run(ctx, 10*time.Second, "nmcli", "radio", "wifi"); strings.TrimSpace(string(out)) != "enabled" {
		_, _ = h.run(ctx, 10*time.Second, "nmcli", "radio", "wifi", "on")
	}
	for i := 0; i < 20; i++ {
		out, _ := h.run(ctx, 10*time.Second, "nmcli", "-t", "-f", "DEVICE,STATE", "device")
		state := ""
		for _, line := range strings.Split(string(out), "\n") {
			if dev, st, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && dev == iface {
				state = st
			}
		}
		if state != "" && state != "unavailable" {
			return
		}
		h.sleep(500 * time.Millisecond)
	}
}

func (h *Helper) applyIfupdown(ctx context.Context, c netcfg.Config) error {
	iface := c.Interface
	// Down with the configuration it is up with (method changes need the old one).
	if from := strings.TrimSpace(h.read(upFrom(iface))); from != "" {
		_, _ = h.run(ctx, 60*time.Second, "ifdown", "-i", from, iface)
	} else {
		_, _ = h.run(ctx, 60*time.Second, "ifdown", iface)
	}
	_, _ = h.run(ctx, 10*time.Second, "ip", "-4", "addr", "flush", "dev", iface, "scope", "global")
	if c.Kind == netcfg.KindWifi && !c.Disabled {
		if err := h.write(pendingWpa(iface), netcfg.WpaConf(*c.Wifi), 0o600); err != nil {
			return err
		}
	} else {
		h.remove(pendingWpa(iface))
	}
	inet6 := h.inet6Stanza(c)
	if inet6 == autoInet6(iface) {
		h.kernelIPv6Addresses(iface)
	}
	if err := h.write(pendingStanza(iface), netcfg.IfupdownStanza(c, pendingWpa(iface), inet6), 0o644); err != nil {
		return err
	}
	if err := h.write(upFrom(iface), pendingStanza(iface)+"\n", 0o644); err != nil {
		return err
	}
	var err error
	if c.Kind == netcfg.KindWifi && c.Disabled {
		_, _ = h.run(ctx, 10*time.Second, "ip", "link", "set", iface, "down")
	} else {
		_, err = h.run(ctx, 90*time.Second, "ifup", "-i", pendingStanza(iface), iface)
	}
	if derr := h.updateDNS(&c); derr != nil && err == nil {
		err = derr
	}
	return err
}

// staticDNS lists the DNS servers of the static interfaces: changed first (nil = none), then
// every other interface with its pending configuration (withPending) or its confirmed one.
func (h *Helper) staticDNS(changed *netcfg.Config, withPending bool) []string {
	var servers []string
	seen := map[string]bool{}
	add := func(c *netcfg.Config) {
		if c == nil || c.Method != netcfg.MethodStatic || c.Static == nil || c.Disabled {
			return
		}
		for _, s := range c.Static.DNS {
			if !seen[s] {
				seen[s] = true
				servers = append(servers, s)
			}
		}
	}
	add(changed)
	for _, it := range h.env().PhysicalInterfaces() {
		if changed != nil && it.Name == changed.Interface {
			continue
		}
		if c := netcfg.ReadSummary(h.p(pendingSummary(it.Name))); withPending && c != nil {
			add(c)
		} else {
			add(netcfg.ReadSummary(h.p(confirmedSummary(it.Name))))
		}
	}
	return servers
}

// resolvFiles hold the static DNS servers: /etc/resolv.conf.head (dhcpcd puts it on top of
// the resolv.conf it writes) and /etc/resolv.conf itself.
var resolvFiles = []string{"/etc/resolv.conf", "/etc/resolv.conf.head"}

// updateDNS writes the static DNS servers (changed: the interface just changed, nil = none)
// into the OpenSignage block of the resolv files. Every file is marked as touched first, so
// that a reboot without confirmation sets the block back to the confirmed servers.
func (h *Helper) updateDNS(changed *netcfg.Config) error {
	servers := h.staticDNS(changed, true)
	for _, f := range resolvFiles {
		if err := h.markTouched(f); err != nil {
			return err
		}
		if err := h.writeResolv(f, servers); err != nil {
			return err
		}
	}
	return nil
}

// writeResolv sets the OpenSignage block of a resolv file to servers; the rest of the file
// (what dhcpcd or the administrator wrote) stays. A file that held nothing but the block is
// removed (dhcpcd writes a new resolv.conf). A symlinked file belongs to resolvconf or
// systemd-resolved and is left alone (resolvconf reads dns-nameservers of the stanza).
func (h *Helper) writeResolv(path string, servers []string) error {
	if fi, err := os.Lstat(h.p(path)); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	cur := h.read(path)
	next := netcfg.WithResolvBlock(cur, servers)
	switch {
	case next == cur:
		return nil
	case next == "":
		h.remove(path)
		return nil
	}
	return h.write(path, next, 0o644)
}

// markTouched notes once per unconfirmed change that a system file is changed.
func (h *Helper) markTouched(path string) error {
	state := netcfg.StateDir + "/unconfirmed/" + strings.ReplaceAll(strings.TrimPrefix(path, "/"), "/", "_") + ".state"
	if h.exists(state) {
		return nil
	}
	return h.write(state, path+"\n", 0o600)
}

func (h *Helper) applyNM(ctx context.Context, c netcfg.Config) error {
	iface := c.Interface
	old := h.read(nmPendingFile(iface))
	h.remove(nmPendingFile(iface))
	if old != "" {
		_, _ = h.run(ctx, 30*time.Second, "nmcli", "connection", "reload")
	}
	if c.Kind == netcfg.KindWifi && c.Disabled {
		_, err := h.run(ctx, 30*time.Second, "nmcli", "device", "disconnect", iface)
		if err != nil && strings.Contains(err.Error(), "not active") {
			err = nil
		}
		return err
	}
	uuid := ids.New()
	if err := h.write(nmPendingFile(iface), netcfg.NMKeyfile(c, "opensignage-pending-"+iface, uuid), 0o600); err != nil {
		return err
	}
	if _, err := h.run(ctx, 30*time.Second, "nmcli", "connection", "reload"); err != nil {
		return err
	}
	_, err := h.run(ctx, 90*time.Second, "nmcli", "--wait", "60", "connection", "up", "uuid", uuid)
	return err
}

func (h *Helper) markPending(iface string) error {
	p := netcfg.ReadPending(h.p(netcfg.PendingFile))
	if p == nil {
		p = &netcfg.Pending{Since: h.now().UTC()}
	}
	if !contains(p.Interfaces, iface) {
		p.Interfaces = append(p.Interfaces, iface)
		sort.Strings(p.Interfaces)
	}
	return h.write(netcfg.PendingFile, mustJSON(p), 0o644)
}

// ---------------------------------------------------------------- confirm

// Confirm makes the pending change permanent.
func (h *Helper) Confirm(ctx context.Context) error {
	p := netcfg.ReadPending(h.p(netcfg.PendingFile))
	if p == nil {
		return nil
	}
	backend := h.env().DetectBackend()
	var errs []error
	for _, iface := range p.Interfaces {
		c := netcfg.ReadSummary(h.p(pendingSummary(iface)))
		if c == nil {
			continue
		}
		var err error
		switch backend {
		case netcfg.BackendIfupdown:
			err = h.confirmIfupdown(*c)
		case netcfg.BackendNM:
			err = h.confirmNM(ctx, *c)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", iface, err))
			continue
		}
		if c.Kind == netcfg.KindWifi && c.Disabled {
			h.remove(confirmedSummary(iface))
		} else if err := h.write(confirmedSummary(iface), mustJSON(*c), 0o644); err != nil {
			errs = append(errs, err)
		}
		h.remove(pendingSummary(iface))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	_ = os.RemoveAll(h.p(netcfg.StateDir + "/unconfirmed"))
	h.remove(netcfg.PendingFile)
	return nil
}

func (h *Helper) confirmIfupdown(c netcfg.Config) error {
	iface := c.Interface
	if err := h.disableSystemStanzas(iface); err != nil {
		return err
	}
	if c.Kind == netcfg.KindWifi && c.Disabled {
		h.remove(stanzaFile(iface))
		h.remove(confirmedWpa(iface))
	} else {
		if c.Kind == netcfg.KindWifi {
			if err := h.write(confirmedWpa(iface), h.read(pendingWpa(iface)), 0o600); err != nil {
				return err
			}
		}
		if err := h.write(stanzaFile(iface), netcfg.IfupdownStanza(c, confirmedWpa(iface), h.inet6Stanza(c)), 0o644); err != nil {
			return err
		}
	}
	// The interface is up with the same settings the system files now hold: later ifdown
	// and ifup use those.
	h.remove(upFrom(iface))
	h.remove(pendingStanza(iface))
	h.remove(pendingWpa(iface))
	return nil
}

// systemInterfaceFiles are the ifupdown files of the system (not those of OpenSignage).
func (h *Helper) systemInterfaceFiles() []string {
	files := []string{"/etc/network/interfaces"}
	if entries, err := os.ReadDir(h.p("/etc/network/interfaces.d")); err == nil {
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), "opensignage-") && !e.IsDir() {
				files = append(files, "/etc/network/interfaces.d/"+e.Name())
			}
		}
	}
	return files
}

// inet6Stanza is the IPv6 part of the interface's stanza: the system's own, carried over;
// else automatic IPv6 for a fixed IPv4 address (with DHCP, dhcpcd configures IPv6 itself –
// with a fixed address nothing would, and the addresses would be gone), unless IPv6 is off.
func (h *Helper) inet6Stanza(c netcfg.Config) string {
	var b strings.Builder
	for _, f := range h.systemInterfaceFiles() {
		b.WriteString(netcfg.Inet6Stanza(h.read(f), c.Interface))
	}
	if b.Len() > 0 {
		return b.String()
	}
	if c.Method == netcfg.MethodStatic && strings.TrimSpace(h.read("/proc/sys/net/ipv6/conf/"+c.Interface+"/disable_ipv6")) == "0" {
		return autoInet6(c.Interface)
	}
	return ""
}

func autoInet6(iface string) string { return "iface " + iface + " inet6 auto\n" }

// kernelIPv6Addresses lets the kernel make the IPv6 addresses of the interface again: dhcpcd
// switches that off on the interfaces it runs on (addr_gen_mode 1, it makes its own), so
// after it stopped the interface would keep no IPv6 address, not even a link-local one. At
// the next boot dhcpcd does not run for a fixed address and the default applies anyway.
func (h *Helper) kernelIPv6Addresses(iface string) {
	def := strings.TrimSpace(h.read("/proc/sys/net/ipv6/conf/default/addr_gen_mode"))
	cur := strings.TrimSpace(h.read("/proc/sys/net/ipv6/conf/" + iface + "/addr_gen_mode"))
	if def != "" && cur != "" && cur != def {
		_ = os.WriteFile(h.p("/proc/sys/net/ipv6/conf/"+iface+"/addr_gen_mode"), []byte(def), 0o644)
	}
}

// disableSystemStanzas comments out other configurations of the interface (the one the
// Debian installer wrote) after saving the original files once.
func (h *Helper) disableSystemStanzas(iface string) error {
	for _, f := range h.systemInterfaceFiles() {
		content := h.read(f)
		next, changed := netcfg.DisableStanzas(content, iface)
		if !changed {
			continue
		}
		orig := netcfg.StateDir + "/original/" + strings.ReplaceAll(strings.TrimPrefix(f, "/"), "/", "_")
		if !h.exists(orig) {
			if err := h.write(orig, content, 0o600); err != nil {
				return err
			}
		}
		mode := os.FileMode(0o644)
		if st, err := os.Stat(h.p(f)); err == nil {
			mode = st.Mode().Perm()
		}
		if err := h.write(f, next, mode); err != nil {
			return err
		}
	}
	return nil
}

func (h *Helper) confirmNM(ctx context.Context, c netcfg.Config) error {
	iface := c.Interface
	var keep string
	if !(c.Kind == netcfg.KindWifi && c.Disabled) {
		keep = nmUUID(h.read(nmPendingFile(iface)))
		if keep == "" {
			return errors.New("pending profile missing")
		}
		// Saved to disk by NetworkManager itself (keyfile or netplan, as the system uses).
		if _, err := h.run(ctx, 30*time.Second, "nmcli", "connection", "modify", "uuid", keep, "connection.id", "opensignage-"+iface); err != nil {
			return err
		}
	}
	// Other profiles of the interface would compete at the next start.
	out, err := h.run(ctx, 30*time.Second, "nmcli", "-t", "-f", "UUID,TYPE", "connection", "show")
	if err != nil {
		return err
	}
	wantType := "802-3-ethernet"
	if c.Kind == netcfg.KindWifi {
		wantType = "802-11-wireless"
	}
	for _, line := range strings.Split(string(out), "\n") {
		uuid, typ, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || uuid == keep || typ != wantType {
			continue
		}
		name, _ := h.run(ctx, 10*time.Second, "nmcli", "-g", "connection.interface-name", "connection", "show", "uuid", uuid)
		if n := strings.TrimSpace(string(name)); n != "" && n != iface {
			continue
		}
		_, _ = h.run(ctx, 30*time.Second, "nmcli", "connection", "delete", "uuid", uuid)
	}
	if c.Kind == netcfg.KindWifi && !c.Disabled && c.Wifi != nil {
		h.persistCountry(ctx, c.Wifi.Country)
	}
	return nil
}

// persistCountry keeps the radio country after reboots.
func (h *Helper) persistCountry(ctx context.Context, country string) {
	if h.exists("/usr/bin/raspi-config") {
		if _, err := h.run(ctx, 60*time.Second, "raspi-config", "nonint", "do_wifi_country", country); err == nil {
			return
		}
	}
	_ = h.write("/etc/modprobe.d/opensignage-wifi.conf", netcfg.Header+"\noptions cfg80211 ieee80211_regdom="+country+"\n", 0o644)
}

func nmUUID(keyfile string) string {
	for _, line := range strings.Split(keyfile, "\n") {
		if v, ok := strings.CutPrefix(line, "uuid="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ---------------------------------------------------------------- cleanup

// Cleanup restores system files after a reboot without confirmation (the pending change
// under /run is gone then).
func (h *Helper) Cleanup() error {
	if h.exists(netcfg.PendingFile) {
		return nil
	}
	return h.restoreUnconfirmed()
}

// Uninstall runs when OpenSignage is removed. Confirmed settings stay (the device keeps its
// connection). An unconfirmed change still ends with the next restart, but no agent is left
// to put back the system files it changed, so they are put back now. It reports whether a
// change was pending.
func (h *Helper) Uninstall() (pending bool, err error) {
	return h.exists(netcfg.PendingFile), h.restoreUnconfirmed()
}

// restoreUnconfirmed sets the files an unconfirmed change touched back to the confirmed
// settings: the OpenSignage block gets the DNS servers of the confirmed static interfaces, or
// goes when there are none. What dhcpcd wrote meanwhile stays.
func (h *Helper) restoreUnconfirmed() error {
	dir := netcfg.StateDir + "/unconfirmed"
	if !h.exists(dir) {
		return nil
	}
	entries, err := os.ReadDir(h.p(dir))
	if err != nil {
		return err
	}
	servers := h.staticDNS(nil, false)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".state") {
			continue
		}
		// One line: the path (older helpers wrote "present|absent" before it).
		lines := strings.Split(strings.TrimSpace(h.read(dir+"/"+e.Name())), "\n")
		target := lines[len(lines)-1]
		if !contains(resolvFiles, target) {
			continue // only files this helper changes
		}
		if err := h.writeResolv(target, servers); err != nil {
			return err
		}
	}
	return os.RemoveAll(h.p(dir))
}

// ---------------------------------------------------------------- scan

// Scan lists the wireless networks around the interface.
func (h *Helper) Scan(ctx context.Context, iface string) ([]netcfg.AP, error) {
	if !netcfg.ValidInterface(iface) || !(h.exists("/sys/class/net/"+iface+"/wireless") || h.exists("/sys/class/net/"+iface+"/phy80211")) {
		return nil, &Error{Field: "interface", Reason: "unknown"}
	}
	backend := h.env().DetectBackend()
	h.radioOn(ctx, backend, iface)
	switch backend {
	case netcfg.BackendNM:
		out, err := h.run(ctx, 40*time.Second, "nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY,FREQ", "device", "wifi", "list", "ifname", iface, "--rescan", "yes")
		if err != nil {
			return nil, err
		}
		return netcfg.ParseNmcliWifi(string(out)), nil
	default:
		if h.exists("/run/wpa_supplicant/" + iface) {
			if _, err := h.run(ctx, 10*time.Second, "wpa_cli", "-i", iface, "scan"); err == nil {
				h.sleep(5 * time.Second)
				out, err := h.run(ctx, 10*time.Second, "wpa_cli", "-i", iface, "scan_results")
				if err != nil {
					return nil, err
				}
				return netcfg.ParseWpaScanResults(string(out)), nil
			}
		}
		_, _ = h.run(ctx, 10*time.Second, "ip", "link", "set", iface, "up")
		var out []byte
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			out, err = h.run(ctx, 40*time.Second, "iw", "dev", iface, "scan")
			if err == nil || !strings.Contains(err.Error(), "busy") {
				break
			}
			h.sleep(2 * time.Second)
		}
		if err != nil {
			return nil, err
		}
		return netcfg.ParseIwScan(string(out)), nil
	}
}

// ---------------------------------------------------------------- firmware

// FirmwareHint is network firmware the kernel misses and the package that has it.
type FirmwareHint struct {
	File    string `json:"file"`
	Package string `json:"package"`
}

// Firmware lists missing network firmware (from the kernel log).
func (h *Helper) Firmware(ctx context.Context) ([]FirmwareHint, error) {
	out, err := h.run(ctx, 10*time.Second, "dmesg")
	if err != nil {
		return nil, err
	}
	var hints []FirmwareHint
	for _, f := range netcfg.MissingFirmware(string(out)) {
		if p := netcfg.FirmwarePackage(f); p != "" && !h.exists("/lib/firmware/"+f) && !h.exists("/lib/firmware/"+f+".xz") && !h.exists("/lib/firmware/"+f+".zst") {
			hints = append(hints, FirmwareHint{File: f, Package: p})
		}
	}
	return hints, nil
}

// InstallFirmware installs one of the known firmware packages (Debian non-free-firmware).
func (h *Helper) InstallFirmware(ctx context.Context, pkg string) error {
	if !netcfg.AllowedFirmwarePackage(pkg) {
		return &Error{Field: "package", Reason: "invalid"}
	}
	_, _ = h.run(ctx, 5*time.Minute, "apt-get", "update", "-q")
	_, err := h.run(ctx, 15*time.Minute, "apt-get", "install", "-y", "-q", "--no-install-recommends", pkg)
	return err
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func mustJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// confirmedWpa is outside the OpenSignage directories: a wireless network set up in the
// WebGUI keeps working after OpenSignage is uninstalled.
func confirmedWpa(iface string) string { return "/etc/wpa_supplicant/opensignage-" + iface + ".conf" }
