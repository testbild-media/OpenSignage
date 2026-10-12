package catalog

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"

	"opensignage/internal/common/ids"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

const maxNameRunes = 200

// Category is the API representation; categories have two levels (category → subcategory).
type Category struct {
	ID           string `json:"id"`
	ParentID     string `json:"parentId,omitempty"`
	Name         string `json:"name"`
	Sort         int    `json:"sort"`
	ProductCount int    `json:"productCount"` // non-archived products directly in this category
	Revision     int64  `json:"revision"`
}

// loadCategories returns all categories, each top-level category followed by its children.
func loadCategories(ctx context.Context, q modkit.Queryer) ([]Category, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, COALESCE(parent_id, ''), name, sort, revision FROM categories`)
	if err != nil {
		return nil, err
	}
	var all []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.ParentID, &c.Name, &c.Sort, &c.Revision); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	crows, err := q.QueryContext(ctx, `SELECT category_id, COUNT(*) FROM products WHERE archived_at IS NULL GROUP BY category_id`)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for crows.Next() {
		var id string
		var n int
		if err := crows.Scan(&id, &n); err != nil {
			crows.Close()
			return nil, err
		}
		counts[id] = n
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return nil, err
	}
	less := func(a, b Category) bool {
		if a.Sort != b.Sort {
			return a.Sort < b.Sort
		}
		if an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name); an != bn {
			return an < bn
		}
		return a.ID < b.ID
	}
	var tops []Category
	children := map[string][]Category{}
	for _, c := range all {
		c.ProductCount = counts[c.ID]
		if c.ParentID == "" {
			tops = append(tops, c)
		} else {
			children[c.ParentID] = append(children[c.ParentID], c)
		}
	}
	sort.Slice(tops, func(i, j int) bool { return less(tops[i], tops[j]) })
	out := make([]Category, 0, len(all))
	for _, t := range tops {
		out = append(out, t)
		kids := children[t.ID]
		sort.Slice(kids, func(i, j int) bool { return less(kids[i], kids[j]) })
		out = append(out, kids...)
	}
	return out, nil
}

func loadCategory(ctx context.Context, q modkit.Queryer, id string) (Category, error) {
	var c Category
	err := q.QueryRowContext(ctx, `SELECT id, COALESCE(parent_id, ''), name, sort, revision FROM categories WHERE id = ?`, id).
		Scan(&c.ID, &c.ParentID, &c.Name, &c.Sort, &c.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return c, httpx.ErrNotFound
	}
	return c, err
}

func (m *Module) listCategories(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	cats, err := loadCategories(r.Context(), m.app.DB.R)
	if err != nil {
		return err
	}
	if cats == nil {
		cats = []Category{}
	}
	return httpx.OK(w, map[string]any{"categories": cats})
}

// parentArg maps "" to NULL for `parent_id IS ?` comparisons.
func parentArg(parentID string) any {
	if parentID == "" {
		return nil
	}
	return parentID
}

func nextCategorySort(ctx context.Context, tx *sql.Tx, parentID string) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort), -1) + 1 FROM categories WHERE parent_id IS ?`, parentArg(parentID)).Scan(&n)
	return n, err
}

// checkParent verifies that parentID may become the parent of the category childID
// ("" for a new category): it must exist, be top-level, differ from the child, and the
// child must not have subcategories of its own (two levels at most).
func checkParent(ctx context.Context, tx *sql.Tx, parentID, childID string) error {
	var grand sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT parent_id FROM categories WHERE id = ?`, parentID).Scan(&grand)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.Invalid("parentId", "unknown_category")
	}
	if err != nil {
		return err
	}
	if grand.Valid || parentID == childID {
		return httpx.Invalid("parentId", "invalid_parent")
	}
	if childID != "" {
		var kids int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM categories WHERE parent_id = ?`, childID).Scan(&kids); err != nil {
			return err
		}
		if kids > 0 {
			return httpx.Invalid("parentId", "has_subcategories")
		}
	}
	return nil
}

type categoryReq struct {
	Revision int64              `json:"revision"`
	Name     *string            `json:"name"`
	ParentID modkit.Opt[string] `json:"parentId"`
	Sort     *int               `json:"sort"`
}

func (m *Module) createCategory(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req categoryReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.Name == nil {
		return httpx.Invalid("name", "required")
	}
	name, ok := modkit.CleanName(*req.Name, maxNameRunes)
	if !ok {
		return httpx.Invalid("name", "invalid")
	}
	parentID := ""
	if req.ParentID.Has() {
		parentID = req.ParentID.Val
	}
	id := ids.New()
	var created Category
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		if parentID != "" {
			if err := checkParent(r.Context(), tx, parentID, ""); err != nil {
				return err
			}
		}
		srt := 0
		var err error
		if req.Sort != nil {
			srt = *req.Sort
		} else if srt, err = nextCategorySort(r.Context(), tx, parentID); err != nil {
			return err
		}
		now := db.Now()
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO categories(id, parent_id, name, sort, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, parentArg(parentID), name, srt, now, now); err != nil {
			return err
		}
		created = Category{ID: id, ParentID: parentID, Name: name, Sort: srt, Revision: 1}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "category.created", ObjectType: "category",
			ObjectID: id, ObjectName: name, New: created})
	})
	if err != nil {
		return err
	}
	m.emit(core.TopicProducts, "category.created", id, "")
	m.emit(core.TopicLive, "category.created", id, "")
	return httpx.OK(w, created)
}

func (m *Module) updateCategory(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req categoryReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.Revision <= 0 {
		return httpx.Invalid("revision", "required")
	}
	var name string
	if req.Name != nil {
		var ok bool
		if name, ok = modkit.CleanName(*req.Name, maxNameRunes); !ok {
			return httpx.Invalid("name", "invalid")
		}
	}
	var updated Category
	changed := false
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		old, err := loadCategory(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if old.Revision != req.Revision {
			return conflictWithRevision(old.Revision)
		}
		next := old
		if req.Name != nil {
			next.Name = name
		}
		if req.ParentID.Set {
			newParent := ""
			if req.ParentID.Has() {
				newParent = req.ParentID.Val
			}
			if newParent != old.ParentID {
				if newParent != "" {
					if err := checkParent(r.Context(), tx, newParent, id); err != nil {
						return err
					}
				}
				next.ParentID = newParent
				if req.Sort == nil {
					if next.Sort, err = nextCategorySort(r.Context(), tx, newParent); err != nil {
						return err
					}
				}
			}
		}
		if req.Sort != nil {
			next.Sort = *req.Sort
		}
		if next == old {
			updated = old
			return nil
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE categories SET parent_id = ?, name = ?, sort = ?, updated_at = ?,
			revision = revision + 1 WHERE id = ?`, parentArg(next.ParentID), next.Name, next.Sort, db.Now(), id); err != nil {
			return err
		}
		next.Revision++
		updated, changed = next, true
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "category.updated", ObjectType: "category",
			ObjectID: id, ObjectName: next.Name, Old: old, New: next})
	})
	if err != nil {
		return err
	}
	if changed {
		m.emit(core.TopicProducts, "category.updated", id, "")
		m.emit(core.TopicLive, "category.updated", id, "")
	}
	return httpx.OK(w, updated)
}

type reorderReq struct {
	IDs []string `json:"ids"`
}

// checkIDList validates a reorder list: non-empty, bounded, no duplicates.
func checkIDList(ids []string) error {
	if len(ids) == 0 || len(ids) > 5000 {
		return httpx.Invalid("ids", "invalid")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			return httpx.Invalid("ids", "invalid")
		}
		seen[id] = true
	}
	return nil
}

// applyOrder assigns sort = position: the listed ids first (in the given order), then the
// remaining siblings in their current order. Only changed rows are written.
func applyOrder(ctx context.Context, tx *sql.Tx, table string, current, listed []string) ([]string, error) {
	order := append([]string(nil), listed...)
	in := make(map[string]bool, len(listed))
	for _, id := range listed {
		in[id] = true
	}
	for _, id := range current {
		if !in[id] {
			order = append(order, id)
		}
	}
	now := db.Now()
	for i, id := range order {
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET sort = ?, updated_at = ? WHERE id = ? AND sort <> ?`, i, now, id, i); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func queryIDs(ctx context.Context, q modkit.Queryer, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (m *Module) reorderCategories(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req reorderReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if err := checkIDList(req.IDs); err != nil {
		return err
	}
	var newOrder []string
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		parent := ""
		for i, id := range req.IDs {
			c, err := loadCategory(r.Context(), tx, id)
			if errors.Is(err, httpx.ErrNotFound) {
				return httpx.Invalid("ids", "unknown_category")
			}
			if err != nil {
				return err
			}
			if i == 0 {
				parent = c.ParentID
			} else if c.ParentID != parent {
				return httpx.Invalid("ids", "mixed_levels")
			}
		}
		current, err := queryIDs(r.Context(), tx, `SELECT id FROM categories WHERE parent_id IS ? ORDER BY sort, name COLLATE NOCASE, id`, parentArg(parent))
		if err != nil {
			return err
		}
		if newOrder, err = applyOrder(r.Context(), tx, "categories", current, req.IDs); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "category.reordered", ObjectType: "category",
			ObjectID: parent, Old: current, New: newOrder})
	})
	if err != nil {
		return err
	}
	m.emit(core.TopicProducts, "category.reordered", "", "")
	m.emit(core.TopicLive, "category.reordered", "", "")
	return httpx.OK(w, map[string]any{"ids": newOrder})
}

func (m *Module) deleteCategory(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		old, err := loadCategory(r.Context(), tx, id)
		if err != nil {
			return err
		}
		var products, subs int
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM products WHERE category_id = ?`, id).Scan(&products); err != nil {
			return err
		}
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM categories WHERE parent_id = ?`, id).Scan(&subs); err != nil {
			return err
		}
		if products > 0 || subs > 0 {
			return errCategoryNotEmpty.With("products", products).With("subcategories", subs)
		}
		// Product lists select categories by id inside the JSON documents (drafts and the
		// published versions of presentations and templates that were not deleted).
		pres, err := presentationsMentioning(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if len(pres) > 0 {
			return errCategoryInUse.With("presentations", refNames(pres))
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM categories WHERE id = ?`, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "category.deleted", ObjectType: "category",
			ObjectID: id, ObjectName: old.Name, Old: old})
	})
	if err != nil {
		return err
	}
	m.emit(core.TopicProducts, "category.deleted", id, "")
	m.emit(core.TopicLive, "category.deleted", id, "")
	return httpx.NoContent(w)
}
