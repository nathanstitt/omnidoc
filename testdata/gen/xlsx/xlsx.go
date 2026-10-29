// Package xlsx generates deterministic .xlsx fixtures for tests, mirroring the
// DOCX generator (testdata/gen/docx): fixtures are readable Go, not opaque
// blobs, and two builds of the same fixture are byte-identical (fixed zip
// timestamps and part order).
package xlsx

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"time"
)

// Builder assembles a minimal valid SpreadsheetML package.
type Builder struct {
	sheets        []sheet
	sharedStrings string
	styles        string
	date1904      bool
	definedNames  string
	extra         []extraPart
}

// extraPart is a part beyond the workbook core (sheet rels, pivot parts, ...).
type extraPart struct {
	name, contentType, xml string
}

type sheet struct {
	name   string
	xml    string // the full worksheet XML (<worksheet>...</worksheet>)
	hidden bool
}

// New returns an empty workbook builder.
func New() *Builder { return &Builder{} }

// AddSheet appends a worksheet part with the given name and XML.
func (b *Builder) AddSheet(name, xml string) *Builder {
	b.sheets = append(b.sheets, sheet{name: name, xml: xml})
	return b
}

// AddHiddenSheet appends a hidden worksheet.
func (b *Builder) AddHiddenSheet(name, xml string) *Builder {
	b.sheets = append(b.sheets, sheet{name: name, xml: xml, hidden: true})
	return b
}

// SetSharedStrings sets the xl/sharedStrings.xml part (empty = no part).
func (b *Builder) SetSharedStrings(xml string) *Builder { b.sharedStrings = xml; return b }

// SetStyles sets the xl/styles.xml part (empty = no part).
func (b *Builder) SetStyles(xml string) *Builder { b.styles = xml; return b }

// SetDate1904 switches the workbook to the 1904 date system.
func (b *Builder) SetDate1904() *Builder { b.date1904 = true; return b }

// SetDefinedNames sets the workbook's <definedNames> children (raw
// <definedName ...>...</definedName> XML; empty = no element).
func (b *Builder) SetDefinedNames(xml string) *Builder { b.definedNames = xml; return b }

// AddPart adds any other part by its package name (for example
// "xl/worksheets/_rels/sheet1.xml.rels" or "xl/pivotTables/pivotTable1.xml").
// A non-empty contentType registers an Override for it; parts with an
// extension Default (.rels, .xml) can pass "". Parts are written after the
// core parts, in call order.
func (b *Builder) AddPart(name, contentType, xml string) *Builder {
	b.extra = append(b.extra, extraPart{name: name, contentType: contentType, xml: xml})
	return b
}

// Bytes serializes the package deterministically.
func (b *Builder) Bytes() []byte {
	var wbSheets, wbRels strings.Builder
	for i, s := range b.sheets {
		state := ""
		if s.hidden {
			state = ` state="hidden"`
		}
		fmt.Fprintf(&wbSheets, `<sheet name="%s" sheetId="%d"%s r:id="rId%d"/>`, s.name, i+1, state, i+1)
		fmt.Fprintf(&wbRels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`+"\n", i+1, i+1)
	}
	n := len(b.sheets)
	if b.sharedStrings != "" {
		n++
		fmt.Fprintf(&wbRels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>`+"\n", n)
	}
	if b.styles != "" {
		n++
		fmt.Fprintf(&wbRels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`+"\n", n)
	}

	pr := ""
	if b.date1904 {
		pr = `<workbookPr date1904="1"/>`
	}
	names := ""
	if b.definedNames != "" {
		names = `<definedNames>` + b.definedNames + `</definedNames>`
	}
	workbook := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		pr + `<sheets>` + wbSheets.String() + `</sheets>` + names + `</workbook>`

	type part struct{ name, data string }
	parts := []part{
		{"[Content_Types].xml", contentTypes(b)},
		{"_rels/.rels", rootRels},
		{"xl/workbook.xml", workbook},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
` + wbRels.String() + `</Relationships>`},
	}
	for i, s := range b.sheets {
		parts = append(parts, part{fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1), s.xml})
	}
	if b.sharedStrings != "" {
		parts = append(parts, part{"xl/sharedStrings.xml", b.sharedStrings})
	}
	if b.styles != "" {
		parts = append(parts, part{"xl/styles.xml", b.styles})
	}
	for _, e := range b.extra {
		parts = append(parts, part{e.name, e.xml})
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	stamp := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, p := range parts {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Deflate, Modified: stamp})
		if err != nil {
			panic(err) // deterministic in-memory build; only a programming error can fail
		}
		if _, err := f.Write([]byte(p.data)); err != nil {
			panic(err)
		}
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func contentTypes(b *Builder) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
`)
	for i := range b.sheets {
		fmt.Fprintf(&sb, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`+"\n", i+1)
	}
	if b.sharedStrings != "" {
		sb.WriteString(`<Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>` + "\n")
	}
	if b.styles != "" {
		sb.WriteString(`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` + "\n")
	}
	for _, e := range b.extra {
		if e.contentType != "" {
			fmt.Fprintf(&sb, `<Override PartName="/%s" ContentType="%s"/>`+"\n", e.name, e.contentType)
		}
	}
	sb.WriteString("</Types>\n")
	return sb.String()
}

const rootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>
`
