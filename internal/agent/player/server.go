// Package player is the loopback server on 127.0.0.1:7600 that Chromium loads: the
// player app (embedded build output), the cached assets, bundled and uploaded fonts,
// and the player WebSocket (ARCHITECTURE §5.8). The player never sees the device
// token; it only talks to this server.
package player

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"opensignage/internal/agent/safe"
	"opensignage/internal/agent/wire"
)

//go:embed all:static
var staticFS embed.FS

//go:embed fallback.html
var fallbackHTML []byte

const maxClients = 8

// Files resolves cached content of the active deployment.
type Files interface {
	AssetFile(assetID string) (path, mime, sha string, ok bool)
	FontFile(sha string) (path, mime string, ok bool)
}

// Server serves the player.
type Server struct {
	Port     int
	ShareDir string
	Files    Files
	Hub      *Hub
	// OnPing receives player heartbeats.
	OnPing func(wire.PlayerPing)
	// OnLog receives the player's log lines (after the rate limit).
	OnLog func(wire.PlayerLog)

	logMu     sync.Mutex
	logWindow time.Time
	logCount  int
}

// Handler returns the HTTP handler (exported for tests).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", s.ws)
	mux.HandleFunc("GET /assets/{id}", s.asset)
	mux.HandleFunc("GET /fonts/custom/{sha}", s.customFont)
	fontDir := http.Dir(filepath.Join(s.ShareDir, "fonts"))
	mux.Handle("GET /fonts/bundled/", http.StripPrefix("/fonts/bundled/", noDirListing(http.FileServer(fontDir))))
	mux.Handle("GET /", s.app())
	return s.guard(mux)
}

// guard admits only loopback clients addressing the loopback host (no DNS rebinding).
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !LoopbackRemote(r.RemoteAddr) {
			slog.Warn("player request from non-loopback address refused", "remote", r.RemoteAddr)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		switch strings.Trim(host, "[]") {
		case "127.0.0.1", "localhost", "::1":
		default:
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// LoopbackRemote reports whether addr (host:port) is a loopback address.
func LoopbackRemote(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Run listens on 127.0.0.1:Port until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Port)))
	if err != nil {
		return fmt.Errorf("player server: %w", err)
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, ErrorLog: safe.HTTPErrorLog()}
	safe.Go("player-server-shutdown", func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	})
	slog.Info("player server listening", "addr", ln.Addr().String())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// HasApp reports whether the built player app is embedded.
func HasApp() bool {
	_, err := fs.Stat(staticFS, "static/index.html")
	return err == nil
}

func (s *Server) app() http.Handler {
	if !HasApp() {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" && r.URL.Path != "/index.html" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(fallbackHTML)
		})
	}
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
}

func noDirListing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		// manifest.json is revalidated on every load (an update can add families); font
		// URLs carry the content hash (?v=…), so changed files are fetched at once.
		if path.Base(r.URL.Path) == "manifest.json" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	if s.Files == nil {
		http.NotFound(w, r)
		return
	}
	p, mime, sha, ok := s.Files.AssetFile(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	serveFile(w, r, p, mime, sha)
}

func (s *Server) customFont(w http.ResponseWriter, r *http.Request) {
	if s.Files == nil {
		http.NotFound(w, r)
		return
	}
	sha := r.PathValue("sha")
	p, mime, ok := s.Files.FontFile(sha)
	if !ok {
		http.NotFound(w, r)
		return
	}
	serveFile(w, r, p, mime, sha)
}

// serveFile serves a cached blob with Range support; the SHA-256 is a perfect ETag.
func serveFile(w http.ResponseWriter, r *http.Request, path, mime, sha string) {
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if mime != "" {
		w.Header().Set("Content-Type", mime)
	}
	w.Header().Set("ETag", `"`+sha+`"`)
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil) // same-origin check: Origin must match Host
	if err != nil {
		slog.Warn("player websocket refused", "err", err)
		return
	}
	conn.SetReadLimit(64 << 10)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c := &client{send: make(chan []byte, 64), drop: cancel}
	if !s.Hub.add(c) {
		_ = conn.Close(websocket.StatusTryAgainLater, "too many players")
		return
	}
	defer s.Hub.remove(c)
	slog.Info("player connected", "remote", r.RemoteAddr, "clients", s.Hub.Clients())
	safe.Go("player-ws-writer", func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-c.send:
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Write(wctx, websocket.MessageText, msg)
				wcancel()
				if err != nil {
					return
				}
			}
		}
	})
	defer safe.Recover("player-ws-reader")
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			status := websocket.CloseStatus(err)
			if status != websocket.StatusNormalClosure && status != websocket.StatusGoingAway && ctx.Err() == nil {
				slog.Info("player disconnected", "err", err)
			}
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return
		}
		s.handle(data)
	}
}

func (s *Server) handle(data []byte) {
	var env wire.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		slog.Debug("invalid player message", "err", err)
		return
	}
	switch env.T {
	case wire.TPing:
		var p wire.PlayerPing
		if err := json.Unmarshal(env.P, &p); err != nil {
			slog.Debug("invalid player ping", "err", err)
			return
		}
		if s.OnPing != nil {
			s.OnPing(p)
		}
	case wire.TLog:
		var l wire.PlayerLog
		if err := json.Unmarshal(env.P, &l); err != nil || !s.allowLog() {
			return
		}
		msg := l.Msg
		if len(msg) > 2000 {
			msg = msg[:2000] + "…"
		}
		switch l.Level {
		case "error":
			slog.Error("player: "+msg, "source", "player")
		case "warn":
			slog.Warn("player: "+msg, "source", "player")
		default:
			slog.Info("player: "+msg, "source", "player")
		}
		if s.OnLog != nil {
			s.OnLog(l)
		}
	default:
		slog.Debug("unknown player message ignored", "type", env.T)
	}
}

// allowLog limits player log lines to 50 per 10 s (a looping player must not flood
// the log files).
func (s *Server) allowLog() bool {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	now := time.Now()
	if now.Sub(s.logWindow) > 10*time.Second {
		s.logWindow, s.logCount = now, 0
	}
	s.logCount++
	return s.logCount <= 50
}
