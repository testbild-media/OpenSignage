package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"opensignage/internal/agent/netcfg"
	"opensignage/internal/agent/netmgr"
	"opensignage/internal/agent/webgui"
)

func TestNetworkFailedOnce(t *testing.T) {
	a := &Agent{}
	if a.NetworkFailed() {
		t.Fatal("nothing failed")
	}
	a.net.lastErr = "ifup enp2s0: exit status 1"
	if !a.NetworkFailed() {
		t.Fatal("the next login leads to the network page")
	}
	if a.NetworkFailed() {
		t.Fatal("only once")
	}
}

// A login right after "apply" – before the change took effect – waits for it, then confirms.
func TestConfirmWaitsForTheChange(t *testing.T) {
	root := t.TempDir()
	a := &Agent{}
	a.net.mgr = &netmgr.Manager{Root: root}
	done := make(chan struct{})
	a.net.applying = done
	if !a.NetworkPending() {
		t.Fatal("a change being applied counts as pending (the login page says so)")
	}
	res := make(chan error, 1)
	go func() { res <- a.ConfirmNetwork(context.Background()) }()
	select {
	case err := <-res:
		t.Fatalf("returned before the change took effect: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	// The change did not come up (no pending file): nothing to confirm.
	a.net.mu.Lock()
	a.net.applying = nil
	a.net.mu.Unlock()
	close(done)
	select {
	case err := <-res:
		if !errors.Is(err, webgui.ErrNothingToConfirm) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("still waiting after the change was done")
	}
	// A pending change makes the login confirm it.
	p := filepath.Join(root, filepath.FromSlash(netcfg.PendingFile))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"since":"2026-10-19T12:00:00Z","interfaces":["wlan0"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !a.NetworkPending() {
		t.Fatal("pending file")
	}
	// (Confirm itself needs the helper, which a test does not have: waiting is what counts.)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	a.net.applying = make(chan struct{})
	if err := a.ConfirmNetwork(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the wait ends with the login request: %v", err)
	}
}

func TestZoneCountry(t *testing.T) {
	table := "# comment\nDE\t+5230+01322\tEurope/Berlin\nAT\t+4813+01620\tEurope/Vienna\nCH,DE,LI\t+4723+00832\tEurope/Zurich\n"
	for zone, want := range map[string]string{"Europe/Berlin": "DE", "Europe/Zurich": "CH", "Europe/Paris": ""} {
		if got := zoneCountry(table, zone); got != want {
			t.Errorf("%s: %q, want %q", zone, got, want)
		}
	}
}
