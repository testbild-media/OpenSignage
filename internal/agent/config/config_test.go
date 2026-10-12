package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearEnv(t *testing.T) {
	for k := range knownKeys {
		if v, ok := os.LookupEnv(k); ok {
			t.Cleanup(func() { os.Setenv(k, v) })
			os.Unsetenv(k)
		}
	}
	old := lookPath
	lookPath = func(name string) (string, error) {
		if name == "chromium" {
			return "/usr/bin/chromium", nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { lookPath = old })
}

func TestDefaultsAndPrecedence(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	f := filepath.Join(dir, "agent.env")
	if err := os.WriteFile(f, []byte("# comment\nOS_SERVER=10.0.0.5\nOS_WEBGUI_PORT=9443\nOS_LOG_LEVEL=debug\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OS_WEBGUI_PORT", "9444")
	cfg, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != "10.0.0.5:7443" {
		t.Fatalf("server = %q", cfg.Server)
	}
	if cfg.WebGUIPort != 9444 {
		t.Fatalf("env must win over file, got %d", cfg.WebGUIPort)
	}
	if cfg.PlayerPort != 7600 || cfg.Display != ":0" || cfg.Chromium != "/usr/bin/chromium" {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
}

func TestUnknownKeyAndInvalidValues(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	f := filepath.Join(dir, "agent.env")
	os.WriteFile(f, []byte("OS_SERVR=1.2.3.4\n"), 0o600)
	if _, err := Load(f); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("expected unknown key error, got %v", err)
	}
	os.WriteFile(f, []byte("OS_PLAYER_PORT=abc\nOS_DISPLAY=0\nOS_CHROMIUM=nope\n"), 0o600)
	_, err := Load(f)
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"OS_PLAYER_PORT", "OS_DISPLAY", "OS_CHROMIUM"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %s", err, want)
		}
	}
}

func TestTrustedKeys(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	f := filepath.Join(dir, "agent.env")
	k1 := "osk1:5w7fufvnSzj/99bUODdFjp8l1r28QS9ay8l/9RkRgco="
	k2 := "osk1:" + strings.Repeat("A", 43) + "="
	os.WriteFile(f, []byte("OS_TRUSTED_KEYS="+k1+" , "+k2+",\n"), 0o600)
	cfg, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.TrustedKeys, ",") != k1+","+k2 {
		t.Fatalf("keys %v", cfg.TrustedKeys)
	}
	os.WriteFile(f, []byte("OS_TRUSTED_KEYS="+k1+",osk1:short\n"), 0o600)
	if _, err := Load(f); err == nil || !strings.Contains(err.Error(), "OS_TRUSTED_KEYS") {
		t.Fatalf("invalid key must be refused: %v", err)
	}
	os.WriteFile(f, []byte("OS_SERVER=10.0.0.5\n"), 0o600)
	if cfg, err := Load(f); err != nil || len(cfg.TrustedKeys) != 0 {
		t.Fatalf("no extra keys by default: %v %v", cfg, err)
	}
}

func TestNormalizeAddress(t *testing.T) {
	for in, want := range map[string]string{
		"10.0.0.1":            "10.0.0.1:7443",
		"server.local:8000":   "server.local:8000",
		"https://srv:7443/":   "srv:7443",
		"[fd00::1]:7443":      "[fd00::1]:7443",
		"fd00::1":             "[fd00::1]:7443",
		" 100.64.1.2 ":        "100.64.1.2:7443",
		"tailscale-host.ts.n": "tailscale-host.ts.n:7443",
	} {
		got, err := NormalizeAddress(in)
		if err != nil || got != want {
			t.Errorf("%q → %q, %v (want %q)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "host:0", "host:99999", "a b", "user@host"} {
		if _, err := NormalizeAddress(bad); err == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestPlayerName(t *testing.T) {
	clearEnv(t)
	f := filepath.Join(t.TempDir(), "agent.env")
	if err := os.WriteFile(f, []byte("OS_PLAYER_NAME=Theke Süd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f)
	if err != nil || cfg.PlayerName != "Theke Süd" {
		t.Fatalf("name %q err %v", cfg.PlayerName, err)
	}
	if err := os.WriteFile(f, []byte("OS_PLAYER_NAME="+strings.Repeat("x", 65)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Never fatal (it is only a proposal for the device name): ignored with a warning.
	cfg, err = Load(f)
	if err != nil || cfg.PlayerName != "" || len(cfg.Warnings) != 1 {
		t.Fatalf("a name over 64 characters must be ignored with a warning: %+v %v", cfg, err)
	}
	// "Theke Süd" typed in a Latin-1 terminal: ü as the single byte 0xfc.
	if err := os.WriteFile(f, []byte("OS_PLAYER_NAME=Theke S\xfcd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err = Load(f); err != nil || cfg.PlayerName != "" || len(cfg.Warnings) != 1 {
		t.Fatalf("a name that is not UTF-8 must be ignored with a warning: %+v %v", cfg, err)
	}
}
