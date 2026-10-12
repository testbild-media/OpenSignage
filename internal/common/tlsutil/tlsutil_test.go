package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCALeafAndPinning(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureCA(dir, "Test CA")
	if err != nil {
		t.Fatal(err)
	}
	ca2, err := EnsureCA(dir, "Test CA")
	if err != nil || !ca2.Cert.Equal(ca.Cert) {
		t.Fatal("CA must be stable across loads")
	}
	names := Names{DNS: []string{"localhost", "box"}, IPs: []net.IP{net.ParseIP("127.0.0.1")}}
	leaf, issued, err := EnsureLeaf(dir, "web", ca, names)
	if err != nil || !issued {
		t.Fatal(err)
	}
	if _, issued, _ := EnsureLeaf(dir, "web", ca, names); issued {
		t.Fatal("unchanged names must not reissue")
	}
	names.IPs = append(names.IPs, net.ParseIP("10.0.0.5"))
	if _, issued, _ := EnsureLeaf(dir, "web", ca, names); !issued {
		t.Fatal("new IP must reissue")
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	if _, err := leaf.Cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "box"}); err != nil {
		t.Fatal(err)
	}

	id, err := EnsureSelfSigned(dir, "device", "OpenSignage device channel", 20)
	if err != nil {
		t.Fatal(err)
	}
	fp := SPKIFingerprint(id.Cert)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{id.TLS}}
	srv.StartTLS()
	defer srv.Close()

	var seen string
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: PinnedConfig("", func(f string) { seen = f })}}
	if _, err := c.Get(srv.URL); err != nil || seen != fp {
		t.Fatalf("tofu: %v %s", err, seen)
	}
	c = &http.Client{Transport: &http.Transport{TLSClientConfig: PinnedConfig(fp, nil)}}
	if _, err := c.Get(srv.URL); err != nil {
		t.Fatal(err)
	}
	c = &http.Client{Transport: &http.Transport{TLSClientConfig: PinnedConfig("00"+fp[2:], nil)}}
	_, err = c.Get(srv.URL)
	var pm *PinMismatchError
	if err == nil || !errors.As(err, &pm) {
		t.Fatalf("expected pin mismatch, got %v", err)
	}
}
