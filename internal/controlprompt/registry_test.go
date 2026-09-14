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
		"team",
		"plugin",
		"model",
		"status",
		"new",
		"resume",
		"compact",
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
	// Every registered product command and alias is reserved from custom
	// delegation handles, so a role cannot hijack command or alias resolution.
	// `team` is the deliberate exception: it stays a legal custom handle because
	// the TUI resolves the built-in /team overlay before considering agent runs,
	// so a colliding role cannot shadow it there.
	for _, goos := range []string{"linux", "windows"} {
		for _, spec := range DefaultSpecsForPlatform(goos) {
			if spec.Name != "team" {
				if err := agentbinding.ValidateCustomHandle(agentbinding.Handle(spec.Name)); err == nil {
					t.Errorf("ValidateCustomHandle(%q) succeeded for %s product command", spec.Name, goos)
				}
			}
			for _, alias := range spec.Aliases {
				if err := agentbinding.ValidateCustomHandle(agentbinding.Handle(alias)); err == nil {
					t.Errorf("ValidateCustomHandle(%q) succeeded for %s product command alias", alias, goos)
				}
			}
		}
	}
	if err := agentbinding.ValidateCustomHandle("team"); err != nil {
		t.Errorf("ValidateCustomHandle(team) = %v, want legal custom handle", err)
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
	for _, hidden := range []string{"connect", "disconnect", "team", "subagent", "plugin", "quit", "exit"} {
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
	for _, hidden := range []string{"help", "agent", "team", "subagent", "model", "new", "resume", "connect", "disconnect", "plugin", "quit", "exit"} {
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

func TestHelpSnapshotResolvesAliasesToSingleCanonicalEntry(t *testing.T) {
	got := HelpSnapshot([]string{"quit", "exit", "team", "subagent"})
	if len(got.Items) != 2 {
		t.Fatalf("HelpSnapshot() items = %#v, want one entry per canonical command", got.Items)
	}
	for index, want := range []string{"/quit", "/team"} {
		if got.Items[index].Name != strings.TrimPrefix(want, "/") || !strings.HasPrefix(got.Items[index].Usage, want) {
			t.Fatalf("HelpSnapshot().Items[%d] = %#v, want canonical %s", index, got.Items[index], want)
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

func TestAvailableWhileRunningMarksResumeAndQuitCommandsOnly(t *testing.T) {
	for _, name := range []string{"resume", "quit", "exit"} {
		spec, ok := Lookup(name)
		if !ok || !spec.AvailableWhileRunning {
			t.Fatalf("Lookup(%q).AvailableWhileRunning = %v ok=%v, want true", name, spec.AvailableWhileRunning, ok)
		}
	}
	for _, name := range []string{"help", "status", "model", "new", "compact", "connect", "team"} {
		spec, ok := Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) missing", name)
		}
		if spec.AvailableWhileRunning {
			t.Fatalf("Lookup(%q).AvailableWhileRunning = true, want unsafe command hidden while running", name)
		}
	}
}

func TestCommandAliasesResolveToCanonicalSpec(t *testing.T) {
	cases := []struct {
		alias     string
		canonical string
	}{
		{alias: "subagent", canonical: "team"},
		{alias: "exit", canonical: "quit"},
	}
	for _, tc := range cases {
		aliasSpec, ok := Lookup(tc.alias)
		if !ok {
			t.Fatalf("Lookup(%q) missing for alias", tc.alias)
		}
		canonicalSpec, ok := Lookup(tc.canonical)
		if !ok {
			t.Fatalf("Lookup(%q) missing for canonical name", tc.canonical)
		}
		if aliasSpec.Name != tc.canonical || canonicalSpec.Name != tc.canonical {
			t.Fatalf("alias %q resolved to %q, canonical %q resolved to %q", tc.alias, aliasSpec.Name, tc.canonical, canonicalSpec.Name)
		}
		if aliasSpec.Usage != canonicalSpec.Usage {
			t.Fatalf("alias %q usage = %q, want canonical %q", tc.alias, aliasSpec.Usage, canonicalSpec.Usage)
		}
	}
}

func TestAliasesAreNotAdvertisedAsSeparateCommands(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		for _, name := range DefaultNamesForPlatform(goos) {
			if name == "subagent" || name == "exit" {
				t.Fatalf("DefaultNamesForPlatform(%s) advertises alias %q", goos, name)
			}
		}
		for _, spec := range DefaultSpecsForPlatform(goos) {
			for _, alias := range spec.Aliases {
				if alias == spec.Name {
					t.Fatalf("spec %q aliases itself", spec.Name)
				}
				resolved, ok := LookupForPlatform(alias, goos)
				if !ok || resolved.Name != spec.Name {
					t.Fatalf("alias %q of %q resolved to %#v ok=%v", alias, spec.Name, resolved, ok)
				}
			}
		}
	}
}

func TestTeamSpecIsArgumentFreeOverlayEntry(t *testing.T) {
	spec, ok := Lookup("team")
	if !ok {
		t.Fatal("Lookup(team) missing")
	}
	if spec.Name != "team" || spec.Usage != "/team" {
		t.Fatalf("team spec identity = %#v, want canonical /team usage", spec)
	}
	if spec.Description != "Configure participant profiles and system Agents" {
		t.Fatalf("team description = %q", spec.Description)
	}
	if spec.DynamicCompleter || len(spec.ArgCandidates) > 0 || len(spec.Details) > 0 {
		t.Fatalf("team spec = %#v, want no argument completion surface", spec)
	}
	if got := spec.Aliases; len(got) != 1 || got[0] != "subagent" {
		t.Fatalf("team aliases = %#v, want [subagent]", got)
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
