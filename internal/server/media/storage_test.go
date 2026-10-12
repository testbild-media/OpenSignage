package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorageSummary(t *testing.T) {
	h := newHarness(t)
	admin := h.login("admin")
	op := h.login("operator")
	if code, _ := op.do("GET", "/api/v1/storage", nil); code != 403 {
		t.Fatalf("storage is admin only: %d", code)
	}

	h.tc.probe = Probe{Container: "mov,mp4", HasVideo: true, VideoCodec: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080, FPS: 25, HasAudio: true, AudioCodec: "aac", DurationMs: 4000}
	f := admin.mkFolder("Videos", nil)
	img := admin.mustUpload("pic.png", pngBytes(50, 50, 1), nil)["id"].(string)
	vid := admin.mustUpload("clip.mp4", mp4Bytes("storage"), &f)["id"].(string)
	trashed := admin.mustUpload("old.png", pngBytes(60, 60, 2), nil)["id"].(string)
	h.runJobs() // the fake probe describes a 1080p H.264 video; for the PNGs it is just a large image
	admin.mustOK("DELETE", "/api/v1/media/"+trashed, nil)
	// custom font
	if code, out := admin.fontUpload(goodFont("Disk Font"), "f.ttf", fakeSFNT("\x00\x01\x00\x00", 2000)); code != 200 {
		t.Fatalf("font: %d %v", code, out)
	}
	_ = os.MkdirAll(filepath.Join(h.dir, "logs"), 0o750)
	_ = os.WriteFile(filepath.Join(h.dir, "logs", "server.log"), []byte(strings.Repeat("l", 1000)), 0o640)
	_ = os.MkdirAll(h.m.tmpDir("uploads"), 0o750)
	_ = os.WriteFile(h.m.tmpDir("uploads", "x.part"), []byte(strings.Repeat("t", 500)), 0o640)
	// an orphan blob waits for the collector
	h.m.gcGrace = time.Hour
	h.seedBlob(data(777, "orphan"))

	h.m.diskFn = func(string) (uint64, uint64, error) { return 100 << 30, 15 << 30, nil } // 15 % free
	out := admin.mustOK("GET", "/api/v1/storage", nil)
	disk := out["disk"].(map[string]any)
	if disk["totalBytes"].(float64) != float64(100<<30) || disk["freeBytes"].(float64) != float64(15<<30) || disk["usedBytes"].(float64) != float64(85<<30) {
		t.Fatalf("disk: %v", disk)
	}
	if disk["level"] != "warning" || disk["freePercent"].(float64) != 15 {
		t.Fatalf("15%% free must warn: %v", disk)
	}
	bd := out["breakdown"].(map[string]any)
	for _, k := range []string{"images", "videoOriginals", "videoVariants", "thumbnails", "trash", "database", "logs", "tmp", "fonts", "unreferenced"} {
		if bd[k].(float64) <= 0 {
			t.Errorf("breakdown.%s should be positive: %v", k, bd)
		}
	}
	if bd["logs"].(float64) != 1000 || bd["tmp"].(float64) < 500 {
		t.Errorf("logs/tmp: %v %v", bd["logs"], bd["tmp"])
	}
	if bd["unreferenced"].(float64) != float64(len("orphan")+777) {
		t.Errorf("unreferenced: %v", bd["unreferenced"])
	}
	largest := out["largest"].([]any)
	if len(largest) < 3 {
		t.Fatalf("largest: %v", largest)
	}
	for i := 1; i < len(largest); i++ {
		if largest[i].(map[string]any)["size"].(float64) > largest[i-1].(map[string]any)["size"].(float64) {
			t.Fatal("largest must be sorted by size")
		}
	}
	folders := out["folders"].([]any)
	if len(folders) != 1 || folders[0].(map[string]any)["name"] != "Videos" || folders[0].(map[string]any)["size"].(float64) <= 0 || folders[0].(map[string]any)["count"].(float64) != 1 {
		t.Fatalf("folder sizes: %v", folders)
	}
	root := out["root"].(map[string]any)
	if root["count"].(float64) != 1 { // only pic.png (old.png is in the trash)
		t.Fatalf("root: %v", root)
	}
	_, _ = img, vid

	// thresholds
	for free, want := range map[uint64]string{50: "ok", 19: "warning", 9: "critical"} {
		h.m.diskFn = func(string) (uint64, uint64, error) { return 100, free, nil }
		if got := admin.mustOK("GET", "/api/v1/storage", nil)["disk"].(map[string]any)["level"]; got != want {
			t.Errorf("%d%% free → %v, want %s", free, got, want)
		}
	}
}

func TestUnusedMediaAndCleanup(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	h.tc.probe = Probe{Container: "mov,mp4", HasVideo: true, VideoCodec: "hevc", PixFmt: "yuv420p", Width: 1920, Height: 1080, FPS: 25, DurationMs: 3000}

	unused := c.mustUpload("unused.mp4", mp4Bytes("unused"), nil)["id"].(string)
	used := c.mustUpload("used.mp4", mp4Bytes("used"), nil)["id"].(string)
	product := c.mustUpload("product.mp4", mp4Bytes("product"), nil)["id"].(string)
	h.runJobs()
	h.exec(`INSERT INTO content_refs(asset_id, owner_type, owner_id) VALUES (?, 'playlist_draft', 'pl1')`, used)
	h.exec(`INSERT INTO categories(id, name, created_at, updated_at) VALUES ('c1', 'C', 'x', 'x')`)
	h.exec(`INSERT INTO products(id, category_id, name, type, price_amount, image_asset_id, created_at, updated_at) VALUES ('pr1', 'c1', 'P', 'food', 100, ?, 'x', 'x')`, product)

	out := c.mustOK("GET", "/api/v1/media/unused", nil)
	assets := out["assets"].([]any)
	if len(assets) != 1 || assets[0].(map[string]any)["id"] != unused {
		t.Fatalf("only the asset without usage and product image: %v", assets)
	}
	first := assets[0].(map[string]any)
	if first["variantCount"].(float64) != 2 || first["variantBytes"].(float64) <= 0 {
		t.Fatalf("variant info: %v", first)
	}
	// filters
	if n := len(c.mustOK("GET", "/api/v1/media/unused?olderThanDays=1", nil)["assets"].([]any)); n != 0 {
		t.Fatalf("fresh asset is not older than 1 day: %d", n)
	}
	h.exec(`UPDATE media_assets SET updated_at = '2020-01-01T00:00:00Z'`)
	if n := len(c.mustOK("GET", "/api/v1/media/unused?olderThanDays=30", nil)["assets"].([]any)); n != 1 {
		t.Fatalf("old asset: %d", n)
	}
	if n := len(c.mustOK("GET", "/api/v1/media/unused?kind=image", nil)["assets"].([]any)); n != 0 {
		t.Fatalf("kind filter: %d", n)
	}
	if code, _ := c.do("GET", "/api/v1/media/unused?olderThanDays=abc", nil); code != 422 {
		t.Fatalf("bad number: %d", code)
	}

	filesBefore := len(h.blobFiles())
	// preview, then delete variants except the thumbnail
	pv := c.mustOK("POST", "/api/v1/media/unused/variants/delete?preview=1", map[string]any{"ids": []string{unused, used}})
	if pv["assets"].(float64) != 1 || pv["bytes"].(float64) <= 0 || len(pv["blocked"].([]any)) != 1 {
		t.Fatalf("preview (used asset is blocked): %v", pv)
	}
	if h.count(`SELECT COUNT(*) FROM media_variants WHERE asset_id = ?`, unused) != 4 {
		t.Fatal("preview deletes nothing")
	}
	res := c.mustOK("POST", "/api/v1/media/unused/variants/delete", map[string]any{"ids": []string{unused, used}})
	if res["assets"].(float64) != 1 {
		t.Fatalf("delete: %v", res)
	}
	// thumbnail, v1080 (needed to play it) and the original stay; v720 and poster are optimisations
	if got := strings.Join(h.variantNames(unused, 1), ","); got != "thumb,v1080" {
		t.Fatalf("variants after cleanup: %s", got)
	}
	if st, _ := h.assetStatus(unused); st != "ready" {
		t.Fatalf("a cleaned asset stays playable: %s", st)
	}
	if h.count(`SELECT COUNT(*) FROM media_variants WHERE asset_id = ?`, used) != 4 {
		t.Fatal("variants of used media are untouched")
	}
	if len(h.blobFiles()) >= filesBefore {
		t.Fatalf("variant files must be collected: %d → %d", filesBefore, len(h.blobFiles()))
	}
	// re-creatable
	c.mustOK("POST", "/api/v1/media/"+unused+"/reprocess", nil)
	h.runJobs()
	if len(h.variantNames(unused, 1)) != 4 {
		t.Fatalf("reprocess restores variants: %v", h.variantNames(unused, 1))
	}

	// old revisions
	h.tc.probe = Probe{}
	img := c.mustUpload("logo.png", pngBytes(40, 40, 1), nil)["id"].(string)
	h.runJobs()
	for i := uint8(2); i <= 3; i++ {
		if code, out := c.upload("logo.png", pngBytes(40, 40, i), nil, &img); code != 200 {
			t.Fatalf("replace: %d %v", code, out)
		}
		h.runJobs()
	}
	old := c.mustOK("GET", "/api/v1/media/unused", nil)["oldRevisions"].([]any)
	if len(old) != 2 {
		t.Fatalf("two old revisions expected: %v", old)
	}
	pv = c.mustOK("POST", "/api/v1/media/old-revisions/delete?preview=1", map[string]any{"ids": []string{img}})
	if pv["assets"].(float64) != 1 || pv["items"].(float64) != 2 || pv["bytes"].(float64) <= 0 {
		t.Fatalf("revision preview: %v", pv)
	}
	if h.count(`SELECT COUNT(*) FROM media_revisions WHERE asset_id = ?`, img) != 3 {
		t.Fatal("preview deletes nothing")
	}
	c.mustOK("POST", "/api/v1/media/old-revisions/delete", map[string]any{"ids": []string{img}})
	if h.count(`SELECT COUNT(*) FROM media_revisions WHERE asset_id = ?`, img) != 1 || h.count(`SELECT revision FROM media_assets WHERE id = ?`, img) != 3 {
		t.Fatal("only the current revision remains, the asset keeps its revision number")
	}
	if h.count(`SELECT COUNT(*) FROM media_variants WHERE asset_id = ? AND revision < 3`, img) != 0 {
		t.Fatal("variants of old revisions are removed with them")
	}
	if h.count(`SELECT COUNT(*) FROM audit_log WHERE action IN ('media.revisions_deleted', 'media.variants_deleted')`) != 2 {
		t.Fatal("cleanups are audited")
	}
	// the current revision's blob is still served
	if res, _ := c.req("GET", "/api/v1/media/"+img+"/file?rev=3", nil, nil); res.StatusCode != 200 {
		t.Fatalf("current revision: %d", res.StatusCode)
	}
}

func TestAuditAndRolesOnMutations(t *testing.T) {
	h := newHarness(t)
	op := h.login("operator")
	user := h.login("user")
	a := op.mustUpload("x.png", pngBytes(10, 10, 1), nil)["id"].(string)
	for _, tc := range []struct{ method, path string }{
		{"PATCH", "/api/v1/media/" + a}, {"DELETE", "/api/v1/media/" + a}, {"GET", "/api/v1/media/" + a},
		{"POST", "/api/v1/media/folders"}, {"GET", "/api/v1/media/trash"}, {"POST", "/api/v1/media/trash/purge"},
		{"POST", "/api/v1/media/trash/restore"}, {"POST", "/api/v1/media/" + a + "/reprocess"}, {"GET", "/api/v1/media/unused"},
	} {
		if code, _ := user.do(tc.method, tc.path, map[string]any{}); code != 403 {
			t.Errorf("%s %s as user: %d, want 403", tc.method, tc.path, code)
		}
	}
	// missing CSRF token on a mutation
	anon := &client{h: h, cookie: op.cookie, csrf: ""}
	if code, _ := anon.do("POST", "/api/v1/media/folders", map[string]any{"name": "x"}); code != 403 {
		t.Errorf("csrf: %d", code)
	}
	// not logged in at all
	none := &client{h: h}
	if code, _ := none.do("GET", "/api/v1/media", nil); code != 401 {
		t.Errorf("anonymous: %d", code)
	}
}

// References of deleted (archived) playlists and presentations do not make a file "used":
// it shows up as unused and its usage list leaves the deleted owner out.
func TestDeletedOwnersDoNotCountAsUsage(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	h.tc.probe = Probe{Container: "mov,mp4", HasVideo: true, VideoCodec: "h264", PixFmt: "yuv420p", Width: 1280, Height: 720, FPS: 25, DurationMs: 3000}
	id := c.mustUpload("clip.mp4", mp4Bytes("clip"), nil)["id"].(string)
	h.runJobs()
	h.exec(`INSERT INTO playlists(id, name, draft, created_at, updated_at) VALUES ('pl1', 'Summer', '{}', 'x', 'x')`)
	h.exec(`INSERT INTO content_refs(asset_id, owner_type, owner_id) VALUES (?, 'playlist_draft', 'pl1')`, id)

	unusedIDs := func() []string {
		var out []string
		for _, a := range c.mustOK("GET", "/api/v1/media/unused", nil)["assets"].([]any) {
			out = append(out, a.(map[string]any)["id"].(string))
		}
		return out
	}
	if got := unusedIDs(); len(got) != 0 {
		t.Fatalf("used by an active playlist: %v", got)
	}
	if u := c.mustOK("GET", "/api/v1/media/"+id, nil)["usage"].([]any); len(u) != 1 {
		t.Fatalf("usage of an active playlist: %v", u)
	}
	h.exec(`UPDATE playlists SET archived_at = 'x' WHERE id = 'pl1'`)
	if got := unusedIDs(); len(got) != 1 || got[0] != id {
		t.Fatalf("a deleted playlist does not use the file: %v", got)
	}
	if u := c.mustOK("GET", "/api/v1/media/"+id, nil)["usage"].([]any); len(u) != 0 {
		t.Fatalf("usage must leave the deleted playlist out: %v", u)
	}
}
