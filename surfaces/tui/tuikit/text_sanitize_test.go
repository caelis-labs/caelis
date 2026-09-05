package tuikit

import (
	"github.com/charmbracelet/x/ansi"
	"net/url"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestLinkifyText_AddsOSC8ForHTTPURLs(t *testing.T) {
	got := LinkifyText("visit https://example.com/docs", lipgloss.NewStyle())
	if !strings.Contains(got, "\x1b]8;;https://example.com/docs") {
		t.Fatalf("expected OSC 8 hyperlink, got %q", got)
	}
	if !strings.Contains(got, "https://example.com/docs") {
		t.Fatalf("expected visible URL, got %q", got)
	}
}

func TestLinkifyText_LeavesTrailingPunctuationOutsideLink(t *testing.T) {
	got := LinkifyText("see (https://example.com/docs).", lipgloss.NewStyle())
	if strings.Contains(got, "https://example.com/docs).\x1b") {
		t.Fatalf("expected punctuation outside hyperlink, got %q", got)
	}
	if !strings.HasSuffix(got, ").") {
		t.Fatalf("expected punctuation preserved, got %q", got)
	}
}

func TestLinkifyText_LeavesPathsAsPlainText(t *testing.T) {
	got := LinkifyText("./theme_test.go", lipgloss.NewStyle())
	if got != "./theme_test.go" {
		t.Fatalf("expected plain text path, got %q", got)
	}
	if strings.Contains(got, "\x1b]8;;") {
		t.Fatalf("did not expect OSC 8 hyperlink, got %q", got)
	}
}

func TestLinkifyText_StripsBrokenFileOSC8ResidueWithoutRelinking(t *testing.T) {
	input := "]8;;file:///opt/homebrew/bin/acpx\x07/opt/homebrew/bin/acpx]8;;\x07"
	got := LinkifyText(input, lipgloss.NewStyle())
	if strings.Contains(got, input) {
		t.Fatalf("expected broken OSC8 residue removed, got %q", got)
	}
	if !strings.Contains(got, "/opt/homebrew/bin/acpx") {
		t.Fatalf("expected file label preserved, got %q", got)
	}
	if strings.Contains(got, "\x1b]8;;") {
		t.Fatalf("did not expect cleaned path to be re-linkified, got %q", got)
	}
}

func TestLinkifyText_PreservesExistingOSC8WithoutNesting(t *testing.T) {
	for _, input := range []string{
		"\x1b]8;;https://example.com/docs\aExample docs\x1b]8;;\a",
		"\x1b]8;;https://example.com/docs\x1b\\Example docs\x1b]8;;\x1b\\",
		"\x1b]8;id=123;https://example.com/docs\aExample docs\x1b]8;;\a",
	} {
		got := LinkifyText(input, lipgloss.NewStyle())
		if got != input {
			t.Fatalf("existing OSC 8 link changed:\n got %q\nwant %q", got, input)
		}
		if strings.Count(got, "https://example.com/docs") != 1 {
			t.Fatalf("existing OSC 8 link was nested: %q", got)
		}
	}
}

func TestLinkifyTextUnicodeTargetPreservesVisibleText(t *testing.T) {
	for _, input := range []string{
		"https://example.com/服务/文件?q=审批#当前",
		"[源码](https://example.com/file.go)：服务与审批。",
		"https://例子.example/路径?q=%E6%96%87#章节",
	} {
		t.Run(input, func(t *testing.T) {
			got := LinkifyText(input, lipgloss.NewStyle())
			if visible := ansi.Strip(got); visible != input {
				t.Fatalf("visible text changed: got %q, want %q", visible, input)
			}
			start := strings.Index(got, "\x1b]8;;")
			if start < 0 {
				t.Fatal("missing hyperlink")
			}
			end, _ := oscSequenceEnd(got, start+len("\x1b]8;;"))
			target := got[start+len("\x1b]8;;") : end]
			for _, b := range []byte(target) {
				if b >= 0x80 {
					t.Fatalf("non-ASCII OSC target %q", target)
				}
			}
			decoded, err := url.PathUnescape(target)
			decodedInput, inputErr := url.PathUnescape(input)
			if err != nil || inputErr != nil || !strings.Contains(decodedInput, decoded) {
				t.Fatalf("target does not round trip: %q, %v", decoded, err)
			}
		})
	}
}
