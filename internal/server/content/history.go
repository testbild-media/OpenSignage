package content

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"opensignage/internal/server/core"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

// Version history of presentations. Every publish is a version; publishes of the same person
// less than phaseGap apart form one editing phase (fine-tuning on the screens). After thinAfter
// only the last version of each phase is kept, and only the newest keepPhases phases are kept
// at all. Named versions and the published version are always kept.
const (
	phaseGap      = 60 * time.Minute
	thinAfter     = 10 * 24 * time.Hour // over a week: venues open only at weekends can still go back a week
	keepPhases    = 50
	maxVersions   = 200 // unnamed versions per presentation, also within the last thinAfter
	maxNamed      = 100 // named versions per presentation
	maxLabelRunes = 80
	thinInterval  = time.Hour
)

var errTooManyNamed = httpx.E(http.StatusConflict, "too_many_named_versions", "too many named versions").With("max", maxNamed)

// historyRules is sent with the version list (the dialog explains the rules).
var historyRules = map[string]int{
	"phaseGapMinutes": int(phaseGap / time.Minute),
	"thinAfterDays":   int(thinAfter / (24 * time.Hour)),
	"keepPhases":      keepPhases,
	"maxNamed":        maxNamed,
	"maxLabel":        maxLabelRunes,
}

// histVersion is one published version as the history rules see it.
type histVersion struct {
	Version int64
	At      time.Time
	By      string
	Label   string
}

// phaseStarts returns for each version (sorted by version) the number of the first version of
// its editing phase.
func phaseStarts(vs []histVersion) []int64 {
	out := make([]int64, len(vs))
	for i, v := range vs {
		if i > 0 && v.By == vs[i-1].By && v.At.Sub(vs[i-1].At) < phaseGap {
			out[i] = out[i-1]
		} else {
			out[i] = v.Version
		}
	}
	return out
}

// thinOut returns the versions the history rules remove (ascending). vs is sorted by version.
func thinOut(vs []histVersion, published int64, now time.Time) []int64 {
	starts := phaseStarts(vs)
	last := map[int64]int{} // phase → index of its last version
	for i := range vs {
		last[starts[i]] = i
	}
	rank := map[int64]int{} // phase → 0 for the newest
	for i := len(vs) - 1; i >= 0; i-- {
		if _, ok := rank[starts[i]]; !ok {
			rank[starts[i]] = len(rank)
		}
	}
	var drop []int64
	var kept []int // unnamed versions that stay
	unnamed := 0
	for i, v := range vs {
		if v.Label != "" {
			continue
		}
		unnamed++
		if v.Version == published {
			continue
		}
		ph := starts[i]
		switch {
		case rank[ph] >= keepPhases:
			drop = append(drop, v.Version)
		case last[ph] != i && now.Sub(vs[last[ph]].At) >= thinAfter:
			drop = append(drop, v.Version)
		default:
			kept = append(kept, i)
		}
	}
	// Limit: the oldest intermediate versions go first. The last versions of the phases are at
	// most keepPhases, so the newest version always stays (numbers are never reused).
	excess := unnamed - len(drop) - maxVersions
	for _, i := range kept {
		if excess <= 0 {
			break
		}
		if last[starts[i]] != i {
			drop = append(drop, vs[i].Version)
			excess--
		}
	}
	slices.Sort(drop)
	return drop
}

// loadHistory reads the versions of one presentation (ascending).
func loadHistory(ctx context.Context, q modkit.Queryer, id string) ([]histVersion, error) {
	rows, err := q.QueryContext(ctx, `SELECT version, published_at, published_by, label FROM presentation_versions
		WHERE presentation_id = ? ORDER BY version`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []histVersion
	for rows.Next() {
		var v histVersion
		var at string
		if err := rows.Scan(&v.Version, &at, &v.By, &v.Label); err != nil {
			return nil, err
		}
		v.At, _ = time.Parse(time.RFC3339Nano, at) // unreadable times count as very old
		out = append(out, v)
	}
	return out, rows.Err()
}

// thinHistory applies the history rules to one presentation inside tx.
func thinHistory(ctx context.Context, tx *sql.Tx, id string, now time.Time) (int, error) {
	var published sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT published_version FROM presentations WHERE id = ?`, id).Scan(&published); err != nil {
		return 0, err
	}
	vs, err := loadHistory(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	drop := thinOut(vs, published.Int64, now)
	for _, v := range drop {
		if _, err := tx.ExecContext(ctx, `DELETE FROM presentation_versions WHERE presentation_id = ? AND version = ?`, id, v); err != nil {
			return 0, err
		}
	}
	return len(drop), nil
}

// Run thins out the version histories once per hour (versions age without a new publish).
func (m *Module) Run(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if n, err := m.ThinHistories(ctx, time.Now()); err != nil {
			slog.Error("content: thinning version histories", "err", err)
		} else if n > 0 {
			slog.Info("content: thinned version histories", "versions", n)
		}
		timer.Reset(thinInterval)
	}
}

// ThinHistories applies the history rules to every presentation with more than one version
// and returns the number of removed versions.
func (m *Module) ThinHistories(ctx context.Context, now time.Time) (int, error) {
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT presentation_id FROM presentation_versions
		GROUP BY presentation_id HAVING COUNT(*) > 1`)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	total := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
			n, err := thinHistory(ctx, tx, id, now)
			total += n
			return err
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

type versionInfo struct {
	Version     int64  `json:"version"`
	PublishedAt string `json:"publishedAt"`
	PublishedBy string `json:"publishedBy"`
	SlideCount  int    `json:"slideCount"`
	Label       string `json:"label"`
	Phase       int64  `json:"phase"` // first version of the editing phase
}

func (m *Module) listVersions(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	id := r.PathValue("id")
	row, err := loadPresRow(r.Context(), m.app.DB.R, id)
	if err != nil {
		return err
	}
	rows, err := m.app.DB.R.QueryContext(r.Context(), `SELECT version, published_at, published_by, label,
		COALESCE(json_array_length(doc, '$.slides'), 0) FROM presentation_versions WHERE presentation_id = ? ORDER BY version`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	var out []versionInfo
	var hist []histVersion
	for rows.Next() {
		var v versionInfo
		if err := rows.Scan(&v.Version, &v.PublishedAt, &v.PublishedBy, &v.Label, &v.SlideCount); err != nil {
			return err
		}
		at, _ := time.Parse(time.RFC3339Nano, v.PublishedAt)
		out = append(out, v)
		hist = append(hist, histVersion{Version: v.Version, At: at, By: v.PublishedBy, Label: v.Label})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i, start := range phaseStarts(hist) {
		out[i].Phase = start
	}
	slices.Reverse(out) // newest first
	if out == nil {
		out = []versionInfo{}
	}
	return httpx.OK(w, map[string]any{"versions": out, "publishedVersion": nullInt(row.PublishedVersion), "rules": historyRules})
}

type labelReq struct {
	Label *string `json:"label"`
}

// labelVersion names a version (an empty label removes the name). Named versions are kept
// for good.
func (m *Module) labelVersion(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	v, err := strconv.ParseInt(r.PathValue("v"), 10, 64)
	if err != nil || v < 1 {
		return httpx.ErrNotFound
	}
	var req labelReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.Label == nil {
		return httpx.Invalid("label", "required")
	}
	label := strings.TrimSpace(*req.Label)
	if utf8.RuneCountInString(label) > maxLabelRunes {
		return httpx.Invalid("label", "too_long")
	}
	err = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		row, err := loadPresRow(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if row.Archived {
			return errPresArchived
		}
		var old string
		err = tx.QueryRowContext(r.Context(), `SELECT label FROM presentation_versions WHERE presentation_id = ? AND version = ?`, id, v).Scan(&old)
		if err == sql.ErrNoRows {
			return httpx.ErrNotFound
		}
		if err != nil || old == label {
			return err
		}
		if old == "" {
			var named int
			if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM presentation_versions WHERE presentation_id = ? AND label <> ''`, id).Scan(&named); err != nil {
				return err
			}
			if named >= maxNamed {
				return errTooManyNamed
			}
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE presentation_versions SET label = ? WHERE presentation_id = ? AND version = ?`, label, id, v); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "presentation.version_named", ObjectType: "presentation", ObjectID: id,
			ObjectName: row.Name, Old: map[string]any{"version": v, "label": old}, New: map[string]any{"version": v, "label": label}})
	})
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"version": v, "label": label})
}
