package netcfg

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reason(err error) string {
	var fe *InputError
	if errors.As(err, &fe) {
		return fe.Field + "/" + fe.Reason
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestValidate(t *testing.T) {
	static := func(addr, gw string, dns ...string) Config {
		return Config{Interface: "enp2s0", Kind: KindEthernet, Method: MethodStatic, Static: &IPv4{Address: addr, Gateway: gw, DNS: dns}}
	}
	cases := map[string]struct {
		c    Config
		want string
	}{
		"dhcp":              {Config{Interface: "eth0", Kind: KindEthernet, Method: MethodDHCP}, ""},
		"static":            {static("192.168.1.50/24", "192.168.1.1", "1.1.1.1", " 2606:4700:4700::1111 "), ""},
		"no gateway":        {static("10.0.0.5/8", ""), ""},
		"bad iface":         {Config{Interface: "eth0;rm", Kind: KindEthernet, Method: MethodDHCP}, "interface/invalid"},
		"loopback":          {Config{Interface: "lo", Kind: KindEthernet, Method: MethodDHCP}, "interface/invalid"},
		"no prefix":         {static("192.168.1.50", ""), "address/invalid"},
		"network address":   {static("192.168.1.0/24", ""), "address/network_address"},
		"broadcast":         {static("192.168.1.255/24", ""), "address/network_address"},
		"gateway outside":   {static("192.168.1.50/24", "192.168.2.1"), "gateway/outside_network"},
		"gateway is self":   {static("192.168.1.50/24", "192.168.1.50"), "gateway/outside_network"},
		"bad dns":           {static("192.168.1.50/24", "", "dns.example"), "dns/invalid"},
		"too many dns":      {static("192.168.1.50/24", "", "1.1.1.1", "8.8.8.8", "9.9.9.9", "1.0.0.1"), "dns/too_many"},
		"ipv6 address":      {static("2001:db8::5/64", ""), "address/invalid"},
		"method missing":    {Config{Interface: "eth0", Kind: KindEthernet}, "method/invalid"},
		"ethernet disabled": {Config{Interface: "eth0", Kind: KindEthernet, Disabled: true}, "disabled/invalid"},
		"wifi": {Config{Interface: "wlan0", Kind: KindWifi, Method: MethodDHCP,
			Wifi: &Wifi{SSID: "Bar Staff", Passphrase: "secret pass!", Country: "DE"}}, ""},
		"wifi open": {Config{Interface: "wlan0", Kind: KindWifi, Method: MethodDHCP, Wifi: &Wifi{SSID: "Guest", Open: true, Country: "AT"}}, ""},
		"wifi keep": {Config{Interface: "wlan0", Kind: KindWifi, Method: MethodDHCP, Wifi: &Wifi{SSID: "Bar", Country: "DE"}}, ""},
		"wifi off":  {Config{Interface: "wlan0", Kind: KindWifi, Disabled: true}, ""},
		"short pass": {Config{Interface: "wlan0", Kind: KindWifi, Method: MethodDHCP,
			Wifi: &Wifi{SSID: "Bar", Passphrase: "short", Country: "DE"}}, "passphrase/invalid"},
		"pass newline": {Config{Interface: "wlan0", Kind: KindWifi, Method: MethodDHCP,
			Wifi: &Wifi{SSID: "Bar", Passphrase: "line\nbreak1", Country: "DE"}}, "passphrase/invalid"},
		"long ssid": {Config{Interface: "wlan0", Kind: KindWifi, Method: MethodDHCP,
			Wifi: &Wifi{SSID: strings.Repeat("x", 33), Open: true, Country: "DE"}}, "ssid/invalid"},
		"ssid control": {Config{Interface: "wlan0", Kind: KindWifi, Method: MethodDHCP,
			Wifi: &Wifi{SSID: "a\tb", Open: true, Country: "DE"}}, "ssid/invalid"},
		"country": {Config{Interface: "wlan0", Kind: KindWifi, Method: MethodDHCP,
			Wifi: &Wifi{SSID: "Bar", Open: true, Country: "de"}}, "country/invalid"},
	}
	for name, tc := range cases {
		c := tc.c
		if got := reason(c.Validate()); got != tc.want {
			t.Errorf("%s: %q (want %q)", name, got, tc.want)
		}
	}
	c := static("192.168.001.050/24", "192.168.1.1", " 1.1.1.1 ", "")
	if reason(c.Validate()) != "address/invalid" {
		t.Error("leading zeros are not accepted")
	}
	c = static("192.168.1.50/24", "192.168.1.1", " 1.1.1.1 ", "")
	if err := c.Validate(); err != nil || c.Static.DNS[0] != "1.1.1.1" || len(c.Static.DNS) != 1 {
		t.Fatalf("normalised: %v %+v", err, c.Static)
	}
}

func TestMask(t *testing.T) {
	for in, want := range map[string]int{"255.255.255.0": 24, "/22": 22, "8": 8, "255.255.252.0": 22, "255.255.255.255": 32} {
		if got, err := MaskBits(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, bad := range []string{"255.0.255.0", "33", "0", "", "abc", "0.0.0.0"} {
		if _, err := MaskBits(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if Netmask(22) != "255.255.252.0" || Netmask(32) != "255.255.255.255" || Netmask(0) != "0.0.0.0" {
		t.Error("netmask")
	}
}

func TestFiles(t *testing.T) {
	st := Config{Interface: "enp2s0", Kind: KindEthernet, Method: MethodStatic, Static: &IPv4{Address: "10.20.3.126/22", Gateway: "10.20.0.1", DNS: []string{"10.20.0.1"}}}
	got := IfupdownStanza(st, "", "")
	if !strings.Contains(got, "allow-hotplug enp2s0\niface enp2s0 inet static\n\taddress 10.20.3.126/22\n\tgateway 10.20.0.1\n\tdns-nameservers 10.20.0.1\n") {
		t.Errorf("static stanza:\n%s", got)
	}
	w := Config{Interface: "wlx1", Kind: KindWifi, Method: MethodDHCP, Wifi: &Wifi{SSID: `Bar "Nord"`, Passphrase: `p"a\ss word`, Country: "DE", Hidden: true}}
	if got := IfupdownStanza(w, "/run/x.conf", ""); !strings.Contains(got, "iface wlx1 inet dhcp\n\twpa-conf /run/x.conf\n\tpost-up iw dev wlx1 set power_save off || true\n") {
		t.Errorf("wifi stanza:\n%s", got)
	}
	conf := WpaConf(*w.Wifi)
	for _, want := range []string{"country=DE\n", "\tssid=" + SSIDHex(`Bar "Nord"`) + "\n", "\tscan_ssid=1\n", "key_mgmt=WPA-PSK WPA-PSK-SHA256 SAE", `psk="p"a\ss word"`} {
		if !strings.Contains(conf, want) {
			t.Errorf("wpa conf misses %q:\n%s", want, conf)
		}
	}
	if WpaPassphrase(conf, `Bar "Nord"`) != `p"a\ss word` || WpaPassphrase(conf, "other") != "" {
		t.Error("passphrase read back")
	}
	open := WpaConf(Wifi{SSID: "Guest", Open: true, Country: "AT"})
	if !strings.Contains(open, "key_mgmt=NONE") || strings.Contains(open, "psk") {
		t.Errorf("open:\n%s", open)
	}
	sae := WpaConf(Wifi{SSID: "New", Passphrase: "wpa3-only-pass", SAE: true, Country: "DE"})
	if !strings.Contains(sae, "key_mgmt=SAE\n\tieee80211w=2\n\tsae_password=\"wpa3-only-pass\"") {
		t.Errorf("sae:\n%s", sae)
	}

	kf := NMKeyfile(w, "opensignage-wlx1", "u-1")
	ssid := keyfileSSID(`Bar "Nord"`)
	for _, want := range []string{"id=opensignage-wlx1\nuuid=u-1\ntype=wifi\ninterface-name=wlx1\n", "ssid=" + ssid + "\n", "[wifi]\nmode=infrastructure\npowersave=2\n", "hidden=true", "key-mgmt=wpa-psk", `psk=p"a\\ss word`, "[ipv4]\nmethod=auto"} {
		if !strings.Contains(kf, want) {
			t.Errorf("keyfile misses %q:\n%s", want, kf)
		}
	}
	if !strings.HasPrefix(ssid, "66;97;114;32;34;") {
		t.Errorf("special SSID as bytes: %s", ssid)
	}
	if KeyfilePassphrase(kf, `Bar "Nord"`) != `p"a\ss word` {
		t.Errorf("keyfile passphrase: %q", KeyfilePassphrase(kf, `Bar "Nord"`))
	}
	if keyfileSSID("Bar Staff") != "Bar Staff" {
		t.Error("plain SSID")
	}
	ekf := NMKeyfile(st, "opensignage-enp2s0", "u-2")
	if !strings.Contains(ekf, "method=manual\naddress1=10.20.3.126/22,10.20.0.1\ndns=10.20.0.1;\n") || !strings.Contains(ekf, "type=ethernet") {
		t.Errorf("ethernet keyfile:\n%s", ekf)
	}
}

func TestDisableStanzas(t *testing.T) {
	in := "source /etc/network/interfaces.d/*\n\nauto lo enp2s0\niface lo inet loopback\n\n# The primary network interface\nallow-hotplug enp2s0\niface enp2s0 inet dhcp\n\twpa-ssid x\n\niface eth1 inet dhcp\n"
	out, changed := DisableStanzas(in, "enp2s0")
	if !changed {
		t.Fatal("unchanged")
	}
	want := "source /etc/network/interfaces.d/*\n\n#opensignage# auto lo enp2s0\nauto lo\niface lo inet loopback\n\n# The primary network interface\n#opensignage# allow-hotplug enp2s0\n#opensignage# iface enp2s0 inet dhcp\n#opensignage# \twpa-ssid x\n\niface eth1 inet dhcp\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	if ConfiguredIn(out, "enp2s0") || !ConfiguredIn(out, "eth1") {
		t.Error("configured")
	}
	if _, changed := DisableStanzas(out, "enp2s0"); changed {
		t.Error("second run changes nothing")
	}
}

func TestInet6Stanza(t *testing.T) {
	sys := "allow-hotplug enp2s0\niface enp2s0 inet dhcp\n\niface enp2s0 inet6 auto\n\t# comment\n\tprivext 2\n\niface eth1 inet6 auto\n"
	want := "iface enp2s0 inet6 auto\n\tprivext 2\n"
	if got := Inet6Stanza(sys, "enp2s0"); got != want {
		t.Fatalf("got %q", got)
	}
	// Also after OpenSignage commented it out (it is carried over into its own stanza).
	disabled, _ := DisableStanzas(sys, "enp2s0")
	if got := Inet6Stanza(disabled, "enp2s0"); got != want {
		t.Fatalf("disabled: got %q", got)
	}
	if Inet6Stanza(sys, "wlan0") != "" || Inet6Stanza("# iface enp2s0 inet6 auto\n", "enp2s0") != "" {
		t.Error("none expected")
	}
	st := Config{Interface: "enp2s0", Kind: KindEthernet, Method: MethodStatic, Static: &IPv4{Address: "10.0.0.2/24"}}
	if got := IfupdownStanza(st, "", want); !strings.HasSuffix(got, "\taddress 10.0.0.2/24\n"+want) {
		t.Fatalf("stanza with inet6:\n%s", got)
	}
}

func TestResolvBlock(t *testing.T) {
	base := "# Generated by dhcpcd\nnameserver 10.20.0.1\n"
	with := WithResolvBlock(base, []string{"1.1.1.1", "9.9.9.9"})
	if with != "# OpenSignage begin (static DNS servers)\nnameserver 1.1.1.1\nnameserver 9.9.9.9\n# OpenSignage end\n# Generated by dhcpcd\nnameserver 10.20.0.1\n" {
		t.Fatalf("with:\n%s", with)
	}
	if WithResolvBlock(with, []string{"8.8.8.8"}) != "# OpenSignage begin (static DNS servers)\nnameserver 8.8.8.8\n# OpenSignage end\n# Generated by dhcpcd\nnameserver 10.20.0.1\n" {
		t.Error("replace")
	}
	if WithResolvBlock(with, nil) != base || WithResolvBlock("", nil) != "" {
		t.Error("remove")
	}
}

func TestParsers(t *testing.T) {
	var it Interface
	ParseIwLink("Connected to aa:bb:cc:dd:ee:ff (on wlan0)\n\tSSID: Bar\\x20Nord\n\tfreq: 5180.0\n\tsignal: -61 dBm\n\ttx bitrate: 390.0 MBit/s VHT-MCS 8\n", &it)
	if it.SSID != "Bar Nord" || it.FreqMHz != 5180 || it.SignalDBM != -61 || it.BitrateMbps != 390 {
		t.Errorf("iw link: %+v", it)
	}
	iw := "BSS 11:22:33:44:55:66(on wlan0)\n\tfreq: 2412\n\tcapability: ESS Privacy ShortSlotTime (0x0411)\n\tsignal: -48.00 dBm\n\tSSID: Bar\n\tRSN:\t * Version: 1\n\t\t * Authentication suites: PSK SAE\n\tWMM:\n" +
		"BSS 11:22:33:44:55:77(on wlan0)\n\tfreq: 5180\n\tsignal: -70.00 dBm\n\tSSID: Bar\n\tRSN:\t * Version: 1\n\t\t * Authentication suites: PSK SAE\n" +
		"BSS aa:22:33:44:55:66(on wlan0)\n\tfreq: 2437\n\tcapability: ESS (0x0401)\n\tsignal: -80.00 dBm\n\tSSID: Guest\n" +
		"BSS bb:22:33:44:55:66(on wlan0)\n\tfreq: 2437\n\tcapability: ESS Privacy (0x0411)\n\tsignal: -60.00 dBm\n\tSSID: Office\n\tRSN:\t * Version: 1\n\t\t * Authentication suites: IEEE 802.1X\n" +
		"BSS cc:22:33:44:55:66(on wlan0)\n\tfreq: 2437\n\tsignal: -50.00 dBm\n\tSSID: \n"
	aps := ParseIwScan(iw)
	if len(aps) != 3 || aps[0].SSID != "Bar" || aps[0].SignalDBM != -48 || aps[0].Security != SecWPA23 || aps[0].FreqMHz != 2412 {
		t.Fatalf("iw scan: %+v", aps)
	}
	if aps[1].SSID != "Office" || aps[1].Security != SecEnterprise || aps[1].Supported() || aps[2].Security != SecOpen {
		t.Errorf("iw scan rest: %+v", aps)
	}
	wpa := "bssid / frequency / signal level / flags / ssid\n11:22:33:44:55:66\t2412\t-52\t[WPA2-PSK-CCMP][ESS]\tBar\naa:22:33:44:55:66\t5500\t-71\t[WPA2-SAE-CCMP][ESS]\tNew\nbb:22:33:44:55:66\t2437\t-60\t[ESS]\tGuest\\x21\n"
	aps = ParseWpaScanResults(wpa)
	if len(aps) != 3 || aps[0].SSID != "Bar" || aps[0].Security != SecWPA2 || aps[1].SSID != "Guest!" || aps[2].Security != SecWPA3 {
		t.Errorf("wpa_cli: %+v", aps)
	}
	nm := "Bar:84:WPA2 WPA3:5180 MHz\nOffice\\:2:60:WPA2 802.1X:2437 MHz\n:40:WPA2:2412 MHz\nGuest:30::2412 MHz\n"
	aps = ParseNmcliWifi(nm)
	if len(aps) != 3 || aps[0].SSID != "Bar" || aps[0].SignalPct != 84 || aps[0].Security != SecWPA23 || aps[1].SSID != "Office:2" || aps[1].Security != SecEnterprise || aps[2].Security != SecOpen {
		t.Errorf("nmcli: %+v", aps)
	}
	if ParseRegCountry("global\ncountry DE: DFS-ETSI\n\t(2400 - 2483 @ 40)\n") != "DE" || ParseRegCountry("global\ncountry 00: DFS-UNSET\n") != "" {
		t.Error("reg country")
	}
	if SignalPercent(-61) != 78 || SignalQuality(-61) != "good" || SignalQuality(-80) != "weak" || Band(5180) != "5" || Channel(5180) != 36 || Channel(2412) != 1 {
		t.Error("signal helpers")
	}
	missing := MissingFirmware("[ 3.1] iwlwifi 0000:02:00.0: Direct firmware load for iwlwifi-8265-36.ucode failed with error -2\n[ 4.2] usb 1-2: Direct firmware load for rtw88/rtw8822b_fw.bin failed with error -2\n[ 4.3] x: Direct firmware load for rtw88/rtw8822b_fw.bin failed with error -2\n")
	if len(missing) != 2 || FirmwarePackage(missing[0]) != "firmware-iwlwifi" || FirmwarePackage(missing[1]) != "firmware-realtek" {
		t.Errorf("firmware: %v", missing)
	}
	for f, p := range map[string]string{"mediatek/WIFI_RAM_CODE_MT7961_1.bin": "firmware-mediatek", "ath10k/QCA6174/hw3.0/firmware-6.bin": "firmware-atheros", "brcm/brcmfmac43455-sdio.bin": "firmware-brcm80211", "i915/skl_dmc.bin": "", "armada/x.bin": ""} {
		if FirmwarePackage(f) != p {
			t.Errorf("%s → %q", f, FirmwarePackage(f))
		}
	}
	if !AllowedFirmwarePackage("firmware-realtek") || AllowedFirmwarePackage("openssh-server") {
		t.Error("allowed packages")
	}
}

// fakeSys builds /sys/class/net with a wired and a wireless interface.
func fakeSys(t *testing.T, root string) {
	t.Helper()
	mk := func(p, content string) {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"enp2s0", "wlan0"} {
		mk("/sys/class/net/"+n+"/device/vendor", "0x10ec")
		mk("/sys/class/net/"+n+"/type", "1\n")
		mk("/sys/class/net/"+n+"/operstate", "up\n")
		mk("/sys/class/net/"+n+"/carrier", "1\n")
		mk("/sys/class/net/"+n+"/address", "02:00:00:00:00:01\n")
	}
	mk("/sys/class/net/enp2s0/speed", "1000\n")
	mk("/sys/class/net/wlan0/phy80211/rfkill0/soft", "1\n")
	mk("/sys/class/net/wlan0/phy80211/rfkill0/hard", "0\n")
	mk("/sys/class/net/tailscale0/type", "65534\n")
	mk("/proc/net/route", "Iface\tDestination\tGateway \tFlags\nenp2s0\t00000000\t0100140A\t0003\nenp2s0\t0000140A\t00000000\t0001\n")
	mk("/etc/resolv.conf", "# Generated by dhcpcd\nnameserver 10.20.0.1\nnameserver 1.1.1.1\n")
	mk("/etc/network/interfaces", "source /etc/network/interfaces.d/*\nauto lo\niface lo inet loopback\nallow-hotplug enp2s0\niface enp2s0 inet dhcp\n")
	mk("/etc/network/interfaces.d/opensignage-wlan0", Header+"\nallow-hotplug wlan0\niface wlan0 inet static\n\taddress 192.168.5.20/24\n\tgateway 192.168.5.1\n\twpa-conf /etc/opensignage/network/wpa-wlan0.conf\n")
}

func TestInspect(t *testing.T) {
	root := t.TempDir()
	fakeSys(t, root)
	env := Env{Root: root,
		Addrs: func(name string) ([]net.Addr, error) {
			if name == "enp2s0" {
				return []net.Addr{&net.IPNet{IP: net.ParseIP("10.20.3.126").To4(), Mask: net.CIDRMask(22, 32)}, &net.IPNet{IP: net.ParseIP("2001:db8::1"), Mask: net.CIDRMask(64, 128)}, &net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)}}, nil
			}
			return nil, nil
		},
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch strings.Join(append([]string{name}, args...), " ") {
			case "iw dev wlan0 link":
				return []byte("Connected to aa (on wlan0)\n\tSSID: Bar\n\tfreq: 2412\n\tsignal: -58 dBm\n"), nil
			case "iw reg get":
				return []byte("global\ncountry DE: DFS-ETSI\n"), nil
			}
			return nil, errors.New("unexpected")
		}}
	st := env.Inspect(context.Background())
	if st.Backend != BackendIfupdown || len(st.Interfaces) != 2 || st.Country != "DE" || strings.Join(st.DNS, ",") != "10.20.0.1,1.1.1.1" {
		t.Fatalf("status: %+v", st)
	}
	eth, wl := st.Interfaces[0], st.Interfaces[1]
	if eth.Name != "enp2s0" || eth.Kind != KindEthernet || eth.SpeedMbps != 1000 || eth.Gateway != "10.20.0.1" || eth.Method != MethodDHCP || eth.Managed ||
		strings.Join(eth.IPv4, ",") != "10.20.3.126/22" || strings.Join(eth.IPv6, ",") != "2001:db8::1" {
		t.Errorf("ethernet: %+v", eth)
	}
	if wl.Kind != KindWifi || !wl.Blocked || wl.SSID != "Bar" || wl.SignalDBM != -58 || wl.Method != MethodStatic || !wl.Managed ||
		wl.Static == nil || wl.Static.Address != "192.168.5.20/24" || wl.Static.Gateway != "192.168.5.1" {
		t.Errorf("wifi: %+v %+v", wl, wl.Static)
	}
}
