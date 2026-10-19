package netmgr

import (
	"errors"
	"testing"

	"opensignage/internal/agent/netcfg"
)

func TestApplyError(t *testing.T) {
	wifi := netcfg.Config{Interface: "wlan0", Kind: netcfg.KindWifi}
	refused := errors.New("nmcli --wait 60 connection up uuid x: exit status 4: Passwords or encryption keys are required " +
		"to access the wireless network 'Bar'. Error: Connection activation failed: Secrets were required, but not provided")
	var fe *netcfg.InputError
	if err := applyError(wifi, refused); !errors.As(err, &fe) || fe.Field != "passphrase" || fe.Reason != "rejected" {
		t.Fatalf("refused password: %v", err)
	}
	other := errors.New("nmcli: connection activation failed")
	if err := applyError(wifi, other); err != other {
		t.Fatalf("other errors stay: %v", err)
	}
	if err := applyError(netcfg.Config{Interface: "eth0", Kind: netcfg.KindEthernet}, refused); err != refused {
		t.Fatalf("wired: %v", err)
	}
	if applyError(wifi, nil) != nil {
		t.Fatal("no error")
	}
}
