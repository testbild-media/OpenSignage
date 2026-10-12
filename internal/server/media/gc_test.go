package media

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// seedBlob stores data in the blob store and returns its hash.
func (h *harness) seedBlob(data []byte) string {
	h.t.Helper()
	src := filepath.Join(h.dir, "seed-"+sha(data)[:8])
	if err := os.WriteFile(src, data, 0o640); err != nil {
		h.t.Fatal(err)
	}
	s, _, _, err := h.m.IngestFile(context.Background(), src)
	if err != nil {
		h.t.Fatal(err)
	}
	_ = os.Remove(src)
	return s
}

func (h *harness) seedAsset(id, blob string) {
	h.t.Helper()
	h.exec(`INSERT INTO media_assets(id, name, kind, revision, blob_sha256, status, created_at, updated_at) VALUES (?, ?, 'image', 1, ?, 'ready', 'x', 'x')`, id, id, blob)
	h.exec(`INSERT INTO media_revisions(asset_id, revision, blob_sha256, created_at) VALUES (?, 1, ?, 'x')`, id, blob)
}

func (h *harness) blobExists(sha string) bool {
	_, err := os.Stat(h.m.BlobPath(sha))
	return err == nil
}

func data(n int, tag string) []byte { return append([]byte(tag), bytes.Repeat([]byte{'x'}, n)...) }

func TestGCNeverDeletesReferencedBlobs(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	revisionOnly := h.seedBlob(data(100, "rev"))
	variantOnly := h.seedBlob(data(100, "var"))
	deployed := h.seedBlob(data(100, "dep"))   // only a deployment still needs it
	fontBlob := h.seedBlob(data(100, "font"))  // only a custom font needs it
	trashedBlob := h.seedBlob(data(100, "tr")) // belongs to a trashed (restorable) asset
	garbage1 := h.seedBlob(data(100, "garbage1"))
	garbage2 := h.seedBlob(data(100, "garbage2"))

	h.seedAsset("a-rev", revisionOnly)
	h.seedAsset("a-var", h.seedBlob(data(100, "orig")))
	h.exec(`INSERT INTO media_variants(asset_id, revision, variant, blob_sha256, created_at) VALUES ('a-var', 1, 'thumb', ?, 'x')`, variantOnly)
	h.seedAsset("a-trashed", trashedBlob)
	h.exec(`UPDATE media_assets SET trashed_at = 'x' WHERE id = 'a-trashed'`)
	// a purged asset's blob that a deployment still lists: the client may still need it
	h.exec(`INSERT INTO deployments(id, zone_id, hash, manifest, built_at) VALUES ('d1', 'z1', 'h', '{}', 'x')`)
	h.exec(`INSERT INTO deployment_blobs(deployment_id, sha256) VALUES ('d1', ?)`, deployed)
	h.exec(`INSERT INTO font_faces(id, family, style, weight_min, weight_max, blob_sha256, format, license, created_at) VALUES ('f1', 'Fam', 'normal', 400, 400, ?, 'truetype', 'OFL', 'x')`, fontBlob)
	// a file on disk without a blobs row (crash between link and insert)
	orphan := sha(data(100, "orphan-file"))
	p := h.m.BlobPath(orphan)
	_ = os.MkdirAll(filepath.Dir(p), 0o750)
	if err := os.WriteFile(p, data(100, "orphan-file"), 0o640); err != nil {
		t.Fatal(err)
	}

	// dry run reports exactly the garbage and touches nothing
	rep, err := h.m.CollectGarbage(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, it := range rep.Items {
		got[it.SHA256] = true
	}
	if rep.Blobs != 3 || !got[garbage1] || !got[garbage2] || !got[orphan] {
		t.Fatalf("dry run: %+v", rep)
	}
	for _, s := range []string{garbage1, garbage2, orphan, revisionOnly, variantOnly, deployed, fontBlob, trashedBlob} {
		if !h.blobExists(s) {
			t.Fatalf("dry run deleted %s", s)
		}
	}

	rep, err = h.m.CollectGarbage(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Blobs != 3 {
		t.Fatalf("real run: %+v", rep)
	}
	for _, s := range []string{garbage1, garbage2, orphan} {
		if h.blobExists(s) {
			t.Fatalf("unreferenced blob %s must be deleted", s)
		}
	}
	if h.count(`SELECT COUNT(*) FROM blobs WHERE sha256 IN (?, ?)`, garbage1, garbage2) != 0 {
		t.Fatal("rows of deleted blobs must be removed")
	}
	for name, s := range map[string]string{"revision": revisionOnly, "variant": variantOnly, "deployment": deployed, "font": fontBlob, "trashed asset": trashedBlob} {
		if !h.blobExists(s) || h.count(`SELECT COUNT(*) FROM blobs WHERE sha256 = ?`, s) != 1 {
			t.Fatalf("blob referenced by %s was deleted", name)
		}
	}

	// the deployment lets go: only now the blob may go
	h.exec(`DELETE FROM deployment_blobs`)
	if rep, _ = h.m.CollectGarbage(ctx, false); rep.Blobs != 1 || h.blobExists(deployed) {
		t.Fatalf("after the deployment released it: %+v", rep)
	}
	// purged assets: rows gone → blobs collectable, but not while another asset shares the blob
	h.seedAsset("a-twin", revisionOnly)
	h.exec(`DELETE FROM media_assets WHERE id = 'a-rev'`)
	if rep, _ = h.m.CollectGarbage(ctx, false); rep.Blobs != 0 || !h.blobExists(revisionOnly) {
		t.Fatalf("shared blob must stay: %+v", rep)
	}
	h.exec(`DELETE FROM media_assets WHERE id = 'a-twin'`)
	if rep, _ = h.m.CollectGarbage(ctx, false); rep.Blobs != 1 || h.blobExists(revisionOnly) {
		t.Fatalf("last owner gone: %+v", rep)
	}
}

func TestGCGracePeriodProtectsFreshBlobs(t *testing.T) {
	h := newHarness(t)
	h.m.gcGrace = time.Hour
	s := h.seedBlob(data(50, "fresh"))
	rep, err := h.m.CollectGarbage(context.Background(), false)
	if err != nil || rep.Blobs != 0 || !h.blobExists(s) {
		t.Fatalf("a freshly ingested blob must survive until it is referenced: %+v %v", rep, err)
	}
	// age the file and the row
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(h.m.BlobPath(s), old, old)
	h.exec(`UPDATE blobs SET created_at = '2000-01-01T00:00:00Z'`)
	if rep, _ = h.m.CollectGarbage(context.Background(), false); rep.Blobs != 1 || h.blobExists(s) {
		t.Fatalf("old unreferenced blob: %+v", rep)
	}
}

func TestIngestRacingWithGC(t *testing.T) {
	h := newHarness(t)
	h.m.gcGrace = time.Minute
	ctx := context.Background()
	// Old, unreferenced blobs are exactly what a collector wants to delete. While it runs,
	// uploads of identical content re-ingest them and then reference them.
	type blob struct {
		data []byte
		sha  string
	}
	var blobs []blob
	for i := 0; i < 40; i++ {
		d := data(200, "race-"+string(rune('a'+i%26))+string(rune('0'+i/26)))
		s := h.seedBlob(d)
		old := time.Now().Add(-time.Hour)
		_ = os.Chtimes(h.m.BlobPath(s), old, old)
		blobs = append(blobs, blob{d, s})
	}
	h.exec(`UPDATE blobs SET created_at = '2000-01-01T00:00:00Z'`)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := h.m.CollectGarbage(ctx, false); err != nil {
					t.Errorf("gc: %v", err)
					return
				}
			}
		}
	}()
	for i, b := range blobs {
		src := filepath.Join(h.dir, "race.bin")
		_ = os.WriteFile(src, b.data, 0o640)
		s, _, _, err := h.m.IngestFile(ctx, src)
		if err != nil {
			t.Fatal(err)
		}
		id := "race-" + itoaN(i)
		h.exec(`INSERT INTO media_assets(id, name, kind, revision, blob_sha256, status, created_at, updated_at) VALUES (?, ?, 'image', 1, ?, 'ready', 'x', 'x')`, id, id, s)
		h.exec(`INSERT INTO media_revisions(asset_id, revision, blob_sha256, created_at) VALUES (?, 1, ?, 'x')`, id, s)
	}
	close(stop)
	wg.Wait()
	for _, b := range blobs {
		if !h.blobExists(b.sha) {
			t.Fatalf("referenced blob %s lost its file", b.sha)
		}
		if h.count(`SELECT COUNT(*) FROM blobs WHERE sha256 = ?`, b.sha) != 1 {
			t.Fatalf("referenced blob %s lost its row", b.sha)
		}
	}
}

func itoaN(n int) string { return string(rune('a'+n/26)) + string(rune('a'+n%26)) }

func TestRepeatedIngestRepairsDamagedBlob(t *testing.T) {
	h := newHarness(t)
	d := data(300, "repair")
	s := h.seedBlob(d)
	// simulate a truncated file with the right name
	if err := os.WriteFile(h.m.BlobPath(s), d[:10], 0o640); err != nil {
		t.Fatal(err)
	}
	s2 := h.seedBlob(d)
	got, _ := os.ReadFile(h.m.BlobPath(s2))
	if s != s2 || !bytes.Equal(got, d) {
		t.Fatal("a damaged blob must be replaced by a fresh ingest of the same content")
	}
}
