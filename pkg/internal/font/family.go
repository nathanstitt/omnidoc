package font

import (
	"strings"

	"github.com/benoitkugler/textlayout/fonts"
	"github.com/benoitkugler/textlayout/fonts/glyphsnames"
	"github.com/benoitkugler/textlayout/fonts/truetype"

	"github.com/nathanstitt/omnidoc/pkg/internal/font/standard"
	"github.com/nathanstitt/omnidoc/pkg/internal/render"
)

// Style selects a weight/slant variant of a font family. Bold and Italic combine
// (Bold && Italic selects a bold-italic face where available). The bundled standard
// substitutes ship regular/bold/italic/bold-italic (see package standard), and a
// Provider may supply a weighted face for a family the bundle does not cover.
type Style struct {
	Bold   bool
	Italic bool
}

// Provider resolves a font family + style to raw font-program bytes (sfnt/TrueType,
// bare CFF, or classic Type1/PFB — the caller detects the format via LoadSFNT or the
// PDF program parser). It is the injectable resolution layer both pipelines consult
// before falling back to the bundled standard substitutes, so a caller can point the
// toolkit at system fonts or a fonts directory — including families the bundle has no
// look-alike for (Symbol, ZapfDingbats) and exact-metric real faces. The interface is
// defined here, in the low font layer, so both pkg/font (PDF) and the higher reflow
// engine can depend on it without a layer inversion; pkg/layout/font's DiskFontProvider
// implements it. ok is false when the provider has no match, and the caller proceeds to
// the bundled fallback.
type Provider interface {
	// LoadStyled returns the raw program bytes for family in the given weight/slant.
	LoadStyled(family string, bold, italic bool) (data []byte, ok bool)
}

// ProgramKind identifies the embedded font-program format of a Face's bytes, so a
// PDF writer can pick /FontFile2 (TrueType/SFNT), /FontFile3 (bare CFF/OpenType),
// or /FontFile (classic Type1) when embedding.
type ProgramKind int

const (
	// ProgramKindUnknown means the program bytes were not retained.
	ProgramKindUnknown ProgramKind = iota
	// ProgramKindTrueType is an SFNT/TrueType (glyf) program (embed as /FontFile2).
	ProgramKindTrueType
	// ProgramKindCFF is a bare CFF or CFF-flavored OpenType program (/FontFile3).
	ProgramKindCFF
	// ProgramKindType1 is a classic Type1 program (eexec/PFB); embed as /FontFile
	// with the Length1/Length2/Length3 segment sizes.
	ProgramKindType1
)

// Face is a measured, outline-able font face independent of point size. It is the
// reflow engine's view of a font: given a rune it yields a glyph outline (em
// units, Y up) and an advance (em units), and it reports vertical line metrics for
// line-height computation.
//
// A Face is read-only after construction and safe for concurrent use, mirroring
// the underlying *program. Build one with LoadStandard (or, in future, from an
// embedded document font), and cache it — parsing the font program is the
// expensive step.
type Face struct {
	prog    *program
	names   map[string]fonts.GID // glyph name → GID, built once for the name route
	aglGIDs map[rune]fonts.GID   // rune → GID by plain Adobe Glyph List name (no ".sc"/".alt" suffix)

	progData []byte      // raw program bytes for embedding (nil if not retained)
	progKind ProgramKind // format of progData

	// colr/cpal back ColorLayers: a face with colour tables paints certain glyphs as
	// stacked coloured outlines rather than one monochrome path. Both are nil for the
	// overwhelmingly common non-colour face, and every colour path checks colr first,
	// so an ordinary face costs one nil check.
	colr *colrTable
	cpal *cpalTable
	// sbix/cbdt back ColorBitmap: a face storing colour glyphs as PNG strikes rather
	// than as layered outlines (Apple Color Emoji, Noto Color Emoji). Consulted only
	// when colr is nil, since vector layers scale and bitmaps do not.
	sbix *sbixTable
	cbdt *cbdtTable
	// numGlyphs is needed to index sbix's per-strike offset array.
	numGlyphs int
}

// ColorBitmapFor returns the colour BITMAP for glyph gid at the given em size, or
// ok=false when the face has none. sizePt selects among the font's strikes: the
// nearest is chosen, preferring a larger one so the image is downscaled rather than
// enlarged.
//
// Prefer ColorLayers: those are outlines and scale cleanly. This is the fallback for
// fonts that ship rendered images (Apple Color Emoji has no COLR data at all), and the
// result is an image whose fidelity is bounded by the strike it came from.
func (f *Face) ColorBitmapFor(gid uint16, sizePt float64) (ColorBitmap, bool) {
	if f == nil {
		return ColorBitmap{}, false
	}
	if f.sbix != nil {
		if bm, ok := f.sbix.bitmapFor(gid, f.numGlyphs, sizePt); ok {
			return bm, true
		}
	}
	if f.cbdt != nil {
		if bm, ok := f.cbdt.bitmapFor(gid, sizePt); ok {
			return bm, true
		}
	}
	return ColorBitmap{}, false
}

// HasColorBitmaps reports whether the face carries colour bitmap strikes.
func (f *Face) HasColorBitmaps() bool { return f != nil && (f.sbix != nil || f.cbdt != nil) }

// ColorLayers returns the colour layers of glyph gid — the ordered (outline, colour)
// pairs a colour font paints it as — or ok=false for a glyph with no colour data, or a
// face with no usable COLR/CPAL pair.
//
// The caller draws each layer's GID through the normal outline path and fills it with
// the layer's Color, bottom layer first. A layer with Foreground set takes the
// surrounding text colour instead, which is how a colour font marks the parts meant to
// follow the document (COLR's 0xFFFF palette sentinel).
//
// ok=false is the honest degradation for a paint graph this engine cannot express —
// gradients, transforms, composites — because the caller then renders the glyph's own
// monochrome outline rather than a flat colour that would be confidently wrong.
func (f *Face) ColorLayers(gid uint16) ([]ColorLayer, bool) {
	if f.colr == nil {
		return nil, false
	}
	return f.colr.layersFor(gid, f.cpal)
}

// HasColorGlyphs reports whether the face carries a usable COLR/CPAL pair, so a caller
// can skip the per-glyph colour probe entirely for an ordinary face.
func (f *Face) HasColorGlyphs() bool { return f != nil && f.colr != nil }

// LoadStandard returns a Face for a named font family, substituting a bundled
// permissively-licensed look-alike. It resolves the standard-14 names and common
// aliases (Arial, Times New Roman, Courier New, and the Office defaults Calibri
// and Cambria) via standard.LookupStyled; family matching is case-insensitive and
// tolerant of spaces and a subset prefix. ok is false when no substitute is
// bundled for the family (e.g. Symbol, Wingdings), so the caller skips the run.
//
// style selects the weight/slant variant of the bundled family: the sans and serif
// substitutes ship regular/bold/italic/bold-italic, so a bold or italic run resolves
// to the matching weighted face (the monospace family reuses its upright weight for
// italic — see package standard). A family that lacks the exact variant falls back to
// the nearest bundled weight.
func LoadStandard(family string, style Style) (*Face, bool) {
	sub, ok := standard.LookupStyled(family, style.Bold, style.Italic)
	if !ok {
		return nil, false
	}
	prog, err := parseProgram(sub.Data, substituteKind(sub.Kind))
	if err != nil {
		return nil, false
	}
	names := prog.nameToGID()
	return &Face{
		prog:     prog,
		names:    names,
		aglGIDs:  aglRuneGIDs(names),
		progData: sub.Data,
		progKind: programKindFromStandard(sub.Kind),
	}, true
}

// LoadScriptFallback returns a Face covering r for a rune the requested family
// cannot map, or ok=false when no bundled face covers it.
//
// It exists because each bundled face covers one script: the Latin substitutes have
// no Hebrew or Arabic, and the two Noto faces have no Latin. A run carries a single
// family but its text can mix scripts, so the covering face has to be chosen per
// rune. Callers should cache by (rune-script, style) rather than calling per glyph —
// parsing the program is the expensive step.
func LoadScriptFallback(r rune, style Style) (*Face, bool) {
	sub, ok := standard.ScriptFallback(r, style.Bold, style.Italic)
	if !ok {
		return nil, false
	}
	prog, err := parseProgram(sub.Data, substituteKind(sub.Kind))
	if err != nil {
		return nil, false
	}
	names := prog.nameToGID()
	return &Face{
		prog:     prog,
		names:    names,
		aglGIDs:  aglRuneGIDs(names),
		progData: sub.Data,
		progKind: programKindFromStandard(sub.Kind),
	}, true
}

// programKindFromStandard maps a bundled substitute's Kind to a ProgramKind so the
// PDF writer picks the right /FontFile flavor for the embedded substitute.
func programKindFromStandard(k standard.Kind) ProgramKind {
	switch k {
	case standard.KindTrueType:
		return ProgramKindTrueType
	case standard.KindType1:
		return ProgramKindType1
	default:
		return ProgramKindUnknown
	}
}

// Glyph resolves rune r to its outline and advance. The outline is in em units
// with the Y axis pointing up (1.0 == 1 em); it is nil for a missing, empty, or
// whitespace glyph (a space has an advance but no outline). advanceEm is the
// horizontal advance in em units. ok is false when the face has no glyph mapped
// for r, in which case the caller should skip it (advancing nothing).
func (f *Face) Glyph(r rune) (outline *render.Path, advanceEm float64, ok bool) {
	gid, ok := f.gidForRune(r)
	if !ok {
		// The face has no glyph for r. If r is a list-marker bullet, synthesize its
		// geometry so the marker renders anyway (the bundled substitutes lack ▪ and
		// other bullet code points); browsers likewise paint markers as shapes, not
		// font glyphs. Any other missing rune is skipped by the caller.
		return syntheticBullet(r)
	}
	adv, _ := f.prog.advanceEm(gid)
	return f.prog.outline(gid), adv, true
}

// gidForRune resolves a rune to a glyph, preferring the standard glyph-name route
// (rune → Adobe glyph name → GID) and falling back to the program's own cmap.
// The name route is necessary for the bundled Type1 substitutes (TeX Gyre), whose
// built-in encoding maps some lowercase code points to alternate glyphs; resolving
// by name picks the expected outline, matching how the PDF simple-font path works.
func (f *Face) gidForRune(r rune) (fonts.GID, bool) {
	if name := runeToGlyphName(r); name != "" {
		if gid, ok := f.names[name]; ok && gid != 0 {
			return gid, true
		}
	}
	if gid, ok := f.aglGIDs[r]; ok {
		return gid, true
	}
	if gid, ok := f.prog.gidForRune(r); ok {
		return gid, true
	}
	return 0, false
}

// aglRuneGIDs maps each rune to the glyph whose plain Adobe Glyph List name
// spells it ("eacute" → é, "uni0151" → ő). A suffixed name ("eacute.sc",
// "a.alt") is an alternate and is skipped, so a rune never resolves to a
// small-cap or stylistic variant. Two plain names for one rune keep the lower
// GID, so the choice does not depend on map order.
func aglRuneGIDs(names map[string]fonts.GID) map[rune]fonts.GID {
	out := make(map[rune]fonts.GID, len(names))
	for name, gid := range names {
		if gid == 0 || strings.ContainsRune(name, '.') {
			continue
		}
		r, ok := glyphsnames.GlyphToRune(name)
		if !ok || r == 0 {
			continue
		}
		if prev, seen := out[r]; !seen || gid < prev {
			out[r] = gid
		}
	}
	return out
}

// ProgramBytes returns the raw font-program bytes for embedding and their format.
// kind is ProgramKindUnknown (and data nil) when the Face did not retain its
// program (the PDF writer then falls back to drawing outlines).
func (f *Face) ProgramBytes() (data []byte, kind ProgramKind) {
	return f.progData, f.progKind
}

// OpenTypeFont returns the parsed SFNT this face was built from, for callers that
// need its OpenType layout tables (GSUB/GPOS) — complex-script shaping above all.
// ok is false for a face whose program is not SFNT-based (a classic Type1 face such
// as the bundled TeX Gyre substitutes), which carries no layout tables at all.
//
// The returned value satisfies harfbuzz.FaceOpentype directly, so a shaper can build
// a harfbuzz font from it with no adapter. It is deliberately typed as the upstream
// *truetype.Font rather than an interface of our own: this package already depends on
// textlayout for parsing, so a wrapper would add a layer with no behavior. The font
// is read-only after parsing and safe for concurrent use.
func (f *Face) OpenTypeFont() (*truetype.Font, bool) {
	tt, ok := f.prog.gp.(ttProgram)
	if !ok {
		return nil, false
	}
	return tt.f, true
}

// FamilyName returns the family the face's own 'name' table declares, or ok=false
// for a face with no readable name table (a classic Type1 face, or an sfnt whose
// name table is absent or empty).
//
// It prefers the typographic family (name ID 16) over the legacy family (ID 1),
// because per-weight files from some foundries put a style-qualified name in ID 1 —
// Google's BarlowCondensed-SemiBold.ttf declares ID 1 "Barlow Condensed SemiBold"
// but ID 16 "Barlow Condensed". ID 16 is optional and is omitted by fonts whose
// family/style pair already fits the legacy four-style model, so ID 1 is the
// fallback rather than the other way round.
//
// This exists so a caller can VERIFY that a font resolved by name is actually the
// font it asked for: the OS matcher is fuzzy and answers even when nothing matches
// (see pkg/layout/font.OSFontProvider), so the declared name is the only trustworthy
// evidence of identity.
func (f *Face) FamilyName() (string, bool) {
	tt, ok := f.OpenTypeFont()
	if !ok {
		return "", false
	}
	for _, id := range [...]truetype.NameID{truetype.NamePreferredFamily, truetype.NameFontFamily} {
		if e := tt.Names.SelectEntry(id); e != nil {
			if name := strings.TrimSpace(e.String()); name != "" {
				return name, true
			}
		}
	}
	return "", false
}

// UnitsPerEm returns the face's units-per-em (always > 0).
func (f *Face) UnitsPerEm() float64 { return f.prog.upm }

// GID resolves rune r to a glyph id, preferring the glyph-name route then the
// program cmap, matching how Glyph resolves outlines. ok is false when the face
// has no glyph for r.
func (f *Face) GID(r rune) (gid uint16, ok bool) {
	g, ok := f.gidForRune(r)
	return uint16(g), ok
}

// Outline returns glyph gid's outline in em units (Y up), or nil if empty. It
// satisfies the render.GlyphRef face contract used by DrawGlyph.
func (f *Face) Outline(gid uint16) *render.Path { return f.prog.outline(fonts.GID(gid)) }

// GlyphAdvance returns gid's horizontal advance in em units (advance/units-per-em),
// for building a PDF /W widths array.
func (f *Face) GlyphAdvance(gid uint16) float64 {
	adv, _ := f.prog.advanceEm(fonts.GID(gid))
	return adv
}

// GlyphName returns gid's PostScript glyph name, or "" if unnamed. A PDF simple
// Type1 font's /Encoding /Differences maps character codes to these names.
func (f *Face) GlyphName(gid uint16) string { return f.prog.gp.glyphName(fonts.GID(gid)) }

// Metrics returns the face's vertical line metrics in em units (Y up): a positive
// ascent above the baseline, a positive descent magnitude below it, and the
// font's suggested extra line gap. The reflow engine derives line height from
// these. Faces whose program exposes no extents fall back to a 0.8/0.2/0
// approximation.
func (f *Face) Metrics() (ascent, descent, lineGap float64) {
	return f.prog.metrics()
}

// GlyphVAdvance returns gid's VERTICAL advance in em units as a positive downward
// distance, for laying out a vertical writing mode, plus whether a vertical layout can
// use it. It is GlyphAdvance's counterpart.
//
// The sign is worth stating because it is the trap here: the underlying library
// returns a NEGATIVE advance (it negates for a Y-down convention, so a 1000-upem
// face reports -1000). This package normalizes to positive-downward once, at the
// program adapter, so a caller can add it to a pen Y without thinking about it. A
// caller that assumed the raw upstream sign would run the pen backwards up the page
// with nothing to catch it — the value is plausible, merely inverted.
//
// ok is true for every bundled face. Where the font states no vertical advance — a
// TrueType face with no `vmtx`, or a format carrying no vertical metrics at all like
// Type1 and bare CFF — one em is synthesized, which is the correct fallback and what
// browsers do. Most Latin faces land there, INCLUDING the bundled sans-serif and
// serif, so that is the common path rather than the exotic one.
//
// Use VMetrics to tell an authored metric from a synthesized one. This method
// deliberately does not push "no answer" onto the caller: there is always a right
// answer, and this package is the layer that knows the em size to compute it from.
func (f *Face) GlyphVAdvance(gid uint16) (float64, bool) {
	return f.prog.vAdvanceEm(fonts.GID(gid))
}

// VMetrics returns the face's vertical-writing line metrics in em units, and whether
// the face genuinely carries a `vhea` table. Unlike Metrics, it does NOT substitute
// an approximation when the font is silent: reporting ok=false lets a caller tell an
// authored vertical metric from a guess, which matters because most Latin faces have
// no vhea at all and a fabricated number would hide that.
func (f *Face) VMetrics() (ascent, descent, lineGap float64, ok bool) {
	return f.prog.vMetrics()
}
