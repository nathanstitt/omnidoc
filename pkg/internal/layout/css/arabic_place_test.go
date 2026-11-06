package css

import (
	"math"
	"testing"

	"github.com/benoitkugler/textlayout/harfbuzz"
	pkgfont "github.com/nathanstitt/omnidoc/pkg/internal/font"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout"
)

// placedGID is one drawn glyph: its id and its paint X in points.
type placedGID struct {
	gid uint16
	x   float64
}

// harfbuzzPlacement shapes word right-to-left with the bundled Arabic face and
// returns each glyph's drawn X in em (pen plus GPOS x offset), in visual order.
func harfbuzzPlacement(t *testing.T, word string) (*pkgfont.Face, []placedGID) {
	t.Helper()
	face, ok := pkgfont.LoadScriptFallback('ب', pkgfont.Style{})
	if !ok {
		t.Fatal("no bundled Arabic face")
	}
	otf, _ := face.OpenTypeFont()
	buf := harfbuzz.NewBuffer()
	buf.AddRunes([]rune(word), 0, -1)
	buf.GuessSegmentProperties()
	buf.Props.Direction = harfbuzz.RightToLeft
	buf.Shape(harfbuzz.NewFont(interface{}(otf).(harfbuzz.FaceOpentype)), nil)
	upem := face.UnitsPerEm()
	var out []placedGID
	pen := 0.0
	for i, info := range buf.Info {
		out = append(out, placedGID{gid: uint16(info.Glyph), x: pen + float64(buf.Pos[i].XOffset)/upem})
		pen += float64(buf.Pos[i].XAdvance) / upem
	}
	return face, out
}

// assertDrawnAsHarfbuzz checks that the drawn glyphs of one Arabic word sit exactly
// where harfbuzz placed them, relative to each other: the letters in right-to-left
// order and each dot over or under its own letter. drawn holds the word's glyphs
// (any order); size is the font size in points.
func assertDrawnAsHarfbuzz(t *testing.T, word string, drawn []placedGID, size float64) {
	t.Helper()
	face, want := harfbuzzPlacement(t, word)
	if len(drawn) != len(want) {
		t.Fatalf("%s: drew %d glyphs, harfbuzz shaped %d", word, len(drawn), len(want))
	}
	// Anchor on the visually last glyph with an advance (the word's first letter).
	anchor := -1
	for i := len(want) - 1; i >= 0; i-- {
		if face.GlyphAdvance(want[i].gid) != 0 {
			anchor = i
			break
		}
	}
	origin := math.NaN()
	for _, d := range drawn {
		if d.gid == want[anchor].gid {
			origin = d.x - want[anchor].x*size
		}
	}
	if math.IsNaN(origin) {
		t.Fatalf("%s: the first letter %s was not drawn", word, face.GlyphName(want[anchor].gid))
	}
	used := make([]bool, len(drawn))
	for _, w := range want {
		found := false
		for i, d := range drawn {
			if !used[i] && d.gid == w.gid && math.Abs(d.x-(origin+w.x*size)) < 0.01 {
				used[i], found = true, true
				break
			}
		}
		if !found {
			t.Errorf("%s: %s is not drawn at x=%.2f (harfbuzz's place)", word, face.GlyphName(w.gid), origin+w.x*size)
		}
	}
}

// arabicGlyphs returns a page's glyph items drawn with the bundled Arabic face.
func arabicGlyphs(p layout.Page) (out []placedGID, rightmostLatin float64) {
	rightmostLatin = math.Inf(-1)
	for _, it := range p.Items {
		if it.Kind != layout.GlyphKind || it.Glyph.Face == nil {
			continue
		}
		if it.Glyph.Face.UnitsPerEm() > 0 && isArabicFace(it.Glyph.Face) {
			out = append(out, placedGID{gid: it.Glyph.GID, x: it.Glyph.XPt})
			continue
		}
		if it.Glyph.XPt > rightmostLatin {
			rightmostLatin = it.Glyph.XPt
		}
	}
	return out, rightmostLatin
}

func isArabicFace(f *pkgfont.Face) bool {
	ar, _ := pkgfont.LoadScriptFallback('ب', pkgfont.Style{})
	_, ok := f.GID('ب')
	return ok && f.UnitsPerEm() == ar.UnitsPerEm()
}

// An Arabic word at the end of a left-to-right line ("Name: كتب") is reordered as
// one right-to-left run after the Latin text. Each letter keeps its place and each
// dot stays on its letter: a word-final letter's dot is harfbuzz's first visual
// glyph, the case that went wrong when glyphs were reordered to place it.
func TestArabicWordAfterLatinKeepsLettersAndDots(t *testing.T) {
	for _, word := range []string{"كتب", "اليابان"} {
		pages := layoutPagedHTML(t, `<html><body style="font-size:40px"><p>Name: `+word+`</p></body></html>`, 1000)
		drawn, latin := arabicGlyphs(pages.Pages[0])
		for _, d := range drawn {
			if d.x < latin {
				t.Errorf("%s: an Arabic glyph is drawn at x=%.1f, left of the Latin text (%.1f)", word, d.x, latin)
			}
		}
		assertDrawnAsHarfbuzz(t, word, drawn, 40)
	}
}

// The same word alone on a right-to-left line.
func TestArabicWordOnRTLLineKeepsLettersAndDots(t *testing.T) {
	for _, word := range []string{"كتب", "اليابان"} {
		pages := layoutPagedHTML(t, `<html><body style="font-size:40px"><p dir="rtl">`+word+`</p></body></html>`, 1000)
		drawn, _ := arabicGlyphs(pages.Pages[0])
		assertDrawnAsHarfbuzz(t, word, drawn, 40)
	}
}

// arabicWordList covers dotted letters in every joining position (initial بيت, medial
// نبت, final كتب, isolated ب), several dotted letters in a row (باب, اليابان, ثابت),
// tashkeel (مُحَمَّد), tanween (شكرًا) and the lam-alef ligature (السلام).
var arabicWordList = []string{"بيت", "باب", "نبت", "كتب", "ب", "اليابان", "ثابت", "مُحَمَّد", "شكرًا", "السلام"}

// After a left-to-right label, every word in the list is drawn exactly as harfbuzz
// placed it — letters in right-to-left order, each mark on its letter — and wholly to
// the right of the label. A glyph that lost its cluster's direction in the bidi
// reorder (a rune-less base or mark resolving as a neutral) lands next to the label
// instead and fails both checks.
func TestArabicWordListAfterLatinStaysWhole(t *testing.T) {
	for _, word := range arabicWordList {
		pages := layoutPagedHTML(t, `<html><body style="font-size:40px"><p>Name: `+word+`</p></body></html>`, 1000)
		drawn, latin := arabicGlyphs(pages.Pages[0])
		for _, d := range drawn {
			if d.x < latin {
				t.Errorf("%s: an Arabic glyph is drawn at x=%.1f, left of the Latin text (%.1f)", word, d.x, latin)
			}
		}
		assertDrawnAsHarfbuzz(t, word, drawn, 40)
	}
}

// Two Arabic words after a label: the phrase is one right-to-left run, so every
// glyph is right of the label and each word keeps its own placement.
func TestArabicPhraseAfterLatinStaysWhole(t *testing.T) {
	pages := layoutPagedHTML(t, `<html><body style="font-size:40px"><p>Name: بيت كتب</p></body></html>`, 1000)
	drawn, latin := arabicGlyphs(pages.Pages[0])
	for _, d := range drawn {
		if d.x < latin {
			t.Errorf("an Arabic glyph is drawn at x=%.1f, left of the Latin text (%.1f)", d.x, latin)
		}
	}
	// In visual order the second word (كتب) is on the left, the first (بيت) on the right.
	_, first := harfbuzzPlacement(t, "بيت")
	_, second := harfbuzzPlacement(t, "كتب")
	if len(drawn) != len(first)+len(second) {
		t.Fatalf("drew %d Arabic glyphs, want %d", len(drawn), len(first)+len(second))
	}
	sortByX(drawn)
	assertDrawnAsHarfbuzz(t, "كتب", drawn[:len(second)], 40)
	assertDrawnAsHarfbuzz(t, "بيت", drawn[len(second):], 40)
}

// sortByX orders drawn glyphs left to right, marks after a base at the same X.
func sortByX(d []placedGID) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j].x < d[j-1].x; j-- {
			d[j], d[j-1] = d[j-1], d[j]
		}
	}
}
