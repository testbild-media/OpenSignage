package webgui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"opensignage/internal/agent/display"
	"opensignage/internal/agent/wire"
	"opensignage/internal/common/passwords"
)

var osHostname = os.Hostname

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------------------------------------------------------------- login

type loginData struct {
	Default bool // the default password is still active
	// NetworkPending: the login confirms a changed network setting.
	NetworkPending bool
}

func (s *Server) loginData() loginData {
	return loginData{Default: s.mustChange(), NetworkPending: s.networkBackend().NetworkPending()}
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, sess := s.session(r); sess != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login", &page{CSRF: loginCSRF(w, r), Data: s.loginData()})
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	lang := s.lang(r)
	c, err := r.Cookie(loginCookie)
	if err != nil || !validCSRF(r.PostFormValue("csrf"), c.Value) {
		s.forbidden(w, r)
		return
	}
	ip := clientIP(r)
	if d := max(s.throttle.Check(ip), s.global.Check("*")); d > 0 {
		slog.Warn("webgui login throttled", "remote", ip, "wait", d.Round(time.Second).String())
		s.render(w, r, http.StatusTooManyRequests, "login", &page{CSRF: c.Value, Data: s.loginData(),
			Error: s.t(lang, "login.throttled", "seconds", int(math.Ceil(d.Seconds())))})
		return
	}
	pw := r.PostFormValue("password")
	hash := s.Backend.CentralPasswordHash()
	if hash == "" {
		wg, err := s.Store.LoadWebGUI()
		if err != nil {
			slog.Error("webgui.json unreadable", "err", err)
		}
		hash = wg.PasswordHash
	}
	if hash == "" || !passwords.Verify(pw, hash) {
		if hash == "" {
			passwords.DummyVerify()
		}
		s.throttle.Fail(ip)
		s.global.Fail("*")
		slog.Warn("webgui login failed", "remote", ip)
		s.render(w, r, http.StatusUnauthorized, "login", &page{CSRF: c.Value, Data: s.loginData(),
			Error: s.t(lang, "login.failed")})
		return
	}
	s.throttle.Success(ip)
	slog.Info("webgui login", "remote", ip)
	s.newSession(w)
	// A new login confirms a changed network setting (it is permanent from now on).
	switch s.confirmNetwork(r) {
	case confirmDone:
		flash(w, r, "/network", "networkConfirmed")
		return
	case confirmApplying:
		http.Redirect(w, r, "/network", http.StatusSeeOther) // it follows the change
		return
	case confirmFailed:
		http.Redirect(w, r, "/network?err=confirm", http.StatusSeeOther)
		return
	}
	if s.networkBackend().NetworkFailed() {
		http.Redirect(w, r, "/network", http.StatusSeeOther) // it says why the change failed
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, token string, _ *session) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ---------------------------------------------------------------- password

type passwordData struct {
	Central bool
	Forced  bool
}

func (s *Server) passwordPage(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	p := s.authedPage(r, sess, "password")
	p.Data = passwordData{Central: s.Backend.CentralPasswordHash() != "", Forced: s.mustChange()}
	s.render(w, r, http.StatusOK, "password", p)
}

func (s *Server) passwordPost(w http.ResponseWriter, r *http.Request, token string, sess *session) {
	lang := s.lang(r)
	p := s.authedPage(r, sess, "password")
	forced := s.mustChange()
	p.Data = passwordData{Central: s.Backend.CentralPasswordHash() != "", Forced: forced}
	fail := func(key string) {
		p.Error = s.t(lang, key)
		s.render(w, r, http.StatusUnprocessableEntity, "password", p)
	}
	if s.Backend.CentralPasswordHash() != "" {
		fail("password.central")
		return
	}
	wg, err := s.Store.LoadWebGUI()
	if err != nil {
		fail("error.generic")
		return
	}
	if !forced {
		ip := clientIP(r)
		if d := s.throttle.Check(ip); d > 0 {
			fail("login.locked")
			return
		}
		if !passwords.Verify(r.PostFormValue("current"), wg.PasswordHash) {
			s.throttle.Fail(ip)
			fail("password.wrongCurrent")
			return
		}
	}
	pw, again := r.PostFormValue("new"), r.PostFormValue("repeat")
	if pw != again {
		fail("password.mismatch")
		return
	}
	switch err := passwords.CheckPolicy(pw); {
	case errors.Is(err, passwords.ErrTooShort):
		fail("password.tooShort")
		return
	case err != nil:
		fail("password.tooLong")
		return
	}
	if pw == DefaultPassword {
		fail("password.notDefault")
		return
	}
	if err := SetPassword(s.Store, pw, false); err != nil {
		slog.Error("saving webgui password failed", "err", err)
		fail("error.generic")
		return
	}
	s.dropSessions(token)
	slog.Info("client WebGUI password changed", "remote", clientIP(r))
	flash(w, r, "/", "passwordChanged")
}

// ---------------------------------------------------------------- status

func (s *Server) statusPage(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	p := s.authedPage(r, sess, "status")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	p.Data = s.Backend.StatusView(ctx)
	s.render(w, r, http.StatusOK, "status", p)
}

// ---------------------------------------------------------------- server address

type serverData struct {
	Manual     string
	SetByUser  bool
	FromConfig string
	Automatic  bool
	Status     Status
}

func (s *Server) serverData(r *http.Request) serverData {
	manual, byUser, fromCfg := s.Backend.ServerAddressSettings()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	return serverData{Manual: manual, SetByUser: byUser, FromConfig: fromCfg, Automatic: manual == "", Status: s.Backend.StatusView(ctx)}
}

func (s *Server) serverPage(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	p := s.authedPage(r, sess, "server")
	p.Data = s.serverData(r)
	s.render(w, r, http.StatusOK, "server", p)
}

func (s *Server) serverPost(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	addr := ""
	if r.PostFormValue("mode") == "manual" {
		addr = strings.TrimSpace(r.PostFormValue("address"))
		if addr == "" {
			p := s.authedPage(r, sess, "server")
			p.Data = s.serverData(r)
			p.Error = s.t(s.lang(r), "server.addressRequired")
			s.render(w, r, http.StatusUnprocessableEntity, "server", p)
			return
		}
	}
	if err := s.Backend.SetServerAddress(addr); err != nil {
		p := s.authedPage(r, sess, "server")
		p.Data = s.serverData(r)
		p.Error = s.t(s.lang(r), "server.invalid", "error", err.Error())
		s.render(w, r, http.StatusUnprocessableEntity, "server", p)
		return
	}
	flash(w, r, "/server", "serverSaved")
}

// ---------------------------------------------------------------- pairing / reset

func (s *Server) pairingPage(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	p := s.authedPage(r, sess, "pairing")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	p.Data = s.Backend.StatusView(ctx)
	s.render(w, r, http.StatusOK, "pairing", p)
}

func (s *Server) pairingPost(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	if r.PostFormValue("confirm") != "yes" {
		p := s.authedPage(r, sess, "pairing")
		p.Data = s.Backend.StatusView(r.Context())
		p.Error = s.t(s.lang(r), "pairing.confirmRequired")
		s.render(w, r, http.StatusUnprocessableEntity, "pairing", p)
		return
	}
	switch r.PostFormValue("do") {
	case "repair":
		if err := s.Backend.Repair(); err != nil {
			slog.Error("re-pair incomplete", "err", err)
		}
		flash(w, r, "/pairing", "repaired")
	case "reset":
		if err := s.Backend.FactoryReset(); err != nil {
			slog.Error("factory reset incomplete", "err", err)
		}
		s.dropSessions("")
		if err := s.ensurePassword(); err != nil {
			slog.Error("restoring default password failed", "err", err)
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// ---------------------------------------------------------------- display

type modeOption struct {
	Value    string
	Label    string
	Selected bool
}

type displayData struct {
	Available bool
	// NoScreen: the display runs, but no output reports a connected screen.
	NoScreen  bool
	Output    display.Output
	Modes     []modeOption
	Saved     wire.Display
	Error     string
	Custom    bool
	Rotations []rotationOption
}

type rotationOption struct {
	Value    int
	Selected bool
}

func modeValue(w, h int, r float64) string { return fmt.Sprintf("%dx%d@%.2f", w, h, r) }

func (s *Server) displayData(r *http.Request) displayData {
	saved, lastErr := s.Backend.DisplaySetting()
	d := displayData{Saved: saved, Error: lastErr}
	for _, rot := range []int{0, 90, 180, 270} {
		d.Rotations = append(d.Rotations, rotationOption{Value: rot, Selected: rot == saved.Rotation})
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	outs, err := s.Backend.DisplayOutputs(ctx)
	if err != nil {
		if !errors.Is(err, display.ErrUnavailable) {
			d.Error = err.Error()
		}
		return d
	}
	o, ok := display.Primary(outs)
	if !ok {
		d.NoScreen = true
		return d
	}
	d.Available, d.Output = true, o
	found := saved.IsAuto()
	d.Modes = append(d.Modes, modeOption{Value: "auto", Selected: saved.IsAuto()})
	for _, m := range o.Modes {
		sel := !saved.IsAuto() && m.Width == saved.Width && m.Height == saved.Height && math.Abs(m.Refresh-saved.Refresh) < 0.5
		found = found || sel
		label := m.Label()
		if m.Preferred {
			label += " ★"
		}
		d.Modes = append(d.Modes, modeOption{Value: modeValue(m.Width, m.Height, m.Refresh), Label: label, Selected: sel})
	}
	d.Custom = !found
	return d
}

func (s *Server) preferredMode(r *http.Request) wire.Display {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	outs, err := s.Backend.DisplayOutputs(ctx)
	if err != nil {
		return wire.Display{}
	}
	o, ok := display.Primary(outs)
	if !ok {
		return wire.Display{}
	}
	for _, m := range o.Modes {
		if m.Preferred {
			return wire.Display{Width: m.Width, Height: m.Height, Refresh: m.Refresh}
		}
	}
	return wire.Display{}
}

func (s *Server) displayPage(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	p := s.authedPage(r, sess, "display")
	p.Data = s.displayData(r)
	s.render(w, r, http.StatusOK, "display", p)
}

func parseMode(v string) (wire.Display, error) {
	var d wire.Display
	if v == "auto" || v == "" {
		return d, nil
	}
	wh, rate, _ := strings.Cut(v, "@")
	ws, hs, ok := strings.Cut(wh, "x")
	if !ok {
		return d, errors.New("invalid mode")
	}
	var err1, err2, err3 error
	d.Width, err1 = strconv.Atoi(strings.TrimSpace(ws))
	d.Height, err2 = strconv.Atoi(strings.TrimSpace(hs))
	if rate != "" {
		d.Refresh, err3 = strconv.ParseFloat(strings.TrimSpace(rate), 64)
	}
	if err := errors.Join(err1, err2, err3); err != nil {
		return d, errors.New("invalid mode")
	}
	return d, nil
}

func (s *Server) displayPost(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	lang := s.lang(r)
	var d wire.Display
	var err error
	if r.PostFormValue("kind") == "custom" {
		w1, e1 := strconv.Atoi(strings.TrimSpace(r.PostFormValue("width")))
		h1, e2 := strconv.Atoi(strings.TrimSpace(r.PostFormValue("height")))
		rate, e3 := strconv.ParseFloat(strings.TrimSpace(r.PostFormValue("refresh")), 64)
		if err = errors.Join(e1, e2, e3); err == nil {
			d = wire.Display{Width: w1, Height: h1, Refresh: rate, Custom: true}
		}
	} else if v := r.PostFormValue("mode"); v == "auto" {
		d = s.preferredMode(r) // the screen's preferred mode
	} else {
		d, err = parseMode(v)
	}
	if err == nil {
		d.Rotation, err = strconv.Atoi(r.PostFormValue("rotation"))
	}
	if err == nil {
		err = display.Validate(d)
	}
	if err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		err = s.Backend.SetDisplay(ctx, d)
		cancel()
	}
	if err != nil {
		p := s.authedPage(r, sess, "display")
		p.Data = s.displayData(r)
		p.Error = s.t(lang, "display.failed", "error", err.Error())
		s.render(w, r, http.StatusUnprocessableEntity, "display", p)
		return
	}
	flash(w, r, "/display", "displaySaved")
}

// ---------------------------------------------------------------- actions

type actionsData struct {
	RebootError  string
	Player       string
	Regeneration bool
}

func (s *Server) actionsData(r *http.Request) actionsData {
	d := actionsData{}
	if err := s.Backend.RebootAllowed(); err != nil {
		d.RebootError = err.Error()
	}
	d.Player = s.Backend.StatusView(r.Context()).Player
	d.Regeneration = s.Backend.Regeneration()
	return d
}

func (s *Server) actionsPage(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	p := s.authedPage(r, sess, "actions")
	p.Data = s.actionsData(r)
	s.render(w, r, http.StatusOK, "actions", p)
}

func (s *Server) actionsPost(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	var err error
	key := ""
	switch r.PostFormValue("do") {
	case "restart":
		err, key = s.Backend.RestartPlayer(), "playerRestarted"
	case "regeneration_on", "regeneration_off":
		on := r.PostFormValue("do") == "regeneration_on"
		s.Backend.SetRegeneration(on, "webgui")
		key = "regenerationOff"
		if on {
			key = "regenerationOn"
		}
	case "reboot":
		if r.PostFormValue("confirm") != "yes" {
			err = errors.New(s.t(s.lang(r), "pairing.confirmRequired"))
		} else {
			err, key = s.Backend.Reboot(), "rebooting"
		}
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		p := s.authedPage(r, sess, "actions")
		p.Data = s.actionsData(r)
		p.Error = s.t(s.lang(r), "actions.failed", "error", err.Error())
		s.render(w, r, http.StatusUnprocessableEntity, "actions", p)
		return
	}
	slog.Info("webgui action", "action", r.PostFormValue("do"), "remote", clientIP(r))
	flash(w, r, "/actions", key)
}

// ---------------------------------------------------------------- logs

type logFile struct {
	Index int
	Name  string
	Size  int64
}

type logsData struct {
	Lines string
	Files []logFile
}

// tailLines returns the last n lines over the log files (newest first in files).
func tailLines(files []string, n int) []string {
	var out []string
	for _, f := range files {
		lines := readTail(f, 512<<10)
		out = append(lines, out...)
		if len(out) >= n {
			break
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

func readTail(path string, maxBytes int64) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	off := st.Size() - maxBytes
	if off < 0 {
		off = 0
	}
	b, err := io.ReadAll(io.NewSectionReader(f, off, st.Size()-off))
	if err != nil {
		return nil
	}
	text := string(b)
	if off > 0 {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func (s *Server) logsPage(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	p := s.authedPage(r, sess, "logs")
	files := s.Backend.LogFiles()
	d := logsData{Lines: strings.Join(tailLines(files, 500), "\n")}
	for i, f := range files {
		if st, err := os.Stat(f); err == nil {
			d.Files = append(d.Files, logFile{Index: i, Name: filepath.Base(f), Size: st.Size()})
		}
	}
	p.Data = d
	s.render(w, r, http.StatusOK, "logs", p)
}

func (s *Server) logsDownload(w http.ResponseWriter, r *http.Request, _ string, _ *session) {
	files := s.Backend.LogFiles()
	i, err := strconv.Atoi(r.URL.Query().Get("f"))
	if err != nil || i < 0 || i >= len(files) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(files[i])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(files[i])))
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// ---------------------------------------------------------------- update

func (s *Server) updateView(r *http.Request) UpdateView {
	v := s.Backend.UpdateView(r.Context())
	if v.Phase == "" {
		v.Phase = "idle"
	}
	return v
}

func (s *Server) updatePage(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	p := s.authedPage(r, sess, "update")
	p.Data = s.updateView(r)
	s.render(w, r, http.StatusOK, "update", p)
}

func (s *Server) updatePost(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	if r.PostFormValue("do") != "github" {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err := s.Backend.StartGitHubUpdate(); err != nil {
		p := s.authedPage(r, sess, "update")
		p.Data = s.updateView(r)
		p.Error = s.t(s.lang(r), "actions.failed", "error", err.Error())
		s.render(w, r, http.StatusUnprocessableEntity, "update", p)
		return
	}
	slog.Info("GitHub fallback update started from the client WebGUI", "remote", clientIP(r))
	flash(w, r, "/update", "updateStarted")
}
