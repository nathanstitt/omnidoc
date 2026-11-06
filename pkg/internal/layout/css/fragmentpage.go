package css

import (
	"github.com/nathanstitt/omnidoc/pkg/internal/layout"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/cssbox"
)

// lineSplittable reports whether a top-level block fragment can be fragmented for
// pagination: it must carry no break-inside: avoid, and EITHER establish an inline
// formatting context with at least two lines (a pure-inline paragraph, line-split by
// splitBlockForPage), hold at least one in-flow block child (a mixed block+inline
// container, split at a child boundary by splitMixedBlock), be a table (split between rows
// by splitTableForPage), or be a flex/grid container (split between item rows by
// splitFlexGridForPage). Floats/positioned children are out of flow and do not by
// themselves make a block splittable. break-inside: avoid disqualifies every shape.
func lineSplittable(b *Fragment) bool {
	if b == nil || keptInsideAvoid(b) {
		return false
	}
	return len(b.Lines) >= 2 || hasInFlowBlockChild(b) || isTableFragment(b) || isFlexOrGridFragment(b)
}

// isTableFragment reports whether f is a table fragment (its box is display:table), so the
// bucketer routes it to the between-rows table splitter.
func isTableFragment(f *Fragment) bool {
	return f != nil && f.Box != nil && f.Box.Display == cssbox.DisplayTable
}

// splitAnyBlockForPage splits b for the page, choosing the splitter by b's content shape:
// a table breaks between rows, a column-flex/grid breaks between item rows, a block with
// in-flow block children breaks at child boundaries, and a pure-inline block line-splits.
//
// Whichever splitter runs, the two fragments are detached from each other's shared
// mutable state before returning — see detachSharedExtras. Doing it here, at the single
// dispatch point, rather than in each splitter means a future splitter cannot forget.
func splitAnyBlockForPage(b *Fragment, pageBottom float64, widows, orphans int) splitResult {
	res := splitOneBlockForPage(b, pageBottom, widows, orphans)
	// Only a genuine split (both sides present) aliases anything: a "moved whole" or
	// "fits whole" result hands back the original pointer, which must not be touched.
	if res.head != nil && res.tail != nil {
		detachSharedExtras(res.head)
		detachSharedExtras(res.tail)
		clampClipToFragment(res.head)
		clampClipToFragment(res.tail)
	}
	return res
}

// clampClipToFragment narrows a clipping fragment's clip rect to its own vertical
// extent after a split.
//
// ClipRect is a value, so the two halves do not alias — but each inherits the WHOLE
// original box's rect. A split `overflow: hidden` block would then clip to the full
// pre-split height on both pages, letting content that belongs to the other fragment
// paint through on whichever page it lands.
//
// Only the vertical extent is narrowed: a page break divides a box along the block
// axis, so the horizontal clip is unchanged.
func clampClipToFragment(f *Fragment) {
	if !f.Clips {
		return
	}
	top, bottom := f.Y, f.Y+f.H
	if f.ClipRect.y < top {
		f.ClipRect.h -= top - f.ClipRect.y
		f.ClipRect.y = top
	}
	if end := f.ClipRect.y + f.ClipRect.h; end > bottom {
		f.ClipRect.h -= end - bottom
	}
	if f.ClipRect.h < 0 {
		f.ClipRect.h = 0
	}
}

// splitAtForcedBreak splits b at a forced break carried by a nested (non-edge)
// descendant, returning ok=false when b has no such break or the split declines.
//
// It differs from the page-boundary path in WHERE it splits: a page break happens
// where the content runs out of room, but a forced break happens where the author
// said, which may be well above the boundary. Everything else — the recursive descent,
// edge suppression, out-of-flow routing, shared-state detach — is the same machinery,
// so this is a thin wrapper that supplies a different split Y.
//
// widows/orphans are deliberately NOT applied: they express a preference about how
// many lines may be stranded, and a forced break is not a preference.
func splitAtForcedBreak(b *Fragment) (splitResult, bool) {
	y, ok := midBlockForcedBreakY(b)
	if !ok {
		return splitResult{}, false
	}
	// The split Y is the break position; a fragment ending exactly there belongs to the
	// head, which is what the splitters' `<= splitY+0.5` tests already express.
	res := splitAnyBlockForPage(b, y-0.5, 0, 0)
	if res.head == nil || res.tail == nil {
		return splitResult{}, false
	}
	return res, true
}

// splitOneBlockForPage is splitAnyBlockForPage's shape dispatch, without the
// shared-state detach.
func splitOneBlockForPage(b *Fragment, pageBottom float64, widows, orphans int) splitResult {
	if isTableFragment(b) {
		return splitTableForPage(b, pageBottom)
	}
	if isFlexOrGridFragment(b) {
		return splitFlexGridForPage(b, pageBottom)
	}
	if hasInFlowBlockChild(b) {
		return splitMixedBlock(b, pageBottom, widows, orphans)
	}
	return splitBlockForPage(b, pageBottom, widows, orphans)
}

// midBlockForcedBreakY returns the page-space Y at which a forced break INSIDE b
// requires a split, and ok=false when b carries no such break.
//
// It looks for a `break-before`/`break-after` on a descendant that is neither at b's
// leading nor trailing edge — an edge break is already propagated to b itself by
// effectiveBreaks and handled by the bucketer. A break strictly inside b has nowhere
// to go without splitting b, which is why it used to be dropped with a log.
//
// The FIRST such break in document order wins: splitting there leaves any later break
// in the tail, which re-enters the bucketer and is honored on the next pass.
func midBlockForcedBreakY(b *Fragment) (float64, bool) {
	if b == nil {
		return 0, false
	}
	best, found := 0.0, false
	// consider records a candidate split Y, keeping the topmost.
	consider := func(y float64) {
		if !found || y < best {
			best, found = y, true
		}
	}
	var walk func(f *Fragment, depth int)
	walk = func(f *Fragment, depth int) {
		for _, c := range f.Children {
			if c.IsFloat || c.IsPositioned {
				continue // out-of-flow content does not force a break in the flow
			}
			// depth 0 children are b's own children: a break-before on the FIRST of
			// them is b's leading edge, and a break-after on the LAST is its trailing
			// edge. Both are already propagated to b, so only deeper or interior
			// breaks are "mid-block".
			if isForcedBreak(breakBefore(c)) && !isLeadingEdgeOf(b, c) {
				consider(c.Y)
			}
			if isForcedBreak(breakAfter(c)) && !isTrailingEdgeOf(b, c) {
				consider(c.Y + c.H)
			}
			walk(c, depth+1)
		}
	}
	walk(b, 0)
	// A split at b's own top or bottom edge is not a split at all.
	if found && (best <= b.Y+0.5 || best >= b.Y+b.H-0.5) {
		return 0, false
	}
	return best, found
}

// isLeadingEdgeOf reports whether c begins b — it is reachable by descending into the
// first in-flow child at every level. A break-before there is b's own leading break.
func isLeadingEdgeOf(b, c *Fragment) bool {
	for node := b; node != nil; node = firstInFlowChild(node) {
		if node == c {
			return true
		}
	}
	return false
}

// isTrailingEdgeOf reports whether c ends b — reachable by descending into the last
// in-flow child at every level. A break-after there is b's own trailing break.
func isTrailingEdgeOf(b, c *Fragment) bool {
	for node := b; node != nil; node = lastInFlowChild(node) {
		if node == c {
			return true
		}
	}
	return false
}

// isFlexOrGridFragment reports whether f is a flex or grid container fragment (its box is
// display:flex or display:grid), so the bucketer routes it to the between-item-rows
// splitter. inline-flex/inline-grid containers flow as inline atoms (not top-level blocks),
// so they are not bucketed and need not be matched here.
func isFlexOrGridFragment(f *Fragment) bool {
	return f != nil && f.Box != nil && (f.Box.Display == cssbox.DisplayFlex || f.Box.Display == cssbox.DisplayGrid)
}

// hasInFlowBlockChild reports whether f has at least one in-flow (non-float,
// non-positioned) child fragment — i.e. a block-level child interleaved with f's content.
func hasInFlowBlockChild(f *Fragment) bool {
	for _, c := range f.Children {
		if !c.IsFloat && !c.IsPositioned {
			return true
		}
	}
	return false
}

// splitMixedBlock splits a block that holds in-flow block children at a CHILD boundary:
// children whose bottom is fully above pageBottom stay in the head; the rest go to the
// tail (its Y moved to the first kept child, top border suppressed). A child straddling
// the boundary is NOT recursively split here (it rides the tail whole). widows/orphans
// apply to the parent's own lines (rare in a mixed block; not separately enforced).
// Returns {head:parent} if all children fit, {tail:parent} if none fit.
func splitMixedBlock(parent *Fragment, pageBottom float64, widows, orphans int) splitResult {
	inflow := inFlowChildren(parent)
	if len(inflow) == 0 {
		return splitBlockForPage(parent, pageBottom, widows, orphans) // pure-inline fallback
	}
	k := 0
	for i, c := range inflow {
		if c.Y+c.H <= pageBottom+0.5 {
			k = i + 1
		} else {
			break
		}
	}
	if k >= len(inflow) {
		return splitResult{head: parent}
	}

	// The child at k straddles the boundary. Split it too, so the part that fits stays
	// on this page instead of the whole child riding the tail and leaving a gap the
	// height of its above-boundary portion.
	//
	// This is the recursive step: splitAnyBlockForPage works at any tree depth because
	// pageBottom is in absolute page space, and the whole fragment tree shares one
	// coordinate system. lineSplittable gates it — it is a shape predicate with no
	// top-level assumption, and it already refuses a `break-inside: avoid` box, which is
	// exactly the stop condition CSS requires.
	var childHead, childTail *Fragment
	var childGrow []splitGrowth
	if straddler := inflow[k]; lineSplittable(straddler) {
		if sub := splitAnyBlockForPage(straddler, pageBottom, widows, orphans); sub.head != nil && sub.tail != nil {
			childHead, childTail, childGrow = sub.head, sub.tail, sub.grow
		}
	}

	if k == 0 && childHead == nil {
		return splitResult{tail: parent}
	}

	head := *parent
	tail := *parent
	head.Children = append([]*Fragment(nil), inflow[:k]...)
	tail.Children = append([]*Fragment(nil), inflow[k:]...)
	if childHead != nil {
		// The straddler's head joins this page; its tail replaces it at the front of the
		// next page's children.
		head.Children = append(head.Children, childHead)
		tail.Children[0] = childTail
	}
	head.H = lastChildBottom(head.Children) - parent.Y
	tail.Y = firstChildTop(tail.Children)
	tail.H = (parent.Y + parent.H) - tail.Y
	head.Border[layout.EdgeBottom] = BorderEdge{}
	tail.Border[layout.EdgeTop] = BorderEdge{}
	// The child lists above were rebuilt from the IN-FLOW children only, so the
	// out-of-flow ones must be put back or they vanish from the document entirely.
	// They are routed by the PAGE BOUNDARY, not by tail.Y: the tail starts at its first
	// in-flow child, which can be well below the boundary, and an out-of-flow box in
	// that gap belongs to the next page rather than overflowing the current one.
	distributeOutOfFlow(parent, &head, &tail, pageBottom)
	// A straddler whose tail grew (a repeated table header) pushes down the children
	// that follow it and makes this tail taller by the same amount.
	if dy := totalGrowth(childGrow); dy > 0 {
		for _, c := range tail.Children[1:] {
			if c.Y >= childGrow[0].at-0.5 {
				translateFragment(c, 0, dy)
			}
		}
		tail.H += dy
	}
	return splitResult{head: &head, tail: &tail, grow: childGrow}
}

// lastChildBottom returns the largest bottom edge among the IN-FLOW children in list,
// or 0 when there are none. It sizes a split head: the head ends at its last in-flow
// child's bottom, not at the page boundary, so a box that ended early does not gain
// phantom height.
//
// Out-of-flow children are excluded deliberately — a float or positioned box does not
// contribute to its parent's block-axis extent.
func lastChildBottom(list []*Fragment) float64 {
	bottom := 0.0
	for _, c := range list {
		if c.IsFloat || c.IsPositioned {
			continue
		}
		if b := c.Y + c.H; b > bottom {
			bottom = b
		}
	}
	return bottom
}

// firstChildTop returns the smallest top edge among the IN-FLOW children in list, or 0
// when there are none. It positions a split tail at its first in-flow child.
func firstChildTop(list []*Fragment) float64 {
	top := 0.0
	found := false
	for _, c := range list {
		if c.IsFloat || c.IsPositioned {
			continue
		}
		if !found || c.Y < top {
			top, found = c.Y, true
		}
	}
	return top
}

// inFlowChildren returns f's in-flow (non-float, non-positioned) child fragments.
func inFlowChildren(f *Fragment) []*Fragment {
	var out []*Fragment
	for _, c := range f.Children {
		if !c.IsFloat && !c.IsPositioned {
			out = append(out, c)
		}
	}
	return out
}

// detachSharedExtras deep-copies the per-fragment state that a shallow clone would
// otherwise SHARE between a split's head and tail.
//
// Splitting is `head := *b; tail := *b`, which copies pointers and slice headers. Two
// fragments then reference one BgImage struct and one ClipChain backing array — and
// shiftFragmentExtras (block.go) moves a fragment to its page's local frame by mutating
// exactly those IN PLACE. So shifting the head also moves the tail's background origin,
// by the head page's offset, on top of the tail's own shift. The continuation page
// paints its background in the wrong place.
//
// Called on both fragments after every split. A fragment with neither a background
// image nor a clip chain (the common case) allocates nothing.
func detachSharedExtras(f *Fragment) {
	if f.BgImage != nil {
		bg := *f.BgImage
		f.BgImage = &bg
	}
	if len(f.PositionedInfo) > 0 {
		pi := append([]PositionedInfo(nil), f.PositionedInfo...)
		for i := range pi {
			if len(pi[i].ClipChain) > 0 {
				pi[i].ClipChain = append([]rect(nil), pi[i].ClipChain...)
			}
		}
		f.PositionedInfo = pi
	}
	if len(f.Collapsed) > 0 {
		f.Collapsed = append([]layout.BorderItem(nil), f.Collapsed...)
	}
}

// distributeOutOfFlow assigns each of parent's out-of-flow children (floats and
// positioned boxes) to the head or tail fragment by which side of the split they sit
// on, appending to the respective child lists.
//
// This exists because a split that rebuilds its child lists from inFlowChildren would
// otherwise DROP every out-of-flow child from both fragments — the content simply
// disappears from the document. A float or absolutely-positioned box is still painted
// content; it belongs on whichever page its geometry puts it.
//
// A child straddling the boundary rides the TAIL whole, matching how a straddling
// in-flow child is handled: better to push it down intact than to clip it.
func distributeOutOfFlow(parent, head, tail *Fragment, splitY float64) {
	for _, c := range parent.Children {
		if !c.IsFloat && !c.IsPositioned {
			continue // in-flow children are partitioned by the caller
		}
		if head != nil && c.Y+c.H <= splitY+0.5 {
			head.Children = append(head.Children, c)
			continue
		}
		if tail != nil {
			tail.Children = append(tail.Children, c)
		}
	}
}

// splitResult is the outcome of attempting to split a block across a page boundary.
type splitResult struct {
	head *Fragment // the part staying on the current page (nil if the block moves whole)
	tail *Fragment // the part flowing to the next page (nil if the whole block fits)
	// grow lists where the tail became taller than the part of the block it continues
	// (a repeated table header), innermost first. Everything laid out below each
	// growth's at — later siblings, later blocks, and the floats among them — must move
	// down by its dy, or it is drawn over the end of the grown tail.
	grow []splitGrowth
}

// splitGrowth is one block-axis growth of a split tail: the content at or below the
// page-space Y at (the original bottom of the box that grew) moves down by dy.
type splitGrowth struct {
	at, dy float64
}

// growthAt reports a tail growth of dy below f's original bottom, or none for dy <= 0.
func growthAt(f *Fragment, dy float64) []splitGrowth {
	if dy <= 0 {
		return nil
	}
	return []splitGrowth{{at: f.Y + f.H, dy: dy}}
}

// totalGrowth sums the growths' heights.
func totalGrowth(grow []splitGrowth) float64 {
	total := 0.0
	for _, g := range grow {
		total += g.dy
	}
	return total
}

// splitBlockForPage decides how a line-splittable block b is placed when the current
// page's content bottom is pageBottom (page space). It honors CSS widows/orphans:
//
//   - If every line fits (b's bottom ≤ pageBottom), it does not split: {head: b}.
//   - It finds k = the number of leading lines that fit above pageBottom, then clamps k
//     by orphans (≥ orphans lines must remain on this page) and widows (≥ widows lines
//     must carry to the next page). If the clamps cannot both be met (the block is too
//     short, n < widows+orphans, or fewer than orphans lines fit), the block moves whole
//     to the next page: {tail: b}.
//   - Otherwise it splits into head (lines [0,k)) and tail (lines [k,n)), each a shallow
//     clone of b with its line subset, the split-side border/padding suppressed, and the
//     tail's lines/height shifted so the tail's content starts at the tail block's top.
//
// b must be lineSplittable. The split is page-space only (it partitions Lines and clones
// the Fragment struct, sharing glyph outlines — read-only *render.Path); no relayout.
func splitBlockForPage(b *Fragment, pageBottom float64, widows, orphans int) splitResult {
	n := len(b.Lines)
	ed := blockEdges(b)
	contentTop := b.Y + ed.bT + ed.pT
	lh := lineHeightOf(b)

	// k = lines whose bottom fits above pageBottom. Line i occupies
	// [contentTop+i*lh, contentTop+(i+1)*lh] (uniform line height — the common case).
	k := 0
	for i := 0; i < n; i++ {
		lineBottom := contentTop + float64(i+1)*lh
		if lineBottom <= pageBottom+0.5 { // small tolerance for fp
			k = i + 1
		} else {
			break
		}
	}
	if k >= n {
		return splitResult{head: b} // everything fits; no split
	}

	if widows < 1 {
		widows = 1
	}
	if orphans < 1 {
		orphans = 1
	}
	// Widows: the tail (n-k lines) must have ≥ widows lines; pull lines back if needed.
	if n-k < widows {
		k = n - widows
	}
	// Orphans: the head (k lines) must have ≥ orphans lines. If that is impossible
	// (k < orphans after the widows clamp, or the block can't satisfy both because
	// n < widows+orphans), move the whole block to the next page.
	if k < orphans {
		return splitResult{tail: b}
	}

	head := splitHead(b, k, ed, lh)
	tail := splitTail(b, k, ed, lh)
	return splitResult{head: head, tail: tail}
}

// splitHead builds the head fragment: a shallow clone of b keeping lines [0,k), its
// height shrunk to end just below line k-1, and its BOTTOM border/padding suppressed
// (CSS box-decoration-break: slice — a box split across a break does not repeat the
// break-side edge). The head stays on the current page (its Y is unchanged).
func splitHead(b *Fragment, k int, ed edges, lh float64) *Fragment {
	h := *b
	h.Lines = append([]LineFragment(nil), b.Lines[:k]...)
	// New border-box height: top edge + k lines of content + bottom edge, but with the
	// bottom border/padding suppressed (slice), so just top edge + k*lh.
	h.H = ed.bT + ed.pT + float64(k)*lh
	h.Border[layout.EdgeBottom] = BorderEdge{} // suppress the split-side edge
	// The head has no children of its own beyond out-of-flow ones; an out-of-flow child
	// (float/abs) stays with the head (it was positioned in this block's space). Keep
	// Children as-is — they are rare on a paragraph and ride the head page.
	return &h
}

// splitTail builds the tail fragment: a shallow clone of b keeping lines [k,n) AT THEIR
// ORIGINAL page-space positions, its TOP border/padding suppressed, and its border-box
// Y moved DOWN to the top of the first kept line so the fragment invariant (lines sit at
// Y + topEdge + i*lh + ascent) still holds. The kept lines are NOT moved — only Y and H
// change — so a recursive re-split of the tail (when it itself overflows the next page)
// recomputes a consistent contentTop. The bucketer shifts the whole tail to the next
// page's local frame via the usual per-page shift.
func splitTail(b *Fragment, k int, ed edges, lh float64) *Fragment {
	tl := *b
	n := len(b.Lines)
	tl.Lines = append([]LineFragment(nil), b.Lines[k:]...)
	m := n - k // tail line count
	// The first kept line's band top is the original content top + k*lh.
	firstKeptTop := b.Y + ed.bT + ed.pT + float64(k)*lh
	// With the top edge suppressed, the tail's content box starts at its border-box top
	// plus only its padding-top, so set Y so content top == firstKeptTop.
	tl.Y = firstKeptTop - ed.pT
	// Tail border box: top edge SUPPRESSED (slice) — no bT/pT above the content — then
	// m lines, then the original bottom edge.
	tl.H = ed.pT + float64(m)*lh + ed.pB + ed.bB
	tl.Border[layout.EdgeTop] = BorderEdge{} // suppress the split-side edge
	return &tl
}

// blockEdges returns a block fragment's resolved top/bottom border + padding edges,
// reading them from its source Box (the border widths are also on the fragment, but
// padding is not, so we resolve from the Box for both to stay consistent). A nil Box
// yields zero edges (an anonymous block — its content top is its border-box top).
func blockEdges(b *Fragment) edges {
	if b == nil || b.Box == nil {
		return edges{}
	}
	// The block's edges resolve against its containing-block width; for top/bottom
	// border+padding only the box's own values matter (percentages on vertical padding
	// resolve against the CB WIDTH, but a paragraph rarely uses %; pass the block's own
	// width as a reasonable basis). usedEdges needs a CB width — use b.W (close enough
	// for the vertical edges in the common case).
	return usedEdges(b.Box, b.W)
}

// lineHeightOf returns the uniform line-height (baseline-to-baseline spacing) of a
// multi-line block: the delta between its first two line baselines. For a 1-line block
// (not line-split) it falls back to the block's content height. This is exact for the
// common uniform-line-height paragraph; a block with mixed per-line heights uses the
// first delta as an approximation (documented).
func lineHeightOf(b *Fragment) float64 {
	if len(b.Lines) >= 2 {
		lh := b.Lines[1].BaselineY - b.Lines[0].BaselineY
		if lh > 0 {
			return lh
		}
	}
	ed := blockEdges(b)
	h := b.H - ed.bT - ed.bB - ed.pT - ed.pB
	if h > 0 {
		return h
	}
	return b.H
}
