package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func makeRelease(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()
	dir := t.TempDir()
	pkg := filepath.Join(dir, "opensignage-1.2.3-linux-amd64.tar.gz")
	if err := os.WriteFile(pkg, []byte("package-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, size, _ := FileSHA256(pkg)
	info := Info{Version: "1.2.3", Packages: []Package{{OS: "linux", Arch: "amd64", File: filepath.Base(pkg), SHA256: sum, Size: size}}}
	b, _ := json.Marshal(info)
	_ = os.WriteFile(filepath.Join(dir, InfoFile), b, 0o644)
	isum, _, _ := FileSHA256(filepath.Join(dir, InfoFile))
	sums := FormatSums(map[string]string{filepath.Base(pkg): sum, InfoFile: isum})
	_ = os.WriteFile(filepath.Join(dir, SumsFile), sums, 0o644)
	_ = os.WriteFile(filepath.Join(dir, SigFile), []byte(Sign(priv, sums)), 0o644)
	return dir
}

func TestVerifyDir(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	dir := makeRelease(t, priv)
	if _, err := VerifyDir(dir, []string{FormatPublicKey(pub)}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDir(dir, []string{FormatPublicKey(other)}, false); err == nil {
		t.Fatal("wrong key must fail")
	}
	// Tampered package → checksum mismatch.
	_ = os.WriteFile(filepath.Join(dir, "opensignage-1.2.3-linux-amd64.tar.gz"), []byte("evil"), 0o644)
	if _, err := VerifyDir(dir, []string{FormatPublicKey(pub)}, false); err == nil {
		t.Fatal("tampered package must fail")
	}
	// Tampered SHA256SUMS → signature fails.
	dir2 := makeRelease(t, priv)
	_ = os.WriteFile(filepath.Join(dir2, SumsFile), []byte("0000000000000000000000000000000000000000000000000000000000000000  x\n"), 0o644)
	if _, err := VerifyDir(dir2, []string{FormatPublicKey(pub)}, false); err == nil {
		t.Fatal("tampered sums must fail")
	}
}
