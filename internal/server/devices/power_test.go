package devices

import (
	"testing"
	"time"

	"opensignage/internal/contracts"
)

// A weak power supply warns while the Pi reports under-voltage and for a while after the
// last event; devices without power data never warn.
func TestPowerLow(t *testing.T) {
	now := time.Date(2026, 10, 11, 21, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		p    *contracts.StatusPower
		want bool
	}{
		{"no data", nil, false},
		{"ok", &contracts.StatusPower{}, false},
		{"now", &contracts.StatusPower{UnderVoltage: true, Events: 3}, true},
		{"recent", &contracts.StatusPower{Events: 1, Last: now.Add(-5 * time.Minute).Format(time.RFC3339)}, true},
		{"old", &contracts.StatusPower{Events: 1, Last: now.Add(-time.Hour).Format(time.RFC3339)}, false},
	}
	for _, c := range cases {
		if got := powerLow(contracts.DeviceStatus{Power: c.p}, now); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	if alertSeverity[AlertPowerLow] != SeverityWarning {
		t.Fatal("power_low is a warning")
	}
}
