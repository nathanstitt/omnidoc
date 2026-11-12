package omnidoc

import (
	"context"
	"errors"
	"image"
	"testing"

	"github.com/nathanstitt/omnidoc/testdata/gen"
)

// regionFixtureHTML exercises the constructs that make sub-region rendering
// hard: blurred box-shadows and gradients (both derive an offscreen surface
// from the device's reported size, so a region that mis-reports it paints them
// clipped or misplaced), overlapping z-order, and text crossing the boundary.
const regionFixtureHTML = `<!doctype html><html><body style="margin:0;width:900px;height:600px;background:#101418;font-family:sans-serif">
<div style="position:absolute;left:40px;top:40px;width:380px;height:240px;
     background:linear-gradient(150deg,#1b2530,#0d1218);border:1px solid #2a3644;
     box-shadow:0 10px 30px rgba(0,0,0,.6);color:#e8eef4;padding:12px">
  <h2 style="margin:0;font-size:22px">Panel A</h2>
  <p style="font-size:13px;line-height:1.5">Text that runs across the region boundary so a
  seam in glyph antialiasing would show up in the comparison.</p>
</div>
<div style="position:absolute;left:300px;top:180px;width:420px;height:280px;
     background:linear-gradient(20deg,#243447,#121a22);border-radius:0;
     box-shadow:0 6px 24px rgba(0,0,0,.55);color:#cfe;padding:12px">
  <h2 style="margin:0;font-size:20px">Panel B overlaps A</h2>
  <div style="margin-top:8px;height:60px;background:#4aa8ff33"></div>
</div>
<div style="position:absolute;left:120px;top:420px;width:600px;height:120px;
     background:#182231;box-shadow:inset 0 8px 20px rgba(0,0,60,.7)"></div>
</body></html>`

func regionDoc(t testing.TB) *Document {
	t.Helper()
	doc, err := OpenHTMLBytes([]byte(regionFixtureHTML), WithViewportWidth(900), WithBundledFonts())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return doc
}

// TestRasterizePageRegionMatchesFullPage is the contract the feature exists
// for: a region's pixels must be IDENTICAL to the same rect of a full-page
// render, or a caller compositing the region over a cached frame sees a seam.
//
// The rects deliberately cut through shadows, gradients, overlapping boxes and
// text rather than landing on empty background, since every mechanism that
// could differ (offscreen surface sizing, mask rasterization at the boundary)
// only shows where content crosses the edge.
func TestRasterizePageRegionMatchesFullPage(t *testing.T) {
	doc := regionDoc(t)
	opts := RasterOptions{DPI: 96, BundledFonts: true}
	fullImg, err := doc.RasterizePage(context.Background(), 0, opts)
	if err != nil {
		t.Fatalf("RasterizePage: %v", err)
	}
	full := fullImg.(*image.RGBA)

	for _, region := range []image.Rectangle{
		image.Rect(0, 0, 200, 150),     // corner, includes the page edge
		image.Rect(300, 150, 700, 450), // through both panels and their shadows
		image.Rect(380, 40, 520, 300),  // a narrow slice cutting text and a gradient
		image.Rect(100, 400, 760, 580), // across the inset shadow
		full.Bounds(),                  // the whole page through the region path
	} {
		got, err := doc.RasterizePageRegion(context.Background(), 0, region, opts)
		if err != nil {
			t.Fatalf("region %v: %v", region, err)
		}
		if got.Bounds() != region {
			t.Errorf("region %v: bounds = %v, want the requested rect in page coordinates", region, got.Bounds())
		}
		gi := got.(*image.RGBA)
		diff := 0
		for y := region.Min.Y; y < region.Max.Y; y++ {
			for x := region.Min.X; x < region.Max.X; x++ {
				if gi.RGBAAt(x, y) != full.RGBAAt(x, y) {
					diff++
				}
			}
		}
		if diff != 0 {
			t.Errorf("region %v: %d of %d pixels differ from the full-page render; a region must be pixel-identical or a composite will seam",
				region, diff, region.Dx()*region.Dy())
		}
	}
}

// TestRasterizePageRegionStrokesMatchFullPage covers the stroke path, which
// builds its own coverage mask rather than going through the fill rasterizer.
// The mask must be shaped by the PAGE, not by the region surface: rasterx
// derives its sampling from the mask rectangle, so a stroke truncated at the
// region edge comes out with slightly different coverage than the same stroke
// rasterized whole. Found on a dashboard painted as vertical strips, where a
// stroked SVG icon lying across a strip edge differed by up to 4/255.
//
// Each region edge below cuts through curved, diagonal, dashed and
// round-capped strokes, since straight axis-aligned strokes have no
// antialiased coverage along the cut to disagree about.
func TestRasterizePageRegionStrokesMatchFullPage(t *testing.T) {
	const html = `<!doctype html><html><body style="margin:0;width:900px;height:600px;background:#101418">
<svg width="900" height="600" viewBox="0 0 900 600" style="position:absolute;left:0;top:0">
  <circle cx="300.5" cy="200.3" r="90.7" fill="none" stroke="#e8a21b" stroke-width="3.5"/>
  <path d="M 120.2 80.7 L 520.9 310.4 L 180.3 520.6" fill="none" stroke="#4aa8ff" stroke-width="7.3" stroke-linejoin="miter"/>
  <path d="M 250 450 C 330 300, 420 560, 610 380" fill="none" stroke="#cfe" stroke-width="2.25" stroke-linecap="round" stroke-dasharray="11 5"/>
  <ellipse cx="640.4" cy="150.6" rx="130.2" ry="60.8" fill="none" stroke="#f66" stroke-width="1.2"/>
  <line x1="60.4" y1="560.2" x2="840.7" y2="40.9" stroke="#8f8" stroke-width="4.6" stroke-linecap="round"/>
</svg>
</body></html>`
	doc, err := OpenHTMLBytes([]byte(html), WithViewportWidth(900), WithBundledFonts())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	opts := RasterOptions{DPI: 96, BundledFonts: true}
	fullImg, err := doc.RasterizePage(context.Background(), 0, opts)
	if err != nil {
		t.Fatalf("RasterizePage: %v", err)
	}
	full := fullImg.(*image.RGBA)

	for _, region := range []image.Rectangle{
		image.Rect(0, 0, 300, 200),     // edges through the circle's centre
		image.Rect(300, 200, 600, 400), // the other side of the same cuts
		image.Rect(217, 133, 431, 377), // odd offsets, every stroke crosses
		image.Rect(450, 0, 900, 600),   // a vertical strip, as a banded paint uses
		image.Rect(0, 290, 900, 310),   // a thin horizontal band
	} {
		got, err := doc.RasterizePageRegion(context.Background(), 0, region, opts)
		if err != nil {
			t.Fatalf("region %v: %v", region, err)
		}
		gi := got.(*image.RGBA)
		diff := 0
		for y := region.Min.Y; y < region.Max.Y; y++ {
			for x := region.Min.X; x < region.Max.X; x++ {
				if gi.RGBAAt(x, y) != full.RGBAAt(x, y) {
					diff++
				}
			}
		}
		if diff != 0 {
			t.Errorf("region %v: %d of %d pixels differ from the full-page render; a stroke crossing the region edge must be rasterized as it is on the whole page",
				region, diff, region.Dx()*region.Dy())
		}
	}
}

// TestRasterizePageRegionClipsToPage checks the two out-of-bounds behaviours:
// a region hanging off the edge is clipped to the page (the caller gets the
// part that exists), and one entirely off the page is an error rather than an
// empty image, which would otherwise be indistinguishable from a blank render.
func TestRasterizePageRegionClipsToPage(t *testing.T) {
	doc := regionDoc(t)
	opts := RasterOptions{DPI: 96, BundledFonts: true}
	full, err := doc.RasterizePage(context.Background(), 0, opts)
	if err != nil {
		t.Fatal(err)
	}
	pageRect := full.Bounds()

	got, err := doc.RasterizePageRegion(context.Background(), 0,
		image.Rect(pageRect.Max.X-50, pageRect.Max.Y-50, pageRect.Max.X+500, pageRect.Max.Y+500), opts)
	if err != nil {
		t.Fatalf("overhanging region: %v", err)
	}
	want := image.Rect(pageRect.Max.X-50, pageRect.Max.Y-50, pageRect.Max.X, pageRect.Max.Y)
	if got.Bounds() != want {
		t.Errorf("overhanging region: bounds = %v, want it clipped to %v", got.Bounds(), want)
	}

	if _, err := doc.RasterizePageRegion(context.Background(), 0,
		image.Rect(pageRect.Max.X+10, 0, pageRect.Max.X+100, 50), opts); err == nil {
		t.Error("a region entirely off the page must error, not return an empty image")
	}
}

// TestRasterizePageRegionUnsupported pins the optional-interface contract: a
// PDF has no region backend and must say so with a sentinel the caller can
// test, rather than failing obscurely or silently rendering the whole page.
func TestRasterizePageRegionUnsupported(t *testing.T) {
	doc, err := OpenBytes(gen.TextPDF())
	if err != nil {
		t.Fatal(err)
	}
	_, err = doc.RasterizePageRegion(context.Background(), 0, image.Rect(0, 0, 50, 50), RasterOptions{DPI: 96})
	if !errors.Is(err, ErrRegionUnsupported) {
		t.Errorf("PDF region render: err = %v, want ErrRegionUnsupported", err)
	}
}

// TestRasterizePageRegionBadPage checks an out-of-range page index is reported
// rather than panicking on the slice index.
func TestRasterizePageRegionBadPage(t *testing.T) {
	doc := regionDoc(t)
	if _, err := doc.RasterizePageRegion(context.Background(), 7, image.Rect(0, 0, 50, 50), RasterOptions{DPI: 96}); err == nil {
		t.Error("out-of-range page index must error")
	}
}

// TestRasterizePageRegionCancelled checks the context is honoured, so a caller
// that abandons a region render (a dialog dismissed before it lands) is not
// billed for the rest of it.
func TestRasterizePageRegionCancelled(t *testing.T) {
	doc := regionDoc(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := doc.RasterizePageRegion(ctx, 0, image.Rect(0, 0, 100, 100), RasterOptions{DPI: 96}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled region render: err = %v, want context.Canceled", err)
	}
}
