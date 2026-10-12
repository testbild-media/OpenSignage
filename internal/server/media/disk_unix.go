//go:build linux || darwin || freebsd

package media

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// diskUsage returns total and free bytes available to unprivileged users for dir.
func diskUsage(dir string) (total, free uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return uint64(st.Blocks) * bs, uint64(st.Bavail) * bs, nil
}

// isDiskFull reports whether err is an out-of-space error.
func isDiskFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}

// lowerPriority makes a child process nicer (nice +10).
func lowerPriority(pid int) {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, 10)
}
