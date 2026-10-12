// Package deploy builds the immutable per-zone deployment manifests (ARCHITECTURE §5.5).
//
// A zone's manifest contains its rule set (zone, global and default rules), every
// published playlist those rules or the zone's CLOSED playlist can reach, the published
// presentations those playlists use, every asset variant (except thumbnails) and the
// uploaded fonts the presentations need. The canonical hash decides whether a new
// deployment row is written; identical content never produces a new deployment. An
// empty result (nothing playable) makes the zone target `cleared`.
package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"opensignage/internal/common/ids"
	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
)

// Target kinds and cleared reasons.
const (
	KindDeployment  = "deployment"
	KindCleared     = "cleared"
	ReasonNoContent = "no_content"
	ReasonNoZone    = "no_zone"
)

// Keep is the number of deployments retained per zone (plus any a device still uses).
const Keep = 5

// TimeFormat is fixed width so stored timestamps sort lexicographically.
const TimeFormat = "2006-01-02T15:04:05.000000000Z07:00"

// ErrZoneNotFound is returned by Build for unknown zones.
var ErrZoneNotFound = errors.New("zone not found")

// Target is what a zone should currently show.
type Target struct {
	Kind   string `json:"kind"` // deployment | cleared
	ID     string `json:"id,omitempty"`
	Hash   string `json:"hash,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Cleared returns a cleared target.
func Cleared(reason string) Target { return Target{Kind: KindCleared, Reason: reason} }

// BlobPath returns the content-addressed location of a blob (ARCHITECTURE §6). Every
// stored file (originals, variants, uploaded fonts) is a row in `blobs` and lives here.
func BlobPath(dataDir, sha string) string {
	if len(sha) < 2 {
		return filepath.Join(dataDir, "blobs", "sha256", "invalid")
	}
	return filepath.Join(dataDir, "blobs", "sha256", sha[:2], sha)
}

// ValidSHA256 reports whether s is a lowercase hex sha256.
func ValidSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// BlobURL is the device channel URL of a blob.
func BlobURL(sha string) string { return "/device/v1/blobs/" + sha }

// Builder reads content tables and writes deployments.
type Builder struct {
	DB      *db.DB
	DataDir string
	Log     *slog.Logger
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (b *Builder) log() *slog.Logger {
	if b.Log != nil {
		return b.Log
	}
	return slog.Default()
}

// Build computes the manifest of a zone without writing anything. A manifest without
// playlists means "nothing playable" (cleared).
func (b *Builder) Build(ctx context.Context, zoneID, timezone, serverVersion string) (*contracts.Manifest, error) {
	tx, err := b.DB.R.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var closed sql.NullString
	var tickerStyle string
	err = tx.QueryRowContext(ctx, `SELECT z.closed_playlist_id, COALESCE(t.style, '') FROM zones z
		LEFT JOIN ticker_templates t ON t.id = z.ticker_template_id WHERE z.id = ?`, zoneID).Scan(&closed, &tickerStyle)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrZoneNotFound
	}
	if err != nil {
		return nil, err
	}
	m := &contracts.Manifest{
		ZoneID: zoneID, MinAgentVersion: serverVersion, Timezone: timezone,
		Rules: []contracts.Rule{}, Playlists: []json.RawMessage{}, Presentations: []contracts.Presentation{},
		Products: contracts.ManifestProducts{ImageAssetIDs: []string{}}, Assets: []contracts.ManifestAsset{},
		Fonts: []contracts.ManifestFont{},
	}
	if m.Rules, err = b.loadRules(ctx, tx, zoneID); err != nil {
		return nil, err
	}
	if err := b.resolveTickers(ctx, tx, m.Rules, tickerStyle); err != nil {
		return nil, err
	}
	groups, err := loadGroups(ctx, tx, zoneID)
	if err != nil {
		return nil, err
	}
	var playlistIDs []string
	for _, r := range m.Rules {
		if r.PlaylistID != "" {
			playlistIDs = append(playlistIDs, r.PlaylistID)
		}
		for _, g := range r.GroupPlaylists {
			playlistIDs = append(playlistIDs, g.PlaylistID)
		}
	}
	if closed.Valid && closed.String != "" {
		playlistIDs = append(playlistIDs, closed.String)
	}
	for _, g := range groups {
		if g.ClosedPlaylistID != "" {
			playlistIDs = append(playlistIDs, g.ClosedPlaylistID)
		}
	}
	playlists, err := b.loadPlaylists(ctx, tx, uniq(playlistIDs))
	if err != nil {
		return nil, err
	}
	if len(groups) > 0 {
		for i := range groups {
			if playlists[groups[i].ClosedPlaylistID] == nil {
				groups[i].ClosedPlaylistID = "" // not playable: the zone's closed playlist applies
			}
		}
		m.Groups = groups
	}
	if len(playlists) == 0 {
		return m, nil // cleared: rules alone show nothing
	}
	if closed.Valid && playlists[closed.String] != nil {
		m.ClosedPlaylistID = closed.String
	}

	var presIDs, mediaIDs []string
	for _, id := range sortedKeys(playlists) {
		pl := playlists[id]
		m.Playlists = append(m.Playlists, pl.raw)
		for _, it := range pl.refs() {
			switch it.Kind {
			case "presentation":
				presIDs = append(presIDs, it.RefID)
			case "slide":
				presIDs = append(presIDs, it.PresentationID)
			case "media":
				mediaIDs = append(mediaIDs, it.RefID)
			}
		}
	}
	if m.Presentations, err = b.loadPresentations(ctx, tx, uniq(presIDs)); err != nil {
		return nil, err
	}
	fullAssets := append([]string(nil), mediaIDs...)
	var families []string
	// Tickers of time schedule rules render with their template font: an uploaded family
	// must reach the players even when no slide uses it.
	for _, r := range m.Rules {
		if r.Ticker != nil && r.Ticker.Template != nil {
			families = append(families, r.Ticker.Template.Font)
		}
	}
	for i := range m.Presentations {
		a, f := m.Presentations[i].References()
		fullAssets = append(fullAssets, a...)
		families = append(families, f...)
	}
	productImages, err := loadProductImages(ctx, tx)
	if err != nil {
		return nil, err
	}
	m.Assets, m.Products.ImageAssetIDs, err = b.loadAssets(ctx, tx, uniq(fullAssets), productImages)
	if err != nil {
		return nil, err
	}
	if m.Fonts, err = b.loadFonts(ctx, tx, uniq(families)); err != nil {
		return nil, err
	}
	return m, nil
}

// resolveTickers puts the style of every rule ticker into the manifest: the rule's own
// template, else the zone's template (zoneStyle), else the default. Players need no
// template list, and a changed template rebuilds the deployment (zones event).
func (b *Builder) resolveTickers(ctx context.Context, q querier, rules []contracts.Rule, zoneStyle string) error {
	styles := map[string]string{}
	for _, r := range rules {
		if r.Ticker != nil && !r.Ticker.Hide && r.Ticker.TemplateID != "" {
			styles[r.Ticker.TemplateID] = ""
		}
	}
	for id := range styles {
		var st string
		err := q.QueryRowContext(ctx, `SELECT style FROM ticker_templates WHERE id = ?`, id).Scan(&st)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("deploy: ticker template: %w", err)
		}
		styles[id] = st
	}
	for i := range rules {
		t := rules[i].Ticker
		if t == nil || t.Hide {
			continue
		}
		style := zoneStyle
		if t.TemplateID != "" && styles[t.TemplateID] != "" {
			style = styles[t.TemplateID]
		}
		tpl := b.parseTickerStyle(rules[i].ID, style)
		t.Template = &tpl
	}
	return nil
}

// parseTickerStyle reads a stored ticker template; missing or invalid ones fall back to
// the default template so the ticker always renders.
func (b *Builder) parseTickerStyle(ruleID, style string) contracts.TickerTemplate {
	def := contracts.DefaultTickerTemplate()
	if strings.TrimSpace(style) == "" {
		return def
	}
	t := def
	if err := json.Unmarshal([]byte(style), &t); err != nil {
		b.log().Error("deploy: ticker template unreadable, using default", "rule", ruleID, "err", err)
		return def
	}
	if err := t.Validate(nil); err != nil {
		b.log().Error("deploy: ticker template invalid, using default", "rule", ruleID, "err", err)
		return def
	}
	return t
}

// ---------------------------------------------------------------- rules

func (b *Builder) loadRules(ctx context.Context, q querier, zoneID string) ([]contracts.Rule, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, scope, COALESCE(zone_id, ''), days, start_time, end_time,
			COALESCE(playlist_id, ''), revision, ticker_text, COALESCE(ticker_template_id, ''), ticker_hide
		FROM schedule_rules
		WHERE enabled = 1 AND (scope = 'global' OR (scope IN ('zone', 'default') AND zone_id = ?))
		ORDER BY id`, zoneID)
	if err != nil {
		return nil, fmt.Errorf("deploy: rules: %w", err)
	}
	out := []contracts.Rule{}
	idx := map[string]int{}
	for rows.Next() {
		var r contracts.Rule
		var days, start, end, tickerText sql.NullString
		var tickerTemplate string
		var tickerHide int
		if err := rows.Scan(&r.ID, &r.Name, &r.Scope, &r.ZoneID, &days, &start, &end, &r.PlaylistID, &r.Revision,
			&tickerText, &tickerTemplate, &tickerHide); err != nil {
			rows.Close()
			return nil, err
		}
		switch {
		case tickerHide == 1:
			r.Ticker = &contracts.RuleTicker{Hide: true}
		case tickerText.Valid:
			r.Ticker = &contracts.RuleTicker{Text: tickerText.String, TemplateID: tickerTemplate}
		}
		r.Enabled = true
		r.Prices = []contracts.PriceOverride{}
		if r.Scope != contracts.ScopeDefault {
			w := contracts.Window{Days: []int{}, Start: start.String, End: end.String}
			if days.Valid && days.String != "" {
				if err := json.Unmarshal([]byte(days.String), &w.Days); err != nil {
					b.log().Error("deploy: rule with unreadable days skipped", "rule", r.ID, "err", err)
					continue
				}
			}
			sort.Ints(w.Days)
			r.Window = &w
		}
		if r.Scope == contracts.ScopeGlobal {
			r.ZoneID = ""
		}
		idx[r.ID] = len(out)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(out))
	for _, r := range out {
		args = append(args, r.ID)
	}
	prows, err := q.QueryContext(ctx, `SELECT rule_id, product_id, amount, currency FROM schedule_prices
		WHERE rule_id IN (`+placeholders(len(args))+`) ORDER BY rule_id, product_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("deploy: prices: %w", err)
	}
	for prows.Next() {
		var ruleID string
		var p contracts.PriceOverride
		if err := prows.Scan(&ruleID, &p.ProductID, &p.Price.Amount, &p.Price.Currency); err != nil {
			prows.Close()
			return nil, err
		}
		if i, ok := idx[ruleID]; ok {
			out[i].Prices = append(out[i].Prices, p)
		}
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return nil, err
	}
	grows, err := q.QueryContext(ctx, `SELECT rule_id, group_id, playlist_id FROM schedule_group_playlists
		WHERE rule_id IN (`+placeholders(len(args))+`) ORDER BY rule_id, group_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("deploy: group playlists: %w", err)
	}
	defer grows.Close()
	for grows.Next() {
		var ruleID string
		var g contracts.GroupPlaylist
		if err := grows.Scan(&ruleID, &g.GroupID, &g.PlaylistID); err != nil {
			return nil, err
		}
		if i, ok := idx[ruleID]; ok {
			out[i].GroupPlaylists = append(out[i].GroupPlaylists, g)
		}
	}
	return out, grows.Err()
}

// loadGroups reads the display groups of the zone, ordered by id so the manifest hash
// stays stable.
func loadGroups(ctx context.Context, q querier, zoneID string) ([]contracts.ManifestGroup, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, COALESCE(closed_playlist_id, '') FROM display_groups WHERE zone_id = ? ORDER BY id`, zoneID)
	if err != nil {
		return nil, fmt.Errorf("deploy: groups: %w", err)
	}
	defer rows.Close()
	var out []contracts.ManifestGroup
	for rows.Next() {
		var g contracts.ManifestGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.ClosedPlaylistID); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- playlists & presentations

type playlistItem struct {
	Kind           string `json:"kind"`
	RefID          string `json:"refId"`
	PresentationID string `json:"presentationId"`
	// Cells: further tracks; null entries show track 1.
	Cells []*playlistItem `json:"cells"`
}

type playlist struct {
	raw   json.RawMessage
	items []playlistItem
}

// refs lists the items and the cells of further tracks.
func (p *playlist) refs() []playlistItem {
	var out []playlistItem
	for _, it := range p.items {
		out = append(out, it)
		for _, c := range it.Cells {
			if c != nil {
				out = append(out, *c)
			}
		}
	}
	return out
}

func (b *Builder) loadPlaylists(ctx context.Context, q querier, ids []string) (map[string]*playlist, error) {
	out := map[string]*playlist{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT p.id, v.doc FROM playlists p
		JOIN playlist_versions v ON v.playlist_id = p.id AND v.version = p.published_version
		WHERE p.archived_at IS NULL AND p.published_version IS NOT NULL AND p.id IN (`+placeholders(len(ids))+`)`, anys(ids)...)
	if err != nil {
		return nil, fmt.Errorf("deploy: playlists: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, doc string
		if err := rows.Scan(&id, &doc); err != nil {
			return nil, err
		}
		var parsed struct {
			Items []playlistItem `json:"items"`
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
			b.log().Error("deploy: published playlist unreadable, skipped", "playlist", id, "err", err)
			continue
		}
		if err := json.Unmarshal([]byte(doc), &obj); err != nil || obj == nil {
			b.log().Error("deploy: published playlist is not an object, skipped", "playlist", id)
			continue
		}
		idJSON, _ := json.Marshal(id)
		obj["id"] = idJSON // the row id is authoritative (rules reference it)
		if obj["items"] == nil || string(obj["items"]) == "null" {
			obj["items"] = json.RawMessage("[]")
		}
		merged, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		canon, err := contracts.CanonicalizeJSON(merged)
		if err != nil {
			b.log().Error("deploy: published playlist not canonicalizable, skipped", "playlist", id, "err", err)
			continue
		}
		out[id] = &playlist{raw: canon, items: parsed.Items}
	}
	return out, rows.Err()
}

func (b *Builder) loadPresentations(ctx context.Context, q querier, ids []string) ([]contracts.Presentation, error) {
	out := []contracts.Presentation{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT pr.id, v.doc FROM presentations pr
		JOIN presentation_versions v ON v.presentation_id = pr.id AND v.version = pr.published_version
		WHERE pr.archived_at IS NULL AND pr.published_version IS NOT NULL AND pr.id IN (`+placeholders(len(ids))+`)
		ORDER BY pr.id`, anys(ids)...)
	if err != nil {
		return nil, fmt.Errorf("deploy: presentations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, doc string
		if err := rows.Scan(&id, &doc); err != nil {
			return nil, err
		}
		var p contracts.Presentation
		if err := json.Unmarshal([]byte(doc), &p); err != nil {
			b.log().Error("deploy: published presentation unreadable, skipped", "presentation", id, "err", err)
			continue
		}
		p.ID = id
		out = append(out, p)
	}
	return out, rows.Err()
}

func loadProductImages(ctx context.Context, q querier) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT image_asset_id FROM products
		WHERE archived_at IS NULL AND image_asset_id IS NOT NULL AND image_asset_id <> '' ORDER BY image_asset_id`)
	if err != nil {
		return nil, fmt.Errorf("deploy: product images: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- assets

type assetInfo struct {
	id, kind string
	revision int64
	orig     string
	meta     assetMeta
	variants map[string]variantInfo
	usable   bool
}

type variantInfo struct {
	sha           string
	width, height int
}

type assetMeta struct {
	Width      float64 `json:"width"`
	Height     float64 `json:"height"`
	DurationMS float64 `json:"durationMs"`
}

type blobInfo struct {
	size int64
	mime string
}

// loadAssets returns the manifest asset entries: every variant except `thumb` for the
// full set, and for product images that are not otherwise used only `i512` (else
// `orig`). It also returns the product image ids that are shippable.
func (b *Builder) loadAssets(ctx context.Context, q querier, full, productImages []string) ([]contracts.ManifestAsset, []string, error) {
	assets := []contracts.ManifestAsset{}
	shipped := []string{}
	all := uniq(append(append([]string(nil), full...), productImages...))
	if len(all) == 0 {
		return assets, shipped, nil
	}
	infos := map[string]*assetInfo{}
	rows, err := q.QueryContext(ctx, `SELECT id, kind, revision, ready_revision, status, COALESCE(blob_sha256, ''), meta FROM media_assets
		WHERE trashed_at IS NULL AND id IN (`+placeholders(len(all))+`)`, anys(all)...)
	if err != nil {
		return nil, nil, fmt.Errorf("deploy: assets: %w", err)
	}
	var older []*assetInfo
	for rows.Next() {
		var a assetInfo
		var readyRev int64
		var status, meta string
		if err := rows.Scan(&a.id, &a.kind, &a.revision, &readyRev, &status, &a.orig, &meta); err != nil {
			rows.Close()
			return nil, nil, err
		}
		a.variants = map[string]variantInfo{}
		switch {
		case status == "ready" && a.orig != "":
			a.usable = true
			parseMeta(meta, &a.meta)
		case readyRev > 0:
			// The newest revision is not (or not yet) processed - a replacement, a reprocess
			// or a failure: keep shipping the newest revision that finished (ready_revision,
			// maintained by the media module) so screens never lose the asset in between.
			a.revision = readyRev
			older = append(older, &a)
		}
		infos[a.id] = &a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for _, a := range older {
		var meta string
		err := q.QueryRowContext(ctx, `SELECT blob_sha256, meta FROM media_revisions WHERE asset_id = ? AND revision = ?`, a.id, a.revision).
			Scan(&a.orig, &meta)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		parseMeta(meta, &a.meta)
		a.usable = a.orig != ""
	}

	ids := make([]string, 0, len(infos))
	for id := range infos {
		ids = append(ids, id)
	}
	if len(ids) > 0 {
		vrows, err := q.QueryContext(ctx, `SELECT asset_id, revision, variant, blob_sha256, width, height FROM media_variants
			WHERE variant <> 'thumb' AND asset_id IN (`+placeholders(len(ids))+`)`, anys(ids)...)
		if err != nil {
			return nil, nil, fmt.Errorf("deploy: variants: %w", err)
		}
		for vrows.Next() {
			var assetID, variant string
			var rev int64
			var v variantInfo
			if err := vrows.Scan(&assetID, &rev, &variant, &v.sha, &v.width, &v.height); err != nil {
				vrows.Close()
				return nil, nil, err
			}
			if a := infos[assetID]; a != nil && a.revision == rev {
				if variant == "orig" {
					// Very large images are stored downscaled as variant "orig" (media module):
					// players get that file instead of the raw upload.
					a.orig = v.sha
					if v.width > 0 {
						a.meta.Width, a.meta.Height = float64(v.width), float64(v.height)
					}
					continue
				}
				a.variants[variant] = v
			}
		}
		vrows.Close()
		if err := vrows.Err(); err != nil {
			return nil, nil, err
		}
	}

	var shas []string
	for _, a := range infos {
		if !a.usable {
			continue
		}
		shas = append(shas, a.orig)
		for _, v := range a.variants {
			shas = append(shas, v.sha)
		}
	}
	blobs, err := b.loadBlobs(ctx, q, uniq(shas))
	if err != nil {
		return nil, nil, err
	}

	entry := func(a *assetInfo, variant, sha string, w, h int) (contracts.ManifestAsset, bool) {
		bi, ok := blobs[sha]
		if !ok {
			return contracts.ManifestAsset{}, false
		}
		e := contracts.ManifestAsset{ID: a.id, Revision: a.revision, Kind: a.kind, SHA256: sha, Size: bi.size, Mime: bi.mime,
			Variant: variant, URL: BlobURL(sha), Width: w, Height: h}
		if (a.kind == "video" || a.kind == "audio") && variant != "poster" {
			e.DurationMS = int64(a.meta.DurationMS)
		}
		return e, true
	}

	fullSet := map[string]bool{}
	for _, id := range full {
		fullSet[id] = true
	}
	for _, id := range sortedKeys(infos) {
		a := infos[id]
		if !a.usable {
			continue
		}
		if fullSet[id] {
			if e, ok := entry(a, "orig", a.orig, int(a.meta.Width), int(a.meta.Height)); ok {
				assets = append(assets, e)
			}
			for _, vname := range sortedKeys(a.variants) {
				v := a.variants[vname]
				if e, ok := entry(a, vname, v.sha, v.width, v.height); ok {
					assets = append(assets, e)
				}
			}
		} else if a.kind == "image" {
			var e contracts.ManifestAsset
			ok := false
			if v, has := a.variants["i512"]; has {
				e, ok = entry(a, "i512", v.sha, v.width, v.height)
			}
			if !ok {
				e, ok = entry(a, "orig", a.orig, int(a.meta.Width), int(a.meta.Height))
			}
			if ok {
				assets = append(assets, e)
			}
		}
	}
	shippedSet := map[string]bool{}
	for _, e := range assets {
		shippedSet[e.ID] = true
	}
	for _, id := range productImages {
		if shippedSet[id] && infos[id] != nil && infos[id].kind == "image" {
			shipped = append(shipped, id)
		}
	}
	sort.Strings(shipped)
	return assets, shipped, nil
}

func parseMeta(raw string, m *assetMeta) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	if err := json.Unmarshal([]byte(raw), m); err != nil {
		slog.Debug("deploy: asset meta unreadable", "err", err)
	}
}

// loadBlobs returns size and mime of the blobs whose file exists. A blob row without
// file is logged and left out: announcing it would make every device fail its sync.
func (b *Builder) loadBlobs(ctx context.Context, q querier, shas []string) (map[string]blobInfo, error) {
	out := map[string]blobInfo{}
	if len(shas) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT sha256, size, mime FROM blobs WHERE sha256 IN (`+placeholders(len(shas))+`)`, anys(shas)...)
	if err != nil {
		return nil, fmt.Errorf("deploy: blobs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sha string
		var bi blobInfo
		if err := rows.Scan(&sha, &bi.size, &bi.mime); err != nil {
			return nil, err
		}
		if _, err := os.Stat(BlobPath(b.DataDir, sha)); err != nil {
			b.log().Error("deploy: blob file missing, left out of the manifest", "sha256", sha, "err", err)
			continue
		}
		out[sha] = bi
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, s := range shas {
		if _, ok := out[s]; !ok {
			b.log().Debug("deploy: blob not available", "sha256", s)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- fonts

func (b *Builder) loadFonts(ctx context.Context, q querier, families []string) ([]contracts.ManifestFont, error) {
	out := []contracts.ManifestFont{}
	if len(families) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT family, style, weight_min, weight_max, blob_sha256, format FROM font_faces
		WHERE family IN (`+placeholders(len(families))+`)`, anys(families)...)
	if err != nil {
		return nil, fmt.Errorf("deploy: fonts: %w", err)
	}
	byFamily := map[string][]contracts.ManifestFontFile{}
	var shas []string
	for rows.Next() {
		var family string
		var f contracts.ManifestFontFile
		if err := rows.Scan(&family, &f.Style, &f.Weight[0], &f.Weight[1], &f.SHA256, &f.Format); err != nil {
			rows.Close()
			return nil, err
		}
		f.URL = BlobURL(f.SHA256)
		byFamily[family] = append(byFamily[family], f)
		shas = append(shas, f.SHA256)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	blobs, err := b.loadBlobs(ctx, q, uniq(shas))
	if err != nil {
		return nil, err
	}
	for _, family := range sortedKeys(byFamily) {
		var files []contracts.ManifestFontFile
		for _, f := range byFamily[family] {
			bi, ok := blobs[f.SHA256]
			if !ok {
				continue
			}
			f.Size = bi.size
			files = append(files, f)
		}
		if len(files) == 0 {
			continue
		}
		sort.Slice(files, func(i, j int) bool {
			a, c := files[i], files[j]
			if a.Style != c.Style {
				return a.Style < c.Style
			}
			if a.Weight != c.Weight {
				return a.Weight[0] < c.Weight[0] || a.Weight[0] == c.Weight[0] && a.Weight[1] < c.Weight[1]
			}
			return a.SHA256 < c.SHA256
		})
		out = append(out, contracts.ManifestFont{Family: family, Files: files})
	}
	return out, nil
}

// ---------------------------------------------------------------- storing

// CurrentTarget returns the zone's current target: its non-superseded deployment, or
// cleared when there is none.
func CurrentTarget(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, zoneID string) (Target, error) {
	if zoneID == "" {
		return Cleared(ReasonNoZone), nil
	}
	var t Target
	err := q.QueryRowContext(ctx, `SELECT id, hash FROM deployments WHERE zone_id = ? AND superseded_at IS NULL
		ORDER BY built_at DESC, id DESC LIMIT 1`, zoneID).Scan(&t.ID, &t.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return Cleared(ReasonNoContent), nil
	}
	if err != nil {
		return Target{}, err
	}
	t.Kind = KindDeployment
	return t, nil
}

// Targets returns the current deployment of every zone that has one.
func Targets(ctx context.Context, q interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}) (map[string]Target, error) {
	rows, err := q.QueryContext(ctx, `SELECT zone_id, id, hash FROM deployments WHERE superseded_at IS NULL ORDER BY zone_id, built_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Target{}
	for rows.Next() {
		var zone string
		t := Target{Kind: KindDeployment}
		if err := rows.Scan(&zone, &t.ID, &t.Hash); err != nil {
			return nil, err
		}
		out[zone] = t // last (newest) wins
	}
	return out, rows.Err()
}

// Apply stores a build result. It inserts a new deployment (and its blob list) only
// when the canonical hash differs from the zone's latest deployment, re-activates the
// latest one when the content returned to it, or clears the zone for an empty manifest.
// It returns the zone's current target and whether it changed.
func (b *Builder) Apply(ctx context.Context, zoneID string, m *contracts.Manifest) (Target, bool, error) {
	var target Target
	changed := false
	now := time.Now().UTC().Format(TimeFormat)
	err := b.DB.Tx(ctx, func(tx *sql.Tx) error {
		cur, err := CurrentTarget(ctx, tx, zoneID)
		if err != nil {
			return err
		}
		if len(m.Playlists) == 0 {
			target = Cleared(ReasonNoContent)
			if cur.Kind != KindDeployment {
				return nil
			}
			if _, err := tx.ExecContext(ctx, `UPDATE deployments SET superseded_at = ? WHERE zone_id = ? AND superseded_at IS NULL`, now, zoneID); err != nil {
				return err
			}
			changed = true
			return core.Audit(ctx, tx, nil, "", core.AuditEntry{Action: "deployment.cleared", ObjectType: "zone",
				ObjectID: zoneID, ZoneID: zoneID, Old: cur, New: target})
		}
		hash, err := m.CanonicalHash()
		if err != nil {
			return err
		}
		var latestID, latestHash string
		var superseded sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT id, hash, superseded_at FROM deployments WHERE zone_id = ?
			ORDER BY built_at DESC, id DESC LIMIT 1`, zoneID).Scan(&latestID, &latestHash, &superseded)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && latestHash == hash {
			target = Target{Kind: KindDeployment, ID: latestID, Hash: hash}
			if !superseded.Valid && cur.ID == latestID {
				return nil
			}
			if _, err := tx.ExecContext(ctx, `UPDATE deployments SET superseded_at = ? WHERE zone_id = ? AND superseded_at IS NULL AND id <> ?`, now, zoneID, latestID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE deployments SET superseded_at = NULL WHERE id = ?`, latestID); err != nil {
				return err
			}
			changed = true
			return core.Audit(ctx, tx, nil, "", core.AuditEntry{Action: "deployment.reactivated", ObjectType: "deployment",
				ObjectID: latestID, ZoneID: zoneID, Old: cur, New: target})
		}
		id := ids.New()
		stored := *m
		stored.ID, stored.Hash, stored.BuiltAt = id, hash, now
		body, err := json.Marshal(&stored)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id, zone_id, hash, manifest, built_at) VALUES (?, ?, ?, ?, ?)`,
			id, zoneID, hash, string(body), now); err != nil {
			return err
		}
		for _, sha := range stored.BlobHashes() {
			if _, err := tx.ExecContext(ctx, `INSERT INTO deployment_blobs(deployment_id, sha256) VALUES (?, ?)`, id, sha); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE deployments SET superseded_at = ? WHERE zone_id = ? AND id <> ? AND superseded_at IS NULL`, now, zoneID, id); err != nil {
			return err
		}
		target = Target{Kind: KindDeployment, ID: id, Hash: hash}
		changed = true
		return core.Audit(ctx, tx, nil, "", core.AuditEntry{Action: "deployment.created", ObjectType: "deployment",
			ObjectID: id, ZoneID: zoneID, Old: cur, New: map[string]any{"hash": hash, "playlists": len(stored.Playlists),
				"presentations": len(stored.Presentations), "assets": len(stored.Assets), "fonts": len(stored.Fonts)}})
	})
	if err != nil {
		return Target{}, false, err
	}
	return target, changed, nil
}

// Prune keeps at most keep deployments per existing zone and removes deployments of
// deleted zones. A deployment that is the current target or that any device reports as
// active or previous is never deleted. It returns the number of deleted deployments.
func (b *Builder) Prune(ctx context.Context, keep int) (int, error) {
	deleted := 0
	err := b.DB.Tx(ctx, func(tx *sql.Tx) error {
		protected := map[string]bool{}
		rows, err := tx.QueryContext(ctx, `SELECT COALESCE(active_deployment_id, ''), COALESCE(previous_deployment_id, '') FROM devices`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var a, p string
			if err := rows.Scan(&a, &p); err != nil {
				rows.Close()
				return err
			}
			protected[a], protected[p] = true, true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		zones := map[string]bool{}
		zrows, err := tx.QueryContext(ctx, `SELECT id FROM zones`)
		if err != nil {
			return err
		}
		for zrows.Next() {
			var z string
			if err := zrows.Scan(&z); err != nil {
				zrows.Close()
				return err
			}
			zones[z] = true
		}
		zrows.Close()
		if err := zrows.Err(); err != nil {
			return err
		}
		drows, err := tx.QueryContext(ctx, `SELECT id, zone_id, superseded_at IS NULL FROM deployments ORDER BY zone_id, built_at DESC, id DESC`)
		if err != nil {
			return err
		}
		var victims []string
		count := map[string]int{}
		for drows.Next() {
			var id, zone string
			var current bool
			if err := drows.Scan(&id, &zone, &current); err != nil {
				drows.Close()
				return err
			}
			count[zone]++
			if protected[id] {
				continue
			}
			if !zones[zone] || (!current && count[zone] > keep) {
				victims = append(victims, id)
			}
		}
		drows.Close()
		if err := drows.Err(); err != nil {
			return err
		}
		if len(victims) == 0 {
			return nil
		}
		for _, id := range victims {
			if _, err := tx.ExecContext(ctx, `DELETE FROM deployments WHERE id = ?`, id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM device_deployments WHERE deployment_id NOT IN (SELECT id FROM deployments)`); err != nil {
			return err
		}
		deleted = len(victims)
		return core.Audit(ctx, tx, nil, "", core.AuditEntry{Action: "deployment.pruned", ObjectType: "deployment",
			Old: map[string]any{"ids": victims}})
	})
	return deleted, err
}

// ---------------------------------------------------------------- helpers

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func anys(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func uniq(s []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(s))
	for _, v := range s {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
