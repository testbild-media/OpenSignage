// Package link is the agent's connection to the server: discovery or manual address,
// PIN pairing with trust-on-first-use pinning of the server's SPKI fingerprint, and the
// device WebSocket (ARCHITECTURE §3 Pairing, §5.7, §7.1). After pairing every request
// uses tlsutil.PinnedConfig with the stored fingerprint: a changed certificate is
// refused and reported, never silently re-paired.
package link

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"opensignage/internal/agent/safe"
	"opensignage/internal/agent/state"
	"opensignage/internal/agent/wire"
	"opensignage/internal/common/discovery"
	"opensignage/internal/common/tlsutil"
)

// Phases of the link.
const (
	PhaseSearching  = "searching"  // no server known yet (discovery running)
	PhasePairing    = "pairing"    // PIN shown, waiting for the admin to claim
	PhaseConnecting = "connecting" // paired, connecting the device WebSocket
	PhaseConnected  = "connected"  // device WebSocket up (welcome received)
	PhaseError      = "error"      // see State.Error
)

// Error codes shown in the client WebGUI and on the player.
const (
	ErrCertificateChanged  = "certificate_changed"
	ErrFingerprintMismatch = "fingerprint_mismatch"
	ErrServerUnreachable   = "server_unreachable"
	ErrPairingRefused      = "pairing_refused"
	ErrDisconnected        = "disconnected"
)

// PairingInfo is what the PIN screen shows.
type PairingInfo struct {
	Address     string
	Fingerprint string // full SPKI SHA-256 (hex)
	PIN         string
	ExpiresAt   time.Time
}

// State describes the link for the UI.
type State struct {
	Phase   string
	Address string
	Error   string
	Detail  string
	Since   time.Time
	Pairing *PairingInfo
	// ServerFingerprint is the fingerprint presented by the server when it differs
	// from the pinned one (certificate_changed).
	ServerFingerprint string
}

// Handler connects the link to the agent.
type Handler interface {
	Credentials() state.Server
	ManualAddress() string
	PairRequest() wire.PairRequest
	Hello() wire.Hello
	Status() wire.Status
	// OnPaired stores new credentials after wiping the previous server's data.
	OnPaired(address, fingerprint, deviceID, token, serverID string) error
	// OnRevoked wipes the credentials and server data (device deleted or re-paired).
	OnRevoked()
	// OnAddressChanged stores a new address found by discovery for the pinned server.
	OnAddressChanged(address string)
	OnState(State)
	// OnConnected runs after the welcome message (send pending reports here).
	OnConnected()
	OnMessage(env wire.Envelope)
	OnDisconnected()
}

// Link manages the server connection.
type Link struct {
	h             Handler
	DiscoveryPort int
	// Intervals (defaults: search 3 s, pairing poll 2 s, status 30 s).
	SearchInterval time.Duration
	PollInterval   time.Duration
	StatusInterval time.Duration
	PingInterval   time.Duration
	MaxBackoff     time.Duration

	mu      sync.Mutex
	st      State
	send    chan []byte
	cancel  context.CancelFunc
	clients map[string]*http.Client
}

// New creates a link.
func New(h Handler, discoveryPort int) *Link {
	return &Link{h: h, DiscoveryPort: discoveryPort, SearchInterval: 3 * time.Second, PollInterval: 2 * time.Second,
		StatusInterval: 30 * time.Second, PingInterval: 30 * time.Second, MaxBackoff: 30 * time.Second,
		clients: map[string]*http.Client{}}
}

// State returns the current link state.
func (l *Link) State() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.st
}

func (l *Link) setState(s State) {
	l.mu.Lock()
	if s.Phase != l.st.Phase || s.Error != l.st.Error || s.Address != l.st.Address {
		s.Since = time.Now()
	} else {
		s.Since = l.st.Since
	}
	l.st = s
	l.mu.Unlock()
	l.h.OnState(s)
}

// Reset aborts the current phase (server address changed, re-pair requested); Run
// then starts over with the stored credentials.
func (l *Link) Reset() {
	l.mu.Lock()
	if l.cancel != nil {
		l.cancel()
	}
	l.mu.Unlock()
}

// Run drives the link until ctx ends.
func (l *Link) Run(ctx context.Context) {
	for ctx.Err() == nil {
		pctx, cancel := context.WithCancel(ctx)
		l.mu.Lock()
		l.cancel = cancel
		l.mu.Unlock()
		func() {
			defer safe.Recover("link")
			creds := l.h.Credentials()
			if creds.Paired() {
				l.connectLoop(pctx, creds)
			} else {
				l.pairLoop(pctx)
			}
		}()
		cancel()
		if ctx.Err() == nil {
			sleep(ctx, 100*time.Millisecond) // a panic must not become a hot loop
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func jitter(d time.Duration) time.Duration {
	return d + time.Duration(rand.Int64N(int64(d)/5+1))
}

// ---------------------------------------------------------------- HTTP

// client returns an HTTP client accepting exactly fingerprint fp ("" = TOFU, then
// seen receives the presented fingerprint). Pinned clients are cached.
func (l *Link) client(fp string, seen func(string)) *http.Client {
	if fp != "" && seen == nil {
		l.mu.Lock()
		defer l.mu.Unlock()
		if c := l.clients[fp]; c != nil {
			return c
		}
		for k, c := range l.clients {
			c.CloseIdleConnections()
			delete(l.clients, k)
		}
		c := newClient(tlsutil.PinnedConfig(fp, nil))
		l.clients[fp] = c
		return c
	}
	return newClient(tlsutil.PinnedConfig(fp, seen))
}

func newClient(cfg *tls.Config) *http.Client {
	tr := &http.Transport{
		Proxy:                 nil, // clients only talk to their server, never via a proxy
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   4,
		TLSClientConfig:       cfg,
	}
	return &http.Client{Transport: tr}
}

// IsPinMismatch reports whether err was caused by a changed server certificate.
func IsPinMismatch(err error) (*tlsutil.PinMismatchError, bool) {
	var pm *tlsutil.PinMismatchError
	if errors.As(err, &pm) {
		return pm, true
	}
	return nil, false
}

func (l *Link) resolve(addr, target string) (string, error) {
	if strings.HasPrefix(target, "/") {
		return "https://" + addr + target, nil
	}
	u, err := url.Parse(target)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" || u.Host != addr {
		return "", fmt.Errorf("refusing URL outside the paired server: %s", target)
	}
	return u.String(), nil
}

// Do performs an authenticated request against the paired server.
func (l *Link) Do(ctx context.Context, method, target string, hdr http.Header, body io.Reader) (*http.Response, error) {
	creds := l.h.Credentials()
	if !creds.Paired() {
		return nil, errors.New("not paired")
	}
	u, err := l.resolve(creds.Address, target)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	resp, err := l.client(creds.Fingerprint, nil).Do(req)
	if err != nil {
		if pm, ok := IsPinMismatch(err); ok {
			l.certificateChanged(creds.Address, pm)
		}
		return nil, err
	}
	return resp, nil
}

// Get implements syncer.Client.
func (l *Link) Get(ctx context.Context, target string, hdr http.Header) (*http.Response, error) {
	return l.Do(ctx, http.MethodGet, target, hdr, nil)
}

func (l *Link) certificateChanged(addr string, pm *tlsutil.PinMismatchError) {
	slog.Error("server certificate changed – refusing connection (re-pair in the client WebGUI if this is expected)",
		"address", addr, "pinned", tlsutil.ShortFingerprint(pm.Expected), "presented", tlsutil.ShortFingerprint(pm.Got))
	l.setState(State{Phase: PhaseError, Address: addr, Error: ErrCertificateChanged, ServerFingerprint: pm.Got,
		Detail: pm.Error()})
}

func readError(resp *http.Response) string {
	var eb wire.ErrorBody
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if json.Unmarshal(b, &eb) == nil && eb.Error.Code != "" {
		return eb.Error.Code
	}
	return "http_" + strconv.Itoa(resp.StatusCode)
}

// ---------------------------------------------------------------- pairing

func (l *Link) pairLoop(ctx context.Context) {
	backoff := l.SearchInterval
	for ctx.Err() == nil {
		addr, expectFP, serverID := l.h.ManualAddress(), "", ""
		if addr == "" {
			st := l.State()
			if st.Phase != PhaseSearching || st.Error != "" {
				l.setState(State{Phase: PhaseSearching})
			}
			start := time.Now()
			infos, err := discovery.Search(ctx, l.DiscoveryPort, 1500*time.Millisecond)
			if err != nil {
				slog.Debug("discovery failed", "err", err)
			}
			if len(infos) == 0 {
				sleep(ctx, max(l.SearchInterval-time.Since(start), 100*time.Millisecond))
				continue
			}
			sort.Slice(infos, func(i, j int) bool { return infos[i].ServerID < infos[j].ServerID })
			if len(infos) > 1 {
				slog.Warn("several servers answered discovery, using the first", "server", infos[0].ServerID, "count", len(infos))
			}
			in := infos[0]
			port := in.DevicePort
			if port == 0 {
				port = 7443
			}
			addr, expectFP, serverID = net.JoinHostPort(in.Source, strconv.Itoa(port)), in.Fingerprint, in.ServerID
			slog.Info("server discovered", "address", addr, "name", in.Name, "version", in.Version)
		}
		err := l.pairWith(ctx, addr, expectFP, serverID)
		if err == nil || ctx.Err() != nil {
			return
		}
		code := ErrServerUnreachable
		var pe *pairError
		if errors.As(err, &pe) {
			code = pe.code
		}
		l.setState(State{Phase: PhaseError, Address: addr, Error: code, Detail: err.Error()})
		slog.Warn("pairing attempt failed", "address", addr, "err", err, "retryIn", backoff.String())
		sleep(ctx, jitter(backoff))
		backoff = min(backoff*2, l.MaxBackoff)
	}
}

type pairError struct{ code, detail string }

func (e *pairError) Error() string { return e.code + ": " + e.detail }

func fingerprintMatches(announced, seen string) bool {
	if announced == "" {
		return true
	}
	if len(announced) == 64 {
		return strings.EqualFold(announced, seen)
	}
	return strings.EqualFold(strings.ReplaceAll(announced, "-", ""), strings.ReplaceAll(tlsutil.ShortFingerprint(seen), "-", ""))
}

// request sends POST /device/v1/pair/request. With fp == "" the certificate is
// accepted and its fingerprint returned (trust on first use).
func (l *Link) request(ctx context.Context, addr, fp string) (wire.PairResponse, string, error) {
	var seen string
	var c *http.Client
	if fp == "" {
		c = l.client("", func(s string) { seen = s })
	} else {
		c = l.client(fp, nil)
		seen = fp
	}
	body, _ := json.Marshal(l.h.PairRequest())
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, "https://"+addr+"/device/v1/pair/request", bytes.NewReader(body))
	if err != nil {
		return wire.PairResponse{}, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		if _, ok := IsPinMismatch(err); ok {
			return wire.PairResponse{}, "", &pairError{ErrCertificateChanged, err.Error()}
		}
		return wire.PairResponse{}, "", &pairError{ErrServerUnreachable, err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return wire.PairResponse{}, "", &pairError{ErrPairingRefused, readError(resp)}
	}
	var pr wire.PairResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&pr); err != nil || pr.PIN == "" || pr.PairingID == "" {
		return wire.PairResponse{}, "", &pairError{ErrPairingRefused, "invalid pairing response"}
	}
	return pr, seen, nil
}

func (l *Link) pairWith(ctx context.Context, addr, expectFP, serverID string) error {
	pr, fp, err := l.request(ctx, addr, "")
	if err != nil {
		return err
	}
	if !fingerprintMatches(expectFP, fp) {
		return &pairError{ErrFingerprintMismatch, fmt.Sprintf("discovery announced %s, server presented %s",
			expectFP, tlsutil.ShortFingerprint(fp))}
	}
	show := func(pr wire.PairResponse) time.Time {
		exp, err := time.Parse(time.RFC3339, pr.ExpiresAt)
		if err != nil {
			exp = time.Now().Add(10 * time.Minute)
		}
		l.setState(State{Phase: PhasePairing, Address: addr,
			Pairing: &PairingInfo{Address: addr, Fingerprint: fp, PIN: pr.PIN, ExpiresAt: exp}})
		return exp
	}
	expires := show(pr)
	slog.Info("pairing requested, showing PIN", "address", addr, "fingerprint", tlsutil.ShortFingerprint(fp))
	pinned := l.client(fp, nil)
	failures := 0
	for ctx.Err() == nil {
		if !sleep(ctx, l.PollInterval) {
			return ctx.Err()
		}
		if time.Until(expires) < 5*time.Second {
			if pr, _, err = l.request(ctx, addr, fp); err != nil {
				return err
			}
			expires = show(pr)
			continue
		}
		q := url.Values{"id": {pr.PairingID}, "secret": {pr.Secret}}
		rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		req, _ := http.NewRequestWithContext(rctx, http.MethodGet, "https://"+addr+"/device/v1/pair/status?"+q.Encode(), nil)
		resp, err := pinned.Do(req)
		if err != nil {
			cancel()
			if _, ok := IsPinMismatch(err); ok {
				return &pairError{ErrCertificateChanged, err.Error()}
			}
			if failures++; failures >= 5 {
				return &pairError{ErrServerUnreachable, err.Error()}
			}
			continue
		}
		failures = 0
		var ps wire.PairStatus
		decErr := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&ps)
		status := resp.StatusCode
		resp.Body.Close()
		cancel()
		if status == http.StatusNotFound || status == http.StatusGone || (status == http.StatusOK && ps.State == wire.PairExpired) {
			if pr, _, err = l.request(ctx, addr, fp); err != nil {
				return err
			}
			expires = show(pr)
			continue
		}
		if status != http.StatusOK || decErr != nil {
			slog.Warn("pairing status failed", "status", status, "err", decErr)
			continue
		}
		if ps.State != wire.PairClaimed {
			continue
		}
		if ps.Token == "" || ps.DeviceID == "" {
			return &pairError{ErrPairingRefused, "claimed without token"}
		}
		if err := l.h.OnPaired(addr, fp, ps.DeviceID, ps.Token, serverID); err != nil {
			slog.Error("storing pairing failed", "err", err)
			return err
		}
		slog.Info("paired with server", "address", addr, "device", ps.DeviceID)
		return nil
	}
	return ctx.Err()
}

// ---------------------------------------------------------------- device link

var errRevoked = errors.New("device revoked by the server")

func (l *Link) connectLoop(ctx context.Context, creds state.Server) {
	backoff := time.Second
	failures := 0
	for ctx.Err() == nil {
		l.setState(State{Phase: PhaseConnecting, Address: creds.Address})
		welcomed, err := l.session(ctx, creds)
		l.mu.Lock()
		l.send = nil
		l.mu.Unlock()
		if welcomed {
			l.h.OnDisconnected()
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errRevoked) {
			slog.Warn("device revoked by the server, returning to pairing")
			l.h.OnRevoked()
			return
		}
		if pm, ok := IsPinMismatch(err); ok {
			l.certificateChanged(creds.Address, pm)
		} else {
			code := ErrServerUnreachable
			if welcomed {
				code = ErrDisconnected
			}
			slog.Warn("device link down", "address", creds.Address, "err", err)
			l.setState(State{Phase: PhaseError, Address: creds.Address, Error: code, Detail: fmt.Sprint(err)})
		}
		if welcomed {
			backoff, failures = time.Second, 0
		} else {
			failures++
		}
		// The paired server may have a new address (DHCP): look for its fingerprint.
		if failures >= 3 && l.h.ManualAddress() == "" {
			if addr := l.rediscover(ctx, creds.Fingerprint); addr != "" && addr != creds.Address {
				slog.Info("paired server found at a new address", "old", creds.Address, "new", addr)
				l.h.OnAddressChanged(addr)
				creds = l.h.Credentials()
				failures, backoff = 0, time.Second
				continue
			}
		}
		if !sleep(ctx, jitter(backoff)) {
			return
		}
		backoff = min(backoff*2, l.MaxBackoff)
	}
}

func (l *Link) rediscover(ctx context.Context, fp string) string {
	infos, err := discovery.Search(ctx, l.DiscoveryPort, 1500*time.Millisecond)
	if err != nil {
		return ""
	}
	for _, in := range infos {
		if in.Fingerprint != "" && fingerprintMatches(in.Fingerprint, fp) {
			port := in.DevicePort
			if port == 0 {
				port = 7443
			}
			return net.JoinHostPort(in.Source, strconv.Itoa(port))
		}
	}
	return ""
}

// Send queues a message on the device link; false when not connected.
func (l *Link) Send(t, id string, p any) bool {
	msg, err := wire.Marshal(t, id, p)
	if err != nil {
		slog.Error("encode message failed", "type", t, "err", err)
		return false
	}
	l.mu.Lock()
	ch := l.send
	l.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- msg:
		return true
	default:
		slog.Warn("device link send queue full, message dropped", "type", t)
		return false
	}
}

// Connected reports whether the device link is up.
func (l *Link) Connected() bool { return l.State().Phase == PhaseConnected }

func (l *Link) session(ctx context.Context, creds state.Server) (welcomed bool, err error) {
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, resp, err := websocket.Dial(dctx, "wss://"+creds.Address+"/device/v1/ws", &websocket.DialOptions{
		HTTPClient: l.client(creds.Fingerprint, nil),
		HTTPHeader: http.Header{"Authorization": {"Bearer " + creds.Token}},
	})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return false, errRevoked
		}
		return false, err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(8 << 20)
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()

	hello, _ := wire.Marshal(wire.THello, "", l.h.Hello())
	wctx, wcancel := context.WithTimeout(sctx, 10*time.Second)
	err = conn.Write(wctx, websocket.MessageText, hello)
	wcancel()
	if err != nil {
		return false, err
	}
	send := make(chan []byte, 256)
	l.mu.Lock()
	l.send = send
	l.mu.Unlock()

	safe.Go("device-link-writer", func() {
		defer scancel()
		ping := time.NewTicker(l.PingInterval)
		defer ping.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case msg := <-send:
				wctx, wcancel := context.WithTimeout(sctx, 15*time.Second)
				err := conn.Write(wctx, websocket.MessageText, msg)
				wcancel()
				if err != nil {
					slog.Debug("device link write failed", "err", err)
					return
				}
			case <-ping.C:
				pctx, pcancel := context.WithTimeout(sctx, 15*time.Second)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil {
					slog.Warn("device link ping failed", "err", err)
					return
				}
			}
		}
	})

	invalid := 0
	window := time.Now()
	statusStarted := false
	for {
		_, data, err := conn.Read(sctx)
		if err != nil {
			if websocket.CloseStatus(err) == wire.CloseRevoked {
				return welcomed, errRevoked
			}
			if ctx.Err() != nil {
				_ = conn.Close(websocket.StatusGoingAway, "agent stopping")
			}
			return welcomed, err
		}
		var env wire.Envelope
		if err := json.Unmarshal(data, &env); err != nil || env.T == "" {
			if time.Since(window) > time.Minute {
				invalid, window = 0, time.Now()
			}
			invalid++
			slog.Warn("invalid message from server", "count", invalid)
			if invalid > 20 {
				_ = conn.Close(websocket.StatusPolicyViolation, "too many invalid messages")
				return welcomed, errors.New("too many invalid messages")
			}
			continue
		}
		switch env.T {
		case wire.TRevoked:
			return welcomed, errRevoked
		case wire.TWelcome:
			welcomed = true
			l.setState(State{Phase: PhaseConnected, Address: creds.Address})
			l.h.OnMessage(env)
			l.h.OnConnected()
			if !statusStarted {
				statusStarted = true
				safe.Go("device-link-status", func() {
					l.Send(wire.TStatus, "", l.h.Status())
					t := time.NewTicker(l.StatusInterval)
					defer t.Stop()
					for {
						select {
						case <-sctx.Done():
							return
						case <-t.C:
							l.Send(wire.TStatus, "", l.h.Status())
						}
					}
				})
			}
		default:
			l.h.OnMessage(env)
		}
	}
}
