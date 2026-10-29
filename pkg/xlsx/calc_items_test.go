package xlsx

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/beevik/etree"
	"github.com/nathanstitt/omnidoc/pkg/xlsx/internal/xmlpart"
	genxlsx "github.com/nathanstitt/omnidoc/testdata/gen/xlsx"
)

// TestBookViewsFollowSheetMoves (request item 7): activeTab and firstSheet
// stay on their sheet through MoveSheet; a deleted active tab passes to the
// sheet that takes its place.
func TestBookViewsFollowSheetMoves(t *testing.T) {
	f := New()
	for _, n := range []string{"B", "C", "D"} {
		if _, err := f.AddSheet(n); err != nil {
			t.Fatal(err)
		}
	}
	wb, err := f.mutatePart("xl/workbook.xml")
	if err != nil {
		t.Fatal(err)
	}
	views := ensureBookView(wb.Root())
	views.CreateAttr("activeTab", "2")  // C
	views.CreateAttr("firstSheet", "1") // B

	view := func() (string, string) {
		_, parts := zipParts(t, mustSave(t, f))
		s := string(parts["xl/workbook.xml"])
		return attrOf(s, "activeTab"), attrOf(s, "firstSheet")
	}
	if err := f.MoveSheet("C", 0); err != nil { // C, Sheet1, B, D
		t.Fatal(err)
	}
	if a, fs := view(); a != "0" || fs != "2" {
		t.Errorf("after move: activeTab %s firstSheet %s, want 0 and 2", a, fs)
	}
	if err := f.DeleteSheet("C"); err != nil { // Sheet1, B, D
		t.Fatal(err)
	}
	if a, fs := view(); a != "0" || fs != "1" {
		t.Errorf("after deleting the active tab: activeTab %s firstSheet %s, want 0 and 1", a, fs)
	}
	if err := f.DeleteSheet("D"); err != nil { // Sheet1, B
		t.Fatal(err)
	}
	if a, fs := view(); a != "0" || fs != "1" {
		t.Errorf("after deleting a later sheet: activeTab %s firstSheet %s", a, fs)
	}
}

// ensureBookView adds <bookViews><workbookView/></bookViews> in schema order
// and returns the view.
func ensureBookView(root *etree.Element) *etree.Element {
	views := xmlpart.EnsureChildInOrder(root, "bookViews", workbookOrder)
	return views.CreateElement("workbookView")
}

// TestSetDefaultSizes (request item 8): the setters write sheetFormatPr,
// keep its other attributes, read back on both views, refuse out-of-range
// values, and leave the part untouched when nothing changes.
func TestSetDefaultSizes(t *testing.T) {
	data := genxlsx.New().
		AddSheet("Has", wsOpen+`<sheetFormatPr baseColWidth="10" defaultRowHeight="15.75" x14ac:dyDescent="0.25" xmlns:x14ac="http://schemas.microsoft.com/office/spreadsheetml/2009/9/ac"/><sheetData/></worksheet>`).
		AddSheet("None", wsOpen+`<sheetViews><sheetView workbookViewId="0"/></sheetViews><sheetData/></worksheet>`).
		SetStyles(fontFixtureStyles). // default font Arial 10
		Bytes()
	f, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	has, _ := f.Sheet("Has")
	none, _ := f.Sheet("None")
	if got := has.DefaultColWidth(); got != 2925.0/256 {
		t.Errorf("derived editor width = %v", got)
	}
	if err := has.SetDefaultRowHeight(21); err != nil {
		t.Fatal(err)
	}
	if err := has.SetDefaultColWidth(14.43); err != nil {
		t.Fatal(err)
	}
	if err := none.SetDefaultColWidth(20); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []float64{0, -1, 410} {
		if err := has.SetDefaultRowHeight(bad); err == nil {
			t.Errorf("row height %v accepted", bad)
		}
	}
	for _, bad := range []float64{0, -1, 256} {
		if err := has.SetDefaultColWidth(bad); err == nil {
			t.Errorf("col width %v accepted", bad)
		}
	}

	out := mustSave(t, f)
	_, parts := zipParts(t, out)
	h := string(parts["xl/worksheets/sheet1.xml"])
	for _, want := range []string{`baseColWidth="10"`, `defaultRowHeight="21"`, `customHeight="1"`, `defaultColWidth="14.43"`, `x14ac:dyDescent="0.25"`} {
		if !strings.Contains(h, want) {
			t.Errorf("Has lacks %s: %s", want, h)
		}
	}
	// A created sheetFormatPr carries the required height: Excel's 12.75 for
	// an Arial 10 workbook. It goes after sheetViews.
	if n := string(parts["xl/worksheets/sheet2.xml"]); !strings.Contains(n, `</sheetViews><sheetFormatPr defaultRowHeight="12.75" defaultColWidth="20"/><sheetData/>`) {
		t.Errorf("None: %s", n)
	}

	wb, err := OpenBytes(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if s := wb.Sheets[0]; s.DefaultRowHeight != 21 || s.DefaultColWidth != 14.43 {
		t.Errorf("reader Has = %v / %v", s.DefaultRowHeight, s.DefaultColWidth)
	}
	f2, _ := Edit(context.Background(), out)
	has2, _ := f2.Sheet("Has")
	if has2.DefaultRowHeight() != 21 || has2.DefaultColWidth() != 14.43 {
		t.Errorf("editor Has = %v / %v", has2.DefaultRowHeight(), has2.DefaultColWidth())
	}
	// Setting the same values again changes nothing.
	if err := has2.SetDefaultRowHeight(21); err != nil {
		t.Fatal(err)
	}
	if err := has2.SetDefaultColWidth(14.43); err != nil {
		t.Fatal(err)
	}
	_, again := zipParts(t, mustSave(t, f2))
	if !bytes.Equal(again["xl/worksheets/sheet1.xml"], parts["xl/worksheets/sheet1.xml"]) {
		t.Error("an unchanged default size rewrote the part")
	}
}

// TestSerial1900 (request item 9): a 1904 serial converts to the 1900 serial
// of the same date, the same date the reader puts in Value.T, and back.
func TestSerial1900(t *testing.T) {
	for _, serial := range []float64{0, 1, 59.5, 365, 45000.25} {
		got := serialToTime(Serial1900(serial, true), false)
		if want := serialToTime(serial, true); !got.Equal(want) {
			t.Errorf("1904 serial %v: via 1900 = %v, want %v", serial, got, want)
		}
		if back := SerialFrom1900(Serial1900(serial, true), true); back != serial {
			t.Errorf("round trip %v -> %v", serial, back)
		}
		if Serial1900(serial, false) != serial || SerialFrom1900(serial, false) != serial {
			t.Errorf("1900 workbook changed serial %v", serial)
		}
	}
}

// TestPaddingCellsCarryStyle: grid cells with no <c> element carry xf 0's
// style, so Cell.Style is never nil and an unstyled cell draws in the
// default font.
func TestPaddingCellsCarryStyle(t *testing.T) {
	data := genxlsx.New().
		AddSheet("S", wsOpen+`<sheetData><row r="2"><c r="C2"><v>1</v></c></row></sheetData></worksheet>`).
		SetStyles(fontFixtureStyles).
		Bytes()
	wb, err := OpenBytes(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	for r, row := range wb.Sheets[0].Cells {
		for c, cell := range row {
			if cell.Style == nil || cell.Style.Font.Name != "Arial" || cell.Style.Font.Size != 10 {
				t.Errorf("%s style = %+v", CellRef(r+1, c+1), cell.Style)
			}
		}
	}
}

// attrOf returns the first value of attribute name in s, or "" when absent.
func attrOf(s, name string) string {
	_, rest, ok := strings.Cut(s, " "+name+`="`)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, `"`)
	return v
}
