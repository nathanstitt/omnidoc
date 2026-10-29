package xlsx

import (
	"encoding/xml"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/beevik/etree"
	"github.com/nathanstitt/omnidoc/pkg/xlsx/internal/xmlpart"
)

// PivotTable is one pivot table: its source data, placement, and field
// layout. The cache carries refreshOnLoad, so a consumer recomputes values on
// open — the definition, not computed results, is what round-trips.
type PivotTable struct {
	// Name is the pivot table's display name.
	Name string
	// SourceSheet/SourceRange locate the source data (the range's first row
	// is the field-name header row).
	SourceSheet string
	SourceRange string
	// TargetSheet/Location place the pivot (Location is the anchor range).
	TargetSheet string
	Location    string
	// CacheFields are the cache's source-column field names in order — the
	// names Rows, Cols, Filters, Values, and AxisSort use. A read fills them
	// from the cache's database fields; a calculated or grouping field
	// (databaseField="0") is not a source column and is left out. On write
	// each column is named by its header's display text (a date reads in ISO
	// form, "2024-12-06", like Cell.Text; repeats are numbered " ", " 2",
	// ...), unless CacheFields holds the producer's name for that column and
	// it still names the header: the same text, the text plus a repeat count,
	// or the same date or number in another format ("12/6/24"). So a re-add
	// keeps Google's names, a renamed header takes its new text, and extra or
	// missing entries (a resized source range) are harmless.
	CacheFields []string
	// Rows/Cols/Filters are the axis fields by source-column name.
	Rows, Cols, Filters []string
	// Values are the aggregated data fields.
	Values []PivotValueField
	// RowGrandTotals/ColGrandTotals toggle the grand-total rows/columns.
	RowGrandTotals, ColGrandTotals bool
	// StyleName is the pivot style ("PivotStyleLight16" when empty on write).
	StyleName string

	// ValuesAxis is the axis that holds the "Values" pseudo-field — the
	// <field x="-2"/> placeholder that marks where the data-field captions go:
	// "col", "row", or "" when the table has none. The placeholder is never a
	// member of Rows or Cols. On write the placeholder is emitted only for two
	// or more Values (Excel writes none for one); "" then places it last on
	// the column axis (Excel's default).
	ValuesAxis string
	// ValuesPosition is the placeholder's index on ValuesAxis: the number of
	// Rows (or Cols) fields before it. On write it is clamped to the axis.
	ValuesPosition int
	// AxisSort maps an axis field's source-column name to the sort order of
	// its items ("ascending" or "descending", from pivotField sortType).
	// Fields in manual (file) order are absent.
	AxisSort map[string]string
	// LocationInfo is the <location> band offsets. A read always sets it. On
	// write, nil keeps the default single-header layout (1, 2, 1). The
	// offsets describe the layout as read and the writer cannot recompute
	// them: after changing Rows, Cols, Filters, Values, or ValuesAxis, set
	// LocationInfo to nil.
	LocationInfo *PivotLocation

	// DataCaption is the "Values" caption ("Values" when empty on write).
	DataCaption string
	// RowHeaderCaption/ColHeaderCaption replace the "Row Labels"/"Column
	// Labels" headers, or "".
	RowHeaderCaption, ColHeaderCaption string
	// GrandTotalCaption replaces the "Grand Total" label, or "".
	GrandTotalCaption string
	// MissingCaption is the text shown in empty value cells ("" = blank).
	// HideMissing is showMissing="0": empty cells do not use MissingCaption.
	MissingCaption string
	HideMissing    bool
	// ErrorCaption is the text shown in error value cells when ShowError is
	// set.
	ErrorCaption string
	ShowError    bool
}

// PivotLocation is the band layout of a pivot's <location>: the offsets,
// from the top-left of the Location range, of the first header row, first
// data row, and first data column, plus the page-field (filter) area shape.
type PivotLocation struct {
	FirstHeaderRow, FirstDataRow, FirstDataCol int
	RowPageCount, ColPageCount                 int
}

// PivotValueField is one aggregated field.
type PivotValueField struct {
	// Field is the source-column name.
	Field string
	// Aggregation is the subtotal function ("sum", "count", "average", "max",
	// "min", "product", "countNums", "stdDev", "stdDevp", "var", "varp";
	// "sum" when empty).
	Aggregation string
	// DisplayName is the data field's shown name ("Sum of X" style; derived
	// when empty).
	DisplayName string
	// NumFmt is the value cells' number format code (resolved through the
	// styles part like Style.NumFmt), or "" for General. On read it comes from
	// the dataField's numFmtId, else the source field's pivotField numFmtId.
	NumFmt string
}

const (
	relPivotTable      = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/pivotTable"
	relPivotCacheDef   = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/pivotCacheDefinition"
	relPivotCacheRecs  = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/pivotCacheRecords"
	ctPivotTable       = "application/vnd.openxmlformats-officedocument.spreadsheetml.pivotTable+xml"
	ctPivotCacheDef    = "application/vnd.openxmlformats-officedocument.spreadsheetml.pivotCacheDefinition+xml"
	ctPivotCacheRecs   = "application/vnd.openxmlformats-officedocument.spreadsheetml.pivotCacheRecords+xml"
	defaultPivotStyle  = "PivotStyleLight16"
	spreadsheetMainXNS = `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"`
	relationshipsXNS   = `xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`
)

// valuesFieldIndex is the rowFields/colFields x of the "Values" placeholder.
const valuesFieldIndex = -2

// sheetRelTargets resolves EVERY relationship of relType on a sheet part.
func (f *File) sheetRelTargets(sheetPart, relType string) []string {
	data := f.rawPartCurrent(sheetRelsName(sheetPart))
	if data == nil {
		return nil
	}
	var doc struct {
		Rels []struct {
			Type   string `xml:"Type,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	var out []string
	for _, r := range doc.Rels {
		if r.Type == relType {
			out = append(out, path.Clean(path.Join(path.Dir(sheetPart), r.Target)))
		}
	}
	return out
}

// PivotTables reads every sheet's pivot definitions.
func (f *File) PivotTables() []PivotTable {
	var out []PivotTable
	for _, name := range f.SheetNames() {
		sh, err := f.Sheet(name)
		if err != nil {
			continue
		}
		for _, ptPart := range f.sheetRelTargets(sh.partName, relPivotTable) {
			if pt, ok := f.readPivotTable(ptPart, name); ok {
				out = append(out, pt)
			}
		}
	}
	return out
}

// readPivotTable decodes one pivotTable part plus its cache definition.
func (f *File) readPivotTable(partName, targetSheet string) (PivotTable, bool) {
	data := f.rawPartCurrent(partName)
	if data == nil {
		return PivotTable{}, false
	}
	var doc struct {
		Name              string `xml:"name,attr"`
		CacheID           int    `xml:"cacheId,attr"`
		RowGrandTotals    string `xml:"rowGrandTotals,attr"`
		ColGrandTotals    string `xml:"colGrandTotals,attr"`
		DataOnRows        string `xml:"dataOnRows,attr"`
		DataPosition      *int   `xml:"dataPosition,attr"`
		DataCaption       string `xml:"dataCaption,attr"`
		RowHeaderCaption  string `xml:"rowHeaderCaption,attr"`
		ColHeaderCaption  string `xml:"colHeaderCaption,attr"`
		GrandTotalCaption string `xml:"grandTotalCaption,attr"`
		MissingCaption    string `xml:"missingCaption,attr"`
		ShowMissing       string `xml:"showMissing,attr"`
		ErrorCaption      string `xml:"errorCaption,attr"`
		ShowError         string `xml:"showError,attr"`
		Location          struct {
			Ref            string `xml:"ref,attr"`
			FirstHeaderRow int    `xml:"firstHeaderRow,attr"`
			FirstDataRow   int    `xml:"firstDataRow,attr"`
			FirstDataCol   int    `xml:"firstDataCol,attr"`
			RowPageCount   int    `xml:"rowPageCount,attr"`
			ColPageCount   int    `xml:"colPageCount,attr"`
		} `xml:"location"`
		PivotFields struct {
			Field []struct {
				Axis     string `xml:"axis,attr"`
				NumFmtID *int   `xml:"numFmtId,attr"`
				SortType string `xml:"sortType,attr"`
			} `xml:"pivotField"`
		} `xml:"pivotFields"`
		RowFields struct {
			Field []struct {
				X int `xml:"x,attr"`
			} `xml:"field"`
		} `xml:"rowFields"`
		ColFields struct {
			Field []struct {
				X int `xml:"x,attr"`
			} `xml:"field"`
		} `xml:"colFields"`
		PageFields struct {
			Field []struct {
				Fld int `xml:"fld,attr"`
			} `xml:"pageField"`
		} `xml:"pageFields"`
		DataFields struct {
			Field []struct {
				Name     string `xml:"name,attr"`
				Fld      int    `xml:"fld,attr"`
				Subtotal string `xml:"subtotal,attr"`
				NumFmtID *int   `xml:"numFmtId,attr"`
			} `xml:"dataField"`
		} `xml:"dataFields"`
		Style struct {
			Name string `xml:"name,attr"`
		} `xml:"pivotTableStyleInfo"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return PivotTable{}, false
	}

	pt := PivotTable{
		Name:        doc.Name,
		TargetSheet: targetSheet,
		Location:    doc.Location.Ref,
		StyleName:   doc.Style.Name,
		// The OOXML default for the grand-total attributes is TRUE.
		RowGrandTotals: doc.RowGrandTotals == "" || onOff(doc.RowGrandTotals),
		ColGrandTotals: doc.ColGrandTotals == "" || onOff(doc.ColGrandTotals),

		DataCaption:       doc.DataCaption,
		RowHeaderCaption:  doc.RowHeaderCaption,
		ColHeaderCaption:  doc.ColHeaderCaption,
		GrandTotalCaption: doc.GrandTotalCaption,
		MissingCaption:    doc.MissingCaption,
		HideMissing:       !onOffDefault(doc.ShowMissing, true),
		ErrorCaption:      doc.ErrorCaption,
		ShowError:         onOff(doc.ShowError),
		LocationInfo: &PivotLocation{
			FirstHeaderRow: doc.Location.FirstHeaderRow,
			FirstDataRow:   doc.Location.FirstDataRow,
			FirstDataCol:   doc.Location.FirstDataCol,
			RowPageCount:   doc.Location.RowPageCount,
			ColPageCount:   doc.Location.ColPageCount,
		},
	}

	// The cache definition supplies the source range and the field names.
	fields := []string{}
	if cachePart, ok := f.cacheDefFor(partName, doc.CacheID); ok {
		if cd := f.rawPartCurrent(cachePart); cd != nil {
			var cache struct {
				Source struct {
					WS struct {
						Ref   string `xml:"ref,attr"`
						Sheet string `xml:"sheet,attr"`
					} `xml:"worksheetSource"`
				} `xml:"cacheSource"`
				Fields struct {
					Field []struct {
						Name          string `xml:"name,attr"`
						DatabaseField string `xml:"databaseField,attr"`
					} `xml:"cacheField"`
				} `xml:"cacheFields"`
			}
			if err := xml.Unmarshal(cd, &cache); err == nil {
				pt.SourceSheet = cache.Source.WS.Sheet
				pt.SourceRange = cache.Source.WS.Ref
				for _, cf := range cache.Fields.Field {
					fields = append(fields, cf.Name)
					// A calculated or grouping field (databaseField="0") is
					// not a source column, so it is not in CacheFields.
					if onOffDefault(cf.DatabaseField, true) {
						pt.CacheFields = append(pt.CacheFields, cf.Name)
					}
				}
			}
		}
	}
	fieldName := func(idx int) string {
		if idx >= 0 && idx < len(fields) {
			return fields[idx]
		}
		return ""
	}
	// x="-2" is the "Values" placeholder, not a cache field: it records
	// where the data-field captions go instead of joining the axis.
	for _, rf := range doc.RowFields.Field {
		if rf.X == valuesFieldIndex {
			pt.ValuesAxis, pt.ValuesPosition = "row", len(pt.Rows)
			continue
		}
		pt.Rows = append(pt.Rows, fieldName(rf.X))
	}
	for _, cf := range doc.ColFields.Field {
		if cf.X == valuesFieldIndex {
			pt.ValuesAxis, pt.ValuesPosition = "col", len(pt.Cols)
			continue
		}
		pt.Cols = append(pt.Cols, fieldName(cf.X))
	}
	if pt.ValuesAxis == "" && onOff(doc.DataOnRows) && len(doc.DataFields.Field) > 1 {
		// No placeholder element, but the table says the values are on rows.
		// (Excel keeps dataOnRows after a table drops to one value, where
		// there is no Values field to place.)
		pt.ValuesAxis, pt.ValuesPosition = "row", len(pt.Rows)
		if doc.DataPosition != nil && *doc.DataPosition >= 0 && *doc.DataPosition < len(pt.Rows) {
			pt.ValuesPosition = *doc.DataPosition
		}
	}
	for _, pf := range doc.PageFields.Field {
		pt.Filters = append(pt.Filters, fieldName(pf.Fld))
	}
	for i, pf := range doc.PivotFields.Field {
		if pf.Axis == "" {
			continue
		}
		if pf.SortType == "ascending" || pf.SortType == "descending" {
			if pt.AxisSort == nil {
				pt.AxisSort = map[string]string{}
			}
			pt.AxisSort[fieldName(i)] = pf.SortType
		}
	}
	custom := f.resolvedStyles().customFmt
	for _, df := range doc.DataFields.Field {
		agg := df.Subtotal
		if agg == "" {
			agg = "sum"
		}
		v := PivotValueField{Field: fieldName(df.Fld), Aggregation: agg, DisplayName: df.Name}
		fmtID := df.NumFmtID
		if fmtID == nil && df.Fld >= 0 && df.Fld < len(doc.PivotFields.Field) {
			fmtID = doc.PivotFields.Field[df.Fld].NumFmtID
		}
		if fmtID != nil {
			v.NumFmt = resolveNumFmt(*fmtID, custom)
		}
		pt.Values = append(pt.Values, v)
	}
	return pt, true
}

// cacheDefFor resolves a pivot table's cache definition part: through the
// pivot part's own rels, falling back to the workbook pivotCaches by cacheId.
func (f *File) cacheDefFor(ptPart string, cacheID int) (string, bool) {
	if targets := f.sheetRelTargets(ptPart, relPivotCacheDef); len(targets) > 0 {
		return targets[0], true
	}
	wb, err := f.part("xl/workbook.xml")
	if err != nil {
		return "", false
	}
	pcs := xmlpart.FindChild(wb.Root(), "pivotCaches")
	if pcs == nil {
		return "", false
	}
	for _, pc := range xmlpart.Children(pcs, "pivotCache") {
		if id, _ := strconv.Atoi(pc.SelectAttrValue("cacheId", "-1")); id == cacheID {
			rid := pc.SelectAttrValue("id", "")
			rels, err := f.part("xl/_rels/workbook.xml.rels")
			if err != nil {
				return "", false
			}
			for _, ch := range xmlpart.Children(rels.Root(), "Relationship") {
				if ch.SelectAttrValue("Id", "") == rid {
					return path.Clean(path.Join("xl", ch.SelectAttrValue("Target", ""))), true
				}
			}
		}
	}
	return "", false
}

// RemovePivotTables deletes every pivot table, cache, and their wiring — the
// clean slate an authoritative save rebuilds onto (re-adding without removing
// would duplicate caches).
func (f *File) RemovePivotTables() error {
	ct, err := f.mutatePart("[Content_Types].xml")
	if err != nil {
		return err
	}
	removeOverride := func(part string) {
		for _, ch := range xmlpart.Children(ct.Root(), "Override") {
			if ch.SelectAttrValue("PartName", "") == "/"+part {
				xmlpart.Remove(ct.Root(), ch)
			}
		}
	}
	dropPart := func(name string) {
		f.deleted[name] = true
		delete(f.added, name)
		delete(f.parsed, name)
		delete(f.dirty, name)
		removeOverride(name)
	}

	for _, name := range f.SheetNames() {
		sh, err := f.Sheet(name)
		if err != nil {
			continue
		}
		ptParts := f.sheetRelTargets(sh.partName, relPivotTable)
		if len(ptParts) == 0 {
			continue
		}
		for _, ptPart := range ptParts {
			for _, cache := range f.sheetRelTargets(ptPart, relPivotCacheDef) {
				for _, recs := range f.sheetRelTargets(cache, relPivotCacheRecs) {
					dropPart(recs)
				}
				dropPart(path.Clean(path.Join(path.Dir(cache), "_rels", path.Base(cache)+".rels")))
				dropPart(cache)
			}
			dropPart(path.Clean(path.Join(path.Dir(ptPart), "_rels", path.Base(ptPart)+".rels")))
			dropPart(ptPart)
		}
		// Strip the sheet's pivot relationships.
		relsName := sheetRelsName(sh.partName)
		if rels, err := f.mutatePart(relsName); err == nil {
			for _, ch := range xmlpart.Children(rels.Root(), "Relationship") {
				if ch.SelectAttrValue("Type", "") == relPivotTable {
					xmlpart.Remove(rels.Root(), ch)
				}
			}
		}
	}

	// Workbook: drop pivotCaches and their rels.
	wb, err := f.mutatePart("xl/workbook.xml")
	if err != nil {
		return err
	}
	if pcs := xmlpart.FindChild(wb.Root(), "pivotCaches"); pcs != nil {
		rels, err := f.mutatePart("xl/_rels/workbook.xml.rels")
		if err != nil {
			return err
		}
		for _, pc := range xmlpart.Children(pcs, "pivotCache") {
			rid := pc.SelectAttrValue("id", "")
			for _, ch := range xmlpart.Children(rels.Root(), "Relationship") {
				if ch.SelectAttrValue("Id", "") == rid {
					target := path.Clean(path.Join("xl", ch.SelectAttrValue("Target", "")))
					dropPart(target)
					dropPart(path.Clean(path.Join(path.Dir(target), "_rels", path.Base(target)+".rels")))
					xmlpart.Remove(rels.Root(), ch)
				}
			}
		}
		xmlpart.Remove(wb.Root(), pcs)
	}
	return nil
}

// AddPivotTable creates a pivot table: the cache definition (field names read
// from the source range's header row, refreshOnLoad so consumers recompute),
// empty cache records, the table definition, and all the wiring.
func (f *File) AddPivotTable(pt PivotTable) error {
	src, err := f.Sheet(pt.SourceSheet)
	if err != nil {
		return err
	}
	srcRange, err := ParseRange(pt.SourceRange)
	if err != nil {
		return err
	}
	if _, err := f.Sheet(pt.TargetSheet); err != nil {
		return err
	}
	if pt.Location == "" {
		return fmt.Errorf("xlsx: pivot table %q needs a target location", pt.Name)
	}
	// Every part the wiring mutates must exist before the first change, so a
	// failure leaves the file as it was.
	for _, part := range []string{"xl/workbook.xml", "xl/_rels/workbook.xml.rels", "[Content_Types].xml"} {
		if _, err := f.part(part); err != nil {
			return err
		}
	}

	// Field names from the header row: each cell's display text (a date
	// header uses the reader's Cell.Text rule). A producer's name from
	// CacheFields is kept while it still names its header (see
	// cacheNameMatches), so a re-add keeps "12/6/24" but a renamed header
	// takes its new text. Cache field names must be unique, so, left to
	// right, a repeat gets the lowest count from 2 that makes it unique
	// appended — " ", " 2", " 3", as Excel and Google Sheets number repeats.
	var fields []string
	fieldIdx := map[string]int{}
	for col := srcRange.StartCol; col <= srcRange.EndCol; col++ {
		i := col - srcRange.StartCol
		text := src.headerText(srcRange.StartRow, col)
		name := text
		if name == "" {
			name = "Field" + strconv.Itoa(i+1)
		}
		if i < len(pt.CacheFields) && src.cacheNameMatches(pt.CacheFields[i], text, srcRange.StartRow, col) {
			name = pt.CacheFields[i]
		}
		base := name
		for k := 2; ; k++ {
			if _, taken := fieldIdx[name]; !taken {
				break
			}
			name = base + strconv.Itoa(k)
		}
		fieldIdx[name] = len(fields)
		fields = append(fields, name)
	}
	resolve := func(names []string, what string) ([]int, error) {
		var out []int
		for _, n := range names {
			idx, ok := fieldIdx[n]
			if !ok {
				return nil, fmt.Errorf("xlsx: pivot %s field %q is not a source column", what, n)
			}
			out = append(out, idx)
		}
		return out, nil
	}
	rowIdx, err := resolve(pt.Rows, "row")
	if err != nil {
		return err
	}
	colIdx, err := resolve(pt.Cols, "column")
	if err != nil {
		return err
	}
	pageIdx, err := resolve(pt.Filters, "filter")
	if err != nil {
		return err
	}
	switch pt.ValuesAxis {
	case "", "row", "col":
	default:
		return fmt.Errorf("xlsx: pivot ValuesAxis %q is not \"row\" or \"col\"", pt.ValuesAxis)
	}
	sortOf := map[int]string{}
	onAxis := map[string]bool{}
	for _, names := range [][]string{pt.Rows, pt.Cols, pt.Filters} {
		for _, n := range names {
			onAxis[n] = true
		}
	}
	for name, order := range pt.AxisSort {
		if order != "ascending" && order != "descending" {
			return fmt.Errorf("xlsx: pivot sort %q for field %q is not \"ascending\" or \"descending\"", order, name)
		}
		if !onAxis[name] {
			return fmt.Errorf("xlsx: pivot sort field %q is not an axis field", name)
		}
		sortOf[fieldIdx[name]] = order
	}
	type dataField struct {
		idx     int
		agg     string
		display string
		fmtID   int
	}
	var dataFields []dataField
	for _, v := range pt.Values {
		idx, ok := fieldIdx[v.Field]
		if !ok {
			return fmt.Errorf("xlsx: pivot value field %q is not a source column", v.Field)
		}
		agg := v.Aggregation
		if agg == "" {
			agg = "sum"
		}
		display := v.DisplayName
		if display == "" {
			display = strings.ToUpper(agg[:1]) + agg[1:] + " of " + v.Field
		}
		dataFields = append(dataFields, dataField{idx: idx, agg: agg, display: display})
	}
	// Number formats resolve only after every check passed, so a refused
	// table leaves the styles part untouched. General and builtin codes need
	// no styles part at all.
	for i, v := range pt.Values {
		if id, ok := builtinNumFmtID(v.NumFmt); ok {
			dataFields[i].fmtID = id
			continue
		}
		root, err := f.stylesRoot()
		if err != nil {
			return err
		}
		dataFields[i].fmtID = numFmtIDFor(root, v.NumFmt)
	}

	// Allocate part names and ids.
	n := 1
	for f.partExists(fmt.Sprintf("xl/pivotCache/pivotCacheDefinition%d.xml", n)) {
		n++
	}
	cachePart := fmt.Sprintf("xl/pivotCache/pivotCacheDefinition%d.xml", n)
	recsPart := fmt.Sprintf("xl/pivotCache/pivotCacheRecords%d.xml", n)
	tn := 1
	for f.partExists(fmt.Sprintf("xl/pivotTables/pivotTable%d.xml", tn)) {
		tn++
	}
	tablePart := fmt.Sprintf("xl/pivotTables/pivotTable%d.xml", tn)

	// Cache definition + empty records.
	var cd strings.Builder
	cd.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	cd.WriteString(`<pivotCacheDefinition ` + spreadsheetMainXNS + ` ` + relationshipsXNS +
		` r:id="rId1" refreshOnLoad="1" recordCount="0">`)
	cd.WriteString(`<cacheSource type="worksheet"><worksheetSource ref="` + xmlEscape(pt.SourceRange) +
		`" sheet="` + xmlEscape(pt.SourceSheet) + `"/></cacheSource>`)
	cd.WriteString(`<cacheFields count="` + strconv.Itoa(len(fields)) + `">`)
	for _, name := range fields {
		cd.WriteString(`<cacheField name="` + xmlEscape(name) + `" numFmtId="0"><sharedItems/></cacheField>`)
	}
	cd.WriteString(`</cacheFields></pivotCacheDefinition>` + "\n")
	if err := f.setPart(cachePart, []byte(cd.String())); err != nil {
		return err
	}
	if err := f.setPart(recsPart, []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<pivotCacheRecords `+spreadsheetMainXNS+` count="0"/>
`)); err != nil {
		return err
	}
	if err := f.setPart(path.Dir(cachePart)+"/_rels/"+path.Base(cachePart)+".rels", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="`+relPivotCacheRecs+`" Target="`+path.Base(recsPart)+`"/>
</Relationships>
`)); err != nil {
		return err
	}

	// Workbook pivotCaches entry + rel.
	cacheID, err := f.registerPivotCache(cachePart)
	if err != nil {
		return err
	}

	// The pivot table definition.
	style := pt.StyleName
	if style == "" {
		style = defaultPivotStyle
	}
	gt := func(on bool) string {
		if on {
			return "1"
		}
		return "0"
	}
	axis := map[int]string{}
	for _, i := range rowIdx {
		axis[i] = "axisRow"
	}
	for _, i := range colIdx {
		axis[i] = "axisCol"
	}
	for _, i := range pageIdx {
		axis[i] = "axisPage"
	}
	inData := map[int]bool{}
	for _, df := range dataFields {
		inData[df.idx] = true
	}
	var tb strings.Builder
	tb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	dataCaption := pt.DataCaption
	if dataCaption == "" {
		dataCaption = "Values"
	}
	// The "Values" placeholder exists only with two or more value fields
	// (Excel writes none for one): where asked, else last on the column axis
	// (Excel's default).
	valuesAxis, valuesPos := pt.ValuesAxis, pt.ValuesPosition
	switch {
	case len(dataFields) < 2:
		valuesAxis = ""
	case valuesAxis == "":
		valuesAxis, valuesPos = "col", len(colIdx)
	}
	axisLen := len(colIdx)
	if valuesAxis == "row" {
		axisLen = len(rowIdx)
	}
	valuesPos = max(0, min(valuesPos, axisLen))
	tb.WriteString(`<pivotTableDefinition ` + spreadsheetMainXNS + ` name="` + xmlEscape(pt.Name) +
		`" cacheId="` + strconv.Itoa(cacheID) + `" dataCaption="` + xmlEscape(dataCaption) + `"`)
	optAttr := func(name, v string) {
		if v != "" {
			tb.WriteString(` ` + name + `="` + xmlEscape(v) + `"`)
		}
	}
	optAttr("grandTotalCaption", pt.GrandTotalCaption)
	if pt.ShowError {
		tb.WriteString(` showError="1"`)
	}
	optAttr("errorCaption", pt.ErrorCaption)
	if pt.HideMissing {
		tb.WriteString(` showMissing="0"`)
	}
	optAttr("missingCaption", pt.MissingCaption)
	if valuesAxis == "row" {
		tb.WriteString(` dataOnRows="1"`)
	}
	if valuesAxis != "" && valuesPos < axisLen {
		tb.WriteString(` dataPosition="` + strconv.Itoa(valuesPos) + `"`)
	}
	optAttr("rowHeaderCaption", pt.RowHeaderCaption)
	optAttr("colHeaderCaption", pt.ColHeaderCaption)
	tb.WriteString(` rowGrandTotals="` + gt(pt.RowGrandTotals) + `" colGrandTotals="` + gt(pt.ColGrandTotals) + `">`)
	loc := PivotLocation{FirstHeaderRow: 1, FirstDataRow: 2, FirstDataCol: 1}
	if pt.LocationInfo != nil {
		loc = *pt.LocationInfo
	}
	tb.WriteString(`<location ref="` + xmlEscape(pt.Location) + `" firstHeaderRow="` + strconv.Itoa(loc.FirstHeaderRow) +
		`" firstDataRow="` + strconv.Itoa(loc.FirstDataRow) + `" firstDataCol="` + strconv.Itoa(loc.FirstDataCol) + `"`)
	if loc.RowPageCount > 0 {
		tb.WriteString(` rowPageCount="` + strconv.Itoa(loc.RowPageCount) + `"`)
	}
	if loc.ColPageCount > 0 {
		tb.WriteString(` colPageCount="` + strconv.Itoa(loc.ColPageCount) + `"`)
	}
	tb.WriteString(`/>`)
	tb.WriteString(`<pivotFields count="` + strconv.Itoa(len(fields)) + `">`)
	for i := range fields {
		tb.WriteString("<pivotField")
		if ax, ok := axis[i]; ok {
			tb.WriteString(` axis="` + ax + `"`)
		}
		if inData[i] {
			tb.WriteString(` dataField="1"`)
		}
		if order, ok := sortOf[i]; ok {
			tb.WriteString(` sortType="` + order + `"`)
		}
		tb.WriteString(` showAll="0"/>`)
	}
	tb.WriteString(`</pivotFields>`)
	// The "Values" placeholder splices into its axis at valuesPos.
	withValues := func(idxs []int, axis string) []int {
		if valuesAxis != axis {
			return idxs
		}
		out := append([]int{}, idxs[:valuesPos]...)
		out = append(out, valuesFieldIndex)
		return append(out, idxs[valuesPos:]...)
	}
	writeAxisFields := func(el, item string, idxs []int) {
		if len(idxs) == 0 {
			return
		}
		tb.WriteString(`<` + el + ` count="` + strconv.Itoa(len(idxs)) + `">`)
		for _, i := range idxs {
			tb.WriteString(`<` + item + ` x="` + strconv.Itoa(i) + `"/>`)
		}
		tb.WriteString(`</` + el + `>`)
	}
	writeAxisFields("rowFields", "field", withValues(rowIdx, "row"))
	writeAxisFields("colFields", "field", withValues(colIdx, "col"))
	if len(pageIdx) > 0 {
		tb.WriteString(`<pageFields count="` + strconv.Itoa(len(pageIdx)) + `">`)
		for _, i := range pageIdx {
			tb.WriteString(`<pageField fld="` + strconv.Itoa(i) + `" hier="-1"/>`)
		}
		tb.WriteString(`</pageFields>`)
	}
	if len(dataFields) > 0 {
		tb.WriteString(`<dataFields count="` + strconv.Itoa(len(dataFields)) + `">`)
		for _, df := range dataFields {
			tb.WriteString(`<dataField name="` + xmlEscape(df.display) + `" fld="` + strconv.Itoa(df.idx) + `"`)
			if df.agg != "sum" {
				tb.WriteString(` subtotal="` + xmlEscape(df.agg) + `"`)
			}
			tb.WriteString(` baseField="0" baseItem="0"`)
			if df.fmtID != 0 {
				tb.WriteString(` numFmtId="` + strconv.Itoa(df.fmtID) + `"`)
			}
			tb.WriteString(`/>`)
		}
		tb.WriteString(`</dataFields>`)
	}
	tb.WriteString(`<pivotTableStyleInfo name="` + xmlEscape(style) + `" showRowHeaders="1" showColHeaders="1" showLastColumn="1"/>`)
	tb.WriteString(`</pivotTableDefinition>` + "\n")
	if err := f.setPart(tablePart, []byte(tb.String())); err != nil {
		return err
	}
	relToCache, err := relPath(path.Dir(tablePart), cachePart)
	if err != nil {
		return err
	}
	if err := f.setPart(path.Dir(tablePart)+"/_rels/"+path.Base(tablePart)+".rels", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="`+relPivotCacheDef+`" Target="`+relToCache+`"/>
</Relationships>
`)); err != nil {
		return err
	}

	// Target sheet references the pivot part; content types register all three.
	target, err := f.Sheet(pt.TargetSheet)
	if err != nil {
		return err
	}
	if _, err := target.addSheetRel(relPivotTable, tablePart); err != nil {
		return err
	}
	ct, err := f.mutatePart("[Content_Types].xml")
	if err != nil {
		return err
	}
	addOverride := func(part, ctype string) {
		over := etree.NewElement("Override")
		over.CreateAttr("PartName", "/"+part)
		over.CreateAttr("ContentType", ctype)
		ct.Root().AddChild(over)
	}
	addOverride(cachePart, ctPivotCacheDef)
	addOverride(recsPart, ctPivotCacheRecs)
	addOverride(tablePart, ctPivotTable)
	return nil
}

// headerText is a pivot source header cell's display text: strings and
// errors as stored, booleans as TRUE/FALSE, numbers and dates through the
// cell's number format (the reader's Cell.Text rule).
func (s *SheetEdit) headerText(row, col int) string {
	cd := s.Cell(row, col)
	switch cd.Value.Kind {
	case KindEmpty:
		return ""
	case KindString, KindError:
		return cd.Value.S
	case KindBool:
		if cd.Value.B {
			return "TRUE"
		}
		return "FALSE"
	}
	raw := rawCell{value: strconv.FormatFloat(cd.Value.F, 'g', -1, 64), styleIdx: cd.StyleID}
	return displayValue(raw, nil, s.file.resolvedStyles(), s.file.Date1904())
}

// cacheNameLayouts are the date forms a producer names a date header by
// (Google Sheets and Excel write the header's display text).
var cacheNameLayouts = []string{
	"1/2/06", "01/02/06", "1/2/2006", "01/02/2006", "2006-01-02", "2-Jan-06", "02-Jan-06",
	"2-Jan-2006", "Jan 2, 2006", "January 2, 2006", "2006/01/02", "2.1.2006", "02.01.2006",
}

// cacheNameMatches reports whether a producer's cache field name still names
// the header cell whose display text is text: the same text; the text with
// a repeat count (" 2" for the second " " header); any name for a blank
// header; or, for a numeric or date header, the same number or date in the
// producer's own format ("12/6/24" for a header read as "2024-12-06").
func (s *SheetEdit) cacheNameMatches(name, text string, row, col int) bool {
	if name == text {
		return true
	}
	if text == "" {
		return true
	}
	if rest, ok := strings.CutPrefix(name, text); ok && rest != "" {
		if n, err := strconv.Atoi(rest); err == nil && n >= 2 && strconv.Itoa(n) == rest {
			return true
		}
	}
	cd := s.Cell(row, col)
	switch cd.Value.Kind {
	case KindNumber:
		f, err := strconv.ParseFloat(strings.ReplaceAll(name, ",", ""), 64)
		return err == nil && f == cd.Value.F
	case KindDate:
		y, m, d := cd.Value.T.Date()
		for _, layout := range cacheNameLayouts {
			if t, err := time.Parse(layout, name); err == nil {
				ty, tm, td := t.Date()
				if ty == y && tm == m && td == d {
					return true
				}
			}
		}
	}
	return false
}

// registerPivotCache adds a workbook pivotCaches entry (allocating the next
// cacheId + workbook rel) for a cache definition part.
func (f *File) registerPivotCache(cachePart string) (int, error) {
	wb, err := f.mutatePart("xl/workbook.xml")
	if err != nil {
		return 0, err
	}
	rels, err := f.mutatePart("xl/_rels/workbook.xml.rels")
	if err != nil {
		return 0, err
	}
	maxRel := 0
	for _, ch := range xmlpart.Children(rels.Root(), "Relationship") {
		id := ch.SelectAttrValue("Id", "")
		if strings.HasPrefix(id, "rId") {
			if n, err := strconv.Atoi(id[3:]); err == nil && n > maxRel {
				maxRel = n
			}
		}
	}
	rid := fmt.Sprintf("rId%d", maxRel+1)
	rel := etree.NewElement("Relationship")
	rel.CreateAttr("Id", rid)
	rel.CreateAttr("Type", relPivotCacheDef)
	rel.CreateAttr("Target", strings.TrimPrefix(cachePart, "xl/"))
	rels.Root().AddChild(rel)

	// CT_Workbook order: pivotCaches follows definedNames/calcPr.
	pcs := xmlpart.EnsureChildInOrder(wb.Root(), "pivotCaches", workbookOrder)
	maxCache := 0
	for _, pc := range xmlpart.Children(pcs, "pivotCache") {
		if id, err := strconv.Atoi(pc.SelectAttrValue("cacheId", "0")); err == nil && id > maxCache {
			maxCache = id
		}
	}
	pc := etree.NewElement("pivotCache")
	pc.CreateAttr("cacheId", strconv.Itoa(maxCache+1))
	pc.CreateAttr(relIDAttr(wb.Root()), rid)
	pcs.AddChild(pc)
	return maxCache + 1, nil
}

// workbookOrder is the CT_Workbook child sequence.
var workbookOrder = []string{
	"fileVersion", "fileSharing", "workbookPr", "workbookProtection", "bookViews",
	"sheets", "functionGroups", "externalReferences", "definedNames", "calcPr",
	"oleSize", "customWorkbookViews", "pivotCaches", "smartTagPr", "smartTagTypes",
	"webPublishing", "fileRecoveryPr", "webPublishObjects", "extLst",
}

// addSheetRel appends a relationship to an internal part on the sheet's rels
// part (created when absent) and returns its id, allocated past the current
// maximum.
func (s *SheetEdit) addSheetRel(relType, target string) (string, error) {
	rels, err := s.sheetRels()
	if err != nil {
		return "", err
	}
	relTarget, err := relPath(path.Dir(s.partName), target)
	if err != nil {
		return "", err
	}
	id := nextRelID(rels)
	el := etree.NewElement("Relationship")
	el.CreateAttr("Id", id)
	el.CreateAttr("Type", relType)
	el.CreateAttr("Target", relTarget)
	rels.AddChild(el)
	return id, nil
}

// SetDefinedNames replaces the workbook's defined names wholesale.
func (f *File) SetDefinedNames(names []DefinedName) error {
	wb, err := f.mutatePart("xl/workbook.xml")
	if err != nil {
		return err
	}
	if dn := xmlpart.FindChild(wb.Root(), "definedNames"); dn != nil {
		xmlpart.Remove(wb.Root(), dn)
	}
	if len(names) == 0 {
		return nil
	}
	dn := xmlpart.EnsureChildInOrder(wb.Root(), "definedNames", workbookOrder)
	for _, n := range names {
		el := etree.NewElement("definedName")
		el.CreateAttr("name", n.Name)
		if n.LocalSheet != nil {
			el.CreateAttr("localSheetId", strconv.Itoa(*n.LocalSheet))
		}
		if n.Hidden {
			el.CreateAttr("hidden", "1")
		}
		el.SetText(n.RefersTo)
		dn.AddChild(el)
	}
	return nil
}

// DefinedNames reads the workbook's defined names (editor view).
func (f *File) DefinedNames() []DefinedName {
	wb, err := f.part("xl/workbook.xml")
	if err != nil {
		return nil
	}
	dn := xmlpart.FindChild(wb.Root(), "definedNames")
	if dn == nil {
		return nil
	}
	var out []DefinedName
	for _, el := range xmlpart.Children(dn, "definedName") {
		d := DefinedName{
			Name:     el.SelectAttrValue("name", ""),
			RefersTo: strings.TrimSpace(el.Text()),
			Hidden:   onOff(el.SelectAttrValue("hidden", "")),
		}
		if ls := el.SelectAttrValue("localSheetId", ""); ls != "" {
			if n, err := strconv.Atoi(ls); err == nil {
				d.LocalSheet = &n
			}
		}
		out = append(out, d)
	}
	return out
}
