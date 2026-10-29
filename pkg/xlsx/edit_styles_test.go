package xlsx

import (
	"context"
	"strings"
	"testing"

	genxlsx "github.com/nathanstitt/omnidoc/testdata/gen/xlsx"
)

func strp(s string) *string     { return &s }
func boolp(b bool) *bool        { return &b }
func floatp(f float64) *float64 { return &f }

// saveAndRead saves the editor and reopens through the enriched reader.
func saveAndRead(t *testing.T, f *File) *Workbook {
	t.Helper()
	out, err := f.Save(context.Background())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	wb, err := OpenBytes(context.Background(), out)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	return wb
}

// TestPatchCellStyleCanaries is the calc-parity audit: every StylePatch leaf,
// applied alone to a fresh workbook, survives Save → reader resolution — the
// per-leaf round-trip contract a style overlay depends on.
func TestPatchCellStyleCanaries(t *testing.T) {
	cases := []struct {
		name   string
		patch  StylePatch
		verify func(t *testing.T, st *Style)
	}{
		{"font.bold", StylePatch{Font: &FontPatch{Bold: boolp(true)}},
			func(t *testing.T, st *Style) {
				if !st.Font.Bold {
					t.Error("bold lost")
				}
			}},
		{"font.italic", StylePatch{Font: &FontPatch{Italic: boolp(true)}},
			func(t *testing.T, st *Style) {
				if !st.Font.Italic {
					t.Error("italic lost")
				}
			}},
		{"font.strike", StylePatch{Font: &FontPatch{Strike: boolp(true)}},
			func(t *testing.T, st *Style) {
				if !st.Font.Strike {
					t.Error("strike lost")
				}
			}},
		{"font.underline", StylePatch{Font: &FontPatch{Underline: strp("double")}},
			func(t *testing.T, st *Style) {
				if st.Font.Underline != "double" {
					t.Errorf("underline = %q", st.Font.Underline)
				}
			}},
		{"font.size", StylePatch{Font: &FontPatch{Size: floatp(16.5)}},
			func(t *testing.T, st *Style) {
				if st.Font.Size != 16.5 {
					t.Errorf("size = %g", st.Font.Size)
				}
			}},
		{"font.name", StylePatch{Font: &FontPatch{Name: strp("Georgia")}},
			func(t *testing.T, st *Style) {
				if st.Font.Name != "Georgia" {
					t.Errorf("name = %q", st.Font.Name)
				}
			}},
		{"font.color", StylePatch{Font: &FontPatch{Color: strp("AA0011")}},
			func(t *testing.T, st *Style) {
				if st.Font.Color.RGB != "AA0011" {
					t.Errorf("color = %+v", st.Font.Color)
				}
			}},
		{"fill.solid", StylePatch{Fill: &FillPatch{Pattern: strp("solid"), Fg: strp("FFEE00")}},
			func(t *testing.T, st *Style) {
				if st.Fill.Pattern != "solid" || st.Fill.Fg.RGB != "FFEE00" {
					t.Errorf("fill = %+v", st.Fill)
				}
			}},
		{"fill.bg", StylePatch{Fill: &FillPatch{Pattern: strp("solid"), Fg: strp("222222"), Bg: strp("111111")}},
			func(t *testing.T, st *Style) {
				if st.Fill.Bg.RGB != "111111" {
					t.Errorf("bg = %+v", st.Fill.Bg)
				}
			}},
		{"alignment.h", StylePatch{Alignment: &AlignmentPatch{Horizontal: strp("center")}},
			func(t *testing.T, st *Style) {
				if st.Alignment.Horizontal != "center" {
					t.Errorf("h = %q", st.Alignment.Horizontal)
				}
			}},
		{"alignment.v", StylePatch{Alignment: &AlignmentPatch{Vertical: strp("top")}},
			func(t *testing.T, st *Style) {
				if st.Alignment.Vertical != "top" {
					t.Errorf("v = %q", st.Alignment.Vertical)
				}
			}},
		{"alignment.wrap", StylePatch{Alignment: &AlignmentPatch{WrapText: boolp(true)}},
			func(t *testing.T, st *Style) {
				if !st.Alignment.WrapText {
					t.Error("wrap lost")
				}
			}},
		{"border.edges", StylePatch{Border: &BorderPatch{
			Top:    &EdgePatch{Style: strp("thin"), Color: strp("111111")},
			Right:  &EdgePatch{Style: strp("double")},
			Bottom: &EdgePatch{Style: strp("dashed"), Color: strp("222222")},
			Left:   &EdgePatch{Style: strp("dotted")},
		}},
			func(t *testing.T, st *Style) {
				b := st.Border
				if b.Top.Style != "thin" || b.Top.Color.RGB != "111111" ||
					b.Right.Style != "double" || b.Bottom.Style != "dashed" || b.Left.Style != "dotted" {
					t.Errorf("border = %+v", b)
				}
			}},
		{"numfmt.custom", StylePatch{NumFmt: strp(`0.000"kg"`)},
			func(t *testing.T, st *Style) {
				if st.NumFmt != `0.000"kg"` || st.NumFmtID < 164 {
					t.Errorf("numFmt = %d %q", st.NumFmtID, st.NumFmt)
				}
			}},
		{"numfmt.builtin-reuse", StylePatch{NumFmt: strp("0.00%")},
			func(t *testing.T, st *Style) {
				if st.NumFmtID != 10 {
					t.Errorf("builtin percent should reuse id 10, got %d", st.NumFmtID)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := New()
			sh, err := f.Sheet("Sheet1")
			if err != nil {
				t.Fatal(err)
			}
			if err := sh.SetNumber(1, 1, 1); err != nil {
				t.Fatal(err)
			}
			if err := sh.PatchCellStyle(1, 1, c.patch); err != nil {
				t.Fatal(err)
			}
			// The editor-side read agrees before saving.
			editorView := sh.CellStyle(1, 1)
			c.verify(t, &editorView)

			wb := saveAndRead(t, f)
			st := wb.Sheets[0].Cells[0][0].Style
			if st == nil {
				t.Fatal("no resolved style after round trip")
			}
			c.verify(t, st)
		})
	}
}

// TestPatchClears covers the explicit clear semantics: *"" leaves, border
// Clear, wrap false, numFmt back to General.
func TestPatchClears(t *testing.T) {
	f := New()
	sh, err := f.Sheet("Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.SetNumber(1, 1, 1); err != nil {
		t.Fatal(err)
	}
	full := StylePatch{
		Font:      &FontPatch{Bold: boolp(true), Underline: strp("single"), Color: strp("AA0011"), Name: strp("Georgia")},
		Fill:      &FillPatch{Pattern: strp("solid"), Fg: strp("FFEE00")},
		Alignment: &AlignmentPatch{Horizontal: strp("center"), WrapText: boolp(true)},
		Border:    &BorderPatch{Top: &EdgePatch{Style: strp("thin")}},
		NumFmt:    strp("0.00%"),
	}
	if err := sh.PatchCellStyle(1, 1, full); err != nil {
		t.Fatal(err)
	}
	clear := StylePatch{
		Font:      &FontPatch{Bold: boolp(false), Underline: strp(""), Color: strp(""), Name: strp("")},
		Fill:      &FillPatch{Pattern: strp("")},
		Alignment: &AlignmentPatch{Horizontal: strp(""), WrapText: boolp(false)},
		Border:    &BorderPatch{Top: &EdgePatch{Clear: true}},
		NumFmt:    strp(""),
	}
	if err := sh.PatchCellStyle(1, 1, clear); err != nil {
		t.Fatal(err)
	}
	wb := saveAndRead(t, f)
	st := wb.Sheets[0].Cells[0][0].Style
	if st == nil {
		t.Fatal("no style")
	}
	// A cleared name drops the <name> element, so the font shows in the
	// workbook default family (Calibri in a New workbook), as Excel does.
	if st.Font.Bold || st.Font.Underline != "" || st.Font.Color.RGB != "" || st.Font.Name != "Calibri" {
		t.Errorf("font not cleared: %+v", st.Font)
	}
	if st.Fill.Pattern != "none" && st.Fill.Pattern != "" {
		t.Errorf("fill not cleared: %+v", st.Fill)
	}
	if st.Alignment.Horizontal != "" || st.Alignment.WrapText {
		t.Errorf("alignment not cleared: %+v", st.Alignment)
	}
	if st.Border.Top.Style != "" {
		t.Errorf("border not cleared: %+v", st.Border.Top)
	}
	if st.NumFmtID != 0 {
		t.Errorf("numFmt not cleared: %d", st.NumFmtID)
	}
}

// TestPatchPreservesUnmodeledFacets is the overlay contract excelize cannot
// express cleanly: a cell whose xf carries facets the patch vocabulary does
// not model — diagonal borders, indent, text rotation, protection — keeps
// them through a Font.Bold patch, verified via the reader AND the raw XML.
func TestPatchPreservesUnmodeledFacets(t *testing.T) {
	src := genxlsx.New().
		AddSheet("S", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
 <sheetData><row r="1"><c r="A1" s="1"><v>7</v></c></row></sheetData>
</worksheet>`).
		SetStyles(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
 <fonts count="1"><font><sz val="11"/><name val="Calibri"/><scheme val="minor"/></font></fonts>
 <fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>
 <borders count="2"><border/><border diagonalUp="1"><left/><right/><top/><bottom/><diagonal style="hair"><color rgb="FF999999"/></diagonal></border></borders>
 <cellXfs count="2">
  <xf numFmtId="0" fontId="0" fillId="0" borderId="0"/>
  <xf numFmtId="0" fontId="0" fillId="0" borderId="1"><alignment indent="3" textRotation="45"/><protection locked="0"/></xf>
 </cellXfs>
</styleSheet>`).
		Bytes()
	f, err := Edit(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := f.Sheet("S")
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.PatchCellStyle(1, 1, StylePatch{Font: &FontPatch{Bold: boolp(true)}}); err != nil {
		t.Fatal(err)
	}
	out, err := f.Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	wb, err := OpenBytes(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	st := wb.Sheets[0].Cells[0][0].Style
	if st == nil || !st.Font.Bold {
		t.Fatalf("patch did not land: %+v", st)
	}
	if st.Border.Diagonal.Style != "hair" || !st.Border.DiagonalUp {
		t.Errorf("diagonal border lost: %+v", st.Border)
	}
	if st.Alignment.Indent != 3 || st.Alignment.TextRotation != 45 {
		t.Errorf("alignment extras lost: %+v", st.Alignment)
	}
	if st.Protection == nil || st.Protection.Locked {
		t.Errorf("protection lost: %+v", st.Protection)
	}
	// The unmodeled font scheme child rides along in the cloned font record.
	_, parts := zipParts(t, out)
	styles := string(parts["xl/styles.xml"])
	if !strings.Contains(styles, `<scheme val="minor"/>`) {
		t.Errorf("font scheme child lost:\n%s", styles)
	}
	if !strings.Contains(styles, `indent="3"`) || !strings.Contains(styles, `textRotation="45"`) {
		t.Errorf("alignment attrs lost:\n%s", styles)
	}
}

// TestPatchDedupes pins record reuse: identical patches on two cells share
// one xf/font; an identical-result patch does not grow the tables.
func TestPatchDedupes(t *testing.T) {
	f := New()
	sh, err := f.Sheet("Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.SetNumber(1, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := sh.SetNumber(2, 1, 2); err != nil {
		t.Fatal(err)
	}
	bold := StylePatch{Font: &FontPatch{Bold: boolp(true)}}
	if err := sh.PatchCellStyle(1, 1, bold); err != nil {
		t.Fatal(err)
	}
	if err := sh.PatchCellStyle(2, 1, bold); err != nil {
		t.Fatal(err)
	}
	// Applying the same patch again to the SAME cell changes nothing.
	if err := sh.PatchCellStyle(1, 1, bold); err != nil {
		t.Fatal(err)
	}
	if a, b := sh.Cell(1, 1).StyleID, sh.Cell(2, 1).StyleID; a != b {
		t.Errorf("identical patches yielded different xfs: %d vs %d", a, b)
	}
	out, err := f.Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, parts := zipParts(t, out)
	styles := string(parts["xl/styles.xml"])
	if got := strings.Count(styles, "<b/>"); got != 1 {
		t.Errorf("bold font records = %d, want 1 (deduped)\n%s", got, styles)
	}
}

// TestRowStylePatch covers the row-level style path.
func TestRowStylePatch(t *testing.T) {
	f := New()
	sh, err := f.Sheet("Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.PatchRowStyle(2, StylePatch{Font: &FontPatch{Italic: boolp(true)}}); err != nil {
		t.Fatal(err)
	}
	if st, ok := sh.RowStyle(2); !ok || !st.Font.Italic {
		t.Errorf("RowStyle = %+v, %v", st, ok)
	}
	wb := saveAndRead(t, f)
	if st := wb.Sheets[0].RowStyles[1]; st == nil || !st.Font.Italic {
		t.Errorf("reader row style = %+v", st)
	}

	f2, err := Edit(context.Background(), mustSave(t, f))
	if err != nil {
		t.Fatal(err)
	}
	sh2, err := f2.Sheet("Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	sh2.ClearRowStyle(2)
	wb2 := saveAndRead(t, f2)
	if len(wb2.Sheets[0].RowStyles) != 0 {
		t.Errorf("row style not cleared: %+v", wb2.Sheets[0].RowStyles)
	}
}

// TestSetCellStyleWhole covers the replacement setter.
func TestSetCellStyleWhole(t *testing.T) {
	f := New()
	sh, err := f.Sheet("Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.SetNumber(1, 1, 5); err != nil {
		t.Fatal(err)
	}
	idx := 12
	st := Style{
		Font:      Font{Bold: true, Size: 14, Name: "Georgia", Color: Color{RGB: "336699"}},
		Fill:      Fill{Pattern: "solid", Fg: Color{RGB: "FFF2A8"}, Bg: Color{Indexed: &idx}},
		Alignment: Alignment{Horizontal: "right", WrapText: true, Indent: 2},
		Border: Border{Top: Edge{Style: "medium", Color: Color{RGB: "111111"}},
			Diagonal: Edge{Style: "hair"}, DiagonalUp: true},
		NumFmt:     "0.00",
		Protection: &Protection{Locked: false, Hidden: true},
	}
	if err := sh.SetCellStyle(1, 1, st); err != nil {
		t.Fatal(err)
	}
	wb := saveAndRead(t, f)
	got := wb.Sheets[0].Cells[0][0].Style
	if got == nil {
		t.Fatal("no style")
	}
	if !got.Font.Bold || got.Font.Size != 14 || got.Font.Name != "Georgia" || got.Font.Color.RGB != "336699" {
		t.Errorf("font = %+v", got.Font)
	}
	if got.Fill.Pattern != "solid" || got.Fill.Fg.RGB != "FFF2A8" || got.Fill.Bg.Indexed == nil || *got.Fill.Bg.Indexed != 12 {
		t.Errorf("fill = %+v", got.Fill)
	}
	if got.Alignment.Horizontal != "right" || !got.Alignment.WrapText || got.Alignment.Indent != 2 {
		t.Errorf("alignment = %+v", got.Alignment)
	}
	if got.Border.Top.Style != "medium" || got.Border.Diagonal.Style != "hair" || !got.Border.DiagonalUp {
		t.Errorf("border = %+v", got.Border)
	}
	if got.NumFmtID != 2 { // "0.00" is builtin id 2 — the reverse lookup reuses it
		t.Errorf("numFmtId = %d, want the builtin 2", got.NumFmtID)
	}
	if got.Protection == nil || got.Protection.Locked || !got.Protection.Hidden {
		t.Errorf("protection = %+v", got.Protection)
	}
}

// TestCustomNumFmtReuse pins reuse-by-code across two allocations.
func TestCustomNumFmtReuse(t *testing.T) {
	f := New()
	sh, err := f.Sheet("Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.SetNumber(1, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := sh.SetNumber(1, 2, 2); err != nil {
		t.Fatal(err)
	}
	code := `#,##0.0" units"`
	if err := sh.PatchCellStyle(1, 1, StylePatch{NumFmt: strp(code)}); err != nil {
		t.Fatal(err)
	}
	if err := sh.PatchCellStyle(1, 2, StylePatch{NumFmt: strp(code), Font: &FontPatch{Bold: boolp(true)}}); err != nil {
		t.Fatal(err)
	}
	wb := saveAndRead(t, f)
	a, b := wb.Sheets[0].Cells[0][0].Style, wb.Sheets[0].Cells[0][1].Style
	if a.NumFmtID != b.NumFmtID || a.NumFmtID < 164 {
		t.Errorf("custom ids = %d vs %d, want one shared id >= 164", a.NumFmtID, b.NumFmtID)
	}
	if a.NumFmt != code || b.NumFmt != code {
		t.Errorf("codes = %q / %q", a.NumFmt, b.NumFmt)
	}
}

// mustSave is a test helper returning saved bytes.
func mustSave(t *testing.T, f *File) []byte {
	t.Helper()
	out, err := f.Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return out
}
