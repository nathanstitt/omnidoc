package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nathanstitt/omnidoc/pkg/crop"
	"github.com/nathanstitt/omnidoc/pkg/omnidoc"
	"github.com/nathanstitt/omnidoc/testdata/gen"
	gendocx "github.com/nathanstitt/omnidoc/testdata/gen/docx"
	genxlsx "github.com/nathanstitt/omnidoc/testdata/gen/xlsx"
)

const convertTestHTML = `<!DOCTYPE html><html><body>
<h1>Convert Title</h1>
<p>Body text for the convert command.</p>
</body></html>`

func writeConvertInput(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConvertCmdHTMLToPDF(t *testing.T) {
	in := writeConvertInput(t, "in.html", []byte(convertTestHTML))
	out := filepath.Join(t.TempDir(), "out.pdf")
	if err := convertCmd([]string{in, out, "--bundled-fonts"}); err != nil {
		t.Fatalf("convertCmd: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("output not written: %v", err)
	}
	if !strings.HasPrefix(string(data), "%PDF-") {
		t.Errorf("output lacks a PDF header")
	}
}

func TestConvertCmdPDFToMarkdown(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.pdf")
	writeTestPDF(t, in, convertTestHTML)
	out := filepath.Join(dir, "out.md")
	if err := convertCmd([]string{in, out}); err != nil {
		t.Fatalf("convertCmd: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("output not written: %v", err)
	}
	if !strings.Contains(string(data), "Convert Title") {
		t.Errorf("markdown output missing recovered text:\n%s", data)
	}
}

func TestConvertCmdDOCXToHTML(t *testing.T) {
	in := writeConvertInput(t, "in.docx", gendocx.Core[0].Bytes())
	out := filepath.Join(t.TempDir(), "out.html")
	if err := convertCmd([]string{in, out}); err != nil {
		t.Fatalf("convertCmd: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("output not written: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "<!DOCTYPE html>") || !strings.Contains(got, "quick brown fox") {
		t.Errorf("html output missing scaffold or content:\n%s", got)
	}
}

// TestConvertCmdToOverridesExtension verifies --to wins over the output
// extension.
func TestConvertCmdToOverridesExtension(t *testing.T) {
	in := writeConvertInput(t, "in.html", []byte(convertTestHTML))
	out := filepath.Join(t.TempDir(), "out.dat")
	if err := convertCmd([]string{in, out, "--to", "md"}); err != nil {
		t.Fatalf("convertCmd: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("output not written: %v", err)
	}
	if !strings.Contains(string(data), "# Convert Title") {
		t.Errorf("markdown output missing heading:\n%s", data)
	}
}

// TestConvertCmdFromOverride verifies --from opens a mislabeled input as the
// named format.
func TestConvertCmdFromOverride(t *testing.T) {
	// A damaged-header PDF under a nonsense extension: detection cannot place it
	// (magic window misses, no extension signal), so --from is required.
	damaged := append(make([]byte, 1500), gen.TextPDF()...)
	in := writeConvertInput(t, "blob.dat", damaged)
	out := filepath.Join(t.TempDir(), "out.md")
	if err := convertCmd([]string{in, out}); err == nil {
		t.Fatalf("expected detection failure without --from")
	}
	if err := convertCmd([]string{in, out, "--from", "pdf"}); err != nil {
		t.Fatalf("convertCmd --from pdf: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("output not written: %v", err)
	}
}

func TestConvertCmdRejectsSameFormat(t *testing.T) {
	in := writeConvertInput(t, "in.html", []byte(convertTestHTML))
	err := convertCmd([]string{in, filepath.Join(t.TempDir(), "out.html")})
	if err == nil || !strings.Contains(err.Error(), "same format") {
		t.Errorf("html->html: want a same-format error, got %v", err)
	}
}

func TestConvertCmdMultiPageImages(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(in, gen.MultiPagePDF(3), 0o600); err != nil {
		t.Fatal(err)
	}
	// Multi-page without %d is a clean error.
	err := convertCmd([]string{in, filepath.Join(dir, "out.png"), "--pages", "1-2"})
	if err == nil || !strings.Contains(err.Error(), "%d") {
		t.Errorf("want a %%d-placeholder error, got %v", err)
	}
	// With %d, one file per page appears.
	if err := convertCmd([]string{in, filepath.Join(dir, "page-%d.png"), "--pages", "1-2", "--dpi", "36"}); err != nil {
		t.Fatalf("convertCmd: %v", err)
	}
	for _, n := range []string{"page-1.png", "page-2.png"} {
		if fi, err := os.Stat(filepath.Join(dir, n)); err != nil || fi.Size() == 0 {
			t.Errorf("%s missing or empty (err=%v)", n, err)
		}
	}
}

func TestConvertCmdUnknownOutputExtension(t *testing.T) {
	in := writeConvertInput(t, "in.html", []byte(convertTestHTML))
	err := convertCmd([]string{in, filepath.Join(t.TempDir(), "out.xyz")})
	if err == nil || !strings.Contains(err.Error(), "--to") {
		t.Errorf("want an error pointing at --to, got %v", err)
	}
	// Stdout output requires --to.
	err = convertCmd([]string{in, "-"})
	if err == nil || !strings.Contains(err.Error(), "--to") {
		t.Errorf("stdout without --to: want an error pointing at --to, got %v", err)
	}
}

func TestRunDispatchesConvert(t *testing.T) {
	in := writeConvertInput(t, "in.html", []byte(convertTestHTML))
	out := filepath.Join(t.TempDir(), "out.md")
	if err := run([]string{"convert", in, out}); err != nil {
		t.Fatalf("run(convert ...): %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("output not written: %v", err)
	}
}

func TestConvertCmdMarkdownInput(t *testing.T) {
	in := writeConvertInput(t, "notes.md", []byte("# MD Title\n\nSome **bold** body.\n"))
	dir := t.TempDir()

	out := filepath.Join(dir, "out.pdf")
	if err := convertCmd([]string{in, out, "--bundled-fonts"}); err != nil {
		t.Fatalf("md->pdf: %v", err)
	}
	if data, _ := os.ReadFile(out); !strings.HasPrefix(string(data), "%PDF-") {
		t.Errorf("md->pdf output lacks a PDF header")
	}

	outHTML := filepath.Join(dir, "out.html")
	if err := convertCmd([]string{in, outHTML}); err != nil {
		t.Fatalf("md->html: %v", err)
	}
	if data, _ := os.ReadFile(outHTML); !strings.Contains(string(data), "MD Title") {
		t.Errorf("md->html output missing content:\n%s", data)
	}
}

func TestConvertCmdTextInput(t *testing.T) {
	in := writeConvertInput(t, "notes.txt", []byte("plain line one\nplain line two\n"))
	out := filepath.Join(t.TempDir(), "out.png")
	if err := convertCmd([]string{in, out, "--dpi", "36", "--bundled-fonts"}); err != nil {
		t.Fatalf("txt->png: %v", err)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() == 0 {
		t.Errorf("txt->png output missing or empty (err=%v)", err)
	}
}

func TestConvertCmdCSVInput(t *testing.T) {
	in := writeConvertInput(t, "data.csv", []byte("Name,Qty\nWidgets,5\n"))
	out := filepath.Join(t.TempDir(), "out.md")
	if err := convertCmd([]string{in, out}); err != nil {
		t.Fatalf("csv->md: %v", err)
	}
	data, _ := os.ReadFile(out)
	if !strings.Contains(string(data), "| Name | Qty |") || !strings.Contains(string(data), "| Widgets | 5 |") {
		t.Errorf("csv->md table wrong:\n%s", data)
	}
}

func TestConvertCmdCSVOutput(t *testing.T) {
	in := writeConvertInput(t, "in.html", []byte(`<html><body>
	<p>prose to drop</p>
	<table><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></table>
	</body></html>`))
	out := filepath.Join(t.TempDir(), "out.csv")
	if err := convertCmd([]string{in, out}); err != nil {
		t.Fatalf("html->csv: %v", err)
	}
	data, _ := os.ReadFile(out)
	if string(data) != "A,B\n1,2\n" {
		t.Errorf("html->csv = %q", data)
	}
}

// TestRunRejectsBareInput pins that a bare input path — which an earlier CLI
// accepted and mapped to a guessed subcommand — is now an error naming the
// commands that exist, while the explicit "convert" form still works.
func TestRunRejectsBareInput(t *testing.T) {
	in := writeConvertInput(t, "in.html", []byte(`<html><body><table><tr><td>x</td></tr></table></body></html>`))
	out := filepath.Join(t.TempDir(), "out.csv")
	if err := run([]string{"convert", in, "--out", out}); err != nil {
		t.Fatalf("run convert: %v", err)
	}
	if data, _ := os.ReadFile(out); string(data) != "x\n" {
		t.Errorf("csv output = %q", data)
	}
	if err := run([]string{in, "--out", out}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("bare input without a subcommand: err = %v, want an unknown-command error", err)
	}
}

func TestConvertCmdXLSXInput(t *testing.T) {
	in := writeConvertInput(t, "book.xlsx", genxlsx.Core[0].Bytes())
	out := filepath.Join(t.TempDir(), "out.md")
	if err := convertCmd([]string{in, out}); err != nil {
		t.Fatalf("xlsx->md: %v", err)
	}
	data, _ := os.ReadFile(out)
	for _, want := range []string{"Name", "42.5", "inline text"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("xlsx->md missing %q:\n%s", want, data)
		}
	}
	// Content detection works without the extension.
	inNoExt := writeConvertInput(t, "mystery-book", genxlsx.Core[0].Bytes())
	out2 := filepath.Join(t.TempDir(), "out2.csv")
	if err := convertCmd([]string{inNoExt, out2}); err != nil {
		t.Fatalf("extension-less xlsx->csv: %v", err)
	}
	if data, _ := os.ReadFile(out2); !strings.Contains(string(data), "Name,Qty") {
		t.Errorf("xlsx->csv wrong:\n%s", data)
	}
}

// xlsxFixture returns the named generated xlsx fixture's bytes.
func xlsxFixture(t *testing.T, name string) []byte {
	t.Helper()
	for _, f := range genxlsx.Core {
		if f.Name == name {
			return f.Bytes()
		}
	}
	t.Fatalf("no xlsx fixture %q", name)
	return nil
}

func TestConvertCmdXLSXSheetFlag(t *testing.T) {
	// --sheet selects one worksheet: only its content, and no sheet heading.
	in := writeConvertInput(t, "book.xlsx", xlsxFixture(t, "multisheet"))
	out := filepath.Join(t.TempDir(), "one.md")
	if err := convertCmd([]string{in, out, "--sheet", "Second"}); err != nil {
		t.Fatalf("convertCmd --sheet: %v", err)
	}
	got, _ := os.ReadFile(out)
	if !strings.Contains(string(got), "second sheet cell") {
		t.Errorf("selected sheet missing:\n%s", got)
	}
	if strings.Contains(string(got), "first sheet cell") {
		t.Errorf("unselected sheet leaked:\n%s", got)
	}

	// Repeated --sheet selects several, in order.
	out2 := filepath.Join(t.TempDir(), "two.md")
	if err := convertCmd([]string{in, out2, "--sheet", "Second", "--sheet", "First"}); err != nil {
		t.Fatalf("convertCmd repeated --sheet: %v", err)
	}
	got2, _ := os.ReadFile(out2)
	if i, j := strings.Index(string(got2), "## Second"), strings.Index(string(got2), "## First"); i < 0 || j < 0 || i > j {
		t.Errorf("repeated --sheet not rendered in order:\n%s", got2)
	}

	// An unknown sheet name is a hard error naming the sheet; no output file.
	out3 := filepath.Join(t.TempDir(), "none.md")
	err := convertCmd([]string{in, out3, "--sheet", "Nope"})
	if err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("want error naming missing sheet, got %v", err)
	}
	if _, statErr := os.Stat(out3); statErr == nil {
		t.Errorf("no output file should be created on a bad --sheet")
	}
}

func TestConvertCmdXLSXOutput(t *testing.T) {
	in := writeConvertInput(t, "in.html", []byte(`<html><body>
	<table><tr><th>A</th></tr><tr><td>1</td></tr></table></body></html>`))
	out := filepath.Join(t.TempDir(), "out.xlsx")
	if err := convertCmd([]string{in, out}); err != nil {
		t.Fatalf("html->xlsx: %v", err)
	}
	doc, err := omnidoc.OpenXLSXFile(out)
	if err != nil {
		t.Fatalf("produced xlsx does not open: %v", err)
	}
	var sb strings.Builder
	if err := doc.WriteText(t.Context(), &sb, omnidoc.MarkdownOptions{}); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if !strings.Contains(sb.String(), "A") || !strings.Contains(sb.String(), "1") {
		t.Errorf("xlsx content lost:\n%s", sb.String())
	}
}

func TestConvertCmdHelpExitsClean(t *testing.T) {
	if err := convertCmd([]string{"-h"}); err != nil {
		t.Errorf("-h should exit clean, got %v", err)
	}
}

// TestRasterizeCmdSniffsExtensionless pins the opener-unification behavior
// change: rasterize no longer assumes an unknown extension is a PDF — it
// detects the format from content, so an extension-less real PDF still works
// and garbage gets a format error instead of a PDF parse error.
func TestRasterizeCmdSniffsExtensionless(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "document") // no extension
	if err := os.WriteFile(in, gen.VectorPDF(), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.png")
	if err := rasterizeCmd([]string{in, "--out", out, "--dpi", "36"}); err != nil {
		t.Fatalf("rasterizeCmd on extension-less PDF: %v", err)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() == 0 {
		t.Errorf("output missing or empty (err=%v)", err)
	}

	// A recognized extension with mismatched content fails in that format's
	// parser (RTF is a real format now, so junk.rtf errors precisely).
	junk := filepath.Join(dir, "junk.rtf")
	if err := os.WriteFile(junk, []byte("not a document"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := rasterizeCmd([]string{junk, "--out", out})
	if err == nil || !strings.Contains(err.Error(), "not an RTF document") {
		t.Errorf("garbage .rtf: want the RTF parser's error, got %v", err)
	}
	// A completely unrecognizable file still reports a detection failure.
	noext := filepath.Join(dir, "junkfile")
	if err := os.WriteFile(noext, []byte{0x00, 0x01, 0xFE, 0xBA}, 0o600); err != nil {
		t.Fatal(err)
	}
	err = rasterizeCmd([]string{noext, "--out", out})
	if err == nil || !strings.Contains(err.Error(), "format") {
		t.Errorf("garbage input: want a format-detection error, got %v", err)
	}
}

func TestParseCropSize(t *testing.T) {
	for _, tc := range []struct {
		in      string
		w, h    int
		wantErr bool
	}{
		{in: "720x720", w: 720, h: 720},
		{in: "1920x1080", w: 1920, h: 1080},
		{in: "720X720", w: 720, h: 720},
		{in: "720", wantErr: true},
		{in: "0x720", wantErr: true},
		{in: "-1x720", wantErr: true},
		{in: "axb", wantErr: true},
		{in: "", wantErr: true},
	} {
		w, h, err := parseCropSize(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseCropSize(%q): expected an error, got %dx%d", tc.in, w, h)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseCropSize(%q): %v", tc.in, err)
			continue
		}
		if w != tc.w || h != tc.h {
			t.Errorf("parseCropSize(%q) = %dx%d, want %dx%d", tc.in, w, h, tc.w, tc.h)
		}
	}
}

func TestParseCropStrategy(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    crop.Strategy
		wantErr bool
	}{
		{in: "center", want: crop.StrategyCenter},
		{in: "centre", want: crop.StrategyCenter},
		{in: "north", want: crop.StrategyNorth},
		{in: "south", want: crop.StrategySouth},
		{in: "east", want: crop.StrategyEast},
		{in: "west", want: crop.StrategyWest},
		{in: "saliency", want: crop.StrategySaliency},
		{in: "smart", want: crop.StrategySaliency},
		{in: "sideways", wantErr: true},
		{in: "", wantErr: true},
	} {
		got, err := parseCropStrategy(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseCropStrategy(%q): expected an error, got %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseCropStrategy(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseCropStrategy(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestCropFlagsAreReorderable(t *testing.T) {
	// reorderArgs must treat --crop/--crop-size as value-taking in BOTH
	// subcommands, or the value is mistaken for the positional input.
	for _, tc := range []struct {
		name  string
		table map[string]bool
	}{
		{"convert", convertValueFlags},
		{"rasterize", rasterizeValueFlags},
	} {
		for _, flag := range []string{"-crop", "--crop", "-crop-size", "--crop-size"} {
			if !tc.table[flag] {
				t.Errorf("%s: %s missing from the value-flag table", tc.name, flag)
			}
		}
	}
}

const pixelHTML = `<p>Pic</p><img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==">`

func TestConvertCmdImagesDir(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.html")
	if err := os.WriteFile(in, []byte(pixelHTML), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "doc", "out.md")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	// The flag may follow the positionals.
	if err := convertCmd([]string{in, out, "--images-dir", filepath.Join(dir, "doc", "out_images")}); err != nil {
		t.Fatalf("convertCmd: %v", err)
	}
	data, _ := os.ReadFile(out)
	if !strings.Contains(string(data), "](out_images/") || strings.Contains(string(data), "data:") {
		t.Errorf("markdown should link out_images/ relative to the output:\n%s", data)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "doc", "out_images")); len(entries) != 1 {
		t.Errorf("%d image files, want 1", len(entries))
	}
}

func TestConvertCmdImagesDirRejectsOtherFormats(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.html")
	if err := os.WriteFile(in, []byte(pixelHTML), 0o600); err != nil {
		t.Fatal(err)
	}
	err := convertCmd([]string{in, filepath.Join(dir, "out.pdf"), "--images-dir", filepath.Join(dir, "i")})
	if err == nil || !strings.Contains(err.Error(), "--images-dir") {
		t.Errorf("err = %v, want a --images-dir format error", err)
	}
}

func TestImageLinkPrefix(t *testing.T) {
	for _, c := range []struct{ dir, out, want string }{
		{"out_images", "out.md", "out_images"},
		{"a/imgs", "a/out.md", "imgs"},
		{"imgs", "a/out.md", "../imgs"},
		{"imgs", "-", "imgs"},
	} {
		if got := imageLinkPrefix(c.dir, c.out); got != c.want {
			t.Errorf("imageLinkPrefix(%q,%q) = %q, want %q", c.dir, c.out, got, c.want)
		}
	}
}
