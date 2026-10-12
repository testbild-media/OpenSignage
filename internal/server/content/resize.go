package content

import (
	"fmt"
	"math"

	"opensignage/internal/contracts"
)

// Resize changes the design resolution of a presentation and scales its content: positions
// per axis (x by the width factor, y by the height factor), sizes (width, height) and all
// typographic and stroke measures by the smaller of the two factors, so shapes keep their
// proportions and text keeps its relation to its box. Product-list scale factors stay as
// they are because they are relative to the (scaled) presentation typography. Every scaled
// value is clamped to its validation range (contracts.Presentation.Validate), so a resize
// never turns a valid document into one the server rejects.
func Resize(p *contracts.Presentation, width, height int) error {
	if width < contracts.MinDesignSize || width > contracts.MaxDesignSize ||
		height < contracts.MinDesignSize || height > contracts.MaxDesignSize {
		return fmt.Errorf("design size %dx%d out of range", width, height)
	}
	if p.Width <= 0 || p.Height <= 0 {
		return fmt.Errorf("presentation has no valid size")
	}
	fx := float64(width) / float64(p.Width)
	fy := float64(height) / float64(p.Height)
	s := math.Min(fx, fy)

	ty := &p.Typography
	for _, st := range []*contracts.TextStyle{&ty.Heading, &ty.Subheading, &ty.Item, &ty.Description, &ty.Price} {
		scaleStyle(st, s)
	}
	maxW, maxH := 4*float64(width), 4*float64(height) // element geometry bounds of the new size
	for i := range p.Slides {
		sl := &p.Slides[i]
		for j := range sl.Elements {
			e := &sl.Elements[j]
			e.X, e.Y = clamp(e.X*fx, -maxW, maxW), clamp(e.Y*fy, -maxH, maxH)
			e.W, e.H = clamp(e.W*s, 1, maxW), clamp(e.H*s, 1, maxH)
			if e.Style != nil {
				scaleStyle(e.Style, s)
			}
			if e.Padding != nil {
				for k := range e.Padding {
					e.Padding[k] = clamp(e.Padding[k]*s, 0, 1000)
				}
			}
			if e.Doc != nil {
				for pi := range e.Doc.Paragraphs {
					for ri := range e.Doc.Paragraphs[pi].Runs {
						scaleMarks(e.Doc.Paragraphs[pi].Runs[ri].Marks, s)
					}
				}
			}
			if e.Stroke != nil {
				e.Stroke.Width = clamp(e.Stroke.Width*s, 0, 500)
			}
			e.Radius = clamp(e.Radius*s, 0, 10000)
			e.RowGap, e.ColumnGap = clamp(e.RowGap*s, 0, 1000), clamp(e.ColumnGap*s, 0, 1000)
			if e.Speed != 0 {
				e.Speed = clamp(e.Speed*s, 10, 2000)
			}
			if o := e.Overrides; o != nil {
				for _, m := range []*contracts.Marks{o.Heading, o.Subheading, o.Item, o.Description, o.Price} {
					scaleMarks(m, s)
				}
			}
		}
	}
	p.Width, p.Height = width, height
	return nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// clamp rounds v to two decimals and keeps it inside [lo, hi]. Values that are not finite
// (only possible in an already invalid document) are left to the validator.
func clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	return math.Min(hi, math.Max(lo, round2(v)))
}

func clampSize(v float64) float64 { return clamp(v, 1, 2000) }

func scaleStyle(st *contracts.TextStyle, s float64) {
	st.Size = clampSize(st.Size * s)
	st.LetterSpacing = clamp(st.LetterSpacing*s, -100, 1000)
	if st.Outline != nil {
		st.Outline.Width = clamp(st.Outline.Width*s, 0, 100)
	}
	if st.Shadow != nil {
		st.Shadow.DX, st.Shadow.DY = clamp(st.Shadow.DX*s, -200, 200), clamp(st.Shadow.DY*s, -200, 200)
	}
}

func scaleMarks(m *contracts.Marks, s float64) {
	if m == nil {
		return
	}
	if m.Size != nil {
		v := clampSize(*m.Size * s)
		m.Size = &v
	}
	if m.LetterSpacing != nil {
		v := clamp(*m.LetterSpacing*s, -100, 1000)
		m.LetterSpacing = &v
	}
}
