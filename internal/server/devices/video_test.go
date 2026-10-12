package devices

import (
	"testing"

	"opensignage/internal/contracts"
)

// A device that switched "auto" hardware decoding off after decode errors raises a warning;
// devices decoding in software by setting, or older agents without video data, never do.
func TestVideoFallback(t *testing.T) {
	cases := []struct {
		name string
		v    *contracts.StatusVideo
		want bool
	}{
		{"no data", nil, false},
		{"hardware", &contracts.StatusVideo{HW: true, Path: "v4l2"}, false},
		{"software by setting", &contracts.StatusVideo{Path: "vaapi-vulkan"}, false},
		{"fallback", &contracts.StatusVideo{Path: "v4l2", Fallback: "decode_errors", FallbackAt: "2026-10-11T21:00:00Z"}, true},
	}
	for _, c := range cases {
		if got := videoFallback(contracts.DeviceStatus{Video: c.v}); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	if alertSeverity[AlertVideoFallback] != SeverityWarning {
		t.Fatal("video_fallback is a warning")
	}
}

// A player that lost the GPU and could not get it back by restarts raises a warning; a
// device that never had the GPU (software) or got it back (recoveries) does not.
func TestGPULostAlert(t *testing.T) {
	cases := []struct {
		name string
		v    *contracts.StatusVideo
		want bool
	}{
		{"no data", nil, false},
		{"hardware after recoveries", &contracts.StatusVideo{HW: true, GPU: "hardware", GPURecoveries: 2}, false},
		{"never had a GPU", &contracts.StatusVideo{GPU: "software"}, false},
		{"lost", &contracts.StatusVideo{GPU: "lost", GPURecoveries: 3, GPULostAt: "2026-10-12T06:33:08Z"}, true},
	}
	for _, c := range cases {
		if got := gpuLost(contracts.DeviceStatus{Video: c.v}); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	if alertSeverity[AlertGPULost] != SeverityWarning {
		t.Fatal("gpu_lost is a warning")
	}
}
