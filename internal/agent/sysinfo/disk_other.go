//go:build !linux && !windows

package sysinfo

import "errors"

// Disk is not implemented on this platform.
func Disk(string) (total, free uint64, err error) {
	return 0, 0, errors.New("disk statistics not supported on this platform")
}
