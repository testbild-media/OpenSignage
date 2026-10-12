package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testInfo() Info {
	return Info{ServerID: "01920000-0000-7000-8000-000000000001", Name: "Bar server", Version: "1.2.3",
		DevicePort: 7443, Fingerprint: "sha256:abcd", Addresses: []string{"192.168.1.10", "100.64.0.7"}}
}

// TestWireFormat pins the answer encoding the agents rely on: JSON with these
// keys, the magic in every answer, the client-side Source never on the wire.
func TestWireFormat(t *testing.T) {
	in := testInfo()
	in.Source = "192.168.1.10"
	data := reply(newLimiter(), []byte(Magic+"\n"), net.ParseIP("192.168.1.50"), func() Info { return in })
	if data == nil {
		t.Fatal("no answer to a valid request")
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"addresses", "devicePort", "fingerprint", "magic", "name", "serverId", "version"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("answer keys %v, want %v", keys, want)
	}
	if raw["magic"] != Magic || raw["devicePort"] != float64(7443) {
		t.Fatalf("answer: %s", data)
	}
	var back Info
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	want := testInfo()
	want.Magic = Magic
	if !reflect.DeepEqual(back, want) {
		t.Fatalf("round trip: %+v, want %+v", back, want)
	}
	if len(data) > 512 {
		t.Fatalf("answer of %d bytes risks fragmentation", len(data))
	}
	// Requests only need the prefix; a trailing newline or version suffix is fine.
	for _, req := range []string{Magic, Magic + "\n", Magic + " agent/1.2.3"} {
		if reply(newLimiter(), []byte(req), net.ParseIP("10.1.2.3"), testInfo) == nil {
			t.Errorf("request %q not answered", req)
		}
	}
}

// TestMalformedPacketsIgnored: anything that is not a request gets no answer, including
// another server's answer (two servers must never talk to each other in a loop).
func TestMalformedPacketsIgnored(t *testing.T) {
	answer, _ := json.Marshal(Info{Magic: Magic, ServerID: "x"})
	for name, pkt := range map[string][]byte{
		"empty":           {},
		"garbage":         []byte("hello world"),
		"truncated magic": []byte(Magic[:len(Magic)-1]),
		"lower case":      []byte(strings.ToLower(Magic)),
		"leading space":   []byte(" " + Magic),
		"binary":          {0x00, 0xff, 0x13, 0x37},
		"server answer":   answer,
		"http":            []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"),
	} {
		called := false
		if got := reply(newLimiter(), pkt, net.ParseIP("192.168.0.2"), func() Info { called = true; return testInfo() }); got != nil || called {
			t.Errorf("%s: answered %q", name, got)
		}
	}
}

// TestOnlyLocalSourcesAreAnswered: private, CGNAT (Tailscale), link-local and loopback
// sources only; never the internet (no reflection/amplification).
func TestOnlyLocalSourcesAreAnswered(t *testing.T) {
	for ip, want := range map[string]bool{
		"10.0.0.1": true, "10.255.255.254": true, "172.16.0.1": true, "172.31.255.254": true,
		"192.168.178.20": true, "100.64.0.1": true, "100.127.255.254": true, "169.254.10.20": true,
		"127.0.0.1": true, "::1": true, "fe80::1": true, "fd12:3456:789a::1": true, "::ffff:192.168.1.5": true,

		"8.8.8.8": false, "1.1.1.1": false, "172.15.255.255": false, "172.32.0.1": false, "192.169.0.1": false,
		"100.63.255.255": false, "100.128.0.1": false, "11.0.0.1": false, "224.0.0.251": false, "255.255.255.255": false,
		"0.0.0.0": false, "2001:db8::1": false, "2a00:1450:4001::200e": false, "::ffff:8.8.4.4": false, "ff02::1": false,
	} {
		ipp := net.ParseIP(ip)
		if ipp == nil {
			t.Fatalf("bad test address %s", ip)
		}
		if got := localSource(ipp); got != want {
			t.Errorf("localSource(%s) = %v, want %v", ip, got, want)
		}
		if got := reply(newLimiter(), []byte(Magic), ipp, testInfo) != nil; got != want {
			t.Errorf("request from %s answered = %v, want %v", ip, got, want)
		}
	}
}

// TestRateLimit: at most 5 answers per source and second, sources are independent, the
// budget renews, and the table never grows without bound.
func TestRateLimit(t *testing.T) {
	l := newLimiter()
	a, b := net.ParseIP("192.168.1.2"), net.ParseIP("192.168.1.3")
	answered := 0
	for i := 0; i < 20; i++ {
		if reply(l, []byte(Magic), a, testInfo) != nil {
			answered++
		}
	}
	if answered != 5 {
		t.Fatalf("answers in one second: %d, want 5", answered)
	}
	if reply(l, []byte(Magic), b, testInfo) == nil {
		t.Fatal("another source must not be limited by the first")
	}
	l.window = l.window.Add(-1100 * time.Millisecond) // the next second
	if reply(l, []byte(Magic), a, testInfo) == nil {
		t.Fatal("budget must renew after a second")
	}
	// Ignored packets (wrong magic, foreign source) never use up a source's budget.
	l = newLimiter()
	for i := 0; i < 50; i++ {
		reply(l, []byte("noise"), a, testInfo)
	}
	if reply(l, []byte(Magic), a, testInfo) == nil {
		t.Fatal("noise must not count against the limit")
	}
	// Bounded memory: many sources reset the table instead of growing it.
	l = newLimiter()
	for i := 0; i < 10000; i++ {
		l.allow(net.IPv4(10, byte(i>>16), byte(i>>8), byte(i)).String())
		if len(l.counts) > 4097 {
			t.Fatalf("limiter grew to %d entries", len(l.counts))
		}
	}
}

func listenLoopback(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skipf("no UDP on loopback: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestResponderOverUDP runs the real responder loop on loopback: answers reflect the
// current info, malformed packets and floods do not stop it, cancelling ends it.
func TestResponderOverUDP(t *testing.T) {
	srvConn := listenLoopback(t)
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, srvConn, func() Info {
			n := calls.Add(1)
			in := testInfo()
			in.Name = "call " + strings.Repeat("I", int(n))
			return in
		})
	}()
	cli := listenLoopback(t)
	to := srvConn.LocalAddr().(*net.UDPAddr)
	send := func(b []byte) {
		if _, err := cli.WriteToUDP(b, to); err != nil {
			t.Fatal(err)
		}
	}
	recv := func(d time.Duration) []byte {
		_ = cli.SetReadDeadline(time.Now().Add(d))
		buf := make([]byte, 4096)
		n, _, err := cli.ReadFromUDP(buf)
		if err != nil {
			return nil
		}
		return buf[:n]
	}

	send([]byte("not a request"))
	send(bytes.Repeat([]byte{0xff}, 2000))
	if got := recv(300 * time.Millisecond); got != nil {
		t.Fatalf("malformed packets answered: %q", got)
	}
	send([]byte(Magic + "\n"))
	got := recv(3 * time.Second)
	var in Info
	if got == nil || json.Unmarshal(got, &in) != nil || in.Magic != Magic || in.ServerID != testInfo().ServerID || in.Name != "call I" {
		t.Fatalf("answer: %q", got)
	}
	send([]byte(Magic))
	if got := recv(3 * time.Second); got == nil || !strings.Contains(string(got), `"name":"call II"`) {
		t.Fatalf("info must be read per request: %q", got)
	}
	// A flood from one source gets at most the per-second budget (5 per second).
	for i := 0; i < 30; i++ {
		send([]byte(Magic))
	}
	answers := 0
	for recv(300*time.Millisecond) != nil {
		answers++
	}
	if answers > 5 {
		t.Fatalf("flood answered %d times", answers)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve ended with %v after cancel", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("responder does not stop on cancel")
	}
}

// TestCollectAnswers: the searching side keeps valid answers only, one per server, and
// records where each came from.
func TestCollectAnswers(t *testing.T) {
	cli := listenLoopback(t)
	srv := listenLoopback(t)
	to := cli.LocalAddr().(*net.UDPAddr)
	a := testInfo()
	a.Magic = Magic
	b := a
	b.ServerID, b.Name = "01920000-0000-7000-8000-000000000002", "Terrace"
	noID := a
	noID.ServerID = ""
	wrongMagic := a
	wrongMagic.Magic = "OSDISCOVER0"
	for _, v := range []any{a, "garbage", strings.Repeat("x", 6000), wrongMagic, noID, a, b} {
		var pkt []byte
		if s, ok := v.(string); ok {
			pkt = []byte(s)
		} else {
			pkt, _ = json.Marshal(v)
		}
		if _, err := srv.WriteToUDP(pkt, to); err != nil {
			t.Fatal(err)
		}
	}
	got := collect(cli, time.Now().Add(500*time.Millisecond))
	if len(got) != 2 || got[0].ServerID != a.ServerID || got[1].ServerID != b.ServerID {
		t.Fatalf("collected %+v", got)
	}
	for _, in := range got {
		if in.Source != "127.0.0.1" || in.DevicePort != 7443 || len(in.Addresses) != 2 {
			t.Fatalf("answer fields: %+v", in)
		}
	}
}
