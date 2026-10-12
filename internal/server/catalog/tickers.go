package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"

	"opensignage/internal/common/ids"
	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

// TickerTemplateRecord is the API representation: a named TickerTemplate (ARCHITECTURE §5.6).
type TickerTemplateRecord struct {
	ID        string                   `json:"id"`
	Name      string                   `json:"name"`
	Template  contracts.TickerTemplate `json:"template"`
	Revision  int64                    `json:"revision"`
	CreatedAt string                   `json:"createdAt"`
	UpdatedAt string                   `json:"updatedAt"`
}

func loadTickerTemplates(ctx context.Context, q modkit.Queryer, where string, args ...any) ([]TickerTemplateRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, style, revision, created_at, updated_at FROM ticker_templates `+where+` ORDER BY name COLLATE NOCASE, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TickerTemplateRecord{}
	for rows.Next() {
		var t TickerTemplateRecord
		var style string
		if err := rows.Scan(&t.ID, &t.Name, &style, &t.Revision, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(style), &t.Template); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func loadTickerTemplate(ctx context.Context, q modkit.Queryer, id string) (TickerTemplateRecord, error) {
	ts, err := loadTickerTemplates(ctx, q, `WHERE id = ?`, id)
	if err != nil {
		return TickerTemplateRecord{}, err
	}
	if len(ts) == 0 {
		return TickerTemplateRecord{}, httpx.ErrNotFound
	}
	return ts[0], nil
}

func (m *Module) listTickerTemplates(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	ts, err := loadTickerTemplates(r.Context(), m.app.DB.R, "")
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"tickerTemplates": ts, "default": contracts.DefaultTickerTemplate()})
}

type tickerTemplateReq struct {
	Revision int64                     `json:"revision"`
	Name     *string                   `json:"name"`
	Template *contracts.TickerTemplate `json:"template"`
}

// validateTemplate checks the template including the font lookup (bundled or uploaded).
func (m *Module) validateTemplate(ctx context.Context, q modkit.Queryer, t *contracts.TickerTemplate) error {
	var lookupErr error
	err := t.Validate(func(family string) bool {
		ok, err := m.fonts.Exists(ctx, q, family)
		if err != nil {
			lookupErr = err
		}
		return ok
	})
	if lookupErr != nil {
		return lookupErr
	}
	return err
}

func (m *Module) createTickerTemplate(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req tickerTemplateReq
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
	if req.Template == nil {
		return httpx.Invalid("template", "required")
	}
	if err := m.validateTemplate(r.Context(), m.app.DB.R, req.Template); err != nil {
		return err
	}
	style, err := json.Marshal(req.Template)
	if err != nil {
		return err
	}
	id := ids.New()
	var out TickerTemplateRecord
	err = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		now := db.Now()
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO ticker_templates(id, name, style, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			id, name, string(style), now, now); err != nil {
			return err
		}
		var err error
		if out, err = loadTickerTemplate(r.Context(), tx, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "ticker_template.created", ObjectType: "ticker_template",
			ObjectID: id, ObjectName: name, New: out})
	})
	if err != nil {
		return err
	}
	m.emit(core.TopicZones, "ticker_template.created", out.ID, "")
	return httpx.OK(w, out)
}

func (m *Module) updateTickerTemplate(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req tickerTemplateReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.Revision <= 0 {
		return httpx.Invalid("revision", "required")
	}
	var name string
	if req.Name != nil {
		var ok bool
		if name, ok = modkit.CleanName(*req.Name, maxNameRunes); !ok {
			return httpx.Invalid("name", "invalid")
		}
	}
	if req.Template != nil {
		if err := m.validateTemplate(r.Context(), m.app.DB.R, req.Template); err != nil {
			return err
		}
	}
	var old, out TickerTemplateRecord
	var zoneIDs []string
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var err error
		if old, err = loadTickerTemplate(r.Context(), tx, id); err != nil {
			return err
		}
		if old.Revision != req.Revision {
			return conflictWithRevision(old.Revision)
		}
		next := old
		if req.Name != nil {
			next.Name = name
		}
		if req.Template != nil {
			next.Template = *req.Template
		}
		if next.Name == old.Name && next.Template == old.Template {
			out = old
			return nil
		}
		style, err := json.Marshal(next.Template)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE ticker_templates SET name = ?, style = ?, updated_at = ?, revision = revision + 1 WHERE id = ?`,
			next.Name, string(style), db.Now(), id); err != nil {
			return err
		}
		if out, err = loadTickerTemplate(r.Context(), tx, id); err != nil {
			return err
		}
		if zoneIDs, err = queryIDs(r.Context(), tx, `SELECT id FROM zones WHERE ticker_template_id = ? ORDER BY id`, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "ticker_template.updated", ObjectType: "ticker_template",
			ObjectID: id, ObjectName: out.Name, Old: old, New: out})
	})
	if err != nil {
		return err
	}
	for _, z := range zoneIDs {
		m.emit(core.TopicLive, "ticker_template.updated", id, z)
	}
	m.emit(core.TopicZones, "ticker_template.updated", id, "")
	return httpx.OK(w, out)
}

func (m *Module) deleteTickerTemplate(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		old, err := loadTickerTemplate(r.Context(), tx, id)
		if err != nil {
			return err
		}
		zs, err := queryRefs(r.Context(), tx, `SELECT id, name FROM zones WHERE ticker_template_id = ? ORDER BY name, id`, id)
		if err != nil {
			return err
		}
		rs, err := queryRefs(r.Context(), tx, `SELECT id, name FROM schedule_rules WHERE ticker_template_id = ? ORDER BY name, id`, id)
		if err != nil {
			return err
		}
		if len(zs) > 0 || len(rs) > 0 {
			used := append(refNames(zs), refNames(rs)...) // one readable list for the message
			return errTickerInUse.With("zones", refNames(zs)).With("rules", refNames(rs)).With("used", used)
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM ticker_templates WHERE id = ?`, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "ticker_template.deleted", ObjectType: "ticker_template",
			ObjectID: id, ObjectName: old.Name, Old: old})
	})
	if err != nil {
		return err
	}
	m.emit(core.TopicZones, "ticker_template.deleted", id, "")
	return httpx.NoContent(w)
}
