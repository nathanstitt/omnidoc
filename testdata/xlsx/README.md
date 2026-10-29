# XLSX reference fixtures (repository license)

Unlike `testdata/external/xlsx/`, these files are under this repository's own MIT license.

| File | Origin | Exercises |
| --- | --- | --- |
| `google-export.xlsx` | tinycld calc's reference workbook (`calc/tests/assets/google-export.xlsx`, tinycld commit "test(calc): add anonymised Google Sheets export fixture"). It is a real Google Sheets export, with every person name and amount replaced. Formula caches and both pivot caches were recomputed from the new amounts. | The facts in `docs/REQUEST-tinycld-calc-xlsx.md`: `showGridLines="0"` on sheets 2 and 3, `autoFilter ref="$A$1:$BY$35"`, a 1×2 frozen pane, shared formulas, M/d/yy date headers, `numFmtId="164"` (`"$"#,##0.00`) on pivot fields, a pivot with `colFields x="-2"`, a pivot source with 20 `" "` headers (cache names `" "`, `" 2"`, …), and a font with a name but no size. |

`pkg/xlsx/reference_test.go` pins the values read from it. Google rounds a formula cache to ten
significant digits, so a cached quotient such as `detail!BD2` is `0.0152330746`, not the
full-precision `0.015233074615971435`.
