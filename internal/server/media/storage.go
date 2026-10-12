package media

import (
	"context"
	"database/sql"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"opensignage/internal/server/core"
	"opensignage/internal/server/httpx"
)

type diskInfo struct {
	Path        string  `json:"path"`
	TotalBytes  uint64  `json:"totalBytes"`
	UsedBytes   uint64  `json:"usedBytes"`
	FreeBytes   uint64  `json:"freeBytes"`
	FreePercent float64 `json:"freePercent"`
	Level       string  `json:"level"` // ok | warning (<20 % free) | critical (<10 % free)
}

// DiskLevel maps the free share to the warning level.
func DiskLevel(freePercent float64) string {
	switch {
	case freePercent < 10:
		return "critical"
	case freePercent < 20:
		return "warning"
	}
	return "ok"
}

type largeAsset struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Kind     string  `json:"kind"`
	Size     int64   `json:"size"` // originals, revisions and variants
	FolderID *string `json:"folderId"`
	InTrash  bool    `json:"inTrash"`
}

type folderSize struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Path  []string `json:"path"`
	Size  int64    `json:"size"`
	Count int      `json:"count"`
}

type breakdown struct {
	Images         int64 `json:"images"`
	VideoOriginals int64 `json:"videoOriginals"`
	VideoVariants  int64 `json:"videoVariants"`
	Audio          int64 `json:"audio"`
	Thumbnails     int64 `json:"thumbnails"`
	Fonts          int64 `json:"fonts"`
	Trash          int64 `json:"trash"`
	Unreferenced   int64 `json:"unreferenced"` // blobs waiting for garbage collection
	Database       int64 `json:"database"`
	Logs           int64 `json:"logs"`
	Tmp            int64 `json:"tmp"`
}

// category priority: a blob shared by several owners counts once, for the first match.
var categoryOrder = []string{"videoOriginals", "images", "audio", "videoVariants", "fonts", "thumbnails", "trash"}

func categoryOf(kind, variant string, trashed bool) string {
	switch {
	case kind == "font":
		return "fonts"
	case trashed:
		return "trash"
	case variant == "thumb" || variant == "poster":
		return "thumbnails"
	case kind == KindImage:
		return "images"
	case kind == KindAudio:
		return "audio"
	case kind == KindVideo && variant == "orig":
		return "videoOriginals"
	}
	return "videoVariants"
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // vanishing temp files are normal
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func fileSize(path string) int64 {
	if st, err := os.Stat(path); err == nil {
		return st.Size()
	}
	return 0
}

// storage serves GET /api/v1/storage (admin).
func (m *Module) storage(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	ctx := r.Context()
	out := map[string]any{}

	total, free, err := m.diskFn(m.dataDir)
	d := diskInfo{Path: m.dataDir, Level: "ok"}
	if err != nil {
		slog.Warn("media: reading disk usage", "err", err)
	} else {
		d.TotalBytes, d.FreeBytes = total, free
		d.UsedBytes = total - min(total, free)
		if total > 0 {
			d.FreePercent = float64(free) * 100 / float64(total)
		}
		d.Level = DiskLevel(d.FreePercent)
	}
	out["disk"] = d

	// breakdown by distinct blob
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT x.sha, b.size, x.kind, x.variant, x.trashed FROM (
		  SELECT r.blob_sha256 AS sha, a.kind AS kind, 'orig' AS variant, (a.trashed_at IS NOT NULL) AS trashed
		    FROM media_revisions r JOIN media_assets a ON a.id = r.asset_id
		  UNION ALL
		  SELECT v.blob_sha256, a.kind, v.variant, (a.trashed_at IS NOT NULL)
		    FROM media_variants v JOIN media_assets a ON a.id = v.asset_id
		  UNION ALL
		  SELECT blob_sha256, 'font', 'font', 0 FROM font_faces
		) x JOIN blobs b ON b.sha256 = x.sha`)
	if err != nil {
		return err
	}
	best := map[string]struct {
		prio int
		cat  string
		size int64
	}{}
	prio := map[string]int{}
	for i, c := range categoryOrder {
		prio[c] = i
	}
	for rows.Next() {
		var sha, kind, variant string
		var size int64
		var trashed int
		if err := rows.Scan(&sha, &size, &kind, &variant, &trashed); err != nil {
			rows.Close()
			return err
		}
		cat := categoryOf(kind, variant, trashed == 1)
		if cur, ok := best[sha]; !ok || prio[cat] < cur.prio {
			best[sha] = struct {
				prio int
				cat  string
				size int64
			}{prio[cat], cat, size}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var bd breakdown
	var classified int64
	for _, v := range best {
		classified += v.size
		switch v.cat {
		case "images":
			bd.Images += v.size
		case "videoOriginals":
			bd.VideoOriginals += v.size
		case "videoVariants":
			bd.VideoVariants += v.size
		case "audio":
			bd.Audio += v.size
		case "thumbnails":
			bd.Thumbnails += v.size
		case "fonts":
			bd.Fonts += v.size
		case "trash":
			bd.Trash += v.size
		}
	}
	var blobsTotal int64
	if err := m.app.DB.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM blobs`).Scan(&blobsTotal); err != nil {
		return err
	}
	bd.Unreferenced = max(0, blobsTotal-classified)
	dbPath := m.app.DB.Path
	bd.Database = fileSize(dbPath) + fileSize(dbPath+"-wal") + fileSize(dbPath+"-shm")
	bd.Logs = dirSize(filepath.Join(m.dataDir, "logs"))
	bd.Tmp = dirSize(m.tmpDir())
	out["breakdown"] = bd

	// per-asset footprint (distinct blobs over all revisions and variants)
	arows, err := m.app.DB.R.QueryContext(ctx, `SELECT a.id, a.name, a.kind, a.folder_id, a.trashed_at IS NOT NULL, SUM(b.size) FROM (
		  SELECT asset_id, blob_sha256 FROM media_revisions UNION SELECT asset_id, blob_sha256 FROM media_variants
		) x JOIN media_assets a ON a.id = x.asset_id JOIN blobs b ON b.sha256 = x.blob_sha256 GROUP BY a.id`)
	if err != nil {
		return err
	}
	var assets []largeAsset
	for arows.Next() {
		var l largeAsset
		var folder sql.NullString
		var trashed int
		if err := arows.Scan(&l.ID, &l.Name, &l.Kind, &folder, &trashed, &l.Size); err != nil {
			arows.Close()
			return err
		}
		l.FolderID, l.InTrash = nullStr(folder), trashed == 1
		assets = append(assets, l)
	}
	arows.Close()
	if err := arows.Err(); err != nil {
		return err
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Size > assets[j].Size })
	out["largest"] = assets[:min(20, len(assets))]
	if len(assets) == 0 {
		out["largest"] = []largeAsset{}
	}

	// per folder (whole subtree), live assets only
	folders, err := liveFolders(ctx, m.app.DB.R)
	if err != nil {
		return err
	}
	byID := map[string]folderRec{}
	for _, f := range folders {
		byID[f.ID] = f
	}
	sizeOf, countOf := map[string]int64{}, map[string]int{}
	rootSize, rootCount := int64(0), 0
	for _, a := range assets {
		if a.InTrash {
			continue
		}
		cur := a.FolderID
		if cur == nil {
			rootSize += a.Size
			rootCount++
			continue
		}
		for i := 0; cur != nil && i < 256; i++ {
			sizeOf[*cur] += a.Size
			countOf[*cur]++
			cur = byID[*cur].ParentID
		}
	}
	fsz := make([]folderSize, 0, len(folders))
	for _, f := range folders {
		var path []string
		cur := &f.ID
		for i := 0; cur != nil && i < 64; i++ {
			rec, ok := byID[*cur]
			if !ok {
				break
			}
			path = append([]string{rec.Name}, path...)
			cur = rec.ParentID
		}
		fsz = append(fsz, folderSize{ID: f.ID, Name: f.Name, Path: path, Size: sizeOf[f.ID], Count: countOf[f.ID]})
	}
	sort.Slice(fsz, func(i, j int) bool { return fsz[i].Size > fsz[j].Size })
	out["folders"] = fsz[:min(100, len(fsz))]
	out["root"] = map[string]any{"size": rootSize, "count": rootCount}
	return httpx.OK(w, out)
}

// ---- unused media and cleanup -----------------------------------------------------------

type unusedAsset struct {
	assetView
	VariantBytes int64 `json:"variantBytes"`
	VariantCount int   `json:"variantCount"`
}

type oldRevision struct {
	AssetID   string `json:"assetId"`
	Name      string `json:"name"`
	Revision  int    `json:"revision"`
	Current   int    `json:"currentRevision"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"createdAt"`
}

// keepVariants are never removed by the variant cleanup: the thumbnail, the (possibly
// downscaled) original and what is needed to play the asset (v1080, or the audio file).
// Everything else (v1080h, v720, poster, i512, i1920) is an optimisation that reprocess
// re-creates.
const keepVariantsSQL = `('thumb', 'orig', 'v1080', 'a')`

var keepVariants = map[string]bool{"thumb": true, "orig": true, "v1080": true, "a": true}

// liveRefSQL limits content_refs rows (alias c) to owners users can still see: references of
// deleted (archived) presentations and playlists do not count. Archived products do – they
// stay visible in the product archive.
const liveRefSQL = `NOT EXISTS (SELECT 1 FROM presentations dp WHERE c.owner_type LIKE 'presentation%' AND dp.id = c.owner_id AND dp.archived_at IS NOT NULL)
	AND NOT EXISTS (SELECT 1 FROM playlists dl WHERE c.owner_type LIKE 'playlist%' AND dl.id = c.owner_id AND dl.archived_at IS NOT NULL)`

const notUsedSQL = `NOT EXISTS (SELECT 1 FROM content_refs c WHERE c.asset_id = a.id AND ` + liveRefSQL + `)
	AND NOT EXISTS (SELECT 1 FROM products p WHERE p.image_asset_id = a.id)`

// unusedList serves GET /api/v1/media/unused?olderThanDays=&kind=: assets nothing uses,
// plus old revisions.
func (m *Module) unusedList(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	ctx := r.Context()
	q := r.URL.Query()
	where, args := []string{"a.trashed_at IS NULL", notUsedSQL}, []any{}
	cutoff := ""
	if q.Has("olderThanDays") {
		days := httpx.QueryInt(r, "olderThanDays", -1, -1, 36500)
		if days < 0 {
			return httpx.Invalid("olderThanDays", "invalid")
		}
		cutoff = time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339Nano)
		where, args = append(where, "a.updated_at < ?"), append(args, cutoff)
	}
	if q.Has("kind") {
		switch k := q.Get("kind"); k {
		case KindImage, KindVideo, KindAudio:
			where, args = append(where, "a.kind = ?"), append(args, k)
		default:
			return httpx.Invalid("kind", "invalid")
		}
	}
	assets, err := m.loadAssets(ctx, m.app.DB.R, `WHERE `+strings.Join(where, " AND ")+` ORDER BY b.size DESC, a.id LIMIT 2000`, args...)
	if err != nil {
		return err
	}
	list := make([]unusedAsset, len(assets))
	idx := map[string]int{}
	idList := make([]string, len(assets))
	for i, a := range assets {
		list[i].assetView = a
		idx[a.ID], idList[i] = i, a.ID
	}
	err = chunked(idList, 400, func(part []string) error {
		rows, err := m.app.DB.R.QueryContext(ctx, `SELECT v.asset_id, COUNT(*), COALESCE(SUM(b.size), 0) FROM media_variants v
			JOIN blobs b ON b.sha256 = v.blob_sha256 WHERE v.variant NOT IN `+keepVariantsSQL+` AND v.asset_id IN (`+inPlaceholders(len(part))+`) GROUP BY v.asset_id`, strArgs(part)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var n int
			var s int64
			if err := rows.Scan(&id, &n, &s); err != nil {
				return err
			}
			list[idx[id]].VariantCount, list[idx[id]].VariantBytes = n, s
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}

	revWhere, revArgs := "r.revision < a.revision AND r.revision <> a.ready_revision AND a.trashed_at IS NULL", []any{}
	if cutoff != "" {
		revWhere, revArgs = revWhere+" AND r.created_at < ?", append(revArgs, cutoff)
	}
	if q.Has("kind") {
		revWhere, revArgs = revWhere+" AND a.kind = ?", append(revArgs, q.Get("kind"))
	}
	rrows, err := m.app.DB.R.QueryContext(ctx, `SELECT r.asset_id, a.name, r.revision, a.revision, COALESCE(b.size, 0), r.created_at FROM media_revisions r
		JOIN media_assets a ON a.id = r.asset_id LEFT JOIN blobs b ON b.sha256 = r.blob_sha256 WHERE `+revWhere+` ORDER BY b.size DESC LIMIT 2000`, revArgs...)
	if err != nil {
		return err
	}
	defer rrows.Close()
	old := []oldRevision{}
	for rrows.Next() {
		var o oldRevision
		if err := rrows.Scan(&o.AssetID, &o.Name, &o.Revision, &o.Current, &o.Size, &o.CreatedAt); err != nil {
			return err
		}
		old = append(old, o)
	}
	if err := rrows.Err(); err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"assets": list, "oldRevisions": old})
}

type cleanupPlan struct {
	Assets   int           `json:"assets"`
	Items    int           `json:"items"` // variants or revisions
	Bytes    int64         `json:"bytes"`
	Blocked  []blockedItem `json:"blocked"`
	assetIDs []string
}

// deleteVariants implements POST /media/unused/variants/delete[?preview=1]: it removes
// all variants except the thumbnail of unused assets; reprocess re-creates them.
func (m *Module) deleteVariants(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req idsReq
	if err := httpx.Decode(w, r, 4<<20, &req); err != nil {
		return err
	}
	req.IDs = dedupe(req.IDs)
	if len(req.IDs) == 0 || len(req.IDs) > 5000 {
		return httpx.Invalid("ids", "invalid")
	}
	preview := r.URL.Query().Get("preview") == "1"
	var plan *cleanupPlan
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var err error
		if plan, err = m.planCleanup(r.Context(), tx, req.IDs, true); err != nil {
			return err
		}
		if preview {
			return nil
		}
		for _, id := range plan.assetIDs {
			if _, err := tx.ExecContext(r.Context(), `DELETE FROM media_variants WHERE asset_id = ? AND variant NOT IN `+keepVariantsSQL, id); err != nil {
				return err
			}
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "media.variants_deleted", ObjectType: "media",
			ObjectName: "variants", New: map[string]any{"assets": plan.Assets, "variants": plan.Items, "ids": firstN(plan.assetIDs, 100)}})
	})
	if err != nil {
		return err
	}
	if !preview && plan.Assets > 0 {
		m.afterCleanup(r.Context(), plan.assetIDs)
	}
	return httpx.OK(w, plan)
}

// deleteOldRevisions implements POST /media/old-revisions/delete[?preview=1].
func (m *Module) deleteOldRevisions(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req idsReq
	if err := httpx.Decode(w, r, 4<<20, &req); err != nil {
		return err
	}
	req.IDs = dedupe(req.IDs)
	if len(req.IDs) == 0 || len(req.IDs) > 5000 {
		return httpx.Invalid("ids", "invalid")
	}
	preview := r.URL.Query().Get("preview") == "1"
	var plan *cleanupPlan
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var err error
		if plan, err = m.planCleanup(r.Context(), tx, req.IDs, false); err != nil {
			return err
		}
		if preview {
			return nil
		}
		for _, id := range plan.assetIDs {
			// never the current revision and never the one screens are using (ready_revision)
			if _, err := tx.ExecContext(r.Context(), `DELETE FROM media_variants WHERE asset_id = ?
				AND revision < (SELECT revision FROM media_assets WHERE id = ?)
				AND revision <> (SELECT ready_revision FROM media_assets WHERE id = ?)`, id, id, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(r.Context(), `DELETE FROM media_revisions WHERE asset_id = ?
				AND revision < (SELECT revision FROM media_assets WHERE id = ?)
				AND revision <> (SELECT ready_revision FROM media_assets WHERE id = ?)`, id, id, id); err != nil {
				return err
			}
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "media.revisions_deleted", ObjectType: "media",
			ObjectName: "revisions", New: map[string]any{"assets": plan.Assets, "revisions": plan.Items, "ids": firstN(plan.assetIDs, 100)}})
	})
	if err != nil {
		return err
	}
	if !preview && plan.Assets > 0 {
		m.afterCleanup(r.Context(), plan.assetIDs)
	}
	return httpx.OK(w, plan)
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (m *Module) afterCleanup(ctx context.Context, assetIDs []string) {
	m.publishMedia("cleaned", "", nil)
	m.publishContent("media.cleaned", "") // deployments re-evaluate which variants exist
	if _, err := m.CollectGarbage(ctx, false); err != nil {
		slog.Error("media: garbage collection after cleanup", "err", err)
	}
}

// planCleanup selects what a cleanup would delete. For variants only unused assets are
// eligible (the rest is reported as blocked); old revisions of any live asset may go.
func (m *Module) planCleanup(ctx context.Context, q queryer, reqIDs []string, variants bool) (*cleanupPlan, error) {
	plan := &cleanupPlan{Blocked: []blockedItem{}}
	eligible := map[string]bool{}
	names := map[string]string{}
	err := chunked(reqIDs, 400, func(part []string) error {
		cond := ""
		if variants {
			cond = " AND " + notUsedSQL
		}
		rows, err := q.QueryContext(ctx, `SELECT a.id, a.name FROM media_assets a WHERE a.trashed_at IS NULL AND a.id IN (`+inPlaceholders(len(part))+`)`+cond, strArgs(part)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				return err
			}
			eligible[id], names[id] = true, name
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	for _, id := range reqIDs {
		if eligible[id] {
			plan.assetIDs = append(plan.assetIDs, id)
		} else {
			plan.Blocked = append(plan.Blocked, blockedItem{ID: id, Name: names[id], Reason: "in_use_or_missing"})
		}
	}
	// count items and what the collector could free afterwards
	cur, ready := map[string]int{}, map[string]int{}
	err = chunked(plan.assetIDs, 400, func(part []string) error {
		rows, err := q.QueryContext(ctx, `SELECT id, revision, ready_revision FROM media_assets WHERE id IN (`+inPlaceholders(len(part))+`)`, strArgs(part)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var rev, rdy int
			if err := rows.Scan(&id, &rev, &rdy); err != nil {
				return err
			}
			cur[id], ready[id] = rev, rdy
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	removed := func(r refRow) bool {
		if !eligible[r.AssetID] {
			return false
		}
		if variants {
			return r.Src == refVariant && !keepVariants[r.Variant]
		}
		return (r.Src == refRevision || r.Src == refVariant) && r.Revision < cur[r.AssetID] && r.Revision != ready[r.AssetID]
	}
	refs, err := loadRefs(ctx, q)
	if err != nil {
		return nil, err
	}
	touched := map[string]bool{}
	for _, r := range refs {
		if removed(r) && cur[r.AssetID] > 0 {
			if variants || r.Src == refRevision {
				plan.Items++
			}
			touched[r.AssetID] = true
		}
	}
	ids := plan.assetIDs[:0:0]
	for _, id := range plan.assetIDs {
		if touched[id] {
			ids = append(ids, id)
		}
	}
	plan.assetIDs, plan.Assets = ids, len(ids)
	if plan.Bytes, err = m.freedBytes(ctx, q, removed); err != nil {
		return nil, err
	}
	return plan, nil
}
