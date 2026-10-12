package content

import (
	"opensignage/internal/common/ids"
	"opensignage/internal/contracts"
)

// Default design resolution when a request names none.
const (
	defaultWidth  = 1920
	defaultHeight = 1080
)

func baseStyle(size float64, color string, bold bool, align string, lineHeight float64) contracts.TextStyle {
	return contracts.TextStyle{Font: "Montserrat", Size: size, Color: color, Bold: bold, LineHeight: lineHeight, Align: align}
}

// newDocument builds the document of a new presentation: one empty slide, Montserrat
// typography (heading 34 px #ff8a00 bold, subheading 26 px #ffa733 bold, item 24 px, description
// 16 px – the same white as the item – and price 24 px bold right),
// black background, 600 ms fade and the default slide duration from the settings.
func newDocument(id, name string, width, height int, slideMS int64) contracts.Presentation {
	return contracts.Presentation{
		ID: id, Name: name, Width: width, Height: height,
		Typography: contracts.Typography{
			Heading:     baseStyle(34, "#ff8a00", true, "left", 1.2),
			Subheading:  baseStyle(26, "#ffa733", true, "left", 1.2),
			Item:        baseStyle(24, "#ffffff", false, "left", 1.25),
			Description: baseStyle(16, "#ffffff", false, "left", 1.25),
			Price:       baseStyle(24, "#ffffff", true, "right", 1.25),
		},
		Slides: []contracts.Slide{{
			ID:         ids.New(),
			Name:       "",
			Duration:   contracts.Duration{Mode: "fixed", MS: slideMS},
			Background: contracts.Background{Type: "color", Color: "#000000"},
			Transition: contracts.Transition{Type: "fade", MS: 600},
			Elements:   []contracts.Element{},
		}},
	}
}
