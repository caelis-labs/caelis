package tuiapp

import "testing"

func TestToolDisplayArgsHidesMetadataOnlyListArgs(t *testing.T) {
	t.Parallel()

	if got := toolDisplayArgs("LIST", map[string]any{"metadata": true}); got != "" {
		t.Fatalf("toolDisplayArgs(LIST metadata) = %q, want empty", got)
	}
}

func TestToolDisplayResultHeaderPreservesSignedNonDiffLine(t *testing.T) {
	t.Parallel()

	output := "+1 for the win\nfallback"

	if got := toolDisplayResultHeader("PATCH", output); got != "+1 for the win" {
		t.Fatalf("toolDisplayResultHeader() = %q, want first non-diff signed line", got)
	}
}

func TestToolDisplayResultHeaderSkipsStandardDiffBody(t *testing.T) {
	t.Parallel()

	output := "-old line\n+new line"

	if got := toolDisplayResultHeader("PATCH", output); got != "" {
		t.Fatalf("toolDisplayResultHeader() = %q, want empty header for pure standard diff body", got)
	}
	if got := toolDisplayPanelOutput("PATCH", output); got != output {
		t.Fatalf("toolDisplayPanelOutput() = %q, want standard diff body preserved", got)
	}
}
