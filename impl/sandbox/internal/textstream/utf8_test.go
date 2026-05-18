package textstream

import "testing"

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
