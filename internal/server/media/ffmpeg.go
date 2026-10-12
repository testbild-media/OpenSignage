package media

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// transcoder is the seam between the job queue and the external ffmpeg/ffprobe binaries.
// Tests replace it to exercise the queue without ffmpeg.
type transcoder interface {
	Probe(ctx context.Context, path string) (Probe, error)
	// ProbeRotation returns the display rotation (0, 90, 180, 270) that ffmpeg applies to
	// the first video frame. Still images carry their EXIF orientation only on the frame,
	// not on the stream, so Probe cannot see it.
	ProbeRotation(ctx context.Context, path string) (int, error)
	// Run executes ffmpeg with args (already complete). onProgress receives the encoded
	// duration in milliseconds.
	Run(ctx context.Context, args []string, onProgress func(outTimeMs int64)) error
}

// Probe is the subset of ffprobe output the media manager uses.
type Probe struct {
	Container  string  `json:"container,omitempty"`
	DurationMs int64   `json:"durationMs,omitempty"`
	Bitrate    int64   `json:"bitrate,omitempty"`
	Width      int     `json:"width,omitempty"` // display size (rotation applied)
	Height     int     `json:"height,omitempty"`
	Rotation   int     `json:"rotation,omitempty"`
	VideoCodec string  `json:"videoCodec,omitempty"`
	PixFmt     string  `json:"pixFmt,omitempty"`
	Profile    string  `json:"profile,omitempty"`
	Level      int     `json:"level,omitempty"` // H.264: 41 = level 4.1 (0 = unknown)
	FPS        float64 `json:"fps,omitempty"`
	AudioCodec string  `json:"audioCodec,omitempty"`
	Channels   int     `json:"channels,omitempty"`
	HasVideo   bool    `json:"hasVideo,omitempty"`
	HasAudio   bool    `json:"hasAudio,omitempty"`
	Frames     int     `json:"frames,omitempty"`
	HasAlpha   bool    `json:"hasAlpha,omitempty"`
}

type ffprobeStream struct {
	CodecType    string            `json:"codec_type"`
	CodecName    string            `json:"codec_name"`
	Profile      string            `json:"profile"`
	Level        int               `json:"level"`
	Width        int               `json:"width"`
	Height       int               `json:"height"`
	PixFmt       string            `json:"pix_fmt"`
	AvgFrameRate string            `json:"avg_frame_rate"`
	RFrameRate   string            `json:"r_frame_rate"`
	NbFrames     string            `json:"nb_frames"`
	Duration     string            `json:"duration"`
	Channels     int               `json:"channels"`
	Disposition  map[string]int    `json:"disposition"`
	Tags         map[string]string `json:"tags"`
	SideData     []struct {
		Type     string  `json:"side_data_type"`
		Rotation float64 `json:"rotation"`
	} `json:"side_data_list"`
}

type ffprobeOut struct {
	Streams []ffprobeStream `json:"streams"`
	Format  struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
}

// parseProbe converts ffprobe's JSON output (-show_format -show_streams) into a Probe.
func parseProbe(data []byte) (Probe, error) {
	var o ffprobeOut
	if err := json.Unmarshal(data, &o); err != nil {
		return Probe{}, fmt.Errorf("ffprobe output: %w", err)
	}
	p := Probe{Container: o.Format.FormatName}
	if v, err := strconv.ParseFloat(o.Format.Duration, 64); err == nil && v > 0 {
		p.DurationMs = int64(v*1000 + 0.5)
	}
	if v, err := strconv.ParseInt(o.Format.BitRate, 10, 64); err == nil && v > 0 {
		p.Bitrate = v
	}
	for _, s := range o.Streams {
		switch s.CodecType {
		case "video":
			if s.Disposition["attached_pic"] == 1 || p.HasVideo {
				continue // cover art or a second video stream
			}
			p.HasVideo = true
			p.VideoCodec, p.PixFmt, p.Profile, p.Level = s.CodecName, s.PixFmt, s.Profile, s.Level
			p.Width, p.Height = s.Width, s.Height
			p.FPS = parseRate(s.AvgFrameRate)
			if p.FPS == 0 {
				p.FPS = parseRate(s.RFrameRate)
			}
			p.Frames, _ = strconv.Atoi(s.NbFrames)
			p.HasAlpha = pixFmtHasAlpha(s.PixFmt)
			rot := 0.0
			for _, sd := range s.SideData {
				if sd.Rotation != 0 {
					rot = sd.Rotation
				}
			}
			if r, err := strconv.ParseFloat(s.Tags["rotate"], 64); err == nil && rot == 0 {
				rot = r
			}
			p.Rotation = ((int(rot) % 360) + 360) % 360
			if p.Rotation == 90 || p.Rotation == 270 {
				p.Width, p.Height = p.Height, p.Width
			}
			if p.DurationMs == 0 {
				if v, err := strconv.ParseFloat(s.Duration, 64); err == nil && v > 0 {
					p.DurationMs = int64(v*1000 + 0.5)
				}
			}
		case "audio":
			if p.HasAudio {
				continue
			}
			p.HasAudio = true
			p.AudioCodec, p.Channels = s.CodecName, s.Channels
			if p.DurationMs == 0 {
				if v, err := strconv.ParseFloat(s.Duration, 64); err == nil && v > 0 {
					p.DurationMs = int64(v*1000 + 0.5)
				}
			}
		}
	}
	return p, nil
}

func parseRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}

func pixFmtHasAlpha(f string) bool {
	switch {
	case strings.HasPrefix(f, "yuva"), strings.HasPrefix(f, "gbrap"), strings.HasPrefix(f, "ya"):
		return true
	}
	switch f {
	case "rgba", "bgra", "argb", "abgr", "rgba64le", "rgba64be", "bgra64le", "pal8":
		return true
	}
	return false
}

// ---- variant planning (pure logic) ---------------------------------------------------------

// outSpec describes one ffmpeg output (a variant).
type outSpec struct {
	Name     string   // thumb, poster, v1080, v1080h, v720, a, orig, i1920, i512
	Ext      string   // output extension including the dot
	Pre      []string // arguments before -i (e.g. -ss)
	Args     []string // arguments after -i
	Alias    bool     // variant is the original blob itself (no ffmpeg run)
	Progress bool     // long-running: report progress
}

// ffmpegArgs assembles the full command line for one output.
func ffmpegArgs(in, out string, s outSpec) []string {
	a := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-progress", "pipe:1", "-nostats",
		"-protocol_whitelist", "file,pipe"} // local files only, whatever the file's content asks for
	a = append(a, s.Pre...)
	a = append(a, "-i", in)
	a = append(a, s.Args...)
	return append(a, out)
}

// fitScale scales down to fit into w×h (never up), keeping the aspect ratio.
func fitScale(w, h int, even bool) string {
	s := fmt.Sprintf("scale=w='min(iw,%d)':h='min(ih,%d)':force_original_aspect_ratio=decrease", w, h)
	if even {
		s += ":force_divisible_by=2"
	}
	return s
}

func exceeds(p Probe, w, h int) bool { return p.Width > w || p.Height > h }

// planImage plans the variants of a raster image. mime is the detected MIME type.
func planImage(p Probe, mime string) []outSpec {
	var out []outSpec
	jpegOut := !p.HasAlpha && mime != "image/png"
	ext, enc := ".png", []string{"-frames:v", "1"}
	if jpegOut {
		ext, enc = ".jpg", []string{"-frames:v", "1", "-q:v", "3"}
	}
	scaled := func(name string, edge int) outSpec {
		return outSpec{Name: name, Ext: ext, Args: append([]string{"-map_metadata", "-1", "-vf", fitScale(edge, edge, false)}, enc...)}
	}
	if mime != "image/gif" { // animated GIFs keep their original; only a thumbnail is made
		if exceeds(p, maxImageEdge, maxImageEdge) {
			out = append(out, scaled("orig", maxImageEdge))
		}
		if exceeds(p, 1920, 1920) {
			out = append(out, scaled("i1920", 1920))
		}
		if exceeds(p, 512, 512) {
			out = append(out, scaled("i512", 512))
		}
	}
	out = append(out, thumbSpec(p))
	return out
}

// thumbSpec makes a 320 px JPEG (fitted, never cropped). Transparent sources are
// composited onto mid-grey so dark logos stay visible.
func thumbSpec(p Probe) outSpec {
	vf := fitScale(320, 320, false)
	if p.HasAlpha {
		vf += ",format=rgba,split[a][b];[a]drawbox=x=0:y=0:w=iw:h=ih:color=0x808080:t=fill[bg];[bg][b]overlay=format=auto,format=yuvj420p"
	}
	return outSpec{Name: "thumb", Ext: ".jpg", Args: []string{"-map_metadata", "-1", "-vf", vf, "-frames:v", "1", "-q:v", "5"}}
}

// Frame rates: v1080 (x86) keeps 25/30/50/60 fps. Above 30 fps a source larger than 720p
// also gets v1080h, full HD at half the frame rate (50 → 25, 60 → 30), for the Raspberry
// Pi 4, which drops about a third of the frames of 1080p60. The v720 proxy for weak players
// (Raspberry Pi 3) always has 25/30 fps.
const (
	maxFPS1080 = 60
	maxFPSHalf = 30 // v1080h and v720
)

// targetFPS returns the output frame rate for a source above max fps: halved until it
// fits when that gives a usual rate (50 → 25, 59.94 → 29.97, 120 → 60), otherwise max.
func targetFPS(f, max float64) float64 {
	if f <= max+0.01 {
		return f
	}
	g := f
	for g > max+0.01 {
		g /= 2
	}
	if g < max*0.76 {
		return max
	}
	return g
}

// remuxable reports whether the source already satisfies the v1080 rules, so the video
// stream can be copied: H.264, yuv420p, ≤1920×1080, ≤60 fps, no rotation, sane bitrate.
// Above 30 fps the level must be known and at most 4.2 (1080p60 hardware decoding).
func remuxable(p Probe) bool {
	if !p.HasVideo || p.VideoCodec != "h264" || p.PixFmt != "yuv420p" {
		return false
	}
	if exceeds(p, 1920, 1080) || p.FPS > maxFPS1080+0.01 || p.Rotation != 0 {
		return false
	}
	high := p.FPS > maxFPSHalf+0.01
	if high && (p.Level == 0 || p.Level > 42) {
		return false
	}
	if p.Bitrate > 25_000_000 && !(high && p.Bitrate <= 40_000_000) {
		return false
	}
	return strings.Contains(p.Container, "mp4") || strings.Contains(p.Container, "matroska")
}

func audioArgs(p Probe) []string {
	if !p.HasAudio {
		return []string{"-an"}
	}
	return []string{"-map", "0:a:0", "-c:a", "aac", "-b:a", "128k", "-ac", "2"}
}

// planVideo plans v1080, v1080h, v720, poster and thumb.
func planVideo(p Probe) []outSpec {
	var out []outSpec
	mapArgs := []string{"-map", "0:v:0"}
	if remuxable(p) {
		a := append([]string{}, mapArgs...)
		switch {
		case !p.HasAudio:
			a = append(a, "-an")
		case p.AudioCodec == "aac":
			a = append(a, "-map", "0:a:0", "-c:a", "copy")
		default:
			a = append(a, audioArgs(p)...)
		}
		a = append(a, "-c:v", "copy", "-sn", "-dn", "-movflags", "+faststart")
		out = append(out, outSpec{Name: "v1080", Ext: ".mp4", Args: a, Progress: true})
	} else {
		out = append(out, outSpec{Name: "v1080", Ext: ".mp4", Args: h264Args(p, 1920, 1080, maxFPS1080), Progress: true})
	}
	// Full HD at half the frame rate for the Raspberry Pi 4. Up to 720p the proxy below has
	// the same size and frame rate, so no extra variant is made.
	if needsHalfRate(p.Width, p.Height, p.FPS) {
		out = append(out, outSpec{Name: "v1080h", Ext: ".mp4", Args: h264Args(p, 1920, 1080, maxFPSHalf), Progress: true})
	}
	// The proxy for weak players: smaller than 1080p, or a high frame rate they cannot decode.
	if needsProxy(p.Width, p.Height, p.FPS) {
		out = append(out, outSpec{Name: "v720", Ext: ".mp4", Args: h264Args(p, 1280, 720, maxFPSHalf), Progress: true})
	}
	ss := posterSeek(p)
	still := func(name string, edgeW, edgeH int, q string) outSpec {
		return outSpec{Name: name, Ext: ".jpg", Pre: []string{"-ss", ss},
			Args: []string{"-map_metadata", "-1", "-an", "-vf", fitScale(edgeW, edgeH, false), "-frames:v", "1", "-q:v", q}}
	}
	out = append(out, still("poster", 1280, 720, "3"), still("thumb", 320, 320, "5"))
	return out
}

// needsHalfRate reports whether a video gets v1080h: above 30 fps and larger than 720p.
func needsHalfRate(w, h int, fps float64) bool {
	return fps > maxFPSHalf+0.01 && (w > 1280 || h > 720)
}

// needsProxy reports whether a video gets the v720 proxy: larger than 720p or above 30 fps.
func needsProxy(w, h int, fps float64) bool {
	return w > 1280 || h > 720 || fps > maxFPSHalf+0.01
}

// h264Args encodes to H.264 within w×h and at most maxFPS; the rate limits follow the
// output (1080p, 720p; doubled above 30 fps, level 4.2 for 1080p50/60).
func h264Args(p Probe, w, h int, maxFPS float64) []string {
	vf := fitScale(w, h, true)
	fps := p.FPS
	if fps > maxFPS+0.01 {
		fps = targetFPS(p.FPS, maxFPS)
		vf += ",fps=" + strconv.FormatFloat(fps, 'f', 3, 64)
	}
	maxrate, bufsize, level := "10M", "20M", "4.1"
	if h <= 720 {
		maxrate, bufsize = "5M", "10M"
	}
	if fps > maxFPSHalf+0.01 {
		maxrate, bufsize, level = "16M", "32M", "4.2"
	}
	a := []string{"-map", "0:v:0"}
	a = append(a, audioArgs(p)...)
	return append(a, "-sn", "-dn", "-vf", vf,
		"-c:v", "libx264", "-preset", "fast", "-crf", "22", "-profile:v", "high", "-level:v", level,
		"-pix_fmt", "yuv420p", "-maxrate", maxrate, "-bufsize", bufsize, "-movflags", "+faststart")
}

func posterSeek(p Probe) string {
	switch {
	case p.DurationMs <= 0:
		return "0"
	case p.DurationMs < 2000:
		return strconv.FormatFloat(float64(p.DurationMs)/2000, 'f', 3, 64)
	}
	return "1"
}

// planAudio plans the single audio variant "a": MP3 and AAC are used as they are,
// everything else becomes AAC in M4A.
func planAudio(p Probe) []outSpec {
	if p.AudioCodec == "mp3" || p.AudioCodec == "aac" {
		return []outSpec{{Name: "a", Alias: true}}
	}
	return []outSpec{{Name: "a", Ext: ".m4a", Progress: true,
		Args: []string{"-vn", "-map", "0:a:0", "-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart"}}}
}

// ---- exec implementation -------------------------------------------------------------------

type execTranscoder struct {
	ffmpeg  string
	ffprobe string
}

// tailBuffer keeps the last bytes written (ffmpeg's error output can be huge).
type tailBuffer struct {
	b   []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return strings.TrimSpace(string(t.b)) }

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func (e *execTranscoder) Probe(ctx context.Context, path string) (Probe, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.ffprobe, probeArgs(path)...)
	var out bytes.Buffer
	stderr := &tailBuffer{max: 2048}
	cmd.Stdout = &limitWriter{w: &out, n: 8 << 20}
	cmd.Stderr = stderr
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Probe{}, fmt.Errorf("ffprobe is not installed (%s)", e.ffprobe)
		}
		if ctx.Err() != nil {
			return Probe{}, fmt.Errorf("ffprobe timed out: %w", ctx.Err())
		}
		return Probe{}, fmt.Errorf("ffprobe failed: %s", firstNonEmpty(lastLine(stderr.String()), err.Error()))
	}
	return parseProbe(out.Bytes())
}

// probeArgs and rotationArgs are the ffprobe command lines. Like ffmpeg, ffprobe may only
// open local files: media containers can reference other protocols (HLS/concat playlists).
func probeArgs(path string) []string {
	return []string{"-hide_banner", "-v", "error", "-protocol_whitelist", "file,pipe", "-print_format", "json",
		"-show_format", "-show_streams", path}
}

func rotationArgs(path string) []string {
	return []string{"-hide_banner", "-v", "error", "-protocol_whitelist", "file,pipe", "-select_streams", "v:0",
		"-read_intervals", "%+#1", "-show_frames", "-show_entries", "frame=side_data_list", "-print_format", "json", path}
}

func (e *execTranscoder) ProbeRotation(ctx context.Context, path string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.ffprobe, rotationArgs(path)...)
	var out bytes.Buffer
	cmd.Stdout = &limitWriter{w: &out, n: 1 << 20}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ffprobe frame probe failed: %w", err)
	}
	return parseFrameRotation(out.Bytes()), nil
}

// parseFrameRotation extracts the display-matrix rotation from `ffprobe -show_frames` JSON.
func parseFrameRotation(data []byte) int {
	var o struct {
		Frames []struct {
			SideData []struct {
				Type     string  `json:"side_data_type"`
				Rotation float64 `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"frames"`
	}
	if json.Unmarshal(data, &o) != nil {
		return 0
	}
	for _, f := range o.Frames {
		for _, sd := range f.SideData {
			if sd.Rotation != 0 {
				return ((int(sd.Rotation) % 360) + 360) % 360
			}
		}
	}
	return 0
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if len(p) > l.n {
		return 0, errors.New("output too large")
	}
	l.n -= len(p)
	return l.w.Write(p)
}

func (e *execTranscoder) Run(ctx context.Context, args []string, onProgress func(int64)) error {
	ctx, cancel := context.WithTimeout(ctx, ffmpegTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.ffmpeg, args...)
	stderr := &tailBuffer{max: 4096}
	cmd.Stderr = stderr
	cmd.WaitDelay = 10 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("ffmpeg is not installed (%s)", e.ffmpeg)
		}
		return fmt.Errorf("cannot start ffmpeg: %w", err)
	}
	lowerPriority(cmd.Process.Pid)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }()
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "out_time_us="); ok && onProgress != nil {
				if us, err := strconv.ParseInt(v, 10, 64); err == nil && us >= 0 {
					onProgress(us / 1000)
				}
			}
		}
	}()
	<-done // stdout closes when ffmpeg exits
	err = cmd.Wait()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("ffmpeg timed out after %s", ffmpegTimeout)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg failed: %s", firstNonEmpty(lastLine(stderr.String()), err.Error()))
	}
	return nil
}
