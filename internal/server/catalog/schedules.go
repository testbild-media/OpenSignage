package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"opensignage/internal/common/ids"
	"opensignage/internal/contracts"
	"opensignage/internal/schedule"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/devices/deploy"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

const (
	maxRulePrices = 1000
	maxRuleGroups = MaxGroupsPerZone + 1 // every group and the screens without group
)

const ruleOrder = ` ORDER BY CASE scope WHEN 'global' THEN 0 WHEN 'zone' THEN 1 ELSE 2 END,
	COALESCE(zone_id, ''), COALESCE(start_time, ''), name COLLATE NOCASE, id`

// loadRules reads the rules matching where (a clause starting with WHERE, without ORDER BY)
// including their price overrides.
func loadRules(ctx context.Context, q modkit.Queryer, where string, args ...any) ([]contracts.Rule, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, revision, name, scope, COALESCE(zone_id, ''), days, start_time, end_time,
		COALESCE(playlist_id, ''), enabled, ticker_text, COALESCE(ticker_template_id, ''), ticker_hide FROM schedule_rules `+where+ruleOrder, args...)
	if err != nil {
		return nil, err
	}
	out := []contracts.Rule{}
	idx := map[string]int{}
	for rows.Next() {
		var r contracts.Rule
		var days, start, end, tickerText sql.NullString
		var tickerTemplate string
		var enabled, tickerHide int
		if err := rows.Scan(&r.ID, &r.Revision, &r.Name, &r.Scope, &r.ZoneID, &days, &start, &end, &r.PlaylistID, &enabled,
			&tickerText, &tickerTemplate, &tickerHide); err != nil {
			rows.Close()
			return nil, err
		}
		switch {
		case tickerHide == 1:
			r.Ticker = &contracts.RuleTicker{Hide: true}
		case tickerText.Valid:
			r.Ticker = &contracts.RuleTicker{Text: tickerText.String, TemplateID: tickerTemplate}
		}
		if days.Valid {
			w := &contracts.Window{Start: start.String, End: end.String}
			if err := json.Unmarshal([]byte(days.String), &w.Days); err != nil {
				rows.Close()
				return nil, err
			}
			r.Window = w
		}
		r.Enabled = enabled == 1
		r.Prices = []contracts.PriceOverride{}
		idx[r.ID] = len(out)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	prows, err := q.QueryContext(ctx, `SELECT rule_id, product_id, amount, currency FROM schedule_prices
		WHERE rule_id IN (SELECT id FROM schedule_rules `+where+`) ORDER BY rule_id, product_id`, args...)
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		var rid string
		var po contracts.PriceOverride
		if err := prows.Scan(&rid, &po.ProductID, &po.Price.Amount, &po.Price.Currency); err != nil {
			prows.Close()
			return nil, err
		}
		if i, ok := idx[rid]; ok {
			out[i].Prices = append(out[i].Prices, po)
		}
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return nil, err
	}
	crows, err := q.QueryContext(ctx, `SELECT rule_id, category_id, amount, currency FROM schedule_category_prices
		WHERE rule_id IN (SELECT id FROM schedule_rules `+where+`) ORDER BY rule_id, category_id`, args...)
	if err != nil {
		return nil, err
	}
	for crows.Next() {
		var rid string
		var po contracts.PriceOverride
		if err := crows.Scan(&rid, &po.CategoryID, &po.Price.Amount, &po.Price.Currency); err != nil {
			crows.Close()
			return nil, err
		}
		if i, ok := idx[rid]; ok {
			out[i].Prices = append(out[i].Prices, po)
		}
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return nil, err
	}
	// Playlists of single display groups, in the order of the zone's groups (screens
	// without group last).
	grows, err := q.QueryContext(ctx, `SELECT gp.rule_id, gp.group_id, gp.playlist_id FROM schedule_group_playlists gp
		LEFT JOIN display_groups g ON g.id = gp.group_id
		WHERE gp.rule_id IN (SELECT id FROM schedule_rules `+where+`)
		ORDER BY gp.rule_id, g.id IS NULL, g.sort, g.name COLLATE NOCASE, gp.group_id`, args...)
	if err != nil {
		return nil, err
	}
	defer grows.Close()
	for grows.Next() {
		var rid string
		var gp contracts.GroupPlaylist
		if err := grows.Scan(&rid, &gp.GroupID, &gp.PlaylistID); err != nil {
			return nil, err
		}
		if i, ok := idx[rid]; ok {
			out[i].GroupPlaylists = append(out[i].GroupPlaylists, gp)
		}
	}
	return out, grows.Err()
}

func loadRule(ctx context.Context, q modkit.Queryer, id string) (contracts.Rule, error) {
	rs, err := loadRules(ctx, q, `WHERE id = ?`, id)
	if err != nil {
		return contracts.Rule{}, err
	}
	if len(rs) == 0 {
		return contracts.Rule{}, httpx.ErrNotFound
	}
	return rs[0], nil
}

func (m *Module) listSchedules(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	where, args := "", []any{}
	if z := r.URL.Query().Get("zone"); z != "" {
		where, args = `WHERE scope = 'global' OR zone_id = ?`, []any{z}
	}
	rs, err := loadRules(r.Context(), m.app.DB.R, where, args...)
	if err != nil {
		return err
	}
	out := make([]contracts.Rule, 0, len(rs))
	for i := range rs {
		if ruleVisible(p, &rs[i]) {
			out = append(out, rs[i])
		}
	}
	return httpx.OK(w, map[string]any{"schedules": out})
}

type ruleReq struct {
	Revision   int64                        `json:"revision"`
	Name       *string                      `json:"name"`
	Scope      *string                      `json:"scope"`
	ZoneID     modkit.Opt[string]           `json:"zoneId"`
	Window     modkit.Opt[contracts.Window] `json:"window"`
	PlaylistID modkit.Opt[string]           `json:"playlistId"`
	// GroupPlaylists replaces the playlists of single display groups.
	GroupPlaylists *[]contracts.GroupPlaylist `json:"groupPlaylists"`
	Prices         *[]contracts.PriceOverride `json:"prices"`
	Ticker         modkit.Opt[ruleTickerReq]  `json:"ticker"` // null removes the ticker
	Enabled        *bool                      `json:"enabled"`
}

type ruleTickerReq struct {
	Hide       bool   `json:"hide"` // hides the ticker of lower rules
	Text       string `json:"text"`
	TemplateID string `json:"templateId"`
}

func (req *ruleReq) apply(r *contracts.Rule) error {
	if req.Name != nil {
		name, ok := modkit.CleanName(*req.Name, contracts.MaxRuleNameRunes)
		if !ok && name != "" {
			return httpx.Invalid("name", "too_long")
		}
		r.Name = name // empty: Validate answers "required"
	}
	if req.Scope != nil {
		r.Scope = *req.Scope
	}
	if req.ZoneID.Set {
		r.ZoneID = ""
		if req.ZoneID.Has() {
			r.ZoneID = req.ZoneID.Val
		}
	}
	if req.Window.Set {
		r.Window = nil
		if req.Window.Has() {
			w := req.Window.Val
			r.Window = &w
		}
	}
	if req.PlaylistID.Set {
		r.PlaylistID = ""
		if req.PlaylistID.Has() {
			r.PlaylistID = req.PlaylistID.Val
		}
	}
	if req.GroupPlaylists != nil {
		r.GroupPlaylists = nil
		for _, g := range *req.GroupPlaylists {
			r.GroupPlaylists = append(r.GroupPlaylists, contracts.GroupPlaylist{GroupID: strings.TrimSpace(g.GroupID), PlaylistID: g.PlaylistID})
		}
	}
	if req.Prices != nil {
		r.Prices = append([]contracts.PriceOverride{}, (*req.Prices)...)
	}
	if req.Ticker.Set {
		r.Ticker = nil
		if req.Ticker.Has() {
			v := req.Ticker.Val
			r.Ticker = &contracts.RuleTicker{Hide: v.Hide, Text: strings.TrimSpace(v.Text), TemplateID: v.TemplateID}
		}
	}
	if req.Enabled != nil {
		r.Enabled = *req.Enabled
	}
	return nil
}

// validateRule runs the structural checks, the reference checks and the uniqueness/overlap
// rules. It must run inside the write transaction so concurrent saves cannot both pass.
// currency is the server's currency: category prices apply to the products in it.
func validateRule(ctx context.Context, tx *sql.Tx, r *contracts.Rule, currency string) error {
	if len(r.Prices) > maxRulePrices {
		return httpx.Invalid("prices", "too_many")
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if r.Window != nil {
		sort.Ints(r.Window.Days)
	}
	var one int
	if r.ZoneID != "" {
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM zones WHERE id = ?`, r.ZoneID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.Invalid("zoneId", "unknown_zone")
		}
		if err != nil {
			return err
		}
	}
	if r.Ticker != nil && r.Ticker.TemplateID != "" {
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM ticker_templates WHERE id = ?`, r.Ticker.TemplateID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.Invalid("ticker.templateId", "unknown_ticker_template")
		}
		if err != nil {
			return err
		}
	}
	if r.PlaylistID != "" {
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM playlists WHERE id = ? AND archived_at IS NULL`, r.PlaylistID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.Invalid("playlistId", "unknown_playlist")
		}
		if err != nil {
			return err
		}
	}
	if len(r.GroupPlaylists) > maxRuleGroups {
		return httpx.Invalid("groupPlaylists", "too_many")
	}
	for i, g := range r.GroupPlaylists {
		if g.GroupID != contracts.NoGroupID {
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM display_groups WHERE id = ? AND zone_id = ?`, g.GroupID, r.ZoneID).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				return httpx.Invalid(fmt.Sprintf("groupPlaylists[%d].groupId", i), "unknown_group")
			}
			if err != nil {
				return err
			}
		}
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM playlists WHERE id = ? AND archived_at IS NULL`, g.PlaylistID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.Invalid(fmt.Sprintf("groupPlaylists[%d].playlistId", i), "unknown_playlist")
		}
		if err != nil {
			return err
		}
	}
	var products, categories []any
	for _, p := range r.Prices {
		if p.CategoryID != "" {
			categories = append(categories, p.CategoryID)
		} else {
			products = append(products, p.ProductID)
		}
	}
	if len(products) > 0 {
		currencies, err := productCurrencies(ctx, tx, products)
		if err != nil {
			return err
		}
		if len(currencies) != len(products) {
			return httpx.Invalid("prices", "unknown_product")
		}
		// A schedule price replaces the base price, so it must be in the product's currency.
		for i, p := range r.Prices {
			if p.CategoryID == "" && p.Price.Currency != currencies[p.ProductID] {
				return httpx.Invalid(fmt.Sprintf("prices[%d].price.currency", i), "currency_mismatch").
					With("currency", currencies[p.ProductID])
			}
		}
	}
	if len(categories) > 0 {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM categories WHERE id IN (?`+strings.Repeat(`,?`, len(categories)-1)+`)`,
			categories...).Scan(&n); err != nil {
			return err
		}
		if n != len(categories) {
			return httpx.Invalid("prices", "unknown_category")
		}
		// A category price applies to the products in the server's currency.
		for i, p := range r.Prices {
			if p.CategoryID != "" && p.Price.Currency != currency {
				return httpx.Invalid(fmt.Sprintf("prices[%d].price.currency", i), "currency_mismatch").
					With("currency", currency)
			}
		}
	}
	switch r.Scope {
	case contracts.ScopeDefault:
		others, err := loadRules(ctx, tx, `WHERE scope = 'default' AND zone_id = ? AND id <> ?`, r.ZoneID, r.ID)
		if err != nil {
			return err
		}
		if len(others) > 0 {
			return errDefaultExists.With("rule", others[0].Name).With("ruleId", others[0].ID)
		}
	case contracts.ScopeZone, contracts.ScopeGlobal:
		// Rules of the same scope may overlap when they change different things (the
		// playlist of all screens, of one group, a price, the ticker); a zone rule
		// and a global rule may always overlap (the zone rule wins). Disabled rules never
		// run, so they do not take part (re-enabling is checked again).
		if !r.Enabled {
			return nil
		}
		where, args := `WHERE scope = 'global' AND enabled = 1 AND id <> ?`, []any{r.ID}
		if r.Scope == contracts.ScopeZone {
			where, args = `WHERE scope = 'zone' AND zone_id = ? AND enabled = 1 AND id <> ?`, []any{r.ZoneID, r.ID}
		}
		others, err := loadRules(ctx, tx, where, args...)
		if err != nil {
			return err
		}
		// Prices are compared per product: a category price and the price of a product in
		// that category change the same thing.
		mine := []contracts.Rule{*r}
		if hasCategoryPrices(others) || hasCategoryPrices(mine) {
			products, err := deploy.CatalogProducts(ctx, tx)
			if err != nil {
				return err
			}
			mine = schedule.ExpandPrices(mine, products)
			others = schedule.ExpandPrices(others, products)
		}
		for _, o := range others {
			if o.Window == nil || !schedule.Overlaps(*r.Window, *o.Window) {
				continue
			}
			if c := schedule.SharedChange(mine[0], o); c != "" {
				return errWindowOverlap.With("rule", o.Name).With("ruleId", o.ID).With("change", c)
			}
		}
	}
	return nil
}

func hasCategoryPrices(rules []contracts.Rule) bool {
	for _, r := range rules {
		for _, p := range r.Prices {
			if p.CategoryID != "" {
				return true
			}
		}
	}
	return false
}

// productCurrencies maps the given product ids to the currency of their base price.
func productCurrencies(ctx context.Context, tx *sql.Tx, ids []any) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, currency FROM products WHERE id IN (?`+strings.Repeat(`,?`, len(ids)-1)+`)`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string, len(ids))
	for rows.Next() {
		var id, cur string
		if err := rows.Scan(&id, &cur); err != nil {
			return nil, err
		}
		out[id] = cur
	}
	return out, rows.Err()
}

func ruleDays(r *contracts.Rule) (days, start, end any) {
	if r.Window == nil {
		return nil, nil, nil
	}
	b, _ := json.Marshal(r.Window.Days) // []int cannot fail
	return string(b), r.Window.Start, r.Window.End
}

func writeGroupPlaylists(ctx context.Context, tx *sql.Tx, r *contracts.Rule) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM schedule_group_playlists WHERE rule_id = ?`, r.ID); err != nil {
		return err
	}
	for _, g := range r.GroupPlaylists {
		if _, err := tx.ExecContext(ctx, `INSERT INTO schedule_group_playlists(rule_id, group_id, playlist_id) VALUES (?, ?, ?)`,
			r.ID, g.GroupID, g.PlaylistID); err != nil {
			return err
		}
	}
	return nil
}

func writePrices(ctx context.Context, tx *sql.Tx, r *contracts.Rule) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM schedule_prices WHERE rule_id = ?`, r.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM schedule_category_prices WHERE rule_id = ?`, r.ID); err != nil {
		return err
	}
	for _, p := range r.Prices {
		query, ref := `INSERT INTO schedule_prices(rule_id, product_id, amount, currency) VALUES (?, ?, ?, ?)`, p.ProductID
		if p.CategoryID != "" {
			query, ref = `INSERT INTO schedule_category_prices(rule_id, category_id, amount, currency) VALUES (?, ?, ?, ?)`, p.CategoryID
		}
		if _, err := tx.ExecContext(ctx, query, r.ID, ref, p.Price.Amount, p.Price.Currency); err != nil {
			return err
		}
	}
	return nil
}

// ruleChanged publishes the events for a changed rule. zones lists the affected zone ids
// ("" = global rule, affects all zones). Deployments contain the rule set, so `content`
// follows; `live` only when prices are involved.
func (m *Module) ruleChanged(kind, id string, zones []string, pricesRelevant bool) {
	seen := map[string]bool{}
	for _, z := range zones {
		if seen[z] {
			continue
		}
		seen[z] = true
		m.emit(core.TopicZones, kind, id, z)
		m.emit(core.TopicContent, kind, id, z)
		if pricesRelevant {
			m.emit(core.TopicLive, kind, id, z)
		}
	}
}

func (m *Module) createSchedule(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	var req ruleReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.Name == nil {
		return httpx.Invalid("name", "required")
	}
	if req.Scope == nil {
		return httpx.Invalid("scope", "required")
	}
	rule := contracts.Rule{ID: ids.New(), Revision: 1, Enabled: true, Prices: []contracts.PriceOverride{}}
	if err := req.apply(&rule); err != nil {
		return err
	}
	var out contracts.Rule
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		if err := checkRuleRights(r.Context(), tx, p, nil, &rule); err != nil {
			return err
		}
		if err := validateRule(r.Context(), tx, &rule, m.app.Settings.Get().Currency); err != nil {
			return err
		}
		days, start, end := ruleDays(&rule)
		now := db.Now()
		tickerText, tickerTemplate, tickerHide := ruleTickerCols(&rule)
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO schedule_rules(id, name, scope, zone_id, days, start_time, end_time,
			playlist_id, enabled, ticker_text, ticker_template_id, ticker_hide, revision, created_at, updated_at)
			VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), ?, ?, NULLIF(?, ''), ?, 1, ?, ?)`,
			rule.ID, rule.Name, rule.Scope, rule.ZoneID, days, start, end, rule.PlaylistID, db.Bool(rule.Enabled),
			tickerText, tickerTemplate, tickerHide, now, now); err != nil {
			return err
		}
		if err := writePrices(r.Context(), tx, &rule); err != nil {
			return err
		}
		if err := writeGroupPlaylists(r.Context(), tx, &rule); err != nil {
			return err
		}
		var err error
		if out, err = loadRule(r.Context(), tx, rule.ID); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "schedule.created", ObjectType: "schedule_rule",
			ObjectID: out.ID, ObjectName: out.Name, ZoneID: out.ZoneID, New: out})
	})
	if err != nil {
		return err
	}
	m.ruleChanged("schedule.created", out.ID, []string{out.ZoneID}, len(out.Prices) > 0)
	return httpx.OK(w, out)
}

func (m *Module) updateSchedule(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var req ruleReq
	if err := httpx.Decode(w, r, 0, &req); err != nil {
		return err
	}
	if req.Revision <= 0 {
		return httpx.Invalid("revision", "required")
	}
	var old, out contracts.Rule
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var err error
		if old, err = loadRule(r.Context(), tx, id); err != nil {
			return err
		}
		if old.Revision != req.Revision {
			return conflictWithRevision(old.Revision)
		}
		next := old
		next.Window = copyWindow(old.Window)
		next.Prices = append([]contracts.PriceOverride{}, old.Prices...)
		next.GroupPlaylists = append([]contracts.GroupPlaylist(nil), old.GroupPlaylists...)
		if old.Ticker != nil {
			t := *old.Ticker
			next.Ticker = &t
		}
		if err := req.apply(&next); err != nil {
			return err
		}
		if err := checkRuleRights(r.Context(), tx, p, &old, &next); err != nil {
			return err
		}
		if err := validateRule(r.Context(), tx, &next, m.app.Settings.Get().Currency); err != nil {
			return err
		}
		days, start, end := ruleDays(&next)
		// The revision rises on every change, including price changes, so clients that
		// compare id and revision notice edits of the currently active rule.
		tickerText, tickerTemplate, tickerHide := ruleTickerCols(&next)
		if _, err := tx.ExecContext(r.Context(), `UPDATE schedule_rules SET name = ?, scope = ?, zone_id = NULLIF(?, ''), days = ?,
			start_time = ?, end_time = ?, playlist_id = NULLIF(?, ''), enabled = ?, ticker_text = ?,
			ticker_template_id = NULLIF(?, ''), ticker_hide = ?, revision = revision + 1, updated_at = ? WHERE id = ?`,
			next.Name, next.Scope, next.ZoneID, days, start, end, next.PlaylistID, db.Bool(next.Enabled),
			tickerText, tickerTemplate, tickerHide, db.Now(), id); err != nil {
			return err
		}
		if err := writePrices(r.Context(), tx, &next); err != nil {
			return err
		}
		if err := writeGroupPlaylists(r.Context(), tx, &next); err != nil {
			return err
		}
		if out, err = loadRule(r.Context(), tx, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "schedule.updated", ObjectType: "schedule_rule",
			ObjectID: id, ObjectName: out.Name, ZoneID: out.ZoneID, Old: old, New: out})
	})
	if err != nil {
		return err
	}
	m.ruleChanged("schedule.updated", id, []string{old.ZoneID, out.ZoneID}, len(old.Prices) > 0 || len(out.Prices) > 0)
	return httpx.OK(w, out)
}

// ruleTickerCols maps the rule's ticker to its columns (text NULL = no ticker; hide = 1
// hides the ticker of lower rules).
func ruleTickerCols(r *contracts.Rule) (text any, templateID string, hide int) {
	switch {
	case r.Ticker == nil:
		return nil, "", 0
	case r.Ticker.Hide:
		return nil, "", 1
	}
	return r.Ticker.Text, r.Ticker.TemplateID, 0
}

func copyWindow(w *contracts.Window) *contracts.Window {
	if w == nil {
		return nil
	}
	c := *w
	c.Days = append([]int(nil), w.Days...)
	return &c
}

func (m *Module) deleteSchedule(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	var old contracts.Rule
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var err error
		if old, err = loadRule(r.Context(), tx, id); err != nil {
			return err
		}
		if !ruleEditable(p, &old) {
			return httpx.ErrForbidden
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM schedule_rules WHERE id = ?`, id); err != nil {
			return err
		}
		return audit(r.Context(), tx, r, p, core.AuditEntry{Action: "schedule.deleted", ObjectType: "schedule_rule",
			ObjectID: id, ObjectName: old.Name, ZoneID: old.ZoneID, Old: old})
	})
	if err != nil {
		return err
	}
	m.ruleChanged("schedule.deleted", id, []string{old.ZoneID}, len(old.Prices) > 0)
	return httpx.NoContent(w)
}

type activeRule struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Scope       string `json:"scope"`
	HasPlaylist bool   `json:"hasPlaylist"`
	HasTicker   bool   `json:"hasTicker"`
}

// nowGroup is what the screens of one display group (or those without group) play now.
type nowGroup struct {
	GroupID  string    `json:"groupId"` // contracts.NoGroupID for the screens without group
	Name     string    `json:"name,omitempty"`
	Kind     string    `json:"kind"`
	Playlist *namedRef `json:"playlist"`
	RuleID   string    `json:"ruleId,omitempty"`
	// Since is the anchor of the synchronous playback (RFC 3339).
	Since   string `json:"since,omitempty"`
	Devices int    `json:"devices"`
}

type nowResult struct {
	ZoneID      string                     `json:"zoneId"`
	At          string                     `json:"at"`
	Timezone    string                     `json:"timezone"`
	ZoneState   string                     `json:"zoneState"`
	Kind        string                     `json:"kind"` // closed | rule | cleared
	Playlist    *namedRef                  `json:"playlist"`
	RuleID      string                     `json:"ruleId,omitempty"`
	ActiveRules []activeRule               `json:"activeRules"`
	Prices      map[string]contracts.Money `json:"prices"`
	// Ticker shown over the content now (text and the rule it comes from).
	Ticker *nowTicker `json:"ticker"`
	// TickerHiddenBy names the rule that hides the ticker now ("" = none).
	TickerHiddenBy string `json:"tickerHiddenBy,omitempty"`
	// Groups: what every display group and the screens without group play;
	// empty when the zone has no groups (then the top level applies to all screens).
	Groups []nowGroup `json:"groups"`
}

type nowTicker struct {
	Text   string `json:"text"`
	RuleID string `json:"ruleId"`
}

// schedulesNow answers "what is running in this zone": the same evaluation the clients run.
// The optional `at` (RFC 3339) previews another moment.
func (m *Module) schedulesNow(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	q := r.URL.Query()
	zoneID := q.Get("zone")
	if zoneID == "" {
		return httpx.Invalid("zone", "required")
	}
	if !p.CanViewZone(zoneID) {
		return httpx.ErrNotFound
	}
	at := time.Now()
	if v := q.Get("at"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return httpx.Invalid("at", "invalid")
		}
		at = t
	}
	ctx := r.Context()
	var state, closedPlaylist, changedAt string
	err := m.app.DB.R.QueryRowContext(ctx, `SELECT state, COALESCE(closed_playlist_id, ''), COALESCE(state_changed_at, '')
		FROM zones WHERE id = ?`, zoneID).Scan(&state, &closedPlaylist, &changedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	rules, err := loadRules(ctx, m.app.DB.R, `WHERE scope = 'global' OR zone_id = ?`, zoneID)
	if err != nil {
		return err
	}
	if hasCategoryPrices(rules) {
		products, err := deploy.CatalogProducts(ctx, m.app.DB.R)
		if err != nil {
			return err
		}
		rules = schedule.ExpandPrices(rules, products)
	}
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT id, name, published_version IS NOT NULL AND archived_at IS NULL FROM playlists`)
	if err != nil {
		return err
	}
	available := map[string]bool{}
	names := map[string]string{}
	for rows.Next() {
		var id, name string
		var ok int
		if err := rows.Scan(&id, &name, &ok); err != nil {
			rows.Close()
			return err
		}
		names[id] = name
		available[id] = ok == 1
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	groups, err := loadGroups(ctx, m.app.DB.R, `WHERE g.zone_id = ?`, zoneID)
	if err != nil {
		return err
	}
	var closedSince time.Time
	if changedAt != "" {
		closedSince, _ = time.Parse(time.RFC3339Nano, changedAt) // unparsable = unknown (midnight)
	}
	loc := m.app.Settings.Location()
	eval := func(groupID, groupClosed string) schedule.Result {
		return schedule.Evaluate(schedule.Input{Rules: rules, ZoneID: zoneID, GroupID: groupID, ZoneClosed: state == contracts.ZoneClosed,
			ClosedPlaylistID: closedPlaylist, GroupClosedPlaylistID: groupClosed, ClosedSince: closedSince, Now: at, Location: loc,
			PlaylistAvailable: func(id string) bool { return available[id] }})
	}
	res := eval("", "")
	byID := map[string]contracts.Rule{}
	for _, ru := range rules {
		byID[ru.ID] = ru
	}
	out := nowResult{ZoneID: zoneID, At: at.In(loc).Format(time.RFC3339), Timezone: loc.String(), ZoneState: state,
		Kind: res.Kind, RuleID: res.RuleID, Prices: res.Prices, ActiveRules: []activeRule{}, Groups: []nowGroup{}}
	if res.PlaylistID != "" {
		out.Playlist = &namedRef{ID: res.PlaylistID, Name: names[res.PlaylistID]}
	}
	for _, id := range res.ActiveRuleIDs {
		ru := byID[id]
		out.ActiveRules = append(out.ActiveRules, activeRule{ID: ru.ID, Name: ru.Name, Scope: ru.Scope,
			HasPlaylist: ru.PlaylistID != "" || len(ru.GroupPlaylists) > 0, HasTicker: ru.Ticker != nil})
	}
	if res.Ticker != nil {
		out.Ticker = &nowTicker{Text: res.Ticker.Text, RuleID: res.TickerRuleID}
	}
	if res.TickerHidden {
		out.TickerHiddenBy = res.TickerRuleID
	}
	if len(groups) > 0 {
		var ungrouped int
		if err := m.app.DB.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE zone_id = ? AND group_id IS NULL`, zoneID).Scan(&ungrouped); err != nil {
			return err
		}
		entry := func(id, name string, devices int, gr schedule.Result) nowGroup {
			g := nowGroup{GroupID: id, Name: name, Kind: gr.Kind, RuleID: gr.RuleID, Devices: devices}
			if gr.PlaylistID != "" {
				g.Playlist = &namedRef{ID: gr.PlaylistID, Name: names[gr.PlaylistID]}
			}
			if gr.Since != nil {
				g.Since = gr.Since.In(loc).Format(time.RFC3339)
			}
			return g
		}
		for _, g := range groups {
			out.Groups = append(out.Groups, entry(g.ID, g.Name, g.DeviceCount, eval(g.ID, g.ClosedPlaylistID)))
		}
		out.Groups = append(out.Groups, entry(contracts.NoGroupID, "", ungrouped, res))
	}
	return httpx.OK(w, out)
}
