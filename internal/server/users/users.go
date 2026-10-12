// Package users implements the setup wizard, login/logout, own password change, user
// management with the last-admin guard, and the audit log view.
package users

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"opensignage/internal/common/ids"
	"opensignage/internal/common/passwords"
	"opensignage/internal/common/ratelimit"
	"opensignage/internal/common/version"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
)

// Module is the users/auth module.
type Module struct {
	app      *core.App
	throttle *ratelimit.Throttle
}

// New creates the module.
func New(app *core.App) *Module { return &Module{app: app, throttle: ratelimit.New()} }

// User is the API representation.
type User struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"displayName"`
	Role        string   `json:"role"`
	Active      bool     `json:"active"`
	Language    string   `json:"language"`
	ZoneIDs     []string `json:"zoneIds"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

// Routes registers the HTTP routes.
func (m *Module) Routes(rt *core.Router) {
	rt.Public("GET /api/v1/setup/status", m.setupStatus)
	rt.Public("POST /api/v1/setup", m.setup)
	rt.Public("POST /api/v1/auth/login", m.login)
	rt.Public("GET /api/v1/auth/session", m.session(rt))
	rt.Role("POST /api/v1/auth/logout", core.RoleUser, m.logout)
	rt.Role("POST /api/v1/auth/password", core.RoleUser, m.changeOwnPassword)
	rt.Role("PATCH /api/v1/auth/profile", core.RoleUser, m.updateProfile)

	rt.Role("GET /api/v1/users", core.RoleAdmin, m.list)
	rt.Role("POST /api/v1/users", core.RoleAdmin, m.create)
	rt.Role("PATCH /api/v1/users/{id}", core.RoleAdmin, m.update)
	rt.Role("POST /api/v1/users/{id}/password", core.RoleAdmin, m.resetPassword)
	rt.Role("DELETE /api/v1/users/{id}", core.RoleAdmin, m.delete)

	rt.Role("GET /api/v1/audit", core.RoleAdmin, m.auditList)
}

func (m *Module) userCount(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (m *Module) setupStatus(w http.ResponseWriter, r *http.Request) error {
	n, err := m.userCount(r.Context(), m.app.DB.R)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"needsSetup": n == 0, "version": version.Version})
}

type setupReq struct {
	Username        string `json:"username"`
	DisplayName     string `json:"displayName"`
	Password        string `json:"password"`
	PasswordConfirm string `json:"passwordConfirm"`
	Language        string `json:"language"`
	Timezone        string `json:"timezone"`
	ServerName      string `json:"serverName"`
}

var errSetupDone = httpx.E(http.StatusConflict, "setup_done", "setup already completed")

func (m *Module) setup(w http.ResponseWriter, r *http.Request) error {
	var req setupReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if err := validateNewUser(req.Username, req.DisplayName, req.Password, req.PasswordConfirm); err != nil {
		return err
	}
	apply := func(settings *core.Settings) error {
		if req.Language != "" {
			settings.Language = req.Language
		}
		if req.Timezone != "" {
			settings.Timezone = req.Timezone
		}
		if strings.TrimSpace(req.ServerName) != "" {
			settings.ServerName = strings.TrimSpace(req.ServerName)
		}
		if err := settings.Validate(); err != nil {
			return httpx.Invalid(err.Error(), "invalid")
		}
		return nil
	}
	if probe := m.app.Settings.Get(); apply(&probe) != nil { // validate before the slow hash
		return apply(&probe)
	}
	var settings core.Settings
	hash, err := passwords.Hash(req.Password)
	if err != nil {
		return err
	}
	var cookie, csrf string
	userID := ids.New()
	err = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		// Checked inside the write transaction: concurrent setup attempts are serialized.
		n, err := m.userCount(r.Context(), tx)
		if err != nil {
			return err
		}
		if n > 0 {
			return errSetupDone
		}
		settings, err = core.ReadTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if err := apply(&settings); err != nil {
			return err
		}
		// No own account language: the administrator follows the server language, so a
		// later change in Settings → General reaches this account too.
		if err := insertUser(r.Context(), tx, userID, req.Username, req.DisplayName, hash, core.RoleAdmin, true, ""); err != nil {
			return err
		}
		if err := m.app.Settings.Save(r.Context(), tx, settings); err != nil {
			return err
		}
		cookie, csrf, err = m.app.Sessions.Create(r.Context(), tx, userID, httpx.ClientIP(r), r.UserAgent())
		if err != nil {
			return err
		}
		p := &core.Principal{UserID: userID, Username: req.Username}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "setup.completed",
			ObjectType: "user", ObjectID: userID, ObjectName: req.Username})
	})
	if err != nil {
		return err
	}
	m.app.Settings.Apply(settings)
	core.SetCookie(w, cookie)
	return httpx.OK(w, map[string]any{"csrf": csrf})
}

// CreateAdminCLI creates an admin from the command line (headless installs). It refuses
// when users already exist unless force is set.
func CreateAdminCLI(ctx context.Context, d *db.DB, username, password string, force bool) error {
	if err := validateNewUser(username, username, password, password); err != nil {
		return err
	}
	hash, err := passwords.Hash(password)
	if err != nil {
		return err
	}
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
			return err
		}
		if n > 0 && !force {
			return errors.New("users already exist (use --force to add another admin)")
		}
		id := ids.New()
		if err := insertUser(ctx, tx, id, username, username, hash, core.RoleAdmin, true, ""); err != nil {
			return err
		}
		return core.Audit(ctx, tx, nil, "cli", core.AuditEntry{Action: "user.created_cli", ObjectType: "user", ObjectID: id, ObjectName: username})
	})
}

// ResetPasswordCLI sets a new password for an existing user (console access = trust).
func ResetPasswordCLI(ctx context.Context, d *db.DB, username, password string) error {
	if err := passwords.CheckPolicy(password); err != nil {
		return err
	}
	hash, err := passwords.Hash(password)
	if err != nil {
		return err
	}
	return d.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, active = 1, updated_at = ?, revision = revision + 1 WHERE username = ?`, hash, db.Now(), username)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errors.New("user not found")
		}
		return core.Audit(ctx, tx, nil, "cli", core.AuditEntry{Action: "user.password_reset_cli", ObjectType: "user", ObjectName: username})
	})
}

func validateNewUser(username, displayName, pw, confirm string) error {
	username = strings.TrimSpace(username)
	if n := utf8.RuneCountInString(username); n < 2 || n > 64 || strings.ContainsAny(username, " \t\r\n/\\") {
		return httpx.Invalid("username", "invalid")
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(displayName)); n < 1 || n > 100 {
		return httpx.Invalid("displayName", "invalid")
	}
	return checkPasswordPair(pw, confirm)
}

func checkPasswordPair(pw, confirm string) error {
	if pw != confirm {
		return httpx.Invalid("passwordConfirm", "mismatch")
	}
	if err := passwords.CheckPolicy(pw); err != nil {
		return httpx.Invalid("password", err.Error()).With("min", passwords.MinLength)
	}
	return nil
}

func insertUser(ctx context.Context, tx *sql.Tx, id, username, displayName, hash, role string, active bool, lang string) error {
	now := db.Now()
	_, err := tx.ExecContext(ctx, `INSERT INTO users(id, username, display_name, password_hash, role, active, language, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)`, id, strings.TrimSpace(username), strings.TrimSpace(displayName), hash, role, db.Bool(active), lang, now, now)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return httpx.E(http.StatusConflict, "username_taken", "username already exists")
	}
	return err
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (m *Module) login(w http.ResponseWriter, r *http.Request) error {
	var req loginReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	ip := httpx.ClientIP(r)
	// Per (user, IP): a stranger cannot lock out an account by failing from another device.
	keyUser := "u:" + strings.ToLower(strings.TrimSpace(req.Username)) + "|" + ip
	keyIP := "ip:" + ip
	if d := max(m.throttle.Check(keyUser), m.throttle.Check(keyIP)); d > 0 {
		return httpx.E(http.StatusTooManyRequests, "login_locked", "too many failed attempts").
			With("seconds", int(d.Seconds())+1)
	}
	var id, hash, role string
	var active int
	err := m.app.DB.R.QueryRowContext(r.Context(), `SELECT id, password_hash, role, active FROM users WHERE username = ?`,
		strings.TrimSpace(req.Username)).Scan(&id, &hash, &role, &active)
	if errors.Is(err, sql.ErrNoRows) {
		passwords.DummyVerify()
		m.throttle.Fail(keyUser)
		m.throttle.Fail(keyIP)
		return httpx.E(http.StatusUnauthorized, "login_failed", "wrong username or password")
	}
	if err != nil {
		return err
	}
	if !passwords.Verify(req.Password, hash) {
		m.throttle.Fail(keyUser)
		m.throttle.Fail(keyIP)
		_ = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
			return core.Audit(r.Context(), tx, &core.Principal{UserID: id, Username: req.Username}, ip,
				core.AuditEntry{Action: "auth.login_failed", ObjectType: "user", ObjectID: id, ObjectName: req.Username})
		})
		return httpx.E(http.StatusUnauthorized, "login_failed", "wrong username or password")
	}
	if active == 0 {
		return httpx.E(http.StatusForbidden, "user_inactive", "this account is deactivated")
	}
	m.throttle.Success(keyUser)
	m.throttle.Success(keyIP)
	var cookie, csrf string
	err = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var err error
		cookie, csrf, err = m.app.Sessions.Create(r.Context(), tx, id, ip, r.UserAgent())
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, &core.Principal{UserID: id, Username: req.Username}, ip,
			core.AuditEntry{Action: "auth.login", ObjectType: "user", ObjectID: id, ObjectName: req.Username})
	})
	if err != nil {
		return err
	}
	core.SetCookie(w, cookie)
	return httpx.OK(w, map[string]any{"csrf": csrf})
}

func (m *Module) session(rt *core.Router) httpx.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		p, err := rt.Authenticate(r)
		if err != nil {
			return err
		}
		n, err := m.userCount(r.Context(), m.app.DB.R)
		if err != nil {
			return err
		}
		s := m.app.Settings.Get()
		out := map[string]any{
			"needsSetup": n == 0,
			"server": map[string]any{"version": version.Version, "name": s.ServerName, "language": s.Language,
				"timezone": s.Timezone, "currency": s.Currency},
		}
		if p != nil {
			out["user"] = map[string]any{"id": p.UserID, "username": p.Username, "displayName": p.DisplayName,
				"role": p.Role, "language": p.Language, "zoneIds": nonNil(p.ZoneIDs)}
			out["csrf"] = p.CSRF
		}
		return httpx.OK(w, out)
	}
}

func (m *Module) logout(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	if err := m.app.Sessions.Delete(r.Context(), p.SessionHash); err != nil {
		return err
	}
	core.ClearCookie(w)
	return httpx.NoContent(w)
}

type pwChangeReq struct {
	Current         string `json:"current"`
	Password        string `json:"password"`
	PasswordConfirm string `json:"passwordConfirm"`
}

func (m *Module) changeOwnPassword(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req pwChangeReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if err := checkPasswordPair(req.Password, req.PasswordConfirm); err != nil {
		return err
	}
	var hash string
	if err := m.app.DB.R.QueryRowContext(r.Context(), `SELECT password_hash FROM users WHERE id = ?`, p.UserID).Scan(&hash); err != nil {
		return err
	}
	if !passwords.Verify(req.Current, hash) {
		return httpx.Invalid("current", "wrong_password")
	}
	newHash, err := passwords.Hash(req.Password)
	if err != nil {
		return err
	}
	err = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), `UPDATE users SET password_hash = ?, updated_at = ?, revision = revision + 1 WHERE id = ?`, newHash, db.Now(), p.UserID); err != nil {
			return err
		}
		if err := core.DeleteSessionsForUser(r.Context(), tx, p.UserID, p.SessionHash); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "user.password_changed", ObjectType: "user", ObjectID: p.UserID, ObjectName: p.Username})
	})
	if err != nil {
		return err
	}
	return httpx.NoContent(w)
}

type profileReq struct {
	Language *string `json:"language"`
}

func (m *Module) updateProfile(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req profileReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.Language == nil {
		return httpx.NoContent(w)
	}
	if _, err := m.app.DB.W.ExecContext(r.Context(), `UPDATE users SET language = NULLIF(?, ''), updated_at = ? WHERE id = ?`, *req.Language, db.Now(), p.UserID); err != nil {
		return err
	}
	return httpx.NoContent(w)
}

func (m *Module) loadUsers(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, where string, args ...any) ([]User, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, username, display_name, role, active, COALESCE(language, ''), created_at, updated_at FROM users `+where+` ORDER BY username COLLATE NOCASE`, args...)
	if err != nil {
		return nil, err
	}
	var out []User
	for rows.Next() {
		var u User
		var active int
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &active, &u.Language, &u.CreatedAt, &u.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		u.Active = active == 1
		u.ZoneIDs = []string{}
		out = append(out, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	zr, err := q.QueryContext(ctx, `SELECT user_id, zone_id FROM user_zones ORDER BY zone_id`)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	idx := map[string]int{}
	for i, u := range out {
		idx[u.ID] = i
	}
	for zr.Next() {
		var uid, zid string
		if err := zr.Scan(&uid, &zid); err != nil {
			return nil, err
		}
		if i, ok := idx[uid]; ok {
			out[i].ZoneIDs = append(out[i].ZoneIDs, zid)
		}
	}
	return out, zr.Err()
}

func (m *Module) list(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	users, err := m.loadUsers(r.Context(), m.app.DB.R, "")
	if err != nil {
		return err
	}
	if users == nil {
		users = []User{}
	}
	return httpx.OK(w, map[string]any{"users": users})
}

type createReq struct {
	Username        string   `json:"username"`
	DisplayName     string   `json:"displayName"`
	Role            string   `json:"role"`
	Password        string   `json:"password"`
	PasswordConfirm string   `json:"passwordConfirm"`
	Active          bool     `json:"active"`
	ZoneIDs         []string `json:"zoneIds"`
	Language        string   `json:"language"`
}

func (m *Module) create(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req createReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if err := validateNewUser(req.Username, req.DisplayName, req.Password, req.PasswordConfirm); err != nil {
		return err
	}
	if !core.ValidRole(req.Role) {
		return httpx.Invalid("role", "invalid")
	}
	hash, err := passwords.Hash(req.Password)
	if err != nil {
		return err
	}
	id := ids.New()
	err = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		if err := insertUser(r.Context(), tx, id, req.Username, req.DisplayName, hash, req.Role, req.Active, req.Language); err != nil {
			return err
		}
		if err := setZones(r.Context(), tx, id, req.ZoneIDs); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "user.created", ObjectType: "user",
			ObjectID: id, ObjectName: req.Username, New: map[string]any{"role": req.Role, "active": req.Active, "zoneIds": req.ZoneIDs}})
	})
	if err != nil {
		return err
	}
	m.app.Bus.Publish(core.Event{Topic: core.TopicUsers, Kind: "created", ID: id})
	users, err := m.loadUsers(r.Context(), m.app.DB.R, "WHERE id = ?", id)
	if err != nil || len(users) == 0 {
		return httpx.ErrInternal.Wrap(err)
	}
	return httpx.OK(w, users[0])
}

func setZones(ctx context.Context, tx *sql.Tx, userID string, zoneIDs []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_zones WHERE user_id = ?`, userID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, z := range zoneIDs {
		if seen[z] {
			continue
		}
		seen[z] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_zones(user_id, zone_id) VALUES (?, ?)`, userID, z); err != nil {
			if strings.Contains(err.Error(), "FOREIGN KEY") {
				return httpx.Invalid("zoneIds", "unknown_zone")
			}
			return err
		}
	}
	return nil
}

// errLastAdmin is returned when an action would leave no active admin.
var errLastAdmin = httpx.E(http.StatusConflict, "last_admin", "at least one active admin must remain")

// ensureAdminRemains must run inside the write transaction after the change.
func ensureAdminRemains(ctx context.Context, tx *sql.Tx) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND active = 1`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return errLastAdmin
	}
	return nil
}

type updateReq struct {
	DisplayName *string   `json:"displayName"`
	Role        *string   `json:"role"`
	Active      *bool     `json:"active"`
	ZoneIDs     *[]string `json:"zoneIds"`
	Language    *string   `json:"language"`
}

func (m *Module) update(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req updateReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.Role != nil && !core.ValidRole(*req.Role) {
		return httpx.Invalid("role", "invalid")
	}
	if req.DisplayName != nil {
		if n := utf8.RuneCountInString(strings.TrimSpace(*req.DisplayName)); n < 1 || n > 100 {
			return httpx.Invalid("displayName", "invalid")
		}
	}
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		before, err := m.loadUsers(r.Context(), tx, "WHERE id = ?", id)
		if err != nil {
			return err
		}
		if len(before) == 0 {
			return httpx.ErrNotFound
		}
		old := before[0]
		set, args := []string{"updated_at = ?", "revision = revision + 1"}, []any{db.Now()}
		if req.DisplayName != nil {
			set, args = append(set, "display_name = ?"), append(args, strings.TrimSpace(*req.DisplayName))
		}
		if req.Role != nil {
			set, args = append(set, "role = ?"), append(args, *req.Role)
		}
		if req.Active != nil {
			set, args = append(set, "active = ?"), append(args, db.Bool(*req.Active))
		}
		if req.Language != nil {
			set, args = append(set, "language = NULLIF(?, '')"), append(args, *req.Language)
		}
		args = append(args, id)
		if _, err := tx.ExecContext(r.Context(), `UPDATE users SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...); err != nil {
			return err
		}
		if req.ZoneIDs != nil {
			if err := setZones(r.Context(), tx, id, *req.ZoneIDs); err != nil {
				return err
			}
		}
		if err := ensureAdminRemains(r.Context(), tx); err != nil {
			return err
		}
		if req.Active != nil && !*req.Active {
			if err := core.DeleteSessionsForUser(r.Context(), tx, id, ""); err != nil {
				return err
			}
		}
		after, err := m.loadUsers(r.Context(), tx, "WHERE id = ?", id)
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "user.updated", ObjectType: "user",
			ObjectID: id, ObjectName: old.Username, Old: old, New: after[0]})
	})
	if err != nil {
		return err
	}
	m.app.Bus.Publish(core.Event{Topic: core.TopicUsers, Kind: "updated", ID: id})
	users, err := m.loadUsers(r.Context(), m.app.DB.R, "WHERE id = ?", id)
	if err != nil || len(users) == 0 {
		return httpx.ErrNotFound
	}
	return httpx.OK(w, users[0])
}

type resetReq struct {
	Password        string `json:"password"`
	PasswordConfirm string `json:"passwordConfirm"`
}

func (m *Module) resetPassword(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req resetReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if err := checkPasswordPair(req.Password, req.PasswordConfirm); err != nil {
		return err
	}
	hash, err := passwords.Hash(req.Password)
	if err != nil {
		return err
	}
	err = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var username string
		if err := tx.QueryRowContext(r.Context(), `SELECT username FROM users WHERE id = ?`, id).Scan(&username); errors.Is(err, sql.ErrNoRows) {
			return httpx.ErrNotFound
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE users SET password_hash = ?, updated_at = ?, revision = revision + 1 WHERE id = ?`, hash, db.Now(), id); err != nil {
			return err
		}
		keep := ""
		if id == p.UserID {
			keep = p.SessionHash
		}
		if err := core.DeleteSessionsForUser(r.Context(), tx, id, keep); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "user.password_reset", ObjectType: "user", ObjectID: id, ObjectName: username})
	})
	if err != nil {
		return err
	}
	return httpx.NoContent(w)
}

func (m *Module) delete(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		before, err := m.loadUsers(r.Context(), tx, "WHERE id = ?", id)
		if err != nil {
			return err
		}
		if len(before) == 0 {
			return httpx.ErrNotFound
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM users WHERE id = ?`, id); err != nil {
			return err
		}
		if err := ensureAdminRemains(r.Context(), tx); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "user.deleted", ObjectType: "user",
			ObjectID: id, ObjectName: before[0].Username, Old: before[0]})
	})
	if err != nil {
		return err
	}
	m.app.Bus.Publish(core.Event{Topic: core.TopicUsers, Kind: "deleted", ID: id})
	return httpx.NoContent(w)
}

// AuditRecord is one audit log row.
type AuditRecord struct {
	ID         int64   `json:"id"`
	At         string  `json:"at"`
	UserID     *string `json:"userId"`
	Username   string  `json:"username"`
	Action     string  `json:"action"`
	ObjectType string  `json:"objectType"`
	ObjectID   string  `json:"objectId"`
	ObjectName string  `json:"objectName"`
	ZoneID     string  `json:"zoneId"`
	Old        *string `json:"old"`
	New        *string `json:"new"`
	IP         string  `json:"ip"`
}

func (m *Module) auditList(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	q := r.URL.Query()
	where, args := []string{"1=1"}, []any{}
	if v := q.Get("before"); v != "" {
		where, args = append(where, "id < ?"), append(args, v)
	}
	if v := q.Get("user"); v != "" {
		where, args = append(where, "username = ?"), append(args, v)
	}
	if v := q.Get("action"); v != "" {
		where, args = append(where, "action LIKE ?"), append(args, v+"%")
	}
	if v := q.Get("objectType"); v != "" {
		where, args = append(where, "object_type = ?"), append(args, v)
	}
	if v := q.Get("zone"); v != "" {
		where, args = append(where, "zone_id = ?"), append(args, v)
	}
	if v := q.Get("from"); v != "" {
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			return httpx.Invalid("from", "invalid")
		}
		where, args = append(where, "at >= ?"), append(args, v)
	}
	if v := q.Get("to"); v != "" {
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			return httpx.Invalid("to", "invalid")
		}
		where, args = append(where, "at <= ?"), append(args, v)
	}
	limit := httpx.QueryInt(r, "limit", 100, 1, 500)
	args = append(args, limit)
	rows, err := m.app.DB.R.QueryContext(r.Context(), `SELECT id, at, user_id, username, action, object_type, object_id, object_name, zone_id, old_value, new_value, ip
		FROM audit_log WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []AuditRecord{}
	for rows.Next() {
		var a AuditRecord
		if err := rows.Scan(&a.ID, &a.At, &a.UserID, &a.Username, &a.Action, &a.ObjectType, &a.ObjectID, &a.ObjectName, &a.ZoneID, &a.Old, &a.New, &a.IP); err != nil {
			return err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"entries": out})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
