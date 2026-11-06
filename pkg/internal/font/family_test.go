package font

import (
	"bytes"
	"testing"
)

// LoadStandard now selects a weighted/slanted bundled variant from style, so a bold
// or italic request must resolve to a different font program than the regular face.
func TestLoadStandardStyleSelectsWeightedFace(t *testing.T) {
	reg, ok := LoadStandard("Arial", Style{})
	if !ok {
		t.Fatal("LoadStandard(Arial, regular): want ok=true")
	}
	regData, _ := reg.ProgramBytes()

	for _, tc := range []struct {
		name  string
		style Style
	}{
		{"bold", Style{Bold: true}},
		{"italic", Style{Italic: true}},
		{"bold-italic", Style{Bold: true, Italic: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			face, ok := LoadStandard("Arial", tc.style)
			if !ok {
				t.Fatalf("LoadStandard(Arial, %+v): want ok=true", tc.style)
			}
			data, _ := face.ProgramBytes()
			if len(data) == 0 {
				t.Fatal("ProgramBytes: want the retained program bytes")
			}
			// The weighted variant is a distinct font program (Heros ships all four),
			// so its embedded bytes differ from the regular face's.
			if bytes.Equal(data, regData) {
				t.Errorf("LoadStandard(Arial, %+v) program bytes equal the regular face; want the weighted variant", tc.style)
			}
		})
	}
}

func TestLoadStandardArial(t *testing.T) {
	face, ok := LoadStandard("Arial", Style{})
	if !ok {
		t.Fatal("LoadStandard(Arial): want a bundled substitute, got ok=false")
	}
	outline, adv, ok := face.Glyph('A')
	if !ok {
		t.Fatal("Glyph('A'): want ok=true")
	}
	if outline == nil || outline.Empty() {
		t.Error("Glyph('A'): want a non-empty outline")
	}
	// A capital A in a typical sans advances well under a full em and clearly more
	// than nothing; a loose sanity band catches a broken advance without pinning a
	// specific face's metrics.
	if adv <= 0.1 || adv >= 1.5 {
		t.Errorf("Glyph('A') advance = %v em, want within (0.1, 1.5)", adv)
	}

	asc, desc, _ := face.Metrics()
	if asc <= 0 || desc <= 0 {
		t.Errorf("Metrics() ascent=%v descent=%v, want both positive", asc, desc)
	}
}

func TestLoadStandardSpaceHasAdvanceNoOutline(t *testing.T) {
	face, ok := LoadStandard("Times New Roman", Style{})
	if !ok {
		t.Fatal("LoadStandard(Times New Roman): want ok=true")
	}
	outline, adv, ok := face.Glyph(' ')
	if !ok {
		t.Fatal("Glyph(' '): want ok=true")
	}
	if outline != nil && !outline.Empty() {
		t.Error("Glyph(' '): want a nil/empty outline for a space")
	}
	if adv <= 0 {
		t.Errorf("Glyph(' ') advance = %v, want > 0", adv)
	}
}

func TestLoadStandardOfficeDefaults(t *testing.T) {
	for _, family := range []string{"Calibri", "Cambria", "Consolas"} {
		if _, ok := LoadStandard(family, Style{}); !ok {
			t.Errorf("LoadStandard(%q): want a bundled substitute", family)
		}
	}
}

func TestLoadStandardUnsupported(t *testing.T) {
	for _, family := range []string{"Wingdings", "Symbol", "ZapfDingbats", "NoSuchFont"} {
		if face, ok := LoadStandard(family, Style{}); ok || face != nil {
			t.Errorf("LoadStandard(%q): want ok=false, nil face; got ok=%v", family, ok)
		}
	}
}

// The bundled TeX Gyre programs map accented code points to their small-cap
// alternates in the built-in cmap; a name route must win, so "Zoë" does not
// print as "Zoᴇ̈" in a small-cap e.
func TestStandardFacesMapAccentedLatinToPlainGlyphs(t *testing.T) {
	want := map[rune]string{
		'é': "eacute", 'ñ': "ntilde", 'ü': "udieresis", 'Å': "Aring",
		'ł': "lslash", 'Ł': "Lslash", 'č': "ccaron", 'ő': "ohungarumlaut",
	}
	for _, family := range []string{"sans-serif", "serif"} {
		for _, style := range []Style{{}, {Bold: true}, {Italic: true}} {
			face, ok := LoadStandard(family, style)
			if !ok {
				t.Fatalf("LoadStandard(%q, %+v) failed", family, style)
			}
			for r, name := range want {
				gid, ok := face.GID(r)
				if !ok {
					t.Errorf("%s %+v: no glyph for %q", family, style, r)
					continue
				}
				if got := face.GlyphName(gid); got != name {
					t.Errorf("%s %+v: %q drew glyph %q, want %q", family, style, r, got, name)
				}
			}
		}
	}
}
