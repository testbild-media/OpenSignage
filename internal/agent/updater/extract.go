package updater

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"opensignage/internal/common/atomicfile"
)

// Extract unpacks a release package (tar.gz with one top-level directory
// opensignage-<v>-<os>-<arch>/) into dest, dropping the top-level directory. Only
// regular files and directories are accepted; paths may not escape dest; the server
// binary is skipped (clients never run it). The package must contain
// bin/opensignage-agent.
//
// Every file and directory is on disk when Extract returns (fsync): the update switches to
// the new release right after, and a power cut seconds later must not leave the switch
// pointing at files whose data never reached the disk – the new agent would crash on every
// start until the rollback.
func Extract(tgz, dest string, maxBytes int64) error {
	f, err := os.Open(tgz)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("package is not gzip: %w", err)
	}
	defer gz.Close()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	haveAgent := false
	dirs := map[string]bool{dest: true}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("package unreadable: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(filepath.ToSlash(h.Name), "./"))
		if strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("package entry escapes the release directory: %q", h.Name)
		}
		_, rel, found := strings.Cut(name, "/")
		if !found || rel == "" {
			continue // the top-level directory itself
		}
		if strings.Contains("/"+rel+"/", "/../") {
			return fmt.Errorf("package entry escapes the release directory: %q", h.Name)
		}
		if rel == "bin/opensignage-server" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			dirs[target] = true
		case tar.TypeReg:
			total += h.Size
			if total > maxBytes {
				return fmt.Errorf("package larger than %d bytes", maxBytes)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			dirs[filepath.Dir(target)] = true
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, io.LimitReader(tr, h.Size))
			if err == nil {
				err = out.Sync()
			}
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			if err := os.Chmod(target, mode); err != nil {
				return err
			}
			if rel == "bin/opensignage-agent" {
				haveAgent = true
			}
		default:
			return fmt.Errorf("unexpected package entry type %c for %q", h.Typeflag, h.Name)
		}
	}
	if !haveAgent {
		return errors.New("package contains no bin/opensignage-agent")
	}
	// The directory entries of the new files as well, deepest first.
	list := make([]string, 0, len(dirs))
	for d := range dirs {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return len(list[i]) > len(list[j]) })
	for _, d := range list {
		atomicfile.SyncDir(d)
	}
	return nil
}
