package xlsx

import (
	"encoding/xml"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/beevik/etree"
	"github.com/nathanstitt/omnidoc/pkg/xlsx/internal/xmlpart"
)

// Comment is one classic cell note. Row and Col are 1-based (A1-space — a
// comment is a positioned annotation, matching the editor's coordinates, not
// the 0-based read grid).
type Comment struct {
	Row, Col int
	Author   string
	Text     string
}

const (
	relComments    = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments"
	relVMLDrawing  = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/vmlDrawing"
	ctComments     = "application/vnd.openxmlformats-officedocument.spreadsheetml.comments+xml"
	ctVMLDrawing   = "application/vnd.openxmlformats-officedocument.vmlDrawing"
	commentsNSMain = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
)

// parseCommentsPart extracts a comments part's notes.
func parseCommentsPart(data []byte) []Comment {
	if data == nil {
		return nil
	}
	var doc struct {
		Authors struct {
			Author []string `xml:"author"`
		} `xml:"authors"`
		CommentList struct {
			Comment []struct {
				Ref      string `xml:"ref,attr"`
				AuthorID int    `xml:"authorId,attr"`
				Text     struct {
					T []string `xml:"t"`
					R []struct {
						T string `xml:"t"`
					} `xml:"r"`
				} `xml:"text"`
			} `xml:"comment"`
		} `xml:"commentList"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	var out []Comment
	for _, c := range doc.CommentList.Comment {
		row, col, err := ParseCellRef(c.Ref)
		if err != nil {
			continue
		}
		var sb strings.Builder
		for _, t := range c.Text.T {
			sb.WriteString(t)
		}
		for _, r := range c.Text.R {
			sb.WriteString(r.T)
		}
		author := ""
		if c.AuthorID >= 0 && c.AuthorID < len(doc.Authors.Author) {
			author = doc.Authors.Author[c.AuthorID]
		}
		out = append(out, Comment{Row: row, Col: col, Author: author, Text: sb.String()})
	}
	return out
}

// sheetRelsName is a worksheet part's rels part name.
func sheetRelsName(sheetPart string) string {
	return path.Join(path.Dir(sheetPart), "_rels", path.Base(sheetPart)+".rels")
}

// sheetRelTarget resolves the first relationship of relType on a sheet part,
// reading the CURRENT state (this session's rel additions included).
func (f *File) sheetRelTarget(sheetPart, relType string) (string, bool) {
	data := f.rawPartCurrent(sheetRelsName(sheetPart))
	if data == nil {
		return "", false
	}
	var doc struct {
		Rels []struct {
			Type   string `xml:"Type,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return "", false
	}
	for _, r := range doc.Rels {
		if r.Type == relType {
			return path.Clean(path.Join(path.Dir(sheetPart), r.Target)), true
		}
	}
	return "", false
}

// Comments reads the sheet's cell notes.
func (s *SheetEdit) Comments() []Comment {
	target, ok := s.file.sheetRelTarget(s.partName, relComments)
	if !ok {
		return nil
	}
	return parseCommentsPart(s.file.rawPartCurrent(target))
}

// rawPartCurrent returns a part's CURRENT bytes — the parsed tree when the
// part is dirty, else the raw bytes.
func (f *File) rawPartCurrent(name string) []byte {
	if f.dirty[name] {
		if data, err := f.parsed[name].Bytes(); err == nil {
			return data
		}
	}
	return f.rawPart(name)
}

// SetComment sets (replacing any existing note at that cell) a classic cell
// note, creating the comments part, its VML drawing, and their wiring on
// first use.
func (s *SheetEdit) SetComment(c Comment) error {
	if c.Row < 1 || c.Col < 1 {
		return fmt.Errorf("%w: (%d, %d)", ErrBadRef, c.Row, c.Col)
	}
	comments := s.Comments()
	replaced := false
	for i := range comments {
		if comments[i].Row == c.Row && comments[i].Col == c.Col {
			comments[i] = c
			replaced = true
			break
		}
	}
	if !replaced {
		comments = append(comments, c)
	}
	return s.writeComments(comments)
}

// RemoveComment deletes the note at a cell (a no-op when none exists).
func (s *SheetEdit) RemoveComment(row, col int) error {
	comments := s.Comments()
	kept := comments[:0]
	for _, c := range comments {
		if c.Row != row || c.Col != col {
			kept = append(kept, c)
		}
	}
	if len(kept) == len(comments) {
		return nil
	}
	return s.writeComments(kept)
}

// writeComments regenerates the sheet's comments part and VML drawing from
// the full note list (both parts are mechanical projections of it).
func (s *SheetEdit) writeComments(comments []Comment) error {
	commentsPart, vmlPart, err := s.ensureCommentParts()
	if err != nil {
		return err
	}
	if err := s.file.setPart(commentsPart, commentsXML(comments)); err != nil {
		return err
	}
	return s.file.setPart(vmlPart, vmlXML(comments))
}

// ensureCommentParts wires the comments + VML parts for this sheet: rels,
// content types, and the sheet's legacyDrawing reference.
func (s *SheetEdit) ensureCommentParts() (commentsPart, vmlPart string, err error) {
	if target, ok := s.file.sheetRelTarget(s.partName, relComments); ok {
		vml, _ := s.file.sheetRelTarget(s.partName, relVMLDrawing)
		return target, vml, nil
	}

	// Allocate part names off the sheet's number when possible.
	n := 1
	for s.file.partExists(fmt.Sprintf("xl/comments%d.xml", n)) {
		n++
	}
	commentsPart = fmt.Sprintf("xl/comments%d.xml", n)
	v := 1
	for s.file.partExists(fmt.Sprintf("xl/drawings/vmlDrawing%d.vml", v)) {
		v++
	}
	vmlPart = fmt.Sprintf("xl/drawings/vmlDrawing%d.vml", v)

	// Sheet rels: add (or create) with the two relationships.
	vmlRelID, err := s.addSheetRel(relVMLDrawing, vmlPart)
	if err != nil {
		return "", "", err
	}
	if _, err := s.addSheetRel(relComments, commentsPart); err != nil {
		return "", "", err
	}

	// Content types: an Override for the comments part; a Default for .vml.
	ct, err := s.file.mutatePart("[Content_Types].xml")
	if err != nil {
		return "", "", err
	}
	hasVMLDefault := false
	for _, ch := range xmlpart.Children(ct.Root(), "Default") {
		if strings.EqualFold(ch.SelectAttrValue("Extension", ""), "vml") {
			hasVMLDefault = true
		}
	}
	if !hasVMLDefault {
		def := etree.NewElement("Default")
		def.CreateAttr("Extension", "vml")
		def.CreateAttr("ContentType", ctVMLDrawing)
		ct.Root().AddChild(def)
	}
	over := etree.NewElement("Override")
	over.CreateAttr("PartName", "/"+commentsPart)
	over.CreateAttr("ContentType", ctComments)
	ct.Root().AddChild(over)

	// The sheet references its VML drawing via legacyDrawing.
	root, err := s.mut()
	if err != nil {
		return "", "", err
	}
	attr := relIDAttr(root)
	ld := xmlpart.EnsureChildInOrder(root, "legacyDrawing", worksheetOrder)
	ld.CreateAttr(attr, vmlRelID)
	return commentsPart, vmlPart, nil
}

// relPath expresses target relative to dir (both package part paths).
func relPath(dir, target string) (string, error) {
	dirParts := strings.Split(path.Clean(dir), "/")
	tgtParts := strings.Split(path.Clean(target), "/")
	common := 0
	for common < len(dirParts) && common < len(tgtParts)-1 && dirParts[common] == tgtParts[common] {
		common++
	}
	var out []string
	for range dirParts[common:] {
		out = append(out, "..")
	}
	out = append(out, tgtParts[common:]...)
	return path.Join(out...), nil
}

// commentsXML renders the comments part from the note list.
func commentsXML(comments []Comment) []byte {
	var authors []string
	authorID := map[string]int{}
	for _, c := range comments {
		if _, ok := authorID[c.Author]; !ok {
			authorID[c.Author] = len(authors)
			authors = append(authors, c.Author)
		}
	}
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	sb.WriteString(`<comments xmlns="` + commentsNSMain + `"><authors>`)
	for _, a := range authors {
		sb.WriteString("<author>" + xmlEscape(a) + "</author>")
	}
	sb.WriteString("</authors><commentList>")
	for _, c := range comments {
		sb.WriteString(`<comment ref="` + CellRef(c.Row, c.Col) + `" authorId="` + strconv.Itoa(authorID[c.Author]) + `">`)
		sb.WriteString(`<text><t xml:space="preserve">` + xmlEscape(c.Text) + `</t></text></comment>`)
	}
	sb.WriteString("</commentList></comments>\n")
	return []byte(sb.String())
}

// vmlXML renders the legacy VML drawing that makes classic notes visible in
// Excel — a mechanical projection of the note list (one hidden note shape per
// comment, anchored near its cell).
func vmlXML(comments []Comment) []byte {
	var sb strings.Builder
	sb.WriteString(`<xml xmlns:v="urn:schemas-microsoft-com:vml" xmlns:o="urn:schemas-microsoft-com:office:office" xmlns:x="urn:schemas-microsoft-com:office:excel">` + "\n")
	sb.WriteString(`<o:shapelayout v:ext="edit"><o:idmap v:ext="edit" data="1"/></o:shapelayout>` + "\n")
	sb.WriteString(`<v:shapetype id="_x0000_t202" coordsize="21600,21600" o:spt="202" path="m,l,21600r21600,l21600,xe"><v:stroke joinstyle="miter"/><v:path gradientshapeok="t" o:connecttype="rect"/></v:shapetype>` + "\n")
	for i, c := range comments {
		row0, col0 := c.Row-1, c.Col-1
		fmt.Fprintf(&sb, `<v:shape id="_x0000_s%d" type="#_x0000_t202" style="position:absolute;margin-left:80pt;margin-top:%dpt;width:108pt;height:60pt;z-index:%d;visibility:hidden" fillcolor="#ffffe1" o:insetmode="auto">`,
			1025+i, 2+row0*14, i+1)
		sb.WriteString(`<v:fill color2="#ffffe1"/><v:shadow on="t" color="black" obscured="t"/><v:path o:connecttype="none"/><v:textbox style="mso-direction-alt:auto"/>`)
		fmt.Fprintf(&sb, `<x:ClientData ObjectType="Note"><x:MoveWithCells/><x:SizeWithCells/><x:AutoFill>False</x:AutoFill><x:Row>%d</x:Row><x:Column>%d</x:Column></x:ClientData>`, row0, col0)
		sb.WriteString("</v:shape>\n")
	}
	sb.WriteString("</xml>\n")
	return []byte(sb.String())
}

// xmlEscape escapes character data / attribute values.
func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
