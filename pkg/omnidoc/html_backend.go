package omnidoc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/nathanstitt/omnidoc/pkg/internal/css"
	"github.com/nathanstitt/omnidoc/pkg/internal/html"
	layoutcss "github.com/nathanstitt/omnidoc/pkg/internal/layout/css"
	layoutfont "github.com/nathanstitt/omnidoc/pkg/internal/layout/font"
	mdfront "github.com/nathanstitt/omnidoc/pkg/internal/markdown"
	"github.com/nathanstitt/omnidoc/pkg/resource"
)

// HTMLOption is a backward-compatible alias for OpenOption. HTML-layout options
// (WithPageSize, WithViewportWidth, ...) are a subset of the universal open
// options, so the two are interchangeable; new code should prefer OpenOption.
type HTMLOption = OpenOption

// openConfig is the resolved open/input configuration shared by every reflow
// frontend: input-selection concerns (which XLSX sheets) alongside the reflow
// layout and resource-loading knobs. A frontend reads only the fields that apply
// to it and ignores the rest.
type openConfig struct {
	viewportPt   float64
	pageHeightPt float64
	// paged requests pagination using the document's @page rules (set by
	// WithDefaultPaged or WithPageSize). explicitSize records that WithPageSize set an
	// explicit page size, which overrides any @page `size`.
	paged        bool
	explicitSize bool
	loader       resource.ResourceLoader
	sys          layoutfont.SystemFontProvider
	// bundledFonts selects hermetic bundled-font mode (no OS system-font lookup). Default
	// false = system mode. Set by WithBundledFonts.
	bundledFonts bool
	// fallbackFontDir is a directory whose fonts join per-character fallback. Set
	// by WithFallbackFontDir.
	fallbackFontDir string
	logf            func(string, ...any)
	// media is the cascade media context. It defaults to css.MediaScreen (the
	// interactive/HTML render); WithPrintMedia switches it to css.MediaPrint so
	// @media print rules apply (used for PDF output).
	media css.Media
	// ctx bounds open-time box generation, resource loading, and layout. It
	// defaults to context.Background() and is set (unexported) by the
	// ctx-taking entry points via withOpenContext.
	ctx context.Context
	// sheets, when non-nil, restricts an XLSX input to the named visible sheets
	// (in the given order); it is read and consumed by the XLSX frontend before
	// HTML generation and is inert for every other frontend. Set by WithSheets.
	// A nil slice means "all visible sheets" (the default); a non-nil empty
	// slice is not producible through WithSheets.
	sheets []string
}

// defaultViewportPt is the default layout viewport width in points (px:pt 1:1).
// A fixed desktop width so a page lays out as a full-page capture (the single
// tall image model); content flows to whatever height it needs.
const defaultViewportPt = 1280

// defaultOpenConfig returns the baseline configuration before options are
// applied: the default viewport width, no loader (links are skipped), and a
// no-op logger.
func defaultOpenConfig() openConfig {
	return openConfig{viewportPt: defaultViewportPt, loader: nil, logf: nil, media: css.MediaScreen, ctx: context.Background()}
}

// WithContext bounds open-time work -- box generation, resource loading, and
// layout -- with a caller's context. A nil ctx is ignored and the default
// (context.Background()) stands.
//
// It applies to every Open* entry point that does layout work, because they all
// resolve their options through the same config; for OpenURL it also bounds the
// HTTP fetch. The parse step of an opened PDF or SVG is not interruptible; ctx
// takes effect from rasterization onward for those.
//
// The path-taking openers (Open, OpenHTMLFile, ...) prepend their own defaults
// before applying opts, so a caller's WithContext always wins.
//
//	doc, err := omnidoc.OpenBytes(data, omnidoc.WithContext(ctx))
func WithContext(ctx context.Context) OpenOption { return withOpenContext(ctx) }

// withOpenContext threads a caller's context into open-time layout and
// resource loading. It backs the exported [WithContext]; the ctx-taking entry
// points (OpenReader, OpenReaderAs, Convert) prepend it. A nil ctx is ignored.
func withOpenContext(ctx context.Context) HTMLOption {
	return func(c *openConfig) {
		if ctx != nil {
			c.ctx = ctx
		}
	}
}

// WithViewportWidth sets the layout viewport width in CSS pixels (treated 1:1 as
// points). Defaults to 1280. Values <= 0 are ignored.
func WithViewportWidth(px float64) HTMLOption {
	return func(c *openConfig) {
		if px > 0 {
			c.viewportPt = px
		}
	}
}

// LetterWidthPt / LetterHeightPt are US-Letter (8.5in × 11in) at 96dpi (px:pt 1:1),
// the conventional default page size for WithPageSize.
const (
	LetterWidthPt  = 816
	LetterHeightPt = 1056
)

// WithPageSize paginates output into fixed widthPt × heightPt (points) pages: the
// document lays out at widthPt and is sliced into heightPt-tall pages, breaking
// between top-level blocks and at forced page breaks (CSS break-before/after: page),
// honoring break-inside / widows / orphans. The explicit size overrides any @page
// `size`, but @page margins and margin boxes (running headers/footers) still apply.
// Without WithPageSize (or WithDefaultPaged) the document renders as a single tall
// page (the default). widthPt or heightPt <= 0 is ignored (no pagination).
func WithPageSize(widthPt, heightPt float64) HTMLOption {
	return func(c *openConfig) {
		if widthPt > 0 && heightPt > 0 {
			c.viewportPt = widthPt
			c.pageHeightPt = heightPt
			c.paged = true
			c.explicitSize = true
		}
	}
}

// WithDefaultPaged paginates output using the document's @page rules (size, margins,
// and margin boxes) when present, falling back to US-Letter (LetterWidthPt ×
// LetterHeightPt) for any dimension the document does not specify. Unlike WithPageSize
// it sets no explicit size, so an @page `size` is honored. Without it (and without
// WithPageSize) the document renders as a single tall page.
func WithDefaultPaged() HTMLOption {
	return func(c *openConfig) {
		c.paged = true
		c.explicitSize = false
		if c.pageHeightPt <= 0 {
			c.pageHeightPt = LetterHeightPt
		}
		c.viewportPt = LetterWidthPt
	}
}

// WithResourceLoader sets the loader used to resolve <link> stylesheet refs (and,
// later, images/fonts). Defaults to no loader (links are skipped). OpenHTMLFile
// supplies a DirLoader rooted at the document's directory.
func WithResourceLoader(l resource.ResourceLoader) HTMLOption {
	return func(c *openConfig) { c.loader = l }
}

// WithSystemFontProvider sets the provider used to resolve @font-face local()
// sources. Defaults to nil (local() never matches; the next src is tried). OpenHTMLFile
// supplies a DirFontProvider rooted at the document's directory.
func WithSystemFontProvider(p SystemFontProvider) HTMLOption {
	return func(c *openConfig) { c.sys = p }
}

// WithBundledFonts selects hermetic bundled-font mode: non-embedded families resolve
// only from the bundled substitutes, never the host's installed OS fonts. The default
// (without this option) is system mode, which uses installed OS fonts and falls back to
// the bundled substitutes when none match. The golden/reference tests use this option
// for reproducibility.
func WithBundledFonts() HTMLOption {
	return func(c *openConfig) { c.bundledFonts = true }
}

// WithFallbackFontDir adds every font file in dir (.ttf, .otf, .woff, .woff2)
// to per-character fallback: a character no family in the font-family list and
// no bundled face can draw is looked up in these fonts, in file-name order,
// before any installed font. Unlike WithSystemFontProvider it leaves installed
// fonts on, matches by glyph rather than by family name, and also applies in
// bundled mode (the caller chose these files). Font collections (.ttc, .otc)
// are skipped with a log. "" is ignored.
func WithFallbackFontDir(dir string) HTMLOption {
	return func(c *openConfig) { c.fallbackFontDir = dir }
}

// WithLogf sets a logger for layout/degradation diagnostics (may be called during
// Build and Layout). Defaults to a no-op.
func WithLogf(f func(string, ...any)) HTMLOption {
	return func(c *openConfig) { c.logf = f }
}

// WithSheets restricts an XLSX input to the named worksheets, rendered in the
// order given, instead of the default (every visible sheet). Names match a
// sheet's tab name exactly (case-sensitive); a name that no sheet carries makes
// OpenXLSX* fail with an error wrapping ErrSheetNotFound, so a typo does not
// silently yield an empty or wrong document. Selecting a single sheet suppresses
// the per-sheet name heading, exactly as a naturally single-sheet workbook does.
// Hidden sheets can be named explicitly and will render. The option is inert for
// every non-XLSX input. Calling it with no names is a no-op (keeps the default).
func WithSheets(names ...string) OpenOption {
	return func(c *openConfig) {
		if len(names) > 0 {
			c.sheets = names
		}
	}
}

// WithPrintMedia makes box generation honor @media print rules (and exclude
// screen-only rules) — the print media context, used for PDF output. Without it the
// cascade uses the screen context (the default). @media all and top-level rules
// apply in both, so a document with no @media blocks is unaffected.
func WithPrintMedia() HTMLOption {
	return func(c *openConfig) { c.media = css.MediaPrint }
}

// OpenHTMLFile reads and renders an HTML file at path, applying any options, and
// returns a Document ready to rasterize. Like OpenHTMLFile it roots a DirLoader and a
// DirFontProvider at the file's directory so relative <link>/<img>/@font-face refs
// resolve from disk; the caller's opts are applied AFTER those defaults, so e.g.
// WithPageSize(LetterWidthPt, LetterHeightPt) paginates the file, and a caller's own
// WithResourceLoader overrides the directory loader.
//
// WithContext bounds box generation, sub-resource loading, and layout. The
// os.ReadFile of path itself is not interruptible (the standard library offers
// no ctx-taking form), so the context takes effect from parsing onward — which
// is where a pathological document spends its time anyway.
func OpenHTMLFile(path string, opts ...HTMLOption) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("omnidoc: open html %q: %w", path, err)
	}
	dir := filepath.Dir(path)
	all := append([]HTMLOption{
		WithResourceLoader(resource.DirLoader{Base: dir}),
		WithSystemFontProvider(DirFontProvider{Dir: dir}),
	}, opts...)
	return OpenHTMLBytes(data, all...)
}

// ErrUnsupportedScheme is returned (wrapped) by OpenURL when rawURL uses a scheme
// other than http or https, so callers can branch on it via errors.Is.
var ErrUnsupportedScheme = errors.New("unsupported URL scheme")

// OpenURL fetches the HTML document at rawURL over HTTP(S), lays it out at the
// default viewport width into a single tall page, and returns a Document ready to
// rasterize. Relative <link>/<img>/@font-face refs resolve against rawURL and are
// fetched over HTTP (data: sub-resource refs are decoded inline) through an
// HTTPLoader rooted at rawURL; rawURL itself must be http or https (a non-http(s)
// scheme returns ErrUnsupportedScheme). Options (e.g. WithViewportWidth, WithLogf,
// WithSystemFontProvider) may be supplied and take effect after the loader is set.
// Unlike OpenHTMLFile, no system font provider is configured by default (a URL has no
// local font directory), so @font-face local() sources do not match unless one is
// supplied.
//
// WithContext bounds BOTH halves of the fetch-then-render: the context is passed
// to the HTTP fetch of rawURL itself (so a server that never responds is a
// deadline, not a hang) and then threaded into box generation, sub-resource
// loading, and layout. Bound it for untrusted URLs — a remote host controls both
// how slowly it answers and how pathological the document it returns is.
func OpenURL(rawURL string, opts ...HTMLOption) (*Document, error) {
	// The context is resolved here as well as downstream, because the HTTP
	// fetch happens before any frontend sees the options.
	cfg := defaultOpenConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	ctx := cfg.ctx
	if rawURL == "" {
		return nil, fmt.Errorf("omnidoc: open url: empty URL")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("omnidoc: open url %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("omnidoc: open url %q: %w (%q)", rawURL, ErrUnsupportedScheme, u.Scheme)
	}
	loader := resource.HTTPLoader{Base: u}
	data, _, err := loader.Load(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("omnidoc: open url %q: %w", rawURL, err)
	}
	allOpts := append([]HTMLOption{WithResourceLoader(loader)}, opts...)
	return OpenHTMLBytes(data, allOpts...)
}

// OpenHTMLBytes parses and renders in-memory HTML, applying any options, and
// returns a Document ready to rasterize. Without WithContext it lays out under
// context.Background() and cannot be cancelled or time-bounded; pass WithContext
// when a pathological document must not be able to wedge the calling goroutine.
// A document whose layout runs long can then be abandoned by cancelling the
// context or giving it a deadline; the open fails with an error wrapping
// context.Canceled / context.DeadlineExceeded rather than returning a silently
// truncated document (the layout engine degrades to partial output, and
// htmlDocument converts that into a hard error at the open boundary).
func OpenHTMLBytes(data []byte, opts ...HTMLOption) (*Document, error) {
	cfg := defaultOpenConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	return htmlDocument(data, cfg)
}

// htmlDocument runs the HTML pipeline — parse → box generation → CSS layout —
// and wraps the resulting pages for rasterization, mirroring docxDocument. Layout
// runs once here (the document lays out into a single tall page); rasterization
// then proceeds over that page.
func htmlDocument(data []byte, cfg openConfig) (*Document, error) {
	doc, err := html.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("omnidoc: parse html: %w", publicNestingErr(err))
	}
	ctx := cfg.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	root, fontFaces, pageRules, running, err := layoutcss.BuildWithFontsPagesRunningMedia(ctx, doc, cfg.loader, cfg.media, cfg.logf)
	if err != nil {
		return nil, fmt.Errorf("omnidoc: build html boxes: %w", err)
	}
	// System mode (the default): if the caller did not supply their own font provider,
	// install an OSFontProvider so installed OS fonts resolve non-embedded families
	// (falling back to the bundled substitutes when none match). Bundled mode leaves
	// sys as-is, so resolveProvider finds no pkgfont.Provider and uses the bundle only.
	sys := cfg.sys
	if !cfg.bundledFonts && sys == nil {
		sys = layoutfont.NewOSFontProviderWithLogf(cfg.logf)
	}
	faces := layoutfont.NewFaceCacheWithFonts(fontFaces, cfg.loader, sys, cfg.logf)
	if cfg.fallbackFontDir != "" {
		faces.SetFallbackDir(cfg.fallbackFontDir)
	}
	engine := layoutcss.New(faces, cfg.loader, cfg.logf)
	// The page's author CSS cascades into inline <svg> content. pkg/internal/svg re-parses an
	// inline <svg> from the markup pkg/internal/html serialized, so it never sees the host
	// document's sheets unless they are handed over explicitly.
	engine.SetAuthorSheets(doc.AuthorSheets)
	pages, err := engine.LayoutPagedDoc(ctx, root, layoutcss.PagedConfig{
		Paged:        cfg.paged,
		FallbackW:    cfg.viewportPt,
		FallbackH:    cfg.pageHeightPt,
		ExplicitSize: cfg.explicitSize,
		Pages:        pageRules,
		Running:      running,
	})
	if err != nil {
		return nil, fmt.Errorf("omnidoc: layout html: %w", err)
	}
	// The layout engine degrades on cancellation (it stops adding content and
	// returns what it has, keeping partial output renderable for its other
	// callers). At the open boundary that would silently hand the caller a
	// truncated document, so convert a cancelled ctx into a hard error here.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("omnidoc: open html: %w", err)
	}
	return &Document{r: &reflowRenderer{pages: pages, root: root, loader: cfg.loader}, format: FormatHTML}, nil
}

// publicNestingErr maps the internal parsers' nesting sentinels onto the
// exported ErrTooDeeplyNested, so a caller can branch on one name without
// reaching into pkg/internal. The original error stays in the chain for its
// message and the depth it reports.
func publicNestingErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, html.ErrTooDeeplyNested) || errors.Is(err, mdfront.ErrTooDeeplyNested) {
		return fmt.Errorf("%w: %w", ErrTooDeeplyNested, err)
	}
	return err
}
