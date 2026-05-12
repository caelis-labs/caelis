package tuikit

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestLinkifyText_LeavesURLsAsPlainText(t *testing.T) {
	got := LinkifyText("visit https://example.com/docs", lipgloss.NewStyle())
	if got != "visit https://example.com/docs" {
		t.Fatalf("expected plain text URL, got %q", got)
	}
	if strings.Contains(got, "\x1b]8;;") {
		t.Fatalf("did not expect OSC 8 hyperlink, got %q", got)
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

func TestLinkifyText_StripsBrokenOSC8ResidueWithoutRelinking(t *testing.T) {
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
