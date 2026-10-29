package omnidoc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/nathanstitt/omnidoc/testdata/gen"
)

// svgGoldenFixtures are the core fixtures whose emitted SVG is committed.
//
// A subset rather than all of gen.Core: these goldens exist to make a change in
// the emitted MARKUP visible in review, and one fixture per distinct construct
// does that. Adding the rest would multiply diff noise without covering a new
// code path, and the pixel-level sweep in svgwrite_test.go already runs the
// whole corpus.
var svgGoldenFixtures = []struct {
	name  string
	build func() []byte
	why   string
}{
	{"text", gen.TextPDF, "glyph outlines hoisted into defs and referenced by use"},
	{"vector", gen.VectorPDF, "fill and stroke paths with their paint attributes"},
	{"even-odd", gen.EvenOddPDF, "a non-default fill rule reaches the path"},
	{"stroke-joins", gen.StrokeJoinsPDF, "cap/join/miter attributes"},
	{"image-flate", gen.ImagePDF, "raster content as a placed data-URI image"},
	{"shading-axial", gen.ShadingAxialPDF, "a shading with no native form falls back to sampling"},
	{"form-xobject", gen.FormXObjectPDF, "nested content keeps its group and clip nesting"},
	{"rotated", func() []byte { return gen.RotatedPDF(90) }, "/Rotate folded into the page transform"},
}

// TestSVGWriteGolden commits the emitted SVG markup for a representative set of
// fixtures.
//
// This is a TEXT golden, deliberately, and it complements rather than duplicates
// the pixel comparison in svgwrite_test.go. The two fail on different things: a
// pixel diff catches output that renders wrong, while this catches output that
// renders the same but says something different — a switch from <use> back to
// inline paths, a lost fill-rule, an attribute that silently stopped being
// emitted. Those are exactly the regressions that a rasterizing check cannot
// see, since several encodings of the same drawing produce identical pixels.
//
// Regenerate: go test ./pkg/omnidoc -run TestSVGWriteGolden -update
// Then read the diff — an unexplained change here is a regression.
func TestSVGWriteGolden(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("testdata", "golden", "svgwrite")
	if *update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range svgGoldenFixtures {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			doc, err := OpenBytes(f.build())
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			var buf bytes.Buffer
			// BundledFonts keeps the emitted glyph outlines reproducible: a
			// host font would make the committed geometry machine-specific.
			if err := doc.WriteSVG(context.Background(), &buf, 0, SVGOptions{
				BundledFonts: true,
				Background:   color.White,
			}); err != nil {
				t.Fatalf("WriteSVG: %v", err)
			}

			path := filepath.Join(dir, f.name+".svg")
			if *update {
				if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
				t.Logf("updated %s (%s)", path, f.why)
				return
			}
			want, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				t.Fatalf("missing golden %s; run: go test ./pkg/omnidoc -run TestSVGWriteGolden -update", path)
			}
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			want, got := normalizePNGURIs(t, want), normalizePNGURIs(t, buf.Bytes())
			if !bytes.Equal(want, got) {
				t.Errorf("emitted SVG differs from golden %s (%s).\nRegenerate with -update and review the diff.\n%s",
					path, f.why, firstDiff(want, got))
			}
		})
	}
}

// TestSVGWriteGoldenIsDeterministic guards the property the goldens depend on:
// the same input must produce byte-identical output every run.
//
// Element ids are minted from a counter and glyph outlines are cached in a map,
// so any iteration over that map — or any id derived from a pointer or from
// wall-clock time — would make the writer emit different bytes each run. That
// breaks the goldens intermittently, which is far worse than breaking them
// consistently, so it is asserted directly rather than left to flakiness.
func TestSVGWriteGoldenIsDeterministic(t *testing.T) {
	t.Parallel()
	doc, err := OpenBytes(gen.TextPDF())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var first []byte
	for i := range 3 {
		var buf bytes.Buffer
		if err := doc.WriteSVG(context.Background(), &buf, 0, SVGOptions{BundledFonts: true}); err != nil {
			t.Fatalf("WriteSVG: %v", err)
		}
		if i == 0 {
			first = bytes.Clone(buf.Bytes())
			continue
		}
		if !bytes.Equal(first, buf.Bytes()) {
			t.Fatalf("run %d differs from run 0: output is not deterministic\n%s", i, firstDiff(first, buf.Bytes()))
		}
	}
}

// pngURI matches a base64 PNG data URI as WriteSVG emits it for raster content.
var pngURI = regexp.MustCompile(`data:image/png;base64,[A-Za-z0-9+/=]+`)

// normalizePNGURIs replaces each PNG data URI in svg with a digest of its
// decoded pixels. The PNG bytes depend on the compress/flate encoder, which
// changes between Go releases, so the golden compares what the image holds,
// not how it was compressed.
func normalizePNGURIs(t *testing.T, svg []byte) []byte {
	t.Helper()
	return pngURI.ReplaceAllFunc(svg, func(uri []byte) []byte {
		raw, err := base64.StdEncoding.DecodeString(string(uri[len("data:image/png;base64,"):]))
		if err != nil {
			t.Fatalf("decode PNG data URI: %v", err)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("decode PNG: %v", err)
		}
		b := img.Bounds()
		rgba := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
		return fmt.Appendf(nil, "png:%dx%d:%x", b.Dx(), b.Dy(), sha256.Sum256(rgba.Pix))
	})
}

// firstDiff reports the first differing line, with context, so a golden failure
// names the change rather than dumping two whole documents.
func firstDiff(want, got []byte) string {
	wl, gl := bytes.Split(want, []byte("\n")), bytes.Split(got, []byte("\n"))
	for i := range max(len(wl), len(gl)) {
		var w, g []byte
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if !bytes.Equal(w, g) {
			return "first difference at line " + itoa(i+1) + ":\n  want: " + trunc(w) + "\n  got:  " + trunc(g)
		}
	}
	return "(files differ only in trailing bytes)"
}

func trunc(b []byte) string {
	const limit = 160
	if len(b) > limit {
		return string(b[:limit]) + "…"
	}
	return string(b)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
