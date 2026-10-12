package core

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"

	"opensignage/internal/server/httpx"
)

type ctxKey int

const principalKey ctxKey = 1

// PrincipalFrom returns the principal stored in the request context (nil if anonymous).
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey).(*Principal)
	return p
}

// AuthedHandler receives the authenticated principal.
type AuthedHandler func(w http.ResponseWriter, r *http.Request, p *Principal) error

// Router registers API routes with role checks enforced on every request.
type Router struct {
	Mux      *http.ServeMux
	sessions *Sessions
}

// NewRouter wraps mux.
func NewRouter(mux *http.ServeMux, s *Sessions) *Router { return &Router{Mux: mux, sessions: s} }

// Public registers a handler that needs no login (CSRF is not checked; such handlers
// must not change state for an existing session, except login/setup).
func (rt *Router) Public(pattern string, h httpx.HandlerFunc) {
	rt.Mux.Handle(pattern, httpx.Adapt(h))
}

// Role registers a handler that requires at least role min.
func (rt *Router) Role(pattern, min string, h AuthedHandler) {
	rt.Mux.Handle(pattern, httpx.Adapt(func(w http.ResponseWriter, r *http.Request) error {
		p, err := rt.Authenticate(r)
		if err != nil {
			return err
		}
		if p == nil {
			return httpx.ErrUnauthorized
		}
		if !p.Is(min) {
			return httpx.ErrForbidden
		}
		if !safeMethod(r.Method) {
			if err := checkCSRF(r, p); err != nil {
				return err
			}
		}
		return h(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)), p)
	}))
}

// Authenticate resolves the session cookie.
func (rt *Router) Authenticate(r *http.Request) (*Principal, error) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return nil, nil
	}
	p, err := rt.sessions.Lookup(r.Context(), c.Value)
	if err != nil {
		return nil, httpx.ErrInternal.Wrap(err)
	}
	return p, nil
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// checkCSRF requires the per-session token header and a same-origin Origin header.
func checkCSRF(r *http.Request, p *Principal) error {
	tok := r.Header.Get("X-CSRF-Token")
	if tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(p.CSRF)) != 1 {
		return httpx.E(http.StatusForbidden, "csrf", "missing or invalid CSRF token")
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return httpx.E(http.StatusForbidden, "csrf", "cross-origin request rejected")
		}
	}
	return nil
}
