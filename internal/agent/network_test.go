package agent

import "testing"

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

func TestZoneCountry(t *testing.T) {
	table := "# comment\nDE\t+5230+01322\tEurope/Berlin\nAT\t+4813+01620\tEurope/Vienna\nCH,DE,LI\t+4723+00832\tEurope/Zurich\n"
	for zone, want := range map[string]string{"Europe/Berlin": "DE", "Europe/Zurich": "CH", "Europe/Paris": ""} {
		if got := zoneCountry(table, zone); got != want {
			t.Errorf("%s: %q, want %q", zone, got, want)
		}
	}
}
