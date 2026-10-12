package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// userLogin logs a new client in and fails the test on any other result.
func userLogin(t *testing.T, c *client, username, password string) {
	t.Helper()
	if code, out := c.do("POST", "/api/v1/auth/login", map[string]any{"username": username, "password": password}); code != 200 {
		t.Fatalf("login %s: %d %v", username, code, out)
	}
}

// loggedIn reports whether the client's session is still valid.
func loggedIn(t *testing.T, c *client) bool {
	t.Helper()
	code, out := c.do("GET", "/api/v1/auth/session", nil)
	if code != 200 {
		t.Fatalf("session: %d %v", code, out)
	}
	return out["user"] != nil
}

// openEvents opens the admin event socket with the client's session.
func openEvents(t *testing.T, c *client) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(c.base, "https")+"/api/v1/ws", &websocket.DialOptions{HTTPClient: c.http})
	if err != nil {
		t.Fatalf("event socket: %v", err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}

// socketEnds reads until the server closes the socket (events are skipped).
func socketEnds(ws *websocket.Conn, within time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			return ctx.Err() == nil
		}
	}
}

func findUser(t *testing.T, admin *client, id string) map[string]any {
	t.Helper()
	code, out := admin.do("GET", "/api/v1/users", nil)
	if code != 200 {
		t.Fatalf("list users: %d %v", code, out)
	}
	for _, x := range out["users"].([]any) {
		if u := x.(map[string]any); u["id"] == id {
			return u
		}
	}
	return nil
}

// TestUserLifecycle covers user management for a normal user (the last-admin guards are in
// TestSetupLoginUsersLastAdmin): create, change name and role (effective on the next
// request), deactivate (login refused, sessions and event socket end), reactivate, delete
// (audit entries stay readable under the name at the time of the action).
func TestUserLifecycle(t *testing.T) {
	_, ts := newTestServer(t)
	admin := newClient(t, ts)
	if code, out := admin.do("POST", "/api/v1/setup", map[string]any{"username": "admin", "displayName": "Admin",
		"password": "secret123", "passwordConfirm": "secret123"}); code != 200 {
		t.Fatalf("setup: %d %v", code, out)
	}

	// Create: invalid input is refused, a valid user can log in.
	for _, bad := range []map[string]any{
		{"username": "maria", "displayName": "Maria", "role": "root", "password": "secret123", "passwordConfirm": "secret123", "active": true},
		{"username": "maria", "displayName": "Maria", "role": "operator", "password": "secret123", "passwordConfirm": "other123", "active": true},
		{"username": "maria", "displayName": "Maria", "role": "operator", "password": "short", "passwordConfirm": "short", "active": true},
		{"username": "", "displayName": "Maria", "role": "operator", "password": "secret123", "passwordConfirm": "secret123", "active": true},
	} {
		if code, out := admin.do("POST", "/api/v1/users", bad); code != 422 {
			t.Fatalf("invalid user %v accepted: %d %v", bad, code, out)
		}
	}
	code, out := admin.do("POST", "/api/v1/users", map[string]any{"username": "maria", "displayName": "Maria",
		"role": "operator", "password": "secret123", "passwordConfirm": "secret123", "active": true, "zoneIds": []string{}})
	if code != 200 || out["role"] != "operator" || out["active"] != true {
		t.Fatalf("create: %d %v", code, out)
	}
	id := out["id"].(string)
	if code, out := admin.do("POST", "/api/v1/users", map[string]any{"username": "MARIA", "displayName": "Twin",
		"role": "user", "password": "secret123", "passwordConfirm": "secret123", "active": true}); code != 409 || errCode(out) != "username_taken" {
		t.Fatalf("duplicate username (case-insensitive): %d %v", code, out)
	}
	m1, m2 := newClient(t, ts), newClient(t, ts)
	userLogin(t, m1, "maria", "secret123")
	userLogin(t, m2, "maria", "secret123")
	if code, _ := m1.do("GET", "/api/v1/media", nil); code != 200 {
		t.Fatalf("operator must see media: %d", code)
	}
	// An operator action that is audited under her name.
	if code, out := m1.do("POST", "/api/v1/zones", map[string]any{"name": "Maria's bar"}); code != 200 {
		t.Fatalf("operator creates zone: %d %v", code, out)
	}

	// Change name and role; the role applies to the very next request of open sessions.
	if code, out := admin.do("PATCH", "/api/v1/users/"+id, map[string]any{"displayName": "  Maria K.  ", "role": "user"}); code != 200 ||
		out["displayName"] != "Maria K." || out["role"] != "user" {
		t.Fatalf("update: %d %v", code, out)
	}
	if u := findUser(t, admin, id); u["displayName"] != "Maria K." || u["role"] != "user" || u["username"] != "maria" {
		t.Fatalf("stored user: %v", u)
	}
	if code, _ := m1.do("GET", "/api/v1/media", nil); code != 403 {
		t.Fatalf("demoted user still has operator rights: %d", code)
	}
	if _, s := m2.do("GET", "/api/v1/auth/session", nil); asMap(s["user"])["role"] != "user" || asMap(s["user"])["displayName"] != "Maria K." {
		t.Fatalf("session must show the new role and name: %v", s)
	}
	for _, bad := range []map[string]any{{"role": "root"}, {"displayName": "   "}, {"displayName": strings.Repeat("x", 101)}, {"username": "x"}} {
		if code, _ := admin.do("PATCH", "/api/v1/users/"+id, bad); code != 422 && code != 400 {
			t.Fatalf("invalid change %v accepted: %d", bad, code)
		}
	}
	if code, _ := admin.do("PATCH", "/api/v1/users/"+devFakeID, map[string]any{"displayName": "Ghost"}); code != 404 {
		t.Fatalf("unknown user: %d", code)
	}

	// Deactivate: every session and the live event socket end, login is refused.
	ws := openEvents(t, m1)
	if code, out := admin.do("PATCH", "/api/v1/users/"+id, map[string]any{"active": false}); code != 200 || out["active"] != false {
		t.Fatalf("deactivate: %d %v", code, out)
	}
	if loggedIn(t, m1) || loggedIn(t, m2) {
		t.Fatal("sessions of a deactivated user must end")
	}
	if code, _ := m2.do("GET", "/api/v1/zones", nil); code != 401 {
		t.Fatalf("API with ended session: %d", code)
	}
	if !socketEnds(ws, 3*time.Second) {
		t.Fatal("event socket of a deactivated user stays open")
	}
	m3 := newClient(t, ts)
	if code, out := m3.do("POST", "/api/v1/auth/login", map[string]any{"username": "maria", "password": "secret123"}); code != 403 || errCode(out) != "user_inactive" {
		t.Fatalf("login of deactivated user: %d %v", code, out)
	}
	if code, out := m3.do("POST", "/api/v1/auth/login", map[string]any{"username": "maria", "password": "wrong-pass"}); code != 401 || errCode(out) != "login_failed" {
		t.Fatalf("wrong password must not reveal the account state: %d %v", code, out)
	}

	// Reactivate: login works again.
	if code, _ := admin.do("PATCH", "/api/v1/users/"+id, map[string]any{"active": true}); code != 200 {
		t.Fatal("reactivate")
	}
	userLogin(t, m3, "maria", "secret123")
	ws = openEvents(t, m3)

	// Delete: the account and its sessions are gone, the audit log keeps her entries.
	if code, _ := admin.do("DELETE", "/api/v1/users/"+id, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if findUser(t, admin, id) != nil {
		t.Fatal("deleted user still listed")
	}
	if loggedIn(t, m3) {
		t.Fatal("session of a deleted user must end")
	}
	if !socketEnds(ws, 3*time.Second) {
		t.Fatal("event socket of a deleted user stays open")
	}
	if code, out := newClient(t, ts).do("POST", "/api/v1/auth/login", map[string]any{"username": "maria", "password": "secret123"}); code != 401 || errCode(out) != "login_failed" {
		t.Fatalf("login of deleted user: %d %v", code, out)
	}
	if code, _ := admin.do("DELETE", "/api/v1/users/"+id, nil); code != 404 {
		t.Fatalf("second delete: %d", code)
	}

	code, audit := admin.do("GET", "/api/v1/audit?user=maria", nil)
	if code != 200 {
		t.Fatalf("audit: %d %v", code, audit)
	}
	actions := map[string]bool{}
	for _, x := range asList(audit["entries"]) {
		e := asMap(x)
		if e["username"] != "maria" || asStr(e["userId"]) != id {
			t.Fatalf("audit entry lost its author: %v", e)
		}
		actions[asStr(e["action"])] = true
	}
	if !actions["auth.login"] || !actions["zone.created"] {
		t.Fatalf("audit entries of the deleted user: %v", actions)
	}
	code, audit = admin.do("GET", "/api/v1/audit?objectType=user", nil)
	if code != 200 {
		t.Fatal("audit by object")
	}
	about := map[string]bool{}
	for _, x := range asList(audit["entries"]) {
		if e := asMap(x); e["objectId"] == id {
			if e["objectName"] != "maria" {
				t.Fatalf("audit entry about the user lost the name: %v", e)
			}
			about[asStr(e["action"])] = true
		}
	}
	for _, a := range []string{"user.created", "user.updated", "user.deleted"} {
		if !about[a] {
			t.Fatalf("missing audit %s: %v", a, about)
		}
	}

	// The name is free again; the new account is a different user.
	code, out = admin.do("POST", "/api/v1/users", map[string]any{"username": "maria", "displayName": "Maria New",
		"role": "user", "password": "secret456", "passwordConfirm": "secret456", "active": true})
	if code != 200 || out["id"] == id {
		t.Fatalf("re-create after delete: %d %v", code, out)
	}
}

const devFakeID = "01920000-0000-7000-8000-00000000beef"
