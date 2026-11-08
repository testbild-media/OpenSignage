package agent

import (
	"context"
	"testing"

	"opensignage/internal/agent/display"
	"opensignage/internal/agent/wire"
)

// A screen that is connected but whose output is off (the X server switched it off when the
// screen went away for a moment) is switched on again with the saved mode.
func TestScreenWatchSwitchesTheOutputOnAgain(t *testing.T) {
	fake := display.NewFake()
	a := &Agent{display: fake}
	a.device.Display = wire.Display{Width: 1280, Height: 720, Refresh: 60}
	ctx := context.Background()

	a.checkScreen(ctx) // on: nothing to do
	if len(fake.Applied) != 0 || !a.screenActive() {
		t.Fatalf("screen on: applied %v", fake.Applied)
	}
	fake.Out.Width, fake.Out.Height = 0, 0 // the output went off
	a.checkScreen(ctx)
	if len(fake.Applied) != 1 || fake.Applied[0] != a.device.Display {
		t.Fatalf("output off: applied %v", fake.Applied)
	}
	if fake.Out.Width != 1280 || !a.screenActive() {
		t.Fatalf("not on again: %+v (%s)", fake.Out, a.screen)
	}
	// Automatic mode: switched on with the screen's own mode.
	a.device.Display = wire.Display{}
	fake.Out.Width, fake.Out.Height = 0, 0
	a.checkScreen(ctx)
	if len(fake.Applied) != 2 || !a.screenActive() {
		t.Fatalf("auto: applied %v, screen %s", fake.Applied, a.screen)
	}
	// No screen connected: nothing to switch on, and the player is not restarted for drawing nothing.
	fake.Out.Connected = false
	a.checkScreen(ctx)
	if len(fake.Applied) != 2 || a.screenActive() {
		t.Fatalf("no screen: applied %v, active %v", fake.Applied, a.screenActive())
	}
}

func TestScreenState(t *testing.T) {
	for _, c := range []struct {
		outs []display.Output
		want string
	}{
		{nil, screenNone},
		{[]display.Output{{Name: "HDMI-1"}}, screenNone},
		{[]display.Output{{Name: "HDMI-1", Connected: true}}, screenOff},
		{[]display.Output{{Name: "HDMI-1", Connected: true, Width: 1920, Height: 1080}}, screenOn},
	} {
		if _, got := screenState(c.outs); got != c.want {
			t.Errorf("%+v: %s, want %s", c.outs, got, c.want)
		}
	}
}
