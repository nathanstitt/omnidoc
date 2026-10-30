// Package xlsx reads and edits SpreadsheetML (.xlsx) workbooks.
//
// The reader (Open/OpenBytes → Workbook) extracts each sheet's CACHED cell
// values — the last value Excel computed, stored in the file — as display
// strings and typed values, along with the presentation facts a document
// conversion needs (fonts, fills, alignment, borders, merged ranges,
// conditional formats, comments). Formulas are not evaluated: the cached <v>
// is the value.
//
// The editor (Edit/New → File, with SheetEdit per worksheet) is
// preservation-first: Edit followed by Save of an untouched workbook is
// part-for-part byte-identical, and an edit re-serializes only the XML parts
// it dirtied, keeping unknown elements, attributes, and prefixes intact. It
// writes typed cells, formulas with cached values, style patches, conditional
// formats, comments, pivot tables, defined names, merges, and frozen panes.
//
// Both are hand-rolled on archive/zip plus encoding/xml (the editor rewrites
// dirty parts through the raw-fidelity DOM in github.com/beevik/etree),
// mirroring pkg/docx. It is a SUPPORTED PUBLIC API consumed outside this
// repository — changes from here are additive only.
package xlsx

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"
)

// ErrNotXLSX reports that the input is not a SpreadsheetML package.
var ErrNotXLSX = errors.New("not an xlsx file")

// maxPartSize caps any single decompressed part, mirroring pkg/docx.
const maxPartSize = 256 << 20

// Workbook is a parsed .xlsx: its sheets in workbook order plus the
// workbook-level facts an editor or importer needs.
type Workbook struct {
	// Sheets holds every sheet in the workbook, including hidden ones (the
	// Hidden flag distinguishes them; conversion skips hidden sheets, but the
	// data is there for callers that want it).
	Sheets []Sheet
	// Date1904 selects the 1904 date system (old Mac Excel) for serial-date
	// conversion. Value.T already accounts for it, but every serial in the
	// file — Value.F of a KindDate, and a date-valued formula cache — counts
	// from 1904-01-01. A 1900-based engine converts with Serial1900, and back
	// with SerialFrom1900 before writing a serial with SetNumber.
	Date1904 bool
	// DefinedNames are the workbook's defined names in file order.
	DefinedNames []DefinedName
	// DefaultFont is the workbook default font: the font of the Normal cell
	// style (cellStyleXfs record of the builtinId-0 cellStyle, normally
	// fonts[0]). It is the zero Font when the workbook has no styles part.
	// A cell font record without a size or name resolves to this font's size
	// and name in Style.Font, and Sheet.DefaultColWidth derives from it.
	DefaultFont Font
}

// DefinedName is one workbook-defined name (a named range or expression).
type DefinedName struct {
	Name string
	// RefersTo is the definition text (e.g. "Sheet1!$A$1:$B$4").
	RefersTo string
	// LocalSheet is the zero-based sheet index the name is scoped to, or nil
	// for a workbook-global name.
	LocalSheet *int
	Hidden     bool
}

// Visibility is a sheet's view state.
type Visibility int

const (
	// SheetVisible is the normal state.
	SheetVisible Visibility = iota
	// SheetHidden is user-hidden (unhide available in the UI).
	SheetHidden
	// SheetVeryHidden is hidden beyond the UI (VBA/state="veryHidden").
	SheetVeryHidden
)

// Sheet is one worksheet's used range as a dense rectangular grid.
type Sheet struct {
	Name string
	// Visibility is the sheet's view state; a document conversion renders only
	// SheetVisible sheets by default.
	Visibility Visibility
	// Cells is the used range, row-major, padded rectangular. Empty trailing
	// rows/columns beyond the used range are not represented.
	Cells [][]Cell
	// Merges are the sheet's merged ranges in file order.
	Merges []Merge
	// TabColorRGB is the sheet tab color as "RRGGBB", or "".
	TabColorRGB string
	// FrozenRows/FrozenCols are the frozen-pane split counts (0 = no freeze).
	FrozenRows, FrozenCols int
	// HideGridLines reports the first sheetView's showGridLines="0": the
	// sheet hides its gridlines. False (the zero value, and the OOXML
	// default) shows them. The editor reads and writes the same fact as
	// SheetEdit.ShowGridLines/SetShowGridLines.
	HideGridLines bool
	// RowHeights maps a ZERO-based row index to its explicit height in points
	// (only rows that declare one).
	RowHeights map[int]float64
	// RowStyles maps a zero-based row index to its row-level style (only rows
	// with a custom format).
	RowStyles map[int]*Style
	// ColWidths maps a zero-based column index to its explicit width in Excel
	// character units (<col> ranges expanded per column).
	ColWidths map[int]float64
	// HiddenRows holds the zero-based index of each row with hidden="1": rows
	// a filter hid and rows hidden by hand or by a collapsed outline group. A
	// hidden row keeps its RowHeights entry, the height it shows at once
	// unhidden. The editor reads and writes the same fact as
	// SheetEdit.RowHidden/SetRowHidden.
	HiddenRows map[int]bool
	// HiddenCols holds the zero-based index of each column a <col
	// hidden="1"> range covers (ranges expanded per column, like ColWidths).
	// The editor reads and writes the same fact as
	// SheetEdit.ColHidden/SetColHidden.
	HiddenCols map[int]bool
	// DefaultRowHeight is the sheetFormatPr defaultRowHeight in points, or
	// zero when the sheet declares none.
	DefaultRowHeight float64
	// DefaultColWidth is the default column width in character units (the
	// unit of ColWidths: widths of the default font's widest digit, padding
	// included). It is sheetFormatPr defaultColWidth verbatim when declared;
	// otherwise it is derived from baseColWidth (8 when absent) and
	// Workbook.DefaultFont as Excel does — baseColWidth digits plus 5 px of
	// padding, rounded up to a multiple of 8 px — so it is never zero. For
	// Calibri 11 or Arial 10 that is 9.140625 (Excel's UI shows "8.43", which
	// excludes the padding).
	DefaultColWidth float64
	// CondFmts are the sheet's conditional-formatting blocks, with each rule
	// carrying both its typed fields and the verbatim XML (CFRule.Raw) for
	// lossless passthrough.
	CondFmts []ConditionalFormatting
	// Comments are the sheet's classic cell notes (1-based coordinates).
	Comments []Comment
	// AutoFilter is the sheet's filter range and column criteria, or nil.
	// The matching hidden _xlnm._FilterDatabase name is in
	// Workbook.DefinedNames.
	AutoFilter *AutoFilter
	// Hyperlinks are the sheet's cell hyperlinks in file order.
	Hyperlinks []Hyperlink
}

// Cell is one cell's display value plus the presentation facts conversion
// uses, and — for structured consumers — its typed value, formula, and full
// resolved style.
type Cell struct {
	// Text is the display string: the cached value rendered through the cell's
	// number format (dates and times included).
	Text string

	// Value is the typed cached value (KindEmpty for a padding cell).
	Value Value
	// Formula is the cell's formula source without the leading "=", or "".
	// Shared formulas are expanded: each member cell carries the master
	// formula with its relative references shifted to the member's position.
	Formula string
	// StyleID is the cell's xf index into the styles part (0 = default).
	StyleID int
	// Style is the fully resolved style, shared (read-only) per xf index. It
	// is never nil: a workbook with no styles part, or a cell whose xf is out
	// of range, carries the zero Style. Presentation facts a conversion needs
	// live here — Style.Font.Bold/Italic, Style.Fill (Pattern "solid" with
	// Fg.RGB set is a solid colour), Style.Alignment.Horizontal.
	Style *Style
}

// Kind classifies a typed cell value.
type Kind uint8

const (
	// KindEmpty is an empty (padding) cell.
	KindEmpty Kind = iota
	// KindString is text (shared, inline, or a formula's cached string).
	KindString
	// KindNumber is a numeric value (F holds it).
	KindNumber
	// KindBool is a boolean (B holds it).
	KindBool
	// KindDate is a numeric serial whose number format is a date/time (F holds
	// the raw serial; T the converted time).
	KindDate
	// KindError is a cached error value ("#DIV/0!"; S holds the text).
	KindError
)

// Value is a cell's typed cached value. Exactly the fields implied by Kind are
// meaningful.
type Value struct {
	Kind Kind
	// S is the string value (KindString) or error text (KindError).
	S string
	// F is the numeric value (KindNumber) or raw date serial (KindDate), as
	// stored: in a Workbook.Date1904 workbook a date serial is 1904-based (see
	// Serial1900).
	F float64
	// B is the boolean value (KindBool).
	B bool
	// T is the converted date/time (KindDate), in UTC.
	T time.Time
}

// Style is a fully resolved cell format: every styles.xml facet the toolkit
// models. NumFmt is the resolved pattern (custom code, else the complete
// builtin table); rendering-oriented consumers keep using Cell.Text.
type Style struct {
	Font      Font
	Fill      Fill
	Alignment Alignment
	Border    Border
	NumFmtID  int
	// NumFmt is the resolved format pattern, or "" for General.
	NumFmt string
	// Protection is the cell-protection facet, or nil when the xf declares none.
	Protection *Protection
}

// Font is the font facet of a style.
type Font struct {
	Bold, Italic, Strike bool
	// Underline is the u val ("single", "double", ...) or "" for none.
	Underline string
	// Size is the font size in points (0 = unset).
	Size float64
	// Name is the font family name, or "".
	Name  string
	Color Color
}

// Fill is the fill facet: Pattern is the patternType ("solid", "gray125",
// ...; "" = none), with the pattern's foreground/background colors.
type Fill struct {
	Pattern string
	Fg, Bg  Color
}

// Alignment is the alignment facet.
type Alignment struct {
	// Horizontal/Vertical are the declared alignments ("" = default).
	Horizontal, Vertical string
	WrapText             bool
	Indent               int
	TextRotation         int
	ShrinkToFit          bool
}

// Border is the border facet: four edges plus the diagonal.
type Border struct {
	Top, Right, Bottom, Left, Diagonal Edge
	DiagonalUp, DiagonalDown           bool
}

// Edge is one border edge: the OOXML style name ("thin", "medium", "thick",
// "dashed", "dotted", "double", "hair", ...; "" = no border) and its color.
type Edge struct {
	Style string
	Color Color
}

// Protection is the cell-protection facet.
type Protection struct {
	Locked, Hidden bool
}

// Color is a styles color in whichever scheme the file used: an explicit RGB,
// an indexed-palette reference, or a theme reference with tint. Auto is the
// "automatic" color.
type Color struct {
	// RGB is "RRGGBB" when the color is stored explicitly, else "".
	RGB string
	// Indexed is the legacy palette index, or nil.
	Indexed *int
	// Theme is the theme color index, or nil.
	Theme *int
	// Tint applies to Theme (-1..1).
	Tint float64
	Auto bool
}

// Merge is a merged range: the origin cell and its extent (in cells, >= 1).
type Merge struct {
	Row, Col         int
	RowSpan, ColSpan int
}

// Open reads and parses the workbook at path. ctx bounds the parse: the
// os.ReadFile itself is not interruptible, but every sheet and every row within
// a sheet checks for cancellation, so a hostile workbook can be abandoned
// instead of wedging the caller. A nil ctx means context.Background().
func Open(ctx context.Context, pathName string) (*Workbook, error) {
	data, err := os.ReadFile(pathName)
	if err != nil {
		return nil, err
	}
	return OpenBytes(ctx, data)
}

// OpenBytes parses a workbook from an in-memory byte slice. See Open for what
// ctx bounds.
func OpenBytes(ctx context.Context, data []byte) (*Workbook, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotXLSX, err)
	}
	pkg := &pkgReader{parts: map[string]*zip.File{}}
	for _, f := range zr.File {
		pkg.parts[cleanPart(f.Name)] = f
	}
	if _, ok := pkg.parts["[Content_Types].xml"]; !ok {
		return nil, fmt.Errorf("%w: missing [Content_Types].xml", ErrNotXLSX)
	}
	return parseWorkbook(ctx, pkg)
}

// pkgReader resolves and reads OPC parts.
type pkgReader struct {
	parts map[string]*zip.File
}

// read returns a part's bytes, or nil if absent or oversized.
func (p *pkgReader) read(name string) []byte {
	f, ok := p.parts[cleanPart(name)]
	if !ok {
		return nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close() //nolint:errcheck // read-only part
	data, err := io.ReadAll(io.LimitReader(rc, maxPartSize+1))
	if err != nil || len(data) > maxPartSize {
		return nil
	}
	return data
}

// cleanPart normalizes an OPC part name (forward slashes, no leading slash).
func cleanPart(name string) string {
	return strings.TrimPrefix(path.Clean(strings.ReplaceAll(name, `\`, "/")), "/")
}

// relationship is one .rels entry.
type relationship struct {
	ID     string `xml:"Id,attr"`
	Type   string `xml:"Type,attr"`
	Target string `xml:"Target,attr"`
	// TargetMode is "External" for a target outside the package (a URL);
	// such a target is kept verbatim, not resolved as a part name.
	TargetMode string `xml:"TargetMode,attr"`
}

// external reports whether the relationship points outside the package.
func (r relationship) external() bool { return r.TargetMode == "External" }

// relsOf unmarshals a part's .rels file (dir/_rels/base.rels), keyed by id.
func (p *pkgReader) relsOf(partName string) map[string]relationship {
	dir, base := path.Split(cleanPart(partName))
	data := p.read(dir + "_rels/" + base + ".rels")
	if data == nil {
		return nil
	}
	var doc struct {
		Rels []relationship `xml:"Relationship"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	out := make(map[string]relationship, len(doc.Rels))
	for _, r := range doc.Rels {
		// A hyperlink's target is a URL (or a link relative to the file),
		// never a package part: keep it as written, as the editor does.
		if !r.external() && !isHyperlinkRel(r.Type) {
			r.Target = joinPart(dir, r.Target)
		}
		out[r.ID] = r
	}
	return out
}

// firstRelOfType returns the first relationship of the given type from a
// parsed rels map... order in a map is unstable, so scan the raw list instead.
func (p *pkgReader) firstRelOfType(partName, relType string) (relationship, bool) {
	dir, base := path.Split(cleanPart(partName))
	data := p.read(dir + "_rels/" + base + ".rels")
	if data == nil {
		return relationship{}, false
	}
	var doc struct {
		Rels []relationship `xml:"Relationship"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return relationship{}, false
	}
	for _, r := range doc.Rels {
		if r.Type == relType {
			r.Target = joinPart(dir, r.Target)
			return r, true
		}
	}
	return relationship{}, false
}

// joinPart resolves a rels target relative to the source part's directory.
func joinPart(dir, target string) string {
	target = strings.ReplaceAll(target, `\`, "/")
	if strings.HasPrefix(target, "/") {
		return cleanPart(target)
	}
	return cleanPart(path.Join(dir, target))
}

// cancelled reports ctx's error, wrapped with the package prefix, or nil.
func cancelled(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("xlsx: %w", err)
	}
	return nil
}
