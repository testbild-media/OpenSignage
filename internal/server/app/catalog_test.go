package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opensignage/internal/common/ids"
	"opensignage/internal/server/core"
	"opensignage/internal/server/devices/live"
)

// ---------------------------------------------------------------- shared helpers
// (prefixed with cat… so parallel modules cannot collide with their own test helpers)

type catEnv struct {
	t     *testing.T
	srv   *Server
	ts    *httptest.Server
	admin *client
	sub   *core.Subscription
}

// catSetup starts a server, runs the setup wizard (timezone Europe/Berlin) and returns the
// admin client plus a bus subscription for event assertions.
func catSetup(t *testing.T) *catEnv {
	t.Helper()
	srv, ts := newTestServer(t)
	c := newClient(t, ts)
	if code, out := c.do("POST", "/api/v1/setup", map[string]any{"username": "admin", "displayName": "Admin",
		"password": "secret123", "passwordConfirm": "secret123", "timezone": "Europe/Berlin"}); code != 200 {
		t.Fatalf("setup %d %v", code, out)
	}
	sub := srv.App.Bus.Subscribe(1024)
	t.Cleanup(sub.Close)
	return &catEnv{t: t, srv: srv, ts: ts, admin: c, sub: sub}
}

// login creates a user with the role (and zones) and returns a logged-in client.
func (e *catEnv) login(username, role string, zoneIDs ...string) *client {
	e.t.Helper()
	if zoneIDs == nil {
		zoneIDs = []string{}
	}
	if code, out := e.admin.do("POST", "/api/v1/users", map[string]any{"username": username, "displayName": username, "role": role,
		"password": "secret123", "passwordConfirm": "secret123", "active": true, "zoneIds": zoneIDs}); code != 200 {
		e.t.Fatalf("create user %d %v", code, out)
	}
	c := newClient(e.t, e.ts)
	if code, out := c.do("POST", "/api/v1/auth/login", map[string]any{"username": username, "password": "secret123"}); code != 200 {
		e.t.Fatalf("login %d %v", code, out)
	}
	return c
}

// seedAsset inserts a media asset directly (the media module is not under test here).
func (e *catEnv) seedAsset(kind string) string {
	e.t.Helper()
	id := ids.New()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := e.srv.App.DB.W.Exec(`INSERT INTO media_assets(id, name, kind, status, created_at, updated_at) VALUES (?, ?, ?, 'ready', ?, ?)`,
		id, kind+"-"+id[:8], kind, now, now); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// events returns and clears the events published since the last call.
func (e *catEnv) events() []core.Event {
	var out []core.Event
	for {
		select {
		case ev := <-e.sub.C:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func catHas(evs []core.Event, topic, zone string) bool {
	for _, ev := range evs {
		if ev.Topic == topic && ev.ZoneID == zone {
			return true
		}
	}
	return false
}

func catCount(evs []core.Event, topic string) int {
	n := 0
	for _, ev := range evs {
		if ev.Topic == topic {
			n++
		}
	}
	return n
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func asStr(v any) string {
	s, _ := v.(string)
	return s
}

func asNum(v any) float64 {
	f, _ := v.(float64)
	return f
}

// errCode extracts error.code from an error response.
func errCode(out map[string]any) string { return asStr(asMap(out["error"])["code"]) }

// errReason extracts error.params.reason (invalid_field responses).
func errReason(out map[string]any) string {
	return asStr(asMap(asMap(out["error"])["params"])["reason"])
}

// expect fails the test unless the status and (optionally) the error code match.
func (e *catEnv) expect(c *client, method, path string, body any, wantCode int, wantErr string) map[string]any {
	e.t.Helper()
	code, out := c.do(method, path, body)
	if code != wantCode {
		e.t.Fatalf("%s %s: status %d (want %d): %v", method, path, code, wantCode, out)
	}
	if wantErr != "" && errCode(out) != wantErr && errReason(out) != wantErr {
		e.t.Fatalf("%s %s: error %q/%q (want %q): %v", method, path, errCode(out), errReason(out), wantErr, out)
	}
	return out
}

// patchCur sends a PATCH to a zone, category or ticker template based on its current
// revision (read from the list as the admin), like an edit dialog opened just now.
func (e *catEnv) patchCur(c *client, path string, body map[string]any, wantCode int, wantErr string) map[string]any {
	e.t.Helper()
	b := map[string]any{"revision": e.catRev(path)}
	for k, v := range body {
		b[k] = v
	}
	return e.expect(c, "PATCH", path, b, wantCode, wantErr)
}

// catRev returns the current revision of the object at path (/api/v1/<resource>/<id>).
func (e *catEnv) catRev(path string) float64 {
	e.t.Helper()
	i := strings.LastIndexByte(path, '/')
	list, id := path[:i], path[i+1:]
	key := map[string]string{"/api/v1/zones": "zones", "/api/v1/categories": "categories", "/api/v1/ticker-templates": "tickerTemplates"}[list]
	for _, x := range asList(e.expect(e.admin, "GET", list, nil, 200, "")[key]) {
		if m := asMap(x); m["id"] == id {
			return asNum(m["revision"])
		}
	}
	return 1 // unknown object: the handler answers 404
}

func (e *catEnv) zone(name string) string {
	e.t.Helper()
	return asStr(e.expect(e.admin, "POST", "/api/v1/zones", map[string]any{"name": name}, 200, "")["id"])
}

func (e *catEnv) category(name, parent string) string {
	e.t.Helper()
	body := map[string]any{"name": name}
	if parent != "" {
		body["parentId"] = parent
	}
	return asStr(e.expect(e.admin, "POST", "/api/v1/categories", body, 200, "")["id"])
}

func (e *catEnv) product(name, category string, cents int) string {
	e.t.Helper()
	return asStr(e.expect(e.admin, "POST", "/api/v1/products", map[string]any{"name": name, "categoryId": category, "type": "drink",
		"price": map[string]any{"amount": cents, "currency": "EUR"}}, 200, "")["id"])
}

func (e *catEnv) auditActions(prefix string) []map[string]any {
	e.t.Helper()
	_, out := e.admin.do("GET", "/api/v1/audit?action="+prefix+"&limit=500", nil)
	var res []map[string]any
	for _, x := range asList(out["entries"]) {
		res = append(res, asMap(x))
	}
	return res
}

func toStrings(v any) []string {
	var out []string
	for _, x := range asList(v) {
		out = append(out, asStr(x))
	}
	return out
}

func catJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---------------------------------------------------------------- categories and products

func TestCatalogCategoriesAndProducts(t *testing.T) {
	e := catSetup(t)
	c := e.admin

	beer := e.category("Beer", "")
	wine := e.category("Wine", "")
	lager := e.category("Lager", beer)
	e.expect(c, "POST", "/api/v1/categories", map[string]any{"name": "Too deep", "parentId": lager}, 422, "invalid_parent")
	e.expect(c, "POST", "/api/v1/categories", map[string]any{"name": "Orphan", "parentId": ids.New()}, 422, "unknown_category")
	e.expect(c, "POST", "/api/v1/categories", map[string]any{"name": "  "}, 422, "invalid")
	e.expect(c, "POST", "/api/v1/categories", map[string]any{"name": "x", "bogus": 1}, 400, "bad_request")

	order := func() []string {
		out := e.expect(c, "GET", "/api/v1/categories", nil, 200, "")
		var names []string
		for _, x := range asList(out["categories"]) {
			names = append(names, asStr(asMap(x)["name"]))
		}
		return names
	}
	if got := strings.Join(order(), ","); got != "Beer,Lager,Wine" {
		t.Fatalf("category order %s", got)
	}
	e.expect(c, "POST", "/api/v1/categories/reorder", map[string]any{"ids": []string{wine, beer}}, 200, "")
	if got := strings.Join(order(), ","); got != "Wine,Beer,Lager" {
		t.Fatalf("category order after reorder %s", got)
	}
	e.expect(c, "POST", "/api/v1/categories/reorder", map[string]any{"ids": []string{wine, lager}}, 422, "mixed_levels")
	e.expect(c, "POST", "/api/v1/categories/reorder", map[string]any{"ids": []string{wine, wine}}, 422, "invalid")
	// A category with subcategories cannot become a subcategory (three levels).
	e.patchCur(c, "/api/v1/categories/"+beer, map[string]any{"parentId": wine}, 422, "has_subcategories")
	// Move the subcategory to the top level and back (null = top level).
	e.patchCur(c, "/api/v1/categories/"+lager, map[string]any{"parentId": nil}, 200, "")
	if got := strings.Join(order(), ","); got != "Wine,Beer,Lager" {
		t.Fatalf("order after moving to top level %s", got)
	}
	e.patchCur(c, "/api/v1/categories/"+lager, map[string]any{"parentId": beer, "name": "Lager beer"}, 200, "")

	img := e.seedAsset("image")
	vid := e.seedAsset("video")
	body := func(extra map[string]any) map[string]any {
		b := map[string]any{"name": "Pils", "description": "Cold", "categoryId": lager, "type": "drink",
			"quantity": map[string]any{"value": "0.33", "unit": "l"}, "price": map[string]any{"amount": 450, "currency": "EUR"}}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	// Validation
	e.expect(c, "POST", "/api/v1/products", body(map[string]any{"quantity": map[string]any{"value": "0,33", "unit": "l"}}), 422, "invalid")
	e.expect(c, "POST", "/api/v1/products", body(map[string]any{"quantity": map[string]any{"value": "0.33", "unit": "oz"}}), 422, "invalid")
	e.expect(c, "POST", "/api/v1/products", body(map[string]any{"price": map[string]any{"amount": -1, "currency": "EUR"}}), 422, "out_of_range")
	e.expect(c, "POST", "/api/v1/products", body(map[string]any{"price": map[string]any{"amount": 100, "currency": "eur"}}), 422, "invalid")
	e.expect(c, "POST", "/api/v1/products", body(map[string]any{"type": "gas"}), 422, "invalid_value")
	e.expect(c, "POST", "/api/v1/products", body(map[string]any{"categoryId": ids.New()}), 422, "unknown_category")
	e.expect(c, "POST", "/api/v1/products", body(map[string]any{"imageAssetId": vid}), 422, "wrong_asset_kind")
	e.expect(c, "POST", "/api/v1/products", body(map[string]any{"imageAssetId": ids.New()}), 422, "unknown_asset")
	e.expect(c, "POST", "/api/v1/products", body(map[string]any{"showImageInsteadOfName": true}), 422, "needs_image")

	e.events()
	created := e.expect(c, "POST", "/api/v1/products", body(map[string]any{"imageAssetId": img, "showImageInsteadOfName": true}), 200, "")
	pid := asStr(created["id"])
	if created["status"] != "AVAILABLE" || asNum(created["revision"]) != 1 || asMap(created["price"])["amount"] != float64(450) ||
		asMap(created["quantity"])["value"] != "0.33" || len(asMap(created["zoneStatus"])) != 0 {
		t.Fatalf("created product %v", created)
	}
	evs := e.events()
	if !catHas(evs, core.TopicProducts, "") || !catHas(evs, core.TopicLive, "") || catCount(evs, core.TopicContent) != 1 {
		t.Errorf("events after create (products, live, content because of the image): %v", evs)
	}
	var refs int
	if err := e.srv.App.DB.R.QueryRow(`SELECT COUNT(*) FROM content_refs WHERE owner_type = 'product' AND owner_id = ? AND asset_id = ?`, pid, img).Scan(&refs); err != nil || refs != 1 {
		t.Fatalf("product image ref: %d %v", refs, err)
	}

	// Optimistic concurrency
	updated := e.expect(c, "PATCH", "/api/v1/products/"+pid, map[string]any{"revision": 1, "name": "Pilsner", "quantity": nil, "imageAssetId": nil}, 200, "")
	if updated["name"] != "Pilsner" || asNum(updated["revision"]) != 2 || updated["quantity"] != nil || updated["imageAssetId"] != nil {
		t.Fatalf("updated %v", updated)
	}
	stale := e.expect(c, "PATCH", "/api/v1/products/"+pid, map[string]any{"revision": 1, "name": "Late"}, 409, "conflict")
	if asNum(asMap(asMap(stale["error"])["params"])["revision"]) != 2 {
		t.Errorf("conflict must carry the current revision: %v", stale)
	}
	e.expect(c, "PATCH", "/api/v1/products/"+pid, map[string]any{"name": "No revision"}, 422, "required")
	if err := e.srv.App.DB.R.QueryRow(`SELECT COUNT(*) FROM content_refs WHERE owner_type = 'product' AND owner_id = ?`, pid).Scan(&refs); err != nil || refs != 0 {
		t.Fatalf("image ref must be removed with the image: %d %v", refs, err)
	}

	// Listing, category filter (a top-level category includes its subcategories), archive
	second := e.product("Helles", lager, 400)
	other := e.product("Riesling", wine, 600)
	list := func(q string) []string {
		out := e.expect(c, "GET", "/api/v1/products"+q, nil, 200, "")
		var names []string
		for _, x := range asList(out["products"]) {
			names = append(names, asStr(asMap(x)["name"]))
		}
		return names
	}
	if got := strings.Join(list(""), ","); got != "Riesling,Pilsner,Helles" {
		t.Fatalf("products %s", got)
	}
	if got := strings.Join(list("?category="+beer), ","); got != "Pilsner,Helles" {
		t.Fatalf("category filter %s", got)
	}
	e.expect(c, "GET", "/api/v1/products?archived=maybe", nil, 422, "invalid")
	e.expect(c, "POST", "/api/v1/products/reorder", map[string]any{"ids": []string{second, pid}}, 200, "")
	if got := strings.Join(list("?category="+lager), ","); got != "Helles,Pilsner" {
		t.Fatalf("product order after reorder %s", got)
	}
	e.expect(c, "POST", "/api/v1/products/reorder", map[string]any{"ids": []string{second, other}}, 422, "mixed_categories")

	// Category deletion is blocked while products exist
	blocked := e.expect(c, "DELETE", "/api/v1/categories/"+wine, nil, 409, "category_not_empty")
	if asNum(asMap(asMap(blocked["error"])["params"])["products"]) != 1 {
		t.Errorf("category_not_empty params: %v", blocked)
	}
	e.expect(c, "DELETE", "/api/v1/categories/"+beer, nil, 409, "category_not_empty") // has a subcategory

	// Deletion only for archived products
	e.expect(c, "DELETE", "/api/v1/products/"+other, nil, 409, "product_not_archived")
	e.expect(c, "POST", "/api/v1/products/"+other+"/archive", nil, 200, "")
	if got := strings.Join(list(""), ","); got != "Helles,Pilsner" {
		t.Fatalf("archived product must leave the list: %s", got)
	}
	if got := strings.Join(list("?archived=1"), ","); got != "Riesling" {
		t.Fatalf("archive list %s", got)
	}
	e.expect(c, "POST", "/api/v1/products/"+other+"/restore", nil, 200, "")
	e.expect(c, "POST", "/api/v1/products/"+other+"/archive", nil, 200, "")
	e.expect(c, "DELETE", "/api/v1/products/"+other, nil, 204, "")
	e.expect(c, "DELETE", "/api/v1/categories/"+wine, nil, 204, "")
	e.expect(c, "GET", "/api/v1/products?category="+wine, nil, 200, "")

	// Audit trail has old and new values
	var found bool
	for _, a := range e.auditActions("product.updated") {
		if a["objectId"] == pid && strings.Contains(asStr(a["old"]), `"Pils"`) && strings.Contains(asStr(a["new"]), `"Pilsner"`) {
			found = true
		}
	}
	if !found {
		t.Error("product.updated audit entry with old and new values missing")
	}

	// Roles: user may read categories and products but not change them
	user := e.login("barkeeper", "user")
	e.expect(user, "GET", "/api/v1/products", nil, 200, "")
	e.expect(user, "GET", "/api/v1/categories", nil, 200, "")
	e.expect(user, "POST", "/api/v1/products", body(nil), 403, "forbidden")
	e.expect(user, "POST", "/api/v1/categories", map[string]any{"name": "x"}, 403, "forbidden")
	op := e.login("manager", "operator")
	e.expect(op, "POST", "/api/v1/categories", map[string]any{"name": "Spirits"}, 200, "")
}

func TestCatalogProductInUseProtection(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	cat := e.category("Drinks", "")
	p1 := e.product("Cola", cat, 300)
	p2 := e.product("Fanta", cat, 300)
	zone := e.zone("Bar")
	pl := asStr(e.expect(c, "POST", "/api/v1/playlists", map[string]any{"name": "Main"}, 200, "")["id"])
	e.expect(c, "POST", "/api/v1/playlists/"+pl+"/publish", nil, 200, "")

	// p1 is referenced by a schedule price, p2 by a presentation product element.
	rule := e.expect(c, "POST", "/api/v1/schedules", map[string]any{"name": "Happy hour", "scope": "zone", "zoneId": zone,
		"window": map[string]any{"days": []int{1, 2}, "start": "17:00", "end": "19:00"}, "playlistId": pl,
		"prices": []map[string]any{{"productId": p1, "price": map[string]any{"amount": 200, "currency": "EUR"}}}}, 200, "")
	pres := e.expect(c, "POST", "/api/v1/presentations", map[string]any{"name": "Menu", "width": 1280, "height": 720}, 200, "")
	presID := asStr(pres["id"])
	var doc map[string]any
	_ = json.Unmarshal([]byte(catJSON(pres["draft"])), &doc)
	slide := asMap(asList(doc["slides"])[0])
	slide["elements"] = []any{map[string]any{"id": "e1", "type": "product", "name": "", "x": 10, "y": 10, "w": 300, "h": 80, "rotation": 0,
		"opacity": 1, "locked": false, "hidden": false, "pinned": false, "productId": p2, "fields": map[string]any{"name": true, "quantity": false, "description": false, "price": true},
		"scale": 1, "priceAlign": "right"}}
	e.expect(c, "PUT", "/api/v1/presentations/"+presID+"/draft", map[string]any{"revision": 1, "doc": doc}, 200, "")

	for _, p := range []string{p1, p2} {
		e.expect(c, "POST", "/api/v1/products/"+p+"/archive", nil, 200, "")
	}
	in1 := e.expect(c, "DELETE", "/api/v1/products/"+p1, nil, 409, "product_in_use")
	if names := toStrings(asMap(asMap(in1["error"])["params"])["rules"]); len(names) != 1 || names[0] != "Happy hour" {
		t.Errorf("product_in_use must name the rule: %v", in1)
	}
	in2 := e.expect(c, "DELETE", "/api/v1/products/"+p2, nil, 409, "product_in_use")
	if names := toStrings(asMap(asMap(in2["error"])["params"])["presentations"]); len(names) != 1 || names[0] != "Menu" {
		t.Errorf("product_in_use must name the presentation: %v", in2)
	}

	// Free p1 by removing the price (revision bump), p2 by removing the element from draft and published version.
	e.expect(c, "PATCH", "/api/v1/schedules/"+asStr(rule["id"]), map[string]any{"revision": 1, "prices": []any{}}, 200, "")
	e.expect(c, "DELETE", "/api/v1/products/"+p1, nil, 204, "")
	e.expect(c, "POST", "/api/v1/presentations/"+presID+"/publish", nil, 200, "") // published version still mentions p2
	e.expect(c, "DELETE", "/api/v1/products/"+p2, nil, 409, "product_in_use")
	slide["elements"] = []any{}
	e.expect(c, "PUT", "/api/v1/presentations/"+presID+"/draft", map[string]any{"revision": 2, "doc": doc}, 200, "")
	e.expect(c, "DELETE", "/api/v1/products/"+p2, nil, 409, "product_in_use") // draft clean, published version not yet
	e.expect(c, "POST", "/api/v1/presentations/"+presID+"/publish", nil, 200, "")
	e.expect(c, "DELETE", "/api/v1/products/"+p2, nil, 204, "")
}

// ---------------------------------------------------------------- live operation

func TestLiveStatusAndRoles(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	cat := e.category("Drinks", "")
	pid := e.product("Cola", cat, 300)
	za, zb := e.zone("Bar A"), e.zone("Bar B")
	user := e.login("barkeeper", "user", za)
	op := e.login("manager", "operator")
	statusPath := "/api/v1/live/products/" + pid + "/status"

	product := func() map[string]any {
		out := e.expect(c, "GET", "/api/v1/products", nil, 200, "")
		return asMap(asList(out["products"])[0])
	}
	e.events()

	// Global status: all three values stay distinct end to end (an administrator may set all).
	for _, st := range []string{"SOLD_OUT", "UNAVAILABLE", "AVAILABLE", "SOLD_OUT"} {
		out := e.expect(c, "POST", statusPath, map[string]any{"status": st}, 200, "")
		if out["status"] != st || out["effective"] != st || out["changed"] != true {
			t.Fatalf("set %s: %v", st, out)
		}
		if product()["status"] != st {
			t.Fatalf("product status after %s: %v", st, product()["status"])
		}
		if evs := e.events(); !catHas(evs, core.TopicLive, "") {
			t.Fatalf("global status change must publish live for all zones: %v", evs)
		}
	}
	// Operators switch between available and sold out only; "not available" (not carried)
	// is the administrators' decision.
	e.expect(op, "POST", statusPath, map[string]any{"status": "AVAILABLE"}, 200, "")
	e.expect(op, "POST", statusPath, map[string]any{"status": "UNAVAILABLE"}, 403, "status_admin_only")
	e.expect(op, "POST", statusPath, map[string]any{"status": "SOLD_OUT"}, 200, "")
	e.events()
	if out := e.expect(op, "POST", statusPath, map[string]any{"status": "SOLD_OUT"}, 200, ""); out["changed"] != false {
		t.Errorf("repeating the same status is a no-op: %v", out)
	}
	if evs := e.events(); len(evs) != 0 {
		t.Errorf("a no-op must not publish events: %v", evs)
	}
	e.expect(op, "POST", statusPath, map[string]any{"status": "MAYBE"}, 422, "invalid_value")
	e.expect(op, "POST", statusPath, map[string]any{}, 422, "required")
	e.expect(op, "POST", statusPath, map[string]any{"status": nil}, 422, "required")
	e.expect(op, "POST", "/api/v1/live/products/"+ids.New()+"/status", map[string]any{"status": "SOLD_OUT"}, 404, "not_found")

	// Role user: global status is forbidden, own zone override allowed (available / sold
	// out), foreign zone forbidden, "not available" only by administrators.
	e.expect(user, "POST", statusPath, map[string]any{"status": "AVAILABLE"}, 403, "forbidden")
	e.expect(user, "POST", statusPath, map[string]any{"status": "AVAILABLE", "zoneId": zb}, 403, "forbidden")
	e.expect(user, "POST", statusPath, map[string]any{"status": "UNAVAILABLE", "zoneId": za}, 403, "status_admin_only")
	out := e.expect(user, "POST", statusPath, map[string]any{"status": "AVAILABLE", "zoneId": za}, 200, "")
	if out["override"] != "AVAILABLE" || out["effective"] != "AVAILABLE" || out["status"] != "SOLD_OUT" {
		t.Fatalf("zone override: %v", out)
	}
	if evs := e.events(); !catHas(evs, core.TopicLive, za) || catHas(evs, core.TopicLive, "") {
		t.Fatalf("zone override must publish live for that zone only: %v", evs)
	}
	// An administrator takes the product off the list in zone A; staff can neither change
	// nor lift that.
	e.expect(c, "POST", statusPath, map[string]any{"status": "UNAVAILABLE", "zoneId": za}, 200, "")
	e.expect(user, "POST", statusPath, map[string]any{"status": "SOLD_OUT", "zoneId": za}, 403, "status_admin_only")
	e.expect(user, "POST", statusPath, map[string]any{"status": nil, "zoneId": za}, 403, "status_admin_only")
	e.events()
	p := product()
	if p["status"] != "SOLD_OUT" || asMap(p["zoneStatus"])[za] != "UNAVAILABLE" {
		t.Fatalf("global stays, zone overrides: %v", p)
	}
	e.expect(op, "POST", statusPath, map[string]any{"status": "SOLD_OUT", "zoneId": ids.New()}, 422, "unknown_zone")

	// /control feed: user sees own zone only, with effective statuses.
	feed := e.expect(user, "GET", "/api/v1/live/zones", nil, 200, "")
	zs := asList(feed["zones"])
	if len(zs) != 1 || asMap(zs[0])["id"] != za {
		t.Fatalf("user must see only own zones: %v", feed)
	}
	entry := asMap(asList(asMap(zs[0])["products"])[0])
	if entry["status"] != "UNAVAILABLE" || entry["override"] != "UNAVAILABLE" {
		t.Errorf("effective status in zone A: %v", entry)
	}
	feed = e.expect(op, "GET", "/api/v1/live/zones", nil, 200, "")
	if len(asList(feed["zones"])) != 2 {
		t.Fatalf("operator sees all zones: %v", feed)
	}
	for _, z := range asList(feed["zones"]) {
		entry := asMap(asList(asMap(z)["products"])[0])
		want := "SOLD_OUT"
		if asMap(z)["id"] == za {
			want = "UNAVAILABLE"
		}
		if entry["status"] != want {
			t.Errorf("zone %v effective status %v, want %v", asMap(z)["name"], entry["status"], want)
		}
	}
	if len(asList(feed["products"])) != 1 || len(asList(feed["categories"])) != 1 {
		t.Errorf("feed must carry products and categories: %v", feed)
	}

	// Clearing the override restores the global status (an administrator lifts "not available").
	out = e.expect(c, "POST", statusPath, map[string]any{"status": nil, "zoneId": za}, 200, "")
	if out["override"] != nil || out["effective"] != "SOLD_OUT" || out["changed"] != true {
		t.Fatalf("clear override: %v", out)
	}
	if len(asMap(product()["zoneStatus"])) != 0 {
		t.Errorf("override must be removed: %v", product())
	}
	if out = e.expect(user, "POST", statusPath, map[string]any{"status": nil, "zoneId": za}, 200, ""); out["changed"] != false {
		t.Errorf("clearing twice is a no-op: %v", out)
	}

	// Audit: old/new and the zone.
	var zoneEntry, globalEntry map[string]any
	for _, a := range e.auditActions("product.") {
		switch a["action"] {
		case "product.zone_status":
			if zoneEntry == nil {
				zoneEntry = a // newest first = the clear
			}
		case "product.status":
			if globalEntry == nil {
				globalEntry = a
			}
		}
	}
	if zoneEntry == nil || zoneEntry["zoneId"] != za || zoneEntry["username"] != "admin" ||
		!strings.Contains(asStr(zoneEntry["old"]), "UNAVAILABLE") || !strings.Contains(asStr(zoneEntry["new"]), `"override":null`) {
		t.Errorf("zone status audit entry: %v", zoneEntry)
	}
	if globalEntry == nil || !strings.Contains(asStr(globalEntry["old"]), "AVAILABLE") || !strings.Contains(asStr(globalEntry["new"]), "SOLD_OUT") {
		t.Errorf("global status audit entry: %v", globalEntry)
	}

	// Archived products cannot be switched.
	e.expect(c, "POST", "/api/v1/products/"+pid+"/archive", nil, 200, "")
	e.expect(op, "POST", statusPath, map[string]any{"status": "AVAILABLE"}, 409, "product_archived")
}

func TestLiveZoneState(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	za, zb := e.zone("Bar A"), e.zone("Bar B")
	user := e.login("barkeeper", "user", za)
	e.events()

	// OPEN → CLOSED without a closed playlist: live only.
	out := e.expect(user, "POST", "/api/v1/live/zones/"+za+"/state", map[string]any{"state": "CLOSED"}, 200, "")
	if out["state"] != "CLOSED" || out["changed"] != true {
		t.Fatalf("state: %v", out)
	}
	evs := e.events()
	if !catHas(evs, core.TopicLive, za) || catCount(evs, core.TopicZones) != 0 {
		t.Fatalf("state change without closed playlist publishes live only: %v", evs)
	}
	e.expect(user, "POST", "/api/v1/live/zones/"+zb+"/state", map[string]any{"state": "CLOSED"}, 403, "forbidden")
	e.expect(user, "POST", "/api/v1/live/zones/"+za+"/state", map[string]any{"state": "HALF"}, 422, "invalid_value")
	if out = e.expect(user, "POST", "/api/v1/live/zones/"+za+"/state", map[string]any{"state": "CLOSED"}, 200, ""); out["changed"] != false {
		t.Errorf("same state is a no-op: %v", out)
	}
	e.expect(user, "POST", "/api/v1/live/zones/"+ids.New()+"/state", map[string]any{"state": "OPEN"}, 403, "forbidden")

	// With a closed playlist the zones topic follows (schedule consumers re-evaluate).
	pl := asStr(e.expect(c, "POST", "/api/v1/playlists", map[string]any{"name": "Closed"}, 200, "")["id"])
	e.patchCur(c, "/api/v1/zones/"+za, map[string]any{"closedPlaylistId": pl}, 200, "")
	e.events()
	e.expect(user, "POST", "/api/v1/live/zones/"+za+"/state", map[string]any{"state": "OPEN"}, 200, "")
	evs = e.events()
	if !catHas(evs, core.TopicLive, za) || !catHas(evs, core.TopicZones, za) {
		t.Fatalf("state change with closed playlist publishes live and zones: %v", evs)
	}

	// The operator UI has no ticker any more: no endpoint, no ticker in the feed.
	e.expect(user, "POST", "/api/v1/live/zones/"+za+"/ticker", map[string]any{"text": "x", "visible": true}, 404, "not_found")
	feed := e.expect(user, "GET", "/api/v1/live/zones", nil, 200, "")
	if _, ok := asMap(asList(feed["zones"])[0])["ticker"]; ok {
		t.Errorf("the operator feed must not carry a ticker: %v", asList(feed["zones"])[0])
	}
}

// ---------------------------------------------------------------- zones and ticker templates

func TestZonesLifecycleAndDeleteMakesDevicesZoneless(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	z1, z2 := e.zone("Counter"), e.zone("Cocktail bar")

	zs := asList(e.expect(c, "GET", "/api/v1/zones", nil, 200, "")["zones"])
	if len(zs) != 2 || asMap(zs[0])["name"] != "Counter" || asMap(zs[0])["state"] != "OPEN" {
		t.Fatalf("zones %v", zs)
	}
	e.patchCur(c, "/api/v1/zones/"+z2, map[string]any{"name": "Cocktails", "sort": 5}, 200, "")
	e.patchCur(c, "/api/v1/zones/"+z2, map[string]any{"closedPlaylistId": ids.New()}, 422, "unknown_playlist")
	e.patchCur(c, "/api/v1/zones/"+z2, map[string]any{"tickerTemplateId": ids.New()}, 422, "unknown_ticker_template")
	e.expect(c, "POST", "/api/v1/zones", map[string]any{"name": ""}, 422, "invalid")

	// Users with assigned zones see only those.
	user := e.login("barkeeper", "user", z1)
	zs = asList(e.expect(user, "GET", "/api/v1/zones", nil, 200, "")["zones"])
	if len(zs) != 1 || asMap(zs[0])["id"] != z1 {
		t.Fatalf("user zones %v", zs)
	}
	e.patchCur(user, "/api/v1/zones/"+z1, map[string]any{"name": "x"}, 403, "forbidden")
	e.expect(user, "DELETE", "/api/v1/zones/"+z1, nil, 403, "forbidden")

	// Devices and rules in z1.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var devIDs []string
	for i := 0; i < 2; i++ {
		id := ids.New()
		devIDs = append(devIDs, id)
		if _, err := e.srv.App.DB.W.Exec(`INSERT INTO devices(id, hardware_id, name, zone_id, config, created_at, updated_at) VALUES (?, ?, ?, ?, '{}', ?, ?)`,
			id, "hw-"+id, "Screen", z1, now, now); err != nil {
			t.Fatal(err)
		}
	}
	e.expect(c, "POST", "/api/v1/schedules", map[string]any{"name": "Default", "scope": "default", "zoneId": z1, "prices": []any{},
		"playlistId": asStr(e.expect(c, "POST", "/api/v1/playlists", map[string]any{"name": "P"}, 200, "")["id"])}, 200, "")
	usage := e.expect(c, "GET", "/api/v1/zones/"+z1+"/usage", nil, 200, "")
	if asNum(usage["devices"]) != 2 || asNum(usage["rules"]) != 1 || asNum(usage["users"]) != 1 {
		t.Fatalf("usage %v", usage)
	}
	if zs = asList(e.expect(c, "GET", "/api/v1/zones", nil, 200, "")["zones"]); asNum(asMap(zs[0])["deviceCount"]) != 2 {
		t.Errorf("deviceCount %v", zs)
	}
	e.expect(c, "GET", "/api/v1/zones/"+ids.New()+"/usage", nil, 404, "not_found")

	e.events()
	del := e.expect(c, "DELETE", "/api/v1/zones/"+z1, nil, 200, "")
	if asNum(del["devicesAffected"]) != 2 || asNum(del["rulesDeleted"]) != 1 {
		t.Fatalf("delete response %v", del)
	}
	evs := e.events()
	if !catHas(evs, core.TopicZones, z1) || !catHas(evs, core.TopicLive, z1) || !catHas(evs, core.TopicContent, z1) {
		t.Errorf("zone deletion events: %v", evs)
	}
	for _, id := range devIDs {
		var zone *string
		if err := e.srv.App.DB.R.QueryRow(`SELECT zone_id FROM devices WHERE id = ?`, id).Scan(&zone); err != nil {
			t.Fatalf("device must survive: %v", err)
		}
		if zone != nil {
			t.Errorf("device must be zone-less after the zone was deleted, still in %s", *zone)
		}
	}
	var n int
	if err := e.srv.App.DB.R.QueryRow(`SELECT COUNT(*) FROM schedule_rules`).Scan(&n); err != nil || n != 0 {
		t.Errorf("zone rules must go with the zone: %d %v", n, err)
	}
	if err := e.srv.App.DB.R.QueryRow(`SELECT COUNT(*) FROM user_zones`).Scan(&n); err != nil || n != 0 {
		t.Errorf("user assignment must go with the zone: %d %v", n, err)
	}
	e.expect(c, "DELETE", "/api/v1/zones/"+z1, nil, 404, "not_found")
	var audited bool
	for _, a := range e.auditActions("zone.deleted") {
		audited = audited || (a["objectId"] == z1 && strings.Contains(asStr(a["new"]), `"devicesAffected":2`))
	}
	if !audited {
		t.Error("zone.deleted audit entry missing")
	}
}

func TestTickerTemplates(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	tpl := map[string]any{"font": "Montserrat", "size": 36, "color": "#ffffff", "background": "#000000", "speed": 150,
		"direction": "left", "height": 70, "position": "bottom"}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for kk, vv := range tpl {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	// Bundled manifest is absent in tests: Montserrat is accepted, everything else needs an upload.
	e.expect(c, "POST", "/api/v1/ticker-templates", map[string]any{"name": "Bad", "template": with("font", "Papyrus")}, 422, "unknown_font")
	e.expect(c, "POST", "/api/v1/ticker-templates", map[string]any{"name": "Bad", "template": with("color", "white")}, 422, "invalid_color")
	e.expect(c, "POST", "/api/v1/ticker-templates", map[string]any{"name": "Bad", "template": with("speed", 0)}, 422, "out_of_range")
	e.expect(c, "POST", "/api/v1/ticker-templates", map[string]any{"name": "Bad", "template": with("position", "left")}, 422, "invalid_value")
	e.expect(c, "POST", "/api/v1/ticker-templates", map[string]any{"name": "Bad"}, 422, "required")
	e.expect(c, "POST", "/api/v1/ticker-templates", map[string]any{"template": tpl}, 422, "required")

	// Uploaded fonts count too.
	if _, err := e.srv.App.DB.W.Exec(`INSERT INTO blobs(sha256, size, mime, created_at) VALUES ('abc', 1, 'font/woff2', 'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.App.DB.W.Exec(`INSERT INTO font_faces(id, family, style, weight_min, weight_max, blob_sha256, format, license, created_at)
		VALUES ('f1', 'Papyrus', 'normal', 400, 400, 'abc', 'woff2', 'OFL', 'now')`); err != nil {
		t.Fatal(err)
	}
	created := e.expect(c, "POST", "/api/v1/ticker-templates", map[string]any{"name": "Standard", "template": with("font", "Papyrus")}, 200, "")
	id := asStr(created["id"])
	if asMap(created["template"])["font"] != "Papyrus" {
		t.Fatalf("created %v", created)
	}
	list := e.expect(c, "GET", "/api/v1/ticker-templates", nil, 200, "")
	if len(asList(list["tickerTemplates"])) != 1 || asMap(list["default"])["font"] != "Montserrat" {
		t.Fatalf("list %v", list)
	}
	upd := e.patchCur(c, "/api/v1/ticker-templates/"+id, map[string]any{"template": with("size", 48)}, 200, "")
	if asNum(asMap(upd["template"])["size"]) != 48 || upd["name"] != "Standard" {
		t.Fatalf("updated %v", upd)
	}
	e.patchCur(c, "/api/v1/ticker-templates/"+id, map[string]any{"template": with("font", "Nope")}, 422, "unknown_font")

	zone := e.zone("Bar")
	e.patchCur(c, "/api/v1/zones/"+zone, map[string]any{"tickerTemplateId": id}, 200, "")
	e.events()
	e.patchCur(c, "/api/v1/ticker-templates/"+id, map[string]any{"name": "Renamed", "template": with("size", 50)}, 200, "")
	if !catHas(e.events(), core.TopicLive, zone) {
		t.Error("changing a template must publish live for the zones using it")
	}
	inUse := e.expect(c, "DELETE", "/api/v1/ticker-templates/"+id, nil, 409, "ticker_template_in_use")
	if names := toStrings(asMap(asMap(inUse["error"])["params"])["zones"]); len(names) != 1 || names[0] != "Bar" {
		t.Errorf("in-use params %v", inUse)
	}
	e.patchCur(c, "/api/v1/zones/"+zone, map[string]any{"tickerTemplateId": nil}, 200, "")
	// A time schedule rule whose ticker uses the template keeps it as well.
	rule := e.expect(c, "POST", "/api/v1/schedules", map[string]any{"name": "Happy Hour", "scope": "zone", "zoneId": zone,
		"window": map[string]any{"days": []int{1, 2, 3, 4, 5}, "start": "17:00", "end": "19:00"},
		"ticker": map[string]any{"text": "  Happy Hour  ", "templateId": id}}, 200, "")
	if tk := asMap(rule["ticker"]); tk["text"] != "Happy Hour" || tk["templateId"] != id || rule["playlistId"] != nil {
		t.Fatalf("ticker-only rule: %v", rule)
	}
	inUse = e.expect(c, "DELETE", "/api/v1/ticker-templates/"+id, nil, 409, "ticker_template_in_use")
	if names := toStrings(asMap(asMap(inUse["error"])["params"])["rules"]); len(names) != 1 || names[0] != "Happy Hour" {
		t.Errorf("in-use by rule params %v", inUse)
	}
	// Removing the ticker (null) frees it; a rule without playlist, prices and ticker is refused.
	e.patchCur(c, "/api/v1/schedules/"+asStr(rule["id"]), map[string]any{"ticker": nil}, 422, "playlist_prices_or_ticker_required")
	e.expect(c, "DELETE", "/api/v1/schedules/"+asStr(rule["id"]), nil, 204, "")
	e.expect(c, "DELETE", "/api/v1/ticker-templates/"+id, nil, 204, "")
	e.expect(c, "DELETE", "/api/v1/ticker-templates/"+id, nil, 404, "not_found")
}

// ---------------------------------------------------------------- time schedules

func TestSchedulesValidationAndRevisions(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	z1, z2 := e.zone("Zone 1"), e.zone("Zone 2")
	cat := e.category("Drinks", "")
	prod := e.product("Cola", cat, 300)
	mkPlaylist := func(name string, publish bool) string {
		id := asStr(e.expect(c, "POST", "/api/v1/playlists", map[string]any{"name": name}, 200, "")["id"])
		if publish {
			e.expect(c, "POST", "/api/v1/playlists/"+id+"/publish", nil, 200, "")
		}
		return id
	}
	p1 := mkPlaylist("P1", true)
	win := func(days []int, start, end string) map[string]any {
		return map[string]any{"days": days, "start": start, "end": end}
	}
	rule := func(name, scope, zone string, w map[string]any, extra map[string]any) map[string]any {
		b := map[string]any{"name": name, "scope": scope, "playlistId": p1, "prices": []any{}}
		if zone != "" {
			b["zoneId"] = zone
		}
		if w != nil {
			b["window"] = w
		}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}

	// Structural validation
	e.expect(c, "POST", "/api/v1/schedules", rule("", "zone", z1, win([]int{1}, "10:00", "11:00"), nil), 422, "required")
	e.expect(c, "POST", "/api/v1/schedules", rule("X", "zone", z1, win([]int{8}, "10:00", "11:00"), nil), 422, "invalid")
	e.expect(c, "POST", "/api/v1/schedules", rule("X", "zone", z1, win([]int{1}, "25:00", "11:00"), nil), 422, "invalid")
	e.expect(c, "POST", "/api/v1/schedules", rule("X", "zone", z1, nil, nil), 422, "required")
	e.expect(c, "POST", "/api/v1/schedules", rule("X", "global", z1, win([]int{1}, "10:00", "11:00"), nil), 422, "must_be_empty")
	e.expect(c, "POST", "/api/v1/schedules", rule("X", "zone", ids.New(), win([]int{1}, "10:00", "11:00"), nil), 422, "unknown_zone")
	e.expect(c, "POST", "/api/v1/schedules", rule("X", "zone", z1, win([]int{1}, "10:00", "11:00"), map[string]any{"playlistId": ids.New()}), 422, "unknown_playlist")
	e.expect(c, "POST", "/api/v1/schedules", rule("X", "zone", z1, win([]int{1}, "10:00", "11:00"),
		map[string]any{"prices": []map[string]any{{"productId": ids.New(), "price": map[string]any{"amount": 1, "currency": "EUR"}}}}), 422, "unknown_product")
	e.expect(c, "POST", "/api/v1/schedules", rule("X", "zone", z1, win([]int{1}, "10:00", "11:00"),
		map[string]any{"playlistId": nil, "prices": []any{}}), 422, "playlist_prices_or_ticker_required")
	e.expect(c, "POST", "/api/v1/schedules", rule("X", "zone", z1, win([]int{1}, "10:00", "11:00"),
		map[string]any{"ticker": map[string]any{"text": "   "}}), 422, "required")
	e.expect(c, "POST", "/api/v1/schedules", rule("X", "zone", z1, win([]int{1}, "10:00", "11:00"),
		map[string]any{"ticker": map[string]any{"text": strings.Repeat("x", 2001)}}), 422, "too_long")
	e.expect(c, "POST", "/api/v1/schedules", rule("X", "zone", z1, win([]int{1}, "10:00", "11:00"),
		map[string]any{"ticker": map[string]any{"text": "Hi", "templateId": ids.New()}}), 422, "unknown_ticker_template")
	archived := mkPlaylist("Old", false)
	e.expect(c, "DELETE", "/api/v1/playlists/"+archived, nil, 204, "")
	e.expect(c, "POST", "/api/v1/schedules", rule("X", "zone", z1, win([]int{1}, "10:00", "11:00"), map[string]any{"playlistId": archived}), 422, "unknown_playlist")

	// Overlap: Mon–Fri evening, and a midnight-crossing Friday night window.
	evening := e.expect(c, "POST", "/api/v1/schedules", rule("Evening", "zone", z1, win([]int{1, 2, 3, 4, 5}, "17:00", "23:00"), nil), 200, "")
	eveningID := asStr(evening["id"])
	if asNum(evening["revision"]) != 1 || evening["enabled"] != true {
		t.Fatalf("created rule %v", evening)
	}
	overlap := e.expect(c, "POST", "/api/v1/schedules", rule("Late", "zone", z1, win([]int{3}, "22:00", "23:30"), nil), 409, "window_overlap")
	if asMap(asMap(overlap["error"])["params"])["rule"] != "Evening" {
		t.Errorf("window_overlap must name the conflicting rule: %v", overlap)
	}
	e.expect(c, "POST", "/api/v1/schedules", rule("Night", "zone", z1, win([]int{5}, "22:00", "02:00"), nil), 409, "window_overlap") // Fri 22–23 overlaps
	// Adjacent windows do not overlap (end is exclusive).
	e.expect(c, "POST", "/api/v1/schedules", rule("Adjacent", "zone", z1, win([]int{1}, "23:00", "23:30"), nil), 200, "")
	// Midnight wrap: Saturday 22:00 → Sunday 02:00, then Sunday 01:00–03:00 overlaps, 02:00–03:00 does not.
	e.expect(c, "POST", "/api/v1/schedules", rule("Sat night", "zone", z1, win([]int{6}, "22:00", "02:00"), nil), 200, "")
	e.expect(c, "POST", "/api/v1/schedules", rule("Sun early", "zone", z1, win([]int{7}, "01:00", "03:00"), nil), 409, "window_overlap")
	e.expect(c, "POST", "/api/v1/schedules", rule("Sun later", "zone", z1, win([]int{7}, "02:00", "03:00"), nil), 200, "")
	// Week wrap: Sunday 23:00 → Monday 01:00 collides with Monday 00:30.
	e.expect(c, "POST", "/api/v1/schedules", rule("Sun late", "zone", z1, win([]int{7}, "23:00", "01:00"), nil), 200, "")
	e.expect(c, "POST", "/api/v1/schedules", rule("Mon 00:30", "zone", z1, win([]int{1}, "00:30", "00:45"), nil), 409, "window_overlap")
	// Same window in another zone is fine.
	e.expect(c, "POST", "/api/v1/schedules", rule("Evening Z2", "zone", z2, win([]int{1, 2, 3, 4, 5}, "17:00", "23:00"), nil), 200, "")
	// A global rule may overlap zone rules but not another global rule.
	g := e.expect(c, "POST", "/api/v1/schedules", rule("Global lunch", "global", "", win([]int{1, 2, 3, 4, 5}, "11:00", "14:00"), nil), 200, "")
	e.expect(c, "POST", "/api/v1/schedules", rule("Global evening", "global", "", win([]int{1, 2, 3}, "18:00", "20:00"), nil), 200, "") // overlaps zone rule: allowed
	e.expect(c, "POST", "/api/v1/schedules", rule("Global clash", "global", "", win([]int{2}, "13:00", "15:00"), nil), 409, "window_overlap")
	// Disabled rules do not take part; re-enabling is checked again.
	dis := e.expect(c, "POST", "/api/v1/schedules", rule("Disabled", "zone", z1, win([]int{1}, "18:00", "19:00"), map[string]any{"enabled": false}), 200, "")
	e.expect(c, "PATCH", "/api/v1/schedules/"+asStr(dis["id"]), map[string]any{"revision": 1, "enabled": true}, 409, "window_overlap")
	// Moving a rule onto a free window works, onto a taken one does not (and a rule never conflicts with itself).
	e.expect(c, "PATCH", "/api/v1/schedules/"+eveningID, map[string]any{"revision": 1, "window": win([]int{1, 2, 3, 4, 5}, "17:30", "22:30")}, 200, "")

	// Default rule: at most one per zone, never with a window.
	e.expect(c, "POST", "/api/v1/schedules", rule("Default", "default", z1, nil, nil), 200, "")
	dup := e.expect(c, "POST", "/api/v1/schedules", rule("Default 2", "default", z1, nil, nil), 409, "default_exists")
	if asMap(asMap(dup["error"])["params"])["rule"] != "Default" {
		t.Errorf("default_exists params %v", dup)
	}
	e.expect(c, "POST", "/api/v1/schedules", rule("Default Z2", "default", z2, nil, nil), 200, "")
	e.expect(c, "POST", "/api/v1/schedules", rule("Default window", "default", z2, win([]int{1}, "10:00", "11:00"), nil), 422, "must_be_empty")
	e.expect(c, "POST", "/api/v1/schedules", rule("Default global", "default", "", nil, nil), 422, "required")

	// Revision bump on every change, price changes included; stale revision conflicts.
	priced := e.expect(c, "POST", "/api/v1/schedules", rule("Happy hour", "zone", z2, win([]int{6}, "16:00", "18:00"),
		map[string]any{"prices": []map[string]any{{"productId": prod, "price": map[string]any{"amount": 200, "currency": "EUR"}}}}), 200, "")
	pid := asStr(priced["id"])
	if asNum(priced["revision"]) != 1 {
		t.Fatalf("revision %v", priced["revision"])
	}
	e.events()
	changed := e.expect(c, "PATCH", "/api/v1/schedules/"+pid, map[string]any{"revision": 1,
		"prices": []map[string]any{{"productId": prod, "price": map[string]any{"amount": 150, "currency": "EUR"}}}}, 200, "")
	if asNum(changed["revision"]) != 2 || asNum(asMap(asList(changed["prices"])[0])["price"].(map[string]any)["amount"]) != 150 {
		t.Fatalf("a price change must bump the revision: %v", changed)
	}
	evs := e.events()
	if !catHas(evs, core.TopicZones, z2) || !catHas(evs, core.TopicContent, z2) || !catHas(evs, core.TopicLive, z2) {
		t.Errorf("price change publishes zones, content and live: %v", evs)
	}
	e.expect(c, "PATCH", "/api/v1/schedules/"+pid, map[string]any{"revision": 1, "name": "Late"}, 409, "conflict")
	renamed := e.expect(c, "PATCH", "/api/v1/schedules/"+pid, map[string]any{"revision": 2, "name": "Happy hour 2"}, 200, "")
	if asNum(renamed["revision"]) != 3 {
		t.Fatalf("revision after rename %v", renamed["revision"])
	}
	// A non-price rule change does not publish live.
	e.events()
	e.expect(c, "PATCH", "/api/v1/schedules/"+eveningID, map[string]any{"revision": 2, "name": "Evening!"}, 200, "")
	if evs = e.events(); catHas(evs, core.TopicLive, z1) || !catHas(evs, core.TopicZones, z1) {
		t.Errorf("non-price change: zones+content but no live: %v", evs)
	}

	// Listing: zone filter returns the zone's rules plus global ones.
	list := asList(e.expect(c, "GET", "/api/v1/schedules?zone="+z2, nil, 200, "")["schedules"])
	scopes := map[string]int{}
	for _, r := range list {
		m := asMap(r)
		scopes[asStr(m["scope"])]++
		if m["scope"] == "zone" && m["zoneId"] != z2 {
			t.Errorf("zone filter leaked %v", m)
		}
	}
	if scopes["global"] != 2 || scopes["zone"] != 2 || scopes["default"] != 1 {
		t.Errorf("zone listing scopes %v", scopes)
	}

	// Delete
	e.events()
	e.expect(c, "DELETE", "/api/v1/schedules/"+asStr(g["id"]), nil, 204, "")
	if evs = e.events(); !catHas(evs, core.TopicZones, "") || !catHas(evs, core.TopicContent, "") {
		t.Errorf("deleting a global rule publishes for all zones: %v", evs)
	}
	e.expect(c, "DELETE", "/api/v1/schedules/"+asStr(g["id"]), nil, 404, "not_found")

	// Roles
	user := e.login("barkeeper", "user", z1)
	e.expect(user, "GET", "/api/v1/schedules", nil, 403, "forbidden")
	op := e.login("manager", "operator")
	e.expect(op, "GET", "/api/v1/schedules", nil, 200, "")

	// Audit with old/new
	var audited bool
	for _, a := range e.auditActions("schedule.updated") {
		audited = audited || (a["objectId"] == pid && strings.Contains(asStr(a["old"]), `"amount":200`) && strings.Contains(asStr(a["new"]), `"amount":150`))
	}
	if !audited {
		t.Error("schedule.updated audit entry with old and new prices missing")
	}
}

func TestSchedulesNow(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	z := e.zone("Bar")
	cat := e.category("Drinks", "")
	cola := e.product("Cola", cat, 300)
	beer := e.product("Beer", cat, 400)
	mk := func(name string, publish bool) string {
		id := asStr(e.expect(c, "POST", "/api/v1/playlists", map[string]any{"name": name}, 200, "")["id"])
		if publish {
			e.expect(c, "POST", "/api/v1/playlists/"+id+"/publish", nil, 200, "")
		}
		return id
	}
	pDefault, pEvening, pGlobal, pClosed, pDraft := mk("Default", true), mk("Evening", true), mk("Global", true), mk("Closed", true), mk("Draft only", false)
	win := func(days []int, s, en string) map[string]any {
		return map[string]any{"days": days, "start": s, "end": en}
	}
	post := func(b map[string]any) string {
		return asStr(e.expect(c, "POST", "/api/v1/schedules", b, 200, "")["id"])
	}
	money := func(a int) map[string]any { return map[string]any{"amount": a, "currency": "EUR"} }

	defaultRule := post(map[string]any{"name": "Default", "scope": "default", "zoneId": z, "playlistId": pDefault, "prices": []any{}})
	eveningRule := post(map[string]any{"name": "Evening", "scope": "zone", "zoneId": z, "playlistId": pEvening,
		"window": win([]int{1, 2, 3, 4, 5}, "17:00", "23:00"), "prices": []map[string]any{{"productId": cola, "price": money(200)}}})
	globalRule := post(map[string]any{"name": "Lunch", "scope": "global", "playlistId": pGlobal,
		"window": win([]int{1, 2, 3, 4, 5}, "11:00", "14:00"),
		"prices": []map[string]any{{"productId": cola, "price": money(250)}, {"productId": beer, "price": money(350)}}})
	// A price-only rule leaves the content unchanged; a rule with an unpublished playlist is skipped.
	post(map[string]any{"name": "Beer promo", "scope": "global", "window": win([]int{1}, "15:00", "16:00"), "prices": []map[string]any{{"productId": beer, "price": money(300)}}})
	post(map[string]any{"name": "Draft rule", "scope": "zone", "zoneId": z, "playlistId": pDraft, "window": win([]int{2}, "08:00", "09:00"), "prices": []any{}})

	now := func(at string) map[string]any {
		return e.expect(c, "GET", "/api/v1/schedules/now?zone="+z+"&at="+at, nil, 200, "")
	}
	// 2026-10-12 is a Monday; the server timezone is Europe/Berlin (UTC+2 in October).
	r := now("2026-10-12T18:00:00%2B02:00")
	if r["kind"] != "rule" || asMap(r["playlist"])["id"] != pEvening || r["ruleId"] != eveningRule || r["timezone"] != "Europe/Berlin" {
		t.Fatalf("Monday 18:00 zone window wins: %v", r)
	}
	if asNum(asMap(asMap(r["prices"])[cola])["amount"]) != 200 {
		t.Errorf("zone price: %v", r["prices"])
	}
	// The same instant in UTC is 16:00 UTC; evaluation happens in the configured zone, not UTC.
	r = now("2026-10-12T16:00:00Z")
	if r["ruleId"] != eveningRule {
		t.Fatalf("evaluation must use the settings timezone: %v", r)
	}
	r = now("2026-10-12T12:00:00%2B02:00")
	if r["ruleId"] != globalRule || asMap(r["playlist"])["id"] != pGlobal {
		t.Fatalf("Monday 12:00 global window: %v", r)
	}
	if asNum(asMap(asMap(r["prices"])[beer])["amount"]) != 350 {
		t.Errorf("global price: %v", r["prices"])
	}
	r = now("2026-10-12T15:30:00%2B02:00") // price-only rule active, default content
	if r["ruleId"] != defaultRule || asNum(asMap(asMap(r["prices"])[beer])["amount"]) != 300 || len(asList(r["activeRules"])) != 2 {
		t.Fatalf("price-only rule keeps the default playlist: %v", r)
	}
	r = now("2026-10-13T08:30:00%2B02:00") // Tuesday: draft-only playlist rule is skipped
	if r["ruleId"] != defaultRule {
		t.Fatalf("rule with an unpublished playlist is skipped: %v", r)
	}

	// CLOSED with closed playlist beats everything.
	e.patchCur(c, "/api/v1/zones/"+z, map[string]any{"closedPlaylistId": pClosed}, 200, "")
	e.expect(c, "POST", "/api/v1/live/zones/"+z+"/state", map[string]any{"state": "CLOSED"}, 200, "")
	r = now("2026-10-12T18:00:00%2B02:00")
	if r["kind"] != "closed" || asMap(r["playlist"])["id"] != pClosed || r["zoneState"] != "CLOSED" {
		t.Fatalf("closed zone: %v", r)
	}
	e.expect(c, "POST", "/api/v1/live/zones/"+z+"/state", map[string]any{"state": "OPEN"}, 200, "")

	// With the closed playlist removed and the default rule gone, nothing applies: cleared.
	e.patchCur(c, "/api/v1/zones/"+z, map[string]any{"closedPlaylistId": nil}, 200, "")
	e.expect(c, "DELETE", "/api/v1/schedules/"+defaultRule, nil, 204, "")
	r = now("2026-10-13T03:00:00%2B02:00")
	if r["kind"] != "cleared" || r["playlist"] != nil {
		t.Fatalf("nothing applies: %v", r)
	}
	// Live (no `at`) works and errors are clean.
	e.expect(c, "GET", "/api/v1/schedules/now?zone="+z, nil, 200, "")
	e.expect(c, "GET", "/api/v1/schedules/now", nil, 422, "required")
	e.expect(c, "GET", "/api/v1/schedules/now?zone="+z+"&at=yesterday", nil, 422, "invalid")
	e.expect(c, "GET", "/api/v1/schedules/now?zone="+ids.New(), nil, 404, "not_found")
}

func TestProductWithTrashedImageStaysEditable(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	cat := e.category("Drinks", "")
	img := e.seedAsset("image")
	p := e.expect(c, "POST", "/api/v1/products", map[string]any{"name": "Cola", "categoryId": cat, "type": "drink",
		"price": map[string]any{"amount": 300, "currency": "EUR"}, "imageAssetId": img, "showImageInsteadOfName": true}, 200, "")
	if _, err := e.srv.App.DB.W.Exec(`UPDATE media_assets SET trashed_at = 'now' WHERE id = ?`, img); err != nil {
		t.Fatal(err)
	}
	// The unchanged (now trashed) image is not re-validated; setting it anew would be.
	e.expect(c, "PATCH", "/api/v1/products/"+asStr(p["id"]), map[string]any{"revision": 1, "price": map[string]any{"amount": 350}}, 200, "")
	other := e.seedAsset("image")
	if _, err := e.srv.App.DB.W.Exec(`UPDATE media_assets SET trashed_at = 'now' WHERE id = ?`, other); err != nil {
		t.Fatal(err)
	}
	e.expect(c, "PATCH", "/api/v1/products/"+asStr(p["id"]), map[string]any{"revision": 2, "imageAssetId": other}, 422, "unknown_asset")
	// Clearing the image resets "image instead of name".
	cleared := e.expect(c, "PATCH", "/api/v1/products/"+asStr(p["id"]), map[string]any{"revision": 2, "imageAssetId": nil}, 200, "")
	if cleared["showImageInsteadOfName"] != false || cleared["imageAssetId"] != nil {
		t.Fatalf("cleared %v", cleared)
	}
}

// "Duplicate" in the product list: the copy is created with afterId and lands right after its
// original; in another category (changed in the dialog) it goes to the end; afterId is only
// for creating.
func TestProductDuplicateLandsAfterItsOriginal(t *testing.T) {
	e := catSetup(t)
	beer := e.category("Beer", "")
	wine := e.category("Wine", "")
	a := e.product("Pils", beer, 450)
	b := e.product("Helles", beer, 480)
	e.product("Weizen", beer, 520)
	order := func(cat string) []string {
		out := e.expect(e.admin, "GET", "/api/v1/products?category="+cat, nil, 200, "")
		var names []string
		for _, x := range asList(out["products"]) {
			names = append(names, asStr(asMap(x)["name"]))
		}
		return names
	}
	copyOf := func(cat, after string) string {
		return asStr(e.expect(e.admin, "POST", "/api/v1/products", map[string]any{"name": "Pils 0,5", "categoryId": cat, "type": "drink",
			"price": map[string]any{"amount": 590, "currency": "EUR"}, "afterId": after}, 200, "")["id"])
	}
	copyOf(beer, a)
	if got := catJSON(order(beer)); got != `["Pils","Pils 0,5","Helles","Weizen"]` {
		t.Fatalf("copy must follow its original: %s", got)
	}
	copyOf(wine, a) // category changed in the dialog
	if got := catJSON(order(wine)); got != `["Pils 0,5"]` {
		t.Fatalf("copy in another category: %s", got)
	}
	e.expect(e.admin, "POST", "/api/v1/products", map[string]any{"name": "X", "categoryId": beer, "type": "drink",
		"price": map[string]any{"amount": 1, "currency": "EUR"}, "afterId": "no-such-product"}, 200, "")
	if got := order(beer); got[len(got)-1] != "X" {
		t.Fatalf("unknown afterId: appended, got %v", got)
	}
	e.expect(e.admin, "PATCH", "/api/v1/products/"+a, map[string]any{"revision": 1, "afterId": b}, 422, "invalid")
}

// Logo size: percent of the automatic size, 100 by default, 25–400, kept on the product
// and sent to the screens in the live state.
func TestProductImageScale(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	cat := e.category("Drinks", "")
	z := e.zone("Bar")
	img := e.seedAsset("image")
	body := map[string]any{"name": "Brand", "categoryId": cat, "type": "drink", "price": map[string]any{"amount": 450, "currency": "EUR"},
		"imageAssetId": img, "showImageInsteadOfName": true}
	p := e.expect(c, "POST", "/api/v1/products", body, 200, "")
	if p["imageScale"] != float64(100) {
		t.Fatalf("default scale %v", p["imageScale"])
	}
	id := asStr(p["id"])
	e.expect(c, "PATCH", "/api/v1/products/"+id, map[string]any{"revision": 1, "imageScale": 401}, 422, "out_of_range")
	e.expect(c, "PATCH", "/api/v1/products/"+id, map[string]any{"revision": 1, "imageScale": 24}, 422, "out_of_range")
	if got := e.expect(c, "PATCH", "/api/v1/products/"+id, map[string]any{"revision": 1, "imageScale": 115}, 200, ""); got["imageScale"] != float64(115) {
		t.Fatalf("patched scale %v", got["imageScale"])
	}
	states, err := live.Compute(context.Background(), e.srv.App.DB.R, []string{z}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, lp := range states[z].Products {
		if lp.ID == id {
			found = lp.ImageScale == 115
		}
	}
	if !found {
		t.Fatalf("live state must carry the scale: %+v", states[z].Products)
	}
}
