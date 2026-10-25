package media

import (
	"bytes"
	"strings"
	"testing"
)

func pad(b []byte) []byte { return append(b, make([]byte, 600)...) }

// textPad pads text formats with spaces (real text files contain no NUL bytes).
func textPad(b []byte) []byte { return append(b, bytes.Repeat([]byte(" "), 600)...) }

func ftyp(major string, compat ...string) []byte {
	body := append([]byte("ftyp"+major), 0, 0, 0, 0)
	for _, c := range compat {
		body = append(body, []byte(c)...)
	}
	size := len(body) + 4
	return pad(append([]byte{0, 0, byte(size >> 8), byte(size)}, body...))
}

func TestDetectTypeByContent(t *testing.T) {
	cases := []struct {
		name  string
		data  []byte
		kind  string
		mime  string
		ambig bool
	}{
		{"jpeg", pad([]byte("\xff\xd8\xff\xe0\x00\x10JFIF")), KindImage, "image/jpeg", false},
		{"png", pad([]byte("\x89PNG\r\n\x1a\n")), KindImage, "image/png", false},
		{"gif89", pad([]byte("GIF89a")), KindImage, "image/gif", false},
		{"gif87", pad([]byte("GIF87a")), KindImage, "image/gif", false},
		{"webp", pad([]byte("RIFF\x24\x00\x00\x00WEBPVP8 ")), KindImage, "image/webp", false},
		{"avif", ftyp("avif", "mif1", "avif"), KindImage, "image/avif", false},
		{"avif sequence", ftyp("avis", "avif"), KindImage, "image/avif", false},
		{"heic (recognised, rejected)", ftyp("heic", "mif1", "heic"), "", "image/heic", false},
		{"svg plain", textPad([]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)), KindImage, "image/svg+xml", false},
		{"svg with prolog and comment", textPad([]byte("\xef\xbb\xbf<?xml version=\"1.0\"?>\n<!-- made by hand -->\n<!DOCTYPE svg PUBLIC \"x\" \"y\">\n<SVG/>")), KindImage, "image/svg+xml", false},
		{"mp4 isom", ftyp("isom", "isom", "iso2", "mp41"), KindVideo, "video/mp4", true},
		{"mp4 mp42", ftyp("mp42", "mp42"), KindVideo, "video/mp4", true},
		{"mov", ftyp("qt  ", "qt  "), KindVideo, "video/quicktime", true},
		{"old mov (moov first)", pad([]byte("\x00\x00\x10\x00moov")), KindVideo, "video/quicktime", true},
		{"3gp", ftyp("3gp4", "3gp4"), KindVideo, "video/3gpp", true},
		{"m4a", ftyp("M4A ", "M4A ", "mp42"), KindAudio, "audio/mp4", false},
		{"webm", pad([]byte("\x1a\x45\xdf\xa3\x93B\x82\x84webm")), KindVideo, "video/webm", true},
		{"mkv", pad([]byte("\x1a\x45\xdf\xa3\x93B\x82\x88matroska")), KindVideo, "video/x-matroska", true},
		{"avi", pad([]byte("RIFF\x24\x00\x00\x00AVI LIST")), KindVideo, "video/x-msvideo", false},
		{"mp3 with id3", pad([]byte("ID3\x04\x00\x00\x00\x00\x00\x00")), KindAudio, "audio/mpeg", false},
		{"mp3 frame", pad([]byte{0xFF, 0xFB, 0x90, 0x64}), KindAudio, "audio/mpeg", false},
		{"aac adts", pad([]byte{0xFF, 0xF1, 0x50, 0x80, 0x01, 0x3F, 0xFC}), KindAudio, "audio/aac", false},
		{"wav", pad([]byte("RIFF\x24\x00\x00\x00WAVEfmt ")), KindAudio, "audio/wav", false},
		{"ogg", pad([]byte("OggS\x00\x02")), KindAudio, "audio/ogg", true},
		{"flac", pad([]byte("fLaC\x00\x00\x00\x22")), KindAudio, "audio/flac", false},
	}
	for _, c := range cases {
		got := DetectType(c.data)
		if got.Kind != c.kind || got.Mime != c.mime || got.Ambiguous != c.ambig {
			t.Errorf("%s: got %+v, want kind=%q mime=%q ambiguous=%v", c.name, got, c.kind, c.mime, c.ambig)
		}
		if c.kind != "" && !got.Allowed() {
			t.Errorf("%s: should be allowed", c.name)
		}
	}
}

func TestDetectTypeRejectsOthers(t *testing.T) {
	for name, data := range map[string][]byte{
		"windows exe":           pad([]byte("MZ\x90\x00\x03")),
		"elf":                   pad([]byte("\x7fELF\x02\x01\x01")),
		"pdf":                   pad([]byte("%PDF-1.7\n")),
		"zip":                   pad([]byte("PK\x03\x04")),
		"html":                  pad([]byte("<!DOCTYPE html><html><body>hi</body></html>")),
		"script":                pad([]byte("#!/bin/sh\nrm -rf /\n")),
		"text":                  []byte(strings.Repeat("hello ", 200)),
		"xml not svg":           pad([]byte(`<?xml version="1.0"?><note><to>x</to></note>`)),
		"svg lookalike in text": pad([]byte("this is not an <svg> document")),
		"bmp":                   pad([]byte("BM\x36\x00")),
		"tiff":                  pad([]byte("II*\x00")),
		"empty":                 nil,
		"riff other":            pad([]byte("RIFF\x24\x00\x00\x00XXXXdata")),
		"binary junk":           bytes.Repeat([]byte{0x00, 0xAA, 0x55}, 300),
	} {
		if got := DetectType(data); got.Allowed() {
			t.Errorf("%s must be rejected, got %+v", name, got)
		}
	}
}

func TestCheckSVGBytes(t *testing.T) {
	ok := []string{
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><circle r="5"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="#a"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><image href="data:image/png;base64,AAAA"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><text>Join online now</text></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><style>.a{fill:red}</style><rect class="a"/></svg>`,
	}
	for _, s := range ok {
		if p := checkSVGBytes([]byte(s)); p != "" {
			t.Errorf("safe SVG rejected (%s): %s", p, s)
		}
	}
	bad := map[string]string{
		"<SCRIPT>":       `<svg xmlns="http://www.w3.org/2000/svg"><SCRIPT>x</SCRIPT></svg>`,
		"onclick":        `<svg xmlns="http://www.w3.org/2000/svg"><rect onclick = "x()"/></svg>`,
		"onmouseover":    `<svg xmlns="http://www.w3.org/2000/svg"><rect fill="red" onmouseover="x()"/></svg>`,
		"js url":         `<svg xmlns="http://www.w3.org/2000/svg"><a xlink:href=" JaVa&#x09;Script:alert(1)"/></svg>`,
		"https href":     `<svg xmlns="http://www.w3.org/2000/svg"><use href="https://evil/x.svg#a"/></svg>`,
		"proto relative": `<svg xmlns="http://www.w3.org/2000/svg"><image href="//evil/x.png"/></svg>`,
		"css url":        `<svg xmlns="http://www.w3.org/2000/svg"><rect style="fill:url(http://evil/a)"/></svg>`,
		"css import":     `<svg xmlns="http://www.w3.org/2000/svg"><style>@import 'x.css';</style></svg>`,
		"entity":         `<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><svg xmlns="http://www.w3.org/2000/svg">&x;</svg>`,
		"iframe":         `<svg xmlns="http://www.w3.org/2000/svg"><iframe src="x"/></svg>`,
		"data html":      `<svg xmlns="http://www.w3.org/2000/svg"><a href="data:text/html,<script>1</script>"/></svg>`,
	}
	for name, s := range bad {
		if p := checkSVGBytes([]byte(s)); p == "" {
			t.Errorf("%s: unsafe SVG accepted", name)
		}
	}
}

func TestSVGSize(t *testing.T) {
	for _, c := range []struct {
		doc  string
		w, h int
	}{
		{`<svg width="120" height="80"/>`, 120, 80},
		{`<svg viewBox="0 0 640 480"/>`, 640, 480},
		{`<svg width="2in" height="1in" viewBox="0 0 10 10"/>`, 192, 96},
		{`<svg width="100%" height="50%" viewBox="0 0 30 20"/>`, 30, 20},
		{`<svg/>`, 0, 0},
		{`not xml`, 0, 0},
	} {
		if w, h := svgSize([]byte(c.doc)); w != c.w || h != c.h {
			t.Errorf("%s: %dx%d", c.doc, w, h)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	for in, want := range map[string]string{
		"photo.jpg":                "photo.jpg",
		"../../etc/passwd":         "passwd",
		`C:\Users\x\evil.exe`:      "evil.exe",
		"a/b/c.png":                "c.png",
		" spaced name .png ":       "spaced name .png",
		"tab\there.png":            "tabhere.png",
		"rtl\u202egnp.exe":         "rtlgnp.exe",
		"Käse & Brötchen (1).jpeg": "Käse & Brötchen (1).jpeg",
	} {
		if got, ok := sanitizeFilename(in); !ok || got != want {
			t.Errorf("%q → %q (%v), want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "   ", ".", "..", "dir/", "\x00\x01"} {
		if got, ok := sanitizeFilename(bad); ok {
			t.Errorf("%q must be rejected, got %q", bad, got)
		}
	}
	long := strings.Repeat("ä", 300) + ".png"
	if got, _ := sanitizeFilename(long); len([]rune(got)) != 200 {
		t.Errorf("length: %d", len([]rune(got)))
	}
}

func TestDownloadName(t *testing.T) {
	for _, c := range [][3]string{
		{"Red Bull", ".png", "Red Bull.png"},
		{"pixel.png", ".png", "pixel.png"},
		{"Logo.PNG", ".png", "Logo.PNG"},
		{"photo.jpeg", ".jpg", "photo.jpeg"},
		{"Red Bull 0.25l", ".png", "Red Bull 0.25l.png"},
		{"clip", ".mp4", "clip.mp4"},
		{"unknown", "", "unknown"},
		{"", ".jpg", "media.jpg"},
	} {
		if got := downloadName(c[0], c[1]); got != c[2] {
			t.Errorf("%q %q: %q, want %q", c[0], c[1], got, c[2])
		}
	}
}
