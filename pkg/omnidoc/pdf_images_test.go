package omnidoc

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// pdfImageFixture returns a PDF holding a heading, a paragraph, and one picture
// whose left half is red and right half blue — asymmetric on purpose, so a
// flipped or rotated carry-through is visible.
func pdfImageFixture(t *testing.T) []byte {
	t.Helper()
	src := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			c := color.NRGBA{R: 220, A: 255}
			if x >= 20 {
				c = color.NRGBA{B: 220, A: 255}
			}
			if y < 4 {
				c = color.NRGBA{G: 220, A: 255} // green stripe along the TOP
			}
			src.SetNRGBA(x, y, c)
		}
	}
	var pb bytes.Buffer
	if err := png.Encode(&pb, src); err != nil {
		t.Fatal(err)
	}
	html := `<html><body><h1>Image Title</h1><p>Body text above the picture.</p>` +
		`<img src="data:image/png;base64,` + base64.StdEncoding.EncodeToString(pb.Bytes()) + `" width="200" height="100">` +
		`<p>Body text below the picture.</p></body></html>`
	var out bytes.Buffer
	err := Convert(context.Background(), strings.NewReader(html), &out, ConvertOptions{
		From: FormatHTML, To: FormatPDF, BundledFonts: true,
	})
	if err != nil {
		t.Fatalf("html→pdf: %v", err)
	}
	return out.Bytes()
}

func convertPDFTo(t *testing.T, pdf []byte, to Format) []byte {
	t.Helper()
	var out bytes.Buffer
	err := Convert(context.Background(), bytes.NewReader(pdf), &out, ConvertOptions{
		From: FormatPDF, To: to, BundledFonts: true,
	})
	if err != nil {
		t.Fatalf("pdf→%s: %v", to, err)
	}
	return out.Bytes()
}

var dataURIRe = regexp.MustCompile(`data:(image/[a-z]+);base64,([A-Za-z0-9+/=]+)`)

// firstDataImage decodes the first data: image URI in s.
func firstDataImage(t *testing.T, s string) image.Image {
	t.Helper()
	m := dataURIRe.FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("no data: image URI in output:\n%.400s", s)
	}
	raw, err := base64.StdEncoding.DecodeString(m[2])
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode %s: %v", m[1], err)
	}
	return img
}

// near reports whether c is within 40 of want on every channel (JPEG tolerance).
func near(c color.Color, want color.NRGBA) bool {
	r, g, b, _ := c.RGBA()
	d := func(a uint32, w uint8) bool {
		v := int(a >> 8)
		return v > int(w)-40 && v < int(w)+40
	}
	return d(r, want.R) && d(g, want.G) && d(b, want.B)
}

func TestPDFImagesMarkdown(t *testing.T) {
	t.Parallel()
	md := string(convertPDFTo(t, pdfImageFixture(t), FormatMarkdown))
	for _, want := range []string{"Image Title", "Body text above the picture", "Body text below the picture"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%.600s", want, md)
		}
	}
	above := strings.Index(md, "above the picture")
	pic := strings.Index(md, "![")
	below := strings.Index(md, "below the picture")
	if above >= pic || pic >= below {
		t.Errorf("reading order wrong: above=%d image=%d below=%d", above, pic, below)
	}
	img := firstDataImage(t, md)
	b := img.Bounds()
	at := func(fx, fy float64) color.Color {
		return img.At(b.Min.X+int(fx*float64(b.Dx())), b.Min.Y+int(fy*float64(b.Dy())))
	}
	if !near(at(0.25, 0.7), color.NRGBA{R: 220}) {
		t.Errorf("lower-left is not red: %v", at(0.25, 0.7))
	}
	if !near(at(0.75, 0.7), color.NRGBA{B: 220}) {
		t.Errorf("lower-right is not blue: %v", at(0.75, 0.7))
	}
	if !near(at(0.5, 0.05), color.NRGBA{G: 220}) {
		t.Errorf("top stripe is not green (image flipped?): %v", at(0.5, 0.05))
	}
}

func TestPDFImagesHTML(t *testing.T) {
	t.Parallel()
	html := string(convertPDFTo(t, pdfImageFixture(t), FormatHTML))
	if !strings.Contains(html, `<img src="data:image/`) {
		t.Errorf("html lacks a data: <img>:\n%.600s", html)
	}
}

func TestPDFImagesPlainTextOmitsThem(t *testing.T) {
	t.Parallel()
	txt := string(convertPDFTo(t, pdfImageFixture(t), FormatText))
	if strings.Contains(txt, "data:") || strings.Contains(txt, "![") {
		t.Errorf("plain text carries image markup:\n%.400s", txt)
	}
	if !strings.Contains(txt, "Body text above the picture") {
		t.Errorf("plain text lost the prose:\n%.400s", txt)
	}
}

// zipNames lists the entries of a zip container.
func zipNames(t *testing.T, data []byte) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

func hasMedia(names []string, dir string) bool {
	for _, n := range names {
		if strings.HasPrefix(n, dir) && (strings.HasSuffix(n, ".png") || strings.HasSuffix(n, ".jpg") || strings.HasSuffix(n, ".jpeg")) {
			return true
		}
	}
	return false
}

func TestPDFImagesContainerFormats(t *testing.T) {
	t.Parallel()
	pdf := pdfImageFixture(t)
	cases := []struct {
		to  Format
		dir string
	}{
		{FormatDOCX, "word/media/"},
		{FormatPPTX, "ppt/media/"},
	}
	for _, c := range cases {
		t.Run(string(c.to), func(t *testing.T) {
			names := zipNames(t, convertPDFTo(t, pdf, c.to))
			if !hasMedia(names, c.dir) {
				t.Errorf("%s package carries no image under %q: %v", c.to, c.dir, names)
			}
		})
	}
}

// TestPDFImagesEPUB: the EPUB writer keeps data: image sources inline in the
// chapter XHTML (they round-trip verbatim), so the picture must appear there.
func TestPDFImagesEPUB(t *testing.T) {
	t.Parallel()
	data := convertPDFTo(t, pdfImageFixture(t), FormatEPUB)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, "chap1.xhtml") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		_, _ = b.ReadFrom(rc)
		_ = rc.Close()
		if !strings.Contains(b.String(), `<img src="data:image/`) {
			t.Errorf("chapter lacks an inline image:\n%.600s", b.String())
		}
		return
	}
	t.Error("no chap1.xhtml in the package")
}

func TestPDFImagesRTF(t *testing.T) {
	t.Parallel()
	rtf := string(convertPDFTo(t, pdfImageFixture(t), FormatRTF))
	if !strings.Contains(rtf, `\pict`) {
		t.Errorf("rtf carries no \\pict group:\n%.400s", rtf)
	}
}

// handPDF assembles a one-page PDF from raw object bodies (1-based), computing
// the xref table so the parser takes the ordinary path.
func handPDF(objs []string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		b.WriteString(strconv.Itoa(i+1) + " 0 obj\n" + o + "\nendobj\n")
	}
	xref := b.Len()
	b.WriteString("xref\n0 " + strconv.Itoa(len(objs)+1) + "\n0000000000 65535 f \n")
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	b.WriteString("trailer\n<< /Size " + strconv.Itoa(len(objs)+1) + " /Root 1 0 R >>\nstartxref\n" + strconv.Itoa(xref) + "\n%%EOF\n")
	return b.Bytes()
}

// TestPDFImageOnRotatedPage: a /Rotate 90 page shows its picture turned a quarter
// clockwise, so the carried image must be too — the red left half lands on top.
func TestPDFImageOnRotatedPage(t *testing.T) {
	t.Parallel()
	content := "q 100 0 0 50 50 100 cm /Im0 Do Q"
	pdf := handPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 300] /Rotate 90 /Contents 4 0 R /Resources << /XObject << /Im0 5 0 R >> >> >>",
		"<< /Length " + strconv.Itoa(len(content)) + " >>\nstream\n" + content + "\nendstream",
		// 2x1 RGB: red pixel (left), blue pixel (right).
		"<< /Type /XObject /Subtype /Image /Width 2 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /ASCIIHexDecode /Length 14 >>\nstream\nFF00000000FF>\nendstream",
	})
	md := string(convertPDFTo(t, pdf, FormatMarkdown))
	img := firstDataImage(t, md)
	b := img.Bounds()
	if b.Dy() <= b.Dx() {
		t.Fatalf("rotated image is %dx%d, want taller than wide", b.Dx(), b.Dy())
	}
	top := img.At(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/5)
	bottom := img.At(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()*4/5)
	if !near(top, color.NRGBA{R: 255}) || !near(bottom, color.NRGBA{B: 255}) {
		t.Errorf("top=%v bottom=%v, want red over blue", top, bottom)
	}
}

// TestPDFImageClippedByRectangle: a picture cropped by a rectangular clip carries
// only the visible pixels, not the whole bitmap behind the crop.
func TestPDFImageClippedByRectangle(t *testing.T) {
	t.Parallel()
	content := "q 50 100 50 50 re W n 100 0 0 50 50 100 cm /Im0 Do Q"
	pdf := handPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 300] /Contents 4 0 R /Resources << /XObject << /Im0 5 0 R >> >> >>",
		"<< /Length " + strconv.Itoa(len(content)) + " >>\nstream\n" + content + "\nendstream",
		"<< /Type /XObject /Subtype /Image /Width 2 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /ASCIIHexDecode /Length 14 >>\nstream\nFF00000000FF>\nendstream",
	})
	md := string(convertPDFTo(t, pdf, FormatMarkdown))
	img := firstDataImage(t, md)
	b := img.Bounds()
	if b.Dx() != 1 || b.Dy() != 1 {
		t.Fatalf("clipped image is %dx%d, want the single visible red pixel", b.Dx(), b.Dy())
	}
	if c := img.At(b.Min.X, b.Min.Y); !near(c, color.NRGBA{R: 255}) {
		t.Errorf("visible pixel = %v, want red", c)
	}
}
