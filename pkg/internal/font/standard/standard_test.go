package standard

import "testing"

func TestLookup(t *testing.T) {
	tests := []struct {
		baseFont string
		wantName string
		wantOK   bool
	}{
		{"Helvetica", "TeXGyreHeros-Regular", true},
		// Weight/slant encoded in the /BaseFont name now selects the matching variant.
		{"Helvetica-Bold", "TeXGyreHeros-Bold", true},
		{"Helvetica-Oblique", "TeXGyreHeros-Italic", true},
		{"Helvetica-BoldOblique", "TeXGyreHeros-BoldItalic", true},
		{"Arial", "TeXGyreHeros-Regular", true},
		{"Arial-BoldMT", "TeXGyreHeros-Bold", true},
		{"Arial-ItalicMT", "TeXGyreHeros-Italic", true},
		{"ArialMT", "TeXGyreHeros-Regular", true},
		{"ABCDEF+Helvetica", "TeXGyreHeros-Regular", true},
		{"Times-Roman", "TeXGyreTermes-Regular", true},
		{"Times-Bold", "TeXGyreTermes-Bold", true},
		{"Times-Italic", "TeXGyreTermes-Italic", true},
		{"Times-BoldItalic", "TeXGyreTermes-BoldItalic", true},
		{"TimesNewRoman", "TeXGyreTermes-Regular", true},
		{"TimesNewRomanPSMT", "TeXGyreTermes-Regular", true},
		{"Courier", "Inconsolata-Regular", true},
		{"Courier-Bold", "Inconsolata-Bold", true},
		{"Courier-Oblique", "Inconsolata-Regular", true}, // no upright-italic mono → regular
		{"CourierNew", "Inconsolata-Regular", true},
		// Generic CSS family keywords.
		{"serif", "TeXGyreTermes-Regular", true},
		{"sans-serif", "TeXGyreHeros-Regular", true},
		{"monospace", "Inconsolata-Regular", true},
		{"cursive", "TeXGyreTermes-Regular", true},
		// The bundled substitutes named directly (canonical() strips spaces).
		{"TeX Gyre Termes", "TeXGyreTermes-Regular", true},
		{"TeX Gyre Heros", "TeXGyreHeros-Regular", true},
		{"TeXGyreHeros-Regular", "TeXGyreHeros-Regular", true},
		{"Inconsolata", "Inconsolata-Regular", true},
		{"Symbol", "", false},
		{"ZapfDingbats", "", false},
		{"SomeRandomFont", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.baseFont, func(t *testing.T) {
			sub, ok := Lookup(tc.baseFont)
			if ok != tc.wantOK {
				t.Fatalf("Lookup(%q) ok = %v, want %v", tc.baseFont, ok, tc.wantOK)
			}
			if ok {
				if sub.Name != tc.wantName {
					t.Errorf("Lookup(%q) name = %q, want %q", tc.baseFont, sub.Name, tc.wantName)
				}
				if len(sub.Data) == 0 {
					t.Errorf("Lookup(%q) returned empty Data", tc.baseFont)
				}
			}
		})
	}
}

// TestLookupStyled covers the explicit-style path the reflow engine uses: the family is
// resolved from the (style-free) name and the variant from the bold/italic flags.
func TestLookupStyled(t *testing.T) {
	tests := []struct {
		family       string
		bold, italic bool
		wantName     string
		wantOK       bool
	}{
		{"Arial", false, false, "TeXGyreHeros-Regular", true},
		{"Arial", true, false, "TeXGyreHeros-Bold", true},
		{"Arial", false, true, "TeXGyreHeros-Italic", true},
		{"Arial", true, true, "TeXGyreHeros-BoldItalic", true},
		{"Times", true, false, "TeXGyreTermes-Bold", true},
		{"Times", true, true, "TeXGyreTermes-BoldItalic", true},
		{"serif", false, true, "TeXGyreTermes-Italic", true},
		{"sans-serif", true, false, "TeXGyreHeros-Bold", true},
		// Monospace has no upright-italic: italic falls back to regular, bold-italic to bold.
		{"monospace", false, false, "Inconsolata-Regular", true},
		{"monospace", true, false, "Inconsolata-Bold", true},
		{"monospace", false, true, "Inconsolata-Regular", true},
		{"monospace", true, true, "Inconsolata-Bold", true},
		// An explicit style overrides the name's own weight token.
		{"Helvetica-Bold", false, false, "TeXGyreHeros-Regular", true},
		{"Symbol", true, false, "", false},
	}
	for _, tc := range tests {
		sub, ok := LookupStyled(tc.family, tc.bold, tc.italic)
		if ok != tc.wantOK {
			t.Errorf("LookupStyled(%q,%v,%v) ok = %v, want %v", tc.family, tc.bold, tc.italic, ok, tc.wantOK)
			continue
		}
		if ok {
			if sub.Name != tc.wantName {
				t.Errorf("LookupStyled(%q,%v,%v) = %q, want %q", tc.family, tc.bold, tc.italic, sub.Name, tc.wantName)
			}
			if len(sub.Data) == 0 {
				t.Errorf("LookupStyled(%q,%v,%v) returned empty Data", tc.family, tc.bold, tc.italic)
			}
		}
	}
}

// Cyrillic is not in TeX Gyre at all and accented Greek is missing, so both
// fall back to the bundled Go fonts, in the requested style.
func TestScriptFallbackCoversGreekAndCyrillic(t *testing.T) {
	cases := []struct {
		r            rune
		bold, italic bool
		want         string
	}{
		{'Ж', false, false, "Go-Regular"},
		{'ж', true, false, "Go-Bold"},
		{'ά', false, true, "Go-Italic"},
		{'Ω', true, true, "Go-BoldItalic"},
	}
	for _, c := range cases {
		sub, ok := ScriptFallback(c.r, c.bold, c.italic)
		if !ok {
			t.Errorf("ScriptFallback(%q) found no face", c.r)
			continue
		}
		if sub.Name != c.want || sub.Kind != KindTrueType || len(sub.Data) == 0 {
			t.Errorf("ScriptFallback(%q, bold=%v, italic=%v) = %s, want %s", c.r, c.bold, c.italic, sub.Name, c.want)
		}
	}
}
