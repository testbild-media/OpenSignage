package media

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"opensignage/internal/common/ids"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
)

// subtreeCTE selects the ids of a live folder and all its live descendants. UNION (not
// UNION ALL) keeps the recursion finite even if the data were ever damaged into a cycle.
const subtreeCTE = `WITH RECURSIVE sub(id) AS (
	SELECT ? UNION
	SELECT f.id FROM media_folders f JOIN sub ON f.parent_id = sub.id WHERE f.trashed_at IS NULL
) `

// trashAsset moves one asset to the trash (DELETE /api/v1/media/{id}).
func (m *Module) trashAsset(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	// an asset that is already in the trash is not an error (idempotent)
	if _, err := m.trashAssets(r.Context(), p, httpx.ClientIP(r), []string{id}, true); err != nil {
		return err
	}
	return httpx.NoContent(w)
}

type idsReq struct {
	IDs []string `json:"ids"`
	All bool     `json:"all"`
}

// trashBulk moves several assets to the trash (POST /api/v1/media/trash).
func (m *Module) trashBulk(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req idsReq
	if err := httpx.Decode(w, r, 4<<20, &req); err != nil {
		return err
	}
	req.IDs = dedupe(req.IDs)
	if len(req.IDs) == 0 || len(req.IDs) > 5000 {
		return httpx.Invalid("ids", "invalid")
	}
	n, err := m.trashAssets(r.Context(), p, httpx.ClientIP(r), req.IDs, false)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"trashed": n})
}

// trashAssets trashes assets, all as one batch per call. With strict, an unknown id is a 404.
func (m *Module) trashAssets(ctx context.Context, p *core.Principal, ip string, assetIDs []string, strict bool) (int, error) {
	batch, now := ids.New(), db.Now()
	var done []string
	err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		for _, id := range assetIDs {
			a, err := loadAssetRec(ctx, tx, id)
			if errors.Is(err, sql.ErrNoRows) {
				if strict {
					return httpx.ErrNotFound
				}
				continue
			}
			if err != nil {
				return err
			}
			if a.TrashedAt != nil {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE media_assets SET trashed_at = ?, trash_batch = ?, original_folder_id = folder_id, folder_id = NULL, updated_at = ? WHERE id = ?`,
				now, batch, now, id); err != nil {
				return err
			}
			if err := core.Audit(ctx, tx, p, ip, core.AuditEntry{Action: "media.trashed", ObjectType: "media", ObjectID: id, ObjectName: a.Name,
				Old: map[string]any{"folderId": a.FolderID}}); err != nil {
				return err
			}
			done = append(done, id)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(done) > 0 {
		// Anything on a screen that used these files must disappear: rebuild deployments.
		for _, id := range done {
			m.publishMedia("trashed", id, nil)
		}
		m.publishContent("media.trashed", done[0])
	}
	return len(done), nil
}

// trashFolder moves a folder with its whole subtree to the trash as one batch.
func (m *Module) trashFolder(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	batch, now := ids.New(), db.Now()
	var nAssets, nFolders int
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var name string
		var trashed sql.NullString
		err := tx.QueryRowContext(r.Context(), `SELECT name, trashed_at FROM media_folders WHERE id = ?`, id).Scan(&name, &trashed)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if trashed.Valid {
			return httpx.E(http.StatusConflict, "in_trash", "the folder is already in the trash")
		}
		// assets first, while the subtree is still "live"
		res, err := tx.ExecContext(r.Context(), subtreeCTE+`UPDATE media_assets SET trashed_at = ?, trash_batch = ?, original_folder_id = folder_id, folder_id = NULL, updated_at = ?
			WHERE trashed_at IS NULL AND folder_id IN (SELECT id FROM sub)`, id, now, batch, now)
		if err != nil {
			return err
		}
		a, _ := res.RowsAffected()
		nAssets = int(a)
		res, err = tx.ExecContext(r.Context(), subtreeCTE+`UPDATE media_folders SET trashed_at = ?, trash_batch = ?, updated_at = ? WHERE id IN (SELECT id FROM sub)`, id, now, batch, now)
		if err != nil {
			return err
		}
		f, _ := res.RowsAffected()
		nFolders = int(f)
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "media.folder_trashed", ObjectType: "media_folder", ObjectID: id, ObjectName: name,
			New: map[string]any{"assets": nAssets, "folders": nFolders, "batch": batch}})
	})
	if err != nil {
		return err
	}
	m.publishMedia("folder", id, nil)
	m.publishContent("media.trashed", id)
	return httpx.OK(w, map[string]any{"trashedAssets": nAssets, "trashedFolders": nFolders})
}

// ---- listing ------------------------------------------------------------------------

type trashItem struct {
	ID               string     `json:"id"`
	Type             string     `json:"type"` // asset | folder
	Name             string     `json:"name"`
	Kind             string     `json:"kind,omitempty"`
	TrashedAt        string     `json:"trashedAt"`
	AgeDays          int        `json:"ageDays"`
	Size             int64      `json:"size"`
	ItemCount        int        `json:"itemCount"`   // assets (folders: assets in the batch)
	FolderCount      int        `json:"folderCount"` // folders in the batch (including itself)
	OriginalFolderID *string    `json:"originalFolderId"`
	OriginalPath     []crumb    `json:"originalPath"`
	Usage            []UsageRef `json:"usage"`
	Blocked          bool       `json:"blocked"` // purge impossible: used as product image
	Batch            string     `json:"batch"`
}

// trashItems builds the trash view: folder batches as one entry each, plus assets that
// were trashed on their own.
func (m *Module) trashItems(ctx context.Context, q queryer) ([]trashItem, error) {
	now := time.Now().UTC()
	age := func(ts string) int {
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return 0
		}
		return int(now.Sub(t).Hours() / 24)
	}
	type batchAgg struct {
		assets, folders int
		size            int64
	}
	agg := map[string]*batchAgg{}
	get := func(b string) *batchAgg {
		if agg[b] == nil {
			agg[b] = &batchAgg{}
		}
		return agg[b]
	}
	rows, err := q.QueryContext(ctx, `SELECT a.trash_batch, COUNT(*), COALESCE(SUM(b.size), 0) FROM media_assets a
		LEFT JOIN blobs b ON b.sha256 = a.blob_sha256 WHERE a.trashed_at IS NOT NULL GROUP BY a.trash_batch`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var batch sql.NullString
		var n int
		var s int64
		if err := rows.Scan(&batch, &n, &s); err != nil {
			rows.Close()
			return nil, err
		}
		g := get(batch.String)
		g.assets, g.size = n, s
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, `SELECT trash_batch, COUNT(*) FROM media_folders WHERE trashed_at IS NOT NULL GROUP BY trash_batch`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var batch sql.NullString
		var n int
		if err := rows.Scan(&batch, &n); err != nil {
			rows.Close()
			return nil, err
		}
		get(batch.String).folders = n
	}
	rows.Close()

	var items []trashItem
	frows, err := q.QueryContext(ctx, `SELECT f.id, f.parent_id, f.name, f.trashed_at, COALESCE(f.trash_batch, '') FROM media_folders f
		WHERE f.trashed_at IS NOT NULL
		  AND (f.parent_id IS NULL OR NOT EXISTS (SELECT 1 FROM media_folders p WHERE p.id = f.parent_id AND p.trash_batch = f.trash_batch))
		ORDER BY f.trashed_at DESC, f.id`)
	if err != nil {
		return nil, err
	}
	for frows.Next() {
		var it trashItem
		var parent sql.NullString
		if err := frows.Scan(&it.ID, &parent, &it.Name, &it.TrashedAt, &it.Batch); err != nil {
			frows.Close()
			return nil, err
		}
		it.Type, it.OriginalFolderID = "folder", nullStr(parent)
		if g := agg[it.Batch]; g != nil {
			it.ItemCount, it.FolderCount, it.Size = g.assets, g.folders, g.size
		}
		it.AgeDays = age(it.TrashedAt)
		items = append(items, it)
	}
	frows.Close()
	if err := frows.Err(); err != nil {
		return nil, err
	}
	arows, err := q.QueryContext(ctx, `SELECT a.id, a.name, a.kind, a.trashed_at, a.original_folder_id, COALESCE(a.trash_batch, ''), COALESCE(b.size, 0)
		FROM media_assets a LEFT JOIN blobs b ON b.sha256 = a.blob_sha256
		WHERE a.trashed_at IS NOT NULL AND NOT EXISTS (SELECT 1 FROM media_folders f WHERE f.trash_batch = a.trash_batch)
		ORDER BY a.trashed_at DESC, a.id`)
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		var it trashItem
		var orig sql.NullString
		if err := arows.Scan(&it.ID, &it.Name, &it.Kind, &it.TrashedAt, &orig, &it.Batch, &it.Size); err != nil {
			arows.Close()
			return nil, err
		}
		it.Type, it.OriginalFolderID, it.ItemCount = "asset", nullStr(orig), 1
		it.AgeDays = age(it.TrashedAt)
		items = append(items, it)
	}
	arows.Close()
	if err := arows.Err(); err != nil {
		return nil, err
	}

	// original path and dependencies
	for i := range items {
		it := &items[i]
		it.Usage = []UsageRef{}
		// for a folder the "original folder" is the parent of the batch root
		if it.OriginalPath, err = m.folderPath(ctx, q, it.OriginalFolderID); err != nil {
			return nil, err
		}
		var assetIDs []string
		if it.Type == "asset" {
			assetIDs = []string{it.ID}
		} else {
			arows, err := q.QueryContext(ctx, `SELECT id FROM media_assets WHERE trash_batch = ? AND trashed_at IS NOT NULL`, it.Batch)
			if err != nil {
				return nil, err
			}
			for arows.Next() {
				var id string
				if err := arows.Scan(&id); err != nil {
					arows.Close()
					return nil, err
				}
				assetIDs = append(assetIDs, id)
			}
			arows.Close()
		}
		usage, err := usageOf(ctx, q, assetIDs)
		if err != nil {
			return nil, err
		}
		seen := map[UsageRef]bool{}
		for _, id := range assetIDs {
			for _, u := range usage[id] {
				if u.OwnerType == "product" {
					it.Blocked = true
				}
				if !seen[u] && len(it.Usage) < 50 {
					seen[u] = true
					it.Usage = append(it.Usage, u)
				}
			}
		}
	}
	if items == nil {
		items = []trashItem{}
	}
	return items, nil
}

func (m *Module) trashList(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	items, err := m.trashItems(r.Context(), m.app.DB.R)
	if err != nil {
		return err
	}
	var total int64
	for _, it := range items {
		total += it.Size
	}
	return httpx.OK(w, map[string]any{"items": items, "totalSize": total, "retentionDays": m.app.Settings.Get().TrashRetentionDays})
}

// ---- restore ------------------------------------------------------------------------

// uniqueFolderName returns name, or name with a "(restored)" suffix if a live sibling has it.
func uniqueFolderName(ctx context.Context, q queryer, parent *string, name, exceptID string) (string, error) {
	cand := name
	for i := 1; i <= 100; i++ {
		err := ensureNameFree(ctx, q, parent, cand, exceptID)
		if err == nil {
			return cand, nil
		}
		if !errors.Is(err, errNameTaken) {
			return "", err
		}
		if i == 1 {
			cand = name + " (restored)"
		} else {
			cand = fmt.Sprintf("%s (restored %d)", name, i)
		}
	}
	return "", errNameTaken
}

// ensureFolderLive makes a folder and its trashed ancestors visible again ("recreate
// missing parent folders"). It reports false if the folder row no longer exists. If an
// ancestor was purged meanwhile, the topmost remaining folder moves to the root.
func ensureFolderLive(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	cur := id
	for i := 0; i < 1000; i++ {
		var name string
		var parent, trashed sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT name, parent_id, trashed_at FROM media_folders WHERE id = ?`, cur).Scan(&name, &parent, &trashed)
		if errors.Is(err, sql.ErrNoRows) {
			return i > 0, nil // only the very first lookup decides "does not exist"
		}
		if err != nil {
			return false, err
		}
		if !trashed.Valid {
			return true, nil
		}
		par := nullStr(parent)
		if par != nil {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_folders WHERE id = ?`, *par).Scan(&n); err != nil {
				return false, err
			}
			if n == 0 {
				par = nil // parent was purged: this folder becomes top level
			}
		}
		newName, err := uniqueFolderName(ctx, tx, par, name, cur)
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE media_folders SET trashed_at = NULL, trash_batch = NULL, parent_id = ?, name = ?, updated_at = ? WHERE id = ?`,
			par, newName, db.Now(), cur); err != nil {
			return false, err
		}
		if par == nil {
			return true, nil
		}
		cur = *par
	}
	return false, nil
}

type restoreResult struct {
	Assets  int `json:"assets"`
	Folders int `json:"folders"`
}

func (m *Module) trashRestore(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req idsReq
	if err := httpx.Decode(w, r, 4<<20, &req); err != nil {
		return err
	}
	req.IDs = dedupe(req.IDs)
	if len(req.IDs) == 0 || len(req.IDs) > 5000 {
		return httpx.Invalid("ids", "invalid")
	}
	var res restoreResult
	ctx := r.Context()
	err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		for _, id := range req.IDs {
			if err := m.restoreOne(ctx, tx, p, httpx.ClientIP(r), id, &res); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	m.publishMedia("restored", "", nil)
	m.publishContent("media.restored", req.IDs[0])
	return httpx.OK(w, res)
}

func (m *Module) restoreOne(ctx context.Context, tx *sql.Tx, p *core.Principal, ip, id string, res *restoreResult) error {
	now := db.Now()
	var name string
	var trashed, orig sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT name, trashed_at, original_folder_id FROM media_assets WHERE id = ?`, id).Scan(&name, &trashed, &orig)
	switch {
	case err == nil:
		if !trashed.Valid {
			return httpx.E(http.StatusConflict, "not_in_trash", "the object is not in the trash")
		}
		target := nullStr(orig)
		if target != nil {
			ok, err := ensureFolderLive(ctx, tx, *target)
			if err != nil {
				return err
			}
			if !ok {
				target = nil // the original folder was purged: restore to the root
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE media_assets SET folder_id = ?, original_folder_id = NULL, trashed_at = NULL, trash_batch = NULL, updated_at = ? WHERE id = ?`,
			target, now, id); err != nil {
			return err
		}
		res.Assets++
		return core.Audit(ctx, tx, p, ip, core.AuditEntry{Action: "media.restored", ObjectType: "media", ObjectID: id, ObjectName: name, New: map[string]any{"folderId": target}})
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}

	var batch sql.NullString
	var parent sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT name, parent_id, trashed_at, trash_batch FROM media_folders WHERE id = ?`, id).Scan(&name, &parent, &trashed, &batch)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	if !trashed.Valid {
		return httpx.E(http.StatusConflict, "not_in_trash", "the object is not in the trash")
	}
	par := nullStr(parent)
	if par != nil {
		ok, err := ensureFolderLive(ctx, tx, *par)
		if err != nil {
			return err
		}
		if !ok {
			par = nil
		}
	}
	newName, err := uniqueFolderName(ctx, tx, par, name, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_folders SET parent_id = ?, name = ? WHERE id = ?`, par, newName, id); err != nil {
		return err
	}
	if batch.Valid {
		fr, err := tx.ExecContext(ctx, `UPDATE media_folders SET trashed_at = NULL, trash_batch = NULL, updated_at = ? WHERE trash_batch = ?`, now, batch.String)
		if err != nil {
			return err
		}
		nf, _ := fr.RowsAffected()
		res.Folders += int(nf)
		ar, err := tx.ExecContext(ctx, `UPDATE media_assets SET
			folder_id = CASE WHEN original_folder_id IN (SELECT id FROM media_folders WHERE trashed_at IS NULL) THEN original_folder_id ELSE NULL END,
			original_folder_id = NULL, trashed_at = NULL, trash_batch = NULL, updated_at = ? WHERE trash_batch = ?`, now, batch.String)
		if err != nil {
			return err
		}
		na, _ := ar.RowsAffected()
		res.Assets += int(na)
	} else if _, err := tx.ExecContext(ctx, `UPDATE media_folders SET trashed_at = NULL, trash_batch = NULL, updated_at = ? WHERE id = ?`, now, id); err != nil {
		return err
	}
	return core.Audit(ctx, tx, p, ip, core.AuditEntry{Action: "media.folder_restored", ObjectType: "media_folder", ObjectID: id, ObjectName: newName})
}

// ---- purge --------------------------------------------------------------------------

type blockedItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type purgeTop struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type purgePlan struct {
	AssetIDs   []string      `json:"-"`
	FolderIDs  []string      `json:"-"`
	Top        []purgeTop    `json:"items"`
	Assets     int           `json:"assets"`
	Folders    int           `json:"folders"`
	FreedBytes int64         `json:"bytes"`
	Blocked    []blockedItem `json:"blocked"`
	names      map[string]string
}

// buildPurgePlan resolves trash item ids (assets or folders) into everything that would be
// deleted, what is blocked, and how many bytes the garbage collector could free.
func (m *Module) buildPurgePlan(ctx context.Context, q queryer, reqIDs []string, all bool) (*purgePlan, error) {
	plan := &purgePlan{Top: []purgeTop{}, Blocked: []blockedItem{}, names: map[string]string{}}
	if all {
		items, err := m.trashItems(ctx, q)
		if err != nil {
			return nil, err
		}
		reqIDs = reqIDs[:0:0]
		for _, it := range items {
			reqIDs = append(reqIDs, it.ID)
		}
	}
	seenA, seenF := map[string]bool{}, map[string]bool{}
	for _, id := range reqIDs {
		var name string
		var trashed, batch sql.NullString
		err := q.QueryRowContext(ctx, `SELECT name, trashed_at, trash_batch FROM media_assets WHERE id = ?`, id).Scan(&name, &trashed, &batch)
		if err == nil {
			if !trashed.Valid {
				return nil, httpx.E(http.StatusConflict, "not_in_trash", "the object is not in the trash")
			}
			if !seenA[id] {
				seenA[id] = true
				plan.AssetIDs = append(plan.AssetIDs, id)
				plan.names[id] = name
				plan.Top = append(plan.Top, purgeTop{ID: id, Type: "asset", Name: name, Count: 1})
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		err = q.QueryRowContext(ctx, `SELECT name, trashed_at, trash_batch FROM media_folders WHERE id = ?`, id).Scan(&name, &trashed, &batch)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, httpx.ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if !trashed.Valid {
			return nil, httpx.E(http.StatusConflict, "not_in_trash", "the object is not in the trash")
		}
		top := purgeTop{ID: id, Type: "folder", Name: name}
		if seenF[id] {
			continue
		}
		rows, err := q.QueryContext(ctx, `SELECT id FROM media_folders WHERE trash_batch = ? AND trashed_at IS NOT NULL`, batch.String)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var fid string
			if err := rows.Scan(&fid); err != nil {
				rows.Close()
				return nil, err
			}
			if !seenF[fid] {
				seenF[fid] = true
				plan.FolderIDs = append(plan.FolderIDs, fid)
			}
		}
		rows.Close()
		if !seenF[id] { // batch column empty (should not happen): at least this folder
			seenF[id] = true
			plan.FolderIDs = append(plan.FolderIDs, id)
		}
		if batch.Valid {
			arows, err := q.QueryContext(ctx, `SELECT id, name FROM media_assets WHERE trash_batch = ? AND trashed_at IS NOT NULL`, batch.String)
			if err != nil {
				return nil, err
			}
			for arows.Next() {
				var aid, an string
				if err := arows.Scan(&aid, &an); err != nil {
					arows.Close()
					return nil, err
				}
				if !seenA[aid] {
					seenA[aid] = true
					plan.AssetIDs = append(plan.AssetIDs, aid)
					plan.names[aid] = an
					top.Count++
				}
			}
			arows.Close()
		}
		plan.Top = append(plan.Top, top)
	}

	// blocked: images of products cannot disappear (FK + products would lose their image)
	blocked := map[string]bool{}
	err := chunked(plan.AssetIDs, 400, func(part []string) error {
		rows, err := q.QueryContext(ctx, `SELECT DISTINCT image_asset_id FROM products WHERE image_asset_id IN (`+inPlaceholders(len(part))+`)`, strArgs(part)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			blocked[id] = true
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	for _, id := range plan.AssetIDs {
		if blocked[id] {
			plan.Blocked = append(plan.Blocked, blockedItem{ID: id, Name: plan.names[id], Reason: "product_image"})
		}
	}
	plan.Assets, plan.Folders = len(plan.AssetIDs), len(plan.FolderIDs)

	freed, err := m.freedBytes(ctx, q, func(r refRow) bool {
		return r.Src != refDeployment && r.Src != refFont && seenA[r.AssetID] && !blocked[r.AssetID]
	})
	if err != nil {
		return nil, err
	}
	plan.FreedBytes = freed
	return plan, nil
}

// trashPurge implements POST /api/v1/media/trash/purge[?preview=1].
func (m *Module) trashPurge(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req idsReq
	if err := httpx.Decode(w, r, 4<<20, &req); err != nil {
		return err
	}
	req.IDs = dedupe(req.IDs)
	if !req.All && (len(req.IDs) == 0 || len(req.IDs) > 5000) {
		return httpx.Invalid("ids", "invalid")
	}
	if r.URL.Query().Get("preview") == "1" {
		plan, err := m.buildPurgePlan(r.Context(), m.app.DB.R, req.IDs, req.All)
		if err != nil {
			return err
		}
		return httpx.OK(w, plan)
	}
	plan, err := m.purge(r.Context(), p, httpx.ClientIP(r), req.IDs, req.All, false)
	if err != nil {
		return err
	}
	return httpx.OK(w, plan)
}

// purge deletes trash items for good. Blob files are not touched here; they are removed by
// CollectGarbage, which only deletes what nothing references any more.
func (m *Module) purge(ctx context.Context, p *core.Principal, ip string, reqIDs []string, all, skipBlocked bool) (*purgePlan, error) {
	var plan *purgePlan
	err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		if plan, err = m.buildPurgePlan(ctx, tx, reqIDs, all); err != nil {
			return err
		}
		if len(plan.Blocked) > 0 && !skipBlocked {
			return httpx.E(http.StatusConflict, "asset_in_use", "some files are still used as product images").With("blocked", plan.Blocked)
		}
		skip := map[string]bool{}
		for _, b := range plan.Blocked {
			skip[b.ID] = true
		}
		if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
			return err
		}
		del, err := tx.PrepareContext(ctx, `DELETE FROM media_assets WHERE id = ?`)
		if err != nil {
			return err
		}
		defer del.Close()
		n := 0
		for _, id := range plan.AssetIDs {
			if skip[id] {
				continue
			}
			if _, err := del.ExecContext(ctx, id); err != nil {
				return err
			}
			n++
		}
		for _, id := range plan.FolderIDs {
			// folders that survive (trashed separately earlier) lose their parent link
			if _, err := tx.ExecContext(ctx, `UPDATE media_folders SET parent_id = NULL WHERE parent_id = ?`, id); err != nil {
				return err
			}
		}
		for _, id := range plan.FolderIDs {
			if _, err := tx.ExecContext(ctx, `DELETE FROM media_folders WHERE id = ?`, id); err != nil {
				return err
			}
		}
		for _, t := range plan.Top {
			action := "media.purged"
			if t.Type == "folder" {
				action = "media.folder_purged"
			}
			if err := core.Audit(ctx, tx, p, ip, core.AuditEntry{Action: action, ObjectType: "media", ObjectID: t.ID, ObjectName: t.Name,
				New: map[string]any{"assets": t.Count, "skippedBlocked": len(skip)}}); err != nil {
				return err
			}
		}
		plan.Assets = n
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.publishMedia("purged", "", nil)
	m.publishContent("media.purged", "")
	if _, err := m.CollectGarbage(ctx, false); err != nil {
		slog.Error("media: garbage collection after purge", "err", err)
	}
	return plan, nil
}

// purgeExpired removes trash items older than the retention setting (0 = never).
func (m *Module) purgeExpired(ctx context.Context) {
	days := m.app.Settings.Get().TrashRetentionDays
	if days <= 0 {
		return
	}
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339Nano)
	items, err := m.trashItems(ctx, m.app.DB.R)
	if err != nil {
		slog.Error("media: listing trash for retention", "err", err)
		return
	}
	var due []string
	for _, it := range items {
		if it.TrashedAt < cutoff {
			due = append(due, it.ID)
		}
	}
	if len(due) == 0 {
		return
	}
	plan, err := m.purge(ctx, nil, "system", due, false, true)
	if err != nil {
		slog.Error("media: retention purge", "err", err)
		return
	}
	slog.Info("media: purged expired trash items", "items", len(due), "assets", plan.Assets, "retentionDays", days)
}
