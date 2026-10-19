package netcfg

import (
	"fmt"
	"strings"
)

// Header marks the files OpenSignage writes (the WebGUI replaces them on every change).
const Header = "# Written by OpenSignage (network settings in the client WebGUI). Changes here are replaced."

// IfupdownStanza is the ifupdown configuration of the interface (Debian). wpaConf is the
// wpa_supplicant file of a wireless interface. DNS servers are not part of it: Debian has no
// resolvconf, so they go to /etc/resolv.conf(.head) (see ResolvHead).
func IfupdownStanza(c Config, wpaConf, inet6 string) string {
	var b strings.Builder
	b.WriteString(Header + "\n")
	if c.Kind == KindWifi && c.Disabled {
		// No stanza: the interface stays down.
		b.WriteString("# " + c.Interface + ": wireless network switched off\n")
		return b.String()
	}
	fmt.Fprintf(&b, "allow-hotplug %s\n", c.Interface)
	if c.Method == MethodStatic && c.Static != nil {
		fmt.Fprintf(&b, "iface %s inet static\n", c.Interface)
		fmt.Fprintf(&b, "\taddress %s\n", c.Static.Address)
		if c.Static.Gateway != "" {
			fmt.Fprintf(&b, "\tgateway %s\n", c.Static.Gateway)
		}
		if len(c.Static.DNS) > 0 {
			// Read by resolvconf when it is installed (else the helper writes resolv.conf).
			fmt.Fprintf(&b, "\tdns-nameservers %s\n", strings.Join(c.Static.DNS, " "))
		}
	} else {
		fmt.Fprintf(&b, "iface %s inet dhcp\n", c.Interface)
	}
	if c.Kind == KindWifi {
		fmt.Fprintf(&b, "\twpa-conf %s\n", wpaConf)
		// A player runs on mains power: power saving only costs latency, and on USB sticks
		// (rtw88) it is a known cause of dropouts. Not every driver allows switching it off.
		fmt.Fprintf(&b, "\tpost-up iw dev %s set power_save off || true\n", c.Interface)
	}
	b.WriteString(inet6)
	return b.String()
}

// Inet6Stanza is the IPv6 stanza of the interface in an interfaces file, with its option
// lines – also one OpenSignage commented out ("" = none). The OpenSignage stanza replaces
// every stanza of the interface, so it carries this one over.
func Inet6Stanza(content, iface string) string {
	var out []string
	in := false
	for _, line := range strings.Split(content, "\n") {
		trim := strings.TrimSpace(strings.TrimPrefix(line, DisabledPrefix))
		fields := strings.Fields(trim)
		if len(fields) > 0 && !strings.HasPrefix(trim, "#") && isStanzaKeyword(fields[0]) {
			in = fields[0] == "iface" && len(fields) >= 4 && fields[1] == iface && fields[2] == "inet6"
			if in {
				out = append(out, strings.Join(fields, " "))
			}
			continue
		}
		if in && trim != "" && !strings.HasPrefix(trim, "#") {
			out = append(out, "\t"+trim)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

// WpaConf is the wpa_supplicant configuration of a wireless network. The SSID is written in
// hex and a passphrase in quotes (wpa_supplicant reads up to the last quote of the line; the
// passphrase is printable ASCII without line breaks), so nothing needs escaping.
func WpaConf(w Wifi) string {
	var b strings.Builder
	b.WriteString(Header + "\n")
	b.WriteString("ctrl_interface=DIR=/run/wpa_supplicant GROUP=netdev\n")
	b.WriteString("update_config=0\n")
	fmt.Fprintf(&b, "country=%s\n", w.Country)
	b.WriteString("network={\n")
	fmt.Fprintf(&b, "\tssid=%s\n", SSIDHex(w.SSID))
	if w.Hidden {
		b.WriteString("\tscan_ssid=1\n")
	}
	switch {
	case w.Open:
		b.WriteString("\tkey_mgmt=NONE\n")
	case IsHexKey(w.Passphrase):
		b.WriteString("\tkey_mgmt=WPA-PSK\n")
		fmt.Fprintf(&b, "\tpsk=%s\n", strings.ToLower(w.Passphrase))
	case w.SAE:
		b.WriteString("\tkey_mgmt=SAE\n\tieee80211w=2\n")
		fmt.Fprintf(&b, "\tsae_password=\"%s\"\n", w.Passphrase)
	default:
		// WPA2 and WPA3 (transition mode): the passphrase serves SAE as well.
		b.WriteString("\tkey_mgmt=WPA-PSK WPA-PSK-SHA256 SAE\n\tieee80211w=1\n")
		fmt.Fprintf(&b, "\tpsk=\"%s\"\n", w.Passphrase)
	}
	b.WriteString("}\n")
	return b.String()
}

// WpaPassphrase reads the passphrase (or key) of the network back from a WpaConf file ("" when
// the file is for another network or has none): the WebGUI does not show passwords, so an
// unchanged network keeps its stored one.
func WpaPassphrase(conf, ssid string) string {
	if !strings.Contains(conf, "\tssid="+SSIDHex(ssid)+"\n") {
		return ""
	}
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		for _, key := range []string{"psk=", "sae_password="} {
			if v, ok := strings.CutPrefix(line, key); ok {
				if strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) && len(v) >= 2 {
					return v[1 : len(v)-1]
				}
				return v
			}
		}
	}
	return ""
}

// NMKeyfile is the NetworkManager profile of the interface (Raspberry Pi OS).
func NMKeyfile(c Config, id, uuid string) string {
	var b strings.Builder
	b.WriteString(Header + "\n")
	b.WriteString("[connection]\n")
	fmt.Fprintf(&b, "id=%s\nuuid=%s\n", id, uuid)
	if c.Kind == KindWifi {
		b.WriteString("type=wifi\n")
	} else {
		b.WriteString("type=ethernet\n")
	}
	fmt.Fprintf(&b, "interface-name=%s\n", c.Interface)
	b.WriteString("autoconnect=true\nautoconnect-priority=100\n\n")
	if c.Kind == KindWifi {
		w := c.Wifi
		b.WriteString("[wifi]\nmode=infrastructure\npowersave=2\n") // off: see IfupdownStanza
		fmt.Fprintf(&b, "ssid=%s\n", keyfileSSID(w.SSID))
		if w.Hidden {
			b.WriteString("hidden=true\n")
		}
		b.WriteString("\n")
		if !w.Open {
			b.WriteString("[wifi-security]\n")
			if w.SAE {
				b.WriteString("key-mgmt=sae\n")
			} else {
				b.WriteString("key-mgmt=wpa-psk\n")
			}
			fmt.Fprintf(&b, "psk=%s\n\n", keyfileEscape(w.Passphrase))
		}
	} else {
		b.WriteString("[ethernet]\n\n")
	}
	b.WriteString("[ipv4]\n")
	if c.Method == MethodStatic && c.Static != nil {
		b.WriteString("method=manual\n")
		if c.Static.Gateway != "" {
			fmt.Fprintf(&b, "address1=%s,%s\n", c.Static.Address, c.Static.Gateway)
		} else {
			fmt.Fprintf(&b, "address1=%s\n", c.Static.Address)
		}
		if len(c.Static.DNS) > 0 {
			fmt.Fprintf(&b, "dns=%s;\n", strings.Join(c.Static.DNS, ";"))
		}
	} else {
		b.WriteString("method=auto\n")
	}
	b.WriteString("\n[ipv6]\nmethod=auto\n")
	return b.String()
}

// keyfileSSID writes plain SSIDs as text and everything else as the list of its bytes
// (both are keyfile formats of NetworkManager), so no SSID needs escaping.
func keyfileSSID(ssid string) string {
	plain := ssid != "" && ssid[0] != ' ' && ssid[len(ssid)-1] != ' '
	for _, r := range ssid {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == ' ' || r == '-' || r == '_' || r == '.') {
			plain = false
		}
	}
	if plain {
		return ssid
	}
	parts := make([]string, 0, len(ssid))
	for _, c := range []byte(ssid) {
		parts = append(parts, fmt.Sprint(c))
	}
	return strings.Join(parts, ";") + ";"
}

// keyfileEscape escapes a string value of a keyfile (GKeyFile rules): backslashes, and
// spaces at the start and end.
func keyfileEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	if strings.HasPrefix(s, " ") {
		s = `\s` + s[1:]
	}
	if strings.HasSuffix(s, " ") && len(s) > 0 {
		s = s[:len(s)-1] + `\s`
	}
	return s
}

// KeyfilePassphrase reads the passphrase of the network back from an NMKeyfile.
func KeyfilePassphrase(keyfile, ssid string) string {
	if !strings.Contains(keyfile, "\nssid="+keyfileSSID(ssid)+"\n") {
		return ""
	}
	for _, line := range strings.Split(keyfile, "\n") {
		if v, ok := strings.CutPrefix(line, "psk="); ok {
			return keyfileUnescape(v)
		}
	}
	return ""
}

func keyfileUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 's':
			b.WriteByte(' ')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// DisableStanzas removes the interface from an ifupdown file: its iface blocks (with their
// option lines) are commented out and its name is taken out of auto/allow-* lines, so a
// configuration of OpenSignage can take its place. It reports whether anything changed.
// DisabledPrefix marks the lines of the system's stanzas OpenSignage commented out.
const DisabledPrefix = "#opensignage# "

func DisableStanzas(content, iface string) (string, bool) {
	lines := strings.Split(content, "\n")
	changed := false
	inBlock := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		fields := strings.Fields(trim)
		startsStanza := len(fields) > 0 && !strings.HasPrefix(trim, "#") && isStanzaKeyword(fields[0])
		if startsStanza {
			inBlock = false
		}
		switch {
		case startsStanza && (fields[0] == "iface" || fields[0] == "mapping") && len(fields) > 1 && fields[1] == iface:
			inBlock = true
			lines[i] = DisabledPrefix + line
			changed = true
		case startsStanza && (fields[0] == "auto" || strings.HasPrefix(fields[0], "allow-")):
			kept := []string{fields[0]}
			for _, f := range fields[1:] {
				if f != iface {
					kept = append(kept, f)
				}
			}
			if len(kept) != len(fields) {
				changed = true
				if len(kept) == 1 {
					lines[i] = DisabledPrefix + line
				} else {
					lines[i] = DisabledPrefix + line + "\n" + strings.Join(kept, " ")
				}
			}
		case inBlock && trim != "" && !strings.HasPrefix(trim, "#"):
			lines[i] = DisabledPrefix + line
			changed = true
		}
	}
	return strings.Join(lines, "\n"), changed
}

func isStanzaKeyword(f string) bool {
	return f == "iface" || f == "mapping" || f == "auto" || f == "source" || f == "source-directory" || strings.HasPrefix(f, "allow-")
}

// ConfiguredIn reports whether an ifupdown file configures the interface (an iface block).
func ConfiguredIn(content, iface string) bool {
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[0] == "iface" && f[1] == iface {
			return true
		}
	}
	return false
}

// ResolvHead is the part of /etc/resolv.conf.head with the static DNS servers (dhcpcd puts
// the file at the top of /etc/resolv.conf); the rest of the file is kept.
const (
	resolvBegin = "# OpenSignage begin (static DNS servers)"
	resolvEnd   = "# OpenSignage end"
)

// WithResolvBlock replaces the OpenSignage block of a resolv.conf(.head) with the servers
// (none: the block is removed).
func WithResolvBlock(content string, servers []string) string {
	var out []string
	skip := false
	for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		switch {
		case line == resolvBegin:
			skip = true
		case line == resolvEnd:
			skip = false
		case !skip && line != "":
			out = append(out, line)
		}
	}
	if len(servers) > 0 {
		block := []string{resolvBegin}
		for _, s := range servers {
			block = append(block, "nameserver "+s)
		}
		block = append(block, resolvEnd)
		out = append(block, out...)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}
