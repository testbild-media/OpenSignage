package kiosk

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"opensignage/internal/common/atomicfile"
)

// PrepareProfile removes crash-restore state and stale locks so Chromium never shows a
// "restore pages?" bubble or refuses to start after a hard kill or power loss.
func PrepareProfile(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie", "DevToolsActivePort"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("chromium profile cleanup failed", "file", name, "err", err)
		}
	}
	def := filepath.Join(dir, "Default")
	for _, name := range []string{"Sessions", "Current Session", "Current Tabs", "Last Session", "Last Tabs"} {
		if err := os.RemoveAll(filepath.Join(def, name)); err != nil {
			slog.Warn("chromium session cleanup failed", "file", name, "err", err)
		}
	}
	if err := disableSecureDNS(dir); err != nil {
		slog.Warn("chromium local state not updated", "err", err)
	}
	prefsPath := filepath.Join(def, "Preferences")
	b, err := os.ReadFile(prefsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var prefs map[string]any
	if err := json.Unmarshal(b, &prefs); err != nil {
		slog.Warn("chromium preferences unreadable, removing", "err", err)
		return os.Remove(prefsPath)
	}
	profile, _ := prefs["profile"].(map[string]any)
	if profile == nil {
		profile = map[string]any{}
	}
	if profile["exit_type"] == "Normal" && profile["exited_cleanly"] == true {
		return nil
	}
	profile["exit_type"] = "Normal"
	profile["exited_cleanly"] = true
	prefs["profile"] = profile
	out, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	return atomicfile.Write(prefsPath, out, 0o600)
}

// disableSecureDNS turns DNS over HTTPS off in "Local State". In its default "automatic"
// mode Chromium upgrades a public resolver from resolv.conf (e.g. 1.1.1.1) to a DoH
// connection to that provider, bypassing the dead proxy that keeps all other traffic of the
// player off the internet. Screens need no DNS at all (everything comes from loopback).
func disableSecureDNS(dir string) error {
	path := filepath.Join(dir, "Local State")
	state := map[string]any{}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(b, &state); err != nil || state == nil {
			slog.Warn("chromium local state unreadable, replacing", "err", err)
			state = map[string]any{}
		}
	}
	doh, _ := state["dns_over_https"].(map[string]any)
	if doh != nil && doh["mode"] == "off" {
		return nil
	}
	if doh == nil {
		doh = map[string]any{}
	}
	doh["mode"] = "off"
	doh["templates"] = ""
	state["dns_over_https"] = doh
	out, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, out, 0o600)
}
