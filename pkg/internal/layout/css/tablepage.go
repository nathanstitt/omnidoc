package css

import (
	"sort"

	"github.com/nathanstitt/omnidoc/pkg/internal/layout"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/cssbox"
)

// splitTableForPage splits a table fragment BETWEEN rows at pageBottom. The CSS table
// engine emits the table's cells directly as the table fragment's children (positioned by
// the grid), so a "row" is recovered as a vertical band of cells sharing a top Y. Bands
// whose bottom is fully above pageBottom stay in the head table; the rest (whole bands)
// move to the tail table (its Y shifted to the first kept band, top border suppressed).
// Mid-cell content is NOT split — a row (and any cell spanning rows) rides one page.
// Returns {head:tbl} if all rows fit, {tail:tbl} if the first row alone overflows the
// page from the top.
func splitTableForPage(tbl *Fragment, pageBottom float64) splitResult {
	rows := tableRowBands(tbl)
	if len(rows) == 0 {
		if tbl.Y+tbl.H <= pageBottom+0.5 {
			return splitResult{head: tbl}
		}
		return splitResult{tail: tbl}
	}
	k := 0
	for i, r := range rows {
		if r.bottom <= pageBottom+0.5 {
			k = i + 1
		} else {
			break
		}
	}
	if k >= len(rows) {
		return splitResult{head: tbl}
	}
	// The row at k straddles the boundary. Split THROUGH its cells so the part that
	// fits stays on this page, rather than pushing the whole row down — which for a
	// single over-tall row means the table overflows and is clipped instead.
	//
	// A cell's content is an ordinary fragment spine, not an opaque nested formatting
	// context, so the recursive splitter handles it with no relayout: the cell's own
	// height was resolved by the row, and its lines are already positioned in page
	// space. Only a cell that declines to split (nothing to break on, or
	// `break-inside: avoid`) forces the row to stay whole.
	if res, ok := splitTableRowThroughCells(tbl, rows[k], pageBottom, k > 0); ok {
		return res
	}
	if k == 0 {
		return splitResult{tail: tbl} // first row unsplittable: move whole, overflow
	}
	splitY := rows[k].top
	head := *tbl
	tail := *tbl
	head.Children = cellsAbove(tbl, splitY)
	tail.Children = cellsFrom(tbl, splitY)
	head.H = rows[k-1].bottom - tbl.Y
	tail.Y = splitY
	tail.H = (tbl.Y + tbl.H) - splitY
	head.Border[layout.EdgeBottom] = BorderEdge{}
	tail.Border[layout.EdgeTop] = BorderEdge{}
	// Collapsed border strips, if any, are dropped on the split (a documented limitation —
	// the collapsed grid is computed table-wide and is not re-derived per page fragment).
	head.Collapsed, tail.Collapsed = nil, nil
	added := repeatHeaderOnTail(tbl, &tail, splitY)
	return splitResult{head: &head, tail: &tail, grow: growthAt(tbl, added)}
}

// splitTableRowThroughCells splits the straddling row's cells at pageBottom, producing
// a head table whose last row is partial and a tail table that resumes it.
//
// ok=false when the row cannot be split — no cell yields both a head and a tail — in
// which case the caller falls back to breaking between whole rows.
//
// hasEarlierRows says whether rows above this one already fit on the page; when they
// do, a row that refuses to split is not a failure (the caller can still break above
// it), but when it is the FIRST row the alternative is overflowing the page, so the
// attempt matters more.
func splitTableRowThroughCells(tbl *Fragment, row tableRow, pageBottom float64, hasEarlierRows bool) (splitResult, bool) {
	var headCells, tailCells []*Fragment
	split := false
	for _, c := range tbl.Children {
		if c.IsFloat || c.IsPositioned {
			continue // routed below, with the rest of the out-of-flow content
		}
		switch {
		case c.Y+c.H <= row.top+0.5:
			headCells = append(headCells, c) // a row entirely above the straddler
		case c.Y >= row.bottom-0.5:
			tailCells = append(tailCells, c) // a row entirely below it
		default:
			// A cell of the straddling row.
			if lineSplittable(c) {
				if sub := splitAnyBlockForPage(c, pageBottom, 0, 0); sub.head != nil && sub.tail != nil {
					headCells = append(headCells, sub.head)
					tailCells = append(tailCells, sub.tail)
					split = true
					continue
				}
			}
			// This cell cannot break: it rides the tail whole. Its row-mates may still
			// have split, which is correct — a row's cells fragment independently, and
			// the tail row simply starts with this cell's full content.
			tailCells = append(tailCells, c)
		}
	}
	if !split || len(headCells) == 0 {
		return splitResult{}, false
	}
	head := *tbl
	tail := *tbl
	head.Children = headCells
	tail.Children = tailCells
	head.H = lastChildBottom(headCells) - tbl.Y
	tail.Y = firstChildTop(tailCells)
	tail.H = (tbl.Y + tbl.H) - tail.Y
	head.Border[layout.EdgeBottom] = BorderEdge{}
	tail.Border[layout.EdgeTop] = BorderEdge{}
	// Collapsed border strips are table-wide and not re-derived per fragment (the same
	// documented limitation as the between-rows path).
	head.Collapsed, tail.Collapsed = nil, nil
	distributeOutOfFlow(tbl, &head, &tail, pageBottom)
	added := repeatHeaderOnTail(tbl, &tail, pageBottom)
	return splitResult{head: &head, tail: &tail, grow: growthAt(tbl, added)}, true
}

// repeatHeaderOnTail prepends a copy of the table's <thead> cells to the tail fragment,
// translated to sit at the tail's top, so a table continued onto another page keeps its
// column headings.
//
// It is a no-op when the table has no header, or when the split falls INSIDE the header
// itself — repeating a header above its own remainder would duplicate it.
//
// The cells are deep-cloned rather than shared: the same header appears on every
// continuation page, and each page shifts its fragments into a local frame in place, so
// sharing one cell across pages would have each shift corrupt the others.
func repeatHeaderOnTail(tbl, tail *Fragment, splitY float64) (added float64) {
	if tbl.HeaderBottom <= tbl.Y || splitY <= tbl.HeaderBottom+0.5 {
		return 0
	}
	// The header starts below a top caption: the caption belongs to the table's first
	// part only, so it is neither repeated nor counted in the repeated height.
	headerTop := tbl.Y
	for _, c := range tbl.Children {
		if isCaptionFragment(c) && c.Y+c.H <= tbl.HeaderBottom+0.5 && c.Y+c.H > headerTop {
			headerTop = c.Y + c.H
		}
	}
	var repeated []*Fragment
	for _, c := range tbl.Children {
		if c.IsFloat || c.IsPositioned || isCaptionFragment(c) || c.Y+c.H > tbl.HeaderBottom+0.5 {
			continue // not a header cell
		}
		// cloneFloatForPageMap is a general alias-preserving deep clone (its name is
		// historical — it is not float-specific); the seen map is per-cell because
		// header cells share no descendants.
		clone := cloneFloatForPageMap(c, map[*Fragment]*Fragment{})
		translateFragment(clone, 0, tail.Y-headerTop)
		repeated = append(repeated, clone)
	}
	if len(repeated) == 0 {
		return 0
	}
	// The repeated header occupies space at the top of the tail, so the body cells that
	// follow it move down by its height and the tail grows to match. The caller reports
	// that growth (splitResult.grow) so the content after the table moves down too.
	headerH := tbl.HeaderBottom - headerTop
	for _, c := range tail.Children {
		translateFragment(c, 0, headerH)
	}
	tail.Children = append(repeated, tail.Children...)
	tail.H += headerH
	// The tail now carries its own copy of the header at its top. Re-anchor
	// HeaderBottom to THAT copy, so if the tail is split again — a table spanning three
	// or more pages — the next continuation repeats the header too. Leaving the
	// original table's value here would point at coordinates the tail no longer has,
	// and the repeat would silently stop after the second page.
	tail.HeaderBottom = tail.Y + headerH
	return headerH
}

// isCaptionFragment reports whether f is a table caption's fragment.
func isCaptionFragment(f *Fragment) bool {
	return f != nil && f.Box != nil && f.Box.Display == cssbox.DisplayTableCaption
}

// tableRow is one recovered table row: the vertical extent of a band of cells that share
// a top Y (a row in the grid). A cell spanning multiple rows extends its band's bottom,
// so a rowspanning cell keeps its rows together (it is not cut).
type tableRow struct{ top, bottom float64 }

// tableRowBands groups a table fragment's in-flow cell children into vertical row bands,
// merging cells whose Y-extents overlap (so a rowspanning cell joins the bands it covers),
// sorted top-to-bottom. Out-of-flow children (floats/positioned) are ignored.
func tableRowBands(tbl *Fragment) []tableRow {
	var bands []tableRow
	for _, c := range inFlowChildren(tbl) {
		t, b := c.Y, c.Y+c.H
		merged := false
		for i := range bands {
			if t < bands[i].bottom-0.5 && b > bands[i].top+0.5 { // overlap → same band
				if t < bands[i].top {
					bands[i].top = t
				}
				if b > bands[i].bottom {
					bands[i].bottom = b
				}
				merged = true
				break
			}
		}
		if !merged {
			bands = append(bands, tableRow{top: t, bottom: b})
		}
	}
	sort.Slice(bands, func(i, j int) bool { return bands[i].top < bands[j].top })
	return bands
}

// cellsAbove returns the table's child cells whose top is above splitY (the head page's
// rows). Out-of-flow children ride the head (they were positioned in the table's space).
func cellsAbove(tbl *Fragment, splitY float64) []*Fragment {
	var out []*Fragment
	for _, c := range tbl.Children {
		if c.Y < splitY-0.5 {
			out = append(out, c)
		}
	}
	return out
}

// cellsFrom returns the table's child cells whose top is at or below splitY (the tail
// page's rows).
func cellsFrom(tbl *Fragment, splitY float64) []*Fragment {
	var out []*Fragment
	for _, c := range tbl.Children {
		if c.Y >= splitY-0.5 {
			out = append(out, c)
		}
	}
	return out
}
