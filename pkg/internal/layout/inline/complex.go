package inline

// Complex-script shaping. Most scripts render correctly one rune at a time: each
// character has one glyph and a fixed advance, which is what the main Shape loop
// does. Arabic does not — a letter takes a different form depending on whether it
// joins to its neighbours (initial, medial, final, isolated), and some pairs fuse
// into a single ligature glyph. That mapping lives in the font's GSUB table and is
// only resolvable by looking at a whole segment at once.
//
// So a run of complex script is handed to harfbuzz (the pure-Go port already in the
// module graph via textlayout) as one segment, and the shaped glyphs come back with
// their cluster indices, which is how a many-runes-to-one-glyph ligature or a
// one-rune-to-many-glyphs decomposition is attributed back to the source text.
//
// DIRECTION: every script routed here (Arabic, Syriac, Thaana) is right-to-left, and
// it is shaped RIGHT-TO-LEFT. Harfbuzz's Arabic shaper picks each letter's joining
// form from its neighbours in the direction it is told; shaped left-to-right it
// joined words backwards (in مرحبا, meem and alef came out isolated and beh
// initial). Harfbuzz returns right-to-left output in visual order, so shapeComplex
// reverses it back to logical order: the rest of the pipeline stays logical up to
// the single UAX#9 L2 reorder in MakeVisualLine, which reverses the segment again and
// so draws exactly the visual order harfbuzz produced.

import (
	"github.com/benoitkugler/textlayout/harfbuzz"

	pkgfont "github.com/nathanstitt/omnidoc/pkg/internal/font"
)

// needsComplexShaping reports whether r belongs to a script whose glyphs depend on
// context. Today that is the Arabic-family scripts (Arabic itself plus Syriac and
// Thaana, which share the joining behavior); Hebrew is NOT included, as it is
// non-joining and shapes correctly rune-at-a-time.
//
// Restricting this to scripts that genuinely need it keeps every other script on the
// cheap path — and keeps their output byte-identical to before harfbuzz existed here.
func needsComplexShaping(r rune) bool {
	switch {
	case r >= 0x0600 && r <= 0x06FF, // Arabic
		r >= 0x0700 && r <= 0x074F, // Syriac
		r >= 0x0750 && r <= 0x077F, // Arabic Supplement
		r >= 0x0780 && r <= 0x07BF, // Thaana
		r >= 0x08A0 && r <= 0x08FF, // Arabic Extended-A
		r >= 0xFB50 && r <= 0xFDFF, // Arabic presentation forms A
		r >= 0xFE70 && r <= 0xFEFF: // Arabic presentation forms B
		return true
	}
	return false
}

// shapedGlyph is one glyph harfbuzz produced for a complex-script segment: the glyph
// id in the shaping face, its advance in em units, and the runes it stands for.
type shapedGlyph struct {
	gid     uint16
	advance float64 // em units (advance / upem)
	runes   []rune  // the source runes of this glyph's cluster; may be several or none
	// yOffset is harfbuzz's GPOS vertical placement in em units, positive up. The
	// horizontal placement is folded into the advances instead (see foldXOffsets).
	yOffset float64
}

// shapeComplex shapes text through harfbuzz using face's OpenType layout tables, in
// LOGICAL order (see the DIRECTION note above). ok is false when the face carries no
// layout tables (a Type1 face) or harfbuzz produced nothing, in which case the caller
// falls back to per-rune shaping.
func shapeComplex(face *pkgfont.Face, text []rune) ([]shapedGlyph, bool) {
	if len(text) == 0 {
		return nil, false
	}
	otf, ok := face.OpenTypeFont()
	if !ok {
		return nil, false // no GSUB/GPOS (e.g. a classic Type1 face)
	}
	hbFace, ok := interface{}(otf).(harfbuzz.FaceOpentype)
	if !ok {
		return nil, false
	}
	hbFont := harfbuzz.NewFont(hbFace)

	buf := harfbuzz.NewBuffer()
	buf.AddRunes(text, 0, -1)
	buf.GuessSegmentProperties()
	// See the DIRECTION note at the top of the file. The script and language harfbuzz
	// guessed are kept — those select the shaping engine.
	buf.Props.Direction = harfbuzz.RightToLeft
	buf.Shape(hbFont, nil)
	if len(buf.Info) == 0 {
		return nil, false
	}

	upem := face.UnitsPerEm()
	if upem <= 0 {
		return nil, false
	}
	out := make([]shapedGlyph, 0, len(buf.Info))
	xOffsets := make([]float64, len(buf.Info))
	for i, info := range buf.Info {
		out = append(out, shapedGlyph{
			gid:     uint16(info.Glyph),
			advance: float64(buf.Pos[i].XAdvance) / upem,
			runes:   clusterRunes(buf.Info, text, i),
			yOffset: float64(buf.Pos[i].YOffset) / upem,
		})
		xOffsets[i] = float64(buf.Pos[i].XOffset) / upem
	}
	foldXOffsets(out, xOffsets)
	// Harfbuzz's right-to-left output is in visual order; hand the pipeline logical
	// order (see the DIRECTION note).
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, true
}

// foldXOffsets applies harfbuzz's GPOS horizontal placements (mark attachment: an
// Arabic dot moved under its letter) by redistributing advances, so every backend
// that places glyphs by advance alone — the raster, the PDF writer, SVG — draws each
// glyph where harfbuzz put it. out and xOffsets are in harfbuzz's visual output order,
// which is the order the glyphs are finally drawn in (see the DIRECTION note).
//
// A glyph at pen p with x offset o is drawn at p+o. Giving the glyph before it an
// advance larger by o, and the glyph itself an advance smaller by o, draws it at p+o
// and leaves the pen after it unchanged. The segment's first glyph has no glyph
// before it in the segment; harfbuzz gives offsets to marks, which never come first,
// so its offset (none in practice) is left unapplied.
func foldXOffsets(out []shapedGlyph, xOffsets []float64) {
	for i := 1; i < len(out); i++ {
		if o := xOffsets[i]; o != 0 {
			out[i-1].advance += o
			out[i].advance -= o
		}
	}
}

// clusterRunes returns the source runes glyph i stands for.
//
// Harfbuzz reports a cluster index per glyph — the offset of the first rune the glyph
// came from. Several glyphs sharing a cluster decompose one character (a base plus its
// marks); one glyph whose cluster spans several source positions is a ligature. Only
// ONE glyph per cluster carries that cluster's runes, so the text is attributed
// exactly once: the others get none, and a backend mapping glyphs to text (the PDF
// writer's /ToUnicode) neither duplicates nor drops it.
//
// The cluster extent is computed WITHOUT assuming the glyph order. shapeComplex forces
// logical order, so clusters normally ascend — but relying on that would make this
// silently mis-attribute if the direction were ever changed: with descending clusters
// a "same as the previous glyph" test misfires, every leading glyph loses its runes,
// and the final glyph swallows the entire string. Scanning the whole cluster set is
// O(n) per glyph on segments of a handful of glyphs, and correct either way.
func clusterRunes(infos []harfbuzz.GlyphInfo, text []rune, i int) []rune {
	start := int(infos[i].Cluster)
	// Attribute the cluster to its first glyph in SLICE order, whichever direction the
	// cluster indices run.
	for j := 0; j < i; j++ {
		if int(infos[j].Cluster) == start {
			return nil // an earlier glyph already carries this cluster
		}
	}
	// The cluster covers [start, next) where next is the smallest cluster index greater
	// than start across all glyphs; absent one, it runs to the end of the text.
	end := len(text)
	for j := range infos {
		if c := int(infos[j].Cluster); c > start && c < end {
			end = c
		}
	}
	if start < 0 || start >= len(text) || end > len(text) || end <= start {
		return nil // defensive: never slice outside the source text
	}
	return text[start:end]
}
