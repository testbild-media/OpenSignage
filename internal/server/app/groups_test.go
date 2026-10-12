package app

import (
	"testing"
	"time"

	"opensignage/internal/common/ids"
)

// Display groups in a zone, places of the screens, rules that change single things
// (playlist of all screens / of a group / of the screens without group, ticker) and what
// each group plays now.
func TestDisplayGroupsPlacesAndRules(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	z, other := e.zone("Bar"), e.zone("Terrace")
	mk := func(name string) string {
		id := asStr(e.expect(c, "POST", "/api/v1/playlists", map[string]any{"name": name}, 200, "")["id"])
		e.expect(c, "POST", "/api/v1/playlists/"+id+"/publish", nil, 200, "")
		return id
	}
	pAll, pMenu, pLunch, pClosed := mk("Werbung"), mk("Speisekarte"), mk("Mittag"), mk("Zu")

	// Groups: unique names per zone, known closed playlist.
	g := e.expect(c, "POST", "/api/v1/zones/"+z+"/groups", map[string]any{"name": "Menüwand"}, 200, "")
	gid := asStr(g["id"])
	e.expect(c, "POST", "/api/v1/zones/"+z+"/groups", map[string]any{"name": "menüwand"}, 409, "group_name_taken")
	e.expect(c, "POST", "/api/v1/zones/"+z+"/groups", map[string]any{"name": "X", "closedPlaylistId": ids.New()}, 422, "unknown_playlist")
	e.expect(c, "POST", "/api/v1/zones/"+other+"/groups", map[string]any{"name": "Menüwand"}, 200, "") // other zone: fine
	otherGroup := asStr(asList(e.expect(c, "GET", "/api/v1/zones", nil, 200, "")["zones"])[1].(map[string]any)["groups"].([]any)[0].(map[string]any)["id"])
	user := e.login("barkeeper", "user", z)
	e.expect(user, "POST", "/api/v1/zones/"+z+"/groups", map[string]any{"name": "Y"}, 403, "forbidden")

	// Places: the next free place is proposed and then kept.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var dev []string
	for _, name := range []string{"Links", "Mitte", "Rechts", "Tresen"} {
		id := ids.New()
		dev = append(dev, id)
		if _, err := e.srv.App.DB.W.Exec(`INSERT INTO devices(id, hardware_id, name, zone_id, config, created_at, updated_at) VALUES (?, ?, ?, ?, '{}', ?, ?)`,
			id, "hw-"+id, name, z, now, now); err != nil {
			t.Fatal(err)
		}
	}
	place := func(i int, body map[string]any, wantGroup any, wantPos float64) {
		t.Helper()
		out := e.expect(c, "PATCH", "/api/v1/devices/"+dev[i], body, 200, "")
		if out["groupId"] != wantGroup || asNum(out["position"]) != wantPos {
			t.Fatalf("device %d after %v: group %v place %v (want %v/%v)", i, body, out["groupId"], out["position"], wantGroup, wantPos)
		}
	}
	place(0, map[string]any{"groupId": gid}, gid, 1)
	place(1, map[string]any{"groupId": gid}, gid, 2)
	place(2, map[string]any{"groupId": gid, "position": 2}, gid, 2) // the same place twice is allowed (same track)
	place(2, map[string]any{"position": 3}, gid, 3)
	place(2, map[string]any{"name": "Rechts außen"}, gid, 3) // other changes keep the place
	place(3, map[string]any{}, nil, 1)                       // without group: first free place there
	e.expect(c, "PATCH", "/api/v1/devices/"+dev[3], map[string]any{"groupId": otherGroup}, 422, "unknown_group")
	e.expect(c, "PATCH", "/api/v1/devices/"+dev[3], map[string]any{"position": 0}, 422, "out_of_range")
	e.expect(c, "PATCH", "/api/v1/devices/"+dev[3], map[string]any{"zoneId": nil, "groupId": gid}, 422, "no_zone")
	// Another zone: the group is left, the next free place there.
	place(3, map[string]any{"zoneId": other}, nil, 1)
	place(3, map[string]any{"zoneId": z}, nil, 1)
	zs := asList(e.expect(c, "GET", "/api/v1/zones", nil, 200, "")["zones"])
	if gs := asList(asMap(zs[0])["groups"]); len(gs) != 1 || asNum(asMap(gs[0])["deviceCount"]) != 3 {
		t.Fatalf("zone groups: %v", gs)
	}

	// Rules change single things; overlapping is refused only for the same thing.
	win := func(days []int, s, en string) map[string]any {
		return map[string]any{"days": days, "start": s, "end": en}
	}
	post := func(b map[string]any, code int, errc string) map[string]any {
		t.Helper()
		if b["prices"] == nil {
			b["prices"] = []any{}
		}
		return e.expect(c, "POST", "/api/v1/schedules", b, code, errc)
	}
	def := post(map[string]any{"name": "Standard", "scope": "default", "zoneId": z, "playlistId": pAll,
		"groupPlaylists": []map[string]any{{"groupId": gid, "playlistId": pMenu}}}, 200, "")
	if gp := asList(def["groupPlaylists"]); len(gp) != 1 || asMap(gp[0])["playlistId"] != pMenu {
		t.Fatalf("stored group playlists: %v", def)
	}
	lunch := post(map[string]any{"name": "Mittag", "scope": "zone", "zoneId": z, "window": win([]int{1, 2, 3, 4, 5}, "11:00", "14:00"),
		"groupPlaylists": []map[string]any{{"groupId": gid, "playlistId": pLunch}}, "ticker": map[string]any{"hide": true}}, 200, "")
	if tk := asMap(lunch["ticker"]); tk["hide"] != true {
		t.Fatalf("hidden ticker: %v", lunch["ticker"])
	}
	post(map[string]any{"name": "Werbung Mittag", "scope": "zone", "zoneId": z, "window": win([]int{1}, "12:00", "13:00"),
		"playlistId": pAll}, 200, "") // all screens vs. one group: different things
	out := post(map[string]any{"name": "Menü 2", "scope": "zone", "zoneId": z, "window": win([]int{1}, "12:30", "15:00"),
		"groupPlaylists": []map[string]any{{"groupId": gid, "playlistId": pAll}}}, 409, "window_overlap")
	if asMap(asMap(out["error"])["params"])["change"] != "playlist:"+gid {
		t.Fatalf("conflict names the shared change: %v", out)
	}
	post(map[string]any{"name": "Ticker", "scope": "zone", "zoneId": z, "window": win([]int{1}, "13:00", "13:30"),
		"ticker": map[string]any{"text": "Mahlzeit"}}, 409, "window_overlap") // the ticker again
	post(map[string]any{"name": "Fremd", "scope": "zone", "zoneId": z, "window": win([]int{6}, "11:00", "12:00"),
		"groupPlaylists": []map[string]any{{"groupId": otherGroup, "playlistId": pAll}}}, 422, "unknown_group")
	post(map[string]any{"name": "Global", "scope": "global", "window": win([]int{6}, "11:00", "12:00"),
		"groupPlaylists": []map[string]any{{"groupId": gid, "playlistId": pAll}}}, 422, "must_be_empty")
	post(map[string]any{"name": "Ohne Gruppe", "scope": "zone", "zoneId": z, "window": win([]int{6}, "11:00", "12:00"),
		"groupPlaylists": []map[string]any{{"groupId": "ungrouped", "playlistId": pLunch}}}, 200, "")

	// What runs now, per group (2026-10-12 is a Monday, Europe/Berlin).
	nowAt := func(at string) map[string]any {
		return e.expect(c, "GET", "/api/v1/schedules/now?zone="+z+"&at="+at, nil, 200, "")
	}
	r := nowAt("2026-10-12T12:30:00%2B02:00")
	groups := asList(r["groups"])
	if len(groups) != 2 {
		t.Fatalf("groups now: %v", r)
	}
	menu, rest := asMap(groups[0]), asMap(groups[1])
	if menu["groupId"] != gid || asMap(menu["playlist"])["id"] != pLunch || menu["since"] != "2026-10-12T11:00:00+02:00" || asNum(menu["devices"]) != 3 {
		t.Fatalf("menu wall at 12:30: %v", menu)
	}
	if rest["groupId"] != "ungrouped" || asMap(rest["playlist"])["id"] != pAll || rest["since"] != "2026-10-12T12:00:00+02:00" || asNum(rest["devices"]) != 1 {
		t.Fatalf("screens without group at 12:30: %v", rest)
	}
	if r["tickerHiddenBy"] != asStr(lunch["id"]) {
		t.Fatalf("ticker hidden by the lunch rule: %v", r)
	}
	r = nowAt("2026-10-12T09:00:00%2B02:00")
	if menu := asMap(asList(r["groups"])[0]); asMap(menu["playlist"])["id"] != pMenu || menu["since"] != "2026-10-12T00:00:00+02:00" {
		t.Fatalf("default rule for the group, anchored at midnight: %v", menu)
	}

	// The group's own closed playlist, anchored at the moment of closing.
	e.patchCur(c, "/api/v1/zones/"+z, map[string]any{"closedPlaylistId": pClosed}, 200, "")
	grev := asNum(g["revision"])
	g = e.expect(c, "PATCH", "/api/v1/zones/"+z+"/groups/"+gid, map[string]any{"revision": grev, "closedPlaylistId": pMenu}, 200, "")
	e.expect(c, "PATCH", "/api/v1/zones/"+z+"/groups/"+gid, map[string]any{"revision": grev, "name": "x"}, 409, "conflict")
	e.expect(c, "POST", "/api/v1/live/zones/"+z+"/state", map[string]any{"state": "CLOSED"}, 200, "")
	r = e.expect(c, "GET", "/api/v1/schedules/now?zone="+z, nil, 200, "")
	menu, rest = asMap(asList(r["groups"])[0]), asMap(asList(r["groups"])[1])
	if menu["kind"] != "closed" || asMap(menu["playlist"])["id"] != pMenu || rest["kind"] != "closed" || asMap(rest["playlist"])["id"] != pClosed {
		t.Fatalf("closed per group: %v", r["groups"])
	}
	if since, err := time.Parse(time.RFC3339, asStr(menu["since"])); err != nil || time.Since(since) > time.Minute {
		t.Fatalf("closed anchor = closing time: %v (%v)", menu["since"], err)
	}
	e.expect(c, "POST", "/api/v1/live/zones/"+z+"/state", map[string]any{"state": "OPEN"}, 200, "")

	// Playlists used by groups or group rules cannot be archived.
	e.expect(c, "DELETE", "/api/v1/playlists/"+pLunch, nil, 409, "playlist_in_use")
	e.expect(c, "DELETE", "/api/v1/playlists/"+pMenu, nil, 409, "playlist_in_use")

	// A group used by rules cannot be deleted; without rules its screens join the screens
	// without group at the next free places.
	e.expect(c, "DELETE", "/api/v1/zones/"+z+"/groups/"+gid, nil, 409, "group_in_use")
	e.expect(c, "PATCH", "/api/v1/schedules/"+asStr(def["id"]), map[string]any{"revision": def["revision"], "groupPlaylists": []any{}}, 200, "")
	e.expect(c, "PATCH", "/api/v1/schedules/"+asStr(lunch["id"]), map[string]any{"revision": lunch["revision"],
		"groupPlaylists": []any{}, "playlistId": pLunch}, 409, "window_overlap") // all screens: "Werbung Mittag" sets them
	e.expect(c, "PATCH", "/api/v1/schedules/"+asStr(lunch["id"]), map[string]any{"revision": lunch["revision"],
		"groupPlaylists": []any{}}, 200, "") // only the ticker is left
	e.expect(c, "DELETE", "/api/v1/zones/"+z+"/groups/"+gid, nil, 204, "")
	got := map[float64]bool{}
	for _, id := range dev[:3] {
		d := e.expect(c, "GET", "/api/v1/devices/"+id, nil, 200, "")
		if d["groupId"] != nil {
			t.Fatalf("device still grouped: %v", d)
		}
		got[asNum(d["position"])] = true
	}
	if !got[2] || !got[3] || !got[4] { // Tresen keeps place 1
		t.Fatalf("places after deleting the group: %v", got)
	}
	if len(e.auditActions("group.")) != 4 { // 2 created, 1 updated, 1 deleted
		t.Fatalf("audit: %v", e.auditActions("group."))
	}
}
