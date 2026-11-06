package css

import (
	"context"

	gcss "github.com/nathanstitt/omnidoc/pkg/internal/css"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/cssbox"
	layoutfont "github.com/nathanstitt/omnidoc/pkg/internal/layout/font"
	"github.com/nathanstitt/omnidoc/pkg/resource"
)

// Engine lays out a cssbox tree into a positioned fragment tree at a fixed
// viewport width (the single-tall-page model). Safe for concurrent use: its only
// shared state is the face cache (itself concurrent). Build with New.
//
// The engine implements the block formatting context (this file) and delegates
// inline-level content to the inline formatting context (inline.go); the two meet
// at layoutInline, the documented hook a block box establishing an inline
// formatting context calls.
type Engine struct {
	faces  *layoutfont.FaceCache
	images *imageCache
	// svgs is the VECTOR counterpart to images: an SVG resource is parsed into an
	// svg.Document and painted through layout.VectorItem, never decoded into an
	// image.Image. Keeping the two caches separate is what stops SVG silently
	// acquiring a bitmap round trip.
	svgs *svgCache
	// inlineSVGs memoizes parses of inline <svg> markup (no ref, no loader).
	inlineSVGs *inlineSVGCache
	// authorSheets are the document's AUTHOR stylesheets, retained so they can cascade
	// into an inline <svg> (see Engine.svgHostContext). An inline <svg> is part of the
	// host document's tree, so a rule like `.icon { fill: blue }` applies to its
	// children — but the SVG is re-parsed from serialized markup by pkg/svg, which
	// never sees the host's sheets unless they are handed to it explicitly.
	//
	// Only AUTHOR sheets are carried: the UA sheet styles HTML elements and has no
	// SVG rules, and passing it would make every inline <svg> parse consult ~40
	// irrelevant rules per element.
	authorSheets []gcss.Stylesheet
	logf         func(string, ...any)
	// marginBase is the style @page margin boxes inherit (the root element's
	// font and color), set at the start of a paged layout. Zero means "not set".
	marginBase gcss.ComputedStyle
	// measures memoizes per-box min/max-content widths within ONE layout. measureContent
	// is a pure function of the box subtree and the (fixed) face cache, but table auto
	// layout, grid track sizing, and flex base sizing each measure every cell/item for
	// BOTH min- and max-content — and measureInline re-gathers+re-shapes (and even lays
	// out inline-block atoms) on each call. Caching keyed by (box, wantMax) collapses that
	// to once per box per intrinsic size. The engine is created per layout (and layout is
	// single-threaded — only rasterization fans out), so this needs no lock and does not
	// leak across documents; the box tree is read-only after box generation, so a cached
	// width is exactly what a fresh measure would return (output stays byte-identical).
	measures map[*cssbox.Box]*minMaxContent
	// warned records the keys warnOnce has already emitted, so one unsupported
	// construct repeated across a document logs once rather than once per box. The
	// engine is per-layout and layout is single-threaded (only rasterization fans
	// out), so this needs no lock — the same reasoning measures relies on.
	warned map[string]bool
	// rootFontSizePt is the ROOT element's computed font size, recorded by
	// layoutTree (the single choke point every public entry goes through) so a
	// `rem` length can resolve against it. CSS defines rem as the root's font
	// size, and no other part of the engine carries it: the cascade folds `rem`
	// into UnitEm at parse time (pkg/css's parseLength), which is a separate,
	// pre-existing approximation this does NOT fix — it only makes the one
	// property parsed at use time, `filter`, resolve rem correctly.
	//
	// Zero means "not laid out through layoutTree" (a hand-built Engine in a
	// unit test), which the resolver treats as a fallback rather than as a zero
	// length.
	rootFontSizePt float64
}

// warnOnce logs a formatted diagnostic the FIRST time it is called with key, and
// does nothing on every later call with the same key. It is for a degraded-but-
// rendered construct that may appear on many boxes (an unsupported background
// repeat, say), where one line names the gap and N lines are noise.
func (e *Engine) warnOnce(key, format string, args ...any) {
	if e.warned[key] {
		return
	}
	e.warned[key] = true
	e.logf(format, args...)
}

// minMaxContent holds a box's memoized intrinsic widths; each is filled lazily and
// guarded by its own set flag (0 is a legitimate width, so a bool is needed).
type minMaxContent struct {
	min, max       float64
	minSet, maxSet bool
}

// New returns an Engine that resolves fonts through faces, decodes replaced-element
// images (e.g. <img>) through loader, and logs unsupported or degraded cases through
// logf. A nil faces builds a fresh cache; a nil loader means images cannot be fetched
// (every <img> degrades to a placeholder); a nil logf is a no-op — so callers need
// supply only what they have.
// SetAuthorSheets supplies the document's author stylesheets so they cascade into
// inline <svg> content. Optional: an engine without them lays out identically except
// that inline SVG children are styled by their own markup alone.
func (e *Engine) SetAuthorSheets(sheets []gcss.Stylesheet) { e.authorSheets = sheets }

func New(faces *layoutfont.FaceCache, loader resource.ResourceLoader, logf func(string, ...any)) *Engine {
	if faces == nil {
		faces = layoutfont.NewFaceCache()
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Engine{
		faces:      faces,
		images:     newImageCache(loader, logf),
		svgs:       newSVGCache(loader, logf),
		inlineSVGs: newInlineSVGCache(),
		logf:       logf,
		measures:   make(map[*cssbox.Box]*minMaxContent),
		warned:     make(map[string]bool),
	}
}

// Layout lays out root at viewportW points and returns a single tall page sized
// viewportW × content-height. It honors ctx cancellation (checked between block
// children, like the flat engine checks between blocks) and never panics on
// malformed input: a recover at the page boundary returns whatever was built —
// at minimum a single empty page.
func (e *Engine) Layout(ctx context.Context, root *cssbox.Box, viewportW float64) (pages *layout.Pages, err error) {
	defer func() {
		if r := recover(); r != nil {
			e.logf("css layout: recovered from panic: %v", r)
			pages = &layout.Pages{Pages: []layout.Page{{WidthPt: viewportW, HeightPt: 0}}}
			err = nil
		}
	}()

	// CSS background propagation: lift the root/body background onto the page canvas
	// (set on the returned Pages below) and clear it from the source box so it is not
	// painted twice. Must run before layoutTree builds the fragments.
	canvasBG := propagateCanvasBackground(root)
	defer func() {
		if pages != nil {
			pages.CanvasBackground = canvasBG
		}
	}()

	frag := e.layoutTree(ctx, root, viewportW)
	if frag == nil {
		return &layout.Pages{Pages: []layout.Page{{WidthPt: viewportW, HeightPt: 0}}}, nil
	}
	// The content height is the root fragment's bottom edge in page space. The root
	// is laid out at the ICB origin (0,0) with a zero outer margin (the ICB has no
	// margins to collapse against), so its border-box bottom is the page height.
	contentH := frag.Y + frag.H
	page := frag.Page(viewportW, contentH)
	return &layout.Pages{Pages: []layout.Page{page}}, nil
}

// layoutTree establishes the initial containing block (ICB) — width viewportW,
// origin (0,0), height auto — and lays out root as a block within it, returning
// the root's positioned border-box fragment (or nil for a degenerate root). It is
// the seam Layout flattens and tests assert geometry against directly, before the
// flatten to a layout.Page.
func (e *Engine) layoutTree(ctx context.Context, root *cssbox.Box, viewportW float64) *Fragment {
	if root == nil {
		return nil
	}
	// Record the root font size before any box is laid out: a `rem` length in a
	// filter chain resolves against it (see Engine.rootFontSizePt). This is the
	// single choke point Layout, LayoutPaged, and LayoutPagedDoc all pass
	// through, so recording it here covers every entry point.
	e.rootFontSizePt = root.Style.FontSizePt
	fc := &floatContext{cbLeft: 0, cbRight: viewportW}
	posCtx := &positionedContext{}
	pageCB := posCBOwner{isPage: true}
	// Root BFC: bandOriginY = 0 (its content-box top defines the frame origin).
	res := e.layoutBlock(ctx, root, viewportW, 0, 0, 0, fc, posCtx, pageCB)
	if res.frag != nil {
		res.frag.IsBFC = true
		res.frag.IsStackingContext = true // the ICB establishes the root stacking context
		// The root is the BFC owner: collect any floats placed directly in it. (A
		// nested-BFC box collects its own via layoutInterior -> in.bfcFloats.)
		if res.frag.Floats == nil {
			res.frag.Floats = fc.floats2frags()
		}
		// The root is the outermost stacking context: consume any relative-positioned
		// descendants that bubbled all the way up without a closer positioned ancestor.
		for _, pp := range res.pendingPositioned {
			res.frag.PositionedInfo = append(res.frag.PositionedInfo, PositionedInfo{CBOwned: false, ClipChain: pp.clipChain})
			res.frag.Positioned = append(res.frag.Positioned, pp.frag)
		}
	}
	// PASS 2: resolve abs/fixed boxes now that the page height and all ancestor
	// fragments are final. An abs-pos box does NOT extend the page height in this
	// slice (matching the float non-enclosure decision); growing the page for an
	// abs-pos box positioned past the bottom is deferred.
	pageH := 0.0
	if res.frag != nil {
		pageH = res.frag.Y + res.frag.H
	}
	e.resolveAbsolute(ctx, posCtx, res.frag, viewportW, pageH)
	return res.frag
}

// blockResult is the outcome of laying out one block box: its positioned
// border-box fragment plus its used (and collapse-resolved) top and bottom
// margins, which the caller folds into adjacent-sibling and parent/child margin
// collapsing. The margins are reported separately from the fragment because a
// fragment carries only its border box — margins live outside it.
type blockResult struct {
	frag         *Fragment
	marginTop    float64 // used top margin, already folded with the first child's per collapse-through
	marginBottom float64 // used bottom margin, already folded with the last child's per collapse-through
	// pendingPositioned bubbles relatively-positioned descendant fragments that have
	// not yet found their nearest stacking-context owner up to the caller (analogous
	// to bfcFloats surfacing a nested BFC's floats one level). layoutBlock sets it to
	// the interior's unconsumed pending list when b is NOT a stacking context (bubble),
	// or nil when b consumed them onto its own frag.Positioned. The parent's
	// layoutBlockChildren collects each child's pendingPositioned into its own list, so
	// a relative box under static ancestors bubbles up until a stacking-context
	// ancestor (or the root, in layoutTree) consumes it. Read by the parent loop.
	pendingPositioned []pendingPos
}

// posCBOwner names the containing block for absolutely-positioned descendants: the
// nearest positioned-ancestor fragment + its box (whose CONTENT box is the CB,
// derived by deflating the final border box), or the page sentinel (isPage) for the
// ICB / fixed CB. A fragment POINTER is captured (not a rect) because the ancestor
// is shifted into final page position as the recursion unwinds; pass 2 reads it
// after that, so coordinates are final. (See the spec: "How positioning threads".)
type posCBOwner struct {
	frag   *Fragment
	box    *cssbox.Box
	isPage bool
}

// deferredAbs is one collected absolutely/fixed-positioned box awaiting pass 2. Its
// stacking-context owner is NOT stored — it is derived in pass 2 from cb (cb.isPage ?
// root : cb.frag), since every positioned box is its own stacking context.
type deferredAbs struct {
	box *cssbox.Box
	cb  posCBOwner // its containing-block owner
}

// pendingPos is a relatively-positioned descendant bubbling toward its nearest
// stacking-context ancestor, plus the clip chain it has accumulated. clipChain holds
// the padding-box rects of the non-positioned overflow≠visible boxes it has bubbled OUT
// OF so far (outermost last appended, but stored outermost-first — see the prepend in
// layoutBlock). When the entry lands on a holder's Positioned, clipChain becomes that
// entry's PositionedInfo.ClipChain, so the descendant is clipped to those boxes even
// though it paints in an ancestor's layer (CSS: every overflow≠visible ancestor between
// a box and its containing block clips it).
type pendingPos struct {
	frag      *Fragment
	clipChain []rect
}

// positionedContext accumulates deferred abs/fixed boxes during the in-flow pass for
// resolution in the abs-pos pass. Per-Layout-call mutable state threaded by pointer
// through one goroutine; never escapes the call (concurrency-safe, like floatContext).
type positionedContext struct {
	deferred []deferredAbs
}

// layoutBlock lays out block box b into a containing block of width cbWidth whose
// left content edge is at originX, with the box's top margin edge at page-space y
// marginTopEdgeY. It returns the positioned border-box fragment and the box's used
// top/bottom margins (for the caller's margin collapsing).
//
// The fragment's X/Y are the border-box top-left: X = originX + usedMarginLeft,
// Y = marginTopEdgeY + result.marginTop. Children are positioned absolutely in
// page space within the fragment.
//
// bandOriginY is b's content-box top measured in the BFC-root-local frame (the frame the float context fc is queried in — see layoutBlockChildren's frame model); fc is the current block formatting context's float context.
//
// posCtx collects abs/fixed descendants (out of flow) for the abs-pos pass; posCB is
// the containing-block owner for absolutely-positioned descendants (the page sentinel
// at the root, or the nearest positioned ancestor's fragment+box). These thread
// exactly like fc/bandOriginY do for floats.
func (e *Engine) layoutBlock(ctx context.Context, b *cssbox.Box, cbWidth, originX, marginTopEdgeY, bandOriginY float64, fc *floatContext, posCtx *positionedContext, posCB posCBOwner) blockResult {
	if b.Kind == cssbox.BoxReplaced {
		// A block-level replaced box (e.g. <img style="display:block">) is sized by
		// the replaced-element algorithm, not the block content flow: with width:auto
		// it uses its INTRINSIC width (not the containing-block fill a normal block
		// gets). It has no in-flow children to collapse margins with, so its top/bottom
		// margins are solid. Margins/border/padding/min-max are honored by
		// replacedUsedSize + replacedFragment. A replaced box has no float children;
		// it ignores fc/bandOriginY.
		return e.layoutBlockReplaced(ctx, b, cbWidth, originX, marginTopEdgeY)
	}

	ed := usedEdges(b, cbWidth)

	// Resolve the content width the children flow into, then the border-box width.
	contentW := resolveContentWidth(b, cbWidth, ed)
	borderW := contentW + ed.pL + ed.pR + ed.bL + ed.bR

	// CSS 10.3.3: distribute the leftover inline space to whichever horizontal margins
	// are `auto`. This is what makes `margin: 0 auto` centre. It runs HERE rather than
	// in usedEdges because the equation needs the resolved width, and because
	// usedEdges is also called from intrinsic-width measurement and float sizing,
	// where an auto margin genuinely is zero.
	resolveAutoMarginsX(b, cbWidth, borderW, &ed)

	// The left edge of the content box in page space. The border-box left is
	// originX+mL; content sits inside the left border+padding.
	contentX := originX + ed.mL + ed.bL + ed.pL

	// Lay out the interior, producing child fragments / inline lines in a *local*
	// frame whose content-box top is local Y 0, plus the collapsing facts.
	//
	// The interior's band origin is this box's content-box top in the BFC-root
	// frame. (marginTopEdgeY is passed as 0 by the stacker in the provisional
	// layout; the float context is queried in the BFC-root frame via bandOriginY,
	// and the stacker's later shift repositions in-flow fragments — floats are
	// placed directly in the BFC frame, so they don't need that shift.)
	childBandOrigin := bandOriginY + ed.mT + ed.bT + ed.pT

	// A box that establishes a positioned containing block becomes the posCB owner for
	// its interior's abs-pos descendants (its own content box is their CB). Its frag is
	// not built yet, so capture {box: b} now and back-fill .frag after the frag exists
	// (below). A static box passes the inherited posCB through unchanged. Record where
	// posCtx.deferred ends so the back-fill targets only THIS box's newly-collected
	// abs-pos descendants.
	childPosCB := posCB
	if establishesStackingContext(b) {
		childPosCB = posCBOwner{box: b}
	}
	deferredBefore := len(posCtx.deferred)
	in := e.layoutInterior(ctx, b, contentW, contentX, childBandOrigin, fc, posCtx, childPosCB)

	// CSS tables and inline-grid containers are shrink-to-fit: a width:auto box wraps its
	// content width rather than filling the containing block. layoutTable reports the grid
	// width as in.intrinsicWidth; layoutGrid does the same for display:inline-grid. Adopt
	// the reported width (recomputing the border-box width) when the width is auto and the
	// natural content width is narrower than the resolved (container-fill) content width.
	if in.intrinsicWidth > 0 && (b.Formatting == cssbox.TableFC || b.Display == cssbox.DisplayInlineGrid) {
		if _, isAuto := resolveLen(b.Style.Width, b.Style.FontSizePt, cbWidth); isAuto && in.intrinsicWidth < contentW {
			contentW = in.intrinsicWidth
			borderW = contentW + ed.pL + ed.pR + ed.bL + ed.bR
		}
	}

	// Resolve the box's own collapse-resolved top/bottom margins and the offset of
	// the content-box top from the border-box top's "natural" position.
	//
	// Parent/first-child collapse-through: when this box has no top border and no
	// top padding, its top margin collapses with the first in-flow child's top
	// margin, and that child sits flush with the content-box top (its margin passes
	// through). Otherwise the child's top margin is laid inside the content box as a
	// leading gap. The bottom is symmetric, but only collapses through when the
	// box's height is auto (a fixed height interposes content between the last child
	// and the bottom edge).
	//
	// Implemented collapsing scope: adjacent siblings (layoutBlockChildren) plus
	// parent/first-child and parent/last-child through zero border/padding (here).
	// Out of scope this PR (no special handling): collapse-through of an empty
	// zero-height block's own top and bottom margins, clearance from floats, and the
	// min-height × collapse-through interaction.
	// A box that establishes a new block formatting context (an inline-block here)
	// does not collapse its margins with its in-flow children — its top/bottom
	// margins stay solid and the children's leading/trailing margins are interior.
	newBFC := establishesNewBFC(b)

	marginTop := ed.mT
	leadingGap := in.leadingMargin
	if !newBFC && ed.bT == 0 && ed.pT == 0 {
		marginTop = collapseMargins(ed.mT, in.leadingMargin)
		leadingGap = 0
	}

	contentH := in.contentHeight + leadingGap

	heightAuto := isHeightAuto(b)
	marginBottom := ed.mB
	if heightAuto {
		if !newBFC && ed.bB == 0 && ed.pB == 0 {
			marginBottom = collapseMargins(ed.mB, in.trailingMargin)
		} else {
			contentH += in.trailingMargin
		}
	} else {
		// A fixed height interrupts bottom collapse-through and overrides the
		// content-derived height; the trailing child margin is then interior space
		// already captured by the fixed box, so it does not extend the box.
		contentH = resolveFixedHeight(b, cbWidth, ed)
	}
	if contentH < 0 {
		contentH = 0
	}

	// Float-height enclosure (CSS 10.6.7): a box that establishes a BFC grows to enclose
	// its floats — its content height includes the bottom of the lowest float in its own
	// BFC. Guarded to an auto-height BFC: a fixed height is authoritative (and clips the
	// floats with overflow:hidden), and a non-BFC box does not enclose floats. in.floatsBottom
	// is 0 for a BFC box with no floats, so this is a no-op there.
	if newBFC && heightAuto && in.floatsBottom > contentH {
		contentH = in.floatsBottom
	}

	// min-height / max-height on an AUTO-height box. The fixed-height path already
	// clamps inside resolveFixedHeight, but that runs only when `height` is not auto,
	// so `max-height` alone never bounded anything: the box grew to fit its content and
	// the clip rect below (built from borderH) was never smaller than the content.
	// Applied after float enclosure so it clamps the final content height, and before
	// borderH so the clip rect, the fragment, and the parent's advance all agree.
	// Anonymous boxes are excluded: they carry a COPY of their parent's ComputedStyle
	// for inherited text properties, but CSS 9.2.1.1 gives them no properties of their
	// own, so applying the parent's max-height here would clamp a box the author never
	// sized — and clamp it once per anonymous wrapper in the chain.
	if heightAuto && !isAnonymous(b) {
		contentH = clampAutoHeight(b, contentH, cbWidth, ed)
	}

	borderH := contentH + ed.pT + ed.pB + ed.bT + ed.bB

	// Position: border-box top-left in page space.
	borderX := originX + ed.mL
	borderY := marginTopEdgeY + marginTop
	contentTopY := borderY + ed.bT + ed.pT

	// Shift the interior from its local content-top-0 frame into page space. The
	// leading gap (non-collapsing top margin) pushes content down inside the box.
	shiftFragments(in.children, contentTopY+leadingGap)
	shiftLines(in.lines, contentTopY+leadingGap)

	frag := &Fragment{
		X: borderX, Y: borderY, W: borderW, H: borderH,
		Background: b.Style.BackgroundColor,
		Children:   in.children,
		Lines:      in.lines,
		DebugTag:   debugTag(b),
	}
	frag.Box = b
	// CSS filter: parsed once, here, and carried on the fragment so the flatten
	// stage can bracket the box's subtree without re-parsing per page or per render
	// worker. nil (the common case) leaves every unfiltered document's item stream
	// byte-identical.
	frag.Filter, frag.FilterShadows = e.filterChain(b)
	// box-shadow: resolved here for the same reason, but carrying only the
	// shadow's PARAMETERS — the shadow box is derived from the fragment's own
	// geometry at flatten time, so a pagination shift moves it for free (see
	// Fragment.Shadows). nil for an unshadowed box leaves its item stream
	// byte-identical.
	frag.Shadows = e.boxShadows(b)
	frag.BgImages = e.resolveBackgroundImages(ctx, b, borderX, borderY, borderW, borderH, ed)
	if n := len(frag.BgImages); n > 0 {
		// BgImage aliases the TOPMOST layer (the last emitted), for consumers that
		// only understand one background.
		frag.BgImage = frag.BgImages[n-1]
	}
	if len(in.collapsedBorders) > 0 {
		// The collapsed grid strips were built from the interior's cell fragments —
		// page-space X but the local content-top-0 Y frame (like in.children). Shift
		// their Y into page space exactly as the children were shifted above; without
		// this the grid paints marginTop+borderTop+paddingTop too high (invisible only
		// when all three are zero — a table with margin-top drew its grid in the
		// margin band).
		frag.Collapsed = in.collapsedBorders
		for i := range frag.Collapsed {
			frag.Collapsed[i].YPt += contentTopY + leadingGap
		}
	}
	if in.headerBottom > 0 {
		// Same local-to-page-space Y shift as the collapsed strips above: the header
		// bottom was measured in the interior's content-top-0 frame.
		frag.HeaderBottom = in.headerBottom + contentTopY + leadingGap
	}
	frag.Border[layout.EdgeTop] = BorderEdge{Width: ed.bT, Color: b.Style.BorderTopColor, Style: mapBorderStyle(b.Style.BorderTopStyle)}
	frag.Border[layout.EdgeRight] = BorderEdge{Width: ed.bR, Color: b.Style.BorderRightColor, Style: mapBorderStyle(b.Style.BorderRightStyle)}
	frag.Border[layout.EdgeBottom] = BorderEdge{Width: ed.bB, Color: b.Style.BorderBottomColor, Style: mapBorderStyle(b.Style.BorderBottomStyle)}
	frag.Border[layout.EdgeLeft] = BorderEdge{Width: ed.bL, Color: b.Style.BorderLeftColor, Style: mapBorderStyle(b.Style.BorderLeftStyle)}
	frag.Radii = usedRadii(&b.Style, borderW, borderH)
	e.logRoundedBorderDegradation(frag)

	// overflow≠visible: this box clips its content to its padding box (the border box
	// deflated by the border widths). The stacking pass brackets the fragment's
	// contents with a clip; its own background/border paint unclipped.
	if clips(b) {
		frag.Clips = true
		frag.ClipRect = rect{
			x: borderX + ed.bL,
			y: borderY + ed.bT,
			w: borderW - ed.bL - ed.bR,
			h: borderH - ed.bT - ed.bB,
		}
		if frag.ClipRect.w < 0 {
			frag.ClipRect.w = 0
		}
		if frag.ClipRect.h < 0 {
			frag.ClipRect.h = 0
		}
		if b.Style.Overflow == "scroll" || b.Style.Overflow == "auto" {
			e.logf("css layout: overflow:%s clips like hidden (no scroll affordance in the single-tall-page model)", b.Style.Overflow)
		}
	}

	// A box that establishes its own BFC owns its floats' paint layer. Its floats were
	// placed in the interior's content-top-0 frame (childBand=0 in layoutInterior), so —
	// like the in-flow children above — they must be shifted down into page space by the
	// box's own content-box top (contentTopY = border-box top + top border + top padding).
	// Without this a float in a bordered/padded BFC painted at the BFC's BORDER-box top,
	// ignoring the box's own top border+padding (its X was already inset by placement, only
	// Y was short). The leading margin gap is NOT added here: it pushes in-flow content down,
	// but a float's margin box still touches the content-box top edge (CSS 9.5).
	if newBFC {
		frag.IsBFC = true
		shiftFragments(in.bfcFloats, contentTopY)
		frag.Floats = in.bfcFloats
	}

	// Positioning. A positioned box establishes a stacking context and is the nearest
	// stacking-context owner for the relative descendants its interior bubbled up: it
	// CONSUMES them onto its own Positioned layer. A static box does not own them — it
	// BUBBLES them up via blockResult.pendingPositioned so an ancestor stacking context
	// (ultimately the root, in layoutTree) consumes them.
	bubble := in.pendingPositioned
	if establishesStackingContext(b) {
		frag.IsStackingContext = true
		for _, pp := range in.pendingPositioned {
			// Sub-case B: when b CLIPS (overflow≠visible), every relative descendant it
			// consumes is clipped to b's padding box — it paints INSIDE the box's own
			// ClipPush/ClipPop bracket (CBOwned). This is correct for ALL consumed relatives,
			// not just direct children: a relative descendant only reaches this consume list
			// if no positioned box sits between it and b (any such box, being a stacking
			// context, would have consumed it first), so b IS its nearest positioned ancestor
			// and its painting bubbles up to b's layer as in-flow content b must clip. A
			// relative descendant that bubbled through an intervening NON-positioned clip box
			// also carries that box's rect in pp.clipChain, so both clips apply. When b does
			// not clip, this reduces to false as before (byte-identical).
			frag.PositionedInfo = append(frag.PositionedInfo, PositionedInfo{CBOwned: frag.Clips, ClipChain: pp.clipChain})
			frag.Positioned = append(frag.Positioned, pp.frag)
		}
		bubble = nil
		// Back-fill the CB owner of any abs/fixed descendant this box collected: their
		// containing block is THIS box's content box, but its frag did not exist when
		// they were recorded (only {box: b} was captured). Wire them to frag now so the
		// abs-pos pass deflates the right (final) border box. (fixed descendants carry
		// the page sentinel and box==nil, so they are not matched here.)
		for j := deferredBefore; j < len(posCtx.deferred); j++ {
			if posCtx.deferred[j].cb.box == b && posCtx.deferred[j].cb.frag == nil {
				posCtx.deferred[j].cb.frag = frag
			}
		}
	} else if frag.Clips {
		// b is a NON-positioned (not a stacking context) overflow≠visible box: a relative
		// descendant bubbling past it is in-flow content of b, so b's overflow clips it
		// even though it paints in an ancestor's positioned layer. Prepend b's clip rect
		// (already computed above as frag.ClipRect) to each still-bubbling entry's chain,
		// so the outermost box ends up first as the entry rises. (This is the in-flow
		// relative-clip-escape fix. The abs/fixed analogue is NOT a gap: an abs box whose
		// CB is an ancestor outside b is correctly NOT clipped by b — CSS 2.1 §11.1.1
		// excepts a descendant whose containing block is an ancestor of the clipping box.
		// When an overflow box DOES clip an abs descendant, that descendant's CB is the box
		// or a descendant of it, so the descendant is already clipped — either by riding
		// that CB's own ClipPush/ClipPop bracket, or, when an intervening non-positioned
		// clip box like b sits above the CB, by b's rect prepended onto the CB's bubble
		// chain. So no separate abs clip-chain threading is needed.)
		grown := make([]pendingPos, len(bubble))
		for i, pp := range bubble {
			grown[i] = pendingPos{frag: pp.frag, clipChain: prependRect(frag.ClipRect, pp.clipChain)}
		}
		bubble = grown
	}

	return blockResult{frag: frag, marginTop: marginTop, marginBottom: marginBottom, pendingPositioned: bubble}
}

// prependRect returns a new slice with r at the front of chain (outermost-first order).
// A fresh slice is returned so sibling entries do not alias one backing array.
func prependRect(r rect, chain []rect) []rect {
	out := make([]rect, 0, len(chain)+1)
	out = append(out, r)
	out = append(out, chain...)
	return out
}

// translateRects returns a copy of chain with every rect's origin shifted by (dx,dy);
// the width/height are unchanged. A nil/empty chain returns nil (no allocation). Used
// to move a clip chain captured in a float's pre-translation local frame into the
// float's final placed frame (placeFloat). A fresh slice is returned so the result does
// not alias the source backing array.
func translateRects(chain []rect, dx, dy float64) []rect {
	if len(chain) == 0 {
		return nil
	}
	out := make([]rect, len(chain))
	for i, r := range chain {
		out[i] = rect{x: r.x + dx, y: r.y + dy, w: r.w, h: r.h}
	}
	return out
}

// interior is the laid-out content of a block box in a local frame whose
// content-box top is at local Y 0: positioned child fragments (or inline lines)
// plus the margin-collapsing facts the parent needs. contentHeight is the local Y
// of the bottom of the in-flow content, excluding the collapsible leading and
// trailing child margins (which the parent decides whether to render or collapse).
type interior struct {
	children       []*Fragment
	lines          []LineFragment
	contentHeight  float64
	leadingMargin  float64     // first in-flow child's top margin (block flow only)
	trailingMargin float64     // last in-flow child's bottom margin (block flow only)
	bfcFloats      []*Fragment // floats placed in this box's OWN BFC (set only when b establishes one)
	// floatsBottom is the bottom of the lowest float placed in this box's OWN BFC (set
	// only when b establishes one), in the box's local content-top-0 frame. layoutBlock
	// folds it into the content height so a BFC box encloses its floats (CSS 10.6.7).
	floatsBottom float64
	// pendingPositioned holds relative-positioned descendants laid out in this box's
	// interior that have not yet found their stacking-context owner (bubbles up like
	// bfcFloats, but for the positioned layer). layoutBlock consumes or re-bubbles it.
	pendingPositioned []pendingPos
	// collapsedBorders holds the resolved border-collapse:collapse edge strips for a
	// table interior, to be copied onto the table fragment. Nil for every non-table
	// interior so non-collapse pages are byte-identical.
	collapsedBorders []layout.BorderItem
	// headerBottom is the bottom Y (in the interior's local frame) of a table's
	// repeatable <thead> rows, or 0 when the box is not a table or has no header.
	// layoutBlock copies it onto the fragment so pagination can repeat the header.
	headerBottom float64
	// intrinsicWidth is a table's grid-content width (Σ column widths + border-spacing),
	// reported by layoutTable so layoutBlock can shrink a width:auto table box to wrap
	// its grid (CSS tables are shrink-to-fit, §17.5.2). Zero for non-table interiors
	// (which keep the resolved block content width).
	intrinsicWidth float64
}

// layoutInterior lays out b's children into a local frame (content-box top at 0)
// according to b's formatting context. contentW is the content width children flow
// into; contentX is the page-space x of the content box's left edge (children that
// establish their own page-space position, i.e. blocks, use it directly).
//
// bandOriginY and fc thread the float context: bandOriginY is this box's content-box top in the BFC-root frame, and fc is the BFC's float context (a fresh one is created here if b establishes its own BFC).
//
// posCtx and posCB thread positioning (see layoutBlock): posCtx collects abs/fixed
// descendants, posCB names the abs-pos containing-block owner. They pass straight
// through to layoutBlockChildren (block flow); the InlineFC case does not collect
// positioned descendants (relative inline atoms / abs descendants of an IFC are out of
// scope this slice), so its interior carries no pendingPositioned.
func (e *Engine) layoutInterior(ctx context.Context, b *cssbox.Box, contentW, contentX, bandOriginY float64, fc *floatContext, posCtx *positionedContext, posCB posCBOwner) interior {
	// A box that establishes a new BFC (inline-block today) isolates floats: its
	// interior gets a fresh context spanning its own content box, and its own band
	// frame (origin 0). Otherwise children share the parent's context and frame, so a
	// float placed by a child is visible to its siblings and the band Y stays in the
	// ancestor BFC-root frame.
	childFC, childBand := fc, bandOriginY
	if establishesNewBFC(b) {
		childFC = &floatContext{cbLeft: contentX, cbRight: contentX + contentW}
		childBand = 0
	}

	// A vertical writing-mode lays a single line out down the page (layoutInlineVertical).
	// The parts of the inline model that phase does not cover — wrapping, atomics, hard
	// breaks, decorations — report themselves from there, at the point where the
	// unsupported case actually occurs, rather than as one blanket warning here that
	// would now contradict the rendering.
	//
	// vertical-lr is accepted alongside vertical-rl and lays its line out identically:
	// the two differ only in which side SUBSEQUENT lines stack from, and only one line is
	// produced. Reported so the two are not silently equated.
	if b.Style.WritingMode == "vertical-lr" {
		e.warnOnce("css-writing-mode-vertical-lr-stacking",
			"css layout: writing-mode=vertical-lr lays out as vertical-rl; multi-line stacking direction is not implemented")
	}

	var in interior
	switch b.Formatting {
	case cssbox.InlineFC:
		// Inline-level children: hand off to the inline formatting context. The hook
		// returns line fragments already positioned in page-space X (at contentX) but
		// in the local content-top-0 frame for Y; block layout shifts them into place.
		// Any atomic inline boxes (inline-block / replaced) come back as child
		// fragments in the same frame, to attach as fragment children so they paint.
		lines, h, atomics := e.layoutInline(ctx, b, contentW, 0, contentX, childBand, childFC)
		in = interior{lines: lines, children: atomics, contentHeight: h}
	case cssbox.BlockFC:
		in = e.layoutBlockChildren(ctx, b, contentW, contentX, childBand, childFC, posCtx, posCB)
	case cssbox.TableFC:
		in = e.layoutTable(ctx, b, contentW, contentX, childBand, childFC)
	case cssbox.FlexFC:
		in = e.layoutFlex(ctx, b, contentW, contentX, childBand, childFC, posCtx, posCB)
	case cssbox.GridFC:
		in = e.layoutGrid(ctx, b, contentW, contentX, childBand, childFC)
	default:
		// Any remaining unimplemented formatting context: degrade to block normal flow
		// so children still position and paint.
		e.logf("css layout: %v not yet implemented; falling back to block normal flow", b.Formatting)
		in = e.layoutBlockChildren(ctx, b, contentW, contentX, childBand, childFC, posCtx, posCB)
	}

	// A new BFC's floats are self-contained: surface them so layoutBlock attaches them
	// to b's own fragment (the float paint layer for b's BFC).
	if establishesNewBFC(b) {
		in.bfcFloats = childFC.floats2frags()
		in.floatsBottom = childFC.maxBottom()
	}
	return in
}

// layoutBlockChildren stacks b's block-level children vertically in a local frame
// whose content-box top is local Y 0, collapsing margins between adjacent siblings.
// The first child's border top sits at local Y 0; its top margin is reported as
// leadingMargin for the parent to collapse or render. Each subsequent child's
// border top sits at the previous child's border bottom plus their collapsed
// margin. The last child's bottom margin is reported as trailingMargin.
//
// Each child is laid out at a provisional origin and then shifted into place using
// its own *reported* top margin (which already folds in any nested
// parent/first-child collapse-through), so the gap arithmetic stays correct for
// deeply collapsing subtrees, not just leaf children.
//
// Coordinate-frame model (the load-bearing detail): the float context is queried
// in ONE frame per BFC — the BFC-root-local frame, whose Y origin is the BFC root's
// content-box top (page Y 0 for the page root). Every place/leftEdge/rightEdge/
// clearY call passes bandOriginY + <local Y>, where bandOriginY is the current
// box's content-top in that frame and <local Y> is the local content-top-0 cursor.
// In-flow fragments are still built in their own local frame and shifted into place
// by the existing per-child shift; float fragments are built directly in the
// BFC-root frame (they attach to the BFC root's Floats, not to a shifted child) —
// see placeFloat. A nested BFC resets bandOriginY to 0 and uses its own context
// (layoutInterior).
func (e *Engine) layoutBlockChildren(ctx context.Context, b *cssbox.Box, contentW, contentX, bandOriginY float64, fc *floatContext, posCtx *positionedContext, posCB posCBOwner) interior {
	var (
		out               []*Fragment
		pendingPositioned []pendingPos // relative descendants bubbling to the nearest stacking-context ancestor
		prevBottom        float64      // previous in-flow sibling's reported bottom margin
		prevBorder        float64      // previous in-flow sibling's border-box bottom (local Y)
		leading           float64
		trailing          float64
		first             = true
		cursorY           float64 // local content-top-0 Y of the in-flow cursor
	)
	for _, child := range b.Children {
		if err := ctx.Err(); err != nil {
			// Cancellation: stop adding children but return what we have (the caller
			// is the Layout recover/return path; we degrade rather than propagate
			// here to keep partial output renderable).
			break
		}
		if !child.Kind.IsBlockLevel() && !isBlockLevelReplaced(child) {
			// The box-gen invariant guarantees a block container's children are all
			// block-level or all inline-level, so a stray inline here is unexpected;
			// skip it defensively rather than misplacing it. The replaced exception:
			// a block-level replaced box — e.g. <img style="display:block"> — has Kind
			// BoxReplaced (inline by kind) yet participates as a block here, and
			// layoutBlock dispatches it to layoutBlockReplaced. (Kind.IsBlockLevel
			// already accepts an inline-block child's BoxBlock kind for callers that
			// lay an inline-block's own children out via this path.)
			e.logf("css layout: unexpected inline-level child in block formatting context; skipping")
			continue
		}

		// Absolutely/fixed-positioned child: out of flow. Collect for pass 2. It does
		// not advance the cursor, collapse margins, or join Children. (box-gen forces an
		// abs/fixed box's Float to none, so this precedes — and excludes it from — the
		// float branch below.)
		if child.Position == cssbox.PosAbsolute || child.Position == cssbox.PosFixed {
			cb := posCB
			if child.Position == cssbox.PosFixed {
				cb = posCBOwner{isPage: true} // fixed: the page (viewport)
			}
			posCtx.deferred = append(posCtx.deferred, deferredAbs{box: child, cb: cb})
			continue // no SC owner stored; pass 2 derives it from cb
		}

		if child.Float != cssbox.FloatNone {
			// Place the float in the BFC-root frame at the current in-flow band. Its top
			// may not rise above the previous in-flow sibling's MARGIN-box bottom (CSS 9.5:
			// "the outer top of a floating box may not be higher than the outer top of any
			// block ... earlier in the source"). cursorY sits at the previous block's
			// border-box bottom (prevBorder); the previous block's bottom margin (prevBottom)
			// is still pending — a float does not collapse with it, but it does sit below it,
			// so advance the float's top past it. A float still does not advance the in-flow
			// cursor or collapse margins for the NEXT block.
			floatTop := cursorY
			if !first {
				if mb := prevBorder + prevBottom; mb > floatTop {
					floatTop = mb
				}
			}
			e.placeFloat(ctx, child, contentW, contentX, bandOriginY+floatTop, fc, posCtx, posCB)
			continue
		}

		// clear: lower the cursor below the matching floats. clearY is in the BFC-root
		// frame; convert to local by subtracting bandOriginY.
		startY := cursorY
		if child.Style.Clear != "" && child.Style.Clear != "none" {
			if cy := fc.clearY(child.Style.Clear, bandOriginY+cursorY) - bandOriginY; cy > startY {
				startY = cy
			}
		}

		// Sibling-BFC float avoidance (CSS 9.5): a child that establishes its own BFC
		// must not overlap an outer float in the PARENT's BFC — its whole border box
		// sits beside the float (unlike a normal block, whose border box slides under
		// the float while only its inline content narrows). Narrow + shift the child to
		// the float band at its Y; if it cannot fit, drop it below the float band.
		childOriginX := contentX
		childWidth := contentW
		if establishesNewBFC(child) {
			h := bfcChildBandHeight(child, contentW)
			for {
				left := fc.leftEdge(bandOriginY+startY, h)
				right := fc.rightEdge(bandOriginY+startY, h)
				avail := right - left
				if left <= contentX+1e-6 && right >= contentX+contentW-1e-6 {
					break // no float intrudes at this band: full width, no shift
				}
				if avail >= childWidthFor(child, contentW)+1e-6 || avail >= contentW-1e-6 {
					childOriginX = left
					childWidth = right - left
					break // fits beside the float
				}
				next := fc.nextDropY(bandOriginY+startY, h) - bandOriginY
				if next <= startY {
					// No lower opportunity: lay out beside at the narrowed band (overflow
					// allowed) rather than spinning.
					childOriginX = left
					childWidth = right - left
					e.logf("css layout: overflow≠visible box cannot fit beside a float; placed at the narrowed band")
					break
				}
				startY = next // drop below the float and retry at full width there
			}
		}

		// Lay the child out at a provisional margin edge of 0; its border top then
		// sits at res.marginTop, which we now know and use to place it exactly. Pass
		// bandOriginY+startY as the child's band origin so a nested in-flow block
		// knows its position in the BFC-root frame (its IFC queries floats there).
		//
		// floatCountBefore snapshots this BFC's float list so we can retroactively
		// correct floats this child placed into it once the child's true border top is
		// known (see below). (Floats a nested-BFC descendant placed go to a DIFFERENT
		// context, ride res.frag's shift, and are not in fc — so they are untouched.)
		floatCountBefore := len(fc.floats)
		res := e.layoutBlock(ctx, child, childWidth, childOriginX, 0, bandOriginY+startY, fc, posCtx, posCB)

		var borderTop float64 // desired local Y of this child's border-box top
		if first {
			borderTop = startY // first child's border top defines the content-box top
			leading = res.marginTop
			first = false
		} else {
			borderTop = prevBorder + collapseMargins(prevBottom, res.marginTop)
			if startY > borderTop {
				borderTop = startY // clearance pushed it down past the collapsed margin
			}
		}
		e.warnCollapseThrough(child, res)
		// The child currently sits with its border top at res.marginTop (margin edge
		// was 0); shift it so its border top lands at borderTop.
		shiftFragment(res.frag, borderTop-res.marginTop)

		// Correct any float this child placed DIRECTLY into this BFC. The child laid its
		// floats out against a PROVISIONAL band origin (bandOriginY+startY+ed.mT), because
		// its real border top was not yet known; the child's in-flow content was then
		// shifted down to borderTop by the line above. A float attaches to the BFC root's
		// float list (not to res.frag), so that shift did NOT move it — leaving it too high
		// by exactly the gap the collapsed margin / clearance opened between the provisional
		// start+margin and the resolved border top. The correction that lands the float's
		// band origin on the child's true content-box top is borderTop-(startY+ed.mT); apply
		// it to both the geometry (so later floats avoid the corrected box) and the fragment.
		// (=0 for the first child and for a cleared child, where borderTop==startY and mT is
		// absorbed — so the common no-collapse case is untouched / byte-identical.)
		if len(fc.floats) > floatCountBefore {
			// childWidth (not contentW) is the width layoutBlock resolved child's ed.mT
			// against — they differ only for a float-narrowed BFC child, whose floats do
			// NOT enter this fc, so this branch only sees non-BFC children where the two are
			// equal; using childWidth keeps the margin basis exactly consistent regardless.
			childMT := usedEdges(child, childWidth).mT
			if adj := borderTop - startY - childMT; adj != 0 {
				fc.shiftFloatsFrom(floatCountBefore, adj)
			}
		}
		out = append(out, res.frag)

		// A relative child stays in flow (its space is reserved above) but is flagged
		// positioned and carries a paint-time offset; it bubbles to the nearest
		// stacking-context ancestor (which lifts it into the positioned layer). cbH is
		// passed 0: px/em top/bottom offsets ignore it, and a % top/bottom degrades to 0
		// against an as-yet-unknown containing-block height (documented deferral).
		if child.Position == cssbox.PosRelative {
			dx, dy := relativeOffset(child, contentW, 0)
			res.frag.IsPositioned = true
			res.frag.IsStackingContext = true
			res.frag.RelOffsetX, res.frag.RelOffsetY = dx, dy
			pendingPositioned = append(pendingPositioned, pendingPos{frag: res.frag})
		}
		// Bubble up any relative descendants the child did not consume (already carrying
		// their own clip chains from deeper clipping boxes).
		pendingPositioned = append(pendingPositioned, res.pendingPositioned...)

		prevBorder = res.frag.Y + res.frag.H
		prevBottom = res.marginBottom
		trailing = res.marginBottom
		cursorY = prevBorder
	}
	return interior{children: out, contentHeight: prevBorder, leadingMargin: leading, trailingMargin: trailing, pendingPositioned: pendingPositioned}
}

// placeFloat lays out a floated child and places it in the float context at
// placeY (the in-flow band's Y in the BFC-root-local frame). The child is laid out
// to learn its size, expanded to its margin box, placed via fc.place, and its
// fragment translated to the placed margin box's border-box origin — directly in the
// BFC-root frame, because a float attaches to the BFC root's Floats (the float paint
// layer), not to a shifted in-flow child. The fragment is marked IsFloat and recorded
// on the just-appended floatBox so the BFC owner (layoutTree / layoutInterior) can
// collect it via floats2frags.
func (e *Engine) placeFloat(ctx context.Context, child *cssbox.Box, cbWidth, contentX, placeY float64, fc *floatContext, posCtx *positionedContext, posCB posCBOwner) {
	// Lay the float out (provisional origin) to learn its border-box size. A float is
	// block-level and establishes its own BFC for its contents, so layoutInterior
	// gives it a fresh float context (it does not inherit this BFC's floats). placeY is
	// passed as bandOriginY for the float's own margin-box arithmetic; its interior
	// resets to its own frame (bandOriginY=0) in layoutInterior. posCtx/posCB thread
	// positioning: a float establishes its own BFC, so abs descendants whose CB is the
	// float resolve against it; a positioned float is their CB owner (set inside
	// layoutBlock). The posCB passed in is the float's own enclosing CB, used for abs
	// descendants that escape to an ancestor (the float is not their CB unless it is
	// itself positioned, which layoutBlock handles).
	res := e.layoutBlock(ctx, child, cbWidth, contentX, 0, placeY, fc, posCtx, posCB)
	if res.frag == nil {
		return
	}
	ed := usedEdges(child, cbWidth)
	marginW := ed.mL + res.frag.W + ed.mR
	marginH := res.marginTop + res.frag.H + res.marginBottom

	// The float sits within its containing block's content box (contentX ..
	// contentX+cbWidth), which may be inset from the BFC's context edges when a
	// non-BFC ancestor (e.g. a padded <body>/<section>) lies between: pass those
	// edges so a left float honors the containing block's left padding/border.
	fb := fc.place(child.Float, marginW, marginH, placeY, contentX, contentX+cbWidth)

	// fb.x/fb.y is the float's MARGIN-box top-left in the BFC-root frame. The border
	// box sits inside it by the left/top margins. Translate the provisional fragment
	// there (X and Y both move; a float's position is absolute in the BFC frame).
	dx := (fb.x + ed.mL) - res.frag.X
	dy := (fb.y + res.marginTop) - res.frag.Y
	translateFragment(res.frag, dx, dy)
	res.frag.IsFloat = true

	// A relatively-positioned descendant of the float is IN FLOW (a normal entry in
	// res.frag.Children), so translateFragment(res.frag, …) above ALREADY moved it by
	// the placement delta — it must NOT be translated again. We only GATHER any relatives
	// that bubbled up (the float is static, so its layoutBlock did not consume them onto
	// frag.Positioned) onto the float's own Positioned layer, so the float's AppendItems
	// paints them in its positioned phase. Relatives the float already consumed (when the
	// float is itself a stacking context) are on res.frag.Positioned and stay there —
	// also already moved by translateFragment. (Abs/fixed descendants are deferred to
	// pass 2 and are not present on the fragment here.)
	for _, pp := range res.pendingPositioned {
		// A clip chain captured inside the float holds rects in the float's
		// pre-translation local frame; translateFragment moved the float subtree by
		// (dx,dy) but not these rects (they ride the pending entry, not the fragment), so
		// re-translate them by the same delta so the clip brackets the descendant at the
		// float's FINAL placed position.
		res.frag.PositionedInfo = append(res.frag.PositionedInfo, PositionedInfo{CBOwned: false, ClipChain: translateRects(pp.clipChain, dx, dy)})
		res.frag.Positioned = append(res.frag.Positioned, pp.frag)
	}

	// A relatively-positioned float is placed at the float edge AND offset at paint
	// time: stamp the offset on the float fragment so the Floats-layer translate
	// (AppendItems) shifts its emitted item range. It is NOT additionally added to a
	// Positioned slice — it paints via the Floats layer. cbH ~0 (px/em offsets ignore
	// it; a % top/bottom degrades to 0).
	if child.Position == cssbox.PosRelative {
		rdx, rdy := relativeOffset(child, cbWidth, 0)
		res.frag.IsPositioned = true
		res.frag.RelOffsetX, res.frag.RelOffsetY = rdx, rdy
	}

	// Record the fragment on the just-appended floatBox (fc.place appended it last).
	fc.floats[len(fc.floats)-1].frag = res.frag
}

// resolveAbsolute lays out and positions every deferred absolutely/fixed-positioned
// box (pass 2), now that the page and all ancestor fragments are final. For each: it
// resolves the containing-block CONTENT rect from the captured owner, lays out the
// box's own subtree as an independent block (its own fresh floatContext; the SAME
// posCtx so nested abs-pos descendants are appended to this loop and resolved
// transitively), computes the used border-box rect via absRect, translates the
// fragment there, marks it positioned + stacking context, and appends it to the
// owning stacking context's Positioned layer.
// consumePendingPositioned attaches relative-positioned descendants that bubbled out
// of frag's interior onto frag's own Positioned layer. A flex item, grid item, and
// table cell each lay their contents out as an isolated BFC paint atom but are not
// themselves stacking contexts, so a static item never consumes its relative
// descendants in layoutBlock — they bubble up in blockResult.pendingPositioned. The
// container's item layout has nowhere to bubble them further (the item fragment is
// flattened atomically), so it consumes them here, mirroring how layoutTree consumes
// the root's leftover bubble. Without this, a position:relative box inside a flex/grid/
// table item — and any abs/fixed descendant riding on it — is never emitted at paint
// time (it stays an IsPositioned child, skipped by every in-flow pass). frag must be
// marked a BFC by the caller so AppendItems emits the layer.
func consumePendingPositioned(frag *Fragment, pending []pendingPos) {
	if frag == nil {
		return
	}
	for _, pp := range pending {
		frag.PositionedInfo = append(frag.PositionedInfo, PositionedInfo{CBOwned: false, ClipChain: pp.clipChain})
		frag.Positioned = append(frag.Positioned, pp.frag)
	}
}

func (e *Engine) resolveAbsolute(ctx context.Context, posCtx *positionedContext, root *Fragment, viewportW, pageH float64) {
	pageRect := rect{x: 0, y: 0, w: viewportW, h: pageH}
	// Iterate by index: laying out a deferred box may APPEND more deferred boxes to
	// posCtx.deferred (a transitive abs-pos descendant collected by its layoutBlock),
	// which this same loop then resolves. The index walk (not range) picks them up.
	for i := 0; i < len(posCtx.deferred); i++ {
		d := posCtx.deferred[i]
		cb := e.resolveCBRect(d.cb, pageRect)
		ed := usedEdges(d.box, cb.w)

		// CSS 10.3.7 shrink-to-fit: a width:auto abs box that is NOT pinned by both left+
		// right (and is not replaced) sizes to min(max(min-content, available), max-content)
		// rather than filling the CB. Compute it up front so BOTH the placement (absRect's
		// auto-width fallback) and the interior layout width use it consistently — important
		// for a right-anchored box whose left edge depends on the resolved width. autoFillW
		// is the content-width fallback absRect uses when width is auto; default is the
		// CB-fill (cb.w - margins - insets).
		autoFillW := cb.w - ed.mL - ed.mR - ed.bL - ed.bR - ed.pL - ed.pR
		if autoFillW < 0 {
			autoFillW = 0
		}
		if e.absWidthShrinksToFit(d.box) {
			autoFillW = e.absShrinkToFitWidth(ctx, d.box, autoFillW)
		}
		border, contentW := absRect(d.box, ed, cb, autoFillW)

		// The width layoutBlock should flow the interior into. Normally the box's own
		// width resolution (containing-block fill for auto, or the explicit width) is
		// correct, so we lay out against the full CB width (cb.w). But when the width is
		// constrained — both left+right pinning a width:auto (shrink-to-offsets), or a
		// shrink-to-fit auto width — absRect's contentW is the used width; feed layoutBlock
		// a containing width whose auto-fill reproduces that content width
		// (cbWidth - margins - insets == contentW), so the interior flows at the
		// constrained width, not the full CB width.
		layoutCBWidth := cb.w
		if absWidthIsOffsetConstrained(d.box) || e.absWidthShrinksToFit(d.box) {
			layoutCBWidth = contentW + ed.mL + ed.mR + ed.bL + ed.bR + ed.pL + ed.pR
		}

		// Lay out the box's subtree as a block at that width, at a provisional origin
		// (originX = cb.x, marginTopEdgeY = cb.y, bandOriginY 0), with a FRESH
		// floatContext (its floats are self-contained) and the SAME posCtx (so its own
		// abs-pos descendants are collected for this loop). Its posCB owner is the box
		// ITSELF (it is a positioned ancestor / new CB); its own layoutBlock sets
		// IsStackingContext + consumes its interior's pending relatives onto
		// frag.Positioned (the same consume-or-bubble as pass 1). The frag is not built
		// before layoutBlock returns, so any nested abs descendant recorded {box: d.box}
		// is back-filled with the built frag just below.
		childFC := &floatContext{cbLeft: cb.x, cbRight: cb.x + layoutCBWidth}
		before := len(posCtx.deferred)
		res := e.layoutBlock(ctx, d.box, layoutCBWidth, cb.x, cb.y, 0, childFC, posCtx, posCBOwner{box: d.box})
		frag := res.frag
		if frag == nil {
			continue
		}
		// Back-fill nested abs descendants' CB owner to this just-built frag (their CB
		// is d.box's content box; they were recorded with cb.box == d.box, frag nil).
		for j := before; j < len(posCtx.deferred); j++ {
			if posCtx.deferred[j].cb.box == d.box && posCtx.deferred[j].cb.frag == nil {
				posCtx.deferred[j].cb.frag = frag
			}
		}

		// Move the provisional fragment to its resolved border-box origin. (absRect's
		// provisional height stands for a fixed/derived height; for a top-only auto
		// height the laid-out fragment's own content-derived H is authoritative and is
		// preserved by the translate, which only moves the origin. A bottom-only
		// auto-height box is positioned against a provisional zero content height — a
		// documented deferral.)
		translateFragment(frag, border.x-frag.X, border.y-frag.Y)
		// height:auto with BOTH top and bottom specified: the used height is the space
		// between them (CSS 10.6.4), not the content height. absRect already resolved
		// it; the fragment keeps its own content-derived H otherwise, so this is the one
		// case where the resolved height must be written back.
		//
		// Without it the box collapsed to its content height — zero for an empty one —
		// so `top: 0; bottom: 20px`, the ordinary way to span a container vertically,
		// painted nothing at all. The horizontal equivalent (left+right) already worked,
		// which is what made the asymmetry surprising.
		fsBox := d.box.Style.FontSizePt
		if isHeightAuto(d.box) && !isAuto2(d.box.Style.Top, fsBox) && !isAuto2(d.box.Style.Bottom, fsBox) {
			setFragmentHeight(frag, border.h)
		}
		if isHeightAuto(d.box) && isAuto2(d.box.Style.Top, fsBox) && !isAuto2(d.box.Style.Bottom, fsBox) {
			e.logf("css layout: abs-pos bottom-only auto-height box positioned against a provisional height (approximate)")
		}
		fs := d.box.Style.FontSizePt
		if isAuto2(d.box.Style.Left, fs) && isAuto2(d.box.Style.Right, fs) {
			e.logf("css layout: abs-pos box with no horizontal offset placed at its containing block's left (static-position approximation)")
		}
		frag.IsPositioned = true
		frag.IsStackingContext = true
		frag.RelOffsetX, frag.RelOffsetY = 0, 0 // abs/fixed bake position into coords

		// Attach to the owning stacking context's Positioned layer: the root for a page
		// CB, else the nearest-positioned-ancestor fragment (itself a stacking context).
		owner := root
		if !d.cb.isPage && d.cb.frag != nil {
			owner = d.cb.frag
		}
		if owner != nil {
			cbOwned := !d.cb.isPage && d.cb.frag != nil && owner == d.cb.frag
			owner.PositionedInfo = append(owner.PositionedInfo, PositionedInfo{CBOwned: cbOwned})
			owner.Positioned = append(owner.Positioned, frag)
		}
	}
}

// resolveCBRect turns a captured posCBOwner into the containing-block CONTENT rect in
// final page coordinates: the page rect for the page sentinel, else the ancestor
// fragment's final border box deflated by the ancestor box's border+padding. (The
// usedEdges percentage basis is the ancestor's border-box width — approximate for %
// border/padding, which are rare.)
func (e *Engine) resolveCBRect(o posCBOwner, pageRect rect) rect {
	if o.isPage || o.frag == nil {
		return pageRect
	}
	ed := usedEdges(o.box, o.frag.W)
	return rect{
		x: o.frag.X + ed.bL + ed.pL,
		y: o.frag.Y + ed.bT + ed.pT,
		w: o.frag.W - ed.bL - ed.bR - ed.pL - ed.pR,
		h: o.frag.H - ed.bT - ed.bB - ed.pT - ed.pB,
	}
}

// The inline formatting context (layoutInline) lives in inline.go; it is the hook
// the InlineFC case of layoutInterior calls to lay out a block's inline-level
// children into LineFragments and atomic child fragments.

// --- box-model helpers (the inline formatting context reuses resolveLen and
// usedEdges) ---

// edges holds a box's used (resolved, content-box-relative) margin, border, and
// padding widths for all four sides, in points.
type edges struct {
	mT, mR, mB, mL float64
	bT, bR, bB, bL float64
	pT, pR, pB, pL float64
}

// resolveAutoMarginsX distributes the leftover inline space of a normal-flow block
// box to its `auto` horizontal margins, per CSS 2.1 §10.3.3, mutating ed in place.
// This is what makes the `margin: 0 auto` centering idiom work.
//
// The constraint is cbWidth = mL + bL + pL + contentW + pR + bR + mR, where borderW
// already carries everything between the margins. leftover is what the margins must
// absorb between them:
//
//   - both margins auto  → split evenly (centred). The canonical case.
//   - one margin auto    → that margin takes the whole leftover, pushing the box to
//     the opposite edge.
//   - neither auto       → nothing to do; an over-constrained box keeps its specified
//     margins and simply overflows, which matches the engine's existing
//     start-edge-wins behaviour elsewhere.
//
// A NEGATIVE leftover means the box is wider than its containing block. CSS 10.3.3
// treats an auto margin as 0 in that case rather than pulling the box backwards, so
// leftover is floored at 0 — otherwise `margin: 0 auto` on an over-wide box would
// shift it LEFT of its container, which no browser does.
//
// Only the horizontal axis is handled. A vertical `auto` margin computes to 0 in
// normal flow per CSS 10.6.3 (vertical centering via auto margins is a flex/grid
// feature, and those paths do their own resolution), so usedEdges' zero is already
// correct there.
func resolveAutoMarginsX(b *cssbox.Box, cbWidth, borderW float64, ed *edges) {
	fs := b.Style.FontSizePt
	mLAuto := isAuto2(b.Style.MarginLeft, fs)
	mRAuto := isAuto2(b.Style.MarginRight, fs)
	if !mLAuto && !mRAuto {
		return
	}
	// An anonymous box has no specified margins to be auto, and its width already
	// fills the container; leave it alone rather than inventing a shift.
	if isAnonymous(b) {
		return
	}
	leftover := cbWidth - borderW - ed.mL - ed.mR
	if leftover <= 0 {
		return // over-constrained or exactly filled: auto margins stay 0
	}
	switch {
	case mLAuto && mRAuto:
		ed.mL += leftover / 2
		ed.mR += leftover / 2
	case mLAuto:
		ed.mL += leftover
	default:
		ed.mR += leftover
	}
}

// usedEdges resolves the margins, border widths, and paddings of box b against a
// containing block of width cbWidth. Percentages on every edge resolve against
// cbWidth (CSS resolves vertical padding/margin percentages against the containing
// block's width too). Padding and border widths clamp to non-negative; margins may
// be negative. A border edge's used width is zero unless its style draws (a
// border-style of none/"" yields zero used width regardless of the declared width).
// Auto margins resolve to 0 HERE; horizontal `auto` margins are then given their
// share of the leftover space by resolveAutoMarginsX, once the used width is known
// (CSS 10.3.3). Callers that only want intrinsic or float sizing — where an auto
// margin genuinely contributes nothing — use this result directly and are unaffected.
func usedEdges(b *cssbox.Box, cbWidth float64) edges {
	fs := b.Style.FontSizePt
	margin := func(l gcss.Length) float64 {
		// Auto margins -> 0 here; see resolveAutoMarginsX. Others may be negative.
		v, isAuto := resolveLen(l, fs, cbWidth)
		if isAuto {
			return 0
		}
		return v
	}
	pad := func(l gcss.Length) float64 {
		v, _ := resolveLen(l, fs, cbWidth)
		if v < 0 {
			v = 0
		}
		return v
	}
	border := func(width gcss.Length, style string) float64 {
		if mapBorderStyle(style) == layout.BorderNone {
			return 0
		}
		v, _ := resolveLen(width, fs, cbWidth)
		if v < 0 {
			v = 0
		}
		return v
	}
	s := &b.Style
	return edges{
		mT: margin(s.MarginTop), mR: margin(s.MarginRight), mB: margin(s.MarginBottom), mL: margin(s.MarginLeft),
		pT: pad(s.PaddingTop), pR: pad(s.PaddingRight), pB: pad(s.PaddingBottom), pL: pad(s.PaddingLeft),
		bT: border(s.BorderTopWidth, s.BorderTopStyle),
		bR: border(s.BorderRightWidth, s.BorderRightStyle),
		bB: border(s.BorderBottomWidth, s.BorderBottomStyle),
		bL: border(s.BorderLeftWidth, s.BorderLeftStyle),
	}
}

// usedRadii resolves a box's four border-radius corners against its USED border box
// (w×h in points) and applies the CSS Backgrounds 3 §5.1 overlap correction.
//
// The two components of one corner resolve against DIFFERENT bases — a horizontal
// semi-axis against the border box's WIDTH, a vertical one against its HEIGHT
// (§5.1). Using one basis for both is the classic bug here: it is invisible on a
// square box and wrong on every other, so the two resolveLen calls below
// deliberately take different pctBasis arguments.
//
// em radii resolve against the box's own font size, like every other length in this
// engine (resolveLen). A negative value cannot reach here — the parser rejects it —
// but a zero-or-negative box makes every radius meaningless, so it returns square.
func usedRadii(s *gcss.ComputedStyle, w, h float64) layout.CornerRadii {
	if w <= 0 || h <= 0 {
		return layout.CornerRadii{}
	}
	fs := s.FontSizePt
	corner := func(c gcss.CornerRadius) [2]float64 {
		hv, _ := resolveLen(c.H, fs, w) // horizontal semi-axis: % of WIDTH
		vv, _ := resolveLen(c.V, fs, h) // vertical semi-axis:   % of HEIGHT
		if hv < 0 {
			hv = 0
		}
		if vv < 0 {
			vv = 0
		}
		return [2]float64{hv, vv}
	}
	r := layout.CornerRadii{
		TL: corner(s.BorderTopLeftRadius),
		TR: corner(s.BorderTopRightRadius),
		BR: corner(s.BorderBottomRightRadius),
		BL: corner(s.BorderBottomLeftRadius),
	}
	if r.Zero() {
		return layout.CornerRadii{}
	}
	return r.Correct(w, h)
}

// logRoundedBorderDegradation reports, once per affected fragment, the two ways a
// ROUNDED border is approximated: the ring carries a single colour and is always
// filled solid (see Fragment.borderRing). A square-cornered box is unaffected —
// it still paints four independent, fully-styled edge strips — so this stays
// silent for every box that does not opt into a radius.
//
// This exists because the engine's degradation rule is that an approximation must
// announce itself. Painting a dashed rounded border as solid without a word would
// look like a rendering bug rather than a known gap.
func (e *Engine) logRoundedBorderDegradation(f *Fragment) {
	if f.Radii.Zero() {
		return
	}
	var seen *BorderEdge
	for _, s := range [...]layout.EdgeSide{layout.EdgeTop, layout.EdgeRight, layout.EdgeBottom, layout.EdgeLeft} {
		e2 := f.Border[s]
		if e2.Width <= 0 || e2.Style == layout.BorderNone {
			continue
		}
		if seen == nil {
			edge := e2
			seen = &edge
			if e2.Style != layout.BorderSolid {
				e.logf("css layout: rounded border painted solid; %v style is not followed around a corner", e2.Style)
			}
			continue
		}
		if e2.Color != seen.Color {
			e.logf("css layout: rounded border painted in one colour; per-side border colours are not followed around a corner")
			return
		}
	}
}

// clips reports whether b clips its overflow (CSS overflow ≠ visible). hidden,
// scroll, and auto all clip in this model (scroll/auto have no scroll affordance in
// the single-tall-page model, so they clip exactly like hidden). A clipping box also
// establishes a block formatting context (see establishesNewBFC).
func clips(b *cssbox.Box) bool {
	return b.Style.Overflow != "" && b.Style.Overflow != "visible"
}

// establishesNewBFC reports whether b establishes a new block formatting context,
// which suppresses margin collapsing between the box and its in-flow children. In
// the supported subset an inline-block does (its interior is an independent BFC), a
// float does (CSS 9.7: a float establishes a BFC for its contents), and an
// absolutely/fixed-positioned box does (it isolates its float context and
// margin-collapsing — without this an abs box containing a float would orphan it);
// an overflow≠visible box does (it clips its content, which requires a BFC). A
// relative box does NOT establish a BFC (only abs/fixed/float/inline-block do).
func establishesNewBFC(b *cssbox.Box) bool {
	if b.Position == cssbox.PosAbsolute || b.Position == cssbox.PosFixed {
		return true
	}
	// A transformed box establishes a containing block for its descendants (CSS
	// Transforms 1 §3), and painting it as ONE atom is what lets the transform bracket
	// wrap its background and its content together — Appendix E otherwise emits those
	// in separate phases, so the matrix would move the background and leave the text.
	if transformed(b) {
		return true
	}
	if b.Display == cssbox.DisplayTableCell || b.Display == cssbox.DisplayTable {
		return true // a table and a table cell each establish a BFC
	}
	if b.Display == cssbox.DisplayFlex || b.Display == cssbox.DisplayInlineFlex ||
		b.Display == cssbox.DisplayGrid || b.Display == cssbox.DisplayInlineGrid {
		return true // a flex/grid container establishes a BFC (CSS Flexbox 2 / Grid 2)
	}
	// A FILTERED box establishes a BFC too (CSS Display 3's BFC-root list includes
	// `filter` other than none). Beyond the spec, the engine relies on it: a BFC
	// fragment flattens through ONE AppendItems call, which is what lets the filter
	// bracket wrap the box's own decorations AND its contents as a single balanced
	// pair. Without it a filtered box's background would be emitted in the ancestor's
	// decoration phase and its text in the ancestor's content phase, with no single
	// range to bracket.
	return b.Display == cssbox.DisplayInlineBlock || b.Float != cssbox.FloatNone || clips(b) || filtered(b)
}

// establishesStackingContext reports whether b establishes a CSS stacking context.
// In the supported subset: any positioned box (relative/absolute/fixed), and any
// filtered box. The page root is treated as a stacking context by layoutTree
// directly. (Full CSS also includes opacity<1, transforms, etc. — none modeled yet.)
//
// filter != none MUST be here, not only in establishesNewBFC. A positioned
// descendant bubbles up to the nearest STACKING CONTEXT holder (see
// Fragment.appendItemsUnfiltered's IsStackingContext || IsBFC test and
// sortedPositioned): a box that is a BFC but not a stacking context does not
// consume its positioned layer, so the descendant escapes past it — and, for a
// filtered box, past the FilterPush/FilterPop bracket entirely. Measured before
// this was added: a position:relative or position:absolute child of a filtered
// box painted OUTSIDE the bracket (a static child correctly painted inside), so
// it would render completely unfiltered rather than merely mis-ordered. A
// filtered box with a positioned child is an everyday pattern (badges, overlays,
// dropdowns), so this is not an exotic case.
func establishesStackingContext(b *cssbox.Box) bool {
	// A TRANSFORMED element establishes one too (CSS Transforms 1 §3). That is not a
	// detail: the transform is painted as a matrix bracket around the box's items, and
	// only a stacking context emits its background, its children, and its content as
	// one contiguous run. A plain block splits them across Appendix E's decoration and
	// content phases, so a bracket around it would transform its background without its
	// text.
	return b.Position != cssbox.PosStatic || filtered(b) || transformed(b)
}

// transformed reports whether b carries a non-identity CSS transform.
//
// The ZERO-VALUE guard is essential, not defensive. An anonymous box carries a
// zero-value ComputedStyle rather than the CSS initial one (see isAnonymous), so its
// Transform is all zeros — which is not the identity matrix, and would make every
// anonymous box a stacking context and a BFC. That silently reordered painting and
// broke a WPT reftest before the guard was added.
func transformed(b *cssbox.Box) bool {
	if b == nil {
		return false
	}
	t := b.Style.Transform
	if t == (gcss.Transform{}) {
		return false // zero-value style (anonymous box): no transform, not a matrix of zeros
	}
	return !t.IsIdentity()
}

// isAnonymous reports whether b is an engine-generated anonymous box. Anonymous
// boxes (inline-in-block wrappers and block-in-inline splits) carry a zero-value
// ComputedStyle, NOT the CSS initial style: e.g. its Width/MaxWidth Unit is the
// zero UnitPx, which would read as "width:0/max-width:0" rather than the auto/none
// an anonymous box must have. Box-model resolution therefore treats an anonymous
// box's own dimensions as their defaults (auto width/height, no min/max, zero
// margins/padding/borders — the last three already follow from the zero style).
func isAnonymous(b *cssbox.Box) bool {
	return b.Kind == cssbox.BoxAnonBlock || b.Kind == cssbox.BoxAnonInline ||
		b.Kind == cssbox.BoxAnonFlexItem || b.Kind == cssbox.BoxAnonGridItem ||
		b.Kind == cssbox.BoxAnonTablePart
}

// resolveContentWidth resolves the content-box width of block box b in a containing
// block of width cbWidth, given its already-resolved edges ed. It implements the
// CSS block width algorithm for the supported subset: an auto width fills the
// containing block minus the horizontal margins/borders/paddings; a fixed width is
// used directly (content-box) or de-padded (border-box). The result is then clamped
// by min-width/max-width (CSS 10.4) in content-box terms and floored at 0.
func resolveContentWidth(b *cssbox.Box, cbWidth float64, ed edges) float64 {
	fs := b.Style.FontSizePt
	horiz := ed.mL + ed.mR + ed.bL + ed.bR + ed.pL + ed.pR
	insets := ed.pL + ed.pR + ed.bL + ed.bR // padding+border subtracted under border-box
	borderBox := b.Style.BoxSizing == "border-box"

	// An anonymous box has no specified dimensions: it fills its container (its
	// edges are all zero, so this is just cbWidth) with no min/max clamp.
	if isAnonymous(b) {
		w := cbWidth - horiz
		if w < 0 {
			w = 0
		}
		return w
	}

	var contentW float64
	if w, isAuto := resolveLen(b.Style.Width, fs, cbWidth); isAuto {
		contentW = cbWidth - horiz
	} else if borderBox {
		contentW = w - insets
	} else {
		contentW = w
	}

	// min/max clamp. Both describe the same box edge as width does (border box under
	// border-box sizing), so convert each to content-box terms by subtracting the
	// insets before comparing — keeping the comparison consistent with contentW.
	maxW, _ := resolveLen(b.Style.MaxWidth, fs, cbWidth)
	hasMax := b.Style.MaxWidth.Unit != gcss.UnitAuto // UnitAuto models "none"
	// min-width default is 0 (UnitPx zero); resolve and apply unconditionally.
	minW, _ := resolveLen(b.Style.MinWidth, fs, cbWidth)
	if borderBox {
		maxW -= insets
		minW -= insets
	}
	return clampMaxMin(contentW, minW, maxW, hasMax)
}

// bfcChildBandHeight estimates the vertical extent of a BFC child for the float-band
// query in sibling avoidance, before the child is laid out. It uses the child's
// resolved fixed height when it has one, else a small nonzero probe (1pt) so the band
// query at least samples the child's top row (the band is coarse; the height feedback
// is not iterated — consistent with the per-line line-height approximation in the IFC).
func bfcChildBandHeight(b *cssbox.Box, cbWidth float64) float64 {
	if !isHeightAuto(b) {
		ed := usedEdges(b, cbWidth)
		if h := resolveFixedHeight(b, cbWidth, ed); h > 0 {
			return h
		}
	}
	return 1
}

// childWidthFor returns a BFC child's resolved border-box width for the
// fits-beside-the-float test in sibling avoidance: the width its border box wants when
// laid out at the full containing width (auto fills, fixed is used). It mirrors
// resolveContentWidth + the border/padding insets so the fit test compares like with
// like.
func childWidthFor(b *cssbox.Box, cbWidth float64) float64 {
	ed := usedEdges(b, cbWidth)
	return resolveContentWidth(b, cbWidth, ed) + ed.pL + ed.pR + ed.bL + ed.bR
}

// clampMaxMin clamps v to at most maxV (only when hasMax), then to at least minV,
// then to non-negative — the CSS 10.4 max-then-min-then-floor order. It is the
// shared min/max-clamp primitive for both the block width algorithm
// (resolveContentWidth) and the replaced-element sizing algorithm
// (replacedUsedSize), so the two stay consistent. Callers resolve the min/max
// Lengths (and adjust for box-sizing) before calling, since the comparison is in
// the same coordinate terms as v.
func clampMaxMin(v, minV, maxV float64, hasMax bool) float64 {
	if hasMax && v > maxV {
		v = maxV
	}
	if v < minV {
		v = minV
	}
	if v < 0 {
		v = 0
	}
	return v
}

// isHeightAuto reports whether b's height is auto (content-derived). An anonymous
// box is always auto-height (its zero-value style would otherwise read as
// height:0; see isAnonymous).
func isHeightAuto(b *cssbox.Box) bool {
	if isAnonymous(b) {
		return true
	}
	_, isAuto := resolveLen(b.Style.Height, b.Style.FontSizePt, 0)
	return isAuto
}

// clampAutoHeight applies min-height/max-height to a block whose height is auto,
// returning the clamped CONTENT height. It mirrors resolveFixedHeight's conversions
// (box-sizing, cbWidth percentage basis) so the two paths agree on what a given
// max-height means; the difference is only which height it constrains.
//
// CSS 10.7 applies max-height before min-height, so a min larger than the max wins.
// contentH is returned unchanged when neither property is set, which is the common
// case and keeps every existing box byte-identical.
func clampAutoHeight(b *cssbox.Box, contentH, cbWidth float64, ed edges) float64 {
	fs := b.Style.FontSizePt
	insets := ed.pT + ed.pB + ed.bT + ed.bB
	borderBox := b.Style.BoxSizing == "border-box"

	if maxL := b.Style.MaxHeight; maxL.Unit != gcss.UnitAuto {
		maxH, _ := resolveLen(maxL, fs, cbWidth)
		if borderBox {
			maxH -= insets
		}
		if maxH < 0 {
			maxH = 0
		}
		if contentH > maxH {
			contentH = maxH
		}
	}
	if minL := b.Style.MinHeight; minL.Unit != gcss.UnitAuto {
		minH, _ := resolveLen(minL, fs, cbWidth)
		if borderBox {
			minH -= insets
		}
		if contentH < minH {
			contentH = minH
		}
	}
	if contentH < 0 {
		contentH = 0
	}
	return contentH
}

// resolveFixedHeight resolves a block's fixed (non-auto) content height against a
// containing block of width cbWidth (CSS resolves height percentages against the
// containing block width in this engine's single-axis model), converting for
// box-sizing and clamping by min-height/max-height. The percentage basis is cbWidth
// to match how the vertical margins/paddings resolve; a height percentage against an
// auto-height containing block is an edge case deferred with the rest of vertical
// percentage subtleties.
func resolveFixedHeight(b *cssbox.Box, cbWidth float64, ed edges) float64 {
	fs := b.Style.FontSizePt
	insets := ed.pT + ed.pB + ed.bT + ed.bB
	borderBox := b.Style.BoxSizing == "border-box"

	h, _ := resolveLen(b.Style.Height, fs, cbWidth)
	if borderBox {
		h -= insets
	}
	if maxL := b.Style.MaxHeight; maxL.Unit != gcss.UnitAuto {
		maxH, _ := resolveLen(maxL, fs, cbWidth)
		if borderBox {
			maxH -= insets
		}
		if h > maxH {
			h = maxH
		}
	}
	minH, _ := resolveLen(b.Style.MinHeight, fs, cbWidth)
	if borderBox {
		minH -= insets
	}
	if h < minH {
		h = minH
	}
	if h < 0 {
		h = 0
	}
	return h
}

// resolveLen resolves a CSS length to points against a font size (for em) and a
// percentage basis (for %). px and pt are treated 1:1 (consistent with the
// cascade's px→pt handling). isAuto is true for the auto keyword, with val 0.
func resolveLen(l gcss.Length, fontSizePt, pctBasis float64) (val float64, isAuto bool) {
	switch l.Unit {
	case gcss.UnitPx, gcss.UnitPt:
		return l.Value, false
	case gcss.UnitEm:
		return l.Value * fontSizePt, false
	case gcss.UnitPercent:
		return l.Value / 100 * pctBasis, false
	case gcss.UnitAuto:
		return 0, true
	default:
		return l.Value, false
	}
}

// collapseMargins collapses a set of adjoining margins per CSS 8.3.1: the collapsed
// margin is the largest of the non-negative margins plus the most-negative of the
// negative margins. With only non-negative inputs it is their max; with mixed signs
// it is maxPositive + minNegative; with only negatives it is the most negative.
func collapseMargins(margins ...float64) float64 {
	maxPos, minNeg := 0.0, 0.0
	for _, m := range margins {
		if m > maxPos {
			maxPos = m
		}
		if m < minNeg {
			minNeg = m
		}
	}
	return maxPos + minNeg
}

// mapBorderStyle maps a CSS border-style keyword to the layout border style. Unknown
// values, the empty string, and "none" map to BorderNone (no border drawn, and zero
// used border width).
func mapBorderStyle(s string) layout.BorderStyle {
	switch s {
	case "solid":
		return layout.BorderSolid
	case "dashed":
		return layout.BorderDashed
	case "dotted":
		return layout.BorderDotted
	case "double":
		return layout.BorderDouble
	case "outset":
		return layout.BorderOutset
	case "inset":
		return layout.BorderInset
	case "ridge":
		return layout.BorderRidge
	case "groove":
		return layout.BorderGroove
	default:
		return layout.BorderNone
	}
}

// shiftFragments translates a slice of fragments (laid out in a local frame whose
// content-box top is local Y 0) down by dy into page space, recursing into
// descendants so the whole subtree moves together.
func shiftFragments(frags []*Fragment, dy float64) {
	if dy == 0 {
		return
	}
	for _, f := range frags {
		shiftFragment(f, dy)
	}
}

// shiftFragment translates one fragment and its descendants by dy. It is
// translateFragment restricted to the block-flow case: block children were
// positioned in page-space X already, so only Y moves.
func shiftFragment(f *Fragment, dy float64) { translateFragment(f, 0, dy) }

// shiftFragmentSelf translates ONLY a fragment's own border box (Y — its border edges and
// background derive from Y) by dy, WITHOUT recursing Children, Floats, or Positioned and
// WITHOUT touching its clip/collapsed/positioned-chain fields. The pagination pass uses it
// on a shallow-cloned html/body wrapper to move the wrapper's own border/background into a
// page's local frame so they fragment correctly, while the wrapper's children (the page's
// blocks) and its positioned layer are shifted separately — moving them here too would
// double-shift them. (A structural html/body wrapper carries no overflow clip, collapsed
// grid, or positioned clip chain of its own, so Y is the only page-space field it owns.)
func shiftFragmentSelf(f *Fragment, dy float64) {
	f.Y += dy
}

// shiftFragmentExtras moves the page-space fields a fragment OWNS — but that are not
// reachable through Children/Floats/Lines/Image/Vector — by (dx,dy): the box's own clip
// rectangle (ClipRect), its border-collapse grid strips (Collapsed), each positioned
// descendant's clip-escape chain (PositionedInfo[].ClipChain), and its out-of-flow
// positioned descendants (the abs/fixed entries of Positioned). shiftFragment delegates
// to translateFragment (dx==0), so this is called from translateFragment alone; it is
// called AFTER recursing Children/Floats so a Positioned entry that is abs/fixed (out of
// flow, NOT aliased by any Children entry) is moved exactly once.
//
// A position:relative entry of Positioned is deliberately SKIPPED here: a relative box
// stays in flow, so the SAME *Fragment pointer is also a Children entry and already moved
// by the Children recursion — moving it again here would double-shift it. (Its paint-time
// RelOffset is applied separately by AppendItems and is frame-independent.) Abs/fixed
// entries are out of flow, appear only in Positioned, and so must be moved here. During
// the in-flow layout passes Positioned holds only consumed relatives (abs/fixed are
// resolved later, in pass 2, directly at final coordinates), so in the un-paginated path
// this loop moves nothing — preserving byte-identical output; the abs/fixed branch fires
// only when a finished subtree carrying baked-in abs/fixed descendants is relocated
// wholesale (the pagination relative-block shift, and resolveAbsolute's own translate).
func shiftFragmentExtras(f *Fragment, dx, dy float64) {
	if f.Clips {
		f.ClipRect.x += dx
		f.ClipRect.y += dy
	}
	for _, bg := range f.BgImages {
		bg.OriginX += dx
		bg.OriginY += dy
		bg.ClipX += dx
		bg.ClipY += dy
	}
	if f.BgImage != nil && len(f.BgImages) == 0 {
		f.BgImage.OriginX += dx
		f.BgImage.OriginY += dy
		f.BgImage.ClipX += dx
		f.BgImage.ClipY += dy
	}
	// HeaderBottom is a Y in the same frame as f.Y; a table moved into page space
	// must carry it along, or splitTableForPage's `HeaderBottom <= Y` guard sees
	// the header above the table and skips the repeat.
	if f.HeaderBottom != 0 {
		f.HeaderBottom += dy
	}
	for i := range f.Collapsed {
		f.Collapsed[i].XPt += dx
		f.Collapsed[i].YPt += dy
	}
	for i := range f.PositionedInfo {
		for j := range f.PositionedInfo[i].ClipChain {
			f.PositionedInfo[i].ClipChain[j].x += dx
			f.PositionedInfo[i].ClipChain[j].y += dy
		}
	}
	for _, p := range f.Positioned {
		if isRelativeFragment(p) {
			continue // in flow → also a Children entry → already shifted there
		}
		translateFragment(p, dx, dy)
	}
}

// isRelativeFragment reports whether a positioned fragment came from a position:relative
// box (in flow, hence aliased with a Children entry). A nil Box reads as not-relative
// (anonymous/synthetic fragments and abs/fixed boxes, whose coordinates are baked in).
func isRelativeFragment(f *Fragment) bool {
	return f.Box != nil && f.Box.Position == cssbox.PosRelative
}

// shiftLines translates inline line baselines by dy (the inline content of the box
// being shifted; its descendant fragments move via shiftFragments).
func shiftLines(lines []LineFragment, dy float64) {
	if dy == 0 {
		return
	}
	for li := range lines {
		lines[li].BaselineY += dy
	}
}

// debugTag derives a stable, paint-irrelevant label for a fragment to aid test
// lookup and debugging. The cssbox.Box does not carry the source element tag/id, so
// this is the box's structural kind and display intent rather than an element name;
// tests navigate the fragment tree positionally, not by this tag.
func debugTag(b *cssbox.Box) string {
	switch b.Kind {
	case cssbox.BoxAnonBlock:
		return "anon-block"
	case cssbox.BoxAnonInline:
		return "anon-inline"
	case cssbox.BoxReplaced:
		return "replaced"
	case cssbox.BoxText:
		return "text"
	case cssbox.BoxInline:
		return "inline"
	default:
		return "block"
	}
}

// setFragmentHeight overrides a fragment's border-box height, growing or shrinking the
// box itself without moving its origin or its children.
//
// Only the abs-positioned top+bottom case uses it: everywhere else a fragment's height
// is content-derived and authoritative. The children are deliberately NOT rescaled —
// CSS says the box's height comes from the offsets while its contents lay out normally
// and may overflow, which is exactly what a taller-than-content box should do.
func setFragmentHeight(f *Fragment, h float64) {
	if f == nil || h < 0 {
		return
	}
	f.H = h
	if f.Clips {
		f.ClipRect.h = h - (f.ClipRect.y - f.Y) - (f.Y + h - (f.ClipRect.y + f.ClipRect.h))
		if f.ClipRect.h < 0 {
			f.ClipRect.h = 0
		}
	}
}
