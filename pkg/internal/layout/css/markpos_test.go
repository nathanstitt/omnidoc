package css

import (
	"strings"
	"testing"

	"github.com/nathanstitt/omnidoc/pkg/internal/layout"
	"github.com/nathanstitt/omnidoc/pkg/internal/render"
)

// inkRange returns a glyph item's horizontal ink extent in points.
func inkRange(g layout.GlyphItem) (lo, hi float64, ok bool) {
	if g.Outline == nil || len(g.Outline.Segments) == 0 {
		return 0, 0, false
	}
	lo, hi = 1e9, -1e9
	for _, s := range g.Outline.Segments {
		xs := []float64{s.P0.X}
		switch s.Kind {
		case render.CubeTo:
			xs = append(xs, s.P1.X, s.P2.X)
		case render.Close:
			continue
		}
		for _, p := range xs {
			if p < lo {
				lo = p
			}
			if p > hi {
				hi = p
			}
		}
	}
	return g.XPt + lo*g.SizePt, g.XPt + hi*g.SizePt, true
}

// An Arabic dot is a mark glyph that harfbuzz attaches to its letter with a GPOS
// offset. In مرحبا the only mark is beh's dot below (the font draws beh as a
// dotless body plus that mark), and it must be drawn under the beh — not at the pen position beside it, under the junction with the next letter.
func TestArabicMarkSitsOnItsBase(t *testing.T) {
	pages := layoutPagedHTML(t, `<html><body style="font-size:48px"><p>مرحبا</p></body></html>`, 1000)
	var mark, beh *layout.GlyphItem
	for i, it := range pages.Pages[0].Items {
		if it.Kind != layout.GlyphKind || it.Glyph.Face == nil {
			continue
		}
		g := &pages.Pages[0].Items[i].Glyph
		switch {
		case g.Face.GlyphAdvance(g.GID) == 0:
			mark = g
		case strings.HasPrefix(g.Face.GlyphName(g.GID), "uni066E"): // the dotless beh body
			beh = g
		}
	}
	if mark == nil || beh == nil {
		t.Fatal("precondition: no beh glyph and dot mark were shaped")
	}
	mlo, mhi, ok1 := inkRange(*mark)
	blo, bhi, ok2 := inkRange(*beh)
	if !ok1 || !ok2 {
		t.Fatal("precondition: the beh and its dot have outlines")
	}
	if center := (mlo + mhi) / 2; center < blo || center > bhi {
		t.Errorf("the dot is centred at x=%.1f, outside the beh's ink %.1f..%.1f", center, blo, bhi)
	}
}
