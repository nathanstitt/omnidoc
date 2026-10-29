package xlsx

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/beevik/etree"
	"github.com/nathanstitt/omnidoc/pkg/xlsx/internal/xmlpart"
)

// Editor sentinels, for callers to branch on via errors.Is.
var (
	// ErrSheetNotFound reports a sheet name with no worksheet.
	ErrSheetNotFound = errors.New("xlsx: sheet not found")
	// ErrLastVisibleSheet reports an operation that would leave the workbook
	// with no visible sheet (delete or hide of the last one).
	ErrLastVisibleSheet = errors.New("xlsx: a workbook must keep at least one visible sheet")
	// ErrBadRef reports an out-of-range row/column (rows and columns are 1-based).
	ErrBadRef = errors.New("xlsx: bad cell reference")
)

// File is a PRESERVATION-FIRST xlsx editor: Edit opens existing bytes, the
// mutators change only what they name, and Save re-emits the package with
// every untouched part copied byte-verbatim at the zip layer (unknown parts —
// themes, drawings, extension lists — survive without being modeled). Parts
// the edits touch are re-serialized through the raw-fidelity XML layer
// (internal/xmlpart on beevik/etree), which preserves unknown elements and
// attributes in order.
//
// A File is a single-goroutine mutable editor (unlike the read-only Workbook,
// it must not be shared without synchronization). Rows and columns are
// 1-based throughout — the spreadsheet convention, and the editor's contract
// with A1 references.
type File struct {
	zr *zip.Reader
	// parsed caches part trees; parsing alone does NOT dirty a part — reads
	// must leave the byte-verbatim copy path intact.
	parsed map[string]*xmlpart.Part
	// dirty marks parts whose trees must re-serialize at Save.
	dirty map[string]bool
	// added maps a new part name to its raw bytes (a parsed tree marked dirty
	// takes precedence at Save).
	added map[string][]byte
	// deleted marks original parts dropped from the output.
	deleted map[string]bool
	// sheets caches SheetEdit handles by sheet name.
	sheets map[string]*SheetEdit
	// calcInvalidated records that a value/formula edit dropped xl/calcChain.xml.
	calcInvalidated bool
	// shared caches the shared-string table for typed reads.
	shared     []string
	sharedInit bool
	// styleGen counts styles.xml mutations; styleCache/styleCacheGen memoize
	// the resolved style table against it (see resolvedStyles).
	styleGen      int
	styleCacheGen int
	styleCache    styleTable
}

// Edit opens xlsx bytes for in-place modification. The data is retained (the
// zip reader indexes into it) and must not be mutated by the caller.
//
// Edit only indexes the package; parts are parsed lazily by the accessors
// (Sheet, DefinedNames, ...), so ctx bounds the open itself and nothing after
// it. Save takes its own context for the serialization. A nil ctx means
// context.Background().
func Edit(ctx context.Context, data []byte) (*File, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := cancelled(ctx); err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotXLSX, err)
	}
	f := &File{
		zr:            zr,
		parsed:        map[string]*xmlpart.Part{},
		dirty:         map[string]bool{},
		added:         map[string][]byte{},
		deleted:       map[string]bool{},
		sheets:        map[string]*SheetEdit{},
		styleCacheGen: -1,
	}
	if f.rawPart("xl/workbook.xml") == nil {
		return nil, fmt.Errorf("%w: missing xl/workbook.xml", ErrNotXLSX)
	}
	return f, nil
}

// New returns an editor over a minimal blank workbook: one visible "Sheet1",
// the default style table, deterministic output. It is the writer-first
// starting point (and regenerates blank-workbook templates).
func New() *File {
	f, err := Edit(context.Background(), blankWorkbook())
	if err != nil {
		panic("xlsx: the built-in blank workbook must open: " + err.Error())
	}
	return f
}

// blankWorkbook assembles the minimal valid package New edits.
func blankWorkbook() []byte {
	parts := []struct{ name, data string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>
`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>
`},
		{"xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>
`},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>
`},
		{"xl/worksheets/sheet1.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData/></worksheet>
`},
		{"xl/styles.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellXfs></styleSheet>
`},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	stamp := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, p := range parts {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Deflate, Modified: stamp})
		if err != nil {
			panic(err) // deterministic in-memory build
		}
		if _, err := w.Write([]byte(p.data)); err != nil {
			panic(err)
		}
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// rawPart returns a part's current bytes: an added part, else the original.
func (f *File) rawPart(name string) []byte {
	if data, ok := f.added[name]; ok {
		return data
	}
	for _, zf := range f.zr.File {
		if zf.Name == name {
			rc, err := zf.Open()
			if err != nil {
				return nil
			}
			defer func() { _ = rc.Close() }()
			data, err := io.ReadAll(io.LimitReader(rc, maxPartSize+1))
			if err != nil || len(data) > maxPartSize {
				return nil
			}
			return data
		}
	}
	return nil
}

// part returns a part's parsed tree for READING — parsing alone never dirties
// a part, so untouched parts keep their byte-verbatim copy at Save.
func (f *File) part(name string) (*xmlpart.Part, error) {
	if p, ok := f.parsed[name]; ok {
		return p, nil
	}
	data := f.rawPart(name)
	if data == nil {
		return nil, fmt.Errorf("xlsx: missing part %s", name)
	}
	p, err := xmlpart.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("xlsx: part %s: %w", name, err)
	}
	f.parsed[name] = p
	return p, nil
}

// mutatePart returns a part's tree for MUTATION, marking it dirty.
func (f *File) mutatePart(name string) (*xmlpart.Part, error) {
	p, err := f.part(name)
	if err != nil {
		return nil, err
	}
	f.dirty[name] = true
	return p, nil
}

// setPart REPLACES a part's content wholesale (parsed through the fidelity
// layer so Save's dirty path emits it, whether the part is an original entry
// or a new one).
func (f *File) setPart(name string, data []byte) error {
	p, err := xmlpart.Parse(data)
	if err != nil {
		return fmt.Errorf("xlsx: part %s: %w", name, err)
	}
	f.parsed[name] = p
	f.dirty[name] = true
	delete(f.deleted, name) // a rewritten part is alive again
	if _, ok := f.added[name]; !ok && !f.originalPart(name) {
		f.added[name] = data
	}
	return nil
}

// originalPart reports whether name exists in the source zip.
func (f *File) originalPart(name string) bool {
	for _, zf := range f.zr.File {
		if zf.Name == name {
			return true
		}
	}
	return false
}

// Save serializes the workbook: untouched original parts are copied
// byte-verbatim (raw compressed streams — a no-op Edit+Save round-trip is
// part-for-part byte-identical), dirty parts re-serialize through xmlpart,
// added parts append in sorted order with a fixed timestamp.
//
// ctx is checked before each part is written, so a large workbook's
// serialization can be abandoned. A nil ctx means context.Background().
func (f *File) Save(ctx context.Context) ([]byte, error) {
	var buf bytes.Buffer
	if err := f.SaveTo(ctx, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SaveTo streams Save's output to w.
func (f *File) SaveTo(ctx context.Context, w io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	zw := zip.NewWriter(w)
	written := map[string]bool{}
	for _, zf := range f.zr.File {
		if err := cancelled(ctx); err != nil {
			return err
		}
		name := zf.Name
		if f.deleted[name] || written[name] {
			continue
		}
		written[name] = true
		if f.dirty[name] {
			data, err := f.parsed[name].Bytes()
			if err != nil {
				return fmt.Errorf("xlsx: serialize %s: %w", name, err)
			}
			out, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: zf.Modified})
			if err != nil {
				return fmt.Errorf("xlsx: create %s: %w", name, err)
			}
			if _, err := out.Write(data); err != nil {
				return fmt.Errorf("xlsx: write %s: %w", name, err)
			}
			continue
		}
		if err := zw.Copy(zf); err != nil {
			return fmt.Errorf("xlsx: copy %s: %w", name, err)
		}
	}
	names := make([]string, 0, len(f.added))
	for name := range f.added {
		if !written[name] && !f.deleted[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	stamp := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range names {
		if err := cancelled(ctx); err != nil {
			return err
		}
		data := f.added[name]
		if f.dirty[name] {
			var err error
			data, err = f.parsed[name].Bytes()
			if err != nil {
				return fmt.Errorf("xlsx: serialize %s: %w", name, err)
			}
		}
		out, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: stamp})
		if err != nil {
			return fmt.Errorf("xlsx: create %s: %w", name, err)
		}
		if _, err := out.Write(data); err != nil {
			return fmt.Errorf("xlsx: write %s: %w", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("xlsx: close package: %w", err)
	}
	return nil
}

// invalidateCalc drops xl/calcChain.xml (and its references) the first time a
// cell value or formula changes — the Excel-sanctioned response to editing
// without recomputing; Excel rebuilds the chain on open. Leaving a stale
// chain in place can crash consumers on mismatched references.
func (f *File) invalidateCalc() {
	if f.calcInvalidated {
		return
	}
	f.calcInvalidated = true
	const chain = "xl/calcChain.xml"
	if f.rawPart(chain) == nil {
		return
	}
	f.deleted[chain] = true
	if ct, err := f.mutatePart("[Content_Types].xml"); err == nil {
		root := ct.Root()
		for _, ch := range xmlpart.Children(root, "Override") {
			if ch.SelectAttrValue("PartName", "") == "/"+chain {
				xmlpart.Remove(root, ch)
			}
		}
	}
	if rels, err := f.mutatePart("xl/_rels/workbook.xml.rels"); err == nil {
		root := rels.Root()
		for _, ch := range xmlpart.Children(root, "Relationship") {
			if ch.SelectAttrValue("Target", "") == "calcChain.xml" {
				xmlpart.Remove(root, ch)
			}
		}
	}
}

// workbookSheets returns the workbook part and its sheet elements.
func (f *File) workbookSheets() (*xmlpart.Part, []*etree.Element, error) {
	wb, err := f.part("xl/workbook.xml")
	if err != nil {
		return nil, nil, err
	}
	sheetsEl := xmlpart.FindChild(wb.Root(), "sheets")
	if sheetsEl == nil {
		return nil, nil, fmt.Errorf("%w: workbook has no sheets element", ErrNotXLSX)
	}
	return wb, sheetsEl.ChildElements(), nil
}

// SheetNames lists the sheets in workbook order.
func (f *File) SheetNames() []string {
	_, sheets, err := f.workbookSheets()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(sheets))
	for _, s := range sheets {
		names = append(names, s.SelectAttrValue("name", ""))
	}
	return names
}

// Date1904 reports the workbook's date system.
func (f *File) Date1904() bool {
	wb, err := f.part("xl/workbook.xml")
	if err != nil {
		return false
	}
	if pr := xmlpart.FindChild(wb.Root(), "workbookPr"); pr != nil {
		return onOff(pr.SelectAttrValue("date1904", ""))
	}
	return false
}

// sheetElement finds a sheet's workbook element by name.
func (f *File) sheetElement(name string) (*etree.Element, error) {
	_, sheets, err := f.workbookSheets()
	if err != nil {
		return nil, err
	}
	for _, s := range sheets {
		if s.SelectAttrValue("name", "") == name {
			return s, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrSheetNotFound, name)
}

// sheetPartName resolves a sheet element's worksheet part via the workbook rels.
func (f *File) sheetPartName(sheetEl *etree.Element) (string, error) {
	rid := ""
	for _, a := range sheetEl.Attr {
		if a.Key == "id" { // r:id — match by local name
			rid = a.Value
		}
	}
	if rid == "" {
		return "", fmt.Errorf("%w: sheet %q has no relationship id", ErrNotXLSX, sheetEl.SelectAttrValue("name", ""))
	}
	rels, err := f.part("xl/_rels/workbook.xml.rels")
	if err != nil {
		return "", err
	}
	for _, ch := range xmlpart.Children(rels.Root(), "Relationship") {
		if ch.SelectAttrValue("Id", "") == rid {
			target := ch.SelectAttrValue("Target", "")
			if strings.HasPrefix(target, "/") {
				return strings.TrimPrefix(target, "/"), nil
			}
			return "xl/" + target, nil
		}
	}
	return "", fmt.Errorf("%w: sheet relationship %s unresolved", ErrNotXLSX, rid)
}

// Sheet returns the editor handle for a sheet by name.
func (f *File) Sheet(name string) (*SheetEdit, error) {
	if s, ok := f.sheets[name]; ok {
		return s, nil
	}
	sheetEl, err := f.sheetElement(name)
	if err != nil {
		return nil, err
	}
	partName, err := f.sheetPartName(sheetEl)
	if err != nil {
		return nil, err
	}
	s := &SheetEdit{file: f, partName: partName, name: name}
	f.sheets[name] = s
	return s, nil
}

// visibleCount counts sheets without a hidden state.
func (f *File) visibleCount() int {
	_, sheets, err := f.workbookSheets()
	if err != nil {
		return 0
	}
	n := 0
	for _, s := range sheets {
		if s.SelectAttrValue("state", "") == "" || s.SelectAttrValue("state", "") == "visible" {
			n++
		}
	}
	return n
}

// AddSheet appends a new empty worksheet and returns its editor handle.
func (f *File) AddSheet(name string) (*SheetEdit, error) {
	if _, err := f.sheetElement(name); err == nil {
		return nil, fmt.Errorf("xlsx: sheet %q already exists", name)
	}
	wb, sheets, err := f.workbookSheets()
	if err != nil {
		return nil, err
	}
	// Allocate the next free part name, sheetId, and relationship id.
	partNum := 1
	for f.partExists(fmt.Sprintf("xl/worksheets/sheet%d.xml", partNum)) {
		partNum++
	}
	partName := fmt.Sprintf("xl/worksheets/sheet%d.xml", partNum)
	maxID := 0
	for _, s := range sheets {
		if id, err := strconv.Atoi(s.SelectAttrValue("sheetId", "0")); err == nil && id > maxID {
			maxID = id
		}
	}
	rels, err := f.mutatePart("xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, err
	}
	maxRel := 0
	for _, ch := range xmlpart.Children(rels.Root(), "Relationship") {
		id := ch.SelectAttrValue("Id", "")
		if strings.HasPrefix(id, "rId") {
			if n, err := strconv.Atoi(id[3:]); err == nil && n > maxRel {
				maxRel = n
			}
		}
	}
	rid := fmt.Sprintf("rId%d", maxRel+1)

	f.added[partName] = []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData/></worksheet>
`)
	rel := etree.NewElement("Relationship")
	rel.CreateAttr("Id", rid)
	rel.CreateAttr("Type", "http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet")
	rel.CreateAttr("Target", strings.TrimPrefix(partName, "xl/"))
	rels.Root().AddChild(rel)

	sheetEl := etree.NewElement("sheet")
	sheetEl.CreateAttr("name", name)
	sheetEl.CreateAttr("sheetId", strconv.Itoa(maxID+1))
	sheetEl.CreateAttr(relIDAttr(wb.Root()), rid)
	xmlpart.FindChild(wb.Root(), "sheets").AddChild(sheetEl)
	f.dirty["xl/workbook.xml"] = true

	if ct, err := f.mutatePart("[Content_Types].xml"); err == nil {
		over := etree.NewElement("Override")
		over.CreateAttr("PartName", "/"+partName)
		over.CreateAttr("ContentType", "application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml")
		ct.Root().AddChild(over)
	}
	return f.Sheet(name)
}

// partExists reports whether a part is present (original not deleted, or added).
func (f *File) partExists(name string) bool {
	if f.deleted[name] {
		return false
	}
	if _, ok := f.added[name]; ok {
		return true
	}
	for _, zf := range f.zr.File {
		if zf.Name == name {
			return true
		}
	}
	return false
}

// DeleteSheet removes a sheet: its workbook entry, relationship, content-type
// override, and part. Deleting the last visible sheet is refused.
func (f *File) DeleteSheet(name string) error {
	sheetEl, err := f.sheetElement(name)
	if err != nil {
		return err
	}
	visible := sheetEl.SelectAttrValue("state", "") == "" || sheetEl.SelectAttrValue("state", "") == "visible"
	if visible && f.visibleCount() <= 1 {
		return ErrLastVisibleSheet
	}
	partName, err := f.sheetPartName(sheetEl)
	if err != nil {
		return err
	}
	rid := sheetEl.SelectAttrValue("id", "")
	idx, err := f.sheetIndex(name)
	if err != nil {
		return err
	}

	wb, _, err := f.workbookSheets()
	if err != nil {
		return err
	}
	xmlpart.Remove(xmlpart.FindChild(wb.Root(), "sheets"), sheetEl)
	f.dirty["xl/workbook.xml"] = true
	// Sheet-local names follow their sheet: the deleted sheet's go (as in
	// Excel), later sheets' indices shift down by one.
	f.remapLocalSheetIDs(func(old int) int {
		switch {
		case old == idx:
			return -1
		case old > idx:
			return old - 1
		}
		return old
	})
	// The active and first-visible tab indices shift the same way; a deleted
	// active tab passes to the sheet that took its place (the last one when
	// it was last).
	remaining := len(xmlpart.Children(xmlpart.FindChild(wb.Root(), "sheets"), "sheet"))
	f.remapBookViews(func(old int) int {
		switch {
		case old > idx:
			return old - 1
		case old == idx:
			return min(idx, remaining-1)
		}
		return old
	})

	if rels, err := f.mutatePart("xl/_rels/workbook.xml.rels"); err == nil {
		for _, ch := range xmlpart.Children(rels.Root(), "Relationship") {
			if ch.SelectAttrValue("Id", "") == rid {
				xmlpart.Remove(rels.Root(), ch)
			}
		}
	}
	if ct, err := f.mutatePart("[Content_Types].xml"); err == nil {
		for _, ch := range xmlpart.Children(ct.Root(), "Override") {
			if ch.SelectAttrValue("PartName", "") == "/"+partName {
				xmlpart.Remove(ct.Root(), ch)
			}
		}
	}
	f.deleted[partName] = true
	delete(f.added, partName)
	delete(f.dirty, partName)
	delete(f.parsed, partName)
	delete(f.sheets, name)
	return nil
}

// MoveSheet reorders a sheet to the given zero-based workbook position.
func (f *File) MoveSheet(name string, index int) error {
	sheetEl, err := f.sheetElement(name)
	if err != nil {
		return err
	}
	wb, sheets, err := f.workbookSheets()
	if err != nil {
		return err
	}
	if index < 0 || index >= len(sheets) {
		return fmt.Errorf("%w: sheet index %d of %d", ErrBadRef, index, len(sheets))
	}
	before := make(map[*etree.Element]int, len(sheets))
	for i, el := range sheets {
		before[el] = i
	}
	sheetsEl := xmlpart.FindChild(wb.Root(), "sheets")
	xmlpart.Remove(sheetsEl, sheetEl)
	remaining := sheetsEl.ChildElements()
	if index >= len(remaining) {
		sheetsEl.AddChild(sheetEl)
	} else {
		xmlpart.InsertBefore(sheetsEl, sheetEl, remaining[index])
	}
	f.dirty["xl/workbook.xml"] = true
	// Sheet-local names keep pointing at the same sheet at its new index.
	moved := map[int]int{}
	for i, el := range xmlpart.Children(sheetsEl, "sheet") {
		moved[before[el]] = i
	}
	remap := func(old int) int {
		if n, ok := moved[old]; ok {
			return n
		}
		return old
	}
	f.remapLocalSheetIDs(remap)
	// The active tab stays on the same sheet. firstSheet (the first tab shown
	// in the tab bar) follows its sheet too.
	f.remapBookViews(remap)
	return nil
}

// remapBookViews rewrites every workbookView's activeTab and firstSheet
// through remap. An absent attribute counts as index 0 (the schema default);
// an attribute remap leaves unchanged is not touched.
func (f *File) remapBookViews(remap func(old int) int) {
	wb, err := f.part("xl/workbook.xml")
	if err != nil {
		return
	}
	views := xmlpart.FindChild(wb.Root(), "bookViews")
	if views == nil {
		return
	}
	for _, view := range xmlpart.Children(views, "workbookView") {
		for _, attr := range []string{"activeTab", "firstSheet"} {
			raw := view.SelectAttrValue(attr, "")
			old, err := strconv.Atoi(raw)
			if raw == "" {
				old, err = 0, nil
			}
			if err != nil {
				continue
			}
			n := max(remap(old), 0)
			if n == old {
				continue
			}
			view.CreateAttr(attr, strconv.Itoa(n))
			f.dirty["xl/workbook.xml"] = true
		}
	}
}

// remapLocalSheetIDs rewrites every sheet-local defined name's localSheetId
// through remap (old index to new; a negative result drops the name). Names
// remap leaves alone are not touched.
func (f *File) remapLocalSheetIDs(remap func(old int) int) {
	wb, err := f.part("xl/workbook.xml")
	if err != nil {
		return
	}
	dn := xmlpart.FindChild(wb.Root(), "definedNames")
	if dn == nil {
		return
	}
	for _, el := range xmlpart.Children(dn, "definedName") {
		old, err := strconv.Atoi(el.SelectAttrValue("localSheetId", ""))
		if err != nil {
			continue
		}
		switch n := remap(old); {
		case n < 0:
			xmlpart.Remove(dn, el)
			f.dirty["xl/workbook.xml"] = true
		case n != old:
			el.CreateAttr("localSheetId", strconv.Itoa(n))
			f.dirty["xl/workbook.xml"] = true
		}
	}
	if len(dn.ChildElements()) == 0 {
		xmlpart.Remove(wb.Root(), dn)
	}
}

// sharedString resolves a shared-string index for typed reads, parsing the
// table once.
func (f *File) sharedString(idx int) string {
	if !f.sharedInit {
		f.sharedInit = true
		f.shared = parseSharedStrings(f.rawPart("xl/sharedStrings.xml"))
	}
	if idx < 0 || idx >= len(f.shared) {
		return ""
	}
	return f.shared[idx]
}
