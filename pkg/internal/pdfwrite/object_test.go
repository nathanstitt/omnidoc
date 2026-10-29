package pdfwrite

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/nathanstitt/omnidoc/pkg/pdf"
)

// TestSerializeMinimalPDFParses builds a tiny PDF (catalog + 1 page, no content)
// and re-parses it with the project's own parser as the oracle.
func TestSerializeMinimalPDFParses(t *testing.T) {
	w := newWriter()

	pages := w.alloc()
	page := w.alloc()
	catalog := w.alloc()

	w.put(catalog, Dict{"Type": Name("Catalog"), "Pages": pages})
	w.put(pages, Dict{
		"Type":  Name("Pages"),
		"Kids":  Array{page},
		"Count": Int(1),
	})
	w.put(page, Dict{
		"Type":     Name("Page"),
		"Parent":   pages,
		"MediaBox": Array{Int(0), Int(0), Int(612), Int(792)},
	})
	w.setRoot(catalog)

	var buf bytes.Buffer
	if err := w.serialize(&buf); err != nil {
		t.Fatalf("serialize: %v", err)
	}

	doc, err := pdf.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("pkg/pdf failed to parse our output: %v", err)
	}
	if got := doc.PageCount(); got != 1 {
		t.Fatalf("page count = %d; want 1", got)
	}
}

// TestSerializeStreamFlateRoundTrips asserts addStream flate-encodes its content,
// declares the filter, and that the project's own parser decodes it back. It
// does not check the encoded bytes: for short input compress/flate may write a
// stored block, which is valid FlateDecode data and differs by Go version.
func TestSerializeStreamFlateRoundTrips(t *testing.T) {
	w := newWriter()
	content := []byte("BT /F1 12 Tf (hi) Tj ET")
	sid := w.addStream(Dict{}, content)
	if sid == 0 {
		t.Fatal("addStream returned zero id")
	}
	// Keep the stream reachable so serialize doesn't error on an unfilled object.
	page := w.alloc()
	pages := w.alloc()
	catalog := w.alloc()
	w.put(page, Dict{
		"Type":     Name("Page"),
		"Parent":   pages,
		"MediaBox": Array{Int(0), Int(0), Int(612), Int(792)},
		"Contents": sid,
	})
	w.put(pages, Dict{"Type": Name("Pages"), "Kids": Array{page}, "Count": Int(1)})
	w.put(catalog, Dict{"Type": Name("Catalog"), "Pages": pages})
	w.setRoot(catalog)

	var buf bytes.Buffer
	if err := w.serialize(&buf); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	doc, err := pdf.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("pkg/pdf failed to parse output with stream: %v", err)
	}
	s := doc.GetStream(pdf.Reference{Number: int(sid)})
	if s == nil {
		t.Fatal("stream object not found in output")
	}
	if f, _ := doc.GetName(s.Dict["Filter"]); f != "FlateDecode" {
		t.Fatalf("/Filter = %q, want FlateDecode", f)
	}
	got, _, err := doc.DecodedStream(s)
	if err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("decoded stream = %q, want %q", got, content)
	}
}

// TestFormatRealRejectsNonFiniteAndExtreme proves formatReal never emits
// "Inf"/"NaN" tokens or an unbounded-length number: a finite-but-astronomical
// input (reachable today from e.g. an SVG coordinate like 1.7e308, or from
// ordinary arithmetic like x+width overflowing) formats verbatim with 'f' into
// a 300+ digit token, and a non-finite input formats as literally "+Inf" or
// "NaN" — both are structurally invalid inside a PDF content stream or object
// dictionary.
func TestFormatRealRejectsNonFiniteAndExtreme(t *testing.T) {
	cases := []struct {
		name string
		in   float64
	}{
		{"nan", math.NaN()},
		{"+inf", math.Inf(1)},
		{"-inf", math.Inf(-1)},
		{"huge-finite", 1.7e308},
		{"huge-finite-negative", -1.7e308},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := formatReal(c.in)
			lower := strings.ToLower(s)
			if strings.Contains(lower, "inf") || strings.Contains(lower, "nan") {
				t.Fatalf("formatReal(%v) = %q, contains Inf/NaN", c.in, s)
			}
			if len(s) > 64 {
				t.Fatalf("formatReal(%v) = %q, length %d exceeds sane bound", c.in, s, len(s))
			}
		})
	}
}
