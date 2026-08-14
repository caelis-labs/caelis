package consoleoutput

import (
	"strings"
	"testing"
)

func TestCappedBufferDecodesPowerShellCLIXML(t *testing.T) {
	t.Parallel()

	raw := "#< CLIXML\r\n" +
		`<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04">` +
		`<Obj S="progress" RefId="0"><MS><PR N="Record"><AV>Preparing modules for first use.</AV></PR></MS></Obj>` +
		`<S S="Error">Property Length cannot be found._x000D__x000A_</S>` +
		`</Objs>`
	buf := NewCappedBuffer(1024)
	if _, err := buf.Write([]byte(raw)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "Property Length cannot be found.\r\n") {
		t.Fatalf("String() = %q, want decoded PowerShell error", got)
	}
	if strings.Contains(got, "<Objs") || strings.Contains(got, "Preparing modules") {
		t.Fatalf("String() = %q, want XML/progress stripped", got)
	}
}

func TestStreamChunkStorageModes(t *testing.T) {
	t.Parallel()

	raw := []byte("hello ")
	var rawDecoder ConsoleOutputDecoder
	rawChunk := DecodeStreamChunk(&rawDecoder, raw, StoreRaw)
	if string(rawChunk.Stored) != string(raw) || string(rawChunk.Emit) != string(raw) {
		t.Fatalf("raw chunk = %#v, want raw bytes stored and emitted", rawChunk)
	}
	rawTail := FlushStreamChunk(&rawDecoder, StoreRaw)
	if len(rawTail.Stored) != 0 {
		t.Fatalf("raw flush stored = %q, want empty stored tail", rawTail.Stored)
	}

	var decodedDecoder ConsoleOutputDecoder
	decodedChunk := DecodeStreamChunk(&decodedDecoder, raw, StoreDecoded)
	if string(decodedChunk.Stored) != string(decodedChunk.Emit) {
		t.Fatalf("decoded chunk stored/emit = %q/%q, want same decoded bytes", decodedChunk.Stored, decodedChunk.Emit)
	}
}

func TestRawCappedBufferPreservesBytes(t *testing.T) {
	t.Parallel()

	buf := NewRawCappedBuffer(4)
	if _, err := buf.Write([]byte("abc")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := buf.Write([]byte("def")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got := buf.String(); got != "cdef" {
		t.Fatalf("String() = %q, want capped raw bytes", got)
	}
}

func TestCappedOutputSinceUsesAbsoluteCursor(t *testing.T) {
	t.Parallel()

	buf := AppendCappedBytes(nil, []byte("abc"), 4)
	buf = AppendCappedBytes(buf, []byte("def"), 4)
	got, cursor := CappedOutputSince(buf, 6, 3)
	if string(got) != "def" || cursor != 6 {
		t.Fatalf("CappedOutputSince() = %q/%d, want def/6", got, cursor)
	}
	got, cursor = CappedOutputSince(buf, 6, 0)
	if string(got) != "cdef" || cursor != 6 {
		t.Fatalf("CappedOutputSince(before cap) = %q/%d, want capped buffer", got, cursor)
	}
}
