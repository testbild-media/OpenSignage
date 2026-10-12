package modkit

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOpt(t *testing.T) {
	type req struct {
		A Opt[string] `json:"a"`
		B Opt[int]    `json:"b"`
		C Opt[struct {
			X int `json:"x"`
		}] `json:"c"`
	}
	var r req
	if err := json.Unmarshal([]byte(`{"a":null,"b":5}`), &r); err != nil {
		t.Fatal(err)
	}
	if !r.A.Set || !r.A.Null || r.A.Has() {
		t.Errorf("a must be set+null: %+v", r.A)
	}
	if !r.B.Has() || r.B.Val != 5 {
		t.Errorf("b: %+v", r.B)
	}
	if r.C.Set {
		t.Errorf("c absent must not be set: %+v", r.C)
	}
	// Unknown nested fields stay rejected, like in the enclosing strict decoder.
	if err := json.Unmarshal([]byte(`{"c":{"x":1,"y":2}}`), &r); err == nil {
		t.Error("unknown nested field must be rejected")
	}
}

func TestDecodeOptional(t *testing.T) {
	var v struct {
		N int `json:"n"`
	}
	for body, wantErr := range map[string]bool{"": false, "  \n": false, `{"n":3}`: false, `{"n":3,"x":1}`: true, `{"n":3} {}`: true, `[`: true} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		w := httptest.NewRecorder()
		err := DecodeOptional(w, r, 0, &v)
		if (err != nil) != wantErr {
			t.Errorf("body %q: err=%v wantErr=%v", body, err, wantErr)
		}
	}
}

func TestFonts(t *testing.T) {
	dir := t.TempDir()
	d, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "t.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`CREATE TABLE font_faces (family TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO font_faces(family) VALUES ('Uploaded Sans')`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// No manifest: only Montserrat from the bundled set (development and tests).
	f := NewFonts(dir)
	for fam, want := range map[string]bool{"Montserrat": true, "Lora": false, "Uploaded Sans": true, "": false} {
		if got, err := f.Exists(ctx, d, fam); err != nil || got != want {
			t.Errorf("no manifest, %q: got %v err %v, want %v", fam, got, err, want)
		}
	}

	// With a manifest the listed families are bundled and Montserrat is no longer implied.
	if err := os.MkdirAll(filepath.Join(dir, "fonts"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "fonts", "manifest.json")
	if err := os.WriteFile(manifest, []byte(`{"version":1,"fallback":"Noto Sans","families":[{"family":"Lora"},{"family":"Noto Sans"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for fam, want := range map[string]bool{"Lora": true, "Noto Sans": true, "Montserrat": false, "Uploaded Sans": true} {
		if got, err := f.Exists(ctx, d, fam); err != nil || got != want {
			t.Errorf("manifest, %q: got %v err %v, want %v", fam, got, err, want)
		}
	}

	// A broken manifest is an error, never a silent "everything unknown".
	if err := os.WriteFile(manifest, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Exists(ctx, d, "Lora"); err == nil {
		t.Error("corrupt manifest must return an error")
	}
}
