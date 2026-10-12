package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opensignage/internal/agent/wire"
)

func TestWipeServerDataKeepsDevice(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 64)
	os.WriteFile(st.BlobPath(sha), []byte("x"), 0o640)
	os.WriteFile(st.PartPath(sha), []byte("x"), 0o640)
	st.SaveManifest("dep-1", []byte(`{"id":"dep-1"}`))
	st.SaveDeployment(Deployment{Active: &Ref{ID: "dep-1"}})
	st.SaveLive(Live{State: []byte(`{}`)})
	st.SaveClock(Clock{OffsetMS: 5000})
	disp := wire.Display{Width: 1366, Height: 768, Refresh: 60}
	st.SaveDevice(Device{HardwareID: "hw", Display: disp, Language: "de",
		Config: &wire.DeviceConfig{Name: "Bar", Blackout: true}, LastServerDisplay: &disp})

	if err := st.WipeServerData(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"deployment.json", "live.json", "clock.json"} {
		if _, err := os.Stat(filepath.Join(st.StateDir(), f)); !os.IsNotExist(err) {
			t.Errorf("%s must be wiped", f)
		}
	}
	for _, p := range []string{st.BlobPath(sha), st.PartPath(sha), st.ManifestPath("dep-1")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s must be wiped", p)
		}
	}
	d, err := st.LoadDevice()
	if err != nil {
		t.Fatal(err)
	}
	if d.Display != disp || d.Language != "de" || d.HardwareID != "hw" {
		t.Fatalf("device settings lost: %+v", d)
	}
	if d.Config != nil || d.LastServerDisplay != nil {
		t.Fatalf("server config must be reset: %+v", d)
	}
}

func TestServerFilePermissionsAndValidation(t *testing.T) {
	st, _ := New(t.TempDir())
	if err := st.SaveServer(Server{Address: "a:1", Fingerprint: "f", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	s, err := st.LoadServer()
	if err != nil || !s.Paired() {
		t.Fatalf("%+v %v", s, err)
	}
	if err := st.SaveManifest("../evil", []byte("{}")); err == nil {
		t.Fatal("path traversal in manifest id must be rejected")
	}
	if ValidSHA("../../etc/passwd") || !ValidSHA(strings.Repeat("0", 64)) {
		t.Fatal("ValidSHA wrong")
	}
}
