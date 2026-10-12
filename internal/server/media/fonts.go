package media

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"opensignage/internal/common/atomicfile"
	"opensignage/internal/common/ids"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
)

var fontMIME = map[string]string{
	"truetype": "font/ttf",
	"opentype": "font/otf",
	"woff":     "font/woff",
	"woff2":    "font/woff2",
}

type fontFaceView struct {
	ID         string `json:"id"`
	Family     string `json:"family"`
	Style      string `json:"style"`
	WeightMin  int    `json:"weightMin"`
	WeightMax  int    `json:"weightMax"`
	Format     string `json:"format"`
	License    string `json:"license"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	UploadedBy string `json:"uploadedBy"`
	CreatedAt  string `json:"createdAt"`
	URL        string `json:"url"`
}

type fontFamilyView struct {
	Family   string         `json:"family"`
	Source   string         `json:"source"` // bundled | custom
	Category string         `json:"category,omitempty"`
	License  string         `json:"license,omitempty"`
	Faces    []fontFaceView `json:"faces,omitempty"`
	Usage    int            `json:"usage"`  // number of users (len(UsedBy))
	UsedBy   []fontUser     `json:"usedBy"` // where the family is used, so it can be changed there
}

// ---- bundled fonts ---------------------------------------------------------------------

type bundledFont struct {
	Family   string
	Category string
	License  string
}

// bundledFonts reads <ShareDir>/fonts/manifest.json. The layout is tolerated loosely: an
// array of entries, or an object with "families"/"fonts" (array or object keyed by family);
// each entry carries "family" (or "name"). A missing file means no bundled fonts.
func (m *Module) bundledFonts() ([]bundledFont, error) {
	b, err := os.ReadFile(filepath.Join(m.app.Cfg.ShareDir, "fonts", "manifest.json"))
	if isNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseFontManifest(b)
}

func parseFontManifest(b []byte) ([]bundledFont, error) {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	var out []bundledFont
	add := func(key string, v any) {
		mp, _ := v.(map[string]any)
		fam := key
		if s, ok := mp["family"].(string); ok && s != "" {
			fam = s
		} else if s, ok := mp["name"].(string); ok && s != "" {
			fam = s
		}
		if fam == "" {
			return
		}
		bf := bundledFont{Family: fam}
		bf.Category, _ = mp["category"].(string)
		switch l := mp["license"].(type) {
		case string:
			bf.License = l
		case map[string]any:
			bf.License, _ = l["name"].(string)
		}
		out = append(out, bf)
	}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case []any:
			for _, it := range t {
				add("", it)
			}
		case map[string]any:
			if f, ok := t["families"]; ok {
				walk(f)
				return
			}
			if f, ok := t["fonts"]; ok {
				walk(f)
				return
			}
			for k, it := range t { // keyed by family name
				if _, isObj := it.(map[string]any); isObj {
					add(k, it)
				}
			}
		}
	}
	walk(raw)
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Family) < strings.ToLower(out[j].Family) })
	return out, nil
}

// ---- usage ---------------------------------------------------------------------------

var fontRefRe = regexp.MustCompile(`"font"\s*:\s*("(?:[^"\\]|\\.)*")`)

// fontUser is content that uses a font family.
type fontUser struct {
	Type   string   `json:"type"` // presentation | template | ticker
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	States []string `json:"states,omitempty"` // draft, published (presentations and templates)
}

// fontUsage lists, per lower-cased family, the content whose JSON names the family in a
// "font" field: the draft and the published version of every presentation and template
// that is not deleted, and every ticker template. Like media usage, older versions in the
// version history and deleted presentations do not count – nobody could find them to change
// the font. Restoring such a version with a deleted font warns, and publishing it is
// refused until the font is changed (restoreVersion, document validation).
func fontUsage(ctx context.Context, q queryer) (map[string][]fontUser, error) {
	type key struct{ fam, typ, id string }
	idx := map[key]int{}
	out := map[string][]fontUser{}
	add := func(doc, typ, id, name, state string) {
		for _, mm := range fontRefRe.FindAllStringSubmatch(doc, -1) {
			var fam string
			if json.Unmarshal([]byte(mm[1]), &fam) != nil || fam == "" {
				continue
			}
			k := key{strings.ToLower(fam), typ, id}
			i, ok := idx[k]
			if !ok {
				out[k.fam] = append(out[k.fam], fontUser{Type: typ, ID: id, Name: name})
				i = len(out[k.fam]) - 1
				idx[k] = i
			}
			if u := &out[k.fam][i]; state != "" && !slices.Contains(u.States, state) {
				u.States = append(u.States, state)
			}
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT p.id, p.name, p.is_template, p.draft, COALESCE(v.doc, '')
		FROM presentations p LEFT JOIN presentation_versions v ON v.presentation_id = p.id AND v.version = p.published_version
		WHERE p.archived_at IS NULL ORDER BY p.name COLLATE NOCASE, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, draft, published string
		var tmpl int
		if err := rows.Scan(&id, &name, &tmpl, &draft, &published); err != nil {
			return nil, err
		}
		typ := "presentation"
		if tmpl == 1 {
			typ = "template"
		}
		add(draft, typ, id, name, "draft")
		add(published, typ, id, name, "published")
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	trows, err := q.QueryContext(ctx, `SELECT id, name, style FROM ticker_templates ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	for trows.Next() {
		var id, name, style string
		if err := trows.Scan(&id, &name, &style); err != nil {
			return nil, err
		}
		add(style, "ticker", id, name, "")
	}
	return out, trows.Err()
}

// ---- upload validation -----------------------------------------------------------------

// detectFont validates the magic bytes and basic structure of a font file and returns its
// format name (truetype | opentype | woff | woff2).
func detectFont(path string, size int64) (string, error) {
	head := readHead(path, 4096)
	if len(head) < 12 {
		return "", errors.New("file too small")
	}
	switch string(head[:4]) {
	case "\x00\x01\x00\x00", "true":
		return "truetype", checkSFNT(path, head, size)
	case "OTTO":
		return "opentype", checkSFNT(path, head, size)
	case "wOFF":
		if int64(binary.BigEndian.Uint32(head[8:12])) != size || len(head) < 44 || binary.BigEndian.Uint16(head[12:14]) == 0 {
			return "", errors.New("inconsistent WOFF header")
		}
		return "woff", nil
	case "wOF2":
		if int64(binary.BigEndian.Uint32(head[8:12])) != size || len(head) < 48 || binary.BigEndian.Uint16(head[12:14]) == 0 {
			return "", errors.New("inconsistent WOFF2 header")
		}
		return "woff2", nil
	}
	return "", errors.New("not a TrueType, OpenType, WOFF or WOFF2 font")
}

// checkSFNT verifies the table directory of a TrueType/OpenType font.
func checkSFNT(path string, head []byte, size int64) error {
	n := int(binary.BigEndian.Uint16(head[4:6]))
	if n < 4 || n > 200 {
		return errors.New("implausible table count")
	}
	dir := readHead(path, 12+16*n)
	if len(dir) < 12+16*n {
		return errors.New("truncated table directory")
	}
	tags := map[string]bool{}
	for i := 0; i < n; i++ {
		e := dir[12+16*i : 12+16*i+16]
		off, length := int64(binary.BigEndian.Uint32(e[8:12])), int64(binary.BigEndian.Uint32(e[12:16]))
		if off+length > size || off < int64(12+16*n) {
			return errors.New("table outside of the file")
		}
		tags[string(e[:4])] = true
	}
	if !tags["cmap"] || !tags["head"] {
		return errors.New("required font tables are missing")
	}
	return nil
}

func validFamily(s string) bool {
	if n := utf8.RuneCountInString(s); n < 1 || n > 100 || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || strings.ContainsRune("-_.'()&+,", r)) {
			return false
		}
	}
	return true
}

func formInt(fields map[string]string, key string, def int) (int, error) {
	s := strings.TrimSpace(fields[key])
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 1000 {
		return 0, httpx.Invalid(key, "invalid")
	}
	return n, nil
}

// readFontUpload streams the multipart body: the file goes to a temporary file (bounded),
// text fields into a map.
func (m *Module) readFontUpload(w http.ResponseWriter, r *http.Request) (fields map[string]string, tmp string, size int64, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFontSize+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, "", 0, httpx.ErrBadRequest.With("detail", "expected multipart/form-data").Wrap(err)
	}
	if err := m.ensureDir(m.tmpDir("fonts")); err != nil {
		return nil, "", 0, err
	}
	fields = map[string]string{}
	cleanup := func() {
		if tmp != "" {
			_ = atomicfile.Remove(tmp)
			tmp = ""
		}
	}
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			cleanup()
			var mbe *http.MaxBytesError
			if errors.As(perr, &mbe) {
				return nil, "", 0, httpx.E(http.StatusRequestEntityTooLarge, "file_too_large", "font file is too large").With("max", int64(maxFontSize))
			}
			return nil, "", 0, httpx.ErrBadRequest.Wrap(perr)
		}
		name := part.FormName()
		if name != "file" {
			b, _ := io.ReadAll(io.LimitReader(part, 2048))
			fields[name] = string(b)
			continue
		}
		if tmp != "" {
			cleanup()
			return nil, "", 0, httpx.Invalid("file", "duplicate")
		}
		size, tmp, err = m.spoolPart(part)
		if err != nil {
			cleanup()
			return nil, "", 0, err
		}
	}
	return fields, tmp, size, nil
}

func (m *Module) spoolPart(part *multipart.Part) (size int64, path string, err error) {
	f, err := os.CreateTemp(m.tmpDir("fonts"), "font-*.part")
	if err != nil {
		return 0, "", err
	}
	path = f.Name()
	n, err := io.Copy(f, io.LimitReader(part, maxFontSize+1))
	cerr := f.Close()
	switch {
	case err != nil:
		_ = os.Remove(path)
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return 0, "", httpx.E(http.StatusRequestEntityTooLarge, "file_too_large", "font file is too large").With("max", int64(maxFontSize))
		}
		return 0, "", err
	case cerr != nil:
		_ = os.Remove(path)
		return 0, "", cerr
	case n > maxFontSize:
		_ = os.Remove(path)
		return 0, "", httpx.E(http.StatusRequestEntityTooLarge, "file_too_large", "font file is too large").With("max", int64(maxFontSize))
	case n == 0:
		_ = os.Remove(path)
		return 0, "", httpx.Invalid("file", "empty")
	}
	return n, path, nil
}

// fontAdd implements POST /api/v1/fonts (admin, multipart).
func (m *Module) fontAdd(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	fields, tmp, size, err := m.readFontUpload(w, r)
	if err != nil {
		return err
	}
	defer func() {
		if tmp != "" {
			_ = atomicfile.Remove(tmp)
		}
	}()
	if tmp == "" {
		return httpx.Invalid("file", "required")
	}
	family := strings.TrimSpace(fields["family"])
	if family == "" {
		return httpx.Invalid("family", "required")
	}
	if !validFamily(family) {
		return httpx.Invalid("family", "invalid")
	}
	style := strings.TrimSpace(fields["style"])
	if style == "" {
		style = "normal"
	}
	if style != "normal" && style != "italic" {
		return httpx.Invalid("style", "invalid")
	}
	wMin, err := formInt(fields, "weightMin", 400)
	if err != nil {
		return err
	}
	wMax, err := formInt(fields, "weightMax", wMin)
	if err != nil {
		return err
	}
	if wMax < wMin {
		return httpx.Invalid("weightMax", "invalid")
	}
	license := strings.TrimSpace(stripUnsafeRunes(fields["license"]))
	if license == "" {
		return httpx.Invalid("license", "required")
	}
	if utf8.RuneCountInString(license) > 200 {
		return httpx.Invalid("license", "too_long")
	}
	if strings.TrimSpace(fields["licenseConfirmed"]) != "true" {
		return httpx.Invalid("licenseConfirmed", "required")
	}
	bundled, err := m.bundledFonts()
	if err != nil {
		slog.Error("media: reading the bundled font manifest", "err", err)
	}
	for _, b := range bundled {
		if strings.EqualFold(b.Family, family) {
			return httpx.E(http.StatusConflict, "font_family_reserved", "this family name belongs to a bundled font").With("family", family)
		}
	}
	format, ferr := detectFont(tmp, size)
	if ferr != nil {
		return httpx.E(http.StatusUnsupportedMediaType, "invalid_font", "not a valid font file").With("detail", ferr.Error())
	}

	sha, _, _, err := m.ingestMove(r.Context(), tmp, "")
	if err != nil {
		return err
	}
	tmp = ""
	id := ids.New()
	err = m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM font_faces WHERE family = ? COLLATE NOCASE AND style = ? AND weight_min <= ? AND weight_max >= ?`,
			family, style, wMax, wMin).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return httpx.E(http.StatusConflict, "font_exists", "this family, style and weight range is already uploaded")
		}
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO font_faces(id, family, style, weight_min, weight_max, blob_sha256, format, scripts, license, uploaded_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, '[]', ?, ?, ?)`, id, family, style, wMin, wMax, sha, format, license, p.Username, db.Now()); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "font.added", ObjectType: "font", ObjectID: id, ObjectName: family,
			New: map[string]any{"style": style, "weightMin": wMin, "weightMax": wMax, "format": format, "license": license, "sha256": sha, "size": size}})
	})
	if err != nil {
		return err
	}
	m.publishContent("font.added", id)
	faces, err := m.fontFaces(r.Context(), m.app.DB.R, id)
	if err != nil || len(faces) == 0 {
		return httpx.ErrInternal.Wrap(err)
	}
	return httpx.OK(w, faces[0])
}

// fontFaces loads custom font faces (all, or the one with the given id).
func (m *Module) fontFaces(ctx context.Context, q queryer, id string) ([]fontFaceView, error) {
	query := `SELECT f.id, f.family, f.style, f.weight_min, f.weight_max, f.format, f.license, f.blob_sha256, f.uploaded_by, f.created_at, COALESCE(b.size, 0)
		FROM font_faces f LEFT JOIN blobs b ON b.sha256 = f.blob_sha256 `
	var args []any
	if id != "" {
		query += `WHERE f.id = ? `
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, query+`ORDER BY f.family COLLATE NOCASE, f.style, f.weight_min`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []fontFaceView{}
	for rows.Next() {
		var f fontFaceView
		if err := rows.Scan(&f.ID, &f.Family, &f.Style, &f.WeightMin, &f.WeightMax, &f.Format, &f.License, &f.SHA256, &f.UploadedBy, &f.CreatedAt, &f.Size); err != nil {
			return nil, err
		}
		f.URL = "/fonts/custom/" + f.ID
		out = append(out, f)
	}
	return out, rows.Err()
}

// fontList implements GET /api/v1/fonts: bundled families plus uploaded ones.
func (m *Module) fontList(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	ctx := r.Context()
	bundled, err := m.bundledFonts()
	if err != nil {
		slog.Error("media: reading the bundled font manifest", "err", err)
	}
	faces, err := m.fontFaces(ctx, m.app.DB.R, "")
	if err != nil {
		return err
	}
	usage, err := fontUsage(ctx, m.app.DB.R)
	if err != nil {
		return err
	}
	out := []fontFamilyView{}
	for _, b := range bundled {
		users := usersOf(usage, b.Family)
		out = append(out, fontFamilyView{Family: b.Family, Source: "bundled", Category: b.Category, License: b.License, Usage: len(users), UsedBy: users})
	}
	idx := map[string]int{}
	for _, f := range faces {
		k := strings.ToLower(f.Family)
		i, ok := idx[k]
		if !ok {
			users := usersOf(usage, k)
			out = append(out, fontFamilyView{Family: f.Family, Source: "custom", License: f.License, Usage: len(users), UsedBy: users})
			i = len(out) - 1
			idx[k] = i
		}
		out[i].Faces = append(out[i].Faces, f)
	}
	return httpx.OK(w, map[string]any{"fonts": out})
}

// usersOf returns the users of a family (never nil, so the JSON is an array).
func usersOf(usage map[string][]fontUser, family string) []fontUser {
	if u := usage[strings.ToLower(family)]; u != nil {
		return u
	}
	return []fontUser{}
}

// fontDelete implements DELETE /api/v1/fonts/{id}.
func (m *Module) fontDelete(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var family, style, sha string
		err := tx.QueryRowContext(r.Context(), `SELECT family, style, blob_sha256 FROM font_faces WHERE id = ?`, id).Scan(&family, &style, &sha)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		usage, err := fontUsage(r.Context(), tx)
		if err != nil {
			return err
		}
		if users := usage[strings.ToLower(family)]; len(users) > 0 {
			return httpx.E(http.StatusConflict, "font_in_use", "the font is used by presentations").With("usage", len(users)).With("family", family).With("usedBy", users)
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM font_faces WHERE id = ?`, id); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "font.deleted", ObjectType: "font", ObjectID: id, ObjectName: family,
			Old: map[string]any{"style": style, "sha256": sha}})
	})
	if err != nil {
		return err
	}
	m.publishContent("font.deleted", id)
	if _, err := m.CollectGarbage(r.Context(), false); err != nil {
		slog.Error("media: garbage collection after font deletion", "err", err)
	}
	return httpx.NoContent(w)
}

// fontFile implements GET /fonts/custom/{id}.
func (m *Module) fontFile(w http.ResponseWriter, r *http.Request, _ *core.Principal) error {
	faces, err := m.fontFaces(r.Context(), m.app.DB.R, r.PathValue("id"))
	if err != nil {
		return err
	}
	if len(faces) == 0 {
		return httpx.ErrNotFound
	}
	f := faces[0]
	file, err := os.Open(m.BlobPath(f.SHA256))
	if err != nil {
		if isNotExist(err) {
			slog.Error("media: font blob missing on disk", "font", f.ID, "sha256", f.SHA256)
			return httpx.E(http.StatusNotFound, "file_missing", "the stored file is missing")
		}
		return err
	}
	defer file.Close()
	h := w.Header()
	h.Set("Content-Type", fontMIME[f.Format])
	h.Set("ETag", `"`+f.SHA256+`"`)
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, r, "", time.Time{}, file) // the ETag (content hash) is the validator
	return nil
}
