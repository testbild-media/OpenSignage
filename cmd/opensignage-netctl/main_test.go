package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opensignage/internal/agent/netcfg"
	"opensignage/internal/agent/netctl"
)

type answer struct {
	OK       bool        `json:"ok"`
	Protocol int         `json:"protocol"`
	Pending  bool        `json:"pending"`
	Networks []netcfg.AP `json:"networks"`
	Error    *errorBody  `json:"error"`
	Missing  []any       `json:"missing"`
}

func call(t *testing.T, h *netctl.Helper, root bool, stdin string, args ...string) (int, answer) {
	t.Helper()
	var out bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, root, h)
	var a answer
	if err := json.Unmarshal(out.Bytes(), &a); err != nil {
		t.Fatalf("%v: not one JSON answer: %q", args, out.String())
	}
	if (code == 0) != a.OK || (code != 0) != (a.Error != nil) {
		t.Fatalf("%v: exit code %d does not match %q", args, code, out.String())
	}
	return code, a
}

func TestCommands(t *testing.T) {
	dir := t.TempDir()
	put := func(p, content string) {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("/sys/class/net/eth0/device/vendor", "0x8086")
	put("/sys/class/net/eth0/type", "1\n")
	put("/etc/network/interfaces", "auto eth0\niface eth0 inet dhcp\n")
	put("/etc/resolv.conf", "nameserver 10.0.0.1\n")
	var ran []string
	h := &netctl.Helper{Root: dir, Now: time.Now, Sleep: func(time.Duration) {},
		Run: func(_ context.Context, _ time.Duration, name string, args ...string) ([]byte, error) {
			ran = append(ran, name+" "+strings.Join(args, " "))
			return nil, nil
		}}

	// version answers anyone (the agent asks it without sudo too); the rest needs root.
	if _, a := call(t, h, false, "", "version"); a.Protocol != netcfg.HelperProtocol {
		t.Fatalf("protocol %d", a.Protocol)
	}
	if code, a := call(t, h, false, "", "cleanup"); code != 1 || !strings.Contains(a.Error.Message, "root") {
		t.Fatalf("non-root: %d %+v", code, a.Error)
	}
	if code, _ := call(t, h, true, ""); code != 1 {
		t.Fatal("no command")
	}
	if code, _ := call(t, h, true, "", "reboot"); code != 1 {
		t.Fatal("unknown command")
	}
	if code, _ := call(t, h, true, "", "scan"); code != 1 {
		t.Fatal("scan without interface")
	}

	// apply: strict JSON, field errors come back with field and reason.
	if code, a := call(t, h, true, `{"interface":"eth0","kind":"ethernet","method":"dhcp","script":"x"}`, "apply"); code != 1 || !strings.Contains(a.Error.Message, "unknown field") {
		t.Fatalf("unknown field: %d %+v", code, a.Error)
	}
	cfg := `{"interface":"eth0","kind":"ethernet","method":"static","static":{"address":"10.0.0.50/24","gateway":"10.9.0.1"}}`
	if code, a := call(t, h, true, cfg, "apply"); code != 1 || a.Error.Field != "gateway" || a.Error.Reason != "outside_network" {
		t.Fatalf("field error: %d %+v", code, a.Error)
	}
	if code, a := call(t, h, true, `{"interface":"eth9","kind":"ethernet","method":"dhcp"}`, "apply"); code != 1 || a.Error.Field != "interface" || a.Error.Reason != "unknown" {
		t.Fatalf("unknown interface: %d %+v", code, a.Error)
	}
	cfg = `{"interface":"eth0","kind":"ethernet","method":"static","static":{"address":"10.0.0.50/24","gateway":"10.0.0.1","dns":["10.0.0.1"]}}`
	if code, a := call(t, h, true, cfg, "apply"); code != 0 {
		t.Fatalf("apply: %+v (ran %v)", a.Error, ran)
	}
	if _, a := call(t, h, true, "", "uninstall"); !a.Pending {
		t.Fatal("uninstall must report the pending change")
	}
	if code, _ := call(t, h, true, "", "confirm"); code != 0 {
		t.Fatal("confirm")
	}
	if code, a := call(t, h, true, "", "install-firmware", "openssh-server"); code != 1 || a.Error.Field != "package" {
		t.Fatalf("only network firmware may be installed: %d %+v", code, a.Error)
	}
}
