package content

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"opensignage/internal/server/core"
	"opensignage/internal/server/httpx"
)

const (
	searchLimit  = 50
	maxQueryRune = 100
)

type searchPres struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	SlideCount       int    `json:"slideCount"`
	PublishedVersion *int64 `json:"publishedVersion"`
}

type searchSlide struct {
	PresentationID   string `json:"presentationId"`
	PresentationName string `json:"presentationName"`
	SlideID          string `json:"slideId"`
	Name             string `json:"name"`
	Index            int    `json:"index"`
}

type searchPlaylist struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ItemCount        int    `json:"itemCount"`
	PublishedVersion *int64 `json:"publishedVersion"`
}

// search feeds the playlist editor sidebar: presentations, slides (by slide name) and
// playlists whose name contains q (case-insensitive). An empty q lists presentations and
// playlists but no slides (use GET /presentations/{id} for the slides of one presentation).
func (m *Module) search(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > maxQueryRune {
		return httpx.Invalid("q", "too_long")
	}
	needle := strings.ToLower(q)
	match := func(s string) bool { return needle == "" || strings.Contains(strings.ToLower(s), needle) }
	ctx := r.Context()

	prows, err := m.app.DB.R.QueryContext(ctx, `SELECT id, name, COALESCE(json_extract(draft, '$.width'), 0), COALESCE(json_extract(draft, '$.height'), 0),
		COALESCE(json_array_length(draft, '$.slides'), 0), published_version FROM presentations
		WHERE archived_at IS NULL AND is_template = 0 ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return err
	}
	pres := []searchPres{}
	for prows.Next() {
		var s searchPres
		var pub *int64
		if err := prows.Scan(&s.ID, &s.Name, &s.Width, &s.Height, &s.SlideCount, &pub); err != nil {
			prows.Close()
			return err
		}
		if match(s.Name) && len(pres) < searchLimit {
			s.PublishedVersion = pub
			pres = append(pres, s)
		}
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return err
	}

	slides := []searchSlide{}
	if needle != "" {
		srows, err := m.app.DB.R.QueryContext(ctx, `SELECT p.id, p.name, CAST(j.key AS INTEGER), COALESCE(json_extract(j.value, '$.id'), ''),
			COALESCE(json_extract(j.value, '$.name'), '') FROM presentations p, json_each(p.draft, '$.slides') j
			WHERE p.archived_at IS NULL AND p.is_template = 0 ORDER BY p.name COLLATE NOCASE, p.id, CAST(j.key AS INTEGER)`)
		if err != nil {
			return err
		}
		for srows.Next() {
			var s searchSlide
			if err := srows.Scan(&s.PresentationID, &s.PresentationName, &s.Index, &s.SlideID, &s.Name); err != nil {
				srows.Close()
				return err
			}
			if s.Name != "" && match(s.Name) && len(slides) < searchLimit {
				slides = append(slides, s)
			}
		}
		srows.Close()
		if err := srows.Err(); err != nil {
			return err
		}
	}

	lrows, err := m.app.DB.R.QueryContext(ctx, `SELECT id, name, COALESCE(json_array_length(draft, '$.items'), 0), published_version FROM playlists
		WHERE archived_at IS NULL ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return err
	}
	defer lrows.Close()
	lists := []searchPlaylist{}
	for lrows.Next() {
		var s searchPlaylist
		var pub *int64
		if err := lrows.Scan(&s.ID, &s.Name, &s.ItemCount, &pub); err != nil {
			return err
		}
		if match(s.Name) && len(lists) < searchLimit {
			s.PublishedVersion = pub
			lists = append(lists, s)
		}
	}
	if err := lrows.Err(); err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"presentations": pres, "slides": slides, "playlists": lists})
}
