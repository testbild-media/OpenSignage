package media

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"opensignage/internal/common/ids"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
)

type folderView struct {
	ID          string  `json:"id"`
	ParentID    *string `json:"parentId"`
	Name        string  `json:"name"`
	ItemCount   int     `json:"itemCount"`   // assets in this folder and all sub folders
	FolderCount int     `json:"folderCount"` // direct sub folders
	TotalSize   int64   `json:"totalSize"`   // bytes of the originals in the whole subtree
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
}

type folderRec struct {
	ID        string
	ParentID  *string
	Name      string
	CreatedAt string
	UpdatedAt string
}

// liveFolders loads all folders that are not in the trash.
func liveFolders(ctx context.Context, q queryer) ([]folderRec, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, parent_id, name, created_at, updated_at FROM media_folders WHERE trashed_at IS NULL ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []folderRec
	for rows.Next() {
		var f folderRec
		var parent sql.NullString
		if err := rows.Scan(&f.ID, &parent, &f.Name, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		f.ParentID = nullStr(parent)
		out = append(out, f)
	}
	return out, rows.Err()
}

// folderStats computes item count and size for the whole subtree of every folder.
func folderStats(ctx context.Context, q queryer, folders []folderRec) (count map[string]int, size map[string]int64, sub map[string]int, err error) {
	rows, err := q.QueryContext(ctx, `SELECT a.folder_id, COUNT(*), COALESCE(SUM(b.size), 0) FROM media_assets a
		LEFT JOIN blobs b ON b.sha256 = a.blob_sha256 WHERE a.trashed_at IS NULL AND a.folder_id IS NOT NULL GROUP BY a.folder_id`)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	direct := map[string]int{}
	directSize := map[string]int64{}
	for rows.Next() {
		var id string
		var n int
		var s int64
		if err := rows.Scan(&id, &n, &s); err != nil {
			return nil, nil, nil, err
		}
		direct[id], directSize[id] = n, s
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	children := map[string][]string{}
	for _, f := range folders {
		if f.ParentID != nil {
			children[*f.ParentID] = append(children[*f.ParentID], f.ID)
		}
	}
	count, size, sub = map[string]int{}, map[string]int64{}, map[string]int{}
	var walk func(id string, depth int) (int, int64)
	walk = func(id string, depth int) (int, int64) {
		if c, ok := count[id]; ok {
			return c, size[id]
		}
		c, s := direct[id], directSize[id]
		count[id], size[id] = c, s // guards against cycles in damaged data
		if depth < 256 {
			for _, ch := range children[id] {
				cc, cs := walk(ch, depth+1)
				c, s = c+cc, s+cs
			}
		}
		count[id], size[id], sub[id] = c, s, len(children[id])
		return c, s
	}
	for _, f := range folders {
		walk(f.ID, 0)
	}
	return count, size, sub, nil
}

func folderViews(folders []folderRec, count map[string]int, size map[string]int64, sub map[string]int) []folderView {
	out := make([]folderView, 0, len(folders))
	for _, f := range folders {
		out = append(out, folderView{ID: f.ID, ParentID: f.ParentID, Name: f.Name, ItemCount: count[f.ID],
			FolderCount: sub[f.ID], TotalSize: size[f.ID], CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt})
	}
	return out
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// list serves GET /api/v1/media?folder=<id>|root&q=&kind=
//
// "folder=root" lists ONLY the top level; omitting "folder" means "no folder filter"
// (search across everything). An empty value is rejected so it can never be mistaken for
// either (otherwise the root folder would show all files).
func (m *Module) list(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	ctx := r.Context()
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("q"))
	where, args := []string{"a.trashed_at IS NULL"}, []any{}

	var folderID *string // the folder whose children are listed
	listFolderChildren, restrictToTop := false, false
	if q.Has("folder") {
		switch f := q.Get("folder"); f {
		case "":
			return httpx.Invalid("folder", "empty")
		case "root":
			where = append(where, "a.folder_id IS NULL")
			listFolderChildren, restrictToTop = true, true
		default:
			var n int
			if err := m.app.DB.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_folders WHERE id = ? AND trashed_at IS NULL`, f).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return httpx.ErrNotFound
			}
			folderID = &f
			where, args = append(where, "a.folder_id = ?"), append(args, f)
			listFolderChildren = true
		}
	}
	if q.Has("kind") {
		switch k := q.Get("kind"); k {
		case KindImage, KindVideo, KindAudio:
			where, args = append(where, "a.kind = ?"), append(args, k)
		default:
			return httpx.Invalid("kind", "invalid")
		}
	}
	if search != "" {
		where, args = append(where, `a.name LIKE ? ESCAPE '\'`), append(args, "%"+escapeLike(search)+"%")
	}
	limit := httpx.QueryInt(r, "limit", 1000, 1, 5000)
	offset := httpx.QueryInt(r, "offset", 0, 0, 1<<30)

	var total int
	if err := m.app.DB.R.QueryRowContext(ctx, `SELECT COUNT(*)`+assetFrom+`WHERE `+strings.Join(where, " AND "), args...).Scan(&total); err != nil {
		return err
	}
	assets, err := m.loadAssets(ctx, m.app.DB.R, `WHERE `+strings.Join(where, " AND ")+` ORDER BY a.name COLLATE NOCASE, a.id LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return err
	}

	all, err := liveFolders(ctx, m.app.DB.R)
	if err != nil {
		return err
	}
	var shown []folderRec
	for _, f := range all {
		if listFolderChildren {
			if restrictToTop && f.ParentID != nil || !restrictToTop && (f.ParentID == nil || *f.ParentID != *folderID) {
				continue
			}
		}
		if search != "" && !strings.Contains(strings.ToLower(f.Name), strings.ToLower(search)) {
			continue
		}
		shown = append(shown, f)
	}
	count, size, sub, err := folderStats(ctx, m.app.DB.R, all)
	if err != nil {
		return err
	}
	crumbs, err := m.folderPath(ctx, m.app.DB.R, folderID)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{
		"folders": folderViews(shown, count, size, sub), "assets": assets, "breadcrumb": crumbs, "total": total,
	})
}

type createFolderReq struct {
	Name     string  `json:"name"`
	ParentID *string `json:"parentId"`
}

func (m *Module) createFolder(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req createFolderReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	name, ok := cleanLabel(req.Name)
	if !ok {
		return httpx.Invalid("name", "invalid")
	}
	if req.ParentID != nil && *req.ParentID == "" {
		return httpx.Invalid("parentId", "empty")
	}
	id := ids.New()
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		if req.ParentID != nil {
			if err := requireLiveFolder(r.Context(), tx, *req.ParentID, "parentId"); err != nil {
				return err
			}
		}
		if err := ensureNameFree(r.Context(), tx, req.ParentID, name, ""); err != nil {
			return err
		}
		now := db.Now()
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO media_folders(id, parent_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			id, req.ParentID, name, now, now); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "media.folder_created", ObjectType: "media_folder",
			ObjectID: id, ObjectName: name, New: map[string]any{"parentId": req.ParentID}})
	})
	if err != nil {
		return err
	}
	m.publishMedia("folder", id, nil)
	return httpx.OK(w, folderView{ID: id, ParentID: req.ParentID, Name: name})
}

// requireLiveFolder returns invalid_field/unknown_folder when the folder is missing or trashed.
func requireLiveFolder(ctx context.Context, q queryer, id, field string) error {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_folders WHERE id = ? AND trashed_at IS NULL`, id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return httpx.Invalid(field, "unknown_folder")
	}
	return nil
}

var errNameTaken = httpx.E(http.StatusConflict, "name_taken", "a folder with this name already exists here")

// ensureNameFree rejects a sibling folder with the same name (case-insensitive).
func ensureNameFree(ctx context.Context, q queryer, parent *string, name, exceptID string) error {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_folders WHERE parent_id IS ? AND name = ? COLLATE NOCASE AND trashed_at IS NULL AND id <> ?`,
		parent, name, exceptID).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return errNameTaken
	}
	return nil
}

// wouldCycle reports whether making newParent the parent of id creates a cycle, i.e.
// whether id is newParent itself or one of its ancestors. Runs inside the write
// transaction so concurrent moves cannot combine into a cycle.
func wouldCycle(ctx context.Context, q queryer, id, newParent string) (bool, error) {
	cur := newParent
	for i := 0; i < 1000; i++ {
		if cur == id {
			return true, nil
		}
		var parent sql.NullString
		err := q.QueryRowContext(ctx, `SELECT parent_id FROM media_folders WHERE id = ?`, cur).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !parent.Valid {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		cur = parent.String
	}
	return true, nil // absurdly deep: refuse
}

type patchFolderReq struct {
	Name     *string `json:"name"`
	ParentID optStr  `json:"parentId"`
}

func (m *Module) patchFolder(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req patchFolderReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	var newName string
	if req.Name != nil {
		n, ok := cleanLabel(*req.Name)
		if !ok {
			return httpx.Invalid("name", "invalid")
		}
		newName = n
	}
	if req.ParentID.Set && req.ParentID.Val != nil && *req.ParentID.Val == "" {
		return httpx.Invalid("parentId", "empty")
	}
	var out folderView
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var oldName string
		var oldParent sql.NullString
		var trashed sql.NullString
		err := tx.QueryRowContext(r.Context(), `SELECT name, parent_id, trashed_at FROM media_folders WHERE id = ?`, id).Scan(&oldName, &oldParent, &trashed)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if trashed.Valid {
			return httpx.E(http.StatusConflict, "in_trash", "the folder is in the trash")
		}
		name, parent := oldName, nullStr(oldParent)
		if req.Name != nil {
			name = newName
		}
		if req.ParentID.Set {
			parent = req.ParentID.Val
			if parent != nil {
				if err := requireLiveFolder(r.Context(), tx, *parent, "parentId"); err != nil {
					return err
				}
				cyc, err := wouldCycle(r.Context(), tx, id, *parent)
				if err != nil {
					return err
				}
				if cyc {
					return httpx.E(http.StatusConflict, "folder_cycle", "a folder cannot be moved into itself or one of its sub folders")
				}
			}
		}
		if err := ensureNameFree(r.Context(), tx, parent, name, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE media_folders SET name = ?, parent_id = ?, updated_at = ? WHERE id = ?`, name, parent, db.Now(), id); err != nil {
			return err
		}
		action := "media.folder_renamed"
		if !strEq(nullStr(oldParent), parent) {
			action = "media.folder_moved"
		}
		out = folderView{ID: id, ParentID: parent, Name: name}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: action, ObjectType: "media_folder", ObjectID: id, ObjectName: name,
			Old: map[string]any{"name": oldName, "parentId": nullStr(oldParent)}, New: map[string]any{"name": name, "parentId": parent}})
	})
	if err != nil {
		return err
	}
	m.publishMedia("folder", id, nil)
	return httpx.OK(w, out)
}
