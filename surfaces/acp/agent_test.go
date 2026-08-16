package acp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/agentbinding"
)

func TestACPPromptCommandNamesExposeOnlyBoundDirectHandles(t *testing.T) {
	status := agentbinding.Status{Handles: []agentbinding.HandleStatus{
		{
			Definition: agentbinding.Definition{
				Handle:       agentbinding.HandleOrbit,
				Class:        agentbinding.HandleClassDelegation,
				Configurable: true,
			},
			Binding: agentbinding.Binding{
				Handle: agentbinding.HandleOrbit, ProfileID: "provider:bound-orbit", Effort: "high",
			},
		},
		{
			Definition: agentbinding.Definition{
				Handle: "research", Class: agentbinding.HandleClassDelegation,
				Configurable: true, Custom: true,
			},
			Binding: agentbinding.Binding{
				Handle: "research", ProfileID: "provider:bound-research", Effort: "high",
			},
		},
	}}

	commands := acpPromptCommandNames(status)
	for _, want := range []string{"status", "review", "orbit", "research"} {
		if !containsCommand(commands, want) {
			t.Fatalf("acpPromptCommandNames() = %#v, want %q", commands, want)
		}
	}
	for _, hidden := range []string{"breeze", "zenith", "helper", "reviewer", "self", "lead"} {
		if containsCommand(commands, hidden) {
			t.Fatalf("acpPromptCommandNames() = %#v, should hide %q", commands, hidden)
		}
	}
	if countCommand(commands, "status") != 1 {
		t.Fatalf("acpPromptCommandNames() = %#v, want one core status command", commands)
	}
}

func TestACPPromptCommandNamesFromSessionRuntimeHandles(t *testing.T) {
	commands := acpPromptCommandNamesFromHandles([]string{"orbit", "research", "ORBIT", "bad name"})
	for _, want := range []string{"status", "review", "orbit", "research"} {
		if !containsCommand(commands, want) {
			t.Fatalf("acpPromptCommandNamesFromHandles() = %#v, want %q", commands, want)
		}
	}
	for _, hidden := range []string{"breeze", "zenith", "bad name"} {
		if containsCommand(commands, hidden) {
			t.Fatalf("acpPromptCommandNamesFromHandles() = %#v, should hide %q", commands, hidden)
		}
	}
	if countCommand(commands, "orbit") != 1 {
		t.Fatalf("acpPromptCommandNamesFromHandles() = %#v, want one orbit", commands)
	}
}

func containsCommand(commands []string, name string) bool {
	return countCommand(commands, name) > 0
}

func countCommand(commands []string, name string) int {
	count := 0
	for _, command := range commands {
		if strings.EqualFold(strings.TrimSpace(command), name) {
			count++
		}
	}
	return count
}
