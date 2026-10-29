// Package font turns embedded PDF font programs into glyph outlines. It parses
// the supported font dictionaries (simple TrueType, CFF, and classic Type1, plus
// Type0 composite fonts), reads the embedded FontFile/FontFile2/FontFile3
// programs with github.com/benoitkugler/textlayout, and exposes them through the
// content.GlyphSource interface so the content interpreter can draw text without
// knowing any font-format details.
//
// Unsupported fonts (non-embedded base-14, non-Identity Type0 CMaps, Type3)
// return a typed error; callers degrade gracefully by drawing nothing while still
// advancing the text cursor.
package font

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/nathanstitt/omnidoc/pkg/internal/content"
	"github.com/nathanstitt/omnidoc/pkg/internal/font/standard"
	"github.com/nathanstitt/omnidoc/pkg/pdf"
)

// PDF FontDescriptor /Flags bits (PDF 32000-1 Table 121) used to derive a
// non-embedded font's intended style when the /BaseFont name lacks a suffix.
const (
	fontFlagItalic    = 1 << 6  // bit 7 (value 64): the font is italic/oblique
	fontFlagForceBold = 1 << 18 // bit 19 (value 262144): the font is forced bold
)

// New builds a content.GlyphSource from a resolved PDF font dictionary. It
// branches on /Subtype. It returns a typed error for unsupported or malformed
// fonts (see the package's sentinel errors); the rasterizer logs and falls back
// to drawing nothing.
//
// provider, when non-nil, is consulted first for a non-embedded standard-14
// /BaseFont: the toolkit asks it for the resolved family + weight/slant, and
// uses those raw program bytes if it has a match; otherwise it falls back to the
// bundled substitute. A nil provider means "bundled substitutes only" (the
// historical behavior).
func New(doc *pdf.Document, fontDict pdf.Dict, provider Provider, logf func(string, ...any)) (content.GlyphSource, error) {
	subtype, _ := doc.GetName(fontDict["Subtype"])
	switch subtype {
	case "TrueType", "Type1", "MMType1":
		prog, err := embeddedSimpleProgram(doc, fontDict)
		if errors.Is(err, ErrNoEmbeddedProgram) {
			// No embedded program: resolve a substitute face. A caller-supplied
			// provider is consulted first, then the bundled permissively-licensed
			// look-alike, so the text still renders. Otherwise propagate the error
			// and the caller skips.
			prog, err = standardSubstituteProgram(doc, fontDict, provider, logf)
		}
		if err != nil {
			return nil, err
		}
		return newSimpleFont(doc, fontDict, prog)
	case "Type0":
		return newType0Font(doc, fontDict)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedFontType, string(subtype))
	}
}

// embeddedSimpleProgram parses the embedded program for a simple font from the
// FontDescriptor: FontFile2 (TrueType), FontFile3 (bare CFF/Type1C or OpenType),
// or the classic FontFile (Type1, eexec).
func embeddedSimpleProgram(doc *pdf.Document, fontDict pdf.Dict) (*program, error) {
	desc := doc.GetDict(fontDict["FontDescriptor"])
	if desc == nil {
		return nil, ErrNoEmbeddedProgram
	}
	if s := doc.GetStream(desc["FontFile2"]); s != nil {
		b, _, derr := doc.DecodedStream(s)
		if derr != nil {
			return nil, fmt.Errorf("%w: FontFile2: %v", ErrUnsupportedFontProgram, derr)
		}
		return parseProgram(withCmap(b), progTrueType)
	}
	if s := doc.GetStream(desc["FontFile3"]); s != nil {
		b, _, derr := doc.DecodedStream(s)
		if derr != nil {
			return nil, fmt.Errorf("%w: FontFile3: %v", ErrUnsupportedFontProgram, derr)
		}
		sub, _ := doc.GetName(s.Dict["Subtype"])
		if sub == "OpenType" {
			return parseProgram(withCmap(b), progTrueType)
		}
		return parseProgram(b, progCFF)
	}
	if s := doc.GetStream(desc["FontFile"]); s != nil {
		b, _, derr := doc.DecodedStream(s)
		if derr != nil {
			return nil, fmt.Errorf("%w: FontFile: %v", ErrUnsupportedFontProgram, derr)
		}
		return parseProgram(b, progType1)
	}
	return nil, ErrNoEmbeddedProgram
}

// standardSubstituteProgram parses a bundled substitute font for a simple font
// whose /BaseFont names a standard-14 font (or a common alias such as Arial or
// TimesNewRoman) but which embeds no program. It returns ErrNoEmbeddedProgram
// when no substitute is bundled for the base font (e.g. Symbol/ZapfDingbats, or a
// non-standard name), so the caller degrades gracefully. The resulting *program
// flows through the normal simpleFont path; the PDF's own /Widths are preferred
// when present, otherwise the substitute font's own advances approximate them.
func standardSubstituteProgram(doc *pdf.Document, fontDict pdf.Dict, provider Provider, logf func(string, ...any)) (*program, error) {
	baseFont, _ := doc.GetName(fontDict["BaseFont"])
	name := string(baseFont)

	// Desired weight/slant, derived from BOTH the /BaseFont name suffix
	// ("Helvetica-Bold") AND the FontDescriptor /Flags, so a descriptor-only bold
	// with a plain name ("/Helvetica" + ForceBold) still resolves to the bold face.
	nameBold, nameItalic := styleFromBaseFont(name)
	flagBold, flagItalic := styleFromFlags(doc, fontDict)
	bold := nameBold || flagBold
	italic := nameItalic || flagItalic

	// The family passed to a provider must not carry the style suffix, else a disk
	// provider probing "Family-Bold" would probe "Helvetica-Bold-Bold". Strip the
	// suffix and pass the derived bold/italic separately.
	family := stripStyleSuffix(name)

	// A caller-supplied provider takes priority over the bundle. It may return any
	// supported program format (sfnt/TrueType, bare CFF, or classic Type1/PFB); the
	// bytes are sniffed to pick the parser.
	if provider != nil {
		if data, ok := provider.LoadStyled(family, bold, italic); ok {
			prog, err := parseProviderProgram(data)
			if err != nil {
				if logf != nil {
					logf("font: provider face for %q (bold=%v italic=%v) failed to parse: %v; falling back to bundle", family, bold, italic, err)
				}
			} else {
				if logf != nil {
					logf("font: using provider face for non-embedded base font %q (family %q bold=%v italic=%v)", name, family, bold, italic)
				}
				return prog, nil
			}
		}
	}

	sub, ok := standard.LookupStyled(name, bold, italic)
	if !ok {
		if logf != nil && baseFont != "" {
			logf("font: no bundled substitute for non-embedded base font %q; skipping", name)
		}
		return nil, ErrNoEmbeddedProgram
	}
	if logf != nil {
		logf("font: substituting bundled %s for non-embedded base font %q", sub.Name, name)
	}
	return parseProgram(sub.Data, substituteKind(sub.Kind))
}

// parseProviderProgram parses raw font-program bytes handed back by a Provider,
// sniffing the format so the caller need not declare it: an sfnt/TrueType or
// OpenType container (optionally WOFF/WOFF2-wrapped) is unwrapped and parsed as
// TrueType/CFF; a classic Type1 program (PFB or bare eexec/PostScript) as Type1;
// and a bare CFF table as CFF. It returns ErrUnsupportedFontProgram for an
// unrecognizable blob so the caller falls back to the bundle.
func parseProviderProgram(data []byte) (*program, error) {
	switch providerProgramKind(data) {
	case progTrueType:
		// Reuse LoadSFNT's container handling (incl. WOFF1/WOFF2 unwrap and the
		// glyf-vs-CFF-flavored OTTO distinction), then adopt its parsed program.
		face, err := LoadSFNT(data)
		if err != nil {
			return nil, err
		}
		return face.prog, nil
	case progCFF:
		return parseProgram(data, progCFF)
	default:
		return parseProgram(data, progType1)
	}
}

// providerProgramKind sniffs raw font-program bytes to a progKind. It recognizes
// the sfnt/WOFF signatures (→ progTrueType, unwrapped/flavor-resolved by
// LoadSFNT), a bare CFF header (first byte 0x01, the CFF major version →
// progCFF), and otherwise assumes a classic Type1 program (PFB magic 0x80 or a
// "%!" PostScript header → progType1).
func providerProgramKind(data []byte) progKind {
	if len(data) < 4 {
		return progType1
	}
	switch binary.BigEndian.Uint32(data[:4]) {
	case sigTrueType, sigTrue, sigOTTO, sigTTCF, sigWOFF, sigWOFF2:
		return progTrueType
	}
	// A bare CFF table begins with major/minor version bytes; the shipped CFF
	// major version is 1, so a leading 0x01 (not a PFB 0x80 or PostScript '%')
	// marks a bare CFF program.
	if data[0] == 0x01 {
		return progCFF
	}
	return progType1
}

// substituteKind maps a standard.Kind to this package's progKind.
func substituteKind(k standard.Kind) progKind {
	if k == standard.KindTrueType {
		return progTrueType
	}
	return progType1
}

// styleFromBaseFont infers weight/slant from a /BaseFont name's conventional
// suffix ("Helvetica-Bold", "Times-BoldItalic", "Courier-Oblique",
// "Arial,BoldItalic"). Matching is on the lowercased, space-stripped name.
func styleFromBaseFont(baseFont string) (bold, italic bool) {
	n := canonicalName(baseFont)
	bold = strings.Contains(n, "bold")
	italic = strings.Contains(n, "italic") || strings.Contains(n, "oblique")
	return bold, italic
}

// styleFromFlags derives weight/slant from the FontDescriptor /Flags integer:
// bit 19 (ForceBold) → bold, bit 7 (Italic) → italic. A missing descriptor or
// /Flags yields (false, false).
func styleFromFlags(doc *pdf.Document, fontDict pdf.Dict) (bold, italic bool) {
	desc := doc.GetDict(fontDict["FontDescriptor"])
	if desc == nil {
		return false, false
	}
	flags, ok := doc.GetInt(desc["Flags"])
	if !ok {
		return false, false
	}
	return flags&fontFlagForceBold != 0, flags&fontFlagItalic != 0
}

// stripStyleSuffix removes a conventional weight/slant token from a /BaseFont
// name so the bare family can be handed to a Provider (whose LoadStyled probes
// "Family-Bold" etc. itself). It drops a "-"/"," separated trailing style token
// (Bold, Italic, Oblique, BoldItalic, BoldOblique, and their reordered forms),
// preserving the family's own casing. A name without such a suffix is returned
// unchanged. The PostScript-subset prefix ("ABCDEF+") is stripped too.
func stripStyleSuffix(baseFont string) string {
	s := baseFont
	if i := strings.IndexByte(s, '+'); i == 6 {
		s = s[i+1:]
	}
	// Split at the last '-' or ',' and check whether the tail is a pure style token.
	sep := strings.LastIndexAny(s, "-,")
	if sep < 0 {
		return s
	}
	tail := strings.ToLower(strings.ReplaceAll(s[sep+1:], " ", ""))
	switch tail {
	case "bold", "italic", "oblique",
		"bolditalic", "boldoblique",
		"italicbold", "obliquebold":
		return s[:sep]
	}
	return s
}

// canonicalName lowercases baseFont, strips a subset prefix ("ABCDEF+") and
// spaces, mirroring standard.canonical so the style tests here agree with the
// bundle's family resolution.
func canonicalName(baseFont string) string {
	s := baseFont
	if i := strings.IndexByte(s, '+'); i == 6 {
		s = s[i+1:]
	}
	s = strings.ReplaceAll(s, " ", "")
	return strings.ToLower(s)
}
