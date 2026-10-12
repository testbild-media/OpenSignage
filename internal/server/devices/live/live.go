// Package live computes the full live state of zones (ARCHITECTURE §5.6): zone state,
// ticker, the category tree and every non-archived product with its effective
// three-valued status (zone override, else global). States are always complete, never
// deltas, so a device that missed messages is correct after the next one.
package live

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"opensignage/internal/contracts"
)

// Product statuses (kept distinct end to end, never reduced to available yes/no).
const (
	StatusAvailable   = "AVAILABLE"
	StatusUnavailable = "UNAVAILABLE"
	StatusSoldOut     = "SOLD_OUT"
)

// ValidStatus reports whether s is one of the three product statuses.
func ValidStatus(s string) bool {
	return s == StatusAvailable || s == StatusUnavailable || s == StatusSoldOut
}

// Querier is satisfied by *sql.DB and *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Empty returns the live state for a device without zone: nothing to show, ticker off.
func Empty(now time.Time) *contracts.LiveState {
	return &contracts.LiveState{
		Zone:       contracts.LiveZone{State: "OPEN"},
		Ticker:     contracts.LiveTicker{Template: contracts.DefaultTickerTemplate()},
		Categories: []contracts.LiveCategory{},
		Products:   []contracts.LiveProduct{},
		ServerTime: now.UTC().Format(time.RFC3339),
	}
}

// Compute returns the live state of the given zones (nil = all zones), keyed by zone id.
// Unknown zone ids are absent from the result. All data is read in one transaction so
// the snapshot is consistent; queries are batched (no per-zone or per-product queries).
func Compute(ctx context.Context, d *sql.DB, zoneIDs []string, now time.Time) (map[string]*contracts.LiveState, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	return ComputeWith(ctx, tx, zoneIDs, now)
}

type zoneRow struct {
	id, name, state, templateStyle, stateChangedAt string
}

type productRow struct {
	p      contracts.LiveProduct
	global string
}

// ComputeWith is Compute on an existing query handle.
func ComputeWith(ctx context.Context, q Querier, zoneIDs []string, now time.Time) (map[string]*contracts.LiveState, error) {
	if zoneIDs != nil && len(zoneIDs) == 0 {
		return map[string]*contracts.LiveState{}, nil
	}
	where, args := "", []any{}
	if zoneIDs != nil {
		where = " WHERE z.id IN (" + placeholders(len(zoneIDs)) + ")"
		for _, z := range zoneIDs {
			args = append(args, z)
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT z.id, z.name, z.state, COALESCE(t.style, ''), COALESCE(z.state_changed_at, '')
		FROM zones z LEFT JOIN ticker_templates t ON t.id = z.ticker_template_id`+where+` ORDER BY z.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("live: zones: %w", err)
	}
	var zones []zoneRow
	for rows.Next() {
		var z zoneRow
		if err := rows.Scan(&z.id, &z.name, &z.state, &z.templateStyle, &z.stateChangedAt); err != nil {
			rows.Close()
			return nil, err
		}
		zones = append(zones, z)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]*contracts.LiveState, len(zones))
	if len(zones) == 0 {
		return out, nil
	}

	cats, err := loadCategories(ctx, q)
	if err != nil {
		return nil, err
	}
	prods, err := loadProducts(ctx, q)
	if err != nil {
		return nil, err
	}
	overrides, err := loadOverrides(ctx, q, zones)
	if err != nil {
		return nil, err
	}

	serverTime := now.UTC().Format(time.RFC3339)
	for _, z := range zones {
		st := &contracts.LiveState{
			Zone: contracts.LiveZone{ID: z.id, Name: z.name, State: z.state, StateChangedAt: z.stateChangedAt},
			// Tickers come from time schedule rules (manifest); the empty zone ticker
			// stays for older players (they read it).
			Ticker:     contracts.LiveTicker{Template: parseTemplate(z.id, z.templateStyle)},
			Categories: cats,
			Products:   make([]contracts.LiveProduct, len(prods)),
			ServerTime: serverTime,
		}
		zo := overrides[z.id]
		for i, pr := range prods {
			p := pr.p
			p.Status = pr.global
			if s, ok := zo[p.ID]; ok {
				p.Status = s
			}
			st.Products[i] = p
		}
		out[z.id] = st
	}
	return out, nil
}

func loadCategories(ctx context.Context, q Querier) ([]contracts.LiveCategory, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, COALESCE(parent_id, ''), name, sort FROM categories`)
	if err != nil {
		return nil, fmt.Errorf("live: categories: %w", err)
	}
	defer rows.Close()
	out := []contracts.LiveCategory{}
	for rows.Next() {
		var c contracts.LiveCategory
		if err := rows.Scan(&c.ID, &c.ParentID, &c.Name, &c.Sort); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Sort != b.Sort {
			return a.Sort < b.Sort
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	// Tree order: every top-level category followed by its subcategories.
	children := map[string][]contracts.LiveCategory{}
	ids := map[string]bool{}
	for _, c := range out {
		ids[c.ID] = true
	}
	var top, orphans []contracts.LiveCategory
	for _, c := range out {
		switch {
		case c.ParentID == "":
			top = append(top, c)
		case ids[c.ParentID]:
			children[c.ParentID] = append(children[c.ParentID], c)
		default:
			orphans = append(orphans, c)
		}
	}
	ordered := make([]contracts.LiveCategory, 0, len(out))
	done := map[string]bool{}
	var emit func(c contracts.LiveCategory)
	emit = func(c contracts.LiveCategory) {
		if done[c.ID] {
			return
		}
		done[c.ID] = true
		ordered = append(ordered, c)
		for _, ch := range children[c.ID] {
			emit(ch)
		}
	}
	for _, c := range top {
		emit(c)
	}
	for _, c := range append(orphans, out...) { // orphans and cycles: never drop a category
		emit(c)
	}
	return ordered, nil
}

func loadProducts(ctx context.Context, q Querier) ([]productRow, error) {
	// Product images are only referenced while the asset is usable (not trashed); the
	// deployment builder ships the same set of images.
	rows, err := q.QueryContext(ctx, `SELECT p.id, p.name, p.description, p.category_id, p.type, p.quantity_value,
			p.quantity_unit, p.price_amount, p.currency, p.status, COALESCE(a.id, ''), p.image_instead_of_name, p.image_scale, p.sort
		FROM products p
		LEFT JOIN media_assets a ON a.id = p.image_asset_id AND a.trashed_at IS NULL AND a.kind = 'image'
		WHERE p.archived_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("live: products: %w", err)
	}
	defer rows.Close()
	var out []productRow
	for rows.Next() {
		var r productRow
		var qv, qu string
		var imageInstead int
		if err := rows.Scan(&r.p.ID, &r.p.Name, &r.p.Description, &r.p.CategoryID, &r.p.Type, &qv, &qu,
			&r.p.Price.Amount, &r.p.Price.Currency, &r.global, &r.p.ImageAssetID, &imageInstead, &r.p.ImageScale, &r.p.Sort); err != nil {
			return nil, err
		}
		if strings.TrimSpace(qv) != "" && strings.TrimSpace(qu) != "" {
			r.p.Quantity = &contracts.Quantity{Value: qv, Unit: qu}
		}
		r.p.ShowImageInsteadOfName = imageInstead != 0
		if !ValidStatus(r.global) {
			slog.Error("live: product with invalid status, treating as UNAVAILABLE", "product", r.p.ID, "status", r.global)
			r.global = StatusUnavailable
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].p, out[j].p
		if a.Sort != b.Sort {
			return a.Sort < b.Sort
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	return out, nil
}

func loadOverrides(ctx context.Context, q Querier, zones []zoneRow) (map[string]map[string]string, error) {
	args := make([]any, len(zones))
	for i, z := range zones {
		args[i] = z.id
	}
	rows, err := q.QueryContext(ctx, `SELECT zone_id, product_id, status FROM zone_product_status WHERE zone_id IN (`+placeholders(len(zones))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("live: zone overrides: %w", err)
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var z, p, s string
		if err := rows.Scan(&z, &p, &s); err != nil {
			return nil, err
		}
		if !ValidStatus(s) {
			slog.Error("live: invalid zone override ignored", "zone", z, "product", p, "status", s)
			continue
		}
		if out[z] == nil {
			out[z] = map[string]string{}
		}
		out[z][p] = s
	}
	return out, rows.Err()
}

// parseTemplate reads a stored ticker template; missing or invalid values fall back to
// the default template so the ticker always renders.
func parseTemplate(zoneID, style string) contracts.TickerTemplate {
	def := contracts.DefaultTickerTemplate()
	if strings.TrimSpace(style) == "" {
		return def
	}
	t := def
	if err := json.Unmarshal([]byte(style), &t); err != nil {
		slog.Error("live: ticker template unreadable, using default", "zone", zoneID, "err", err)
		return def
	}
	if err := t.Validate(nil); err != nil {
		slog.Error("live: ticker template invalid, using default", "zone", zoneID, "err", err)
		return def
	}
	return t
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}
