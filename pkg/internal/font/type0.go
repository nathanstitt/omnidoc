package font

import (
	"encoding/binary"

	"github.com/benoitkugler/textlayout/fonts"

	"github.com/nathanstitt/omnidoc/pkg/internal/content"
	"github.com/nathanstitt/omnidoc/pkg/pdf"
)

// type0Font is a GlyphSource for a composite (Type0) font using Identity-H
// encoding, where two-byte big-endian show-string codes are the CIDs directly.
// The descendant CIDFont is CIDFontType2 (TrueType/FontFile2) or CIDFontType0
// (CFF/FontFile3). CID→GID comes from /CIDToGIDMap; widths from /DW and /W.
type type0Font struct {
	prog     *program
	dw       float64         // default width, em units
	w        map[int]float64 // CID → width, em units
	toUni    map[int]rune    // CID → rune from /ToUnicode, for text extraction
	cidToGID func(cid int) fonts.GID
}

// newType0Font builds a type0Font from a resolved Type0 font dictionary. Only
// Identity-H/Identity-V encodings are supported; others yield ErrUnsupportedCMap.
func newType0Font(doc *pdf.Document, fontDict pdf.Dict) (*type0Font, error) {
	if name, ok := doc.GetName(fontDict["Encoding"]); !ok ||
		(name != "Identity-H" && name != "Identity-V") {
		return nil, ErrUnsupportedCMap
	}

	cidFont := firstDescendant(doc, fontDict)
	if cidFont == nil {
		return nil, ErrUnsupportedFontType
	}

	prog, err := embeddedCIDProgram(doc, cidFont)
	if err != nil {
		return nil, err
	}

	f := &type0Font{prog: prog, dw: 1.0}
	if dw, ok := pdf.Number(doc.Resolve(cidFont["DW"])); ok {
		f.dw = dw / 1000
	}
	f.w = parseCIDWidths(doc, doc.GetArray(cidFont["W"]))
	f.toUni = parseToUnicode(doc, fontDict)
	f.cidToGID = buildCIDToGID(doc, cidFont, prog.numGlyphs())
	return f, nil
}

// firstDescendant returns the single CIDFont in /DescendantFonts.
func firstDescendant(doc *pdf.Document, fontDict pdf.Dict) pdf.Dict {
	arr := doc.GetArray(fontDict["DescendantFonts"])
	if len(arr) == 0 {
		return nil
	}
	return doc.GetDict(arr[0])
}

// embeddedCIDProgram parses the embedded program for a CIDFont. CIDFontType2
// uses FontFile2 (TrueType); CIDFontType0 uses FontFile3 (bare CFF/CIDFontType0C,
// or an OpenType wrapper).
func embeddedCIDProgram(doc *pdf.Document, cidFont pdf.Dict) (*program, error) {
	desc := doc.GetDict(cidFont["FontDescriptor"])
	if desc == nil {
		return nil, ErrNoEmbeddedProgram
	}
	if s := doc.GetStream(desc["FontFile2"]); s != nil {
		b, _, derr := doc.DecodedStream(s)
		if derr != nil {
			return nil, ErrUnsupportedFontProgram
		}
		return parseProgram(withCmap(b), progTrueType)
	}
	if s := doc.GetStream(desc["FontFile3"]); s != nil {
		b, _, derr := doc.DecodedStream(s)
		if derr != nil {
			return nil, ErrUnsupportedFontProgram
		}
		sub, _ := doc.GetName(s.Dict["Subtype"])
		if sub == "OpenType" {
			return parseProgram(withCmap(b), progTrueType)
		}
		return parseProgram(b, progCFF)
	}
	return nil, ErrNoEmbeddedProgram
}

// buildCIDToGID returns the CID→GID mapping function from /CIDToGIDMap: the name
// /Identity (or absent) is the identity map; a stream is a packed big-endian
// uint16 array indexed by CID. Out-of-range results clamp to GID 0 (.notdef).
func buildCIDToGID(doc *pdf.Document, cidFont pdf.Dict, numGlyphs int) func(int) fonts.GID {
	clamp := func(gid int) fonts.GID {
		if gid < 0 || gid >= numGlyphs {
			return 0
		}
		return fonts.GID(gid)
	}
	if s := doc.GetStream(cidFont["CIDToGIDMap"]); s != nil {
		if data, _, err := doc.DecodedStream(s); err == nil {
			return func(cid int) fonts.GID {
				off := 2 * cid
				if off < 0 || off+2 > len(data) {
					return 0
				}
				return clamp(int(binary.BigEndian.Uint16(data[off:])))
			}
		}
	}
	// Identity (the name /Identity or absent).
	return func(cid int) fonts.GID { return clamp(cid) }
}

// parseCIDWidths parses a /W array into a CID→width(em) map. /W is a sequence of
// either "c [w1 w2 …]" (consecutive CIDs from c) or "cFirst cLast w" (a range
// all sharing width w). Absurdly large ranges are skipped defensively.
func parseCIDWidths(doc *pdf.Document, w pdf.Array) map[int]float64 {
	const maxRange = 65536
	out := map[int]float64{}
	i := 0
	for i < len(w) {
		c, ok := pdf.IntValue(doc.Resolve(w[i]))
		if !ok {
			i++
			continue
		}
		if i+1 >= len(w) {
			break
		}
		// Form 1: c [w1 w2 ...].
		if arr := doc.GetArray(w[i+1]); arr != nil {
			for k, wObj := range arr {
				if wv, ok := pdf.Number(doc.Resolve(wObj)); ok {
					out[c+k] = wv / 1000
				}
			}
			i += 2
			continue
		}
		// Form 2: cFirst cLast w.
		if i+2 >= len(w) {
			break
		}
		cLast, ok1 := pdf.IntValue(doc.Resolve(w[i+1]))
		wv, ok2 := pdf.Number(doc.Resolve(w[i+2]))
		if ok1 && ok2 && cLast >= c && cLast-c <= maxRange {
			for cid := c; cid <= cLast; cid++ {
				out[cid] = wv / 1000
			}
		}
		i += 3
	}
	return out
}

// widthOf returns the advance for a CID in em units: /W if present, else /DW.
func (f *type0Font) widthOf(cid int) float64 {
	if w, ok := f.w[cid]; ok {
		return w
	}
	return f.dw
}

// DecodeString implements content.GlyphSource: two bytes per glyph (Identity-H).
// A trailing odd byte is dropped.
func (f *type0Font) DecodeString(s []byte) []content.Glyph {
	glyphs := make([]content.Glyph, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		cid := int(s[i])<<8 | int(s[i+1])
		gid := f.cidToGID(cid)
		glyphs = append(glyphs, content.Glyph{
			Code:    cid,
			Width:   f.widthOf(cid),
			Rune:    f.toUni[cid], // 0 when the font carries no /ToUnicode
			IsSpace: false,
			Outline: f.prog.outline(gid),
		})
	}
	return glyphs
}
