package devices

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"opensignage/internal/contracts"
)

// Snapshot slots: the newest config, live state and deployment target per connection.
// A newer snapshot replaces an unsent older one, so a burst of changes never queues up
// and a slow link always receives the latest complete state.
const (
	slotConfig = iota
	slotLive
	slotDeploy
	numSlots
)

const (
	queueSize      = 64      // bounded write queue for non-snapshot messages (actions, offers)
	maxMessageSize = 1 << 20 // read limit per message
	invalidWindow  = 60 * time.Second
	invalidMax     = 20 // more than this many invalid messages per window → close 1008
	rateBurst      = 100
	ratePerSecond  = 20
)

type slot struct {
	seq   uint64
	data  []byte
	dirty bool
	last  []byte // last offered payload (dedupe of unchanged config/targets)
}

// conn is one device link connection. All fields that change after the handshake are
// guarded by mu or atomic.
type conn struct {
	m          *Module
	ws         *websocket.Conn
	deviceID   string
	hardwareID string
	tokenHash  string
	remote     string

	ctx    context.Context
	cancel context.CancelFunc

	versionMatch bool      // fixed after hello
	connectedAt  time.Time // when the connection was accepted
	registered   bool

	mu     sync.Mutex
	zoneID string
	slots  [numSlots]slot

	queue chan []byte
	wake  chan struct{}

	closing      atomic.Bool // no further writes
	closeStarted atomic.Bool // closeWith ran; closeDone will be closed
	closeOnce    sync.Once
	closeDone    chan struct{}

	lastRead atomic.Int64

	invMu   sync.Mutex
	invalid []time.Time
	tokens  float64
	tokenAt time.Time
	unknown int
}

func newConn(m *Module, ws *websocket.Conn, dev *authDev, remote string) *conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &conn{m: m, ws: ws, deviceID: dev.ID, hardwareID: dev.HardwareID, tokenHash: dev.TokenHash, remote: remote, ctx: ctx, cancel: cancel,
		zoneID: dev.ZoneID, queue: make(chan []byte, queueSize), wake: make(chan struct{}, 1),
		closeDone: make(chan struct{}), tokens: rateBurst, tokenAt: time.Now(), connectedAt: time.Now()}
	c.lastRead.Store(time.Now().UnixNano())
	return c
}

func (c *conn) zone() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.zoneID
}

// setZone updates the zone and reports whether it changed.
func (c *conn) setZone(z string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	changed := c.zoneID != z
	c.zoneID = z
	return changed
}

// offer places a snapshot into its slot unless a newer one (higher seq) is already
// there. With onlyIfChanged an identical payload to the last offered one is skipped.
func (c *conn) offer(kind int, seq uint64, data []byte, onlyIfChanged bool) bool {
	c.mu.Lock()
	s := &c.slots[kind]
	if seq <= s.seq {
		c.mu.Unlock()
		return false
	}
	if onlyIfChanged && bytes.Equal(s.last, data) {
		s.seq = seq
		c.mu.Unlock()
		return false
	}
	s.seq, s.data, s.dirty, s.last = seq, data, true, data
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return true
}

func (c *conn) take(kind int) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := &c.slots[kind]
	if !s.dirty {
		return nil
	}
	s.dirty = false
	d := s.data
	s.data = nil
	return d
}

// enqueue adds a message to the bounded queue. A full queue means the device does not
// keep up: it is disconnected instead of letting memory grow.
func (c *conn) enqueue(b []byte) bool {
	if c.closing.Load() {
		return false
	}
	select {
	case c.queue <- b:
		return true
	default:
		c.m.log.Warn("device link: write queue full, disconnecting slow device", "device", c.deviceID)
		c.closeWith(websocket.StatusTryAgainLater, "write queue overflow", nil)
		return false
	}
}

// closeWith closes the connection once with a close code; preface is written first
// (e.g. the `revoked` message). It never blocks the caller.
func (c *conn) closeWith(code websocket.StatusCode, reason string, preface []byte) {
	c.closeOnce.Do(func() {
		c.closing.Store(true)
		c.closeStarted.Store(true)
		go func() {
			defer close(c.closeDone)
			defer func() {
				if p := recover(); p != nil {
					c.m.log.Error("device link: panic while closing", "device", c.deviceID, "panic", p)
				}
			}()
			if preface != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := c.ws.Write(ctx, websocket.MessageText, preface); err != nil {
					c.m.log.Debug("device link: writing close preface", "device", c.deviceID, "err", err)
				}
				cancel()
			}
			if err := c.ws.Close(code, reason); err != nil {
				c.m.log.Debug("device link: close", "device", c.deviceID, "code", int(code), "err", err)
			}
		}()
	})
}

// revoke sends `revoked` and closes with 4401 (the agent wipes and returns to pairing).
func (c *conn) revoke() {
	b, err := encode(contracts.MsgRevoked, "", struct{}{})
	if err != nil {
		b = nil
	}
	c.closeWith(contracts.CloseRevoked, "revoked", b)
}

// invalidMsg counts an invalid message; more than invalidMax per window closes the
// connection with 1008. Other devices are never affected.
func (c *conn) invalidMsg(reason string) {
	now := time.Now()
	c.invMu.Lock()
	keep := c.invalid[:0]
	for _, t := range c.invalid {
		if now.Sub(t) < invalidWindow {
			keep = append(keep, t)
		}
	}
	c.invalid = append(keep, now)
	if len(c.invalid) > invalidMax+1 {
		c.invalid = c.invalid[len(c.invalid)-(invalidMax+1):]
	}
	n := len(c.invalid)
	c.invMu.Unlock()
	if n == 1 || n%10 == 0 {
		c.m.log.Warn("device link: invalid message", "device", c.deviceID, "reason", reason, "countInWindow", n)
	}
	if n > invalidMax {
		c.m.log.Warn("device link: too many invalid messages, disconnecting", "device", c.deviceID)
		c.closeWith(websocket.StatusPolicyViolation, "too many invalid messages", nil)
	}
}

// allow is a token bucket limiting messages per connection.
func (c *conn) allow(now time.Time) bool {
	c.invMu.Lock()
	defer c.invMu.Unlock()
	c.tokens += now.Sub(c.tokenAt).Seconds() * ratePerSecond
	if c.tokens > rateBurst {
		c.tokens = rateBurst
	}
	c.tokenAt = now
	if c.tokens < 1 {
		return false
	}
	c.tokens--
	return true
}

func (c *conn) logUnknown(t string) {
	c.invMu.Lock()
	c.unknown++
	n := c.unknown
	c.invMu.Unlock()
	if n <= 5 {
		c.m.log.Info("device link: unknown message type ignored", "device", c.deviceID, "type", truncate(t, 64))
	}
}

// ---------------------------------------------------------------- hub

// hub tracks the live connection of every device (at most one per device).
type hub struct {
	mu     sync.RWMutex
	conns  map[string]*conn
	closed bool
}

func newHub() *hub { return &hub{conns: map[string]*conn{}} }

// register makes c the device's connection and returns the replaced one. ok is false
// when the hub is shut down.
func (h *hub) register(c *conn) (old *conn, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, false
	}
	old = h.conns[c.deviceID]
	h.conns[c.deviceID] = c
	c.registered = true
	return old, true
}

// unregister removes c if it is still the device's current connection.
func (h *hub) unregister(c *conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns[c.deviceID] == c {
		delete(h.conns, c.deviceID)
		return true
	}
	return false
}

func (h *hub) get(id string) *conn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[id]
}

func (h *hub) snapshot() []*conn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*conn, 0, len(h.conns))
	for _, c := range h.conns {
		out = append(out, c)
	}
	return out
}

func (h *hub) count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// closeAll closes every connection (graceful shutdown) and waits up to wait.
func (h *hub) closeAll(code websocket.StatusCode, reason string, wait time.Duration) {
	h.mu.Lock()
	h.closed = true
	conns := make([]*conn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.closeWith(code, reason, nil)
	}
	deadline := time.Now().Add(wait)
	for h.count() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := h.count(); n > 0 {
		slog.Warn("device link: connections still open after shutdown wait", "count", n)
	}
}

// reopen allows registrations again (Run restarted).
func (h *hub) reopen() {
	h.mu.Lock()
	h.closed = false
	h.mu.Unlock()
}

// encode builds a link message.
func encode(t, id string, p any) ([]byte, error) {
	pb, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return json.Marshal(contracts.LinkMessage{T: t, ID: id, P: pb})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
