package app

import (
	"encoding/pem"
	"net/http"
	"path"
)

func pemCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// cacheFonts sets the caching of the bundled fonts: manifest.json is revalidated on every
// load, because an update can add families; the font files are kept for a day, and their
// URLs carry the content hash (?v=…, fontFaceCss), so a changed file is fetched at once.
func cacheFonts(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", fontCacheControl(r.URL.Path))
		h.ServeHTTP(w, r)
	})
}

func fontCacheControl(p string) string {
	if path.Base(p) == "manifest.json" {
		return "no-cache"
	}
	return "public, max-age=86400"
}
