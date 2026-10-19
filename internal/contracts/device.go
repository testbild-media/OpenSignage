package contracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Device channel contracts: deployment manifest (ARCHITECTURE §5.5), live state (§5.6),
// device link messages (§5.7) and the pairing REST payloads (§7.1).

// ---------------------------------------------------------------- deployment manifest

// Manifest describes everything a zone can play (immutable, one per deployment row).
type Manifest struct {
	ID               string `json:"id"`
	ZoneID           string `json:"zoneId"`
	Hash             string `json:"hash"` // sha256 of the canonical JSON without id/hash/builtAt
	BuiltAt          string `json:"builtAt"`
	MinAgentVersion  string `json:"minAgentVersion"` // = server version
	Timezone         string `json:"timezone"`
	ClosedPlaylistID string `json:"closedPlaylistId,omitempty"`
	// Groups are the display groups of the zone; the agent uses the one in
	// its device config for the group's own closed playlist.
	Groups []ManifestGroup `json:"groups,omitempty"`
	Rules  []Rule          `json:"rules"`
	// Playlists are the published playlist documents (§5.3) in canonical JSON form
	// (object keys sorted). They are carried verbatim so the manifest never drops a
	// field the playlist contract adds later.
	Playlists     []json.RawMessage `json:"playlists"`
	Presentations []Presentation    `json:"presentations"`
	Products      ManifestProducts  `json:"products"`
	Assets        []ManifestAsset   `json:"assets"`
	Fonts         []ManifestFont    `json:"fonts"`
}

// ManifestGroup is a display group of the zone.
type ManifestGroup struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ClosedPlaylistID string `json:"closedPlaylistId,omitempty"`
}

// ManifestProducts lists product images the client caches for live product rows.
type ManifestProducts struct {
	ImageAssetIDs []string `json:"imageAssetIds"`
}

// ManifestAsset is one downloadable variant of a media asset.
type ManifestAsset struct {
	ID         string `json:"id"`
	Revision   int64  `json:"revision"`
	Kind       string `json:"kind"` // image | video | audio | font
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Mime       string `json:"mime"`
	Variant    string `json:"variant"` // orig | v1080 | v1080h | v720 | i1920 | i512 | poster | a
	URL        string `json:"url"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	DurationMS int64  `json:"durationMs,omitempty"`
}

// ManifestFont is an uploaded font family (bundled fonts ship with the client).
type ManifestFont struct {
	Family string             `json:"family"`
	Files  []ManifestFontFile `json:"files"`
}

// ManifestFontFile is one face of an uploaded font family.
type ManifestFontFile struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Weight [2]int `json:"weight"` // [min, max] (variable fonts span a range)
	Style  string `json:"style"`  // normal | italic
	Format string `json:"format"` // woff2 | woff | truetype | opentype
}

// canonicalManifest is Manifest without id, hash and builtAt; its JSON is the hash input.
type canonicalManifest struct {
	ZoneID           string            `json:"zoneId"`
	MinAgentVersion  string            `json:"minAgentVersion"`
	Timezone         string            `json:"timezone"`
	ClosedPlaylistID string            `json:"closedPlaylistId,omitempty"`
	Groups           []ManifestGroup   `json:"groups,omitempty"`
	Rules            []Rule            `json:"rules"`
	Playlists        []json.RawMessage `json:"playlists"`
	Presentations    []Presentation    `json:"presentations"`
	Products         ManifestProducts  `json:"products"`
	Assets           []ManifestAsset   `json:"assets"`
	Fonts            []ManifestFont    `json:"fonts"`
}

// CanonicalJSON returns the deterministic JSON of the manifest without id, hash and
// builtAt. Callers must keep slices in a stable order (the builder sorts them).
func (m *Manifest) CanonicalJSON() ([]byte, error) {
	c := canonicalManifest{
		ZoneID: m.ZoneID, MinAgentVersion: m.MinAgentVersion, Timezone: m.Timezone,
		ClosedPlaylistID: m.ClosedPlaylistID, Groups: m.Groups, Rules: m.Rules, Playlists: m.Playlists,
		Presentations: m.Presentations, Products: m.Products, Assets: m.Assets, Fonts: m.Fonts,
	}
	return json.Marshal(c)
}

// CanonicalHash returns the hex sha256 of CanonicalJSON.
func (m *Manifest) CanonicalHash() (string, error) {
	b, err := m.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// BlobHashes returns the distinct, sorted sha256 values of all assets and font files.
func (m *Manifest) BlobHashes() []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	for _, a := range m.Assets {
		add(a.SHA256)
	}
	for _, f := range m.Fonts {
		for _, file := range f.Files {
			add(file.SHA256)
		}
	}
	sort.Strings(out)
	return out
}

// CanonicalizeJSON re-encodes a JSON value with sorted object keys and exact numbers, so
// equal documents always produce equal bytes.
func CanonicalizeJSON(raw []byte) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// ---------------------------------------------------------------- live state

// LiveState is the complete live state of one zone (payload of `live.state`).
type LiveState struct {
	Zone       LiveZone       `json:"zone"`
	Ticker     LiveTicker     `json:"ticker"`
	Categories []LiveCategory `json:"categories"`
	Products   []LiveProduct  `json:"products"`
	ServerTime string         `json:"serverTime"`
}

// LiveZone identifies the zone and its OPEN/CLOSED state.
type LiveZone struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"` // OPEN | CLOSED
	// StateChangedAt (RFC 3339) is when the state was last switched: the anchor of the
	// closed content for synchronous playback. "" = unknown.
	StateChangedAt string `json:"stateChangedAt,omitempty"`
}

// LiveTicker is the zone ticker overlay.
type LiveTicker struct {
	Text     string         `json:"text"`
	Visible  bool           `json:"visible"`
	Template TickerTemplate `json:"template"`
}

// LiveCategory is a product category (max two levels).
type LiveCategory struct {
	ID       string `json:"id"`
	ParentID string `json:"parentId,omitempty"`
	Name     string `json:"name"`
	Sort     int64  `json:"sort"`
}

// LiveProduct is one product with its effective status for the zone.
type LiveProduct struct {
	ID                     string    `json:"id"`
	Name                   string    `json:"name"`
	Description            string    `json:"description"`
	CategoryID             string    `json:"categoryId"`
	Type                   string    `json:"type"` // drink | food | other
	Quantity               *Quantity `json:"quantity,omitempty"`
	Price                  Money     `json:"price"`  // base price; schedule prices come from the rules
	Status                 string    `json:"status"` // AVAILABLE | UNAVAILABLE | SOLD_OUT (zone override → global)
	ImageAssetID           string    `json:"imageAssetId,omitempty"`
	ShowImageInsteadOfName bool      `json:"showImageInsteadOfName"`
	// ImageScale: size of the image instead of the name, percent of the automatic size
	// (absent = 100).
	ImageScale int   `json:"imageScale,omitempty"`
	Sort       int64 `json:"sort"`
	// Variants share the price and are shown like the description, in this order
	// (UNAVAILABLE ones not at all). Absent when the product has none.
	Variants []LiveVariant `json:"variants,omitempty"`
}

// LiveVariant is one variant of a product with its effective status for the zone.
type LiveVariant struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // AVAILABLE | UNAVAILABLE | SOLD_OUT (zone override → global)
}

// ---------------------------------------------------------------- device link (WS)

// Device link message types (envelope field `t`).
const (
	// Server → agent.
	MsgWelcome         = "welcome"
	MsgDeviceConfig    = "device.config"
	MsgLiveState       = "live.state"
	MsgDeployAvailable = "deploy.available"
	MsgDeployCleared   = "deploy.cleared"
	MsgAction          = "action"
	MsgUpdateOffer     = "update.offer"
	MsgRevoked         = "revoked"
	// Agent → server.
	MsgHello          = "hello"
	MsgStatus         = "status"
	MsgDeployProgress = "deploy.progress"
	MsgActionResult   = "action.result"
	MsgUpdateProgress = "update.progress"
	// Clock measurement: the agent asks, the server answers at once.
	MsgTimePing = "time.ping"
	MsgTimePong = "time.pong"
)

// Device link close codes (besides the standard 1000/1001/1008/1009/1013).
const (
	CloseRevoked  = 4401 // token revoked or unknown: wipe server data, return to pairing
	CloseReplaced = 4409 // a newer connection of the same device replaced this one
)

// LinkMessage is the envelope of every device link message.
type LinkMessage struct {
	T  string          `json:"t"`
	ID string          `json:"id,omitempty"`
	P  json.RawMessage `json:"p,omitempty"`
}

// Welcome answers hello.
type Welcome struct {
	DeviceID      string `json:"deviceId"`
	ServerVersion string `json:"serverVersion"`
	Protocol      int    `json:"protocol"`
	ServerTime    string `json:"serverTime"`
	VersionMatch  bool   `json:"versionMatch"` // false → no deployments are sent
}

// DisplayConfig controls the physical output. Width/Height 0 = keep the current mode.
type DisplayConfig struct {
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Refresh  float64 `json:"refresh"`
	Rotation int     `json:"rotation"` // 0 | 90 | 180 | 270
	Custom   bool    `json:"custom"`   // force a mode the screen does not report
}

// DeviceConfig is the `device.config` payload.
type DeviceConfig struct {
	Name   string `json:"name"`
	ZoneID string `json:"zoneId,omitempty"`
	// GroupID is the display group in the zone ("" = none); Position the screen's place in
	// its group or among the screens without group (1..n, 0 = unknown).
	GroupID  string `json:"groupId,omitempty"`
	Position int    `json:"position,omitempty"`
	// ClockKey (hex) authenticates the LAN time exchange of the zone's screens.
	ClockKey           string        `json:"clockKey,omitempty"`
	ScaleMode          string        `json:"scaleMode"` // contain | cover | none
	Blackout           bool          `json:"blackout"`
	Display            DisplayConfig `json:"display"`
	Perf               string        `json:"perf"`    // auto | low | standard | high
	HWVideo            string        `json:"hwVideo"` // auto | on | off
	PlasmaShift        bool          `json:"plasmaShift"`
	ShowIPOverlay      bool          `json:"showIpOverlay"`
	WebGUIPasswordHash string        `json:"webguiPasswordHash,omitempty"` // "" = managed locally
	// Language is the server default UI language (player texts, number and date formats).
	Language string `json:"language,omitempty"`
	// ServerVersion and UpdateRepo let the client WebGUI offer the GitHub fallback for
	// exactly the server's version.
	ServerVersion string `json:"serverVersion,omitempty"`
	UpdateRepo    string `json:"updateRepo,omitempty"`
}

// TimePing asks for the server time; T0 is echoed (the agent's own reference).
type TimePing struct {
	Seq uint64 `json:"seq"`
	T0  int64  `json:"t0"`
}

// TimePong answers a TimePing with the server time (unix µs) when it was handled.
type TimePong struct {
	Seq    uint64 `json:"seq"`
	T0     int64  `json:"t0"`
	Server int64  `json:"server"`
}

// DeployAvailable announces the zone's current deployment.
type DeployAvailable struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}

// DeployCleared tells the agent to show nothing (re-sent on every connection).
type DeployCleared struct {
	Reason string `json:"reason"` // no_content | no_zone
}

// Action is a remote action for the agent.
type Action struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"` // sync_now | reload_player | restart_client | reboot_device | clean_cache | screenshot
	Params json.RawMessage `json:"params,omitempty"`
}

// UpdateOffer asks the agent to install a release package. The agent trusts SHA256 and
// Size only when they match the signed release metadata (UpdateRelease).
type UpdateOffer struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// UpdateRelease is the signed metadata of a release (`GET /device/v1/update/release`):
// release.json, SHA256SUMS and SHA256SUMS.sig exactly as published (base64 in JSON). The
// agent verifies the signature with its own trusted keys and takes the checksum and size
// of its package only from this data.
type UpdateRelease struct {
	Version string `json:"version"`
	Info    []byte `json:"release"`
	Sums    []byte `json:"sums"`
	Sig     []byte `json:"sig"`
}

// ActiveSource is what the player really shows.
type ActiveSource struct {
	Kind       string `json:"kind"` // playlist | closed | cleared
	PlaylistID string `json:"playlistId,omitempty"`
	RuleID     string `json:"ruleId,omitempty"`
}

// Hello is the first agent message on every connection.
type Hello struct {
	Version              string       `json:"version"`
	Protocol             int          `json:"protocol"`
	HardwareID           string       `json:"hardwareId"`
	ActiveDeploymentID   string       `json:"activeDeploymentId"`
	PreviousDeploymentID string       `json:"previousDeploymentId,omitempty"`
	ActiveSource         ActiveSource `json:"activeSource"`
	IPs                  []string     `json:"ips"`
}

// DeviceStatus lists the well-known keys of a `status` message (every 30 s). The server
// stores the whole object; additional metrics are allowed. Every field is optional: older
// agents do not send the newer ones.
type DeviceStatus struct {
	Version      string         `json:"version,omitempty"`
	OS           string         `json:"os,omitempty"`   // package platform for client updates
	Arch         string         `json:"arch,omitempty"` // amd64 | arm64 | armv7
	IPs          []string       `json:"ips,omitempty"`
	Player       string         `json:"player,omitempty"` // ok | starting | crashed | …
	ActiveSource *ActiveSource  `json:"activeSource,omitempty"`
	DiskTotal    uint64         `json:"diskTotal,omitempty"`  // bytes of the file system holding the data dir
	DiskFree     uint64         `json:"diskFree,omitempty"`   // bytes available
	CacheBytes   int64          `json:"cacheBytes,omitempty"` // content cache (blobs)
	LogsBytes    *int64         `json:"logsBytes,omitempty"`  // log files (absent in older agents)
	Power        *StatusPower   `json:"power,omitempty"`      // supply voltage (Raspberry Pi)
	Video        *StatusVideo   `json:"video,omitempty"`      // video decoding (absent in older agents)
	Display      *StatusDisplay `json:"display,omitempty"`
	Applied      *AppliedConfig `json:"applied,omitempty"`
	Caps         []string       `json:"caps,omitempty"`
	Model        string         `json:"model,omitempty"` // hardware model
}

// Agent capabilities reported in DeviceStatus.Caps.
const (
	// CapCleanCachePreview: clean_cache accepts params {"dryRun": true} and then only
	// reports what it would delete (older agents would delete at once).
	CapCleanCachePreview = "clean_cache_preview"
	// CapRegeneration: the agent knows the plasma_regeneration action (params {"on": bool}).
	CapRegeneration = "plasma_regeneration"
)

// StatusDisplay is the output mode in use and the modes the screen offers.
type StatusDisplay struct {
	Output   string             `json:"output,omitempty"`
	Width    int                `json:"width"` // as the content sees it (after rotation)
	Height   int                `json:"height"`
	Refresh  float64            `json:"refresh"`
	Rotation int                `json:"rotation"`
	Modes    []DisplayModeGroup `json:"modes,omitempty"`
}

// DisplayModeGroup is one resolution the screen reports (unrotated) with its refresh
// rates. Preferred/Current carry the refresh rate of the preferred/current mode when it
// has this resolution (0 otherwise).
type DisplayModeGroup struct {
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	Refresh   []float64 `json:"refresh"`
	Preferred float64   `json:"preferred,omitempty"`
	Current   float64   `json:"current,omitempty"`
}

// AppliedConfig is what the agent made of the last device.config: the server compares it
// with the stored config to show a change as pending until the device confirms it.
type AppliedConfig struct {
	Blackout bool `json:"blackout"`
	// Display is the last display setting received from the server.
	Display DisplayConfig `json:"display"`
	// DisplayMode is the setting in effect on the device (the server's or one chosen in
	// the client WebGUI).
	DisplayMode  DisplayConfig `json:"displayMode"`
	DisplayBusy  bool          `json:"displayBusy,omitempty"`  // the server's setting is being applied
	DisplayError string        `json:"displayError,omitempty"` // applying the server's setting failed
}

// CacheCleanup is the data of a clean_cache result (a preview with DryRun).
type CacheCleanup struct {
	DryRun       bool  `json:"dryRun,omitempty"`
	Deleted      int   `json:"deleted"`      // files (unused content and unfinished downloads)
	FreedBytes   int64 `json:"freedBytes"`   // bytes of those files
	Partial      int   `json:"partial"`      // of which unfinished downloads
	PartialBytes int64 `json:"partialBytes"` // and their bytes
	CacheBytes   int64 `json:"cacheBytes"`   // cache size afterwards
}

// DeployProgress reports the sync state of one deployment.
type DeployProgress struct {
	ID    string `json:"id"`
	State string `json:"state"` // preparing | ready | active | failed
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Error string `json:"error,omitempty"`
}

// ActionResult answers an action.
type ActionResult struct {
	ID    string          `json:"id"`
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// UpdateProgress reports an agent update. States: downloading (done/total bytes),
// verifying, installing, restarting, done, failed, rolled_back.
type UpdateProgress struct {
	Version string `json:"version"`
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`
	Done    int64  `json:"done,omitempty"`
	Total   int64  `json:"total,omitempty"`
}

// ---------------------------------------------------------------- pairing (REST, §7.1)

// PairRequest is sent by an unpaired agent (`POST /device/v1/pair/request`).
type PairRequest struct {
	HardwareID string          `json:"hardwareId"`
	Hostname   string          `json:"hostname"`
	Name       string          `json:"name,omitempty"` // name given at installation (proposed device name)
	Version    string          `json:"version"`
	OS         string          `json:"os"`
	Arch       string          `json:"arch"`
	IPs        []string        `json:"ips"`
	Screen     json.RawMessage `json:"screen,omitempty"`
}

// PairResponse carries the PIN shown on screen and the secret for status polling.
type PairResponse struct {
	PairingID string `json:"pairingId"`
	PIN       string `json:"pin"`
	Secret    string `json:"secret"`
	ExpiresAt string `json:"expiresAt"`
}

// PairStatus answers `GET /device/v1/pair/status` (token exactly once).
type PairStatus struct {
	State    string `json:"state"` // pending | claimed | expired
	DeviceID string `json:"deviceId,omitempty"`
	Token    string `json:"token,omitempty"`
}

// StatusPower is the supply voltage state a device reports (Raspberry Pi: under-voltage
// makes the CPU throttle, videos stutter).
type StatusPower struct {
	UnderVoltage bool   `json:"underVoltage"`
	Events       int    `json:"events"`
	Last         string `json:"last,omitempty"`
}

// StatusVideo is how a device decodes videos: in hardware (path v4l2 = Raspberry Pi,
// vaapi-vulkan = Intel/AMD) or in software. Fallback "decode_errors" / "player_crashes": the
// setting "auto" switched to software decoding after repeated decode errors or player
// crashes with Vulkan compositing (until the agent restarts).
// GPU: what Chromium really renders with – hardware | software (never
// had the GPU) | lost (had it, lost it; restarting the player did not bring it back).
type StatusVideo struct {
	HW            bool   `json:"hw"`
	Path          string `json:"path,omitempty"`
	Fallback      string `json:"fallback,omitempty"`
	FallbackAt    string `json:"fallbackAt,omitempty"`
	GPU           string `json:"gpu,omitempty"`
	GPUCrashes    int    `json:"gpuCrashes,omitempty"`
	GPURecoveries int    `json:"gpuRecoveries,omitempty"`
	GPULostAt     string `json:"gpuLostAt,omitempty"`
}
