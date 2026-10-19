package player

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"sync"

	"opensignage/internal/agent/wire"
)

// Snapshot is the complete state the player needs. The hub sends only the parts that
// changed, in a fixed order (mode, config, bundle, source, live, versionNotice,
// networkNotice), so a
// new bundle always arrives before a source that refers to it.
type Snapshot struct {
	Mode          wire.PlayerMode
	Config        wire.PlayerConfig
	Bundle        wire.PlayerBundle
	Source        wire.PlayerSource
	Live          wire.PlayerLive
	VersionNotice wire.VersionNotice
	NetworkNotice wire.NetworkNotice
}

var order = []string{wire.TMode, wire.TConfig, wire.TBundle, wire.TSource, wire.TLive, wire.TVersionNotice, wire.TNetworkNotice}

// Hub fans the snapshot out to connected players.
type Hub struct {
	mu         sync.Mutex
	last       map[string][]byte
	bundleHash *string
	clients    map[*client]struct{}
}

type client struct {
	send chan []byte
	drop func()
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{last: map[string][]byte{}, clients: map[*client]struct{}{}}
}

// Set updates the snapshot and broadcasts changed parts.
func (h *Hub) Set(s Snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	parts := map[string]any{
		wire.TMode: s.Mode, wire.TConfig: s.Config, wire.TSource: s.Source,
		wire.TLive: s.Live, wire.TVersionNotice: s.VersionNotice, wire.TNetworkNotice: s.NetworkNotice,
	}
	// The bundle can be megabytes: re-encode only when its hash changed.
	if h.bundleHash == nil || *h.bundleHash != s.Bundle.Hash {
		b := s.Bundle
		if len(b.Manifest) == 0 {
			b.Manifest = json.RawMessage("null")
		}
		parts[wire.TBundle] = b
		hash := s.Bundle.Hash
		h.bundleHash = &hash
	}
	for _, t := range order {
		p, ok := parts[t]
		if !ok {
			continue
		}
		msg, err := wire.Marshal(t, "", p)
		if err != nil {
			slog.Error("encode player message failed", "type", t, "err", err)
			continue
		}
		if bytes.Equal(h.last[t], msg) {
			continue
		}
		h.last[t] = msg
		h.broadcastLocked(msg)
	}
}

// Cmd sends a one-shot command (not part of the snapshot).
func (h *Hub) Cmd(name string) {
	msg, err := wire.Marshal(wire.TCmd, "", wire.PlayerCmd{Name: name})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.broadcastLocked(msg)
}

// Clients returns the number of connected players.
func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// Last returns the last encoded message of a type (tests, diagnostics).
func (h *Hub) Last(t string) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last[t]
}

func (h *Hub) broadcastLocked(msg []byte) {
	for c := range h.clients {
		select {
		case c.send <- msg:
		default:
			slog.Warn("player connection too slow, dropping it")
			delete(h.clients, c)
			c.drop()
		}
	}
}

// add registers a client and queues the full snapshot for it.
func (h *Hub) add(c *client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) >= maxClients {
		return false
	}
	h.clients[c] = struct{}{}
	for _, t := range order {
		if msg := h.last[t]; msg != nil {
			c.send <- msg
		}
	}
	return true
}

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}
