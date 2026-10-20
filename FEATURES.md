# Features

Everything that has shipped, validated against the real-world corpus in
`testdata/external/`. Every feature that lands gets a bullet here in the same PR — each one a
pointer, with the design and rationale left to the commit and PR history.

What is *not* done yet, and the known approximations, live in the per-subsystem docs:
[docs/PDF.md](docs/PDF.md), [docs/DOCX.md](docs/DOCX.md),
[docs/CSS-LAYOUT.md](docs/CSS-LAYOUT.md), and [docs/SVG.md](docs/SVG.md).

**PDF pipeline** (covered by `gen.Core` fixtures + golden images):

- **Parsing**: classic xref tables, xref streams (`/Type /XRef`), and object streams (`/ObjStm`).
  A broken `startxref` falls back to an object-scan rebuild.
- **Bounded against hostile input** (`FuzzParse` in `pkg/pdf`): object nesting is capped at 256
  (both `parseArray` and `parseDictOrStream`, which recurse through each other); the page-tree walk
  marks visited object numbers, so a node whose `Kids` point back at an earlier node cannot fan out
  — that is exponential *below* the depth cap, and produced 67 million pages from a 1.7 KB file;
  an object stream's `/N` is checked against what its data can hold before sizing a slice, and a
  stream whose `/N` refers back into itself is refused rather than recursing through `Document`;
  a stream `/Length` is bounds-checked without integer overflow; and a decoded stream is capped at
  `filter.MaxDecodedSize` (512 MB) *during* decompression, so a flate bomb never allocates its
  output. These matter beyond fidelity: a stack overflow and an OOM are raised through
  `runtime.throw`, which `recover()` cannot catch, so the per-page recover guarantee depends on
  them. Every one was found by fuzzing, not by reading the code.
- **Every raster allocation is bounded** (`maxPixels`, ~134M px): the page canvas and every image
  decoded onto it both take their dimensions from the file, so both are capped. The page path can
  compare in `float64` (which cannot wrap); the image path compares by **division**, because its
  row arithmetic (`w*nComps*bpc`, `rowBytes*h`) is integer and a large `/Height` wraps the product
  negative — sliding past a naive size check and panicking inside `image.NewRGBA`. A refused image
  is logged and skipped; the rest of the page still renders.
- **A blank page and an unreadable one are different values** (`Page.ContentBytes`): a page with no
  `/Contents` (or an empty array) is a blank page — no bytes, no error, which is legal and common.
  A `/Contents` that is *present but does not resolve to a stream* is an error. They used to be the
  same `(nil, nil)`, which is how a broken PDF converted to a zero-byte file with a nil error and no
  diagnostic. Structure-extraction failures now reach the caller's `WithLogf` too — the PDF frontend
  previously discarded the open options, so the logger it was handed was never read.
- **Encryption** (`pkg/pdf/crypt.go`): Standard Security Handler with an empty user password, over
  RC4 (V1/V2), AES-128 (V4/AESV2), and AES-256 (V5/R6/AESV3). A doc with a real password returns
  `ErrEncryptedNeedsPassword`; an unsupported handler returns `ErrEncrypted`.
- **Filters**: Flate, LZW, ASCIIHex, ASCII85, and RunLength, all with PNG/TIFF predictors, plus
  CCITTFax (Group 4 / Group 3 1D+2D) and DCTDecode (JPEG). JBIG2 runs through a vendored pure-Go
  Apache-2.0 decoder in `pkg/internal/filter/jbig2`, wired at `decodeImageXObject`. JPX/JPEG2000 is
  pending and returns `ErrUnsupported` — no viable pure-Go decoder exists.
- **Content interpreter** (`pkg/internal/content`): path construction and painting, graphics state,
  device color, Separation/DeviceN spot color through the tint-transform `/Function`, clipping, the
  text operators including text render modes, and `Do` XObjects.
- **Fills**: nonzero and even-odd winding. The even-odd rasterizer is hand-rolled and dep-free.
- **Strokes** (`pkg/internal/raster/stroke.go`): joins (miter/round/bevel, with limit), caps, dashes.
- **Form XObjects**: recursion with `/Matrix`, scoped `/Resources`, a depth guard, and the mandatory
  `/BBox` clip.
- **Fonts** (`github.com/benoitkugler/textlayout`): embedded TrueType (FontFile2), CFF/Type1C
  (FontFile3), classic Type1 (FontFile, eexec), Type0/CIDFont (Identity-H/V), and symbolic subset
  TrueType. **A malformed font program cannot take the process down**: the upstream parser indexes
  its tables without checking, so a self-inconsistent cmap subtable slices at a negative bound and
  panics (found by fuzzing). The parse is recovered and reported as a load failure — the same guard
  this package already applied to `FontHExtents`/`FontVExtents`, extended to the entry point. It
  matters because a font program is untrusted document input, embedded in a PDF or fetched as a web
  font, and the panic fires during OPEN, before any per-page recover exists; a caller now falls back
  to a substitute face, which is what an unreadable font should do anyway. Non-embedded base-14 faces come from bundled substitutes (`pkg/internal/font/standard`: TeX Gyre
  Heros/Termes, Inconsolata) in **regular/bold/italic/bold-italic** variants, chosen from the
  `/BaseFont` name plus descriptor `/Flags` in PDF, or from the computed `Style` in reflow.
  **Installed system fonts are the DEFAULT** source for non-embedded fonts. An `OSFontProvider`
  (`pkg/internal/layout/font`) resolves them via `adrg/sysfont`, live-scanning the OS font dirs including
  macOS `.ttc` collections, and falls through to the bundled substitutes. Hermetic **bundled-only**
  mode is an opt-out: `--bundled-fonts` on the CLI, or `RasterOptions.BundledFonts` /
  `PDFOptions.BundledFonts` / `WithBundledFonts()` in the library. The golden tests pin it. An
  explicit `RasterOptions.FontProvider` (or reflow `WithSystemFontProvider`) still overrides both.
  Those seams are typed with the **public `omnidoc.FontProvider` and `omnidoc.SystemFontProvider`
  interfaces** (one method each: `LoadStyled(family, bold, italic)` and `LoadLocal(name)`), so an
  application can implement them, and **`omnidoc.DirFontProvider{Dir}`** is the ready-made
  implementation over a directory of font files — the same resolver the file-based openers install
  beside a document. `SVGOptions.FontProvider` takes the same type.
  Each system match is **verified against the face's own `name` table** (`Face.FamilyName`) and
  rejected when that names a different family. `sysfont.Match` never reports a miss, so without the
  check a request for an absent family came back as some unrelated installed font (measured: `DejaVu
  Sans` → Lucida Grande; `Roboto`, `IBM Plex Mono` and a nonexistent name → the same Arial Unicode
  bytes). A declared name may extend the request with style words — `Barlow Condensed SemiBold`
  satisfies `Barlow Condensed` — but a merely-shared prefix does not (`Times New Roman` ≠ `Times`).
  A face that declares no readable family is accepted rather than rejected. **This does not find
  fonts the matcher cannot identify at all.** It identifies installed files by filename against a
  fixed registry, so an unregistered family (Roboto, Barlow, IBM Plex on many hosts) stays unfound;
  `@font-face` with `url()` is the reliable route for non-standard families.
- **Font-family terminal fallback** (`pkg/internal/layout/font/cache.go`): when **no** candidate in a
  `font-family` list resolves, the engine degrades to the bundled serif and logs once per
  (list, style). Previously it resolved to nothing and the caller skipped the run, which rendered a
  page whose every family was unavailable as an empty box. The fallback is style-aware — bold and
  italic select the matching bundled face. A list ending in a generic keyword is unaffected;
  showcase §29.
- **Transparency**: ExtGState alpha `/ca`/`/CA`, plus all PDF blend modes, separable and
  non-separable, via `/BM` (`pkg/internal/raster/blend.go`).
- **Shadings** (`pkg/internal/raster/shading.go`, `render.Shader`): axial, radial and function-based
  shadings via `sh`; shading patterns (PatternType 2) via `scn`; and mesh shadings (Types 4–7,
  `shading_mesh.go`), where Coons and tensor patches are tessellated with a bilinear-corner
  approximation. Tiling patterns (PatternType 1) are pending. Axial and radial shadings are a 1-D
  function of the parametric value, so their colours are memoized in a 4096-entry table over the
  `/Domain` on first paint: the `/Function` runs once per table entry rather than once per pixel.
  The table quantizes the parameter, bounded by test to at most 1/255 on any channel — inside the
  golden suites' own tolerance.
- **Images** (`pkg/internal/raster/image.go`): DeviceGray/RGB/CMYK/Indexed/ICCBased at 1–16 bpc,
  baseline JPEG, `/SMask` alpha, `/ImageMask` stencils, `/Decode` arrays on both the raw and DCT
  paths, and inline images (`BI`/`ID`/`EI`).
- **Page geometry**: `/Rotate` (0/90/180/270), MediaBox/CropBox.
- **Concurrency**: a `GOMAXPROCS`-bounded worker pool, with a per-page recover so one bad page can't
  kill a batch. Crafted-PDF panic sites are guarded directly.

**Reflow engine (HTML + DOCX)** — shared CSS layout engine (`pkg/internal/layout/css`), covered by
`html-*` / `docx-*` / `htmldoc-*` goldens, WPT-style reftests, and per-algorithm unit suites. Each
bullet's design rationale is in its PR:

- **CSS parse + cascade** (`pkg/internal/css`): dependency-free tokenizer and parser, selector matching with
  specificity, the full cascade (specificity, source order, inheritance, `!important`, inline
  `style`, origins), and shorthand expansion.
- **Custom properties + `var()`** (CSS Variables 1; `pkg/internal/css/customprop.go`, `varsubst.go`):
  `--*` properties cascade by the normal rules and INHERIT. They are stored as raw token streams,
  the same treatment `filter` already gets, and substituted at computed-value time. Substitution
  runs between the cascade and value parsing, so `var()` works in every property, shorthands
  included: `border: var(--rule)` expands normally once substituted. Fallbacks (`var(--x, blue)`),
  nested fallbacks, recursive substitution (`--a: var(--b)`), and the case-sensitivity rule that
  keeps `--Foo` and `--foo` distinct all work. Cycles are detected exactly, via an active-reference
  set rather than a depth guess; a depth cap bounds non-cyclic exponential fan-out. An unresolvable
  reference is **invalid at computed-value time**, not a dropped declaration: per spec the property
  falls back to its inherited-or-initial value as though `unset` were specified, rather than leaving
  an earlier declaration showing. This is the one case where the engine must NOT treat a bad value
  as "keep the previous one". `:root` landed here too (`pkg/internal/css/selector.go`), since that is where a
  palette normally gets declared.
- **HTML frontend — box generation** (`pkg/internal/html`, `pkg/internal/layout/cssbox`): owned DOM, UA stylesheet,
  anonymous-box fixups, whitespace collapsing, and `display:none` pruning. `<link>` resolves through
  `pkg/resource.ResourceLoader`.
- **Block + inline normal flow** (`pkg/internal/layout/inline`, `pkg/internal/layout/css/block.go`+`inline.go`,
  `pkg/internal/layout/paint`, `OpenHTMLFile`/`OpenHTMLBytes`): the box model — width/`auto`/%, `box-sizing`,
  min/max, margins including vertical collapsing, padding, borders, backgrounds — plus the IFC
  (shaping and breaking, `text-align`, `line-height`) and the fragment tree. **Horizontal `auto`
  margins** resolve per CSS 2.1 §10.3.3 (`resolveAutoMarginsX`), so `margin: 0 auto` centres a
  fixed-width block: two auto margins split the leftover evenly, one absorbs all of it and pushes
  the box to the opposite edge, and the leftover is measured from the BORDER box. An over-wide box
  clamps to a zero margin and overflows right rather than being dragged off the left edge. Vertical
  auto margins stay 0 in normal flow (§10.6.3); flex and grid resolve their own. Showcase §44.
  **Margin collapsing
  degrades honestly:** collapsing is pairwise between adjacent siblings and through parent/first-
  and last-child, but an EMPTY block's own top and bottom margins do not collapse THROUGH it
  (CSS 2.1 §8.3.1), so an empty `<div style="margin:40px 0">` between two paragraphs opens a gap up
  to twice a browser's. That now logs once per layout rather than diverging silently.
- **Replaced content + images** (`pkg/internal/layout/css/image.go`+`replaced.go`): `<img>` decodes through
  the stdlib for PNG/JPEG/GIF, `pkg/heif` for HEIC, and `pkg/internal/webp` for WebP, then goes through CSS
  replaced-sizing and paints via `DrawImage`, honoring `object-fit`/`object-position`.
- **Floats + clear** (`pkg/internal/layout/css/floats.go`): a per-BFC float context, narrowing and wrapping,
  `clear`, and a paint layer of its own.
- **Positioning** (`pkg/internal/layout/css/positioning.go`): relative, as a paint-time offset;
  absolute/fixed, out-of-flow and resolved in two passes against the containing block; and stacking
  contexts. **Degrades honestly:** `position: relative` on a NON-REPLACED INLINE box moves nothing —
  an inline generates no fragment, so there is nothing to carry the offset — and a non-zero offset
  now logs once per layout instead of vanishing silently. A zero/absent offset stays quiet, since
  that is the establish-a-containing-block idiom and it works. Block-level relative is exact.
- **Overflow clipping** (`pkg/internal/css` `overflow`, `layout.ClipPushKind`/`ClipPopKind`): clipping to the padding
  box, BFC establishment, and deferred float interactions. All four clip keywords are honored —
  `hidden`/`scroll`/`auto`/**`clip`**, where `clip` differs only in forbidding programmatic
  scrolling and allowing `overflow-clip-margin`, neither of which exists in the single-tall-page
  model. So are **`overflow-x`/`overflow-y`** and the **two-value shorthand**: they fold onto the
  one clip flag this engine models, with the *clipping* keyword winning when the axes disagree. That
  deliberately over-clips `visible hidden`, because dropping the clip is the worse error.
  Showcase §31.
- **`max-height`/`min-height` on auto-height blocks** (`pkg/internal/layout/css/block.go` `clampAutoHeight`):
  `max-height` previously applied only on the fixed-height path, so `max-height` *without* `height`
  never bounded anything and a clip built from that height clipped nothing. It now clamps the auto
  height after float enclosure and before the clip rect, so box, clip, and parent advance agree.
  `min-height` applies after `max-height` per CSS 10.7. Anonymous boxes are exempt: they copy the
  parent's computed style for inherited text properties but have no properties of their own
  (CSS 9.2.1.1), and clamping them truncated boxes the author never sized.
- **Full z-index stacking** (`pkg/internal/layout/css/fragment.go`): the Appendix E bands — negative-z behind
  in-flow, then auto/0 in doc order, then positive — plus relative clip-escape (sub-project 6b).
- **CSS 2.1 §17 tables** (`pkg/internal/layout/css/table.go`+`tableborder.go`+`tablefix.go`+`measure.go`):
  anonymous-table fixup, the grid model, fixed and auto column-width solving, colspan/rowspan,
  `vertical-align`, captions, `<col>`/`<colgroup>`, and both `border-collapse` models.
- **Bounded span and track counts** (`pkg/internal/layout/css/build.go`+`table.go`, `pkg/internal/css/grid_value.go`):
  the table grid and the grid occupancy map both materialize one entry per covered slot, so a count
  named in markup is an allocation, not a description. `colspan`/`<col span>` clamp to 1000 and
  `rowspan` to 65534 (HTML's own ceilings), a `rowspan` is additionally clipped to the rows the
  document actually has, and an explicit track list, grid line number, or `span` count is bounded at
  100,000 tracks. Without these a `<td colspan="900000000">` or `repeat(200000000, 1px)` did not
  render slowly — it did not return. Clamping rather than rejecting matches HTML: an over-large span
  means "span everything", and the grid-extent pass trims it to the real count anyway.
- **Web fonts** (`pkg/internal/css/fontface.go`, `pkg/internal/font/sfnt.go`/`woff1.go`/`woff2*.go`,
  `pkg/internal/layout/font`): `@font-face` capture, WOFF1/WOFF2 decode including the glyf/loca transform,
  `local()` through `DirFontProvider` (the public face of the internal `DiskFontProvider`), and
  family-fallback-list resolution.
  **Degrades honestly:** two `@font-face` descriptors are recognized but not implemented —
  `unicode-range` (so a family split across subsetted faces uses the WHOLE face for every rune) and
  `font-display` (no async loading exists in synchronous layout). Both are now reported through
  `Stylesheet.Unsupported` and logged once by a caller that has a logger; before, they were dropped
  by the descriptor switch with no record at all. The face itself still loads either way — unlike a
  dropped SELECTOR, a dropped descriptor does not disable its rule, and the diagnostic says so.
- **Flexbox** (`pkg/internal/layout/css/flex.go`+`flexfix.go`): axis-abstracted layout, §9.7
  flexible-length resolution, `justify-content`/`align-items`/`align-self`, `inline-flex`, and
  **multi-line wrapping**. Wrapping covers `flex-wrap: wrap`/`wrap-reverse`, §9.3 line collection,
  `align-content` — including the flex `stretch` initial, which differs from the shared grid default
  — the cross-axis gap between lines, and the `flex-flow` shorthand. justify-content and baseline
  groups resolve per LINE; the §9.4 step-8 cross clamp is gated to single-line containers.
  wrap-reverse XORs with the RTL cross flip. Wrapped rows paginate between lines for free, since
  `splitFlexGridForPage` is geometry-driven.
- **CSS Grid** (explicit grid; `pkg/internal/layout/css/grid.go`+`grid_track.go`+`grid_place.go`+`gridfix.go`
  +`baseline.go`): §11 track-sizing and §8 placement (spans, named areas, sparse and dense
  auto-placement), item and content-distribution alignment, `inline-grid`, and a cross-cutting
  baseline backport covering grid, flex, and table cells.
- **`OpenURL` + HTTP loader** (`pkg/resource/http.go`): fetches HTML over HTTP(S) and resolves
  relative refs, handles `data:` URIs, and does URL-userinfo Basic auth, redacted from logs.
- **Pagination** (`pkg/internal/layout/css/paginate.go`, `WithPageSize`): fixed-height page fragmentation,
  the break cascade, between-block and forced breaks, and per-page distribution of
  relative/abs/fixed/float boxes and the html/body border.
- **Per-page bottom-anchored `position: fixed`** (`pkg/internal/layout/css/paginate.go`): a `bottom`-anchored
  fixed box sits at the bottom of every page. It previously resolved against the full single-tall
  document height, which put it below every page and made it invisible. Top-anchored boxes are
  untouched, since their Y is already the page-local offset, and a percentage `bottom` is declined
  rather than mis-shifted.
- **Repeated `<thead>` on continuation pages** (`pkg/internal/layout/css/tablepage.go`, `table.go`): a table
  split across pages repeats its header rows on every continuation, so a long table keeps its column
  headings. Grid construction flattens the head/body/footer distinction away, so the header's bottom
  Y is recorded on the table fragment as `HeaderBottom` and the cells above it are deep-cloned onto
  each tail. The tail's own `HeaderBottom` is then re-anchored to its copy, so a table spanning three
  or more pages keeps repeating.
- **Mid-cell table splitting** (`pkg/internal/layout/css/tablepage.go`): a table row taller than the page —
  including a single-row table — splits THROUGH its cells rather than overflowing and being clipped.
  A cell's content is an ordinary fragment spine, so the recursive splitter handles it with no
  relayout. Cells fragment independently: one that cannot break rides the tail whole while its
  row-mates split. Breaking between whole rows is still preferred when a row boundary is available.
- **Mid-block forced breaks** (`pkg/internal/layout/css/fragmentpage.go`, `paginate.go`): a `break-before` or
  `break-after` on a nested block sitting at neither its ancestor's leading nor trailing edge now
  splits that ancestor at the break position, instead of being dropped with a warning. It reuses the
  recursive splitter, and the split Y comes from the author's break rather than the page boundary,
  so it applies even when the page is not full.
- **Recursive spine splitting** (`pkg/internal/layout/css/fragmentpage.go`): a child straddling a page boundary
  is itself split rather than riding the tail whole, so a `section > div > p` spine breaks at a line
  boundary inside the paragraph instead of leaving the head page blank below the last whole child.
  The dispatcher needed no signature change: `pageBottom` is absolute page space and the fragment
  tree shares one coordinate system, so it was already valid at any depth. `break-inside: avoid` stops
  the recursion.
- **Page-split correctness** (`pkg/internal/layout/css/fragmentpage.go`): a split now routes out-of-flow
  children — floats and positioned boxes — to the fragment whose band contains them, instead of
  dropping them from both. It also detaches the `BgImage`/`ClipChain`/`Collapsed` state that the
  shallow clone would otherwise share between two pages, since the per-page shift mutates those in
  place and one fragment's shift moved the other's background. Finally it clamps a clipping
  fragment's `ClipRect` to its own extent. All of it hangs off the single `splitAnyBlockForPage`
  dispatch point.
- **CSS Paged Media** (`pkg/internal/css/page.go`+`pagesize.go`, `pkg/internal/layout/css/pagemodel.go`+
  `fragmentpage.go`+`marginbox.go`, `WithDefaultPaged`): `@page` size, margins, named and pseudo
  pages, and the 16 margin boxes; `break-inside`; widows and orphans via mid-block line
  fragmentation; running headers and footers with page counters; `@page marks`/`bleed`;
  `string-set`/`string()`; `position: running()`/`content: element()`; and named-page multi-width
  reflow.
- **`white-space`** (`pkg/internal/css` + `pkg/internal/layout/inline`): normal/nowrap/pre/pre-wrap/pre-line, plus tab
  stops.
- **`overflow-wrap` / `word-break` — mid-word line breaking** (`pkg/internal/css/cascade.go`,
  `pkg/internal/layout/inline/wordbreak.go` + `grapheme.go`): `overflow-wrap: normal | break-word |
  anywhere`, plus the legacy `word-wrap` alias that sets the same property, and `word-break:
  normal | break-all | keep-all`. Both inherit. `break-word` and `anywhere` break inside a word only
  as a LAST RESORT — a word that fits on a line of its own is moved down whole — while
  `break-all` is eager and chops at the line edge regardless. `keep-all` suppresses mid-word
  breaking. `anywhere` and `break-all` additionally shrink intrinsic **min-content** width (CSS
  Text 3 §5.5) while `break-word` deliberately does not, and that is the only difference between
  `break-word` and `anywhere`. Every break lands on a **grapheme-cluster boundary**: full UAX #29
  extended clusters (GB3–GB13, including emoji ZWJ sequences and regional-indicator flag pairs),
  read from the UCD tables already vendored with `benoitkugler/textlayout`, so no combining mark,
  jamo, or emoji is ever split. `white-space: nowrap` outranks all of it. Untouched callers — DOCX,
  SVG text, any page not setting these — keep the whitespace-only breaking path byte-identical.
- **`letter-spacing` / `word-spacing` on the CSS text path** (`pkg/internal/css/cascade.go`,
  `pkg/internal/layout/inline/shape.go`, `pkg/internal/layout/css/inline.go`): `letter-spacing: normal | <length>` and
  `word-spacing: normal | <length>`, both inherited, both taking NEGATIVE lengths that tighten,
  in `px`/`pt`/`em`. `letter-spacing` is added after **every** typographic character unit including
  the last one on a line. That matches Chrome, Firefox and Safari rather than CSS Text 3's literal
  "between" wording, so a right-aligned tracked line correctly stops one tracking-width short of the
  edge. `word-spacing` is added only at word separators, U+0020 and U+00A0, per CSS Text 3 §8.2;
  U+00A0 takes the spacing without becoming a break opportunity. `normal` is modeled as the zero
  length, since the spec's only distinction is latitude this engine's justifier never takes, so it
  correctly RESETS an inherited value. Both resolve against the run's own font size and fold into
  `Glyph.Advance` at shaping time, so **line breaking, min/max-content sizing, tab stops, and
  justification all compose with no change to any of them**: a justified line still lands flush
  because word-spacing widens the gaps before the slack is computed. Per-glyph advances are floored
  at zero, so a large negative tracking overlaps glyphs the way browsers do rather than producing
  negative advances the greedy breaker cannot represent. Bidi control marks stay zero-width.
  Untouched callers — DOCX, PDF, any page not setting these — are byte-identical.
  Two limits are recorded rather than implied. Tracking is applied to cursive scripts, where CSS
  Text 3 says it should be suppressed for joined runs, because harfbuzz join data is not surfaced
  through this engine's complex-shaping result. And these properties still do **not** inherit into
  an inline `<svg>` — nothing does, because inline SVG is replaced content re-parsed in isolation
  (see `docs/SVG.md`).
- **List markers + CSS counters** (`pkg/internal/css/counter_format.go`, `pkg/internal/layout/css/counters.go`,
  `pkg/internal/font/bullet.go`): `list-style-*`, `counter-reset`/`-increment`/`-set`, `content: counter()`,
  and synthetic bullet outlines.
- **`background-image`** (`pkg/internal/css/background.go`, `pkg/internal/layout/css/background.go` + paint):
  `url(..)` with `-repeat`/`-position`/`-size`/`-origin`/`-clip`.
- **The `background` shorthand validates before it commits** (`parseBackgroundShorthand` in
  `pkg/internal/css/shorthand.go`): every component must classify, and if any one does not, the WHOLE
  declaration is discarded with no longhand touched. That is CSS 2.1 §4.2 and CSS Syntax 3, which
  treat an invalid declaration as never having entered the cascade. It is also what makes the
  standard fallback idiom work: `background: red` followed by `background: linear-gradient(…)`
  leaves red standing on an engine that cannot parse the gradient. An expander that reset first and
  tolerated unknown components would turn every such fallback transparent. Verified against
  Blink and Gecko, including the case where a bad component sits beside a good one
  (`notacolour url(x.png)` applies neither).
- **CSS gradients as a background image** (`pkg/internal/css/gradient.go`, `pkg/internal/layout/gradient.go`,
  `pkg/internal/layout/paint/gradient.go`): `linear-gradient()`, `radial-gradient()` and both
  `repeating-*` forms. Linear takes an `<angle>` in any CSS unit (`deg`/`grad`/`rad`/`turn`),
  `to <side>`, or `to <corner>`. The corner case computes the spec's aspect-dependent
  gradient line, perpendicular to the box's other diagonal, and **not** 45°, which is only
  right on a square. Radial crosses `circle`/`ellipse` with
  `closest-side`/`closest-corner`/`farthest-side`/`farthest-corner`, and also takes explicit
  `<length-percentage>` radii and `at <position>`. Colour stops take positions in `%` or
  lengths, with the full CSS normalization: omitted endpoints default to 0%/100%, an
  unpositioned run is spread evenly between its bracketing stops, and a decreasing position
  is corrected UP to the running maximum. That last is a forward clamp producing a hard break
  — never a sort, which would reorder the author's colours. Two stops at one position give a
  hard colour break. Interpolation is in **premultiplied alpha**, matching browsers: fading to
  `transparent` stays in its own hue instead of showing the grey/black band a straight-RGBA
  ramp produces.
  Gradients paint through the **same shading seam SVG paint servers use**
  (`raster.NewAxialShader`/`NewRadialShader` → `render.Device.FillShading`), so they are
  evaluated per device pixel rather than baked to a bitmap, and the PDF writer emits a native
  `/Shading` dictionary for them. A gradient has no intrinsic size, so it takes the
  background-origin box's. That is what makes `background-size`/`-position`/`-repeat`/`-origin`/
  `-clip` all apply to it through the unchanged geometry path: its geometry resolves in
  TILE space, so a resized gradient really is resized.
  **Degrades honestly:** a colour hint — a bare `<length-percentage>` between two stops —
  needs a non-linear ramp the shared seam cannot express, so it is REJECTED at parse time, as
  are `conic-gradient()`, a unitless angle, and a one-stop list. The declaration is dropped,
  so the background colour still paints rather than a subtly wrong ramp appearing. An ending
  shape with a zero radius, such as `closest-side` centred on a box corner, establishes no
  geometry: nothing paints, the background colour remains, and the skip is logged via
  `warnOnce`.
- **CSS Color 4 colour values — ONE grammar for the whole engine** (`pkg/internal/css/color.go`; `pkg/internal/svg`
  delegates to it): the full 148-keyword named table, from `golang.org/x/image/colornames` plus
  `rebeccapurple` and `transparent`; all four hex forms (`#rgb`/`#rgba`/`#rrggbb`/`#rrggbbaa`); and
  `rgb()`/`rgba()`/`hsl()`/`hsla()` in both the legacy comma syntax and the modern space syntax with
  `/` alpha, taking integer or percentage channels. Alpha is LIVE end to end: parsing yields a
  `color.RGBA` that the painter hands to the device unchanged and the rasteriser composites. A pixel
  check confirmed it — `background:rgba(0,0,0,0.9)` on an 80×80 box went from 0 painted pixels
  to 6400. Previously `pkg/internal/css` had a hand-written parser covering only `#rgb`/`#rrggbb`/`rgb()` and
  eight keywords while `pkg/internal/svg` carried a complete implementation, so any alpha-bearing value
  failed the cascade, the declaration was dropped per CSS error handling, and the element painted
  *nothing*. The merge went into `pkg/internal/css` rather than the reverse because `pkg/internal/svg` already depends
  on it and `pkg/internal/css` depends on no internal package. Malformed values still yield `ok=false`, so
  the declaration drops and the prior value stands — through the `background` shorthand as well as
  through the longhands (`background-color`, `color`, `border-*-color`).
- **`border-radius`** (`pkg/internal/css/borderradius.go`, `pkg/internal/layout/borderradius.go`,
  `usedRadii` in `pkg/internal/layout/css/block.go` + paint): CSS Backgrounds 3 §5 in full — the shorthand
  taking 1–4 values in CORNER order (diagonal pairing, not `expandBox`'s clockwise side rule), the
  `/` form for elliptical corners, all four longhands, and percentages. A corner's two semi-axes
  resolve against DIFFERENT bases, the horizontal one against the border box's width and the
  vertical one against its height, which is why radii stay unresolved `Length` pairs until layout.
  The §5.1 overlap correction scales all eight components by one shared factor `f = min` over sides,
  so `border-radius:100px` on an 80×80 box yields a true circle rather than four separately-clamped
  arcs. Backgrounds fill the rounded path directly, so the backend antialiases the arcs itself;
  background IMAGES are bracketed by a rounded clip, since `DrawImage` has no shape parameter.
  Borders paint as one even-odd RING, the outer rounded rect minus the inner, where the inner radius
  is the outer minus the border width floored at zero. A uniformly-thick rounded border is NOT the
  outer path stroked, and a border thicker than its radius correctly squares the inner corner while
  the outside stays round. PDF keeps real curves: `pdfwrite` emits the same Béziers as `c` operators
  natively, for both the fill and the `W n` clip. DOCX is unaffected, as that writer builds a
  document model rather than painting and has no rounded-box primitive to target.
  **Degradations, logged by the layout engine and covered by tests:** a rounded border is filled in
  ONE colour as SOLID, so per-side border colours and the non-solid styles (dashed/dotted/double/
  ridge/groove/inset/outset) are approximated on a rounded box. Square-cornered boxes still paint
  four fully-styled strips and are byte-identical.

- **`box-shadow`, outer and `inset`** (CSS Backgrounds 3 §6 — `pkg/internal/css/boxshadow.go`,
  `pkg/internal/layout/css/boxshadow.go`, `pkg/internal/layout/paint/boxshadow.go`). The parser takes the full grammar,
  `&&` combinator included: `inset`, the 2–4 lengths and the colour may appear in **any order**, so
  `inset red 2px 2px` and `2px 2px inset red` name the same shadow. Comma-separated **lists** paint
  in the spec's order, so the **first shadow is on top**. Error handling is the engine's usual — one
  malformed entry kills the whole declaration. A negative *blur* is an error; a negative *spread* is
  legal and shrinks the shadow. An omitted colour, and the `currentColor` keyword, resolve to the
  element's own `color` at layout time, where the cascade is still reachable.
  **`inset` is a genuinely different rendering, not a sign flip.** An outer shadow fills the region
  OUTSIDE the border box, so a transparent box shows a ring rather than a filled blob. An inset one
  fills whatever part of the PADDING box its own shape does not cover and can never escape that box,
  however far you offset it; its spread sign inverts too, because shrinking the lit interior thickens
  the band. The two also occupy **different slots of the paint order** — outer behind the background,
  inset over both backgrounds but under the border — so a list carrying both shows both.
  The blur is `sigma = radius/2`, per the spec's "the shadow's edge transitions over a distance
  equal to the blur radius, centred on the edge". It runs through **`pkg/internal/svg/filter`'s existing
  `feGaussianBlur`**: the repo keeps exactly one blur implementation, shared by SVG filters, the CSS
  `filter` shorthand and now this. A shadow's silhouette is one uniform colour, so it takes that
  blur's **alpha-only entry point** (`filter.BlurAlpha`) — the same three box passes over a coverage
  plane instead of four RGBA channels, skipping both `RGBA<->float32` conversions, which profiled as
  costing more than the blur arithmetic itself. A test asserts the two agree channel-for-channel.
  Square corners only — `border-radius` is not implemented, and
  `paint.shadowOutline` documents the single integration point for it.
- **A `box-shadow` with no blur stays fully vector, including in PDF.** It is a plain even-odd fill
  of the box's shape, so the common patterns — a hard offset, a spread ring, an `inset` colour
  spine — cost no rasterization anywhere. A **blurred** shadow needs an offscreen raster surface via
  `render.Device.RenderOffscreen`, and `pkg/internal/pdfwrite` returns nil from that by design: PDF has
  no blur operator, and a blur has no vector representation. There — and whenever the surface would
  be degenerate, off-canvas, or over the per-shadow pixel cap — the shadow **degrades to the same
  shape with a HARD edge**, at the same place and size. That follows the "a visible approximation
  beats a blank" rule the CSS `filter` path already uses, and is deliberately NOT a rasterization of
  the page. **That degradation is currently SILENT**: `pkg/internal/layout/paint` has no logger to report it
  through, since `PaintPage` takes only a Device, a Page and a Matrix — exactly as the CSS-filter
  pixel-cap degradation in the same package does. **DOCX output carries no shadow at all.**
  `pkg/internal/docxwrite` consumes the `cssbox` tree directly rather than the painted item list, so
  it never sees a shadow item, and it has no `box-shadow` analogue to map one onto.
- **Sub-region rasterization** (`Document.RasterizePageRegion`, `raster.NewRegion`): renders only a
  rect of a page, returning an image whose `Bounds()` are that rect in **page-absolute** device
  pixels, so it composites straight onto a cached full-page frame with no offset arithmetic. The
  pixels are **identical** to the same crop of `RasterizePage` — the property that makes the
  composite seamless, pinned by a test over rects cutting through shadows, gradients, overlapping
  boxes and text. That identity is the whole difficulty: the device reports the **page's** size and
  rasterizes masks against the **page**, because a device reporting its own sub-rect clamps every
  offscreen surface derived from it (a box-shadow blur, a CSS filter) to the region — measured at
  56/255 off — while clipping masks to the sub-rect shifts antialiasing at the boundary by 1/255,
  exactly where it shows. Cost scales with the region but not proportionally: the page's item list is
  replayed in full, with out-of-region items rejected by a bounds test and blurred shadows skipped
  via `WriteBounds`, so a small region on a busy page is bounded below by the cost of that walk.
  Measured on a 1920×480 dashboard, a 400×300 region renders **3.4× faster** than the page (52ms →
  16ms) with 79% fewer allocations. Reflow documents only; an opened PDF returns
  `ErrRegionUnsupported`, since its content interpreter has no notion of a partial page.
- **Link pseudo-classes + `text-decoration: underline`** (`pkg/internal/css/selector.go`, `pkg/internal/html/ua.go`):
  `:link`/`:visited` plus general pseudo-class parsing.
- **Inline emphasis UA defaults** (`pkg/internal/html/ua.go`): `strong`/`b` bold, `em`/`i`/`cite`/`var`/`dfn`
  italic, `u`/`ins` underlined. Each resolves to the same computed style as its CSS equivalent, and
  they nest without flattening. These tags used to be structurally present — they even survived
  conversion to Markdown — yet looked identical to plain text in every rasterized format. The sheet
  spells the weight `bold` rather than the spec's `bolder`, because the cascade's `font-weight` is
  the binary bold/normal the four-style bundled families can express and it rejects the relative
  keywords; `bolder` would drop as invalid and the emphasis would stay invisible. A test pins this.
  `<small>`/`<big>` are omitted rather than given a hardcoded px size. `<mark>` carries the standard
  yellow highlight — it landed with inline-box backgrounds below, and before that the rule would
  have cascaded and painted nothing. Showcase §30.
- **`transform`** (`pkg/internal/css/transform.go`, `pkg/internal/layout/css/fragment.go`): the 2D functions, composed
  left to right — `translate`/`translateX`/`translateY` taking lengths and percentages of the box's
  own size, `scale`/`scaleX`/`scaleY`, `rotate`, `skew`/`skewX`/`skewY`, and `matrix()`. It is a
  PAINT-time effect and changes no layout (CSS Transforms 1 §3): the box keeps the space it
  occupied, and the matrix brackets its already-flattened items. A transformed element establishes a
  stacking context and a BFC, as the spec requires. That is also what lets the bracket wrap its
  background and content together rather than splitting them across Appendix E's phases. Not
  modeled: the 3D functions (`translate3d`, `rotateX`, `perspective`, `matrix3d`), refused rather
  than flattened since the engine has no 3D pipeline; and `transform-origin`, which is always the
  box centre. Showcase §42.
- **Absolute positioning in flex containers, and flex-derived heights** (`pkg/internal/layout/css/flex.go`,
  `block.go`): an abs/fixed child of a flex container is out of flow and honours its offsets (CSS
  Flexbox §4.1) instead of being laid out as a flex item pinned to the edge. `top`+`bottom` with
  `height: auto` sizes the box to the space between them (CSS 10.6.4), matching what `left`+`right`
  already did. And an element whose height comes from flex layout — `align-items: stretch` or its
  own `flex: 1` — resolves `justify-content` for its own children, because the cross size is now
  definite BEFORE its interior lays out rather than written onto the fragment afterwards.
  Showcase §41.
- **SVG presentation attributes inherit from the root** (`pkg/internal/svg/svg.go` `rootStyle`): `fill`,
  `stroke`, `stroke-width`, caps/joins/dashes and the rest of the inherited vocabulary set on the
  root `<svg>` now reach its children, as CSS inheritance requires. Only the font and text
  properties used to carry across; the root's paint properties were resolved and then discarded. An
  icon authored as `<svg stroke="…">` with detail paths inheriting it painted its filled parts and
  none of its strokes. The set is built by INVERSION: start from the root's resolved style and clear
  only what CSS marks non-inherited (`opacity`, `clip-path`, `mask`, `filter`, `mask-type`,
  `overflow`, `display`), so a property added later defaults to inheriting rather than to being
  dropped. Showcase §40.
- **Comma-separated `background` / `background-image` layer lists** (`pkg/internal/css/shorthand.go`,
  `pkg/internal/layout/css/background.go`): multiple layers paint, first layer on top, with the
  background-color behind them. So `background: <gradient>, <color>` — the ordinary way to give a
  gradient a fallback — works. It used to make the whole declaration unparseable, so the element
  painted NOTHING and it read as "gradients are unsupported". A colour is accepted in the final
  layer only (CSS Backgrounds §3.10); one earlier invalidates the declaration, as does any
  unparseable layer. Known limit: `background-size`/`-repeat`/`-position`/`-origin`/`-clip` are
  single-valued and apply to every layer. Genuinely per-layer values are a separate slice.
  Showcase §39.
- **Margins on flex children** (`pkg/internal/layout/css/flex.go`): honoured on both axes, `margin: auto`
  absorbing free space included (CSS Flexbox §8.1). Flex layout used to be margin-blind, taking an
  item's size and position from its border box, so a margin on a flex child did nothing while the
  identical rule on a block child worked. Margins now count where they must: line packing uses the
  OUTER size, free-space distribution and `justify-content` position the margin box, the line grows
  to hold a cross margin, and `stretch` fills the line less the item's cross margins. An auto-height
  container's own content extent also encloses its children's margin boxes, so a trailing margin
  does not overflow the container that should have grown for it. Showcase §38.
- **`line-height`, all forms** (`pkg/internal/css/value.go` `UnitNumber`, `pkg/internal/layout/css/inline.go`): `em`,
  `%`, lengths, `normal`, and the commonest spelling of all, the **unitless multiplier**
  (`line-height: 1.5`). The unitless form used to be rejected as an invalid length and the
  declaration dropped, so every block used the font-metric height and the property looked inert.
  Units differ where it matters: a NUMBER inherits as a number and re-multiplies against each
  descendant's own font size, while `em`/`%` compute against the declaring element and inherit as a
  fixed length (CSS 2.1 §10.8.1). A unitless number is still not a valid length elsewhere —
  `width: 5` remains invalid. Showcase §37.
- **Colour fonts / emoji** (`pkg/internal/font/colr.go`, `colrv1.go`, `bitmap.go`,
  `pkg/internal/layout/paint/colrgradient.go`): colour glyphs paint in colour, through both families of
  table. **`COLR`/`CPAL`** (v0 and v1) decode to layered outlines. Those are vector, so they scale
  like text, and they carry per-layer **full affine transforms** (translation, mirror, rotation,
  scale) and **linear/radial gradients** with pad/repeat/reflect spreads. **`sbix`** (Apple) and
  **`CBDT`/`CBLC`** (Google) decode PNG strikes, picking the strike nearest the used size and
  preferring a larger one so the image is downscaled rather than enlarged; these do NOT scale like
  outlines. A `.ttc` collection resolves its first face's tables. Emoji in ordinary prose reach an
  **installed** colour font through the script-fallback chain (Apple Color Emoji, Segoe UI Emoji,
  Noto Color Emoji…), because a colour emoji font is far too large to bundle. On a host with none,
  the run degrades to the missing-glyph path. A colour glyph keeps the FONT's palette rather than
  the CSS `color`, unless the font opts a layer into the foreground via the palette sentinel.
  Showcase §36. Not modeled: **sweep (conic) gradients** and **composite paints**, refused as a
  whole so the glyph falls back to its monochrome outline rather than a plausible wrong colour;
  CPAL light/dark palette variants (the first palette is used); and non-PNG strike payloads.
- **`text-overflow: ellipsis` and `-webkit-line-clamp`** (`pkg/internal/layout/inline/ellipsis.go`,
  `pkg/internal/layout/css/inline.go`): single-line ellipsis truncation and N-line clamping, the two ways CSS
  truncates text. Truncation runs in GLYPH units, dropping whole glyphs until the ellipsis fits, so
  a cut never lands mid-character and the line never spills past the clip edge. Trailing whitespace
  is dropped before the ellipsis, and the ellipsis inherits the styling of the glyph it follows.
  `-webkit-line-clamp` (and the unprefixed `line-clamp`) is a LAYOUT effect: the box stops after N
  line boxes and its height shrinks to them, matching the height a browser reports, and the final
  line is marked only when text actually remains. `text-overflow` applies only where the box clips,
  so an `overflow: visible` box still overflows visibly, as browsers do. When even the ellipsis
  alone does not fit it is still rendered (CSS Overflow 3 §5). `text-overflow: clip` (the initial)
  and an over-large clamp are both inert. Showcase §35. Not modeled: `text-overflow`'s custom
  `<string>` and two-value forms, and per-axis clamping.
- **Host CSS cascades into inline `<svg>`** (`pkg/internal/svg` `HostContext`/`ParseWithHost`,
  `pkg/internal/layout/css/replaced.go`): a page author sheet styles inline SVG children, so
  `.icon { fill: blue }` works the way CSS says it should. Class, element, id, grouped, and
  **descendant selectors rooted outside the `<svg>`** all match, because the ancestor chain
  continues past the SVG root into the host tree — `#sidebar .icon` matches rather than silently
  doing nothing. `currentColor` resolves against the `color` the `<svg>` box inherits, and the
  host's font-size and font-family seed the SVG root. Precedence follows CSS: presentation
  attributes (zero specificity) < host sheets < the SVG's own `<style>` (the more specific context
  wins a tie) < inline `style=`. The inline-SVG parse cache is keyed by markup **plus the host
  context**, so two byte-identical subtrees under different rules, colors, or ancestors do not
  collide. An `<img src="*.svg">` is deliberately NOT reached: a referenced SVG is a separate
  document that CSS does not cascade into, and a test pins that direction too. Showcase §34.
- **`color-mix()`** (`pkg/internal/css/colormix.go`, `pkg/internal/css/colorspace.go`): CSS Color 5 §3, in every
  interpolation space — `srgb`, `srgb-linear`, `hsl`, `hwb`, `lab`, `lch`, `oklab`, `oklch`, `xyz`,
  `xyz-d50`, `xyz-d65` — plus all four hue-interpolation modes (`shorter`/`longer`/`increasing`/
  `decreasing hue`) for the polar spaces. Percentages may precede or follow either colour; omitted
  ones fill the remainder; two that sum under 100% normalize the weights **and** scale the result's
  alpha. Interpolation is premultiplied per CSS Color 4 §12.3, so a semi-transparent input
  contributes proportionally less hue. It evaluates inside the single shared colour grammar, so it
  works in every property that takes a colour, and it nests. Expected values are pinned to
  **Chrome**, captured by canvas readback rather than derived from the implementation's own
  arithmetic. Two divergences are deliberate, both toward exactness. Mixing with `transparent`
  leaves the opaque colour's channels untouched — Chrome rounds up to 2/255 through an intermediate
  space — which makes `color-mix(in srgb, X N%, transparent)` exactly `rgba(X, N/100)`. And nested
  mixes stay unquantized between levels. An unknown space or a malformed component drops the
  declaration per CSS error handling. Showcase §33. Gamut mapping is not modeled — out-of-gamut
  results clamp.
- **Inline-box backgrounds and borders** (`pkg/internal/layout/inline` `InlineBoxStyle`,
  `pkg/internal/layout/css/fragment.go` `appendInlineBoxDecorations`): `background-color`, a uniform solid
  `border`, and horizontal `padding` on a non-replaced inline box (`<span>`, `<em>`, `<a>`…). Line
  breaking flattens inline boxes into glyph runs, so every glyph carries the box's identity and
  consecutive glyphs coalesce **per line box** — a span that wraps paints one rect per line, the
  same shape `text-decoration` uses. Identity is a POINTER, not a color: two adjacent spans with the
  same background stay two rects. Inkless glyphs survive this pass so a background stays continuous
  across intra-span spaces. Padding is part of LAYOUT, riding on a zero-ink edge glyph at each
  boundary, which lets the breaker, intrinsic sizing and alignment reserve it by reading advances
  alone. The rect is the content area — tallest ascent plus deepest descent among its glyphs — so a
  span mixing font sizes is sized to its largest. Not modeled: background images, vertical
  padding/margins (which per CSS 10.6.1 overflow the line box rather than growing it), and per-edge
  or rounded inline borders. Showcase §32.
- **Legacy presentational-attribute hints** (`pkg/internal/css/hints.go`): `bgcolor`/`align`/`valign`/
  `width`/`cellspacing`/`cellpadding`/`border`/`<font>`/`<ol type/start>`/`<body link>`/`dir`… map to
  CSS below author rules, so HN renders with its bgcolor.
- **Direction-relative alignment + bidi plumbing** (`pkg/internal/css/cascade.go`, `pkg/internal/css/hints.go`,
  `pkg/internal/layout/css/inline.go`) — RTL slice 1 of 5. `text-align: start|end|match-parent`, whose
  initial value is now `start` — byte-identical for LTR, since every consumer defaults to left.
  `unicode-bidi` parsed and stored, but not inherited. The global `dir` attribute as a hint: the
  selector engine has no attribute selectors, so the spec's `[dir=rtl]` UA rules are not
  expressible, and hint rank is equivalent. `bdi`/`bdo` isolation. `effectiveDirection` — an
  anonymous box's Style is zero-valued, so `Direction` is `""` not `"ltr"`; never read the field
  directly. And the RTL text-indent edge. `dir=auto` degrades + logs.
- **`writing-mode`: vertical text, single line** (`pkg/internal/css/cascade.go`,
  `pkg/internal/layout/css/inline.go`, `pkg/internal/layout/css/fragment.go`): `horizontal-tb` (the initial value) is
  honoured; `vertical-rl` **lays out** — the baseline runs down the page, every glyph on a line
  shares one X, and the pen advances along Y by the font's vertical metric. An auto-sized box grows
  along the text's own axis, so a vertical label sizes itself instead of overflowing. The deprecated
  SVG 1.1 spellings (`lr`, `lr-tb`, `rl`, `rl-tb`) resolve to `horizontal-tb`, matching the SVG path;
  `sideways-rl`/`sideways-lr` are dropped rather than folded into a vertical value. Inherited
  (CSS Writing Modes 4 §3.1) — pinned by a test, since `inheritFrom` silently resets an unregistered
  field instead of inheriting it. Shown in `testdata/htmldoc/` §43.

  Implemented by transposing at the `layoutInline` boundary: shaping is shared (it is
  axis-independent), and a separate emit path walks the pen down. Horizontal documents take the
  identical path they did before — pinned by a test, and by the whole golden corpus rendering
  unchanged. `LineFragment.Vertical` marks the line; `GlyphFragment.Y` carries the per-glyph offset
  and is zero on every horizontal line, so existing whole-line Y shifts (block stacking, table rows,
  pagination) keep working untouched.

  **Not implemented, each logged:** vertical line *wrapping* (one line only — a longer run overflows
  and says so); `vertical-lr`'s stacking direction (it lays out as `vertical-rl` and logs, since the
  two differ only in which side subsequent lines stack from and there is one line); atomic inline
  boxes; hard line breaks; **float avoidance** — a vertical line beside a float is drawn straight
  *through* it, the one gap here that produces overlapping ink rather than missing ink, so it is
  logged rather than left to be discovered; **shrink-to-fit sizing** — an inline-block or float in a
  vertical mode is still measured on the horizontal axis, so its cross size comes out as long as its
  text (transposing that means turning the intrinsic-measure seam table/grid/flex sizing all share);
  and text-decoration and inline-box backgrounds, skipped because every span the painter computes is
  an X range and drawing them would rule a line across the page instead of beside the text.
- **`text-orientation`** (`pkg/internal/css/cascade.go`, `pkg/internal/layout/css/inline.go`,
  `pkg/internal/layout/paint/paint.go`): `mixed` (the initial value) | `upright` | `sideways`, plus the CSS
  Writing Modes 3 alias `sideways-right`. Decides, per glyph, whether it stands upright in a vertical
  line or lies on its side. Inherited (§5.1) and a no-op in a horizontal writing mode, per spec — but
  parsed and carried there anyway, since a value set on a horizontal ancestor must still reach a
  vertical descendant. Shown in `testdata/htmldoc/` §43.

  The rotation composes into the **per-glyph matrix paint already builds** (`layout.GlyphItem.Rotate`,
  applied inside `paintGlyph`), not a `TransformPush`/`Pop` bracket around the run: the turn is about
  each glyph's own origin, so a shared bracket would need a per-glyph translation anyway and would not
  amortize, while costing two display-list items and a recursive paint call per push — on the hottest
  path in painting, under the *initial* value. Zero rotation is skipped rather than multiplied
  through, so every glyph emitted before this existed paints byte-identically.

  A rotated glyph advances the pen by its **horizontal** extent, since it is lying on its side; an
  upright one advances a full em. That is what makes `sideways` proportional and `upright`
  fixed-pitch — a `sideways` run is shorter than the `upright` one for the same string.

  **Which glyphs stay upright under `mixed` approximates Unicode's `Vertical_Orientation`
  (UAX #50)** — neither the standard library nor `textlayout` ships that table. The check covers the
  scripts a vertical line is actually set in (Han, Hiragana, Katakana, Hangul, Bopomofo, Yi) plus the
  CJK punctuation and full-width blocks stdlib's script tables exclude, and **errs toward rotating**:
  a wrongly-rotated glyph is visibly odd, while a wrongly-upright one silently looks like `upright`
  was intended. Vendoring the real table is the correct fix and is recorded as outstanding.

  **Not implemented:** vertical alternate glyph forms (the `vert`/`vrt2` GSUB features) — a rotated
  glyph is the rotated Latin form, not a purpose-designed vertical one. `text-combine-upright` is out
  of scope. The `mixed` upright case **cannot be shown in the visual showcase**: no bundled face
  covers CJK, so it would render as empty boxes; the showcase says so and the case is covered by unit
  tests against the classifier instead.
- **Vertical `<text>` in SVG** (`pkg/internal/svg/style.go`, `pkg/internal/svg/draw/text.go`): `writing-mode` and
  `text-orientation` are honoured on the SVG path, which places `<text>` through its own layout
  rather than the CSS inline layer. The pen walks down the page by the font's vertical advance, and
  `text-anchor`, text decoration, bidi reordering and the chunk model all follow the run's own inline
  axis rather than assuming X. SVG 1.1's `tb`/`tb-rl`
  resolve onto `vertical-rl` so the renderer has one vocabulary; `sideways-rl`/`sideways-lr` are
  reported rather than folded into a mode the author did not ask for. Shown in `testdata/htmldoc/`
  §43 alongside the CSS demos.

  **The orientation classifier and the vertical advance are shared with the CSS path**
  (`inline.GlyphRotation` / `inline.VerticalAdvancePt`, moved to `pkg/internal/layout/inline` for this), so
  the two agree by construction rather than by two implementations happening to match — which
  matters most for the UAX #50 approximation, where a drift between them would be invisible.

  Three passes are axis-aware rather than assuming X: `applyAnchors` (a chunk aligns along its own
  axis), `reorderChunk` (the bidi slot redeal), and `paintDecorationSegment` (the rule's span). The
  decoration rect is built in the segment's unrotated frame and carried through the same matrix the
  glyph is, so an underline turns with the text instead of staying axis-aligned.

  **`direction: rtl` does not flip a vertical chunk's anchor.** `direction` reverses the inline axis,
  and in a vertical mode that axis runs top-to-bottom for both ltr and rtl — an rtl vertical run
  reverses the order *lines* stack in, which is the multi-line case this does not reach. Applying the
  horizontal flip would move a vertical chunk to the wrong end of its own axis.

  **`writing-mode` on a `<tspan>` is ignored** — the property establishes the mode for a whole
  `<text>` (SVG 1.1 §10.7.2) — while still INHERITING from an ancestor, so a `<g writing-mode="tb">`
  around a `<text>` applies. The distinction matters because style resolves per character here: a
  tspan declaration that were honoured would turn only the glyphs it covers, mid-run.

  **19 of the 23 upstream `text/writing-mode/` fixtures are vendored**, goldens eyeballed. The four
  held back are CJK-set and render as `.notdef` columns against the bundled faces, so their goldens
  would lock in tofu as expected output; they land with a CJK face. See `docs/SVG.md`.

  **Not implemented:** multi-line stacking (one `<text>` is one vertical run, so `vertical-lr` places
  identically to `vertical-rl`); the deprecated `glyph-orientation-vertical`/`-horizontal`;
  letter/word-spacing on an *upright* vertical run, whose advance comes from the font's vertical
  metric rather than the spacing-adjusted horizontal one (a *sideways* run honours them).
- **Box-level RTL — tables, flex, grid** (`pkg/internal/layout/css` table/tableborder/flex/grid) — RTL
  slice 2 of 5, retiring **all three** "laying out LTR" logs. Tables mirror their solved column
  x-offsets, and `buildCollapsedBorders` flips its index→physical-side mapping — without that flip,
  collapsed borders resolve against the wrong neighbor with no log. Flex resolves direction in
  `axisFor`: a row XORs `reverseMain`, so RTL composes with `row-reverse`, and a column flips the
  CROSS axis, the case the old guard skipped silently. Grid mirrors track positions AND resolves
  `justify-items`/`justify-self` `start`/`end` logically; both flips are required, verified
  independently by mutation. This also fixes `crossOffset` ignoring the Box Alignment `start`/`end`
  spellings. Showcase §15 + 4 WPT reftests. (Text WITHIN a line is reordered by the next slice; at
  this point it still rendered in logical order.).
- **Arabic contextual shaping** (`pkg/internal/layout/inline/complex.go`) — RTL slice 4 of 5. A run of
  joining script — Arabic, Syriac, Thaana — is shaped as a whole segment through harfbuzz, which
  resolves the font's GSUB tables so letters take their initial/medial/final/isolated forms and
  ligatures fuse. Hebrew is non-joining and stays on the cheap per-rune path. `Face.OpenTypeFont`
  exposes the SFNT, which satisfies `harfbuzz.FaceOpentype` directly. Shaping is forced
  LEFT-TO-RIGHT so the pipeline stays logical up to the single L2 reorder; harfbuzz would otherwise
  emit visual order and be reversed twice. Glyphs carry their cluster's runes exactly once, so
  `/ToUnicode` neither duplicates nor drops text. Showcase §15 "Real script".
- **Bundled RTL faces + per-rune script fallback** (`pkg/internal/font/standard`, `pkg/internal/layout/font/cache.go`,
  `pkg/internal/layout/inline/shape.go`): Noto Sans Hebrew and Noto Naskh Arabic (both OFL 1.1, no Reserved
  Font Name) ship alongside the Latin substitutes. Each bundled face covers exactly ONE script — the
  Latin faces have no Hebrew or Arabic, the Noto faces have no Latin — so the covering face resolves
  per **rune**, not per run. A Hebrew or Arabic phrase inside an otherwise-Latin paragraph now shapes
  instead of being silently dropped. Results cache per (script, style), and the fallback consults
  bundled faces only. A fallback glyph carries the face it resolved from, since a GID is only
  meaningful against its own face.
- **Vertical font metrics** (`pkg/internal/font/program.go`, `family.go`): `Face.GlyphVAdvance` and
  `Face.VMetrics` expose `vmtx`/`vhea` alongside the horizontal `GlyphAdvance`/`Metrics`, for
  vertical writing modes. **The advance is normalized to a POSITIVE downward distance in em units**
  — the underlying library returns a negative one (it negates for a Y-down convention, so a
  1000-upem face reports -1000), and the flip happens once at the adapter so no caller carries the
  convention. A caller taking the raw sign would run the pen backwards up the page with nothing to
  catch it, so a test pins it. **`GlyphVAdvance` answers for every face:** where the font states no
  vertical advance — a TrueType face without `vmtx`, or a format carrying none at all like Type1 and
  bare CFF — one em is synthesized, which is the correct fallback and what browsers do. That is the
  COMMON path, not an edge case: **the bundled `sans-serif` and `serif` faces are Type1**, so nearly
  every HTML page takes it and only `monospace` carries real metrics. The synthesis lives here, where
  the em size is known, rather than in each caller — a caller improvising one would reach for the
  glyph's horizontal advance and space a vertical line by how wide each letter is. `VMetrics` keeps
  the distinction that matters, reporting `ok=false` unless the face genuinely carries `vhea`, so a
  synthesized metric is never presented as authored. Covered across bundled faces (Inconsolata
  `.ttf`, TeX Gyre Heros `.pfb`) and asserted for every generic family. `FontVExtents` panics on
  inconsistent tables exactly as `FontHExtents` does, so it carries the same `recover`. This retires
  the "needs `vhea`/`vmtx` reading `pkg/internal/font` does not have" blocker long cited for vertical text.
- **`.notdef` for unmappable runes** (`pkg/internal/font/notdef.go`, `pkg/internal/layout/inline/shape.go`): a rune that
  neither the run's family nor any script fallback can map now draws the tofu box instead of rendering
  as NOTHING. `Face.NotdefGlyph` follows the browser order. It takes the font's own glyph 0 when that
  has geometry — DejaVu draws a hollow box, Noto a box of hex digits — and otherwise synthesizes a
  hollow rectangle. That second branch is the one the bundled TeX Gyre substitutes take, since all of
  them ship a BLANK `.notdef`. The box carries a non-zero advance so line-breaking measures the text at
  its true width. `Glyph.Runes` is retained so bidi sees the character's real class and SVG's
  glyph→character mapping still locates it. `Glyph.Face` is cleared so every backend fills the same
  outline: handing a text backend GID 0 would emit the font's blank `.notdef`, making the box visible
  in a raster and invisible in a PDF of the same page. **Each distinct missing rune is warned about
  exactly once per `Shape` call**, through the `logf` the CSS engine and the SVG text path thread in.
  On the SVG side that logger is a constructor argument (`draw.NewWithLogf`, mirroring
  `layoutfont.NewOSFontProviderWithLogf`) rather than a field left to default, so a renderer that can
  draw a page of tofu is never silent by accident. All three entry points carry it — standalone
  `.svg`, inline `<svg>`, and an SVG background — each pinned end to end. The family named in the
  message is the one actually tried: SVG appends a generic fallback to every list, and since SVG's
  own initial family is `sans-serif`, `familyWithFallback` does not append a generic the list already
  ends with (it still does when the generic appears earlier but not last, since only the final entry
  makes the chain terminal). Invisible characters are excluded (`invisibleRune`): a space variant,
  format control, or variation selector draws no ink even where it IS mapped, so giving it a box would
  invent a mark the author never wrote. This repo's own showcase carries a U+202F that regressed exactly
  that way before the exclusion existed. It applies to the shared CSS/SVG text path; DOCX/PDF and any
  page whose glyphs all resolve stay byte-identical. Showcase §19.
- **Inline bidi reordering** (`pkg/internal/layout/inline/bidi.go`) — RTL slice 3 of 5. Shaping and breaking stay
  in LOGICAL order; once the break is chosen, `MakeVisualLine` applies UAX#9 rule L2 per line plus rule
  L4 bracket mirroring. `Glyph.Runes` keeps the ORIGINAL character, so `/ToUnicode` recovers the authored
  text. `golang.org/x/text` was promoted indirect→direct for UAX#9 — no new module. Line metrics are
  computed on the logical slice, because the space that ends the text reorders to an RTL line's visual
  START. Bidi control characters now survive shaping as zero-width glyphs; they were being dropped,
  silently discarding directional intent. (Arabic reordered correctly at this point but still rendered
  as isolated forms; the cluster model arrives in slice 4, below.).
- **Column flex vertical content sizing** (`pkg/internal/layout/css/flex.go`): a column container's main axis is
  vertical, so `flex-basis: auto`/`content` and the `min:auto` automatic minimum now resolve to the
  item's content HEIGHT rather than a max-content WIDTH compared against a vertical budget. The height
  is measured by laying the item out at its cross width and reading the fragment height back
  (`measureColumnMainContent`), the same two-phase pattern grid uses for row tracks. An auto-width
  column item's cross width is also clamped to the container, fixing a ~2.5x overflow for prose.
  Backlog H4.
- **Static form controls** (`pkg/internal/layout/css/control.go`): `<input>`/`<button>`/`<textarea>`/
  `<select>` render as static native widgets — classic chrome, non-interactive.
- **End-to-end "specimen" showcase** (`testdata/htmldoc/`, `htmldoc-*` goldens): one multi-file doc
  exercising every HTML/CSS/image slice, served over loopback HTTP via `OpenURL` + `WithPageSize`.

**DOCX frontend** (`OpenDOCXFile`/`OpenDOCXBytes`, `docx-*` goldens):

- **Parse + cascade** (`pkg/docx`, `pkg/internal/style`): the ZIP/OPC container, `document.xml`
  (paragraphs, runs, `w:t`/`w:br`/`w:tab`), run and paragraph properties, section geometry
  (`w:sectPr`), and the full `docDefaults → basedOn → direct` cascade.
- **Zip bombs are bounded in aggregate, not just per part** (`maxTotalPartBytes`, 512 MiB; the same
  budget in `pkg/internal/epub`): both readers already capped each part at 256 MiB, but both do bulk reads —
  `word/media/*` for DOCX, every container entry for EPUB — so N parts each just under the per-part
  cap multiplied. Measured, a 4 MB `.docx` holding 20 compressible media parts decompressed to
  **4.2 GB** and drove peak RSS to 6 GB, with every individual part inside its limit the whole way.
  Over-budget parts are dropped, so a hostile document degrades to missing images rather than
  taking the process down; DOCX takes parts in sorted order so the truncation is deterministic
  rather than following map iteration. PPTX and XLSX need no such budget — they fetch one part at a
  time and never accumulate.
- **CSS-engine convergence** (`pkg/internal/cssbox`): DOCX lowers straight to `cssbox` +
  `ComputedStyle` and runs through the shared CSS engine, with page geometry supplied as a
  synthesized `@page` stylesheet. The old flat model and engine are deleted.
- **DOCX fidelity**: lists/numbering, tables, images, headers/footers, and multi-section documents.
  Lowering lets most of these reuse the CSS engine's existing vocabulary.

**HTML/DOCX → PDF writer** (`pkg/internal/pdfwrite`, `WritePDF`):

- A second `render.Device` that emits a real PDF with **selectable/searchable text**. TrueType goes
  out as Type0/Identity-H CIDFontType2 with a glyf-subsetted `/FontFile2`; the bundled substitutes
  go out as simple `/Type1` with `/FontFile`. Every face carries `/ToUnicode`. Bands assemble
  concurrently, output is deterministic, and `@media print` is captured (`pkg/internal/css/media.go`). The
  raster corpus stays byte-identical, since the new `DrawGlyph` seam rasterizes via the outline.
- **`/ExtGState` resource emission**: a partially-transparent fill, stroke, glyph, or image now
  survives into PDF output as `/ca` (non-stroking) or `/CA` (stroking) alpha, and a non-Normal blend
  mode as `/BM`. A scoped `q`/`Q` wraps the state so it never leaks into later content. States
  deduplicate by content, so many shapes sharing one alpha/blend emit a single `/ExtGState`
  resource. Fully-opaque, Normal-blend output is unchanged byte-for-byte — no resource and no `gs`
  operator are emitted.

**Any → SVG writer** (`pkg/internal/svgwrite`, `WriteSVG`, CLI `convert <in> <out>.svg`):

- A third `render.Device` that serializes paint operations to SVG markup. It works for **every**
  input format, PDF included, because all three frontends already paint through `render.Device` —
  the PDF content interpreter, the reflow paint layer, and the SVG painter alike — so no
  per-format work exists. Output is **genuinely vector**: `<path>` geometry, `<clipPath>`,
  `<mask>`, and native `<linearGradient>`/`<radialGradient>`, not a rasterized page wrapped in an
  `<image>`. Device space is already top-left/Y-down, matching SVG's user space, so there is no
  page-level flip (contrast pdfwrite's `1 0 0 -1 0 H cm`).
- Conversion goes through `vectorPages`, **not** `reflowPages`: the latter yields `*layout.Pages`,
  which an opened PDF lacks, so a writer built on it would be reflow-only. `raster.RunPage` shares
  the PDF page matrix/resource setup between the raster and SVG backends, so a page rendered to
  SVG and the same page rasterized cannot drift.
- **Text is glyph outlines, not `<text>`** — stated plainly rather than approximated. The pipeline
  carries enough identity for real text on the reflow path (`GlyphRef` has `Face`/`GID`/`Runes`),
  but the bundled substitutes are TeX Gyre Type1 `.pfb`, which browsers cannot load via
  `@font-face`, and the repo has no WOFF/WOFF2 encoder — so `<text>` would render with whatever
  the viewer happened to substitute. Outlines render identically everywhere. Each unique
  (face, glyph) outline is defined once in `<defs>` and referenced with `<use>`, which measured
  **9.2× smaller** output on an HTML text page (1.09 MB → 118 KB) versus inlining transformed
  copies. Each `<use>` carries an `aria-label` with the source characters it stands for, so the
  text stays recoverable by a screen reader or a scraper. Output is not text-selectable; that cost
  is the trade.
  - **The hoisting applies to reflow input only** (HTML/DOCX/SVG), not to PDF. A PDF's text
    reaches the Device through `FillGlyph` with an already-flattened, already-positioned outline
    (`pkg/internal/content/showtext.go`) rather than through `DrawGlyph`, so there is no glyph identity
    to key a definition on and PDF text emits inline paths. Closing that needs the same new
    interpreter seam the deferred `<text>` mode would — see `docs/SVG.md`.
- One page per document, like an image: `SVGOptions.Page` selects it on the generic `Convert`
  path, and the CLI reuses the same `%d` fan-out, page-selection flags, and per-page error
  handling as image output.
- **Canvas background follows the same precedence as rasterization** — a CSS-propagated
  root/body background (the browser's background-propagation rule) wins over
  `SVGOptions.Background`. Only the fallback differs, deliberately: with neither set the page
  stays **transparent**, where rasterization commits to opaque white, since a vector document
  composited over an unknown backdrop should not carry an assumed backdrop of its own.
- **Tested three ways.** Committed **text goldens** of the emitted markup
  (`testdata/golden/svgwrite/`, regenerate with
  `go test ./pkg/omnidoc -run TestSVGWriteGolden -update`) catch output that renders the same but
  says something different — a lost `fill-rule`, a switch from `<use>` back to inline paths —
  which a pixel check cannot see; output is asserted deterministic so they cannot flake. A
  **round-trip** sweep over `gen.Core` writes SVG, re-reads it through `pkg/internal/svg`, rasterizes, and
  diffs against a direct raster. The **htmldoc showcase** (`TestHTMLDocSVGShowcase`) runs all 44
  pages of the specimen document through that same loop against the existing `htmldoc-p*.png`
  raster goldens — no new goldens, so any raster-vs-SVG disagreement shows against a
  known-good reference. 37 of 45 pages match within the standard budget; the 8 pages embedding
  an `<image>` are bounded loosely (the reader cannot draw it back), and the budget is 0.3%
  rather than 0.2% for a measured, documented reason: a gradient swatch's single antialiased
  edge row differs between the two rasterizations, interiors byte-identical.
- Degradations, each logged once: a blend mode with no CSS `mix-blend-mode` equivalent paints
  source-over; a `Shader` that cannot describe itself (mesh shadings, and **PDF-sourced shadings**,
  which are deliberately not self-describing upstream) is sampled and embedded as an `<image>`.
  Masks and filters likewise embed a bitmap rather than being dropped — `svgwrite` borrows
  `pkg/internal/raster`'s rasterizer for `BuildClipMask`/`BuildLuminanceMask`/`RenderOffscreen`
  instead of taking the degradations `render.Device` permits a vector backend, so filters work on
  SVG output where they cannot on PDF output.
- **Masks emit `mask-type="alpha"`, with coverage in the alpha channel.** A `GroupMask` is already
  final coverage (reduced via sRGB Rec. 709, per this engine's documented choice of sRGB over SVG
  1.1's linearRGB — see `pkg/internal/svg/mask.go`). Encoding it as gray under the default *luminance*
  mask-type would make the viewer convert it a second time, in linearRGB, turning coverage 128
  into 55 — an error of 73/255 that reads as a far-too-dark mask. `mask-type="alpha"` takes the
  channel verbatim with no conversion in any viewer; the RGB channels are white so a viewer that
  ignores `mask-type` degrades to a too-permissive mask rather than an invisible one.

**HTML/DOCX → Markdown & plain text** (`pkg/internal/markdownwrite`, `WriteMarkdown`
+ `WriteText`, CLI `convert .. out.md`):

- A conversion backend that walks the shared `cssbox` tree rather than the paint seam, because it
  needs structure rather than glyphs. One walker therefore serves both HTML and DOCX. Both frontends
  capture small additive semantic annotations on `cssbox.Box` — `SemTag`/`HeadingLvl`/`Href` — which
  carry the facts computed style drops: heading level, link URLs, and DOCX style identity. Layout,
  raster, and PDF ignore them, so those paths stay byte-identical. The output is GFM: headings,
  bold/italic/strikethrough/code, links, images, blockquotes, fenced code, nested and task lists,
  thematic breaks, and **high-fidelity pipe tables** with alignment, captions, and colspan/rowspan
  expanded by content duplication.

**PDF → Markdown & HTML** (`pkg/internal/extract`, `pkg/internal/htmlwrite`, `WriteHTML`,
CLI `convert in.pdf out.md` / `out.html`):

- Structure recovery from a PDF's positioned glyphs and vector paths. The content interpreter gains
  optional, paint-neutral capture sinks — `content.Options.TextSink`/`GraphicsSink`, where nil keeps
  output byte-identical. `pkg/internal/extract` reconstructs words→lines→**XY-cut** reading-order blocks,
  handling columns, and does **automatic table recognition**: lattice from ruling lines, stream from
  whitespace, auto-selected between them. It all lowers to a synthetic `cssbox` tree the Markdown
  writer reuses. A new `pkg/internal/htmlwrite` serializes `cssbox`→HTML with native
  `colspan`/`rowspan`. PDF `Document` satisfies `reflowTree` via lazy extraction. ToUnicode CMaps
  (Type0/CID text), font weight/slant, and scanned-PDF OCR are follow-ups.
- **Images are carried through** (`pkg/internal/extract/images.go`). The extractor's device records every
  `DrawImage` the interpreter issues, and image XObjects and inline images decode through the same
  `raster.ResolveImage`/`ResolveInlineImage` a page render uses. Each image lowers to a block-level
  `<p><img src="data:…">` placed among the prose by its top edge and column (an image in the left column
  of a two-column page lands in the left column's flow; a page of only pictures, like a scan, yields just
  them). The CTM's flips and rotations, `/Rotate` included, are baked into the pixels because an `<img>`
  carries no transform. Opaque images encode as JPEG when that is under half the PNG size, else PNG;
  translucent ones as PNG. Because the tree is shared, Markdown, HTML, DOCX, PPTX, RTF, and EPUB all carry
  the picture (the PDF backend now supplies a `data:` loader to the media-embedding writers; EPUB keeps
  the `data:` source inline like every EPUB it writes); plain text and CSV/TSV/XLSX drop it by design.
  Marks under 2pt on a side (a stretched 1-pixel spacer) are skipped with a debug log. A clip path crops the
  picture to the clip's BOUNDING BOX (nested clips intersect, q/Q scopes them), and an image the clip hides
  entirely is skipped with a log; a non-rectangular clip (a circle crop) keeps its whole bounding square.
  Not honored, and silent: blend modes and constant alpha on a picture.
- **Right-to-left text extracts in LOGICAL order** (`pkg/internal/extract/bidi.go`). A PDF stores glyphs
  by POSITION, so sorting a line left-to-right yielded RTL script reversed. Each maximal RTL run is
  reversed back at BOTH levels the PDF mirrors: the characters within a word, and the order of
  consecutive RTL words. This runs after word grouping, which splits on x-gaps and would break on
  reordered glyphs, so each word keeps the geometry that table/block detection needs. No-op for
  Latin.

**Unified conversion core** (`pkg/omnidoc/format.go`+`detect.go`+`open.go`+`convert.go`+
`image_backend.go`, CLI `convert`):

- One `Format` type and a capability table: `CanConvert`, plus typed `ErrUnknownFormat`/
  `ErrUnsupportedFormat`/`ErrSameFormat`. `DetectFormat` is content-first — magic, then the
  extension hint, then a WHATWG HTML sniff, with no UTF-8⇒text fallback. `Open`/`OpenBytes` sniff
  any supported format and the PDF path stays byte-identical; `OpenAs`/`OpenBytesAs` skip detection.
- **Branchable errors, not string matching**: `ErrNoStructure` (the document carries no box tree the
  structure writers can walk — an SVG, whose renderer lays out pages but keeps no tree) and
  `ErrPageOutOfRange`. Both are wrapped by every site that raises them, across nine writers that
  previously returned bare `fmt.Errorf` in two different wordings. `omnidoc.ErrSheetNotFound` is now
  an alias of `xlsx.ErrSheetNotFound` rather than a second value meaning the same thing — they were
  distinct sentinels whose messages differed by one colon, so `errors.Is` between them was false in
  both directions and a caller using both packages would write a check that compiled and never
  matched. Wiring `ErrNoStructure` surfaced a live bug: `svg → md` used to write an empty file and
  exit 0, because the nil box tree passed a type assertion that only tested the interface.
- **`WithContext(ctx)`** bounds open-time box generation, resource loading and layout on **every**
  `Open*` entry point, and for `OpenURL` the HTTP fetch of the page itself. It replaced, and then
  retired, a parallel `*Context` naming family that covered HTML and URLs only. A caller's own
  `WithContext` outranks the one the path-taking openers prepend, and a nil ctx is ignored.
  Every opener stamps `Document.Format()`. Generic `Convert`/`ConvertFile`/`(*Document).Write`
  dispatch any valid input→output pair; the legacy `ConvertXToY` wrappers were shims pinned
  byte-identical and have since been removed. Same-format conversion is a deliberate
  `ErrSameFormat`, and only on the generic path. PNG/JPEG/WebP are output formats via
  `WriteImage`/`EncodeImage`: Convert-to-image writes one page, and multi-page goes through CLI `%d`
  fan-out. SVG is both an input and an output format (`WriteSVG`), sharing the image path's
  one-page-per-file shape and `%d` fan-out. The CLI is `convert <in> <out>` with `--from`/`--to`, and all subcommands share one
  detection-based opener — so rasterize no longer assumes unknown extensions are PDF, and
  `--print` actually applies print media now. A new format lands by flipping its capability bit and
  adding one switch case in `openDetected`/`Write` — see the sibling contract in.

**Markdown + plain-text input** (`pkg/internal/markdown` via goldmark (MIT, pure Go, zero transitive
deps), `pkg/omnidoc/markdown_frontend.go`+`text_frontend.go`):

- Both `.md` (CommonMark + GFM: tables, strikethrough, task lists, autolinks, raw-HTML
  passthrough) and `.txt` (escaped `<pre>` + `pre-wrap`; hard line breaks preserved, long
  lines soft-wrap, and.txt→.md is a lossless fenced block) open through the HTML pipeline
  via `OpenMarkdown*`/`OpenText*`. Every `HTMLOption` applies, and md→md round-trips are a
  fixed point. Detection is extension-only, with no content magic — the hint step outranks
  HTML sniffing by design. Landed with a cross-cutting inline-core fix: empty forced lines
  (blank lines in pre/pre-wrap/pre-line) now get a CSS strut height instead of collapsing
  (`pkg/internal/layout/inline` shape/break). All prior goldens stayed byte-identical.
- **Bounded block nesting** (`maxBlockNesting`, 1024 markers per line; `ErrTooDeeplyNested`):
  goldmark's block parser is quadratic in the containers opened on ONE line — 50,000 `- ` markers
  take 10.8s, and 200,000 (a 400 KB file) never finish. It is DEPTH that costs, not size: the same
  50,000 items spread over 50,000 lines parse in 79ms, so the bound is per line rather than a
  document-size cap that would reject large legitimate files. A linear pre-scan counts the leading
  marker run and declines the source before goldmark sees it. Fenced code blocks are skipped — a
  README showing example Markdown inside ``` opens no containers, and costs 238µs rather than
  seconds.

**DOCX writer** (`pkg/internal/docxwrite`, `WriteDOCX`, CLI `convert.. out.docx`):

- Everything →.docx: HTML, Markdown, text, and PDF via extraction. It is a cssbox STRUCTURE
  writer — boxwalk-based like the Markdown one, and not layout-faithful — emitting native Word
  constructs chosen so our own reader round-trips them: HeadingN pStyles (plus rPr scale),
  direct-rPr emphasis, `w:hyperlink` + External rels, Quote/CodeBlock/HorizontalRule styles (the
  reader maps the latter two to pre/hr, and `w:rStyle` is now parsed so CodeChar marks inline code),
  one-paragraph code blocks via `w:br`, per-instance ordered-list numbering, and deterministic OPC
  output. Backed by a round-trip parity matrix (HTML→docx→md ≡ HTML→md), reopen-verified units, and
  a `docxout-basic` golden. Landed with a cross-cutting lowering fix: consecutive DOCX list
  paragraphs now group into nested list-container boxes, so mixed bodies no longer drop non-list
  content from Markdown/HTML conversion and nested lists keep their depth. Tables ship natively —
  `boxwalk.BuildOccupancyGrid` → `w:tbl`/`gridSpan`/explicit `vMerge` chains, per-cell
  borders/shading, and `tblHeader` rows, with a lowering addition marking header-row cells bold so
  headers round-trip; captions become a bold Caption style. Images embed as deduped media parts and
  `wp:inline` drawings, fetched through a new `reflowResources` loader seam; with no loader they
  degrade to alt text plus a log. Pinned by the round-trip parity matrix including tables, the
  `docxout-basic`/`docxout-htmldoc-p1` goldens, and the `htmldoc.docx.md` showcase round-trip
  golden.

**CSV/TSV input + output** (`pkg/omnidoc/csv_frontend.go`, `pkg/internal/csvwrite`,
`OpenCSV*`/`OpenTSV*`, `WriteCSV`/`WriteTSV`):

- Input: stdlib `encoding/csv` (lazy quotes, ragged rows padded, BOM/CRLF) becomes an HTML table
  with the first row as header, through the reflow pipeline. CSV and TSV are distinct formats, so
  csv ⇄ tsv are real conversions; detection is extension-only. Output: a tables-only structure
  writer over the boxwalk occupancy grid. Spans are duplicated (the GFM strategy), multiple tables
  are blank-line separated, prose is dropped and logged, and table-less documents produce empty
  output plus a loud log. That writer is what makes **PDF → CSV table extraction** work through the
  existing lattice/stream recognizer, pinned by test. `csv-specimen` golden.

**XLSX input** (`pkg/xlsx` hand-rolled reader + `pkg/omnidoc/xlsx_frontend.go`,
`OpenXLSX*`, `testdata/gen/xlsx` fixture builder):

- Read-only cached-value extraction, with no formula evaluation; the dep audit that ruled out
  excelize/tealeg is in the spec. Covers an OPC container mirroring `pkg/docx/zip.go`, shared and
  rich strings, styles (bold/italic/fill/alignment), dates via builtin plus heuristic numFmt codes
  against the 1900 (Lotus-leap-safe) or 1904 epoch, General/percent number rendering, and
  `mergeCells` → native spans. Hidden sheets are skipped, but hidden rows and columns still render
  because they are view state, not data. Visible sheets become `<h2>`-headed ruled tables through
  the HTML pipeline, and a bold first row becomes the header row via the writers' existing
  detector. ZIP detection is generalized to an OPC classifier: `word/`→DOCX, `xl/`→XLSX.
  `xlsx-specimen` golden.
- **Sheet selection** via the `WithSheets(names..)` open option and the repeatable,
  comma-separated `convert --sheet` CLI flag: render only the named worksheets, in the given order,
  instead of every visible sheet. A single selected sheet drops its heading, an explicitly named
  hidden sheet renders, and an unknown name fails with `ErrSheetNotFound`. `WithSheets` is a
  universal `OpenOption` and stays inert for non-XLSX inputs; the option type is now `OpenOption`,
  with `HTMLOption` kept as a back-compat alias.

**XLSX output** (`pkg/internal/xlsxwrite`, `WriteXLSX`, `convert.. out.xlsx`):

- A tables-only writer sharing `boxwalk.CollectTables`/`CellPlainText` with csvwrite. It emits one
  worksheet per table with caption-derived names (sanitized, unique, 31 characters), native
  `mergeCells` spans, a bold header xf that the reader's header detector picks up so headers
  round-trip, inlineStr and numeric cells, and deterministic OPC. Clean numbers stay numbers, which
  makes csv→xlsx→csv byte-identical, while `007` stays text. Table-less documents write one empty
  sheet plus a loud log. Round-trip parity runs through the `pkg/xlsx` reader, and pdf→xlsx
  extraction is pinned. v1 punts: alignment/fill write-back, typed date cells.

**Stream + MIME input surface** (`pkg/omnidoc` format.go/open.go, first tinycld-adoption PR):

- `FormatFromMIME`/`Format.MIME()` strip params and case-fold. Explicit-Unknown pins cover legacy
  binary Office — never the OOXML cousins — plus HEIC *sequences*, zip, and octet-stream;
  `image/webp` was such a pin until the WebP reader landed. Unlisted `text/*` maps to FormatText,
  with `text/rtf` excepted. Those rows flip to PPTX/EPUB/RTF when those frontends land. The
  `OpenReader`/`OpenReaderAs(ctx,..)` stream entry points are fully buffered and thread a real
  open-time context through layout, so a cancelled open ERRORS rather than returning a silently
  truncated document — that is a boundary check, while the engine itself degrades.
  `Convert`/`ConvertFile` now pass their ctx to open. `MarkdownOptions.MaxBytes` is a rune-safe cap
  on text output, for search-index extraction. The capability gate for hosts is
  `FormatFromMIME(mt).ValidInput()`.

- **Cancellable HTML render, end to end.** `WithContext` reaches every HTML entry point
  (`OpenHTMLBytes`, `OpenHTMLFile`, `OpenURL`), and for `OpenURL` bounds the HTTP fetch of the page
  itself as well as the layout. Threading a live context changes no pixel and no page dimension
  versus the same open without one (pinned by test).
  Rasterization now actually honors its context: `reflowRenderer.renderPage` took
  `_ context.Context` and dropped it, so `RasterizePage` advertised a cancellation it never
  performed. It now checks before the allocation/paint and again after paint. Layout gained the two
  checks that bound the real worst case. The first is `inline`'s new `ShapeContext`, checked
  between runs and every 1024 runes, because shaping runs BEFORE line breaking and was therefore
  the longest uninterruptible stretch. The second is a per-line check in the CSS engine's
  `layoutInline`, since a single huge paragraph is one block child that the pre-existing
  between-children check never revisits. Measured on a ~3s pathological layout, cancellation
  latency went from ~2.6s to ~2ms, and the normal path is unchanged (benchstat over 12 runs: raster
  -1.3%, shape -2.0%, open flat). Cancellation degrades in the engine and hardens to an error at
  the open boundary, so a truncated document is never returned silently.

**DOCX reader fidelity — the public-model PR 1/3** (`pkg/docx`, toward a supported read+write
document model consumed externally by tinycld/text):

- New model coverage: tracked changes (w:ins/w:del as `ParaChild.Revision` containers, `w:delText`,
  rPr/pPr/tcPr `*Change` before-states, cellIns/cellDel), comments (part, range markers, and
  reference runs, with markers inside hyperlinks hoisted outward), endnotes (a `Notes` container
  with exported `ByID` and a shared footnote/endnote parser), drop-cap frames, anchored-drawing
  wrap facts, `Border.Style` names, paragraph-attached `SectPr`, a numbering restructure
  (`Abstract`/`Instances`/`Start`/`StartAt`), run `Shd`, `Relationship.Type`, `Hyperlink.Target`
  resolved at parse, `Style.Name`, and `Document.ExtraParts` holding customXml verbatim. Rendering
  pins: revisions render the FINAL state ("No Markup"), comments stay invisible, and the drop cap
  degrades. Rendering upgrades: endnote markers, run shading, list start/override seeding, and
  anchored square-wrap images as CSS floats. `fidelity` core fixture + golden.

**DOCX model writer — the public-model PR 2/3** (`pkg/docx` `Write`/`Bytes`):

- **Context-bounded, reader and writer.** `Open`/`OpenBytes`/`OpenReaderAt` and `Write`/`Bytes`
  take a `context.Context` first. The parser checks it per body block and between parts, the
  writer between assembled parts and before each is compressed, so either direction fails with
  `context.Canceled` instead of running to completion on a document the caller has given up on.

- A full-vocabulary deterministic OPC emitter living in pkg/docx itself: stdlib-only,
  schema-ordered props, rels preserved with structural/hyperlink rels allocated, tabs/delText/
  xml:space mirrored, a Word-complete drawing scaffold, and zero SectionProps falling back to
  Letter defaults. `ErrInvalidDocument` hard-fails rather than dropping content. Adds
  `DefaultStyles()`/`AddImage` constructors, and the package doc declares the vocabulary plus an
  additive-only stability promise. The Parse∘Write ≡ id round-trip contract is pinned by a
  15-fixture modelCore corpus, a 200-doc seeded randomized sweep, per-fixture determinism, and a
  byte-level second-write fixed point over the gen corpus. The `model-specimen` core fixture
  renders the construct→Write→reopen path into a golden.

**XLSX reader enrichment — calc-adoption PR 1/5** (`pkg/xlsx`):

- An additive structured read surface. `Cell.Value` is typed Empty/String/Number/Bool/Date/Error,
  with dates going through the shared serial logic. `Cell.Formula` EXPANDS shared formulas per
  member via a lexical A1 shifter: anchors stay fixed, literals and sheet names are opaque, and "("
  marks a function call. `Cell.StyleID`/`Cell.Style` carry the full
  font/fill/alignment/border-with-diagonal/numFmt/protection vocabulary, and Color keeps rgb OR
  indexed OR theme+tint. Sheet facts cover the visibility enum, tab color, frozen panes, sparse row
  heights, row styles, col widths, and defaults. Also workbook `Date1904` + `DefinedNames`, 1-based
  coordinate helpers, and the complete builtin numFmt id table. The display path stays
  byte-identical: Text is untouched and the formatter keeps its subset.

**XLSX preservation-first editor core — calc-adoption PR 2/5** (`pkg/xlsx` `Edit`/`New`/`Save`):

- **Context-bounded.** `Open`/`OpenBytes`/`Edit`/`Save`/`SaveTo` take a `context.Context` first;
  the reader checks it per sheet and per `<row>`, `Save` per part, so a hostile workbook is
  abandoned mid-parse with `context.Canceled` rather than wedging the caller. `Edit` only indexes
  the package (parts parse lazily on access), so its context bounds the open alone.
- Open-mutate-save under the strongest preservation contract. Untouched parts copy byte-verbatim at
  the zip layer, so a no-op Edit+Save is part-for-part byte-identical and reads never dirty
  anything. Dirty parts re-serialize through `internal/xmlpart` on pinned beevik/etree settings:
  unknown elements, attrs, and prefixes survive in order, DOCTYPE is rejected, and the keystone
  parse→serialize→reparse tree-equal property is fuzzed. Sheet ops cover add/delete/move/rename and
  visibility with last-visible guards, plus tab color. Typed cell writes are SetString (inlineStr),
  SetNumber, SetBool, and SetDate with xf-clone date-format ensuring, alongside an ATOMIC
  SetFormula(src, cached). ClearCell keeps style, Cells iterates, and merges, frozen panes,
  dimension, row heights, and col widths all range-split. A stale calcChain is dropped on the first
  value edit — part, CT, and rel. Saves are deterministic; the editor is single-goroutine and uses
  1-based coordinates.

**XLSX style read-modify-write — calc-adoption PR 3/5** (`pkg/xlsx` `PatchCellStyle`):

- The patch-not-replace overlay contract. `StylePatch` is all-pointer-leaf — fonts, fills,
  alignment, borders-with-Clear, numFmt, and `*""` clears — and applies by CLONING the cell's xf
  plus its font/fill/border records and editing nodes. Unmodeled facets therefore ride along:
  diagonal borders, indent, rotation, protection, theme colors, and unknown children such as font
  `scheme`, all pinned by test. Records dedupe semantically via `xmlpart.Equal`. numFmt patterns
  reuse builtin ids deterministically, then reuse custom codes, and otherwise allocate ≥164. Adds
  whole-style `SetCellStyle`, row-style variants, and memoized `CellStyle` reads. A per-leaf canary
  audit covers both the editor read AND save/reopen, mirroring calc's style_attribute_registry.

**RTF input** (`pkg/internal/rtf`, `OpenRTF*`, `convert in.rtf..`):

- A dependency-free tokenizer and converter producing HTML through the reflow pipeline. It handles
  paragraph and character formatting with 0-toggles, font and color tables, alignment and indents,
  cp1252 plus `\uN`/`\ucN` escapes, hyperlink fields, `\trowd` tables, `\pngblip`/`\jpegblip`
  pictures as data: URIs (other picture types are logged and skipped), and `\paperw`-family page
  geometry lowered to `@page`. The degrade story is the RTF resilience rule: unknown words are
  skipped and unknown `{\*}` destinations ignored. Wiring: `{\rtf` magic, `.rtf`, MIME rows
  flipped, and the input capability bit. Landed with a cross-cutting engine fix — **data: image
  URIs decode without a resource loader**, because `resource.LoadDataURL` short-circuits the image
  cache, which is the browser rule. `rtf-specimen` golden.
- **Bounded list nesting** (`maxListLevel`, 64): the HTML emitter opens one `<ul>`/`<ol>` per
  `\ilvl` level, so an unbounded value is an unbounded write rather than a deep list — a 34-byte
  document with `\ilvl2000000000` never finished, and `\ilvl100000` turned 30 bytes into 1.4 MB of
  markup. RTF allows levels 0–8 and Word exposes nine, so the clamp cannot reach a real document.

**RTF output** (`pkg/internal/rtfwrite`, `WriteRTF`, `convert.. out.rtf`):

- Everything →.rtf, through a cssbox STRUCTURE writer that is boxwalk-based in the Markdown/DOCX
  shape, with mappings chosen so our own reader round-trips them. Block semantics ride on
  stylesheet names — `\sN` "heading N" plus `\outlinelevel`, and Quote/CodeBlock/HorizontalRule —
  and the reader now parses the stylesheet and maps the names back, which also upgrades real Word
  files. Lists use `\ls`/`\ilvl` plus a literal `\pntext` marker, and the reader now captures those
  markers into nested `<ul>`/`<ol>`. Inline code rides on the monospace font, which the reader maps
  back to `<code>`. Also HYPERLINK fields, `\trowd` tables with `\trhdr` header rows that the
  reader turns into `<th>`, spans DUPLICATED into covered slots (the GFM strategy, so
  round-tripped grids match direct conversion), captions as a bold line, `\pict` png/jpeg (data:
  URIs embed loaderless and round-trip byte-identically), and `\uN?` escapes including surrogate
  pairs. Output is deterministic. Pinned by a 17-case html→rtf→md ≡ html→md parity matrix, md/pdf
  loops, and the `rtfout-basic` golden; RTF is in the convert matrix as input AND output.

**PPTX output** (`pkg/internal/pptxwrite`, `WritePPTX`, `convert.. out.pptx`):

- Everything →.pptx, through a cssbox STRUCTURE writer. Every `<h1>`/`<h2>` starts a new slide with
  that heading as the title placeholder, and the following blocks become the body: text box
  paragraphs, `buChar`/`buAutoNum`+`lvl` lists, native `a:tbl` with `gridSpan`/`rowSpan` plus
  `hMerge`/`vMerge` continuations, and `p:pic` media parts with loaderless data:-URI embedding.
  Logged degrades: h3–h6 become bold paragraphs, quote and code flatten, links drop their targets,
  and hr is skipped. OPC output is deterministic, in the gen-fixture package shape. Pinned by
  reopen-verified per-construct round trips through pkg/internal/pptx, a slide-count pin, and the
  `pptxout-basic` golden; PPTX joins the convert matrix as input AND output. Landed with a D1
  frontend fix the round trip exposed: a nested-list `<ul>` now opens INSIDE its parent `<li>`,
  which structure writers previously dropped nested items over.

**EPUB output** (`pkg/internal/epubwrite`, `WriteEPUB`, `convert.. out.epub`)
— **completes the any⇄any table: every format is both an input AND an output** (fifteen today, with HEIC the one input-only addition since):

- Deterministic EPUB 3 built ON htmlwrite, since content documents ARE XHTML: a new
  byte-identical `XHTML` mode self-closes voids, and a new `ImageSrc` hook rewrites srcs during
  serialization. The package carries a stored `mimetype` first entry, container.xml, an OPF (title
  from the option, else the first `<h1>`, else "Document"; fixed dcterms:modified), and a
  `nav.xhtml` TOC. Chapters split at `<h1>`, and a heading-less document becomes one chapter.
  Images land as deduped manifest items via the loader seam, while data: URIs stay inline and
  round-trip verbatim. Pinned by the STRICT parity bar — 17-case html→epub→md ≡ html→md exact
  equality — plus package-shape pins (stored-mimetype-first, nav links, chapters ⇒ pages), an
  md→epub→md loop, and the `epubout-basic` golden; EPUB joins the convert matrix as input AND
  output.

**DOCX writer unification** (`pkg/internal/docxwrite` → `docx.Write`) — the public-model
PR 3/3:

- docxwrite's cssbox walk now BUILDS a `*docx.Document` — DefaultStyles/AddImage plus model
  paragraphs, runs, hyperlinks, tables, and drawings, with parse-shaped runs — and serializes via
  `docx.Write`. That leaves ONE OPC emitter for the repo: docxwrite's private XML/zip machinery
  (opc.go, styles.go, numbering.go, xml.go) is deleted, while the public `docxwrite.Write` API is
  unchanged. Facts the old XML carried land as additive model fields, each parsed, written, and
  fixture-covered: `ParagraphProps.Borders` (w:pBdr, so HorizontalRule keeps its visible rule),
  `NumLevel.IndentLeft/Hanging` for per-level list indents, and `TableProps.LayoutFixed`. The
  result is semantically 1:1 — the parity matrix and reopen units are unchanged and the PNG
  goldens are byte-identical. One improvement: a linked image now survives the round trip BESIDE
  its link group, where the reader used to drop drawings inside w:hyperlink; pinned by test.

**PPTX input** (`pkg/internal/pptx`, `OpenPPTX*`, `convert deck.pptx..`):

- A hand-rolled PresentationML reader covering visible slides' shape trees: text frames with
  level, bullet, and alignment plus run b/i/sz/color, pictures, and spanned tables. Frames resolve
  through slide→layout→master placeholder inheritance, and hidden slides are skipped. Animations,
  SmartArt, and themes are out of scope, though content still extracts. The frontend renders one
  fixed-size page per slide with absolutely positioned frames, pictures as data: URIs, titles as
  h2, and bullets as nested ul/ol with kind-switch handling; shapes are ordered title-first and
  top-down for the structure writers. `classifyOPC` gains ppt/; `.pptx`/`.pptm` are recognized;
  the presentationml MIME row is flipped; and the input capability bit is set (output = D2).
  `pptx-specimen` golden.

**EPUB input** (`pkg/internal/epub`, `OpenEPUB*`, `convert book.epub..` — reverses the old
out-of-scope note):

- A container reader: container.xml leads to the OPF for title, manifest, and spine, skipping
  `linear="no"`; EPUB 2 and 3 both go through the spine, and NCX is ignored. Spine documents' body
  markup concatenates in reading order, with each chapter getting `break-before: page` when
  paginated, alongside collected package CSS and inline styles. Images, fonts, and linked CSS
  resolve from the container through a loader adapter following the OPF-directory layout every
  real-world book uses; the dir-loader default is skipped so the container loader wins. DRM
  (META-INF/encryption.xml) surfaces as a typed `epub.ErrEncrypted`. Detection uses the OCF
  `mimetype` zip entry in classifyOPC and `.epub`; the MIME row is flipped and the input capability
  bit set (output = E2). `epub-specimen` golden.

**PNG/JPEG input — images as documents** (`OpenImage*`):

- The any⇄any principle applied to the image formats. An image opens as a single page exactly its
  pixel size, with the format stamped from the actual encoding via DecodeConfig and the pixels
  embedded as a data: URI through the reflow pipeline. image→PDF fills the page edge to edge
  (pinned), image→JPEG transcodes, markdown carries a data: URI, and plain-text and tables-only
  outputs are empty by design. png→png stays ErrSameFormat. Input capability bits flipped;
  conversion matrix extended.

**HEIF/HEIC input — pure-Go HEVC intra decoder** (`pkg/heif`, `pkg/internal/hevc`):

- A from-scratch, in-tree HEVC intra-only decoder sitting beneath a HEIF container layer. The
  decoder covers CABAC, all 35 intra modes, residual coding with sign hiding, deblocking, SAO, and
  WPP plus tiles — the full toolset real HEIC encoders emit — for Main/Main 10/Main Still, 4:2:0,
  8/10-bit. The container layer handles grids of tiles, auxiliary alpha, irot/imir/clap, and nclx
  colour. It is bit-exact against reference decodes on a 42-fixture corpus, and WPP rows and tiles
  decode in parallel with byte-identical output. Registered with `image.RegisterFormat`, `.heic`
  lights up as a document input, works inside HTML/EPUB `<img>`, and transcodes to PNG inside
  DOCX/PPTX/RTF/EPUB outputs (`pkg/internal/imageconv`). Image *sequences* (msf1) and AVIF stay
  refused.

**WebP input + output** (`pkg/internal/webp`, over `golang.org/x/image/webp` for decode — BSD, already an
approved dep — and `github.com/HugoSmits86/nativewebp` for encode — MIT, pure Go, whose only
dependency is `x/image`, so no new transitive surface):

- Still WebP decodes everywhere the other raster formats do. `.webp` is a document input
  (`FormatWebP`, MIME `image/webp`), content-first `DetectFormat` recognizes it by magic — the RIFF
  `WEBP` form type, so WAV/AVI stay unknown — it works inside HTML/EPUB `<img>` by content type or
  by sniffing, and it transcodes to PNG inside DOCX/PPTX/RTF/EPUB outputs (`pkg/internal/imageconv`).
  Lossy VP8, lossless VP8L, and the extended VP8X container with an alpha plane are all covered.
- **Animated WebP returns a typed `ErrAnimated`.** `x/image/webp` handles stills only, and its
  failure mode is misleading: `DecodeConfig` parses VP8X, ignores the animation flag, and returns
  the canvas size with NO error, while `Decode` then fails with a bare `webp: invalid format` —
  the same error corrupt bytes produce. `pkg/internal/webp` reads the flag upstream skips and returns
  `ErrAnimated` from both entry points, and `OpenImageBytes` and `TranscodeToPNG` check it, so a
  valid animation is reported as unsupported instead of broken.
- The `image.Decode` **sniffing** path cannot carry that check. `image.sniff` returns the first
  registered format whose magic matches, in registration order, and an importing package's `init`
  always runs after the package it imports — so no registration here can outrank `x/image/webp`'s.
  Code that must not be fooled by an animated file calls `webp.Decode`/`webp.DecodeConfig`, or
  `webp.IsAnimated` on the bytes; the two call sites that matter do. A test pins the upstream
  behavior so this gets revisited if it changes.
- **Output is lossless VP8L**, wired as a full image target: `Convert`/`WriteImage`/`EncodeImage`
  to `FormatWebP`, the CLI's `rasterize --format webp`, and any `.webp` output path, which also
  infers the `rasterize` subcommand. Round-trips are verified **pixel-exact** against `x/image`'s
  independent decoder, alpha included, at sizes down to 1×1 and up to the format's 16384-pixel
  ceiling (`webp.MaxDimension`, VP8L's 14-bit dimension field) — one pixel over is a clean error,
  not a truncated file. The showcase pairs `img/quad.webp` with `img/quad.png`, and the two decode
  to 0 differing pixels across all 4096.
- **WebP output is a PNG-class target, not a JPEG-class one.** There is no pure-Go lossy VP8
  encoder and the toolkit takes no CGo, so a photographic page encoded to WebP comes out much
  larger than a lossy encoder would produce, possibly larger than the equivalent JPEG — ask for
  JPEG when the output needs to be small. `ImageOptions.Quality` is a **no-op** for WebP, pinned by
  a test: two quality values produce byte-identical output.
- `webp.Encode` buffers and writes once, checking the error. `nativewebp.Encode` ignores the error
  from every write it makes to the destination, so passing it the caller's `io.Writer` would report
  a failed write — full disk, closed pipe — as a successful encode. A test pins that a failing
  writer surfaces the error.
- No animated output: the toolkit rasterizes pages to still images, so a multi-page document
  becomes one file per page through the `%d` fan-out, never one animation.
- Bug fixed on the way in: `rasterize` took its encoding from `--format` alone, whose `png` default
  always won, so `--out page.jpg` wrote a **PNG under a .jpg name** with no diagnostic, and `.webp`
  would have done the same. The output extension now picks the format when `--format` is absent; an
  explicit `--format` still wins. Regression test in `cmd/omnidoc/rasterize_test.go`.

**XLSX conditional formats + cell notes — calc-adoption PR 4/5** (`pkg/xlsx`):

- Conditional formats get one raw-fidelity read path serving the Workbook AND the editor:
  `CFRule` typed fields, a resolved dxf, and a VERBATIM `Raw`, so data bars and color scales pass
  through byte-faithfully. `SetConditionalFormats` replaces wholesale — raw rules re-emit verbatim
  with renumbered priorities, while typed rules mint deduped dxfs. For notes, `Comment` (1-based
  A1-space) reads on both views, and `SetComment`/`RemoveComment` regenerate the comments part and
  the legacy VML wholesale, wiring rels, content-types, and legacyDrawing on first use. Two
  editor-core fixes came along: `sheetRelTarget` reads current bytes, and `File.setPart` lets an
  original part be regenerated through the dirty machinery.

**XLSX pivots + defined-names write — calc-adoption PR 5/5** (`pkg/xlsx`):

- `PivotTables()` reads each definition joined with its cache for source and field names.
  `RemovePivotTables()` wipes to a clean slate: parts, rels, workbook caches, and CTs.
  `AddPivotTable` writes a cache with `refreshOnLoad` and empty records, so definitions round-trip
  and values recompute; it does the full wiring in one call and takes axis and value fields by
  source header name, hard-erroring on unknowns. `SetDefinedNames`/`DefinedNames()` replace and
  read the workbook names, including sheet-local and hidden ones. Editor-core fix: `setPart`
  resurrects a deleted part, so remove-then-add works in one session — calc's save shape.

**External office corpus — real-world DOCX/XLSX preservation fixtures**
(`testdata/external/{docx,xlsx}`):

- 24 committed files authored by Word, Mac Office, Excel, and LibreOffice: Apache-2.0 from POI,
  MPL-2.0 from LibreOffice core, and MIT from Open-XML-SDK including an ISO-strict workbook.
  Per-file provenance and license texts live in each dir's README, isolated like the CC-BY-SA PDF
  corpus. Three sweeps cover them: the xlsx preservation contract (pinned feature counts, a no-op
  Edit+Save that is BYTE-IDENTICAL, and edit+reopen), the docx save-cycle contract (the Parse∘Write
  fixed point — the external half this contract always promised), and a full-pipeline
  open/raster/convert smoke. Landing them caught two real fidelity bugs, both fixed at the source.
  `Run.CommentRef`'s zero sentinel dropped id-0 comment references, since both Word and LibreOffice
  number comments from 0 — hence the new `HasCommentRef`. And a bare `<w:ilvl>` without a `numId`,
  which Word's own Subtitle style emits, was dropped on write.

**DOCX model — content controls, drawing title, note separators** (`pkg/docx`, additive
read+write vocabulary for the tinycld text adoption path):

- **`w:sdt` content controls unwrapped** — block, inline, and nested structured-document-tag
  wrappers now have their `w:sdtContent` parsed transparently, as if the wrapper were absent, so
  the inner text, tables, and lists survive parsing instead of being silently dropped. Empty and
  unbound controls drain cleanly and contribute nothing.
- **`Drawing.Title`** — `wp:docPr@title` is parsed and written, kept distinct from
  `@descr`/`Description` because Word's alt-text dialog exposes both.
- **`RunProps.HighlightName`** — the raw `w:highlight` name, such as `darkGreen`, is preserved so a
  consumer can apply its own palette; the writer prefers it over remapping the resolved RGBA.
- **`VerbatimChar`** character style in `DefaultStyles` — the pandoc/HTML-export inline-code
  synonym of `CodeChar` that other tools recognize on read.
- **`Run.NoteSep`** — the reserved footnote/endnote separator notes (ids -1/0) round-trip.
  `<w:separator/>` and `<w:continuationSeparator/>` both write AND parse back, so a content-less
  separator run is a Parse∘Write fixed point rather than being culled.
- **val-less `<w:u>` fix** — a bare `<w:u w:color=../>`, Word's shorthand for single underline, now
  reads as underline-ON; it was previously read as underline-off.

**Page geometry + fit-within raster sizing** (`pkg/omnidoc`, CLI `--max-width/--max-height`):

- `Document.PageSize(i)` reports points, post-/Rotate for PDF, so it is always the rendered aspect.
  `RasterOptions.MaxWidthPx/MaxHeightPx` give fit-within-box sizing, resolved per page to a
  concrete DPI above the backends. Painting is untouched: fit ≡ explicit-DPI, pixel-identical and
  test-pinned, with ceil-safe exact fits. Alongside the box, DPI becomes a resolution CEILING —
  zero fills the box, upscaling vector-sharp, while a positive value gives downscale-only
  thumbnails. CLI flags land on `rasterize` and `convert` image output, where an unset `--dpi`
  means pure fit, detected via flag.Visit.

**Exact-size cropping incl. classical saliency** (`pkg/crop`, `ImageOptions.Crop`, CLI
`--crop/--crop-size`):

- Fit-within sizing preserves aspect, so it can never fill a non-matching target — a 4:3 source
  into 720×720 comes back 720×540. `crop.Rect`/`crop.Scale` fill the box instead, offering center
  and N/S/E/W gravities, an explicit caller-supplied rectangle (`StrategyRect`), and a
  content-aware `StrategySaliency`. `ImageOptions.Crop` is a nil-able pointer applied inside
  `EncodeImage`, so every existing caller stays byte-identical; `WriteImage` and `Convert` both
  honour it.
- Saliency is classical — no model, no training data, pure Go. Sobel edge energy, HSV-style
  saturation, a deliberately wide luma-independent YCbCr skin box, and a radial centre prior sum
  into a per-pixel score map. Candidate windows are then evaluated through a summed-area table at
  O(1) per candidate, fanned out across `GOMAXPROCS`. Ties resolve to the centred window and every
  worker seeds from one immutable candidate, so results are deterministic and race-free.
  Scoring weights are pointers (`crop.Weights`) so an explicit zero disables a term — a plain
  float64 cannot distinguish "off" from "unset". Skin weight defaults *below* edge weight
  deliberately: published YCbCr skin ranges underweight darker skin tones, so skin nudges the crop
  rather than deciding it.
- `crop.ScaleRect`, `EncodeImageRect` and `Document.WriteImageRect` report the source rectangle
  that was cropped. For saliency the window is chosen from image content, so it cannot be derived
  from the options; feeding the reported rect back as `Options.Rect` with `StrategyRect` replays a
  byte-identical crop, which is what lets a caller persist a smart crop.
- Golden crop rectangles over a committed CC0 photograph back the synthetic direction tests, which
  cannot catch a quality regression on real image statistics.

**SVG input — core scene graph** (`pkg/internal/svg` parse, `pkg/internal/svg/draw` scene → `render.Device`;
`OpenSVG*`, `convert in.svg..`):

- Standalone `.svg` and gzipped `.svgz` documents open as a single vector page. Detection also fixes
  a pre-existing bug. An XML-prologed SVG — `<?xml ..?>` before the root — used to mis-sniff as HTML.
  Content sniffing, the `.svg`/`.svgz` extensions, and `image/svg+xml` MIME now all route correctly,
  including a namespace-prefixed `<svg:svg>` root.
- The full path `d` grammar: all commands, relative and absolute, implicit repeats, with arcs and
  quadratics converted to cubics. Also the six basic shapes
  (`rect`/`circle`/`ellipse`/`line`/`polyline`/`polygon`), transform lists, and `viewBox` +
  `preserveAspectRatio`. Solid fill and stroke arrive through inherited presentation attributes with
  the full CSS color syntax (`fill-rule`, opacities, dashes, caps, joins, miter limit).
- Rasterization renders at device resolution. PDF output keeps REAL VECTORS through a new
  `layout.VectorKind` item instead of rasterizing to an embedded image, so an SVG circle reaches PDF
  with no image XObject. This landed alongside a cross-cutting fix in the shared rasterizer, where an
  unclosed subpath used to fill incorrectly — a bug that also hit the PDF content interpreter's `f`
  operator on unclosed paths.
- Group opacity composites correctly through a new `render.Device.BeginGroup`/`EndGroup`
  offscreen-compositing primitive that BOTH backends implement. This covers `<g opacity>`, opacity on
  the root `<svg>` element, and nested groups multiplying. Overlapping children inside an opacity
  group blend once, at the flattened result, rather than each child's own paint alpha double-darkening
  the overlap. The same primitive fixes the analogous double-paint case on a single shape carrying
  both a fill AND a stroke at element `opacity` < 1, where the stroke's inner edge overlaps the fill.
  A shape routes through a group only when it has both a fill and a stroke and opacity < 1, so the
  common opaque/single-paint shape stays on the cheap per-paint path and allocates nothing offscreen.
  In PDF output, `BeginGroup`/`EndGroup` emit a real
  `/Group << /S /Transparency /CS /DeviceRGB /I true >>` Form XObject: children paint into their own
  content stream and resources, then the group composites back with one `/GSn gs /Fmn Do` referencing
  an ExtGState that carries the group's `/ca` and `/BM`. A fully-opaque, ungrouped document's PDF
  output stays byte-identical to before this feature — `cmp` on a rendered fixture matches the
  pre-groups commit exactly.
- `clip-path`/`<clipPath>`: a clipPath's children form a UNION, not an intersection. A new
  `render.Device.BuildClipMask(paths []MaskPath) GroupMask` primitive flattens them, rasterizing EACH
  child under its OWN `clip-rule` and combining coverage with `max()`. That is the
  correctness-critical design point: two non-overlapping children pushed as separate `PushClip` calls
  would intersect to empty. Everything below ships. `clipPathUnits` defaults to `userSpaceOnUse`, and
  `objectBoundingBox` reuses the same pre-transform-`Path.Bounds()` mapping gradients use.
  `clip-rule` is a new inherited property, applied per-child, so nonzero and evenodd can mix within
  one clipPath. A `clip-path` on the `<clipPath>` element itself intersects the whole union; one on a
  child intersects that child before it joins the union. An explicit shape/`<text>`/`<use>` allowlist
  governs valid children, so a `<g>`/`<image>`/`<switch>` child is dropped rather than recursed into
  as a forgiving container. An empty `<clipPath>` with no valid children clips its target to NOTHING
  — distinct from `clip-path="none"` or an unresolved reference, which clip not at all. Both
  `display:none` and `visibility:hidden` remove a clip child from the union, verified against the
  resvg corpus's reference renders. fill/stroke/opacity/filter/mask on a clip child have no effect;
  only geometry, transform, clip-rule, and clip-path matter. Resolution happens during `Parse`, like
  paint servers, behind a `buildingClip`-style recursion guard so a self-referencing or
  mutually-cyclic clipPath terminates. raster implements `BuildClipMask` exactly, per-child rasterize
  plus max union. pdfwrite has no offscreen surface to rasterize a pixel-exact per-child-rule union
  into, so it returns a documented rectangular bounding-box approximation — now applied for real,
  fed through the same `/SMask` machinery a luminance mask uses (a DeviceGray coverage image behind a
  luminosity soft mask). A PDF clip-path is therefore a real restriction, if a rectangular-approximate
  one, rather than an inert no-op.
- `mask`/`<mask>`: the LUMINANCE of the mask's rendered content becomes per-pixel alpha, not its
  geometry. A new `render.Device.BuildLuminanceMask(size, alphaOnly, paint func(dev Device)) GroupMask`
  primitive carries this: the backend hands back a scratch surface, `pkg/internal/svg/draw` paints the mask's
  subtree into it through the ordinary `Device` seam without ever importing a concrete backend, and
  the backend converts the result to a mask. Luminance defaults to sRGB
  (`0.2126R+0.7152G+0.0722B`, times the pixel's own alpha) rather than SVG 1.1's linearRGB, matching
  browsers, SVG2, and resvg — following the letter of SVG 1.1 here would make every mask golden
  visibly wrong. `mask-type` (SVG2, a new non-inherited property in both `hints.go` and `style.go`)
  selects `luminance`, the default, or `alpha`, which reads the pixel's own alpha channel directly.
  `maskUnits` and `maskContentUnits` both ship and reuse the clip-path objectBoundingBox mapping:
  `maskUnits` defaults to `objectBoundingBox` with a `-10%/-10%/120%/120%` region, a real 10% bleed
  past the masked element's bbox, while `maskContentUnits` defaults to `userSpaceOnUse` — the
  OPPOSITE default. A `transform` on the `<mask>` element itself is ignored; only a transform on the
  masked element applies. An empty `<mask>` with no children makes its target FULLY TRANSPARENT,
  distinct from `mask="none"` or an unresolved reference, which mask not at all. Mask, clip-path, and
  opacity compose in that order — clip → mask → opacity — by intersecting the mask before a single
  `EndGroup` call. Nested masks, where one mask references another, terminate, as do
  self-referencing and cyclic mask chains, guarded by a `buildingMask` recursion guard mirroring
  `buildingClip`. Resolution happens during `Parse`, like clip-path. raster implements
  `BuildLuminanceMask` exactly: it renders into a scratch `*image.RGBA` and converts per pixel.
  pdfwrite renders the mask's content into a SECOND, nested Form XObject
  (`/Group << /S /Transparency /CS /DeviceGray >>`) and wires it into the group's own ExtGState as
  `/SMask << /S /Luminosity /G <form> /BC [0] >>` — a real PDF luminosity soft mask, not an
  approximation. The black backdrop `/BC [0]` is mandatory: without it, the area outside the mask
  form's own content is undefined where SVG requires fully transparent. `pkg/internal/content`, the PDF
  *reader*, was taught `/SMask` too. An ExtGState soft mask now renders through
  `Device.BuildLuminanceMask` exactly as the writer produces it, scoped per paint operator: fills,
  strokes, `sh`, image and inline-image `Do`, and a form XObject's entire nested content. Text glyph
  fill/stroke is the one documented gap, since masking each glyph individually would reintroduce the
  per-child compositing seam this whole feature exists to avoid. The reader support makes the
  SVG → PDF → reopen → raster round trip genuine end-to-end proof for masks, independently
  cross-checked against Poppler's own renderer.
- Known gaps in the group/clip/mask feature, each verified by rendering rather than merely inferred.
  **`BuildClipMask`'s pdfwrite approximation is rectangular, not exact** — a non-rectangular
  `<clipPath>` union, say two non-overlapping circles, clips to the union's BOUNDING BOX in PDF
  output rather than its true shape. Only the PDF writer degrades, because it has no offscreen
  surface to rasterize a pixel-exact union into; raster stays pixel-exact through the per-child
  rasterize + max-union primitive. **Glyph fill/stroke is not soft-mask-wrapped** —
  `pkg/internal/content`'s ExtGState soft-mask support scopes to fills, strokes, `sh`, images, and nested
  form XObjects, but not per-glyph text painting, which would reintroduce the per-child compositing
  seam this feature exists to eliminate. **`reflect`/`repeat` gradient spreads still rasterize** in
  PDF output; see the alpha-gradient shading lift above, where only `pad` gets a native
  `/Shading`/`/Extend`. **`objectBoundingBox` clip-path/mask units on a `<g>` target degrade to
  `userSpaceOnUse`** with an Identity mapping. A `<g>` has no single `Path` to measure a bounding box
  from the way a `Shape` does, so `pkg/internal/svg/draw` passes a nil `boundsFunc` for a Group target and
  `clipUnitsMatrix`/mask region resolution falls back to Identity instead of resolving the group's
  real post-layout bbox. Verified against the resvg corpus's `mask/on-group-with-transform.svg` and
  `mask/half-width-region-with-rotation.svg`: both render blank under this engine — a graceful
  degradation, not a crash — where resvg produces a correctly bbox-relative result.
- **SVG `<filter>` — the primitives real documents use.** `pkg/internal/svg/filter.go` resolves the graph,
  `pkg/internal/svg/filter` holds the pixel math, and `pkg/internal/svg/draw/filter.go` drives it. The `filter`
  property runs through the same presentation-attribute and cascade path as `clip-path`/`mask`, and
  the `<filter>` element resolves at PARSE time, since the document index is gone once `Parse`
  returns. That resolution covers `filterUnits`/`primitiveUnits` with their opposite defaults of
  `objectBoundingBox` and `userSpaceOnUse`; the `x`/`y`/`width`/`height` region, which defaults to
  **-10%,-10%,120%,120%** so a filter has room to bleed past its source; per-primitive subregions;
  and the `result`/`in`/`in2` wiring with the implicit `SourceGraphic`/`SourceAlpha` inputs. A cycle
  is impossible BY CONSTRUCTION rather than by a runtime guard, because `in` may only name an EARLIER
  `result` — the resolved graph is a strictly backward DAG that the renderer evaluates in one forward
  pass. An `in` naming an undefined result falls back to the previous primitive's output, per spec.
- **Filters run in linearRGB by default** — the one place this engine departs from sRGB, and the
  likeliest source of subtly-wrong output. `color-interpolation-filters` defaults to `linearRGB`, and
  the `sRGB` opt-out is supported. A filter therefore converts sRGB → linear, operates, and converts
  back, using the exact IEC 61966-2-1 transfer function INCLUDING its linear segment near zero. A
  `pow(2.2)` approximation is wrong across the range and worst in the near-black values a shadow's
  falloff is made of. The inverse is computed rather than table-driven: a 256-entry inverse table
  quantizes the dark end enough to band exactly those gradients.
- **Filter error handling is the OPPOSITE of clip-path/mask's**, and the corpus pins each case. An
  unresolvable `filter="url(#missing)"` means the element is **not rendered at all**, not merely
  unfiltered. An empty `<filter>` outputs transparent black, so the element disappears rather than
  passing through. A zero or negative region, or primitive subregion, likewise disables the element.
  An `objectBoundingBox` region on an element with no bounding box — an empty group — disables it,
  while a `userSpaceOnUse` region on that same group still paints.
- **`feGaussianBlur` uses the spec's own three-box approximation**, not a hand-rolled Gaussian
  convolution: `d = floor(s·3·√(2π)/4 + 0.5)`, three boxes of `d` when `d` is odd, and for an even
  `d` two boxes centred on OPPOSITE pixel boundaries so their half-pixel shifts cancel, plus one of
  `d+1`. Ignore that odd/even split and the blur comes out correct in shape but visibly TRANSLATED.
  `stdDeviation` takes one or two numbers, each axis independent. A negative value, an empty value,
  or more than two values is an error that disables the PRIMITIVE, so the element renders **unblurred
  rather than blank**. **The blur operates on PREMULTIPLIED values** — the one primitive where that
  matters most, since averaging straight colour across a transparent edge weights the transparent
  pixels' meaningless black equally and darkens every blurred edge. A blur costs O(pixels) per pass
  whatever the box width. The deviation is also clamped so the box never spans more than half the
  extent it blurs across: past that the window sits almost entirely off-buffer and the element decays
  toward NOTHING rather than becoming more blurred.
- **`feComposite`** — the five Porter-Duff operators (`over`, `in`, `out`, `atop`, `xor`) plus
  `arithmetic` with its `k1..k4`. Both run on premultiplied values and clamp the result, not the
  coefficients, to [0,1]. The corpus's `k4="100"` fixture renders opaque white, which proves the
  reference feeds the coefficients through verbatim despite its own `<desc>` claiming otherwise. An
  unrecognized `operator` falls back to `over` rather than disabling the primitive.
- **`feMerge`/`feMergeNode`** — composites its inputs in DOCUMENT order, which is painting order, so
  the first node is the bottom of the stack. It is exactly a fold of `over`, asserted against
  `Composite` directly so an `feMerge` cannot drift from the equivalent `feComposite` chain.
- **`feBlend`** — `normal` plus the fifteen CSS/PDF blend modes: `multiply`, `screen`, `overlay`,
  `darken`, `lighten`, `color-dodge`, `color-burn`, `hard-light`, `soft-light`, `difference`,
  `exclusion`, and the four non-separable `hue`/`saturation`/`color`/`luminosity`. The blend
  FUNCTIONS moved to `pkg/internal/render/blend.go` and are now **shared with the raster backend's PDF `/BM`
  compositing** rather than reimplemented — one `colorBurn`, two consumers. Compositing follows the
  full CSS formula, so a source over a TRANSPARENT backdrop comes through unblended instead of
  multiplied against the backdrop's meaningless colour.
- **`feColorMatrix`** — `matrix` (5x4), `saturate`, `hueRotate` and `luminanceToAlpha`. Each
  shorthand expands into a matrix at PARSE time, so the renderer implements one operation.
  **It operates UN-premultiplied**, the exact opposite of blur and composite. Tests mutation-prove
  both directions, since getting either backwards is the classic bug. `luminanceToAlpha` uses SVG's
  BT.709 filter weights (0.2125/0.7154/0.0721), deliberately not the 0.3/0.59/0.11 set PDF's blend
  functions use for the same-sounding quantity. An unrecognized `type` is treated as `matrix`, so
  `type="qwe"` with a full `values` list still applies it; a `values` list that is not exactly 20
  numbers falls back to the identity.
- **`feDropShadow` is EXPANDED into its five-primitive chain**, not special-cased: blur → offset →
  flood → composite(`in`) → merge. A test asserts the shorthand and the hand-written chain produce
  **byte-identical** pixels. It therefore inherits the chain's premultiplication, fractional
  resampling and colour-space handling instead of re-deriving them.
- **The CSS `filter:` shorthand** — `blur()`, `drop-shadow()`, `brightness()`, `contrast()`,
  `grayscale()`, `sepia()`, `saturate()`, `hue-rotate()`, `invert()`, `opacity()`, and `url()`, in
  any combination, composing in sequence. Each lowers to its spec-defined primitive chain rather
  than to separate pixel code. The parser lives in **`pkg/internal/filtereffects`, deliberately shared**:
  `filter` is one property with one grammar, so the HTML/CSS side consumes this parser instead of
  growing a second one. Error handling is CSS's, and the corpus tests it hard. **One invalid
  function invalidates the WHOLE declaration** — the element renders completely unfiltered, not with
  the functions that did parse — while an unresolvable `url()` inside a list is merely dropped.
  `blur(50%)` and `hue-rotate(45)` are both invalid, since a percentage is not a `<length>` and a CSS
  `<angle>` requires a unit, even though `blur(1mm)` and `hue-rotate(45deg)` are fine. Unlike the
  `<filter>` element, a filter function has **no filter region**, so a large `drop-shadow()` spreads
  across the canvas instead of being clipped to a box barely larger than its element.
- **Filters apply BEFORE clip-path, mask and opacity**, per SVG's rendering model. All three are
  stripped from the filter's source pass and applied to the filtered RESULT, so a blur spreads past
  a clip's edge and is then cut off hard by it. Clipping the filter's INPUT instead would remove the
  content the blur spreads from, which reads as a too-soft blur rather than as a mis-ordered clip.
- **The remaining nine primitives degrade to the UNFILTERED element with a warn-once log naming
  each**: `feTurbulence`, `feConvolveMatrix`, `feDiffuseLighting`/`feSpecularLighting`,
  `feMorphology`, `feImage`, `feTile`, `feComponentTransfer`, and `feDisplacementMap`. A visible
  approximation beats a blank, and an unknown primitive never silently yields an empty result.
  `enable-background` is DROPPED outright rather than deferred, since the spec removed it and no
  browser implements it, so its `BackgroundImage`/`BackgroundAlpha` inputs resolve like any other
  unknown name.
- **Filters rasterize, including in PDF output — stated plainly rather than discovered later.** This
  is the one place the series' vector-native principle does not apply. A blur has no vector
  representation and PDF has no filter operator, so **any filtered element is rasterized**, at a
  resolution taken from the filter region and the current transform. Every PDF producer does the same
  with SVG filters, but it is a real trade-off rather than a free lunch. The seam is
  `render.Device.RenderOffscreen`, a third member of the `BuildClipMask`/`BuildLuminanceMask` family.
  It hands back a group's rasterized PIXELS (`*image.RGBA`) instead of a coverage mask, which keeps
  rasterization in the backend and `pkg/internal/svg/draw` backend-agnostic. `pkg/internal/pdfwrite` returns nil
  from it — the documented "cannot rasterize offscreen" degradation — and the caller then paints the
  element unfiltered. PDF output keeps the content visible and correctly placed, minus the filter's
  visual effect.
- **A filter on `<text>` uses REAL placed-glyph bounds** — `textUserBounds`, computed from the shaped
  glyphs — never `pkg/internal/svg`'s build-time `textBBox` estimate. That estimate assumes a half em per
  character and measures 0.53x–2.25x off. An `objectBoundingBox` filter region built on it would
  visibly clip the filtered result, not merely shift a gradient.
- **Filters are bounded against a build-time DoS.** The region is intersected with the part of the
  canvas it could actually reach BEFORE any buffer is allocated, so a crafted `width="400000"` costs
  the same pixels a sane region would. On top of that, a hard per-region pixel cap, a primitive-count
  cap, and a filter-nesting-depth cap each degrade to the unfiltered element with a log rather than
  allocating unboundedly.
- **`<use>` and `<symbol>` instantiation.** A `<use>` instantiates its href target as if it were a
  deep clone spliced in at the `<use>`'s own position. The clone inherits from the USE SITE, not from
  the target's document parent, so the target's own attributes win where it sets them and the
  `<use>`'s cascade shows through everywhere else — `style-inheritance-1.svg` and
  `complex-style-resolving-order.svg` pin this down. Instantiation is therefore deliberately NEVER
  memoized by target id: two `<use>`s of one target under different inherited style must produce
  genuinely different `Shape`s, the opposite of `clipMemo`/`maskMemo`'s idempotent by-id caching. The
  `<use>`'s `x`/`y` and its own `transform` compose under the target's, and its element `opacity`
  composites on the wrapper Group exactly like a `<g opacity>`'s does. A `<symbol>` target
  additionally establishes a real second viewport, per SVG2 §5.6. That viewport is sized from the
  `<use>`'s own `width`/`height`, lacuna 100% of the current viewport, and mapped through the
  symbol's `viewBox`/`preserveAspectRatio` with the same machinery as the root `<svg>`;
  `userSpaceOnUse` percentages inside resolve against the symbol's extent. Its default
  `overflow:hidden` clips to the viewport rect through a cheap axis-aligned `Group.ViewportClip` — a
  plain `PushClip` with no offscreen compositing pass — resolved through the cascade, so
  `style="overflow:visible"` disables it. A `<symbol>`'s own `transform` is ignored per SVG 1.1.
  `<use>` also works as a `<clipPath>` child. Both cycle shapes terminate. An href chain
  (`#u1` → `#u2` → `#u1`) is one. Tree recursion is the other: a `<use>` targeting its own DOM
  ancestor, or a descendant `<use>` targeting an enclosing one. Tree recursion is unreachable by
  href-following alone, and a `buildingUse` "id currently on the call stack" guard catches it, keyed
  on BOTH the `<use>`'s own id and its target's. A long ACYCLIC chain of distinct targets is bounded
  separately by `maxUseDepth` (64).
- **`<marker>` painting** on `path`, `line`, `polyline`, and `polygon` — the SVG 1.1 markerable set.
  `marker-start`/`-mid`/`-end` and the `marker` shorthand all resolve through the cascade and
  inherit, so a marker set on a `<g>` reaches its shapes. Markers place at every vertex with correct
  per-vertex tangents, including a synthesized vertex for a closed subpath per SVG 1.1 §11.6.3. They
  honor `orient="auto"`, `orient="auto-start-reverse"`, or an explicit angle, plus `refX`/`refY`,
  `markerWidth`/`markerHeight`, `markerUnits` (`strokeWidth` default vs. `userSpaceOnUse`), and their
  own `viewBox`/`preserveAspectRatio`. A marker clips to its viewport BY DEFAULT, since
  `overflow:hidden` is a marker's initial value — the opposite of most SVG elements. Markers are
  memoized by id in `markerMemo`, like `clipMemo`/`maskMemo`, because resolution is idempotent here.
  A marker whose own content carries a `marker-*` property is guarded against self-reference by
  `buildingMarker` and against a long acyclic chain by `maxMarkerChainDepth` (64).
- Known scope limits of `<use>`/`<symbol>`/`<marker>`, each degrading rather than failing. **A nested
  `<svg>` as a `<use>` target is not supported** — it establishes its own viewport, which this slice
  does not implement, so the reference resolves to nothing SILENTLY, with no `WithLogf` line unlike
  most degradations here. That defers the corpus's seven `xlink-to-svg-element*.svg` fixtures.
  **Markers paint only on `path`/`line`/`polyline`/`polygon`.** SVG 2 extends the markerable set to
  the remaining shapes and this engine does not follow it, so a `marker-*` property reaching a
  `<circle>`/`<rect>`/`<ellipse>` by inheritance paints nothing — the corpus's `marker-on-circle.svg`,
  `marker-on-rect.svg`, and `marker-on-rounded-rect.svg` assert exactly that. **A `<use>` inside a
  `<clipPath>` may not itself target another `<use>`.** **Total `<use>`/`<symbol>` instantiations per
  document are capped at `maxUseNodes` (100,000)**, logged once via `WithLogf` when exhausted. That
  cap is a build-time DoS bound: `maxUseDepth` limits recursion depth but not breadth, and a graph
  where each level references the previous level twice expands ~4× per level entirely inside `Parse`,
  where `pkg/internal/svg/draw`'s draw-time `maxDrawCalls` can never fire. The budget is a monotonic
  whole-document total, because a per-subtree counter would reset on every sibling and let such a
  graph through. It sits about an order of magnitude above the largest realistic icon sprite sheet,
  so legitimate documents are never truncated.
- **`<text>` and `<tspan>` as vector outlines** — the core text pipeline. The SAME `inline.Shape` the
  CSS reflow engine uses shapes SVG text; there is no second shaper. `Shape` is a pure function that
  is not fused to line-breaking, so SVG calls it and walks the flat `[]Glyph` accumulating advances,
  skipping `Break`/`MakeLine`/`Place` entirely — those need a width to wrap against, and SVG text
  does not wrap. Arabic harfbuzz shaping and per-rune script fallback happen inside `Shape`, so SVG
  gets them for free, and `inline.Reorder` supplies UAX#9 bidi on the same flat slice. The supported
  set starts with the per-character `x`/`y`/`dx`/`dy`/`rotate` LISTS under SVG's full rule set: an
  absolute reset starts a new text chunk, relative offsets accumulate, and a short list stops
  applying — except `rotate`, whose last value persists. Then `text-anchor` (`start`/`middle`/`end`),
  resolved per CHUNK and DIRECTION-RELATIVE, so `start` anchors the right edge in an rtl chunk.
  Then `<tspan>` nesting with an inherited position cursor and per-tspan style. Then `font-family`,
  `font-size` (including `em`/`ex`/percentage against the parent, and the CSS size keywords),
  `font-weight` (including `bolder`/`lighter`), and `font-style`. Finally `direction`,
  `unicode-bidi: bidi-override`, both `xml:space` modes, and the SVG 2 `clip-path`/`mask`/`opacity`
  properties on a `<tspan>`. `<text>` also works as `<clipPath>` and `<mask>` geometry.
- SVG text paints through `Device.FillGlyph`/`Stroke`, **never `DrawGlyph`**. It therefore routes
  through the same `paintFill`/`paintStroke` helpers a `<path>` uses and gets gradients, patterns,
  and independent fill+stroke for free. `DrawGlyph` emits PDF text-showing operators, which cannot
  express a per-glyph transform (SVG's `rotate`), independent fill and stroke on one glyph, or a
  glyph acting as clip/mask geometry. **The cost, stated plainly: SVG text in PDF output is vector
  outlines, not selectable or searchable text.**
- Known scope limits of SVG text, each degrading with a log. **`<textPath>`** renders its text on a
  straight baseline, since arc-length parameterization of a `render.Path` is a subsystem of its own.
  **`<tref>` is dropped, not deferred** —
  SVG 2 removed it and no current browser implements it. A ligature or cursive join spanning a
  `<tspan>` boundary does not form, since the two sides reach the shaper as separate runs. An
  `objectBoundingBox` paint server on text resolves against an approximated box, because a text
  chunk's true box needs shaping, which happens a layer away from where paint servers resolve;
  `userSpaceOnUse` is exact. A `<tspan>` nesting cap and a whole-document character budget
  (`maxTextChars`, 200,000) bound text against hostile input, both logged once.
- **Fuzzed against hostile input** (`FuzzParse` in `pkg/internal/svg`, `pkg/internal/css`, seeded from the resvg
  corpus): `svg.Parse` survives arbitrary bytes through XML parsing, the cascade, and scene
  building. Two defects it found and that are now fixed: a `<text>` position list whose recorded
  character range outlived the characters after a trailing space was stripped (an index panic out of
  the public `Parse`), and a `closepath` followed by numbers, which the implicit-repetition rule
  repeated forever without consuming input (a hang). `pkg/internal/css` fuzzes `Parse` + cascade,
  `ParseDeclarations`, and `ParseColorValue`, and came back clean.
- **Bounded HTML nesting** (`pkg/internal/html`, `maxNestingDepth` 4096): `golang.org/x/net/html` resolves
  close tags with a linear scan of the open-element stack, so deep nesting is quadratic — 60,000
  nested `<div>` take 15s inside the dependency and 200,000 do not finish. Since the cost lands
  before this package gets control, `Parse` counts nesting with a linear tokenizer pre-pass (11 ms
  at 200,000 levels, early-exit) and returns `ErrTooDeeplyNested` rather than handing the document
  over. Box generation applies its own `maxBoxTreeDepth` (1024) for the same reason: `generate`
  recurses per element carrying a 2,144-byte `ComputedStyle` by value, so ~80,000 levels exhausted
  the goroutine stack — a `fatal error` no `recover` can catch.
- **`letter-spacing` and `word-spacing` on SVG text** — applied as a post-shaping advance adjustment
  on the flat glyph slice, resolved per SOURCE CHARACTER, so a `<tspan letter-spacing="10">` inside a
  `<text letter-spacing="3">` widens only its own gaps. Values may be a bare number, any absolute
  unit, `em`/`ex`/`%` against the element's own `font-size`, `normal`, or negative. `letter-spacing`
  widens the gap AFTER each character except the last in the whole `<text>`. That is CSS Text 3's
  rule rather than SVG 1.1's literal wording, and it matches resvg, whose
  `letter-spacing/filter-bbox.svg` asserts the flush trailing edge with a filter region and states
  the rule in its own `<desc>`. `word-spacing` adds at each space character. **Deliberate asymmetry:
  neither property exists anywhere else in this engine — not in `pkg/internal/css`, not in `pkg/internal/layout/css` —
  so both work in SVG and are inert in HTML/DOCX.** Wiring them into CSS reflow means threading them
  through line-breaking and justification, a materially larger job. The asymmetry is recorded on the
  `Style` fields so it does not read as a bug.
- **`textLength` and `lengthAdjust`** — force a range's advance to an exact width.
  `lengthAdjust="spacing"`, the default, distributes the difference into the interior gaps only, so
  the leading and trailing edges are untouched; `lengthAdjust="spacingAndGlyphs"` scales the glyph
  OUTLINES horizontally as well. Both work on `<text>` and on `<tspan>`, and both nest: the innermost
  wins for the characters they share, and a nested `spacingAndGlyphs` COMPOUNDS its outline scale
  onto the outer one so the outline and the advance never drift apart. `textLength` is an XML
  attribute, not a presentation attribute, so it neither cascades nor inherits. The edge cases fall
  out without a special case — a target smaller than the natural width overlaps the glyphs, exactly
  zero collapses them onto a point, and a negative value is invalid and ignored. A single-character
  range has no interior gap for `"spacing"` to use, so it stays at its natural width rather than
  dividing by zero.
- **`dominant-baseline` and `alignment-baseline`** — `auto`, `alphabetic`, `middle`, `central`,
  `hanging`, `text-before-edge`/`before-edge`, and `text-after-edge`/`after-edge`. Each resolves
  against the GLYPH's own `Face.Metrics()`, so a per-rune script fallback hangs each character from
  its own font. `alignment-baseline` wins when set and defers at `auto`, and its `baseline` keyword
  DEFERS to the parent's dominant baseline rather than resetting to alphabetic. Scoping matches
  resvg: `dominant-baseline` propagates inside a `<text>` subtree but does not arrive from a `<g>`
  above it, while `alignment-baseline` is non-inherited with an explicit `inherit` that reaches back
  for the parent's value. `ideographic`, `mathematical`, `use-script`, `no-change`, and `reset-size`
  **degrade to the alphabetic baseline with a warn-once**, since they need OS/2 and BASE table
  metrics `pkg/internal/font` does not parse. `middle` uses the conventional 0.5 em x-height substitute for
  the same reason — there is no OS/2 `sxHeight`.
- **`baseline-shift`** — `sub`, `super`, lengths, and percentages against the element's own
  `font-size`, CUMULATIVE through nested `<tspan>`s per SVG2 §11.10.2, with the `baseline` keyword
  contributing zero WITHOUT resetting the accumulation. A shift written on the `<text>` element
  itself, or inherited from above it, is inert: the accumulation starts at zero there, and only a
  `<tspan>` inward can add to it. resvg asserts this four ways, each overlaying an unshifted
  reference the shifted text must exactly cover. `sub`/`super` use the conventional ∓0.2 em / ±0.4
  em offsets, since the font's OS/2 sub/superscript offsets are not parsed.
- **`text-decoration`** — `underline`, `overline`, and `line-through`, in any combination and either
  separator, drawn as filled rectangles, and stroked as well when the declaring element has a stroke.
  **A rule takes the paint AND the font metrics of the element that DECLARED it, not of the
  descendant characters it covers**, so `<text fill="red" text-decoration="underline"><tspan
  fill="blue">x</tspan></text>` underlines in RED, and a decoration declared at `font-size:200` draws
  a thick rule over 48 px text. There is one bend: a decoration inherited from ABOVE the `<text>`
  keeps its line but adopts the `<text>`'s paint, which is what resvg's `outside-the-text-element.svg`
  and `style-resolving-2.svg` both assert. A rule spans the whole run that inherited the same
  declaration, crossing `<tspan>` boundaries, and is emitted once per baseline FRAME rather than once
  per run. It therefore staircases with a `dy`/`y` list and tilts per glyph with a `rotate` list,
  matching the references. Underline and overline paint under the glyphs, line-through over them.
  Position and thickness use conventional em fractions, because `pkg/internal/font` parses no `post` table and
  a face's own `underlinePosition`/`underlineThickness` are unavailable.
- **The `font` shorthand** — expands to `font-style`, `font-weight`, `font-size`, and `font-family`,
  discarding an optional `/line-height` since SVG text does not wrap. Per CSS Cascade §3 it **RESETS
  every longhand it covers to that longhand's initial value whether or not the value names it**, so
  `font="40px X"` inside a `<g font-weight="bold">` renders REGULAR. `font-variant` and
  `font-stretch`, tracked here only as degradation flags, clear alongside, so a shorthand cannot
  leave an ancestor's stale diagnostic attached. `bolder`/`lighter` still step from the inherited
  weight: the reset governs where a slot starts, not what an explicitly-named relative keyword
  measures against. A value that does not yield BOTH a size and a family is invalid per CSS and
  applies nothing rather than half of itself — not even the reset. The system-font keywords
  (`caption`, `icon`, …) name platform UI fonts this engine cannot resolve, so they are logged and
  ignored.
- **`font-stretch`, `font-variant`, `kerning`, and `font-kerning` ship as honest no-ops**, each
  logged once. Stated plainly rather than approximated: the bundled families in `pkg/internal/font/standard`
  have no condensed, expanded, or small-caps variant, no synthetic stretching or obliquing exists
  anywhere in the engine, OpenType feature settings are not plumbed through the shaper, and no GPOS
  kerning-pair pass runs for simple scripts. There is therefore no kerning to disable and no length
  that could replace it. A synthetic squeeze or a fabricated small-caps would change advances and
  glyph shapes in ways no font designer sanctioned and no other renderer reproduces.
- `<image>` is the one element still refused, degrading with a `WithLogf` debug line rather than
  failing (filters and inline `<svg>`/`<img src=*.svg>` in HTML, once deferred alongside it, have
  since shipped — see the SVG filters and "SVG in HTML" entries below).
- 100 curated fixtures from the resvg suite's `text/**` tranche land with SVG text, with committed
  goldens: `text/` (25), `tspan/` (25), `text-anchor/` (11), `font-size/` (16), `font-weight/` (12),
  `font-family/` (6), `font-style/` (3), `direction/` (1), `unicode-bidi/` (1). Most of that suite
  names a font this repo does not bundle, so every golden differs from resvg's reference in glyph
  SHAPE. Each file was vendored only because its claim is GEOMETRIC — anchoring, per-character
  positioning, whitespace, clipping — and survives substitution, and each was compared against the
  reference by eye first. Fixtures whose reference cannot be matched for font reasons were SKIPPED
  with the reason recorded, rather than committing a golden that locks in a substituted rendering as
  if it were correct; see `testdata/svg/resvg/README.md`. The sweep found five real bugs in SVG text:
  `clip-path`/`mask` ignored on a `<tspan>`; bidi reordered before the pen walk, so an absolute `x`
  landed on the wrong glyph; `text-anchor` not direction-relative; `unicode-bidi: bidi-override`
  doing nothing to Latin; and source indentation stealing the following `<tspan>`'s `x`. It found two
  more outside `pkg/internal/svg`: `pkg/internal/font` glyph outlines carried a leading drawing op before any move-to,
  so every glyph's `Bounds` stretched back to the origin, and `inline.Reorder` emitted a multi-rune
  cluster glyph once per RUNE, returning more glyphs than it was given.
- 98 further `text/**` fixtures land with the spacing, length, baseline, and decoration properties
  above, all with committed goldens: `baseline-shift/` (22), `text-decoration/` (20),
  `dominant-baseline/` (17), `alignment-baseline/` (11), `textLength/` (10), `letter-spacing/` (7),
  `word-spacing/` (6), `lengthAdjust/` (2), `font/` (2), `font-kerning/` (1). Every one was compared
  against resvg's reference PNG by eye before vendoring. The sweep settled four behaviours the spec
  leaves ambiguous: `letter-spacing`'s trailing gap, whose paint an inherited decoration takes,
  `baseline-shift`'s inertness on `<text>` itself, and the opposite `inherit` scoping of the two
  baseline-selection properties. It also caught two outright bugs before any golden was committed.
  `alignment-baseline="baseline"` was resetting to alphabetic instead of deferring, and a decoration
  was one rectangle across the whole run instead of one per baseline frame, which flattened the
  `dy`-list staircase and merged the `rotate`-list's four tilted segments. One more was fixed at the
  source: `applyFontSize` silently dropped every ABSOLUTE-unit `font-size`, so `font-size="40px"`
  fell back to the inherited value with an "unparseable" log, because the branch delegating
  px/pt/pc/mm/cm/in to `parseLength` was unreachable. No vendored fixture caught it, since the resvg
  text corpus writes bare numbers throughout. Fixtures whose reference cannot be matched — the `(UB)`
  cases, the ones needing `<textPath>`/`writing-mode`/filters, unbundled fonts, real GPOS kerning, or
  the OS/2 and BASE metrics the two degraded baselines want — were SKIPPED with the reason recorded
  in `testdata/svg/resvg/README.md`.
- 74 curated fixtures from the resvg test suite's `masking/**` tranche land with this feature:
  `clipPath/` (37) and `mask/` (37), all with committed goldens. (MIT, commit
  `d8e064337faf01bc5a9579187a56dbdbe3eacc72`; see `testdata/svg/resvg/README.md` for the earlier
  tranches' counts.) This tranche's goldens were additionally cross-checked pixel-for-pixel against
  resvg's own reference PNGs, not just inspected visually against fixture intent — the strongest
  verification available. The sweep found and fixed three real bugs: a `visibility:hidden` clipPath
  child wrongly kept in the union, nested and self-referencing masks composing via `min` instead of
  multiplication, and a clipPath child's own nested `clip-path` resolving in the wrong coordinate
  space when the child carried its own transform.

**SVG input — CSS styling** (`pkg/internal/svg`, `pkg/internal/css`):

- An SVG-local cascade in `pkg/internal/svg/cascade.go` mirrors `pkg/internal/css/cascade.go`'s ladder minus the UA
  origin. Its rungs are `<style>` sheets — including CDATA-wrapped rule bodies, with a non-CSS
  `type=` correctly skipped — the `style=""` inline attribute, `class`, and presentation attributes
  folded in as the lowest-priority cascade origin. The supported selectors are type, class, id,
  universal, descendant, and grouping, with full specificity comparison and `!important`. Type
  matching is case-insensitive but preserves the authored case, so `linearGradient`/`clipPath` and
  friends stay addressable. `element` gained parent/id/class tracking and a `css.Node` adapter, which
  lets the shared selector matcher run unmodified over the SVG tree. A document index pre-pass
  collects stylesheets — plus an id/defs table later slices use — once per document rather than per
  element. `Style.apply` resolves every presentation property through the cascade: fill/stroke
  variants, opacity, fill-rule, linecap/linejoin/miterlimit, dasharray/dashoffset, display,
  visibility, and color. `transform` and shape geometry stay XML-attribute-only, matching SVG 1's
  separation of presentation from geometry.
- Known gaps that fail safe rather than mismatch. Attribute selectors (`[foo]`) and the
  combinators (`>`, `+`, `~`) parse without erroring but never match, because the selector engine
  handles neither and they parse into an inert simple selector. `@import` is recognized and skipped
  with a debug log rather than fetched. The selector gaps are shared with HTML (`pkg/internal/css`) and
  tracked as planned work — see [docs/CSS-LAYOUT.md](docs/CSS-LAYOUT.md), "Selectors".
- Two shared `pkg/internal/css` fixes landed here that also apply to HTML: `!important` is now recognized with
  no preceding whitespace (`red!important`), and `/* */` comments inside a `style=""` attribute value
  are stripped before parsing, matching what a `<style>` sheet's rule body already did.
- 13 curated fixtures from the same resvg test suite covering selector kinds, specificity,
  `!important`, cascade order, and CDATA — with committed goldens.

**SVG input — paint servers** (`pkg/internal/svg` gradient/pattern resolution, `pkg/internal/svg/draw` fill dispatch,
`pkg/internal/raster` shading, `pkg/internal/pdfwrite` native `/Shading` emission):

- `<linearGradient>` and `<radialGradient>`, with both `gradientUnits` values — `objectBoundingBox`
  by default and `userSpaceOnUse`, percentages allowed in either — plus `gradientTransform` and all
  three `spreadMethod` values (`pad`/`reflect`/`repeat`). `<stop>` parsing covers `offset` as a
  number or percentage, clamped to `[0,1]` and non-decreasing across the list, so an out-of-order
  stop clamps forward rather than sorting. `stop-color` takes the full color grammar plus
  `currentColor`, resolved against the stop's own `color`. `stop-opacity` composes in as REAL alpha,
  so a fading stop shows whatever is behind the shape instead of a black composite.
- `xlink:href`/`href` reference chains. Inheritance is per-attribute, with the nearest element
  winning as the walk moves outward, while stop inheritance is all-or-nothing. A cross-type href — a
  `linearGradient` hrefing a `radialGradient` or the reverse — inherits attributes and stops but
  paints as the referencing element's own kind. Chain walking is cycle-safe: 2-cycles, 3-cycles, and
  self-referencing chains all terminate and degrade to "own stops win" or "paints nothing" rather
  than looping.
- `<pattern>`: `patternUnits`/`patternContentUnits` in both unit systems, percentages included; a
  `viewBox` on the pattern, which takes precedence over `patternContentUnits` when both are set and
  resolves through an href chain; `preserveAspectRatio`; `patternTransform`, which composes correctly
  with the referencing shape's own `transform`; the `x`/`y` cell offset; and attribute and child
  inheritance via href, including a pattern nested inside another pattern's tile. A self-referencing
  tile or a mutual two-pattern cycle terminates through a build-time guard, degrading to "unpainted
  fill, the tile's own stroke still shows" rather than recursing forever.
- An unresolved `url(#id)` reference with no fallback color paints nothing, not the inherited solid
  color. That was a real bug fixed while building this: `Style.applyPaint` now clears
  `hasFill`/`hasStroke` for a still-unresolved reference instead of leaving the previous cascade
  value in place.
- PDF output emits a native `/Shading` dictionary for an axial or radial gradient whose stops are all
  fully opaque and whose `spreadMethod` is `pad`. The seam is `render.ShadingDescriber`, a
  Shader-optional companion interface that `pkg/internal/raster`'s SVG-built shadings implement and
  `pkg/internal/svg/draw`'s `alphaShader` delegates through. The dictionary carries `/ShadingType 2`/`3`,
  `/Coords`, `/ColorSpace /DeviceRGB`, `/Extend [true true]`, and a `/Function`: a single
  `FunctionType 2` (exponential, linear) for two stops, or a `FunctionType 3` stitching function over
  one `FunctionType 2` per segment for more. It paints with `sh` under the shape's existing clip.
  Coincident stop offsets — a hard color break — are nudged apart by a sub-point epsilon so
  `/Bounds` stays strictly increasing without visibly smearing the break. The proof goes beyond a
  well-formed dictionary: an opaque multi-stop gradient renders two ways, SVG→raster directly and
  SVG→PDF→reopen→raster, and the two are asserted pixel-for-pixel equivalent.
  A gradient with `stop-opacity` < 1 ALSO emits vector output now that luminosity soft masks exist.
  The color ramp still emits as the native `/DeviceRGB` `/Shading` above, paired with a second,
  parallel `/DeviceGray` shading — one gray component per stop, equal to that stop's own alpha —
  painted into a Form XObject and wired in as the `sh` operator's ExtGState `/SMask`. It uses the
  same `/Coords`, `/Extend`, and offset segmentation as the color shading, so the two agree
  pixel-for-pixel. This lifts a real, previously-shipped fallback rather than adding scope: **only
  the alpha half lifts** — a `reflect`/`repeat` spread still has no native `/Extend` equivalent and
  still rasterizes into an image XObject, logged once with the reason, exactly as before.
- Known gaps, each verified by rendering rather than merely inferred, and excluded from the golden
  corpus rather than locked in as correct. **Gradient/pattern strokes** (`stroke="url(#g)")`) degrade
  to the paint's fallback color, or to no stroke, with a one-per-document warn-once log:
  `pkg/internal/raster/stroke.go` has no stroke-to-outline conversion to clip a shading or tile
  against. **SVG2 `fr`**, the radial focal radius, is not read at all. A radial gradient's focal
  point is not projected onto the `r` circle boundary when `fx`/`fy` lies outside it, as the spec
  requires. A radial gradient with `r="0"` does not yet paint the spec-required solid fill of the
  last stop's color. `<pattern overflow="visible">` is not honored, so every tile clips to its own
  cell. And a `<stop>`'s `currentColor`/`inherit` only ever resolves against the stop's own
  attributes, never a real ancestor's `color`/`stop-color`, because `resolveStopColor` in
  `pkg/internal/svg/stops.go` has no inherited-style walk from a stop up through its parent gradient or an
  enclosing `<g>`.
- 110 curated fixtures from the same resvg test suite covering both gradient types, patterns, stop
  parsing, and the reference-chain/cycle machinery above — with committed goldens.

**SVG in HTML** (`pkg/internal/html` foreign-content capture, `pkg/internal/layout/css` vector carrier,
`pkg/internal/svg` intrinsic sizing):

- **`<img src="*.svg">` and inline `<svg>` render as VECTORS end to end — never rasterized.** Both
  route through the pre-existing `layout.VectorItem` / `layout.VectorScene` seam
  (`pkg/internal/layout/page.go`), and `paint.paintVector` hands the scene straight to the `render.Device`.
  On `pkg/internal/pdfwrite` that emits real path operators, so a PDF built from an HTML page with an
  SVG image contains **no image XObject at all**. The test asserts that structurally on the emitted
  PDF bytes: a golden alone would still pass if the SVG round-tripped through a bitmap. The path
  deliberately avoids `imageCache`/`decodeImageBytes`/`ImageContent`, since all three carry an
  `image.Image` and would force exactly that bitmap round trip. `Fragment.Vector *VectorContent` is
  the parallel carrier that keeps the scene itself, and `svgCache` the parallel document cache.
- **Intrinsic sizing takes the SVG's UN-DEFAULTED size** via the new `svg.Document.Intrinsic()`
  (`svg.IntrinsicSize`: has-width / has-height / has-ratio). By the time a `Document` exists,
  `resolveSize` has already applied CSS's 300×150 default and the viewBox-extent fallback. That is
  correct for a standalone SVG, which is its own sizing authority. It is wrong for an embedded one,
  where the outer `<img>`'s CSS supplies an axis and the SVG must contribute only a ratio. All four
  cases ship. Explicit `width`/`height` is honored. viewBox-only plus one CSS axis derives the other
  from the ratio, so `<img src="ratio.svg" style="width:600px">` on a 2:1 viewBox is 600×300, not
  600×150. A `width` attribute alone derives the height from the ratio. Neither a size nor a ratio
  falls back to 300×150. The used size then drives the DRAWING as well as the box: a scene whose box
  differs from its own viewport is scaled through the ctm, which stays vector because a coordinate
  transform is not a resample. When the box already matches, nothing wraps the scene at all.
- **Inline `<svg>` re-serializes rather than bridging DOM to DOM.** `x/net/html` fully implements
  HTML5 foreign content: the subtree arrives with `Namespace: "svg"` and its camelCase names already
  REPAIRED by `svgTagNameAdjustments` (`clippath`→`clipPath`, `lineargradient`→`linearGradient`,
  `gradientunits`→`gradientUnits`, …). `pkg/internal/html` captures that subtree as markup
  (`Element.ForeignSource`) and `pkg/internal/svg` re-parses it, which keeps the SVG parser as the single
  source of truth. An `x/net/html.Node` → `pkg/internal/svg` AST bridge would instead duplicate that
  package's whole element/attribute construction against a second node type, and every future parser
  fix would have to land twice. The camelCase round trip is load-bearing — losing it silently kills
  every gradient and clip — so a test pins it. The serializer reinstates the `xmlns` declaration
  inline SVG is allowed to omit, plus `xmlns:xlink` when the subtree actually uses the legacy prefix.
  Without either, `pkg/internal/svg`'s XML parser rejects the markup outright.
- **`<svg>` is replaced content**, so box generation stops there. `<circle>`/`<path>` no longer
  generate meaningless HTML block boxes, and an SVG-internal `<style>` no longer leaks into the HOST
  document's cascade — its rules stay scoped to the SVG, where `pkg/internal/svg` runs its own cascade. An
  `<svg>` behaves as a normal atomic inline: it sits on a line with text, stacks in block flow, and
  carries backgrounds/borders/margins like any replaced element.
- **Untrusted-input bounds.** An SVG reached from an `<img>` is untrusted, and `pkg/internal/svg`'s own
  budgets — the `<use>` instantiation budget and the `<text>` character budget — bound EXPANSION,
  not the size of the source document. A 32 MB source cap therefore applies before parsing, and logs
  when it fires. Every degradation path reserves the box and paints nothing rather than panicking:
  unfetchable, wrong content type, unparseable, over-cap, and malformed inline markup are each
  covered by tests.
- **The vector path is chosen by CONTENT TYPE, never by sniffing bytes.** An unknown or empty
  content type falls through to the raster path, so an unrecognized binary blob is never fed to an
  XML parser. A `data:` URI SVG works too, carrying its own type and bypassing the loader entirely.
- **`background-image: url(*.svg)` is vector too.** `resolveBackgroundImage` previously called the
  same `imageCache` as `<img>`, so an SVG background dead-ended there — invisibly, since a
  background that never paints looks like a styling mistake. It now resolves through the SVG cache
  first (`backgroundSource`), and `layout.BackgroundImageItem` carries a `Scene` alongside `Img`,
  exactly one of which is set. The two share the geometry model verbatim: tile size, position,
  origin box and clip box are computed identically (`BackgroundImageItem.TileSize`), and only the
  final draw call differs — `DrawImage` for a raster source, a scaled ctm handed to the scene for a
  vector one. The assertion mirrors `<img>`: an SVG background emits path operators and **no image
  XObject**, with a PNG-background control keeping that falsifiable.
- **`background-size` uses the SVG's real intrinsic ratio.** `cover`/`contain`, and a single-axis
  explicit size, go through `svgIntrinsic` — the same un-defaulted accessor the replaced path uses.
  A viewBox-only SVG therefore contains and covers by its viewBox ratio rather than by the 300×150
  default `Document.WidthPt`/`HeightPt` already carry. All four size modes ship: `cover`, `contain`,
  explicit lengths with either axis auto, and the initial `auto`.
- **`background-repeat` TILING of an SVG is deliberately NOT implemented, and degrades visibly.**
  Repeating a vector source interacts with the SVG's own viewBox/`preserveAspectRatio` mapping in a
  corner most engines special-case, and a subtly wrong tile grid would be a silent fidelity bug. A
  tiling declaration instead paints the image ONCE — correctly sized, positioned, and clipped, never
  blank — and logs a warn-once naming the ref. The raster path is untouched and still tiles. Tests
  cover both halves, including a control proving the suppression does not leak onto raster
  backgrounds.

**EPUB cover images** (`pkg/internal/epub` manifest, `pkg/omnidoc` frontend):

- **The OPF manifest's cover image is surfaced and rendered.** The parser previously read it and
  threw it away: `parseBook` touched `Manifest.Items` only to build `hrefByID` for spine resolution.
  A cover therefore appeared only when some chapter happened to `<img>` it, which is a minority of
  real books. `Book.CoverHref`/`CoverMediaType` now report it.
- **Both real-world conventions resolve**: the EPUB 3 manifest property `properties="cover-image"`,
  and the EPUB 2 de-facto `<meta name="cover" content="itemID">`. Plenty of EPUB 3 files ship both
  for reader compatibility, so both are read, and the normative EPUB 3 property wins when they
  disagree.
- **Any image format works.** An SVG cover reaches the page through the same vector seam as any
  other `<img src="*.svg">`, so it stays vector and stays sharp at any zoom. A JPEG/PNG cover takes
  the raster path. Neither is privileged, and both are tested.
- **The cover renders alone on the first page, ahead of the spine.** That is what a cover is, and it
  is also the only position available: a cover-image manifest item is not part of the reading order,
  so it has no place within the spine to occupy. `max-width: 100%` constrains it, so a typical
  oversized cover scales down whole rather than being clipped to a corner crop. Only the width is
  bounded. The engine has no `vh` unit, and a percentage height on a replaced element has no basis
  in its single-axis model, so a height bound would be dropped — one of the two silently.
- **`Book.CoverInSpine` guards the duplicate.** Many EPUB 3 books put a cover XHTML document in the
  spine that `<img>`s the same manifest item, and prepending the image would then show it twice.
  Detection works both ways — the cover item IS a spine document, or a spine document references it
  — and the prepend is skipped. A book with **no** declared cover is byte-for-byte unchanged: no
  section, and no `break-before` on the first chapter, so it gains no leading blank page. Covered by
  a test.

**Unsupported-selector diagnostic** (`pkg/internal/css`, shared by HTML/DOCX/SVG):

- **A selector dropped for an unimplemented construct now says so, once.** `pkg/internal/css` supports type,
  class, id, universal, descendant, grouping and the structural pseudo-classes. It has no child
  (`>`), sibling (`+`, `~`), attribute, or namespace selectors. Those already failed SAFE — the rule
  goes inert and never mis-matches — but they failed SILENTLY. Design-tool SVG exports lean on
  `[class^="cls-"]` and `.icon > path`, so an inline `<svg>` carrying its own `<style>` lost those
  rules with no hint why. **The selector ENGINE is unchanged** (see docs/CSS-LAYOUT.md,
  "Selectors"). Only the diagnostic ships here — the cheap half that item already identifies as
  worth doing first.
- `Parse` cannot log. `html.UAStylesheet` is a package-level var initialized by `Parse`, so no
  caller exists at that point to hold a logger. The records instead ride on `Stylesheet.Unsupported`
  as data, deduplicated and capped, and the two places that already hold both a logger and every
  sheet drain them: `NewResolver` for HTML/DOCX, and `pkg/internal/svg`'s index for SVG-internal `<style>`.
- **Warn-once per CONSTRUCT, not per selector**, and never for a UA sheet. A framework stylesheet
  can hold hundreds of `>` rules, and blaming the author for the engine's own UA sheet would fire on
  every document ever rendered. The negative half is tested as hard as the positive: every supported
  selector form records nothing; a drop for a non-construct reason such as a pseudo-element, `:not`,
  or `:is` records nothing; and valid `An+B` syntax is never mis-reported as a sibling combinator,
  including `:nth-child(2n+1)` and the spaced `:nth-last-child(2n + 1)` the parser already could not
  handle.
- Known scope limits, each recorded rather than silently missing. `letter-spacing`/`word-spacing` do
  not inherit across the HTML→SVG boundary, because `ComputedStyle` has no such fields. SVG
  background tiling degrades to one paint, as above. And the selector engine itself is NOT fixed —
  only the diagnostic. (CSS `filter:` on HTML boxes was listed here as deferred; it has since
  shipped — see below.)

**CSS `filter:` on HTML boxes** (`pkg/internal/css` cascade, `pkg/internal/layout/css` bracket emission,
`pkg/internal/layout/paint` the pixel chain; showcase section 18, `htmldoc-p18.png`):

- **All ten shorthand functions render**: `blur()`, `drop-shadow()`, `grayscale()`, `sepia()`,
  `saturate()`, `hue-rotate()`, `invert()`, `brightness()`, `contrast()`, `opacity()`. A list
  composes **left to right**, each function consuming the previous one's output. The effect applies
  to the element's WHOLE rendering — background and border as well as contents and descendants. That
  is the one structural difference from the clip bracket, whose pair deliberately excludes the box's
  own border box.
- **No new pixel math.** Every function lowers to the Filter Effects specification's own equivalent
  primitive and runs through **`pkg/internal/svg/filter`**, the same corpus-tested code the `<filter>` element
  uses. The blur premultiplication, the colour-matrix arithmetic and the colour-space handling are
  therefore inherited rather than reimplemented, and the two spellings of `filter: invert(1)` cannot
  drift apart. The parser is the already-shared `pkg/internal/filtereffects`.
- The spec expresses four of them — `invert`/`brightness`/`contrast`/`opacity` — as an
  **`feComponentTransfer` with LINEAR transfer functions**, a primitive the SVG series deliberately
  deferred. Rather than revive it, each is written as the equivalent affine per-channel map and
  evaluated through `feColorMatrix`, which computes exactly `slope·v + intercept` per channel. A
  linear transfer function IS an affine map, so this is an exact reformulation, not an approximation.
- **The CSS functions run in sRGB**, not the linearRGB that SVG's own primitives default to. Getting
  this backwards makes every `blur()` and `drop-shadow()` visibly lighter than a browser's.
- **`drop-shadow()`'s colour is resolved at LAYOUT time.** An omitted colour, like `currentColor`,
  means the element's own `color` property, and only the cascade knows that. It rides on the item as
  `layout.FilterItem.ShadowColors`.
- **CSS does not clip a filter to a region**, unlike SVG's `filterUnits`/`x`/`y`/`width`/`height`.
  The offscreen surface therefore covers the border box UNIONED with what the bracketed items
  actually paint, grown by three standard deviations for a blur plus a drop-shadow's own offset. A
  border-box-sized surface would crop a blur dead at the box edge and drop overflowing content
  outright.
- **A filtered box establishes a BFC and a stacking context.** Both are spec-required and both are
  load-bearing. The BFC makes the box flatten through ONE `AppendItems` call, so a single balanced
  bracket wraps decorations and content together. The stacking context stops a positioned descendant
  bubbling past the bracket and rendering unfiltered.
- **`rem` in a filter length resolves against the ROOT font size**, per CSS Values, using a root size
  recorded once per layout at `layoutTree`. The cascade's own `parseLength` still folds `rem` into
  `em` for every other property — a separate, pre-existing approximation this does not change.
- Error handling matches CSS's. `filter: none` and **any** invalid value leave rendering
  **byte-identical** to a document with no declaration, because an invalid declaration is ignored
  ENTIRELY rather than applying the entries that did parse.

Honest degradations. Every one of them logs on the raster path, and the one place a
cap stays silent is named explicitly below rather than glossed. `pkg/internal/layout/paint`
carries the same optional `Logf` the rest of the engine uses — see the painter's
own entry further down:

- **PDF output paints filtered content UNFILTERED.** `pkg/internal/pdfwrite`'s `RenderOffscreen`
  declines by design, since PDF has no filter operator and a blur has no vector representation. The
  bracket degrades to a plain transparency group: the content is present, correctly placed, and
  still **vector**, with no image XObject emitted — asserted directly on the operators. Rasterizing
  a page region to fake the effect is deliberately refused. Logged once per document.
- **The page-break approximation.** Brackets are page-local, because pagination splits the fragment
  tree rather than the item list, so a box straddling a break emits its own balanced pair on each
  page. That is **exact** for the eight per-pixel colour adjustments and an **approximation** for
  the two spatial ones: a blur cannot sample content that fell on the other page, so the seam
  differs from an unbroken render. Logged once per document for a split `blur()`/`drop-shadow()`
  only; a split `grayscale()` stays silent.
- **`filter: url(#id)` is dropped** with a warn-once. It references an SVG `<filter>` element, which
  an HTML box tree cannot resolve. The surrounding shorthand functions still apply.
- A degenerate, off-device, or over-cap region degrades to painting the content unfiltered, **logged
  once per page** with the specific cause named. Over-cap, off-device, and degenerate-box are three
  different problems, so they read as three different lines. The cap is `maxCSSFilterPixels`, 4M
  pixels — the same bound the SVG side uses, and meaningful for the same reason, since the surface
  is clipped to the device and its origin shifted to (0,0) before allocating. Note 4M is NOT above
  every legitimate page: a 300 DPI A4 page is ~8.7M pixels, so a full-page filter renders filtered
  at 72 and 150 DPI and unfiltered at 300. The over-cap line names the cap and points at the DPI,
  since that outcome is otherwise unexplainable from the output. The surface also covers the border
  box UNIONED with the bracketed content's extents, because CSS does not clip a filter's input, so
  one far-flung positioned descendant inflates the hull and can reach the cap on an otherwise modest
  box.
- Filters nested more than 4 deep degrade to unfiltered, **logged once per page**, matching the SVG
  side's nesting bound. Each live level holds its own offscreen surface, so depth bounds concurrent
  memory rather than just CPU.
- **Still silent, deliberately:** the two caps above do NOT log on the **PDF** path. `pkg/internal/pdfwrite`
  calls plain `PaintPage`, because its `RenderOffscreen` always declines and it already
  reports once per document that every filter in the file paints unfiltered. A second, narrower
  reason for a subset of brackets would annotate an outcome already stated for all of them, and it
  would have to fire from the concurrent per-band render phase, where the once-per-page flags are
  per-band and so could repeat. A PDF caller therefore learns THAT its filters were not applied, but
  not that a particular one would also have exceeded a cap.
- Not implemented: `backdrop-filter`, which needs the backdrop rather than the element's own pixels
  and is a different mechanism entirely, and native PDF filter emulation via soft masks.

**`paint.PaintPageWithOptions` — an optional diagnostics logger on the painter.** `PaintPage` gained
a sibling entry point taking `paint.Options{Logf: ...}` instead of a widened signature, so all
existing callers stay source-compatible and byte-identical: the zero `Options` is exactly the old
behavior. The `Logf func(string, ...any)` signature matches every other degradation logger in the
engine — `svg/draw.Renderer.Logf`, `raster.Options.Logf`, `pdfwrite.Options.Logf` — so one func
threads through the whole pipeline. With no logger the warn-once state is a **nil pointer**, so the
per-page hot path allocates nothing and captures nothing; a test pins that logger-less path. Notices
are warn-once **per cause, per page**, allocated per call and never stored, so the concurrent page
fan-out cannot race on them or suppress each other's first line. The raster/reflow backend passes
the caller's `Logf` through automatically. One test asserts the *captured output* of each
degradation rather than merely that the branch runs, and another pins that attaching a logger moves
no pixels.
