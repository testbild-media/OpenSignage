package core

// User rights. Admins and operators with scope "all" may do everything their role allows.
// Operators with scope "zones" (restricted) see only the zones assigned to them, each with
// view or edit access, plus these rights: create content, media (use, upload, manage),
// products (view, edit) and controlling the displays of their zones. Routes registered with
// Router.Role refuse restricted operators (default deny); Router.Scoped lets them through to
// handlers that check their rights with the methods below.

// Scopes of operators.
const (
	ScopeAll   = "all"
	ScopeZones = "zones"
)

// Access to one zone.
const (
	AccessView = "view"
	AccessEdit = "edit"
)

// Media rights, each including the one before.
const (
	MediaUse    = "use"    // see and use media in content
	MediaUpload = "upload" // + upload, rename and move own uploads, create folders
	MediaManage = "manage" // + everything else (delete, trash, other people's media)
)

// Product rights.
const (
	ProductsView = "view"
	ProductsEdit = "edit"
)

var mediaRank = map[string]int{MediaUse: 1, MediaUpload: 2, MediaManage: 3}

// ValidScope, ValidAccess, ValidMediaRight and ValidProductsRight check API input.
func ValidScope(s string) bool         { return s == ScopeAll || s == ScopeZones }
func ValidAccess(s string) bool        { return s == AccessView || s == AccessEdit }
func ValidMediaRight(s string) bool    { return mediaRank[s] > 0 }
func ValidProductsRight(s string) bool { return s == ProductsView || s == ProductsEdit }

// Restricted reports whether p is an operator limited to selected zones.
func (p *Principal) Restricted() bool {
	return p != nil && p.Role == RoleOperator && p.Scope == ScopeZones
}

// Unrestricted reports whether p is an admin or an operator of all zones.
func (p *Principal) Unrestricted() bool {
	return p != nil && AtLeast(p.Role, RoleOperator) && !p.Restricted()
}

// ZoneAccess returns "edit", "view" or "" for a zone.
func (p *Principal) ZoneAccess(zoneID string) string {
	if p == nil || zoneID == "" {
		return ""
	}
	if p.Unrestricted() {
		return AccessEdit
	}
	return p.Zones[zoneID]
}

// CanViewZone reports whether p may see a zone (its displays, schedule and content).
func (p *Principal) CanViewZone(zoneID string) bool { return p.ZoneAccess(zoneID) != "" }

// CanEditZone reports whether p may change a zone (rules, ticker, live operation, content).
func (p *Principal) CanEditZone(zoneID string) bool { return p.ZoneAccess(zoneID) == AccessEdit }

// CanZone reports whether p may operate a zone live (product status, zone state): admins
// and operators of all zones everywhere, users and restricted operators where they may edit.
func (p *Principal) CanZone(zoneID string) bool { return p.CanEditZone(zoneID) }

// CanMedia reports whether p has at least the media right level.
func (p *Principal) CanMedia(level string) bool {
	if p.Unrestricted() {
		return true
	}
	return p.Restricted() && mediaRank[p.Media] >= mediaRank[level]
}

// CanEditProducts reports whether p may change products, categories and base prices.
func (p *Principal) CanEditProducts() bool {
	return p.Unrestricted() || (p.Restricted() && p.Products == ProductsEdit)
}

// CanCreateContent reports whether p may create presentations and playlists.
func (p *Principal) CanCreateContent() bool {
	return p.Unrestricted() || (p.Restricted() && p.CanCreate)
}

// CanControlDevice reports whether p may restart, identify or black out a display of zoneID.
func (p *Principal) CanControlDevice(zoneID string) bool {
	return p.Unrestricted() || (p.Restricted() && p.Devices && p.CanEditZone(zoneID))
}

// EditZones returns the zones p may edit (nil for unrestricted principals: all).
func (p *Principal) EditZones() []string {
	if p.Unrestricted() {
		return nil
	}
	var out []string
	for z, a := range p.Zones {
		if a == AccessEdit {
			out = append(out, z)
		}
	}
	return out
}
