package system

import (
	"archive/tar"
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // validate uploaded databases

	"opensignage/internal/common/atomicfile"
	"opensignage/internal/common/diskfree"
	"opensignage/internal/common/ids"
	"opensignage/internal/common/tlsutil"
	"opensignage/internal/server/core"
	"opensignage/internal/server/httpx"
)

// Restore staging: a validated database (and optionally TLS identity) waits in
// <DataDir>/restore/ until the next start, where ApplyPendingRestore swaps it in. The
// live database is never replaced under open connections.
const (
	restoreDir     = "restore"
	pendingDB      = "pending.db"
	pendingTLS     = "tls"
	maxRestoreSize = 64 << 30
)

// Restarter ends the process so systemd starts it again (controlled restart).
type Restarter interface {
	Restart(reason string)
}

func (m *Module) backupRoutes(rt *core.Router) {
	rt.Role("GET /api/v1/backup/database", core.RoleAdmin, m.downloadDB)
	rt.Role("GET /api/v1/backup/media.zip", core.RoleAdmin, m.downloadMedia)
	rt.Role("GET /api/v1/backup/full", core.RoleAdmin, m.downloadFull)
	rt.Role("POST /api/v1/backup/restore", core.RoleAdmin, m.restore)
	rt.Role("GET /api/v1/backup/restore", core.RoleAdmin, m.restoreStatus)
	rt.Role("DELETE /api/v1/backup/restore", core.RoleAdmin, m.cancelRestore)
	rt.Role("POST /api/v1/system/restart", core.RoleAdmin, m.restart)
}

func stamp() string { return time.Now().UTC().Format("20060102-150405") }

func (m *Module) audit(r *http.Request, p *core.Principal, action string, v any) {
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: action, ObjectType: "backup", New: v})
	})
	if err != nil {
		slog.Error("audit", "action", action, "err", err)
	}
}

// snapshot writes a consistent copy of the live database (WAL-safe VACUUM INTO).
func (m *Module) snapshot(ctx context.Context) (string, error) {
	tmp := filepath.Join(m.app.Cfg.DataDir, "tmp", "backup-"+ids.Token(6)+".db")
	if err := os.MkdirAll(filepath.Dir(tmp), 0o750); err != nil {
		return "", err
	}
	if err := m.app.DB.Backup(ctx, tmp); err != nil {
		_ = atomicfile.Remove(tmp)
		return "", fmt.Errorf("database snapshot: %w", err)
	}
	return tmp, nil
}

func (m *Module) downloadDB(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	tmp, err := m.snapshot(r.Context())
	if err != nil {
		return err
	}
	defer atomicfile.Remove(tmp)
	f, err := os.Open(tmp)
	if err != nil {
		return err
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="opensignage-db-%s.db"`, stamp()))
	m.audit(r, p, "backup.database", nil)
	if _, err := io.Copy(w, f); err != nil {
		slog.Warn("database download aborted", "err", err)
	}
	return nil
}

// mediaEntries lists current originals of all non-trashed assets with their folder path.
func (m *Module) mediaEntries(ctx context.Context) ([]mediaEntry, error) {
	folders := map[string][2]string{} // id → parent, name
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT id, COALESCE(parent_id, ''), name FROM media_folders WHERE trashed_at IS NULL`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, parent, name string
		if err := rows.Scan(&id, &parent, &name); err != nil {
			rows.Close()
			return nil, err
		}
		folders[id] = [2]string{parent, name}
	}
	rows.Close()
	folderPath := func(id string) string {
		var parts []string
		for i := 0; id != "" && i < 64; i++ {
			f, ok := folders[id]
			if !ok {
				break
			}
			parts = append([]string{safeName(f[1])}, parts...)
			id = f[0]
		}
		return strings.Join(parts, "/")
	}
	rows, err = m.app.DB.R.QueryContext(ctx, `SELECT COALESCE(a.folder_id, ''), a.name, a.blob_sha256, COALESCE(b.mime, '')
		FROM media_assets a LEFT JOIN blobs b ON b.sha256 = a.blob_sha256
		WHERE a.trashed_at IS NULL AND a.blob_sha256 IS NOT NULL ORDER BY a.name, a.created_at, a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mediaEntry
	used := map[string]bool{} // lower-cased: ZIP tools on Windows/macOS ignore case
	for rows.Next() {
		var folder, name, sha, mime string
		if err := rows.Scan(&folder, &name, &sha, &mime); err != nil {
			return nil, err
		}
		p := path.Join(folderPath(folder), withMediaExt(safeName(name), mime))
		if used[strings.ToLower(p)] {
			ext := path.Ext(p)
			base := strings.TrimSuffix(p, ext)
			for n := 2; used[strings.ToLower(p)]; n++ {
				p = fmt.Sprintf("%s (%d)%s", base, n, ext)
			}
		}
		used[strings.ToLower(p)] = true
		out = append(out, mediaEntry{path: p, sha: sha})
	}
	return out, rows.Err()
}

type mediaEntry struct{ path, sha string }

// mediaExt maps the storable media types (media/detect.go) to their usual extension.
var mediaExt = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp", "image/avif": ".avif",
	"image/svg+xml": ".svg", "video/mp4": ".mp4", "video/webm": ".webm", "video/quicktime": ".mov",
	"video/x-matroska": ".mkv", "video/x-msvideo": ".avi", "video/3gpp": ".3gp", "audio/mpeg": ".mp3",
	"audio/wav": ".wav", "audio/ogg": ".ogg", "audio/flac": ".flac", "audio/aac": ".aac", "audio/mp4": ".m4a",
}

// withMediaExt appends the type's extension when a (renamed) asset has no media file
// extension, so the files from the ZIP open with the right program.
func withMediaExt(name, mime string) string {
	ext, ok := mediaExt[mime]
	if !ok {
		return name
	}
	cur := strings.ToLower(path.Ext(name))
	switch cur {
	case ".jpeg", ".jpe", ".jfif", ".m4v", ".oga", ".opus", ".mpeg", ".mpg":
		return name
	}
	for _, e := range mediaExt {
		if cur == e {
			return name
		}
	}
	return name + ext
}

func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.Trim(s, ". ")
	if s == "" {
		return "_"
	}
	return s
}

func (m *Module) blobFile(sha string) string {
	if len(sha) < 2 {
		return ""
	}
	return filepath.Join(m.app.Cfg.DataDir, "blobs", "sha256", sha[:2], sha)
}

// downloadMedia streams all media as a ZIP (stored, not compressed: media is already
// compressed). Memory stays constant for libraries of any size.
func (m *Module) downloadMedia(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	entries, err := m.mediaEntries(r.Context())
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="opensignage-media-%s.zip"`, stamp()))
	m.audit(r, p, "backup.media", map[string]int{"files": len(entries)})
	zw := zip.NewWriter(w)
	for _, e := range entries {
		f, err := os.Open(m.blobFile(e.sha))
		if err != nil {
			slog.Error("media zip: missing blob", "path", e.path, "sha", e.sha, "err", err)
			continue
		}
		st, _ := f.Stat()
		h := &zip.FileHeader{Name: e.path, Method: zip.Store}
		if st != nil {
			h.Modified = st.ModTime()
		}
		dst, err := zw.CreateHeader(h)
		if err == nil {
			_, err = io.Copy(dst, f)
		}
		f.Close()
		if err != nil {
			slog.Warn("media zip aborted", "err", err)
			return nil
		}
	}
	if err := zw.Close(); err != nil {
		slog.Warn("media zip close", "err", err)
	}
	return nil
}

// downloadFull streams a tar with the database snapshot, all blobs (media, variants,
// fonts), the TLS identities (so paired players keep trusting a restored server) and
// the server id. It is the "one file" backup.
func (m *Module) downloadFull(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	tmp, err := m.snapshot(r.Context())
	if err != nil {
		return err
	}
	defer atomicfile.Remove(tmp)
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="opensignage-backup-%s.tar"`, stamp()))
	m.audit(r, p, "backup.full", nil)
	tw := tar.NewWriter(w)
	add := func(name, file string) error {
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: st.Size(), ModTime: st.ModTime(), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		return err
	}
	if err := add("database.db", tmp); err != nil {
		slog.Warn("full backup aborted", "err", err)
		return nil
	}
	data := m.app.Cfg.DataDir
	for _, f := range []string{"server-id"} {
		if err := add(f, filepath.Join(data, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("full backup", "file", f, "err", err)
		}
	}
	for _, dir := range []string{"tls", "blobs"} {
		root := filepath.Join(data, dir)
		err := filepath.WalkDir(root, func(pth string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			rel, err := filepath.Rel(data, pth)
			if err != nil {
				return err
			}
			return add(filepath.ToSlash(rel), pth)
		})
		if err != nil {
			slog.Warn("full backup aborted", "dir", dir, "err", err)
			return nil
		}
	}
	if err := tw.Close(); err != nil {
		slog.Warn("full backup close", "err", err)
	}
	return nil
}

// validateDB checks that a file is an intact OpenSignage database.
func validateDB(ctx context.Context, file string) (int, error) {
	conn, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file)+"?_pragma=query_only(1)")
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	var check string
	if err := conn.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil {
		return 0, fmt.Errorf("not a database: %w", err)
	}
	if check != "ok" {
		return 0, fmt.Errorf("integrity check failed: %s", check)
	}
	var v sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, errors.New("not an OpenSignage database")
	}
	var users int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		return 0, errors.New("not an OpenSignage database")
	}
	return int(v.Int64), nil
}

var errRestoreInvalid = func(detail string) *httpx.Error {
	return httpx.E(http.StatusUnprocessableEntity, "restore_invalid", "the file is not a valid OpenSignage backup").With("detail", detail)
}

// restoreMu serialises restore uploads (two tabs must not clobber each other).
var restoreMu sync.Mutex

func (m *Module) stagedDir() string { return filepath.Join(m.app.Cfg.DataDir, restoreDir, "staged") }
func (m *Module) incomingDir() string {
	return filepath.Join(m.app.Cfg.DataDir, restoreDir, "incoming")
}

// restore accepts a database file (.db) or a full backup (.tar) as the raw request body.
// Everything is written to restore/incoming and checked there (database integrity and
// schema, TLS key pairs, blob hashes); only a fully valid backup is moved atomically to
// restore/staged, which the next start applies. A restart during the upload or the
// validation therefore never applies an unchecked file.
func (m *Module) restore(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	if !restoreMu.TryLock() {
		return httpx.E(http.StatusConflict, "restore_busy", "another restore is being uploaded")
	}
	defer restoreMu.Unlock()
	need := uint64(1 << 30)
	if r.ContentLength > 0 {
		need = uint64(r.ContentLength)*2 + 512<<20
	}
	if free, err := diskfree.Free(m.app.Cfg.DataDir); err == nil && free < need {
		return httpx.E(http.StatusInsufficientStorage, "disk_full", "not enough free disk space for the restore").
			With("needed", need).With("free", free)
	}
	in := m.incomingDir()
	_ = os.RemoveAll(in)
	if err := os.MkdirAll(in, 0o750); err != nil {
		return err
	}
	fail := func(err error) error {
		_ = os.RemoveAll(in)
		return err
	}
	body := http.MaxBytesReader(w, r.Body, maxRestoreSize)
	upload := filepath.Join(in, "upload")
	f, err := os.Create(upload)
	if err != nil {
		return fail(err)
	}
	_, err = io.Copy(f, body)
	cerr := f.Close()
	if err != nil {
		return fail(httpx.ErrBadRequest.With("detail", "upload interrupted").Wrap(err))
	}
	if cerr != nil {
		return fail(cerr)
	}
	head := make([]byte, 16)
	hf, err := os.Open(upload)
	if err != nil {
		return fail(err)
	}
	_, _ = io.ReadFull(hf, head)
	hf.Close()
	blobs := 0
	if strings.HasPrefix(string(head), "SQLite format 3") {
		if err := atomicfile.Rename(upload, filepath.Join(in, pendingDB)); err != nil {
			return fail(err)
		}
	} else {
		blobs, err = extractFull(upload, in)
		_ = os.Remove(upload)
		if err != nil {
			return fail(errRestoreInvalid(err.Error()))
		}
	}
	ver, err := validateDB(r.Context(), filepath.Join(in, pendingDB))
	if err != nil {
		return fail(errRestoreInvalid(err.Error()))
	}
	cur, _ := m.app.DB.SchemaVersion(r.Context())
	if ver > cur {
		return fail(httpx.E(http.StatusConflict, "restore_newer", "the backup is from a newer version; update the server first").With("backup", ver).With("current", cur))
	}
	if err := validateTLS(filepath.Join(in, pendingTLS)); err != nil {
		return fail(errRestoreInvalid(err.Error()))
	}
	staged := m.stagedDir()
	_ = os.RemoveAll(staged)
	if err := atomicfile.Rename(in, staged); err != nil {
		return fail(err)
	}
	m.audit(r, p, "backup.restore_staged", map[string]any{"schema": ver, "blobs": blobs})
	return httpx.OK(w, map[string]any{"staged": true, "schemaVersion": ver, "blobs": blobs, "restartRequired": true})
}

// validTarName accepts only clean, relative, slash-separated names (also on Windows).
func validTarName(name string) (string, bool) {
	if strings.ContainsAny(name, `\:`) || strings.HasPrefix(name, "/") {
		return "", false
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// extractFull unpacks a full backup into dir: database.db, server-id, tls/*, and blobs
// (into dir/blobs, each verified against the SHA-256 in its name).
func extractFull(file, dir string) (int, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	blobs := 0
	sawDB := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return blobs, fmt.Errorf("not a backup archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name, ok := validTarName(h.Name)
		if !ok {
			return blobs, fmt.Errorf("unsafe path %q", h.Name)
		}
		var dest, wantSHA string
		switch {
		case name == "database.db":
			dest, sawDB = filepath.Join(dir, pendingDB), true
		case name == "server-id":
			dest = filepath.Join(dir, "server-id")
		case strings.HasPrefix(name, "tls/") && !strings.Contains(strings.TrimPrefix(name, "tls/"), "/"):
			dest = filepath.Join(dir, pendingTLS, strings.TrimPrefix(name, "tls/"))
		case strings.HasPrefix(name, "blobs/sha256/"):
			sha := path.Base(name)
			if !isHex64(sha) {
				return blobs, fmt.Errorf("invalid blob name %q", h.Name)
			}
			dest, wantSHA = filepath.Join(dir, "blobs", sha[:2], sha), sha
			blobs++
		default:
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return blobs, err
		}
		out, err := os.Create(dest)
		if err != nil {
			return blobs, err
		}
		hash := sha256.New()
		_, err = io.Copy(io.MultiWriter(out, hash), tr)
		cerr := out.Close()
		if err != nil {
			return blobs, err
		}
		if cerr != nil {
			return blobs, cerr
		}
		if wantSHA != "" && hex.EncodeToString(hash.Sum(nil)) != wantSHA {
			return blobs, fmt.Errorf("blob %s is corrupt", wantSHA[:12])
		}
	}
	if !sawDB {
		return blobs, errors.New("database.db missing in archive")
	}
	return blobs, nil
}

// validateTLS checks a staged tls directory (if present): the local CA and the device
// identity must be complete, matching key pairs, so a restore can never leave the server
// without certificates or silently change the identity players have pinned.
func validateTLS(dir string) error {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	for _, name := range []string{"ca", "device"} {
		if _, err := tlsutil.Load(filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")); err != nil {
			return fmt.Errorf("tls/%s: %w", name, err)
		}
	}
	return nil
}

func (m *Module) restoreStatus(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	_, err := os.Stat(filepath.Join(m.stagedDir(), pendingDB))
	return httpx.OK(w, map[string]any{"staged": err == nil})
}

func (m *Module) cancelRestore(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	restoreMu.Lock()
	defer restoreMu.Unlock()
	if err := os.RemoveAll(filepath.Join(m.app.Cfg.DataDir, restoreDir)); err != nil {
		return err
	}
	m.audit(r, p, "backup.restore_cancelled", nil)
	return httpx.NoContent(w)
}

func (m *Module) restart(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	if m.restarter == nil {
		return httpx.E(http.StatusConflict, "restart_unavailable", "restart is not available in this mode")
	}
	m.audit(r, p, "system.restart", nil)
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"restarting": true})
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
	go m.restarter.Restart("requested by " + p.Username)
	return nil
}

// RestoreResult describes an applied restore (for the audit entry written after start).
type RestoreResult struct {
	Applied  bool
	KeptIn   string
	Blobs    int
	TLS      bool
	ServerID bool
}

// ApplyPendingRestore swaps a validated, staged restore into place before the database
// is opened: verified blobs are added to the blob store (content addressed, existing
// files are never changed), TLS files and server id are replaced file by file, the
// previous database is kept with a timestamp in <DataDir>/backups/.
func ApplyPendingRestore(dataDir string) (RestoreResult, error) {
	var res RestoreResult
	_ = os.RemoveAll(filepath.Join(dataDir, restoreDir, "incoming")) // never apply unchecked data
	dir := filepath.Join(dataDir, restoreDir, "staged")
	staged := filepath.Join(dir, pendingDB)
	if _, err := os.Stat(staged); errors.Is(err, os.ErrNotExist) {
		return res, nil
	}
	live := filepath.Join(dataDir, "db", "opensignage.db")
	keep := filepath.Join(dataDir, "backups", "pre-restore-"+stamp())
	if err := os.MkdirAll(keep, 0o750); err != nil {
		return res, err
	}
	res.KeptIn = keep
	// Blobs first: if anything fails later, extra content-addressed files are harmless.
	blobRoot := filepath.Join(dir, "blobs")
	err := filepath.WalkDir(blobRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		sha := d.Name()
		if !isHex64(sha) {
			return nil
		}
		dest := filepath.Join(dataDir, "blobs", "sha256", sha[:2], sha)
		if _, err := os.Stat(dest); err == nil {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return err
		}
		res.Blobs++
		return atomicfile.Rename(p, dest)
	})
	if err != nil {
		return res, fmt.Errorf("restore: blobs: %w", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(live + suffix); err == nil {
			if err := atomicfile.Rename(live+suffix, filepath.Join(keep, "opensignage.db"+suffix)); err != nil {
				return res, fmt.Errorf("restore: keeping old database: %w", err)
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(live), 0o750); err != nil {
		return res, err
	}
	if err := atomicfile.Rename(staged, live); err != nil {
		return res, fmt.Errorf("restore: activating database: %w", err)
	}
	tlsStaged := filepath.Join(dir, pendingTLS)
	if entries, err := os.ReadDir(tlsStaged); err == nil {
		tlsLive := filepath.Join(dataDir, "tls")
		if err := os.MkdirAll(filepath.Join(keep, "tls"), 0o700); err != nil {
			return res, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			_ = atomicfile.Rename(filepath.Join(tlsLive, e.Name()), filepath.Join(keep, "tls", e.Name()))
			if err := atomicfile.Rename(filepath.Join(tlsStaged, e.Name()), filepath.Join(tlsLive, e.Name())); err != nil {
				return res, fmt.Errorf("restore: certificates: %w", err)
			}
		}
		res.TLS = true
	}
	if _, err := os.Stat(filepath.Join(dir, "server-id")); err == nil {
		_ = atomicfile.Rename(filepath.Join(dataDir, "server-id"), filepath.Join(keep, "server-id"))
		if err := atomicfile.Rename(filepath.Join(dir, "server-id"), filepath.Join(dataDir, "server-id")); err == nil {
			res.ServerID = true
		}
	}
	res.Applied = true
	slog.Info("restored database from backup", "previousKeptIn", keep, "blobs", res.Blobs, "tls", res.TLS)
	return res, os.RemoveAll(filepath.Join(dataDir, restoreDir))
}

// AfterRestore runs once the restored database is open: old sessions are invalid (they
// may have been logged out after the backup was made) and the restore is audited.
func AfterRestore(ctx context.Context, d *sql.DB, res RestoreResult) error {
	if !res.Applied {
		return nil
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return err
	}
	if err := core.Audit(ctx, tx, nil, "", core.AuditEntry{Action: "backup.restored", ObjectType: "backup",
		New: map[string]any{"previousKeptIn": res.KeptIn, "blobs": res.Blobs, "tls": res.TLS}}); err != nil {
		return err
	}
	return tx.Commit()
}

// SweepTemp deletes leftover temporary backup files (aborted downloads) older than 1 h.
func SweepTemp(dataDir string) {
	matches, _ := filepath.Glob(filepath.Join(dataDir, "tmp", "backup-*.db"))
	for _, f := range matches {
		if st, err := os.Stat(f); err == nil && time.Since(st.ModTime()) > time.Hour {
			_ = atomicfile.Remove(f)
		}
	}
}
