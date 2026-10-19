package schedule

import (
	"time"

	"opensignage/internal/contracts"
)

// Deployment is what a screen knows from the deployment manifest of its zone.
type Deployment struct {
	ZoneID           string
	Rules            []contracts.Rule
	ClosedPlaylistID string
	Groups           []contracts.ManifestGroup
	// Playlists contained in the deployment: only these can be played.
	Playlists map[string]bool
}

// Screen is the state of one screen of the zone.
type Screen struct {
	GroupID     string // display group of the screen ("" = none)
	ZoneClosed  bool
	ClosedSince time.Time // when the zone was closed (zero = unknown)
}

// Evaluate resolves what the screen shows at now, exactly as the players do (the agent and the
// zone preview of the server use this function).
func (d Deployment) Evaluate(s Screen, now time.Time, loc *time.Location) Result {
	groupClosed := ""
	for _, g := range d.Groups {
		if g.ID == s.GroupID {
			groupClosed = g.ClosedPlaylistID
		}
	}
	return Evaluate(Input{
		Rules:                 d.Rules,
		ZoneID:                d.ZoneID,
		GroupID:               s.GroupID,
		ZoneClosed:            s.ZoneClosed,
		ClosedPlaylistID:      d.ClosedPlaylistID,
		GroupClosedPlaylistID: groupClosed,
		ClosedSince:           s.ClosedSince,
		Now:                   now,
		Location:              loc,
		PlaylistAvailable:     func(id string) bool { return d.Playlists[id] },
	})
}
