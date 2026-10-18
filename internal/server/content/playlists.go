package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"opensignage/internal/common/ids"
	"opensignage/internal/contracts"
	"opensignage/internal/server/access"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

type plRow struct {
	ID               string
	Name             string
	Draft            string
	DraftRevision    int64
	PublishedVersion sql.NullInt64
	HasChanges       bool
	Archived         bool
	UpdatedAt        string
}

func loadPlRow(ctx context.Context, q modkit.Queryer, id string) (plRow, error) {
	var r plRow
	var changes, arch int
	err := q.QueryRowContext(ctx, `SELECT id, name, draft, draft_revision, published_version, has_changes, archived_at IS NOT NULL, updated_at
		FROM playlists WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.Draft, &r.DraftRevision, &r.PublishedVersion, &changes, &arch, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, httpx.ErrNotFound
	}
	r.HasChanges, r.Archived = changes == 1, arch == 1
	return r, err
}

type plDetail struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Draft            json.RawMessage `json:"draft"`
	DraftRevision    int64           `json:"draftRevision"`
	PublishedVersion *int64          `json:"publishedVersion"`
	HasChanges       bool            `json:"hasChanges"`
	Archived         bool            `json:"archived"`
	UpdatedAt        string          `json:"updatedAt"`
	RuntimeMS        int64           `json:"runtimeMs"`
	RuntimeExact     bool            `json:"runtimeExact"`
	// ItemPasses is the length of one pass per item id: the editor shows the slide's own
	// duration and the total of repetitions.
	ItemPasses map[string]itemPass `json:"itemPasses"`
	// Access and CopyZones are sent with GET only (the editor keeps them for the session).
	Access    *access.Rights `json:"access,omitempty"`
	CopyZones []string       `json:"copyZones,omitempty"`
}

// itemPass is the length of one pass of a playlist item; MS 0 means unknown. Exact is false
// when the length could only be bounded ("until the video ends" slides).
type itemPass struct {
	MS    int64 `json:"ms"`
	Exact bool  `json:"exact"`
}

func (m *Module) plDetail(ctx context.Context, q modkit.Queryer, r plRow) (plDetail, error) {
	d := plDetail{ID: r.ID, Name: r.Name, Draft: json.RawMessage(r.Draft), DraftRevision: r.DraftRevision,
		HasChanges: r.HasChanges, Archived: r.Archived, UpdatedAt: r.UpdatedAt}
	if r.PublishedVersion.Valid {
		v := r.PublishedVersion.Int64
		d.PublishedVersion = &v
	}
	var pl contracts.Playlist
	if err := json.Unmarshal([]byte(r.Draft), &pl); err != nil {
		return d, err
	}
	var err error
	d.RuntimeMS, d.RuntimeExact, d.ItemPasses, err = m.estimateRuntime(ctx, q, &pl)
	return d, err
}

type plSummary struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	ItemCount        int           `json:"itemCount"`
	PublishedVersion *int64        `json:"publishedVersion"`
	HasChanges       bool          `json:"hasChanges"`
	Archived         bool          `json:"archived"`
	UpdatedAt        string        `json:"updatedAt"`
	Access           access.Rights `json:"access"`
}

func (m *Module) listPlaylists(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	arch := 0
	switch r.URL.Query().Get("archived") {
	case "", "0":
	case "1":
		arch = 1
	default:
		return httpx.Invalid("archived", "invalid")
	}
	rows, err := m.app.DB.R.QueryContext(r.Context(), `SELECT id, name, COALESCE(json_array_length(draft, '$.items'), 0), published_version,
		has_changes, archived_at IS NOT NULL, updated_at FROM playlists WHERE (archived_at IS NOT NULL) = ? ORDER BY name COLLATE NOCASE, id`, arch)
	if err != nil {
		return err
	}
	defer rows.Close()
	l, err := m.newLister(r.Context(), p, kindPl)
	if err != nil {
		return err
	}
	out := []plSummary{}
	for rows.Next() {
		var s plSummary
		var pub sql.NullInt64
		var changes, archived int
		if err := rows.Scan(&s.ID, &s.Name, &s.ItemCount, &pub, &changes, &archived, &s.UpdatedAt); err != nil {
			return err
		}
		s.HasChanges, s.Archived = changes == 1, archived == 1
		if pub.Valid {
			s.PublishedVersion = &pub.Int64
		}
		if s.Access = l.rights(kindPl, s.ID, false); !s.Access.View {
			continue
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"playlists": out})
}

func (m *Module) getPlaylist(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	ctx := r.Context()
	row, err := loadPlRow(ctx, m.app.DB.R, r.PathValue("id"))
	if err != nil {
		return err
	}
	d, err := m.plDetail(ctx, m.app.DB.R, row)
	if err != nil {
		return err
	}
	rt, err := rightsOn(ctx, m.app.DB.R, p, kindPl, row.ID)
	if err != nil {
		return err
	}
	d.Access = &rt
	if d.CopyZones, err = copyZones(ctx, m.app.DB.R, p, kindPl, row.ID); err != nil {
		return err
	}
	return httpx.OK(w, d)
}

func normalizePlaylist(p *contracts.Playlist) {
	if p.Items == nil {
		p.Items = []contracts.PlaylistItem{}
	}
}

// playlistAssets returns the media assets used by a playlist.
func playlistAssets(p *contracts.Playlist) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range p.Refs() {
		if it.Kind == contracts.ItemMedia && !seen[it.RefID] {
			seen[it.RefID] = true
			out = append(out, it.RefID)
		}
	}
	return out
}

// plFingerprint is the canonical JSON of a playlist without its name.
func plFingerprint(p contracts.Playlist) (string, error) {
	p.Name = ""
	b, err := json.Marshal(p)
	return string(b), err
}

func plHasChanges(ctx context.Context, q modkit.Queryer, id string, published sql.NullInt64, draft contracts.Playlist) (bool, error) {
	if !published.Valid {
		return true, nil
	}
	raw, err := publishedDoc(ctx, q, "playlist_versions", "playlist_id", id, published.Int64)
	if err != nil {
		return false, err
	}
	var pub contracts.Playlist
	if err := json.Unmarshal([]byte(raw), &pub); err != nil {
		return false, err
	}
	pf, err := plFingerprint(pub)
	if err != nil {
		return false, err
	}
	df, err := plFingerprint(draft)
	if err != nil {
		return false, err
	}
	return pf != df, nil
}

func insertPlaylist(ctx context.Context, tx *sql.Tx, doc contracts.Playlist) error {
	normalizePlaylist(&doc)
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	now := db.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO playlists(id, name, draft, draft_revision, published_version, has_changes, created_at, updated_at)
		VALUES (?, ?, ?, 1, NULL, 1, ?, ?)`, doc.ID, doc.Name, string(b), now, now); err != nil {
		return err
	}
	return syncRefs(ctx, tx, "playlist_draft", doc.ID, playlistAssets(&doc))
}

func (m *Module) createPlaylist(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req nameReq
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
	id := ids.New()
	var out plRow
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		if err := insertPlaylist(r.Context(), tx, contracts.Playlist{ID: id, Name: name}); err != nil {
			return err
		}
		if err := setCreator(r.Context(), tx, "playlists", id, p); err != nil {
			return err
		}
		var err error
		if out, err = loadPlRow(r.Context(), tx, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "playlist.created", ObjectType: "playlist", ObjectID: id, ObjectName: name})
	})
	if err != nil {
		return err
	}
	d, err := m.plDetail(r.Context(), m.app.DB.R, out)
	if err != nil {
		return err
	}
	m.emit("playlist.created", "") // lists and the sidebar refresh
	return httpx.OK(w, d)
}

func (m *Module) renamePlaylist(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req nameReq
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
	var out plRow
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPlRow(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if row.Name == name {
			out = row
			return nil
		}
		var doc contracts.Playlist
		if err := json.Unmarshal([]byte(row.Draft), &doc); err != nil {
			return err
		}
		doc.Name = name
		normalizePlaylist(&doc)
		b, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE playlists SET name = ?, draft = ?, draft_revision = draft_revision + 1, updated_at = ? WHERE id = ?`,
			name, string(b), db.Now(), id); err != nil {
			return err
		}
		if out, err = loadPlRow(r.Context(), tx, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "playlist.renamed", ObjectType: "playlist", ObjectID: id,
			ObjectName: name, Old: map[string]any{"name": row.Name}, New: map[string]any{"name": name}})
	})
	if err != nil {
		return err
	}
	d, err := m.plDetail(r.Context(), m.app.DB.R, out)
	if err != nil {
		return err
	}
	m.emit("playlist.renamed", "") // lists and the sidebar refresh
	return httpx.OK(w, d)
}

type playlistDraftReq struct {
	Revision int64               `json:"revision"`
	Doc      *contracts.Playlist `json:"doc"`
}

// savePlaylistDraft stores the playlist draft (optimistic concurrency, full validation).
// Editing never distributes anything; only publishing does.
func (m *Module) savePlaylistDraft(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req playlistDraftReq
	if err := httpx.Decode(w, r, maxPlaylistBytes, &req); err != nil {
		return err
	}
	if req.Revision <= 0 {
		return httpx.Invalid("revision", "required")
	}
	if req.Doc == nil {
		return httpx.Invalid("doc", "required")
	}
	doc := *req.Doc
	if doc.ID != id {
		return httpx.Invalid("doc.id", "mismatch")
	}
	doc.Name = strings.TrimSpace(doc.Name)
	normalizePlaylist(&doc)
	defaultFit := m.app.Settings.Get().Defaults.Fit
	for i := range doc.Items {
		if doc.Items[i].Kind == contracts.ItemMedia && doc.Items[i].Fit == "" {
			doc.Items[i].Fit = defaultFit
		}
		for _, c := range doc.Items[i].Cells {
			if c != nil && c.Kind == contracts.ItemMedia && c.Fit == "" {
				c.Fit = defaultFit
			}
		}
	}
	if !p.Unrestricted() {
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if err := referencesVisible(r.Context(), m.app.DB.R, p, string(raw)); err != nil {
			return err
		}
	}
	var res map[string]any
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPlRow(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if row.Archived {
			return errPlaylistArchived
		}
		if row.DraftRevision != req.Revision {
			return conflictWithRevision(row.DraftRevision)
		}
		if err := m.validatePlaylistDoc(r.Context(), tx, &doc); err != nil {
			return err
		}
		b, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		runtime, exact, passes, err := m.estimateRuntime(r.Context(), tx, &doc)
		if err != nil {
			return err
		}
		if string(b) == row.Draft && doc.Name == row.Name {
			res = map[string]any{"revision": row.DraftRevision, "hasChanges": row.HasChanges, "updatedAt": row.UpdatedAt,
				"changed": false, "runtimeMs": runtime, "runtimeExact": exact, "itemPasses": passes}
			return nil
		}
		changes, err := plHasChanges(r.Context(), tx, id, row.PublishedVersion, doc)
		if err != nil {
			return err
		}
		now := db.Now()
		if _, err := tx.ExecContext(r.Context(), `UPDATE playlists SET name = ?, draft = ?, draft_revision = draft_revision + 1,
			has_changes = ?, updated_at = ? WHERE id = ?`, doc.Name, string(b), db.Bool(changes), now, id); err != nil {
			return err
		}
		if err := syncRefs(r.Context(), tx, "playlist_draft", id, playlistAssets(&doc)); err != nil {
			return err
		}
		res = map[string]any{"revision": row.DraftRevision + 1, "hasChanges": changes, "updatedAt": now,
			"changed": true, "runtimeMs": runtime, "runtimeExact": exact, "itemPasses": passes}
		return nil
	})
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

// checkPublishable verifies that everything the playlist plays is already published:
// deployments carry published snapshots only.
func checkPublishable(ctx context.Context, tx *sql.Tx, pl *contracts.Playlist) error {
	for _, it := range pl.Refs() {
		var presID string
		switch it.Kind {
		case contracts.ItemPresentation:
			presID = it.RefID
		case contracts.ItemSlide:
			presID = it.PresentationID
		default:
			continue
		}
		path := it.Path + ".refId"
		var pub sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT published_version FROM presentations WHERE id = ?`, presID).Scan(&pub); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return httpx.Invalid(path, "unknown_reference")
			}
			return err
		}
		if !pub.Valid {
			return httpx.Invalid(path, "presentation_not_published")
		}
		if it.Kind == contracts.ItemSlide {
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM presentation_versions v, json_each(v.doc, '$.slides') j
				WHERE v.presentation_id = ? AND v.version = ? AND json_extract(j.value, '$.id') = ? LIMIT 1`, presID, pub.Int64, it.RefID).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				return httpx.Invalid(path, "slide_not_published")
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Module) publishPlaylist(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req publishReq
	if err := modkit.DecodeOptional(w, r, 0, &req); err != nil {
		return err
	}
	var version int64
	var changed bool
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPlRow(r.Context(), tx, id)
		if err != nil {
			return err
		}
		switch {
		case row.Archived:
			return errPlaylistArchived
		case req.Revision != 0 && req.Revision != row.DraftRevision:
			return conflictWithRevision(row.DraftRevision)
		}
		if row.PublishedVersion.Valid && !row.HasChanges {
			version = row.PublishedVersion.Int64
			return nil
		}
		var doc contracts.Playlist
		if err := json.Unmarshal([]byte(row.Draft), &doc); err != nil {
			return err
		}
		if err := m.validatePlaylistDoc(r.Context(), tx, &doc); err != nil {
			return err
		}
		if err := checkPublishable(r.Context(), tx, &doc); err != nil {
			return err
		}
		if version, err = nextVersion(r.Context(), tx, "playlist_versions", "playlist_id", id); err != nil {
			return err
		}
		now := db.Now()
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO playlist_versions(playlist_id, version, doc, published_at, published_by)
			VALUES (?, ?, ?, ?, ?)`, id, version, row.Draft, now, p.Username); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM playlist_versions WHERE playlist_id = ? AND version <= ?`, id, version-keepVersions); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE playlists SET published_version = ?, has_changes = 0, updated_at = ? WHERE id = ?`, version, now, id); err != nil {
			return err
		}
		if err := syncRefs(r.Context(), tx, "playlist_published", id, playlistAssets(&doc)); err != nil {
			return err
		}
		changed = true
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "playlist.published", ObjectType: "playlist", ObjectID: id,
			ObjectName: row.Name, Old: map[string]any{"version": nullInt(row.PublishedVersion)}, New: map[string]any{"version": version}})
	})
	if err != nil {
		return err
	}
	if changed {
		m.emit("playlist.published", id)
	}
	return httpx.OK(w, map[string]any{"publishedVersion": version, "hasChanges": false, "changed": changed})
}

func (m *Module) discardPlaylist(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var out plRow
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPlRow(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if row.Archived {
			return errPlaylistArchived
		}
		if !row.PublishedVersion.Valid {
			return errNeverPublished
		}
		raw, err := publishedDoc(r.Context(), tx, "playlist_versions", "playlist_id", id, row.PublishedVersion.Int64)
		if err != nil {
			return err
		}
		var doc contracts.Playlist
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			return err
		}
		doc.ID, doc.Name = id, row.Name
		normalizePlaylist(&doc)
		b, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE playlists SET draft = ?, draft_revision = draft_revision + 1, has_changes = 0, updated_at = ? WHERE id = ?`,
			string(b), db.Now(), id); err != nil {
			return err
		}
		if err := syncRefs(r.Context(), tx, "playlist_draft", id, playlistAssets(&doc)); err != nil {
			return err
		}
		if out, err = loadPlRow(r.Context(), tx, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "playlist.discarded", ObjectType: "playlist", ObjectID: id,
			ObjectName: row.Name, Old: map[string]any{"draftRevision": row.DraftRevision, "hadChanges": row.HasChanges},
			New: map[string]any{"draftRevision": out.DraftRevision, "version": row.PublishedVersion.Int64}})
	})
	if err != nil {
		return err
	}
	d, err := m.plDetail(r.Context(), m.app.DB.R, out)
	if err != nil {
		return err
	}
	return httpx.OK(w, d)
}

func (m *Module) duplicatePlaylist(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	src := r.PathValue("id")
	var req nameReq
	if err := modkit.DecodeOptional(w, r, 0, &req); err != nil {
		return err
	}
	newID := ids.New()
	var out plRow
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPlRow(r.Context(), tx, src)
		if err != nil {
			return err
		}
		name := row.Name + " (copy)"
		if req.Name != nil {
			var ok bool
			if name, ok = modkit.CleanName(*req.Name, maxNameRunes); !ok {
				return httpx.Invalid("name", "invalid")
			}
		}
		var doc contracts.Playlist
		if err := json.Unmarshal([]byte(row.Draft), &doc); err != nil {
			return err
		}
		doc.ID, doc.Name = newID, name
		if err := insertPlaylist(r.Context(), tx, doc); err != nil {
			return err
		}
		if err := setCreator(r.Context(), tx, "playlists", newID, p); err != nil {
			return err
		}
		if out, err = loadPlRow(r.Context(), tx, newID); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "playlist.duplicated", ObjectType: "playlist", ObjectID: newID,
			ObjectName: name, New: map[string]any{"from": src}})
	})
	if err != nil {
		return err
	}
	d, err := m.plDetail(r.Context(), m.app.DB.R, out)
	if err != nil {
		return err
	}
	m.emit("playlist.duplicated", "") // lists and the sidebar refresh
	return httpx.OK(w, d)
}

// archivePlaylist serves DELETE: playlists are archived, never removed, and only when no
// schedule rule and no zone or display group (closed playlist) still points at them.
func (m *Module) archivePlaylist(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	archived := false
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPlRow(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if row.Archived {
			return nil
		}
		rules, err := queryRefs(r.Context(), tx, `SELECT id, name FROM schedule_rules WHERE playlist_id = ?1
			OR id IN (SELECT rule_id FROM schedule_group_playlists WHERE playlist_id = ?1) ORDER BY name COLLATE NOCASE, id`, id)
		if err != nil {
			return err
		}
		zones, err := queryRefs(r.Context(), tx, `SELECT id, name FROM zones WHERE closed_playlist_id = ?1
			OR id IN (SELECT zone_id FROM display_groups WHERE closed_playlist_id = ?1) ORDER BY name COLLATE NOCASE, id`, id)
		if err != nil {
			return err
		}
		if len(rules) > 0 || len(zones) > 0 {
			return errPlaylistInUse.With("rules", refNames(rules)).With("zones", refNames(zones))
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE playlists SET archived_at = ?, updated_at = ? WHERE id = ?`, db.Now(), db.Now(), id); err != nil {
			return err
		}
		archived = true
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "playlist.archived", ObjectType: "playlist", ObjectID: id,
			ObjectName: row.Name, Old: map[string]any{"archived": false}, New: map[string]any{"archived": true}})
	})
	if err != nil {
		return err
	}
	if archived {
		m.emit("playlist.archived", id)
	}
	return httpx.NoContent(w)
}

// restorePlaylist brings an archived playlist back. Its published version is what schedules
// and zones distribute, so everything it plays must still be playable (409
// restore_references_missing otherwise); problems of the draft only come back as warnings
// (the next publish validates the draft anyway).
func (m *Module) restorePlaylist(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var out plRow
	restored := false
	warnings := []string{}
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPlRow(r.Context(), tx, id)
		if err != nil {
			return err
		}
		out = row
		if !row.Archived {
			return nil
		}
		res := m.resolver(r.Context(), tx)
		if row.PublishedVersion.Valid {
			raw, err := publishedDoc(r.Context(), tx, "playlist_versions", "playlist_id", id, row.PublishedVersion.Int64)
			if err != nil {
				return err
			}
			var pub contracts.Playlist
			if err := json.Unmarshal([]byte(raw), &pub); err != nil {
				return err
			}
			missing, err := danglingPlaylistRefs(r.Context(), tx, res, &pub)
			if err != nil {
				return err
			}
			if len(missing) > 0 {
				return errRestoreRefs.With("references", missing)
			}
		}
		var draft contracts.Playlist
		if err := json.Unmarshal([]byte(row.Draft), &draft); err != nil {
			return err
		}
		if warnings, err = danglingPlaylistRefs(r.Context(), tx, res, &draft); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE playlists SET archived_at = NULL, updated_at = ? WHERE id = ?`, db.Now(), id); err != nil {
			return err
		}
		restored = true
		if out, err = loadPlRow(r.Context(), tx, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "playlist.restored", ObjectType: "playlist", ObjectID: id,
			ObjectName: row.Name, Old: map[string]any{"archived": true}, New: map[string]any{"archived": false}})
	})
	if err != nil {
		return err
	}
	if restored {
		m.emit("playlist.restored", id)
	}
	d, err := m.plDetail(r.Context(), m.app.DB.R, out)
	if err != nil {
		return err
	}
	return httpx.OK(w, struct {
		plDetail
		Warnings []string `json:"warnings"`
	}{d, warnings})
}

// ---------------------------------------------------------------- runtime estimate

type slideDuration struct {
	ID       string `json:"id"`
	Hidden   bool   `json:"hidden"`
	Duration struct {
		Mode  string `json:"mode"`
		MS    int64  `json:"ms"`
		MaxMS int64  `json:"maxMs"`
	} `json:"duration"`
}

type presDurations struct {
	Slides []slideDuration `json:"slides"`
}

// slideMS returns the duration of one slide; a "until the video ends" slide is bounded by
// its maximum, which makes the estimate inexact.
func slideMS(s slideDuration) (ms int64, exact bool) {
	if s.Duration.Mode == "video" {
		if s.Duration.MaxMS > 0 {
			return s.Duration.MaxMS, false
		}
		return contracts.DefaultVideoMaxMS, false
	}
	return s.Duration.MS, true
}

// estimateRuntime computes the total running time of one pass over the playlist. A
// duration item always completes its running pass, so it rounds up to whole passes when
// the pass length is known. exact is false when some length could only be bounded or was
// unknown (video slides, videos without probe data). passes holds the length of one pass
// per item id.
func (m *Module) estimateRuntime(ctx context.Context, q modkit.Queryer, pl *contracts.Playlist) (total int64, exact bool, passes map[string]itemPass, err error) {
	exact = true
	passes = make(map[string]itemPass, len(pl.Items))
	if pl.Tracks() > 1 {
		// Several tracks: every step runs exactly its duration.
		for _, it := range pl.Items {
			passes[it.ID] = itemPass{MS: it.Play.MS, Exact: true}
			total += it.Play.MS
		}
		return total, true, passes, nil
	}
	pres := map[string]*presDurations{}
	loadPres := func(id string) (*presDurations, error) {
		if d, ok := pres[id]; ok {
			return d, nil
		}
		var raw string
		err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT v.doc FROM presentation_versions v WHERE v.presentation_id = p.id AND v.version = p.published_version), p.draft)
			FROM presentations p WHERE p.id = ?`, id).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			pres[id] = nil
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var d presDurations
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			return nil, err
		}
		pres[id] = &d
		return &d, nil
	}
	for _, it := range pl.Items {
		var pass int64 // length of one pass; 0 = unknown
		passExact := true
		switch it.Kind {
		case contracts.ItemPresentation:
			d, err := loadPres(it.RefID)
			if err != nil {
				return 0, false, nil, err
			}
			if d != nil {
				for _, s := range d.Slides {
					if s.Hidden {
						continue
					}
					ms, ex := slideMS(s)
					pass += ms
					passExact = passExact && ex
				}
			}
		case contracts.ItemSlide:
			d, err := loadPres(it.PresentationID)
			if err != nil {
				return 0, false, nil, err
			}
			if d != nil {
				for _, s := range d.Slides {
					if s.ID == it.RefID {
						pass, passExact = slideMS(s)
					}
				}
			}
		case contracts.ItemMedia:
			var meta string
			var kind string
			err := q.QueryRowContext(ctx, `SELECT kind, meta FROM media_assets WHERE id = ?`, it.RefID).Scan(&kind, &meta)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return 0, false, nil, err
			}
			if kind == "image" {
				pass = it.Play.MS
			} else if err == nil {
				var mm struct {
					DurationMS float64 `json:"durationMs"`
				}
				if json.Unmarshal([]byte(meta), &mm) == nil && mm.DurationMS > 0 {
					pass = int64(mm.DurationMS + 0.999)
				}
			}
		}
		passes[it.ID] = itemPass{MS: pass, Exact: pass > 0 && passExact}
		switch it.Play.Mode {
		case contracts.PlayDuration:
			if pass > 0 {
				total += (it.Play.MS + pass - 1) / pass * pass
				exact = exact && passExact
			} else {
				total += it.Play.MS
				exact = false
			}
		case contracts.PlayRepeat:
			if pass > 0 {
				total += int64(it.Play.Count) * pass
				exact = exact && passExact
			} else {
				exact = false
			}
		}
	}
	return total, exact, passes, nil
}
