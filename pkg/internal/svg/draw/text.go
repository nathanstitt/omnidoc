package draw

import (
	"math"
	"strings"

	layoutfont "github.com/nathanstitt/omnidoc/pkg/internal/layout/font"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/inline"
	"github.com/nathanstitt/omnidoc/pkg/internal/render"
	"github.com/nathanstitt/omnidoc/pkg/internal/svg"
)

// placedGlyph is one shaped glyph with its final pen position and per-
// character rotation resolved, produced by the measure pass and consumed by
// the paint pass.
//
// The two passes exist because text-anchor shifts a whole text chunk by its
// own total advance, which is not known until every glyph in that chunk has
// been shaped and measured. Measuring first also means the shaper runs
// exactly once per style run, not once per pass.
type placedGlyph struct {
	// glyph is the shaped glyph: its Outline is in em units, Y UP, and must
	// be scaled by SizePt and Y-flipped to reach SVG user space.
	glyph inline.Glyph

	// penX, penY is the glyph's origin on the baseline, in the <text>
	// element's user space, before any text-anchor shift.
	penX, penY float64

	// rotateRad is the per-character rotation about the glyph's own origin. In
	// a vertical writing mode it carries the text-orientation turn as well,
	// summed with the character's own rotate= (see layoutText).
	rotateRad float64

	// vertical marks a glyph laid out in a vertical writing mode, where the pen
	// walked down the page and `advance` is the font's VERTICAL advance. The
	// later passes need it because they are otherwise free to assume the run
	// extends along X: anchor shifts move along the run's own axis, and the
	// decoration extents span it.
	vertical bool

	// xScale horizontally scales the glyph OUTLINE, for
	// lengthAdjust="spacingAndGlyphs". It is 1 for every other case, and the
	// scale is applied in the glyph's own em space (before the rotation and
	// the translation to the pen), so a stretched glyph stays anchored at its
	// own origin rather than sliding.
	xScale float64

	// advance is the glyph's EFFECTIVE advance: the shaped advance plus any
	// letter-spacing/word-spacing, then scaled or padded by a textLength.
	// It is what the anchor measurement and the decoration extents read, so
	// they see the same widths the pen actually walked.
	advance float64

	// charIndex is the index in Text.Chars of the source character, kept so a
	// textLength (which addresses a CHARACTER range) can select the glyphs it
	// governs after shaping has already collapsed the two apart.
	charIndex int

	// chunk indexes the text chunk this glyph belongs to, so the anchor shift
	// resolved for that chunk is applied to exactly its own glyphs.
	chunk int

	// style is the resolved style at the glyph's source character, carrying
	// fill/stroke paint and the paint servers.
	style svg.Style

	// fillGradient/strokeGradient/fillPattern/strokePattern are the character's
	// resolved paint servers, carried through so paintGlyph can route them to
	// the same fill/stroke helpers a Shape uses.
	fillGradient, strokeGradient gradient
	fillPattern, strokePattern   pattern

	// clip and mask are the clip-path/mask in effect for this glyph, set by a
	// <tspan> that carries one (SVG 2). Glyphs sharing the same pair are
	// composited together in one offscreen group — see paintTextRuns — rather
	// than one group per glyph, which would both cost a scratch buffer each
	// and break a mask whose effect spans the whole run.
	clip *svg.ClipPath
	mask *svg.Mask
}

// matrix returns the transform mapping this glyph's outline from em space
// into the space tm maps from, where tm is the <text>'s own accumulated
// matrix. Innermost first:
//
//  1. Scale by SizePt and FLIP Y. Glyph outlines are in em units with Y UP
//     (see inline.Glyph.Outline); SVG user space has Y DOWN, so the sign flip
//     is what puts the glyph the right way up.
//  2. Rotate by the character's own rotate angle, about the glyph's ORIGIN —
//     which is the origin of the scaled space, so it composes before the
//     translation rather than after. Composing it the other way would rotate
//     each glyph about the text's origin instead of its own, fanning the
//     glyphs out along an arc.
//  3. Translate to the pen position on the baseline.
//  4. Compose with tm.
//
// Painting and clip-geometry extraction share this so the outlines a
// <clipPath> gets are provably the same ones a fill would have drawn.
// The horizontal glyph scale for lengthAdjust="spacingAndGlyphs" folds into
// step 1, multiplying the X size only: it is defined in the glyph's own space
// about its origin, so it must compose innermost of all.
func (p *placedGlyph) matrix(tm render.Matrix) render.Matrix {
	size := p.glyph.SizePt
	xs := p.xScale
	if xs == 0 {
		xs = 1 // defensive: a zero-initialised placedGlyph must still paint
	}
	m := render.Scale(size*xs, -size)
	if (!p.vertical || p.rotateRad != 0) && (p.glyph.XShiftPt != 0 || p.glyph.BaselineShiftPt != 0) {
		// A GPOS mark placement (an Arabic dot on its letter) moves the glyph off its
		// pen in the glyph's own frame, before any rotation, so a sideways glyph in a
		// vertical run keeps its dot too. An upright glyph in a vertical run is laid
		// out on the vertical axis, where the horizontal placement does not apply.
		m = m.Mul(render.Translate(p.glyph.XShiftPt, -p.glyph.BaselineShiftPt))
	}
	if p.rotateRad != 0 {
		m = m.Mul(render.Rotate(p.rotateRad))
	}
	return m.Mul(render.Translate(p.penX, p.penY)).Mul(tm)
}

// faces returns the Renderer's font-face cache, creating it on first use.
//
// layoutfont.FaceCache is itself mutex-protected and safe for concurrent use,
// so sharing one across the engine's parallel page-render fan-out is exactly
// what it is designed for — and sharing matters, since parsing a font program
// is the expensive step and every <text> in a document resolves the same few
// families. The sync.Once keeps Renderer's "no mutable state" contract
// honest: the cache is created once and never replaced, so concurrent
// DrawVector calls cannot race on the field.
func (r *Renderer) faces() *layoutfont.FaceCache {
	r.faceOnce.Do(func() {
		r.faceCache = layoutfont.NewFaceCache()
	})
	return r.faceCache
}

// paintText paints one Text node: shape, measure, resolve each chunk's
// text-anchor shift, then paint every glyph as ordinary vector geometry.
//
// Glyphs go through FillGlyph/Stroke, never Device.DrawGlyph. DrawGlyph emits
// PDF text-showing operators in the pdfwrite backend, which cannot express
// what SVG text needs: independent fill AND stroke paint on the same glyph,
// an arbitrary per-glyph transform (SVG's rotate list), or a glyph
// participating in a clip path or mask as ordinary geometry. Routing through
// the same paintFill/paintStroke helpers paintShape uses is also what gives
// text gradients and patterns for free. The cost, stated plainly: SVG text in
// PDF output is vector outlines, not selectable text.
func (r *Renderer) paintText(dev render.Device, t *svg.Text, m render.Matrix, alpha float64, warned *warnFlags) {
	if t == nil || len(t.Chars) == 0 {
		return
	}
	if warned.drawCalls >= maxDrawCalls {
		r.logDrawBudgetCapOnce(warned)
		return
	}

	tm := t.M.Mul(m)
	placed := r.layoutText(t)
	if len(placed) == 0 {
		return
	}

	if t.Filter != nil {
		// The objectBoundingBox target is textUserBounds — the REAL placed-
		// glyph extent — not pkg/svg's build-time textBBox, whose half-em-
		// per-character estimate measures up to 2.25x off. A filter region
		// built on that estimate would visibly clip the filtered result,
		// which is exactly why the filter work owns this seam (see
		// svg.Text.Filter).
		//
		// The <text> element's OWN opacity is stripped from the source pass
		// and re-applied to the filtered RESULT, exactly as paintShape and
		// paintGroupBody do for their node kinds — SVG applies a filter
		// BEFORE opacity. Text needs this handled per CHARACTER rather than
		// on a single node field: opacity reaches a glyph through its own
		// TextChar style (see paintGlyph), so clearing it on the Text node
		// alone would leave every glyph still dimming itself inside the
		// filter's source buffer. That is invisible for most primitives but
		// total for one that discards its input: an feFlood under
		// opacity="0.5" comes out fully opaque.
		//
		// clip-path and mask are likewise stripped from the source pass and
		// applied to the filtered RESULT, since the same filter → clip → mask
		// → opacity order governs all three — see paintFilteredThenClip.
		elemOpacity := clamp01(t.Opacity)
		unfiltered := *t
		unfiltered.Filter = nil
		unfiltered.Opacity = 1
		unfiltered.ClipPath = nil
		unfiltered.Mask = nil
		unfiltered.Chars = charsWithoutElementOpacity(t.Chars, elemOpacity)
		bounds := textUserBounds(placed)
		r.paintFilteredThenClip(dev, t.ClipPath, t.Mask, tm, bounds, alpha*elemOpacity, func(target render.Device, a float64) {
			r.paintFilteredAlpha(target, t.Filter, tm, bounds, warned, a, func(inner render.Device) {
				r.paintText(inner, &unfiltered, m, 1, warned)
			})
		})
		return
	}
	if t.ClipPath != nil || t.Mask != nil {
		// The <text>'s OWN clip-path/mask wrap everything as one unit, so the
		// whole node goes into a single group. Any per-<tspan> clip/mask
		// inside it still applies, nested one level down.
		r.paintTextGrouped(dev, t, placed, tm, alpha, warned)
		return
	}
	// Underline and overline paint UNDER the glyphs, line-through OVER them —
	// SVG2 §11.11's painting order, and the only order that keeps a
	// line-through visible on text with a heavy stroke.
	r.paintDecorations(dev, placed, tm, alpha, warned, decorationsUnder)
	r.paintTextRuns(dev, placed, tm, alpha, warned)
	r.paintDecorations(dev, placed, tm, alpha, warned, decorationsOver)
}

// decorationsUnder and decorationsOver name the two decoration painting
// passes: the lines drawn beneath the glyphs and those drawn on top. See
// paintDecorations.
const (
	decorationsUnder = false
	decorationsOver  = true
)

// paintDecorations draws the text-decoration lines for placed, as filled (and
// optionally stroked) rectangles.
//
// The subtlety this implements, and the reason a decoration is not just a
// boolean on a glyph: SVG resolves a decoration's PAINT and its
// position/thickness from the element that DECLARED it, never from the
// descendant characters it happens to cover. So
//
//	<text fill="red" text-decoration="underline"><tspan fill="blue">x</tspan></text>
//
// draws blue text under a RED underline. resvg's style-resolving-1..4 and
// indirect-with-multiple-colors fixtures assert every corner of this,
// including that the declaring element's FONT-SIZE — not the covered text's —
// sets the line's thickness and offset (style-resolving-4 draws a 200px-scaled
// rule under 48px text).
//
// Extent: a decoration spans the whole contiguous run of glyphs that inherited
// the SAME declaration, so it crosses <tspan> boundaries inside the declaring
// element and stops where the declaration does. Runs are keyed by the
// declaring style POINTER, which svg.Style.DecorationLines hands back per
// declaration, so two different elements declaring the same line keyword still
// draw two separate rules (indirect-with-multiple-colors).
//
// over selects the pass: false draws underline and overline, true draws
// line-through.
func (r *Renderer) paintDecorations(dev render.Device, placed []placedGlyph, tm render.Matrix, alpha float64, warned *warnFlags, over bool) {
	for i := 0; i < len(placed); {
		lines, styles := placed[i].style.DecorationLines()
		if len(lines) == 0 {
			i++
			continue
		}
		// Extend the run while the decoration SET is identical. Comparing the
		// styles by value would merge two sibling tspans that each declared
		// their own identical underline into one rule — visually the same
		// here, but wrong the moment their paint differs, so the whole set is
		// compared instead.
		j := i + 1
		for j < len(placed) && sameDecorations(placed[i].style, placed[j].style) {
			j++
		}
		run := placed[i:j]
		for k, line := range lines {
			if (line == "line-through") != over {
				continue
			}
			if warned.drawCalls >= maxDrawCalls {
				r.logDrawBudgetCapOnce(warned)
				return
			}
			r.paintDecorationLine(dev, run, line, styles[k], tm, alpha, warned)
		}
		i = j
	}
}

// sameDecorations reports whether two styles carry the identical decoration
// declarations — same lines, in the same order, declared by the same elements.
func sameDecorations(a, b svg.Style) bool {
	al, as := a.DecorationLines()
	bl, bs := b.DecorationLines()
	if len(al) != len(bl) {
		return false
	}
	for i := range al {
		if al[i] != bl[i] || !sameDecorationPaint(as[i], bs[i]) {
			return false
		}
	}
	return true
}

// sameDecorationPaint reports whether two declaring styles would draw an
// identical rule: same fill, same stroke, same font size. Those are the only
// properties paintDecorationLine reads, so comparing more would split runs for
// differences that cannot show.
func sameDecorationPaint(a, b svg.Style) bool {
	af, aok := a.FillPaint()
	bf, bok := b.FillPaint()
	if aok != bok || af != bf {
		return false
	}
	as, aok := a.StrokePaint()
	bs, bok := b.StrokePaint()
	if aok != bok {
		return false
	}
	if aok && (as.Color != bs.Color || as.Width != bs.Width) {
		return false
	}
	return a.FontSizePt() == b.FontSizePt()
}

// paintDecorationLine draws one decoration rule across run, as a filled
// rectangle per contiguous group of glyphs that share a baseline FRAME.
//
// It is not one rectangle across the whole run, and that is load-bearing.
// SVG's per-character y/dy/rotate lists move each glyph independently, and a
// decoration follows them: resvg's underline-with-dy-list-1 staircases its
// underline down with the text, and underline-with-rotate-list-3 tilts each
// segment to its glyph's own angle. Splitting at every change of pen Y or
// rotation reproduces both, and collapses to the single rectangle of the
// ordinary flat case because a flat run never changes frame.
//
// Geometry comes from the DECLARING style's font size (so style-resolving-4's
// 200px-scaled rule under 48px text is thick and far below) and the covered
// glyphs' face metrics for the overline's ascent. pkg/font parses no `post`
// table, so a face's own underlinePosition/underlineThickness are unavailable
// and the em fractions below are the conventional fallbacks.
//
// Each segment is a rectangle, not a stroked line, so it fills and strokes
// through the same device calls a glyph outline does — which is what makes a
// decoration honour stroke width, dashes, and opacity without a second paint
// path.
func (r *Renderer) paintDecorationLine(dev render.Device, run []placedGlyph, line string, declaring svg.Style, tm render.Matrix, alpha float64, warned *warnFlags) {
	size := declaring.FontSizePt()
	if len(run) == 0 || size <= 0 {
		return
	}
	for i := 0; i < len(run); {
		j := i + 1
		// A ROTATED glyph is never merged with its neighbour, even one at the
		// same angle: SVG rotates each glyph about its OWN origin, so two
		// glyphs sharing an angle still sit on two different (parallel but
		// offset) baselines, and one rectangle spanning both would drift away
		// from the second. resvg's underline-with-rotate-list-3.svg draws four
		// separate tilted segments under a uniform rotate="15" for exactly
		// this reason.
		if run[i].rotateRad == 0 {
			for j < len(run) && run[j].penY == run[i].penY && run[j].rotateRad == 0 {
				j++
			}
		}
		if warned.drawCalls >= maxDrawCalls {
			r.logDrawBudgetCapOnce(warned)
			return
		}
		r.paintDecorationSegment(dev, run[i:j], line, declaring, size, tm, alpha, warned)
		i = j
	}
}

// paintDecorationSegment draws one rule segment: the glyphs in seg all share a
// pen Y and a rotation, so one rectangle in their common frame covers them.
//
// The rectangle is built in the segment's UNROTATED frame — x from the first
// glyph's pen to the last glyph's advance edge, y offset from their shared
// baseline — and then rotated about the segment's own start, exactly as
// placedGlyph.matrix rotates a glyph about its origin. That keeps a rotated
// underline parallel to the glyphs it sits under instead of staying axis
// aligned.
func (r *Renderer) paintDecorationSegment(dev render.Device, seg []placedGlyph, line string, declaring svg.Style, size float64, tm render.Matrix, alpha float64, warned *warnFlags) {
	// The segment spans its own INLINE axis: X horizontally, Y in a vertical
	// writing mode. Both are measured into the same x0/x1 pair because the rect
	// below is built in the segment's UNROTATED frame — where the inline axis is
	// always the local X — and the matrix then rotates and translates it into
	// place. A vertical glyph already carries the text-orientation turn in
	// rotateRad, so an upright vertical run's underline lands beside the text
	// and a sideways one's lands under it, both without a second code path.
	vertical := seg[0].vertical
	inlinePen := func(g *placedGlyph) float64 {
		if vertical {
			return g.penY
		}
		return g.penX
	}
	x0, x1 := math.Inf(1), math.Inf(-1)
	for i := range seg {
		x0 = math.Min(x0, inlinePen(&seg[i]))
		x1 = math.Max(x1, inlinePen(&seg[i])+seg[i].advance)
	}
	if !(x1 > x0) {
		return // an all-zero-advance segment has no extent to decorate
	}

	thickness := size * decorationThicknessEm
	var top float64
	switch line {
	case "underline":
		top = size * underlineOffsetEm
	case "overline":
		// One ascent above the baseline. The declaring element resolves no
		// face of its own (it may have covered no characters directly), so the
		// segment's ascent, rescaled from its size to the declaring size, is
		// the closest available reading.
		top = -ascentFor(&seg[0].glyph, size) - thickness
	case "line-through":
		top = -size * strikeOffsetEm
	default:
		return
	}

	// The rect is built in the segment's own unrotated frame, with local X along
	// the inline axis and local Y across it — the same frame a glyph's outline
	// occupies before placedGlyph.matrix rotates it. Offsets are relative to the
	// FIRST glyph's pen on the inline axis, so local (0,0) is that pen.
	start := inlinePen(&seg[0])
	rect := &render.Path{}
	rect.MoveTo(x0-start, top)
	rect.LineTo(x1-start, top)
	rect.LineTo(x1-start, top+thickness)
	rect.LineTo(x0-start, top+thickness)
	rect.Close()

	m := render.Identity
	if rot := seg[0].rotateRad; rot != 0 {
		m = m.Mul(render.Rotate(rot))
	}
	// Then translate that local origin to the first glyph's pen in user space.
	// The rotation above has already oriented the frame, so this is the glyph's
	// own pen either way — no axis swap here, which is what an earlier revision
	// got wrong by swapping the translation as well as the measurement and
	// scattering the rule beside the text.
	m = m.Mul(render.Translate(seg[0].penX, seg[0].penY)).Mul(tm)

	dp := render.TransformPath(rect, m)
	if dp == nil {
		return
	}
	warned.drawCalls++
	a := alpha * clamp01(declaring.Opacity())
	if fp, ok := declaring.FillPaint(); ok {
		fp.Color.A = scaleAlpha(fp.Color.A, a)
		dev.Fill(dp, fp)
	}
	if sp, ok := declaring.StrokePaint(); ok {
		sf := tm.ScaleFactor()
		if sf == 0 {
			return
		}
		sp.Color.A = scaleAlpha(sp.Color.A, a)
		sp.Width *= sf
		sp.DashPhase *= sf
		if sp.DashArray != nil {
			scaled := make([]float64, len(sp.DashArray))
			for i, d := range sp.DashArray {
				scaled[i] = d * sf
			}
			sp.DashArray = scaled
		}
		dev.Stroke(dp, sp)
	}
}

// ascentFor returns a glyph's ascent rescaled to size points, falling back to
// the 0.8em approximation when the face exposed no extents.
func ascentFor(g *inline.Glyph, size float64) float64 {
	if g.SizePt > 0 && g.AscentPt > 0 {
		return g.AscentPt / g.SizePt * size
	}
	return size * 0.8
}

// descentFor is ascentFor's counterpart: a glyph's descent (a positive magnitude
// BELOW the baseline) rescaled to size points, with the 0.2em fallback that
// complements ascentFor's 0.8em so the two always sum to one em.
//
// Only the vertical writing-mode path reads it, to centre a sideways glyph across
// the baseline — see verticalGlyph.
func descentFor(g *inline.Glyph, size float64) float64 {
	if g.SizePt > 0 && g.DescentPt > 0 {
		return g.DescentPt / g.SizePt * size
	}
	return size * 0.2
}

// The em fractions a text-decoration rule uses for its position and weight.
// pkg/font parses no `post` table, so a face's own underlinePosition and
// underlineThickness are unavailable; these are the conventional fallbacks,
// and they are what every fixture in resvg's text-decoration/ tranche is
// compared against.
const (
	// underlineOffsetEm is how far BELOW the baseline the underline's top edge
	// sits.
	underlineOffsetEm = 0.09
	// strikeOffsetEm is how far ABOVE the baseline the line-through's top edge
	// sits — roughly the middle of the lowercase x-height.
	strikeOffsetEm = 0.26
	// decorationThicknessEm is every rule's height.
	decorationThicknessEm = 0.07
)

// paintTextRuns paints placed in maximal runs of glyphs sharing the same
// per-<tspan> clip-path/mask pair: a run with neither paints directly, a run
// with either goes through one offscreen group.
//
// Grouping by run rather than per glyph is not just an optimisation. A mask
// is evaluated over the region it covers, so applying it separately to each
// glyph would give every glyph its own independent mask evaluation instead of
// one shared across the tspan — visibly wrong for any mask with a gradient or
// a shape smaller than the text. Per-glyph grouping would also allocate a
// full-canvas scratch buffer per character.
func (r *Renderer) paintTextRuns(dev render.Device, placed []placedGlyph, tm render.Matrix, alpha float64, warned *warnFlags) {
	for i := 0; i < len(placed); {
		clip, mask := placed[i].clip, placed[i].mask
		j := i
		for j < len(placed) && placed[j].clip == clip && placed[j].mask == mask {
			j++
		}
		run := placed[i:j]
		if clip == nil && mask == nil {
			for k := range run {
				if warned.drawCalls >= maxDrawCalls {
					r.logDrawBudgetCapOnce(warned)
					return
				}
				r.paintGlyph(dev, &run[k], tm, alpha, warned)
			}
		} else {
			r.paintGlyphRunGrouped(dev, run, clip, mask, tm, alpha, warned)
		}
		i = j
	}
}

// paintGlyphRunGrouped paints one run of glyphs through an offscreen group so
// a <tspan>'s clip-path and/or mask can be applied to the run as a unit —
// EndGroup being the only place a GroupMask can be applied, exactly as in
// paintShapeGrouped.
func (r *Renderer) paintGlyphRunGrouped(dev render.Device, run []placedGlyph, clip *svg.ClipPath, mask *svg.Mask, tm render.Matrix, alpha float64, warned *warnFlags) {
	if warned.groupDepth >= maxGroupNestingDepth {
		// Bounds concurrently-live scratch memory, not just CPU: see
		// paintShapeGrouped's matching guard. Degrade to painting the run
		// unclipped and unmasked rather than dropping it.
		r.logGroupDepthCapOnce(warned)
		for k := range run {
			r.paintGlyph(dev, &run[k], tm, alpha, warned)
		}
		return
	}
	dev.Save()
	dev.BeginGroup()
	warned.groupDepth++
	for k := range run {
		if warned.drawCalls >= maxDrawCalls {
			r.logDrawBudgetCapOnce(warned)
			break
		}
		r.paintGlyph(dev, &run[k], tm, 1.0, warned)
	}
	warned.groupDepth--

	bbox := textUserBounds(run)
	var clipMask, softMask render.GroupMask
	if clip != nil {
		clipMask = r.buildClipMask(dev, clip, tm, bbox)
	}
	if mask != nil {
		// Passed to EndGroup separately from clipMask, never pre-combined —
		// see paintShapeGrouped for the pdfwrite regression that caused.
		softMask = r.buildMask(dev, mask, tm, bbox)
	}
	dev.EndGroup(alpha, "", clipMask, softMask)
	dev.Restore()
}

// paintTextGrouped paints text carrying a clip-path or mask, which — exactly
// like paintShapeGrouped — requires an offscreen group because EndGroup is
// the only place a GroupMask can be applied.
//
// The objectBoundingBox target passed to buildClipMask/buildMask is the
// text's own device-space glyph bounds, computed from the placed glyphs: text
// has no single pre-transform Path the way a Shape does, so the box is
// measured here rather than read off the node.
func (r *Renderer) paintTextGrouped(dev render.Device, t *svg.Text, placed []placedGlyph, tm render.Matrix, alpha float64, warned *warnFlags) {
	if warned.groupDepth >= maxGroupNestingDepth {
		// Same memory-bounding rationale as paintShapeGrouped's guard:
		// degrade to painting without isolation, clip, or mask rather than
		// dropping the text entirely.
		r.logGroupDepthCapOnce(warned)
		r.paintDecorations(dev, placed, tm, alpha, warned, decorationsUnder)
		r.paintTextRuns(dev, placed, tm, alpha, warned)
		r.paintDecorations(dev, placed, tm, alpha, warned, decorationsOver)
		return
	}
	dev.Save()
	dev.BeginGroup()
	warned.groupDepth++
	// Through paintTextRuns, not a bare glyph loop: a <tspan> INSIDE a
	// clipped/masked <text> carries its own clip/mask, which must still
	// apply, nested one level inside this group.
	//
	// Decorations join the same group so the <text>'s clip-path/mask cuts
	// them alongside the glyphs, which is what makes a clipped underline
	// stop at the clip boundary rather than running past it.
	r.paintDecorations(dev, placed, tm, 1.0, warned, decorationsUnder)
	r.paintTextRuns(dev, placed, tm, 1.0, warned)
	r.paintDecorations(dev, placed, tm, 1.0, warned, decorationsOver)
	warned.groupDepth--

	bbox := textUserBounds(placed)
	var clipMask, softMask render.GroupMask
	if t.ClipPath != nil {
		clipMask = r.buildClipMask(dev, t.ClipPath, tm, bbox)
	}
	if t.Mask != nil {
		// Composite order is clip -> mask -> opacity, and the two masks reach
		// EndGroup SEPARATELY rather than pre-combined — see paintShapeGrouped
		// for the pdfwrite regression pre-combining caused.
		softMask = r.buildMask(dev, t.Mask, tm, bbox)
	}
	dev.EndGroup(alpha, "", clipMask, softMask)
	dev.Restore()
}

// textUserBounds returns a bounds function over the placed glyphs' user-space
// extent, in the shape buildClipMask/buildMask expect for an
// objectBoundingBox target (the same signature *render.Path.Bounds has).
//
// The box is the union of each glyph's em box at its pen position rather than
// its true ink extent: an exact ink box would need every outline transformed
// and measured, and objectBoundingBox on text is already an approximation
// (see pkg/svg's textBBox) — the em box is the stable, cheap choice, and it
// is what an author reasoning about a text bounding box expects.
func textUserBounds(placed []placedGlyph) func() (float64, float64, float64, float64, bool) {
	return func() (minX, minY, maxX, maxY float64, ok bool) {
		for i := range placed {
			p := &placed[i]
			size := p.glyph.SizePt
			x0, y0 := p.penX, p.penY-size*0.8
			x1, y1 := p.penX+p.advance, p.penY+size*0.2
			if !ok {
				minX, minY, maxX, maxY, ok = x0, y0, x1, y1, true
				continue
			}
			minX, minY = math.Min(minX, x0), math.Min(minY, y0)
			maxX, maxY = math.Max(maxX, x1), math.Max(maxY, y1)
		}
		return minX, minY, maxX, maxY, ok
	}
}

// layoutText runs the measure pass: it shapes each maximal run of characters
// sharing one style, walks the flat glyph slice accumulating advances while
// applying the per-character absolute/relative position adjustments, applies
// bidi reordering per chunk, and finally shifts each chunk by its resolved
// text-anchor offset.
//
// Note what it does NOT call: inline.Break, BreakNextWrap, MakeLine, or
// Place. Those are box-layout machinery — they need a containing width to
// break against, and SVG text does not wrap. inline.Shape is a pure function
// that is not fused to line-breaking (it returns a flat []Glyph), which is
// exactly what makes reusing the engine's real shaper here possible instead
// of writing a second one. Arabic harfbuzz shaping and per-rune script
// fallback both happen INSIDE Shape, so SVG text gets them for free.
func (r *Renderer) layoutText(t *svg.Text) []placedGlyph {
	glyphs := r.shapeChars(t.Chars)
	if len(glyphs) == 0 {
		return nil
	}

	// textLength governs a CHARACTER range and needs each glyph's natural
	// advance, so it is resolved into a per-glyph correction BEFORE the pen
	// walk and consumed inside it. Spacing is folded in first, because SVG
	// measures the natural width a textLength corrects WITH the element's
	// letter/word-spacing already applied.
	r.applyTextSpacing(glyphs, t)
	gaps := r.applyTextLengths(glyphs, t)

	// Place every glyph, threading the pen through the character list.
	placed := make([]placedGlyph, 0, len(glyphs))
	penX, penY := 0.0, 0.0
	chunk := -1
	nextAnchor := 0
	for i := range glyphs {
		g := &glyphs[i]
		c := &t.Chars[g.charIndex]

		if nextAnchor < len(t.Anchors) && g.charIndex >= t.Anchors[nextAnchor] {
			// Advance to the chunk this character belongs to. A chunk can be
			// entered by a character whose index is past the recorded anchor
			// when earlier characters produced no glyphs, so this walks
			// forward rather than testing for equality.
			for nextAnchor < len(t.Anchors) && g.charIndex >= t.Anchors[nextAnchor] {
				nextAnchor++
			}
			chunk++
		}
		if chunk < 0 {
			chunk = 0
		}

		if c.HasAbsX {
			penX = c.AbsX
		}
		if c.HasAbsY {
			penY = c.AbsY
		}
		// dx/dy shift the pen permanently, not just this one glyph: that is
		// what makes a dx list shift each successive character cumulatively.
		penX += c.DX
		penY += c.DY

		// In a vertical writing mode the pen walks DOWN the page: the advance
		// is the font's vertical metric, and text-orientation decides whether
		// the glyph stands upright or lies on its side. verticalGlyph returns
		// both, plus the cross-axis shift that keeps a recumbent glyph centred
		// on the vertical baseline.
		//
		// The horizontal path is untouched — vertical is false for every
		// ordinary <text>, so advance stays g.advance and both offsets are 0.
		vertical := c.Style.Vertical()
		advance := g.advance
		var orientRad, crossShift float64
		if vertical {
			advance, orientRad, crossShift = r.verticalGlyph(c.Style, &g.glyph, g.advance)
		}

		placed = append(placed, placedGlyph{
			glyph: g.glyph,
			penX:  penX + crossShift,
			penY:  penY + r.baselineOffset(c.Style, &g.glyph),
			// A character's own rotate= composes WITH the writing mode's
			// orientation rather than replacing it. SVG defines rotate as an
			// additional rotation of the glyph about its origin, and in a
			// vertical run the glyph is already turned by text-orientation, so
			// adding them keeps rotate="90" meaning "a further quarter turn" in
			// either mode. orientRad is 0 horizontally, leaving this identical.
			rotateRad: c.RotateDeg*math.Pi/180 + orientRad,
			vertical:  vertical,
			chunk:     chunk,
			style:     c.Style,
			xScale:    g.xScale,
			// The glyph's EFFECTIVE advance, which every later pass (anchor
			// measurement, decoration extent, bidi re-lay) must read instead
			// of glyph.Advance so they see the spacing and textLength the pen
			// actually walked — and, in a vertical run, the VERTICAL advance
			// the pen walked rather than the horizontal one it did not.
			advance:        advance,
			charIndex:      g.charIndex,
			fillGradient:   asGradient(c.FillGradient()),
			strokeGradient: asGradient(c.StrokeGradient()),
			fillPattern:    asPattern(c.FillPattern()),
			strokePattern:  asPattern(c.StrokePattern()),
			clip:           c.ClipPath(),
			mask:           c.Mask(),
		})
		// The gap a textLength="spacing" request opened (or closed) sits
		// BETWEEN this glyph and the next, so it is added after the advance —
		// which is precisely why it never introduces a leading or trailing
		// space, and why the last glyph's index carries no gap.
		//
		// Which axis it advances is the whole of the vertical change: the pen
		// walk, the chunk bookkeeping, dx/dy and the absolute x/y overrides are
		// all axis-agnostic and are shared.
		if vertical {
			penY += advance + gaps[i]
		} else {
			penX += advance + gaps[i]
		}
	}

	reorderVisually(placed)
	applyAnchors(placed, t)
	return placed
}

// verticalGlyph resolves one glyph's placement in a vertical writing mode: how far the
// pen advances down the page, how far the glyph is rotated about its own origin, and
// how far the glyph shifts across the baseline.
//
// The three move together, which is why they are resolved in one place:
//
//   - An UPRIGHT glyph keeps its own baseline and advances by the font's VERTICAL
//     metric — one em for a face with no vhea, which is the browser fallback. It sits
//     centred on the vertical baseline, so it needs no cross shift.
//   - A SIDEWAYS glyph is turned a quarter turn, so what advances the pen is its
//     HORIZONTAL extent (it is lying on its side), and its ascent now extends across
//     the baseline rather than above it. At +QuarterTurn em-space up maps to page +X,
//     putting the ascent to the RIGHT, so the baseline shifts LEFT by half the
//     ascent/descent difference to leave the glyph centred.
//
// Mixing those up is the failure mode worth naming: advancing a sideways glyph by the
// vertical metric spaces it one em per letter, which renders as legible fixed-pitch
// text and reads as intentional rather than as a bug.
//
// hAdvance is the glyph's EFFECTIVE horizontal advance — letter/word-spacing and any
// textLength correction already folded in — so a sideways run honours those exactly as
// a horizontal run does. An upright run does not: its advance comes from the font, and
// SVG's spacing properties are defined along the inline axis, which is a gap noted in
// FEATURES.md rather than silently split.
func (r *Renderer) verticalGlyph(st svg.Style, g *inline.Glyph, hAdvance float64) (advance, rotateRad, crossShift float64) {
	rot, sideways := inline.GlyphRotation(st.TextOrientation(), g.Runes)
	if !sideways {
		return inline.VerticalAdvancePt(g), rot, 0
	}
	return hAdvance, rot, -(ascentFor(g, g.SizePt) - descentFor(g, g.SizePt)) / 2
}

// baselineOffset returns how far DOWN (SVG's +Y) a character's baseline moves
// from the text's own, resolving dominant-baseline / alignment-baseline
// against the face's real metrics and then subtracting the accumulated
// baseline-shift.
//
// The two baseline-SELECTION properties answer one question — "which of the
// font's baselines does this box sit on?" — and alignment-baseline wins when
// it is set, deferring to dominant-baseline only at its "auto" initial value
// (CSS Inline §5.2; resvg's dominant-baseline/different-alignment-baseline-on-
// tspan.svg is what pins the precedence).
//
// Every offset is expressed against the ALPHABETIC baseline, which is where
// the shaper already put the glyph, so "auto"/"alphabetic" is zero and costs
// nothing. The metrics come from the glyph's own face rather than from the
// style, since a per-rune script fallback can hand different characters of one
// run different faces and each must hang from its own font's metrics.
//
// baseline-shift is SUBTRACTED because it is defined positive-UP while SVG's
// Y axis points DOWN.
func (r *Renderer) baselineOffset(st svg.Style, g *inline.Glyph) float64 {
	kind := st.AlignmentBaseline()
	if kind == "auto" || kind == "baseline" {
		// "baseline" is alignment-baseline's spelling of "use my parent box's
		// dominant baseline", NOT of "alphabetic" — it DEFERS, exactly like
		// "auto". resvg's dominant-baseline/alignment-baseline=baseline-on-
		// tspan.svg is the discriminating case: a tspan carrying it inside a
		// <text dominant-baseline="middle"> renders FLUSH with its siblings,
		// which only happens if it inherits their middle baseline rather than
		// resetting itself to alphabetic.
		kind = st.DominantBaseline()
	}
	return baselineKeywordOffset(kind, g) - st.BaselineShiftPt()
}

// baselineKeywordOffset resolves one baseline keyword to a downward offset in
// points, using the glyph's face metrics.
//
// ascent and descent arrive from Face.Metrics() in EM units with ascent
// positive above the baseline and descent a positive magnitude below it;
// inline.Glyph carries them already scaled to points (AscentPt/DescentPt), so
// no unit conversion happens here.
//
//   - "hanging" and "text-before-edge" put the box's TOP edge on the reference
//     line, so the baseline moves DOWN by the ascent.
//   - "text-after-edge" puts the BOTTOM edge there, so the baseline moves UP by
//     the descent.
//   - "middle" centres the X-HEIGHT, so the baseline moves down by half of it.
//     pkg/font parses no OS/2 sxHeight, so the x-height is the conventional
//     0.5em substitute — which lands "middle" slightly ABOVE "central", the
//     relationship resvg's two reference renderings show.
//   - "central" centres the full ascent-to-descent box.
//
// Anything else — including the keywords that degraded to "auto" back in the
// cascade — is the alphabetic baseline, i.e. no offset.
func baselineKeywordOffset(kind string, g *inline.Glyph) float64 {
	ascent, descent := g.AscentPt, g.DescentPt
	if ascent <= 0 && descent <= 0 {
		// A face that exposes no extents at all: the 0.8/0.2 em fallback
		// pkg/font uses, so a baseline keyword still lands somewhere sane
		// instead of collapsing to alphabetic.
		ascent, descent = g.SizePt*0.8, g.SizePt*0.2
	}
	switch kind {
	case "hanging", "text-before-edge", "before-edge":
		return ascent
	case "text-after-edge", "after-edge":
		return -descent
	case "middle":
		return g.SizePt * xHeightEmFallback / 2
	case "central":
		return (ascent - descent) / 2
	default:
		return 0
	}
}

// xHeightEmFallback is the x-height, as a fraction of the em, that
// baselineKeywordOffset's "middle" uses. pkg/font parses no OS/2 table, so
// there is no real sxHeight to read; 0.5em is CSS's own stated fallback for
// the `ex` unit and is what applyFontSize/parseLength already assume.
const xHeightEmFallback = 0.5

// applyTextSpacing folds letter-spacing and word-spacing into each glyph's
// effective advance.
//
// SVG/CSS put these at DIFFERENT places, and the difference is visible:
//
//   - word-spacing is added AT each space character, so it widens the space
//     glyph itself. A run with no spaces is unaffected no matter how large the
//     value.
//   - letter-spacing widens the gap AFTER each glyph except the last one in
//     the whole <text>. Despite SVG 1.1's wording suggesting it applies after
//     every glyph including the final one, resvg follows CSS Text 3 and adds
//     NO trailing space: its own letter-spacing/filter-bbox.svg states this in
//     a <desc> and asserts it with a filter region whose flood rectangle ends
//     flush with the final glyph's right edge. resvg's reference PNGs are this
//     corpus's ground truth, so that is what is implemented.
//
// Both come from the SOURCE CHARACTER's style, not from a single run-wide
// value, so a <tspan letter-spacing="10"> inside a <text letter-spacing="3">
// widens only the gaps after its own characters (resvg's mixed-spacing.svg)
// and the character after it reverts to the enclosing 3.
func (r *Renderer) applyTextSpacing(glyphs []shapedChar, t *svg.Text) {
	for i := range glyphs {
		g := &glyphs[i]
		if g.charIndex < 0 || g.charIndex >= len(t.Chars) {
			continue // defensive; mapGlyphsToChars always produces a valid index
		}
		st := t.Chars[g.charIndex].Style

		if ws := st.WordSpacingPt(); ws != 0 && isWordSpace(g) {
			g.advance += ws
		}

		// The gap AFTER this glyph takes THIS character's value — never the
		// next character's — so a <tspan letter-spacing="10"> widens the gap
		// after each of its own characters and the one following it reverts to
		// the enclosing value. The very last glyph of the whole <text> is the
		// only one skipped, which is what keeps the trailing edge flush.
		if i+1 < len(glyphs) {
			g.advance += st.LetterSpacingPt()
		}
	}
}

// isWordSpace reports whether a shaped glyph is a word-separator that
// word-spacing applies to. CSS Text 3 defines the set as U+0020 and U+00A0;
// SVG's whitespace processing has already collapsed tabs and newlines into
// U+0020 by the time lowering finishes, so only those two can appear.
func isWordSpace(g *shapedChar) bool {
	for _, r := range g.glyph.Runes {
		if r == ' ' || r == ' ' {
			return true
		}
	}
	return false
}

// applyTextLengths resolves every textLength request against the glyphs it
// covers, returning the per-glyph inter-glyph GAP the pen walk must insert
// after each glyph (len(glyphs) entries; the last is always zero).
//
// The two lengthAdjust modes differ in what absorbs the difference:
//
//   - "spacing" (the default) leaves every glyph exactly as shaped and puts
//     the whole difference into the n-1 gaps between them. This is the mode
//     that must not touch the leading or trailing edge: the range still starts
//     at its own pen position and ends one natural advance past the last
//     glyph's origin, so the TOTAL width lands on the target exactly.
//   - "spacingAndGlyphs" scales advances AND outlines by target/natural, so
//     the glyphs themselves stretch or squeeze. No gaps are opened at all.
//
// Requests arrive outermost-first (see svg.textBuilder.trimLengths), so
// applying them in slice order lets an inner <tspan>'s request overwrite the
// outer one for the characters they share — SVG's nesting rule.
//
// Edge cases the corpus exercises, all handled without a special case leaking
// into the caller: a target SMALLER than natural (every gap goes negative and
// the glyphs overlap — resvg's zero.svg), a target of exactly zero, and a
// SINGLE-glyph range, where "spacing" has no gap to distribute into and is
// therefore a no-op rather than a division by zero.
func (r *Renderer) applyTextLengths(glyphs []shapedChar, t *svg.Text) []float64 {
	gaps := make([]float64, len(glyphs))
	if len(t.Lengths) == 0 || len(glyphs) == 0 {
		return gaps
	}
	for _, l := range t.Lengths {
		lo, hi := glyphRangeFor(glyphs, l.Start, l.End)
		if lo >= hi {
			continue
		}
		natural := 0.0
		for i := lo; i < hi; i++ {
			natural += glyphs[i].advance
		}
		if l.Glyphs {
			// spacingAndGlyphs: uniform horizontal scale. A zero natural width
			// (an all-whitespace or all-zero-advance range) has no scale that
			// reaches the target, so it degrades to leaving the range alone
			// rather than dividing by zero.
			if natural <= 0 {
				continue
			}
			scale := l.Target / natural
			for i := lo; i < hi; i++ {
				// BOTH compound. Requests arrive outermost-first, so an inner
				// <tspan textLength=... lengthAdjust="spacingAndGlyphs"> sees
				// advances the outer request already scaled, and its own scale
				// composes on top. Assigning xScale rather than multiplying it
				// would leave the outline carrying only the INNER factor while
				// the advance carried the product — the glyphs then render too
				// narrow (or too wide) inside their own advance boxes, leaving
				// gaps no gap value accounts for.
				glyphs[i].advance *= scale
				glyphs[i].xScale *= scale
			}
			// Any gap an OUTER spacing request opened inside this range is
			// superseded: the range's width is now exactly the target.
			for i := lo; i < hi; i++ {
				gaps[i] = 0
			}
			continue
		}
		// spacing: distribute the difference evenly into the interior gaps.
		n := hi - lo - 1
		if n <= 0 {
			// A single glyph has no interior gap. SVG leaves it at its natural
			// width rather than scaling it (that is what lengthAdjust=
			// "spacingAndGlyphs" is for), so there is nothing to do.
			//
			// Defence-in-depth, not the only guard: the loop below zeroes the
			// gap at i == hi-1, which for a one-glyph range is the ONLY index,
			// so a non-finite `per` could never be stored even without this.
			// Kept because it states the intent at the point the range size is
			// known, rather than leaving it implicit in a branch further down.
			continue
		}
		per := (l.Target - natural) / float64(n)
		for i := lo; i < hi; i++ {
			if i == hi-1 {
				gaps[i] = 0 // never past the last glyph: no trailing space
				continue
			}
			gaps[i] = per
		}
	}
	return gaps
}

// glyphRangeFor maps a CHARACTER range [start,end) onto the contiguous glyph
// indices whose source characters fall inside it.
//
// It scans rather than indexes because shaping is not one-to-one: a ligature
// covers several characters with one glyph and a decomposition does the
// reverse, so there is no arithmetic relation between the two index spaces.
// The result is a half-open [lo,hi) glyph range, empty when the character
// range produced no glyphs (an all-whitespace tspan, or one whose font-size
// resolved to zero).
func glyphRangeFor(glyphs []shapedChar, start, end int) (lo, hi int) {
	lo, hi = -1, -1
	for i := range glyphs {
		c := glyphs[i].charIndex
		if c < start || c >= end {
			continue
		}
		if lo < 0 {
			lo = i
		}
		hi = i + 1
	}
	if lo < 0 {
		return 0, 0
	}
	return lo, hi
}

// reorderVisually applies UAX#9 bidi reordering to the already-PLACED glyphs,
// per text chunk, by permuting which glyph occupies each x slot.
//
// The ordering has to happen here rather than before the pen walk. SVG's
// x/y/dx/dy lists address characters in LOGICAL order (SVG2 §11.5), so the
// walk must see glyphs in logical order or an absolute x meant for the first
// logical character lands on whatever reordering put first — which for the
// corpus's direction/rtl.svg threw the anchor 170 units away from the rest of
// the text.
//
// So the walk assigns each glyph its advance-accumulated slot in logical
// order, and this then REASSIGNS those slots: the x positions themselves stay
// exactly where the walk put them (preserving every dx, dy, and absolute
// reset), and only WHICH glyph sits at each is permuted. Because the slots
// were laid left to right and inline.Reorder returns visual order, walking
// the reordered glyphs and re-dealing the slots left to right is precisely
// UAX#9 rule L2 applied to a line.
//
// inline.Reorder is standalone on a flat glyph slice — it needs no line box —
// which is what lets SVG reuse the engine's real UAX#9 implementation (rule
// L2 plus L4 bracket mirroring) rather than approximating it. An SVG <text>
// chunk is one line for bidi purposes, since it never wraps.
func reorderVisually(placed []placedGlyph) {
	for i := 0; i < len(placed); {
		chunk := placed[i].chunk
		j := i
		for j < len(placed) && placed[j].chunk == chunk {
			j++
		}
		reorderChunk(placed[i:j])
		i = j
	}
}

// reorderChunk reorders one chunk in place. It is a no-op unless reordering
// actually changes the sequence, so a Latin-only document pays nothing beyond
// inline.Reorder's own fast path.
func reorderChunk(run []placedGlyph) {
	if len(run) < 2 {
		return
	}
	dir := inline.DirLTR
	if run[0].style.DirectionRTL() {
		dir = inline.DirRTL
	}
	// Reorder a slice of TAGGED copies rather than the glyphs themselves, so
	// the permutation can be read back exactly.
	//
	// Matching reordered glyphs back to their sources by VALUE does not work:
	// a shaped run is full of zero-advance, nil-outline mark glyphs that are
	// indistinguishable from one another, so any value-based match assigns
	// several visual slots to the same logical glyph and scrambles the rest.
	// Encoding the index in a field inline.Reorder preserves but does not read
	// for ordering — Atomic, which it treats as an opaque payload — makes the
	// permutation exact and total. Runes must stay untouched: they are what
	// the algorithm reads to classify each glyph's bidi character type.
	tagged := make([]inline.Glyph, 0, len(run)+2)
	idx := make([]inline.AtomicItem, len(run))

	// unicode-bidi: bidi-override forces EVERY character into the base
	// direction rather than letting UAX#9 choose per character, so Latin
	// inside an rtl override reads backwards too. That is precisely what the
	// Unicode override controls mean, so the run is bracketed in an
	// LRO/RLO ... PDF pair here — synthetic, zero-width, and carrying no tag,
	// so they order the real glyphs without becoming any.
	//
	// They are added at REORDER time rather than folded into the shaper's
	// input, because the pen walk between shaping and here drops control
	// glyphs (a control must not consume a position-list entry), which would
	// have discarded them before they could do their job.
	override := run[0].style.BidiOverride()
	if override {
		open := '‭' // LRO
		if dir == inline.DirRTL {
			open = '‮' // RLO
		}
		tagged = append(tagged, inline.Glyph{Runes: []rune{open}})
	}
	for i := range run {
		g := run[i].glyph
		idx[i] = inline.AtomicItem{Ref: i}
		g.Atomic = &idx[i]
		tagged = append(tagged, g)
	}
	if override {
		tagged = append(tagged, inline.Glyph{Runes: []rune{'‬'}}) // PDF
	}

	visual := inline.Reorder(tagged, dir)
	if len(visual) != len(tagged) {
		return // defensive: a reorder that changed the count is not usable
	}
	order := make([]int, 0, len(run))
	for _, vg := range visual {
		if vg.Atomic == nil {
			continue // a synthetic bracket control: it maps to no real glyph
		}
		l, ok := vg.Atomic.Ref.(int)
		if !ok || l < 0 || l >= len(run) {
			return
		}
		order = append(order, l)
	}
	if len(order) != len(run) {
		return // not a total permutation: leave logical order rather than guess
	}

	if isIdentity(order) {
		return // nothing moved: leave the logical placement untouched
	}

	// Permute the whole glyph record, not just the outline: a glyph's paint,
	// clip/mask, and rotation all belong to the CHARACTER, and follow it into
	// its visual position.
	reordered := make([]placedGlyph, len(run))
	for s, l := range order {
		reordered[s] = run[l] // the untagged original; the tag lived only on the copy
	}

	// Re-lay the x positions along the visual sequence. The slots the logical
	// walk produced cannot simply be reused: they were accumulated from the
	// LOGICAL advances, so once the glyphs are permuted a glyph would sit in a
	// slot sized for a different glyph, overlapping its neighbour wherever the
	// two advances differ.
	//
	// Instead the chunk keeps its logical START — the pen position of its
	// first character, which is what carries the element's x/y and any leading
	// dx — and each visual glyph is laid from there by its own advance. Each
	// glyph also carries the LEAD GAP the logical walk left in front of it
	// (its own dx, i.e. whatever separated it from its logical predecessor
	// beyond that predecessor's advance), so a dx on an RTL character still
	// offsets it in the visual layout.
	//
	// Per-character absolute x/y resets inside an RTL chunk are the one case
	// this does not reproduce exactly: an absolute reset starts a NEW chunk
	// (see svg.Text.Anchors), so within a single chunk there is at most the
	// leading one, which is exactly the start captured here.
	// The slots run along the chunk's own INLINE axis — X horizontally, Y in a
	// vertical writing mode — so both the gap measurement and the redeal follow
	// it. Everything else about the reorder is axis-independent: it permutes
	// which glyph occupies each slot, not where the slots are.
	vertical := run[0].vertical
	penOf := func(g *placedGlyph) float64 {
		if vertical {
			return g.penY
		}
		return g.penX
	}
	leads := make([]float64, len(run))
	for l := 1; l < len(run); l++ {
		if gap := penOf(&run[l]) - (penOf(&run[l-1]) + run[l-1].advance); gap > 0 {
			leads[l] = gap
		}
	}
	pen := penOf(&run[0])
	for s := range reordered {
		pen += leads[order[s]]
		if vertical {
			reordered[s].penY = pen
		} else {
			reordered[s].penX = pen
		}
		pen += reordered[s].advance
	}
	copy(run, reordered)
}

// isIdentity reports whether order is 0,1,2,... i.e. reordering changed
// nothing.
func isIdentity(order []int) bool {
	for i, v := range order {
		if i != v {
			return false
		}
	}
	return true
}

// asGradient narrows svg.GradientPaint to this package's own `gradient`
// accessor interface (the method sets are identical), preserving nil-ness so
// the `!= nil` checks in paintGlyphFill/paintGlyphStroke stay meaningful.
func asGradient(g svg.GradientPaint) gradient {
	if g == nil {
		return nil
	}
	return g
}

// asPattern narrows svg.PatternPaint to this package's own `pattern`
// accessor interface. See asGradient.
func asPattern(p svg.PatternPaint) pattern {
	if p == nil {
		return nil
	}
	return p
}

// applyAnchors shifts each text chunk by its text-anchor offset, measured
// from the chunk's own first glyph to the end of its last so an intervening
// dx or absolute reset inside the chunk is included.
//
// The offsets are DIRECTION-RELATIVE, which is the part that is easy to miss:
// text-anchor's "start" and "end" name the start and end of the INLINE BASE
// DIRECTION, not the left and right of the canvas (SVG2 §11.5, CSS Writing
// Modes). In an ltr chunk "start" is the left edge and shifts nothing; in an
// rtl chunk "start" is the RIGHT edge, so the chunk must extend leftward from
// its anchor point — shifted by its whole advance, exactly what "end" does in
// ltr. The corpus's direction/rtl.svg is anchored at x=170 with the default
// text-anchor and expects the Arabic to run leftward from there; treating
// "start" as "no shift" ran it off the right edge of the viewport.
//
// text-anchor itself is read from the chunk's FIRST character's style, per
// SVG2 §11.5: the property applies to a text chunk as a whole, so a <tspan>
// that changes it mid-chunk has no effect on that chunk (the corpus's
// text-anchor-not-on-text-chunk.svg asserts exactly this).
func applyAnchors(placed []placedGlyph, t *svg.Text) {
	if len(placed) == 0 {
		return
	}
	i := 0
	for i < len(placed) {
		chunk := placed[i].chunk
		j := i
		// text-anchor aligns a chunk along its own INLINE axis, which is X for a
		// horizontal run and Y for a vertical one. Both the measurement and the
		// shift follow the axis; everything else about the rule is identical, so
		// the axis is selected once here rather than duplicating the function.
		vertical := placed[i].vertical
		pen := func(g *placedGlyph) float64 {
			if vertical {
				return g.penY
			}
			return g.penX
		}
		lo, hi := math.Inf(1), math.Inf(-1)
		for j < len(placed) && placed[j].chunk == chunk {
			lo = math.Min(lo, pen(&placed[j]))
			hi = math.Max(hi, pen(&placed[j])+placed[j].advance)
			j++
		}
		extent := hi - lo
		anchor := placed[i].style.TextAnchor()
		if placed[i].style.DirectionRTL() && !vertical {
			// Map the direction-relative keywords onto physical edges for an
			// rtl chunk: start is the right edge, end the left. "middle" is
			// symmetric and unaffected.
			//
			// Skipped for a vertical run: `direction` reverses the INLINE axis,
			// and in a vertical writing mode that axis runs top-to-bottom for
			// both ltr and rtl — an rtl vertical run reverses the order lines
			// STACK in, which is a multi-line concern this engine does not reach
			// (one <text> is one vertical run). Applying the horizontal flip here
			// would move a vertical chunk to the wrong end of its own axis.
			switch anchor {
			case "start":
				anchor = "end"
			case "end":
				anchor = "start"
			}
		}
		var shift float64
		switch anchor {
		case "middle":
			shift = -extent / 2
		case "end":
			shift = -extent
		}
		if shift != 0 {
			for k := i; k < j; k++ {
				if vertical {
					placed[k].penY += shift
				} else {
					placed[k].penX += shift
				}
			}
		}
		i = j
	}
}

// shapedChar pairs one shaped glyph with the index of the source character it
// came from, so the per-character position adjustments survive shaping.
//
// The mapping is not one-to-one in either direction: a ligature or an Arabic
// contextual form can turn several characters into one glyph, and a
// decomposition can turn one into several. The index recorded is the FIRST
// source character a glyph covers, which is what SVG's positioning model
// wants — an x/dx entry addresses a character, and when several characters
// fuse into one glyph the fused glyph is placed by the first of them.
type shapedChar struct {
	glyph     inline.Glyph
	charIndex int

	// advance starts as glyph.Advance and is then adjusted in place, first by
	// letter-spacing/word-spacing and then by any textLength correction. The
	// pen walk reads THIS, never glyph.Advance, so the two adjustment passes
	// compose without either needing to know about the other.
	advance float64

	// xScale is the horizontal outline scale a lengthAdjust="spacingAndGlyphs"
	// request imposed; 1 otherwise.
	xScale float64
}

// shapeChars shapes the character list, one inline.Run per maximal span of
// characters sharing the same font style, and maps each resulting glyph back
// to a source character index.
//
// Each style span is shaped SEPARATELY, which is a real fidelity limit worth
// naming: a ligature or a cursive Arabic join spanning a <tspan> boundary
// will not form, because the two sides reach the shaper as different runs.
// That matches how the boundary must behave anyway whenever the two sides
// differ in family or size, and merging spans that happen to share a style
// would make the behavior depend on whether an author's <tspan> changed
// anything visible — inconsistent in a way that is harder to reason about
// than the uniform rule.
func (r *Renderer) shapeChars(chars []svg.TextChar) []shapedChar {
	faces := r.faces()
	var out []shapedChar
	for i := 0; i < len(chars); {
		j := i
		for j < len(chars) && sameShapingStyle(chars[i].Style, chars[j].Style) {
			j++
		}
		span := chars[i:j]
		st := span[0].Style
		if st.FontSizePt() <= 0 {
			// A zero (or, defensively, negative) font-size paints nothing.
			// The characters still consumed their position-list entries
			// above, which is correct: SVG's zero-size fixtures expect the
			// text to vanish, not the following text to shift.
			i = j
			continue
		}
		runes := make([]rune, len(span))
		for k := range span {
			runes[k] = span[k].R
		}
		// unicode-bidi: bidi-override is NOT applied here by wrapping the
		// text in the Unicode LRO/RLO controls. That works at the shaper
		// level, but reordering now happens after the pen walk (see
		// reorderVisually), and the walk drops the zero-width control glyphs
		// because they must not consume a position-list entry — so the
		// controls would be gone by the time the reorder ran. The override is
		// applied at reorder time instead, in reorderChunk.
		run := inline.Run{
			Text:   string(runes),
			Family: familyWithFallback(st.FontFamily()),
			Bold:   st.FontBold(),
			Italic: st.FontItalic(),
			SizePt: st.FontSizePt(),
			// white-space is already fully resolved at build time (see
			// pkg/svg's appendText), so the shaper must not collapse again;
			// "pre" preserves what arrives. No '\n' or '\t' survives
			// lowering, so the preserving mode's newline/tab handling never
			// triggers.
			WhiteSpace: "pre",
		}
		glyphs := inline.Shape(faces, []inline.Run{run}, r.Logf)
		// Shaping ONLY here — deliberately NOT inline.Reorder. Bidi reordering
		// has to happen AFTER the pen walk, not before it: SVG's x/y/dx/dy
		// lists address characters in LOGICAL order, so a reorder applied here
		// would hand the pen walk glyphs whose source-character indices jump
		// around, and an absolute x meant for the first logical character
		// would land on whichever glyph reordering happened to put first.
		// See reorderVisually, which the caller runs on the PLACED glyphs.
		out = append(out, mapGlyphsToChars(glyphs, span, i)...)
		i = j
	}
	return out
}

// familyWithFallback appends a terminal generic family to a font-family list
// so resolution can never fail outright.
//
// This matters because inline.Shape SKIPS a run whose family does not
// resolve, which for a reflow document is the right call (the box had a
// fallback chain and none of it matched) but for SVG would silently delete
// the text. Naming a font the engine has no bundled look-alike for —
// `font-family="Noto Sans"`, which most of the resvg text corpus does — is
// ordinary, and every SVG renderer substitutes rather than rendering nothing.
// The generic keyword always resolves (see pkg/font/standard), so appending
// one makes the chain terminal.
//
// It is appended, never substituted: an author's own list is still tried
// first, name by name, exactly as written.
func familyWithFallback(family string) string {
	if family == "" {
		return genericFallbackFamily
	}
	// Don't append a fallback the list already ends with. This string is not
	// only a lookup key — the shaper quotes it verbatim in its missing-glyph
	// diagnostic, where "sans-serif, sans-serif" reads as though two distinct
	// families were tried and both failed. SVG's own initial font-family IS
	// sans-serif, so an element that states none hit this on every unmappable
	// rune, in exactly the message meant to explain the tofu.
	if family == genericFallbackFamily || strings.HasSuffix(family, ", "+genericFallbackFamily) {
		return family
	}
	return family + ", " + genericFallbackFamily
}

// genericFallbackFamily is the generic keyword appended to every SVG family
// list as its last resort. It always resolves (see pkg/font/standard), which is
// what makes the chain terminal.
const genericFallbackFamily = "sans-serif"

// mapGlyphsToChars pairs each shaped glyph with the index (in the whole
// character list) of the source character it came from, dropping the
// zero-width bidi control glyphs a bidi-override wrapper introduced.
//
// Matching is by RUNE IDENTITY against the span, scanning forward from the
// last match: Glyph.Runes carries the source runes a glyph covers, so the
// first rune of each glyph locates its character. A glyph whose runes cannot
// be located (a synthesized outline with no Runes, or reordering that moved a
// glyph before its predecessor's match point) falls back to the span's
// running position, which keeps every glyph placed rather than dropping it.
func mapGlyphsToChars(glyphs []inline.Glyph, span []svg.TextChar, spanStart int) []shapedChar {
	out := make([]shapedChar, 0, len(glyphs))
	next := 0
	for _, g := range glyphs {
		if g.Break {
			continue
		}
		if len(g.Runes) == 1 && isBidiControl(g.Runes[0]) {
			// A bidi control draws nothing and has no advance, so it must not
			// occupy a position-list entry. It is dropped here rather than
			// carried through placement; the reorder that would have read it
			// runs later on the placed glyphs, and gets the base direction
			// and the override flag from the style instead.
			continue
		}
		idx := -1
		if len(g.Runes) > 0 {
			for k := next; k < len(span); k++ {
				if span[k].R == g.Runes[0] {
					idx = k
					break
				}
			}
			if idx < 0 {
				// Reordering can place a glyph whose character sits BEFORE
				// the running position; search the whole span before giving
				// up, so an RTL line still maps every glyph.
				for k := 0; k < len(span); k++ {
					if span[k].R == g.Runes[0] {
						idx = k
						break
					}
				}
			}
		}
		if idx < 0 {
			idx = next
			if idx >= len(span) {
				idx = len(span) - 1
			}
		}
		next = idx + 1
		out = append(out, shapedChar{
			glyph:     g,
			charIndex: spanStart + idx,
			advance:   g.Advance,
			xScale:    1,
		})
	}
	return out
}

// isBidiControl reports whether r is one of the Unicode explicit
// bidirectional formatting controls. Only the two overrides and the
// terminating pop are produced by this package, but the whole set is
// recognized so a control an AUTHOR wrote into the text is never mistaken for
// content with an advance.
func isBidiControl(r rune) bool {
	switch r {
	case '‎', '‏', // LRM, RLM
		'‪', '‫', '‬', '‭', '‮', // LRE, RLE, PDF, LRO, RLO
		'⁦', '⁧', '⁨', '⁩': // LRI, RLI, FSI, PDI
		return true
	}
	return false
}

// sameShapingStyle reports whether two characters' styles shape identically,
// i.e. whether they can share one inline.Run. Only the properties the shaper
// reads matter — family, size, weight, slant, and the bidi properties that
// change how the run is reordered. Fill/stroke deliberately do NOT: a colour
// change mid-word must not break a ligature, and paint is applied per glyph
// afterward from each character's own style.
func sameShapingStyle(a, b svg.Style) bool {
	return a.FontFamily() == b.FontFamily() &&
		a.FontSizePt() == b.FontSizePt() &&
		a.FontBold() == b.FontBold() &&
		a.FontItalic() == b.FontItalic() &&
		a.DirectionRTL() == b.DirectionRTL() &&
		a.BidiOverride() == b.BidiOverride()
}

// paintGlyph paints one placed glyph as ordinary vector geometry: it builds
// the em-space-to-device transform, maps the outline through it, and routes
// the result to the SAME paintFill/paintStroke helpers a Shape uses — which
// is what gives text gradients, patterns, and the whole paint pipeline for
// free.
//
// See placedGlyph.matrix for the transform itself.
func (r *Renderer) paintGlyph(dev render.Device, p *placedGlyph, tm render.Matrix, alpha float64, warned *warnFlags) {
	if p.glyph.Outline == nil {
		return // whitespace, or a rune the face has no ink for: advance only
	}
	if p.glyph.SizePt <= 0 {
		return
	}

	dp := render.TransformPath(p.glyph.Outline, p.matrix(tm))
	if dp == nil {
		return
	}

	opacity := clamp01(p.style.Opacity())
	a := alpha * opacity

	// A glyph is a shape for painting purposes, so the fill/stroke split goes
	// through the identical helpers — including the gradient/pattern routing
	// and the stroke's user-space-to-device width scaling. tm — NOT the
	// glyph's own matrix — is what a paint server's local space composes
	// under: a gradient on text is defined in the TEXT's user space, not in
	// each glyph's scaled, rotated em space, so passing the glyph matrix here
	// would give every glyph its own private copy of the gradient.
	warned.drawCalls++
	r.paintGlyphFill(dev, p, dp, tm, a, warned)
	r.paintGlyphStroke(dev, p, dp, tm, a, warned)
}

// paintGlyphFill fills one glyph outline, mirroring paintFill's branch order
// exactly (gradient, then pattern, then solid) so text and shapes can never
// diverge in which paint wins.
func (r *Renderer) paintGlyphFill(dev render.Device, p *placedGlyph, dp *render.Path, tm render.Matrix, alpha float64, warned *warnFlags) {
	if p.fillGradient != nil {
		r.fillGradient(dev, dp, p.style, p.fillGradient, tm, alpha)
		return
	}
	if p.fillPattern != nil {
		r.fillPattern(dev, dp, p.style, p.fillPattern, tm, alpha, warned)
		return
	}
	fp, ok := p.style.FillPaint()
	if !ok {
		return
	}
	fp.Color.A = scaleAlpha(fp.Color.A, alpha)
	if fp.Rule != render.NonZero {
		// render.FillColor carries no fill rule and FillGlyph is defined to
		// use nonzero winding, so a fill-rule:evenodd on text has to go
		// through the general Fill path to be honored at all. The corpus's
		// text/fill-rule=evenodd.svg is what makes this branch load-bearing:
		// the counters of glyphs like "o" invert under even-odd, which is a
		// visible difference, not a rounding one.
		dev.Fill(dp, fp)
		return
	}
	// FillGlyph, not Fill: the outline IS a glyph, and a backend that can
	// treat it specially (hinting, a glyph cache) should get the chance to.
	// Both current backends rasterize it identically to Fill.
	dev.FillGlyph(dp, render.FillColor{R: fp.Color.R, G: fp.Color.G, B: fp.Color.B, A: fp.Color.A}, "")
}

// paintGlyphStroke strokes one glyph outline. It reuses the plain
// Device.Stroke path with the same user-space-to-device width scaling
// paintStroke applies, so stroke-width on text means the same thing it means
// on a <path>.
func (r *Renderer) paintGlyphStroke(dev render.Device, p *placedGlyph, dp *render.Path, tm render.Matrix, alpha float64, warned *warnFlags) {
	if p.strokeGradient != nil || p.strokePattern != nil {
		// Same documented degradation as a gradient stroke on a shape: no
		// stroke-to-outline conversion exists to clip a shading against, so
		// this falls back to StrokePaint's fallback colour.
		r.logStrokeGradientOnce(warned)
	}
	sp, ok := p.style.StrokePaint()
	if !ok {
		return
	}
	sf := tm.ScaleFactor()
	if sf == 0 {
		return
	}
	sp.Color.A = scaleAlpha(sp.Color.A, alpha)
	sp.Width *= sf
	sp.DashPhase *= sf
	if sp.DashArray != nil {
		scaled := make([]float64, len(sp.DashArray))
		for i, d := range sp.DashArray {
			scaled[i] = d * sf
		}
		sp.DashArray = scaled
	}
	dev.Stroke(dp, sp)
}

// textClipPath shapes t and returns every glyph outline unioned into a single
// device-space path under m, for a <text> used as clip or mask geometry.
//
// This is the whole payoff of painting text as outlines rather than through
// DrawGlyph: a glyph is already a *render.Path, so text becomes clip geometry
// with no new machinery — the same transform paintGlyph builds, applied to
// the same outline, appended into one path instead of filled.
//
// Returns nil when t shapes to nothing (no characters, an unresolvable
// zero font-size, or no glyph carrying ink), which the caller treats as "this
// child contributes nothing to the union" — NOT as "clip to everything".
func (r *Renderer) textClipPath(t *svg.Text, m render.Matrix) *render.Path {
	if t == nil {
		return nil
	}
	placed := r.layoutText(t)
	if len(placed) == 0 {
		return nil
	}
	// t.M is the <text> element's own transform, which composes under the
	// caller's matrix exactly as it does in paintText.
	tm := t.M.Mul(m)
	out := &render.Path{}
	for i := range placed {
		p := &placed[i]
		if p.glyph.Outline == nil || p.glyph.SizePt <= 0 {
			continue
		}
		dp := render.TransformPath(p.glyph.Outline, p.matrix(tm))
		if dp == nil {
			continue
		}
		out.Segments = append(out.Segments, dp.Segments...)
	}
	if out.Empty() {
		return nil
	}
	return out
}

// TextAdvances shapes text exactly as paintText would and returns each
// glyph's advance in points. It exists so a test can assert that an SVG
// <text> and an equivalent CSS/HTML inline run produce the SAME advances —
// i.e. that this package really does reuse inline.Shape rather than having
// forked a second shaper. It is not used by rendering.
func (r *Renderer) TextAdvances(chars []svg.TextChar) []float64 {
	shaped := r.shapeChars(chars)
	out := make([]float64, 0, len(shaped))
	for _, s := range shaped {
		out = append(out, s.glyph.Advance)
	}
	return out
}

// charsWithoutElementOpacity returns a copy of chars with any character
// carrying exactly the <text> element's own opacity reset to fully opaque,
// so a filter can paint its source at full strength and attenuate the
// RESULT instead (SVG applies a filter before opacity).
//
// Only characters whose opacity EQUALS elem are reset. Opacity is
// non-inherited and a <tspan opacity> REPLACES rather than multiplies the
// element's value (see svg.Text.Opacity), so a character showing something
// other than elem got it from its own tspan — that attenuation is not the
// element's to hoist, and must keep applying inside the filter's source
// exactly as it would unfiltered.
//
// The result is always a fresh slice: the scene is read-only after Parse and
// shared lock-free across concurrent renders, so the characters themselves
// must never be mutated.
func charsWithoutElementOpacity(chars []svg.TextChar, elem float64) []svg.TextChar {
	if elem >= 1 {
		return chars // nothing to hoist; avoid the copy entirely
	}
	out := make([]svg.TextChar, len(chars))
	copy(out, chars)
	for i := range out {
		if clamp01(out[i].Style.Opacity()) == elem {
			out[i].Style = out[i].Style.SetOpacity(1)
		}
	}
	return out
}
