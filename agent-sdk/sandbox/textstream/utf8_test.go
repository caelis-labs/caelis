package textstream

import (
	"testing"
	"unicode/utf8"
)

func TestUTF8DecoderKeepsSplitRuneIntact(t *testing.T) {
	var decoder UTF8Decoder
	raw := []byte("中文")
	first := decoder.Decode(raw[:4])
	if first != "中" {
		t.Fatalf("first Decode() = %q, want first complete rune", first)
	}
	second := decoder.Decode(raw[4:])
	if second != "文" {
		t.Fatalf("second Decode() = %q, want second complete rune", second)
	}
	if tail := decoder.Flush(); tail != "" {
		t.Fatalf("Flush() = %q, want empty tail", tail)
	}
}

func TestUTF8DecoderReplacesInvalidBytesWithoutEmittingInvalidStrings(t *testing.T) {
	var decoder UTF8Decoder
	got := decoder.Decode([]byte{'o', 'k', 0xff, '!'}) + decoder.Flush()
	if !utf8.ValidString(got) || got != "ok\uFFFD!" {
		t.Fatalf("decoded text = %q valid=%v, want deterministic replacement", got, utf8.ValidString(got))
	}
}

func TestUTF8DecoderFlushesIncompleteRuneAsReplacement(t *testing.T) {
	var decoder UTF8Decoder
	if got := decoder.Decode([]byte{0xe4, 0xb8}); got != "" {
		t.Fatalf("Decode() = %q, want incomplete suffix buffered", got)
	}
	if got := decoder.Flush(); got != "\uFFFD" || !utf8.ValidString(got) {
		t.Fatalf("Flush() = %q, want one valid replacement rune", got)
	}
}
