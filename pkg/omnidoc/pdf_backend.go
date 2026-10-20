package omnidoc

import (
	"context"
	"fmt"
	"image"
	"math"
	"sync"

	"github.com/nathanstitt/omnidoc/pkg/internal/extract"
	"github.com/nathanstitt/omnidoc/pkg/internal/font"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/cssbox"
	layoutfont "github.com/nathanstitt/omnidoc/pkg/internal/layout/font"
	"github.com/nathanstitt/omnidoc/pkg/internal/raster"
	"github.com/nathanstitt/omnidoc/pkg/internal/render"
	"github.com/nathanstitt/omnidoc/pkg/pdf"
	"github.com/nathanstitt/omnidoc/pkg/resource"
)

// pdfRenderer renders a parsed PDF document. The *pdf.Document is read-only after
// parsing, so it is shared across the page fan-out without locks.
type pdfRenderer struct {
	doc *pdf.Document

	// logf receives degradation messages from structure extraction, or is nil.
	// It is the OPEN-time logger, captured here because extraction happens lazily
	// on the first Write* call, by which point the open options are long gone --
	// so there is no later opportunity to ask the caller for one.
	logf func(string, ...any)

	// extractOnce/extractRoot lazily hold the structure-recovery cssbox tree, built on
	// the first WriteMarkdown/WriteText/WriteHTML (extraction is expensive and most
	// callers only rasterize). The tree is read-only once built. This makes a PDF
	// document satisfy reflowTree, so the conversion backends work on PDF inputs.
	extractOnce sync.Once
	extractRoot *cssbox.Box
}

func (r *pdfRenderer) pageCount() int { return r.doc.PageCount() }

// dataURILoader resolves only data: URIs, which is all extraction emits: a PDF's
// images are carried into the structure tree as inline data: sources, so a
// media-embedding writer (DOCX, EPUB, PPTX, RTF) needs no file or network access
// to fetch them.
type dataURILoader struct{}

func (dataURILoader) Load(_ context.Context, ref string) ([]byte, string, error) {
	return resource.LoadDataURL(ref)
}

// resourceLoader exposes the data: URI loader so media-embedding backends can
// embed the images extraction recovered from the PDF.
func (r *pdfRenderer) resourceLoader() resource.ResourceLoader { return dataURILoader{} }

// cssboxRoot lazily extracts the PDF's logical structure (paragraphs, headings, lists,
// tables) into a cssbox tree the conversion backends walk, satisfying reflowTree so
// WriteMarkdown/WriteText/WriteHTML work on PDF inputs. Extraction runs once and is
// cached; it never panics (the extractor recovers per page). A nil result (extraction
// failure) yields an empty document downstream rather than an error, matching the
// degrade-gracefully rule.
//
// Degrading is only honest if it SAYS so. Every PDF-to-anything conversion routes
// through here, so a dropped error produced an empty output file and a nil error
// -- the worst failure mode available, because it is indistinguishable from a
// document that genuinely had no text. The error is now reported through the
// open-time logger, and r.logf is threaded into extract.Lower so its own per-page
// messages ("skipping page 3: ...", "page 0 content unavailable: ...") reach the
// caller instead of being discarded at the nil it used to be handed.
func (r *pdfRenderer) cssboxRoot() *cssbox.Box {
	r.extractOnce.Do(func() {
		root, err := extract.Lower(r.doc, r.logf)
		if err != nil {
			// extractRoot stays nil; downstream writes an empty document.
			if r.logf != nil {
				r.logf("omnidoc: PDF structure extraction failed, output will be empty: %v", err)
			}
			return
		}
		r.extractRoot = root
	})
	return r.extractRoot
}

// pageSize reports the page's MediaBox size in points, post-/Rotate — the same
// geometry choices raster.RenderPage makes, so a fit computed from this size
// matches the rendered pixel dimensions exactly.
func (r *pdfRenderer) pageSize(index int) (float64, float64, error) {
	pg, err := r.doc.Page(index)
	if err != nil {
		return 0, 0, fmt.Errorf("page %d: %w", index, err)
	}
	w, h := pg.MediaBox.Width(), pg.MediaBox.Height()
	// Mirror raster.RenderPage's validation so fit math never divides by junk
	// from a crafted MediaBox (NaN fails the > 0 comparison).
	if !(w > 0 && h > 0) || math.IsInf(w, 1) || math.IsInf(h, 1) {
		return 0, 0, fmt.Errorf("page %d: invalid MediaBox %gx%g", index, w, h)
	}
	if pg.Rotate == 90 || pg.Rotate == 270 {
		w, h = h, w
	}
	return w, h, nil
}

func (r *pdfRenderer) renderPage(ctx context.Context, index int, opts RasterOptions) (image.Image, error) {
	pg, err := r.doc.Page(index)
	if err != nil {
		return nil, fmt.Errorf("page %d: %w", index, err)
	}
	return raster.RenderPage(ctx, pg, raster.Options{
		DPI:          opts.dpi(),
		Background:   opts.Background,
		Logf:         opts.Logf,
		FontProvider: opts.fontProvider(),
	})
}

// paintVector interprets page index's content stream against dev, an arbitrary
// render.Device, so a PDF can be converted to a vector format.
//
// The PDF content interpreter has always been backend-agnostic — it drives a
// render.Device and never touches pixels — so this needs no new PDF machinery,
// only the page setup that raster.RunPage already shares with the rasterizer.
// That shared path is why a PDF rendered to SVG and the same PDF rasterized
// cannot drift.
func (r *pdfRenderer) paintVector(ctx context.Context, index int, dev render.Device, scale float64, opts RasterOptions) error {
	pg, err := r.doc.Page(index)
	if err != nil {
		return fmt.Errorf("page %d: %w", index, err)
	}
	return raster.RunPage(ctx, pg, dev, scale, raster.Options{
		Logf:         opts.Logf,
		FontProvider: opts.fontProvider(),
	})
}

// fontProvider resolves the font provider for a rasterize call per the mode precedence:
// an explicit FontProvider always wins; else bundled mode (BundledFonts) installs no
// provider (bundled-only); else the default installs an OSFontProvider so installed OS
// fonts are used, falling through to the bundled substitute when none match.
func (o RasterOptions) fontProvider() font.Provider {
	if o.FontProvider != nil {
		return o.FontProvider
	}
	if o.BundledFonts {
		return nil
	}
	return layoutfont.NewOSFontProviderWithLogf(o.Logf)
}
