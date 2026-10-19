package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"opensignage/internal/common/ids"
	"opensignage/internal/contracts"
	"opensignage/internal/server/content"
	"opensignage/internal/server/core"
)

// ---------------------------------------------------------------- helpers (cnt… prefix)

func cntDoc(t *testing.T, detail map[string]any) contracts.Presentation {
	t.Helper()
	var doc contracts.Presentation
	if err := json.Unmarshal([]byte(catJSON(detail["draft"])), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func cntTextElem(id, font string) contracts.Element {
	st := contracts.TextStyle{Font: font, Size: 40, Color: "#ffffff", LineHeight: 1.2, Align: "left"}
	return contracts.Element{ID: id, Type: contracts.ElText, X: 100, Y: 50, W: 400, H: 100, Opacity: 1, VAlign: "top",
		Padding: &[4]float64{0, 0, 0, 0}, Style: &st,
		Doc: &contracts.RichDoc{Paragraphs: []contracts.Paragraph{{Runs: []contracts.Run{{Text: "Hello"}}}}}}
}

func cntMediaElem(id, typ, asset string) contracts.Element {
	on := true
	return contracts.Element{ID: id, Type: typ, X: 0, Y: 0, W: 300, H: 200, Opacity: 1, AssetID: asset, Fit: "contain", Loop: &on, Muted: &on}
}

func cntSlide(id, name string, ms int64) contracts.Slide {
	return contracts.Slide{ID: id, Name: name, Duration: contracts.Duration{Mode: "fixed", MS: ms},
		Background: contracts.Background{Type: "color", Color: "#000000"},
		Transition: contracts.Transition{Type: "fade", MS: 600}, Elements: []contracts.Element{}}
}

func (e *catEnv) newPres(name string, w, h int) (string, map[string]any) {
	e.t.Helper()
	out := e.expect(e.admin, "POST", "/api/v1/presentations", map[string]any{"name": name, "width": w, "height": h}, 200, "")
	return asStr(out["id"]), out
}

// savePres stores the document and returns the new draft revision.
func (e *catEnv) savePres(id string, rev int64, doc contracts.Presentation) int64 {
	e.t.Helper()
	out := e.expect(e.admin, "PUT", "/api/v1/presentations/"+id+"/draft", map[string]any{"revision": rev, "doc": doc}, 200, "")
	return int64(asNum(out["revision"]))
}

func (e *catEnv) refs(ownerType, ownerID string) []string {
	e.t.Helper()
	rows, err := e.srv.App.DB.R.Query(`SELECT asset_id FROM content_refs WHERE owner_type = ? AND owner_id = ? ORDER BY asset_id`, ownerType, ownerID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func problems(out map[string]any) string {
	return catJSON(asMap(asMap(out["error"])["params"])["problems"])
}

// ---------------------------------------------------------------- presentations

func TestPresentationDraftPublishDiscardCycle(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	img, vid := e.seedAsset("image"), e.seedAsset("video")

	id, created := e.newPres("Menu", 1280, 720)
	if created["draftRevision"] != float64(1) || created["hasChanges"] != true || created["publishedVersion"] != nil {
		t.Fatalf("created %v", created)
	}
	doc := cntDoc(t, created)
	if doc.ID != id || doc.Name != "Menu" || doc.Width != 1280 || len(doc.Slides) != 1 || doc.Slides[0].Duration.MS != 10000 {
		t.Fatalf("default document %+v", doc)
	}
	list := asList(e.expect(c, "GET", "/api/v1/presentations", nil, 200, "")["presentations"])
	if len(list) != 1 {
		t.Fatalf("list %v", list)
	}
	item := asMap(list[0])
	if item["slideCount"] != float64(1) || item["width"] != float64(1280) || item["height"] != float64(720) ||
		item["publishedVersion"] != nil || item["hasChanges"] != true || asMap(item["firstSlide"])["id"] != doc.Slides[0].ID {
		t.Fatalf("list item %v", item)
	}
	// The thumbnails draw product lists with the presentation's own text styles.
	if ty := asMap(item["typography"]); asMap(ty["price"])["font"] != doc.Typography.Price.Font || asMap(ty["heading"])["size"] != float64(doc.Typography.Heading.Size) {
		t.Fatalf("list item typography %v", item["typography"])
	}

	// Validation of create requests
	e.expect(c, "POST", "/api/v1/presentations", map[string]any{"width": 1280, "height": 720}, 422, "required")
	e.expect(c, "POST", "/api/v1/presentations", map[string]any{"name": "x", "width": 5, "height": 720}, 422, "out_of_range")
	e.expect(c, "POST", "/api/v1/presentations", map[string]any{"name": "x", "width": 1280}, 422, "required")

	// Autosave: revision, conflict, no-op
	e.events()
	doc.Slides[0].Name = "Intro"
	doc.Slides[0].Elements = []contracts.Element{cntTextElem("t1", "Montserrat"), cntMediaElem("i1", contracts.ElImage, img)}
	rev := e.savePres(id, 1, doc)
	if rev != 2 {
		t.Fatalf("revision %d", rev)
	}
	if got := e.refs("presentation_draft", id); len(got) != 1 || got[0] != img {
		t.Fatalf("draft refs %v", got)
	}
	noop := e.expect(c, "PUT", "/api/v1/presentations/"+id+"/draft", map[string]any{"revision": 2, "doc": doc}, 200, "")
	if noop["changed"] != false || noop["revision"] != float64(2) {
		t.Fatalf("identical save must not change the revision: %v", noop)
	}
	stale := e.expect(c, "PUT", "/api/v1/presentations/"+id+"/draft", map[string]any{"revision": 1, "doc": doc}, 409, "conflict")
	if asNum(asMap(asMap(stale["error"])["params"])["revision"]) != 2 {
		t.Errorf("conflict must report the current revision: %v", stale)
	}
	e.expect(c, "PUT", "/api/v1/presentations/"+id+"/draft", map[string]any{"doc": doc}, 422, "required")
	wrongID := doc
	wrongID.ID = ids.New()
	e.expect(c, "PUT", "/api/v1/presentations/"+id+"/draft", map[string]any{"revision": 2, "doc": wrongID}, 422, "mismatch")
	ghost := doc
	ghost.ID = ids.New()
	e.expect(c, "PUT", "/api/v1/presentations/"+ghost.ID+"/draft", map[string]any{"revision": 1, "doc": ghost}, 404, "not_found")

	// Validation: unknown font, two videos, wrong kind, unknown asset – nothing is stored.
	bad := func(name string, mutate func(d *contracts.Presentation), want string) {
		d := doc
		d.Slides = []contracts.Slide{doc.Slides[0]}
		d.Slides[0].Elements = append([]contracts.Element(nil), doc.Slides[0].Elements...)
		mutate(&d)
		out := e.expect(c, "PUT", "/api/v1/presentations/"+id+"/draft", map[string]any{"revision": 2, "doc": d}, 422, "invalid_document")
		if !strings.Contains(problems(out), want) {
			t.Errorf("%s: problems %s lack %q", name, problems(out), want)
		}
	}
	bad("unknown font", func(d *contracts.Presentation) { d.Slides[0].Elements[0].Style.Font = "Comic Sans" }, "unknown_font")
	bad("two videos", func(d *contracts.Presentation) {
		d.Slides[0].Elements = append(d.Slides[0].Elements, cntMediaElem("v1", contracts.ElVideo, vid), cntMediaElem("v2", contracts.ElVideo, vid))
	}, "max_one_video_element")
	bad("wrong kind", func(d *contracts.Presentation) { d.Slides[0].Elements[1].AssetID = vid }, "wrong_asset_kind")
	bad("unknown asset", func(d *contracts.Presentation) { d.Slides[0].Elements[1].AssetID = ids.New() }, "unknown_asset")
	bad("trashed asset", func(d *contracts.Presentation) {
		if _, err := e.srv.App.DB.W.Exec(`UPDATE media_assets SET trashed_at = 'now' WHERE id = ?`, img); err != nil {
			t.Fatal(err)
		}
	}, "unknown_asset")
	if _, err := e.srv.App.DB.W.Exec(`UPDATE media_assets SET trashed_at = NULL WHERE id = ?`, img); err != nil {
		t.Fatal(err)
	}
	if got := e.expect(c, "GET", "/api/v1/presentations/"+id, nil, 200, ""); got["draftRevision"] != float64(2) {
		t.Fatalf("a rejected save must not change anything: %v", got["draftRevision"])
	}

	// Oversized bodies are rejected (8 MB limit).
	huge := doc
	huge.Slides = []contracts.Slide{doc.Slides[0]}
	huge.Slides[0].Elements = []contracts.Element{cntTextElem("big", "Montserrat")}
	huge.Slides[0].Elements[0].Doc = &contracts.RichDoc{Paragraphs: []contracts.Paragraph{{Runs: []contracts.Run{{Text: strings.Repeat("a", 9<<20)}}}}}
	e.expect(c, "PUT", "/api/v1/presentations/"+id+"/draft", map[string]any{"revision": 2, "doc": huge}, 413, "too_large")

	// Editing never distributes: no content events so far.
	if n := catCount(e.events(), core.TopicContent); n != 0 {
		t.Fatalf("autosave published %d content events", n)
	}

	// Publish
	e.expect(c, "POST", "/api/v1/presentations/"+id+"/publish", map[string]any{"revision": 1}, 409, "conflict")
	pub := e.expect(c, "POST", "/api/v1/presentations/"+id+"/publish", map[string]any{"revision": 2}, 200, "")
	if pub["publishedVersion"] != float64(1) || pub["changed"] != true {
		t.Fatalf("publish %v", pub)
	}
	var contentEvents []core.Event
	for _, ev := range e.events() {
		if ev.Topic == core.TopicContent {
			contentEvents = append(contentEvents, ev)
		}
	}
	if len(contentEvents) != 1 || contentEvents[0].ID != id || contentEvents[0].Kind != "presentation.published" {
		t.Fatalf("publish must publish exactly one content event: %v", contentEvents)
	}
	if got := e.refs("presentation_published", id); len(got) != 1 || got[0] != img {
		t.Fatalf("published refs %v", got)
	}
	d := e.expect(c, "GET", "/api/v1/presentations/"+id, nil, 200, "")
	if d["hasChanges"] != false || d["publishedVersion"] != float64(1) {
		t.Fatalf("after publish %v", d)
	}
	if again := e.expect(c, "POST", "/api/v1/presentations/"+id+"/publish", nil, 200, ""); again["changed"] != false || len(e.events()) != 0 {
		t.Fatalf("publishing an unchanged draft is a no-op: %v", again)
	}

	// Editing after publish: hasChanges, draft refs follow the draft, published refs stay.
	doc2 := cntDoc(t, d)
	doc2.Slides[0].Elements = doc2.Slides[0].Elements[:1] // drop the image
	doc2.Slides = append(doc2.Slides, cntSlide("s2", "Second", 8000))
	rev = e.savePres(id, 2, doc2)
	d = e.expect(c, "GET", "/api/v1/presentations/"+id, nil, 200, "")
	if d["hasChanges"] != true || len(e.refs("presentation_draft", id)) != 0 || len(e.refs("presentation_published", id)) != 1 {
		t.Fatalf("draft diverged: %v draft refs %v published refs %v", d["hasChanges"], e.refs("presentation_draft", id), e.refs("presentation_published", id))
	}

	// Discard returns to the published state.
	disc := e.expect(c, "POST", "/api/v1/presentations/"+id+"/discard", nil, 200, "")
	back := cntDoc(t, disc)
	if disc["hasChanges"] != false || len(back.Slides) != 1 || len(back.Slides[0].Elements) != 2 || disc["draftRevision"] != float64(rev+1) {
		t.Fatalf("discard %v", disc)
	}
	if got := e.refs("presentation_draft", id); len(got) != 1 {
		t.Fatalf("discard must restore draft refs: %v", got)
	}

	// Second version, version list, restoring version 1 into the draft.
	back.Slides[0].Name = "Intro v2"
	e.savePres(id, int64(disc["draftRevision"].(float64)), back)
	e.expect(c, "POST", "/api/v1/presentations/"+id+"/publish", nil, 200, "")
	vs := e.expect(c, "GET", "/api/v1/presentations/"+id+"/versions", nil, 200, "")
	versions := asList(vs["versions"])
	if len(versions) != 2 || asMap(versions[0])["version"] != float64(2) || asMap(versions[1])["version"] != float64(1) || vs["publishedVersion"] != float64(2) ||
		asMap(versions[0])["publishedBy"] != "admin" {
		t.Fatalf("versions %v", vs)
	}
	restored := e.expect(c, "POST", "/api/v1/presentations/"+id+"/versions/1/restore", nil, 200, "")
	if cntDoc(t, restored).Slides[0].Name != "Intro" || restored["hasChanges"] != true || len(asList(restored["warnings"])) != 0 {
		t.Fatalf("restore %v", restored)
	}
	e.expect(c, "POST", "/api/v1/presentations/"+id+"/versions/9/restore", nil, 404, "not_found")
	e.expect(c, "POST", "/api/v1/presentations/"+id+"/versions/x/restore", nil, 404, "not_found")

	// Publishing validates again: a trashed asset blocks it.
	if _, err := e.srv.App.DB.W.Exec(`UPDATE media_assets SET trashed_at = 'now' WHERE id = ?`, img); err != nil {
		t.Fatal(err)
	}
	out := e.expect(c, "POST", "/api/v1/presentations/"+id+"/publish", nil, 422, "invalid_document")
	if !strings.Contains(problems(out), "unknown_asset") {
		t.Errorf("publish re-validation: %s", problems(out))
	}

	// A restore of a version that mentions vanished media still works but warns.
	if _, err := e.srv.App.DB.W.Exec(`UPDATE media_assets SET trashed_at = NULL WHERE id = ?`, img); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.App.DB.W.Exec(`UPDATE media_assets SET trashed_at = 'now' WHERE id = ?`, img); err != nil {
		t.Fatal(err)
	}
	warned := e.expect(c, "POST", "/api/v1/presentations/"+id+"/versions/2/restore", nil, 200, "")
	if len(asList(warned["warnings"])) == 0 {
		t.Errorf("restoring a version with missing media must warn: %v", warned["warnings"])
	}

	// Discard on a never-published presentation
	other, _ := e.newPres("Never published", 1920, 1080)
	e.expect(c, "POST", "/api/v1/presentations/"+other+"/discard", nil, 409, "never_published")

	// Audit trail: publish/discard carry versions
	var publishedAudit bool
	for _, a := range e.auditActions("presentation.published") {
		publishedAudit = publishedAudit || (a["objectId"] == id && strings.Contains(asStr(a["new"]), `"version":2`) && strings.Contains(asStr(a["old"]), `"version":1`))
	}
	if !publishedAudit {
		t.Error("presentation.published audit entry with old/new version missing")
	}

	// Roles: user may not touch content
	user := e.login("barkeeper", "user")
	e.expect(user, "GET", "/api/v1/presentations", nil, 403, "forbidden")
	op := e.login("manager", "operator")
	e.expect(op, "GET", "/api/v1/presentations", nil, 200, "")
}

// A video switched to play once and with sound keeps that through saving and publishing –
// false was dropped before, and the player then looped the video and muted it again.
func TestPresentationVideoLoopAndSoundOff(t *testing.T) {
	e := catSetup(t)
	vid := e.seedAsset("video")
	id, created := e.newPres("Video", 1280, 720)
	doc := cntDoc(t, created)
	off := false
	once := cntMediaElem("v1", contracts.ElVideo, vid)
	once.Loop, once.Muted = &off, &off
	plain := cntMediaElem("v2", contracts.ElVideo, vid)
	plain.Loop, plain.Muted = nil, nil // never touched: the defaults of the player
	doc.Slides[0].Elements = []contracts.Element{once}
	second := cntSlide(ids.New(), "Second", 5000)
	second.Elements = []contracts.Element{plain}
	doc.Slides = append(doc.Slides, second)
	rev := e.savePres(id, 1, doc)
	e.expect(e.admin, "POST", "/api/v1/presentations/"+id+"/publish", map[string]any{"revision": rev}, 200, "")
	d := e.expect(e.admin, "GET", "/api/v1/presentations/"+id, nil, 200, "")
	raw := catJSON(d["draft"])
	if !strings.Contains(raw, `"loop":false`) || !strings.Contains(raw, `"muted":false`) {
		t.Fatalf("false must be stored: %s", raw)
	}
	got := cntDoc(t, d)
	v1, v2 := got.Slides[0].Elements[0], got.Slides[1].Elements[0]
	if v1.Loop == nil || *v1.Loop || v1.Muted == nil || *v1.Muted {
		t.Fatalf("loop/sound off lost: %+v", v1)
	}
	if v2.Loop != nil || v2.Muted != nil {
		t.Fatalf("untouched video must keep the defaults: %+v", v2)
	}
	// The published version (what the players get) as well.
	var pub string
	if err := e.srv.App.DB.R.QueryRow(`SELECT doc FROM presentation_versions WHERE presentation_id = ? AND version = 1`, id).Scan(&pub); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pub, `"loop":false`) || !strings.Contains(pub, `"muted":false`) {
		t.Fatalf("published version without the settings: %s", pub)
	}
}

func TestPresentationCopiesTemplatesResizeRenameArchive(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	id, created := e.newPres("Menu", 1280, 720)
	doc := cntDoc(t, created)
	doc.Slides[0].Elements = []contracts.Element{cntTextElem("t1", "Montserrat")}
	rev := e.savePres(id, 1, doc)
	e.expect(c, "POST", "/api/v1/presentations/"+id+"/publish", nil, 200, "")

	// Rename keeps "no unpublished changes" and renames the draft document as well.
	renamed := e.expect(c, "PATCH", "/api/v1/presentations/"+id, map[string]any{"name": "Main menu"}, 200, "")
	if renamed["name"] != "Main menu" || renamed["hasChanges"] != false || cntDoc(t, renamed).Name != "Main menu" || renamed["draftRevision"] != float64(rev+1) {
		t.Fatalf("rename %v", renamed)
	}
	e.expect(c, "PATCH", "/api/v1/presentations/"+id, map[string]any{"name": " "}, 422, "invalid")
	rev++

	// Autosaving the (renamed) document under the new revision keeps hasChanges false.
	d := cntDoc(t, renamed)
	if out := e.expect(c, "PUT", "/api/v1/presentations/"+id+"/draft", map[string]any{"revision": rev, "doc": d}, 200, ""); out["hasChanges"] != false {
		t.Fatalf("hasChanges after no-op save %v", out)
	}

	// Duplicate: a new, unpublished presentation with the same content.
	dup := e.expect(c, "POST", "/api/v1/presentations/"+id+"/duplicate", nil, 200, "")
	dupDoc := cntDoc(t, dup)
	if dup["id"] == id || dup["name"] != "Main menu (copy)" || dup["publishedVersion"] != nil || dup["hasChanges"] != true ||
		dupDoc.ID != dup["id"] || len(dupDoc.Slides[0].Elements) != 1 {
		t.Fatalf("duplicate %v", dup)
	}
	named := e.expect(c, "POST", "/api/v1/presentations/"+id+"/duplicate", map[string]any{"name": "Kopie"}, 200, "")
	if named["name"] != "Kopie" {
		t.Fatalf("named duplicate %v", named)
	}

	// Resize scales positions per axis, sizes and fonts by the smaller factor.
	cur := e.expect(c, "GET", "/api/v1/presentations/"+id, nil, 200, "")
	e.expect(c, "POST", "/api/v1/presentations/"+id+"/resize", map[string]any{"revision": 1, "width": 1920, "height": 1080}, 409, "conflict")
	e.expect(c, "POST", "/api/v1/presentations/"+id+"/resize", map[string]any{"width": 99999, "height": 1080}, 422, "out_of_range")
	rs := e.expect(c, "POST", "/api/v1/presentations/"+id+"/resize", map[string]any{"revision": cur["draftRevision"], "width": 1920, "height": 1080}, 200, "")
	rd := cntDoc(t, rs)
	el := rd.Slides[0].Elements[0]
	if rd.Width != 1920 || rd.Height != 1080 || el.X != 150 || el.Y != 75 || el.W != 600 || el.H != 150 || el.Style.Size != 60 || rs["hasChanges"] != true {
		t.Fatalf("resize result %+v", el)
	}
	rs = e.expect(c, "POST", "/api/v1/presentations/"+id+"/resize", map[string]any{"width": 1080, "height": 1920}, 200, "") // 16:9 → 9:16
	// Current draft is 1920×1080 with the element at 150/75, 600×150, font 60: fx 0.5625, fy 1.7778, s 0.5625.
	got := cntDoc(t, rs)
	el = got.Slides[0].Elements[0]
	if got.Width != 1080 || got.Height != 1920 || el.X != 84.38 || el.Y != 133.33 || el.W != 337.5 || el.H != 84.38 || el.Style.Size != 33.75 {
		t.Fatalf("portrait resize %+v", el)
	}

	// Templates: save, list, create from template (with and without a new size), no publishing.
	tpl := e.expect(c, "POST", "/api/v1/presentations/"+id+"/save-as-template", map[string]any{"name": "Bar template"}, 200, "")
	tplID := asStr(tpl["id"])
	if tpl["isTemplate"] != true || tpl["name"] != "Bar template" {
		t.Fatalf("template %v", tpl)
	}
	e.expect(c, "POST", "/api/v1/presentations/"+id+"/save-as-template", map[string]any{}, 422, "required")
	names := func(q string) []string {
		var out []string
		for _, x := range asList(e.expect(c, "GET", "/api/v1/presentations"+q, nil, 200, "")["presentations"]) {
			out = append(out, asStr(asMap(x)["name"]))
		}
		return out
	}
	if got := strings.Join(names("?templates=1"), ","); got != "Bar template" {
		t.Fatalf("templates %s", got)
	}
	if got := names(""); len(got) != 3 || strings.Contains(strings.Join(got, ","), "Bar template") {
		t.Fatalf("normal list must not contain templates: %v", got)
	}
	e.expect(c, "POST", "/api/v1/presentations/"+tplID+"/publish", nil, 409, "template_not_publishable")
	fromTpl := e.expect(c, "POST", "/api/v1/presentations", map[string]any{"name": "From template", "templateId": tplID}, 200, "")
	ftd := cntDoc(t, fromTpl)
	if ftd.Width != 1080 || ftd.Height != 1920 || ftd.ID != fromTpl["id"] || ftd.Name != "From template" || len(ftd.Slides[0].Elements) != 1 || fromTpl["isTemplate"] != false {
		t.Fatalf("from template %v", fromTpl)
	}
	sized := e.expect(c, "POST", "/api/v1/presentations", map[string]any{"name": "Wide", "templateId": tplID, "width": 2160, "height": 3840}, 200, "")
	if sd := cntDoc(t, sized); sd.Width != 2160 || sd.Slides[0].Elements[0].Style.Size != 67.5 {
		t.Fatalf("template with a new size must be scaled: %+v", sd.Slides[0].Elements[0].Style)
	}
	e.expect(c, "POST", "/api/v1/presentations", map[string]any{"name": "x", "templateId": id}, 422, "unknown_template") // not a template
	e.expect(c, "POST", "/api/v1/presentations", map[string]any{"name": "x", "templateId": ids.New()}, 422, "unknown_template")

	// Archive: DELETE and POST …/archive are the same operation, restore brings it back.
	e.events()
	e.expect(c, "DELETE", "/api/v1/presentations/"+asStr(named["id"]), nil, 204, "")
	if ev := e.events(); catCount(ev, core.TopicContent) != 1 || ev[len(ev)-1].Kind != "presentation.archived" {
		t.Fatalf("archive publishes content: %v", ev)
	}
	e.expect(c, "POST", "/api/v1/presentations/"+asStr(dup["id"])+"/archive", nil, 204, "")
	if got := strings.Join(names("?archived=1"), ","); got != "Kopie,Main menu (copy)" {
		t.Fatalf("archived %s", got)
	}
	arch := e.expect(c, "GET", "/api/v1/presentations/"+asStr(named["id"]), nil, 200, "")
	archDoc := cntDoc(t, arch)
	e.expect(c, "PUT", "/api/v1/presentations/"+asStr(named["id"])+"/draft", map[string]any{"revision": arch["draftRevision"], "doc": archDoc}, 409, "presentation_archived")
	e.expect(c, "POST", "/api/v1/presentations/"+asStr(named["id"])+"/publish", nil, 409, "presentation_archived")
	e.expect(c, "POST", "/api/v1/presentations/"+asStr(named["id"])+"/restore", nil, 200, "")
	if got := names(""); len(got) != 4 { // Main menu, Kopie (restored), From template, Wide
		t.Fatalf("after restore %v", got)
	}
}

// ---------------------------------------------------------------- playlists

func TestPlaylistValidationRuntimeAndPublish(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	img, vid := e.seedAsset("image"), e.seedAsset("video")
	if _, err := e.srv.App.DB.W.Exec(`UPDATE media_assets SET meta = '{"durationMs": 8000}' WHERE id = ?`, vid); err != nil {
		t.Fatal(err)
	}
	vidNoMeta := e.seedAsset("video")

	// Presentation with two 5 s slides and one hidden 7 s slide, published.
	presID, created := e.newPres("Menu", 1280, 720)
	pd := cntDoc(t, created)
	pd.Slides = []contracts.Slide{cntSlide("sa", "Intro", 5000), cntSlide("sb", "Offers", 5000), cntSlide("sc", "Hidden", 7000)}
	pd.Slides[2].Hidden = true
	e.savePres(presID, 1, pd)
	e.expect(c, "POST", "/api/v1/presentations/"+presID+"/publish", nil, 200, "")

	pl := e.expect(c, "POST", "/api/v1/playlists", map[string]any{"name": "Main loop"}, 200, "")
	plID := asStr(pl["id"])
	if pl["draftRevision"] != float64(1) || pl["publishedVersion"] != nil || pl["runtimeMs"] != float64(0) || pl["runtimeExact"] != true || len(asList(asMap(pl["draft"])["items"])) != 0 {
		t.Fatalf("new playlist %v", pl)
	}
	e.expect(c, "POST", "/api/v1/playlists", map[string]any{"name": ""}, 422, "invalid")

	item := func(id, kind, ref string, play map[string]any, extra map[string]any) map[string]any {
		m := map[string]any{"id": id, "kind": kind, "refId": ref, "play": play}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	dur := func(ms int) map[string]any { return map[string]any{"mode": "duration", "ms": ms} }
	rep := func(n int) map[string]any { return map[string]any{"mode": "repeat", "count": n} }
	put := func(rev int64, items ...map[string]any) (int, map[string]any) {
		return c.do("PUT", "/api/v1/playlists/"+plID+"/draft", map[string]any{"revision": rev, "doc": map[string]any{"id": plID, "name": "Main loop", "items": items}})
	}
	mustFail := func(name string, want string, items ...map[string]any) {
		t.Helper()
		code, out := put(1, items...)
		if code != 422 || errReason(out) != want {
			t.Errorf("%s: status %d, reason %q (want %q): %v", name, code, errReason(out), want, out)
		}
	}
	// exactly one of duration/repeat per item
	mustFail("both", "duration_xor_repeat", item("a", "presentation", presID, map[string]any{"mode": "duration", "ms": 5000, "count": 2}, nil))
	mustFail("repeat with ms", "duration_xor_repeat", item("a", "presentation", presID, map[string]any{"mode": "repeat", "ms": 5000, "count": 2}, nil))
	mustFail("neither", "out_of_range", item("a", "presentation", presID, map[string]any{"mode": "duration"}, nil))
	mustFail("no mode", "invalid_value", item("a", "presentation", presID, map[string]any{"ms": 5000}, nil))
	mustFail("repeat zero", "out_of_range", item("a", "presentation", presID, rep(0), nil))
	mustFail("image repeat", "image_requires_duration", item("a", "media", img, rep(2), nil))
	mustFail("unknown presentation", "unknown_reference", item("a", "presentation", ids.New(), dur(5000), nil))
	mustFail("unknown slide", "unknown_reference", item("a", "slide", ids.New(), dur(5000), map[string]any{"presentationId": presID}))
	mustFail("unknown media", "unknown_reference", item("a", "media", ids.New(), dur(5000), nil))
	mustFail("fit on presentation", "media_only", item("a", "presentation", presID, dur(5000), map[string]any{"fit": "cover"}))
	mustFail("bad kind", "invalid_value", item("a", "web", "http://x", dur(5000), nil))
	mustFail("duplicate item id", "invalid", item("a", "presentation", presID, dur(5000), nil), item("a", "presentation", presID, dur(5000), nil))
	e.expect(c, "PUT", "/api/v1/playlists/"+plID+"/draft", map[string]any{"revision": 1, "doc": map[string]any{"id": ids.New(), "name": "x", "items": []any{}}}, 422, "mismatch")

	// Valid playlist and runtime estimate:
	//  presentation, duration 25 s over a 10 s pass → 3 passes = 30 s   (hidden slide excluded)
	//  slide "Intro" × 3                           = 15 s
	//  image 4 s                                   =  4 s
	//  video (8 s from probe data) × 2             = 16 s
	e.events()
	code, out := put(1,
		item("i1", "presentation", presID, dur(25000), nil),
		item("i2", "slide", "sa", rep(3), map[string]any{"presentationId": presID}),
		item("i3", "media", img, dur(4000), map[string]any{"fit": "cover"}),
		item("i4", "media", vid, rep(2), nil))
	if code != 200 || out["revision"] != float64(2) || out["runtimeMs"] != float64(65000) || out["runtimeExact"] != true {
		t.Fatalf("save %d %v", code, out)
	}
	// One pass per item for the editor: the slide's own duration, the image duration, the
	// probed video length.
	for id, ms := range map[string]float64{"i1": 10000, "i2": 5000, "i3": 4000, "i4": 8000} {
		if p := asMap(asMap(out["itemPasses"])[id]); p["ms"] != ms || p["exact"] != true {
			t.Errorf("pass of %s = %v, want %v ms (exact)", id, p, ms)
		}
	}
	if n := catCount(e.events(), core.TopicContent); n != 0 {
		t.Fatalf("editing a playlist must not publish content events (%d)", n)
	}
	if got := e.refs("playlist_draft", plID); len(got) != 2 {
		t.Fatalf("playlist draft refs %v", got)
	}
	detail := e.expect(c, "GET", "/api/v1/playlists/"+plID, nil, 200, "")
	items := asList(asMap(detail["draft"])["items"])
	if len(items) != 4 || asMap(items[3])["fit"] != "contain" || detail["runtimeMs"] != float64(65000) {
		t.Fatalf("detail: media fit defaults from settings, runtime follows: %v", detail)
	}
	if p := asMap(asMap(detail["itemPasses"])["i2"]); p["ms"] != float64(5000) {
		t.Fatalf("detail: item passes %v", detail["itemPasses"])
	}
	// Unknown video length makes the estimate inexact.
	code, out = put(2, item("i1", "media", vidNoMeta, dur(5000), nil))
	if code != 200 || out["runtimeExact"] != false || out["runtimeMs"] != float64(5000) {
		t.Fatalf("inexact runtime %d %v", code, out)
	}
	if p := asMap(asMap(out["itemPasses"])["i1"]); p["ms"] != float64(0) || p["exact"] != false {
		t.Fatalf("unknown pass %v", p)
	}
	// Stale revision
	stale := e.expect(c, "PUT", "/api/v1/playlists/"+plID+"/draft", map[string]any{"revision": 2, "doc": map[string]any{"id": plID, "name": "x", "items": []any{}}}, 409, "conflict")
	if asNum(asMap(asMap(stale["error"])["params"])["revision"]) != 3 {
		t.Errorf("conflict params %v", stale)
	}
	// A single slide with a duration (playlists from before slides used their own duration)
	// still saves and plays whole slide passes: 12 s over 5 s slides = 15 s.
	code, out = put(3,
		item("i1", "presentation", presID, dur(25000), nil),
		item("i2", "slide", "sa", rep(3), map[string]any{"presentationId": presID}),
		item("i5", "slide", "sb", dur(12000), map[string]any{"presentationId": presID}))
	if code != 200 || out["runtimeMs"] != float64(30000+15000+15000) {
		t.Fatalf("save %d %v", code, out)
	}

	// Publishing needs published content: add a slide to the draft only → slide_not_published.
	cur := e.expect(c, "GET", "/api/v1/presentations/"+presID, nil, 200, "")
	nd := cntDoc(t, cur)
	nd.Slides = append(nd.Slides, cntSlide("sd", "New slide", 5000))
	e.savePres(presID, int64(cur["draftRevision"].(float64)), nd)
	code, out = put(4,
		item("i1", "presentation", presID, dur(25000), nil),
		item("i2", "slide", "sd", rep(1), map[string]any{"presentationId": presID})) // slide exists in the draft
	if code != 200 {
		t.Fatalf("save with draft-only slide %d %v", code, out)
	}
	e.expect(c, "POST", "/api/v1/playlists/"+plID+"/publish", nil, 422, "slide_not_published")
	unpub, _ := e.newPres("Unpublished", 1280, 720)
	code, out = put(5, item("i1", "presentation", unpub, dur(5000), nil))
	if code != 200 {
		t.Fatalf("save %d %v", code, out)
	}
	e.expect(c, "POST", "/api/v1/playlists/"+plID+"/publish", nil, 422, "presentation_not_published")
	code, out = put(6,
		item("i1", "presentation", presID, dur(25000), nil),
		item("i2", "slide", "sa", rep(3), map[string]any{"presentationId": presID}),
		item("i3", "media", img, dur(4000), nil))
	if code != 200 {
		t.Fatalf("save %d %v", code, out)
	}
	e.events()
	pub := e.expect(c, "POST", "/api/v1/playlists/"+plID+"/publish", map[string]any{"revision": 7}, 200, "")
	if pub["publishedVersion"] != float64(1) || pub["changed"] != true {
		t.Fatalf("publish %v", pub)
	}
	var pubEvents []core.Event
	for _, ev := range e.events() {
		if ev.Topic == core.TopicContent {
			pubEvents = append(pubEvents, ev)
		}
	}
	if len(pubEvents) != 1 || pubEvents[0].Kind != "playlist.published" || pubEvents[0].ID != plID {
		t.Fatalf("playlist publish event: %v", pubEvents)
	}
	if got := e.refs("playlist_published", plID); len(got) != 1 || got[0] != img {
		t.Fatalf("published refs %v", got)
	}
	e.expect(c, "POST", "/api/v1/playlists/"+plID+"/publish", map[string]any{"revision": 3}, 409, "conflict")

	// Discard returns to the published state; never published → 409.
	code, _ = put(7, item("i1", "presentation", presID, dur(1000), nil))
	if code != 200 {
		t.Fatal("save")
	}
	disc := e.expect(c, "POST", "/api/v1/playlists/"+plID+"/discard", nil, 200, "")
	if disc["hasChanges"] != false || len(asList(asMap(disc["draft"])["items"])) != 3 {
		t.Fatalf("discard %v", disc)
	}
	fresh := asStr(e.expect(c, "POST", "/api/v1/playlists", map[string]any{"name": "Fresh"}, 200, "")["id"])
	e.expect(c, "POST", "/api/v1/playlists/"+fresh+"/discard", nil, 409, "never_published")

	// Rename + duplicate
	ren := e.expect(c, "PATCH", "/api/v1/playlists/"+plID, map[string]any{"name": "Evening loop"}, 200, "")
	if ren["name"] != "Evening loop" || asMap(ren["draft"])["name"] != "Evening loop" || ren["hasChanges"] != false {
		t.Fatalf("rename %v", ren)
	}
	dup := e.expect(c, "POST", "/api/v1/playlists/"+plID+"/duplicate", nil, 200, "")
	if dup["name"] != "Evening loop (copy)" || dup["publishedVersion"] != nil || len(asList(asMap(dup["draft"])["items"])) != 3 || dup["id"] == plID {
		t.Fatalf("duplicate %v", dup)
	}

	// Presentation protection: used by a playlist (draft and published).
	inUse := e.expect(c, "DELETE", "/api/v1/presentations/"+presID, nil, 409, "presentation_in_use")
	names := toStrings(asMap(asMap(inUse["error"])["params"])["playlists"])
	if len(names) != 2 || names[0] != "Evening loop" || names[1] != "Evening loop (copy)" {
		t.Fatalf("presentation_in_use must list the playlists: %v", inUse)
	}
	// Removing it from the drafts is not enough while the published version still plays it.
	dupID := asStr(dup["id"])
	e.expect(c, "PUT", "/api/v1/playlists/"+dupID+"/draft", map[string]any{"revision": 1, "doc": map[string]any{"id": dupID, "name": "Evening loop (copy)", "items": []any{}}}, 200, "")
	e.expect(c, "DELETE", "/api/v1/presentations/"+presID, nil, 409, "presentation_in_use")

	// Playlist protection: schedule rules and closed playlists.
	zone := e.zone("Bar")
	rule := e.expect(c, "POST", "/api/v1/schedules", map[string]any{"name": "Daytime", "scope": "default", "zoneId": zone, "playlistId": plID, "prices": []any{}}, 200, "")
	e.patchCur(c, "/api/v1/zones/"+zone, map[string]any{"closedPlaylistId": plID}, 200, "")
	blocked := e.expect(c, "DELETE", "/api/v1/playlists/"+plID, nil, 409, "playlist_in_use")
	p := asMap(asMap(blocked["error"])["params"])
	if r := toStrings(p["rules"]); len(r) != 1 || r[0] != "Daytime" {
		t.Errorf("playlist_in_use rules %v", p)
	}
	if z := toStrings(p["zones"]); len(z) != 1 || z[0] != "Bar" {
		t.Errorf("playlist_in_use zones %v", p)
	}
	e.expect(c, "DELETE", "/api/v1/schedules/"+asStr(rule["id"]), nil, 204, "")
	e.expect(c, "DELETE", "/api/v1/playlists/"+plID, nil, 409, "playlist_in_use") // still the closed playlist
	e.patchCur(c, "/api/v1/zones/"+zone, map[string]any{"closedPlaylistId": nil}, 200, "")
	e.events()
	e.expect(c, "DELETE", "/api/v1/playlists/"+plID, nil, 204, "")
	if ev := e.events(); catCount(ev, core.TopicContent) != 1 || ev[len(ev)-1].Kind != "playlist.archived" {
		t.Fatalf("archive publishes content: %v", ev)
	}
	// Archived playlists cannot be used or edited; they can be restored.
	e.expect(c, "POST", "/api/v1/schedules", map[string]any{"name": "Again", "scope": "default", "zoneId": zone, "playlistId": plID, "prices": []any{}}, 422, "unknown_playlist")
	e.expect(c, "PUT", "/api/v1/playlists/"+plID+"/draft", map[string]any{"revision": 1, "doc": map[string]any{"id": plID, "name": "x", "items": []any{}}}, 409, "playlist_archived")
	var archivedNames []string
	for _, x := range asList(e.expect(c, "GET", "/api/v1/playlists?archived=1", nil, 200, "")["playlists"]) {
		archivedNames = append(archivedNames, asStr(asMap(x)["name"]))
	}
	if len(archivedNames) != 1 || archivedNames[0] != "Evening loop" {
		t.Fatalf("archived list %v", archivedNames)
	}
	e.expect(c, "POST", "/api/v1/playlists/"+plID+"/restore", nil, 200, "")

	// Now that the playlist is archived once and restored, publish a version without the presentation
	// and the presentation is free again (published JSON no longer mentions it).
	cleared := e.expect(c, "GET", "/api/v1/playlists/"+plID, nil, 200, "")
	e.expect(c, "PUT", "/api/v1/playlists/"+plID+"/draft", map[string]any{"revision": cleared["draftRevision"], "doc": map[string]any{"id": plID, "name": "Evening loop", "items": []any{}}}, 200, "")
	e.expect(c, "POST", "/api/v1/playlists/"+plID+"/publish", nil, 200, "")
	e.expect(c, "DELETE", "/api/v1/presentations/"+presID, nil, 204, "")
}

func TestContentSearch(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	id, created := e.newPres("Cocktail menu", 1280, 720)
	d := cntDoc(t, created)
	d.Slides = []contracts.Slide{cntSlide("s1", "Happy Hour", 5000), cntSlide("s2", "Desserts", 5000), cntSlide("s3", "", 5000)}
	e.savePres(id, 1, d)
	e.newPres("Food", 1920, 1080)
	e.expect(c, "POST", "/api/v1/playlists", map[string]any{"name": "Cocktail loop"}, 200, "")
	e.expect(c, "POST", "/api/v1/playlists", map[string]any{"name": "Closed"}, 200, "")
	tpl := e.expect(c, "POST", "/api/v1/presentations/"+id+"/save-as-template", map[string]any{"name": "Cocktail template"}, 200, "")
	_ = tpl

	out := e.expect(c, "GET", "/api/v1/content/search?q=COCKTAIL", nil, 200, "")
	if len(asList(out["presentations"])) != 1 || asMap(asList(out["presentations"])[0])["id"] != id || len(asList(out["playlists"])) != 1 || len(asList(out["slides"])) != 0 {
		t.Fatalf("search by name (case-insensitive, templates excluded): %v", out)
	}
	out = e.expect(c, "GET", "/api/v1/content/search?q=happy", nil, 200, "")
	slides := asList(out["slides"])
	if len(slides) != 1 {
		t.Fatalf("slide search %v", out)
	}
	s := asMap(slides[0])
	if s["presentationId"] != id || s["slideId"] != "s1" || s["name"] != "Happy Hour" || s["index"] != float64(0) || s["presentationName"] != "Cocktail menu" {
		t.Fatalf("slide hit %v", s)
	}
	out = e.expect(c, "GET", "/api/v1/content/search", nil, 200, "")
	if len(asList(out["presentations"])) != 2 || len(asList(out["playlists"])) != 2 || len(asList(out["slides"])) != 0 {
		t.Fatalf("empty query lists presentations and playlists only: %v", out)
	}
	out = e.expect(c, "GET", "/api/v1/content/search?q=%25_", nil, 200, "") // LIKE wildcards are plain characters
	if len(asList(out["presentations"])) != 0 {
		t.Fatalf("wildcards must not match: %v", out)
	}
	e.expect(c, "GET", "/api/v1/content/search?q="+strings.Repeat("a", 101), nil, 422, "too_long")
	user := e.login("barkeeper", "user")
	e.expect(user, "GET", "/api/v1/content/search?q=x", nil, 403, "forbidden")
}

func TestVersionHistoryPhasesLabelsAndThinning(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	id, created := e.newPres("Long running", 1280, 720)
	doc := cntDoc(t, created)
	rev := int64(1)
	for i := 1; i <= 55; i++ {
		doc.Slides[0].Name = "Version " + strings.Repeat("x", i)
		rev = e.savePres(id, rev, doc)
		pub := e.expect(c, "POST", "/api/v1/presentations/"+id+"/publish", nil, 200, "")
		if pub["publishedVersion"] != float64(i) {
			t.Fatalf("publish %d returned %v", i, pub["publishedVersion"])
		}
	}
	// One editing phase: everything is kept for now.
	out := e.expect(c, "GET", "/api/v1/presentations/"+id+"/versions", nil, 200, "")
	vs := asList(out["versions"])
	if len(vs) != 55 || asMap(vs[0])["version"] != float64(55) || asMap(vs[54])["phase"] != float64(1) || asMap(vs[0])["phase"] != float64(1) {
		t.Fatalf("one phase of 55 versions, newest first: %d %v", len(vs), vs[0])
	}
	if r := asMap(out["rules"]); r["phaseGapMinutes"] != float64(60) || r["thinAfterDays"] != float64(10) {
		t.Fatalf("rules %v", r)
	}

	// Names.
	path := "/api/v1/presentations/" + id + "/versions/10/label"
	if r := e.expect(c, "PUT", path, map[string]any{"label": "  Summer menu "}, 200, ""); r["label"] != "Summer menu" {
		t.Fatalf("label is trimmed: %v", r)
	}
	e.expect(c, "PUT", path, map[string]any{"label": strings.Repeat("ä", 81)}, 422, "invalid_field")
	e.expect(c, "PUT", path, map[string]any{}, 422, "invalid_field")
	e.expect(c, "PUT", "/api/v1/presentations/"+id+"/versions/999/label", map[string]any{"label": "x"}, 404, "")
	user := e.login("barkeeper", "user")
	e.expect(user, "PUT", path, map[string]any{"label": "x"}, 403, "forbidden")
	if v := asMap(asList(e.expect(c, "GET", "/api/v1/presentations/"+id+"/versions", nil, 200, "")["versions"])[45]); v["version"] != float64(10) || v["label"] != "Summer menu" {
		t.Fatalf("named version %v", v)
	}
	var audits int
	if err := e.srv.App.DB.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'presentation.version_named' AND object_id = ?`, id).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit %d %v", audits, err)
	}

	// Eleven days later only the named version and the last one of the phase are left.
	old := time.Now().Add(-11 * 24 * time.Hour).UTC()
	if err := e.srv.App.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		for v := 1; v <= 55; v++ {
			at := old.Add(time.Duration(v) * time.Second).Format(time.RFC3339Nano)
			if _, err := tx.Exec(`UPDATE presentation_versions SET published_at = ? WHERE presentation_id = ? AND version = ?`, at, id, v); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var mod *content.Module
	for _, m := range e.srv.modules {
		if cm, ok := m.(*content.Module); ok {
			mod = cm
		}
	}
	if n, err := mod.ThinHistories(context.Background(), time.Now()); err != nil || n != 53 {
		t.Fatalf("thinned %d %v", n, err)
	}
	vs = asList(e.expect(c, "GET", "/api/v1/presentations/"+id+"/versions", nil, 200, "")["versions"])
	if len(vs) != 2 || asMap(vs[0])["version"] != float64(55) || asMap(vs[1])["version"] != float64(10) {
		t.Fatalf("left: %v", vs)
	}

	// Restoring does not reuse numbers: publishing after a restore continues at 56.
	e.expect(c, "POST", "/api/v1/presentations/"+id+"/versions/10/restore", nil, 200, "")
	if pub := e.expect(c, "POST", "/api/v1/presentations/"+id+"/publish", nil, 200, ""); pub["publishedVersion"] != float64(56) {
		t.Fatalf("version numbers are never reused: %v", pub)
	}

	// At most 100 named versions per presentation; renaming a named one still works.
	if err := e.srv.App.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		for v := 100; v < 199; v++ {
			if _, err := tx.Exec(`INSERT INTO presentation_versions(presentation_id, version, doc, published_at, published_by, label)
				SELECT presentation_id, ?, doc, published_at, published_by, 'kept' FROM presentation_versions WHERE presentation_id = ? AND version = 56`, v, id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.expect(c, "PUT", "/api/v1/presentations/"+id+"/versions/56/label", map[string]any{"label": "One too many"}, 409, "too_many_named_versions")
	e.expect(c, "PUT", path, map[string]any{"label": "Summer menu 2027"}, 200, "")
	e.expect(c, "PUT", path, map[string]any{"label": ""}, 200, "")
	e.expect(c, "PUT", "/api/v1/presentations/"+id+"/versions/56/label", map[string]any{"label": "Now it fits"}, 200, "")
}
