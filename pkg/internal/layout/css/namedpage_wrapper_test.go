package css

import (
	"context"
	"strings"
	"testing"

	"github.com/nathanstitt/omnidoc/pkg/internal/layout"
)

// In a document with named pages (the per-run path), the body's border is one box
// fragmented across pages, as on the single-geometry path: its top edge is drawn on
// the first page only, so a later page's first line is not drawn over a rule that
// belongs to page one, and its bottom edge only on the last page.
func TestNamedPageRunsFragmentTheBodyBorder(t *testing.T) {
	var paras strings.Builder
	for i := 0; i < 30; i++ {
		paras.WriteString("<p>para</p>")
	}
	css := `
		@page { size: 300px 300px; margin: 20px 0 }
		@page wide { size: 500px 300px; margin: 20px 0 }`
	src := `<html><head><style>` + css + `
		.wide { page: wide }
		body { margin: 0; border: 6px solid black; padding: 10px }
		p { margin: 0; height: 30px }
	</style></head><body>` + paras.String() + `<div class="wide"><p>wide</p></div></body></html>`
	root := buildRoot(t, src, nil)
	pages, err := New(nil, nil, nil).LayoutPagedDoc(context.Background(), root, pagedConfigFor(css, 300, 300, false))
	if err != nil {
		t.Fatalf("LayoutPagedDoc: %v", err)
	}
	assertWrapperSliced(t, pages)
}

// With one page geometry, a forced break before the last block leaves blank space the
// body's flow height never counted; the body's bottom edge still belongs to the last
// page, not the one before it.
func TestForcedBreakKeepsBodyBottomOnLastPage(t *testing.T) {
	var paras strings.Builder
	for i := 0; i < 30; i++ {
		paras.WriteString("<p>para</p>")
	}
	css := `@page { size: 300px 300px; margin: 20px 0 }`
	src := `<html><head><style>` + css + `
		body { margin: 0; border: 6px solid black; padding: 10px }
		p { margin: 0; height: 30px }
	</style></head><body>` + paras.String() + `<p style="break-before: page">last</p></body></html>`
	root := buildRoot(t, src, nil)
	pages, err := New(nil, nil, nil).LayoutPagedDoc(context.Background(), root, pagedConfigFor(css, 300, 300, false))
	if err != nil {
		t.Fatalf("LayoutPagedDoc: %v", err)
	}
	assertWrapperSliced(t, pages)
}

// assertWrapperSliced checks that the body's 6px top edge is drawn on the first page
// only and its bottom edge on the last page only.
func assertWrapperSliced(t *testing.T, pages *layout.Pages) {
	t.Helper()
	if len(pages.Pages) < 3 {
		t.Fatalf("want at least 3 pages, got %d", len(pages.Pages))
	}
	last := len(pages.Pages) - 1
	for pi, p := range pages.Pages {
		top, bottom := 0, 0
		for _, it := range p.Items {
			if it.Kind != layout.BorderKind || it.Border.HPt != 6 {
				continue
			}
			switch it.Border.Side {
			case layout.EdgeTop:
				if it.Border.YPt > -6 && it.Border.YPt < p.HeightPt {
					top++
				}
			case layout.EdgeBottom:
				if it.Border.YPt > -6 && it.Border.YPt < p.HeightPt {
					bottom++
				}
			}
		}
		if wantTop := pi == 0; (top > 0) != wantTop {
			t.Errorf("page %d: body top border drawn = %v, want %v", pi, top > 0, wantTop)
		}
		if wantBottom := pi == last; (bottom > 0) != wantBottom {
			t.Errorf("page %d: body bottom border drawn = %v, want %v", pi, bottom > 0, wantBottom)
		}
	}
}

// The body's trailing padding and bottom border belong on the last page, inside its
// content box: when the last block leaves too little room for them, the bucketer
// moves (or splits) it so the wrapper closes within the page rather than in the
// bottom margin band, clipped at the bitmap edge.
func TestBodyBottomBorderFitsTheLastPage(t *testing.T) {
	var paras strings.Builder
	for i := 0; i < 16; i++ {
		paras.WriteString("<p>para</p>")
	}
	for _, named := range []bool{false, true} {
		css := `@page { size: 300px 300px; margin: 20px 0 }`
		tail := ""
		if named {
			css += `
		@page wide { size: 500px 300px; margin: 20px 0 }`
			tail = `<div class="wide"><p>wide</p></div>`
		}
		// 16 x 30px = 480px of paragraphs over 260px pages: page 2 ends 6+10+480-260 =
		// 236px down, leaving 24px — less than the body's 36px padding + 6px border.
		src := `<html><head><style>` + css + `
		.wide { page: wide }
		body { margin: 0; border: 6px solid black; padding: 10px 10px 36px 10px }
		p { margin: 0; height: 30px }
	</style></head><body>` + tail + paras.String() + `</body></html>`
		if named {
			src = strings.Replace(src, tail+paras.String(), paras.String()+tail+paras.String(), 1)
		}
		root := buildRoot(t, src, nil)
		pages, err := New(nil, nil, nil).LayoutPagedDoc(context.Background(), root, pagedConfigFor(css, 300, 300, false))
		if err != nil {
			t.Fatalf("LayoutPagedDoc: %v", err)
		}
		last := pages.Pages[len(pages.Pages)-1]
		found := false
		for _, it := range last.Items {
			if it.Kind == layout.BorderKind && it.Border.Side == layout.EdgeBottom && it.Border.HPt == 6 {
				found = true
				if bottom := it.Border.YPt + it.Border.HPt; bottom > last.HeightPt-20+0.5 {
					t.Errorf("named=%v: the body bottom border ends at y=%.1f, below the content box (%.1f)", named, bottom, last.HeightPt-20)
				}
			}
		}
		if !found {
			t.Errorf("named=%v: the last page draws no body bottom border", named)
		}
	}
}
