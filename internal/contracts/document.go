package contracts

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Presentation is a set of slides sharing one design resolution (ARCHITECTURE §5.2).
// All geometry and typography values are design pixels.
type Presentation struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Width      int        `json:"width"`
	Height     int        `json:"height"`
	Typography Typography `json:"typography"`
	Slides     []Slide    `json:"slides"`
}

// Typography is the presentation-wide style for product lists and single products.
type Typography struct {
	Heading     TextStyle `json:"heading"`
	Subheading  TextStyle `json:"subheading"`
	Item        TextStyle `json:"item"`
	Description TextStyle `json:"description"`
	Price       TextStyle `json:"price"`
}

// DefaultTypography is the factory typography of new presentations: Montserrat, heading
// 34 px #ff8a00 bold, subheading 26 px #ffa733 bold, item 24 px, description 16 px – the
// same white as the item – and price 24 px bold right.
func DefaultTypography() Typography {
	style := func(size float64, color string, bold bool, align string, lineHeight float64) TextStyle {
		return TextStyle{Font: "Montserrat", Size: size, Color: color, Bold: bold, LineHeight: lineHeight, Align: align}
	}
	return Typography{
		Heading:     style(34, "#ff8a00", true, "left", 1.2),
		Subheading:  style(26, "#ffa733", true, "left", 1.2),
		Item:        style(24, "#ffffff", false, "left", 1.25),
		Description: style(16, "#ffffff", false, "left", 1.25),
		Price:       style(24, "#ffffff", true, "right", 1.25),
	}
}

// Fonts lists the font families of the typography.
func (t Typography) Fonts() []string {
	return []string{t.Heading.Font, t.Subheading.Font, t.Item.Font, t.Description.Font, t.Price.Font}
}

// TextStyle is a complete text style. Shadows never blur (Pi 3 rule).
type TextStyle struct {
	Font          string   `json:"font"`
	Size          float64  `json:"size"`
	Color         string   `json:"color"`
	Bold          bool     `json:"bold"`
	Italic        bool     `json:"italic"`
	Underline     bool     `json:"underline"`
	Strike        bool     `json:"strike"`
	Highlight     string   `json:"highlight,omitempty"`
	LetterSpacing float64  `json:"letterSpacing"`
	LineHeight    float64  `json:"lineHeight"`
	Align         string   `json:"align"`
	Uppercase     bool     `json:"uppercase,omitempty"`
	Outline       *Outline `json:"outline,omitempty"`
	Shadow        *Shadow  `json:"shadow,omitempty"`
}

// Outline draws a text stroke.
type Outline struct {
	Color string  `json:"color"`
	Width float64 `json:"width"`
}

// Shadow is a hard text shadow (no blur radius by design).
type Shadow struct {
	Color string  `json:"color"`
	DX    float64 `json:"dx"`
	DY    float64 `json:"dy"`
}

// Marks is a partial style applied to a text run or used as an override.
type Marks struct {
	Font          *string  `json:"font,omitempty"`
	Size          *float64 `json:"size,omitempty"`
	Color         *string  `json:"color,omitempty"`
	Bold          *bool    `json:"bold,omitempty"`
	Italic        *bool    `json:"italic,omitempty"`
	Underline     *bool    `json:"underline,omitempty"`
	Strike        *bool    `json:"strike,omitempty"`
	Highlight     *string  `json:"highlight,omitempty"`
	LetterSpacing *float64 `json:"letterSpacing,omitempty"`
}

// RichDoc is structured rich text: paragraphs of runs. Never HTML.
type RichDoc struct {
	Paragraphs []Paragraph `json:"paragraphs"`
}

// Paragraph of a rich text document.
type Paragraph struct {
	Align  string `json:"align,omitempty"`
	Bullet string `json:"bullet,omitempty"`
	Runs   []Run  `json:"runs"`
}

// Run is a piece of text with uniform marks.
type Run struct {
	Text  string `json:"text"`
	Marks *Marks `json:"marks,omitempty"`
}

// Slide is one scene.
type Slide struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Hidden     bool       `json:"hidden"`
	Duration   Duration   `json:"duration"`
	Background Background `json:"background"`
	Transition Transition `json:"transition"`
	Elements   []Element  `json:"elements"`
}

// Duration of a slide: fixed, or until the background video ends (MaxMS is the safety cap).
type Duration struct {
	Mode  string `json:"mode"` // "fixed" | "video"
	MS    int64  `json:"ms,omitempty"`
	MaxMS int64  `json:"maxMs,omitempty"`
}

// GradientStop of a linear gradient.
type GradientStop struct {
	Color string  `json:"color"`
	At    float64 `json:"at"` // 0..1
}

// Background of a slide.
type Background struct {
	Type    string         `json:"type"` // color | gradient | image | video
	Color   string         `json:"color"`
	Angle   float64        `json:"angle,omitempty"`
	Stops   []GradientStop `json:"stops,omitempty"`
	AssetID string         `json:"assetId,omitempty"`
	Fit     string         `json:"fit,omitempty"`
}

// Transition into a slide.
type Transition struct {
	Type      string `json:"type"` // cut | fade | slide | zoom
	MS        int64  `json:"ms"`
	Direction string `json:"direction,omitempty"` // left | right | up | down
}

// Stroke of a shape.
type Stroke struct {
	Color string  `json:"color"`
	Width float64 `json:"width"`
	Dash  string  `json:"dash"` // solid | dashed | dotted
}

// ProductSource selects products for a list.
type ProductSource struct {
	Kind string   `json:"kind"` // all | categories | products
	IDs  []string `json:"ids"`
}

// ProductFields toggles visible product fields.
type ProductFields struct {
	Name        bool `json:"name"`
	Quantity    bool `json:"quantity"`
	Description bool `json:"description"`
	Price       bool `json:"price"`
	// Variants: the product's variants below the name (absent = shown).
	Variants *bool `json:"variants,omitempty"`
}

// ProductOverrides adjust the presentation typography for one element.
type ProductOverrides struct {
	Heading     *Marks `json:"heading,omitempty"`
	Subheading  *Marks `json:"subheading,omitempty"`
	Item        *Marks `json:"item,omitempty"`
	Description *Marks `json:"description,omitempty"`
	Price       *Marks `json:"price,omitempty"`
}

// Element is a flat union of all element kinds; Validate enforces per-type fields.
type Element struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	Name     string  `json:"name"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	W        float64 `json:"w"`
	H        float64 `json:"h"`
	Rotation float64 `json:"rotation"`
	Opacity  float64 `json:"opacity"`
	Locked   bool    `json:"locked"`
	Hidden   bool    `json:"hidden"`
	Pinned   bool    `json:"pinned"`
	GroupID  string  `json:"groupId,omitempty"`

	// text, clock, ticker
	Doc        *RichDoc    `json:"doc,omitempty"`
	Style      *TextStyle  `json:"style,omitempty"`
	VAlign     string      `json:"valign,omitempty"`
	Padding    *[4]float64 `json:"padding,omitempty"`
	Fill       string      `json:"fill,omitempty"`
	AutoShrink bool        `json:"autoShrink,omitempty"`

	// image, video, audio
	AssetID string `json:"assetId,omitempty"`
	Fit     string `json:"fit,omitempty"`
	// Loop and Muted: absent = the default of the element type (a video loops and is muted,
	// audio plays once); false must survive a save, so they are pointers.
	Loop   *bool    `json:"loop,omitempty"`
	Muted  *bool    `json:"muted,omitempty"`
	Volume *float64 `json:"volume,omitempty"`

	// shape
	Shape  string  `json:"shape,omitempty"`
	Stroke *Stroke `json:"stroke,omitempty"`
	Radius float64 `json:"radius,omitempty"`

	// productList, product
	Source              *ProductSource    `json:"source,omitempty"`
	ProductID           string            `json:"productId,omitempty"`
	Columns             int               `json:"columns,omitempty"`
	Fields              *ProductFields    `json:"fields,omitempty"`
	CategoryHeadings    bool              `json:"categoryHeadings,omitempty"`
	SubcategoryHeadings bool              `json:"subcategoryHeadings,omitempty"`
	CategorySpace       bool              `json:"categorySpace,omitempty"`    // hidden category headings keep their space
	SubcategorySpace    bool              `json:"subcategorySpace,omitempty"` // hidden subcategory headings keep their space
	Scale               float64           `json:"scale,omitempty"`
	RowGap              float64           `json:"rowGap,omitempty"`
	ColumnGap           float64           `json:"columnGap,omitempty"`
	PriceAlign          string            `json:"priceAlign,omitempty"`
	Overrides           *ProductOverrides `json:"overrides,omitempty"`

	// clock
	Format string `json:"format,omitempty"`
	Date   string `json:"date,omitempty"`

	// ticker
	TickerSource string  `json:"tickerSource,omitempty"` // text | zone
	Text         string  `json:"text,omitempty"`
	Speed        float64 `json:"speed,omitempty"`
	Direction    string  `json:"direction,omitempty"`
}

// Element types.
const (
	ElText        = "text"
	ElImage       = "image"
	ElVideo       = "video"
	ElAudio       = "audio"
	ElShape       = "shape"
	ElProductList = "productList"
	ElProduct     = "product"
	ElClock       = "clock"
	ElTicker      = "ticker"
)

// Limits enforced by validation (Pi 3 performance rules are part of the format).
const (
	MaxSlides           = 200
	MaxElementsPerSlide = 200
	MaxTextRunes        = 20000
	MaxParagraphs       = 1000 // per text element
	MaxRunsPerParagraph = 500
	MaxSourceIDs        = 500 // ids of a product list source
	MaxDesignSize       = 7680
	MinDesignSize       = 16
	MaxSlideMS          = 24 * 60 * 60 * 1000
	MinSlideMS          = 1000
	DefaultVideoMaxMS   = 30 * 60 * 1000
)

// Resolver answers reference questions during validation.
type Resolver interface {
	AssetKind(id string) (kind string, ok bool) // image | video | audio | font
	FontExists(family string) bool
	ProductExists(id string) bool
	CategoryExists(id string) bool
}

var (
	colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?$`)
	idRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// ValidColor reports whether s is #rrggbb or #rrggbbaa.
func ValidColor(s string) bool { return colorRe.MatchString(s) }

type validator struct {
	res  Resolver
	errs []string
	w, h float64
}

func (v *validator) fail(path, code string) {
	if len(v.errs) < 50 {
		v.errs = append(v.errs, path+": "+code)
	}
}

func (v *validator) num(path string, x, lo, hi float64) {
	if math.IsNaN(x) || math.IsInf(x, 0) || x < lo || x > hi {
		v.fail(path, fmt.Sprintf("out_of_range[%g..%g]", lo, hi))
	}
}

func (v *validator) color(path, c string, optional bool) {
	if c == "" && optional {
		return
	}
	if !ValidColor(c) {
		v.fail(path, "invalid_color")
	}
}

func (v *validator) oneOf(path, s string, allowed ...string) {
	for _, a := range allowed {
		if s == a {
			return
		}
	}
	v.fail(path, "invalid_value")
}

func (v *validator) font(path, family string) {
	if family == "" || (v.res != nil && !v.res.FontExists(family)) {
		v.fail(path, "unknown_font")
	}
}

func (v *validator) asset(path, id, kind string) {
	if id == "" {
		v.fail(path, "required")
		return
	}
	if v.res == nil {
		return
	}
	k, ok := v.res.AssetKind(id)
	if !ok {
		v.fail(path, "unknown_asset")
	} else if k != kind {
		v.fail(path, "wrong_asset_kind")
	}
}

// DocumentError lists all validation problems of a document.
type DocumentError struct{ Problems []string }

func (e *DocumentError) Error() string {
	return "invalid presentation: " + strings.Join(e.Problems, "; ")
}

// Validate checks a presentation completely. res may be nil to skip reference checks.
func (p *Presentation) Validate(res Resolver) error {
	v := &validator{res: res, w: float64(p.Width), h: float64(p.Height)}
	if strings.TrimSpace(p.Name) == "" || utf8.RuneCountInString(p.Name) > 200 {
		v.fail("name", "invalid")
	}
	if p.Width < MinDesignSize || p.Width > MaxDesignSize || p.Height < MinDesignSize || p.Height > MaxDesignSize {
		v.fail("size", "out_of_range")
	}
	ty := p.Typography
	for name, s := range map[string]*TextStyle{"heading": &ty.Heading, "subheading": &ty.Subheading,
		"item": &ty.Item, "description": &ty.Description, "price": &ty.Price} {
		v.textStyle("typography."+name, s)
	}
	if len(p.Slides) > MaxSlides {
		v.fail("slides", "too_many")
	}
	seen := map[string]bool{}
	for i := range p.Slides {
		s := &p.Slides[i]
		path := fmt.Sprintf("slides[%d]", i)
		if !idRe.MatchString(s.ID) || seen[s.ID] {
			v.fail(path+".id", "invalid")
		}
		seen[s.ID] = true
		v.slide(path, s)
	}
	if len(v.errs) > 0 {
		return &DocumentError{Problems: v.errs}
	}
	return nil
}

func (v *validator) slide(path string, s *Slide) {
	if utf8.RuneCountInString(s.Name) > 200 {
		v.fail(path+".name", "too_long")
	}
	switch s.Duration.Mode {
	case "fixed":
		v.num(path+".duration.ms", float64(s.Duration.MS), MinSlideMS, MaxSlideMS)
	case "video":
		if s.Background.Type != "video" {
			v.fail(path+".duration.mode", "video_mode_needs_video_background")
		}
		if s.Duration.MaxMS != 0 {
			v.num(path+".duration.maxMs", float64(s.Duration.MaxMS), MinSlideMS, MaxSlideMS)
		}
	default:
		v.fail(path+".duration.mode", "invalid_value")
	}
	b := s.Background
	v.color(path+".background.color", b.Color, false)
	switch b.Type {
	case "color":
	case "gradient":
		if len(b.Stops) < 2 || len(b.Stops) > 5 {
			v.fail(path+".background.stops", "invalid")
		}
		for j, st := range b.Stops {
			v.color(fmt.Sprintf("%s.background.stops[%d].color", path, j), st.Color, false)
			v.num(fmt.Sprintf("%s.background.stops[%d].at", path, j), st.At, 0, 1)
		}
		v.num(path+".background.angle", b.Angle, -360, 360)
	case "image":
		v.asset(path+".background.assetId", b.AssetID, "image")
		v.oneOf(path+".background.fit", b.Fit, "contain", "cover", "fill")
	case "video":
		v.asset(path+".background.assetId", b.AssetID, "video")
		v.oneOf(path+".background.fit", b.Fit, "contain", "cover", "fill")
	default:
		v.fail(path+".background.type", "invalid_value")
	}
	t := s.Transition
	v.oneOf(path+".transition.type", t.Type, "cut", "fade", "slide", "zoom")
	if t.Type != "cut" {
		v.num(path+".transition.ms", float64(t.MS), 100, 5000)
	}
	if t.Type == "slide" {
		v.oneOf(path+".transition.direction", t.Direction, "left", "right", "up", "down")
	}
	if len(s.Elements) > MaxElementsPerSlide {
		v.fail(path+".elements", "too_many")
	}
	videos, audios := 0, 0
	ids := map[string]bool{}
	for j := range s.Elements {
		e := &s.Elements[j]
		ep := fmt.Sprintf("%s.elements[%d]", path, j)
		if !idRe.MatchString(e.ID) || ids[e.ID] {
			v.fail(ep+".id", "invalid")
		}
		ids[e.ID] = true
		switch e.Type {
		case ElVideo:
			videos++
		case ElAudio:
			audios++
		}
		v.element(ep, e)
	}
	if videos > 1 {
		v.fail(path+".elements", "max_one_video_element")
	}
	if audios > 1 {
		v.fail(path+".elements", "max_one_audio_element")
	}
}

func (v *validator) textStyle(path string, s *TextStyle) {
	v.font(path+".font", s.Font)
	v.num(path+".size", s.Size, 1, 2000)
	v.color(path+".color", s.Color, false)
	v.color(path+".highlight", s.Highlight, true)
	v.num(path+".letterSpacing", s.LetterSpacing, -100, 1000)
	v.num(path+".lineHeight", s.LineHeight, 0.5, 5)
	v.oneOf(path+".align", s.Align, "left", "center", "right", "justify")
	if s.Outline != nil {
		v.color(path+".outline.color", s.Outline.Color, false)
		v.num(path+".outline.width", s.Outline.Width, 0, 100)
	}
	if s.Shadow != nil {
		v.color(path+".shadow.color", s.Shadow.Color, false)
		v.num(path+".shadow.dx", s.Shadow.DX, -200, 200)
		v.num(path+".shadow.dy", s.Shadow.DY, -200, 200)
	}
}

func (v *validator) marks(path string, m *Marks) {
	if m == nil {
		return
	}
	if m.Font != nil {
		v.font(path+".font", *m.Font)
	}
	if m.Size != nil {
		v.num(path+".size", *m.Size, 1, 2000)
	}
	if m.Color != nil {
		v.color(path+".color", *m.Color, false)
	}
	if m.Highlight != nil {
		v.color(path+".highlight", *m.Highlight, true)
	}
	if m.LetterSpacing != nil {
		v.num(path+".letterSpacing", *m.LetterSpacing, -100, 1000)
	}
}

func (v *validator) element(path string, e *Element) {
	if utf8.RuneCountInString(e.Name) > 200 {
		v.fail(path+".name", "too_long")
	}
	maxW, maxH := v.w*4, v.h*4
	v.num(path+".x", e.X, -maxW, maxW)
	v.num(path+".y", e.Y, -maxH, maxH)
	v.num(path+".w", e.W, 1, maxW)
	v.num(path+".h", e.H, 1, maxH)
	v.num(path+".rotation", e.Rotation, -360, 360)
	v.num(path+".opacity", e.Opacity, 0, 1)
	if e.GroupID != "" && !idRe.MatchString(e.GroupID) {
		v.fail(path+".groupId", "invalid")
	}
	switch e.Type {
	case ElText:
		if e.Doc == nil || e.Style == nil {
			v.fail(path, "doc_and_style_required")
			return
		}
		v.textStyle(path+".style", e.Style)
		v.oneOf(path+".valign", e.VAlign, "top", "middle", "bottom")
		v.color(path+".fill", e.Fill, true)
		if e.Padding != nil {
			for k, p := range e.Padding {
				v.num(fmt.Sprintf("%s.padding[%d]", path, k), p, 0, 1000)
			}
		}
		if len(e.Doc.Paragraphs) > MaxParagraphs {
			v.fail(path+".doc.paragraphs", "too_many")
			return
		}
		runes := 0
		for pi, para := range e.Doc.Paragraphs {
			pp := fmt.Sprintf("%s.doc.paragraphs[%d]", path, pi)
			if len(para.Runs) > MaxRunsPerParagraph {
				v.fail(pp+".runs", "too_many")
				continue
			}
			if para.Align != "" {
				v.oneOf(pp+".align", para.Align, "left", "center", "right", "justify")
			}
			if para.Bullet != "" {
				v.oneOf(pp+".bullet", para.Bullet, "none", "disc", "number")
			}
			for ri := range para.Runs {
				runes += utf8.RuneCountInString(para.Runs[ri].Text)
				v.marks(fmt.Sprintf("%s.runs[%d].marks", pp, ri), para.Runs[ri].Marks)
			}
		}
		if runes > MaxTextRunes {
			v.fail(path+".doc", "too_long")
		}
	case ElImage:
		v.asset(path+".assetId", e.AssetID, "image")
		v.oneOf(path+".fit", e.Fit, "contain", "cover", "fill")
	case ElVideo:
		v.asset(path+".assetId", e.AssetID, "video")
		v.oneOf(path+".fit", e.Fit, "contain", "cover", "fill")
	case ElAudio:
		v.asset(path+".assetId", e.AssetID, "audio")
		if e.Volume == nil {
			v.fail(path+".volume", "required")
		} else {
			v.num(path+".volume", *e.Volume, 0, 1)
		}
	case ElShape:
		v.oneOf(path+".shape", e.Shape, "rect", "ellipse", "line", "arrow")
		v.color(path+".fill", e.Fill, true)
		if e.Stroke == nil {
			v.fail(path+".stroke", "required")
		} else {
			v.color(path+".stroke.color", e.Stroke.Color, false)
			v.num(path+".stroke.width", e.Stroke.Width, 0, 500)
			v.oneOf(path+".stroke.dash", e.Stroke.Dash, "solid", "dashed", "dotted")
		}
		v.num(path+".radius", e.Radius, 0, 10000)
	case ElProductList:
		if e.Source == nil || e.Fields == nil {
			v.fail(path, "source_and_fields_required")
			return
		}
		v.oneOf(path+".source.kind", e.Source.Kind, "all", "categories", "products")
		if e.Source.Kind != "all" && len(e.Source.IDs) == 0 {
			v.fail(path+".source.ids", "required")
		}
		if len(e.Source.IDs) > MaxSourceIDs {
			v.fail(path+".source.ids", "too_many")
			return
		}
		for k, id := range e.Source.IDs {
			ok := true
			if v.res != nil {
				switch e.Source.Kind {
				case "categories":
					ok = v.res.CategoryExists(id)
				case "products":
					ok = v.res.ProductExists(id)
				}
			}
			if !ok {
				v.fail(fmt.Sprintf("%s.source.ids[%d]", path, k), "unknown_reference")
			}
		}
		v.num(path+".columns", float64(e.Columns), 1, 4)
		v.num(path+".scale", e.Scale, 0.25, 4)
		v.num(path+".rowGap", e.RowGap, 0, 1000)
		v.num(path+".columnGap", e.ColumnGap, 0, 1000)
		v.oneOf(path+".priceAlign", e.PriceAlign, "left", "right")
		v.productOverrides(path, e.Overrides)
	case ElProduct:
		if e.Fields == nil {
			v.fail(path+".fields", "required")
		}
		if e.ProductID == "" || (v.res != nil && !v.res.ProductExists(e.ProductID)) {
			v.fail(path+".productId", "unknown_reference")
		}
		v.num(path+".scale", e.Scale, 0.25, 4)
		v.oneOf(path+".priceAlign", e.PriceAlign, "left", "right")
		v.productOverrides(path, e.Overrides)
	case ElClock:
		if e.Style == nil {
			v.fail(path+".style", "required")
			return
		}
		v.textStyle(path+".style", e.Style)
		v.oneOf(path+".format", e.Format, "HH:mm", "HH:mm:ss", "h:mm a", "h:mm:ss a")
		v.oneOf(path+".date", e.Date, "none", "short", "long")
		v.oneOf(path+".valign", e.VAlign, "top", "middle", "bottom")
	case ElTicker:
		if e.Style == nil {
			v.fail(path+".style", "required")
			return
		}
		v.textStyle(path+".style", e.Style)
		v.oneOf(path+".tickerSource", e.TickerSource, "text", "zone")
		if utf8.RuneCountInString(e.Text) > 2000 {
			v.fail(path+".text", "too_long")
		}
		v.color(path+".fill", e.Fill, true)
		v.num(path+".speed", e.Speed, 10, 2000)
		v.oneOf(path+".direction", e.Direction, "left", "right")
	default:
		v.fail(path+".type", "invalid_value")
	}
}

func (v *validator) productOverrides(path string, o *ProductOverrides) {
	if o == nil {
		return
	}
	v.marks(path+".overrides.heading", o.Heading)
	v.marks(path+".overrides.subheading", o.Subheading)
	v.marks(path+".overrides.item", o.Item)
	v.marks(path+".overrides.description", o.Description)
	v.marks(path+".overrides.price", o.Price)
}

// References collects asset IDs and font families used by a presentation.
func (p *Presentation) References() (assets []string, fonts []string) {
	seenA, seenF := map[string]bool{}, map[string]bool{}
	addA := func(id string) {
		if id != "" && !seenA[id] {
			seenA[id] = true
			assets = append(assets, id)
		}
	}
	addF := func(f string) {
		if f != "" && !seenF[f] {
			seenF[f] = true
			fonts = append(fonts, f)
		}
	}
	addMarks := func(m *Marks) {
		if m != nil && m.Font != nil {
			addF(*m.Font)
		}
	}
	ty := p.Typography
	for _, s := range []TextStyle{ty.Heading, ty.Subheading, ty.Item, ty.Description, ty.Price} {
		addF(s.Font)
	}
	for _, s := range p.Slides {
		addA(s.Background.AssetID)
		for _, e := range s.Elements {
			addA(e.AssetID)
			if e.Style != nil {
				addF(e.Style.Font)
			}
			if e.Doc != nil {
				for _, para := range e.Doc.Paragraphs {
					for _, r := range para.Runs {
						addMarks(r.Marks)
					}
				}
			}
			if o := e.Overrides; o != nil {
				for _, m := range []*Marks{o.Heading, o.Subheading, o.Item, o.Description, o.Price} {
					addMarks(m)
				}
			}
		}
	}
	return assets, fonts
}
