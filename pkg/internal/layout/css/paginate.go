package css

import (
	"context"

	gcss "github.com/nathanstitt/omnidoc/pkg/internal/css"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/cssbox"
)

// LayoutPaged lays out root at viewportW points and fragments the result into
// pageH-tall pages, returning one-or-more pages. pageH <= 0 means no pagination:
// it returns exactly what Layout returns (a single tall page sized to the content
// height) — the byte-identical path. Otherwise it builds the same fragment tree as
// Layout (via the shared layoutTree), then splits the document's top-level in-flow
// block fragments into pageH-tall pages, breaking between block boundaries and at
// forced page breaks (break-before / break-after: page|always and the legacy
// page-break-* aliases).
//
// It never panics on malformed input: a recover at the page boundary returns a
// single empty pageH-tall page. It degrades gracefully — a single block taller than
// a page overflows its page rather than splitting (logged once per over-tall block).
// position:relative blocks paginate normally (a top-level one is routed to its bucket's
// page; one nested under a static wrapper rides its nearest top-level ancestor's page),
// and a relative block's abs descendants and its own border/clip follow it (shiftFragment
// moves them). Only absolute/fixed boxes whose containing block is the page (not a
// paginated relative ancestor) are undistributed and ride the first page — a documented
// deferral. The html/body wrapper's border/background is fragmented per page.
func (e *Engine) LayoutPaged(ctx context.Context, root *cssbox.Box, viewportW, pageH float64) (pages *layout.Pages, err error) {
	e.setMarginBase(root)
	if pageH <= 0 {
		return e.Layout(ctx, root, viewportW) // handles canvas-background propagation itself
	}
	defer func() {
		if r := recover(); r != nil {
			e.logf("css layout: recovered from panic: %v", r)
			pages = &layout.Pages{Pages: []layout.Page{{WidthPt: viewportW, HeightPt: pageH}}}
			err = nil
		}
	}()

	// CSS background propagation: lift the root/body background onto every page's
	// canvas (set on the returned Pages below) and clear it from the source box.
	// Must run before layoutTree.
	canvasBG := propagateCanvasBackground(root)
	defer func() {
		if pages != nil {
			pages.CanvasBackground = canvasBG
		}
	}()

	frag := e.layoutTree(ctx, root, viewportW)
	if frag == nil {
		return &layout.Pages{Pages: []layout.Page{{WidthPt: viewportW, HeightPt: pageH}}}, nil
	}
	return e.paginate(frag, viewportW, pageH), nil
}

// paginate fragments the laid-out root fragment into pageH-tall pages, breaking only
// between the document's top-level in-flow block fragments (the body fragment's
// Children). Floats and positioned descendants are lifted out of Children (into the
// root's Floats / Positioned), so the walk never sees them; they flatten with the
// first page's root wrapper.
func (e *Engine) paginate(root *Fragment, viewportW, pageH float64) *layout.Pages {
	body := bodyFragment(root)
	if body == nil || len(body.Children) == 0 {
		// No top-level blocks: the whole document is a single page, pageH tall.
		return &layout.Pages{Pages: []layout.Page{root.Page(viewportW, pageH)}}
	}

	// The containing-block width the top-level blocks' own margins resolve against is
	// the body content width (border box minus the body's own border + padding). It is
	// needed to recover a block's used top margin when retaining it at an unforced break.
	cbWidth := contentWidth(body, viewportW)
	buckets, grown := bucketBlocksGrowing(body.Children, pageH, cbWidth, e.logf)
	applySplitGrowth(grown, root.Floats, root, body)

	// Pull the FIRST page's top up to the outermost wrapper border-box top when the
	// html/body wrapper has a border or background ABOVE the first block (its top border +
	// padding). bucketBlocks sets page 0's top to the first block's Y, which would clip the
	// body's own top border/background off the top of page 0 (the wrapper sits above the
	// first block). Including the wrapper box on page 0 keeps the body's TOP border on
	// page 0 and lets the first block sit below it. Only page 0 is adjusted — the wrapper
	// has no "top" on later pages (its content continues), so their tops stay at the first
	// block of that page. A wrapper with no visible decoration leaves top unchanged
	// (decorationTop returns the block Y), so the un-bordered common case — including the
	// existing paginate golden — is byte-identical.
	if len(buckets) > 0 {
		top := buckets[0].top
		if t := wrapperDecorationTop(root, top); t < top {
			top = t
		}
		if t := wrapperDecorationTop(body, top); t < top {
			top = t
		}
		buckets[0].top = top
	}

	// Distribute the root's positioned layer across pages. A top-level position:relative
	// block is in flow (so it is in body.Children and gets bucketed) but is ALSO lifted
	// into the root's positioned layer for painting — the SAME *Fragment pointer appears
	// in both root.Positioned and a bucket; it is routed to its block's page and shifted
	// for free as a bucket block. A page-CB absolute box is routed to the page whose band
	// contains its top and shifted into that page's local frame here; a position:fixed box
	// is cloned onto every page (each clone shifted), so it repeats. (An abs box whose CB
	// is a positioned ancestor is not in root.Positioned — it follows that ancestor.)
	perPagePos := splitPositionedByPage(root, buckets, func(int) float64 { return pageH })
	// Distribute the out-of-flow float layer per page by each float's Y band (mirroring
	// the page-CB-absolute distribution above), so a float inside a section forced onto a
	// later page paints on that page rather than riding page 0.
	perPageFloats := splitFloatsByPage(root, buckets)

	// Uniform geometry: every page is viewportW × pageH with no content inset (the
	// WithPageSize path; @page margins/sizes are applied by paginateDoc's geomFn).
	geomFn := func(int, pageBucket) pageGeom {
		return pageGeom{pageW: viewportW, pageH: pageH, contentW: viewportW, contentH: pageH}
	}
	// nil running map: this path (WithPageSize, no @page rule) has no margin boxes, so
	// appendMarginBoxes early-returns and element() never fires.
	pages := e.assemblePages(root, body, buckets, perPagePos, perPageFloats, geomFn, nil)
	return &layout.Pages{Pages: pages}
}

// assemblePages builds one layout.Page per bucket: it shallow-clones the root/body
// wrapper for the page, assigns the page's positioned + float layers, fragments the
// wrapper's own border/background into the page's local frame, shifts the page's blocks
// to local Y 0, optionally insets the content by the page's @page margins (geomFn), and
// flattens. geomFn(i, bucket) returns page i's size and content box; a zero marginL/
// marginT (the WithPageSize path) is byte-identical to the un-inset behavior.
func (e *Engine) assemblePages(root, body *Fragment, buckets []pageBucket, perPagePos []pagePositioned, perPageFloats [][]*Fragment, geomFn func(int, pageBucket) pageGeom, running map[string]*cssbox.Box) []layout.Page {
	// Per-page CSS running-string snapshots (for string() in @page margin boxes),
	// computed once: each page's value carried-in / first-set / last-set, from the
	// blocks bucketed up to and including that page.
	snaps := buildStringSnapshots(buckets)
	pages := make([]layout.Page, 0, len(buckets))
	for i, bk := range buckets {
		g := geomFn(i, bk)
		// Shallow-clone the root/body wrapper so each page can carry its own block
		// list without mutating the shared tree. The block fragments themselves are
		// the original pointers — each belongs to exactly one page, so shifting them
		// in place is safe (no cross-page aliasing).
		pageRoot := *root
		pageBody := *body
		pageBody.Children = bk.blocks
		// Assign this page's share of the positioned layer (its relative blocks, the
		// page-CB abs boxes whose band is this page, and a clone of every fixed box) and
		// its share of the out-of-flow float layer (each float routed to the page whose
		// band holds its top). The body owns no positioned/float layer of its own (the
		// root does), so its copies are cleared.
		pageRoot.Positioned = perPagePos[i].frags
		pageRoot.PositionedInfo = perPagePos[i].infos
		pageRoot.Floats = perPageFloats[i]
		pageBody.Positioned = nil
		pageBody.PositionedInfo = nil
		pageBody.Floats = nil
		// Fragment the html/body wrapper's own border + background across pages. The
		// wrapper clones are NOT in bk.blocks, so shiftFragments below does not move them;
		// position each wrapper's OWN box (not its children) into the same block-shifted
		// frame by subtracting bk.top. This puts the wrapper's full-document border box at
		// local Y body.Y-bk.top..body.Y+body.H-bk.top, so the page bitmap naturally
		// fragments it: the TOP edge (at the box top) is on-page only where the box top
		// falls (page 0), the BOTTOM edge only on the last page, and the LEFT/RIGHT side
		// edges span every page's band (clipped to the bitmap). Without this the wrapper
		// border painted at full-document geometry on every page — a spurious top edge at
		// Y 0 and full-height sides on pages ≥1. (Backgroundless, borderless wrappers — the
		// common case, incl. the existing paginate golden — emit nothing here, so this is
		// byte-identical for them.)
		shiftFragmentSelf(&pageBody, -bk.top)
		shiftFragmentSelf(&pageRoot, -bk.top)
		sliceWrapperForPage(&pageBody, i == len(buckets)-1, g.contentH)
		sliceWrapperForPage(&pageRoot, i == len(buckets)-1, g.contentH)
		// Replace the body entry (root.Children[last]) with the body clone, preserving
		// any non-body children.
		children := make([]*Fragment, len(root.Children))
		copy(children, root.Children)
		children[len(children)-1] = &pageBody
		pageRoot.Children = children

		// Translate this page's blocks up so the page's first content sits at local
		// Y 0. Must run BEFORE Page(...) flattens (the flatten reads the shifted Ys).
		// Because a routed relative-block entry is the SAME pointer as a bucket block,
		// this shift also moves that entry into its page's local space — no separate
		// positioned shift is needed.
		shiftFragments(bk.blocks, -bk.top)

		// Content is inset by the @page margins AND, when bleeding, shifted inward by the
		// bleed so the trim box sits at (bleed,bleed) within the larger media-box bitmap.
		dx, dy := g.marginL+g.bleed, g.marginT+g.bleed
		if dx != 0 || dy != 0 {
			translateFragment(&pageRoot, dx, dy)
		}
		// The page bitmap is the MEDIA box (trim + bleed all sides); with no bleed this is
		// the trim box, byte-identical to before.
		pg := pageRoot.Page(g.mediaW(), g.mediaH())
		// Margin boxes (running headers/footers) are positioned in the trim-box frame, so
		// they too must shift by the bleed. appendMarginBoxes computes rects from g (trim
		// frame); shift the items it adds by (bleed,bleed). Simplest: append them, then
		// translate only the newly-added items by the bleed.
		before := len(pg.Items)
		pg.Items = e.appendMarginBoxes(pg.Items, g, i, len(buckets), snaps[i], running)
		if g.bleed != 0 {
			translateItems(pg.Items, before, g.bleed, g.bleed)
		}
		// Registration marks draw in the bleed band, in MEDIA-box coordinates (no shift).
		pg.Items = appendCropMarks(pg.Items, g)
		pages = append(pages, pg)
	}
	return pages
}

// sliceWrapperForPage finishes a page's clone of the html/body wrapper, already moved
// into the page's block-shifted frame. The wrapper is one box that runs from the first
// page to the last, so on every page but the last it continues past the page: its
// bottom edge is not drawn there, and its box reaches at least the bottom of the
// page's content area. Without this, a forced page break (which leaves blank space the
// wrapper's flow height never counted) put the wrapper's bottom edge on the page
// before the last, with its side edges stopping short.
func sliceWrapperForPage(w *Fragment, lastPage bool, contentH float64) {
	if lastPage {
		return
	}
	w.Border[layout.EdgeBottom] = BorderEdge{}
	if w.Y+w.H < contentH {
		w.H = contentH - w.Y
	}
}

// pageBucket is one page's worth of top-level block fragments plus the page-space Y
// (top) where the page's content begins; the blocks are shifted up by top so the
// page's first content lands at local Y 0.
type pageBucket struct {
	top    float64
	blocks []*Fragment
}

// pagePositioned is one page's slice of the positioned layer: the fragments painted
// in that page's positioned band and the parallel PositionedInfo (same length).
type pagePositioned struct {
	frags []*Fragment
	infos []PositionedInfo
}

// splitPositionedByPage partitions root.Positioned (and its parallel PositionedInfo)
// across the buckets, distributing each kind of positioned entry to the page(s) it
// belongs on:
//
//   - A position:relative block — in flow (so it is bucketed) but painted from the
//     positioned layer — is routed to the page its in-flow position landed on, so it
//     paginates normally: directly when its *Fragment pointer is itself a top-level bucket
//     block, or via its nearest TOP-LEVEL ANCESTOR block when it is a relative block NESTED
//     below a static wrapper (its pointer bubbled to root.Positioned but its in-flow box
//     lives in that ancestor's subtree, shifted onto the ancestor's page). It is NOT shifted
//     here — the entry IS the in-flow fragment, already moved by the caller's shiftFragments.
//   - A page-CB position:absolute box is routed to the page whose band contains its
//     border-box top (pageForY) and SHIFTED into that page's local frame here (it is out of
//     flow — not a bucket block — so this is its only shift). A box above page 0 / below the
//     last band clamps to the first / last page (never dropped).
//   - A page-CB position:fixed box repeats on EVERY page: a deep clone per page, each shifted
//     into that page's local frame, so it paints at the same on-page coordinates everywhere.
//     (A clone is required because shiftFragment mutates in place and one *Fragment cannot
//     carry N page origins.)
//
// (An abs box whose CB is a positioned ancestor is in that ancestor's .Positioned, not
// root.Positioned, so it is not seen here — it follows the ancestor.) The result has one
// entry per bucket; each page's parallel info slice stays aligned with its frags.
// pageHeightFn returns page i's content height, for re-anchoring a bottom-anchored
// position:fixed box. The uniform path passes a constant; the @page path can vary it
// per page.
type pageHeightFn func(i int) float64

func splitPositionedByPage(root *Fragment, buckets []pageBucket, pageHeightOf pageHeightFn) []pagePositioned {
	out := make([]pagePositioned, len(buckets))
	if root == nil || len(root.Positioned) == 0 || len(buckets) == 0 {
		return out
	}
	// Map each top-level block — AND every positioned (relative) descendant in its in-flow
	// subtree — to the block's bucket index. Walking the subtree (Children only: the
	// in-flow chain) catches a relative block nested under static wrappers whose pointer
	// bubbled to root.Positioned. Abs/fixed entries are out of flow and so are NOT in any
	// block's Children subtree → they are not in this map and are distributed by kind below.
	blockPage := make(map[*Fragment]int)
	var mark func(f *Fragment, page int)
	mark = func(f *Fragment, page int) {
		blockPage[f] = page
		for _, c := range f.Children {
			mark(c, page)
		}
	}
	for bi := range buckets {
		for _, b := range buckets[bi].blocks {
			mark(b, bi)
		}
	}
	assign := func(page int, frag *Fragment, info PositionedInfo) {
		out[page].frags = append(out[page].frags, frag)
		out[page].infos = append(out[page].infos, info)
	}
	for i, frag := range root.Positioned {
		// PositionedInfo may be shorter than Positioned (a nil/short slice reads as the
		// zero value); guard the parallel index.
		var info PositionedInfo
		if i < len(root.PositionedInfo) {
			info = root.PositionedInfo[i]
		}
		if p, ok := blockPage[frag]; ok {
			// A relative block (or a relative descendant of one) → its in-flow page; the
			// caller's shiftFragments moves it (it is a bucket block / in a bucket subtree).
			assign(p, frag, info)
			continue
		}
		if isFixedFragment(frag) {
			// position:fixed → repeat on every page. A fixed box is positioned against the
			// VIEWPORT, so resolveAbsolute placed it against the page rect {y:0,h:pageH}:
			// its frag.Y is already its offset from the page top (e.g. top:10px → Y 10),
			// which is the correct page-LOCAL Y on EVERY page. So it is assigned to every
			// page WITHOUT a per-page shift — the same read-only fragment is shared (the
			// flatten only reads it; no page mutates it, since the abs shift below touches a
			// different pointer and the block shift touches bucket blocks).
			//
			// A BOTTOM-anchored fixed box is the exception: pass 2 resolved it against the
			// full single-tall document height, so its Y sits at the document bottom and it
			// is off-page on every page. Those are re-anchored per page below.
			if bottom, h, ok := fixedBottomAnchor(frag); ok && pageHeightOf != nil {
				// Bottom-anchored: pass 2 resolved it against the single tall
				// document, so its Y is at the DOCUMENT bottom and off-page
				// everywhere. Re-derive a page-local Y per page. Each page needs its
				// own Y, so the fragment is cloned rather than shared.
				for pi := range buckets {
					clone := cloneFloatForPageMap(frag, map[*Fragment]*Fragment{})
					translateFragment(clone, 0, pageHeightOf(pi)-bottom-h-clone.Y)
					assign(pi, clone, info)
				}
				continue
			}
			for pi := range buckets {
				assign(pi, frag, info)
			}
			continue
		}
		// page-CB position:absolute → the page whose band holds its top, shifted into that
		// page's local frame (it is out of flow, not a bucket block, so this is its only
		// shift).
		page := pageForY(frag.Y, buckets)
		shiftFragment(frag, -buckets[page].top)
		assign(page, frag, info)
	}
	return out
}

// fixedBottomAnchor reports whether f is a BOTTOM-anchored position:fixed box —
// a definite `bottom` with an auto `top` — returning the resolved bottom offset and
// the fragment's border-box height.
//
// Only this shape needs re-anchoring. A top-anchored fixed box's Y is already its
// offset from the page top (pass 2 resolved it against the page rect), which is the
// correct page-local Y on every page; a box with BOTH offsets definite is sized by
// them and likewise resolved from the top.
func fixedBottomAnchor(f *Fragment) (bottom, height float64, ok bool) {
	if f == nil || f.Box == nil {
		return 0, 0, false
	}
	st := f.Box.Style
	if st.Top.Unit != gcss.UnitAuto || st.Bottom.Unit == gcss.UnitAuto {
		return 0, 0, false
	}
	// A percentage bottom resolves against the page height, which varies per page; the
	// caller would need to re-resolve it per page rather than shift by a fixed amount.
	// Only absolute units are handled here.
	v, pct := resolveLen(st.Bottom, st.FontSizePt, 0)
	if pct {
		return 0, 0, false
	}
	return v, f.H, true
}

// pageForY returns the index of the bucket whose vertical band contains page-space Y y:
// the last bucket whose top is <= y (bands are [buckets[i].top, buckets[i+1].top), the last
// extending to +inf). A y above the first band clamps to page 0; below the last clamps to
// the last page. buckets must be non-empty and ordered by ascending top (bucketBlocks
// guarantees both).
func pageForY(y float64, buckets []pageBucket) int {
	page := 0
	for i := range buckets {
		if buckets[i].top <= y {
			page = i
		} else {
			break
		}
	}
	return page
}

// isFixedFragment reports whether a fragment came from a position:fixed box (its CB is the
// viewport, so it repeats on every page). A nil fragment or nil Box reads as not-fixed.
func isFixedFragment(f *Fragment) bool {
	return f.Box != nil && f.Box.Position == cssbox.PosFixed
}

// splitFloatsByPage partitions root.Floats across pages by each float's vertical band,
// mirroring the page-CB-absolute case of splitPositionedByPage: a float is out of flow
// (its pointer is in root.Floats, never in a bucket's Children), so it is routed to the
// page whose band contains its margin-box top (pageForY) and shifted into that page's
// local frame here. Each float belongs to exactly one page, so shifting the original
// pointer in place is safe (no cross-page aliasing). A float's own subtree — including
// any abs/relative descendant on its Positioned layer and its clip rect — rides along,
// because shiftFragment moves every page-space field the fragment owns. Returns one
// slice per bucket (nil where a page has no floats). Without this a top-level float rode
// page 0 only; this distributes it to the page its containing block fragments onto.
func splitFloatsByPage(root *Fragment, buckets []pageBucket) [][]*Fragment {
	out := make([][]*Fragment, len(buckets))
	if root == nil || len(root.Floats) == 0 || len(buckets) == 0 {
		return out
	}
	for _, fl := range root.Floats {
		page := pageForY(fl.Y, buckets)
		shiftFragment(fl, -buckets[page].top)
		out[page] = append(out[page], fl)
	}
	return out
}

// bucketBlocks assigns top-level in-flow block fragments to pageH-tall pages,
// breaking between blocks on overflow and at forced page breaks. It is pure (no
// engine receiver) so it can be unit-tested in isolation; logf records the over-tall
// degradation. cbWidth is the blocks' containing-block (body content) width, used to
// recover a block's used top margin when retaining it at an unforced break. It always
// returns at least one bucket (an empty bucket{top:0} for empty input), and never
// emits a leading or trailing empty bucket for a forced break on the first / last block.
//
// A top-level block's effective forced break (effectiveBreaks) folds in a forced
// break-before on content at the START of the block and a forced break-after on
// content at its END (a nested break propagated to the top-level ancestor), so the
// common `.page-break { break-before: page }` on a nested element is honored.
func bucketBlocks(blocks []*Fragment, pageH, cbWidth float64, logf func(string, ...any)) []pageBucket {
	buckets, _ := bucketBlocksGrowing(blocks, pageH, cbWidth, logf)
	return buckets
}

// bucketBlocksGrowing is bucketBlocks that also returns the growths of split tails
// (repeated table headers), in the order they happened. The blocks after a grown tail
// are moved down here; the caller must move the out-of-flow content it owns (the
// root's floats) and the wrapper's height by the same growths — see applySplitGrowth.
func bucketBlocksGrowing(blocks []*Fragment, pageH, cbWidth float64, logf func(string, ...any)) ([]pageBucket, []splitGrowth) {
	if len(blocks) == 0 {
		return []pageBucket{{top: 0}}, nil
	}
	var grown []splitGrowth
	// queueGrownTail queues a split's tail and moves every later block down by the
	// tail's growth, so the block after a table whose header repeated starts below the
	// table's real bottom instead of over its last rows.
	queueGrownTail := func(work *[]*Fragment, i int, res splitResult) {
		queueTail(work, i, res.tail)
		if dy := totalGrowth(res.grow); dy > 0 {
			shiftFragments((*work)[i+2:], dy)
			grown = append(grown, res.grow...)
		}
	}
	// work is a mutable copy: a widows/orphans line split replaces the current block
	// with its head (placed) and inserts its tail as the next item to process (so a tail
	// that itself overflows splits again — iterative). prevBlock tracks the last block
	// placed (for the break-*: avoid pairwise keep), since indexing into a mutating
	// slice for blocks[bi-1] is unsafe.
	work := append([]*Fragment(nil), blocks...)
	var buckets []pageBucket
	var cur pageBucket
	var prevBlock *Fragment
	// noteSplit records the one pagination approximation a CSS filter suffers.
	// Filter brackets are PAGE-LOCAL (see layout.FilterPushKind): each page's slice
	// of a split box gets its own balanced push/pop. That is exact for a per-pixel
	// function, so those stay silent; a SPATIAL one (blur, drop-shadow) cannot
	// sample the content that fell on the other page, so its seam differs from an
	// unbroken render — logged once per document, since a document that splits one
	// blurred box usually splits several.
	spatialWarned := false
	noteSplit := func(head, tail *Fragment) {
		if spatialWarned || (!hasSpatialFilter(head) && !hasSpatialFilter(tail)) {
			return
		}
		spatialWarned = true
		logf("css pagination: a spatial CSS filter (blur/drop-shadow) was split across a page break; " +
			"each page's slice is filtered on its own, so the seam differs from an unbroken render")
	}
	for i := 0; i < len(work); i++ {
		b := work[i]
		forcedBefore, forcedAfter := effectiveBreaks(b)
		overflow := len(cur.blocks) > 0 && (b.Y+b.H)-cur.top > pageH
		// break-*: avoid keep-together (CSS Fragmentation §4.2): when an UNFORCED
		// (overflow) break would land between the previous block and b, but that join is
		// bound by break-after: avoid (on the previous block) or break-before: avoid (on
		// b), keep them together by carrying the previous block — AND any further
		// avoid-bound blocks immediately preceding it (the maximal contiguous avoid-bound
		// run ending at the previous block) — onto b's new page, provided the whole run
		// plus b then fits a page (else the avoid is dropped, logged, and the plain
		// overflow break stands). A forced break always wins over avoid (CSS), so this
		// applies only to overflow. The keep is CHAIN-AWARE: a run of length 1 is the
		// pairwise heading+paragraph case; a longer chain (e.g. heading + sub-heading +
		// paragraph) moves whole. At least one block is always left on the current page
		// (runStart >= 1), so this never empties the page it breaks from.
		if overflow && !forcedBefore && len(cur.blocks) >= 2 && prevBlock != nil && breakAvoidBetween(prevBlock, b) {
			// Collect the maximal contiguous run at the end of cur whose internal joins are
			// all avoid-bound AND whose last member is avoid-bound to b (already true).
			runStart := len(cur.blocks) - 1
			for runStart > 0 && breakAvoidBetween(cur.blocks[runStart-1], cur.blocks[runStart]) {
				runStart--
			}
			// Leave at least one block on the current page (else this becomes a plain move).
			if runStart >= 1 {
				run := cur.blocks[runStart:]
				if fitsRunWith(run, b, pageH) {
					moved := append([]*Fragment(nil), run...)
					cur.blocks = cur.blocks[:runStart]
					buckets = append(buckets, cur)
					cur = pageBucket{top: moved[0].Y - usedTopMargin(moved[0], cbWidth)}
					cur.blocks = append(cur.blocks, moved...)
					overflow = false
				} else {
					logf("css pagination: break-*: avoid chain could not be kept together (exceeds a page); breaking")
				}
			}
		}
		// Widows/orphans line splitting on an OVERFLOW of an already-occupied page (CSS
		// Fragmentation §4): when an unforced break would push a line-splittable block
		// whole to the next page but PART of it fits on the current page, split it at a
		// line boundary BEFORE closing the page (so the head fills the rest of this page).
		// splitBlockForPage honors widows/orphans and may decline (move whole), in which
		// case we fall through to the normal page-close + whole-block placement. A forced
		// break is not line-split.
		// A forced break carried by a NESTED (non-edge) descendant: split b at the break
		// position rather than at the page boundary. This runs before the overflow logic
		// because the author's break can sit well ABOVE the boundary — the page is not
		// full, it is simply ending here. An edge break needs no split; effectiveBreaks
		// has already propagated it to b itself.
		if lineSplittable(b) {
			if res, ok := splitAtForcedBreak(b); ok {
				noteSplit(res.head, res.tail)
				// b's own forced break (if any) closes the page before it first; the
				// nested break then ends the page b's head starts.
				if forcedBefore && len(cur.blocks) > 0 {
					buckets = append(buckets, cur)
					cur = pageBucket{}
				}
				if len(cur.blocks) == 0 {
					cur.top = b.Y
				}
				cur.blocks = append(cur.blocks, res.head)
				buckets = append(buckets, cur)
				cur = pageBucket{top: res.tail.Y}
				queueGrownTail(&work, i, res)
				prevBlock = res.head
				continue
			}
		}
		if overflow && !forcedBefore && len(cur.blocks) > 0 && lineSplittable(b) {
			res := splitAnyBlockForPage(b, cur.top+pageH, widowsOf(b), orphansOf(b))
			if res.head != nil && res.tail != nil {
				noteSplit(res.head, res.tail)
				cur.blocks = append(cur.blocks, res.head)
				buckets = append(buckets, cur)
				cur = pageBucket{top: res.tail.Y} // tail's top edge is suppressed → flush
				queueGrownTail(&work, i, res)
				prevBlock = res.head
				continue
			}
			// res.head==nil ⇒ move whole (widows/orphans can't be met): fall through.
		}
		// Close the current page only if it already has content: a forced-before on
		// the first block (cur empty) is a no-op, not a leading empty page.
		if (forcedBefore || overflow) && len(cur.blocks) > 0 {
			buckets = append(buckets, cur)
			cur = pageBucket{}
		}
		if len(cur.blocks) == 0 {
			// The page top determines where the page's first block lands once shifted to
			// local space. Reading b.Y (its BORDER-box top), not a provisional
			// previous-block bottom, keeps the top correct even when a margin gaps the
			// block below the prior one.
			//
			// At a FORCED break CSS truncates the leading margin, so the block lands flush
			// at the page top: cur.top = b.Y. At an UNFORCED/overflow break CSS RETAINS the
			// block's own leading margin as whitespace at the top of the new page, so we
			// pull the page top up by that margin (cur.top = b.Y - mT) and the block lands
			// at local Y == its top margin. (The very first page has no preceding break —
			// forcedBefore is false and cur is empty from the start — so it takes the
			// b.Y branch via the !startsWithForcedBreak path; its body-decoration top is
			// handled separately by wrapperDecorationTop.)
			cur.top = b.Y
			if overflow && !forcedBefore {
				cur.top = b.Y - usedTopMargin(b, cbWidth)
			}
		}
		// Widows/orphans line splitting of a block LEADING a fresh page that is itself
		// taller than a whole page (the iterative case — a tail that still overflows, or a
		// paragraph taller than a page). The head fills this page; the tail is queued.
		// A forced break-before does not stop this split: it has already been honored by
		// closing the previous page (or is a no-op on the first block), and its tail
		// keeps the break-before style, so excluding forced blocks here left every page
		// after the first overflowing and clipped.
		if len(cur.blocks) == 0 && lineSplittable(b) && (b.Y+b.H)-cur.top > pageH {
			res := splitAnyBlockForPage(b, cur.top+pageH, widowsOf(b), orphansOf(b))
			if res.head != nil && res.tail != nil {
				noteSplit(res.head, res.tail)
				cur.blocks = append(cur.blocks, res.head)
				buckets = append(buckets, cur)
				cur = pageBucket{top: res.tail.Y}
				queueGrownTail(&work, i, res)
				prevBlock = res.head
				continue
			}
			// res.head==nil (can't satisfy minimums while leading the page): place whole,
			// it overflows.
		}
		// A block taller than a page that could not be (line-)split overflows its page
		// (clipped by the bitmap) — the documented degradation.
		if len(cur.blocks) == 0 && b.H > pageH {
			logf("css pagination: block taller than page (%.0fpt > %.0fpt); overflowing, not splitting", b.H, pageH)
		}
		cur.blocks = append(cur.blocks, b)
		prevBlock = b
		if forcedAfter {
			buckets = append(buckets, cur)
			cur = pageBucket{}
		}
	}
	// Append the final page if it has content. After a forced-after on the last
	// block, cur is empty, so it is not appended (no trailing empty page).
	if len(cur.blocks) > 0 {
		buckets = append(buckets, cur)
	}
	if len(buckets) == 0 {
		buckets = append(buckets, pageBucket{top: 0})
	}
	return buckets, grown
}

// applySplitGrowth moves the content outside the bucketed blocks by the split-tail
// growths bucketBlocksGrowing reported: each float at or below a growth's Y moves down
// by its dy, and each wrapper (html, body) grows by the total so its bottom border and
// background still close below the last block. Growths are applied in order, because
// each one's Y is in the frame left by the growths before it.
func applySplitGrowth(grown []splitGrowth, floats []*Fragment, wrappers ...*Fragment) {
	for _, g := range grown {
		for _, fl := range floats {
			if fl.Y >= g.at-0.5 {
				translateFragment(fl, 0, g.dy)
			}
		}
		for _, w := range wrappers {
			if w != nil {
				w.H += g.dy
			}
		}
	}
}

// queueTail inserts a split block's tail into the work slice at index i+1 so the
// bucketing loop processes it as the very next block (it leads the next page and may
// split again — making the splitter iterative across an arbitrary number of pages).
func queueTail(work *[]*Fragment, i int, tail *Fragment) {
	*work = append(*work, nil)
	copy((*work)[i+2:], (*work)[i+1:])
	(*work)[i+1] = tail
}

// widowsOf / orphansOf read a block fragment's widows / orphans count (CSS, inherited,
// initial 2). A nil Box reads as the initial 2 so a synthetic block still gets the
// default protection.
func widowsOf(f *Fragment) int {
	if f == nil || f.Box == nil || f.Box.Style.Widows < 1 {
		return 2
	}
	return f.Box.Style.Widows
}

func orphansOf(f *Fragment) int {
	if f == nil || f.Box == nil || f.Box.Style.Orphans < 1 {
		return 2
	}
	return f.Box.Style.Orphans
}

// wrapperDecorationTop returns f's border-box top (f.Y) when f paints a decoration that
// extends above the page's first content — a non-transparent background or any drawn
// border edge — so the first page's top is pulled up to include it; otherwise it returns
// fallback unchanged (an undecorated wrapper must not move the page top, preserving the
// byte-identical un-bordered case). A nil fragment returns fallback.
func wrapperDecorationTop(f *Fragment, fallback float64) float64 {
	if f == nil {
		return fallback
	}
	if f.Background.A > 0 {
		return f.Y
	}
	for _, e := range f.Border {
		if e.Width > 0 && e.Style != layout.BorderNone {
			return f.Y
		}
	}
	return fallback
}

// breakAvoidBetween reports whether a break between block prev and block b is
// discouraged by an avoid hint: break-after: avoid / avoid-page on prev, or
// break-before: avoid / avoid-page on b (CSS Fragmentation §4.2). The bucketer uses it
// to keep the pair on one page when an unforced break would otherwise split them.
func breakAvoidBetween(prev, b *Fragment) bool {
	return isAvoidBreak(breakAfter(prev)) || isAvoidBreak(breakBefore(b))
}

// isAvoidBreak reports whether a break-before/after value asks to avoid a break here
// ("avoid" / "avoid-page"). Other values (forced, auto, "") are not avoid.
func isAvoidBreak(v string) bool {
	return v == "avoid" || v == "avoid-page"
}

// fitsRunWith reports whether a contiguous run of blocks plus a trailing block b fits on
// one pageH-tall page (first block's top to b's bottom).
func fitsRunWith(run []*Fragment, b *Fragment, pageH float64) bool {
	if len(run) == 0 {
		return b.H <= pageH
	}
	return (b.Y+b.H)-run[0].Y <= pageH
}

// keptInsideAvoid reports whether a block carries break-inside: avoid / avoid-page,
// asking the paginator to keep its content on one page (so the line-level splitter
// must not split it). A nil Box reads as not-avoid.
func keptInsideAvoid(f *Fragment) bool {
	if f == nil || f.Box == nil {
		return false
	}
	return isAvoidBreak(f.Box.Style.BreakInside)
}

// effectiveBreaks returns a top-level block's effective forced break-before / break-after,
// folding a NESTED forced break that sits at the block's leading or trailing edge into the
// block itself. A browser forces a break on the ancestor when the break point falls at the
// start/end of its content, so:
//   - a forced break-before on the block, OR on content at the START of the block (the
//     leftmost in-flow descendant spine), yields forcedBefore;
//   - a forced break-after on the block, OR on content at the END of the block (the
//     rightmost in-flow descendant spine), yields forcedAfter.
//
// A forced break in the MIDDLE of the block (neither the leading nor trailing edge) is NOT
// reflected here, because it cannot be expressed as a break BETWEEN top-level blocks: the
// bucketer instead splits the block at the break position (see splitAtForcedBreak).
func effectiveBreaks(b *Fragment) (forcedBefore, forcedAfter bool) {
	forcedBefore = isForcedBreak(breakBefore(b)) || leadingEdgeForcedBreakBefore(b)
	forcedAfter = isForcedBreak(breakAfter(b)) || trailingEdgeForcedBreakAfter(b)
	return forcedBefore, forcedAfter
}

// leadingEdgeForcedBreakBefore reports whether a forced break-before sits on the leftmost
// in-flow descendant spine of b — i.e. on content that begins b, so the break is at b's
// leading edge. It descends into the FIRST in-flow child at each level (skipping out-of-flow
// positioned/float children, which do not begin in-flow content), checking break-before on
// each spine node below b. (b's own break-before is handled by the caller.)
func leadingEdgeForcedBreakBefore(b *Fragment) bool {
	node := b
	for {
		first := firstInFlowChild(node)
		if first == nil {
			return false
		}
		if isForcedBreak(breakBefore(first)) {
			return true
		}
		node = first
	}
}

// trailingEdgeForcedBreakAfter reports whether a forced break-after sits on the rightmost
// in-flow descendant spine of b — content that ends b, so the break is at b's trailing edge.
// Mirror of leadingEdgeForcedBreakBefore over the LAST in-flow child at each level.
func trailingEdgeForcedBreakAfter(b *Fragment) bool {
	node := b
	for {
		last := lastInFlowChild(node)
		if last == nil {
			return false
		}
		if isForcedBreak(breakAfter(last)) {
			return true
		}
		node = last
	}
}

// firstInFlowChild returns f's first in-flow child fragment (skipping out-of-flow
// positioned and floated children), or nil if there is none.
func firstInFlowChild(f *Fragment) *Fragment {
	for _, c := range f.Children {
		if c.IsPositioned || c.IsFloat {
			continue
		}
		return c
	}
	return nil
}

// lastInFlowChild returns f's last in-flow child fragment (skipping out-of-flow positioned
// and floated children), or nil if there is none.
func lastInFlowChild(f *Fragment) *Fragment {
	for i := len(f.Children) - 1; i >= 0; i-- {
		c := f.Children[i]
		if c.IsPositioned || c.IsFloat {
			continue
		}
		return c
	}
	return nil
}

// contentWidth returns f's content-box width (its border-box W minus its own left/right
// border + padding), the containing-block width its in-flow children's horizontal
// metrics resolve against. cbWidth is the basis for resolving f's own percentage
// edges. A nil f or nil Box returns f.W (or 0) unchanged.
func contentWidth(f *Fragment, cbWidth float64) float64 {
	if f == nil {
		return 0
	}
	if f.Box == nil {
		return f.W
	}
	ed := usedEdges(f.Box, cbWidth)
	w := f.W - ed.bL - ed.bR - ed.pL - ed.pR
	if w < 0 {
		w = 0
	}
	return w
}

// usedTopMargin returns a top-level block fragment's own used top margin, resolved
// against its containing-block width cbWidth (the body content width). Returns 0 for a
// fragment with no Box. A negative margin returns its value (it pulls the block up,
// matching layout); the caller clamps page geometry as needed.
func usedTopMargin(b *Fragment, cbWidth float64) float64 {
	if b == nil || b.Box == nil {
		return 0
	}
	return usedEdges(b.Box, cbWidth).mT
}

// isForcedBreak reports whether a break-before / break-after value forces a page
// break. "page" / "always" (and the named page-side values left / right / recto /
// verso, treated as a plain forced break — this model has no left/right page
// distinction) are forced; "auto" / "avoid" / "avoid-page" / "" / anything else is
// not.
func isForcedBreak(v string) bool {
	switch v {
	case "page", "always", "left", "right", "recto", "verso":
		return true
	}
	return false
}

// bodyFragment returns the body fragment — the document's top-level block container
// — which is the root fragment's last child (x/net/html always synthesizes
// <html><body>; a leading <style> is display:none and not emitted). Returns nil for
// a nil or childless root.
func bodyFragment(root *Fragment) *Fragment {
	if root == nil || len(root.Children) == 0 {
		return nil
	}
	return root.Children[len(root.Children)-1]
}

// breakBefore reads a fragment's break-before hint (the legacy page-break-before
// folds onto the same style field). A nil fragment or nil Box reads as "" (no
// forced break).
func breakBefore(f *Fragment) string {
	if f == nil || f.Box == nil {
		return ""
	}
	return f.Box.Style.BreakBefore
}

// breakAfter reads a fragment's break-after hint (the legacy page-break-after folds
// onto the same style field). A nil fragment or nil Box reads as "" (no forced
// break).
func breakAfter(f *Fragment) string {
	if f == nil || f.Box == nil {
		return ""
	}
	return f.Box.Style.BreakAfter
}
