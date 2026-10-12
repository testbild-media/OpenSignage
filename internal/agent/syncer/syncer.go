// Package syncer downloads deployments in two phases (ARCHITECTURE §3 "Content"): fetch
// the manifest, check disk space, download every missing blob with HTTP Range resume
// and SHA-256 verification, and only then switch deployment.json atomically
// (pending → active, active → previous). The old content keeps playing until the
// switch; failures retry with exponential backoff and never touch the active content.
package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"opensignage/internal/agent/state"
	"opensignage/internal/agent/wire"
	"opensignage/internal/common/atomicfile"
	"opensignage/internal/common/version"
)

// Client performs authenticated requests against the paired server.
type Client interface {
	// Get issues a GET for target, a path ("/device/v1/…") or an absolute URL on the
	// paired server. hdr may be nil.
	Get(ctx context.Context, target string, hdr http.Header) (*http.Response, error)
}

// Error codes reported in deploy.progress and the status.
const (
	ErrDiskFull         = "disk_full"
	ErrManifestFetch    = "manifest_fetch_failed"
	ErrManifestInvalid  = "manifest_invalid"
	ErrHashMismatch     = "hash_mismatch"
	ErrAgentTooOld      = "agent_too_old"
	ErrDownload         = "download_failed"
	ErrChecksum         = "checksum_mismatch"
	ErrStorage          = "storage_error"
	ErrNotPaired        = "not_paired"
	ErrDeploymentAbsent = "deployment_not_found"
)

// Options configure a Syncer.
type Options struct {
	Store        *state.Store
	Client       Client
	Prefs        func() Prefs
	FreeSpace    func(dir string) (uint64, error)
	AgentVersion string
	// OnProgress receives deploy.progress messages for the server.
	OnProgress func(wire.DeployProgress)
	// OnChange is called after the active deployment or the sync state changed.
	OnChange func()
	// MinBackoff/MaxBackoff bound the retry delay after a failure (30 s … 15 min).
	MinBackoff, MaxBackoff time.Duration
	// ReserveBytes is added to the space check (200 MB).
	ReserveBytes uint64
	Now          func() time.Time
}

// Loaded is a parsed manifest with its variant choice. It is immutable.
type Loaded struct {
	Ref     state.Ref
	Raw     []byte
	M       *wire.Manifest
	choices map[string]Choice
	fonts   map[string]string // sha → mime
}

type target struct {
	cleared bool
	ref     state.Ref
}

// Syncer owns deployment.json, the manifests and the blob cache.
type Syncer struct {
	o Options

	mu         sync.Mutex
	dep        state.Deployment
	active     *Loaded
	target     *target
	info       wire.SyncInfo
	failures   int
	nextTry    time.Time
	cancel     context.CancelFunc
	cacheBytes int64

	work sync.Mutex // held while downloading, switching, collecting garbage or wiping
	kick chan struct{}
}

// New loads deployment.json and the active manifest. A pending deployment found on
// disk becomes the target again, so an interrupted download resumes after a restart.
func New(o Options) *Syncer {
	if o.MinBackoff == 0 {
		o.MinBackoff = 30 * time.Second
	}
	if o.MaxBackoff == 0 {
		o.MaxBackoff = 15 * time.Minute
	}
	if o.ReserveBytes == 0 {
		o.ReserveBytes = 200 << 20
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Prefs == nil {
		o.Prefs = func() Prefs { return Prefs{Perf: "standard", ScreenWidth: 1920} }
	}
	if o.AgentVersion == "" {
		o.AgentVersion = version.Version
	}
	s := &Syncer{o: o, kick: make(chan struct{}, 1), info: wire.SyncInfo{State: "idle"}}
	s.loadState()
	return s
}

func (s *Syncer) loadState() {
	dep, err := s.o.Store.LoadDeployment()
	if err != nil {
		slog.Error("deployment.json unreadable, starting without content", "err", err)
		dep = state.Deployment{}
	}
	s.dep = dep
	s.active = nil
	if dep.Active != nil {
		l, err := s.load(*dep.Active)
		if err != nil {
			slog.Error("active manifest unreadable", "deployment", dep.Active.ID, "err", err)
		} else {
			s.active = l
		}
	}
	switch {
	case dep.Pending != nil:
		s.target = &target{ref: *dep.Pending}
	case dep.Cleared:
		s.target = &target{cleared: true}
		s.info = wire.SyncInfo{State: "cleared"}
	case dep.Active != nil:
		s.target = &target{ref: *dep.Active}
		s.info = wire.SyncInfo{State: wire.DeployActive, DeploymentID: dep.Active.ID}
	}
	s.cacheBytes = dirSize(s.o.Store.BlobDir())
}

func (s *Syncer) load(ref state.Ref) (*Loaded, error) {
	raw, m, err := s.o.Store.LoadManifest(ref.ID)
	if err != nil {
		return nil, err
	}
	return s.index(ref, raw, m), nil
}

func (s *Syncer) index(ref state.Ref, raw []byte, m *wire.Manifest) *Loaded {
	l := &Loaded{Ref: ref, Raw: raw, M: m, choices: Select(m, s.o.Prefs()), fonts: map[string]string{}}
	for _, f := range m.Fonts {
		for _, file := range f.Files {
			if state.ValidSHA(file.SHA256) {
				l.fonts[file.SHA256] = fontMIME(file.Format)
			}
		}
	}
	return l
}

// ---------------------------------------------------------------- inputs

func (s *Syncer) poke() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Announce handles deploy.available.
func (s *Syncer) Announce(id, hash string) {
	if !state.ValidID(id) {
		slog.Warn("deploy.available with invalid id ignored", "id", id)
		return
	}
	s.mu.Lock()
	same := s.target != nil && !s.target.cleared && s.target.ref.ID == id && s.target.ref.Hash == hash
	if !same {
		s.target = &target{ref: state.Ref{ID: id, Hash: hash}}
		s.failures, s.nextTry = 0, time.Time{}
		if s.cancel != nil {
			s.cancel()
		}
	}
	s.mu.Unlock()
	s.poke()
}

// Clear handles deploy.cleared: an explicit "show nothing" target.
func (s *Syncer) Clear(reason string) {
	s.mu.Lock()
	if s.target == nil || !s.target.cleared {
		slog.Info("deployment cleared by server", "reason", reason)
	}
	s.target = &target{cleared: true}
	s.failures, s.nextTry = 0, time.Time{}
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.poke()
}

// SyncNow retries immediately (remote action sync_now).
func (s *Syncer) SyncNow() {
	s.mu.Lock()
	s.failures, s.nextTry = 0, time.Time{}
	s.mu.Unlock()
	s.poke()
}

// PrefsChanged re-selects variants (perf or screen width changed) and downloads what
// the new choice needs.
func (s *Syncer) PrefsChanged() {
	s.mu.Lock()
	if s.active != nil {
		s.active = s.index(s.active.Ref, s.active.Raw, s.active.M)
	}
	s.mu.Unlock()
	s.poke()
}

// Reset cancels all work, clears the in-memory state and runs wipe while no download
// can write into the cache (used when pairing with a server).
func (s *Syncer) Reset(wipe func() error) error {
	s.mu.Lock()
	s.target = nil
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.work.Lock()
	defer s.work.Unlock()
	err := wipe()
	s.mu.Lock()
	s.dep, s.active, s.failures, s.nextTry = state.Deployment{}, nil, 0, time.Time{}
	s.info = wire.SyncInfo{State: "idle"}
	s.cacheBytes = dirSize(s.o.Store.BlobDir())
	s.mu.Unlock()
	s.changed()
	return err
}

// ---------------------------------------------------------------- outputs

// Current returns the deployment pointer and the active manifest (nil when none).
func (s *Syncer) Current() (state.Deployment, *Loaded) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dep, s.active
}

// Info returns the sync state for status reports.
func (s *Syncer) Info() wire.SyncInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// LastProgress returns the progress message describing the current state (re-sent
// after every reconnect so the server never shows stale data).
func (s *Syncer) LastProgress() (wire.DeployProgress, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.info.State {
	case wire.DeployPreparing, wire.DeployReady, wire.DeployActive, wire.DeployFailed:
		if s.info.DeploymentID == "" {
			return wire.DeployProgress{}, false
		}
		return wire.DeployProgress{ID: s.info.DeploymentID, State: s.info.State, Done: s.info.Done, Total: s.info.Total, Error: s.info.Error}, true
	}
	return wire.DeployProgress{}, false
}

// CacheBytes is the size of the blob cache.
func (s *Syncer) CacheBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cacheBytes
}

// AssetFile resolves /assets/{id} to the cached variant of the active deployment
// (falling back to another cached variant of the same asset).
func (s *Syncer) AssetFile(assetID string) (path, mime, sha string, ok bool) {
	s.mu.Lock()
	l := s.active
	s.mu.Unlock()
	if l == nil {
		return "", "", "", false
	}
	c, found := l.choices[assetID]
	if !found {
		return "", "", "", false
	}
	for _, a := range append([]wire.ManifestAsset{c.Asset}, c.Alt...) {
		p := s.o.Store.BlobPath(a.SHA256)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, a.MIME, a.SHA256, true
		}
	}
	return "", "", "", false
}

// FontFile resolves /fonts/custom/{sha} for fonts of the active deployment.
func (s *Syncer) FontFile(sha string) (path, mime string, ok bool) {
	s.mu.Lock()
	l := s.active
	s.mu.Unlock()
	if l == nil || !state.ValidSHA(sha) {
		return "", "", false
	}
	mime, found := l.fonts[sha]
	if !found {
		return "", "", false
	}
	p := s.o.Store.BlobPath(sha)
	if _, err := os.Stat(p); err != nil {
		return "", "", false
	}
	return p, mime, true
}

func (s *Syncer) changed() {
	if s.o.OnChange != nil {
		s.o.OnChange()
	}
}

func (s *Syncer) progress(p wire.DeployProgress) {
	if s.o.OnProgress != nil {
		s.o.OnProgress(p)
	}
}

func (s *Syncer) setInfo(i wire.SyncInfo) {
	s.mu.Lock()
	s.info = i
	s.mu.Unlock()
}

// ---------------------------------------------------------------- loop

// Run processes targets until ctx ends.
func (s *Syncer) Run(ctx context.Context) {
	for ctx.Err() == nil {
		s.Step(ctx)
		var wait <-chan time.Time
		s.mu.Lock()
		if !s.nextTry.IsZero() {
			d := s.nextTry.Sub(s.o.Now())
			if d < 0 {
				d = 0
			}
			wait = time.After(d)
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		case <-wait:
		}
	}
}

// Step performs one unit of work for the current target (exported for tests).
func (s *Syncer) Step(ctx context.Context) {
	s.mu.Lock()
	if !s.nextTry.IsZero() && s.o.Now().Before(s.nextTry) {
		s.mu.Unlock()
		return
	}
	t := s.target
	actx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mu.Unlock()
	defer cancel()

	s.work.Lock()
	defer s.work.Unlock()
	if actx.Err() != nil {
		return
	}
	switch {
	case t == nil:
		return
	case t.cleared:
		s.applyCleared()
	default:
		s.sync(actx, t.ref)
	}
}

func (s *Syncer) applyCleared() {
	s.mu.Lock()
	if s.dep.Cleared && s.dep.Pending == nil {
		s.mu.Unlock()
		return
	}
	dep := s.dep
	if dep.Active != nil {
		dep.Previous = dep.Active
	}
	dep.Active, dep.Pending, dep.Cleared = nil, nil, true
	dep.UpdatedAt = s.o.Now().UTC().Format(time.RFC3339)
	s.mu.Unlock()
	if err := s.o.Store.SaveDeployment(dep); err != nil {
		slog.Error("save deployment.json failed", "err", err)
		s.fail("", ErrStorage, 0, 0)
		return
	}
	s.mu.Lock()
	s.dep, s.active = dep, nil
	s.info = wire.SyncInfo{State: "cleared"}
	s.mu.Unlock()
	s.o.Store.PruneManifests(dep.IDs())
	slog.Info("cleared target active")
	s.changed()
	s.gcLocked()
}

func (s *Syncer) fail(id, code string, done, total int64) {
	s.mu.Lock()
	s.failures++
	d := s.o.MinBackoff << min(s.failures-1, 16)
	if d > s.o.MaxBackoff || d <= 0 {
		d = s.o.MaxBackoff
	}
	s.nextTry = s.o.Now().Add(d)
	s.info = wire.SyncInfo{State: wire.DeployFailed, DeploymentID: id, Done: done, Total: total, Error: code}
	s.mu.Unlock()
	slog.Warn("sync failed, will retry", "deployment", id, "error", code, "retryIn", d.String())
	s.progress(wire.DeployProgress{ID: id, State: wire.DeployFailed, Done: done, Total: total, Error: code})
	s.changed()
}

func (s *Syncer) sync(ctx context.Context, ref state.Ref) {
	s.mu.Lock()
	dep := s.dep
	active := s.active
	s.mu.Unlock()

	isActive := dep.Active != nil && dep.Active.ID == ref.ID && !dep.Cleared && active != nil
	var l *Loaded
	if isActive {
		l = active
	} else {
		// Phase 1: remember the pending target (survives restarts).
		if dep.Pending == nil || *dep.Pending != ref {
			old := dep.Pending
			dep.Pending = &ref
			dep.UpdatedAt = s.o.Now().UTC().Format(time.RFC3339)
			if err := s.o.Store.SaveDeployment(dep); err != nil {
				slog.Error("save deployment.json failed", "err", err)
				s.fail(ref.ID, ErrStorage, 0, 0)
				return
			}
			s.mu.Lock()
			s.dep = dep
			s.mu.Unlock()
			if old != nil {
				s.o.Store.PruneManifests(dep.IDs())
			}
		}
		var code string
		l, code = s.manifest(ctx, ref)
		if ctx.Err() != nil {
			return
		}
		if code != "" {
			s.fail(ref.ID, code, 0, 0)
			return
		}
	}

	// Phase 2: make sure every needed blob is present and verified.
	blobs := Needed(l.M, s.o.Prefs())
	var total, present int64
	var missing []Blob
	for _, b := range blobs {
		total += b.Size
		if fileExists(s.o.Store.BlobPath(b.SHA)) {
			present += b.Size
		} else {
			missing = append(missing, b)
		}
	}
	if len(missing) > 0 {
		var need uint64
		for _, b := range missing {
			if b.Size > 0 {
				need += uint64(b.Size)
			}
		}
		need += need/10 + s.o.ReserveBytes
		if s.o.FreeSpace != nil {
			free, err := s.o.FreeSpace(s.o.Store.CacheDir())
			if err != nil {
				slog.Warn("free space unknown", "err", err)
			} else if free < need {
				slog.Warn("not enough disk space for deployment", "deployment", ref.ID, "need", need, "free", free)
				s.fail(ref.ID, ErrDiskFull, present, total)
				return
			}
		}
		done := present
		last := time.Time{}
		report := func(force bool) {
			now := s.o.Now()
			if !force && now.Sub(last) < time.Second {
				return
			}
			last = now
			s.setInfo(wire.SyncInfo{State: wire.DeployPreparing, DeploymentID: ref.ID, Done: done, Total: total})
			if !isActive {
				s.progress(wire.DeployProgress{ID: ref.ID, State: wire.DeployPreparing, Done: done, Total: total})
			}
		}
		report(true)
		for _, b := range missing {
			base := done
			err := s.download(ctx, b, func(n int64) {
				done = base + n
				report(false)
			})
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				code := ErrDownload
				var ce *codeError
				if errors.As(err, &ce) {
					code = ce.code
				}
				slog.Warn("blob download failed", "deployment", ref.ID, "sha256", b.SHA, "err", err)
				s.fail(ref.ID, code, done, total)
				return
			}
			done = base + b.Size
			report(false)
		}
		s.mu.Lock()
		s.cacheBytes = dirSize(s.o.Store.BlobDir())
		s.mu.Unlock()
	}
	if ctx.Err() != nil {
		return
	}
	if isActive {
		s.mu.Lock()
		s.failures, s.nextTry = 0, time.Time{}
		s.info = wire.SyncInfo{State: wire.DeployActive, DeploymentID: ref.ID, Done: total, Total: total}
		s.mu.Unlock()
		s.progress(wire.DeployProgress{ID: ref.ID, State: wire.DeployActive, Done: total, Total: total})
		if len(missing) > 0 {
			s.changed()
			s.gcLocked()
		}
		return
	}
	s.progress(wire.DeployProgress{ID: ref.ID, State: wire.DeployReady, Done: total, Total: total})

	// Atomic switch: pending → active, active → previous.
	s.mu.Lock()
	dep = s.dep
	s.mu.Unlock()
	if dep.Active != nil && dep.Active.ID != ref.ID {
		dep.Previous = dep.Active
	}
	dep.Active, dep.Pending, dep.Cleared = &ref, nil, false
	dep.UpdatedAt = s.o.Now().UTC().Format(time.RFC3339)
	if err := s.o.Store.SaveDeployment(dep); err != nil {
		slog.Error("switch deployment failed", "err", err)
		s.fail(ref.ID, ErrStorage, total, total)
		return
	}
	s.mu.Lock()
	s.dep, s.active = dep, l
	s.failures, s.nextTry = 0, time.Time{}
	s.info = wire.SyncInfo{State: wire.DeployActive, DeploymentID: ref.ID, Done: total, Total: total}
	s.mu.Unlock()
	s.o.Store.PruneManifests(dep.IDs())
	slog.Info("deployment activated", "deployment", ref.ID, "hash", ref.Hash, "bytes", total)
	s.progress(wire.DeployProgress{ID: ref.ID, State: wire.DeployActive, Done: total, Total: total})
	s.changed()
	s.gcLocked()
}

// manifest returns the stored manifest of ref or fetches it.
func (s *Syncer) manifest(ctx context.Context, ref state.Ref) (*Loaded, string) {
	if raw, m, err := s.o.Store.LoadManifest(ref.ID); err == nil && m.ID == ref.ID && (ref.Hash == "" || m.Hash == ref.Hash) {
		return s.index(ref, raw, m), ""
	}
	if s.o.Client == nil {
		return nil, ErrNotPaired
	}
	resp, err := s.o.Client.Get(ctx, "/device/v1/deployments/"+ref.ID, nil)
	if err != nil {
		slog.Warn("manifest fetch failed", "deployment", ref.ID, "err", err)
		return nil, ErrManifestFetch
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrDeploymentAbsent
	}
	if resp.StatusCode != http.StatusOK {
		slog.Warn("manifest fetch failed", "deployment", ref.ID, "status", resp.StatusCode)
		return nil, ErrManifestFetch
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, ErrManifestFetch
	}
	var m wire.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		slog.Warn("manifest invalid", "deployment", ref.ID, "err", err)
		return nil, ErrManifestInvalid
	}
	if m.ID != ref.ID {
		slog.Warn("manifest id mismatch", "want", ref.ID, "got", m.ID)
		return nil, ErrManifestInvalid
	}
	if ref.Hash != "" && m.Hash != ref.Hash {
		slog.Warn("manifest hash mismatch", "want", ref.Hash, "got", m.Hash)
		return nil, ErrHashMismatch
	}
	if m.MinAgentVersion != "" && version.CompareStrings(s.o.AgentVersion, m.MinAgentVersion) < 0 {
		slog.Warn("deployment needs a newer agent", "need", m.MinAgentVersion, "have", s.o.AgentVersion)
		return nil, ErrAgentTooOld
	}
	if m.Timezone != "" {
		if _, err := time.LoadLocation(m.Timezone); err != nil {
			slog.Warn("manifest timezone unknown", "tz", m.Timezone, "err", err)
			return nil, ErrManifestInvalid
		}
	}
	if err := s.o.Store.SaveManifest(ref.ID, raw); err != nil {
		slog.Error("store manifest failed", "err", err)
		return nil, ErrStorage
	}
	return s.index(ref, raw, &m), ""
}

// ---------------------------------------------------------------- download

type codeError struct {
	code string
	err  error
}

func (e *codeError) Error() string { return e.code + ": " + e.err.Error() }
func (e *codeError) Unwrap() error { return e.err }

var errChecksum = errors.New("checksum mismatch")

// download fetches one blob with resume; a checksum mismatch deletes the partial file
// and retries (up to three times).
func (s *Syncer) download(ctx context.Context, b Blob, progress func(int64)) error {
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		err = s.fetchOnce(ctx, b, progress)
		if err == nil || ctx.Err() != nil {
			return err
		}
		if !errors.Is(err, errChecksum) {
			return err
		}
		slog.Warn("blob checksum mismatch, restarting download", "sha256", b.SHA, "attempt", attempt)
		_ = atomicfile.Remove(s.o.Store.PartPath(b.SHA))
	}
	return &codeError{code: ErrChecksum, err: err}
}

type countingWriter struct {
	w   io.Writer
	h   hash.Hash
	n   int64
	cb  func(int64)
	off int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.h.Write(p[:n])
	c.n += int64(n)
	if c.cb != nil {
		c.cb(c.off + c.n)
	}
	return n, err
}

func (s *Syncer) fetchOnce(ctx context.Context, b Blob, progress func(int64)) error {
	if s.o.Client == nil {
		return &codeError{code: ErrNotPaired, err: errors.New("no server")}
	}
	partPath := s.o.Store.PartPath(b.SHA)
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return &codeError{code: ErrStorage, err: err}
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()
	st, err := f.Stat()
	if err != nil {
		return &codeError{code: ErrStorage, err: err}
	}
	offset := st.Size()
	if b.Size > 0 && offset > b.Size {
		offset = 0
		if err := f.Truncate(0); err != nil {
			return &codeError{code: ErrStorage, err: err}
		}
	}
	h := sha256.New()
	if offset > 0 {
		if _, err := io.Copy(h, io.NewSectionReader(f, 0, offset)); err != nil {
			return &codeError{code: ErrStorage, err: err}
		}
	}
	if b.Size == 0 || offset < b.Size {
		var hdr http.Header
		if offset > 0 {
			hdr = http.Header{"Range": {fmt.Sprintf("bytes=%d-", offset)}}
		}
		resp, err := s.o.Client.Get(ctx, b.URL, hdr)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusPartialContent:
			var start int64 = -1
			fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-", &start)
			if start != offset {
				return fmt.Errorf("unexpected content range %q (want offset %d)", resp.Header.Get("Content-Range"), offset)
			}
		case http.StatusOK:
			if offset > 0 {
				offset = 0
				h.Reset()
				if err := f.Truncate(0); err != nil {
					return &codeError{code: ErrStorage, err: err}
				}
			}
		case http.StatusRequestedRangeNotSatisfiable:
			_ = f.Truncate(0)
			return errChecksum // restart from zero
		default:
			return fmt.Errorf("blob %s: HTTP %d", b.SHA, resp.StatusCode)
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return &codeError{code: ErrStorage, err: err}
		}
		limit := int64(1 << 40)
		if b.Size > 0 {
			limit = b.Size - offset + 1 // one extra byte detects oversized bodies
		}
		cw := &countingWriter{w: f, h: h, cb: progress, off: offset}
		if _, err := io.Copy(cw, io.LimitReader(resp.Body, limit)); err != nil {
			_ = f.Sync()
			return err // keep the partial file for resume
		}
		offset += cw.n
	}
	if err := f.Sync(); err != nil {
		return &codeError{code: ErrStorage, err: err}
	}
	closed = true
	if err := f.Close(); err != nil {
		return &codeError{code: ErrStorage, err: err}
	}
	if b.Size > 0 && offset != b.Size {
		return errChecksum
	}
	if hex.EncodeToString(h.Sum(nil)) != b.SHA {
		return errChecksum
	}
	if err := atomicfile.Rename(partPath, s.o.Store.BlobPath(b.SHA)); err != nil {
		return &codeError{code: ErrStorage, err: err}
	}
	atomicfile.SyncDir(s.o.Store.BlobDir())
	return nil
}

// ---------------------------------------------------------------- cache GC

// GCResult describes a cache cleanup (or, with DryRun, what a cleanup would delete).
type GCResult struct {
	DryRun       bool  `json:"dryRun,omitempty"`
	Deleted      int   `json:"deleted"`      // files: unused content and unfinished downloads
	FreedBytes   int64 `json:"freedBytes"`   // their bytes
	Partial      int   `json:"partial"`      // of which unfinished downloads
	PartialBytes int64 `json:"partialBytes"` // and their bytes
	CacheBytes   int64 `json:"cacheBytes"`   // cache size afterwards
}

// CleanCache runs the garbage collection now (remote action clean_cache).
func (s *Syncer) CleanCache() (GCResult, error) {
	s.work.Lock()
	defer s.work.Unlock()
	return s.gc(false)
}

// PreviewCleanCache reports what CleanCache would delete without deleting anything
// (a cleanup shows a preview first).
func (s *Syncer) PreviewCleanCache() (GCResult, error) {
	s.work.Lock()
	defer s.work.Unlock()
	return s.gc(true)
}

func (s *Syncer) gcLocked() {
	if r, err := s.gc(false); err != nil {
		slog.Warn("cache cleanup skipped", "err", err)
	} else if r.Deleted > 0 {
		slog.Info("cache cleaned", "deleted", r.Deleted, "freedBytes", r.FreedBytes)
	}
}

// gc deletes blobs and partial downloads that no kept deployment (active, previous,
// pending) references. Every rule-reachable playlist is part of the manifest, so files
// needed for offline schedule switches are protected too. With dryRun nothing is deleted;
// the result says what would be. Must hold s.work.
func (s *Syncer) gc(dryRun bool) (GCResult, error) {
	s.mu.Lock()
	dep := s.dep
	s.mu.Unlock()
	keep := map[string]bool{}
	for _, r := range []*state.Ref{dep.Active, dep.Previous, dep.Pending} {
		if r == nil {
			continue
		}
		_, m, err := s.o.Store.LoadManifest(r.ID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && dep.Pending != nil && r.ID == dep.Pending.ID {
				continue // pending manifest not fetched yet
			}
			return GCResult{}, fmt.Errorf("manifest %s unreadable, not collecting: %w", r.ID, err)
		}
		for sha := range referenced(m) {
			keep[sha] = true
		}
	}
	res := GCResult{DryRun: dryRun}
	sweep := func(dir string, partial bool, name func(string) (string, bool)) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			slog.Warn("cache listing failed", "dir", dir, "err", err)
			return
		}
		for _, e := range entries {
			sha, ok := name(e.Name())
			if ok && keep[sha] {
				continue
			}
			p := filepath.Join(dir, e.Name())
			var size int64
			if info, err := e.Info(); err == nil {
				size = info.Size()
			}
			if !dryRun {
				if err := os.RemoveAll(p); err != nil {
					slog.Warn("cache delete failed", "file", p, "err", err)
					continue
				}
			}
			res.Deleted++
			res.FreedBytes += size
			if partial {
				res.Partial++
				res.PartialBytes += size
			}
		}
	}
	sweep(s.o.Store.BlobDir(), false, func(n string) (string, bool) { return n, state.ValidSHA(n) })
	sweep(s.o.Store.TmpDir(), true, func(n string) (string, bool) {
		sha := strings.TrimSuffix(n, ".part")
		return sha, sha != n && state.ValidSHA(sha)
	})
	size := dirSize(s.o.Store.BlobDir())
	s.mu.Lock()
	s.cacheBytes = size
	s.mu.Unlock()
	res.CacheBytes = size
	if dryRun {
		res.CacheBytes = max(0, size-(res.FreedBytes-res.PartialBytes))
	}
	return res, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func dirSize(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var n int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			n += info.Size()
		}
	}
	return n
}
