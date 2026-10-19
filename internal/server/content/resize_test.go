package content

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"

	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
)

func loadFixture(t *testing.T) contracts.Presentation {
	t.Helper()
	raw, err := os.ReadFile("../../../testdata/contracts/presentation-valid.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var p contracts.Presentation
	if err := dec.Decode(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.011 }

func TestResizeUniform(t *testing.T) {
	p := loadFixture(t) // 1280x720 → 1920x1080: factor 1.5 on both axes
	if err := Resize(&p, 1920, 1080); err != nil {
		t.Fatal(err)
	}
	if p.Width != 1920 || p.Height != 1080 {
		t.Fatalf("size %dx%d", p.Width, p.Height)
	}
	logo := p.Slides[0].Elements[0] // x1100 y20 w160 h80
	if !near(logo.X, 1650) || !near(logo.Y, 30) || !near(logo.W, 240) || !near(logo.H, 120) {
		t.Errorf("logo %+v", logo)
	}
	title := p.Slides[0].Elements[1] // text, size 60, bold run
	if !near(title.Style.Size, 90) {
		t.Errorf("title font size %v", title.Style.Size)
	}
	if !near(p.Typography.Heading.Size, 51) || !near(p.Typography.Description.Size, 24) {
		t.Errorf("typography %v %v", p.Typography.Heading.Size, p.Typography.Description.Size)
	}
	line := p.Slides[0].Elements[3]
	if !near(line.Stroke.Width, 4.5) {
		t.Errorf("stroke width %v", line.Stroke.Width)
	}
	box := p.Slides[1].Elements[2]
	if !near(box.Radius, 24) || !near(box.Stroke.Width, 3) {
		t.Errorf("box radius/stroke %v %v", box.Radius, box.Stroke.Width)
	}
	list := p.Slides[0].Elements[2]
	if list.Scale != 1 || list.Columns != 2 {
		t.Errorf("product list scale/columns must not change: %v %v", list.Scale, list.Columns)
	}
	if !near(list.ColumnGap, 60) || !near(list.RowGap, 9) {
		t.Errorf("gaps %v %v", list.RowGap, list.ColumnGap)
	}
	tick := p.Slides[0].Elements[5]
	if !near(tick.Speed, 180) {
		t.Errorf("ticker speed %v", tick.Speed)
	}
	if err := p.Validate(nil); err != nil {
		t.Fatalf("resized document must stay valid: %v", err)
	}
}

func TestResizeAspectChange(t *testing.T) {
	p := loadFixture(t)
	// 1280x720 → 720x1280: fx = 0.5625, fy = 1.7778, smaller factor s = 0.5625.
	if err := Resize(&p, 720, 1280); err != nil {
		t.Fatal(err)
	}
	logo := p.Slides[0].Elements[0] // x1100 y20 w160 h80
	if !near(logo.X, 618.75) {
		t.Errorf("x scales by the width factor: %v", logo.X)
	}
	if !near(logo.Y, 35.56) {
		t.Errorf("y scales by the height factor: %v", logo.Y)
	}
	if !near(logo.W, 90) || !near(logo.H, 45) {
		t.Errorf("sizes scale by the smaller factor: %v x %v", logo.W, logo.H)
	}
	title := p.Slides[0].Elements[1]
	if !near(title.Style.Size, 33.75) {
		t.Errorf("font sizes scale by the smaller factor: %v", title.Style.Size)
	}
	// Run marks scale too.
	if m := title.Doc.Paragraphs[0].Runs[1].Marks; m == nil {
		t.Fatal("marks lost")
	}
}

func TestResizeRoundTripAndLimits(t *testing.T) {
	p := loadFixture(t)
	if err := Resize(&p, 2560, 1440); err != nil {
		t.Fatal(err)
	}
	if err := Resize(&p, 1280, 720); err != nil {
		t.Fatal(err)
	}
	orig := loadFixture(t)
	if !near(p.Slides[0].Elements[0].X, orig.Slides[0].Elements[0].X) || !near(p.Slides[0].Elements[1].Style.Size, orig.Slides[0].Elements[1].Style.Size) {
		t.Error("scaling up and back must restore values")
	}
	if err := Resize(&p, 8000, 720); err == nil {
		t.Error("size beyond the limit must be rejected")
	}
	if err := Resize(&p, 10, 720); err == nil {
		t.Error("size below the limit must be rejected")
	}
	// Tiny targets clamp sizes at 1 instead of producing invalid documents.
	if err := Resize(&p, 16, 16); err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(nil); err != nil {
		t.Errorf("extreme shrink must still validate: %v", err)
	}
}

func TestNewDocumentDefaults(t *testing.T) {
	d := newDocument("id", "Menu", 1280, 720, core.DefaultSettings())
	if err := d.Validate(nil); err != nil {
		t.Fatal(err)
	}
	ty := d.Typography
	if ty.Heading.Size != 34 || ty.Heading.Color != "#ff8a00" || !ty.Heading.Bold ||
		ty.Subheading.Size != 26 || ty.Subheading.Color != "#ffa733" || !ty.Subheading.Bold ||
		ty.Item.Size != 24 || ty.Item.Color != "#ffffff" ||
		ty.Description.Size != 16 || ty.Description.Color != ty.Item.Color ||
		ty.Price.Size != 24 || !ty.Price.Bold || ty.Price.Align != "right" {
		t.Errorf("typography defaults: %+v", ty)
	}
	for _, s := range []contracts.TextStyle{ty.Heading, ty.Subheading, ty.Item, ty.Description, ty.Price} {
		if s.Font != "Montserrat" {
			t.Errorf("font %q", s.Font)
		}
	}
	s := d.Slides[0]
	if len(d.Slides) != 1 || s.Background.Type != "color" || s.Background.Color != "#000000" ||
		s.Transition.Type != "fade" || s.Transition.MS != 600 || s.Duration.Mode != "fixed" || s.Duration.MS != 10000 {
		t.Errorf("slide defaults: %+v", s)
	}
}

// TestResizeClampsEveryMeasureToItsRange sets every scaled measure to the edge of its
// validation range and resizes far up and far down: the result must always validate
// (a resize never produces a 422).
func TestResizeClampsEveryMeasureToItsRange(t *testing.T) {
	edge := func() contracts.Presentation {
		p := loadFixture(t)
		ls, size := 1000.0, 2000.0
		for _, st := range []*contracts.TextStyle{&p.Typography.Heading, p.Slides[0].Elements[1].Style, p.Slides[0].Elements[5].Style} {
			st.LetterSpacing = 1000
			st.Outline = &contracts.Outline{Color: "#000000", Width: 100}
			st.Shadow = &contracts.Shadow{Color: "#000000", DX: 200, DY: -200}
		}
		text := &p.Slides[0].Elements[1]
		text.Padding = &[4]float64{1000, 1000, 0, 1000}
		text.Doc.Paragraphs[0].Runs[0].Marks = &contracts.Marks{LetterSpacing: &ls, Size: &size}
		list := &p.Slides[0].Elements[2]
		list.RowGap, list.ColumnGap = 1000, 1000
		list.Overrides = &contracts.ProductOverrides{Item: &contracts.Marks{LetterSpacing: &ls}}
		p.Slides[0].Elements[3].Stroke.Width = 500
		box := &p.Slides[1].Elements[2]
		box.Radius, box.Stroke.Width = 10000, 500
		p.Slides[0].Elements[5].Speed = 2000
		if err := p.Validate(nil); err != nil {
			t.Fatalf("edge document must be valid before the resize: %v", err)
		}
		return p
	}
	for _, size := range [][2]int{{7680, 7680}, {7680, 16}, {16, 7680}, {16, 16}} {
		p := edge()
		if err := Resize(&p, size[0], size[1]); err != nil {
			t.Fatal(err)
		}
		if err := p.Validate(nil); err != nil {
			t.Errorf("resize to %dx%d must stay valid: %v", size[0], size[1], err)
		}
	}
	// Negative edges and the minimum ticker speed stay in range when growing and shrinking.
	p := loadFixture(t)
	p.Typography.Item.LetterSpacing = -100
	p.Slides[0].Elements[5].Speed = 10
	if err := Resize(&p, 7680, 4320); err != nil {
		t.Fatal(err)
	}
	if p.Typography.Item.LetterSpacing != -100 || p.Slides[0].Elements[5].Speed != 60 {
		t.Errorf("letter spacing %v speed %v", p.Typography.Item.LetterSpacing, p.Slides[0].Elements[5].Speed)
	}
	if err := Resize(&p, 16, 16); err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(nil); err != nil || p.Slides[0].Elements[5].Speed != 10 {
		t.Errorf("shrunk document: speed %v, %v", p.Slides[0].Elements[5].Speed, err)
	}
}
