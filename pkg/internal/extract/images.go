package extract

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"sort"
	"strconv"

	gcss "github.com/nathanstitt/omnidoc/pkg/internal/css"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/cssbox"
	"github.com/nathanstitt/omnidoc/pkg/internal/render"
)

// This file carries a PDF's raster images into the recovered box tree. The
// interpreter paints every image through Device.DrawImage with a matrix mapping
// the unit square to device space; the extractor's device records that call, and
// lowering turns each record into a block-level <img> whose src is a data: URI,
// placed among the prose blocks by its position on the page. Every structure
// writer (Markdown, HTML, DOCX, RTF, EPUB, PPTX, ...) already renders a data: <img>
// replaced box, so none of them needs PDF-specific code.

// minImagePt is the smallest side (points) an image may have and still be carried.
// Smaller marks are spacers and hairlines (a 1-pixel image stretched into a rule);
// as content they are noise. A genuine tiny picture, like the 3.84pt swatches in
// imagemagick-images.pdf, stays above this floor.
const minImagePt = 2.0

// imageRef is one painted image in device space (points, y-down).
type imageRef struct {
	img            image.Image
	ctm            render.Matrix
	x0, y0, x1, y1 float64 // bounding box of the transformed unit square
	clip           clipBox // bounding box of the clip active when it was painted
}

// clipBox is a device-space rectangle. The zero value means "unclipped" (set is
// false); a set box with x1 <= x0 or y1 <= y0 clips everything away.
type clipBox struct {
	set            bool
	x0, y0, x1, y1 float64
}

// boundsOf returns p's bounding box as a clip. An empty path clips everything.
func boundsOf(p *render.Path) clipBox {
	if p == nil {
		return clipBox{set: true}
	}
	x0, y0, x1, y1, ok := p.Bounds()
	if !ok {
		return clipBox{set: true}
	}
	return clipBox{set: true, x0: x0, y0: y0, x1: x1, y1: y1}
}

// intersect narrows c by o. Clip paths are reduced to their bounding boxes, so a
// non-rectangular clip (a circle crop) keeps its whole bounding square: the
// approximation errs toward showing more picture, never less.
func (c clipBox) intersect(o clipBox) clipBox {
	if !c.set {
		return o
	}
	if !o.set {
		return c
	}
	return clipBox{set: true,
		x0: math.Max(c.x0, o.x0), y0: math.Max(c.y0, o.y0),
		x1: math.Min(c.x1, o.x1), y1: math.Min(c.y1, o.y1)}
}

// image records one DrawImage call. A fully transparent paint draws nothing, so it
// is not content.
func (c *collector) image(img image.Image, ctm render.Matrix, alpha float64, clip clipBox) {
	if img == nil || alpha <= 0 {
		return
	}
	x0, y0, x1, y1 := unitQuadBox(ctm)
	if !finite(x0, y0, x1, y1) {
		return
	}
	c.images = append(c.images, imageRef{img: img, ctm: ctm, x0: x0, y0: y0, x1: x1, y1: y1, clip: clip})
}

// unitQuadBox returns the device-space bounding box of ctm's unit square.
func unitQuadBox(m render.Matrix) (x0, y0, x1, y1 float64) {
	x0, y0 = math.Inf(1), math.Inf(1)
	x1, y1 = math.Inf(-1), math.Inf(-1)
	for _, p := range [4][2]float64{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		x, y := m.Apply(p[0], p[1])
		x0, x1 = math.Min(x0, x), math.Max(x1, x)
		y0, y1 = math.Min(y0, y), math.Max(y1, y)
	}
	return
}

func finite(vs ...float64) bool {
	for _, v := range vs {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// cropped returns r reduced to the part its clip leaves visible, with the pixels
// cropped to match. ok is false when the clip hides the whole image. An unclipped
// or fully visible image comes back unchanged.
func (r imageRef) cropped() (imageRef, bool) {
	if !r.clip.set {
		return r, true
	}
	v := r.clip.intersect(clipBox{set: true, x0: r.x0, y0: r.y0, x1: r.x1, y1: r.y1})
	if v.x1 <= v.x0 || v.y1 <= v.y0 {
		return r, false
	}
	if v.x0 <= r.x0 && v.y0 <= r.y0 && v.x1 >= r.x1 && v.y1 >= r.y1 {
		return r, true
	}
	up := r.upright()
	b := up.Bounds()
	bw, bh := r.x1-r.x0, r.y1-r.y0
	px := func(frac float64, n int, lo int) int { return lo + int(math.Round(frac*float64(n))) }
	rect := image.Rect(
		px((v.x0-r.x0)/bw, b.Dx(), b.Min.X), px((v.y0-r.y0)/bh, b.Dy(), b.Min.Y),
		px((v.x1-r.x0)/bw, b.Dx(), b.Min.X), px((v.y1-r.y0)/bh, b.Dy(), b.Min.Y),
	).Intersect(b)
	if rect.Empty() {
		return r, false
	}
	sub, ok := up.(interface {
		SubImage(image.Rectangle) image.Image
	})
	if !ok {
		return r, true
	}
	// The cropped pixels are already upright, so the identity-upright matrix of the
	// visible box stands in for the original CTM.
	return imageRef{
		img: sub.SubImage(rect), x0: v.x0, y0: v.y0, x1: v.x1, y1: v.y1,
		ctm: render.Matrix{A: v.x1 - v.x0, D: -(v.y1 - v.y0), E: v.x0, F: v.y1},
	}, true
}

// keepImage reports whether an image is large enough to be content.
func keepImage(r imageRef) bool {
	return r.x1-r.x0 >= minImagePt && r.y1-r.y0 >= minImagePt
}

// upright returns the image as a viewer shows it: the pixels reoriented so the top
// row is up and the left column is left in device space. The PDF CTM may flip,
// rotate by a right angle, or rotate arbitrarily, while an <img> cannot carry a
// transform. The common upright case is returned untouched.
func (r imageRef) upright() image.Image {
	m := r.ctm
	if m.B == 0 && m.C == 0 && m.A > 0 && m.D < 0 {
		return r.img
	}
	sb := r.img.Bounds()
	w, h := sb.Dx(), sb.Dy()
	// Output at about the source pixel density: scale the device box so one of its
	// axes matches the matching source axis' pixel count.
	bw, bh := r.x1-r.x0, r.y1-r.y0
	// Source pixels per device point along each source axis.
	sx := math.Hypot(m.A, m.B)
	sy := math.Hypot(m.C, m.D)
	if sx <= 0 || sy <= 0 {
		return r.img
	}
	density := math.Max(float64(w)/sx, float64(h)/sy)
	ow := clampDim(bw * density)
	oh := clampDim(bh * density)
	inv, ok := invertMatrix(m)
	if !ok {
		return r.img
	}
	out := image.NewNRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			dx := r.x0 + (float64(x)+0.5)*bw/float64(ow)
			dy := r.y0 + (float64(y)+0.5)*bh/float64(oh)
			u, v := inv.Apply(dx, dy)
			if u < 0 || u >= 1 || v < 0 || v >= 1 {
				continue
			}
			px := sb.Min.X + int(u*float64(w))
			py := sb.Min.Y + int((1-v)*float64(h))
			if px >= sb.Max.X {
				px = sb.Max.X - 1
			}
			if py >= sb.Max.Y {
				py = sb.Max.Y - 1
			}
			out.Set(x, y, color.NRGBAModel.Convert(r.img.At(px, py)))
		}
	}
	return out
}

// clampDim bounds an output dimension to [1, 8192] so a degenerate matrix cannot
// ask for a huge buffer.
func clampDim(v float64) int {
	n := int(math.Round(v))
	if n < 1 {
		return 1
	}
	if n > 8192 {
		return 8192
	}
	return n
}

func invertMatrix(m render.Matrix) (render.Matrix, bool) {
	det := m.A*m.D - m.B*m.C
	if det == 0 || math.IsNaN(det) || math.IsInf(det, 0) {
		return render.Matrix{}, false
	}
	return render.Matrix{
		A: m.D / det, B: -m.B / det,
		C: -m.C / det, D: m.A / det,
		E: (m.C*m.F - m.D*m.E) / det,
		F: (m.B*m.E - m.A*m.F) / det,
	}, true
}

// dataURI encodes img as a data: URI. Opaque images take JPEG when that is smaller
// than PNG (photographs); everything else, and every image with transparency,
// takes lossless PNG. The choice depends only on the pixels, so output is
// deterministic.
func dataURI(img image.Image) (string, bool) {
	var pb bytes.Buffer
	if err := png.Encode(&pb, img); err != nil {
		return "", false
	}
	mime, data := "image/png", pb.Bytes()
	if isOpaque(img) {
		var jb bytes.Buffer
		if err := jpeg.Encode(&jb, img, &jpeg.Options{Quality: 90}); err == nil && jb.Len() < pb.Len()/2 {
			mime, data = "image/jpeg", jb.Bytes()
		}
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), true
}

func isOpaque(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return o.Opaque()
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return false
			}
		}
	}
	return true
}

// lowerImage builds a block-level paragraph holding one <img> replaced box. The
// width and height attributes are CSS pixels (96/in), the size the page paints the
// image at, so writers that size by attribute reproduce the page's layout.
func lowerImage(r imageRef, logf func(string, ...any)) *cssbox.Box {
	src, ok := dataURI(r.upright())
	if !ok {
		if logf != nil {
			logf("extract: image at (%.0f,%.0f) could not be encoded; skipped", r.x0, r.y0)
		}
		return nil
	}
	const pxPerPt = 96.0 / 72.0
	st := gcss.InitialStyle()
	st.Display = "inline"
	img := &cssbox.Box{
		Kind: cssbox.BoxReplaced, Display: cssbox.DisplayInline, Style: st,
		Replaced: &cssbox.ReplacedContent{Tag: "img", Attrs: map[string]string{
			"src":    src,
			"alt":    "",
			"width":  strconv.Itoa(int(math.Round((r.x1 - r.x0) * pxPerPt))),
			"height": strconv.Itoa(int(math.Round((r.y1 - r.y0) * pxPerPt))),
		}},
	}
	p := blockBox(cssbox.InlineFC)
	p.SemTag = "p"
	p.Children = []*cssbox.Box{img}
	return p
}

// placeImages assigns each kept image a slot among the reading-order blocks:
// the returned map's key i holds the images to emit before blocks[i], and key
// len(blocks) those after the last block. An image goes before the first block that
// starts at or below the image's top and shares horizontal extent with it, so an
// image in the left column of a two-column page lands in the left column's flow.
// With no such block it goes before the first block starting below its top, and
// failing that, at the end. Images in one slot keep top-to-bottom, left-to-right
// order.
func placeImages(blocks []block, imgs []imageRef) map[int][]imageRef {
	if len(imgs) == 0 {
		return nil
	}
	sorted := append([]imageRef(nil), imgs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].y0 != sorted[j].y0 {
			return sorted[i].y0 < sorted[j].y0
		}
		return sorted[i].x0 < sorted[j].x0
	})
	slots := map[int][]imageRef{}
	for _, im := range sorted {
		at := len(blocks)
		fallback := -1
		for i, b := range blocks {
			if blockTop(b) < im.y0-snapTol {
				continue
			}
			if fallback < 0 {
				fallback = i
			}
			if overlapsX(b, im) {
				at = i
				break
			}
		}
		if at == len(blocks) && fallback >= 0 {
			at = fallback
		}
		slots[at] = append(slots[at], im)
	}
	return slots
}

// overlapsX reports whether a block's horizontal extent intersects the image's.
func overlapsX(b block, im imageRef) bool {
	for _, l := range b.lines {
		if l.x1 > im.x0 && l.x0 < im.x1 {
			return true
		}
	}
	return false
}
