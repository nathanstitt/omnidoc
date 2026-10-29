package font

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nathanstitt/omnidoc/testdata/gen"
)

// fixturePath resolves a testdata/fonts/* fixture from the repo root (two levels
// up from pkg/font).
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "testdata", "fonts", name)
}

func TestLoadSFNTRawTTF(t *testing.T) {
	data, err := os.ReadFile(fixturePath(t, "webfont.ttf"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	face, err := LoadSFNT(data)
	if err != nil {
		t.Fatalf("LoadSFNT(ttf): %v", err)
	}
	// A letter present in the subset must resolve to a non-empty advance.
	out, adv, ok := face.Glyph('A')
	if !ok || adv <= 0 {
		t.Fatalf("Glyph('A') ok=%v adv=%v, want a real glyph", ok, adv)
	}
	_ = out // outline presence is not asserted here; advance + ok is the contract
}

func TestLoadSFNTRejectsGarbage(t *testing.T) {
	_, err := LoadSFNT([]byte("not a font at all"))
	if err == nil {
		t.Fatal("LoadSFNT(garbage) = nil error, want a typed error")
	}
}

// TestWithCmap covers the empty cmap added to an embedded sfnt that has none:
// without it the TrueType parser rejects the program; with it the program
// parses, keeps every glyph, and maps no rune. A font that has a cmap is
// returned unchanged.
func TestWithCmap(t *testing.T) {
	full := gen.RobotoTTF()
	if got := withCmap(full); &got[0] != &full[0] {
		t.Error("withCmap changed a font that has a cmap")
	}

	noCmap := stripCmap(t, full)
	if _, err := parseProgram(noCmap, progTrueType); !errors.Is(err, ErrUnsupportedFontProgram) {
		t.Fatalf("parseProgram(no cmap) err = %v, want ErrUnsupportedFontProgram", err)
	}
	prog, err := parseProgram(withCmap(noCmap), progTrueType)
	if err != nil {
		t.Fatalf("parseProgram(withCmap): %v", err)
	}
	ref, err := parseProgram(full, progTrueType)
	if err != nil {
		t.Fatalf("parseProgram(full): %v", err)
	}
	if prog.numGlyphs() != ref.numGlyphs() {
		t.Errorf("numGlyphs = %d, want %d", prog.numGlyphs(), ref.numGlyphs())
	}
	for _, r := range []rune{'A', 'a', 0xFFFF} {
		if gid, ok := prog.gidForRune(r); ok && gid != 0 {
			t.Errorf("gidForRune(%q) = %d, want no mapping", r, gid)
		}
	}
	gid, _ := ref.gidForRune('H')
	if p := prog.outline(gid); p == nil || len(p.Segments) == 0 {
		t.Errorf("outline(GID %d) is empty, want the 'H' glyph", gid)
	}
}

// stripCmap rebuilds an sfnt without its cmap table.
func stripCmap(t *testing.T, data []byte) []byte {
	t.Helper()
	tables, flavor, err := ParseSFNTTables(data)
	if err != nil {
		t.Fatalf("ParseSFNTTables: %v", err)
	}
	delete(tables, "cmap")
	return BuildSFNT(flavor, tables)
}
