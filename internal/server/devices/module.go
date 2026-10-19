// Package devices implements device management: PIN pairing, the device channel on port
// 7443 (REST + the device link WebSocket), the live hub that pushes complete zone live
// states, the deployment builder loop, remote actions, monitoring and alerts.
//
// Ownership (ARCHITECTURE §7): devices, pairing_requests, deployments,
// deployment_blobs, device_deployments. Cross-module reactions arrive on the event bus
// (topics live, products, zones, content, settings, devices); this module publishes
// `devices` events and `live` events of kind "pushed".
package devices

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
	"opensignage/internal/server/devices/deploy"
)

// Event kinds this module publishes on core.TopicDevices.
const (
	EvClaimed      = "claimed"
	EvUpdated      = "updated"
	EvRepaired     = "repaired"
	EvDeleted      = "deleted"
	EvOnline       = "online"
	EvOffline      = "offline"
	EvStatus       = "status"
	EvProgress     = "progress"
	EvAction       = "action"
	EvPairing      = "pairing"
	EvDeployment   = "deployment"
	EvBlackout     = "blackout"
	EvUpdateReport = "update_progress"
	EvAlertAck     = "alert_ack" // a problem was acknowledged or shown again
)

// telemetryKinds are device events that never require re-reading device settings.
var telemetryKinds = map[string]bool{EvOnline: true, EvOffline: true, EvStatus: true, EvProgress: true,
	EvAction: true, EvPairing: true, EvDeployment: true, EvUpdateReport: true, EvAlertAck: true}

// PackageProvider serves release packages and their signed metadata to devices
// (`GET /device/v1/update/package`, `GET /device/v1/update/release`). The updates module
// implements it.
type PackageProvider interface {
	// DevicePackage opens the package for os/arch of the given version. It returns
	// ErrNoPackage when none is available.
	DevicePackage(ctx context.Context, version, goos, arch string) (*Package, error)
	// DeviceRelease returns release.json, SHA256SUMS and SHA256SUMS.sig of the given
	// version unchanged (the agent verifies them itself). It returns ErrNoPackage when
	// the release is not available.
	DeviceRelease(ctx context.Context, version string) (*contracts.UpdateRelease, error)
}

// Package is an open release package.
type Package struct {
	Name    string
	ModTime time.Time
	Content io.ReadSeekCloser
}

// ErrNoPackage is returned by a PackageProvider without a matching package.
var ErrNoPackage = fmt.Errorf("no package")

// UpdateListener receives `update.progress` reports from devices (updates module).
type UpdateListener interface {
	UpdateProgress(deviceID string, p contracts.UpdateProgress)
}

// Module is the devices module.
type Module struct {
	app     *core.App
	log     *slog.Logger
	hub     *hub
	actions *actionStore
	builder *deploy.Builder
	pairRL  *ipLimiter

	since      *sinceTracker // when a device problem was first seen
	acks       *ackStore     // acknowledged device problems
	cfgChanges *cfgChanges   // when blackout/display were last changed

	seq      atomic.Uint64 // orders snapshot messages (config, live, deploy) per connection
	buildMu  sync.Mutex
	running  atomic.Bool
	packages atomic.Pointer[PackageProvider]
	updates  atomic.Pointer[UpdateListener]

	expectMu      sync.Mutex
	expectOffline map[string]time.Time // device → planned offline until (reboot/restart)

	// Tunables (tests shorten them).
	HelloTimeout   time.Duration
	PingInterval   time.Duration
	WriteTimeout   time.Duration
	LiveDebounce   time.Duration
	DeployDebounce time.Duration
	ActionTimeout  time.Duration
	OfflineAfter   time.Duration
	// PlannedOffline is how long after a reboot/restart action the device may be offline
	// without an alert.
	PlannedOffline time.Duration
	// DeployRetry is the first delay before a failed deployment rebuild or live push is
	// tried again (doubling up to maxRetryDelay, at most maxRetries times in a row).
	DeployRetry time.Duration

	// Pairing bounds: open requests in total (the oldest is evicted beyond it) and per
	// source address (refused beyond it).
	MaxOpenPairings  int
	MaxPairingsPerIP int
}

var (
	regMu    sync.Mutex
	registry = map[*core.App]*Module{}
)

// New creates the module.
func New(a *core.App) *Module {
	log := a.Log
	if log == nil {
		log = slog.Default()
	}
	m := &Module{
		app:            a,
		log:            log.With("module", "devices"),
		hub:            newHub(),
		actions:        newActionStore(),
		builder:        &deploy.Builder{DB: a.DB, DataDir: a.Cfg.DataDir, Log: log.With("module", "deploy")},
		pairRL:         newIPLimiter(30, time.Minute, 4096),
		since:          newSinceTracker(),
		acks:           newAckStore(),
		cfgChanges:     newCfgChanges(),
		expectOffline:  map[string]time.Time{},
		HelloTimeout:   10 * time.Second,
		PingInterval:   30 * time.Second,
		WriteTimeout:   15 * time.Second,
		LiveDebounce:   50 * time.Millisecond,
		DeployDebounce: 500 * time.Millisecond,
		ActionTimeout:  60 * time.Second,
		OfflineAfter:   2 * time.Minute,
		PlannedOffline: plannedOfflineFor,
		DeployRetry:    2 * time.Second,

		MaxOpenPairings:  500,
		MaxPairingsPerIP: 5,
	}
	regMu.Lock()
	registry[a] = m
	regMu.Unlock()
	return m
}

// For returns the devices module of an app (nil if not constructed). Other modules
// (updates) use it to send offers and to plug in package and progress handling.
func For(a *core.App) *Module {
	regMu.Lock()
	defer regMu.Unlock()
	return registry[a]
}

// SetPackageProvider plugs in the release package source.
func (m *Module) SetPackageProvider(p PackageProvider) { m.packages.Store(&p) }

// SetUpdateListener plugs in the receiver of update.progress reports.
func (m *Module) SetUpdateListener(l UpdateListener) { m.updates.Store(&l) }

// SendUpdateOffer sends `update.offer` to a connected device; false if it is offline.
func (m *Module) SendUpdateOffer(deviceID string, offer contracts.UpdateOffer) bool {
	c := m.hub.get(deviceID)
	if c == nil {
		return false
	}
	b, err := encode(contracts.MsgUpdateOffer, "", offer)
	if err != nil {
		m.log.Error("encode update offer", "err", err)
		return false
	}
	return c.enqueue(b)
}

// Online reports whether a device currently has a link connection.
func (m *Module) Online(deviceID string) bool { return m.hub.get(deviceID) != nil }

// Routes registers the WebGUI API (443).
func (m *Module) Routes(rt *core.Router) {
	rt.Role("GET /api/v1/devices", core.RoleUser, m.list)
	rt.Role("GET /api/v1/devices/pairing", core.RoleOperator, m.pairingList)
	rt.Role("DELETE /api/v1/devices/pairing/{id}", core.RoleOperator, m.dismissPairing)
	rt.Role("POST /api/v1/devices/claim", core.RoleOperator, m.claim)
	rt.Role("GET /api/v1/devices/{id}", core.RoleUser, m.get)
	rt.Role("PATCH /api/v1/devices/{id}", core.RoleOperator, m.patch)
	rt.Role("POST /api/v1/devices/{id}/repair", core.RoleOperator, m.repair)
	rt.Role("DELETE /api/v1/devices/{id}", core.RoleOperator, m.delete)
	// Operators limited to zones: displays of their zones only (screenshots with view access,
	// everything else with the displays right and edit access, see deviceAccess and postAction).
	rt.Scoped("POST /api/v1/devices/{id}/actions", core.RoleOperator, m.deviceAccess(false, m.postAction))
	rt.Scoped("GET /api/v1/devices/{id}/actions/{requestId}", core.RoleOperator, m.deviceAccess(false, m.getAction))
	rt.Scoped("GET /api/v1/devices/{id}/screenshot", core.RoleOperator, m.deviceAccess(false, m.screenshot))
	rt.Scoped("POST /api/v1/devices/{id}/blackout", core.RoleOperator, m.deviceAccess(true, m.blackout))
	// Zone preview: what the screens of a zone show now or at another moment (zone viewers).
	rt.Scoped("GET /api/v1/zones/{id}/preview", core.RoleOperator, m.zonePreview)
	rt.Role("GET /api/v1/alerts", core.RoleUser, m.alerts)
	rt.Role("POST /api/v1/alerts/ack", core.RoleUser, m.ackAlert)
	rt.Role("POST /api/v1/alerts/unack", core.RoleUser, m.unackAlert)
}

// DeviceRoutes registers the device channel (7443).
func (m *Module) DeviceRoutes(mux *http.ServeMux) {
	mux.Handle("POST /device/v1/pair/request", m.isolate(m.pairRequest))
	mux.Handle("GET /device/v1/pair/status", m.isolate(m.pairStatus))
	mux.Handle("GET /device/v1/ws", http.HandlerFunc(m.serveWS))
	mux.Handle("GET /device/v1/deployments/{id}", m.isolate(m.manifest))
	mux.Handle("GET /device/v1/blobs/{sha256}", m.isolate(m.blob))
	mux.Handle("POST /device/v1/screenshots/{requestId}", m.isolate(m.uploadScreenshot))
	mux.Handle("GET /device/v1/update/package", m.isolate(m.updatePackage))
	mux.Handle("GET /device/v1/update/release", m.isolate(m.updateRelease))
}

// Run starts the live hub, the deployment loop and housekeeping. It blocks until ctx is
// done and then closes all device connections with 1001 (going away).
func (m *Module) Run(ctx context.Context) {
	if !m.running.CompareAndSwap(false, true) {
		m.log.Error("devices: Run called twice")
		<-ctx.Done()
		return
	}
	defer m.running.Store(false)
	m.hub.reopen()
	var wg sync.WaitGroup
	loop := func(name string, f func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				func() {
					defer func() {
						if p := recover(); p != nil {
							m.log.Error("devices: loop crashed, restarting", "loop", name, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
							select {
							case <-ctx.Done():
							case <-time.After(time.Second):
							}
						}
					}()
					f(ctx)
				}()
			}
		}()
	}
	loop("live", m.liveLoop)
	loop("deploy", m.deployLoop)
	loop("housekeeping", m.housekeeping)
	<-ctx.Done()
	wg.Wait()
	m.hub.closeAll(1001, "server shutting down", 5*time.Second)
}

// isolate adapts an error-returning handler and contains panics (one device can never
// stop the server).
func (m *Module) isolate(h func(w http.ResponseWriter, r *http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				m.log.Error("devices: panic in device handler", "path", r.URL.Path, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
				writeErr(w, r, errInternal)
			}
		}()
		if err := h(w, r); err != nil {
			writeErr(w, r, err)
		}
	})
}

// housekeeping removes expired pairing requests, times out actions and forgets planned
// offline windows.
func (m *Module) housekeeping(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	lastPairing := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if changed := m.actions.sweep(now, m.ActionTimeout); len(changed) > 0 {
				for _, a := range changed {
					m.app.Bus.Publish(core.Event{Topic: core.TopicDevices, Kind: EvAction, ID: a.DeviceID,
						Data: map[string]any{"requestId": a.ID, "state": a.State}})
				}
			}
			if now.Sub(lastPairing) >= time.Minute {
				lastPairing = now
				m.cleanupPairing(ctx)
				m.expectMu.Lock()
				for id, until := range m.expectOffline {
					if now.After(until) {
						delete(m.expectOffline, id)
					}
				}
				m.expectMu.Unlock()
			}
		}
	}
}

func (m *Module) expectedOffline(deviceID string, now time.Time) bool {
	m.expectMu.Lock()
	defer m.expectMu.Unlock()
	until, ok := m.expectOffline[deviceID]
	return ok && now.Before(until)
}

func (m *Module) planOffline(deviceID string, d time.Duration) {
	m.expectMu.Lock()
	defer m.expectMu.Unlock()
	if len(m.expectOffline) >= 10000 {
		m.expectOffline = map[string]time.Time{}
	}
	m.expectOffline[deviceID] = time.Now().Add(d)
}

func (m *Module) publish(kind, deviceID, zoneID string, data any) {
	m.app.Bus.Publish(core.Event{Topic: core.TopicDevices, Kind: kind, ID: deviceID, ZoneID: zoneID, Data: data})
}
