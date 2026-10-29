package xlsx

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/beevik/etree"
	"github.com/nathanstitt/omnidoc/pkg/xlsx/internal/xmlpart"
)

// worksheetOrder is the CT_Worksheet child sequence (ECMA-376), for inserting
// elements a part omitted at the position every consumer accepts.
var worksheetOrder = []string{
	"sheetPr", "dimension", "sheetViews", "sheetFormatPr", "cols", "sheetData",
	"sheetCalcPr", "sheetProtection", "protectedRanges", "scenarios", "autoFilter",
	"sortState", "dataConsolidate", "customSheetViews", "mergeCells", "phoneticPr",
	"conditionalFormatting", "dataValidations", "hyperlinks", "printOptions",
	"pageMargins", "pageSetup", "headerFooter", "rowBreaks", "colBreaks",
	"customProperties", "cellWatches", "ignoredErrors", "smartTags", "drawing",
	"legacyDrawing", "legacyDrawingHF", "picture", "oleObjects", "controls",
	"webPublishItems", "tableParts", "extLst",
}

// SheetEdit is one worksheet's editor handle (obtained from File.Sheet).
// Rows and columns are 1-based.
type SheetEdit struct {
	file     *File
	partName string
	name     string
}

// Name returns the sheet's current name.
func (s *SheetEdit) Name() string { return s.name }

// doc returns the worksheet tree for reading.
func (s *SheetEdit) doc() (*etree.Element, error) {
	p, err := s.file.part(s.partName)
	if err != nil {
		return nil, err
	}
	return p.Root(), nil
}

// mut returns the worksheet tree for mutation (marks the part dirty).
func (s *SheetEdit) mut() (*etree.Element, error) {
	p, err := s.file.mutatePart(s.partName)
	if err != nil {
		return nil, err
	}
	return p.Root(), nil
}

// SetName renames the sheet (workbook-level attribute). Renaming to an
// existing name is refused.
func (s *SheetEdit) SetName(name string) error {
	if name == s.name {
		return nil
	}
	if _, err := s.file.sheetElement(name); err == nil {
		return fmt.Errorf("xlsx: sheet %q already exists", name)
	}
	el, err := s.file.sheetElement(s.name)
	if err != nil {
		return err
	}
	el.CreateAttr("name", name)
	s.file.dirty["xl/workbook.xml"] = true
	delete(s.file.sheets, s.name)
	s.name = name
	s.file.sheets[name] = s
	return nil
}

// Visibility reports the sheet's view state.
func (s *SheetEdit) Visibility() Visibility {
	el, err := s.file.sheetElement(s.name)
	if err != nil {
		return SheetVisible
	}
	switch el.SelectAttrValue("state", "") {
	case "hidden":
		return SheetHidden
	case "veryHidden":
		return SheetVeryHidden
	}
	return SheetVisible
}

// SetVisibility changes the sheet's view state; hiding the last visible sheet
// is refused.
func (s *SheetEdit) SetVisibility(v Visibility) error {
	el, err := s.file.sheetElement(s.name)
	if err != nil {
		return err
	}
	current := el.SelectAttrValue("state", "")
	visible := current == "" || current == "visible"
	if v != SheetVisible && visible && s.file.visibleCount() <= 1 {
		return ErrLastVisibleSheet
	}
	switch v {
	case SheetVisible:
		el.RemoveAttr("state")
	case SheetHidden:
		el.CreateAttr("state", "hidden")
	case SheetVeryHidden:
		el.CreateAttr("state", "veryHidden")
	}
	s.file.dirty["xl/workbook.xml"] = true
	return nil
}

// TabColorRGB reports the sheet tab color ("RRGGBB" or "").
func (s *SheetEdit) TabColorRGB() string {
	root, err := s.doc()
	if err != nil {
		return ""
	}
	if pr := xmlpart.FindChild(root, "sheetPr"); pr != nil {
		if tc := xmlpart.FindChild(pr, "tabColor"); tc != nil {
			return normalizeRGB(tc.SelectAttrValue("rgb", ""))
		}
	}
	return ""
}

// SetTabColor sets the sheet tab color as "RRGGBB"; "" removes it.
func (s *SheetEdit) SetTabColor(rgb string) {
	root, err := s.mut()
	if err != nil {
		return
	}
	if rgb == "" {
		if pr := xmlpart.FindChild(root, "sheetPr"); pr != nil {
			if tc := xmlpart.FindChild(pr, "tabColor"); tc != nil {
				xmlpart.Remove(pr, tc)
			}
			if len(pr.ChildElements()) == 0 && len(pr.Attr) == 0 {
				xmlpart.Remove(root, pr)
			}
		}
		return
	}
	pr := xmlpart.EnsureChildInOrder(root, "sheetPr", worksheetOrder)
	tc := xmlpart.FindChild(pr, "tabColor")
	if tc == nil {
		tc = etree.NewElement("tabColor")
		// tabColor precedes the other sheetPr children; prepend is safe for
		// the subset this editor writes.
		pr.InsertChildAt(0, tc)
	}
	tc.CreateAttr("rgb", "FF"+strings.ToUpper(rgb))
}

// Frozen reports the frozen-pane split (rows, cols).
func (s *SheetEdit) Frozen() (rows, cols int) {
	root, err := s.doc()
	if err != nil {
		return 0, 0
	}
	if views := xmlpart.FindChild(root, "sheetViews"); views != nil {
		if view := xmlpart.FindChild(views, "sheetView"); view != nil {
			if pane := xmlpart.FindChild(view, "pane"); pane != nil {
				state := pane.SelectAttrValue("state", "")
				if state == "frozen" || state == "frozenSplit" {
					x, _ := strconv.Atoi(pane.SelectAttrValue("xSplit", "0"))
					y, _ := strconv.Atoi(pane.SelectAttrValue("ySplit", "0"))
					return y, x
				}
			}
		}
	}
	return 0, 0
}

// SetFrozen freezes the top rows and left cols (0, 0 removes the pane).
func (s *SheetEdit) SetFrozen(rows, cols int) {
	root, err := s.mut()
	if err != nil {
		return
	}
	views := xmlpart.EnsureChildInOrder(root, "sheetViews", worksheetOrder)
	view := xmlpart.FindChild(views, "sheetView")
	if view == nil {
		view = etree.NewElement("sheetView")
		view.CreateAttr("workbookViewId", "0")
		views.AddChild(view)
	}
	if pane := xmlpart.FindChild(view, "pane"); pane != nil {
		xmlpart.Remove(view, pane)
	}
	if rows <= 0 && cols <= 0 {
		return
	}
	pane := etree.NewElement("pane")
	if cols > 0 {
		pane.CreateAttr("xSplit", strconv.Itoa(cols))
	}
	if rows > 0 {
		pane.CreateAttr("ySplit", strconv.Itoa(rows))
	}
	pane.CreateAttr("topLeftCell", CellRef(rows+1, cols+1))
	pane.CreateAttr("activePane", activePane(rows, cols))
	pane.CreateAttr("state", "frozen")
	// pane is sheetView's first child element in the schema.
	view.InsertChildAt(0, pane)
}

// ShowGridLines reports the first sheetView's showGridLines flag (true when
// the attribute or the view is absent — the OOXML default).
func (s *SheetEdit) ShowGridLines() bool {
	root, err := s.doc()
	if err != nil {
		return true
	}
	if views := xmlpart.FindChild(root, "sheetViews"); views != nil {
		if view := xmlpart.FindChild(views, "sheetView"); view != nil {
			return onOffDefault(view.SelectAttrValue("showGridLines", ""), true)
		}
	}
	return true
}

// SetShowGridLines shows or hides the sheet's gridlines on every sheetView.
// Hiding writes showGridLines="0" (creating the view when the sheet has
// none); showing removes the attribute, restoring the default. Every other
// sheetView attribute and child (pane, selection) is kept. A call that
// changes nothing leaves the part untouched.
func (s *SheetEdit) SetShowGridLines(show bool) {
	if s.gridLinesUniform(show) {
		return
	}
	root, err := s.mut()
	if err != nil {
		return
	}
	views := xmlpart.EnsureChildInOrder(root, "sheetViews", worksheetOrder)
	if len(xmlpart.Children(views, "sheetView")) == 0 {
		view := etree.NewElement("sheetView")
		view.CreateAttr("workbookViewId", "0")
		views.AddChild(view)
	}
	for _, view := range xmlpart.Children(views, "sheetView") {
		if show {
			view.RemoveAttr("showGridLines")
		} else {
			view.CreateAttr("showGridLines", "0")
		}
	}
}

// gridLinesUniform reports whether every sheetView already matches show.
func (s *SheetEdit) gridLinesUniform(show bool) bool {
	root, err := s.doc()
	if err != nil {
		return false
	}
	var list []*etree.Element
	if views := xmlpart.FindChild(root, "sheetViews"); views != nil {
		list = xmlpart.Children(views, "sheetView")
	}
	if len(list) == 0 {
		return show // no view reads as shown; hiding must create one
	}
	for _, view := range list {
		if onOffDefault(view.SelectAttrValue("showGridLines", ""), true) != show {
			return false
		}
	}
	return true
}

// activePane names the quadrant the cursor lives in after a freeze.
func activePane(rows, cols int) string {
	switch {
	case rows > 0 && cols > 0:
		return "bottomRight"
	case rows > 0:
		return "bottomLeft"
	default:
		return "topRight"
	}
}

// Merges returns the sheet's merged ranges (1-based).
func (s *SheetEdit) Merges() []Range {
	root, err := s.doc()
	if err != nil {
		return nil
	}
	mc := xmlpart.FindChild(root, "mergeCells")
	if mc == nil {
		return nil
	}
	var out []Range
	for _, m := range xmlpart.Children(mc, "mergeCell") {
		if r, err := ParseRange(m.SelectAttrValue("ref", "")); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// SetMerges replaces the sheet's merged ranges wholesale (the unmerge-all-
// then-merge shape an authoritative save uses). An empty set removes the
// element.
func (s *SheetEdit) SetMerges(ranges []Range) {
	root, err := s.mut()
	if err != nil {
		return
	}
	if mc := xmlpart.FindChild(root, "mergeCells"); mc != nil {
		xmlpart.Remove(root, mc)
	}
	if len(ranges) == 0 {
		return
	}
	mc := xmlpart.EnsureChildInOrder(root, "mergeCells", worksheetOrder)
	mc.CreateAttr("count", strconv.Itoa(len(ranges)))
	for _, r := range ranges {
		m := etree.NewElement("mergeCell")
		m.CreateAttr("ref", r.String())
		mc.AddChild(m)
	}
}

// Dimension reports the declared used range, when present.
func (s *SheetEdit) Dimension() (Range, bool) {
	root, err := s.doc()
	if err != nil {
		return Range{}, false
	}
	if dim := xmlpart.FindChild(root, "dimension"); dim != nil {
		if r, err := ParseRange(dim.SelectAttrValue("ref", "")); err == nil {
			return r, true
		}
	}
	return Range{}, false
}

// SetDimension declares the used range.
func (s *SheetEdit) SetDimension(r Range) {
	root, err := s.mut()
	if err != nil {
		return
	}
	dim := xmlpart.EnsureChildInOrder(root, "dimension", worksheetOrder)
	dim.CreateAttr("ref", r.String())
}

// RowHeight reports a row's explicit height in points.
func (s *SheetEdit) RowHeight(row int) (float64, bool) {
	el := s.findRow(row)
	if el == nil {
		return 0, false
	}
	ht := el.SelectAttrValue("ht", "")
	if ht == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(ht, 64)
	return f, err == nil
}

// SetRowHeight sets a row's explicit height in points.
func (s *SheetEdit) SetRowHeight(row int, pts float64) {
	el := s.ensureRow(row)
	if el == nil {
		return
	}
	el.CreateAttr("ht", trimFloat(pts))
	el.CreateAttr("customHeight", "1")
}

// ClearRowHeight removes a row's explicit height.
func (s *SheetEdit) ClearRowHeight(row int) {
	if el := s.findRow(row); el != nil {
		if _, err := s.mut(); err == nil {
			el.RemoveAttr("ht")
			el.RemoveAttr("customHeight")
		}
	}
}

// ColWidth reports a column's explicit width in character units.
func (s *SheetEdit) ColWidth(col int) (float64, bool) {
	root, err := s.doc()
	if err != nil {
		return 0, false
	}
	cols := xmlpart.FindChild(root, "cols")
	if cols == nil {
		return 0, false
	}
	for _, c := range xmlpart.Children(cols, "col") {
		minC, _ := strconv.Atoi(c.SelectAttrValue("min", "0"))
		maxC, _ := strconv.Atoi(c.SelectAttrValue("max", "0"))
		if col >= minC && col <= maxC {
			if w := c.SelectAttrValue("width", ""); w != "" {
				f, err := strconv.ParseFloat(w, 64)
				return f, err == nil
			}
		}
	}
	return 0, false
}

// SetColWidth sets one column's width in character units, splitting any
// existing <col> range that covers it so neighbors keep their widths.
func (s *SheetEdit) SetColWidth(col int, chars float64) {
	root, err := s.mut()
	if err != nil {
		return
	}
	cols := xmlpart.EnsureChildInOrder(root, "cols", worksheetOrder)
	s.removeColFromRanges(cols, col)
	el := etree.NewElement("col")
	el.CreateAttr("min", strconv.Itoa(col))
	el.CreateAttr("max", strconv.Itoa(col))
	el.CreateAttr("width", trimFloat(chars))
	el.CreateAttr("customWidth", "1")
	cols.AddChild(el)
}

// ClearColWidth removes a column's explicit width.
func (s *SheetEdit) ClearColWidth(col int) {
	root, err := s.mut()
	if err != nil {
		return
	}
	cols := xmlpart.FindChild(root, "cols")
	if cols == nil {
		return
	}
	s.removeColFromRanges(cols, col)
	if len(cols.ChildElements()) == 0 {
		xmlpart.Remove(root, cols)
	}
}

// removeColFromRanges excises col from every <col> range, splitting ranges
// that straddle it (the remainder keeps the range's attributes).
func (s *SheetEdit) removeColFromRanges(cols *etree.Element, col int) {
	for _, c := range xmlpart.Children(cols, "col") {
		minC, _ := strconv.Atoi(c.SelectAttrValue("min", "0"))
		maxC, _ := strconv.Atoi(c.SelectAttrValue("max", "0"))
		if col < minC || col > maxC {
			continue
		}
		switch {
		case minC == maxC:
			xmlpart.Remove(cols, c)
		case col == minC:
			c.CreateAttr("min", strconv.Itoa(col+1))
		case col == maxC:
			c.CreateAttr("max", strconv.Itoa(col-1))
		default:
			// Split: shrink this range to the left side, clone for the right.
			right := c.Copy()
			right.CreateAttr("min", strconv.Itoa(col+1))
			right.CreateAttr("max", strconv.Itoa(maxC))
			c.CreateAttr("max", strconv.Itoa(col-1))
			xmlpart.InsertBefore(cols, right, c)
			// Keep document order min-ascending: right belongs after c.
			xmlpart.Remove(cols, right)
			cols.AddChild(right)
		}
	}
}

// sheetData returns the sheetData element, creating it if a degenerate part
// lacks one.
func (s *SheetEdit) sheetData(mutate bool) *etree.Element {
	var root *etree.Element
	var err error
	if mutate {
		root, err = s.mut()
	} else {
		root, err = s.doc()
	}
	if err != nil {
		return nil
	}
	if !mutate {
		return xmlpart.FindChild(root, "sheetData")
	}
	return xmlpart.EnsureChildInOrder(root, "sheetData", worksheetOrder)
}

// findRow returns the row element for a 1-based row, or nil.
func (s *SheetEdit) findRow(row int) *etree.Element {
	sd := s.sheetData(false)
	if sd == nil {
		return nil
	}
	for _, r := range xmlpart.Children(sd, "row") {
		if n, _ := strconv.Atoi(r.SelectAttrValue("r", "0")); n == row {
			return r
		}
	}
	return nil
}

// ensureRow returns the row element for a 1-based row, inserting it in
// ascending order when absent (marks the sheet dirty).
func (s *SheetEdit) ensureRow(row int) *etree.Element {
	sd := s.sheetData(true)
	if sd == nil {
		return nil
	}
	var before *etree.Element
	for _, r := range xmlpart.Children(sd, "row") {
		n, _ := strconv.Atoi(r.SelectAttrValue("r", "0"))
		if n == row {
			return r
		}
		if n > row && before == nil {
			before = r
		}
	}
	el := etree.NewElement("row")
	el.CreateAttr("r", strconv.Itoa(row))
	if before != nil {
		xmlpart.InsertBefore(sd, el, before)
	} else {
		sd.AddChild(el)
	}
	return el
}

// findCell returns a row's cell element for a 1-based column, or nil.
func findCell(rowEl *etree.Element, row, col int) *etree.Element {
	want := CellRef(row, col)
	for _, c := range xmlpart.Children(rowEl, "c") {
		if c.SelectAttrValue("r", "") == want {
			return c
		}
	}
	return nil
}

// ensureCell returns the cell element, inserting it in column order.
func ensureCell(rowEl *etree.Element, row, col int) *etree.Element {
	if c := findCell(rowEl, row, col); c != nil {
		return c
	}
	var before *etree.Element
	for _, c := range xmlpart.Children(rowEl, "c") {
		_, cc := parseRef(c.SelectAttrValue("r", ""))
		if cc+1 > col && before == nil {
			before = c
		}
	}
	el := etree.NewElement("c")
	el.CreateAttr("r", CellRef(row, col))
	if before != nil {
		xmlpart.InsertBefore(rowEl, el, before)
	} else {
		rowEl.AddChild(el)
	}
	return el
}

// resetCell strips a cell's value content (v/f/is children and the type
// attribute), keeping its reference and style.
func resetCell(c *etree.Element) {
	for _, ch := range c.ChildElements() {
		switch ch.Tag {
		case "v", "f", "is":
			xmlpart.Remove(c, ch)
		}
	}
	c.RemoveAttr("t")
}

// cellFor is the shared mutation entry: ensure the cell exists and reset it.
func (s *SheetEdit) cellFor(row, col int) (*etree.Element, error) {
	if row < 1 || col < 1 {
		return nil, fmt.Errorf("%w: (%d, %d)", ErrBadRef, row, col)
	}
	rowEl := s.ensureRow(row)
	if rowEl == nil {
		return nil, fmt.Errorf("xlsx: sheet %s has no sheetData", s.name)
	}
	c := ensureCell(rowEl, row, col)
	resetCell(c)
	s.file.invalidateCalc()
	return c, nil
}

// SetString writes an inline string (sharedStrings stays untouched, keeping
// the preservation surface minimal).
func (s *SheetEdit) SetString(row, col int, v string) error {
	c, err := s.cellFor(row, col)
	if err != nil {
		return err
	}
	c.CreateAttr("t", "inlineStr")
	is := c.CreateElement("is")
	t := is.CreateElement("t")
	if strings.TrimSpace(v) != v {
		t.CreateAttr("xml:space", "preserve")
	}
	t.SetText(v)
	return nil
}

// SetNumber writes a numeric cell.
func (s *SheetEdit) SetNumber(row, col int, v float64) error {
	c, err := s.cellFor(row, col)
	if err != nil {
		return err
	}
	c.CreateElement("v").SetText(formatCellNumber(v))
	return nil
}

// SetBool writes a boolean cell.
func (s *SheetEdit) SetBool(row, col int, v bool) error {
	c, err := s.cellFor(row, col)
	if err != nil {
		return err
	}
	c.CreateAttr("t", "b")
	val := "0"
	if v {
		val = "1"
	}
	c.CreateElement("v").SetText(val)
	return nil
}

// SetDate writes a date cell: the serial under the workbook's date system,
// ensuring the cell's number format is a date/time code (builtin 14 for a
// midnight date, 22 for a date-time) unless it already has one.
func (s *SheetEdit) SetDate(row, col int, t time.Time) error {
	c, err := s.cellFor(row, col)
	if err != nil {
		return err
	}
	serial := timeToSerial(t, s.file.Date1904())
	c.CreateElement("v").SetText(trimFloat(serial))
	return s.ensureDateNumFmt(c, t)
}

// SetFormula writes a formula cell ATOMICALLY with its cached value — the
// pair every consumer needs together (a formula without a cached value
// renders empty in viewers that do not evaluate). cached's kind selects the
// cell type; KindEmpty leaves no cache.
func (s *SheetEdit) SetFormula(row, col int, src string, cached Value) error {
	c, err := s.cellFor(row, col)
	if err != nil {
		return err
	}
	c.CreateElement("f").SetText(strings.TrimPrefix(src, "="))
	switch cached.Kind {
	case KindNumber:
		c.CreateElement("v").SetText(formatCellNumber(cached.F))
	case KindString:
		c.CreateAttr("t", "str")
		c.CreateElement("v").SetText(cached.S)
	case KindBool:
		c.CreateAttr("t", "b")
		v := "0"
		if cached.B {
			v = "1"
		}
		c.CreateElement("v").SetText(v)
	case KindDate:
		c.CreateElement("v").SetText(trimFloat(cached.F))
	case KindError:
		c.CreateAttr("t", "e")
		c.CreateElement("v").SetText(cached.S)
	}
	return nil
}

// ClearCell removes a cell's value and formula, keeping its style (the shape
// of an authoritative save's delete pass).
func (s *SheetEdit) ClearCell(row, col int) {
	rowEl := s.findRow(row)
	if rowEl == nil {
		return
	}
	c := findCell(rowEl, row, col)
	if c == nil {
		return
	}
	if _, err := s.mut(); err != nil {
		return
	}
	resetCell(c)
	s.file.invalidateCalc()
}

// CellData is a typed read of one cell through the editor.
type CellData struct {
	Value   Value
	Formula string
	StyleID int
}

// Cell reads a cell's typed value, formula, and style index.
func (s *SheetEdit) Cell(row, col int) CellData {
	rowEl := s.findRow(row)
	if rowEl == nil {
		return CellData{}
	}
	c := findCell(rowEl, row, col)
	if c == nil {
		return CellData{}
	}
	return s.cellData(c)
}

// Cells iterates the sheet's populated cells in document order (row-major);
// returning false stops the walk.
func (s *SheetEdit) Cells(fn func(row, col int, c CellData) bool) {
	sd := s.sheetData(false)
	if sd == nil {
		return
	}
	for _, rowEl := range xmlpart.Children(sd, "row") {
		for _, c := range xmlpart.Children(rowEl, "c") {
			r0, c0 := parseRef(c.SelectAttrValue("r", ""))
			if r0 < 0 || c0 < 0 {
				continue
			}
			if !fn(r0+1, c0+1, s.cellData(c)) {
				return
			}
		}
	}
}

// cellData decodes a cell element into a CellData.
func (s *SheetEdit) cellData(c *etree.Element) CellData {
	var out CellData
	out.StyleID, _ = strconv.Atoi(c.SelectAttrValue("s", "0"))
	typ := c.SelectAttrValue("t", "")

	var raw string
	if v := xmlpart.FindChild(c, "v"); v != nil {
		raw = v.Text()
	}
	if f := xmlpart.FindChild(c, "f"); f != nil {
		out.Formula = f.Text()
	}
	switch typ {
	case "s":
		idx, err := strconv.Atoi(strings.TrimSpace(raw))
		if err == nil {
			out.Value = Value{Kind: KindString, S: s.file.sharedString(idx)}
		} else {
			out.Value = Value{Kind: KindString}
		}
	case "str":
		out.Value = Value{Kind: KindString, S: raw}
	case "inlineStr":
		if is := xmlpart.FindChild(c, "is"); is != nil {
			var sb strings.Builder
			collectText(is, &sb)
			out.Value = Value{Kind: KindString, S: sb.String()}
		}
	case "b":
		out.Value = Value{Kind: KindBool, B: strings.TrimSpace(raw) == "1"}
	case "e":
		out.Value = Value{Kind: KindError, S: raw}
	default:
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return out
		}
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			out.Value = Value{Kind: KindString, S: raw}
			return out
		}
		if isDateCode(s.numFmtCodeFor(out.StyleID)) {
			out.Value = Value{Kind: KindDate, F: f, T: serialToTime(f, s.file.Date1904())}
		} else {
			out.Value = Value{Kind: KindNumber, F: f}
		}
	}
	return out
}

// collectText concatenates the t descendants of an inline-string container.
func collectText(el *etree.Element, sb *strings.Builder) {
	for _, ch := range el.ChildElements() {
		if ch.Tag == "t" {
			sb.WriteString(ch.Text())
			continue
		}
		collectText(ch, sb)
	}
}

// numFmtCodeFor resolves an xf index to its format code through the styles
// part tree (reading; the tree may already carry this Save's edits).
func (s *SheetEdit) numFmtCodeFor(xf int) string {
	styles, err := s.file.part("xl/styles.xml")
	if err != nil {
		return ""
	}
	root := styles.Root()
	cellXfs := xmlpart.FindChild(root, "cellXfs")
	if cellXfs == nil {
		return ""
	}
	xfs := xmlpart.Children(cellXfs, "xf")
	if xf < 0 || xf >= len(xfs) {
		return ""
	}
	id, _ := strconv.Atoi(xfs[xf].SelectAttrValue("numFmtId", "0"))
	custom := map[int]string{}
	if numFmts := xmlpart.FindChild(root, "numFmts"); numFmts != nil {
		for _, nf := range xmlpart.Children(numFmts, "numFmt") {
			cid, _ := strconv.Atoi(nf.SelectAttrValue("numFmtId", "-1"))
			custom[cid] = nf.SelectAttrValue("formatCode", "")
		}
	}
	return resolveNumFmt(id, custom)
}

// ensureDateNumFmt gives a date cell a date number format unless its xf
// already renders dates: builtin 14 (m/d/yy) for a midnight date, 22
// (m/d/yy h:mm) otherwise. The cell's existing xf is cloned with the new
// numFmtId (deduping against an identical existing xf), so its other facets
// ride along untouched.
func (s *SheetEdit) ensureDateNumFmt(c *etree.Element, t time.Time) error {
	xf, _ := strconv.Atoi(c.SelectAttrValue("s", "0"))
	if isDateCode(s.numFmtCodeFor(xf)) {
		return nil
	}
	wantID := 14
	if t.Hour() != 0 || t.Minute() != 0 || t.Second() != 0 {
		wantID = 22
	}
	styles, err := s.file.mutatePart("xl/styles.xml")
	if err != nil {
		return err
	}
	cellXfs := xmlpart.FindChild(styles.Root(), "cellXfs")
	if cellXfs == nil {
		return fmt.Errorf("xlsx: styles part has no cellXfs")
	}
	xfs := xmlpart.Children(cellXfs, "xf")

	// Clone the cell's current xf with the date numFmtId.
	var clone *etree.Element
	if xf >= 0 && xf < len(xfs) {
		clone = xfs[xf].Copy()
	} else {
		clone = etree.NewElement("xf")
		clone.CreateAttr("fontId", "0")
		clone.CreateAttr("fillId", "0")
		clone.CreateAttr("borderId", "0")
	}
	clone.CreateAttr("numFmtId", strconv.Itoa(wantID))
	clone.CreateAttr("applyNumberFormat", "1")

	// Dedup: an existing xf with identical attributes and no children reuses.
	for i, existing := range xfs {
		if sameXfAttrs(existing, clone) && len(existing.ChildElements()) == len(clone.ChildElements()) {
			c.CreateAttr("s", strconv.Itoa(i))
			return nil
		}
	}
	cellXfs.AddChild(clone)
	cellXfs.CreateAttr("count", strconv.Itoa(len(xfs)+1))
	c.CreateAttr("s", strconv.Itoa(len(xfs)))
	return nil
}

// sameXfAttrs compares two xf elements' attribute sets.
func sameXfAttrs(a, b *etree.Element) bool {
	if len(a.Attr) != len(b.Attr) {
		return false
	}
	for _, at := range a.Attr {
		if b.SelectAttrValue(at.Key, "\x00") != at.Value {
			return false
		}
	}
	return true
}

// formatCellNumber renders a float the way spreadsheet producers do: plain
// decimal notation for sane magnitudes, shortest round-trip otherwise.
func formatCellNumber(v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if len(s) > 25 {
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	return s
}

// timeToSerial converts a time to an Excel serial under the date system.
func timeToSerial(t time.Time, date1904 bool) float64 {
	epoch := excelEpoch1900
	if date1904 {
		epoch = excelEpoch1904
	}
	return t.UTC().Sub(epoch).Hours() / 24
}
