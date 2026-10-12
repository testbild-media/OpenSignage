package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestOpenMigrateBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	d, err := Open(ctx, filepath.Join(dir, "db", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	v, err := d.SchemaVersion(ctx)
	if err != nil || v < 1 {
		t.Fatalf("schema version %d %v", v, err)
	}
	// rollback on error
	boom := errors.New("boom")
	err = d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO settings(key, value, updated_at) VALUES ('a', '1', 'x')`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	var n int
	if err := d.R.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollback failed: %d %v", n, err)
	}
	// foreign keys on
	_, err = d.W.Exec(`INSERT INTO sessions(id_hash, user_id, csrf, created_at, last_seen_at, expires_at) VALUES ('h','missing','c','x','x','x')`)
	if err == nil {
		t.Fatal("foreign keys not enforced")
	}
	if err := d.Backup(ctx, filepath.Join(dir, "backup.db")); err != nil {
		t.Fatal(err)
	}
	// reopen applies nothing new
	d2, err := Open(ctx, filepath.Join(dir, "backup.db"))
	if err != nil {
		t.Fatal(err)
	}
	d2.Close()
}

// Migration 0009: only the account the setup created, and only while nobody changed it,
// goes back to following the server language.
func TestSetupAdminLanguageMigration(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, filepath.Join(t.TempDir(), "db", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	users := []struct{ id, lang, created, updated string }{
		{"setup-admin", "de", "2026-10-11T10:00:00Z", "2026-10-11T10:00:00Z"},   // copied by the setup
		{"setup-changed", "de", "2026-10-11T10:00:00Z", "2026-10-11T11:00:00Z"}, // chose it later
		{"other", "fr", "2026-10-11T10:00:00Z", "2026-10-11T10:00:00Z"},         // not from the setup
	}
	for _, u := range users {
		if _, err := d.W.Exec(`INSERT INTO users(id, username, display_name, password_hash, role, active, language, created_at, updated_at)
			VALUES (?, ?, ?, 'x', 'admin', 1, ?, ?, ?)`, u.id, u.id, u.id, u.lang, u.created, u.updated); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"setup-admin", "setup-changed"} {
		if _, err := d.W.Exec(`INSERT INTO audit_log(at, username, action, object_type, object_id) VALUES ('x', ?, 'setup.completed', 'user', ?)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	m, err := migrationFS.ReadFile("migrations/0009_setup_admin_language.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.W.Exec(string(m)); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"setup-admin": "", "setup-changed": "de", "other": "fr"}
	for id, w := range want {
		var got string
		if err := d.R.QueryRow(`SELECT COALESCE(language, '') FROM users WHERE id = ?`, id).Scan(&got); err != nil || got != w {
			t.Errorf("%s: language %q (%v), want %q", id, got, err, w)
		}
	}
}
