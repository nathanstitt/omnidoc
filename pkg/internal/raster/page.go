package raster

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // register JPEG decoder for DCTDecode XObjects
	"math"

	"github.com/nathanstitt/omnidoc/pkg/internal/content"
	"github.com/nathanstitt/omnidoc/pkg/internal/filter"
	"github.com/nathanstitt/omnidoc/pkg/internal/font"
	"github.com/nathanstitt/omnidoc/pkg/internal/function"
	"github.com/nathanstitt/omnidoc/pkg/internal/pageres"
	"github.com/nathanstitt/omnidoc/pkg/internal/render"
	"github.com/nathanstitt/omnidoc/pkg/pdf"
)

// Options configures page rendering.
type Options struct {
	// DPI is the output resolution; PDF user space is 72 units/inch, so the scale
	// is DPI/72. Defaults to 72 when zero or negative.
	DPI float64
	// Background fills the page before drawing. Defaults to opaque white. Use a
	// transparent color for an alpha page.
	Background color.Color
	// Logf, if set, receives debug messages for unsupported features.
	Logf func(string, ...any)
	// FontProvider, if set, resolves a non-embedded standard-14 /BaseFont to raw
	// font-program bytes before the bundled substitute is tried, letting a caller
	// point the rasterizer at system fonts or a fonts directory (including the
	// families the bundle has no look-alike for, e.g. Symbol). Nil keeps the
	// historical bundled-substitute-only behavior.
	FontProvider font.Provider
}

// maxPixels bounds any single allocated raster: ~134M px (about 11600² , or A0
// at 300 dpi) — generous for real documents but bounded.
//
// It applies to the page canvas AND to every image decoded onto it, because both
// take their dimensions from the file. The page path can compare in float64
// (which cannot wrap); the image path must compare by division, since its row
// arithmetic is integer and overflows into a negative product that passes a
// naive size check.
const maxPixels = 1 << 27

// RenderPage rasterizes a single page to an *image.RGBA at the requested DPI,
// honoring the page's /Rotate. It runs the content interpreter against the
// raster Device. Unsupported features (e.g. text without a font backend yet)
// are skipped with a debug log rather than failing the render.
//
// The context is checked before the (potentially expensive) interpretation
// begins so batch callers can cancel promptly.
func RenderPage(ctx context.Context, pg *pdf.Page, opts Options) (out *image.RGBA, err error) {
	if pg == nil {
		return nil, fmt.Errorf("raster: nil page")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dpi := opts.DPI
	if dpi <= 0 {
		dpi = 72
	}
	scale := dpi / 72

	// Page size in points, then pixels. /Rotate swaps dimensions for 90/270.
	wpt := pg.MediaBox.Width()
	hpt := pg.MediaBox.Height()
	if !isFinitePositive(wpt) || !isFinitePositive(hpt) {
		return nil, fmt.Errorf("raster: invalid MediaBox %gx%g", wpt, hpt)
	}
	// Compute dimensions in float64, then validate before casting to int, so an
	// attacker-controlled MediaBox or DPI cannot overflow int or trigger a
	// multi-gigapixel allocation.
	fW := math.Ceil(wpt * scale)
	fH := math.Ceil(hpt * scale)
	if !isFinitePositive(fW) || !isFinitePositive(fH) {
		return nil, fmt.Errorf("raster: degenerate scaled size %gx%g", fW, fH)
	}
	if fW*fH > float64(maxPixels) {
		return nil, fmt.Errorf("raster: page too large (%.0fx%.0f px exceeds %d-pixel cap; lower DPI)", fW, fH, maxPixels)
	}
	pxW := int(fW)
	pxH := int(fH)
	if pg.Rotate == 90 || pg.Rotate == 270 {
		pxW, pxH = pxH, pxW
	}
	if pxW <= 0 || pxH <= 0 {
		return nil, fmt.Errorf("raster: degenerate page size %dx%d", pxW, pxH)
	}

	img := image.NewRGBA(image.Rect(0, 0, pxW, pxH))
	bg := opts.Background
	if bg == nil {
		bg = color.White
	}
	fillBackground(img, bg)

	// Recover at the page boundary (CLAUDE.md: "one bad page can't kill a batch").
	// The interpreter is written never to panic on malformed input, but a defect (or a
	// malformed construct that slips a bounds check) must not crash the whole render
	// fan-out — RasterizePages runs this in worker goroutines, where an unrecovered panic
	// is process-fatal. On panic, return the page painted so far (the background plus
	// whatever drew before the fault) and log, rather than propagating the panic.
	defer func() {
		if r := recover(); r != nil {
			if opts.Logf != nil {
				opts.Logf("raster: recovered from panic rendering page: %v", r)
			}
			out = img
			err = nil
		}
	}()

	dev := New(img)
	dev.SetLogf(opts.Logf)
	if err := RunPage(ctx, pg, dev, scale, opts); err != nil {
		return nil, err
	}
	return img, nil
}

// RunPage interprets pg's content stream against dev, an arbitrary
// render.Device, mapping PDF user space to device space at the given scale
// (pixels per point) and honoring the page's /Rotate.
//
// This is the device-independent half of RenderPage, exported so a non-raster
// backend (the SVG writer) can drive the PDF interpreter without duplicating
// the page matrix and resource wiring — and, more importantly, without the two
// drifting apart. RenderPage itself calls it, so there is one code path.
//
// Missing or undecodable content is not an error: a blank page is a valid
// result, matching how the rasterizer has always treated it.
func RunPage(ctx context.Context, pg *pdf.Page, dev render.Device, scale float64, opts Options) error {
	if pg == nil {
		return fmt.Errorf("raster: nil page")
	}
	if dev == nil {
		return fmt.Errorf("raster: nil device")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if pg.Rotate != 0 && pg.Rotate != 90 && pg.Rotate != 180 && pg.Rotate != 270 && opts.Logf != nil {
		opts.Logf("raster: unexpected /Rotate %d treated as 0", pg.Rotate)
	}
	wpt, hpt := pg.MediaBox.Width(), pg.MediaBox.Height()
	base := pageMatrix(pg, scale, wpt, hpt)

	content0, err := pg.ContentBytes()
	if err != nil {
		// No content (or undecodable): a blank page is a valid result.
		if opts.Logf != nil {
			opts.Logf("raster: page content unavailable: %v", err)
		}
		return nil
	}

	res := &pageResources{doc: pg.Doc(), dict: pg.Resources, logf: opts.Logf, provider: opts.FontProvider}
	interp := content.New(pg.Doc(), dev, res, base, content.Options{
		Logf:   opts.Logf,
		MaxOps: 5_000_000,
	})
	if err := interp.Run(content0); err != nil {
		return fmt.Errorf("raster: interpreting page: %w", err)
	}
	return nil
}

// isFinitePositive reports whether v is a finite, strictly positive number.
func isFinitePositive(v float64) bool {
	return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v)
}

// pageMatrix builds the transform from PDF user space (points, y-up, origin
// bottom-left) to device pixels (y-down, origin top-left) at the given scale,
// applying the page's /Rotate. wpt/hpt are the unrotated page size in points.
//
// It is the composition: scale → rotate clockwise by /Rotate about the origin →
// translate the rotated content back into the positive quadrant → flip y into
// the top-left device frame. Each branch is written out directly (rather than
// composed via Mul) so the six coefficients are easy to verify against a known
// point.
func pageMatrix(pg *pdf.Page, scale, wpt, hpt float64) render.Matrix {
	w, h := wpt*scale, hpt*scale // page size in pixels (unrotated)
	switch pg.Rotate {
	case 90:
		// User (x,y) → device (h_px - y_scaled? ) : 90° CW then y-flip.
		// Verified: user (0,0)→(0,0); (wpt,0)→(0,w); (0,hpt)→(h,0).
		return render.Matrix{A: 0, B: scale, C: scale, D: 0, E: 0, F: 0}
	case 180:
		// 180°: user (0,0)→(w,0) bottom-left maps to top-right then flips.
		// Verified: (0,0)→(w,0); (wpt,0)→(0,0); (0,hpt)→(w,h).
		return render.Matrix{A: -scale, B: 0, C: 0, D: scale, E: w, F: 0}
	case 270:
		// Verified: (0,0)→(h,w); (wpt,0)→(h,0); (0,hpt)→(0,w).
		return render.Matrix{A: 0, B: -scale, C: -scale, D: 0, E: h, F: w}
	default: // 0°
		// Plain scale + y-flip. Verified: (0,0)→(0,h); (wpt,0)→(w,h); (0,hpt)→(0,0).
		return render.Matrix{A: scale, B: 0, C: 0, D: -scale, E: 0, F: h}
	}
}

// pageResources resolves fonts, image XObjects, and form XObjects from a page's
// /Resources. The same type backs nested form resources (see Form), so form
// content resolves names against its own /Resources, falling back to the page's.
type pageResources struct {
	doc      *pdf.Document
	dict     pdf.Dict
	logf     func(string, ...any)
	provider font.Provider // caller-supplied substitute resolver; nil = bundled only
}

// Font resolves a font resource by name to a GlyphSource. Unsupported or
// malformed fonts (non-embedded base-14, classic Type1, non-Identity Type0
// CMaps) return nil, so the interpreter advances the cursor without drawing.
func (r *pageResources) Font(name string) content.GlyphSource {
	return pageres.ResolveFont(r.doc, r.dict, name, r.provider, "raster", r.logf)
}

// Form resolves a form XObject by name to its decoded content stream, its scoped
// resources, its /Matrix, and whether it declares a transparency /Group. It
// returns ok=false if name is not a form XObject or its content cannot be
// decoded, so the interpreter skips it gracefully. Per the PDF spec a form
// without its own /Resources inherits the page's, so the child pageResources
// falls back to this dict.
func (r *pageResources) Form(name string) ([]byte, content.Resources, render.Matrix, *[4]float64, bool, bool) {
	data, childRes, m, bbox, isGroup, ok := pageres.ResolveForm(r.doc, r.dict, name, "raster", r.logf)
	if !ok {
		return nil, nil, render.Identity, nil, false, false
	}
	child := &pageResources{doc: r.doc, dict: childRes, logf: r.logf, provider: r.provider}
	return data, child, m, bbox, isGroup, true
}

// ColorSpace resolves a named /ColorSpace resource that is a Separation or DeviceN space
// into a tint transform (parsing its tint-transform /Function and recording the alternate
// space's component count), or ok=false for any other space (device/Pattern/ICCBased/…,
// which the interpreter handles by component count). A malformed array, a /Function that
// fails to parse, or an unrecognized alternate space degrades to ok=false (the prior
// component-count approximation), never a crash.
func (r *pageResources) ColorSpace(name string) (*content.TintTransform, bool) {
	csDict := r.doc.GetDict(r.dict["ColorSpace"])
	if csDict == nil {
		return nil, false
	}
	arr := r.doc.GetArray(csDict[pdf.Name(name)])
	if len(arr) < 4 {
		return nil, false
	}
	family, _ := r.doc.GetName(arr[0])
	if family != "Separation" && family != "DeviceN" {
		return nil, false
	}
	// arr[2] = alternate space, arr[3] = tint transform function.
	altComps := colorSpaceComponents(r.doc, arr[2])
	if altComps == 0 {
		return nil, false
	}
	fn, err := function.Parse(r.doc, arr[3])
	if err != nil || fn == nil {
		if r.logf != nil {
			r.logf("raster: %s color space %q tint /Function: %v", family, name, err)
		}
		return nil, false
	}
	return &content.TintTransform{
		Eval:           fn.Eval,
		AlternateComps: altComps,
	}, true
}

// colorSpaceComponents returns the number of color components of a color space object
// (a /ColorSpace alternate space): a device-name space, or an array whose head names the
// family. Returns 0 for an unrecognized space (the caller degrades). Recognizes the
// common alternate spaces a Separation/DeviceN uses: DeviceGray/RGB/CMYK, CalGray/RGB,
// Lab, and ICCBased (by its /N).
func colorSpaceComponents(doc *pdf.Document, o pdf.Object) int {
	if n, ok := doc.GetName(o); ok {
		switch n {
		case "DeviceGray", "G", "CalGray":
			return 1
		case "DeviceRGB", "RGB", "CalRGB", "Lab":
			return 3
		case "DeviceCMYK", "CMYK":
			return 4
		}
		return 0
	}
	arr := doc.GetArray(o)
	if len(arr) == 0 {
		return 0
	}
	head, _ := doc.GetName(arr[0])
	switch head {
	case "CalGray":
		return 1
	case "CalRGB", "Lab":
		return 3
	case "ICCBased":
		if st := doc.GetStream(arr[1]); st != nil {
			if n, ok := doc.GetInt(st.Dict["N"]); ok {
				return n
			}
		}
		return 0
	}
	return 0
}

// Shading resolves a named /Shading resource and builds a shader for it. A
// shading entry may be a bare dictionary (axial/radial/function shadings) or a
// stream (mesh shadings, Types 4–7); newShader handles both. Unsupported types or
// malformed geometry return ok=false (with a debug log) so the `sh` operator
// degrades gracefully.
func (r *pageResources) Shading(name string) (render.Shader, bool) {
	shadings := r.doc.GetDict(r.dict["Shading"])
	if shadings == nil {
		return nil, false
	}
	sh, err := newShader(r.doc, shadings[pdf.Name(name)])
	if err != nil {
		if r.logf != nil {
			r.logf("raster: shading %q: %v", name, err)
		}
		return nil, false
	}
	return sh, true
}

// Pattern resolves a named /Pattern resource to a shader plus its /Matrix.
// Shading patterns (/PatternType 2) carry a /Shading and an optional /Matrix
// mapping pattern space to the default coordinate system; we build a shader from
// the /Shading and return that matrix. Tiling patterns (/PatternType 1, a stream)
// are unsupported: they return ok=false with a debug log so the caller falls back
// gracefully.
func (r *pageResources) Pattern(name string) (render.Shader, render.Matrix, bool) {
	patterns := r.doc.GetDict(r.dict["Pattern"])
	if patterns == nil {
		return nil, render.Identity, false
	}
	dict, ok := shadingDict(r.doc, patterns[pdf.Name(name)])
	if !ok {
		return nil, render.Identity, false
	}
	pt, _ := r.doc.GetInt(dict["PatternType"])
	if pt != 2 {
		if r.logf != nil {
			r.logf("raster: pattern %q: unsupported /PatternType %d (only shading patterns)", name, pt)
		}
		return nil, render.Identity, false
	}
	sh, err := newShader(r.doc, dict["Shading"])
	if err != nil {
		if r.logf != nil {
			r.logf("raster: pattern %q: %v", name, err)
		}
		return nil, render.Identity, false
	}
	return sh, patternMatrix(r.doc, dict["Matrix"]), true
}

// shadingDict resolves o to a dictionary, accepting either a bare dict or a
// stream (returning its stream dict). It returns ok=false otherwise.
func shadingDict(doc *pdf.Document, o pdf.Object) (pdf.Dict, bool) {
	if s := doc.GetStream(o); s != nil {
		return s.Dict, true
	}
	if d := doc.GetDict(o); d != nil {
		return d, true
	}
	return nil, false
}

// patternMatrix reads a pattern's /Matrix (six numbers), returning identity when
// absent or malformed (the PDF default). Shares the parsing of pageres.FormMatrix.
func patternMatrix(doc *pdf.Document, o pdf.Object) render.Matrix {
	return pageres.FormMatrix(doc, o)
}

// ExtGState resolves a named entry of the /ExtGState resource dict, reporting the
// constant alpha (/ca, /CA), blend mode, and any /SMask soft mask.
func (r *pageResources) ExtGState(name string) (content.ExtGStateParams, bool) {
	gsDicts := r.doc.GetDict(r.dict["ExtGState"])
	if gsDicts == nil {
		return content.ExtGStateParams{}, false
	}
	gs := r.doc.GetDict(gsDicts[pdf.Name(name)])
	if gs == nil {
		return content.ExtGStateParams{}, false
	}
	var p content.ExtGStateParams
	if ca, ok := pdf.Number(r.doc.Resolve(gs["ca"])); ok {
		p.FillAlpha, p.HasFillAlpha = clampAlpha(ca), true
	}
	if ca, ok := pdf.Number(r.doc.Resolve(gs["CA"])); ok {
		p.StrokeAlpha, p.HasStrokeAlpha = clampAlpha(ca), true
	}
	// /BM is a blend-mode name, or an array of names (use the first one). Pass it
	// through; the device applies recognized modes and falls back to Normal.
	if bm := blendModeName(r.doc, gs["BM"]); bm != "" {
		p.BlendMode, p.HasBlendMode = bm, true
	}
	if smVal, present := gs["SMask"]; present {
		r.resolveSoftMask(smVal, &p)
	}
	return p, true
}

// resolveSoftMask fills p.SoftMask/ExplicitNone/HasUnsupported from an
// ExtGState's /SMask entry: /None explicitly clears any mask in effect; a
// dictionary is resolved via ResolveFormObject (the same form-decoding
// pageres.go's ResolveForm uses, since /G is a form XObject reference) into a
// content.SoftMask; anything else (absent already handled by the caller,
// malformed dict, unresolvable /G) is flagged HasUnsupported so the
// interpreter degrades to "no mask" and logs once rather than silently
// misrendering.
func (r *pageResources) resolveSoftMask(smVal pdf.Object, p *content.ExtGStateParams) {
	if name, ok := r.doc.GetName(smVal); ok {
		if name == "None" {
			p.ExplicitNone = true
		} else {
			p.HasUnsupported = true
		}
		return
	}
	smDict := r.doc.GetDict(smVal)
	if smDict == nil {
		p.HasUnsupported = true
		return
	}
	subtype, _ := r.doc.GetName(smDict["S"])
	alphaOnly := subtype == "Alpha"
	if subtype != "Luminosity" && subtype != "Alpha" {
		p.HasUnsupported = true
		return
	}
	data, childRes, matrix, bbox, _, ok := pageres.ResolveFormObject(r.doc, r.dict, smDict["G"], "raster", r.logf)
	if !ok {
		p.HasUnsupported = true
		return
	}
	p.SoftMask = &content.SoftMask{
		AlphaOnly: alphaOnly,
		Content:   data,
		Res:       &pageResources{doc: r.doc, dict: childRes, logf: r.logf, provider: r.provider},
		Matrix:    matrix,
		BBox:      bbox,
	}
}

// blendModeName resolves a /BM entry (a name or an array of names) to a single
// blend-mode name, returning "" when absent.
func blendModeName(doc *pdf.Document, o pdf.Object) string {
	switch v := doc.Resolve(o).(type) {
	case pdf.Name:
		return string(v)
	case pdf.Array:
		// First name in the array (PDF: the first supported mode).
		for _, e := range v {
			if n, ok := doc.GetName(e); ok {
				return string(n)
			}
		}
	}
	return ""
}

// clampAlpha constrains an alpha value to [0,1].
func clampAlpha(a float64) float64 {
	switch {
	case a < 0:
		return 0
	case a > 1:
		return 1
	default:
		return a
	}
}

func (r *pageResources) Image(name string, fill render.FillColor) (image.Image, bool) {
	return ResolveImage(r.doc, r.dict, name, fill, r.logf)
}

// ResolveImage decodes the image XObject called name in the resource dictionary
// res. The rasterizer and the structure extractor share it, so a PDF image
// decodes identically whether it is painted or carried into a Markdown/DOCX
// conversion. It reports false for a missing, non-image, or undecodable
// XObject (logged through logf, which may be nil).
func ResolveImage(doc *pdf.Document, res pdf.Dict, name string, fill render.FillColor, logf func(string, ...any)) (image.Image, bool) {
	xobjs := doc.GetDict(res["XObject"])
	if xobjs == nil {
		return nil, false
	}
	s := doc.GetStream(xobjs[pdf.Name(name)])
	if s == nil {
		return nil, false
	}
	if sub, _ := doc.GetName(s.Dict["Subtype"]); sub != "Image" {
		return nil, false
	}
	img, err := decodeImageXObject(doc, s, fill, logf)
	if err != nil {
		if logf != nil {
			logf("raster: image %q: %v", name, err)
		}
		return nil, false
	}
	return img, true
}

// inlineKeyAliases maps inline-image abbreviated keys to their full equivalents
// (PDF 32000-1 Table 93). Full names are also accepted, so the map only lists
// the abbreviations.
var inlineKeyAliases = map[pdf.Name]pdf.Name{
	"W":   "Width",
	"H":   "Height",
	"BPC": "BitsPerComponent",
	"CS":  "ColorSpace",
	"F":   "Filter",
	"D":   "Decode",
	"DP":  "DecodeParms",
	"IM":  "ImageMask",
	"I":   "Interpolate",
}

// inlineCSAliases maps abbreviated inline color-space names to full names; the
// decode path (resolveImageCS) understands the full names.
var inlineCSAliases = map[string]string{
	"G":    "DeviceGray",
	"RGB":  "DeviceRGB",
	"CMYK": "DeviceCMYK",
	"I":    "Indexed",
}

// InlineImage decodes a BI...ID...EI inline image into a drawable image. It
// normalizes the abbreviated keys into a synthetic stream dict and reuses the
// XObject image-decode path, so the two share color-space and bit-depth handling.
func (r *pageResources) InlineImage(dict pdf.Dict, data []byte, fill render.FillColor) (image.Image, bool) {
	return ResolveInlineImage(r.doc, dict, data, fill, r.logf)
}

// ResolveInlineImage is ResolveImage for a BI...ID...EI inline image.
func ResolveInlineImage(doc *pdf.Document, dict pdf.Dict, data []byte, fill render.FillColor, logf func(string, ...any)) (image.Image, bool) {
	// Normalize abbreviated keys to their full forms.
	norm := pdf.Dict{}
	for k, v := range dict {
		if full, ok := inlineKeyAliases[k]; ok {
			k = full
		}
		norm[k] = v
	}
	// Expand an abbreviated named color space.
	if name, ok := norm["ColorSpace"].(pdf.Name); ok {
		if full, ok := inlineCSAliases[string(name)]; ok {
			norm["ColorSpace"] = pdf.Name(full)
		}
	}

	img, err := decodeInlineImage(doc, norm, data, fill, logf)
	if err != nil {
		if logf != nil {
			logf("raster: inline image: %v", err)
		}
		return nil, false
	}
	return img, true
}

// decodeImageXObject turns an image XObject stream into an image.Image. It
// handles raw samples in the common color spaces and bit depths (DeviceGray/RGB/
// CMYK, Indexed, ICCBased by component count; 1/2/4/8/16 bpc), baseline JPEG
// (DCTDecode), and 1-bit /ImageMask stencils painted in the fill color. It honors
// the /Decode array and applies a grayscale /SMask as the image's alpha channel.
func decodeImageXObject(doc *pdf.Document, s *pdf.Stream, fill render.FillColor, logf func(string, ...any)) (image.Image, error) {
	data, imgFilter, err := doc.DecodedStream(s)
	if err != nil {
		return nil, err
	}
	w, _ := doc.GetInt(s.Dict["Width"])
	h, _ := doc.GetInt(s.Dict["Height"])
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("bad dimensions %dx%d", w, h)
	}

	// JBIG2Decode: decode to a 1-bpp buffer up front and treat the image as a normal
	// 1-bpc bilevel image thereafter. This MUST happen before the /ImageMask and raw
	// branches, which consume `data` directly — a JBIG2 /ImageMask would otherwise get
	// the undecoded stream. On failure the whole image is skipped (returned error →
	// caller logs + draws nothing).
	if imgFilter == "JBIG2Decode" {
		globals := jbig2Globals(doc, s.Dict)
		buf, jerr := filter.DecodeJBIG2(data, globals, w, h)
		if jerr != nil {
			return nil, jerr
		}
		data = buf
		imgFilter = "" // now a raw 1-bpp bilevel image
	}

	// /ImageMask: a 1-bit stencil. Sample 0 paints the fill color, 1 is
	// transparent (default /Decode [0 1]); /Decode [1 0] inverts that.
	if isImageMask(doc, s.Dict) {
		return decodeImageMask(data, w, h, imageMaskInverted(doc, s.Dict), fill)
	}

	var base *image.RGBA
	switch imgFilter {
	case "DCTDecode":
		decoded, _, derr := image.Decode(bytes.NewReader(data))
		if derr != nil {
			return nil, fmt.Errorf("jpeg decode: %w", derr)
		}
		// Honor a non-identity /Decode array on the JPEG (the raw-sample path already
		// does). The dominant case is an Adobe CMYK JPEG shipping /Decode [1 0 1 0 1 0 1 0]
		// to invert; ignoring it renders inverted colors. Apply the remap in the image's
		// NATIVE component space (CMYK before the RGB conversion; RGB otherwise) so the
		// inversion matches the PDF's intent. 8 bpc is the only DCT depth.
		decoded = applyDCTDecode(doc, decoded, s.Dict["Decode"])
		base = toRGBA(decoded)
	case "":
		bpc, _ := doc.GetInt(s.Dict["BitsPerComponent"])
		if bpc == 0 {
			bpc = 8
		}
		cs, err := resolveImageCS(doc, s.Dict["ColorSpace"], bpc, logf)
		if err != nil {
			return nil, err
		}
		cs.decode = imageDecodeArray(doc, s.Dict["Decode"], bpc, cs)
		base, err = decodeRawImage(data, w, h, bpc, cs)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported image filter %s", imgFilter)
	}

	applySoftMask(doc, s, base, logf)
	return base, nil
}

// jbig2Globals returns the decoded bytes of a JBIG2 image's /JBIG2Globals stream (shared
// segment dictionary), or nil when absent. The globals live in the image's /DecodeParms
// (or /DP) — either a single parameter dict (a lone JBIG2Decode filter) or a parallel
// array of per-filter parameter dicts (JBIG2Decode in a multi-filter chain). We locate
// the parameter dict carrying /JBIG2Globals in either form.
func jbig2Globals(doc *pdf.Document, dict pdf.Dict) []byte {
	pd := jbig2ParmsDict(doc, dict["DecodeParms"])
	if pd == nil {
		pd = jbig2ParmsDict(doc, dict["DP"])
	}
	if pd == nil {
		return nil
	}
	gs := doc.GetStream(pd["JBIG2Globals"])
	if gs == nil {
		return nil
	}
	b, _, err := doc.DecodedStream(gs)
	if err != nil {
		return nil
	}
	return b
}

// jbig2ParmsDict resolves a /DecodeParms (or /DP) value to the parameter dict that
// carries /JBIG2Globals. It accepts the single-dict form directly, or scans the parallel
// array form (a JBIG2Decode in a filter chain) for the element dict that has a
// JBIG2Globals key. Returns nil when neither form yields one.
func jbig2ParmsDict(doc *pdf.Document, parms pdf.Object) pdf.Dict {
	if d := doc.GetDict(parms); d != nil {
		if _, ok := d["JBIG2Globals"]; ok {
			return d
		}
		return nil
	}
	for _, e := range doc.GetArray(parms) {
		if d := doc.GetDict(e); d != nil {
			if _, ok := d["JBIG2Globals"]; ok {
				return d
			}
		}
	}
	return nil
}

// decodeInlineImage decodes a normalized inline image (full-key dict + verbatim
// sample bytes) by wrapping it as a synthetic stream and reusing
// decodeImageXObject, so inline and XObject images share one decode path. The
// dict's keys must already be normalized to full names by the caller.
func decodeInlineImage(doc *pdf.Document, dict pdf.Dict, data []byte, fill render.FillColor, logf func(string, ...any)) (image.Image, error) {
	return decodeImageXObject(doc, &pdf.Stream{Dict: dict, Raw: data}, fill, logf)
}

// isImageMask reports whether the image dict declares /ImageMask true.
func isImageMask(doc *pdf.Document, dict pdf.Dict) bool {
	switch v := doc.Resolve(dict["ImageMask"]).(type) {
	case pdf.Boolean:
		return bool(v)
	default:
		return false
	}
}

// imageMaskInverted reports whether a stencil mask's /Decode is [1 0] (paint on
// sample 1 instead of the default sample 0).
func imageMaskInverted(doc *pdf.Document, dict pdf.Dict) bool {
	arr := doc.GetArray(dict["Decode"])
	if len(arr) >= 2 {
		d0, _ := pdf.Number(doc.Resolve(arr[0]))
		return d0 == 1
	}
	return false
}

// decodeImageMask builds an RGBA image from a 1-bpc stencil: painted samples take
// the fill color (opaque), unpainted samples are transparent. Rows are padded to
// a byte boundary. inverted selects sample==1 as the painted value.
func decodeImageMask(data []byte, w, h int, inverted bool, fill render.FillColor) (image.Image, error) {
	rowBytes := (w + 7) / 8
	if len(data) < rowBytes*h {
		return nil, fmt.Errorf("short image-mask data: %d < %d", len(data), rowBytes*h)
	}
	paintBit := byte(0) // default /Decode [0 1]: a 0 sample paints
	if inverted {
		paintBit = 1
	}
	fc := color.RGBA{R: fill.R, G: fill.G, B: fill.B, A: fill.A}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := data[y*rowBytes:]
		for x := 0; x < w; x++ {
			bit := (row[x>>3] >> uint(7-(x&7))) & 1
			if bit == paintBit {
				img.SetRGBA(x, y, fc)
			} // else leave transparent (zero value)
		}
	}
	return img, nil
}

// applyDCTDecode applies a non-identity /Decode array to a decoded JPEG, in the
// image's native component space, and returns the remapped image (the input unchanged
// when /Decode is absent or the identity). A /Decode pair [min,max] maps a normalized
// component s to min + s*(max-min) — so [1,0] inverts. For a CMYK JPEG (the dominant
// case: Adobe files ship /Decode [1 0 1 0 1 0 1 0]) the four pairs remap C,M,Y,K before
// the RGB conversion; for an RGB/grayscale JPEG the pairs remap the RGB(A) channels.
// An unexpected pair count for the image's component model leaves the image unchanged.
func applyDCTDecode(doc *pdf.Document, img image.Image, decodeObj pdf.Object) image.Image {
	arr := doc.GetArray(decodeObj)
	if len(arr) == 0 {
		return img
	}
	dec := make([]float64, len(arr))
	identity := true
	for i, e := range arr {
		v, _ := pdf.Number(doc.Resolve(e))
		dec[i] = v
		if (i%2 == 0 && v != 0) || (i%2 == 1 && v != 1) {
			identity = false
		}
	}
	if identity {
		return img
	}
	// remap8 maps a 0..255 sample through the [min,max] pair for component c.
	remap8 := func(s uint8, c int) uint8 {
		lo, hi := dec[2*c], dec[2*c+1]
		return render.Clamp8(lo + float64(s)/255*(hi-lo))
	}
	b := img.Bounds()
	switch src := img.(type) {
	case *image.CMYK:
		if len(dec) != 8 {
			return img // a 4-component decode is required for CMYK
		}
		out := image.NewCMYK(b)
		copy(out.Pix, src.Pix)
		for i := 0; i+3 < len(out.Pix); i += 4 {
			out.Pix[i] = remap8(out.Pix[i], 0)
			out.Pix[i+1] = remap8(out.Pix[i+1], 1)
			out.Pix[i+2] = remap8(out.Pix[i+2], 2)
			out.Pix[i+3] = remap8(out.Pix[i+3], 3)
		}
		return out
	default:
		if len(dec) != 6 {
			return img // expect a 3-component (RGB) decode for non-CMYK JPEGs
		}
		out := toRGBA(img)
		for i := 0; i+3 < len(out.Pix); i += 4 {
			out.Pix[i] = remap8(out.Pix[i], 0)
			out.Pix[i+1] = remap8(out.Pix[i+1], 1)
			out.Pix[i+2] = remap8(out.Pix[i+2], 2)
			// alpha (Pix[i+3]) is untouched by /Decode
		}
		return out
	}
}

// imageDecodeArray reads a /Decode array into per-component [min,max] pairs in
// [0,1], or nil when absent or the default identity. For Indexed images /Decode
// is over index range, which we leave to the palette (returns nil).
func imageDecodeArray(doc *pdf.Document, o pdf.Object, bpc int, cs imageCS) []float64 {
	arr := doc.GetArray(o)
	if len(arr) == 0 || cs.kind == csIndexed {
		return nil
	}
	out := make([]float64, len(arr))
	identity := true
	for i, e := range arr {
		v, _ := pdf.Number(doc.Resolve(e))
		out[i] = v
		// Identity is [0 1 0 1 ...]: even indices 0, odd indices 1.
		if (i%2 == 0 && v != 0) || (i%2 == 1 && v != 1) {
			identity = false
		}
	}
	if identity {
		return nil
	}
	return out
}

// applySoftMask reads the image's /SMask (a grayscale image whose samples are the
// base image's per-pixel alpha) and writes it into base's alpha channel. Mask
// dimensions may differ from the base; samples are taken by nearest-neighbor. A
// missing or undecodable mask leaves base fully opaque.
func applySoftMask(doc *pdf.Document, s *pdf.Stream, base *image.RGBA, logf func(string, ...any)) {
	sm := doc.GetStream(s.Dict["SMask"])
	if sm == nil {
		return
	}
	data, filter, err := doc.DecodedStream(sm)
	if err != nil {
		if logf != nil {
			logf("raster: image /SMask: %v", err)
		}
		return
	}
	mw, _ := doc.GetInt(sm.Dict["Width"])
	mh, _ := doc.GetInt(sm.Dict["Height"])
	if mw <= 0 || mh <= 0 {
		return
	}
	var mask *image.RGBA // gray mask decoded as RGBA (R==G==B==alpha sample)
	switch filter {
	case "DCTDecode":
		decoded, _, derr := image.Decode(bytes.NewReader(data))
		if derr != nil {
			return
		}
		mask = toRGBA(decoded)
	case "":
		bpc, _ := doc.GetInt(sm.Dict["BitsPerComponent"])
		if bpc == 0 {
			bpc = 8
		}
		mask, err = decodeRawImage(data, mw, mh, bpc, imageCS{kind: csGray, nComps: 1})
		if err != nil {
			return
		}
	default:
		return
	}

	b := base.Bounds()
	for y := 0; y < b.Dy(); y++ {
		my := y * mh / b.Dy()
		for x := 0; x < b.Dx(); x++ {
			mx := x * mw / b.Dx()
			a := mask.RGBAAt(mx, my).R // gray sample == alpha
			c := base.RGBAAt(x, y)
			c.A = a
			base.SetRGBA(x, y, c)
		}
	}
}

// toRGBA returns img as an *image.RGBA, copying only if it is not already one.
func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.Set(x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}
