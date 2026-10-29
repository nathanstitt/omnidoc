package font

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// sfnt version tags / WOFF signatures (the first 4 bytes of the container).
const (
	sigTrueType = 0x00010000
	sigTrue     = 0x74727565 // "true"
	sigOTTO     = 0x4F54544F // "OTTO"
	sigTTCF     = 0x74746366 // "ttcf" (TrueType Collection)
	sigWOFF     = 0x774F4646 // "wOFF"
	sigWOFF2    = 0x774F4632 // "wOF2"
)

// LoadSFNT builds a reflow Face from a font file's bytes, transparently unwrapping
// a WOFF1 or WOFF2 container to its sfnt tables first. Raw sfnt (TrueType/OpenType)
// is parsed directly. It returns a typed error for an unrecognized or malformed
// container so the caller (the face cache) falls back to a bundled substitute.
func LoadSFNT(data []byte) (*Face, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("%w: too short", ErrInvalidWOFF)
	}
	sig := binary.BigEndian.Uint32(data[:4])
	var sfnt []byte
	switch sig {
	case sigTrueType, sigTrue, sigOTTO, sigTTCF:
		sfnt = data
	case sigWOFF:
		b, err := decodeWOFF1(data)
		if err != nil {
			return nil, err
		}
		sfnt = b
	case sigWOFF2:
		b, err := decodeWOFF2(data)
		if err != nil {
			return nil, err
		}
		sfnt = b
	default:
		return nil, fmt.Errorf("%w: unrecognized signature 0x%08x", ErrUnsupportedFontProgram, sig)
	}
	prog, err := parseProgram(sfnt, progTrueType)
	if err != nil {
		return nil, err
	}
	// Retain the (decompressed) sfnt bytes for PDF embedding. A CFF-flavored sfnt
	// (an OTTO container with a "CFF " table) embeds as /FontFile3 OpenType, a
	// glyf-flavored one as /FontFile2; sniff the table directory to tell them apart.
	//
	// For a ttcf collection, progData is the raw collection bytes (parseProgram above
	// already extracts the first face for rasterization). Those raw bytes are NOT a
	// standalone embeddable sfnt, so the PDF-embed path would need a reserialized
	// standalone sfnt for the selected face — a documented follow-up. This is fine for
	// the rasterization-only system-font use case; the PDF writer embeds its own faces
	// and falls back to drawing outlines when a program isn't embeddable.
	kind := ProgramKindTrueType
	if sfntHasTable(sfnt, "CFF ") {
		kind = ProgramKindCFF
	}
	f := &Face{prog: prog, names: prog.nameToGID(), progData: sfnt, progKind: kind}
	f.colr, f.cpal = parseColorTables(sfnt)
	f.numGlyphs = prog.numGlyphs()
	f.sbix, f.cbdt = parseBitmapTables(sfnt, f.numGlyphs)
	return f, nil
}

// parseColorTables reads COLR/CPAL from an sfnt, if present and well-formed. Both are
// required: layers reference palette entries, so a COLR without a CPAL cannot be
// resolved and is dropped rather than half-applied. A malformed table yields nil, so
// the face renders monochrome instead of failing to load — a colour table is untrusted
// document input and must never make an otherwise-usable font unusable.
func parseColorTables(sfnt []byte) (*colrTable, *cpalTable) {
	tables, _, err := ParseSFNTTables(firstFaceOf(sfnt))
	if err != nil {
		return nil, nil
	}
	colrRaw, okC := tables["COLR"]
	cpalRaw, okP := tables["CPAL"]
	if !okC || !okP {
		return nil, nil
	}
	colr, ok := parseCOLR(colrRaw)
	if !ok {
		return nil, nil
	}
	cpal, ok := parseCPAL(cpalRaw)
	if !ok {
		return nil, nil
	}
	return colr, cpal
}

// parseBitmapTables reads the colour BITMAP tables from an sfnt: Apple's sbix, or
// Google's CBLC index paired with its CBDT data. Both are optional and independent; a
// malformed one yields nil so the face still loads and renders monochrome.
func parseBitmapTables(sfnt []byte, numGlyphs int) (*sbixTable, *cbdtTable) {
	tables, _, err := ParseSFNTTables(firstFaceOf(sfnt))
	if err != nil {
		return nil, nil
	}
	var sb *sbixTable
	if raw, ok := tables["sbix"]; ok {
		if t, ok := parseSbix(raw, numGlyphs); ok {
			sb = t
		}
	}
	var cb *cbdtTable
	if cblc, ok := tables["CBLC"]; ok {
		if cbdtRaw, ok2 := tables["CBDT"]; ok2 {
			if t, ok3 := parseCBLC(cblc, cbdtRaw); ok3 {
				cb = t
			}
		}
	}
	return sb, cb
}

// ParseSFNTTables reads the offset table and table directory into a tag->bytes
// map, also returning the sfnt flavor (version tag). Table values alias data
// (zero-copy sub-slices).
func ParseSFNTTables(data []byte) (map[string][]byte, uint32, error) {
	if len(data) < 12 {
		return nil, 0, errors.New("font: sfnt: short sfnt")
	}
	flavor := binary.BigEndian.Uint32(data[0:4])
	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	tables := make(map[string][]byte, numTables)
	for i := 0; i < numTables; i++ {
		rec := 12 + 16*i
		if rec+16 > len(data) {
			return nil, 0, errors.New("font: sfnt: truncated table directory")
		}
		tag := string(data[rec : rec+4])
		off := int(binary.BigEndian.Uint32(data[rec+8 : rec+12]))
		length := int(binary.BigEndian.Uint32(data[rec+12 : rec+16]))
		if off < 0 || length < 0 || off+length > len(data) {
			return nil, 0, fmt.Errorf("font: sfnt: table %q out of range", tag)
		}
		tables[tag] = data[off : off+length]
	}
	return tables, flavor, nil
}

// sfntHasTable reports whether the sfnt table directory in data declares a table
// with the 4-byte tag. It is tolerant of a short/malformed directory (returns
// false rather than panicking).
func sfntHasTable(data []byte, tag string) bool {
	if len(data) < 12 || len(tag) != 4 {
		return false
	}
	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	const dirStart = 12
	const recSize = 16 // tag(4) + checksum(4) + offset(4) + length(4)
	for i := 0; i < numTables; i++ {
		off := dirStart + i*recSize
		if off+4 > len(data) {
			return false
		}
		if string(data[off:off+4]) == tag {
			return true
		}
	}
	return false
}

// firstFaceOf returns the sfnt bytes whose table directory should be read.
//
// A TrueType Collection ("ttcf") has no table directory of its own — it is a header
// pointing at one per face — so reading tables from the raw bytes finds nothing. The
// outline parser already extracts the first face, so the colour tables must come from
// the SAME face or they would describe glyphs the outlines do not have.
//
// The returned slice ALIASES data from the face's offset, which is what keeps the
// absolute table offsets inside the directory valid: they are relative to the file
// start, and slicing from 0 would shift every one of them. So this returns data
// unchanged for a collection whose first face is not at offset 0... which is every
// collection. Instead it returns data itself and lets ParseSFNTTables read the
// directory at the face offset via a shifted view.
func firstFaceOf(data []byte) []byte {
	if len(data) < 16 || string(data[:4]) != "ttcf" {
		return data
	}
	n := binary.BigEndian.Uint32(data[8:12])
	if n == 0 {
		return data
	}
	off := binary.BigEndian.Uint32(data[12:16])
	if int(off) >= len(data) {
		return data
	}
	// Table offsets in a collection's face directory are absolute from the FILE start,
	// so the directory must be read at `off` while offsets still resolve against the
	// whole file. collectionFace splices a view that satisfies both.
	return collectionFace(data, off)
}

// collectionFace builds a byte slice whose first 12+16n bytes are the face's table
// directory (read from off) but whose absolute offsets still address the original
// file, by returning the original slice with the directory copied to the front.
func collectionFace(data []byte, off uint32) []byte {
	if int(off)+12 > len(data) {
		return data
	}
	numTables := int(binary.BigEndian.Uint16(data[off+4:]))
	dirLen := 12 + 16*numTables
	if int(off)+dirLen > len(data) {
		return data
	}
	out := make([]byte, len(data))
	copy(out, data)
	copy(out[:dirLen], data[off:int(off)+dirLen])
	return out
}

// emptyCmap is a minimal cmap table that maps no character: version 0, one
// (3,1) format-4 subtable at offset 12 holding only the required final segment
// (0xFFFF..0xFFFF, idDelta 1, so it maps to GID 0).
var emptyCmap = []byte{
	0, 0, 0, 1, // version 0, numTables 1
	0, 3, 0, 1, 0, 0, 0, 12, // platform 3, encoding 1, offset 12
	0, 4, 0, 24, 0, 0, // format 4, length 24, language 0
	0, 2, 0, 2, 0, 0, 0, 0, // segCountX2 2, searchRange 2, entrySelector 0, rangeShift 0
	0xFF, 0xFF, 0, 0, // endCode[0] 0xFFFF, reservedPad
	0xFF, 0xFF, 0, 1, 0, 0, // startCode[0] 0xFFFF, idDelta 1, idRangeOffset 0
}

// withCmap returns an sfnt that has a cmap table, for the TrueType parser,
// which rejects a font without one. A PDF-embedded TrueType program often has
// no cmap: PDF 32000-1 §9.9 does not require it for a CIDFontType2 font, whose
// glyphs come from /CIDToGIDMap, and subsetters drop it from simple fonts too.
// When data has a cmap, or is not a single-face sfnt, it is returned unchanged;
// otherwise the sfnt is rebuilt with emptyCmap added, so every rune lookup
// misses and glyph selection falls back to names, CIDToGIDMap, or raw codes.
func withCmap(data []byte) []byte {
	if len(data) < 4 || sfntHasTable(data, "cmap") {
		return data
	}
	switch flavor := binary.BigEndian.Uint32(data[:4]); flavor {
	case sigTrueType, sigTrue, sigOTTO:
		tables, _, err := ParseSFNTTables(data)
		if err != nil {
			return data
		}
		tables["cmap"] = emptyCmap
		return BuildSFNT(flavor, tables)
	}
	return data
}
