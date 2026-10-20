package omnidoc

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func convertPDFWith(t *testing.T, pdf []byte, to Format, opts ConvertOptions) string {
	t.Helper()
	opts.From, opts.To, opts.BundledFonts = FormatPDF, to, true
	var out bytes.Buffer
	if err := Convert(context.Background(), bytes.NewReader(pdf), &out, opts); err != nil {
		t.Fatalf("pdf→%s: %v", to, err)
	}
	return out.String()
}

func TestImageDirMarkdown(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "pics")
	md := convertPDFWith(t, pdfImageFixture(t), FormatMarkdown, ConvertOptions{
		Markdown: MarkdownOptions{ImageDir: dir},
	})
	if strings.Contains(md, "data:") {
		t.Fatalf("markdown still inlines the image:\n%.300s", md)
	}
	m := regexp.MustCompile(`!\[\]\(pics/([0-9a-f]{12}\.(?:png|jpg))\)`).FindStringSubmatch(md)
	if m == nil {
		t.Fatalf("no link into pics/ in:\n%.400s", md)
	}
	f, err := os.Open(filepath.Join(dir, m[1]))
	if err != nil {
		t.Fatalf("linked file missing: %v", err)
	}
	defer f.Close()
	if _, _, err := image.Decode(f); err != nil {
		t.Errorf("linked file is not an image: %v", err)
	}
}

func TestImageDirHTMLAndCustomRef(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "x")
	html := convertPDFWith(t, pdfImageFixture(t), FormatHTML, ConvertOptions{
		HTMLOut: HTMLWriteOptions{ImageDir: dir, ImageRef: "/static/img/"},
	})
	if !regexp.MustCompile(`<img src="/static/img/[0-9a-f]{12}\.(png|jpg)"`).MatchString(html) {
		t.Errorf("html lacks a link with the custom ref prefix:\n%.500s", html)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("%d files saved, want 1", len(entries))
	}
}

// TestImageDirIsDeterministicAndDedups: the same picture twice is one file, and a
// second run reproduces the same output.
func TestImageDirIsDeterministicAndDedups(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := `<img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==">` +
		`<img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==">` +
		`<img src="https://example.com/remote.png">`
	run := func() string {
		var out bytes.Buffer
		err := Convert(context.Background(), strings.NewReader(src), &out, ConvertOptions{
			From: FormatHTML, To: FormatMarkdown, Markdown: MarkdownOptions{ImageDir: dir},
		})
		if err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	first, second := run(), run()
	if first != second {
		t.Errorf("output differs between runs:\n%s\n%s", first, second)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("%d files saved for two identical images, want 1", len(entries))
	}
	if !strings.Contains(first, "(https://example.com/remote.png)") {
		t.Errorf("a non-data image reference was rewritten:\n%s", first)
	}
}

func TestImageDirNotCreatedWithoutImages(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "never")
	var out bytes.Buffer
	err := Convert(context.Background(), strings.NewReader("<p>text only</p>"), &out, ConvertOptions{
		From: FormatHTML, To: FormatMarkdown, Markdown: MarkdownOptions{ImageDir: dir},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("image directory created although the document has no images")
	}
}

// TestImageDirUnwritableKeepsInline: when the directory cannot be made, the image
// stays inline (and is logged) instead of vanishing.
func TestImageDirUnwritableKeepsInline(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var logged []string
	md := convertPDFWith(t, pdfImageFixture(t), FormatMarkdown, ConvertOptions{
		Markdown: MarkdownOptions{ImageDir: filepath.Join(blocker, "sub"), Logf: func(f string, a ...any) { logged = append(logged, f) }},
	})
	if !strings.Contains(md, "data:image/") || len(logged) == 0 {
		t.Errorf("inline=%v logged=%v, want the image kept inline and a log", strings.Contains(md, "data:image/"), logged)
	}
}
