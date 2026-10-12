package link

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"opensignage/internal/agent/state"
	"opensignage/internal/agent/wire"
	"opensignage/internal/common/tlsutil"
)

// fakeHandler records link callbacks.
type fakeHandler struct {
	mu        sync.Mutex
	creds     state.Server
	manual    string
	states    []State
	paired    chan struct{}
	connected chan struct{}
	revoked   atomic.Int32
	messages  []wire.Envelope
}

func newHandler(manual string) *fakeHandler {
	return &fakeHandler{manual: manual, paired: make(chan struct{}, 1), connected: make(chan struct{}, 4)}
}

func (h *fakeHandler) Credentials() state.Server {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.creds
}
func (h *fakeHandler) ManualAddress() string { return h.manual }
func (h *fakeHandler) PairRequest() wire.PairRequest {
	return wire.PairRequest{HardwareID: "hw-1", Hostname: "player", Version: "1.0.0"}
}
func (h *fakeHandler) Hello() wire.Hello {
	return wire.Hello{Version: "1.0.0", Protocol: 1, HardwareID: "hw-1"}
}
func (h *fakeHandler) Status() wire.Status { return wire.Status{Version: "1.0.0"} }
func (h *fakeHandler) OnPaired(addr, fp, deviceID, token, serverID string) error {
	h.mu.Lock()
	h.creds = state.Server{Address: addr, Fingerprint: fp, DeviceID: deviceID, Token: token}
	h.mu.Unlock()
	h.paired <- struct{}{}
	return nil
}
func (h *fakeHandler) OnRevoked() {
	h.revoked.Add(1)
	h.mu.Lock()
	h.creds = state.Server{}
	h.mu.Unlock()
}
func (h *fakeHandler) OnAddressChanged(string) {}
func (h *fakeHandler) OnState(s State) {
	h.mu.Lock()
	h.states = append(h.states, s)
	h.mu.Unlock()
}
func (h *fakeHandler) OnConnected() { h.connected <- struct{}{} }
func (h *fakeHandler) OnMessage(env wire.Envelope) {
	h.mu.Lock()
	h.messages = append(h.messages, env)
	h.mu.Unlock()
}
func (h *fakeHandler) OnDisconnected() {}

func (h *fakeHandler) lastState() State {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.states) == 0 {
		return State{}
	}
	return h.states[len(h.states)-1]
}

// fakeServer implements the device channel endpoints the link uses.
type fakeServer struct {
	srv       *httptest.Server
	fp        string
	polls     atomic.Int32
	hellos    chan wire.Hello
	revokeWS  atomic.Bool
	tokenSeen atomic.Value
}

func newFakeServer(t *testing.T) *fakeServer {
	pair, err := tlsutil.EnsureSelfSigned(t.TempDir(), "device", "test server", 1)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{fp: tlsutil.SPKIFingerprint(pair.Cert), hellos: make(chan wire.Hello, 4)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /device/v1/pair/request", func(w http.ResponseWriter, r *http.Request) {
		var req wire.PairRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.HardwareID != "hw-1" {
			http.Error(w, "bad", 400)
			return
		}
		json.NewEncoder(w).Encode(wire.PairResponse{PairingID: "p1", PIN: "123456", Secret: "s3cret",
			ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	})
	mux.HandleFunc("GET /device/v1/pair/status", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("secret") != "s3cret" {
			http.Error(w, "bad", 403)
			return
		}
		if f.polls.Add(1) < 2 {
			json.NewEncoder(w).Encode(wire.PairStatus{State: wire.PairPending})
			return
		}
		json.NewEncoder(w).Encode(wire.PairStatus{State: wire.PairClaimed, DeviceID: "dev-1", Token: "tok-1"})
	})
	mux.HandleFunc("GET /device/v1/ws", func(w http.ResponseWriter, r *http.Request) {
		f.tokenSeen.Store(r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			http.Error(w, `{"error":{"code":"revoked","message":"revoked"}}`, http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		var env wire.Envelope
		json.Unmarshal(data, &env)
		var hello wire.Hello
		json.Unmarshal(env.P, &hello)
		if env.T == wire.THello {
			f.hellos <- hello
		}
		msg, _ := wire.Marshal(wire.TWelcome, "", wire.Welcome{DeviceID: "dev-1", ServerVersion: "1.0.0", Protocol: 1,
			ServerTime: time.Now().UTC().Format(time.RFC3339), VersionMatch: true})
		c.Write(r.Context(), websocket.MessageText, msg)
		if f.revokeWS.Load() {
			c.Close(websocket.StatusCode(wire.CloseRevoked), "revoked")
			return
		}
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	})
	f.srv = httptest.NewUnstartedServer(mux)
	f.srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair.TLS}}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) addr() string { return strings.TrimPrefix(f.srv.URL, "https://") }

func run(t *testing.T, l *Link) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	return func() { cancel(); <-done }
}

func fastLink(h Handler) *Link {
	l := New(h, 0)
	l.PollInterval, l.SearchInterval, l.MaxBackoff = 20*time.Millisecond, 20*time.Millisecond, 50*time.Millisecond
	return l
}

func TestPairingPinsCertificateAndConnects(t *testing.T) {
	srv := newFakeServer(t)
	h := newHandler(srv.addr())
	l := fastLink(h)
	stop := run(t, l)
	defer stop()
	select {
	case <-h.paired:
	case <-time.After(10 * time.Second):
		t.Fatalf("not paired, state %+v", h.lastState())
	}
	if c := h.Credentials(); c.Fingerprint != srv.fp || c.Token != "tok-1" || c.Address != srv.addr() {
		t.Fatalf("credentials %+v", c)
	}
	var sawPIN bool
	h.mu.Lock()
	for _, s := range h.states {
		if s.Phase == PhasePairing && s.Pairing != nil && s.Pairing.PIN == "123456" && s.Pairing.Fingerprint == srv.fp {
			sawPIN = true
		}
	}
	h.mu.Unlock()
	if !sawPIN {
		t.Fatal("PIN with fingerprint was never shown")
	}
	select {
	case hello := <-srv.hellos:
		if hello.HardwareID != "hw-1" {
			t.Fatalf("hello %+v", hello)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no hello")
	}
	select {
	case <-h.connected:
	case <-time.After(10 * time.Second):
		t.Fatal("no welcome")
	}
	if !l.Connected() {
		t.Fatal("link must report connected")
	}
	if !l.Send(wire.TStatus, "", wire.Status{}) {
		t.Fatal("send must work while connected")
	}
}

func TestChangedCertificateIsRefused(t *testing.T) {
	srv := newFakeServer(t)
	other, _ := tlsutil.EnsureSelfSigned(t.TempDir(), "other", "other", 1)
	h := newHandler(srv.addr())
	h.creds = state.Server{Address: srv.addr(), Fingerprint: tlsutil.SPKIFingerprint(other.Cert), DeviceID: "dev-1", Token: "tok-1"}
	l := fastLink(h)
	stop := run(t, l)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && h.lastState().Error != ErrCertificateChanged {
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	st := h.lastState()
	if st.Error != ErrCertificateChanged || st.ServerFingerprint != srv.fp {
		t.Fatalf("state %+v", st)
	}
	if h.revoked.Load() != 0 || h.Credentials().Token != "tok-1" {
		t.Fatal("a changed certificate must never wipe or re-pair")
	}
	if srv.tokenSeen.Load() != nil {
		t.Fatal("the token must never be sent to a server with a different certificate")
	}
	// HTTP requests are refused as well.
	if _, err := l.Get(context.Background(), "/device/v1/deployments/x", nil); err == nil {
		t.Fatal("pinned GET must fail")
	}
}

func TestRevokedReturnsToPairing(t *testing.T) {
	srv := newFakeServer(t)
	srv.revokeWS.Store(true)
	h := newHandler(srv.addr())
	h.creds = state.Server{Address: srv.addr(), Fingerprint: srv.fp, DeviceID: "dev-1", Token: "tok-1"}
	l := fastLink(h)
	stop := run(t, l)
	defer stop()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && h.revoked.Load() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if h.revoked.Load() == 0 {
		t.Fatal("close code 4401 must revoke")
	}
	// After the wipe the link requests a new PIN.
	for time.Now().Before(deadline) && h.lastState().Phase != PhasePairing {
		time.Sleep(20 * time.Millisecond)
	}
	if h.lastState().Phase != PhasePairing {
		t.Fatalf("expected pairing, got %+v", h.lastState())
	}
}

func TestUnauthorizedTokenRevokes(t *testing.T) {
	srv := newFakeServer(t)
	h := newHandler(srv.addr())
	h.creds = state.Server{Address: srv.addr(), Fingerprint: srv.fp, DeviceID: "dev-1", Token: "wrong"}
	l := fastLink(h)
	stop := run(t, l)
	defer stop()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && h.revoked.Load() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if h.revoked.Load() == 0 {
		t.Fatal("HTTP 401 on the device link must revoke")
	}
}

func TestResolveRefusesForeignHosts(t *testing.T) {
	l := New(newHandler(""), 0)
	if _, err := l.resolve("srv:7443", "https://evil.example/x"); err == nil {
		t.Fatal("foreign host must be refused")
	}
	if u, err := l.resolve("srv:7443", "/device/v1/blobs/abc"); err != nil || u != "https://srv:7443/device/v1/blobs/abc" {
		t.Fatalf("%s %v", u, err)
	}
	if !fingerprintMatches("", "abc") || !fingerprintMatches(strings.Repeat("ab", 32), strings.Repeat("AB", 32)) ||
		!fingerprintMatches("ABCD-1234", "abcd1234ffff") || fingerprintMatches("ABCD-0000", "abcd1234ffff") {
		t.Fatal("fingerprintMatches wrong")
	}
}
