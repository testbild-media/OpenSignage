package updates

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"opensignage/internal/common/atomicfile"
)

// Links reads and atomically replaces the release links "current" and "previous".
// Production uses symbolic links (Symlinks); tests on systems without symlink
// permission use a file based implementation.
type Links interface {
	// Read returns the absolute, cleaned target (os.ErrNotExist when missing).
	Read(link string) (string, error)
	// Set points link to target atomically (create + rename).
	Set(link, target string) error
	// Remove deletes the link (missing is not an error).
	Remove(link string) error
}

// Symlinks implements Links with symbolic links.
type Symlinks struct{}

// Read implements Links.
func (Symlinks) Read(link string) (string, error) {
	t, err := os.Readlink(link)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(t) {
		t = filepath.Join(filepath.Dir(link), t)
	}
	return filepath.Clean(t), nil
}

// Set implements Links. rename(2) replaces the link itself, never the directory it
// points to.
func (Symlinks) Set(link, target string) error {
	tmp := link + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	atomicfile.SyncDir(filepath.Dir(link))
	return nil
}

// Remove implements Links.
func (Symlinks) Remove(link string) error {
	err := os.Remove(link)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Layout is the installed release layout created by the installer:
//
//	<Root>/releases/<version>/{bin,share,…}
//	<Root>/current  → releases/<running version>
//	<Root>/previous → releases/<version before the last update>
type Layout struct {
	Root  string
	Links Links
}

func (l *Layout) releasesDir() string        { return filepath.Join(l.Root, "releases") }
func (l *Layout) releaseDir(v string) string { return filepath.Join(l.Root, "releases", v) }
func (l *Layout) currentLink() string        { return filepath.Join(l.Root, "current") }
func (l *Layout) previousLink() string       { return filepath.Join(l.Root, "previous") }

// LayoutError explains why server updates are not possible on this installation. Code is
// stable for the UI (not_linux, not_installed, not_writable); the message carries details.
type LayoutError struct {
	Code string
	Err  error
}

func (e *LayoutError) Error() string { return e.Err.Error() }
func (e *LayoutError) Unwrap() error { return e.Err }

func layoutErr(code, format string, args ...any) error {
	return &LayoutError{Code: code, Err: fmt.Errorf(format, args...)}
}

// DetectLayout derives the layout from the running executable
// (<Root>/releases/<v>/bin/opensignage-server with <Root>/current pointing to that
// release). Development runs and foreign installations return an error explaining why
// server updates are not possible.
func DetectLayout(exe string, links Links) (*Layout, error) {
	if runtime.GOOS != "linux" {
		return nil, layoutErr("not_linux", "server updates are supported on Linux only")
	}
	root, relDir, err := releaseOf(exe)
	if err != nil {
		return nil, &LayoutError{Code: "not_installed", Err: err}
	}
	releases := filepath.Dir(relDir)
	cur, err := links.Read(filepath.Join(root, "current"))
	if err != nil {
		return nil, layoutErr("not_installed", "%s: %w", filepath.Join(root, "current"), err)
	}
	if !sameDir(cur, relDir) {
		return nil, layoutErr("not_installed", "%s does not point to the running release %s", filepath.Join(root, "current"), relDir)
	}
	probe, err := os.CreateTemp(releases, ".write-probe-*")
	if err != nil {
		return nil, layoutErr("not_writable", "%s is not writable: %w", releases, err)
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return &Layout{Root: root, Links: links}, nil
}

// ReleaseRoot returns the release root of an executable installed as
// <root>/releases/<version>/bin/<name> (links such as <root>/previous are resolved).
func ReleaseRoot(exe string) (string, error) {
	root, _, err := releaseOf(exe)
	return root, err
}

// releaseOf resolves exe and returns the release root and the release directory.
func releaseOf(exe string) (root, relDir string, err error) {
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", "", fmt.Errorf("resolving executable: %w", err)
	}
	binDir := filepath.Dir(real)
	relDir = filepath.Dir(binDir)
	releases := filepath.Dir(relDir)
	if filepath.Base(binDir) != "bin" || filepath.Base(releases) != "releases" {
		return "", "", fmt.Errorf("%s is not inside <root>/releases/<version>/bin", real)
	}
	return filepath.Dir(releases), relDir, nil
}

// sameDir reports whether two paths name the same directory.
func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(sa, sb)
}
