Omnidoc

Pure‑Go document toolkit HTML rendering specimen

# The Rendering Feature Tour

A single document that exercises every implemented slice of the HTML / CSS / image pipeline — typography, the box model, floats, flexbox, CSS grid, tables, positioning, overflow, stacking, and decoded images — fetched over HTTP and paginated onto Letter pages.

**01 / TYPE**

## Typography & Inline Flow

Block stacking, inline wrapping, three font families, and inline atoms sharing one baseline.

### Headings cascade from the UA sheet

Body copy is set in `TeX Gyre Termes`, a serif. Headings switch to `TeX Gyre Heros`, a grotesque. This paragraph carries enough text to wrap across several lines at the page measure, so greedy line‑breaking, leading, and left alignment are all visible. Short inline runs such as `display:inline-block` sit on the same line as the surrounding prose.

BADGE

> “Accept interfaces, return concrete types.” A pull‑quote drawn with a gold left rule and the sans family.

#### Alignment

This line is centered.

This line is right‑aligned.

**02 / BOX**

## The Box Model & Borders

Padding, margins, backgrounds, and every border style the engine renders — including the four 3D bevels.

border: outset

border: inset

border: ridge

border: groove

Four `border-style` bevels. Outset reads raised, inset sunken; ridge and groove are their two‑facet cousins.

Line styles: `solid`, `dashed`, `dotted`, `double`.

**03 / FLOAT**

## Floats & Clear

A floated figure with text wrapping beside it, then a cleared block returning to full width.

img/photo.jpg · JPEG

This paragraph wraps its lines along the right edge of the left‑floated figure. The decoded JPEG to the left is a continuous‑tone image, so it exercises the baseline DCT decode path rather than a flat fill. As the text continues past the bottom of the figure box, the lines reclaim the full measure of the column and run edge to edge again, exactly as float behaviour requires. The figure keeps its margin gap so the text never touches the frame.

A `clear:left` block drops below the float and spans the full width beneath it.

**04 / FLEX**

## Flexbox

Proportional growth, space distribution, cross‑axis centering, and multi‑line wrapping with `align-content`.

#### `flex-grow` in 1 : 2 : 3 proportion

#### `justify-content: space-between`

#### `align-items: center`

A short and a tall item, both centered in the strip.

#### `flex-wrap: wrap` with `gap`

Items too wide to share one line break onto the next; the `gap` applies both between items and between the lines.

#### `align-content: space-between` on a taller container

With more cross space than the lines need, `align-content` distributes the surplus between them — here pinning the first and last lines to the container's edges. The default is `stretch`, which instead grows each line to absorb it.

**05 / GRID**

## CSS Grid

Explicit tracks, named template areas, fractional units, spans, and auto‑placement.

#### Named areas & `fr` tracks

A header spanning both columns over a 2fr main / 1fr side row.

#### Auto‑placement with a spanning tile

The gold tile spans two columns; the rest auto‑flow into the grid.

**06 / TABLE**

## Tables

Collapsed borders, a caption, a spanning header, striped rows, and a total rule — then separate borders with column / row background layers.

****Quarterly Ledger****

| Quarter | Region | Units | Revenue |
| --- | --- | --- | --- |
| Q1 | North | 1,204 | $48,160 |
| Q2 | North | 1,560 | $62,400 |
| Q3 | South | 1,028 | $41,120 |
| Total | Total | Total | $151,680 |

| a1 | b1 | c1 | d1 |
| --- | --- | --- | --- |
| a2 | b2 | c2 | d2 |
| a3 | b3 | c3 | d3 |

Separate borders: column 2 carries a tint, alternating rows a stripe; where they cross, the row layer wins.

**07 / LAYER**

## Positioning, Overflow & Stacking

Absolute pinning, relative paint‑time offset, overflow clipping, and z‑index order.

![gold ring badge](img/icon.gif)

This stage is `position:relative`. The gold badge is `position:absolute`, pinned to its top‑right corner and painted above this text. The GIF inside it decodes through the indexed palette path.

The rust tag is `position:relative`, nudged down and right from its in‑flow slot at paint time.

#### `overflow: hidden`

A 320×320 child clipped to the padding box.

#### `z-index` order

Higher z paints on top: gold over slate over rust.

**08 / IMAGE**

## Decoded Images & `object-fit`

One source square fitted three ways into wide frames.

object-fit: contain

object-fit: cover

none + position

The four‑quadrant PNG makes orientation unambiguous: _contain_ letterboxes the whole square, _cover_ fills and crops, and _none_ pins the unscaled image to the bottom‑right.

HEIC (pure‑Go HEVC decode)

PNG reference

The same quad image as an Apple‑encoded HEIC decoded by the in‑tree HEVC decoder, beside its lossless PNG twin — identical layout, near‑identical pixels (HEIC is lossy 4:2:0).

WebP (lossless VP8L)

PNG reference

The same quad again, round‑tripped through the toolkit's own WebP encoder and back through its decoder. VP8L is lossless, so unlike the HEIC pair above these two must be pixel‑identical — any visible difference is a bug.

**09 / FORMS**

## Form Controls

Static, non‑interactive native widgets — text fields, buttons, checkboxes, radios, a textarea, and a select — sized and painted with classic chrome.

Full name

Email

Password

Notes

Plan

**Preferences**

Subscribe to updates

Remember this device

Contact by email

Contact by phone

**10 / WHITE-SPACE**

## The `white-space` Property

How runs of spaces, tabs, and newlines are collapsed or preserved, and whether lines wrap.

#### `pre` — preserve everything, no wrap (tab‑aligned)

```
name      role        team
ada       founder     core
grace     compiler    tools
margaret  navigation  apollo
```

Spaces, the literal newlines, and tab stops are preserved; long lines do not wrap (they overflow / are clipped by the box).

#### `pre-wrap` — preserve, but wrap

```
This preformatted block keeps    its    runs of spaces and its
explicit line breaks, but unlike pre it still wraps a line that is too long to fit within the width of its containing box.
```

#### `pre-line` — collapse spaces, keep newlines

Spaces collapse to one, but each newline is kept.

#### `nowrap` — collapse, never wrap

This single line never wraps no matter how narrow the box is — it just runs straight off the right edge and is clipped.

The clip box (`overflow:hidden`) cuts the overflowing line at its edge.

**11 / LISTS**

## Lists & Counters

Bullets and numbers, nested marker rotation, the numbering styles, and CSS counters.

#### Unordered (nested)

- Disc at the top level
- Another item
  - Circle one level in
    - Square two levels in
- Back to disc

#### Ordered styles

1. Decimal one
2. Decimal two

a. Lower-alpha
b. Lower-alpha

I. Roman
II. Roman
III. Roman
IV. Roman

#### Nested numbering with `counters()`

1. First section
  1. Subsection
  2. Subsection
2. Second section

The inner list's `counter-reset` opens a new scope; `counters(list-item, ".")` would join them as 1.1, 1.2 — here the default per-list numbering restarts each level.

**12 / BACKGROUNDS**

## Background Images

A tiled texture, a cover hero, and a positioned badge — `background-image` with repeat, size and position.

#### Tiled texture (`repeat`)

A small seamless tile repeats to fill the panel. The text sits on top of the background, which paints behind the content and inside the padding box.

#### `cover`, centered

The photo scales to cover the box, cropping the overflow.

#### Positioned badge

A single icon, `no-repeat`, pinned bottom-right over a tint.

**13 / LINKS**

## Links

The link pseudo-classes: a UA default, an author `a:link` color, an underline opt-out, and an inert `:visited` rule.

Body copy with a [default hyperlink](https://example.com) — blue and underlined, the user-agent style for an unvisited link.

A [call-to-action link](https://example.com) recolored with `a:link { color }`, keeping its underline.

A [borderless link](https://example.com) with `text-decoration: none` — colored but not underlined.

A `:visited` rule is parsed but never matches in a static render (no browsing history), so [this link](https://example.com) stays the unvisited style — and a bare anchor with no `href`, like this, is not a link at all.

**14 / LEGACY ATTRIBUTES**

## Presentational Attributes

Pre‑CSS styling via HTML attributes — `bgcolor`, `align`, `cellpadding`, `border`, `<font>` — mapped to CSS as below‑cascade hints (real CSS still wins).

The kind of table the early web was built from: colored rows, padded bordered cells, and aligned columns, all set with attributes rather than a stylesheet.

| Component | Language | Lines |
| --- | --- | ---: |
| Parser | Go | 4,210 |
| Layout engine | Go | 11,930 |
| Rasterizer | Go | 6,740 |

An obsolete `<font>` still works: large red text, and an `<ol type="I" start="3">` numbers with the attribute:

III. Third in Roman
IV. Fourth in Roman

**15 / DIRECTION**

## RTL & Bidirectional Layout

`direction: rtl` runs the inline axis right‑to‑left. Tables mirror their column order, flex rows reverse their main axis, grids mirror their tracks, and the direction‑relative `start`/`end` keywords resolve against it.

The table below sets `dir="rtl"`. Column one is the right‑most, and the border‑collapse grid lines mirror with it:

| First | Second | Third |
| --- | --- | --- |
| Rightmost cell | Middle cell | Leftmost cell |

A flex row under `dir="rtl"` packs from the right. The items are authored one‑two‑three in source order, so item one takes the right‑most slot:

One

Two

Three

A grid mirrors its tracks the same way — the `1fr 2fr` ratio is preserved, but the single‑width track is now on the right:

1fr — right track

2fr — left track

Finally, `text-align` takes the logical keywords. Both paragraphs below use `text-align: end`; only their `direction` differs, so _end_ resolves to opposite edges:

LTR — `end` resolves to the right edge

RTL — `end` resolves to the left edge

**15 / DIRECTION**

### Real script

Everything above mirrors _boxes_. Text inside a line is reordered separately, per UAX #9: shaping and line‑breaking run in logical order, and each line is reordered to visual order once the break is chosen. The Hebrew below is authored left‑to‑right in the source and reads right‑to‑left on the page:

שלום עולם

A right‑to‑left phrase inside an otherwise left‑to‑right paragraph reorders in place, and the Latin around it does not move — the bracket pairs mirror too (rule L4):

The greeting (שלום) sits inline.

Arabic additionally needs _contextual_ shaping: a letter takes a different form depending on whether it joins to its neighbours, and some pairs fuse into one glyph. These are shaped through the font’s OpenType tables, so the letters connect rather than standing as isolated forms:

مرحبا بالعالم

**16 / CROP**

## Exact-size Cropping

Filling an exact pixel box, rather than fitting within one.

`--max-width`/`--max-height` fit a render inside a box with aspect preserved, so a 3:2 source can never fill a square target — it comes back 300×200, not 200×200. `ImageOptions.Crop` fills the target instead, discarding what falls outside. The source below is 480×360:

img/crop-source.jpg · 480×360 source

The same square target, placed three ways. _Saliency_ scores candidate windows on edge energy, saturation, skin likelihood and a centre prior — no model, no training data — and picks the highest; the gravities place the window deterministically:

saliency

center

west

The hippo’s head — the most textured part of the frame — sits right of centre, so the saliency window shifts toward it: `(110,0)–(470,360)` against the centre crop’s `(60,0)–(420,360)`. `EncodeImageRect` returns that rectangle, so a caller can record where a smart crop landed and replay it later with `StrategyRect`.

**17 / LANDSCAPE**

## Landscape Reflow

This section selects a wider `@page landscape` via `page: landscape`; its content reflows to the wider measure of a landscape US‑Letter page (1056×816px) instead of the portrait column.

Because the named page is wider, the flex row below stretches edge to edge across the full landscape measure, and the table beneath it reflows to the same wide width — both visibly wider than any portrait page in this specimen. The running chrome (the head at top‑left, the section title at top‑right, and the page counters along the bottom) carries over onto the landscape page band unchanged.

Parser & xref

Content interpreter

Raster backend

Reflow engine

CSS cascade

| Pipeline stage | Package | Input | Output | Notes |
| --- | --- | --- | --- | --- |
| Parse | pkg/pdf | bytes | Document | xref tables & streams, object streams |
| Interpret | pkg/pdf/content | content stream | paint ops | paths, text, images, shadings |
| Reflow | pkg/layout/css | cssbox tree | fragment tree | block / inline / flex / grid / table |
| Raster | pkg/render/raster | paint ops | bitmap | pure‑Go, dependency‑light |

Omnidoc · pure‑Go, MIT‑licensed · rendered from HTTP‑fetched HTML, CSS, fonts and images, paginated onto US‑Letter pages. Set in TeX Gyre Heros & Termes with Inconsolata; wordmark in Pacifico.

**18 / FILTER**

## CSS `filter`

Every swatch below is the _same_ source rendering — a bordered tile with text and two colour bands — under a different `filter` declaration. The effect runs over the box's flattened pixels, so its border, background, text and children are all filtered together.

**none**

unfiltered

**gray**

grayscale(1)

**sepia**

sepia(1)

**sat**

saturate(2.5)

**hue**

hue-rotate(140deg)

**inv**

invert(1)

**bright**

brightness(1.6)

**contr**

contrast(2.2)

**alpha**

opacity(0.4)

**blur**

blur(2px)

**shadow**

drop-shadow(5px 5px 3px)

**chain**

grayscale(1) brightness(1.5) contrast(1.4)

Look for: the two spatial functions. `blur(2px)` and `drop-shadow()` both reach _outside_ the tile's border box — CSS, unlike SVG, does not clip a filter to a region — while the eight colour adjustments recolour each pixel in place. The last swatch chains three functions, which apply left to right: grey first, then brighten that result, then raise its contrast.

The chain runs over the box's flattened pixels in an offscreen surface, which is why a filtered box also establishes a block formatting context and a stacking context: its whole rendering has to compose as one isolated group before the effect can apply. In PDF output the same content paints _unfiltered_ — a PDF writer has no offscreen raster surface and PDF has no filter operator, so the content stays vector and correctly placed rather than being rasterised into a picture of itself.

**19 / CUSTOM PROPERTIES**

## Custom properties and `var()`

The palette below is declared _once_ on `:root` and reached by every swatch through `var()`. Custom properties inherit, so a value set at the document root is visible to any descendant without being redeclared — the pattern almost every themed stylesheet is built on.

var(--brand)

var(--accent)

var(--muted)

var(--absent, #7a5cff)

var(--indirect)

scoped override

Look for: the last two swatches. _Indirect_ resolves `--indirect: var(--brand)` — a custom property whose own value is another `var()`, substituted recursively. _Scoped override_ redeclares `--brand` on the swatch itself, so the same `var(--brand)` reference resolves differently there than in the first swatch: custom properties cascade and inherit exactly like any other property, and the nearest declaration wins.

Substitution happens between the cascade and value parsing, so `var()` works in _any_ property, shorthands included — the rule below draws its whole border from one variable. A reference that cannot be resolved is _invalid at computed‑value time_, which is not the same as a syntax error: rather than leaving an earlier declaration showing, the property falls back to its inherited or initial value, as though `unset` had been specified.

border: var(--rule)

**20 / COLOUR**

## Alpha-bearing colour values

The engine has one CSS Color 4 colour grammar, shared by the HTML cascade and the SVG parser. Every swatch below straddles a pale field and a dark block, so an alpha channel that is honoured looks _different on each side_ while one that is dropped looks the same on both.

#4f9cff _(opaque reference)_

rgba(79,156,255,0.35)

#4f9cff59

#4bf6

hsl(214,100%,65%)

hsla(214,100%,65%,0.35)

rgb(79 156 255 / 35%)

hsl(214deg 100% 65% / 35%)

rebeccapurple

border rgba(176,48,48,0.5)

**Aa**

color rgba(20,30,45,0.4)

transparent _(valid, paints nothing)_

rgb(nope,0,0) _(dropped → red stands)_

Look for: `rgba(79,156,255,0.35)`, `#4f9cff59` and `rgb(79 156 255 / 35%)` are the SAME colour at the same alpha spelled three ways — 0.35 and 35 % and the hex byte `59` all resolve to an alpha of 89 — so those three swatches must be pixel-identical. The two `hsl` swatches sit one step away at (77,154,255) rather than (79,156,255): that is the honest result of converting HSL to RGB and rounding, not a parsing error. `#4bf6` is deliberately a _different_ colour (#44bbff at alpha 102) to show the four-digit form expanding each nibble. Every alpha-bearing swatch must read pale over the light field and muted-blue over the dark block; a swatch that looks the same on both halves has lost its alpha, and a swatch that is not there at all means the value failed to parse and the declaration was dropped.

The last two swatches are the error cases, and they use the `background-color` longhand deliberately. `transparent` is a _valid_ colour and correctly paints nothing. The malformed `rgb(nope,0,0)` is dropped per CSS whole-declaration error handling, so the earlier red declaration stays in force and the swatch renders solid red — a blank swatch there would mean the engine had wrongly accepted the bad value and painted transparent black. Through the `background` _shorthand_ the same bad value behaves differently, because that expander resets each sub-property before classifying components and tolerates a component it cannot classify; that divergence lives in the shorthand, not in the colour grammar.

Alpha survives the whole pipeline because the paint stack carries a live alpha channel: parsing produces an RGBA, the painter hands it to the device unchanged, and the rasteriser composites it over whatever it lands on. In PDF output the same colours are emitted through the writer's own colour handling rather than being pre-blended, so the text above stays selectable and the fills stay vector.

**21 / MISSING GLYPHS**

## Characters No Font Can Draw

No bundled face covers emoji or Devanagari, and none ever will — the bundle is a handful of permissively‑licensed Latin, Hebrew and Arabic substitutes. A character none of them maps now draws `.notdef`, the “tofu” box, exactly as a browser does.

Nine weather emoji. Every one is unmappable, so every one is a box:

🌈🌤🌥🌦🌧🌨🌩🌪🌫

The box is not a placeholder pasted over the line — it is a glyph, and it shapes like one. Below, unmappable characters sit inline with text that resolves normally, and the correct text keeps its own metrics on either side:

Latin, then Devanagari कखग, then Latin again.

Whether the mark is drawn by the font or by us depends on the font. A face that ships its own `.notdef` gets _its_ glyph — DejaVu draws a hollow box, Noto draws a box containing the code point's hex digits. The bundled TeX Gyre substitutes ship a `.notdef` that is _blank_, so for the boxes above the geometry is synthesized. Each distinct missing character is also reported once through the layout log, which is the half that makes a font gap diagnosable rather than merely visible.

Invisible characters are deliberately excluded. A no‑break space, a zero‑width joiner or a variation selector draws no ink even in a font that maps it, so giving it a box would invent a mark the author never wrote. The sentence you are reading contains a U+202F NARROW NO‑BREAK SPACE that no bundled face maps; it renders as space, and nothing is logged, because nothing is wrong.

Look for: the boxes have a side bearing, so they read as separate marks rather than one bar, and they sit on the baseline with the text around them. This matters more than it looks. Before `.notdef`, an unmappable character rendered as _nothing_ — and because the surrounding text was untouched, a page with a font gap looked like a page with a _layout_ bug. It is worse when only some characters of a set are missing: the report behind this section was a board carrying only DejaVu and Liberation, where three of nine weather emoji drew and six vanished.

**22 / GRADIENTS**

## CSS `linear-gradient()` and `radial-gradient()`

A gradient is a generated _background image_, not a colour: it is painted by the same shading engine SVG paint servers use, evaluated per device pixel rather than baked into a bitmap. Every tile below is 116×58 — deliberately not square, because a `to <corner>` gradient's line is only 45° on a square.

### Direction

(none) → to bottom

to right

to left

to top

45deg

0.75turn

to bottom right

to top right

Look for: the two corner swatches. Their bands are _not_ at 45°. CSS angles the gradient line so the perpendicular through each end lands exactly on a corner, which on a 2:1 box makes the line noticeably shallower — and puts the two _other_ corners at precisely the midpoint colour. `0deg` points up and angles run clockwise, so `0.75turn` (270°) matches `to left`.

### Colour stops

four colours, no positions

25% … 75%

two stops at 50% (hard break)

four hard bands

→ transparent

Look for: stops with no position are spread _evenly_ between the ones that have them, so the four-colour swatch changes at 33% and 67% without being told to. Two stops sharing a position give a hard edge with no blend at all — the mechanism behind the four-band swatch. The last swatch fades to `transparent` and stays _red_ the whole way: interpolation runs in premultiplied alpha, which is what keeps a grey band from appearing through the middle of it.

### Radial

ellipse (default)

circle

closest-side

circle farthest-side

circle at 20% 30%

Look for: the default ending shape is an _ellipse_ sized to the farthest corner, so it stretches with the box; adding `circle` makes both radii equal and the rings become round. The sizing keyword picks which box feature the shape must reach — `closest-side` lands the end colour on the nearest edges, so the corners sit beyond the ramp and are solid.

**23 / GRADIENTS, CONTINUED**

## Repeating gradients, and the `background-*` properties

A gradient is a generated _image_, so it answers to the same sizing, tiling and positioning properties a bitmap does. These swatches are the proof: the same declarations that place a `url()` background place a gradient one.

repeating, 16px period

repeating at 45deg

repeating radial

background-size: 40px

60% size, centered, no-repeat

Look for: a repeating gradient takes its _stop range_ as the period, so the ramp tiles rather than stretching once. The last two swatches show a gradient really is a background image — `background-size` resizes its box, and the result tiles or is placed by `background-position` exactly as a bitmap would.

**24 / BORDER RADIUS**

## Rounded Corners

Every form of `border-radius`: circular and elliptical corners, the four per‑corner longhands, percentage radii, the overlap‑correction rule, and rounded borders with their true inner curve.

none

no radius

8px

border-radius: 8px

pill

border-radius: 999px

50%

border-radius: 50%

30/12

border-radius: 30px / 12px

4 vals

2px 10px 20px 10px

Look for: the `50%` tile is a true ellipse rather than a circle, because its box is wider than it is tall — a percentage resolves the horizontal radius against the box's _width_ and the vertical one against its _height_. The `30px / 12px` tile shows the same asymmetry written explicitly with the slash form, and the last tile gives all four corners a different radius in one declaration.

thin

2px border, 14px radius

thick

10px border, 14px radius

inner

12px border, 6px radius

over

radius 200px, corrected

Look for: the inner curve. A rounded border is not the outer path stroked — the inner edge's radius is the outer radius _minus_ the border width, so the thick tile's hole is far less round than its outside, and in the third tile the border is thicker than the radius, which squares the inner corner completely while the outside stays round. The last tile asks for a 200px radius on a box far smaller than that; the corners cannot overlap, so every radius scales down by one shared factor until they exactly meet.

The overlap correction (CSS Backgrounds 3 §5.1) is what makes an over‑large radius degrade gracefully rather than producing self‑crossing arcs: each side compares its length against the sum of the two radii meeting along it, and the _smallest_ of those ratios scales all eight radius components at once. That single shared factor is why `border-radius: 999px` on the pill above resolves to exactly half the box's height on every corner.

Backgrounds and borders both follow the rounded outline, and a background _image_ is clipped to it. In PDF output the corners stay genuinely curved: the writer emits the same cubic curve path natively, so a rounded box remains vector rather than being flattened to a picture. A rounded border is filled as one ring in a single colour, so a dashed or multi‑coloured rounded border paints solid — the engine logs that substitution rather than making it silently.

**25 / MID-WORD BREAKING**

## The `overflow-wrap` & `word-break` Properties

Where a line may break _inside_ a word. Normal wrapping only breaks at spaces, so a single long token — a URL, a hash, a compound noun — runs straight past the edge of its box. These two properties add opportunities between characters, and they differ in when they take them.

#### The problem — one unbreakable token

https://example.com/very/long/path/to/a/private/calendar/feed.ics

Default `overflow-wrap:normal`: the token has no space in it, so there is nowhere to break and it overflows the box on a single line.

#### `overflow-wrap: break-word` — break only as a last resort

https://example.com/very/long/path/to/a/private/calendar/feed.ics

The same token now breaks between characters and stays inside the box. The legacy alias `word-wrap:break-word` is accepted for the same effect.

#### Last resort vs. eager — the distinction that matters

wrap this sesquipedalian

`break-word` moves a word that _fits on a line of its own_ down whole, breaking it only when it cannot fit anywhere.

wrap this sesquipedalian

`break-all` is eager: every character boundary is an ordinary opportunity, so the word is chopped at the line edge regardless.

#### `word-break: keep-all` — suppress mid‑word breaking

https://example.com/very/long/path/to/a/private/calendar/feed.ics

`keep-all` forbids breaking the affected text, so it overrides `overflow-wrap` and the token overflows again.

Every mid‑word break lands on a grapheme‑cluster boundary (UAX #29), so a combining mark is never separated from its base letter and an emoji ZWJ sequence or a flag is never split in half. `overflow-wrap:anywhere` breaks in the same places as `break-word` but additionally shrinks a box's intrinsic _min‑content_ width, which is visible in flex and grid sizing rather than in the line breaking itself.

**26 / BOX-SHADOW**

## CSS `box-shadow`

Every tile below is identical — a flat fill inside a hairline border — under a different `box-shadow` declaration. Unlike a `filter`, a shadow is not computed from the box's pixels: it is the box's own _shape_, displaced, grown and blurred, so it costs nothing to draw and stays sharp at any resolution.

none

6px 6px

6px 6px 8px

0 0 0 5px

8px 8px 4px −4px

5px 5px 4px (currentColor)

inset 6px 6px 6px

inset 5px 0 0

inset 0 0 0 5px

three offsets, comma‑separated

outer + inset in one list

Look for: the four arguments separating cleanly. _offset_ moves the shape; _blur_ softens its edge over a distance equal to the radius, centred on the edge; _spread_ grows the shape on all four sides before that blur; and a _negative_ spread shrinks it — the −4px swatch casts a visibly narrower shadow than its 8px offset alone would. The swatch with no colour written takes the element's own `color`.

The `inset` keyword is a genuinely different rendering, not a sign flip. An outer shadow fills the region _outside_ the border box — so a tile with a transparent background shows a ring, never a filled blob — while an inset shadow fills the part of the _padding_ box its own shape does not cover, and can never escape the box however far it is offset. That is why an inset spread runs the other way: growing the shape shrinks the lit interior, which _thickens_ the band. The last two swatches show a comma‑separated list, where the first shadow in the list paints on top of the ones after it, and where an outer and an inset shadow coexist because they sit in different slots of the box's paint order — outer behind the background, inset over the background but under the border.

A shadow with no blur is a plain vector fill, so the common patterns — a hard offset, a spread ring, an `inset` colour spine — stay fully vector in PDF output. A _blurred_ shadow needs an offscreen raster surface, which a PDF writer does not have and PDF has no operator for; there it degrades to the same shadow with a hard edge, at the same place and size, rather than rasterising the page or dropping the decoration.

**27 / TRACKING**

## The `letter-spacing` & `word-spacing` Properties

Extra space added _between_ characters and _at_ word separators. Both are inherited, both accept negative lengths, and both are folded into each glyph's advance during shaping — so line breaking, intrinsic sizing and alignment all see the adjusted widths without knowing the properties exist. `normal` resets an inherited value, and an `em` length inherits as _specified_, re‑resolving against each descendant's own font size.

#### `letter-spacing` — positive tracks, negative tightens

Tracking changes the texture of a line.

The control: `letter-spacing:normal`.

Tracking changes the texture of a line.

`letter-spacing:3px` — every character gains 3px of advance.

Tracking changes the texture of a line.

`letter-spacing:-0.5px` — a negative value is legal and tightens. Advances are floored at zero, so an extreme negative value overlaps glyphs rather than producing the negative advances the line breaker cannot represent.

#### `word-spacing` — separators only

Only the gaps between words grow here.

`word-spacing:10px` widens U+0020 and U+00A0 and nothing else, so the words themselves are untouched.

#### Where the spacing lands at the end of a line

flush right

flush right

Both boxes are right‑aligned. CSS Text 3 words `letter-spacing` as spacing _between_ characters, but every shipping browser adds it after _every_ character including the last — which is why the tracked line stops one tracking‑width short of the right edge. This engine matches the browsers. SVG deliberately does _not_: SVG 1.1's wording adds no trailing gap, so `pkg/svg` keeps its trailing edge flush. Two specs, not an inconsistency.

**28 / TRACKING, CONTINUED**

## Tracking changes where lines break

Spacing is folded into each glyph's advance during shaping, so everything downstream sees the adjusted widths without knowing the properties exist. Line breaking is the visible proof.

tracking moves words

tracking moves words

The same text in the same box. The spacing is folded into each glyph's advance, so the greedy line breaker sees the wider run and pushes a word down with no change to the breaker itself — the same mechanism that makes _min‑content_ and _max‑content_ account for tracking.

**29 / UNRESOLVABLE FONT FAMILIES**

## Text always renders, even with no matching font

A `font-family` naming only families the host does not have used to render _nothing at all_ — the run was skipped, so the page came out an empty box. Every line below names at least one unavailable family; all of them draw.

A family that exists nowhere.

The whole list is unresolvable, so it degrades to the bundled serif and is logged once. Previously: no ink whatsoever.

An absent family, then a generic.

The generic keyword still terminates the list the way CSS says it should — this line was always fine, and is the control proving the fallback above did not simply replace normal resolution.

**Bold** and _italic_ survive the substitution.

The fallback is style‑aware: the bundled bold and italic faces are selected, rather than every run collapsing to one regular face. The weight and slant here come from `<strong>` and `<em>` via the UA stylesheet — see §30.

A related failure sat one layer down. The OS font matcher never reports a miss — asked for a family it does not have, it confidently returns some other installed font. A request for `DejaVu Sans` came back as Lucida Grande, and `Roboto`, `IBM Plex Mono` and a deliberately nonexistent name all returned the identical Arial Unicode bytes. Each resolved face is now checked against the family its own `name` table declares, so a wrong answer becomes an honest miss and falls through to the bundled face above.

**30 / INLINE EMPHASIS DEFAULTS**

## Semantic emphasis tags carry their own weight and slant

These tags need no author CSS: the user‑agent stylesheet gives them a weight or a slant, the way a browser does. Without those rules the markup still parsed and still converted to Markdown correctly — it was only _invisible_ in every rasterized format.

**strong** and **b** are bold. _em_, _i_, _cite_, _var_ and _dfn_ are italic. u and ins are underlined.

Each tag resolves to exactly the same computed style as its CSS equivalent (`font-weight:bold`, `font-style:italic`, `text-decoration:underline`) — the UA rules are the only difference between this line and unstyled text.

** A bold line where strong is nested inside.**

Nesting does not flatten: `<strong>` inside already‑bold text stays bold. The HTML spec spells these defaults `bolder` and `smaller`, but this engine's `font-weight` is the binary bold/normal its four‑style bundled families can express and its `font-size` takes a length, so neither relative keyword parses — they would be dropped as invalid and the emphasis would stay invisible. The sheet uses `bold`, the honest spelling of what the renderer can do, and a test pins that choice.

`<small>` and `<big>` are deliberately absent rather than given a hardcoded pixel size, which would be wrong at every font‑size but the default. `<mark>` is absent too: its default is a _background_, and backgrounds do not paint on non‑replaced inline boxes here — an inline `<span>` with an explicit `background-color` paints nothing, while the same span at `display:inline-block` paints. A `<mark>` rule would cascade and then do nothing, which reads as support; it belongs with the inline‑background work instead.

**31 / CLAMPING CONTENT**

## The other three ways to clamp a box

Only `height` plus `overflow: hidden` used to clip. The other spellings parsed, cascaded, and then quietly did nothing, so an author who reached for `max-height` got an unclipped box and no diagnostic — a content overflow that reads as a layout bug.

#### `max-height` + `overflow:hidden`

#### `height` + `overflow:clip`

#### `max-height` + `overflow-y`

A 320×320 child in each. All three cut it at the padding box, identically to the `overflow: hidden` window in §7 — before this, every one of them painted the full 320px.

#### What each fix was

`overflow: clip` was dropped by the value check, so the property kept its `visible` initial. It clips exactly like `hidden` here: the two differ only in forbidding programmatic scrolling and allowing `overflow-clip-margin`, and this engine's single tall page has neither. `overflow-x`/`overflow-y` and the two‑value shorthand now fold onto the same clip flag, with the _clipping_ keyword winning when the axes disagree — a deliberate over‑clip on `visible hidden`, since dropping the clip entirely is the worse error.

`max-height` was only ever applied on the fixed‑height path, which runs when `height` is _not_ auto — so `max-height` alone never bounded anything. It now clamps the auto height too, after float enclosure and before the clip rect is built, so the box, its clip, and its parent's advance all agree. `min-height` still applies after `max-height` per CSS 10.7, so a min above the max wins.

Anonymous boxes are deliberately exempt. They carry a copy of the parent's computed style so inherited text properties reach their inline content, but CSS 9.2.1.1 gives them no properties of their own — clamping them truncated boxes the author never sized. A WPT reftest caught it.

**32 / INLINE BOX DECORATION**

## Backgrounds and borders on inline boxes

A background on a `<span>` used to paint nothing at all. The property parsed and cascaded; the paint step simply had no geometry for a non‑replaced inline box, so the most common highlight idiom on the web was dropped in silence.

Highlighted words sit inside ordinary prose, and the rect follows the text rather than the line box.

One spanthen another, adjacent and distinct. Samecolour stays two boxes too.

A span whose text is long enough to wrap paints one rect per line, which falls out of coalescing per line box rather than any explicit bookkeeping.

Padded​|​unpadded​| — the bar after the first span sits past its padding, not under it.

A bordered inline and big TALL text, whose rect grows to its tallest glyph.

The `<mark>` element finally works: marked text.

#### Why this was not a one-liner

An inline box has no single rectangle. It is flattened into glyph runs during line breaking, so the box identity is gone by paint time — and a box that wraps needs _one rect per line_. The fix carries the innermost inline box's identity onto every glyph and coalesces consecutive glyphs per line, the same shape `text-decoration` already uses for underlines.

Identity is a _pointer_, not a colour. Two adjacent spans with the same background are still two boxes; a colour comparison would merge them and a plain boolean — how underline does it — could not tell them apart at all. Inkless glyphs are retained for this pass too, or the space in “two words” would split one rect into two.

Padding is part of _layout_, not just paint. It rides on a zero‑ink edge glyph at each boundary, so the line breaker, intrinsic sizing, and alignment all reserve the space by reading advances — none of them needs to know inline boxes exist. Painting a padded rect without that would draw a background wider than the layout ever agreed to.

Still absent, deliberately: background _images_, vertical padding and margins (which per CSS 10.6.1 overflow the line box instead of growing it), and per‑edge or rounded inline borders. Each needs layout work this slice does not do, so each is omitted rather than half‑applied.

**33 / COLOR-MIX()**

## Mixing colours in a named interpolation space

Every swatch below is `color-mix(in <space>, red, blue)` — the same two colours, mixed 50/50, eleven times. The space is not a formality: it decides the answer.

`srgb` 128,0,128

`srgb-linear` 188,0,188

`hsl` 255,0,255

`hwb` 255,0,255

`lab` 193,0,136

`oklab` 140,83,162

`lch` 245,0,134

`oklch` 186,0,194

`xyz` 188,0,188

#### Hue interpolation

`shorter hue` — via magenta

`longer hue` — the long way, via green

`increasing hue`

`decreasing hue`

#### Weights and alpha

`red 30%` — the remainder goes to blue

Both `20%` — weights normalize _and_ the alpha drops to 0.4

Premultiplied — the half‑transparent red gives up hue, so this is not 128,0,128

A 24% wash over white

Every expected value was _captured from Chrome_ rather than derived here, and the tests pin them byte‑for‑byte. A transposed conversion matrix or a swapped white point yields colours that look entirely plausible and are quietly wrong — and a test written from the same arithmetic as the implementation would happily agree with the bug.

Mixing with `transparent` is the one deliberate divergence. Premultiplication weights a zero‑alpha colour's channels by zero, so the opaque colour's channels must survive _untouched_ and only its alpha scales — making `color-mix(in srgb, X N%, transparent)` exactly `rgba(X, N/100)`. Chrome reports up to 2/255 off from rounding through an intermediate space; the engine keeps the exact value. Nested mixes stay in float for the same reason: quantizing between levels turns Chrome's 191 into 192.

**34 / INLINE SVG & THE HOST CASCADE**

## The page's CSS reaches inside inline `<svg>`

An inline `<svg>` is part of the host document, so a rule in the page's stylesheet styles its children. It previously did not: the subtree was re-serialized to markup and re-parsed as a _standalone_ document, so the page's CSS never reached it and every shape fell back to SVG's default black fill.

Not one `fill` or `stroke` attribute in that markup — every colour comes from `.chart .curve`, `.chart .area` and friends in the page stylesheet. That includes _descendant_ selectors rooted outside the `<svg>`: the ancestor chain now continues past the SVG root into the host tree, so `.chart .curve` matches. Without that, `.curve` alone would work while `.chart .curve` silently did nothing — partial support that reads as a styling bug.

#### Same markup, different context

The first two are _byte‑identical_ SVG markup; only their containing `#id` differs. The parsed-SVG cache is keyed by markup, so the host context had to join that key — otherwise the second would reuse the first's parse and silently paint the wrong colour. The third takes its fill from `currentColor`, which resolves against the `color` the `<svg>` box inherits from its parent.

#### What still wins

Host sheets cascade _below_ the SVG's own `<style>`, so an internal rule takes a specificity tie — the SVG is the more specific context for its own content. A presentation attribute has zero specificity and loses to any host rule; an inline `style=` still beats everything. And an `<img src="…svg">` is deliberately untouched: a referenced SVG is a separate document, and CSS does not cascade into it. Both directions are pinned by tests, because a fix that overreaches is as wrong as one that under‑reaches.

**35 / TRUNCATION**

## `text-overflow` and `-webkit-line-clamp`

Neither existed. A single line clipped mid‑glyph with no ellipsis, and there was no way at all to truncate to N lines — so the only route was to cut the string in application code, before it ever reached the engine, guessing at how much would fit.

#### Single line

A headline long enough that it cannot fit on one line of this card

`text-overflow: ellipsis` — whole glyphs are dropped until the ellipsis fits, so the cut never lands mid‑character and the line never spills past the clip edge.

A headline long enough that it cannot fit on one line of this card

`text-overflow: clip`, the initial value — the hard cut, mid‑glyph, for contrast.

Short enough

Text that fits is untouched: no ellipsis appears just because the property is set.

#### Multiple lines

Clamping to a fixed number of lines is what a card layout actually needs, and it is the case that had no CSS answer here at all: the box stops after the second line and the ellipsis marks what was cut.

`-webkit-line-clamp: 2`. The box is genuinely _two lines tall_ — the clamp changes layout, not just paint, so the height a browser would report is the height this engine lays out.

Clamping to a fixed number of lines is what a card layout actually needs, and it is the case that had no CSS answer here at all: the box stops after the second line and the ellipsis marks what was cut.

The same text at `-webkit-line-clamp: 3`.

Two short lines.

A clamp larger than the content is inert — no height change and no ellipsis, because nothing was cut. An ellipsis that appears when nothing was truncated is a lie about the text.

#### How the cut is chosen

Truncation happens in _glyph_ units, where the advances are already resolved, so the fit is exact rather than a re‑measure of a guessed substring. Trailing whitespace is dropped before the ellipsis — “foo …” reads as a gap — and the ellipsis inherits the styling of the glyph it follows, so a line ending in a larger or differently coloured span gets a matching one.

Two edge cases worth stating. An ellipsis needs something to hide the truncation behind, so it applies only where the box actually clips; in an `overflow: visible` box the text still overflows visibly, as browsers do. And when even the ellipsis alone will not fit, CSS Overflow 3 still requires it to render — so a box a few pixels wide shows part of an ellipsis rather than nothing, which would erase the only sign that text was cut.

**36 / COLOUR FONTS**

## Emoji, in colour

Colour glyphs painted nothing at all. A colour font's base glyph is _empty_ — its ink lives in separate tables the engine did not read — so an emoji rendered as a blank space, and naming an emoji family rendered the whole run as nothing.

#### Layered outlines — `COLR`/`CPAL`

😀🎉❤👍🌟

😀🎉❤👍🌟

Each glyph is a stack of ordinary outlines with palette colours, so it is _vector_ and scales like text. The grinning face carries a radial gradient and the party popper a linear one, with its streamers placed by genuine rotation matrices — an earlier draft modelled only offsets and mirrors, which forced it to refuse that glyph entirely.

#### Bitmap strikes — `CBDT`/`CBLC` and `sbix`

😀🎉❤👍🌟

😀🎉❤👍🌟

Other fonts — Apple Color Emoji among them, which has no `COLR` table at all — ship a rendered PNG per glyph per size. The strike nearest the used size is chosen, preferring a larger one so the image is downscaled rather than enlarged. These do _not_ scale like outlines, and saying so is more honest than implying they do.

#### In ordinary prose

An emoji with no font named resolves through the script‑fallback chain. The bundled substitutes have no emoji face — a colour emoji font is megabytes, well past what the toolkit embeds — so emoji route to an _installed_ one instead: Apple Color Emoji, Segoe UI Emoji, Noto Color Emoji and friends, in that order. On a host with none, the run degrades to the missing‑glyph path rather than painting a wrong character.

That path is deliberately _not_ exercised on this page. The goldens render hermetically, with no system fonts, so an emoji here would show a missing‑glyph box — which is the honest result for a host with no emoji font, and a poor demonstration of a feature that works. The two rows above name a font explicitly, and it ships as a fixture, so they render identically everywhere.

#### What is deliberately absent

A colour glyph the engine cannot express — a sweep (conic) gradient, or a composite paint needing per‑layer group compositing — is refused as a whole and falls back to the glyph's own monochrome outline. Painting it in a plausible flat colour would be harder to notice than painting it in none, which is the failure this whole area was reported for.

A colour glyph keeps the _font's_ colours rather than the CSS `color`. A font can opt a layer into the text colour through the palette's foreground sentinel; that is the font's choice to make, not the document's.

**37 / LINE-HEIGHT**

## The unitless multiplier

A unitless `line-height` — the commonest spelling of the property — was rejected as an invalid length, so the declaration was dropped and every block used the font‑metric height instead. The property appeared to do nothing at all, and the slack compounded down a page.

Two lines at `line-height: 1`, set tight enough that the ascenders and descenders nearly touch.

Two lines at `line-height: 1.5`, the usual comfortable measure for body copy.

Two lines at `line-height: 2.5`, loose enough that the leading is unmistakable.

All three are the same text at the same font size. Before this they rendered identically.

#### The units agree — except when they inherit

At one font size, `2`, `2em`, `200%` and `30px` all give the same line box on 15px text. They diverge on _inheritance_, and the difference is the reason the units stay distinct downstream: a _number_ inherits as a number and re‑multiplies against each descendant's own font size, while an `em` or `%` is computed against the element that declares it and inherits as a fixed length (CSS 2.1 §10.8.1).

A 26px child inheriting `line-height: 2` from a 10px parent gets a 52px line box.

The same child inheriting `2em` gets a 20px one — computed against the parent, then fixed.

Getting that backwards is invisible until a nested font‑size change appears, which is exactly when it matters, so both directions are pinned by tests.

#### What did not change

`line-height: normal` and an absent declaration both keep the font‑metric height, so a document that never states the property renders exactly as before. And a unitless number is still _not_ a length: `width: 5` remains invalid, because accepting it there would be a hole in every length rather than a fix to one property.

**38 / FLEX MARGINS**

## Margins on flex children

Flex layout was margin‑blind: an item's size and position came from its border box, so `margin-top` on a child of a flex column did nothing at all — while the identical rule on a block child worked. That reads as “the rule did not apply”, which is the most expensive kind of failure to diagnose, because the stylesheet is plainly correct.

No margin — the item sits at the container's start edge.

`margin-left: 80px` on the main axis.

`margin-left: auto` absorbs the free space and pushes the item to the end — the idiomatic way to do it (CSS Flexbox §8.1). Auto margins take the space _before_ `justify-content` sees any, which is the spec's own resolution order.

Two auto margins centre it.

`justify-content: flex-end` with a trailing margin: the item's _margin box_ is what lands against the end edge, so the margin has to be counted while free space is distributed — not applied as an offset afterwards.

A column: `margin-top` is the main axis and `margin-left` the cross one. Both apply, and the line grows to hold the item's margin box rather than letting a cross margin overflow it.

#### An auto-height column grows for its children's margins

The container has no stated height, so its height _is_ its content extent — and that extent has to enclose the children's margin boxes. The tinted background is the container itself: it ends below the third item, with the middle item's 40px `margin-bottom` inside it rather than spilling past. Line packing, free‑space distribution and cross sizing all counted margins already; the branch that content‑sizes a column with an indefinite main size still summed the border box, so the children were placed correctly and then overflowed the container that should have grown for them. The case above uses an explicit `height`, which takes the other branch and was always right — which is why it took an auto‑height column to show the gap.

#### The block control

The same `margin-top` on a _block_ child was always correct, and still is — a test pins the two to the same position. This distinction matters: the original report contrasted a flex column against a plain block whose bare `margin-top` measured zero, but that is CSS margin _collapsing_ through the body, not a bug. Only the flex case was wrong.

**39 / BACKGROUND LAYERS**

## Comma-separated `background` lists

Layering backgrounds on one element — the normal way to put a gradient over a base colour — made the whole declaration paint _nothing_. Only a single value was accepted, and adding the fallback colour that every browser wants was exactly what broke it. The failure was silent and total, so it read as “gradients are unsupported” rather than “the list was rejected”.

A gradient over a fallback colour. The colour is legal in the _last_ layer only (CSS Backgrounds §3.10); one earlier is a parse error, not something to quietly ignore.

Two gradients. The _first_ layer paints on top, and it fades to transparent on the right so the green beneath shows through — which is the proof that both layers paint, rather than the first one winning.

Three layers: a fully transparent one, a gradient, and a base colour. Everything composites in order.

#### What this needed

The layers travel from the cascade through the box tree to paint as a _list_, emitted last‑first so the first layer ends up on top. `background-image` takes the same list.

One subtlety worth recording, because getting it wrong is silent: the per‑layer records carry only the _image_. An early version also captured `background-size` and friends when the image list was parsed, which discarded any of those declared _after_ it — so `background-image` then `background-size` lost the size, while the reverse order worked. Those properties are single‑valued here and are read at layout time instead, so declaration order stops mattering. Making them genuinely per‑layer is a separate slice, and FEATURES.md says so.

**40 / SVG INHERITANCE**

## Presentation attributes reach the children

A paint property set on the root `<svg>` did not reach its children. The root's attributes were resolved and then _discarded_ — only the font and text properties were copied back — so a path relying on an inherited `stroke` painted nothing at all.

Not one `stroke` or `fill` on any child — every colour, width, cap and join comes from the root and inherits down. This is exactly how the icons that surfaced the bug were authored: the first icon's rays, the third one's outline. Before this, each would have shown only its filled parts, which reads as “the icon is not rendering” rather than “the strokes are gone”.

#### The construction, and why it is inverted

The old code listed the properties that inherit, and anything missing from that list was silently dropped — which is how the entire paint vocabulary came to be absent. It now starts from the root's fully resolved style and clears only the properties CSS marks _non_‑inherited, so a property added later defaults to the spec's answer rather than to “dropped”.

The non‑inherited set matters as much: `opacity`, `clip-path`, `mask` and `filter` must _not_ reach the children, or the root's value would apply twice — once to its own group and again to each child. A 50% root over a black child would composite to 75% rather than 50%, which a test pins.

The whole resvg conformance corpus passes unchanged, which is the strongest evidence available that the inherited/non‑inherited split is right: those fixtures exercise inheritance directly.

**41 / ABSOLUTE POSITIONING & FLEX**

## Boxes that would not size or place

Three related failures, all silent: a box given both `top` and `bottom` collapsed to nothing, an absolutely positioned child of a flex container ignored its offsets, and an element whose height came from flex layout laid its own children out as if it had no height at all.

#### `top` + `bottom` sizes a box

Rust: `top` + `bottom`, spanning the container. Slate: an explicit `height`, for contrast. Green: both pairs at once. The rust bar used to paint _nothing_ — it collapsed to its content height, which for an empty box is zero. The horizontal equivalent (`left` + `right`) always worked, which is what made the asymmetry surprising.

#### An absolutely positioned child of a flex container

Per CSS Flexbox §4.1 such a child is _not_ a flex item: it is out of flow and positioned against the container's padding box. It was being laid out as an item instead, pinned to the container's edge with `left` discarded — so this bar sat at x = 0 no matter what offset it was given.

#### A flex-derived height aligns its own children

Left: height from `flex: 1`. Right: an explicit `height`. Both centre their child. The cross size was being written onto the fragment _after_ its interior had already been laid out, so anything inside that needs a definite height — `justify-content`, a percentage height — had already resolved against auto and packed to the top.

#### One reported gap that did not reproduce

The same report said an absolutely positioned box never shrink‑wraps and that `width` is ignored on one. Measured here, neither holds: an `position: absolute` box around a 72px string comes out 72px — identical to `display: inline-block` — and an explicit `width: 76px` is honoured exactly. Recorded as not‑reproducing rather than “fixed”, since nothing was changed.

**42 / TRANSFORMS**

## `transform`

The property did nothing at all — it was not in the cascade, so every declaration was silently dropped. It was reported alongside a flex‑margin bug, but it is not flex‑specific: `translateX` on a plain block did nothing either.

none

translateX

scale(.7)

rotate

skewX

chained

The dashed outline is the space each box still occupies: a transform is a _paint‑time_ effect and does not change layout (CSS Transforms 1 §3), so the boxes stay on their grid while their ink moves. The default origin is the box's centre, which is why `scale(.7)` shrinks inward rather than toward a corner, and why the percentage in the last tile resolves against the box's own width.

#### How it is implemented, and the trap in it

Because it changes no layout, the transform is a matrix _bracket_ around the box's already‑flattened items — the same shape the `filter` bracket uses. That only works if the box's background and its content are emitted as one contiguous run, and for a plain block they are not: Appendix E paints child decorations and in‑flow content in separate phases, so a bracket would have moved the background and left the text behind.

The resolution is the spec's own: a transformed element establishes a _stacking context_ and a containing block for its descendants, and such a box already paints as a single atom. Marking it so was the fix — not a workaround for the bracket.

The trap: an _anonymous_ box carries a zero‑value style, and a zero matrix is not the identity matrix. Without a guard, every anonymous box became a stacking context and a BFC, which reordered painting and broke a WPT reftest. It is the second time that same zero‑value distinction has bitten in this codebase, so it is called out at the predicate itself.

#### What is refused

The 3D functions — `translate3d`, `rotateX`, `perspective`, `matrix3d` — are rejected rather than flattened. This engine has no 3D pipeline, and dropping the Z terms would paint something confidently wrong; a refused declaration drops and the previous value stands, which is at least visible. `transform-origin` is not yet settable — the centre is used unconditionally.

**43 / WRITING MODES**

## `writing-mode`: vertical text

A vertical writing mode turns the baseline through a right angle: the pen walks _down_ the page and lines stack across it. The property reached the cascade but nothing honoured it, so a sidebar label styled `writing-mode: vertical-rl` laid itself out horizontally and overflowed the narrow box it was given.

### A vertical label beside horizontal text

NOW

The label to the left is a single `div` with `writing-mode: vertical-rl` and no other positioning. Its box grows _downward_ to fit the text's own extent, the way a horizontal box grows sideways — which is what makes it size itself correctly instead of spilling out of a fixed width.

Look for: the letters stacked _evenly_. The vertical advance comes from the font's vertical metric — one em — not from each letter's width. Advancing by width instead spaces an `N` and a `W` differently, which reads as plausible-but-wrong text rather than as a bug, and is precisely how the first implementation of this passed its own test.

### The same string, both modes

NOW

horizontal‑tb

NOW

vertical‑rl

Both boxes are 40 px wide. The horizontal one overflows its width; the vertical one turns the run down the page and takes the height it needs. That difference — a box that _grows along the text's own axis_ — is the whole of what turning the line vertical delivers; which way each glyph _faces_ within it is the next question.

### The same properties in SVG

SVG places `<text>` itself rather than through the CSS inline layer, so it inherited none of the above for free — it laid every vertical value out horizontally and said so. It now shares the orientation classifier and the vertical advance, so the two paths agree by construction rather than by two implementations happening to match.

Look for: the underlined run. Its rule turns with the glyphs rather than staying axis‑aligned, because the decoration rect is built in the segment's own unrotated frame and carried through the same matrix the glyph is. SVG 1.1's `tb` and `tb-rl` spellings resolve onto `vertical-rl`, and `text-anchor` aligns a vertical chunk along _its_ axis — down the page, not across it.

### `text-orientation`: which way each glyph faces

A vertical line has to decide, per glyph, whether it stands upright or lies on its side. `mixed` — the initial value — keeps upright scripts upright and turns everything else a quarter turn, which is how CJK text sets with Latin embedded in it. `upright` stands every glyph up; `sideways` turns every glyph, including the ones `mixed` would leave alone.

Wax

mixed (initial)

Wax

upright

Wax

sideways

Look for: `mixed` and `sideways` agree here, because Latin rotates under both — the two differ only on the scripts `mixed` keeps upright. Note also that the `upright` run is _taller_: an upright glyph advances a full em regardless of how wide the letter is, while a rotated one advances by its own width.

The case that actually separates `mixed` from `sideways` — a Han character standing upright beside recumbent Latin — **cannot be shown here**: no bundled face covers CJK, so it would render as a row of empty boxes and demonstrate nothing. It is covered by unit tests against the classifier instead, which is the honest place for a claim the page cannot make visually.

Which glyphs stay upright is Unicode's `Vertical_Orientation` property (UAX #50). Neither the standard library nor this engine's font dependency ships that table, so the check here approximates it from the script tables plus the CJK punctuation and full‑width blocks — and errs toward _rotating_, because a wrongly‑rotated glyph looks obviously wrong while a wrongly‑upright one just looks like `upright` was intended.

#### What is not implemented

One line only. There is no vertical line _wrapping_: a run longer than the block extent overflows and logs, rather than starting a second line beside the first. `vertical-lr` is accepted and lays out identically to `vertical-rl` — the two differ only in which side subsequent lines stack from, and there are none — so it logs rather than quietly equating them. A _shrink‑to‑fit_ vertical box (inline‑block, float) is still measured on the horizontal axis, so it comes out as wide as its text is long; transposing that means turning the intrinsic‑measure seam that table, grid and flex sizing all share, so it logs instead. Text decoration and inline‑box backgrounds are skipped on a vertical line: every span the painter computes is an X range, and drawing them anyway would rule a line across the page instead of beside the text. Vertical alternate glyph forms (the `vert`/ `vrt2` font features) are out of scope — a rotated glyph is the rotated Latin form, not a purpose‑designed vertical one.

**44 / AUTO MARGINS**

## `margin: 0 auto`

The most common centering idiom in CSS did nothing. Auto margins resolved to zero, so every centered‑layout document rendered hard against the left edge — silently, and for as long as the engine had existed. No document in the corpus used it, which is why nothing caught it.

margin: 0 auto

margin-right: auto

margin-left: auto

margin-left: 50px; margin-right: auto

Every rail is the same width, so the only thing moving each box is how its margins resolve. Two auto margins split the leftover space evenly and centre the box; _one_ auto margin absorbs all of it and pushes the box to the opposite edge — which is why the second and third rails are mirror images. A specified margin is honoured as‑is and the auto one takes only what remains, so the fourth box sits at exactly 50px rather than being centred.

#### What the leftover is measured from

100 + 48

The space to distribute is what is left after the whole _border_ box — content, padding and borders together (CSS 2.1 §10.3.3). This box has a 100px content width but 20px of padding and a 4px border on each side, so 148px is centred, not 100px. Measuring from the content box instead would shift it 24px to the right.

#### The case that must not centre

wider than its container

When the box is _wider_ than the space it sits in, the leftover is negative. A naïve half‑split would give each margin a negative value and drag the box off the left edge of the page; the spec instead treats the auto margin as zero, so the box starts at the left and overflows to the right — what every browser does. The clamp is a one‑line floor, and the test that pins it is the reason it is not a one‑line bug.

Vertical auto margins are untouched: in normal flow they compute to zero (CSS §10.6.3), and vertical centering with `auto` is a flex/grid feature that those paths resolve themselves. Only the horizontal axis is handled here.

**45 / FONT FALLBACK**

## Per-character font fallback

A face that lacks a character no longer draws a box or a wrong glyph. Each character is checked against the faces in the `font-family` list, then the bundled scripts, then a fonts directory the caller supplies, then installed fonts — the first face that has the glyph draws it.

#### Accented Latin

Zoë Brontë · Ñandú · Łódź · Čeština · Ångström · Øresund · Ősz

The bundled TeX Gyre faces map accented code points to small‑cap alternates in their own cmap, so “Zoë” once printed with a small‑cap “e”. A glyph is now found by its plain Adobe Glyph List name first (`edieresis`, not `edieresis.sc`).

#### Greek and Cyrillic

Ελληνικά · Αθήνα · Θεσσαλονίκη

Москва · Київ · Беларусь · Србија

The Latin faces have no Greek or Cyrillic letters (or only part of the Greek). These words come from the bundled Go fonts, which cover the WGL4 repertoire; a letter outside it goes on to the caller’s fonts directory and then installed fonts.

#### Mixed in one line

Invoice for Zoë Ivanova (Иванова), Αθήνα — €1.250,00

One run of text can mix scripts: the fallback is per character, so the Latin letters stay in the requested face and only the letters it lacks change face.
