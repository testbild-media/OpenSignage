package contracts

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

type fakeResolver struct{}

func (fakeResolver) AssetKind(id string) (string, bool) {
	switch id {
	case "asset-video":
		return "video", true
	case "asset-image":
		return "image", true
	}
	return "", false
}
func (fakeResolver) FontExists(f string) bool      { return f == "Montserrat" }
func (fakeResolver) ProductExists(id string) bool  { return id != "" }
func (fakeResolver) CategoryExists(id string) bool { return id != "" }

func loadFixture(t *testing.T) Presentation {
	t.Helper()
	data, err := os.ReadFile("../../testdata/contracts/presentation-valid.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var p Presentation
	if err := dec.Decode(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFixtureValid(t *testing.T) {
	p := loadFixture(t)
	if err := p.Validate(fakeResolver{}); err != nil {
		t.Fatal(err)
	}
	assets, fonts := p.References()
	if len(assets) != 2 || len(fonts) != 1 {
		t.Fatalf("refs: %v %v", assets, fonts)
	}
}

func TestValidationRejects(t *testing.T) {
	mutations := map[string]func(p *Presentation){
		"two videos": func(p *Presentation) {
			e := p.Slides[1].Elements[1]
			e.ID = "clip2"
			p.Slides[1].Elements = append(p.Slides[1].Elements, e)
		},
		"unknown font": func(p *Presentation) { p.Slides[0].Elements[1].Style.Font = "Arial" },
		"bad color":    func(p *Presentation) { p.Slides[0].Background.Color = "red" },
		"video mode without video bg": func(p *Presentation) {
			p.Slides[1].Background = Background{Type: "color", Color: "#000000"}
		},
		"wrong asset kind": func(p *Presentation) { p.Slides[0].Elements[0].AssetID = "asset-video" },
		"duplicate element id": func(p *Presentation) {
			p.Slides[0].Elements[2].ID = p.Slides[0].Elements[1].ID
		},
		"zero width":   func(p *Presentation) { p.Slides[0].Elements[1].W = 0 },
		"unknown type": func(p *Presentation) { p.Slides[0].Elements[1].Type = "iframe" },
		"too many paragraphs": func(p *Presentation) {
			p.Slides[0].Elements[1].Doc.Paragraphs = make([]Paragraph, MaxParagraphs+1)
		},
		"too many runs": func(p *Presentation) {
			p.Slides[0].Elements[1].Doc.Paragraphs = []Paragraph{{Runs: make([]Run, MaxRunsPerParagraph+1)}}
		},
		"too many source ids": func(p *Presentation) {
			ids := make([]string, MaxSourceIDs+1)
			for i := range ids {
				ids[i] = "cat"
			}
			p.Slides[0].Elements[2].Source = &ProductSource{Kind: "categories", IDs: ids}
		},
	}
	for name, mutate := range mutations {
		p := loadFixture(t)
		mutate(&p)
		if err := p.Validate(fakeResolver{}); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

// TestDocumentLimitsAtBoundary accepts exactly the maximum number of paragraphs, runs and
// product list ids (the limits are inclusive) and names the offending path beyond them.
func TestDocumentLimitsAtBoundary(t *testing.T) {
	p := loadFixture(t)
	paras := make([]Paragraph, MaxParagraphs)
	for i := range paras {
		paras[i] = Paragraph{Runs: []Run{{Text: "x"}}}
	}
	paras[0].Runs = make([]Run, MaxRunsPerParagraph)
	p.Slides[0].Elements[1].Doc.Paragraphs = paras
	ids := make([]string, MaxSourceIDs)
	for i := range ids {
		ids[i] = "cat"
	}
	p.Slides[0].Elements[2].Source = &ProductSource{Kind: "categories", IDs: ids}
	if err := p.Validate(fakeResolver{}); err != nil {
		t.Fatalf("limits are inclusive: %v", err)
	}
	p.Slides[0].Elements[2].Source.IDs = append(ids, "cat")
	var de *DocumentError
	if err := p.Validate(fakeResolver{}); !errors.As(err, &de) || len(de.Problems) != 1 ||
		de.Problems[0] != "slides[0].elements[2].source.ids: too_many" {
		t.Fatalf("too many ids: %v", err)
	}
}
