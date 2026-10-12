package display

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"opensignage/internal/agent/wire"
)

// Runner executes a command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// X11 controls the output with xrandr (and cvt for forced modes).
type X11 struct {
	Display string
	Run     Runner

	mu        sync.Mutex
	lastProbe time.Time
}

// Probing the screen (xrandr --query) makes the X server read the EDID again over DDC and log
// every mode: done for every status report (30 s) that grew Xorg.0.log by ~14 MB a day. Status
// queries read the X server's current state instead (xrandr --current; a hot-plugged screen
// is probed by the X server itself). A real probe runs at the first query, before a mode is
// applied, when no screen is connected (at most every probeGap) and every probeEvery.
const (
	probeEvery = 6 * time.Hour
	probeGap   = time.Minute
)

// NewX11 returns an xrandr provider for the given X display.
func NewX11(display string) *X11 {
	x := &X11{Display: display}
	x.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = append(os.Environ(), "DISPLAY="+x.Display, "LC_ALL=C")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return string(out), nil
	}
	return x
}

// Name implements Provider.
func (x *X11) Name() string { return "x11" }

// Outputs implements Provider.
func (x *X11) Outputs(ctx context.Context) ([]Output, error) {
	x.mu.Lock()
	since := time.Since(x.lastProbe)
	probed := !x.lastProbe.IsZero()
	x.mu.Unlock()
	if probed && since < probeEvery {
		out, err := x.Run(ctx, "xrandr", "--current")
		if err == nil {
			outs := ParseXrandr(out)
			if _, ok := Primary(outs); ok || since < probeGap {
				return outs, nil
			}
		}
	}
	return x.probe(ctx)
}

// probe asks the X server to probe the outputs (EDID, connected screens).
func (x *X11) probe(ctx context.Context) ([]Output, error) {
	out, err := x.Run(ctx, "xrandr", "--query")
	if err != nil {
		return nil, err
	}
	x.mu.Lock()
	x.lastProbe = time.Now()
	x.mu.Unlock()
	return ParseXrandr(out), nil
}

var (
	outputRe = regexp.MustCompile(`^(\S+) (connected|disconnected)( primary)?(?: (\d+)x(\d+)\+(-?\d+)\+(-?\d+))?(?: (normal|left|inverted|right))?`)
	modeRe   = regexp.MustCompile(`^\s+((\d+)x(\d+)\S*)\s+(.*)$`)
	rateRe   = regexp.MustCompile(`^(\d+(?:\.\d+)?)([*+ ]*)$`)
)

// ParseXrandr parses `xrandr --query` output.
func ParseXrandr(s string) []Output {
	var outs []Output
	var cur *Output
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := sc.Text()
		if m := outputRe.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "Screen ") {
			outs = append(outs, Output{Name: m[1], Connected: m[2] == "connected", Primary: m[3] != ""})
			cur = &outs[len(outs)-1]
			cur.Rotation = rotationFromX(m[8])
			continue
		}
		mm := modeRe.FindStringSubmatch(line)
		if mm == nil || cur == nil {
			continue
		}
		w, _ := strconv.Atoi(mm[2])
		h, _ := strconv.Atoi(mm[3])
		if strings.HasSuffix(strings.SplitN(mm[1], "_", 2)[0], "i") {
			continue // interlaced modes are not offered
		}
		tokens := strings.Fields(mm[4])
		// Glue standalone "+" / "*" flags to the preceding rate.
		var rates []string
		for _, t := range tokens {
			if strings.Trim(t, "*+") == "" && len(rates) > 0 {
				rates[len(rates)-1] += t
				continue
			}
			rates = append(rates, t)
		}
		for _, r := range rates {
			rm := rateRe.FindStringSubmatch(r)
			if rm == nil {
				continue
			}
			v, err := strconv.ParseFloat(rm[1], 64)
			if err != nil {
				continue
			}
			mode := Mode{Width: w, Height: h, Refresh: v, Preferred: strings.Contains(rm[2], "+"),
				Current: strings.Contains(rm[2], "*"), XName: mm[1]}
			cur.Modes = append(cur.Modes, mode)
			if mode.Current {
				cur.Width, cur.Height, cur.Refresh = w, h, v
			}
		}
	}
	return outs
}

func rotationFromX(s string) int {
	switch s {
	case "right":
		return 90
	case "inverted":
		return 180
	case "left":
		return 270
	}
	return 0
}

func rotationToX(deg int) string {
	switch NormRotation(deg) {
	case 90:
		return "right"
	case 180:
		return "inverted"
	case 270:
		return "left"
	}
	return "normal"
}

// Apply implements Provider: an exact reported mode is set directly; a mode the screen
// does not report is generated with cvt (only when d.Custom is set). If the result does
// not match, the previous mode is restored and an error returned.
func (x *X11) Apply(ctx context.Context, d wire.Display) error {
	if err := Validate(d); err != nil {
		return err
	}
	outs, err := x.probe(ctx) // the modes the screen offers right now
	if err != nil {
		return err
	}
	o, ok := Primary(outs)
	if !ok {
		return fmt.Errorf("no connected output")
	}
	if Matches(o, d) {
		return nil
	}
	rot := rotationToX(d.Rotation)
	var args []string
	switch {
	case d.IsAuto(): // back to the screen's preferred mode
		args = []string{"--output", o.Name, "--auto", "--rotate", rot}
	default:
		name, rate, found := findMode(o, d)
		if !found {
			if !d.Custom {
				return fmt.Errorf("mode %dx%d@%.2f is not reported by the screen (use a custom mode to force it)", d.Width, d.Height, d.Refresh)
			}
			name, err = x.addCustomMode(ctx, o.Name, d)
			if err != nil {
				return err
			}
			args = []string{"--output", o.Name, "--mode", name, "--rotate", rot}
		} else {
			args = []string{"--output", o.Name, "--mode", name, "--rate", strconv.FormatFloat(rate, 'f', 2, 64), "--rotate", rot}
		}
	}
	slog.Info("applying display mode", "output", o.Name, "args", strings.Join(args, " "))
	_, applyErr := x.Run(ctx, "xrandr", args...)
	if applyErr == nil {
		if after, err := x.Outputs(ctx); err == nil {
			if na, ok := Primary(after); ok && !Matches(na, d) {
				applyErr = fmt.Errorf("screen reports %dx%d@%.2f after applying", na.Width, na.Height, na.Refresh)
			}
		}
	}
	if applyErr != nil {
		x.restore(ctx, o)
		return applyErr
	}
	return nil
}

func (x *X11) restore(ctx context.Context, prev Output) {
	if prev.Width == 0 {
		return
	}
	name, rate, ok := findMode(prev, wire.Display{Width: prev.Width, Height: prev.Height, Refresh: prev.Refresh})
	if !ok {
		return
	}
	if _, err := x.Run(ctx, "xrandr", "--output", prev.Name, "--mode", name, "--rate", strconv.FormatFloat(rate, 'f', 2, 64),
		"--rotate", rotationToX(prev.Rotation)); err != nil {
		slog.Error("restoring previous display mode failed", "err", err)
	} else {
		slog.Info("previous display mode restored", "mode", fmt.Sprintf("%dx%d@%.2f", prev.Width, prev.Height, prev.Refresh))
	}
}

func findMode(o Output, d wire.Display) (name string, rate float64, ok bool) {
	best := -1.0
	var xname string
	for _, m := range o.Modes {
		if m.Width != d.Width || m.Height != d.Height {
			continue
		}
		diff := 0.0
		if d.Refresh > 0 {
			diff = math.Abs(m.Refresh - d.Refresh)
			if diff >= 0.5 {
				continue
			}
		} else if !m.Preferred && !m.Current {
			diff = 0.1 // prefer the preferred/current rate when none was requested
		}
		if best < 0 || diff < best {
			best, rate, ok, xname = diff, m.Refresh, true, m.XName
		}
	}
	if !ok {
		return "", 0, false
	}
	if xname == "" {
		xname = fmt.Sprintf("%dx%d", d.Width, d.Height)
	}
	return xname, rate, true
}

var modelineRe = regexp.MustCompile(`Modeline\s+"([^"]+)"\s+(.+)$`)

func (x *X11) addCustomMode(ctx context.Context, output string, d wire.Display) (string, error) {
	refresh := d.Refresh
	if refresh <= 0 {
		refresh = 60
	}
	out, err := x.Run(ctx, "cvt", strconv.Itoa(d.Width), strconv.Itoa(d.Height), strconv.FormatFloat(refresh, 'f', 2, 64))
	if err != nil {
		return "", err
	}
	var name string
	var params []string
	for _, line := range strings.Split(out, "\n") {
		if m := modelineRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			name, params = m[1], strings.Fields(m[2])
		}
	}
	if name == "" || len(params) < 9 {
		return "", fmt.Errorf("cvt returned no modeline")
	}
	// CVT rounds the width to a multiple of 8 (1366 → 1368). Keep the generated timings
	// but display exactly the requested size when it fits before the sync pulse.
	fix := func(idx, want int) {
		got, err1 := strconv.Atoi(params[idx])
		syncStart, err2 := strconv.Atoi(params[idx+1])
		if err1 == nil && err2 == nil && got != want && want > 0 && want <= syncStart {
			params[idx] = strconv.Itoa(want)
		}
	}
	fix(1, d.Width)
	fix(5, d.Height)
	name = fmt.Sprintf("%sx%s_%.2f", params[1], params[5], refresh)
	// --newmode fails harmlessly when the mode already exists.
	if _, err := x.Run(ctx, "xrandr", append([]string{"--newmode", name}, params...)...); err != nil {
		slog.Debug("xrandr --newmode", "err", err)
	}
	if _, err := x.Run(ctx, "xrandr", "--addmode", output, name); err != nil {
		slog.Debug("xrandr --addmode", "err", err)
	}
	return name, nil
}
