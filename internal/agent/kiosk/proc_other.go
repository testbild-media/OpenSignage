//go:build !linux

package kiosk

import (
	"os"
	"os/exec"
	"time"
)

func prepareCmd(*exec.Cmd) {}

func killTree(p *os.Process, exited <-chan struct{}) {
	_ = p.Kill()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
	}
}

func displayReady(string) bool { return true }

func gpuProcessPID(int) int { return 0 }

const platformFlags = ""

const fitWindowSupported = false
