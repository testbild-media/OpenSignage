package webgui

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"opensignage/internal/agent/display"
	"opensignage/internal/agent/netcfg"
	"opensignage/internal/common/passwords"
	"opensignage/locales"
)

// netEnv is a logged-in WebGUI (central password: no forced change) with one wired and one
// wireless adapter.
func netEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	h, _ := passwords.Hash("central-secret")
	e.be.central = h
	e.be.net = NetworkView{Available: true, Country: "DE", Status: netcfg.Status{
		Interfaces: []netcfg.Interface{
			{Name: "eth0", Kind: netcfg.KindEthernet, Up: true, Carrier: true, IPv4: []string{"10.0.0.2/24"}, Gateway: "10.0.0.1",
				Method: netcfg.MethodDHCP, MAC: "02:00:00:00:00:01", SpeedMbps: 1000},
			{Name: "wlan0", Kind: netcfg.KindWifi, Up: true, MAC: "02:00:00:00:00:02"},
		},
		DNS: []string{"10.0.0.1"},
	}}
	if resp := e.login(t, "central-secret"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	return e
}

func (e *env) en(t *testing.T, key string, args ...any) string {
	t.Helper()
	return html.EscapeString(e.srv.t("en", key, args...))
}

func TestNetworkPage(t *testing.T) {
	e := netEnv(t)
	resp, body := e.get(t, "/network")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("network page: %d", resp.StatusCode)
	}
	for _, want := range []string{`id="if-eth0"`, `id="if-wlan0"`, `id="form-eth0"`, `id="form-wlan0"`, "10.0.0.2/24",
		`name="address" value="10.0.0.2"`, `name="netmask" value="255.255.255.0"`, `name="gateway" value="10.0.0.1"`, `name="dns1" value="10.0.0.1"`,
		`name="country" maxlength="2" pattern="[A-Za-z]{2}" value="DE"`, e.en(t, "network.state.connected"), e.en(t, "network.state.disconnected")} {
		if !strings.Contains(body, want) {
			t.Errorf("network page without %q", want)
		}
	}
	if strings.Contains(body, e.en(t, "network.noWifi")) {
		t.Error("an adapter is there: no hint about a missing one")
	}

	// No wireless adapter: shown as not available, no Wi-Fi form.
	e.be.net.Status.Interfaces = e.be.net.Status.Interfaces[:1]
	_, body = e.get(t, "/network")
	if !strings.Contains(body, e.en(t, "network.noWifi")) || strings.Contains(body, `name="ssid"`) {
		t.Fatalf("missing Wi-Fi adapter not shown: %s", body)
	}

	// Without the root helper of this version the page only shows the state.
	e.be.net.Available = false
	_, body = e.get(t, "/network")
	if !strings.Contains(body, e.en(t, "network.helperMissing")) || strings.Contains(body, `value="apply"`) {
		t.Fatalf("read-only page expected: %s", body)
	}
}

func TestNetworkScan(t *testing.T) {
	e := netEnv(t)
	e.be.scan = []netcfg.AP{
		{SSID: "Bar & Lounge", SignalDBM: -50, SignalPct: 100, FreqMHz: 5180, Security: netcfg.SecWPA23},
		{SSID: "Legacy", SignalDBM: -80, SignalPct: 40, FreqMHz: 2412, Security: netcfg.SecWEP},
		{SSID: "Hall", SignalPct: 72, FreqMHz: 2437, Security: netcfg.SecWPA2,
			Bands: []netcfg.BandSignal{{FreqMHz: 2437, SignalPct: 72}, {FreqMHz: 5200, SignalPct: 57}}},
	}
	_, body := e.get(t, "/network")
	resp, body := e.post(t, "/network", url.Values{"csrf": {csrf(t, body)}, "do": {"scan"}, "iface": {"wlan0"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scan: %d", resp.StatusCode)
	}
	for _, want := range []string{"Bar &amp; Lounge", "ssid=Bar%20%26%20Lounge", "sec=wpa2%2fwpa3", e.en(t, "network.security.wpa2wpa3"),
		"<td>5 GHz</td>", "<td>2.4 GHz 72 % · 5 GHz 57 %</td>",
		`class="unsupported"`, e.en(t, "network.unsupported")} {
		if !strings.Contains(body, want) {
			t.Errorf("scan result without %q", want)
		}
	}
	// Choosing a network fills the form.
	_, body = e.get(t, "/network?iface=wlan0&ssid=Bar+%26+Lounge&sec=wpa3")
	if !strings.Contains(body, `name="ssid" maxlength="32" value="Bar &amp; Lounge"`) || !strings.Contains(body, `name="sae" value="yes"`) {
		t.Fatalf("chosen network not in the form: %s", body)
	}
}

func TestNetworkApplyAndConfirm(t *testing.T) {
	e := netEnv(t)
	_, body := e.get(t, "/network")
	token := csrf(t, body)
	static := func(gw string) url.Values {
		return url.Values{"csrf": {token}, "do": {"apply"}, "iface": {"eth0"}, "kind": {"ethernet"}, "method": {"static"},
			"address": {"10.0.0.50"}, "netmask": {"255.255.255.0"}, "gateway": {gw}, "dns1": {"10.0.0.1"}, "dns2": {""}}
	}

	// A gateway outside the subnet is refused with its reason; the input stays in the form.
	resp, body := e.post(t, "/network", static("10.9.0.1"))
	if resp.StatusCode != http.StatusUnprocessableEntity || len(e.be.applied) != 0 {
		t.Fatalf("invalid gateway: %d, applied %d", resp.StatusCode, len(e.be.applied))
	}
	if !strings.Contains(body, e.en(t, "network.error.gateway_outside_network")) || !strings.Contains(body, `name="gateway" value="10.9.0.1"`) {
		t.Fatalf("error page: %s", body)
	}
	if resp, _ := e.post(t, "/network", url.Values{"csrf": {token}, "do": {"apply"}, "iface": {"eth0"}, "kind": {"ethernet"},
		"method": {"static"}, "address": {"10.0.0.50"}, "netmask": {"255.0.255.0"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid netmask: %d", resp.StatusCode)
	}

	// A valid change: applied, every session ends, the login page explains the confirmation.
	resp, body = e.post(t, "/network", static("10.0.0.1"))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, e.en(t, "network.applied.text")) {
		t.Fatalf("apply: %d %s", resp.StatusCode, body)
	}
	if len(e.be.applied) != 1 {
		t.Fatalf("applied %d", len(e.be.applied))
	}
	c := e.be.applied[0]
	if c.Interface != "eth0" || c.Kind != netcfg.KindEthernet || c.Method != netcfg.MethodStatic || c.Static == nil ||
		c.Static.Address != "10.0.0.50/24" || c.Static.Gateway != "10.0.0.1" || len(c.Static.DNS) != 1 || c.Static.DNS[0] != "10.0.0.1" {
		t.Fatalf("applied config %+v %+v", c, c.Static)
	}
	if resp, _ := e.get(t, "/network"); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("session must end after a change: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, body = e.get(t, "/login")
	if !strings.Contains(body, e.en(t, "login.networkPending")) {
		t.Fatal("login page without the confirmation hint")
	}

	// The next login confirms the change.
	resp = e.login(t, "central-secret")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/network?ok=networkConfirmed" || e.be.confirmed != 1 {
		t.Fatalf("login must confirm: %d %s confirmed=%d", resp.StatusCode, resp.Header.Get("Location"), e.be.confirmed)
	}
	if _, body = e.get(t, "/network?ok=networkConfirmed"); !strings.Contains(body, e.en(t, "flash.networkConfirmed")) {
		t.Fatal("no confirmation message")
	}
	// Nothing pending: a later login goes to the status page.
	e.client.Jar, _ = cookiejar.New(nil)
	if resp = e.login(t, "central-secret"); resp.Header.Get("Location") != "/" || e.be.confirmed != 1 {
		t.Fatalf("login without a pending change: %s confirmed=%d", resp.Header.Get("Location"), e.be.confirmed)
	}
}

func TestNetworkWifiApply(t *testing.T) {
	e := netEnv(t)
	_, body := e.get(t, "/network")
	token := csrf(t, body)
	// The passphrase is never shown again after an error.
	resp, body := e.post(t, "/network", url.Values{"csrf": {token}, "do": {"apply"}, "iface": {"wlan0"}, "kind": {"wifi"},
		"method": {"dhcp"}, "ssid": {"Bar"}, "passphrase": {"short"}, "country": {"de"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, e.en(t, "network.error.passphrase")) || strings.Contains(body, "short") {
		t.Fatalf("short passphrase: %d %s", resp.StatusCode, body)
	}
	resp, _ = e.post(t, "/network", url.Values{"csrf": {token}, "do": {"apply"}, "iface": {"wlan0"}, "kind": {"wifi"},
		"method": {"dhcp"}, "ssid": {"Bar"}, "passphrase": {"long enough"}, "country": {"de"}})
	if resp.StatusCode != http.StatusOK || len(e.be.applied) != 1 {
		t.Fatalf("wifi apply: %d", resp.StatusCode)
	}
	w := e.be.applied[0].Wifi
	if w == nil || w.SSID != "Bar" || w.Passphrase != "long enough" || w.Country != "DE" || w.Open {
		t.Fatalf("wifi config %+v", w)
	}
}

func TestNetworkWifiOffAndConfirmFailure(t *testing.T) {
	e := netEnv(t)
	e.be.net.Configured = map[string]*netcfg.Config{"wlan0": {Interface: "wlan0", Kind: netcfg.KindWifi, Method: netcfg.MethodDHCP,
		Wifi: &netcfg.Wifi{SSID: "Bar", Country: "AT"}}}
	_, body := e.get(t, "/network")
	if !strings.Contains(body, `value="wifiOff"`) || !strings.Contains(body, e.en(t, "network.passphraseKeep")) || !strings.Contains(body, `value="AT"`) {
		t.Fatalf("configured Wi-Fi: %s", body)
	}
	// Set up but not connected (e.g. in advance, out of range): the page says it joins later.
	if !strings.Contains(body, e.en(t, "network.waitingFor", "ssid", "Bar")) {
		t.Fatal("no hint that the player joins the network when it is in range")
	}
	resp, _ := e.post(t, "/network", url.Values{"csrf": {csrf(t, body)}, "do": {"wifiOff"}, "iface": {"wlan0"}})
	if resp.StatusCode != http.StatusOK || len(e.be.applied) != 1 || !e.be.applied[0].Disabled {
		t.Fatalf("wifi off: %d %+v", resp.StatusCode, e.be.applied)
	}
	// Confirming fails: the login still succeeds and says so.
	e.be.confirmErr = errors.New("disk full")
	resp = e.login(t, "central-secret")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/network?err=confirm" {
		t.Fatalf("failed confirmation: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, body = e.get(t, "/network?err=confirm"); !strings.Contains(body, e.en(t, "network.confirmFailed")) {
		t.Fatal("no message about the failed confirmation")
	}
}

// The login waited for a change that then did not come up: no "confirmation failed", the
// error of the change is shown instead.
func TestNetworkNothingToConfirm(t *testing.T) {
	e := netEnv(t)
	e.be.net.Pending = &netcfg.Pending{Interfaces: []string{"wlan0"}}
	e.be.confirmErr = ErrNothingToConfirm
	e.be.net.LastError = errors.New("nmcli: activation failed")
	e.client.Jar, _ = cookiejar.New(nil)
	if resp := e.login(t, "central-secret"); resp.Header.Get("Location") != "/network" {
		t.Fatalf("login: %s", resp.Header.Get("Location"))
	}
}

// A login while the change is still being applied (a Wi-Fi network that refuses the
// password takes up to about two minutes) does not hang: it leads to the network page, which
// follows the change and then offers to save it.
func TestLoginWhileApplying(t *testing.T) {
	e := netEnv(t)
	defer func(d time.Duration) { loginNetworkWait = d }(loginNetworkWait)
	loginNetworkWait = 50 * time.Millisecond
	e.be.net.Applying = true
	e.client.Jar, _ = cookiejar.New(nil)
	start := time.Now()
	resp := e.login(t, "central-secret")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/network" || e.be.confirmed != 0 {
		t.Fatalf("login while applying: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the login waited too long")
	}
	resp, body := e.get(t, "/network")
	if resp.Header.Get("Refresh") == "" || !strings.Contains(body, e.en(t, "network.applying.text")) ||
		strings.Contains(body, e.en(t, "network.pending.keep")) {
		t.Fatalf("the page must follow the change: refresh %q", resp.Header.Get("Refresh"))
	}
	// The change came up and waits for its confirmation: "Save now".
	e.be.net.Applying = false
	e.be.net.Pending = &netcfg.Pending{Interfaces: []string{"wlan0"}}
	resp, body = e.get(t, "/network")
	if resp.Header.Get("Refresh") != "" || !strings.Contains(body, e.en(t, "network.pending.keep")) {
		t.Fatal("no button to save the change")
	}
	resp, _ = e.post(t, "/network", url.Values{"csrf": {csrf(t, body)}, "do": {"confirm"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/network?ok=networkConfirmed" || e.be.confirmed != 1 {
		t.Fatalf("save now: %d %s confirmed=%d", resp.StatusCode, resp.Header.Get("Location"), e.be.confirmed)
	}
	// Nothing left to save (it did not come up): back to the page, which shows why.
	e.be.net.Pending = &netcfg.Pending{Interfaces: []string{"wlan0"}}
	e.be.confirmErr = ErrNothingToConfirm
	e.be.net.LastError = &netcfg.InputError{Field: "passphrase", Reason: "rejected"}
	_, body = e.get(t, "/network")
	resp, _ = e.post(t, "/network", url.Values{"csrf": {csrf(t, body)}, "do": {"confirm"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/network" {
		t.Fatalf("nothing to save: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	e.be.net.Pending = nil
	if _, body = e.get(t, "/network"); !strings.Contains(body, e.en(t, "network.error.passphrase_rejected")) {
		t.Fatal("a refused password must be said in plain words")
	}
}

func TestNetworkFailedChange(t *testing.T) {
	e := netEnv(t)
	// The change did not come up: the helper went back to the previous settings.
	e.be.net.LastError = errors.New("ifup enp2s0: exit status 1")
	e.client.Jar, _ = cookiejar.New(nil)
	if resp := e.login(t, "central-secret"); resp.Header.Get("Location") != "/network" || e.be.confirmed != 0 {
		t.Fatalf("login after a failed change: %s", resp.Header.Get("Location"))
	}
	_, body := e.get(t, "/network")
	if !strings.Contains(body, "ifup enp2s0: exit status 1") || !strings.Contains(body, e.en(t, "network.reverted")) {
		t.Fatalf("failure not shown: %s", body)
	}
}

// A screen that does not report itself to the player (many TVs on a Raspberry Pi): the
// display page says that no screen was detected, not that display control is missing.
func TestDisplayNoScreen(t *testing.T) {
	e := netEnv(t)
	e.be.outputs = []display.Output{{Name: "HDMI-1"}, {Name: "HDMI-2"}}
	_, body := e.get(t, "/display")
	if !strings.Contains(body, e.en(t, "display.noScreen")) || strings.Contains(body, e.en(t, "display.notAvailable")) {
		t.Fatal("no hint about the missing screen")
	}
}

func TestNetworkErrorTexts(t *testing.T) {
	bundle, err := locales.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	e := &env{srv: &Server{I18n: bundle}}
	for _, c := range []struct {
		err  error
		want string
	}{
		{&netcfg.InputError{Field: "gateway", Reason: "outside_network"}, "network.error.gateway_outside_network"},
		{&netcfg.InputError{Field: "dns", Reason: "invalid"}, "network.error.dns"},
		{&netcfg.InputError{Field: "backend", Reason: "unsupported"}, "network.error.backend_unsupported"},
		{&netcfg.InputError{Field: "passphrase", Reason: "rejected"}, "network.error.passphrase_rejected"},
	} {
		if got := e.srv.networkError("en", c.err); got != e.srv.t("en", c.want) {
			t.Errorf("%v: %q", c.err, got)
		}
	}
	// Without a text of its own the reason is shown in the generic one.
	got := e.srv.networkError("en", &netcfg.InputError{Field: "package", Reason: "invalid"})
	if got != e.srv.t("en", "network.error.generic", "error", "package: invalid") {
		t.Errorf("generic: %q", got)
	}
	if got := e.srv.networkError("en", context.DeadlineExceeded); !strings.Contains(got, "deadline") {
		t.Errorf("plain error: %q", got)
	}
}
