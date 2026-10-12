package media

import (
	"context"
	"os"
	"testing"
	"time"

	"opensignage/internal/server/core"
)

// waitTopic reports whether an event on topic arrives within d.
func waitTopic(sub *core.Subscription, topic string, d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case e := <-sub.C:
			if e.Topic == topic {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func (h *harness) folderOf(assetID string) (folder *string, trashed bool) {
	h.t.Helper()
	var tr *string
	if err := h.app.DB.R.QueryRow(`SELECT folder_id, trashed_at FROM media_assets WHERE id = ?`, assetID).Scan(&folder, &tr); err != nil {
		h.t.Fatal(err)
	}
	return folder, tr != nil
}

func TestTrashFolderSubtreeAndRestore(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	f := c.mkFolder("Menus", nil)
	g := c.mkFolder("Summer", &f)
	a1 := c.mustUpload("in-f.png", pngBytes(10, 10, 1), &f)["id"].(string)
	a2 := c.mustUpload("in-g.png", pngBytes(10, 10, 2), &g)["id"].(string)
	a3 := c.mustUpload("in-root.png", pngBytes(10, 10, 3), nil)["id"].(string)

	// a2 is used by a published presentation
	h.exec(`INSERT INTO presentations(id, name, draft, created_at, updated_at) VALUES ('p1', 'Happy Hour', '{}', 'x', 'x')`)
	h.exec(`INSERT INTO content_refs(asset_id, owner_type, owner_id) VALUES (?, 'presentation_published', 'p1')`, a2)

	sub := h.app.Bus.Subscribe(64)
	defer sub.Close()
	out := c.mustOK("DELETE", "/api/v1/media/folders/"+f, nil)
	if out["trashedAssets"].(float64) != 2 || out["trashedFolders"].(float64) != 2 {
		t.Fatalf("trash result: %v", out)
	}
	if !waitTopic(sub, core.TopicContent, time.Second) {
		t.Fatal("trashing used content must publish a content event so deployments are rebuilt")
	}
	// everything in the subtree is gone from the listings, nothing fell back to the root
	root := c.mustOK("GET", "/api/v1/media?folder=root", nil)
	if got := idSet(root["assets"]); len(got) != 1 || !got[a3] {
		t.Fatalf("only the unrelated root asset remains: %v", names(root["assets"]))
	}
	if len(root["folders"].([]any)) != 0 {
		t.Fatal("trashed folder must not be listed")
	}
	all := c.mustOK("GET", "/api/v1/media?q=in-", nil)
	if len(all["assets"].([]any)) != 1 {
		t.Fatalf("search must not find trashed assets: %v", names(all["assets"]))
	}
	if code, _ := c.do("GET", "/api/v1/media?folder="+f, nil); code != 404 {
		t.Fatalf("trashed folder is not browsable: %d", code)
	}

	// the trash shows ONE entry for the folder, with size, dependencies and age
	tr := c.mustOK("GET", "/api/v1/media/trash", nil)
	items := tr["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("trash entries: %v", items)
	}
	it := items[0].(map[string]any)
	if it["type"] != "folder" || it["name"] != "Menus" || it["itemCount"].(float64) != 2 || it["folderCount"].(float64) != 2 || it["size"].(float64) <= 0 {
		t.Fatalf("folder trash item: %v", it)
	}
	usage := it["usage"].([]any)
	if len(usage) != 1 || usage[0].(map[string]any)["ownerName"] != "Happy Hour" || usage[0].(map[string]any)["ownerType"] != "presentation_published" {
		t.Fatalf("dependencies must name the presentation: %v", usage)
	}
	if it["ageDays"].(float64) != 0 {
		t.Fatalf("age: %v", it["ageDays"])
	}

	// restore: same ids, same structure
	res := c.mustOK("POST", "/api/v1/media/trash/restore", map[string]any{"ids": []string{f}})
	if res["assets"].(float64) != 2 || res["folders"].(float64) != 2 {
		t.Fatalf("restore result: %v", res)
	}
	if fo, tr := h.folderOf(a1); fo == nil || *fo != f || tr {
		t.Fatalf("a1 must be back in its folder: %v %v", fo, tr)
	}
	if fo, tr := h.folderOf(a2); fo == nil || *fo != g || tr {
		t.Fatalf("a2 must be back in the sub folder: %v %v", fo, tr)
	}
	root = c.mustOK("GET", "/api/v1/media?folder=root", nil)
	if got := names(root["folders"]); len(got) != 1 || got[0] != "Menus" {
		t.Fatalf("folder back at the root: %v", got)
	}
	if h.count(`SELECT COUNT(*) FROM media_folders WHERE id = ? AND parent_id = ?`, g, f) != 1 {
		t.Fatal("sub folder must be below its parent again")
	}
	if len(c.mustOK("GET", "/api/v1/media/trash", nil)["items"].([]any)) != 0 {
		t.Fatal("trash must be empty after restoring")
	}
	if code, out := c.do("POST", "/api/v1/media/trash/restore", map[string]any{"ids": []string{a1}}); code != 409 || errCode(out) != "not_in_trash" {
		t.Fatalf("restoring something live: %d %v", code, out)
	}
}

func TestRestoreRecreatesMissingParents(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	f := c.mkFolder("F", nil)
	g := c.mkFolder("G", &f)
	a1 := c.mustUpload("one.png", pngBytes(10, 10, 1), &f)["id"].(string)
	a2 := c.mustUpload("two.png", pngBytes(10, 10, 2), &g)["id"].(string)
	c.mustOK("DELETE", "/api/v1/media/folders/"+f, nil)

	// restoring one asset brings back its folder chain F/G, not the other asset
	c.mustOK("POST", "/api/v1/media/trash/restore", map[string]any{"ids": []string{a2}})
	if fo, tr := h.folderOf(a2); fo == nil || *fo != g || tr {
		t.Fatalf("a2 restored into its folder: %v %v", fo, tr)
	}
	if h.count(`SELECT COUNT(*) FROM media_folders WHERE id IN (?, ?) AND trashed_at IS NULL`, f, g) != 2 {
		t.Fatal("the folder chain must be visible again")
	}
	if _, tr := h.folderOf(a1); !tr {
		t.Fatal("a1 stays in the trash")
	}
	// the remaining asset is now a stand-alone trash entry with its original path
	items := c.mustOK("GET", "/api/v1/media/trash", nil)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != a1 {
		t.Fatalf("remaining trash: %v", items)
	}
	path := items[0].(map[string]any)["originalPath"].([]any)
	if len(path) != 1 || path[0].(map[string]any)["name"] != "F" {
		t.Fatalf("original path: %v", path)
	}
	c.mustOK("POST", "/api/v1/media/trash/restore", map[string]any{"ids": []string{a1}})
	if fo, _ := h.folderOf(a1); fo == nil || *fo != f {
		t.Fatalf("a1 back in F: %v", fo)
	}
}

func TestRestoreNameCollisionAndPurgedParent(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	f := c.mkFolder("Posters", nil)
	a := c.mustUpload("a.png", pngBytes(10, 10, 1), &f)["id"].(string)
	// trash the asset alone, then its folder, then purge the folder for good
	c.mustOK("DELETE", "/api/v1/media/"+a, nil)
	c.mustOK("DELETE", "/api/v1/media/folders/"+f, nil)
	c.mustOK("POST", "/api/v1/media/trash/purge", map[string]any{"ids": []string{f}})
	if h.count(`SELECT COUNT(*) FROM media_folders`) != 0 {
		t.Fatal("folder must be purged")
	}
	if h.count(`SELECT COUNT(*) FROM media_assets WHERE id = ?`, a) != 1 {
		t.Fatal("the separately trashed asset must survive the folder purge")
	}
	c.mustOK("POST", "/api/v1/media/trash/restore", map[string]any{"ids": []string{a}})
	if fo, tr := h.folderOf(a); fo != nil || tr {
		t.Fatalf("original folder is gone: restore to the root, got %v %v", fo, tr)
	}

	// name collision: a new live folder took the name meanwhile
	g := c.mkFolder("Menus", nil)
	c.mustOK("DELETE", "/api/v1/media/folders/"+g, nil)
	c.mkFolder("menus", nil)
	c.mustOK("POST", "/api/v1/media/trash/restore", map[string]any{"ids": []string{g}})
	var name string
	_ = h.app.DB.R.QueryRow(`SELECT name FROM media_folders WHERE id = ?`, g).Scan(&name)
	if name != "Menus (restored)" {
		t.Fatalf("restored folder must be renamed, got %q", name)
	}
}

func TestPurgePreviewThenPurge(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	shared := pngBytes(12, 12, 7)
	a1 := c.mustUpload("a.png", pngBytes(12, 12, 1), nil)["id"].(string)
	a2 := c.mustUpload("b.png", shared, nil)["id"].(string)
	a3 := c.mustUpload("c.png", shared, nil)["id"].(string) // same bytes as a2
	h.runJobs()
	c.mustOK("DELETE", "/api/v1/media/"+a1, nil)
	c.mustOK("DELETE", "/api/v1/media/"+a2, nil)

	if code, out := c.do("POST", "/api/v1/media/trash/purge", map[string]any{"ids": []string{a3}}); code != 409 || errCode(out) != "not_in_trash" {
		t.Fatalf("purging a live asset: %d %v", code, out)
	}
	if code, _ := c.do("POST", "/api/v1/media/trash/purge", map[string]any{"ids": []string{"missing"}}); code != 404 {
		t.Fatalf("unknown id: %d", code)
	}

	preview := c.mustOK("POST", "/api/v1/media/trash/purge?preview=1", map[string]any{"ids": []string{a1, a2}})
	if preview["assets"].(float64) != 2 || len(preview["items"].([]any)) != 2 {
		t.Fatalf("preview: %v", preview)
	}
	// a2's original is shared with the live a3, so only a1's blobs are freed
	freed := int64(preview["bytes"].(float64))
	var a1Bytes int64
	_ = h.app.DB.R.QueryRow(`SELECT COALESCE(SUM(size), 0) FROM blobs WHERE sha256 IN
		(SELECT blob_sha256 FROM media_revisions WHERE asset_id = ? UNION SELECT blob_sha256 FROM media_variants WHERE asset_id = ?)`, a1, a1).Scan(&a1Bytes)
	if freed <= 0 || freed > a1Bytes+1<<20 {
		t.Fatalf("preview bytes %d implausible (a1 owns %d)", freed, a1Bytes)
	}
	if h.count(`SELECT COUNT(*) FROM media_assets`) != 3 {
		t.Fatal("preview must not delete anything")
	}
	before := len(h.blobFiles())

	out := c.mustOK("POST", "/api/v1/media/trash/purge", map[string]any{"ids": []string{a1, a2}})
	if out["assets"].(float64) != 2 {
		t.Fatalf("purge result: %v", out)
	}
	if h.count(`SELECT COUNT(*) FROM media_assets`) != 1 || h.count(`SELECT COUNT(*) FROM media_revisions`) != 1 {
		t.Fatal("rows of the purged assets must be gone, including revisions")
	}
	if h.count(`SELECT COUNT(*) FROM media_variants WHERE asset_id IN (?, ?)`, a1, a2) != 0 {
		t.Fatal("variants must be gone")
	}
	// blob files of a1 are collected, the shared original of a2/a3 stays for a3
	after := h.blobFiles()
	if len(after) >= before {
		t.Fatalf("physical files must be collected after a purge: %d -> %d", before, len(after))
	}
	if _, err := os.Stat(h.m.BlobPath(sha(shared))); err != nil {
		t.Fatalf("shared blob must survive while a3 uses it: %v", err)
	}
	if h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'media.purged'`) != 2 {
		t.Fatal("purges are audited")
	}
}

func TestPurgeBlockedByProductImage(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	a := c.mustUpload("beer.png", pngBytes(12, 12, 1), nil)["id"].(string)
	h.exec(`INSERT INTO categories(id, name, created_at, updated_at) VALUES ('cat', 'Beer', 'x', 'x')`)
	h.exec(`INSERT INTO products(id, category_id, name, type, price_amount, image_asset_id, created_at, updated_at) VALUES ('pr', 'cat', 'Pils', 'drink', 350, ?, 'x', 'x')`, a)
	c.mustOK("DELETE", "/api/v1/media/"+a, nil)

	tr := c.mustOK("GET", "/api/v1/media/trash", nil)["items"].([]any)[0].(map[string]any)
	if tr["blocked"] != true {
		t.Fatalf("trash item must show it is blocked: %v", tr)
	}
	usage := tr["usage"].([]any)
	if len(usage) != 1 || usage[0].(map[string]any)["ownerType"] != "product" || usage[0].(map[string]any)["ownerName"] != "Pils" {
		t.Fatalf("dependency: %v", usage)
	}
	pv := c.mustOK("POST", "/api/v1/media/trash/purge?preview=1", map[string]any{"ids": []string{a}})
	if len(pv["blocked"].([]any)) != 1 {
		t.Fatalf("preview must list the blocker: %v", pv)
	}
	code, out := c.do("POST", "/api/v1/media/trash/purge", map[string]any{"ids": []string{a}})
	if code != 409 || errCode(out) != "asset_in_use" {
		t.Fatalf("purge must be refused: %d %v", code, out)
	}
	if h.count(`SELECT COUNT(*) FROM media_assets`) != 1 {
		t.Fatal("nothing may be deleted")
	}
	// the retention job skips it quietly
	h.exec(`UPDATE media_assets SET trashed_at = '2000-01-01T00:00:00Z'`)
	s := h.app.Settings.Get()
	s.TrashRetentionDays = 7
	h.app.Settings.Apply(s)
	h.m.purgeExpired(context.Background())
	if h.count(`SELECT COUNT(*) FROM media_assets`) != 1 {
		t.Fatal("retention must not purge blocked items")
	}
}

func TestTrashRetention(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	old := c.mustUpload("old.png", pngBytes(12, 12, 1), nil)["id"].(string)
	fresh := c.mustUpload("fresh.png", pngBytes(12, 12, 2), nil)["id"].(string)
	c.mustOK("DELETE", "/api/v1/media/"+old, nil)
	c.mustOK("DELETE", "/api/v1/media/"+fresh, nil)
	h.exec(`UPDATE media_assets SET trashed_at = ? WHERE id = ?`, time.Now().UTC().Add(-40*24*time.Hour).Format(time.RFC3339Nano), old)

	s := h.app.Settings.Get()
	s.TrashRetentionDays = 0 // never
	h.app.Settings.Apply(s)
	h.m.purgeExpired(context.Background())
	if h.count(`SELECT COUNT(*) FROM media_assets`) != 2 {
		t.Fatal("retention 0 means never")
	}
	s.TrashRetentionDays = 30
	h.app.Settings.Apply(s)
	h.m.purgeExpired(context.Background())
	if h.count(`SELECT COUNT(*) FROM media_assets WHERE id = ?`, old) != 0 || h.count(`SELECT COUNT(*) FROM media_assets WHERE id = ?`, fresh) != 1 {
		t.Fatal("only the item older than the retention is purged")
	}
	if h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'media.purged' AND username = 'system'`) != 1 {
		t.Fatal("system purge must be audited")
	}
}

func TestTrashBulkAndEmptyTrash(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	f := c.mkFolder("F", nil)
	a1 := c.mustUpload("a.png", pngBytes(12, 12, 1), nil)["id"].(string)
	a2 := c.mustUpload("b.png", pngBytes(12, 12, 2), &f)["id"].(string)
	out := c.mustOK("POST", "/api/v1/media/trash", map[string]any{"ids": []string{a1, "unknown"}})
	if out["trashed"].(float64) != 1 {
		t.Fatalf("bulk trash: %v", out)
	}
	c.mustOK("DELETE", "/api/v1/media/folders/"+f, nil)
	// idempotent
	if code, _ := c.do("DELETE", "/api/v1/media/"+a1, nil); code != 204 {
		t.Fatalf("second delete: %d", code)
	}
	if code, _ := c.do("DELETE", "/api/v1/media/unknown", nil); code != 404 {
		t.Fatalf("unknown delete: %d", code)
	}
	out = c.mustOK("POST", "/api/v1/media/trash/purge", map[string]any{"all": true})
	if out["assets"].(float64) != 2 {
		t.Fatalf("empty trash: %v", out)
	}
	if h.count(`SELECT COUNT(*) FROM media_assets`) != 0 || h.count(`SELECT COUNT(*) FROM media_folders`) != 0 || len(h.blobFiles()) != 0 {
		t.Fatalf("everything must be gone: assets=%d folders=%d files=%d (a2=%s)", h.count(`SELECT COUNT(*) FROM media_assets`),
			h.count(`SELECT COUNT(*) FROM media_folders`), len(h.blobFiles()), a2)
	}
}
