package clocksync

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

func TestFromExchangeAndBest(t *testing.T) {
	t0 := time.Now()
	t3 := t0.Add(40 * time.Millisecond)
	// The remote clock is 5 s ahead; it answered in the middle of the round trip.
	remote := t0.Add(20 * time.Millisecond).Add(5 * time.Second).Round(0)
	s := FromExchange(t0, remote, t3)
	if s.RTT != 40*time.Millisecond || (s.Offset-5*time.Second).Abs() > time.Millisecond {
		t.Fatalf("sample %+v", s)
	}
	b := NewBurst()
	b.Sent(1, t0)
	b.Sent(2, t0)
	if !b.Answer(1, remote, t3) || b.Answer(1, remote, t3) || b.Answer(9, remote, t3) {
		t.Fatal("answers are matched once by sequence number")
	}
	b.Answer(2, t0.Add(2*time.Millisecond).Add(5*time.Second).Round(0), t0.Add(4*time.Millisecond))
	best, ok := b.Result()
	if !ok || best.RTT != 4*time.Millisecond || !b.Done() {
		t.Fatalf("the shortest round trip wins: %+v", best)
	}
}

func TestChooseLeader(t *testing.T) {
	now := time.Now()
	peers := map[string]peer{
		"b": {online: false, seen: now},
		"c": {online: true, seen: now},
		"a": {online: true, seen: now.Add(-time.Minute)}, // gone
	}
	if got := chooseLeader("x", false, peers, now); got != "c" {
		t.Fatalf("a device with the server time leads: %q", got)
	}
	delete(peers, "c")
	if got := chooseLeader("x", false, peers, now); got != "b" {
		t.Fatalf("else the lowest id: %q", got)
	}
	if got := chooseLeader("a", false, peers, now); got != "" {
		t.Fatalf("this agent leads itself: %q", got)
	}
}

// Two agents of a zone without server: the follower measures against the leader over UDP
// and takes its time; a packet with another zone key is ignored.
func TestFollowLeaderOverUDP(t *testing.T) {
	const key = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	never := func() time.Duration { return time.Hour }
	free := func() int {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port
	}
	var mu sync.Mutex
	var got []Sample
	leader := &Peers{Port: free(), Self: Self{
		DeviceID: func() string { return "a-leader" }, Zone: func() (string, string) { return "z", key },
		Now: func() time.Time { return time.Now().Add(7 * time.Second) }, ServerAge: never, Apply: func(Sample, string) {},
	}}
	follower := &Peers{Port: free(), Self: Self{
		DeviceID: func() string { return "b-follower" }, Zone: func() (string, string) { return "z", key },
		Now: time.Now, ServerAge: never,
		Apply: func(s Sample, l string) {
			mu.Lock()
			got = append(got, s)
			mu.Unlock()
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = leader.Run(ctx) }()
	go func() { _ = follower.Run(ctx) }()
	time.Sleep(200 * time.Millisecond)
	// The leader's beacon as the follower would receive it in the LAN.
	b, err := seal(mustKey(t, key), packet{K: "beacon", Zone: "z", Dev: "a-leader"})
	if err != nil {
		t.Fatal(err)
	}
	follower.handle(b, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: leader.Port}, time.Now())
	forged, _ := seal(mustKey(t, "ff"+key[2:]), packet{K: "beacon", Zone: "z", Dev: "0-intruder", Online: true})
	follower.handle(forged, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}, time.Now())
	follower.follow(ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || (got[0].Offset-7*time.Second).Abs() > 50*time.Millisecond || follower.Leader() != "a-leader" {
		t.Fatalf("follower samples %+v, leader %q", got, follower.Leader())
	}
}

func mustKey(t *testing.T, h string) []byte {
	t.Helper()
	p := &Peers{Self: Self{Zone: func() (string, string) { return "z", h }}}
	_, k := p.key()
	if k == nil {
		t.Fatal("bad key")
	}
	return k
}
