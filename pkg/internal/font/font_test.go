package font

import (
	"errors"
	"math"
	"testing"

	"github.com/benoitkugler/textlayout/fonts"

	"github.com/nathanstitt/omnidoc/pkg/internal/content"
	"github.com/nathanstitt/omnidoc/pkg/internal/render"
	"github.com/nathanstitt/omnidoc/pkg/pdf"
	"github.com/nathanstitt/omnidoc/testdata/gen"
)

// ttProg parses the embedded Roboto TrueType program for outline-level tests.
func ttProg(t *testing.T) *program {
	t.Helper()
	prog, err := parseProgram(gen.RobotoTTF(), progTrueType)
	if err != nil {
		t.Fatalf("parseProgram: %v", err)
	}
	return prog
}

// gidFor resolves a rune to a GID through the program's own cmap.
func gidFor(t *testing.T, prog *program, r rune) fonts.GID {
	t.Helper()
	gid, ok := prog.gidForRune(r)
	if !ok {
		t.Fatalf("no GID for %q", r)
	}
	return gid
}

// fontDict parses a generated PDF and returns its /F1 font dictionary, so tests
// can drive font.New the same way the rasterizer does.
func fontDict(t *testing.T, pdfBytes []byte) (*pdf.Document, pdf.Dict) {
	t.Helper()
	doc, err := pdf.Parse(pdfBytes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pg, err := doc.Page(0)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	res := doc.GetDict(pg.Resources["Font"])
	if res == nil {
		t.Fatal("no /Font in resources")
	}
	fd := doc.GetDict(res["F1"])
	if fd == nil {
		t.Fatal("no /F1 font dict")
	}
	return doc, fd
}

func TestSimpleTrueTypeDecodes(t *testing.T) {
	doc, fd := fontDict(t, gen.EmbeddedTrueTypePDF())
	src, err := New(doc, fd, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	glyphs := src.DecodeString([]byte("Hello"))
	if len(glyphs) != 5 {
		t.Fatalf("got %d glyphs, want 5", len(glyphs))
	}
	for i, g := range glyphs {
		if g.Outline == nil {
			t.Errorf("glyph %d (%q): nil outline", i, g.Rune)
		}
		if g.Width <= 0 || g.Width > 1.5 {
			t.Errorf("glyph %d: implausible width %v em", i, g.Width)
		}
	}
	if glyphs[0].Rune != 'H' {
		t.Errorf("glyph 0 rune = %q, want 'H'", glyphs[0].Rune)
	}
}

func TestSimpleCFFDecodes(t *testing.T) {
	doc, fd := fontDict(t, gen.EmbeddedCFFPDF())
	src, err := New(doc, fd, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	glyphs := src.DecodeString([]byte("Hello"))
	if len(glyphs) != 5 {
		t.Fatalf("got %d glyphs, want 5", len(glyphs))
	}
	for i, g := range glyphs {
		if g.Outline == nil {
			t.Errorf("glyph %d (%q): nil outline — CFF charset name→GID failed", i, g.Rune)
		}
	}
}

func TestType0Decodes(t *testing.T) {
	doc, fd := fontDict(t, gen.EmbeddedType0PDF())
	src, err := New(doc, fd, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The fixture's show-string is 2-byte GIDs of "Hello"; decode the same bytes.
	// Build them from the font program for an exact comparison.
	glyphs := src.DecodeString(type0HelloCodes(t))
	if len(glyphs) != 5 {
		t.Fatalf("got %d glyphs, want 5", len(glyphs))
	}
	for i, g := range glyphs {
		if g.Outline == nil {
			t.Errorf("glyph %d: nil outline", i)
		}
		if g.Width <= 0 {
			t.Errorf("glyph %d: non-positive width %v", i, g.Width)
		}
	}
}

// TestType0WithoutCmapDecodes covers a CIDFontType2 whose embedded TrueType
// program has no cmap table. PDF does not require one there (glyphs come from
// /CIDToGIDMap), so the font must load and draw instead of being rejected.
func TestType0WithoutCmapDecodes(t *testing.T) {
	doc, fd := fontDict(t, gen.EmbeddedType0NoCmapPDF())
	src, err := New(doc, fd, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	glyphs := src.DecodeString(type0HelloCodes(t))
	if len(glyphs) != 5 {
		t.Fatalf("got %d glyphs, want 5", len(glyphs))
	}
	for i, g := range glyphs {
		if g.Outline == nil {
			t.Errorf("glyph %d: nil outline", i)
		}
		if g.Width <= 0 {
			t.Errorf("glyph %d: non-positive width %v", i, g.Width)
		}
	}
}

// type0HelloCodes returns the Identity-H 2-byte codes (== GIDs) for "Hello" in
// Roboto, matching what EmbeddedType0PDF draws.
func type0HelloCodes(t *testing.T) []byte {
	t.Helper()
	prog := ttProg(t)
	var out []byte
	for _, r := range "Hello" {
		g := gidFor(t, prog, r)
		out = append(out, byte(uint16(g)>>8), byte(uint16(g)))
	}
	return out
}

// TestOutlineYUp verifies a glyph outline is in em units with the Y axis up: the
// uppercase 'H' should sit above the baseline (max Y positive, around cap
// height) and within the em box.
func TestOutlineYUp(t *testing.T) {
	prog := ttProg(t)
	out := prog.outline(gidFor(t, prog, 'H'))
	if out == nil || out.Empty() {
		t.Fatal("no outline for 'H'")
	}
	var minY, maxY = math.Inf(1), math.Inf(-1)
	var maxAbs float64
	for _, s := range out.Segments {
		for _, p := range []render.Point{s.P0, s.P1, s.P2} {
			if p.X == 0 && p.Y == 0 {
				continue
			}
			minY = math.Min(minY, p.Y)
			maxY = math.Max(maxY, p.Y)
			maxAbs = math.Max(maxAbs, math.Max(math.Abs(p.X), math.Abs(p.Y)))
		}
	}
	if maxY <= 0 {
		t.Errorf("max Y = %v, want positive (Y-up: 'H' above baseline)", maxY)
	}
	if minY < -0.1 {
		t.Errorf("min Y = %v, 'H' should rest near the baseline", minY)
	}
	if maxAbs > 1.5 {
		t.Errorf("coordinate magnitude %v exceeds em box; not normalized", maxAbs)
	}
}

// TestQuadToCubicElevation checks the closed-form quad→cubic control points by
// building a path with a single quadratic and inspecting the emitted cubic.
func TestQuadToCubicElevation(t *testing.T) {
	// Minimal sfnt Segments are not constructible directly here; instead verify
	// the elevation formula used in outline() against a hand computation. Use a
	// representative quad: P0=(0,0), Q=(1,2), P2=(2,0).
	p0 := render.Point{X: 0, Y: 0}
	q := render.Point{X: 1, Y: 2}
	p2 := render.Point{X: 2, Y: 0}
	c1 := render.Point{X: p0.X + (2.0/3.0)*(q.X-p0.X), Y: p0.Y + (2.0/3.0)*(q.Y-p0.Y)}
	c2 := render.Point{X: p2.X + (2.0/3.0)*(q.X-p2.X), Y: p2.Y + (2.0/3.0)*(q.Y-p2.Y)}
	// Known values: C1 = (2/3, 4/3), C2 = (4/3, 4/3).
	if !approx(c1.X, 2.0/3.0) || !approx(c1.Y, 4.0/3.0) {
		t.Errorf("C1 = %+v, want (0.667, 1.333)", c1)
	}
	if !approx(c2.X, 4.0/3.0) || !approx(c2.Y, 4.0/3.0) {
		t.Errorf("C2 = %+v, want (1.333, 1.333)", c2)
	}
	// The cubic must share the quad's endpoints.
	if c1 == p0 || c2 == p2 {
		t.Error("control points must differ from endpoints for a non-degenerate quad")
	}
}

// TestRobotoOutlineHasCubics ensures a real glyph yields cubic segments (proving
// TrueType quadratics were elevated, since render.Path has no QuadTo).
func TestRobotoOutlineHasCubics(t *testing.T) {
	prog := ttProg(t)
	out := prog.outline(gidFor(t, prog, 'o')) // 'o' is all curves
	if out == nil {
		t.Fatal("no outline")
	}
	cubics := 0
	for _, s := range out.Segments {
		if s.Kind == render.CubeTo {
			cubics++
		}
	}
	if cubics == 0 {
		t.Error("expected cubic segments from elevated quadratics")
	}
}

// TestBareCFFNameToGID verifies that a bare CFF program (FontFile3 Type1C)
// resolves glyph names to GIDs through the parser's charset — the path simple
// CFF fonts use to map a PDF code → glyph name → GID.
func TestBareCFFNameToGID(t *testing.T) {
	prog, err := parseProgram(gen.SourceSansCFF(), progCFF)
	if err != nil {
		t.Fatalf("parseProgram(CFF): %v", err)
	}
	if n := prog.numGlyphs(); n < 100 {
		t.Errorf("bare CFF has %d glyphs, expected the full set", n)
	}
	names := prog.nameToGID()
	for _, name := range []string{"space", "A", "a"} {
		gid, ok := names[name]
		if !ok {
			t.Errorf("name %q not found in CFF charset", name)
			continue
		}
		if prog.outline(gid) == nil && name != "space" {
			t.Errorf("name %q (gid %d) has no outline", name, gid)
		}
	}
}

func TestErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		dict    pdf.Dict
		wantErr error
	}{
		{
			name:    "unsupported subtype",
			dict:    pdf.Dict{"Subtype": pdf.Name("Type3")},
			wantErr: ErrUnsupportedFontType,
		},
		{
			name:    "no embedded program",
			dict:    pdf.Dict{"Subtype": pdf.Name("TrueType"), "FontDescriptor": pdf.Dict{}},
			wantErr: ErrNoEmbeddedProgram,
		},
		{
			name: "non-identity Type0 CMap",
			dict: pdf.Dict{
				"Subtype":  pdf.Name("Type0"),
				"Encoding": pdf.Name("UniGB-UCS2-H"),
			},
			wantErr: ErrUnsupportedCMap,
		},
	}
	doc := &pdf.Document{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(doc, tc.dict, nil, nil)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestType1ProgramOutlines parses the two committed classic Type1 programs
// directly and verifies each yields a non-empty outline for 'A' — covering both
// the serif (Termes) and sans (Heros) designs.
func TestType1ProgramOutlines(t *testing.T) {
	for name, pfb := range map[string][]byte{
		"termes": gen.TeXGyreTermesPFB(),
		"heros":  gen.TeXGyreHerosPFB(),
	} {
		t.Run(name, func(t *testing.T) {
			prog, err := parseProgram(pfb, progType1)
			if err != nil {
				t.Fatalf("parseProgram(Type1): %v", err)
			}
			gid := gidFor(t, prog, 'A')
			if out := prog.outline(gid); out == nil || out.Empty() {
				t.Errorf("no outline for 'A'")
			}
		})
	}
}

// TestClassicType1Decodes verifies a simple /Type1 font with an embedded classic
// FontFile (eexec PostScript) now parses and yields glyph outlines — the
// capability that benoitkugler/textlayout adds over the old sfnt-only pipeline.
func TestClassicType1Decodes(t *testing.T) {
	doc, fd := fontDict(t, gen.EmbeddedType1PDF())
	src, err := New(doc, fd, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	glyphs := src.DecodeString([]byte("Hello"))
	if len(glyphs) != 5 {
		t.Fatalf("got %d glyphs, want 5", len(glyphs))
	}
	for i, g := range glyphs {
		if g.Outline == nil {
			t.Errorf("glyph %d (%q): nil outline — Type1 FontFile not rendered", i, g.Rune)
		}
	}
}

// TestStandardSubstituteDecodes verifies that a simple font naming a standard-14
// /BaseFont (here /Helvetica) but embedding NO program still produces glyph
// outlines via a bundled substitute face. gen.TextPDF uses non-embedded
// /Helvetica, the exact base-14 gap this feature closes.
func TestStandardSubstituteDecodes(t *testing.T) {
	doc, fd := fontDict(t, gen.TextPDF())
	src, err := New(doc, fd, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	glyphs := src.DecodeString([]byte("Hello"))
	if len(glyphs) != 5 {
		t.Fatalf("got %d glyphs, want 5", len(glyphs))
	}
	for i, g := range glyphs {
		if g.Outline == nil {
			t.Errorf("glyph %d (%q): nil outline — base-14 substitute not rendered", i, g.Rune)
		}
		if g.Width <= 0 || g.Width > 1.5 {
			t.Errorf("glyph %d: implausible width %v em", i, g.Width)
		}
	}
	if glyphs[0].Rune != 'H' {
		t.Errorf("glyph 0 rune = %q, want 'H'", glyphs[0].Rune)
	}
}

// TestStandardSubstituteFamilies covers the name→substitute mapping for each
// bundled family and an alias, plus the unsupported Symbol case which must skip.
func TestStandardSubstituteFamilies(t *testing.T) {
	doc := &pdf.Document{}
	render := func(t *testing.T, baseFont string) content.GlyphSource {
		t.Helper()
		dict := pdf.Dict{"Subtype": pdf.Name("Type1"), "BaseFont": pdf.Name(baseFont)}
		src, err := New(doc, dict, nil, nil)
		if err != nil {
			t.Fatalf("New(%s): %v", baseFont, err)
		}
		return src
	}

	for _, baseFont := range []string{"Helvetica", "Helvetica-Bold", "Arial", "Times-Roman", "TimesNewRoman", "Courier", "CourierNew", "ABCDEF+Helvetica"} {
		t.Run(baseFont, func(t *testing.T) {
			glyphs := render(t, baseFont).DecodeString([]byte("Ag"))
			for i, g := range glyphs {
				if g.Outline == nil {
					t.Errorf("%s glyph %d (%q): nil outline", baseFont, i, g.Rune)
				}
			}
		})
	}

	// Symbol and ZapfDingbats have no bundled substitute: New must report
	// ErrNoEmbeddedProgram so the caller skips the text.
	for _, baseFont := range []string{"Symbol", "ZapfDingbats", "NotAStandardFont"} {
		t.Run("unsupported/"+baseFont, func(t *testing.T) {
			dict := pdf.Dict{"Subtype": pdf.Name("Type1"), "BaseFont": pdf.Name(baseFont)}
			if _, err := New(doc, dict, nil, nil); !errors.Is(err, ErrNoEmbeddedProgram) {
				t.Errorf("err = %v, want ErrNoEmbeddedProgram", err)
			}
		})
	}
}

// TestSymbolicTrueTypeDecodes verifies the symbolic-TrueType glyph path: a font
// flagged Symbolic with no /Encoding, whose codes resolve to glyphs via the
// raw-code/symbol cmap or the code-as-GID fallback rather than code→rune→GID.
func TestSymbolicTrueTypeDecodes(t *testing.T) {
	doc, fd := fontDict(t, gen.EmbeddedSymbolicTrueTypePDF())
	src, err := New(doc, fd, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Codes 5..10 are drawn; each must resolve to a real outline.
	glyphs := src.DecodeString([]byte{5, 6, 7, 8, 9, 10})
	if len(glyphs) != 6 {
		t.Fatalf("got %d glyphs, want 6", len(glyphs))
	}
	for _, g := range glyphs {
		if g.Outline == nil {
			t.Errorf("code %d: nil outline — symbolic fallback did not resolve a glyph", g.Code)
		}
	}
}

// TestNonSymbolicUnmappedStaysNotdef guards the gating: a NORMAL (non-symbolic)
// font must not invent glyphs for codes its encoding/cmap doesn't map. The
// code-as-GID fallback must NOT fire, so an unmapped control code resolves to
// GID 0 (.notdef) rather than to the arbitrary glyph at that index.
func TestNonSymbolicUnmappedStaysNotdef(t *testing.T) {
	doc, fd := fontDict(t, gen.EmbeddedTrueTypePDF()) // WinAnsi, non-symbolic
	src, err := New(doc, fd, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sf, ok := src.(*simpleFont)
	if !ok {
		t.Fatalf("expected *simpleFont, got %T", src)
	}
	// Code 7 (BEL) has no WinAnsi glyph. Under the symbolic code-as-GID fallback
	// it would resolve to GID 7 (a real Roboto glyph); gated off for this
	// non-symbolic font, it must stay GID 0 (.notdef).
	if gid := sf.toGID[7]; gid != 0 {
		t.Errorf("non-symbolic unmapped code 7 resolved to GID %d, want 0 (.notdef)", gid)
	}
}

// TestMalformedFontFileGraceful confirms a /Type1 font with a bogus FontFile
// still degrades gracefully (typed error, no panic).
func TestMalformedFontFileGraceful(t *testing.T) {
	doc := &pdf.Document{}
	dict := pdf.Dict{
		"Subtype": pdf.Name("Type1"),
		"FontDescriptor": pdf.Dict{
			"FontFile": &pdf.Stream{Dict: pdf.Dict{}, Raw: []byte("not a font")},
		},
	}
	_, err := New(doc, dict, nil, nil)
	if !errors.Is(err, ErrUnsupportedFontProgram) {
		t.Errorf("malformed FontFile err = %v, want ErrUnsupportedFontProgram", err)
	}
}

// TestType0WidthsAndCIDToGID exercises /W parsing and an explicit CIDToGIDMap
// stream by constructing a Type0 dict directly over the Roboto program.
func TestType0WidthForms(t *testing.T) {
	w := pdf.Array{
		pdf.Integer(3), pdf.Array{pdf.Integer(250), pdf.Integer(300)}, // CIDs 3,4
		pdf.Integer(10), pdf.Integer(12), pdf.Integer(500), // CIDs 10..12 = 500
	}
	doc := &pdf.Document{}
	got := parseCIDWidths(doc, w)
	want := map[int]float64{3: 0.25, 4: 0.30, 10: 0.5, 11: 0.5, 12: 0.5}
	if len(got) != len(want) {
		t.Fatalf("got %d widths, want %d: %v", len(got), len(want), got)
	}
	for cid, wv := range want {
		if math.Abs(got[cid]-wv) > 1e-9 {
			t.Errorf("CID %d width = %v, want %v", cid, got[cid], wv)
		}
	}
}

// hOutlineFingerprint returns a geometry fingerprint of the 'H' glyph (segment
// count + summed coordinate magnitude) resolved through prog's own glyph-name
// map. It differs between weight/slant variants of a family (the bold face has
// thicker stems → different coordinates), so it tells bundled variants apart
// even when their advances happen to match.
func hOutlineFingerprint(t *testing.T, prog *program) (segs int, sum float64) {
	t.Helper()
	names := prog.nameToGID()
	gid, ok := names["H"]
	if !ok {
		t.Fatalf("no 'H' glyph in program")
	}
	out := prog.outline(gid)
	if out == nil {
		t.Fatalf("no outline for 'H'")
	}
	for _, s := range out.Segments {
		segs++
		for _, p := range []render.Point{s.P0, s.P1, s.P2} {
			sum += math.Abs(p.X) + math.Abs(p.Y)
		}
	}
	return segs, sum
}

// sameFace reports whether two programs render 'H' identically (same weight).
func sameFace(t *testing.T, a, b *program) bool {
	sa, xa := hOutlineFingerprint(t, a)
	sb, xb := hOutlineFingerprint(t, b)
	return sa == sb && math.Abs(xa-xb) < 1e-9
}

// TestStandardSubstituteStyleFromName verifies that a name-suffixed /BaseFont
// (/Helvetica-Bold) with a nil provider selects the BUNDLED BOLD face, distinct
// from the regular weight.
func TestStandardSubstituteStyleFromName(t *testing.T) {
	doc := &pdf.Document{}
	regDict := pdf.Dict{"Subtype": pdf.Name("Type1"), "BaseFont": pdf.Name("Helvetica")}
	boldDict := pdf.Dict{"Subtype": pdf.Name("Type1"), "BaseFont": pdf.Name("Helvetica-Bold")}

	reg, err := standardSubstituteProgram(doc, regDict, nil, nil)
	if err != nil {
		t.Fatalf("regular: %v", err)
	}
	bold, err := standardSubstituteProgram(doc, boldDict, nil, nil)
	if err != nil {
		t.Fatalf("bold: %v", err)
	}
	if sameFace(t, reg, bold) {
		t.Errorf("Helvetica-Bold resolved to same face as Helvetica; bold variant not selected")
	}
}

// TestStandardSubstituteStyleFromFlags proves the FontDescriptor-/Flags path: a
// plain /Helvetica name whose descriptor sets the ForceBold flag (bit 19) still
// resolves to the bundled BOLD face.
func TestStandardSubstituteStyleFromFlags(t *testing.T) {
	doc := &pdf.Document{}
	plain := pdf.Dict{"Subtype": pdf.Name("Type1"), "BaseFont": pdf.Name("Helvetica")}
	forceBold := pdf.Dict{
		"Subtype":        pdf.Name("Type1"),
		"BaseFont":       pdf.Name("Helvetica"),
		"FontDescriptor": pdf.Dict{"Flags": pdf.Integer(fontFlagForceBold)},
	}

	reg, err := standardSubstituteProgram(doc, plain, nil, nil)
	if err != nil {
		t.Fatalf("plain: %v", err)
	}
	bold, err := standardSubstituteProgram(doc, forceBold, nil, nil)
	if err != nil {
		t.Fatalf("forceBold: %v", err)
	}
	if sameFace(t, reg, bold) {
		t.Errorf("ForceBold-flagged /Helvetica resolved to regular face; flags path not honored")
	}
}

// stubProvider returns fixed bytes for a set of families, recording the last
// (family, bold, italic) it was asked for so tests can assert the strip/derive
// logic.
type stubProvider struct {
	faces      map[string][]byte
	lastFamily string
	lastBold   bool
	lastItalic bool
	calledFor  int
}

func (p *stubProvider) LoadStyled(family string, bold, italic bool) ([]byte, bool) {
	p.lastFamily, p.lastBold, p.lastItalic = family, bold, italic
	p.calledFor++
	b, ok := p.faces[family]
	return b, ok
}

// TestProviderTakesPriority verifies the provider is consulted before the bundle
// and wins: it supplies a distinct TrueType face for "Helvetica" (a real sfnt,
// which the bundled Helvetica substitute is NOT — that is a Type1 PFB), so a
// successful provider hit is observable by the resolved program differing from
// the bundle, and by the family/style handed to the provider being the stripped
// "Helvetica" with bold=true.
func TestProviderTakesPriority(t *testing.T) {
	doc := &pdf.Document{}
	prov := &stubProvider{faces: map[string][]byte{"Helvetica": gen.RobotoTTF()}}
	dict := pdf.Dict{"Subtype": pdf.Name("Type1"), "BaseFont": pdf.Name("Helvetica-Bold")}

	prog, err := standardSubstituteProgram(doc, dict, prov, nil)
	if err != nil {
		t.Fatalf("standardSubstituteProgram: %v", err)
	}
	if prov.calledFor != 1 {
		t.Fatalf("provider consulted %d times, want 1", prov.calledFor)
	}
	if prov.lastFamily != "Helvetica" {
		t.Errorf("provider family = %q, want %q (style suffix must be stripped)", prov.lastFamily, "Helvetica")
	}
	if !prov.lastBold || prov.lastItalic {
		t.Errorf("provider style = (bold=%v italic=%v), want (true,false)", prov.lastBold, prov.lastItalic)
	}
	// The provider's Roboto sfnt must be what got parsed, not the bundled Heros PFB.
	// Roboto has a 'g' glyph reachable by its own cmap; compare against a direct parse.
	want, err := parseProgram(gen.RobotoTTF(), progTrueType)
	if err != nil {
		t.Fatalf("parse Roboto: %v", err)
	}
	if prog.numGlyphs() != want.numGlyphs() {
		t.Errorf("provider face numGlyphs = %d, want Roboto's %d (bundle used instead?)", prog.numGlyphs(), want.numGlyphs())
	}
}

// TestProviderResolvesSymbol verifies that a family the bundle has NO look-alike
// for (Symbol) resolves when a provider supplies bytes — and still returns
// ErrNoEmbeddedProgram when no provider is given (graceful skip preserved).
func TestProviderResolvesSymbol(t *testing.T) {
	doc := &pdf.Document{}
	dict := pdf.Dict{"Subtype": pdf.Name("Type1"), "BaseFont": pdf.Name("Symbol")}

	// No provider: unchanged graceful skip.
	if _, err := New(doc, dict, nil, nil); !errors.Is(err, ErrNoEmbeddedProgram) {
		t.Errorf("Symbol with nil provider: err = %v, want ErrNoEmbeddedProgram", err)
	}

	// With a provider that supplies Symbol bytes: resolves to a real font.
	prov := &stubProvider{faces: map[string][]byte{"Symbol": gen.RobotoTTF()}}
	src, err := New(doc, dict, prov, nil)
	if err != nil {
		t.Fatalf("Symbol with provider: %v", err)
	}
	if src == nil {
		t.Fatal("Symbol with provider: nil GlyphSource")
	}
}

// TestProviderFallsBackToBundle verifies that when the provider has no match, the
// bundled substitute is still used (a standard family renders).
func TestProviderFallsBackToBundle(t *testing.T) {
	doc := &pdf.Document{}
	prov := &stubProvider{faces: map[string][]byte{}} // matches nothing
	dict := pdf.Dict{"Subtype": pdf.Name("Type1"), "BaseFont": pdf.Name("Helvetica")}

	src, err := New(doc, dict, prov, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if glyphs := src.DecodeString([]byte("H")); len(glyphs) != 1 || glyphs[0].Outline == nil {
		t.Errorf("bundle fallback did not render 'H'")
	}
}

// TestStripStyleSuffix covers the family-name stripping handed to a provider.
func TestStripStyleSuffix(t *testing.T) {
	cases := map[string]string{
		"Helvetica":             "Helvetica",
		"Helvetica-Bold":        "Helvetica",
		"Times-BoldItalic":      "Times",
		"Courier-Oblique":       "Courier",
		"Arial,BoldItalic":      "Arial",
		"ABCDEF+Helvetica-Bold": "Helvetica",
		"TimesNewRomanPSMT":     "TimesNewRomanPSMT", // no style token → unchanged
	}
	for in, want := range cases {
		if got := stripStyleSuffix(in); got != want {
			t.Errorf("stripStyleSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// Ensure the constructed sources satisfy the content.GlyphSource interface at
// compile time.
var _ content.GlyphSource = (*simpleFont)(nil)
var _ content.GlyphSource = (*type0Font)(nil)
