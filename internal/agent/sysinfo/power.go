package sysinfo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Power watches the supply voltage where the platform reports it (Raspberry Pi: the
// firmware's under-voltage alarm as hwmon "rpi_volt"). A weak power supply or cable makes
// the Pi throttle its CPU, so videos stutter; the state is reported to the server.
type Power struct {
	mu     sync.Mutex
	alarm  string // path of the alarm file ("" = not available)
	now    bool
	events int
	last   time.Time
}

// PowerState is a snapshot for the status message.
type PowerState struct {
	UnderVoltage bool      // the alarm is active right now
	Events       int       // under-voltage events since the agent started
	Last         time.Time // start of the last event (zero when none)
}

// NewPower finds the under-voltage alarm; Available is false on other platforms.
func NewPower() *Power {
	p := &Power{}
	dirs, _ := filepath.Glob(filepath.Join(sysRoot, "class", "hwmon", "hwmon*"))
	for _, d := range dirs {
		name, err := os.ReadFile(filepath.Join(d, "name"))
		if err != nil || strings.TrimSpace(string(name)) != "rpi_volt" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "in0_lcrit_alarm")); err == nil {
			p.alarm = filepath.Join(d, "in0_lcrit_alarm")
			break
		}
	}
	return p
}

// Available reports whether the platform reports its supply voltage.
func (p *Power) Available() bool { return p != nil && p.alarm != "" }

// Check reads the alarm once and counts a new event on the rising edge.
func (p *Power) Check(now time.Time) {
	if !p.Available() {
		return
	}
	b, err := os.ReadFile(p.alarm)
	if err != nil {
		return
	}
	active := strings.TrimSpace(string(b)) == "1"
	p.mu.Lock()
	defer p.mu.Unlock()
	if active && !p.now {
		p.events++
		p.last = now
	}
	p.now = active
}

// Run polls every 2 s (the firmware updates the alarm about as often) until ctx ends.
func (p *Power) Run(ctx context.Context) {
	if !p.Available() {
		return
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	p.Check(time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			p.Check(now)
		}
	}
}

// State returns the current snapshot (false when the platform does not report it).
func (p *Power) State() (PowerState, bool) {
	if !p.Available() {
		return PowerState{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return PowerState{UnderVoltage: p.now, Events: p.events, Last: p.last}, true
}
