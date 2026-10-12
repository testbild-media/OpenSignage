// Package display controls the physical output (resolution, refresh rate, rotation)
// behind a Provider interface: X11 via xrandr on Linux, a fake for tests and a
// "none" provider where display control is not available.
package display

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"opensignage/internal/agent/wire"
)

// Mode is a resolution with a refresh rate.
type Mode struct {
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	Refresh   float64 `json:"refresh"`
	Preferred bool    `json:"preferred,omitempty"`
	Current   bool    `json:"current,omitempty"`
	XName     string  `json:"-"` // provider-specific mode name (xrandr)
}

// Label formats a mode as "1920×1080 @ 60.00 Hz".
func (m Mode) Label() string {
	return fmt.Sprintf("%d×%d @ %.2f Hz", m.Width, m.Height, m.Refresh)
}

// Output is one connected screen.
type Output struct {
	Name      string  `json:"name"`
	Connected bool    `json:"connected"`
	Primary   bool    `json:"primary"`
	Modes     []Mode  `json:"modes"`
	Width     int     `json:"width"`  // current (unrotated) mode width, 0 when off
	Height    int     `json:"height"` // current mode height
	Refresh   float64 `json:"refresh"`
	Rotation  int     `json:"rotation"` // degrees clockwise
}

// ErrUnavailable means display control is not possible here (no X, not Linux).
var ErrUnavailable = errors.New("display control not available")

// Provider controls the output.
type Provider interface {
	// Name identifies the implementation ("x11", "fake", "none").
	Name() string
	// Outputs lists outputs with their modes.
	Outputs(ctx context.Context) ([]Output, error)
	// Apply sets the mode on the primary output. A failure leaves the previous mode.
	Apply(ctx context.Context, d wire.Display) error
}

// Primary returns the output to control (primary connected, else first connected).
func Primary(outs []Output) (Output, bool) {
	for _, o := range outs {
		if o.Connected && o.Primary {
			return o, true
		}
	}
	for _, o := range outs {
		if o.Connected {
			return o, true
		}
	}
	return Output{}, false
}

// Info converts the current mode of the primary output into the status format; the
// reported width/height are those the content sees (after rotation).
func Info(ctx context.Context, p Provider) (wire.DisplayInfo, bool) {
	outs, err := p.Outputs(ctx)
	if err != nil {
		return wire.DisplayInfo{}, false
	}
	o, ok := Primary(outs)
	if !ok || o.Width == 0 {
		return wire.DisplayInfo{}, false
	}
	return info(o), true
}

func info(o Output) wire.DisplayInfo {
	w, h := o.Width, o.Height
	if o.Rotation == 90 || o.Rotation == 270 {
		w, h = h, w
	}
	return wire.DisplayInfo{Output: o.Name, Width: w, Height: h, Refresh: o.Refresh, Rotation: o.Rotation}
}

// Report is Info plus the modes the screen offers (status reports).
// A connected screen that is switched off still reports its modes.
func Report(ctx context.Context, p Provider) (wire.DisplayInfo, bool) {
	outs, err := p.Outputs(ctx)
	if err != nil {
		return wire.DisplayInfo{}, false
	}
	o, ok := Primary(outs)
	if !ok {
		return wire.DisplayInfo{}, false
	}
	var d wire.DisplayInfo
	if o.Width > 0 {
		d = info(o)
	} else {
		d.Output = o.Name
	}
	d.Modes = Modes(o)
	return d, o.Width > 0 || len(d.Modes) > 0
}

// Bounds of the compact mode list in status reports.
const (
	maxModeGroups   = 64
	maxRatesPerMode = 16
)

// Modes groups the reported modes by resolution in the screen's order (largest first
// with xrandr), refresh rates rounded to 0.01 Hz without duplicates.
func Modes(o Output) []wire.DisplayModes {
	var out []wire.DisplayModes
	index := map[[2]int]int{}
	for _, m := range o.Modes {
		if m.Width <= 0 || m.Height <= 0 || m.Refresh <= 0 {
			continue
		}
		key := [2]int{m.Width, m.Height}
		i, ok := index[key]
		if !ok {
			if len(out) >= maxModeGroups {
				continue
			}
			i = len(out)
			index[key] = i
			out = append(out, wire.DisplayModes{Width: m.Width, Height: m.Height, Refresh: []float64{}})
		}
		g := &out[i]
		r := math.Round(m.Refresh*100) / 100
		dup := false
		for _, have := range g.Refresh {
			if math.Abs(have-r) < 0.005 {
				dup = true
				break
			}
		}
		if !dup && len(g.Refresh) < maxRatesPerMode {
			g.Refresh = append(g.Refresh, r)
		}
		if m.Preferred && g.Preferred == 0 {
			g.Preferred = r
		}
		if m.Current {
			g.Current = r
		}
	}
	return out
}

// Matches reports whether the output already shows the requested setting.
func Matches(o Output, d wire.Display) bool {
	if o.Rotation != NormRotation(d.Rotation) {
		return false
	}
	if d.IsAuto() {
		// Automatic = the screen's preferred mode (when it reports one).
		for _, m := range o.Modes {
			if m.Preferred {
				return m.Current
			}
		}
		return true
	}
	if o.Width != d.Width || o.Height != d.Height {
		return false
	}
	return d.Refresh <= 0 || math.Abs(o.Refresh-d.Refresh) < 0.5
}

// NormRotation maps any value onto 0/90/180/270.
func NormRotation(r int) int {
	r = ((r % 360) + 360) % 360
	switch {
	case r < 45 || r >= 315:
		return 0
	case r < 135:
		return 90
	case r < 225:
		return 180
	}
	return 270
}

// Validate checks a requested setting.
func Validate(d wire.Display) error {
	if d.IsAuto() {
		if d.Width != 0 || d.Height != 0 {
			return errors.New("width and height must both be set")
		}
	} else if d.Width < 320 || d.Height < 200 || d.Width > 7680 || d.Height > 4320 {
		return fmt.Errorf("resolution %dx%d out of range (320x200 … 7680x4320)", d.Width, d.Height)
	}
	if d.Refresh < 0 || d.Refresh > 240 {
		return fmt.Errorf("refresh rate %.2f out of range", d.Refresh)
	}
	switch d.Rotation {
	case 0, 90, 180, 270:
	default:
		return fmt.Errorf("rotation %d must be 0, 90, 180 or 270", d.Rotation)
	}
	return nil
}

// ---------------------------------------------------------------- none

// None is used where no display control exists.
type None struct{}

// Name implements Provider.
func (None) Name() string { return "none" }

// Outputs implements Provider.
func (None) Outputs(context.Context) ([]Output, error) { return nil, ErrUnavailable }

// Apply implements Provider.
func (None) Apply(context.Context, wire.Display) error { return ErrUnavailable }

// ---------------------------------------------------------------- fake

// Fake is an in-memory provider for tests.
type Fake struct {
	mu      sync.Mutex
	Out     Output
	Fail    error
	Applied []wire.Display
}

// NewFake returns a fake 1920×1080 output.
func NewFake() *Fake {
	return &Fake{Out: Output{Name: "FAKE-1", Connected: true, Primary: true, Width: 1920, Height: 1080, Refresh: 60,
		Modes: []Mode{{Width: 1920, Height: 1080, Refresh: 60, Preferred: true, Current: true}, {Width: 1280, Height: 720, Refresh: 60}}}}
}

// Name implements Provider.
func (f *Fake) Name() string { return "fake" }

// Outputs implements Provider.
func (f *Fake) Outputs(context.Context) ([]Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []Output{f.Out}, nil
}

// Apply implements Provider.
func (f *Fake) Apply(_ context.Context, d wire.Display) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := Validate(d); err != nil {
		return err
	}
	if f.Fail != nil {
		return f.Fail
	}
	f.Applied = append(f.Applied, d)
	if !d.IsAuto() {
		f.Out.Width, f.Out.Height = d.Width, d.Height
		if d.Refresh > 0 {
			f.Out.Refresh = d.Refresh
		}
	}
	f.Out.Rotation = NormRotation(d.Rotation)
	return nil
}
