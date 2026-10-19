package media

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSFNT builds a structurally valid (but empty) TrueType/OpenType font: header, table
// directory with cmap/head/hhea/maxp and zero-padded tables.
func fakeSFNT(magic string, extra int) []byte {
	tags := []string{"cmap", "head", "hhea", "maxp"}
	var b bytes.Buffer
	b.WriteString(magic)
	_ = binary.Write(&b, binary.BigEndian, uint16(len(tags)))
	b.Write(make([]byte, 6))
	off := 12 + 16*len(tags)
	for _, tag := range tags {
		b.WriteString(tag)
		_ = binary.Write(&b, binary.BigEndian, uint32(0))
		_ = binary.Write(&b, binary.BigEndian, uint32(off))
		_ = binary.Write(&b, binary.BigEndian, uint32(16))
		off += 16
	}
	b.Write(make([]byte, 16*len(tags)+extra))
	return b.Bytes()
}

func fakeWOFF2() []byte {
	b := make([]byte, 200)
	copy(b, "wOF2")
	binary.BigEndian.PutUint32(b[8:], uint32(len(b)))
	binary.BigEndian.PutUint16(b[12:], 5)
	return b
}

func (c *client) fontUpload(fields map[string]string, filename string, file []byte) (int, map[string]any) {
	c.h.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if file != nil {
		fw, _ := mw.CreateFormFile("file", filename)
		_, _ = fw.Write(file)
	}
	_ = mw.Close()
	res, b := c.req("POST", "/api/v1/fonts", &body, map[string]string{"Content-Type": mw.FormDataContentType()})
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return res.StatusCode, out
}

func goodFont(family string) map[string]string {
	return map[string]string{"family": family, "style": "normal", "weightMin": "400", "weightMax": "400", "license": "SIL OFL 1.1", "licenseConfirmed": "true"}
}

func TestFontUploadValidation(t *testing.T) {
	h := newHarness(t)
	admin := h.login("admin")
	ttf := fakeSFNT("\x00\x01\x00\x00", 100)

	// bundled families are reserved (manifest in the share directory)
	_ = os.MkdirAll(filepath.Join(h.dir, "share", "fonts"), 0o750)
	manifest := `{"families":[{"family":"Inter","category":"sans-serif","license":"OFL"},{"family":"Roboto Slab","category":"serif"}]}`
	if err := os.WriteFile(filepath.Join(h.dir, "share", "fonts", "manifest.json"), []byte(manifest), 0o640); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		mod    func(m map[string]string)
		file   []byte
		code   int
		errKey string
	}{
		{"missing file", nil, nil, 422, "invalid_field"},
		{"missing family", func(m map[string]string) { delete(m, "family") }, ttf, 422, "invalid_field"},
		{"family with quote", func(m map[string]string) { m["family"] = `Bad"Name` }, ttf, 422, "invalid_field"},
		{"missing license", func(m map[string]string) { delete(m, "license") }, ttf, 422, "invalid_field"},
		{"license not confirmed", func(m map[string]string) { m["licenseConfirmed"] = "false" }, ttf, 422, "invalid_field"},
		{"license confirmation missing", func(m map[string]string) { delete(m, "licenseConfirmed") }, ttf, 422, "invalid_field"},
		{"bad style", func(m map[string]string) { m["style"] = "oblique" }, ttf, 422, "invalid_field"},
		{"weight out of range", func(m map[string]string) { m["weightMin"] = "5000" }, ttf, 422, "invalid_field"},
		{"weight order", func(m map[string]string) { m["weightMin"] = "700"; m["weightMax"] = "300" }, ttf, 422, "invalid_field"},
		{"not a font (png)", nil, pngBytes(20, 20, 1), 415, "invalid_font"},
		{"exe renamed to ttf", nil, append([]byte("MZ\x90\x00"), make([]byte, 300)...), 415, "invalid_font"},
		{"truncated table directory", nil, fakeSFNT("OTTO", 0)[:40], 415, "invalid_font"},
		{"bundled family", func(m map[string]string) { m["family"] = "inter" }, ttf, 409, "font_family_reserved"},
	}
	for _, tc := range cases {
		f := goodFont("My Font")
		if tc.mod != nil {
			tc.mod(f)
		}
		code, out := admin.fontUpload(f, "font.ttf", tc.file)
		if code != tc.code || errCode(out) != tc.errKey {
			t.Errorf("%s: got %d %v, want %d %s", tc.name, code, out, tc.code, tc.errKey)
		}
	}
	if h.count(`SELECT COUNT(*) FROM font_faces`) != 0 {
		t.Fatal("nothing may be stored for rejected uploads")
	}

	// only admins may upload
	op := h.login("operator")
	if code, _ := op.fontUpload(goodFont("Op Font"), "f.ttf", ttf); code != 403 {
		t.Fatalf("operator upload: %d", code)
	}
	// too large
	big := fakeSFNT("true", maxFontSize)
	if code, out := admin.fontUpload(goodFont("Big"), "f.ttf", big); code != 413 || errCode(out) != "file_too_large" {
		t.Fatalf("oversize: %d %v", code, out)
	}

	// success, each accepted format
	for name, file := range map[string][]byte{"My TTF": ttf, "My OTF": fakeSFNT("OTTO", 10), "My TrueType": fakeSFNT("true", 10), "My WOFF2": fakeWOFF2()} {
		code, out := admin.fontUpload(goodFont(name), "f.bin", file)
		if code != 200 || out["family"] != name || out["url"] == "" {
			t.Fatalf("%s: %d %v", name, code, out)
		}
	}
	// the same face twice is a conflict
	if code, out := admin.fontUpload(goodFont("My TTF"), "f.ttf", ttf); code != 409 || errCode(out) != "font_exists" {
		t.Fatalf("duplicate: %d %v", code, out)
	}
	// a different style of the same family is fine
	f := goodFont("My TTF")
	f["style"] = "italic"
	if code, out := admin.fontUpload(f, "f.ttf", fakeSFNT("\x00\x01\x00\x00", 55)); code != 200 {
		t.Fatalf("italic: %d %v", code, out)
	}
	if h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'font.added'`) != 5 {
		t.Fatal("font uploads are audited")
	}
}

func TestFontListServeDeleteAndUsage(t *testing.T) {
	h := newHarness(t)
	admin := h.login("admin")
	user := h.login("user")
	_ = os.MkdirAll(filepath.Join(h.dir, "share", "fonts"), 0o750)
	_ = os.WriteFile(filepath.Join(h.dir, "share", "fonts", "manifest.json"), []byte(`[{"family":"Inter","category":"sans-serif"},{"family":"Lobster"}]`), 0o640)

	ttf := fakeSFNT("\x00\x01\x00\x00", 100)
	code, face := admin.fontUpload(goodFont("Bar Script"), "bar.ttf", ttf)
	if code != 200 {
		t.Fatalf("upload: %d %v", code, face)
	}
	id := face["id"].(string)
	sub := h.app.Bus.Subscribe(16)
	defer sub.Close()

	// any role may list: bundled + custom
	out := user.mustOK("GET", "/api/v1/fonts", nil)
	fonts := out["fonts"].([]any)
	if len(fonts) != 3 {
		t.Fatalf("fonts: %v", fonts)
	}
	bySource := map[string]int{}
	for _, f := range fonts {
		bySource[f.(map[string]any)["source"].(string)]++
	}
	if bySource["bundled"] != 2 || bySource["custom"] != 1 {
		t.Fatalf("sources: %v", bySource)
	}
	// and serve the file (role user), with caching headers and the right type
	res, body := user.req("GET", "/fonts/custom/"+id, nil, nil)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "font/ttf" || !bytes.Equal(body, ttf) {
		t.Fatalf("font file: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if !strings.Contains(res.Header.Get("Cache-Control"), "immutable") || res.Header.Get("ETag") == "" {
		t.Fatal("font caching headers")
	}
	if res, _ = user.req("GET", "/fonts/custom/unknown", nil, nil); res.StatusCode != 404 {
		t.Fatalf("unknown font: %d", res.StatusCode)
	}
	if code, _ := user.do("DELETE", "/api/v1/fonts/"+id, nil); code != 403 {
		t.Fatalf("user delete: %d", code)
	}

	// referenced by a presentation draft → cannot be deleted
	h.exec(`INSERT INTO presentations(id, name, draft, created_at, updated_at) VALUES ('p1', 'Menu', ?, 'x', 'x')`,
		`{"slides":[{"elements":[{"type":"text","style":{"font":"Bar Script","size":40}}]}]}`)
	usedBy := func() string { // where the list says the family is used (JSON, keys sorted)
		t.Helper()
		for _, f := range user.mustOK("GET", "/api/v1/fonts", nil)["fonts"].([]any) {
			if m := f.(map[string]any); m["family"] == "Bar Script" {
				b, _ := json.Marshal(m["usedBy"])
				if n := len(m["usedBy"].([]any)); m["usage"].(float64) != float64(n) {
					t.Fatalf("usage %v != %d users", m["usage"], n)
				}
				return string(b)
			}
		}
		t.Fatal("Bar Script not listed")
		return ""
	}
	if got := usedBy(); got != `[{"id":"p1","name":"Menu","states":["draft"],"type":"presentation"}]` {
		t.Fatalf("used by: %s", got)
	}
	code, o := admin.do("DELETE", "/api/v1/fonts/"+id, nil)
	if code != 409 || errCode(o) != "font_in_use" {
		t.Fatalf("delete used font: %d %v", code, o)
	}
	if b, _ := json.Marshal(o["error"].(map[string]any)["params"].(map[string]any)["usedBy"]); !strings.Contains(string(b), `"name":"Menu"`) {
		t.Fatalf("the refusal names where the font is used: %s", b)
	}
	if h.count(`SELECT COUNT(*) FROM font_faces`) != 1 {
		t.Fatal("font must stay")
	}
	// ... also when only the published version uses it
	h.exec(`UPDATE presentations SET draft = '{}', published_version = 1`)
	h.exec(`INSERT INTO presentation_versions(presentation_id, version, doc, published_at) VALUES ('p1', 1, ?, 'x')`, `{"typography":{"heading":{"font": "bar script"}}}`)
	if got := usedBy(); got != `[{"id":"p1","name":"Menu","states":["published"],"type":"presentation"}]` {
		t.Fatalf("used by the published version: %s", got)
	}
	if code, o = admin.do("DELETE", "/api/v1/fonts/"+id, nil); code != 409 || errCode(o) != "font_in_use" {
		t.Fatalf("delete font used by the published version: %d %v", code, o)
	}
	// not by an older version in the history or a deleted presentation: nobody can find them
	// there (restoring such a version warns and blocks publishing until the font is changed)
	h.exec(`INSERT INTO presentation_versions(presentation_id, version, doc, published_at) VALUES ('p1', 2, '{}', 'x')`)
	h.exec(`UPDATE presentations SET published_version = 2`)
	h.exec(`INSERT INTO presentations(id, name, draft, created_at, updated_at, archived_at) VALUES ('p2', 'Old', '{"font":"Bar Script"}', 'x', 'x', 'x')`)
	if got := usedBy(); got != `[]` {
		t.Fatalf("old version and deleted presentation must not count: %s", got)
	}
	// a template and a ticker template do
	h.exec(`INSERT INTO presentations(id, name, draft, is_template, created_at, updated_at) VALUES ('p3', 'Vorlage', '{"font":"Bar Script"}', 1, 'x', 'x')`)
	h.exec(`INSERT INTO ticker_templates(id, name, style, created_at, updated_at) VALUES ('t1', 'T', '{"font":"Bar Script"}', 'x', 'x')`)
	if got := usedBy(); got != `[{"id":"p3","name":"Vorlage","states":["draft"],"type":"template"},{"id":"t1","name":"T","type":"ticker"}]` {
		t.Fatalf("template and ticker template: %s", got)
	}
	if code, o = admin.do("DELETE", "/api/v1/fonts/"+id, nil); code != 409 {
		t.Fatalf("ticker template usage: %d %v", code, o)
	}
	h.exec(`DELETE FROM presentations WHERE id = 'p3'`)
	h.exec(`DELETE FROM ticker_templates`)

	// the presentation defaults of the settings do too
	h.exec(`INSERT INTO settings(key, value, updated_at) VALUES ('settings', ?, 'x')`, `{"presentations":{"text":{"font":"Bar Script","color":"#ffffff"}}}`)
	if got := usedBy(); got != `[{"id":"defaults","name":"","type":"settings"}]` {
		t.Fatalf("presentation defaults: %s", got)
	}
	if code, o = admin.do("DELETE", "/api/v1/fonts/"+id, nil); code != 409 {
		t.Fatalf("defaults usage: %d %v", code, o)
	}
	h.exec(`DELETE FROM settings WHERE key = 'settings'`)

	// free → deletable; the blob is collected
	if code, _ = admin.do("DELETE", "/api/v1/fonts/"+id, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if !waitTopic(sub, "content", time.Second) {
		t.Fatal("deleting a font publishes a content event")
	}
	if h.count(`SELECT COUNT(*) FROM font_faces`) != 0 || len(h.blobFiles()) != 0 {
		t.Fatalf("font and file must be gone: files=%v", h.blobFiles())
	}
	if h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'font.deleted'`) != 1 {
		t.Fatal("delete is audited")
	}
}

func TestParseFontManifestVariants(t *testing.T) {
	for name, doc := range map[string]string{
		"array":         `[{"family":"A"},{"name":"B"}]`,
		"families":      `{"families":[{"family":"A"},{"family":"B"}]}`,
		"fonts keyed":   `{"fonts":{"A":{"category":"x"},"B":{}}}`,
		"top-level map": `{"A":{"license":{"name":"OFL"}},"B":{"license":"OFL"}}`,
	} {
		got, err := parseFontManifest([]byte(doc))
		if err != nil || len(got) != 2 || got[0].Family != "A" || got[1].Family != "B" {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
	if _, err := parseFontManifest([]byte(`{broken`)); err == nil {
		t.Error("broken json must be an error")
	}
}

func TestDetectFontFormats(t *testing.T) {
	dir := t.TempDir()
	w := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, b, 0o640)
		return p
	}
	for want, b := range map[string][]byte{"truetype": fakeSFNT("\x00\x01\x00\x00", 4), "opentype": fakeSFNT("OTTO", 4), "woff2": fakeWOFF2()} {
		p := w(want, b)
		if got, err := detectFont(p, int64(len(b))); err != nil || got != want {
			t.Errorf("%s: %q %v", want, got, err)
		}
	}
	woff := make([]byte, 100)
	copy(woff, "wOFF")
	binary.BigEndian.PutUint32(woff[8:], 100)
	binary.BigEndian.PutUint16(woff[12:], 3)
	if got, err := detectFont(w("w", woff), 100); err != nil || got != "woff" {
		t.Errorf("woff: %q %v", got, err)
	}
	// header says the file is longer than it is
	binary.BigEndian.PutUint32(woff[8:], 5000)
	if _, err := detectFont(w("w2", woff), 100); err == nil {
		t.Error("inconsistent woff length must be rejected")
	}
	if _, err := detectFont(w("junk", []byte("hello world, this is not a font")), 31); err == nil {
		t.Error("junk")
	}
}
