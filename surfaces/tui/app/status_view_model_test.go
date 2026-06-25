package tuiapp

import (
	"testing"
)

func TestFormatStatusContextDisplayStripsLegacyCtxPrefix(t *testing.T) {
	got := formatStatusContextDisplay("ctx 4.9k / 1.0m · 0%")
	if got != "4.9k / 1.0m · 0%" {
		t.Fatalf("formatStatusContextDisplay() = %q, want legacy ctx prefix stripped", got)
	}
}
