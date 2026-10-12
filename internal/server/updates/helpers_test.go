package updates

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"opensignage/internal/common/atomicfile"
	"opensignage/internal/common/release"
	"opensignage/internal/server/config"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
)

// fileLinks implements Links with plain files holding the target (Windows test hosts
// cannot create symlinks without extra privileges).
type fileLinks struct{}

func (fileLinks) Read(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(string(b)), nil
}
func (fileLinks) Set(p, t string) error { return atomicfile.Write(p, []byte(t), 0o644) }
func (fileLinks) Remove(p string) error { return atomicfile.Remove(p) }

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestModule builds the module on a fresh database (no devices module; tests plug
// in a fake device link).
func newTestModule(t *testing.T) *Module {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(context.Background(), filepath.Join(dir, "db", "opensignage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st, err := core.LoadSettings(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	a := &core.App{Cfg: &config.Config{DataDir: dir, UpdateRepo: "owner/repo"}, DB: d, Log: quietLog(), Settings: st, Bus: core.NewBus()}
	m := New(a)
	m.exit = func(int) { t.Error("unexpected exit") }
	m.RestartDelay = 0
	return m
}

type testKey struct {
	pub  string
	priv ed25519.PrivateKey
}

func newKey(t *testing.T) testKey {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{pub: release.FormatPublicKey(pub), priv: priv}
}

// trust adds a key to the settings cache (no database write needed for tests).
func trust(m *Module, k testKey) {
	s := m.app.Settings.Get()
	s.TrustedKeys = append(s.TrustedKeys, k.pub)
	m.app.Settings.Apply(s)
}

// tgz builds a package archive with one top directory.
func tgz(t *testing.T, top string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: top + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		mode := int64(0o644)
		if filepath.Dir(n) == "bin" {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: top + "/" + n, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(files[n]))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(files[n]))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// writeRelease writes a signed release (packages + release.json + SHA256SUMS + sig)
// into dir. pkgs maps "os/arch" to the package bytes.
func writeRelease(t *testing.T, dir, ver string, key testKey, pkgs map[string][]byte) release.Info {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	info := release.Info{Version: ver, Date: time.Now().UTC().Format(time.RFC3339), Notes: "Line 1\nLine 2"}
	sums := map[string]string{}
	plats := make([]string, 0, len(pkgs))
	for p := range pkgs {
		plats = append(plats, p)
	}
	sort.Strings(plats)
	for _, plat := range plats {
		goos, arch, _ := cut(plat)
		name := "opensignage-" + ver + "-" + goos + "-" + arch + ".tar.gz"
		if err := os.WriteFile(filepath.Join(dir, name), pkgs[plat], 0o644); err != nil {
			t.Fatal(err)
		}
		sum, size, err := release.FileSHA256(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		info.Packages = append(info.Packages, release.Package{OS: goos, Arch: arch, File: name, SHA256: sum, Size: size})
		sums[name] = sum
	}
	b, _ := json.MarshalIndent(info, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, release.InfoFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _, _ := release.FileSHA256(filepath.Join(dir, release.InfoFile))
	sums[release.InfoFile] = sum
	sumsData := release.FormatSums(sums)
	_ = os.WriteFile(filepath.Join(dir, release.SumsFile), sumsData, 0o644)
	_ = os.WriteFile(filepath.Join(dir, release.SigFile), []byte(release.Sign(key.priv, sumsData)+"\n"), 0o644)
	return info
}

func cut(plat string) (string, string, bool) {
	for i := 0; i < len(plat); i++ {
		if plat[i] == '/' {
			return plat[:i], plat[i+1:], true
		}
	}
	return plat, "", false
}

// bundle packs the given files of dir into an offline bundle (flat tar).
func bundle(t *testing.T, dir string, extra map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		_ = tw.WriteHeader(&tar.Header{Name: e.Name(), Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(b))})
		_, _ = tw.Write(b)
	}
	for name, content := range extra {
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))})
		_, _ = tw.Write([]byte(content))
	}
	_ = tw.Close()
	return buf.Bytes()
}

// readyRelease installs a verified release for version v into the module's data dir.
func readyRelease(t *testing.T, m *Module, v string, key testKey, pkgs map[string][]byte) release.Info {
	t.Helper()
	info := writeRelease(t, m.releaseDir(v), v, key, pkgs)
	if _, err := m.app.DB.W.Exec(`INSERT INTO releases(version, source, notes, size, published_at, state, error, path, updated_at)
		VALUES (?, 'upload', '', 1, '', 'ready', '', ?, ?)`, v, m.releaseDir(v), db.Now()); err != nil {
		t.Fatal(err)
	}
	return info
}

func mustExec(t *testing.T, m *Module, q string, args ...any) {
	t.Helper()
	if _, err := m.app.DB.W.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
