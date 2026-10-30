package xlsx

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	genxlsx "github.com/nathanstitt/omnidoc/testdata/gen/xlsx"
)

// hiddenFixture holds each way a file hides rows and columns: a row a
// filter hid (inside the autoFilter range, no height), a row hidden by hand
// that keeps its own height, hidden="true" as some producers spell it, a
// hidden column range wider than one column with a width and a style, and a
// hidden column with no width at all.
func hiddenFixture() []byte {
	return genxlsx.New().
		AddSheet("Data", wsOpen+
			`<cols><col min="2" max="2" width="20" customWidth="1"/><col min="3" max="5" width="12" style="0" hidden="1" customWidth="1"/><col min="8" max="8" hidden="1"/></cols>`+
			`<sheetData>`+
			`<row r="1"><c r="A1" t="inlineStr"><is><t>head</t></is></c></row>`+
			`<row r="2" hidden="1"><c r="A2" t="inlineStr"><is><t>filtered</t></is></c></row>`+
			`<row r="3"><c r="A3" t="inlineStr"><is><t>kept</t></is></c></row>`+
			`<row r="7" ht="30" customHeight="1" hidden="1"><c r="A7" t="inlineStr"><is><t>by hand</t></is></c></row>`+
			`<row r="9" hidden="true"/>`+
			`</sheetData><autoFilter ref="A1:A3"/></worksheet>`).
		Bytes()
}

func TestHiddenRowsAndColsRead(t *testing.T) {
	data := hiddenFixture()
	wb, err := OpenBytes(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	s := wb.Sheets[0]
	if want := map[int]bool{1: true, 6: true, 8: true}; !reflect.DeepEqual(s.HiddenRows, want) {
		t.Errorf("HiddenRows = %v, want %v", s.HiddenRows, want)
	}
	if want := map[int]bool{2: true, 3: true, 4: true, 7: true}; !reflect.DeepEqual(s.HiddenCols, want) {
		t.Errorf("HiddenCols = %v, want %v", s.HiddenCols, want)
	}
	if s.RowHeights[6] != 30 {
		t.Errorf("hidden row 7 lost its height: RowHeights = %v", s.RowHeights)
	}
	if s.ColWidths[3] != 12 {
		t.Errorf("hidden column D lost its width: ColWidths = %v", s.ColWidths)
	}

	f, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("Data")
	for row, want := range map[int]bool{1: false, 2: true, 3: false, 7: true, 9: true, 10: false} {
		if got := sh.RowHidden(row); got != want {
			t.Errorf("RowHidden(%d) = %v, want %v", row, got, want)
		}
	}
	for col, want := range map[int]bool{1: false, 2: false, 3: true, 5: true, 6: false, 8: true} {
		if got := sh.ColHidden(col); got != want {
			t.Errorf("ColHidden(%d) = %v, want %v", col, got, want)
		}
	}
}

// TestSetRowHidden hides a row the sheet has no element for, shows the
// hand-hidden row (which must keep its height), and shows the bare hidden
// row (whose element then says nothing and goes).
func TestSetRowHidden(t *testing.T) {
	f, err := Edit(context.Background(), hiddenFixture())
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("Data")
	for _, step := range []struct {
		row    int
		hidden bool
	}{{5, true}, {7, false}, {9, false}} {
		if err := sh.SetRowHidden(step.row, step.hidden); err != nil {
			t.Fatalf("SetRowHidden(%d, %v): %v", step.row, step.hidden, err)
		}
	}

	out := mustSave(t, f)
	_, parts := zipParts(t, out)
	x := string(parts["xl/worksheets/sheet1.xml"])
	if !strings.Contains(x, `<row r="3">`) || !strings.Contains(x, `<row r="5" hidden="1"/><row r="7"`) {
		t.Errorf("row 5 not hidden in order: %s", x)
	}
	if !strings.Contains(x, `<row r="7" ht="30" customHeight="1">`) {
		t.Errorf("showing row 7 did not keep its height: %s", x)
	}
	if strings.Contains(x, `<row r="9"`) {
		t.Errorf("showing bare row 9 kept an empty element: %s", x)
	}

	wb, err := OpenBytes(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[int]bool{1: true, 4: true}; !reflect.DeepEqual(wb.Sheets[0].HiddenRows, want) {
		t.Errorf("reopened HiddenRows = %v, want %v", wb.Sheets[0].HiddenRows, want)
	}
	if wb.Sheets[0].RowHeights[6] != 30 {
		t.Errorf("reopened row 7 height = %v, want 30", wb.Sheets[0].RowHeights[6])
	}
}

// TestSetColHidden splits the hidden C:E range to show D, so C and E stay
// hidden with the range's width and style; hides B, which keeps its width;
// hides G, which no range covers, at the default width; and shows H, whose
// <col> then says nothing and goes.
func TestSetColHidden(t *testing.T) {
	f, err := Edit(context.Background(), hiddenFixture())
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("Data")
	for _, step := range []struct {
		col    int
		hidden bool
	}{{4, false}, {2, true}, {7, true}, {8, false}} {
		if err := sh.SetColHidden(step.col, step.hidden); err != nil {
			t.Fatalf("SetColHidden(%d, %v): %v", step.col, step.hidden, err)
		}
	}

	out := mustSave(t, f)
	_, parts := zipParts(t, out)
	x := string(parts["xl/worksheets/sheet1.xml"])
	want := `<cols>` +
		`<col min="2" max="2" width="20" customWidth="1" hidden="1"/>` +
		`<col min="3" max="3" width="12" style="0" hidden="1" customWidth="1"/>` +
		`<col min="4" max="4" width="12" style="0" customWidth="1"/>` +
		`<col min="5" max="5" width="12" style="0" hidden="1" customWidth="1"/>` +
		`<col min="7" max="7" width="9.140625" hidden="1"/>` +
		`</cols>`
	if !strings.Contains(x, want) {
		t.Errorf("cols =\n%s\nwant\n%s", x, want)
	}

	wb, err := OpenBytes(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[int]bool{1: true, 2: true, 4: true, 6: true}; !reflect.DeepEqual(wb.Sheets[0].HiddenCols, want) {
		t.Errorf("reopened HiddenCols = %v, want %v", wb.Sheets[0].HiddenCols, want)
	}

	// Showing G again removes the <col> hiding added, and the last one
	// takes <cols> with it.
	f2, err := Edit(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	sh2, _ := f2.Sheet("Data")
	if err := sh2.SetColHidden(7, false); err != nil {
		t.Fatal(err)
	}
	_, parts2 := zipParts(t, mustSave(t, f2))
	if x := string(parts2["xl/worksheets/sheet1.xml"]); strings.Contains(x, `min="7"`) {
		t.Errorf("showing G kept its default <col>: %s", x)
	}

	empty, err := Edit(context.Background(), genxlsx.New().AddSheet("S", wsOpen+`<sheetData/></worksheet>`).Bytes())
	if err != nil {
		t.Fatal(err)
	}
	es, _ := empty.Sheet("S")
	if err := es.SetColHidden(1, true); err != nil {
		t.Fatal(err)
	}
	if err := es.SetColHidden(1, false); err != nil {
		t.Fatal(err)
	}
	_, ep := zipParts(t, mustSave(t, empty))
	if x := string(ep["xl/worksheets/sheet1.xml"]); strings.Contains(x, "<cols") {
		t.Errorf("hide then show left a <cols>: %s", x)
	}
}

// TestSetHiddenNoOp writes back every fact as read; the part must stay
// byte-identical, so a caller can push the flags on every save.
func TestSetHiddenNoOp(t *testing.T) {
	data := hiddenFixture()
	f, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("Data")
	for row := 1; row <= 12; row++ {
		if err := sh.SetRowHidden(row, sh.RowHidden(row)); err != nil {
			t.Fatal(err)
		}
	}
	for col := 1; col <= 12; col++ {
		if err := sh.SetColHidden(col, sh.ColHidden(col)); err != nil {
			t.Fatal(err)
		}
	}
	_, parts := zipParts(t, mustSave(t, f))
	_, orig := zipParts(t, data)
	if !bytes.Equal(parts["xl/worksheets/sheet1.xml"], orig["xl/worksheets/sheet1.xml"]) {
		t.Errorf("a no-op write changed the part:\n%s", parts["xl/worksheets/sheet1.xml"])
	}
}

func TestSetHiddenOutOfRange(t *testing.T) {
	f, err := Edit(context.Background(), hiddenFixture())
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("Data")
	for _, row := range []int{0, -1, maxSheetRows + 1} {
		if err := sh.SetRowHidden(row, true); err == nil {
			t.Errorf("SetRowHidden(%d) accepted an out-of-range row", row)
		}
	}
	for _, col := range []int{0, maxSheetCols + 1} {
		if err := sh.SetColHidden(col, true); err == nil {
			t.Errorf("SetColHidden(%d) accepted an out-of-range column", col)
		}
	}
}

// TestColWidthKeepsHidden: setting or clearing a width splits the range like
// SetColHidden does, so the column keeps its hidden flag and style, and a
// split keeps the ranges min-ascending even with a range after it.
func TestColWidthKeepsHidden(t *testing.T) {
	f, err := Edit(context.Background(), hiddenFixture())
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("Data")
	sh.SetColWidth(4, 30)
	sh.ClearColWidth(5)
	sh.SetColWidth(6, 8)

	_, parts := zipParts(t, mustSave(t, f))
	x := string(parts["xl/worksheets/sheet1.xml"])
	want := `<cols>` +
		`<col min="2" max="2" width="20" customWidth="1"/>` +
		`<col min="3" max="3" width="12" style="0" hidden="1" customWidth="1"/>` +
		`<col min="4" max="4" width="30" style="0" hidden="1" customWidth="1"/>` +
		`<col min="5" max="5" style="0" hidden="1"/>` +
		`<col min="6" max="6" width="8" customWidth="1"/>` +
		`<col min="8" max="8" hidden="1"/>` +
		`</cols>`
	if !strings.Contains(x, want) {
		t.Errorf("cols =\n%s\nwant\n%s", x, want)
	}
}
