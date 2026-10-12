// Package config loads and validates the agent configuration (/etc/opensignage/agent.env).
//
// Precedence (highest first): process environment, config file, built-in default.
// Unknown keys in the file are an error. Invalid values abort the start with a clear
// message. Everything that changes at runtime (server address chosen in the client
// WebGUI, display mode, …) lives in the JSON state files, not here.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"

	"opensignage/internal/common/logging"
	"opensignage/internal/common/release"
)

// DefaultDevicePort is the server's device channel port.
const DefaultDevicePort = 7443

// Config is the validated agent configuration.
type Config struct {
	DataDir       string
	ShareDir      string // bundled fonts (<exe>/../share)
	Server        string // manual server address host:port ("" = discovery)
	WebGUIPort    int
	PlayerPort    int // loopback only
	DiscoveryPort int
	ClockPort     int    // UDP: LAN time exchange of the zone's screens
	Chromium      string // resolved browser binary ("" = not found)
	Display       string // X display, e.g. ":0"
	InstallDir    string // /opt/opensignage-agent (releases/, current, previous); "" = not installed
	// PlayerName is the name given at installation (OS_PLAYER_NAME): the host name became
	// osp-<name>, and the pairing request proposes it as the device name.
	PlayerName string
	// Warnings are problems that do not stop the agent (logged at start).
	Warnings []string
	// TrustedKeys are release signing keys trusted in addition to the built-in ones
	// (OS_TRUSTED_KEYS, comma separated "osk1:…"). Keys added in the server's settings do
	// not reach devices: a device only trusts what its own configuration says.
	TrustedKeys []string
	LogLevel    slog.Level
	File        string // config file that was read ("" if none)
}

var knownKeys = map[string]bool{
	"OS_AGENT_DATA_DIR": true, "OS_SERVER": true, "OS_WEBGUI_PORT": true, "OS_PLAYER_PORT": true,
	"OS_CHROMIUM": true, "OS_DISPLAY": true, "OS_LOG_LEVEL": true, "OS_SHARE_DIR": true,
	"OS_DISCOVERY_PORT": true, "OS_INSTALL_DIR": true, "OS_TRUSTED_KEYS": true, "OS_PLAYER_NAME": true,
}

// DefaultFile returns the platform default config path.
func DefaultFile() string {
	if runtime.GOOS == "windows" {
		return "agent.env"
	}
	return "/etc/opensignage/agent.env"
}

// lookPath is replaceable in tests.
var lookPath = exec.LookPath

// Load reads the config file (if it exists) and the environment.
func Load(file string) (*Config, error) {
	values := map[string]string{}
	cfg := &Config{}
	if file != "" {
		fv, err := ReadEnvFile(file)
		switch {
		case err == nil:
			cfg.File = file
			for k, v := range fv {
				if !knownKeys[k] {
					return nil, fmt.Errorf("config %s: unknown key %s", file, k)
				}
				values[k] = v
			}
		case errors.Is(err, os.ErrNotExist):
		default:
			return nil, fmt.Errorf("config %s: %w", file, err)
		}
	}
	for k := range knownKeys {
		if v, ok := os.LookupEnv(k); ok {
			values[k] = v
		}
	}
	get := func(k, def string) string {
		if v, ok := values[k]; ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}
	var errs []error
	port := func(k string, def int) int {
		s := get(k, strconv.Itoa(def))
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 65535 {
			errs = append(errs, fmt.Errorf("%s: invalid port %q", k, s))
		}
		return n
	}
	defaultData := "/var/lib/opensignage-agent"
	if runtime.GOOS == "windows" {
		defaultData = filepath.Join(".", "agent-data")
	}
	cfg.DataDir = get("OS_AGENT_DATA_DIR", defaultData)
	cfg.ShareDir = get("OS_SHARE_DIR", defaultShareDir())
	cfg.WebGUIPort = port("OS_WEBGUI_PORT", 8443)
	cfg.PlayerPort = port("OS_PLAYER_PORT", 7600)
	cfg.DiscoveryPort = port("OS_DISCOVERY_PORT", 7444)
	cfg.ClockPort = port("OS_CLOCK_PORT", 7445)
	if cfg.WebGUIPort == cfg.PlayerPort {
		errs = append(errs, fmt.Errorf("OS_WEBGUI_PORT and OS_PLAYER_PORT must differ (%d)", cfg.WebGUIPort))
	}
	if s := get("OS_SERVER", ""); s != "" {
		addr, err := NormalizeAddress(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("OS_SERVER: %w", err))
		}
		cfg.Server = addr
	}
	cfg.Display = get("OS_DISPLAY", ":0")
	if !strings.HasPrefix(cfg.Display, ":") {
		errs = append(errs, fmt.Errorf("OS_DISPLAY: invalid display %q (expected :N)", cfg.Display))
	}
	if c := get("OS_CHROMIUM", ""); c != "" {
		p, err := lookPath(c)
		if err != nil {
			errs = append(errs, fmt.Errorf("OS_CHROMIUM: %q not found", c))
		}
		cfg.Chromium = p
	} else {
		cfg.Chromium = DetectChromium()
	}
	lvl, err := logging.ParseLevel(get("OS_LOG_LEVEL", "info"))
	if err != nil {
		errs = append(errs, fmt.Errorf("OS_LOG_LEVEL: %w", err))
	}
	cfg.LogLevel = lvl
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		errs = append(errs, fmt.Errorf("OS_AGENT_DATA_DIR: %w", err))
	}
	cfg.DataDir = abs
	if s, err := filepath.Abs(cfg.ShareDir); err == nil {
		cfg.ShareDir = s
	}
	cfg.InstallDir = get("OS_INSTALL_DIR", DefaultInstallDir())
	if cfg.InstallDir != "" && !filepath.IsAbs(cfg.InstallDir) {
		errs = append(errs, fmt.Errorf("OS_INSTALL_DIR: %q must be an absolute path", cfg.InstallDir))
	}
	// Only a proposal for the device name: an unusable value must never keep a screen dark.
	cfg.PlayerName = get("OS_PLAYER_NAME", "")
	if !validPlayerName(cfg.PlayerName) {
		cfg.Warnings = append(cfg.Warnings, "OS_PLAYER_NAME ignored: at most 64 characters (UTF-8) without control characters")
		cfg.PlayerName = ""
	}
	for _, k := range strings.Split(get("OS_TRUSTED_KEYS", ""), ",") {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		pub, err := release.ParsePublicKey(k)
		if err != nil {
			errs = append(errs, fmt.Errorf("OS_TRUSTED_KEYS: invalid release key %q (expected osk1:…)", k))
			continue
		}
		cfg.TrustedKeys = append(cfg.TrustedKeys, release.FormatPublicKey(pub))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

func validPlayerName(s string) bool {
	if utf8.RuneCountInString(s) > 64 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// DetectChromium finds the system browser ("" when none is installed).
func DetectChromium() string {
	for _, name := range []string{"chromium", "chromium-browser"} {
		if p, err := lookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// NormalizeAddress validates host[:port] and adds the default device port.
func NormalizeAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "wss://")
	s = strings.TrimSuffix(s, "/")
	if s == "" {
		return "", errors.New("empty address")
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		// No port (or a bare IPv6 address).
		host, port = strings.Trim(s, "[]"), strconv.Itoa(DefaultDevicePort)
	}
	if host == "" || strings.ContainsAny(host, " /?#@\\") {
		return "", fmt.Errorf("invalid host %q", host)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("invalid port %q", port)
	}
	return net.JoinHostPort(host, strconv.Itoa(n)), nil
}

// DefaultInstallDir derives the installation root from the running binary
// (<root>/releases/<v>/bin/opensignage-agent); "" when not running from a release.
func DefaultInstallDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	bin := filepath.Dir(exe)
	releases := filepath.Dir(filepath.Dir(bin))
	if filepath.Base(bin) != "bin" || filepath.Base(releases) != "releases" {
		return ""
	}
	return filepath.Dir(releases)
}

func defaultShareDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "share"
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	// Installed layout: releases/<v>/bin/opensignage-agent → releases/<v>/share
	return filepath.Join(filepath.Dir(filepath.Dir(exe)), "share")
}

// ReadEnvFile parses KEY=VALUE lines (comments with #, optional quotes).
func ReadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", line)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, sc.Err()
}
