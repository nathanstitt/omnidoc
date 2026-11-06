package inline

import (
	"testing"

	pkgfont "github.com/nathanstitt/omnidoc/pkg/internal/font"
)

// Arabic letters take their joining form from their neighbours in reading order:
// in مرحبا, meem starts the word (init), reh joins only to the right (fina), so hah
// starts a new join (init), beh is between two joiners (medi), and alef ends the
// word (fina). Shaping the segment left-to-right made harfbuzz join the word
// backwards: meem and alef came out isolated and beh initial.
func TestArabicJoiningFormsFollowReadingOrder(t *testing.T) {
	face, ok := pkgfont.LoadScriptFallback('م', pkgfont.Style{})
	if !ok {
		t.Fatal("no bundled Arabic face")
	}
	shaped, ok := shapeComplex(face, []rune("مرحبا"))
	if !ok {
		t.Fatal("shaping failed")
	}
	var got []string
	for _, g := range shaped {
		if face.GlyphAdvance(g.gid) == 0 {
			continue // a mark (beh's dot)
		}
		got = append(got, face.GlyphName(g.gid))
	}
	want := []string{"uni0645.init", "uni0631.fina", "uni062D.init", "uni066E.medi", "uni0627.fina"}
	if len(got) != len(want) {
		t.Fatalf("letters in logical order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("letters in logical order = %v, want %v", got, want)
			break
		}
	}
}
