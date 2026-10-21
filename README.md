![omnidoc — any document to any other document, in pure Go](docs/assets/omnidoc-banner.svg)

[![CI](https://github.com/nathanstitt/omnidoc/actions/workflows/ci.yml/badge.svg)](https://github.com/nathanstitt/omnidoc/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/nathanstitt/omnidoc.svg)](https://pkg.go.dev/github.com/nathanstitt/omnidoc/pkg/omnidoc)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00758d?labelColor=211c17)](go.mod)
[![CGo free](https://img.shields.io/badge/CGo-none-c8401a?labelColor=211c17)](#why)
[![MIT](https://img.shields.io/badge/license-MIT-c8401a?labelColor=211c17)](LICENSE)

A pure-Go document toolkit: parse, lay out, rasterize, extract, convert, and edit
documents. It brings its own PDF interpreter and its own CSS layout engine. No
CGo, no native bindings, no copyleft.

Read and write fifteen formats, and convert between any two of them.

## Fifteen formats, in and out

Every supported format is both an input **and** an output. All 225 ordered pairs
convert (a format to itself is a deliberate `ErrSameFormat`):

> `pdf` · `docx` · `xlsx` · `pptx` · `epub` · `rtf` · `html` · `md` · `txt` · `csv` · `tsv` · `png` · `jpeg` · `webp` · `svg`

SVG is vector in **both** directions. Standalone `.svg`/`.svgz` files open as a
vector page and convert to PDF as real path operators rather than a rasterized
image; on output, any document — a PDF included — becomes real `<path>` geometry,
`<clipPath>` and `<mask>`, not a bitmap wrapped in an `<image>`. Text is written
as glyph outlines, so it renders identically everywhere but is not selectable
(each glyph carries an `aria-label` with its source characters). Like image
output, one SVG holds one page.

Two things still embed a bitmap, and only where SVG has no faithful equivalent:
a filter's rasterized input, and a gradient that cannot describe its own geometry
— which includes every gradient that arrived from a PDF `/Shading`. CSS and SVG
gradients emit native `<linearGradient>`/`<radialGradient>`. See
[docs/SVG.md](docs/SVG.md).

One format is input-only: **`heic`**. HEIF/HEIC stills decode through an in-tree
pure-Go HEVC intra decoder — no libheif, no CGo — so an iPhone photo converts to
any of the fifteen outputs and drops into an HTML or EPUB `<img>` unchanged. WebP
reads lossy VP8, lossless VP8L, and alpha; it writes lossless VP8L, so it replaces
PNG rather than JPEG.

```sh
omnidoc convert report.docx report.pdf         # typeset through the CSS engine
omnidoc convert https://example.com page.png   # fetch, lay out, rasterize
omnidoc convert statement.pdf tables.xlsx      # tables recovered from ruling lines & whitespace
omnidoc convert book.epub book.docx            # ebook → Word, images and all
omnidoc convert notes.md deck.pptx             # each heading becomes a slide
omnidoc convert photo.heic photo.webp          # iPhone photo → lossless WebP
omnidoc convert diagram.pdf diagram.svg        # PDF → SVG paths, not a bitmap
omnidoc convert -v scanned.pdf notes.md        # report what could not be converted
omnidoc rasterize input.pdf --page 1 --out page1.png --dpi 150
```

Conversions **degrade rather than fail**: an unreadable page or an unsupported
construct is skipped and the rest of the document still converts. That is only
useful if you can find out it happened, so `-v` reports each degradation to
stderr — a skipped page, a font that could not be resolved, a construct that was
dropped. It is off by default; reach for it when an output looks emptier than the
input did.

`convert` sniffs the input format from **content first** (magic bytes, OPC/zip
classification, HTML sniffing), then the extension; the output format comes from
the output extension. `--from`/`--to` override both. HTML input can also be an
`http(s)` URL, with relative resources, `data:` URIs, and web fonts resolved.
Image and SVG output write one page by default, or many with `--pages all` and a
`%d` in the output name (`page-%d.png`, `page-%d.svg`).

Thumbnails come in two shapes. `--max-width`/`--max-height` fit within a bound
without knowing page sizes up front. `--crop <gravity|saliency> --crop-size WxH`
fills an exact pixel box instead — the difference between a 720×720 square and a
720×540 fit. `--crop saliency` picks the window from image content: edge energy,
saturation, skin likelihood, and centre bias, with no model and no CGo.

**Demo:** [`testdata/htmldoc/index.html`](testdata/htmldoc/index.html) is the
rendering specimen, one document exercising every implemented HTML/CSS/image
slice. [`docs/assets/htmldoc-specimen.pdf`](docs/assets/htmldoc-specimen.pdf)
is the PDF `omnidoc convert` typesets from it: 18 Letter pages with running
headers, page counters, a WOFF2 script wordmark, floats, flexbox, grid, and
tables, all as selectable text.

## Quick start

```sh
go install github.com/nathanstitt/omnidoc/cmd/omnidoc@latest
```

Or as a library:

```go
import "github.com/nathanstitt/omnidoc/pkg/omnidoc"

// Open sniffs the format from content — PDF, DOCX, XLSX, EPUB, RTF, HTML, …
doc, err := omnidoc.Open("input.pdf")

// Rasterize a page (RasterizePages renders many pages concurrently;
// a parsed document is read-only and goroutine-safe).
img, err := doc.RasterizePage(ctx, 0, omnidoc.RasterOptions{DPI: 150})

// Or convert in one call — any input format to any output format.
err = omnidoc.ConvertFile(ctx, "report.docx", "report.pdf", omnidoc.ConvertOptions{})

// Streams work too, with explicit formats when there's no filename to sniff.
err = omnidoc.Convert(ctx, in, out, omnidoc.ConvertOptions{
    From: omnidoc.FormatPDF,
    To:   omnidoc.FormatMarkdown,
})
```

For hosts routing uploads: `OpenReader(ctx, r)` / `OpenReaderAs` accept plain
`io.Reader`s, and `FormatFromMIME` maps content types onto the capability table
(`Format.ValidInput()` is the gate). Full API reference:
[pkg.go.dev/github.com/nathanstitt/omnidoc/pkg/omnidoc](https://pkg.go.dev/github.com/nathanstitt/omnidoc/pkg/omnidoc).

## How it works

Three routes run through the code. They all meet at one format-neutral CSS box
tree and one backend-agnostic paint interface.

```text
 DOCX · HTML · Markdown · text       ┌────────────────────────────┐
 CSV/TSV · XLSX · RTF · PPTX         │  one CSS layout engine     │     render.Device
 EPUB · PNG/JPEG/HEIC · http(s) URLs │  (pkg/internal/layout/css) │     ├─ raster   → PNG · JPEG
   frontends lower to a shared  ───▶ │  blocks · inlines ·        │ ──▶ └─ pdfwrite → PDF
   box tree                          │  floats · tables ·         │        (selectable text)
   (pkg/internal/layout/cssbox)      │  flex · grid ·             │
        │                            │  paged media               │
        │                            └────────────────────────────┘
        └───▶ structure writers walk the box tree, not pixels:
              Markdown · text · HTML · DOCX · RTF · PPTX · EPUB · CSV/TSV · XLSX

 PDF ──▶ parse (pkg/pdf) ──▶ filters ──▶ interpret (pkg/internal/content) ──▶ render.Device
              └───▶ extraction (pkg/internal/extract): positioned glyphs + ruling lines
                    → reading order (XY-cut) + table recognition → the same box tree
```

- **The PDF pipeline** parses and interprets real-world PDFs itself: classic and
  stream xrefs, object streams, broken-file repair, RC4/AES-128/AES-256
  encryption, the full filter set (Flate, LZW, CCITT G3/G4, DCT, JBIG2, …),
  embedded TrueType/CFF/Type1/CID fonts, blend modes, shadings, clipping,
  inline images.
- **The reflow engine** is a from-scratch CSS 2.1+ layout engine: cascade,
  floats, absolute positioning, Appendix E stacking, both table border models,
  multi-line flexbox, grid, web fonts (WOFF1/WOFF2), CSS counters, bidirectional
  text (UAX#9) with Arabic shaping, and CSS Paged Media (`@page`, margin boxes,
  named pages, running headers/footers). DOCX lowers into this same engine rather
  than getting a bespoke renderer.
- **The structure writers** convert by walking the box tree, so a PDF's
  recovered headings and tables come out as real Markdown headings and pipe
  tables, and `pdf → xlsx` extracts spreadsheet-ready tables.

Conversions are pinned by round-trip parity tests (e.g. `html → docx → md` must
equal `html → md`), golden images, and WPT-style reftests.

## Beyond conversion: native office models

Two packages stand on their own as supported public surfaces, built for programs
that edit files rather than convert them:

- **`pkg/xlsx`** is a preservation-first spreadsheet editor. `Edit` + `Save` of an
  untouched workbook is **part-for-part byte-identical**; edits rewrite only the
  dirty XML parts, keeping unknown elements, attributes, and prefixes intact.
  Typed cell writes, style patches (patch-not-replace), conditional formats,
  comments, pivot tables, defined names, frozen panes, merges.
- **`pkg/docx`** is a full document model with a deterministic writer.
  `Parse ∘ Write` is a fixed point over both generated and real Word/LibreOffice
  corpora: tracked changes, comments, footnotes/endnotes, numbering, sections,
  drawings, and unmodeled parts all survive the round trip.

## Why?

The high-fidelity incumbents — PDFium, MuPDF, Poppler — require CGo, carry
copyleft licenses, or both. omnidoc implements the whole stack in Go instead: PDF
interpretation, font parsing, CSS layout, rasterization, OOXML. It cross-compiles
freely, builds as a single static binary, and stays MIT. The few dependencies are
pure-Go and permissively licensed (see `go.mod`); the one vendored decoder is
Apache-2.0.

Built for [tinycld](https://github.com/tinycld), where it powers document
thumbnails, text extraction, and editing of xlsx/docx with the
[calc](https://github.com/tinycld/calc) and [text](https://github.com/tinycld/text)
packages respectively.

## Limitations

Unsupported constructs degrade rather than crash. You get a skip and a debug log,
or a typed error (`ErrEncryptedNeedsPassword`, `ErrUnsupportedFormat`, …) — never
a panic, and one bad page can't kill a batch. The gaps worth knowing about:

- **Bidi/RTL is implemented but not complete.** `direction: rtl` mirrors tables,
  flex, and grid; text reorders per UAX#9 (with bracket mirroring); Arabic shapes
  with connected letterforms through the bundled Noto faces; and RTL PDFs extract
  in reading order. Remaining: OpenType mark positioning (Arabic diacritics sit on
  the baseline), nested bidi embeddings deeper than one level, and `dir="auto"`.
- **CJK text extraction from PDFs.** ToUnicode CMap parsing is pending, so
  Type0/CID text can extract as unknown runes (it still *renders* correctly).
- **No OCR.** Scanned PDFs rasterize fine but extract no text.
- Grid lacks named-line placement and subgrid; JPEG2000 images and PDF tiling
  patterns are skipped; password-protected PDFs open only with an empty user
  password.
- **Pagination splits blocks, tables, and lines, but not flex or grid items.** A
  flex or grid item taller than a page moves whole rather than fragmenting, because
  those size their items collectively and would need re-solving per page.
- **HEIC is stills-only and read-only.** HEIF image *sequences* (`msf1`) and
  AVIF are refused with a typed error, and nothing writes HEIC. Decoding is
  intra-only 4:2:0 at 8/10-bit (Main/Main 10/Main Still); P/B slices, range
  extensions, and >10-bit are rejected rather than approximated.
- **WebP is stills-only, and writes lossless.** Animated WebP is refused with a
  typed `ErrAnimatedImage`. Output is VP8L, since no pure-Go lossy VP8 encoder
  exists — expect PNG-sized files, not JPEG-sized ones.

The complete feature inventory lives in [FEATURES.md](FEATURES.md).

## Layout of the codebase

| Area | Packages | Responsibility |
|------|----------|----------------|
| PDF | `pkg/pdf`, `pkg/internal/filter`, `pkg/internal/content`, `pkg/internal/extract` | Parse, decode streams, interpret content, recover structure |
| Frontends | `pkg/internal/html` + `pkg/internal/css`, `pkg/docx`, `pkg/xlsx`, `pkg/internal/pptx`, `pkg/internal/epub`, `pkg/internal/rtf`, `pkg/internal/markdown` | Parse each format, lower to the shared box tree |
| Layout | `pkg/internal/layout/cssbox`, `pkg/internal/layout/css`, `pkg/internal/layout/inline` | The box model, the CSS engine, shaping & line breaking |
| Fonts | `pkg/internal/font`, `pkg/internal/layout/font` | SFNT/WOFF/WOFF2 parsing, system + bundled resolution, per-rune script fallback |
| Backends | `pkg/internal/{raster,pdfwrite,svgwrite}`, `pkg/internal/{markdownwrite,htmlwrite,docxwrite,rtfwrite,pptxwrite,epubwrite,csvwrite,xlsxwrite}` | Pixels, PDFs, SVG, and structure output |
| API / CLI | `pkg/omnidoc`, `cmd/omnidoc` | Public entry points, format detection, the conversion matrix |

**Only seven packages are importable**: `pkg/omnidoc`, `pkg/docx`, `pkg/xlsx`,
`pkg/pdf`, `pkg/crop`, `pkg/heif`, `pkg/resource`. Everything under
`pkg/internal/` is engine detail — the table describes how the work is laid out,
not an API surface.

That split is deliberate and predates the tag, because a tag freezes every
exported package under semver. The CSS engine's `ComputedStyle` is the clearest
case: its own doc says the typed property set is "deliberately minimal" and that
properties outside it "do not yet populate a typed computed field", which is a
written promise that the type will change. Anything here can be promoted to
public later, additively; nothing can be withdrawn.

The `render.Device` interface is the seam. Parsing, interpretation, and layout
never know which backend they're painting into, so a new backend bolts on without
touching them.

That describes the internal architecture; it is **not** an extension point, and
`pkg/internal/render` is deliberately not importable. Every implementer lives in
this repo, and no public function accepts a caller-supplied `Device` — so making
the interface public would freeze 69 symbols, including a 15-method interface
that has already changed shape twice (`EndGroup`'s own doc records one such
break, and `RenderOffscreen` and `BuildLuminanceMask` were added later). A 16th
method after v1 would mean v2.

Opening it later is additive and always possible; withdrawing it after a tag is
not. If you want to write a backend, say so — the constraint is a choice about
what to promise, not a technical one.

## Testing

Fidelity claims are only worth what the corpus behind them proves:

- **Generated fixtures.** Test PDFs, DOCX, and XLSX are built deterministically
  in `testdata/gen` (readable Go, not opaque blobs). Materialize them with
  `go run ./cmd/dumpfixtures`.
- **Golden images.** Rendered pages are compared to committed PNGs with a
  per-pixel tolerance; every intentional change is regenerated and eyeballed.
- **Real-world corpus.** `testdata/external/` holds third-party PDFs, DOCX, and
  XLSX files that must parse, render, convert, and (for the editors) round-trip
  byte-identically.
- **Round-trip parity.** Structure writers are pinned so converting through an
  intermediate format equals converting directly.

```sh
make build   # build the CLI
make test    # go test ./... (race detector on)
make lint    # go vet + golangci-lint
```

## Contributing and security

Contributions are welcome — [CONTRIBUTING.md](CONTRIBUTING.md) covers the two hard
constraints (pure Go, and a very short MIT-compatible dependency list) and what a
change is expected to land with. Known-open work is in
[docs/BACKLOG.md](docs/BACKLOG.md); [docs/SCOPE.md](docs/SCOPE.md) records what is
deliberately excluded.

This library parses untrusted documents, so a crash, hang, or unbounded allocation
from a malformed file is treated as a vulnerability. Report those privately —
[SECURITY.md](SECURITY.md) has the process and the current threat model.

Release-to-release changes are in [CHANGELOG.md](CHANGELOG.md).

## License

MIT — see [LICENSE](LICENSE).

Everything compiled into the module is MIT-compatible.

**Bundled fonts are embedded in the binary** (`go:embed`) and ship with anything
built from it. All are permissively licensed and may be redistributed:

- TeX Gyre Heros and TeX Gyre Termes — GUST Font License (an LPPL instance).
- Inconsolata, Noto Sans Hebrew, and Noto Naskh Arabic — SIL Open Font License
  1.1. None declares a Reserved Font Name, so they ship under their original
  names. See the `LICENSE-*.txt` files in
  [`pkg/internal/font/standard/fonts/`](pkg/internal/font/standard/fonts/).

Two further carve-outs, both isolated:

- [`pkg/internal/filter/jbig2/`](pkg/internal/filter/jbig2/) vendors
  [xiaoqidun/jbig2](https://github.com/xiaoqidun/jbig2) (pure-Go JBIG2 decoding,
  **Apache-2.0**, MIT-compatible) with its upstream `LICENSE` and `NOTICE`. It
  IS compiled into the library — it is the PDF `JBIG2Decode` filter — so a
  binary built from omnidoc carries that Apache-2.0 code and its `NOTICE`.
- [`testdata/external/`](testdata/external/) holds third-party **test inputs
  only**, never compiled or shipped, under their own licenses: PDFs (CC-BY-SA-4.0, from
  [py-pdf/sample-files](https://github.com/py-pdf/sample-files)) and DOCX/XLSX
  files (Apache-2.0 / MPL-2.0 / MIT, from Apache POI, LibreOffice, and
  Open-XML-SDK). Each directory's README carries per-file provenance.
