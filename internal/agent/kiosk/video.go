package kiosk

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// VideoAccel is the way this device can decode video in hardware.
type VideoAccel string

const (
	// AccelNone: no known working path. "on" still asks Chromium to try (generic flags);
	// it falls back to software decoding by itself.
	AccelNone VideoAccel = ""
	// AccelV4L2: Raspberry Pi with Chromium from the Raspberry Pi repository. That build
	// decodes H.264 with the V4L2 codec driver by default; it only must not be switched off.
	AccelV4L2 VideoAccel = "v4l2"
	// AccelVAAPI: Intel/AMD on x86 with a VA-API driver and a hardware Vulkan driver.
	// Chromium (≥ 131) on X11 only renders VA-API frames when it composites with Vulkan:
	// with ANGLE on GLX it cannot import NV12 frames and the copy fallback fails, so the
	// decoder gives up after the first frame and FFmpeg decodes in software.
	AccelVAAPI VideoAccel = "vaapi-vulkan"
)

// HWVideoEnabled resolves the device setting auto | on | off. "auto" is on wherever a
// working hardware path is detected (hardware decoding is the default on every client):
// the Raspberry Pi with its own Chromium build and Intel/AMD with VA-API + Vulkan. The
// agent falls back to software for the session when it fails (decode errors, player
// crashes).
func HWVideoEnabled(mode string, accel VideoAccel) bool {
	switch mode {
	case "on":
		return true
	case "off":
		return false
	}
	return accel == AccelV4L2 || accel == AccelVAAPI
}

// hwVideoArgs returns the Chromium flags for the resolved setting.
func hwVideoArgs(on bool, accel VideoAccel) []string {
	if !on {
		return []string{"--disable-accelerated-video-decode"}
	}
	switch accel {
	case AccelV4L2:
		// No extra flags: the Raspberry Pi build decodes with V4L2 by default, and an own
		// --enable-features would replace one the distribution sets (the last one wins).
		return nil
	case AccelVAAPI:
		// VaapiIgnoreDriverChecks: Chromium accepts VA-API with Vulkan only on Intel by
		// itself; AMD (Mesa radeonsi) needs the check skipped.
		return []string{"--use-gl=angle", "--use-angle=vulkan",
			"--enable-features=Vulkan,VulkanFromANGLE,DefaultANGLEVulkan,VaapiIgnoreDriverChecks"}
	}
	return []string{"--enable-features=AcceleratedVideoDecodeLinuxGL,AcceleratedVideoDecodeLinuxZeroCopyGL,VaapiIgnoreDriverChecks",
		"--ignore-gpu-blocklist"}
}

// videoPaths are the system locations DetectVideoAccel reads (replaceable in tests).
type videoPaths struct {
	model      string   // device tree model
	dpkgStatus string   // dpkg database
	drm        string   // /sys/class/drm
	vaDrivers  []string // libva driver directories
	vulkanICDs []string // Vulkan ICD manifest directories
}

func systemVideoPaths() videoPaths {
	va := []string{"/usr/lib/x86_64-linux-gnu/dri", "/usr/lib64/dri", "/usr/lib/dri"}
	if p := os.Getenv("LIBVA_DRIVERS_PATH"); p != "" {
		va = append(filepath.SplitList(p), va...)
	}
	return videoPaths{
		model:      "/proc/device-tree/model",
		dpkgStatus: "/var/lib/dpkg/status",
		drm:        "/sys/class/drm",
		vaDrivers:  va,
		vulkanICDs: []string{"/usr/share/vulkan/icd.d", "/etc/vulkan/icd.d"},
	}
}

// DetectVideoAccel finds the hardware decoding path of this device (Linux only).
func DetectVideoAccel() VideoAccel {
	if runtime.GOOS != "linux" {
		return AccelNone
	}
	return detectVideoAccel(systemVideoPaths(), runtime.GOARCH)
}

func detectVideoAccel(p videoPaths, arch string) VideoAccel {
	if b, err := os.ReadFile(p.model); err == nil &&
		strings.HasPrefix(strings.TrimSpace(strings.TrimRight(string(b), "\x00")), "Raspberry Pi") &&
		strings.Contains(chromiumVersion(p.dpkgStatus), "rpt") {
		return AccelV4L2
	}
	if arch != "amd64" {
		return AccelNone
	}
	var va, icd []string
	switch gpuDriver(p.drm) {
	case "i915", "xe":
		va = []string{"iHD_drv_video.so", "i965_drv_video.so"}
		icd = []string{"intel_icd", "intel_hasvk_icd"}
	case "amdgpu":
		va = []string{"radeonsi_drv_video.so"}
		icd = []string{"radeon_icd", "amd_icd"}
	default:
		return AccelNone
	}
	if hasFile(p.vaDrivers, func(n string) bool { return slices.Contains(va, n) }) &&
		hasFile(p.vulkanICDs, func(n string) bool {
			for _, pre := range icd {
				if strings.HasPrefix(n, pre+".") || strings.HasPrefix(n, pre+"64.") {
					return true
				}
			}
			return false
		}) {
		return AccelVAAPI
	}
	return AccelNone
}

// chromiumVersion returns the version of the installed chromium (or chromium-browser)
// package from the dpkg database ("" if none). Raspberry Pi builds carry "+rpt".
func chromiumVersion(status string) string {
	f, err := os.Open(status)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var pkg, ver, st string
	check := func() string {
		if (pkg == "chromium" || pkg == "chromium-browser") && strings.HasSuffix(st, " installed") {
			return ver
		}
		return ""
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if v := check(); v != "" {
				return v
			}
			pkg, ver, st = "", "", ""
		case strings.HasPrefix(line, "Package: "):
			pkg = strings.TrimSpace(line[len("Package: "):])
		case strings.HasPrefix(line, "Version: "):
			ver = strings.TrimSpace(line[len("Version: "):])
		case strings.HasPrefix(line, "Status: "):
			st = strings.TrimSpace(line[len("Status: "):])
		}
	}
	return check()
}

// gpuDriver returns the kernel driver of the first DRM render node driven by i915, xe or
// amdgpu ("" if none).
func gpuDriver(drm string) string {
	nodes, _ := filepath.Glob(filepath.Join(drm, "renderD*"))
	for _, n := range nodes {
		b, err := os.ReadFile(filepath.Join(n, "device", "uevent"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if d, ok := strings.CutPrefix(strings.TrimSpace(line), "DRIVER="); ok && slices.Contains([]string{"i915", "xe", "amdgpu"}, d) {
				return d
			}
		}
	}
	return ""
}

func hasFile(dirs []string, match func(string) bool) bool {
	for _, d := range dirs {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			if match(e.Name()) {
				return true
			}
		}
	}
	return false
}
