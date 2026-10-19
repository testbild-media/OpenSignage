package app

import (
	"fmt"
	"testing"
)

// The zone preview evaluates every screen from the deployment as the players do: at a chosen
// moment, with the real or a chosen zone state, with the prices, ticker and live state of
// the zone – without any device online.
func TestZonePreview(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	z := e.zone("Bar")
	cat := e.category("Drinks", "")
	cola := e.product("Cola", cat, 300)
	mk := func(name string, screens int) string {
		id := asStr(e.expect(c, "POST", "/api/v1/playlists", map[string]any{"name": name}, 200, "")["id"])
		if screens > 1 {
			e.expect(c, "PUT", "/api/v1/playlists/"+id+"/draft", map[string]any{"revision": 1,
				"doc": map[string]any{"id": id, "name": name, "items": []any{}, "screens": screens}}, 200, "")
		}
		e.expect(c, "POST", "/api/v1/playlists/"+id+"/publish", nil, 200, "")
		return id
	}
	pDefault, pEvening, pWall, pClosed := mk("Default", 1), mk("Evening", 1), mk("Wall", 3), mk("Closed", 1)
	wall := asStr(e.expect(c, "POST", "/api/v1/zones/"+z+"/groups", map[string]any{"name": "Menu wall"}, 200, "")["id"])
	e.patchCur(c, "/api/v1/zones/"+z, map[string]any{"closedPlaylistId": pClosed}, 200, "")
	e.expect(c, "POST", "/api/v1/schedules", map[string]any{"name": "Default", "scope": "default", "zoneId": z, "playlistId": pDefault, "prices": []any{}}, 200, "")
	evening := asStr(e.expect(c, "POST", "/api/v1/schedules", map[string]any{"name": "Happy hour", "scope": "zone", "zoneId": z, "playlistId": pEvening,
		"window":         map[string]any{"days": []int{1, 2, 3, 4, 5}, "start": "17:00", "end": "19:00"},
		"groupPlaylists": []map[string]any{{"groupId": wall, "playlistId": pWall}},
		"prices":         []map[string]any{{"productId": cola, "price": map[string]any{"amount": 200, "currency": "EUR"}}},
		"ticker":         map[string]any{"text": "Happy hour!"}}, 200, "")["id"])
	e.expect(c, "POST", "/api/v1/live/products/"+cola+"/status", map[string]any{"status": "SOLD_OUT", "zoneId": z}, 200, "")

	preview := func(query string) map[string]any {
		return e.expect(c, "GET", "/api/v1/zones/"+z+"/preview"+query, nil, 200, "")
	}
	screens := func(r map[string]any) []string {
		var out []string
		for _, s := range asList(r["screens"]) {
			m := asMap(s)
			out = append(out, asStr(m["groupName"])+"#"+fmt.Sprint(asNum(m["position"]))+"="+asStr(m["playlistId"]))
		}
		return out
	}

	// Monday 18:00 in the server's time zone (local date and time are accepted).
	r := preview("?at=2026-10-12T18:00")
	if r["at"] != "2026-10-12T18:00:00+02:00" || r["zoneState"] != "OPEN" || r["zoneName"] != "Bar" {
		t.Fatalf("header: %v %v %v", r["at"], r["zoneState"], r["zoneName"])
	}
	// No devices: the wall with one screen per track of its playlist, then the screens without group.
	want := []string{"Menu wall#1=" + pWall, "Menu wall#2=" + pWall, "Menu wall#3=" + pWall, "#1=" + pEvening}
	if got := screens(r); len(got) != 4 || got[0] != want[0] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("screens: %v", got)
	}
	first := asMap(asList(r["screens"])[3])
	if first["kind"] != "rule" || first["ruleId"] != evening || first["since"] != "2026-10-12T17:00:00+02:00" || first["deviceId"] != nil {
		t.Fatalf("screen: %v", first)
	}
	if asNum(asMap(asMap(r["prices"])[cola])["amount"]) != 200 || asMap(r["ticker"])["text"] != "Happy hour!" {
		t.Fatalf("prices/ticker: %v %v", r["prices"], r["ticker"])
	}
	if rules := asList(r["activeRules"]); len(rules) != 2 || asMap(rules[0])["name"] != "Happy hour" {
		t.Fatalf("active rules: %v", rules)
	}
	// The live state of the zone (zone overrides) and the deployment the screens get.
	var colaStatus string
	for _, p := range asList(asMap(r["live"])["products"]) {
		if asMap(p)["id"] == cola {
			colaStatus = asStr(asMap(p)["status"])
		}
	}
	if colaStatus != "SOLD_OUT" {
		t.Fatalf("live status of the zone: %q", colaStatus)
	}
	if man := asMap(r["manifest"]); man["zoneId"] != z || len(asList(man["playlists"])) != 4 || len(asList(man["rules"])) != 2 {
		t.Fatalf("manifest: %v", man)
	}

	// Outside the window the default rule plays; a chosen CLOSED state shows the closed playlist.
	if got := screens(preview("?at=2026-10-12T12:00:00%2B02:00")); got[len(got)-1] != "#1="+pDefault {
		t.Fatalf("noon: %v", got)
	}
	closed := preview("?at=2026-10-12T18:00&state=CLOSED")
	if got := screens(closed); got[len(got)-1] != "#1="+pClosed || closed["zoneState"] != "CLOSED" || asMap(closed["live"])["zone"].(map[string]any)["state"] != "CLOSED" {
		t.Fatalf("closed: %v %v", got, closed["zoneState"])
	}
	if closed["ticker"] != nil {
		t.Fatalf("no ticker while closed: %v", closed["ticker"])
	}

	e.expect(c, "GET", "/api/v1/zones/"+z+"/preview?at=tomorrow", nil, 422, "invalid")
	e.expect(c, "GET", "/api/v1/zones/"+z+"/preview?state=HALF", nil, 422, "invalid_value")
	e.expect(c, "GET", "/api/v1/zones/nope/preview", nil, 404, "")
	// Operators limited to other zones do not see it.
	other := e.zone("Terrace")
	op := e.rightsUser("op", map[string]any{"scope": "zones", "media": "use", "products": "view",
		"zones": []any{map[string]any{"zoneId": other, "access": "view"}}})
	e.expect(op, "GET", "/api/v1/zones/"+z+"/preview", nil, 404, "")
	e.expect(op, "GET", "/api/v1/zones/"+other+"/preview", nil, 200, "")
}
