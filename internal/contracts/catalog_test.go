package contracts

import (
	"errors"
	"testing"
)

func TestQuantityValidate(t *testing.T) {
	good := []Quantity{{"0.33", "l"}, {"500", "ml"}, {"1", "pcs"}, {"0.5", "kg"}, {"12.345", "cl"}, {"250", "g"}}
	for _, q := range good {
		if err := q.Validate(); err != nil {
			t.Errorf("%v should be valid: %v", q, err)
		}
	}
	bad := []Quantity{{"", "l"}, {"0", "l"}, {"0.00", "ml"}, {"0,33", "l"}, {".5", "l"}, {"1.", "l"}, {"-1", "l"}, {"1e3", "l"},
		{"00.5", "l"}, {"0.3333", "l"}, {"1234567", "ml"}, {"0.33", "oz"}, {"0.33", ""}, {" 1", "l"}}
	for _, q := range bad {
		if err := q.Validate(); err == nil {
			t.Errorf("%v should be invalid", q)
		}
	}
}

func TestMoneyValidate(t *testing.T) {
	if err := (Money{Amount: 0, Currency: "EUR"}).Validate("price"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []Money{{-1, "EUR"}, {100, "eur"}, {100, "EU"}, {100, ""}, {MaxMoneyAmount + 1, "EUR"}} {
		if err := m.Validate("price"); err == nil {
			t.Errorf("%v should be invalid", m)
		}
	}
}

type fakePlaylistRes struct{}

func (fakePlaylistRes) PresentationExists(id string) bool { return id == "pres" }
func (fakePlaylistRes) SlideExists(p, s string) bool      { return p == "pres" && s == "slide" }
func (fakePlaylistRes) MediaKind(id string) (string, bool) {
	switch id {
	case "img":
		return "image", true
	case "vid":
		return "video", true
	case "aud":
		return "audio", true
	}
	return "", false
}

func fieldOf(t *testing.T, err error) (string, string) {
	t.Helper()
	var fe *FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("expected FieldError, got %v", err)
	}
	return fe.Field, fe.Code
}

func TestPlaylistValidate(t *testing.T) {
	ok := Playlist{ID: "p", Name: "Main", Items: []PlaylistItem{
		{ID: "a", Kind: ItemPresentation, RefID: "pres", Play: Play{Mode: PlayDuration, MS: 10000}},
		{ID: "b", Kind: ItemSlide, RefID: "slide", PresentationID: "pres", Play: Play{Mode: PlayRepeat, Count: 2}},
		{ID: "c", Kind: ItemMedia, RefID: "img", Play: Play{Mode: PlayDuration, MS: 5000}, Fit: "cover"},
		{ID: "d", Kind: ItemMedia, RefID: "vid", Play: Play{Mode: PlayRepeat, Count: 1}},
	}}
	if err := ok.Validate(fakePlaylistRes{}); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		mutate func(p *Playlist)
		field  string
		code   string
	}{
		"both duration and repeat": {func(p *Playlist) { p.Items[0].Play = Play{Mode: PlayDuration, MS: 5000, Count: 2} }, "items[0].play", "duration_xor_repeat"},
		"repeat with ms":           {func(p *Playlist) { p.Items[1].Play = Play{Mode: PlayRepeat, MS: 5000, Count: 2} }, "items[1].play", "duration_xor_repeat"},
		"neither":                  {func(p *Playlist) { p.Items[0].Play = Play{Mode: PlayDuration} }, "items[0].play.ms", "out_of_range"},
		"unknown mode":             {func(p *Playlist) { p.Items[0].Play.Mode = "loop" }, "items[0].play.mode", "invalid_value"},
		"zero repeat":              {func(p *Playlist) { p.Items[1].Play = Play{Mode: PlayRepeat} }, "items[1].play.count", "out_of_range"},
		"image repeat":             {func(p *Playlist) { p.Items[2].Play = Play{Mode: PlayRepeat, Count: 2} }, "items[2].play.mode", "image_requires_duration"},
		"audio media":              {func(p *Playlist) { p.Items[3].RefID = "aud" }, "items[3].refId", "wrong_asset_kind"},
		"unknown presentation":     {func(p *Playlist) { p.Items[0].RefID = "nope" }, "items[0].refId", "unknown_reference"},
		"unknown slide":            {func(p *Playlist) { p.Items[1].RefID = "nope" }, "items[1].refId", "unknown_reference"},
		"slide without pres":       {func(p *Playlist) { p.Items[1].PresentationID = "" }, "items[1].presentationId", "required"},
		"fit on presentation":      {func(p *Playlist) { p.Items[0].Fit = "cover" }, "items[0].fit", "media_only"},
		"bad fit":                  {func(p *Playlist) { p.Items[2].Fit = "zoom" }, "items[2].fit", "invalid_value"},
		"duplicate item id":        {func(p *Playlist) { p.Items[1].ID = "a" }, "items[1].id", "invalid"},
		"bad kind":                 {func(p *Playlist) { p.Items[0].Kind = "url" }, "items[0].kind", "invalid_value"},
		"empty name":               {func(p *Playlist) { p.Name = " " }, "name", "invalid"},
	}
	for name, tc := range cases {
		p := ok
		p.Items = append([]PlaylistItem(nil), ok.Items...)
		tc.mutate(&p)
		field, code := fieldOf(t, p.Validate(fakePlaylistRes{}))
		if field != tc.field || code != tc.code {
			t.Errorf("%s: got %s/%s, want %s/%s", name, field, code, tc.field, tc.code)
		}
	}
	if err := (&Playlist{ID: "p", Name: "x", Items: nil}).Validate(nil); err != nil {
		t.Errorf("empty playlist must be valid: %v", err)
	}
}

func TestTickerTemplateValidate(t *testing.T) {
	fonts := func(f string) bool { return f == "Montserrat" }
	d := DefaultTickerTemplate()
	if err := d.Validate(fonts); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(t *TickerTemplate){
		"unknown font": func(t *TickerTemplate) { t.Font = "Arial" },
		"empty font":   func(t *TickerTemplate) { t.Font = "" },
		"tiny size":    func(t *TickerTemplate) { t.Size = 2 },
		"bad color":    func(t *TickerTemplate) { t.Color = "white" },
		"bad bg":       func(t *TickerTemplate) { t.Background = "#12" },
		"slow":         func(t *TickerTemplate) { t.Speed = 1 },
		"direction":    func(t *TickerTemplate) { t.Direction = "up" },
		"height":       func(t *TickerTemplate) { t.Height = 5000 },
		"position":     func(t *TickerTemplate) { t.Position = "middle" },
	}
	for name, mutate := range cases {
		x := d
		mutate(&x)
		if err := x.Validate(fonts); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// Playlists for several screens (tracks).
func TestPlaylistTracksValidate(t *testing.T) {
	ok := Playlist{ID: "p", Name: "Wall", Screens: 3, Items: []PlaylistItem{
		{ID: "a", Kind: ItemSlide, RefID: "slide", PresentationID: "pres", Play: Play{Mode: PlayDuration, MS: 10000},
			Cells: []*PlaylistCell{{Kind: ItemMedia, RefID: "img", Fit: "cover"}, nil}},
		{ID: "b", Kind: ItemMedia, RefID: "vid", Play: Play{Mode: PlayDuration, MS: 8000},
			Cells: []*PlaylistCell{nil, {Kind: ItemSlide, RefID: "slide", PresentationID: "pres"}}},
	}}
	if err := ok.Validate(fakePlaylistRes{}); err != nil {
		t.Fatal(err)
	}
	if refs := ok.Refs(); len(refs) != 4 || refs[1].Path != "items[0].cells[0]" || refs[3].Path != "items[1].cells[1]" {
		t.Fatalf("refs: %+v", refs)
	}
	cases := map[string]struct {
		mutate func(p *Playlist)
		field  string
		code   string
	}{
		"presentation step": {func(p *Playlist) {
			p.Items[0] = PlaylistItem{ID: "a", Kind: ItemPresentation, RefID: "pres", Play: Play{Mode: PlayDuration, MS: 5000}}
		}, "items[0].kind", "single_track_only"},
		"repeat step":       {func(p *Playlist) { p.Items[1].Play = Play{Mode: PlayRepeat, Count: 1} }, "items[1].play.mode", "tracks_require_duration"},
		"too many cells":    {func(p *Playlist) { p.Items[0].Cells = []*PlaylistCell{nil, nil, nil} }, "items[0].cells", "too_many"},
		"unknown cell":      {func(p *Playlist) { p.Items[0].Cells = []*PlaylistCell{{Kind: ItemMedia, RefID: "nope"}} }, "items[0].cells[0].refId", "unknown_reference"},
		"audio cell":        {func(p *Playlist) { p.Items[0].Cells = []*PlaylistCell{{Kind: ItemMedia, RefID: "aud"}} }, "items[0].cells[0].refId", "wrong_asset_kind"},
		"cell presentation": {func(p *Playlist) { p.Items[0].Cells = []*PlaylistCell{{Kind: ItemPresentation, RefID: "pres"}} }, "items[0].cells[0].kind", "invalid_value"},
		"slide cell fit": {func(p *Playlist) {
			p.Items[0].Cells = []*PlaylistCell{{Kind: ItemSlide, RefID: "slide", PresentationID: "pres", Fit: "cover"}}
		}, "items[0].cells[0].fit", "media_only"},
		"too many screens": {func(p *Playlist) { p.Screens = MaxPlaylistScreens + 1 }, "screens", "out_of_range"},
		"cells single":     {func(p *Playlist) { p.Screens = 1 }, "items[0].cells", "must_be_empty"},
	}
	for name, tc := range cases {
		p := ok
		p.Items = append([]PlaylistItem(nil), ok.Items...)
		tc.mutate(&p)
		field, code := fieldOf(t, p.Validate(fakePlaylistRes{}))
		if field != tc.field || code != tc.code {
			t.Errorf("%s: got %s/%s, want %s/%s", name, field, code, tc.field, tc.code)
		}
	}
}
