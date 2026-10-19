// Command opensignage-netctl is the root helper of the network settings in the client
// WebGUI. The installer puts it root-owned under /usr/lib/opensignage-agent and allows the
// agent's service user to run it with sudo; the agent cannot change it.
//
//	opensignage-netctl version
//	opensignage-netctl apply              (configuration as JSON on stdin)
//	opensignage-netctl confirm
//	opensignage-netctl cleanup
//	opensignage-netctl uninstall          (run by the client uninstaller)
//	opensignage-netctl scan IFACE
//	opensignage-netctl firmware
//	opensignage-netctl install-firmware PACKAGE
//
// The answer is one JSON object on stdout: {"ok":true,…} or {"error":{field,reason,message}}
// with exit code 1.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"opensignage/internal/agent/netcfg"
	"opensignage/internal/agent/netctl"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Geteuid() == 0, &netctl.Helper{}))
}

type errorBody struct {
	Field   string `json:"field,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message"`
}

func run(args []string, in io.Reader, out io.Writer, root bool, h *netctl.Helper) int {
	reply := func(v map[string]any) int {
		v["ok"] = true
		_ = json.NewEncoder(out).Encode(v)
		return 0
	}
	fail := func(err error) int {
		body := errorBody{Message: err.Error()}
		var fe *netcfg.InputError
		if errors.As(err, &fe) {
			body.Field, body.Reason = fe.Field, fe.Reason
		}
		_ = json.NewEncoder(out).Encode(map[string]any{"error": body})
		return 1
	}
	if len(args) == 0 {
		return fail(errors.New("usage: opensignage-netctl version|apply|confirm|cleanup|uninstall|scan IFACE|firmware|install-firmware PACKAGE"))
	}
	if args[0] == "version" {
		return reply(map[string]any{"protocol": netcfg.HelperProtocol})
	}
	if !root {
		return fail(errors.New("must run as root (sudo)"))
	}
	ctx := context.Background()
	arg := func() (string, error) {
		if len(args) != 2 {
			return "", fmt.Errorf("%s needs exactly one argument", args[0])
		}
		return args[1], nil
	}
	switch args[0] {
	case "apply":
		var c netcfg.Config
		dec := json.NewDecoder(io.LimitReader(in, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return fail(fmt.Errorf("invalid configuration: %w", err))
		}
		if err := h.Apply(ctx, c); err != nil {
			return fail(err)
		}
		return reply(map[string]any{})
	case "confirm":
		if err := h.Confirm(ctx); err != nil {
			return fail(err)
		}
		return reply(map[string]any{})
	case "cleanup":
		if err := h.Cleanup(); err != nil {
			return fail(err)
		}
		return reply(map[string]any{})
	case "uninstall":
		pending, err := h.Uninstall()
		if err != nil {
			return fail(err)
		}
		return reply(map[string]any{"pending": pending})
	case "scan":
		iface, err := arg()
		if err != nil {
			return fail(err)
		}
		aps, err := h.Scan(ctx, iface)
		if err != nil {
			return fail(err)
		}
		if aps == nil {
			aps = []netcfg.AP{}
		}
		return reply(map[string]any{"networks": aps})
	case "firmware":
		hints, err := h.Firmware(ctx)
		if err != nil {
			return fail(err)
		}
		if hints == nil {
			hints = []netctl.FirmwareHint{}
		}
		return reply(map[string]any{"missing": hints})
	case "install-firmware":
		pkg, err := arg()
		if err != nil {
			return fail(err)
		}
		if err := h.InstallFirmware(ctx, pkg); err != nil {
			return fail(err)
		}
		return reply(map[string]any{})
	}
	return fail(fmt.Errorf("unknown command %q", args[0]))
}
