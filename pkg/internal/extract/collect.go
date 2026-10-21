// Package extract reconstructs document structure (paragraphs, headings, lists,
// tables) from a PDF's positioned glyphs and vector graphics, then builds a
// *cssbox.Box tree the existing conversion writers turn into Markdown/HTML.
//
// A PDF is a page-description format: it says "paint this glyph at (x,y)" and
// "stroke this line", with no notion of a paragraph, a heading, or a table cell.
// This package recovers that lost logical structure from the geometry alone. The
// pipeline is:
//
//	Collect  — run the content interpreter with the capture sinks, accumulating
//	           per-page positioned glyphs and candidate ruling lines (collect.go).
//	words    — group glyphs into words and lines with the pdfplumber tolerance
//	           heuristics (words.go).
//	tables   — detect tables by both a lattice (ruling-line) and a stream
//	           (whitespace-column) strategy, auto-selected per region (tables.go).
//	blocks   — order the remaining lines by an XY-cut recursive split and classify
//	           each run of lines as a heading, list item, or paragraph (blocks.go).
//	Lower    — assemble the classified structure into a cssbox tree (lower.go).
//
// The extractor never trusts the input: a malformed page must not crash the whole
// document, so Collect recovers at the page boundary and returns whatever was
// captured before the fault (mirroring raster.RenderPage's page-boundary recover).
package extract

import (
	"errors"
	"image"
	"math"
	"unicode"

	"github.com/nathanstitt/omnidoc/pkg/internal/content"
	"github.com/nathanstitt/omnidoc/pkg/internal/pageres"
	"github.com/nathanstitt/omnidoc/pkg/internal/raster"
	"github.com/nathanstitt/omnidoc/pkg/internal/render"
	"github.com/nathanstitt/omnidoc/pkg/pdf"
)

// errNilDocument is returned by Collect/Lower for a nil *pdf.Document.
var errNilDocument = errors.New("extract: nil document")

// glyph is one captured glyph in device space. It is the extractor's internal
// echo of content.TextGlyph with the trailing x-edge (x1 = x + advance)
// precomputed, since word grouping keys off the gap between one glyph's x1 and
// the next glyph's x.
type glyph struct {
	r       rune    // source rune (0 when the font gives no mapping — handled, never crashes)
	x, y    float64 // glyph origin (baseline pen position), device space
	x1      float64 // x + advance (the glyph's right edge on the baseline)
	size    float64 // effective font size in device units
	advance float64 // horizontal advance in device units
	isSpace bool    // the single-byte space (code 32)
	fontID  string  // opaque per-font identity, for run grouping
	bold    bool    // best-effort weight
	italic  bool    // best-effort slant
}

// rule is a candidate table-ruling line recovered from a vector op: either a
// horizontal segment (dir == horizontal) or a vertical one. A horizontal rule
// spans [x0,x1] at a fixed y; a vertical rule spans [y0,y1] at a fixed x. The
// fields are named generically (a,b0,b1) and interpreted by dir so both share the
// snapping/joining code in tables.go.
type rule struct {
	horizontal bool    // true = horizontal segment, false = vertical
	a          float64 // the fixed coordinate (y for horizontal, x for vertical)
	b0, b1     float64 // the span along the free axis (x0..x1 or y0..y1), b0 <= b1
}

// pageContent is everything Collect recovers from one page: the positioned
// glyphs, the candidate ruling lines, and the page's device-space size (used to
// bound XY-cut splits and to normalize coordinates).
type pageContent struct {
	glyphs []glyph
	rules  []rule
	images []imageRef
	width  float64 // page width in device units
	height float64 // page height in device units
}

// Tunable geometry thresholds for rule extraction. They are expressed in device
// units (the collector runs the interpreter at the identity page scale, so device
// units are PDF points). A rule is only a rule if it is thin: a thick stroke or a
// tall/wide filled box is real content, not a separator.
const (
	// maxRuleThickness caps a stroke width (or a filled bar's minor dimension) for
	// it to count as a ruling line, in points. Table rules are hairlines to a
	// couple of points; anything thicker is treated as content.
	maxRuleThickness = 3.0
	// straightTol is how far (points) a segment's minor-axis extent may wander and
	// still count as axis-aligned (near-horizontal / near-vertical).
	straightTol = 0.75
	// minRuleLength is the shortest segment (points) worth recording as a rule, to
	// drop dots, tick marks, and glyph-sized vector noise.
	minRuleLength = 3.0
)

// collector implements the two content sinks, accumulating glyphs and rules for
// one page. It is deliberately allocation-light (append into slices) because the
// interpreter calls the sinks once per glyph and once per painted path.
type collector struct {
	glyphs []glyph
	rules  []rule
	images []imageRef
}

// text is the TextSink: it records one shown glyph. A glyph with rune 0 (a font
// with no ToUnicode mapping) is kept — words.go substitutes U+FFFD so positions
// are preserved — rather than dropped, so table/column geometry stays intact even
// when the text itself is unmappable.
func (c *collector) text(g content.TextGlyph) {
	c.glyphs = append(c.glyphs, glyph{
		r:       g.Rune,
		x:       g.X,
		y:       g.Y,
		x1:      g.X + g.Advance,
		size:    g.SizePt,
		advance: g.Advance,
		isSpace: isWhitespaceGlyph(g),
		fontID:  g.FontID,
		bold:    g.Bold,
		italic:  g.Italic,
	})
}

// isWhitespaceGlyph reports whether a shown glyph is genuinely whitespace (and so
// should act as a hard word break that emits no character). The interpreter sets
// TextGlyph.IsSpace for byte code 32 to drive PDF word-spacing (Tw), but a subset
// font routinely remaps code 32 to a real glyph (e.g. "R"): such a glyph carries a
// non-space Unicode rune and is real content, not a break. We therefore treat a
// glyph as whitespace only when the interpreter flagged it AND its decoded rune is
// absent (unmapped, rune 0) or an actual Unicode space — never when it maps to a
// visible character.
func isWhitespaceGlyph(g content.TextGlyph) bool {
	if !g.IsSpace {
		return false
	}
	return g.Rune == 0 || unicode.IsSpace(g.Rune)
}

// vector is the GraphicsSink: it inspects one painted path and, when it looks like
// a ruling line (a thin axis-aligned stroke, or a thin filled rectangular bar),
// records the corresponding rule(s). Everything else (text fills, thick strokes,
// real rectangles) is ignored.
func (c *collector) vector(op content.VectorOp) {
	if op.Path == nil {
		return
	}
	switch op.Kind {
	case content.VectorStroke:
		if op.StrokeWidth > maxRuleThickness {
			return // a thick stroke is content, not a separator
		}
		c.rulesFromStrokedSegments(op.Path)
	case content.VectorFill:
		c.rulesFromFilledBars(op.Path)
	}
}

// rulesFromStrokedSegments walks a stroked path's subpaths and records each
// near-horizontal or near-vertical straight segment as a rule. A stroked table
// grid is drawn as many such segments (often one move+line per edge), so this
// catches the common "line-drawn" table.
func (c *collector) rulesFromStrokedSegments(p *render.Path) {
	var have bool
	var px, py float64 // previous point
	for _, s := range p.Segments {
		switch s.Kind {
		case render.MoveTo:
			px, py, have = s.P0.X, s.P0.Y, true
		case render.LineTo:
			if have {
				c.recordSegment(px, py, s.P0.X, s.P0.Y)
			}
			px, py, have = s.P0.X, s.P0.Y, true
		case render.CubeTo:
			// A curve is not a ruling line; just advance the pen.
			px, py, have = s.P2.X, s.P2.Y, true
		case render.Close:
			// A stroked close re-draws the segment back to the subpath start; we do
			// not track the subpath origin here (rare for rules), so just stop the run.
			have = false
		}
	}
}

// recordSegment records a single straight segment as a rule when it is long enough
// and axis-aligned. A segment whose minor-axis extent is within straightTol counts
// as horizontal (or vertical); a diagonal is ignored.
func (c *collector) recordSegment(x0, y0, x1, y1 float64) {
	dx := math.Abs(x1 - x0)
	dy := math.Abs(y1 - y0)
	switch {
	case dy <= straightTol && dx >= minRuleLength: // horizontal
		lo, hi := minmax(x0, x1)
		c.rules = append(c.rules, rule{horizontal: true, a: (y0 + y1) / 2, b0: lo, b1: hi})
	case dx <= straightTol && dy >= minRuleLength: // vertical
		lo, hi := minmax(y0, y1)
		c.rules = append(c.rules, rule{horizontal: false, a: (x0 + x1) / 2, b0: lo, b1: hi})
	}
}

// rulesFromFilledBars recovers rules drawn as thin filled rectangles. Many
// producers draw table borders as filled bars (a 0.5pt-tall filled rect) rather
// than strokes. For each closed subpath we take its bounding box; a box whose
// height is thin becomes a horizontal rule (at its vertical center, spanning its
// width), and a box whose width is thin becomes a vertical rule. A box thin in
// both directions (a dot) is dropped by the length guard.
func (c *collector) rulesFromFilledBars(p *render.Path) {
	for _, sub := range subpathBounds(p) {
		w := sub.maxX - sub.minX
		h := sub.maxY - sub.minY
		switch {
		case h <= maxRuleThickness && w >= minRuleLength:
			c.rules = append(c.rules, rule{horizontal: true, a: (sub.minY + sub.maxY) / 2, b0: sub.minX, b1: sub.maxX})
		case w <= maxRuleThickness && h >= minRuleLength:
			c.rules = append(c.rules, rule{horizontal: false, a: (sub.minX + sub.maxX) / 2, b0: sub.minY, b1: sub.maxY})
		}
	}
}

// bounds is an axis-aligned bounding box of one subpath.
type bounds struct{ minX, minY, maxX, maxY float64 }

// subpathBounds returns the bounding box of every subpath in p (a subpath runs
// from a MoveTo to the next MoveTo or the path end). Only subpaths with at least
// three distinct points are considered (a rectangle has four corners plus a
// close), so a stray single line is not mistaken for a filled bar.
func subpathBounds(p *render.Path) []bounds {
	var out []bounds
	var cur bounds
	var pts int
	flush := func() {
		if pts >= 3 {
			out = append(out, cur)
		}
		pts = 0
	}
	add := func(x, y float64) {
		if pts == 0 {
			cur = bounds{minX: x, minY: y, maxX: x, maxY: y}
		} else {
			cur.minX, cur.maxX = math.Min(cur.minX, x), math.Max(cur.maxX, x)
			cur.minY, cur.maxY = math.Min(cur.minY, y), math.Max(cur.maxY, y)
		}
		pts++
	}
	for _, s := range p.Segments {
		switch s.Kind {
		case render.MoveTo:
			flush()
			add(s.P0.X, s.P0.Y)
		case render.LineTo:
			add(s.P0.X, s.P0.Y)
		case render.CubeTo:
			add(s.P1.X, s.P1.Y)
			add(s.P2.X, s.P2.Y)
		case render.Close:
			// no new point; the subpath continues until the next MoveTo
		}
	}
	flush()
	return out
}

// nullDevice is a no-op render.Device. The interpreter requires a Device even when
// we only want the capture sinks (it still issues FillGlyph/Fill/Stroke calls),
// so this discards every paint call. It mirrors recDevice in interp_test.go but
// records nothing.
type nullDevice struct {
	w, h int
	c    *collector // receives DrawImage calls; nil discards them

	// clip is the bounding box of the active clip path(s); clipStack holds the
	// boxes Save has pushed. Only images consult it (to crop to what is visible).
	clip      clipBox
	clipStack []clipBox
}

func (d *nullDevice) Size() (int, int)                        { return d.w, d.h }
func (d *nullDevice) Fill(*render.Path, render.FillPaint)     {}
func (d *nullDevice) Stroke(*render.Path, render.StrokePaint) {}
func (d *nullDevice) DrawImage(img image.Image, ctm render.Matrix, alpha float64, _ string) {
	if d.c != nil {
		d.c.image(img, ctm, alpha, d.clip)
	}
}
func (d *nullDevice) FillGlyph(*render.Path, render.FillColor, string) {}
func (d *nullDevice) DrawGlyph(render.GlyphRef)                        {}
func (d *nullDevice) FillShading(render.Shader, render.Matrix, string) {}
func (d *nullDevice) PushClip(p *render.Path, _ render.FillRule) {
	d.clip = d.clip.intersect(boundsOf(p))
}
func (d *nullDevice) BeginGroup()                                                  {}
func (d *nullDevice) EndGroup(float64, string, render.GroupMask, render.GroupMask) {}
func (d *nullDevice) Save()                                                        { d.clipStack = append(d.clipStack, d.clip) }
func (d *nullDevice) Restore() {
	if n := len(d.clipStack); n > 0 {
		d.clip, d.clipStack = d.clipStack[n-1], d.clipStack[:n-1]
	}
}
func (d *nullDevice) BuildClipMask([]render.MaskPath) render.GroupMask {
	return image.NewAlpha(image.Rectangle{})
}
func (d *nullDevice) BuildLuminanceMask(image.Point, bool, func(render.Device)) render.GroupMask {
	return image.NewAlpha(image.Rectangle{})
}

// RenderOffscreen returns nil: this device collects text, not pixels, so it
// has no raster surface — the documented "cannot rasterize offscreen"
// degradation (see render.Device). Text inside a filtered element is still
// collected, because the caller falls back to painting unfiltered.
func (d *nullDevice) RenderOffscreen(image.Point, func(render.Device)) *image.RGBA {
	return nil
}

// Collect runs the content interpreter over one page with the capture sinks and
// returns the recovered glyphs, ruling lines, and page size. It recovers at the
// page boundary: a panic in the interpreter (a defect, or a malformed construct
// that slips a bounds check) is caught and the partially-collected content is
// returned rather than propagating — the same discipline raster.RenderPage uses,
// since a batch of pages must not die on one bad page. logf, if non-nil, receives
// diagnostics; it may be nil.
func Collect(doc *pdf.Document, pageIndex int, logf func(string, ...any)) (pc *pageContent, err error) {
	if doc == nil {
		return nil, errNilDocument
	}
	pg, err := doc.Page(pageIndex)
	if err != nil {
		return nil, err
	}

	c := &collector{}
	wpt := pg.MediaBox.Width()
	hpt := pg.MediaBox.Height()
	pc = &pageContent{width: wpt, height: hpt}

	// Recover at the page boundary. On panic, return what was collected so far.
	defer func() {
		if r := recover(); r != nil {
			if logf != nil {
				logf("extract: recovered from panic collecting page %d: %v", pageIndex, r)
			}
			pc.glyphs = c.glyphs
			pc.rules = c.rules
			pc.images = c.images
			err = nil
		}
	}()

	contentBytes, cerr := pg.ContentBytes()
	if cerr != nil {
		// No content (or undecodable) is a blank page, not an error.
		if logf != nil {
			logf("extract: page %d content unavailable: %v", pageIndex, cerr)
		}
		return pc, nil
	}

	// Drive the interpreter at the identity page scale (device units == points).
	// pageExtractMatrix flips PDF y-up into a y-down device frame so downstream
	// reading order (top-to-bottom) matches human reading, honoring /Rotate.
	base := pageExtractMatrix(pg)
	res := &pageResources{doc: pg.Doc(), dict: pg.Resources, logf: logf}
	interp := content.New(pg.Doc(), &nullDevice{w: int(math.Ceil(wpt)), h: int(math.Ceil(hpt)), c: c}, res, base, content.Options{
		Logf:         logf,
		MaxOps:       5_000_000,
		TextSink:     c.text,
		GraphicsSink: c.vector,
	})
	if rerr := interp.Run(contentBytes); rerr != nil {
		if logf != nil {
			logf("extract: page %d interpret: %v", pageIndex, rerr)
		}
	}
	pc.glyphs = c.glyphs
	pc.rules = c.rules
	pc.images = c.images
	return pc, nil
}

// pageExtractMatrix maps PDF user space (points, y-up, origin bottom-left) to a
// device frame (y-down, origin top-left) at 1:1 scale, honoring /Rotate — the
// scale-1 specialization of raster.pageMatrix. The y-flip is essential: it makes a
// larger device y mean "lower on the page", so sorting glyphs by ascending y
// yields natural top-to-bottom reading order.
func pageExtractMatrix(pg *pdf.Page) render.Matrix {
	w := pg.MediaBox.Width()
	h := pg.MediaBox.Height()
	switch pg.Rotate {
	case 90:
		return render.Matrix{A: 0, B: 1, C: 1, D: 0, E: 0, F: 0}
	case 180:
		return render.Matrix{A: -1, B: 0, C: 0, D: 1, E: w, F: 0}
	case 270:
		return render.Matrix{A: 0, B: -1, C: -1, D: 0, E: h, F: w}
	default:
		return render.Matrix{A: 1, B: 0, C: 0, D: -1, E: 0, F: h}
	}
}

// minmax returns a,b ordered so the first is the smaller.
func minmax(a, b float64) (float64, float64) {
	if a <= b {
		return a, b
	}
	return b, a
}

// pageResources is the extractor's content.Resources implementation. Font and
// Form delegate to pkg/pdf/pageres, shared with the raster backend. Only those
// two matter for text/rule capture, plus Image and InlineImage for picture
// capture. Shadings and patterns paint into the nullDevice and are irrelevant to
// structure, so those return "unresolved", which the interpreter handles
// gracefully (it skips the op).
type pageResources struct {
	doc  *pdf.Document
	dict pdf.Dict
	logf func(string, ...any)
}

// Font resolves a font resource by name to a GlyphSource, so the interpreter can
// decode show-strings into runes and advances. This is the one resource the
// extractor genuinely needs. Unsupported fonts return nil (the interpreter still
// advances the pen, so column geometry survives even for unmappable text).
//
// Extraction has no font-provider wiring (it recovers text/structure, not exact
// visual faces), so it passes a nil provider — the bundled substitutes resolve
// non-embedded fonts.
func (r *pageResources) Font(name string) content.GlyphSource {
	return pageres.ResolveFont(r.doc, r.dict, name, nil, "extract", r.logf)
}

// Form resolves a form XObject to its decoded content, scoped resources, matrix,
// BBox, and whether it declares a transparency /Group, so text inside a form (a
// common wrapper) is captured too. A form without its own /Resources inherits
// the page's.
func (r *pageResources) Form(name string) ([]byte, content.Resources, render.Matrix, *[4]float64, bool, bool) {
	data, childRes, m, bbox, isGroup, ok := pageres.ResolveForm(r.doc, r.dict, name, "extract", r.logf)
	if !ok {
		return nil, nil, render.Identity, nil, false, false
	}
	child := &pageResources{doc: r.doc, dict: childRes, logf: r.logf}
	return data, child, m, bbox, isGroup, true
}

// Image decodes an image XObject through the rasterizer's decoder, so the
// extractor sees the same pixels a viewer paints.
func (r *pageResources) Image(name string, fill render.FillColor) (image.Image, bool) {
	return raster.ResolveImage(r.doc, r.dict, name, fill, r.logf)
}

// InlineImage decodes a BI...ID...EI inline image the same way.
func (r *pageResources) InlineImage(dict pdf.Dict, data []byte, fill render.FillColor) (image.Image, bool) {
	return raster.ResolveInlineImage(r.doc, dict, data, fill, r.logf)
}

// Shading reports "absent": shadings paint gradients we do not extract.
func (r *pageResources) Shading(string) (render.Shader, bool) { return nil, false }

// Pattern reports "absent": patterns are fills we do not extract.
func (r *pageResources) Pattern(string) (render.Shader, render.Matrix, bool) {
	return nil, render.Identity, false
}

// ExtGState reports "absent": alpha/blend state does not affect captured geometry.
func (r *pageResources) ExtGState(string) (content.ExtGStateParams, bool) {
	return content.ExtGStateParams{}, false
}

// ColorSpace reports "not a Separation/DeviceN space": tint transforms only affect
// paint color, which the extractor ignores.
func (r *pageResources) ColorSpace(string) (*content.TintTransform, bool) { return nil, false }
