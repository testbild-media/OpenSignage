package content

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"opensignage/internal/contracts"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

// Errors of the reference checks around archiving, restoring and publishing.
var (
	errSlideInUse  = httpx.E(http.StatusConflict, "slide_in_use", "published playlists still play slides this version removes")
	errRestoreRefs = httpx.E(http.StatusConflict, "restore_references_missing",
		"the published version uses content that is gone, archived or in the trash")
)

// refList collects display names without duplicates, in first-seen order.
type refList struct {
	out  []string
	seen map[string]bool
}

func (l *refList) add(s string) {
	if l.seen == nil {
		l.seen = map[string]bool{}
	}
	if !l.seen[s] {
		l.seen[s] = true
		l.out = append(l.out, s)
	}
}

func (l *refList) list() []string {
	if l.out == nil {
		return []string{}
	}
	return l.out
}

// assetName returns the name of a media asset (also while it is in the trash) or its id
// when the asset was removed for good.
func assetName(ctx context.Context, q modkit.Queryer, id string) (string, error) {
	var name string
	err := q.QueryRowContext(ctx, `SELECT name FROM media_assets WHERE id = ?`, id).Scan(&name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return id, nil
	case err != nil:
		return "", err
	}
	return name, nil
}

// danglingDocRefs lists the references of a presentation document that no longer resolve:
// media files that are gone or in the trash, fonts, products and categories that were
// removed. Entries are display names where one is still known (media files), ids otherwise.
func danglingDocRefs(ctx context.Context, q modkit.Queryer, res *docResolver, doc *contracts.Presentation) ([]string, error) {
	var l refList
	assets, fonts := doc.References()
	for _, a := range assets {
		if _, ok := res.AssetKind(a); ok {
			continue
		}
		name, err := assetName(ctx, q, a)
		if err != nil {
			return nil, err
		}
		l.add(name)
	}
	for _, f := range fonts {
		if !res.FontExists(f) {
			l.add(f)
		}
	}
	for _, s := range doc.Slides {
		for _, e := range s.Elements {
			switch {
			case e.Type == contracts.ElProduct && e.ProductID != "":
				if !res.ProductExists(e.ProductID) {
					l.add(e.ProductID)
				}
			case e.Type == contracts.ElProductList && e.Source != nil:
				for _, id := range e.Source.IDs {
					if e.Source.Kind == "products" && !res.ProductExists(id) || e.Source.Kind == "categories" && !res.CategoryExists(id) {
						l.add(id)
					}
				}
			}
		}
	}
	if res.err != nil {
		return nil, res.err
	}
	return l.list(), nil
}

// danglingPlaylistRefs lists what a playlist plays that is no longer playable: presentations
// that are gone, archived, templates or unpublished, slides missing from the published
// presentation, and media files that are gone or in the trash.
func danglingPlaylistRefs(ctx context.Context, q modkit.Queryer, res *docResolver, pl *contracts.Playlist) ([]string, error) {
	var l refList
	type presInfo struct {
		name   string
		ok     bool
		active sql.NullInt64 // published version of a usable presentation
	}
	pres := map[string]presInfo{}
	lookup := func(id string) (presInfo, error) {
		if p, ok := pres[id]; ok {
			return p, nil
		}
		var p presInfo
		var archived, tmpl int
		var pub sql.NullInt64
		err := q.QueryRowContext(ctx, `SELECT name, archived_at IS NOT NULL, is_template, published_version FROM presentations WHERE id = ?`, id).
			Scan(&p.name, &archived, &tmpl, &pub)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			p.name = id
		case err != nil:
			return p, err
		default:
			p.ok = archived == 0 && tmpl == 0 && pub.Valid
			p.active = pub
		}
		pres[id] = p
		return p, nil
	}
	for _, it := range pl.Refs() {
		switch it.Kind {
		case contracts.ItemPresentation:
			p, err := lookup(it.RefID)
			if err != nil {
				return nil, err
			}
			if !p.ok {
				l.add(p.name)
			}
		case contracts.ItemSlide:
			p, err := lookup(it.PresentationID)
			if err != nil {
				return nil, err
			}
			if !p.ok {
				l.add(p.name)
				continue
			}
			var one int
			err = q.QueryRowContext(ctx, `SELECT 1 FROM presentation_versions v, json_each(v.doc, '$.slides') j
				WHERE v.presentation_id = ? AND v.version = ? AND json_extract(j.value, '$.id') = ? LIMIT 1`,
				it.PresentationID, p.active.Int64, it.RefID).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				l.add(p.name + ": " + it.RefID)
				continue
			}
			if err != nil {
				return nil, err
			}
		case contracts.ItemMedia:
			if _, ok := res.MediaKind(it.RefID); ok {
				continue
			}
			name, err := assetName(ctx, q, it.RefID)
			if err != nil {
				return nil, err
			}
			l.add(name)
		}
	}
	if res.err != nil {
		return nil, res.err
	}
	return l.list(), nil
}
