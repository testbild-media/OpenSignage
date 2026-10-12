// Package app wires the server: database, settings, certificates, modules and the three
// listeners (80 redirect, 443 WebGUI, 7443 device channel) plus the discovery responder.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"opensignage/internal/common/discovery"
	"opensignage/internal/common/ids"
	"opensignage/internal/common/tlsutil"
	"opensignage/internal/common/version"
	"opensignage/internal/server/config"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
	"opensignage/internal/server/system"
	"opensignage/internal/server/users"
	"opensignage/internal/server/web"
)

// Module registers WebGUI API routes.
type Module interface {
	Routes(rt *core.Router)
}

// DeviceModule registers device channel routes (port 7443).
type DeviceModule interface {
	DeviceRoutes(mux *http.ServeMux)
}

// Runner is a background loop started with the server.
type Runner interface {
	Run(ctx context.Context)
}

// Server is the assembled server.
type Server struct {
	App       *core.App
	WebMux    *http.ServeMux
	DeviceMux *http.ServeMux
	Router    *core.Router
	modules   []any
	serverID  string
	restart   chan string
}

// Restart ends Run so the service manager starts the server again (controlled restart).
func (s *Server) Restart(reason string) {
	select {
	case s.restart <- reason:
	default:
	}
}

// New opens everything and registers all modules.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*Server, error) {
	restored, err := system.ApplyPendingRestore(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("applying staged restore: %w", err)
	}
	d, err := db.Open(ctx, filepath.Join(cfg.DataDir, "db", "opensignage.db"))
	if err != nil {
		return nil, err
	}
	if err := system.AfterRestore(ctx, d.W, restored); err != nil {
		slog.Error("post-restore cleanup failed", "err", err)
	}
	settings, err := core.LoadSettings(ctx, d)
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	a := &core.App{Cfg: cfg, DB: d, Log: log, Settings: settings, Bus: core.NewBus(), Sessions: core.NewSessions(d), WebCert: &tlsutil.Store{}}
	if prev, ended, err := a.Sessions.EndAfterVersionChange(ctx, version.Version); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("checking the server version: %w", err)
	} else if prev != "" && prev != version.Version {
		slog.Info("server version changed: all login sessions ended", "from", prev, "to", version.Version, "sessions", ended)
	}
	s := &Server{App: a, WebMux: http.NewServeMux(), DeviceMux: http.NewServeMux(), restart: make(chan string, 1)}
	if err := s.initTLS(); err != nil {
		_ = d.Close()
		return nil, err
	}
	s.serverID, err = s.loadServerID()
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	s.Router = core.NewRouter(s.WebMux, a.Sessions)

	s.modules = append(s.modules, users.New(a), system.New(a, s))
	s.modules = append(s.modules, extraModules(a)...)

	for _, m := range s.modules {
		if wm, ok := m.(Module); ok {
			wm.Routes(s.Router)
		}
		if dm, ok := m.(DeviceModule); ok {
			dm.DeviceRoutes(s.DeviceMux)
		}
	}
	s.Router.Public("GET /api/v1/system/info", s.systemInfo)
	s.WebMux.Handle("GET /ca.crt", http.HandlerFunc(s.serveCA))
	// Bundled fonts (offline, never from a CDN); custom fonts are served by the media module.
	fontDir := http.Dir(filepath.Join(cfg.ShareDir, "fonts"))
	s.WebMux.Handle("GET /fonts/", http.StripPrefix("/fonts/", cacheFonts(http.FileServer(fontDir))))
	s.WebMux.Handle("/api/", httpx.Adapt(func(http.ResponseWriter, *http.Request) error { return httpx.ErrNotFound }))
	s.WebMux.Handle("/", web.Handler())
	return s, nil
}

// registry holds module constructors registered by mod_*.go files (one file per module,
// so parallel work never conflicts in this file).
var registry []func(*core.App) any

// Register adds a module constructor; call it from an init() in mod_<name>.go.
func Register(f func(*core.App) any) { registry = append(registry, f) }

func extraModules(a *core.App) []any {
	out := make([]any, 0, len(registry))
	for _, f := range registry {
		out = append(out, f(a))
	}
	return out
}

// ServerID returns the stable id of this installation.
func (s *Server) ServerID() string { return s.serverID }

func (s *Server) loadServerID() (string, error) {
	p := filepath.Join(s.App.Cfg.DataDir, "server-id")
	if b, err := os.ReadFile(p); err == nil && ids.Valid(strings.TrimSpace(string(b))) {
		return strings.TrimSpace(string(b)), nil
	}
	id := ids.New()
	return id, os.WriteFile(p, []byte(id+"\n"), 0o640)
}

func (s *Server) tlsDir() string { return filepath.Join(s.App.Cfg.DataDir, "tls") }

func (s *Server) initTLS() error {
	host, _ := os.Hostname()
	ca, err := tlsutil.EnsureCA(s.tlsDir(), "OpenSignage Local CA ("+host+")")
	if err != nil {
		return fmt.Errorf("local CA: %w", err)
	}
	s.App.CA = ca
	dev, err := tlsutil.EnsureSelfSigned(s.tlsDir(), "device", "OpenSignage device channel ("+host+")", 30)
	if err != nil {
		return fmt.Errorf("device certificate: %w", err)
	}
	s.App.DeviceID = dev
	return s.RefreshWebCert()
}

// RefreshWebCert serves the uploaded certificate if present and valid, otherwise a leaf
// from the local CA covering all current addresses (reissued when addresses change).
func (s *Server) RefreshWebCert() error {
	dir := s.tlsDir()
	if p, err := tlsutil.Load(filepath.Join(dir, "custom.crt"), filepath.Join(dir, "custom.key")); err == nil {
		if time.Now().Before(p.Cert.NotAfter) {
			s.App.WebCert.Set(p)
			return nil
		}
		slog.Warn("uploaded certificate expired, falling back to local CA certificate", "notAfter", p.Cert.NotAfter)
	} else if !errors.Is(err, os.ErrNotExist) {
		slog.Error("uploaded certificate unreadable, falling back to local CA certificate", "err", err)
	}
	leaf, issued, err := tlsutil.EnsureLeaf(dir, "web", s.App.CA, tlsutil.LocalNames())
	if err != nil {
		return fmt.Errorf("web certificate: %w", err)
	}
	if issued {
		slog.Info("issued WebGUI certificate", "dns", leaf.Cert.DNSNames, "ips", fmt.Sprint(leaf.Cert.IPAddresses))
	}
	s.App.WebCert.Set(leaf)
	return nil
}

func (s *Server) serveCA(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="opensignage-ca.crt"`)
	_, _ = w.Write(pemCert(s.App.CA.Cert.Raw))
}

func (s *Server) systemInfo(w http.ResponseWriter, _ *http.Request) error {
	st := s.App.Settings.Get()
	return httpx.OK(w, map[string]any{"version": version.Version, "name": st.ServerName})
}

// DiscoveryInfo is the answer to LAN discovery requests.
func (s *Server) DiscoveryInfo() discovery.Info {
	return discovery.Info{ServerID: s.serverID, Name: s.App.Settings.Get().ServerName, Version: version.Version,
		DevicePort: s.App.Cfg.DevicePort, Fingerprint: tlsutil.SPKIFingerprint(s.App.DeviceID.Cert),
		Addresses: tlsutil.InterfaceIPs(false)}
}

// Run starts listeners and background jobs and blocks until ctx is cancelled, then
// shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	cfg := s.App.Cfg
	if cfg.DevPorts {
		slog.Warn("WebGUI is not on ports 80/443 (development override)", "http", cfg.HTTPPort, "https", cfg.HTTPSPort)
	}
	webTLS := s.App.WebCert.Config()
	webSrv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.HTTPSPort),
		Handler:           httpx.Recover(httpx.SecurityHeaders(s.WebMux)),
		TLSConfig:         webTLS,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}
	devSrv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.DevicePort),
		Handler:           httpx.Recover(s.DeviceMux),
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{s.App.DeviceID.TLS}},
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}
	redirect := s.redirectServer(":" + strconv.Itoa(cfg.HTTPPort))

	var lns []net.Listener
	listen := func(addr string) (net.Listener, error) {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			for _, l := range lns {
				_ = l.Close()
			}
			return nil, fmt.Errorf("cannot listen on %s: %w", addr, err)
		}
		lns = append(lns, ln)
		return ln, nil
	}
	webLn, err := listen(webSrv.Addr)
	if err != nil {
		return err
	}
	devLn, err := listen(devSrv.Addr)
	if err != nil {
		return err
	}
	redLn, err := listen(redirect.Addr)
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errCh := make(chan error, 3)
	serve := func(name string, f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("%s: %w", name, err)
			}
		}()
	}
	serve("webgui", func() error { return webSrv.ServeTLS(webLn, "", "") })
	serve("device", func() error { return devSrv.ServeTLS(devLn, "", "") })
	serve("redirect", func() error { return redirect.Serve(redLn) })

	background := func(name string, f func(ctx context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			backoff := time.Second
			for runCtx.Err() == nil {
				started := time.Now()
				func() {
					defer func() {
						if p := recover(); p != nil {
							slog.Error("background job crashed, restarting", "job", name, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
						}
					}()
					f(runCtx)
				}()
				if runCtx.Err() != nil {
					return
				}
				if time.Since(started) > 10*time.Minute {
					backoff = time.Second // it ran fine for a while: start over
				}
				slog.Warn("background job ended, restarting", "job", name, "in", backoff.String())
				select {
				case <-runCtx.Done():
					return
				case <-time.After(backoff):
				}
				backoff = min(backoff*2, time.Minute)
			}
		}()
	}
	background("discovery", func(ctx context.Context) { discovery.Respond(ctx, cfg.DiscoveryPort, s.DiscoveryInfo) })
	background("maintenance", s.maintenance)
	for i, m := range s.modules {
		if r, ok := m.(Runner); ok {
			background(fmt.Sprintf("module-%d", i), r.Run)
		}
	}

	slog.Info("OpenSignage server started", "version", version.Version, "webgui", "https://"+firstIP()+portSuffix(cfg.HTTPSPort, 443),
		"devicePort", cfg.DevicePort, "discoveryPort", cfg.DiscoveryPort, "data", cfg.DataDir)

	var runErr error
	select {
	case <-ctx.Done():
	case reason := <-s.restart:
		slog.Info("controlled restart", "reason", reason)
	case runErr = <-errCh:
		slog.Error("listener failed, shutting down", "err", runErr)
	}
	cancel()
	shutCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	for _, srv := range []*http.Server{webSrv, devSrv, redirect} {
		if err := srv.Shutdown(shutCtx); err != nil {
			slog.Warn("shutdown", "addr", srv.Addr, "err", err)
		}
	}
	wg.Wait()
	if err := s.App.DB.Close(); err != nil {
		slog.Warn("closing database", "err", err)
	}
	slog.Info("OpenSignage server stopped")
	return runErr
}

func (s *Server) maintenance(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.App.Sessions.Cleanup(ctx)
			system.SweepTemp(s.App.Cfg.DataDir)
			if err := s.RefreshWebCert(); err != nil {
				slog.Error("refreshing WebGUI certificate", "err", err)
			}
		}
	}
}

// redirectServer is the plain-HTTP listener: it only redirects to HTTPS and serves the CA
// certificate, with short timeouts (it faces untrusted clients without TLS).
func (s *Server) redirectServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           httpx.Recover(http.HandlerFunc(s.redirectHandler)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
}

func (s *Server) redirectHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/ca.crt" {
		s.serveCA(w, r)
		return
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" { // HTTP/1.0 without Host header: "https:///…" would be no URL at all
		host = firstIP()
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	target := "https://" + host + portSuffix(s.App.Cfg.HTTPSPort, 443) + r.URL.RequestURI()
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

func portSuffix(port, def int) string {
	if port == def {
		return ""
	}
	return ":" + strconv.Itoa(port)
}

func firstIP() string {
	if ips := tlsutil.InterfaceIPs(false); len(ips) > 0 {
		return ips[0]
	}
	return "localhost"
}
