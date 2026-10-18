// Package content implements presentations (draft/publish/versions/templates), playlists and
// the content search. It writes presentations, presentation_versions, playlists,
// playlist_versions and content_refs. Editing never distributes anything: only publishing,
// archiving and deleting publish a `content` event, after the transaction committed.
package content

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

// Limits.
const (
	maxDraftBytes    = 8 << 20 // presentation draft request body
	maxPlaylistBytes = 2 << 20
	maxNameRunes     = 200
	keepVersions     = 50 // published versions kept per playlist (presentations: history.go)
)

// Module is the content module.
type Module struct {
	app   *core.App
	fonts *modkit.Fonts
}

// New creates the module.
func New(app *core.App) *Module {
	return &Module{app: app, fonts: modkit.NewFonts(app.Cfg.ShareDir)}
}

// Routes registers the WebGUI API (operators and admins). Operators limited to zones get
// the routes registered with Scoped; guard and creating check their rights (rights.go).
func (m *Module) Routes(rt *core.Router) {
	op := core.RoleOperator
	view := func(k contentKind, h core.AuthedHandler) core.AuthedHandler { return m.guard(k, false, h) }
	edit := func(k contentKind, h core.AuthedHandler) core.AuthedHandler { return m.guard(k, true, h) }
	pres, pl := kindPres, kindPl
	rt.Scoped("GET /api/v1/presentations", op, m.listPresentations)
	rt.Scoped("POST /api/v1/presentations", op, creating(m.createPresentation))
	rt.Scoped("GET /api/v1/presentations/{id}", op, view(pres, m.getPresentation))
	rt.Scoped("PATCH /api/v1/presentations/{id}", op, edit(pres, m.renamePresentation))
	rt.Scoped("PUT /api/v1/presentations/{id}/draft", op, edit(pres, m.saveDraft))
	rt.Scoped("POST /api/v1/presentations/{id}/publish", op, edit(pres, m.publishPresentation))
	rt.Scoped("POST /api/v1/presentations/{id}/discard", op, edit(pres, m.discardPresentation))
	rt.Scoped("GET /api/v1/presentations/{id}/versions", op, view(pres, m.listVersions))
	rt.Scoped("POST /api/v1/presentations/{id}/versions/{v}/restore", op, edit(pres, m.restoreVersion))
	rt.Scoped("PUT /api/v1/presentations/{id}/versions/{v}/label", op, edit(pres, m.labelVersion))
	rt.Scoped("POST /api/v1/presentations/{id}/duplicate", op, view(pres, creating(m.duplicatePresentation)))
	rt.Scoped("POST /api/v1/presentations/{id}/copy-for-zone", op, m.copyPresentationForZone)
	rt.Scoped("POST /api/v1/presentations/{id}/archive", op, edit(pres, m.archivePresentation))
	rt.Scoped("POST /api/v1/presentations/{id}/restore", op, edit(pres, m.restorePresentation))
	rt.Scoped("DELETE /api/v1/presentations/{id}", op, edit(pres, m.archivePresentation))
	rt.Role("POST /api/v1/presentations/{id}/save-as-template", op, m.saveAsTemplate) // templates serve every zone
	rt.Scoped("POST /api/v1/presentations/{id}/resize", op, edit(pres, m.resizePresentation))

	rt.Scoped("GET /api/v1/playlists", op, m.listPlaylists)
	rt.Scoped("POST /api/v1/playlists", op, creating(m.createPlaylist))
	rt.Scoped("GET /api/v1/playlists/{id}", op, view(pl, m.getPlaylist))
	rt.Scoped("PATCH /api/v1/playlists/{id}", op, edit(pl, m.renamePlaylist))
	rt.Scoped("PUT /api/v1/playlists/{id}/draft", op, edit(pl, m.savePlaylistDraft))
	rt.Scoped("POST /api/v1/playlists/{id}/publish", op, edit(pl, m.publishPlaylist))
	rt.Scoped("POST /api/v1/playlists/{id}/discard", op, edit(pl, m.discardPlaylist))
	rt.Scoped("POST /api/v1/playlists/{id}/duplicate", op, view(pl, creating(m.duplicatePlaylist)))
	rt.Scoped("POST /api/v1/playlists/{id}/copy-for-zone", op, m.copyPlaylistForZone)
	rt.Scoped("POST /api/v1/playlists/{id}/restore", op, edit(pl, m.restorePlaylist))
	rt.Scoped("DELETE /api/v1/playlists/{id}", op, edit(pl, m.archivePlaylist))

	rt.Scoped("GET /api/v1/content/search", op, m.search)
}

// Stable error constructors (the UI translates `errors.<code>`).
var (
	errNeverPublished   = httpx.E(http.StatusConflict, "never_published", "there is no published version to go back to")
	errPresInUse        = httpx.E(http.StatusConflict, "presentation_in_use", "the presentation is used by playlists")
	errPlaylistInUse    = httpx.E(http.StatusConflict, "playlist_in_use", "the playlist is used by schedule rules or zones")
	errPresArchived     = httpx.E(http.StatusConflict, "presentation_archived", "the presentation is archived")
	errPlaylistArchived = httpx.E(http.StatusConflict, "playlist_archived", "the playlist is archived")
	errTemplatePublish  = httpx.E(http.StatusConflict, "template_not_publishable", "templates cannot be published")
)

func (m *Module) emit(kind, id string) {
	m.app.Bus.Publish(core.Event{Topic: core.TopicContent, Kind: kind, ID: id})
}

// audit writes an audit record for the request inside tx.
func audit(ctx context.Context, tx *sql.Tx, r *http.Request, p *core.Principal, e core.AuditEntry) error {
	return core.Audit(ctx, tx, p, httpx.ClientIP(r), e)
}

func conflictWithRevision(current int64) *httpx.Error {
	return httpx.ErrConflict.With("revision", current)
}

// ---------------------------------------------------------------- content_refs

// syncRefs replaces the asset references of one owner (presentation_draft, …).
func syncRefs(ctx context.Context, tx *sql.Tx, ownerType, ownerID string, assetIDs []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM content_refs WHERE owner_type = ? AND owner_id = ?`, ownerType, ownerID); err != nil {
		return err
	}
	for _, a := range assetIDs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO content_refs(asset_id, owner_type, owner_id) VALUES (?, ?, ?)`, a, ownerType, ownerID); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- reference resolvers

// docResolver implements contracts.Resolver and contracts.PlaylistResolver on top of the
// database. Lookups cannot return errors through the interfaces, so the first database error
// is remembered in err and must be checked by the caller after validation.
type docResolver struct {
	ctx   context.Context
	q     modkit.Queryer
	fonts *modkit.Fonts
	err   error
	cache map[string]bool
	kinds map[string]string
}

func (m *Module) resolver(ctx context.Context, q modkit.Queryer) *docResolver {
	return &docResolver{ctx: ctx, q: q, fonts: m.fonts, cache: map[string]bool{}, kinds: map[string]string{}}
}

func (r *docResolver) exists(key, query string, args ...any) bool {
	if v, ok := r.cache[key]; ok {
		return v
	}
	var one int
	err := r.q.QueryRowContext(r.ctx, query, args...).Scan(&one)
	found := err == nil
	if err != nil && err != sql.ErrNoRows && r.err == nil {
		r.err = err
	}
	r.cache[key] = found
	return found
}

// AssetKind returns the kind of a usable media asset (not trashed, not failed).
func (r *docResolver) AssetKind(id string) (string, bool) {
	if k, ok := r.kinds[id]; ok {
		return k, k != ""
	}
	var kind string
	err := r.q.QueryRowContext(r.ctx, `SELECT kind FROM media_assets WHERE id = ? AND trashed_at IS NULL AND (status <> 'failed' OR COALESCE(ready_revision, 0) > 0)`, id).Scan(&kind)
	if err != nil && err != sql.ErrNoRows && r.err == nil {
		r.err = err
	}
	r.kinds[id] = kind
	return kind, kind != ""
}

// MediaKind is AssetKind for playlist items.
func (r *docResolver) MediaKind(id string) (string, bool) { return r.AssetKind(id) }

// FontExists checks bundled and uploaded fonts.
func (r *docResolver) FontExists(family string) bool {
	if v, ok := r.cache["font:"+family]; ok {
		return v
	}
	ok, err := r.fonts.Exists(r.ctx, r.q, family)
	if err != nil && r.err == nil {
		r.err = err
	}
	r.cache["font:"+family] = ok
	return ok
}

// ProductExists reports whether the product row exists (archived products still count, so a
// draft does not become unsaveable when a product is archived).
func (r *docResolver) ProductExists(id string) bool {
	return r.exists("product:"+id, `SELECT 1 FROM products WHERE id = ?`, id)
}

// CategoryExists reports whether the category exists.
func (r *docResolver) CategoryExists(id string) bool {
	return r.exists("category:"+id, `SELECT 1 FROM categories WHERE id = ?`, id)
}

// PresentationExists reports whether a normal, non-archived presentation exists.
func (r *docResolver) PresentationExists(id string) bool {
	return r.exists("pres:"+id, `SELECT 1 FROM presentations WHERE id = ? AND archived_at IS NULL AND is_template = 0`, id)
}

// SlideExists looks the slide up in the draft of the presentation.
func (r *docResolver) SlideExists(presentationID, slideID string) bool {
	return r.exists("slide:"+presentationID+"/"+slideID,
		`SELECT 1 FROM presentations p, json_each(p.draft, '$.slides') j
		 WHERE p.id = ? AND p.archived_at IS NULL AND p.is_template = 0 AND json_extract(j.value, '$.id') = ? LIMIT 1`, presentationID, slideID)
}

// ---------------------------------------------------------------- document helpers

// normalizeDoc makes nil slices empty so the stored JSON always has arrays, never null.
func normalizeDoc(p *contracts.Presentation) {
	if p.Slides == nil {
		p.Slides = []contracts.Slide{}
	}
	for i := range p.Slides {
		s := &p.Slides[i]
		if s.Elements == nil {
			s.Elements = []contracts.Element{}
		}
		for j := range s.Elements {
			e := &s.Elements[j]
			if e.Source != nil && e.Source.IDs == nil {
				e.Source.IDs = []string{}
			}
			if e.Doc != nil {
				if e.Doc.Paragraphs == nil {
					e.Doc.Paragraphs = []contracts.Paragraph{}
				}
				for k := range e.Doc.Paragraphs {
					if e.Doc.Paragraphs[k].Runs == nil {
						e.Doc.Paragraphs[k].Runs = []contracts.Run{}
					}
				}
			}
		}
	}
}

// fingerprint hashes the canonical JSON of a document without its name, so renaming alone
// never counts as an unpublished change.
func fingerprint(p contracts.Presentation) (string, error) {
	p.Name = ""
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func fingerprintRaw(raw string) (string, error) {
	var p contracts.Presentation
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return "", err
	}
	return fingerprint(p)
}

// validateDoc validates a presentation against the database. A lookup error wins over
// validation problems (it would make them unreliable).
func (m *Module) validateDoc(ctx context.Context, q modkit.Queryer, p *contracts.Presentation) error {
	res := m.resolver(ctx, q)
	err := p.Validate(res)
	if res.err != nil {
		return res.err
	}
	return err
}

func (m *Module) validatePlaylistDoc(ctx context.Context, q modkit.Queryer, p *contracts.Playlist) error {
	res := m.resolver(ctx, q)
	err := p.Validate(res)
	if res.err != nil {
		return res.err
	}
	return err
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
