package app

import (
	"testing"

	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
)

// The presentation defaults of the settings (Settings → Defaults) are the start values of new
// presentations; they are validated like a document and name only existing fonts.
func TestPresentationDefaults(t *testing.T) {
	e := catSetup(t)
	c := e.admin
	_, cur := c.do("GET", "/api/v1/settings", nil)
	if p := asMap(cur["presentations"]); p["width"] != float64(1920) || p["height"] != float64(1080) || asMap(p["text"])["font"] != "Montserrat" {
		t.Fatalf("factory defaults: %v", cur["presentations"])
	}

	d := core.DefaultSettings().Presentations
	d.Width, d.Height, d.Background = 1280, 720, "#112233"
	d.Transition = contracts.Transition{Type: "slide", MS: 800, Direction: "up"}
	d.Text = core.TextDefault{Font: "Montserrat", Color: "#ff0000"}
	d.Typography.Heading.Size = 50

	bad := d
	bad.Typography.Heading.Font = "No Such Font"
	e.expect(c, "PATCH", "/api/v1/settings", map[string]any{"presentations": bad}, 422, "unknown_font")
	bad = d
	bad.Text.Font = "No Such Font"
	e.expect(c, "PATCH", "/api/v1/settings", map[string]any{"presentations": bad}, 422, "unknown_font")
	bad = d
	bad.Transition.MS = 50
	e.expect(c, "PATCH", "/api/v1/settings", map[string]any{"presentations": bad}, 422, "")
	bad = d
	bad.Width = 8
	e.expect(c, "PATCH", "/api/v1/settings", map[string]any{"presentations": bad}, 422, "")
	bad = d
	bad.Text.Color = "red"
	e.expect(c, "PATCH", "/api/v1/settings", map[string]any{"presentations": bad}, 422, "")

	out := e.expect(c, "PATCH", "/api/v1/settings", map[string]any{"presentations": d, "defaults": map[string]any{"mode": "duration", "durationMs": 10000, "repeat": 1, "fit": "contain", "slideMs": 7000}}, 200, "")
	if p := asMap(out["presentations"]); p["background"] != "#112233" || asMap(p["transition"])["direction"] != "up" {
		t.Fatalf("stored: %v", out["presentations"])
	}

	// A new presentation without a size gets the default format; one with a size keeps it.
	doc := cntDoc(t, e.expect(c, "POST", "/api/v1/presentations", map[string]any{"name": "Menu"}, 200, ""))
	s := doc.Slides[0]
	if doc.Width != 1280 || doc.Height != 720 || doc.Typography.Heading.Size != 50 || s.Background.Color != "#112233" ||
		s.Transition != d.Transition || s.Duration.MS != 7000 {
		t.Fatalf("new presentation: %dx%d heading %v slide %+v", doc.Width, doc.Height, doc.Typography.Heading.Size, s)
	}
	if doc = cntDoc(t, e.expect(c, "POST", "/api/v1/presentations", map[string]any{"name": "Tall", "width": 1080, "height": 1920}, 200, "")); doc.Width != 1080 || doc.Height != 1920 {
		t.Fatalf("chosen size: %dx%d", doc.Width, doc.Height)
	}
}
