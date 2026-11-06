package inline

// Bidirectional reordering (UAX#9). Shaping and line breaking both operate in
// LOGICAL order — the order the runes appear in the source — because breaking at a
// space, measuring widths, and carrying a tail to the next line are all logical-order
// operations. Reordering to VISUAL order happens per line, after the break is chosen,
// because rule L2 reorders within each line and a paragraph's lines can split a
// directional run.
//
// The engine resolves levels with golang.org/x/text/unicode/bidi (a complete UAX#9
// implementation including the bracket-pair algorithm), then applies L2 itself:
// x/text exposes directional runs and their positions but not a display-order
// reorder (its Reorder method is unimplemented upstream).
//
// KNOWN LIMIT: x/text reports a flat list of directional runs rather than per-rune
// embedding levels, so a nested embedding deeper than one level (an LTR island inside
// an RTL island inside an LTR paragraph) reorders as if it were a single level. The
// common cases — an RTL phrase in an LTR paragraph and vice versa, with bracket
// pairs — are exact.

import (
	"golang.org/x/text/unicode/bidi"

	"github.com/benoitkugler/textlayout/unicodedata"
)

// ParagraphDirection is the base direction a line's glyphs are reordered against. It
// mirrors CSS `direction` on the block establishing the inline formatting context.
type ParagraphDirection int

const (
	// DirLTR is a left-to-right paragraph (CSS direction: ltr, the initial value).
	DirLTR ParagraphDirection = iota
	// DirRTL is a right-to-left paragraph (CSS direction: rtl).
	DirRTL
)

// Reorder rearranges one line's glyphs from logical into visual order per UAX#9 rule
// L2, and mirrors paired punctuation in right-to-left runs per rule L4.
//
// It returns glyphs unchanged (no allocation) when the line needs no reordering: an
// LTR paragraph whose text is entirely left-to-right, which is every line of a
// Latin-only document. That keeps the overwhelmingly common path free of cost and
// byte-identical to the pre-bidi behavior.
//
// Glyphs carrying no runes (atomics, hard breaks, and whitespace glyphs the shaper
// emitted without a rune) are treated as neutral and keep their logical position
// relative to the runs around them — except a shaped glyph with a BidiRune, which
// takes that character's class so it moves with its cluster.
func Reorder(glyphs []Glyph, dir ParagraphDirection) []Glyph {
	out, _ := reorder(glyphs, dir)
	return out
}

// reorder is Reorder plus an explicit report of whether anything moved, so callers
// that also need the logical-order metrics can tell the no-op path apart without
// comparing slice identity.
func reorder(glyphs []Glyph, dir ParagraphDirection) (out []Glyph, changed bool) {
	if len(glyphs) < 2 {
		return glyphs, false
	}
	text, idx := lineText(glyphs)
	// No resolvable text (all atomics/breaks): nothing to reorder.
	if len(idx) == 0 {
		return glyphs, false
	}
	// Fast path: a left-to-right paragraph with no right-to-left or Arabic-script
	// character anywhere cannot reorder. This is the Latin-only case.
	if dir == DirLTR && !hasBidiControl(text) {
		return glyphs, false
	}

	var p bidi.Paragraph
	opts := []bidi.Option{bidi.DefaultDirection(bidi.LeftToRight)}
	if dir == DirRTL {
		opts = []bidi.Option{bidi.DefaultDirection(bidi.RightToLeft)}
	}
	if _, err := p.SetString(string(text), opts...); err != nil {
		return glyphs, false // malformed input: leave logical order rather than failing
	}
	ord, err := p.Order()
	if err != nil {
		return glyphs, false
	}

	// Build the visual sequence of RUNE positions by walking the directional runs in
	// visual order and, within a right-to-left run, walking its runes backwards (L2).
	visual := make([]int, 0, len(text))
	n := ord.NumRuns()
	for i := 0; i < n; i++ {
		ri := i
		if dir == DirRTL {
			ri = n - 1 - i // an RTL paragraph lays its runs out right-to-left
		}
		r := ord.Run(ri)
		start, end := r.Pos() // inclusive rune indices into text
		if r.Direction() == bidi.RightToLeft {
			for k := end; k >= start; k-- {
				visual = append(visual, k)
			}
			continue
		}
		for k := start; k <= end; k++ {
			visual = append(visual, k)
		}
	}
	if len(visual) != len(idx) {
		// Defensive: a position mismatch means the mapping is untrustworthy, so keep
		// logical order rather than emitting scrambled text.
		return glyphs, false
	}

	// Emit each GLYPH once, at the first visual position any of its runes
	// reaches — not once per rune position.
	//
	// A glyph can cover several runes (a ligature, or an Arabic contextual
	// cluster), and lineText deliberately records the glyph's index once per
	// rune so every rune keeps a slot in the bidi algorithm's own indexing.
	// Emitting per position therefore DUPLICATED such a glyph, returning more
	// glyphs than came in: an Arabic phrase with three two-rune clusters came
	// back three glyphs longer. Callers that assume the count is preserved —
	// anything pairing the result back against per-character data — then read
	// off the end of their own arrays or mis-associate every glyph after the
	// first cluster.
	//
	// A cluster moves as a unit under UAX#9 (all its runes belong to the same
	// directional run), so the first visual position reached is exactly where
	// the whole cluster belongs.
	res := make([]Glyph, 0, len(glyphs))
	rtlAt := rtlPositions(&ord, len(text))
	emitted := make([]bool, len(glyphs))
	for _, pos := range visual {
		gi := idx[pos]
		if emitted[gi] {
			continue
		}
		emitted[gi] = true
		g := glyphs[gi]
		if rtlAt[pos] {
			mirrorGlyph(&g)
		}
		res = append(res, g)
	}
	return res, true
}

// lineText returns the line's text in logical order plus, for each rune, the index of
// the glyph that produced it. Glyphs with no runes (atomic boxes, hard breaks) are
// represented by U+FFFC OBJECT REPLACEMENT CHARACTER so they participate in the bidi
// algorithm as neutral objects and keep a slot in the mapping.
func lineText(glyphs []Glyph) (text []rune, glyphIdx []int) {
	text = make([]rune, 0, len(glyphs))
	glyphIdx = make([]int, 0, len(glyphs))
	for i := range glyphs {
		g := &glyphs[i]
		switch {
		case len(g.Runes) > 0:
			// A glyph may map several runes (a ligature); the whole cluster moves
			// together, so only the first rune carries the glyph and the rest are
			// recorded as continuations pointing at the same glyph.
			for range g.Runes {
				glyphIdx = append(glyphIdx, i)
			}
			text = append(text, g.Runes...)
		case g.BidiRune != 0:
			// A shaped glyph without runes takes its cluster's class, so it stays in
			// its cluster's directional run (see Glyph.BidiRune).
			text = append(text, g.BidiRune)
			glyphIdx = append(glyphIdx, i)
		default:
			text = append(text, '￼')
			glyphIdx = append(glyphIdx, i)
		}
	}
	return text, glyphIdx
}

// rtlPositions marks each rune position that falls inside a right-to-left run, so the
// caller can mirror paired punctuation there (rule L4).
func rtlPositions(ord *bidi.Ordering, n int) []bool {
	out := make([]bool, n)
	for i := 0; i < ord.NumRuns(); i++ {
		r := ord.Run(i)
		if r.Direction() != bidi.RightToLeft {
			continue
		}
		start, end := r.Pos()
		for k := start; k <= end && k < n; k++ {
			out[k] = true
		}
	}
	return out
}

// mirrorGlyph applies rule L4: in a right-to-left run, a character with the
// Bidi_Mirrored property is displayed as its mirror image — '(' renders as ')'.
// The glyph's Runes are rewritten so a text-emitting backend (the PDF writer's
// /ToUnicode) still maps to the ORIGINAL character; only the drawn shape changes.
//
// Mirroring needs the face to re-resolve the outline, which the core does not hold at
// this point, so a glyph whose mirror cannot be resolved keeps its original shape.
// That is the correct degradation: the character is still legible, just not mirrored.
func mirrorGlyph(g *Glyph) {
	if len(g.Runes) != 1 || g.Face == nil {
		return
	}
	m, ok := unicodedata.LookupMirrorChar(g.Runes[0])
	if !ok || m == g.Runes[0] {
		return
	}
	outline, adv, ok := g.Face.Glyph(m)
	if !ok {
		return
	}
	g.Outline = outline
	g.Advance = adv * g.SizePt
	if gid, ok := g.Face.GID(m); ok {
		g.GID = gid
	}
	// Runes deliberately keeps the ORIGINAL rune: the text content is unchanged, only
	// its rendered form is mirrored, and /ToUnicode must recover what the author wrote.
}

// isBidiControlRune reports whether r is one of the invisible bidi formatting
// characters: the marks (LRM/RLM/ALM), the embedding/override set, and the isolates.
// They carry no ink but decide ordering, so shaping keeps them as zero-width glyphs.
func isBidiControlRune(r rune) bool {
	switch r {
	case 0x200E, 0x200F, // LRM, RLM
		0x061C,                                 // ALM
		0x202A, 0x202B, 0x202C, 0x202D, 0x202E, // LRE, RLE, PDF, LRO, RLO
		0x2066, 0x2067, 0x2068, 0x2069: // LRI, RLI, FSI, PDI
		return true
	}
	return false
}

// hasBidiControl reports whether text contains any character that could introduce
// right-to-left ordering: a strong RTL character (Hebrew, Arabic, Syriac, Thaana,
// N'Ko, Samaritan, and the RTL presentation forms) or an explicit bidi control.
// It is the cheap gate that keeps Latin-only lines on the no-op path.
func hasBidiControl(text []rune) bool {
	for _, r := range text {
		switch {
		case r < 0x0590:
			continue // ASCII, Latin, Greek, Cyrillic: never RTL
		case r <= 0x08FF:
			return true // Hebrew, Arabic, Syriac, Thaana, NKo, Samaritan, Arabic Ext-A
		case r >= 0x200E && r <= 0x200F, // LRM, RLM
			r >= 0x202A && r <= 0x202E, // LRE, RLE, PDF, LRO, RLO
			r >= 0x2066 && r <= 0x2069: // LRI, RLI, FSI, PDI
			return true
		case r >= 0xFB1D && r <= 0xFDFF, // Hebrew/Arabic presentation forms A
			r >= 0xFE70 && r <= 0xFEFF: // Arabic presentation forms B
			return true
		case r >= 0x10800 && r <= 0x10FFF, // Cypriot, Phoenician, Kharoshthi, Avestan…
			r >= 0x1E800 && r <= 0x1EFFF: // Mende Kikakui, Adlam, Arabic Math
			return true
		}
	}
	return false
}
