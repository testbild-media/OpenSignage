package netcfg

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Security of a wireless network as the WebGUI shows it.
const (
	SecOpen       = "open"
	SecWEP        = "wep"        // not supported
	SecWPA        = "wpa"        // WPA (v1)
	SecWPA2       = "wpa2"       // WPA2-Personal
	SecWPA23      = "wpa2/wpa3"  // transition mode
	SecWPA3       = "wpa3"       // SAE only
	SecEnterprise = "enterprise" // 802.1X, not supported
)

// AP is a wireless network found by a scan (the strongest access point of an SSID).
type AP struct {
	SSID      string `json:"ssid"`
	SignalDBM int    `json:"signalDbm,omitempty"` // when the scanner reports dBm
	SignalPct int    `json:"signalPct"`
	FreqMHz   int    `json:"freqMHz,omitempty"`
	Security  string `json:"security"`
}

// Supported reports whether the WebGUI can join the network (personal or open).
func (a AP) Supported() bool { return a.Security != SecWEP && a.Security != SecEnterprise }

// Joinable networks: keep the strongest entry per SSID, drop hidden ones, strongest first.
func dedupe(aps []AP) []AP {
	best := map[string]AP{}
	for _, a := range aps {
		if a.SSID == "" {
			continue
		}
		if b, ok := best[a.SSID]; !ok || a.SignalPct > b.SignalPct {
			best[a.SSID] = a
		}
	}
	out := make([]AP, 0, len(best))
	for _, a := range best {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SignalPct != out[j].SignalPct {
			return out[i].SignalPct > out[j].SignalPct
		}
		return out[i].SSID < out[j].SSID
	})
	return out
}

// ParseIwScan reads `iw dev X scan`.
func ParseIwScan(out string) []AP {
	var aps []AP
	var cur *AP
	var rsn, wpa, privacy, inRSN bool
	var suites string
	finish := func() {
		if cur == nil {
			return
		}
		cur.Security = securityOf(rsn, wpa, privacy, suites)
		cur.SignalPct = SignalPercent(cur.SignalDBM)
		aps = append(aps, *cur)
	}
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(line, "BSS ") {
			finish()
			cur = &AP{}
			rsn, wpa, privacy, inRSN, suites = false, false, false, false, ""
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(t, "freq: "):
			if f, err := strconv.ParseFloat(strings.TrimPrefix(t, "freq: "), 64); err == nil {
				cur.FreqMHz = int(f)
			}
		case strings.HasPrefix(t, "signal: "):
			if f := strings.Fields(strings.TrimPrefix(t, "signal: ")); len(f) > 0 {
				if v, err := strconv.ParseFloat(f[0], 64); err == nil {
					cur.SignalDBM = int(v)
				}
			}
		case strings.HasPrefix(t, "SSID: ") || t == "SSID:":
			cur.SSID = unescapeIw(strings.TrimPrefix(strings.TrimPrefix(t, "SSID:"), " "))
		case strings.HasPrefix(t, "capability:"):
			privacy = strings.Contains(t, "Privacy")
		case strings.HasPrefix(t, "RSN:"):
			rsn, inRSN = true, true
		case strings.HasPrefix(t, "WPA:"):
			wpa, inRSN = true, true
		case inRSN && strings.HasPrefix(t, "* Authentication suites:"):
			suites += " " + strings.TrimPrefix(t, "* Authentication suites:")
		case !strings.HasPrefix(t, "*"):
			inRSN = false
		}
	}
	finish()
	return dedupe(aps)
}

func securityOf(rsn, wpa, privacy bool, suites string) string {
	s := " " + suites + " "
	switch {
	case strings.Contains(s, "IEEE 802.1X") || strings.Contains(s, "802.1X"):
		return SecEnterprise
	case rsn && strings.Contains(s, " SAE ") && strings.Contains(s, " PSK "):
		return SecWPA23
	case rsn && strings.Contains(s, " SAE "):
		return SecWPA3
	case rsn:
		return SecWPA2
	case wpa:
		return SecWPA
	case privacy:
		return SecWEP
	}
	return SecOpen
}

// ParseWpaScanResults reads `wpa_cli -i X scan_results` (bssid, frequency, signal, flags, ssid).
func ParseWpaScanResults(out string) []AP {
	var aps []AP
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 5)
		if len(f) < 5 || !strings.Contains(f[0], ":") {
			continue
		}
		freq, _ := strconv.Atoi(f[1])
		sig, _ := strconv.Atoi(f[2])
		flags := f[3]
		sec := SecOpen
		switch {
		case strings.Contains(flags, "EAP"):
			sec = SecEnterprise
		case strings.Contains(flags, "SAE") && strings.Contains(flags, "-PSK"):
			sec = SecWPA23
		case strings.Contains(flags, "SAE"):
			sec = SecWPA3
		case strings.Contains(flags, "WPA2") || strings.Contains(flags, "RSN"):
			sec = SecWPA2
		case strings.Contains(flags, "WPA"):
			sec = SecWPA
		case strings.Contains(flags, "WEP"):
			sec = SecWEP
		}
		aps = append(aps, AP{SSID: unescapeIw(f[4]), SignalDBM: sig, SignalPct: SignalPercent(sig), FreqMHz: freq, Security: sec})
	}
	return dedupe(aps)
}

// ParseNmcliWifi reads `nmcli -t -f SSID,SIGNAL,SECURITY,FREQ device wifi list`.
func ParseNmcliWifi(out string) []AP {
	var aps []AP
	for _, line := range strings.Split(out, "\n") {
		f := splitTerse(strings.TrimRight(line, "\r"))
		if len(f) < 4 {
			continue
		}
		pct, _ := strconv.Atoi(f[1])
		freq, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(f[3]), " MHz"))
		secs := " " + f[2] + " "
		sec := SecOpen
		switch {
		case strings.Contains(secs, "802.1X"):
			sec = SecEnterprise
		case strings.Contains(secs, "WPA3") && strings.Contains(secs, "WPA2"):
			sec = SecWPA23
		case strings.Contains(secs, "WPA3"):
			sec = SecWPA3
		case strings.Contains(secs, "WPA2"):
			sec = SecWPA2
		case strings.Contains(secs, "WPA1") || strings.Contains(secs, " WPA "):
			sec = SecWPA
		case strings.Contains(secs, "WEP"):
			sec = SecWEP
		}
		aps = append(aps, AP{SSID: f[0], SignalPct: pct, FreqMHz: freq, Security: sec})
	}
	return dedupe(aps)
}

// splitTerse splits a terse nmcli line at unescaped colons.
func splitTerse(line string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\\' && i+1 < len(line) {
			cur.WriteByte(line[i+1])
			i++
			continue
		}
		if c == ':' {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	return append(out, cur.String())
}

var firmwareFailRe = regexp.MustCompile(`Direct firmware load for (\S+) failed`)

// MissingFirmware lists the firmware files the kernel could not load (from dmesg).
func MissingFirmware(dmesg string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range firmwareFailRe.FindAllStringSubmatch(dmesg, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// FirmwarePackages are the Debian packages (component non-free-firmware) with network
// firmware that the root helper may install.
var FirmwarePackages = []string{"firmware-iwlwifi", "firmware-realtek", "firmware-atheros", "firmware-mediatek", "firmware-brcm80211", "firmware-misc-nonfree", "firmware-libertas", "firmware-ti-connectivity"}

// FirmwarePackage is the Debian package with a network firmware file ("" = not a network
// firmware OpenSignage knows).
func FirmwarePackage(file string) string {
	f := strings.ToLower(file)
	switch {
	case strings.HasPrefix(f, "iwlwifi-"):
		return "firmware-iwlwifi"
	case strings.HasPrefix(f, "rtw88/"), strings.HasPrefix(f, "rtw89/"), strings.HasPrefix(f, "rtlwifi/"), strings.HasPrefix(f, "rtl_nic/"):
		return "firmware-realtek"
	case strings.HasPrefix(f, "ath9k_htc/"), strings.HasPrefix(f, "ath10k/"), strings.HasPrefix(f, "ath11k/"), strings.HasPrefix(f, "ath12k/"),
		strings.HasPrefix(f, "ar3k/"), strings.HasPrefix(f, "ar5"), strings.HasPrefix(f, "ar7"), strings.HasPrefix(f, "ar9"):
		return "firmware-atheros"
	case strings.HasPrefix(f, "mediatek/"), strings.HasPrefix(f, "mt7"):
		return "firmware-mediatek"
	case strings.HasPrefix(f, "brcm/"):
		return "firmware-brcm80211"
	case strings.HasPrefix(f, "libertas/"), strings.HasPrefix(f, "mrvl/"):
		return "firmware-libertas"
	case strings.HasPrefix(f, "ti-connectivity/"):
		return "firmware-ti-connectivity"
	}
	return ""
}

// AllowedFirmwarePackage reports whether the root helper may install the package.
func AllowedFirmwarePackage(p string) bool {
	for _, x := range FirmwarePackages {
		if x == p {
			return true
		}
	}
	return false
}
