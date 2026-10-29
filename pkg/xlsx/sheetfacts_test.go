package xlsx

import (
	"bytes"
	"context"
	"strings"
	"testing"

	genxlsx "github.com/nathanstitt/omnidoc/testdata/gen/xlsx"
)

const wsOpen = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">`

// gridFixture has a sheet with hidden gridlines (plus a pane and selection
// that must survive edits), one with an explicit "1", and one with no view.
func gridFixture() []byte {
	return genxlsx.New().
		AddSheet("Hidden", wsOpen+`<sheetViews><sheetView showGridLines="0" tabSelected="1" workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/><selection pane="bottomLeft" activeCell="B3" sqref="B3"/></sheetView></sheetViews><sheetData/></worksheet>`).
		AddSheet("Shown", wsOpen+`<sheetViews><sheetView showGridLines="1" workbookViewId="0"/></sheetViews><sheetData/></worksheet>`).
		AddSheet("NoView", wsOpen+`<sheetData/></worksheet>`).
		Bytes()
}

// TestShowGridLinesRead pins both views: showGridLines="0" reads false; an
// explicit "1" and an absent view read true (the OOXML default).
func TestShowGridLinesRead(t *testing.T) {
	data := gridFixture()
	wb, err := OpenBytes(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"Hidden": false, "Shown": true, "NoView": true}
	for _, s := range wb.Sheets {
		if s.HideGridLines == want[s.Name] {
			t.Errorf("reader %s: HideGridLines = %v, want %v", s.Name, s.HideGridLines, !want[s.Name])
		}
	}
	f, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	for name, w := range want {
		sh, err := f.Sheet(name)
		if err != nil {
			t.Fatal(err)
		}
		if got := sh.ShowGridLines(); got != w {
			t.Errorf("editor %s: ShowGridLines() = %v, want %v", name, got, w)
		}
	}
}

// TestSetShowGridLines covers the write: showing removes the attribute and
// keeps the pane and selection; hiding a sheet with no view creates one; a
// call that changes nothing leaves the part byte-verbatim.
func TestSetShowGridLines(t *testing.T) {
	data := gridFixture()
	f, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	hidden, _ := f.Sheet("Hidden")
	hidden.SetShowGridLines(true)
	noView, _ := f.Sheet("NoView")
	noView.SetShowGridLines(false)
	shown, _ := f.Sheet("Shown")
	shown.SetShowGridLines(true) // already shown: no-op

	out := mustSave(t, f)
	_, parts := zipParts(t, out)
	_, orig := zipParts(t, data)
	h := string(parts["xl/worksheets/sheet1.xml"])
	if strings.Contains(h, "showGridLines") {
		t.Errorf("showing kept the attribute: %s", h)
	}
	for _, keep := range []string{`tabSelected="1"`, `<pane ySplit="1"`, `<selection pane="bottomLeft" activeCell="B3"`} {
		if !strings.Contains(h, keep) {
			t.Errorf("lost %s: %s", keep, h)
		}
	}
	if nv := string(parts["xl/worksheets/sheet3.xml"]); !strings.Contains(nv, `<sheetViews><sheetView workbookViewId="0" showGridLines="0"/></sheetViews><sheetData/>`) {
		t.Errorf("hiding did not create the view before sheetData: %s", nv)
	}
	if !bytes.Equal(parts["xl/worksheets/sheet2.xml"], orig["xl/worksheets/sheet2.xml"]) {
		t.Error("a no-op SetShowGridLines changed the part")
	}

	wb, err := OpenBytes(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if wb.Sheets[0].HideGridLines || !wb.Sheets[2].HideGridLines {
		t.Errorf("reopened HideGridLines = %v/%v, want false/true", wb.Sheets[0].HideGridLines, wb.Sheets[2].HideGridLines)
	}
	if wb.Sheets[0].FrozenRows != 1 {
		t.Errorf("pane lost: FrozenRows = %d", wb.Sheets[0].FrozenRows)
	}
}

// fontFixtureStyles makes the Normal style point at fonts[1] (Arial 10), so
// the default font is NOT fonts[0], and gives xf 1 a font with a name but no
// size and xf 2 a font with neither.
const fontFixtureStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
 <fonts count="4"><font><sz val="11"/><name val="Calibri"/></font><font><sz val="10"/><color rgb="FF000000"/><name val="Arial"/></font><font><b/><name val="Arial"/></font><font><i/></font></fonts>
 <fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>
 <borders count="1"><border/></borders>
 <cellStyleXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0"/></cellStyleXfs>
 <cellXfs count="3"><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="1"/><xf numFmtId="0" fontId="2" fillId="0" borderId="0" xfId="1"/><xf numFmtId="0" fontId="3" fillId="0" borderId="0" xfId="1"/></cellXfs>
 <cellStyles count="2"><cellStyle name="Calibri Body" xfId="0"/><cellStyle name="Normal" xfId="1" builtinId="0"/></cellStyles>
</styleSheet>`

func fontFixture() []byte {
	return genxlsx.New().
		AddSheet("S", wsOpen+`<sheetData><row r="1"><c r="A1"><v>1</v></c><c r="B1" s="1"><v>2</v></c><c r="C1" s="2"><v>3</v></c></row></sheetData></worksheet>`).
		SetStyles(fontFixtureStyles).
		Bytes()
}

// TestDefaultFont pins the Normal-style resolution (builtinId 0 →
// cellStyleXfs → fonts) and that a cell font without sz/name shows in the
// default font's size and family.
func TestDefaultFont(t *testing.T) {
	data := fontFixture()
	wb, err := OpenBytes(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if wb.DefaultFont.Name != "Arial" || wb.DefaultFont.Size != 10 || wb.DefaultFont.Color.RGB != "000000" {
		t.Errorf("DefaultFont = %+v, want Arial 10 black", wb.DefaultFont)
	}
	row := wb.Sheets[0].Cells[0]
	if f := row[1].Style.Font; f.Name != "Arial" || f.Size != 10 || !f.Bold {
		t.Errorf("B1 font (name, no sz) = %+v, want bold Arial 10", f)
	}
	if f := row[2].Style.Font; f.Name != "Arial" || f.Size != 10 || !f.Italic {
		t.Errorf("C1 font (no name, no sz) = %+v, want italic Arial 10", f)
	}

	ed, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if got := ed.DefaultFont(); got != wb.DefaultFont {
		t.Errorf("editor DefaultFont = %+v, want %+v", got, wb.DefaultFont)
	}
	sh, _ := ed.Sheet("S")
	if got := sh.CellStyle(1, 2).Font; got != row[1].Style.Font {
		t.Errorf("editor B1 font = %+v, want %+v", got, row[1].Style.Font)
	}
}

// TestDefaultFontFallbacks covers a styles part with no cellStyles (the
// Normal style is cellStyleXfs record 0) and one with no cellStyleXfs
// (fonts[0]).
func TestDefaultFontFallbacks(t *testing.T) {
	noCellStyles := strings.Replace(fontFixtureStyles, `<cellStyles count="2"><cellStyle name="Calibri Body" xfId="0"/><cellStyle name="Normal" xfId="1" builtinId="0"/></cellStyles>`, "", 1)
	noStyleXfs := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="1"><font><sz val="12"/><name val="Georgia"/></font></fonts><cellXfs count="1"><xf fontId="0"/></cellXfs></styleSheet>`
	for _, tc := range []struct {
		styles string
		want   Font
	}{
		{noCellStyles, Font{Size: 11, Name: "Calibri"}},
		{noStyleXfs, Font{Size: 12, Name: "Georgia"}},
		{"", Font{}},
	} {
		data := genxlsx.New().AddSheet("S", wsOpen+`<sheetData/></worksheet>`).SetStyles(tc.styles).Bytes()
		wb, err := OpenBytes(context.Background(), data)
		if err != nil {
			t.Fatal(err)
		}
		if wb.DefaultFont != tc.want {
			t.Errorf("DefaultFont = %+v, want %+v", wb.DefaultFont, tc.want)
		}
	}
}

// TestDerivedDefaultColWidth pins the ECMA-376 §18.3.1.81 width plus Excel's
// round-up to a multiple of 8 pixels, against the values Excel writes.
func TestDerivedDefaultColWidth(t *testing.T) {
	for _, tc := range []struct {
		base int
		font Font
		want float64
	}{
		// 7-px digits: 8*7+5 = 61 -> 64 px -> 64/7 truncated to 1/256.
		{8, Font{Name: "Calibri", Size: 11}, 9.140625},
		{8, Font{Name: "Arial", Size: 10}, 9.140625},
		{8, Font{}, 9.140625},                               // no font: Calibri 11
		{8, Font{Name: "No Such Font", Size: 11}, 9.140625}, // unknown: Calibri ratio
		// Arial 11: 8-px digits, 69 -> 72 px -> 9 characters.
		{8, Font{Name: "Arial", Size: 11}, 9},
		// baseColWidth 10, Calibri 11: 75 -> 80 px -> 11.428... -> 2925/256.
		{10, Font{Name: "Calibri", Size: 11}, 2925.0 / 256},
	} {
		if got := derivedDefaultColWidth(tc.base, tc.font); got != tc.want {
			t.Errorf("derivedDefaultColWidth(%d, %+v) = %v, want %v", tc.base, tc.font, got, tc.want)
		}
	}
}

// TestSheetDefaultColWidth: an explicit defaultColWidth is kept verbatim; a
// sheet with only baseColWidth, or with no sheetFormatPr, gets the derived
// width from the workbook default font.
func TestSheetDefaultColWidth(t *testing.T) {
	data := genxlsx.New().
		AddSheet("Explicit", wsOpen+`<sheetFormatPr defaultColWidth="14.43" defaultRowHeight="15.75"/><sheetData/></worksheet>`).
		AddSheet("BaseOnly", wsOpen+`<sheetFormatPr baseColWidth="10" defaultRowHeight="15"/><sheetData/></worksheet>`).
		AddSheet("None", wsOpen+`<sheetData/></worksheet>`).
		SetStyles(fontFixtureStyles). // default font Arial 10 (7-px digits)
		Bytes()
	wb, err := OpenBytes(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{14.43, 2925.0 / 256, 9.140625}
	for i, s := range wb.Sheets {
		if s.DefaultColWidth != want[i] {
			t.Errorf("%s: DefaultColWidth = %v, want %v", s.Name, s.DefaultColWidth, want[i])
		}
	}
}
