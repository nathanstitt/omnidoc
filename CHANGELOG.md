# Changelog

Notable changes per release. From v1.0.0 the exported API of the seven public
packages (`omnidoc`, `docx`, `xlsx`, `pdf`, `crop`, `heif`, `resource`) is stable under
semantic versioning: a breaking change means a new major version. Only the latest
release receives fixes.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.2] — 2026-10-29

No breaking API changes. The behaviour changes listed under "Fixed" fix bugs.

### Added

- **`pkg/xlsx` hidden rows and columns**, read on both the `Workbook` and the editor:
  `Sheet.HiddenRows`/`HiddenCols`, with `SheetEdit.RowHidden`/`SetRowHidden` and
  `ColHidden`/`SetColHidden`. A hidden row keeps its height, and `SetColHidden` splits a
  covering `<col>` range so the column keeps its width and style. A write that changes nothing
  leaves the part byte-identical.

### Fixed

- `SetColWidth` replaced the column's `<col>` with a new one, so the column lost its style and
  any hidden flag. It now splits the covering range and changes only the width. `ClearColWidth`
  likewise removes only the width.
- Splitting a `<col>` range put the right-hand part at the end of `<cols>`, and a new `<col>` was
  always added at the end. Both now go in min-ascending order, as Excel writes them.

### Documentation

- `docs/REQUEST-tinycld-calc-xlsx.md` marks item 10 (hidden rows and columns) as released in
  v1.1.2.

## [1.1.1] — 2026-10-29

### Documentation

- `docs/REQUEST-tinycld-calc-xlsx.md` marks items 1–9 as released in v1.1.0 and adds item 10:
  read and write the `hidden="1"` flag on rows and columns.

## [1.1.0] — 2026-10-29

No breaking API changes: `gorelease -base=v1.0.0` reports only additions. The behaviour
changes listed under "Changed" fix bugs or match what Excel does.

### Added

- **PDF images in every structure output.** PDF → HTML, Markdown, DOCX, PPTX, EPUB, and RTF
  now keep each painted image, in reading order, with its orientation (`/Rotate`, CTM) and
  clip box. Plain text and CSV/TSV/XLSX omit images by design. Images under 2 pt are skipped.
- `HTMLWriteOptions` and `MarkdownOptions` take `ImageDir`/`ImageRef`, and the CLI takes
  `--images-dir DIR` for md/html output, to save embedded images as files and link them
  instead of inlining `data:` URIs.
- **`pkg/xlsx` sheet facts**, read on both the `Workbook` and the editor:
  - `Sheet.HideGridLines`, with `SheetEdit.ShowGridLines`/`SetShowGridLines`.
  - `Sheet.AutoFilter`, with `SheetEdit.AutoFilter`/`SetAutoFilter`. Every filter kind is
    typed, with a verbatim `Raw` passthrough. The hidden `_xlnm._FilterDatabase` name follows
    the filter.
  - `Sheet.Hyperlinks`, with `SheetEdit.Hyperlinks`/`SetHyperlinks` (external URLs through
    the sheet relationships).
  - `SheetEdit.DefaultRowHeight`/`DefaultColWidth`, with `SetDefaultRowHeight` and
    `SetDefaultColWidth`.
- **`pkg/xlsx` workbook facts:** `Workbook.DefaultFont` and `File.DefaultFont()` (the Normal
  style's font). `Serial1900`/`SerialFrom1900` convert serials for a `Date1904` workbook.
- **`pkg/xlsx` pivot layout**, read and written by `AddPivotTable`, so it survives a
  remove-then-add save:
  - the `x="-2"` Values placeholder (`ValuesAxis`/`ValuesPosition`);
  - `PivotValueField.NumFmt`;
  - `AxisSort` and `LocationInfo`;
  - the data, header, grand-total, missing, and error captions;
  - `CacheFields`, which keeps the producer's cache field names.
- `testdata/xlsx/google-export.xlsx`: an anonymized Google Sheets export, with the facts
  above pinned against it.

### Changed

- `Sheet.DefaultColWidth` is never 0. A sheet that declares no `defaultColWidth` gets the
  width Excel derives from `baseColWidth` and the default font (9.140625 for Calibri 11 or
  Arial 10).
- `Style.Font` fills a missing size or name from the workbook default font.
- Grid cells with no `<c>` element carry a style (the row's, else the column's, else xf 0),
  so `Cell.Style` is never nil, as documented.
- `PivotTables()` no longer lists the Values placeholder as an empty-named axis field.
- `AddPivotTable` names cache fields by the header's display text (a numeric header was
  `FieldN`), numbers repeated headers (`" "`, `" 2"`), and writes the Values placeholder when a
  table has two or more value fields.
- A style edit, or a custom pivot number format, on a workbook with no styles part creates
  the part instead of failing. `NumFmt: "General"` maps to id 0 instead of a custom record.

### Fixed

- An embedded TrueType/OpenType font with no `cmap` table (allowed for CIDFontType2) loads.
- `pkg/xlsx` editor:
  - `r:id` attributes on a sheet the editor created had no `xmlns:r` declaration (seen with
    `SetComment`).
  - `MoveSheet`/`DeleteSheet` left `localSheetId`, `activeTab`, and `firstSheet` on the wrong
    sheet.
  - `SetName` left the sheet's own `_FilterDatabase` and print names on the old name.
  - Hyperlink targets without `TargetMode` read differently in the two views.
  - ISO-strict hyperlinks did not resolve.

### Security

- `golang.org/x/net` v0.62.0 fixes GO-2026-6866 and GO-2026-6860, which reach `html.Parse` and
  `html.Render`.
- CI and release builds use the latest Go 1.26 patch, for the standard-library advisories.

## [1.0.0] — 2026-09-01

### Changed — BREAKING

- **The module was renamed** from `github.com/nathanstitt/doctaculous` to
  `github.com/nathanstitt/omnidoc`. Existing users must update their import paths;
  there is no compatibility shim.
- **The public API shrank from 16 packages to 7.** `omnidoc`, `docx`, `xlsx`, `pdf`,
  `crop`, `heif`, and `resource` remain public; the engine internals (`css`, `html`,
  `layout`, `font`, `render`, `markdown`, `rtf`, `pptx`, `epub`, and the rest) moved
  under `pkg/internal/`. Nothing an application should have been importing was
  removed, but anything reaching into the engine directly will no longer compile.
  This is the surface a v1.0.0 tag would freeze, which is why it was narrowed first.
- `Convert`, `ConvertFile`, and the `Open*` family take a `context.Context` on the
  paths that do layout work, so a pathological document can be cancelled rather than
  wedging the calling goroutine.
- `RasterOptions.FontProvider`, `SVGOptions.FontProvider`, and `WithSystemFontProvider`
  are now typed with the exported `FontProvider` and `SystemFontProvider` interfaces.
  They previously named interfaces from an internal package, which compiled but no
  importer could refer to. The method sets are identical, so an existing
  implementation keeps working unchanged.
- `docx.ParseNumberingForTest`, a test hook that had leaked into the public surface,
  is removed.
- **`pkg/docx` and `pkg/xlsx` take a `context.Context`** on every entry point that
  parses or serializes a package: `docx.Open`/`OpenBytes`/`OpenReaderAt`/`Write`/`Bytes`
  and `xlsx.Open`/`OpenBytes`/`Edit`/`File.Save`/`File.SaveTo`. The context is checked
  per body block and per part in docx, and per row and per part in xlsx, so a hostile
  file can be abandoned mid-parse. Pass `context.Background()` to keep the old
  behaviour; a nil context is tolerated and means the same.
- **The per-format openers are now one consistent family**: every input has
  `Open<Format>File(path, opts...)` and `Open<Format>Bytes(data, opts...)`. The twelve
  option-less `Open<Format>(path)` shorthands (`OpenHTML`, `OpenDOCX`, `OpenCSV`, ...)
  are removed — each was `Open<Format>File` with no way to pass options — and DOCX
  gained `OpenDOCXFile` plus options on `OpenDOCXBytes`. The generic `Open`/`OpenAs`/
  `OpenBytes`/`OpenBytesAs`/`OpenReader`/`OpenReaderAs` family is unchanged.
- **`xlsx.Cell` and `xlsx.Sheet` drop their redundant flat fields.** `Cell.Bold`,
  `Cell.Italic`, `Cell.FillRGB`, and `Cell.Align` duplicated what `Cell.Style` already
  carries (`Style.Font.Bold`, `Style.Fill`, `Style.Alignment.Horizontal`); `Cell.Style`
  is now never nil so the replacement reads are unconditional. `Sheet.Hidden` duplicated
  `Sheet.Visibility`; compare against `SheetVisible`.
- **The CLI is `convert` and `rasterize`.** The pre-conversion-matrix subcommands
  `topdf`, `todocx`, `tomd`, and `tohtml`, and the bare `--in/--out` form that guessed a
  subcommand from extensions, are removed; every one of their flags exists on `convert`.
- `OpenHTMLBytesContext`, `OpenHTMLFileContext`, and `OpenURLContext` are removed;
  pass `WithContext(ctx)` to the plain openers instead. It bounds exactly what they
  did, the HTTP fetch in `OpenURL` included.

### Added

- **`margin: 0 auto` centres.** Horizontal `auto` margins resolve per CSS 2.1 §10.3.3
  — the most common centering idiom in CSS previously did nothing, silently.
- Vertical writing modes: `writing-mode` and `text-orientation` on the HTML and SVG
  paths, with vertical font metrics.
- SVG output, and WebP encoding.
- Error sentinels (`ErrSheetNotFound`, `ErrNoStructure`, `ErrPageOutOfRange`,
  `ErrTooDeeplyNested`, `ErrAnimatedImage`, `ErrRegionUnsupported`) so callers can
  branch on failure kinds with `errors.Is` instead of matching strings.
- `Document.RasterizePageRegion` renders a window of a page at the same pixels as the
  corresponding crop of the full render, for viewers and tiled thumbnails; every
  input except PDF supports it.
- `DirFontProvider`, a ready-made `FontProvider`/`SystemFontProvider` over a directory
  of font files — the type the file-based openers already installed for fonts beside a
  document, now constructible by callers.
- Runnable examples for `Convert`, `ConvertFile`, `Open`, and `RasterizePage`.
- A `-v` flag on the CLI, surfacing the degradation logs the library already emitted.

### Fixed — hardening

A dedicated pass fuzzed every parser and fixed each crash and hang it found. Before
it, several formats could be crashed or hung by a file of a few kilobytes.

- Panics and stack overflows on malformed PDF, XLSX, SVG, HTML, Markdown, RTF, CSS,
  and font input.
- Unbounded allocation and decompression bombs: a 512 MB cap per decoded stream, plus
  an aggregate part budget for DOCX and EPUB archives.
- Integer overflows in image dimensions, spreadsheet cell references, table spans, and
  grid track counts, now checked by division rather than multiplication.
- Cycles in the PDF page tree and object streams.

Three of the defects were in dependencies rather than in this code.

### Fixed — API

- `OpenURL` honours `WithContext` for the HTTP fetch itself, not only for the layout
  after it; previously only the removed `OpenURLContext` bounded the fetch.

### Performance

- Box-shadow blur runs through a single coverage plane instead of four RGBA channels.
- Axial and radial shading ramps are memoized per shading rather than re-evaluated
  per pixel.

### Fixed — rendering

- Silent degradations that changed output with nothing in the log now warn once:
  `@font-face` `unicode-range` and `font-display`, `position: relative` on a
  non-replaced inline box, and empty-block margin collapse-through.
- Flex content height with margins; overflow clipping with `max-height`.

### Security

- CI now runs `govulncheck` on every push and PR. It is reachability-aware, so it
  reports an advisory only when a path in this module actually calls the vulnerable
  symbol.
- Its first run found **9 reachable vulnerabilities in dependencies**, now fixed by
  upgrading `golang.org/x/net` v0.43.0 → v0.55.0 (8, all in the HTML parser this
  project's core path calls) and `golang.org/x/image` v0.43.0 → v0.45.0 (1).
- One of those, GO-2026-4440, is the quadratic HTML-nesting blowup this project had
  already found by fuzzing and worked around with its own depth limit. Upstream has now
  fixed it with a limit of the same shape, so the local guard was re-matched to
  upstream's (4096 → 510) rather than left as unreachable dead code.
- Release binaries are built on the latest 1.25 patch as well; the release workflow
  had the same exact-pin problem as CI.
- CI was building and testing on **go1.25.0** — `setup-go` treats `go.mod`'s `go`
  directive as an exact pin rather than a minimum — leaving it fourteen patch releases
  behind with 8 known standard-library vulnerabilities. CI now tracks the latest 1.25
  patch; `go.mod` still states the real minimum.

### Fixed — portability

- CI now runs the suite on Linux, macOS, and Windows; previously only Linux. The first
  Windows run found that the repo had no `.gitattributes`, so text goldens were checked
  out as CRLF while the engine emits LF, failing every byte-compared `.svg`/`.md`/
  `.txt`/`.html` golden. Line endings are now normalized, and every binary fixture
  format is marked explicitly.

### Documentation

- The v1 planning document and the per-feature design specs under `docs/` were
  removed once their contents had shipped; they remain in git history, and
  `docs/BACKLOG.md` is the only list of outstanding work.
- Three overlapping backlog documents were consolidated into `docs/BACKLOG.md`,
  holding only outstanding work. Every entry was re-verified against the code, which
  found eight stale claims.
- Added `CONTRIBUTING.md`, `SECURITY.md`, and this file.

## [0.1.1] — 2026-07-29

## [0.1.0] — 2026-07-28

## [0.0.6] — 2026-07-27

Releases up to 0.1.1 predate this changelog; see the git history and the release
notes on GitHub for what they contained.

[1.1.2]: https://github.com/nathanstitt/omnidoc/compare/v1.1.1...v1.1.2
[1.1.1]: https://github.com/nathanstitt/omnidoc/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/nathanstitt/omnidoc/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/nathanstitt/omnidoc/compare/v0.1.1...v1.0.0
[0.1.1]: https://github.com/nathanstitt/omnidoc/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/nathanstitt/omnidoc/compare/v0.0.6...v0.1.0
[0.0.6]: https://github.com/nathanstitt/omnidoc/releases/tag/v0.0.6
