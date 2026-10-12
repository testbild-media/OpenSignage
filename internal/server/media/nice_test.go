//go:build linux

package media

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLowerPriorityNicesChildProcess checks that ffmpeg-style children run with a positive
// nice value on Linux (/proc/<pid>/stat, nice is the 19th field).
func TestLowerPriorityNicesChildProcess(t *testing.T) {
	cmd := exec.Command("sleep", "3")
	if err := cmd.Start(); err != nil {
		t.Skip("sleep not available")
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	lowerPriority(cmd.Process.Pid)
	time.Sleep(50 * time.Millisecond)
	b, err := os.ReadFile("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/stat")
	if err != nil {
		t.Skip("no /proc")
	}
	s := string(b)
	fields := strings.Fields(s[strings.LastIndexByte(s, ')')+2:]) // fields after "(comm)": state is index 0
	nice, err := strconv.Atoi(fields[16])                         // 19th field overall
	if err != nil || nice < 10 {
		t.Fatalf("nice value %q (err %v), want >= 10", fields[16], err)
	}
}
