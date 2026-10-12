package updates

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"opensignage/internal/common/atomicfile"
	"opensignage/internal/common/diskfree"
	"opensignage/internal/common/release"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
)

const (
	maxMetaBytes     = 4 << 20
	maxBundleBytes   = 2 << 30
	stallTimeout     = 60 * time.Second
	downloadAttempts = 3
)

// permanentErr marks download errors a retry cannot fix.
type permanentErr struct{ error }

func (p permanentErr) Unwrap() error { return p.error }

func (m *Module) downloadHandler(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	v := r.PathValue("version")
	if !validVersion(v) {
		return errReleaseNotFound
	}
	row, err := m.getRelease(r.Context(), v)
	if err != nil {
		return err
	}
	if row == nil || row.Source != "github" {
		return errReleaseNotFound
	}
	if row.State == relReady {
		return m.overview(w, r, p)
	}
	if err := m.begin("download", v, p); err != nil {
		return err
	}
	if err := m.setReleaseState(r.Context(), v, relDownloading, "", ""); err != nil {
		m.end()
		return err
	}
	ip := clientIP(r)
	m.audit(r.Context(), p, ip, core.AuditEntry{Action: "update.download_started", ObjectType: "release", ObjectID: v, ObjectName: v,
		New: map[string]any{"repo": m.repo()}})
	m.publish("release", v, map[string]any{"state": relDownloading})
	go func() {
		defer m.end()
		var err error
		defer func() {
			if rec := recover(); rec != nil {
				err = fmt.Errorf("download crashed: %v", rec)
				m.log.Error("updates: download panic", "panic", fmt.Sprint(rec))
			}
			m.finishDownload(v, err, p, ip)
		}()
		err = m.downloadRelease(m.ctx(), v)
	}()
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"version": v})
	return nil
}

func (m *Module) finishDownload(v string, err error, p *core.Principal, ip string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dir := m.releaseDir(v)
	if err == nil {
		var size int64
		if info, ierr := readInfo(dir); ierr == nil {
			for _, pk := range info.Packages {
				size += pk.Size
			}
		}
		if _, dbErr := m.app.DB.W.ExecContext(ctx, `UPDATE releases SET state = ?, error = '', path = ?, size = ?, updated_at = ? WHERE version = ?`,
			relReady, dir, size, db.Now(), v); dbErr != nil {
			m.log.Error("updates: storing download result", "err", dbErr)
		}
		m.log.Info("updates: release downloaded and verified", "version", v)
		m.audit(ctx, p, ip, core.AuditEntry{Action: "update.downloaded", ObjectType: "release", ObjectID: v, ObjectName: v})
		m.publish("release", v, map[string]any{"state": relReady})
		return
	}
	if errors.Is(err, errSignature) || isCode(err, errSignature.Code) {
		_ = os.RemoveAll(dir) // never keep files of an untrusted release
	}
	if dbErr := m.setReleaseState(ctx, v, relFailed, encodeErr(err), ""); dbErr != nil {
		m.log.Error("updates: storing download result", "err", dbErr)
	}
	m.log.Warn("updates: download failed", "version", v, "err", err)
	m.audit(ctx, p, ip, core.AuditEntry{Action: "update.download_failed", ObjectType: "release", ObjectID: v, ObjectName: v,
		New: map[string]any{"error": err.Error()}})
	m.publish("release", v, map[string]any{"state": relFailed, "error": encodeErr(err)})
}

func isCode(err error, code string) bool {
	var ae *httpx.Error
	return errors.As(err, &ae) && ae.Code == code
}

// downloadRelease fetches release.json, SHA256SUMS and its signature, verifies them,
// then downloads every package (all architectures: devices need theirs) with resume,
// and finally verifies the complete directory.
func (m *Module) downloadRelease(ctx context.Context, v string) error {
	list, err := m.gh.releases(ctx, m.repo())
	if err != nil {
		return err
	}
	cand := findRelease(list, v)
	if cand == nil {
		return errReleaseNotFound
	}
	dir := m.releaseDir(v)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	m.progress("metadata", 0, 0)
	for _, name := range []string{release.InfoFile, release.SumsFile, release.SigFile} {
		a, ok := cand.Assets[name]
		switch {
		case !ok && name == release.SigFile:
			return errSignature.With("detail", "SHA256SUMS.sig missing (unsigned release)")
		case !ok:
			return errReleaseInvalid.With("detail", name+" missing")
		case a.Size > maxMetaBytes:
			return errReleaseInvalid.With("detail", name+" too large")
		}
		if err := m.fetchWithRetry(ctx, a.URL, filepath.Join(dir, name), a.Size, "", nil); err != nil {
			return err
		}
	}
	// Signature and release.json first: an untrusted release never gets its packages
	// downloaded.
	if _, err := release.VerifyDir(dir, m.trustedKeys(), true); err != nil {
		return verifyErr(err)
	}
	info, err := readInfo(dir)
	if err != nil {
		return errReleaseInvalid.With("detail", err.Error())
	}
	if info.Version != v {
		return errReleaseVersion.With("version", info.Version)
	}
	var total, have int64
	for _, p := range info.Packages {
		total += p.Size
		if st, err := os.Stat(filepath.Join(dir, p.File)); err == nil && st.Size() == p.Size {
			have += p.Size
		} else if st, err := os.Stat(filepath.Join(dir, p.File+".part")); err == nil && st.Size() <= p.Size {
			have += st.Size()
		}
	}
	if err := checkDisk(dir, (total-have)*12/10); err != nil {
		return err
	}
	var done int64
	for _, p := range info.Packages {
		final := filepath.Join(dir, p.File)
		if fileMatches(final, p) {
			done += p.Size
			m.progress("packages", done, total)
			continue
		}
		a, ok := cand.Assets[p.File]
		if !ok {
			return errReleaseInvalid.With("detail", p.File+" missing")
		}
		base := done
		if err := m.fetchWithRetry(ctx, a.URL, final, p.Size, p.SHA256, func(n int64) { m.progress("packages", base+n, total) }); err != nil {
			return err
		}
		done += p.Size
	}
	m.progress("verifying", total, total)
	if _, err := release.VerifyDir(dir, m.trustedKeys(), false); err != nil {
		return verifyErr(err)
	}
	return nil
}

// findRelease returns the release with exactly this version (drafts excluded).
func findRelease(list []ghRelease, v string) *candidate {
	for _, r := range list {
		if r.Draft || strings.TrimPrefix(strings.TrimSpace(r.TagName), "v") != v {
			continue
		}
		c := &candidate{Version: v, Notes: r.Body, PublishedAt: r.PublishedAt, Prerelease: r.Prerelease, Assets: map[string]ghAsset{}}
		for _, a := range r.Assets {
			c.Assets[a.Name] = a
		}
		return c
	}
	return nil
}

func verifyErr(err error) error {
	switch {
	case errors.Is(err, release.ErrUntrusted):
		return errSignature.Wrap(err)
	case strings.Contains(err.Error(), "not signed"), strings.Contains(err.Error(), "signature"):
		return errSignature.With("detail", err.Error()).Wrap(err)
	case strings.Contains(err.Error(), "checksum"):
		return errChecksum.With("detail", err.Error()).Wrap(err)
	}
	return errReleaseInvalid.With("detail", err.Error()).Wrap(err)
}

// checkDisk refuses work that would fill the disk: a job that does not fit is rejected
// up front.
func checkDisk(dir string, need int64) error {
	if need <= 0 {
		return nil
	}
	free, err := diskfree.Free(dir)
	if err != nil {
		return nil // unknown: the write fails cleanly if the disk is really full
	}
	if int64(free) < need {
		return errDiskSpace.With("neededMb", mb(need)).With("freeMb", mb(int64(free)))
	}
	return nil
}

func mb(n int64) int64 { return (n + (1 << 20) - 1) >> 20 }

func fileMatches(p string, pkg release.Package) bool {
	st, err := os.Stat(p)
	if err != nil || st.Size() != pkg.Size {
		return false
	}
	sum, _, err := release.FileSHA256(p)
	return err == nil && sum == pkg.SHA256
}

// fetchWithRetry downloads src to dest through dest.part, resuming after interruptions
// (HTTP Range). With a sha256 the result is checked before it replaces dest.
func (m *Module) fetchWithRetry(ctx context.Context, src, dest string, size int64, sha string, onProgress func(int64)) error {
	part := dest + ".part"
	var last error
	for attempt := 1; attempt <= downloadAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := m.fetchPart(ctx, src, part, size, onProgress)
		if err == nil {
			if sha != "" {
				got, n, err := release.FileSHA256(part)
				if err != nil {
					return err
				}
				if n != size || got != sha {
					_ = os.Remove(part)
					return errChecksum.With("file", filepath.Base(dest))
				}
			}
			return atomicfile.Rename(part, dest)
		}
		last = err
		var perm permanentErr
		if errors.As(err, &perm) {
			return perm.error
		}
		m.log.Info("updates: download interrupted, resuming", "file", filepath.Base(dest), "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
	return errDownloadFailed.With("file", filepath.Base(dest)).With("detail", shortNetErr(last)).Wrap(last)
}

// fetchPart appends the missing bytes of src to part. size < 0 means unknown. Only
// GitHub download URLs are fetched (also after redirects).
func (m *Module) fetchPart(ctx context.Context, src, part string, size int64, onProgress func(int64)) error {
	file := filepath.Base(strings.TrimSuffix(part, ".part"))
	if u, err := url.Parse(src); err != nil || !m.gh.allowed(u) {
		return permanentErr{errDownloadFailed.With("file", file).With("detail", errForeignURL.Error())}
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return permanentErr{err}
	}
	defer f.Close()
	off, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return permanentErr{err}
	}
	if size >= 0 && off > size {
		if err := truncate(f); err != nil {
			return permanentErr{err}
		}
		off = 0
	}
	if size >= 0 && off == size {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return permanentErr{err}
	}
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("Accept", "application/octet-stream")
	if off > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", off))
	}
	res, err := m.gh.dl.Do(req)
	if errors.Is(err, errForeignURL) {
		return permanentErr{errDownloadFailed.With("file", file).With("detail", errForeignURL.Error()).Wrap(err)}
	}
	if err != nil {
		return err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusPartialContent:
		if !strings.HasPrefix(res.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", off)) {
			_ = truncate(f)
			return fmt.Errorf("unexpected Content-Range %q", res.Header.Get("Content-Range"))
		}
	case http.StatusOK:
		if off > 0 { // the server ignored the range: start over
			if err := truncate(f); err != nil {
				return permanentErr{err}
			}
			off = 0
		}
	case http.StatusRequestedRangeNotSatisfiable:
		_ = truncate(f)
		return fmt.Errorf("range not satisfiable, restarting")
	case http.StatusNotFound, http.StatusGone, http.StatusForbidden, http.StatusUnauthorized:
		return permanentErr{errDownloadFailed.With("file", file).With("detail", res.Status)}
	default:
		return fmt.Errorf("HTTP %s", res.Status)
	}
	stall := time.AfterFunc(stallTimeout, cancel)
	defer stall.Stop()
	limit := int64(-1)
	if size >= 0 {
		limit = size - off
	}
	buf := make([]byte, 64<<10)
	written := int64(0)
	for limit < 0 || written < limit {
		chunk := buf
		if limit >= 0 && limit-written < int64(len(chunk)) {
			chunk = chunk[:limit-written]
		}
		n, rerr := res.Body.Read(chunk)
		if n > 0 {
			stall.Reset(stallTimeout)
			if _, werr := f.Write(chunk[:n]); werr != nil {
				return permanentErr{werr} // disk full or I/O error: retrying does not help
			}
			written += int64(n)
			if onProgress != nil {
				onProgress(off + written)
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			_ = f.Sync()
			return rerr
		}
	}
	if err := f.Sync(); err != nil {
		return permanentErr{err}
	}
	if size >= 0 && off+written != size {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func truncate(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.Seek(0, io.SeekStart)
	return err
}
