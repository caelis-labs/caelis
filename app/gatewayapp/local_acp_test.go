package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/impl/model/providers"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/spawn"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/task"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestLocalStackInjectsSpawnForSelfAndRegisteredACPAgents(t *testing.T) {
	ctx := context.Background()
	withAgents, session := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{
		Agents: []assembly.AgentConfig{{
			Name:        "helper",
			Description: "bounded ACP helper",
			Command:     "go",
			Args:        []string{"run", "./internal/acpe2eagent"},
			WorkDir:     repoRootForGatewayAppTest(t),
		}},
	})
	resolved, err := withAgents.Gateway.Resolver().ResolveTurn(ctx, kernel.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(with agents) error = %v", err)
	}
	if !toolSetHas(resolved.RunRequest.AgentSpec.Tools, spawn.ToolName) {
		t.Fatalf("tools missing %s when assembly agents exist", spawn.ToolName)
	}
	if !toolSetHas(resolved.RunRequest.AgentSpec.Tools, task.ToolName) {
		t.Fatalf("tools missing %s", task.ToolName)
	}
	systemPrompt, _ := resolved.RunRequest.AgentSpec.Metadata["system_prompt"].(string)
	if !strings.Contains(systemPrompt, "SPAWN for bounded child ACP work") {
		t.Fatalf("system prompt missing delegation guidance: %q", systemPrompt)
	}

	withoutAgents, session := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
	resolved, err = withoutAgents.Gateway.Resolver().ResolveTurn(ctx, kernel.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(without agents) error = %v", err)
	}
	if !toolSetHas(resolved.RunRequest.AgentSpec.Tools, spawn.ToolName) {
		t.Fatalf("tools missing %s for default self spawn", spawn.ToolName)
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
	if got, want := strings.Join(agent.Args, " "), "-y @agentclientprotocol/claude-agent-acp@^0.31.0"; got != want {
		t.Fatalf("claude args = %q, want %q", got, want)
	}
	if len(agent.Env) != 0 {
		t.Fatalf("claude env = %#v, want none", agent.Env)
	}
}

func TestDefaultSelfACPAgentPassesLiteralTokenViaEnv(t *testing.T) {
	agent := defaultSelfACPAgent(defaultSelfACPAgentConfig{
		Config: Config{
			AppName:      "caelis",
			UserID:       "u",
			StoreDir:     "/tmp/store",
			WorkspaceKey: "ws",
			WorkspaceCWD: "/tmp/ws",
			Model: ModelConfig{
				Provider: "deepseek",
				API:      providers.APIDeepSeek,
				Model:    "deepseek-reasoner",
				Token:    "super-secret-token",
			},
		},
		AppName:      "caelis",
		UserID:       "u",
		StoreDir:     "/tmp/store",
		WorkspaceKey: "ws",
		WorkspaceCWD: "/tmp/ws",
	})
	if strings.Contains(strings.Join(agent.Args, " "), "super-secret-token") {
		t.Fatalf("self ACP args leaked token: %#v", agent.Args)
	}
	if slices.Contains(agent.Args, "-token") {
		t.Fatalf("self ACP args contain raw -token flag: %#v", agent.Args)
	}
	if !slices.Contains(agent.Args, "-token-env") || !slices.Contains(agent.Args, "CAELIS_SELF_MODEL_TOKEN") {
		t.Fatalf("self ACP args = %#v, want token-env indirection", agent.Args)
	}
	if got := agent.Env["CAELIS_SELF_MODEL_TOKEN"]; got != "super-secret-token" {
		t.Fatalf("self ACP env token = %q, want literal token", got)
	}
}

func TestDefaultSelfACPAgentPreservesConfiguredTokenEnv(t *testing.T) {
	agent := defaultSelfACPAgent(defaultSelfACPAgentConfig{
		Config: Config{
			AppName:      "caelis",
			UserID:       "u",
			StoreDir:     "/tmp/store",
			WorkspaceKey: "ws",
			WorkspaceCWD: "/tmp/ws",
			Model: ModelConfig{
				Provider: "deepseek",
				API:      providers.APIDeepSeek,
				Model:    "deepseek-reasoner",
				TokenEnv: "DEEPSEEK_API_KEY",
			},
		},
		AppName:      "caelis",
		UserID:       "u",
		StoreDir:     "/tmp/store",
		WorkspaceKey: "ws",
		WorkspaceCWD: "/tmp/ws",
	})
	if !slices.Contains(agent.Args, "DEEPSEEK_API_KEY") {
		t.Fatalf("self ACP args = %#v, want configured token env", agent.Args)
	}
	if len(agent.Env) != 0 {
		t.Fatalf("self ACP env = %#v, want none for configured token env", agent.Env)
	}
}

func TestRegisterBuiltinACPAgentNpxDoesNotPreferPATHAdapterBinary(t *testing.T) {
	binDir := t.TempDir()
	writeExecutableForGatewayAppTest(t, binDir, "claude-agent-acp", "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", binDir)

	stack, _ := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
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
	if got, want := strings.Join(agent.Args, " "), "-y @agentclientprotocol/claude-agent-acp@^0.31.0"; got != want {
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

	stack, _ := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
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
	wantCommand := managedACPAgentBinPath(stack.managedACPAgentRoot(), "claude-agent-acp")
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
	if got, want := strings.Count(string(logData), "@agentclientprotocol/claude-agent-acp@^0.31.0"), 1; got != want {
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

	stack, _ := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
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
	wantCommand := managedACPAgentBinPath(stack.managedACPAgentRoot(), "codex-acp")
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

	stack, _ := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
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
	wantCommand := managedACPAgentBinPath(stack.managedACPAgentRoot(), "claude-agent-acp")
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

	stack, _ := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
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
	stack, session := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
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
	resolved, err := stack.Gateway.Resolver().ResolveTurn(ctx, kernel.TurnIntent{SessionRef: session.SessionRef})
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
	resolved, err = stack.Gateway.Resolver().ResolveTurn(ctx, kernel.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(after unregister) error = %v", err)
	}
	if spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, "codex") {
		t.Fatalf("SPAWN agent enum still includes codex after unregister")
	}
}

func TestRegisterCustomACPAgentUpdatesConfigAndRuntimeRegistry(t *testing.T) {
	ctx := context.Background()
	stack, session := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
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
	resolved, err := stack.Gateway.Resolver().ResolveTurn(ctx, kernel.TurnIntent{SessionRef: session.SessionRef})
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
	resolved, err = stack.Gateway.Resolver().ResolveTurn(ctx, kernel.TurnIntent{SessionRef: session.SessionRef})
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
		PermissionMode: "auto-review",
		ContextWindow:  12345,
		Sandbox: SandboxConfig{
			RequestedType: "host",
		},
		Model: ModelConfig{
			Provider:     "deepseek",
			API:          providers.APIDeepSeek,
			Model:        "deepseek-reasoner",
			BaseURL:      "https://api.deepseek.example/v1",
			TokenEnv:     "DEEPSEEK_TEST_TOKEN",
			AuthType:     providers.AuthAPIKey,
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

func newStackWithAssemblyForToolTest(t *testing.T, assembly assembly.ResolvedAssembly) (*Stack, session.Session) {
	t.Helper()
	workdir := t.TempDir()
	stack, err := NewLocalStack(Config{
		AppName:        "caelis",
		UserID:         "tool-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "auto-review",
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

func toolSetHas(tools []tool.Tool, name string) bool {
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

func spawnToolHasAgent(tools []tool.Tool, name string) bool {
	for _, tool := range tools {
		if tool == nil || !strings.EqualFold(strings.TrimSpace(tool.Definition().Name), spawn.ToolName) {
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

func attachAgentForToolTest(t *testing.T, stack *Stack, ref session.SessionRef, agent string) {
	t.Helper()
	_, err := stack.Sessions.PutParticipant(context.Background(), session.PutParticipantRequest{
		SessionRef: ref,
		Binding: session.ParticipantBinding{
			ID:        "sidecar-" + strings.ToLower(strings.TrimSpace(agent)),
			Kind:      session.ParticipantKindACP,
			Role:      session.ParticipantRoleSidecar,
			Label:     strings.TrimSpace(agent),
			SessionID: "remote-" + strings.ToLower(strings.TrimSpace(agent)),
			Source:    "test_attach",
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant(%q) error = %v", agent, err)
	}
}

func agentConfigSetHas(agents []assembly.AgentConfig, name string) bool {
	_, ok := agentConfigForToolTest(agents, name)
	return ok
}

func agentConfigForToolTest(agents []assembly.AgentConfig, name string) (assembly.AgentConfig, bool) {
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.Name), strings.TrimSpace(name)) {
			return agent, true
		}
	}
	return assembly.AgentConfig{}, false
}

func assertSelfAgentArgsForModel(t *testing.T, agents []assembly.AgentConfig) {
	t.Helper()
	self, ok := agentConfigForToolTest(agents, "self")
	if !ok {
		t.Fatalf("self agent missing from assembly: %#v", agents)
	}
	for _, want := range []string{
		"-provider", "deepseek",
		"-api", string(providers.APIDeepSeek),
		"-model", "deepseek-reasoner",
		"-base-url", "https://api.deepseek.example/v1",
		"-token-env", "DEEPSEEK_TEST_TOKEN",
		"-auth-type", string(providers.AuthAPIKey),
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
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		path += ".cmd"
		if strings.Contains(body, "install failed") {
			body = "@echo off\r\necho install failed 1>&2\r\nexit /b 7\r\n"
		} else {
			body = "@echo off\r\nexit /b 0\r\n"
		}
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatalf("Chmod(%s) error = %v", path, err)
		}
	}
	return path
}

func writeFakeNPMInstallerForGatewayAppTest(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		scriptPath := filepath.Join(dir, "npm-installer.ps1")
		script := `$ErrorActionPreference = 'Stop'
$prefix = ''
$pkg = ''
for ($i = 0; $i -lt $args.Count; $i++) {
  $arg = $args[$i]
  if ($arg -eq '--prefix') {
    $i++
    if ($i -lt $args.Count) {
      $prefix = $args[$i]
    }
    continue
  }
  if ($arg -like '@zed-industries/codex-acp@*' -or $arg -like '@agentclientprotocol/claude-agent-acp@*') {
    $pkg = $arg
  }
}
if ([string]::IsNullOrWhiteSpace($prefix)) {
  Write-Error 'missing --prefix'
  exit 2
}
$bin = Join-Path $prefix 'node_modules\.bin'
New-Item -ItemType Directory -Force -Path $bin | Out-Null
$name = ''
switch -Wildcard ($pkg) {
  '@zed-industries/codex-acp@*' { $name = 'codex-acp.cmd'; break }
  '@agentclientprotocol/claude-agent-acp@*' { $name = 'claude-agent-acp.cmd'; break }
}
if ([string]::IsNullOrWhiteSpace($name)) {
  Write-Error "unexpected package: $pkg"
  exit 2
}
if ($env:CAELIS_FAKE_NPM_LOG) {
  Add-Content -LiteralPath $env:CAELIS_FAKE_NPM_LOG -Value $pkg
}
$adapter = "@echo off" + [Environment]::NewLine + "exit /b 0" + [Environment]::NewLine
Set-Content -LiteralPath (Join-Path $bin $name) -Value $adapter -NoNewline -Encoding ASCII
exit 0
`
		if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", scriptPath, err)
		}
		body := `@echo off
powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "%~dp0npm-installer.ps1" %*
exit /b %ERRORLEVEL%
`
		return writeCommandFileForGatewayAppTest(t, dir, "npm.cmd", body)
	}
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

func writeCommandFileForGatewayAppTest(t *testing.T, dir string, name string, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatalf("Chmod(%s) error = %v", path, err)
		}
	}
	return path
}
