package devices

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"opensignage/internal/common/version"
	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
	"opensignage/internal/server/devices/deploy"
	"opensignage/internal/server/devices/live"
	"opensignage/internal/server/httpx"
)

// invalidErr marks a message the device got wrong (counted per connection), as opposed
// to a server-side failure (logged, never held against the device).
type invalidErr struct{ reason string }

func (e *invalidErr) Error() string { return e.reason }

func invalid(format string, a ...any) error { return &invalidErr{reason: fmt.Sprintf(format, a...)} }

// serveWS handles GET /device/v1/ws.
func (m *Module) serveWS(w http.ResponseWriter, r *http.Request) {
	accepted := false
	defer func() {
		if p := recover(); p != nil {
			if p == http.ErrAbortHandler {
				panic(p)
			}
			m.log.Error("device link: panic before handshake", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			if !accepted {
				writeErr(w, r, errInternal)
			}
		}
	}()
	tok := bearerToken(r)
	if tok == "" {
		writeErr(w, r, errNoToken)
		return
	}
	dev, err := m.deviceByToken(r.Context(), tok)
	if err != nil {
		writeErr(w, r, errInternal.Wrap(err))
		return
	}
	// Device clients are not browsers: auth is the bearer token, no cookies, so the
	// origin check does not apply.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		m.log.Debug("device link: upgrade failed", "remote", httpx.ClientIP(r), "err", err)
		return
	}
	accepted = true
	if dev == nil {
		m.log.Info("device link: rejected revoked or unknown token", "remote", httpx.ClientIP(r))
		b, _ := encode(contracts.MsgRevoked, "", struct{}{})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = ws.Write(ctx, websocket.MessageText, b)
		cancel()
		_ = ws.Close(contracts.CloseRevoked, "revoked")
		return
	}
	ws.SetReadLimit(maxMessageSize)
	c := newConn(m, ws, dev, httpx.ClientIP(r))
	c.serve()
}

// serve runs one connection: hello, handshake, then reader/writer/heartbeat until the
// connection ends. Panics end only this connection.
func (c *conn) serve() {
	m := c.m
	defer func() {
		if p := recover(); p != nil {
			m.log.Error("device link: connection crashed", "device", c.deviceID, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			c.closeWith(websocket.StatusInternalError, "internal error", nil)
		}
		c.cleanup()
	}()

	timer := time.AfterFunc(m.HelloTimeout, func() {
		c.closeWith(websocket.StatusPolicyViolation, "hello timeout", nil)
	})
	typ, data, err := c.ws.Read(c.ctx)
	if !timer.Stop() {
		return // the hello timeout already fired and is closing the connection
	}
	if err != nil {
		m.log.Debug("device link: no hello", "device", c.deviceID, "err", err)
		return
	}
	hello, err := parseHello(typ, data)
	if err != nil {
		m.log.Warn("device link: invalid hello", "device", c.deviceID, "err", err)
		c.closeWith(websocket.StatusPolicyViolation, "hello expected", nil)
		return
	}
	if hello.HardwareID != "" && hello.HardwareID != c.hardwareID {
		m.log.Warn("device link: hardware id differs from the paired one", "device", c.deviceID,
			"paired", c.hardwareID, "reported", truncate(hello.HardwareID, 128))
	}
	c.versionMatch = hello.Version == version.Version
	if !c.versionMatch {
		m.log.Warn("device link: version mismatch, deployments withheld", "device", c.deviceID,
			"agent", hello.Version, "server", version.Version)
	}
	if err := m.storeHello(c.ctx, c.deviceID, hello); err != nil {
		m.log.Error("device link: storing hello", "device", c.deviceID, "err", err)
	}

	old, ok := m.hub.register(c)
	if !ok {
		c.closeWith(websocket.StatusGoingAway, "server shutting down", nil)
		return
	}
	if old != nil {
		m.log.Info("device link: replacing older connection", "device", c.deviceID)
		old.closeWith(contracts.CloseReplaced, "replaced by a newer connection", nil)
	}

	welcome, err := encode(contracts.MsgWelcome, "", contracts.Welcome{DeviceID: c.deviceID, ServerVersion: version.Version,
		Protocol: version.Protocol, ServerTime: time.Now().UTC().Format(time.RFC3339Nano), VersionMatch: c.versionMatch})
	if err != nil {
		panic(err)
	}
	if !c.write(welcome) {
		return
	}
	// Full snapshot on every connection: config, live state, deployment target (the
	// cleared target is re-confirmed every time).
	m.syncConn(c.ctx, c, true)

	m.endAcks(c.ctx, c.deviceID, []string{AlertOffline}, func(string) bool { return false }) // back online
	m.publish(EvOnline, c.deviceID, c.zone(), map[string]any{"version": hello.Version, "versionMatch": c.versionMatch})
	m.log.Info("device link: connected", "device", c.deviceID, "remote", c.remote, "version", hello.Version, "versionMatch", c.versionMatch)

	go c.writeLoop()
	go c.heartbeat()
	c.readLoop()
}

func (c *conn) cleanup() {
	started := c.closeStarted.Load()
	c.closing.Store(true) // nothing more is queued for this link
	c.cancel()
	current := c.registered && c.m.hub.unregister(c)
	if started {
		select {
		case <-c.closeDone:
		case <-time.After(20 * time.Second):
		}
	}
	_ = c.ws.CloseNow()
	if current {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := c.m.app.DB.W.ExecContext(ctx, `UPDATE devices SET last_seen_at = ? WHERE id = ?`, ts(time.Now()), c.deviceID); err != nil {
			c.m.log.Error("device link: storing last seen", "device", c.deviceID, "err", err)
		}
		c.m.publish(EvOffline, c.deviceID, c.zone(), nil)
		c.m.log.Info("device link: disconnected", "device", c.deviceID)
	}
}

// write sends one message directly (bounded by WriteTimeout).
func (c *conn) write(b []byte) bool {
	if c.closing.Load() {
		return false
	}
	ctx, cancel := context.WithTimeout(c.ctx, c.m.WriteTimeout)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		c.m.log.Debug("device link: write failed", "device", c.deviceID, "err", err)
		return false
	}
	return true
}

func (c *conn) writeLoop() {
	defer func() {
		if p := recover(); p != nil {
			c.m.log.Error("device link: writer crashed", "device", c.deviceID, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			c.closeWith(websocket.StatusInternalError, "internal error", nil)
		}
	}()
	for {
		for k := 0; k < numSlots; k++ {
			if b := c.take(k); b != nil && !c.write(b) {
				return
			}
		}
		select {
		case <-c.ctx.Done():
			return
		case b := <-c.queue:
			if !c.write(b) {
				return
			}
		case <-c.wake:
		}
	}
}

// heartbeat pings every PingInterval; three missed answers in a row (and no other
// traffic) mark the connection dead.
func (c *conn) heartbeat() {
	defer func() {
		if p := recover(); p != nil {
			c.m.log.Error("device link: heartbeat crashed", "device", c.deviceID, "panic", fmt.Sprint(p))
		}
	}()
	t := time.NewTicker(c.m.PingInterval)
	defer t.Stop()
	missed := 0
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(c.ctx, c.m.PingInterval)
		err := c.ws.Ping(ctx)
		cancel()
		if c.ctx.Err() != nil {
			return
		}
		if err == nil || time.Since(time.Unix(0, c.lastRead.Load())) < c.m.PingInterval {
			missed = 0
			continue
		}
		missed++
		if missed >= 3 {
			c.m.log.Warn("device link: heartbeat lost, dropping connection", "device", c.deviceID)
			_ = c.ws.CloseNow()
			return
		}
	}
}

func (c *conn) readLoop() {
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			if st := websocket.CloseStatus(err); st != -1 {
				c.m.log.Debug("device link: closed", "device", c.deviceID, "status", int(st))
			} else {
				c.m.log.Debug("device link: read ended", "device", c.deviceID, "err", err)
			}
			return
		}
		now := time.Now()
		c.lastRead.Store(now.UnixNano())
		if !c.allow(now) {
			c.invalidMsg("rate limit exceeded")
			continue
		}
		if typ != websocket.MessageText {
			c.invalidMsg("binary message")
			continue
		}
		var env contracts.LinkMessage
		if err := json.Unmarshal(data, &env); err != nil || env.T == "" {
			c.invalidMsg("malformed envelope")
			continue
		}
		c.dispatch(env)
	}
}

func (c *conn) dispatch(env contracts.LinkMessage) {
	defer func() {
		if p := recover(); p != nil {
			c.m.log.Error("device link: handler crashed", "device", c.deviceID, "type", truncate(env.T, 64),
				"panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			c.invalidMsg("handler panic")
		}
	}()
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	var err error
	switch env.T {
	case contracts.MsgHello:
		var h contracts.Hello
		if h, err = parseHelloPayload(env.P); err == nil {
			err = c.m.storeHello(ctx, c.deviceID, h)
		}
	case contracts.MsgStatus:
		err = c.m.onStatus(ctx, c, env.P)
	case contracts.MsgDeployProgress:
		err = c.m.onDeployProgress(ctx, c, env.P)
	case contracts.MsgActionResult:
		err = c.m.onActionResult(c, env.P)
	case contracts.MsgUpdateProgress:
		err = c.m.onUpdateProgress(c, env.P)
	case contracts.MsgTimePing:
		err = c.onTimePing(env.P)
	default:
		c.logUnknown(env.T)
		return
	}
	var ie *invalidErr
	switch {
	case err == nil:
	case errors.As(err, &ie):
		c.invalidMsg(env.T + ": " + ie.reason)
	default:
		c.m.log.Error("device link: handling message", "device", c.deviceID, "type", env.T, "err", err)
	}
}

// ---------------------------------------------------------------- message handlers

// onTimePing answers at once with the server time (the agent measures its clock offset
// from the round trip; the pong skips the slots of large messages).
func (c *conn) onTimePing(p json.RawMessage) error {
	var ping contracts.TimePing
	if err := json.Unmarshal(p, &ping); err != nil {
		return &invalidErr{reason: "invalid time.ping"}
	}
	b, err := encode(contracts.MsgTimePong, "", contracts.TimePong{Seq: ping.Seq, T0: ping.T0, Server: time.Now().UnixMicro()})
	if err != nil {
		return err
	}
	c.enqueue(b)
	return nil
}

func parseHello(typ websocket.MessageType, data []byte) (contracts.Hello, error) {
	if typ != websocket.MessageText {
		return contracts.Hello{}, invalid("binary hello")
	}
	var env contracts.LinkMessage
	if err := json.Unmarshal(data, &env); err != nil {
		return contracts.Hello{}, invalid("malformed envelope")
	}
	if env.T != contracts.MsgHello {
		return contracts.Hello{}, invalid("first message must be hello, got %q", truncate(env.T, 64))
	}
	return parseHelloPayload(env.P)
}

func parseHelloPayload(p json.RawMessage) (contracts.Hello, error) {
	var h contracts.Hello
	if err := json.Unmarshal(p, &h); err != nil {
		return h, invalid("malformed hello")
	}
	switch {
	case h.Version == "" || len(h.Version) > 64:
		return h, invalid("hello.version")
	case len(h.HardwareID) > 128:
		return h, invalid("hello.hardwareId")
	case len(h.ActiveDeploymentID) > 64 || len(h.PreviousDeploymentID) > 64:
		return h, invalid("hello.activeDeploymentId")
	case len(h.ActiveSource.PlaylistID) > 64 || len(h.ActiveSource.RuleID) > 64:
		return h, invalid("hello.activeSource")
	}
	switch h.ActiveSource.Kind {
	case "", "playlist", "closed", "cleared":
	default:
		return h, invalid("hello.activeSource.kind")
	}
	ips, err := cleanIPs(h.IPs)
	if err != nil {
		return h, err
	}
	h.IPs = ips
	return h, nil
}

func cleanIPs(in []string) ([]string, error) {
	if len(in) > 32 {
		return nil, invalid("too many ips")
	}
	out := make([]string, 0, len(in))
	for _, ip := range in {
		if ip == "" || len(ip) > 64 || !utf8.ValidString(ip) {
			return nil, invalid("ip")
		}
		out = append(out, ip)
	}
	return out, nil
}

// storeHello records what the agent really runs and shows. When the active deployment
// changes, the old one becomes previous (unless the agent names its previous itself).
func (m *Module) storeHello(ctx context.Context, deviceID string, h contracts.Hello) error {
	m.since.observe(deviceID, AlertVersionMismatch, !versionMatches(h.Version), time.Now())
	ips, _ := json.Marshal(h.IPs)
	src, _ := json.Marshal(h.ActiveSource)
	return m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := setActiveDeployment(ctx, tx, deviceID, h.ActiveDeploymentID, h.PreviousDeploymentID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE devices SET version = ?, ips = ?, active_source = ?, last_seen_at = ? WHERE id = ?`,
			h.Version, string(ips), string(src), ts(time.Now()), deviceID)
		return err
	})
}

func setActiveDeployment(ctx context.Context, tx *sql.Tx, deviceID, active, previous string) error {
	var curActive, curPrev string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(active_deployment_id, ''), COALESCE(previous_deployment_id, '') FROM devices WHERE id = ?`, deviceID).
		Scan(&curActive, &curPrev)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	newPrev := curPrev
	switch {
	case previous != "":
		newPrev = previous
	case curActive != "" && curActive != active:
		newPrev = curActive
	}
	_, err = tx.ExecContext(ctx, `UPDATE devices SET active_deployment_id = NULLIF(?, ''), previous_deployment_id = NULLIF(?, '') WHERE id = ?`,
		active, newPrev, deviceID)
	return err
}

const maxStatusBytes = 64 << 10

func (m *Module) onStatus(ctx context.Context, c *conn, p json.RawMessage) error {
	if len(p) == 0 || len(p) > maxStatusBytes {
		return invalid("status size")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(p, &obj); err != nil || obj == nil {
		return invalid("status must be an object")
	}
	var st contracts.DeviceStatus
	if err := json.Unmarshal(p, &st); err != nil {
		return invalid("status fields")
	}
	compact, err := contracts.CanonicalizeJSON(p)
	if err != nil {
		return invalid("status json")
	}
	set, args := "last_status = ?, last_seen_at = ?", []any{string(compact), ts(time.Now())}
	if st.IPs != nil {
		ips, err := cleanIPs(st.IPs)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(ips)
		set, args = set+", ips = ?", append(args, string(b))
	}
	if st.Version != "" {
		if len(st.Version) > 64 {
			return invalid("status.version")
		}
		set, args = set+", version = ?", append(args, st.Version)
	}
	if src := st.ActiveSource; src != nil {
		// The status is sent whenever the active source changes: keep it current between
		// hellos (the admin sees which playlist is on screen).
		switch src.Kind {
		case "", "playlist", "closed", "cleared":
		default:
			return invalid("status.activeSource.kind")
		}
		if len(src.PlaylistID) > 64 || len(src.RuleID) > 64 {
			return invalid("status.activeSource")
		}
		if src.Kind != "" {
			b, _ := json.Marshal(src)
			set, args = set+", active_source = ?", append(args, string(b))
		}
	}
	if a := st.Applied; a != nil && len(a.DisplayError) > 2000 {
		return invalid("status.applied.displayError")
	}
	if len(st.Caps) > 32 {
		return invalid("status.caps")
	}
	if _, err := m.app.DB.W.ExecContext(ctx, `UPDATE devices SET `+set+` WHERE id = ?`, append(args, c.deviceID)...); err != nil {
		return err
	}
	m.observeStatus(ctx, c.deviceID, st, time.Now())
	m.publish(EvStatus, c.deviceID, c.zone(), nil)
	return nil
}

func (m *Module) onDeployProgress(ctx context.Context, c *conn, p json.RawMessage) error {
	var dp contracts.DeployProgress
	if err := json.Unmarshal(p, &dp); err != nil {
		return invalid("malformed deploy.progress")
	}
	switch dp.State {
	case "preparing", "ready", "active", "failed":
	default:
		return invalid("deploy.progress.state")
	}
	if dp.ID == "" || len(dp.ID) > 64 || dp.Done < 0 || dp.Total < 0 || len(dp.Error) > 2000 {
		return invalid("deploy.progress fields")
	}
	var exists int
	if err := m.app.DB.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments WHERE id = ?`, dp.ID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		m.log.Debug("device link: progress for unknown deployment ignored", "device", c.deviceID, "deployment", dp.ID)
		return nil
	}
	err := m.app.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO device_deployments(device_id, deployment_id, state, done, total, error, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(device_id, deployment_id) DO UPDATE SET state = excluded.state, done = excluded.done,
				total = excluded.total, error = excluded.error, updated_at = excluded.updated_at`,
			c.deviceID, dp.ID, dp.State, dp.Done, dp.Total, dp.Error, ts(time.Now())); err != nil {
			return err
		}
		// Bounded history per device.
		if _, err := tx.ExecContext(ctx, `DELETE FROM device_deployments WHERE device_id = ? AND deployment_id NOT IN (
			SELECT deployment_id FROM device_deployments WHERE device_id = ? ORDER BY updated_at DESC LIMIT 20)`, c.deviceID, c.deviceID); err != nil {
			return err
		}
		if dp.State == "active" {
			return setActiveDeployment(ctx, tx, c.deviceID, dp.ID, "")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if dp.State != "failed" { // the newest progress decides about sync_failed
		m.endAcks(ctx, c.deviceID, []string{AlertSyncFailed}, func(string) bool { return false })
	}
	m.publish(EvProgress, c.deviceID, c.zone(), dp)
	return nil
}

func (m *Module) onActionResult(c *conn, p json.RawMessage) error {
	var ar contracts.ActionResult
	if err := json.Unmarshal(p, &ar); err != nil || ar.ID == "" {
		return invalid("malformed action.result")
	}
	if len(ar.Data) > maxStatusBytes || len(ar.Error) > 2000 {
		return invalid("action.result too large")
	}
	a, ok := m.actions.result(c.deviceID, ar)
	if !ok {
		m.log.Debug("device link: result for unknown or finished action", "device", c.deviceID, "action", truncate(ar.ID, 64))
		return nil
	}
	m.publish(EvAction, c.deviceID, c.zone(), map[string]any{"requestId": a.ID, "state": a.State})
	return nil
}

func (m *Module) onUpdateProgress(c *conn, p json.RawMessage) error {
	var up contracts.UpdateProgress
	if err := json.Unmarshal(p, &up); err != nil {
		return invalid("malformed update.progress")
	}
	if up.Version == "" || len(up.Version) > 64 || up.State == "" || len(up.State) > 32 || len(up.Error) > 2000 {
		return invalid("update.progress fields")
	}
	if l := m.updates.Load(); l != nil {
		(*l).UpdateProgress(c.deviceID, up)
	}
	m.app.Bus.Publish(core.Event{Topic: core.TopicUpdates, Kind: "device_progress", ID: c.deviceID, ZoneID: c.zone(), Data: up})
	m.publish(EvUpdateReport, c.deviceID, c.zone(), up)
	return nil
}

// ---------------------------------------------------------------- snapshots

// syncConn re-reads the device and offers its config; with force (handshake, explicit
// sync) or after a zone change it also offers the live state and deployment target.
func (m *Module) syncConn(ctx context.Context, c *conn, force bool) {
	seq := m.seq.Add(1)
	rows, err := m.loadDevicesByID(ctx, []string{c.deviceID})
	if err != nil {
		m.log.Error("device link: loading device", "device", c.deviceID, "err", err)
		return
	}
	m.applyRow(ctx, c, rows[c.deviceID], seq, force)
}

// resyncAll re-reads every connected device in one query (zone deleted, settings
// changed, device changed elsewhere).
func (m *Module) resyncAll(ctx context.Context) {
	conns := m.hub.snapshot()
	if len(conns) == 0 {
		return
	}
	seq := m.seq.Add(1)
	ids := make([]string, len(conns))
	for i, c := range conns {
		ids[i] = c.deviceID
	}
	rows, err := m.loadDevicesByID(ctx, ids)
	if err != nil {
		m.log.Error("device link: resync", "err", err)
		return
	}
	for _, c := range conns {
		m.applyRow(ctx, c, rows[c.deviceID], seq, false)
	}
}

func (m *Module) applyRow(ctx context.Context, c *conn, row *deviceRow, seq uint64, force bool) {
	if row == nil || !row.Paired || row.tokenHash != c.tokenHash {
		m.log.Info("device link: device deleted, unpaired or re-paired, revoking connection", "device", c.deviceID)
		c.revoke()
		return
	}
	zoneChanged := c.setZone(row.ZoneID)
	b, err := encode(contracts.MsgDeviceConfig, "", m.configMessage(row))
	if err != nil {
		m.log.Error("device link: encode config", "err", err)
		return
	}
	c.offer(slotConfig, seq, b, !force)
	if force || zoneChanged {
		m.pushLive(ctx, c)
		m.pushDeploy(ctx, c, !force)
	}
}

func (m *Module) configMessage(row *deviceRow) contracts.DeviceConfig {
	cfg := row.Config
	settings := m.app.Settings.Get()
	repo := settings.UpdateRepo
	if repo == "" {
		repo = m.app.Cfg.UpdateRepo
	}
	return contracts.DeviceConfig{Name: row.Name, ZoneID: row.ZoneID, GroupID: row.GroupID, Position: row.Position, ClockKey: row.ClockKey,
		ScaleMode: cfg.ScaleMode, Blackout: cfg.Blackout,
		Display: cfg.Display, Perf: cfg.Perf, HWVideo: cfg.HWVideo, PlasmaShift: cfg.PlasmaShift, ShowIPOverlay: cfg.ShowIPOverlay,
		WebGUIPasswordHash: settings.ClientWebPassword, Language: settings.Language,
		ServerVersion: version.Version, UpdateRepo: repo}
}

// pushLive offers the current live state of the connection's zone.
func (m *Module) pushLive(ctx context.Context, c *conn) {
	seq := m.seq.Add(1)
	zone := c.zone()
	now := time.Now()
	st := live.Empty(now)
	if zone != "" {
		states, err := live.Compute(ctx, m.app.DB.R, []string{zone}, now)
		if err != nil {
			m.log.Error("live: computing state", "zone", zone, "err", err)
			return
		}
		if s := states[zone]; s != nil {
			st = s
		}
	}
	b, err := encode(contracts.MsgLiveState, "", st)
	if err != nil {
		m.log.Error("live: encode", "err", err)
		return
	}
	c.offer(slotLive, seq, b, false)
}

// pushDeploy offers the zone's deployment target (never to version-mismatched agents).
func (m *Module) pushDeploy(ctx context.Context, c *conn, onlyIfChanged bool) {
	if !c.versionMatch {
		return
	}
	seq := m.seq.Add(1)
	t, err := deploy.CurrentTarget(ctx, m.app.DB.R, c.zone())
	if err != nil {
		m.log.Error("deploy: reading target", "device", c.deviceID, "err", err)
		return
	}
	b, err := encodeTarget(t)
	if err != nil {
		m.log.Error("deploy: encode target", "err", err)
		return
	}
	c.offer(slotDeploy, seq, b, onlyIfChanged)
}

func encodeTarget(t deploy.Target) ([]byte, error) {
	if t.Kind == deploy.KindDeployment {
		return encode(contracts.MsgDeployAvailable, "", contracts.DeployAvailable{ID: t.ID, Hash: t.Hash})
	}
	return encode(contracts.MsgDeployCleared, "", contracts.DeployCleared{Reason: t.Reason})
}

// announce offers a zone's new target to its connected, version-matching devices.
func (m *Module) announce(zoneID string, t deploy.Target) {
	seq := m.seq.Add(1)
	b, err := encodeTarget(t)
	if err != nil {
		m.log.Error("deploy: encode target", "err", err)
		return
	}
	for _, c := range m.hub.snapshot() {
		if c.versionMatch && c.zone() == zoneID {
			c.offer(slotDeploy, seq, b, true)
		}
	}
}

// pushLiveZones computes the live state of the given zones (nil = all) in one batch and
// offers it to every connected device of those zones, then tells admin UIs. An error means
// nothing was pushed (the caller retries).
func (m *Module) pushLiveZones(ctx context.Context, zones []string) error {
	seq := m.seq.Add(1)
	now := time.Now()
	states, err := live.Compute(ctx, m.app.DB.R, zones, now)
	if err != nil {
		m.log.Error("live: computing states", "err", err)
		return err
	}
	want := map[string]bool{}
	for _, z := range zones {
		want[z] = true
	}
	encoded := map[string][]byte{}
	var gone []*conn
	for _, c := range m.hub.snapshot() {
		z := c.zone()
		if z == "" || (zones != nil && !want[z]) {
			continue
		}
		st := states[z]
		if st == nil {
			gone = append(gone, c) // zone deleted meanwhile: re-read the device
			continue
		}
		b, ok := encoded[z]
		if !ok {
			if b, err = encode(contracts.MsgLiveState, "", st); err != nil {
				m.log.Error("live: encode", "err", err)
				continue
			}
			encoded[z] = b
		}
		c.offer(slotLive, seq, b, false)
	}
	for _, c := range gone {
		m.syncConn(ctx, c, false)
	}
	for z := range states {
		m.app.Bus.Publish(core.Event{Topic: core.TopicLive, Kind: "pushed", ZoneID: z})
	}
	return nil
}

// Retry bounds for a failed live push or deployment rebuild (e.g. a transient database
// error): the work is tried again after DeployRetry, doubling up to maxRetryDelay, at most
// maxRetries times in a row; the next change event starts over.
const (
	maxRetries    = 6
	maxRetryDelay = time.Minute
)

// retryDelay is the wait before retry number n (0-based).
func (m *Module) retryDelay(n int) time.Duration {
	d := m.DeployRetry
	if d <= 0 {
		d = time.Second
	}
	for i := 0; i < n && d < maxRetryDelay; i++ {
		d *= 2
	}
	return min(d, maxRetryDelay)
}

// ---------------------------------------------------------------- loops

// liveLoop reacts to live/product/zone/device/settings events, debounced by
// LiveDebounce (the timer starts at the first event, so pushes are never starved). A push
// that fails keeps its pending zones and is retried with backoff.
func (m *Module) liveLoop(ctx context.Context) {
	sub := m.app.Bus.Subscribe(512)
	defer sub.Close()
	var timerC <-chan time.Time
	all, resync := false, false
	dirty := map[string]bool{}
	retries := 0
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-sub.C:
			if sub.TakeOverflow() {
				all, resync = true, true
			}
			switch ev.Topic {
			case core.TopicLive:
				// Our own "pushed" notifications for admin UIs must not trigger a push.
				if ev.Kind != "pushed" {
					if ev.ZoneID == "" || len(dirty) >= 1000 {
						all = true
					} else {
						dirty[ev.ZoneID] = true
					}
				}
			case core.TopicProducts, core.TopicContent:
				all = true
			case core.TopicZones:
				all, resync = true, true
			case core.TopicSettings:
				resync = true
			case core.TopicDevices:
				if !telemetryKinds[ev.Kind] {
					resync = true
				}
			}
			if timerC == nil && (all || resync || len(dirty) > 0) {
				timerC = time.After(m.LiveDebounce)
			}
		case <-timerC:
			timerC = nil
			if resync {
				m.resyncAll(ctx)
				resync = false
			}
			var err error
			switch {
			case all:
				err = m.pushLiveZones(ctx, nil)
			case len(dirty) > 0:
				zones := make([]string, 0, len(dirty))
				for z := range dirty {
					zones = append(zones, z)
				}
				err = m.pushLiveZones(ctx, zones)
			}
			if err != nil && ctx.Err() == nil && retries < maxRetries {
				// Keep all/dirty: the same zones are pushed again (later events merge in).
				d := m.retryDelay(retries)
				retries++
				m.log.Warn("live: push failed, retrying", "in", d, "attempt", retries)
				timerC = time.After(d)
				continue
			}
			if err != nil {
				m.log.Error("live: push failed repeatedly, waiting for the next change", "err", err)
			}
			retries = 0
			all = false
			dirty = map[string]bool{}
		}
	}
}

// deployLoop rebuilds all zone deployments at start and after content, zone, product
// or settings changes (debounced by DeployDebounce). A failed rebuild (e.g. a transient
// database error) is retried with backoff instead of waiting for the next change.
func (m *Module) deployLoop(ctx context.Context) {
	sub := m.app.Bus.Subscribe(256)
	defer sub.Close()
	var timerC <-chan time.Time
	retries := 0
	failed := func(err error) {
		if retries >= maxRetries {
			m.log.Error("deploy: rebuild failed repeatedly, waiting for the next change", "err", err)
			retries = 0
			return
		}
		d := m.retryDelay(retries)
		retries++
		m.log.Error("deploy: rebuild failed, retrying", "err", err, "in", d, "attempt", retries)
		timerC = time.After(d)
	}
	if err := m.RebuildNow(ctx); err != nil && ctx.Err() == nil {
		failed(err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-sub.C:
			pending := sub.TakeOverflow()
			switch ev.Topic {
			case core.TopicContent, core.TopicZones, core.TopicProducts, core.TopicSettings:
				pending = true
			}
			if pending && timerC == nil {
				timerC = time.After(m.DeployDebounce)
			}
		case <-timerC:
			timerC = nil
			if err := m.RebuildNow(ctx); err != nil && ctx.Err() == nil {
				failed(err)
			} else {
				retries = 0
			}
		}
	}
}

// RebuildNow builds every zone, stores changed deployments, announces new targets and
// prunes old deployments. It returns the first error (all zones are still attempted).
func (m *Module) RebuildNow(ctx context.Context) error {
	m.buildMu.Lock()
	defer m.buildMu.Unlock()
	rows, err := m.app.DB.R.QueryContext(ctx, `SELECT id FROM zones ORDER BY id`)
	if err != nil {
		return err
	}
	var zones []string
	for rows.Next() {
		var z string
		if err := rows.Scan(&z); err != nil {
			rows.Close()
			return err
		}
		zones = append(zones, z)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	tz := m.app.Settings.Get().Timezone
	var firstErr error
	keep := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	for _, z := range zones {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		man, err := m.builder.Build(ctx, z, tz, version.Version)
		if errors.Is(err, deploy.ErrZoneNotFound) {
			continue
		}
		if err != nil {
			m.log.Error("deploy: build failed", "zone", z, "err", err)
			keep(err)
			continue
		}
		t, changed, err := m.builder.Apply(ctx, z, man)
		if err != nil {
			m.log.Error("deploy: storing deployment failed", "zone", z, "err", err)
			keep(err)
			continue
		}
		if changed {
			m.log.Info("deploy: zone target changed", "zone", z, "kind", t.Kind, "deployment", t.ID)
			m.announce(z, t)
			m.publish(EvDeployment, "", z, t)
		}
	}
	if n, err := m.builder.Prune(ctx, deploy.Keep); err != nil {
		m.log.Error("deploy: pruning", "err", err)
		keep(err)
	} else if n > 0 {
		m.log.Info("deploy: pruned old deployments", "count", n)
	}
	return firstErr
}
