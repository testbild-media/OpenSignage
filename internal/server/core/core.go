// Package core holds what every server module shares: the App dependencies, the
// authenticated Principal, the role-aware router, sessions, audit log, settings and the
// in-process event bus.
package core

import (
	"log/slog"

	"opensignage/internal/common/tlsutil"
	"opensignage/internal/server/config"
	"opensignage/internal/server/db"
)

// Roles, ordered by power.
const (
	RoleUser     = "user"
	RoleOperator = "operator"
	RoleAdmin    = "admin"
)

var roleRank = map[string]int{RoleUser: 1, RoleOperator: 2, RoleAdmin: 3}

// ValidRole reports whether r is a known role.
func ValidRole(r string) bool { return roleRank[r] > 0 }

// AtLeast reports whether role has at least the rights of min.
func AtLeast(role, min string) bool { return roleRank[role] >= roleRank[min] && roleRank[min] > 0 }

// App bundles dependencies. Modules receive it at construction.
type App struct {
	Cfg      *config.Config
	DB       *db.DB
	Log      *slog.Logger
	Settings *SettingsStore
	Bus      *Bus
	Sessions *Sessions
	WebCert  *tlsutil.Store // WebGUI certificate (local CA leaf or uploaded)
	CA       *tlsutil.Pair  // local CA
	DeviceID *tlsutil.Pair  // device channel identity (pinned by clients)
}

// Principal is the authenticated user of a request.
type Principal struct {
	UserID      string
	Username    string
	DisplayName string
	Role        string
	Language    string
	ZoneIDs     []string
	SessionHash string
	CSRF        string
}

// Is reports whether the principal has at least the given role.
func (p *Principal) Is(min string) bool { return p != nil && AtLeast(p.Role, min) }

// CanZone reports whether the principal may act on a zone (admins and operators on all
// zones, users only on assigned zones).
func (p *Principal) CanZone(zoneID string) bool {
	if p == nil {
		return false
	}
	if AtLeast(p.Role, RoleOperator) {
		return true
	}
	for _, z := range p.ZoneIDs {
		if z == zoneID {
			return true
		}
	}
	return false
}
