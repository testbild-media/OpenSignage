package updates

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"opensignage/internal/common/release"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
)

// uploadHandler accepts an offline bundle (raw body = opensignage-<v>-bundle.tar, up to
// 2 GB). The tar is extracted while it streams in (bounded memory, no second copy on
// disk), then verified like a downloaded release.
func (m *Module) uploadHandler(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	if r.ContentLength > maxBundleBytes {
		return httpx.ErrTooLarge
	}
	if err := m.begin("upload", "", p); err != nil {
		return err
	}
	defer m.end()
	info, err := m.importBundle(r.Context(), http.MaxBytesReader(w, r.Body, maxBundleBytes), r.ContentLength)
	ip := clientIP(r)
	if err != nil {
		m.log.Warn("updates: bundle upload rejected", "err", err)
		m.audit(r.Context(), p, ip, core.AuditEntry{Action: "update.upload_failed", ObjectType: "release",
			New: map[string]any{"error": err.Error()}})
		return err
	}
	m.audit(r.Context(), p, ip, core.AuditEntry{Action: "update.uploaded", ObjectType: "release", ObjectID: info.Version,
		ObjectName: info.Version, New: map[string]any{"packages": len(info.Packages)}})
	return httpx.OK(w, map[string]any{"version": info.Version})
}

type countingReader struct {
	r    io.Reader
	n    int64
	tick func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if n > 0 && c.tick != nil {
		c.tick(c.n)
	}
	return n, err
}

// importBundle extracts and verifies a bundle and makes it a ready release.
func (m *Module) importBundle(ctx context.Context, body io.Reader, size int64) (*release.Info, error) {
	if err := os.MkdirAll(m.updatesDir(), 0o750); err != nil {
		return nil, err
	}
	if size > 0 {
		if err := checkDisk(m.updatesDir(), size*12/10); err != nil {
			return nil, err
		}
	}
	tmp, err := os.MkdirTemp(m.updatesDir(), ".upload-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	total := size
	if total < 0 {
		total = 0
	}
	cr := &countingReader{r: body, tick: func(n int64) { m.progress("receiving", n, max(total, n)) }}
	if err := release.ExtractBundle(cr, tmp, maxBundleBytes); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, httpx.ErrTooLarge
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errBundleInvalid.With("detail", err.Error()).Wrap(err)
	}
	m.progress("verifying", cr.n, cr.n)
	if _, err := os.Stat(filepath.Join(tmp, release.InfoFile)); err != nil {
		return nil, errBundleInvalid.With("detail", "release.json missing")
	}
	if _, err := os.Stat(filepath.Join(tmp, release.SigFile)); err != nil {
		return nil, errSignature.With("detail", "SHA256SUMS.sig missing (unsigned release)")
	}
	ver, err := release.VerifyDir(tmp, m.trustedKeys(), false)
	if err != nil {
		return nil, verifyErr(err)
	}
	info := ver.Info
	v := info.Version
	if !validVersion(v) {
		return nil, errReleaseInvalid.With("detail", "invalid version "+v)
	}
	if run, err := m.activeClientRun(ctx); err != nil {
		return nil, err
	} else if run != nil && run.Version == v {
		return nil, errReleaseInUse.With("version", v)
	}
	dest := m.releaseDir(v)
	if err := os.RemoveAll(dest); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return nil, err
	}
	var pkgSize int64
	for _, p := range info.Packages {
		pkgSize += p.Size
	}
	if _, err := m.app.DB.W.ExecContext(ctx, `INSERT INTO releases(version, source, notes, size, published_at, state, error, path, updated_at)
		VALUES (?, 'upload', ?, ?, ?, ?, '', ?, ?)
		ON CONFLICT(version) DO UPDATE SET source = excluded.source, notes = excluded.notes, size = excluded.size,
			published_at = excluded.published_at, state = excluded.state, error = '', path = excluded.path, updated_at = excluded.updated_at`,
		v, info.Notes, pkgSize, info.Date, relReady, dest, db.Now()); err != nil {
		return nil, err
	}
	m.log.Info("updates: release bundle imported", "version", v, "packages", len(info.Packages))
	m.publish("release", v, map[string]any{"state": relReady})
	return &info, nil
}

// deleteRelease removes the downloaded files of a release (disk space). A GitHub release
// stays listed and can be downloaded again.
func (m *Module) deleteRelease(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	v := r.PathValue("version")
	if !validVersion(v) {
		return errReleaseNotFound
	}
	row, err := m.getRelease(r.Context(), v)
	if err != nil {
		return err
	}
	if row == nil {
		return errReleaseNotFound
	}
	if op := m.currentOp(); op != nil && (op.Version == v || op.Kind == "upload") || row.State == relDownloading {
		return errReleaseInUse.With("version", v)
	}
	if run, err := m.activeClientRun(r.Context()); err != nil {
		return err
	} else if run != nil && run.Version == v {
		return errReleaseInUse.With("version", v)
	}
	if err := os.RemoveAll(m.releaseDir(v)); err != nil {
		return err
	}
	if row.Source == "github" {
		err = m.setReleaseState(r.Context(), v, relAvailable, "", "")
	} else {
		_, err = m.app.DB.W.ExecContext(r.Context(), `DELETE FROM releases WHERE version = ?`, v)
	}
	if err != nil {
		return err
	}
	m.audit(r.Context(), p, clientIP(r), core.AuditEntry{Action: "update.release_deleted", ObjectType: "release", ObjectID: v, ObjectName: v})
	m.publish("release", v, map[string]any{"state": "deleted"})
	return m.overview(w, r, p)
}
