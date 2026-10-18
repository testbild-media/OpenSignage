// Package catalog implements categories, products (master data and the live status),
// zones, ticker templates and time schedules. It writes categories, products,
// zone_product_status, zones, display_groups, ticker_templates, schedule_rules,
// schedule_prices and schedule_group_playlists, and
// publishes products, zones, live and content events after the transaction committed.
package catalog

import (
	"context"
	"database/sql"
	"net/http"

	"opensignage/internal/server/core"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

// Module is the catalog module.
type Module struct {
	app   *core.App
	fonts *modkit.Fonts
}

// New creates the module.
func New(app *core.App) *Module {
	return &Module{app: app, fonts: modkit.NewFonts(app.Cfg.ShareDir)}
}

// Routes registers the WebGUI API. Operators limited to zones get the routes registered with
// Scoped; the handlers check their rights (rights.go).
func (m *Module) Routes(rt *core.Router) {
	op := core.RoleOperator
	// Categories
	rt.Role("GET /api/v1/categories", core.RoleUser, m.listCategories)
	rt.Scoped("POST /api/v1/categories", op, productsEdit(m.createCategory))
	rt.Scoped("POST /api/v1/categories/reorder", op, productsEdit(m.reorderCategories))
	rt.Scoped("PATCH /api/v1/categories/{id}", op, productsEdit(m.updateCategory))
	rt.Scoped("DELETE /api/v1/categories/{id}", op, productsEdit(m.deleteCategory))

	// Products
	rt.Role("GET /api/v1/products", core.RoleUser, m.listProducts)
	rt.Scoped("POST /api/v1/products", op, productsEdit(m.createProduct))
	rt.Scoped("POST /api/v1/products/reorder", op, productsEdit(m.reorderProducts))
	rt.Scoped("PATCH /api/v1/products/{id}", op, productsEdit(m.updateProduct))
	rt.Scoped("POST /api/v1/products/{id}/archive", op, productsEdit(m.archiveProduct))
	rt.Scoped("POST /api/v1/products/{id}/restore", op, productsEdit(m.restoreProduct))
	rt.Scoped("DELETE /api/v1/products/{id}", op, productsEdit(m.deleteProduct))

	// Live operation (operator UI /control)
	rt.Role("GET /api/v1/live/zones", core.RoleUser, m.liveZones)
	rt.Role("POST /api/v1/live/products/{id}/status", core.RoleUser, m.setProductStatus)
	rt.Role("POST /api/v1/live/zones/{id}/state", core.RoleUser, m.setZoneState)

	// Zones
	rt.Role("GET /api/v1/zones", core.RoleUser, m.listZones)
	rt.Role("POST /api/v1/zones", core.RoleOperator, m.createZone)
	rt.Scoped("PATCH /api/v1/zones/{id}", op, m.updateZone)
	rt.Role("DELETE /api/v1/zones/{id}", core.RoleOperator, m.deleteZone)
	rt.Role("GET /api/v1/zones/{id}/usage", core.RoleOperator, m.zoneUsage)
	rt.Role("POST /api/v1/zones/{id}/groups", core.RoleOperator, m.createGroup)
	rt.Role("PATCH /api/v1/zones/{id}/groups/{gid}", core.RoleOperator, m.updateGroup)
	rt.Role("DELETE /api/v1/zones/{id}/groups/{gid}", core.RoleOperator, m.deleteGroup)

	// Ticker templates
	rt.Role("GET /api/v1/ticker-templates", core.RoleUser, m.listTickerTemplates)
	rt.Role("POST /api/v1/ticker-templates", core.RoleOperator, m.createTickerTemplate)
	rt.Role("PATCH /api/v1/ticker-templates/{id}", core.RoleOperator, m.updateTickerTemplate)
	rt.Role("DELETE /api/v1/ticker-templates/{id}", core.RoleOperator, m.deleteTickerTemplate)

	// Time schedules
	rt.Scoped("GET /api/v1/schedules", op, m.listSchedules)
	rt.Scoped("GET /api/v1/schedules/now", op, m.schedulesNow)
	rt.Scoped("POST /api/v1/schedules", op, m.createSchedule)
	rt.Scoped("PATCH /api/v1/schedules/{id}", op, m.updateSchedule)
	rt.Scoped("DELETE /api/v1/schedules/{id}", op, m.deleteSchedule)
}

// emit publishes an event; callers invoke it only after the transaction committed.
func (m *Module) emit(topic, kind, id, zoneID string) {
	m.app.Bus.Publish(core.Event{Topic: topic, Kind: kind, ID: id, ZoneID: zoneID})
}

// audit writes an audit record for the request inside tx.
func audit(ctx context.Context, tx *sql.Tx, r *http.Request, p *core.Principal, e core.AuditEntry) error {
	return core.Audit(ctx, tx, p, httpx.ClientIP(r), e)
}

// Stable error constructors (the UI translates `errors.<code>`).
var (
	errCategoryNotEmpty = httpx.E(http.StatusConflict, "category_not_empty", "the category still has products or subcategories")
	errCategoryInUse    = httpx.E(http.StatusConflict, "category_in_use", "the category is used by product lists in presentations")
	errProductInUse     = httpx.E(http.StatusConflict, "product_in_use", "the product is used by schedule prices or presentations")
	errProductNotArch   = httpx.E(http.StatusConflict, "product_not_archived", "only archived products can be deleted")
	errProductArchived  = httpx.E(http.StatusConflict, "product_archived", "the product is archived")
	errDefaultExists    = httpx.E(http.StatusConflict, "default_exists", "this zone already has a default rule")
	errWindowOverlap    = httpx.E(http.StatusConflict, "window_overlap", "the time window overlaps another rule that changes the same thing")
	errTickerInUse      = httpx.E(http.StatusConflict, "ticker_template_in_use", "the ticker template is used by zones or time schedule rules")
	errGroupNameTaken   = httpx.E(http.StatusConflict, "group_name_taken", "the zone already has a display group with this name")
	errGroupInUse       = httpx.E(http.StatusConflict, "group_in_use", "time schedule rules set a playlist for this display group")
	errTooManyGroups    = httpx.E(http.StatusConflict, "too_many_groups", "the zone has the maximum number of display groups")
)

// conflictWithRevision is the stale-revision error carrying the current revision.
func conflictWithRevision(current int64) *httpx.Error {
	return httpx.ErrConflict.With("revision", current)
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
