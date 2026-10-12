package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrecedenceAndValidation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "server.env")
	if err := os.WriteFile(file, []byte("# comment\nOS_DEVICE_PORT=7500\nOS_LOG_LEVEL=debug\nOS_DATA_DIR=\""+filepath.ToSlash(dir)+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OS_DEVICE_PORT", "7600") // env beats file
	cfg, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DevicePort != 7600 || cfg.HTTPSPort != 443 || cfg.HTTPPort != 80 || cfg.DevPorts {
		t.Fatalf("unexpected %+v", cfg)
	}
	if cfg.LogLevel.String() != "DEBUG" {
		t.Fatal("file value ignored")
	}

	t.Setenv("OS_DEVICE_PORT", "abc")
	if _, err := Load(file); err == nil {
		t.Fatal("invalid port must fail")
	}
	t.Setenv("OS_DEVICE_PORT", "443")
	if _, err := Load(file); err == nil {
		t.Fatal("duplicate port must fail")
	}
	t.Setenv("OS_DEVICE_PORT", "7443")
	if err := os.WriteFile(file, []byte("OS_DEVICE_PROT=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); err == nil {
		t.Fatal("unknown key must fail")
	}
}

func TestValidRepo(t *testing.T) {
	for s, want := range map[string]bool{"a/b": true, "testbild-media/OpenSignage": true, "a.b/c.d": true, "a": false, "a/b/c": false,
		"a b/c": false, "a/b?x=1": false, "a/b#x": false, "a/b%2F..": false, "a/..": false, "../b": false, "a/.": false,
		"a/<b>": false, "a/" + strings.Repeat("b", 200): false} {
		if ValidRepo(s) != want {
			t.Errorf("%s", s)
		}
	}
}
