package app

import (
	"slices"
	"testing"

	"opensignage/internal/contracts"
)

// rightsUser creates an operator limited to zones with the given rights and logs it in.
func (e *catEnv) rightsUser(username string, rights map[string]any) *client {
	e.t.Helper()
	e.expect(e.admin, "POST", "/api/v1/users", map[string]any{"username": username, "displayName": username, "role": "operator",
		"password": "secret123", "passwordConfirm": "secret123", "active": true, "rights": rights}, 200, "")
	c := newClient(e.t, e.ts)
	if code, out := c.do("POST", "/api/v1/auth/login", map[string]any{"username": username, "password": "secret123"}); code != 200 {
		e.t.Fatalf("login %d %v", code, out)
	}
	c.do("GET", "/api/v1/auth/session", nil)
	return c
}

func (e *catEnv) setRights(userID string, rights map[string]any) {
	e.t.Helper()
	e.expect(e.admin, "PATCH", "/api/v1/users/"+userID, map[string]any{"rights": rights}, 200, "")
}

func idsOf(list []any) []string {
	out := []string{}
	for _, v := range list {
		out = append(out, asStr(asMap(v)["id"]))
	}
	slices.Sort(out)
	return out
}

func sorted(s ...string) []string {
	slices.Sort(s)
	return s
}

// rightsWorld: Bar 1 plays A (L1), Bar 2 plays B (L2), both play C (L3), every zone plays G
// through a global rule (LG). Bar 3 has no rules.
type rightsWorld struct {
	bar1, bar2, bar3       string
	a, b, c, g             string
	l1, l2, l3, lg         string
	ruleBar1, ruleBar2     string
	ruleBar1L3, ruleBar2L3 string
}

func (e *catEnv) rightsWorld() rightsWorld {
	e.t.Helper()
	var w rightsWorld
	w.bar1, w.bar2, w.bar3 = e.zone("Bar 1"), e.zone("Bar 2"), e.zone("Bar 3")
	slide := func(id string) contracts.Slide { return cntSlide(id, id, 5000) }
	w.a, _ = e.rvPublishedPres("A", slide("a1"))
	w.b, _ = e.rvPublishedPres("B", slide("b1"))
	w.c, _ = e.rvPublishedPres("C", slide("c1"))
	w.g, _ = e.rvPublishedPres("G", slide("g1"))
	w.l1 = e.rvPlaylist("L1", true, rvItem("i1", "presentation", w.a, nil))
	w.l2 = e.rvPlaylist("L2", true, rvItem("i2", "presentation", w.b, nil))
	w.l3 = e.rvPlaylist("L3", true, rvItem("i3", "presentation", w.c, nil))
	w.lg = e.rvPlaylist("LG", true, rvItem("ig", "slide", "g1", map[string]any{"presentationId": w.g}))
	rule := func(name, scope, zone, pl, start, end string) string {
		body := map[string]any{"name": name, "scope": scope, "playlistId": pl, "prices": []any{},
			"window": map[string]any{"days": []int{1, 2, 3, 4, 5, 6, 7}, "start": start, "end": end}}
		if zone != "" {
			body["zoneId"] = zone
		}
		return asStr(e.expect(e.admin, "POST", "/api/v1/schedules", body, 200, "")["id"])
	}
	w.ruleBar1 = rule("Bar 1 main", "zone", w.bar1, w.l1, "10:00", "12:00")
	w.ruleBar2 = rule("Bar 2 main", "zone", w.bar2, w.l2, "10:00", "12:00")
	w.ruleBar1L3 = rule("Bar 1 shared", "zone", w.bar1, w.l3, "13:00", "15:00")
	w.ruleBar2L3 = rule("Bar 2 shared", "zone", w.bar2, w.l3, "13:00", "15:00")
	rule("Everywhere", "global", "", w.lg, "16:00", "18:00")
	return w
}

func TestRightsContentAndZones(t *testing.T) {
	e := catSetup(t)
	w := e.rightsWorld()
	red := e.rightsUser("redakteur", map[string]any{"scope": "zones", "media": "use", "products": "view",
		"zones": []any{map[string]any{"zoneId": w.bar2, "access": "edit"}, map[string]any{"zoneId": w.bar3, "access": "view"}}})

	// Session: the WebGUI learns the rights.
	sess := e.expect(red, "GET", "/api/v1/auth/session", nil, 200, "")
	rights := asMap(asMap(sess["user"])["rights"])
	if rights["scope"] != "zones" || asMap(rights["zones"])[w.bar2] != "edit" || rights["createContent"] != false || rights["media"] != "use" {
		t.Fatalf("session rights %v", rights)
	}

	// Lists: B (edit), C (shared), G (global, shared); A runs only in Bar 1.
	list := asList(e.expect(red, "GET", "/api/v1/presentations", nil, 200, "")["presentations"])
	if got := idsOf(list); !slices.Equal(got, sorted(w.b, w.c, w.g)) {
		t.Fatalf("visible presentations %v", got)
	}
	for _, v := range list {
		acc := asMap(asMap(v)["access"])
		edit := asStr(asMap(v)["id"]) == w.b
		if acc["view"] != true || acc["edit"] != edit || acc["shared"] != !edit {
			t.Errorf("access of %v: %v", asMap(v)["name"], acc)
		}
	}
	if got := idsOf(asList(e.expect(red, "GET", "/api/v1/playlists", nil, 200, "")["playlists"])); !slices.Equal(got, sorted(w.l2, w.l3, w.lg)) {
		t.Fatalf("visible playlists %v", got)
	}
	search := e.expect(red, "GET", "/api/v1/content/search?q=", nil, 200, "")
	if got := idsOf(asList(search["presentations"])); !slices.Equal(got, sorted(w.b, w.c, w.g)) {
		t.Fatalf("search %v", got)
	}

	// Foreign content does not exist; shared content is read only.
	e.expect(red, "GET", "/api/v1/presentations/"+w.a, nil, 404, "")
	e.expect(red, "POST", "/api/v1/presentations/"+w.a+"/publish", nil, 404, "")
	detail := e.expect(red, "GET", "/api/v1/presentations/"+w.c, nil, 200, "")
	if acc := asMap(detail["access"]); acc["edit"] != false || acc["shared"] != true {
		t.Fatalf("C access %v", acc)
	}
	if cz := asList(detail["copyZones"]); len(cz) != 1 || cz[0] != w.bar2 {
		t.Fatalf("copy zones %v", cz)
	}
	docC := cntDoc(t, detail)
	e.expect(red, "PUT", "/api/v1/presentations/"+w.c+"/draft", map[string]any{"revision": asNum(detail["draftRevision"]), "doc": docC}, 403, "content_read_only")
	e.expect(red, "PATCH", "/api/v1/presentations/"+w.c, map[string]any{"name": "Mine"}, 403, "content_read_only")

	// Own zone: edit and publish.
	detailB := e.expect(red, "GET", "/api/v1/presentations/"+w.b, nil, 200, "")
	docB := cntDoc(t, detailB)
	docB.Slides[0].Name = "Changed by the editor"
	e.expect(red, "PUT", "/api/v1/presentations/"+w.b+"/draft", map[string]any{"revision": asNum(detailB["draftRevision"]), "doc": docB}, 200, "")
	e.expect(red, "POST", "/api/v1/presentations/"+w.b+"/publish", nil, 200, "")

	// Creating needs the right; templates stay with operators of all zones.
	e.expect(red, "POST", "/api/v1/presentations", map[string]any{"name": "New"}, 403, "content_create_denied")
	e.expect(red, "POST", "/api/v1/presentations/"+w.b+"/duplicate", nil, 403, "content_create_denied")
	e.expect(red, "POST", "/api/v1/playlists", map[string]any{"name": "New"}, 403, "content_create_denied")
	user := asMap(asList(e.expect(e.admin, "GET", "/api/v1/users", nil, 200, "")["users"])[1])
	if user["username"] != "redakteur" {
		t.Fatalf("users %v", user)
	}
	e.setRights(asStr(user["id"]), map[string]any{"scope": "zones", "createContent": true, "media": "use", "products": "view",
		"zones": []any{map[string]any{"zoneId": w.bar2, "access": "edit"}, map[string]any{"zoneId": w.bar3, "access": "view"}}})
	own := asStr(e.expect(red, "POST", "/api/v1/presentations", map[string]any{"name": "New"}, 200, "")["id"])
	if acc := asMap(e.expect(red, "GET", "/api/v1/presentations/"+own, nil, 200, "")["access"]); acc["edit"] != true {
		t.Fatalf("own new presentation %v", acc)
	}
	e.expect(red, "POST", "/api/v1/presentations/"+w.b+"/save-as-template", map[string]any{"name": "T"}, 403, "")

	// A playlist of the own zone may only contain presentations the editor sees.
	plB := e.expect(red, "GET", "/api/v1/playlists/"+w.l2, nil, 200, "")
	e.expect(red, "PUT", "/api/v1/playlists/"+w.l2+"/draft", map[string]any{"revision": asNum(plB["draftRevision"]),
		"doc": map[string]any{"id": w.l2, "name": "L2", "items": []any{rvItem("x", "presentation", w.a, nil)}}}, 422, "invalid_field")

	// Copy for the own zone: C stays in Bar 1, Bar 2 plays its own copy (through a copy of the
	// shared playlist L3).
	e.expect(red, "POST", "/api/v1/presentations/"+w.c+"/copy-for-zone", map[string]any{"zoneId": w.bar1}, 403, "zone_forbidden")
	e.expect(red, "POST", "/api/v1/presentations/"+w.b+"/copy-for-zone", map[string]any{"zoneId": w.bar2}, 409, "copy_not_shared")
	e.expect(red, "POST", "/api/v1/presentations/"+w.g+"/copy-for-zone", map[string]any{"zoneId": w.bar2}, 409, "copy_global_rule")
	copyC := e.expect(red, "POST", "/api/v1/presentations/"+w.c+"/copy-for-zone", map[string]any{"zoneId": w.bar2}, 200, "")
	cID := asStr(copyC["id"])
	if copyC["publishedVersion"] != float64(1) || copyC["hasChanges"] != false {
		t.Fatalf("the copy plays the published content: %v", copyC)
	}
	if acc := asMap(e.expect(red, "GET", "/api/v1/presentations/"+cID, nil, 200, "")["access"]); acc["edit"] != true {
		t.Fatalf("copy is editable: %v", acc)
	}
	bar2Rule := e.expect(e.admin, "GET", "/api/v1/schedules", nil, 200, "")
	var l3Copy string
	for _, r := range asList(bar2Rule["schedules"]) {
		switch asStr(asMap(r)["id"]) {
		case w.ruleBar1L3:
			if asStr(asMap(r)["playlistId"]) != w.l3 {
				t.Errorf("Bar 1 keeps L3: %v", r)
			}
		case w.ruleBar2L3:
			l3Copy = asStr(asMap(r)["playlistId"])
		}
	}
	if l3Copy == "" || l3Copy == w.l3 {
		t.Fatalf("Bar 2 plays a copy of L3: %q", l3Copy)
	}
	pubCopy := asMap(e.expect(e.admin, "GET", "/api/v1/playlists/"+l3Copy, nil, 200, ""))
	if pubCopy["publishedVersion"] != float64(1) || len(asList(asMap(pubCopy["draft"])["items"])) != 1 ||
		asStr(asMap(asList(asMap(pubCopy["draft"])["items"])[0])["refId"]) != cID {
		t.Fatalf("copied playlist %v", pubCopy)
	}
	e.expect(red, "GET", "/api/v1/presentations/"+w.c, nil, 404, "") // C now runs in Bar 1 only

	// Schedules: own zones and global rules are visible, only own zone rules editable.
	rules := idsOf(asList(e.expect(red, "GET", "/api/v1/schedules", nil, 200, "")["schedules"]))
	if slices.Contains(rules, w.ruleBar1) || !slices.Contains(rules, w.ruleBar2) {
		t.Fatalf("visible rules %v", rules)
	}
	win := map[string]any{"days": []int{6}, "start": "20:00", "end": "22:00"}
	e.expect(red, "POST", "/api/v1/schedules", map[string]any{"name": "x", "scope": "zone", "zoneId": w.bar1, "playlistId": w.l2, "window": win, "prices": []any{}}, 403, "")
	e.expect(red, "POST", "/api/v1/schedules", map[string]any{"name": "x", "scope": "global", "playlistId": w.l2, "window": win, "prices": []any{}}, 403, "")
	e.expect(red, "POST", "/api/v1/schedules", map[string]any{"name": "x", "scope": "zone", "zoneId": w.bar2, "playlistId": w.l1, "window": win, "prices": []any{}}, 422, "invalid_field")
	e.expect(red, "POST", "/api/v1/schedules", map[string]any{"name": "Weekend", "scope": "zone", "zoneId": w.bar2, "playlistId": w.l2, "window": win, "prices": []any{}}, 200, "")
	e.expect(red, "DELETE", "/api/v1/schedules/"+w.ruleBar1, nil, 403, "")
	e.expect(red, "GET", "/api/v1/schedules/now?zone="+w.bar1, nil, 404, "")
	e.expect(red, "GET", "/api/v1/schedules/now?zone="+w.bar3, nil, 200, "")

	// Zones: own ones only; closed playlist and ticker template yes, name no.
	if got := idsOf(asList(e.expect(red, "GET", "/api/v1/zones", nil, 200, "")["zones"])); !slices.Equal(got, sorted(w.bar2, w.bar3)) {
		t.Fatalf("visible zones %v", got)
	}
	z2 := asMap(e.expect(e.admin, "GET", "/api/v1/zones", nil, 200, "")["zones"].([]any)[1])
	e.expect(red, "PATCH", "/api/v1/zones/"+w.bar2, map[string]any{"revision": asNum(z2["revision"]), "name": "Renamed"}, 403, "")
	e.expect(red, "PATCH", "/api/v1/zones/"+w.bar2, map[string]any{"revision": asNum(z2["revision"]), "closedPlaylistId": w.l2}, 200, "")
	e.expect(red, "PATCH", "/api/v1/zones/"+w.bar3, map[string]any{"revision": 1, "closedPlaylistId": w.l2}, 403, "")

	// Default deny: everything not opened for limited operators.
	e.expect(red, "POST", "/api/v1/zones", map[string]any{"name": "Bar 4"}, 403, "")
	e.expect(red, "GET", "/api/v1/zones/"+w.bar2+"/usage", nil, 403, "")
	e.expect(red, "POST", "/api/v1/ticker-templates", map[string]any{"name": "T"}, 403, "")
	e.expect(red, "GET", "/api/v1/devices/pairing", nil, 403, "")
	e.expect(red, "GET", "/api/v1/users", nil, 403, "")
	e.expect(red, "GET", "/api/v1/settings", nil, 200, "")
}

func TestRightsMediaAndProducts(t *testing.T) {
	e := catSetup(t)
	bar := e.zone("Bar")
	cat := e.category("Beer", "")
	red := e.rightsUser("redakteur", map[string]any{"scope": "zones", "media": "use", "products": "view",
		"zones": []any{map[string]any{"zoneId": bar, "access": "edit"}}})
	userID := asStr(asMap(asList(e.expect(e.admin, "GET", "/api/v1/users", nil, 200, "")["users"])[1])["id"])
	adminAsset := e.admin.upload("admin.png", testImage(t, "png", 3), "")

	// use: browse, no uploads, no changes
	e.expect(red, "GET", "/api/v1/media", nil, 200, "")
	e.expect(red, "POST", "/api/v1/media/uploads", map[string]any{"filename": "x.png", "size": 10}, 403, "media_forbidden")
	e.expect(red, "PATCH", "/api/v1/media/"+adminAsset, map[string]any{"name": "x"}, 403, "media_forbidden")
	e.expect(red, "GET", "/api/v1/media/trash", nil, 403, "media_forbidden")

	// upload: own files and folders may change, foreign ones not, nothing is deleted
	e.setRights(userID, map[string]any{"scope": "zones", "media": "upload", "products": "view",
		"zones": []any{map[string]any{"zoneId": bar, "access": "edit"}}})
	mine := red.upload("mine.png", testImage(t, "png", 5), "")
	if by := asStr(e.expect(red, "GET", "/api/v1/media/"+mine, nil, 200, "")["uploadedBy"]); by != userID {
		t.Fatalf("uploadedBy %q", by)
	}
	e.expect(red, "PATCH", "/api/v1/media/"+mine, map[string]any{"name": "Mine"}, 200, "")
	e.expect(red, "PATCH", "/api/v1/media/"+adminAsset, map[string]any{"name": "x"}, 403, "media_not_own")
	e.expect(red, "POST", "/api/v1/media/uploads", map[string]any{"filename": "x.png", "size": 10, "replaceAssetId": adminAsset}, 403, "media_not_own")
	folder := asStr(e.expect(red, "POST", "/api/v1/media/folders", map[string]any{"name": "Mine"}, 200, "")["id"])
	e.expect(red, "PATCH", "/api/v1/media/folders/"+folder, map[string]any{"name": "Still mine"}, 200, "")
	e.expect(red, "DELETE", "/api/v1/media/"+mine, nil, 403, "media_forbidden")
	e.expect(red, "DELETE", "/api/v1/media/folders/"+folder, nil, 403, "media_forbidden")

	// manage: everything
	e.setRights(userID, map[string]any{"scope": "zones", "media": "manage", "products": "view",
		"zones": []any{map[string]any{"zoneId": bar, "access": "edit"}}})
	e.expect(red, "PATCH", "/api/v1/media/"+adminAsset, map[string]any{"name": "Renamed"}, 200, "")
	e.expect(red, "DELETE", "/api/v1/media/"+mine, nil, 204, "")

	// products: view only, then edit; the live status of the own zone always
	e.expect(red, "POST", "/api/v1/products", map[string]any{"name": "Pils", "categoryId": cat, "type": "drink",
		"price": map[string]any{"amount": 450, "currency": "EUR"}}, 403, "products_forbidden")
	pils := e.product("Pils", cat, 450)
	e.expect(red, "POST", "/api/v1/live/products/"+pils+"/status", map[string]any{"zoneId": bar, "status": "SOLD_OUT"}, 200, "")
	e.expect(red, "POST", "/api/v1/live/products/"+pils+"/status", map[string]any{"status": "SOLD_OUT"}, 403, "")
	e.setRights(userID, map[string]any{"scope": "zones", "media": "manage", "products": "edit",
		"zones": []any{map[string]any{"zoneId": bar, "access": "edit"}}})
	e.expect(red, "PATCH", "/api/v1/products/"+pils, map[string]any{"revision": 1, "name": "Pilsner"}, 200, "")
}

func TestRightsUsersAPI(t *testing.T) {
	e := catSetup(t)
	bar := e.zone("Bar")
	e.expect(e.admin, "POST", "/api/v1/users", map[string]any{"username": "x", "displayName": "x", "role": "operator",
		"password": "secret123", "passwordConfirm": "secret123", "active": true,
		"rights": map[string]any{"scope": "some", "media": "use", "products": "view", "zones": []any{}}}, 422, "invalid_field")
	e.expect(e.admin, "POST", "/api/v1/users", map[string]any{"username": "x", "displayName": "x", "role": "operator",
		"password": "secret123", "passwordConfirm": "secret123", "active": true,
		"rights": map[string]any{"scope": "zones", "media": "use", "products": "view", "zones": []any{map[string]any{"zoneId": "nope", "access": "edit"}}}}, 422, "invalid_field")
	// Users (bar staff) always have edit access to their zones (live operation), nothing else.
	u := e.expect(e.admin, "POST", "/api/v1/users", map[string]any{"username": "staff", "displayName": "Staff", "role": "user",
		"password": "secret123", "passwordConfirm": "secret123", "active": true,
		"rights": map[string]any{"scope": "zones", "createContent": true, "media": "manage", "products": "edit",
			"zones": []any{map[string]any{"zoneId": bar, "access": "view"}}}}, 200, "")
	r := asMap(u["rights"])
	if r["scope"] != "all" || r["createContent"] != false || r["media"] != "use" || asMap(asList(r["zones"])[0])["access"] != "edit" {
		t.Fatalf("user rights are normalized: %v", r)
	}
	if z := asList(u["zoneIds"]); len(z) != 1 || z[0] != bar {
		t.Fatalf("zoneIds %v", u["zoneIds"])
	}
	// Older clients send zoneIds: edit access.
	op := e.expect(e.admin, "PATCH", "/api/v1/users/"+asStr(u["id"]), map[string]any{"role": "operator", "zoneIds": []string{bar}}, 200, "")
	if asMap(asList(asMap(op["rights"])["zones"])[0])["access"] != "edit" {
		t.Fatalf("zoneIds %v", op)
	}
	if n := len(e.auditActions("user.updated")); n != 1 {
		t.Fatalf("audit %d", n)
	}
}
