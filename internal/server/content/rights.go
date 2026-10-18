package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"opensignage/internal/common/ids"
	"opensignage/internal/contracts"
	"opensignage/internal/server/access"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

// Rights of operators limited to zones on presentations and playlists (internal/server/access):
// they see content that runs in one of their zones or that they created, and change it when
// it runs only in zones they may edit (or nowhere yet and they created it). Templates are
// seen by everybody and changed by admins and operators of all zones only.

var (
	errReadOnly = httpx.E(http.StatusForbidden, "content_read_only",
		"the content also runs in zones you may not change; copy it for your zone instead")
	errCreateDenied   = httpx.E(http.StatusForbidden, "content_create_denied", "you may not create presentations or playlists")
	errCopyGlobal     = httpx.E(http.StatusConflict, "copy_global_rule", "the content runs in every zone through a global rule")
	errCopyNotInZone  = httpx.E(http.StatusConflict, "copy_not_in_zone", "the content does not run in this zone")
	errCopyNotShared  = httpx.E(http.StatusConflict, "copy_not_shared", "the content runs in this zone only")
	errZoneNotAllowed = httpx.E(http.StatusForbidden, "zone_forbidden", "you may not change this zone")
)

var fullRights = access.Rights{View: true, Edit: true}

// contentKind is a presentation or a playlist.
type contentKind struct {
	table string
	zones func(*access.Usage, string) ([]string, bool)
}

var (
	kindPres = contentKind{"presentations", (*access.Usage).PresentationZones}
	kindPl   = contentKind{"playlists", (*access.Usage).PlaylistZones}
)

// rightsOn returns p's rights on one presentation or playlist (none when it does not exist).
func rightsOn(ctx context.Context, q modkit.Queryer, p *core.Principal, k contentKind, id string) (access.Rights, error) {
	if p.Unrestricted() {
		return fullRights, nil
	}
	if !p.Restricted() {
		return access.Rights{}, nil
	}
	var by sql.NullString
	var tmpl int
	query := `SELECT created_by, is_template FROM presentations WHERE id = ?`
	if k.table == "playlists" {
		query = `SELECT created_by, 0 FROM playlists WHERE id = ?`
	}
	if err := q.QueryRowContext(ctx, query, id).Scan(&by, &tmpl); errors.Is(err, sql.ErrNoRows) {
		return access.Rights{}, nil
	} else if err != nil {
		return access.Rights{}, err
	}
	if tmpl == 1 {
		return access.Rights{View: true}, nil
	}
	u, err := access.Load(ctx, q)
	if err != nil {
		return access.Rights{}, err
	}
	zones, global := k.zones(u, id)
	return access.Decide(p, zones, global, by.String), nil
}

// guard wraps a handler for /…/{id}: content p may not see does not exist for p (404); with
// edit it must also be changeable (content_read_only).
func (m *Module) guard(k contentKind, edit bool, h core.AuthedHandler) core.AuthedHandler {
	return func(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
		if !p.Unrestricted() {
			rt, err := rightsOn(r.Context(), m.app.DB.R, p, k, r.PathValue("id"))
			if err != nil {
				return err
			}
			if !rt.View {
				return httpx.ErrNotFound
			}
			if edit && !rt.Edit {
				return errReadOnly
			}
		}
		return h(w, r, p)
	}
}

// creating wraps a handler that creates content (new, from a template, duplicate).
func creating(h core.AuthedHandler) core.AuthedHandler {
	return func(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
		if !p.CanCreateContent() {
			return errCreateDenied
		}
		return h(w, r, p)
	}
}

// lister decides the rights for every item of a list with one usage lookup.
type lister struct {
	p        *core.Principal
	usage    *access.Usage
	creators map[string]string
}

func (m *Module) newLister(ctx context.Context, p *core.Principal, k contentKind) (*lister, error) {
	l := &lister{p: p}
	if p.Unrestricted() || !p.Restricted() {
		return l, nil
	}
	var err error
	if l.usage, err = access.Load(ctx, m.app.DB.R); err != nil {
		return nil, err
	}
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT id, COALESCE(created_by, '') FROM `+k.table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	l.creators = map[string]string{}
	for rows.Next() {
		var id, by string
		if err := rows.Scan(&id, &by); err != nil {
			return nil, err
		}
		l.creators[id] = by
	}
	return l, rows.Err()
}

func (l *lister) rights(k contentKind, id string, template bool) access.Rights {
	switch {
	case l.p.Unrestricted():
		return fullRights
	case !l.p.Restricted():
		return access.Rights{}
	case template:
		return access.Rights{View: true}
	}
	zones, global := k.zones(l.usage, id)
	return access.Decide(l.p, zones, global, l.creators[id])
}

// copyZones lists the zones p may give an own copy of the content: zones it runs in (not
// through a global rule) and p may edit, when it also runs elsewhere.
func copyZones(ctx context.Context, q modkit.Queryer, p *core.Principal, k contentKind, id string) ([]string, error) {
	u, err := access.Load(ctx, q)
	if err != nil {
		return nil, err
	}
	zones, global := k.zones(u, id)
	out := []string{}
	if global || len(zones) < 2 {
		return out, nil
	}
	for _, z := range zones {
		if p.CanEditZone(z) {
			out = append(out, z)
		}
	}
	return out, nil
}

// setCreator records who created a presentation or playlist.
func setCreator(ctx context.Context, tx *sql.Tx, table, id string, p *core.Principal) error {
	_, err := tx.ExecContext(ctx, `UPDATE `+table+` SET created_by = ? WHERE id = ?`, p.UserID, id)
	return err
}

// referencesVisible refuses a playlist draft of an operator limited to zones that contains
// presentations the operator may not see.
func referencesVisible(ctx context.Context, q modkit.Queryer, p *core.Principal, raw string) error {
	if p.Unrestricted() {
		return nil
	}
	for i, pres := range access.Presentations(raw) {
		rt, err := rightsOn(ctx, q, p, kindPres, pres)
		if err != nil {
			return err
		}
		if !rt.View {
			return httpx.Invalid("items", "no_access").With("index", i)
		}
	}
	return nil
}

// ---------------------------------------------------------------- copy for a zone

type copyZoneReq struct {
	ZoneID string `json:"zoneId"`
}

// zoneForCopy checks the zone of a copy request and returns its name.
func zoneForCopy(ctx context.Context, q modkit.Queryer, p *core.Principal, zoneID string) (string, error) {
	if zoneID == "" {
		return "", httpx.Invalid("zoneId", "required")
	}
	var name string
	if err := q.QueryRowContext(ctx, `SELECT name FROM zones WHERE id = ?`, zoneID).Scan(&name); errors.Is(err, sql.ErrNoRows) {
		return "", httpx.Invalid("zoneId", "unknown_zone")
	} else if err != nil {
		return "", err
	}
	if !p.CanEditZone(zoneID) {
		return "", errZoneNotAllowed
	}
	return name, nil
}

// copyPresentationForZone (POST /api/v1/presentations/{id}/copy-for-zone) gives a zone its
// own copy of a presentation that also runs elsewhere. The copy has the published content of
// the original, so the screens show the same until the copy is changed and published. It
// takes the original's place in the zone's playlists: a playlist running only in this zone is
// changed in place (draft and published version), a shared one is copied as well and the
// zone's rules, display groups and closed playlist point to the copy.
func (m *Module) copyPresentationForZone(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	src := r.PathValue("id")
	var req copyZoneReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	ctx := r.Context()
	var out presRow
	var changedPl, copiedPl []string
	err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		zoneName, err := zoneForCopy(ctx, tx, p, req.ZoneID)
		if err != nil {
			return err
		}
		if rt, err := rightsOn(ctx, tx, p, kindPres, src); err != nil {
			return err
		} else if !rt.View {
			return httpx.ErrNotFound
		}
		row, err := loadPresRow(ctx, tx, src)
		if err != nil {
			return err
		}
		u, err := access.Load(ctx, tx)
		if err != nil {
			return err
		}
		zones, global := u.PresentationZones(src)
		switch {
		case global:
			return errCopyGlobal
		case !slices.Contains(zones, req.ZoneID):
			return errCopyNotInZone
		case len(zones) < 2:
			return errCopyNotShared
		}
		newID := ids.New()
		if err := copyPresentationRow(ctx, tx, row, newID, row.Name+" – "+zoneName, p); err != nil {
			return err
		}
		for _, pl := range u.PlaylistsWith(src) {
			plZones, _ := u.PlaylistZones(pl)
			if !slices.Contains(plZones, req.ZoneID) {
				continue
			}
			if len(plZones) == 1 {
				if err := replaceInPlaylist(ctx, tx, pl, src, newID, p); err != nil {
					return err
				}
				changedPl = append(changedPl, pl)
				continue
			}
			plRow, err := loadPlRow(ctx, tx, pl)
			if err != nil {
				return err
			}
			copyID := ids.New()
			if err := copyPlaylistRow(ctx, tx, plRow, copyID, plRow.Name+" – "+zoneName, src, newID, p); err != nil {
				return err
			}
			if err := pointZoneTo(ctx, tx, req.ZoneID, pl, copyID); err != nil {
				return err
			}
			copiedPl = append(copiedPl, pl)
		}
		if out, err = loadPresRow(ctx, tx, newID); err != nil {
			return err
		}
		return audit(ctx, tx, r, p, core.AuditEntry{Action: "presentation.copied_for_zone", ObjectType: "presentation", ObjectID: newID,
			ObjectName: out.Name, ZoneID: req.ZoneID, New: map[string]any{"from": src, "zoneId": req.ZoneID,
				"playlistsChanged": changedPl, "playlistsCopied": copiedPl}})
	})
	if err != nil {
		return err
	}
	m.emit("presentation.copied_for_zone", out.ID)
	m.app.Bus.Publish(core.Event{Topic: core.TopicZones, Kind: "schedules.changed", ZoneID: req.ZoneID})
	return httpx.OK(w, out.detail())
}

// copyPlaylistForZone (POST /api/v1/playlists/{id}/copy-for-zone) gives a zone its own copy
// of a playlist that also runs elsewhere; the zone's rules, display groups and closed playlist
// point to the copy (published content of the original, so the screens show the same).
func (m *Module) copyPlaylistForZone(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	src := r.PathValue("id")
	var req copyZoneReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	ctx := r.Context()
	var out plRow
	err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		zoneName, err := zoneForCopy(ctx, tx, p, req.ZoneID)
		if err != nil {
			return err
		}
		if rt, err := rightsOn(ctx, tx, p, kindPl, src); err != nil {
			return err
		} else if !rt.View {
			return httpx.ErrNotFound
		}
		row, err := loadPlRow(ctx, tx, src)
		if err != nil {
			return err
		}
		u, err := access.Load(ctx, tx)
		if err != nil {
			return err
		}
		zones, global := u.PlaylistZones(src)
		switch {
		case global:
			return errCopyGlobal
		case !slices.Contains(zones, req.ZoneID):
			return errCopyNotInZone
		case len(zones) < 2:
			return errCopyNotShared
		}
		newID := ids.New()
		if err := copyPlaylistRow(ctx, tx, row, newID, row.Name+" – "+zoneName, "", "", p); err != nil {
			return err
		}
		if err := pointZoneTo(ctx, tx, req.ZoneID, src, newID); err != nil {
			return err
		}
		if out, err = loadPlRow(ctx, tx, newID); err != nil {
			return err
		}
		return audit(ctx, tx, r, p, core.AuditEntry{Action: "playlist.copied_for_zone", ObjectType: "playlist", ObjectID: newID,
			ObjectName: out.Name, ZoneID: req.ZoneID, New: map[string]any{"from": src, "zoneId": req.ZoneID}})
	})
	if err != nil {
		return err
	}
	d, err := m.plDetail(ctx, m.app.DB.R, out)
	if err != nil {
		return err
	}
	m.emit("playlist.copied_for_zone", out.ID)
	m.app.Bus.Publish(core.Event{Topic: core.TopicZones, Kind: "schedules.changed", ZoneID: req.ZoneID})
	return httpx.OK(w, d)
}

// copyPresentationRow stores a copy of a presentation: draft and (when the original is
// published) version 1 are the published content of the original.
func copyPresentationRow(ctx context.Context, tx *sql.Tx, row presRow, newID, name string, p *core.Principal) error {
	raw := row.Draft
	if row.PublishedVersion.Valid {
		pub, err := publishedDoc(ctx, tx, "presentation_versions", "presentation_id", row.ID, row.PublishedVersion.Int64)
		if err != nil {
			return err
		}
		raw = pub
	}
	doc, err := copyOf(raw, newID, name)
	if err != nil {
		return err
	}
	if err := insertPresentation(ctx, tx, doc, false); err != nil {
		return err
	}
	if err := setCreator(ctx, tx, "presentations", newID, p); err != nil {
		return err
	}
	if !row.PublishedVersion.Valid {
		return nil
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	now := db.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO presentation_versions(presentation_id, version, doc, published_at, published_by)
		VALUES (?, 1, ?, ?, ?)`, newID, string(b), now, p.Username); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE presentations SET published_version = 1, has_changes = 0 WHERE id = ?`, newID); err != nil {
		return err
	}
	assets, _ := doc.References()
	return syncRefs(ctx, tx, "presentation_published", newID, assets)
}

// swapPresentation replaces presentation from by to in a playlist document.
func swapPresentation(raw, from, to string) (contracts.Playlist, bool, error) {
	var pl contracts.Playlist
	if err := json.Unmarshal([]byte(raw), &pl); err != nil {
		return pl, false, err
	}
	changed := false
	for i := range pl.Items {
		it := &pl.Items[i]
		if it.Kind == contracts.ItemPresentation && it.RefID == from {
			it.RefID, changed = to, true
		}
		if it.Kind == contracts.ItemSlide && it.PresentationID == from {
			it.PresentationID, changed = to, true
		}
		for _, c := range it.Cells {
			if c != nil && c.Kind == contracts.ItemSlide && c.PresentationID == from {
				c.PresentationID, changed = to, true
			}
		}
	}
	return pl, changed, nil
}

// copyPlaylistRow stores a copy of a playlist (published content when published, with
// presentation from replaced by to when from is set).
func copyPlaylistRow(ctx context.Context, tx *sql.Tx, row plRow, newID, name, from, to string, p *core.Principal) error {
	raw := row.Draft
	if row.PublishedVersion.Valid {
		pub, err := publishedDoc(ctx, tx, "playlist_versions", "playlist_id", row.ID, row.PublishedVersion.Int64)
		if err != nil {
			return err
		}
		raw = pub
	}
	doc, _, err := swapPresentation(raw, from, to)
	if err != nil {
		return err
	}
	doc.ID, doc.Name = newID, name
	if err := insertPlaylist(ctx, tx, doc); err != nil {
		return err
	}
	if err := setCreator(ctx, tx, "playlists", newID, p); err != nil {
		return err
	}
	if !row.PublishedVersion.Valid {
		return nil
	}
	normalizePlaylist(&doc)
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	now := db.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO playlist_versions(playlist_id, version, doc, published_at, published_by)
		VALUES (?, 1, ?, ?, ?)`, newID, string(b), now, p.Username); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE playlists SET published_version = 1, has_changes = 0 WHERE id = ?`, newID); err != nil {
		return err
	}
	return syncRefs(ctx, tx, "playlist_published", newID, playlistAssets(&doc))
}

// replaceInPlaylist swaps a presentation in a playlist's draft and published version (a new
// version with the same content otherwise).
func replaceInPlaylist(ctx context.Context, tx *sql.Tx, id, from, to string, p *core.Principal) error {
	row, err := loadPlRow(ctx, tx, id)
	if err != nil {
		return err
	}
	if doc, changed, err := swapPresentation(row.Draft, from, to); err != nil {
		return err
	} else if changed {
		normalizePlaylist(&doc)
		b, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE playlists SET draft = ?, draft_revision = draft_revision + 1, updated_at = ? WHERE id = ?`,
			string(b), db.Now(), id); err != nil {
			return err
		}
	}
	if !row.PublishedVersion.Valid {
		return nil
	}
	pub, err := publishedDoc(ctx, tx, "playlist_versions", "playlist_id", id, row.PublishedVersion.Int64)
	if err != nil {
		return err
	}
	doc, changed, err := swapPresentation(pub, from, to)
	if err != nil || !changed {
		return err
	}
	normalizePlaylist(&doc)
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	version, err := nextVersion(ctx, tx, "playlist_versions", "playlist_id", id)
	if err != nil {
		return err
	}
	now := db.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO playlist_versions(playlist_id, version, doc, published_at, published_by)
		VALUES (?, ?, ?, ?, ?)`, id, version, string(b), now, p.Username); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM playlist_versions WHERE playlist_id = ? AND version <= ?`, id, version-keepVersions); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE playlists SET published_version = ?, updated_at = ? WHERE id = ?`, version, now, id)
	return err
}

// pointZoneTo makes everything of a zone that plays playlist from play to instead: its zone
// and default rules, the playlists of its display groups (also in global rules) and the closed
// playlists of the zone and its display groups.
func pointZoneTo(ctx context.Context, tx *sql.Tx, zoneID, from, to string) error {
	now := db.Now()
	for _, s := range []struct {
		query string
		args  []any
	}{
		{`UPDATE schedule_rules SET playlist_id = ?, revision = revision + 1, updated_at = ?
			WHERE playlist_id = ? AND zone_id = ? AND scope IN ('zone', 'default')`, []any{to, now, from, zoneID}},
		{`UPDATE schedule_group_playlists SET playlist_id = ?
			WHERE playlist_id = ? AND group_id IN (SELECT id FROM display_groups WHERE zone_id = ?)`, []any{to, from, zoneID}},
		{`UPDATE schedule_rules SET revision = revision + 1, updated_at = ? WHERE id IN (SELECT sg.rule_id FROM schedule_group_playlists sg
			JOIN display_groups g ON g.id = sg.group_id WHERE g.zone_id = ? AND sg.playlist_id = ?)`, []any{now, zoneID, to}},
		{`UPDATE zones SET closed_playlist_id = ?, updated_at = ? WHERE id = ? AND closed_playlist_id = ?`, []any{to, now, zoneID, from}},
		{`UPDATE display_groups SET closed_playlist_id = ?, revision = revision + 1, updated_at = ?
			WHERE zone_id = ? AND closed_playlist_id = ?`, []any{to, now, zoneID, from}},
	} {
		if _, err := tx.ExecContext(ctx, s.query, s.args...); err != nil {
			return err
		}
	}
	return nil
}
