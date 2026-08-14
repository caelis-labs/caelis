package acpagentenv

import (
	"strings"
	"testing"
)

func TestSelfAgentFromEnvPrefersStructuredCommand(t *testing.T) {
	values := map[string]string{
		EnvName:        "worker",
		EnvDescription: "custom worker",
		EnvCommand:     "/bin/worker",
		EnvArgsJSON:    `["--one","two words"]`,
		EnvLegacyCmd:   "echo ignored",
		EnvWorkDir:     "/tmp/work",
	}
	agent, err := SelfAgentFromEnv(func(key string) string { return values[key] }, "default desc")
	if err != nil {
		t.Fatalf("SelfAgentFromEnv() error = %v", err)
	}
	if agent == nil {
		t.Fatal("SelfAgentFromEnv() agent = nil")
	}
	if agent.Name != "worker" || agent.Description != "custom worker" {
		t.Fatalf("agent identity = %q/%q", agent.Name, agent.Description)
	}
	if agent.Command != "/bin/worker" || strings.Join(agent.Args, "\x00") != "--one\x00two words" {
		t.Fatalf("agent command = %q %#v", agent.Command, agent.Args)
	}
	if agent.WorkDir != "/tmp/work" {
		t.Fatalf("agent workdir = %q", agent.WorkDir)
	}
}

func TestSelfAgentFromEnvUsesLegacyShellCommand(t *testing.T) {
	values := map[string]string{
		EnvLegacyCmd: "echo child",
	}
	agent, err := SelfAgentFromEnv(func(key string) string { return values[key] }, "default desc")
	if err != nil {
		t.Fatalf("SelfAgentFromEnv() error = %v", err)
	}
	if agent == nil {
		t.Fatal("SelfAgentFromEnv() agent = nil")
	}
	wantCommand, wantArgs := shellCommandSpec("echo child")
	if agent.Name != "self" || agent.Description != "default desc" {
		t.Fatalf("agent identity = %q/%q", agent.Name, agent.Description)
	}
	if agent.Command != wantCommand || strings.Join(agent.Args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("agent command = %q %#v, want %q %#v", agent.Command, agent.Args, wantCommand, wantArgs)
	}
}

func TestSelfAgentFromEnvRejectsArgsJSONWithoutCommand(t *testing.T) {
	values := map[string]string{
		EnvArgsJSON: `["--stdio"]`,
	}
	agent, err := SelfAgentFromEnv(func(key string) string { return values[key] }, "")
	if err == nil || !strings.Contains(err.Error(), EnvCommand) {
		t.Fatalf("SelfAgentFromEnv() error = %v, want missing command error", err)
	}
	if agent != nil {
		t.Fatalf("SelfAgentFromEnv() agent = %#v, want nil", agent)
	}
}

func TestSelfAgentFromEnvRejectsInvalidArgsJSON(t *testing.T) {
	values := map[string]string{
		EnvCommand:  "/bin/worker",
		EnvArgsJSON: `{"bad":true}`,
	}
	agent, err := SelfAgentFromEnv(func(key string) string { return values[key] }, "")
	if err == nil || !strings.Contains(err.Error(), EnvArgsJSON) {
		t.Fatalf("SelfAgentFromEnv() error = %v, want args JSON error", err)
	}
	if agent != nil {
		t.Fatalf("SelfAgentFromEnv() agent = %#v, want nil", agent)
	}
}

func TestSelfAgentFromEnvNotConfigured(t *testing.T) {
	agent, err := SelfAgentFromEnv(func(string) string { return "" }, "default desc")
	if err != nil {
		t.Fatalf("SelfAgentFromEnv() error = %v", err)
	}
	if agent != nil {
		t.Fatalf("SelfAgentFromEnv() agent = %#v, want nil", agent)
	}
}

func TestShellCommandSpecForGOOS(t *testing.T) {
	command, args := shellCommandSpecForGOOS("windows", "echo hi")
	if command != "cmd" || strings.Join(args, "\x00") != "/C\x00echo hi" {
		t.Fatalf("windows shell spec = %q %#v", command, args)
	}
	command, args = shellCommandSpecForGOOS("linux", "echo hi")
	if command != "bash" || strings.Join(args, "\x00") != "-lc\x00echo hi" {
		t.Fatalf("unix shell spec = %q %#v", command, args)
	}
}
