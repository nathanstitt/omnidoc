package omnidoc

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// htmlDocDir is the on-disk fixture tree for the end-to-end HTML showcase: an
// index.html that links css/reset.css + css/main.css, declares an @font-face served
// from fonts/, and references images under img/. It is served over loopback HTTP so
// the test exercises OpenURL + the HTTP ResourceLoader resolving relative refs
// across nested directories — the same path a real document on the web takes.
var htmlDocDir = filepath.Join("..", "..", "testdata", "htmldoc")

// htmlDocPages is the number of US-Letter pages the showcase fragments into. It is
// asserted so an accidental reflow that adds or drops a page fails loudly (not just
// a per-page pixel diff). Update it (and regenerate goldens) when the document
// intentionally changes length.
const htmlDocPages = 51

// TestHTMLDocShowcase renders the multi-file "uber" specimen document over HTTP and
// compares every paginated page to a committed PNG (htmldoc-p<i>.png). It is the one
// golden that drives the whole HTML pipeline through OpenURL: multi-<link> cascade,
// @font-face WOFF2 download, PNG/JPEG/GIF decode, and fixed-height pagination, all
// from relative refs resolved against the document URL. Run with -update to
// regenerate, then eyeball every page PNG in review.
func TestHTMLDocShowcase(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.FileServer(http.Dir(htmlDocDir)))
	defer srv.Close()

	// WithDefaultPaged drives pagination from the document's own @page rule (Letter
	// size, a bottom margin band, and a running page-counter footer) — exercising the
	// full paged-media path end to end, not just fixed-height slicing.
	doc, err := OpenURL(srv.URL+"/index.html", WithDefaultPaged(), WithBundledFonts())
	if err != nil {
		t.Fatalf("OpenURL: %v", err)
	}

	if !*update && doc.PageCount() != htmlDocPages {
		t.Fatalf("PageCount = %d, want %d (the showcase reflowed; eyeball it, then update htmlDocPages and regenerate goldens)",
			doc.PageCount(), htmlDocPages)
	}

	dir := filepath.Join("testdata", "golden")
	for i := 0; i < doc.PageCount(); i++ {
		img, err := doc.RasterizePage(context.Background(), i, RasterOptions{DPI: goldenDPI, BundledFonts: true})
		if err != nil {
			t.Fatalf("RasterizePage(%d): %v", i, err)
		}
		got, ok := img.(*image.RGBA)
		if !ok {
			t.Fatalf("rasterized image is %T, want *image.RGBA", img)
		}

		path := filepath.Join(dir, "htmldoc-p"+strconv.Itoa(i)+".png")
		if *update {
			writePNG(t, path, got)
			t.Logf("updated %s", path)
			continue
		}
		want := readPNG(t, path)
		if want == nil {
			t.Fatalf("missing golden %s; run: go test ./pkg/omnidoc -run TestHTMLDocShowcase -update", path)
		}
		if diff, n := compareImages(want, got); diff {
			t.Errorf("page %d differs from golden %s: %d pixels beyond tolerance (max %d)",
				i, path, n, int(maxDifferingFraction*float64(got.Bounds().Dx()*got.Bounds().Dy())))
		}
	}
}

// TestHTMLDocMarkdown exports the same multi-file showcase specimen to Markdown and
// plain text, comparing to committed htmldoc.md / htmldoc.txt goldens. It drives the
// conversion path end to end on a real, feature-dense document (headings, lists,
// tables, links, emphasis, images), the text-side counterpart to the raster showcase.
// Run with -update to regenerate, then eyeball the committed .md/.txt in review.
func TestHTMLDocMarkdown(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.FileServer(http.Dir(htmlDocDir)))
	defer srv.Close()

	doc, err := OpenURL(srv.URL+"/index.html", WithBundledFonts())
	if err != nil {
		t.Fatalf("OpenURL: %v", err)
	}
	dir := filepath.Join("testdata", "golden")
	for _, tc := range []struct {
		name string
		opts MarkdownOptions
	}{
		{"htmldoc.md", MarkdownOptions{}},
		{"htmldoc.txt", MarkdownOptions{Plain: true}},
	} {
		var out bytes.Buffer
		if err := doc.WriteMarkdown(context.Background(), &out, tc.opts); err != nil {
			t.Fatalf("WriteMarkdown(%s): %v", tc.name, err)
		}
		path := filepath.Join(dir, tc.name)
		if *update {
			if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("updated %s", path)
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing golden %s; run: go test ./pkg/omnidoc -run TestHTMLDocMarkdown -update", path)
		}
		if !bytes.Equal(want, out.Bytes()) {
			t.Errorf("%s differs from golden; run -update and eyeball the diff", tc.name)
		}
	}
}

// TestHTMLDocSVGShowcase is the SVG backend's visual entry (CLAUDE.md: "every
// feature lands with a visual entry"), adapted for an OUTPUT format.
//
// Adding a section to index.html would not exercise an output format at all —
// the showcase renders HTML to PNG. So this drives the richest document in the
// repo THROUGH the SVG writer instead: every page is converted to SVG, re-read
// with pkg/internal/svg, rasterized, and compared against the SAME committed
// htmldoc-p<i>.png goldens the raster path is held to. No new goldens are
// committed; reusing the raster ones is the point, because any place the two
// backends disagree shows up as a diff against a reference that is already
// known-good.
//
// It covers what the gen.Core sweep in svgwrite_test.go cannot: real prose in
// many faces and sizes, tables, lists, borders, backgrounds, gradients, opacity,
// transforms, and web fonts — at 44 pages, the broadest fidelity check the SVG
// backend has.
//
// Failures here are fidelity gaps, not crashes. If one appears, diff the page
// against its golden before touching the tolerance: docs/SVG.md records the
// known reader-side gaps (notably <image>, which makes any raster content and
// any sampled gradient render blank on the way back in), and a NEW difference
// is a regression in the writer.
func TestHTMLDocSVGShowcase(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.FileServer(http.Dir(htmlDocDir)))
	defer srv.Close()

	doc, err := OpenURL(srv.URL+"/index.html", WithDefaultPaged(), WithBundledFonts())
	if err != nil {
		t.Fatalf("OpenURL: %v", err)
	}
	if doc.PageCount() != htmlDocPages {
		t.Fatalf("PageCount = %d, want %d (see TestHTMLDocShowcase)", doc.PageCount(), htmlDocPages)
	}

	dir := filepath.Join("testdata", "golden")
	for i := range doc.PageCount() {
		path := filepath.Join(dir, "htmldoc-p"+strconv.Itoa(i)+".png")
		want := readPNG(t, path)
		if want == nil {
			t.Fatalf("missing golden %s; run: go test ./pkg/omnidoc -run TestHTMLDocShowcase -update", path)
		}

		// goldenDPI is 72, so one point is one SVG user unit and the emitted
		// document lands on the golden's pixel grid with no scaling.
		var svg bytes.Buffer
		if err := doc.WriteSVG(context.Background(), &svg, i, SVGOptions{
			BundledFonts: true,
			Background:   color.White, // the goldens are rendered on opaque white
		}); err != nil {
			t.Fatalf("WriteSVG(%d): %v", i, err)
		}

		// Re-reading through this toolkit's own parser is what makes the
		// comparison meaningful: a document that is malformed, mis-nested, or
		// geometrically wrong fails here even when every attribute in it looks
		// individually plausible.
		back, err := OpenSVGBytes(svg.Bytes())
		if err != nil {
			t.Fatalf("page %d: emitted SVG did not parse: %v", i, err)
		}
		img, err := back.RasterizePage(context.Background(), 0, RasterOptions{
			DPI: goldenDPI, BundledFonts: true, Background: color.White,
		})
		if err != nil {
			t.Fatalf("page %d: rasterize emitted SVG: %v", i, err)
		}
		got, ok := img.(*image.RGBA)
		if !ok {
			t.Fatalf("page %d: rasterized image is %T, want *image.RGBA", i, img)
		}
		if got.Bounds() != want.Bounds() {
			t.Errorf("page %d: SVG size %v, golden %v", i, got.Bounds(), want.Bounds())
			continue
		}
		_, n := compareImages(want, got)
		total := got.Bounds().Dx() * got.Bounds().Dy()
		budget := int(svgShowcaseBudget(i) * float64(total))
		if n > budget {
			t.Errorf("page %d: SVG round-trip differs from raster golden %s: %d/%d pixels beyond per-pixel tolerance (budget %d, %.2f%%)",
				i, path, n, total, budget, svgShowcaseBudget(i)*100)
		}
	}
}

// svgShowcasePagesWithRasterContent are the showcase pages whose SVG output
// embeds an <image> — raster content, or a gradient that had to be sampled.
//
// pkg/internal/svg does not implement <image> (docs/SVG.md), so on the way back in that
// content renders as nothing and the comparison would measure the READER, not
// this writer. They are listed explicitly rather than detected, so that a page
// which stops containing raster content — or a NEW page that starts embedding
// one — changes this list and gets looked at.
var svgShowcasePagesWithRasterContent = map[int]bool{
	2: true, 7: true, 11: true, 16: true, 18: true, 22: true, 26: true, 36: true,
}

// svgShowcaseBudget is the differing-pixel budget for one showcase page.
//
// Most pages hold the standard 0.2%. Two deliberate relaxations:
//
//   - A page whose SVG embeds an <image> is checked only for gross corruption
//     (50%), since the reader cannot draw that content at all. It still runs,
//     because the REST of the page — text, borders, backgrounds — must stay
//     correct, and a layout regression there would blow past even this bound.
//   - Every page gets 0.3% rather than 0.2%, because this comparison crosses
//     BACKENDS. Measured on page 39 (gradients, no raster content): the only
//     differing pixels are single antialiased rows at each gradient swatch's
//     top and bottom edge — 1920 of 907200, 0.21% — where the raster path
//     fills a rect directly and the SVG path fills a <rect> carrying a
//     gradient reference. Interior pixels are byte-identical. That is a
//     sub-pixel edge seam between two rasterizations of the same geometry, not
//     a fidelity loss, and the per-channel tolerance stays at 4 either way.
func svgShowcaseBudget(page int) float64 {
	if svgShowcasePagesWithRasterContent[page] {
		return 0.50
	}
	return 0.003
}
