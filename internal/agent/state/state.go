// Package state persists the agent's runtime state as small JSON files (atomic writes,
// bounded retention). Layout below the data dir (ARCHITECTURE §6):
//
//	state/server.json      server address, pinned SPKI, device id, token (0600)
//	state/deployment.json  active / previous / pending deployment (+ cleared flag)
//	state/manifests/<id>.json  only the manifests referenced by deployment.json
//	state/live.json        last live state
//	state/device.json      device settings (display mode, language) – survive re-pairing
//	state/clock.json       server time offset
//	state/webgui.json      local client WebGUI password hash
//	cache/blobs/<sha256>   verified content files
//	cache/tmp/<sha256>.part  resumable downloads
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"opensignage/internal/agent/wire"
	"opensignage/internal/common/atomicfile"
)

// Store gives typed access to the state files.
type Store struct {
	dataDir string
	mu      sync.Mutex // serialises read-modify-write of device.json
}

// New creates the directories and returns a store.
func New(dataDir string) (*Store, error) {
	s := &Store{dataDir: dataDir}
	for _, d := range []string{s.StateDir(), s.ManifestDir(), s.BlobDir(), s.TmpDir()} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, fmt.Errorf("create %s: %w", d, err)
		}
	}
	return s, nil
}

// DataDir is the agent data directory.
func (s *Store) DataDir() string { return s.dataDir }

// StateDir holds the JSON state files.
func (s *Store) StateDir() string { return filepath.Join(s.dataDir, "state") }

// ManifestDir holds manifests/<id>.json.
func (s *Store) ManifestDir() string { return filepath.Join(s.StateDir(), "manifests") }

// CacheDir is the content cache root.
func (s *Store) CacheDir() string { return filepath.Join(s.dataDir, "cache") }

// BlobDir holds verified blobs named by SHA-256.
func (s *Store) BlobDir() string { return filepath.Join(s.CacheDir(), "blobs") }

// TmpDir holds partial downloads.
func (s *Store) TmpDir() string { return filepath.Join(s.CacheDir(), "tmp") }

func (s *Store) file(name string) string { return filepath.Join(s.StateDir(), name) }

var (
	shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	idRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// ValidSHA reports whether s is a lowercase hex SHA-256 (safe as a file name).
func ValidSHA(s string) bool { return shaRe.MatchString(s) }

// ValidID reports whether s is safe as a file name component.
func ValidID(s string) bool { return idRe.MatchString(s) }

// BlobPath returns the cache path of a verified blob.
func (s *Store) BlobPath(sha string) string { return filepath.Join(s.BlobDir(), sha) }

// PartPath returns the path of a partial download.
func (s *Store) PartPath(sha string) string { return filepath.Join(s.TmpDir(), sha+".part") }

func load(path string, v any) error {
	err := atomicfile.ReadJSON(path, v)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ---------------------------------------------------------------- server.json

// Server holds the link to the paired server.
type Server struct {
	Address     string `json:"address,omitempty"`     // host:port of the device channel
	Fingerprint string `json:"fingerprint,omitempty"` // pinned SPKI SHA-256 (hex)
	DeviceID    string `json:"deviceId,omitempty"`
	Token       string `json:"token,omitempty"`
	ServerID    string `json:"serverId,omitempty"` // from discovery (informational)
	PairedAt    string `json:"pairedAt,omitempty"`
	// ManualAddress is the address chosen in the client WebGUI. AddressSetByUser
	// distinguishes "user chose automatic discovery" from "never set" (then the
	// installer's OS_SERVER applies).
	ManualAddress    string `json:"manualAddress,omitempty"`
	AddressSetByUser bool   `json:"addressSetByUser,omitempty"`
}

// Paired reports whether credentials exist.
func (s Server) Paired() bool { return s.Token != "" && s.Address != "" && s.Fingerprint != "" }

// LoadServer reads server.json (zero value when missing).
func (s *Store) LoadServer() (Server, error) {
	var v Server
	return v, load(s.file("server.json"), &v)
}

// SaveServer writes server.json with mode 0600 (contains the device token).
func (s *Store) SaveServer(v Server) error {
	return atomicfile.WriteJSON(s.file("server.json"), v, 0o600)
}

// ---------------------------------------------------------------- deployment.json

// Ref identifies a deployment.
type Ref struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}

// Deployment is the two-phase deployment pointer.
type Deployment struct {
	Active    *Ref   `json:"active,omitempty"`
	Previous  *Ref   `json:"previous,omitempty"`
	Pending   *Ref   `json:"pending,omitempty"`
	Cleared   bool   `json:"cleared"` // explicit "show nothing" target is active
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// IDs returns the deployment ids referenced (active, previous, pending).
func (d Deployment) IDs() []string {
	var out []string
	for _, r := range []*Ref{d.Active, d.Previous, d.Pending} {
		if r != nil && r.ID != "" {
			out = append(out, r.ID)
		}
	}
	return out
}

// LoadDeployment reads deployment.json.
func (s *Store) LoadDeployment() (Deployment, error) {
	var v Deployment
	return v, load(s.file("deployment.json"), &v)
}

// SaveDeployment atomically replaces deployment.json.
func (s *Store) SaveDeployment(v Deployment) error {
	return atomicfile.WriteJSON(s.file("deployment.json"), v, 0o640)
}

// ---------------------------------------------------------------- manifests

// ManifestPath returns the file of a stored manifest.
func (s *Store) ManifestPath(id string) string { return filepath.Join(s.ManifestDir(), id+".json") }

// SaveManifest stores the raw manifest bytes exactly as received.
func (s *Store) SaveManifest(id string, raw []byte) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid deployment id %q", id)
	}
	return atomicfile.Write(s.ManifestPath(id), raw, 0o640)
}

// LoadManifest reads and parses a stored manifest.
func (s *Store) LoadManifest(id string) ([]byte, *wire.Manifest, error) {
	if !ValidID(id) {
		return nil, nil, fmt.Errorf("invalid deployment id %q", id)
	}
	raw, err := os.ReadFile(s.ManifestPath(id))
	if err != nil {
		return nil, nil, err
	}
	var m wire.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, fmt.Errorf("manifest %s: %w", id, err)
	}
	return raw, &m, nil
}

// PruneManifests deletes every stored manifest not in keep (bounded retention).
func (s *Store) PruneManifests(keep []string) {
	k := map[string]bool{}
	for _, id := range keep {
		k[id] = true
	}
	entries, err := os.ReadDir(s.ManifestDir())
	if err != nil {
		slog.Warn("list manifests failed", "err", err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		id := name[:len(name)-len(filepath.Ext(name))]
		if k[id] && filepath.Ext(name) == ".json" {
			continue
		}
		if err := atomicfile.Remove(filepath.Join(s.ManifestDir(), name)); err != nil {
			slog.Warn("remove manifest failed", "file", name, "err", err)
		}
	}
}

// ---------------------------------------------------------------- live.json

// Live is the last live state received from the server.
type Live struct {
	ReceivedAt string          `json:"receivedAt"`
	State      json.RawMessage `json:"state"`
}

// LoadLive reads live.json.
func (s *Store) LoadLive() (Live, error) {
	var v Live
	return v, load(s.file("live.json"), &v)
}

// SaveLive replaces live.json.
func (s *Store) SaveLive(v Live) error { return atomicfile.WriteJSON(s.file("live.json"), v, 0o640) }

// ---------------------------------------------------------------- device.json

// Device holds settings that belong to the physical device. They survive re-pairing
// (only Config, the server's last device.config, is reset by a wipe).
type Device struct {
	HardwareID string             `json:"hardwareId,omitempty"` // generated when /etc/machine-id is missing
	Config     *wire.DeviceConfig `json:"config,omitempty"`
	Display    wire.Display       `json:"display"` // effective display mode
	// LastServerDisplay is the display setting last received from the server. A server
	// value is applied only when it differs from this one, so a mode set locally in
	// the client WebGUI is not overwritten on every reconnect.
	LastServerDisplay *wire.Display `json:"lastServerDisplay,omitempty"`
	Language          string        `json:"language,omitempty"`
}

// LoadDevice reads device.json.
func (s *Store) LoadDevice() (Device, error) {
	var v Device
	return v, load(s.file("device.json"), &v)
}

// SaveDevice replaces device.json.
func (s *Store) SaveDevice(v Device) error {
	return atomicfile.WriteJSON(s.file("device.json"), v, 0o640)
}

// UpdateDevice applies fn to device.json under a lock and saves it.
func (s *Store) UpdateDevice(fn func(*Device)) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.LoadDevice()
	if err != nil {
		return d, err
	}
	fn(&d)
	return d, s.SaveDevice(d)
}

// ---------------------------------------------------------------- clock.json

// Clock is the measured difference server time − local time.
type Clock struct {
	OffsetMS   int64  `json:"offsetMs"`
	MeasuredAt string `json:"measuredAt"`
}

// LoadClock reads clock.json.
func (s *Store) LoadClock() (Clock, error) {
	var v Clock
	return v, load(s.file("clock.json"), &v)
}

// SaveClock replaces clock.json.
func (s *Store) SaveClock(v Clock) error { return atomicfile.WriteJSON(s.file("clock.json"), v, 0o640) }

// ---------------------------------------------------------------- webgui.json

// WebGUI holds the local client WebGUI password.
type WebGUI struct {
	PasswordHash string `json:"passwordHash"`
	MustChange   bool   `json:"mustChange"`
	ChangedAt    string `json:"changedAt,omitempty"`
}

// LoadWebGUI reads webgui.json.
func (s *Store) LoadWebGUI() (WebGUI, error) {
	var v WebGUI
	return v, load(s.file("webgui.json"), &v)
}

// SaveWebGUI replaces webgui.json (0600).
func (s *Store) SaveWebGUI(v WebGUI) error {
	return atomicfile.WriteJSON(s.file("webgui.json"), v, 0o600)
}

// ---------------------------------------------------------------- wipe

// WipeServerData deletes everything that belongs to the previous server (deployments,
// manifests, cache, live state, clock offset, the server's device config). Device
// settings (display mode, language, hardware id) are kept. server.json is rewritten by
// the caller.
func (s *Store) WipeServerData() error {
	var errs []error
	for _, f := range []string{"deployment.json", "live.json", "clock.json"} {
		if err := atomicfile.Remove(s.file(f)); err != nil {
			errs = append(errs, err)
		}
	}
	for _, dir := range []string{s.ManifestDir(), s.BlobDir(), s.TmpDir()} {
		if err := clearDir(dir); err != nil {
			errs = append(errs, err)
		}
	}
	if _, err := s.UpdateDevice(func(d *Device) {
		d.Config = nil
		d.LastServerDisplay = nil
	}); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// FactoryReset additionally forgets device settings, the server address choice and
// the local WebGUI password.
func (s *Store) FactoryReset() error {
	err := s.WipeServerData()
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	for _, f := range []string{"server.json", "webgui.json"} {
		if err := atomicfile.Remove(s.file(f)); err != nil {
			errs = append(errs, err)
		}
	}
	if _, err := s.UpdateDevice(func(d *Device) { *d = Device{HardwareID: d.HardwareID} }); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
