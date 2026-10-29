package xlsx

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	genxlsx "github.com/nathanstitt/omnidoc/testdata/gen/xlsx"
)

// calcPivotTable carries what a Google Sheets export puts in a pivot: the
// "Values" placeholder on the column axis, a numFmtId on the pivotField (not
// the dataField) for one value and on the dataField for the other, item sort
// orders, non-default location offsets, and every caption.
const calcPivotTable = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<pivotTableDefinition xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" name="Comp" cacheId="1" dataCaption="Vals" rowHeaderCaption="Region!" colHeaderCaption="Prod" grandTotalCaption="All" missingCaption="-" showMissing="0" showError="1" errorCaption="err" rowGrandTotals="1" colGrandTotals="0">
<location ref="A3:E9" firstHeaderRow="1" firstDataRow="3" firstDataCol="1"/>
<pivotFields count="4">
<pivotField axis="axisRow" showAll="0" sortType="descending"/>
<pivotField axis="axisCol" showAll="0" sortType="ascending"/>
<pivotField dataField="1" numFmtId="164" showAll="0"/>
<pivotField dataField="1" showAll="0" sortType="ascending"/>
</pivotFields>
<rowFields count="1"><field x="0"/></rowFields>
<colFields count="2"><field x="1"/><field x="-2"/></colFields>
<dataFields count="2"><dataField name="Sum of Sales" fld="2" baseField="0" baseItem="0"/><dataField name="Avg Margin" fld="3" subtotal="average" numFmtId="10"/></dataFields>
</pivotTableDefinition>`

func calcPivotFixture() []byte {
	data := wsOpen + `<sheetData><row r="1">` +
		`<c r="A1" t="inlineStr"><is><t>Region</t></is></c><c r="B1" t="inlineStr"><is><t>Product</t></is></c>` +
		`<c r="C1" t="inlineStr"><is><t>Sales</t></is></c><c r="D1" t="inlineStr"><is><t>Margin</t></is></c>` +
		`</row><row r="2"><c r="A2" t="inlineStr"><is><t>East</t></is></c><c r="B2" t="inlineStr"><is><t>W</t></is></c><c r="C2"><v>10</v></c><c r="D2"><v>0.2</v></c></row></sheetData></worksheet>`
	cache := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<pivotCacheDefinition xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><cacheSource type="worksheet"><worksheetSource ref="A1:D2" sheet="Data"/></cacheSource><cacheFields count="4"><cacheField name="Region"/><cacheField name="Product"/><cacheField name="Sales"/><cacheField name="Margin"/></cacheFields></pivotCacheDefinition>`
	styles := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><numFmts count="1"><numFmt numFmtId="164" formatCode="&quot;$&quot;#,##0.00"/></numFmts><fonts count="1"><font><sz val="10"/><name val="Arial"/></font></fonts><cellXfs count="1"><xf fontId="0"/></cellXfs></styleSheet>`
	return genxlsx.New().
		AddSheet("Data", data).
		AddSheet("Pivot", wsOpen+`<sheetData/></worksheet>`).
		SetStyles(styles).
		AddPart("xl/worksheets/_rels/sheet2.xml.rels", "", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="`+relPivotTable+`" Target="../pivotTables/pivotTable1.xml"/></Relationships>`).
		AddPart("xl/pivotTables/pivotTable1.xml", ctPivotTable, calcPivotTable).
		AddPart("xl/pivotTables/_rels/pivotTable1.xml.rels", "", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="`+relPivotCacheDef+`" Target="../pivotCache/pivotCacheDefinition1.xml"/></Relationships>`).
		AddPart("xl/pivotCache/pivotCacheDefinition1.xml", ctPivotCacheDef, cache).
		Bytes()
}

// wantCalcPivot is the full read of calcPivotTable.
func wantCalcPivot() PivotTable {
	return PivotTable{
		Name: "Comp", SourceSheet: "Data", SourceRange: "A1:D2", TargetSheet: "Pivot", Location: "A3:E9",
		CacheFields: []string{"Region", "Product", "Sales", "Margin"},
		Rows:        []string{"Region"}, Cols: []string{"Product"},
		Values: []PivotValueField{
			{Field: "Sales", Aggregation: "sum", DisplayName: "Sum of Sales", NumFmt: `"$"#,##0.00`},
			{Field: "Margin", Aggregation: "average", DisplayName: "Avg Margin", NumFmt: "0.00%"},
		},
		RowGrandTotals: true,
		ValuesAxis:     "col", ValuesPosition: 1,
		AxisSort:          map[string]string{"Region": "descending", "Product": "ascending"},
		LocationInfo:      &PivotLocation{FirstHeaderRow: 1, FirstDataRow: 3, FirstDataCol: 1},
		DataCaption:       "Vals",
		RowHeaderCaption:  "Region!",
		ColHeaderCaption:  "Prod",
		GrandTotalCaption: "All",
		MissingCaption:    "-", HideMissing: true,
		ErrorCaption: "err", ShowError: true,
	}
}

// TestPivotCalcFactsRead pins the "Values" placeholder (not an empty-named
// axis field), the numFmt fallback to the pivotField, sort orders on axis
// fields only, the location offsets, and the captions.
func TestPivotCalcFactsRead(t *testing.T) {
	f, err := Edit(context.Background(), calcPivotFixture())
	if err != nil {
		t.Fatal(err)
	}
	pts := f.PivotTables()
	if len(pts) != 1 {
		t.Fatalf("pivots = %d", len(pts))
	}
	if want := wantCalcPivot(); !reflect.DeepEqual(pts[0], want) {
		t.Errorf("read:\n got %+v\nwant %+v", pts[0], want)
	}
}

// TestPivotCalcFactsRoundTrip is calc's save shape: remove every pivot, add
// the read definition back, and read the same definition again.
func TestPivotCalcFactsRoundTrip(t *testing.T) {
	f, err := Edit(context.Background(), calcPivotFixture())
	if err != nil {
		t.Fatal(err)
	}
	pt := f.PivotTables()[0]
	if err := f.RemovePivotTables(); err != nil {
		t.Fatal(err)
	}
	if err := f.AddPivotTable(pt); err != nil {
		t.Fatal(err)
	}
	out := mustSave(t, f)
	f2, err := Edit(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	pts := f2.PivotTables()
	if len(pts) != 1 {
		t.Fatalf("pivots = %d", len(pts))
	}
	got := pts[0]
	want := wantCalcPivot()
	want.StyleName = defaultPivotStyle // the writer names a style when the source has none
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, want)
	}
	// The custom code reuses the styles part's existing id; the builtin
	// percent code maps to builtin id 10.
	_, parts := zipParts(t, out)
	table := string(parts["xl/pivotTables/pivotTable1.xml"])
	for _, s := range []string{`fld="2" baseField="0" baseItem="0" numFmtId="164"`, `fld="3" subtotal="average" baseField="0" baseItem="0" numFmtId="10"`, `<field x="1"/><field x="-2"/>`} {
		if !strings.Contains(table, s) {
			t.Errorf("table lacks %s:\n%s", s, table)
		}
	}
	if strings.Count(string(parts["xl/styles.xml"]), "<numFmt ") != 1 {
		t.Errorf("numFmt duplicated: %s", parts["xl/styles.xml"])
	}
}

// TestPivotValuesPlaceholderWrite: two values with no ValuesAxis go last on
// the column axis (Excel's default); ValuesAxis "row" at position 0 writes
// dataOnRows and dataPosition and reads back; one value writes no
// placeholder, so the existing single-value output is unchanged.
func TestPivotValuesPlaceholderWrite(t *testing.T) {
	two := testPivot()
	two.Values = append(two.Values, PivotValueField{Field: "Sales", Aggregation: "max"})
	f := pivotSource(t)
	if err := f.AddPivotTable(two); err != nil {
		t.Fatal(err)
	}
	_, parts := zipParts(t, mustSave(t, f))
	if s := string(parts["xl/pivotTables/pivotTable1.xml"]); !strings.Contains(s, `<colFields count="2"><field x="1"/><field x="-2"/></colFields>`) {
		t.Errorf("default placeholder:\n%s", s)
	}

	rows := two
	rows.Rows, rows.Cols = []string{"Region", "Product"}, nil
	rows.ValuesAxis, rows.ValuesPosition = "row", 0
	f = pivotSource(t)
	if err := f.AddPivotTable(rows); err != nil {
		t.Fatal(err)
	}
	out := mustSave(t, f)
	_, parts = zipParts(t, out)
	s := string(parts["xl/pivotTables/pivotTable1.xml"])
	for _, want := range []string{`dataOnRows="1"`, `dataPosition="0"`, `<rowFields count="3"><field x="-2"/><field x="0"/><field x="1"/></rowFields>`} {
		if !strings.Contains(s, want) {
			t.Errorf("row placeholder lacks %s:\n%s", want, s)
		}
	}
	f2, _ := Edit(context.Background(), out)
	if got := f2.PivotTables()[0]; got.ValuesAxis != "row" || got.ValuesPosition != 0 || !reflect.DeepEqual(got.Rows, []string{"Region", "Product"}) {
		t.Errorf("row placeholder read = %q@%d rows %v", got.ValuesAxis, got.ValuesPosition, got.Rows)
	}

	f = pivotSource(t)
	if err := f.AddPivotTable(testPivot()); err != nil {
		t.Fatal(err)
	}
	_, parts = zipParts(t, mustSave(t, f))
	if s := parts["xl/pivotTables/pivotTable1.xml"]; bytes.Contains(s, []byte(`x="-2"`)) || bytes.Contains(s, []byte("dataPosition")) {
		t.Errorf("single value wrote a placeholder:\n%s", s)
	}
}

// TestPivotDataOnRowsWithoutPlaceholder reads a table that marks
// dataOnRows/dataPosition without a -2 field element.
func TestPivotDataOnRowsWithoutPlaceholder(t *testing.T) {
	table := strings.Replace(calcPivotTable, `rowGrandTotals="1"`, `dataOnRows="1" dataPosition="0" rowGrandTotals="1"`, 1)
	table = strings.Replace(table, `<field x="1"/><field x="-2"/>`, `<field x="1"/>`, 1)
	f, err := Edit(context.Background(), pivotFixtureWithTable(table))
	if err != nil {
		t.Fatal(err)
	}
	got := f.PivotTables()[0]
	if got.ValuesAxis != "row" || got.ValuesPosition != 0 || !reflect.DeepEqual(got.Cols, []string{"Product"}) {
		t.Errorf("ValuesAxis = %q@%d cols %v", got.ValuesAxis, got.ValuesPosition, got.Cols)
	}
}

// TestPivotCalcFactsErrors pins the validation of the new fields.
func TestPivotCalcFactsErrors(t *testing.T) {
	for name, mutate := range map[string]func(*PivotTable){
		"axis":          func(p *PivotTable) { p.ValuesAxis = "page" },
		"sort order":    func(p *PivotTable) { p.AxisSort = map[string]string{"Region": "up"} },
		"sort non-axis": func(p *PivotTable) { p.AxisSort = map[string]string{"Sales": "ascending"} },
	} {
		pt := testPivot()
		mutate(&pt)
		if err := pivotSource(t).AddPivotTable(pt); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// pivotFixtureWithTable swaps the pivot table definition of
// calcPivotFixture.
func pivotFixtureWithTable(table string) []byte {
	f, err := Edit(context.Background(), calcPivotFixture())
	if err != nil {
		panic(err)
	}
	if err := f.setPart("xl/pivotTables/pivotTable1.xml", []byte(table)); err != nil {
		panic(err)
	}
	out, err := f.Save(context.Background())
	if err != nil {
		panic(err)
	}
	return out
}

// TestPivotCacheFieldNames: without CacheFields, repeated header text gets
// the next free count appended (" ", " 2", " 3" — Excel's and Google's rule)
// and a date header is named by its display text; CacheFields from a read is
// written back verbatim, and a list that does not fit the source is refused.
func TestPivotCacheFieldNames(t *testing.T) {
	src := wsOpen + `<sheetData><row r="1">` +
		`<c r="A1" t="inlineStr"><is><t>K</t></is></c><c r="B1" t="inlineStr"><is><t> </t></is></c>` +
		`<c r="C1" t="inlineStr"><is><t> </t></is></c><c r="D1" t="inlineStr"><is><t> 2</t></is></c>` +
		`<c r="E1" s="1"><v>45632</v></c></row>` +
		`<row r="2"><c r="A2" t="inlineStr"><is><t>a</t></is></c><c r="E2"><v>1</v></c></row></sheetData></worksheet>`
	styles := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><numFmts count="1"><numFmt numFmtId="165" formatCode="m/d/yy"/></numFmts><fonts count="1"><font><sz val="10"/><name val="Arial"/></font></fonts><cellXfs count="2"><xf fontId="0"/><xf numFmtId="165" fontId="0"/></cellXfs></styleSheet>`
	data := genxlsx.New().AddSheet("S", src).AddSheet("P", wsOpen+`<sheetData/></worksheet>`).SetStyles(styles).Bytes()
	base := PivotTable{Name: "p", SourceSheet: "S", SourceRange: "A1:E2", TargetSheet: "P", Location: "A1",
		Rows: []string{"K"}, Values: []PivotValueField{{Field: "2024-12-06"}}}

	f, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.AddPivotTable(base); err != nil {
		t.Fatal(err)
	}
	f2, _ := Edit(context.Background(), mustSave(t, f))
	read := f2.PivotTables()[0]
	// Left to right: the second " " takes " 2", so the header that is
	// literally " 2" moves on to " 22".
	if want := []string{"K", " ", " 2", " 22", "2024-12-06"}; !reflect.DeepEqual(read.CacheFields, want) {
		t.Errorf("derived names = %q, want %q", read.CacheFields, want)
	}

	// A producer's own names survive remove-then-add while they still name
	// their header: Google's "12/6/24" for the date header, " 2" for the
	// second " ". A name that no longer matches (the header was renamed)
	// gives way to the header text, and a short list names only its columns.
	for _, tc := range []struct {
		name  string
		cache []string
		want  []string
	}{
		{"verbatim", []string{"K", " ", " 2", " 22", "12/6/24"}, []string{"K", " ", " 2", " 22", "12/6/24"}},
		{"renamed header", []string{"Key", " ", " 2", " 22", "11/6/24"}, []string{"K", " ", " 2", " 22", "2024-12-06"}},
		{"short", []string{"K", " "}, []string{"K", " ", " 2", " 22", "2024-12-06"}},
		{"duplicate", []string{"K", " ", " ", " 22", "12/6/24"}, []string{"K", " ", " 2", " 22", "12/6/24"}},
	} {
		pt := base
		pt.CacheFields = tc.cache
		pt.Values = []PivotValueField{{Field: tc.want[4]}}
		g, _ := Edit(context.Background(), data)
		if err := g.AddPivotTable(pt); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		h, _ := Edit(context.Background(), mustSave(t, g))
		if got := h.PivotTables()[0].CacheFields; !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: names = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestPivotCalculatedFieldReAdd: a calculated (or grouping) cache field,
// databaseField="0", is not a source column, so it is not in CacheFields and
// remove-then-add still works.
func TestPivotCalculatedFieldReAdd(t *testing.T) {
	f, err := Edit(context.Background(), calcPivotFixture())
	if err != nil {
		t.Fatal(err)
	}
	cache := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<pivotCacheDefinition xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><cacheSource type="worksheet"><worksheetSource ref="A1:D2" sheet="Data"/></cacheSource><cacheFields count="5"><cacheField name="Region"/><cacheField name="Product"/><cacheField name="Sales"/><cacheField name="Margin"/><cacheField name="Double" formula="Sales*2" databaseField="0"/></cacheFields></pivotCacheDefinition>`
	if err := f.setPart("xl/pivotCache/pivotCacheDefinition1.xml", []byte(cache)); err != nil {
		t.Fatal(err)
	}
	pt := f.PivotTables()[0]
	if want := []string{"Region", "Product", "Sales", "Margin"}; !reflect.DeepEqual(pt.CacheFields, want) {
		t.Errorf("CacheFields = %q, want %q", pt.CacheFields, want)
	}
	if err := f.RemovePivotTables(); err != nil {
		t.Fatal(err)
	}
	if err := f.AddPivotTable(pt); err != nil {
		t.Fatalf("re-add with a calculated cache field: %v", err)
	}
}
