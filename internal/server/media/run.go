package media

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Run is the background loop: one job worker (ffmpeg) and a maintenance loop (expired
// uploads, trash retention, blob garbage collection). Both are supervised: a panic is
// logged and the loop restarts. Run returns when ctx is done.
func (m *Module) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		supervise(ctx, "worker", func() { m.workerLoop(ctx) })
	}()
	go func() {
		defer wg.Done()
		supervise(ctx, "maintenance", func() { m.maintenanceLoop(ctx) })
	}()
	wg.Wait()
}

func (m *Module) maintenanceLoop(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		m.maintenance(ctx, time.Now())
		timer.Reset(10 * time.Minute)
	}
}

// maintenance does the periodic housekeeping; the heavy parts run once per day.
func (m *Module) maintenance(ctx context.Context, now time.Time) {
	m.maintMu.Lock()
	defer m.maintMu.Unlock()
	m.cleanupUploads(ctx)
	if now.Sub(m.lastDay) < 24*time.Hour {
		return
	}
	m.lastDay = now
	m.purgeExpired(ctx)
	if _, err := m.CollectGarbage(ctx, false); err != nil {
		slog.Error("media: daily garbage collection", "err", err)
	}
	m.removeEmptyBlobDirs()
}
