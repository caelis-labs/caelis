//go:build windows

package consoleoutput

import "testing"

func TestDecodeCodePageToUTF8(t *testing.T) {
	gbkDate := []byte{0x32, 0x30, 0x32, 0x36, 0xc4, 0xea, 0x35, 0xd4, 0xc2, 0x31, 0x39, 0xc8, 0xd5, 0x20, 0x31, 0x37, 0x3a, 0x32, 0x31, 0x3a, 0x34, 0x34}
	got, err := decodeCodePageToUTF8(936, gbkDate)
	if err != nil {
		t.Fatalf("decodeCodePageToUTF8() error = %v", err)
	}
	want := "2026\u5e745\u670819\u65e5 17:21:44"
	if string(got) != want {
		t.Fatalf("decodeCodePageToUTF8() = %q, want %q", string(got), want)
	}
}
