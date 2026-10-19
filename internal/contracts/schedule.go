package contracts

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Money is an amount in minor units (e.g. cents) plus ISO 4217 currency code.
type Money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// MaxRuleNameRunes bounds the name of a schedule rule.
const MaxRuleNameRunes = 200

// Rule scopes.
const (
	ScopeZone    = "zone"
	ScopeGlobal  = "global"
	ScopeDefault = "default"
)

// Window is a recurring weekly time window. Days are ISO weekdays (1=Mon..7=Sun) of the
// day the window starts. End <= Start means the window crosses midnight; End == Start
// means 24 hours. From and Until limit it to a period of dates (YYYY-MM-DD in the server time
// zone, inclusive, "" = open): an occurrence counts when it starts on a day of the period –
// a window from 22:00 to 02:00 on the 7th runs until 02:00 on the 8th.
type Window struct {
	Days  []int  `json:"days"`
	Start string `json:"start"`
	End   string `json:"end"`
	From  string `json:"from,omitempty"`
	Until string `json:"until,omitempty"`
}

// DayNumber parses a date "YYYY-MM-DD" into days since 1970-01-01.
func DayNumber(s string) (int, error) {
	if len(s) != 10 {
		return 0, fmt.Errorf("invalid date %q (expected YYYY-MM-DD)", s)
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return 0, fmt.Errorf("invalid date %q (expected YYYY-MM-DD)", s)
	}
	return int(t.Unix() / 86400), nil
}

// ISOWeekdayOfDay is the ISO weekday (1=Mon..7=Sun) of a day number (1970-01-01 = Thursday).
func ISOWeekdayOfDay(day int) int {
	return ((day%7+7)%7+3)%7 + 1
}

// PriceOverride replaces the base price of one product, or of every product of a category
// and its subcategories (exactly one of ProductID and CategoryID), while the rule is active.
// Within a rule a product price wins over a subcategory price, which wins over a category
// price. Deployments carry product prices only (schedule.ExpandPrices).
type PriceOverride struct {
	ProductID  string `json:"productId,omitempty"`
	CategoryID string `json:"categoryId,omitempty"`
	Price      Money  `json:"price"`
}

// MaxTickerRunes bounds a ticker text.
const MaxTickerRunes = 2000

// RuleTicker is a ticker shown over the content of every screen of the zone while the
// rule is active, e.g. "Happy Hour" without a ticker in the presentation.
// Hide instead hides the ticker of a lower rule while this rule is active.
type RuleTicker struct {
	Hide bool   `json:"hide,omitempty"`
	Text string `json:"text"`
	// TemplateID chooses the ticker template; "" = the template of the zone (or the default).
	TemplateID string `json:"templateId,omitempty"`
	// Template is the resolved style, set in deployment manifests only.
	Template *TickerTemplate `json:"template,omitempty"`
}

// NoGroupID targets the screens of a zone that are in no display group.
const NoGroupID = "ungrouped"

// GroupPlaylist sets the playlist of one display group of the zone (or NoGroupID).
type GroupPlaylist struct {
	GroupID    string `json:"groupId"`
	PlaylistID string `json:"playlistId"`
}

// Rule is a time schedule rule (see ARCHITECTURE §5.4). A rule changes only what it sets:
// the playlist of all screens, the playlist of single display groups,
// prices, showing or hiding the ticker; everything else stays as lower rules have it.
type Rule struct {
	ID         string  `json:"id"`
	Revision   int64   `json:"revision"`
	Name       string  `json:"name"`
	Scope      string  `json:"scope"`
	ZoneID     string  `json:"zoneId,omitempty"`
	Window     *Window `json:"window,omitempty"`
	PlaylistID string  `json:"playlistId,omitempty"`
	// GroupPlaylists: playlists of single display groups (zone and default rules only).
	GroupPlaylists []GroupPlaylist `json:"groupPlaylists,omitempty"`
	Prices         []PriceOverride `json:"prices"`
	Ticker         *RuleTicker     `json:"ticker,omitempty"`
	Enabled        bool            `json:"enabled"`
}

// validatePeriod checks From and Until: valid dates, Until not before From, and – for a
// period shorter than a week – at least one of the weekdays inside it (else the window
// would never apply).
func (w *Window) validatePeriod() error {
	from, until := -1, -1
	if w.From != "" {
		d, err := DayNumber(w.From)
		if err != nil {
			return fieldErr("window.from", "invalid")
		}
		from = d
	}
	if w.Until != "" {
		d, err := DayNumber(w.Until)
		if err != nil {
			return fieldErr("window.until", "invalid")
		}
		until = d
	}
	if from < 0 || until < 0 {
		return nil
	}
	if until < from {
		return fieldErr("window.until", "before_from")
	}
	if until-from < 6 {
		for d := from; d <= until; d++ {
			for _, x := range w.Days {
				if x == ISOWeekdayOfDay(d) {
					return nil
				}
			}
		}
		return fieldErr("window.days", "no_day_in_range")
	}
	return nil
}

// ParseHHMM parses "HH:MM" (00:00..23:59) into minutes since midnight. Exactly two ASCII
// digits are required on each side: no signs, spaces or other number forms.
func ParseHHMM(s string) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok || !twoDigits(h) || !twoDigits(m) {
		return 0, fmt.Errorf("invalid time %q (expected HH:MM)", s)
	}
	hh := int(h[0]-'0')*10 + int(h[1]-'0')
	mm := int(m[0]-'0')*10 + int(m[1]-'0')
	if hh > 23 || mm > 59 {
		return 0, fmt.Errorf("invalid time %q (expected HH:MM)", s)
	}
	return hh*60 + mm, nil
}

func twoDigits(s string) bool {
	return len(s) == 2 && s[0] >= '0' && s[0] <= '9' && s[1] >= '0' && s[1] <= '9'
}

// Validate checks the structural validity of a rule (not overlaps, not references).
func (r *Rule) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fieldErr("name", "required")
	}
	if utf8.RuneCountInString(r.Name) > MaxRuleNameRunes {
		return fieldErr("name", "too_long")
	}
	switch r.Scope {
	case ScopeZone:
		if r.ZoneID == "" {
			return fieldErr("zoneId", "required")
		}
		if r.Window == nil {
			return fieldErr("window", "required")
		}
	case ScopeGlobal:
		if r.ZoneID != "" {
			return fieldErr("zoneId", "must_be_empty")
		}
		if r.Window == nil {
			return fieldErr("window", "required")
		}
	case ScopeDefault:
		if r.ZoneID == "" {
			return fieldErr("zoneId", "required")
		}
		if r.Window != nil {
			return fieldErr("window", "must_be_empty")
		}
	default:
		return fieldErr("scope", "invalid")
	}
	if r.Window != nil {
		if len(r.Window.Days) == 0 {
			return fieldErr("window.days", "required")
		}
		seen := map[int]bool{}
		for _, d := range r.Window.Days {
			if d < 1 || d > 7 || seen[d] {
				return fieldErr("window.days", "invalid")
			}
			seen[d] = true
		}
		if _, err := ParseHHMM(r.Window.Start); err != nil {
			return fieldErr("window.start", "invalid")
		}
		if _, err := ParseHHMM(r.Window.End); err != nil {
			return fieldErr("window.end", "invalid")
		}
		if err := r.Window.validatePeriod(); err != nil {
			return err
		}
	}
	if r.PlaylistID == "" && len(r.GroupPlaylists) == 0 && len(r.Prices) == 0 && r.Ticker == nil {
		return fieldErr("playlistId", "playlist_prices_or_ticker_required")
	}
	if len(r.GroupPlaylists) > 0 && r.Scope == ScopeGlobal {
		return fieldErr("groupPlaylists", "must_be_empty") // groups belong to a zone
	}
	groups := map[string]bool{}
	for _, g := range r.GroupPlaylists {
		if g.GroupID == "" || g.PlaylistID == "" || groups[g.GroupID] {
			return fieldErr("groupPlaylists", "invalid")
		}
		groups[g.GroupID] = true
	}
	if r.Ticker != nil {
		if r.Ticker.Hide {
			if r.Ticker.Text != "" || r.Ticker.TemplateID != "" {
				return fieldErr("ticker.text", "must_be_empty")
			}
		} else {
			if strings.TrimSpace(r.Ticker.Text) == "" {
				return fieldErr("ticker.text", "required")
			}
			if utf8.RuneCountInString(r.Ticker.Text) > MaxTickerRunes {
				return fieldErr("ticker.text", "too_long")
			}
		}
	}
	seen := map[string]bool{}
	for i, p := range r.Prices {
		key := "p:" + p.ProductID
		if p.CategoryID != "" {
			key = "c:" + p.CategoryID
		}
		if (p.ProductID == "") == (p.CategoryID == "") || seen[key] {
			return fieldErr("prices", "invalid")
		}
		seen[key] = true
		if err := p.Price.Validate(fmt.Sprintf("prices[%d].price", i)); err != nil {
			return err
		}
	}
	return nil
}

// FieldError describes an invalid input field; Code is translated by the UI.
type FieldError struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Code }

func fieldErr(field, code string) error { return &FieldError{Field: field, Code: code} }
