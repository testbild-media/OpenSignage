package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// After an update with a new font family, browsers must not keep the old manifest.json
// (it was cached for a day, so the new family stayed invisible in the editor).
func TestBundledFontCaching(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "barlow"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"manifest.json": `{"version":1}`, "barlow/Barlow-Regular.woff2": "wOF2"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := http.StripPrefix("/fonts/", cacheFonts(http.FileServer(http.Dir(dir))))
	for url, want := range map[string]string{
		"/fonts/manifest.json":                         "no-cache",
		"/fonts/barlow/Barlow-Regular.woff2?v=0123abc": "public, max-age=86400",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != want {
			t.Errorf("%s: %d %q, want %q", url, w.Code, w.Header().Get("Cache-Control"), want)
		}
	}
}
