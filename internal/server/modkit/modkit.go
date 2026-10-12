// Package modkit holds small helpers shared by the catalog and content modules: optional
// JSON fields that distinguish "absent" from "null", the bundled/uploaded font lookup,
// optional request bodies and name cleaning.
package modkit

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"opensignage/internal/server/httpx"
)

// Queryer is implemented by *sql.DB and *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Opt is an optional JSON field: Set is true when the key was present, Null when its value
// was JSON null (e.g. "clear this field"), otherwise Val holds the value.
type Opt[T any] struct {
	Set  bool
	Null bool
	Val  T
}

// UnmarshalJSON implements json.Unmarshaler.
func (o *Opt[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	var zero T
	o.Val = zero
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Null = true
		return nil
	}
	o.Null = false
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields() // keep the strictness of the enclosing request decoder
	return dec.Decode(&o.Val)
}

// Has reports whether the key was present with a non-null value.
func (o Opt[T]) Has() bool { return o.Set && !o.Null }

// CleanName trims s and checks that it is 1..max characters.
func CleanName(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	return s, n >= 1 && n <= max
}

// IsConstraint reports whether err is a SQLite constraint failure of the given kind
// ("UNIQUE" or "FOREIGN KEY").
func IsConstraint(err error, kind string) bool {
	return err != nil && strings.Contains(err.Error(), kind)
}

// DecodeOptional decodes a strict JSON body but accepts an empty body (v stays untouched).
func DecodeOptional(w http.ResponseWriter, r *http.Request, maxBytes int64, v any) error {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return httpx.ErrTooLarge
		}
		return httpx.ErrBadRequest.Wrap(err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return httpx.ErrBadRequest.With("detail", err.Error()).Wrap(err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return httpx.ErrBadRequest.With("detail", "trailing data")
	}
	return nil
}

// Fonts answers "does this font family exist": bundled fonts from the manifest in the share
// directory plus uploaded families from font_faces. When the manifest file does not exist
// (development, tests) only "Montserrat" is accepted from the bundled set.
type Fonts struct {
	manifest string

	mu       sync.Mutex
	mtime    time.Time
	size     int64
	families map[string]bool
	missing  bool
}

// NewFonts creates a lookup for <shareDir>/fonts/manifest.json.
func NewFonts(shareDir string) *Fonts {
	return &Fonts{manifest: filepath.Join(shareDir, "fonts", "manifest.json")}
}

type manifestFile struct {
	Families []struct {
		Family string `json:"family"`
	} `json:"families"`
}

func (f *Fonts) bundled() (map[string]bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, err := os.Stat(f.manifest)
	switch {
	case errors.Is(err, os.ErrNotExist):
		f.families, f.missing = nil, true
		return nil, true, nil
	case err != nil:
		return nil, false, fmt.Errorf("fonts manifest: %w", err)
	}
	if f.families != nil && st.ModTime().Equal(f.mtime) && st.Size() == f.size {
		return f.families, false, nil
	}
	raw, err := os.ReadFile(f.manifest)
	if err != nil {
		return nil, false, fmt.Errorf("fonts manifest: %w", err)
	}
	var mf manifestFile
	if err := json.Unmarshal(raw, &mf); err != nil {
		return nil, false, fmt.Errorf("fonts manifest %s: %w", f.manifest, err)
	}
	fams := make(map[string]bool, len(mf.Families))
	for _, x := range mf.Families {
		if x.Family != "" {
			fams[x.Family] = true
		}
	}
	f.families, f.mtime, f.size, f.missing = fams, st.ModTime(), st.Size(), false
	return fams, false, nil
}

// Exists reports whether the family is bundled or uploaded.
func (f *Fonts) Exists(ctx context.Context, q Queryer, family string) (bool, error) {
	if strings.TrimSpace(family) == "" {
		return false, nil
	}
	fams, missing, err := f.bundled()
	if err != nil {
		return false, err
	}
	if missing && family == "Montserrat" || fams[family] {
		return true, nil
	}
	var one int
	err = q.QueryRowContext(ctx, `SELECT 1 FROM font_faces WHERE family = ? LIMIT 1`, family).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// NextPosition is the smallest free place among the screens of the zone in the group
// ("" = the screens without group), not counting the device except.
func NextPosition(ctx context.Context, q Queryer, zoneID, groupID, except string) (int, error) {
	rows, err := q.QueryContext(ctx, `SELECT position FROM devices WHERE zone_id = ? AND COALESCE(group_id, '') = ? AND id <> ?
		AND position IS NOT NULL`, zoneID, groupID, except)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	taken := map[int]bool{}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return 0, err
		}
		taken[p] = true
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	p := 1
	for taken[p] {
		p++
	}
	return p, nil
}
