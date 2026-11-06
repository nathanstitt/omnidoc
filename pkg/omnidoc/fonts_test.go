package omnidoc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/font/gofont/gobold"
)

// The public font seams must be nameable and implementable from outside this
// module: a caller writes a type against FontProvider / SystemFontProvider and
// hands it to RasterOptions, SVGOptions, or WithSystemFontProvider. Before
// these types existed the signatures named interfaces from pkg/internal, which
// compiled but could not be referred to by any importer.
var (
	_ FontProvider       = DirFontProvider{}
	_ SystemFontProvider = DirFontProvider{}
)

// callerProvider stands in for an application's own implementation.
type callerProvider struct{ data []byte }

func (c callerProvider) LoadStyled(string, bool, bool) ([]byte, bool) { return c.data, c.data != nil }
func (c callerProvider) LoadLocal(string) ([]byte, bool)              { return c.data, c.data != nil }

func TestPublicFontProviderSeams(t *testing.T) {
	p := callerProvider{data: []byte("not a real font")}
	// Assignability is the whole point: these must compile.
	opts := RasterOptions{FontProvider: p}
	svg := SVGOptions{FontProvider: p}
	_ = WithSystemFontProvider(p)

	if got := opts.fontProvider(); got == nil {
		t.Fatal("an explicit FontProvider must win over the mode default")
	} else if b, ok := got.LoadStyled("Any", false, false); !ok || !bytes.Equal(b, p.data) {
		t.Fatalf("LoadStyled through the internal seam = (%q, %v), want the caller's bytes", b, ok)
	}
	if svg.FontProvider == nil {
		t.Fatal("SVGOptions.FontProvider dropped the caller's provider")
	}
	// A nil public interface must convert to a nil internal one, so the
	// bundled/system default still applies when the caller sets nothing.
	if got := (RasterOptions{BundledFonts: true}).fontProvider(); got != nil {
		t.Fatalf("nil FontProvider in bundled mode resolved to %T, want nil", got)
	}
}

func TestDirFontProvider(t *testing.T) {
	dir := t.TempDir()
	bold := []byte("bold-face-bytes")
	regular := []byte("regular-face-bytes")
	for name, data := range map[string][]byte{"Foo-Bold.ttf": bold, "Foo.ttf": regular} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := DirFontProvider{Dir: dir}

	if b, ok := p.LoadStyled("Foo", true, false); !ok || !bytes.Equal(b, bold) {
		t.Errorf("LoadStyled(Foo, bold) = (%q, %v), want the Foo-Bold file", b, ok)
	}
	// No italic file: degrade to the less specific candidate rather than fail.
	if b, ok := p.LoadStyled("Foo", false, true); !ok || !bytes.Equal(b, regular) {
		t.Errorf("LoadStyled(Foo, italic) = (%q, %v), want the regular file", b, ok)
	}
	// local() names match case-insensitively and without an extension.
	if b, ok := p.LoadLocal("foo-bold"); !ok || !bytes.Equal(b, bold) {
		t.Errorf("LoadLocal(foo-bold) = (%q, %v), want the Foo-Bold file", b, ok)
	}
	if _, ok := p.LoadLocal("Missing"); ok {
		t.Error("LoadLocal(Missing) reported a match")
	}
	if _, ok := (DirFontProvider{}).LoadStyled("Foo", false, false); ok {
		t.Error("zero-value DirFontProvider must never match")
	}
}

// Names in Greek, Cyrillic, Hebrew and accented Latin print with the bundled
// fonts alone: no rune reaches the .notdef path.
func TestBundledFontsPrintGreekCyrillicHebrewAndAccentedLatin(t *testing.T) {
	var logs []string
	src := `<!doctype html><html style="font-family: sans-serif"><body>` +
		`<p>Zoë Ñandú Łódź</p><p>Ελληνικά ά</p><p>Москва Київ</p><p>שלום</p></body></html>`
	doc, err := OpenHTMLBytes([]byte(src), WithDefaultPaged(), WithBundledFonts(),
		WithLogf(func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var pdf bytes.Buffer
	if err := doc.WritePDF(context.Background(), &pdf, PDFOptions{}); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, l := range logs {
		if strings.Contains(l, "no glyph for") {
			t.Errorf("missing glyph: %s", l)
		}
	}
	// Positive check: the PDF reopens and its text layer gives the words back,
	// so each glyph was embedded with its /ToUnicode mapping.
	reopened, err := OpenBytes(pdf.Bytes())
	if err != nil {
		t.Fatalf("reopen the PDF: %v", err)
	}
	var text bytes.Buffer
	if err := reopened.WriteText(context.Background(), &text, MarkdownOptions{}); err != nil {
		t.Fatalf("extract text: %v", err)
	}
	for _, word := range []string{"Zoë", "Ñandú", "Łódź", "Ελληνικά", "Москва", "Київ"} {
		if !strings.Contains(text.String(), word) {
			t.Errorf("the PDF text lacks %q; got %q", word, text.String())
		}
	}
}

// Through the public API, in hermetic bundled mode: box-drawing characters the
// bundle lacks draw as .notdef without a fonts directory, and from the
// directory's font with one.
func TestWithFallbackFontDirCoversWhatTheBundleLacks(t *testing.T) {
	src := []byte(`<!doctype html><html style="font-family: sans-serif"><body><p>Box ─░▲</p></body></html>`)
	missing := func(opts ...HTMLOption) int {
		var n int
		opts = append(opts, WithDefaultPaged(), WithBundledFonts(), WithLogf(func(f string, a ...any) {
			if strings.Contains(fmt.Sprintf(f, a...), "no glyph for") {
				n++
			}
		}))
		doc, err := OpenHTMLBytes(src, opts...)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		var pdf bytes.Buffer
		if err := doc.WritePDF(context.Background(), &pdf, PDFOptions{}); err != nil {
			t.Fatalf("write: %v", err)
		}
		return n
	}
	if missing() == 0 {
		t.Fatal("precondition: the bundle already covers ─░▲, pick other characters")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Extra.ttf"), gobold.TTF, 0o644); err != nil {
		t.Fatalf("write font: %v", err)
	}
	if n := missing(WithFallbackFontDir(dir)); n != 0 {
		t.Errorf("%d characters still missing with the fonts directory", n)
	}
}
