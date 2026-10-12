package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opensignage/internal/common/release"
)

// assemble takes exactly the packages of the requested version: never those of a
// pre-release whose version starts the same (1.2.3-rc1) or unknown architectures.
func TestAssembleMatchesExactPackages(t *testing.T) {
	dist := t.TempDir()
	key := filepath.Join(t.TempDir(), "release.key")
	if err := keygen(key); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"opensignage-1.2.3-linux-amd64.tar.gz", "opensignage-1.2.3-linux-arm64.tar.gz",
		"opensignage-1.2.3-rc1-linux-amd64.tar.gz", "opensignage-1.2.3-linux-mips.tar.gz", "opensignage-1.2.3-windows-amd64.tar.gz",
	} {
		if err := os.WriteFile(filepath.Join(dist, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := func(ver string) []string {
		t.Helper()
		if err := assemble(dist, ver, "", key, ""); err != nil {
			t.Fatalf("assemble %s: %v", ver, err)
		}
		b, err := os.ReadFile(filepath.Join(dist, "release-"+ver, release.InfoFile))
		if err != nil {
			t.Fatal(err)
		}
		var info release.Info
		if err := json.Unmarshal(b, &info); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range info.Packages {
			out = append(out, p.OS+"/"+p.Arch+"="+p.File)
		}
		return out
	}
	if got := strings.Join(files("1.2.3"), ","); got != "linux/amd64=opensignage-1.2.3-linux-amd64.tar.gz,linux/arm64=opensignage-1.2.3-linux-arm64.tar.gz" {
		t.Fatalf("1.2.3 packages: %s", got)
	}
	if got := strings.Join(files("1.2.3-rc1"), ","); got != "linux/amd64=opensignage-1.2.3-rc1-linux-amd64.tar.gz" {
		t.Fatalf("1.2.3-rc1 packages: %s", got)
	}
	for _, bad := range []string{"v1.2.3", "1.2.3/../x", "1.2", "1.2.3+build", "1.2.3-", "1.2.3 "} {
		if err := assemble(dist, bad, "", key, ""); err == nil || !strings.Contains(err.Error(), "invalid version") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := assemble(dist, "9.9.9", "", key, ""); err == nil || !strings.Contains(err.Error(), "no packages") {
		t.Errorf("missing packages: %v", err)
	}
}
