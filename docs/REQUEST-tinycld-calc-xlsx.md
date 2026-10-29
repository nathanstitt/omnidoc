# Request — pkg/xlsx features for tinycld calc

Requested by the tinycld `calc` package (`calc/server/bootstrap.go`, `pivot.go`, `persist.go`).
calc reads an xlsx with `xlsx.Read` / `xlsx.Edit` and writes it back with the `SheetEdit` editor.
Each item below is a fact that calc must read on import and keep on save. Today pkg/xlsx does not
give it to calc, so the fact is lost on import, on save, or both.

The reference file is a Google Sheets export (`workers-comp.xlsx`, kept by the calc team). Items
found later are added at the end of this file.

Status legend: ☐ open · ◐ in progress · ☑ done (released in version …).

## Status (2026-10-29)

- Items 1–9 are all implemented and in review in
  [PR #176](https://github.com/nathanstitt/omnidoc/pull/176) (branch `xlsx-calc-requests`). They
  are not merged or released. Each item stays ◐ until a release contains it.
- Each item has a **Built:** note where the API differs from the proposal.
- The anonymized reference export is committed as `testdata/xlsx/google-export.xlsx` (repository
  license). `pkg/xlsx/reference_test.go` pins items 1, 2, 4, and 5 against it, and also checks
  calc's save loop: writing back every fact as read leaves the file byte-identical, and both
  pivots survive remove-then-add unchanged. It checks:
  - Gridlines are off on sheets 2 and 3.
  - The autoFilter is `A1:BY35` and the frozen pane is 1×2.
  - The default font is Arial 10, and no cell font is left without a size.
  - The `x="-2"` placeholder reads as `ValuesAxis "col"`.
  - `numFmtId="164"` resolves to `"$"#,##0.00`.
  - C34 is 7800.87, BC34 is 420323.55, and BG38 is 425717.05.
  - BD2 is cached as `0.0152330746`, because Google rounds caches to ten digits. BC2/BC34 in full
    precision is 0.015233074615971435.
  - The pivot sheets have no cached output cells, so the 420323.55 grand total is checked as
    SUM(BC2:BC33).
- The check found one more gap, now fixed. A pivot re-add wrote 20 cache fields all named `" "`,
  and it named date headers `FieldN`. The new `PivotTable.CacheFields` keeps the cache names on
  re-add.
- The file also showed that padding cells (cells with no `<c>` element) had a nil `Style`. They
  now carry xf 0's style, so an unstyled cell draws in the default font (item 5).
- Added on the omnidoc side, which calc can use:
  - Writing back an unchanged fact (gridlines, autoFilter, hyperlinks) leaves the sheet part
    byte-identical. calc can push every sheet's facts on every save.
  - `MoveSheet`/`DeleteSheet` keep sheet-local defined names (such as `_FilterDatabase`) on their
    sheet.
- `SetName` rewrites the sheet's own `_FilterDatabase` (and print) names. Known gap: it does not
  rewrite the old sheet name in cell formulas or other defined names (omnidoc backlog X1).
- A review pass (tinycld) found 13 issues; all are fixed in PR #176. They include: a pivot with a
  calculated cache field can be re-added; a typed edit of a filter column read with `Raw` is
  written; and `SetAutoFilter(nil)` on a sheet without a filter keeps an Advanced Filter's name.

---

## 1. Sheet view: gridlines

- ◐ **Read and write `<sheetView showGridLines="0">`.**
  - Read: add `Sheet.ShowGridLines bool` (default `true` when the attribute is absent).
  - Write: add `SheetEdit.SetShowGridLines(bool)`. Keep all other `sheetView` attributes and
    children (`pane`, `selection`).
  - Reference file: sheets 2 and 3 set `showGridLines="0"`. calc shows gridlines on them.
  - **Built:** the read field is `Sheet.HideGridLines` (true for `showGridLines="0"`), so a
    zero-value `Sheet` shows gridlines. The editor has `SheetEdit.ShowGridLines()` and
    `SetShowGridLines(bool)` as proposed. The write changes every `sheetView` of the sheet.

## 2. AutoFilter

- ◐ **Read and write the sheet `<autoFilter>`.**
  - Read: add `Sheet.AutoFilter *AutoFilter` with `Ref string` (for example `A1:BY35`) and the
    `<filterColumn>` children: `ColID int`, the `<filters>` value list, `blank="1"`, and
    `<customFilters>` (operator and value). Keep unknown children as raw XML for passthrough.
  - Read the hidden `_xlnm._FilterDatabase` defined name. It is already in `DefinedNames`; no
    change is necessary if `AutoFilter` is given.
  - Write: add `SheetEdit.SetAutoFilter(*AutoFilter)` (nil removes it and the
    `_xlnm._FilterDatabase` name).
  - Reference file: `detail` has `<autoFilter ref="$A$1:$BY$35"/>`. Note the absolute (`$`)
    references — return the ref without `$`, or document that it can contain `$`.
  - **Built:** `Ref` never contains `$`. Columns are typed for every filter kind (values, blank,
    date groups, custom, top10, dynamic, color, icon), with `Raw` per column and `Extra` for
    `sortState`/`extLst`. The editor reader is `SheetEdit.AutoFilter()`.

## 3. Hyperlinks

- ◐ **Read and write cell hyperlinks (`<hyperlinks>` + the sheet rels).**
  - Read: add `Sheet.Hyperlinks []Hyperlink` with `Ref string`, `URL string` (resolved from the
    external relationship `r:id`), `Location string` (internal target, for example `Sheet2!A1`),
    `Display string`, and `Tooltip string`.
  - Write: add `SheetEdit.SetHyperlinks([]Hyperlink)`. It replaces the sheet's hyperlinks and
    adds or removes the `TargetMode="External"` relationships.
  - calc now stores a link per cell. Until this ships, a saved link is lost on the next load.
  - **Built:** as proposed, plus `SheetEdit.Hyperlinks()`. `Ref` has `$` stripped.

## 4. Pivot tables

- ◐ **Expose the "Values" placeholder on an axis.** In `<colFields><field x="-2"/></colFields>`
  (and in `rowFields`), `x="-2"` is not a cache field. It marks where the data-field captions go.
  Today `fieldName(-2)` returns `""` and calc gets an axis field with an empty name. Do not add
  it to `Rows`/`Cols`; add `PivotTable.ValuesAxis string` (`"col"` or `"row"`) and
  `ValuesPosition int`.
  - **Built:** as proposed. `ValuesPosition` is the number of axis fields before the
    placeholder. `AddPivotTable` writes it back.
- ◐ **Data field number format.** Read `<dataField numFmtId="…">` and, when it is absent, the
  `numFmtId` on the matching `<pivotField>`. Add `PivotValueField.NumFmt string` (the format code,
  resolved through the styles part the same way as `Style.NumFmt`). Reference file: every pivot
  field has `numFmtId="164"` (`"$"#,##0.00`).
  - Today calc falls back to the format of the first number cell in the source column. A value
    field over a text column (reference file: "SUM of Employee") has no number cell, so calc
    shows `0` where Excel and Google show `$0.00`.
  - **Built:** as proposed. `AddPivotTable` writes it as the dataField `numFmtId`. "SUM of
    Employee" reads `"$"#,##0.00` from its pivotField (pinned against the reference file).
- ◐ **Axis item sort order.** Read `<pivotField sortType="ascending|descending">` for axis fields.
  Add a `Sort string` to an axis-field type (today axis fields are plain `[]string`, so this
  needs a small struct, for example `PivotAxisField{Name, Sort string}`).
  - **Built:** `PivotTable.AxisSort map[string]string` (field name → `"ascending"` /
    `"descending"`). This keeps `Rows`/`Cols`/`Filters` as `[]string`, so the v1 API does not
    break.
- ◐ **Location metadata.** Read `<location firstHeaderRow firstDataRow firstDataCol>`. calc uses
  these to place the header band.
  - **Built:** `PivotTable.LocationInfo *PivotLocation` (also `rowPageCount`/`colPageCount`).
- ◐ **Show-empty / row header caption.** Read `rowHeaderCaption`, `colHeaderCaption`, and
  `dataCaption` from `<pivotTableDefinition>`.
  - **Built:** `DataCaption`, `RowHeaderCaption`, `ColHeaderCaption`, plus `GrandTotalCaption`,
    `MissingCaption`/`HideMissing` (the "for empty cells show" setting), and
    `ErrorCaption`/`ShowError`. All are written back.

## 5. Workbook default font

- ◐ **Expose the workbook default font.** Add `Workbook.DefaultFont Font` (the font of the
  `Normal` cell style, `<cellStyleXfs>` xf 0, normally `<fonts>` index 0).
  - When a cell font has no `<sz>` (reference file: fonts 5 has `name="Arial"` and no size), the
    resolved `Font.Size` is 0. Excel and Google show it at the default size (10pt in the
    reference file). Either resolve the missing size to the default in `Style.Font`, or give the
    default to calc so it can do it.
  - calc also needs the default font name and size to draw cells that have no style (xf 0).
  - **Built:** `Workbook.DefaultFont` and `File.DefaultFont()`. `Style.Font` fills a missing size
    AND a missing name from the default font.

## 6. Sheet default column width

- ◐ **Document the unit of `DefaultColWidth`.** It is the raw `defaultColWidth` attribute (character
  units). Confirm that a sheet with only `baseColWidth` returns the derived width
  (`baseColWidth + 5px padding` rule), not 0.
  - **Built:** the unit is characters (widths of the default font's widest digit). A sheet with
    only `baseColWidth`, or with no `sheetFormatPr`, now returns the derived width: the digits
    plus 5 px, rounded up to a multiple of 8 px as Excel does. Calibri 11 / Arial 10 → 9.140625.

## 7. Sheet reorder and sheet-scoped names

- ◐ **`MoveSheet` must remap sheet indexes.** After a move, a defined name's `localSheetId` and
  `<workbookView activeTab>` still hold the old index, so a sheet-scoped name can point at the
  wrong sheet. calc now reorders sheets on save (it inserts a sheet for an imported pivot).
  - **Built:** `MoveSheet` remaps `localSheetId`, `activeTab`, and `firstSheet`. `DeleteSheet`
    does too: the deleted sheet's local names go, and a deleted active tab passes to the sheet that
    takes its place.

## 8. Sheet default sizes on write

- ◐ **Add `SheetEdit.SetDefaultRowHeight(pt)` / `SetDefaultColWidth(chars)`** (writes
  `<sheetFormatPr>`). calc reads the defaults on import; it cannot save a change to them yet.
  - **Built:** as proposed, plus the readers `SheetEdit.DefaultRowHeight()`/`DefaultColWidth()`.
    A height is (0, 409] pt and is written with `customHeight="1"`. A width is (0, 255]
    characters. A sheet with no `sheetFormatPr` gets one with the required `defaultRowHeight`:
    12.75 for an Arial 10 workbook, else 15.

## 9. 1904 date system

- ◐ **Expose the date system per formula cache.** A `KindDate` cached value in a 1904 workbook is
  a 1904-system serial. calc's formula engine uses the 1900 system. Either give the serial in the
  1900 system, or keep `Workbook.Date1904` (already present) documented as the switch callers
  must apply to `Value.F`.
  - **Built:** `Value.F` stays the stored serial (documented on `Value.F` and
    `Workbook.Date1904`). New `xlsx.Serial1900(serial, date1904)` converts to the 1900 system, and
    `xlsx.SerialFrom1900` converts back before `SetNumber`. The offset is 1462 days. `Value.T`
    was already correct in both systems.

---

## Notes for later items

Add new items above this line, with the section number, the XML that carries the fact, the
API you propose, and the reference file location.
