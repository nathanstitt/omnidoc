package markdown

import (
	"strings"

	"github.com/nathanstitt/omnidoc/pkg/internal/boxwalk"
	"github.com/nathanstitt/omnidoc/pkg/internal/layout/cssbox"
)

// inline serializes a box's inline subtree to a single line of Markdown (or plain
// text). It flattens the subtree to a stream of styled runs — each run is a maximal
// span of text sharing bold/italic/code/link state — then emits the appropriate
// markers around each run, coalescing adjacent runs that share a marker so we do not
// emit "**a****b**". Whitespace is collapsed to single spaces (the normal-flow model);
// a <pre> box is handled by codeBlock, not here.
func (w *writer) inline(b *cssbox.Box) string {
	return w.inlineOpt(b, false)
}

// inlineOpt is inline with an option to suppress bold emphasis. Callers whose context
// already implies bold — a heading (the "#" conveys it) or a table header cell — pass
// suppressBold=true so a UA-stylesheet font-weight:bold does not add stray "**" markers
// around the whole run. Italic/code/link emphasis is still honored.
func (w *writer) inlineOpt(b *cssbox.Box, suppressBold bool) string {
	var runs []boxwalk.StyledRun
	boxwalk.CollectRuns(b, boxwalk.InlineState{}, w.imageMarkup, &runs)
	runs = boxwalk.Coalesce(runs)
	var sb strings.Builder
	for _, r := range runs {
		if suppressBold {
			r.Bold = false
		}
		sb.WriteString(w.renderRun(r))
	}
	return boxwalk.CollapseSpaces(sb.String())
}

// imageMarkup renders an <img> replaced box as a Markdown image "![alt](src)". A
// missing src yields "" (dropped); a missing alt yields an empty label. The ImageSrc
// hook, when set, rewrites the src first.
func (w *writer) imageMarkup(rc *cssbox.ReplacedContent) string {
	src := rc.Attrs["src"]
	if src != "" && w.opts.ImageSrc != nil {
		src = w.opts.ImageSrc(src)
	}
	if src == "" {
		return ""
	}
	return "![" + escapeText(rc.Attrs["alt"]) + "](" + escapeURL(src) + ")"
}

// renderRun emits one styled run with its Markdown markers. In plain mode all markers
// and the link URL are dropped, leaving the text (a link renders as its label). Marker
// nesting order, outermost to innermost, is link, then strike, then italic, then bold,
// then code, which produces well-formed GFM like "[~~_**text**_~~](url)".
func (w *writer) renderRun(r boxwalk.StyledRun) string {
	if r.Literal != "" {
		if w.opts.Plain {
			return plainLiteral(r.Literal)
		}
		return r.Literal
	}
	if w.opts.Plain {
		return r.Text
	}
	s := escapeText(r.Text)
	if r.Code {
		// Inline code is verbatim (no escaping inside backticks); use the raw text.
		s = "`" + r.Text + "`"
	}
	if r.Bold {
		s = "**" + s + "**"
	}
	if r.Italic {
		s = "_" + s + "_"
	}
	if r.Strike {
		s = "~~" + s + "~~"
	}
	if r.Href != "" {
		s = "[" + s + "](" + escapeURL(r.Href) + ")"
	}
	return s
}

// mdEscape are the characters escaped in ordinary Markdown text so literal syntax
// characters do not trigger formatting. Kept intentionally small: over-escaping harms
// readability, so we escape only the markers this writer itself produces plus the ones
// that most commonly start a line-level construct.
var mdEscaper = strings.NewReplacer(
	`\`, `\\`,
	"`", "\\`",
	"*", `\*`,
	"_", `\_`,
	"[", `\[`,
	"]", `\]`,
)

// escapeText escapes Markdown metacharacters in ordinary text.
func escapeText(s string) string { return mdEscaper.Replace(s) }

// plainLiteral reduces a pre-formatted Markdown literal to plain text: an image
// "![alt](src)" becomes its alt label (or "" when empty). Other literals pass through.
func plainLiteral(lit string) string {
	if strings.HasPrefix(lit, "![") {
		if end := strings.Index(lit, "]("); end >= 0 {
			return lit[2:end]
		}
	}
	return lit
}

// escapeURL escapes the characters that would break a Markdown link destination
// (spaces and parens); other characters are left as-is so URLs stay readable.
func escapeURL(s string) string {
	r := strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29")
	return r.Replace(s)
}
