package css

import (
	"context"
	"image/color"
	"strings"

	gcss "github.com/nathanstitt/omnidoc/pkg/internal/css"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/cssbox"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/inline"
)

// appendMarginBoxes lays out and appends a page's @page margin boxes (running
// headers/footers) to its item list, AFTER the page content so they paint over the
// margin band. g carries the page geometry (size + margins + the resolved UsedPage);
// pageIndex is the zero-based page number and pageCount the total — both feed the page
// counters counter(page) / counter(pages). running holds the document's out-of-flow
// running elements (CSS GCPM) keyed by name, so a box whose content is element(name)
// paints the captured running element instead of text; nil/empty when none exist. It is
// a no-op when the page has no @page rule or no margin boxes (the common case →
// byte-identical).
func (e *Engine) appendMarginBoxes(items []layout.Item, g pageGeom, pageIndex, pageCount int, ps pageStrings, running map[string]*cssbox.Box) []layout.Item {
	if !g.used.HasRule || len(g.used.MarginBoxes) == 0 {
		return items
	}
	// First pass: resolve each box's content (text OR an element() reference) + measure
	// its width (for edge distribution). An element() box reserves the FULL band width
	// (its placement centers within the band), so it pins to the lead corner.
	type mbItem struct {
		slot    gcss.MarginBoxSlot
		text    string
		element string // running-element name when content is element(name); "" otherwise
		decls   []gcss.Declaration
		width   float64
	}
	var items2 []mbItem
	boxW := map[gcss.MarginBoxSlot]float64{}
	for _, mb := range g.used.MarginBoxes {
		// content: element(name) — paint the captured running element (formatted markup),
		// not text. An element() reference with no captured running element for the name is
		// dropped (graceful), exactly like an empty text box.
		if name := marginElementName(mb.Content); name != "" {
			if running[name] == nil {
				continue
			}
			items2 = append(items2, mbItem{slot: mb.Slot, element: name, decls: mb.Decls})
			continue
		}
		text := resolveMarginContentWithStrings(mb.Content, pageIndex+1, pageCount, ps)
		if text == "" {
			continue
		}
		cs := gcss.Stylesheet{}.ComputeMarginBox(mb.Decls, e.marginStyle())
		run := inline.Run{Text: text, Family: cs.FontFamily, Bold: cs.Bold, Italic: cs.Italic, SizePt: cs.FontSizePt, Color: cs.Color}
		glyphs := inline.Shape(e.faces, []inline.Run{run}, e.logf)
		w := 0.0
		if len(glyphs) > 0 {
			w = inline.MakeLine(glyphs).WidthPt
		}
		boxW[mb.Slot] = w
		items2 = append(items2, mbItem{slot: mb.Slot, text: text, decls: mb.Decls, width: w})
	}
	// Second pass: place each box in its distributed rect.
	for _, it := range items2 {
		if it.element != "" {
			// An element() box centers within its edge's FULL band (the raw slot rect, not
			// the text-shrink distribution): the running element is a self-contained block
			// laid out at the band width, then centered by appendMarginElement. This matches
			// the single-box-per-edge model (one centered/edge box per side).
			r := marginBoxRect(it.slot, g)
			if r.w <= 0 || r.h <= 0 {
				continue
			}
			items = e.appendMarginElement(items, running[it.element], r)
			continue
		}
		r := marginBoxRectShared(it.slot, g, boxW)
		if r.w <= 0 || r.h <= 0 {
			continue
		}
		items = e.appendMarginText(items, it.text, it.decls, r)
	}
	return items
}

// marginElementName returns the name from a `content: element(name)` value (lowercased
// so it matches a running()-element name), or "" if the content is not an element()
// reference.
func marginElementName(content string) string {
	c := strings.TrimSpace(content)
	if !strings.HasPrefix(c, "element(") || !strings.HasSuffix(c, ")") {
		return ""
	}
	name := strings.TrimSpace(c[len("element(") : len(c)-1])
	if name == "" {
		return ""
	}
	return strings.ToLower(name)
}

// appendMarginElement places the captured running element box centered within band rect
// r. It delegates to placeRunningElementBox, which re-captures the fragment fresh per call
// so the shared box is never corrupted across pages. The capture is a self-contained leaf
// layout, so it uses a background context (the document-level context already gated the
// main layout).
func (e *Engine) appendMarginElement(items []layout.Item, box *cssbox.Box, r marginRect) []layout.Item {
	return e.placeRunningElementBox(context.Background(), items, box, r)
}

// resolveMarginContent resolves an @page margin box `content` value to a string for the
// given 1-based page number and total page count, with NO string-set values available
// (string(name) resolves to ""). It is a thin wrapper over resolveMarginContentWithStrings
// for callers (and the early-return single-page paths) that carry no per-page snapshot.
func resolveMarginContent(content string, page, pages int) string {
	return resolveMarginContentWithStrings(content, page, pages, pageStrings{})
}

// resolveMarginContentWithStrings resolves an @page margin box `content` value to a string
// for the given 1-based page number and total page count, using ps (the page's string-set
// snapshot) to resolve string(). It supports a sequence of components:
// a quoted "literal", counter(page), counter(pages), counter(page|pages, <style>) where
// <style> is any list-style the css.FormatCounter helper handles (decimal,
// decimal-leading-zero, lower/upper-roman, lower/upper-alpha; an unknown style falls back
// to decimal), and string(name) / string(name, first|last|start|first-except) (CSS GCPM
// running strings — the value carried into / first set on / last set through this page).
// Components are concatenated.
// An unsupported component (e.g. element(), attr()) contributes nothing — a documented
// deferral (see the design doc). An empty or `normal`/`none` value yields "".
func resolveMarginContentWithStrings(content string, page, pages int, ps pageStrings) string {
	content = strings.TrimSpace(content)
	if content == "" || content == "normal" || content == "none" {
		return ""
	}
	var b strings.Builder
	for _, comp := range splitContentComponents(content) {
		switch {
		case len(comp) >= 2 && (comp[0] == '"' || comp[0] == '\''):
			b.WriteString(unquote(comp))
		case strings.HasPrefix(comp, "counter("):
			inner := strings.TrimSuffix(strings.TrimPrefix(comp, "counter("), ")")
			parts := strings.SplitN(inner, ",", 2)
			name := strings.TrimSpace(parts[0])
			style := "decimal"
			if len(parts) == 2 {
				if s := strings.TrimSpace(parts[1]); s != "" {
					style = s
				}
			}
			switch name {
			case "page":
				b.WriteString(gcss.FormatCounter(page, style))
			case "pages":
				b.WriteString(gcss.FormatCounter(pages, style))
			}
		case strings.HasPrefix(comp, "string("):
			inner := strings.TrimSuffix(strings.TrimPrefix(comp, "string("), ")")
			parts := strings.SplitN(inner, ",", 2)
			name := strings.ToLower(strings.TrimSpace(parts[0]))
			keyword := ""
			if len(parts) == 2 {
				keyword = strings.ToLower(strings.TrimSpace(parts[1]))
			}
			b.WriteString(ps.stringValue(name, keyword))
		default:
			// element(...) / attr(...) / unknown: contribute nothing (deferred). Bare
			// unquoted idents are not valid content; skipped.
		}
	}
	return b.String()
}

// splitContentComponents splits a content value into its top-level components
// (whitespace-separated, but keeping quoted strings and counter(...) parens intact).
func splitContentComponents(s string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	var quote byte
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			cur.WriteByte(c)
			if c == quote {
				quote = 0
				flush()
			}
		case c == '"' || c == '\'':
			flush()
			quote = c
			cur.WriteByte(c)
		case c == '(':
			depth++
			cur.WriteByte(c)
		case c == ')':
			depth--
			cur.WriteByte(c)
			if depth == 0 {
				flush()
			}
		case (c == ' ' || c == '\t' || c == '\n') && depth == 0:
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// marginRect is a margin box's rectangle in page space (points, Y-down).
type marginRect struct{ x, y, w, h float64 }

// marginBoxRect computes the rectangle of margin box slot within page geometry g (CSS
// Paged Media §8.3.1 corner/edge geometry). The corners occupy the intersection of the
// two margins; each edge box spans the page content width/height between the corners.
// For the common single-box-per-edge authoring (@top-center etc.) the three boxes of an
// edge share the edge band; this model gives each the FULL edge span (a simplification —
// see the design doc — adequate for one centered/edge-aligned box per side).
func marginBoxRect(slot gcss.MarginBoxSlot, g pageGeom) marginRect {
	mT, mL := g.marginT, g.marginL
	mR := g.pageW - g.marginL - g.contentW
	mB := g.pageH - g.marginT - g.contentH
	cw, ch := g.contentW, g.contentH
	switch slot {
	// Top edge boxes: y in [0, mT), x spanning the content width.
	case gcss.MarginTopLeft, gcss.MarginTopCenter, gcss.MarginTopRight:
		return marginRect{x: mL, y: 0, w: cw, h: mT}
	// Bottom edge boxes: y in [pageH-mB, pageH).
	case gcss.MarginBottomLeft, gcss.MarginBottomCenter, gcss.MarginBottomRight:
		return marginRect{x: mL, y: g.pageH - mB, w: cw, h: mB}
	// Left edge boxes: x in [0, mL), y spanning the content height.
	case gcss.MarginLeftTop, gcss.MarginLeftMiddle, gcss.MarginLeftBottom:
		return marginRect{x: 0, y: mT, w: mL, h: ch}
	// Right edge boxes.
	case gcss.MarginRightTop, gcss.MarginRightMiddle, gcss.MarginRightBottom:
		return marginRect{x: g.pageW - mR, y: mT, w: mR, h: ch}
	// Corners.
	case gcss.MarginTopLeftCorner:
		return marginRect{x: 0, y: 0, w: mL, h: mT}
	case gcss.MarginTopRightCorner:
		return marginRect{x: g.pageW - mR, y: 0, w: mR, h: mT}
	case gcss.MarginBottomLeftCorner:
		return marginRect{x: 0, y: g.pageH - mB, w: mL, h: mB}
	case gcss.MarginBottomRightCorner:
		return marginRect{x: g.pageW - mR, y: g.pageH - mB, w: mR, h: mB}
	}
	return marginRect{}
}

// marginBoxRectShared computes a margin box's rect, distributing each edge's three
// boxes (left/center/right) within the edge band by their measured content widths
// (CSS Paged Media §8.3.1): the leading box pins to the leading corner, the trailing
// box to the trailing corner, the center box centers. boxW maps a present slot to its
// laid-out content width (in points); a slot absent from boxW reserves no space. Corner
// slots delegate to marginBoxRect (their geometry is unaffected by siblings).
func marginBoxRectShared(slot gcss.MarginBoxSlot, g pageGeom, boxW map[gcss.MarginBoxSlot]float64) marginRect {
	band := marginBoxRect(slot, g) // full-span band for this slot's edge (or the corner rect)
	lead, center, trail, horizontal, ok := edgeTriple(slot)
	if !ok {
		return band // a corner: unchanged
	}
	// The box's own content extent (w) is what pins the trailing/center boxes AND sizes
	// their rect: each distributed box gets a rect of its OWN width w at its pinned
	// origin, so appendMarginText's per-box text-align resolves WITHIN that exact-width
	// rect (a full-band width here would double-apply the alignment — a trailing box
	// would pin right, then right-align again within the band, landing off-page). A
	// zero/unmeasured w (e.g. an element() box, sized elsewhere) falls back to the full
	// band so the box is not collapsed to nothing.
	w := boxW[slot]
	switch {
	case horizontal:
		bw := w
		if bw <= 0 {
			bw = band.w
		}
		switch slot {
		case lead:
			return marginRect{x: band.x, y: band.y, w: bw, h: band.h}
		case trail:
			return marginRect{x: band.x + band.w - bw, y: band.y, w: bw, h: band.h}
		case center:
			return marginRect{x: band.x + (band.w-bw)/2, y: band.y, w: bw, h: band.h}
		}
	default: // vertical edge: distribute along Y (the box's extent runs along Y here)
		bh := w
		if bh <= 0 {
			bh = band.h
		}
		switch slot {
		case lead:
			return marginRect{x: band.x, y: band.y, w: band.w, h: bh}
		case trail:
			return marginRect{x: band.x, y: band.y + band.h - bh, w: band.w, h: bh}
		case center:
			return marginRect{x: band.x, y: band.y + (band.h-bh)/2, w: band.w, h: bh}
		}
	}
	return band
}

// edgeTriple returns the three slots of slot's edge (lead, center, trail), whether the
// edge is horizontal (top/bottom) vs vertical (left/right), and ok=false for a corner.
func edgeTriple(slot gcss.MarginBoxSlot) (lead, center, trail gcss.MarginBoxSlot, horizontal, ok bool) {
	switch slot {
	case gcss.MarginTopLeft, gcss.MarginTopCenter, gcss.MarginTopRight:
		return gcss.MarginTopLeft, gcss.MarginTopCenter, gcss.MarginTopRight, true, true
	case gcss.MarginBottomLeft, gcss.MarginBottomCenter, gcss.MarginBottomRight:
		return gcss.MarginBottomLeft, gcss.MarginBottomCenter, gcss.MarginBottomRight, true, true
	case gcss.MarginLeftTop, gcss.MarginLeftMiddle, gcss.MarginLeftBottom:
		return gcss.MarginLeftTop, gcss.MarginLeftMiddle, gcss.MarginLeftBottom, false, true
	case gcss.MarginRightTop, gcss.MarginRightMiddle, gcss.MarginRightBottom:
		return gcss.MarginRightTop, gcss.MarginRightMiddle, gcss.MarginRightBottom, false, true
	}
	return 0, 0, 0, false, false
}

// appendMarginText lays out text on a single line within rect r (the margin box),
// aligned per the box's text-align, vertically centered in the band, and appends its
// glyphs to items. The text styling (font-family/size, color, text-align) is resolved by
// running the box's declarations through the CSS cascade over the root element's font
// and color (marginStyle) — so a margin box styled `font: bold 9pt sans-serif; color:
// gray` renders accordingly, and an unstyled one takes the document's font.
func (e *Engine) appendMarginText(items []layout.Item, text string, decls []gcss.Declaration, r marginRect) []layout.Item {
	cs := gcss.Stylesheet{}.ComputeMarginBox(decls, e.marginStyle())
	run := inline.Run{
		Text:   text,
		Family: cs.FontFamily,
		Bold:   cs.Bold,
		Italic: cs.Italic,
		SizePt: cs.FontSizePt,
		Color:  cs.Color,
	}
	glyphs := inline.Shape(e.faces, []inline.Run{run}, e.logf)
	if len(glyphs) == 0 {
		return items // no resolvable font/glyphs
	}
	// MakeLine, not MakeVisualLine: a page margin box is generated by the page context
	// rather than the document tree, so it has no inherited `direction` to reorder
	// against (see alignOf, which resolves start/end as LTR for the same reason).
	line := inline.MakeLine(glyphs)
	p := inline.Place(alignOf(cs.TextAlign), r.x, r.w, line.WidthPt, inline.CountSpaces(line.Glyphs), true)
	// Vertical: center the line's text height in the band.
	asc := ascentOfLine(line)
	textH := line.AscentPt + line.DescentPt
	baselineY := r.y + (r.h-textH)/2 + asc
	x := p.StartX
	for gi := range line.Glyphs {
		g := &line.Glyphs[gi]
		if g.Outline != nil {
			items = append(items, layout.Item{
				Kind: layout.GlyphKind,
				Glyph: layout.GlyphItem{
					Outline: g.Outline, XPt: x, YPt: baselineY, SizePt: g.SizePt,
					Color: color.RGBA{R: g.Color.R, G: g.Color.G, B: g.Color.B, A: g.Color.A},
					Face:  g.Face, GID: g.GID, Runes: g.Runes,
				},
			})
		}
		x += g.Advance
		if g.Space {
			x += p.ExtraPerSpace
		}
	}
	return items
}

// marginBoxBaseStyle is the UA default a margin box falls back to when the root element
// sets no font: the serif body font at 11pt, black, left-aligned. marginBaseFrom layers
// the root element's font and color over it.
func marginBoxBaseStyle() gcss.ComputedStyle {
	return gcss.ComputedStyle{
		FontFamily: "serif",
		FontSizePt: 11,
		Color:      color.RGBA{0, 0, 0, 255},
		TextAlign:  "left",
	}
}

// marginBaseFrom is the style a margin box inherits. CSS Paged Media puts the
// page context under the root element, so a footer takes the document's font,
// size and color unless its own declarations set them. A field the root leaves
// unset (empty family, zero size, transparent color) keeps the fallback (serif,
// 11pt, black). @page-level declarations are not applied: UsedPage does not
// carry them.
func marginBaseFrom(root gcss.ComputedStyle) gcss.ComputedStyle {
	base := marginBoxBaseStyle()
	if root.FontFamily != "" {
		base.FontFamily = root.FontFamily
	}
	if root.FontSizePt > 0 {
		base.FontSizePt = root.FontSizePt
	}
	base.Bold, base.Italic = root.Bold, root.Italic
	if root.Color.A != 0 {
		base.Color = root.Color
	}
	return base
}

// setMarginBase records the root element's inherited style for this layout's margin
// boxes. It resets on every paged layout so an Engine reused for another document
// never carries the previous root's font.
func (e *Engine) setMarginBase(root *cssbox.Box) {
	e.marginBase = gcss.ComputedStyle{}
	if root != nil {
		e.marginBase = marginBaseFrom(root.Style)
	}
}

// marginStyle is the base a margin box's declarations cascade over.
func (e *Engine) marginStyle() gcss.ComputedStyle {
	if e.marginBase.FontFamily == "" {
		return marginBoxBaseStyle()
	}
	return e.marginBase
}

// alignOf maps a CSS text-align value to the inline core's Align for a page margin
// box. A margin box is generated by the page context rather than the document tree,
// so it has no inherited `direction` to resolve against; "start"/"end" therefore
// resolve LTR (and marginBoxBaseStyle sets the physical "left" anyway).
func alignOf(textAlign string) inline.Align {
	return mapTextAlign(textAlign, "ltr")
}
