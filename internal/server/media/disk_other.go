//go:build !windows && !linux && !darwin && !freebsd

package media

import "errors"

func diskUsage(dir string) (total, free uint64, err error) {
	return 0, 0, errors.New("disk usage is not supported on this platform")
}

func isDiskFull(err error) bool { return false }

func lowerPriority(pid int) {}
