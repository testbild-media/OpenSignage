// Package config loads and validates the server configuration.
//
// Precedence (highest first): process environment, config file, built-in default.
// Unknown keys in the config file are an error (catches typos). Invalid values abort
// the start with a clear message instead of silently falling back.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"opensignage/internal/common/logging"
)

// DefaultUpdateRepo is injected at build time (-X …config.DefaultUpdateRepo=owner/repo).
// Development builds point to the public test repository.
var DefaultUpdateRepo = "testbild-media/OpenSignage"

// Config is the validated server configuration. Values here need a restart to change.
type Config struct {
	DataDir       string
	ShareDir      string // bundled fonts and other read-only assets
	HTTPPort      int    // WebGUI redirect port, fixed 80 in production
	HTTPSPort     int    // WebGUI port, fixed 443 in production
	DevicePort    int    // device channel (HTTPS + WS, pinned identity certificate)
	DiscoveryPort int    // UDP discovery responder
	LogLevel      slog.Level
	FFmpeg        string
	FFprobe       string
	UpdateRepo    string // default update source (owner/repo); settings may override
	File          string // config file that was read ("" if none)
	DevPorts      bool   // WebGUI ports differ from 80/443 (development only)
}

var knownKeys = map[string]bool{
	"OS_DATA_DIR": true, "OS_SHARE_DIR": true, "OS_DEVICE_PORT": true, "OS_DISCOVERY_PORT": true,
	"OS_LOG_LEVEL": true, "OS_FFMPEG": true, "OS_FFPROBE": true, "OS_UPDATE_REPO": true,
}

// devKeys are honoured only from the process environment together with OS_DEV=1, never
// from the config file: in production the WebGUI is always on ports 80/443.
var devKeys = []string{"OS_DEV_HTTP_PORT", "OS_DEV_HTTPS_PORT"}

// DefaultFile returns the platform default config path.
func DefaultFile() string {
	if runtime.GOOS == "windows" {
		return "server.env"
	}
	return "/etc/opensignage/server.env"
}

// Load reads the config file (if it exists) and the environment.
func Load(file string) (*Config, error) {
	values := map[string]string{}
	cfg := &Config{}
	if file != "" {
		fv, err := readEnvFile(file)
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
		if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
			values[k] = v
		}
	}
	for _, k := range devKeys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if os.Getenv("OS_DEV") != "1" {
				return nil, fmt.Errorf("%s is only allowed for development (set OS_DEV=1); the web interface runs on ports 80/443", k)
			}
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
	defaultData := "/var/lib/opensignage"
	if runtime.GOOS == "windows" {
		defaultData = filepath.Join(".", "data")
	}
	cfg.DataDir = get("OS_DATA_DIR", defaultData)
	cfg.ShareDir = get("OS_SHARE_DIR", defaultShareDir())
	cfg.HTTPPort = port("OS_DEV_HTTP_PORT", 80)
	cfg.HTTPSPort = port("OS_DEV_HTTPS_PORT", 443)
	cfg.DevPorts = cfg.HTTPPort != 80 || cfg.HTTPSPort != 443
	cfg.DevicePort = port("OS_DEVICE_PORT", 7443)
	cfg.DiscoveryPort = port("OS_DISCOVERY_PORT", 7444)
	lvl, err := logging.ParseLevel(get("OS_LOG_LEVEL", "info"))
	if err != nil {
		errs = append(errs, fmt.Errorf("OS_LOG_LEVEL: %w", err))
	}
	cfg.LogLevel = lvl
	cfg.FFmpeg = get("OS_FFMPEG", "ffmpeg")
	cfg.FFprobe = get("OS_FFPROBE", "ffprobe")
	cfg.UpdateRepo = get("OS_UPDATE_REPO", DefaultUpdateRepo)
	if !ValidRepo(cfg.UpdateRepo) {
		errs = append(errs, fmt.Errorf("OS_UPDATE_REPO: invalid repository %q (expected owner/name)", cfg.UpdateRepo))
	}
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		errs = append(errs, fmt.Errorf("OS_DATA_DIR: %w", err))
	}
	cfg.DataDir = abs
	if s, err := filepath.Abs(cfg.ShareDir); err == nil {
		cfg.ShareDir = s
	}
	seen := map[int]string{}
	for name, p := range map[string]int{"http": cfg.HTTPPort, "https": cfg.HTTPSPort, "device": cfg.DevicePort} {
		if other, dup := seen[p]; dup {
			errs = append(errs, fmt.Errorf("port %d used twice (%s, %s)", p, other, name))
		}
		seen[p] = name
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

// ValidRepo checks "owner/name" for GitHub.
func ValidRepo(s string) bool {
	owner, name, ok := strings.Cut(s, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") || len(s) > 200 || strings.Contains(s, "..") || name == "." {
		return false
	}
	for _, c := range owner + name {
		if !(c == '-' || c == '_' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func defaultShareDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "share"
	}
	exe, _ = filepath.EvalSymlinks(exe)
	// Installed layout: releases/<v>/bin/opensignage-server → releases/<v>/share
	return filepath.Join(filepath.Dir(filepath.Dir(exe)), "share")
}

func readEnvFile(path string) (map[string]string, error) {
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
