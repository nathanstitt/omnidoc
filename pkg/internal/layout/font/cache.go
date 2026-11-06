// Package font provides a concurrency-safe cache of resolved font faces for the
// reflow engine. Parsing a font program is the expensive step, so a document that
// uses the same family across many runs should parse each (family, style) once.
//
// The cache lives under pkg/layout (not a specific format package) because every
// reflowable frontend — DOCX, and later HTML/EPUB — resolves named families the
// same way; keeping the mutable cache here keeps pkg/font itself free of global
// state.
package font

import (
	"context"
	"strings"
	"sync"

	gcss "github.com/nathanstitt/omnidoc/pkg/internal/css"
	pkgfont "github.com/nathanstitt/omnidoc/pkg/internal/font"
	"github.com/nathanstitt/omnidoc/pkg/resource"
)

// faceKey identifies a resolved face: a normalized family name plus its style.
type faceKey struct {
	family string
	style  pkgfont.Style
}

// cacheEntry memoizes one resolution, including the negative result (no bundled
// substitute) so a missing family is not re-looked-up on every run.
type cacheEntry struct {
	face *pkgfont.Face
	ok   bool
}

// FaceCache resolves named font families to *font.Face, caching each result. It
// is safe for concurrent use. The zero value is not usable; build one with
// NewFaceCache.
type FaceCache struct {
	mu    sync.Mutex
	faces map[faceKey]cacheEntry

	// Web-font resolution state (nil/empty for bundled-only caches, e.g. DOCX).
	fontFaces map[string][]gcss.FontFace // normalized family -> @font-face entries
	loader    resource.ResourceLoader
	sys       SystemFontProvider
	logf      func(string, ...any)
}

// NewFaceCache returns an empty cache ready for use. Its logf is a no-op; use
// NewFaceCacheWithFonts to observe degradation.
func NewFaceCache() *FaceCache {
	return &FaceCache{
		faces: make(map[faceKey]cacheEntry),
		// Non-nil so every resolution path can log unconditionally, matching the
		// invariant NewFaceCacheWithFonts establishes.
		logf: func(string, ...any) {},
	}
}

// NewFaceCacheWithFonts returns a cache that resolves @font-face families to
// downloaded faces before falling back to bundled substitutes. faces are the
// captured @font-face rules (grouped by family internally); loader fetches url()
// sources; sys resolves local() sources (nil → local() never matches); logf logs
// degradation (nil → no-op). It is safe for concurrent use.
func NewFaceCacheWithFonts(faces []gcss.FontFace, loader resource.ResourceLoader, sys SystemFontProvider, logf func(string, ...any)) *FaceCache {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	byFamily := make(map[string][]gcss.FontFace)
	for _, ff := range faces {
		key := normalizeFamily(ff.Family)
		byFamily[key] = append(byFamily[key], ff)
	}
	return &FaceCache{
		faces:     make(map[faceKey]cacheEntry),
		fontFaces: byFamily,
		loader:    loader,
		sys:       sys,
		logf:      logf,
	}
}

// normalizeFamily lowercases and trims a family name for case-insensitive lookup.
func normalizeFamily(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Resolve returns the face for family in the requested style, substituting a
// bundled look-alike via pkg/font. family may be a CSS font-family fallback list
// (comma-separated, as cleaned by the cascade); each candidate is tried in order —
// @font-face sources first, then a bundled substitute — and the first that
// resolves wins.
//
// A list that resolves to nothing degrades to the bundled serif (logged), so text is
// never silently dropped for want of a font; see resolveList. ok is therefore false
// only when the bundle itself cannot be loaded, which callers still treat as "skip
// the run". Results — including misses — are cached under the whole list string, so
// repeated calls for the same (list, style) are cheap and the fallback logs once.
func (c *FaceCache) Resolve(family string, style pkgfont.Style) (*pkgfont.Face, bool) {
	key := faceKey{family: normalizeFamily(family), style: style}

	c.mu.Lock()
	defer c.mu.Unlock()
	if e, found := c.faces[key]; found {
		return e.face, e.ok
	}
	face, ok := c.resolveList(family, style)
	c.faces[key] = cacheEntry{face: face, ok: ok}
	return face, ok
}

// ResolveScriptFallback returns a bundled face covering r, for a rune the run's own
// family cannot map, or ok=false when none does.
//
// Each bundled face covers one script — the Latin substitutes have no Hebrew or
// Arabic, and the two Noto faces have no Latin — so a paragraph mixing scripts needs
// a covering face chosen per RUNE. Results are cached under a synthetic family key so
// the program is parsed once per (script, style), not once per glyph.
//
// It deliberately consults only the BUNDLED faces: a fallback triggers when the
// author's chosen family lacks the character, and quietly substituting a downloaded
// @font-face or a system font there would be a surprising second guess at intent.
func (c *FaceCache) ResolveScriptFallback(r rune, style pkgfont.Style) (*pkgfont.Face, bool) {
	// Emoji come FIRST and take a different route: the bundle has no emoji face (a
	// colour emoji font is megabytes, far past what the toolkit embeds), so they
	// resolve through the injected system provider instead. Without this, an emoji in
	// ordinary text falls through to the Latin substitutes, which have no glyph for it
	// — the reason "Hi 😀" rendered as "Hi " with nothing after it.
	if isEmojiRune(r) {
		if face, ok := c.resolveEmojiFallback(style); ok {
			return face, true
		}
		// No emoji font on this host: fall through, and the caller degrades to the
		// missing-glyph path rather than painting a wrong character.
	}
	script, ok := fallbackScriptOf(r)
	if !ok {
		return nil, false
	}
	key := faceKey{family: "\x00fallback:" + script, style: style}

	c.mu.Lock()
	defer c.mu.Unlock()
	if e, found := c.faces[key]; found {
		return e.face, e.ok
	}
	face, ok := pkgfont.LoadScriptFallback(r, style)
	c.faces[key] = cacheEntry{face: face, ok: ok}
	return face, ok
}

// emojiFallbackFamilies are the installed families tried, in order, for an emoji with
// no glyph in the run's own face. The list spans the three platforms' system emoji
// fonts plus the common Linux package names; the first that resolves AND actually has
// a glyph for the rune wins.
//
// It is a fixed list rather than a scan because the OS font matcher answers by name,
// and "the emoji font" is not something a font's metadata declares.
var emojiFallbackFamilies = []string{
	"Apple Color Emoji", // macOS, iOS
	"Segoe UI Emoji",    // Windows
	"Noto Color Emoji",  // Linux, Android, ChromeOS
	"Twemoji Mozilla",   // Firefox's bundled emoji font
	"EmojiOne Color",
	"Symbola", // monochrome, but covers the range
}

// resolveEmojiFallback finds an installed emoji face, cached under one synthetic key
// so the (expensive) provider lookup happens once per style per document.
func (c *FaceCache) resolveEmojiFallback(style pkgfont.Style) (*pkgfont.Face, bool) {
	key := faceKey{family: "\x00fallback:emoji", style: style}

	c.mu.Lock()
	defer c.mu.Unlock()
	if e, found := c.faces[key]; found {
		return e.face, e.ok
	}
	var face *pkgfont.Face
	ok := false
	for _, fam := range emojiFallbackFamilies {
		if f, hit := c.resolveProvider(fam, style); hit {
			face, ok = f, true
			break
		}
	}
	if ok {
		c.logf("font: emoji fallback resolved to an installed colour font")
	}
	c.faces[key] = cacheEntry{face: face, ok: ok}
	return face, ok
}

// isEmojiRune reports whether r is in a range an emoji font is expected to cover.
//
// It is deliberately a RANGE test rather than a per-font cmap probe: the caller reaches
// here only after the run's own face already failed to map the rune, so the question is
// "which fallback should try this", and the emoji blocks answer it without loading a
// font. The ranges are the Unicode emoji blocks plus the older dingbat/symbol blocks
// that emoji fonts also cover.
func isEmojiRune(r rune) bool {
	switch {
	case r >= 0x1F300 && r <= 0x1FAFF: // Misc Symbols and Pictographs .. Symbols Extended-A
		return true
	case r >= 0x1F000 && r <= 0x1F2FF: // Mahjong, Dominoes, Playing Cards, Enclosed
		return true
	case r >= 0x2600 && r <= 0x27BF: // Misc Symbols, Dingbats
		return true
	case r >= 0x2B00 && r <= 0x2BFF: // Misc Symbols and Arrows
		return true
	case r >= 0xFE00 && r <= 0xFE0F: // Variation Selectors (VS16 requests emoji style)
		return true
	case r >= 0x1F900 && r <= 0x1F9FF: // Supplemental Symbols and Pictographs
		return true
	case r == 0x203C || r == 0x2049 || r == 0x20E3 || r == 0x2122 || r == 0x2139:
		return true
	}
	return false
}

// fallbackScriptOf names the script r belongs to for fallback-cache purposes, or
// ok=false if no bundled face covers it. The names only have to be stable and
// distinct — they key the cache, nothing else reads them.
func fallbackScriptOf(r rune) (string, bool) {
	switch {
	case r >= 0x0590 && r <= 0x05FF, r >= 0xFB1D && r <= 0xFB4F:
		return "hebrew", true
	case r >= 0x0600 && r <= 0x06FF, r >= 0x0750 && r <= 0x077F,
		r >= 0x08A0 && r <= 0x08FF, r >= 0xFB50 && r <= 0xFDFF,
		r >= 0xFE70 && r <= 0xFEFF:
		return "arabic", true
	case r >= 0x0370 && r <= 0x03FF, r >= 0x0400 && r <= 0x04FF:
		return "greek-cyrillic", true
	}
	return "", false
}

// resolveList tries each comma-separated candidate in the font-family list in
// order, returning the first face that resolves. For each candidate the resolution
// chain is: @font-face sources (so a downloaded face beats a bundled look-alike of
// the same name), then the injected Provider's style-aware lookup (system/disk fonts,
// including weighted real faces and families the bundle has no look-alike for), then
// the bundled substitute. Caller holds c.mu.
//
// When NO candidate resolves, it falls back to the bundled serif rather than reporting
// a miss, because the caller's response to a miss is to skip the run — i.e. to render
// no text at all. A list that names only families the host lacks ("Roboto", with no
// trailing generic) would otherwise produce a blank page, which reads as a layout bug
// rather than a font one. Browsers substitute a default face here, and so do we; the
// substitution is logged once per (list, style) because Resolve caches this result.
func (c *FaceCache) resolveList(family string, style pkgfont.Style) (*pkgfont.Face, bool) {
	for _, name := range splitFamilyList(family) {
		if face, ok := c.resolveFontFace(name, style); ok {
			return face, true
		}
		if face, ok := c.resolveProvider(name, style); ok {
			return face, true
		}
		if face, ok := pkgfont.LoadStandard(name, style); ok {
			return face, true
		}
	}
	// Terminal fallback. defaultFallbackFamily is a generic keyword, which
	// LoadStandard always resolves, so this fails only if the bundle itself is
	// unusable — and then there is genuinely nothing to draw with.
	if face, ok := pkgfont.LoadStandard(defaultFallbackFamily, style); ok {
		c.logf("font: no face for %q; substituting bundled %s", family, defaultFallbackFamily)
		return face, true
	}
	return nil, false
}

// defaultFallbackFamily is the generic used when a font-family list resolves to
// nothing. CSS leaves the initial font-family up to the UA; serif matches this
// engine's own initial value, so an unresolvable list degrades to the same face an
// unstyled document already uses rather than introducing a third typeface.
const defaultFallbackFamily = "serif"

// resolveProvider consults the injected Provider (when the configured sys also
// implements pkgfont.Provider) for a style-aware, non-@font-face face: the disk or
// system provider serves a weighted real face for the family, which beats the bundled
// look-alike. It decodes the returned bytes via pkgfont.LoadSFNT, so it handles
// TrueType/OpenType and WOFF1/WOFF2 program bytes; a provider that returns any other
// program format (e.g. a classic Type1 PFB) is logged and skipped, and resolution
// falls through to the bundled substitute — pkg/font exposes no general public loader
// for arbitrary program bytes, and the bundled DiskFontProvider only serves sfnt/WOFF
// files. Returns false when no Provider is configured or it has no match. Caller holds
// c.mu.
func (c *FaceCache) resolveProvider(family string, style pkgfont.Style) (*pkgfont.Face, bool) {
	prov, ok := c.sys.(pkgfont.Provider)
	if !ok || prov == nil {
		return nil, false
	}
	raw, ok := prov.LoadStyled(family, style.Bold, style.Italic)
	if !ok {
		return nil, false
	}
	face, err := pkgfont.LoadSFNT(raw)
	if err != nil {
		c.logf("font provider %q: decode failed (non-sfnt program?): %v", family, err)
		return nil, false
	}
	return face, true
}

// splitFamilyList splits a (already-cleaned) comma-separated font-family list into
// its candidate names, dropping empties. A single bare name yields one element, so
// callers need not special-case the non-list form.
func splitFamilyList(family string) []string {
	parts := strings.Split(family, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// resolveFontFace walks the @font-face entries for family (best style match first),
// trying each source in order: local() via the system provider, url() via the
// loader. The first that decodes wins. Returns false when there is no @font-face
// for the family or none of its sources resolve. Caller holds c.mu.
func (c *FaceCache) resolveFontFace(family string, style pkgfont.Style) (*pkgfont.Face, bool) {
	entries := c.fontFaces[normalizeFamily(family)]
	if len(entries) == 0 {
		return nil, false
	}
	for _, ff := range bestFirst(entries, style) {
		for _, src := range ff.Sources {
			var raw []byte
			switch {
			case src.Local != "":
				if c.sys == nil {
					continue
				}
				b, ok := c.sys.LoadLocal(src.Local)
				if !ok {
					continue
				}
				raw = b
			case src.URL != "":
				if c.loader == nil {
					continue
				}
				b, _, err := c.loader.Load(context.Background(), src.URL)
				if err != nil {
					c.logf("@font-face %q: fetch %q failed: %v", family, src.URL, err)
					continue
				}
				raw = b
			default:
				continue
			}
			face, err := pkgfont.LoadSFNT(raw)
			if err != nil {
				c.logf("@font-face %q: decode failed: %v", family, err)
				continue
			}
			return face, true
		}
	}
	return nil, false
}

// bestFirst orders @font-face entries so the one best matching style comes first:
// exact weight+style, then a regular/unspecified entry, then the rest in source
// order. This is a coarse match — full font-weight numeric matching is a deferral.
func bestFirst(entries []gcss.FontFace, style pkgfont.Style) []gcss.FontFace {
	wantBold := style.Bold
	wantItalic := style.Italic
	score := func(ff gcss.FontFace) int {
		ffBold := ff.Weight == "bold" || ff.Weight == "700"
		ffItalic := ff.Style == "italic" || ff.Style == "oblique"
		s := 0
		if ffBold == wantBold {
			s += 2
		}
		if ffItalic == wantItalic {
			s++
		}
		return s
	}
	out := make([]gcss.FontFace, len(entries))
	copy(out, entries)
	// Insertion sort by DESCENDING score, stable (keeps source order within equal
	// scores). Avoids sort.Slice/modernize friction; entry counts are tiny.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && score(out[j]) > score(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
