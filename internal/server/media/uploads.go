package media

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"opensignage/internal/common/atomicfile"
	"opensignage/internal/common/ids"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
)

type uploadRow struct {
	ID        string
	UserID    string
	Filename  string
	Size      int64
	Received  int64
	FolderID  *string
	ReplaceID *string
	ExpiresAt string
}

// upHash keeps the running SHA-256 of an upload so completing a 20 GB file does not
// need a second read pass. It is only a cache: after a restart (or an out-of-order
// event) the hash is rebuilt from the part file.
type upHash struct {
	h   hash.Hash
	off int64
}

func (m *Module) partPath(id string) string { return m.tmpDir("uploads", id+".part") }

// upLock returns the mutex serializing the chunks of one upload. Callers must have verified
// that the upload row exists (and belongs to them) first: the map therefore holds at most
// one entry per open upload row and cannot be inflated with made-up ids. Entries leave the
// map when the upload ends (dropUploadState).
func (m *Module) upLock(id string) *sync.Mutex {
	m.upMu.Lock()
	defer m.upMu.Unlock()
	l, ok := m.upLocks[id]
	if !ok {
		l = &sync.Mutex{}
		m.upLocks[id] = l
	}
	return l
}

func (m *Module) dropUploadState(id string) {
	m.upMu.Lock()
	delete(m.upLocks, id)
	delete(m.upHashes, id)
	m.upMu.Unlock()
}

// hashFor returns a hash positioned at offset start, rebuilding it from the file if needed.
func (m *Module) hashFor(ctx context.Context, id string, start int64) (*upHash, error) {
	m.upMu.Lock()
	uh := m.upHashes[id]
	m.upMu.Unlock()
	if uh != nil && uh.off == start {
		return uh, nil
	}
	h := sha256.New()
	if start > 0 {
		f, err := os.Open(m.partPath(id))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if _, err := io.CopyN(h, &ctxReader{ctx: ctx, r: f}, start); err != nil {
			return nil, err
		}
	}
	uh = &upHash{h: h, off: start}
	m.upMu.Lock()
	if len(m.upHashes) > 512 { // bounded: evict everything, hashes are rebuilt on demand
		m.upHashes = map[string]*upHash{}
	}
	m.upHashes[id] = uh
	m.upMu.Unlock()
	return uh, nil
}

// errUploadGone means the upload row does not exist (any more).
var errUploadGone = errors.New("upload not found")

// loadUploadRow reads an upload; a missing row is errUploadGone.
func (m *Module) loadUploadRow(ctx context.Context, q queryer, id string) (*uploadRow, error) {
	var u uploadRow
	var folder, repl sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id, user_id, filename, size, received, folder_id, replace_asset_id, expires_at FROM uploads WHERE id = ?`, id).
		Scan(&u.ID, &u.UserID, &u.Filename, &u.Size, &u.Received, &folder, &repl, &u.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errUploadGone
	}
	if err != nil {
		return nil, err
	}
	u.FolderID, u.ReplaceID = nullStr(folder), nullStr(repl)
	return &u, nil
}

// loadUpload reads an upload for an API caller: unknown uploads and uploads of other users
// are both a plain 404. It never touches the lock or hash state of the upload.
func (m *Module) loadUpload(ctx context.Context, q queryer, id string, p *core.Principal) (*uploadRow, error) {
	u, err := m.loadUploadRow(ctx, q, id)
	if errors.Is(err, errUploadGone) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if p != nil && u.UserID != p.UserID && !p.Is(core.RoleAdmin) {
		return nil, httpx.ErrNotFound
	}
	return u, nil
}

type createUploadReq struct {
	Filename       string  `json:"filename"`
	Size           int64   `json:"size"`
	FolderID       *string `json:"folderId"`
	ReplaceAssetID *string `json:"replaceAssetId"`
}

// createUpload starts a resumable upload (POST /api/v1/media/uploads).
func (m *Module) createUpload(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req createUploadReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	name, ok := sanitizeFilename(req.Filename)
	if !ok {
		return httpx.Invalid("filename", "invalid")
	}
	if req.Size <= 0 {
		return httpx.Invalid("size", "invalid")
	}
	if req.Size > maxFileSize {
		return httpx.E(http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds the maximum size").With("max", int64(maxFileSize))
	}
	// Reject before any byte is stored: original + processing copies + reserve.
	if err := m.checkDisk(r.Context(), req.Size*3+diskReserve, ""); err != nil {
		return err
	}
	if req.FolderID != nil {
		if *req.FolderID == "" {
			return httpx.Invalid("folderId", "empty") // use null for the root
		}
		var n int
		if err := m.app.DB.R.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM media_folders WHERE id = ? AND trashed_at IS NULL`, *req.FolderID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return httpx.Invalid("folderId", "unknown_folder")
		}
	}
	if req.ReplaceAssetID != nil && !p.CanMedia(core.MediaManage) {
		// Replacing a file changes it everywhere: with the upload right only for own files.
		if ok, err := m.ownsAsset(r.Context(), p, *req.ReplaceAssetID); err != nil {
			return err
		} else if !ok {
			return errMediaNotOwn
		}
	}
	if req.ReplaceAssetID != nil {
		var n int
		if err := m.app.DB.R.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM media_assets WHERE id = ? AND trashed_at IS NULL`, *req.ReplaceAssetID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return httpx.Invalid("replaceAssetId", "unknown_asset")
		}
	}
	id := ids.New()
	if err := m.ensureDir(m.tmpDir("uploads")); err != nil {
		return err
	}
	f, err := os.OpenFile(m.partPath(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	_ = f.Close()
	now := time.Now().UTC()
	// the limit is checked in the same write transaction as the insert, so parallel
	// requests cannot slip past it
	err = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var open int
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM uploads WHERE user_id = ?`, p.UserID).Scan(&open); err != nil {
			return err
		}
		if open >= maxOpenUpload {
			return httpx.E(http.StatusTooManyRequests, "too_many_uploads", "too many open uploads").With("max", maxOpenUpload)
		}
		_, err := tx.ExecContext(r.Context(), `INSERT INTO uploads(id, user_id, filename, size, received, folder_id, replace_asset_id, created_at, expires_at)
			VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?)`, id, p.UserID, name, req.Size, req.FolderID, req.ReplaceAssetID,
			now.Format(time.RFC3339Nano), now.Add(uploadTTL).Format(time.RFC3339Nano))
		return err
	})
	if err != nil {
		_ = os.Remove(m.partPath(id))
		return err
	}
	return httpx.OK(w, map[string]any{"uploadId": id, "received": 0, "chunkSize": maxChunk, "filename": name})
}

// parseContentRange parses "bytes start-end/total" and the query form "bytes */total".
func parseContentRange(h string) (start, end, total int64, query bool, err error) {
	bad := errors.New("malformed Content-Range")
	rest, ok := strings.CutPrefix(strings.TrimSpace(h), "bytes ")
	if !ok {
		return 0, 0, 0, false, bad
	}
	rng, tot, ok := strings.Cut(rest, "/")
	if !ok {
		return 0, 0, 0, false, bad
	}
	if total, err = strconv.ParseInt(strings.TrimSpace(tot), 10, 64); err != nil || total <= 0 {
		return 0, 0, 0, false, bad
	}
	rng = strings.TrimSpace(rng)
	if rng == "*" {
		return 0, 0, total, true, nil
	}
	s, e, ok := strings.Cut(rng, "-")
	if !ok {
		return 0, 0, 0, false, bad
	}
	if start, err = strconv.ParseInt(s, 10, 64); err != nil || start < 0 {
		return 0, 0, 0, false, bad
	}
	if end, err = strconv.ParseInt(e, 10, 64); err != nil || end < start {
		return 0, 0, 0, false, bad
	}
	return start, end, total, false, nil
}

var errBadRange = httpx.E(http.StatusBadRequest, "bad_range", "invalid Content-Range")

// putChunk appends one chunk (PUT /api/v1/media/uploads/{id}).
func (m *Module) putChunk(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	start, end, total, query, err := parseContentRange(r.Header.Get("Content-Range"))
	if err != nil {
		return errBadRange.With("detail", err.Error())
	}
	// Existence and ownership first, without creating any lock: other users' and made-up
	// ids must not influence the state of real uploads.
	if _, err := m.loadUpload(r.Context(), m.app.DB.R, id, p); err != nil {
		return err
	}
	lock := m.upLock(id)
	lock.Lock()
	defer lock.Unlock()

	// reload under the lock: earlier chunks may have advanced the offset meanwhile
	up, err := m.loadUploadRow(r.Context(), m.app.DB.R, id)
	if errors.Is(err, errUploadGone) { // finished or aborted while we waited
		m.dropUploadState(id)
		return httpx.ErrNotFound
	}
	if err != nil { // e.g. a cancelled request: the upload itself is fine, keep its state
		return err
	}
	if total != up.Size {
		return errBadRange.With("detail", "total does not match the announced size")
	}
	if query {
		if up.Received == up.Size {
			return m.respondFinalize(w, r, up, p)
		}
		return httpx.OK(w, map[string]any{"uploadId": id, "received": up.Received, "complete": false})
	}
	if start != up.Received {
		return httpx.E(http.StatusConflict, "upload_offset", "chunk does not start at the received offset").With("received", up.Received)
	}
	n := end - start + 1
	if n > maxChunk {
		return httpx.E(http.StatusRequestEntityTooLarge, "chunk_too_large", "chunk exceeds the maximum size").With("max", int64(maxChunk))
	}
	if end >= up.Size {
		return httpx.E(http.StatusRequestedRangeNotSatisfiable, "bad_range", "chunk exceeds the announced size")
	}
	if r.ContentLength >= 0 && r.ContentLength != n {
		return errBadRange.With("detail", "Content-Length does not match the range")
	}

	f, err := os.OpenFile(m.partPath(id), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() < start { // part file lost data (e.g. restored from an old backup)
		if _, err := m.app.DB.W.ExecContext(r.Context(), `UPDATE uploads SET received = ? WHERE id = ?`, st.Size(), id); err != nil {
			return err
		}
		m.upMu.Lock()
		delete(m.upHashes, id)
		m.upMu.Unlock()
		return httpx.E(http.StatusConflict, "upload_offset", "upload data is shorter than recorded").With("received", st.Size())
	}
	uh, err := m.hashFor(r.Context(), id, start)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		m.upMu.Lock()
		delete(m.upHashes, id) // hash state no longer matches the file
		m.upMu.Unlock()
		switch {
		case isDiskFull(err):
			return diskFullErr(up.Size-start, 0)
		case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, context.Canceled):
			// the client went away mid-chunk: nothing was recorded, it can resume
			return errBadRange.With("detail", "request body ended early")
		}
		return err
	}
	if err := f.Truncate(start); err != nil { // drop leftovers of an interrupted chunk
		return fail(err)
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return fail(err)
	}
	body := http.MaxBytesReader(w, r.Body, n)
	copied, err := io.Copy(io.MultiWriter(f, uh.h), body)
	if err != nil {
		return fail(err)
	}
	if copied != n {
		_ = fail(nil)
		return errBadRange.With("detail", "request body shorter than the announced range")
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	uh.off = end + 1
	up.Received = end + 1
	exp := time.Now().UTC().Add(uploadTTL).Format(time.RFC3339Nano)
	if _, err := m.app.DB.W.ExecContext(r.Context(), `UPDATE uploads SET received = ?, expires_at = ? WHERE id = ?`, up.Received, exp, id); err != nil {
		return fail(err)
	}
	if up.Received < up.Size {
		return httpx.OK(w, map[string]any{"uploadId": id, "received": up.Received, "complete": false})
	}
	_ = f.Close()
	return m.respondFinalize(w, r, up, p)
}

func (m *Module) respondFinalize(w http.ResponseWriter, r *http.Request, up *uploadRow, p *core.Principal) error {
	a, err := m.finalizeUpload(r.Context(), up, p, httpx.ClientIP(r))
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"uploadId": up.ID, "received": up.Size, "complete": true, "asset": a})
}

// discardUpload removes the part file and the row.
func (m *Module) discardUpload(ctx context.Context, id string) {
	m.dropUploadState(id)
	if err := atomicfile.Remove(m.partPath(id)); err != nil {
		slog.Warn("media: removing part file", "upload", id, "err", err)
	}
	if _, err := m.app.DB.W.ExecContext(context.WithoutCancel(ctx), `DELETE FROM uploads WHERE id = ?`, id); err != nil {
		slog.Warn("media: deleting upload row", "upload", id, "err", err)
	}
}

func kindCompatible(assetKind string, ti TypeInfo) bool {
	if assetKind == ti.Kind {
		return true
	}
	av := func(k string) bool { return k == KindVideo || k == KindAudio }
	return ti.Ambiguous && av(assetKind) && av(ti.Kind)
}

// finalizeUpload validates the content, stores it and creates the asset or revision.
func (m *Module) finalizeUpload(ctx context.Context, up *uploadRow, p *core.Principal, ip string) (*assetView, error) {
	part := m.partPath(up.ID)
	st, err := os.Stat(part)
	if err != nil || st.Size() != up.Size {
		return nil, httpx.E(http.StatusConflict, "upload_offset", "upload data is incomplete").With("received", sizeOrZero(st))
	}
	ti := DetectType(readHead(part, 8192))
	if !ti.Allowed() {
		m.discardUpload(ctx, up.ID)
		return nil, errUnsupported(ti.Mime)
	}
	if ti.Mime == "image/svg+xml" {
		if err := m.checkSVGFile(part); err != nil {
			m.discardUpload(ctx, up.ID)
			return nil, err
		}
	}
	known := ""
	m.upMu.Lock()
	if uh := m.upHashes[up.ID]; uh != nil && uh.off == up.Size {
		known = hex.EncodeToString(uh.h.Sum(nil))
	}
	m.upMu.Unlock()
	sha, size, _, err := m.ingestKeep(ctx, part, known)
	if err != nil {
		if isDiskFull(err) {
			return nil, diskFullErr(up.Size, 0)
		}
		return nil, err
	}

	var assetID string
	var replaced bool
	var newRev int
	err = m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		now := db.Now()
		metaJSON := `{"mime":"` + ti.Mime + `"}`
		if up.ReplaceID != nil {
			a, err := loadAssetRec(ctx, tx, *up.ReplaceID)
			if errors.Is(err, sql.ErrNoRows) {
				return httpx.ErrNotFound
			}
			if err != nil {
				return err
			}
			if a.TrashedAt != nil {
				return httpx.E(http.StatusConflict, "in_trash", "the asset is in the trash")
			}
			if !kindCompatible(a.Kind, ti) {
				return httpx.E(http.StatusConflict, "kind_mismatch", "the replacement has a different media kind").With("expected", a.Kind)
			}
			assetID = a.ID
			if a.BlobSHA == sha { // identical file: nothing to do
				_, err := tx.ExecContext(ctx, `DELETE FROM uploads WHERE id = ?`, up.ID)
				return err
			}
			rev := a.Revision + 1
			if _, err := tx.ExecContext(ctx, `UPDATE media_assets SET blob_sha256 = ?, revision = ?, status = 'processing', error = '', meta = ?, updated_at = ? WHERE id = ?`,
				sha, rev, metaJSON, now, a.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO media_revisions(asset_id, revision, blob_sha256, meta, created_at) VALUES (?, ?, ?, ?, ?)`,
				a.ID, rev, sha, metaJSON, now); err != nil {
				return err
			}
			if err := enqueue(ctx, tx, a.ID, rev); err != nil {
				return err
			}
			replaced, newRev = true, rev
			if _, err := tx.ExecContext(ctx, `DELETE FROM uploads WHERE id = ?`, up.ID); err != nil {
				return err
			}
			return core.Audit(ctx, tx, p, ip, core.AuditEntry{Action: "media.replaced", ObjectType: "media", ObjectID: a.ID, ObjectName: a.Name,
				Old: map[string]any{"revision": a.Revision, "sha256": a.BlobSHA},
				New: map[string]any{"revision": rev, "sha256": sha, "size": size, "filename": up.Filename}})
		}
		folder := up.FolderID
		if folder != nil {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_folders WHERE id = ? AND trashed_at IS NULL`, *folder).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				folder = nil // the target folder disappeared meanwhile: keep the file, put it in the root
			}
		}
		assetID = ids.New()
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_assets(id, folder_id, name, kind, revision, blob_sha256, status, error, meta, created_at, updated_at, uploaded_by)
			VALUES (?, ?, ?, ?, 1, ?, 'processing', '', ?, ?, ?, NULLIF(?, ''))`, assetID, folder, up.Filename, ti.Kind, sha, metaJSON, now, now, up.UserID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_revisions(asset_id, revision, blob_sha256, meta, created_at) VALUES (?, 1, ?, ?, ?)`,
			assetID, sha, metaJSON, now); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, assetID, 1); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM uploads WHERE id = ?`, up.ID); err != nil {
			return err
		}
		return core.Audit(ctx, tx, p, ip, core.AuditEntry{Action: "media.uploaded", ObjectType: "media", ObjectID: assetID, ObjectName: up.Filename,
			New: map[string]any{"kind": ti.Kind, "size": size, "sha256": sha, "folderId": folder}})
	})
	if err != nil {
		// A definitive refusal (replace target gone, other kind, in the trash) cannot be
		// fixed by retrying: drop the upload. Anything else (e.g. a busy database) keeps
		// the part file so the client can finish with "Content-Range: bytes */total".
		var he *httpx.Error
		if errors.As(err, &he) && he.Status >= 400 && he.Status < 500 {
			m.discardUpload(ctx, up.ID)
		}
		return nil, err
	}
	if err := atomicfile.Remove(part); err != nil {
		slog.Warn("media: removing finished part file", "upload", up.ID, "err", err)
	}
	m.dropUploadState(up.ID)
	m.kick()
	if replaced {
		m.cancelObsoleteJob(assetID, newRev) // its result would be thrown away
		m.publishMedia("replaced", assetID, nil)
		m.publishContent("media.replaced", assetID)
	} else {
		m.publishMedia("created", assetID, nil)
	}
	return m.assetByID(ctx, assetID)
}

func sizeOrZero(st os.FileInfo) int64 {
	if st == nil {
		return 0
	}
	return st.Size()
}

// checkSVGFile validates an SVG on disk.
func (m *Module) checkSVGFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := readSVG(f)
	if errors.Is(err, errSVGTooLarge) {
		return httpx.E(http.StatusUnprocessableEntity, "svg_unsafe", "SVG file is too large").With("reason", "too_large")
	}
	if err != nil {
		return err
	}
	if prob := checkSVGBytes(b); prob != "" {
		return httpx.E(http.StatusUnprocessableEntity, "svg_unsafe", "SVG contains scripts or external references").With("reason", string(prob))
	}
	return nil
}

func errUnsupported(detected string) *httpx.Error {
	e := httpx.E(http.StatusUnsupportedMediaType, "unsupported_type", "this file type is not supported")
	if i := strings.IndexByte(detected, ';'); i >= 0 {
		detected = detected[:i]
	}
	if detected != "" {
		e = e.With("detected", detected)
	}
	return e
}

func (m *Module) uploadStatus(w http.ResponseWriter, r *http.Request, p *core.Principal, id string) error {
	up, err := m.loadUpload(r.Context(), m.app.DB.R, id, p)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"uploadId": up.ID, "received": up.Received, "size": up.Size, "filename": up.Filename,
		"complete": false, "chunkSize": maxChunk})
}

func (m *Module) abortUpload(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	if _, err := m.loadUpload(r.Context(), m.app.DB.R, id, p); err != nil {
		return err
	}
	lock := m.upLock(id)
	lock.Lock()
	defer lock.Unlock()
	m.discardUpload(r.Context(), id)
	return httpx.NoContent(w)
}

// cleanupUploads removes expired uploads and stray temporary files.
func (m *Module) cleanupUploads(ctx context.Context) {
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT id FROM uploads WHERE expires_at < ?`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		slog.Error("media: listing expired uploads", "err", err)
		return
	}
	var expired []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			expired = append(expired, id)
		}
	}
	rows.Close()
	for _, id := range expired {
		func() {
			l := m.upLock(id)
			l.Lock()
			defer l.Unlock()
			slog.Info("media: removing expired upload", "upload", id)
			m.discardUpload(ctx, id)
		}()
	}
	// part files without a row (crash between create and insert, or manual deletion)
	known := map[string]bool{}
	rows, err = m.app.DB.R.QueryContext(ctx, `SELECT id FROM uploads`)
	if err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				known[id] = true
			}
		}
		rows.Close()
		m.sweepDir(m.tmpDir("uploads"), time.Hour, func(name string) bool {
			return known[strings.TrimSuffix(name, ".part")]
		})
	}
	m.sweepDir(m.tmpDir("ingest"), 24*time.Hour, nil)
	m.sweepDir(m.tmpDir("fonts"), time.Hour, nil)
	running := map[string]bool{}
	if rows, err := m.app.DB.R.QueryContext(ctx, `SELECT id FROM media_jobs WHERE state = 'running'`); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				running[id] = true
			}
		}
		rows.Close()
	}
	m.sweepDir(m.tmpDir("jobs"), 24*time.Hour, func(name string) bool { return running[name] })
}

// sweepDir deletes entries older than age, except those keep() accepts.
func (m *Module) sweepDir(dir string, age time.Duration, keep func(name string) bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !isNotExist(err) {
			slog.Warn("media: reading temp directory", "dir", dir, "err", err)
		}
		return
	}
	cutoff := time.Now().Add(-age)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) || keep != nil && keep(e.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			slog.Warn("media: removing stale temp entry", "path", filepath.Join(dir, e.Name()), "err", err)
		}
	}
}
