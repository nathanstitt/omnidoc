package omnidoc

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/nathanstitt/omnidoc/pkg/docx"
	gcss "github.com/nathanstitt/omnidoc/pkg/internal/css"
	docxcssbox "github.com/nathanstitt/omnidoc/pkg/internal/cssbox"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout"
	layoutcss "github.com/nathanstitt/omnidoc/pkg/internal/layout/css"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/cssbox"
	layoutfont "github.com/nathanstitt/omnidoc/pkg/internal/layout/font"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/paint"
	"github.com/nathanstitt/omnidoc/pkg/internal/raster"
	"github.com/nathanstitt/omnidoc/pkg/internal/render"
	"github.com/nathanstitt/omnidoc/pkg/internal/style"
	"github.com/nathanstitt/omnidoc/pkg/resource"
)

// reflowRenderer renders a reflowable document that has already been laid out into
// pages. It is shared by every reflow format (DOCX today; HTML/EPUB later), since
// once a frontend has produced *layout.Pages the rasterization is identical. The
// laid-out pages are read-only, so the page fan-out needs no locks.
type reflowRenderer struct {
	pages *layout.Pages
	// root is the finalized cssbox tree the pages were laid out from. It is retained
	// (read-only, like pages) so the conversion backends (markdown/text) can walk the
	// document structure; the raster/PDF backends ignore it. nil is tolerated (a
	// document opened before this field was populated) and yields empty conversion
	// output.
	root *cssbox.Box
	// loader resolves the document's image refs for conversion backends that embed
	// media (the DOCX writer). nil is tolerated (images degrade to alt text).
	loader resource.ResourceLoader
}

// OpenDOCXFile reads and parses a .docx file at path, lays out all pages, and
// returns a Document ready to rasterize or convert. Layout runs once here
// (pagination is global, so pages cannot be laid out independently);
// rasterization then parallelizes over the precomputed pages. Of the options,
// WithContext bounds both the parse and the layout; the HTML-specific options
// (viewport, resource loader, media, fonts) are inert for DOCX, whose geometry
// and media come from the package itself.
func OpenDOCXFile(path string, opts ...OpenOption) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("omnidoc: open docx %q: %w", path, err)
	}
	return OpenDOCXBytes(data, opts...)
}

// OpenDOCXBytes parses a .docx from an in-memory byte slice and lays it out,
// applying any options. See OpenDOCXFile for which options apply.
func OpenDOCXBytes(data []byte, opts ...OpenOption) (*Document, error) {
	cfg := defaultOpenConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	d, err := docx.OpenBytes(cfg.ctx, data)
	if err != nil {
		return nil, err
	}
	return docxDocument(cfg.ctx, d)
}

// docxDocument lowers a parsed DOCX through the style cascade into the recursive
// cssbox tree and runs the shared CSS layout engine, wrapping the result for
// rasterization. The DOCX section's page size and margins are carried into the CSS
// paged engine as a synthesized @page stylesheet (docxPageSheet), reusing the exact
// margin-inset machinery HTML uses for a real @page rule. ctx bounds the layout.
func docxDocument(ctx context.Context, d *docx.Document) (*Document, error) {
	resolver := style.NewResolver(d, nil)
	root := docxcssbox.Lower(d, resolver)
	geom := docxcssbox.Geometry(d)
	faces := layoutfont.NewFaceCache()
	engine := layoutcss.New(faces, docxcssbox.MediaLoader(d), nil)
	running := docxcssbox.LowerRunning(d, resolver)
	hasHeader := running[docxcssbox.RunningHeaderName] != nil
	hasFooter := running[docxcssbox.RunningFooterName] != nil
	pages, err := engine.LayoutPagedDoc(ctx, root, layoutcss.PagedConfig{
		Paged:        true,
		FallbackW:    geom.PageWidthPt, // full page; @page size/margins refine below
		FallbackH:    geom.PageHeightPt,
		ExplicitSize: false, // let the synthesized @page size apply
		Pages:        docxPageSheet(geom, hasHeader, hasFooter),
		Running:      running,
	})
	if err != nil {
		return nil, err
	}
	// As in htmlDocument: layout degrades on cancellation rather than erroring,
	// so the open boundary must not hand back a silently truncated document.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("omnidoc: open docx: %w", err)
	}
	return &Document{r: &reflowRenderer{pages: pages, root: root, loader: docxcssbox.MediaLoader(d)}, format: FormatDOCX}, nil
}

// docxPageSheet synthesizes an @page stylesheet carrying the DOCX section's page
// size and margins, so the CSS paged engine insets DOCX content exactly as it does
// for an HTML @page rule. Point values are emitted as px (the layout scalar treats
// px:pt 1:1), preserving DOCX's physical 72dpi-equivalent scale.
func docxPageSheet(g docxcssbox.PageGeometry, hasHeader, hasFooter bool) gcss.Stylesheet {
	// %f (not %g) so a fractional twip→point value can never fall into %g's exponent
	// notation, which the @page length parser would reject.
	px := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) + "px" }
	var mb strings.Builder
	if hasHeader {
		mb.WriteString(" @top-center { content: element(" + docxcssbox.RunningHeaderName + ") }")
	}
	if hasFooter {
		mb.WriteString(" @bottom-center { content: element(" + docxcssbox.RunningFooterName + ") }")
	}
	css := fmt.Sprintf("@page { size: %s %s; margin: %s %s %s %s%s }",
		px(g.PageWidthPt), px(g.PageHeightPt),
		px(g.MarginTopPt), px(g.MarginRightPt), px(g.MarginBottomPt), px(g.MarginLeftPt),
		mb.String())
	return gcss.Parse(css)
}

// reflowPages is implemented by renderers backed by *layout.Pages, so the PDF writer
// can drive the same laid-out pages the rasterizer uses.
type reflowPages interface{ layoutPages() *layout.Pages }

// layoutPages exposes the laid-out pages for the PDF writer (WritePDF).
func (r *reflowRenderer) layoutPages() *layout.Pages { return r.pages }

// paintVector paints page index onto dev, which is any render.Device, mapping
// document space to device space at scale (device units per point).
//
// This is what makes SVG output work for EVERY input format rather than only
// reflow ones. It deliberately does NOT go through reflowPages: that interface
// hands back *layout.Pages, which an opened PDF does not have, so a writer
// built on it (WritePDF) is reflow-only. Since all three frontends already
// paint through render.Device, exposing the Device rather than the page model
// is what lets a PDF, a DOCX and an SVG all reach the same backend.
func (r *reflowRenderer) paintVector(ctx context.Context, index int, dev render.Device, scale float64, opts RasterOptions) error {
	if index < 0 || index >= len(r.pages.Pages) {
		return errPageOutOfRange(index, len(r.pages.Pages))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Page space is already points, Y-down, origin top-left — the same
	// convention the SVG writer emits — so this is a plain uniform scale with
	// no flip, matching renderPage's raster path exactly.
	paint.PaintPageWithOptions(dev, &r.pages.Pages[index], render.Scale(scale, scale), paint.Options{Logf: opts.Logf})
	return nil
}

// canvasBackground reports the CSS-propagated root/body background, or a zero
// (fully transparent) color when the document sets none.
//
// The browser's background-propagation rule makes a background on <html>/<body>
// paint the whole canvas rather than just that box, and the layout engine
// resolves it here. Vector output has to honor it for the same reason the
// rasterizer does (see renderPage's canvas fill precedence) — otherwise a page
// with `body { background: green }` rasterizes green but converts to SVG
// transparent.
func (r *reflowRenderer) canvasBackground() color.RGBA { return r.pages.CanvasBackground }

// reflowTree is implemented by renderers that retain their source cssbox tree, so the
// conversion backends (markdown/text) can walk the document structure.
type reflowTree interface{ cssboxRoot() *cssbox.Box }

// cssboxRoot exposes the finalized box tree for the conversion backends (WriteMarkdown).
func (r *reflowRenderer) cssboxRoot() *cssbox.Box { return r.root }

// structureRoot returns the document's box tree for a structure writer, or an
// error wrapping [ErrNoStructure] naming the caller.
//
// It checks the ROOT, not just the interface. Satisfying reflowTree is not the
// same as having a tree: an SVG document is a reflowRenderer built with pages but
// no root, so the assertion succeeds and cssboxRoot returns nil. The writers then
// walked a nil tree, wrote nothing, and returned nil -- an empty output file
// reported as success, which is the failure mode Phase 0 item 0g was about, in a
// path 0g did not reach.
func structureRoot(d *Document, writer string) (*cssbox.Box, error) {
	rt, ok := d.r.(reflowTree)
	if !ok {
		return nil, fmt.Errorf("omnidoc: %s: %w", writer, ErrNoStructure)
	}
	root := rt.cssboxRoot()
	if root == nil {
		return nil, fmt.Errorf("omnidoc: %s: %w", writer, ErrNoStructure)
	}
	return root, nil
}

// reflowResources is implemented by renderers that retain their source's resource
// loader, so a conversion backend that embeds media (the DOCX writer) can fetch the
// document's images. The PDF renderer implements it with a data: URI loader, because
// extraction carries a PDF's images inline.
type reflowResources interface {
	resourceLoader() resource.ResourceLoader
}

// resourceLoader exposes the source's resource loader for media-embedding backends.
func (r *reflowRenderer) resourceLoader() resource.ResourceLoader { return r.loader }

func (r *reflowRenderer) pageCount() int { return len(r.pages.Pages) }

// pageSize reports the laid-out page's size in points.
func (r *reflowRenderer) pageSize(index int) (float64, float64, error) {
	if index < 0 || index >= len(r.pages.Pages) {
		return 0, 0, errPageOutOfRange(index, len(r.pages.Pages))
	}
	pg := &r.pages.Pages[index]
	return pg.WidthPt, pg.HeightPt, nil
}

func (r *reflowRenderer) renderPage(ctx context.Context, index int, opts RasterOptions) (image.Image, error) {
	if index < 0 || index >= len(r.pages.Pages) {
		return nil, errPageOutOfRange(index, len(r.pages.Pages))
	}
	// Cancellation is checked here, before the allocation and the paint walk, and
	// again after paint (below). Those are the only two useful seams on this path:
	// everything between them is a single PaintPage call over an already-laid-out
	// item list, and the per-item/per-glyph interior of that walk is far too hot to
	// carry a ctx.Err() (see pkg/internal/layout/paint) — a check there would cost more than
	// the work it guards. The genuinely unbounded loops on the reflow pipeline are
	// in LAYOUT, which runs at open time and has its own check between block
	// children (pkg/internal/layout/css/block.go); by the time renderPage is reached the
	// pages are fixed and the remaining work is bounded by the pixel cap enforced
	// just below. The multi-page fan-out is cancelled per page by RasterizePages,
	// which consults ctx before dispatching each job.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("omnidoc: render page %d: %w", index, err)
	}
	pg := &r.pages.Pages[index]

	// Validate before casting to int, so an attacker-controlled page size (SVG's
	// width/height are unclamped document attributes, unlike CSS-derived sizes
	// elsewhere in the reflow pipeline) cannot overflow int, trigger a
	// multi-gigabyte allocation, or reach image.NewRGBA with a huge/negative
	// rectangle (which panics). Mirrors pkg/internal/raster/page.go's RenderPage
	// guard (isFinitePositive-style finiteness check + maxPixels cap), except a
	// non-positive-but-finite size (e.g. an empty HTML/DOCX document laying out
	// to zero height) is a legitimate degenerate document, not an attack, so it
	// clamps to a 1x1 page exactly as before this guard existed; only non-finite
	// (NaN/Inf, impossible from any real layout) is rejected outright.
	pxW, pxH, scale, err := reflowPagePixels(pg, opts)
	if err != nil {
		return nil, err
	}

	img := image.NewRGBA(image.Rect(0, 0, pxW, pxH))
	// Canvas fill precedence: a CSS-propagated root/body background (the browser's
	// background-propagation rule, set by the layout engine) wins; else the caller's
	// RasterOptions.Background; else opaque white.
	bg := opts.Background
	if bg == nil {
		bg = color.White
	}
	if cb := r.pages.CanvasBackground; cb.A != 0 {
		bg = cb
	}
	fillBackground(img, bg)

	dev := raster.New(img)
	dev.SetLogf(opts.Logf)
	// Page space is already points, Y-down, origin top-left, so the transform to
	// device pixels is a single uniform scale — no Y-flip (unlike PDF).
	mat := render.Scale(scale, scale)
	// Hand the painter the SAME Logf the device already carries, so the CSS
	// filter caps it can hit surface on the caller's logger instead of vanishing.
	// This is the path where they actually bite: maxCSSFilterPixels is 4M and a
	// 300 DPI A4 page is ~8.7M, so a full-page filter degrades to unfiltered at
	// print resolution and the user otherwise has no way to learn why.
	paint.PaintPageWithOptions(dev, pg, mat, paint.Options{Logf: opts.Logf})
	// Re-check after paint: a very large page (up to the maxRasterPixels cap) can
	// spend real time inside the painter, which takes no ctx of its own. Reporting
	// the cancellation here means a caller that gave up mid-paint gets the context
	// error rather than an image it no longer wants — and, unlike the layout engine,
	// nothing downstream needs the partial raster, so erroring loses nothing.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("omnidoc: render page %d: %w", index, err)
	}
	return img, nil
}

// renderPageRegion rasterizes the part of page index covered by region (in
// device pixels at the effective DPI), returning an image whose Bounds() are
// that rect in PAGE-ABSOLUTE coordinates.
//
// The pixels are identical to the same rect of a full renderPage: the painter
// replays the page's whole item list through a region Device, which restricts
// writes to the rect while keeping every geometry decision in page space (see
// raster.NewRegion). Nothing about item ORDER changes, so groups, clips, blend
// modes and z-order behave exactly as in a full render — which is what a caller
// compositing the result over a cached frame depends on.
func (r *reflowRenderer) renderPageRegion(ctx context.Context, index int, region image.Rectangle, opts RasterOptions) (image.Image, error) {
	if index < 0 || index >= len(r.pages.Pages) {
		return nil, errPageOutOfRange(index, len(r.pages.Pages))
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("omnidoc: render page %d region: %w", index, err)
	}
	pg := &r.pages.Pages[index]
	pxW, pxH, scale, err := reflowPagePixels(pg, opts)
	if err != nil {
		return nil, err
	}

	// The region is clipped to the page: a caller asking for a rect that hangs
	// off the edge gets the part that exists rather than an error, matching how
	// every other geometry input here degrades. An empty intersection is an
	// error, though — it means the caller asked for pixels that cannot exist,
	// and silently handing back a zero-sized image would look like a render that
	// simply drew nothing.
	full := image.Rect(0, 0, pxW, pxH)
	clipped := region.Intersect(full)
	if clipped.Empty() {
		return nil, fmt.Errorf("omnidoc: render page %d region %v: does not intersect the page %v", index, region, full)
	}

	// The canvas is page-sized but only the region's rows are touched, so the
	// allocation is the page's while the WORK is the region's. Allocating just
	// the region instead would make its Bounds() start at the origin and lose the
	// page-absolute coordinates every geometry decision depends on.
	canvas := image.NewRGBA(full)
	sub := canvas.SubImage(clipped).(*image.RGBA)
	bg := opts.Background
	if bg == nil {
		bg = color.White
	}
	if cb := r.pages.CanvasBackground; cb.A != 0 {
		bg = cb
	}
	fillBackground(sub, bg)

	dev := raster.NewRegion(sub, full)
	dev.SetLogf(opts.Logf)
	paint.PaintPageWithOptions(dev, pg, render.Scale(scale, scale), paint.Options{Logf: opts.Logf})
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("omnidoc: render page %d region: %w", index, err)
	}
	return sub, nil
}

// reflowPagePixels resolves a laid-out page's pixel size and the page→device
// scale at opts' effective DPI, applying the same finiteness and pixel-cap
// guards renderPage does. Shared so the region path cannot drift from the
// full-page path on sizing, which would put the two in different coordinate
// systems and make a region composite misalign.
func reflowPagePixels(pg *layout.Page, opts RasterOptions) (pxW, pxH int, scale float64, err error) {
	scale = opts.dpi() / 72
	fW := math.Ceil(pg.WidthPt * scale)
	fH := math.Ceil(pg.HeightPt * scale)
	if math.IsNaN(fW) || math.IsInf(fW, 0) || math.IsNaN(fH) || math.IsInf(fH, 0) {
		return 0, 0, 0, fmt.Errorf("omnidoc: degenerate scaled page size %gx%g", fW, fH)
	}
	if fW*fH > float64(maxRasterPixels) {
		return 0, 0, 0, fmt.Errorf("omnidoc: page too large (%.0fx%.0f px exceeds %d-pixel cap; lower DPI)", fW, fH, maxRasterPixels)
	}
	pxW, pxH = int(fW), int(fH)
	if pxW <= 0 || pxH <= 0 {
		pxW, pxH = 1, 1
	}
	return pxW, pxH, scale, nil
}
