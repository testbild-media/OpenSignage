package catalog

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"opensignage/internal/common/ids"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

// Zone is the API representation of a zone.
type Zone struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	State            string `json:"state"`
	ClosedPlaylistID string `json:"closedPlaylistId,omitempty"`
	TickerTemplateID string `json:"tickerTemplateId,omitempty"`
	Sort             int    `json:"sort"`
	DeviceCount      int    `json:"deviceCount"`
	Revision         int64  `json:"revision"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
	// Groups are the display groups of the zone.
	Groups []DisplayGroup `json:"groups"`
}

// sameZoneFields compares the stored fields of two zones (not the groups).
func sameZoneFields(a, b Zone) bool {
	return a.Name == b.Name && a.ClosedPlaylistID == b.ClosedPlaylistID && a.TickerTemplateID == b.TickerTemplateID && a.Sort == b.Sort
}

func loadZones(ctx context.Context, q modkit.Queryer, where string, args ...any) ([]Zone, error) {
	rows, err := q.QueryContext(ctx, `SELECT z.id, z.name, z.state, COALESCE(z.closed_playlist_id, ''), COALESCE(z.ticker_template_id, ''),
		z.sort, (SELECT COUNT(*) FROM devices d WHERE d.zone_id = z.id), z.revision, z.created_at, z.updated_at
		FROM zones z `+where+` ORDER BY z.sort, z.name COLLATE NOCASE, z.id`, args...)
	if err != nil {
		return nil, err
	}
	out := []Zone{}
	idx := map[string]int{}
	for rows.Next() {
		var z Zone
		if err := rows.Scan(&z.ID, &z.Name, &z.State, &z.ClosedPlaylistID, &z.TickerTemplateID, &z.Sort, &z.DeviceCount, &z.Revision, &z.CreatedAt, &z.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		z.Groups = []DisplayGroup{}
		idx[z.ID] = len(out)
		out = append(out, z)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	gwhere, gargs := "", []any{}
	if len(out) == 1 {
		gwhere, gargs = `WHERE g.zone_id = ?`, []any{out[0].ID}
	}
	groups, err := loadGroups(ctx, q, gwhere, gargs...)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if i, ok := idx[g.ZoneID]; ok {
			out[i].Groups = append(out[i].Groups, g)
		}
	}
	return out, nil
}

func loadZone(ctx context.Context, q modkit.Queryer, id string) (Zone, error) {
	zs, err := loadZones(ctx, q, `WHERE z.id = ?`, id)
	if err != nil {
		return Zone{}, err
	}
	if len(zs) == 0 {
		return Zone{}, httpx.ErrNotFound
	}
	return zs[0], nil
}

func (m *Module) listZones(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	all, err := loadZones(r.Context(), m.app.DB.R, "")
	if err != nil {
		return err
	}
	out := make([]Zone, 0, len(all))
	for _, z := range all {
		if p.CanViewZone(z.ID) {
			out = append(out, z)
		}
	}
	return httpx.OK(w, map[string]any{"zones": out})
}

type zoneReq struct {
	Revision         int64              `json:"revision"`
	Name             *string            `json:"name"`
	ClosedPlaylistID modkit.Opt[string] `json:"closedPlaylistId"`
	TickerTemplateID modkit.Opt[string] `json:"tickerTemplateId"`
	Sort             *int               `json:"sort"`
}

// checkZoneRefs verifies the closed playlist (exists, not archived) and the ticker template.
func checkZoneRefs(ctx context.Context, tx *sql.Tx, z *Zone) error {
	var one int
	if z.ClosedPlaylistID != "" {
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM playlists WHERE id = ? AND archived_at IS NULL`, z.ClosedPlaylistID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.Invalid("closedPlaylistId", "unknown_playlist")
		}
		if err != nil {
			return err
		}
	}
	if z.TickerTemplateID != "" {
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM ticker_templates WHERE id = ?`, z.TickerTemplateID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.Invalid("tickerTemplateId", "unknown_ticker_template")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (req *zoneReq) apply(z *Zone) error {
	if req.Name != nil {
		name, ok := modkit.CleanName(*req.Name, maxNameRunes)
		if !ok {
			return httpx.Invalid("name", "invalid")
		}
		z.Name = name
	}
	if req.ClosedPlaylistID.Set {
		z.ClosedPlaylistID = ""
		if req.ClosedPlaylistID.Has() {
			z.ClosedPlaylistID = req.ClosedPlaylistID.Val
		}
	}
	if req.TickerTemplateID.Set {
		z.TickerTemplateID = ""
		if req.TickerTemplateID.Has() {
			z.TickerTemplateID = req.TickerTemplateID.Val
		}
	}
	if req.Sort != nil {
		z.Sort = *req.Sort
	}
	return nil
}

func (m *Module) createZone(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req zoneReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.Name == nil {
		return httpx.Invalid("name", "required")
	}
	z := Zone{ID: ids.New(), State: "OPEN"}
	if err := req.apply(&z); err != nil {
		return err
	}
	var out Zone
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		if err := checkZoneRefs(r.Context(), tx, &z); err != nil {
			return err
		}
		if req.Sort == nil {
			if err := tx.QueryRowContext(r.Context(), `SELECT COALESCE(MAX(sort), -1) + 1 FROM zones`).Scan(&z.Sort); err != nil {
				return err
			}
		}
		now := db.Now()
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO zones(id, name, state, closed_playlist_id, ticker_template_id, sort, created_at, updated_at, clock_key)
			VALUES (?, ?, 'OPEN', NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?)`,
			z.ID, z.Name, z.ClosedPlaylistID, z.TickerTemplateID, z.Sort, now, now, ids.Token(32)); err != nil {
			return err
		}
		var err error
		if out, err = loadZone(r.Context(), tx, z.ID); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "zone.created", ObjectType: "zone", ObjectID: z.ID,
			ObjectName: z.Name, ZoneID: z.ID, New: out})
	})
	if err != nil {
		return err
	}
	m.emit(core.TopicZones, "zone.created", out.ID, out.ID)
	m.emit(core.TopicLive, "zone.created", out.ID, out.ID)
	return httpx.OK(w, out)
}

func (m *Module) updateZone(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req zoneReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.Revision <= 0 {
		return httpx.Invalid("revision", "required")
	}
	var old, out Zone
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var err error
		if old, err = loadZone(r.Context(), tx, id); err != nil {
			return err
		}
		if old.Revision != req.Revision {
			return conflictWithRevision(old.Revision)
		}
		next := old
		if err := req.apply(&next); err != nil {
			return err
		}
		if !p.Unrestricted() {
			// Operators limited to zones set the closed playlist and the ticker template of
			// zones they may edit; names and order stay with admins and operators of all zones.
			if !p.CanEditZone(id) {
				return httpx.ErrForbidden
			}
			if next.Name != old.Name || next.Sort != old.Sort {
				return httpx.ErrForbidden
			}
			if next.ClosedPlaylistID != old.ClosedPlaylistID {
				if ok, err := playlistVisible(r.Context(), tx, p, next.ClosedPlaylistID); err != nil {
					return err
				} else if !ok {
					return httpx.Invalid("closedPlaylistId", "no_access")
				}
			}
		}
		if err := checkZoneRefs(r.Context(), tx, &next); err != nil {
			return err
		}
		if sameZoneFields(next, old) {
			out = old
			return nil
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE zones SET name = ?, closed_playlist_id = NULLIF(?, ''),
			ticker_template_id = NULLIF(?, ''), sort = ?, updated_at = ?, revision = revision + 1 WHERE id = ?`,
			next.Name, next.ClosedPlaylistID, next.TickerTemplateID, next.Sort, db.Now(), id); err != nil {
			return err
		}
		if out, err = loadZone(r.Context(), tx, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "zone.updated", ObjectType: "zone", ObjectID: id,
			ObjectName: out.Name, ZoneID: id, Old: old, New: out})
	})
	if err != nil {
		return err
	}
	if out.Revision != old.Revision {
		m.emit(core.TopicZones, "zone.updated", id, id)
		m.emit(core.TopicLive, "zone.updated", id, id)
		if out.ClosedPlaylistID != old.ClosedPlaylistID {
			m.emit(core.TopicContent, "zone.closed_playlist", id, id)
		}
	}
	return httpx.OK(w, out)
}

type zoneUsageResult struct {
	Devices int `json:"devices"`
	Rules   int `json:"rules"`
	Users   int `json:"users"`
}

func zoneUsageOf(ctx context.Context, q modkit.Queryer, id string) (zoneUsageResult, error) {
	var u zoneUsageResult
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE zone_id = ?`, id).Scan(&u.Devices); err != nil {
		return u, err
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM schedule_rules WHERE zone_id = ?`, id).Scan(&u.Rules); err != nil {
		return u, err
	}
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_zones WHERE zone_id = ?`, id).Scan(&u.Users)
	return u, err
}

// zoneUsage tells the UI what a deletion would affect, so it can warn before.
func (m *Module) zoneUsage(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	id := r.PathValue("id")
	if _, err := loadZone(r.Context(), m.app.DB.R, id); err != nil {
		return err
	}
	u, err := zoneUsageOf(r.Context(), m.app.DB.R, id)
	if err != nil {
		return err
	}
	return httpx.OK(w, u)
}

// deleteZone removes a zone: devices become zone-less (foreign key SET NULL), its schedule
// rules, product overrides and user assignments are removed with it.
func (m *Module) deleteZone(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var usage zoneUsageResult
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		old, err := loadZone(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if usage, err = zoneUsageOf(r.Context(), tx, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM zones WHERE id = ?`, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "zone.deleted", ObjectType: "zone", ObjectID: id,
			ObjectName: old.Name, ZoneID: id, Old: old,
			New: map[string]any{"devicesAffected": usage.Devices, "rulesDeleted": usage.Rules}})
	})
	if err != nil {
		return err
	}
	m.emit(core.TopicZones, "zone.deleted", id, id)
	m.emit(core.TopicLive, "zone.deleted", id, id)
	m.emit(core.TopicContent, "zone.deleted", id, id)
	return httpx.OK(w, map[string]any{"devicesAffected": usage.Devices, "rulesDeleted": usage.Rules})
}
