package uipreferences

import "testing"

func TestMergeCopiesNonzeroFieldsAndLeavesZerosUnchanged(t *testing.T) {
	base := Preferences{SubagentLayout: Left, HorizontalRatio: 40, VerticalRatio: 45, Theme: "nord"}
	if got := base.Merge(Preferences{}); got != base {
		t.Fatalf("empty merge=%#v", got)
	}
	got := base.Merge(Preferences{Theme: "catppuccin"})
	want := Preferences{SubagentLayout: Left, HorizontalRatio: 40, VerticalRatio: 45, Theme: "catppuccin"}
	if got != want {
		t.Fatalf("theme merge=%#v", got)
	}
	got = base.Merge(Preferences{SubagentLayout: Overlay, HorizontalRatio: DefaultHorizontalRatio, VerticalRatio: DefaultVerticalRatio})
	want = Preferences{SubagentLayout: Overlay, HorizontalRatio: DefaultHorizontalRatio, VerticalRatio: DefaultVerticalRatio, Theme: "nord"}
	if got != want {
		t.Fatalf("explicit defaults merge=%#v", got)
	}
}

func TestWithDefaultsDoesNotSelectTheme(t *testing.T) {
	got := Preferences{}.WithDefaults()
	if got.Theme != "" || got.SubagentLayout != Overlay || got.HorizontalRatio != DefaultHorizontalRatio || got.VerticalRatio != DefaultVerticalRatio {
		t.Fatalf("defaults=%#v", got)
	}
	if got := (Preferences{Theme: "auto"}).WithDefaults(); got.Theme != "auto" {
		t.Fatalf("retained theme=%#v", got)
	}
}

func TestValidateAcceptsOpaqueThemeAndRejectsBadLayout(t *testing.T) {
	if err := (Preferences{Theme: "not-a-palette"}).Validate(); err != nil {
		t.Fatalf("opaque theme: %v", err)
	}
	if err := (Preferences{Theme: "catppuccin"}).Validate(); err != nil {
		t.Fatalf("adaptive catppuccin: %v", err)
	}
	if err := (Preferences{SubagentLayout: "grid"}).Validate(); err == nil {
		t.Fatal("accepted invalid layout")
	}
}
