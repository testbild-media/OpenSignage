package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"opensignage/internal/server/db"
	"opensignage/internal/server/system"
)

func TestBackupDownloadAndRestoreStaging(t *testing.T) {
	srv, ts := newTestServer(t)
	c := newClient(t, ts)
	c.do("POST", "/api/v1/setup", map[string]any{"username": "admin", "displayName": "A", "password": "secret123", "passwordConfirm": "secret123"})

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/backup/database", nil)
	res, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !bytes.HasPrefix(snapshot, []byte("SQLite format 3")) {
		t.Fatalf("database download: %d %q", res.StatusCode, snapshot[:min(16, len(snapshot))])
	}

	// Garbage is rejected, the snapshot is staged.
	for _, body := range [][]byte{[]byte("not a backup"), snapshot} {
		req, _ = http.NewRequest("POST", ts.URL+"/api/v1/backup/restore", bytes.NewReader(body))
		req.Header.Set("X-CSRF-Token", c.csrf)
		res, err = c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		want := 200
		if len(body) < 100 {
			want = 422
		}
		if res.StatusCode != want {
			t.Fatalf("restore of %d bytes: got %d want %d", len(body), res.StatusCode, want)
		}
	}
	if _, err := os.Stat(filepath.Join(srv.App.Cfg.DataDir, "restore", "staged", "pending.db")); err != nil {
		t.Fatal("restore not staged")
	}

	// Media ZIP streams even when empty.
	req, _ = http.NewRequest("GET", ts.URL+"/api/v1/backup/media.zip", nil)
	res, err = c.http.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("media zip: %v %d", err, res.StatusCode)
	}
	res.Body.Close()
}

func TestApplyPendingRestore(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	live, err := db.Open(ctx, filepath.Join(dir, "db", "opensignage.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := live.W.Exec(`INSERT INTO settings(key, value, updated_at) VALUES ('marker', '"old"', 'x')`); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "restore", "staged", "pending.db")
	if err := live.Backup(ctx, staged); err != nil {
		t.Fatal(err)
	}
	if _, err := live.W.Exec(`UPDATE settings SET value = '"new"' WHERE key = 'marker'`); err != nil {
		t.Fatal(err)
	}
	live.Close()
	if res, err := system.ApplyPendingRestore(dir); err != nil || !res.Applied {
		t.Fatal(err)
	}
	re, err := db.Open(ctx, filepath.Join(dir, "db", "opensignage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	var v string
	if err := re.R.QueryRow(`SELECT value FROM settings WHERE key = 'marker'`).Scan(&v); err != nil || v != `"old"` {
		t.Fatalf("restored value %q %v", v, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "restore")); !os.IsNotExist(err) {
		t.Fatal("staging dir must be removed")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "backups", "pre-restore-*", "opensignage.db"))
	if len(matches) != 1 {
		t.Fatal("previous database must be kept")
	}
}
