package media

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Media kinds.
const (
	KindImage = "image"
	KindVideo = "video"
	KindAudio = "audio"
)

// TypeInfo is the result of content based type detection.
type TypeInfo struct {
	Kind string // image | video | audio ("" = not an allowed type)
	Mime string // detected MIME type (also set for recognised but rejected types)
	Ext  string
	// Ambiguous containers (ISO-BMFF, Matroska, Ogg) can hold audio only or video; the
	// probe step settles the kind.
	Ambiguous bool
}

// Allowed reports whether the type may be stored.
func (t TypeInfo) Allowed() bool { return t.Kind != "" }

// DetectType identifies a file by its first bytes (never by file name). head should hold
// at least 512 bytes where available.
func DetectType(head []byte) TypeInfo {
	if t, ok := detectOwn(head); ok {
		return t
	}
	// Fallback on the standard library sniffer for the types it knows reliably.
	switch strings.Split(http.DetectContentType(head), ";")[0] {
	case "image/jpeg":
		return TypeInfo{KindImage, "image/jpeg", ".jpg", false}
	case "image/png":
		return TypeInfo{KindImage, "image/png", ".png", false}
	case "image/gif":
		return TypeInfo{KindImage, "image/gif", ".gif", false}
	case "image/webp":
		return TypeInfo{KindImage, "image/webp", ".webp", false}
	case "audio/mpeg":
		return TypeInfo{KindAudio, "audio/mpeg", ".mp3", false}
	case "audio/wave":
		return TypeInfo{KindAudio, "audio/wav", ".wav", false}
	case "audio/ogg", "application/ogg":
		return TypeInfo{KindAudio, "audio/ogg", ".ogg", true}
	case "video/mp4":
		return TypeInfo{KindVideo, "video/mp4", ".mp4", true}
	case "video/webm":
		return TypeInfo{KindVideo, "video/webm", ".webm", true}
	case "video/avi":
		return TypeInfo{KindVideo, "video/x-msvideo", ".avi", false}
	}
	return TypeInfo{Mime: http.DetectContentType(head)}
}

func detectOwn(h []byte) (TypeInfo, bool) {
	switch {
	case len(h) >= 3 && h[0] == 0xFF && h[1] == 0xD8 && h[2] == 0xFF:
		return TypeInfo{KindImage, "image/jpeg", ".jpg", false}, true
	case bytes.HasPrefix(h, []byte("\x89PNG\r\n\x1a\n")):
		return TypeInfo{KindImage, "image/png", ".png", false}, true
	case bytes.HasPrefix(h, []byte("GIF87a")), bytes.HasPrefix(h, []byte("GIF89a")):
		return TypeInfo{KindImage, "image/gif", ".gif", false}, true
	case len(h) >= 12 && string(h[0:4]) == "RIFF":
		switch string(h[8:12]) {
		case "WEBP":
			return TypeInfo{KindImage, "image/webp", ".webp", false}, true
		case "WAVE":
			return TypeInfo{KindAudio, "audio/wav", ".wav", false}, true
		case "AVI ":
			return TypeInfo{KindVideo, "video/x-msvideo", ".avi", false}, true
		}
		return TypeInfo{Mime: "application/x-riff"}, true
	case bytes.HasPrefix(h, []byte("OggS")):
		return TypeInfo{KindAudio, "audio/ogg", ".ogg", true}, true
	case bytes.HasPrefix(h, []byte("fLaC")):
		return TypeInfo{KindAudio, "audio/flac", ".flac", false}, true
	case bytes.HasPrefix(h, []byte{0x1A, 0x45, 0xDF, 0xA3}):
		window := h[4:min(len(h), 80)]
		if bytes.Contains(window, []byte("webm")) {
			return TypeInfo{KindVideo, "video/webm", ".webm", true}, true
		}
		return TypeInfo{KindVideo, "video/x-matroska", ".mkv", true}, true
	case len(h) >= 12 && string(h[4:8]) == "ftyp":
		return detectISOBMFF(h), true
	case len(h) >= 8 && isOldQuickTimeAtom(h[4:8]):
		return TypeInfo{KindVideo, "video/quicktime", ".mov", true}, true
	case bytes.HasPrefix(h, []byte("ID3")):
		return TypeInfo{KindAudio, "audio/mpeg", ".mp3", false}, true
	case isMPEGFrame(h):
		return TypeInfo{KindAudio, "audio/mpeg", ".mp3", false}, true
	case isADTS(h):
		return TypeInfo{KindAudio, "audio/aac", ".aac", false}, true
	case isSVGHead(h):
		return TypeInfo{KindImage, "image/svg+xml", ".svg", false}, true
	}
	return TypeInfo{}, false
}

func isOldQuickTimeAtom(b []byte) bool {
	switch string(b) {
	case "moov", "mdat", "wide", "free", "skip":
		return true
	}
	return false
}

func detectISOBMFF(h []byte) TypeInfo {
	major := string(h[8:12])
	brands := []string{major}
	size := int(binary.BigEndian.Uint32(h[0:4]))
	if size < 16 || size > len(h) {
		size = len(h)
	}
	for i := 16; i+4 <= size; i += 4 {
		brands = append(brands, string(h[i:i+4]))
	}
	has := func(b ...string) bool {
		for _, x := range brands {
			for _, y := range b {
				if x == y {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has("avif", "avis"):
		return TypeInfo{KindImage, "image/avif", ".avif", false}
	case major == "heic" || major == "heix" || major == "heim" || major == "heis" || major == "hevc" || major == "hevx" || major == "mif1" || major == "msf1":
		return TypeInfo{Mime: "image/heic"} // recognised, not allowed
	case major == "qt  ":
		return TypeInfo{KindVideo, "video/quicktime", ".mov", true}
	case major == "M4A " || major == "M4B " || major == "M4P ":
		return TypeInfo{KindAudio, "audio/mp4", ".m4a", false}
	case strings.HasPrefix(major, "3g"):
		return TypeInfo{KindVideo, "video/3gpp", ".3gp", true}
	}
	return TypeInfo{KindVideo, "video/mp4", ".mp4", true}
}

// isMPEGFrame checks for a valid MPEG audio (layer III) frame header.
func isMPEGFrame(h []byte) bool {
	if len(h) < 4 || h[0] != 0xFF || h[1]&0xE0 != 0xE0 {
		return false
	}
	version := (h[1] >> 3) & 3
	layer := (h[1] >> 1) & 3
	bitrate := h[2] >> 4
	rate := (h[2] >> 2) & 3
	return version != 1 && layer == 1 && bitrate != 0 && bitrate != 15 && rate != 3
}

// isADTS checks for an AAC ADTS frame header.
func isADTS(h []byte) bool {
	if len(h) < 7 || h[0] != 0xFF || h[1]&0xF6 != 0xF0 {
		return false
	}
	rate := (h[2] >> 2) & 0x0F
	return rate < 13
}

// sniffMIME returns the detected MIME type for the blobs table.
func sniffMIME(head []byte) string {
	t := DetectType(head)
	if t.Mime != "" {
		return t.Mime
	}
	return "application/octet-stream"
}

// isSVGHead recognises an SVG document: only whitespace, an XML declaration, comments or
// a DOCTYPE may precede the <svg root element.
func isSVGHead(h []byte) bool {
	s := bytes.TrimPrefix(h, []byte("\xef\xbb\xbf"))
	if bytes.IndexByte(s, 0) >= 0 {
		return false
	}
	for {
		s = bytes.TrimLeft(s, " \t\r\n")
		switch {
		case bytes.HasPrefix(s, []byte("<?")):
			i := bytes.Index(s, []byte("?>"))
			if i < 0 {
				return false
			}
			s = s[i+2:]
		case bytes.HasPrefix(s, []byte("<!--")):
			i := bytes.Index(s, []byte("-->"))
			if i < 0 {
				return false
			}
			s = s[i+3:]
		case bytes.HasPrefix(bytes.ToLower(s[:min(len(s), 9)]), []byte("<!doctype")):
			i := bytes.IndexByte(s, '>')
			if i < 0 {
				return false
			}
			// an internal subset ("[ … ]") may contain '>' – skip it conservatively
			if j := bytes.IndexByte(s[:i], '['); j >= 0 {
				k := bytes.Index(s, []byte("]>"))
				if k < 0 {
					return false
				}
				i = k + 1
			}
			s = s[i+1:]
		default:
			return len(s) >= 4 && bytes.EqualFold(s[:4], []byte("<svg"))
		}
	}
}

// ---- SVG safety -------------------------------------------------------------------------

const maxSVGSize = 5 << 20

var (
	svgEventHandler = regexp.MustCompile(`(?i)[\s"'/;]on[a-z]{2,}\s*=`)
	svgExternalRef  = regexp.MustCompile(`(?i)(?:href|src)\s*=\s*["']?\s*(?:https?:|ftp:|file:|//)`)
	svgExternalURL  = regexp.MustCompile(`(?i)url\(\s*["']?\s*(?:https?:|ftp:|file:|//)`)
	svgNumEntity    = regexp.MustCompile(`&#(?:x([0-9a-fA-F]+)|([0-9]+));?`)
)

// svgProblem describes why an SVG is refused ("" = safe).
type svgProblem string

// checkSVGBytes inspects a whole SVG document. It rejects scripts, event handler
// attributes, javascript: URLs, foreignObject/iframe/embed/object, entity declarations and
// any external reference. SVGs are rendered as <img> by the players and served with a
// restrictive CSP, so this is a second line of defence.
func checkSVGBytes(b []byte) svgProblem {
	s := strings.ToLower(string(b))
	// decode numeric entities so "&#106;avascript:" cannot hide a scheme
	s = svgNumEntity.ReplaceAllStringFunc(s, func(m string) string {
		sub := svgNumEntity.FindStringSubmatch(m)
		var n int64
		var err error
		if sub[1] != "" {
			n, err = strconv.ParseInt(sub[1], 16, 32)
		} else {
			n, err = strconv.ParseInt(sub[2], 10, 32)
		}
		if err != nil || n <= 0 || n > 0x10FFFF {
			return ""
		}
		return string(rune(n))
	})
	switch {
	case strings.Contains(s, "<script"):
		return "script"
	case svgEventHandler.MatchString(s):
		return "event_handler"
	case strings.Contains(stripSpace(s), "javascript:"), strings.Contains(stripSpace(s), "vbscript:"):
		return "javascript_url"
	case strings.Contains(s, "<foreignobject"), strings.Contains(s, "<iframe"), strings.Contains(s, "<embed"), strings.Contains(s, "<object"):
		return "embedded_content"
	case strings.Contains(s, "<!entity"):
		return "entity"
	case svgExternalRef.MatchString(s), svgExternalURL.MatchString(s), strings.Contains(s, "@import"):
		return "external_reference"
	case strings.Contains(stripSpace(s), "data:text/html"):
		return "data_url"
	}
	return ""
}

func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if r <= ' ' {
			return -1
		}
		return r
	}, s)
}

// svgSize reads the intrinsic size of an SVG from width/height or the viewBox.
func svgSize(b []byte) (w, h int) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	for {
		tok, err := dec.Token()
		if err != nil {
			return 0, 0
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !strings.EqualFold(se.Name.Local, "svg") {
			return 0, 0
		}
		var width, height float64
		var vbW, vbH float64
		for _, a := range se.Attr {
			switch strings.ToLower(a.Name.Local) {
			case "width":
				width = parseSVGLength(a.Value)
			case "height":
				height = parseSVGLength(a.Value)
			case "viewbox":
				f := strings.FieldsFunc(a.Value, func(r rune) bool { return r == ' ' || r == ',' })
				if len(f) == 4 {
					vbW, _ = strconv.ParseFloat(f[2], 64)
					vbH, _ = strconv.ParseFloat(f[3], 64)
				}
			}
		}
		if width <= 0 {
			width = vbW
		}
		if height <= 0 {
			height = vbH
		}
		return int(width + 0.5), int(height + 0.5)
	}
}

func parseSVGLength(s string) float64 {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || strings.HasSuffix(s, "%") {
		return 0
	}
	unit := 1.0
	for suffix, f := range map[string]float64{"px": 1, "pt": 96.0 / 72, "pc": 16, "mm": 96 / 25.4, "cm": 96 / 2.54, "in": 96} {
		if strings.HasSuffix(s, suffix) {
			s, unit = strings.TrimSuffix(s, suffix), f
			break
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 {
		return 0
	}
	return v * unit
}

var errSVGTooLarge = errors.New("svg too large")

// readSVG reads a whole SVG (bounded) from r.
func readSVG(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxSVGSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSVGSize {
		return nil, errSVGTooLarge
	}
	return b, nil
}
