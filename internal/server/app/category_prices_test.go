package app

import "testing"

// Schedule prices for whole categories: validation, evaluation per product (own price before
// subcategory before category), overlaps per product and the delete protection.
func TestScheduleCategoryPrices(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	z := e.zone("Bar")
	drinks := e.category("Long drinks", "")
	vodka := e.category("Vodka", drinks)
	food := e.category("Food", "")
	empty := e.category("Empty", "")
	gin := e.product("Gin tonic", drinks, 800)
	cuba := e.product("Cuba libre", drinks, 850)
	vo := e.product("Vodka orange", vodka, 750)
	fries := e.product("Fries", food, 400)
	money := func(a int, cur string) map[string]any { return map[string]any{"amount": a, "currency": cur} }
	win := map[string]any{"days": []int{1}, "start": "17:00", "end": "19:00"}
	rule := func(name string, w map[string]any, prices ...map[string]any) map[string]any {
		return map[string]any{"name": name, "scope": "zone", "zoneId": z, "window": w, "prices": prices}
	}

	// Exactly one of product and category, each at most once, known category, server currency.
	e.expect(c, "POST", "/api/v1/schedules", rule("X", win, map[string]any{"productId": gin, "categoryId": drinks, "price": money(500, "EUR")}), 422, "invalid")
	e.expect(c, "POST", "/api/v1/schedules", rule("X", win, map[string]any{"price": money(500, "EUR")}), 422, "invalid")
	e.expect(c, "POST", "/api/v1/schedules", rule("X", win, map[string]any{"categoryId": drinks, "price": money(500, "EUR")},
		map[string]any{"categoryId": drinks, "price": money(400, "EUR")}), 422, "invalid")
	e.expect(c, "POST", "/api/v1/schedules", rule("X", win, map[string]any{"categoryId": "nope", "price": money(500, "EUR")}), 422, "unknown_category")
	out := e.expect(c, "POST", "/api/v1/schedules", rule("X", win, map[string]any{"categoryId": drinks, "price": money(500, "USD")}), 422, "currency_mismatch")
	if p := rvErrParams(out); p["field"] != "prices[0].price.currency" || p["currency"] != "EUR" {
		t.Errorf("currency_mismatch params %v", p)
	}

	happy := e.expect(c, "POST", "/api/v1/schedules", rule("Happy hour", win,
		map[string]any{"categoryId": drinks, "price": money(500, "EUR")},
		map[string]any{"categoryId": vodka, "price": money(450, "EUR")},
		map[string]any{"productId": gin, "price": money(550, "EUR")}), 200, "")
	// The rule keeps what was set: one product price and two category prices.
	prices := asList(happy["prices"])
	if len(prices) != 3 || asMap(prices[0])["productId"] != gin || asMap(prices[1])["categoryId"] == nil || asMap(prices[1])["productId"] != nil {
		t.Fatalf("stored prices %v", prices)
	}

	now := e.expect(c, "GET", "/api/v1/schedules/now?zone="+z+"&at=2026-10-12T18:00:00%2B02:00", nil, 200, "")
	got := asMap(now["prices"])
	want := map[string]float64{gin: 550, cuba: 500, vo: 450}
	for id, a := range want {
		if asNum(asMap(got[id])["amount"]) != a {
			t.Errorf("price of %s: %v (want %v)", id, got[id], a)
		}
	}
	if got[fries] != nil || len(got) != 3 {
		t.Errorf("only the long drinks get a price: %v", got)
	}
	// A product created later in the category gets the price as well.
	mule := e.product("Moscow mule", vodka, 900)
	now = e.expect(c, "GET", "/api/v1/schedules/now?zone="+z+"&at=2026-10-12T18:00:00%2B02:00", nil, 200, "")
	if asNum(asMap(asMap(now["prices"])[mule])["amount"]) != 450 {
		t.Errorf("new product: %v", now["prices"])
	}

	// Overlapping rules may not change the price of the same product, also through a category.
	overlap := map[string]any{"days": []int{1}, "start": "18:00", "end": "20:00"}
	out = e.expect(c, "POST", "/api/v1/schedules", rule("Cuba", overlap, map[string]any{"productId": cuba, "price": money(600, "EUR")}), 409, "window_overlap")
	if p := rvErrParams(out); p["change"] != "price:"+cuba {
		t.Errorf("overlap params %v", p)
	}
	e.expect(c, "POST", "/api/v1/schedules", rule("Vodka", overlap, map[string]any{"categoryId": vodka, "price": money(400, "EUR")}), 409, "window_overlap")
	e.expect(c, "POST", "/api/v1/schedules", rule("Food", overlap, map[string]any{"categoryId": food, "price": money(300, "EUR")}), 200, "")

	// A category with schedule prices cannot be deleted; without them it can.
	e.expect(c, "PATCH", "/api/v1/schedules/"+asStr(happy["id"]), map[string]any{"revision": 1, "prices": []map[string]any{
		{"categoryId": empty, "price": money(100, "EUR")}}}, 200, "")
	out = e.expect(c, "DELETE", "/api/v1/categories/"+empty, nil, 409, "category_in_rules")
	if p := asList(rvErrParams(out)["rules"]); len(p) != 1 || p[0] != "Happy hour" {
		t.Errorf("category_in_rules params %v", p)
	}
	e.expect(c, "PATCH", "/api/v1/schedules/"+asStr(happy["id"]), map[string]any{"revision": 2, "prices": []map[string]any{
		{"productId": gin, "price": money(500, "EUR")}}}, 200, "")
	e.expect(c, "DELETE", "/api/v1/categories/"+empty, nil, 204, "")
}
