package css

import (
	"fmt"
	"strings"
	"testing"
)

// pageTexts joins each page's words, for content-on-page assertions.
func pageTexts(t *testing.T, src string, pageH float64) []string {
	t.Helper()
	words := pageWords(layoutPagedHTML(t, src, pageH))
	out := make([]string, len(words))
	for i, ws := range words {
		var b strings.Builder
		for _, w := range ws {
			b.WriteString(w.text + " ")
		}
		out[i] = b.String()
	}
	return out
}

// A section that starts with a forced break and is taller than a page splits across
// pages, and every paragraph lands on a page: the last one on the last page, inside
// the page box rather than overflowing the first page.
func TestForcedBreakSectionContentReachesTheLastPage(t *testing.T) {
	var paras strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&paras, "<p>P%dX</p>", i)
	}
	src := `<html><head><style>body { margin: 0 } p { margin: 0; height: 30px }</style></head><body>` +
		`<p>LEAD</p><section style="break-before: page">` + paras.String() + `</section></body></html>`
	pages := pageTexts(t, src, 300)
	if len(pages) != 4 { // the lead, then 30 x 30px over 300px pages
		t.Fatalf("want 4 pages, got %d", len(pages))
	}
	if !strings.Contains(pages[len(pages)-1], "P30X") {
		t.Errorf("the last paragraph is not on the last page: %q", pages[len(pages)-1])
	}
	for i := 1; i <= 30; i++ {
		n := 0
		for _, p := range pages {
			n += strings.Count(p, fmt.Sprintf("P%dX", i))
		}
		if n != 1 {
			t.Errorf("paragraph %d appears %d times", i, n)
		}
	}
}

// A block that starts with a forced break and holds a nested forced break further
// down breaks at both: the nested break is not dropped because its ancestor's own
// break was forced too.
func TestNestedBreakInsideForcedBreakBlock(t *testing.T) {
	src := `<html><head><style>body { margin: 0 } p { margin: 0; height: 30px }</style></head><body>` +
		`<p>LEAD</p><section style="break-before: page"><p>FIRST</p>` +
		`<p style="break-before: page">SECOND</p></section></body></html>`
	pages := pageTexts(t, src, 300)
	want := []string{"LEAD", "FIRST", "SECOND"}
	if len(pages) != len(want) {
		t.Fatalf("got %d pages %q, want %d", len(pages), pages, len(want))
	}
	for i, w := range want {
		if !strings.Contains(pages[i], w) {
			t.Errorf("page %d = %q, want %s", i, pages[i], w)
		}
	}
}
