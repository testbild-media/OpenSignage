//go:build windows

// Package diskfree reports free disk space of the file system containing a path.
package diskfree

import "golang.org/x/sys/windows"

// Free returns the bytes available to the current user.
func Free(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var avail, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &totalFree); err != nil {
		return 0, err
	}
	return avail, nil
}
