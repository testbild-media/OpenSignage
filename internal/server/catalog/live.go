package catalog

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

// statusResult is the answer of a live status change (of a product, or of one of its
// variants: VariantID set).
type statusResult struct {
	ProductID string  `json:"productId"`
	VariantID string  `json:"variantId,omitempty"`
	ZoneID    string  `json:"zoneId,omitempty"`
	Status    string  `json:"status"`   // global status
	Override  *string `json:"override"` // zone override (null = none)
	Effective string  `json:"effective"`
	Changed   bool    `json:"changed"`
}

type statusReq struct {
	Status modkit.Opt[string] `json:"status"`
	ZoneID string             `json:"zoneId"`
}

// setProductStatus is the operator hot path: one small transaction, audit with old/new, event
// after commit. Without zoneId it sets the global status (operator and above); with zoneId it
// sets or clears (status null) the override of that zone (role user: assigned zones only).
// errNotCarriedAdminOnly: UNAVAILABLE means "the product is not carried" – only
// administrators set or lift it, globally or per zone. Operators and users switch between
// AVAILABLE and SOLD_OUT only.
var errNotCarriedAdminOnly = httpx.E(http.StatusForbidden, "status_admin_only",
	"only administrators decide whether a product is carried (not available)")

// statusTarget describes what a status change applies to: a product or a variant.
type statusTarget struct {
	kind string // "product" | "variant" (audit actions <kind>.status, <kind>.zone_status)
	// lookup returns name, global status, archived (of the product) and the product id.
	lookup        string
	update        func(ctx context.Context, tx *sql.Tx, status, id string) error // sets the global status
	overrideTable string
	overrideCol   string
}

var (
	productStatus = statusTarget{kind: "product",
		lookup: `SELECT name, status, archived_at IS NOT NULL, id FROM products WHERE id = ?`,
		update: func(ctx context.Context, tx *sql.Tx, status, id string) error {
			_, err := tx.ExecContext(ctx, `UPDATE products SET status = ?, updated_at = ? WHERE id = ?`, status, db.Now(), id)
			return err
		},
		overrideTable: "zone_product_status", overrideCol: "product_id"}
	variantStatus = statusTarget{kind: "variant",
		lookup: `SELECT p.name || ' – ' || v.name, v.status, p.archived_at IS NOT NULL, p.id
			FROM product_variants v JOIN products p ON p.id = v.product_id WHERE v.id = ?`,
		update: func(ctx context.Context, tx *sql.Tx, status, id string) error {
			if _, err := tx.ExecContext(ctx, `UPDATE product_variants SET status = ? WHERE id = ?`, status, id); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE products SET updated_at = ? WHERE id = (SELECT product_id FROM product_variants WHERE id = ?)`, db.Now(), id)
			return err
		},
		overrideTable: "zone_variant_status", overrideCol: "variant_id"}
)

func (m *Module) setProductStatus(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	return m.setStatus(w, r, p, productStatus)
}

// setVariantStatus: the same for one variant of a product (a single sold-out variant is
// struck through on the screens).
func (m *Module) setVariantStatus(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	return m.setStatus(w, r, p, variantStatus)
}

func (m *Module) setStatus(w http.ResponseWriter, r *http.Request, p *core.Principal, target statusTarget) error {
	id := r.PathValue("id")
	var req statusReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	zone := req.ZoneID
	if zone == "" && !p.CanEditProducts() { // the global status applies to every zone
		return httpx.ErrForbidden
	}
	if zone != "" && !p.CanZone(zone) {
		return httpx.ErrForbidden
	}
	if !req.Status.Set {
		return httpx.Invalid("status", "required")
	}
	if req.Status.Null && zone == "" {
		return httpx.Invalid("status", "required")
	}
	if req.Status.Has() && !contracts.ValidStatus(req.Status.Val) {
		return httpx.Invalid("status", "invalid_value")
	}

	var res statusResult
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var name, global, productID string
		var archived int
		err := tx.QueryRowContext(r.Context(), target.lookup, id).Scan(&name, &global, &archived, &productID)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if archived == 1 {
			return errProductArchived
		}
		res = statusResult{ProductID: productID, ZoneID: zone, Status: global, Effective: global}
		if target.kind == "variant" {
			res.VariantID = id
		}
		admin := p.Is(core.RoleAdmin)
		if zone == "" {
			if global == req.Status.Val {
				return nil
			}
			if !admin && (global == contracts.StatusUnavailable || req.Status.Val == contracts.StatusUnavailable) {
				return errNotCarriedAdminOnly
			}
			if err := target.update(r.Context(), tx, req.Status.Val, id); err != nil {
				return err
			}
			res.Status, res.Effective, res.Changed = req.Status.Val, req.Status.Val, true
			return audit(r.Context(), tx, r, p, core.AuditEntry{Action: target.kind + ".status", ObjectType: target.kind, ObjectID: id,
				ObjectName: name, Old: map[string]any{"status": global}, New: map[string]any{"status": req.Status.Val}})
		}

		var one int
		if err := tx.QueryRowContext(r.Context(), `SELECT 1 FROM zones WHERE id = ?`, zone).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return httpx.Invalid("zoneId", "unknown_zone")
		} else if err != nil {
			return err
		}
		var cur sql.NullString
		if err := tx.QueryRowContext(r.Context(), `SELECT status FROM `+target.overrideTable+` WHERE zone_id = ? AND `+target.overrideCol+` = ?`, zone, id).Scan(&cur); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		oldEff := global
		if cur.Valid {
			oldEff = cur.String
			s := cur.String
			res.Override = &s
		}
		res.Effective = oldEff
		var want *string
		if req.Status.Has() {
			want = &req.Status.Val
		}
		if (want == nil && !cur.Valid) || (want != nil && cur.Valid && *want == cur.String) {
			return nil
		}
		if !admin {
			newEff := global
			if want != nil {
				newEff = *want
			}
			if oldEff == contracts.StatusUnavailable || newEff == contracts.StatusUnavailable {
				return errNotCarriedAdminOnly // neither hide nor bring back a product or variant here
			}
		}
		if want == nil {
			if _, err := tx.ExecContext(r.Context(), `DELETE FROM `+target.overrideTable+` WHERE zone_id = ? AND `+target.overrideCol+` = ?`, zone, id); err != nil {
				return err
			}
			res.Override, res.Effective = nil, global
		} else {
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO `+target.overrideTable+`(zone_id, `+target.overrideCol+`, status) VALUES (?, ?, ?)
				ON CONFLICT(zone_id, `+target.overrideCol+`) DO UPDATE SET status = excluded.status`, zone, id, *want); err != nil {
				return err
			}
			res.Override, res.Effective = want, *want
		}
		res.Changed = true
		var oldOverride any
		if cur.Valid {
			oldOverride = cur.String
		}
		var newOverride any
		if want != nil {
			newOverride = *want
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: target.kind + ".zone_status", ObjectType: target.kind, ObjectID: id,
			ObjectName: name, ZoneID: zone,
			Old: map[string]any{"override": oldOverride, "effective": oldEff},
			New: map[string]any{"override": newOverride, "effective": res.Effective}})
	})
	if err != nil {
		return err
	}
	if res.Changed {
		m.emit(core.TopicLive, target.kind+".status", res.ProductID, zone)
	}
	return httpx.OK(w, res)
}

type zoneStateReq struct {
	State string `json:"state"`
}

type zoneStateResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Changed bool   `json:"changed"`
}

func (m *Module) setZoneState(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	if !p.CanZone(id) {
		return httpx.ErrForbidden
	}
	var req zoneStateReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.State != contracts.ZoneOpen && req.State != contracts.ZoneClosed {
		return httpx.Invalid("state", "invalid_value")
	}
	var res zoneStateResult
	closedPlaylist := false
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var old string
		var cp sql.NullString
		err := tx.QueryRowContext(r.Context(), `SELECT name, state, COALESCE(closed_playlist_id,
			(SELECT g.closed_playlist_id FROM display_groups g WHERE g.zone_id = zones.id AND g.closed_playlist_id IS NOT NULL LIMIT 1))
			FROM zones WHERE id = ?`, id).Scan(&res.Name, &old, &cp)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		res.ID, res.State = id, old
		closedPlaylist = cp.Valid
		if old == req.State {
			return nil
		}
		// state_changed_at anchors the closed content of synchronous screens.
		now := db.Now()
		if _, err := tx.ExecContext(r.Context(), `UPDATE zones SET state = ?, state_changed_at = ?, updated_at = ? WHERE id = ?`,
			req.State, now, now, id); err != nil {
			return err
		}
		res.State, res.Changed = req.State, true
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "zone.state", ObjectType: "zone", ObjectID: id,
			ObjectName: res.Name, ZoneID: id, Old: map[string]any{"state": old}, New: map[string]any{"state": req.State}})
	})
	if err != nil {
		return err
	}
	if res.Changed {
		m.emit(core.TopicLive, "zone.state", id, id)
		if closedPlaylist {
			// The closed playlist takes over (or releases) the screen: schedule consumers re-evaluate.
			m.emit(core.TopicZones, "zone.state", id, id)
		}
	}
	return httpx.OK(w, res)
}

type liveStatusEntry struct {
	ID       string  `json:"id"`
	Status   string  `json:"status"`   // effective: zone override, else global
	Override *string `json:"override"` // zone override (null = none)
}

type liveZone struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	State             string            `json:"state"`
	HasClosedPlaylist bool              `json:"hasClosedPlaylist"`
	Products          []liveStatusEntry `json:"products"`
	// Variants: the status of every variant in the zone (id = variant id).
	Variants []liveStatusEntry `json:"variants"`
}

type liveVariant struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // global status
}

type liveProduct struct {
	ID                     string              `json:"id"`
	Name                   string              `json:"name"`
	Description            string              `json:"description"`
	CategoryID             string              `json:"categoryId"`
	Type                   string              `json:"type"`
	Quantity               *contracts.Quantity `json:"quantity,omitempty"`
	Price                  contracts.Money     `json:"price"`
	Status                 string              `json:"status"` // global status
	ImageAssetID           string              `json:"imageAssetId,omitempty"`
	ShowImageInsteadOfName bool                `json:"showImageInsteadOfName"`
	Sort                   int                 `json:"sort"`
	Variants               []liveVariant       `json:"variants"`
}

// liveZones feeds the operator UI: the zones the user may operate (role user: assigned
// zones, otherwise all) with state and the effective status of every product. Tickers come
// from time schedule rules and presentations, not from the operator UI.
func (m *Module) liveZones(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	ctx := r.Context()
	q := m.app.DB.R
	cats, err := loadCategories(ctx, q)
	if err != nil {
		return err
	}
	if cats == nil {
		cats = []Category{}
	}
	prods, err := loadProducts(ctx, q, `WHERE archived_at IS NULL`)
	if err != nil {
		return err
	}
	sortByCategory(prods, cats)
	master := make([]liveProduct, 0, len(prods))
	for _, pr := range prods {
		lp := liveProduct{ID: pr.ID, Name: pr.Name, Description: pr.Description, CategoryID: pr.CategoryID,
			Type: pr.Type, Quantity: pr.Quantity, Price: pr.Price, Status: pr.Status, ImageAssetID: pr.ImageAssetID,
			ShowImageInsteadOfName: pr.ShowImageInsteadOfName, Sort: pr.Sort, Variants: make([]liveVariant, len(pr.Variants))}
		for i, v := range pr.Variants {
			lp.Variants[i] = liveVariant{ID: v.ID, Name: v.Name, Status: v.Status}
		}
		master = append(master, lp)
	}
	rows, err := q.QueryContext(ctx, `SELECT id, name, state, closed_playlist_id IS NOT NULL
			OR EXISTS (SELECT 1 FROM display_groups g WHERE g.zone_id = zones.id AND g.closed_playlist_id IS NOT NULL)
		FROM zones ORDER BY sort, name COLLATE NOCASE, id`)
	if err != nil {
		return err
	}
	zones := []liveZone{}
	for rows.Next() {
		var z liveZone
		var cp int
		if err := rows.Scan(&z.ID, &z.Name, &z.State, &cp); err != nil {
			rows.Close()
			return err
		}
		if !p.CanZone(z.ID) {
			continue
		}
		z.HasClosedPlaylist = cp == 1
		z.Products = make([]liveStatusEntry, 0, len(prods))
		z.Variants = []liveStatusEntry{}
		entry := func(id, global string, zoneStatus map[string]string) liveStatusEntry {
			e := liveStatusEntry{ID: id, Status: global}
			if ov, ok := zoneStatus[z.ID]; ok {
				o := ov
				e.Status, e.Override = ov, &o
			}
			return e
		}
		for _, pr := range prods {
			z.Products = append(z.Products, entry(pr.ID, pr.Status, pr.ZoneStatus))
			for _, v := range pr.Variants {
				z.Variants = append(z.Variants, entry(v.ID, v.Status, v.ZoneStatus))
			}
		}
		zones = append(zones, z)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"zones": zones, "categories": cats, "products": master,
		"serverTime": time.Now().UTC().Format(time.RFC3339)})
}
