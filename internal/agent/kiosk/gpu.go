package kiosk

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// GPU self-healing: after crashes of its GPU process Chromium may continue with software
// rendering and software video decoding for the rest of its life – seen on a Raspberry Pi 3
// after an under-voltage: 65 % dropped video frames for hours while everything looked fine
// from the outside. The supervisor asks Chromium for its GPU state (the same as chrome://gpu)
// every GPUCheckInterval. A player that had the GPU and lost it is restarted at once, which
// gives it the GPU back. After gpuRestartLimit such restarts within gpuRestartWindow it keeps
// running in software and reports the GPU as lost; a later restart is tried again when the
// window has passed. A device that never had a GPU (a VM, a missing driver) is left alone.

// GPU modes reported in the status (wire.VideoInfo.GPU).
const (
	GPUHardware = "hardware" // Chromium renders with the GPU
	GPUSoftware = "software" // Chromium renders in software (this device never had the GPU)
	GPULost     = "lost"     // the GPU was in use and is gone; restarts did not bring it back
)

const (
	reasonGPULost    = "gpu lost"
	gpuRestartLimit  = 3
	gpuRestartWindow = time.Hour
)

// GPUInfo is the GPU state of the player.
type GPUInfo struct {
	Mode string // GPUHardware | GPUSoftware | GPULost; "" = not known yet
	// Crashes counts GPU process restarts seen in this agent session (Chromium falls back
	// to software after a few of them in a row).
	Crashes int
	// Recoveries counts browser restarts because the GPU was lost (agent session).
	Recoveries int
	LostAt     time.Time
}

// GPUProbe returns Chromium's GPU mode and the PID of its GPU process (0 = none).
type GPUProbe func(ctx context.Context) (mode string, gpuPID int, err error)

// gpuModeOf maps Chromium's feature status to a mode: GPU compositing decides ("enabled",
// "enabled_on", … = hardware; "disabled_software", "unavailable_software", … = software).
func gpuModeOf(featureStatus map[string]string) string {
	c, ok := featureStatus["gpu_compositing"]
	switch {
	case !ok || c == "":
		return ""
	case strings.HasPrefix(c, "enabled"):
		return GPUHardware
	}
	return GPUSoftware
}

// cdpGPUProbe asks the browser target (DevTools of the running Chromium) via
// SystemInfo.getInfo and SystemInfo.getProcessInfo.
func cdpGPUProbe(userDataDir string) GPUProbe {
	return func(ctx context.Context) (string, int, error) {
		port, err := ReadDevToolsPort(userDataDir)
		if err != nil {
			return "", 0, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/version", port), nil)
		if err != nil {
			return "", 0, err
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", 0, err
		}
		var v struct {
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		err = json.NewDecoder(res.Body).Decode(&v)
		res.Body.Close()
		if err != nil || v.WebSocketDebuggerURL == "" {
			return "", 0, fmt.Errorf("no browser devtools endpoint: %v", err)
		}
		c, err := DialCDP(ctx, v.WebSocketDebuggerURL)
		if err != nil {
			return "", 0, err
		}
		defer c.Close()
		var info struct {
			GPU struct {
				FeatureStatus map[string]string `json:"featureStatus"`
			} `json:"gpu"`
		}
		if err := c.Call(ctx, "SystemInfo.getInfo", map[string]any{}, &info); err != nil {
			return "", 0, err
		}
		var procs struct {
			ProcessInfo []struct {
				Type string `json:"type"`
				ID   int    `json:"id"`
			} `json:"processInfo"`
		}
		pid := 0
		if c.Call(ctx, "SystemInfo.getProcessInfo", map[string]any{}, &procs) == nil {
			for _, p := range procs.ProcessInfo {
				if strings.EqualFold(p.Type, "gpu") {
					pid = p.ID
				}
			}
		}
		return gpuModeOf(info.GPU.FeatureStatus), pid, nil
	}
}

// GPU returns the GPU state of the player.
func (s *Supervisor) GPU() GPUInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gpu
}

// gpuRun is the GPU bookkeeping of one Chromium run.
type gpuRun struct {
	lastCheck time.Time
	pid       int
	tracked   bool // the GPU process is followed in the process tree (Linux), every check
}

// startGPURun: a player restarted because it lost the GPU does not report "software" until
// the new browser was checked.
func (s *Supervisor) startGPURun() {
	s.mu.Lock()
	changed := s.gpuEver && s.gpu.Mode == GPUSoftware
	if changed {
		s.gpu.Mode = ""
	}
	s.mu.Unlock()
	if changed && s.o.OnGPUChange != nil {
		s.o.OnGPUChange()
	}
}

// trackGPUProcess follows the GPU process in the process tree on every supervisor check: a
// new PID is a crash of the GPU process (Chromium restarted it).
func (s *Supervisor) trackGPUProcess(run *gpuRun, browser int) {
	pid := gpuProcessPID(browser)
	if pid == 0 {
		return
	}
	run.tracked = true
	s.gpuPIDChanged(run, pid)
}

func (s *Supervisor) gpuPIDChanged(run *gpuRun, pid int) {
	crashed := run.pid != 0 && pid != run.pid
	run.pid = pid
	if !crashed {
		return
	}
	s.mu.Lock()
	s.gpu.Crashes++
	n := s.gpu.Crashes
	s.mu.Unlock()
	slog.Warn("chromium gpu process restarted", "crashes", n)
	if s.o.OnGPUChange != nil {
		s.o.OnGPUChange()
	}
}

// checkGPU probes Chromium and returns reasonGPULost when the browser must be restarted.
func (s *Supervisor) checkGPU(ctx context.Context, run *gpuRun, startedAt time.Time) string {
	now := time.Now()
	if now.Sub(run.lastCheck) < s.o.GPUCheckInterval {
		return ""
	}
	run.lastCheck = now
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	mode, pid, err := s.o.GPUProbe(pctx)
	cancel()
	if err != nil || mode == "" {
		return "" // unknown: the CDP health check handles a browser that does not answer
	}
	if pid != 0 && !run.tracked {
		s.gpuPIDChanged(run, pid) // no process tree (other systems, tests): the probe's PID
	}
	if mode == GPUSoftware && now.Sub(startedAt) < s.o.GPUSettle {
		return "" // Chromium may still be bringing the GPU up
	}
	s.mu.Lock()
	prev := s.gpu
	restart := false
	switch {
	case mode == GPUHardware:
		s.gpuEver = true
		s.gpu.Mode = GPUHardware
	case !s.gpuEver:
		s.gpu.Mode = GPUSoftware // never had the GPU here: nothing to heal
	default:
		s.gpu.LostAt = now
		s.gpuRestarts = recentTimes(s.gpuRestarts, now, gpuRestartWindow)
		if len(s.gpuRestarts) < gpuRestartLimit {
			s.gpuRestarts = append(s.gpuRestarts, now)
			s.gpu.Recoveries++
			s.gpu.Mode = GPUSoftware
			restart = true
		} else {
			s.gpu.Mode = GPULost
		}
	}
	cur := s.gpu
	s.mu.Unlock()
	if restart {
		slog.Warn("player lost the GPU (software rendering), restarting it", "recoveries", cur.Recoveries)
	} else if cur.Mode == GPULost && prev.Mode != GPULost {
		slog.Warn("player lost the GPU and restarts did not help, it keeps running in software",
			"restartsWithin", gpuRestartWindow.String())
	}
	if cur != prev && s.o.OnGPUChange != nil {
		s.o.OnGPUChange()
	}
	if restart {
		return reasonGPULost
	}
	return ""
}

// recentTimes keeps the times within window before now.
func recentTimes(ts []time.Time, now time.Time, window time.Duration) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < window {
			out = append(out, t)
		}
	}
	return out
}
