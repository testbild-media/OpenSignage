//go:build windows

package updater

import (
	"os"
	"strings"
)

// Windows (development and tests) cannot create symlinks without extra privileges; the
// release pointers are small files holding the target instead.

func symlink(target, path string) error {
	return os.WriteFile(path, []byte(target+"\n"), 0o644)
}

func readlink(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
