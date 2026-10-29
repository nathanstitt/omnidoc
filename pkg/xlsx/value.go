package xlsx

import (
	"strconv"
	"strings"
	"time"
)

// displayValue renders a raw cell's cached value as its display string.
func displayValue(c rawCell, shared []string, styles styleTable, date1904 bool) string {
	switch c.typ {
	case "s": // shared string
		idx, err := strconv.Atoi(strings.TrimSpace(c.value))
		if err != nil || idx < 0 || idx >= len(shared) {
			return ""
		}
		return shared[idx]
	case "str", "inlineStr": // formula string cache / inline string
		return c.value
	case "b":
		if strings.TrimSpace(c.value) == "1" {
			return "TRUE"
		}
		return "FALSE"
	case "e": // error cell: the cached error text ("#DIV/0!")
		return c.value
	default: // numeric
		return formatNumber(strings.TrimSpace(c.value), styles.formatCode(styles.numFmtID(c.styleIdx)), date1904)
	}
}

// formatCode resolves a numFmtId to its format code: custom codes from the
// styles part, else the builtin table (only the ids the formatter consults).
func (st styleTable) formatCode(id int) string {
	if code, ok := st.customFmt[id]; ok {
		return code
	}
	if code, ok := builtinNumFmt[id]; ok {
		return code
	}
	return "" // General
}

// builtinNumFmt is the subset of Excel's builtin number formats the formatter
// distinguishes: dates/times (14-22, 45-47) and percents (9, 10). Everything
// else renders as General.
var builtinNumFmt = map[int]string{
	9:  "0%",
	10: "0.00%",
	14: "mm-dd-yy",
	15: "d-mmm-yy",
	16: "d-mmm",
	17: "mmm-yy",
	18: "h:mm AM/PM",
	19: "h:mm:ss AM/PM",
	20: "h:mm",
	21: "h:mm:ss",
	22: "m/d/yy h:mm",
	45: "mm:ss",
	46: "[h]:mm:ss",
	47: "mmss.0",
}

// builtinNumFmtAll is the COMPLETE builtin id table (ECMA-376 §18.8.30, the
// implied formats every producer shares; 0 = General is the "" zero value).
// The display formatter deliberately keeps consulting only builtinNumFmt so
// rendered Text is unchanged; this table serves the structured path —
// Style.NumFmt resolution and date typing — and, later, code→id reuse in the
// editor.
var builtinNumFmtAll = map[int]string{
	1:  "0",
	2:  "0.00",
	3:  "#,##0",
	4:  "#,##0.00",
	9:  "0%",
	10: "0.00%",
	11: "0.00E+00",
	12: "# ?/?",
	13: "# ??/??",
	14: "mm-dd-yy",
	15: "d-mmm-yy",
	16: "d-mmm",
	17: "mmm-yy",
	18: "h:mm AM/PM",
	19: "h:mm:ss AM/PM",
	20: "h:mm",
	21: "h:mm:ss",
	22: "m/d/yy h:mm",
	37: "#,##0 ;(#,##0)",
	38: "#,##0 ;[Red](#,##0)",
	39: "#,##0.00;(#,##0.00)",
	40: "#,##0.00;[Red](#,##0.00)",
	45: "mm:ss",
	46: "[h]:mm:ss",
	47: "mmss.0",
	48: "##0.0E+0",
	49: "@",
}

// resolveNumFmt resolves a numFmtId to its pattern for the structured path:
// custom codes win, then the complete builtin table; "" is General.
func resolveNumFmt(id int, custom map[int]string) string {
	if code, ok := custom[id]; ok {
		return code
	}
	if code, ok := builtinNumFmtAll[id]; ok {
		return code
	}
	return ""
}

// typedValue derives a cell's typed cached value — the structured companion
// to displayValue (which is unchanged and keeps producing Text).
func typedValue(c rawCell, shared []string, styles styleTable, date1904 bool) Value {
	switch c.typ {
	case "s":
		idx, err := strconv.Atoi(strings.TrimSpace(c.value))
		if err != nil || idx < 0 || idx >= len(shared) {
			return Value{Kind: KindString}
		}
		return Value{Kind: KindString, S: shared[idx]}
	case "str", "inlineStr":
		return Value{Kind: KindString, S: c.value}
	case "b":
		return Value{Kind: KindBool, B: strings.TrimSpace(c.value) == "1"}
	case "e":
		return Value{Kind: KindError, S: c.value}
	default: // numeric
		raw := strings.TrimSpace(c.value)
		if raw == "" {
			return Value{} // a style-only cell holds no value
		}
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return Value{Kind: KindString, S: c.value} // malformed number: keep the text
		}
		code := resolveNumFmt(styles.numFmtID(c.styleIdx), styles.customFmt)
		if isDateCode(code) {
			return Value{Kind: KindDate, F: f, T: serialToTime(f, date1904)}
		}
		return Value{Kind: KindNumber, F: f}
	}
}

// serialToTime converts an Excel serial to a UTC time under the workbook's
// date system, rounding the fractional day to whole seconds.
func serialToTime(serial float64, date1904 bool) time.Time {
	epoch := excelEpoch1900
	if date1904 {
		epoch = excelEpoch1904
	}
	days := int(serial)
	frac := serial - float64(days)
	secs := int(frac*86400 + 0.5)
	return epoch.AddDate(0, 0, days).Add(time.Duration(secs) * time.Second)
}

// formatNumber renders a numeric cached value through its format code:
// date/time codes convert the Excel serial, percent codes scale and suffix,
// and everything else — including custom codes the formatter does not model —
// renders as Excel "General" (a documented degrade; the value is never lost).
func formatNumber(raw, code string, date1904 bool) string {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw // a malformed number passes through verbatim
	}
	switch {
	case isDateCode(code):
		return formatSerialDate(f, code, date1904)
	case strings.Contains(code, "%"):
		return trimFloat(f*100) + "%"
	default:
		return trimFloat(f)
	}
}

// trimFloat is Excel's "General": the shortest representation that round-trips,
// with plain notation for the magnitudes tables actually hold.
func trimFloat(f float64) string {
	if f == float64(int64(f)) && f >= -1e15 && f <= 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// isDateCode reports whether a format code renders a date or time: it contains
// a day/month/year/hour/second token outside of quoted literals, escapes, and
// brackets — except a bracketed ELAPSED token ([h], [mm], [ss]), which is a
// time code, unlike a color/condition bracket ([Magenta], [>=100]) whose
// letters must not match. ("General" and numeric codes contain none.)
func isDateCode(code string) bool {
	if code == "" {
		return false
	}
	inQuote := false
	for i := 0; i < len(code); i++ {
		ch := code[i]
		switch {
		case inQuote:
			if ch == '"' {
				inQuote = false
			}
		case ch == '"':
			inQuote = true
		case ch == '\\':
			i++ // skip the escaped literal
		case ch == '[':
			end := strings.IndexByte(code[i:], ']')
			if end < 0 {
				return false // malformed; treat as non-date
			}
			if isElapsedToken(code[i+1 : i+end]) {
				return true
			}
			i += end
		case ch == 'y' || ch == 'd' || ch == 'h' || ch == 's' || ch == 'm' || ch == 'Y' || ch == 'D' || ch == 'H' || ch == 'S' || ch == 'M':
			return true
		}
	}
	return false
}

// isElapsedToken reports whether a bracket's content is an elapsed-time token:
// one or more of the same h/m/s letter ("h", "mm", "ss").
func isElapsedToken(s string) bool {
	if s == "" {
		return false
	}
	c := s[0] | 0x20 // lowercase
	if c != 'h' && c != 'm' && c != 's' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i]|0x20 != c {
			return false
		}
	}
	return true
}

// excelEpoch1900 is the serial-0 moment of the 1900 date system. Excel calls
// 1900-01-01 serial 1 AND (for Lotus 1-2-3 compatibility) believes 1900 was a
// leap year; anchoring serial 0 at 1899-12-30 makes every serial from
// 1900-03-01 onward — i.e. all real-world data — convert correctly.
var excelEpoch1900 = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

// excelEpoch1904 is the serial-0 moment of the 1904 date system (old Mac Excel).
var excelEpoch1904 = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)

// formatSerialDate converts an Excel serial to a readable date/time string:
// dates as 2006-01-02, date-times as 2006-01-02 15:04, pure times as 15:04:05.
func formatSerialDate(serial float64, code string, date1904 bool) string {
	t := serialToTime(serial, date1904)

	hasDate := containsAny(strings.ToLower(stripLiterals(code)), "y", "d")
	hasTime := containsAny(strings.ToLower(stripLiterals(code)), "h", "s")
	switch {
	case serial < 1 || (hasTime && !hasDate):
		return t.Format("15:04:05")
	case hasTime:
		return t.Format("2006-01-02 15:04")
	default:
		return t.Format("2006-01-02")
	}
}

// stripLiterals removes quoted literals and escaped characters from a format
// code so token checks do not match literal text.
func stripLiterals(code string) string {
	var sb strings.Builder
	inQuote := false
	for i := 0; i < len(code); i++ {
		ch := code[i]
		switch {
		case inQuote:
			if ch == '"' {
				inQuote = false
			}
		case ch == '"':
			inQuote = true
		case ch == '\\':
			i++
		default:
			sb.WriteByte(ch)
		}
	}
	return sb.String()
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// date1904Offset is the distance between the date systems: 1904-01-01, day 0
// of the 1904 system, is serial 1462 in the 1900 system (which counts
// Excel's fictitious 1900-02-29).
const date1904Offset = 1462

// Serial1900 converts a serial from a workbook's date system to the 1900
// system: it adds 1462 days when date1904 is set and returns the serial
// unchanged otherwise. The time-of-day fraction is kept.
func Serial1900(serial float64, date1904 bool) float64 {
	if date1904 {
		return serial + date1904Offset
	}
	return serial
}

// SerialFrom1900 converts a 1900-system serial to a workbook's date system
// (the inverse of Serial1900).
func SerialFrom1900(serial float64, date1904 bool) float64 {
	if date1904 {
		return serial - date1904Offset
	}
	return serial
}
