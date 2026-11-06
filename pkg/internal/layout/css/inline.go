package css

import (
	"context"
	"image/color"
	"strings"
	"unicode"

	gcss "github.com/nathanstitt/omnidoc/pkg/internal/css"
	pkgfont "github.com/nathanstitt/omnidoc/pkg/internal/font"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/cssbox"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/inline"
)

// cssDefaultLineMult is the line-height multiplier applied to the natural font
// height when line-height is "normal"/auto. It mirrors pkg/layout's unexported
// defaultLineMult (~1.15, Word's default leading) so the CSS and flat engines
// agree on auto line spacing; it is redeclared here because the flat engine's
// constant is unexported and outside this package.
const cssDefaultLineMult = 1.15

// layoutInline is the inline-formatting-context hook a block box establishing an
// inline formatting context calls. It lays out b's inline-level children into line
// fragments within contentW points, with line baselines measured from the local
// content-box top (contentTopY, which block layout passes as 0 and later shifts
// into page space) and glyph/atomic X already in page space (offset by contentX so
// the caller's vertical-only shift lands them correctly).
//
// It returns the positioned lines, the total inline content height (the sum of the
// line heights), and the atomic child fragments (inline-block / replaced boxes) the
// caller must attach to the box's Fragment.Children so they paint. The atomic
// fragments are in the same local content-top-0 / page-space-X frame as the lines.
//
// Inline-box decoration (backgrounds/borders/padding on a BoxInline / BoxAnonInline)
// is deferred: this pass renders inline TEXT plus atomic inline-blocks/replaced
// boxes; an inline element box contributes only its text leaves' glyphs.
func (e *Engine) layoutInline(ctx context.Context, b *cssbox.Box, contentW, contentTopY, contentX, bandOriginY float64, fc *floatContext) (lines []LineFragment, height float64, atomics []*Fragment) {
	// 1. Gather inline-level descendants into styled runs (atomics laid out eagerly).
	var runs []inline.Run
	e.gatherInlineRuns(ctx, b, contentW, &runs, &atomics)
	if len(runs) == 0 {
		return nil, 0, nil
	}

	// 2. Shape once (width-independent).
	// ShapeContext, not Shape: this is the one call that can run long before the
	// per-line loop below is ever reached (see the cancellation note there).
	glyphs := inline.ShapeContext(ctx, e.faces, runs, e.logf)

	// A vertical writing-mode takes a separate, self-contained path. Shaping above is
	// axis-independent (it measures runes against a face), so it is shared; everything
	// below assumes a pen walking X across a float-narrowed band and does not
	// generalize. Splitting here rather than threading an axis flag through the loop is
	// what keeps every horizontal document byte-identical.
	if wm := b.Style.WritingMode; wm == "vertical-rl" || wm == "vertical-lr" {
		return e.layoutInlineVertical(b, glyphs, contentW, contentTopY, contentX, bandOriginY, fc)
	}

	align := effectiveTextAlign(b)
	// Used direction for this IFC: resolves start/end alignment (inside
	// effectiveTextAlign) and the edge the first-line indent insets from. NOTE: this
	// does NOT reorder glyphs within a line — inline bidi reordering is a separate
	// slice, so right-to-left script still renders in logical order.
	dir := effectiveDirection(b)
	// The block's white-space controls whether lines wrap at the available width
	// (normal/pre-wrap/pre-line wrap; nowrap/pre do not). A non-wrapping IFC takes the
	// whole run (to the next hard break) per line, overflowing the width.
	_, _, wrap := gcss.WhiteSpaceFlags(b.Style.WhiteSpace)

	// 3. Break + position one line at a time against the float-narrowed band. Float
	//    queries use the BFC-root frame (bandOriginY + the local pen offset); emitted
	//    glyph/line Y stays in the local content-top-0 frame (the block stacker shifts
	//    the whole interior into page space later). Glyph X is absolute page-space
	//    already (availLeft and contentX are absolute), so X is never shifted.
	//
	//    The float edges are CLAMPED to THIS box's own content box [contentX,
	//    contentX+contentW]: the float context spans the whole BFC (its cbLeft/cbRight
	//    are the BFC root's content box, which for a non-BFC box narrower than its
	//    containing block — e.g. a width:200px <p> in a 1280px page — is WIDER than this
	//    box). Without the clamp such a box would lay text out to the BFC width, not its
	//    own. When no float intrudes, the clamp pins avail to exactly [contentX,
	//    contentX+contentW] and the per-line BreakNext reproduces a single fixed-width
	//    Break (the non-float invariant). When a float intrudes, its edge is inside the
	//    box, so the clamp leaves the float-narrowing intact.
	penY := contentTopY
	rest := glyphs
	lineHGuess := e.lineHeightGuess(b)             // representative band height for the float-band queries
	cbLeft, cbRight := contentX, contentX+contentW // this box's own content box (clamp bounds)
	// text-indent applies to the FIRST line only: it narrows the first line's available
	// width and shifts its start X by the same amount. A negative indent (hanging) widens
	// the first line and shifts it left; the arithmetic handles it without clamping.
	indent := textIndentPt(b, contentW)
	firstLine := true
	// Truncation state. clampAt is the line count -webkit-line-clamp allows (0 = no
	// clamp); wantEllipsis is text-overflow:ellipsis on a box that actually clips —
	// an overflowing line in an overflow:visible box still overflows visibly, because
	// there is nothing to hide the truncation behind.
	clampAt := b.Style.LineClamp
	wantEllipsis := b.Style.TextOverflow == "ellipsis" && clips(b)
	lineNo := 0
	for len(rest) > 0 {
		// Cancellation: checked PER LINE, not per glyph. This loop is the second
		// unbounded walk in the engine (layoutBlockChildren is the first): a single
		// paragraph holding megabytes of text is ONE block child, so the check
		// between block children never fires while this loop runs — line breaking a
		// pathological run could spin here for a long time with no way out. One
		// ctx.Err() per emitted line is negligible against the BreakNextWrap +
		// MakeVisualLine + per-glyph emit that follows it; putting it in the inner
		// `for gi := range line.Glyphs` loop instead would place an atomic load on
		// the hottest path in layout, which is exactly the regression to avoid.
		//
		// Degrade rather than propagate, matching layoutBlockChildren: stop adding
		// lines and return what is built. The open boundary (htmlDocument /
		// docxDocument) turns a cancelled ctx into a hard error, so a caller never
		// receives the truncated result silently.
		if ctx.Err() != nil {
			break
		}
		bandY := bandOriginY + (penY - contentTopY) // this line's Y in the BFC-root frame
		availLeft := fc.leftEdge(bandY, lineHGuess)
		if availLeft < cbLeft {
			availLeft = cbLeft
		}
		availRight := fc.rightEdge(bandY, lineHGuess)
		if availRight > cbRight {
			availRight = cbRight
		}
		avail := availRight - availLeft
		if avail < 1 {
			// Fully blocked band (opposite-side floats meet). Drop below the shallowest
			// float and retry rather than emitting a zero-width line.
			next := fc.nextDropY(bandY, lineHGuess)
			if next > bandY {
				penY += next - bandY
				continue
			}
			// Non-advancing band (shouldn't happen): fall back to full width to avoid a
			// spin.
			availLeft, avail = contentX, contentW
		}

		// The first line is indented: it starts `indent` points inset from the band's
		// left and has that much less width. Wrapped lines are unaffected.
		lineIndent := 0.0
		if firstLine {
			lineIndent = indent
			firstLine = false
		}

		var lineGlyphs []inline.Glyph
		lineGlyphs, rest = inline.BreakNextWrap(rest, avail-lineIndent, wrap)
		lineNo++

		// Truncation. Two properties, one operation: drop trailing glyphs until an
		// ellipsis fits, then append it.
		//
		//   - line-clamp ends the box at line N and marks THAT line, so the ellipsis
		//     signals the text that was cut. It fires only when text actually remains
		//     (`len(rest) > 0`), matching browsers: a clamp larger than the line count
		//     leaves the last line untouched.
		//   - text-overflow marks any line too wide for the box, which in the
		//     white-space:nowrap case it is written for is the only line.
		//
		// The line is measured against its own available width — narrowed by a first-line
		// indent or an intruding float — so truncation lands where the clip will.
		lineWidth := avail - lineIndent
		atClamp := clampAt > 0 && lineNo >= clampAt && len(rest) > 0
		overflows := wantEllipsis && inline.VisibleWidth(lineGlyphs) > lineWidth
		if atClamp || overflows {
			if ell, ok := e.ellipsisGlyphFor(b, lineGlyphs); ok {
				// At a clamp boundary the line usually FITS — the ellipsis marks the
				// text cut after it, not an overflow of this line — so it has to be
				// appended rather than merely made room for.
				var cut []inline.Glyph
				if atClamp {
					cut, _ = inline.AppendEllipsis(lineGlyphs, lineWidth, ell)
				} else {
					cut, _ = inline.TruncateWithEllipsis(lineGlyphs, lineWidth, ell)
				}
				lineGlyphs = cut
			}
		}
		if atClamp {
			// Stop after this line: the clamped box is N lines tall, which is a LAYOUT
			// effect (a browser reports the shorter height), not just a paint clip.
			rest = nil
		}
		// Breaking happened in logical order; MakeVisualLine reorders this line's
		// glyphs into visual order (UAX#9 L2) so the emitter below, which walks the
		// slice left-to-right at increasing x, paints them correctly. For an LTR
		// paragraph of Latin-only text this is a no-op on the same backing slice.
		line := inline.MakeVisualLine(lineGlyphs, bidiDir(dir))

		lh := e.effectiveLineHeight(b, line)
		baselineY := penY + ascentOfLine(line)

		// Count the justification gaps on the LOGICAL glyphs: CountSpaces excludes the
		// run of spaces that ENDS the text, which it finds by scanning from the end of
		// the slice — in a reordered RTL line that space sits at the visual start.
		spaceCount := inline.CountSpaces(lineGlyphs)
		isLast := len(rest) == 0
		// availLeft is the absolute page-space left for this line; avail is its width.
		// The first-line indent insets the START edge and narrows the placement width.
		// Under LTR the start edge is the left, so the origin shifts right by the
		// indent; under RTL the start edge is the right, so the width shrinks by the
		// same amount but the left origin does not move.
		startX := availLeft + lineIndent
		if dir == "rtl" {
			startX = availLeft
		}
		p := inline.Place(align, startX, avail-lineIndent, line.WidthPt, spaceCount, isLast)

		var emitted []GlyphFragment
		x := p.StartX
		for gi := range line.Glyphs {
			g := &line.Glyphs[gi]
			switch {
			case g.Atomic != nil:
				// Place the atom: its border-box left = x + the atom's left margin (the
				// margin is part of the advance); its top = baselineY - BaselinePt, i.e.
				// bottom-aligned so the atom's baseline rests on the line baseline.
				if frag, ok := g.Atomic.Ref.(*Fragment); ok && frag != nil {
					translateFragment(frag, (x+g.Atomic.MarginLeftPt)-frag.X, (baselineY-g.Atomic.BaselinePt)-frag.Y)
				}
			// A glyph with ink is emitted; so is an INKLESS one (a space) that sits
			// inside a decorated inline box, because the box's background has to stay
			// continuous across the spaces in "<span>Hello world</span>" rather than
			// painting as two rects with a hole between them. Blank marks the latter so
			// the paint loop keeps skipping it — only the decoration pass reads it.
			// A COLOUR glyph has no outline — its ink lives in COLR layers or a bitmap
			// strike, both resolved at paint time from Face+GID — so an outline test
			// alone drops emoji entirely.
			case g.Outline != nil || hasColorInk(g) || g.InlineBox.Paints() || g.Edge != inline.EdgeNone:
				emitted = append(emitted, GlyphFragment{
					Outline: g.Outline, X: x + g.XShiftPt, AdvancePt: g.Advance, SizePt: g.SizePt,
					Color:           color.RGBA{R: g.Color.R, G: g.Color.G, B: g.Color.B, A: g.Color.A},
					Underline:       g.Underline,
					Strike:          g.Strike,
					InlineBox:       g.InlineBox,
					Edge:            g.Edge,
					AscentPt:        g.AscentPt,
					DescentPt:       g.DescentPt,
					Blank:           g.Outline == nil,
					BaselineShiftPt: g.BaselineShiftPt,
					Face:            g.Face, GID: g.GID, Runes: g.Runes,
				})
			}
			x += g.Advance
			if g.Space {
				x += p.ExtraPerSpace
			}
		}

		lines = append(lines, LineFragment{BaselineY: baselineY, Glyphs: emitted})
		penY += lh
	}

	return lines, penY - contentTopY, atomics
}

// layoutInlineVertical lays out one vertical line: the baseline runs DOWN the page and
// the pen advances along Y by each glyph's vertical advance.
//
// This is the boundary transpose the writing-mode plan chose over renaming the inline
// layer to logical axes. Shaping is shared with the horizontal path (it is
// axis-independent); breaking, float bands, alignment and justification are not reused,
// because each is stated in X and reproducing it here would be the wide rename by
// another route. What that costs is listed under "not implemented" below, and each item
// reports itself rather than failing quietly.
//
// The returned height is the line's INLINE extent — how far down the page the text
// reaches — so an auto-height box grows along the text's own axis, which is what makes
// a vertical label size itself correctly.
//
// Not implemented in this phase, each logged where it occurs: wrapping (a run longer
// than the block extent overflows), vertical-lr line stacking (reported in block.go —
// both vertical values place their single line identically, differing only in which side
// subsequent lines stack from, and there is one line), hard line breaks, atomic inline
// boxes, and text-decoration/inline-box decoration (see appendDecoRules).
//
// Float avoidance is absent and is NOT harmless: the horizontal path narrows each line's
// band against the float context, and this one places its baseline in the middle of the
// full content box, so a vertical line beside a float is drawn straight THROUGH it
// (measured — a 60pt float and a vertical run in a 200pt body put the baseline at x=100,
// inside the float). It logs.
//
// Text alignment along the vertical axis is the one omission left unlogged: with a single
// line there is no free space to distribute along the block axis, so there is nothing an
// author could have asked for that is being ignored. It becomes real with wrapping.
func (e *Engine) layoutInlineVertical(b *cssbox.Box, glyphs []inline.Glyph, contentW, contentTopY, contentX, bandOriginY float64, fc *floatContext) (lines []LineFragment, height float64, atomics []*Fragment) {
	// The baseline is a vertical ruler down the middle of the block extent. In a vertical
	// writing mode the "line height" is measured ACROSS the page, so centring the ruler in
	// the content box is what places the line's cross axis — the counterpart of centring a
	// horizontal line's content area within its line box.
	line := inline.MakeLine(glyphs)
	baselineX := contentX + contentW/2

	// text-orientation is resolved once for the whole line: it is inherited and this
	// path lays out a single box's inline content, so every glyph shares the box's value.
	orient := b.Style.TextOrientation
	if orient == "" {
		// An anonymous box carries a zero-valued Style, where "" is not the CSS initial
		// value — the same trap isAnonymous documents for width/height, and the one that
		// made every anonymous box a stacking context in the transform work.
		orient = "mixed"
	}

	// Where the pen starts depends on how the glyphs are oriented, and getting this wrong
	// clips the first glyph rather than erroring.
	//
	//   - UPRIGHT: the glyph keeps its own baseline, so the pen starts one ascent down
	//     from the content top, exactly as a horizontal line's first baseline does.
	//   - SIDEWAYS: the glyph is turned a quarter turn about its origin, so its ascent no
	//     longer extends UP from the pen — it extends to the side. The pen starts at the
	//     content top, and the ascent is taken out of the cross axis instead (below).
	//
	// A line under `mixed` can hold both, so the inset follows the FIRST glyph: it is the
	// one that would be clipped by the content edge.
	var emitted []GlyphFragment
	leadingInset := 0.0
	if len(glyphs) > 0 {
		if _, sideways := inline.GlyphRotation(orient, glyphs[0].Runes); !sideways {
			leadingInset = ascentOfLine(line)
		}
	}
	penY := leadingInset
	for gi := range glyphs {
		g := &glyphs[gi]
		if g.Break {
			// A hard break would start a new vertical line beside this one, which is the
			// stacking this phase does not do. Report and keep going on one line.
			e.warnOnce("css-writing-mode-vertical-break",
				"css layout: a hard line break in a vertical writing-mode is not implemented; the text stays on one line")
			continue
		}
		if g.Atomic != nil {
			// An atomic box (inline-block/image) would need its own cross-axis placement
			// against a vertical baseline. Reserve nothing and say so, rather than
			// placing it on the horizontal axis where it would land beside the text.
			e.warnOnce("css-writing-mode-vertical-atomic",
				"css layout: an atomic inline box in a vertical writing-mode is not implemented; it is not placed")
			continue
		}
		rot, sideways := inline.GlyphRotation(orient, g.Runes)
		adv := inline.VerticalAdvancePt(g)
		if sideways {
			// A rotated glyph lies on its side, so what advances the pen down the page is
			// its HORIZONTAL extent — the same measure that would advance a horizontal
			// line. Using the vertical advance here would space sideways Latin text by one
			// em per letter, which is the fixed-pitch look of `upright`, not `mixed`.
			adv = g.Advance
		}
		// Cross-axis placement. An UPRIGHT glyph straddles its baseline the way horizontal
		// text does, so the vertical baseline runs down the middle of the box and the glyph
		// centres on it. A SIDEWAYS glyph has been turned about its origin, which puts its
		// ascent to one side of the baseline and its descent to the other — so the baseline
		// itself has to move, by half the difference, to leave the glyph centred.
		x := baselineX
		if sideways {
			x -= (g.AscentPt - g.DescentPt) / 2
		}
		if g.Outline != nil || hasColorInk(g) {
			emitted = append(emitted, GlyphFragment{
				Outline: g.Outline, X: x, Y: penY, AdvancePt: adv, SizePt: g.SizePt,
				Color:           color.RGBA{R: g.Color.R, G: g.Color.G, B: g.Color.B, A: g.Color.A},
				AscentPt:        g.AscentPt,
				DescentPt:       g.DescentPt,
				BaselineShiftPt: g.BaselineShiftPt,
				Face:            g.Face, GID: g.GID, Runes: g.Runes,
				Rotate: rot,
			})
		}
		penY += adv
	}

	// Decorations are dropped for this line (appendDecoRules / appendInlineBoxDecorations
	// bail on Vertical). Report it here, where the style is still in hand, so the reason
	// is stated once rather than inferred from missing ink.
	for gi := range glyphs {
		if glyphs[gi].Underline || glyphs[gi].Strike || glyphs[gi].InlineBox.Paints() {
			e.warnOnce("css-writing-mode-vertical-decoration",
				"css layout: text-decoration and inline-box decoration in a vertical writing-mode are not implemented; they are not painted")
			break
		}
	}

	// The used extent runs from the content top to the last glyph's descender. penY has
	// been advanced once per glyph, so the final advance already carries it past the last
	// glyph's origin — that trailing advance IS the descent allowance, the same way a
	// horizontal line's height ends at its own descent rather than at the next baseline.
	//
	// The leading inset is subtracted back off because it positions the first glyph
	// WITHIN the extent rather than adding to it. It is zero for a sideways line, which
	// is why it is tracked rather than assumed to be ascentOfLine.
	height = penY - leadingInset

	// A shrink-to-fit box (inline-block, float, table cell, flex/grid item) is sized by
	// the intrinsic measure helpers, and those measure the HORIZONTAL axis — they shape
	// the content and break it at a width. In a vertical writing mode the axes swap, so a
	// shrink-to-fit vertical box comes out as wide as its text is long instead of about
	// one em wide. Measured: an inline-block holding "ABCDEF" at 20px is sized 78.9pt
	// wide where a browser gives it roughly the em.
	//
	// Transposing that is a change to measureContent, which table, grid, flex and
	// inline-block sizing all share — outside this phase, and the kind of shared seam
	// that should not be turned by a feature branch without its own tests. Block-level
	// vertical boxes (the label case) do not go through it and are correct.
	if isShrinkToFit(b) {
		e.warnOnce("css-writing-mode-vertical-shrink-to-fit",
			"css layout: a shrink-to-fit box in a vertical writing-mode is sized on the horizontal axis; its cross size is too large")
	}

	// A float intruding on this line is not avoided: the baseline is placed in the middle
	// of the full content box, so the text is drawn straight THROUGH the float rather than
	// beside it. Checked against the extent the line actually occupies.
	if fc != nil {
		bandY := bandOriginY
		if l, r := fc.leftEdge(bandY, height), fc.rightEdge(bandY, height); l > contentX || r < contentX+contentW {
			e.warnOnce("css-writing-mode-vertical-float",
				"css layout: a float intruding on a vertical writing-mode line is not avoided; the text overlaps it")
		}
	}

	// A single line only. If the text runs past the box's own block extent it overflows
	// visibly, exactly as an unwrapped nowrap line does horizontally — but silence here
	// would be indistinguishable from working wrapping, so it is reported.
	if h := b.Style.Height; h.Unit != gcss.UnitAuto && h.Value > 0 && height > h.Value {
		e.warnOnce("css-writing-mode-vertical-overflow",
			"css layout: a vertical writing-mode run of %.0fpt overflows its %.0fpt block extent; vertical line wrapping is not implemented",
			height, h.Value)
	}

	lines = append(lines, LineFragment{BaselineY: contentTopY, Glyphs: emitted, Vertical: true})
	return lines, height, nil
}

// isShrinkToFit reports whether b's inline size comes from the intrinsic measure
// helpers rather than from its containing block — an inline-block, a float, or an
// absolutely positioned box with an auto width. Those measure the horizontal axis, so a
// vertical box among them is sized on the wrong axis (see the caller).
//
// Table cells, flex items and grid items are also intrinsically sized but reach it
// through their own container's algorithm rather than this path, so they are not listed
// here; a vertical box inside one is not diagnosed. Said plainly rather than implied,
// because a diagnostic that looks exhaustive and is not is its own trap.
func isShrinkToFit(b *cssbox.Box) bool {
	if b.Float != cssbox.FloatNone {
		return true
	}
	if b.Display == cssbox.DisplayInlineBlock {
		return true
	}
	return b.Position == cssbox.PosAbsolute || b.Position == cssbox.PosFixed
}

// lineHeightGuess estimates a line's height for the float band query, before the
// line's glyphs are measured. It uses the box's resolved line-height when fixed
// (px/pt/em); for "normal"/auto it falls back to the font size times the default
// multiplier — a representative value (the float bands are coarse, so an exact
// per-line height is unnecessary, and the actual line height is recomputed once the
// line is built). For an anonymous block it reads the inherited line-height/font-size
// from a text leaf, mirroring effectiveLineHeight.
func (e *Engine) lineHeightGuess(b *cssbox.Box) float64 {
	lh := b.Style.LineHeight
	fs := b.Style.FontSizePt
	if isAnonymous(b) {
		if l, f, ok := firstInlineLineHeight(b); ok {
			lh, fs = l, f
		}
	}
	switch lh.Unit {
	case gcss.UnitPx, gcss.UnitPt:
		return lh.Value
	case gcss.UnitEm:
		return lh.Value * fs
	default:
		if fs <= 0 {
			fs = 12 // defensive: a sane default if the font size is unset
		}
		return fs * cssDefaultLineMult
	}
}

// gatherInlineRuns walks b's inline-level descendants depth-first, appending one
// inline.Run per text run / atomic box to *runs and any laid-out atomic fragment to
// *atomics. contentW is the inline content width atomics resolve percentages and
// auto widths against. It recurses into inline element boxes (whose text leaves
// already carry the correct cascaded style) and never panics on an unexpected box.
func (e *Engine) gatherInlineRuns(ctx context.Context, b *cssbox.Box, contentW float64, runs *[]inline.Run, atomics *[]*Fragment) {
	e.gatherInlineRunsIn(ctx, b, contentW, runs, atomics, nil)
}

// gatherInlineRunsIn is gatherInlineRuns carrying the innermost decorated inline box
// down the recursion. box is nil at the top and for undecorated ancestors, so an
// ordinary paragraph produces exactly the runs it always did.
func (e *Engine) gatherInlineRunsIn(ctx context.Context, b *cssbox.Box, contentW float64, runs *[]inline.Run, atomics *[]*Fragment, box *inline.InlineBoxStyle) {
	for _, child := range b.Children {
		switch {
		case child.Kind == cssbox.BoxText:
			// A text box carries the parent's inherited font/color/size; only those
			// fields are meaningful (see makeTextBox) — never its box-level fields.
			*runs = append(*runs, inline.Run{
				Text:            applyTextTransform(child.Text, child.Style.TextTransform),
				Family:          child.Style.FontFamily,
				Bold:            child.Style.Bold,
				Italic:          child.Style.Italic,
				SizePt:          child.Style.FontSizePt,
				Color:           child.Style.Color,
				WhiteSpace:      child.Style.WhiteSpace,
				WordBreak:       wordBreakMode(child.Style),
				LetterSpacingPt: spacingPt(child.Style.LetterSpacing, child.Style.FontSizePt),
				WordSpacingPt:   spacingPt(child.Style.WordSpacing, child.Style.FontSizePt),
				Underline:       child.Style.TextDecorationLine == "underline",
				Strike:          child.Style.TextDecorationLine == "line-through",
				InlineBox:       box,
				BaselineShiftPt: baselineShiftPt(child.Style),
			})
		case child.Kind == cssbox.BoxReplaced:
			// A replaced inline (e.g. <img>, including an inline-block <img>) sizes via
			// the replaced-element algorithm (intrinsic size + aspect ratio, clamped),
			// decoding its image; its fragment carries the image for paint. This case
			// precedes the inline-block case so a replaced inline-block is sized as a
			// replaced box (not laid out as an empty block container).
			w, h := e.replacedUsedSize(ctx, child, contentW)
			frag := e.replacedFragment(ctx, child, w, h, 0, 0, contentW)
			*atomics = append(*atomics, frag)
			*runs = append(*runs, atomicRunFor(child, frag, contentW))
		case child.Display == cssbox.DisplayInlineBlock || child.Display == cssbox.DisplayInlineFlex || child.Display == cssbox.DisplayInlineGrid:
			// An inline-block, inline-flex, or inline-grid establishes a new BFC; lay it
			// out as a block at its resolved width and carry its border box as an atomic
			// unit. It gets a fresh float context (its internal floats stay self-contained)
			// and bandOriginY 0; the IFC then positions the whole atom (subtree + any
			// internal floats) on the line via translateFragment. For inline-flex the
			// layoutBlock→layoutInterior→FlexFC path routes to layoutFlex; for inline-grid
			// it routes to layoutGrid; the outer atom mechanism is identical to inline-block.
			// Positioning inside an inline atom is self-contained: give it a fresh
			// throwaway positionedContext and the page sentinel as its abs-pos CB, then
			// resolve that context immediately against the atom's own provisional frame.
			// Relative/abs positioning of inline ATOMS (and abs descendants of an
			// inline-block) is out of scope this slice (the atom is repositioned on the
			// line by translateFragment, which does not move its Positioned layer), so
			// these resolve approximately against the atom's provisional box — a
			// documented limitation; block-level positioning is exact.
			// CSS 10.3.9 shrink-to-fit: a width:auto inline-block wraps its content
			// rather than filling the parent's content width. Compute the shrink-to-fit
			// width and lay the atom out as if its containing block were that wide, so
			// resolveContentWidth's auto branch (cbWidth - horiz) yields the shrink-to-fit
			// content width. (inline-flex/inline-grid keep their own sizing — inline-grid
			// already shrink-to-fits via its intrinsic width; only inline-BLOCK is filled
			// here.) A specified width is honored as-is (atomCBWidth stays contentW).
			atomCBWidth := contentW
			if child.Display == cssbox.DisplayInlineBlock {
				atomCBWidth = e.inlineBlockCBWidth(ctx, child, contentW)
			}
			atomPos := &positionedContext{}
			res := e.layoutBlock(ctx, child, atomCBWidth, 0, 0, 0, &floatContext{cbLeft: 0, cbRight: atomCBWidth}, atomPos, posCBOwner{isPage: true})
			frag := res.frag
			e.resolveAbsolute(ctx, atomPos, frag, atomCBWidth, 0)
			*atomics = append(*atomics, frag)
			*runs = append(*runs, atomicRunFor(child, frag, atomCBWidth))
		case child.Kind == cssbox.BoxInline || child.Kind == cssbox.BoxAnonInline:
			// Descend into the inline element box; its text leaves carry the correct
			// cascaded font/color. The box's own background/border/padding are not on
			// those leaves (background-color does not inherit), so its decoration is
			// captured here and carried down as the runs' identity. A box with nothing
			// to paint keeps the ancestor's, so an undecorated <em> inside a decorated
			// <span> stays part of the span's rect rather than splitting it.
			inner := box
			if d := inlineBoxStyleFor(child); d != nil {
				inner = d
			}
			e.warnRelativeInline(child, contentW)
			// A box with its own decoration brackets its content with edge runs, which
			// is what reserves its horizontal padding+border in the line. A box that
			// merely inherits the ancestor's identity (an undecorated <em> inside a
			// decorated <span>) adds no edges — it is part of the span's run, not a
			// separate box.
			if inner != box && inner.Reserves() {
				*runs = append(*runs, edgeRun(child, inner, inline.EdgeLead))
			}
			e.gatherInlineRunsIn(ctx, child, contentW, runs, atomics, inner)
			if inner != box && inner.Reserves() {
				*runs = append(*runs, edgeRun(child, inner, inline.EdgeTrail))
			}
		default:
			// A block-level child in an inline formatting context violates the box-gen
			// invariant; skip it defensively rather than misplacing it.
			e.logf("css layout: unexpected non-inline child in inline formatting context; skipping")
		}
	}
}

// applyTextTransform applies the CSS text-transform to a run's text before shaping.
// "capitalize" uppercases the first letter of each word; small-caps is handled upstream
// (mapped to uppercase). The default (including "none" and any unmodeled value) returns
// s unchanged, so a run without text-transform is byte-identical.
func applyTextTransform(s, transform string) string {
	switch transform {
	case "uppercase":
		return strings.ToUpper(s)
	case "lowercase":
		return strings.ToLower(s)
	case "capitalize":
		return capitalizeWords(s)
	default:
		return s
	}
}

// capitalizeWords uppercases the first rune of each run of letters, leaving the rest
// unchanged (a pragmatic approximation of CSS "capitalize": a letter preceded by a
// non-letter starts a word). An apostrophe (U+0027 ' or U+2019 ’) is treated as part
// of the surrounding word — it does NOT reset the word state — so a contraction like
// "it's" capitalizes to "It's" rather than "It'S".
func capitalizeWords(s string) string {
	var b strings.Builder
	prevLetter := false
	for _, r := range s {
		if !prevLetter && unicode.IsLetter(r) {
			b.WriteRune(unicode.ToUpper(r))
		} else {
			b.WriteRune(r)
		}
		// An apostrophe is transparent to word boundaries: leave prevLetter as-is so the
		// letter after it is not treated as a new word start. Any other non-letter resets
		// the state, so the next letter begins a word.
		if r != '\'' && r != '’' {
			prevLetter = unicode.IsLetter(r)
		}
	}
	return b.String()
}

// baselineShiftPt returns the baseline shift (points, positive = up) for a box's
// vertical-align: super/sub. Every other value (baseline/top/middle/bottom/…) yields 0
// — its line-box effects are handled elsewhere / deferred — so a run without super/sub
// is unshifted (byte-identical). The shift scales with the run's font size: super ≈
// +0.33em, sub ≈ −0.20em (browser-plausible). The caller typically also reduces the
// font size (e.g. a DOCX superscript at 0.75em); this adds only the vertical offset.
func baselineShiftPt(st gcss.ComputedStyle) float64 {
	switch st.VerticalAlign {
	case "super":
		return st.FontSizePt * 0.33
	case "sub":
		return -st.FontSizePt * 0.20
	default:
		return 0
	}
}

// spacingPt resolves a letter-spacing / word-spacing length to points against the run's
// OWN font size, which is what makes an em value track a large heading more widely than
// the small text it was inherited from — the reason authors write tracking in em.
//
// A percentage has no defined meaning for either property in CSS Text 3 (unlike SVG,
// where `letter-spacing: 50%` resolves against the font size), so a percentage resolves
// to ZERO here rather than being silently reinterpreted: resolveLen's percentage branch
// is given a zero basis, which is the honest answer for "no basis exists". The initial
// `normal` is stored as the zero Length by the cascade (see setSpacingLength), so an
// unset property returns 0 and leaves every advance untouched.
//
// The result is deliberately NOT clamped here. A negative value is legal and must reach
// the shaper, which floors the per-glyph ADVANCE (not the spacing) at zero — clamping the
// spacing itself would turn a legal tightening into a no-op.
func spacingPt(l gcss.Length, fontSizePt float64) float64 {
	v, isAuto := resolveLen(l, fontSizePt, 0)
	if isAuto {
		return 0
	}
	return v
}

// wordBreakMode reduces the two CSS mid-word-breaking properties, overflow-wrap (a.k.a.
// word-wrap) and word-break, into the single policy the inline breaker implements.
//
// They are independent properties that can be set together, so the reduction has to pick
// a winner. The precedence below is what CSS Text 3 implies and what browsers do:
//
//   - `word-break: break-all` is strictly stronger than any overflow-wrap value. It makes
//     every cluster boundary an ordinary opportunity, which already subsumes
//     overflow-wrap's last-resort break — a word break-all would have chopped early is
//     never reached by the last-resort path.
//   - `word-break: keep-all` is strictly weaker in the opposite direction: it forbids
//     breaking the affected text, so it SUPPRESSES overflow-wrap. (Per spec keep-all
//     governs the implicit CJK opportunities; since this engine generates none, the
//     suppression is its only observable effect here — see ComputedStyle.WordBreak.)
//   - Otherwise overflow-wrap decides, with `anywhere` distinguished from `break-word`
//     only by its effect on intrinsic sizing.
func wordBreakMode(st gcss.ComputedStyle) inline.WordBreakMode {
	switch st.WordBreak {
	case "break-all":
		return inline.WordBreakAll
	case "keep-all":
		return inline.WordBreakKeepAll
	}
	switch st.OverflowWrap {
	case "break-word":
		return inline.WordBreakWord
	case "anywhere":
		return inline.WordBreakAnywhere
	}
	return inline.WordBreakNormal
}

// inlineBlockCBWidth returns the containing-block width to lay a width:auto inline-block
// out against so it SHRINKS TO FIT its content (CSS 10.3.9) rather than filling the
// parent's content width (the default resolveContentWidth auto behavior). It returns a
// width W such that the auto branch (W - horiz) equals the shrink-to-fit content width
//
//	stf = min( max(min-content, available), max-content )
//
// where available = parentContentW - horiz (the inline-block's own margins/border/padding).
// A specified (non-auto) width is left to resolveContentWidth — this returns parentContentW
// unchanged so the specified width and its min/max clamp apply normally. Percentage widths
// also pass through (resolved against parentContentW). Min/max-content are measured via the
// memoized measure helpers (shared with table/grid sizing), so this adds no committed layout.
func (e *Engine) inlineBlockCBWidth(ctx context.Context, b *cssbox.Box, parentContentW float64) float64 {
	if _, isAuto := resolveLen(b.Style.Width, b.Style.FontSizePt, parentContentW); !isAuto {
		return parentContentW // specified width: normal resolution + clamp
	}
	ed := usedEdges(b, parentContentW)
	horiz := ed.mL + ed.mR + ed.bL + ed.bR + ed.pL + ed.pR
	avail := parentContentW - horiz
	if avail < 0 {
		avail = 0
	}
	maxC := e.measureMaxContent(ctx, b)
	minC := e.measureMinContent(ctx, b)
	stf := avail
	if minC > stf { // never narrower than min-content
		stf = minC
	}
	if maxC < stf { // never wider than max-content
		stf = maxC
	}
	if stf < 0 {
		stf = 0
	}
	return stf + horiz
}

// atomicRunFor builds the inline.Run for an atomic inline-level box (inline-block
// or replaced) whose border-box fragment is frag, resolving its horizontal margins
// against basis. The atom's advance is marginL + borderWidth + marginR, with the
// left margin recorded so the IFC offsets the fragment's border box past it. Negative
// margins are honored (they may pull the atom or following content leftward), matching
// the block flow's margin treatment.
//
// The atom's baseline (BaselinePt, measured from its border-box top) follows CSS 2.1
// §10.8.1 for vertical-align:baseline: it is the baseline of the atom's LAST in-flow
// line box, UNLESS the atom has no in-flow line boxes or its overflow is not visible —
// in which case it is the bottom margin edge (frag.H). So a replaced atom (an <img>: no
// line boxes) and an overflow:hidden inline-block stay bottom-aligned, while a plain
// inline-block with text aligns its text baseline with the surrounding line's baseline
// (rather than resting its whole box on the baseline, which dropped the box too low and
// inflated the line box). Full vertical-align keyword handling (sub/super/top/middle/…)
// is still deferred — this fixes only the default-baseline case.
func atomicRunFor(b *cssbox.Box, frag *Fragment, basis float64) inline.Run {
	ed := usedEdges(b, basis)
	baseline := frag.H // default: bottom margin edge (replaced, overflow≠visible, empty)
	if !clips(b) {
		if by, ok := lastInFlowLineBaseline(frag); ok {
			baseline = by - frag.Y // distance from the border-box top to that baseline
		}
	}
	return inline.Run{Atomic: &inline.AtomicItem{
		WidthPt:      ed.mL + frag.W + ed.mR,
		HeightPt:     frag.H,
		MarginLeftPt: ed.mL,
		BaselinePt:   baseline,
		Ref:          frag,
	}}
}

// lastInFlowLineBaseline returns the page-frame Y of the baseline of the LAST in-flow
// line box in frag's subtree (CSS 2.1 §10.8.1 "the baseline of its last line box in the
// normal flow"), and whether one exists. A fragment that establishes an inline formatting
// context carries its line boxes in Lines; a block-container fragment carries block
// children, so the last line box is found by recursing into its last IN-FLOW child
// (skipping out-of-flow positioned/floated children, which do not contribute a normal-flow
// line box). Returns ok=false for an atom with no in-flow line box (e.g. a replaced image,
// or an empty inline-block), so the caller falls back to the bottom margin edge.
func lastInFlowLineBaseline(frag *Fragment) (float64, bool) {
	if n := len(frag.Lines); n > 0 {
		return frag.Lines[n-1].BaselineY, true
	}
	for i := len(frag.Children) - 1; i >= 0; i-- {
		c := frag.Children[i]
		if c.IsPositioned || c.IsFloat {
			continue
		}
		if by, ok := lastInFlowLineBaseline(c); ok {
			return by, true
		}
	}
	return 0, false
}

// attrPx parses a width/height presentation attribute as a non-negative integer
// pixel count (treated 1:1 as points). A non-numeric or empty value yields 0.
func attrPx(s string) float64 {
	n := 0
	if s == "" {
		return 0
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0 // non-integer (e.g. "50%"): unsupported here, degrade to 0
		}
		n = n*10 + int(r-'0')
	}
	return float64(n)
}

// effectiveTextAlign resolves the alignment for b's inline content as an
// inline.Align. A real element box (BoxBlock / BoxInline) uses its own
// Style.TextAlign. An anonymous block (BoxAnonBlock) has a zero-value Style whose
// TextAlign is "", so the alignment is read from the inherited Style of its first
// descendant text/inline box that carries a non-empty TextAlign (the cascade copies
// text-align onto every inline descendant), falling back to left.
//
// The direction-relative keywords ("start"/"end", and the initial value) resolve
// against effectiveDirection(b), which applies the same anonymous-box recovery.
func effectiveTextAlign(b *cssbox.Box) inline.Align {
	dir := effectiveDirection(b)
	if b.Kind == cssbox.BoxAnonBlock {
		return mapTextAlign(firstInlineTextAlign(b), dir)
	}
	return mapTextAlign(b.Style.TextAlign, dir)
}

// textIndentPt resolves a block's text-indent to points. px/pt are absolute; em
// resolves against the font size; percent resolves against contentW; anything
// else (incl. an anonymous box's zero style) is 0. The value is signed: a negative
// result is a hanging indent (the first line starts to the left of the wrapped ones).
func textIndentPt(b *cssbox.Box, contentW float64) float64 {
	ti := b.Style.TextIndent
	switch ti.Unit {
	case gcss.UnitPx, gcss.UnitPt:
		return ti.Value
	case gcss.UnitEm:
		return ti.Value * b.Style.FontSizePt
	case gcss.UnitPercent:
		return ti.Value / 100 * contentW
	default:
		return 0
	}
}

// firstInlineTextAlign returns the first non-empty TextAlign found walking b's
// inline descendants depth-first, or "" if none carries one.
func firstInlineTextAlign(b *cssbox.Box) string {
	for _, c := range b.Children {
		if c.Style.TextAlign != "" {
			return c.Style.TextAlign
		}
		if a := firstInlineTextAlign(c); a != "" {
			return a
		}
	}
	return ""
}

// firstInlineLineHeight returns the inherited line-height carried by b's first
// BoxText descendant (walking inline descendants depth-first), with its font size
// for em resolution, or ok=false if b has no text leaf. It mirrors
// firstInlineTextAlign: an anonymous block's own Style is zero-valued, so its
// line-height (a CSS-inherited property) must be recovered from a text leaf, which
// carries the cascaded line-height/font-size from the real ancestor (see
// makeTextBox). The first BoxText is authoritative — every text leaf under the same
// anon block shares the inherited value.
func firstInlineLineHeight(b *cssbox.Box) (lh gcss.Length, fontSizePt float64, ok bool) {
	for _, c := range b.Children {
		if c.Kind == cssbox.BoxText {
			return c.Style.LineHeight, c.Style.FontSizePt, true
		}
		if l, fs, found := firstInlineLineHeight(c); found {
			return l, fs, true
		}
	}
	return gcss.Length{}, 0, false
}

// mapTextAlign maps a CSS text-align keyword to the inline core's neutral Align,
// resolving the direction-relative keywords ("start"/"end", and the initial value)
// against dir. The physical keywords never flip. An empty or unknown value, and an
// empty dir, default to AlignLeft — matching the pre-RTL behavior exactly, so LTR
// content is unaffected.
func mapTextAlign(s, dir string) inline.Align {
	switch s {
	case "right":
		return inline.AlignRight
	case "center":
		return inline.AlignCenter
	case "justify":
		return inline.AlignJustify
	case "start":
		if dir == "rtl" {
			return inline.AlignRight
		}
		return inline.AlignLeft
	case "end":
		if dir == "rtl" {
			return inline.AlignLeft
		}
		return inline.AlignRight
	default:
		return inline.AlignLeft
	}
}

// effectiveDirection resolves the used `direction` for b. It mirrors
// effectiveTextAlign: an anonymous box carries a zero-value Style whose Direction is
// "" (NOT "ltr"), so reading b.Style.Direction directly would silently lay an
// anonymous block out LTR inside an RTL subtree. The value is recovered from the
// first inline descendant that carries one, since the cascade copies the inherited
// direction onto every inline descendant.
//
// Always use this instead of b.Style.Direction — an empty value resolves to "ltr" so
// callers never have to spell the zero case themselves.
func effectiveDirection(b *cssbox.Box) string {
	d := b.Style.Direction
	if d == "" {
		d = firstInlineDirection(b)
	}
	if d == "" {
		return "ltr"
	}
	return d
}

// bidiDir maps a CSS `direction` value onto the inline core's neutral paragraph
// direction. Anything other than "rtl" (including the empty string) is left-to-right,
// matching effectiveDirection's fallback.
func bidiDir(dir string) inline.ParagraphDirection {
	if dir == "rtl" {
		return inline.DirRTL
	}
	return inline.DirLTR
}

// firstInlineDirection returns the first non-empty Direction found walking b's
// inline descendants depth-first, or "" if none carries one. Mirrors
// firstInlineTextAlign.
func firstInlineDirection(b *cssbox.Box) string {
	for _, c := range b.Children {
		if c.Style.Direction != "" {
			return c.Style.Direction
		}
		if d := firstInlineDirection(c); d != "" {
			return d
		}
	}
	return ""
}

// effectiveLineHeight computes a line's height from b's line-height and the line's
// own metrics. UnitAuto ("normal") uses the natural height (ascent+descent+gap)
// times cssDefaultLineMult; UnitPx/UnitPt is that exact value in points; UnitEm is
// the value times b's font size (see resolveLineHeight). A line containing atomic
// inline boxes is never shorter than its tallest atomic (which rests bottom-aligned
// on the baseline), so the atomic extent is a floor on every mode. Unitless
// multipliers (e.g. line-height:1.5) are not supported yet: the cascade drops a bare
// number, so they arrive as UnitAuto and degrade to the metrics-based default.
//
// An anonymous box (BoxAnonBlock/BoxAnonInline) is handled specially: it carries a
// zero-value ComputedStyle, so reading its own LineHeight gives Length{0, UnitPx} —
// which the UnitPx case would take literally as height 0, collapsing every line to a
// single baseline. But line-height is a CSS *inherited* property, so the real value
// lives on the anon box's text leaves (makeTextBox copies the cascaded line-height
// and font-size onto them). We therefore recover the inherited line-height (and the
// font size for em resolution) from the first text leaf via firstInlineLineHeight and
// resolve THAT — so e.g. a div with line-height:30px gives its bare-text anon block a
// 30px line box, not the auto default. Only when the anon box has no text leaf at all
// do we fall back to auto. (Same zero-value-Style-is-not-the-CSS-initial-value
// reasoning isAnonymous documents for width/height in block.go.)
func (e *Engine) effectiveLineHeight(b *cssbox.Box, line inline.Line) float64 {
	var h float64
	if isAnonymous(b) {
		if lh, fs, ok := firstInlineLineHeight(b); ok {
			h = resolveLineHeight(lh, fs, line)
		} else {
			h = autoLineHeight(line) // no text leaf: nothing to inherit from
		}
	} else {
		h = resolveLineHeight(b.Style.LineHeight, b.Style.FontSizePt, line)
	}
	if min := lineHeightMinPt(b); min > h {
		h = min
	}
	if atomic := atomicLineExtent(line); atomic > h {
		h = atomic
	}
	return h
}

// lineHeightMinPt resolves a block's line-height "at least" floor to points.
// px/pt are absolute; em resolves against the font size; anything else is 0 (no floor).
func lineHeightMinPt(b *cssbox.Box) float64 {
	m := b.Style.LineHeightMin
	switch m.Unit {
	case gcss.UnitPx, gcss.UnitPt:
		return m.Value
	case gcss.UnitEm:
		return m.Value * b.Style.FontSizePt
	default:
		return 0
	}
}

// resolveLineHeight turns a line-height Length plus a font size into a line height in
// points, against the line's own metrics: UnitPx/UnitPt is the exact value, UnitEm is
// the value times fontSizePt, and UnitAuto ("normal") or any unsupported unit falls
// back to the natural metrics-based height. Both the real-element path and the
// anonymous-block path (which supplies the inherited line-height/font-size from a text
// leaf) share this so the two stay in lockstep. The atomic-height floor is applied by
// the caller, not here.
func resolveLineHeight(lh gcss.Length, fontSizePt float64, line inline.Line) float64 {
	switch lh.Unit {
	case gcss.UnitPx, gcss.UnitPt:
		return lh.Value
	case gcss.UnitEm, gcss.UnitNumber:
		// A unitless number multiplies the font size, exactly as em does here. They
		// differ only in how they INHERIT (a number inherits as a number, an em as the
		// computed length), which is resolved by the cascade before this point.
		return lh.Value * fontSizePt
	case gcss.UnitPercent:
		// A percentage resolves against the element's own font size (CSS 2.1 10.8.1).
		return lh.Value / 100 * fontSizePt
	default: // UnitAuto / unsupported: metrics × default multiplier
		return autoLineHeight(line)
	}
}

// autoLineHeight is the "normal"/auto line height: the glyph extent
// (ascent+descent) times cssDefaultLineMult, which supplies the leading.
//
// The font's own line GAP is deliberately NOT added: the bundled substitute faces (the
// TeX Gyre family) report an anomalous hhea line gap of ~1.3–1.4 em, so adding it (and
// then multiplying by the 1.15 leading factor) ballooned "normal" to ~3 em — a 16px line
// came out ~49pt tall. Browsers compute "normal" from a font's ascent/descent (the Windows
// or typo metrics), not a runaway hhea gap; (ascent+descent)×1.15 reproduces a
// browser-comparable ~1.2 em for all three bundled families (mono ≈ 1.21, serif ≈ 1.54,
// sans ≈ 1.65 em) without the bad gap. A real (small) line gap from a well-behaved embedded
// font is likewise folded into the leading factor rather than added on top. (LineGapPt is
// still tracked on the line for any future metric that wants the raw value.)
func autoLineHeight(line inline.Line) float64 {
	return (line.AscentPt + line.DescentPt) * cssDefaultLineMult
}

// ascentOfLine returns the line's ascent used to place its baseline below the pen
// top: the greater of the text ascent and the tallest atom's above-baseline extent
// (inline.MakeLine records them separately). The baseline thus sits below a tall
// atom's top rather than the atom overflowing above the line; an all-atomic line
// (zero text ascent) still gets its ascent from the atom.
func ascentOfLine(line inline.Line) float64 {
	if line.AtomAscentPt > line.AscentPt {
		return line.AtomAscentPt
	}
	return line.AscentPt
}

// atomicLineExtent returns the vertical extent an atomic inline box needs about the
// baseline (its above-baseline ascent plus below-baseline descent). It is the floor
// effectiveLineHeight applies to the line HEIGHT (vertical advance) so a fixed
// line-height shorter than a tall atom (e.g. line-height:10px with a 100px image)
// still reserves room for the atom — the atom-aware ascent places the baseline
// correctly, but a fixed line-height ignores ascent, so the height floor remains
// necessary. A line with no atoms returns 0.
func atomicLineExtent(line inline.Line) float64 {
	return line.AtomAscentPt + line.AtomDescentPt
}

// translateFragment shifts fragment f and its whole subtree by (dx, dy): the
// border box, its inline line glyph X / baselines, and every descendant fragment.
// It is used to move an atomic inline box's already-laid-out fragment from its
// provisional origin to its position on a line; unlike shiftFragment (block flow,
// vertical-only) it moves X too, because an atomic's horizontal position is set
// here rather than by the block stacker. Floats are recursed in addition to Children
// so a repositioned nested BFC carries its inner floats by the same delta.
func translateFragment(f *Fragment, dx, dy float64) {
	if dx == 0 && dy == 0 {
		return
	}
	f.X += dx
	f.Y += dy
	for li := range f.Lines {
		f.Lines[li].BaselineY += dy
		for gi := range f.Lines[li].Glyphs {
			f.Lines[li].Glyphs[gi].X += dx
		}
	}
	if f.Image != nil {
		f.Image.CX += dx
		f.Image.CY += dy
	}
	if f.Vector != nil {
		f.Vector.CX += dx
		f.Vector.CY += dy
	}
	if f.Control != nil {
		f.Control.CX += dx
		f.Control.CY += dy
	}
	for _, c := range f.Children {
		translateFragment(c, dx, dy)
	}
	for _, fl := range f.Floats {
		translateFragment(fl, dx, dy)
	}
	// Move the page-space fields the fragment owns (clip rect, collapsed grid strips,
	// positioned clip chains, and out-of-flow positioned descendants) by the same delta,
	// AFTER the Children/Floats recursion so an abs/fixed Positioned entry — present only
	// here, never in Children — moves exactly once. See shiftFragmentExtras.
	shiftFragmentExtras(f, dx, dy)
}

// inlineBoxStyleFor builds the paintable decoration of a non-replaced inline box, or
// nil when the box paints nothing — which is the overwhelmingly common case, so an
// ordinary paragraph allocates nothing and produces byte-identical runs.
//
// Only what the inline paint path honors is read. CSS also gives inline boxes vertical
// padding and margins, which overflow the line box without growing it, and background
// images; those are deliberately left out rather than half-applied, so the rect that
// paints is exactly the rect this describes. See appendInlineBoxDecorations.
func inlineBoxStyleFor(b *cssbox.Box) *inline.InlineBoxStyle {
	st := &b.Style
	// A border paints only when it has a width, a visible style, and a visible color —
	// the same three-way test the block border path applies.
	borderW := 0.0
	if st.BorderLeftStyle != "" && st.BorderLeftStyle != "none" && st.BorderLeftStyle != "hidden" {
		if w, _ := resolveLen(st.BorderLeftWidth, st.FontSizePt, 0); w > 0 {
			borderW = w
		}
	}
	d := &inline.InlineBoxStyle{
		Background:    st.BackgroundColor,
		BorderColor:   st.BorderLeftColor,
		BorderWidthPt: borderW,
	}
	// Horizontal padding resolves against the containing block's WIDTH in CSS. The
	// percentage basis is not threaded into this helper, so a PERCENTAGE inline padding
	// resolves to 0 rather than being guessed against the wrong basis — a known limit,
	// not a silently wrong value.
	d.PaddingLeftPt, _ = resolveLen(st.PaddingLeft, st.FontSizePt, 0)
	d.PaddingRightPt, _ = resolveLen(st.PaddingRight, st.FontSizePt, 0)
	if !d.Paints() && !d.Reserves() {
		return nil
	}
	return d
}

// edgeRun builds the zero-ink run carrying one side's padding+border. It takes the
// box's font so the edge glyph carries the same metrics as its text, which is what
// gives an empty or all-whitespace decorated span a rect with a height.
func edgeRun(b *cssbox.Box, style *inline.InlineBoxStyle, edge inline.InlineEdge) inline.Run {
	return inline.Run{
		Family:        b.Style.FontFamily,
		Bold:          b.Style.Bold,
		Italic:        b.Style.Italic,
		SizePt:        b.Style.FontSizePt,
		InlineBox:     style,
		InlineBoxEdge: edge,
	}
}

// ellipsisGlyphFor shapes the ellipsis for a line, styled like the run it will
// terminate: the last glyph's family/size/colour, so the ellipsis matches the text it
// truncates rather than the block's own defaults.
//
// ok=false when the face has no U+2026 (or no face resolves), in which case the caller
// leaves the line untruncated — a hard cut is honest, a substituted box glyph is not.
func (e *Engine) ellipsisGlyphFor(b *cssbox.Box, line []inline.Glyph) (inline.Glyph, bool) {
	family, size := b.Style.FontFamily, b.Style.FontSizePt
	style := pkgfont.Style{Bold: b.Style.Bold, Italic: b.Style.Italic}
	col := inline.Color{R: b.Style.Color.R, G: b.Style.Color.G, B: b.Style.Color.B, A: b.Style.Color.A}
	if col.A == 0 {
		col.A = 0xff
	}
	// Prefer the LAST inked glyph's metrics: a line ending in a larger or differently
	// styled span should get an ellipsis sized to match it.
	for i := len(line) - 1; i >= 0; i-- {
		if g := &line[i]; g.Outline != nil && g.SizePt > 0 {
			size, col = g.SizePt, g.Color
			break
		}
	}
	if size <= 0 {
		return inline.Glyph{}, false
	}
	return inline.ShapeEllipsis(e.faces, family, style, size, col)
}

// hasColorInk reports whether a glyph paints through a colour-font path (COLR layers or
// a bitmap strike) rather than through an outline. Such a glyph must survive the emit
// even though Outline is nil, or an emoji is dropped between shaping and paint.
func hasColorInk(g *inline.Glyph) bool {
	if g.Face == nil || g.Break || g.Atomic != nil {
		return false
	}
	if g.Face.HasColorBitmaps() {
		return true
	}
	if g.Face.HasColorGlyphs() {
		layers, ok := g.Face.ColorLayers(g.GID)
		return ok && len(layers) > 0
	}
	return false
}
