package webgui

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"opensignage/internal/agent/display"
	"opensignage/internal/agent/netcfg"
	"opensignage/internal/agent/state"
	"opensignage/internal/agent/wire"
	"opensignage/internal/common/passwords"
	"opensignage/locales"
)

type fakeBackend struct {
	githubStarted int
	central       string
	display       wire.Display
	repaired      int
	address       string
	regen         bool
	regenSource   string
	net           NetworkView
	applied       []netcfg.Config
	confirmed     int
	confirmErr    error
	scan          []netcfg.AP
	outputs       []display.Output // nil: one fake 1920×1080 screen
}

func (f *fakeBackend) NetworkView(context.Context) NetworkView { return f.net }
func (f *fakeBackend) ScanWifi(context.Context, string) ([]netcfg.AP, error) {
	return f.scan, nil
}
func (f *fakeBackend) ApplyNetwork(c netcfg.Config) error {
	f.applied = append(f.applied, c)
	f.net.Pending = &netcfg.Pending{Interfaces: []string{c.Interface}}
	return nil
}
func (f *fakeBackend) NetworkPending() bool  { return f.net.Pending != nil || f.net.Applying }
func (f *fakeBackend) NetworkApplying() bool { return f.net.Applying }
func (f *fakeBackend) NetworkFailed() bool   { return f.net.LastError != nil }
func (f *fakeBackend) ConfirmNetwork(ctx context.Context) error {
	if f.net.Applying {
		<-ctx.Done() // the change takes longer than the login waits
		return ctx.Err()
	}
	if f.confirmErr != nil {
		return f.confirmErr
	}
	f.confirmed++
	f.net.Pending = nil
	return nil
}
func (f *fakeBackend) InstallFirmware(context.Context, string) error { return nil }

func (f *fakeBackend) StatusView(context.Context) Status {
	return Status{Version: "1.2.3", IPs: []string{"10.0.0.2"}, LinkPhase: "connected", Mode: "playing",
		Source: wire.ActiveSource{Kind: "playlist"}, Sync: wire.SyncInfo{State: "active"}, Player: "ok"}
}
func (f *fakeBackend) ServerAddressSettings() (string, bool, string) { return f.address, true, "" }
func (f *fakeBackend) SetServerAddress(a string) error               { f.address = a; return nil }
func (f *fakeBackend) Repair() error                                 { f.repaired++; return nil }
func (f *fakeBackend) FactoryReset() error                           { return nil }
func (f *fakeBackend) DisplayOutputs(ctx context.Context) ([]display.Output, error) {
	if f.outputs != nil {
		return f.outputs, nil
	}
	return display.NewFake().Outputs(ctx)
}
func (f *fakeBackend) DisplaySetting() (wire.Display, string) { return f.display, "" }
func (f *fakeBackend) SetDisplay(_ context.Context, d wire.Display) error {
	f.display = d
	return nil
}
func (f *fakeBackend) RestartPlayer() error { return nil }
func (f *fakeBackend) Regeneration() bool   { return f.regen }
func (f *fakeBackend) SetRegeneration(on bool, source string) {
	f.regen, f.regenSource = on, source
}
func (f *fakeBackend) RebootAllowed() error        { return nil }
func (f *fakeBackend) Reboot() error               { return nil }
func (f *fakeBackend) SetLanguage(string)          {}
func (f *fakeBackend) DeviceLanguage() string      { return "" }
func (f *fakeBackend) CentralPasswordHash() string { return f.central }
func (f *fakeBackend) LogFiles() []string          { return nil }
func (f *fakeBackend) UpdateView(context.Context) UpdateView {
	return UpdateView{Own: "1.2.3", Server: "1.3.0", HaveServer: true, Repo: "owner/repo", Phase: "failed", Error: "checksum_mismatch", Fallback: true}
}
func (f *fakeBackend) StartGitHubUpdate() error { f.githubStarted++; return nil }

type env struct {
	srv    *Server
	be     *fakeBackend
	base   string
	client *http.Client
	ln     net.Listener
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := state.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := locales.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	be := &fakeBackend{}
	s, err := New(0, dir+"/tls", st, bundle, be)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go s.Serve(ctx, ln)
	t.Cleanup(cancel)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 30 * time.Second}
	return &env{srv: s, be: be, base: "https://" + ln.Addr().String(), client: c, ln: ln}
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`)

func (e *env) get(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	resp, err := e.client.Get(e.base + path)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func (e *env) post(t *testing.T, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	resp, err := e.client.PostForm(e.base+path, form)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func csrf(t *testing.T, body string) string {
	t.Helper()
	m := csrfRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no csrf token in page: %s", body[:min(len(body), 400)])
	}
	return m[1]
}

func (e *env) login(t *testing.T, pw string) *http.Response {
	_, body := e.get(t, "/login")
	resp, _ := e.post(t, "/login", url.Values{"csrf": {csrf(t, body)}, "password": {pw}})
	return resp
}

func TestLoginForcedChangeAndCSRF(t *testing.T) {
	e := newEnv(t)
	// Not logged in → login page.
	if resp, _ := e.get(t, "/"); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("expected redirect to login, got %d", resp.StatusCode)
	}
	_, body := e.get(t, "/login")
	if !strings.Contains(body, "opensignage") {
		t.Fatal("default password hint missing while the default is active")
	}
	// Login without CSRF token is refused.
	if resp, _ := e.post(t, "/login", url.Values{"password": {DefaultPassword}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("login without csrf: %d", resp.StatusCode)
	}
	if resp := e.login(t, "wrong-password"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	loginResp := e.login(t, DefaultPassword)
	if loginResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: %d", loginResp.StatusCode)
	}
	for _, c := range loginResp.Cookies() {
		if c.Name == sessionCookie && (!c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode) {
			t.Fatalf("insecure session cookie %+v", c)
		}
	}
	// Forced change: every page redirects to /password.
	if resp, _ := e.get(t, "/"); resp.Header.Get("Location") != "/password" {
		t.Fatalf("forced change not enforced: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, body = e.get(t, "/password")
	token := csrf(t, body)
	// POST without the session CSRF token is refused.
	if resp, _ := e.post(t, "/password", url.Values{"new": {"new-secret-1"}, "repeat": {"new-secret-1"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("password change without csrf: %d", resp.StatusCode)
	}
	if resp, _ := e.post(t, "/password", url.Values{"csrf": {token}, "new": {"new-secret-1"}, "repeat": {"other"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch must fail: %d", resp.StatusCode)
	}
	if resp, _ := e.post(t, "/password", url.Values{"csrf": {token}, "new": {DefaultPassword}, "repeat": {DefaultPassword}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("default password must be refused: %d", resp.StatusCode)
	}
	if resp, _ := e.post(t, "/password", url.Values{"csrf": {token}, "new": {"new-secret-1"}, "repeat": {"new-secret-1"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("password change: %d", resp.StatusCode)
	}
	if resp, body := e.get(t, "/"); resp.StatusCode != http.StatusOK || !strings.Contains(body, "1.2.3") {
		t.Fatalf("status page after change: %d", resp.StatusCode)
	}
	wg, _ := e.srv.Store.LoadWebGUI()
	if wg.MustChange || !passwords.Verify("new-secret-1", wg.PasswordHash) {
		t.Fatal("new password not stored")
	}
	// A state-changing POST with a forged token is refused.
	if resp, _ := e.post(t, "/pairing", url.Values{"csrf": {"forged"}, "do": {"repair"}, "confirm": {"yes"}}); resp.StatusCode != http.StatusForbidden || e.be.repaired != 0 {
		t.Fatalf("forged csrf: %d", resp.StatusCode)
	}
	_, body = e.get(t, "/pairing")
	if resp, _ := e.post(t, "/pairing", url.Values{"csrf": {csrf(t, body)}, "do": {"repair"}, "confirm": {"yes"}}); resp.StatusCode != http.StatusSeeOther || e.be.repaired != 1 {
		t.Fatalf("repair: %d", resp.StatusCode)
	}
	// Display mode from the list.
	_, body = e.get(t, "/display")
	if resp, _ := e.post(t, "/display", url.Values{"csrf": {csrf(t, body)}, "kind": {"list"}, "mode": {"1280x720@60.00"}, "rotation": {"90"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("display: %d", resp.StatusCode)
	}
	if e.be.display != (wire.Display{Width: 1280, Height: 720, Refresh: 60, Rotation: 90}) {
		t.Fatalf("display %+v", e.be.display)
	}
	// Plasma regeneration: switched on and off by hand on the actions page.
	_, body = e.get(t, "/actions")
	if !strings.Contains(body, `value="regeneration_on"`) {
		t.Fatalf("actions page without regeneration: %s", body)
	}
	if resp, _ := e.post(t, "/actions", url.Values{"csrf": {csrf(t, body)}, "do": {"regeneration_on"}}); resp.StatusCode != http.StatusSeeOther || !e.be.regen || e.be.regenSource != "webgui" {
		t.Fatalf("regeneration on: %d %v %q", resp.StatusCode, e.be.regen, e.be.regenSource)
	}
	_, body = e.get(t, "/actions")
	if !strings.Contains(body, `value="regeneration_off"`) || strings.Contains(body, `value="regeneration_on"`) {
		t.Fatalf("actions page while regenerating: %s", body)
	}
	if resp, _ := e.post(t, "/actions", url.Values{"csrf": {csrf(t, body)}, "do": {"regeneration_off"}}); resp.StatusCode != http.StatusSeeOther || e.be.regen {
		t.Fatalf("regeneration off: %d %v", resp.StatusCode, e.be.regen)
	}
	// Update page with the GitHub fallback for exactly the server version.
	_, body = e.get(t, "/update")
	if !strings.Contains(body, "1.3.0") || !strings.Contains(body, "checksum_mismatch") || !strings.Contains(body, `value="github"`) {
		t.Fatalf("update page: %s", body)
	}
	if resp, _ := e.post(t, "/update", url.Values{"csrf": {csrf(t, body)}, "do": {"github"}}); resp.StatusCode != http.StatusSeeOther || e.be.githubStarted != 1 {
		t.Fatalf("github fallback: %d", resp.StatusCode)
	}
	// Every page renders.
	for _, p := range []string{"/", "/server", "/pairing", "/display", "/actions", "/logs", "/update", "/password"} {
		if resp, body := e.get(t, p); resp.StatusCode != http.StatusOK || !strings.Contains(body, "</html>") {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
}

func TestLoginThrottle(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		if resp := e.login(t, "nope-nope"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, resp.StatusCode)
		}
	}
	e.login(t, "nope-nope") // sixth failure starts the lockout
	if resp := e.login(t, DefaultPassword); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("even the right password must wait during the lockout, got %d", resp.StatusCode)
	}
}

func TestCentralPasswordDisablesLocalChange(t *testing.T) {
	e := newEnv(t)
	h, _ := passwords.Hash("central-secret")
	e.be.central = h
	if resp := e.login(t, DefaultPassword); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("local password must not work when the server manages it")
	}
	if resp := e.login(t, "central-secret"); resp.StatusCode != http.StatusSeeOther {
		t.Fatal("central password must work")
	}
	if resp, _ := e.get(t, "/"); resp.StatusCode != http.StatusOK {
		t.Fatalf("no forced change with a central password: %d", resp.StatusCode)
	}
	_, body := e.get(t, "/password")
	if strings.Contains(body, `name="new"`) {
		t.Fatal("local change must be disabled")
	}
}

func TestHTTPRedirectOnSamePort(t *testing.T) {
	e := newEnv(t)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get("http://" + e.ln.Addr().String() + "/display?x=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "https://"+e.ln.Addr().String()+"/display?x=1" {
		t.Fatalf("redirect: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	// HTTPS on the same port still works.
	if resp, _ := e.get(t, "/login"); resp.StatusCode != http.StatusOK {
		t.Fatalf("https: %d", resp.StatusCode)
	}
	if !strings.Contains(e.mustHeader(t, "/login", "Content-Security-Policy"), "script-src 'none'") {
		t.Fatal("CSP missing")
	}
}

func (e *env) mustHeader(t *testing.T, path, h string) string {
	resp, _ := e.get(t, path)
	return resp.Header.Get(h)
}
