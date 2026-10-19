package content

import (
	"opensignage/internal/contracts"
	"opensignage/internal/server/core"
)

// newDocument builds the document of a new presentation from the defaults of the settings
// (format when the request names none, typography, one empty slide with the default
// background, transition and duration).
func newDocument(id, name string, width, height int, s core.Settings) contracts.Presentation {
	if width == 0 {
		width, height = s.Presentations.Width, s.Presentations.Height
	}
	return s.Presentations.Document(id, name, width, height, s.Defaults.SlideMS)
}
