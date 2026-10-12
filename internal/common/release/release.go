// Package release defines the release format (ARCHITECTURE §5.9) and verifies releases:
// SHA256SUMS signed with Ed25519, packages per OS/architecture, offline bundles.
package release

import (
	"archive/tar"
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"opensignage/internal/common/version"
)

// Package describes one archive of a release.
type Package struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Info is release.json.
type Info struct {
	Version        string    `json:"version"`
	Date           string    `json:"date"`
	Notes          string    `json:"notes"`
	Packages       []Package `json:"packages"`
	MinUpgradeFrom string    `json:"minUpgradeFrom,omitempty"`
}

// File names inside a release.
const (
	InfoFile = "release.json"
	SumsFile = "SHA256SUMS"
	SigFile  = "SHA256SUMS.sig"
)

// Arch maps GOARCH (+GOARM) to the package architecture name.
func Arch(goarch, goarm string) string {
	if goarch == "arm" {
		return "armv7"
	}
	return goarch
}

// PackageFor returns the package for an OS/architecture.
func (i *Info) PackageFor(osName, arch string) (Package, bool) {
	for _, p := range i.Packages {
		if p.OS == osName && p.Arch == arch {
			return p, true
		}
	}
	return Package{}, false
}

// Validate checks release.json.
func (i *Info) Validate() error {
	if _, err := version.Parse(i.Version); err != nil {
		return fmt.Errorf("release.json: %w", err)
	}
	if len(i.Packages) == 0 {
		return errors.New("release.json: no packages")
	}
	for _, p := range i.Packages {
		if p.File == "" || strings.ContainsAny(p.File, "/\\") || strings.Contains(p.File, "..") {
			return fmt.Errorf("release.json: invalid file name %q", p.File)
		}
		if len(p.SHA256) != 64 {
			return fmt.Errorf("release.json: invalid sha256 for %s", p.File)
		}
	}
	return nil
}

// Sums parses a SHA256SUMS file ("<hex>  <name>" per line).
func Sums(data []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			return nil, fmt.Errorf("SHA256SUMS: malformed line %q", line)
		}
		out[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
	}
	return out, sc.Err()
}

// FormatSums renders a SHA256SUMS file with sorted names.
func FormatSums(sums map[string]string) []byte {
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", sums[n], n)
	}
	return b.Bytes()
}

// ParsePublicKey decodes a base64 Ed25519 public key ("osk1:" prefix optional).
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "osk1:")
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("invalid public key")
	}
	return ed25519.PublicKey(b), nil
}

// FormatPublicKey encodes a public key for configuration and display.
func FormatPublicKey(k ed25519.PublicKey) string {
	return "osk1:" + base64.StdEncoding.EncodeToString(k)
}

// Sign returns the base64 signature of SHA256SUMS.
func Sign(priv ed25519.PrivateKey, sums []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums))
}

// ErrUntrusted means no trusted key verified the signature.
var ErrUntrusted = errors.New("release signature is not valid for any trusted key")

// VerifySignature checks sig over sums against the trusted keys.
func VerifySignature(sums []byte, sig string, trusted []string) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sig))
	if err != nil {
		return fmt.Errorf("signature unreadable: %w", err)
	}
	for _, k := range trusted {
		pub, err := ParsePublicKey(k)
		if err != nil {
			continue
		}
		if ed25519.Verify(pub, sums, raw) {
			return nil
		}
	}
	return ErrUntrusted
}

// FileSHA256 hashes a file.
func FileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Verified is a release directory whose files were checked.
type Verified struct {
	Dir  string
	Info Info
}

// VerifyDir checks a directory containing release.json, SHA256SUMS, SHA256SUMS.sig and
// packages: signature, every listed checksum, and release.json consistency. Packages that
// are listed but missing are allowed only when allowMissing is set (partial downloads).
func VerifyDir(dir string, trusted []string, allowMissing bool) (*Verified, error) {
	sumsData, err := os.ReadFile(filepath.Join(dir, SumsFile))
	if err != nil {
		return nil, err
	}
	sig, err := os.ReadFile(filepath.Join(dir, SigFile))
	if err != nil {
		return nil, fmt.Errorf("release is not signed: %w", err)
	}
	if err := VerifySignature(sumsData, string(sig), trusted); err != nil {
		return nil, err
	}
	sums, err := Sums(sumsData)
	if err != nil {
		return nil, err
	}
	infoData, err := os.ReadFile(filepath.Join(dir, InfoFile))
	if err != nil {
		return nil, err
	}
	if want, ok := sums[InfoFile]; !ok || want != sha256Hex(infoData) {
		return nil, errors.New("release.json does not match SHA256SUMS")
	}
	var info Info
	if err := json.Unmarshal(infoData, &info); err != nil {
		return nil, fmt.Errorf("release.json: %w", err)
	}
	if err := info.Validate(); err != nil {
		return nil, err
	}
	for _, p := range info.Packages {
		if sums[p.File] != p.SHA256 {
			return nil, fmt.Errorf("%s: checksum differs between release.json and SHA256SUMS", p.File)
		}
		got, size, err := FileSHA256(filepath.Join(dir, p.File))
		if errors.Is(err, os.ErrNotExist) && allowMissing {
			continue
		}
		if err != nil {
			return nil, err
		}
		if got != p.SHA256 || size != p.Size {
			return nil, fmt.Errorf("%s: checksum mismatch", p.File)
		}
	}
	return &Verified{Dir: dir, Info: info}, nil
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ExtractBundle unpacks an offline bundle (plain tar of the release files) into dir,
// refusing paths that escape it.
func ExtractBundle(r io.Reader, dir string, maxBytes int64) error {
	tr := tar.NewReader(io.LimitReader(r, maxBytes))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("bundle: %w", err)
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		clean := path.Clean(strings.TrimPrefix(filepath.ToSlash(h.Name), "./"))
		if h.Typeflag != tar.TypeReg || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			return fmt.Errorf("bundle: unexpected entry %q", h.Name)
		}
		name := path.Base(clean) // bundles are flat; directories inside are ignored
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, tr)
		cerr := f.Close()
		if err != nil {
			return err
		}
		if cerr != nil {
			return cerr
		}
	}
}

// TrustedKeys is the built-in, comma separated list of trusted release keys, set at build
// time with -ldflags "-X opensignage/internal/common/release.TrustedKeys=osk1:…".
// The default is the key the published releases are signed with; install.sh embeds the same key.
var TrustedKeys = "osk1:5w7fufvnSzj/99bUODdFjp8l1r28QS9ay8l/9RkRgco="

// BuiltinKeys returns the built-in trusted keys.
func BuiltinKeys() []string {
	var out []string
	for _, k := range strings.Split(TrustedKeys, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}
