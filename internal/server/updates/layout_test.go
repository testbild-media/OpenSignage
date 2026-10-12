package updates

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSymlinksAndDetectLayout uses real symlinks (skipped where they are not permitted,
// e.g. Windows without developer mode).
func TestSymlinksAndDetectLayout(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "releases", "1.0.0")
	b := filepath.Join(root, "releases", "1.1.0")
	for _, d := range []string{filepath.Join(a, "bin"), filepath.Join(b, "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exe := filepath.Join(a, "bin", "opensignage-server")
	_ = os.WriteFile(exe, []byte("x"), 0o755)
	if err := os.Symlink(a, filepath.Join(root, "probe")); err != nil {
		t.Skipf("symlinks not permitted here: %v", err)
	}
	var l Symlinks
	cur := filepath.Join(root, "current")
	if err := l.Set(cur, a); err != nil {
		t.Fatal(err)
	}
	if err := l.Set(cur, b); err != nil { // replaces the link, never the directory
		t.Fatal(err)
	}
	if got, _ := l.Read(cur); got != b {
		t.Fatalf("current → %s", got)
	}
	if _, err := os.Stat(filepath.Join(a, "bin")); err != nil {
		t.Fatalf("old release damaged: %v", err)
	}
	if runtime.GOOS == "linux" {
		if _, err := DetectLayout(exe, l); err == nil {
			t.Fatalf("current points to another release: must be refused")
		}
		_ = l.Set(cur, a)
		lay, err := DetectLayout(filepath.Join(cur, "bin", "opensignage-server"), l)
		if err != nil || lay.Root != root {
			t.Fatalf("layout: %v %+v", err, lay)
		}
	}
	if err := l.Remove(cur); err != nil || l.Remove(cur) != nil {
		t.Fatalf("remove: %v", err)
	}
}
