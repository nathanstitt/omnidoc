package inline

import (
	"math"
	"testing"

	"github.com/benoitkugler/textlayout/harfbuzz"

	pkgfont "github.com/nathanstitt/omnidoc/pkg/internal/font"
)

// Arabic letters take their joining form from their neighbours in reading order:
// in مرحبا, meem starts the word (init), reh joins only to the right (fina), so hah
// starts a new join (init), beh is between two joiners (medi), and alef ends the
// word (fina). Shaping the segment left-to-right made harfbuzz join the word
// backwards: meem and alef came out isolated and beh initial.
func TestArabicJoiningFormsFollowReadingOrder(t *testing.T) {
	face, ok := pkgfont.LoadScriptFallback('م', pkgfont.Style{})
	if !ok {
		t.Fatal("no bundled Arabic face")
	}
	shaped, ok := shapeComplex(face, []rune("مرحبا"))
	if !ok {
		t.Fatal("shaping failed")
	}
	var got []string
	for _, g := range shaped {
		if face.GlyphAdvance(g.gid) == 0 {
			continue // a mark (beh's dot)
		}
		got = append(got, face.GlyphName(g.gid))
	}
	want := []string{"uni0645.init", "uni0631.fina", "uni062D.init", "uni066E.medi", "uni0627.fina"}
	if len(got) != len(want) {
		t.Fatalf("letters in logical order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("letters in logical order = %v, want %v", got, want)
			break
		}
	}
}

// Every shaped glyph must be drawn where harfbuzz placed it. shapeComplex returns
// logical order with harfbuzz's advances and each glyph's GPOS x offset; drawn in
// visual order (the reverse), each glyph's pen plus its offset must equal harfbuzz's.
// A word-final letter's mark is harfbuzz's FIRST visual glyph (كتب's beh dot, بيت's
// teh dots).
func TestShapedGlyphsLandWhereHarfbuzzPlacedThem(t *testing.T) {
	face, ok := pkgfont.LoadScriptFallback('م', pkgfont.Style{})
	if !ok {
		t.Fatal("no bundled Arabic face")
	}
	otf, _ := face.OpenTypeFont()
	hbFont := harfbuzz.NewFont(interface{}(otf).(harfbuzz.FaceOpentype))
	upem := face.UnitsPerEm()
	for _, word := range []string{"كتب", "بيت", "مرحبا"} {
		buf := harfbuzz.NewBuffer()
		buf.AddRunes([]rune(word), 0, -1)
		buf.GuessSegmentProperties()
		buf.Props.Direction = harfbuzz.RightToLeft
		buf.Shape(hbFont, nil)
		want := map[uint16][]float64{}
		pen := 0.0
		for i, info := range buf.Info {
			gid := uint16(info.Glyph)
			want[gid] = append(want[gid], pen+float64(buf.Pos[i].XOffset)/upem)
			pen += float64(buf.Pos[i].XAdvance) / upem
		}

		shaped, ok := shapeComplex(face, []rune(word))
		if !ok {
			t.Fatalf("%s: shaping failed", word)
		}
		got := map[uint16][]float64{}
		pen = 0
		for i := len(shaped) - 1; i >= 0; i-- { // visual order
			got[shaped[i].gid] = append(got[shaped[i].gid], pen+shaped[i].xOffset)
			pen += shaped[i].advance
		}
		for gid, xs := range want {
			gs := got[gid]
			if len(gs) != len(xs) {
				t.Errorf("%s: glyph %s drawn %d times, want %d", word, face.GlyphName(gid), len(gs), len(xs))
				continue
			}
			for _, x := range xs {
				found := false
				for _, g := range gs {
					if math.Abs(g-x) < 1e-6 {
						found = true
					}
				}
				if !found {
					t.Errorf("%s: glyph %s drawn at %v em, harfbuzz placed it at %.4f", word, face.GlyphName(gid), gs, x)
				}
			}
		}
	}
}
