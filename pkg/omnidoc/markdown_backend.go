package omnidoc

import (
	"context"
	"fmt"
	"io"
	"unicode/utf8"

	markdownwrite "github.com/nathanstitt/omnidoc/pkg/internal/markdownwrite"
)

// MarkdownOptions controls Markdown (or plain-text) output.
type MarkdownOptions struct {
	// Plain renders plain text instead of Markdown: no heading hashes, emphasis
	// markers, or link URLs — just the document's text with block and table structure
	// preserved as whitespace. Default false (GFM Markdown).
	Plain bool
	// MaxBytes, when > 0, truncates the output to at most MaxBytes bytes, cutting at
	// a UTF-8 rune boundary; further output is discarded (the document walk still
	// completes, cheaply). 0 (the default) writes everything. Built for search-index
	// extraction ("the first 50 KB of text").
	MaxBytes int
	// ImageDir, when non-empty, saves each embedded (data:) image as a file in this
	// directory and references the file instead of inlining the bytes. The
	// directory is created on the first image. Other image references are left
	// as they are. See ImageRef for what the written reference looks like.
	ImageDir string
	// ImageRef is the prefix written before a saved image's file name, e.g.
	// "report_images" gives "![](report_images/3fa9c1.png)". Default: the base
	// name of ImageDir. Only used with ImageDir.
	ImageRef string
	// Logf receives degradation diagnostics (e.g. a table with a synthesized header
	// row). nil -> no-op.
	Logf func(string, ...any)
}

func (o MarkdownOptions) toWriterOptions() markdownwrite.Options {
	return markdownwrite.Options{Plain: o.Plain, ImageSrc: newImageExporter(o.ImageDir, o.ImageRef, o.Logf).src, Logf: o.Logf}
}

// WriteMarkdown writes an opened document to out as GitHub-Flavored Markdown. It
// works on any document that can produce a box tree: every reflow input (HTML, DOCX,
// EPUB, ...), or an opened PDF (whose logical structure is recovered by extraction).
// A document with no convertible structure returns ErrNoStructure. Set opts.Plain
// for plain text.
func (d *Document) WriteMarkdown(_ context.Context, out io.Writer, opts MarkdownOptions) error {
	root, err := structureRoot(d, "WriteMarkdown")
	if err != nil {
		return err
	}
	if opts.MaxBytes > 0 {
		out = &truncateWriter{w: out, remaining: opts.MaxBytes}
	}
	if err := markdownwrite.Write(root, out, opts.toWriterOptions()); err != nil {
		return fmt.Errorf("omnidoc: write markdown: %w", err)
	}
	return nil
}

// truncateWriter passes writes through to w until its byte budget is spent,
// then silently discards the rest — reporting full-length writes so the
// document walk above completes without error. The write that crosses the
// budget is cut back to a UTF-8 rune boundary so the output never ends
// mid-rune.
type truncateWriter struct {
	w         io.Writer
	remaining int
}

func (t *truncateWriter) Write(p []byte) (int, error) {
	if t.remaining <= 0 {
		return len(p), nil
	}
	if len(p) <= t.remaining {
		n, err := t.w.Write(p)
		t.remaining -= n
		return n, err
	}
	// The crossing write: cut at the budget, then back up over any partial
	// rune straddling the cut (a UTF-8 continuation byte is never a start).
	cut := t.remaining
	for cut > 0 && !utf8.RuneStart(p[cut]) {
		cut--
	}
	n, err := t.w.Write(p[:cut])
	t.remaining = 0
	if err != nil {
		return n, err
	}
	return len(p), nil
}

// WriteText writes an opened reflow document to out as plain text (WriteMarkdown with
// opts.Plain forced true).
func (d *Document) WriteText(ctx context.Context, out io.Writer, opts MarkdownOptions) error {
	opts.Plain = true
	return d.WriteMarkdown(ctx, out, opts)
}
