package netcfg

import (
	"encoding/json"
	"os"
	"time"
)

// Locations shared by the agent and the root helper.
const (
	// HelperPath is the root helper the agent may run with sudo (installed root-owned by the
	// installer; the agent cannot change it).
	HelperPath = "/usr/lib/opensignage-agent/opensignage-netctl"
	// HelperProtocol is the version of the helper's commands; the agent asks for it and asks
	// to run the installer again when the helper is older.
	HelperProtocol = 1

	// RunDir holds a change until it is confirmed: it is gone after a reboot, so the device
	// starts with the previous settings.
	RunDir = "/run/opensignage-net"
	// EtcDir holds the confirmed wireless settings (root only) and summaries (readable).
	EtcDir = "/etc/opensignage/network"
	// StateDir keeps copies of system files changed by an unconfirmed change (restored after
	// a reboot without confirmation).
	StateDir = "/var/lib/opensignage-net"
	// PendingFile describes the unconfirmed change (readable by the agent).
	PendingFile = RunDir + "/pending.json"
)

// Pending is a change that waits for confirmation (a new login to the client WebGUI).
type Pending struct {
	Since      time.Time `json:"since"`
	Interfaces []string  `json:"interfaces"`
}

// ReadPending reads the pending change (nil = none).
func ReadPending(path string) *Pending {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var p Pending
	if json.Unmarshal(b, &p) != nil || len(p.Interfaces) == 0 {
		return nil
	}
	return &p
}

// Summary is the configuration without its secret (stored readable next to the files).
func (c Config) Summary() Config {
	out := c
	if c.Wifi != nil {
		w := *c.Wifi
		w.Passphrase = ""
		out.Wifi = &w
	}
	if c.Static != nil {
		s := *c.Static
		s.DNS = append([]string(nil), c.Static.DNS...)
		out.Static = &s
	}
	return out
}

// ReadSummary reads a stored summary (nil = none).
func ReadSummary(path string) *Config {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var c Config
	if json.Unmarshal(b, &c) != nil || c.Interface == "" {
		return nil
	}
	return &c
}
