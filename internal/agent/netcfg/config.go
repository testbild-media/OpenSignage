// Package netcfg is the network configuration of a player: the wanted settings of a
// wired or wireless interface (DHCP or a static IPv4 address, the wireless network), their
// validation, the configuration files for ifupdown/wpa_supplicant (Debian) and
// NetworkManager (Raspberry Pi OS), and reading the state of the interfaces.
//
// The agent runs without root rights. It prepares a Config and hands it to the root helper
// opensignage-netctl, which validates it again and applies it: first only until the next
// reboot (files under /run), and permanently once the change is confirmed by logging in to
// the client WebGUI again.
package netcfg

import (
	"encoding/hex"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Interface kinds.
const (
	KindEthernet = "ethernet"
	KindWifi     = "wifi"
)

// Address methods.
const (
	MethodDHCP   = "dhcp"
	MethodStatic = "static"
)

// MaxDNS is the number of DNS servers of a static configuration.
const MaxDNS = 3

// IPv4 is a static IPv4 configuration.
type IPv4 struct {
	// Address with prefix length, e.g. "192.168.1.50/24".
	Address string   `json:"address"`
	Gateway string   `json:"gateway,omitempty"`
	DNS     []string `json:"dns,omitempty"`
}

// Wifi is the wireless network an interface joins.
type Wifi struct {
	SSID string `json:"ssid"`
	// Passphrase: 8–63 printable ASCII characters or 64 hex digits; "" with Open = a network
	// without password. Empty without Open keeps the stored passphrase of the same network.
	Passphrase string `json:"passphrase,omitempty"`
	Open       bool   `json:"open,omitempty"`
	Hidden     bool   `json:"hidden,omitempty"`
	// SAE: the network only offers WPA3 (from the scan); otherwise WPA2/WPA3 mixed.
	SAE bool `json:"sae,omitempty"`
	// Country is the ISO 3166 code of the radio rules (e.g. "DE").
	Country string `json:"country"`
}

// Config is the wanted configuration of one interface.
type Config struct {
	Interface string `json:"interface"`
	Kind      string `json:"kind"`
	// Disabled (wireless only): no network; the interface stays off.
	Disabled bool   `json:"disabled,omitempty"`
	Method   string `json:"method,omitempty"`
	Static   *IPv4  `json:"static,omitempty"`
	Wifi     *Wifi  `json:"wifi,omitempty"`
}

// InputError names the invalid field (the WebGUI shows a translated reason).
type InputError struct {
	Field  string
	Reason string
}

func (e *InputError) Error() string { return e.Field + ": " + e.Reason }

func badInput(field, reason string) error { return &InputError{Field: field, Reason: reason} }

var (
	ifaceRe   = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)
	countryRe = regexp.MustCompile(`^[A-Z]{2}$`)
	hexKeyRe  = regexp.MustCompile(`^[0-9A-Fa-f]{64}$`)
)

// ValidInterface reports whether name can be a network interface name (not loopback).
func ValidInterface(name string) bool { return ifaceRe.MatchString(name) && name != "lo" }

// ValidCountry reports whether c is an ISO 3166 alpha-2 code (not the world domain "00").
func ValidCountry(c string) bool { return countryRe.MatchString(c) }

// Validate checks the configuration. It normalises the static addresses (canonical form).
func (c *Config) Validate() error {
	if !ValidInterface(c.Interface) {
		return badInput("interface", "invalid")
	}
	switch c.Kind {
	case KindEthernet:
		if c.Disabled {
			return badInput("disabled", "invalid")
		}
		if c.Wifi != nil {
			return badInput("wifi", "invalid")
		}
	case KindWifi:
		if c.Disabled {
			if c.Wifi != nil && c.Wifi.Country != "" && !ValidCountry(c.Wifi.Country) {
				return badInput("country", "invalid")
			}
			return nil
		}
		if c.Wifi == nil {
			return badInput("ssid", "required")
		}
		if err := c.Wifi.validate(); err != nil {
			return err
		}
	default:
		return badInput("kind", "invalid")
	}
	switch c.Method {
	case MethodDHCP:
		c.Static = nil
	case MethodStatic:
		if c.Static == nil {
			return badInput("address", "required")
		}
		if err := c.Static.normalise(); err != nil {
			return err
		}
	default:
		return badInput("method", "invalid")
	}
	return nil
}

func (w *Wifi) validate() error {
	if w.SSID == "" {
		return badInput("ssid", "required")
	}
	if len(w.SSID) > 32 || !utf8.ValidString(w.SSID) || strings.ContainsFunc(w.SSID, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return badInput("ssid", "invalid")
	}
	if w.Open {
		if w.Passphrase != "" || w.SAE {
			return badInput("passphrase", "invalid")
		}
	} else if w.Passphrase != "" && !ValidPassphrase(w.Passphrase) {
		return badInput("passphrase", "invalid")
	}
	if !ValidCountry(w.Country) {
		return badInput("country", "invalid")
	}
	return nil
}

// ValidPassphrase: 8–63 printable ASCII characters (WPA passphrase) or 64 hex digits (key).
func ValidPassphrase(p string) bool {
	if hexKeyRe.MatchString(p) {
		return true
	}
	if len(p) < 8 || len(p) > 63 {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] > 0x7e {
			return false
		}
	}
	return true
}

// IsHexKey reports whether the passphrase is a raw 64-digit key.
func IsHexKey(p string) bool { return hexKeyRe.MatchString(p) }

// normalise validates the static configuration: a host address inside its network, the
// gateway in the same network, up to MaxDNS DNS servers (IPv4 or IPv6).
func (s *IPv4) normalise() error {
	pfx, err := netip.ParsePrefix(strings.TrimSpace(s.Address))
	if err != nil || !pfx.Addr().Is4() || pfx.Bits() < 1 {
		return badInput("address", "invalid")
	}
	addr := pfx.Addr()
	if addr.IsUnspecified() || addr.IsLoopback() || addr.IsMulticast() || addr.IsLinkLocalUnicast() {
		return badInput("address", "invalid")
	}
	if pfx.Bits() <= 30 {
		network := pfx.Masked().Addr()
		if addr == network || addr == broadcast(pfx) {
			return badInput("address", "network_address")
		}
	}
	s.Address = pfx.String()
	if g := strings.TrimSpace(s.Gateway); g != "" {
		gw, err := netip.ParseAddr(g)
		if err != nil || !gw.Is4() {
			return badInput("gateway", "invalid")
		}
		if !pfx.Masked().Contains(gw) || gw == addr {
			return badInput("gateway", "outside_network")
		}
		s.Gateway = gw.String()
	} else {
		s.Gateway = ""
	}
	var dns []string
	for _, d := range s.DNS {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		a, err := netip.ParseAddr(d)
		if err != nil || a.Zone() != "" || a.IsUnspecified() || a.IsMulticast() {
			return badInput("dns", "invalid")
		}
		dns = append(dns, a.String())
	}
	if len(dns) > MaxDNS {
		return badInput("dns", "too_many")
	}
	s.DNS = dns
	return nil
}

func broadcast(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 1
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	v |= host
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// MaskBits converts a subnet mask ("255.255.255.0") or a prefix length ("24", "/24") into
// the prefix length.
func MaskBits(mask string) (int, error) {
	m := strings.TrimPrefix(strings.TrimSpace(mask), "/")
	if n, err := parseInt(m); err == nil {
		if n < 1 || n > 32 {
			return 0, badInput("netmask", "invalid")
		}
		return n, nil
	}
	a, err := netip.ParseAddr(m)
	if err != nil || !a.Is4() {
		return 0, badInput("netmask", "invalid")
	}
	b := a.As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	bits := 0
	for v&0x80000000 != 0 {
		bits++
		v <<= 1
	}
	if v != 0 || bits == 0 {
		return 0, badInput("netmask", "invalid")
	}
	return bits, nil
}

// Netmask is the dotted subnet mask of a prefix length (24 → "255.255.255.0").
func Netmask(bits int) string {
	if bits < 0 || bits > 32 {
		return ""
	}
	v := uint32(0)
	if bits > 0 {
		v = ^uint32(0) << (32 - bits)
	}
	return fmt.Sprintf("%d.%d.%d.%d", v>>24, v>>16&0xff, v>>8&0xff, v&0xff)
}

func parseInt(s string) (int, error) {
	if s == "" || len(s) > 2 {
		return 0, fmt.Errorf("not a number")
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// SSIDHex is the SSID in hex (wpa_supplicant accepts it unquoted, so no escaping is needed).
func SSIDHex(ssid string) string { return hex.EncodeToString([]byte(ssid)) }
