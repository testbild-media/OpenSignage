package netcfg

import (
	"bufio"
	"context"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Backends (how the system configures its network).
const (
	BackendIfupdown = "ifupdown"       // Debian: /etc/network/interfaces
	BackendNM       = "networkmanager" // Raspberry Pi OS
)

// Interface is the state of a physical network interface.
type Interface struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"` // ethernet | wifi
	MAC       string   `json:"mac,omitempty"`
	Driver    string   `json:"driver,omitempty"`
	Up        bool     `json:"up"`
	Carrier   bool     `json:"carrier"` // cable plugged in / associated
	SpeedMbps int      `json:"speedMbps,omitempty"`
	IPv4      []string `json:"ipv4,omitempty"` // with prefix length
	IPv6      []string `json:"ipv6,omitempty"` // global addresses
	Gateway   string   `json:"gateway,omitempty"`
	// Method is how the system configures the interface: dhcp | static | "" (not configured).
	Method string `json:"method,omitempty"`
	// Static is the configured static address (when Method is static and readable).
	Static *IPv4 `json:"static,omitempty"`
	// Managed: the configuration was made in the client WebGUI.
	Managed bool `json:"managed"`
	// Wireless only.
	Blocked        bool    `json:"blocked,omitempty"` // radio switched off (rfkill)
	SSID           string  `json:"ssid,omitempty"`    // connected network
	SignalDBM      int     `json:"signalDbm,omitempty"`
	FreqMHz        int     `json:"freqMHz,omitempty"`
	BitrateMbps    float64 `json:"bitrateMbps,omitempty"`
	ConfiguredSSID string  `json:"configuredSsid,omitempty"`
}

// Status is the network state of the device.
type Status struct {
	Backend    string      `json:"backend"`
	Interfaces []Interface `json:"interfaces"`
	DNS        []string    `json:"dns,omitempty"`
	Country    string      `json:"country,omitempty"` // radio rules in effect ("" = world)
}

// Env is the system the status is read from (tests use a directory tree and fake commands).
type Env struct {
	Root  string                                                                 // "" = /
	Run   func(ctx context.Context, name string, args ...string) ([]byte, error) // nil = exec
	Addrs func(name string) ([]net.Addr, error)                                  // nil = net package
}

func (e Env) path(p string) string {
	if e.Root == "" {
		return p
	}
	return filepath.Join(e.Root, filepath.FromSlash(p))
}

func (e Env) read(p string) string {
	b, err := os.ReadFile(e.path(p))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (e Env) exists(p string) bool {
	_, err := os.Stat(e.path(p))
	return err == nil
}

func (e Env) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if e.Run != nil {
		return e.Run(ctx, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, Tool(name), args...)
	cmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	return cmd.Output()
}

// Tool finds a system program also outside the PATH of a service user (/usr/sbin).
func Tool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range []string{"/usr/sbin", "/sbin", "/usr/bin", "/bin"} {
		p := dir + "/" + name
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return name
}

// DetectBackend: NetworkManager when it runs, else ifupdown when configured, else "".
func (e Env) DetectBackend() string {
	if e.exists("/run/NetworkManager/NetworkManager.pid") || e.exists("/run/NetworkManager/devices") {
		return BackendNM
	}
	if e.exists("/etc/network/interfaces") {
		return BackendIfupdown
	}
	return ""
}

// PhysicalInterfaces lists the wired and wireless interfaces of the hardware (no loopback,
// VPN or other virtual interfaces), sorted: wired first.
func (e Env) PhysicalInterfaces() []Interface {
	entries, err := os.ReadDir(e.path("/sys/class/net"))
	if err != nil {
		return nil
	}
	var out []Interface
	for _, en := range entries {
		n := en.Name()
		base := "/sys/class/net/" + n
		if !e.exists(base+"/device") || e.read(base+"/type") != "1" {
			continue
		}
		it := Interface{Name: n, Kind: KindEthernet, MAC: e.read(base + "/address")}
		if e.exists(base+"/wireless") || e.exists(base+"/phy80211") {
			it.Kind = KindWifi
		}
		if l, err := os.Readlink(e.path(base + "/device/driver")); err == nil {
			it.Driver = filepath.Base(l)
		}
		it.Up = e.read(base+"/operstate") == "up"
		it.Carrier = e.read(base+"/carrier") == "1"
		if it.Kind == KindEthernet && it.Carrier {
			if s, err := strconv.Atoi(e.read(base + "/speed")); err == nil && s > 0 {
				it.SpeedMbps = s
			}
		}
		if it.Kind == KindWifi {
			it.Blocked = e.rfkillBlocked(base)
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == KindEthernet
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (e Env) rfkillBlocked(base string) bool {
	matches, _ := filepath.Glob(e.path(base + "/phy80211/rfkill*"))
	for _, m := range matches {
		soft, _ := os.ReadFile(filepath.Join(m, "soft"))
		hard, _ := os.ReadFile(filepath.Join(m, "hard"))
		if strings.TrimSpace(string(soft)) == "1" || strings.TrimSpace(string(hard)) == "1" {
			return true
		}
	}
	return false
}

// Inspect reads the full state: interfaces with addresses, gateway and configuration, the
// connected wireless network with its signal, DNS servers and the radio country.
func (e Env) Inspect(ctx context.Context) Status {
	st := Status{Backend: e.DetectBackend(), Interfaces: e.PhysicalInterfaces()}
	gws := e.gateways()
	addrs := e.Addrs
	if addrs == nil {
		addrs = func(name string) ([]net.Addr, error) {
			ifc, err := net.InterfaceByName(name)
			if err != nil {
				return nil, err
			}
			return ifc.Addrs()
		}
	}
	for i := range st.Interfaces {
		it := &st.Interfaces[i]
		if as, err := addrs(it.Name); err == nil {
			for _, a := range as {
				ipn, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				if ipn.IP.To4() != nil {
					it.IPv4 = append(it.IPv4, ipn.String())
				} else if ipn.IP.IsGlobalUnicast() {
					it.IPv6 = append(it.IPv6, ipn.IP.String())
				}
			}
		}
		it.Gateway = gws[it.Name]
		if it.Kind == KindWifi && it.Up {
			if out, err := e.run(ctx, "iw", "dev", it.Name, "link"); err == nil {
				ParseIwLink(string(out), it)
			}
		}
	}
	switch st.Backend {
	case BackendIfupdown:
		e.ifupdownMethods(st.Interfaces)
	case BackendNM:
		e.nmMethods(ctx, st.Interfaces)
	}
	st.DNS = ParseResolv(e.read("/etc/resolv.conf"))
	if out, err := e.run(ctx, "iw", "reg", "get"); err == nil {
		st.Country = ParseRegCountry(string(out))
	}
	return st
}

// gateways reads the IPv4 default routes per interface from /proc/net/route.
func (e Env) gateways() map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(e.read("/proc/net/route")))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || f[1] != "00000000" || f[2] == "00000000" {
			continue
		}
		b, err := hex.DecodeString(f[2])
		if err != nil || len(b) != 4 {
			continue
		}
		if _, ok := out[f[0]]; !ok {
			out[f[0]] = net.IPv4(b[3], b[2], b[1], b[0]).String()
		}
	}
	return out
}

// ifupdownMethods reads the configured methods from /etc/network/interfaces(.d).
func (e Env) ifupdownMethods(ifs []Interface) {
	files := []string{"/etc/network/interfaces"}
	if entries, err := os.ReadDir(e.path("/etc/network/interfaces.d")); err == nil {
		for _, en := range entries {
			files = append(files, "/etc/network/interfaces.d/"+en.Name())
		}
	}
	for _, f := range files {
		content := e.read(f)
		managed := strings.HasPrefix(filepath.Base(f), "opensignage-")
		cur := ""
		for _, line := range strings.Split(content, "\n") {
			fl := strings.Fields(line)
			if len(fl) == 0 || strings.HasPrefix(fl[0], "#") {
				continue
			}
			if fl[0] == "iface" && len(fl) >= 4 && fl[2] == "inet" {
				cur = fl[1]
				for i := range ifs {
					if ifs[i].Name == cur {
						switch fl[3] {
						case "dhcp":
							ifs[i].Method = MethodDHCP
						case "static":
							ifs[i].Method, ifs[i].Static = MethodStatic, &IPv4{}
						}
						ifs[i].Managed = managed
					}
				}
				continue
			}
			if isStanzaKeyword(fl[0]) {
				cur = ""
				continue
			}
			for i := range ifs {
				if ifs[i].Name != cur || len(fl) < 2 {
					continue
				}
				switch fl[0] {
				case "address":
					if ifs[i].Static != nil {
						ifs[i].Static.Address = fl[1]
					}
				case "gateway":
					if ifs[i].Static != nil {
						ifs[i].Static.Gateway = fl[1]
					}
				case "wpa-ssid":
					ifs[i].ConfiguredSSID = strings.Trim(strings.Join(fl[1:], " "), `"`)
				case "wpa-conf":
					// Readable only when it is not ours (ours are root-only: the agent reads the
					// summary OpenSignage keeps next to them).
					if ssid := configuredSSID(e.read(fl[1])); ssid != "" {
						ifs[i].ConfiguredSSID = ssid
					}
				}
			}
		}
	}
}

// configuredSSID reads the network name of a wpa_supplicant file (hex or quoted).
func configuredSSID(conf string) string {
	for _, line := range strings.Split(conf, "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "ssid=")
		if !ok {
			continue
		}
		if strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) && len(v) >= 2 {
			return v[1 : len(v)-1]
		}
		if b, err := hex.DecodeString(v); err == nil {
			return string(b)
		}
	}
	return ""
}

// nmMethods asks NetworkManager for the active profile of every interface.
func (e Env) nmMethods(ctx context.Context, ifs []Interface) {
	for i := range ifs {
		out, err := e.run(ctx, "nmcli", "-t", "-f", "GENERAL.CONNECTION", "device", "show", ifs[i].Name)
		if err != nil {
			continue
		}
		con := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "GENERAL.CONNECTION:"))
		if con == "" || con == "--" {
			continue
		}
		ifs[i].Managed = strings.HasPrefix(con, "opensignage-")
		out, err = e.run(ctx, "nmcli", "-t", "-f", "ipv4.method,ipv4.addresses,ipv4.gateway,802-11-wireless.ssid", "connection", "show", con)
		if err != nil {
			continue
		}
		vals := ParseNMFields(string(out))
		switch vals["ipv4.method"] {
		case "auto":
			ifs[i].Method = MethodDHCP
		case "manual":
			ifs[i].Method = MethodStatic
			ifs[i].Static = &IPv4{Address: strings.Split(vals["ipv4.addresses"], ",")[0], Gateway: vals["ipv4.gateway"]}
			if ifs[i].Static.Gateway == "--" {
				ifs[i].Static.Gateway = ""
			}
		}
		if s := vals["802-11-wireless.ssid"]; s != "" && s != "--" {
			ifs[i].ConfiguredSSID = s
		}
	}
}

// ParseNMFields reads `nmcli -t -f a,b … show` output (key:value per line).
func ParseNMFields(out string) map[string]string {
	vals := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok {
			vals[k] = strings.ReplaceAll(v, `\:`, ":")
		}
	}
	return vals
}

// ParseIwLink fills the connected network from `iw dev X link`.
func ParseIwLink(out string, it *Interface) {
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "SSID: "):
			it.SSID = unescapeIw(strings.TrimPrefix(t, "SSID: "))
		case strings.HasPrefix(t, "freq: "):
			if f, err := strconv.ParseFloat(strings.TrimPrefix(t, "freq: "), 64); err == nil {
				it.FreqMHz = int(f)
			}
		case strings.HasPrefix(t, "signal: "):
			if f := strings.Fields(strings.TrimPrefix(t, "signal: ")); len(f) > 0 {
				if v, err := strconv.ParseFloat(f[0], 64); err == nil {
					it.SignalDBM = int(v)
				}
			}
		case strings.HasPrefix(t, "tx bitrate: "):
			if f := strings.Fields(strings.TrimPrefix(t, "tx bitrate: ")); len(f) > 0 {
				it.BitrateMbps, _ = strconv.ParseFloat(f[0], 64)
			}
		}
	}
}

// unescapeIw decodes the \xNN escapes iw uses for non-printable SSID bytes.
func unescapeIw(s string) string {
	if !strings.Contains(s, `\x`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if v, err := hex.DecodeString(s[i+2 : i+4]); err == nil {
				b.Write(v)
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ParseResolv lists the name servers of a resolv.conf.
func ParseResolv(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" {
			out = append(out, f[1])
		}
	}
	return out
}

// ParseRegCountry reads the global country of `iw reg get` ("" for the world domain 00).
func ParseRegCountry(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "country "); ok {
			c := strings.TrimSuffix(strings.Fields(v)[0], ":")
			if ValidCountry(c) {
				return c
			}
			return ""
		}
	}
	return ""
}

// SignalPercent maps dBm to 0–100 % (-100 dBm = 0 %, -50 dBm and better = 100 %).
func SignalPercent(dbm int) int {
	switch {
	case dbm == 0:
		return 0
	case dbm >= -50:
		return 100
	case dbm <= -100:
		return 0
	}
	return 2 * (dbm + 100)
}

// SignalQuality rates a signal: excellent | good | fair | weak (for the WebGUI and alerts).
func SignalQuality(dbm int) string {
	switch {
	case dbm >= -55:
		return "excellent"
	case dbm >= -67:
		return "good"
	case dbm >= -75:
		return "fair"
	}
	return "weak"
}

// Band is "2.4", "5" or "6" (GHz) for a frequency in MHz.
func Band(freq int) string {
	switch {
	case freq >= 2400 && freq < 2500:
		return "2.4"
	case freq >= 4900 && freq < 5925:
		return "5"
	case freq >= 5925 && freq < 7200:
		return "6"
	}
	return ""
}

// Channel is the channel number of a frequency in MHz (0 = unknown).
func Channel(freq int) int {
	switch {
	case freq == 2484:
		return 14
	case freq >= 2412 && freq <= 2472:
		return (freq - 2407) / 5
	case freq >= 5160 && freq <= 5885:
		return (freq - 5000) / 5
	case freq >= 5955 && freq <= 7115:
		return (freq - 5950) / 5
	}
	return 0
}
