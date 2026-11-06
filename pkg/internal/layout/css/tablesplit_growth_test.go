package css

import (
	"context"
	"strings"
	"testing"

	"github.com/nathanstitt/omnidoc/pkg/internal/html"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout"
)

// pagedWord is one run of glyphs on one baseline of a page, in page order.
type pagedWord struct {
	text string
	y    float64
}

// layoutPagedHTML lays out a full HTML document onto pageH-tall pages.
func layoutPagedHTML(t *testing.T, src string, pageH float64) *layout.Pages {
	t.Helper()
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
	return pages
}

// pageWords groups each page's glyphs into words: consecutive glyphs on one baseline.
func pageWords(pages *layout.Pages) [][]pagedWord {
	out := make([][]pagedWord, len(pages.Pages))
	for i, p := range pages.Pages {
		var words []pagedWord
		for _, it := range p.Items {
			if it.Kind != layout.GlyphKind || len(it.Glyph.Runes) == 0 {
				continue
			}
			n := len(words)
			if n > 0 && words[n-1].y == it.Glyph.YPt {
				words[n-1].text += string(it.Glyph.Runes)
				continue
			}
			words = append(words, pagedWord{text: string(it.Glyph.Runes), y: it.Glyph.YPt})
		}
		out[i] = words
	}
	return out
}

const growthDocHead = `<!DOCTYPE html><html><head><style>
  body { margin: 0; font-family: serif; font-size: 16px; }
  table { width: 300px; } td, th, caption { padding: 0; }
</style></head><body>`

// longRowsTable is a table with a <thead>, optionally a <caption>, and n numbered rows.
func longRowsTable(n int, caption string) string {
	var b strings.Builder
	b.WriteString("<table>")
	if caption != "" {
		b.WriteString("<caption>" + caption + "</caption>")
	}
	b.WriteString("<thead><tr><th>HEADER</th></tr></thead><tbody>")
	for i := 0; i < n; i++ {
		b.WriteString("<tr><td>row</td></tr>")
	}
	b.WriteString("</tbody></table>")
	return b.String()
}

// assertAfterBelowRows checks that on the page holding AFTER, AFTER sits below every
// table row: a repeated header grows the split table, and what follows must move
// down with it rather than being drawn over the last rows.
func assertAfterBelowRows(t *testing.T, words [][]pagedWord) {
	t.Helper()
	for pi, ws := range words {
		afterY, found := 0.0, false
		lastRowY := -1.0
		for _, w := range ws {
			switch {
			case strings.Contains(w.text, "AFTER"):
				afterY, found = w.y, true
			case strings.Contains(w.text, "row"):
				if w.y > lastRowY {
					lastRowY = w.y
				}
			}
		}
		if !found {
			continue
		}
		if lastRowY >= 0 && afterY <= lastRowY+1 {
			t.Errorf("page %d: AFTER baseline %.1f is not below the last row baseline %.1f (overlap)", pi, afterY, lastRowY)
		}
		return
	}
	t.Fatal("AFTER was not laid out on any page")
}

func TestBlockAfterSplitTableStartsBelowIt(t *testing.T) {
	src := growthDocHead + longRowsTable(40, "") + `<p>AFTER</p></body></html>`
	words := pageWords(layoutPagedHTML(t, src, 300))
	if len(words) < 3 {
		t.Fatalf("want at least 3 pages (two header repeats), got %d", len(words))
	}
	assertAfterBelowRows(t, words)
}

// The same, with the table and the following paragraph inside a wrapper block, so
// the growth must reach the wrapper's later children through the nested split.
func TestNestedBlockAfterSplitTableStartsBelowIt(t *testing.T) {
	src := growthDocHead + `<div>` + longRowsTable(40, "") + `<p>AFTER</p></div><p>LAST</p></body></html>`
	words := pageWords(layoutPagedHTML(t, src, 300))
	if len(words) < 3 {
		t.Fatalf("want at least 3 pages (two header repeats), got %d", len(words))
	}
	assertAfterBelowRows(t, words)
}

// A table with a <caption> repeats its <thead> on each continuation page and shows
// the caption once, above the table's first part.
func TestCaptionedTableRepeatsHeaderNotCaption(t *testing.T) {
	src := growthDocHead + longRowsTable(40, "CAPTION") + `<p>AFTER</p></body></html>`
	words := pageWords(layoutPagedHTML(t, src, 300))
	if len(words) < 3 {
		t.Fatalf("want at least 3 pages, got %d", len(words))
	}
	captions := 0
	for pi, ws := range words {
		var page strings.Builder
		for _, w := range ws {
			page.WriteString(w.text + " ")
		}
		txt := page.String()
		captions += strings.Count(txt, "CAPTION")
		if strings.Contains(txt, "row") && !strings.Contains(txt, "HEADER") {
			t.Errorf("page %d has table rows but no repeated header", pi)
		}
	}
	if captions != 1 {
		t.Errorf("the caption appears %d times, want once", captions)
	}
	assertAfterBelowRows(t, words)
}

// A float after the split table lives on the root's float layer, not in the block
// list; it must move down with the growth too, or it lands over the table's rows.
func TestFloatAfterSplitTableStartsBelowIt(t *testing.T) {
	src := growthDocHead + longRowsTable(40, "") +
		`<div><div style="float: left; width: 80px">FLOATED</div><p>AFTER</p></div></body></html>`
	words := pageWords(layoutPagedHTML(t, src, 300))
	for pi, ws := range words {
		lastRowY, floatY := -1.0, -1.0
		for _, w := range ws {
			if strings.Contains(w.text, "row") && w.y > lastRowY {
				lastRowY = w.y
			}
			if strings.Contains(w.text, "FLOATED") {
				floatY = w.y
			}
		}
		if floatY < 0 {
			continue
		}
		if lastRowY >= 0 && floatY <= lastRowY+1 {
			t.Errorf("page %d: the float baseline %.1f is not below the last row baseline %.1f", pi, floatY, lastRowY)
		}
		return
	}
	t.Fatal("the float was not laid out on any page")
}

// A border-collapse table keeps its grid lines on every page it spans: each page
// draws a rule under every row it holds (and under the repeated header), and the
// column rules run the height of that page's rows.
func TestCollapsedBordersSurviveTableSplit(t *testing.T) {
	src := `<!DOCTYPE html><html><head><style>
  body { margin: 0; font-family: serif; font-size: 16px; }
  table { width: 300px; border-collapse: collapse; } td, th { padding: 0; border: 1px solid black; }
</style></head><body><p>BEFORE</p>` + longRowsTable(40, "") + `<p>AFTER</p></body></html>`
	pages := layoutPagedHTML(t, src, 300)
	if len(pages.Pages) < 3 {
		t.Fatalf("want at least 3 pages, got %d", len(pages.Pages))
	}
	for pi, p := range pages.Pages {
		rows, horizontal, vertical := 0, 0, 0
		for _, it := range p.Items {
			switch it.Kind {
			case layout.GlyphKind:
				if string(it.Glyph.Runes) == "r" {
					rows++ // the first letter of each "row" cell
				}
			case layout.BorderKind:
				if it.Border.WPt > it.Border.HPt {
					horizontal++
				} else {
					vertical++
				}
			}
		}
		if rows == 0 {
			continue
		}
		if horizontal < rows {
			t.Errorf("page %d: %d horizontal grid rules for %d rows", pi, horizontal, rows)
		}
		if vertical < rows {
			t.Errorf("page %d: %d vertical grid rules for %d rows", pi, vertical, rows)
		}
	}
}

// splitCollapsedBorders: a row rule on the split line goes to both sides, and a column
// rule crossing it (a row split through its cells) is clipped to each side.
func TestSplitCollapsedBordersClipsAtTheSplit(t *testing.T) {
	strips := []layout.BorderItem{
		{XPt: 0, YPt: 9.5, WPt: 100, HPt: 1, Side: layout.EdgeTop},   // rule at y=10
		{XPt: 0, YPt: 49.5, WPt: 100, HPt: 1, Side: layout.EdgeTop},  // rule at y=50 (the split)
		{XPt: 0, YPt: 89.5, WPt: 100, HPt: 1, Side: layout.EdgeTop},  // rule at y=90
		{XPt: -0.5, YPt: 30, WPt: 1, HPt: 40, Side: layout.EdgeLeft}, // column rule 30..70
	}
	head, tail := splitCollapsedBorders(strips, 50)
	if len(head) != 3 || len(tail) != 3 {
		t.Fatalf("head %d strips, tail %d strips; want 3 and 3", len(head), len(tail))
	}
	if v := head[2]; v.YPt != 30 || v.HPt != 20 {
		t.Errorf("head column rule = %v..%v, want 30..50", v.YPt, v.YPt+v.HPt)
	}
	if v := tail[2]; v.YPt != 50 || v.HPt != 20 {
		t.Errorf("tail column rule = %v..%v, want 50..70", v.YPt, v.YPt+v.HPt)
	}
}
