package tuiapp

import (
	"context"
	"strings"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

func TestACPAuthSelectionUsesDeclaredMethodIDs(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{})
	model.slashArgLoadPending = true
	model.slashArgLoadSeq = 7
	responses := make(chan PromptResponse, 1)
	model.handleACPAuthSelectionRequest(acpAuthSelectionRequestMsg{
		seq: 7,
		request: controlagents.AuthenticationSelectionRequest{
			AgentID: "codex",
			Methods: []controlagents.AuthenticationMethod{
				{ID: "browser", Name: "Browser login", Type: controlagents.AuthenticationAgent},
				{ID: "terminal", Name: "Terminal login", Type: controlagents.AuthenticationTerminal},
			},
		},
		response: responses,
	})
	if model.activePrompt == nil {
		t.Fatal("active auth selection prompt = nil")
	}
	if got := model.activePrompt.prompt; got != "Choose an authentication method" {
		t.Fatalf("prompt = %q", got)
	}
	if len(model.activePrompt.choices) != 2 ||
		model.activePrompt.choices[0].value != "browser" ||
		model.activePrompt.choices[1].value != "terminal" {
		t.Fatalf("choices = %#v, want exact declared method IDs", model.activePrompt.choices)
	}
}

func TestTerminalAuthenticationCommandUsesAuthorizedInvocation(t *testing.T) {
	t.Setenv("ACP_AUTH_BASE", "base")
	request := controlagents.TerminalAuthenticationRequest{
		Command: "agent-acp",
		Args:    []string{"acp", "--login"},
		Env: map[string]string{
			"ACP_AUTH_BASE":       "overridden",
			"ACP_AUTH_ADDITIONAL": "yes",
		},
		WorkDir: "/workspace",
	}
	command := terminalAuthenticationCommand(context.Background(), request)
	if strings.Join(command.Args, "\x00") != "agent-acp\x00acp\x00--login" {
		t.Fatalf("command args = %#v", command.Args)
	}
	if command.Dir != "/workspace" {
		t.Fatalf("command dir = %q", command.Dir)
	}
	env := strings.Join(command.Env, "\n")
	for _, want := range []string{"ACP_AUTH_BASE=overridden", "ACP_AUTH_ADDITIONAL=yes"} {
		if !strings.Contains(env, want) {
			t.Fatalf("command env missing %q: %q", want, env)
		}
	}
}

func TestMergedAuthenticationEnvironmentIsDeterministic(t *testing.T) {
	t.Parallel()

	got := mergedAuthenticationEnvironment(
		[]string{"Z=base", "A=base", "M=base"},
		map[string]string{"Z": "override", "B": "added"},
	)
	want := []string{"A=base", "B=added", "M=base", "Z=override"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("merged environment = %#v, want %#v", got, want)
	}
}
