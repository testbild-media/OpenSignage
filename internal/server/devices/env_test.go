package devices_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"opensignage/internal/common/version"
	"opensignage/internal/contracts"
	"opensignage/internal/server/app"
	"opensignage/internal/server/config"
	"opensignage/internal/server/db"
	"opensignage/internal/server/devices"
	"opensignage/internal/server/httpx"
)

type env struct {
	t     *testing.T
	srv   *app.Server
	mod   *devices.Module
	web   *httptest.Server
	dev   *httptest.Server
	admin *client
}

func newEnv(t *testing.T, tune ...func(*devices.Module)) *env {
	t.Helper()
	cfg := &config.Config{DataDir: t.TempDir(), HTTPPort: 80, HTTPSPort: 443, DevicePort: 7443, DiscoveryPort: 7444}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv, err := app.New(context.Background(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	mod := devices.For(srv.App)
	if mod == nil {
		t.Fatal("devices module not registered")
	}
	mod.LiveDebounce = 10 * time.Millisecond
	mod.DeployDebounce = 20 * time.Millisecond
	for _, f := range tune {
		f(mod)
	}
	web := httptest.NewUnstartedServer(srv.WebMux)
	web.StartTLS()
	dev := httptest.NewUnstartedServer(httpx.Recover(srv.DeviceMux))
	dev.StartTLS()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		mod.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		dev.Close()
		web.Close()
		_ = srv.App.DB.Close()
	})
	e := &env{t: t, srv: srv, mod: mod, web: web, dev: dev}
	e.admin = e.newClient()
	if code, out := e.admin.do("POST", "/api/v1/setup", map[string]any{"username": "admin", "displayName": "Admin",
		"password": "secret123", "passwordConfirm": "secret123"}); code != 200 {
		t.Fatalf("setup: %d %v", code, out)
	}
	return e
}

func (e *env) db() *db.DB { return e.srv.App.DB }

// ---------------------------------------------------------------- admin client

type client struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

func (e *env) newClient() *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: e.t, base: e.web.URL, http: &http.Client{Transport: e.web.Client().Transport, Jar: jar}}
}

func (c *client) raw(method, path string, body any) (int, []byte, http.Header) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b, res.Header
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	code, b, _ := c.raw(method, path, body)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	if tok, ok := out["csrf"].(string); ok {
		c.csrf = tok
	}
	return code, out
}

func errCode(out map[string]any) string {
	if e, ok := out["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

// ---------------------------------------------------------------- device REST

func (e *env) devDo(method, path, token string, body io.Reader, hdr map[string]string) (int, []byte, http.Header) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.dev.URL+path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := e.dev.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b, res.Header
}

func (e *env) devJSON(method, path, token string, body any) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	code, b, _ := e.devDo(method, path, token, rd, map[string]string{"Content-Type": "application/json"})
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return code, out
}

func (e *env) requestPairing(hw string) contracts.PairResponse {
	e.t.Helper()
	code, out := e.devJSON("POST", "/device/v1/pair/request", "", map[string]any{"hardwareId": hw, "hostname": "host-" + hw,
		"version": version.Version, "os": "linux", "arch": "arm64", "ips": []string{"10.0.0.7"}, "screen": map[string]int{"width": 1920, "height": 1080},
		"futureField": true})
	if code != 200 {
		e.t.Fatalf("pair request: %d %v", code, out)
	}
	return contracts.PairResponse{PairingID: out["pairingId"].(string), PIN: out["pin"].(string), Secret: out["secret"].(string),
		ExpiresAt: out["expiresAt"].(string)}
}

func (e *env) pairStatus(id, secret string) (int, contracts.PairStatus) {
	e.t.Helper()
	code, b, _ := e.devDo("GET", "/device/v1/pair/status?id="+id+"&secret="+secret, "", nil, nil)
	var st contracts.PairStatus
	_ = json.Unmarshal(b, &st)
	return code, st
}

func (e *env) claim(pin, name, zone string) (int, map[string]any) {
	e.t.Helper()
	body := map[string]any{"pin": pin, "name": name}
	if zone != "" {
		body["zoneId"] = zone
	}
	return e.admin.do("POST", "/api/v1/devices/claim", body)
}

// pair runs the complete pairing flow and returns device id and token.
func (e *env) pair(hw, zone string) (string, string) {
	e.t.Helper()
	pr := e.requestPairing(hw)
	code, out := e.claim(pr.PIN, "Screen "+hw, zone)
	if code != 200 {
		e.t.Fatalf("claim: %d %v", code, out)
	}
	code, st := e.pairStatus(pr.PairingID, pr.Secret)
	if code != 200 || st.State != "claimed" || st.Token == "" || st.DeviceID != out["id"] {
		e.t.Fatalf("pair status: %d %+v", code, st)
	}
	return st.DeviceID, st.Token
}

// ---------------------------------------------------------------- simulated agent

type agent struct {
	t      *testing.T
	ws     *websocket.Conn
	msgs   chan contracts.LinkMessage
	done   chan struct{}
	status websocket.StatusCode
}

func hello(hw string) contracts.Hello {
	return contracts.Hello{Version: version.Version, Protocol: version.Protocol, HardwareID: hw, IPs: []string{"10.0.0.7"},
		ActiveSource: contracts.ActiveSource{Kind: "cleared"}}
}

func (e *env) dial(token string) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hdr := http.Header{}
	if token != "" {
		hdr.Set("Authorization", "Bearer "+token)
	}
	return websocket.Dial(ctx, "wss"+strings.TrimPrefix(e.dev.URL, "https")+"/device/v1/ws",
		&websocket.DialOptions{HTTPClient: e.dev.Client(), HTTPHeader: hdr})
}

// connect opens a link and sends hello (nil hello: send nothing).
func (e *env) connect(token string, h *contracts.Hello) *agent {
	e.t.Helper()
	ws, _, err := e.dial(token)
	if err != nil {
		e.t.Fatalf("dial: %v", err)
	}
	ws.SetReadLimit(16 << 20)
	a := &agent{t: e.t, ws: ws, msgs: make(chan contracts.LinkMessage, 1024), done: make(chan struct{})}
	go a.read()
	e.t.Cleanup(func() { _ = ws.CloseNow() })
	if h != nil {
		a.send(contracts.MsgHello, h)
	}
	return a
}

func (a *agent) read() {
	defer close(a.done)
	for {
		typ, data, err := a.ws.Read(context.Background())
		if err != nil {
			a.status = websocket.CloseStatus(err)
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var m contracts.LinkMessage
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		select {
		case a.msgs <- m:
		default:
		}
	}
}

func (a *agent) send(t string, p any) {
	a.t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		a.t.Fatal(err)
	}
	msg, _ := json.Marshal(contracts.LinkMessage{T: t, P: b})
	a.write(websocket.MessageText, msg)
}

func (a *agent) write(typ websocket.MessageType, b []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.ws.Write(ctx, typ, b)
}

func (a *agent) next(d time.Duration) (contracts.LinkMessage, bool) {
	select {
	case m := <-a.msgs:
		return m, true
	case <-time.After(d):
		return contracts.LinkMessage{}, false
	}
}

// expect requires exactly these message types next, in order.
func (a *agent) expect(types ...string) []contracts.LinkMessage {
	a.t.Helper()
	var out []contracts.LinkMessage
	for _, typ := range types {
		m, ok := a.next(3 * time.Second)
		if !ok {
			a.t.Fatalf("expected %q, got nothing (so far %v)", typ, typesOf(out))
		}
		if m.T != typ {
			a.t.Fatalf("expected %q, got %q (so far %v)", typ, m.T, typesOf(out))
		}
		out = append(out, m)
	}
	return out
}

// waitFor skips messages until one of type t matches pred.
func (a *agent) waitFor(t string, d time.Duration, pred func(contracts.LinkMessage) bool) contracts.LinkMessage {
	a.t.Helper()
	deadline := time.Now().Add(d)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			a.t.Fatalf("timeout waiting for %q", t)
		}
		m, ok := a.next(left)
		if !ok {
			a.t.Fatalf("timeout waiting for %q", t)
		}
		if m.T == t && (pred == nil || pred(m)) {
			return m
		}
	}
}

// quiet asserts that no message of the given types arrives within d.
func (a *agent) quiet(d time.Duration, types ...string) {
	a.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		m, ok := a.next(time.Until(deadline))
		if !ok {
			return
		}
		for _, typ := range types {
			if m.T == typ {
				a.t.Fatalf("unexpected %q: %s", m.T, m.P)
			}
		}
	}
}

func (a *agent) closed(d time.Duration) websocket.StatusCode {
	a.t.Helper()
	select {
	case <-a.done:
		return a.status
	case <-time.After(d):
		a.t.Fatal("connection was not closed")
		return 0
	}
}

func typesOf(ms []contracts.LinkMessage) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.T)
	}
	return out
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

func eventually(t *testing.T, d time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
