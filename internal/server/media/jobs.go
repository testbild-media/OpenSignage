package media

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"opensignage/internal/common/ids"
	"opensignage/internal/server/core"
	"opensignage/internal/server/db"
	"opensignage/internal/server/httpx"
)

// progressInfo is the live state of a running job (not persisted).
type progressInfo struct {
	Step    string `json:"step"`
	Index   int    `json:"index"`
	Total   int    `json:"total"`
	Percent int    `json:"percent"`
}

type jobRow struct {
	ID       string
	AssetID  string
	Revision int
	Attempts int
}

// jobError carries a stable code in front of the human readable text.
type jobError struct{ code, msg string }

func (e *jobError) Error() string { return e.code + ": " + e.msg }

var errAssetGone = errors.New("asset no longer exists")

// kick wakes the worker.
func (m *Module) kick() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// enqueue adds a queued job for the given asset revision inside tx.
func enqueue(ctx context.Context, tx *sql.Tx, assetID string, revision int) error {
	now := db.Now()
	_, err := tx.ExecContext(ctx, `INSERT INTO media_jobs(id, asset_id, revision, state, attempts, error, reserved_bytes, created_at, updated_at)
		VALUES (?, ?, ?, 'queued', 0, '', 0, ?, ?)`, ids.New(), assetID, revision, now, now)
	return err
}

// workerLoop processes queued jobs one at a time until ctx is done.
func (m *Module) workerLoop(ctx context.Context) {
	m.requeueInterrupted(ctx)
	m.queueHalfRate(ctx)
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		for ctx.Err() == nil {
			did, err := m.processNext(ctx)
			if err != nil {
				slog.Error("media: job queue", "err", err)
				break
			}
			if !did {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-t.C:
		}
	}
}

// requeueInterrupted puts jobs that were running when the server stopped back in the
// queue (a restart is not a processing failure). A job interrupted three times is
// failed so a poison file cannot crash the server in a loop.
func (m *Module) requeueInterrupted(ctx context.Context) {
	err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		now := db.Now()
		if _, err := tx.ExecContext(ctx, `UPDATE media_jobs SET state = 'failed', reserved_bytes = 0, updated_at = ?,
			error = 'interrupted: the server stopped while processing, too many times' WHERE state = 'running' AND attempts >= 3`, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE media_assets SET status = 'failed', error = 'interrupted: the server stopped while processing, too many times'
			WHERE status = 'processing' AND id IN (SELECT asset_id FROM media_jobs WHERE state = 'failed' AND updated_at = ?)`, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE media_jobs SET state = 'queued', reserved_bytes = 0, updated_at = ? WHERE state = 'running'`, now)
		return err
	})
	if err != nil {
		slog.Error("media: requeue interrupted jobs", "err", err)
	}
}

// queueHalfRate processes videos once more that need v1080h but were processed by an older
// version that did not make v1080h yet: ready, not in the trash, above 30 fps and larger
// than 720p. Videos whose proxy the cleanup of unused media removed are left alone, so the
// cleanup is not undone. It runs at every start; a processed video no longer matches.
// Screens keep their ready revision while the job runs.
func (m *Module) queueHalfRate(ctx context.Context) {
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT a.id, a.revision, a.meta FROM media_assets a
		WHERE a.kind = 'video' AND a.status = 'ready' AND a.trashed_at IS NULL AND a.ready_revision = a.revision
		  AND EXISTS (SELECT 1 FROM media_variants v WHERE v.asset_id = a.id AND v.revision = a.revision AND v.variant = 'v720')
		  AND NOT EXISTS (SELECT 1 FROM media_variants v WHERE v.asset_id = a.id AND v.revision = a.revision AND v.variant = 'v1080h')
		  AND NOT EXISTS (SELECT 1 FROM media_jobs j WHERE j.asset_id = a.id AND j.state IN ('queued', 'running'))`)
	if err != nil {
		slog.Error("media: looking for videos without v1080h", "err", err)
		return
	}
	type rec struct {
		id  string
		rev int
	}
	var todo []rec
	for rows.Next() {
		var r rec
		var meta string
		if err := rows.Scan(&r.id, &r.rev, &meta); err != nil {
			rows.Close()
			slog.Error("media: looking for videos without v1080h", "err", err)
			return
		}
		mm := map[string]any{}
		if jsonUnmarshalMap(meta, &mm) != nil {
			continue
		}
		if needsHalfRate(int(metaNum(mm, "width")), int(metaNum(mm, "height")), metaNum(mm, "fps")) {
			todo = append(todo, r)
		}
	}
	rows.Close()
	if len(todo) == 0 {
		return
	}
	err = m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		now := db.Now()
		for _, r := range todo {
			if _, err := tx.ExecContext(ctx, `DELETE FROM media_jobs WHERE asset_id = ? AND revision = ?`, r.id, r.rev); err != nil {
				return err
			}
			if err := enqueue(ctx, tx, r.id, r.rev); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE media_assets SET status = 'processing', error = '', updated_at = ? WHERE id = ?`, now, r.id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		slog.Error("media: queueing videos for v1080h", "err", err)
		return
	}
	slog.Info("media: videos above 30 fps processed again for the half frame rate variant (v1080h)", "count", len(todo))
}

// metaNum reads a number of an asset's meta (JSON numbers are float64).
func metaNum(meta map[string]any, key string) float64 {
	f, _ := meta[key].(float64)
	return f
}

// processNext claims and runs one job. It reports whether a job was found.
func (m *Module) processNext(ctx context.Context) (bool, error) {
	job, err := m.claimJob(ctx)
	if err != nil || job == nil {
		return false, err
	}
	m.runJob(ctx, *job)
	return true, nil
}

func (m *Module) claimJob(ctx context.Context) (*jobRow, error) {
	var job *jobRow
	err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		var j jobRow
		err := tx.QueryRowContext(ctx, `SELECT id, asset_id, revision, attempts FROM media_jobs WHERE state = 'queued' ORDER BY id LIMIT 1`).
			Scan(&j.ID, &j.AssetID, &j.Revision, &j.Attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE media_jobs SET state = 'running', attempts = attempts + 1, error = '', updated_at = ? WHERE id = ?`, db.Now(), j.ID); err != nil {
			return err
		}
		j.Attempts++
		job = &j
		return nil
	})
	return job, err
}

// runningJob lets a replacement upload cancel the obsolete job of the same asset.
type runningJob struct {
	jobID    string
	revision int
	cancel   context.CancelFunc
}

// cancelObsoleteJob stops the running job of assetID if it processes a revision older than
// newRevision (a replacement arrived; the work would be thrown away).
func (m *Module) cancelObsoleteJob(assetID string, newRevision int) {
	m.jobMu.Lock()
	defer m.jobMu.Unlock()
	if rj := m.running[assetID]; rj != nil && rj.revision < newRevision {
		slog.Info("media: cancelling obsolete job after a replacement", "asset", assetID, "job", rj.jobID)
		rj.cancel()
	}
}

// runJob executes a job, recording the outcome. Panics are contained.
func (m *Module) runJob(ctx context.Context, job jobRow) {
	jctx, cancel := context.WithCancel(ctx)
	rj := &runningJob{jobID: job.ID, revision: job.Revision, cancel: cancel}
	m.jobMu.Lock()
	m.running[job.AssetID] = rj
	m.jobMu.Unlock()
	defer func() {
		m.jobMu.Lock()
		if m.running[job.AssetID] == rj {
			delete(m.running, job.AssetID)
		}
		m.jobMu.Unlock()
		cancel()
		m.progress.Delete(job.AssetID)
	}()
	var err error
	if guard("job "+job.ID, func() { err = m.executeJob(jctx, job) }) {
		err = errors.New("internal error while processing (see server log)")
	}
	switch {
	case err == nil:
	case errors.Is(err, errAssetGone):
		m.finishJob(job, "failed", "asset no longer exists")
		slog.Info("media: job dropped, asset was purged", "job", job.ID)
	case ctx.Err() != nil:
		// shutdown: leave the job to be requeued by the next start
		slog.Info("media: job interrupted by shutdown", "job", job.ID)
	case jctx.Err() != nil:
		m.finishJob(job, "done", "superseded: cancelled by a replacement")
	default:
		slog.Error("media: job failed", "job", job.ID, "asset", job.AssetID, "err", err)
		m.failJob(job, err)
	}
}

func (m *Module) finishJob(job jobRow, state, msg string) {
	bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := m.app.DB.W.ExecContext(bg, `UPDATE media_jobs SET state = ?, error = ?, reserved_bytes = 0, updated_at = ? WHERE id = ?`,
		state, truncate(msg, 500), db.Now(), job.ID); err != nil {
		slog.Error("media: updating job state", "job", job.ID, "err", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// failJob records a processing failure on the job and the asset (no automatic retry).
func (m *Module) failJob(job jobRow, cause error) {
	msg := truncate(cause.Error(), 500)
	bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var name string
	var replaced bool
	err := m.app.DB.Tx(bg, func(tx *sql.Tx) error {
		now := db.Now()
		if _, err := tx.ExecContext(bg, `UPDATE media_jobs SET state = 'failed', error = ?, reserved_bytes = 0, updated_at = ? WHERE id = ?`, msg, now, job.ID); err != nil {
			return err
		}
		var rev int
		err := tx.QueryRowContext(bg, `SELECT name, revision FROM media_assets WHERE id = ?`, job.AssetID).Scan(&name, &rev)
		if errors.Is(err, sql.ErrNoRows) || err == nil && rev != job.Revision {
			return nil
		}
		if err != nil {
			return err
		}
		replaced = rev > 1
		if _, err := tx.ExecContext(bg, `UPDATE media_assets SET status = 'failed', error = ?, updated_at = ? WHERE id = ?`, msg, now, job.AssetID); err != nil {
			return err
		}
		return core.Audit(bg, tx, nil, "system", core.AuditEntry{Action: "media.processing_failed", ObjectType: "media",
			ObjectID: job.AssetID, ObjectName: name, New: map[string]any{"error": msg, "revision": job.Revision}})
	})
	if err != nil {
		slog.Error("media: recording job failure", "job", job.ID, "err", err)
		return
	}
	m.publishMedia("progress", job.AssetID, map[string]any{"status": "failed", "error": msg})
	if replaced {
		m.publishContent("media.failed", job.AssetID)
	}
}

// ---- disk guard ------------------------------------------------------------------------

// effectiveFree returns the free bytes minus what running jobs and open uploads have
// reserved. exceptJob is excluded from the reservations (the job asking).
func (m *Module) effectiveFree(ctx context.Context, exceptJob string) (free uint64, ok bool) {
	_, f, err := m.diskFn(m.dataDir)
	if err != nil {
		slog.Warn("media: cannot determine free disk space, not enforcing the guard", "err", err)
		return 0, false
	}
	var reserved, pending int64
	if err := m.app.DB.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(reserved_bytes), 0) FROM media_jobs WHERE state = 'running' AND id <> ?`, exceptJob).Scan(&reserved); err != nil {
		slog.Warn("media: reading job reservations", "err", err)
	}
	if err := m.app.DB.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(size - received), 0) FROM uploads`).Scan(&pending); err != nil {
		slog.Warn("media: reading upload reservations", "err", err)
	}
	used := uint64(max(reserved, 0)) + uint64(max(pending, 0))
	if used >= f {
		return 0, true
	}
	return f - used, true
}

// checkDisk fails with a disk_full error when need bytes are not available.
func (m *Module) checkDisk(ctx context.Context, need int64, exceptJob string) error {
	free, ok := m.effectiveFree(ctx, exceptJob)
	if ok && free < uint64(need) {
		return diskFullErr(need, int64(free))
	}
	return nil
}

func diskFullErr(need, free int64) *httpx.Error {
	return httpx.E(http.StatusInsufficientStorage, "disk_full", "not enough free disk space").
		With("needed", need).With("free", free)
}

// jobDiskErr turns a disk_full API error into the job error text.
func jobDiskErr(e *httpx.Error) error {
	return &jobError{"disk_full", fmt.Sprintf("not enough free disk space (needed %s, free %s)",
		humanBytes(asInt64(e.Params["needed"])), humanBytes(asInt64(e.Params["free"])))}
}

func asInt64(v any) int64 {
	if n, ok := v.(int64); ok {
		return n
	}
	return 0
}

// ---- processing ------------------------------------------------------------------------

type assetRec struct {
	ID        string
	Name      string
	Kind      string
	Status    string
	Revision  int
	FolderID  *string
	BlobSHA   string
	Size      int64
	Mime      string
	TrashedAt *string
}

func loadAssetRec(ctx context.Context, q queryer, id string) (*assetRec, error) {
	var a assetRec
	var folder, trashed sql.NullString
	var sha sql.NullString
	err := q.QueryRowContext(ctx, `SELECT a.id, a.name, a.kind, a.status, a.revision, a.folder_id, a.blob_sha256, a.trashed_at,
		COALESCE(b.size, 0), COALESCE(b.mime, '') FROM media_assets a LEFT JOIN blobs b ON b.sha256 = a.blob_sha256 WHERE a.id = ?`, id).
		Scan(&a.ID, &a.Name, &a.Kind, &a.Status, &a.Revision, &folder, &sha, &trashed, &a.Size, &a.Mime)
	if err != nil {
		return nil, err
	}
	a.FolderID, a.TrashedAt, a.BlobSHA = nullStr(folder), nullStr(trashed), sha.String
	return &a, nil
}

type variantRec struct {
	Name          string
	SHA           string // set once stored (or immediately for aliases and reused variants)
	Path          string // finished output in the job directory, not stored yet
	Width, Height int
}

type jobResult struct {
	Meta     map[string]any
	Kind     string
	Variants []variantRec
}

func (m *Module) setProgress(assetID string, pi progressInfo, publish bool) {
	m.progress.Store(assetID, pi)
	if publish {
		m.publishMedia("progress", assetID, map[string]any{"status": "processing", "step": pi.Step,
			"index": pi.Index, "total": pi.Total, "percent": pi.Percent})
	}
}

func (m *Module) executeJob(ctx context.Context, job jobRow) error {
	a, err := loadAssetRec(ctx, m.app.DB.R, job.AssetID)
	if errors.Is(err, sql.ErrNoRows) {
		return errAssetGone
	}
	if err != nil {
		return err
	}
	if a.Revision != job.Revision {
		m.finishJob(job, "done", "superseded by a newer revision")
		return nil
	}
	src := m.BlobPath(a.BlobSHA)
	if src == "" {
		return &jobError{"file_missing", "the asset has no stored original"}
	}
	st, err := os.Stat(src)
	if err != nil {
		return &jobError{"file_missing", "the stored original is missing on disk"}
	}
	head := readHead(src, 8192)
	ti := DetectType(head)
	if !ti.Allowed() {
		return &jobError{"unsupported_type", "stored file content is not an allowed media type"}
	}
	m.setProgress(a.ID, progressInfo{Step: "probe", Total: 1}, true)

	need := 2*st.Size() + diskReserve
	if err := m.checkDisk(ctx, need, job.ID); err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return jobDiskErr(he)
		}
		return err
	}
	if _, err := m.app.DB.W.ExecContext(ctx, `UPDATE media_jobs SET reserved_bytes = ?, updated_at = ? WHERE id = ?`, 2*st.Size(), db.Now(), job.ID); err != nil {
		return err
	}

	work := m.tmpDir("jobs", job.ID)
	if err := m.ensureDir(work); err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(work); err != nil {
			slog.Warn("media: removing job directory", "dir", work, "err", err)
		}
	}()

	var res *jobResult
	switch {
	case ti.Mime == "image/svg+xml":
		res, err = m.processSVG(src)
	case ti.Kind == KindImage:
		res, err = m.processImage(ctx, job, a, src, work, ti)
	case ti.Kind == KindVideo || ti.Kind == KindAudio:
		if res, err = m.copyFromTwin(ctx, a, ti); err == nil && res == nil {
			res, err = m.processAV(ctx, job, a, src, work, ti)
		}
	}
	if err != nil {
		return err
	}
	pinned, err := m.storeVariants(ctx, res)
	defer m.unpin(pinned...)
	if err != nil {
		return err
	}
	if m.beforeFinalize != nil {
		m.beforeFinalize()
	}
	return m.finalizeJob(ctx, job, a, res)
}

func (m *Module) processSVG(src string) (*jobResult, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := readSVG(f)
	if err != nil {
		return nil, &jobError{"svg_unsafe", "SVG is too large"}
	}
	if p := checkSVGBytes(b); p != "" {
		return nil, &jobError{"svg_unsafe", "SVG contains unsafe content: " + string(p)}
	}
	w, h := svgSize(b)
	meta := map[string]any{"mime": "image/svg+xml"}
	if w > 0 && h > 0 {
		meta["width"], meta["height"] = w, h
	}
	return &jobResult{Meta: meta, Kind: KindImage}, nil
}

func buildMeta(ti TypeInfo, p Probe) map[string]any {
	meta := map[string]any{"mime": ti.Mime}
	set := func(k string, v any, ok bool) {
		if ok {
			meta[k] = v
		}
	}
	set("width", p.Width, p.Width > 0)
	set("height", p.Height, p.Height > 0)
	set("durationMs", p.DurationMs, p.DurationMs > 0)
	set("fps", p.FPS, p.FPS > 0 && p.HasVideo && p.Frames != 1)
	set("videoCodec", p.VideoCodec, p.VideoCodec != "")
	set("audioCodec", p.AudioCodec, p.AudioCodec != "")
	set("pixFmt", p.PixFmt, p.PixFmt != "")
	set("container", p.Container, p.Container != "")
	set("bitrate", p.Bitrate, p.Bitrate > 0)
	set("hasAudio", true, p.HasAudio)
	set("rotation", p.Rotation, p.Rotation != 0)
	set("animated", true, ti.Mime == "image/gif" && p.Frames > 1)
	set("hasAlpha", true, p.HasAlpha)
	return meta
}

func (m *Module) processImage(ctx context.Context, job jobRow, a *assetRec, src, work string, ti TypeInfo) (*jobResult, error) {
	p, err := m.tc.Probe(ctx, src)
	if err != nil {
		return nil, &jobError{"probe_failed", err.Error()}
	}
	if !p.HasVideo || p.Width <= 0 || p.Height <= 0 {
		return nil, &jobError{"probe_failed", "no decodable image found in the file"}
	}
	if p.Rotation == 0 {
		// EXIF orientation: ffmpeg rotates the pixels when it encodes variants, so the
		// recorded size must be the displayed (rotated) size as well.
		if rot, rerr := m.tc.ProbeRotation(ctx, src); rerr != nil {
			slog.Debug("media: probing image orientation", "asset", a.ID, "err", rerr)
		} else if rot != 0 {
			p.Rotation = rot
			if rot == 90 || rot == 270 {
				p.Width, p.Height = p.Height, p.Width
			}
		}
	}
	vars, err := m.runSpecs(ctx, job, a, src, work, planImage(p, ti.Mime), p)
	if err != nil {
		return nil, err
	}
	return &jobResult{Meta: buildMeta(ti, p), Kind: KindImage, Variants: vars}, nil
}

func (m *Module) processAV(ctx context.Context, job jobRow, a *assetRec, src, work string, ti TypeInfo) (*jobResult, error) {
	p, err := m.tc.Probe(ctx, src)
	if err != nil {
		return nil, &jobError{"probe_failed", err.Error()}
	}
	kind := ti.Kind
	switch {
	case kind == KindVideo && !p.HasVideo && p.HasAudio:
		kind = KindAudio // e.g. an audio-only MP4/MKV/WebM
	case kind == KindAudio && p.HasVideo:
		kind = KindVideo // e.g. an Ogg video
	}
	if kind == KindVideo && !p.HasVideo || kind == KindAudio && !p.HasAudio {
		return nil, &jobError{"probe_failed", "no playable " + kind + " stream found in the file"}
	}
	if kind == KindVideo && (p.Width <= 0 || p.Height <= 0) {
		return nil, &jobError{"probe_failed", "video has no valid dimensions"}
	}
	var specs []outSpec
	if kind == KindVideo {
		specs = planVideo(p)
	} else {
		specs = planAudio(p)
	}
	vars, err := m.runSpecs(ctx, job, a, src, work, specs, p)
	if err != nil {
		return nil, err
	}
	return &jobResult{Meta: buildMeta(ti, p), Kind: kind, Variants: vars}, nil
}

// runSpecs produces all planned variants one by one. Each step checks the disk first.
func (m *Module) runSpecs(ctx context.Context, job jobRow, a *assetRec, src, work string, specs []outSpec, p Probe) ([]variantRec, error) {
	var out []variantRec
	for i, s := range specs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pi := progressInfo{Step: s.Name, Index: i + 1, Total: len(specs)}
		m.setProgress(a.ID, pi, true)
		if s.Alias {
			out = append(out, variantRec{Name: s.Name, SHA: a.BlobSHA})
			continue
		}
		if err := m.checkDisk(ctx, 2*a.Size+diskReserve, job.ID); err != nil {
			var he *httpx.Error
			if errors.As(err, &he) {
				return nil, jobDiskErr(he)
			}
			return nil, err
		}
		dst := filepath.Join(work, s.Name+s.Ext)
		var last time.Time
		onProgress := func(ms int64) {
			if p.DurationMs <= 0 || !s.Progress || time.Since(last) < time.Second {
				return
			}
			last = time.Now()
			pi.Percent = int(min(99, ms*100/p.DurationMs))
			m.setProgress(a.ID, pi, true)
		}
		err := m.tc.Run(ctx, ffmpegArgs(src, dst, s), onProgress)
		if err != nil && ctx.Err() == nil && len(s.Pre) > 0 {
			// A seek behind the last frame (container longer than its video stream)
			// produces no output: take the first frame instead.
			slog.Debug("media: retrying a seeked still without seeking", "variant", s.Name, "err", err)
			s.Pre = nil
			err = m.tc.Run(ctx, ffmpegArgs(src, dst, s), onProgress)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if strings.Contains(err.Error(), "No space left on device") {
				return nil, &jobError{"disk_full", fmt.Sprintf("the disk filled up while creating %s", s.Name)}
			}
			return nil, &jobError{"ffmpeg_failed", fmt.Sprintf("%s: %v", s.Name, err)}
		}
		st, err := os.Stat(dst)
		if err != nil || st.Size() == 0 {
			return nil, &jobError{"ffmpeg_failed", s.Name + ": ffmpeg produced no output"}
		}
		var w, h int
		if op, perr := m.tc.Probe(ctx, dst); perr == nil {
			w, h = op.Width, op.Height
		} else if ctx.Err() == nil {
			slog.Debug("media: probing variant output", "variant", s.Name, "err", perr)
		}
		out = append(out, variantRec{Name: s.Name, Path: dst, Width: w, Height: h})
	}
	return out, nil
}

// storeVariants moves the finished outputs of a job into the blob store. It runs right
// before the final transaction (not after each transcode), and every stored blob is pinned
// so the garbage collector cannot remove it before media_variants references it. The
// caller releases the pins with unpin(pinned...).
func (m *Module) storeVariants(ctx context.Context, res *jobResult) (pinned []string, err error) {
	for i := range res.Variants {
		v := &res.Variants[i]
		if v.Path == "" {
			continue
		}
		sha, _, _, err := m.ingestPinned(ctx, v.Path)
		if err != nil {
			return pinned, fmt.Errorf("storing variant %s: %w", v.Name, err)
		}
		pinned = append(pinned, sha)
		v.SHA, v.Path = sha, ""
	}
	return pinned, nil
}

// copyFromTwin reuses the variants of another ready asset with the identical original
// (an expensive transcode is not repeated for a duplicate upload).
func (m *Module) copyFromTwin(ctx context.Context, a *assetRec, ti TypeInfo) (*jobResult, error) {
	primary := "v1080"
	if ti.Kind == KindAudio {
		primary = "a"
	}
	var twinID, meta, kind string
	var twinRev int
	err := m.app.DB.R.QueryRowContext(ctx, `SELECT t.id, t.revision, t.meta, t.kind FROM media_assets t
		WHERE t.blob_sha256 = ? AND t.id <> ? AND t.status = 'ready'
		  AND EXISTS (SELECT 1 FROM media_variants v WHERE v.asset_id = t.id AND v.revision = t.revision AND v.variant = ?)
		ORDER BY t.updated_at DESC LIMIT 1`, a.BlobSHA, a.ID, primary).Scan(&twinID, &twinRev, &meta, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT variant, blob_sha256, width, height FROM media_variants WHERE asset_id = ? AND revision = ?`, twinID, twinRev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := &jobResult{Kind: kind, Meta: map[string]any{}}
	for rows.Next() {
		var v variantRec
		if err := rows.Scan(&v.Name, &v.SHA, &v.Width, &v.Height); err != nil {
			return nil, err
		}
		if m.BlobSize(v.SHA) < 0 {
			return nil, nil // twin is damaged: process normally
		}
		res.Variants = append(res.Variants, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := jsonUnmarshalMap(meta, &res.Meta); err != nil {
		return nil, nil
	}
	if kind == KindVideo && !twinComplete(res) {
		return nil, nil // processed by an older version or cleaned up: process normally
	}
	slog.Info("media: reused variants of an identical file", "asset", a.ID, "twin", twinID)
	return res, nil
}

// twinComplete reports whether a twin video has every variant the current rules make (a
// twin from before v1080h lacks it; the cleanup of unused media removes v720 and posters).
func twinComplete(res *jobResult) bool {
	have := map[string]bool{}
	for _, v := range res.Variants {
		have[v.Name] = true
	}
	w, h, fps := int(metaNum(res.Meta, "width")), int(metaNum(res.Meta, "height")), metaNum(res.Meta, "fps")
	want := []string{"v1080", "poster", "thumb"}
	if needsHalfRate(w, h, fps) {
		want = append(want, "v1080h")
	}
	if needsProxy(w, h, fps) {
		want = append(want, "v720")
	}
	for _, n := range want {
		if !have[n] {
			return false
		}
	}
	return true
}

// finalizeJob stores the results atomically and marks asset and job as done.
func (m *Module) finalizeJob(ctx context.Context, job jobRow, a *assetRec, res *jobResult) error {
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	metaJSON, err := marshalJSON(res.Meta)
	if err != nil {
		return err
	}
	var superseded bool
	err = m.app.DB.Tx(bg, func(tx *sql.Tx) error {
		var rev int
		err := tx.QueryRowContext(bg, `SELECT revision FROM media_assets WHERE id = ?`, a.ID).Scan(&rev)
		if errors.Is(err, sql.ErrNoRows) {
			return errAssetGone
		}
		if err != nil {
			return err
		}
		now := db.Now()
		if rev != job.Revision {
			superseded = true
			_, err := tx.ExecContext(bg, `UPDATE media_jobs SET state = 'done', error = 'superseded by a newer revision', reserved_bytes = 0, updated_at = ? WHERE id = ?`, now, job.ID)
			return err
		}
		if _, err := tx.ExecContext(bg, `DELETE FROM media_variants WHERE asset_id = ? AND revision = ?`, a.ID, job.Revision); err != nil {
			return err
		}
		for _, v := range res.Variants {
			if _, err := tx.ExecContext(bg, `INSERT INTO media_variants(asset_id, revision, variant, blob_sha256, width, height, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, a.ID, job.Revision, v.Name, v.SHA, v.Width, v.Height, now); err != nil {
				return err
			}
		}
		kind := res.Kind
		if kind == "" {
			kind = a.Kind
		}
		if _, err := tx.ExecContext(bg, `UPDATE media_assets SET status = 'ready', error = '', meta = ?, kind = ?, ready_revision = ?, updated_at = ? WHERE id = ?`,
			metaJSON, kind, job.Revision, now, a.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(bg, `UPDATE media_revisions SET meta = ? WHERE asset_id = ? AND revision = ?`, metaJSON, a.ID, job.Revision); err != nil {
			return err
		}
		_, err = tx.ExecContext(bg, `UPDATE media_jobs SET state = 'done', error = '', reserved_bytes = 0, updated_at = ? WHERE id = ?`, now, job.ID)
		return err
	})
	if err != nil {
		return err
	}
	if superseded {
		return nil
	}
	m.publishMedia("progress", a.ID, map[string]any{"status": "ready", "percent": 100})
	// Every successfully processed revision (first upload, replacement, reprocess) changes
	// what deployments can ship: variants appeared or were rebuilt.
	m.publishContent("media.ready", a.ID)
	return nil
}

// reprocess re-queues processing of the current revision (POST /media/{id}/reprocess).
func (m *Module) reprocess(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	id := r.PathValue("id")
	err := m.app.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		a, err := loadAssetRec(r.Context(), tx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		var active int
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM media_jobs WHERE asset_id = ? AND state IN ('queued', 'running')`, id).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return httpx.E(http.StatusConflict, "job_active", "the asset is already being processed")
		}
		now := db.Now()
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM media_jobs WHERE asset_id = ? AND revision = ?`, id, a.Revision); err != nil {
			return err
		}
		if err := enqueue(r.Context(), tx, id, a.Revision); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE media_assets SET status = 'processing', error = '', updated_at = ? WHERE id = ?`, now, id); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p, httpx.ClientIP(r), core.AuditEntry{Action: "media.reprocess", ObjectType: "media",
			ObjectID: id, ObjectName: a.Name, Old: map[string]any{"status": a.Status}})
	})
	if err != nil {
		return err
	}
	m.kick()
	m.publishMedia("progress", id, map[string]any{"status": "processing"})
	v, err := m.assetByID(r.Context(), id)
	if err != nil {
		return err
	}
	return httpx.OK(w, v)
}
