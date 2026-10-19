package live

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"opensignage/internal/contracts"
	"opensignage/internal/server/db"
)

func openDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func exec(t *testing.T, d *db.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.W.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// TestStatusesStayDistinct is a regression test: SOLD_OUT and UNAVAILABLE must never
// collapse, and the zone override beats the global status.
func TestStatusesStayDistinct(t *testing.T) {
	d := openDB(t)
	now := "2026-10-11T10:00:00Z"
	style, _ := json.Marshal(contracts.TickerTemplate{Font: "Inter", Size: 30, Color: "#000000", Background: "#ffffff",
		Speed: 80, Direction: "right", Height: 50, Position: "top"})
	exec(t, d, `INSERT INTO ticker_templates(id, name, style, created_at, updated_at) VALUES ('tt', 'T', ?, ?, ?)`, string(style), now, now)
	exec(t, d, `INSERT INTO zones(id, name, state, ticker_text, ticker_visible, created_at, updated_at) VALUES ('z1', 'Bar', 'OPEN', 'Happy hour', 1, ?, ?)`, now, now)
	exec(t, d, `INSERT INTO zones(id, name, state, ticker_template_id, created_at, updated_at) VALUES ('z2', 'VIP', 'CLOSED', 'tt', ?, ?)`, now, now)
	exec(t, d, `INSERT INTO categories(id, name, sort, created_at, updated_at) VALUES ('c1', 'Drinks', 1, ?, ?)`, now, now)
	exec(t, d, `INSERT INTO categories(id, parent_id, name, sort, created_at, updated_at) VALUES ('c2', 'c1', 'Beer', 1, ?, ?)`, now, now)
	exec(t, d, `INSERT INTO media_assets(id, name, kind, status, created_at, updated_at) VALUES ('img', 'i', 'image', 'ready', ?, ?)`, now, now)
	exec(t, d, `INSERT INTO media_assets(id, name, kind, status, created_at, updated_at, trashed_at) VALUES ('gone', 'g', 'image', 'ready', ?, ?, ?)`, now, now, now)
	prod := func(id, status string, sort int, img any, archived any) {
		exec(t, d, `INSERT INTO products(id, category_id, name, type, quantity_value, quantity_unit, price_amount, currency, status,
			image_asset_id, sort, archived_at, created_at, updated_at) VALUES (?, 'c2', ?, 'drink', '0.33', 'l', 450, 'EUR', ?, ?, ?, ?, ?, ?)`,
			id, "Product "+id, status, img, sort, archived, now, now)
	}
	prod("pa", "SOLD_OUT", 1, "img", nil)
	prod("pb", "UNAVAILABLE", 2, "gone", nil)
	prod("pc", "AVAILABLE", 3, nil, nil)
	prod("pd", "AVAILABLE", 4, nil, now) // archived
	exec(t, d, `INSERT INTO zone_product_status(zone_id, product_id, status) VALUES ('z1', 'pc', 'SOLD_OUT')`)
	exec(t, d, `INSERT INTO zone_product_status(zone_id, product_id, status) VALUES ('z2', 'pc', 'UNAVAILABLE')`)
	exec(t, d, `INSERT INTO zone_product_status(zone_id, product_id, status) VALUES ('z2', 'pa', 'AVAILABLE')`)

	states, err := Compute(context.Background(), d.R, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("zones: %d", len(states))
	}
	status := func(zone string) map[string]string {
		out := map[string]string{}
		for _, p := range states[zone].Products {
			out[p.ID] = p.Status
		}
		return out
	}
	z1, z2 := status("z1"), status("z2")
	if len(z1) != 3 || z1["pd"] != "" {
		t.Fatalf("archived product must be absent: %v", z1)
	}
	if z1["pa"] != "SOLD_OUT" || z1["pb"] != "UNAVAILABLE" || z1["pc"] != "SOLD_OUT" {
		t.Fatalf("z1 statuses: %v", z1)
	}
	if z2["pa"] != "AVAILABLE" || z2["pb"] != "UNAVAILABLE" || z2["pc"] != "UNAVAILABLE" {
		t.Fatalf("z2 statuses: %v", z2)
	}
	s1 := states["z1"]
	// Tickers come from time schedule rules: the zone ticker of the live state stays empty
	// even when old data has a text.
	if s1.Zone.Name != "Bar" || s1.Zone.State != "OPEN" || s1.Ticker.Visible || s1.Ticker.Text != "" {
		t.Fatalf("zone/ticker: %+v %+v", s1.Zone, s1.Ticker)
	}
	if s1.Ticker.Template != contracts.DefaultTickerTemplate() {
		t.Fatalf("default ticker template expected: %+v", s1.Ticker.Template)
	}
	if states["z2"].Ticker.Template.Font != "Inter" || states["z2"].Zone.State != "CLOSED" {
		t.Fatalf("custom template: %+v", states["z2"].Ticker.Template)
	}
	p := s1.Products[0]
	if p.ID != "pa" || p.Quantity == nil || p.Quantity.Value != "0.33" || p.Price.Amount != 450 || p.ImageAssetID != "img" {
		t.Fatalf("product fields: %+v", p)
	}
	if s1.Products[1].ImageAssetID != "" {
		t.Fatal("trashed image must not be referenced")
	}
	if len(s1.Categories) != 2 || s1.Categories[1].ParentID != "c1" {
		t.Fatalf("categories: %+v", s1.Categories)
	}
	// JSON keeps the three values verbatim.
	b, _ := json.Marshal(s1)
	var back contracts.LiveState
	if err := json.Unmarshal(b, &back); err != nil || back.Products[1].Status != "UNAVAILABLE" {
		t.Fatalf("round trip: %v %s", err, b)
	}
	only, err := Compute(context.Background(), d.R, []string{"z2", "nope"}, time.Now())
	if err != nil || len(only) != 1 || only["z2"] == nil {
		t.Fatalf("subset: %v %v", only, err)
	}
	if e := Empty(time.Now()); e.Zone.ID != "" || e.Products == nil || e.Ticker.Visible {
		t.Fatalf("empty: %+v", e)
	}
}

// Variants carry their own effective status (zone override, else global), keep their order,
// and a product whose offered variants are all sold out is sold out in that zone.
func TestVariants(t *testing.T) {
	d := openDB(t)
	now := "2026-10-19T10:00:00Z"
	exec(t, d, `INSERT INTO zones(id, name, state, created_at, updated_at) VALUES ('z1', 'Bar', 'OPEN', ?, ?)`, now, now)
	exec(t, d, `INSERT INTO zones(id, name, state, created_at, updated_at) VALUES ('z2', 'Terrace', 'OPEN', ?, ?)`, now, now)
	exec(t, d, `INSERT INTO categories(id, name, sort, created_at, updated_at) VALUES ('c1', 'Juices', 1, ?, ?)`, now, now)
	exec(t, d, `INSERT INTO products(id, category_id, name, type, price_amount, currency, status, sort, created_at, updated_at)
		VALUES ('juice', 'c1', 'Juices', 'drink', 400, 'EUR', 'AVAILABLE', 1, ?, ?)`, now, now)
	exec(t, d, `INSERT INTO products(id, category_id, name, type, price_amount, currency, status, sort, created_at, updated_at)
		VALUES ('cola', 'c1', 'Cola', 'drink', 300, 'EUR', 'AVAILABLE', 2, ?, ?)`, now, now)
	variant := func(id, name string, sort int, status string) {
		exec(t, d, `INSERT INTO product_variants(id, product_id, name, sort, status) VALUES (?, 'juice', ?, ?, ?)`, id, name, sort, status)
	}
	variant("v-apple", "Apple", 2, "AVAILABLE")
	variant("v-orange", "Orange", 1, "SOLD_OUT")
	variant("v-guava", "Guava", 3, "UNAVAILABLE")
	exec(t, d, `INSERT INTO zone_variant_status(zone_id, variant_id, status) VALUES ('z2', 'v-apple', 'SOLD_OUT')`)
	exec(t, d, `INSERT INTO zone_variant_status(zone_id, variant_id, status) VALUES ('z2', 'v-guava', 'AVAILABLE')`)

	states, err := Compute(context.Background(), d.R, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	juice := func(zone string) contracts.LiveProduct {
		for _, p := range states[zone].Products {
			if p.ID == "juice" {
				return p
			}
		}
		t.Fatalf("no juice in %s", zone)
		return contracts.LiveProduct{}
	}
	got := func(p contracts.LiveProduct) string {
		s := p.Status
		for _, v := range p.Variants {
			s += " " + v.Name + "=" + v.Status
		}
		return s
	}
	if g := got(juice("z1")); g != "AVAILABLE Orange=SOLD_OUT Apple=AVAILABLE Guava=UNAVAILABLE" {
		t.Fatalf("z1: %s", g)
	}
	if g := got(juice("z2")); g != "AVAILABLE Orange=SOLD_OUT Apple=SOLD_OUT Guava=AVAILABLE" {
		t.Fatalf("z2: %s", g)
	}
	for _, p := range states["z1"].Products {
		if p.ID == "cola" && p.Variants != nil {
			t.Fatalf("a product without variants has none: %+v", p)
		}
	}

	// Apple sold out in the bar as well: every variant offered there is sold out (Guava is not
	// carried), so the whole product is; on the terrace Guava is still there.
	exec(t, d, `INSERT INTO zone_variant_status(zone_id, variant_id, status) VALUES ('z1', 'v-apple', 'SOLD_OUT')`)
	states, err = Compute(context.Background(), d.R, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if juice("z1").Status != "SOLD_OUT" || juice("z2").Status != "AVAILABLE" {
		t.Fatalf("derived status: bar %s, terrace %s", juice("z1").Status, juice("z2").Status)
	}
}
