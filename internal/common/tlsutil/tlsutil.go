// Package tlsutil creates and manages the certificates OpenSignage needs without a domain:
// a local CA, a WebGUI leaf certificate signed by it, self-signed identity certificates
// for the device channel, and SPKI pinning helpers.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"opensignage/internal/common/atomicfile"
)

// Pair is a certificate with its key.
type Pair struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
	TLS  tls.Certificate
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err)
	}
	return n
}

func encode(certDER []byte, key *ecdsa.PrivateKey) (certPEM, keyPEM []byte, err error) {
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), nil
}

// Load reads a PEM certificate chain and key from files.
func Load(certFile, keyFile string) (*Pair, error) {
	cp, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	kp, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	return Parse(cp, kp)
}

// Parse parses PEM certificate (chain) and key. The key may be EC, RSA or PKCS#8.
func Parse(certPEM, keyPEM []byte) (*Pair, error) {
	tc, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(tc.Certificate[0])
	if err != nil {
		return nil, err
	}
	tc.Leaf = leaf
	p := &Pair{Cert: leaf, TLS: tc}
	if k, ok := tc.PrivateKey.(*ecdsa.PrivateKey); ok {
		p.Key = k
	}
	return p, nil
}

func write(certFile, keyFile string, certPEM, keyPEM []byte) error {
	if err := atomicfile.Write(keyFile, keyPEM, 0o600); err != nil {
		return err
	}
	return atomicfile.Write(certFile, certPEM, 0o644)
}

// EnsureCA loads or creates the local CA in dir (ca.crt, ca.key).
func EnsureCA(dir, commonName string) (*Pair, error) {
	certFile, keyFile := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if p, err := Load(certFile, keyFile); err == nil && p.Key != nil && p.Cert.IsCA {
		return p, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("local CA unreadable (%s): %w", dir, err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"OpenSignage"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cp, kp, err := encode(der, key)
	if err != nil {
		return nil, err
	}
	if err := write(certFile, keyFile, cp, kp); err != nil {
		return nil, err
	}
	return Parse(cp, kp)
}

// Names holds the DNS names and IPs a leaf certificate must cover.
type Names struct {
	DNS []string
	IPs []net.IP
}

// LocalNames collects hostname, hostname.local, localhost and all interface addresses.
func LocalNames() Names {
	n := Names{DNS: []string{"localhost"}}
	if h, err := os.Hostname(); err == nil && h != "" {
		h = strings.ToLower(h)
		n.DNS = append(n.DNS, h)
		if !strings.Contains(h, ".") {
			n.DNS = append(n.DNS, h+".local")
		}
	}
	n.IPs = append(n.IPs, net.ParseIP("127.0.0.1"), net.ParseIP("::1"))
	for _, ip := range InterfaceIPs(false) { // IPv6 privacy addresses rotate; IPv4 + names suffice
		n.IPs = append(n.IPs, net.ParseIP(ip))
	}
	return n
}

// Key returns a canonical string for comparing name sets.
func (n Names) Key() string {
	var s []string
	for _, d := range n.DNS {
		s = append(s, "d:"+d)
	}
	for _, ip := range n.IPs {
		s = append(s, "i:"+ip.String())
	}
	sort.Strings(s)
	return strings.Join(s, ",")
}

func certNames(c *x509.Certificate) Names {
	return Names{DNS: c.DNSNames, IPs: c.IPAddresses}
}

// EnsureLeaf loads dir/<name>.crt|key or (re)issues it from ca when missing, expiring
// within 30 days, or not covering names. Validity is 397 days (accepted by iOS).
func EnsureLeaf(dir, name string, ca *Pair, names Names) (*Pair, bool, error) {
	certFile, keyFile := filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
	if p, err := Load(certFile, keyFile); err == nil {
		fresh := time.Until(p.Cert.NotAfter) > 30*24*time.Hour
		signed := p.Cert.CheckSignatureFrom(ca.Cert) == nil
		if fresh && signed && certNames(p.Cert).Key() == names.Key() {
			return p, false, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, false, err
	}
	now := time.Now()
	cn := "OpenSignage"
	if len(names.DNS) > 1 {
		cn = names.DNS[1]
	}
	tpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"OpenSignage"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(0, 0, 397),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     names.DNS,
		IPAddresses:  names.IPs,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, false, err
	}
	cp, kp, err := encode(der, key)
	if err != nil {
		return nil, false, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert.Raw})
	chain := append(cp, caPEM...)
	if err := write(certFile, keyFile, chain, kp); err != nil {
		return nil, false, err
	}
	p, err := Parse(chain, kp)
	return p, true, err
}

// EnsureSelfSigned loads or creates a long-lived self-signed identity certificate
// (device channel on the server, client WebGUI on players). It is never rotated
// automatically because clients pin it.
func EnsureSelfSigned(dir, name, commonName string, years int) (*Pair, error) {
	certFile, keyFile := filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
	if p, err := Load(certFile, keyFile); err == nil {
		return p, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("certificate %s unreadable: %w", certFile, err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"OpenSignage"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(years, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"opensignage"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cp, kp, err := encode(der, key)
	if err != nil {
		return nil, err
	}
	if err := write(certFile, keyFile, cp, kp); err != nil {
		return nil, err
	}
	return Parse(cp, kp)
}

// SPKIFingerprint returns the hex SHA-256 of the certificate's SubjectPublicKeyInfo.
func SPKIFingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// ShortFingerprint formats the first 8 hex digits as "ABCD-1234" for humans to compare.
func ShortFingerprint(fp string) string {
	if len(fp) < 8 {
		return strings.ToUpper(fp)
	}
	return strings.ToUpper(fp[:4] + "-" + fp[4:8])
}

// PinnedConfig returns a client TLS config that accepts exactly the given SPKI
// fingerprint (hex). With an empty pin it accepts any certificate and reports the
// presented fingerprint through seen (trust on first use during pairing).
func PinnedConfig(pin string, seen func(fp string)) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // replaced by VerifyPeerCertificate (pinning)
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("no server certificate")
			}
			c, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			fp := SPKIFingerprint(c)
			if seen != nil {
				seen(fp)
			}
			if pin != "" && fp != pin {
				return &PinMismatchError{Expected: pin, Got: fp}
			}
			return nil
		},
	}
}

// PinMismatchError means the server presented an unexpected certificate.
type PinMismatchError struct{ Expected, Got string }

func (e *PinMismatchError) Error() string {
	return fmt.Sprintf("server certificate changed (expected %s, got %s)",
		ShortFingerprint(e.Expected), ShortFingerprint(e.Got))
}

// Store serves a certificate that can be swapped at runtime.
type Store struct {
	mu   sync.RWMutex
	pair *Pair
}

// Set replaces the served certificate.
func (s *Store) Set(p *Pair) {
	s.mu.Lock()
	s.pair = p
	s.mu.Unlock()
}

// Get returns the current pair.
func (s *Store) Get() *Pair {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pair
}

// Config returns a server TLS config using the store.
func (s *Store) Config() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			p := s.Get()
			if p == nil {
				return nil, errors.New("no certificate configured")
			}
			return &p.TLS, nil
		},
	}
}

// InterfaceIPs lists non-loopback unicast addresses of up interfaces. IPv6 link-local
// addresses are skipped; IPv6 is included only when withV6 is set.
func InterfaceIPs(withV6 bool) []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() || ipn.IP.IsMulticast() {
				continue
			}
			if ipn.IP.To4() == nil && !withV6 {
				continue
			}
			out = append(out, ipn.IP.String())
		}
	}
	sort.Strings(out)
	return out
}
