//go:build windows

package media

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// diskUsage returns total and free bytes available to the current user for dir.
func diskUsage(dir string) (total, free uint64, err error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, 0, err
	}
	var avail, tot, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &tot, &totalFree); err != nil {
		return 0, 0, err
	}
	return tot, avail, nil
}

// isDiskFull reports whether err is an out-of-space error.
func isDiskFull(err error) bool {
	// ERROR_HANDLE_DISK_FULL = 39, ERROR_DISK_FULL = 112
	return errors.Is(err, syscall.Errno(39)) || errors.Is(err, syscall.Errno(112))
}

// lowerPriority makes a child process run at below-normal priority.
func lowerPriority(pid int) {
	h, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.SetPriorityClass(h, windows.BELOW_NORMAL_PRIORITY_CLASS)
}
