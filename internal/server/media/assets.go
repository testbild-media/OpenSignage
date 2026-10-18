package media

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
)

// assetView is the API representation of an asset.
type assetView struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Kind     string  `json:"kind"`
	FolderID *string `json:"folderId"`
	Revision int     `json:"revision"`
	// ReadyRevision is the newest fully processed revision (0 = none yet). While a
	// replacement or a reprocess runs, screens keep using it.
	ReadyRevision int             `json:"readyRevision"`
	Status        string          `json:"status"`
	Error         string          `json:"error,omitempty"`
	Meta          json.RawMessage `json:"meta"`
	Size          int64           `json:"size"`
	Mime          string          `json:"mime"`
	SHA256        string          `json:"sha256"`
	Variants      []string        `json:"variants"`
	Progress      *progressInfo   `json:"progress,omitempty"`
	CreatedAt     string          `json:"createdAt"`
	UpdatedAt     string          `json:"updatedAt"`
	TrashedAt     *string         `json:"trashedAt,omitempty"`
	UploadedBy    string          `json:"uploadedBy,omitempty"` // user id
}

const assetCols = `a.id, a.folder_id, a.name, a.kind, a.revision, a.ready_revision, COALESCE(a.blob_sha256, ''), a.status, a.error, a.meta,
	a.created_at, a.updated_at, a.trashed_at, COALESCE(b.size, 0), COALESCE(b.mime, ''), COALESCE(a.uploaded_by, '')`

const assetFrom = ` FROM media_assets a LEFT JOIN blobs b ON b.sha256 = a.blob_sha256 `

// loadAssets runs a query over assetCols/assetFrom with the given tail ("WHERE … ORDER BY …")
// and fills in variant names and live progress.
func (m *Module) loadAssets(ctx context.Context, q queryer, tail string, args ...any) ([]assetView, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+assetCols+assetFrom+tail, args...)
	if err != nil {
		return nil, err
	}
	out := []assetView{}
	for rows.Next() {
		var a assetView
		var folder, trashed sql.NullString
		var meta string
		if err := rows.Scan(&a.ID, &folder, &a.Name, &a.Kind, &a.Revision, &a.ReadyRevision, &a.SHA256, &a.Status, &a.Error, &meta,
			&a.CreatedAt, &a.UpdatedAt, &trashed, &a.Size, &a.Mime, &a.UploadedBy); err != nil {
			rows.Close()
			return nil, err
		}
		a.FolderID, a.TrashedAt, a.Meta = nullStr(folder), nullStr(trashed), rawJSON(meta)
		a.Variants = []string{}
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	idx := make(map[string]int, len(out))
	idList := make([]string, len(out))
	for i, a := range out {
		idx[a.ID], idList[i] = i, a.ID
		if pi, ok := m.progress.Load(a.ID); ok {
			v := pi.(progressInfo)
			out[i].Progress = &v
		}
	}
	err = chunked(idList, 400, func(part []string) error {
		vr, err := q.QueryContext(ctx, `SELECT asset_id, revision, variant FROM media_variants WHERE asset_id IN (`+inPlaceholders(len(part))+`) ORDER BY variant`, strArgs(part)...)
		if err != nil {
			return err
		}
		defer vr.Close()
		for vr.Next() {
			var id, v string
			var rev int
			if err := vr.Scan(&id, &rev, &v); err != nil {
				return err
			}
			if i, ok := idx[id]; ok && out[i].Revision == rev {
				out[i].Variants = append(out[i].Variants, v)
			}
		}
		return vr.Err()
	})
	return out, err
}

func (m *Module) assetByID(ctx context.Context, id string) (*assetView, error) {
	list, err := m.loadAssets(ctx, m.app.DB.R, `WHERE a.id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, httpx.ErrNotFound
	}
	return &list[0], nil
}

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func jsonUnmarshalMap(s string, dst *map[string]any) error {
	return json.Unmarshal([]byte(s), dst)
}

// ---- details --------------------------------------------------------------------------

type revisionView struct {
	Revision  int             `json:"revision"`
	Size      int64           `json:"size"`
	Mime      string          `json:"mime"`
	SHA256    string          `json:"sha256"`
	Meta      json.RawMessage `json:"meta"`
	CreatedAt string          `json:"createdAt"`
	Current   bool            `json:"current"`
}

type variantView struct {
	Variant  string `json:"variant"`
	Revision int    `json:"revision"`
	Size     int64  `json:"size"`
	Mime     string `json:"mime"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	SHA256   string `json:"sha256"`
}

// UsageRef tells which content uses an asset.
type UsageRef struct {
	OwnerType string `json:"ownerType"`
	OwnerID   string `json:"ownerId"`
	OwnerName string `json:"ownerName"`
}

type jobView struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Revision  int    `json:"revision"`
	Attempts  int    `json:"attempts"`
	Error     string `json:"error,omitempty"`
	UpdatedAt string `json:"updatedAt"`
}

type crumb struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type assetDetails struct {
	*assetView
	Revisions []revisionView `json:"revisions"`
	Variants  []variantView  `json:"variantInfo"`
	Usage     []UsageRef     `json:"usage"`
	Job       *jobView       `json:"job"`
	Path      []crumb        `json:"path"`
}

// usageOf lists the content that references the given assets (with owner names); deleted
// presentations and playlists are left out (liveRefSQL).
func usageOf(ctx context.Context, q queryer, assetIDs []string) (map[string][]UsageRef, error) {
	out := map[string][]UsageRef{}
	err := chunked(assetIDs, 400, func(part []string) error {
		ph := inPlaceholders(len(part))
		args := append(strArgs(part), strArgs(part)...)
		rows, err := q.QueryContext(ctx, `
			SELECT c.asset_id, c.owner_type, c.owner_id, COALESCE(p.name, pl.name, pr.name, '')
			  FROM content_refs c
			  LEFT JOIN presentations p ON c.owner_type LIKE 'presentation%' AND p.id = c.owner_id
			  LEFT JOIN playlists pl ON c.owner_type LIKE 'playlist%' AND pl.id = c.owner_id
			  LEFT JOIN products pr ON c.owner_type = 'product' AND pr.id = c.owner_id
			 WHERE c.asset_id IN (`+ph+`) AND `+liveRefSQL+`
			UNION
			SELECT image_asset_id, 'product', id, name FROM products WHERE image_asset_id IN (`+ph+`)
			ORDER BY 1, 2, 4`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var u UsageRef
			if err := rows.Scan(&id, &u.OwnerType, &u.OwnerID, &u.OwnerName); err != nil {
				return err
			}
			out[id] = append(out[id], u)
		}
		return rows.Err()
	})
	return out, err
}

func (m *Module) folderPath(ctx context.Context, q queryer, folderID *string) ([]crumb, error) {
	path := []crumb{}
	cur := folderID
	for i := 0; cur != nil && i < 64; i++ {
		var name string
		var parent sql.NullString
		err := q.QueryRowContext(ctx, `SELECT name, parent_id FROM media_folders WHERE id = ?`, *cur).Scan(&name, &parent)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		path = append([]crumb{{ID: *cur, Name: name}}, path...)
		cur = nullStr(parent)
	}
	return path, nil
}

// details serves GET /api/v1/media/{id}.
func (m *Module) details(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	ctx := r.Context()
	a, err := m.assetByID(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	d := assetDetails{assetView: a, Revisions: []revisionView{}, Variants: []variantView{}, Usage: []UsageRef{}}

	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT r.revision, r.blob_sha256, r.meta, r.created_at, COALESCE(b.size, 0), COALESCE(b.mime, '')
		FROM media_revisions r LEFT JOIN blobs b ON b.sha256 = r.blob_sha256 WHERE r.asset_id = ? ORDER BY r.revision DESC`, a.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var rv revisionView
		var meta string
		if err := rows.Scan(&rv.Revision, &rv.SHA256, &meta, &rv.CreatedAt, &rv.Size, &rv.Mime); err != nil {
			rows.Close()
			return err
		}
		rv.Meta, rv.Current = rawJSON(meta), rv.Revision == a.Revision
		d.Revisions = append(d.Revisions, rv)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	vrows, err := m.app.DB.R.QueryContext(ctx, `SELECT v.variant, v.revision, v.blob_sha256, v.width, v.height, COALESCE(b.size, 0), COALESCE(b.mime, '')
		FROM media_variants v LEFT JOIN blobs b ON b.sha256 = v.blob_sha256 WHERE v.asset_id = ? ORDER BY v.revision DESC, v.variant`, a.ID)
	if err != nil {
		return err
	}
	for vrows.Next() {
		var v variantView
		if err := vrows.Scan(&v.Variant, &v.Revision, &v.SHA256, &v.Width, &v.Height, &v.Size, &v.Mime); err != nil {
			vrows.Close()
			return err
		}
		d.Variants = append(d.Variants, v)
	}
	vrows.Close()
	if err := vrows.Err(); err != nil {
		return err
	}

	usage, err := usageOf(ctx, m.app.DB.R, []string{a.ID})
	if err != nil {
		return err
	}
	if u := usage[a.ID]; u != nil {
		d.Usage = u
	}
	var j jobView
	err = m.app.DB.R.QueryRowContext(ctx, `SELECT id, state, revision, attempts, error, updated_at FROM media_jobs WHERE asset_id = ? ORDER BY id DESC LIMIT 1`, a.ID).
		Scan(&j.ID, &j.State, &j.Revision, &j.Attempts, &j.Error, &j.UpdatedAt)
	switch {
	case err == nil:
		d.Job = &j
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if d.Path, err = m.folderPath(ctx, m.app.DB.R, a.FolderID); err != nil {
		return err
	}
	return httpx.OK(w, d)
}

// ---- rename / move -----------------------------------------------------------------------

type patchAssetReq struct {
	Name     *string `json:"name"`
	FolderID optStr  `json:"folderId"`
}

// patchAsset renames and/or moves an asset. The id, the revision and the content stay
// untouched, so nothing is rebuilt and no client downloads anything.
func (m *Module) patchAsset(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req patchAssetReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	var name string
	if req.Name != nil {
		n, ok := cleanLabel(*req.Name)
		if !ok {
			return httpx.Invalid("name", "invalid")
		}
		name = n
	}
	if req.FolderID.Set && req.FolderID.Val != nil && *req.FolderID.Val == "" {
		return httpx.Invalid("folderId", "empty") // null moves to the root, "" is a client bug
	}
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		a, err := loadAssetRec(r.Context(), tx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if a.TrashedAt != nil {
			return httpx.E(http.StatusConflict, "in_trash", "the asset is in the trash")
		}
		newName, newFolder := a.Name, a.FolderID
		if req.Name != nil {
			newName = name
		}
		if req.FolderID.Set {
			newFolder = req.FolderID.Val
			if newFolder != nil {
				var n int
				if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM media_folders WHERE id = ? AND trashed_at IS NULL`, *newFolder).Scan(&n); err != nil {
					return err
				}
				if n == 0 {
					return httpx.Invalid("folderId", "unknown_folder")
				}
			}
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE media_assets SET name = ?, folder_id = ?, updated_at = ? WHERE id = ?`,
			newName, newFolder, db.Now(), id); err != nil {
			return err
		}
		action := "media.renamed"
		if req.FolderID.Set && !strEq(a.FolderID, newFolder) {
			action = "media.moved"
			if req.Name != nil && newName != a.Name {
				action = "media.renamed_moved"
			}
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: action, ObjectType: "media", ObjectID: id, ObjectName: newName,
			Old: map[string]any{"name": a.Name, "folderId": a.FolderID}, New: map[string]any{"name": newName, "folderId": newFolder}})
	})
	if err != nil {
		return err
	}
	m.publishMedia("updated", id, nil) // deliberately no "content" event: nothing a screen shows has changed
	v, err := m.assetByID(r.Context(), id)
	if err != nil {
		return err
	}
	return httpx.OK(w, v)
}

func strEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ---- variant resolution (shared with other modules) ------------------------------------------

// ErrNoVariant means the asset has no such variant (yet).
var ErrNoVariant = errors.New("media: variant not available")

// BlobRef locates the stored file of an asset variant.
type BlobRef struct {
	SHA256   string
	Size     int64
	Mime     string
	Variant  string // the variant that was actually resolved (may be a fallback)
	Revision int
	Width    int
	Height   int
}

// variantChain lists the variants tried in order for a requested variant. "orig" is the
// uploaded original, or the downscaled original for very large images.
func variantChain(variant string) []string {
	switch variant {
	case "orig":
		return []string{"orig"}
	case "i1920":
		return []string{"i1920", "orig"}
	case "i512":
		return []string{"i512", "i1920", "orig"}
	case "v1080":
		return []string{"v1080"}
	case "v1080h": // only made above 30 fps; otherwise v1080 has at most 30 fps anyway
		return []string{"v1080h", "v1080"}
	case "v720":
		return []string{"v720", "v1080"}
	case "thumb", "poster", "a":
		return []string{variant}
	case "display": // best variant for showing on screens; resolved per kind below
		return []string{"display"}
	}
	return nil
}

// displayChain is the "display" variant for the editor and previews.
func displayChain(kind string) []string {
	switch kind {
	case "image":
		return []string{"i1920", "orig"}
	case "video":
		return []string{"v1080", "v720"}
	default:
		return []string{"a"}
	}
}

// ValidVariant reports whether name is a known variant name.
func ValidVariant(name string) bool { return variantChain(name) != nil }

// ResolveVariant picks the blob for an asset variant. rev 0 means the current revision.
// Fallbacks: i512 → i1920 → orig, v1080h → v1080, v720 → v1080, and SVG images use the SVG
// for thumbs.
// Other modules (deployment builder, device blob endpoint) use it so that variant rules
// live in one place. It returns ErrNoVariant when nothing suitable exists.
func ResolveVariant(ctx context.Context, q queryer, assetID string, rev int, variant string) (BlobRef, error) {
	chain := variantChain(variant)
	if chain == nil {
		return BlobRef{}, ErrNoVariant
	}
	var kind, status string
	var cur, ready int
	var curSHA sql.NullString
	err := q.QueryRowContext(ctx, `SELECT kind, revision, status, ready_revision, blob_sha256 FROM media_assets WHERE id = ?`, assetID).
		Scan(&kind, &cur, &status, &ready, &curSHA)
	if errors.Is(err, sql.ErrNoRows) {
		return BlobRef{}, ErrNoVariant
	}
	if err != nil {
		return BlobRef{}, err
	}
	explicit := rev > 0
	if rev <= 0 {
		rev = cur
	}
	// The upload itself ("orig") is addressed exactly. Derived variants come from the newest
	// revision that finished processing while a replacement or a reprocess is running.
	useRev := rev
	if variant != "orig" && !explicit && status != "ready" && ready > 0 {
		useRev = ready
	}
	if variant == "display" {
		chain = displayChain(kind)
	}
	for _, name := range chain {
		var ref BlobRef
		var err error
		if name == "orig" {
			// downscaled original (images above 3840 px), else the uploaded revision
			err = q.QueryRowContext(ctx, `SELECT v.blob_sha256, COALESCE(b.size, 0), COALESCE(b.mime, ''), v.width, v.height FROM media_variants v
				LEFT JOIN blobs b ON b.sha256 = v.blob_sha256 WHERE v.asset_id = ? AND v.revision = ? AND v.variant = 'orig'`, assetID, useRev).
				Scan(&ref.SHA256, &ref.Size, &ref.Mime, &ref.Width, &ref.Height)
			if errors.Is(err, sql.ErrNoRows) {
				err = q.QueryRowContext(ctx, `SELECT r.blob_sha256, COALESCE(b.size, 0), COALESCE(b.mime, '') FROM media_revisions r
					LEFT JOIN blobs b ON b.sha256 = r.blob_sha256 WHERE r.asset_id = ? AND r.revision = ?`, assetID, useRev).
					Scan(&ref.SHA256, &ref.Size, &ref.Mime)
			}
		} else {
			err = q.QueryRowContext(ctx, `SELECT v.blob_sha256, COALESCE(b.size, 0), COALESCE(b.mime, ''), v.width, v.height FROM media_variants v
				LEFT JOIN blobs b ON b.sha256 = v.blob_sha256 WHERE v.asset_id = ? AND v.revision = ? AND v.variant = ?`, assetID, useRev, name).
				Scan(&ref.SHA256, &ref.Size, &ref.Mime, &ref.Width, &ref.Height)
		}
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return BlobRef{}, err
		}
		ref.Variant, ref.Revision = name, useRev
		return ref, nil
	}
	if variant == "thumb" && kind == KindImage { // SVG: no raster thumbnail, the image itself is small
		return ResolveVariant(ctx, q, assetID, rev, "orig")
	}
	return BlobRef{}, ErrNoVariant
}

// serveFile implements GET /api/v1/media/{id}/file?variant=&rev=.
func (m *Module) serveFile(w http.ResponseWriter, r *http.Request, id string) error {
	q := r.URL.Query()
	variant := "orig"
	if q.Has("variant") {
		variant = q.Get("variant")
		if !ValidVariant(variant) {
			return httpx.Invalid("variant", "invalid")
		}
	}
	rev, hasRev := 0, q.Get("rev") != ""
	if hasRev {
		n, err := strconv.Atoi(q.Get("rev"))
		if err != nil || n < 1 {
			return httpx.Invalid("rev", "invalid")
		}
		rev = n
	}
	ctx := r.Context()
	ref, err := ResolveVariant(ctx, m.app.DB.R, id, rev, variant)
	if errors.Is(err, ErrNoVariant) && !hasRev && (variant == "thumb" || variant == "poster") {
		// while a replacement is being processed, keep showing the previous preview
		var prev int
		if qerr := m.app.DB.R.QueryRowContext(ctx, `SELECT MAX(revision) FROM media_variants WHERE asset_id = ? AND variant = ?`, id, variant).Scan(&prev); qerr == nil && prev > 0 {
			ref, err = ResolveVariant(ctx, m.app.DB.R, id, prev, variant)
		}
	}
	if errors.Is(err, ErrNoVariant) {
		var n int
		if qerr := m.app.DB.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_assets WHERE id = ?`, id).Scan(&n); qerr == nil && n == 0 {
			return httpx.ErrNotFound
		}
		return httpx.E(http.StatusNotFound, "variant_not_found", "this variant does not exist (yet)")
	}
	if err != nil {
		return err
	}
	path := m.BlobPath(ref.SHA256)
	f, err := os.Open(path)
	if err != nil {
		if isNotExist(err) {
			slog.Error("media: blob file missing on disk", "asset", id, "variant", variant, "sha256", ref.SHA256)
			return httpx.E(http.StatusNotFound, "file_missing", "the stored file is missing")
		}
		return err
	}
	defer f.Close()
	var name string
	_ = m.app.DB.R.QueryRowContext(ctx, `SELECT name FROM media_assets WHERE id = ?`, id).Scan(&name)

	h := w.Header()
	h.Set("Content-Type", firstNonEmpty(ref.Mime, "application/octet-stream"))
	h.Set("ETag", `"`+ref.SHA256+`"`)
	if hasRev {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "private, no-cache")
	}
	disp := "inline"
	if q.Get("download") == "1" {
		disp = "attachment"
	}
	h.Set("Content-Disposition", disp+"; filename*=UTF-8''"+url.PathEscape(name))
	if ref.Mime == "image/svg+xml" {
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox")
	}
	// zero mod time: the content hash in the ETag is the only validator needed
	http.ServeContent(w, r, "", time.Time{}, f)
	return nil
}
