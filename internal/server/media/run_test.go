package media

import (
	"context"
	"sync"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRunLoopProcessesJobsAndStops(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.m.Run(ctx); close(done) }()

	a := c.mustUpload("run.png", pngBytes(64, 48, 1), nil)["id"].(string)
	waitFor(t, "asset ready", 5*time.Second, func() bool { st, _ := h.assetStatus(a); return st == "ready" })

	// a job queued while the worker idles wakes it immediately (not on the 10 s tick)
	b := c.mustUpload("run2.png", pngBytes(64, 48, 2), nil)["id"].(string)
	waitFor(t, "second asset ready", 3*time.Second, func() bool { st, _ := h.assetStatus(b); return st == "ready" })

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run must return after the context is cancelled")
	}
}

// blockingTC hangs in ffmpeg until its context is cancelled (a long transcode).
type blockingTC struct {
	fakeTC
	once    sync.Once
	started chan struct{}
}

func (b *blockingTC) Run(ctx context.Context, _ []string, _ func(int64)) error {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return ctx.Err()
}

func TestShutdownMidJobRequeuesInsteadOfFailing(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	a := c.mustUpload("long.png", pngBytes(64, 48, 3), nil)["id"].(string)

	b := &blockingTC{started: make(chan struct{})}
	h.m.tc = b
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.m.Run(ctx); close(done) }()
	select {
	case <-b.started:
	case <-time.After(5 * time.Second):
		t.Fatal("job did not start")
	}
	var reserved int64
	_ = h.app.DB.R.QueryRow(`SELECT reserved_bytes FROM media_jobs WHERE asset_id = ?`, a).Scan(&reserved)
	if reserved <= 0 {
		t.Fatalf("a running job reserves disk space: %d", reserved)
	}
	cancel()
	<-done

	var state string
	_ = h.app.DB.R.QueryRow(`SELECT state FROM media_jobs WHERE asset_id = ?`, a).Scan(&state)
	if st, _ := h.assetStatus(a); st != "processing" || state != "running" {
		t.Fatalf("shutdown must not mark the job failed: asset=%s job=%s", st, state)
	}
	// next start: the interrupted job is picked up again and finishes
	h.m.tc = h.tc
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { h.m.Run(ctx2); close(done2) }()
	waitFor(t, "asset ready after restart", 5*time.Second, func() bool { st, _ := h.assetStatus(a); return st == "ready" })
	cancel2()
	<-done2
	_ = h.app.DB.R.QueryRow(`SELECT state FROM media_jobs WHERE asset_id = ?`, a).Scan(&state)
	if state != "done" {
		t.Fatalf("job state: %s", state)
	}
	if h.count(`SELECT COUNT(*) FROM media_jobs WHERE reserved_bytes <> 0`) != 0 {
		t.Fatal("reservations must be released")
	}
}

func TestMaintenanceRunsDailyWork(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	old := c.mustUpload("old.png", pngBytes(12, 12, 1), nil)["id"].(string)
	c.mustOK("DELETE", "/api/v1/media/"+old, nil)
	h.exec(`UPDATE media_assets SET trashed_at = '2000-01-01T00:00:00Z'`)
	h.seedBlob(data(40, "garbage"))
	s := h.app.Settings.Get()
	s.TrashRetentionDays = 7
	h.app.Settings.Apply(s)
	// make the garbage blob old enough for the collector
	h.exec(`UPDATE blobs SET created_at = '2000-01-01T00:00:00Z'`)

	h.m.maintenance(context.Background(), time.Now())
	if h.count(`SELECT COUNT(*) FROM media_assets`) != 0 || len(h.blobFiles()) != 0 {
		t.Fatalf("daily run purges expired trash and collects garbage: assets=%d files=%d", h.count(`SELECT COUNT(*) FROM media_assets`), len(h.blobFiles()))
	}
	// the heavy part runs only once per day
	c2 := c.mustUpload("again.png", pngBytes(12, 12, 2), nil)["id"].(string)
	c.mustOK("DELETE", "/api/v1/media/"+c2, nil)
	h.exec(`UPDATE media_assets SET trashed_at = '2000-01-01T00:00:00Z'`)
	h.m.maintenance(context.Background(), time.Now().Add(time.Hour))
	if h.count(`SELECT COUNT(*) FROM media_assets`) != 1 {
		t.Fatal("second run within 24 h must not purge again")
	}
	h.m.maintenance(context.Background(), time.Now().Add(25*time.Hour))
	if h.count(`SELECT COUNT(*) FROM media_assets`) != 0 {
		t.Fatal("run after 24 h purges")
	}
}

func TestRouteDispatchEdgeCases(t *testing.T) {
	h := newHarness(t)
	c := h.login("operator")
	a := c.mustUpload("x.png", pngBytes(10, 10, 1), nil)["id"].(string)
	// the two-segment GET dispatcher must not leak between its two meanings
	for path, want := range map[string]int{
		"/api/v1/media/uploads/file":       404, // looks like {id}/file but "uploads" is the upload namespace
		"/api/v1/media/" + a + "/other":    404,
		"/api/v1/media/" + a + "/file":     200,
		"/api/v1/media/uploads/unknown-id": 404,
		"/api/v1/media/trash":              200,
		"/api/v1/media/unused":             200,
	} {
		if res, _ := c.req("GET", path, nil, nil); res.StatusCode != want {
			t.Errorf("GET %s: %d, want %d", path, res.StatusCode, want)
		}
	}
	// the upload status endpoint is operator+ even though the route is shared with file serving
	u := h.login("user")
	if res, _ := u.req("GET", "/api/v1/media/uploads/anything", nil, nil); res.StatusCode != 403 {
		t.Errorf("user on uploads: %d", res.StatusCode)
	}
}
