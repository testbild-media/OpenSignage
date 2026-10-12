package deploy_test

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"

	"opensignage/internal/server/devices/devtest"
)

// TestReadyRevisionIsShippedWhileTheNewestIsNotReady covers the media module's
// ready_revision: a replacement that is processing (or failed) and a reprocess of a ready
// asset must not make the asset disappear from the manifest.
func TestReadyRevisionIsShippedWhileTheNewestIsNotReady(t *testing.T) {
	b, c := setup(t)
	ctx := context.Background()
	entries := func() string {
		m, err := b.Build(ctx, c.Z1, "Europe/Berlin", "1.2.3")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, a := range m.Assets {
			if a.ID == "asset-video" {
				out = append(out, a.Variant+"@"+strconv.Itoa(int(a.Revision)))
			}
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	ready := "orig@1,poster@1,v1080@1,v720@1"
	if got := entries(); got != ready {
		t.Fatalf("baseline: %s", got)
	}

	// revision 2 uploaded, still processing; revision 1 stays the ready one
	devtest.Exec(t, b.DB, `UPDATE media_assets SET revision = 2, status = 'processing', ready_revision = 1 WHERE id = 'asset-video'`)
	devtest.Exec(t, b.DB, `INSERT INTO media_revisions(asset_id, revision, blob_sha256, meta, created_at)
		SELECT asset_id, 2, blob_sha256, meta, created_at FROM media_revisions WHERE asset_id = 'asset-video'`)
	if got := entries(); got != ready {
		t.Fatalf("replacement processing must ship the ready revision: %s", got)
	}

	// the replacement failed: still the ready revision
	devtest.Exec(t, b.DB, `UPDATE media_assets SET status = 'failed' WHERE id = 'asset-video'`)
	if got := entries(); got != ready {
		t.Fatalf("failed replacement must ship the ready revision: %s", got)
	}

	// a reprocess of a ready asset (status processing, ready_revision == revision)
	devtest.Exec(t, b.DB, `UPDATE media_assets SET revision = 1, status = 'processing', ready_revision = 1 WHERE id = 'asset-video'`)
	if got := entries(); got != ready {
		t.Fatalf("reprocess must not make a ready asset unusable: %s", got)
	}

	// never processed (first upload still running or failed): nothing to ship
	devtest.Exec(t, b.DB, `UPDATE media_assets SET status = 'processing', ready_revision = 0 WHERE id = 'asset-video'`)
	if got := entries(); got != "" {
		t.Fatalf("an asset that never finished must not be shipped: %s", got)
	}

	// finished again: the new revision is shipped with its own variants
	devtest.Exec(t, b.DB, `UPDATE media_assets SET revision = 2, status = 'ready', ready_revision = 2 WHERE id = 'asset-video'`)
	devtest.Exec(t, b.DB, `INSERT INTO media_variants(asset_id, revision, variant, blob_sha256, width, height, created_at)
		SELECT asset_id, 2, variant, blob_sha256, width, height, created_at FROM media_variants WHERE asset_id = 'asset-video' AND revision = 1 AND variant = 'v1080'`)
	if got := entries(); got != "orig@2,v1080@2" {
		t.Fatalf("new ready revision: %s", got)
	}
}
