// Package wire holds the agent's view of the device channel (ARCHITECTURE §5.5–5.8,
// §7.1). The structs match the contract field by field; the TypeScript mirror lives in
// web/packages/contracts. Fields the agent only passes through (presentations, the live
// catalogue) stay raw JSON so nothing is lost between server and player.
package wire

import (
	"encoding/json"

	"opensignage/internal/contracts"
)

// Envelope wraps every device link and player link message: { "t", "id"?, "p" }.
type Envelope struct {
	T  string          `json:"t"`
	ID string          `json:"id,omitempty"`
	P  json.RawMessage `json:"p,omitempty"`
}

// Message types of the device link (§5.7).
const (
	// Server → agent.
	TWelcome         = "welcome"
	TDeviceConfig    = "device.config"
	TLiveState       = "live.state"
	TDeployAvailable = "deploy.available"
	TDeployCleared   = "deploy.cleared"
	TAction          = "action"
	TUpdateOffer     = "update.offer"
	TRevoked         = "revoked"
	TTimePong        = "time.pong"

	// Agent → server.
	THello          = "hello"
	TStatus         = "status"
	TDeployProgress = "deploy.progress"
	TActionResult   = "action.result"
	TUpdateProgress = "update.progress"
	TTimePing       = "time.ping"
)

// TimePing asks the server for its time (clock measurement).
type TimePing struct {
	Seq uint64 `json:"seq"`
	T0  int64  `json:"t0"`
}

// TimePong is the server's answer: its time (unix µs) when it handled the ping.
type TimePong struct {
	Seq    uint64 `json:"seq"`
	T0     int64  `json:"t0"`
	Server int64  `json:"server"`
}

// ClockInfo reports where the agent's time comes from (status).
type ClockInfo struct {
	Source   string  `json:"source"` // server | peer | local
	OffsetMS float64 `json:"offsetMs"`
	RTTMS    float64 `json:"rttMs"`
	Leader   string  `json:"leader,omitempty"` // LAN time leader (hardware id) when following one
	// AgeS: seconds since the last measurement (server or leader).
	AgeS int64 `json:"ageS"`
}

// Message types of the player link (§5.8).
const (
	// Agent → player.
	TMode          = "mode"
	TBundle        = "bundle"
	TSource        = "source"
	TLive          = "live"
	TConfig        = "config"
	TCmd           = "cmd"
	TVersionNotice = "versionNotice"

	// Player → agent.
	TPing = "ping"
	TLog  = "log"
)

// Close codes used by the server on the device WebSocket.
const (
	CloseRevoked = 4401
)

// Marshal builds an envelope with payload p.
func Marshal(t, id string, p any) ([]byte, error) {
	var raw json.RawMessage
	if p != nil {
		b, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return json.Marshal(Envelope{T: t, ID: id, P: raw})
}

// ---------------------------------------------------------------- server → agent

// Welcome is the server's first message after the agent's hello.
type Welcome struct {
	DeviceID      string `json:"deviceId"`
	ServerVersion string `json:"serverVersion"`
	Protocol      int    `json:"protocol"`
	ServerTime    string `json:"serverTime"`
	VersionMatch  bool   `json:"versionMatch"`
}

// Display is the requested output mode. Width/Height 0 = keep the current mode (only the
// rotation is applied).
// Rotation is in degrees clockwise: 0, 90, 180, 270. Custom forces a mode the screen
// does not report (generated with cvt).
type Display struct {
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Refresh  float64 `json:"refresh"`
	Rotation int     `json:"rotation"`
	Custom   bool    `json:"custom"`
}

// IsAuto reports whether no explicit resolution is requested (keep the current mode).
func (d Display) IsAuto() bool { return d.Width <= 0 || d.Height <= 0 }

// DeviceConfig is the per-device configuration managed in the admin WebGUI.
type DeviceConfig struct {
	Name   string `json:"name"`
	ZoneID string `json:"zoneId,omitempty"`
	// GroupID: display group in the zone ("" = none); Position: the screen's place there
	// (it plays track Position of a playlist for several screens).
	GroupID            string  `json:"groupId,omitempty"`
	Position           int     `json:"position,omitempty"`
	ClockKey           string  `json:"clockKey,omitempty"` // LAN time exchange of the zone
	ScaleMode          string  `json:"scaleMode"`          // contain | cover | none
	Blackout           bool    `json:"blackout"`           // screen black on request
	Display            Display `json:"display"`            // output mode
	Perf               string  `json:"perf"`               // auto | low | standard | high
	HWVideo            string  `json:"hwVideo"`            // auto | on | off
	PlasmaShift        bool    `json:"plasmaShift"`        // pixel shift against burn-in
	ShowIPOverlay      bool    `json:"showIpOverlay"`
	WebGUIPasswordHash string  `json:"webguiPasswordHash,omitempty"`
	// Language is the server default UI language (player texts, number and date formats).
	Language string `json:"language,omitempty"`
	// ServerVersion and UpdateRepo let the client WebGUI offer the GitHub fallback for
	// exactly the server's version.
	ServerVersion string `json:"serverVersion,omitempty"`
	UpdateRepo    string `json:"updateRepo,omitempty"`
}

// DeployAvailable announces a new deployment for the device's zone.
type DeployAvailable struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}

// DeployCleared means: show nothing (explicit cleared target).
type DeployCleared struct {
	Reason string `json:"reason,omitempty"` // no_content | no_zone
}

// Action is a remote action; the result goes back as ActionResult with the same id.
type Action struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Remote action names.
const (
	ActionSyncNow       = "sync_now"
	ActionReloadPlayer  = "reload_player"
	ActionRestartClient = "restart_client"
	ActionRebootDevice  = "reboot_device"
	ActionCleanCache    = "clean_cache"
	ActionScreenshot    = "screenshot"
	// ActionRegeneration switches plasma regeneration on or off (RegenerationParams).
	ActionRegeneration = "plasma_regeneration"
)

// RegenerationParams are the parameters of the plasma_regeneration action.
type RegenerationParams struct {
	On *bool `json:"on"`
}

// UpdateOffer asks the agent to install another version. SHA256 and Size are only
// trusted when they match the signed release metadata (UpdateRelease).
type UpdateOffer struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// UpdateRelease is the signed release metadata served by `GET /device/v1/update/release`:
// release.json, SHA256SUMS and SHA256SUMS.sig exactly as published.
type UpdateRelease struct {
	Version string `json:"version"`
	Info    []byte `json:"release"`
	Sums    []byte `json:"sums"`
	Sig     []byte `json:"sig"`
}

// ---------------------------------------------------------------- agent → server

// Source kinds reported as activeSource.
const (
	SourcePlaylist = "playlist"
	SourceClosed   = "closed"
	SourceCleared  = "cleared"
)

// ActiveSource is what the device actually shows right now.
type ActiveSource struct {
	Kind       string `json:"kind"` // playlist | closed | cleared
	PlaylistID string `json:"playlistId,omitempty"`
	RuleID     string `json:"ruleId,omitempty"`
}

// Hello is the agent's first message on every connection.
type Hello struct {
	Version              string       `json:"version"`
	Protocol             int          `json:"protocol"`
	HardwareID           string       `json:"hardwareId"`
	ActiveDeploymentID   string       `json:"activeDeploymentId"`
	PreviousDeploymentID string       `json:"previousDeploymentId,omitempty"`
	ActiveSource         ActiveSource `json:"activeSource"`
	IPs                  []string     `json:"ips"`
}

// DisplayInfo is the output mode currently in use (width/height as the content sees it,
// after rotation) and, in status reports, the modes the screen offers.
type DisplayInfo struct {
	Output   string         `json:"output,omitempty"`
	Width    int            `json:"width"`
	Height   int            `json:"height"`
	Refresh  float64        `json:"refresh"`
	Rotation int            `json:"rotation"`
	Modes    []DisplayModes `json:"modes,omitempty"`
}

// DisplayModes is one resolution the screen reports (unrotated) with its refresh rates.
// Preferred/Current are the refresh rates of the preferred/current mode when it has this
// resolution (0 otherwise).
type DisplayModes struct {
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	Refresh   []float64 `json:"refresh"`
	Preferred float64   `json:"preferred,omitempty"`
	Current   float64   `json:"current,omitempty"`
}

// AppliedConfig tells the server what the agent made of its last device.config, so a
// change shows as pending until the device confirms it.
type AppliedConfig struct {
	Blackout     bool    `json:"blackout"`               // blackout setting followed
	Display      Display `json:"display"`                // last display setting from the server
	DisplayMode  Display `json:"displayMode"`            // setting in effect (server or client WebGUI)
	DisplayBusy  bool    `json:"displayBusy,omitempty"`  // the server's setting is being applied
	DisplayError string  `json:"displayError,omitempty"` // applying the server's setting failed
}

// Capabilities reported in Status.Caps.
const (
	// CapCleanCachePreview: clean_cache with params {"dryRun": true} only reports what it
	// would delete.
	CapCleanCachePreview = "clean_cache_preview"
	// CapRegeneration: the agent knows the plasma_regeneration action.
	CapRegeneration = "plasma_regeneration"
)

// CleanCacheParams are the parameters of the clean_cache action.
type CleanCacheParams struct {
	DryRun bool `json:"dryRun"`
}

// SyncInfo is the state of the content sync.
type SyncInfo struct {
	State        string `json:"state"` // idle | preparing | ready | active | failed | cleared
	DeploymentID string `json:"deploymentId,omitempty"`
	Done         int64  `json:"done"`
	Total        int64  `json:"total"`
	Error        string `json:"error,omitempty"`
}

// Player states reported in Status.
const (
	PlayerOK         = "ok"
	PlayerStarting   = "starting"
	PlayerCrashed    = "crashed"
	PlayerRestarting = "restarting"
)

// Status is sent every 30 s and whenever the active source changes.
type Status struct {
	Version            string       `json:"version"`
	OS                 string       `json:"os"`   // package platform for updates (linux)
	Arch               string       `json:"arch"` // amd64 | arm64 | armv7
	ActiveDeploymentID string       `json:"activeDeploymentId"`
	ActiveSource       ActiveSource `json:"activeSource"`
	Mode               string       `json:"mode"`            // player mode kind
	CPU                float64      `json:"cpu"`             // percent, all cores
	RAMUsed            uint64       `json:"ramUsed"`         // bytes
	RAMTotal           uint64       `json:"ramTotal"`        // bytes
	Temperature        *float64     `json:"temperature"`     // °C, null when unknown
	Power              *PowerInfo   `json:"power,omitempty"` // only where the platform reports the supply
	DiskTotal          uint64       `json:"diskTotal"`       // bytes (data dir)
	DiskFree           uint64       `json:"diskFree"`        // bytes (data dir)
	CacheBytes         int64        `json:"cacheBytes"`      // blob cache
	LogsBytes          int64        `json:"logsBytes"`       // log files
	Player             string       `json:"player"`          // ok | starting | crashed | restarting
	Video              *VideoInfo   `json:"video,omitempty"` // how videos are decoded
	PlayerItem         string       `json:"playerItem,omitempty"`
	PlayerRestarts     int          `json:"playerRestarts"`
	Display            DisplayInfo  `json:"display"`
	Sync               SyncInfo     `json:"sync"`
	IPs                []string     `json:"ips"`
	Uptime             int64        `json:"uptime"` // agent uptime in seconds
	Update             *UpdateInfo  `json:"update,omitempty"`
	// Applied is nil before the first device.config.
	Applied *AppliedConfig `json:"applied,omitempty"`
	Caps    []string       `json:"caps,omitempty"`
	// Model is the hardware (device tree on ARM boards, DMI on PCs), e.g. "Raspberry Pi 3
	// Model B Rev 1.2" – the admin marks unsupported boards.
	Model string `json:"model,omitempty"`
	// Clock: source and quality of the time used for synchronous playback.
	Clock *ClockInfo `json:"clock,omitempty"`
}

// UpdateInfo is the agent update state in status reports (absent when idle).
type UpdateInfo struct {
	Phase   string `json:"phase"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    int64  `json:"done,omitempty"`
	Total   int64  `json:"total,omitempty"`
}

// Deploy progress states.
const (
	DeployPreparing = "preparing"
	DeployReady     = "ready"
	DeployActive    = "active"
	DeployFailed    = "failed"
)

// DeployProgress reports the sync of one deployment.
type DeployProgress struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Error string `json:"error,omitempty"`
}

// ActionResult answers an Action.
type ActionResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

// UpdateProgress reports an update run (done/total in bytes while downloading). States:
// downloading, verifying, installing, restarting, done, failed, rolled_back.
type UpdateProgress struct {
	Version string `json:"version"`
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`
	Done    int64  `json:"done,omitempty"`
	Total   int64  `json:"total,omitempty"`
}

// ---------------------------------------------------------------- pairing (§7.1)

// Screen is the current output size.
type Screen struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// PairRequest is POST /device/v1/pair/request.
type PairRequest struct {
	HardwareID string   `json:"hardwareId"`
	Hostname   string   `json:"hostname"`
	Name       string   `json:"name,omitempty"` // name given at installation (proposed device name)
	Version    string   `json:"version"`
	OS         string   `json:"os"`
	Arch       string   `json:"arch"`
	IPs        []string `json:"ips"`
	Screen     Screen   `json:"screen"`
}

// PairResponse answers PairRequest.
type PairResponse struct {
	PairingID string `json:"pairingId"`
	PIN       string `json:"pin"`
	Secret    string `json:"secret"`
	ExpiresAt string `json:"expiresAt"`
}

// Pair states.
const (
	PairPending = "pending"
	PairClaimed = "claimed"
	PairExpired = "expired"
)

// PairStatus is GET /device/v1/pair/status?id=&secret=.
type PairStatus struct {
	State    string `json:"state"`
	DeviceID string `json:"deviceId,omitempty"`
	Token    string `json:"token,omitempty"`
}

// ErrorBody is the uniform HTTP error format (§5.1).
type ErrorBody struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Params  map[string]any `json:"params,omitempty"`
	} `json:"error"`
}

// ---------------------------------------------------------------- manifest (§5.5)

// Manifest is one immutable deployment of a zone.
type Manifest struct {
	ID               string `json:"id"`
	ZoneID           string `json:"zoneId"`
	Hash             string `json:"hash"`
	BuiltAt          string `json:"builtAt"`
	MinAgentVersion  string `json:"minAgentVersion"`
	Timezone         string `json:"timezone"`
	ClosedPlaylistID string `json:"closedPlaylistId,omitempty"`
	// Groups: display groups of the zone with their own closed playlist.
	Groups    []contracts.ManifestGroup `json:"groups,omitempty"`
	Rules     []contracts.Rule          `json:"rules"`
	Playlists []Playlist                `json:"playlists"`
	// Presentations are passed to the player unchanged (the agent never renders).
	Presentations []json.RawMessage `json:"presentations"`
	Products      ManifestProducts  `json:"products"`
	Assets        []ManifestAsset   `json:"assets"`
	Fonts         []ManifestFont    `json:"fonts"`
}

// Playlist (§5.3).
type Playlist struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Items []PlaylistItem `json:"items"`
}

// PlaylistItem (§5.3).
type PlaylistItem struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	RefID          string   `json:"refId"`
	PresentationID string   `json:"presentationId,omitempty"`
	Play           PlayMode `json:"play"`
	Fit            string   `json:"fit,omitempty"`
}

// PlayMode is { mode: "duration", ms } | { mode: "repeat", count }.
type PlayMode struct {
	Mode  string `json:"mode"`
	MS    int64  `json:"ms,omitempty"`
	Count int    `json:"count,omitempty"`
}

// ManifestProducts lists product images to cache.
type ManifestProducts struct {
	ImageAssetIDs []string `json:"imageAssetIds"`
}

// Asset kinds and variants.
const (
	KindImage = "image"
	KindVideo = "video"
	KindAudio = "audio"
	KindFont  = "font"

	VariantOrig   = "orig"
	VariantV1080  = "v1080"
	VariantV1080H = "v1080h" // full HD at half the frame rate, only for sources above 30 fps
	VariantV720   = "v720"
	VariantI1920  = "i1920"
	VariantI512   = "i512"
	VariantAudio  = "a"
)

// ManifestAsset is one downloadable variant of an asset. An asset id may appear once
// per variant; the agent caches exactly one variant per asset.
type ManifestAsset struct {
	ID         string `json:"id"`
	Revision   int64  `json:"revision"`
	Kind       string `json:"kind"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	MIME       string `json:"mime"`
	Variant    string `json:"variant"`
	URL        string `json:"url"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	DurationMS int64  `json:"durationMs,omitempty"`
}

// ManifestFont is an uploaded font family.
type ManifestFont struct {
	Family string             `json:"family"`
	Files  []ManifestFontFile `json:"files"`
}

// ManifestFontFile is one font file (served at /fonts/custom/{sha256}).
type ManifestFontFile struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Weight [2]int `json:"weight"`
	Style  string `json:"style"`
	Format string `json:"format,omitempty"`
}

// ---------------------------------------------------------------- live state (§5.6)

// LiveZone is the part of the live state the agent itself needs.
type LiveZone struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"` // OPEN | CLOSED
	// StateChangedAt (RFC 3339) anchors the closed content.
	StateChangedAt string `json:"stateChangedAt,omitempty"`
}

// LiveHeader parses only the zone of a live state; the rest is passed through raw.
type LiveHeader struct {
	Zone       LiveZone `json:"zone"`
	ServerTime string   `json:"serverTime"`
}

// ---------------------------------------------------------------- player link (§5.8)

// Player mode kinds.
const (
	ModeSearching = "searching"
	ModePairing   = "pairing"
	ModeStandby   = "standby"
	ModePlaying   = "playing"
	ModeCleared   = "cleared"
	ModeBlackout  = "blackout"
	ModeUpdating  = "updating"
	// ModeRegeneration: plasma regeneration, full-screen white noise until switched off or
	// the agent restarts (never stored).
	ModeRegeneration = "regeneration"
)

// PlayerMode tells the player what to show.
type PlayerMode struct {
	Kind          string   `json:"kind"`
	PIN           string   `json:"pin,omitempty"`
	IPs           []string `json:"ips"`
	WebGUIURL     string   `json:"webguiUrl"`
	Fingerprint   string   `json:"fingerprint,omitempty"` // short server fingerprint
	DeviceName    string   `json:"deviceName,omitempty"`
	ServerAddress string   `json:"serverAddress,omitempty"`
	Language      string   `json:"language"`
	Notice        string   `json:"notice,omitempty"`
}

// PlayerBundle carries the active manifest (null when there is none).
type PlayerBundle struct {
	Hash     string          `json:"hash"`
	Manifest json.RawMessage `json:"manifest"`
}

// PlayerSource selects the playlist to play (null = show nothing).
type PlayerSource struct {
	PlaylistID *string `json:"playlistId"`
	Reason     string  `json:"reason"`
	// Anchor (ms since the epoch, corrected clock) starts the playlist's timeline: every
	// screen plays position (now - anchor) mod length, so screens switch together.
	// Null: play from the start (older behavior).
	Anchor *int64 `json:"anchor"`
	// Position of the screen in its display group: it plays that track of a playlist for
	// several screens (track 1 when the playlist has fewer tracks).
	Position int `json:"position"`
}

// PlayerLive carries the zone live state plus the schedule-effective prices.
type PlayerLive struct {
	State  json.RawMessage            `json:"state"`
	Prices map[string]contracts.Money `json:"prices"`
	// Ticker of the active time schedule rule, shown over the content of the zone's
	// screens (null when no rule shows one).
	Ticker *PlayerTicker `json:"ticker"`
}

// PlayerTicker is a ticker from a time schedule rule with its resolved style.
type PlayerTicker struct {
	Text     string                   `json:"text"`
	Template contracts.TickerTemplate `json:"template"`
	RuleID   string                   `json:"ruleId"`
}

// PlayerConfig are the per-device rendering settings.
type PlayerConfig struct {
	ScaleMode     string `json:"scaleMode"`
	Perf          string `json:"perf"` // low | standard | high (auto resolved by the agent)
	PlasmaShift   bool   `json:"plasmaShift"`
	ShowIPOverlay bool   `json:"showIpOverlay"`
	Timezone      string `json:"timezone"`
	ClockOffsetMS int64  `json:"clockOffsetMs"`
}

// PlayerCmd is a one-shot command.
type PlayerCmd struct {
	Name string `json:"name"`
}

// VersionNotice is shown discreetly when agent and server versions differ.
type VersionNotice struct {
	Text *string `json:"text"`
}

// PlayerPing is the player's heartbeat (every 5 s).
type PlayerPing struct {
	Heap int64   `json:"heap"`
	FPS  float64 `json:"fps"`
	Item string  `json:"item"`
}

// PlayerLog forwards a player log line.
type PlayerLog struct {
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// PowerInfo reports the supply voltage state (Raspberry Pi under-voltage alarm).
type PowerInfo struct {
	UnderVoltage bool   `json:"underVoltage"`   // the alarm is active right now
	Events       int    `json:"events"`         // under-voltage events since the agent started
	Last         string `json:"last,omitempty"` // RFC 3339 start of the last event
}

// VideoInfo reports how the player decodes videos.
type VideoInfo struct {
	HW         bool   `json:"hw"`                   // hardware decoding in use
	Path       string `json:"path,omitempty"`       // v4l2 | vaapi-vulkan ("" = no known path)
	Fallback   string `json:"fallback,omitempty"`   // decode_errors | player_crashes: "auto" switched to software
	FallbackAt string `json:"fallbackAt,omitempty"` // RFC 3339
	// GPU is what Chromium really renders with: hardware | software (never had the GPU) |
	// lost (had it, lost it, restarts did not help); "" = not known yet.
	GPU           string `json:"gpu,omitempty"`
	GPUCrashes    int    `json:"gpuCrashes,omitempty"`    // GPU process restarts seen (agent session)
	GPURecoveries int    `json:"gpuRecoveries,omitempty"` // player restarts that brought the GPU back
	GPULostAt     string `json:"gpuLostAt,omitempty"`     // RFC 3339, last loss
}
