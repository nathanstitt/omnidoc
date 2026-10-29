package xlsx

import (
	"math"
	"strings"
)

// defaultBaseColWidth is the schema default of sheetFormatPr@baseColWidth,
// in characters.
const defaultBaseColWidth = 8

// defaultFontSize is the size assumed when the default font declares none
// (Excel's Calibri 11).
const defaultFontSize = 11

// digitWidthEm is each font family's widest digit advance ("0"–"9") as a
// fraction of the em, measured from the font files (the Liberation, Carlito,
// Caladea, Arimo, Tinos, and Cousine families are metric-compatible with the
// fonts they stand in for). Keys are lowercase family names.
var digitWidthEm = map[string]float64{
	"calibri":          1038.0 / 2048,
	"carlito":          1038.0 / 2048,
	"arial":            1139.0 / 2048,
	"helvetica":        1139.0 / 2048,
	"liberation sans":  1139.0 / 2048,
	"arimo":            1139.0 / 2048,
	"times new roman":  1024.0 / 2048,
	"liberation serif": 1024.0 / 2048,
	"tinos":            1024.0 / 2048,
	"courier new":      1229.0 / 2048,
	"liberation mono":  1229.0 / 2048,
	"cousine":          1229.0 / 2048,
	"cambria":          517.0 / 1000,
	"caladea":          517.0 / 1000,
	"verdana":          1302.0 / 2048,
	"tahoma":           1118.0 / 2048,
	"georgia":          1257.0 / 2048,
	"trebuchet ms":     1074.0 / 2048,
	"comic sans ms":    1250.0 / 2048,
	"roboto":           1151.0 / 2048,
	"open sans":        1171.0 / 2048,
}

// maxDigitWidthPx is the default font's maximum digit width in whole pixels
// at 96 DPI — the unit Excel's column widths count in. A family missing from
// digitWidthEm uses Calibri's ratio (no logger exists on this path; the
// approximation is documented in FEATURES.md).
func maxDigitWidthPx(f Font) float64 {
	size := f.Size
	if size <= 0 {
		size = defaultFontSize
	}
	ratio, ok := digitWidthEm[strings.ToLower(strings.TrimSpace(f.Name))]
	if !ok {
		ratio = digitWidthEm["calibri"]
	}
	px := math.Round(size * 96 / 72 * ratio)
	if px < 1 {
		px = 1
	}
	return px
}

// derivedDefaultColWidth is the default column width, in characters, of a
// sheet that declares no defaultColWidth. ECMA-376 §18.3.1.81 defines it as
// baseColWidth plus 5 pixels of padding (2 per side and 1 of gridline);
// Excel then rounds the pixel width up to a multiple of 8 (Microsoft KB
// 214123). The result is truncated to 1/256 of a character like every
// stored width. For Calibri 11 and Arial 10 (7-pixel digits) with the
// default baseColWidth of 8, that is 64 px = 9.140625 characters, the
// "8.43" Excel shows in its UI.
func derivedDefaultColWidth(baseColWidth int, defaultFont Font) float64 {
	mdw := maxDigitWidthPx(defaultFont)
	px := float64(baseColWidth)*mdw + 5
	px = math.Ceil(px/8) * 8
	return math.Floor(px/mdw*256) / 256
}

// excelDefaultRowHeight is the default row height, in points, Excel uses
// for a default font: 15 for Calibri 11 and 12.75 for Arial 10, its two
// standard defaults. Excel derives the height from hinted font metrics this
// package does not have, so any other font gets 15 — the height of Excel's
// current default. It is used only when a sheetFormatPr must be created and
// the schema requires a height.
func excelDefaultRowHeight(f Font) float64 {
	if strings.EqualFold(strings.TrimSpace(f.Name), "arial") && f.Size == 10 {
		return 12.75
	}
	return 15
}
