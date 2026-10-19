package contracts

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Product and zone states (ARCHITECTURE §5.1). The three product states are kept distinct
// from the server to the screen; they must never collapse into "available yes/no".
const (
	StatusAvailable   = "AVAILABLE"
	StatusUnavailable = "UNAVAILABLE"
	StatusSoldOut     = "SOLD_OUT"

	ZoneOpen   = "OPEN"
	ZoneClosed = "CLOSED"
)

// ValidStatus reports whether s is one of the three product states.
func ValidStatus(s string) bool {
	return s == StatusAvailable || s == StatusUnavailable || s == StatusSoldOut
}

// VariantsSoldOut reports whether a product with variants is sold out because every
// variant it offers (not UNAVAILABLE) is: the screens then show the whole product as sold out.
func VariantsSoldOut(variants []LiveVariant) bool {
	offered := false
	for _, v := range variants {
		switch v.Status {
		case StatusUnavailable:
		case StatusSoldOut:
			offered = true
		default:
			return false
		}
	}
	return offered
}

// Product types and quantity units.
var (
	productTypes  = []string{"drink", "food", "other"}
	quantityUnits = []string{"ml", "cl", "l", "g", "kg", "pcs"}
)

// ValidProductType reports whether t is a known product type.
func ValidProductType(t string) bool { return listHas(productTypes, t) }

// Quantity is the serving size of a product. Value is a decimal string ("0.33") so no
// floating point rounding can ever creep in.
type Quantity struct {
	Value string `json:"value"`
	Unit  string `json:"unit"`
}

var (
	decimalRe  = regexp.MustCompile(`^(0|[1-9][0-9]{0,5})(\.[0-9]{1,3})?$`)
	currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)
)

// Validate checks value (positive decimal, at most three fraction digits) and unit.
func (q Quantity) Validate() error {
	if !decimalRe.MatchString(q.Value) || strings.Trim(q.Value, "0.") == "" {
		return fieldErr("quantity.value", "invalid")
	}
	if !listHas(quantityUnits, q.Unit) {
		return fieldErr("quantity.unit", "invalid")
	}
	return nil
}

// MaxMoneyAmount bounds a single price (minor units) far below int64 overflow.
const MaxMoneyAmount = 100_000_000_000

// Validate checks a price: non-negative minor units and an upper-case ISO 4217 code.
func (m Money) Validate(field string) error {
	if m.Amount < 0 || m.Amount > MaxMoneyAmount {
		return fieldErr(field+".amount", "out_of_range")
	}
	if !currencyRe.MatchString(m.Currency) {
		return fieldErr(field+".currency", "invalid")
	}
	return nil
}

// Playlist limits.
const (
	MaxPlaylistItems = 500
	MinPlayMS        = 1000
	MaxPlayMS        = 24 * 60 * 60 * 1000
	MaxRepeat        = 1000
	// MaxPlaylistScreens bounds the tracks of a playlist for several screens.
	MaxPlaylistScreens = 16
)

// Play modes of a playlist item. Exactly one of the two applies per item.
const (
	PlayDuration = "duration"
	PlayRepeat   = "repeat"
)

// Playlist item kinds.
const (
	ItemPresentation = "presentation"
	ItemSlide        = "slide"
	ItemMedia        = "media"
)

// Play says how long an item runs: for MS milliseconds (the running pass is finished, not
// cut off) or for Count complete passes.
type Play struct {
	Mode  string `json:"mode"` // duration | repeat
	MS    int64  `json:"ms,omitempty"`
	Count int    `json:"count,omitempty"`
}

// PlaylistItem is one entry of a playlist (ARCHITECTURE §5.3).
type PlaylistItem struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`                     // presentation | slide | media
	RefID          string `json:"refId"`                    // presentation id, slide id or media asset id
	PresentationID string `json:"presentationId,omitempty"` // only for kind "slide"
	Play           Play   `json:"play"`
	Fit            string `json:"fit,omitempty"` // media only
	// Cells are the contents of tracks 2..Screens of a playlist for several screens;
	// a nil cell shows the same as track 1 (the item itself).
	Cells []*PlaylistCell `json:"cells,omitempty"`
}

// PlaylistCell is the content of one track in one step: a slide or a medium.
type PlaylistCell struct {
	Kind           string `json:"kind"` // slide | media
	RefID          string `json:"refId"`
	PresentationID string `json:"presentationId,omitempty"` // only for kind "slide"
	Fit            string `json:"fit,omitempty"`            // media only
}

// Playlist is an ordered list of items.
type Playlist struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Items []PlaylistItem `json:"items"`
	// Screens is the number of tracks (0 or 1 = every screen shows the same). A screen at
	// position n plays track n, a position beyond the tracks plays track 1. With several
	// tracks every step is a slide or a medium with a fixed duration.
	Screens int `json:"screens,omitempty"`
}

// PlaylistResolver answers reference questions while validating a playlist.
type PlaylistResolver interface {
	PresentationExists(id string) bool
	SlideExists(presentationID, slideID string) bool
	MediaKind(assetID string) (kind string, ok bool) // image | video | audio
}

// Validate checks a playlist. res may be nil to skip reference checks. The first problem
// is returned as a *FieldError whose Field is the path (e.g. "items[2].play").
func (p *Playlist) Validate(res PlaylistResolver) error {
	if n := utf8.RuneCountInString(strings.TrimSpace(p.Name)); n < 1 || n > 200 {
		return fieldErr("name", "invalid")
	}
	if len(p.Items) > MaxPlaylistItems {
		return fieldErr("items", "too_many")
	}
	if p.Screens < 0 || p.Screens > MaxPlaylistScreens {
		return fieldErr("screens", "out_of_range")
	}
	tracks := p.Screens > 1
	seen := map[string]bool{}
	for i := range p.Items {
		it := &p.Items[i]
		path := fmt.Sprintf("items[%d]", i)
		if !idRe.MatchString(it.ID) || seen[it.ID] {
			return fieldErr(path+".id", "invalid")
		}
		seen[it.ID] = true
		if it.Kind != ItemMedia && it.Fit != "" {
			return fieldErr(path+".fit", "media_only")
		}
		if it.RefID == "" {
			return fieldErr(path+".refId", "required")
		}
		switch it.Kind {
		case ItemPresentation:
			if it.PresentationID != "" {
				return fieldErr(path+".presentationId", "must_be_empty")
			}
			if res != nil && !res.PresentationExists(it.RefID) {
				return fieldErr(path+".refId", "unknown_reference")
			}
		case ItemSlide:
			if it.PresentationID == "" {
				return fieldErr(path+".presentationId", "required")
			}
			if res != nil && !res.SlideExists(it.PresentationID, it.RefID) {
				return fieldErr(path+".refId", "unknown_reference")
			}
		case ItemMedia:
			if it.PresentationID != "" {
				return fieldErr(path+".presentationId", "must_be_empty")
			}
			if it.Fit != "" && it.Fit != "contain" && it.Fit != "cover" && it.Fit != "fill" {
				return fieldErr(path+".fit", "invalid_value")
			}
			if res != nil {
				kind, ok := res.MediaKind(it.RefID)
				switch {
				case !ok:
					return fieldErr(path+".refId", "unknown_reference")
				case kind != "image" && kind != "video":
					return fieldErr(path+".refId", "wrong_asset_kind")
				case kind == "image" && it.Play.Mode != PlayDuration:
					return fieldErr(path+".play.mode", "image_requires_duration")
				}
			}
		default:
			return fieldErr(path+".kind", "invalid_value")
		}
		if err := validatePlay(path, it.Play); err != nil {
			return err
		}
		if !tracks {
			if len(it.Cells) > 0 {
				return fieldErr(path+".cells", "must_be_empty")
			}
			continue
		}
		// Several tracks: every step runs exactly its duration on all screens.
		if it.Kind == ItemPresentation {
			return fieldErr(path+".kind", "single_track_only")
		}
		if it.Play.Mode != PlayDuration {
			return fieldErr(path+".play.mode", "tracks_require_duration")
		}
		if len(it.Cells) > p.Screens-1 {
			return fieldErr(path+".cells", "too_many")
		}
		for c, cell := range it.Cells {
			if cell == nil {
				continue
			}
			if err := validateCell(fmt.Sprintf("%s.cells[%d]", path, c), cell, res); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCell(path string, c *PlaylistCell, res PlaylistResolver) error {
	if c.RefID == "" {
		return fieldErr(path+".refId", "required")
	}
	switch c.Kind {
	case ItemSlide:
		if c.Fit != "" {
			return fieldErr(path+".fit", "media_only")
		}
		if c.PresentationID == "" {
			return fieldErr(path+".presentationId", "required")
		}
		if res != nil && !res.SlideExists(c.PresentationID, c.RefID) {
			return fieldErr(path+".refId", "unknown_reference")
		}
	case ItemMedia:
		if c.PresentationID != "" {
			return fieldErr(path+".presentationId", "must_be_empty")
		}
		if c.Fit != "" && c.Fit != "contain" && c.Fit != "cover" && c.Fit != "fill" {
			return fieldErr(path+".fit", "invalid_value")
		}
		if res != nil {
			kind, ok := res.MediaKind(c.RefID)
			switch {
			case !ok:
				return fieldErr(path+".refId", "unknown_reference")
			case kind != "image" && kind != "video":
				return fieldErr(path+".refId", "wrong_asset_kind")
			}
		}
	default:
		return fieldErr(path+".kind", "invalid_value")
	}
	return nil
}

// PlaylistRef is one thing a playlist plays: an item or a cell of another track.
type PlaylistRef struct {
	Path           string // "items[2]" or "items[2].cells[0]"
	Kind           string // presentation | slide | media
	RefID          string
	PresentationID string
}

// Refs lists everything the playlist plays, the cells of further tracks included.
func (p *Playlist) Refs() []PlaylistRef {
	var out []PlaylistRef
	for i, it := range p.Items {
		path := fmt.Sprintf("items[%d]", i)
		out = append(out, PlaylistRef{Path: path, Kind: it.Kind, RefID: it.RefID, PresentationID: it.PresentationID})
		for c, cell := range it.Cells {
			if cell != nil {
				out = append(out, PlaylistRef{Path: fmt.Sprintf("%s.cells[%d]", path, c), Kind: cell.Kind, RefID: cell.RefID,
					PresentationID: cell.PresentationID})
			}
		}
	}
	return out
}

// Tracks is the number of tracks (at least 1).
func (p *Playlist) Tracks() int {
	if p.Screens > 1 {
		return p.Screens
	}
	return 1
}

func validatePlay(path string, pl Play) error {
	switch pl.Mode {
	case PlayDuration:
		if pl.Count != 0 {
			return fieldErr(path+".play", "duration_xor_repeat")
		}
		if pl.MS < MinPlayMS || pl.MS > MaxPlayMS {
			return fieldErr(path+".play.ms", "out_of_range")
		}
	case PlayRepeat:
		if pl.MS != 0 {
			return fieldErr(path+".play", "duration_xor_repeat")
		}
		if pl.Count < 1 || pl.Count > MaxRepeat {
			return fieldErr(path+".play.count", "out_of_range")
		}
	default:
		return fieldErr(path+".play.mode", "invalid_value")
	}
	return nil
}

// TickerTemplate styles the zone ticker (ARCHITECTURE §5.6). Height is in design pixels of
// a 1080 px tall screen.
type TickerTemplate struct {
	Font       string  `json:"font"`
	Size       float64 `json:"size"`
	Color      string  `json:"color"`
	Background string  `json:"background"`
	Speed      float64 `json:"speed"` // design px per second
	Direction  string  `json:"direction"`
	Height     float64 `json:"height"`
	Position   string  `json:"position"`
}

// DefaultTickerTemplate is used by zones without an assigned template.
func DefaultTickerTemplate() TickerTemplate {
	return TickerTemplate{Font: "Montserrat", Size: 32, Color: "#ffffff", Background: "#000000cc",
		Speed: 120, Direction: "left", Height: 64, Position: "bottom"}
}

// Validate checks the template; fontExists decides whether the font family is available.
func (t *TickerTemplate) Validate(fontExists func(family string) bool) error {
	if t.Font == "" || (fontExists != nil && !fontExists(t.Font)) {
		return fieldErr("font", "unknown_font")
	}
	if !(t.Size >= 8 && t.Size <= 400) {
		return fieldErr("size", "out_of_range")
	}
	if !ValidColor(t.Color) {
		return fieldErr("color", "invalid_color")
	}
	if !ValidColor(t.Background) {
		return fieldErr("background", "invalid_color")
	}
	if !(t.Speed >= 10 && t.Speed <= 2000) {
		return fieldErr("speed", "out_of_range")
	}
	if t.Direction != "left" && t.Direction != "right" {
		return fieldErr("direction", "invalid_value")
	}
	if !(t.Height >= 16 && t.Height <= 540) {
		return fieldErr("height", "out_of_range")
	}
	if t.Position != "top" && t.Position != "bottom" {
		return fieldErr("position", "invalid_value")
	}
	return nil
}

func listHas(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
