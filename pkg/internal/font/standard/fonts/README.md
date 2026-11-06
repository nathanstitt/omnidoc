# Bundled standard-font substitutes

These font programs ship **inside the library** (via `//go:embed` in
`../standard.go`) and are used at runtime to render simple PDF fonts that declare
a standard-14 `/BaseFont` (or a common alias) but embed no font program.

All are permissively licensed and MIT-compatible — no GPL/AGPL.

| File | Substitutes for | Format | License |
| --- | --- | --- | --- |
| `TeXGyreHeros-Regular.pfb` | Helvetica / Arial (regular) | Classic Type 1 (PFB) | GUST Font License (LPPL-equiv.) — see `GUST-FONT-LICENSE.txt` |
| `TeXGyreHeros-Bold.pfb` | Helvetica / Arial (bold) | Classic Type 1 (PFB) | GUST Font License |
| `TeXGyreHeros-Italic.pfb` | Helvetica / Arial (italic / oblique) | Classic Type 1 (PFB) | GUST Font License |
| `TeXGyreHeros-BoldItalic.pfb` | Helvetica / Arial (bold italic / boldoblique) | Classic Type 1 (PFB) | GUST Font License |
| `TeXGyreTermes-Regular.pfb` | Times-Roman / TimesNewRoman (regular) | Classic Type 1 (PFB) | GUST Font License |
| `TeXGyreTermes-Bold.pfb` | Times (bold) | Classic Type 1 (PFB) | GUST Font License |
| `TeXGyreTermes-Italic.pfb` | Times (italic) | Classic Type 1 (PFB) | GUST Font License |
| `TeXGyreTermes-BoldItalic.pfb` | Times (bold italic) | Classic Type 1 (PFB) | GUST Font License |
| `Inconsolata-Regular.ttf` | Courier / CourierNew (regular, italic → regular) | TrueType (SFNT) | SIL Open Font License 1.1 — see `LICENSE-Inconsolata.txt` |
| `Inconsolata-Bold.ttf` | Courier / CourierNew (bold, bold italic → bold) | TrueType (SFNT) | SIL Open Font License 1.1 |

## Licensing rationale

- **GUST Font License** is an instance of the LaTeX Project Public License (LPPL):
  free use, modification, and redistribution; renaming on modification is
  *requested but not legally required*. It is genuinely permissive and
  MIT-compatible for shipping inside a library.
- **SIL OFL 1.1** permits embedding, modification, and redistribution; the only
  restriction (cannot be sold on its own) does not affect bundling inside this
  toolkit.

## Coverage / approximations

- **Regular, bold, italic, and bold-italic** are bundled for the sans (Heros) and
  serif (Termes) families, so a weighted/slanted standard-14 base font renders in
  the matching face. The monospace family (Inconsolata) ships regular + bold; its
  italic and bold-italic fall back to the upright weight (Inconsolata has no
  upright-italic in this set) — logged.
- **Symbol** and **ZapfDingbats** have no bundled look-alike; a caller-supplied font
  provider resolves them when a real face is reachable, otherwise the library
  reports them unavailable and the caller skips the text (graceful degradation).
- Widths: the PDF's own `/Widths` are preferred when present; otherwise the
  substitute face's own advances approximate them.

Sources: TeX Gyre collection (GUST e-foundry, via CTAN
`fonts/tex-gyre/type1/` — `qhv*`/`qtm*`); Inconsolata (The Inconsolata Project
Authors, via the googlefonts/Inconsolata release).

Greek and Cyrillic fall back to the Go fonts (Go-Regular, Go-Bold, Go-Italic, Go-BoldItalic) embedded from `golang.org/x/image/font/gofont`, BSD-3-Clause, © Bigelow & Holmes Inc. and The Go Authors.
