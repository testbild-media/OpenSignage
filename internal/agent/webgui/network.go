package webgui

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"opensignage/internal/agent/netcfg"
)

// FirmwareHint is a network firmware the kernel misses.
type FirmwareHint struct {
	File    string
	Package string
}

// NetworkView is what the network page shows.
type NetworkView struct {
	// Available: changes are possible (the root helper of this version is installed).
	Available bool
	Status    netcfg.Status
	Pending   *netcfg.Pending
	// Configured per interface: the settings made in the WebGUI (nil = the system's own).
	Configured map[string]*netcfg.Config
	Firmware   []FirmwareHint
	// Country proposed for the radio rules (current one, else from the time zone).
	Country string
	// LastError of the last change (it is applied after the page was answered); the
	// interface is back on its previous settings then.
	LastError string
}

// NetworkBackend is implemented by the agent.
type NetworkBackend interface {
	NetworkView(ctx context.Context) NetworkView
	ScanWifi(ctx context.Context, iface string) ([]netcfg.AP, error)
	// ApplyNetwork starts a change (after the answer is sent): active at once, permanent
	// once confirmed by the next login.
	ApplyNetwork(c netcfg.Config) error
	NetworkPending() bool
	// NetworkFailed: the last change did not come up (the previous settings are active);
	// reported once, so that only the next login leads to the network page.
	NetworkFailed() bool
	ConfirmNetwork(ctx context.Context) error
	InstallFirmware(ctx context.Context, pkg string) error
}

type netIface struct {
	netcfg.Interface
	State      string // connected | disconnected | nocable | blocked | down
	IPv4Text   string
	SignalPct  int
	Quality    string
	Band       string
	Channel    int
	Configured *netcfg.Config
	Pending    bool
	Form       netForm
	Scan       []netAP
	Scanned    bool
	ScanError  string
}

type netForm struct {
	Method, Address, Netmask, Gateway, DNS1, DNS2 string
	SSID, Country                                 string
	Hidden, Open, SAE, HavePass                   bool
}

type netAP struct {
	netcfg.AP
	Band      string
	Supported bool
	SecKey    string
}

type networkData struct {
	View       NetworkView
	Ethernet   []netIface
	Wifi       []netIface
	HelperNote bool
	Since      string
}

func (s *Server) networkBackend() NetworkBackend { return s.Backend }

func (s *Server) networkData(r *http.Request, scanIface string, scan []netcfg.AP, scanErr error, form *netForm, formIface string) networkData {
	nb := s.networkBackend()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	v := nb.NetworkView(ctx)
	d := networkData{View: v, HelperNote: !v.Available}
	if v.Pending != nil {
		d.Since = v.Pending.Since.Local().Format("15:04")
	}
	q := r.URL.Query()
	for _, it := range v.Status.Interfaces {
		ni := netIface{Interface: it, IPv4Text: strings.Join(it.IPv4, ", "), Configured: v.Configured[it.Name]}
		switch {
		case it.Kind == netcfg.KindWifi && it.Blocked:
			ni.State = "blocked"
		case !it.Up:
			ni.State = "down"
		case it.Kind == netcfg.KindEthernet && !it.Carrier:
			ni.State = "nocable"
		case it.Kind == netcfg.KindWifi && it.SSID == "":
			ni.State = "disconnected"
		default:
			ni.State = "connected"
		}
		if it.SignalDBM != 0 {
			ni.SignalPct, ni.Quality = netcfg.SignalPercent(it.SignalDBM), netcfg.SignalQuality(it.SignalDBM)
		}
		ni.Band, ni.Channel = netcfg.Band(it.FreqMHz), netcfg.Channel(it.FreqMHz)
		if v.Pending != nil {
			for _, p := range v.Pending.Interfaces {
				if p == it.Name {
					ni.Pending = true
				}
			}
		}
		ni.Form = prefill(it, ni.Configured, v.Country, v.Status.DNS)
		if form != nil && formIface == it.Name {
			ni.Form = *form
		}
		if it.Kind == netcfg.KindWifi && q.Get("iface") == it.Name && q.Get("ssid") != "" {
			ni.Form.SSID, ni.Form.SAE, ni.Form.Hidden = q.Get("ssid"), q.Get("sec") == netcfg.SecWPA3, false
			ni.Form.Open = q.Get("sec") == netcfg.SecOpen
			ni.Form.HavePass = ni.Configured != nil && ni.Configured.Wifi != nil && ni.Configured.Wifi.SSID == ni.Form.SSID
		}
		if scanIface == it.Name {
			ni.Scanned = true
			if scanErr != nil {
				ni.ScanError = scanErr.Error()
			}
			for _, a := range scan {
				ni.Scan = append(ni.Scan, netAP{AP: a, Band: netcfg.Band(a.FreqMHz), Supported: a.Supported(), SecKey: strings.ReplaceAll(a.Security, "/", "")})
			}
		}
		if it.Kind == netcfg.KindWifi {
			d.Wifi = append(d.Wifi, ni)
		} else {
			d.Ethernet = append(d.Ethernet, ni)
		}
	}
	return d
}

// prefill fills the form with the configuration made in the WebGUI, else with what is in
// effect now (dns: the DNS servers in use, suggested for a fixed address).
func prefill(it netcfg.Interface, c *netcfg.Config, country string, dns []string) netForm {
	f := netForm{Method: netcfg.MethodDHCP, Country: country}
	src := it.Static
	if c != nil {
		f.Method = c.Method
		src = c.Static
		if c.Wifi != nil {
			f.SSID, f.Hidden, f.Open, f.SAE, f.HavePass = c.Wifi.SSID, c.Wifi.Hidden, c.Wifi.Open, c.Wifi.SAE, !c.Wifi.Open
			if c.Wifi.Country != "" {
				f.Country = c.Wifi.Country
			}
		}
	} else if it.Method == netcfg.MethodStatic {
		f.Method = netcfg.MethodStatic
	}
	if f.SSID == "" {
		f.SSID = it.ConfiguredSSID
	}
	addr := ""
	if src != nil {
		addr = src.Address
		f.Gateway = src.Gateway
		if len(src.DNS) > 0 {
			f.DNS1 = src.DNS[0]
		}
		if len(src.DNS) > 1 {
			f.DNS2 = src.DNS[1]
		}
	}
	if addr == "" && len(it.IPv4) > 0 {
		addr = it.IPv4[0]
	}
	if f.Gateway == "" {
		f.Gateway = it.Gateway
	}
	if f.DNS1 == "" {
		var v4 []string
		for _, s := range dns {
			if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
				v4 = append(v4, s)
			}
		}
		if len(v4) > 0 {
			f.DNS1 = v4[0]
		}
		if len(v4) > 1 {
			f.DNS2 = v4[1]
		}
	}
	if p, err := netip.ParsePrefix(addr); err == nil {
		f.Address, f.Netmask = p.Addr().String(), netcfg.Netmask(p.Bits())
	}
	return f
}

func (s *Server) networkPage(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	p := s.authedPage(r, sess, "network")
	p.Data = s.networkData(r, "", nil, nil, nil, "")
	if r.URL.Query().Get("err") == "confirm" {
		p.Error = s.t(s.lang(r), "network.confirmFailed")
	}
	s.render(w, r, http.StatusOK, "network", p)
}

// networkFormConfig reads a configuration form (a field error names the invalid input).
func networkFormConfig(r *http.Request) (netcfg.Config, netForm, error) {
	f := netForm{
		Method:  r.PostFormValue("method"),
		Address: strings.TrimSpace(r.PostFormValue("address")), Netmask: strings.TrimSpace(r.PostFormValue("netmask")),
		Gateway: strings.TrimSpace(r.PostFormValue("gateway")), DNS1: strings.TrimSpace(r.PostFormValue("dns1")),
		DNS2: strings.TrimSpace(r.PostFormValue("dns2")), SSID: r.PostFormValue("ssid"),
		Country: strings.ToUpper(strings.TrimSpace(r.PostFormValue("country"))),
		Hidden:  r.PostFormValue("hidden") == "yes", Open: r.PostFormValue("open") == "yes", SAE: r.PostFormValue("sae") == "yes",
		HavePass: r.PostFormValue("havepass") == "yes",
	}
	c := netcfg.Config{Interface: r.PostFormValue("iface"), Kind: r.PostFormValue("kind"), Method: f.Method}
	if c.Kind == netcfg.KindWifi {
		c.Wifi = &netcfg.Wifi{SSID: f.SSID, Passphrase: r.PostFormValue("passphrase"), Open: f.Open, Hidden: f.Hidden, SAE: f.SAE && !f.Open, Country: f.Country}
	}
	if f.Method == netcfg.MethodStatic {
		bits, err := netcfg.MaskBits(f.Netmask)
		if err != nil {
			return c, f, err
		}
		if _, err := netip.ParseAddr(f.Address); err != nil {
			return c, f, &netcfg.InputError{Field: "address", Reason: "invalid"}
		}
		c.Static = &netcfg.IPv4{Address: f.Address + "/" + strconv.Itoa(bits), Gateway: f.Gateway, DNS: []string{f.DNS1, f.DNS2}}
	}
	return c, f, c.Validate()
}

func (s *Server) networkPost(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	lang := s.lang(r)
	nb := s.networkBackend()
	again := func(status int, msg string, form *netForm, iface string) {
		p := s.authedPage(r, sess, "network")
		p.Data = s.networkData(r, "", nil, nil, form, iface)
		p.Error = msg
		s.render(w, r, status, "network", p)
	}
	switch r.PostFormValue("do") {
	case "scan":
		iface := r.PostFormValue("iface")
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		aps, err := nb.ScanWifi(ctx, iface)
		cancel()
		p := s.authedPage(r, sess, "network")
		p.Data = s.networkData(r, iface, aps, err, nil, "")
		s.render(w, r, http.StatusOK, "network", p)
	case "apply", "wifiOff":
		var c netcfg.Config
		var form *netForm // the entered values shown again on an error
		var err error
		if r.PostFormValue("do") == "wifiOff" {
			c = netcfg.Config{Interface: r.PostFormValue("iface"), Kind: netcfg.KindWifi, Disabled: true}
			err = c.Validate()
		} else {
			var f netForm
			c, f, err = networkFormConfig(r)
			form = &f
		}
		if err == nil {
			err = nb.ApplyNetwork(c)
		}
		if err != nil {
			again(http.StatusUnprocessableEntity, s.networkError(lang, err), form, c.Interface)
			return
		}
		// Every session ends: the next login (at the new address) confirms the change.
		s.dropSessions("")
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		s.render(w, r, http.StatusOK, "network_applied", &page{CSRF: loginCSRF(w, r), Title: s.t(lang, "network.applied.title")})
	case "firmware":
		ctx, cancel := context.WithTimeout(r.Context(), 16*time.Minute)
		err := nb.InstallFirmware(ctx, r.PostFormValue("package"))
		cancel()
		if err != nil {
			again(http.StatusBadGateway, s.t(lang, "network.firmware.failed", "error", err.Error()), nil, "")
			return
		}
		flash(w, r, "/network", "firmwareInstalled")
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// networkError translates a failed change.
func (s *Server) networkError(lang string, err error) string {
	var fe *netcfg.InputError
	if errors.As(err, &fe) {
		key := "network.error." + fe.Field
		switch fe.Reason {
		case "network_address", "outside_network", "too_many", "required", "unsupported", "unknown":
			key += "_" + fe.Reason
		}
		if msg := s.t(lang, key); msg != "client."+key {
			return msg
		}
		return s.t(lang, "network.error.generic", "error", fe.Error())
	}
	return s.t(lang, "network.error.generic", "error", err.Error())
}

// confirmNetwork runs after a successful login: it makes a pending change permanent.
func (s *Server) confirmNetwork(r *http.Request) (confirmed bool, failure string) {
	nb := s.networkBackend()
	if !nb.NetworkPending() {
		return false, ""
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := nb.ConfirmNetwork(ctx); err != nil {
		slog.Error("confirming the network change failed", "err", err)
		return false, err.Error()
	}
	slog.Info("network change confirmed by login")
	return true, ""
}
