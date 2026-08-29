package controlprompt

import (
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/agentbinding"
)

func TestDefaultNamesExposePlatformCoreCommandsOnly(t *testing.T) {
	got := DefaultNamesForPlatform("linux")
	want := []string{
		"help",
		"review",
		"breeze",
		"orbit",
		"zenith",
		"connect",
		"disconnect",
		"subagent",
		"plugin",
		"model",
		"status",
		"new",
		"resume",
		"compact",
		"exit",
		"quit",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultNamesForPlatform(linux) = %#v, want %#v", got, want)
	}
	windows := DefaultNamesForPlatform("windows")
	if !sliceContainsString(windows, "doctor") {
		t.Fatalf("DefaultNamesForPlatform(windows) = %#v, want doctor", windows)
	}
	if IsKnownForPlatform("doctor", "linux") {
		t.Fatal("IsKnownForPlatform(doctor, linux) = true, want false")
	}
	if !IsKnownForPlatform("doctor", "windows") {
		t.Fatal("IsKnownForPlatform(doctor, windows) = false, want true")
	}
}

func TestProductCommandNamesAreReservedFromCustomRoles(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		for _, spec := range DefaultSpecsForPlatform(goos) {
			if err := agentbinding.ValidateCustomHandle(agentbinding.Handle(spec.Name)); err == nil {
				t.Errorf("ValidateCustomHandle(%q) succeeded for %s product command", spec.Name, goos)
			}
		}
	}
	for _, name := range []string{"lead", "sandbox"} {
		if err := agentbinding.ValidateCustomHandle(agentbinding.Handle(name)); err == nil {
			t.Errorf("ValidateCustomHandle(%q) succeeded for reserved remote command", name)
		}
	}
}

func TestDefaultSharedNamesExcludeTUIPrivateCommands(t *testing.T) {
	got := DefaultSharedNamesForPlatform("linux")
	for _, want := range []string{"help", "review", "breeze", "orbit", "zenith", "model", "status", "new", "resume", "compact"} {
		if !sliceContainsString(got, want) {
			t.Fatalf("DefaultSharedNamesForPlatform(linux) = %#v, want %q", got, want)
		}
	}
	for _, hidden := range []string{"connect", "disconnect", "subagent", "plugin", "exit", "quit"} {
		if sliceContainsString(got, hidden) {
			t.Fatalf("DefaultSharedNamesForPlatform(linux) = %#v, should exclude TUI-private %q", got, hidden)
		}
		if IsSharedKnownForPlatform(hidden, "linux") {
			t.Fatalf("IsSharedKnownForPlatform(%q, linux) = true, want false", hidden)
		}
	}
}

func TestDefaultACPNamesExposeACPPromptCommandsOnly(t *testing.T) {
	got := DefaultACPNamesForPlatform("linux")
	want := []string{"status", "breeze", "orbit", "zenith", "compact", "review"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultACPNamesForPlatform(linux) = %#v, want %#v", got, want)
	}
	for _, hidden := range []string{"help", "agent", "subagent", "model", "new", "resume", "connect", "disconnect", "plugin", "exit", "quit"} {
		if sliceContainsString(got, hidden) {
			t.Fatalf("DefaultACPNamesForPlatform(linux) = %#v, should exclude %q", got, hidden)
		}
		if IsACPKnownForPlatform(hidden, "linux") {
			t.Fatalf("IsACPKnownForPlatform(%q, linux) = true, want false", hidden)
		}
	}
}

func TestHelpSnapshotUsesRegistrySpecs(t *testing.T) {
	got := HelpSnapshot([]string{"help", "breeze", "review", "custom"})
	if len(got.Items) != 4 {
		t.Fatalf("HelpSnapshot() items = %#v, want four entries", got.Items)
	}
	for index, want := range []string{"/help", "/breeze <prompt>", "/review [instructions]", "/custom <prompt>"} {
		if !strings.Contains(got.Items[index].Usage, want) {
			t.Fatalf("HelpSnapshot().Items[%d] = %#v, want usage %q", index, got.Items[index], want)
		}
	}
}

func TestRootArgCandidatesReturnsCopies(t *testing.T) {
	first := RootArgCandidates("plugin")
	if len(first) == 0 {
		t.Fatal("RootArgCandidates(plugin) returned no candidates")
	}
	first[0].Value = "mutated"
	second := RootArgCandidates("plugin")
	if second[0].Value == "mutated" {
		t.Fatalf("RootArgCandidates(model) leaked mutable backing slice: %#v", second)
	}
}

func TestRemovedAgentManagementCommandIsUnknown(t *testing.T) {
	for _, removed := range []string{"agent"} {
		if IsKnownForPlatform(removed, "linux") || IsSharedKnownForPlatform(removed, "linux") {
			t.Fatalf("removed command %q is still registered", removed)
		}
		if got := RootArgCandidatesForPlatform(removed, "linux"); len(got) != 0 {
			t.Fatalf("RootArgCandidatesForPlatform(%q) = %#v, want none", removed, got)
		}
	}
	if !IsKnownForPlatform("subagent", "linux") || IsSharedKnownForPlatform("subagent", "linux") || IsACPKnownForPlatform("subagent", "linux") {
		t.Fatal("subagent must be registered as a TUI-private command only")
	}
}

func TestDoctorRootCandidatesExcludeRemovedFixAction(t *testing.T) {
	if got := RootArgCandidatesForPlatform("doctor", "windows"); len(got) != 0 {
		t.Fatalf("RootArgCandidates(doctor) = %#v, want none", got)
	}
}

func sliceContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
