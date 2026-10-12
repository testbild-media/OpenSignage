package media

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"opensignage/internal/common/atomicfile"
)

// Reference sources (what keeps a blob alive).
const (
	refAsset      = "asset"      // current original of an asset
	refRevision   = "revision"   // any revision of an asset (also trashed assets)
	refVariant    = "variant"    // thumbnails, posters, transcodes
	refDeployment = "deployment" // active or previous deployment of any client
	refFont       = "font"       // custom font faces
)

// refRow is one reference from a table to a blob.
type refRow struct {
	Src      string
	AssetID  string
	Revision int
	Variant  string
	SHA      string
}

// loadRefs returns every reference to a blob. A blob may only be deleted when none exists.
// Purged assets have no rows any more; trashed assets still hold their blobs (restorable).
// Open uploads and running jobs reference no blob yet (their data lives in tmp/ until it
// is ingested), and the original of a running job belongs to its asset row.
func loadRefs(ctx context.Context, q queryer) ([]refRow, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT 'asset', id, revision, '', blob_sha256 FROM media_assets WHERE blob_sha256 IS NOT NULL
		UNION ALL SELECT 'revision', asset_id, revision, '', blob_sha256 FROM media_revisions
		UNION ALL SELECT 'variant', asset_id, revision, variant, blob_sha256 FROM media_variants
		UNION ALL SELECT 'deployment', '', 0, '', sha256 FROM deployment_blobs
		UNION ALL SELECT 'font', id, 0, '', blob_sha256 FROM font_faces`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []refRow
	for rows.Next() {
		var r refRow
		if err := rows.Scan(&r.Src, &r.AssetID, &r.Revision, &r.Variant, &r.SHA); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// isReferenced is the authoritative check, run inside the write transaction right before a
// blob row is deleted.
func isReferenced(ctx context.Context, q queryer, sha string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM media_assets WHERE blob_sha256 = ?1)
		OR EXISTS (SELECT 1 FROM media_revisions WHERE blob_sha256 = ?1)
		OR EXISTS (SELECT 1 FROM media_variants WHERE blob_sha256 = ?1)
		OR EXISTS (SELECT 1 FROM deployment_blobs WHERE sha256 = ?1)
		OR EXISTS (SELECT 1 FROM font_faces WHERE blob_sha256 = ?1)`, sha).Scan(&n)
	return n == 1, err
}

func blobSizes(ctx context.Context, q queryer, shas []string) (map[string]int64, error) {
	out := make(map[string]int64, len(shas))
	err := chunked(shas, 400, func(part []string) error {
		rows, err := q.QueryContext(ctx, `SELECT sha256, size FROM blobs WHERE sha256 IN (`+inPlaceholders(len(part))+`)`, strArgs(part)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sha string
			var size int64
			if err := rows.Scan(&sha, &size); err != nil {
				return err
			}
			out[sha] = size
		}
		return rows.Err()
	})
	return out, err
}

// freedBytes estimates what the garbage collector could free if all references for which
// removed returns true disappeared: blobs that no remaining reference needs.
func (m *Module) freedBytes(ctx context.Context, q queryer, removed func(refRow) bool) (int64, error) {
	refs, err := loadRefs(ctx, q)
	if err != nil {
		return 0, err
	}
	keep := map[string]bool{}
	for _, r := range refs {
		if !removed(r) {
			keep[r.SHA] = true
		}
	}
	cand := map[string]bool{}
	var list []string
	for _, r := range refs {
		if removed(r) && !keep[r.SHA] && !cand[r.SHA] {
			cand[r.SHA] = true
			list = append(list, r.SHA)
		}
	}
	sizes, err := blobSizes(ctx, q, list)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, s := range sizes {
		total += s
	}
	return total, nil
}

// GCItem is one blob the collector deleted (or would delete).
type GCItem struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Row    bool   `json:"row"`  // had a blobs table row
	File   bool   `json:"file"` // had a file on disk
}

// GCReport summarises a collection run.
type GCReport struct {
	DryRun bool     `json:"dryRun"`
	Blobs  int      `json:"blobs"`
	Bytes  int64    `json:"bytes"`
	Items  []GCItem `json:"items"` // at most 200 entries
}

type gcCand struct {
	GCItem
	young bool
}

// CollectGarbage physically deletes blobs that nothing references. A blob is kept if any
// media revision or variant (of any asset that has not been purged, trashed ones
// included), any deployment_blobs row or any font face points to it. Files younger than
// the grace period are never touched, so a blob that was just ingested and is about to be
// referenced cannot disappear. With dryRun nothing is deleted.
func (m *Module) CollectGarbage(ctx context.Context, dryRun bool) (GCReport, error) {
	m.gcRun.Lock()
	defer m.gcRun.Unlock()
	rep := GCReport{DryRun: dryRun, Items: []GCItem{}}

	refs, err := loadRefs(ctx, m.app.DB.R)
	if err != nil {
		return rep, err
	}
	referenced := make(map[string]bool, len(refs))
	for _, r := range refs {
		referenced[r.SHA] = true
	}
	cutoff := time.Now().Add(-m.gcGrace)
	cands := map[string]*gcCand{}

	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT sha256, size, created_at FROM blobs`)
	if err != nil {
		return rep, err
	}
	for rows.Next() {
		var sha, created string
		var size int64
		if err := rows.Scan(&sha, &size, &created); err != nil {
			rows.Close()
			return rep, err
		}
		if referenced[sha] {
			continue
		}
		c := &gcCand{GCItem: GCItem{SHA256: sha, Size: size, Row: true}}
		if t, err := time.Parse(time.RFC3339Nano, created); err == nil && t.After(cutoff) {
			c.young = true
		}
		cands[sha] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rep, err
	}

	root := filepath.Join(m.dataDir, "blobs", "sha256")
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if isNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		name := d.Name()
		if strings.Contains(name, ".part-") { // leftover of an interrupted copy
			if info.ModTime().Before(cutoff) && !dryRun {
				_ = atomicfile.Remove(path)
			}
			return nil
		}
		if !ValidSHA(name) || filepath.Base(filepath.Dir(path)) != name[:2] || referenced[name] {
			return nil
		}
		c := cands[name]
		if c == nil {
			c = &gcCand{GCItem: GCItem{SHA256: name, Size: info.Size()}}
			cands[name] = c
		}
		c.File = true
		c.Size = info.Size()
		c.young = info.ModTime().After(cutoff)
		return nil
	})
	if walkErr != nil {
		slog.Warn("media: scanning the blob directory", "err", walkErr)
	}

	var todo []*gcCand
	for _, c := range cands {
		if !c.young {
			todo = append(todo, c)
		}
	}
	if dryRun {
		for _, c := range todo {
			rep.Blobs++
			rep.Bytes += c.Size
			if len(rep.Items) < 200 {
				rep.Items = append(rep.Items, c.GCItem)
			}
		}
		return rep, nil
	}

	// Delete phase: no ingest may run concurrently (it holds gcMu.RLock), and every
	// candidate is re-checked inside the write transaction.
	m.gcMu.Lock()
	defer m.gcMu.Unlock()
	for len(todo) > 0 {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		n := min(200, len(todo))
		batch := todo[:n]
		todo = todo[n:]
		var deleted []*gcCand
		err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
			deleted = deleted[:0]
			for _, c := range batch {
				// Results of a running job that are not recorded yet (checked under gcMu.Lock,
				// pins are registered under gcMu.RLock).
				if m.isPinned(c.SHA256) {
					continue
				}
				// An ingest that happened since the scan touched the file: leave it.
				if st, err := os.Stat(m.BlobPath(c.SHA256)); err == nil && st.ModTime().After(cutoff) {
					continue
				}
				ref, err := isReferenced(ctx, tx, c.SHA256)
				if err != nil {
					return err
				}
				if ref {
					continue
				}
				if c.Row {
					if _, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE sha256 = ?`, c.SHA256); err != nil {
						return err
					}
				}
				deleted = append(deleted, c)
			}
			return nil
		})
		if err != nil {
			return rep, fmt.Errorf("blob garbage collection: %w", err)
		}
		for _, c := range deleted {
			if c.File {
				if err := atomicfile.Remove(m.BlobPath(c.SHA256)); err != nil {
					slog.Warn("media: cannot remove blob file now, it is retried by the next run", "sha256", c.SHA256, "err", err)
					continue
				}
			}
			rep.Blobs++
			rep.Bytes += c.Size
			if len(rep.Items) < 200 {
				rep.Items = append(rep.Items, c.GCItem)
			}
		}
	}
	if rep.Blobs > 0 {
		slog.Info("media: garbage collection finished", "blobs", rep.Blobs, "bytes", rep.Bytes)
	}
	return rep, nil
}

// removeEmptyBlobDirs tidies empty two-letter directories (cosmetic).
func (m *Module) removeEmptyBlobDirs() {
	root := filepath.Join(m.dataDir, "blobs", "sha256")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			_ = os.Remove(filepath.Join(root, e.Name())) // fails (harmlessly) when not empty
		}
	}
}
