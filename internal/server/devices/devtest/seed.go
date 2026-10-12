// Package devtest seeds content tables for device module tests (zones, media with
// variants and blob files, a published presentation from the shared contract fixture,
// playlists, schedule rules, fonts and products). It is imported by tests only.
package devtest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"opensignage/internal/server/db"
	"opensignage/internal/server/devices/deploy"
)

const now = "2026-10-11T10:00:00Z"

// Content names the seeded objects.
type Content struct {
	Z1, Z2         string // Z1: default rule + global rule + CLOSED playlist; Z2: own zone rule
	PresentationID string
	P1             string // published, used by Z1 default rule (presentation + media item)
	P2             string // archived, used by the global rule
	P3             string // published, Z1's CLOSED playlist (single slide)
	P4             string // published, used by Z2's zone rule (media only)
	Blobs          map[string]string
	Foreign        string // blob in the blobs table and on disk, but in no deployment
}

// Exec runs a statement or fails the test.
func Exec(t testing.TB, d *db.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.W.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// Blob stores content as a blob file plus row and returns its sha256.
func Blob(t testing.TB, d *db.DB, dataDir, content, mime string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])
	p := deploy.BlobPath(dataDir, sha)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	Exec(t, d, `INSERT OR IGNORE INTO blobs(sha256, size, mime, created_at) VALUES (?, ?, ?, ?)`, sha, len(content), mime, now)
	return sha
}

// Fixture returns the shared presentation fixture.
func Fixture(t testing.TB) []byte {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "testdata", "contracts", "presentation-valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Zone inserts a zone.
func Zone(t testing.TB, d *db.DB, id, name string) {
	t.Helper()
	Exec(t, d, `INSERT INTO zones(id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`, id, name, now, now)
}

// SeedContent creates the full content set.
func SeedContent(t testing.TB, d *db.DB, dataDir string) Content {
	t.Helper()
	c := Content{Z1: "zone-1", Z2: "zone-2", PresentationID: "01920000-0000-7000-8000-000000000001",
		P1: "pl-1", P2: "pl-2", P3: "pl-3", P4: "pl-4", Blobs: map[string]string{}}
	b := func(name, mime string) string {
		sha := Blob(t, d, dataDir, "blob:"+name, mime)
		c.Blobs[name] = sha
		return sha
	}
	asset := func(id, kind, orig, meta string, trashed bool) {
		var tr any
		if trashed {
			tr = now
		}
		Exec(t, d, `INSERT INTO media_assets(id, name, kind, revision, blob_sha256, status, meta, created_at, updated_at, trashed_at)
			VALUES (?, ?, ?, 1, ?, 'ready', ?, ?, ?, ?)`, id, id, kind, orig, meta, now, now, tr)
		Exec(t, d, `INSERT INTO media_revisions(asset_id, revision, blob_sha256, meta, created_at) VALUES (?, 1, ?, ?, ?)`, id, orig, meta, now)
	}
	variant := func(id, name, sha string, w, h int) {
		Exec(t, d, `INSERT INTO media_variants(asset_id, revision, variant, blob_sha256, width, height, created_at)
			VALUES (?, 1, ?, ?, ?, ?, ?)`, id, name, sha, w, h, now)
	}
	asset("asset-video", "video", b("video-orig", "video/mp4"), `{"width":3840,"height":2160,"durationMs":12000}`, false)
	variant("asset-video", "v1080", b("video-1080", "video/mp4"), 1920, 1080)
	variant("asset-video", "v720", b("video-720", "video/mp4"), 1280, 720)
	variant("asset-video", "poster", b("video-poster", "image/jpeg"), 1920, 1080)
	variant("asset-video", "thumb", b("video-thumb", "image/jpeg"), 320, 180)
	asset("asset-image", "image", b("image-orig", "image/png"), `{"width":2400,"height":1200}`, false)
	variant("asset-image", "i1920", b("image-1920", "image/webp"), 1920, 960)
	variant("asset-image", "thumb", b("image-thumb", "image/jpeg"), 320, 160)
	asset("asset-media", "video", b("media-orig", "video/mp4"), `{"width":1280,"height":720,"durationMs":5000}`, false)
	asset("asset-prod", "image", b("prod-orig", "image/png"), `{"width":1000,"height":1000}`, false)
	variant("asset-prod", "i512", b("prod-512", "image/webp"), 512, 512)
	variant("asset-prod", "thumb", b("prod-thumb", "image/jpeg"), 320, 320)
	asset("asset-prod2", "image", b("prod2-orig", "image/png"), `{"width":400,"height":400}`, false)
	asset("asset-trashed", "image", b("trashed-orig", "image/png"), `{}`, true)
	asset("asset-z2", "image", b("z2-orig", "image/jpeg"), `{"width":1920,"height":1080}`, false)
	c.Foreign = b("foreign", "image/png")

	Exec(t, d, `INSERT INTO font_faces(id, family, style, weight_min, weight_max, blob_sha256, format, license, created_at)
		VALUES ('font-1', 'Montserrat', 'normal', 100, 900, ?, 'woff2', 'OFL', ?)`, b("font", "font/woff2"), now)

	doc := string(Fixture(t))
	Exec(t, d, `INSERT INTO presentations(id, name, draft, published_version, has_changes, created_at, updated_at)
		VALUES (?, 'Bar menu', ?, 1, 0, ?, ?)`, c.PresentationID, doc, now, now)
	Exec(t, d, `INSERT INTO presentation_versions(presentation_id, version, doc, published_at) VALUES (?, 1, ?, ?)`, c.PresentationID, doc, now)

	playlist := func(id, doc string, archived bool) {
		var ar any
		if archived {
			ar = now
		}
		Exec(t, d, `INSERT INTO playlists(id, name, draft, published_version, has_changes, created_at, updated_at, archived_at)
			VALUES (?, ?, ?, 1, 0, ?, ?, ?)`, id, id, doc, now, now, ar)
		Exec(t, d, `INSERT INTO playlist_versions(playlist_id, version, doc, published_at) VALUES (?, 1, ?, ?)`, id, doc, now)
	}
	playlist(c.P1, `{"id":"pl-1","name":"Main","items":[
		{"id":"i1","kind":"presentation","refId":"`+c.PresentationID+`","play":{"mode":"repeat","count":1}},
		{"id":"i2","kind":"media","refId":"asset-media","play":{"mode":"duration","ms":5000},"fit":"cover"}]}`, false)
	playlist(c.P2, `{"id":"pl-2","name":"Old","items":[{"id":"i1","kind":"media","refId":"asset-trashed","play":{"mode":"duration","ms":5000}}]}`, true)
	playlist(c.P3, `{"name":"Closed","items":[{"id":"i1","kind":"slide","refId":"s2","presentationId":"`+c.PresentationID+`","play":{"mode":"repeat","count":2}}],"id":"pl-3"}`, false)
	playlist(c.P4, `{"id":"pl-4","name":"Z2","items":[{"id":"i1","kind":"media","refId":"asset-z2","play":{"mode":"duration","ms":8000}}]}`, false)

	Zone(t, d, c.Z1, "Bar")
	Zone(t, d, c.Z2, "Terrace")
	Exec(t, d, `UPDATE zones SET closed_playlist_id = ? WHERE id = ?`, c.P3, c.Z1)

	Exec(t, d, `INSERT INTO categories(id, name, created_at, updated_at) VALUES ('cat', 'Drinks', ?, ?)`, now, now)
	product := func(id, img string, archived bool) {
		var ar, im any
		if archived {
			ar = now
		}
		if img != "" {
			im = img
		}
		Exec(t, d, `INSERT INTO products(id, category_id, name, type, price_amount, image_asset_id, archived_at, created_at, updated_at)
			VALUES (?, 'cat', ?, 'drink', 500, ?, ?, ?, ?)`, id, id, im, ar, now, now)
	}
	product("prod-1", "asset-prod", false)
	product("prod-2", "asset-prod2", false)
	product("prod-3", "asset-trashed", false)
	product("prod-4", "asset-image", true) // archived: its image is not a product image
	product("prod-5", "", false)

	rule := func(id, name, scope, zone, days, start, end, playlist string, enabled bool) {
		var z, dy, st, en, pl any
		if zone != "" {
			z = zone
		}
		if days != "" {
			dy, st, en = days, start, end
		}
		if playlist != "" {
			pl = playlist
		}
		Exec(t, d, `INSERT INTO schedule_rules(id, name, scope, zone_id, days, start_time, end_time, playlist_id, enabled, revision, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 3, ?, ?)`, id, name, scope, z, dy, st, en, pl, enabled, now, now)
	}
	rule("r1", "Default", "default", c.Z1, "", "", "", c.P1, true)
	rule("r2", "Happy hour", "global", "", "[5,1,3]", "17:00", "19:00", c.P2, true)
	rule("r3", "Terrace", "zone", c.Z2, "[1,2,3,4,5,6,7]", "00:00", "00:00", c.P4, true)
	rule("r4", "Disabled", "default", c.Z1, "", "", "", c.P4, false)
	rule("r5", "Prices only", "zone", c.Z1, "[6]", "22:00", "02:00", "", true)
	Exec(t, d, `INSERT INTO schedule_prices(rule_id, product_id, amount, currency) VALUES ('r5', 'prod-2', 350, 'EUR')`)
	Exec(t, d, `INSERT INTO schedule_prices(rule_id, product_id, amount, currency) VALUES ('r5', 'prod-1', 300, 'EUR')`)
	return c
}
