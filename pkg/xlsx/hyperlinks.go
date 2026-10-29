package xlsx

import (
	"errors"
	"fmt"

	"github.com/beevik/etree"
	"github.com/nathanstitt/omnidoc/pkg/xlsx/internal/xmlpart"
)

const (
	relHyperlink       = nsRelationships + "/hyperlink"
	relHyperlinkStrict = nsRelationshipsStrict + "/hyperlink"
)

// isHyperlinkRel reports a transitional or strict hyperlink relationship.
func isHyperlinkRel(relType string) bool {
	return relType == relHyperlink || relType == relHyperlinkStrict
}

// ErrHyperlinkTarget reports a Hyperlink with neither a URL nor a Location.
var ErrHyperlinkTarget = errors.New("xlsx: a hyperlink needs a URL or a location")

// Hyperlink is one cell (or range) hyperlink from the sheet's <hyperlinks>.
// A link has an external URL, an internal Location, or both (some producers
// keep a URL's "#fragment" in Location).
type Hyperlink struct {
	// Ref is the linked cell or range in A1 form ("B7", "A1:C1"), "$"
	// anchors stripped.
	Ref string
	// URL is the external target, resolved from the r:id relationship.
	URL string
	// Location is the in-workbook target ("Sheet2!A1", a defined name), or "".
	Location string
	// Display is the link's display text attribute, or "".
	Display string
	// Tooltip is the link's screen tip, or "".
	Tooltip string
}

// parseHyperlinks reads a worksheet's <hyperlinks> through the raw-fidelity
// tree (shared by the Workbook reader and the editor). rels maps the sheet's
// relationship ids; an r:id that does not resolve leaves URL "".
func parseHyperlinks(root *etree.Element, rels map[string]relationship) []Hyperlink {
	hl := xmlpart.FindChild(root, "hyperlinks")
	if hl == nil {
		return nil
	}
	var out []Hyperlink
	for _, el := range xmlpart.Children(hl, "hyperlink") {
		h := Hyperlink{
			Ref:      stripAnchors(el.SelectAttrValue("ref", "")),
			Location: el.SelectAttrValue("location", ""),
			Display:  el.SelectAttrValue("display", ""),
			Tooltip:  el.SelectAttrValue("tooltip", ""),
		}
		if id := relIDValue(el); id != "" {
			if r, ok := rels[id]; ok && isHyperlinkRel(r.Type) {
				h.URL = r.Target
			}
		}
		out = append(out, h)
	}
	return out
}

// Hyperlinks reads the sheet's hyperlinks (editor view).
func (s *SheetEdit) Hyperlinks() []Hyperlink {
	root, err := s.doc()
	if err != nil {
		return nil
	}
	return parseHyperlinks(root, s.sheetRelsByID())
}

// SetHyperlinks REPLACES the sheet's hyperlinks wholesale: the <hyperlinks>
// element is rebuilt, every existing hyperlink relationship is dropped, and
// each link with a URL gets a fresh TargetMode="External" relationship. An
// empty list removes them all. Every Ref must be a valid A1 cell or range,
// and every link needs a URL or a Location.
func (s *SheetEdit) SetHyperlinks(links []Hyperlink) error {
	refs := make([]string, len(links))
	for i, h := range links {
		rng, err := ParseRange(stripAnchors(h.Ref))
		if err != nil {
			return fmt.Errorf("xlsx: hyperlink %d: %w", i, err)
		}
		if h.URL == "" && h.Location == "" {
			return fmt.Errorf("hyperlink %d (%s): %w", i, rng, ErrHyperlinkTarget)
		}
		refs[i] = rng.String()
	}

	root, err := s.doc()
	if err != nil {
		return err
	}
	hasOldLinks := xmlpart.FindChild(root, "hyperlinks") != nil
	hasOldRels := false
	for _, r := range s.sheetRelsByID() {
		if isHyperlinkRel(r.Type) {
			hasOldRels = true
		}
	}
	if len(links) == 0 && !hasOldLinks && !hasOldRels {
		return nil // nothing to remove; leave the part byte-verbatim
	}
	if cur := parseHyperlinks(root, s.sheetRelsByID()); len(links) > 0 && len(cur) == len(links) {
		same := true
		for i, h := range links {
			h.Ref = refs[i]
			if cur[i] != h {
				same = false
				break
			}
		}
		if same {
			return nil // the sheet already has exactly these links
		}
	}

	needRels := hasOldRels
	for _, h := range links {
		if h.URL != "" {
			needRels = true
		}
	}
	var rels *etree.Element
	if needRels {
		if rels, err = s.sheetRels(); err != nil {
			return err
		}
		for _, ch := range xmlpart.Children(rels, "Relationship") {
			if isHyperlinkRel(ch.SelectAttrValue("Type", "")) {
				xmlpart.Remove(rels, ch)
			}
		}
	}

	root, err = s.mut()
	if err != nil {
		return err
	}
	if old := xmlpart.FindChild(root, "hyperlinks"); old != nil {
		xmlpart.Remove(root, old)
	}
	if len(links) == 0 {
		return nil
	}
	idAttr := ""
	linkType := relHyperlink
	if strictRels(root) {
		linkType = relHyperlinkStrict
	}
	container := etree.NewElement("hyperlinks")
	for i, h := range links {
		el := container.CreateElement("hyperlink")
		el.CreateAttr("ref", refs[i])
		if h.URL != "" {
			if idAttr == "" {
				idAttr = relIDAttr(root)
			}
			id := nextRelID(rels)
			rel := rels.CreateElement("Relationship")
			rel.CreateAttr("Id", id)
			rel.CreateAttr("Type", linkType)
			rel.CreateAttr("Target", h.URL)
			rel.CreateAttr("TargetMode", "External")
			el.CreateAttr(idAttr, id)
		}
		if h.Location != "" {
			el.CreateAttr("location", h.Location)
		}
		if h.Tooltip != "" {
			el.CreateAttr("tooltip", h.Tooltip)
		}
		if h.Display != "" {
			el.CreateAttr("display", h.Display)
		}
	}
	placeholder := xmlpart.EnsureChildInOrder(root, "hyperlinks", worksheetOrder)
	xmlpart.InsertBefore(root, container, placeholder)
	xmlpart.Remove(root, placeholder)
	return nil
}
