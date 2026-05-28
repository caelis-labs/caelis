//go:build windows

package tuiapp

import (
	"testing"
	"unicode/utf16"
)

func TestWindowsClipboardUTF16PreservesChineseText(t *testing.T) {
	const text = "原始的错误出来了"
	got := windowsClipboardUTF16(text)
	if len(got) == 0 || got[len(got)-1] != 0 {
		t.Fatalf("UTF-16 clipboard text missing NUL terminator: %#v", got)
	}
	if decoded := string(utf16.Decode(got[:len(got)-1])); decoded != text {
		t.Fatalf("decoded UTF-16 clipboard text = %q, want %q", decoded, text)
	}
}
