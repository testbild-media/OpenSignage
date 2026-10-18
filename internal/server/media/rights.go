package media

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"opensignage/internal/server/core"
	"opensignage/internal/server/httpx"
)

// Media rights of operators limited to zones (core/rights.go): "use" browses and uses media,
// "upload" adds files and folders and renames, moves or replaces own ones, "manage" may do
// everything operators of all zones may do.

var (
	errMediaDenied = httpx.E(http.StatusForbidden, "media_forbidden", "you may not do this with media")
	errMediaNotOwn = httpx.E(http.StatusForbidden, "media_not_own", "only media you uploaded yourself can be changed")
)

// mediaRight wraps a handler that needs at least the media right level.
func mediaRight(level string, h core.AuthedHandler) core.AuthedHandler {
	return func(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
		if !p.CanMedia(level) {
			return errMediaDenied
		}
		return h(w, r, p)
	}
}

// ownsAsset reports whether p uploaded the asset (or may manage all media).
func (m *Module) ownsAsset(ctx context.Context, p *core.Principal, id string) (bool, error) {
	if p.CanMedia(core.MediaManage) {
		return true, nil
	}
	var by sql.NullString
	err := m.app.DB.R.QueryRowContext(ctx, `SELECT uploaded_by FROM media_assets WHERE id = ?`, id).Scan(&by)
	if errors.Is(err, sql.ErrNoRows) {
		return false, httpx.ErrNotFound
	}
	return err == nil && by.Valid && by.String == p.UserID, err
}

// ownAsset wraps PATCH /media/{id}: with the upload right only for own uploads.
func (m *Module) ownAsset(h core.AuthedHandler) core.AuthedHandler {
	return func(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
		if ok, err := m.ownsAsset(r.Context(), p, r.PathValue("id")); err != nil {
			return err
		} else if !ok {
			return errMediaNotOwn
		}
		return h(w, r, p)
	}
}

// ownFolder wraps PATCH /media/folders/{id}: with the upload right only for own folders.
func (m *Module) ownFolder(h core.AuthedHandler) core.AuthedHandler {
	return func(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
		if !p.CanMedia(core.MediaManage) {
			var by sql.NullString
			err := m.app.DB.R.QueryRowContext(r.Context(), `SELECT created_by FROM media_folders WHERE id = ?`, r.PathValue("id")).Scan(&by)
			if errors.Is(err, sql.ErrNoRows) {
				return httpx.ErrNotFound
			}
			if err != nil {
				return err
			}
			if !by.Valid || by.String != p.UserID {
				return errMediaNotOwn
			}
		}
		return h(w, r, p)
	}
}
