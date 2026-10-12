package media

import (
	"sync"
	"testing"
	"time"

	"opensignage/internal/server/core"
)

func idSet(list any) map[string]bool {
	out := map[string]bool{}
	for _, x := range list.([]any) {
		out[x.(map[string]any)["id"].(string)] = true
	}
	return out
}

func names(list any) []string {
	var out []string
	for _, x := range list.([]any) {
		out = append(out, x.(map[string]any)["name"].(string))
	}
	return out
}

func TestFolderCyclePrevention(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	a := c.mkFolder("A", nil)
	b := c.mkFolder("B", &a)
	cc := c.mkFolder("C", &b)

	for name, target := range map[string]string{"into itself": a, "into child": b, "into grandchild": cc} {
		code, out := c.do("PATCH", "/api/v1/media/folders/"+a, map[string]any{"parentId": target})
		if code != 409 || errCode(out) != "folder_cycle" {
			t.Errorf("%s: expected 409 folder_cycle, got %d %v", name, code, out)
		}
	}
	// the tree is unchanged
	if h.count(`SELECT COUNT(*) FROM media_folders WHERE id = ? AND parent_id IS NULL`, a) != 1 {
		t.Fatal("A must still be at the root")
	}
	// legal moves: C to root, then A below C
	c.mustOK("PATCH", "/api/v1/media/folders/"+cc, map[string]any{"parentId": nil})
	c.mustOK("PATCH", "/api/v1/media/folders/"+a, map[string]any{"parentId": cc})
	if h.count(`SELECT COUNT(*) FROM media_folders WHERE id = ? AND parent_id = ?`, a, cc) != 1 {
		t.Fatal("A should be below C now")
	}
	// B is now C → A → B. Moving C under B would close the loop.
	if code, out := c.do("PATCH", "/api/v1/media/folders/"+cc, map[string]any{"parentId": b}); code != 409 || errCode(out) != "folder_cycle" {
		t.Fatalf("indirect cycle: %d %v", code, out)
	}
	if code, _ := c.do("PATCH", "/api/v1/media/folders/"+a, map[string]any{"parentId": "does-not-exist"}); code != 422 {
		t.Fatalf("unknown parent: %d", code)
	}
	if code, _ := c.do("PATCH", "/api/v1/media/folders/"+a, map[string]any{"parentId": ""}); code != 422 {
		t.Fatalf("empty parent must be rejected, not treated as root: %d", code)
	}
}

func TestConcurrentMovesCannotCreateCycle(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	for round := 0; round < 10; round++ {
		a := c.mkFolder("A"+itoa(round), nil)
		b := c.mkFolder("B"+itoa(round), nil)
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i, mv := range [][2]string{{a, b}, {b, a}} {
			wg.Add(1)
			go func(i int, id, parent string) {
				defer wg.Done()
				codes[i], _ = c.do("PATCH", "/api/v1/media/folders/"+id, map[string]any{"parentId": parent})
			}(i, mv[0], mv[1])
		}
		wg.Wait()
		if codes[0] == 200 && codes[1] == 200 {
			t.Fatalf("round %d: both moves succeeded, that is a cycle", round)
		}
		var pa, pb *string
		_ = h.app.DB.R.QueryRow(`SELECT parent_id FROM media_folders WHERE id = ?`, a).Scan(&pa)
		_ = h.app.DB.R.QueryRow(`SELECT parent_id FROM media_folders WHERE id = ?`, b).Scan(&pb)
		if pa != nil && pb != nil {
			t.Fatalf("round %d: cycle in the database", round)
		}
	}
}

func itoa(n int) string { return string(rune('a' + n)) }

func TestFolderNamesAndValidation(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	a := c.mkFolder("Menu", nil)
	if code, out := c.do("POST", "/api/v1/media/folders", map[string]any{"name": "menu"}); code != 409 || errCode(out) != "name_taken" {
		t.Fatalf("sibling name (case-insensitive): %d %v", code, out)
	}
	// the same name below another parent is fine
	c.mkFolder("Menu", &a)
	for _, bad := range []string{"", "   ", "a/b", "..", "x\\y"} {
		if code, _ := c.do("POST", "/api/v1/media/folders", map[string]any{"name": bad}); code != 422 {
			t.Errorf("name %q must be rejected, got %d", bad, code)
		}
	}
	if code, _ := c.do("POST", "/api/v1/media/folders", map[string]any{"name": "ok", "parentId": "nope"}); code != 422 {
		t.Errorf("unknown parent: %d", code)
	}
	if code, _ := c.do("PATCH", "/api/v1/media/folders/"+a, map[string]any{"name": "Drinks"}); code != 200 {
		t.Errorf("rename: %d", code)
	}
	if h.count(`SELECT COUNT(*) FROM audit_log WHERE action LIKE 'media.folder_%'`) < 3 {
		t.Error("folder changes must be audited")
	}
}

func TestRootListingVersusSearch(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	fa := c.mkFolder("Sub", nil)
	fb := c.mkFolder("Deeper", &fa)
	rootAsset := c.mustUpload("root-item.png", pngBytes(10, 10, 1), nil)
	subAsset := c.mustUpload("sub-item.png", pngBytes(10, 10, 2), &fa)
	deepAsset := c.mustUpload("deep-item.png", pngBytes(10, 10, 3), &fb)

	// "folder=root" is ONLY the top level
	out := c.mustOK("GET", "/api/v1/media?folder=root", nil)
	if got := idSet(out["assets"]); len(got) != 1 || !got[rootAsset["id"].(string)] {
		t.Fatalf("root must list only its own file: %v", names(out["assets"]))
	}
	if got := names(out["folders"]); len(got) != 1 || got[0] != "Sub" {
		t.Fatalf("root must list only top-level folders: %v", got)
	}
	if len(out["breadcrumb"].([]any)) != 0 {
		t.Fatalf("root has no breadcrumb: %v", out["breadcrumb"])
	}
	// search without a folder parameter covers every folder
	out = c.mustOK("GET", "/api/v1/media?q=item", nil)
	if got := idSet(out["assets"]); len(got) != 3 {
		t.Fatalf("search must cover all folders: %v", names(out["assets"]))
	}
	// search inside the root only
	out = c.mustOK("GET", "/api/v1/media?folder=root&q=item", nil)
	if got := idSet(out["assets"]); len(got) != 1 || !got[rootAsset["id"].(string)] {
		t.Fatalf("scoped search: %v", names(out["assets"]))
	}
	// folder search by name
	out = c.mustOK("GET", "/api/v1/media?q=deep", nil)
	if got := names(out["folders"]); len(got) != 1 || got[0] != "Deeper" {
		t.Fatalf("folder name search: %v", got)
	}
	// a folder shows its children, breadcrumb and recursive counters
	out = c.mustOK("GET", "/api/v1/media?folder="+fa, nil)
	if got := idSet(out["assets"]); len(got) != 1 || !got[subAsset["id"].(string)] {
		t.Fatalf("folder listing: %v", names(out["assets"]))
	}
	bc := out["breadcrumb"].([]any)
	if len(bc) != 1 || bc[0].(map[string]any)["name"] != "Sub" {
		t.Fatalf("breadcrumb: %v", bc)
	}
	out = c.mustOK("GET", "/api/v1/media?folder=root", nil)
	sub := out["folders"].([]any)[0].(map[string]any)
	if sub["itemCount"].(float64) != 2 || sub["folderCount"].(float64) != 1 || sub["totalSize"].(float64) <= 0 {
		t.Fatalf("folder counters must include the subtree: %v", sub)
	}
	out = c.mustOK("GET", "/api/v1/media?folder="+fb, nil)
	if bc := out["breadcrumb"].([]any); len(bc) != 2 {
		t.Fatalf("nested breadcrumb: %v", bc)
	}
	if !idSet(out["assets"])[deepAsset["id"].(string)] {
		t.Fatal("deep asset")
	}

	// an empty folder parameter is a client bug and must never mean "everything" or "root"
	if code, out := c.do("GET", "/api/v1/media?folder=", nil); code != 422 {
		t.Fatalf("empty folder param: %d %v", code, out)
	}
	if code, _ := c.do("GET", "/api/v1/media?folder=unknown", nil); code != 404 {
		t.Fatalf("unknown folder: %d", code)
	}
	if code, _ := c.do("GET", "/api/v1/media?kind=document", nil); code != 422 {
		t.Fatalf("bad kind: %d", code)
	}
	out = c.mustOK("GET", "/api/v1/media?kind=video", nil)
	if len(out["assets"].([]any)) != 0 {
		t.Fatal("kind filter")
	}
}

func TestRenameAndMoveKeepIdentity(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	f := c.mkFolder("Target", nil)
	a := c.mustUpload("before.png", pngBytes(10, 10, 4), nil)
	id := a["id"].(string)
	h.runJobs()
	revBefore := h.count(`SELECT revision FROM media_assets WHERE id = ?`, id)

	sub := h.app.Bus.Subscribe(64)
	defer sub.Close()
	out := c.mustOK("PATCH", "/api/v1/media/"+id, map[string]any{"name": "after.png", "folderId": f})
	if out["id"] != id || out["name"] != "after.png" || out["folderId"] != f || int(out["revision"].(float64)) != revBefore {
		t.Fatalf("rename+move result: %v", out)
	}
	if h.count(`SELECT COUNT(*) FROM media_jobs`) != 1 {
		t.Fatal("rename/move must not queue any processing")
	}
	// no content event: nothing a screen shows has changed, so no deployment rebuild
	deadline := time.After(200 * time.Millisecond)
loop:
	for {
		select {
		case e := <-sub.C:
			if e.Topic == core.TopicContent {
				t.Fatalf("rename/move must not publish a content event: %+v", e)
			}
		case <-deadline:
			break loop
		}
	}
	// back to the root with an explicit null
	out = c.mustOK("PATCH", "/api/v1/media/"+id, map[string]any{"folderId": nil})
	if out["folderId"] != nil || out["name"] != "after.png" {
		t.Fatalf("move to root: %v", out)
	}
	if code, _ := c.do("PATCH", "/api/v1/media/"+id, map[string]any{"name": " "}); code != 422 {
		t.Fatalf("empty name: %d", code)
	}
	if code, _ := c.do("PATCH", "/api/v1/media/"+id, map[string]any{"folderId": "nope"}); code != 422 {
		t.Fatalf("unknown folder: %d", code)
	}
	if h.count(`SELECT COUNT(*) FROM audit_log WHERE action IN ('media.renamed_moved', 'media.moved', 'media.renamed')`) != 2 {
		t.Fatal("audit entries for rename/move")
	}
}
