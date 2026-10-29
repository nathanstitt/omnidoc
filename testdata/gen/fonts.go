package gen

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"fmt"

	"github.com/benoitkugler/textlayout/fonts/type1"

	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Embedded test fonts. These are real, permissively-licensed fonts used only by
// the test corpus (never shipped in any binary). Roboto (Apache-2.0) is a
// TrueType (glyf) program; Source Sans 3 (SIL OFL 1.1) is OpenType/CFF. See the
// LICENSE files alongside them under fonts/.

//go:embed fonts/Roboto-Regular.ttf
var robotoTTF []byte

//go:embed fonts/SourceSans3-Regular.otf
var sourceSansOTF []byte

// TeX Gyre Termes and Heros (GUST Font License, LPPL-equivalent) are classic
// Type 1 (PostScript) programs in PFB form, embedded via /FontFile to exercise
// the Type1 glyph path. Termes is a serif (Times-like), Heros a sans
// (Helvetica-like) — two distinct designs from one foundry so the Type1
// charstring parser sees varied outlines. See fonts/README-type1.md.
//
//go:embed fonts/TeXGyreTermes-Regular.pfb
var texGyreTermesPFB []byte

//go:embed fonts/TeXGyreHeros-Regular.pfb
var texGyreHerosPFB []byte

// TeXGyreTermesPFB and TeXGyreHerosPFB expose the embedded classic Type 1
// programs to tests in other packages (notably pkg/internal/font).
func TeXGyreTermesPFB() []byte { return texGyreTermesPFB }
func TeXGyreHerosPFB() []byte  { return texGyreHerosPFB }

// EmbeddedSymbolicTrueTypePDF returns a single-page PDF using a symbolic embedded
// /TrueType font (Roboto, /Flags Symbolic) with NO /Encoding, drawing a string of
// low byte codes that map to glyphs by index. This reproduces the real-world
// subsetted-symbolic case (e.g. LibreOffice exports) where code→rune→GID fails
// and the renderer must fall back to the raw-code/symbol cmap or code-as-GID.
func EmbeddedSymbolicTrueTypePDF() []byte {
	b := newBuilder()

	fontFile := b.addStream(fmt.Sprintf(" /Length1 %d", len(robotoTTF)), robotoTTF)
	descNum := b.addObject(fmt.Sprintf(
		// /Flags 4 = Symbolic (no /Encoding implied).
		"<< /Type /FontDescriptor /FontName /Roboto-Regular /Flags 4 "+
			"/FontBBox [-681 -271 1535 1061] /ItalicAngle 0 /Ascent 1061 /Descent -271 "+
			"/CapHeight 711 /StemV 80 /MissingWidth 500 /FontFile2 %d 0 R >>", fontFile))

	// Draw codes 5..10 (which equal GIDs under the code-as-GID fallback and point
	// at glyphs with real outlines in Roboto); give each a nominal width. No
	// /Encoding is present, marking this symbolic.
	first := 5
	last := 10
	widths := make([]int, last-first+1)
	for i := range widths {
		widths[i] = 600
	}
	fontNum := b.addObject(fmt.Sprintf(
		"<< /Type /Font /Subtype /TrueType /BaseFont /Roboto-Regular "+
			"/FirstChar %d /LastChar %d /Widths %s /FontDescriptor %d 0 R >>",
		first, last, widthsArray(widths), descNum))

	var codes []byte
	for c := first; c <= last; c++ {
		codes = append(codes, byte(c))
	}
	content := []byte(fmt.Sprintf("BT /F1 48 Tf 72 680 Td (%s) Tj ET", codes))
	return finishFontPage(b, fontNum, content)
}

// EmbeddedTrueTypePDF returns a single-page PDF using a simple /TrueType font
// with an embedded FontFile2 (Roboto) and WinAnsi encoding, drawing a short
// string. It exercises the embedded-TrueType glyph path end to end.
func EmbeddedTrueTypePDF() []byte {
	const text = "Hello"
	b := newBuilder()

	fontFile := b.addStream(fmt.Sprintf(" /Length1 %d", len(robotoTTF)), robotoTTF)
	descNum := b.addObject(fmt.Sprintf(
		"<< /Type /FontDescriptor /FontName /Roboto-Regular /Flags 32 "+
			"/FontBBox [-681 -271 1535 1061] /ItalicAngle 0 /Ascent 1061 /Descent -271 "+
			"/CapHeight 711 /StemV 80 /MissingWidth 500 /FontFile2 %d 0 R >>", fontFile))

	first, widths := simpleWidths(robotoTTF, text)
	fontNum := b.addObject(fmt.Sprintf(
		"<< /Type /Font /Subtype /TrueType /BaseFont /Roboto-Regular "+
			"/FirstChar %d /LastChar %d /Widths %s /Encoding /WinAnsiEncoding "+
			"/FontDescriptor %d 0 R >>",
		first, first+len(widths)-1, widthsArray(widths), descNum))

	content := []byte(fmt.Sprintf("BT /F1 48 Tf 72 680 Td (%s) Tj ET", text))
	return finishFontPage(b, fontNum, content)
}

// EmbeddedType1PDF returns a single-page PDF using a simple /Type1 font with an
// embedded classic FontFile (PostScript Type 1, eexec) — TeX Gyre Termes — and
// WinAnsi encoding. It exercises the classic-Type1 glyph path end to end.
func EmbeddedType1PDF() []byte {
	const text = "Hello"
	raw, l1, l2, l3 := pfbToRaw(texGyreTermesPFB)
	b := newBuilder()

	fontFile := b.addStream(
		fmt.Sprintf(" /Length1 %d /Length2 %d /Length3 %d", l1, l2, l3), raw)
	descNum := b.addObject(fmt.Sprintf(
		"<< /Type /FontDescriptor /FontName /NimbusRoman-Regular /Flags 34 "+
			"/FontBBox [-168 -281 1000 924] /ItalicAngle 0 /Ascent 683 /Descent -217 "+
			"/CapHeight 662 /StemV 84 /MissingWidth 500 /FontFile %d 0 R >>", fontFile))

	// Real per-glyph advances from the Type 1 program, so the rendered string is
	// correctly spaced (covers the WinAnsi codes from 'H' to 'o').
	first := int('H')
	widths := type1Widths(texGyreTermesPFB, first, int('o'))
	fontNum := b.addObject(fmt.Sprintf(
		"<< /Type /Font /Subtype /Type1 /BaseFont /NimbusRoman-Regular "+
			"/FirstChar %d /LastChar %d /Widths %s /Encoding /WinAnsiEncoding "+
			"/FontDescriptor %d 0 R >>",
		first, first+len(widths)-1, widthsArray(widths), descNum))

	content := []byte(fmt.Sprintf("BT /F1 48 Tf 72 680 Td (%s) Tj ET", text))
	return finishFontPage(b, fontNum, content)
}

// type1Widths returns the /Widths array (1000-unit glyph space) for codes
// first..last of a classic Type 1 program, read via the textlayout parser.
// Unresolvable codes default to 500.
func type1Widths(pfb []byte, first, last int) []int {
	f, err := type1.Parse(bytes.NewReader(pfb))
	if err != nil {
		panic("gen: parsing Type1 font: " + err.Error())
	}
	widths := make([]int, last-first+1)
	for c := first; c <= last; c++ {
		gid, ok := f.NominalGlyph(rune(c))
		if !ok {
			widths[c-first] = 500
			continue
		}
		widths[c-first] = int(f.HorizontalAdvance(gid))
	}
	return widths
}

// pfbToRaw converts a PFB (segmented) Type 1 font into the raw concatenated
// program bytes a PDF /FontFile stream expects, plus the segment lengths
// Length1 (clear-text ASCII), Length2 (binary eexec), Length3 (trailing ASCII).
// PFB segments each start with 0x80, a type byte (1=ASCII, 2=binary, 3=EOF), and
// a 4-byte little-endian length.
func pfbToRaw(pfb []byte) (raw []byte, l1, l2, l3 int) {
	var out bytes.Buffer
	lens := []int{} // length of each ASCII/binary segment in order
	types := []byte{}
	pos := 0
	for pos+6 <= len(pfb) {
		if pfb[pos] != 0x80 {
			break
		}
		segType := pfb[pos+1]
		if segType == 3 { // EOF marker
			break
		}
		n := int(binary.LittleEndian.Uint32(pfb[pos+2 : pos+6]))
		pos += 6
		if pos+n > len(pfb) {
			n = len(pfb) - pos
		}
		out.Write(pfb[pos : pos+n])
		lens = append(lens, n)
		types = append(types, segType)
		pos += n
	}
	// Type 1 fonts are ASCII (clear) + binary (eexec) + ASCII (trailer). Sum the
	// segments by type in order: first ASCII run -> Length1, binary -> Length2,
	// trailing ASCII -> Length3.
	for i, t := range types {
		switch {
		case t == 1 && l2 == 0:
			l1 += lens[i] // leading clear-text
		case t == 2:
			l2 += lens[i] // eexec binary
		default:
			l3 += lens[i] // trailing clear-text
		}
	}
	return out.Bytes(), l1, l2, l3
}

// EmbeddedType0PDF returns a single-page PDF using a Type0 composite font with
// Identity-H encoding and a CIDFontType2 descendant embedding Roboto
// (FontFile2), with CIDToGIDMap /Identity. The show-string is the 2-byte GIDs of
// a few letters, so it exercises the composite glyph path without a cmap step.
func EmbeddedType0PDF() []byte { return embeddedType0PDF(robotoTTF) }

// EmbeddedType0NoCmapPDF is EmbeddedType0PDF with an embedded program that has
// no cmap table, as many PDF producers write for CIDFontType2 fonts (PDF
// 32000-1 §9.9 does not require one there).
func EmbeddedType0NoCmapPDF() []byte { return embeddedType0PDF(withoutCmap(robotoTTF)) }

// withoutCmap returns a copy of an sfnt whose cmap directory entry is renamed
// to an unknown tag, so parsers no longer see a cmap. Table offsets and every
// other table stay the same.
func withoutCmap(ttf []byte) []byte {
	out := append([]byte(nil), ttf...)
	n := int(binary.BigEndian.Uint16(out[4:]))
	for i := 0; i < n; i++ {
		rec := out[12+16*i:]
		if string(rec[0:4]) == "cmap" {
			copy(rec[0:4], "xmap")
			return out
		}
	}
	panic("gen: no cmap table in TTF")
}

// embeddedType0PDF builds the EmbeddedType0PDF page around the given embedded
// program. GIDs and widths always come from the full Roboto program.
func embeddedType0PDF(program []byte) []byte {
	b := newBuilder()

	fontFile := b.addStream(fmt.Sprintf(" /Length1 %d", len(program)), program)
	descNum := b.addObject(fmt.Sprintf(
		"<< /Type /FontDescriptor /FontName /Roboto-Regular /Flags 32 "+
			"/FontBBox [-681 -271 1535 1061] /ItalicAngle 0 /Ascent 1061 /Descent -271 "+
			"/CapHeight 711 /StemV 80 /FontFile2 %d 0 R >>", fontFile))

	// CIDFontType2 descendant. DW + a simple W run covering the GIDs we draw.
	gids := runeGIDs(robotoTTF, []rune("Hello"))
	cidFontNum := b.addObject(fmt.Sprintf(
		"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /Roboto-Regular "+
			"/CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> "+
			"/FontDescriptor %d 0 R /CIDToGIDMap /Identity /DW 1000 /W %s >>",
		descNum, cidWidthsArray(robotoTTF, gids)))

	fontNum := b.addObject(fmt.Sprintf(
		"<< /Type /Font /Subtype /Type0 /BaseFont /Roboto-Regular "+
			"/Encoding /Identity-H /DescendantFonts [ %d 0 R ] >>", cidFontNum))

	content := []byte(fmt.Sprintf("BT /F1 48 Tf 72 680 Td <%s> Tj ET", hexGIDs(gids)))
	return finishFontPage(b, fontNum, content)
}

// EmbeddedCFFPDF returns a single-page PDF using a simple /Type1 font with an
// embedded FontFile3 (/Subtype Type1C) — the bare CFF table extracted from
// Source Sans 3 — and WinAnsi encoding. It exercises the bare-CFF wrapper and
// the CFF charset (name→GID) resolution path.
func EmbeddedCFFPDF() []byte {
	const text = "Hello"
	cff := extractCFFTable(sourceSansOTF)
	b := newBuilder()

	fontFile := b.addStream(fmt.Sprintf(" /Subtype /Type1C /Length1 %d", len(cff)), cff)
	descNum := b.addObject(fmt.Sprintf(
		"<< /Type /FontDescriptor /FontName /SourceSans3-Regular /Flags 32 "+
			"/FontBBox [-231 -384 1142 1193] /ItalicAngle 0 /Ascent 1193 /Descent -384 "+
			"/CapHeight 700 /StemV 80 /MissingWidth 500 /FontFile3 %d 0 R >>", fontFile))

	// Metrics come from the full OTF (sfnt parses OTTO/CFF directly); the GIDs
	// match the bare CFF table embedded above.
	first, widths := simpleWidths(sourceSansOTF, text)
	fontNum := b.addObject(fmt.Sprintf(
		"<< /Type /Font /Subtype /Type1 /BaseFont /SourceSans3-Regular "+
			"/FirstChar %d /LastChar %d /Widths %s /Encoding /WinAnsiEncoding "+
			"/FontDescriptor %d 0 R >>",
		first, first+len(widths)-1, widthsArray(widths), descNum))

	content := []byte(fmt.Sprintf("BT /F1 48 Tf 72 680 Td (%s) Tj ET", text))
	return finishFontPage(b, fontNum, content)
}

// finishFontPage assembles the page, pages, and catalog objects for a font
// fixture whose font object is fontNum, with the given content stream, and
// returns the serialized PDF.
func finishFontPage(b *builder, fontNum int, content []byte) []byte {
	contentNum := b.addStream("", content)
	pageNum := len(b.offsets)
	pagesNum := pageNum + 1
	page := b.addObject(fmt.Sprintf(
		"<< /Type /Page /Parent %d 0 R /MediaBox [0 0 612 792] "+
			"/Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
		pagesNum, fontNum, contentNum))
	if page != pageNum {
		panic("gen: page object number mismatch in finishFontPage")
	}
	pages := b.addObject(fmt.Sprintf("<< /Type /Pages /Kids [ %d 0 R ] /Count 1 >>", page))
	if pages != pagesNum {
		panic("gen: pages object number mismatch in finishFontPage")
	}
	catalog := b.addObject(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	return b.finish(catalog)
}

// --- helpers that parse the embedded fonts to build correct dictionaries ---

// mustParse parses font bytes (TrueType or wrapped CFF) for metrics extraction.
func mustParseSFNT(data []byte) *sfnt.Font {
	f, err := sfnt.Parse(data)
	if err != nil {
		panic("gen: parsing embedded font: " + err.Error())
	}
	return f
}

// runeGIDs maps runes to GIDs via a TrueType font's cmap.
func runeGIDs(ttf []byte, runes []rune) []sfnt.GlyphIndex {
	f := mustParseSFNT(ttf)
	var b sfnt.Buffer
	gids := make([]sfnt.GlyphIndex, len(runes))
	for i, r := range runes {
		g, err := f.GlyphIndex(&b, r)
		if err != nil {
			panic("gen: glyph index: " + err.Error())
		}
		gids[i] = g
	}
	return gids
}

// hexGIDs encodes GIDs as a big-endian 2-byte-per-glyph hex string for a PDF
// show-string under Identity-H.
func hexGIDs(gids []sfnt.GlyphIndex) string {
	var sb bytes.Buffer
	for _, g := range gids {
		fmt.Fprintf(&sb, "%04X", uint16(g))
	}
	return sb.String()
}

// emWidth returns a glyph's advance in 1000-unit glyph space from a font.
func emWidth(f *sfnt.Font, gid sfnt.GlyphIndex) int {
	var b sfnt.Buffer
	ppem := fixed.I(int(f.UnitsPerEm()))
	adv, err := f.GlyphAdvance(&b, gid, ppem, 0)
	if err != nil {
		return 500
	}
	return int(float64(adv) / 64.0 / float64(f.UnitsPerEm()) * 1000)
}

// simpleWidths computes the /FirstChar and /Widths (1000-unit) for the WinAnsi
// codes of text, reading advances from the given SFNT program (TrueType or a
// full OpenType/CFF container).
func simpleWidths(program []byte, text string) (first int, widths []int) {
	f := mustParseSFNT(program)
	var b sfnt.Buffer

	lo, hi := 0xFF, 0x00
	for _, r := range text {
		c := int(r) // ASCII text: code == rune for the Latin range used here
		if c < lo {
			lo = c
		}
		if c > hi {
			hi = c
		}
	}
	widths = make([]int, hi-lo+1)
	for c := lo; c <= hi; c++ {
		g, err := f.GlyphIndex(&b, rune(c))
		if err != nil || g == 0 {
			widths[c-lo] = 500
			continue
		}
		widths[c-lo] = emWidth(f, g)
	}
	return lo, widths
}

// widthsArray formats an int slice as a PDF array literal.
func widthsArray(w []int) string {
	var sb bytes.Buffer
	sb.WriteByte('[')
	for i, v := range w {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%d", v)
	}
	sb.WriteByte(']')
	return sb.String()
}

// cidWidthsArray builds a /W array giving each drawn GID its advance, in the
// "c [w]" form (one entry per CID).
func cidWidthsArray(ttf []byte, gids []sfnt.GlyphIndex) string {
	f := mustParseSFNT(ttf)
	var sb bytes.Buffer
	sb.WriteByte('[')
	for i, g := range gids {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%d [%d]", uint16(g), emWidth(f, g))
	}
	sb.WriteByte(']')
	return sb.String()
}

// extractCFFTable returns the raw bytes of the "CFF " table from an OpenType
// (OTTO) font.
func extractCFFTable(otf []byte) []byte {
	if len(otf) < 12 {
		panic("gen: OTF too short")
	}
	n := int(binary.BigEndian.Uint16(otf[4:]))
	for i := 0; i < n; i++ {
		rec := otf[12+16*i:]
		if string(rec[0:4]) == "CFF " {
			o := binary.BigEndian.Uint32(rec[8:])
			l := binary.BigEndian.Uint32(rec[12:])
			return otf[o : o+l]
		}
	}
	panic("gen: no CFF table in OTF")
}
