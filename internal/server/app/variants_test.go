package app

import (
	"strings"
	"testing"
)

// Product variants: stored in order, kept with their status when the product is edited,
// validated, and switched one by one (globally and per zone) with the rights of products.
func TestProductVariants(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	bar := e.zone("Bar")
	e.zone("Terrace") // without overrides
	cat := e.category("Juices", "")
	names := func(p map[string]any) string {
		var out []string
		for _, v := range asList(p["variants"]) {
			out = append(out, asStr(asMap(v)["name"])+"="+asStr(asMap(v)["status"]))
		}
		return strings.Join(out, ",")
	}
	variants := func(list ...map[string]any) []map[string]any { return list }

	prod := e.expect(c, "POST", "/api/v1/products", map[string]any{"name": "Granini", "categoryId": cat, "type": "drink",
		"price":    map[string]any{"amount": 400, "currency": "EUR"},
		"variants": variants(map[string]any{"name": " Orange "}, map[string]any{"name": "Apple"}, map[string]any{"name": "Passion fruit"})}, 200, "")
	id := asStr(prod["id"])
	if names(prod) != "Orange=AVAILABLE,Apple=AVAILABLE,Passion fruit=AVAILABLE" {
		t.Fatalf("created: %s", names(prod))
	}
	vid := func(p map[string]any, name string) string {
		for _, v := range asList(p["variants"]) {
			if asMap(v)["name"] == name {
				return asStr(asMap(v)["id"])
			}
		}
		t.Fatalf("no variant %s in %v", name, p["variants"])
		return ""
	}
	orange, apple := vid(prod, "Orange"), vid(prod, "Apple")

	// One variant sold out everywhere, another one in the bar only.
	out := e.expect(c, "POST", "/api/v1/live/variants/"+orange+"/status", map[string]any{"status": "SOLD_OUT"}, 200, "")
	if out["productId"] != id || out["variantId"] != orange || out["effective"] != "SOLD_OUT" || out["changed"] != true {
		t.Fatalf("global variant status: %v", out)
	}
	op := e.login("op", "operator")
	e.expect(op, "POST", "/api/v1/live/variants/"+apple+"/status", map[string]any{"status": "SOLD_OUT", "zoneId": bar}, 200, "")
	e.expect(op, "POST", "/api/v1/live/variants/"+apple+"/status", map[string]any{"status": "UNAVAILABLE", "zoneId": bar}, 403, "status_admin_only")
	e.expect(c, "POST", "/api/v1/live/variants/nope/status", map[string]any{"status": "SOLD_OUT"}, 404, "")

	// Editing keeps known variants with their status and overrides: rename, reorder, drop
	// one, add one.
	cur := e.expect(c, "GET", "/api/v1/products", nil, 200, "")
	var stored map[string]any
	for _, p := range asList(cur["products"]) {
		if asMap(p)["id"] == id {
			stored = asMap(p)
		}
	}
	if names(stored) != "Orange=SOLD_OUT,Apple=AVAILABLE,Passion fruit=AVAILABLE" {
		t.Fatalf("stored: %s", names(stored))
	}
	for _, v := range asList(stored["variants"]) {
		if asMap(v)["id"] == apple && asMap(asMap(v)["zoneStatus"])[bar] != "SOLD_OUT" {
			t.Fatalf("zone override: %v", v)
		}
	}
	upd := e.expect(c, "PATCH", "/api/v1/products/"+id, map[string]any{"revision": stored["revision"],
		"variants": variants(map[string]any{"id": apple, "name": "Apple juice"}, map[string]any{"id": orange, "name": "Orange"}, map[string]any{"name": "Guava"})}, 200, "")
	if names(upd) != "Apple juice=AVAILABLE,Orange=SOLD_OUT,Guava=AVAILABLE" || vid(upd, "Apple juice") != apple {
		t.Fatalf("updated: %s", names(upd))
	}

	// The operator view lists the variants and their status per zone.
	live := e.expect(op, "GET", "/api/v1/live/zones", nil, 200, "")
	for _, z := range asList(live["zones"]) {
		zm := asMap(z)
		want := map[string]string{orange: "SOLD_OUT", apple: "AVAILABLE"}
		if zm["id"] == bar {
			want[apple] = "SOLD_OUT"
		}
		for _, v := range asList(zm["variants"]) {
			vm := asMap(v)
			if w, ok := want[asStr(vm["id"])]; ok && vm["status"] != w {
				t.Errorf("zone %v variant %v: %v (want %s)", zm["name"], vm["id"], vm["status"], w)
			}
		}
	}

	// Validation: names, duplicates (ignoring case), unknown ids, the number of variants.
	rev := upd["revision"]
	e.expect(c, "PATCH", "/api/v1/products/"+id, map[string]any{"revision": rev, "variants": variants(map[string]any{"name": "  "})}, 422, "invalid")
	e.expect(c, "PATCH", "/api/v1/products/"+id, map[string]any{"revision": rev, "variants": variants(map[string]any{"name": "Kiwi"}, map[string]any{"name": "kiwi"})}, 422, "duplicate")
	e.expect(c, "PATCH", "/api/v1/products/"+id, map[string]any{"revision": rev, "variants": variants(map[string]any{"id": "foreign", "name": "Kiwi"})}, 422, "invalid")
	many := []map[string]any{}
	for i := 0; i < 51; i++ {
		many = append(many, map[string]any{"name": "V" + strings.Repeat("x", i)})
	}
	e.expect(c, "PATCH", "/api/v1/products/"+id, map[string]any{"revision": rev, "variants": many}, 422, "too_many")

	// Without variants the product is as before; an archived product's variants cannot be
	// switched; deleting the product removes its variants.
	plain := e.expect(c, "PATCH", "/api/v1/products/"+id, map[string]any{"revision": rev, "variants": []any{}}, 200, "")
	if len(asList(plain["variants"])) != 0 {
		t.Fatalf("variants removed: %v", plain["variants"])
	}
	again := e.expect(c, "PATCH", "/api/v1/products/"+id, map[string]any{"revision": plain["revision"], "variants": variants(map[string]any{"name": "Cherry"})}, 200, "")
	cherry := vid(again, "Cherry")
	e.expect(c, "POST", "/api/v1/products/"+id+"/archive", nil, 200, "")
	e.expect(c, "POST", "/api/v1/live/variants/"+cherry+"/status", map[string]any{"status": "SOLD_OUT"}, 409, "")
	e.expect(c, "DELETE", "/api/v1/products/"+id, nil, 204, "")
	var n int
	if err := e.srv.App.DB.R.QueryRow(`SELECT COUNT(*) FROM product_variants`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("variants left after delete: %d %v", n, err)
	}
}
