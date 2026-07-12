package identity

import (
	"slices"
	"testing"
)

func TestLookupResolvesCanonicalHistoricalAndDisplayOnlyIdentities(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"READ": Read, "SEARCH": Grep, "Grep": Grep,
		"RUN_COMMAND": RunCommand, "rUn_CoMmAnD": RunCommand, "RunCommand": RunCommand,
		"web_search": WebSearch, "WebSearch": WebSearch,
		"tool_search": ToolSearch, "ToolSearch": ToolSearch,
		"LIST": List,
	}
	for input, want := range tests {
		info, ok := Lookup(input)
		if !ok || info.Name != want {
			t.Fatalf("Lookup(%q) = %#v, %v, want %q", input, info, ok, want)
		}
	}
	if _, ok := LookupExecutable("LIST"); ok {
		t.Fatal("LookupExecutable(LIST) = true, want historical display identity excluded")
	}
	if got := ExecutableOrSelf("mcp__server__search"); got != "mcp__server__search" {
		t.Fatalf("ExecutableOrSelf(external) = %q", got)
	}
}

func TestRegistryOwnsStructuredBehavior(t *testing.T) {
	t.Parallel()

	run, _ := Lookup(RunCommand)
	if run.Kind != KindExecute || !run.TerminalKnown || !run.TerminalPanel || run.ResultStyle != ResultCommand {
		t.Fatalf("RunCommand info = %#v", run)
	}
	list, _ := Lookup(List)
	if !list.HistoricalOnly || list.ExplorationVerb != "List" || list.ResultStyle != ResultList {
		t.Fatalf("List info = %#v", list)
	}
}

func TestRegistryOwnsCompleteCanonicalNameSet(t *testing.T) {
	t.Parallel()

	got := make([]string, 0, len(entries))
	for _, item := range entries {
		if !item.HistoricalOnly {
			got = append(got, item.Name)
		}
	}
	want := []string{
		Read, Write, Patch, Glob, Grep, RunCommand, Task, Plan, Skill,
		WebSearch, WebFetch, Spawn, ToolSearch,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("executable registry names = %#v, want %#v", got, want)
	}
}
