//go:build linux

package kiosk

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"opensignage/internal/agent/sysinfo"
)

// prepareCmd puts Chromium into its own process group and kills it if the agent dies.
func prepareCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

// killTree terminates Chromium, its process group and every descendant.
func killTree(p *os.Process, exited <-chan struct{}) {
	pids := sysinfo.ProcessTree(p.Pid)
	_ = syscall.Kill(-p.Pid, syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
	}
	_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// gpuProcessPID returns the PID of Chromium's GPU process (a descendant of the browser
// process), 0 when there is none.
func gpuProcessPID(browser int) int {
	for _, pid := range sysinfo.ProcessTree(browser) {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		if err == nil && strings.Contains(string(b), "--type=gpu-process") {
			return pid
		}
	}
	return 0
}

// displayReady reports whether the X server socket for display ":N" exists.
func displayReady(display string) bool {
	n := strings.TrimPrefix(display, ":")
	if i := strings.IndexByte(n, '.'); i >= 0 {
		n = n[:i]
	}
	_, err := os.Stat("/tmp/.X11-unix/X" + n)
	return err == nil
}

const platformFlags = "--ozone-platform=x11"

// fitWindowSupported: Chromium runs on X11 without a window manager (xwindow.go).
const fitWindowSupported = true
