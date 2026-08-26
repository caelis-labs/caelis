package agentregistry

import (
	"reflect"
	"strings"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentenv"
)

func TestConfiguredSelfAgentAttachesManagedHostAndCarriesSessionOptions(t *testing.T) {
	t.Parallel()

	agent, err := configuredSelfAgent(DefaultSelfConfig{
		StoreDir:         "/store",
		WorkspaceKey:     "workspace",
		WorkspaceCWD:     "/workspace",
		ControlURL:       "http://127.0.0.1:1234",
		ControlTokenFile: "/run/caelis/child-token",
		SessionOptions: controlagents.SessionOptions{
			ModelID:                 " model-config ",
			ConfigValues:            map[string]string{"mode": "manual", "reasoning_effort": "high"},
			ReasoningEffortConfigID: " reasoning_effort ",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := agent.Args, []string{
		"acp", "-store-dir", "/store",
		"-control-url", "http://127.0.0.1:1234", "-control-token-file", "/run/caelis/child-token",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("configuredSelfAgent() args = %#v, want managed attach args %#v", got, want)
	}
	joined := strings.Join(agent.Args, " ")
	for _, forbidden := range []string{
		"--embedded", "-approval-mode", "-policy-profile", "-model-profile",
		"-reasoning-effort", "-system-prompt", "--dangerously-skip-permissions",
		"-control-operation-retention", "-context-window", "-sandbox-backend",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("configuredSelfAgent() args = %#v, contain Host-construction option %q", agent.Args, forbidden)
		}
	}
	if got, want := agent.Env, map[string]string{
		"CAELIS_CONTROL_TOKEN":                    "",
		"CAELIS_CONTROL_TOKEN_FILE":               "",
		acpagentenv.EnvManagedSessionHistoryToken: "",
		acpagentenv.EnvWorkspaceKey:               "workspace",
		acpagentenv.EnvWorkspaceCWD:               "/workspace",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("configuredSelfAgent() env = %#v, want inherited raw credentials scrubbed %#v", got, want)
	}
	if agent.WorkDir != "/workspace" {
		t.Fatalf("configuredSelfAgent() workdir = %q, want /workspace", agent.WorkDir)
	}
	if agent.SessionOptions.ModelID != "model-config" ||
		agent.SessionOptions.ConfigValues["mode"] != "manual" ||
		agent.SessionOptions.ConfigValues["reasoning_effort"] != "high" ||
		agent.SessionOptions.ReasoningEffortConfigID != "reasoning_effort" {
		t.Fatalf("configuredSelfAgent() SessionOptions = %#v, want normalized post-session/new values", agent.SessionOptions)
	}
}

func TestConfiguredSelfAgentWithoutChildBridgeScrubsParentCredentials(t *testing.T) {
	t.Parallel()

	agent, err := configuredSelfAgent(DefaultSelfConfig{
		StoreDir:     "/store",
		WorkspaceKey: "workspace",
		WorkspaceCWD: "/workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := agent.Env["CAELIS_CONTROL_TOKEN"]; got != "" {
		t.Fatalf("CAELIS_CONTROL_TOKEN = %q, want scrubbed", got)
	}
	if got := agent.Env["CAELIS_CONTROL_TOKEN_FILE"]; got != "" {
		t.Fatalf("CAELIS_CONTROL_TOKEN_FILE = %q, want scrubbed", got)
	}
	if got := agent.Env[acpagentenv.EnvManagedSessionHistoryToken]; got != "" {
		t.Fatalf("%s = %q, want scrubbed", acpagentenv.EnvManagedSessionHistoryToken, got)
	}
	if strings.Contains(strings.Join(agent.Args, " "), "control-token") {
		t.Fatalf("configuredSelfAgent() args = %#v, contain unavailable child credential", agent.Args)
	}
}

func TestConnectableAgentsContainsBuiltInNativeACPCommandsAndCustom(t *testing.T) {
	t.Parallel()

	agents := ConnectableAgents()
	if got, want := len(agents), 14; got != want {
		t.Fatalf("ConnectableAgents() count = %d, want %d", got, want)
	}
	gotIDs := make([]string, 0, len(agents))
	for _, agent := range agents {
		gotIDs = append(gotIDs, agent.ID)
	}
	wantIDs := []string{
		"codex", "grok", "kimi", "opencode", "copilot", "qoder", "gemini", "qwen-code",
		"auggie", "cline", "factory-droid", "goose", "kilo", "custom",
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("ConnectableAgents() order = %#v, want %#v", gotIDs, wantIDs)
	}
	for _, agent := range agents {
		if !controlagents.IsName(agent.ID) || ReservedSlashCommandName(agent.ID) {
			t.Fatalf("ConnectableAgents() id %q is not available to the Agent roster", agent.ID)
		}
		if !SupportsLauncher(agent.ID, agent.Preferred) {
			t.Fatalf("%s preferred launcher %q is not declared in %#v", agent.ID, agent.Preferred, agent.Launchers)
		}
		if len(agent.Launchers) != 1 {
			t.Fatalf("%s launchers = %#v, want one user-owned launcher", agent.ID, agent.Launchers)
		}
	}
	for _, removed := range []string{"claude", "deepagents", "glm-acp-agent", "pi-acp"} {
		if got, ok := LookupConnectableAgent(removed); ok {
			t.Fatalf("LookupConnectableAgent(%q) = %#v, want removed from guided catalog", removed, got)
		}
	}
}

func TestNativeACPLaunchersPreserveCommandsAndDetachedArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		args    []string
		env     map[string]string
	}{
		{name: "grok", command: "grok", args: []string{"agent", "stdio"}},
		{name: "kimi", command: "kimi", args: []string{"acp"}},
		{name: "opencode", command: "opencode", args: []string{"acp"}},
		{name: "copilot", command: "copilot", args: []string{"--acp"}},
		{name: "qoder", command: "qoder", args: []string{"--acp"}},
		{name: "gemini", command: "gemini", args: []string{"--acp"}},
		{name: "qwen-code", command: "qwen", args: []string{"--acp"}},
		{
			name: "auggie", command: "auggie", args: []string{"--acp"},
			env: map[string]string{"AUGMENT_DISABLE_AUTO_UPDATE": "1"},
		},
		{name: "cline", command: "cline", args: []string{"--acp"}},
		{
			name: "factory-droid", command: "droid", args: []string{"exec", "--output-format", "acp-daemon"},
			env: map[string]string{
				"DROID_DISABLE_AUTO_UPDATE":         "true",
				"FACTORY_DROID_AUTO_UPDATE_ENABLED": "false",
			},
		},
		{name: "goose", command: "goose", args: []string{"acp"}},
		{name: "kilo", command: "kilo", args: []string{"acp"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preset, ok := LookupInstalledAgent(test.name)
			if !ok {
				t.Fatalf("LookupInstalledAgent(%q) not found", test.name)
			}
			if preset.Command != test.command || !reflect.DeepEqual(preset.Args, test.args) {
				t.Fatalf("LookupInstalledAgent(%q) = %#v, want %q %#v", test.name, preset, test.command, test.args)
			}
			if !reflect.DeepEqual(preset.Env, test.env) {
				t.Fatalf("LookupInstalledAgent(%q) env = %#v, want %#v", test.name, preset.Env, test.env)
			}
			preset.Args[0] = "changed"
			if preset.Env != nil {
				preset.Env["changed"] = "true"
			}
			again, _ := LookupInstalledAgent(test.name)
			if reflect.DeepEqual(again.Args, preset.Args) {
				t.Fatalf("LookupInstalledAgent(%q) returned aliased args", test.name)
			}
			if _, aliased := again.Env["changed"]; aliased {
				t.Fatalf("LookupInstalledAgent(%q) returned aliased env", test.name)
			}
		})
	}
}

func TestNativeACPCommandCandidatesPreserveQoderInstallerNames(t *testing.T) {
	t.Parallel()

	commands := InstalledAgentCommandCandidates("qoder")
	if want := []string{"qoder", "qodercli"}; !reflect.DeepEqual(commands, want) {
		t.Fatalf("InstalledAgentCommandCandidates(qoder) = %#v, want %#v", commands, want)
	}
	commands[0] = "changed"
	if again := InstalledAgentCommandCandidates("qoder"); again[0] != "qoder" {
		t.Fatalf("InstalledAgentCommandCandidates(qoder) returned aliased values: %#v", again)
	}
}
