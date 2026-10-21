package omnidoc

import (
	"context"
	"fmt"
	"io"

	"github.com/nathanstitt/omnidoc/pkg/internal/htmlwrite"
)

// HTMLWriteOptions controls conversion to HTML.
type HTMLWriteOptions struct {
	// Fragment, when true, emits only the body markup (no <!DOCTYPE>/<html>/<head>
	// wrapper), for embedding. Default false (a complete document).
	Fragment bool
	// ImageDir saves each embedded (data:) image as a file in this directory and
	// references the file instead of inlining the bytes. The directory is created
	// on the first image. Other image references are left as they are.
	ImageDir string
	// ImageRef is the prefix written before a saved image's file name. Default:
	// the base name of ImageDir. Only used with ImageDir.
	ImageRef string
	// Logf receives degradation diagnostics (nil -> no-op).
	Logf func(string, ...any)
}

func (o HTMLWriteOptions) toWriterOptions() htmlwrite.Options {
	return htmlwrite.Options{Fragment: o.Fragment, ImageSrc: newImageExporter(o.ImageDir, o.ImageRef, o.Logf).src, Logf: o.Logf}
}

// WriteHTML writes an opened document to out as HTML. It works on any document that can
// produce a box tree: every reflow input (HTML, DOCX, EPUB, Markdown, ...), or an opened
// PDF (whose logical structure is recovered by extraction). A document with no
// convertible structure returns ErrNoStructure. Unlike GFM, HTML expresses table cell
// spans natively, so extracted tables round-trip their colspan/rowspan losslessly.
func (d *Document) WriteHTML(_ context.Context, out io.Writer, opts HTMLWriteOptions) error {
	root, err := structureRoot(d, "WriteHTML")
	if err != nil {
		return err
	}
	if err := htmlwrite.Write(root, out, opts.toWriterOptions()); err != nil {
		return fmt.Errorf("omnidoc: write html: %w", err)
	}
	return nil
}
