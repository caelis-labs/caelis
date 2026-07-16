package controlcommand

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/control"
)

func TestDefaultNamesExposePlatformCoreCommandsOnly(t *testing.T) {
	got := DefaultNamesForPlatform("linux")
	want := []string{
		"help",
		"review",
		"lead",
		"connect",
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

func TestDefaultSharedNamesExcludeTUIPrivateCommands(t *testing.T) {
	got := DefaultSharedNamesForPlatform("linux")
	for _, want := range []string{"help", "review", "lead", "model", "status", "new", "resume", "compact"} {
		if !sliceContainsString(got, want) {
			t.Fatalf("DefaultSharedNamesForPlatform(linux) = %#v, want %q", got, want)
		}
	}
	for _, hidden := range []string{"connect", "subagent", "plugin", "exit", "quit"} {
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
	want := []string{"status", "lead", "compact", "review"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultACPNamesForPlatform(linux) = %#v, want %#v", got, want)
	}
	for _, hidden := range []string{"help", "agent", "subagent", "model", "new", "resume", "connect", "plugin", "exit", "quit"} {
		if sliceContainsString(got, hidden) {
			t.Fatalf("DefaultACPNamesForPlatform(linux) = %#v, should exclude %q", got, hidden)
		}
		if IsACPKnownForPlatform(hidden, "linux") {
			t.Fatalf("IsACPKnownForPlatform(%q, linux) = true, want false", hidden)
		}
	}
}

func TestHelpTextUsesRegistrySpecs(t *testing.T) {
	got := HelpText([]string{"help", "lead", "review", "custom"})
	for _, want := range []string{"/help", "Show commands and shortcuts", "/lead <agent|local>", "/review [instructions]", "/custom <prompt>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("HelpText() = %q, want %q", got, want)
		}
	}
}

func TestAppendRegisteredAgentNamesDedupesAndFilters(t *testing.T) {
	lister := staticAgentLister{
		{Name: "Helper"},
		{Name: "status"},
		{Name: "helper"},
		{Name: "  "},
	}
	got := AppendRegisteredAgentNames(context.Background(), lister, []string{"status"}, func(name string) bool {
		return name != "status"
	})
	want := []string{"status", "helper"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AppendRegisteredAgentNames() = %#v, want %#v", got, want)
	}
	if !RegisteredAgentNameAllowed(context.Background(), lister, "helper") {
		t.Fatal("RegisteredAgentNameAllowed(helper) = false, want true")
	}
	if AgentNameAllowed([]string{"helper"}, "status", func(name string) bool { return name != "status" }) {
		t.Fatal("AgentNameAllowed(status) = true despite filter")
	}
}

func TestLocalDuringACPMatchesAdvertisedLocalCommands(t *testing.T) {
	local := []string{"help", "review", "lead", "subagent", "plugin", "status", "resume", "model", "exit", "quit"}
	for _, name := range local {
		if !IsLocalDuringACP(name) {
			t.Fatalf("IsLocalDuringACP(%q) = false, want true", name)
		}
	}
	if !IsLocalDuringACPForPlatform("doctor", "windows") {
		t.Fatal("IsLocalDuringACPForPlatform(doctor, windows) = false, want true")
	}
	if IsLocalDuringACPForPlatform("doctor", "linux") {
		t.Fatal("IsLocalDuringACPForPlatform(doctor, linux) = true, want false")
	}
	remote := []string{"connect", "new", "compact", "sandbox"}
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

type staticAgentLister []control.AgentCandidate

func (l staticAgentLister) ListAgents(context.Context, int) ([]control.AgentCandidate, error) {
	return append([]control.AgentCandidate(nil), l...), nil
}

func sliceContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
