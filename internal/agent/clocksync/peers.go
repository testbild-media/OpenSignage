package clocksync

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sort"
	"sync"
	"time"
)

// DefaultPort is the UDP port of the LAN time exchange.
const DefaultPort = 7445

const (
	beaconEvery = 5 * time.Second
	followEvery = 10 * time.Second
	peerTimeout = 20 * time.Second
	burstSize   = 3
	burstWait   = time.Second
	// OfflineAfter: without a server measurement for this long the agent follows the LAN.
	OfflineAfter = 3 * time.Minute
	maxPacket    = 1024
	macLen       = 16
	// A leader answer with a longer round trip is not used (Wi-Fi hiccup).
	maxRTT = 250 * time.Millisecond
)

// Source of the agent's clock correction (status reports).
const (
	SourceServer = "server"
	SourcePeer   = "peer"
	SourceLocal  = "local"
)

// Self describes this agent to the LAN time exchange; every field is read when needed.
type Self struct {
	DeviceID func() string
	// Zone returns the zone and its clock key (hex); "" disables the exchange.
	Zone func() (zone, keyHex string)
	// Now is the corrected clock (local time plus the current offset).
	Now func() time.Time
	// ServerAge is the time since the last server measurement (large: never).
	ServerAge func() time.Duration
	// Apply sets a new offset measured against the LAN leader.
	Apply func(Sample, string)
}

type packet struct {
	K      string `json:"k"` // beacon | req | rep
	Zone   string `json:"z"`
	Dev    string `json:"d"`
	Online bool   `json:"o,omitempty"` // beacon: has the server time
	Seq    uint64 `json:"s,omitempty"`
	Nonce  uint64 `json:"n,omitempty"`
	T      int64  `json:"t,omitempty"` // rep: leader's corrected time (unix µs)
}

type peer struct {
	addr   *net.UDPAddr
	online bool
	seen   time.Time
}

// Peers runs the LAN time exchange.
type Peers struct {
	Port int
	Self Self

	mu      sync.Mutex
	peers   map[string]peer
	burst   *Burst
	nonce   uint64
	seq     uint64
	leader  string
	conn    *net.UDPConn
	lastErr string
}

// Leader returns the device id the agent follows ("" = none: own or server time).
func (p *Peers) Leader() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.leader
}

// Run listens and sends until ctx ends.
func (p *Peers) Run(ctx context.Context) error {
	port := p.Port
	if port == 0 {
		port = DefaultPort
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.conn = conn
	p.peers = map[string]peer{}
	p.mu.Unlock()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	go p.sendLoop(ctx, port)
	buf := make([]byte, maxPacket)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			continue
		}
		p.handle(buf[:n], from, time.Now())
	}
}

func (p *Peers) key() (zone string, key []byte) {
	z, k := p.Self.Zone()
	if z == "" || k == "" {
		return "", nil
	}
	b, err := hex.DecodeString(k)
	if err != nil || len(b) < 16 {
		return "", nil
	}
	return z, b
}

func seal(key []byte, pk packet) ([]byte, error) {
	body, err := json.Marshal(pk)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, key)
	m.Write(body)
	return append(m.Sum(nil)[:macLen], body...), nil
}

func open(key, data []byte) (packet, bool) {
	var pk packet
	if len(data) <= macLen {
		return pk, false
	}
	m := hmac.New(sha256.New, key)
	m.Write(data[macLen:])
	if !hmac.Equal(m.Sum(nil)[:macLen], data[:macLen]) {
		return pk, false
	}
	return pk, json.Unmarshal(data[macLen:], &pk) == nil
}

func (p *Peers) handle(data []byte, from *net.UDPAddr, at time.Time) {
	zone, key := p.key()
	if key == nil {
		return
	}
	pk, ok := open(key, data)
	if !ok || pk.Zone != zone {
		return
	}
	self := p.Self.DeviceID()
	if pk.Dev == self {
		return // our own broadcast
	}
	switch pk.K {
	case "beacon":
		p.mu.Lock()
		p.peers[pk.Dev] = peer{addr: &net.UDPAddr{IP: from.IP, Port: from.Port}, online: pk.Online, seen: at}
		p.mu.Unlock()
	case "req":
		rep := packet{K: "rep", Zone: zone, Dev: self, Seq: pk.Seq, Nonce: pk.Nonce, T: p.Self.Now().UnixMicro()}
		if b, err := seal(key, rep); err == nil {
			p.write(b, from)
		}
	case "rep":
		p.mu.Lock()
		if p.burst != nil && pk.Nonce == p.nonce && pk.Dev == p.leader {
			p.burst.Answer(pk.Seq, time.UnixMicro(pk.T), at)
		}
		p.mu.Unlock()
	}
}

func (p *Peers) write(b []byte, to *net.UDPAddr) {
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn == nil {
		return
	}
	if _, err := conn.WriteToUDP(b, to); err != nil {
		p.mu.Lock()
		if p.lastErr != err.Error() {
			p.lastErr = err.Error()
			slog.Debug("clock exchange: send failed", "to", to.String(), "err", err)
		}
		p.mu.Unlock()
	}
}

func (p *Peers) sendLoop(ctx context.Context, port int) {
	beacon := time.NewTicker(beaconEvery)
	defer beacon.Stop()
	follow := time.NewTicker(followEvery)
	defer follow.Stop()
	p.sendBeacon(port)
	for {
		select {
		case <-ctx.Done():
			return
		case <-beacon.C:
			p.sendBeacon(port)
		case <-follow.C:
			p.follow(ctx)
		}
	}
}

// online: this agent has a recent server measurement.
func (p *Peers) online() bool { return p.Self.ServerAge() < OfflineAfter }

func (p *Peers) sendBeacon(port int) {
	zone, key := p.key()
	if key == nil {
		return
	}
	b, err := seal(key, packet{K: "beacon", Zone: zone, Dev: p.Self.DeviceID(), Online: p.online()})
	if err != nil {
		return
	}
	for _, addr := range broadcastAddrs(port) {
		p.write(b, addr)
	}
}

// chooseLeader picks the time leader among the live peers and this agent: devices with
// the server time first, then the lowest device id. "" = this agent itself.
func chooseLeader(self string, selfOnline bool, peers map[string]peer, now time.Time) string {
	type cand struct {
		id     string
		online bool
	}
	list := []cand{{self, selfOnline}}
	for id, pe := range peers {
		if now.Sub(pe.seen) <= peerTimeout {
			list = append(list, cand{id, pe.online})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].online != list[j].online {
			return list[i].online
		}
		return list[i].id < list[j].id
	})
	if list[0].id == self {
		return ""
	}
	return list[0].id
}

// follow measures against the leader when the server is out of reach.
func (p *Peers) follow(ctx context.Context) {
	zone, key := p.key()
	if key == nil || p.online() {
		p.setLeader("")
		return
	}
	self := p.Self.DeviceID()
	p.mu.Lock()
	leader := chooseLeader(self, false, p.peers, time.Now())
	pe, ok := p.peers[leader]
	p.mu.Unlock()
	p.setLeader(leader)
	if leader == "" || !ok {
		return // this agent leads (or is alone): its clock keeps running
	}
	var nb [8]byte
	_, _ = rand.Read(nb[:])
	p.mu.Lock()
	p.nonce = binary.LittleEndian.Uint64(nb[:])
	p.burst = NewBurst()
	nonce, burst := p.nonce, p.burst
	p.mu.Unlock()
	for i := 0; i < burstSize; i++ {
		p.mu.Lock()
		p.seq++
		seq := p.seq
		burst.Sent(seq, time.Now())
		p.mu.Unlock()
		if b, err := seal(key, packet{K: "req", Zone: zone, Dev: self, Seq: seq, Nonce: nonce}); err == nil {
			p.write(b, pe.addr)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(burstWait):
	}
	p.mu.Lock()
	s, ok := burst.Result()
	p.burst = nil
	p.mu.Unlock()
	if ok && s.RTT <= maxRTT {
		p.Self.Apply(s, leader)
	}
}

func (p *Peers) setLeader(id string) {
	p.mu.Lock()
	changed := p.leader != id
	p.leader = id
	p.mu.Unlock()
	if changed {
		if id == "" {
			slog.Info("clock exchange: no LAN leader to follow")
		} else {
			slog.Info("clock exchange: following LAN time leader", "device", id)
		}
	}
}

// broadcastAddrs are the limited broadcast and the directed broadcast of every IPv4
// interface (some networks drop one of them).
func broadcastAddrs(port int) []*net.UDPAddr {
	out := []*net.UDPAddr{{IP: net.IPv4bcast, Port: port}}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil {
				continue
			}
			ip := n.IP.To4()
			mask := net.IP(n.Mask).To4()
			if mask == nil {
				continue
			}
			b := make(net.IP, 4)
			for i := range b {
				b[i] = ip[i] | ^mask[i]
			}
			out = append(out, &net.UDPAddr{IP: b, Port: port})
		}
	}
	return out
}
