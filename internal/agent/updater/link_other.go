//go:build !windows

package updater

import "os"

func symlink(target, path string) error { return os.Symlink(target, path) }

func readlink(path string) (string, error) { return os.Readlink(path) }
