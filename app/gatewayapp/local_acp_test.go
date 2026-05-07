package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	sdkproviders "github.com/OnslaughtSnail/caelis/sdk/model/providers"
	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
	spawntool "github.com/OnslaughtSnail/caelis/sdk/tool/builtin/spawn"
	tasktool "github.com/OnslaughtSnail/caelis/sdk/tool/builtin/task"
)

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

func TestRegisterBuiltinACPAgentInstallRunsNPMEvenWhenPATHAdapterExists(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "npm.log")
	writeExecutableForGatewayAppTest(t, binDir, "claude-agent-acp", "#!/bin/sh\nexit 0\n")
	writeFakeNPMInstallerForGatewayAppTest(t, binDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAELIS_FAKE_NPM_LOG", logPath)

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
		t.Fatalf("stored command = %q, want managed adapter %q", agent.Command, wantCommand)
	}
	if len(agent.Args) != 0 {
		t.Fatalf("stored args = %#v, want none", agent.Args)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(npm log) error = %v", err)
	}
	if got, want := strings.Count(string(logData), "@agentclientprotocol/claude-agent-acp@latest"), 1; got != want {
		t.Fatalf("npm install count = %d, want %d; log=%q", got, want, string(logData))
	}
}

func TestRegisterBuiltinACPAgentInstallUpdatesManagedAdapterOnRepeatedRuns(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "npm.log")
	writeExecutableForGatewayAppTest(t, binDir, "codex-acp", "#!/bin/sh\nexit 0\n")
	writeFakeNPMInstallerForGatewayAppTest(t, binDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAELIS_FAKE_NPM_LOG", logPath)

	stack, _ := newStackWithAssemblyForToolTest(t, sdkplugin.ResolvedAssembly{})
	for i := 0; i < 2; i++ {
		if err := stack.RegisterBuiltinACPAgentWithOptions(context.Background(), "codex", RegisterBuiltinACPAgentOptions{
			Install: true,
		}); err != nil {
			t.Fatalf("RegisterBuiltinACPAgentWithOptions(codex, install #%d) error = %v", i+1, err)
		}
	}
	doc, err := LoadAppConfig(stack.storeDir)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	if len(doc.Agents) != 1 {
		t.Fatalf("stored agents = %#v, want one", doc.Agents)
	}
	wantCommand := filepath.Join(stack.storeDir, "acp-agents", "npm", "node_modules", ".bin", "codex-acp")
	if doc.Agents[0].Command != wantCommand {
		t.Fatalf("stored command = %q, want managed adapter %q", doc.Agents[0].Command, wantCommand)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(npm log) error = %v", err)
	}
	if got, want := strings.Count(string(logData), "@zed-industries/codex-acp@latest"), 2; got != want {
		t.Fatalf("npm install count = %d, want %d; log=%q", got, want, string(logData))
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

func TestRegisterCustomACPAgentUpdatesConfigAndRuntimeRegistry(t *testing.T) {
	ctx := context.Background()
	stack, session := newStackWithAssemblyForToolTest(t, sdkplugin.ResolvedAssembly{})
	oldGateway := stack.Gateway
	oldEngine := stack.engine

	cfg := AgentConfig{
		Name:        "helper",
		Description: "custom helper",
		Command:     "helper-acp",
		Args:        []string{"--stdio"},
		Env:         map[string]string{"HELPER_MODE": "test"},
		WorkDir:     "/tmp/helper",
		Builtin:     true,
	}
	if err := stack.RegisterACPAgent(ctx, cfg); err != nil {
		t.Fatalf("RegisterACPAgent(helper) error = %v", err)
	}
	if stack.Gateway != oldGateway {
		t.Fatal("RegisterACPAgent rebuilt gateway")
	}
	if stack.engine != oldEngine {
		t.Fatal("RegisterACPAgent rebuilt runtime engine")
	}
	doc, err := LoadAppConfig(stack.storeDir)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	if len(doc.Agents) != 1 {
		t.Fatalf("stored agents = %#v, want one", doc.Agents)
	}
	agent := doc.Agents[0]
	if agent.Name != "helper" || agent.Command != "helper-acp" || agent.Builtin {
		t.Fatalf("stored custom agent = %#v, want custom helper", agent)
	}
	if got, want := strings.Join(agent.Args, " "), "--stdio"; got != want {
		t.Fatalf("stored args = %q, want %q", got, want)
	}
	if got, want := agent.Env["HELPER_MODE"], "test"; got != want {
		t.Fatalf("stored env HELPER_MODE = %q, want %q", got, want)
	}
	resolved, err := stack.Gateway.Resolver().ResolveTurn(ctx, appgateway.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(after custom register) error = %v", err)
	}
	if !spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, "helper") {
		t.Fatalf("SPAWN agent enum missing helper after custom registry update")
	}

	replacement := AgentConfig{Name: "helper", Command: "helper-v2", Args: []string{"--json"}}
	if err := stack.RegisterACPAgent(ctx, replacement); err != nil {
		t.Fatalf("RegisterACPAgent(helper replacement) error = %v", err)
	}
	doc, err = LoadAppConfig(stack.storeDir)
	if err != nil {
		t.Fatalf("LoadAppConfig(after replacement) error = %v", err)
	}
	if len(doc.Agents) != 1 {
		t.Fatalf("stored agents after replacement = %#v, want one", doc.Agents)
	}
	if got, want := doc.Agents[0].Command, "helper-v2"; got != want {
		t.Fatalf("replacement command = %q, want %q", got, want)
	}

	if err := stack.UnregisterACPAgent("helper"); err != nil {
		t.Fatalf("UnregisterACPAgent(helper) error = %v", err)
	}
	resolved, err = stack.Gateway.Resolver().ResolveTurn(ctx, appgateway.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(after custom unregister) error = %v", err)
	}
	if spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, "helper") {
		t.Fatalf("SPAWN agent enum still includes helper after unregister")
	}
}

func TestLocalStackAgentRegistryUpdatesPreserveSelfModelArgs(t *testing.T) {
	workdir := t.TempDir()
	stack, err := NewLocalStack(Config{
		AppName:        "caelis",
		UserID:         "self-model-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
		ContextWindow:  12345,
		Sandbox: SandboxConfig{
			RequestedType: "host",
		},
		Model: ModelConfig{
			Provider:     "deepseek",
			API:          sdkproviders.APIDeepSeek,
			Model:        "deepseek-reasoner",
			BaseURL:      "https://api.deepseek.example/v1",
			TokenEnv:     "DEEPSEEK_TEST_TOKEN",
			AuthType:     sdkproviders.AuthAPIKey,
			HeaderKey:    "Authorization",
			MaxOutputTok: 8192,
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}

	if err := stack.RegisterBuiltinACPAgent("codex"); err != nil {
		t.Fatalf("RegisterBuiltinACPAgent(codex) error = %v", err)
	}
	assertSelfAgentArgsForModel(t, stack.runtime.Assembly.Agents)

	if err := stack.UnregisterACPAgent("codex"); err != nil {
		t.Fatalf("UnregisterACPAgent(codex) error = %v", err)
	}
	assertSelfAgentArgsForModel(t, stack.runtime.Assembly.Agents)
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
	_, ok := agentConfigForToolTest(agents, name)
	return ok
}

func agentConfigForToolTest(agents []sdkplugin.AgentConfig, name string) (sdkplugin.AgentConfig, bool) {
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.Name), strings.TrimSpace(name)) {
			return agent, true
		}
	}
	return sdkplugin.AgentConfig{}, false
}

func assertSelfAgentArgsForModel(t *testing.T, agents []sdkplugin.AgentConfig) {
	t.Helper()
	self, ok := agentConfigForToolTest(agents, "self")
	if !ok {
		t.Fatalf("self agent missing from assembly: %#v", agents)
	}
	for _, want := range []string{
		"-provider", "deepseek",
		"-api", string(sdkproviders.APIDeepSeek),
		"-model", "deepseek-reasoner",
		"-base-url", "https://api.deepseek.example/v1",
		"-token-env", "DEEPSEEK_TEST_TOKEN",
		"-auth-type", string(sdkproviders.AuthAPIKey),
		"-header-key", "Authorization",
		"-context-window", "12345",
		"-max-output-tokens", "8192",
	} {
		if !slices.Contains(self.Args, want) {
			t.Fatalf("self args = %#v, missing %q", self.Args, want)
		}
	}
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
if [ -n "${CAELIS_FAKE_NPM_LOG:-}" ]; then
  printf '%s\n' "$pkg" >> "$CAELIS_FAKE_NPM_LOG"
fi
cat > "$bin/$name" <<'SCRIPT'
#!/bin/sh
exit 0
SCRIPT
chmod +x "$bin/$name"
`
	return writeExecutableForGatewayAppTest(t, dir, "npm", body)
}
