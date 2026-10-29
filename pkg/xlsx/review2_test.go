package xlsx

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	genxlsx "github.com/nathanstitt/omnidoc/testdata/gen/xlsx"
)

// TestSetNameKeepsFilterDatabase: a rename rewrites the sheet's own
// _FilterDatabase name, so it never points at a sheet that no longer exists
// (Excel repairs that), whatever the call order.
func TestSetNameKeepsFilterDatabase(t *testing.T) {
	refersTo := func(f *File) string {
		for _, d := range f.DefinedNames() {
			if d.Name == filterDatabaseName {
				return d.RefersTo
			}
		}
		return ""
	}
	// SetName, then save.
	f, err := Edit(context.Background(), autoFilterFixture())
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("detail")
	if err := sh.SetName("Q1 detail"); err != nil {
		t.Fatal(err)
	}
	f2, _ := Edit(context.Background(), mustSave(t, f))
	if got := refersTo(f2); got != "'Q1 detail'!$A$1:$H$35" {
		t.Errorf("after SetName: %q", got)
	}
	// SetName, then SetAutoFilter.
	sh2, _ := f2.Sheet("Q1 detail")
	if err := sh2.SetName("Totals"); err != nil {
		t.Fatal(err)
	}
	if err := sh2.SetAutoFilter(&AutoFilter{Ref: "A1:C3"}); err != nil {
		t.Fatal(err)
	}
	if got := refersTo(f2); got != "Totals!$A$1:$C$3" {
		t.Errorf("after SetName+SetAutoFilter: %q", got)
	}
}

func TestRenameSheetRefs(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Data!$A$1:$B$2", "'New Name'!$A$1:$B$2"},
		{"'Data'!$A$1", "'New Name'!$A$1"},
		{"data!$1:$1,Data!$A:$A", "'New Name'!$1:$1,'New Name'!$A:$A"},
		{"MyData!$A$1", "MyData!$A$1"},
		{`"Data!A1"&Data!A1`, `"Data!A1"&'New Name'!A1`},
	} {
		if got := renameSheetRefs(tc.in, "Data", "New Name"); got != tc.want {
			t.Errorf("renameSheetRefs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFilterColumnTypedEditWins: editing the typed fields of a column read
// with Raw writes the edit; leaving them alone re-emits Raw.
func TestFilterColumnTypedEditWins(t *testing.T) {
	f, err := Edit(context.Background(), autoFilterFixture())
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("detail")
	af := sh.AutoFilter()
	af.Columns[0].Values = []string{"North"}
	af.Columns[2].Custom.Filters[0].Val = "9"
	if err := sh.SetAutoFilter(af); err != nil {
		t.Fatal(err)
	}
	got := saveAndRead(t, f).Sheets[1].AutoFilter
	if !reflect.DeepEqual(got.Columns[0].Values, []string{"North"}) || !got.Columns[0].Blank {
		t.Errorf("values edit lost: %+v", got.Columns[0])
	}
	if got.Columns[2].Custom.Filters[0].Val != "9" {
		t.Errorf("custom edit lost: %+v", got.Columns[2].Custom)
	}
	if !bytes.Equal(got.Columns[3].Raw, af.Columns[3].Raw) {
		t.Errorf("unedited column not verbatim:\n got %s\nwant %s", got.Columns[3].Raw, af.Columns[3].Raw)
	}
}

// TestSetAutoFilterValidation: ColID must fall inside Ref, once; a custom
// criterion holds one or two comparisons; a date group always writes year and
// dateTimeGrouping (both required).
func TestSetAutoFilterValidation(t *testing.T) {
	f := New()
	sh, _ := f.Sheet("Sheet1")
	for name, af := range map[string]*AutoFilter{
		"past width": {Ref: "A1:C9", Columns: []FilterColumn{{ColID: 3, Values: []string{"x"}}}},
		"negative":   {Ref: "A1:C9", Columns: []FilterColumn{{ColID: -1, Values: []string{"x"}}}},
		"duplicate":  {Ref: "A1:C9", Columns: []FilterColumn{{ColID: 1, Values: []string{"x"}}, {ColID: 1, Blank: true}}},
		"3 custom":   {Ref: "A1:C9", Columns: []FilterColumn{{Custom: &CustomFilters{Filters: []CustomFilter{{"equal", "1"}, {"equal", "2"}, {"equal", "3"}}}}}},
		"0 custom":   {Ref: "A1:C9", Columns: []FilterColumn{{Custom: &CustomFilters{}}}},
	} {
		if err := sh.SetAutoFilter(af); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := sh.SetAutoFilter(&AutoFilter{Ref: "A1:C9", Columns: []FilterColumn{
		{ColID: 0, DateGroups: []DateGroupItem{{Month: 3}, {Year: 2024, Day: 5, Month: 1}}},
	}}); err != nil {
		t.Fatal(err)
	}
	_, parts := zipParts(t, mustSave(t, f))
	s := string(parts["xl/worksheets/sheet1.xml"])
	for _, want := range []string{`<dateGroupItem year="0" month="3" dateTimeGrouping="month"/>`, `<dateGroupItem year="2024" month="1" day="5" dateTimeGrouping="day"/>`} {
		if !strings.Contains(s, want) {
			t.Errorf("lacks %s: %s", want, s)
		}
	}
}

// TestSetAutoFilterNilKeepsAdvancedFilter: nil on a sheet with no autoFilter
// keeps an Advanced Filter's _FilterDatabase name (and the part); removing a
// real autoFilter clears sheetPr filterMode.
func TestSetAutoFilterNilKeepsAdvancedFilter(t *testing.T) {
	data := genxlsx.New().
		AddSheet("A", wsOpen+`<sheetData/></worksheet>`).
		AddSheet("B", wsOpen+`<sheetPr filterMode="1"><tabColor rgb="FF00FF00"/></sheetPr><sheetData/><autoFilter ref="A1:B4"><filterColumn colId="0"><filters><filter val="x"/></filters></filterColumn></autoFilter></worksheet>`).
		SetDefinedNames(`<definedName name="_xlnm._FilterDatabase" localSheetId="0" hidden="1">A!$A$1:$D$9</definedName><definedName name="_xlnm._FilterDatabase" localSheetId="1" hidden="1">B!$A$1:$B$4</definedName>`).
		Bytes()
	f, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := f.Sheet("A")
	b, _ := f.Sheet("B")
	if err := a.SetAutoFilter(nil); err != nil {
		t.Fatal(err)
	}
	if err := b.SetAutoFilter(nil); err != nil {
		t.Fatal(err)
	}
	_, orig := zipParts(t, data)
	_, parts := zipParts(t, mustSave(t, f))
	if !bytes.Equal(orig["xl/worksheets/sheet1.xml"], parts["xl/worksheets/sheet1.xml"]) {
		t.Error("nil on a sheet with no autoFilter changed its part")
	}
	wb := string(parts["xl/workbook.xml"])
	if !strings.Contains(wb, `localSheetId="0" hidden="1">A!$A$1:$D$9`) || strings.Contains(wb, "B!$A$1:$B$4") {
		t.Errorf("defined names: %s", wb)
	}
	if s := string(parts["xl/worksheets/sheet2.xml"]); strings.Contains(s, "filterMode") || !strings.Contains(s, `<tabColor rgb="FF00FF00"/>`) {
		t.Errorf("sheet B: %s", s)
	}
}

// TestDeleteActiveSheetSkipsHidden: deleting the active sheet next to a
// hidden one activates the next visible sheet, marks it tabSelected, and
// keeps every other sheet's local names on their sheet.
func TestDeleteActiveSheetSkipsHidden(t *testing.T) {
	data := genxlsx.New().
		AddSheet("A", wsOpen+`<sheetViews><sheetView workbookViewId="0"/></sheetViews><sheetData/></worksheet>`).
		AddSheet("Active", wsOpen+`<sheetViews><sheetView tabSelected="1" workbookViewId="0"/></sheetViews><sheetData/></worksheet>`).
		AddHiddenSheet("Hidden", wsOpen+`<sheetViews><sheetView workbookViewId="0"/></sheetViews><sheetData/></worksheet>`).
		AddSheet("D", wsOpen+`<sheetViews><sheetView workbookViewId="0"/></sheetViews><sheetData/></worksheet>`).
		SetDefinedNames(`<definedName name="onActive" localSheetId="1">Active!$A$1</definedName><definedName name="onHidden" localSheetId="2">Hidden!$A$1</definedName><definedName name="onD" localSheetId="3">D!$A$1</definedName>`).
		Bytes()
	f, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	wb, _ := f.mutatePart("xl/workbook.xml")
	ensureBookView(wb.Root()).CreateAttr("activeTab", "1")
	if err := f.DeleteSheet("Active"); err != nil { // A, Hidden, D
		t.Fatal(err)
	}
	_, parts := zipParts(t, mustSave(t, f))
	if a := attrOf(string(parts["xl/workbook.xml"]), "activeTab"); a != "2" {
		t.Errorf("activeTab = %s, want 2 (D, skipping Hidden)", a)
	}
	if d := string(parts["xl/worksheets/sheet4.xml"]); !strings.Contains(d, `tabSelected="1"`) {
		t.Errorf("D not selected: %s", d)
	}
	got := map[string]int{}
	for _, d := range f.DefinedNames() {
		got[d.Name] = *d.LocalSheet
	}
	if want := map[string]int{"onHidden": 1, "onD": 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("local names = %v, want %v", got, want)
	}
}

// TestPaddingCellsTakeRowAndColumnStyle: an empty cell shows its row's style
// (customFormat), else its column's, else xf 0.
func TestPaddingCellsTakeRowAndColumnStyle(t *testing.T) {
	sheet := wsOpen + `<cols><col min="2" max="2" width="9" style="2" customWidth="1"/></cols><sheetData>` +
		`<row r="1" s="1" customFormat="1"><c r="C1"><v>1</v></c></row>` +
		`<row r="2"><c r="C2"><v>2</v></c></row></sheetData></worksheet>`
	data := genxlsx.New().AddSheet("S", sheet).
		SetStyles(fontFixtureStyles). // xf 1 bold, xf 2 italic
		Bytes()
	wb, err := OpenBytes(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	c := wb.Sheets[0].Cells
	if !c[0][0].Style.Font.Bold || !c[0][1].Style.Font.Bold || c[0][0].StyleID != 1 {
		t.Errorf("row-styled padding: %+v / %+v", c[0][0], c[0][1].Style.Font)
	}
	if !c[1][1].Style.Font.Italic || c[1][1].StyleID != 2 {
		t.Errorf("column-styled padding: %+v", c[1][1])
	}
	if f := c[1][0].Style.Font; f.Bold || f.Italic || c[1][0].StyleID != 0 || f.Name != "Arial" {
		t.Errorf("plain padding: %+v", c[1][0])
	}
}

// TestPivotNoStylesPart: a custom value format on a workbook with no styles
// part creates the part (wired into rels and content types); a refused
// table changes nothing.
func TestPivotNoStylesPart(t *testing.T) {
	src := wsOpen + `<sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>K</t></is></c><c r="B1" t="inlineStr"><is><t>V</t></is></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>a</t></is></c><c r="B2"><v>1</v></c></row></sheetData></worksheet>`
	data := genxlsx.New().AddSheet("S", src).AddSheet("P", wsOpen+`<sheetData/></worksheet>`).Bytes()
	pt := PivotTable{Name: "p", SourceSheet: "S", SourceRange: "A1:B2", TargetSheet: "P", Location: "A1",
		Rows: []string{"K"}, Values: []PivotValueField{{Field: "V", NumFmt: `"$"#,##0.00`}}}

	f, _ := Edit(context.Background(), data)
	bad := pt
	bad.TargetSheet = "Nope"
	if err := f.AddPivotTable(bad); err == nil {
		t.Fatal("missing target accepted")
	}
	_, orig := zipParts(t, data)
	_, after := zipParts(t, mustSave(t, f))
	if len(after) != len(orig) {
		t.Errorf("refused table changed the package: %d -> %d parts", len(orig), len(after))
	}

	if err := f.AddPivotTable(pt); err != nil {
		t.Fatal(err)
	}
	out := mustSave(t, f)
	f2, err := Edit(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if got := f2.PivotTables()[0].Values[0].NumFmt; got != `"$"#,##0.00` {
		t.Errorf("NumFmt = %q", got)
	}
	_, parts := zipParts(t, out)
	if !bytes.Contains(parts["xl/_rels/workbook.xml.rels"], []byte("relationships/styles")) ||
		!bytes.Contains(parts["[Content_Types].xml"], []byte("/xl/styles.xml")) {
		t.Error("created styles part not wired")
	}
}

// TestColorFilterMintsDxf: a typed colorFilter writes its style as a dxf
// (deduped against an equal existing one) and reads it back.
func TestColorFilterMintsDxf(t *testing.T) {
	f, err := Edit(context.Background(), autoFilterFixture())
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("detail")
	red := &Style{Font: Font{Color: Color{RGB: "FF0000"}}}
	blue := &Style{Fill: Fill{Pattern: "solid", Bg: Color{RGB: "0000FF"}}}
	if err := sh.SetAutoFilter(&AutoFilter{Ref: "A1:C9", Columns: []FilterColumn{
		{ColID: 0, Color: &ColorFilter{FontColor: true, Style: red}},
		{ColID: 1, Color: &ColorFilter{Style: blue}},
	}}); err != nil {
		t.Fatal(err)
	}
	out := mustSave(t, f)
	_, parts := zipParts(t, out)
	if n := strings.Count(string(parts["xl/styles.xml"]), "<dxf>"); n != 2 {
		t.Errorf("dxfs = %d, want 2 (red reused, blue added)", n)
	}
	wb, _ := OpenBytes(context.Background(), out)
	cols := wb.Sheets[1].AutoFilter.Columns
	if cols[0].Color.Style.Font.Color.RGB != "FF0000" || cols[1].Color.Style.Fill.Bg.RGB != "0000FF" || cols[1].Color.FontColor {
		t.Errorf("color filters = %+v / %+v", cols[0].Color, cols[1].Color)
	}
}

// TestSchemaOrderWithOtherElements: autoFilter and hyperlinks land in their
// CT_Worksheet positions among mergeCells, conditionalFormatting,
// dataValidations, printOptions, and pageMargins.
func TestSchemaOrderWithOtherElements(t *testing.T) {
	sheet := wsOpen + `<sheetData/><mergeCells count="1"><mergeCell ref="A1:B1"/></mergeCells>` +
		`<conditionalFormatting sqref="A1"><cfRule type="expression" priority="1"><formula>TRUE</formula></cfRule></conditionalFormatting>` +
		`<dataValidations count="1"><dataValidation sqref="C1" type="whole"/></dataValidations>` +
		`<printOptions gridLines="1"/><pageMargins left="0.7" right="0.7" top="0.75" bottom="0.75" header="0.3" footer="0.3"/></worksheet>`
	f, err := Edit(context.Background(), genxlsx.New().AddSheet("S", sheet).Bytes())
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("S")
	if err := sh.SetHyperlinks([]Hyperlink{{Ref: "A1", URL: "https://example.com"}}); err != nil {
		t.Fatal(err)
	}
	if err := sh.SetAutoFilter(&AutoFilter{Ref: "A1:C4"}); err != nil {
		t.Fatal(err)
	}
	_, parts := zipParts(t, mustSave(t, f))
	s := string(parts["xl/worksheets/sheet1.xml"])
	order := []string{"<sheetData", "<autoFilter", "<mergeCells", "<conditionalFormatting", "<dataValidations", "<hyperlinks", "<printOptions", "<pageMargins"}
	last := -1
	for _, tag := range order {
		i := strings.Index(s, tag)
		if i < 0 || i < last {
			t.Fatalf("%s out of order in %s", tag, s)
		}
		last = i
	}
	// The sheet had no rels part; SetHyperlinks created it.
	if !bytes.Contains(parts["xl/worksheets/_rels/sheet1.xml.rels"], []byte(`TargetMode="External"`)) {
		t.Errorf("rels part: %s", parts["xl/worksheets/_rels/sheet1.xml.rels"])
	}
}
