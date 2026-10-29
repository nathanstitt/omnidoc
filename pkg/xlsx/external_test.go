package xlsx

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// externalWorkbooks pins each committed real-world fixture's shape as read by
// this package — sheet count and the feature surfaces the calc adoption
// depends on (conditional formats, notes, pivots, autoFilters, hyperlinks) —
// verified at download time.
// Producers span Excel, Mac Excel, LibreOffice 5.4/25.2, and an ISO-strict
// workbook; see testdata/external/xlsx/README.md for provenance and licensing.
var externalWorkbooks = []struct {
	file                                      string
	sheets, cf, notes, pivots, filters, links int
}{
	{"50784-font_theme_colours.xlsx", 3, 0, 0, 0, 0, 0},
	{"55406_Conditional_formatting_sample.xlsx", 1, 2, 0, 0, 0, 0},
	{"ExcelPivotTableSample.xlsx", 3, 0, 0, 2, 0, 0},
	{"NewStyleConditionalFormattings.xlsx", 1, 18, 0, 0, 0, 0},
	{"SimpleWithComments.xlsx", 3, 0, 3, 0, 0, 0},
	{"Themes.xlsx", 1, 0, 0, 0, 0, 0},
	{"WithConditionalFormatting.xlsx", 3, 3, 0, 0, 0, 0},
	{"WithVariousData.xlsx", 3, 0, 2, 0, 0, 1},
	{"autofilter.xlsx", 1, 0, 0, 0, 1, 0},
	{"cell-note.xlsx", 1, 0, 1, 0, 0, 0},
	{"filter_type.xlsx", 3, 0, 0, 0, 0, 0}, // ISO-strict namespaces
}

// TestExternalCorpusPreservation enforces the editor's preservation contract
// against real Excel- and LibreOffice-authored workbooks: the reader opens
// each file and sees its pinned features, a no-op Edit+Save is part-for-part
// BYTE-IDENTICAL, and a real cell edit still reopens through the reader.
// Skips if the corpus is absent (a sparse checkout).
func TestExternalCorpusPreservation(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "external", "xlsx")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("external xlsx corpus not present")
	}
	for _, tc := range externalWorkbooks {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, tc.file))
			if err != nil {
				t.Skipf("fixture missing: %v", err)
			}

			// The reader sees the pinned shape.
			wb, err := OpenBytes(context.Background(), data)
			if err != nil {
				t.Fatalf("OpenBytes: %v", err)
			}
			if len(wb.Sheets) != tc.sheets {
				t.Errorf("sheets = %d, want %d", len(wb.Sheets), tc.sheets)
			}
			cf, notes, filters, links := 0, 0, 0, 0
			for _, s := range wb.Sheets {
				cf += len(s.CondFmts)
				notes += len(s.Comments)
				links += len(s.Hyperlinks)
				if s.AutoFilter != nil {
					filters++
				}
			}
			if filters != tc.filters {
				t.Errorf("autoFilters = %d, want %d", filters, tc.filters)
			}
			if links != tc.links {
				t.Errorf("hyperlinks = %d, want %d", links, tc.links)
			}
			if cf != tc.cf {
				t.Errorf("conditional formats = %d, want %d", cf, tc.cf)
			}
			if notes != tc.notes {
				t.Errorf("notes = %d, want %d", notes, tc.notes)
			}

			// The pivot read path agrees on the editor view.
			f, err := Edit(context.Background(), data)
			if err != nil {
				t.Fatalf("Edit: %v", err)
			}
			if got := len(f.PivotTables()); got != tc.pivots {
				t.Errorf("pivots = %d, want %d", got, tc.pivots)
			}

			// The preservation contract: a no-op Edit+Save is part-for-part
			// byte-identical (reads never dirty).
			out, err := f.Save(context.Background())
			if err != nil {
				t.Fatalf("no-op Save: %v", err)
			}
			wantNames, wantParts := zipParts(t, data)
			gotNames, gotParts := zipParts(t, out)
			if len(wantNames) != len(gotNames) {
				t.Fatalf("part count changed: %d -> %d", len(wantNames), len(gotNames))
			}
			for _, name := range wantNames {
				if !bytes.Equal(wantParts[name], gotParts[name]) {
					t.Errorf("no-op save changed part %s", name)
				}
			}

			// A real edit still reopens through the reader.
			f2, err := Edit(context.Background(), data)
			if err != nil {
				t.Fatalf("Edit: %v", err)
			}
			sh, err := f2.Sheet(f2.SheetNames()[0])
			if err != nil {
				t.Fatalf("Sheet: %v", err)
			}
			if err := sh.SetString(1, 1, "edited by test"); err != nil {
				t.Fatalf("SetString: %v", err)
			}
			edited, err := f2.Save(context.Background())
			if err != nil {
				t.Fatalf("Save after edit: %v", err)
			}
			if _, err := OpenBytes(context.Background(), edited); err != nil {
				t.Errorf("edited workbook does not reopen: %v", err)
			}
		})
	}
}

// TestExternalCalcFacts checks the autoFilter and hyperlink values in the
// real-world corpus (Excel- and POI-authored), not only their counts.
func TestExternalCalcFacts(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "external", "xlsx")
	open := func(name string) *Workbook {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Skipf("fixture missing: %v", err)
		}
		wb, err := OpenBytes(context.Background(), data)
		if err != nil {
			t.Fatal(err)
		}
		return wb
	}
	af := open("autofilter.xlsx").Sheets[0].AutoFilter
	if af == nil || af.Ref != "A1:C5" || len(af.Columns) != 1 || af.Columns[0].ColID != 2 ||
		af.Columns[0].Custom == nil || !af.Columns[0].Custom.And ||
		len(af.Columns[0].Custom.Filters) != 1 || af.Columns[0].Custom.Filters[0] != (CustomFilter{"equal", "4"}) {
		t.Errorf("autofilter.xlsx filter = %+v", af)
	}
	var links []Hyperlink
	for _, s := range open("WithVariousData.xlsx").Sheets {
		links = append(links, s.Hyperlinks...)
	}
	if len(links) != 1 || links[0] != (Hyperlink{Ref: "C4", URL: "http://poi.apache.org/"}) {
		t.Errorf("WithVariousData.xlsx links = %+v", links)
	}
}

// TestExternalPushBackIsNoOp is calc's save loop on real workbooks: writing
// back every sheet's gridlines, autoFilter (nil for most), and hyperlinks
// exactly as read changes no part.
func TestExternalPushBackIsNoOp(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "external", "xlsx")
	for _, tc := range externalWorkbooks {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, tc.file))
			if err != nil {
				t.Skipf("fixture missing: %v", err)
			}
			f, err := Edit(context.Background(), data)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range f.SheetNames() {
				sh, err := f.Sheet(name)
				if err != nil {
					t.Fatal(err)
				}
				sh.SetShowGridLines(sh.ShowGridLines())
				if err := sh.SetAutoFilter(sh.AutoFilter()); err != nil {
					t.Fatalf("%s: SetAutoFilter: %v", name, err)
				}
				if err := sh.SetHyperlinks(sh.Hyperlinks()); err != nil {
					t.Fatalf("%s: SetHyperlinks: %v", name, err)
				}
			}
			out, err := f.Save(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			_, want := zipParts(t, data)
			_, got := zipParts(t, out)
			for name := range want {
				if !bytes.Equal(want[name], got[name]) {
					t.Errorf("push-back changed %s", name)
				}
			}
		})
	}
}
