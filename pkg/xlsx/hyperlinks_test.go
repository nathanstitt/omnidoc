package xlsx

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	genxlsx "github.com/nathanstitt/omnidoc/testdata/gen/xlsx"
)

// hyperlinkFixture has an external link, an internal one, a link whose
// r:id does not resolve, and an unrelated (comments) relationship that every
// hyperlink edit must keep.
func hyperlinkFixture() []byte {
	sheet := wsOpen + `<sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData>` +
		`<hyperlinks>` +
		`<hyperlink ref="$A$1" r:id="rId1" tooltip="Docs" display="docs"/>` +
		`<hyperlink ref="B2:C2" location="Other!A1" display="jump"/>` +
		`<hyperlink ref="D4" r:id="rId9"/>` +
		`</hyperlinks></worksheet>`
	rels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://example.com/a?b=1&amp;c=2#top" TargetMode="External"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments" Target="../comments1.xml"/>
</Relationships>`
	return genxlsx.New().
		AddSheet("Links", sheet).
		AddSheet("Other", wsOpen+`<sheetData/></worksheet>`).
		AddPart("xl/worksheets/_rels/sheet1.xml.rels", "", rels).
		Bytes()
}

// TestHyperlinksRead pins both views: the external URL resolves verbatim
// through the rels (not as a part path), the internal location and the
// attributes read through, and an unresolvable r:id leaves URL empty.
func TestHyperlinksRead(t *testing.T) {
	data := hyperlinkFixture()
	want := []Hyperlink{
		{Ref: "A1", URL: "https://example.com/a?b=1&c=2#top", Display: "docs", Tooltip: "Docs"},
		{Ref: "B2:C2", Location: "Other!A1", Display: "jump"},
		{Ref: "D4"},
	}
	wb, err := OpenBytes(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if got := wb.Sheets[0].Hyperlinks; !reflect.DeepEqual(got, want) {
		t.Errorf("reader:\n got %+v\nwant %+v", got, want)
	}
	if wb.Sheets[1].Hyperlinks != nil {
		t.Errorf("sheet without links read %+v", wb.Sheets[1].Hyperlinks)
	}
	f, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("Links")
	if got := sh.Hyperlinks(); !reflect.DeepEqual(got, want) {
		t.Errorf("editor:\n got %+v\nwant %+v", got, want)
	}
}

// TestSetHyperlinks replaces the set: old hyperlink relationships go, each
// URL gets a fresh External relationship, the comments relationship stays,
// and the result reads back.
func TestSetHyperlinks(t *testing.T) {
	f, err := Edit(context.Background(), hyperlinkFixture())
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("Links")
	links := []Hyperlink{
		{Ref: "C3", URL: "mailto:a@example.com", Tooltip: "mail"},
		{Ref: "$E$5", Location: "'Other'!B2"},
		{Ref: "F6", URL: "https://example.org/x y", Location: "frag", Display: "both"},
	}
	if err := sh.SetHyperlinks(links); err != nil {
		t.Fatal(err)
	}
	out := mustSave(t, f)
	_, parts := zipParts(t, out)
	rels := string(parts["xl/worksheets/_rels/sheet1.xml.rels"])
	if strings.Contains(rels, "example.com/a") {
		t.Errorf("old hyperlink relationship kept: %s", rels)
	}
	if !strings.Contains(rels, `Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments"`) {
		t.Errorf("comments relationship lost: %s", rels)
	}
	if strings.Count(rels, `TargetMode="External"`) != 2 {
		t.Errorf("want 2 external relationships: %s", rels)
	}

	links[1].Ref = "E5"
	links[2].URL = "https://example.org/x%20y" // stored escaped, as Excel does
	wb, err := OpenBytes(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if got := wb.Sheets[0].Hyperlinks; !reflect.DeepEqual(got, links) {
		t.Errorf("reopened:\n got %+v\nwant %+v", got, links)
	}

	// An empty set removes the element and the relationships.
	if err := sh.SetHyperlinks(nil); err != nil {
		t.Fatal(err)
	}
	_, parts = zipParts(t, mustSave(t, f))
	if s := string(parts["xl/worksheets/sheet1.xml"]); strings.Contains(s, "hyperlink") {
		t.Errorf("hyperlinks not removed: %s", s)
	}
	if r := string(parts["xl/worksheets/_rels/sheet1.xml.rels"]); strings.Contains(r, "hyperlink") || !strings.Contains(r, "comments") {
		t.Errorf("rels after removal: %s", r)
	}
}

// TestSetHyperlinksNewSheet: on a sheet the editor created (no rels part, no
// xmlns:r), the write creates the rels part and declares the namespace, so
// the part is well-formed namespaced XML.
func TestSetHyperlinksNewSheet(t *testing.T) {
	f := New()
	sh, _ := f.Sheet("Sheet1")
	if err := sh.SetHyperlinks([]Hyperlink{{Ref: "A1", URL: "https://example.com"}}); err != nil {
		t.Fatal(err)
	}
	out := mustSave(t, f)
	_, parts := zipParts(t, out)
	assertNamespacedRID(t, parts["xl/worksheets/sheet1.xml"])
	wb, err := OpenBytes(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if got := wb.Sheets[0].Hyperlinks; len(got) != 1 || got[0].URL != "https://example.com" {
		t.Errorf("reopened = %+v", got)
	}
}

// TestSetCommentDeclaresRelNamespace is the regression for a sheet the
// editor created: legacyDrawing's r:id used to carry an unbound prefix.
func TestSetCommentDeclaresRelNamespace(t *testing.T) {
	f := New()
	sh, _ := f.Sheet("Sheet1")
	if err := sh.SetComment(Comment{Row: 1, Col: 1, Author: "a", Text: "note"}); err != nil {
		t.Fatal(err)
	}
	_, parts := zipParts(t, mustSave(t, f))
	assertNamespacedRID(t, parts["xl/worksheets/sheet1.xml"])
}

// TestSetHyperlinksNoOp: removing links from a sheet that has none leaves
// every part byte-verbatim.
func TestSetHyperlinksNoOp(t *testing.T) {
	data := hyperlinkFixture()
	f, err := Edit(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := f.Sheet("Other")
	if err := sh.SetHyperlinks(nil); err != nil {
		t.Fatal(err)
	}
	_, want := zipParts(t, data)
	_, got := zipParts(t, mustSave(t, f))
	for name := range want {
		if !bytes.Equal(want[name], got[name]) {
			t.Errorf("no-op changed %s", name)
		}
	}
}

func TestSetHyperlinksErrors(t *testing.T) {
	f := New()
	sh, _ := f.Sheet("Sheet1")
	if err := sh.SetHyperlinks([]Hyperlink{{Ref: "A1"}}); !errors.Is(err, ErrHyperlinkTarget) {
		t.Errorf("no target: err = %v, want ErrHyperlinkTarget", err)
	}
	if err := sh.SetHyperlinks([]Hyperlink{{Ref: "1A", URL: "https://x"}}); err == nil {
		t.Error("bad ref accepted")
	}
	if err := sh.SetHyperlinks([]Hyperlink{{Ref: "A1", URL: "https://x/" + strings.Repeat("a", 2079)}}); err == nil {
		t.Error("URL over 2079 characters accepted")
	}
	if got := escapeURL(`http://x/a b"<>\^{|}` + "`%20é"); got != `http://x/a%20b%22%3C%3E%5C%5E%7B%7C%7D%60%20é` {
		t.Errorf("escapeURL = %q", got)
	}
}

// assertNamespacedRID decodes a worksheet and fails unless every attribute
// named "id" with a prefix resolves to the relationships namespace (an
// unbound prefix decodes with the bare prefix as its Space).
func assertNamespacedRID(t *testing.T, part []byte) {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(part))
	found := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("worksheet does not decode: %v", err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			for _, a := range se.Attr {
				if a.Name.Local == "id" && a.Name.Space != "" {
					found = true
					if a.Name.Space != nsRelationships {
						t.Errorf("<%s> id attribute in namespace %q, want %q:\n%s", se.Name.Local, a.Name.Space, nsRelationships, part)
					}
				}
			}
		}
	}
	if !found {
		t.Errorf("no r:id attribute in:\n%s", part)
	}
}
