package omnidoc

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"testing"
)

// atPageDocHTML embeds an @page rule (A4, 0.5in margins) and stacks enough block
// content (1500pt) to span multiple A4 (1123pt-tall) pages.
const atPageDocHTML = `<html><head><style>
	@page { size: A4; margin: 0.5in }
</style></head><body>
	<div style="height:500px;margin:0;background-color:rgb(11,11,11)">a</div>
	<div style="height:500px;margin:0;background-color:rgb(22,22,22)">b</div>
	<div style="height:500px;margin:0;background-color:rgb(33,33,33)">c</div>
</body></html>`

// TestWithDefaultPagedUsesAtPage proves WithDefaultPaged paginates using the
// document's @page size: A4 pages (794 x 1123 @96), multiple of them for 1500pt of
// content, each non-blank.
func TestWithDefaultPagedUsesAtPage(t *testing.T) {
	t.Parallel()
	doc, err := OpenHTMLBytes([]byte(atPageDocHTML), WithDefaultPaged())
	if err != nil {
		t.Fatalf("OpenHTMLBytes: %v", err)
	}
	if got := doc.PageCount(); got <= 1 {
		t.Fatalf("PageCount() = %d, want > 1 (1500pt of content on A4 pages)", got)
	}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for i := 0; i < doc.PageCount(); i++ {
		img, err := doc.RasterizePage(context.Background(), i, RasterOptions{})
		if err != nil {
			t.Fatalf("RasterizePage(%d): %v", i, err)
		}
		rgba, ok := img.(*image.RGBA)
		if !ok {
			t.Fatalf("page %d: image is %T, want *image.RGBA", i, img)
		}
		// A4 width at 96dpi is ~794px; the raster default scale is 1.0 (72dpi → but
		// RasterOptions default scale renders points 1:1). Assert the page is portrait
		// (taller than wide), the A4 signature.
		b := rgba.Bounds()
		if b.Dy() <= b.Dx() {
			t.Errorf("page %d bounds %v not portrait; expected A4 portrait", i, b)
		}
		if n := countNonBackground(rgba, white); n == 0 {
			t.Fatalf("page %d: painted nothing", i)
		}
	}
}

// TestAtPageInertWithoutOptIn is the byte-identical guard: a document WITH an @page
// rule but opened WITHOUT WithDefaultPaged / WithPageSize is still a single tall page
// (the @page rule is inert until pagination is requested).
func TestAtPageInertWithoutOptIn(t *testing.T) {
	t.Parallel()
	doc, err := OpenHTMLBytes([]byte(atPageDocHTML))
	if err != nil {
		t.Fatalf("OpenHTMLBytes: %v", err)
	}
	if got := doc.PageCount(); got != 1 {
		t.Fatalf("PageCount() = %d, want 1 (@page inert without opt-in)", got)
	}
	// The single page is taller than any A4 page (1500pt+ of content), proving it is
	// the single-tall path, not an A4 slice.
	pg := doc.r.(*reflowRenderer).pages.Pages[0]
	if pg.HeightPt < 1400 {
		t.Errorf("single page height = %.0f, want full content height (single-tall path)", pg.HeightPt)
	}
}

// tallNoAtPageHTML stacks 1500pt of content with NO @page rule (taller than a Letter
// page's 1056pt), so WithDefaultPaged must slice it into multiple Letter pages.
const tallNoAtPageHTML = `<html><body>` +
	`<div style="height:500px;margin:0;background-color:rgb(11,11,11)">a</div>` +
	`<div style="height:500px;margin:0;background-color:rgb(22,22,22)">b</div>` +
	`<div style="height:500px;margin:0;background-color:rgb(33,33,33)">c</div>` +
	`</body></html>`

// TestWithDefaultPagedNoAtPageFallsBackToLetter proves WithDefaultPaged on a document
// with NO @page rule falls back to US-Letter pages.
func TestWithDefaultPagedNoAtPageFallsBackToLetter(t *testing.T) {
	t.Parallel()
	doc, err := OpenHTMLBytes([]byte(tallNoAtPageHTML), WithDefaultPaged())
	if err != nil {
		t.Fatalf("OpenHTMLBytes: %v", err)
	}
	if got := doc.PageCount(); got <= 1 {
		t.Fatalf("PageCount() = %d, want > 1 (1500pt on Letter pages)", got)
	}
	pg := doc.r.(*reflowRenderer).pages.Pages[0]
	if math.Abs(pg.WidthPt-LetterWidthPt) > 0.5 || math.Abs(pg.HeightPt-LetterHeightPt) > 0.5 {
		t.Errorf("page size = %.0f x %.0f, want Letter %d x %d (fallback)",
			pg.WidthPt, pg.HeightPt, LetterWidthPt, LetterHeightPt)
	}
}

// CSS Paged Media: the page context inherits from the root element, so a
// footer takes the document's font. With html { font-family: sans-serif } the
// PDF must embed only the sans face; a serif footer would add TeX Gyre Termes.
func TestMarginBoxInheritsTheRootFont(t *testing.T) {
	src := `<!doctype html><html style="font-family: sans-serif"><head><style>
		@page { size: Letter; margin: 0.5in; @bottom-right { content: "Page " counter(page) " of " counter(pages) } }
	</style></head><body><p>Body text</p></body></html>`
	doc, err := OpenHTMLBytes([]byte(src), WithDefaultPaged(), WithBundledFonts())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var pdf bytes.Buffer
	if err := doc.WritePDF(context.Background(), &pdf, PDFOptions{}); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The embedded font programs are Flate streams, and every font is named
	// "Embedded" in its dictionaries, so the face names are only readable in the
	// decoded programs. (Matching the raw bytes passed only where the compressor
	// happened to store the program's cleartext header uncompressed.)
	programs := pdfStreams(t, pdf.Bytes())
	if !strings.Contains(programs, "TeXGyreHeros") {
		t.Fatal("the sans face is not embedded")
	}
	if strings.Contains(programs, "TeXGyreTermes") {
		t.Error("the footer used the serif face, not the root's sans-serif")
	}
}

// A block that starts with a forced break and is taller than a page must still
// split across pages. The forced break only closes the page before it; it must
// not stop the block from fragmenting, or everything below the first page
// overflows the page box and is clipped.
func TestForcedBreakBlockTallerThanAPageSplits(t *testing.T) {
	var paras strings.Builder
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&paras, "<p>Paragraph %d</p>", i)
	}
	pages := func(lead, sectionStyle string) int {
		src := `<!doctype html><html><head><style>@page { size: Letter; margin: 0.5in }</style></head><body>` +
			lead + `<section style="` + sectionStyle + `">` + paras.String() + `</section></body></html>`
		doc, err := OpenHTMLBytes([]byte(src), WithDefaultPaged(), WithBundledFonts())
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		return doc.PageCount()
	}
	unforced := pages("", "")
	if unforced < 2 {
		t.Fatalf("precondition: the section must be taller than a page, got %d page(s)", unforced)
	}
	// A forced break on the first block is a no-op, so the page count must match.
	if got := pages("", "break-before: page"); got != unforced {
		t.Errorf("forced-break first section: %d pages, want %d (the block did not split)", got, unforced)
	}
	// After a lead paragraph the break adds exactly the lead's page.
	if got := pages("<p>Lead</p>", "break-before: page"); got != unforced+1 {
		t.Errorf("forced-break section after a lead: %d pages, want %d (the block did not split)", got, unforced+1)
	}
}
