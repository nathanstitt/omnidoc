package xlsx

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	genxlsx "github.com/nathanstitt/omnidoc/testdata/gen/xlsx"
)

// TestPivotSingleValueNoPlaceholder: with one value field the writer emits no
// "Values" placeholder even when ValuesAxis is set, and the reader does not
// invent one from a leftover dataOnRows.
func TestPivotSingleValueNoPlaceholder(t *testing.T) {
	pt := testPivot()
	pt.ValuesAxis = "row"
	f := pivotSource(t)
	if err := f.AddPivotTable(pt); err != nil {
		t.Fatal(err)
	}
	_, parts := zipParts(t, mustSave(t, f))
	if s := parts["xl/pivotTables/pivotTable1.xml"]; bytes.Contains(s, []byte(`x="-2"`)) || bytes.Contains(s, []byte("dataOnRows")) {
		t.Errorf("one value wrote a placeholder:\n%s", s)
	}

	one := strings.Replace(calcPivotTable, `<dataField name="Avg Margin" fld="3" subtotal="average" numFmtId="10"/>`, "", 1)
	one = strings.Replace(one, `<field x="1"/><field x="-2"/>`, `<field x="1"/>`, 1)
	one = strings.Replace(one, `rowGrandTotals="1"`, `dataOnRows="1" rowGrandTotals="1"`, 1)
	ed, err := Edit(context.Background(), pivotFixtureWithTable(one))
	if err != nil {
		t.Fatal(err)
	}
	if got := ed.PivotTables()[0]; got.ValuesAxis != "" {
		t.Errorf("leftover dataOnRows read as ValuesAxis %q", got.ValuesAxis)
	}
}

// TestPivotNumFmtResolution: General maps to id 0 (no custom record), a
// builtin code needs no styles part, and a refused table leaves styles.xml
// untouched.
func TestPivotNumFmtResolution(t *testing.T) {
	f := pivotSource(t)
	pt := testPivot()
	pt.Values[0].NumFmt = "General"
	if err := f.AddPivotTable(pt); err != nil {
		t.Fatal(err)
	}
	_, parts := zipParts(t, mustSave(t, f))
	if bytes.Contains(parts["xl/styles.xml"], []byte("<numFmt ")) || bytes.Contains(parts["xl/pivotTables/pivotTable1.xml"], []byte("numFmtId")) {
		t.Errorf("General allocated a format:\n%s\n%s", parts["xl/styles.xml"], parts["xl/pivotTables/pivotTable1.xml"])
	}

	// A refused table (second value names no column) does not dirty styles.
	data := mustSave(t, pivotSource(t))
	f2, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	bad := testPivot()
	bad.Values = []PivotValueField{{Field: "Sales", NumFmt: `"$"#,##0.00`}, {Field: "Nope"}}
	if err := f2.AddPivotTable(bad); err == nil {
		t.Fatal("unknown value field accepted")
	}
	_, before := zipParts(t, data)
	_, after := zipParts(t, mustSave(t, f2))
	if !bytes.Equal(before["xl/styles.xml"], after["xl/styles.xml"]) {
		t.Error("a refused table changed styles.xml")
	}

	// No styles part at all: a builtin code still writes.
	src := wsOpen + `<sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>K</t></is></c><c r="B1" t="inlineStr"><is><t>V</t></is></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>a</t></is></c><c r="B2"><v>1</v></c></row></sheetData></worksheet>`
	f3, err := Edit(context.Background(), genxlsx.New().AddSheet("S", src).AddSheet("P", wsOpen+`<sheetData/></worksheet>`).Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := f3.AddPivotTable(PivotTable{Name: "p", SourceSheet: "S", SourceRange: "A1:B2", TargetSheet: "P", Location: "A1",
		Rows: []string{"K"}, Values: []PivotValueField{{Field: "V", NumFmt: "0.00%"}}}); err != nil {
		t.Fatalf("builtin format without a styles part: %v", err)
	}
}

// TestHyperlinksStrictAndInternal: an ISO-strict sheet's link resolves and a
// rewrite keeps the strict relationship type and the bound prefix; a
// relationship without TargetMode reads the same target in both views.
func TestHyperlinksStrictAndInternal(t *testing.T) {
	sheet := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://purl.oclc.org/ooxml/spreadsheetml/main" xmlns:r="http://purl.oclc.org/ooxml/officeDocument/relationships"><sheetData/><hyperlinks><hyperlink ref="A1" r:id="rId1"/><hyperlink ref="A2" r:id="rId2"/></hyperlinks></worksheet>`
	rels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://purl.oclc.org/ooxml/officeDocument/relationships/hyperlink" Target="https://strict.example/" TargetMode="External"/><Relationship Id="rId2" Type="http://purl.oclc.org/ooxml/officeDocument/relationships/hyperlink" Target="docs/spec.pdf"/></Relationships>`
	data := genxlsx.New().AddSheet("S", sheet).AddPart("xl/worksheets/_rels/sheet1.xml.rels", "", rels).Bytes()
	want := []Hyperlink{{Ref: "A1", URL: "https://strict.example/"}, {Ref: "A2", URL: "docs/spec.pdf"}}

	wb, err := OpenBytes(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if got := wb.Sheets[0].Hyperlinks; !reflect.DeepEqual(got, want) {
		t.Errorf("reader = %+v, want %+v", got, want)
	}
	f, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("S")
	if got := sh.Hyperlinks(); !reflect.DeepEqual(got, want) {
		t.Errorf("editor = %+v, want %+v", got, want)
	}
	if err := sh.SetHyperlinks([]Hyperlink{{Ref: "B1", URL: "https://new.example/"}}); err != nil {
		t.Fatal(err)
	}
	_, parts := zipParts(t, mustSave(t, f))
	ws := string(parts["xl/worksheets/sheet1.xml"])
	if strings.Contains(ws, "xmlns:r1") || !strings.Contains(ws, `<hyperlink ref="B1" r:id="rId1"/>`) {
		t.Errorf("strict sheet rewrite: %s", ws)
	}
	if r := string(parts["xl/worksheets/_rels/sheet1.xml.rels"]); !strings.Contains(r, relHyperlinkStrict) {
		t.Errorf("strict rels rewrite: %s", r)
	}
}

// TestSetShowGridLinesEmptyViews: hiding on an empty <sheetViews/> creates the
// view instead of doing nothing.
func TestSetShowGridLinesEmptyViews(t *testing.T) {
	f, err := Edit(context.Background(), genxlsx.New().AddSheet("S", wsOpen+`<sheetViews/><sheetData/></worksheet>`).Bytes())
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("S")
	sh.SetShowGridLines(false)
	if sh.ShowGridLines() {
		t.Error("gridlines still shown")
	}
}

// TestLocalNamesFollowSheetMoves: MoveSheet and DeleteSheet keep every
// sheet-local name (the autoFilter's _FilterDatabase among them) on its
// sheet; the deleted sheet's local names go.
func TestLocalNamesFollowSheetMoves(t *testing.T) {
	f, err := Edit(context.Background(), autoFilterFixture()) // Other(0), detail(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.AddSheet("Third"); err != nil {
		t.Fatal(err)
	}
	third, _ := f.Sheet("Third")
	if err := third.SetAutoFilter(&AutoFilter{Ref: "A1:B2"}); err != nil {
		t.Fatal(err)
	}
	if err := f.MoveSheet("detail", 0); err != nil { // detail(0), Other(1), Third(2)
		t.Fatal(err)
	}
	local := func() map[string]int {
		out := map[string]int{}
		for _, d := range f.DefinedNames() {
			if d.LocalSheet != nil {
				out[d.RefersTo] = *d.LocalSheet
			}
		}
		return out
	}
	if got, want := local(), map[string]int{"detail!$A$1:$H$35": 0, "Third!$A$1:$B$2": 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("after move = %v, want %v", got, want)
	}
	if err := f.DeleteSheet("detail"); err != nil { // Other(0), Third(1)
		t.Fatal(err)
	}
	if got, want := local(), map[string]int{"Third!$A$1:$B$2": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("after delete = %v, want %v", got, want)
	}
}
