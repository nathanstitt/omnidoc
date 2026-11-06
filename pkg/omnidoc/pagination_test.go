package omnidoc

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"
)

// multiBlockHTML stacks four 200pt-tall blocks (800pt of content) with distinct
// background colors so each painted page is clearly non-blank. With no margins the
// content height is a clean 800pt, exceeding any modest WithPageSize height.
const multiBlockHTML = `<html><body>` +
	`<div style="height:200px;margin:0;background-color:rgb(10,10,10)">a</div>` +
	`<div style="height:200px;margin:0;background-color:rgb(20,20,20)">b</div>` +
	`<div style="height:200px;margin:0;background-color:rgb(30,30,30)">c</div>` +
	`<div style="height:200px;margin:0;background-color:rgb(40,40,40)">d</div>` +
	`</body></html>`

// shortBlockHTML is a single short block that fits comfortably within a Letter page.
const shortBlockHTML = `<html><body>` +
	`<div style="height:100px;margin:0;background-color:rgb(50,50,50)">x</div>` +
	`</body></html>`

// TestWithPageSizeMultiPage proves WithPageSize paginates: 800pt of stacked block
// content sliced into 300pt-tall pages yields multiple pages, each of which
// rasterizes without error and paints real (non-blank) content. The layout width
// is set from LetterWidthPt so the constant is exercised here.
func TestWithPageSizeMultiPage(t *testing.T) {
	t.Parallel()
	doc, err := OpenHTMLBytes([]byte(multiBlockHTML), WithPageSize(LetterWidthPt, 300))
	if err != nil {
		t.Fatalf("OpenHTMLBytes: %v", err)
	}
	if got := doc.PageCount(); got <= 1 {
		t.Fatalf("PageCount() = %d, want > 1 (800pt of content sliced into 300pt pages)", got)
	}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for i := 0; i < doc.PageCount(); i++ {
		img, err := doc.RasterizePage(context.Background(), i, RasterOptions{})
		if err != nil {
			t.Fatalf("RasterizePage(%d): %v", i, err)
		}
		b := img.Bounds()
		if b.Dx() <= 0 || b.Dy() <= 0 {
			t.Fatalf("page %d: empty image bounds %v", i, b)
		}
		rgba, ok := img.(*image.RGBA)
		if !ok {
			t.Fatalf("page %d: rasterized image is %T, want *image.RGBA", i, img)
		}
		if n := countNonBackground(rgba, white); n == 0 {
			t.Fatalf("page %d: no non-background pixels (page painted nothing)", i)
		}
	}
}

// TestWithPageSizeSinglePageWhenShort checks that content shorter than the page
// height stays on a single page even with WithPageSize active.
func TestWithPageSizeSinglePageWhenShort(t *testing.T) {
	t.Parallel()
	doc, err := OpenHTMLBytes([]byte(shortBlockHTML), WithPageSize(LetterWidthPt, LetterHeightPt))
	if err != nil {
		t.Fatalf("OpenHTMLBytes: %v", err)
	}
	if got := doc.PageCount(); got != 1 {
		t.Fatalf("PageCount() = %d, want 1 (short content fits one Letter page)", got)
	}
}

// TestDefaultIsSingleTallPage is the byte-identical-default guard at the API level:
// without WithPageSize the document is one tall page regardless of content height —
// the 800pt of stacked blocks is NOT sliced.
func TestDefaultIsSingleTallPage(t *testing.T) {
	t.Parallel()
	doc, err := OpenHTMLBytes([]byte(multiBlockHTML))
	if err != nil {
		t.Fatalf("OpenHTMLBytes: %v", err)
	}
	if got := doc.PageCount(); got != 1 {
		t.Fatalf("PageCount() = %d, want 1 (no WithPageSize ⇒ single tall page)", got)
	}
}

// TestLetterConstants documents the exported US-Letter page-size constants (and
// keeps the unused linter satisfied for both).
func TestLetterConstants(t *testing.T) {
	t.Parallel()
	if LetterWidthPt != 816 {
		t.Fatalf("LetterWidthPt = %v, want 816", LetterWidthPt)
	}
	if LetterHeightPt != 1056 {
		t.Fatalf("LetterHeightPt = %v, want 1056", LetterHeightPt)
	}
}

// Through the public API: an HTML table under a heading, written to PDF and read
// back, carries its header text once per page.
func TestHTMLTableHeaderRepeatsOnEveryPDFPage(t *testing.T) {
	var rows strings.Builder
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&rows, "<tr><td>row %d</td></tr>", i)
	}
	src := `<!doctype html><html><head><style>@page { size: Letter; margin: 0.5in }</style></head><body>` +
		`<h1>Title above the table</h1><table><thead><tr><th>COLHEAD</th></tr></thead><tbody>` +
		rows.String() + `</tbody></table></body></html>`
	doc, err := OpenHTMLBytes([]byte(src), WithDefaultPaged(), WithBundledFonts())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var pdf bytes.Buffer
	if err := doc.WritePDF(context.Background(), &pdf, PDFOptions{}); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	back, err := OpenBytes(pdf.Bytes())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	var text bytes.Buffer
	if err := back.WriteText(context.Background(), &text, MarkdownOptions{}); err != nil {
		t.Fatalf("text: %v", err)
	}
	pages := back.PageCount()
	if pages < 3 {
		t.Fatalf("want at least 3 pages, got %d", pages)
	}
	if got := strings.Count(text.String(), "COLHEAD"); got != pages {
		t.Errorf("header appears %d times over %d pages", got, pages)
	}
}
