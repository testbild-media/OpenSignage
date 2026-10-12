package app

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// startRedirect runs the real plain-HTTP listener of the server on a free local port
// and returns its address.
func startRedirect(t *testing.T, s *Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := s.redirectServer(ln.Addr().String())
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// noFollow is a plain HTTP client that reports redirects instead of following them.
var noFollow = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// TestHTTPRedirectsToHTTPS: plain HTTP answers with a permanent redirect to the HTTPS
// URL on the same host, keeping path and query; only /ca.crt is served over HTTP so
// devices can fetch the CA before they trust the WebGUI.
func TestHTTPRedirectsToHTTPS(t *testing.T) {
	srv, _ := newTestServer(t)
	addr := startRedirect(t, srv)

	for _, tc := range []struct {
		method, host, uri string
		httpsPort         int
		want              string
	}{
		{"GET", "signage.local", "/", 443, "https://signage.local/"},
		{"GET", "signage.local:80", "/media?folder=root&sort=name", 443, "https://signage.local/media?folder=root&sort=name"},
		{"GET", "192.168.1.20", "/control/zone/a%20b?x=%C3%BC#frag", 443, "https://192.168.1.20/control/zone/a%20b?x=%C3%BC"},
		{"GET", "[fe80::1]:80", "/login", 443, "https://[fe80::1]/login"},
		{"HEAD", "signage.local", "/devices", 443, "https://signage.local/devices"},
		{"POST", "signage.local", "/api/v1/auth/login", 443, "https://signage.local/api/v1/auth/login"},
		{"GET", "10.0.0.5:8080", "/settings?tab=updates", 8443, "https://10.0.0.5:8443/settings?tab=updates"},
	} {
		srv.App.Cfg.HTTPSPort = tc.httpsPort
		req, _ := http.NewRequest(tc.method, "http://"+addr+tc.uri, nil)
		req.Host = tc.host
		res, err := noFollow.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.uri, err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusMovedPermanently && res.StatusCode != http.StatusPermanentRedirect {
			t.Fatalf("%s %s%s: status %d, want 301/308", tc.method, tc.host, tc.uri, res.StatusCode)
		}
		if got := res.Header.Get("Location"); got != tc.want {
			t.Fatalf("%s %s%s: Location %q, want %q", tc.method, tc.host, tc.uri, got, tc.want)
		}
	}
	srv.App.Cfg.HTTPSPort = 443

	// HTTP/1.0 without Host header: the redirect must still name a host.
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("GET /x?y=1 HTTP/1.0\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if loc := res.Header.Get("Location"); res.StatusCode != http.StatusMovedPermanently ||
		!strings.HasPrefix(loc, "https://") || strings.HasPrefix(loc, "https:///") || !strings.HasSuffix(loc, "/x?y=1") {
		t.Fatalf("redirect without Host: %d %q", res.StatusCode, loc)
	}

	// The CA certificate is downloadable over plain HTTP (no redirect).
	res, err = noFollow.Get("http://" + addr + "/ca.crt")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/x-x509-ca-cert" ||
		!strings.Contains(res.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("/ca.crt over HTTP: %d %v", res.StatusCode, res.Header)
	}
	block, _ := pem.Decode(body)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("/ca.crt is not a PEM certificate: %q", body)
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA || !bytes.Equal(block.Bytes, srv.App.CA.Cert.Raw) {
		t.Fatalf("/ca.crt is not the server's local CA: %v", err)
	}
	// Only the exact path is served; anything else (and the WebGUI API) redirects.
	for _, uri := range []string{"/ca.crt/../api/v1/users", "/ca.crt.bak", "/api/v1/setup/status"} {
		res, err := noFollow.Get("http://" + addr + uri)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusMovedPermanently {
			t.Fatalf("%s over HTTP must redirect, got %d", uri, res.StatusCode)
		}
	}
}

// TestRedirectServerTimeouts: the unauthenticated plain-HTTP listener must not let slow or
// idle clients hold connections open (slowloris).
func TestRedirectServerTimeouts(t *testing.T) {
	srv, _ := newTestServer(t)
	rs := srv.redirectServer(":0")
	for name, d := range map[string]struct {
		got time.Duration
		max time.Duration
	}{
		"ReadHeaderTimeout": {rs.ReadHeaderTimeout, 30 * time.Second},
		"ReadTimeout":       {rs.ReadTimeout, time.Minute},
		"WriteTimeout":      {rs.WriteTimeout, time.Minute},
		"IdleTimeout":       {rs.IdleTimeout, 2 * time.Minute},
	} {
		if d.got <= 0 || d.got > d.max {
			t.Errorf("%s = %v, want 0 < t <= %v", name, d.got, d.max)
		}
	}
	if rs.ReadHeaderTimeout > rs.ReadTimeout {
		t.Errorf("ReadHeaderTimeout %v exceeds ReadTimeout %v", rs.ReadHeaderTimeout, rs.ReadTimeout)
	}
	if rs.Handler == nil || rs.TLSConfig != nil {
		t.Error("redirect server must be plain HTTP with its own handler")
	}

	// A client that never finishes its request header is disconnected (shortened timeout,
	// same server construction).
	rs.ReadHeaderTimeout = 200 * time.Millisecond
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = rs.Serve(ln) }()
	defer rs.Close()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n")) // header never completed
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	_, err = io.ReadAll(conn)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatal("slow client was not disconnected")
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("slow client disconnected only after %v", el)
	}
}
