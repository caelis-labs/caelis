package acpagentbridge

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestGatewayCommandNamesFromSessionRuntimeHandles(t *testing.T) {
	commands := gatewayCommandNamesFromHandles([]string{"orbit", "research", "ORBIT", "bad name"})
	for _, want := range []string{"status", "review", "orbit", "research"} {
		if !containsGatewayCommand(commands, want) {
			t.Fatalf("gatewayCommandNamesFromHandles() = %#v, want %q", commands, want)
		}
	}
	for _, hidden := range []string{"breeze", "zenith", "bad name"} {
		if containsGatewayCommand(commands, hidden) {
			t.Fatalf("gatewayCommandNamesFromHandles() = %#v, should hide %q", commands, hidden)
		}
	}
	if countGatewayCommand(commands, "orbit") != 1 {
		t.Fatalf("gatewayCommandNamesFromHandles() = %#v, want one orbit", commands)
	}
}

func TestGatewayCommandNamesForACPControllerHidesCompactFromHelp(t *testing.T) {
	t.Parallel()

	commands := gatewayCommandNamesForSession(nil, session.Session{Controller: session.ControllerBinding{Kind: session.ControllerKindACP}})
	if containsGatewayCommand(commands, "compact") {
		t.Fatalf("commands = %#v, should hide /compact from ACP /help", commands)
	}
}

func containsGatewayCommand(commands []string, name string) bool {
	return countGatewayCommand(commands, name) > 0
}

func countGatewayCommand(commands []string, name string) int {
	count := 0
	for _, command := range commands {
		if strings.EqualFold(strings.TrimSpace(command), name) {
			count++
		}
	}
	return count
}
