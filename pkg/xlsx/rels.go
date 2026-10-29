package xlsx

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/beevik/etree"
	"github.com/nathanstitt/omnidoc/pkg/xlsx/internal/xmlpart"
)

// nsRelationships is the officeDocument relationships namespace that r:id
// attributes live in; nsRelationshipsStrict is its ISO-strict form, which is
// also the prefix of strict relationship types.
const (
	nsRelationships       = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	nsRelationshipsStrict = "http://purl.oclc.org/ooxml/officeDocument/relationships"
)

// isRelNamespace reports a transitional or strict relationships namespace.
func isRelNamespace(ns string) bool { return ns == nsRelationships || ns == nsRelationshipsStrict }

// strictRels reports whether root binds the ISO-strict relationships
// namespace (so new relationship types should use the strict form too).
func strictRels(root *etree.Element) bool {
	for _, a := range root.Attr {
		if a.Space == "xmlns" && a.Value == nsRelationshipsStrict {
			return true
		}
	}
	return false
}

// emptyRels is a rels part with no relationships.
const emptyRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
</Relationships>
`

// relIDAttr returns the qualified name ("r:id") for a relationship-id
// attribute on elements under root. It reuses the prefix root already binds
// to the relationships namespace (transitional or strict); when none is
// bound it declares xmlns:r (or the first free "rN" prefix), so the
// attribute never carries an unbound prefix — a worksheet this editor
// created has no such declaration.
func relIDAttr(root *etree.Element) string {
	taken := map[string]bool{}
	for _, a := range root.Attr {
		if a.Space == "xmlns" {
			if isRelNamespace(a.Value) {
				return a.Key + ":id"
			}
			taken[a.Key] = true
		}
	}
	prefix := "r"
	for n := 1; taken[prefix]; n++ {
		prefix = "r" + strconv.Itoa(n)
	}
	root.CreateAttr("xmlns:"+prefix, nsRelationships)
	return prefix + ":id"
}

// relIDValue reads an element's relationship-id attribute (any prefix bound
// to the transitional or strict relationships namespace; "r" by
// convention).
func relIDValue(el *etree.Element) string {
	for _, a := range el.Attr {
		if a.Key == "id" && a.Space != "" && isRelNamespace(a.NamespaceURI()) {
			return a.Value
		}
	}
	return ""
}

// nextRelID returns the first "rIdN" past every id in a rels tree.
func nextRelID(rels *etree.Element) string {
	maxRel := 0
	for _, ch := range xmlpart.Children(rels, "Relationship") {
		id := ch.SelectAttrValue("Id", "")
		if strings.HasPrefix(id, "rId") {
			if n, err := strconv.Atoi(id[3:]); err == nil && n > maxRel {
				maxRel = n
			}
		}
	}
	return fmt.Sprintf("rId%d", maxRel+1)
}

// sheetRels returns the sheet's rels tree for mutation, creating the part
// when it is absent.
func (s *SheetEdit) sheetRels() (*etree.Element, error) {
	relsName := sheetRelsName(s.partName)
	if !s.file.partExists(relsName) {
		s.file.added[relsName] = []byte(emptyRels)
	}
	rels, err := s.file.mutatePart(relsName)
	if err != nil {
		return nil, err
	}
	return rels.Root(), nil
}

// sheetRelsByID reads the sheet's relationships keyed by id, from the
// CURRENT state. External targets stay verbatim; internal ones are not
// resolved (callers that need a part name use sheetRelTarget).
func (s *SheetEdit) sheetRelsByID() map[string]relationship {
	data := s.file.rawPartCurrent(sheetRelsName(s.partName))
	if data == nil {
		return nil
	}
	p, err := xmlpart.Parse(data)
	if err != nil {
		return nil
	}
	out := map[string]relationship{}
	for _, ch := range xmlpart.Children(p.Root(), "Relationship") {
		r := relationship{
			ID:         ch.SelectAttrValue("Id", ""),
			Type:       ch.SelectAttrValue("Type", ""),
			Target:     ch.SelectAttrValue("Target", ""),
			TargetMode: ch.SelectAttrValue("TargetMode", ""),
		}
		out[r.ID] = r
	}
	return out
}
