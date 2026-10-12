package kiosk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Logs the detection on the machine running the tests (to check a player:
// go test -c, then ./kiosk.test -test.run DetectVideoAccelHere -test.v).
func TestDetectVideoAccelHere(t *testing.T) {
	accel := DetectVideoAccel()
	t.Logf("video accel %q, auto → hardware %v", accel, HWVideoEnabled("auto", accel))
}

func TestHWVideoEnabled(t *testing.T) {
	for _, c := range []struct {
		mode  string
		accel VideoAccel
		want  bool
	}{
		{"auto", AccelV4L2, true},  // Raspberry Pi: on by default
		{"auto", AccelVAAPI, true}, // Intel/AMD with VA-API + Vulkan: on by default
		{"auto", AccelNone, false},
		{"", AccelV4L2, true}, // no config yet = auto
		{"", AccelNone, false},
		{"on", AccelNone, true},
		{"on", AccelVAAPI, true},
		{"off", AccelV4L2, false}, // "off" is honoured everywhere
		{"off", AccelVAAPI, false},
	} {
		if got := HWVideoEnabled(c.mode, c.accel); got != c.want {
			t.Errorf("HWVideoEnabled(%q, %q) = %v", c.mode, c.accel, got)
		}
	}
}

func argsFor(on bool, accel VideoAccel) []string {
	s := New(Options{URL: "http://127.0.0.1:7600/", UserDataDir: "/tmp/x", HWVideo: func() bool { return on }, VideoAccel: accel})
	return s.Args()
}

func countPrefix(args []string, prefix string) int {
	n := 0
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			n++
		}
	}
	return n
}

func TestHWVideoArgs(t *testing.T) {
	for _, accel := range []VideoAccel{AccelNone, AccelV4L2, AccelVAAPI} {
		args := argsFor(false, accel)
		if countPrefix(args, "--disable-accelerated-video-decode") != 1 || countPrefix(args, "--use-angle") != 0 {
			t.Errorf("off (%q): %v", accel, args)
		}
		for _, on := range []bool{false, true} {
			args := argsFor(on, accel)
			// Chromium only uses the last --enable-features/--disable-features switch.
			if countPrefix(args, "--enable-features=") > 1 || countPrefix(args, "--disable-features=") != 1 {
				t.Errorf("duplicate feature switches (%q, %v): %v", accel, on, args)
			}
			if strings.Contains(strings.Join(args, " "), "VaapiVideoDecoder") {
				t.Errorf("obsolete feature name VaapiVideoDecoder (%q, %v)", accel, on)
			}
		}
	}

	// Raspberry Pi: its Chromium decodes with V4L2 by default; the agent adds nothing that
	// could replace distribution flags (and does not ignore the GPU blocklist).
	pi := strings.Join(argsFor(true, AccelV4L2), " ")
	for _, bad := range []string{"--disable-accelerated-video-decode", "--enable-features=", "--ignore-gpu-blocklist", "--use-angle", "--use-gl"} {
		if strings.Contains(pi, bad) {
			t.Errorf("pi: unexpected %s in %s", bad, pi)
		}
	}

	// Intel/AMD: VA-API frames only reach the screen with Vulkan compositing on X11.
	vk := strings.Join(argsFor(true, AccelVAAPI), " ")
	for _, want := range []string{"--use-gl=angle", "--use-angle=vulkan",
		"--enable-features=Vulkan,VulkanFromANGLE,DefaultANGLEVulkan,VaapiIgnoreDriverChecks"} {
		if !strings.Contains(vk, want) {
			t.Errorf("vaapi: missing %s in %s", want, vk)
		}
	}
	if strings.Contains(vk, "--disable-accelerated-video-decode") || strings.Contains(vk, "--ignore-gpu-blocklist") {
		t.Errorf("vaapi: %s", vk)
	}

	// Unknown platform forced on: generic flags, Chromium falls back by itself.
	gen := strings.Join(argsFor(true, AccelNone), " ")
	if !strings.Contains(gen, "--enable-features=AcceleratedVideoDecodeLinuxGL") || strings.Contains(gen, "--use-angle") {
		t.Errorf("generic: %s", gen)
	}
}

// fakeSystem builds the files DetectVideoAccel reads below a temporary directory.
type fakeSystem struct {
	t    *testing.T
	root string
}

func newFakeSystem(t *testing.T) *fakeSystem { return &fakeSystem{t: t, root: t.TempDir()} }

func (f *fakeSystem) write(rel, content string) {
	p := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeSystem) paths() videoPaths {
	j := func(rel string) string { return filepath.Join(f.root, filepath.FromSlash(rel)) }
	return videoPaths{
		model:      j("proc/device-tree/model"),
		dpkgStatus: j("var/lib/dpkg/status"),
		drm:        j("sys/class/drm"),
		vaDrivers:  []string{j("usr/lib/x86_64-linux-gnu/dri")},
		vulkanICDs: []string{j("usr/share/vulkan/icd.d"), j("etc/vulkan/icd.d")},
	}
}

func dpkgStatus(pkg, version string) string {
	return "Package: libfoo\nStatus: install ok installed\nVersion: 1.0\n\n" +
		"Package: " + pkg + "\nStatus: install ok installed\nPriority: optional\nVersion: " + version + "\nDescription: x\n multi-line\n\n" +
		"Package: chromium-l10n\nStatus: deinstall ok config-files\nVersion: 1:1+rpt1\n"
}

func TestDetectVideoAccel(t *testing.T) {
	// Raspberry Pi 3 with Chromium from archive.raspberrypi.com.
	pi := newFakeSystem(t)
	pi.write("proc/device-tree/model", "Raspberry Pi 3 Model B Rev 1.2\x00")
	pi.write("var/lib/dpkg/status", dpkgStatus("chromium", "1:154.0.8037.92-1~deb13u1+rpt1"))
	pi.write("sys/class/drm/renderD128/device/uevent", "DRIVER=vc4-drm\nOF_NAME=gpu\n")
	if got := detectVideoAccel(pi.paths(), "arm64"); got != AccelV4L2 {
		t.Errorf("pi: %q", got)
	}
	// Older Raspberry Pi OS: package chromium-browser.
	old := newFakeSystem(t)
	old.write("proc/device-tree/model", "Raspberry Pi 4 Model B Rev 1.4\x00")
	old.write("var/lib/dpkg/status", dpkgStatus("chromium-browser", "126.0.6478.164-rpt1"))
	if got := detectVideoAccel(old.paths(), "arm64"); got != AccelV4L2 {
		t.Errorf("pi chromium-browser: %q", got)
	}
	// Raspberry Pi with Debian's Chromium (no V4L2 build): not proven.
	deb := newFakeSystem(t)
	deb.write("proc/device-tree/model", "Raspberry Pi 4 Model B Rev 1.4\x00")
	deb.write("var/lib/dpkg/status", dpkgStatus("chromium", "154.0.8037.92-1~deb13u1"))
	if got := detectVideoAccel(deb.paths(), "arm64"); got != AccelNone {
		t.Errorf("pi with debian chromium: %q", got)
	}

	// x86 Intel (i3-6100U, Debian 13): VA-API driver + Vulkan ICD.
	intel := newFakeSystem(t)
	intel.write("var/lib/dpkg/status", dpkgStatus("chromium", "154.0.8037.92-1~deb13u1"))
	intel.write("sys/class/drm/renderD128/device/uevent", "DRIVER=i915\nPCI_ID=8086:1916\n")
	intel.write("usr/lib/x86_64-linux-gnu/dri/iHD_drv_video.so", "")
	if got := detectVideoAccel(intel.paths(), "amd64"); got != AccelNone {
		t.Errorf("intel without vulkan driver: %q", got)
	}
	intel.write("usr/share/vulkan/icd.d/lvp_icd.json", "{}") // lavapipe is software
	if got := detectVideoAccel(intel.paths(), "amd64"); got != AccelNone {
		t.Errorf("intel with lavapipe only: %q", got)
	}
	intel.write("usr/share/vulkan/icd.d/intel_icd.json", "{}")
	if got := detectVideoAccel(intel.paths(), "amd64"); got != AccelVAAPI {
		t.Errorf("intel: %q", got)
	}
	if got := detectVideoAccel(intel.paths(), "arm64"); got != AccelNone {
		t.Errorf("intel paths on arm64: %q", got)
	}

	// x86 Intel without VA-API driver.
	nova := newFakeSystem(t)
	nova.write("sys/class/drm/renderD128/device/uevent", "DRIVER=i915\n")
	nova.write("usr/share/vulkan/icd.d/intel_icd.x86_64.json", "{}")
	if got := detectVideoAccel(nova.paths(), "amd64"); got != AccelNone {
		t.Errorf("intel without va driver: %q", got)
	}

	// AMD (amdgpu) with Mesa VA-API and RADV; the legacy radeon driver has no Vulkan.
	amd := newFakeSystem(t)
	amd.write("sys/class/drm/renderD128/device/uevent", "DRIVER=amdgpu\n")
	amd.write("usr/lib/x86_64-linux-gnu/dri/radeonsi_drv_video.so", "")
	amd.write("etc/vulkan/icd.d/radeon_icd.x86_64.json", "{}")
	if got := detectVideoAccel(amd.paths(), "amd64"); got != AccelVAAPI {
		t.Errorf("amd: %q", got)
	}
	amd.write("sys/class/drm/renderD128/device/uevent", "DRIVER=radeon\n")
	if got := detectVideoAccel(amd.paths(), "amd64"); got != AccelNone {
		t.Errorf("legacy radeon: %q", got)
	}

	// Virtual machine (virtio/qxl) or nothing at all.
	vm := newFakeSystem(t)
	vm.write("sys/class/drm/renderD128/device/uevent", "DRIVER=virtio-pci\n")
	if got := detectVideoAccel(vm.paths(), "amd64"); got != AccelNone {
		t.Errorf("vm: %q", got)
	}
	if got := detectVideoAccel(newFakeSystem(t).paths(), "amd64"); got != AccelNone {
		t.Errorf("empty: %q", got)
	}
}
