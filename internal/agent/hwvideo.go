package agent

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"opensignage/internal/agent/kiosk"
	"opensignage/internal/agent/wire"
)

// Automatic fallback to software decoding: the player reloads a video after a decode error
// up to three times a minute and then reports "media failed: media_error_3". Two such
// reports within decodeErrorWindow while hardware decoding runs because of "auto" (e.g. the
// Raspberry Pi 3 running out of contiguous GPU memory at 1080p) switch this agent session to
// software decoding. On Intel/AMD hardware decoding also moves Chromium's whole compositing
// to Vulkan, so repeated player crashes there switch back as well. An explicit "on" is
// never overridden.
const (
	decodeErrorReport = "media failed: media_error_3" // MediaError.MEDIA_ERR_DECODE
	decodeErrorLimit  = 2
	decodeErrorWindow = 10 * time.Minute
	crashLimit        = 3
	crashWindow       = 10 * time.Minute
)

// Reasons reported in the status (wire.VideoInfo.Fallback).
const (
	FallbackDecodeErrors = "decode_errors"
	FallbackCrashes      = "player_crashes"
)

type hwVideoState struct {
	mu       sync.Mutex
	mode     string      // device setting auto | on | off
	fallback time.Time   // "auto" switched to software decoding (zero = no)
	reason   string      // FallbackDecodeErrors | FallbackCrashes
	errors   []time.Time // recent decode failures reported by the player
	crashes  []time.Time // recent player crashes while decoding in hardware
}

// recentWithin keeps the times within window before now and appends now.
func recentWithin(ts []time.Time, now time.Time, window time.Duration) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < window {
			out = append(out, t)
		}
	}
	return append(out, now)
}

// fallBackLocked switches an "auto" session to software decoding (a.hw.mu held; unlocks it).
func (a *Agent) fallBackLocked(now time.Time, reason string) {
	a.hw.fallback, a.hw.reason, a.hw.errors, a.hw.crashes = now, reason, nil, nil
	mode := a.hw.mode
	a.hw.mu.Unlock()
	slog.Warn("hardware video decoding failed repeatedly, using software decoding until the agent restarts",
		"reason", reason, "path", string(a.videoAccel))
	a.setHWVideo(mode)
	if a.link != nil && a.link.Connected() {
		a.link.Send(wire.TStatus, "", a.Status())
	}
}

// playerCrashed counts player crashes while "auto" decodes in hardware with Vulkan
// compositing (Intel/AMD); crashLimit of them within crashWindow end it for the session.
func (a *Agent) playerCrashed(now time.Time) {
	if !a.hwVideo.Load() || a.videoAccel != kiosk.AccelVAAPI {
		return
	}
	a.hw.mu.Lock()
	if !isAutoMode(a.hw.mode) || !a.hw.fallback.IsZero() {
		a.hw.mu.Unlock()
		return
	}
	a.hw.crashes = recentWithin(a.hw.crashes, now, crashWindow)
	if len(a.hw.crashes) < crashLimit {
		a.hw.mu.Unlock()
		return
	}
	a.fallBackLocked(now, FallbackCrashes)
}

func isAutoMode(mode string) bool { return mode != "on" && mode != "off" }

// setHWVideo applies the hardware video setting (auto | on | off) resolved for this device;
// Chromium restarts when the result changes (its flags are fixed at start).
func (a *Agent) setHWVideo(mode string) {
	a.hw.mu.Lock()
	a.hw.mode = mode
	hw := kiosk.HWVideoEnabled(mode, a.videoAccel) && !(isAutoMode(mode) && !a.hw.fallback.IsZero())
	a.hw.mu.Unlock()
	if a.hwVideo.Swap(hw) != hw && a.kiosk != nil {
		slog.Info("hardware video decoding changed, restarting the player", "setting", mode, "on", hw)
		a.kiosk.Restart("hwVideo changed")
	}
}

// playerLog watches the player's log for decode failures (automatic fallback).
func (a *Agent) playerLog(l wire.PlayerLog) {
	if !strings.Contains(l.Msg, decodeErrorReport) || !a.hwVideo.Load() {
		return
	}
	now := time.Now()
	a.hw.mu.Lock()
	if !isAutoMode(a.hw.mode) || !a.hw.fallback.IsZero() {
		a.hw.mu.Unlock()
		return
	}
	a.hw.errors = recentWithin(a.hw.errors, now, decodeErrorWindow)
	if len(a.hw.errors) < decodeErrorLimit {
		a.hw.mu.Unlock()
		return
	}
	a.fallBackLocked(now, FallbackDecodeErrors)
}

// videoStatus reports how videos are decoded (status for the admin): the setting, and what
// Chromium really does – without the GPU there is no hardware decoding either.
func (a *Agent) videoStatus() *wire.VideoInfo {
	v := &wire.VideoInfo{HW: a.hwVideo.Load(), Path: string(a.videoAccel)}
	a.hw.mu.Lock()
	if !a.hw.fallback.IsZero() {
		v.Fallback, v.FallbackAt = a.hw.reason, a.hw.fallback.UTC().Format(time.RFC3339)
	}
	a.hw.mu.Unlock()
	if a.kiosk != nil {
		g := a.kiosk.GPU()
		v.GPU, v.GPUCrashes, v.GPURecoveries = g.Mode, g.Crashes, g.Recoveries
		if !g.LostAt.IsZero() {
			v.GPULostAt = g.LostAt.UTC().Format(time.RFC3339)
		}
		if g.Mode == kiosk.GPUSoftware || g.Mode == kiosk.GPULost {
			v.HW = false
		}
	}
	return v
}
