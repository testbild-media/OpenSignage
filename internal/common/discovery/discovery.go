// Package discovery finds the OpenSignage server in the local network with a tiny UDP
// broadcast protocol. It is optional: errors are logged, never fatal, and a
// manually configured server address works without it.
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sort"
	"strings"
	"time"
)

// Magic prefixes every request and response.
const Magic = "OSDISCOVER1"

// Info is the server's answer.
type Info struct {
	Magic       string   `json:"magic"`
	ServerID    string   `json:"serverId"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	DevicePort  int      `json:"devicePort"`
	Fingerprint string   `json:"fingerprint"`
	Addresses   []string `json:"addresses"`
	// Source is the address the answer came from (filled by the client).
	Source string `json:"-"`
}

// Respond answers discovery requests on udp port until ctx ends. info is called per
// request so the answer always reflects current addresses.
func Respond(ctx context.Context, port int, info func() Info) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := respondOnce(ctx, port, info)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("discovery responder stopped, retrying", "port", port, "err", err, "retryIn", backoff.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func respondOnce(ctx context.Context, port int, info func() Info) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		return err
	}
	defer conn.Close()
	return serve(ctx, conn, info)
}

// serve answers requests arriving on conn until ctx ends or the socket fails.
func serve(ctx context.Context, conn *net.UDPConn, info func() Info) error {
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	buf := make([]byte, 512)
	limiter := newLimiter()
	fails := 0
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// A read error usually concerns a single datagram (oversized packet, or an
			// ICMP echo of an earlier reply on Windows): keep serving instead of letting
			// one stray packet stop discovery. Only a closed or persistently failing
			// socket ends the loop.
			if fails++; errors.Is(err, net.ErrClosed) || fails > 100 {
				return err
			}
			continue
		}
		fails = 0
		data := reply(limiter, buf[:n], addr.IP, info)
		if data == nil {
			continue
		}
		if _, err := conn.WriteToUDP(data, addr); err != nil {
			slog.Debug("discovery reply failed", "to", addr.String(), "err", err)
		}
	}
}

// reply returns the answer to one request packet from src, or nil when it must be ignored.
func reply(limiter *limiter, pkt []byte, src net.IP, info func() Info) []byte {
	if !strings.HasPrefix(string(pkt), Magic) {
		return nil
	}
	// LAN only, and never usable for traffic amplification: private/link-local
	// sources only, at most a few answers per source and second.
	if !localSource(src) || !limiter.allow(src.String()) {
		return nil
	}
	in := info()
	in.Magic = Magic
	data, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	return data
}

// BroadcastAddrs returns the IPv4 broadcast address of every up interface plus the
// limited broadcast address.
func BroadcastAddrs() []string {
	set := map[string]bool{"255.255.255.255": true}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			mask := ipn.Mask
			if len(mask) == 16 {
				mask = mask[12:]
			}
			b := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				b[i] = ip4[i] | ^mask[i]
			}
			set[b.String()] = true
		}
	}
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// Search sends one round of requests and collects answers for the given wait time.
func Search(ctx context.Context, port int, wait time.Duration) ([]Info, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	for _, b := range BroadcastAddrs() {
		addr := &net.UDPAddr{IP: net.ParseIP(b), Port: port}
		if _, err := conn.WriteToUDP([]byte(Magic+"\n"), addr); err != nil {
			slog.Debug("discovery send failed", "to", b, "err", err)
		}
	}
	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	return collect(conn, deadline), nil
}

// collect reads answers until the deadline: valid ones only, one per server.
func collect(conn *net.UDPConn, deadline time.Time) []Info {
	_ = conn.SetReadDeadline(deadline)
	seen := map[string]bool{}
	var out []Info
	buf := make([]byte, 4096)
	for fails := 0; ; {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			// Deadline reached or socket gone; other errors concern one datagram (see serve).
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, net.ErrClosed) || time.Now().After(deadline) {
				break
			}
			if fails++; fails > 100 {
				break
			}
			continue
		}
		var in Info
		if json.Unmarshal(buf[:n], &in) != nil || in.Magic != Magic || in.ServerID == "" {
			continue
		}
		if seen[in.ServerID] {
			continue
		}
		seen[in.ServerID] = true
		in.Source = addr.IP.String()
		out = append(out, in)
	}
	return out
}

var privateNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10",
		"169.254.0.0/16", "127.0.0.0/8", "fc00::/7", "fe80::/10", "::1/128"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// localSource reports whether ip belongs to a private, link-local or loopback network.
func localSource(ip net.IP) bool {
	for _, n := range privateNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// limiter allows 5 answers per source per second; memory is bounded.
type limiter struct {
	window time.Time
	counts map[string]int
}

func newLimiter() *limiter { return &limiter{counts: map[string]int{}} }

func (l *limiter) allow(src string) bool {
	now := time.Now()
	if now.Sub(l.window) > time.Second || len(l.counts) > 4096 {
		l.window = now
		l.counts = map[string]int{}
	}
	l.counts[src]++
	return l.counts[src] <= 5
}
