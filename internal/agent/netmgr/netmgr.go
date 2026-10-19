// Package netmgr is the agent's side of the network settings: it reads the network state
// without privileges and runs the root helper (opensignage-netctl) with sudo for changes,
// scans and firmware.
package netmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"opensignage/internal/agent/netcfg"
)

// ErrHelperMissing: the installer of this version has not run yet (players updated from an
// older version get the helper and its sudo rule with the next run of the installer).
var ErrHelperMissing = errors.New("network helper not installed")

// FirmwareHint is a network firmware the kernel misses.
type FirmwareHint struct {
	File    string `json:"file"`
	Package string `json:"package"`
}

// Manager reads and changes the network configuration.
type Manager struct {
	Env netcfg.Env
	// Root is a directory prefix of the files the helper writes (tests); "" = /.
	Root string
	// Sudo runs the helper as root with the arguments and stdin (nil = sudo -n).
	Sudo func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)

	mu        sync.Mutex
	checked   time.Time
	helperErr error
	cache     netcfg.Status
	cachedAt  time.Time
}

func (m *Manager) path(p string) string {
	if m.Root == "" {
		return p
	}
	return filepath.Join(m.Root, filepath.FromSlash(p))
}

func (m *Manager) sudo(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if m.Sudo != nil {
		return m.Sudo(ctx, stdin, args...)
	}
	cmd := exec.CommandContext(ctx, "sudo", append([]string{"-n", netcfg.HelperPath}, args...)...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.Bytes(), err
}

type reply struct {
	OK    bool `json:"ok"`
	Error *struct {
		Field   string `json:"field"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"error"`
	Protocol int            `json:"protocol"`
	Networks []netcfg.AP    `json:"networks"`
	Missing  []FirmwareHint `json:"missing"`
}

// call runs a helper command and decodes its answer.
func (m *Manager) call(ctx context.Context, timeout time.Duration, stdin []byte, args ...string) (*reply, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, runErr := m.sudo(ctx, stdin, args...)
	var r reply
	if err := json.Unmarshal(bytes.TrimSpace(out), &r); err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrHelperMissing, runErr)
		}
		return nil, fmt.Errorf("unreadable helper answer: %q", strings.TrimSpace(string(out)))
	}
	if r.Error != nil {
		if r.Error.Field != "" {
			return nil, &netcfg.InputError{Field: r.Error.Field, Reason: r.Error.Reason}
		}
		return nil, errors.New(r.Error.Message)
	}
	return &r, nil
}

// HelperError is nil when changes are possible (the helper is installed, allowed via sudo
// and speaks this protocol); checked at most every minute.
func (m *Manager) HelperError(ctx context.Context) error {
	m.mu.Lock()
	if !m.checked.IsZero() && time.Since(m.checked) < time.Minute {
		err := m.helperErr
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	err := ErrHelperMissing
	if r, cerr := m.call(ctx, 10*time.Second, nil, "version"); cerr == nil {
		if r.Protocol >= netcfg.HelperProtocol {
			err = nil
		} else {
			err = fmt.Errorf("%w (protocol %d)", ErrHelperMissing, r.Protocol)
		}
	}
	m.mu.Lock()
	m.checked, m.helperErr = time.Now(), err
	m.mu.Unlock()
	return err
}

// Status is the current state (cached for a few seconds: the status report and the WebGUI
// may ask at the same time).
func (m *Manager) Status(ctx context.Context) netcfg.Status {
	m.mu.Lock()
	if time.Since(m.cachedAt) < 5*time.Second {
		st := m.cache
		m.mu.Unlock()
		return st
	}
	m.mu.Unlock()
	st := m.Env.Inspect(ctx)
	m.mu.Lock()
	m.cache, m.cachedAt = st, time.Now()
	m.mu.Unlock()
	return st
}

// Refresh drops the cached state (after a change).
func (m *Manager) Refresh() {
	m.mu.Lock()
	m.cachedAt = time.Time{}
	m.mu.Unlock()
}

// Pending is the unconfirmed change (nil = none).
func (m *Manager) Pending() *netcfg.Pending { return netcfg.ReadPending(m.path(netcfg.PendingFile)) }

// Configured is the stored configuration of the interface made in the WebGUI (the pending
// one first; nil = none, the system's own configuration applies).
func (m *Manager) Configured(iface string) *netcfg.Config {
	if c := netcfg.ReadSummary(m.path(netcfg.RunDir + "/" + iface + ".json")); c != nil {
		return c
	}
	return netcfg.ReadSummary(m.path(netcfg.EtcDir + "/" + iface + ".json"))
}

// Apply hands the configuration to the helper: active at once, until the next reboot.
func (m *Manager) Apply(ctx context.Context, c netcfg.Config) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = m.call(ctx, 4*time.Minute, b, "apply")
	m.Refresh()
	return err
}

// Confirm makes the pending change permanent.
func (m *Manager) Confirm(ctx context.Context) error {
	_, err := m.call(ctx, 2*time.Minute, nil, "confirm")
	m.Refresh()
	return err
}

// Cleanup restores files after a reboot without confirmation (only when there is
// something to restore, so sudo is not run on every start).
func (m *Manager) Cleanup(ctx context.Context) error {
	if _, err := os.Stat(m.path(netcfg.StateDir + "/unconfirmed")); err != nil {
		return nil
	}
	if m.Pending() != nil {
		return nil
	}
	_, err := m.call(ctx, time.Minute, nil, "cleanup")
	return err
}

// Scan lists the wireless networks around.
func (m *Manager) Scan(ctx context.Context, iface, country string) ([]netcfg.AP, error) {
	args := []string{"scan", iface}
	if country != "" {
		args = append(args, country)
	}
	r, err := m.call(ctx, time.Minute, nil, args...)
	if err != nil {
		return nil, err
	}
	return r.Networks, nil
}

// Firmware lists missing network firmware.
func (m *Manager) Firmware(ctx context.Context) ([]FirmwareHint, error) {
	r, err := m.call(ctx, 20*time.Second, nil, "firmware")
	if err != nil {
		return nil, err
	}
	return r.Missing, nil
}

// InstallFirmware installs a firmware package (takes minutes).
func (m *Manager) InstallFirmware(ctx context.Context, pkg string) error {
	_, err := m.call(ctx, 16*time.Minute, nil, "install-firmware", pkg)
	return err
}
