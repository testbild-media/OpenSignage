// Package access decides what operators limited to selected zones may see and change of
// shared content. A playlist runs in the zones whose schedule rules (zone and default rules,
// display group playlists) or closed playlists (zone, display group) use it; a global rule
// makes it run everywhere. A presentation runs where the playlists run that contain it (draft
// or published). Admins and operators of all zones may see and change everything.
package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"

	"opensignage/internal/server/core"
	"opensignage/internal/server/modkit"
)

// Usage maps content to the zones it runs in.
type Usage struct {
	plZones  map[string]map[string]bool // playlist → zones
	plGlobal map[string]bool            // playlist used by a global rule
	presPls  map[string]map[string]bool // presentation → playlists containing it
}

// Load reads the usage of all playlists and presentations.
func Load(ctx context.Context, q modkit.Queryer) (*Usage, error) {
	u := &Usage{plZones: map[string]map[string]bool{}, plGlobal: map[string]bool{}, presPls: map[string]map[string]bool{}}
	add := func(m map[string]map[string]bool, k, v string) {
		if k == "" || v == "" {
			return
		}
		if m[k] == nil {
			m[k] = map[string]bool{}
		}
		m[k][v] = true
	}
	// Disabled rules count too: enabling them must not change who may edit the content.
	rows, err := q.QueryContext(ctx, `SELECT scope, COALESCE(zone_id, ''), COALESCE(playlist_id, '') FROM schedule_rules
		UNION ALL SELECT r.scope, g.zone_id, sg.playlist_id FROM schedule_group_playlists sg
			JOIN schedule_rules r ON r.id = sg.rule_id JOIN display_groups g ON g.id = sg.group_id
		UNION ALL SELECT 'zone', id, closed_playlist_id FROM zones WHERE closed_playlist_id IS NOT NULL
		UNION ALL SELECT 'zone', zone_id, closed_playlist_id FROM display_groups WHERE closed_playlist_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var scope, zone, pl string
		if err := rows.Scan(&scope, &zone, &pl); err != nil {
			rows.Close()
			return nil, err
		}
		if scope == "global" && pl != "" {
			u.plGlobal[pl] = true
		} else {
			add(u.plZones, pl, zone)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	docs, err := q.QueryContext(ctx, `SELECT id, draft FROM playlists
		UNION ALL SELECT p.id, v.doc FROM playlists p JOIN playlist_versions v ON v.playlist_id = p.id AND v.version = p.published_version`)
	if err != nil {
		return nil, err
	}
	defer docs.Close()
	for docs.Next() {
		var id, raw string
		if err := docs.Scan(&id, &raw); err != nil {
			return nil, err
		}
		for _, pres := range Presentations(raw) {
			add(u.presPls, pres, id)
		}
	}
	return u, docs.Err()
}

// Presentations returns the presentations a playlist document contains (items and cells).
func Presentations(doc string) []string {
	var pl struct {
		Items []struct {
			Kind           string `json:"kind"`
			RefID          string `json:"refId"`
			PresentationID string `json:"presentationId"`
			Cells          []*struct {
				Kind           string `json:"kind"`
				PresentationID string `json:"presentationId"`
			} `json:"cells"`
		} `json:"items"`
	}
	if json.Unmarshal([]byte(doc), &pl) != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	keep := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, it := range pl.Items {
		switch it.Kind {
		case "presentation":
			keep(it.RefID)
		case "slide":
			keep(it.PresentationID)
		}
		for _, c := range it.Cells {
			if c != nil && c.Kind == "slide" {
				keep(c.PresentationID)
			}
		}
	}
	return out
}

// PlaylistZones returns the zones a playlist runs in (sorted) and whether a global rule uses it.
func (u *Usage) PlaylistZones(id string) ([]string, bool) {
	return keys(u.plZones[id]), u.plGlobal[id]
}

// PresentationZones returns the zones a presentation runs in and whether it runs everywhere.
func (u *Usage) PresentationZones(id string) ([]string, bool) {
	zones := map[string]bool{}
	global := false
	for pl := range u.presPls[id] {
		for z := range u.plZones[pl] {
			zones[z] = true
		}
		global = global || u.plGlobal[pl]
	}
	return keys(zones), global
}

// PlaylistsWith returns the playlists that contain a presentation (sorted).
func (u *Usage) PlaylistsWith(presentationID string) []string { return keys(u.presPls[presentationID]) }

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Rights of a principal on one presentation or playlist.
type Rights struct {
	View bool `json:"view"`
	Edit bool `json:"edit"`
	// Shared: visible but not editable because it also runs in zones p may not edit
	// (or everywhere); "copy for my zone" gives an own copy.
	Shared bool `json:"shared"`
}

// Decide applies the rules to content running in zones (global: in every zone), created by
// createdBy (user id, "" unknown). Operators limited to zones see content that runs in one of
// their zones or that they created; they may change it when it runs only in zones they may
// edit, or when they created it and it runs nowhere yet.
func Decide(p *core.Principal, zones []string, global bool, createdBy string) Rights {
	if p.Unrestricted() {
		return Rights{View: true, Edit: true}
	}
	if !p.Restricted() {
		return Rights{}
	}
	own := createdBy != "" && createdBy == p.UserID
	view := own || (global && len(p.Zones) > 0)
	editAll := len(zones) > 0 && !global
	for _, z := range zones {
		if p.CanViewZone(z) {
			view = true
		}
		if !p.CanEditZone(z) {
			editAll = false
		}
	}
	edit := editAll || (own && len(zones) == 0 && !global)
	return Rights{View: view, Edit: edit, Shared: view && !edit && (len(zones) > 0 || global)}
}

// Creator returns created_by of a presentation or playlist ("" when unknown).
func Creator(ctx context.Context, q modkit.Queryer, table, id string) (string, error) {
	var by sql.NullString
	err := q.QueryRowContext(ctx, `SELECT created_by FROM `+table+` WHERE id = ?`, id).Scan(&by)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return by.String, err
}
