// Package events forwards bus events to logged-in browsers over WebSocket
// (/api/v1/ws). Messages are small notifications; pages reload what they show.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"opensignage/internal/server/core"
	"opensignage/internal/server/httpx"
)

// Module serves the admin event socket.
type Module struct {
	app  *core.App
	rt   *core.Router
	mu   sync.Mutex
	base context.Context // server lifetime; sockets close with 1001 when it ends
}

// Run records the server lifetime context (sockets close with "going away" on shutdown).
func (m *Module) Run(ctx context.Context) {
	m.mu.Lock()
	m.base = ctx
	m.mu.Unlock()
	<-ctx.Done()
}

func (m *Module) baseCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.base == nil {
		return context.Background()
	}
	return m.base
}

// New creates the module.
func New(app *core.App) *Module { return &Module{app: app} }

// Routes registers the socket endpoint.
func (m *Module) Routes(rt *core.Router) {
	m.rt = rt
	rt.Public("GET /api/v1/ws", m.serve)
}

func (m *Module) serve(w http.ResponseWriter, r *http.Request) error {
	p, err := m.rt.Authenticate(r)
	if err != nil {
		return err
	}
	if p == nil {
		return httpx.ErrUnauthorized
	}
	// Cross-site WebSocket hijacking protection: the Origin must be this host.
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return httpx.ErrForbidden
		}
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{r.Host}})
	if err != nil {
		return nil // Accept already wrote the response
	}
	go m.pump(c, p)
	return nil
}

func (m *Module) pump(c *websocket.Conn, p *core.Principal) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("admin event socket crashed", "panic", r)
		}
	}()
	defer c.CloseNow()
	sub := m.app.Bus.Subscribe(256)
	defer sub.Close()
	base := m.baseCtx()
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	c.SetReadLimit(4096)
	go func() {
		// Drain client messages (none expected) to detect closure.
		for {
			if _, _, err := c.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	// The socket lives as long as the session: logout, deactivation, deletion or a role
	// change end it (the page reconnects, or meets the login).
	sessionOK := func() bool {
		role, err := m.app.Sessions.Role(ctx, p.SessionHash)
		if err != nil || role == p.Role {
			return true // a database hiccup never drops the socket
		}
		_ = c.Close(websocket.StatusPolicyViolation, "session ended")
		return false
	}
	write := func(v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return true
		}
		wctx, done := context.WithTimeout(ctx, 10*time.Second)
		defer done()
		return c.Write(wctx, websocket.MessageText, b) == nil
	}
	for {
		select {
		case <-ctx.Done():
			if base.Err() != nil {
				_ = c.Close(websocket.StatusGoingAway, "server shutdown")
			}
			return
		case <-ping.C:
			pctx, done := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			done()
			if err != nil || !sessionOK() {
				return
			}
			if sub.TakeOverflow() && !write(core.Event{Topic: "*", Kind: "resync"}) {
				return
			}
		case e := <-sub.C:
			if e.Topic == core.TopicUsers && e.ID == p.UserID && !sessionOK() {
				return
			}
			if e.ZoneID != "" && !p.CanViewZone(e.ZoneID) {
				continue
			}
			if p.Role == core.RoleUser && e.Topic != core.TopicLive && e.Topic != core.TopicZones && e.Topic != core.TopicDevices {
				continue
			}
			e.Data = nil // never leak payloads; pages reload through the API with role checks
			if !write(e) {
				return
			}
		}
	}
}
