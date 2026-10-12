package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"opensignage/internal/common/atomicfile"
	"opensignage/internal/common/ids"
	"opensignage/internal/server/db"
)

// BlobPath returns the path of a content-addressed blob below dataDir:
// <dataDir>/blobs/sha256/<aa>/<hash>. It returns "" for anything that is not a
// lowercase hex SHA-256, so a path can never be steered out of the blob directory.
func BlobPath(dataDir, sha string) string {
	if !ValidSHA(sha) {
		return ""
	}
	return filepath.Join(dataDir, "blobs", "sha256", sha[:2], sha)
}

// BlobPath returns the path of the blob with the given hash ("" if the hash is invalid).
func (m *Module) BlobPath(sha string) string { return BlobPath(m.dataDir, sha) }

// ValidSHA reports whether s is a lowercase hex SHA-256 digest.
func ValidSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// IngestFile copies the file at path into the blob store (content-addressed, atomic,
// idempotent) and registers it in the blobs table. The source file is left untouched.
// Concurrent ingests of identical content are safe: the final name is created with
// "link if not exists" semantics and the table row with INSERT OR IGNORE.
func (m *Module) IngestFile(ctx context.Context, path string) (sha string, size int64, mime string, err error) {
	if err := m.ensureDir(m.tmpDir("ingest")); err != nil {
		return "", 0, "", err
	}
	stage, sha, size, err := copyAndHash(ctx, path, m.tmpDir("ingest"))
	if err != nil {
		return "", 0, "", err
	}
	mime, err = m.commitBlob(ctx, stage, sha, size, false, false)
	if err != nil {
		_ = atomicfile.Remove(stage)
		return "", 0, "", err
	}
	return sha, size, mime, nil
}

// ingestMove consumes src (it must live inside the data directory): the file is hashed
// (unless knownSHA is given), linked into the blob store and removed.
func (m *Module) ingestMove(ctx context.Context, src, knownSHA string) (sha string, size int64, mime string, err error) {
	return m.ingestFromTmp(ctx, src, knownSHA, false, false)
}

// ingestPinned is ingestMove for results of a running job: the blob is pinned (protected
// from the garbage collector) until the caller releases it with unpin, which it does after
// the transaction that records the blob in media_variants.
func (m *Module) ingestPinned(ctx context.Context, src string) (sha string, size int64, mime string, err error) {
	return m.ingestFromTmp(ctx, src, "", false, true)
}

// ingestKeep is ingestMove without removing src, so the caller can retry later if the
// database step that follows fails (resumable upload completion).
func (m *Module) ingestKeep(ctx context.Context, src, knownSHA string) (sha string, size int64, mime string, err error) {
	return m.ingestFromTmp(ctx, src, knownSHA, true, false)
}

func (m *Module) ingestFromTmp(ctx context.Context, src, knownSHA string, keep, pin bool) (sha string, size int64, mime string, err error) {
	st, err := os.Stat(src)
	if err != nil {
		return "", 0, "", err
	}
	if !st.Mode().IsRegular() {
		return "", 0, "", fmt.Errorf("ingest %s: not a regular file", filepath.Base(src))
	}
	sha = knownSHA
	if sha == "" {
		if sha, err = hashFile(ctx, src); err != nil {
			return "", 0, "", err
		}
	}
	mime, err = m.commitBlob(ctx, src, sha, st.Size(), keep, pin)
	if err != nil {
		return "", 0, "", err
	}
	return sha, st.Size(), mime, nil
}

// commitBlob places stage as blob sha and records it. Unless keep is set, stage is
// consumed on success. The caller guarantees the content hashes to sha.
func (m *Module) commitBlob(ctx context.Context, stage, sha string, size int64, keep, pin bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	dest := m.BlobPath(sha)
	if dest == "" {
		return "", errors.New("invalid blob hash")
	}
	drop := func() {
		if !keep {
			_ = atomicfile.Remove(stage)
		}
	}
	mime := sniffMIME(readHead(stage, 8192))
	m.gcMu.RLock()
	defer m.gcMu.RUnlock()

	st, statErr := os.Stat(dest)
	if statErr == nil && st.Size() == size {
		// Deduplicated. Touch it so a concurrent garbage collection (which skips
		// recent files) cannot remove it before the caller records a reference.
		now := time.Now()
		_ = os.Chtimes(dest, now, now)
		drop()
	} else {
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return "", err
		}
		syncPath(stage)
		if statErr == nil {
			// A file with the right name but wrong size is damaged: replace it.
			_ = atomicfile.Remove(dest)
		}
		if err := linkOrCopy(stage, dest); err != nil {
			return "", err
		}
		now := time.Now()
		_ = os.Chtimes(dest, now, now) // "fresh" for the garbage collector until referenced
		drop()
		atomicfile.SyncDir(filepath.Dir(dest))
	}
	if _, err := m.app.DB.W.ExecContext(ctx, `INSERT OR IGNORE INTO blobs(sha256, size, mime, created_at) VALUES (?, ?, ?, ?)`,
		sha, size, mime, db.Now()); err != nil {
		return "", err
	}
	if pin {
		m.pin(sha) // still under gcMu.RLock: the collector cannot run between ingest and pin
	}
	return mime, nil
}

// pin protects a blob from the garbage collector until unpin.
func (m *Module) pin(sha string) {
	m.pinMu.Lock()
	m.pins[sha]++
	m.pinMu.Unlock()
}

func (m *Module) unpin(shas ...string) {
	m.pinMu.Lock()
	for _, s := range shas {
		if m.pins[s] > 1 {
			m.pins[s]--
		} else {
			delete(m.pins, s)
		}
	}
	m.pinMu.Unlock()
}

func (m *Module) isPinned(sha string) bool {
	m.pinMu.Lock()
	defer m.pinMu.Unlock()
	return m.pins[sha] > 0
}

// linkOrCopy places src at dest without ever overwriting an existing dest. Existing dest
// means a concurrent identical ingest won the race, which is fine (same content).
func linkOrCopy(src, dest string) error {
	err := os.Link(src, dest)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrExist):
		return nil
	}
	// No hard links (other filesystem, FAT, …): copy next to dest, then rename.
	part := dest + ".part-" + ids.Token(4)
	if cerr := copyFile(src, part); cerr != nil {
		_ = os.Remove(part)
		return fmt.Errorf("link %s: %v; copy: %w", filepath.Base(dest), err, cerr)
	}
	if _, serr := os.Stat(dest); serr == nil {
		_ = os.Remove(part)
		return nil
	}
	if rerr := atomicfile.Rename(part, dest); rerr != nil {
		_ = os.Remove(part)
		return rerr
	}
	return nil
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// copyAndHash copies src into a new file in dir while hashing it (single pass).
func copyAndHash(ctx context.Context, src, dir string) (stage, sha string, size int64, err error) {
	in, err := os.Open(src)
	if err != nil {
		return "", "", 0, err
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, "ingest-*.part")
	if err != nil {
		return "", "", 0, err
	}
	stage = out.Name()
	fail := func(err error) (string, string, int64, error) {
		_ = out.Close()
		_ = os.Remove(stage)
		return "", "", 0, err
	}
	h := sha256.New()
	size, err = io.Copy(io.MultiWriter(out, h), &ctxReader{ctx: ctx, r: in})
	if err != nil {
		return fail(err)
	}
	if err := out.Sync(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(stage)
		return "", "", 0, err
	}
	return stage, hex.EncodeToString(h.Sum(nil)), size, nil
}

// hashFile returns the SHA-256 of a file (streamed).
func hashFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, &ctxReader{ctx: ctx, r: f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ctxReader aborts a long read when the context is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// readHead returns up to n bytes from the start of the file (nil on error).
func readHead(path string, n int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, n)
	k, _ := io.ReadFull(f, buf)
	return buf[:k]
}

// syncPath flushes a file's data to disk (best effort: some platforms refuse read-only
// handles, in which case the data is already as durable as the writer left it).
func syncPath(path string) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return
	}
	if err := f.Sync(); err != nil {
		slog.Debug("media: fsync failed", "file", filepath.Base(path), "err", err)
	}
	_ = f.Close()
}

// BlobSize returns the stored size of a blob file, or -1 when it is missing.
func (m *Module) BlobSize(sha string) int64 {
	p := m.BlobPath(sha)
	if p == "" {
		return -1
	}
	st, err := os.Stat(p)
	if err != nil {
		return -1
	}
	return st.Size()
}
