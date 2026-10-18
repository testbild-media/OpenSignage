package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"opensignage/internal/common/ids"
	"opensignage/internal/contracts"
	"opensignage/internal/server/access"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

// presRow is one presentations row.
type presRow struct {
	ID               string
	Name             string
	Draft            string
	DraftRevision    int64
	PublishedVersion sql.NullInt64
	HasChanges       bool
	IsTemplate       bool
	Archived         bool
	UpdatedAt        string
}

func loadPresRow(ctx context.Context, q modkit.Queryer, id string) (presRow, error) {
	var r presRow
	var changes, tmpl, arch int
	err := q.QueryRowContext(ctx, `SELECT id, name, draft, draft_revision, published_version, has_changes, is_template,
		archived_at IS NOT NULL, updated_at FROM presentations WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.Draft, &r.DraftRevision, &r.PublishedVersion, &changes, &tmpl, &arch, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, httpx.ErrNotFound
	}
	r.HasChanges, r.IsTemplate, r.Archived = changes == 1, tmpl == 1, arch == 1
	return r, err
}

// presDetail is the editor payload: the full draft plus state.
type presDetail struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Draft            json.RawMessage `json:"draft"`
	DraftRevision    int64           `json:"draftRevision"`
	PublishedVersion *int64          `json:"publishedVersion"`
	HasChanges       bool            `json:"hasChanges"`
	IsTemplate       bool            `json:"isTemplate"`
	Archived         bool            `json:"archived"`
	UpdatedAt        string          `json:"updatedAt"`
	// Access and CopyZones are sent with GET only (the editor keeps them for the session).
	Access    *access.Rights `json:"access,omitempty"`
	CopyZones []string       `json:"copyZones,omitempty"`
}

func (r presRow) detail() presDetail {
	d := presDetail{ID: r.ID, Name: r.Name, Draft: json.RawMessage(r.Draft), DraftRevision: r.DraftRevision,
		HasChanges: r.HasChanges, IsTemplate: r.IsTemplate, Archived: r.Archived, UpdatedAt: r.UpdatedAt}
	if r.PublishedVersion.Valid {
		v := r.PublishedVersion.Int64
		d.PublishedVersion = &v
	}
	return d
}

type presSummary struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Width            int             `json:"width"`
	Height           int             `json:"height"`
	SlideCount       int             `json:"slideCount"`
	PublishedVersion *int64          `json:"publishedVersion"`
	HasChanges       bool            `json:"hasChanges"`
	IsTemplate       bool            `json:"isTemplate"`
	Archived         bool            `json:"archived"`
	UpdatedAt        string          `json:"updatedAt"`
	FirstSlide       json.RawMessage `json:"firstSlide"` // first slide of the draft, for thumbnails
	Typography       json.RawMessage `json:"typography"` // text styles of the draft, for thumbnails
	Access           access.Rights   `json:"access"`
}

func (m *Module) listPresentations(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	q := r.URL.Query()
	tmpl, arch := 0, 0
	switch q.Get("templates") {
	case "", "0":
	case "1":
		tmpl = 1
	default:
		return httpx.Invalid("templates", "invalid")
	}
	switch q.Get("archived") {
	case "", "0":
	case "1":
		arch = 1
	default:
		return httpx.Invalid("archived", "invalid")
	}
	rows, err := m.app.DB.R.QueryContext(r.Context(), `SELECT id, name, COALESCE(json_extract(draft, '$.width'), 0),
		COALESCE(json_extract(draft, '$.height'), 0), COALESCE(json_array_length(draft, '$.slides'), 0),
		json_extract(draft, '$.slides[0]'), json_extract(draft, '$.typography'), published_version, has_changes, is_template,
		archived_at IS NOT NULL, updated_at
		FROM presentations WHERE is_template = ? AND (archived_at IS NOT NULL) = ? ORDER BY name COLLATE NOCASE, id`, tmpl, arch)
	if err != nil {
		return err
	}
	defer rows.Close()
	l, err := m.newLister(r.Context(), p, kindPres)
	if err != nil {
		return err
	}
	out := []presSummary{}
	for rows.Next() {
		var s presSummary
		var first, typo sql.NullString
		var pub sql.NullInt64
		var changes, isTmpl, archived int
		if err := rows.Scan(&s.ID, &s.Name, &s.Width, &s.Height, &s.SlideCount, &first, &typo, &pub, &changes, &isTmpl, &archived, &s.UpdatedAt); err != nil {
			return err
		}
		s.HasChanges, s.IsTemplate, s.Archived = changes == 1, isTmpl == 1, archived == 1
		if pub.Valid {
			s.PublishedVersion = &pub.Int64
		}
		if first.Valid {
			s.FirstSlide = json.RawMessage(first.String)
		}
		if typo.Valid {
			s.Typography = json.RawMessage(typo.String)
		}
		if s.Access = l.rights(kindPres, s.ID, s.IsTemplate); !s.Access.View {
			continue
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"presentations": out})
}

func (m *Module) getPresentation(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	ctx := r.Context()
	row, err := loadPresRow(ctx, m.app.DB.R, r.PathValue("id"))
	if err != nil {
		return err
	}
	d := row.detail()
	rt, err := rightsOn(ctx, m.app.DB.R, p, kindPres, row.ID)
	if err != nil {
		return err
	}
	d.Access = &rt
	if !row.IsTemplate {
		if d.CopyZones, err = copyZones(ctx, m.app.DB.R, p, kindPres, row.ID); err != nil {
			return err
		}
	}
	return httpx.OK(w, d)
}

// insertPresentation stores a new presentation row (draft revision 1, never published).
func insertPresentation(ctx context.Context, tx *sql.Tx, doc contracts.Presentation, isTemplate bool) error {
	normalizeDoc(&doc)
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	now := db.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO presentations(id, name, draft, draft_revision, published_version, has_changes,
		is_template, created_at, updated_at) VALUES (?, ?, ?, 1, NULL, 1, ?, ?, ?)`,
		doc.ID, doc.Name, string(b), db.Bool(isTemplate), now, now); err != nil {
		return err
	}
	assets, _ := doc.References()
	return syncRefs(ctx, tx, "presentation_draft", doc.ID, assets)
}

// copyOf decodes a stored document and re-identifies it as a new presentation.
func copyOf(raw, newID, newName string) (contracts.Presentation, error) {
	var doc contracts.Presentation
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return doc, err
	}
	doc.ID, doc.Name = newID, newName
	normalizeDoc(&doc)
	return doc, nil
}

type createPresReq struct {
	Name       *string `json:"name"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	TemplateID string  `json:"templateId"`
}

func (m *Module) createPresentation(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req createPresReq
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
	if (req.Width == 0) != (req.Height == 0) {
		return httpx.Invalid("height", "required")
	}
	if req.Width != 0 && (req.Width < contracts.MinDesignSize || req.Width > contracts.MaxDesignSize) {
		return httpx.Invalid("width", "out_of_range")
	}
	if req.Height != 0 && (req.Height < contracts.MinDesignSize || req.Height > contracts.MaxDesignSize) {
		return httpx.Invalid("height", "out_of_range")
	}
	id := ids.New()
	var out presRow
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var doc contracts.Presentation
		if req.TemplateID != "" {
			tpl, err := loadPresRow(r.Context(), tx, req.TemplateID)
			if errors.Is(err, httpx.ErrNotFound) || (err == nil && (!tpl.IsTemplate || tpl.Archived)) {
				return httpx.Invalid("templateId", "unknown_template")
			}
			if err != nil {
				return err
			}
			if doc, err = copyOf(tpl.Draft, id, name); err != nil {
				return err
			}
			if req.Width != 0 && (req.Width != doc.Width || req.Height != doc.Height) {
				if err := Resize(&doc, req.Width, req.Height); err != nil {
					return httpx.Invalid("width", "out_of_range")
				}
			}
		} else {
			w, h := req.Width, req.Height
			if w == 0 {
				w, h = defaultWidth, defaultHeight
			}
			doc = newDocument(id, name, w, h, m.app.Settings.Get().Defaults.SlideMS)
		}
		if err := doc.Validate(nil); err != nil {
			return err
		}
		if err := insertPresentation(r.Context(), tx, doc, false); err != nil {
			return err
		}
		if err := setCreator(r.Context(), tx, "presentations", id, p); err != nil {
			return err
		}
		var err error
		if out, err = loadPresRow(r.Context(), tx, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "presentation.created", ObjectType: "presentation",
			ObjectID: id, ObjectName: name, New: map[string]any{"width": doc.Width, "height": doc.Height, "templateId": req.TemplateID}})
	})
	if err != nil {
		return err
	}
	m.emit("presentation.created", "") // lists and the sidebar refresh
	return httpx.OK(w, out.detail())
}

type draftReq struct {
	Revision int64                   `json:"revision"`
	Doc      *contracts.Presentation `json:"doc"`
}

// hasChangesFor reports whether the draft differs from the published version.
func hasChangesFor(ctx context.Context, q modkit.Queryer, id string, published sql.NullInt64, draft contracts.Presentation) (bool, error) {
	if !published.Valid {
		return true, nil
	}
	var raw string
	err := q.QueryRowContext(ctx, `SELECT doc FROM presentation_versions WHERE presentation_id = ? AND version = ?`, id, published.Int64).Scan(&raw)
	if err != nil {
		return false, err
	}
	pubFP, err := fingerprintRaw(raw)
	if err != nil {
		return false, err
	}
	draftFP, err := fingerprint(draft)
	if err != nil {
		return false, err
	}
	return pubFP != draftFP, nil
}

// saveDraft is the autosave endpoint: validated, optimistic concurrency, no audit entry and
// no event (a draft is not visible state and never triggers a distribution).
func (m *Module) saveDraft(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	id := r.PathValue("id")
	var req draftReq
	if err := httpx.Decode(w, r, maxDraftBytes, &req); err != nil {
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
	normalizeDoc(&doc)
	var res map[string]any
	renamed := false
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPresRow(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if row.Archived {
			return errPresArchived
		}
		if row.DraftRevision != req.Revision {
			return conflictWithRevision(row.DraftRevision)
		}
		if err := m.validateDoc(r.Context(), tx, &doc); err != nil {
			return err
		}
		b, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if string(b) == row.Draft && doc.Name == row.Name {
			res = map[string]any{"revision": row.DraftRevision, "hasChanges": row.HasChanges, "updatedAt": row.UpdatedAt, "changed": false}
			return nil
		}
		changes, err := hasChangesFor(r.Context(), tx, id, row.PublishedVersion, doc)
		if err != nil {
			return err
		}
		now := db.Now()
		if _, err := tx.ExecContext(r.Context(), `UPDATE presentations SET name = ?, draft = ?, draft_revision = draft_revision + 1,
			has_changes = ?, updated_at = ? WHERE id = ?`, doc.Name, string(b), db.Bool(changes), now, id); err != nil {
			return err
		}
		assets, _ := doc.References()
		if err := syncRefs(r.Context(), tx, "presentation_draft", id, assets); err != nil {
			return err
		}
		res = map[string]any{"revision": row.DraftRevision + 1, "hasChanges": changes, "updatedAt": now, "changed": true}
		renamed = doc.Name != row.Name
		return nil
	})
	if err != nil {
		return err
	}
	if renamed {
		m.emit("presentation.renamed", id)
	}
	return httpx.OK(w, res)
}

type renameReq struct {
	Name *string `json:"name"`
}

func (m *Module) renamePresentation(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req renameReq
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
	var out presRow
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPresRow(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if row.Name == name {
			out = row
			return nil
		}
		doc, err := copyOf(row.Draft, id, name)
		if err != nil {
			return err
		}
		b, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		// The draft revision rises so an open editor notices the rename instead of overwriting it.
		if _, err := tx.ExecContext(r.Context(), `UPDATE presentations SET name = ?, draft = ?, draft_revision = draft_revision + 1,
			updated_at = ? WHERE id = ?`, name, string(b), db.Now(), id); err != nil {
			return err
		}
		if out, err = loadPresRow(r.Context(), tx, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "presentation.renamed", ObjectType: "presentation",
			ObjectID: id, ObjectName: name, Old: map[string]any{"name": row.Name}, New: map[string]any{"name": name}})
	})
	if err != nil {
		return err
	}
	m.emit("presentation.renamed", "") // lists and the sidebar refresh
	return httpx.OK(w, out.detail())
}

type publishReq struct {
	Revision int64 `json:"revision"`
}

// nextVersion returns the next version number (versions are never reused).
func nextVersion(ctx context.Context, tx *sql.Tx, table, col, id string) (int64, error) {
	var v int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM `+table+` WHERE `+col+` = ?`, id).Scan(&v)
	return v, err
}

func (m *Module) publishPresentation(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req publishReq
	if err := modkit.DecodeOptional(w, r, 0, &req); err != nil {
		return err
	}
	var version int64
	var changed bool
	var oldVersion sql.NullInt64
	var name string
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPresRow(r.Context(), tx, id)
		if err != nil {
			return err
		}
		name, oldVersion = row.Name, row.PublishedVersion
		switch {
		case row.Archived:
			return errPresArchived
		case row.IsTemplate:
			return errTemplatePublish
		case req.Revision != 0 && req.Revision != row.DraftRevision:
			return conflictWithRevision(row.DraftRevision)
		}
		if row.PublishedVersion.Valid && !row.HasChanges {
			version = row.PublishedVersion.Int64
			return nil
		}
		var doc contracts.Presentation
		if err := json.Unmarshal([]byte(row.Draft), &doc); err != nil {
			return err
		}
		if err := m.validateDoc(r.Context(), tx, &doc); err != nil {
			return err
		}
		if err := checkSlidesInUse(r.Context(), tx, row, &doc); err != nil {
			return err
		}
		if version, err = nextVersion(r.Context(), tx, "presentation_versions", "presentation_id", id); err != nil {
			return err
		}
		now := db.Now()
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO presentation_versions(presentation_id, version, doc, published_at, published_by)
			VALUES (?, ?, ?, ?, ?)`, id, version, row.Draft, now, p.Username); err != nil {
			return err
		}
		if _, err := thinHistory(r.Context(), tx, id, time.Now()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE presentations SET published_version = ?, has_changes = 0, updated_at = ? WHERE id = ?`, version, now, id); err != nil {
			return err
		}
		assets, _ := doc.References()
		if err := syncRefs(r.Context(), tx, "presentation_published", id, assets); err != nil {
			return err
		}
		changed = true
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "presentation.published", ObjectType: "presentation",
			ObjectID: id, ObjectName: name, Old: map[string]any{"version": nullInt(oldVersion)}, New: map[string]any{"version": version}})
	})
	if err != nil {
		return err
	}
	if changed {
		m.emit("presentation.published", id)
	}
	return httpx.OK(w, map[string]any{"publishedVersion": version, "hasChanges": false, "changed": changed})
}

func nullInt(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
}

// publishedDoc loads the document of the currently published version.
func publishedDoc(ctx context.Context, q modkit.Queryer, table, col, id string, version int64) (string, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT doc FROM `+table+` WHERE `+col+` = ? AND version = ?`, id, version).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", httpx.ErrNotFound
	}
	return raw, err
}

// writeDraft stores doc as the new draft (revision + 1) of the row and syncs the refs.
func writeDraft(ctx context.Context, tx *sql.Tx, row presRow, doc contracts.Presentation) (presRow, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return row, err
	}
	changes, err := hasChangesFor(ctx, tx, row.ID, row.PublishedVersion, doc)
	if err != nil {
		return row, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE presentations SET draft = ?, draft_revision = draft_revision + 1, has_changes = ?,
		updated_at = ? WHERE id = ?`, string(b), db.Bool(changes), db.Now(), row.ID); err != nil {
		return row, err
	}
	assets, _ := doc.References()
	if err := syncRefs(ctx, tx, "presentation_draft", row.ID, assets); err != nil {
		return row, err
	}
	return loadPresRow(ctx, tx, row.ID)
}

func (m *Module) discardPresentation(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var out presRow
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPresRow(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if row.Archived {
			return errPresArchived
		}
		if !row.PublishedVersion.Valid {
			return errNeverPublished
		}
		raw, err := publishedDoc(r.Context(), tx, "presentation_versions", "presentation_id", id, row.PublishedVersion.Int64)
		if err != nil {
			return err
		}
		doc, err := copyOf(raw, id, row.Name)
		if err != nil {
			return err
		}
		if out, err = writeDraft(r.Context(), tx, row, doc); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "presentation.discarded", ObjectType: "presentation",
			ObjectID: id, ObjectName: row.Name, Old: map[string]any{"draftRevision": row.DraftRevision, "hadChanges": row.HasChanges},
			New: map[string]any{"draftRevision": out.DraftRevision, "version": row.PublishedVersion.Int64}})
	})
	if err != nil {
		return err
	}
	return httpx.OK(w, out.detail())
}

// restoreVersion copies an earlier published version into the draft. The result is not
// validated against the current references (an old version may mention media or products
// removed since); problems are returned as warnings and block the next publish.
func (m *Module) restoreVersion(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	v, err := strconv.ParseInt(r.PathValue("v"), 10, 64)
	if err != nil || v < 1 {
		return httpx.ErrNotFound
	}
	var out presRow
	warnings := []string{}
	err = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPresRow(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if row.Archived {
			return errPresArchived
		}
		raw, err := publishedDoc(r.Context(), tx, "presentation_versions", "presentation_id", id, v)
		if err != nil {
			return err
		}
		doc, err := copyOf(raw, id, row.Name)
		if err != nil {
			return err
		}
		var de *contracts.DocumentError
		if verr := m.validateDoc(r.Context(), tx, &doc); errors.As(verr, &de) {
			warnings = de.Problems
		} else if verr != nil {
			return verr
		}
		if out, err = writeDraft(r.Context(), tx, row, doc); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "presentation.version_restored", ObjectType: "presentation",
			ObjectID: id, ObjectName: row.Name, New: map[string]any{"version": v, "draftRevision": out.DraftRevision}})
	})
	if err != nil {
		return err
	}
	return httpx.OK(w, struct {
		presDetail
		Warnings []string `json:"warnings"`
	}{out.detail(), warnings})
}

type nameReq struct {
	Name *string `json:"name"`
}

func (m *Module) duplicatePresentation(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	return m.copyPresentation(w, r, p, "presentation.duplicated", false)
}

func (m *Module) saveAsTemplate(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	return m.copyPresentation(w, r, p, "presentation.saved_as_template", true)
}

// copyPresentation copies the draft of a presentation into a new, never published row.
func (m *Module) copyPresentation(w http.ResponseWriter, r *http.Request, p *core.Principal, action string, asTemplate bool) error {
	src := r.PathValue("id")
	var req nameReq
	if asTemplate {
		if err := httpx.Decode(w, r, 0, &req); err != nil {
			return err
		}
		if req.Name == nil {
			return httpx.Invalid("name", "required")
		}
	} else if err := modkit.DecodeOptional(w, r, 0, &req); err != nil {
		return err
	}
	newID := ids.New()
	var out presRow
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPresRow(r.Context(), tx, src)
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
		doc, err := copyOf(row.Draft, newID, name)
		if err != nil {
			return err
		}
		isTemplate := asTemplate || row.IsTemplate
		if isTemplate && !p.Unrestricted() {
			return httpx.ErrForbidden // templates serve every zone
		}
		if err := insertPresentation(r.Context(), tx, doc, isTemplate); err != nil {
			return err
		}
		if err := setCreator(r.Context(), tx, "presentations", newID, p); err != nil {
			return err
		}
		if out, err = loadPresRow(r.Context(), tx, newID); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: action, ObjectType: "presentation", ObjectID: newID,
			ObjectName: name, New: map[string]any{"from": src, "isTemplate": isTemplate}})
	})
	if err != nil {
		return err
	}
	m.emit("presentation.duplicated", "") // lists and the sidebar refresh
	return httpx.OK(w, out.detail())
}

// playlistsUsing lists the playlists whose draft or published version contains the id
// (presentation items and slide items). Deleted (archived) playlists do not count: users
// cannot see them, and restoring one re-validates its references
// (restore_references_missing). Ids are UUIDs, so a quoted-id match is exact.
func playlistsUsing(ctx context.Context, q modkit.Queryer, id string) ([]namedRef, error) {
	return queryRefs(ctx, q, `SELECT pl.id, pl.name FROM playlists pl WHERE pl.archived_at IS NULL AND (instr(pl.draft, ?1) > 0 OR EXISTS (
		SELECT 1 FROM playlist_versions v WHERE v.playlist_id = pl.id AND v.version = pl.published_version AND instr(v.doc, ?1) > 0))
		ORDER BY pl.name COLLATE NOCASE, pl.id`, `"`+id+`"`)
}

// checkSlidesInUse refuses a publish that would drop slides which published playlists play
// as single slides: the playlist would silently lose the item on every screen. Archived
// playlists are not distributed; restoring them re-validates their references.
func checkSlidesInUse(ctx context.Context, tx *sql.Tx, row presRow, doc *contracts.Presentation) error {
	if !row.PublishedVersion.Valid {
		return nil // nothing published yet, so no published playlist can play its slides
	}
	keep := make(map[string]bool, len(doc.Slides))
	for _, s := range doc.Slides {
		keep[s.ID] = true
	}
	// Single slides are items or cells of further tracks.
	rows, err := tx.QueryContext(ctx, `SELECT name, ref FROM (
		SELECT pl.name AS name, pl.id AS id, COALESCE(json_extract(j.value, '$.refId'), '') AS ref FROM playlists pl
			JOIN playlist_versions v ON v.playlist_id = pl.id AND v.version = pl.published_version
			JOIN json_each(v.doc, '$.items') j
			WHERE pl.archived_at IS NULL AND json_extract(j.value, '$.kind') = 'slide' AND json_extract(j.value, '$.presentationId') = ?1
		UNION ALL
		SELECT pl.name, pl.id, COALESCE(json_extract(c.value, '$.refId'), '') FROM playlists pl
			JOIN playlist_versions v ON v.playlist_id = pl.id AND v.version = pl.published_version
			JOIN json_each(v.doc, '$.items') j
			JOIN json_each(j.value, '$.cells') c
			WHERE pl.archived_at IS NULL AND json_type(c.value) = 'object' AND json_extract(c.value, '$.kind') = 'slide'
				AND json_extract(c.value, '$.presentationId') = ?1)
		ORDER BY name COLLATE NOCASE, id`, row.ID)
	if err != nil {
		return err
	}
	var playlists, slides []string
	seenPl, seenSl := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var name, slide string
		if err := rows.Scan(&name, &slide); err != nil {
			rows.Close()
			return err
		}
		if keep[slide] {
			continue
		}
		if !seenPl[name] {
			seenPl[name] = true
			playlists = append(playlists, name)
		}
		if !seenSl[slide] {
			seenSl[slide] = true
			slides = append(slides, slide)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(slides) == 0 {
		return nil
	}
	// Name the slides as the published version (the one the playlists play) calls them.
	names := make([]string, len(slides))
	copy(names, slides)
	if raw, err := publishedDoc(ctx, tx, "presentation_versions", "presentation_id", row.ID, row.PublishedVersion.Int64); err == nil {
		var pub contracts.Presentation
		if json.Unmarshal([]byte(raw), &pub) == nil {
			byID := map[string]string{}
			for _, s := range pub.Slides {
				byID[s.ID] = s.Name
			}
			for i, id := range slides {
				if n := strings.TrimSpace(byID[id]); n != "" {
					names[i] = n
				}
			}
		}
	} else if !errors.Is(err, httpx.ErrNotFound) {
		return err
	}
	return errSlideInUse.With("playlists", playlists).With("slides", slides).With("slideNames", names)
}

// archivePresentation serves DELETE and POST …/archive: a presentation is never removed, it
// is archived – unless a playlist still uses it.
func (m *Module) archivePresentation(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	archived := false
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPresRow(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if row.Archived {
			return nil
		}
		used, err := playlistsUsing(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if len(used) > 0 {
			return errPresInUse.With("playlists", refNames(used))
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE presentations SET archived_at = ?, updated_at = ? WHERE id = ?`, db.Now(), db.Now(), id); err != nil {
			return err
		}
		archived = true
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "presentation.archived", ObjectType: "presentation",
			ObjectID: id, ObjectName: row.Name, Old: map[string]any{"archived": false}, New: map[string]any{"archived": true}})
	})
	if err != nil {
		return err
	}
	if archived {
		m.emit("presentation.archived", id)
	}
	return httpx.NoContent(w)
}

// restorePresentation brings an archived presentation back. Its published version is what
// playlists distribute, so it must not reference content that disappeared while it was
// archived (409 restore_references_missing); problems of the draft only come back as
// warnings (the next publish validates the draft anyway).
func (m *Module) restorePresentation(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var out presRow
	restored := false
	warnings := []string{}
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPresRow(r.Context(), tx, id)
		if err != nil {
			return err
		}
		out = row
		if !row.Archived {
			return nil
		}
		res := m.resolver(r.Context(), tx)
		if row.PublishedVersion.Valid {
			raw, err := publishedDoc(r.Context(), tx, "presentation_versions", "presentation_id", id, row.PublishedVersion.Int64)
			if err != nil {
				return err
			}
			var pub contracts.Presentation
			if err := json.Unmarshal([]byte(raw), &pub); err != nil {
				return err
			}
			missing, err := danglingDocRefs(r.Context(), tx, res, &pub)
			if err != nil {
				return err
			}
			if len(missing) > 0 {
				return errRestoreRefs.With("references", missing)
			}
		}
		var draft contracts.Presentation
		if err := json.Unmarshal([]byte(row.Draft), &draft); err != nil {
			return err
		}
		if warnings, err = danglingDocRefs(r.Context(), tx, res, &draft); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE presentations SET archived_at = NULL, updated_at = ? WHERE id = ?`, db.Now(), id); err != nil {
			return err
		}
		restored = true
		if out, err = loadPresRow(r.Context(), tx, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "presentation.restored", ObjectType: "presentation",
			ObjectID: id, ObjectName: row.Name, Old: map[string]any{"archived": true}, New: map[string]any{"archived": false}})
	})
	if err != nil {
		return err
	}
	if restored {
		m.emit("presentation.restored", id)
	}
	return httpx.OK(w, struct {
		presDetail
		Warnings []string `json:"warnings"`
	}{out.detail(), warnings})
}

type resizeReq struct {
	Revision int64 `json:"revision"`
	Width    int   `json:"width"`
	Height   int   `json:"height"`
}

// resizePresentation changes the design resolution of the draft and scales its content.
func (m *Module) resizePresentation(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req resizeReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.Width < contracts.MinDesignSize || req.Width > contracts.MaxDesignSize {
		return httpx.Invalid("width", "out_of_range")
	}
	if req.Height < contracts.MinDesignSize || req.Height > contracts.MaxDesignSize {
		return httpx.Invalid("height", "out_of_range")
	}
	var out presRow
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPresRow(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if row.Archived {
			return errPresArchived
		}
		if req.Revision != 0 && req.Revision != row.DraftRevision {
			return conflictWithRevision(row.DraftRevision)
		}
		doc, err := copyOf(row.Draft, id, row.Name)
		if err != nil {
			return err
		}
		oldW, oldH := doc.Width, doc.Height
		if oldW == req.Width && oldH == req.Height {
			out = row
			return nil
		}
		if err := Resize(&doc, req.Width, req.Height); err != nil {
			return httpx.Invalid("width", "out_of_range")
		}
		if err := m.validateDoc(r.Context(), tx, &doc); err != nil {
			return err
		}
		if out, err = writeDraft(r.Context(), tx, row, doc); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "presentation.resized", ObjectType: "presentation",
			ObjectID: id, ObjectName: row.Name, Old: map[string]any{"width": oldW, "height": oldH},
			New: map[string]any{"width": req.Width, "height": req.Height}})
	})
	if err != nil {
		return err
	}
	return httpx.OK(w, out.detail())
}
