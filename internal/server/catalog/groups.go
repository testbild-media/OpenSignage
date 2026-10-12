package catalog

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"opensignage/internal/common/ids"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

// DisplayGroup is a named row of screens inside a zone. The screens keep their place
// (devices.position); the group may have its own closed playlist.
type DisplayGroup struct {
	ID               string `json:"id"`
	ZoneID           string `json:"zoneId"`
	Name             string `json:"name"`
	ClosedPlaylistID string `json:"closedPlaylistId,omitempty"`
	Sort             int    `json:"sort"`
	DeviceCount      int    `json:"deviceCount"`
	Revision         int64  `json:"revision"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
}

// MaxGroupsPerZone bounds the display groups of one zone.
const MaxGroupsPerZone = 50

func loadGroups(ctx context.Context, q modkit.Queryer, where string, args ...any) ([]DisplayGroup, error) {
	rows, err := q.QueryContext(ctx, `SELECT g.id, g.zone_id, g.name, COALESCE(g.closed_playlist_id, ''), g.sort,
		(SELECT COUNT(*) FROM devices d WHERE d.group_id = g.id), g.revision, g.created_at, g.updated_at
		FROM display_groups g `+where+` ORDER BY g.zone_id, g.sort, g.name COLLATE NOCASE, g.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DisplayGroup{}
	for rows.Next() {
		var g DisplayGroup
		if err := rows.Scan(&g.ID, &g.ZoneID, &g.Name, &g.ClosedPlaylistID, &g.Sort, &g.DeviceCount, &g.Revision,
			&g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func loadGroup(ctx context.Context, q modkit.Queryer, zoneID, id string) (DisplayGroup, error) {
	gs, err := loadGroups(ctx, q, `WHERE g.id = ? AND g.zone_id = ?`, id, zoneID)
	if err != nil {
		return DisplayGroup{}, err
	}
	if len(gs) == 0 {
		return DisplayGroup{}, httpx.ErrNotFound
	}
	return gs[0], nil
}

type groupReq struct {
	Revision         int64              `json:"revision"`
	Name             *string            `json:"name"`
	ClosedPlaylistID modkit.Opt[string] `json:"closedPlaylistId"`
	Sort             *int               `json:"sort"`
}

func (req *groupReq) apply(g *DisplayGroup) error {
	if req.Name != nil {
		name, ok := modkit.CleanName(*req.Name, maxNameRunes)
		if !ok {
			return httpx.Invalid("name", "invalid")
		}
		g.Name = name
	}
	if req.ClosedPlaylistID.Set {
		g.ClosedPlaylistID = ""
		if req.ClosedPlaylistID.Has() {
			g.ClosedPlaylistID = req.ClosedPlaylistID.Val
		}
	}
	if req.Sort != nil {
		g.Sort = *req.Sort
	}
	return nil
}

// checkGroup verifies a unique name in the zone and the closed playlist.
func checkGroup(ctx context.Context, tx *sql.Tx, g *DisplayGroup) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM display_groups WHERE zone_id = ? AND id <> ? AND lower(name) = lower(?)`,
		g.ZoneID, g.ID, strings.TrimSpace(g.Name)).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return errGroupNameTaken
	}
	if g.ClosedPlaylistID != "" {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM playlists WHERE id = ? AND archived_at IS NULL`, g.ClosedPlaylistID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.Invalid("closedPlaylistId", "unknown_playlist")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// groupChanged publishes the events of a changed group: zone pages refresh, deployments
// rebuild (groups are part of the manifest) and devices receive their configuration.
func (m *Module) groupChanged(kind, id, zoneID string) {
	m.emit(core.TopicZones, kind, id, zoneID)
	m.emit(core.TopicContent, kind, id, zoneID)
}

func (m *Module) createGroup(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	zoneID := r.PathValue("id")
	var req groupReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.Name == nil {
		return httpx.Invalid("name", "required")
	}
	g := DisplayGroup{ID: ids.New(), ZoneID: zoneID}
	if err := req.apply(&g); err != nil {
		return err
	}
	var out DisplayGroup
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		if _, err := loadZone(r.Context(), tx, zoneID); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM display_groups WHERE zone_id = ?`, zoneID).Scan(&count); err != nil {
			return err
		}
		if count >= MaxGroupsPerZone {
			return errTooManyGroups
		}
		if err := checkGroup(r.Context(), tx, &g); err != nil {
			return err
		}
		if req.Sort == nil {
			if err := tx.QueryRowContext(r.Context(), `SELECT COALESCE(MAX(sort), -1) + 1 FROM display_groups WHERE zone_id = ?`, zoneID).Scan(&g.Sort); err != nil {
				return err
			}
		}
		now := db.Now()
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO display_groups(id, zone_id, name, closed_playlist_id, sort, created_at, updated_at)
			VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?)`, g.ID, zoneID, g.Name, g.ClosedPlaylistID, g.Sort, now, now); err != nil {
			return err
		}
		var err error
		if out, err = loadGroup(r.Context(), tx, zoneID, g.ID); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "group.created", ObjectType: "display_group", ObjectID: g.ID,
			ObjectName: g.Name, ZoneID: zoneID, New: out})
	})
	if err != nil {
		return err
	}
	m.groupChanged("group.created", out.ID, zoneID)
	return httpx.OK(w, out)
}

func (m *Module) updateGroup(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	zoneID, id := r.PathValue("id"), r.PathValue("gid")
	var req groupReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.Revision <= 0 {
		return httpx.Invalid("revision", "required")
	}
	var old, out DisplayGroup
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var err error
		if old, err = loadGroup(r.Context(), tx, zoneID, id); err != nil {
			return err
		}
		if old.Revision != req.Revision {
			return conflictWithRevision(old.Revision)
		}
		next := old
		if err := req.apply(&next); err != nil {
			return err
		}
		if next == old {
			out = old
			return nil
		}
		if err := checkGroup(r.Context(), tx, &next); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE display_groups SET name = ?, closed_playlist_id = NULLIF(?, ''), sort = ?,
			revision = revision + 1, updated_at = ? WHERE id = ?`, next.Name, next.ClosedPlaylistID, next.Sort, db.Now(), id); err != nil {
			return err
		}
		if out, err = loadGroup(r.Context(), tx, zoneID, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "group.updated", ObjectType: "display_group", ObjectID: id,
			ObjectName: out.Name, ZoneID: zoneID, Old: old, New: out})
	})
	if err != nil {
		return err
	}
	if out.Revision != old.Revision {
		m.groupChanged("group.updated", id, zoneID)
	}
	return httpx.OK(w, out)
}

// deleteGroup removes a group that no schedule rule uses. Its screens join the screens
// without group and get the next free places there (in their previous order).
func (m *Module) deleteGroup(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	zoneID, id := r.PathValue("id"), r.PathValue("gid")
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		old, err := loadGroup(r.Context(), tx, zoneID, id)
		if err != nil {
			return err
		}
		rules, err := queryRefs(r.Context(), tx, `SELECT r.id, r.name FROM schedule_rules r
			WHERE r.id IN (SELECT rule_id FROM schedule_group_playlists WHERE group_id = ?) ORDER BY r.name COLLATE NOCASE, r.id`, id)
		if err != nil {
			return err
		}
		if len(rules) > 0 {
			names := make([]string, len(rules))
			for i, ru := range rules {
				names[i] = ru.Name
			}
			return errGroupInUse.With("rules", names)
		}
		rows, err := tx.QueryContext(r.Context(), `SELECT id FROM devices WHERE group_id = ? ORDER BY COALESCE(position, 1000000), name COLLATE NOCASE, id`, id)
		if err != nil {
			return err
		}
		var moved []string
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				rows.Close()
				return err
			}
			moved = append(moved, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM display_groups WHERE id = ?`, id); err != nil {
			return err
		}
		for _, d := range moved {
			pos, err := modkit.NextPosition(r.Context(), tx, zoneID, "", d)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(r.Context(), `UPDATE devices SET group_id = NULL, position = ?, updated_at = ? WHERE id = ?`,
				pos, db.Now(), d); err != nil {
				return err
			}
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "group.deleted", ObjectType: "display_group", ObjectID: id,
			ObjectName: old.Name, ZoneID: zoneID, Old: old, New: map[string]any{"devicesUngrouped": len(moved)}})
	})
	if err != nil {
		return err
	}
	m.groupChanged("group.deleted", id, zoneID)
	m.emit(core.TopicDevices, "device.updated", "", zoneID)
	return httpx.NoContent(w)
}
