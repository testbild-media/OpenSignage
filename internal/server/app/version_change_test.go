package app

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"testing"

	"opensignage/internal/common/version"
	"opensignage/internal/server/config"
)

// After an update (another version than last time) every login session ends, so no browser
// keeps working with the WebGUI of the old version; a plain restart keeps them.
func TestSessionsEndWhenTheServerVersionChanges(t *testing.T) {
	old := version.Version
	t.Cleanup(func() { version.Version = old })
	dir := t.TempDir()
	start := func(v string) (*Server, *httptest.Server) {
		t.Helper()
		version.Version = v
		srv, err := New(context.Background(), &config.Config{DataDir: dir, HTTPPort: 80, HTTPSPort: 443, DevicePort: 7443, DiscoveryPort: 7444}, slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewUnstartedServer(srv.WebMux)
		ts.StartTLS()
		return srv, ts
	}
	stop := func(srv *Server, ts *httptest.Server) {
		ts.Close()
		_ = srv.App.DB.Close()
	}

	srv, ts := start("1.0.0")
	c := newClient(t, ts)
	if code, out := c.do("POST", "/api/v1/setup", map[string]any{"username": "admin", "displayName": "Admin",
		"password": "secret123", "passwordConfirm": "secret123", "timezone": "Europe/Berlin"}); code != 200 {
		t.Fatalf("setup %d %v", code, out)
	}
	jar := c.http.Jar
	stop(srv, ts)

	loggedIn := func(ts *httptest.Server) bool {
		c := newClient(t, ts)
		c.http.Jar = jar
		code, out := c.do("GET", "/api/v1/auth/session", nil)
		return code == 200 && out["user"] != nil
	}
	// Same version again (plain restart): still logged in. The cookie jar is keyed by host,
	// and every httptest server listens on 127.0.0.1 with another port – the jar ignores ports.
	srv, ts = start("1.0.0")
	if !loggedIn(ts) {
		t.Fatal("a restart with the same version must keep the session")
	}
	stop(srv, ts)

	// Update: the session is gone and the change is in the audit log.
	srv, ts = start("1.1.0")
	defer stop(srv, ts)
	if loggedIn(ts) {
		t.Fatal("after a version change the session must have ended")
	}
	var n int
	if err := srv.App.DB.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'server.version_changed'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit entries %d %v", n, err)
	}
}
