//go:build unix

// Package diskfree reports free disk space of the file system containing a path.
package diskfree

import "golang.org/x/sys/unix"

// Free returns the bytes available to unprivileged users.
func Free(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
