package xlsx

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	genxlsx "github.com/nathanstitt/omnidoc/testdata/gen/xlsx"
)

// autoFilterSheet carries every filter kind on one autoFilter (absolute "$"
// ref, as Google Sheets writes it) plus a sortState child the package does
// not model.
const autoFilterSheet = wsOpen + `<sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData>` +
	`<autoFilter ref="$A$1:$H$35">` +
	`<filterColumn colId="0"><filters blank="1"><filter val="East"/><filter val="West"/></filters></filterColumn>` +
	`<filterColumn colId="1"><filters><dateGroupItem year="2024" month="3" dateTimeGrouping="month"/></filters></filterColumn>` +
	`<filterColumn colId="2"><customFilters and="1"><customFilter operator="greaterThan" val="4"/><customFilter val="1*"/></customFilters></filterColumn>` +
	`<filterColumn colId="3"><top10 top="0" percent="1" val="10" filterVal="2.5"/></filterColumn>` +
	`<filterColumn colId="4"><dynamicFilter type="aboveAverage" val="12.5"/></filterColumn>` +
	`<filterColumn colId="5"><colorFilter dxfId="0" cellColor="0"/></filterColumn>` +
	`<filterColumn colId="6" showButton="0"><iconFilter iconSet="3Arrows" iconId="2"/></filterColumn>` +
	`<sortState ref="A2:H35"><sortCondition descending="1" ref="C2:C35"/></sortState>` +
	`</autoFilter></worksheet>`

const autoFilterStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><cellXfs count="1"><xf fontId="0"/></cellXfs><dxfs count="1"><dxf><font><color rgb="FFFF0000"/></font></dxf></dxfs></styleSheet>`

func autoFilterFixture() []byte {
	return genxlsx.New().
		AddSheet("Other", wsOpen+`<sheetData/></worksheet>`).
		AddSheet("detail", autoFilterSheet).
		SetStyles(autoFilterStyles).
		SetDefinedNames(`<definedName name="_xlnm._FilterDatabase" localSheetId="1" hidden="1">detail!$A$1:$H$35</definedName><definedName name="Keep" comment="kept">Other!$A$1</definedName>`).
		Bytes()
}

// TestAutoFilterRead pins every typed field, the "$"-free Ref, the Raw and
// Extra passthrough bytes, and that the editor view reads the same value.
func TestAutoFilterRead(t *testing.T) {
	data := autoFilterFixture()
	wb, err := OpenBytes(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if wb.Sheets[0].AutoFilter != nil {
		t.Errorf("sheet without a filter read %+v", wb.Sheets[0].AutoFilter)
	}
	af := wb.Sheets[1].AutoFilter
	if af == nil {
		t.Fatal("no AutoFilter")
	}
	if af.Ref != "A1:H35" {
		t.Errorf("Ref = %q, want A1:H35", af.Ref)
	}
	if len(af.Columns) != 7 {
		t.Fatalf("columns = %d, want 7", len(af.Columns))
	}
	c := af.Columns
	if c[0].ColID != 0 || !c[0].Blank || !reflect.DeepEqual(c[0].Values, []string{"East", "West"}) {
		t.Errorf("values column = %+v", c[0])
	}
	if !reflect.DeepEqual(c[1].DateGroups, []DateGroupItem{{Year: 2024, Month: 3, Grouping: "month"}}) {
		t.Errorf("date groups = %+v", c[1].DateGroups)
	}
	if c[2].Custom == nil || !c[2].Custom.And ||
		!reflect.DeepEqual(c[2].Custom.Filters, []CustomFilter{{"greaterThan", "4"}, {"equal", "1*"}}) {
		t.Errorf("custom = %+v", c[2].Custom)
	}
	if tt := c[3].Top10; tt == nil || !tt.Bottom || !tt.Percent || tt.Val != 10 || tt.FilterVal == nil || *tt.FilterVal != 2.5 {
		t.Errorf("top10 = %+v", c[3].Top10)
	}
	if d := c[4].Dynamic; d == nil || d.Type != "aboveAverage" || d.Val == nil || *d.Val != 12.5 || d.MaxVal != nil {
		t.Errorf("dynamic = %+v", c[4].Dynamic)
	}
	if cf := c[5].Color; cf == nil || !cf.FontColor || cf.Style == nil || cf.Style.Font.Color.RGB != "FF0000" {
		t.Errorf("color = %+v", c[5].Color)
	}
	if ic := c[6].Icon; ic == nil || ic.IconSet != "3Arrows" || ic.IconID == nil || *ic.IconID != 2 || !c[6].HiddenButton {
		t.Errorf("icon = %+v hidden=%v", c[6].Icon, c[6].HiddenButton)
	}
	if !strings.Contains(string(c[2].Raw), `<customFilter operator="greaterThan" val="4"/>`) {
		t.Errorf("Raw not verbatim: %s", c[2].Raw)
	}
	if len(af.Extra) != 1 || !strings.HasPrefix(string(af.Extra[0]), `<sortState ref="A2:H35">`) {
		t.Errorf("Extra = %q", af.Extra)
	}

	f, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("detail")
	if got := sh.AutoFilter(); !reflect.DeepEqual(got, af) {
		t.Errorf("editor view differs:\n got %+v\nwant %+v", got, af)
	}
}

// TestAutoFilterRawRoundTrip: handing the read value back re-emits it
// unchanged (Raw columns and Extra verbatim, the defined name already right).
func TestAutoFilterRawRoundTrip(t *testing.T) {
	f, err := Edit(context.Background(), autoFilterFixture())
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("detail")
	before := sh.AutoFilter()
	if err := sh.SetAutoFilter(before); err != nil {
		t.Fatal(err)
	}
	wb := saveAndRead(t, f)
	if got := wb.Sheets[1].AutoFilter; !reflect.DeepEqual(got, before) {
		t.Errorf("raw round trip changed the filter:\n got %+v\nwant %+v", got, before)
	}
	if n := countName(wb.DefinedNames, filterDatabaseName); n != 1 {
		t.Errorf("_FilterDatabase names = %d, want 1", n)
	}
}

// TestAutoFilterTypedRoundTrip writes every kind from the typed fields (Raw
// cleared) and reads back the same criteria; the colorFilter mints a dxf.
func TestAutoFilterTypedRoundTrip(t *testing.T) {
	data := autoFilterFixture()
	wb, err := OpenBytes(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	want := *wb.Sheets[1].AutoFilter
	want.Columns = append([]FilterColumn{}, want.Columns...)
	for i := range want.Columns {
		want.Columns[i].Raw = nil
	}

	f, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("detail")
	if err := sh.SetAutoFilter(&want); err != nil {
		t.Fatal(err)
	}
	got := saveAndRead(t, f).Sheets[1].AutoFilter
	for i := range got.Columns {
		got.Columns[i].Raw = nil
	}
	if !reflect.DeepEqual(got, &want) {
		t.Errorf("typed round trip:\n got %+v\nwant %+v", got, &want)
	}
}

// TestSetAutoFilterDefinedName: a new filter creates the hidden sheet-local
// _xlnm._FilterDatabase name (quoted sheet name, absolute ref); nil removes
// the filter and only that name, keeping other names' attributes.
func TestSetAutoFilterDefinedName(t *testing.T) {
	f := New()
	sh, _ := f.Sheet("Sheet1")
	if err := sh.SetName("Q1 data"); err != nil {
		t.Fatal(err)
	}
	if err := sh.SetAutoFilter(&AutoFilter{Ref: "$B$2:D9", Columns: []FilterColumn{{ColID: 1, Values: []string{"x"}}}}); err != nil {
		t.Fatal(err)
	}
	wb := saveAndRead(t, f)
	if af := wb.Sheets[0].AutoFilter; af == nil || af.Ref != "B2:D9" {
		t.Fatalf("filter = %+v", af)
	}
	var dn *DefinedName
	for i := range wb.DefinedNames {
		if wb.DefinedNames[i].Name == filterDatabaseName {
			dn = &wb.DefinedNames[i]
		}
	}
	if dn == nil || dn.RefersTo != "'Q1 data'!$B$2:$D$9" || !dn.Hidden || dn.LocalSheet == nil || *dn.LocalSheet != 0 {
		t.Fatalf("defined name = %+v", dn)
	}

	// Removing on the fixture keeps the other name and its comment attr.
	f2, err := Edit(context.Background(), autoFilterFixture())
	if err != nil {
		t.Fatal(err)
	}
	sh2, _ := f2.Sheet("detail")
	if err := sh2.SetAutoFilter(nil); err != nil {
		t.Fatal(err)
	}
	out := mustSave(t, f2)
	_, parts := zipParts(t, out)
	if s := string(parts["xl/worksheets/sheet2.xml"]); strings.Contains(s, "autoFilter") {
		t.Errorf("filter not removed: %s", s)
	}
	wbXML := string(parts["xl/workbook.xml"])
	if strings.Contains(wbXML, filterDatabaseName) || !strings.Contains(wbXML, `<definedName name="Keep" comment="kept">Other!$A$1</definedName>`) {
		t.Errorf("defined names after removal: %s", wbXML)
	}
}

// TestSetAutoFilterErrors: a bad ref and a column with two filter kinds are
// refused before anything changes.
func TestSetAutoFilterErrors(t *testing.T) {
	f := New()
	sh, _ := f.Sheet("Sheet1")
	if err := sh.SetAutoFilter(&AutoFilter{Ref: "not a ref"}); err == nil {
		t.Error("bad ref accepted")
	}
	two := &AutoFilter{Ref: "A1:B2", Columns: []FilterColumn{{Values: []string{"a"}, Top10: &Top10Filter{Val: 3}}}}
	if err := sh.SetAutoFilter(two); !errors.Is(err, ErrFilterColumn) {
		t.Errorf("two kinds: err = %v, want ErrFilterColumn", err)
	}
	if sh.AutoFilter() != nil {
		t.Error("a refused filter was written")
	}
}

func TestQuoteSheetName(t *testing.T) {
	for in, want := range map[string]string{
		"detail":    "detail",
		"Sheet1":    "Sheet1",
		"Q1 data":   "'Q1 data'",
		"A1":        "'A1'",
		"xfd100":    "'xfd100'",
		"R1C1":      "'R1C1'",
		"rc":        "'rc'",
		"Report":    "Report",
		"1st":       "'1st'",
		"it's":      "'it''s'",
		"data.2024": "data.2024",
	} {
		if got := quoteSheetName(in); got != want {
			t.Errorf("quoteSheetName(%q) = %q, want %q", in, got, want)
		}
	}
}

func countName(names []DefinedName, name string) int {
	n := 0
	for _, d := range names {
		if d.Name == name {
			n++
		}
	}
	return n
}
