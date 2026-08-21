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

func TestManagedACPAdaptersMatchRegistrySnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pkg     string
		version string
	}{
		{name: "codex", pkg: "@agentclientprotocol/codex-acp", version: "1.1.7"},
		{name: "claude", pkg: "@agentclientprotocol/claude-agent-acp", version: "0.63.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := BuiltinAdapterPackageFor(test.name)
			if !ok {
				t.Fatalf("BuiltinAdapterPackageFor(%q) not found", test.name)
			}
			if got.Package != test.pkg || got.Version != test.version {
				t.Fatalf("BuiltinAdapterPackageFor(%q) = %#v, want %s@%s", test.name, got, test.pkg, test.version)
			}
		})
	}
}

func TestConnectableAgentsIncludesRegistryNPXAndInstalledCatalog(t *testing.T) {
	t.Parallel()

	if got, want := len(registeredNPXAgents), 21; got != want {
		t.Fatalf("registeredNPXAgents count = %d, want Registry snapshot count %d", got, want)
	}
	seen := map[string]ConnectableAgent{}
	for _, agent := range ConnectableAgents() {
		if _, duplicate := seen[agent.ID]; duplicate {
			t.Fatalf("ConnectableAgents() has duplicate id %q", agent.ID)
		}
		if !controlagents.IsName(agent.ID) || ReservedSlashCommandName(agent.ID) {
			t.Fatalf("ConnectableAgents() id %q is not available to the Agent roster", agent.ID)
		}
		seen[agent.ID] = agent
	}
	if got, want := len(seen), 23; got != want {
		t.Fatalf("ConnectableAgents() count = %d, want %d unique launchable entries", got, want)
	}
	for _, name := range []string{
		"codex", "claude", "copilot", "gemini", "grok", "opencode",
		"auggie", "cline", "factory-droid", "kilo", "qwen-code", "custom",
	} {
		if _, ok := seen[name]; !ok {
			t.Fatalf("ConnectableAgents() missing %q", name)
		}
	}
	if seen["codex"].RegistryID != "codex-acp" || seen["opencode"].RegistryID != "opencode" {
		t.Fatalf("ConnectableAgents() registry identities = codex %#v, opencode %#v", seen["codex"], seen["opencode"])
	}
	agents := ConnectableAgents()
	if len(agents) < 3 || agents[0].ID != "codex" || agents[1].ID != "claude" || agents[2].ID != "custom" {
		t.Fatalf("ConnectableAgents() priority order = %#v, want declared codex, claude, custom", agents[:min(3, len(agents))])
	}
	if !reflect.DeepEqual(seen["codex"].Launchers, []controlagents.LauncherChoice{
		controlagents.LauncherChoiceManaged,
		controlagents.LauncherChoiceNPX,
		controlagents.LauncherChoiceGlobal,
	}) {
		t.Fatalf("codex launchers = %#v, want data-declared managed, npx, global", seen["codex"].Launchers)
	}
}

func TestRegistryNPXLauncherPreservesPinnedArgumentsAndEnvironment(t *testing.T) {
	t.Parallel()

	factory, ok := LookupNPXAgent("factory-droid")
	if !ok {
		t.Fatal("LookupNPXAgent(factory-droid) not found")
	}
	if got, want := factory.Args, []string{"-y", "droid@0.181.0", "exec", "--output-format", "acp-daemon"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("factory-droid args = %#v, want %#v", got, want)
	}
	wantEnv := map[string]string{
		"DROID_DISABLE_AUTO_UPDATE":         "true",
		"FACTORY_DROID_AUTO_UPDATE_ENABLED": "false",
	}
	if !reflect.DeepEqual(factory.Env, wantEnv) {
		t.Fatalf("factory-droid env = %#v, want %#v", factory.Env, wantEnv)
	}
	factory.Args[0] = "changed"
	factory.Env["DROID_DISABLE_AUTO_UPDATE"] = "changed"
	again, ok := LookupNPXAgent("factory-droid")
	if !ok || again.Args[0] != "-y" || again.Env["DROID_DISABLE_AUTO_UPDATE"] != "true" {
		t.Fatalf("LookupNPXAgent() returned aliased launcher: %#v", again)
	}
}

func TestRegistryIdentityDoesNotBecomeSecondConnectionID(t *testing.T) {
	t.Parallel()

	for _, upstream := range []string{"codex-acp", "claude-acp", "github-copilot-cli", "grok-build"} {
		if got, ok := LookupConnectableAgent(upstream); ok {
			t.Fatalf("LookupConnectableAgent(%q) = %#v, want Registry ID kept as provenance only", upstream, got)
		}
	}
}

func TestCatalogLauncherDeclarationsHaveResolvablePresets(t *testing.T) {
	t.Parallel()

	for _, agent := range ConnectableAgents() {
		if !SupportsLauncher(agent.ID, agent.Preferred) {
			t.Errorf("%s preferred launcher %q is not declared in %#v", agent.ID, agent.Preferred, agent.Launchers)
		}
		for _, launcher := range agent.Launchers {
			switch launcher {
			case controlagents.LauncherChoiceCommand:
				if agent.ID != customConnectableAgent.ID {
					t.Errorf("%s declares command launcher outside the custom entry", agent.ID)
				}
			case controlagents.LauncherChoiceNPX:
				if _, ok := LookupNPXAgent(agent.ID); !ok {
					t.Errorf("%s declares npx launcher without an npx preset", agent.ID)
				}
			case controlagents.LauncherChoiceManaged, controlagents.LauncherChoiceGlobal:
				if _, ok := LookupNPXAgent(agent.ID); !ok {
					t.Errorf("%s declares %s launcher without an npx install preset", agent.ID, launcher)
				}
				if _, ok := BuiltinAdapterPackageFor(agent.ID); !ok {
					t.Errorf("%s declares %s launcher without a managed package", agent.ID, launcher)
				}
			case controlagents.LauncherChoiceInstalled:
				if _, ok := LookupInstalledAgent(agent.ID); !ok {
					t.Errorf("%s declares installed launcher without an installed-command preset", agent.ID)
				}
			default:
				t.Errorf("%s declares unsupported launcher %q", agent.ID, launcher)
			}
		}
	}
}
