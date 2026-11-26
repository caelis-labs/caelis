package consoleoutput

import (
	"fmt"
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

	buf := AppendCappedBytes(make([]byte, 0, 8), []byte("abc"), 4)
	buf = AppendCappedBytes(buf, []byte("def"), 4)
	got, cursor := CappedOutputSince(buf, 6, 3)
	if string(got) != "def" || cursor != 6 {
		t.Fatalf("CappedOutputSince() = %q/%d, want def/6", got, cursor)
	}
	got, cursor = CappedOutputSince(buf, 6, 0)
	if string(got) != "cdef" || cursor != 6 {
		t.Fatalf("CappedOutputSince(before cap) = %q/%d, want cdef/6", got, cursor)
	}
	buf = AppendCappedBytes(buf, []byte("ghijk"), 4)
	next, cursor := CappedOutputSince(buf, 11, cursor)
	if string(next) != "hijk" || cursor != 11 || string(got) != "cdef" {
		t.Fatalf("retained output = %q/%d, previous read = %q, want hijk/11 and cdef", next, cursor, got)
	}
}

func BenchmarkAppendCappedBytes(b *testing.B) {
	for _, capacity := range []int{64 << 10, 1 << 20} {
		for _, chunkSize := range []int{64, 4 << 10, 32 << 10} {
			b.Run(fmt.Sprintf("cap=%d/chunk=%d", capacity, chunkSize), func(b *testing.B) {
				chunk := make([]byte, chunkSize)
				buf := AppendCappedBytes(nil, make([]byte, capacity), capacity)
				buf = AppendCappedBytes(buf, chunk, capacity)
				b.SetBytes(int64(chunkSize))
				b.ReportAllocs()
				for b.Loop() {
					buf = AppendCappedBytes(buf, chunk, capacity)
				}
			})
		}
	}
}

func BenchmarkCappedOutputSince(b *testing.B) {
	const capacity = 1 << 20
	const total = 2 * capacity
	buf := make([]byte, capacity)
	for _, readSize := range []int{4 << 10, capacity} {
		b.Run(fmt.Sprintf("read=%d", readSize), func(b *testing.B) {
			b.SetBytes(int64(readSize))
			b.ReportAllocs()
			for b.Loop() {
				_, _ = CappedOutputSince(buf, total, int64(total-readSize))
			}
		})
	}
}
