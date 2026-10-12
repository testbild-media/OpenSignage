package webgui

import (
	"bufio"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"opensignage/internal/agent/safe"
)

// splitListener serves TLS and plain HTTP on the same port: the first byte of a TLS
// connection is 0x16 (handshake record); everything else is plain HTTP and gets a
// redirect to HTTPS.
type splitListener struct {
	ln    net.Listener
	tls   *chanListener
	plain *chanListener
}

func newSplitListener(ln net.Listener) *splitListener {
	s := &splitListener{ln: ln, tls: newChanListener(ln.Addr()), plain: newChanListener(ln.Addr())}
	safe.Go("webgui-accept", s.acceptLoop)
	return s
}

func (s *splitListener) acceptLoop() {
	defer s.tls.Close()
	defer s.plain.Close()
	delay := 5 * time.Millisecond
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(delay)
				delay = min(delay*2, time.Second)
				continue
			}
			return
		}
		delay = 5 * time.Millisecond
		safe.Go("webgui-sniff", func() { s.route(conn) })
	}
}

func (s *splitListener) route(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)
	b, err := br.Peek(1)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return
	}
	pc := &peekConn{Conn: conn, r: br}
	target := s.plain
	if b[0] == 0x16 {
		target = s.tls
	}
	if !target.deliver(pc) {
		_ = conn.Close()
	}
}

// Close stops accepting.
func (s *splitListener) Close() error { return s.ln.Close() }

type peekConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// chanListener is a net.Listener fed from a channel.
type chanListener struct {
	addr   net.Addr
	ch     chan net.Conn
	once   sync.Once
	closed chan struct{}
}

func newChanListener(addr net.Addr) *chanListener {
	return &chanListener{addr: addr, ch: make(chan net.Conn, 16), closed: make(chan struct{})}
}

func (l *chanListener) deliver(c net.Conn) bool {
	select {
	case <-l.closed:
		return false
	case l.ch <- c:
		return true
	case <-time.After(10 * time.Second):
		slog.Warn("webgui connection dropped (server busy)")
		return false
	}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }
