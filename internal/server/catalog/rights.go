package catalog

import (
	"context"
	"net/http"

	"opensignage/internal/contracts"
	"opensignage/internal/server/access"
	"opensignage/internal/server/core"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/modkit"
)

// Rights of operators limited to zones (core/rights.go): products only with the products
// right; zone and default rules, closed playlist and ticker template of zones they may edit;
// global rules, zone names, display groups and ticker templates stay with admins and
// operators of all zones.

var errProductsDenied = httpx.E(http.StatusForbidden, "products_forbidden", "you may not change products and categories")

// productsEdit wraps handlers that change products, categories or base prices.
func productsEdit(h core.AuthedHandler) core.AuthedHandler {
	return func(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
		if !p.CanEditProducts() {
			return errProductsDenied
		}
		return h(w, r, p)
	}
}

// ruleVisible: global rules run in every zone, zone and default rules in their zone.
func ruleVisible(p *core.Principal, r *contracts.Rule) bool {
	if p.Unrestricted() {
		return true
	}
	if r.Scope == contracts.ScopeGlobal {
		return len(p.Zones) > 0
	}
	return p.CanViewZone(r.ZoneID)
}

// ruleEditable: operators limited to zones change the zone and default rules of their zones.
func ruleEditable(p *core.Principal, r *contracts.Rule) bool {
	return p.Unrestricted() || (r.Scope != contracts.ScopeGlobal && p.CanEditZone(r.ZoneID))
}

// playlistVisible reports whether p may use a playlist (in a rule or as closed playlist): it
// must already be visible to p, so a rule cannot pull a foreign zone's playlist into view.
func playlistVisible(ctx context.Context, q modkit.Queryer, p *core.Principal, id string) (bool, error) {
	if p.Unrestricted() || id == "" {
		return true, nil
	}
	u, err := access.Load(ctx, q)
	if err != nil {
		return false, err
	}
	by, err := access.Creator(ctx, q, "playlists", id)
	if err != nil {
		return false, err
	}
	zones, global := u.PlaylistZones(id)
	return access.Decide(p, zones, global, by).View, nil
}

// checkRuleRights refuses a rule an operator limited to zones may not write (before or after
// the change) or that uses playlists the operator may not see.
func checkRuleRights(ctx context.Context, q modkit.Queryer, p *core.Principal, old, next *contracts.Rule) error {
	if p.Unrestricted() {
		return nil
	}
	if (old != nil && !ruleEditable(p, old)) || !ruleEditable(p, next) {
		return httpx.ErrForbidden
	}
	ids := []string{next.PlaylistID}
	for _, g := range next.GroupPlaylists {
		ids = append(ids, g.PlaylistID)
	}
	for _, id := range ids {
		ok, err := playlistVisible(ctx, q, p, id)
		if err != nil {
			return err
		}
		if !ok {
			return httpx.Invalid("playlistId", "no_access")
		}
	}
	return nil
}
