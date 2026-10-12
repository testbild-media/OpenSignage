package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"opensignage/internal/common/ids"
	"opensignage/internal/server/db"
)

// Session cookie and lifetimes. Bar tablets stay logged in, idle sessions expire.
const (
	CookieName      = "os_sid"
	sessionIdle     = 30 * 24 * time.Hour
	sessionAbsolute = 90 * 24 * time.Hour
	touchInterval   = 5 * time.Minute
)

// Sessions manages login sessions stored in the database (hashed ids).
type Sessions struct {
	db *db.DB
}

// NewSessions creates the session manager.
func NewSessions(d *db.DB) *Sessions { return &Sessions{db: d} }

func hashID(id string) string {
	s := sha256.Sum256([]byte(id))
	return hex.EncodeToString(s[:])
}

// Create starts a session inside tx and returns the cookie value and CSRF token.
func (s *Sessions) Create(ctx context.Context, tx *sql.Tx, userID, ip, ua string) (cookie, csrf string, err error) {
	cookie = ids.Token(32)
	csrf = ids.Token(16)
	now := time.Now().UTC()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sessions(id_hash, user_id, csrf, created_at, last_seen_at, expires_at, ip, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, hashID(cookie), userID, csrf, now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano), now.Add(sessionAbsolute).Format(time.RFC3339Nano), ip, ua)
	return cookie, csrf, err
}

// SetCookie writes the session cookie (always Secure: every WebGUI runs on HTTPS).
func SetCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: value, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionAbsolute.Seconds())})
}

// ClearCookie removes the session cookie.
func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// Lookup resolves a cookie value to an active principal (nil if invalid/expired).
func (s *Sessions) Lookup(ctx context.Context, cookie string) (*Principal, error) {
	if cookie == "" || len(cookie) > 128 {
		return nil, nil
	}
	h := hashID(cookie)
	var p Principal
	var lastSeen, expires string
	var language sql.NullString
	var active int
	err := s.db.R.QueryRowContext(ctx, `SELECT s.csrf, s.last_seen_at, s.expires_at, u.id, u.username, u.display_name, u.role, u.language, u.active
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id_hash = ?`, h).
		Scan(&p.CSRF, &lastSeen, &expires, &p.UserID, &p.Username, &p.DisplayName, &p.Role, &language, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	ls, _ := time.Parse(time.RFC3339Nano, lastSeen)
	ex, _ := time.Parse(time.RFC3339Nano, expires)
	if active == 0 || now.After(ex) || now.Sub(ls) > sessionIdle {
		_, _ = s.db.W.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, h)
		return nil, nil
	}
	p.SessionHash = h
	p.Language = language.String
	if p.Role == RoleUser {
		rows, err := s.db.R.QueryContext(ctx, `SELECT zone_id FROM user_zones WHERE user_id = ? ORDER BY zone_id`, p.UserID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var z string
			if err := rows.Scan(&z); err != nil {
				return nil, err
			}
			p.ZoneIDs = append(p.ZoneIDs, z)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if now.Sub(ls) > touchInterval {
		_, _ = s.db.W.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id_hash = ?`, now.Format(time.RFC3339Nano), h)
	}
	return &p, nil
}

// Role returns the current role of the session's user, or "" when the session no longer
// exists or the user is deactivated (long-lived connections re-check their session).
func (s *Sessions) Role(ctx context.Context, sessionHash string) (string, error) {
	var role string
	err := s.db.R.QueryRowContext(ctx, `SELECT u.role FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id_hash = ? AND u.active = 1`, sessionHash).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return role, err
}

// Delete ends one session.
func (s *Sessions) Delete(ctx context.Context, sessionHash string) error {
	_, err := s.db.W.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, sessionHash)
	return err
}

// DeleteForUser ends all sessions of a user except keepHash (inside tx).
func DeleteSessionsForUser(ctx context.Context, tx *sql.Tx, userID, keepHash string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id_hash <> ?`, userID, keepHash)
	return err
}

// lastVersionKey stores the server version that ran last (settings table, JSON string).
const lastVersionKey = "server.lastVersion"

// EndAfterVersionChange ends every login session when the server starts with another
// version than last time (update, rollback, restore of an older system). Each browser then
// loads the WebGUI of this version and logs in again – a page of the old version must not
// keep working against the new server. It returns the previous version ("" on the first
// start) and the number of ended sessions; nothing happens when the version is unchanged.
func (s *Sessions) EndAfterVersionChange(ctx context.Context, version string) (prev string, ended int64, err error) {
	tx, err := s.db.W.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, lastVersionKey).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return "", 0, err
	default:
		_ = json.Unmarshal([]byte(raw), &prev)
	}
	if prev == version && raw != "" {
		return prev, 0, nil
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM sessions`)
	if err != nil {
		return prev, 0, err
	}
	ended, _ = res.RowsAffected()
	v, _ := json.Marshal(version)
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, lastVersionKey, string(v), db.Now()); err != nil {
		return prev, 0, err
	}
	if prev != "" {
		if err := Audit(ctx, tx, nil, "", AuditEntry{Action: "server.version_changed", ObjectType: "server",
			Old: map[string]any{"version": prev}, New: map[string]any{"version": version, "sessionsEnded": ended}}); err != nil {
			return prev, 0, err
		}
	}
	return prev, ended, tx.Commit()
}

// Cleanup removes expired sessions.
func (s *Sessions) Cleanup(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-sessionIdle).Format(time.RFC3339Nano)
	_, _ = s.db.W.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ? OR last_seen_at < ?`, db.Now(), cutoff)
}

// AuditEntry describes one audit log record.
type AuditEntry struct {
	Action     string
	ObjectType string
	ObjectID   string
	ObjectName string
	ZoneID     string
	Old        any
	New        any
}

// Audit writes an audit record inside tx. p may be nil for system actions.
func Audit(ctx context.Context, tx *sql.Tx, p *Principal, ip string, e AuditEntry) error {
	enc := func(v any) sql.NullString {
		if v == nil {
			return sql.NullString{}
		}
		b, err := json.Marshal(v)
		if err != nil {
			return sql.NullString{String: `"<unserializable>"`, Valid: true}
		}
		return sql.NullString{String: string(b), Valid: true}
	}
	var uid sql.NullString
	username := "system"
	if p != nil {
		uid = sql.NullString{String: p.UserID, Valid: true}
		username = p.Username
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_log(at, user_id, username, action, object_type, object_id, object_name, zone_id, old_value, new_value, ip)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, db.Now(), uid, username, e.Action, e.ObjectType, e.ObjectID,
		e.ObjectName, e.ZoneID, enc(e.Old), enc(e.New), ip)
	return err
}
