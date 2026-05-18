package commands

import (
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultNamesExposeCanonicalCoreCommandsOnly(t *testing.T) {
	got := DefaultNames()
	want := []string{
		"help",
		"agent",
		"connect",
		"model",
		"approval",
		"status",
		"doctor",
		"new",
		"resume",
		"compact",
		"exit",
		"quit",
	}
	if runtime.GOOS == "windows" {
		want = append(want[:5], append([]string{"sandbox"}, want[5:]...)...)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultNames() = %#v, want %#v", got, want)
	}
}

func TestHelpTextUsesRegistrySpecs(t *testing.T) {
	got := HelpText([]string{"help", "agent", "custom"})
	for _, want := range []string{"/help  Show available slash commands", "/agent list", "/custom"} {
		if !strings.Contains(got, want) {
			t.Fatalf("HelpText() = %q, want %q", got, want)
		}
	}
}

func TestLocalDuringACPMatchesLegacyLocalCommands(t *testing.T) {
	local := []string{"help", "agent", "status", "doctor", "resume", "model", "approval", "exit", "quit"}
	if runtime.GOOS == "windows" {
		local = append(local, "sandbox")
	}
	for _, name := range local {
		if !IsLocalDuringACP(name) {
			t.Fatalf("IsLocalDuringACP(%q) = false, want true", name)
		}
	}
	remote := []string{"connect", "new", "compact"}
	if runtime.GOOS != "windows" {
		remote = append(remote, "sandbox")
	}
	for _, name := range remote {
		if IsLocalDuringACP(name) {
			t.Fatalf("IsLocalDuringACP(%q) = true, want false", name)
		}
	}
}

func TestRootArgCandidatesReturnsCopies(t *testing.T) {
	first := RootArgCandidates("model")
	if len(first) == 0 {
		t.Fatal("RootArgCandidates(model) returned no candidates")
	}
	first[0].Value = "mutated"
	second := RootArgCandidates("model")
	if second[0].Value == "mutated" {
		t.Fatalf("RootArgCandidates(model) leaked mutable backing slice: %#v", second)
	}
}
