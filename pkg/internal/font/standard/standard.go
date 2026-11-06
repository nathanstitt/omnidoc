// Package standard bundles permissively-licensed substitute fonts for the
// PDF standard-14 base fonts (and common aliases) so that simple fonts which
// declare a standard /BaseFont but embed no font program can still be rendered.
//
// # Bundled faces and licensing
//
// All shipped fonts are permissively licensed and MIT-compatible:
//
//   - TeX Gyre Heros (Helvetica/Arial-like sans) and TeX Gyre Termes
//     (Times-like serif), classic Type 1 PFB programs from the GUST e-foundry,
//     under the GUST Font License — an instance of the LaTeX Project Public
//     License (LPPL): free use, modification, and redistribution (renaming only
//     *requested*, not required, on modification). See fonts/GUST-FONT-LICENSE.txt.
//   - Inconsolata (monospace, Courier-like), SIL Open Font License 1.1 (OFL) —
//     free use, modification, and redistribution. See fonts/LICENSE-Inconsolata.txt.
//   - Noto Sans Hebrew and Noto Naskh Arabic, SIL Open Font License 1.1 (OFL),
//     from the upstream notofonts.github.io release. Neither declares a Reserved
//     Font Name, so they ship under their original names. See
//     fonts/LICENSE-NotoSansHebrew.txt and fonts/LICENSE-NotoNaskhArabic.txt.
//
// None are GPL/AGPL. All may be embedded and shipped inside this library and any
// binary built from it.
//
// # Script fallback
//
// The Latin substitutes cover no right-to-left script, and the two Noto faces cover
// ONLY their own script (no Latin, and not each other's). A paragraph is one styled
// run with one family but its text can mix scripts, so ScriptFallback resolves a
// covering face per RUNE — without it, Hebrew and Arabic in an otherwise-Latin
// paragraph are silently dropped by the shaper.
//
// # Coverage
//
// Helvetica/Arial -> Heros; Times/TimesNewRoman -> Termes; Courier/CourierNew ->
// Inconsolata. Regular, bold, italic, and bold-italic are bundled for the sans and
// serif families, so a weighted/slanted base font resolves to the matching face; the
// monospace family ships regular + bold (its italic reuses the upright weight, as
// Inconsolata has no upright-italic here). Lookup infers the variant from the /BaseFont
// name; LookupStyled takes an explicit weight/slant (the reflow path). The Symbol and
// ZapfDingbats fonts have no permissively-licensed look-alike bundled here, so they are
// reported as unavailable and a caller-supplied font provider or graceful skip handles
// them.
package standard

import (
	_ "embed"
	"strings"

	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gobolditalic"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/goregular"
)

//go:embed fonts/NotoSansHebrew-Regular.ttf
var notoHebrewTTF []byte

//go:embed fonts/NotoNaskhArabic-Regular.ttf
var notoArabicTTF []byte

//go:embed fonts/TeXGyreHeros-Regular.pfb
var herosPFB []byte

//go:embed fonts/TeXGyreHeros-Bold.pfb
var herosBoldPFB []byte

//go:embed fonts/TeXGyreHeros-Italic.pfb
var herosItalicPFB []byte

//go:embed fonts/TeXGyreHeros-BoldItalic.pfb
var herosBoldItalicPFB []byte

//go:embed fonts/TeXGyreTermes-Regular.pfb
var termesPFB []byte

//go:embed fonts/TeXGyreTermes-Bold.pfb
var termesBoldPFB []byte

//go:embed fonts/TeXGyreTermes-Italic.pfb
var termesItalicPFB []byte

//go:embed fonts/TeXGyreTermes-BoldItalic.pfb
var termesBoldItalicPFB []byte

//go:embed fonts/Inconsolata-Regular.ttf
var inconsolataTTF []byte

//go:embed fonts/Inconsolata-Bold.ttf
var inconsolataBoldTTF []byte

// Kind identifies the on-disk font-program format of a bundled substitute so the
// caller can hand it to the matching parser (classic Type1 PFB vs. TrueType).
type Kind int

const (
	// KindType1 is a classic Type 1 (PostScript, eexec) program in PFB form,
	// parsed the same way as a PDF /FontFile.
	KindType1 Kind = iota
	// KindTrueType is an SFNT/TrueType program, parsed like a PDF /FontFile2.
	KindTrueType
)

// Substitute is a bundled replacement face for a standard font: its embedded
// program bytes and the format those bytes are in.
type Substitute struct {
	// Name is the bundled face's identifier, useful for logging which substitute
	// was chosen (e.g. "TeXGyreHeros-Regular").
	Name string
	// Data is the raw embedded font program.
	Data []byte
	// Kind is the program format of Data.
	Kind Kind
}

// family holds the four weight/slant variants of a bundled font family. A missing
// variant (nil-data) is never stored: a family that lacks a real variant repeats the
// nearest available face (e.g. the monospace italic reuses its upright weight), so
// pick always returns a usable Substitute.
type family struct {
	regular, bold, italic, boldItalic Substitute
}

// pick returns the variant matching bold/italic, falling back to the nearest bundled
// weight when the exact variant is not shipped (recorded via the returned Substitute's
// Name so the caller can log the approximation).
func (f family) pick(bold, italic bool) Substitute {
	switch {
	case bold && italic:
		return f.boldItalic
	case bold:
		return f.bold
	case italic:
		return f.italic
	default:
		return f.regular
	}
}

// The three bundled families, each with its weight/slant variants. Heros (sans) and
// Termes (serif) ship all four; Inconsolata (mono) ships regular + bold, so its italic
// and bold-italic reuse the upright weight (see package doc).
var (
	heros = family{
		regular:    Substitute{Name: "TeXGyreHeros-Regular", Data: herosPFB, Kind: KindType1},
		bold:       Substitute{Name: "TeXGyreHeros-Bold", Data: herosBoldPFB, Kind: KindType1},
		italic:     Substitute{Name: "TeXGyreHeros-Italic", Data: herosItalicPFB, Kind: KindType1},
		boldItalic: Substitute{Name: "TeXGyreHeros-BoldItalic", Data: herosBoldItalicPFB, Kind: KindType1},
	}
	termes = family{
		regular:    Substitute{Name: "TeXGyreTermes-Regular", Data: termesPFB, Kind: KindType1},
		bold:       Substitute{Name: "TeXGyreTermes-Bold", Data: termesBoldPFB, Kind: KindType1},
		italic:     Substitute{Name: "TeXGyreTermes-Italic", Data: termesItalicPFB, Kind: KindType1},
		boldItalic: Substitute{Name: "TeXGyreTermes-BoldItalic", Data: termesBoldItalicPFB, Kind: KindType1},
	}
	mono = family{
		regular:    Substitute{Name: "Inconsolata-Regular", Data: inconsolataTTF, Kind: KindTrueType},
		bold:       Substitute{Name: "Inconsolata-Bold", Data: inconsolataBoldTTF, Kind: KindTrueType},
		italic:     Substitute{Name: "Inconsolata-Regular", Data: inconsolataTTF, Kind: KindTrueType},  // no upright-italic bundled
		boldItalic: Substitute{Name: "Inconsolata-Bold", Data: inconsolataBoldTTF, Kind: KindTrueType}, // bold-italic → bold
	}
	// Script families. Unlike the Latin substitutes above these are NOT
	// general-purpose: each covers only its own script (Noto Sans Hebrew has no
	// Latin or Arabic; Noto Naskh Arabic has no Latin or Hebrew). They exist so a
	// run of Hebrew or Arabic inside an otherwise-Latin paragraph resolves per
	// rune rather than being dropped — see ScriptFallback.
	//
	// Only a regular weight is bundled for each; bold/italic reuse it rather than
	// synthesizing, so weighted RTL text renders upright-regular.
	hebrew = family{
		regular:    Substitute{Name: "NotoSansHebrew-Regular", Data: notoHebrewTTF, Kind: KindTrueType},
		bold:       Substitute{Name: "NotoSansHebrew-Regular", Data: notoHebrewTTF, Kind: KindTrueType},
		italic:     Substitute{Name: "NotoSansHebrew-Regular", Data: notoHebrewTTF, Kind: KindTrueType},
		boldItalic: Substitute{Name: "NotoSansHebrew-Regular", Data: notoHebrewTTF, Kind: KindTrueType},
	}
	arabic = family{
		regular:    Substitute{Name: "NotoNaskhArabic-Regular", Data: notoArabicTTF, Kind: KindTrueType},
		bold:       Substitute{Name: "NotoNaskhArabic-Regular", Data: notoArabicTTF, Kind: KindTrueType},
		italic:     Substitute{Name: "NotoNaskhArabic-Regular", Data: notoArabicTTF, Kind: KindTrueType},
		boldItalic: Substitute{Name: "NotoNaskhArabic-Regular", Data: notoArabicTTF, Kind: KindTrueType},
	}
	// greekCyrillic covers Greek and Cyrillic, which the TeX Gyre substitutes
	// cover only in part (accented Greek is missing) or not at all (Cyrillic).
	// The Go fonts carry the WGL4 set in four styles under a BSD licence and
	// already ship in golang.org/x/image, so they add no download.
	greekCyrillic = family{
		regular:    Substitute{Name: "Go-Regular", Data: goregular.TTF, Kind: KindTrueType},
		bold:       Substitute{Name: "Go-Bold", Data: gobold.TTF, Kind: KindTrueType},
		italic:     Substitute{Name: "Go-Italic", Data: goitalic.TTF, Kind: KindTrueType},
		boldItalic: Substitute{Name: "Go-BoldItalic", Data: gobolditalic.TTF, Kind: KindTrueType},
	}
)

// ScriptFallback returns the bundled face that covers r, for a rune the requested
// family cannot map. It resolves Hebrew, Arabic, Greek and Cyrillic, which no Latin
// substitute covers fully; every other rune returns ok=false so the caller keeps its
// existing behavior (skip the rune and log).
//
// This is a per-RUNE lookup on purpose. A paragraph is one styled run with one
// family, but its text can mix scripts, and each bundled face covers exactly one —
// so the fallback has to be decided per character, not per run.
func ScriptFallback(r rune, bold, italic bool) (Substitute, bool) {
	switch {
	case r >= 0x0590 && r <= 0x05FF, // Hebrew
		r >= 0xFB1D && r <= 0xFB4F: // Hebrew presentation forms
		return hebrew.pick(bold, italic), true
	case r >= 0x0600 && r <= 0x06FF, // Arabic
		r >= 0x0750 && r <= 0x077F, // Arabic Supplement
		r >= 0x08A0 && r <= 0x08FF, // Arabic Extended-A
		r >= 0xFB50 && r <= 0xFDFF, // Arabic presentation forms A
		r >= 0xFE70 && r <= 0xFEFF: // Arabic presentation forms B
		return arabic.pick(bold, italic), true
	case r >= 0x0370 && r <= 0x03FF, // Greek and Coptic
		r >= 0x0400 && r <= 0x04FF: // Cyrillic
		return greekCyrillic.pick(bold, italic), true
	}
	return Substitute{}, false
}

// Lookup returns the bundled substitute face for a font family name, resolving
// the 14 PDF standard names and common aliases (Arial->Helvetica, CourierNew->
// Courier, TimesNewRoman->Times) as well as the default Office families used by
// DOCX and other reflowable documents (Calibri/Segoe UI->Heros sans,
// Cambria/Georgia->Termes serif, Consolas->Inconsolata monospace). It also resolves
// the generic CSS family keywords (serif->Termes, sans-serif->Heros,
// monospace->Inconsolata, cursive/fantasy->Termes), which is the family a CSS reflow
// frontend computes when no concrete family is named. It strips a subset prefix
// ("ABCDEF+Name") and is case-insensitive. ok is false for families with no bundled
// substitute — notably Symbol, ZapfDingbats, and Wingdings, and any unrecognized
// name.
func Lookup(baseFont string) (Substitute, bool) {
	bold, italic := styleFromName(baseFont)
	return LookupStyled(baseFont, bold, italic)
}

// LookupStyled is Lookup with an explicit weight/slant, for callers (the reflow engine)
// that carry the computed style separately from the family name. It resolves the family
// exactly as Lookup, then returns that family's bold / italic / bold-italic variant
// (falling back to the nearest bundled weight for a family that lacks the exact one —
// see family.pick). bold/italic passed here override any weight/slant encoded in the
// name. ok is false for families with no bundled substitute (Symbol, ZapfDingbats,
// Wingdings, or an unrecognized name).
func LookupStyled(baseFont string, bold, italic bool) (Substitute, bool) {
	fam, ok := familyOf(baseFont)
	if !ok {
		return Substitute{}, false
	}
	return fam.pick(bold, italic), true
}

// familyOf resolves a base-font / family name to its bundled family, or ok=false.
func familyOf(baseFont string) (family, bool) {
	name := canonical(baseFont)
	// Generic CSS family keywords (the default a reflow frontend computes when no
	// concrete family is named) map to the matching substitute style: serif->Termes,
	// sans-serif->Heros, monospace->Inconsolata, with cursive/fantasy falling back to
	// serif. These are matched exactly (not by prefix), before the named-family
	// aliases, so a concrete family is never shadowed by a generic keyword.
	switch name {
	case "serif", "cursive", "fantasy":
		return termes, true
	case "sans-serif", "system-ui", "ui-sans-serif":
		return heros, true
	case "monospace", "ui-monospace":
		return mono, true
	}
	switch {
	case strings.HasPrefix(name, "courier"),
		strings.HasPrefix(name, "consolas"),
		strings.HasPrefix(name, "inconsolata"):
		return mono, true
	case strings.HasPrefix(name, "times"),
		strings.HasPrefix(name, "cambria"),
		strings.HasPrefix(name, "georgia"),
		strings.HasPrefix(name, "texgyretermes"): // the bundled serif's own name
		return termes, true
	case strings.HasPrefix(name, "helvetica"),
		strings.HasPrefix(name, "arial"),
		strings.HasPrefix(name, "calibri"),
		strings.HasPrefix(name, "segoeui"),
		strings.HasPrefix(name, "verdana"),
		strings.HasPrefix(name, "texgyreheros"): // the bundled sans's own name
		return heros, true
	default:
		return family{}, false
	}
}

// styleFromName infers weight/slant from a PDF /BaseFont name, which conventionally
// encodes the variant as a suffix ("Helvetica-Bold", "Times-BoldItalic",
// "Courier-Oblique", "Arial,BoldItalic", "ArialMT-Italic"). Matching is on the
// canonicalized (lowercased, space-stripped) name so all the punctuation styles reduce
// to substring tests. It is best-effort: a name with no weight/slant token yields
// (false, false), and an explicit style passed to LookupStyled overrides this.
func styleFromName(baseFont string) (bold, italic bool) {
	n := canonical(baseFont)
	bold = strings.Contains(n, "bold")
	italic = strings.Contains(n, "italic") || strings.Contains(n, "oblique")
	return bold, italic
}

// canonical lowercases baseFont and removes a subset tag ("ABCDEF+") and any
// spaces so "Arial", "ArialMT", "Helvetica-Bold", "TimesNewRomanPSMT" and a
// subsetted "ABCDEF+Helvetica" all reduce to a comparable form.
func canonical(baseFont string) string {
	s := baseFont
	if i := strings.IndexByte(s, '+'); i >= 0 && i == 6 {
		s = s[i+1:]
	}
	s = strings.ReplaceAll(s, " ", "")
	return strings.ToLower(s)
}
