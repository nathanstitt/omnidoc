package extract

import (
	"image"
	"image/color"
	"math"
	"math/rand"
	"strings"
	"testing"

	"github.com/nathanstitt/omnidoc/pkg/internal/layout/cssbox"
	"github.com/nathanstitt/omnidoc/pkg/internal/render"
)

var (
	red  = color.NRGBA{R: 255, A: 255}
	blue = color.NRGBA{B: 255, A: 255}
)

// sideBySide is a 2x1 image: red left pixel, blue right pixel.
func sideBySide() *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	im.SetNRGBA(0, 0, red)
	im.SetNRGBA(1, 0, blue)
	return im
}

func refFor(img image.Image, ctm render.Matrix) imageRef {
	x0, y0, x1, y1 := unitQuadBox(ctm)
	return imageRef{img: img, ctm: ctm, x0: x0, y0: y0, x1: x1, y1: y1}
}

func colorAt(img image.Image, fx, fy float64) color.NRGBA {
	b := img.Bounds()
	return color.NRGBAModel.Convert(img.At(b.Min.X+int(fx*float64(b.Dx())), b.Min.Y+int(fy*float64(b.Dy())))).(color.NRGBA)
}

// TestUprightOrientation pins that the PDF CTM's flips and right-angle rotations
// are baked into the pixels, since an <img> carries no transform. Device space is
// y-down; the unit square's u runs along the image's columns and v up its rows.
func TestUprightOrientation(t *testing.T) {
	const w, h = 100.0, 50.0
	cases := []struct {
		name string
		ctm  render.Matrix
		// colors at the left/right (or top/bottom for rotations) of the output
		a, b     color.NRGBA // first and second probe
		vertical bool        // probe top/bottom instead of left/right
	}{
		{"upright", render.Matrix{A: w, D: -h, E: 10, F: 80}, red, blue, false},
		{"mirrored", render.Matrix{A: -w, D: -h, E: 110, F: 80}, blue, red, false},
		{"rotated 180", render.Matrix{A: -w, D: h, E: 110, F: 30}, blue, red, false},
		{"rotated 90 clockwise", render.Matrix{B: w, C: h, E: 10, F: 30}, red, blue, true},
		{"rotated 90 counterclockwise", render.Matrix{B: -w, C: -h, E: 60, F: 130}, blue, red, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := refFor(sideBySide(), c.ctm).upright()
			var first, second color.NRGBA
			if c.vertical {
				first, second = colorAt(out, 0.5, 0.2), colorAt(out, 0.5, 0.8)
			} else {
				first, second = colorAt(out, 0.2, 0.5), colorAt(out, 0.8, 0.5)
			}
			if first != c.a || second != c.b {
				t.Errorf("got %v then %v, want %v then %v", first, second, c.a, c.b)
			}
		})
	}
}

func TestUprightUntouchedWhenUpright(t *testing.T) {
	src := sideBySide()
	got := refFor(src, render.Matrix{A: 100, D: -50, E: 0, F: 50}).upright()
	if got != image.Image(src) {
		t.Error("an upright image was resampled; want the source returned as is")
	}
}

func TestKeepImageDropsTinyMarks(t *testing.T) {
	big := refFor(sideBySide(), render.Matrix{A: 40, D: -40, F: 40})
	thin := refFor(sideBySide(), render.Matrix{A: 400, D: -1, F: 1})
	dot := refFor(sideBySide(), render.Matrix{A: 1, D: -1, F: 1})
	swatch := refFor(sideBySide(), render.Matrix{A: 3.84, D: -3.84, F: 3.84})
	if !keepImage(big) || !keepImage(swatch) || keepImage(thin) || keepImage(dot) {
		t.Errorf("keep(big,swatch,thin,dot) = %v,%v,%v,%v; want true,true,false,false", keepImage(big), keepImage(swatch), keepImage(thin), keepImage(dot))
	}
}

func TestCollectorIgnoresInvisibleAndNonFinite(t *testing.T) {
	c := &collector{}
	ok := render.Matrix{A: 10, D: -10, F: 10}
	c.image(sideBySide(), ok, 0, clipBox{})
	c.image(nil, ok, 1, clipBox{})
	c.image(sideBySide(), render.Matrix{A: math.NaN(), D: -10}, 1, clipBox{})
	c.image(sideBySide(), render.Matrix{A: math.Inf(1), D: -10}, 1, clipBox{})
	c.image(sideBySide(), ok, 1, clipBox{})
	if len(c.images) != 1 {
		t.Fatalf("recorded %d images, want only the valid one", len(c.images))
	}
}

func TestDataURIPicksSmallerEncoding(t *testing.T) {
	flat := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := range flat.Pix {
		flat.Pix[i] = 255
	}
	if u, ok := dataURI(flat); !ok || !strings.HasPrefix(u, "data:image/png;base64,") {
		t.Errorf("flat graphic = %.30s, want PNG", u)
	}

	noisy := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	r := rand.New(rand.NewSource(1))
	for i := 0; i < len(noisy.Pix); i += 4 {
		noisy.Pix[i], noisy.Pix[i+1], noisy.Pix[i+2], noisy.Pix[i+3] = uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), 255
	}
	if u, ok := dataURI(noisy); !ok || !strings.HasPrefix(u, "data:image/jpeg;base64,") {
		t.Errorf("photographic image = %.30s, want JPEG", u)
	}

	clear := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := 0; i < len(clear.Pix); i += 4 {
		clear.Pix[i], clear.Pix[i+1], clear.Pix[i+2], clear.Pix[i+3] = uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), 128
	}
	if u, ok := dataURI(clear); !ok || !strings.HasPrefix(u, "data:image/png;base64,") {
		t.Errorf("translucent image = %.30s, want PNG (JPEG has no alpha)", u)
	}
}

func imgBox(t *testing.T, b *cssbox.Box) *cssbox.ReplacedContent {
	t.Helper()
	if b.SemTag != "p" || len(b.Children) != 1 || b.Children[0].Replaced == nil {
		t.Fatalf("not an image paragraph: %+v", b)
	}
	return b.Children[0].Replaced
}

func TestLowerImageShape(t *testing.T) {
	// 150pt x 75pt on the page is 200px x 100px at 96dpi.
	b := lowerImage(refFor(sideBySide(), render.Matrix{A: 150, D: -75, F: 75}), nil)
	rc := imgBox(t, b)
	if rc.Tag != "img" || !strings.HasPrefix(rc.Attrs["src"], "data:image/") {
		t.Errorf("replaced = %+v", rc)
	}
	if rc.Attrs["width"] != "200" || rc.Attrs["height"] != "100" {
		t.Errorf("size = %sx%s, want 200x100", rc.Attrs["width"], rc.Attrs["height"])
	}
}

func lineAt(text string, x0, y float64) line {
	g := sizedGlyphs(text, x0, y, 6, 10)
	return buildLines(g)[0]
}

func TestPlaceImagesFollowsColumnFlow(t *testing.T) {
	left1 := block{lines: []line{lineAt("left one", 20, 100)}}
	left2 := block{lines: []line{lineAt("left two", 20, 300)}}
	right1 := block{lines: []line{lineAt("right one", 320, 100)}}
	right2 := block{lines: []line{lineAt("right two", 320, 300)}}
	blocks := []block{left1, left2, right1, right2} // reading order: left column, then right

	inRight := refFor(sideBySide(), render.Matrix{A: 100, D: -50, E: 320, F: 250}) // top y=200, right column
	inLeft := refFor(sideBySide(), render.Matrix{A: 100, D: -50, E: 20, F: 250})   // top y=200, left column
	below := refFor(sideBySide(), render.Matrix{A: 100, D: -50, E: 20, F: 600})    // below everything

	slots := placeImages(blocks, []imageRef{inRight, inLeft, below})
	if len(slots[1]) != 1 || slots[1][0].x0 != 20 {
		t.Errorf("left-column image slots = %v, want it before left two (slot 1)", slots)
	}
	if len(slots[3]) != 1 || slots[3][0].x0 != 320 {
		t.Errorf("right-column image slots = %v, want it before right two (slot 3)", slots)
	}
	if len(slots[4]) != 1 {
		t.Errorf("below-everything image slots = %v, want the tail slot", slots)
	}
}

func TestLowerPageInterleavesImage(t *testing.T) {
	var g []glyph
	g = append(g, sizedGlyphs("Above", 0, 60, 6, 10)...)
	g = append(g, sizedGlyphs("Below", 0, 300, 6, 10)...)
	pc := &pageContent{glyphs: g, width: 600, height: 800,
		images: []imageRef{refFor(sideBySide(), render.Matrix{A: 100, D: -50, F: 200})}} // top y=150
	boxes := lowerPage(pc, nil)
	if len(boxes) != 3 {
		t.Fatalf("got %d boxes, want text, image, text", len(boxes))
	}
	imgBox(t, boxes[1])
}

func TestLowerPageImageOnly(t *testing.T) {
	pc := &pageContent{width: 600, height: 800, images: []imageRef{
		refFor(sideBySide(), render.Matrix{A: 100, D: -50, F: 400}),
		refFor(sideBySide(), render.Matrix{A: 100, D: -50, F: 100}),
	}}
	boxes := lowerPage(pc, nil)
	if len(boxes) != 2 {
		t.Fatalf("got %d boxes, want 2 image paragraphs", len(boxes))
	}
}

func TestLowerPageDropsTinyImageWithLog(t *testing.T) {
	var logged []string
	pc := &pageContent{width: 600, height: 800, images: []imageRef{
		refFor(sideBySide(), render.Matrix{A: 1, D: -1, F: 1}),
	}}
	boxes := lowerPage(pc, func(f string, a ...any) { logged = append(logged, f) })
	if len(boxes) != 0 || len(logged) != 1 {
		t.Errorf("boxes=%d logged=%v, want 0 boxes and one debug log", len(boxes), logged)
	}
}

func rectPath(x0, y0, x1, y1 float64) *render.Path {
	p := &render.Path{}
	p.MoveTo(x0, y0)
	p.LineTo(x1, y0)
	p.LineTo(x1, y1)
	p.LineTo(x0, y1)
	p.Close()
	return p
}

// TestNullDeviceClipScopesToSaveRestore pins that a clip applies to the images
// painted inside its q...Q and not to those after it.
func TestNullDeviceClipScopesToSaveRestore(t *testing.T) {
	c := &collector{}
	d := &nullDevice{w: 300, h: 300, c: c}
	ctm := render.Matrix{A: 100, D: -50, F: 50}
	d.Save()
	d.PushClip(rectPath(0, 0, 50, 50), render.NonZero)
	d.PushClip(rectPath(0, 0, 80, 20), render.NonZero) // nests: 50x20 remains
	d.DrawImage(sideBySide(), ctm, 1, "")
	d.Restore()
	d.DrawImage(sideBySide(), ctm, 1, "")
	if len(c.images) != 2 {
		t.Fatalf("recorded %d images, want 2", len(c.images))
	}
	if got, want := c.images[0].clip, (clipBox{set: true, x0: 0, y0: 0, x1: 50, y1: 20}); got != want {
		t.Errorf("clipped image clip = %+v, want %+v", got, want)
	}
	if c.images[1].clip.set {
		t.Errorf("image after Restore is still clipped: %+v", c.images[1].clip)
	}
}

// TestCroppedKeepsOnlyVisiblePixels: clipping a 100x50 red|blue image to its left
// quarter leaves a red-only crop covering that quarter of the box.
func TestCroppedKeepsOnlyVisiblePixels(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 8, 2))
	for x := 0; x < 8; x++ {
		for y := 0; y < 2; y++ {
			if x < 4 {
				src.SetNRGBA(x, y, red)
			} else {
				src.SetNRGBA(x, y, blue)
			}
		}
	}
	r := refFor(src, render.Matrix{A: 100, D: -50, F: 50})
	r.clip = clipBox{set: true, x0: 0, y0: 0, x1: 25, y1: 50}
	got, ok := r.cropped()
	if !ok {
		t.Fatal("a partly visible image reported hidden")
	}
	if got.x1-got.x0 != 25 || got.y1-got.y0 != 50 {
		t.Errorf("visible box = %vx%v, want 25x50", got.x1-got.x0, got.y1-got.y0)
	}
	b := got.img.Bounds()
	if b.Dx() != 2 || b.Dy() != 2 {
		t.Errorf("cropped pixels = %dx%d, want 2x2", b.Dx(), b.Dy())
	}
	for x := b.Min.X; x < b.Max.X; x++ {
		if c := color.NRGBAModel.Convert(got.img.At(x, b.Min.Y)).(color.NRGBA); c != red {
			t.Errorf("pixel %d = %v, want only red", x, c)
		}
	}
	// A crop must still render upright through the normal path.
	if up := got.upright(); up != got.img {
		t.Error("cropped image was resampled again; its matrix should already be upright")
	}
}

func TestCroppedHiddenAndUnclipped(t *testing.T) {
	r := refFor(sideBySide(), render.Matrix{A: 100, D: -50, F: 50})
	if _, ok := r.cropped(); !ok {
		t.Error("unclipped image reported hidden")
	}
	r.clip = clipBox{set: true, x0: 200, y0: 200, x1: 300, y1: 300}
	if _, ok := r.cropped(); ok {
		t.Error("image fully outside its clip reported visible")
	}
	r.clip = clipBox{set: true, x0: -10, y0: -10, x1: 500, y1: 500}
	got, ok := r.cropped()
	if !ok || got.img != r.img {
		t.Error("an image inside its clip must come back untouched")
	}
}
