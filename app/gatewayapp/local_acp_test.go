package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	headlessadapter "github.com/OnslaughtSnail/caelis/gateway/adapter/headless"
	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
	spawntool "github.com/OnslaughtSnail/caelis/sdk/tool/builtin/spawn"
	tasktool "github.com/OnslaughtSnail/caelis/sdk/tool/builtin/task"
)

func TestLocalStackGatewayACPMainE2E(t *testing.T) {
	repo := repoRootForGatewayAppTest(t)
	root := t.TempDir()
	workdir := t.TempDir()

	stack, err := NewLocalStack(Config{
		AppName:        "caelis",
		UserID:         "user-1",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
		Assembly: sdkplugin.ResolvedAssembly{
			Agents: []sdkplugin.AgentConfig{{
				Name:        "codex",
				Description: "ACP main controller.",
				Command:     "go",
				Args:        []string{"run", "./acpbridge/cmd/e2eagent"},
				WorkDir:     repo,
				Env: map[string]string{
					"SDK_ACP_STUB_REPLY":         "gateway acp main ok",
					"SDK_ACP_ENABLE_MODE_CONFIG": "1",
					"SDK_ACP_SESSION_ROOT":       filepath.Join(root, "controller-sessions"),
					"SDK_ACP_TASK_ROOT":          filepath.Join(root, "controller-tasks"),
				},
			}},
		},
		Model: ModelConfig{
			Provider: "minimax",
			Model:    "MiniMax-M2",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}

	session, err := stack.StartSession(context.Background(), "gateway-acp-main", "surface-acp-main")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	updated, err := stack.Gateway.HandoffController(context.Background(), appgateway.HandoffControllerRequest{
		SessionRef: session.SessionRef,
		Kind:       sdksession.ControllerKindACP,
		Agent:      "codex",
		Source:     "test",
		Reason:     "delegate main control",
	})
	if err != nil {
		t.Fatalf("HandoffController() error = %v", err)
	}
	if updated.Controller.Kind != sdksession.ControllerKindACP {
		t.Fatalf("controller kind = %q, want %q", updated.Controller.Kind, sdksession.ControllerKindACP)
	}

	state, err := stack.Gateway.ControlPlaneState(context.Background(), appgateway.ControlPlaneStateRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("ControlPlaneState() error = %v", err)
	}
	if state.Controller.Kind != sdksession.ControllerKindACP || strings.TrimSpace(state.Controller.EpochID) == "" {
		t.Fatalf("control state = %+v", state)
	}
	controllerStatus, found, err := stack.ACPControllerStatus(context.Background(), session.SessionRef)
	if err != nil {
		t.Fatalf("ACPControllerStatus() error = %v", err)
	}
	if !found {
		t.Fatal("ACPControllerStatus() found = false")
	}
	if got := strings.TrimSpace(controllerStatus.Mode); got != "default" {
		t.Fatalf("ACPControllerStatus().Mode = %q, want default", got)
	}
	if got := len(controllerStatus.ModeOptions); got != 2 {
		t.Fatalf("len(ACPControllerStatus().ModeOptions) = %d, want 2", got)
	}
	updatedStatus, err := stack.SetACPControllerMode(context.Background(), session.SessionRef, "plan")
	if err != nil {
		t.Fatalf("SetACPControllerMode(plan) error = %v", err)
	}
	if got := strings.TrimSpace(updatedStatus.Mode); got != "plan" {
		t.Fatalf("SetACPControllerMode(plan).Mode = %q, want plan", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	result, err := headlessadapter.RunOnce(ctx, stack.Gateway, appgateway.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "run through acp controller",
		Surface:    "headless-acp-main-e2e",
	}, headlessadapter.Options{})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if got := strings.TrimSpace(result.Output); got != "gateway acp main ok" {
		t.Fatalf("RunOnce() output = %q, want %q", got, "gateway acp main ok")
	}

	loaded, err := stack.Sessions.LoadSession(ctx, sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var sawACPAssistant bool
	for _, event := range loaded.Events {
		if event == nil || sdksession.EventTypeOf(event) != sdksession.EventTypeAssistant || event.Scope == nil {
			continue
		}
		if event.Scope.Controller.Kind == sdksession.ControllerKindACP && strings.TrimSpace(event.Text) == "gateway acp main ok" {
			sawACPAssistant = true
			break
		}
	}
	if !sawACPAssistant {
		t.Fatalf("loaded events missing ACP-scoped assistant reply: %#v", loaded.Events)
	}
}

func TestLocalStackInjectsSpawnForSelfAndRegisteredACPAgents(t *testing.T) {
	ctx := context.Background()
	withAgents, session := newStackWithAssemblyForToolTest(t, sdkplugin.ResolvedAssembly{
		Agents: []sdkplugin.AgentConfig{{
			Name:        "helper",
			Description: "bounded ACP helper",
			Command:     "go",
			Args:        []string{"run", "./acpbridge/cmd/e2eagent"},
			WorkDir:     repoRootForGatewayAppTest(t),
		}},
	})
	resolved, err := withAgents.Gateway.Resolver().ResolveTurn(ctx, appgateway.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(with agents) error = %v", err)
	}
	if !toolSetHas(resolved.RunRequest.AgentSpec.Tools, spawntool.ToolName) {
		t.Fatalf("tools missing %s when assembly agents exist", spawntool.ToolName)
	}
	if !toolSetHas(resolved.RunRequest.AgentSpec.Tools, tasktool.ToolName) {
		t.Fatalf("tools missing %s", tasktool.ToolName)
	}
	systemPrompt, _ := resolved.RunRequest.AgentSpec.Metadata["system_prompt"].(string)
	if !strings.Contains(systemPrompt, "SPAWN for bounded child ACP work") {
		t.Fatalf("system prompt missing delegation guidance: %q", systemPrompt)
	}

	withoutAgents, session := newStackWithAssemblyForToolTest(t, sdkplugin.ResolvedAssembly{})
	resolved, err = withoutAgents.Gateway.Resolver().ResolveTurn(ctx, appgateway.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(without agents) error = %v", err)
	}
	if !toolSetHas(resolved.RunRequest.AgentSpec.Tools, spawntool.ToolName) {
		t.Fatalf("tools missing %s for default self spawn", spawntool.ToolName)
	}
	systemPrompt, _ = resolved.RunRequest.AgentSpec.Metadata["system_prompt"].(string)
	if !strings.Contains(systemPrompt, "SPAWN for bounded child ACP work") {
		t.Fatalf("system prompt missing delegation guidance for default self spawn: %q", systemPrompt)
	}
	if !agentConfigSetHas(withoutAgents.runtime.Assembly.Agents, "self") {
		t.Fatalf("self agent missing from assembly: %#v", withoutAgents.runtime.Assembly.Agents)
	}
	for _, removed := range []string{"claude", "codex", "copilot", "gemini"} {
		if agentConfigSetHas(withoutAgents.runtime.Assembly.Agents, removed) {
			t.Fatalf("unregistered built-in agent %q unexpectedly present: %#v", removed, withoutAgents.runtime.Assembly.Agents)
		}
	}
}

func TestLookupBuiltInACPAgentIncludesClaude(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	agent, ok := lookupBuiltInACPAgent("claude")
	if !ok {
		t.Fatal("lookupBuiltInACPAgent(claude) ok = false")
	}
	if agent.Name != "claude" {
		t.Fatalf("claude agent name = %q", agent.Name)
	}
	if agent.Command != "npx" {
		t.Fatalf("claude command = %q, want npx", agent.Command)
	}
	if got, want := strings.Join(agent.Args, " "), "-y @agentclientprotocol/claude-agent-acp"; got != want {
		t.Fatalf("claude args = %q, want %q", got, want)
	}
	if len(agent.Env) != 0 {
		t.Fatalf("claude env = %#v, want none", agent.Env)
	}
}

func TestRegisterBuiltinACPAgentNpxDoesNotPreferPATHAdapterBinary(t *testing.T) {
	binDir := t.TempDir()
	writeExecutableForGatewayAppTest(t, binDir, "claude-agent-acp", "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", binDir)

	stack, _ := newStackWithAssemblyForToolTest(t, sdkplugin.ResolvedAssembly{})
	if err := stack.RegisterBuiltinACPAgent("claude"); err != nil {
		t.Fatalf("RegisterBuiltinACPAgent(claude) error = %v", err)
	}
	doc, err := LoadAppConfig(stack.storeDir)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	if len(doc.Agents) != 1 {
		t.Fatalf("stored agents = %#v, want one", doc.Agents)
	}
	agent := doc.Agents[0]
	if agent.Command != "npx" {
		t.Fatalf("stored command = %q, want npx", agent.Command)
	}
	if got, want := strings.Join(agent.Args, " "), "-y @agentclientprotocol/claude-agent-acp"; got != want {
		t.Fatalf("stored args = %q, want %q", got, want)
	}
}

func TestRegisterBuiltinACPAgentInstallUsesPATHAdapterBinary(t *testing.T) {
	binDir := t.TempDir()
	claudeBin := writeExecutableForGatewayAppTest(t, binDir, "claude-agent-acp", "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", binDir)

	stack, _ := newStackWithAssemblyForToolTest(t, sdkplugin.ResolvedAssembly{})
	if err := stack.RegisterBuiltinACPAgentWithOptions(context.Background(), "claude", RegisterBuiltinACPAgentOptions{
		Install: true,
	}); err != nil {
		t.Fatalf("RegisterBuiltinACPAgentWithOptions(claude, install) error = %v", err)
	}
	doc, err := LoadAppConfig(stack.storeDir)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	if len(doc.Agents) != 1 {
		t.Fatalf("stored agents = %#v, want one", doc.Agents)
	}
	agent := doc.Agents[0]
	if agent.Command != claudeBin {
		t.Fatalf("stored command = %q, want PATH binary %q", agent.Command, claudeBin)
	}
	if len(agent.Args) != 0 {
		t.Fatalf("stored args = %#v, want none", agent.Args)
	}
}

func TestRegisterBuiltinACPAgentInstallUsesManagedAdapter(t *testing.T) {
	binDir := t.TempDir()
	writeFakeNPMInstallerForGatewayAppTest(t, binDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stack, _ := newStackWithAssemblyForToolTest(t, sdkplugin.ResolvedAssembly{})
	if err := stack.RegisterBuiltinACPAgentWithOptions(context.Background(), "claude", RegisterBuiltinACPAgentOptions{
		Install: true,
	}); err != nil {
		t.Fatalf("RegisterBuiltinACPAgentWithOptions(claude, install) error = %v", err)
	}

	doc, err := LoadAppConfig(stack.storeDir)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	if len(doc.Agents) != 1 {
		t.Fatalf("stored agents = %#v, want one", doc.Agents)
	}
	agent := doc.Agents[0]
	wantCommand := filepath.Join(stack.storeDir, "acp-agents", "npm", "node_modules", ".bin", "claude-agent-acp")
	if agent.Command != wantCommand {
		t.Fatalf("stored command = %q, want %q", agent.Command, wantCommand)
	}
	if len(agent.Args) != 0 {
		t.Fatalf("stored args = %#v, want none", agent.Args)
	}
	if len(agent.Env) != 0 {
		t.Fatalf("stored env = %#v, want none", agent.Env)
	}
}

func TestRegisterBuiltinACPAgentInstallFailureDoesNotUpdateConfig(t *testing.T) {
	binDir := t.TempDir()
	writeExecutableForGatewayAppTest(t, binDir, "npm", "#!/bin/sh\necho install failed >&2\nexit 7\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stack, _ := newStackWithAssemblyForToolTest(t, sdkplugin.ResolvedAssembly{})
	err := stack.RegisterBuiltinACPAgentWithOptions(context.Background(), "claude", RegisterBuiltinACPAgentOptions{
		Install: true,
	})
	if err == nil {
		t.Fatal("RegisterBuiltinACPAgentWithOptions(claude, install) error = nil, want failure")
	}
	doc, loadErr := LoadAppConfig(stack.storeDir)
	if loadErr != nil {
		t.Fatalf("LoadAppConfig() error = %v", loadErr)
	}
	if len(doc.Agents) != 0 {
		t.Fatalf("stored agents after failed install = %#v, want none", doc.Agents)
	}
}

func TestLocalStackAgentRegistryUpdatesWithoutRuntimeRebuild(t *testing.T) {
	ctx := context.Background()
	stack, session := newStackWithAssemblyForToolTest(t, sdkplugin.ResolvedAssembly{})
	oldGateway := stack.Gateway
	oldEngine := stack.engine

	if err := stack.RegisterBuiltinACPAgent("codex"); err != nil {
		t.Fatalf("RegisterBuiltinACPAgent(codex) error = %v", err)
	}
	if stack.Gateway != oldGateway {
		t.Fatal("RegisterBuiltinACPAgent rebuilt gateway")
	}
	if stack.engine != oldEngine {
		t.Fatal("RegisterBuiltinACPAgent rebuilt runtime engine")
	}
	resolved, err := stack.Gateway.Resolver().ResolveTurn(ctx, appgateway.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(after register) error = %v", err)
	}
	if !spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, "codex") {
		t.Fatalf("SPAWN agent enum missing codex after registry update")
	}

	if err := stack.UnregisterACPAgent("codex"); err != nil {
		t.Fatalf("UnregisterACPAgent(codex) error = %v", err)
	}
	if stack.Gateway != oldGateway {
		t.Fatal("UnregisterACPAgent rebuilt gateway")
	}
	if stack.engine != oldEngine {
		t.Fatal("UnregisterACPAgent rebuilt runtime engine")
	}
	resolved, err = stack.Gateway.Resolver().ResolveTurn(ctx, appgateway.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(after unregister) error = %v", err)
	}
	if spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, "codex") {
		t.Fatalf("SPAWN agent enum still includes codex after unregister")
	}
}

func newStackWithAssemblyForToolTest(t *testing.T, assembly sdkplugin.ResolvedAssembly) (*Stack, sdksession.Session) {
	t.Helper()
	workdir := t.TempDir()
	stack, err := NewLocalStack(Config{
		AppName:        "caelis",
		UserID:         "tool-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
		Assembly:       assembly,
		// These tests rewrite PATH to exercise ACP adapter lookup. Keep the
		// sandbox on host so auto-probing does not execute the test binary as a
		// landlock helper and recursively re-enter this package's tests.
		Sandbox: SandboxConfig{
			RequestedType: "host",
		},
		Model: ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	session, err := stack.StartSession(context.Background(), "", "surface-tool-test")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return stack, session
}

func toolSetHas(tools []sdktool.Tool, name string) bool {
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(tool.Definition().Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func spawnToolHasAgent(tools []sdktool.Tool, name string) bool {
	for _, tool := range tools {
		if tool == nil || !strings.EqualFold(strings.TrimSpace(tool.Definition().Name), spawntool.ToolName) {
			continue
		}
		schema := tool.Definition().InputSchema
		props, _ := schema["properties"].(map[string]any)
		agentProp, _ := props["agent"].(map[string]any)
		enum, _ := agentProp["enum"].([]string)
		for _, item := range enum {
			if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(name)) {
				return true
			}
		}
	}
	return false
}

func attachAgentForToolTest(t *testing.T, stack *Stack, ref sdksession.SessionRef, agent string) {
	t.Helper()
	_, err := stack.Sessions.PutParticipant(context.Background(), sdksession.PutParticipantRequest{
		SessionRef: ref,
		Binding: sdksession.ParticipantBinding{
			ID:        "sidecar-" + strings.ToLower(strings.TrimSpace(agent)),
			Kind:      sdksession.ParticipantKindACP,
			Role:      sdksession.ParticipantRoleSidecar,
			Label:     strings.TrimSpace(agent),
			SessionID: "remote-" + strings.ToLower(strings.TrimSpace(agent)),
			Source:    "test_attach",
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant(%q) error = %v", agent, err)
	}
}

func agentConfigSetHas(agents []sdkplugin.AgentConfig, name string) bool {
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func repoRootForGatewayAppTest(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

func writeExecutableForGatewayAppTest(t *testing.T, dir string, name string, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("Chmod(%s) error = %v", path, err)
	}
	return path
}

func writeFakeNPMInstallerForGatewayAppTest(t *testing.T, dir string) string {
	t.Helper()
	body := `#!/bin/sh
set -eu
prefix=""
pkg=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --prefix)
      shift
      prefix="$1"
      ;;
    @*)
      pkg="$1"
      ;;
  esac
  shift || true
done
bin="$prefix/node_modules/.bin"
mkdir -p "$bin"
case "$pkg" in
  @zed-industries/codex-acp@*)
    name="codex-acp"
    ;;
  @agentclientprotocol/claude-agent-acp@*)
    name="claude-agent-acp"
    ;;
  *)
    echo "unexpected package: $pkg" >&2
    exit 2
    ;;
esac
cat > "$bin/$name" <<'SCRIPT'
#!/bin/sh
exit 0
SCRIPT
chmod +x "$bin/$name"
`
	return writeExecutableForGatewayAppTest(t, dir, "npm", body)
}
