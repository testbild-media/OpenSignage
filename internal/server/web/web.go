// Package web serves the embedded admin WebGUI and operator UI (single page apps).
package web

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:static
var static embed.FS

// Embedded reports an error when the web interface was not built into this binary
// (used by the self-test after a server update).
func Embedded() error {
	for _, entry := range []string{"static/index.html", "static/control.html"} {
		if st, err := fs.Stat(static, entry); err != nil || st.Size() == 0 {
			return fmt.Errorf("web interface not embedded: %s missing", entry)
		}
	}
	return nil
}

// Handler serves static files; unknown paths fall back to the SPA entry point
// (index.html, or control.html below /control).
func Handler() http.Handler {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" && !strings.HasSuffix(r.URL.Path, "/") {
			if st, err := fs.Stat(sub, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		entry := "index.html"
		if p == "control" || strings.HasPrefix(p, "control/") {
			entry = "control.html"
		}
		data, err := fs.ReadFile(sub, entry)
		if err != nil {
			http.Error(w, "web interface not built (run scripts/build.sh)", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
	})
}
