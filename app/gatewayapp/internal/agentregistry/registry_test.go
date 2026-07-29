package agentregistry

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

func TestSelfRuntimeInvocationCarriesOnlyModelProfile(t *testing.T) {
	t.Parallel()

	args, env := SelfRuntimeInvocation(RuntimeConfig{
		ModelProfileID:     "provider:deepseek@default/deepseek/deepseek-v4-pro",
		ModelProfileEffort: "high",
		SystemPrompt:       "child prompt",
		ContextWindow:      1048576,
	})
	if env != nil {
		t.Fatalf("SelfRuntimeInvocation() env = %#v, want no model credentials", env)
	}
	for _, want := range []string{
		"-model-profile", "provider:deepseek@default/deepseek/deepseek-v4-pro",
		"-reasoning-effort", "high",
	} {
		if !slices.Contains(args, want) {
			t.Fatalf("SelfRuntimeInvocation() args = %#v, missing %q", args, want)
		}
	}
	joined := strings.Join(args, " ")
	for _, forbidden := range []string{
		"-provider", "-model ", "-api ", "-base-url", "-token", "-token-env",
		"-auth-type", "-header-key", "-default-reasoning-effort", "-reasoning-levels",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("SelfRuntimeInvocation() args = %#v, contain legacy model override %q", args, forbidden)
		}
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
	if got, want := len(seen), 24; got != want {
		t.Fatalf("ConnectableAgents() count = %d, want %d unique launchable entries", got, want)
	}
	for _, name := range []string{
		"codex", "claude", "copilot", "gemini", "grok", "opencode", "codefree-o",
		"auggie", "cline", "factory-droid", "kilo", "qwen-code", "custom",
	} {
		if _, ok := seen[name]; !ok {
			t.Fatalf("ConnectableAgents() missing %q", name)
		}
	}
	if seen["codex"].RegistryID != "codex-acp" || seen["opencode"].RegistryID != "opencode" {
		t.Fatalf("ConnectableAgents() registry identities = codex %#v, opencode %#v", seen["codex"], seen["opencode"])
	}
	if seen["codefree-o"].RegistryID != "" {
		t.Fatalf("ConnectableAgents() codefree-o = %#v, want maintained local entry", seen["codefree-o"])
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
