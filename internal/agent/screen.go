package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"opensignage/internal/agent/display"
)

// Screen states seen by the screen watch.
const (
	screenNone = "none" // no screen connected
	screenOff  = "off"  // a screen is connected, but its output shows nothing (no mode)
	screenOn   = "on"   // the output shows the picture
)

// screenCheckEvery is how often the screen watch looks at the outputs.
const screenCheckEvery = 30 * time.Second

// screenState is the state of the primary output.
func screenState(outs []display.Output) (display.Output, string) {
	o, ok := display.Primary(outs)
	switch {
	case !ok:
		return o, screenNone
	case o.Width == 0 || o.Height == 0:
		return o, screenOff
	}
	return o, screenOn
}

// screenWatch looks at the screen every 30 s. When a screen goes away and comes back (an
// HDMI splitter syncing again, a TV switched off and on), the X server can leave its output
// switched off; without a desktop nothing switches it on again, and the screen stayed black
// until a restart. The watch switches it on with the saved mode and logs every change, so a
// black screen can be explained afterwards.
func (a *Agent) screenWatch(ctx context.Context) {
	t := time.NewTicker(screenCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.checkScreen(ctx)
	}
}

func (a *Agent) checkScreen(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	outs, err := a.display.Outputs(cctx)
	if err != nil {
		return // no X server (yet) or no display control here
	}
	o, state := screenState(outs)
	a.mu.Lock()
	prev := a.screen
	a.screen = state
	saved := a.device.Display
	a.mu.Unlock()
	if prev != "" && prev != state {
		switch state {
		case screenNone:
			slog.Warn("screen disconnected")
		case screenOff:
			slog.Warn("screen connected, but its output is off", "output", o.Name)
		case screenOn:
			slog.Info("screen shows the picture again", "output", o.Name, "width", o.Width, "height", o.Height)
		}
	}
	if state != screenOff {
		return
	}
	slog.Info("switching the screen output on again", "output", o.Name)
	if err := a.applyDisplay(cctx, saved); err != nil && !errors.Is(err, display.ErrUnavailable) {
		slog.Error("switching the screen output on failed", "output", o.Name, "err", err)
		return
	}
	if after, err := a.display.Outputs(cctx); err == nil {
		if _, st := screenState(after); st != state {
			a.mu.Lock()
			a.screen = st
			a.mu.Unlock()
			if st == screenOn {
				slog.Info("screen shows the picture again", "output", o.Name)
			}
		}
	}
}

// screenActive: a screen shows the picture (or the state is not known yet). The player is
// only restarted for drawing nothing while it does.
func (a *Agent) screenActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.screen == "" || a.screen == screenOn
}
