package display

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"opensignage/internal/agent/wire"
)

const sample = `Screen 0: minimum 320 x 200, current 1280 x 800, maximum 16384 x 16384
Virtual-1 connected primary 1280x800+0+0 (normal left inverted right x axis y axis) 0mm x 0mm
   1280x800      59.81*+
   1920x1080     60.00 +  59.96    59.93
   1366x768      59.79    60.00
   1920x1080i    60.00
   1024x768_75.00  74.90
HDMI-1 disconnected (normal left inverted right x axis y axis)
`

func TestParseXrandr(t *testing.T) {
	outs := ParseXrandr(sample)
	if len(outs) != 2 {
		t.Fatalf("outputs = %d", len(outs))
	}
	o := outs[0]
	if o.Name != "Virtual-1" || !o.Connected || !o.Primary || o.Width != 1280 || o.Height != 800 || o.Refresh != 59.81 {
		t.Fatalf("output parsed wrong: %+v", o)
	}
	if len(o.Modes) != 7 {
		t.Fatalf("modes = %d: %+v", len(o.Modes), o.Modes)
	}
	if !o.Modes[1].Preferred || o.Modes[1].Refresh != 60 || o.Modes[6].XName != "1024x768_75.00" {
		t.Fatalf("flags/names wrong: %+v", o.Modes)
	}
	rotated := ParseXrandr("DP-1 connected 1080x1920+0+0 left (normal left inverted right x axis y axis) 1mm x 1mm\n   1920x1080     60.00*+\n")
	if rotated[0].Rotation != 270 || rotated[0].Width != 1920 {
		t.Fatalf("rotation parsed wrong: %+v", rotated[0])
	}
}

type fakeX struct {
	current string
	calls   []string
	failSet bool
}

func (f *fakeX) run(_ context.Context, name string, args ...string) (string, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	switch {
	case name == "cvt" && args[0] == "1366":
		return "Modeline \"1368x768_60.00\"   85.25  1368 1440 1576 1784  768 771 781 798 -hsync +vsync\n", nil
	case name == "cvt":
		return "# 1600x900 59.95 Hz (CVT 1.44M9) hsync: 55.99 kHz; pclk: 118.25 MHz\nModeline \"1600x900_60.00\"  118.25  1600 1696 1856 2112  900 903 908 934 -hsync +vsync\n", nil
	case len(args) > 0 && (args[0] == "--query" || args[0] == "--current"):
		return f.current, nil
	case strings.Contains(call, "--mode 1366x768"):
		if f.failSet {
			return "", errors.New("xrandr: Configure crtc 0 failed")
		}
		f.current = strings.Replace(sample, "1280x800+0+0", "1366x768+0+0", 1)
		f.current = strings.Replace(f.current, "59.81*+", "59.81 +", 1)
		f.current = strings.Replace(f.current, "1366x768      59.79    60.00\n", "1366x768      59.79    60.00*\n", 1)
	}
	return "", nil
}

func TestX11Apply(t *testing.T) {
	fx := &fakeX{current: sample}
	x := &X11{Run: fx.run}
	if err := x.Apply(context.Background(), wire.Display{Width: 1366, Height: 768, Refresh: 60}); err != nil {
		t.Fatal(err)
	}
	if !contains(fx.calls, "xrandr --output Virtual-1 --mode 1366x768 --rate 60.00 --rotate normal") {
		t.Fatalf("calls: %v", fx.calls)
	}

	// Unreported mode without "custom" is refused.
	fx = &fakeX{current: sample}
	x.Run = fx.run
	if err := x.Apply(context.Background(), wire.Display{Width: 1600, Height: 900, Refresh: 60}); err == nil {
		t.Fatal("unreported mode must need custom")
	}
	// With custom: cvt + newmode + addmode.
	err := x.Apply(context.Background(), wire.Display{Width: 1600, Height: 900, Refresh: 60, Custom: true})
	if err == nil {
		t.Fatal("fake does not switch to 1600x900, verification must fail")
	}
	for _, want := range []string{"cvt 1600 900 60.00", "xrandr --newmode 1600x900_60.00 118.25 1600 1696 1856 2112 900 903 908 934 -hsync +vsync",
		"xrandr --addmode Virtual-1 1600x900_60.00", "xrandr --output Virtual-1 --mode 1600x900_60.00 --rotate normal",
		"xrandr --output Virtual-1 --mode 1280x800 --rate 59.81 --rotate normal"} {
		if !contains(fx.calls, want) {
			t.Errorf("missing call %q in %v", want, fx.calls)
		}
	}

	// CVT rounds 1366 to 1368: the modeline is corrected to the requested width.
	fx = &fakeX{current: strings.Replace(sample, "   1366x768      59.79    60.00\n", "", 1)}
	x.Run = fx.run
	_ = x.Apply(context.Background(), wire.Display{Width: 1366, Height: 768, Refresh: 60, Custom: true})
	if !contains(fx.calls, "xrandr --newmode 1366x768_60.00 85.25 1366 1440 1576 1784 768 771 781 798 -hsync +vsync") {
		t.Fatalf("1366 not corrected: %v", fx.calls)
	}

	// A failing xrandr restores the previous mode.
	fx = &fakeX{current: sample, failSet: true}
	x.Run = fx.run
	if err := x.Apply(context.Background(), wire.Display{Width: 1366, Height: 768, Refresh: 60}); err == nil {
		t.Fatal("expected error")
	}
	if fx.calls[len(fx.calls)-1] != "xrandr --output Virtual-1 --mode 1280x800 --rate 59.81 --rotate normal" {
		t.Fatalf("previous mode not restored: %v", fx.calls)
	}
}

func TestValidateAndInfo(t *testing.T) {
	for _, bad := range []wire.Display{{Width: 100, Height: 100}, {Width: 1920}, {Rotation: 45}, {Width: 1920, Height: 1080, Refresh: 500}} {
		if Validate(bad) == nil {
			t.Errorf("%+v should be invalid", bad)
		}
	}
	f := NewFake()
	f.Apply(context.Background(), wire.Display{Rotation: 90})
	info, ok := Info(context.Background(), f)
	if !ok || info.Width != 1080 || info.Height != 1920 || info.Rotation != 90 {
		t.Fatalf("info %+v", info)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Automatic means the screen's preferred mode: a screen left in another mode does not match.
func TestAutoMatchesPreferredMode(t *testing.T) {
	auto := wire.Display{}
	pref := Output{Name: "HDMI-1", Connected: true, Width: 1920, Height: 1080, Refresh: 60,
		Modes: []Mode{{Width: 1920, Height: 1080, Refresh: 60, Preferred: true, Current: true}, {Width: 1280, Height: 720, Refresh: 60}}}
	if !Matches(pref, auto) {
		t.Fatal("preferred mode active: auto must match")
	}
	other := Output{Name: "HDMI-1", Connected: true, Width: 1280, Height: 720, Refresh: 60,
		Modes: []Mode{{Width: 1920, Height: 1080, Refresh: 60, Preferred: true}, {Width: 1280, Height: 720, Refresh: 60, Current: true}}}
	if Matches(other, auto) {
		t.Fatal("another mode active: auto must not match (xrandr --auto has to run)")
	}
	none := Output{Name: "HDMI-1", Connected: true, Width: 1280, Height: 720, Refresh: 60,
		Modes: []Mode{{Width: 1280, Height: 720, Refresh: 60, Current: true}}}
	if !Matches(none, auto) {
		t.Fatal("no preferred mode reported: keep what is there")
	}
}

// TestModes covers the compact mode list of status reports: one
// entry per resolution in the screen's order, rates without duplicates, the preferred and
// the current rate marked; interlaced modes are not offered.
func TestModes(t *testing.T) {
	o := ParseXrandr(sample)[0]
	got := Modes(o)
	if len(got) != 4 {
		t.Fatalf("groups = %d: %+v", len(got), got)
	}
	first, fhd := got[0], got[1]
	if first.Width != 1280 || first.Height != 800 || first.Current != 59.81 || first.Preferred != 59.81 {
		t.Fatalf("current mode group: %+v", first)
	}
	if fhd.Width != 1920 || len(fhd.Refresh) != 3 || fhd.Refresh[0] != 60 || fhd.Preferred != 60 || fhd.Current != 0 {
		t.Fatalf("1920x1080 group: %+v", fhd)
	}
	if got[3].Width != 1024 || got[3].Refresh[0] != 74.9 {
		t.Fatalf("custom mode group: %+v", got[3])
	}
	dup := Output{Modes: []Mode{{Width: 800, Height: 600, Refresh: 60.004}, {Width: 800, Height: 600, Refresh: 60}, {Width: 0, Height: 600, Refresh: 60}}}
	if g := Modes(dup); len(g) != 1 || len(g[0].Refresh) != 1 {
		t.Fatalf("duplicates: %+v", g)
	}
	info, ok := Report(context.Background(), NewFake())
	if !ok || info.Width != 1920 || len(info.Modes) != 2 || info.Modes[0].Preferred != 60 {
		t.Fatalf("report: %+v", info)
	}
	if _, ok := Report(context.Background(), None{}); ok {
		t.Fatal("no display control: no report")
	}
}

// Status queries must not make the X server probe the screen (it logs every mode each time):
// only the first query, a mode change and a query without a connected screen probe.
func TestX11ProbesRarely(t *testing.T) {
	fx := &fakeX{current: sample}
	x := &X11{Run: fx.run}
	for i := 0; i < 5; i++ {
		if _, ok := Info(context.Background(), x); !ok {
			t.Fatal("info")
		}
	}
	probes := 0
	for _, c := range fx.calls {
		if c == "xrandr --query" {
			probes++
		}
	}
	if probes != 1 || fx.calls[len(fx.calls)-1] != "xrandr --current" {
		t.Fatalf("one probe, then the current state: %v", fx.calls)
	}
	fx.calls = nil
	_ = x.Apply(context.Background(), wire.Display{Width: 1366, Height: 768, Refresh: 60})
	if len(fx.calls) == 0 || fx.calls[0] != "xrandr --query" {
		t.Fatalf("a mode change probes first: %v", fx.calls)
	}
	// No screen in the current state: probed again (a screen plugged in later), but not more
	// often than once a minute.
	fx.calls, fx.current = nil, strings.ReplaceAll(sample, " connected", " disconnected")
	x.lastProbe = time.Now().Add(-2 * time.Minute)
	Info(context.Background(), x)
	Info(context.Background(), x)
	if got := strings.Join(fx.calls, ","); got != "xrandr --current,xrandr --query,xrandr --current" {
		t.Fatalf("calls without screen: %s", got)
	}
}
