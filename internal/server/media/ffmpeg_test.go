package media

import (
	"strings"
	"testing"
)

const probeVideoJSON = `{
 "streams": [
  {"codec_type":"video","codec_name":"h264","profile":"High","width":1920,"height":1080,"pix_fmt":"yuv420p",
   "avg_frame_rate":"30000/1001","r_frame_rate":"30000/1001","nb_frames":"240","duration":"8.008000"},
  {"codec_type":"audio","codec_name":"aac","channels":2,"duration":"8.010000"}
 ],
 "format": {"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"8.010000","bit_rate":"4000000"}
}`

func TestParseProbe(t *testing.T) {
	p, err := parseProbe([]byte(probeVideoJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasVideo || !p.HasAudio || p.Width != 1920 || p.Height != 1080 || p.VideoCodec != "h264" || p.AudioCodec != "aac" ||
		p.DurationMs != 8010 || p.Bitrate != 4000000 || p.Frames != 240 || p.PixFmt != "yuv420p" {
		t.Fatalf("%+v", p)
	}
	if p.FPS < 29.96 || p.FPS > 29.98 {
		t.Fatalf("fps %v", p.FPS)
	}

	// phone video: stored 1920×1080 with a 90° display matrix → displayed 1080×1920
	rot := `{"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"pix_fmt":"yuv420p","avg_frame_rate":"30/1",
	  "side_data_list":[{"side_data_type":"Display Matrix","rotation":-90}]}],"format":{"format_name":"mov,mp4","duration":"3"}}`
	p, _ = parseProbe([]byte(rot))
	if p.Width != 1080 || p.Height != 1920 || p.Rotation != 270 {
		t.Fatalf("rotation: %+v", p)
	}
	if remuxable(p) {
		t.Fatal("rotated video must be re-encoded")
	}

	// cover art in an MP3 is not a video stream
	mp3 := `{"streams":[{"codec_type":"audio","codec_name":"mp3","channels":2},
	  {"codec_type":"video","codec_name":"mjpeg","width":500,"height":500,"disposition":{"attached_pic":1}}],"format":{"format_name":"mp3","duration":"61.5"}}`
	p, _ = parseProbe([]byte(mp3))
	if p.HasVideo || !p.HasAudio || p.DurationMs != 61500 || p.AudioCodec != "mp3" {
		t.Fatalf("cover art: %+v", p)
	}

	// an alpha PNG
	alpha := `{"streams":[{"codec_type":"video","codec_name":"png","width":100,"height":50,"pix_fmt":"rgba","avg_frame_rate":"0/0"}],"format":{"format_name":"png_pipe"}}`
	p, _ = parseProbe([]byte(alpha))
	if !p.HasAlpha || p.FPS != 0 {
		t.Fatalf("alpha: %+v", p)
	}
	if _, err := parseProbe([]byte("not json")); err == nil {
		t.Fatal("garbage must be an error")
	}
}

func names2(specs []outSpec) string {
	var n []string
	for _, s := range specs {
		n = append(n, s.Name)
	}
	return strings.Join(n, ",")
}

func find(specs []outSpec, name string) outSpec {
	for _, s := range specs {
		if s.Name == name {
			return s
		}
	}
	return outSpec{}
}

func TestPlanVideoRemuxWhenConforming(t *testing.T) {
	p, _ := parseProbe([]byte(probeVideoJSON))
	specs := planVideo(p)
	if got := names2(specs); got != "v1080,v720,poster,thumb" {
		t.Fatalf("plan: %s", got)
	}
	v := strings.Join(find(specs, "v1080").Args, " ")
	if !strings.Contains(v, "-c:v copy") || !strings.Contains(v, "+faststart") || !strings.Contains(v, "-c:a copy") || strings.Contains(v, "libx264") {
		t.Fatalf("conforming source must be remuxed: %s", v)
	}
	// non-AAC audio is converted while the video stream is still copied
	p.AudioCodec = "ac3"
	v = strings.Join(find(planVideo(p), "v1080").Args, " ")
	if !strings.Contains(v, "-c:v copy") || !strings.Contains(v, "-c:a aac") || !strings.Contains(v, "128k") {
		t.Fatalf("ac3 audio: %s", v)
	}
	// no audio
	p.HasAudio = false
	if v = strings.Join(find(planVideo(p), "v1080").Args, " "); !strings.Contains(v, "-an") || !strings.Contains(v, "-c:v copy") {
		t.Fatalf("silent video: %s", v)
	}
}

func TestPlanVideoTranscodeCases(t *testing.T) {
	base, _ := parseProbe([]byte(probeVideoJSON))
	mod := map[string]func(p *Probe){
		"hevc":           func(p *Probe) { p.VideoCodec = "hevc" },
		"10 bit":         func(p *Probe) { p.PixFmt = "yuv420p10le" },
		"4:2:2":          func(p *Probe) { p.PixFmt = "yuv422p" },
		"4k":             func(p *Probe) { p.Width, p.Height = 3840, 2160 },
		"60 fps level 5": func(p *Probe) { p.FPS, p.Level = 59.94, 51 },
		"60 fps level ?": func(p *Probe) { p.FPS = 59.94 },
		"75 fps":         func(p *Probe) { p.FPS, p.Level = 75, 42 },
		"huge bitrate":   func(p *Probe) { p.Bitrate = 80_000_000 },
		"avi container":  func(p *Probe) { p.Container = "avi" },
		"portrait 4k":    func(p *Probe) { p.Width, p.Height = 2160, 3840 },
		"1440p":          func(p *Probe) { p.Width, p.Height = 2560, 1440 },
	}
	for name, f := range mod {
		p := base
		f(&p)
		v := find(planVideo(p), "v1080")
		a := strings.Join(v.Args, " ")
		for _, want := range []string{"libx264", "-profile:v high", "-pix_fmt yuv420p", "-movflags +faststart", "-c:a aac", "-b:a 128k", "force_divisible_by=2", "-maxrate"} {
			if !strings.Contains(a, want) {
				t.Errorf("%s: v1080 lacks %q: %s", name, want, a)
			}
		}
		if strings.Contains(a, "-c:v copy") {
			t.Errorf("%s: must not be copied", name)
		}
	}
	// Frame rates: v1080 keeps 50/60 fps, the v720 proxy has 25/30 fps.
	p := base
	p.FPS = 59.94
	a := strings.Join(find(planVideo(p), "v1080").Args, " ")
	if strings.Contains(a, "fps=") || !strings.Contains(a, "-level:v 4.2") || !strings.Contains(a, "-maxrate 16M") {
		t.Errorf("59.94 fps stays in v1080 (level 4.2): %s", a)
	}
	if a := strings.Join(find(planVideo(p), "v720").Args, " "); !strings.Contains(a, "fps=29.970") || !strings.Contains(a, "-level:v 4.1") {
		t.Errorf("v720 proxy at 29.97: %s", a)
	}
	// v1080h: full HD at half the frame rate for the Raspberry Pi 4
	if got := names2(planVideo(p)); got != "v1080,v1080h,v720,poster,thumb" {
		t.Errorf("1080p59.94 plan: %s", got)
	}
	if a := strings.Join(find(planVideo(p), "v1080h").Args, " "); !strings.Contains(a, "min(iw,1920)") || !strings.Contains(a, "fps=29.970") ||
		!strings.Contains(a, "-level:v 4.1") || !strings.Contains(a, "-maxrate 10M") {
		t.Errorf("v1080h at 29.97: %s", a)
	}
	p.VideoCodec = "hevc"
	p.FPS = 50
	if a := strings.Join(find(planVideo(p), "v1080").Args, " "); strings.Contains(a, "fps=") || !strings.Contains(a, "libx264") {
		t.Errorf("HEVC 1080p50 → H.264 1080p50: %s", a)
	}
	if a := strings.Join(find(planVideo(p), "v720").Args, " "); !strings.Contains(a, "fps=25.000") {
		t.Errorf("HEVC 1080p50 proxy at 25: %s", a)
	}
	if a := strings.Join(find(planVideo(p), "v1080h").Args, " "); !strings.Contains(a, "fps=25.000") || !strings.Contains(a, "min(ih,1080)") {
		t.Errorf("HEVC 1080p50 → v1080h at 25: %s", a)
	}
	p.Width, p.Height = 3840, 2160
	if got := names2(planVideo(p)); got != "v1080,v1080h,v720,poster,thumb" {
		t.Errorf("4K50 plan: %s", got)
	}
	// v720 for sources larger than 1280×720 or with more than 30 fps
	p = base
	p.Width, p.Height, p.FPS = 1280, 720, 60
	if got := names2(planVideo(p)); got != "v1080,v720,poster,thumb" {
		t.Errorf("720p60 gets a 30 fps proxy (no v1080h, the proxy is the same): %s", got)
	}
	p = base
	p.Width, p.Height = 3840, 2160
	if got := names2(planVideo(p)); got != "v1080,v720,poster,thumb" {
		t.Errorf("4k plan: %s", got)
	}
	p.Width, p.Height = 1280, 720
	p.VideoCodec = "vp9"
	if got := names2(planVideo(p)); got != "v1080,poster,thumb" {
		t.Errorf("720p plan: %s", got)
	}
	// poster time: 1 s, or the middle of very short clips
	p.DurationMs = 10000
	if find(planVideo(p), "poster").Pre[1] != "1" {
		t.Error("poster at 1 s")
	}
	p.DurationMs = 800
	if s := find(planVideo(p), "poster").Pre[1]; s != "0.400" {
		t.Errorf("short clip poster: %s", s)
	}
}

func TestTargetFPS(t *testing.T) {
	for in, want := range map[float64]float64{24: 24, 25: 25, 29.97: 29.97, 30: 30, 50: 25, 59.94: 29.97, 60: 30, 120: 30, 48: 24, 40: 30, 100: 25} {
		if got := targetFPS(in, 30); got < want-0.01 || got > want+0.01 {
			t.Errorf("targetFPS(%v, 30) = %v, want %v", in, got, want)
		}
	}
	for in, want := range map[float64]float64{25: 25, 50: 50, 59.94: 59.94, 60: 60, 120: 60, 100: 50, 144: 60, 75: 60} {
		if got := targetFPS(in, 60); got < want-0.01 || got > want+0.01 {
			t.Errorf("targetFPS(%v, 60) = %v, want %v", in, got, want)
		}
	}
}

// Conforming H.264 at 50/60 fps is copied when its level allows 1080p60 hardware decoding
// (≤ 4.2), with a 30 fps proxy beside it.
func TestRemux60fps(t *testing.T) {
	p, _ := parseProbe([]byte(probeVideoJSON))
	p.FPS, p.Level = 50, 42
	specs := planVideo(p)
	if a := strings.Join(find(specs, "v1080").Args, " "); !strings.Contains(a, "-c:v copy") {
		t.Fatalf("1080p50 level 4.2 is copied: %s", a)
	}
	if a := strings.Join(find(specs, "v720").Args, " "); !strings.Contains(a, "fps=25.000") {
		t.Fatalf("its proxy has 25 fps: %s", a)
	}
	if a := strings.Join(find(specs, "v1080h").Args, " "); !strings.Contains(a, "fps=25.000") || !strings.Contains(a, "libx264") {
		t.Fatalf("its half frame rate variant is encoded at 25 fps: %s", a)
	}
	p.Bitrate = 35_000_000
	if !remuxable(p) {
		t.Fatal("up to 40 Mbit/s above 30 fps")
	}
	if p.Level = 0; remuxable(p) {
		t.Fatal("unknown level above 30 fps is re-encoded")
	}
	lv := `{"streams":[{"codec_type":"video","codec_name":"h264","level":42,"width":1920,"height":1080,"pix_fmt":"yuv420p","avg_frame_rate":"60/1"}],"format":{"format_name":"mov,mp4","duration":"3"}}`
	if q, _ := parseProbe([]byte(lv)); q.Level != 42 || q.FPS != 60 {
		t.Fatalf("level parsed: %+v", q)
	}
}

func TestPlanImage(t *testing.T) {
	jpg := Probe{HasVideo: true, VideoCodec: "mjpeg", PixFmt: "yuvj420p", Width: 6000, Height: 4000}
	if got := names2(planImage(jpg, "image/jpeg")); got != "orig,i1920,i512,thumb" {
		t.Fatalf("huge jpeg: %s", got)
	}
	if o := find(planImage(jpg, "image/jpeg"), "orig"); o.Ext != ".jpg" || !strings.Contains(strings.Join(o.Args, " "), "min(iw,3840)") {
		t.Fatalf("orig: %+v", o)
	}
	hd := Probe{HasVideo: true, VideoCodec: "mjpeg", Width: 1920, Height: 1080}
	if got := names2(planImage(hd, "image/jpeg")); got != "i512,thumb" {
		t.Fatalf("full hd: %s", got)
	}
	portrait := Probe{HasVideo: true, Width: 1080, Height: 1920}
	if got := names2(planImage(portrait, "image/jpeg")); got != "i512,thumb" {
		t.Fatalf("portrait full hd: %s", got)
	}
	tiny := Probe{HasVideo: true, Width: 300, Height: 200}
	if got := names2(planImage(tiny, "image/png")); got != "thumb" {
		t.Fatalf("tiny: %s", got)
	}
	// transparency stays PNG, but thumbnails are flattened onto grey JPEGs
	alpha := Probe{HasVideo: true, VideoCodec: "png", PixFmt: "rgba", HasAlpha: true, Width: 3000, Height: 2000}
	specs := planImage(alpha, "image/png")
	if find(specs, "i1920").Ext != ".png" {
		t.Fatal("alpha png → png")
	}
	th := find(specs, "thumb")
	if th.Ext != ".jpg" || !strings.Contains(strings.Join(th.Args, " "), "overlay") {
		t.Fatalf("alpha thumb: %+v", th)
	}
	// webp without alpha → jpeg, webp with alpha → png
	if find(planImage(Probe{HasVideo: true, Width: 3000, Height: 2000}, "image/webp"), "i1920").Ext != ".jpg" {
		t.Fatal("webp → jpg")
	}
	if find(planImage(Probe{HasVideo: true, Width: 3000, Height: 2000, HasAlpha: true}, "image/webp"), "i1920").Ext != ".png" {
		t.Fatal("alpha webp → png")
	}
	// animated GIFs are not re-encoded (only a thumbnail)
	if got := names2(planImage(Probe{HasVideo: true, Width: 4000, Height: 3000, Frames: 20, PixFmt: "pal8", HasAlpha: true}, "image/gif")); got != "thumb" {
		t.Fatalf("gif: %s", got)
	}
}

func TestPlanAudio(t *testing.T) {
	for codec, alias := range map[string]bool{"mp3": true, "aac": true, "pcm_s16le": false, "flac": false, "vorbis": false, "opus": false, "alac": false} {
		specs := planAudio(Probe{HasAudio: true, AudioCodec: codec})
		if len(specs) != 1 || specs[0].Name != "a" || specs[0].Alias != alias {
			t.Errorf("%s: %+v", codec, specs)
		}
		if !alias {
			a := strings.Join(specs[0].Args, " ")
			if specs[0].Ext != ".m4a" || !strings.Contains(a, "-vn") || !strings.Contains(a, "-c:a aac") {
				t.Errorf("%s: %s", codec, a)
			}
		}
	}
}

func TestFFmpegArgsAreNonInteractive(t *testing.T) {
	args := ffmpegArgs("/in", "/out.mp4", outSpec{Pre: []string{"-ss", "1"}, Args: []string{"-an"}})
	if args[0] != "-nostdin" || args[len(args)-1] != "/out.mp4" {
		t.Fatalf("%v", args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-ss 1 -i /in -an /out.mp4") || !strings.Contains(joined, "-y") {
		t.Fatalf("%s", joined)
	}
}

func TestDiskLevels(t *testing.T) {
	for pct, want := range map[float64]string{50: "ok", 20: "ok", 19.9: "warning", 10: "warning", 9.9: "critical", 0: "critical"} {
		if got := DiskLevel(pct); got != want {
			t.Errorf("DiskLevel(%v) = %s, want %s", pct, got, want)
		}
	}
}
