package css

import (
	"context"
	"image/color"

	gcss "github.com/nathanstitt/omnidoc/pkg/internal/css"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/cssbox"
)

// tableGrid is the private intermediate the table layout algorithm operates on: the
// row/column grid recovered from a fixed-up table box, the single source of truth
// for the width solve, height solve, and border resolution. It never escapes the
// layoutTable call.
type tableGrid struct {
	table    *cssbox.Box
	caption  *cssbox.Box
	rows     []*gridRow
	cols     []gridCol
	cells    []*gridCell
	collapse bool
	fixed    bool
	spacingH float64
	spacingV float64
	// cellMap is the row-major occupancy grid: cellMap[row][col] is the *gridCell
	// covering that slot (origin or spanned-into), or nil. Built by the occupancy scan
	// in buildGrid; used by cellAt for O(1) neighbor lookups (collapse border solving)
	// instead of a per-neighbor linear scan of cells.
	cellMap [][]*gridCell
	// colGroups / rowGroups record <colgroup>/<col-spanning> and row-group boxes with
	// their column/row ranges, so their backgrounds paint as layers (CSS 17.5.1). Only
	// groups with a visible background need to be retained, but they are collected
	// unconditionally and filtered at paint time.
	colGroups []gridSpan
	rowGroups []gridSpan
	// headerRows is the number of LEADING visual rows that came from a <thead>
	// (DisplayTableHeaderGroup). Header rows sort to the front of visualRows, so they
	// are always rows [0, headerRows). Recorded here because the head/body/footer
	// distinction is otherwise flattened away by grid construction, and pagination
	// needs it to repeat the header on continuation pages.
	headerRows int
}

// cellAt returns the cell occupying slot (r,c) — its origin or a span it covers — or nil
// when out of range / empty. O(1) via the occupancy map.
func (g *tableGrid) cellAt(r, c int) *gridCell {
	if r < 0 || r >= len(g.cellMap) || c < 0 || c >= len(g.cellMap[r]) {
		return nil
	}
	return g.cellMap[r][c]
}

type gridRow struct {
	box    *cssbox.Box // the DisplayTableRow box (real or anonymous)
	cells  []*gridCell // cells ORIGINATING in this row (not spanned into it)
	height float64     // resolved row height (tallest non-spanning cell)
	y      float64     // y-offset of the row's top edge in the table content box
}

type gridCol struct {
	hasWidth bool
	width    float64     // specified/hint width (px) when hasWidth
	pct      float64     // percentage width [0..100], or <0 when none
	x        float64     // x-offset of the column's left edge in the table content box
	min, max float64     // content min/max-content widths (auto layout)
	box      *cssbox.Box // the <col> box (for its background), or nil for an implicit column
}

// gridSpan records a row-group or column-group box plus the half-open index range of
// rows/columns it covers, so its background can be painted as one layer behind its cells.
type gridSpan struct {
	box        *cssbox.Box
	start, end int // half-open [start,end) index into g.rows (row-group) or g.cols (col-group)
}

type gridCell struct {
	box      *cssbox.Box
	row, col int // origin slot (top-left), 0-based
	rowSpan  int
	colSpan  int
	frag     *Fragment // fragment produced by laying out this cell
}

// buildGrid recovers the grid from a fixed-up table box. It flattens row-groups in
// visual order (header groups, then body row-groups in document order, then footer
// groups), reads <col>/<colgroup> width hints, and assigns each cell to its origin
// slot with an occupancy scan honoring colspan/rowspan.
func buildGrid(tbl *cssbox.Box) *tableGrid {
	g := &tableGrid{
		table:    tbl,
		collapse: tbl.Style.BorderCollapse == "collapse",
		fixed:    tbl.Style.TableLayout == "fixed",
	}
	if !g.collapse {
		g.spacingH = tbl.Style.BorderSpacingH
		g.spacingV = tbl.Style.BorderSpacingV
	}

	// 1. Collect caption + column hints + rows (visual order).
	var headRows, bodyRows, footRows []*cssbox.Box
	// groupOfRow maps a row box to its row-group box (for the row-group background layer).
	groupOfRow := map[*cssbox.Box]*cssbox.Box{}
	collectRows := func(group *cssbox.Box) []*cssbox.Box {
		var rows []*cssbox.Box
		for _, c := range group.Children {
			if c.Display == cssbox.DisplayTableRow {
				rows = append(rows, c)
				groupOfRow[c] = group
			}
		}
		return rows
	}
	for _, c := range tbl.Children {
		switch c.Display {
		case cssbox.DisplayTableCaption:
			if g.caption == nil {
				g.caption = c
			}
		case cssbox.DisplayTableColumn:
			g.addColumnHint(c)
		case cssbox.DisplayTableColumnGroup:
			start := len(g.cols)
			cols := 0
			for _, cc := range c.Children {
				if cc.Display == cssbox.DisplayTableColumn {
					g.addColumnHint(cc)
					cols++
				}
			}
			if cols == 0 {
				g.addColumnHintN(c, spanOf(c))
			}
			// Record the colgroup's column span for its background layer.
			if len(g.cols) > start {
				g.colGroups = append(g.colGroups, gridSpan{box: c, start: start, end: len(g.cols)})
			}
		case cssbox.DisplayTableHeaderGroup:
			headRows = append(headRows, collectRows(c)...)
		case cssbox.DisplayTableFooterGroup:
			footRows = append(footRows, collectRows(c)...)
		case cssbox.DisplayTableRowGroup:
			bodyRows = append(bodyRows, collectRows(c)...)
		case cssbox.DisplayTableRow:
			// defensive: a bare row with no group affiliation is treated as a body row
			// (fixup normally wraps these in a row-group first).
			bodyRows = append(bodyRows, c)
		}
	}
	visualRows := make([]*cssbox.Box, 0, len(headRows)+len(bodyRows)+len(footRows))
	visualRows = append(visualRows, headRows...)
	visualRows = append(visualRows, bodyRows...)
	visualRows = append(visualRows, footRows...)
	g.headerRows = len(headRows)

	// Row-group spans: contiguous runs of visual rows sharing a row-group box (for the
	// row-group background layer). visualRows keeps each group's rows contiguous.
	for i := 0; i < len(visualRows); {
		grp := groupOfRow[visualRows[i]]
		j := i + 1
		for j < len(visualRows) && groupOfRow[visualRows[j]] == grp {
			j++
		}
		if grp != nil {
			g.rowGroups = append(g.rowGroups, gridSpan{box: grp, start: i, end: j})
		}
		i = j
	}

	// 2. Occupancy scan: assign each cell to its origin slot. occupied[r][c] holds the
	// *gridCell covering that slot (nil when free), retained on the grid as cellMap for
	// O(1) neighbor lookups (cellAt).
	var occupied [][]*gridCell
	ensure := func(r, c int) {
		for len(occupied) <= r {
			occupied = append(occupied, make([]*gridCell, len(g.cols)))
		}
		if c >= len(g.cols) {
			grow := c + 1 - len(g.cols)
			for i := 0; i < grow; i++ {
				g.cols = append(g.cols, gridCol{pct: -1})
			}
			for ri := range occupied {
				for len(occupied[ri]) < len(g.cols) {
					occupied[ri] = append(occupied[ri], nil)
				}
			}
		}
	}
	for ri, rb := range visualRows {
		gr := &gridRow{box: rb}
		col := 0
		for _, cb := range rb.Children {
			if cb.Display != cssbox.DisplayTableCell {
				continue
			}
			// Ensure column `col` exists before the skip loop reads occupied[ri][col].
			ensure(ri, col)
			for col < len(g.cols) && occupied[ri][col] != nil {
				col++
				ensure(ri, col)
			}
			cs := cb.ColSpan
			if cs < 1 {
				cs = 1
			}
			rs := cb.RowSpan
			if rs < 1 {
				rs = 1
			}
			// A rowspan cannot reach past the rows the document actually has
			// (CSS 2.1 17.5.1: it is clipped to the row group). Clipping here
			// rather than in the extent pass below matters for cost, not just
			// correctness: the occupancy loop calls ensure() once per covered
			// slot, so an unclipped rowspan fabricates empty rows -- 65534 of
			// them for a one-row table, times every cell in it.
			if rs > len(visualRows)-ri {
				rs = len(visualRows) - ri
			}
			gc := &gridCell{box: cb, row: ri, col: col, colSpan: cs, rowSpan: rs}
			g.cells = append(g.cells, gc)
			gr.cells = append(gr.cells, gc)
			for dr := 0; dr < rs; dr++ {
				for dc := 0; dc < cs; dc++ {
					ensure(ri+dr, col+dc)
					occupied[ri+dr][col+dc] = gc
				}
			}
			col += cs
		}
		g.rows = append(g.rows, gr)
	}
	g.cellMap = occupied

	// 3. Clamp each cell's spans to the final grid extent.
	for _, gc := range g.cells {
		if gc.col+gc.colSpan > len(g.cols) {
			gc.colSpan = len(g.cols) - gc.col
			if gc.colSpan < 1 {
				gc.colSpan = 1
			}
		}
		if gc.row+gc.rowSpan > len(g.rows) {
			gc.rowSpan = len(g.rows) - gc.row
			if gc.rowSpan < 1 {
				gc.rowSpan = 1
			}
		}
	}
	return g
}

// addColumnHint appends one column carrying cb's width hint (a <col> width).
func (g *tableGrid) addColumnHint(cb *cssbox.Box) {
	g.addColumnHintN(cb, spanOf(cb))
}

// addColumnHintN appends n columns carrying cb's width hint (and box, for its background).
func (g *tableGrid) addColumnHintN(cb *cssbox.Box, n int) {
	for i := 0; i < n; i++ {
		col := gridCol{pct: -1, box: cb}
		if w, ok := specifiedFixedWidth(cb); ok {
			col.hasWidth = true
			col.width = w
		}
		if pct, ok := pctWidthOf(cb); ok {
			col.pct = pct
		}
		g.cols = append(g.cols, col)
	}
}

// spanOf reads a <col>/<colgroup> span (ColSpan field, >=1).
func spanOf(cb *cssbox.Box) int {
	if cb.ColSpan < 1 {
		return 1
	}
	return cb.ColSpan
}

// pctWidthOf returns a box's width as a percentage [0..100] and true when its width
// is specified in percent; false otherwise. (specifiedFixedWidth rejects percent, so
// percentage widths are read separately for table columns.)
func pctWidthOf(b *cssbox.Box) (float64, bool) {
	w := b.Style.Width
	if w.Unit == gcss.UnitPercent {
		return w.Value, true
	}
	return 0, false
}

// layoutTable is the TableFC entry point (called from layoutInterior). It builds the
// grid, solves column widths (fixed layout this slice), lays out + positions cells,
// and returns the interior (cell fragments in the local content-top-0 frame). A table
// establishes a BFC, so leading/trailing margins are 0. contentW is the table's
// content-box width; contentX the page-space left of that content box.
func (e *Engine) layoutTable(ctx context.Context, b *cssbox.Box, contentW, contentX, bandOriginY float64, fc *floatContext) interior {
	_ = bandOriginY // reserved for future use (float interactions)
	_ = fc          // reserved for future use (float interactions)
	g := buildGrid(b)
	rtl := effectiveDirection(g.table) == "rtl"
	if len(g.rows) == 0 || len(g.cols) == 0 {
		return interior{contentHeight: 0}
	}

	if g.fixed {
		e.solveFixedWidths(g, contentW)
	} else {
		e.solveAutoWidths(ctx, g, contentW)
	}

	// Column x-offsets (left content edge of each column), with border-spacing.
	x := g.spacingH
	for ci := range g.cols {
		g.cols[ci].x = x
		x += g.cols[ci].width + g.spacingH
	}
	tableContentW := x // span of the column grid: Σ column widths + (ncols+1) border-spacing gaps

	// RTL: the inline-start edge is the RIGHT edge, so column 0 sits rightmost and
	// the grid runs leftward. Mirroring the solved x-offsets about the grid span
	// keeps every downstream consumer (cell placement, colsRect/backgroundLayers)
	// geometry-driven and unchanged — they read cols[ci].x and never assume the
	// column index and the x order agree.
	//
	// NOTE: the COLUMN order mirrors; text inside each cell does not (inline bidi
	// reordering is a later slice).
	if rtl {
		for ci := range g.cols {
			g.cols[ci].x = tableContentW - g.cols[ci].x - g.cols[ci].width
		}
	}

	// Caption: a block laid out at the table content width, placed above (top) or below.
	var captionFrag *Fragment
	captionH := 0.0
	if g.caption != nil {
		captionPos := &positionedContext{}
		res := e.layoutBlock(ctx, g.caption, tableContentW, contentX, 0, 0,
			&floatContext{cbLeft: contentX, cbRight: contentX + tableContentW}, captionPos, posCBOwner{isPage: true})
		captionFrag = res.frag
		if captionFrag != nil {
			captionH = captionFrag.H
			// The caption is a block container and its own positioned CB; resolve any
			// abs/fixed descendants against its provisional box now (mirroring the cell
			// path) so they are not silently dropped.
			e.resolveAbsolute(ctx, captionPos, captionFrag, tableContentW, captionFrag.H)
		}
	}
	gridDY := 0.0
	// caption-side: "top" is the initial value; any non-"bottom" value (including "")
	// is treated as top. Read from the caption box: caption-side is inherited, so this
	// honors it whether the author set it on the <table> (inherited) or the <caption>.
	if g.caption != nil && g.caption.Style.CaptionSide != "bottom" {
		gridDY = captionH // caption on top: shift the grid down by the caption height
	}

	// Lay out each cell at its column width; capture natural height.
	natH := map[*gridCell]float64{}
	for _, gc := range g.cells {
		cw := g.cellWidth(gc)
		cellPos := &positionedContext{}
		res := e.layoutBlock(ctx, gc.box, cw, 0, 0, 0, &floatContext{cbLeft: 0, cbRight: cw}, cellPos, posCBOwner{isPage: true})
		gc.frag = res.frag
		// A cell is a BFC and its own positioned containing block; consume any relative
		// descendants that bubbled out of the cell (so they paint in the cell's atom),
		// then resolve any abs/fixed descendants against the cell's provisional box now
		// (mirroring the inline-block atom path) so neither is silently dropped.
		// (Positioning relative to the cell is approximate here, like the inline-block
		// atom case — exact cross-cell positioning is out of scope.)
		if gc.frag != nil {
			natH[gc] = gc.frag.H
			consumePendingPositioned(gc.frag, res.pendingPositioned)
			e.resolveAbsolute(ctx, cellPos, gc.frag, cw, gc.frag.H)
		}
	}

	// CSS empty-cells:hide (separate-borders mode only): an EMPTY cell paints no border
	// or background. Suppress those after layout so the cell still reserves its grid slot
	// (sizing is unchanged) but its decorations are cleared. In collapse mode empty-cells
	// has no effect (collapsed borders are resolved table-wide), so this is skipped.
	if !g.collapse {
		for _, gc := range g.cells {
			if gc.frag != nil && gc.box.Style.EmptyCells == "hide" && isEmptyCellFragment(gc.frag) {
				gc.frag.Background = color.RGBA{}
				gc.frag.Border = [4]BorderEdge{}
			}
		}
	}

	// Row natural heights = tallest non-spanning cell originating in the row.
	for _, gr := range g.rows {
		h := 0.0
		for _, gc := range gr.cells {
			if gc.rowSpan == 1 && gc.frag != nil && gc.frag.H > h {
				h = gc.frag.H
			}
		}
		gr.height = h
	}

	e.distributeRowspanHeights(g)

	// Row y-offsets with vertical spacing.
	y := g.spacingV
	for _, gr := range g.rows {
		gr.y = y
		y += gr.height + g.spacingV
	}
	tableContentH := y

	// Position cells: a cell fills its column(s) × row(s) rectangle. Stretch in g.cells
	// order so paint order is stable; vertical-align runs in a separate per-row pass.
	var children []*Fragment
	for _, gc := range g.cells {
		if gc.frag == nil {
			continue
		}
		cx := contentX + g.cols[gc.col].x
		cy := g.rows[gc.row].y + gridDY
		cw := g.cellWidth(gc)
		ch := gc.rowBandHeight(g)
		stretchCellFragment(gc.frag, cx, cy, cw, ch)
		// A table cell establishes an independent formatting context (CSS 2.1 §17.5.1).
		// Mark the fragment a BFC so AppendItems flattens it atomically and emits its
		// positioned layer — otherwise an abs/fixed descendant of a cell is dropped at
		// paint time (it is resolved onto gc.frag.Positioned above).
		gc.frag.IsBFC = true
		children = append(children, gc.frag)
	}

	// Vertical-align pass, row by row top-to-bottom. top/middle/bottom shift a cell's
	// content within its own band (per cell). vertical-align:baseline is a per-ROW
	// decision: the row's baseline cells shift their content so their first text
	// baselines coincide (alignBaselineCellContents, the shared baseline machinery). If
	// a baseline group needs more height than the band, the row grows: every cell in the
	// row grows its border box to the taller band and all LOWER rows shift down by the
	// growth. This is a LOCALIZED grow (row positions were assigned after cell layout —
	// no track/row-height re-solve), accumulated in rowShift. When no cell uses baseline
	// (the common case — equal-font or empty cells), extra is 0 everywhere, rowShift
	// stays 0, no cell moves, and the result is byte-identical to the prior per-cell
	// applyCellVAlign behavior.
	//
	// KNOWN LIMITATION (rowspan + baseline): a rowspan cell appears only in its ORIGIN
	// row's gr.cells, so it grows/shifts with that row correctly, but if a row it merely
	// SPANS INTO later grows from its own baseline group, the rowspan cell is not in that
	// row's gr.cells and so does not re-grow — its border box can under-cover the grown
	// band. Re-growing it would need the cross-row re-solve the design avoids, so it is
	// left as a localized approximation (no fixture stacks rowspan + baseline + differing
	// spanned-row content); it degrades without panic and never affects a table that does
	// not combine those three.
	rowShift := 0.0
	for _, gr := range g.rows {
		// Carry the accumulated growth of earlier rows down onto this row's cells (the
		// whole cell band moved down, so move border box + content together).
		if rowShift > 0 {
			for _, gc := range gr.cells {
				if gc.frag != nil {
					translateFragment(gc.frag, 0, rowShift)
				}
			}
		}
		// Baseline group first (it determines how much the band grows).
		var baselineCells []*Fragment
		for _, gc := range gr.cells {
			if gc.frag != nil && gc.box.Style.VerticalAlign == "baseline" {
				baselineCells = append(baselineCells, gc.frag)
			}
		}
		bandExtra := alignBaselineCellContents(baselineCells)
		// Grow every cell in the row to the (possibly taller) band, then place the
		// non-baseline cells within it.
		for _, gc := range gr.cells {
			if gc.frag == nil {
				continue
			}
			if bandExtra > 0 {
				gc.frag.H += bandExtra
			}
			if gc.box.Style.VerticalAlign != "baseline" {
				applyCellVAlign(gc.frag, gc.box, natH[gc], gc.rowBandHeight(g)+bandExtra)
			}
		}
		rowShift += bandExtra
	}

	gridBottom := tableContentH + gridDY + rowShift
	if captionFrag != nil {
		if g.caption.Style.CaptionSide == "bottom" {
			translateFragment(captionFrag, 0, gridBottom-captionFrag.Y) // just below the grid
			children = append(children, captionFrag)
			gridBottom += captionH
		} else {
			translateFragment(captionFrag, 0, 0-captionFrag.Y) // at local Y 0 (the very top)
			children = append(children, captionFrag)
		}
	}

	// border-collapse:collapse — resolve shared edges and clear per-cell borders so the
	// collapsed grid lines (stored on the table fragment) don't double-paint.
	var collapsedBorders []layout.BorderItem
	if g.collapse {
		collapsedBorders = g.buildCollapsedBorders(g.cellAt, rtl)
		// In collapse mode the resolved grid edges replace per-cell borders: clear each
		// cell fragment's own border so it does not double-paint.
		for i := 0; i < len(g.cells); i++ {
			gc := g.cells[i]
			if gc.frag != nil {
				gc.frag.Border = [4]BorderEdge{}
			}
		}
	}

	// CSS 17.5.1 background layers: paint column-group, column, row-group, and row
	// backgrounds BEHIND the cells (the table's own background is painted by the table
	// fragment; cell backgrounds are on the cell fragments). Each layer's rect is the
	// union of the final positioned cells it covers, so it is correct after the
	// vertical-align/baseline grow. Prepended (in back-to-front order) so they paint
	// behind cells. Most tables have no such backgrounds → bg is empty → nothing added,
	// keeping non-styled tables byte-identical.
	if bg := g.backgroundLayers(); len(bg) > 0 {
		children = append(bg, children...)
	}

	// Repeatable-header extent: the bottom of the last <thead> row, so pagination can
	// clone those cells onto continuation pages. Header rows sort to the front of the
	// visual row order, so they are rows [0, headerRows). It is read from the header
	// cells' FINAL fragments, not the grid's row geometry: the grid rows do not include
	// the top caption's offset (gridDY) or a header row's baseline growth (rowShift),
	// and a header bottom short by the caption height selected the caption, not the
	// <thead>, as the repeatable header.
	headerBottom := 0.0
	if n := g.headerRows; n > 0 && n <= len(g.rows) {
		for i := range g.cells {
			gc := g.cells[i]
			if gc.frag != nil && gc.row < n && gc.frag.Y+gc.frag.H > headerBottom {
				headerBottom = gc.frag.Y + gc.frag.H
			}
		}
	}

	return interior{
		children:         children,
		contentHeight:    gridBottom,
		leadingMargin:    0,
		trailingMargin:   0,
		collapsedBorders: collapsedBorders,
		intrinsicWidth:   tableContentW,
		headerBottom:     headerBottom,
	}
}

// backgroundLayers builds the table's column-group / column / row-group / row background
// fragments (CSS 17.5.1), in back-to-front paint order, from the final positioned cells.
// A layer is emitted only when its box has a visible (non-zero-alpha) background; the
// rect is the union of the cells the layer covers (robust to the baseline grow). Cells
// are looked up via cellMap, so a spanned-into slot still contributes to a row/column
// layer. Returns nil when no layer has a background (the common case).
func (g *tableGrid) backgroundLayers() []*Fragment {
	var layers []*Fragment
	// Column groups (painted first / furthest back).
	for _, cg := range g.colGroups {
		if r, ok := g.colsRect(cg.start, cg.end); ok {
			if f := bgFragment(cg.box, r); f != nil {
				layers = append(layers, f)
			}
		}
	}
	// Columns.
	for ci := range g.cols {
		if g.cols[ci].box == nil {
			continue
		}
		if r, ok := g.colsRect(ci, ci+1); ok {
			if f := bgFragment(g.cols[ci].box, r); f != nil {
				layers = append(layers, f)
			}
		}
	}
	// Row groups.
	for _, rg := range g.rowGroups {
		if r, ok := g.rowsRect(rg.start, rg.end); ok {
			if f := bgFragment(rg.box, r); f != nil {
				layers = append(layers, f)
			}
		}
	}
	// Rows (painted last / closest to the cells).
	for _, gr := range g.rows {
		if gr.box == nil {
			continue
		}
		if r, ok := g.rowBoxRect(gr); ok {
			if f := bgFragment(gr.box, r); f != nil {
				layers = append(layers, f)
			}
		}
	}
	return layers
}

// colsRect returns the page-space rect spanning columns [start,end) across all rows that
// have a cell in those columns (the union of those cells' border boxes), and ok=false
// when no cell occupies the range (nothing to paint behind).
func (g *tableGrid) colsRect(start, end int) (rect, bool) {
	var u rect
	any := false
	for _, gc := range g.cells {
		if gc.frag == nil || gc.col >= end || gc.col+gc.colSpan <= start {
			continue
		}
		u = unionRect(u, fragRect(gc.frag), any)
		any = true
	}
	return u, any
}

// rowsRect returns the union border-box rect of every cell originating in rows [start,end).
func (g *tableGrid) rowsRect(start, end int) (rect, bool) {
	var u rect
	any := false
	for _, gc := range g.cells {
		if gc.frag == nil || gc.row >= end || gc.row+gc.rowSpan <= start {
			continue
		}
		u = unionRect(u, fragRect(gc.frag), any)
		any = true
	}
	return u, any
}

// rowBoxRect returns the union border-box rect of the cells originating in gr.
func (g *tableGrid) rowBoxRect(gr *gridRow) (rect, bool) {
	var u rect
	any := false
	for _, gc := range gr.cells {
		if gc.frag == nil {
			continue
		}
		u = unionRect(u, fragRect(gc.frag), any)
		any = true
	}
	return u, any
}

// bgFragment builds a background-only fragment covering r when box has a visible
// (non-zero-alpha) background color, else nil. The fragment carries only Background +
// Box (so the flatten emits one BackgroundKind item); it has no border or content.
func bgFragment(box *cssbox.Box, r rect) *Fragment {
	if box == nil {
		return nil
	}
	bg := box.Style.BackgroundColor
	if bg.A == 0 {
		return nil
	}
	return &Fragment{X: r.x, Y: r.y, W: r.w, H: r.h, Background: bg, Box: box}
}

// fragRect returns f's border box as a rect.
func fragRect(f *Fragment) rect { return rect{x: f.X, y: f.Y, w: f.W, h: f.H} }

// unionRect returns the bounding rect of a and b; when hasA is false a is ignored and b
// is returned (so the first element seeds the union).
func unionRect(a, b rect, hasA bool) rect {
	if !hasA {
		return b
	}
	x0 := a.x
	if b.x < x0 {
		x0 = b.x
	}
	y0 := a.y
	if b.y < y0 {
		y0 = b.y
	}
	x1 := a.x + a.w
	if b.x+b.w > x1 {
		x1 = b.x + b.w
	}
	y1 := a.y + a.h
	if b.y+b.h > y1 {
		y1 = b.y + b.h
	}
	return rect{x: x0, y: y0, w: x1 - x0, h: y1 - y0}
}

// cellWidth returns the column-band width allocated to a cell: the sum of its spanned
// column widths plus the inter-column border-spacing it spans over. This is passed to
// layoutBlock as the cell's containing-block width (layoutBlock derives the cell's own
// border/content box from it).
func (g *tableGrid) cellWidth(gc *gridCell) float64 {
	w := 0.0
	for i := 0; i < gc.colSpan; i++ {
		w += g.cols[gc.col+i].width
	}
	w += float64(gc.colSpan-1) * g.spacingH
	return w
}

// rowBandHeight is the total height a cell spans = sum of its rows' heights + the
// inter-row border-spacing between them.
func (gc *gridCell) rowBandHeight(g *tableGrid) float64 {
	h := 0.0
	for i := 0; i < gc.rowSpan; i++ {
		h += g.rows[gc.row+i].height
	}
	h += float64(gc.rowSpan-1) * g.spacingV
	return h
}

// solveFixedWidths implements CSS 17.5.2.1: column widths from the first row's cells
// + <col> hints + the table width, content-independent. The table used content width
// fills its container (width:auto), or grows to the column sum if larger. Auto columns
// split the leftover equally.
func (e *Engine) solveFixedWidths(g *tableGrid, contentW float64) {
	// A percentage column width resolves against the width AVAILABLE TO COLUMNS — the
	// table content width minus the total inter-column border-spacing — matching the auto
	// layout path (solveAutoWidths). Using the full contentW here would over-size
	// percentage columns by the spacing amount (the F6 fix; only observable with
	// border-spacing > 0 + percentage columns).
	spacing := g.spacingH * float64(len(g.cols)+1)
	used := contentW - spacing
	if used < 0 {
		used = 0
	}
	if len(g.rows) > 0 {
		for _, gc := range g.rows[0].cells {
			if w, ok := specifiedFixedWidth(gc.box); ok {
				per := w / float64(gc.colSpan)
				for i := 0; i < gc.colSpan; i++ {
					if !g.cols[gc.col+i].hasWidth {
						g.cols[gc.col+i].hasWidth = true
						g.cols[gc.col+i].width = per
					}
				}
			}
			if pct, ok := pctWidthOf(gc.box); ok {
				for i := 0; i < gc.colSpan; i++ {
					if g.cols[gc.col+i].pct < 0 {
						g.cols[gc.col+i].pct = pct
					}
				}
			}
		}
	}
	for ci := range g.cols {
		if g.cols[ci].pct >= 0 && !g.cols[ci].hasWidth {
			g.cols[ci].hasWidth = true
			g.cols[ci].width = used * g.cols[ci].pct / 100
		}
	}
	fixedSum := 0.0
	autoCount := 0
	for ci := range g.cols {
		if g.cols[ci].hasWidth {
			fixedSum += g.cols[ci].width
		} else {
			autoCount++
		}
	}
	// used already excludes the inter-column spacing, so the remaining width for auto
	// columns is just used - fixedSum (no second spacing subtraction).
	remain := used - fixedSum
	if remain < 0 {
		remain = 0
	}
	if autoCount > 0 {
		per := remain / float64(autoCount)
		for ci := range g.cols {
			if !g.cols[ci].hasWidth {
				g.cols[ci].width = per
			}
		}
	} else if remain > 0 && fixedSum > 0 {
		for ci := range g.cols {
			g.cols[ci].width += remain * (g.cols[ci].width / fixedSum)
		}
	}
}

// distributeAuto sets .width on every column whose index is in idxs, distributing
// `budget` across them: each column gets at least its min-content width, then the
// surplus (budget − Σmin) is handed out in proportion to each column's flex room
// (max − min). When the columns have no flex room (Σmax == Σmin) the surplus is split
// equally instead. This conserves exactly — the assigned widths sum to `budget` (when
// budget ≥ Σmin) — and is the shared core of both the percentage-leftover and the
// all-columns distribution passes in solveAutoWidths.
func distributeAuto(cols []gridCol, idxs []int, budget float64) {
	sumMin, sumMax := 0.0, 0.0
	for _, ci := range idxs {
		sumMin += cols[ci].min
		sumMax += cols[ci].max
	}
	if budget <= sumMin || sumMax == sumMin {
		for _, ci := range idxs {
			cols[ci].width = cols[ci].min
		}
		if budget > sumMin && len(idxs) > 0 {
			extra := (budget - sumMin) / float64(len(idxs))
			for _, ci := range idxs {
				cols[ci].width += extra
			}
		}
		return
	}
	surplus := budget - sumMin
	flex := sumMax - sumMin
	for _, ci := range idxs {
		span := cols[ci].max - cols[ci].min
		cols[ci].width = cols[ci].min + surplus*(span/flex)
	}
}

// isEmptyCellFragment reports whether a cell fragment has no rendered content — no
// inline lines (text), no block/atomic children, no replaced image, no form control,
// no floats, and no positioned descendants. Used by CSS empty-cells:hide to decide
// whether a cell's border and background are suppressed. A cell holding only collapsed
// whitespace produces no line boxes, so it reads as empty (matching browsers).
func isEmptyCellFragment(f *Fragment) bool {
	return len(f.Lines) == 0 && len(f.Children) == 0 && f.Image == nil &&
		f.Control == nil && len(f.Floats) == 0 && len(f.Positioned) == 0
}

// cellMinMax returns a cell's min-content and max-content BORDER-box widths: the
// measured min/max-content plus the cell's own horizontal border+padding. A specified
// (non-auto, non-percentage) width pins both — it raises the min floor and sets the
// max — with max never allowed below min. This is the shared per-cell intrinsic-width
// computation used by both the non-spanning column pass and the colspan distribution.
func (e *Engine) cellMinMax(ctx context.Context, gc *gridCell) (mn, mx float64) {
	mn = e.measureMinContent(ctx, gc.box) + horizontalEdges(gc.box)
	mx = e.measureMaxContent(ctx, gc.box) + horizontalEdges(gc.box)
	if w, ok := specifiedFixedWidth(gc.box); ok {
		ew := w + horizontalEdges(gc.box)
		if ew > mn {
			mn = ew
		}
		mx = ew
		if mx < mn {
			mx = mn
		}
	}
	return mn, mx
}

// solveAutoWidths implements CSS 17.5.2.2 (automatic table layout): per-column
// min/max content widths from non-spanning cells, the table used width, and a
// distribution of that width across columns between their min and max. Spanning-cell
// contributions (distributeSpanWidths), percentage columns, and the non-spanning
// common case are all handled here.
func (e *Engine) solveAutoWidths(ctx context.Context, g *tableGrid, contentW float64) {
	for ci := range g.cols {
		g.cols[ci].min = 0
		g.cols[ci].max = 0
	}
	for _, gc := range g.cells {
		if gc.colSpan != 1 {
			continue
		}
		mn, mx := e.cellMinMax(ctx, gc)
		col := &g.cols[gc.col]
		if mn > col.min {
			col.min = mn
		}
		if mx > col.max {
			col.max = mx
		}
		if col.hasWidth && col.width > col.min {
			col.min = col.width
			if col.max < col.min {
				col.max = col.min
			}
		}
		if pct, ok := pctWidthOf(gc.box); ok && pct > g.cols[gc.col].pct {
			g.cols[gc.col].pct = pct
		}
	}
	e.distributeSpanWidths(ctx, g) // colspan cells raise spanned columns' min/max before distribution

	for ci := range g.cols {
		if g.cols[ci].max < g.cols[ci].min {
			g.cols[ci].max = g.cols[ci].min
		}
	}

	spacing := g.spacingH * float64(len(g.cols)+1)
	sumMin, sumMax := 0.0, 0.0
	for ci := range g.cols {
		sumMin += g.cols[ci].min
		sumMax += g.cols[ci].max
	}
	avail := contentW - spacing
	if avail < 0 {
		avail = 0
	}
	var used float64
	if w, ok := specifiedFixedWidth(g.table); ok {
		used = w - spacing
	} else if _, pct := pctWidthOf(g.table); pct {
		// A PERCENTAGE table width (the classic <table width="100%">) is a DEFINITE width:
		// the caller has already resolved it into contentW, so the table fills the available
		// content width and the surplus over the columns' max-content is distributed across
		// them (CSS 17.5.2 — a specified table width ≥ the columns' max is honored). This is
		// distinct from width:auto (the else branch), which shrink-wraps to sumMax.
		used = avail
	} else {
		used = sumMax
		if used > avail {
			used = avail
		}
	}
	if used < sumMin {
		used = sumMin
	}

	// Percentage columns reserve their share of the used width (clamped to >= min).
	// The remaining (non-percentage) columns distribute the leftover by min/max below.
	pctReserved := 0.0
	pctCols := 0
	for ci := range g.cols {
		if g.cols[ci].pct >= 0 {
			want := used * g.cols[ci].pct / 100
			if want < g.cols[ci].min {
				want = g.cols[ci].min
			}
			g.cols[ci].width = want
			pctReserved += want
			pctCols++
		}
	}
	if pctCols > 0 {
		leftover := used - pctReserved
		// Percentage minimums can collectively exceed `used` when the table is narrower
		// than their mins; clamp the leftover for the auto columns to zero.
		if leftover < 0 {
			leftover = 0
		}
		var autoIdx []int
		for ci := range g.cols {
			if g.cols[ci].pct < 0 {
				autoIdx = append(autoIdx, ci)
			}
		}
		distributeAuto(g.cols, autoIdx, leftover)
		return // percentage path complete; skip the all-columns distribution
	}

	allIdx := make([]int, len(g.cols))
	for ci := range g.cols {
		allIdx[ci] = ci
	}
	distributeAuto(g.cols, allIdx, used)
}

// distributeSpanWidths adds spanning cells' min/max to the columns they cross (CSS
// 17.5.2.2): if a colspan cell's min (or max) exceeds the sum of its columns' current
// min (or max), the excess is distributed across those columns in proportion to each
// column's current max-min headroom (evenly if all equal). Inter-column border-spacing
// the span covers is excluded from the cell's contribution.
func (e *Engine) distributeSpanWidths(ctx context.Context, g *tableGrid) {
	for _, gc := range g.cells {
		if gc.colSpan == 1 {
			continue
		}
		mn, mx := e.cellMinMax(ctx, gc)
		innerSpacing := float64(gc.colSpan-1) * g.spacingH
		mn -= innerSpacing
		mx -= innerSpacing
		if mn < 0 {
			mn = 0
		}
		if mx < mn {
			mx = mn
		}
		distributeExcess(g.cols, gc.col, gc.colSpan, mn, false)
		distributeExcess(g.cols, gc.col, gc.colSpan, mx, true)
	}
}

// distributeExcess raises the columns [col..col+span) so their summed min (or max,
// when toMax) is at least target, distributing the shortfall in proportion to each
// column's current max-min headroom (evenly if all zero).
func distributeExcess(cols []gridCol, col, span int, target float64, toMax bool) {
	cur := 0.0
	for i := 0; i < span; i++ {
		if toMax {
			cur += cols[col+i].max
		} else {
			cur += cols[col+i].min
		}
	}
	if cur >= target {
		return
	}
	short := target - cur
	headroom := 0.0
	for i := 0; i < span; i++ {
		headroom += cols[col+i].max - cols[col+i].min
	}
	for i := 0; i < span; i++ {
		var share float64
		if headroom > 0 {
			share = short * ((cols[col+i].max - cols[col+i].min) / headroom)
		} else {
			share = short / float64(span)
		}
		if toMax {
			cols[col+i].max += share
		} else {
			cols[col+i].min += share
			if cols[col+i].max < cols[col+i].min {
				cols[col+i].max = cols[col+i].min
			}
		}
	}
}

// distributeRowspanHeights grows the rows a rowspan cell covers so their summed height
// (plus inter-row border-spacing) is at least the cell's border-box height — a spanning
// cell's height is distributed across its rows. The excess is split in proportion to the
// rows' current heights (evenly if all zero). One top-to-bottom pass (deterministic;
// sufficient for the common case).
func (e *Engine) distributeRowspanHeights(g *tableGrid) {
	for _, gc := range g.cells {
		if gc.rowSpan == 1 || gc.frag == nil {
			continue
		}
		cur := 0.0
		for i := 0; i < gc.rowSpan; i++ {
			cur += g.rows[gc.row+i].height
		}
		cur += float64(gc.rowSpan-1) * g.spacingV
		need := gc.frag.H
		if need <= cur {
			continue
		}
		short := need - cur
		sum := 0.0
		for i := 0; i < gc.rowSpan; i++ {
			sum += g.rows[gc.row+i].height
		}
		for i := 0; i < gc.rowSpan; i++ {
			var share float64
			if sum > 0 {
				share = short * (g.rows[gc.row+i].height / sum)
			} else {
				share = short / float64(gc.rowSpan)
			}
			g.rows[gc.row+i].height += share
		}
	}
}

// stretchCellFragment positions a cell's border-box fragment at (x,y) and stretches
// it to (w,h) — table cells fill their row band. It translates the fragment's whole
// subtree so its top-left lands at (x,y), then sets the border box to (w,h). Content
// keeps its top-left origin; vertical-align shift is applied separately by applyCellVAlign.
func stretchCellFragment(f *Fragment, x, y, w, h float64) {
	translateFragment(f, x-f.X, y-f.Y)
	f.W = w
	f.H = h
}

// applyCellVAlign shifts a cell's content down within its row band per vertical-align.
// b is the cell's cssbox.Box (used to read its VerticalAlign style).
// natH is the content's natural (pre-stretch) height; bandH the row band height.
// top keeps content at the band top; middle centers it; bottom drops it to the band
// bottom. The shift moves the fragment's children and inline lines, leaving the border
// box (which fills the band) in place. vertical-align:baseline is NOT handled here — it
// is a per-row decision applied by alignBaselineCellContents (so a row's baseline cells
// share one baseline); a cell reaching the default arm has top/sub/super/text-* (or no
// text baseline) and falls back to top.
func applyCellVAlign(f *Fragment, b *cssbox.Box, natH, bandH float64) {
	va := b.Style.VerticalAlign
	var dy float64
	switch va {
	case "bottom":
		dy = bandH - natH
	case "middle":
		dy = (bandH - natH) / 2
	default:
		dy = 0 // top, baseline-without-text, and sub/super/text-* fall back to top
	}
	if dy <= 0 { // top: no shift; content taller than band: no negative shift
		return
	}
	shiftCellContent(f, dy)
}

// alignBaselineCellContents aligns the first text baselines of a row's
// vertical-align:baseline cells: it shifts each cell's CONTENT down (via shiftCellContent,
// the same mechanism applyCellVAlign uses) so every cell's first baseline coincides with
// the group's deepest first baseline. It reuses the shared firstBaselineOffset to read
// each cell's first-baseline offset from its band top (cells stretch to the band, so all
// share the same top Y); a cell with no text baseline (firstBaselineOffset ok=false) is
// skipped and stays top-aligned (CSS Box Alignment baseline→start fallback). Returns a
// CONSERVATIVE extra band height — the largest downward content shift applied — which the
// caller uses to grow the row band so the shifted content is contained (mirroring
// alignBaselineGroup's contract, but shifting cell content rather than the cell box). When
// the group is empty or all baselines already coincide (e.g. equal-font cells), the return
// is 0 and nothing shifts (byte-identical to the prior per-cell top behavior).
func alignBaselineCellContents(cells []*Fragment) float64 {
	maxBaseline := 0.0
	offs := make([]float64, len(cells))
	oks := make([]bool, len(cells))
	for i, f := range cells {
		off, ok := firstBaselineOffset(f)
		offs[i], oks[i] = off, ok
		if ok && off > maxBaseline {
			maxBaseline = off
		}
	}
	extra := 0.0
	for i, f := range cells {
		if !oks[i] {
			continue
		}
		dy := maxBaseline - offs[i]
		if dy > 0 {
			shiftCellContent(f, dy)
			if dy > extra {
				extra = dy
			}
		}
	}
	return extra
}

// shiftCellContent translates a cell fragment's content down by dy without moving the
// cell's own border box: its child fragments, its BFC floats (a cell is a BFC, so a
// floated child lives in Floats, not Children), and its inline lines all shift. (An
// abs/fixed descendant in f.Positioned is NOT shifted — it is positioned against the
// cell's content box, not the in-flow content, per CSS.)
func shiftCellContent(f *Fragment, dy float64) {
	for _, c := range f.Children {
		translateFragment(c, 0, dy)
	}
	for _, fl := range f.Floats {
		translateFragment(fl, 0, dy)
	}
	for i := range f.Lines {
		f.Lines[i].BaselineY += dy
	}
}
