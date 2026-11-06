package draw

import (
	"math"
	"testing"

	"github.com/benoitkugler/textlayout/harfbuzz"
	pkgfont "github.com/nathanstitt/omnidoc/pkg/internal/font"
	"github.com/nathanstitt/omnidoc/pkg/internal/render"
)

// In SVG <text>, an Arabic word after Latin text is drawn as harfbuzz placed it:
// letters right to left and each dot on its own letter, including the dot of the
// word's last letter, which harfbuzz emits first.
func TestSVGArabicWordKeepsLettersAndDots(t *testing.T) {
	face, ok := pkgfont.LoadScriptFallback('ب', pkgfont.Style{})
	if !ok {
		t.Fatal("no bundled Arabic face")
	}
	otf, _ := face.OpenTypeFont()
	hb := harfbuzz.NewFont(interface{}(otf).(harfbuzz.FaceOpentype))
	upem := face.UnitsPerEm()
	for _, word := range []string{"كتب", "اليابان"} {
		doc, _ := parseSVG(t, `<svg xmlns="http://www.w3.org/2000/svg" width="400" height="100"><text x="10" y="60" font-size="40">Name `+word+`</text></svg>`)
		placed := New(doc).layoutText(firstText(t, doc))
		type gx struct {
			gid uint16
			x   float64
		}
		var drawn []gx
		latin := math.Inf(-1)
		for i := range placed {
			p := &placed[i]
			x, _ := p.matrix(render.Identity).Apply(0, 0)
			if p.glyph.Face != nil && p.glyph.Face.UnitsPerEm() == upem {
				if _, isAr := p.glyph.Face.GID('ب'); isAr {
					drawn = append(drawn, gx{p.glyph.GID, x})
					continue
				}
			}
			if x > latin {
				latin = x
			}
		}
		buf := harfbuzz.NewBuffer()
		buf.AddRunes([]rune(word), 0, -1)
		buf.GuessSegmentProperties()
		buf.Props.Direction = harfbuzz.RightToLeft
		buf.Shape(hb, nil)
		if len(drawn) != len(buf.Info) {
			t.Fatalf("%s: drew %d Arabic glyphs, harfbuzz shaped %d", word, len(drawn), len(buf.Info))
		}
		var want []gx
		pen := 0.0
		for i, info := range buf.Info {
			want = append(want, gx{uint16(info.Glyph), pen + float64(buf.Pos[i].XOffset)/upem})
			pen += float64(buf.Pos[i].XAdvance) / upem
		}
		// Anchor on the word's first letter (harfbuzz's last visual glyph with an advance).
		anchor := len(want) - 1
		for face.GlyphAdvance(want[anchor].gid) == 0 {
			anchor--
		}
		origin := math.NaN()
		for _, d := range drawn {
			if d.gid == want[anchor].gid {
				origin = d.x - want[anchor].x*40
			}
		}
		used := make([]bool, len(drawn))
		for _, w := range want {
			found := false
			for i, d := range drawn {
				if !used[i] && d.gid == w.gid && math.Abs(d.x-(origin+w.x*40)) < 0.01 {
					used[i], found = true, true
					break
				}
			}
			if !found {
				t.Errorf("%s: %s is not drawn at harfbuzz's place x=%.2f", word, face.GlyphName(w.gid), origin+w.x*40)
			}
		}
		for _, d := range drawn {
			if d.x < latin {
				t.Errorf("%s: an Arabic glyph is drawn at x=%.1f, left of the Latin text", word, d.x)
			}
		}
	}
}
