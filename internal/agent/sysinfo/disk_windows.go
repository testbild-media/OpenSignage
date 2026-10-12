//go:build windows

package sysinfo

import "golang.org/x/sys/windows"

// Disk returns total and available bytes of the volume holding path.
func Disk(path string) (total, free uint64, err error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var avail, tot, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &tot, &totalFree); err != nil {
		return 0, 0, err
	}
	return tot, avail, nil
}
