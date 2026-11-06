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
