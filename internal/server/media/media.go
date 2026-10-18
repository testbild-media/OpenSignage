// Package media implements the media manager: content-addressed blob store, resumable
// chunked uploads, type detection by content, the ffmpeg job queue, virtual folders,
// trash, safe blob garbage collection, the storage summary and custom fonts.
//
// Error codes (HTTP status, params) used by this package:
//
//	unsupported_type   415 {detected}        file content is not an allowed image/video/audio type
//	svg_unsafe         422 {reason}          SVG with scripts, event handlers or external references
//	file_too_large     413 {max}             above the 20 GB limit (fonts: 20 MB)
//	chunk_too_large    413 {max}             one upload request above 16 MB
//	disk_full          507 {needed, free}    not enough free disk space
//	upload_offset      409 {received}        chunk does not start at the received offset
//	bad_range          400/416               malformed or inconsistent Content-Range
//	too_many_uploads   429 {max}             too many open uploads for this user
//	folder_cycle       409                   move would make a folder its own ancestor
//	name_taken         409                   a sibling folder with that name exists
//	kind_mismatch      409 {expected}        replacement file has another media kind
//	in_trash           409                   object is in the trash
//	not_in_trash       409                   object is not in the trash
//	asset_in_use       409 {blocked}         purge blocked: used as product image
//	job_active         409                   a processing job is already queued/running
//	variant_not_found  404                   variant does not exist (yet)
//	file_missing       404                   blob file missing on disk (logged)
//	font_in_use        409 {usage}           font family referenced by presentations
//	font_exists        409                   same family/style/weight already uploaded
//	font_family_reserved 409                 family collides with a bundled font family
//	invalid_font       415                   font file magic bytes / structure invalid
//	invalid_field      422 {field, reason}   validation (shared code)
//
// Processing failures are stored as "<code>: <text>" in media_jobs.error and
// media_assets.error: disk_full, probe_failed, ffmpeg_failed, file_missing, svg_unsafe,
// unsupported_type (shown to the user next to the failed asset; reprocess retries).
//
// Events on the bus (topic: kinds):
//
//	media:   progress (data: status, step, index, total, percent, error), created, replaced,
//	         updated (rename/move), folder, trashed, restored, purged, cleaned
//	content: media.ready (every finished job: first upload, replacement, reprocess),
//	         media.replaced, media.failed, media.trashed, media.restored, media.purged,
//	         media.cleaned (variants / old revisions removed), font.added, font.deleted —
//	         "something a screen may show has changed"
//
// media_assets.revision is the newest upload, ready_revision the newest revision whose
// variants are complete (0 = none). Screens ship ready_revision; a replacement, a reprocess
// or a failure never takes it away, and the cleanups never delete it.
//
// Variant rules for other modules (see ResolveVariant): the variant "orig" of an image above
// 3840 px is the downscaled file, otherwise the uploaded revision; i512 → i1920 → orig and
// v1080h → v1080 and v720 → v1080 are fallbacks; thumbnails of SVGs are the SVG itself.
package media

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"opensignage/internal/server/core"
	"opensignage/internal/server/httpx"
)

// Limits and policy constants.
const (
	maxFileSize   = 20 << 30  // 20 GB per upload
	maxChunk      = 16 << 20  // 16 MB per PUT request
	diskReserve   = 500 << 20 // 500 MB always kept free
	uploadTTL     = 24 * time.Hour
	maxOpenUpload = 100
	ffmpegTimeout = 2 * time.Hour
	maxFontSize   = 20 << 20
	maxImageEdge  = 3840 // originals above this are downscaled into variant "orig"
)

// Module is the media module.
type Module struct {
	app     *core.App
	dataDir string

	// test seams
	diskFn  func(dir string) (total, free uint64, err error)
	tc      transcoder
	gcGrace time.Duration // blobs younger than this are never collected

	gcMu     sync.RWMutex // ingest holds RLock, the delete phase of the GC holds Lock
	gcRun    sync.Mutex   // one GC at a time
	wake     chan struct{}
	upMu     sync.Mutex
	upLocks  map[string]*sync.Mutex
	upHashes map[string]*upHash
	progress sync.Map // asset id -> progressInfo
	maintMu  sync.Mutex
	lastDay  time.Time

	// Blobs that a running job has stored but not yet recorded in media_variants. The garbage
	// collector must not touch them (guarded by pinMu; registered while ingest holds gcMu.RLock).
	pinMu sync.Mutex
	pins  map[string]int
	// running ffmpeg job per asset, so a replacement can cancel the obsolete one
	jobMu   sync.Mutex
	running map[string]*runningJob

	beforeFinalize func() // test seam: runs after the variants are stored, before the final transaction
}

// New creates the module.
func New(app *core.App) *Module {
	m := &Module{
		app:      app,
		dataDir:  app.Cfg.DataDir,
		diskFn:   diskUsage,
		gcGrace:  10 * time.Minute,
		wake:     make(chan struct{}, 1),
		upLocks:  map[string]*sync.Mutex{},
		upHashes: map[string]*upHash{},
		pins:     map[string]int{},
		running:  map[string]*runningJob{},
	}
	m.tc = &execTranscoder{ffmpeg: orDefault(app.Cfg.FFmpeg, "ffmpeg"), ffprobe: orDefault(app.Cfg.FFprobe, "ffprobe")}
	return m
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// Routes registers the WebGUI API.
func (m *Module) Routes(rt *core.Router) {
	// Operators limited to zones: media right "use" browses and uses media, "upload" adds
	// files and folders and renames/moves own ones, "manage" (as operators of all zones) also
	// deletes and cleans up (core/rights.go).
	op, use, upload, manage := core.RoleOperator, core.MediaUse, core.MediaUpload, core.MediaManage

	// uploads
	rt.Scoped("POST /api/v1/media/uploads", op, mediaRight(upload, m.createUpload))
	rt.Scoped("PUT /api/v1/media/uploads/{id}", op, mediaRight(upload, m.putChunk))
	rt.Scoped("DELETE /api/v1/media/uploads/{id}", op, mediaRight(upload, m.abortUpload))
	// GET /media/uploads/{id} and GET /media/{id}/file would conflict in the mux, so one
	// route dispatches both. Both need operator rights: media files are not for everybody.
	rt.Scoped("GET /api/v1/media/{a}/{b}", op, mediaRight(use, m.getTwoSegments))

	// browse and details
	rt.Scoped("GET /api/v1/media", op, mediaRight(use, m.list))
	rt.Scoped("GET /api/v1/media/trash", op, mediaRight(manage, m.trashList))
	rt.Scoped("GET /api/v1/media/unused", op, mediaRight(manage, m.unusedList))
	rt.Scoped("GET /api/v1/media/{id}", op, mediaRight(use, m.details))
	rt.Scoped("PATCH /api/v1/media/{id}", op, mediaRight(upload, m.ownAsset(m.patchAsset)))
	rt.Scoped("DELETE /api/v1/media/{id}", op, mediaRight(manage, m.trashAsset))
	rt.Scoped("POST /api/v1/media/{id}/reprocess", op, mediaRight(manage, m.reprocess))

	// folders
	rt.Scoped("POST /api/v1/media/folders", op, mediaRight(upload, m.createFolder))
	rt.Scoped("PATCH /api/v1/media/folders/{id}", op, mediaRight(upload, m.ownFolder(m.patchFolder)))
	rt.Scoped("DELETE /api/v1/media/folders/{id}", op, mediaRight(manage, m.trashFolder))

	// trash and cleanup
	rt.Scoped("POST /api/v1/media/trash", op, mediaRight(manage, m.trashBulk))
	rt.Scoped("POST /api/v1/media/trash/restore", op, mediaRight(manage, m.trashRestore))
	rt.Scoped("POST /api/v1/media/trash/purge", op, mediaRight(manage, m.trashPurge))
	rt.Scoped("POST /api/v1/media/unused/variants/delete", op, mediaRight(manage, m.deleteVariants))
	rt.Scoped("POST /api/v1/media/old-revisions/delete", op, mediaRight(manage, m.deleteOldRevisions))

	// storage dashboard
	rt.Role("GET /api/v1/storage", core.RoleAdmin, m.storage)

	// fonts
	rt.Role("GET /api/v1/fonts", core.RoleUser, m.fontList)
	rt.Role("POST /api/v1/fonts", core.RoleAdmin, m.fontAdd)
	rt.Role("DELETE /api/v1/fonts/{id}", core.RoleAdmin, m.fontDelete)
	rt.Role("GET /fonts/custom/{id}", core.RoleUser, m.fontFile)
}

// getTwoSegments serves GET /media/uploads/{id} and GET /media/{id}/file (operator+).
func (m *Module) getTwoSegments(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	a, b := r.PathValue("a"), r.PathValue("b")
	if a == "uploads" {
		return m.uploadStatus(w, r, p, b)
	}
	if b == "file" {
		return m.serveFile(w, r, a)
	}
	return httpx.ErrNotFound
}

// ---- small shared helpers -------------------------------------------------------------

// Queryer is satisfied by *sql.DB and *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type queryer = Queryer

// optStr distinguishes an absent JSON key from an explicit null.
type optStr struct {
	Set bool
	Val *string
}

// UnmarshalJSON implements json.Unmarshaler.
func (o *optStr) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Val = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	o.Val = &s
	return nil
}

func (m *Module) publish(topic, kind, id string, data any) {
	m.app.Bus.Publish(core.Event{Topic: topic, Kind: kind, ID: id, Data: data})
}

func (m *Module) publishMedia(kind, id string, data any) { m.publish(core.TopicMedia, kind, id, data) }

func (m *Module) publishContent(kind, id string) { m.publish(core.TopicContent, kind, id, nil) }

func (m *Module) tmpDir(parts ...string) string {
	return filepath.Join(append([]string{m.dataDir, "tmp"}, parts...)...)
}

func (m *Module) ensureDir(path string) error { return os.MkdirAll(path, 0o750) }

// sanitizeFilename keeps only the last path component and removes control and
// direction-override characters.
func sanitizeFilename(s string) (string, bool) {
	s = strings.ReplaceAll(s, "\\", "/")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	s = stripUnsafeRunes(s)
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." {
		return "", false
	}
	if utf8.RuneCountInString(s) > 200 {
		r := []rune(s)
		s = string(r[:200])
	}
	return s, true
}

// cleanLabel validates folder and asset display names.
func cleanLabel(s string) (string, bool) {
	s = strings.TrimSpace(stripUnsafeRunes(s))
	n := utf8.RuneCountInString(s)
	if n < 1 || n > 120 || strings.ContainsAny(s, "/\\") || s == "." || s == ".." {
		return "", false
	}
	return s, true
}

func stripUnsafeRunes(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
}

func inPlaceholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func strArgs(ids []string) []any {
	out := make([]any, len(ids))
	for i, v := range ids {
		out[i] = v
	}
	return out
}

// chunked calls f for slices of at most n elements.
func chunked(ids []string, n int, f func([]string) error) error {
	for len(ids) > 0 {
		k := min(n, len(ids))
		if err := f(ids[:k]); err != nil {
			return err
		}
		ids = ids[k:]
	}
	return nil
}

func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := ids[:0:0]
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func nullStr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

func rawJSON(s string) json.RawMessage {
	if strings.TrimSpace(s) == "" || !json.Valid([]byte(s)) {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(s)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// guard runs f and logs a panic instead of crashing the goroutine.
func guard(name string, f func()) (panicked bool) {
	defer func() {
		if p := recover(); p != nil {
			panicked = true
			slog.Error("media: goroutine panic", "where", name, "panic", fmt.Sprint(p))
		}
	}()
	f()
	return false
}

// supervise restarts f after a panic until ctx is done.
func supervise(ctx context.Context, name string, f func()) {
	for ctx.Err() == nil {
		if !guard(name, f) {
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

func isNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }
