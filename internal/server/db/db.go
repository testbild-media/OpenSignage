// Package db opens the SQLite database, applies migrations and offers transaction helpers.
// One writer connection serializes all writes (no SQLITE_BUSY under load); reads use a
// small pool that sees WAL snapshots.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go SQLite driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// DB bundles the writer and reader handles.
type DB struct {
	W    *sql.DB // single connection, all writes
	R    *sql.DB // read pool
	Path string
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_txlock", "immediate")
	}
	return "file:" + filepath.ToSlash(path) + "?" + q.Encode()
}

// Open opens (and creates) the database and applies pending migrations.
func Open(ctx context.Context, path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	w, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	if err := w.PingContext(ctx); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	d := &DB{W: w, Path: path}
	if err := d.migrate(ctx); err != nil {
		_ = w.Close()
		return nil, err
	}
	r, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		_ = w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(4)
	d.R = r
	return d, nil
}

// Close closes both handles.
func (d *DB) Close() error {
	errR := d.R.Close()
	errW := d.W.Close()
	return errors.Join(errR, errW)
}

// Tx runs fn inside a write transaction (BEGIN IMMEDIATE). It commits when fn returns
// nil and rolls back otherwise (including panics, which are re-raised).
func (d *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) migrate(ctx context.Context) error {
	if _, err := d.W.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	type mig struct {
		version int
		name    string
	}
	var migs []mig
	for _, e := range entries {
		name := e.Name()
		num, _, ok := strings.Cut(name, "_")
		if !ok || !strings.HasSuffix(name, ".sql") {
			continue
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return fmt.Errorf("bad migration name %s", name)
		}
		migs = append(migs, mig{v, name})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	for _, m := range migs {
		var exists int
		if err := d.W.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + m.name)
		if err != nil {
			return err
		}
		err = d.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("migration %s: %w", m.name, err)
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
				m.version, m.name, Now())
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// LatestVersion returns the highest migration embedded in this binary (the schema
// version a successfully migrated database must report).
func LatestVersion() (int, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return 0, err
	}
	latest := 0
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		if !ok || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return 0, fmt.Errorf("bad migration name %s", e.Name())
		}
		latest = max(latest, v)
	}
	return latest, nil
}

// SchemaVersion returns the highest applied migration.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	err := d.R.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	return int(v.Int64), err
}

// Backup writes a consistent copy of the live database to dest (WAL-safe).
func (d *DB) Backup(ctx context.Context, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	_ = os.Remove(dest)
	// A dedicated connection reads a WAL snapshot; the single writer keeps working, so
	// live changes (sold out, device status) are never delayed by a backup.
	conn, err := sql.Open("sqlite", "file:"+filepath.ToSlash(d.Path)+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	_, err = conn.ExecContext(ctx, `VACUUM INTO ?`, dest)
	return err
}

// Now returns the current UTC time in the storage format.
func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Bool converts to the 0/1 storage form.
func Bool(b bool) int {
	if b {
		return 1
	}
	return 0
}
