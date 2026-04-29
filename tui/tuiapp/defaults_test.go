package tuiapp

import (
	"reflect"
	"testing"
)

func TestDefaultCommandsExposeCanonicalCoreCommandsOnly(t *testing.T) {
	got := DefaultCommands()
	want := []string{
		"help",
		"agent",
		"connect",
		"model",
		"sandbox",
		"status",
		"new",
		"resume",
		"compact",
		"exit",
		"quit",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultCommands() = %#v, want %#v", got, want)
	}
}

func TestDefaultWizardsCoverCoreConfigFlows(t *testing.T) {
	wizards := DefaultWizards()
	if len(wizards) < 1 {
		t.Fatalf("expected core wizards, got %d", len(wizards))
	}
}

func TestDefaultConnectWizardMatchesLegacyStepShape(t *testing.T) {
	var connect *WizardDef
	for i := range DefaultWizards() {
		if DefaultWizards()[i].Command == "connect" {
			connect = &DefaultWizards()[i]
			break
		}
	}
	if connect == nil {
		t.Fatalf("connect wizard not found")
	}
	if connect.DisplayLine != "/connect" {
		t.Fatalf("DisplayLine = %q, want /connect", connect.DisplayLine)
	}
	if got := len(connect.Steps); got != 8 {
		t.Fatalf("connect wizard step count = %d, want 8", got)
	}
	keys := make([]string, 0, len(connect.Steps))
	for _, step := range connect.Steps {
		keys = append(keys, step.Key)
	}
	want := []string{"provider", "endpoint", "baseurl", "apikey", "model", "context_window_tokens", "max_output_tokens", "reasoning_levels"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("connect wizard steps = %#v, want %#v", keys, want)
	}
}

func TestDefaultCommandsHideBTWFromDefaultTUI(t *testing.T) {
	for _, command := range DefaultCommands() {
		if command == "btw" {
			t.Fatalf("DefaultCommands() unexpectedly includes hidden command %q", command)
		}
	}
}
