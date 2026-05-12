package tuikit

import (
	"strings"
	"testing"
)

func TestSanitizeLogTextEmpty(t *testing.T) {
	if SanitizeLogText("") != "" {
		t.Error("expected empty")
	}
}

func TestSanitizeLogTextPreservesNewlines(t *testing.T) {
	got := SanitizeLogText("line1\nline2\nline3")
	if got != "line1\nline2\nline3" {
		t.Errorf("expected preserved newlines, got %q", got)
	}
}

func TestSanitizeLogTextExpandsTab(t *testing.T) {
	got := SanitizeLogText("a\tb")
	if got != "a    b" {
		t.Errorf("expected tab expansion, got %q", got)
	}
}

func TestSanitizeLogTextStripsANSICSI(t *testing.T) {
	// ESC[31m = red, ESC[0m = reset
	input := "\x1b[31mred text\x1b[0m"
	got := SanitizeLogText(input)
	if got != "red text" {
		t.Errorf("expected 'red text', got %q", got)
	}
}

func TestSanitizeLogTextStripsANSIOSC(t *testing.T) {
	// OSC with BEL
	input := "\x1b]0;title\x07normal text"
	got := SanitizeLogText(input)
	if got != "normal text" {
		t.Errorf("expected 'normal text', got %q", got)
	}
}

func TestSanitizeLogTextStripsControlChars(t *testing.T) {
	input := "hello\x01\x02\x03world"
	got := SanitizeLogText(input)
	if got != "helloworld" {
		t.Errorf("expected 'helloworld', got %q", got)
	}
}

func TestSanitizeLogTextStripsDEL(t *testing.T) {
	input := "abc\x7fdef"
	got := SanitizeLogText(input)
	if got != "abcdef" {
		t.Errorf("expected 'abcdef', got %q", got)
	}
}

func TestSanitizeLogTextPreservesUTF8(t *testing.T) {
	input := "Hello 中文 世界 🌍"
	got := SanitizeLogText(input)
	if got != input {
		t.Errorf("expected %q, got %q", input, got)
	}
}

func TestSanitizeLogTextComplexMix(t *testing.T) {
	input := "\x1b[1;32m* assistant output\x1b[0m\n\x1b[33mwarn: careful\x1b[0m"
	got := SanitizeLogText(input)
	if !strings.Contains(got, "* assistant output") {
		t.Errorf("expected assistant text in %q", got)
	}
	if !strings.Contains(got, "warn: careful") {
		t.Errorf("expected warn text in %q", got)
	}
	// Should not contain any ESC characters.
	if strings.ContainsRune(got, '\x1b') {
		t.Errorf("unexpected ESC in sanitized output: %q", got)
	}
}

func TestSkipANSISequenceCSI(t *testing.T) {
	input := "\x1b[31m"
	end := skipANSISequence(input, 0)
	if end != len(input) {
		t.Errorf("expected end=%d, got %d", len(input), end)
	}
}

func TestSkipANSISequenceOSC(t *testing.T) {
	input := "\x1b]0;title\x07rest"
	end := skipANSISequence(input, 0)
	// Should skip past the BEL.
	if end != 10 {
		t.Errorf("expected end=10, got %d", end)
	}
}

func TestSkipANSISequenceTwoByte(t *testing.T) {
	input := "\x1bM"
	end := skipANSISequence(input, 0)
	if end != 2 {
		t.Errorf("expected end=2, got %d", end)
	}
}

// TestSkipANSISequenceBeforeMultibyte verifies that a bare ESC followed by a
// multibyte UTF-8 rune (e.g. a Chinese character) does not consume the first
// byte of the rune.  The two-byte-escape handler must stop at ESC itself so
// the main SanitizeLogText loop can decode the rune correctly.
func TestSkipANSISequenceBeforeMultibyte(t *testing.T) {
	// ESC immediately followed by '你' (0xE4 0xBD 0xA0).
	input := "\x1b你"
	end := skipANSISequence(input, 0)
	// The function should advance past ESC only (index 1), not past ESC + 0xE4.
	if end != 1 {
		t.Errorf("skipANSISequence before multibyte rune: expected end=1, got %d", end)
	}
}

// TestSanitizeLogTextPreservesChineseAfterEsc verifies that a stray ESC byte
// before Chinese text does not cause the following character to be dropped.
func TestSanitizeLogTextPreservesChineseAfterEsc(t *testing.T) {
	// A bare ESC in the middle of Chinese text must not eat the next rune.
	got := SanitizeLogText("你好\x1b世界")
	if got != "你好世界" {
		t.Errorf("expected %q, got %q", "你好世界", got)
	}
}
