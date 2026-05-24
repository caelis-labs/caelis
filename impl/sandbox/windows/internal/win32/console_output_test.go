package win32

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestConsoleOutputDecoderKeepsSplitUTF8RuneIntact(t *testing.T) {
	t.Parallel()

	raw := []byte("hello \u4f60\u597d\n")
	var decoder ConsoleOutputDecoder
	first := decoder.Decode(raw[:7])
	if string(first) != "hello " {
		t.Fatalf("first Decode() = %q, want prefix before split rune", string(first))
	}
	second := decoder.Decode(raw[7:])
	if string(second) != "\u4f60\u597d\n" {
		t.Fatalf("second Decode() = %q, want completed UTF-8 tail", string(second))
	}
	if tail := decoder.Flush(); len(tail) != 0 {
		t.Fatalf("Flush() = %q, want empty", string(tail))
	}
}

func TestConsoleOutputDecoderFlushesPendingUTF8(t *testing.T) {
	t.Parallel()

	raw := []byte("\u4f60")
	var decoder ConsoleOutputDecoder
	if got := decoder.Decode(raw[:1]); len(got) != 0 {
		t.Fatalf("Decode(partial) = %q, want pending only", string(got))
	}
	if got := decoder.Decode(raw[1:]); string(got) != "\u4f60" {
		t.Fatalf("Decode(rest) = %q, want completed rune", string(got))
	}
}

func TestConsoleOutputDecoderDecodesUTF16LE(t *testing.T) {
	t.Parallel()

	raw := utf16LEBytes("caelis\r\ncodex\r\ndemo\r\n")
	var decoder ConsoleOutputDecoder
	first := decoder.Decode(raw[:len(raw)-1])
	second := decoder.Decode(raw[len(raw)-1:])
	if string(first)+string(second) != "caelis\r\ncodex\r\ndemo\r\n" {
		t.Fatalf("Decode(split utf16) = %q + %q, want decoded text", string(first), string(second))
	}
	if tail := decoder.Flush(); len(tail) != 0 {
		t.Fatalf("Flush() = %q, want empty", string(tail))
	}
}

func TestConsoleOutputDecoderNormalizesNULSeparators(t *testing.T) {
	t.Parallel()

	var decoder ConsoleOutputDecoder
	got := decoder.Decode([]byte("caelis\x00codex\x00demo"))
	if string(got) != "caelis\ncodex\ndemo" {
		t.Fatalf("Decode(NUL separators) = %q, want newline separators", string(got))
	}
}

func TestConsoleOutputDecoderUnwrapsPowerShellCLIXMLError(t *testing.T) {
	t.Parallel()

	npmError := "npm : \u65e0\u6cd5\u5c06\u201cnpm\u201d\u9879\u8bc6\u522b\u4e3a cmdlet"
	locationError := "\u6240\u5728\u4f4d\u7f6e \u884c:1 \u5b57\u7b26: 177"
	raw := "#< CLIXML\r\n" +
		`<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04">` +
		`<Obj S="progress" RefId="0"><MS><PR N="Record"><AV>Preparing modules for first use.</AV></PR></MS></Obj>` +
		`<S S="Error">` + npmError + `_x000D__x000A_</S>` +
		`<S S="Error">` + locationError + `_x000D__x000A_</S>` +
		`</Objs>`
	var decoder ConsoleOutputDecoder
	got := string(decoder.Decode([]byte(raw)))
	if !strings.Contains(got, npmError+"\r\n") ||
		!strings.Contains(got, locationError+"\r\n") {
		t.Fatalf("Decode(CLIXML error) = %q, want decoded error text", got)
	}
	if strings.Contains(got, "<Objs") || strings.Contains(got, "Preparing modules") {
		t.Fatalf("Decode(CLIXML error) = %q, want XML/progress stripped", got)
	}
}

func TestConsoleOutputDecoderDropsPowerShellCLIXMLWriteHostMirror(t *testing.T) {
	t.Parallel()

	header := "\u5f53\u524d\u76ee\u5f55\u5185\u5bb9:"
	raw := header + "\r\n" +
		"#< CLIXML\r\n" +
		`<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04">` +
		`<Obj S="information" RefId="1"><TN RefId="1"><T>System.Management.Automation.InformationRecord</T></TN>` +
		`<ToString>` + header + `</ToString><Props><S N="Source">Write-Host</S></Props></Obj>` +
		"</Objs>\r\n"
	var decoder ConsoleOutputDecoder
	got := string(decoder.Decode([]byte(raw)))
	if got != header+"\r\n" {
		t.Fatalf("Decode(CLIXML Write-Host mirror) = %q, want only plain host output", got)
	}
}

func TestConsoleOutputDecoderKeepsSplitPowerShellCLIXMLPending(t *testing.T) {
	t.Parallel()

	var decoder ConsoleOutputDecoder
	first := decoder.Decode([]byte("before\r\n#< CLI"))
	if string(first) != "before\r\n" {
		t.Fatalf("Decode(CLIXML prefix) = %q, want text before pending marker", string(first))
	}
	errText := "\u9519\u8bef"
	second := decoder.Decode([]byte(`XML
<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04"><S S="Error">` + errText + `_x000D__x000A_</S></Objs>after`))
	if string(second) != errText+"\r\nafter" {
		t.Fatalf("Decode(CLIXML suffix) = %q, want decoded CLIXML then trailing text", string(second))
	}
}

func utf16LEBytes(text string) []byte {
	words := utf16.Encode([]rune(text))
	out := make([]byte, len(words)*2)
	for i, word := range words {
		binary.LittleEndian.PutUint16(out[i*2:], word)
	}
	return out
}
