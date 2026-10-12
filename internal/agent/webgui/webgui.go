// Package webgui is the client WebGUI on port 8443: HTTPS with a
// self-signed certificate (plain HTTP on the same port is redirected), server-rendered
// pages without external resources, login with a throttle, forced password change,
// session cookie (Secure, HttpOnly, SameSite=Strict) and CSRF tokens in every form.
package webgui

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"opensignage/internal/agent/display"
	"opensignage/internal/agent/safe"
	"opensignage/internal/agent/state"
	"opensignage/internal/agent/wire"
	"opensignage/internal/common/ids"
	"opensignage/internal/common/passwords"
	"opensignage/internal/common/ratelimit"
	"opensignage/internal/common/tlsutil"
	"opensignage/locales"
)

// DefaultPassword is the initial client WebGUI password; it must be changed at the
// first login.
const DefaultPassword = "opensignage"

// Status is everything the status page shows.
type Status struct {
	Version          string
	Hostname         string
	IPs              []string
	WebGUIURL        string
	LinkPhase        string
	LinkError        string
	LinkDetail       string
	ServerAddress    string
	ManualAddress    string
	Fingerprint      string // short, pinned
	FingerprintFull  string
	Presented        string // short fingerprint presented by the server when it changed
	PairingPIN       string
	Paired           bool
	ServerVersion    string
	HaveWelcome      bool
	VersionMatch     bool
	DeviceName       string
	ZoneID           string
	ZoneName         string
	Display          wire.DisplayInfo
	HaveDisplay      bool
	Temperature      *float64
	CPU              float64
	RAMUsed          uint64
	RAMTotal         uint64
	DiskTotal        uint64
	DiskFree         uint64
	CacheBytes       int64
	ActiveDeployment string
	Source           wire.ActiveSource
	PlaylistName     string
	Sync             wire.SyncInfo
	Player           string
	PlayerRestarts   int
	PlayerError      string
	Mode             string
	ClockOffset      time.Duration
	Uptime           time.Duration
}

// UpdateView is what the update page shows.
type UpdateView struct {
	Own        string
	Server     string // server version ("" = unknown)
	HaveServer bool
	Match      bool
	Repo       string // configured update repository (from the server)
	Phase      string
	Version    string
	Done       int64
	Total      int64
	Error      string
	UpdatedAt  time.Time
	// Fallback: the GitHub download of exactly the server version is possible.
	Fallback     bool
	FallbackNote string // why it is not possible (shown on a version mismatch)
}

// Backend is implemented by the agent.
type Backend interface {
	UpdateView(ctx context.Context) UpdateView
	StartGitHubUpdate() error
	StatusView(ctx context.Context) Status
	ServerAddressSettings() (manual string, setByUser bool, fromConfig string)
	SetServerAddress(addr string) error
	Repair() error
	FactoryReset() error
	DisplayOutputs(ctx context.Context) ([]display.Output, error)
	DisplaySetting() (wire.Display, string)
	SetDisplay(ctx context.Context, d wire.Display) error
	RestartPlayer() error
	Regeneration() bool
	SetRegeneration(on bool, source string)
	RebootAllowed() error
	Reboot() error
	SetLanguage(lang string)
	DeviceLanguage() string
	CentralPasswordHash() string
	LogFiles() []string
}

// Server is the client WebGUI.
type Server struct {
	Port    int
	TLSDir  string
	Store   *state.Store
	I18n    *locales.Bundle
	Backend Backend

	mu       sync.Mutex
	sessions map[string]*session
	throttle *ratelimit.Throttle
	global   *ratelimit.Throttle
	tmpl     *template.Template
	cert     *tlsutil.Pair
}

type session struct {
	csrf     string
	created  time.Time
	lastSeen time.Time
}

const (
	sessionCookie = "os_client_session"
	loginCookie   = "os_client_login"
	langCookie    = "os_client_lang"
	maxSessions   = 16
	idleTimeout   = 8 * time.Hour
	maxLifetime   = 24 * time.Hour
)

// New prepares templates, the certificate and the default password.
func New(port int, tlsDir string, st *state.Store, bundle *locales.Bundle, b Backend) (*Server, error) {
	s := &Server{Port: port, TLSDir: tlsDir, Store: st, I18n: bundle, Backend: b, sessions: map[string]*session{},
		throttle: ratelimit.New(), global: ratelimit.New()}
	s.global.Free = 50
	var err error
	if s.tmpl, err = parseTemplates(bundle); err != nil {
		return nil, err
	}
	if err := s.ensurePassword(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) ensurePassword() error {
	w, err := s.Store.LoadWebGUI()
	if err != nil {
		return fmt.Errorf("webgui.json: %w", err)
	}
	if w.PasswordHash != "" {
		return nil
	}
	h, err := passwords.Hash(DefaultPassword)
	if err != nil {
		return err
	}
	slog.Info("client WebGUI password initialised with the default (change required at first login)")
	return s.Store.SaveWebGUI(state.WebGUI{PasswordHash: h, MustChange: true})
}

// SetPassword stores a new local password (installer command, password page).
func SetPassword(st *state.Store, pw string, mustChange bool) error {
	if err := passwords.CheckPolicy(pw); err != nil {
		return err
	}
	h, err := passwords.Hash(pw)
	if err != nil {
		return err
	}
	return st.SaveWebGUI(state.WebGUI{PasswordHash: h, MustChange: mustChange, ChangedAt: time.Now().UTC().Format(time.RFC3339)})
}

// Run serves HTTPS (and the HTTP redirect) until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	host := "client"
	if h, err := netHostname(); err == nil {
		host = h
	}
	cert, err := tlsutil.EnsureSelfSigned(s.TLSDir, "webgui", "OpenSignage client ("+host+")", 10)
	if err != nil {
		return fmt.Errorf("webgui certificate: %w", err)
	}
	s.cert = cert
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(s.Port))
	if err != nil {
		return fmt.Errorf("webgui: %w", err)
	}
	return s.Serve(ctx, ln)
}

// Serve runs on an existing listener (tests).
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if s.cert == nil {
		cert, err := tlsutil.EnsureSelfSigned(s.TLSDir, "webgui", "OpenSignage client", 10)
		if err != nil {
			return err
		}
		s.cert = cert
	}
	split := newSplitListener(ln)
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{s.cert.TLS}}
	httpsSrv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second,
		WriteTimeout: 120 * time.Second, IdleTimeout: 120 * time.Second, ErrorLog: safe.HTTPErrorLog(), MaxHeaderBytes: 64 << 10}
	redirectSrv := &http.Server{Handler: http.HandlerFunc(redirect), ReadHeaderTimeout: 10 * time.Second, ErrorLog: safe.HTTPErrorLog()}
	safe.Go("webgui-redirect", func() { _ = redirectSrv.Serve(split.plain) })
	safe.Go("webgui-shutdown", func() {
		<-ctx.Done()
		_ = split.Close()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpsSrv.Shutdown(sctx)
		_ = redirectSrv.Shutdown(sctx)
	})
	slog.Info("client WebGUI listening", "addr", ln.Addr().String())
	if err := httpsSrv.Serve(tls.NewListener(split.tls, tlsCfg)); err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
		return err
	}
	return nil
}

func redirect(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if host == "" {
		http.Error(w, "use https", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
}

// ---------------------------------------------------------------- routing

// Handler returns the HTTPS handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.loginPost)
	mux.HandleFunc("POST /logout", s.auth(false, s.logout))
	mux.HandleFunc("POST /lang", s.langPost)
	mux.HandleFunc("GET /password", s.auth(true, s.passwordPage))
	mux.HandleFunc("POST /password", s.auth(true, s.passwordPost))
	mux.HandleFunc("GET /{$}", s.auth(false, s.statusPage))
	mux.HandleFunc("GET /server", s.auth(false, s.serverPage))
	mux.HandleFunc("POST /server", s.auth(false, s.serverPost))
	mux.HandleFunc("GET /pairing", s.auth(false, s.pairingPage))
	mux.HandleFunc("POST /pairing", s.auth(false, s.pairingPost))
	mux.HandleFunc("GET /display", s.auth(false, s.displayPage))
	mux.HandleFunc("POST /display", s.auth(false, s.displayPost))
	mux.HandleFunc("GET /actions", s.auth(false, s.actionsPage))
	mux.HandleFunc("POST /actions", s.auth(false, s.actionsPost))
	mux.HandleFunc("GET /logs", s.auth(false, s.logsPage))
	mux.HandleFunc("GET /logs/download", s.auth(false, s.logsDownload))
	mux.HandleFunc("GET /update", s.auth(false, s.updatePage))
	mux.HandleFunc("POST /update", s.auth(false, s.updatePost))
	mux.HandleFunc("GET /style.css", serveCSS)
	return s.secure(mux)
}

func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("webgui panic", "path", r.URL.Path, "panic", rec)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; img-src 'self' data:; script-src 'none'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------- sessions

func (s *Server) session(r *http.Request) (string, *session) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[c.Value]
	if sess == nil {
		return "", nil
	}
	now := time.Now()
	if now.Sub(sess.lastSeen) > idleTimeout || now.Sub(sess.created) > maxLifetime {
		delete(s.sessions, c.Value)
		return "", nil
	}
	sess.lastSeen = now
	return c.Value, sess
}

func (s *Server) newSession(w http.ResponseWriter) {
	token := ids.Token(32)
	s.mu.Lock()
	if len(s.sessions) >= maxSessions {
		var oldest string
		var at time.Time
		for k, v := range s.sessions {
			if oldest == "" || v.lastSeen.Before(at) {
				oldest, at = k, v.lastSeen
			}
		}
		delete(s.sessions, oldest)
	}
	now := time.Now()
	s.sessions[token] = &session{csrf: ids.Token(32), created: now, lastSeen: now}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(maxLifetime.Seconds())})
}

func (s *Server) dropSessions(except string) {
	s.mu.Lock()
	for k := range s.sessions {
		if k != except {
			delete(s.sessions, k)
		}
	}
	s.mu.Unlock()
}

// mustChange reports whether the local default password must be replaced first.
func (s *Server) mustChange() bool {
	if s.Backend.CentralPasswordHash() != "" {
		return false
	}
	w, err := s.Store.LoadWebGUI()
	return err == nil && w.MustChange
}

type authedHandler func(w http.ResponseWriter, r *http.Request, token string, sess *session)

// auth requires a session; unless allowMustChange, a pending forced password change
// redirects to /password. POST requests must carry the session's CSRF token.
func (s *Server) auth(allowMustChange bool, h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, sess := s.session(r)
		if sess == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost && !validCSRF(r.PostFormValue("csrf"), sess.csrf) {
			s.forbidden(w, r)
			return
		}
		if !allowMustChange && s.mustChange() {
			http.Redirect(w, r, "/password", http.StatusSeeOther)
			return
		}
		h(w, r, token, sess)
	}
}

func validCSRF(got, want string) bool {
	return got != "" && want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *Server) forbidden(w http.ResponseWriter, r *http.Request) {
	slog.Warn("webgui request with invalid CSRF token refused", "path", r.URL.Path, "remote", r.RemoteAddr)
	http.Error(w, s.t(s.lang(r), "error.csrf"), http.StatusForbidden)
}

// loginCSRF returns (and sets) the pre-session CSRF cookie of the login form.
func loginCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(loginCookie); err == nil && len(c.Value) == 64 {
		return c.Value
	}
	v := ids.Token(32)
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: v, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return v
}

// ---------------------------------------------------------------- language

func (s *Server) lang(r *http.Request) string {
	if c, err := r.Cookie(langCookie); err == nil && locales.Supported(c.Value) {
		return c.Value
	}
	if l := s.Backend.DeviceLanguage(); locales.Supported(l) {
		return l
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		code := strings.ToLower(strings.TrimSpace(strings.SplitN(strings.SplitN(part, ";", 2)[0], "-", 2)[0]))
		if locales.Supported(code) {
			return code
		}
	}
	return locales.Default
}

func (s *Server) t(lang, key string, params ...any) string {
	return s.I18n.T(lang, "client."+key, params...)
}

func (s *Server) langPost(w http.ResponseWriter, r *http.Request) {
	l := r.PostFormValue("lang")
	if !locales.Supported(l) {
		http.Error(w, "unsupported language", http.StatusBadRequest)
		return
	}
	_, sess := s.session(r)
	csrf := r.PostFormValue("csrf")
	switch {
	case sess != nil && validCSRF(csrf, sess.csrf):
		s.Backend.SetLanguage(l) // also the language of the player screens
	case sess == nil:
		c, err := r.Cookie(loginCookie)
		if err != nil || !validCSRF(csrf, c.Value) {
			s.forbidden(w, r)
			return
		}
	default:
		s.forbidden(w, r)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: langCookie, Value: l, Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: 365 * 24 * 3600})
	back := r.PostFormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// ---------------------------------------------------------------- rendering

type page struct {
	L        string
	Title    string
	Nav      string
	CSRF     string
	LoggedIn bool
	Back     string
	Langs    []langOption
	Flash    string
	Error    string
	Data     any
	Body     template.HTML
}

type langOption struct {
	Code, Name string
	Selected   bool
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, p *page) {
	p.L = s.lang(r)
	p.Back = r.URL.Path
	for _, l := range locales.Languages {
		p.Langs = append(p.Langs, langOption{Code: l.Code, Name: l.Name, Selected: l.Code == p.L})
	}
	if p.Title == "" {
		p.Title = s.t(p.L, "nav."+name)
	}
	var body bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&body, name, p); err != nil {
		slog.Error("webgui template failed", "page", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p.Body = template.HTML(body.String()) //nolint:gosec // rendered by html/template above
	var out bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&out, "layout", p); err != nil {
		slog.Error("webgui layout failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(out.Bytes())
}

func (s *Server) authedPage(r *http.Request, sess *session, nav string) *page {
	return &page{Nav: nav, CSRF: sess.csrf, LoggedIn: true, Flash: r.URL.Query().Get("ok")}
}

// flash redirects back with a translated confirmation (key in the query string).
func flash(w http.ResponseWriter, r *http.Request, path, key string) {
	http.Redirect(w, r, path+"?ok="+key, http.StatusSeeOther)
}

func netHostname() (string, error) {
	h, err := osHostname()
	if err != nil || h == "" {
		return "", errors.New("no hostname")
	}
	return h, nil
}
