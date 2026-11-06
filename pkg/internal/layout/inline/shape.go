// Package inline is the format-neutral inline-layout core: text shaping, greedy
// line-breaking, and horizontal alignment math shared by every reflow engine.
//
// Both the flat DOCX engine (pkg/layout) and the forthcoming CSS inline
// formatting context adapt their own styled-run models into this package's
// neutral Run, then reuse one implementation of shaping, breaking, and
// alignment. The core does no cascade and no unit math: every Run arrives with a
// concrete family, point size, and concrete color, so the only font work here is
// face resolution and per-rune measurement.
//
// Coordinates follow the same convention as the rest of the reflow pipeline:
// advances and metrics are in points; glyph outlines stay in em units with Y up
// (the engine scales them at emit time).
package inline

import (
	"context"
	"image/color"

	pkgfont "github.com/nathanstitt/omnidoc/pkg/internal/font"
	layoutfont "github.com/nathanstitt/omnidoc/pkg/internal/layout/font"
	"github.com/nathanstitt/omnidoc/pkg/internal/render"
)

// Run is the neutral styled run: the shaper's single input, which both the flat
// DOCX engine and the CSS inline formatting context adapt into. Everything is
// already resolved (concrete family, point size, concrete color); the core does
// no cascade or unit math.
type Run struct {
	Text         string      // ignored when Break or Atomic != nil
	Family       string      // resolved family name (e.g. "Arial")
	Bold, Italic bool        // weight/slant request, resolved against the face cache
	SizePt       float64     // em size in points
	Color        color.RGBA  // zero-alpha => shaped opaque (the historical flat-engine fixup)
	Break        bool        // hard line break: forces a new line, produces no glyphs
	Atomic       *AtomicItem // non-nil => an unbreakable inline box (inline-block/replaced)
	// WhiteSpace is the run's CSS white-space value ("normal" | "nowrap" | "pre" |
	// "pre-wrap" | "pre-line"). The empty string means "normal" — the historical
	// behavior — so a caller (e.g. the DOCX engine) that never sets it is unaffected.
	// In a preserving mode (pre/pre-wrap/pre-line) a '\n' in Text becomes a hard
	// break and a '\t' advances to the next tab stop; box generation has already
	// collapsed whitespace for the non-preserving modes, so Text arrives pre-collapsed
	// there.
	WhiteSpace string
	// Underline marks a run whose text has text-decoration: underline. It is carried
	// opaquely onto each shaped glyph (Glyph.Underline) for the engine to paint; the
	// shaper itself does nothing with it. The zero value (false) is the historical
	// behavior, so a caller (e.g. the DOCX engine) that never sets it is unaffected.
	Underline bool
	// Strike marks a run whose text has text-decoration: line-through. Like Underline it
	// is carried opaquely onto each shaped glyph (Glyph.Strike) for the engine to paint
	// (a rule at mid-glyph rather than below the baseline); the shaper does nothing with
	// it. The zero value (false) is the historical behavior, so a caller that never sets
	// it is unaffected.
	Strike bool
	// BaselineShiftPt raises (positive) or lowers (negative) the run's glyphs relative to
	// the line baseline, in points — vertical-align: super/sub. It is carried opaquely
	// onto each shaped glyph (Glyph.BaselineShiftPt); the shaper does nothing with it and
	// it does not affect line-box metrics here. Zero (the default) leaves the run on the
	// baseline, so a caller (e.g. the DOCX engine) that never sets it is unaffected.
	BaselineShiftPt float64
	// WordBreak is the run's mid-word break policy: CSS overflow-wrap (a.k.a. word-wrap)
	// combined with word-break, reduced to the distinct behaviors the breaker implements
	// (see wordbreak.go). It is carried onto every glyph the run produces, so a
	// break-word span inside an otherwise-normal paragraph breaks only its own text. The
	// zero value (WordBreakNormal) is the CSS initial state and the historical behavior,
	// so a caller that never sets it (e.g. the DOCX engine) is unaffected.
	WordBreak WordBreakMode
	// InlineBox identifies the innermost non-replaced INLINE box (a <span>, <em>, <a>…)
	// this run came from, or nil when the run's nearest box is the block itself. Like
	// Underline it is carried opaquely onto every glyph (Glyph.InlineBox); the shaper
	// does nothing with it.
	//
	// It exists so the engine can paint an inline box's background and border, which
	// need the box's EXTENT rather than a per-glyph flag: an inline box spanning a line
	// break paints one rect per line, and the identity is what lets consecutive glyphs
	// be coalesced into those rects (see appendInlineBoxDecorations). A plain color
	// would not do — two ADJACENT spans with the same background are different boxes,
	// and merging them would lose the gap their padding creates.
	//
	// Nil (the default) means no inline decoration, so a caller that never sets it
	// (e.g. the DOCX engine) is byte-identical.
	InlineBox *InlineBoxStyle
	// InlineBoxEdge marks this run as an inline box's leading (EdgeLead) or trailing
	// (EdgeTrail) edge rather than text: it produces exactly one zero-ink glyph whose
	// advance is the box's padding+border on that side. That is what makes inline
	// padding part of LAYOUT — the breaker, VisibleWidth, intrinsic sizing, and
	// alignment all read Glyph.Advance and so account for it with no knowledge of
	// inline boxes. EdgeNone (the zero value) is an ordinary run.
	InlineBoxEdge InlineEdge
	// LetterSpacingPt is CSS letter-spacing in points, already resolved against this
	// run's font size. It is added to the advance of EVERY glyph the run produces,
	// including the last one, and may be negative (tightening). Zero — the default —
	// leaves advances untouched, so a caller that never sets it (e.g. the DOCX engine)
	// is byte-identical.
	//
	// Unlike WordBreak this is NOT carried opaquely to the breaker: it is folded into
	// Glyph.Advance here, at shaping time. Everything downstream — the greedy breaker,
	// VisibleWidth, min/max-content measurement, alignment — reads only Advance, so
	// folding it in is what makes the property compose with line breaking and
	// intrinsic sizing without any of them learning about it.
	//
	// TRAILING SPACING, the part that is easy to get wrong: the spacing is added after
	// the last character too, not only BETWEEN characters. CSS Text 3 §8.1 phrases
	// letter-spacing as spacing "between" characters, but the CSS Text 4 editors' draft
	// and every shipping browser add it after every typographic character unit,
	// including the final one on a line — which is why a right-aligned tracked line in
	// a browser sits one tracking-width short of the right edge. This engine matches
	// the browsers, deliberately, because alignment is the observable consequence and
	// matching Firefox/Chrome/Safari is the browser-faithful choice.
	//
	// This is exactly where the CSS path DIVERGES from pkg/svg, which implements the
	// same property against SVG 1.1's literal wording and adds NO trailing gap (see
	// pkg/svg/draw's applyTextSpacing, whose rule the resvg corpus pins with
	// letter-spacing/filter-bbox.svg). The two are different specs, not an
	// inconsistency to be unified.
	LetterSpacingPt float64
	// WordSpacingPt is CSS word-spacing in points, already resolved against this run's
	// font size. It is added to the advance of each word-separator character — U+0020
	// and U+00A0 per CSS Text 3 §8.2 — and nowhere else. It may be negative. Zero (the
	// default) leaves advances untouched.
	//
	// Note the interaction with justification: a justified line distributes its slack at
	// inter-word gaps (inline.Place's ExtraPerSpace), and word-spacing has ALREADY been
	// folded into each space's advance before that slack is computed. The two therefore
	// compose in the spec-correct order — word-spacing widens the spaces, then
	// justification stretches whatever gap remains — with no extra work in Place.
	WordSpacingPt float64
}

// AtomicItem is an inline-level box that participates in a line as one unbreakable
// unit of a fixed width. The IFC lays out its own fragment separately; the line
// InlineBoxStyle is the paintable decoration of one non-replaced inline box — its
// background, border, and horizontal padding — plus the identity that lets the engine
// tell one inline box from another.
//
// It is shared by POINTER, and the pointer IS the identity: every run generated from a
// given <span> carries the same *InlineBoxStyle, and two different spans always carry
// different pointers even when they are styled identically. That is what makes
// "consecutive glyphs belonging to the same inline box" a well-defined run to coalesce,
// which comparing colors could not do — two adjacent spans with the same background are
// still two boxes, and merging them would erase the gap their padding creates.
//
// Horizontal padding and border are part of LAYOUT, not just paint: they widen the
// inline box's advance, via the zero-ink edge glyphs produced from an InlineBoxEdge run
// at each boundary. Everything downstream — the greedy breaker, VisibleWidth,
// min/max-content measurement, alignment — reads only Glyph.Advance, so folding the edge
// width into an advance is what makes padding compose with line breaking without any of
// them learning about inline boxes. It is the same trick LetterSpacingPt uses.
//
// Not modeled: background images, and VERTICAL padding/margins — which per CSS 10.6.1
// overflow the line box rather than growing it, so honoring them would need the line to
// track an overflowing paint extent separate from its advance. Per-edge and rounded
// inline borders are likewise absent (they would need the block path's ring machinery).
// Each is absent rather than half-applied, so the rect that paints is exactly the rect
// this describes. See appendInlineBoxDecorations.
type InlineBoxStyle struct {
	// Background is the fill painted behind the box's text. A zero-alpha value means
	// no background (the CSS initial "transparent"), which is the common case.
	Background color.RGBA

	// BorderColor and BorderWidthPt describe a uniform solid border painted around the
	// box's fragment on each line. A zero width means no border.
	BorderColor   color.RGBA
	BorderWidthPt float64

	// PaddingLeftPt / PaddingRightPt are CSS padding-left/right, already resolved to
	// points. With the border they set the box's leading and trailing edge advances, so
	// text after a padded span starts past its padding rather than under it.
	PaddingLeftPt, PaddingRightPt float64
}

// InlineEdge marks an InlineBoxEdge run as a box's leading or trailing edge.
type InlineEdge uint8

const (
	// EdgeNone is an ordinary run (the zero value).
	EdgeNone InlineEdge = iota
	// EdgeLead is the run holding an inline box's left padding + border.
	EdgeLead
	// EdgeTrail is the run holding an inline box's right padding + border.
	EdgeTrail
)

// LeadEdgePt is the horizontal space the box reserves before its first glyph: its left
// padding plus its border. It is both the advance of the leading edge glyph and the
// amount the painted rect extends left of the glyphs, so layout and paint agree by
// construction.
func (s *InlineBoxStyle) LeadEdgePt() float64 {
	if s == nil {
		return 0
	}
	return s.PaddingLeftPt + s.borderPt()
}

// TrailEdgePt mirrors LeadEdgePt for the box's trailing side.
func (s *InlineBoxStyle) TrailEdgePt() float64 {
	if s == nil {
		return 0
	}
	return s.PaddingRightPt + s.borderPt()
}

// borderPt is the painted border width, or 0 when the border would not paint.
func (s *InlineBoxStyle) borderPt() float64 {
	if s.BorderWidthPt > 0 && s.BorderColor.A > 0 {
		return s.BorderWidthPt
	}
	return 0
}

// EdgePt returns the reserved width on the given side.
func (s *InlineBoxStyle) EdgePt(e InlineEdge) float64 {
	switch e {
	case EdgeLead:
		return s.LeadEdgePt()
	case EdgeTrail:
		return s.TrailEdgePt()
	}
	return 0
}

// Paints reports whether the style has anything to draw, so the engine can skip the
// coalescing pass entirely for the overwhelmingly common undecorated inline box.
func (s *InlineBoxStyle) Paints() bool {
	if s == nil {
		return false
	}
	return s.Background.A > 0 || s.borderPt() > 0
}

// Reserves reports whether the box takes horizontal space in the line, which a box with
// padding does even when it paints nothing.
func (s *InlineBoxStyle) Reserves() bool {
	if s == nil {
		return false
	}
	return s.LeadEdgePt() != 0 || s.TrailEdgePt() != 0
}

// only needs its advance and baseline placement. Carried opaquely through shaping.
//
// WidthPt is the item's full inline advance INCLUDING its horizontal margins;
// MarginLeftPt is the left margin within that advance, so the IFC offsets the
// item's border box past it when placing the kept fragment. HeightPt and BaselinePt
// describe the margin box's vertical extent and the baseline it rests on. The core
// uses only WidthPt (for breaking/placement) and the vertical metrics (for line-box
// sizing); MarginLeftPt, BaselinePt, and Ref are read by the IFC at emit time.
type AtomicItem struct {
	WidthPt, HeightPt float64
	MarginLeftPt      float64 // left margin within WidthPt; the IFC shifts the box past it
	BaselinePt        float64 // distance from the item's top down to the baseline it rests on
	Ref               any     // opaque back-reference the IFC uses to position the item's fragment
}

// Glyph is one shaped glyph, or whitespace (nil Outline, Space=true), or a hard
// break (Break=true), or an atomic inline box (Atomic != nil). It is the unit
// passed from Shape to Break and to each engine's line emitter.
type Glyph struct {
	Outline             *render.Path // em units, Y up; nil for whitespace/missing ink/atomic
	Advance             float64      // points
	Color               Color
	SizePt              float64
	AscentPt, DescentPt float64
	LineGapPt           float64
	Space               bool        // a break opportunity; excluded from a line's trailing width
	Break               bool        // a hard line break (no ink)
	Atomic              *AtomicItem // non-nil => atomic box occupying Advance width
	// NoWrap marks a glyph belonging to a non-wrapping run (white-space: nowrap/pre):
	// the breaker must not take a soft (width) break at or before it. A Space with
	// NoWrap set is still a space for width/trailing purposes but is NOT a break
	// opportunity, so a nowrap inline span stays on one line even inside a wrapping
	// block.
	NoWrap bool
	// WordBreak carries the run's mid-word break policy (the reduced combination of CSS
	// overflow-wrap and word-break — see wordbreak.go). The breaker consults it to decide
	// whether a cluster boundary inside this glyph's word is a break opportunity. The zero
	// value (WordBreakNormal) is whitespace-only breaking, the historical behavior, so a
	// caller that never sets Run.WordBreak (e.g. DOCX) is unaffected.
	WordBreak WordBreakMode
	// Underline carries the run's text-decoration: underline onto the glyph, for the
	// engine's line emitter to paint as an underline rule. The shaper does not act on
	// it. Zero (false) for callers that don't set Run.Underline (e.g. DOCX).
	Underline bool
	// Strike carries the run's text-decoration: line-through onto the glyph, for the
	// engine's line emitter to paint as a mid-glyph rule. The shaper does not act on it.
	// Zero (false) for callers that don't set Run.Strike (e.g. DOCX).
	Strike bool
	// InlineBox carries the run's innermost inline box identity onto the glyph, for the
	// engine's line emitter to coalesce into per-line background/border rects. The
	// shaper does not act on it. Nil for callers that don't set Run.InlineBox (e.g.
	// DOCX) and for text whose nearest box is the block itself.
	InlineBox *InlineBoxStyle
	// Edge marks this glyph as an inline box's leading or trailing edge: it has no ink,
	// and its Advance is the box's padding+border on that side. EdgeNone (the zero
	// value) is an ordinary glyph.
	Edge InlineEdge
	// BaselineShiftPt carries the run's vertical-align: super/sub shift (points, positive
	// = up) onto the glyph, for the engine's line emitter to offset the glyph's paint Y
	// from the line baseline. The shaper does not act on it. Zero for callers that don't
	// set Run.BaselineShiftPt (e.g. DOCX).
	BaselineShiftPt float64
	// Face, GID, and Runes carry font identity for text-emitting backends (the PDF
	// writer embeds Face's program for GID and maps GID -> Runes in /ToUnicode). Face
	// is nil for whitespace/atomic/break glyphs; a rasterizing backend ignores all
	// three and paints Outline.
	Face  *pkgfont.Face
	GID   uint16
	Runes []rune
}

// Color is the package's own RGBA so the public glyph type carries no image/color
// dependency; it is the same four-uint8 layout as image/color.RGBA (no size win),
// and each engine converts to/from image/color at emit time.
type Color struct{ R, G, B, A uint8 }

// Shape turns styled runs into a flat slice of shaped glyphs, resolving each run's
// face through faces and measuring every rune at the run's size. A run whose family
// has no bundled face is skipped (logged via logf); a rune that neither the run's
// face nor any script fallback can map yields a .notdef glyph — the font's own glyph
// 0, or a synthesized tofu box — and is logged once per rune per call. A Break run
// yields one hard-break glyph; an Atomic run yields one atomic glyph whose Advance is
// AtomicItem.WidthPt. A zero-alpha Run.Color is shaped opaque. logf may be nil. Shape
// never panics on malformed input.
//
// Shape is uninterruptible; use ShapeContext when the caller needs to bound how
// long shaping may run.
func Shape(faces *layoutfont.FaceCache, runs []Run, logf func(string, ...any)) []Glyph {
	return ShapeContext(context.Background(), faces, runs, logf)
}

// shapeCancelStride is how many runes Shape processes between cancellation
// checks. The per-rune body (face lookup, outline extraction, advance
// accumulation) is the hottest loop in layout, so a ctx.Err() on every rune
// would be a measurable tax on every document — while a check every 1024 runes
// is far below the noise floor and still bounds the worst case to a fraction of
// a millisecond of extra work after cancellation. A power of two so the
// remainder test is a mask.
const shapeCancelStride = 1024

// ShapeContext is Shape with a context bounding the work. Shaping a single very
// large text run is the longest uninterruptible stretch on the layout path — it
// happens BEFORE line breaking, so the per-line cancellation check in the CSS
// engine's layoutInline cannot help until shaping finishes. Without a check here
// a pathological paragraph pins a core for the whole shaping pass regardless of
// the caller's deadline.
//
// On cancellation it returns the glyphs shaped so far rather than an error: this
// mirrors the layout engine's degrade-don't-propagate convention (see
// layoutBlockChildren), and the open boundary converts a cancelled ctx into a
// hard error so a truncated result is never handed to a caller silently.
func ShapeContext(ctx context.Context, faces *layoutfont.FaceCache, runs []Run, logf func(string, ...any)) []Glyph {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var out []Glyph
	// warnedRunes records which missing runes have already been logged, so a document
	// with 500 unmappable emoji emits a bounded number of lines instead of 500. It is
	// per-CALL rather than per-engine on purpose: Shape is the only place that knows
	// the rune is missing, it is called once per paragraph/measure pass, and a set
	// scoped here needs no lock and cannot leak between documents. The cost is that a
	// rune missing in several paragraphs logs once per paragraph — bounded by the
	// paragraph count, which is the property that matters, and it keeps the report
	// attached to where the text actually is. (The CSS engine also shapes during
	// intrinsic-width measurement, so a missing rune inside a table cell or flex item
	// can be reported by the measure pass as well as the layout pass — still bounded,
	// and still one line per rune per pass rather than one per occurrence.)
	warnedRunes := map[rune]bool{}
	// lineCol tracks the current column x-position (points) since the last line start,
	// used to compute tab-stop advances. It re-bases to 0 at each hard break.
	lineCol := 0.0
	for _, r := range runs {
		// Between runs: cheap (a document with many small runs checks often, and
		// each check is dwarfed by the face resolution that follows).
		if ctx.Err() != nil {
			return out
		}
		if r.Break {
			lineCol = 0
			out = append(out, Glyph{Break: true})
			continue
		}
		if r.Atomic != nil {
			out = append(out, Glyph{Advance: r.Atomic.WidthPt, Atomic: r.Atomic})
			continue
		}
		style := pkgfont.Style{Bold: r.Bold, Italic: r.Italic}
		face, ok := faces.Resolve(r.Family, style)
		if !ok {
			logf("layout: no font for family %q; skipping run", r.Family)
			continue
		}
		asc, desc, gap := face.Metrics()
		// An inline box's edge: one zero-ink glyph whose ADVANCE is the box's padding +
		// border on that side. Emitting it as a glyph is what puts inline padding into
		// layout — the breaker and every measurement read Advance and nothing else, so
		// they reserve the space without knowing inline boxes exist. It carries the
		// box's font metrics too, so a rect for an empty or all-space span still has a
		// height, and NoWrap so a break is never taken between an edge and its text.
		if r.InlineBoxEdge != EdgeNone {
			out = append(out, Glyph{
				Advance:   r.InlineBox.EdgePt(r.InlineBoxEdge),
				SizePt:    r.SizePt,
				AscentPt:  asc * r.SizePt,
				DescentPt: desc * r.SizePt,
				LineGapPt: gap * r.SizePt,
				InlineBox: r.InlineBox,
				Edge:      r.InlineBoxEdge,
				NoWrap:    true,
			})
			continue
		}
		col := Color{R: r.Color.R, G: r.Color.G, B: r.Color.B, A: r.Color.A}
		if r.Color.A == 0 {
			col.A = 0xff // a zero-alpha color is unset; treat as opaque
		}
		_, preserveNL, wrap := flagsFor(r.WhiteSpace)
		noWrap := !wrap
		// spacedAdvance folds this run's letter-spacing and word-spacing into a glyph's
		// natural advance. It is applied at EVERY site below that sets g.Advance, so no
		// glyph-producing path can silently miss the adjustment.
		//
		// The floor at zero is the guard the breaker needs. A negative letter-spacing is
		// legal CSS and tightens text, but nothing downstream is prepared for a NEGATIVE
		// advance: the greedy breaker accumulates advances monotonically and treats the
		// running total as non-decreasing, and VisibleWidth's trailing-space subtraction
		// assumes the same. Letting an advance go negative would let a line's width shrink
		// as glyphs are appended, so an overflowing line could un-overflow and the breaker
		// would pick a break position that does not exist. Clamping each glyph's own
		// advance at zero keeps the accumulation monotonic while still letting a large
		// negative tracking collapse glyphs onto each other — which is visually what a
		// browser shows for `letter-spacing:-1em`, since the glyph OUTLINES still paint at
		// their (now coincident) pen positions and simply overlap.
		spacedAdvance := func(adv float64, isWordSep bool) float64 {
			adv += r.LetterSpacingPt
			if isWordSep {
				adv += r.WordSpacingPt
			}
			if adv < 0 {
				return 0
			}
			return adv
		}
		// spaceEm is the face's ' ' advance in EM units, kept unscaled so it can serve
		// both the tab-stop math below and the advance given to an unmapped invisible
		// character — deriving the latter by dividing the scaled value back down would
		// produce NaN at a zero font-size, which SVG's zero-size fixtures do reach.
		spaceEm := 0.25 // fallback if the face has no space glyph
		if _, sa, ok := face.Glyph(' '); ok {
			spaceEm = sa
		}
		spaceAdv := spaceEm * r.SizePt
		tabStop := tabSize * spaceAdv // width of one tab-stop interval, points
		base := Glyph{Color: col, SizePt: r.SizePt, AscentPt: asc * r.SizePt, DescentPt: desc * r.SizePt, LineGapPt: gap * r.SizePt, NoWrap: noWrap, WordBreak: r.WordBreak, Underline: r.Underline, Strike: r.Strike, InlineBox: r.InlineBox, BaselineShiftPt: r.BaselineShiftPt, Face: face}
		// Complex-script segments (Arabic and friends) are shaped as whole runs through
		// harfbuzz rather than rune-at-a-time, since a letter's form depends on its
		// neighbours. runes carries the run's text once so segments can be sliced from
		// it; skipTo lets the per-rune loop below jump past a segment already emitted.
		runes := []rune(r.Text)
		skipTo := 0
		for ri := 0; ri < len(runes); ri++ {
			// One check per shapeCancelStride runes. The between-runs check above
			// does nothing for the case that matters most here — a single run
			// holding the entire document's text — so the stride is what actually
			// bounds shaping latency, without putting an atomic load in the
			// per-rune path.
			if ri&(shapeCancelStride-1) == 0 && ctx.Err() != nil {
				return out
			}
			rn := runes[ri]
			if ri < skipTo {
				continue
			}
			if needsComplexShaping(rn) {
				end := ri + 1
				for end < len(runes) && needsComplexShaping(runes[end]) {
					end++
				}
				seg := runes[ri:end]
				// The run's own face may not cover the script (a Latin family with an
				// Arabic phrase in it); fall back to a covering face first, so shaping
				// runs against the face that actually has the glyphs.
				shapeFace := face
				if _, _, ok := face.Glyph(rn); !ok {
					if fb, fbOK := faces.ResolveFallback(rn, r.Family, style); fbOK {
						shapeFace = fb
					}
				}
				if shaped, ok := shapeComplex(shapeFace, seg); ok {
					for _, sg := range shaped {
						g := base
						g.Face = shapeFace
						g.GID = sg.gid
						g.Outline = shapeFace.Outline(sg.gid)
						// A complex-shaped cluster is one typographic character unit, so it
						// takes one letter-spacing increment. Word-spacing does not apply:
						// this path only runs for complex scripts, whose segments never
						// contain U+0020 (a space ends the segment — see needsComplexShaping).
						//
						// KNOWN DIVERGENCE, recorded rather than hidden: CSS Text 3 §8.1 says
						// letter-spacing must NOT be applied within a cursive script's joined
						// runs, because inserting space between joined letters breaks the
						// connection. pkg/svg records the same rule (see the resvg corpus's
						// letter-spacing/on-Arabic note). Honoring it needs per-cluster join
						// information harfbuzz does not surface through shapeComplex's flat
						// result, so tracked Arabic is spaced here where a browser would leave
						// it alone. Tracking is vanishingly rare on cursive text, and the
						// alternative — silently dropping the property for those scripts —
						// would be a different wrong answer.
						g.Advance = spacedAdvance(sg.advance*r.SizePt, false)
						g.Runes = sg.runes
						// A mark's GPOS vertical placement (positive = up) rides the same
						// per-glyph shift as vertical-align. Only the CSS line emitter applies
						// that shift; margin boxes and SVG text drop it (BACKLOG G11).
						g.BaselineShiftPt += sg.yOffset * r.SizePt
						out = append(out, g)
						lineCol += g.Advance
					}
					skipTo = end
					continue
				}
				// Shaping unavailable (no layout tables, or it produced nothing): fall
				// through to the per-rune path, which still renders isolated forms.
			}
			switch {
			case rn == '\n' && preserveNL:
				// A preserved newline becomes a hard break and re-bases the tab column.
				// The break glyph carries the run's font metrics so an empty forced line
				// (a blank line in pre/pre-wrap/pre-line) gets a CSS strut height — see
				// the empty-forced-line rule in Break/BreakNextWrap — instead of
				// collapsing to zero height.
				out = append(out, Glyph{Break: true, SizePt: r.SizePt, AscentPt: base.AscentPt, DescentPt: base.DescentPt, LineGapPt: base.LineGapPt})
				lineCol = 0
			case rn == '\t' && preserveNL:
				// A preserved tab advances to the next tab stop from the current column.
				// lineCol carries every advance already emitted on this line INCLUDING the
				// spacing folded into it, so the tab stop is measured from the true pen
				// position rather than an unspaced one — tracked preformatted text still
				// lands on its tab stops.
				adv := tabStop
				if tabStop > 0 {
					if a := tabStop - mathMod(lineCol, tabStop); a > 0 {
						adv = a
					}
				}
				// A tab is a break opportunity and measures as whitespace, but it is NOT one
				// of the word-separator characters CSS Text 3 §8.2 enumerates (U+0020,
				// U+00A0), so word-spacing does not apply to it. Letter-spacing does, as to
				// any other character unit.
				g := base
				g.Advance = spacedAdvance(adv, false)
				g.Space = true
				out = append(out, g)
				lineCol += g.Advance
			default:
				// Ordinary rune (and, in collapsing modes, a stray '\n'/'\t' that box-gen
				// already reduced to a space — shape it as a space).
				if rn == '\n' || rn == '\t' {
					rn = ' '
				}
				// A bidi control (LRM/RLM, the embedding/override set, the isolates) draws
				// nothing but DETERMINES ordering, so it must survive shaping as a
				// zero-width glyph — the reorder reads the line's runes, and dropping the
				// control here would silently discard the author's directional intent.
				//
				// It stays ZERO-width under tracking: letter-spacing applies to typographic
				// character units, and a bidi control is not one. Spacing it would make an
				// invisible ordering mark widen the line, so "abc" and "abc" with an
				// embedded LRM would track to different widths.
				if isBidiControlRune(rn) {
					g := base
					g.Runes = []rune{rn}
					g.Face = nil // no outline to draw; not a font glyph
					out = append(out, g)
					continue
				}
				outline, advEm, ok := face.Glyph(rn)
				glyphFace := face
				if !ok {
					// The run's face has no glyph for this rune. Before dropping it,
					// fall back per character: the rest of the font-family list, then
					// a bundled script face, then the supplied fonts directory, then
					// installed fallback fonts. So Hebrew, CJK or a symbol inside an
					// otherwise-Latin paragraph resolves here rather than vanishing.
					if fb, fbOK := faces.ResolveFallback(rn, r.Family, style); fbOK {
						if o, a, gOK := fb.Glyph(rn); gOK {
							outline, advEm, ok, glyphFace = o, a, true, fb
						} else if gid, adv, cOK := colorGlyphIn(fb, rn); cOK {
							// A COLOUR glyph has no outline of its own: its ink is in
							// COLR layers or a bitmap strike, both resolved at paint
							// time from the face + gid. Requiring an outline here is
							// what made an emoji vanish even once its font was found.
							outline, advEm, ok, glyphFace = nil, adv, true, fb
							_ = gid
						}
					}
				}
				// missing marks a rune NO face could map AND that would have drawn ink.
				// Two very different cases hide behind an unmapped rune:
				//
				//   - An INVISIBLE character (a space variant like U+202F NARROW NO-BREAK
				//     SPACE, a format control, a variation selector). It draws nothing even
				//     in a font that maps it, so a tofu box would INVENT a mark the author
				//     never asked for — a regression, not a fix. Browsers render an unmapped
				//     invisible character as blank. It gets an advance and no outline, and no
				//     warning: nothing about the page is wrong.
				//   - A VISIBLE character the available fonts genuinely cannot draw. This is
				//     the case .notdef exists for.
				//
				// Distinguishing them is the whole reason invisibleRune exists; without it,
				// turning on .notdef sprays boxes through documents that render correctly
				// today (this repo's own showcase carries a U+202F).
				missing := !ok && !invisibleRune(rn)
				switch {
				case missing:
					// Historically the rune was DROPPED here, which renders as empty space —
					// and because the surrounding text is unaffected, the result reads as a
					// layout bug rather than a font problem. It is worse still when only SOME
					// runes of a set are missing (three of nine weather emoji drawing, six
					// vanishing), which is exactly what happened on a board carrying only
					// DejaVu and Liberation.
					//
					// Draw .notdef instead, as a browser does. Face.NotdefGlyph picks the
					// font's own glyph 0 when it has geometry and synthesizes a tofu box when
					// it does not (the bundled substitutes all ship a BLANK .notdef, so the
					// synthesized box is what usually shows).
					//
					// The mark comes from the RUN's face, not a script fallback: the fallback
					// search has already failed, and the run's face is the one whose size and
					// metrics the rest of the line uses, so its .notdef is the mark that
					// visually belongs in this text.
					outline, advEm = face.NotdefGlyph()
					glyphFace = face
					warnMissingGlyph(logf, warnedRunes, rn, r.Family)
				case !ok:
					// Invisible and unmapped: occupy space, draw nothing. The advance uses the
					// face's space advance — the closest available approximation, and the value
					// the tab-stop code above already trusts for exactly this purpose — so the
					// character still separates its neighbours instead of collapsing the line.
					outline, advEm, glyphFace = nil, spaceEm, face
				}
				g := base
				g.Face = glyphFace
				g.Outline = outline
				g.Space = rn == ' '
				// word-spacing applies to WORD-SEPARATOR characters, which CSS Text 3 §8.2
				// defines as U+0020 and U+00A0 — the same pair pkg/svg/draw's isWordSpace
				// uses. Note the deliberate mismatch with g.Space just above: U+00A0 takes
				// word-spacing but is NOT a Space for breaking purposes (a no-break space
				// must not become a break opportunity), so the two conditions differ and
				// must not be folded together.
				g.Advance = spacedAdvance(advEm*r.SizePt, isWordSeparator(rn))
				// Carry font identity ONLY for a real font glyph. When GID lookup fails the
				// outline came from a synthesized marker (e.g. a bullet the face lacks) —
				// its GID would be .notdef, so a text-emitting backend must not re-fetch by
				// GID; clear Face so paint fills the synthesized Outline directly.
				// NOTE: this resolves against glyphFace, which for a fallback glyph is the
				// covering script face, not the run's own — a GID is only meaningful
				// against the face it came from.
				//
				// A .notdef glyph takes that same outline route, unconditionally — hence the
				// `missing` guard rather than relying on GID lookup to fail. The reason is on
				// Face.NotdefGlyph: handing a text backend Face+GID 0 would have the PDF
				// writer embed and emit the font's own .notdef, which in every bundled
				// substitute is BLANK, so the box would show in a raster and vanish in a PDF
				// of the same page.
				//
				// Runes IS still set for a .notdef glyph, so the bidi pass sees the real
				// character's class — rather than the U+FFFC placeholder lineText substitutes
				// for a runeless glyph, which reorders differently — and so SVG's
				// glyph-to-character mapping still locates the box.
				switch gid, gidOK := glyphFace.GID(rn); {
				case missing:
					g.Face = nil
					g.Runes = []rune{rn}
				case gidOK:
					g.GID = gid
					g.Runes = []rune{rn}
				default:
					g.Face = nil
				}
				out = append(out, g)
				lineCol += g.Advance
			}
		}
	}
	return out
}

// invisibleRune reports whether r is a character that draws NOTHING even in a font
// that maps it, so an unmappable one must degrade to blank space rather than to a
// .notdef box. Drawing tofu for an invisible character would invent a visible mark
// where the correctly-rendered page has none — the opposite of the fidelity the
// .notdef mark is there to provide.
//
// It covers, by Unicode general category and by name:
//
//   - Zs SPACE SEPARATOR beyond U+0020: the no-break, en/em/thin/hair quad family
//     (U+00A0, U+2000..U+200A), U+202F NARROW NO-BREAK SPACE, U+205F MEDIUM
//     MATHEMATICAL SPACE, and U+3000 IDEOGRAPHIC SPACE. These appear in ordinary
//     well-formed text — U+202F alone occurs in this repo's own showcase — and the
//     bundled substitutes map only a few of them.
//   - Zl/Zp LINE and PARAGRAPH SEPARATOR (U+2028, U+2029).
//   - Cf FORMAT: the zero-width set (U+200B..U+200F), the bidi controls
//     (U+202A..U+202E, U+2060..U+2064, U+2066..U+2069), U+00AD SOFT HYPHEN, the
//     variation selectors (U+FE00..U+FE0F), and U+FEFF ZERO WIDTH NO-BREAK SPACE.
//   - Cc CONTROL (U+0000..U+001F, U+007F..U+009F), which has no printable form.
//
// The bidi controls are listed for completeness; in practice isBidiControlRune
// intercepts them earlier in Shape, since they must keep a zero-width slot for the
// reorder rather than a space-sized advance.
//
// The list is only consulted for a rune NOTHING maps, so a font's own decision wins
// where it has one. U+00AD SOFT HYPHEN is the instructive case: the bundled faces
// DO map it, to a visible hyphen — which is exactly right, since a soft hyphen that
// has been broken at renders as one — so it never reaches this function at all.
//
// Unicode's own tables are not consulted because pkg/layout/inline deliberately
// carries no Unicode-database dependency, and this set is small, stable, and
// exactly the characters a text run actually contains.
func invisibleRune(r rune) bool {
	switch {
	case r <= 0x001F, r >= 0x007F && r <= 0x009F: // Cc control
		return true
	case r == 0x00A0, r == 0x00AD: // NBSP, SOFT HYPHEN
		return true
	case r >= 0x2000 && r <= 0x200F: // en/em/thin/hair spaces, ZWSP..RLM
		return true
	case r == 0x2028, r == 0x2029: // LINE/PARAGRAPH SEPARATOR
		return true
	case r >= 0x202A && r <= 0x202F: // bidi embedding/override, NNBSP
		return true
	case r == 0x205F, r == 0x3000: // MEDIUM MATHEMATICAL, IDEOGRAPHIC SPACE
		return true
	case r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x2069: // word joiner, isolates
		return true
	case r >= 0xFE00 && r <= 0xFE0F: // variation selectors
		return true
	case r == 0xFEFF: // ZERO WIDTH NO-BREAK SPACE / BOM
		return true
	}
	return false
}

// warnMissingGlyph logs that rn has no glyph in any face reachable from family, the
// first time this Shape call sees rn. seen is the call's warn-once set; logf is never
// nil here (Shape substitutes a no-op).
//
// The rune is reported as U+XXXX rather than as the character itself, because the
// whole point is that the character does not render — a log line whose diagnostic
// content is an unprintable box in the reader's terminal helps nobody. The family is
// included because it is the actionable half: the fix is almost always to install or
// embed a font for that text, and the family name says which request went unmet.
func warnMissingGlyph(logf func(string, ...any), seen map[rune]bool, rn rune, family string) {
	if seen[rn] {
		return
	}
	seen[rn] = true
	logf("layout: no glyph for U+%04X in family %q or any fallback; drawing .notdef", rn, family)
}

// isWordSeparator reports whether rn is one of the word-separator characters CSS
// Text 3 §8.2 says word-spacing applies to: U+0020 SPACE and U+00A0 NO-BREAK SPACE.
// It is spelled as a named helper rather than an inline comparison because the
// U+00A0 literal is indistinguishable from an ordinary space in a source listing —
// the same reason pkg/svg/draw factors out its isWordSpace.
//
// Note this is deliberately NOT the same predicate as Glyph.Space. U+00A0 takes
// word-spacing but must never become a break opportunity, and a preserved tab is a
// break opportunity but is not a word separator, so the two sets cross.
func isWordSeparator(rn rune) bool {
	return rn == ' ' || rn == ' '
}

// tabSize is the CSS tab-size used for tab-stop advance in preserving white-space
// modes (the CSS initial value; the tab-size property itself is not yet supported).
const tabSize = 8

// flagsFor decomposes an inline run's white-space value into (collapseSpaces,
// preserveNewlines, wrap). It mirrors css.WhiteSpaceFlags; the inline core keeps its
// own copy so pkg/layout/inline has no dependency on pkg/css. An empty/unknown value
// is "normal".
func flagsFor(ws string) (collapseSpaces, preserveNewlines, wrap bool) {
	switch ws {
	case "nowrap":
		return true, false, false
	case "pre":
		return false, true, false
	case "pre-wrap":
		return false, true, true
	case "pre-line":
		return true, true, true
	default:
		return true, false, true
	}
}

// mathMod returns the non-negative remainder a mod m (m > 0). A local helper to
// avoid importing math for one fmod (the values are small, positive points).
func mathMod(a, m float64) float64 {
	if m <= 0 {
		return 0
	}
	for a >= m {
		a -= m
	}
	for a < 0 {
		a += m
	}
	return a
}

// colorGlyphIn reports whether face has a COLOUR glyph for r — COLR layers or a bitmap
// strike — returning its id and advance. It exists because such a glyph has no outline
// of its own, so the ordinary Glyph() probe reports "no glyph" for a character the font
// can very much draw.
func colorGlyphIn(face *pkgfont.Face, r rune) (uint16, float64, bool) {
	if face == nil || (!face.HasColorGlyphs() && !face.HasColorBitmaps()) {
		return 0, 0, false
	}
	gid, ok := face.GID(r)
	if !ok {
		return 0, 0, false
	}
	if layers, lok := face.ColorLayers(gid); !lok || len(layers) == 0 {
		// No layers: only a bitmap face can still draw it. A size-independent probe is
		// enough here — the strike is chosen at paint time.
		if !face.HasColorBitmaps() {
			return 0, 0, false
		}
	}
	return gid, face.GlyphAdvance(gid), true
}
