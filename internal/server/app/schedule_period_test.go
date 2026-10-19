package app

import "testing"

// Time schedule rules with a period of dates: a happy hour on one Saturday only, created
// weeks ahead; rules on different Saturdays do not overlap.
func TestSchedulePeriod(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	z := e.zone("Bar")
	cat := e.category("Drinks", "")
	cola := e.product("Cola", cat, 300)
	money := func(a int) map[string]any { return map[string]any{"amount": a, "currency": "EUR"} }
	hh := func(name, from, until string, price int) map[string]any {
		w := map[string]any{"days": []int{6}, "start": "17:00", "end": "19:00"}
		if from != "" {
			w["from"] = from
		}
		if until != "" {
			w["until"] = until
		}
		return map[string]any{"name": name, "scope": "zone", "zoneId": z, "window": w,
			"prices": []map[string]any{{"productId": cola, "price": money(price)}}}
	}

	// 2026-11-07 and 2026-11-14 are Saturdays.
	first := e.expect(c, "POST", "/api/v1/schedules", hh("HH 7th", "2026-11-07", "2026-11-07", 200), 200, "")
	w := asMap(first["window"])
	if w["from"] != "2026-11-07" || w["until"] != "2026-11-07" {
		t.Fatalf("period not stored: %v", w)
	}
	// Another Saturday changes the same price: no overlap.
	second := e.expect(c, "POST", "/api/v1/schedules", hh("HH 14th", "2026-11-14", "2026-11-14", 150), 200, "")
	// The same Saturday does overlap; so does a rule for every week.
	e.expect(c, "POST", "/api/v1/schedules", hh("HH again", "2026-11-07", "2026-11-07", 100), 409, "window_overlap")
	e.expect(c, "POST", "/api/v1/schedules", hh("HH weekly", "", "", 100), 409, "window_overlap")

	// Invalid periods.
	e.expect(c, "POST", "/api/v1/schedules", hh("Bad", "2026-11-08", "2026-11-07", 100), 422, "before_from")
	e.expect(c, "POST", "/api/v1/schedules", hh("Bad", "2026-11-09", "2026-11-10", 100), 422, "no_day_in_range") // Mon–Tue, no Saturday
	e.expect(c, "POST", "/api/v1/schedules", hh("Bad", "7.11.2026", "", 100), 422, "invalid")

	// Only on its day: the cola costs 2.00 on the 7th, 1.50 on the 14th, 3.00 on the 21st.
	now := func(at string) map[string]any {
		return e.expect(c, "GET", "/api/v1/schedules/now?zone="+z+"&at="+at, nil, 200, "")
	}
	price := func(r map[string]any) any {
		if p := asMap(asMap(r["prices"])[cola]); p != nil {
			return p["amount"]
		}
		return nil
	}
	if r := now("2026-11-07T18:00:00%2B01:00"); price(r) != float64(200) {
		t.Fatalf("the 7th: %v", r["prices"])
	}
	if r := now("2026-11-14T18:00:00%2B01:00"); price(r) != float64(150) {
		t.Fatalf("the 14th: %v", r["prices"])
	}
	if r := now("2026-11-21T18:00:00%2B01:00"); price(r) != nil {
		t.Fatalf("the 21st: %v", r["prices"])
	}

	// Removing the period makes the rule weekly – then it overlaps the other one.
	e.expect(c, "PATCH", "/api/v1/schedules/"+asStr(second["id"]), map[string]any{"revision": 1,
		"window": map[string]any{"days": []int{6}, "start": "17:00", "end": "19:00"}}, 409, "window_overlap")
	moved := e.expect(c, "PATCH", "/api/v1/schedules/"+asStr(second["id"]), map[string]any{"revision": 1,
		"window": map[string]any{"days": []int{6}, "start": "17:00", "end": "19:00", "from": "2026-11-21", "until": "2026-11-21"}}, 200, "")
	if asMap(moved["window"])["from"] != "2026-11-21" {
		t.Fatalf("moved: %v", moved["window"])
	}
}
