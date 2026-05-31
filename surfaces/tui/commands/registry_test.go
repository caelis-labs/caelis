package commands

import (
	"reflect"
	"strings"
	"testing"
)

func TestDefaultNamesExposeCanonicalCoreCommandsOnly(t *testing.T) {
	got := DefaultNames()
	want := []string{
		"help",
		"agent",
		"connect",
		"controller",
		"model",
		"approval",
		"status",
		"settings",
		"task",
		"doctor",
		"new",
		"resume",
		"compact",
		"exit",
		"quit",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultNames() = %#v, want %#v", got, want)
	}
}

func TestHelpTextUsesRegistrySpecs(t *testing.T) {
	got := HelpText([]string{"help", "agent", "custom"})
	for _, want := range []string{"/help", "Show commands and shortcuts", "/agent <action>", "actions: list", "/custom <prompt>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("HelpText() = %q, want %q", got, want)
		}
	}
}

func TestLocalDuringACPMatchesLegacyLocalCommands(t *testing.T) {
	local := []string{"help", "agent", "controller", "status", "settings", "task", "doctor", "resume", "model", "approval", "exit", "quit"}
	for _, name := range local {
		if !IsLocalDuringACP(name) {
			t.Fatalf("IsLocalDuringACP(%q) = false, want true", name)
		}
	}
	remote := []string{"connect", "new", "compact", "sandbox"}
	for _, name := range remote {
		if IsLocalDuringACP(name) {
			t.Fatalf("IsLocalDuringACP(%q) = true, want false", name)
		}
	}
}
