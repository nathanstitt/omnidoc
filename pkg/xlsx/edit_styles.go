package xlsx

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/beevik/etree"
	"github.com/nathanstitt/omnidoc/pkg/xlsx/internal/xmlpart"
)

// styleSheetOrder is the CT_Stylesheet child sequence.
var styleSheetOrder = []string{
	"numFmts", "fonts", "fills", "borders", "cellStyleXfs", "cellXfs",
	"cellStyles", "dxfs", "tableStyles", "colors", "extLst",
}

// fontChildOrder is the conventional CT_Font child sequence Excel emits.
var fontChildOrder = []string{
	"b", "i", "strike", "condense", "extend", "outline", "shadow", "u",
	"vertAlign", "sz", "color", "name", "family", "charset", "scheme",
}

// StylePatch is a partial style change: nil leaves are untouched, set leaves
// land on TOP of the cell's existing style — every facet the patch does not
// name (diagonal borders, indent, protection, theme colors, unknown style
// XML) rides along, because the patch clones and edits the cell's existing
// style records rather than replacing them. It mirrors the all-pointer-leaf
// patch shape a spreadsheet application's style overlay uses.
type StylePatch struct {
	Font      *FontPatch
	Fill      *FillPatch
	Alignment *AlignmentPatch
	Border    *BorderPatch
	// NumFmt is the format pattern; a pointer to "" clears to General (id 0).
	NumFmt *string
}

// FontPatch patches font leaves. String pointers to "" clear the leaf.
type FontPatch struct {
	Bold, Italic, Strike *bool
	// Underline is the u style ("single", "double", ...); "" removes it.
	Underline *string
	Size      *float64
	Name      *string
	// Color is "RRGGBB"; "" removes the explicit color.
	Color *string
}

// FillPatch patches the pattern fill. Pattern "" clears the fill to none.
type FillPatch struct {
	Pattern *string
	// Fg / Bg are "RRGGBB" pattern colors.
	Fg, Bg *string
}

// AlignmentPatch patches alignment leaves ("" clears an axis; false clears
// wrap).
type AlignmentPatch struct {
	Horizontal, Vertical *string
	WrapText             *bool
}

// BorderPatch patches the four edges; nil edges are untouched.
type BorderPatch struct {
	Top, Right, Bottom, Left *EdgePatch
}

// EdgePatch sets one border edge's style/color, or clears the edge entirely.
type EdgePatch struct {
	// Style is the OOXML edge style name ("thin", "medium", "dashed", ...).
	Style *string
	// Color is "RRGGBB".
	Color *string
	// Clear removes the edge (style and color) — the explicit delete.
	Clear bool
}

// resolvedStyles returns the style table reflecting the styles part's CURRENT
// tree (including this session's edits), rebuilt only when the part changed.
func (f *File) resolvedStyles() styleTable {
	if f.styleCacheGen == f.styleGen && f.styleCacheGen >= 0 {
		return f.styleCache
	}
	var data []byte
	if p, ok := f.parsed["xl/styles.xml"]; ok {
		data, _ = p.Bytes()
	} else {
		data = f.rawPart("xl/styles.xml")
	}
	f.styleCache = parseStyles(data)
	f.styleCacheGen = f.styleGen
	return f.styleCache
}

// DefaultFont reads the workbook default font (see Workbook.DefaultFont).
func (f *File) DefaultFont() Font { return f.resolvedStyles().defaultFont }

// CellStyle reads a cell's fully resolved style (the zero Style for an
// unstyled cell).
func (s *SheetEdit) CellStyle(row, col int) Style {
	return s.styleAt(s.Cell(row, col).StyleID)
}

// styleAt resolves an xf index through the (possibly edited) styles tree.
func (s *SheetEdit) styleAt(xf int) Style {
	if st := s.file.resolvedStyles().styleFor(xf); st != nil {
		return *st
	}
	return Style{}
}

// PatchCellStyle applies a partial style change to one cell: the cell's
// existing xf (and its font/fill/border records) are CLONED, only the named
// leaves are edited, and the results are deduped back into the style tables —
// so every unmodeled facet survives. An all-nil patch is a no-op.
func (s *SheetEdit) PatchCellStyle(row, col int, p StylePatch) error {
	if row < 1 || col < 1 {
		return fmt.Errorf("%w: (%d, %d)", ErrBadRef, row, col)
	}
	if patchEmpty(p) {
		return nil
	}
	rowEl := s.ensureRow(row)
	if rowEl == nil {
		return fmt.Errorf("xlsx: sheet %s has no sheetData", s.name)
	}
	c := ensureCell(rowEl, row, col)
	cur, _ := strconv.Atoi(c.SelectAttrValue("s", "0"))
	next, err := s.file.patchXf(cur, p)
	if err != nil {
		return err
	}
	if next != cur || c.SelectAttr("s") == nil {
		c.CreateAttr("s", strconv.Itoa(next))
	}
	return nil
}

// SetCellStyle REPLACES a cell's style with exactly the given facets (the
// whole-style setter; unmodeled facets of the previous style do not carry
// over — use PatchCellStyle to overlay).
func (s *SheetEdit) SetCellStyle(row, col int, st Style) error {
	if row < 1 || col < 1 {
		return fmt.Errorf("%w: (%d, %d)", ErrBadRef, row, col)
	}
	rowEl := s.ensureRow(row)
	if rowEl == nil {
		return fmt.Errorf("xlsx: sheet %s has no sheetData", s.name)
	}
	c := ensureCell(rowEl, row, col)
	idx, err := s.file.xfForStyle(st)
	if err != nil {
		return err
	}
	c.CreateAttr("s", strconv.Itoa(idx))
	return nil
}

// RowStyle reads a row-level style (rows with customFormat).
func (s *SheetEdit) RowStyle(row int) (Style, bool) {
	el := s.findRow(row)
	if el == nil || !onOff(el.SelectAttrValue("customFormat", "")) {
		return Style{}, false
	}
	xf, _ := strconv.Atoi(el.SelectAttrValue("s", "0"))
	return s.styleAt(xf), true
}

// PatchRowStyle overlays a patch onto a row's style (creating one from the
// default when the row had none).
func (s *SheetEdit) PatchRowStyle(row int, p StylePatch) error {
	if row < 1 {
		return fmt.Errorf("%w: row %d", ErrBadRef, row)
	}
	if patchEmpty(p) {
		return nil
	}
	el := s.ensureRow(row)
	if el == nil {
		return fmt.Errorf("xlsx: sheet %s has no sheetData", s.name)
	}
	cur := 0
	if onOff(el.SelectAttrValue("customFormat", "")) {
		cur, _ = strconv.Atoi(el.SelectAttrValue("s", "0"))
	}
	next, err := s.file.patchXf(cur, p)
	if err != nil {
		return err
	}
	el.CreateAttr("s", strconv.Itoa(next))
	el.CreateAttr("customFormat", "1")
	return nil
}

// SetRowStyle replaces a row's style wholesale.
func (s *SheetEdit) SetRowStyle(row int, st Style) error {
	if row < 1 {
		return fmt.Errorf("%w: row %d", ErrBadRef, row)
	}
	el := s.ensureRow(row)
	if el == nil {
		return fmt.Errorf("xlsx: sheet %s has no sheetData", s.name)
	}
	idx, err := s.file.xfForStyle(st)
	if err != nil {
		return err
	}
	el.CreateAttr("s", strconv.Itoa(idx))
	el.CreateAttr("customFormat", "1")
	return nil
}

// ClearRowStyle removes a row-level style.
func (s *SheetEdit) ClearRowStyle(row int) {
	if el := s.findRow(row); el != nil {
		if _, err := s.mut(); err == nil {
			el.RemoveAttr("s")
			el.RemoveAttr("customFormat")
		}
	}
}

// patchEmpty reports an all-nil patch.
func patchEmpty(p StylePatch) bool {
	return p.Font == nil && p.Fill == nil && p.Alignment == nil && p.Border == nil && p.NumFmt == nil
}

// stylesRoot returns the styles part root for mutation, bumping the
// resolution-cache generation.
func (f *File) stylesRoot() (*etree.Element, error) {
	p, err := f.mutatePart("xl/styles.xml")
	if err != nil {
		return nil, err
	}
	f.styleGen++
	return p.Root(), nil
}

// patchXf clones xf index cur, applies the patch, and dedupes the result,
// returning the target xf index.
func (f *File) patchXf(cur int, p StylePatch) (int, error) {
	root, err := f.stylesRoot()
	if err != nil {
		return 0, err
	}
	cellXfs := xmlpart.EnsureChildInOrder(root, "cellXfs", styleSheetOrder)
	xfs := xmlpart.Children(cellXfs, "xf")

	var xf *etree.Element
	if cur >= 0 && cur < len(xfs) {
		xf = xfs[cur].Copy()
	} else {
		xf = defaultXfNode()
	}

	if p.Font != nil {
		fontID := f.patchStyleRecord(root, "fonts", "font", attrInt(xf, "fontId"), func(font *etree.Element) {
			applyFontPatch(font, p.Font)
		})
		xf.CreateAttr("fontId", strconv.Itoa(fontID))
		xf.CreateAttr("applyFont", "1")
	}
	if p.Fill != nil {
		fillID := f.patchStyleRecord(root, "fills", "fill", attrInt(xf, "fillId"), func(fill *etree.Element) {
			applyFillPatch(fill, p.Fill)
		})
		xf.CreateAttr("fillId", strconv.Itoa(fillID))
		xf.CreateAttr("applyFill", "1")
	}
	if p.Border != nil {
		borderID := f.patchStyleRecord(root, "borders", "border", attrInt(xf, "borderId"), func(border *etree.Element) {
			applyBorderPatch(border, p.Border)
		})
		xf.CreateAttr("borderId", strconv.Itoa(borderID))
		xf.CreateAttr("applyBorder", "1")
	}
	if p.Alignment != nil {
		applyAlignmentPatch(xf, p.Alignment)
		xf.CreateAttr("applyAlignment", "1")
	}
	if p.NumFmt != nil {
		id := numFmtIDFor(root, *p.NumFmt)
		xf.CreateAttr("numFmtId", strconv.Itoa(id))
		if id != 0 {
			xf.CreateAttr("applyNumberFormat", "1")
		} else {
			xf.RemoveAttr("applyNumberFormat")
		}
	}

	return dedupeAppend(cellXfs, "xf", xf), nil
}

// patchStyleRecord clones the id'th record of a style table (fonts/fills/
// borders), applies edit, and dedupes it back, returning the record index.
func (f *File) patchStyleRecord(root *etree.Element, table, tag string, id int, edit func(*etree.Element)) int {
	container := xmlpart.EnsureChildInOrder(root, table, styleSheetOrder)
	records := xmlpart.Children(container, tag)
	var rec *etree.Element
	if id >= 0 && id < len(records) {
		rec = records[id].Copy()
	} else {
		rec = defaultStyleRecord(tag)
	}
	edit(rec)
	return dedupeAppend(container, tag, rec)
}

// defaultStyleRecord builds the zero record for a style table.
func defaultStyleRecord(tag string) *etree.Element {
	el := etree.NewElement(tag)
	switch tag {
	case "fill":
		pf := el.CreateElement("patternFill")
		pf.CreateAttr("patternType", "none")
	case "border":
		for _, edge := range []string{"left", "right", "top", "bottom", "diagonal"} {
			el.CreateElement(edge)
		}
	}
	return el
}

// defaultXfNode is the all-defaults cell format.
func defaultXfNode() *etree.Element {
	xf := etree.NewElement("xf")
	xf.CreateAttr("numFmtId", "0")
	xf.CreateAttr("fontId", "0")
	xf.CreateAttr("fillId", "0")
	xf.CreateAttr("borderId", "0")
	return xf
}

// dedupeAppend returns the index of an existing equivalent record, or appends
// el and returns its new index (updating the container's count attribute).
func dedupeAppend(container *etree.Element, tag string, el *etree.Element) int {
	records := xmlpart.Children(container, tag)
	for i, existing := range records {
		if xmlpart.Equal(existing, el) {
			return i
		}
	}
	container.AddChild(el)
	container.CreateAttr("count", strconv.Itoa(len(records)+1))
	return len(records)
}

// attrInt reads an integer attribute (0 when absent/malformed).
func attrInt(el *etree.Element, name string) int {
	n, _ := strconv.Atoi(el.SelectAttrValue(name, "0"))
	return n
}

// setToggleChild ensures/removes a presence-toggle child (<b/>, <i/>, ...).
func setToggleChild(font *etree.Element, name string, on bool) {
	if on {
		xmlpart.EnsureChildInOrder(font, name, fontChildOrder)
		return
	}
	if el := xmlpart.FindChild(font, name); el != nil {
		xmlpart.Remove(font, el)
	}
}

// setValChild ensures a <name val="..."/> child (removing it for empty).
func setValChild(font *etree.Element, name, val string, order []string) {
	if val == "" {
		if el := xmlpart.FindChild(font, name); el != nil {
			xmlpart.Remove(font, el)
		}
		return
	}
	el := xmlpart.EnsureChildInOrder(font, name, order)
	el.CreateAttr("val", val)
}

// setColorChild sets an explicit-RGB color child, clearing any indexed/theme
// scheme it previously used ("" removes the color entirely).
func setColorChild(parent *etree.Element, name, rgb string, order []string) {
	if rgb == "" {
		if el := xmlpart.FindChild(parent, name); el != nil {
			xmlpart.Remove(parent, el)
		}
		return
	}
	el := xmlpart.EnsureChildInOrder(parent, name, order)
	el.RemoveAttr("indexed")
	el.RemoveAttr("theme")
	el.RemoveAttr("tint")
	el.RemoveAttr("auto")
	el.CreateAttr("rgb", "FF"+strings.ToUpper(rgb))
}

func applyFontPatch(font *etree.Element, p *FontPatch) {
	if p.Bold != nil {
		setToggleChild(font, "b", *p.Bold)
	}
	if p.Italic != nil {
		setToggleChild(font, "i", *p.Italic)
	}
	if p.Strike != nil {
		setToggleChild(font, "strike", *p.Strike)
	}
	if p.Underline != nil {
		if *p.Underline == "" {
			setToggleChild(font, "u", false)
		} else {
			u := xmlpart.EnsureChildInOrder(font, "u", fontChildOrder)
			u.CreateAttr("val", *p.Underline)
		}
	}
	if p.Size != nil {
		setValChild(font, "sz", trimFloat(*p.Size), fontChildOrder)
	}
	if p.Name != nil {
		setValChild(font, "name", *p.Name, fontChildOrder)
	}
	if p.Color != nil {
		setColorChild(font, "color", *p.Color, fontChildOrder)
	}
}

// patternFillOrder is CT_PatternFill's color sequence.
var patternFillOrder = []string{"fgColor", "bgColor"}

func applyFillPatch(fill *etree.Element, p *FillPatch) {
	pf := fill.FindElement("patternFill")
	if pf == nil {
		pf = fill.CreateElement("patternFill")
	}
	if p.Pattern != nil {
		if *p.Pattern == "" {
			pf.CreateAttr("patternType", "none")
			for _, name := range patternFillOrder {
				if el := xmlpart.FindChild(pf, name); el != nil {
					xmlpart.Remove(pf, el)
				}
			}
		} else {
			pf.CreateAttr("patternType", *p.Pattern)
		}
	}
	if p.Fg != nil {
		setColorChild(pf, "fgColor", *p.Fg, patternFillOrder)
	}
	if p.Bg != nil {
		setColorChild(pf, "bgColor", *p.Bg, patternFillOrder)
	}
}

// borderEdgeOrder is CT_Border's edge sequence.
var borderEdgeOrder = []string{"left", "right", "top", "bottom", "diagonal", "vertical", "horizontal"}

func applyBorderPatch(border *etree.Element, p *BorderPatch) {
	apply := func(name string, ep *EdgePatch) {
		if ep == nil {
			return
		}
		edge := xmlpart.EnsureChildInOrder(border, name, borderEdgeOrder)
		if ep.Clear {
			// An empty edge element is "no border on this side".
			edge.RemoveAttr("style")
			for _, ch := range edge.ChildElements() {
				xmlpart.Remove(edge, ch)
			}
			return
		}
		if ep.Style != nil {
			if *ep.Style == "" {
				edge.RemoveAttr("style")
			} else {
				edge.CreateAttr("style", *ep.Style)
			}
		}
		if ep.Color != nil {
			setColorChild(edge, "color", *ep.Color, nil)
		}
	}
	apply("top", p.Top)
	apply("right", p.Right)
	apply("bottom", p.Bottom)
	apply("left", p.Left)
}

// xfChildOrder is CT_Xf's child sequence.
var xfChildOrder = []string{"alignment", "protection", "extLst"}

func applyAlignmentPatch(xf *etree.Element, p *AlignmentPatch) {
	al := xmlpart.EnsureChildInOrder(xf, "alignment", xfChildOrder)
	setOrClear := func(attr string, v *string) {
		if v == nil {
			return
		}
		if *v == "" {
			al.RemoveAttr(attr)
		} else {
			al.CreateAttr(attr, *v)
		}
	}
	setOrClear("horizontal", p.Horizontal)
	setOrClear("vertical", p.Vertical)
	if p.WrapText != nil {
		if *p.WrapText {
			al.CreateAttr("wrapText", "1")
		} else {
			al.RemoveAttr("wrapText")
		}
	}
	// A fully attribute-less alignment element is noise; drop it.
	if len(al.Attr) == 0 && len(al.ChildElements()) == 0 {
		xmlpart.Remove(xf, al)
	}
}

// builtinNumFmtID maps "" and "General" to id 0 and a builtin code to its
// lowest builtin id (ascending for determinism); ok is false for a custom
// code.
func builtinNumFmtID(code string) (int, bool) {
	if code == "" || strings.EqualFold(code, "General") {
		return 0, true
	}
	for id := 1; id <= 49; id++ {
		if builtinNumFmtAll[id] == code {
			return id, true
		}
	}
	return 0, false
}

// numFmtIDFor resolves a format pattern to an id: exact builtin match (ids
// ascending for determinism), an existing custom entry with the same code,
// else a freshly allocated id >= 164 appended to <numFmts>.
func numFmtIDFor(root *etree.Element, code string) int {
	if id, ok := builtinNumFmtID(code); ok {
		return id
	}
	numFmts := xmlpart.FindChild(root, "numFmts")
	maxID := 163
	if numFmts != nil {
		for _, nf := range xmlpart.Children(numFmts, "numFmt") {
			id := attrInt(nf, "numFmtId")
			if nf.SelectAttrValue("formatCode", "") == code {
				return id
			}
			if id > maxID {
				maxID = id
			}
		}
	} else {
		numFmts = xmlpart.EnsureChildInOrder(root, "numFmts", styleSheetOrder)
	}
	nf := etree.NewElement("numFmt")
	nf.CreateAttr("numFmtId", strconv.Itoa(maxID+1))
	nf.CreateAttr("formatCode", code)
	numFmts.AddChild(nf)
	numFmts.CreateAttr("count", strconv.Itoa(len(xmlpart.Children(numFmts, "numFmt"))))
	return maxID + 1
}

// xfForStyle builds the style records for a complete Style and returns the
// deduped xf index — the whole-style path (SetCellStyle/SetRowStyle).
func (f *File) xfForStyle(st Style) (int, error) {
	root, err := f.stylesRoot()
	if err != nil {
		return 0, err
	}
	cellXfs := xmlpart.EnsureChildInOrder(root, "cellXfs", styleSheetOrder)

	fonts := xmlpart.EnsureChildInOrder(root, "fonts", styleSheetOrder)
	fontID := dedupeAppend(fonts, "font", buildFontNode(st.Font))
	fills := xmlpart.EnsureChildInOrder(root, "fills", styleSheetOrder)
	fillID := dedupeAppend(fills, "fill", buildFillNode(st.Fill))
	borders := xmlpart.EnsureChildInOrder(root, "borders", styleSheetOrder)
	borderID := dedupeAppend(borders, "border", buildBorderNode(st.Border))

	numID := st.NumFmtID
	if st.NumFmt != "" {
		numID = numFmtIDFor(root, st.NumFmt)
	}

	xf := defaultXfNode()
	xf.CreateAttr("numFmtId", strconv.Itoa(numID))
	xf.CreateAttr("fontId", strconv.Itoa(fontID))
	xf.CreateAttr("fillId", strconv.Itoa(fillID))
	xf.CreateAttr("borderId", strconv.Itoa(borderID))
	if fontID != 0 {
		xf.CreateAttr("applyFont", "1")
	}
	if fillID != 0 {
		xf.CreateAttr("applyFill", "1")
	}
	if borderID != 0 {
		xf.CreateAttr("applyBorder", "1")
	}
	if numID != 0 {
		xf.CreateAttr("applyNumberFormat", "1")
	}
	if a := st.Alignment; a != (Alignment{}) {
		al := xf.CreateElement("alignment")
		if a.Horizontal != "" {
			al.CreateAttr("horizontal", a.Horizontal)
		}
		if a.Vertical != "" {
			al.CreateAttr("vertical", a.Vertical)
		}
		if a.WrapText {
			al.CreateAttr("wrapText", "1")
		}
		if a.Indent != 0 {
			al.CreateAttr("indent", strconv.Itoa(a.Indent))
		}
		if a.TextRotation != 0 {
			al.CreateAttr("textRotation", strconv.Itoa(a.TextRotation))
		}
		if a.ShrinkToFit {
			al.CreateAttr("shrinkToFit", "1")
		}
		xf.CreateAttr("applyAlignment", "1")
	}
	if pr := st.Protection; pr != nil {
		pe := xf.CreateElement("protection")
		if !pr.Locked {
			pe.CreateAttr("locked", "0")
		}
		if pr.Hidden {
			pe.CreateAttr("hidden", "1")
		}
		xf.CreateAttr("applyProtection", "1")
	}
	return dedupeAppend(cellXfs, "xf", xf), nil
}

// buildColorAttrs writes a Color onto a color element by scheme.
func buildColorAttrs(el *etree.Element, c Color) {
	switch {
	case c.RGB != "":
		el.CreateAttr("rgb", "FF"+strings.ToUpper(c.RGB))
	case c.Indexed != nil:
		el.CreateAttr("indexed", strconv.Itoa(*c.Indexed))
	case c.Theme != nil:
		el.CreateAttr("theme", strconv.Itoa(*c.Theme))
		if c.Tint != 0 {
			el.CreateAttr("tint", trimFloat(c.Tint))
		}
	case c.Auto:
		el.CreateAttr("auto", "1")
	}
}

// colorEmpty reports the zero Color.
func colorEmpty(c Color) bool {
	return c.RGB == "" && c.Indexed == nil && c.Theme == nil && !c.Auto
}

func buildFontNode(ft Font) *etree.Element {
	font := etree.NewElement("font")
	if ft.Bold {
		font.CreateElement("b")
	}
	if ft.Italic {
		font.CreateElement("i")
	}
	if ft.Strike {
		font.CreateElement("strike")
	}
	if ft.Underline != "" {
		u := font.CreateElement("u")
		u.CreateAttr("val", ft.Underline)
	}
	if ft.Size != 0 {
		sz := font.CreateElement("sz")
		sz.CreateAttr("val", trimFloat(ft.Size))
	}
	if !colorEmpty(ft.Color) {
		buildColorAttrs(font.CreateElement("color"), ft.Color)
	}
	if ft.Name != "" {
		name := font.CreateElement("name")
		name.CreateAttr("val", ft.Name)
	}
	return font
}

func buildFillNode(fl Fill) *etree.Element {
	fill := etree.NewElement("fill")
	pf := fill.CreateElement("patternFill")
	pattern := fl.Pattern
	if pattern == "" {
		pattern = "none"
	}
	pf.CreateAttr("patternType", pattern)
	if !colorEmpty(fl.Fg) {
		buildColorAttrs(pf.CreateElement("fgColor"), fl.Fg)
	}
	if !colorEmpty(fl.Bg) {
		buildColorAttrs(pf.CreateElement("bgColor"), fl.Bg)
	}
	return fill
}

func buildBorderNode(bd Border) *etree.Element {
	border := etree.NewElement("border")
	if bd.DiagonalUp {
		border.CreateAttr("diagonalUp", "1")
	}
	if bd.DiagonalDown {
		border.CreateAttr("diagonalDown", "1")
	}
	edge := func(name string, e Edge) {
		el := border.CreateElement(name)
		if e.Style != "" {
			el.CreateAttr("style", e.Style)
		}
		if !colorEmpty(e.Color) {
			buildColorAttrs(el.CreateElement("color"), e.Color)
		}
	}
	edge("left", bd.Left)
	edge("right", bd.Right)
	edge("top", bd.Top)
	edge("bottom", bd.Bottom)
	edge("diagonal", bd.Diagonal)
	return border
}
