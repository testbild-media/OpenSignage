package catalog

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"opensignage/internal/common/ids"
	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

const (
	maxDescriptionRunes = 2000
	// A product has at most this many variants with names of at most maxVariantRunes.
	maxVariants     = 50
	maxVariantRunes = 100
)

// Product is the API representation of a product with its global status and the per-zone
// overrides (zoneId → status). The three states are never reduced.
type Product struct {
	ID                     string              `json:"id"`
	Name                   string              `json:"name"`
	Description            string              `json:"description"`
	CategoryID             string              `json:"categoryId"`
	Type                   string              `json:"type"`
	Quantity               *contracts.Quantity `json:"quantity,omitempty"`
	Price                  contracts.Money     `json:"price"`
	Status                 string              `json:"status"`
	ZoneStatus             map[string]string   `json:"zoneStatus"`
	ImageAssetID           string              `json:"imageAssetId,omitempty"`
	ShowImageInsteadOfName bool                `json:"showImageInsteadOfName"`
	// ImageScale: size of the image instead of the name in percent of the automatic size.
	ImageScale int `json:"imageScale"`
	// Variants share the price and are shown like the description; each has its own status.
	Variants  []Variant `json:"variants"`
	Sort      int       `json:"sort"`
	Archived  bool      `json:"archived"`
	Revision  int64     `json:"revision"`
	CreatedAt string    `json:"createdAt"`
	UpdatedAt string    `json:"updatedAt"`
}

// Variant is one variant of a product ("Orange") with its global status and the per-zone
// overrides (zoneId → status), like a product.
type Variant struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Status     string            `json:"status"`
	ZoneStatus map[string]string `json:"zoneStatus"`
}

const productCols = `id, category_id, name, description, type, quantity_value, quantity_unit, price_amount, currency,
	status, COALESCE(image_asset_id, ''), image_instead_of_name, image_scale, sort, archived_at IS NOT NULL, created_at, updated_at, revision`

func scanProduct(sc interface{ Scan(...any) error }) (Product, error) {
	var p Product
	var qv, qu string
	var show, archived int
	err := sc.Scan(&p.ID, &p.CategoryID, &p.Name, &p.Description, &p.Type, &qv, &qu, &p.Price.Amount, &p.Price.Currency,
		&p.Status, &p.ImageAssetID, &show, &p.ImageScale, &p.Sort, &archived, &p.CreatedAt, &p.UpdatedAt, &p.Revision)
	if err != nil {
		return p, err
	}
	if qv != "" {
		p.Quantity = &contracts.Quantity{Value: qv, Unit: qu}
	}
	p.ShowImageInsteadOfName = show == 1
	p.Archived = archived == 1
	p.ZoneStatus = map[string]string{}
	p.Variants = []Variant{}
	return p, nil
}

// loadProducts reads products matching the WHERE clause (including the keyword) ordered by
// sort, name; zone overrides and the variants (with their overrides) are attached.
func loadProducts(ctx context.Context, q modkit.Queryer, where string, args ...any) ([]Product, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+productCols+` FROM products `+where+` ORDER BY sort, name COLLATE NOCASE, id`, args...)
	if err != nil {
		return nil, err
	}
	var out []Product
	idx := map[string]int{}
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		idx[p.ID] = len(out)
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	orows, err := q.QueryContext(ctx, `SELECT product_id, zone_id, status FROM zone_product_status`)
	if err != nil {
		return nil, err
	}
	for orows.Next() {
		var pid, zid, st string
		if err := orows.Scan(&pid, &zid, &st); err != nil {
			orows.Close()
			return nil, err
		}
		if i, ok := idx[pid]; ok {
			out[i].ZoneStatus[zid] = st
		}
	}
	orows.Close()
	if err := orows.Err(); err != nil {
		return nil, err
	}
	return out, loadVariants(ctx, q, out, idx, where, args)
}

// loadVariants attaches the variants of the products (in their order) with their zone
// overrides.
func loadVariants(ctx context.Context, q modkit.Queryer, ps []Product, idx map[string]int, where string, args []any) error {
	rows, err := q.QueryContext(ctx, `SELECT id, product_id, name, status FROM product_variants
		WHERE product_id IN (SELECT id FROM products `+where+`) ORDER BY product_id, sort, id`, args...)
	if err != nil {
		return err
	}
	at := map[string][2]int{} // variant id -> product index, variant index
	for rows.Next() {
		var v Variant
		var pid string
		if err := rows.Scan(&v.ID, &pid, &v.Name, &v.Status); err != nil {
			rows.Close()
			return err
		}
		if i, ok := idx[pid]; ok {
			v.ZoneStatus = map[string]string{}
			at[v.ID] = [2]int{i, len(ps[i].Variants)}
			ps[i].Variants = append(ps[i].Variants, v)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(at) == 0 {
		return nil
	}
	zrows, err := q.QueryContext(ctx, `SELECT zv.variant_id, zv.zone_id, zv.status FROM zone_variant_status zv
		JOIN product_variants v ON v.id = zv.variant_id WHERE v.product_id IN (SELECT id FROM products `+where+`)`, args...)
	if err != nil {
		return err
	}
	defer zrows.Close()
	for zrows.Next() {
		var vid, zid, st string
		if err := zrows.Scan(&vid, &zid, &st); err != nil {
			return err
		}
		if pos, ok := at[vid]; ok {
			ps[pos[0]].Variants[pos[1]].ZoneStatus[zid] = st
		}
	}
	return zrows.Err()
}

func loadProduct(ctx context.Context, q modkit.Queryer, id string) (Product, error) {
	ps, err := loadProducts(ctx, q, `WHERE id = ?`, id)
	if err != nil {
		return Product{}, err
	}
	if len(ps) == 0 {
		return Product{}, httpx.ErrNotFound
	}
	return ps[0], nil
}

func (m *Module) listProducts(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	q := r.URL.Query()
	where, args := "WHERE archived_at IS NULL", []any{}
	switch q.Get("archived") {
	case "", "0":
	case "1":
		where = "WHERE archived_at IS NOT NULL"
	default:
		return httpx.Invalid("archived", "invalid")
	}
	if c := q.Get("category"); c != "" {
		where += ` AND (category_id = ? OR category_id IN (SELECT id FROM categories WHERE parent_id = ?))`
		args = append(args, c, c)
	}
	ps, err := loadProducts(r.Context(), m.app.DB.R, where, args...)
	if err != nil {
		return err
	}
	cats, err := loadCategories(r.Context(), m.app.DB.R)
	if err != nil {
		return err
	}
	sortByCategory(ps, cats)
	if ps == nil {
		ps = []Product{}
	}
	if !p.Unrestricted() {
		// Users and operators limited to zones see the overrides of their zones only (ARCHITECTURE §7).
		hide := func(zs map[string]string) {
			for zone := range zs {
				if !p.CanViewZone(zone) {
					delete(zs, zone)
				}
			}
		}
		for i := range ps {
			hide(ps[i].ZoneStatus)
			for _, v := range ps[i].Variants {
				hide(v.ZoneStatus)
			}
		}
	}
	return httpx.OK(w, map[string]any{"products": ps})
}

// sortByCategory orders products by the display order of their category, keeping the
// product order (sort, name) inside a category.
func sortByCategory(ps []Product, cats []Category) {
	pos := make(map[string]int, len(cats))
	for i, c := range cats {
		pos[c.ID] = i
	}
	sort.SliceStable(ps, func(i, j int) bool { return pos[ps[i].CategoryID] < pos[ps[j].CategoryID] })
}

type productReq struct {
	Revision               int64                          `json:"revision"`
	Name                   *string                        `json:"name"`
	Description            *string                        `json:"description"`
	CategoryID             *string                        `json:"categoryId"`
	Type                   *string                        `json:"type"`
	Quantity               modkit.Opt[contracts.Quantity] `json:"quantity"`
	Price                  *contracts.Money               `json:"price"`
	ImageAssetID           modkit.Opt[string]             `json:"imageAssetId"`
	ShowImageInsteadOfName *bool                          `json:"showImageInsteadOfName"`
	ImageScale             *int                           `json:"imageScale"`
	// Variants replaces the variants: entries with the id of an existing variant keep it
	// (and its status), entries without id are new.
	Variants *[]variantReq `json:"variants"`
	Sort     *int          `json:"sort"`
	// AfterID (create only, "Duplicate"): place the new product right after this one when
	// both are in the same category; otherwise it goes to the end as usual.
	AfterID *string `json:"afterId"`
}

type variantReq struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// apply merges the request into p and checks the shape of every provided field.
func (req *productReq) apply(p *Product) error {
	if req.Name != nil {
		name, ok := modkit.CleanName(*req.Name, maxNameRunes)
		if !ok {
			return httpx.Invalid("name", "invalid")
		}
		p.Name = name
	}
	if req.Description != nil {
		d := strings.TrimSpace(*req.Description)
		if utf8.RuneCountInString(d) > maxDescriptionRunes {
			return httpx.Invalid("description", "too_long")
		}
		p.Description = d
	}
	if req.CategoryID != nil {
		if *req.CategoryID == "" {
			return httpx.Invalid("categoryId", "required")
		}
		p.CategoryID = *req.CategoryID
	}
	if req.Type != nil {
		if !contracts.ValidProductType(*req.Type) {
			return httpx.Invalid("type", "invalid_value")
		}
		p.Type = *req.Type
	}
	if req.Quantity.Set {
		if req.Quantity.Null {
			p.Quantity = nil
		} else {
			q := req.Quantity.Val
			if err := q.Validate(); err != nil {
				return err
			}
			p.Quantity = &q
		}
	}
	if req.Price != nil {
		price := *req.Price
		if price.Currency == "" {
			price.Currency = p.Price.Currency
		}
		if err := price.Validate("price"); err != nil {
			return err
		}
		p.Price = price
	}
	if req.ImageAssetID.Set {
		p.ImageAssetID = ""
		if req.ImageAssetID.Has() {
			p.ImageAssetID = req.ImageAssetID.Val
		}
	}
	if req.ShowImageInsteadOfName != nil {
		p.ShowImageInsteadOfName = *req.ShowImageInsteadOfName
	} else if p.ImageAssetID == "" {
		p.ShowImageInsteadOfName = false // "image instead of name" is meaningless without an image
	}
	if req.ImageScale != nil {
		if *req.ImageScale < MinImageScale || *req.ImageScale > MaxImageScale {
			return httpx.Invalid("imageScale", "out_of_range")
		}
		p.ImageScale = *req.ImageScale
	}
	if req.Sort != nil {
		p.Sort = *req.Sort
	}
	if req.Variants != nil {
		vs, err := applyVariants(*req.Variants, p.Variants)
		if err != nil {
			return err
		}
		p.Variants = vs
	}
	return nil
}

// applyVariants builds the new variant list: known ids keep their variant (status and zone
// overrides), entries without id become new variants. Names are unique (ignoring case).
func applyVariants(req []variantReq, old []Variant) ([]Variant, error) {
	if len(req) > maxVariants {
		return nil, httpx.Invalid("variants", "too_many")
	}
	byID := make(map[string]Variant, len(old))
	for _, v := range old {
		byID[v.ID] = v
	}
	out := make([]Variant, 0, len(req))
	names, seen := map[string]bool{}, map[string]bool{}
	for _, r := range req {
		name, ok := modkit.CleanName(r.Name, maxVariantRunes)
		if !ok {
			return nil, httpx.Invalid("variantName", "invalid")
		}
		key := strings.ToLower(name)
		if names[key] {
			return nil, httpx.Invalid("variantName", "duplicate")
		}
		names[key] = true
		v := Variant{ID: ids.New(), Name: name, Status: contracts.StatusAvailable, ZoneStatus: map[string]string{}}
		if r.ID != "" {
			known, ok := byID[r.ID]
			if !ok || seen[r.ID] {
				return nil, httpx.Invalid("variants", "invalid")
			}
			seen[r.ID] = true
			v.ID, v.Status, v.ZoneStatus = known.ID, known.Status, known.ZoneStatus
		}
		out = append(out, v)
	}
	return out, nil
}

// writeVariants stores the variants of a product in their order; variants that are gone are
// deleted with their zone overrides.
func writeVariants(ctx context.Context, tx *sql.Tx, productID string, old, next []Variant) error {
	keep := make(map[string]bool, len(next))
	for _, v := range next {
		keep[v.ID] = true
	}
	for _, v := range old {
		if !keep[v.ID] {
			if _, err := tx.ExecContext(ctx, `DELETE FROM product_variants WHERE id = ?`, v.ID); err != nil {
				return err
			}
		}
	}
	for i, v := range next {
		if _, err := tx.ExecContext(ctx, `INSERT INTO product_variants(id, product_id, name, sort, status) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name, sort = excluded.sort`, v.ID, productID, v.Name, i, v.Status); err != nil {
			return err
		}
	}
	return nil
}

func sameVariants(a, b []Variant) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

// Size of an image instead of the name, percent of the automatic size.
const (
	MinImageScale = 25
	MaxImageScale = 400
)

// checkProductRefs verifies the references of the merged product against the database.
// old is the stored product (nil on create): references that did not change are not checked
// again, so a product whose image was trashed meanwhile can still be renamed or repriced.
func checkProductRefs(ctx context.Context, tx *sql.Tx, p, old *Product) error {
	if old == nil || p.CategoryID != old.CategoryID {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM categories WHERE id = ?`, p.CategoryID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.Invalid("categoryId", "unknown_category")
		}
		if err != nil {
			return err
		}
	}
	if p.ImageAssetID != "" && (old == nil || p.ImageAssetID != old.ImageAssetID) {
		var kind string
		err := tx.QueryRowContext(ctx, `SELECT kind FROM media_assets WHERE id = ? AND trashed_at IS NULL`, p.ImageAssetID).Scan(&kind)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.Invalid("imageAssetId", "unknown_asset")
		}
		if err != nil {
			return err
		}
		if kind != "image" {
			return httpx.Invalid("imageAssetId", "wrong_asset_kind")
		}
	}
	if p.ShowImageInsteadOfName && p.ImageAssetID == "" {
		return httpx.Invalid("showImageInsteadOfName", "needs_image")
	}
	return nil
}

func nextProductSort(ctx context.Context, tx *sql.Tx, categoryID string) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort), -1) + 1 FROM products WHERE category_id = ?`, categoryID).Scan(&n)
	return n, err
}

// syncProductRef keeps the content_refs row that ties a product to its image asset (used
// by the media module for usage display and safe deletion).
func syncProductRef(ctx context.Context, tx *sql.Tx, productID, assetID string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM content_refs WHERE owner_type = 'product' AND owner_id = ?`, productID); err != nil {
		return err
	}
	if assetID == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO content_refs(asset_id, owner_type, owner_id) VALUES (?, 'product', ?)`, assetID, productID)
	return err
}

func quantityParts(q *contracts.Quantity) (string, string) {
	if q == nil {
		return "", ""
	}
	return q.Value, q.Unit
}

func (m *Module) createProduct(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req productReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	switch {
	case req.Name == nil:
		return httpx.Invalid("name", "required")
	case req.CategoryID == nil:
		return httpx.Invalid("categoryId", "required")
	case req.Type == nil:
		return httpx.Invalid("type", "required")
	case req.Price == nil:
		return httpx.Invalid("price", "required")
	}
	prod := Product{ID: ids.New(), Status: contracts.StatusAvailable, ZoneStatus: map[string]string{}, Variants: []Variant{},
		Price: contracts.Money{Currency: m.app.Settings.Get().Currency}, ImageScale: 100, Revision: 1}
	if err := req.apply(&prod); err != nil {
		return err
	}
	var out Product
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		if err := checkProductRefs(r.Context(), tx, &prod, nil); err != nil {
			return err
		}
		if req.Sort == nil {
			var err error
			if prod.Sort, err = nextProductSort(r.Context(), tx, prod.CategoryID); err != nil {
				return err
			}
		}
		now := db.Now()
		qv, qu := quantityParts(prod.Quantity)
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO products(id, category_id, name, description, type, quantity_value, quantity_unit,
			price_amount, currency, status, image_asset_id, image_instead_of_name, image_scale, sort, created_at, updated_at, revision)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, 1)`,
			prod.ID, prod.CategoryID, prod.Name, prod.Description, prod.Type, qv, qu, prod.Price.Amount, prod.Price.Currency,
			prod.Status, prod.ImageAssetID, db.Bool(prod.ShowImageInsteadOfName), prod.ImageScale, prod.Sort, now, now); err != nil {
			return err
		}
		if err := syncProductRef(r.Context(), tx, prod.ID, prod.ImageAssetID); err != nil {
			return err
		}
		if err := writeVariants(r.Context(), tx, prod.ID, nil, prod.Variants); err != nil {
			return err
		}
		if req.AfterID != nil && req.Sort == nil {
			if err := placeAfter(r.Context(), tx, prod.ID, prod.CategoryID, *req.AfterID); err != nil {
				return err
			}
		}
		var err error
		if out, err = loadProduct(r.Context(), tx, prod.ID); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "product.created", ObjectType: "product",
			ObjectID: prod.ID, ObjectName: prod.Name, New: out})
	})
	if err != nil {
		return err
	}
	m.productChanged("created", out.ID, out.ImageAssetID != "")
	return httpx.OK(w, out)
}

// placeAfter moves product id right behind afterID inside their category (a duplicate lands
// next to its original). Nothing happens when afterID is unknown or in another category.
func placeAfter(ctx context.Context, tx *sql.Tx, id, category, afterID string) error {
	var afterCat string
	err := tx.QueryRowContext(ctx, `SELECT category_id FROM products WHERE id = ?`, afterID).Scan(&afterCat)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && afterCat != category) {
		return nil
	}
	if err != nil {
		return err
	}
	current, err := queryIDs(ctx, tx, `SELECT id FROM products WHERE category_id = ? ORDER BY sort, name COLLATE NOCASE, id`, category)
	if err != nil {
		return err
	}
	order := make([]string, 0, len(current))
	for _, x := range current {
		if x == id {
			continue
		}
		order = append(order, x)
		if x == afterID {
			order = append(order, id)
		}
	}
	_, err = applyOrder(ctx, tx, "products", current, order)
	return err
}

// productChanged publishes the events for changed product master data. The live state of
// every zone carries the product list; content changes only when product images do.
func (m *Module) productChanged(kind, id string, imageRelevant bool) {
	m.emit(core.TopicProducts, kind, id, "")
	m.emit(core.TopicLive, "product."+kind, id, "")
	if imageRelevant {
		m.emit(core.TopicContent, "product."+kind, id, "")
	}
}

func (m *Module) updateProduct(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req productReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.Revision <= 0 {
		return httpx.Invalid("revision", "required")
	}
	if req.AfterID != nil {
		return httpx.Invalid("afterId", "invalid") // placement only when creating
	}
	var old, out Product
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var err error
		if old, err = loadProduct(r.Context(), tx, id); err != nil {
			return err
		}
		if old.Revision != req.Revision {
			return conflictWithRevision(old.Revision)
		}
		next := old
		if err := req.apply(&next); err != nil {
			return err
		}
		if err := checkProductRefs(r.Context(), tx, &next, &old); err != nil {
			return err
		}
		if next.CategoryID != old.CategoryID && req.Sort == nil {
			if next.Sort, err = nextProductSort(r.Context(), tx, next.CategoryID); err != nil {
				return err
			}
		}
		qv, qu := quantityParts(next.Quantity)
		if _, err := tx.ExecContext(r.Context(), `UPDATE products SET category_id = ?, name = ?, description = ?, type = ?,
			quantity_value = ?, quantity_unit = ?, price_amount = ?, currency = ?, image_asset_id = NULLIF(?, ''),
			image_instead_of_name = ?, image_scale = ?, sort = ?, updated_at = ?, revision = revision + 1 WHERE id = ?`,
			next.CategoryID, next.Name, next.Description, next.Type, qv, qu, next.Price.Amount, next.Price.Currency,
			next.ImageAssetID, db.Bool(next.ShowImageInsteadOfName), next.ImageScale, next.Sort, db.Now(), id); err != nil {
			return err
		}
		if next.ImageAssetID != old.ImageAssetID {
			if err := syncProductRef(r.Context(), tx, id, next.ImageAssetID); err != nil {
				return err
			}
		}
		if !sameVariants(old.Variants, next.Variants) {
			if err := writeVariants(r.Context(), tx, id, old.Variants, next.Variants); err != nil {
				return err
			}
		}
		if out, err = loadProduct(r.Context(), tx, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "product.updated", ObjectType: "product",
			ObjectID: id, ObjectName: out.Name, Old: old, New: out})
	})
	if err != nil {
		return err
	}
	m.productChanged("updated", id, old.ImageAssetID != "" || out.ImageAssetID != "")
	return httpx.OK(w, out)
}

func (m *Module) reorderProducts(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req reorderReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if err := checkIDList(req.IDs); err != nil {
		return err
	}
	var newOrder []string
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		category := ""
		for i, id := range req.IDs {
			var cat string
			err := tx.QueryRowContext(r.Context(), `SELECT category_id FROM products WHERE id = ?`, id).Scan(&cat)
			if errors.Is(err, sql.ErrNoRows) {
				return httpx.Invalid("ids", "unknown_product")
			}
			if err != nil {
				return err
			}
			if i == 0 {
				category = cat
			} else if cat != category {
				return httpx.Invalid("ids", "mixed_categories")
			}
		}
		current, err := queryIDs(r.Context(), tx, `SELECT id FROM products WHERE category_id = ? ORDER BY sort, name COLLATE NOCASE, id`, category)
		if err != nil {
			return err
		}
		if newOrder, err = applyOrder(r.Context(), tx, "products", current, req.IDs); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "product.reordered", ObjectType: "category",
			ObjectID: category, Old: current, New: newOrder})
	})
	if err != nil {
		return err
	}
	m.productChanged("reordered", "", false)
	return httpx.OK(w, map[string]any{"ids": newOrder})
}

func (m *Module) setArchived(w http.ResponseWriter, r *http.Request, p *core.Principal, archive bool) error {
	id := r.PathValue("id")
	var old, out Product
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var err error
		if old, err = loadProduct(r.Context(), tx, id); err != nil {
			return err
		}
		if old.Archived == archive {
			out = old
			return nil
		}
		at := any(nil)
		action := "product.restored"
		if archive {
			at, action = db.Now(), "product.archived"
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE products SET archived_at = ?, updated_at = ?, revision = revision + 1 WHERE id = ?`, at, db.Now(), id); err != nil {
			return err
		}
		if out, err = loadProduct(r.Context(), tx, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: action, ObjectType: "product", ObjectID: id,
			ObjectName: out.Name, Old: map[string]any{"archived": old.Archived}, New: map[string]any{"archived": out.Archived}})
	})
	if err != nil {
		return err
	}
	if old.Archived != archive {
		kind := "restored"
		if archive {
			kind = "archived"
		}
		m.productChanged(kind, id, out.ImageAssetID != "")
	}
	return httpx.OK(w, out)
}

func (m *Module) archiveProduct(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	return m.setArchived(w, r, p, true)
}

func (m *Module) restoreProduct(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	return m.setArchived(w, r, p, false)
}

type namedRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func queryRefs(ctx context.Context, q modkit.Queryer, query string, args ...any) ([]namedRef, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []namedRef{}
	for rows.Next() {
		var n namedRef
		if err := rows.Scan(&n.ID, &n.Name); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func refNames(refs []namedRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.Name
	}
	return out
}

// presentationsMentioning lists the presentations and templates whose draft or published
// version contains the id. Deleted (archived) presentations do not count: users cannot see
// them, and restoring one re-validates its references (restore_references_missing).
// Ids are UUIDs, so a quoted-id match is exact.
func presentationsMentioning(ctx context.Context, q modkit.Queryer, id string) ([]namedRef, error) {
	return queryRefs(ctx, q, `SELECT p.id, p.name FROM presentations p
		WHERE p.archived_at IS NULL AND (instr(p.draft, ?1) > 0 OR EXISTS (
		  SELECT 1 FROM presentation_versions v WHERE v.presentation_id = p.id AND v.version = p.published_version AND instr(v.doc, ?1) > 0))
		ORDER BY p.name COLLATE NOCASE, p.id`, `"`+id+`"`)
}

func (m *Module) deleteProduct(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	hadImage := false
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		old, err := loadProduct(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if !old.Archived {
			return errProductNotArch
		}
		hadImage = old.ImageAssetID != ""
		rules, err := queryRefs(r.Context(), tx, `SELECT DISTINCT r.id, r.name FROM schedule_prices sp
			JOIN schedule_rules r ON r.id = sp.rule_id WHERE sp.product_id = ? ORDER BY r.name, r.id`, id)
		if err != nil {
			return err
		}
		// A product is referenced by presentations through its id inside the JSON documents
		// (product elements, manual product lists).
		pres, err := presentationsMentioning(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if len(rules) > 0 || len(pres) > 0 {
			return errProductInUse.With("rules", refNames(rules)).With("presentations", refNames(pres))
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM content_refs WHERE owner_type = 'product' AND owner_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM products WHERE id = ?`, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "product.deleted", ObjectType: "product",
			ObjectID: id, ObjectName: old.Name, Old: old})
	})
	if err != nil {
		return err
	}
	m.productChanged("deleted", id, hadImage)
	return httpx.NoContent(w)
}
