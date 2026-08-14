package argparse

import "testing"

func TestStringPreservesOuterWhitespace(t *testing.T) {
	got, err := String(map[string]any{"value": "  indented\n"}, "value", true)
	if err != nil {
		t.Fatalf("String() error = %v", err)
	}
	if got != "  indented\n" {
		t.Fatalf("String() = %q, want original value", got)
	}
}

func TestStringRequiredRejectsOnlyEmptyString(t *testing.T) {
	got, err := String(map[string]any{"value": "   "}, "value", true)
	if err != nil {
		t.Fatalf("String() error = %v, want whitespace-only value accepted", err)
	}
	if got != "   " {
		t.Fatalf("String() = %q, want whitespace-only value", got)
	}

	if _, err := String(map[string]any{"value": ""}, "value", true); err == nil {
		t.Fatal("String() error = nil, want empty string rejected")
	}
}
