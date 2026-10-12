package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"opensignage/internal/server/config"
)

type client struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
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
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if tok, ok := out["csrf"].(string); ok {
		c.csrf = tok
	}
	return res.StatusCode, out
}

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	cfg := &config.Config{DataDir: t.TempDir(), HTTPPort: 80, HTTPSPort: 443, DevicePort: 7443, DiscoveryPort: 7444}
	srv, err := New(context.Background(), cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(srv.WebMux)
	ts.StartTLS()
	t.Cleanup(func() { ts.Close(); _ = srv.App.DB.Close() })
	return srv, ts
}

func newClient(t *testing.T, ts *httptest.Server) *client {
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Transport: ts.Client().Transport, Jar: jar} // ts.Client() is shared
	return &client{t: t, base: ts.URL, http: hc}
}

func TestSetupLoginUsersLastAdmin(t *testing.T) {
	_, ts := newTestServer(t)
	c := newClient(t, ts)

	if code, out := c.do("GET", "/api/v1/setup/status", nil); code != 200 || out["needsSetup"] != true {
		t.Fatalf("status %d %v", code, out)
	}
	if code, _ := c.do("POST", "/api/v1/setup", map[string]any{"username": "admin", "displayName": "Admin",
		"password": "secret123", "passwordConfirm": "secret12", "timezone": "Europe/Berlin"}); code != 422 {
		t.Fatalf("mismatch must fail: %d", code)
	}
	if code, out := c.do("POST", "/api/v1/setup", map[string]any{"username": "admin", "displayName": "Admin",
		"password": "secret123", "passwordConfirm": "secret123", "timezone": "Europe/Berlin"}); code != 200 {
		t.Fatalf("setup %d %v", code, out)
	}
	other := newClient(t, ts)
	if code, _ := other.do("POST", "/api/v1/setup", map[string]any{"username": "evil", "displayName": "E",
		"password": "secret123", "passwordConfirm": "secret123"}); code != 409 {
		t.Fatalf("second setup must be rejected, got %d", code)
	}
	if code, out := c.do("GET", "/api/v1/auth/session", nil); code != 200 || out["user"] == nil {
		t.Fatalf("session %d %v", code, out)
	}
	// CSRF required
	saved := c.csrf
	c.csrf = ""
	if code, _ := c.do("POST", "/api/v1/users", map[string]any{}); code != 403 {
		t.Fatalf("csrf missing must be 403, got %d", code)
	}
	c.csrf = saved

	code, out := c.do("POST", "/api/v1/users", map[string]any{"username": "bar", "displayName": "Bar",
		"role": "user", "password": "secret123", "passwordConfirm": "secret123", "active": true, "zoneIds": []string{}})
	if code != 200 {
		t.Fatalf("create user %d %v", code, out)
	}
	barID := out["id"].(string)

	// user role cannot list users
	u := newClient(t, ts)
	if code, _ := u.do("POST", "/api/v1/auth/login", map[string]any{"username": "bar", "password": "secret123"}); code != 200 {
		t.Fatalf("login %d", code)
	}
	if code, _ := u.do("GET", "/api/v1/users", nil); code != 403 {
		t.Fatalf("user must not list users, got %d", code)
	}

	// find admin id
	_, list := c.do("GET", "/api/v1/users", nil)
	var adminID string
	for _, x := range list["users"].([]any) {
		m := x.(map[string]any)
		if m["username"] == "admin" {
			adminID = m["id"].(string)
		}
	}
	// last admin guards
	if code, out := c.do("PATCH", "/api/v1/users/"+adminID, map[string]any{"role": "operator"}); code != 409 || !strings.Contains(toJSON(out), "last_admin") {
		t.Fatalf("demote last admin: %d %v", code, out)
	}
	if code, _ := c.do("PATCH", "/api/v1/users/"+adminID, map[string]any{"active": false}); code != 409 {
		t.Fatalf("deactivate last admin: %d", code)
	}
	if code, _ := c.do("DELETE", "/api/v1/users/"+adminID, nil); code != 409 {
		t.Fatalf("delete last admin: %d", code)
	}
	// audit log readable (checked before the concurrent demotion: if the logged-in admin
	// wins that race it is an operator afterwards and may no longer read the log)
	if code, out := c.do("GET", "/api/v1/audit", nil); code != 200 || len(out["entries"].([]any)) == 0 {
		t.Fatalf("audit %d", code)
	}
	// concurrent: promote bar, then demote both concurrently → exactly one succeeds
	if code, _ := c.do("PATCH", "/api/v1/users/"+barID, map[string]any{"role": "admin"}); code != 200 {
		t.Fatal("promote")
	}
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, id := range []string{adminID, barID} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			codes[i], _ = c.do("PATCH", "/api/v1/users/"+id, map[string]any{"role": "operator"})
		}(i, id)
	}
	wg.Wait()
	// One demotion wins. The other fails with 409 (last admin) or – if the admin demoted
	// themselves first and lost the admin role – with 403.
	refused := func(c int) bool { return c == 409 || c == 403 }
	if !(codes[0] == 200 && refused(codes[1]) || refused(codes[0]) && codes[1] == 200) {
		t.Fatalf("concurrent demotion must leave one admin: %v", codes)
	}
}

func TestLoginThrottle(t *testing.T) {
	_, ts := newTestServer(t)
	c := newClient(t, ts)
	c.do("POST", "/api/v1/setup", map[string]any{"username": "admin", "displayName": "A", "password": "secret123", "passwordConfirm": "secret123"})
	x := newClient(t, ts)
	var last int
	for i := 0; i < 7; i++ {
		last, _ = x.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "wrong-pass"})
	}
	if last != 429 {
		t.Fatalf("expected lockout, got %d", last)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The update source is validated as strictly as the config file value: it becomes part
// of GitHub API URLs.
func TestSettingsUpdateRepoValidation(t *testing.T) {
	_, ts := newTestServer(t)
	c := newClient(t, ts)
	c.do("POST", "/api/v1/setup", map[string]any{"username": "admin", "displayName": "A", "password": "secret123", "passwordConfirm": "secret123", "timezone": "UTC"})
	for _, bad := range []string{"owner/repo?per_page=1", "owner/repo#x", "owner/..", "owner/<x>"} {
		if code, _ := c.do("PATCH", "/api/v1/settings", map[string]any{"updateRepo": bad}); code != 422 {
			t.Errorf("%q: %d", bad, code)
		}
	}
	if code, out := c.do("PATCH", "/api/v1/settings", map[string]any{"updateRepo": "owner/repo.name"}); code != 200 || out["updateRepo"] != "owner/repo.name" {
		t.Fatalf("valid repo: %d %v", code, out)
	}
}

// Concurrent settings changes of different fields must both survive (no lost update).
func TestSettingsNoLostUpdate(t *testing.T) {
	_, ts := newTestServer(t)
	c := newClient(t, ts)
	c.do("POST", "/api/v1/setup", map[string]any{"username": "admin", "displayName": "A", "password": "secret123", "passwordConfirm": "secret123", "timezone": "UTC"})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		c.do("POST", "/api/v1/settings/client-password", map[string]any{"password": "client123", "passwordConfirm": "client123"})
	}()
	go func() {
		defer wg.Done()
		c.do("PATCH", "/api/v1/settings", map[string]any{"timezone": "Europe/Berlin"})
	}()
	wg.Wait()
	_, out := c.do("GET", "/api/v1/settings", nil)
	if out["timezone"] != "Europe/Berlin" || out["clientPasswordSet"] != true {
		t.Fatalf("lost update: %v", out)
	}
}

// The first administrator follows the server language: changing it in Settings → General
// changes the interface of that account too (the setup used to pin its own language).
func TestSetupAdminFollowsServerLanguage(t *testing.T) {
	_, ts := newTestServer(t)
	c := newClient(t, ts)
	if code, out := c.do("POST", "/api/v1/setup", map[string]any{"username": "admin", "displayName": "Admin",
		"password": "secret123", "passwordConfirm": "secret123", "language": "de", "timezone": "Europe/Berlin"}); code != 200 {
		t.Fatalf("setup %d %v", code, out)
	}
	lang := func() (user, server string) {
		_, out := c.do("GET", "/api/v1/auth/session", nil)
		return out["user"].(map[string]any)["language"].(string), out["server"].(map[string]any)["language"].(string)
	}
	if u, s := lang(); u != "" || s != "de" {
		t.Fatalf("after setup: account %q, server %q", u, s)
	}
	if code, out := c.do("PATCH", "/api/v1/settings", map[string]any{"language": "en"}); code != 200 {
		t.Fatalf("settings %d %v", code, out)
	}
	if u, s := lang(); u != "" || s != "en" {
		t.Fatalf("after the server language change: account %q, server %q", u, s)
	}
}
