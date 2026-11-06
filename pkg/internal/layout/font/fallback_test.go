package font

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/image/font/gofont/gobold"

	pkgfont "github.com/nathanstitt/omnidoc/pkg/internal/font"
	"github.com/nathanstitt/omnidoc/pkg/internal/font/standard"
)

// fakeFonts is an installed-font provider serving fixed programs by family name
// and counting lookups.
type fakeFonts struct {
	byFamily map[string][]byte
	calls    int
}

func (f *fakeFonts) LoadLocal(string) ([]byte, bool) { return nil, false }

func (f *fakeFonts) LoadStyled(family string, _, _ bool) ([]byte, bool) {
	f.calls++
	b, ok := f.byFamily[family]
	return b, ok
}

func programOf(t *testing.T, face *pkgfont.Face) []byte {
	t.Helper()
	data, _ := face.ProgramBytes()
	return data
}

// firstRuneMissingFromSans returns the first candidate the bundled sans lacks
// and Go Bold has, so the test does not depend on one font's exact coverage.
func firstRuneMissingFromSans(t *testing.T, candidates ...rune) rune {
	t.Helper()
	sans, ok := pkgfont.LoadStandard("sans-serif", pkgfont.Style{})
	if !ok {
		t.Fatal("no bundled sans")
	}
	bold, err := pkgfont.LoadSFNT(gobold.TTF)
	if err != nil {
		t.Fatalf("load Go Bold: %v", err)
	}
	for _, r := range candidates {
		if gid, ok := sans.GID(r); ok && gid != 0 {
			continue
		}
		if gid, ok := bold.GID(r); ok && gid != 0 {
			return r
		}
	}
	t.Fatalf("every candidate %q is in the bundled sans or missing from Go Bold", candidates)
	return 0
}

// CSS Fonts 4 §5.3: a character the first family lacks is looked up in the
// next family of the list before any system fallback.
func TestResolveFallback_TriesLaterFamiliesInTheList(t *testing.T) {
	fonts := &fakeFonts{byFamily: map[string][]byte{"Test Family": gobold.TTF}}
	c := NewFaceCacheWithFonts(nil, nil, fonts, nil)
	face, ok := c.ResolveFallback('Ж', "sans-serif, Test Family", pkgfont.Style{})
	if !ok {
		t.Fatal("no fallback for Ж")
	}
	if !bytes.Equal(programOf(t, face), gobold.TTF) {
		t.Error("Ж did not come from the list's second family")
	}
}

// A rune no bundled face covers comes from an installed fallback family.
func TestResolveFallback_UsesInstalledFallbackFamiliesLast(t *testing.T) {
	r := firstRuneMissingFromSans(t, 0x2500, 0x2591, 0x25B2)
	fonts := &fakeFonts{byFamily: map[string][]byte{"DejaVu Sans": gobold.TTF}}
	c := NewFaceCacheWithFonts(nil, nil, fonts, nil)
	face, ok := c.ResolveFallback(r, "sans-serif", pkgfont.Style{})
	if !ok {
		t.Fatalf("no fallback for %q", r)
	}
	if !bytes.Equal(programOf(t, face), gobold.TTF) {
		t.Errorf("%q did not come from the installed DejaVu Sans", r)
	}
}

// The first installed emoji font is used only if it has the glyph; otherwise
// the search goes on (the ✓-as-tofu bug: Apple Color Emoji has no U+2713).
func TestResolveFallback_SkipsAnInstalledEmojiFontWithoutTheGlyph(t *testing.T) {
	r := firstRuneMissingFromSans(t, 0x2660, 0x263A, 0x266A)
	hebrew, ok := standard.ScriptFallback('א', false, false)
	if !ok {
		t.Fatal("no bundled Hebrew")
	}
	fonts := &fakeFonts{byFamily: map[string][]byte{
		"Apple Color Emoji": hebrew.Data,
		"Symbola":           gobold.TTF,
	}}
	c := NewFaceCacheWithFonts(nil, nil, fonts, nil)
	face, ok := c.ResolveFallback(r, "sans-serif", pkgfont.Style{})
	if !ok {
		t.Fatalf("no fallback for %q", r)
	}
	if !bytes.Equal(programOf(t, face), gobold.TTF) {
		t.Errorf("%q came from a font without the glyph", r)
	}
}

// Bundled-only mode has no installed fonts: an uncovered rune is a clean miss.
func TestResolveFallback_BundledOnlyMissesCleanly(t *testing.T) {
	r := firstRuneMissingFromSans(t, 0x2500, 0x2591, 0x25B2)
	if face, ok := NewFaceCache().ResolveFallback(r, "sans-serif", pkgfont.Style{}); ok {
		t.Errorf("got face %v for %q with no installed fonts", face, r)
	}
}

// The answer is cached per rune, list and style: a document with the same
// missing rune a thousand times asks the provider once.
func TestResolveFallback_CachesTheAnswer(t *testing.T) {
	r := firstRuneMissingFromSans(t, 0x2500, 0x2591, 0x25B2)
	fonts := &fakeFonts{byFamily: map[string][]byte{"DejaVu Sans": gobold.TTF}}
	c := NewFaceCacheWithFonts(nil, nil, fonts, nil)
	c.ResolveFallback(r, "sans-serif", pkgfont.Style{})
	before := fonts.calls
	c.ResolveFallback(r, "sans-serif", pkgfont.Style{})
	if fonts.calls != before {
		t.Errorf("second lookup asked the provider %d more times", fonts.calls-before)
	}
}

// fontDir writes files into a temp dir and returns it.
func fontDir(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// A caller-supplied directory covers what the bundle lacks, by glyph, whatever
// the files are called, and even with no system provider (bundled mode).
func TestResolveFallback_UsesTheSuppliedFontDirectory(t *testing.T) {
	r := firstRuneMissingFromSans(t, 0x2500, 0x2591, 0x25B2)
	c := NewFaceCache()
	c.SetFallbackDir(fontDir(t, map[string][]byte{"SomeVendorFont-Regular.ttf": gobold.TTF}))
	face, ok := c.ResolveFallback(r, "sans-serif", pkgfont.Style{})
	if !ok {
		t.Fatalf("no fallback for %q from the directory", r)
	}
	if !bytes.Equal(programOf(t, face), gobold.TTF) {
		t.Errorf("%q did not come from the directory's font", r)
	}
}

// Files that are not fonts, or that do not parse, are logged and skipped; the
// directory's good fonts still serve.
func TestResolveFallback_FontDirectorySkipsWhatIsNotAFont(t *testing.T) {
	r := firstRuneMissingFromSans(t, 0x2500, 0x2591, 0x25B2)
	var logs []string
	c := NewFaceCacheWithFonts(nil, nil, nil, func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	c.SetFallbackDir(fontDir(t, map[string][]byte{
		"a-broken.ttf": []byte("not a font"),
		"notes.txt":    []byte("hello"),
		"z-good.ttf":   gobold.TTF,
	}))
	if _, ok := c.ResolveFallback(r, "sans-serif", pkgfont.Style{}); !ok {
		t.Fatalf("no fallback for %q", r)
	}
	if !slices.ContainsFunc(logs, func(l string) bool { return strings.Contains(l, "a-broken.ttf") }) {
		t.Errorf("the broken font was not logged: %q", logs)
	}
}

// A font collection is skipped with a log: the parser reads only a collection's
// first face, and the PDF writer cannot embed a face taken from a collection.
func TestResolveFallback_FontDirectorySkipsCollections(t *testing.T) {
	r := firstRuneMissingFromSans(t, 0x2500, 0x2591, 0x25B2)
	var logs []string
	c := NewFaceCacheWithFonts(nil, nil, nil, func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	c.SetFallbackDir(fontDir(t, map[string][]byte{"Family.ttc": gobold.TTF}))
	if face, ok := c.ResolveFallback(r, "sans-serif", pkgfont.Style{}); ok {
		t.Errorf("got face %v for %q from a skipped collection", face, r)
	}
	if !slices.ContainsFunc(logs, func(l string) bool {
		return strings.Contains(l, "Family.ttc") && strings.Contains(l, "collections are not supported")
	}) {
		t.Errorf("the skipped collection was not logged: %q", logs)
	}
}

// A directory that does not exist is a logged miss, not a failure.
func TestResolveFallback_MissingFontDirectoryIsAMiss(t *testing.T) {
	r := firstRuneMissingFromSans(t, 0x2500, 0x2591, 0x25B2)
	var logs []string
	c := NewFaceCacheWithFonts(nil, nil, nil, func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	absent := filepath.Join(t.TempDir(), "absent")
	c.SetFallbackDir(absent)
	if _, ok := c.ResolveFallback(r, "sans-serif", pkgfont.Style{}); ok {
		t.Errorf("found %q with no fonts at all", r)
	}
	if !slices.ContainsFunc(logs, func(l string) bool { return strings.Contains(l, absent) }) {
		t.Errorf("the missing directory was not logged: %q", logs)
	}
}
