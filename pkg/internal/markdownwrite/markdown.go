// Package markdown renders a cssbox tree (the shared box model produced by the HTML
// and DOCX frontends) to GitHub-Flavored Markdown, or to plain text. It is a
// conversion backend that walks the box tree directly rather than the render.Device
// paint seam: the paint seam sees only positioned glyphs, whereas Markdown needs the
// document's structure — headings, lists, links, emphasis, and tables. Because DOCX
// and HTML converge on the same cssbox tree, one walker serves both formats.
//
// The writer reads the semantic annotations the frontends record on each box
// (Box.SemTag / Box.HeadingLvl / Box.Href) together with the structural display kinds
// (DisplayListItem, DisplayTable, ...) and the inherited computed style (Bold / Italic
// / TextDecorationLine). It never mutates the tree, so the same read-only tree that
// feeds layout feeds conversion.
package markdown

import (
	"io"
	"strings"

	"github.com/nathanstitt/omnidoc/pkg/internal/boxwalk"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/cssbox"
)

// Options configures Markdown/text rendering.
type Options struct {
	// Plain renders plain text (no Markdown syntax): emphasis markers, heading
	// hashes, and link URLs are dropped; block and table structure is kept as
	// whitespace. The default (false) renders GFM Markdown.
	Plain bool
	// ImageSrc, when non-nil, rewrites each image's src before it is written, so a
	// caller can move an embedded data: image to a file and reference that instead.
	// Returning "" drops the image. nil keeps every src as is.
	ImageSrc func(src string) string
	// Logf receives degradation messages (e.g. a synthesized table header). May be
	// nil (messages discarded).
	Logf func(string, ...any)
}

// Write renders the cssbox tree rooted at root to w. root is expected to be a
// finalized tree (post box-generation fixups), so list markers and anonymous boxes are
// already resolved. A nil root writes nothing.
func Write(root *cssbox.Box, w io.Writer, opts Options) error {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	wr := &writer{opts: opts}
	if root != nil {
		wr.block(root, 0)
	}
	out := wr.finish()
	_, err := io.WriteString(w, out)
	return err
}

// writer accumulates rendered output. Blocks are emitted into blocks (each a fully
// formatted chunk with no surrounding blank lines); finish joins them with blank-line
// separators, which is how Markdown delimits block-level constructs.
type writer struct {
	opts   Options
	blocks []string
}

// emit appends a finished block chunk (trailing/leading blank lines are added by
// finish). Empty chunks are dropped so we never emit runs of blank lines.
func (w *writer) emit(chunk string) {
	chunk = strings.Trim(chunk, "\n")
	if chunk == "" {
		return
	}
	w.blocks = append(w.blocks, chunk)
}

// finish joins the accumulated blocks with a single blank line between them and a
// trailing newline, the conventional Markdown document shape.
func (w *writer) finish() string {
	if len(w.blocks) == 0 {
		return ""
	}
	return strings.Join(w.blocks, "\n\n") + "\n"
}

// block dispatches a block-level box. depth is the list-nesting depth (0 at the top),
// used to indent nested list items. A box that is not itself a recognized block
// construct recurses into its children so wrapper/anonymous boxes are transparent.
func (w *writer) block(b *cssbox.Box, depth int) {
	switch {
	case b.HeadingLvl >= 1 && b.HeadingLvl <= 6:
		w.heading(b)
	case b.SemTag == "blockquote":
		w.blockquote(b, depth)
	case b.SemTag == "pre":
		w.codeBlock(b)
	case b.SemTag == "hr":
		w.thematicBreak()
	case b.Display == cssbox.DisplayTable:
		w.table(b)
	case boxwalk.IsListContainer(b):
		w.list(b, depth)
	case b.Display == cssbox.DisplayListItem:
		// A stray list item not inside a recognized list container (rare); render it
		// as a one-item list.
		w.list(&cssbox.Box{Children: []*cssbox.Box{b}}, depth)
	case boxwalk.IsBlockContainer(b):
		// A block that holds inline content (an inline formatting context, or a
		// paragraph) becomes one paragraph; a block holding further blocks recurses.
		if boxwalk.HasInlineContent(b) {
			w.paragraph(b, depth)
		} else {
			w.children(b, depth)
		}
	default:
		w.children(b, depth)
	}
}

// children recurses over a box's block-level children (used for transparent wrappers
// like the html/body/div boxes and list containers). List containers (a box whose
// children are DisplayListItem) do not increase depth themselves; the item's own
// nesting depth is tracked by counting ancestor lists in listItem.
func (w *writer) children(b *cssbox.Box, depth int) {
	for _, c := range b.Children {
		w.block(c, depth)
	}
}

// heading emits an ATX heading ("## Text"). In plain mode the hashes are dropped.
func (w *writer) heading(b *cssbox.Box) {
	text := strings.TrimSpace(w.inlineOpt(b, true)) // "#" conveys emphasis; drop UA bold
	if text == "" {
		return
	}
	if w.opts.Plain {
		w.emit(text)
		return
	}
	w.emit(strings.Repeat("#", b.HeadingLvl) + " " + text)
}

// paragraph emits a box's inline content as one paragraph.
func (w *writer) paragraph(b *cssbox.Box, _ int) {
	w.emit(strings.TrimSpace(w.inline(b)))
}

// thematicBreak emits a horizontal rule (<hr>). In plain mode a dashed line stands in.
func (w *writer) thematicBreak() {
	w.emit("---")
}

// blockquote prefixes every line of its rendered content with "> " (recursively, so a
// nested quote deepens the prefix). In plain mode the prefix is dropped but the content
// still forms its own block.
func (w *writer) blockquote(b *cssbox.Box, depth int) {
	inner := &writer{opts: w.opts}
	inner.children2(b, depth)
	body := inner.finish()
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return
	}
	if w.opts.Plain {
		w.emit(body)
		return
	}
	var sb strings.Builder
	for i, line := range strings.Split(body, "\n") {
		if i > 0 {
			sb.WriteByte('\n')
		}
		if line == "" {
			sb.WriteString(">")
		} else {
			sb.WriteString("> " + line)
		}
	}
	w.emit(sb.String())
}

// children2 renders a box's block children into this writer, treating a blockquote's
// own inline content (a blockquote directly containing text) as a paragraph.
func (w *writer) children2(b *cssbox.Box, depth int) {
	if boxwalk.HasInlineContent(b) {
		w.paragraph(b, depth)
		return
	}
	w.children(b, depth)
}

// codeBlock emits a fenced code block from a <pre> box, preserving its text verbatim.
// In plain mode the fences are dropped.
func (w *writer) codeBlock(b *cssbox.Box) {
	text := strings.TrimRight(boxwalk.RawText(b), "\n")
	if text == "" {
		return
	}
	if w.opts.Plain {
		w.emit(text)
		return
	}
	w.emit("```\n" + text + "\n```")
}
