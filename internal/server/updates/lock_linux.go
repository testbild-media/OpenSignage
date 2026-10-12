//go:build linux

package updates

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// flockTry takes an exclusive flock without blocking; false while another open file
// holds it. The kernel releases the lock when the holder closes the file or exits.
func flockTry(f *os.File) (bool, error) {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, unix.EWOULDBLOCK):
			return false, nil
		case errors.Is(err, unix.EINTR):
			continue
		default:
			return false, err
		}
	}
}
