# Backlog — outstanding fidelity work

The single list of what is NOT done. Shipped work lives in [../FEATURES.md](../FEATURES.md);
deliberate exclusions live in [SCOPE.md](SCOPE.md). Prose on subsystems in progress is in
[CSS-LAYOUT.md](CSS-LAYOUT.md), [PDF.md](PDF.md), [DOCX.md](DOCX.md), and [SVG.md](SVG.md).

**Goal:** fix every fidelity issue in the engine — HTML/CSS, PDF, and DOCX-render — until the
only remaining gaps are the explicitly out-of-scope ones. Each fix lands with a fixture/test in
the same PR, mutation-verified, corpus byte-identical except intended golden changes (eyeballed).

Status legend: ☐ open · ◐ in progress.

## Where it stands

**40 open · 2 in progress**, audited against the code on 2026-09-01 (see below). The engine is
feature-complete across its stated scope; what remains is a long tail of approximations, each
degrading gracefully — and, where a degradation changes rendering, saying so in the log.

This file replaced three overlapping documents (`FIDELITY-BACKLOG.md`, `GAPS-REMEDIATION.md`,
`GAPS-REMEDIATION-2.md`) that had accumulated ~1,100 lines of prose about finished work, against
CLAUDE.md's rule that these files hold only what is outstanding. The write-ups for completed work
are in git history — `git log --diff-filter=D --name-only` finds the removing commit, and
`git show <rev>^:docs/GAPS-REMEDIATION.md` reads any of them at full length. The design rationale
for each shipped item is also in its own PR and commit messages.

**Every entry below was checked against the code when this file was written**, because the
document it replaced warned that its own checkboxes were untrustworthy and named five stale
entries. That warning was justified and was still under-counting: this audit found three more.
Two were entries claiming work was outstanding when it had shipped (`transform`, `writing-mode`);
one, E1, ran the other way and claimed more had shipped than actually had. **Re-verify before
trusting any entry that has aged** — grep for the claimed symptom first.

---

## HTML/CSS — positioning

- ☐ **C1. Precise static-position solve** for an all-`auto`-offset abs box (today approximates to the
  containing block's top-left). *Deferred (Medium).* Needs each abs box's hypothetical in-flow position
  captured at collection time (`layoutBlockChildren`) and threaded onto `deferredAbs` — it touches the
  core positioning path. The approximation is documented and logged (`block.go`, "static-position
  approximation"). Low frequency: all-auto abs is uncommon.
- ☐ **C4. Percentage `top`/`bottom` against an auto-height containing block.** *Deferred (Medium).*
  Vertical percentages resolve against the CB's computed height; an auto-height CB's height does not
  account for the out-of-flow box. Documented edge case, low value.
- ☐ **C5. `bottom`-only auto-height abs box.** *Deferred (Medium).* Needs a shrink-to-fit HEIGHT (the C2
  machinery, but vertical); the engine measures content WIDTH only — the same single-axis limitation D3,
  flex, grid, and table vertical-content sizing all share. Documented and logged (`block.go`,
  "positioned against a provisional height").
- ☐ **C6. `position: relative` on a text-only inline box.** *Deferred (Medium–large).* Structural: a
  `BoxInline` generates no fragment (its glyphs flatten into the parent's lines), so there is nothing to
  carry a `RelOffset`. Needs inline-box fragments or a per-run offset on `LineFragment`. Block-level
  relative is exact; atomic inline-block/replaced relative is a separate known no-op. **Not silent:** a
  non-zero offset warns once per layout (`warnRelativeInline`, `positioning.go`). A zero/absent offset
  stays quiet — that is the establish-a-containing-block idiom, and it works.

## HTML/CSS — replaced content

- ☐ **D3. Percentage `height` basis on replaced elements.** *Deferred (structural).* Needs a definite
  containing-block HEIGHT threaded through the layout chain; the engine is fundamentally single-axis, so
  a percentage height resolves against a 0 basis and is treated as auto. Logged (`replaced.go`, "no
  basis; treating as auto"). Broad plumbing change, low frequency.

## HTML/CSS — inline and flow

- ☐ **E1. Full `vertical-align` keyword set.** *Medium–large.* **Only `super` and `sub` are implemented**
  (`baselineShiftPt`, `inline.go`, as an em-scaled baseline shift); `top`/`middle`/`bottom`/`text-top`/
  `text-bottom`/percentage/length all fall through to a 0 shift. Table cells are a separate mechanism
  (`table.go` reads `VerticalAlign` for cell alignment) and are unaffected.
  *Audit note: the entry this replaced listed the whole keyword set as landed. It had not.*
- ☐ **E3. Margin-collapse edge cases** — empty-block collapse-through, clearance, `min-height`
  interaction. *Medium.* Collapse-through is **no longer silent**: an empty block's margins do not
  collapse through it (CSS 2.1 §8.3.1), so an empty `<div style="margin:40px 0">` between two paragraphs
  opens an 80pt gap where a browser gives 40pt — measured, warned once per layout
  (`warnCollapseThrough`), and pinned by a test that fails if the gap is ever fixed. Clearance and the
  `min-height` interaction are still silent; both need collapse state carried across the split point,
  which is the same plumbing this entry is about.

## HTML/CSS — tables

- ☐ **F8. A rowspan cell whose *spanned-into* row grows from baseline does not re-grow.** *Deferred
  (localized).* Needs the cross-row baseline re-solve the table design deliberately avoids. Documented in
  `table.go`. Low value, high complexity. (Same issue as I6.)
- ☐ **F9. Repeat `<tfoot>` on every page a table spans.** *Medium.* Only `<thead>` repeats: a
  `table-footer-group` is laid out once, at the end of the table. Needs a `FooterTop` on the table
  fragment like `HeaderBottom` (`fragment.go`) and a reservation for the footer rows in
  `splitTableForPage` (`tablepage.go`).
- ☐ **F10. Collapsed borders are dropped from a split table.** *Medium.* `splitTableForPage` and
  `splitTableRowThroughCells` set `Collapsed` to nil on both the head and the tail (`tablepage.go`),
  so a `border-collapse: collapse` table that spans pages loses every cell border on every page it
  spans. Needs the collapsed strips clipped per fragment (and shifted with the repeated header).
- ☐ **F11. Content after a split table overlaps the table's last rows.** *Medium.* Each repeated
  `<thead>` (or a caption repeated by the wrapper split) grows the tail by its height
  (`repeatHeaderOnTail`, `tablepage.go`), but the blocks after the table keep their Y, so on the
  table's last page the next block is drawn over the last rows — one header height per repeat (a
  2026-11-06 probe: 48 rows over three pages, the following paragraph covered the last two rows).
  A table in a `<caption>` wrapper also repeats the caption, not the `<thead>`, on continuation
  pages. Needs the growth carried to the following siblings and the enclosing ancestors.

## HTML/CSS — web fonts

- ☐ **G1. Synthetic bold/oblique** for an `@font-face` family supplying one variant. *Medium.* This is
  about `@font-face` families only: the bundled substitutes ship real bold/italic/bold-italic faces.
- ☐ **G2. `unicode-range` subsetting.** *Medium.* The whole face is used for every rune. The descriptor
  is **not** parsed into `FontFace` — it is recognized and reported, not honoured. It reaches
  `Stylesheet.Unsupported` and is logged once by a caller holding a logger, so the wrong-face case is at
  least explicable. *Audit note: an earlier entry called this "captured-but-ignored", which overstated
  it — nothing captured it.*
- ☐ **G3. `font-display`.** *Small.* No async loading exists in synchronous layout, so this is likely a
  documented no-op kept permanently. Reported alongside G2 so an author can tell which of the two
  descriptors was honoured — neither is, and the log now says so.
- ☐ **G4. Variable-font axes** (`font-variation-settings` → default instance). *Large.* No `fvar`
  handling exists.
- ☐ **G5. `local()` beyond `DiskFontProvider`** — no OS font-store enumeration. *Medium*
  (platform-specific).
- ☐ **G6. Content-addressed fetch cache.** *Small (perf).* `FaceCache` is keyed `(family, style)`
  (`faceKey`, `cache.go`), so one file is fetched per style.
- ☐ **G7. Font collections in `WithFallbackFontDir`.** *Medium.* The fallback directory skips `.ttc`/`.otc`
  files with a log (`dirFallbackFaces`, `cache.go`). Serving each face of a collection needs a
  face-index API in the parser (`parseTrueTypeOrCollection` and `firstFaceOf` read only the first face)
  and collection-face embedding in the PDF writer (`LoadSFNT` keeps the raw collection bytes, which are
  not a standalone embeddable sfnt).
- ☐ **G8. Fallback directory loads under the cache lock.** *Small (perf).* On the first glyph miss,
  `dirFallbackFaces` reads and parses every file in the directory while holding `FaceCache.mu`, so other
  `Resolve`/`ResolveFallback` calls on that cache block while large CJK files load. Candidates: parse
  each file lazily, only when the earlier files lack the glyph, or load outside the lock.
- ☐ **G9. Fallback directory is parsed once per document.** *Small (perf).* Each document's
  `FaceCache` parses the `WithFallbackFontDir` fonts again on its first glyph miss
  (`dirFallbackFaces`, `cache.go`). A server that renders many documents with CJK text would gain
  from a process-wide cache of parsed faces, keyed by path and modification time.
- ☐ **G10. Bundled Greek/Cyrillic covers WGL4 only.** *Small.* `standard.Fallback` sends all of
  U+0370-03FF and U+0400-04FF to the bundled Go fonts, but those fonts cover the WGL4 repertoire,
  not the full blocks: a 2026-11-06 probe found 158 of the 256 Cyrillic code points missing (from
  U+0460 on, the historic letters and the letters of non-Russian languages). A missing letter goes
  on to the fallback directory and the installed fonts; in bundled mode with no directory it is
  drawn as `.notdef` with a log. Needs a wider OFL face.

## HTML/CSS — grid

- ☐ **I1. Named-LINE placement** (`grid-column: start/end` referencing `[name]`s). *Deferred (Medium).*
  Needs new machinery: `TrackList` has no named-line storage and placement never resolves a line name.
  *Audit note: a `LineName` placement kind DOES exist in `grid_value.go` — it carries grid-AREA names,
  which resolve at placement. The gap is the track-list side, not the parser as the old entry implied.*
- ☐ **I2. Flow-axis-locked auto-placement** (definite flow-axis line + auto cross axis honours the span,
  ignores the start line). *Deferred (Small)* — a documented, non-overlapping simplification:
  `grid_place.go` scans the locked line from 0 rather than continuing the sparse cursor.
  **Deliberately NOT logged.** Unlike the other degradations here, this always produces a valid,
  non-overlapping placement and differs from a browser only in WHICH free slot a sparse locked item
  lands in. There is no runtime test for "this diverged" short of running the browser algorithm — which
  is the fix — so a log would fire on every sparse locked item and tell the author nothing actionable.
  `dense` resets to the origin anyway, which is spec-correct, so only sparse diverges at all.
- ☐ **I6. Rowspan cell whose spanned-into row grows from baseline** — same as F8.
- ☐ **I7. `subgrid`** (degrades to `none`; the caller logs). *Large.*
- ☐ **I8. `repeat(auto-fill/auto-fit)` empty-track collapse is approximate.** *Medium.*

## PDF — feature gaps

- ☐ **K1. Scan filters** — ◐ in progress. JBIG2 is **done** (vendored pure-Go Apache-2.0 decoder).
  **JPX/JPEG2000 remains blocked:** no viable pure-Go decoder exists, and writing one is out of
  proportion to the demand. See [DEPENDENCIES.md](DEPENDENCIES.md) for why a cgo decoder is not an option.
- ☐ **K2. Tiling patterns (PatternType 1)** — skipped and logged today. *Medium.* The log is real and was
  re-verified at `pkg/internal/raster/page.go` ("unsupported /PatternType %d (only shading patterns)")
  plus `pkg/internal/content/shading.go`. Cited because an audit once doubted this entry after reading the
  interface doc comment in `interp.go`, which is not the implementation.
- ☐ **K3. Higher-fidelity Coons/tensor patches (Types 6/7)** — bicubic boundary vs. the current
  bilinear-corner approximation. *Medium.*
- ☐ **K4. Luminosity soft masks (`/SMask` in ExtGState) + transparency groups.** *Large.* The interpreter
  tracks the `/SMask` state (including explicit `/None`) and reports what it cannot resolve; what is
  missing is the rendering.
- ☐ **K5. Encryption: non-empty user/owner passwords, per-stream `/Crypt` overrides, `/Perms`
  validation.** *Medium.* Only empty-password documents authenticate today (`pkg/pdf/crypt.go`).
- ☐ **K6. Base-14 residuals** — weighted/slanted substitutes ship (bold/italic/bold-italic Heros,
  Termes, and a bold Inconsolata), and a caller-supplied `FontProvider` resolves Symbol/ZapfDingbats.
  Remaining: a bundled OFL Symbol look-alike for the no-provider case (those runs are skipped today),
  AFM tables for exact base-14 advances when a PDF omits `/Widths`, and synthetic emboldening for a
  family missing a real variant. *Medium.* See `docs/PDF.md`.

## PDF — writer

- ☐ **W1. Arabic dot marks are missing from PDF output.** *Medium.* A 2026-11-06 probe rendered
  "مرحبا" and "السلام عليكم" (bundled Noto Naskh Arabic). The letters join in both outputs, and the
  direct raster draws the dots. In the PDF (viewed with poppler and with omnidoc's own PDF raster)
  every dot is missing, although the content stream shows the mark glyphs (GIDs `0x0143`, `0x0145`)
  at the same origin as their base glyphs. Not yet diagnosed: check the subset (`subset.go`) for
  those GIDs, then mark placement. Related: `complex.go` reads only `XAdvance` from harfbuzz, so
  GPOS `XOffset`/`YOffset` (mark attachment) are discarded on every output path.

## PDF — performance

- ☐ **L1. Per-glyph paint allocations** (`paint.go transformPath` + `device.go rasterizeMask`, ~3 per
  glyph). Only worth a transformed-glyph cache if profiling shows paint dominating. *Small–medium.*
- ☐ **L2. `over()` straight-vs-premultiplied alpha** (`device.go`) — latent fragility if a transparent
  page background is ever introduced. Not a live bug. *Small (document or harden).*

## DOCX

- ☐ **M5. Embedded fonts** — de-obfuscate `word/fonts/*`, which also fixes bold/italic. *Medium.* DOCX
  additionally resolves bundled-only today: the `OSFontProvider` seam exists and the HTML and PDF
  backends install it, but `docxDocument` (`reflow_backend.go`) does not.

## XLSX editor

- ☐ **X1. Sheet rename does not rewrite most references.** *Medium.* `SheetEdit.SetName` rewrites
  only the renamed sheet's own built-in local names (`_xlnm._FilterDatabase`, `Print_Area`,
  `Print_Titles`). Excel also rewrites the old name in every cell formula, other defined names,
  pivot cache sources, and conditional-format or validation formulas. Here they keep the old name
  and point at a sheet that no longer exists. `renameSheetRefs` (boundary-aware, skips string
  literals) does the rewrite and can be applied to those.
- ☐ **X2. Rows hidden by default are not read.** *Low.* `sheetFormatPr zeroHeight="1"` hides
  every row by default, so a row with no `<row>` element is hidden. `Sheet.HiddenRows` and
  `SheetEdit.RowHidden` report only rows with `hidden="1"`, so such a sheet reads as showing
  its unlisted rows. The flag is kept on save. Excel writes it when most rows of a sheet are
  hidden.

## Pagination — structural

- ☐ **N1. Mid-box splitting** — ◐ in progress. Most of it landed: pagination splits a nested spine, a
  table row through its cells, and a mid-block forced break, and `<thead>` repeats on continuation pages.
  What remains:
  - ☐ **N1d. Mid-flex-item / mid-grid-item splitting.** *Very large; recommend permanent deferral.*
    Requires the fragmentainer to reach into flex line sizing and grid track sizing, since
    `flex-grow`/`stretch`/`fr` size items collectively and would have to be re-solved per fragmentainer.
    Already an owner-signed deferral, and the case with the least real-world demand.
- ☐ **N4. Per-page float distribution.** *Medium.*
- ☐ **N7. `@page`-level declarations on margin boxes.** *Small.* Margin boxes inherit the root
  element's font and color (`marginBaseFrom`, `marginbox.go`), but a font or color declared on
  `@page` itself is not applied: `UsedPage` does not carry those declarations.
