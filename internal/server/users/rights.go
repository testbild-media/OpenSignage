package users

import (
	"context"
	"database/sql"
	"strings"

	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
)

// Rights of a user (core/rights.go). Admins ignore them; users (role user) use only the
// zones (live operation, always "edit"); operators use all of them when Scope is "zones".
type Rights struct {
	Scope         string       `json:"scope"`
	Zones         []ZoneAccess `json:"zones"`
	CreateContent bool         `json:"createContent"`
	Media         string       `json:"media"`
	Products      string       `json:"products"`
	Devices       bool         `json:"devices"`
}

// ZoneAccess is view or edit access to one zone.
type ZoneAccess struct {
	ZoneID string `json:"zoneId"`
	Access string `json:"access"`
}

// defaultRights are those of a new user without explicit rights (and of existing ones).
func defaultRights() Rights {
	return Rights{Scope: core.ScopeAll, Zones: []ZoneAccess{}, Media: core.MediaUse, Products: core.ProductsView}
}

// validate checks the input and normalizes it for the role: users have edit access to their
// zones (live operation) and nothing else.
func (r *Rights) validate(role string) error {
	if !core.ValidScope(r.Scope) {
		return httpx.Invalid("rights.scope", "invalid")
	}
	if !core.ValidMediaRight(r.Media) {
		return httpx.Invalid("rights.media", "invalid")
	}
	if !core.ValidProductsRight(r.Products) {
		return httpx.Invalid("rights.products", "invalid")
	}
	seen := map[string]bool{}
	zones := make([]ZoneAccess, 0, len(r.Zones))
	for _, z := range r.Zones {
		if z.ZoneID == "" || !core.ValidAccess(z.Access) {
			return httpx.Invalid("rights.zones", "invalid")
		}
		if seen[z.ZoneID] {
			continue
		}
		seen[z.ZoneID] = true
		if role == core.RoleUser {
			z.Access = core.AccessEdit
		}
		zones = append(zones, z)
	}
	r.Zones = zones
	if role == core.RoleUser {
		*r = Rights{Scope: core.ScopeAll, Zones: r.Zones, Media: core.MediaUse, Products: core.ProductsView}
	}
	return nil
}

// fromZoneIDs turns the older zoneIds field (edit access) into rights.
func fromZoneIDs(base Rights, zoneIDs []string) Rights {
	base.Zones = make([]ZoneAccess, 0, len(zoneIDs))
	for _, z := range zoneIDs {
		base.Zones = append(base.Zones, ZoneAccess{ZoneID: z, Access: core.AccessEdit})
	}
	return base
}

// saveRights stores the rights of a user inside tx.
func saveRights(ctx context.Context, tx *sql.Tx, userID string, r Rights) error {
	if _, err := tx.ExecContext(ctx, `UPDATE users SET scope = ?, can_create = ?, media_right = ?, products_right = ?, devices_right = ?
		WHERE id = ?`, r.Scope, db.Bool(r.CreateContent), r.Media, r.Products, db.Bool(r.Devices), userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_zones WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, z := range r.Zones {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_zones(user_id, zone_id, access) VALUES (?, ?, ?)`, userID, z.ZoneID, z.Access); err != nil {
			if strings.Contains(err.Error(), "FOREIGN KEY") {
				return httpx.Invalid("rights.zones", "unknown_zone")
			}
			return err
		}
	}
	return nil
}

// sessionRights describes the rights of the logged-in principal for the WebGUI: what it may
// see and do (effective values, also for admins and operators of all zones).
func sessionRights(p *core.Principal) map[string]any {
	zones := map[string]string{}
	for z, a := range p.Zones {
		zones[z] = a
	}
	scope := core.ScopeAll
	if p.Restricted() || p.Role == core.RoleUser {
		scope = core.ScopeZones
	}
	media := ""
	for _, m := range []string{core.MediaManage, core.MediaUpload, core.MediaUse} {
		if p.CanMedia(m) {
			media = m
			break
		}
	}
	products := core.ProductsView
	if p.CanEditProducts() {
		products = core.ProductsEdit
	}
	return map[string]any{"scope": scope, "zones": zones, "createContent": p.CanCreateContent(), "media": media,
		"products": products, "devices": p.Unrestricted() || (p.Restricted() && p.Devices)}
}
