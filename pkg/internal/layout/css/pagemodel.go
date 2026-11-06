package css

import (
	"context"
	"math"

	gcss "github.com/nathanstitt/omnidoc/pkg/internal/css"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/cssbox"
)

// defaultMarkInset is the bleed-band width synthesized when @page marks are requested
// without an explicit bleed, so the marks have room outside the trim box (~6pt).
const defaultMarkInset = 8 // px-as-pt (≈6pt)

// PagedConfig is the resolved paged-media configuration handed to LayoutPagedDoc by
// the omnidoc backend: whether to paginate at all, a fallback page size (from
// WithPageSize / the Letter default), an explicit-size flag (so an API size overrides
// an @page size), and the parsed @page rules (for size, margins, and margin boxes).
//
// The fields combine like CSS Paged Media + the API override rule:
//   - Paged false  → a single tall page (LayoutPagedDoc delegates to Layout).
//   - FallbackW/H  → the page size when no @page `size` applies (the WithPageSize
//     value, else Letter).
//   - ExplicitSize → the caller passed WithPageSize: its size wins over any @page
//     `size` (but @page margins/margin-boxes still apply).
//   - Pages        → the document's @page rules (may be empty).
type PagedConfig struct {
	Paged        bool
	FallbackW    float64
	FallbackH    float64
	ExplicitSize bool
	Pages        gcss.Stylesheet
	// Running holds the document's out-of-flow running elements (CSS GCPM
	// position:running(name)), keyed by name. A @page margin box whose content is
	// element(name) re-paints Running[name] on every page. Empty/nil when no element
	// uses running() — the byte-identical path (element() never fires).
	Running map[string]*cssbox.Box
}

// pageGeom is one page's resolved geometry: the full page rect size and the content
// box (origin + size) the page's blocks occupy after the @page margins are applied.
// The margin boxes (running headers/footers) are resolved separately at flatten time.
type pageGeom struct {
	pageW, pageH       float64 // full page size
	marginL, marginT   float64 // content-box origin offset (left/top margins)
	contentW, contentH float64 // content box size (page minus margins)
	used               gcss.UsedPage
	bleed              float64 // @page bleed: the trim→media-box inset on each side (0 = none)
}

// mediaW / mediaH are the page BITMAP size: the trim box (pageW/pageH) plus bleed on
// each side. With no bleed they equal pageW/pageH (byte-identical).
func (g pageGeom) mediaW() float64 { return g.pageW + 2*g.bleed }
func (g pageGeom) mediaH() float64 { return g.pageH + 2*g.bleed }

// resolvePageGeom resolves the geometry for page index i (named `name`) from a
// PagedConfig: the page size (API explicit size > @page size > fallback) and the
// content box after @page margins. blank marks an empty (forced-break) page.
func (pc PagedConfig) resolvePageGeom(i int, name string, blank bool) pageGeom {
	up := pc.Pages.ResolvePage(i, name, blank)
	g := pageGeom{pageW: pc.FallbackW, pageH: pc.FallbackH, used: up}
	// An explicit API size (WithPageSize) overrides the DEFAULT (unnamed) @page size,
	// but a section that opted into a NAMED page (page: <name>) must still get that
	// page's own @page size — otherwise a `page: landscape` section can never reflow
	// wider. So apply a named page's size unconditionally, and the unnamed page's size
	// only when the API did not pin one. "A named page's size" is the size the CSS
	// cascade RESOLVED for that page, which may be INHERITED from an unnamed @page rule
	// (a named lookup folds in unnamed @page declarations — see page.go matchingPageRules),
	// not only a size the named rule declared itself — this is cascade-consistent and
	// intended (a named section inherits the unnamed @page size even under ExplicitSize).
	if up.HasSize && (name != "" || !pc.ExplicitSize) {
		g.pageW, g.pageH = up.WidthPt, up.HeightPt
	}
	if up.HasRule {
		g.marginL, g.marginT = up.MarginLeft, up.MarginTop
		g.contentW = g.pageW - up.MarginLeft - up.MarginRight
		g.contentH = g.pageH - up.MarginTop - up.MarginBottom
	}
	if g.contentW <= 0 {
		g.contentW = g.pageW
	}
	if g.contentH <= 0 {
		g.contentH = g.pageH
	}
	g.bleed = up.Bleed
	if g.bleed == 0 && up.Marks != "" {
		g.bleed = defaultMarkInset // marks need room to draw outside the trim box
	}
	return g
}

// pageRun is a maximal consecutive run of top-level blocks sharing a resolved page
// name (and therefore a content width). start/end are indices into body.Children
// (half-open [start,end)).
type pageRun struct {
	name       string
	start, end int
}

// blockPageName returns a block fragment's resolved CSS `page` name ("" = the default,
// un-named page). A nil Box reads as default.
func blockPageName(f *Fragment) string {
	if f == nil || f.Box == nil {
		return ""
	}
	return f.Box.Style.Page
}

// groupRuns partitions blocks into maximal consecutive runs sharing a page name. A page
// name change between adjacent blocks starts a new run (CSS GCPM: a `page` change forces
// a break). Returns one run spanning everything when all blocks share a name (the common
// single-width case).
func groupRuns(blocks []*Fragment) []pageRun {
	var runs []pageRun
	for i := 0; i < len(blocks); {
		name := blockPageName(blocks[i])
		j := i + 1
		for j < len(blocks) && blockPageName(blocks[j]) == name {
			j++
		}
		runs = append(runs, pageRun{name: name, start: i, end: j})
		i = j
	}
	return runs
}

// LayoutPagedDoc lays out root paginated per a PagedConfig: it resolves the page size
// and margins from the document's @page rules (combined with any API override) and
// fragments the document into pages, placing each page's content inside the @page
// margin box and emitting the @page margin boxes (running headers/footers) on each
// page.
//
// When cfg.Paged is false it delegates to Layout (a single tall page) — the
// byte-identical path. Otherwise the LAYOUT width is page 0's content-box width (the
// page width minus its horizontal @page margins, or the fallback width when there is
// no @page rule), so existing geometry is preserved while content is inset by the
// margins. It never panics (the same page-boundary recover as Layout).
func (e *Engine) LayoutPagedDoc(ctx context.Context, root *cssbox.Box, cfg PagedConfig) (pages *layout.Pages, err error) {
	e.setMarginBase(root)
	if !cfg.Paged {
		return e.Layout(ctx, root, cfg.FallbackW) // single tall page (handles canvas bg)
	}
	defer func() {
		if r := recover(); r != nil {
			e.logf("css layout: recovered from panic: %v", r)
			g := cfg.resolvePageGeom(0, "", false)
			pages = &layout.Pages{Pages: []layout.Page{{WidthPt: g.pageW, HeightPt: g.pageH}}}
			err = nil
		}
	}()

	// CSS background propagation (same as LayoutPaged): lift the root/body background
	// onto every page's canvas and clear it from the source box before layoutTree.
	canvasBG := propagateCanvasBackground(root)
	defer func() {
		if pages != nil {
			pages.CanvasBackground = canvasBG
		}
	}()

	// Lay out once at the default width to discover the top-level block list + each
	// block's resolved page name (the cssbox tree is the same regardless of width, so the
	// run grouping is width-independent; only the per-run geometry differs).
	g0 := cfg.resolvePageGeom(0, "", false)
	base := e.layoutTree(ctx, root, g0.contentW)
	if base == nil {
		return &layout.Pages{Pages: []layout.Page{{WidthPt: g0.pageW, HeightPt: g0.pageH}}}, nil
	}
	body := bodyFragment(base)
	if body == nil || len(body.Children) == 0 {
		// No top-level blocks: single page (with margin boxes), as paginateDoc did.
		page := base.Page(g0.pageW, g0.pageH)
		page.Items = e.appendMarginBoxes(page.Items, g0, 0, 1, pageStrings{}, cfg.Running)
		return &layout.Pages{Pages: []layout.Page{page}}, nil
	}
	runs := groupRuns(body.Children)
	// Fast path: a single run at the default name ⇒ the existing single-width pipeline,
	// byte-identical. (groupRuns returns one run named "" when no block sets `page`.)
	if len(runs) == 1 && runs[0].name == "" {
		return e.paginateDoc(base, cfg), nil
	}
	return e.paginateRuns(ctx, root, base, cfg, runs), nil
}

// paginateDoc fragments the laid-out root fragment into pages whose size and content
// inset come from the document's @page rules (via cfg). It mirrors paginate (same
// between-blocks bucketing, positioned/float distribution, wrapper fragmentation, and —
// once components 3/4 land — keep-together and line splitting) but resolves each page's
// geometry through cfg.resolvePageGeom and emits the @page margin boxes. The content
// height used for bucketing is page 0's content box (page height minus @page vertical
// margins); per-page size variation via named pages adjusts only the inset.
func (e *Engine) paginateDoc(root *Fragment, cfg PagedConfig) *layout.Pages {
	g0 := cfg.resolvePageGeom(0, "", false)
	body := bodyFragment(root)
	if body == nil || len(body.Children) == 0 {
		page := root.Page(g0.pageW, g0.pageH)
		page.Items = e.appendMarginBoxes(page.Items, g0, 0, 1, pageStrings{}, cfg.Running)
		return &layout.Pages{Pages: []layout.Page{page}}
	}

	cbWidth := contentWidth(body, g0.contentW)
	buckets, grown := bucketBlocksGrowing(body.Children, g0.contentH, cbWidth, wrapperDecorationBottom(root, body), e.logf)
	applySplitGrowth(grown, root.Floats, root, body)

	// Pull page 0's top up to include the wrapper's own top border/background (same as
	// paginate) so a body top border shows on page 0.
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

	perPagePos := splitPositionedByPage(root, buckets, func(i int) float64 {
		return cfg.resolvePageGeom(i, "", false).contentH
	})
	perPageFloats := splitFloatsByPage(root, buckets)

	// Per-page geometry: page i's size + content inset, resolved from @page (named
	// pages are a follow-up; "" name today). The content height drives no further
	// bucketing here (already bucketed at g0.contentH); the inset positions content.
	geomFn := func(i int, _ pageBucket) pageGeom {
		return cfg.resolvePageGeom(i, "", false)
	}
	pages := e.assemblePages(root, body, buckets, perPagePos, perPageFloats, geomFn, cfg.Running)
	return &layout.Pages{Pages: pages}
}

// runBucket is a bucket plus the page geometry its run resolved to (size/margins/
// marginboxes for that run's named page), so the global assembly can size + inset + chrome
// each page correctly even though pages come from different runs.
type runBucket struct {
	bucket pageBucket
	geom   pageGeom
	// floats holds this bucket's out-of-flow floats, cloned from the run's cached
	// layout and shifted into the bucket's local frame (Y 0). nil when the bucket has
	// no floats — so a float-free document is byte-identical (no float items emitted).
	floats []*Fragment
	// pos holds this bucket's share of the positioned layer (its position:relative
	// blocks, the page-CB position:absolute boxes whose band is this bucket, and a clone
	// of every position:fixed box), with its parallel PositionedInfo. Empty when the
	// bucket has no positioned content — so a positioned-free document is byte-identical.
	// Without this, paginateRuns nil'd the positioned layer and never re-attached it, so
	// every relative/absolute/fixed box in a multi-named-page document painted nowhere.
	pos pagePositioned
}

// floatsForRun partitions THIS run's floats across its buckets, mirroring the
// single-width splitFloatsByPage: a float is routed to the bucket whose vertical band
// contains its margin-box top (pageForY — a float above the first band clamps to bucket
// 0, below the last clamps to the last), then shifted into that bucket's local frame
// (-bk.top) to match the bucket's blocks. It CLONES each float before shifting, because
// the run layout (layoutByWidth[w]) is a shared cache reused across every bucket of every
// run at this width — shifting the original in place (as splitFloatsByPage safely does,
// since it owns its layout) would corrupt later buckets. Returns one slice per bucket
// (nil where a bucket has no floats), so a float-free run is byte-identical.
//
// CRITICAL: runLayout.Floats is the ENTIRE document layout's float list (every top-level
// float bubbles up to the page-root BFC), so it must NOT be distributed wholesale into
// every run — two non-consecutive runs that resolve to the same @page width share one
// layoutByWidth[w] entry, and consuming its whole Floats slice in each run would paint
// every float once per run (duplication) and cross-route floats between sections. We
// therefore scope to floats ORIGINATING in this run's block range [r.start,r.end) via
// runFloats (structural cssbox-descendant attribution), so each float is emitted by
// exactly one run — its owning run — exactly once.
func floatsForRun(runFloats []*Fragment, bks []pageBucket) [][]*Fragment {
	out := make([][]*Fragment, len(bks))
	if len(runFloats) == 0 || len(bks) == 0 {
		return out
	}
	for _, fl := range runFloats {
		bi := pageForY(fl.Y, bks)
		clone := cloneFloatForPage(fl)
		shiftFragment(clone, -bks[bi].top)
		out[bi] = append(out[bi], clone)
	}
	return out
}

// floatsOriginatingIn scopes the layout's page-root floats to those ORIGINATING within
// the given top-level block fragments (a run's block range). Each top-level float
// bubbles up to the page-root BFC (layoutByWidth[w].Floats), losing its in-flow position
// in the tree; but the floated box is still a cssbox descendant of the top-level block it
// was declared in. So a float belongs to a run iff its source cssbox.Box (Fragment.Box)
// is a descendant of one of the run's top-level block boxes. This is an exact structural
// attribution — unaffected by the float's Y frame — so a float is assigned to exactly one
// run even when several runs share the same @page width (hence the same shared Floats
// slice). A float whose Box is nil or is not a descendant of ANY of these blocks is not
// this run's (it belongs to another run and will be picked up there); it is skipped here.
//
// Only page-root-BFC floats flow through here; a float bubbled to a NESTED BFC paints
// within its own subtree and is correctly not seen (nor wanted) here. Two floats are
// deliberately skipped as a narrow, documented limitation of the multi-named-page path
// (the common single-run paginateDoc/splitFloatsByPage path emits all root floats fine):
// a float that is a DIRECT child of <body> (a sibling of the top-level blocks, descendant
// of none) and a float with a nil .Box (anonymous/synthetic) are attributed to no run and
// so appear on no page. This is less wrong than the pre-fix cross-run duplication, and such
// floats have no `page` name to attribute to anyway.
func floatsOriginatingIn(allFloats []*Fragment, runBlocks []*Fragment) []*Fragment {
	if len(allFloats) == 0 || len(runBlocks) == 0 {
		return nil
	}
	owned := ownedBoxes(runBlocks)
	var out []*Fragment
	for _, fl := range allFloats {
		if fl != nil && fl.Box != nil && owned[fl.Box] {
			out = append(out, fl)
		}
	}
	return out
}

// ownedBoxes returns the set of every cssbox.Box in the subtrees rooted at runBlocks'
// boxes — the structural attribution both floatsOriginatingIn and positionedForRun use to
// scope a shared-layout out-of-flow entry (float / abs / fixed) to its owning run.
func ownedBoxes(runBlocks []*Fragment) map[*cssbox.Box]bool {
	owned := map[*cssbox.Box]bool{}
	var collect func(b *cssbox.Box)
	collect = func(b *cssbox.Box) {
		if b == nil || owned[b] {
			return
		}
		owned[b] = true
		for _, c := range b.Children {
			collect(c)
		}
	}
	for _, blk := range runBlocks {
		if blk != nil {
			collect(blk.Box)
		}
	}
	return owned
}

// positionedForRun partitions THIS run's slice of the layout's positioned layer across its
// buckets, the positioned-layer analogue of floatsForRun. It mirrors splitPositionedByPage
// (the single-run path) but is scoped and clones like the float path, because runRoot is a
// shared cached layout (layoutByWidth[w], reused across every bucket of every run at this
// width). runBlocks are this run's top-level block fragments; bks are the run's buckets
// (already carrying bk.blocks, shifted into their local frames by the caller). Each kind of
// positioned entry is routed like splitPositionedByPage:
//
//   - A position:relative block (or a relative descendant of one) is IN FLOW, so its SAME
//     *Fragment pointer is a bucket block (or lives in a bucket's Children subtree) that the
//     caller already shifted into its page-local frame. It is re-attached BY POINTER — NOT
//     cloned, NOT re-shifted (cloning would split the Children<->Positioned alias and its
//     paint copy would never move; the caller's translateFragment margin-inset moves it via
//     Children and skips the aliased Positioned entry, so it lands correctly).
//   - A page-CB position:absolute box is out of flow (not a bucket block): routed to the
//     bucket whose band holds its border-box top (pageForY, clamped), CLONED, and shifted
//     into that bucket's local frame (the caller's translateFragment then applies the @page
//     margin inset, exactly as for floats).
//   - A position:fixed box repeats on every bucket of this run: a clone per bucket, each
//     shifted into that bucket's local frame. (A fixed box's frag.Y is already viewport-page
//     relative, so the shift lands it at the same on-page Y on every page. Repeating only
//     across THIS run's pages — not the whole document — is a narrow limitation of the
//     multi-named-page path, mirroring the per-run float scoping; it is strictly better than
//     the pre-fix drop, and the showcase uses no fixed boxes.)
//
// Scoping: runRoot.Positioned is the WHOLE document's positioned layer (top-level positioned
// boxes bubble to the page-root), shared by every run at this width — so, like floats, each
// entry is attributed to exactly one run structurally (its source .Box is a descendant of one
// of this run's blocks). An entry owned by no run in this width-layout is skipped here (it
// belongs to another run and is emitted there). Returns one pagePositioned per bucket, each
// with parallel frags/infos; all empty when this run has no positioned content (byte-identical).
func positionedForRun(runRoot *Fragment, runBlocks []*Fragment, bks []pageBucket) []pagePositioned {
	out := make([]pagePositioned, len(bks))
	if runRoot == nil || len(runRoot.Positioned) == 0 || len(bks) == 0 {
		return out
	}
	owned := ownedBoxes(runBlocks)
	// Map each bucket block — and every positioned (relative) descendant in its in-flow
	// Children subtree — to its bucket index, so an aliased relative entry is routed to the
	// page its in-flow position landed on (catching a relative block nested under static
	// wrappers whose pointer bubbled to the root). Abs/fixed entries are out of flow and so
	// are not in any block's Children subtree → not in this map → distributed by kind below.
	blockPage := map[*Fragment]int{}
	var mark func(f *Fragment, page int)
	mark = func(f *Fragment, page int) {
		blockPage[f] = page
		for _, c := range f.Children {
			mark(c, page)
		}
	}
	for bi := range bks {
		for _, b := range bks[bi].blocks {
			mark(b, bi)
		}
	}
	assign := func(page int, frag *Fragment, info PositionedInfo) {
		out[page].frags = append(out[page].frags, frag)
		out[page].infos = append(out[page].infos, info)
	}
	for i, frag := range runRoot.Positioned {
		var info PositionedInfo
		if i < len(runRoot.PositionedInfo) {
			info = runRoot.PositionedInfo[i]
		}
		if p, ok := blockPage[frag]; ok {
			// Relative block / relative descendant: aliased with a bucket block already
			// shifted by the caller — re-attach by pointer, no clone, no shift.
			assign(p, frag, info)
			continue
		}
		// From here on the entry is out of flow (abs/fixed). Scope it to this run: an entry
		// owned by another run's blocks is emitted there, not here.
		if frag == nil || frag.Box == nil || !owned[frag.Box] {
			continue
		}
		if isFixedFragment(frag) {
			for bi := range bks {
				clone := cloneFloatForPage(frag)
				shiftFragment(clone, -bks[bi].top)
				assign(bi, clone, info)
			}
			continue
		}
		page := pageForY(frag.Y, bks)
		clone := cloneFloatForPage(frag)
		shiftFragment(clone, -bks[page].top)
		assign(page, clone, info)
	}
	return out
}

// cloneFloatForPage deep-copies a float fragment enough that shiftFragment (and the
// later translateFragment page-inset) can move it WITHOUT mutating the shared cached
// run layout (layoutByWidth[w], reused across buckets/runs at one width). It copies
// every field those two functions mutate in place or recurse into: the child slices
// (Children, Floats, Positioned), the inline line/glyph slices (BaselineY/Glyphs[].X
// are shifted), the pointer contents shiftFragment/translateFragment mutate (Image,
// Control, BgImage — CX/CY/Origin*/Clip* are moved), and the in-place-mutated auxiliary
// slices (Collapsed strips, PositionedInfo[].ClipChain rects). Value-typed fields
// (ClipRect, Border, Background, …) are copied by the struct copy `c := *f`. Genuinely
// read-only fields (glyph Outline paths, decoded images, the source cssbox.Box, styles)
// are shared by pointer — neither shift function touches them.
//
// ALIASING: a position:relative descendant of the float is stored as the SAME *Fragment
// pointer in BOTH Children (painted in flow, skipped at paint via IsPositioned) AND
// Positioned (the copy that actually paints). shiftFragment/shiftFragmentExtras rely on
// this alias — the Children recursion shifts the fragment, and shiftFragmentExtras
// deliberately SKIPS relative Positioned entries because they are already shifted through
// Children. A naive deep clone splits that one pointer into two independent clones, so the
// Positioned-side copy (the one that paints) would never be shifted onto the page. This
// clone therefore threads an old->new pointer map: each Children/Floats entry records its
// clone, and a Positioned entry reuses the mapped clone when its source was already cloned
// (an aliased in-flow relative), cloning independently only genuinely out-of-flow entries
// (abs/fixed, never present in Children). This preserves the alias in the clone.
func cloneFloatForPage(f *Fragment) *Fragment {
	return cloneFloatForPageMap(f, map[*Fragment]*Fragment{})
}

// cloneFloatForPageMap is cloneFloatForPage's worker: seen maps each ORIGINAL fragment
// pointer to its clone, so an aliased relative descendant (present in both Children and
// Positioned) maps to a SINGLE clone — preserving the Children<->Positioned alias that
// shiftFragment relies on.
func cloneFloatForPageMap(f *Fragment, seen map[*Fragment]*Fragment) *Fragment {
	if f == nil {
		return nil
	}
	if existing, ok := seen[f]; ok {
		return existing // already cloned (aliased across Children/Floats/Positioned)
	}
	c := *f
	seen[f] = &c
	if len(f.Children) > 0 {
		c.Children = make([]*Fragment, len(f.Children))
		for i, ch := range f.Children {
			c.Children[i] = cloneFloatForPageMap(ch, seen)
		}
	}
	if len(f.Floats) > 0 {
		c.Floats = make([]*Fragment, len(f.Floats))
		for i, ch := range f.Floats {
			c.Floats[i] = cloneFloatForPageMap(ch, seen)
		}
	}
	if len(f.Positioned) > 0 {
		c.Positioned = make([]*Fragment, len(f.Positioned))
		for i, ch := range f.Positioned {
			// A relative descendant is aliased into Children (already in seen); reuse
			// that clone so the Positioned-side pointer stays the paint-layer alias of
			// the in-flow copy shiftFragment moves. An abs/fixed descendant appears only
			// here and is cloned independently.
			c.Positioned[i] = cloneFloatForPageMap(ch, seen)
		}
	}
	if len(f.Lines) > 0 {
		c.Lines = make([]LineFragment, len(f.Lines))
		for i, ln := range f.Lines {
			nl := ln
			if len(ln.Glyphs) > 0 {
				nl.Glyphs = make([]GlyphFragment, len(ln.Glyphs))
				copy(nl.Glyphs, ln.Glyphs)
			}
			c.Lines[i] = nl
		}
	}
	if len(f.Collapsed) > 0 {
		c.Collapsed = make([]layout.BorderItem, len(f.Collapsed))
		copy(c.Collapsed, f.Collapsed)
	}
	if len(f.PositionedInfo) > 0 {
		c.PositionedInfo = make([]PositionedInfo, len(f.PositionedInfo))
		for i, pi := range f.PositionedInfo {
			np := pi
			if len(pi.ClipChain) > 0 {
				np.ClipChain = make([]rect, len(pi.ClipChain))
				copy(np.ClipChain, pi.ClipChain)
			}
			c.PositionedInfo[i] = np
		}
	}
	if f.Image != nil {
		img := *f.Image
		c.Image = &img
	}
	if f.Control != nil {
		ctl := *f.Control
		c.Control = &ctl
	}
	if f.BgImage != nil {
		bg := *f.BgImage
		c.BgImage = &bg
	}
	if f.Vector != nil {
		// Must be cloned for the same reason Image is: translateFragment
		// shifts the content's origin IN PLACE, so per-page clones sharing one
		// *VectorContent would compound every page's shift onto it. A
		// bottom-anchored position:fixed SVG over three pages then lands
		// entirely off-page on all of them and silently disappears.
		vec := *f.Vector
		c.Vector = &vec
	}
	return &c
}

// paginateRuns paginates a document whose top-level blocks resolve to more than one
// named page (different content widths). It lays the document out once per DISTINCT run
// content width, takes each run's block fragments from the layout matching its width,
// buckets each run against its own page geometry, then assembles + numbers all pages
// globally (so counter(page)/string() are document-wide).
func (e *Engine) paginateRuns(ctx context.Context, root *cssbox.Box, base *Fragment, cfg PagedConfig, runs []pageRun) *layout.Pages {
	// Cache one full-document layout per distinct content width.
	layoutByWidth := map[float64]*Fragment{}
	bodyByWidth := map[float64][]*Fragment{}
	getLayout := func(name string) (geom pageGeom, blocks []*Fragment) {
		geom = cfg.resolvePageGeom(0, name, false)
		w := geom.contentW
		if _, ok := layoutByWidth[w]; !ok {
			var frag *Fragment
			if math.Abs(w-contentWidth(bodyFragment(base), cfgFallback(cfg))) < 0.01 {
				frag = base // reuse the base layout when the width matches it
			} else {
				frag = e.layoutTree(ctx, root, w)
			}
			layoutByWidth[w] = frag
			bodyByWidth[w] = nil
			if b := bodyFragment(frag); b != nil {
				bodyByWidth[w] = b.Children
			}
		}
		return geom, bodyByWidth[w]
	}

	var all []runBucket
	attributed := map[*Fragment]bool{} // floats routed to some run (for the unattributed-float diagnostic below)
	for ri, r := range runs {
		geom, blocks := getLayout(r.name)
		// Take this run's blocks BY INDEX from the matching-width layout. The cssbox tree
		// is identical across widths, so body.Children indices align run-for-run.
		if r.end > len(blocks) {
			continue // defensive: layout produced fewer blocks (shouldn't happen)
		}
		runBlocks := blocks[r.start:r.end]
		cb := contentWidth(bodyFragment(layoutByWidth[geom.contentW]), geom.contentW)
		// Only the document's last run holds the wrapper's bottom edge.
		wrapperBottom := 0.0
		if ri == len(runs)-1 {
			frag := layoutByWidth[geom.contentW]
			wrapperBottom = wrapperDecorationBottom(frag, bodyFragment(frag))
		}
		bks, grown := bucketBlocksGrowing(runBlocks, geom.contentH, cb, wrapperBottom, e.logf)
		applySplitGrowth(grown, nil, bodyFragment(layoutByWidth[geom.contentW]))
		// The document's first page also holds the html/body wrapper's top border and
		// padding, as on the single-geometry path (paginateDoc).
		if len(all) == 0 && len(bks) > 0 {
			frag := layoutByWidth[geom.contentW]
			top := bks[0].top
			if t := wrapperDecorationTop(frag, top); t < top {
				top = t
			}
			if t := wrapperDecorationTop(bodyFragment(frag), top); t < top {
				top = t
			}
			bks[0].top = top
		}
		// Split this run's out-of-flow floats across its buckets (cloned + shifted into
		// each bucket's local frame). Without this, floats in a named-page section were
		// dropped entirely (paginateRuns nil'd Floats and never re-attached them).
		//
		// Scope to floats ORIGINATING in THIS run's block range: layoutByWidth[w].Floats
		// is the whole document's float list, shared by every run at this width. Two
		// non-consecutive runs at the same @page width (e.g. wide, default, wide) would
		// otherwise each emit ALL of that width's floats — painting each float once per run
		// and cross-routing floats between sections. floatsOriginatingIn attributes each
		// float to its owning run structurally (its cssbox.Box is a descendant of one of
		// this run's top-level block boxes), so each float is emitted by exactly one run.
		ownFloats := floatsOriginatingIn(layoutByWidth[geom.contentW].Floats, runBlocks)
		for _, fl := range ownFloats {
			attributed[fl] = true
		}
		applySplitGrowth(grown, ownFloats)
		runFloats := floatsForRun(ownFloats, bks)
		// Split this run's positioned layer (relative blocks re-attached by pointer; page-CB
		// abs boxes routed to their band + cloned + shifted; fixed boxes cloned onto every
		// bucket) across its buckets. Computed BEFORE the assembly loop shifts bk.blocks: a
		// relative entry is the same pointer as its bucket block, moved later by that shift;
		// an abs/fixed clone is shifted here off the run-layout's (still un-shifted) Y, which
		// matches bks[].top. Without this the positioned layer was nil'd and never re-attached
		// — every relative/absolute/fixed box in a multi-named-page document painted nowhere.
		runPos := positionedForRun(layoutByWidth[geom.contentW], runBlocks, bks)
		for bi, bk := range bks {
			all = append(all, runBucket{bucket: bk, geom: geom, floats: runFloats[bi], pos: runPos[bi]})
		}
	}
	// Diagnostic: a page-root float attributed to NO run (a body-direct float — a sibling of
	// the top-level blocks, descendant of none — or a nil-Box anonymous/synthetic float)
	// appears on no page. This is a documented limitation of the multi-named-page path (see
	// floatsOriginatingIn); log it so a future regression that silently drops a float surfaces.
	for w, frag := range layoutByWidth {
		for _, fl := range frag.Floats {
			if fl != nil && !attributed[fl] {
				e.logf("css layout: page-root float attributed to no run (body-direct or nil-Box), painted on no page (width=%.0f, box=%v)", w, fl.Box)
			}
		}
	}
	if len(all) == 0 {
		g0 := cfg.resolvePageGeom(0, "", false)
		return &layout.Pages{Pages: []layout.Page{{WidthPt: g0.pageW, HeightPt: g0.pageH}}}
	}

	// Global string snapshots over the concatenated bucket list.
	buckets := make([]pageBucket, len(all))
	for i := range all {
		buckets[i] = all[i].bucket
	}
	snaps := buildStringSnapshots(buckets)

	pages := make([]layout.Page, 0, len(all))
	for i := range all {
		bk := all[i].bucket
		g := all[i].geom
		// Shift this bucket's blocks to local Y 0 (each run's blocks are in their own
		// layout's page space).
		shiftFragments(bk.blocks, -bk.top)
		// Build a minimal page root carrying just this bucket's blocks. We synthesize a
		// shallow body wrapper so AppendItems flattens the blocks; the per-run base body
		// is reused for its box/styling.
		pageRoot := *base
		runBody := *bodyFragment(layoutByWidth[g.contentW])
		runBody.Children = bk.blocks
		runBody.Positioned, runBody.PositionedInfo, runBody.Floats = nil, nil, nil
		// Fragment the html/body wrapper's own border and background across pages, as
		// assemblePages does: move each wrapper box (not its children) into this page's
		// block-shifted frame, so its top edge shows only on the page holding the
		// wrapper's top and its bottom edge only on the page holding its bottom. Left at
		// its document position, the wrapper drew its top rule at the top of every page,
		// under that page's first line.
		shiftFragmentSelf(&runBody, -bk.top)
		shiftFragmentSelf(&pageRoot, -bk.top)
		sliceWrapperForPage(&runBody, i == len(all)-1, g.contentH)
		sliceWrapperForPage(&pageRoot, i == len(all)-1, g.contentH)
		children := make([]*Fragment, len(base.Children))
		copy(children, base.Children)
		children[len(children)-1] = &runBody
		pageRoot.Children = children
		// Attach this bucket's positioned layer at the page root (mirroring paginateDoc/
		// assemblePages). Relative entries are the same pointers as bucket blocks (moved by
		// the shiftFragments above and, below, by translateFragment via Children — and skipped
		// as Positioned entries by shiftFragmentExtras, so not double-moved); abs/fixed clones
		// are in the bucket-local frame and the translateFragment inset carries them by the
		// @page margin/bleed delta (it recurses into Positioned). The body wrapper owns no
		// positioned layer of its own (the root does), so its copy stays nil.
		pageRoot.Positioned = all[i].pos.frags
		pageRoot.PositionedInfo = all[i].pos.infos
		// Attach this bucket's floats at the page root (mirroring paginateDoc/assemblePages,
		// which sets pageRoot.Floats and keeps the body wrapper's Floats nil). They are in
		// the bucket's local frame; the translateFragment inset below carries them by the
		// same @page margin/bleed delta (translateFragment recurses into Floats).
		pageRoot.Floats = all[i].floats

		dx, dy := g.marginL+g.bleed, g.marginT+g.bleed
		if dx != 0 || dy != 0 {
			translateFragment(&pageRoot, dx, dy)
		}
		pg := pageRoot.Page(g.mediaW(), g.mediaH())
		before := len(pg.Items)
		pg.Items = e.appendMarginBoxes(pg.Items, g, i, len(all), snaps[i], cfg.Running)
		if g.bleed != 0 {
			translateItems(pg.Items, before, g.bleed, g.bleed)
		}
		pg.Items = appendCropMarks(pg.Items, g)
		pages = append(pages, pg)
	}
	return &layout.Pages{Pages: pages}
}

// cfgFallback returns the content width the base layout was built at (page 0 default
// content width), used to detect when a run's width matches the base layout so it can be
// reused instead of re-laid-out.
func cfgFallback(cfg PagedConfig) float64 {
	return cfg.resolvePageGeom(0, "", false).contentW
}
