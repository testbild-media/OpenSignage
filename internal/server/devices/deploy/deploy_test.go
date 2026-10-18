package deploy_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"opensignage/internal/contracts"
	"opensignage/internal/schedule"
	"opensignage/internal/server/db"
	"opensignage/internal/server/devices/deploy"
	"opensignage/internal/server/devices/devtest"
)

func setup(t *testing.T) (*deploy.Builder, devtest.Content) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(context.Background(), filepath.Join(dir, "db", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	c := devtest.SeedContent(t, d, dir)
	return &deploy.Builder{DB: d, DataDir: dir}, c
}

func count(t *testing.T, b *deploy.Builder, q string, args ...any) int {
	t.Helper()
	var n int
	if err := b.DB.R.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBuildManifest(t *testing.T) {
	b, c := setup(t)
	ctx := context.Background()
	m, err := b.Build(ctx, c.Z1, "Europe/Berlin", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	var ruleIDs []string
	for _, r := range m.Rules {
		ruleIDs = append(ruleIDs, r.ID)
	}
	if strings.Join(ruleIDs, ",") != "r1,r2,r5" {
		t.Fatalf("rules: %v", ruleIDs)
	}
	r2, r5 := m.Rules[1], m.Rules[2]
	if r2.Window == nil || r2.ZoneID != "" || len(r2.Window.Days) != 3 || r2.Window.Days[0] != 1 || r2.PlaylistID != c.P2 || r2.Revision != 3 {
		t.Fatalf("global rule: %+v", r2)
	}
	if len(r5.Prices) != 2 || r5.Prices[0].ProductID != "prod-1" || r5.Prices[0].Price.Amount != 300 || r5.PlaylistID != "" {
		t.Fatalf("price rule: %+v", r5)
	}
	if m.Rules[0].Window != nil || m.Rules[0].Prices == nil {
		t.Fatalf("default rule: %+v", m.Rules[0])
	}
	// Playlists: the archived one is absent (its rule stays, the evaluator skips it).
	var plIDs []string
	for _, raw := range m.Playlists {
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		plIDs = append(plIDs, p.ID)
	}
	if strings.Join(plIDs, ",") != c.P1+","+c.P3 || m.ClosedPlaylistID != c.P3 {
		t.Fatalf("playlists %v closed %q", plIDs, m.ClosedPlaylistID)
	}
	if !strings.HasPrefix(string(m.Playlists[1]), `{"id":"pl-3","items":`) {
		t.Fatalf("playlist JSON must be canonical: %s", m.Playlists[1])
	}
	if len(m.Presentations) != 1 || m.Presentations[0].ID != c.PresentationID || len(m.Presentations[0].Slides) != 2 {
		t.Fatalf("presentations: %d", len(m.Presentations))
	}
	var assets []string
	for _, a := range m.Assets {
		assets = append(assets, a.ID+"/"+a.Variant)
		if a.Variant == "thumb" {
			t.Fatal("thumbnails must never be shipped")
		}
		if a.URL != "/device/v1/blobs/"+a.SHA256 || a.Size <= 0 || a.Mime == "" {
			t.Fatalf("asset entry: %+v", a)
		}
	}
	want := "asset-image/orig,asset-image/i1920,asset-media/orig,asset-prod/i512,asset-prod2/orig," +
		"asset-video/orig,asset-video/poster,asset-video/v1080,asset-video/v720"
	if strings.Join(assets, ",") != want {
		t.Fatalf("assets:\n got %s\nwant %s", strings.Join(assets, ","), want)
	}
	for _, a := range m.Assets {
		if a.ID == "asset-video" && a.Variant == "v1080" && (a.Width != 1920 || a.DurationMS != 12000) {
			t.Fatalf("video variant meta: %+v", a)
		}
		if a.ID == "asset-video" && a.Variant == "orig" && a.Width != 3840 {
			t.Fatalf("orig meta: %+v", a)
		}
	}
	if strings.Join(m.Products.ImageAssetIDs, ",") != "asset-prod,asset-prod2" {
		t.Fatalf("product images: %v", m.Products.ImageAssetIDs)
	}
	if len(m.Fonts) != 1 || m.Fonts[0].Family != "Montserrat" || m.Fonts[0].Files[0].Weight != [2]int{100, 900} {
		t.Fatalf("fonts: %+v", m.Fonts)
	}
	if m.Timezone != "Europe/Berlin" || m.MinAgentVersion != "1.2.3" || m.ZoneID != c.Z1 {
		t.Fatalf("header: %+v", m)
	}

	// The shared evaluator works on the manifest alone.
	available := map[string]bool{}
	for _, id := range plIDs {
		available[id] = true
	}
	res := schedule.Evaluate(schedule.Input{Rules: m.Rules, ZoneID: c.Z1, Now: mustTime(t, "2026-10-12T18:00:00Z"),
		PlaylistAvailable: func(id string) bool { return available[id] }})
	if res.Kind != schedule.KindRule || res.PlaylistID != c.P1 {
		t.Fatalf("evaluation must skip the archived global playlist: %+v", res)
	}

	z2, err := b.Build(ctx, c.Z2, "UTC", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if len(z2.Playlists) != 1 || len(z2.Presentations) != 0 || len(z2.Fonts) != 0 || len(z2.Rules) != 2 {
		t.Fatalf("zone 2 manifest: %d playlists %d presentations %d rules", len(z2.Playlists), len(z2.Presentations), len(z2.Rules))
	}
}

func TestApplyDeterministicAndCleared(t *testing.T) {
	b, c := setup(t)
	ctx := context.Background()
	m1, err := b.Build(ctx, c.Z1, "UTC", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	m2, err := b.Build(ctx, c.Z1, "UTC", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := m1.CanonicalHash()
	h2, _ := m2.CanonicalHash()
	if h1 != h2 || h1 == "" {
		t.Fatalf("hash not deterministic: %s %s", h1, h2)
	}
	t1, changed, err := b.Apply(ctx, c.Z1, m1)
	if err != nil || !changed || t1.Kind != deploy.KindDeployment || t1.Hash != h1 {
		t.Fatalf("first apply: %+v %v %v", t1, changed, err)
	}
	t2, changed, err := b.Apply(ctx, c.Z1, m2)
	if err != nil || changed || t2.ID != t1.ID {
		t.Fatalf("identical content must not create a deployment: %+v %v %v", t2, changed, err)
	}
	if n := count(t, b, `SELECT COUNT(*) FROM deployments WHERE zone_id = ?`, c.Z1); n != 1 {
		t.Fatalf("deployments: %d", n)
	}
	if n := count(t, b, `SELECT COUNT(*) FROM deployment_blobs WHERE deployment_id = ?`, t1.ID); n != len(m1.BlobHashes()) || n != 10 {
		t.Fatalf("deployment blobs: %d (manifest %d)", n, len(m1.BlobHashes()))
	}
	// Stored manifest carries id/hash/builtAt and hashes to the same value.
	var body string
	if err := b.DB.R.QueryRow(`SELECT manifest FROM deployments WHERE id = ?`, t1.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var stored contracts.Manifest
	if err := json.Unmarshal([]byte(body), &stored); err != nil {
		t.Fatal(err)
	}
	if hs, _ := stored.CanonicalHash(); stored.ID != t1.ID || stored.Hash != h1 || hs != h1 || stored.BuiltAt == "" {
		t.Fatalf("stored manifest: id=%s hash=%s recomputed=%s", stored.ID, stored.Hash, hs)
	}

	// Content change → new deployment, old one superseded.
	devtest.Exec(t, b.DB, `UPDATE schedule_rules SET revision = 4 WHERE id = 'r1'`)
	m3, _ := b.Build(ctx, c.Z1, "UTC", "1.0.0")
	t3, changed, err := b.Apply(ctx, c.Z1, m3)
	if err != nil || !changed || t3.ID == t1.ID {
		t.Fatalf("changed content: %+v %v %v", t3, changed, err)
	}
	if n := count(t, b, `SELECT COUNT(*) FROM deployments WHERE zone_id = ? AND superseded_at IS NULL`, c.Z1); n != 1 {
		t.Fatalf("exactly one current deployment expected, got %d", n)
	}

	// Nothing playable → cleared.
	devtest.Exec(t, b.DB, `UPDATE zones SET closed_playlist_id = NULL WHERE id = ?`, c.Z1)
	devtest.Exec(t, b.DB, `UPDATE playlists SET archived_at = '2026-10-11T11:00:00Z' WHERE id = ?`, c.P1)
	m4, _ := b.Build(ctx, c.Z1, "UTC", "1.0.0")
	if len(m4.Playlists) != 0 {
		t.Fatal("expected empty manifest")
	}
	t4, changed, err := b.Apply(ctx, c.Z1, m4)
	if err != nil || !changed || t4.Kind != deploy.KindCleared || t4.Reason != deploy.ReasonNoContent {
		t.Fatalf("cleared: %+v %v %v", t4, changed, err)
	}
	if cur, _ := deploy.CurrentTarget(ctx, b.DB.R, c.Z1); cur.Kind != deploy.KindCleared {
		t.Fatalf("current target: %+v", cur)
	}
	if _, changed, _ := b.Apply(ctx, c.Z1, m4); changed {
		t.Fatal("clearing twice is no change")
	}
	// Content returns to the previous state → the same deployment is re-activated.
	devtest.Exec(t, b.DB, `UPDATE zones SET closed_playlist_id = ? WHERE id = ?`, c.P3, c.Z1)
	devtest.Exec(t, b.DB, `UPDATE playlists SET archived_at = NULL WHERE id = ?`, c.P1)
	m5, _ := b.Build(ctx, c.Z1, "UTC", "1.0.0")
	t5, changed, err := b.Apply(ctx, c.Z1, m5)
	if err != nil || !changed || t5.ID != t3.ID {
		t.Fatalf("reactivation: %+v %v %v (want %s)", t5, changed, err, t3.ID)
	}
	if n := count(t, b, `SELECT COUNT(*) FROM deployments WHERE zone_id = ?`, c.Z1); n != 2 {
		t.Fatalf("deployments after reactivation: %d", n)
	}
}

// TestTickerTemplateFontIsShipped: a custom font used only by the zone's ticker template
// (no slide uses it) must still be part of the zone's manifest.
func TestTickerTemplateFontIsShipped(t *testing.T) {
	b, c := setup(t)
	ctx := context.Background()
	devtest.Exec(t, b.DB, `INSERT INTO font_faces(id, family, style, weight_min, weight_max, blob_sha256, format, license, created_at)
		VALUES ('font-t', 'Bar Script', 'normal', 400, 400, ?, 'woff2', 'OFL', 'x')`,
		devtest.Blob(t, b.DB, b.DataDir, "blob:bar-script", "font/woff2"))
	devtest.Exec(t, b.DB, `INSERT INTO ticker_templates(id, name, style, created_at, updated_at)
		VALUES ('tt', 'Script', '{"font":"Bar Script","size":40}', 'x', 'x')`)
	families := func(zone string) []string {
		t.Helper()
		m, err := b.Build(ctx, zone, "UTC", "1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range m.Fonts {
			out = append(out, f.Family)
		}
		return out
	}
	// Without a rule ticker the zone's template font is not needed.
	devtest.Exec(t, b.DB, `UPDATE zones SET ticker_template_id = 'tt' WHERE id = ?`, c.Z2)
	if got := families(c.Z2); len(got) != 0 { // zone 2 plays media only
		t.Fatalf("fonts without a rule ticker: %v", got)
	}
	// A rule with a ticker and no own template: the zone's template, resolved into the
	// manifest, and its font is shipped.
	devtest.Exec(t, b.DB, `UPDATE schedule_rules SET ticker_text = 'Happy Hour' WHERE zone_id = ?`, c.Z2)
	if got := strings.Join(families(c.Z2), ","); got != "Bar Script" {
		t.Fatalf("the ticker template font must be shipped: %q", got)
	}
	m, err := b.Build(ctx, c.Z2, "UTC", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	var tk *contracts.RuleTicker
	for _, r := range m.Rules {
		if r.Ticker != nil {
			tk = r.Ticker
		}
	}
	if tk == nil || tk.Text != "Happy Hour" || tk.Template == nil || tk.Template.Font != "Bar Script" || tk.Template.Size != 40 ||
		tk.Template.Speed != contracts.DefaultTickerTemplate().Speed {
		t.Fatalf("rule ticker in the manifest: %+v", tk)
	}
	// Its own template wins over the zone's; no template at all: the default.
	devtest.Exec(t, b.DB, `INSERT INTO ticker_templates(id, name, style, created_at, updated_at)
		VALUES ('t2', 'Big', '{"font":"Montserrat","size":60}', 'x', 'x')`)
	devtest.Exec(t, b.DB, `UPDATE schedule_rules SET ticker_template_id = 't2' WHERE zone_id = ?`, c.Z2)
	if m, err = b.Build(ctx, c.Z2, "UTC", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Rules {
		if r.Ticker != nil && (r.Ticker.Template.Size != 60 || r.Ticker.Template.Font != "Montserrat") {
			t.Fatalf("rule template must win: %+v", r.Ticker.Template)
		}
	}
	devtest.Exec(t, b.DB, `UPDATE schedule_rules SET ticker_template_id = NULL WHERE zone_id = ?`, c.Z2)
	devtest.Exec(t, b.DB, `UPDATE zones SET ticker_template_id = NULL WHERE id = ?`, c.Z2)
	if m, err = b.Build(ctx, c.Z2, "UTC", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Rules {
		if r.Ticker != nil && *r.Ticker.Template != contracts.DefaultTickerTemplate() {
			t.Fatalf("default template expected: %+v", r.Ticker.Template)
		}
	}
}

func TestPruneKeepsUsedDeployments(t *testing.T) {
	b, c := setup(t)
	ctx := context.Background()
	var first string
	for i := 0; i < 9; i++ {
		devtest.Exec(t, b.DB, `UPDATE schedule_rules SET revision = ? WHERE id = 'r1'`, 10+i)
		m, err := b.Build(ctx, c.Z1, "UTC", "1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		tg, _, err := b.Apply(ctx, c.Z1, m)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = tg.ID
		}
	}
	devtest.Exec(t, b.DB, `INSERT INTO devices(id, hardware_id, name, config, previous_deployment_id, created_at, updated_at)
		VALUES ('dev', 'hw', 'D', '{}', ?, 'x', 'x')`, first)
	devtest.Exec(t, b.DB, `INSERT INTO deployments(id, zone_id, hash, manifest, built_at) VALUES ('orphan', 'deleted-zone', 'h', '{}', 'x')`)
	n, err := b.Prune(ctx, deploy.Keep)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 { // 9 - 5 kept - 1 protected + 1 orphan of a deleted zone
		t.Fatalf("pruned %d", n)
	}
	if count(t, b, `SELECT COUNT(*) FROM deployments WHERE id = ?`, first) != 1 {
		t.Fatal("deployment used by a device must survive")
	}
	if count(t, b, `SELECT COUNT(*) FROM deployments WHERE zone_id = ?`, c.Z1) != 6 {
		t.Fatal("expected 5 newest + 1 protected")
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tt
}

// Display groups with their closed playlist, playlists of single groups, hidden tickers
// and the contents of further tracks reach the manifest.
func TestManifestGroupsAndTracks(t *testing.T) {
	b, c := setup(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	devtest.Exec(t, b.DB, `INSERT INTO playlist_versions(playlist_id, version, doc, published_at) VALUES (?, 2, ?, ?)`, c.P4,
		`{"id":"pl-4","name":"Wall","screens":2,"items":[{"id":"i1","kind":"media","refId":"asset-z2","play":{"mode":"duration","ms":8000},
		"cells":[{"kind":"media","refId":"asset-image"}]}]}`, now)
	devtest.Exec(t, b.DB, `UPDATE playlists SET published_version = 2 WHERE id = ?`, c.P4)
	devtest.Exec(t, b.DB, `INSERT INTO display_groups(id, zone_id, name, closed_playlist_id, created_at, updated_at) VALUES ('g1', ?, 'Wand', ?, ?, ?)`,
		c.Z1, c.P4, now, now)
	devtest.Exec(t, b.DB, `INSERT INTO display_groups(id, zone_id, name, closed_playlist_id, created_at, updated_at) VALUES ('g2', ?, 'Tresen', ?, ?, ?)`,
		c.Z1, c.P2, now, now) // archived playlist: not playable
	devtest.Exec(t, b.DB, `INSERT INTO schedule_group_playlists(rule_id, group_id, playlist_id) VALUES ('r5', 'g1', ?)`, c.P4)
	devtest.Exec(t, b.DB, `UPDATE schedule_rules SET ticker_hide = 1 WHERE id = 'r5'`)
	m, err := b.Build(ctx, c.Z1, "Europe/Berlin", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Groups) != 2 || m.Groups[0].ID != "g1" || m.Groups[0].ClosedPlaylistID != c.P4 || m.Groups[1].ClosedPlaylistID != "" {
		t.Fatalf("groups: %+v", m.Groups)
	}
	r5 := m.Rules[2]
	if len(r5.GroupPlaylists) != 1 || r5.GroupPlaylists[0].PlaylistID != c.P4 || r5.Ticker == nil || !r5.Ticker.Hide || r5.Ticker.Template != nil {
		t.Fatalf("rule r5: %+v", r5)
	}
	found := false
	for _, a := range m.Assets {
		found = found || a.ID == "asset-image" // only used by the second track
	}
	if !found {
		t.Fatalf("the medium of track 2 must be shipped: %+v", m.Assets)
	}
}

// Category prices reach the screens as product prices: the product's own price first, then
// the subcategory, then the category; archived products get none.
func TestManifestCategoryPrices(t *testing.T) {
	b, c := setup(t)
	now := db.Now()
	devtest.Exec(t, b.DB, `INSERT INTO categories(id, parent_id, name, created_at, updated_at) VALUES ('sub', 'cat', 'Sub', ?, ?)`, now, now)
	devtest.Exec(t, b.DB, `UPDATE products SET category_id = 'sub' WHERE id = 'prod-5'`)
	devtest.Exec(t, b.DB, `INSERT INTO schedule_category_prices(rule_id, category_id, amount, currency) VALUES ('r5', 'cat', 450, 'EUR')`)
	devtest.Exec(t, b.DB, `INSERT INTO schedule_category_prices(rule_id, category_id, amount, currency) VALUES ('r5', 'sub', 420, 'EUR')`)
	m, err := b.Build(context.Background(), c.Z1, "Europe/Berlin", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range m.Rules[2].Prices {
		if p.CategoryID != "" {
			t.Fatalf("category price in the manifest: %+v", p)
		}
		got = append(got, p.ProductID+"="+strconv.FormatInt(p.Price.Amount, 10))
	}
	if strings.Join(got, ",") != "prod-1=300,prod-2=350,prod-3=450,prod-5=420" {
		t.Fatalf("prices: %v", got)
	}
}
