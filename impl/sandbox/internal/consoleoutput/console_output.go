package consoleoutput

import (
	"encoding/binary"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ConsoleOutputDecoder normalizes Windows console output chunks to UTF-8.
// It preserves split UTF-8/UTF-16 text across pipe/frame boundaries, then
// falls back to the active Windows code page for genuinely non-UTF-8 output.
// Windows PowerShell serializes non-success streams as CLIXML when redirected;
// this also unwraps those records into terminal text.
type ConsoleOutputDecoder struct {
	pending    []byte
	utf16Order binary.ByteOrder
	clixml     string
}

func (d *ConsoleOutputDecoder) Decode(chunk []byte) []byte {
	if len(chunk) == 0 {
		return nil
	}
	data := append(append([]byte(nil), d.pending...), chunk...)
	d.pending = nil
	if d.utf16Order != nil {
		decoded, suffix := decodeUTF16ConsoleOutputWithOrder(data, d.utf16Order)
		d.pending = append(d.pending, suffix...)
		if len(suffix) == 0 {
			d.utf16Order = nil
		}
		return []byte(d.decodeConsoleText(decoded, false))
	}
	if decoded, suffix, order, ok := decodeUTF16ConsoleOutput(data); ok {
		d.pending = append(d.pending, suffix...)
		if len(suffix) > 0 {
			d.utf16Order = order
		}
		return []byte(d.decodeConsoleText(decoded, false))
	}
	if utf8.Valid(data) {
		return []byte(d.decodeConsoleText(string(data), false))
	}
	if prefix, suffix, ok := splitValidUTF8PrefixWithPendingSuffix(data); ok {
		d.pending = append(d.pending, suffix...)
		return []byte(d.decodeConsoleText(string(prefix), false))
	}
	decoded, err := DecodeConsoleOutputToUTF8(data)
	if err == nil {
		return []byte(d.decodeConsoleText(string(decoded), false))
	}
	return []byte(d.decodeConsoleText(string(data), false))
}

func (d *ConsoleOutputDecoder) Flush() []byte {
	if len(d.pending) == 0 {
		if d.clixml != "" {
			return []byte(d.decodeConsoleText("", true))
		}
		return nil
	}
	data := d.pending
	d.pending = nil
	if d.utf16Order != nil {
		decoded, _ := decodeUTF16ConsoleOutputWithOrder(data, d.utf16Order)
		d.utf16Order = nil
		return []byte(d.decodeConsoleText(decoded, true))
	}
	d.utf16Order = nil
	decoded, err := DecodeConsoleOutputToUTF8(data)
	if err == nil {
		return []byte(d.decodeConsoleText(string(decoded), true))
	}
	return []byte(d.decodeConsoleText(string(data), true))
}

func (d *ConsoleOutputDecoder) decodeConsoleText(text string, final bool) string {
	text = normalizeConsoleText(text)
	if d.clixml != "" {
		text = d.clixml + text
		d.clixml = ""
	}
	decoded, pending := decodePowerShellCLIXML(text, final)
	d.clixml = pending
	return normalizePowerShellNativeCommandErrors(decoded)
}

func decodeUTF16ConsoleOutput(data []byte) (string, []byte, binary.ByteOrder, bool) {
	if len(data) < 4 {
		return "", nil, nil, false
	}
	body := data
	var suffix []byte
	if len(body)%2 != 0 {
		suffix = append([]byte(nil), body[len(body)-1])
		body = body[:len(body)-1]
	}
	order, ok := utf16ByteOrder(body)
	if !ok {
		return "", nil, nil, false
	}
	decoded, _ := decodeUTF16ConsoleOutputWithOrder(body, order)
	return decoded, suffix, order, true
}

func decodeUTF16ConsoleOutputWithOrder(data []byte, order binary.ByteOrder) (string, []byte) {
	if order == nil {
		return "", append([]byte(nil), data...)
	}
	body := data
	var suffix []byte
	if len(body)%2 != 0 {
		suffix = append([]byte(nil), body[len(body)-1])
		body = body[:len(body)-1]
	}
	words := make([]uint16, len(body)/2)
	for i := range words {
		words[i] = order.Uint16(body[i*2 : i*2+2])
	}
	if len(words) > 0 && words[0] == 0xfeff {
		words = words[1:]
	}
	return string(utf16.Decode(words)), suffix
}

func utf16ByteOrder(data []byte) (binary.ByteOrder, bool) {
	if len(data) >= 2 {
		switch {
		case data[0] == 0xff && data[1] == 0xfe:
			return binary.LittleEndian, true
		case data[0] == 0xfe && data[1] == 0xff:
			return binary.BigEndian, true
		}
	}
	pairs := len(data) / 2
	if pairs < 2 {
		return nil, false
	}
	zeroEven := 0
	zeroOdd := 0
	for i := 0; i+1 < len(data); i += 2 {
		if data[i] == 0 {
			zeroEven++
		}
		if data[i+1] == 0 {
			zeroOdd++
		}
	}
	if zeroOdd*2 >= pairs && zeroEven*8 <= pairs {
		return binary.LittleEndian, true
	}
	if zeroEven*2 >= pairs && zeroOdd*8 <= pairs {
		return binary.BigEndian, true
	}
	return nil, false
}

func normalizeConsoleText(text string) string {
	if !strings.ContainsRune(text, '\x00') {
		return text
	}
	return strings.ReplaceAll(text, "\x00", "\n")
}

const powerShellCLIXMLMarker = "#< CLIXML"

func decodePowerShellCLIXML(text string, final bool) (string, string) {
	if text == "" {
		return "", ""
	}
	var out strings.Builder
	for text != "" {
		start := strings.Index(text, powerShellCLIXMLMarker)
		if start < 0 {
			if !final {
				if cut := powerShellCLIXMLMarkerSuffixStart(text); cut >= 0 {
					out.WriteString(text[:cut])
					return out.String(), text[cut:]
				}
			}
			out.WriteString(text)
			return out.String(), ""
		}
		out.WriteString(text[:start])
		text = text[start:]
		end := strings.Index(text, "</Objs>")
		if end < 0 {
			if final {
				out.WriteString(convertPowerShellCLIXML(text))
				return out.String(), ""
			}
			return out.String(), text
		}
		end += len("</Objs>")
		out.WriteString(convertPowerShellCLIXML(text[:end]))
		text = trimCLIXMLEnvelopeLineBreak(text[end:])
	}
	return out.String(), ""
}

func trimCLIXMLEnvelopeLineBreak(text string) string {
	if strings.HasPrefix(text, "\r\n") {
		return text[2:]
	}
	if strings.HasPrefix(text, "\n") || strings.HasPrefix(text, "\r") {
		return text[1:]
	}
	return text
}

func powerShellCLIXMLMarkerSuffixStart(text string) int {
	maxSuffix := minInt(len(text), len(powerShellCLIXMLMarker)-1)
	for size := maxSuffix; size > 0; size-- {
		if strings.HasPrefix(powerShellCLIXMLMarker, text[len(text)-size:]) {
			return len(text) - size
		}
	}
	return -1
}

func convertPowerShellCLIXML(text string) string {
	start := strings.Index(text, "<Objs")
	if start < 0 {
		return text
	}
	converted, ok, displayStream := parsePowerShellCLIXML(text[start:])
	if !ok {
		if displayStream || converted != "" {
			return converted
		}
		return text
	}
	return converted
}

func parsePowerShellCLIXML(text string) (string, bool, bool) {
	decoder := xml.NewDecoder(strings.NewReader(text))
	var out strings.Builder
	displayStream := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if out.Len() > 0 {
				return out.String(), true, displayStream
			}
			return "", false, displayStream
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "S" {
			continue
		}
		stream := clixmlStreamAttribute(start)
		if !displayCLIXMLStream(stream) {
			continue
		}
		displayStream = true
		var value string
		if err := decoder.DecodeElement(&value, &start); err != nil {
			if out.Len() > 0 {
				return out.String(), true, displayStream
			}
			return "", false, displayStream
		}
		appendCLIXMLText(&out, decodePowerShellEscapes(value))
	}
	return out.String(), true, displayStream
}

func clixmlStreamAttribute(start xml.StartElement) string {
	for _, attr := range start.Attr {
		if attr.Name.Local == "S" {
			return strings.TrimSpace(attr.Value)
		}
	}
	return ""
}

func displayCLIXMLStream(stream string) bool {
	switch strings.ToLower(strings.TrimSpace(stream)) {
	case "error", "warning", "verbose", "debug":
		return true
	default:
		return false
	}
}

func appendCLIXMLText(out *strings.Builder, text string) {
	if text == "" {
		return
	}
	if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") && !strings.HasPrefix(text, "\n") {
		out.WriteByte('\n')
	}
	out.WriteString(text)
}

func decodePowerShellEscapes(text string) string {
	if !strings.Contains(text, "_x") {
		return text
	}
	var out strings.Builder
	for i := 0; i < len(text); {
		if i+7 <= len(text) && text[i] == '_' && text[i+1] == 'x' && text[i+6] == '_' {
			if value, err := strconv.ParseInt(text[i+2:i+6], 16, 32); err == nil {
				out.WriteRune(rune(value))
				i += 7
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		out.WriteRune(r)
		i += size
	}
	return out.String()
}

func normalizePowerShellNativeCommandErrors(text string) string {
	if !strings.Contains(text, "NativeCommandError") || !strings.Contains(text, "FullyQualifiedErrorId") {
		return text
	}
	lines := splitConsoleLines(text)
	var out strings.Builder
	for i := 0; i < len(lines); {
		if end, ok := nativeCommandErrorBlockEnd(lines, i+1); ok {
			body, ending := trimConsoleLineEnding(lines[i])
			if command, message, found := strings.Cut(body, " : "); found && strings.TrimSpace(command) != "" {
				out.WriteString(message)
				out.WriteString(ending)
				i = end
				continue
			}
		}
		if isNativeCommandErrorMetadataLine(lines[i]) {
			i++
			continue
		}
		out.WriteString(lines[i])
		i++
	}
	return out.String()
}

func nativeCommandErrorBlockEnd(lines []string, start int) (int, bool) {
	limit := start + 8
	if limit > len(lines) {
		limit = len(lines)
	}
	for i := start; i < limit; i++ {
		line := lines[i]
		if strings.Contains(line, "FullyQualifiedErrorId") && strings.Contains(line, "NativeCommandError") {
			return i + 1, true
		}
	}
	return 0, false
}

func isNativeCommandErrorMetadataLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "+ CategoryInfo") ||
		strings.Contains(trimmed, "FullyQualifiedErrorId") && strings.Contains(trimmed, "NativeCommandError")
}

func splitConsoleLines(text string) []string {
	if text == "" {
		return nil
	}
	var lines []string
	for text != "" {
		index := strings.IndexByte(text, '\n')
		if index < 0 {
			lines = append(lines, text)
			break
		}
		lines = append(lines, text[:index+1])
		text = text[index+1:]
	}
	return lines
}

func trimConsoleLineEnding(line string) (string, string) {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return line[:len(line)-2], "\r\n"
	case strings.HasSuffix(line, "\n"):
		return line[:len(line)-1], "\n"
	case strings.HasSuffix(line, "\r"):
		return line[:len(line)-1], "\r"
	default:
		return line, ""
	}
}

func splitValidUTF8PrefixWithPendingSuffix(data []byte) ([]byte, []byte, bool) {
	for cut := len(data) - 1; cut >= 0 && len(data)-cut < utf8.UTFMax; cut-- {
		if !utf8.Valid(data[:cut]) {
			continue
		}
		suffix := data[cut:]
		if !looksLikeIncompleteUTF8(suffix) {
			continue
		}
		return append([]byte(nil), data[:cut]...), append([]byte(nil), suffix...), true
	}
	return nil, nil, false
}

func looksLikeIncompleteUTF8(data []byte) bool {
	if len(data) == 0 || len(data) >= utf8.UTFMax {
		return false
	}
	need := utf8SequenceLength(data[0])
	if need <= len(data) {
		return false
	}
	for _, b := range data[1:] {
		if b < 0x80 || b > 0xbf {
			return false
		}
	}
	return true
}

func utf8SequenceLength(b byte) int {
	switch {
	case b >= 0xc2 && b <= 0xdf:
		return 2
	case b >= 0xe0 && b <= 0xef:
		return 3
	case b >= 0xf0 && b <= 0xf4:
		return 4
	default:
		return 0
	}
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
