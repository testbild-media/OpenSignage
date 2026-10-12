package app

import (
	"strings"
	"testing"

	"opensignage/internal/contracts"
)

// Regression tests for review findings (reference integrity, revisions, schedule
// validation, zone visibility). Helpers carry the rv… prefix.

func rvErrParams(out map[string]any) map[string]any { return asMap(asMap(out["error"])["params"]) }

func rvProductList(id, kind string, ids ...string) contracts.Element {
	return contracts.Element{ID: id, Type: contracts.ElProductList, X: 0, Y: 0, W: 600, H: 400, Opacity: 1,
		Source: &contracts.ProductSource{Kind: kind, IDs: ids}, Fields: &contracts.ProductFields{Name: true, Price: true},
		Columns: 1, Scale: 1, PriceAlign: "right"}
}

// rvPublishedPres creates a presentation with the given slides and publishes it.
func (e *catEnv) rvPublishedPres(name string, slides ...contracts.Slide) (string, int64) {
	e.t.Helper()
	id, created := e.newPres(name, 1280, 720)
	doc := cntDoc(e.t, created)
	doc.Slides = slides
	rev := e.savePres(id, 1, doc)
	e.expect(e.admin, "POST", "/api/v1/presentations/"+id+"/publish", nil, 200, "")
	return id, rev
}

// rvPlaylist creates a playlist with the given items and publishes it when asked.
func (e *catEnv) rvPlaylist(name string, publish bool, items ...map[string]any) string {
	e.t.Helper()
	id := asStr(e.expect(e.admin, "POST", "/api/v1/playlists", map[string]any{"name": name}, 200, "")["id"])
	e.expect(e.admin, "PUT", "/api/v1/playlists/"+id+"/draft", map[string]any{"revision": 1,
		"doc": map[string]any{"id": id, "name": name, "items": items}}, 200, "")
	if publish {
		e.expect(e.admin, "POST", "/api/v1/playlists/"+id+"/publish", nil, 200, "")
	}
	return id
}

func rvItem(id, kind, ref string, extra map[string]any) map[string]any {
	m := map[string]any{"id": id, "kind": kind, "refId": ref, "play": map[string]any{"mode": "duration", "ms": 5000}}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func (e *catEnv) rvExec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.srv.App.DB.W.Exec(q, args...); err != nil {
		e.t.Fatal(err)
	}
}

// Finding 2: publishing a presentation must not silently drop slides that published
// playlists play as single slides.
func TestPublishKeepsSlidesOfPublishedPlaylists(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	presID, rev := e.rvPublishedPres("Menu", cntSlide("sa", "Intro", 5000), cntSlide("sb", "Offers", 5000))
	slideItem := func(id, slide string) map[string]any {
		return rvItem(id, "slide", slide, map[string]any{"presentationId": presID})
	}
	e.rvPlaylist("Bar loop", true, slideItem("i1", "sb"))
	e.rvPlaylist("Draft only", false, slideItem("i1", "sa")) // drafts are checked when they are published

	cur := e.expect(c, "GET", "/api/v1/presentations/"+presID, nil, 200, "")
	doc := cntDoc(t, cur)
	doc.Slides = []contracts.Slide{doc.Slides[0]} // drop "Offers" (sb)
	rev = e.savePres(presID, rev, doc)
	out := e.expect(c, "POST", "/api/v1/presentations/"+presID+"/publish", nil, 409, "slide_in_use")
	p := rvErrParams(out)
	if pl := toStrings(p["playlists"]); len(pl) != 1 || pl[0] != "Bar loop" {
		t.Errorf("slide_in_use must name the published playlist: %v", p)
	}
	if sl := toStrings(p["slides"]); len(sl) != 1 || sl[0] != "sb" {
		t.Errorf("slide_in_use must name the slide id: %v", p)
	}
	if sn := toStrings(p["slideNames"]); len(sn) != 1 || sn[0] != "Offers" {
		t.Errorf("slide_in_use must carry the slide name: %v", p)
	}
	if v := asNum(e.expect(c, "GET", "/api/v1/presentations/"+presID, nil, 200, "")["publishedVersion"]); v != 1 {
		t.Fatalf("nothing may be published: version %v", v)
	}

	// Removing the slide that only the draft playlist uses is fine; keeping sb too.
	doc.Slides = []contracts.Slide{cntSlide("sb", "Offers", 5000)}
	rev = e.savePres(presID, rev, doc)
	e.expect(c, "POST", "/api/v1/presentations/"+presID+"/publish", nil, 200, "")
	_ = rev
}

// Finding 3, as decided: deleted (archived) presentations and playlists are invisible to
// users and never block anything; restoring one re-validates its published version.
func TestDeletedOwnersDoNotBlockAndRestoreRevalidates(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	cat := e.category("Drinks", "")
	prod := e.product("Cola", cat, 300)
	img := e.seedAsset("image")

	slide := cntSlide("s1", "Main", 5000)
	slide.Elements = []contracts.Element{rvProductList("pl1", "products", prod), cntMediaElem("im", contracts.ElImage, img)}
	presID, _ := e.rvPublishedPres("Menu", slide)

	// An active playlist blocks deleting the presentation; a deleted playlist does not.
	plID := e.rvPlaylist("Loop", true, rvItem("i1", "presentation", presID, nil), rvItem("i2", "slide", "s1", map[string]any{"presentationId": presID}))
	arch := e.expect(c, "POST", "/api/v1/presentations/"+presID+"/archive", nil, 409, "presentation_in_use")
	if names := toStrings(rvErrParams(arch)["playlists"]); len(names) != 1 || names[0] != "Loop" {
		t.Errorf("the playlist must be named: %v", arch)
	}
	e.expect(c, "DELETE", "/api/v1/playlists/"+plID, nil, 204, "")
	e.expect(c, "POST", "/api/v1/presentations/"+presID+"/archive", nil, 204, "")

	// The playlist cannot come back while the presentation it plays is deleted.
	out := e.expect(c, "POST", "/api/v1/playlists/"+plID+"/restore", nil, 409, "restore_references_missing")
	if refs := toStrings(rvErrParams(out)["references"]); len(refs) != 1 || refs[0] != "Menu" {
		t.Errorf("the deleted presentation must be named: %v", out)
	}

	// Restoring the presentation re-validates its published version: the image went to
	// the trash meanwhile → 409, back from the trash → restored.
	e.rvExec(`UPDATE media_assets SET trashed_at = 'now' WHERE id = ?`, img)
	out = e.expect(c, "POST", "/api/v1/presentations/"+presID+"/restore", nil, 409, "restore_references_missing")
	if refs := toStrings(rvErrParams(out)["references"]); len(refs) != 1 || !strings.HasPrefix(refs[0], "image-") {
		t.Errorf("the missing image must be named: %v", out)
	}
	e.rvExec(`UPDATE media_assets SET trashed_at = NULL WHERE id = ?`, img)
	restored := e.expect(c, "POST", "/api/v1/presentations/"+presID+"/restore", nil, 200, "")
	if restored["archived"] != false || len(asList(restored["warnings"])) != 0 {
		t.Fatalf("restored %v", restored)
	}
	e.expect(c, "POST", "/api/v1/playlists/"+plID+"/restore", nil, 200, "")
	e.expect(c, "DELETE", "/api/v1/playlists/"+plID, nil, 204, "")

	// The slide the deleted playlist plays may disappear (deleted playlists are not
	// distributed) – but then the playlist cannot come back as it is.
	cur := e.expect(c, "GET", "/api/v1/presentations/"+presID, nil, 200, "")
	doc := cntDoc(t, cur)
	doc.Slides = []contracts.Slide{cntSlide("s2", "Other", 5000)}
	e.savePres(presID, int64(asNum(cur["draftRevision"])), doc)
	e.expect(c, "POST", "/api/v1/presentations/"+presID+"/publish", nil, 200, "")
	out = e.expect(c, "POST", "/api/v1/playlists/"+plID+"/restore", nil, 409, "restore_references_missing")
	if refs := toStrings(rvErrParams(out)["references"]); len(refs) != 1 || refs[0] != "Menu: s1" {
		t.Errorf("the missing slide must be named: %v", out)
	}
	if got := e.expect(c, "GET", "/api/v1/playlists/"+plID, nil, 200, ""); got["archived"] != true {
		t.Fatalf("a refused restore leaves the playlist deleted: %v", got)
	}

	// An active presentation blocks deleting a product it shows, a deleted one does not;
	// restoring that presentation afterwards names the missing product.
	slide2 := cntSlide("s3", "Cola", 5000)
	slide2.Elements = []contracts.Element{rvProductList("pl2", "products", prod)}
	pres2, _ := e.rvPublishedPres("Cola menu", slide2)
	e.expect(c, "POST", "/api/v1/products/"+prod+"/archive", nil, 200, "")
	in := e.expect(c, "DELETE", "/api/v1/products/"+prod, nil, 409, "product_in_use")
	if names := toStrings(rvErrParams(in)["presentations"]); len(names) != 1 || names[0] != "Cola menu" {
		t.Errorf("the presentation must be named: %v", in)
	}
	e.expect(c, "POST", "/api/v1/presentations/"+pres2+"/archive", nil, 204, "")
	e.expect(c, "DELETE", "/api/v1/products/"+prod, nil, 204, "")
	out = e.expect(c, "POST", "/api/v1/presentations/"+pres2+"/restore", nil, 409, "restore_references_missing")
	if refs := toStrings(rvErrParams(out)["references"]); len(refs) != 1 {
		t.Errorf("the deleted product must be named: %v", out)
	}

	// A never-published playlist only gets warnings for its draft.
	vid := e.seedAsset("video")
	draftOnly := e.rvPlaylist("Draft", false, rvItem("i1", "media", vid, nil))
	e.expect(c, "DELETE", "/api/v1/playlists/"+draftOnly, nil, 204, "")
	e.rvExec(`UPDATE media_assets SET trashed_at = 'now' WHERE id = ?`, vid)
	back := e.expect(c, "POST", "/api/v1/playlists/"+draftOnly+"/restore", nil, 200, "")
	if w := toStrings(back["warnings"]); back["archived"] != false || len(w) != 1 || !strings.HasPrefix(w[0], "video-") {
		t.Fatalf("draft problems come back as warnings: %v", back)
	}
}

// Finding 8: a category used by a product list (draft or published version of a presentation
// that was not deleted) cannot be deleted.
func TestCategoryDeleteBlockedByProductLists(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	cat := e.category("Cocktails", "")
	slide := cntSlide("s1", "Main", 5000)
	slide.Elements = []contracts.Element{rvProductList("pl1", "categories", cat)}
	presID, rev := e.rvPublishedPres("Cocktail menu", slide)
	out := e.expect(c, "DELETE", "/api/v1/categories/"+cat, nil, 409, "category_in_use")
	if names := toStrings(rvErrParams(out)["presentations"]); len(names) != 1 || names[0] != "Cocktail menu" {
		t.Errorf("category_in_use must name the presentation: %v", out)
	}
	// Draft clean, published version still uses it.
	cur := e.expect(c, "GET", "/api/v1/presentations/"+presID, nil, 200, "")
	doc := cntDoc(t, cur)
	doc.Slides[0].Elements = []contracts.Element{rvProductList("pl1", "all")}
	rev = e.savePres(presID, rev, doc)
	e.expect(c, "DELETE", "/api/v1/categories/"+cat, nil, 409, "category_in_use")
	// Deleted (archived) presentations do not count; restoring one re-validates it.
	e.expect(c, "POST", "/api/v1/presentations/"+presID+"/archive", nil, 204, "")
	e.expect(c, "DELETE", "/api/v1/categories/"+cat, nil, 204, "")
	e.expect(c, "POST", "/api/v1/presentations/"+presID+"/restore", nil, 409, "restore_references_missing")
	_ = rev
}

// Finding 9: role user only sees the zone overrides of its own zones.
func TestProductListHidesForeignZoneOverrides(t *testing.T) {
	e := catSetup(t)
	cat := e.category("Drinks", "")
	prod := e.product("Cola", cat, 300)
	za, zb := e.zone("Bar A"), e.zone("Bar B")
	for _, z := range []string{za, zb} {
		e.expect(e.admin, "POST", "/api/v1/live/products/"+prod+"/status", map[string]any{"status": "SOLD_OUT", "zoneId": z}, 200, "")
	}
	overrides := func(c *client) map[string]any {
		return asMap(asMap(asList(e.expect(c, "GET", "/api/v1/products", nil, 200, "")["products"])[0])["zoneStatus"])
	}
	if got := overrides(e.login("barkeeper", "user", za)); len(got) != 1 || got[za] != "SOLD_OUT" {
		t.Errorf("user must see its own zone only: %v", got)
	}
	if got := overrides(e.login("nozone", "user")); len(got) != 0 {
		t.Errorf("user without zones sees no overrides: %v", got)
	}
	if got := overrides(e.login("manager", "operator")); len(got) != 2 {
		t.Errorf("operators see every zone: %v", got)
	}
}

// Finding 14: zones, categories and ticker templates use optimistic concurrency.
func TestZoneCategoryTickerRevisions(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	tpl := map[string]any{"font": "Montserrat", "size": 36, "color": "#ffffff", "background": "#000000", "speed": 150,
		"direction": "left", "height": 70, "position": "bottom"}
	objects := []struct {
		path string
		body map[string]any
		edit map[string]any
	}{
		{"/api/v1/zones", map[string]any{"name": "Bar"}, map[string]any{"name": "Bar 2"}},
		{"/api/v1/categories", map[string]any{"name": "Beer"}, map[string]any{"name": "Beers"}},
		{"/api/v1/ticker-templates", map[string]any{"name": "Std", "template": tpl}, map[string]any{"name": "Standard"}},
	}
	for _, o := range objects {
		created := e.expect(c, "POST", o.path, o.body, 200, "")
		id := asStr(created["id"])
		if asNum(created["revision"]) != 1 {
			t.Fatalf("%s: created revision %v", o.path, created)
		}
		path := o.path + "/" + id
		e.expect(c, "PATCH", path, o.edit, 422, "required")
		with := func(rev int) map[string]any {
			b := map[string]any{"revision": rev}
			for k, v := range o.edit {
				b[k] = v
			}
			return b
		}
		updated := e.expect(c, "PATCH", path, with(1), 200, "")
		if asNum(updated["revision"]) != 2 || updated["name"] != o.edit["name"] {
			t.Fatalf("%s: updated %v", o.path, updated)
		}
		stale := e.expect(c, "PATCH", path, map[string]any{"revision": 1, "name": "Late"}, 409, "conflict")
		if asNum(rvErrParams(stale)["revision"]) != 2 {
			t.Errorf("%s: conflict must carry the current revision: %v", o.path, stale)
		}
		if e.catRev(path) != 2 {
			t.Errorf("%s: list must carry the revision", o.path)
		}
		e.expect(c, "PATCH", o.path+"/"+id+"x", with(2), 404, "not_found")
	}
}

// Findings 10 and 11: schedule prices are validated like product prices and in the
// product's currency; rule names are bounded.
func TestSchedulePriceCurrencyAndNameLength(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	zone := e.zone("Bar")
	cat := e.category("Drinks", "")
	prod := e.product("Cola", cat, 300) // EUR
	rule := func(name string, price map[string]any) map[string]any {
		return map[string]any{"name": name, "scope": "zone", "zoneId": zone,
			"window": map[string]any{"days": []int{1}, "start": "17:00", "end": "19:00"},
			"prices": []map[string]any{{"productId": prod, "price": price}}}
	}
	money := func(a int64, cur string) map[string]any { return map[string]any{"amount": a, "currency": cur} }
	out := e.expect(c, "POST", "/api/v1/schedules", rule("X", money(200, "USD")), 422, "currency_mismatch")
	if p := rvErrParams(out); p["field"] != "prices[0].price.currency" || p["currency"] != "EUR" {
		t.Errorf("currency_mismatch params %v", p)
	}
	out = e.expect(c, "POST", "/api/v1/schedules", rule("X", money(200, "eur")), 422, "invalid")
	if rvErrParams(out)["field"] != "prices[0].price.currency" {
		t.Errorf("invalid currency field %v", out)
	}
	e.expect(c, "POST", "/api/v1/schedules", rule("X", money(contracts.MaxMoneyAmount+1, "EUR")), 422, "out_of_range")
	e.expect(c, "POST", "/api/v1/schedules", rule(strings.Repeat("n", 201), money(200, "EUR")), 422, "too_long")
	created := e.expect(c, "POST", "/api/v1/schedules", rule(strings.Repeat("ü", 200), money(200, "EUR")), 200, "")
	e.expect(c, "PATCH", "/api/v1/schedules/"+asStr(created["id"]), map[string]any{"revision": 1, "name": strings.Repeat("n", 201)}, 422, "too_long")
	e.expect(c, "PATCH", "/api/v1/schedules/"+asStr(created["id"]), map[string]any{"revision": 1,
		"window": map[string]any{"days": []int{1}, "start": "+1:00", "end": "19:00"}}, 422, "invalid")
}
