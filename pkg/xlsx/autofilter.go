package xlsx

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/beevik/etree"
	"github.com/nathanstitt/omnidoc/pkg/xlsx/internal/xmlpart"
)

// filterDatabaseName is the hidden, sheet-local defined name Excel keeps in
// step with a sheet's autoFilter range.
const filterDatabaseName = "_xlnm._FilterDatabase"

// ErrFilterColumn reports a FilterColumn that sets more than one filter kind
// (the schema allows exactly one per column).
var ErrFilterColumn = errors.New("xlsx: a filter column sets more than one filter kind")

// AutoFilter is a sheet's <autoFilter>: the filtered range and the criteria
// on its columns.
type AutoFilter struct {
	// Ref is the filtered range in A1 form ("A1:BY35"). It never contains "$":
	// absolute anchors in the file are stripped on read and on write.
	Ref string
	// Columns are the per-column criteria in file order.
	Columns []FilterColumn
	// Extra holds the verbatim XML of the autoFilter children this package
	// does not model (sortState, extLst). SetAutoFilter re-emits them after
	// the columns, unchanged.
	Extra [][]byte
}

// FilterColumn is one <filterColumn>. At most one filter kind is set:
// Values/Blank/DateGroups (a value list), Custom, Top10, Dynamic, Color, or
// Icon. Raw carries the VERBATIM element for lossless passthrough: while the
// typed fields still match what Raw encodes, SetAutoFilter re-emits Raw and
// changes only colId (from ColID). Once a typed field is edited, the typed
// fields are written instead, so an edit is never lost (unmodeled parts of
// that column's Raw then drop).
type FilterColumn struct {
	// ColID is the zero-based column offset from the first column of Ref.
	ColID int
	// HiddenButton hides the column's filter button (hiddenButton="1", or
	// showButton="0" on read).
	HiddenButton bool
	// Values are the <filters> value list: a row shows when its cell's display
	// text is one of these.
	Values []string
	// Blank also shows blank cells (filters blank="1").
	Blank bool
	// DateGroups are the <filters> date-group items.
	DateGroups []DateGroupItem
	// Custom is the <customFilters> criterion, or nil.
	Custom *CustomFilters
	// Top10 is the <top10> criterion, or nil.
	Top10 *Top10Filter
	// Dynamic is the <dynamicFilter> criterion, or nil.
	Dynamic *DynamicFilter
	// Color is the <colorFilter> criterion, or nil.
	Color *ColorFilter
	// Icon is the <iconFilter> criterion, or nil.
	Icon *IconFilter
	// Raw is the verbatim <filterColumn> element XML.
	Raw []byte
}

// DateGroupItem is one <dateGroupItem>: a date (or date-time) prefix that
// matches every cell in that period. Grouping names the deepest field that
// applies ("year", "month", "day", "hour", "minute", "second").
type DateGroupItem struct {
	Year, Month, Day, Hour, Minute, Second int
	Grouping                               string
}

// grouping is the item's dateTimeGrouping: Grouping when set, else the
// deepest part that is set (a zero year groups by year).
func (g DateGroupItem) grouping() string {
	if g.Grouping != "" {
		return g.Grouping
	}
	for _, p := range []struct {
		name string
		v    int
	}{{"second", g.Second}, {"minute", g.Minute}, {"hour", g.Hour}, {"day", g.Day}, {"month", g.Month}} {
		if p.v != 0 {
			return p.name
		}
	}
	return "year"
}

// sameFilterCriteria reports whether two columns carry the same typed
// criteria (ColID and Raw aside).
func sameFilterCriteria(a, b FilterColumn) bool {
	a.ColID, a.Raw, b.ColID, b.Raw = 0, nil, 0, nil
	return reflect.DeepEqual(a, b)
}

// CustomFilters is a <customFilters> criterion: one or two comparisons,
// joined by AND when And is set, else by OR.
type CustomFilters struct {
	And     bool
	Filters []CustomFilter
}

// CustomFilter is one <customFilter> comparison. Operator is the OOXML
// operator ("equal", "lessThan", "lessThanOrEqual", "notEqual",
// "greaterThanOrEqual", "greaterThan"); it reads as "equal" when the file
// omits it. Val may carry "*" and "?" wildcards.
type CustomFilter struct {
	Operator string
	Val      string
}

// Top10Filter is a <top10> criterion: the top (or, with Bottom, the bottom)
// Val items, or Val percent with Percent.
type Top10Filter struct {
	Bottom  bool
	Percent bool
	Val     float64
	// FilterVal is the cut-off value the producer computed, or nil.
	FilterVal *float64
}

// DynamicFilter is a <dynamicFilter> criterion. Type is the OOXML dynamic
// filter type ("aboveAverage", "belowAverage", "today", "thisMonth",
// "Q1", ...). Val/MaxVal are the producer's computed bounds, or nil.
type DynamicFilter struct {
	Type        string
	Val, MaxVal *float64
}

// ColorFilter is a <colorFilter> criterion: cells whose fill (or, with
// FontColor, font) color matches Style's.
type ColorFilter struct {
	FontColor bool
	// Style is the resolved differential format the color comes from, or nil.
	Style *Style
}

// IconFilter is an <iconFilter> criterion: cells showing icon IconID of
// IconSet (from conditional formatting). A nil IconID matches cells with no
// icon.
type IconFilter struct {
	IconSet string
	IconID  *int
}

// parseAutoFilter reads a worksheet's <autoFilter> through the raw-fidelity
// tree (shared by the Workbook reader and the editor). dxfs resolves
// colorFilter dxfId indices. It returns nil when the sheet has no filter.
func parseAutoFilter(root *etree.Element, dxfs []*Style) *AutoFilter {
	afEl := xmlpart.FindChild(root, "autoFilter")
	if afEl == nil {
		return nil
	}
	af := &AutoFilter{Ref: stripAnchors(afEl.SelectAttrValue("ref", ""))}
	for _, ch := range afEl.ChildElements() {
		if ch.Tag != "filterColumn" {
			af.Extra = append(af.Extra, elementBytes(ch))
			continue
		}
		af.Columns = append(af.Columns, parseFilterColumn(ch, dxfs))
	}
	return af
}

// parseFilterColumn reads one <filterColumn>.
func parseFilterColumn(el *etree.Element, dxfs []*Style) FilterColumn {
	fc := FilterColumn{Raw: elementBytes(el)}
	fc.ColID, _ = strconv.Atoi(el.SelectAttrValue("colId", "0"))
	fc.HiddenButton = onOff(el.SelectAttrValue("hiddenButton", "")) ||
		!onOffDefault(el.SelectAttrValue("showButton", ""), true)
	if f := xmlpart.FindChild(el, "filters"); f != nil {
		fc.Blank = onOff(f.SelectAttrValue("blank", ""))
		for _, ch := range f.ChildElements() {
			switch ch.Tag {
			case "filter":
				fc.Values = append(fc.Values, ch.SelectAttrValue("val", ""))
			case "dateGroupItem":
				fc.DateGroups = append(fc.DateGroups, DateGroupItem{
					Year:     attrInt(ch, "year"),
					Month:    attrInt(ch, "month"),
					Day:      attrInt(ch, "day"),
					Hour:     attrInt(ch, "hour"),
					Minute:   attrInt(ch, "minute"),
					Second:   attrInt(ch, "second"),
					Grouping: ch.SelectAttrValue("dateTimeGrouping", ""),
				})
			}
		}
	}
	if c := xmlpart.FindChild(el, "customFilters"); c != nil {
		cf := &CustomFilters{And: onOff(c.SelectAttrValue("and", ""))}
		for _, ch := range xmlpart.Children(c, "customFilter") {
			cf.Filters = append(cf.Filters, CustomFilter{
				Operator: ch.SelectAttrValue("operator", "equal"),
				Val:      ch.SelectAttrValue("val", ""),
			})
		}
		fc.Custom = cf
	}
	if t := xmlpart.FindChild(el, "top10"); t != nil {
		fc.Top10 = &Top10Filter{
			Bottom:    !onOffDefault(t.SelectAttrValue("top", ""), true),
			Percent:   onOff(t.SelectAttrValue("percent", "")),
			Val:       attrFloat(t, "val"),
			FilterVal: attrFloatPtr(t, "filterVal"),
		}
	}
	if d := xmlpart.FindChild(el, "dynamicFilter"); d != nil {
		fc.Dynamic = &DynamicFilter{
			Type:   d.SelectAttrValue("type", ""),
			Val:    attrFloatPtr(d, "val"),
			MaxVal: attrFloatPtr(d, "maxVal"),
		}
	}
	if c := xmlpart.FindChild(el, "colorFilter"); c != nil {
		cf := &ColorFilter{FontColor: !onOffDefault(c.SelectAttrValue("cellColor", ""), true)}
		if id, err := strconv.Atoi(c.SelectAttrValue("dxfId", "")); err == nil && id >= 0 && id < len(dxfs) {
			cf.Style = dxfs[id]
		}
		fc.Color = cf
	}
	if ic := xmlpart.FindChild(el, "iconFilter"); ic != nil {
		fi := &IconFilter{IconSet: ic.SelectAttrValue("iconSet", "")}
		if v := ic.SelectAttrValue("iconId", ""); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				fi.IconID = &n
			}
		}
		fc.Icon = fi
	}
	return fc
}

// AutoFilter reads the sheet's autoFilter (editor view), or nil.
func (s *SheetEdit) AutoFilter() *AutoFilter {
	root, err := s.doc()
	if err != nil {
		return nil
	}
	return parseAutoFilter(root, s.file.resolvedStyles().dxfs)
}

// SetAutoFilter REPLACES the sheet's autoFilter. nil removes it, together
// with the sheet's hidden _xlnm._FilterDatabase defined name. A non-nil
// filter writes the element and sets that defined name to the absolute range
// (creating it when absent), as Excel does. Ref must be a valid A1 range
// ("$" anchors are accepted and stripped); each column's ColID must fall
// inside it, once.
//
// nil on a sheet with no autoFilter changes nothing — not even a
// _FilterDatabase name an Advanced Filter keeps. Removing a filter also
// clears sheetPr filterMode. Row visibility is not recomputed in either
// direction: rows the filter hid keep their hidden flag after it is removed
// (the file does not say which hidden rows the filter hid and which were
// hidden by hand).
func (s *SheetEdit) SetAutoFilter(af *AutoFilter) error {
	root, err := s.doc()
	if err != nil {
		return err
	}
	old := xmlpart.FindChild(root, "autoFilter")
	if af == nil {
		if old == nil {
			return nil
		}
		if root, err = s.mut(); err != nil {
			return err
		}
		xmlpart.Remove(root, xmlpart.FindChild(root, "autoFilter"))
		if pr := xmlpart.FindChild(root, "sheetPr"); pr != nil {
			pr.RemoveAttr("filterMode")
		}
		return s.setFilterDatabase(false, Range{})
	}

	rng, err := ParseRange(stripAnchors(af.Ref))
	if err != nil {
		return err
	}
	el, err := s.file.autoFilterElement(af, rng)
	if err != nil {
		return err
	}
	// A call that changes nothing leaves the sheet part byte-verbatim: calc
	// pushes every sheet's filter on every save.
	if sameAutoFilter(old, el) {
		return s.setFilterDatabase(true, rng)
	}
	if root, err = s.mut(); err != nil {
		return err
	}
	if old := xmlpart.FindChild(root, "autoFilter"); old != nil {
		xmlpart.Remove(root, old)
	}
	placeholder := xmlpart.EnsureChildInOrder(root, "autoFilter", worksheetOrder)
	xmlpart.InsertBefore(root, el, placeholder)
	xmlpart.Remove(root, placeholder)
	return s.setFilterDatabase(true, rng)
}

// sameAutoFilter reports whether the sheet's current <autoFilter> (nil when
// absent) already equals the one a write would emit (nil = none). The
// current ref is compared with its "$" anchors stripped, as written.
func sameAutoFilter(old, el *etree.Element) bool {
	if old == nil || el == nil {
		return old == el
	}
	norm := old.Copy()
	if ref := norm.SelectAttr("ref"); ref != nil {
		ref.Value = stripAnchors(ref.Value)
	}
	return xmlpart.Equal(norm, el)
}

// autoFilterElement builds the <autoFilter> element for af.
func (f *File) autoFilterElement(af *AutoFilter, rng Range) (*etree.Element, error) {
	el := etree.NewElement("autoFilter")
	el.CreateAttr("ref", rng.String())
	width := rng.EndCol - rng.StartCol + 1
	seen := map[int]bool{}
	for i, col := range af.Columns {
		if col.ColID < 0 || col.ColID >= width {
			return nil, fmt.Errorf("xlsx: filter column %d: ColID %d is outside the %d columns of %s", i, col.ColID, width, rng)
		}
		if seen[col.ColID] {
			return nil, fmt.Errorf("xlsx: filter column %d: ColID %d is already filtered", i, col.ColID)
		}
		seen[col.ColID] = true
		ch, err := f.filterColumnElement(col)
		if err != nil {
			return nil, fmt.Errorf("xlsx: filter column %d: %w", i, err)
		}
		el.AddChild(ch)
	}
	for _, raw := range af.Extra {
		p, err := xmlpart.Parse(raw)
		if err != nil {
			return nil, err
		}
		el.AddChild(p.Root().Copy())
	}
	return el, nil
}

// filterColumnElement builds one <filterColumn>: verbatim from Raw when
// present (colId restamped), else from the typed fields.
func (f *File) filterColumnElement(col FilterColumn) (*etree.Element, error) {
	// Raw re-emits verbatim only while the typed fields still say what it
	// says; a typed edit of a column read with Raw wins, so it is never lost.
	if len(col.Raw) > 0 {
		p, err := xmlpart.Parse(col.Raw)
		if err != nil {
			return nil, err
		}
		if sameFilterCriteria(parseFilterColumn(p.Root(), f.resolvedStyles().dxfs), col) {
			el := p.Root().Copy()
			el.CreateAttr("colId", strconv.Itoa(col.ColID))
			return el, nil
		}
	}
	kinds := 0
	if len(col.Values) > 0 || col.Blank || len(col.DateGroups) > 0 {
		kinds++
	}
	for _, set := range []bool{col.Custom != nil, col.Top10 != nil, col.Dynamic != nil, col.Color != nil, col.Icon != nil} {
		if set {
			kinds++
		}
	}
	if kinds > 1 {
		return nil, ErrFilterColumn
	}

	el := etree.NewElement("filterColumn")
	el.CreateAttr("colId", strconv.Itoa(col.ColID))
	if col.HiddenButton {
		el.CreateAttr("hiddenButton", "1")
	}
	switch {
	case len(col.Values) > 0 || col.Blank || len(col.DateGroups) > 0:
		fe := el.CreateElement("filters")
		if col.Blank {
			fe.CreateAttr("blank", "1")
		}
		for _, v := range col.Values {
			fe.CreateElement("filter").CreateAttr("val", v)
		}
		for _, g := range col.DateGroups {
			// year and dateTimeGrouping are required; the other parts are
			// written when set.
			ge := fe.CreateElement("dateGroupItem")
			ge.CreateAttr("year", strconv.Itoa(g.Year))
			for _, a := range []struct {
				name string
				v    int
			}{{"month", g.Month}, {"day", g.Day}, {"hour", g.Hour}, {"minute", g.Minute}, {"second", g.Second}} {
				if a.v != 0 {
					ge.CreateAttr(a.name, strconv.Itoa(a.v))
				}
			}
			ge.CreateAttr("dateTimeGrouping", g.grouping())
		}
	case col.Custom != nil:
		if n := len(col.Custom.Filters); n < 1 || n > 2 {
			return nil, fmt.Errorf("xlsx: customFilters holds %d comparisons; the schema allows 1 or 2", n)
		}
		ce := el.CreateElement("customFilters")
		if col.Custom.And {
			ce.CreateAttr("and", "1")
		}
		for _, c := range col.Custom.Filters {
			cfe := ce.CreateElement("customFilter")
			if c.Operator != "" && c.Operator != "equal" {
				cfe.CreateAttr("operator", c.Operator)
			}
			cfe.CreateAttr("val", c.Val)
		}
	case col.Top10 != nil:
		te := el.CreateElement("top10")
		if col.Top10.Bottom {
			te.CreateAttr("top", "0")
		}
		if col.Top10.Percent {
			te.CreateAttr("percent", "1")
		}
		te.CreateAttr("val", trimFloat(col.Top10.Val))
		if col.Top10.FilterVal != nil {
			te.CreateAttr("filterVal", trimFloat(*col.Top10.FilterVal))
		}
	case col.Dynamic != nil:
		de := el.CreateElement("dynamicFilter")
		de.CreateAttr("type", col.Dynamic.Type)
		if col.Dynamic.Val != nil {
			de.CreateAttr("val", trimFloat(*col.Dynamic.Val))
		}
		if col.Dynamic.MaxVal != nil {
			de.CreateAttr("maxVal", trimFloat(*col.Dynamic.MaxVal))
		}
	case col.Color != nil:
		ce := el.CreateElement("colorFilter")
		if col.Color.Style != nil {
			id, err := f.dxfIDFor(*col.Color.Style)
			if err != nil {
				return nil, err
			}
			ce.CreateAttr("dxfId", strconv.Itoa(id))
		}
		if col.Color.FontColor {
			ce.CreateAttr("cellColor", "0")
		}
	case col.Icon != nil:
		ie := el.CreateElement("iconFilter")
		ie.CreateAttr("iconSet", col.Icon.IconSet)
		if col.Icon.IconID != nil {
			ie.CreateAttr("iconId", strconv.Itoa(*col.Icon.IconID))
		}
	}
	return el, nil
}

// setFilterDatabase keeps the sheet's hidden _xlnm._FilterDatabase name in
// step with its autoFilter: set to the absolute range when on, removed when
// off. Only that one definedName element is touched, so every other name
// keeps its unmodeled attributes.
func (s *SheetEdit) setFilterDatabase(on bool, rng Range) error {
	idx, err := s.file.sheetIndex(s.name)
	if err != nil {
		return err
	}
	wb, err := s.file.part("xl/workbook.xml")
	if err != nil {
		return err
	}
	var existing *etree.Element
	if dn := xmlpart.FindChild(wb.Root(), "definedNames"); dn != nil {
		for _, el := range xmlpart.Children(dn, "definedName") {
			if el.SelectAttrValue("name", "") == filterDatabaseName &&
				el.SelectAttrValue("localSheetId", "") == strconv.Itoa(idx) {
				existing = el
				break
			}
		}
	}
	if !on && existing == nil {
		return nil
	}
	refersTo := ""
	if on {
		refersTo = quoteSheetName(s.name) + "!" + absoluteRange(rng)
		if existing != nil && strings.TrimSpace(existing.Text()) == refersTo {
			return nil
		}
	}
	wbm, err := s.file.mutatePart("xl/workbook.xml")
	if err != nil {
		return err
	}
	dn := xmlpart.EnsureChildInOrder(wbm.Root(), "definedNames", workbookOrder)
	if !on {
		xmlpart.Remove(dn, existing)
		if len(dn.ChildElements()) == 0 {
			xmlpart.Remove(wbm.Root(), dn)
		}
		return nil
	}
	if existing == nil {
		existing = dn.CreateElement("definedName")
		existing.CreateAttr("name", filterDatabaseName)
		existing.CreateAttr("localSheetId", strconv.Itoa(idx))
		existing.CreateAttr("hidden", "1")
	}
	existing.SetText(refersTo)
	return nil
}

// sheetIndex is a sheet's zero-based position in the workbook's <sheets>
// list (the index localSheetId refers to).
func (f *File) sheetIndex(name string) (int, error) {
	_, sheets, err := f.workbookSheets()
	if err != nil {
		return 0, err
	}
	for i, el := range sheets {
		if el.SelectAttrValue("name", "") == name {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrSheetNotFound, name)
}

// stripAnchors removes "$" absolute anchors from an A1 reference.
func stripAnchors(ref string) string { return strings.ReplaceAll(ref, "$", "") }

// absoluteRange renders a range with "$" anchors ("$A$1:$C$5").
func absoluteRange(r Range) string {
	abs := func(row, col int) string { return "$" + ColumnName(col) + "$" + strconv.Itoa(row) }
	return abs(r.StartRow, r.StartCol) + ":" + abs(r.EndRow, r.EndCol)
}

// quoteSheetName renders a sheet name for a formula reference: bare when it
// is a plain identifier that cannot be read as a cell reference, otherwise
// single-quoted with embedded quotes doubled.
func quoteSheetName(name string) string {
	plain := name != ""
	for i, r := range name {
		isLetter := r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
		isDigit := r >= '0' && r <= '9'
		if !isLetter && (i == 0 || (!isDigit && r != '.')) {
			plain = false
			break
		}
	}
	if plain {
		if row, _ := parseRef(strings.ToUpper(name)); row >= 0 {
			plain = false // "A1", "XFD9" read as cells
		} else if looksLikeR1C1(name) {
			plain = false
		}
	}
	if plain {
		return name
	}
	return "'" + strings.ReplaceAll(name, "'", "''") + "'"
}

// looksLikeR1C1 reports names Excel would read as an R1C1 reference ("R",
// "C", "R1", "C2", "R1C2").
func looksLikeR1C1(name string) bool {
	s := strings.ToUpper(name)
	digits := func(t string) bool {
		for _, r := range t {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	if rest, ok := strings.CutPrefix(s, "R"); ok {
		rowPart, colPart, hasC := strings.Cut(rest, "C")
		return digits(rowPart) && (!hasC || digits(colPart))
	}
	if rest, ok := strings.CutPrefix(s, "C"); ok {
		return digits(rest)
	}
	return false
}

// elementBytes serializes one element (and its subtree) verbatim.
func elementBytes(el *etree.Element) []byte {
	doc := etree.NewDocument()
	doc.SetRoot(el.Copy())
	raw, err := doc.WriteToBytes()
	if err != nil {
		return nil
	}
	return raw
}

// attrFloat reads a float attribute (0 when absent or malformed).
func attrFloat(el *etree.Element, name string) float64 {
	f, _ := strconv.ParseFloat(el.SelectAttrValue(name, ""), 64)
	return f
}

// attrFloatPtr reads an optional float attribute (nil when absent or
// malformed).
func attrFloatPtr(el *etree.Element, name string) *float64 {
	v := el.SelectAttrValue(name, "")
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil
	}
	return &f
}
