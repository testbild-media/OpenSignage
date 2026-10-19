package devices

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"opensignage/internal/common/version"
	"opensignage/internal/contracts"
	"opensignage/internal/schedule"
	"opensignage/internal/server/core"
	"opensignage/internal/server/devices/deploy"
	"opensignage/internal/server/devices/live"
	"opensignage/internal/server/httpx"
)

// previewScreen is one screen of the zone in the preview: a device, or a display group (or
// the zone) without device, with what the schedule plays there.
type previewScreen struct {
	DeviceID  string `json:"deviceId,omitempty"`
	Name      string `json:"name,omitempty"`
	Online    bool   `json:"online"`
	GroupID   string `json:"groupId,omitempty"`
	GroupName string `json:"groupName,omitempty"`
	Position  int    `json:"position"`
	// Width/Height: the screen as the content sees it (after rotation); ScaleMode as configured.
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	ScaleMode string `json:"scaleMode"`
	// Kind: closed | rule | cleared; PlaylistID with the anchor of the synchronous playback.
	Kind       string `json:"kind"`
	PlaylistID string `json:"playlistId,omitempty"`
	RuleID     string `json:"ruleId,omitempty"`
	Since      string `json:"since,omitempty"`
}

type previewRule struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Scope string `json:"scope"`
}

// previewTicker is the ticker of the time schedule over the content (as the players get it).
type previewTicker struct {
	Text     string                   `json:"text"`
	Template contracts.TickerTemplate `json:"template"`
	RuleID   string                   `json:"ruleId"`
}

type previewResult struct {
	ZoneID    string `json:"zoneId"`
	ZoneName  string `json:"zoneName"`
	At        string `json:"at"`
	Timezone  string `json:"timezone"`
	ZoneState string `json:"zoneState"` // the state the preview shows (real or chosen)
	// Manifest is the deployment the screens get (built now, so it contains what is published).
	Manifest    *contracts.Manifest        `json:"manifest"`
	Live        *contracts.LiveState       `json:"live"`
	Prices      map[string]contracts.Money `json:"prices"`
	Ticker      *previewTicker             `json:"ticker"`
	ActiveRules []previewRule              `json:"activeRules"`
	Screens     []previewScreen            `json:"screens"`
}

// zonePreview answers "what do the screens of this zone show" – now or at another moment
// (`at`: RFC 3339, or local date and time "2006-01-02T15:04" in the server's time zone), with
// the real zone state or a chosen one (`state`: OPEN | CLOSED). Every screen is evaluated
// from the deployment manifest exactly as its player does it, so the preview works without
// a single device online.
func (m *Module) zonePreview(w http.ResponseWriter, r *http.Request, p *core.Principal) error {
	ctx := r.Context()
	zoneID := r.PathValue("id")
	if !visible(p, zoneID) {
		return httpx.ErrNotFound
	}
	q := r.URL.Query()
	loc := m.app.Settings.Location()
	at := time.Now()
	if v := q.Get("at"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			t, err = time.ParseInLocation("2006-01-02T15:04", v, loc)
		}
		if err != nil {
			return httpx.Invalid("at", "invalid")
		}
		at = t
	}
	var name, state, changedAt string
	err := m.app.DB.R.QueryRowContext(ctx, `SELECT name, state, COALESCE(state_changed_at, '') FROM zones WHERE id = ?`, zoneID).
		Scan(&name, &state, &changedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	var closedSince time.Time
	if changedAt != "" {
		closedSince, _ = time.Parse(time.RFC3339Nano, changedAt) // unparsable = unknown (midnight)
	}
	switch v := q.Get("state"); v {
	case "":
	case contracts.ZoneOpen, contracts.ZoneClosed:
		if v != state {
			state, closedSince = v, at // closed from the previewed moment on
		}
	default:
		return httpx.Invalid("state", "invalid_value")
	}

	man, err := m.builder.Build(ctx, zoneID, m.app.Settings.Get().Timezone, version.Version)
	if errors.Is(err, deploy.ErrZoneNotFound) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	states, err := live.Compute(ctx, m.app.DB.R, []string{zoneID}, time.Now())
	if err != nil {
		return err
	}
	st := states[zoneID]
	if st == nil {
		return httpx.ErrNotFound
	}
	st.Zone.State = state

	d := schedule.Deployment{ZoneID: zoneID, Rules: man.Rules, ClosedPlaylistID: man.ClosedPlaylistID, Groups: man.Groups,
		Playlists: map[string]bool{}}
	tracks := map[string]int{} // playlist id -> screens it is made for (tracks)
	for _, raw := range man.Playlists {
		var pl struct {
			ID      string `json:"id"`
			Screens int    `json:"screens"`
		}
		if json.Unmarshal(raw, &pl) == nil && pl.ID != "" {
			d.Playlists[pl.ID] = true
			tracks[pl.ID] = pl.Screens
		}
	}
	closed := state == contracts.ZoneClosed
	eval := func(groupID string) schedule.Result {
		return d.Evaluate(schedule.Screen{GroupID: groupID, ZoneClosed: closed, ClosedSince: closedSince}, at, loc)
	}
	top := eval("")
	out := previewResult{ZoneID: zoneID, ZoneName: name, At: at.In(loc).Format(time.RFC3339), Timezone: loc.String(),
		ZoneState: state, Manifest: man, Live: st, Prices: top.Prices, ActiveRules: []previewRule{}, Screens: []previewScreen{}}
	if out.Prices == nil {
		out.Prices = map[string]contracts.Money{}
	}
	rules := map[string]contracts.Rule{}
	for _, ru := range man.Rules {
		rules[ru.ID] = ru
	}
	for _, id := range top.ActiveRuleIDs {
		out.ActiveRules = append(out.ActiveRules, previewRule{ID: id, Name: rules[id].Name, Scope: rules[id].Scope})
	}
	if t := top.Ticker; t != nil {
		tpl := contracts.DefaultTickerTemplate()
		if t.Template != nil {
			tpl = *t.Template
		}
		out.Ticker = &previewTicker{Text: t.Text, Template: tpl, RuleID: top.TickerRuleID}
	}

	screen := func(s previewScreen) previewScreen {
		res := eval(s.GroupID)
		s.Kind, s.PlaylistID, s.RuleID = res.Kind, res.PlaylistID, res.RuleID
		if res.Since != nil {
			s.Since = res.Since.In(loc).Format(time.RFC3339Nano)
		}
		if s.Width <= 0 || s.Height <= 0 {
			s.Width, s.Height = 1920, 1080
		}
		if s.ScaleMode == "" {
			s.ScaleMode = "contain"
		}
		if s.Position <= 0 {
			s.Position = 1
		}
		return s
	}
	rows, err := m.app.DB.R.QueryContext(ctx, deviceSelect+` WHERE d.zone_id = ?
		ORDER BY g.id IS NULL, g.sort, g.name COLLATE NOCASE, d.position, d.name COLLATE NOCASE, d.id`, zoneID)
	if err != nil {
		return err
	}
	devs, err := scanDevices(rows)
	if err != nil {
		return err
	}
	withDevices := map[string]bool{}
	for _, dv := range devs {
		w, h := screenSize(dv)
		out.Screens = append(out.Screens, screen(previewScreen{DeviceID: dv.ID, Name: dv.Name, Online: m.hub.get(dv.ID) != nil,
			GroupID: dv.GroupID, GroupName: dv.GroupName, Position: dv.Position, Width: w, Height: h, ScaleMode: dv.Config.ScaleMode}))
		withDevices[dv.GroupID] = true
	}
	// Places without a device are shown as well: every display group without device (and, in a
	// zone without any device, the screens without group) with one screen per track of its
	// playlist, so a menu wall can be checked before its players are set up.
	virtual := func(groupID, groupName string) {
		first := screen(previewScreen{GroupID: groupID, GroupName: groupName})
		out.Screens = append(out.Screens, first)
		for pos := 2; pos <= tracks[first.PlaylistID] && pos <= contracts.MaxPlaylistScreens; pos++ {
			out.Screens = append(out.Screens, screen(previewScreen{GroupID: groupID, GroupName: groupName, Position: pos}))
		}
	}
	for _, g := range man.Groups {
		if !withDevices[g.ID] {
			virtual(g.ID, g.Name)
		}
	}
	if len(devs) == 0 {
		virtual("", "")
	}
	return httpx.OK(w, out)
}

// screenSize is the screen as the content sees it: the size the device reported, else the
// configured display mode (rotated), else 0 (unknown).
func screenSize(d *deviceRow) (int, int) {
	var st contracts.DeviceStatus
	if len(d.LastStatus) > 0 && json.Unmarshal(d.LastStatus, &st) == nil && st.Display != nil && st.Display.Width > 0 && st.Display.Height > 0 {
		return st.Display.Width, st.Display.Height
	}
	c := d.Config.Display
	if c.Width > 0 && c.Height > 0 {
		if c.Rotation == 90 || c.Rotation == 270 {
			return c.Height, c.Width
		}
		return c.Width, c.Height
	}
	return 0, 0
}
