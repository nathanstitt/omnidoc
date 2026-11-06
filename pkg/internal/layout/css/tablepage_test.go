package css

import (
	"context"
	"strings"
	"testing"

	gcss "github.com/nathanstitt/omnidoc/pkg/internal/css"
	"github.com/nathanstitt/omnidoc/pkg/internal/html"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/cssbox"
)

// makeTable builds a table fragment as the CSS table engine produces it: the table
// fragment's direct children are CELL fragments (DisplayTableCell), two per row, each row
// a vertical band of height rowH stacked from y0. (The engine flattens rows into a grid of
// cell fragments; tableRowBands recovers the rows as Y bands.)
func makeTable(y0, rowH float64, rowCount int) *Fragment {
	box := &cssbox.Box{Kind: cssbox.BoxBlock, Display: cssbox.DisplayTable}
	t := &Fragment{Y: y0, H: float64(rowCount) * rowH, Box: box}
	for i := 0; i < rowCount; i++ {
		cy := y0 + float64(i)*rowH
		for c := 0; c < 2; c++ { // two cells per row, same Y band
			cb := &cssbox.Box{Kind: cssbox.BoxBlock, Display: cssbox.DisplayTableCell}
			t.Children = append(t.Children, &Fragment{X: float64(c) * 50, Y: cy, W: 50, H: rowH, Box: cb})
		}
	}
	return t
}

func TestSplitTableBetweenRows(t *testing.T) {
	// 6 rows of 20pt at y0=0 (table 120pt), 2 cells each. Page bottom 65 ⇒ 3 rows fit
	// (bottoms 20/40/60). Head keeps rows 1-3 (6 cells), tail rows 4-6 (6 cells).
	tbl := makeTable(0, 20, 6)
	res := splitTableForPage(tbl, 65)
	if res.head == nil || res.tail == nil {
		t.Fatalf("expected a table split, got head=%v tail=%v", res.head, res.tail)
	}
	if len(res.head.Children) != 6 {
		t.Errorf("head cells = %d, want 6 (3 rows × 2)", len(res.head.Children))
	}
	if len(res.tail.Children) != 6 {
		t.Errorf("tail cells = %d, want 6 (3 rows × 2)", len(res.tail.Children))
	}
	// The split lands cleanly on a row boundary: head ends at row 3's bottom (60), the tail
	// starts at row 4's top (60).
	if res.head.H != 60 {
		t.Errorf("head H = %.1f, want 60 (rows 1-3)", res.head.H)
	}
	if res.tail.Y != 60 {
		t.Errorf("tail Y = %.1f, want 60 (row 4 top)", res.tail.Y)
	}
}

// A rowspanning cell (a tall cell overlapping two row bands) keeps its rows together: the
// band merge means the splitter never cuts through it.
func TestSplitTableRowspanStaysWhole(t *testing.T) {
	box := &cssbox.Box{Kind: cssbox.BoxBlock, Display: cssbox.DisplayTable}
	tbl := &Fragment{Y: 0, H: 80, Box: box}
	cell := func(x, y, h float64) *Fragment {
		return &Fragment{X: x, Y: y, W: 50, H: h, Box: &cssbox.Box{Kind: cssbox.BoxBlock, Display: cssbox.DisplayTableCell}}
	}
	// Rows at y 0,20,40,60 (each 20 tall). Left column of rows 1-2 is a single rowspan=2
	// cell (y 0, h 40), so rows 1 and 2 merge into one band [0,40).
	tbl.Children = []*Fragment{
		cell(0, 0, 40), cell(50, 0, 20), // row1: rowspan cell + normal cell
		cell(50, 20, 20), // row2: right cell only (left is spanned)
		cell(0, 40, 20), cell(50, 40, 20),
		cell(0, 60, 20), cell(50, 60, 20),
	}
	// Page bottom 50 ⇒ band [0,40) fits, band [40,60) does not. The rowspan band is not cut.
	res := splitTableForPage(tbl, 50)
	if res.head == nil || res.tail == nil {
		t.Fatalf("expected a split, got head=%v tail=%v", res.head, res.tail)
	}
	if res.head.H != 40 {
		t.Errorf("head H = %.1f, want 40 (the merged rowspan band)", res.head.H)
	}
	// Head holds the 3 cells of the merged band; tail holds the remaining 4.
	if len(res.head.Children) != 3 || len(res.tail.Children) != 4 {
		t.Errorf("split %d/%d cells, want 3/4", len(res.head.Children), len(res.tail.Children))
	}
}

// All rows fit ⇒ no split (head == tbl).
func TestSplitTableAllRowsFit(t *testing.T) {
	tbl := makeTable(0, 20, 3)
	res := splitTableForPage(tbl, 1000)
	if res.head != tbl || res.tail != nil {
		t.Errorf("all-fit should yield head=tbl,tail=nil; got head=%v tail=%v", res.head, res.tail)
	}
}

// The first row alone overflows the page ⇒ move the whole table (it overflows; a row is
// not split mid-cell).
func TestSplitTableFirstRowOverflows(t *testing.T) {
	tbl := makeTable(0, 100, 3)
	res := splitTableForPage(tbl, 50)
	if res.head != nil || res.tail != tbl {
		t.Errorf("first-row-overflow should move whole table; got head=%v tail=%v", res.head, res.tail)
	}
}

// lineSplittable accepts a table fragment (so the bucketer routes it to the table splitter).
func TestLineSplittableTable(t *testing.T) {
	tbl := makeTable(0, 20, 3)
	if !lineSplittable(tbl) {
		t.Errorf("a table fragment should be splittable")
	}
	// break-inside: avoid on the table disqualifies it.
	tbl.Box.Style = gcss.ComputedStyle{BreakInside: "avoid"}
	if lineSplittable(tbl) {
		t.Errorf("break-inside:avoid table must not be splittable")
	}
}

// TestSplitTableThroughOverTallCell is the N1c case: a table with a SINGLE row whose
// cell is taller than the page.
//
// There is no row boundary to break at, so the table previously moved whole and
// overflowed — the content past the page bottom was simply clipped. A cell's content
// is an ordinary fragment spine rather than an opaque nested formatting context, so the
// recursive splitter breaks it with no relayout.
func TestSplitTableThroughOverTallCell(t *testing.T) {
	src := `<!DOCTYPE html><html><head><style>
	  body { margin: 0; font-family: serif; font-size: 16px; }
	  table { border-collapse: collapse; width: 300px; }
	  td { padding: 0; vertical-align: top; }
	</style></head><body>
	  <table><tr><td><p>` + strings.Repeat("word ", 300) + `</p></td></tr></table>
	</body></html>`
	doc, err := html.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root, err := Build(context.Background(), doc, nil, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	pages, err := New(nil, nil, nil).LayoutPaged(context.Background(), root, 400, 300)
	if err != nil {
		t.Fatalf("layout: %v", err)
	}
	if len(pages.Pages) < 2 {
		t.Fatalf("an over-tall cell should paginate: got %d page(s), want >= 2", len(pages.Pages))
	}
	// Every page carries text, and nothing is lost.
	total := 0
	for i, p := range pages.Pages {
		n := 0
		for _, it := range p.Items {
			if len(it.Glyph.Runes) > 0 {
				n++
			}
		}
		if n == 0 {
			t.Errorf("page %d has no glyphs", i)
		}
		total += n
	}
	if total < 1000 {
		t.Errorf("pages carry %d glyphs in total; content appears to have been lost", total)
	}
}

// TestSplitTableBetweenRowsStillPreferred pins that a multi-row table still breaks at a
// ROW boundary when one is available — splitting through cells is the fallback for a
// row that straddles, not the primary strategy.
func TestSplitTableBetweenRowsStillPreferred(t *testing.T) {
	tbl := &Fragment{Y: 0, H: 120, W: 100,
		Box: &cssbox.Box{Kind: cssbox.BoxBlock, Display: cssbox.DisplayTable}}
	cellBox := &cssbox.Box{Kind: cssbox.BoxBlock, Display: cssbox.DisplayTableCell}
	for r := 0; r < 3; r++ { // rows at y=0,40,80 — no lines, so no cell can split
		tbl.Children = append(tbl.Children,
			&Fragment{Y: float64(r) * 40, H: 40, W: 100, Box: cellBox})
	}
	res := splitTableForPage(tbl, 80)
	if res.head == nil || res.tail == nil {
		t.Fatalf("expected a split; got head=%v tail=%v", res.head, res.tail)
	}
	// The break lands on the row boundary at y=80, not inside a row.
	if res.tail.Y != 80 {
		t.Errorf("tail starts at y=%v, want 80 (the row boundary)", res.tail.Y)
	}
	if len(res.head.Children) != 2 || len(res.tail.Children) != 1 {
		t.Errorf("head=%d tail=%d cells, want 2 and 1",
			len(res.head.Children), len(res.tail.Children))
	}
}

// TestSplitTableUnsplittableRowMovesWhole: a straddling first row whose cells cannot
// break still moves whole rather than being clipped mid-cell.
func TestSplitTableUnsplittableRowMovesWhole(t *testing.T) {
	tbl := &Fragment{Y: 0, H: 200, W: 100,
		Box: &cssbox.Box{Kind: cssbox.BoxBlock, Display: cssbox.DisplayTable}}
	cellBox := &cssbox.Box{Kind: cssbox.BoxBlock, Display: cssbox.DisplayTableCell}
	// One row, one cell, no lines and no children: nothing to split on.
	tbl.Children = append(tbl.Children, &Fragment{Y: 0, H: 200, W: 100, Box: cellBox})
	res := splitTableForPage(tbl, 100)
	if res.head != nil {
		t.Errorf("an unsplittable single row should move whole; got a head of h=%v", res.head.H)
	}
	if res.tail != tbl {
		t.Error("expected the original table as the tail")
	}
}

// pagedTableText renders a long table and returns each page's text.
func pagedTableText(t *testing.T, tableHTML string, pageH float64) []string {
	t.Helper()
	src := `<!DOCTYPE html><html><head><style>
	  body { margin: 0; font-family: serif; font-size: 16px; }
	  table { width: 300px; } td, th { padding: 0; }
	</style></head><body>` + tableHTML + `</body></html>`
	doc, err := html.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root, err := Build(context.Background(), doc, nil, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	pages, err := New(nil, nil, nil).LayoutPaged(context.Background(), root, 400, pageH)
	if err != nil {
		t.Fatalf("layout: %v", err)
	}
	out := make([]string, len(pages.Pages))
	for i, p := range pages.Pages {
		var sb strings.Builder
		for _, it := range p.Items {
			for _, r := range it.Glyph.Runes {
				sb.WriteRune(r)
			}
		}
		out[i] = sb.String()
	}
	return out
}

// longTable builds a table of n body rows, optionally with a <thead>.
func longTable(n int, withHead bool) string {
	var b strings.Builder
	b.WriteString("<table>")
	if withHead {
		b.WriteString("<thead><tr><th>HEADER</th></tr></thead>")
	}
	b.WriteString("<tbody>")
	for i := 0; i < n; i++ {
		b.WriteString("<tr><td>body</td></tr>")
	}
	b.WriteString("</tbody></table>")
	return b.String()
}

// TestTableHeaderRepeatsOnEveryPage: a table split across pages repeats its <thead> on
// each continuation, which is what makes a long table readable.
//
// The re-anchoring matters and is easy to get wrong: the tail carries a CLONE of the
// header at its own top, so its HeaderBottom must point at that clone. Leaving the
// original table's value there makes the repeat stop after the second page.
func TestTableHeaderRepeatsOnEveryPage(t *testing.T) {
	pages := pagedTableText(t, longTable(40, true), 300)
	if len(pages) < 3 {
		t.Fatalf("want at least 3 pages to exercise re-splitting, got %d", len(pages))
	}
	for i, txt := range pages {
		if !strings.Contains(txt, "HEADER") {
			t.Errorf("page %d is missing the repeated header", i)
		}
	}
}

// TestTableWithoutHeaderRepeatsNothing pins the no-op path: a table with no <thead>
// gains nothing on its continuation pages.
func TestTableWithoutHeaderRepeatsNothing(t *testing.T) {
	pages := pagedTableText(t, longTable(40, false), 300)
	if len(pages) < 2 {
		t.Fatalf("want at least 2 pages, got %d", len(pages))
	}
	for i, txt := range pages {
		if strings.Contains(txt, "HEADER") {
			t.Errorf("page %d gained a header from a table that has none", i)
		}
	}
}

// TestTableHeaderNotDuplicatedOnFirstPage: the header appears ONCE on page 1 — it is
// repeated onto continuations, not re-emitted above itself.
func TestTableHeaderNotDuplicatedOnFirstPage(t *testing.T) {
	pages := pagedTableText(t, longTable(40, true), 300)
	if n := strings.Count(pages[0], "HEADER"); n != 1 {
		t.Errorf("page 0 contains %d copies of the header, want exactly 1", n)
	}
}

// TestRepeatHeaderNoOpWhenSplitInsideHeader: when the break falls inside the header
// itself, no repeat happens — there is no completed header to carry forward.
func TestRepeatHeaderNoOpWhenSplitInsideHeader(t *testing.T) {
	box := &cssbox.Box{Kind: cssbox.BoxBlock, Display: cssbox.DisplayTable}
	cellBox := &cssbox.Box{Kind: cssbox.BoxBlock, Display: cssbox.DisplayTableCell}
	// A real header cell must exist, or the loop finds nothing and returns early for a
	// reason unrelated to the guard under test.
	mk := func() (*Fragment, *Fragment) {
		tbl := &Fragment{Y: 0, H: 100, W: 100, Box: box, HeaderBottom: 60,
			Children: []*Fragment{
				{Y: 0, H: 60, W: 100, Box: cellBox},  // the header cell
				{Y: 60, H: 40, W: 100, Box: cellBox}, // a body cell
			}}
		tail := &Fragment{Y: 30, H: 70, W: 100, Box: box,
			Children: []*Fragment{{Y: 30, H: 70, W: 100, Box: cellBox}}}
		return tbl, tail
	}

	// Split at y=30 — INSIDE the header band [0,60): nothing to carry forward.
	tbl, tail := mk()
	before := len(tail.Children)
	repeatHeaderOnTail(tbl, tail, 30)
	if len(tail.Children) != before {
		t.Errorf("a split inside the header should repeat nothing; tail gained %d children",
			len(tail.Children)-before)
	}

	// Split at y=80 — BELOW the header: the header is complete and must be repeated.
	// This half is what proves the guard above is doing real work rather than the loop
	// happening to find nothing.
	tbl, tail = mk()
	before = len(tail.Children)
	repeatHeaderOnTail(tbl, tail, 80)
	if len(tail.Children) != before+1 {
		t.Errorf("a split below the header should repeat it; tail has %d children, want %d",
			len(tail.Children), before+1)
	}
}

// A table below other content lays out in its own frame and is then moved into
// page space. Its HeaderBottom must move with it, or splitTableForPage sees the
// header above the table's top and never repeats it.
func TestTableHeaderRepeatsWhenTheTableIsNotFirst(t *testing.T) {
	pages := pagedTableText(t, "<p>intro</p>"+longTable(40, true), 300)
	if len(pages) < 3 {
		t.Fatalf("want at least 3 pages, got %d", len(pages))
	}
	for i, txt := range pages {
		if !strings.Contains(txt, "HEADER") {
			t.Errorf("page %d is missing the repeated header", i)
		}
	}
}
