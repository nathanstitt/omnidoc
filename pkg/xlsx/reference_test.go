package xlsx

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// googleExport is tinycld calc's reference workbook: a real Google Sheets
// export with every name and amount replaced (see testdata/xlsx/README.md).
func googleExport(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "xlsx", "google-export.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestGoogleExportRead pins the facts calc imports from the reference export
// (request items 1, 2, 4, 5) and the recalculated values calc checks.
func TestGoogleExportRead(t *testing.T) {
	data := googleExport(t)
	wb, err := OpenBytes(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if len(wb.Sheets) != 3 {
		t.Fatalf("sheets = %d, want 3", len(wb.Sheets))
	}
	detail := wb.Sheets[0]

	// Item 1: gridlines are off on sheets 2 and 3 only.
	for i, want := range []bool{false, true, true} {
		if wb.Sheets[i].HideGridLines != want {
			t.Errorf("sheet %d HideGridLines = %v, want %v", i+1, wb.Sheets[i].HideGridLines, want)
		}
	}
	// Item 2: the autoFilter ref is stored as $A$1:$BY$35.
	if af := detail.AutoFilter; af == nil || af.Ref != "A1:BY35" {
		t.Errorf("detail AutoFilter = %+v, want A1:BY35", af)
	}
	if detail.FrozenRows != 1 || detail.FrozenCols != 2 {
		t.Errorf("frozen = %d/%d, want 1/2", detail.FrozenRows, detail.FrozenCols)
	}
	// Item 5: the Normal style is Arial 10, and font 5 (Arial, no <sz>)
	// resolves to that size.
	if f := wb.DefaultFont; f.Name != "Arial" || f.Size != 10 {
		t.Errorf("DefaultFont = %+v, want Arial 10", f)
	}
	for _, s := range wb.Sheets {
		for r, row := range s.Cells {
			for c, cell := range row {
				if cell.Style.Font.Size == 0 || cell.Style.Font.Name == "" {
					t.Fatalf("%s %s: unresolved font %+v", s.Name, CellRef(r+1, c+1), cell.Style.Font)
				}
			}
		}
	}

	// The recalculated formula caches, as stored. Google rounds a cache to
	// ten significant digits: BD2 (=BC2/BC34) is cached as 0.0152330746;
	// the full-precision quotient is 0.015233074615971435.
	at := func(ref string) float64 {
		row, col, _ := ParseCellRef(ref)
		return detail.Cells[row-1][col-1].Value.F
	}
	for ref, want := range map[string]float64{"C34": 7800.87, "BC34": 420323.55, "BD2": 0.0152330746, "BG38": 425717.05} {
		if got := at(ref); got != want {
			t.Errorf("detail!%s = %v, want %v", ref, got, want)
		}
	}
	if q := at("BC2") / at("BC34"); q != 0.015233074615971435 {
		t.Errorf("BC2/BC34 = %v, want 0.015233074615971435", q)
	}
	// Google writes no cached output cells for a pivot, so its grand total
	// (420323.55 for "Summary of $ by person") is checked at the source: the
	// SUM of "Total for year" (column BC) over the pivot's data rows.
	var total float64
	for r := 2; r <= 33; r++ {
		total += at("BC" + strconv.Itoa(r))
	}
	if math.Abs(total-420323.55) > 1e-6 {
		t.Errorf("SUM(BC2:BC33) = %v, want 420323.55", total)
	}
}

// TestGoogleExportPivots pins item 4 on the real export and calc's save
// shape: both pivots survive RemovePivotTables+AddPivotTable with the same
// definition and every cache field name (20 repeated " " headers, M/d/yy
// date headers) unchanged.
func TestGoogleExportPivots(t *testing.T) {
	data := googleExport(t)
	f, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	pts := f.PivotTables()
	if len(pts) != 2 {
		t.Fatalf("pivots = %d, want 2", len(pts))
	}
	both, byPerson := pts[0], pts[1]
	if both.Name != "Summary both $ and Hours" || byPerson.Name != "Summary of $ by person" {
		t.Fatalf("pivot names = %q, %q", both.Name, byPerson.Name)
	}
	// colFields <field x="-2"/>: the Values placeholder, not an axis field.
	if both.ValuesAxis != "col" || both.ValuesPosition != 0 || len(both.Cols) != 0 {
		t.Errorf("Values placeholder = %q@%d, cols %q", both.ValuesAxis, both.ValuesPosition, both.Cols)
	}
	// "SUM of Employee" is over a text column: its format comes from the
	// pivotField numFmtId="164", not from any number cell.
	wantFmt := []string{"", `"$"#,##0.00`, `"$"#,##0.00`}
	for i, v := range both.Values {
		if v.NumFmt != wantFmt[i] {
			t.Errorf("%s NumFmt = %q, want %q", v.DisplayName, v.NumFmt, wantFmt[i])
		}
	}
	if want := map[string]string{"Employee": "ascending", "W/C code - pay ": "ascending"}; !reflect.DeepEqual(byPerson.AxisSort, want) {
		t.Errorf("AxisSort = %v, want %v", byPerson.AxisSort, want)
	}
	if loc := byPerson.LocationInfo; loc == nil || *loc != (PivotLocation{FirstDataRow: 1, FirstDataCol: 1}) {
		t.Errorf("LocationInfo = %+v", loc)
	}
	if n := len(byPerson.CacheFields); n != 77 || byPerson.CacheFields[2] != "12/6/24" || byPerson.CacheFields[58] != " 2" {
		t.Errorf("CacheFields (%d) = %q", n, byPerson.CacheFields)
	}

	if err := f.RemovePivotTables(); err != nil {
		t.Fatal(err)
	}
	for _, pt := range pts {
		if err := f.AddPivotTable(pt); err != nil {
			t.Fatalf("AddPivotTable(%q): %v", pt.Name, err)
		}
	}
	f2, err := Edit(context.Background(), mustSave(t, f))
	if err != nil {
		t.Fatal(err)
	}
	again := f2.PivotTables()
	for i := range pts {
		want := pts[i]
		// Google omits the required dataCaption and the style name; the
		// writer fills in Excel's defaults.
		if want.StyleName == "" {
			want.StyleName = defaultPivotStyle
		}
		if want.DataCaption == "" {
			want.DataCaption = "Values"
		}
		if !reflect.DeepEqual(again[i], want) {
			t.Errorf("pivot %q changed on re-add:\n got %+v\nwant %+v", want.Name, again[i], want)
		}
	}
}

// TestGoogleExportPushBack is calc's save loop on the real export: writing
// back every sheet's gridlines, autoFilter, and hyperlinks as read changes no
// part.
func TestGoogleExportPushBack(t *testing.T) {
	data := googleExport(t)
	f, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range f.SheetNames() {
		sh, _ := f.Sheet(name)
		sh.SetShowGridLines(sh.ShowGridLines())
		if err := sh.SetAutoFilter(sh.AutoFilter()); err != nil {
			t.Fatal(err)
		}
		if err := sh.SetHyperlinks(sh.Hyperlinks()); err != nil {
			t.Fatal(err)
		}
	}
	_, want := zipParts(t, data)
	_, got := zipParts(t, mustSave(t, f))
	for name := range want {
		if !bytes.Equal(want[name], got[name]) {
			t.Errorf("push-back changed %s", name)
		}
	}
}
