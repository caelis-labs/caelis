package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/model/providers"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/spawn"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/task"
	"github.com/OnslaughtSnail/caelis/internal/testenv"
	"github.com/OnslaughtSnail/caelis/ports/agentprofile"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
	"github.com/OnslaughtSnail/caelis/protocol/acp"
)

func TestLocalStackInjectsSpawnForSelfAndAgentProfilesOnly(t *testing.T) {
	ctx := context.Background()
	withAgents, session := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{
		Agents: []assembly.AgentConfig{{
			Name:        "helper",
			Description: "bounded ACP helper",
			Command:     "go",
			Args:        []string{"run", "./internal/acpe2eagent"},
			WorkDir:     repoRootForGatewayAppTest(t),
		}, {
			Name:        "claude",
			Description: "native Claude ACP agent",
			Command:     "claude",
			Args:        []string{"acp"},
		}, {
			Name:        "codex",
			Description: "native Codex ACP agent",
			Command:     "codex",
			Args:        []string{"acp"},
		}},
	})
	resolved, err := withAgents.currentGateway().Resolver().ResolveTurn(ctx, gateway.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(with agents) error = %v", err)
	}
	if !toolSetHas(resolved.RunRequest.AgentSpec.Tools, spawn.ToolName) {
		t.Fatalf("tools missing %s when assembly agents exist", spawn.ToolName)
	}
	if !toolSetHas(resolved.RunRequest.AgentSpec.Tools, task.ToolName) {
		t.Fatalf("tools missing %s", task.ToolName)
	}
	for _, want := range []string{"self", "explorer", "reviewer"} {
		if !spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, want) {
			t.Fatalf("SPAWN agent enum missing %q with profile agents", want)
		}
	}
	if spawnToolRequiresAgent(resolved.RunRequest.AgentSpec.Tools) {
		t.Fatalf("SPAWN agent is required despite self default")
	}
	for _, forbidden := range []string{"helper", "claude", "codex"} {
		if spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, forbidden) {
			t.Fatalf("SPAWN agent enum includes raw ACP agent %q", forbidden)
		}
	}
	systemPrompt, _ := resolved.RunRequest.AgentSpec.Metadata["system_prompt"].(string)
	if !strings.Contains(systemPrompt, "Delegate only when the subtask has clear independent scope") {
		t.Fatalf("system prompt missing delegation guidance: %q", systemPrompt)
	}

	withoutAgents, session := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
	resolved, err = withoutAgents.currentGateway().Resolver().ResolveTurn(ctx, gateway.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(without agents) error = %v", err)
	}
	if !toolSetHas(resolved.RunRequest.AgentSpec.Tools, spawn.ToolName) {
		t.Fatalf("tools missing %s for default profile spawn", spawn.ToolName)
	}
	for _, want := range []string{"self", "explorer", "reviewer"} {
		if !spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, want) {
			t.Fatalf("SPAWN agent enum missing %q for default profile agents", want)
		}
	}
	if spawnToolRequiresAgent(resolved.RunRequest.AgentSpec.Tools) {
		t.Fatalf("SPAWN agent is required for default profile agents")
	}
	systemPrompt, _ = resolved.RunRequest.AgentSpec.Metadata["system_prompt"].(string)
	if !strings.Contains(systemPrompt, "Delegate only when the subtask has clear independent scope") {
		t.Fatalf("system prompt missing delegation guidance for default profile spawn: %q", systemPrompt)
	}
	if !agentConfigSetHas(withoutAgents.runtime.Assembly.Agents, "self") {
		t.Fatalf("self agent missing from assembly: %#v", withoutAgents.runtime.Assembly.Agents)
	}
	for _, removed := range []string{"claude", "codex", "opencode", "codefree-o", "copilot", "gemini"} {
		if agentConfigSetHas(withoutAgents.runtime.Assembly.Agents, removed) {
			t.Fatalf("unregistered built-in agent %q unexpectedly present: %#v", removed, withoutAgents.runtime.Assembly.Agents)
		}
	}
}

func TestSpawnFallsBackToImplicitSelfWhenNoSubagentProfilesExist(t *testing.T) {
	agents := delegationAgentsForSpawn(assembly.ResolvedAssembly{
		Agents: []assembly.AgentConfig{{
			Name:        "self",
			Description: "Caelis self ACP agent",
		}},
	}, nil)
	tools := spawnTools(agents)
	if !toolSetHas(tools, spawn.ToolName) {
		t.Fatalf("tools missing %s for self fallback", spawn.ToolName)
	}
	if !spawnToolHasAgent(tools, "self") {
		t.Fatalf("SPAWN agent enum missing self for implicit fallback")
	}
	if spawnToolRequiresAgent(tools) {
		t.Fatalf("SPAWN agent is required for implicit self fallback")
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

func TestACPSurfaceAvailableCommandsExposeRegisteredACPAgents(t *testing.T) {
	stack, session := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{
		Agents: []assembly.AgentConfig{{
			Name:        "helper",
			Description: "bounded ACP helper",
			Command:     "go",
			Args:        []string{"run", "./internal/acpe2eagent"},
			WorkDir:     repoRootForGatewayAppTest(t),
		}},
	})
	commands, err := stack.ACPSurface(nil, false, nil).AvailableCommands(context.Background(), session.SessionID)
	if err != nil {
		t.Fatalf("AvailableCommands() error = %v", err)
	}
	if acpCommandForToolTest(commands, "approval") != nil {
		t.Fatalf("AvailableCommands() = %#v, should not expose removed /approval command", commands)
	}
	helper := acpCommandForToolTest(commands, "helper")
	if helper == nil {
		t.Fatalf("AvailableCommands() = %#v, want helper ACP agent command", commands)
		return
	}
	if helper.Description != "bounded ACP helper" {
		t.Fatalf("helper description = %q, want assembly description", helper.Description)
	}
	for _, hidden := range []string{"explorer", "reviewer", "guardian"} {
		if acpCommandForToolTest(commands, hidden) != nil {
			t.Fatalf("AvailableCommands() exposed subagent profile command %q: %#v", hidden, commands)
		}
	}
}

func TestACPSurfaceAvailableCommandsHideReservedACPAgentNames(t *testing.T) {
	stack, session := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{
		Agents: []assembly.AgentConfig{{
			Name:        "status",
			Description: "reserved collision",
			Command:     "go",
			Args:        []string{"run", "./internal/acpe2eagent"},
			WorkDir:     repoRootForGatewayAppTest(t),
		}},
	})
	commands, err := stack.ACPSurface(nil, false, nil).AvailableCommands(context.Background(), session.SessionID)
	if err != nil {
		t.Fatalf("AvailableCommands() error = %v", err)
	}
	status := acpCommandForToolTest(commands, "status")
	if status == nil {
		t.Fatalf("AvailableCommands() = %#v, want built-in /status command", commands)
	}
	for _, command := range commands {
		if strings.EqualFold(strings.TrimSpace(command.Name), "status") && command.Description == "reserved collision" {
			t.Fatalf("AvailableCommands() exposed reserved ACP agent command: %#v", command)
		}
	}
}

func TestLookupBuiltInACPAgentIncludesNativeOpenCodeFamily(t *testing.T) {
	for _, tt := range []struct {
		name        string
		command     string
		description string
	}{
		{name: "opencode", command: "opencode", description: "OpenCode ACP agent"},
		{name: "codefree-o", command: "codefree-o", description: "CodeFree-O ACP agent"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			agent, ok := lookupBuiltInACPAgent(tt.name)
			if !ok {
				t.Fatalf("lookupBuiltInACPAgent(%s) ok = false", tt.name)
			}
			if agent.Name != tt.name {
				t.Fatalf("%s agent name = %q", tt.name, agent.Name)
			}
			if agent.Description != tt.description {
				t.Fatalf("%s description = %q, want %q", tt.name, agent.Description, tt.description)
			}
			if agent.Command != tt.command {
				t.Fatalf("%s command = %q, want %q", tt.name, agent.Command, tt.command)
			}
			if got, want := strings.Join(agent.Args, " "), "acp"; got != want {
				t.Fatalf("%s args = %q, want %q", tt.name, got, want)
			}
			if len(agent.Env) != 0 {
				t.Fatalf("%s env = %#v, want none", tt.name, agent.Env)
			}
			if _, ok := builtinACPAdapterPackageFor(tt.name); ok {
				t.Fatalf("%s unexpectedly has an npm adapter package", tt.name)
			}
		})
	}
}

func acpCommandForToolTest(commands []acp.AvailableCommand, name string) *acp.AvailableCommand {
	for i := range commands {
		if strings.EqualFold(strings.TrimSpace(commands[i].Name), strings.TrimSpace(name)) {
			return &commands[i]
		}
	}
	return nil
}

func TestNativeOpenCodeFamilyBuiltinOptionsAreAddOnly(t *testing.T) {
	stack, _ := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
	addOptions := stack.ListBuiltinACPAgentAddOptions()
	installOptions := stack.ListInstallableACPAgentOptions()
	for _, name := range []string{"opencode", "codefree-o"} {
		option, ok := acpAgentAddOptionForToolTest(addOptions, name)
		if !ok {
			t.Fatalf("builtin add options missing %s: %#v", name, addOptions)
		}
		if option.Display != name {
			t.Fatalf("%s add display = %q, want %q", name, option.Display, name)
		}
		if strings.Contains(option.Detail, "npx") || strings.Contains(option.Detail, "npm install") {
			t.Fatalf("%s add detail = %q, want native command detail", name, option.Detail)
		}
		if _, ok := acpAgentAddOptionForToolTest(installOptions, name); ok {
			t.Fatalf("%s unexpectedly appears in installable options: %#v", name, installOptions)
		}
	}
}

func TestRegisterNativeOpenCodeFamilyBuiltinAgents(t *testing.T) {
	stack, _ := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
	for _, name := range []string{"opencode", "codefree-o"} {
		if err := stack.RegisterBuiltinACPAgent(name); err != nil {
			t.Fatalf("RegisterBuiltinACPAgent(%s) error = %v", name, err)
		}
	}
	doc, err := LoadAppConfig(stack.storeDir)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	for _, name := range []string{"opencode", "codefree-o"} {
		agent, ok := storedAgentConfigForToolTest(doc.Agents, name)
		if !ok {
			t.Fatalf("stored agents missing %s: %#v", name, doc.Agents)
		}
		if agent.Command != name {
			t.Fatalf("%s stored command = %q, want %q", name, agent.Command, name)
		}
		if got, want := strings.Join(agent.Args, " "), "acp"; got != want {
			t.Fatalf("%s stored args = %q, want %q", name, got, want)
		}
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
	oldGateway := stack.currentGateway()
	oldEngine := stack.engine

	if err := stack.RegisterBuiltinACPAgent("codex"); err != nil {
		t.Fatalf("RegisterBuiltinACPAgent(codex) error = %v", err)
	}
	if stack.currentGateway() != oldGateway {
		t.Fatal("RegisterBuiltinACPAgent rebuilt gateway")
	}
	if stack.engine != oldEngine {
		t.Fatal("RegisterBuiltinACPAgent rebuilt runtime engine")
	}
	resolved, err := stack.currentGateway().Resolver().ResolveTurn(ctx, gateway.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(after register) error = %v", err)
	}
	if spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, "codex") {
		t.Fatalf("SPAWN agent enum includes raw external ACP codex after registry update")
	}
	commands, err := stack.ACPSurface(nil, false, nil).AvailableCommands(ctx, session.SessionID)
	if err != nil {
		t.Fatalf("AvailableCommands(after codex register) error = %v", err)
	}
	if acpCommandForToolTest(commands, "codex") == nil {
		t.Fatalf("AvailableCommands() = %#v, want /codex command after registry update", commands)
	}

	if err := stack.UnregisterACPAgent("codex"); err != nil {
		t.Fatalf("UnregisterACPAgent(codex) error = %v", err)
	}
	if stack.currentGateway() != oldGateway {
		t.Fatal("UnregisterACPAgent rebuilt gateway")
	}
	if stack.engine != oldEngine {
		t.Fatal("UnregisterACPAgent rebuilt runtime engine")
	}
	resolved, err = stack.currentGateway().Resolver().ResolveTurn(ctx, gateway.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(after unregister) error = %v", err)
	}
	if spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, "codex") {
		t.Fatalf("SPAWN agent enum still includes codex after unregister")
	}
	commands, err = stack.ACPSurface(nil, false, nil).AvailableCommands(ctx, session.SessionID)
	if err != nil {
		t.Fatalf("AvailableCommands(after codex unregister) error = %v", err)
	}
	if acpCommandForToolTest(commands, "codex") != nil {
		t.Fatalf("AvailableCommands() still exposes /codex after unregister: %#v", commands)
	}
}

func TestRegisterCustomACPAgentUpdatesConfigAndRuntimeRegistry(t *testing.T) {
	ctx := context.Background()
	stack, session := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
	oldGateway := stack.currentGateway()
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
	if stack.currentGateway() != oldGateway {
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
	resolved, err := stack.currentGateway().Resolver().ResolveTurn(ctx, gateway.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(after custom register) error = %v", err)
	}
	if spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, "helper") {
		t.Fatalf("SPAWN agent enum includes raw external ACP helper after custom registry update")
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
	resolved, err = stack.currentGateway().Resolver().ResolveTurn(ctx, gateway.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(after custom unregister) error = %v", err)
	}
	if spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, "helper") {
		t.Fatalf("SPAWN agent enum still includes helper after unregister")
	}
}

func TestAgentProfileBindACPAgentPreservesRegisteredName(t *testing.T) {
	ctx := context.Background()
	stack, session := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
	if err := stack.RegisterACPAgent(ctx, AgentConfig{
		Name:        "my_agent",
		Description: "custom reviewer runtime",
		Command:     "my-agent-acp",
		Args:        []string{"--stdio"},
	}); err != nil {
		t.Fatalf("RegisterACPAgent(my_agent) error = %v", err)
	}
	status, err := stack.AgentProfiles().Bind(ctx, AgentProfileBindingConfig{
		ProfileID: "reviewer",
		Target:    agentprofile.BindingTargetACP,
		ACPAgent:  "my_agent",
	})
	if err != nil {
		t.Fatalf("AgentProfiles.Bind(reviewer acp my_agent) error = %v", err)
	}
	reviewerStatus, ok := agentProfileSnapshotForToolTest(status.Profiles, "reviewer")
	if !ok {
		t.Fatalf("reviewer profile missing from status: %#v", status.Profiles)
	}
	binding := agentprofile.NormalizeBinding(reviewerStatus.Binding)
	if binding.Target != agentprofile.BindingTargetACP || binding.ACPAgent != "my_agent" {
		t.Fatalf("reviewer binding = %#v, want ACP my_agent", binding)
	}
	reviewer, ok := agentConfigForToolTest(stack.runtime.Assembly.Agents, "reviewer")
	if !ok {
		t.Fatalf("reviewer profile agent missing from assembly: %#v", stack.runtime.Assembly.Agents)
	}
	if reviewer.Command != "my-agent-acp" || !slices.Contains(reviewer.Args, "--stdio") {
		t.Fatalf("reviewer agent = %#v, want clone of my_agent", reviewer)
	}
	if reviewer.Env["CAELIS_SUBAGENT_PROFILE_ID"] != "reviewer" {
		t.Fatalf("reviewer env = %#v, want reviewer profile marker", reviewer.Env)
	}
	resolved, err := stack.currentGateway().Resolver().ResolveTurn(ctx, gateway.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(reviewer ACP binding) error = %v", err)
	}
	if !spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, "reviewer") {
		t.Fatalf("SPAWN agent enum missing reviewer profile binding")
	}
	if spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, "my_agent") {
		t.Fatalf("SPAWN agent enum includes raw external ACP my_agent")
	}
}

func TestAgentProfileNameCollisionReportsStaleAndNotRunnable(t *testing.T) {
	ctx := context.Background()
	stack, session := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{
		Agents: []assembly.AgentConfig{{
			Name:        "reviewer",
			Description: "raw reviewer runtime",
			Command:     "go",
			Args:        []string{"run", "./internal/acpe2eagent"},
			WorkDir:     repoRootForGatewayAppTest(t),
		}},
	})
	reviewerAgent, ok := agentConfigForToolTest(stack.runtime.Assembly.Agents, "reviewer")
	if !ok {
		t.Fatalf("reviewer agent missing from assembly: %#v", stack.runtime.Assembly.Agents)
	}
	if isSubagentProfileAgent(reviewerAgent) {
		t.Fatalf("reviewer collision materialized as profile agent: %#v", reviewerAgent)
	}
	status, err := stack.AgentProfiles().Status(ctx)
	if err != nil {
		t.Fatalf("AgentProfiles.Status() error = %v", err)
	}
	reviewer, ok := agentProfileSnapshotForToolTest(status.Profiles, "reviewer")
	if !ok {
		t.Fatalf("reviewer profile missing from status: %#v", status.Profiles)
	}
	binding := agentprofile.NormalizeBinding(reviewer.Binding)
	if binding.Status != agentprofile.BindingStatusStale || !strings.Contains(binding.Warning, "conflicts") {
		t.Fatalf("reviewer binding = %#v, want stale conflict", binding)
	}
	resolved, err := stack.currentGateway().Resolver().ResolveTurn(ctx, gateway.TurnIntent{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(reviewer collision) error = %v", err)
	}
	if spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, "reviewer") {
		t.Fatalf("SPAWN agent enum includes conflicted reviewer profile")
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

func TestAgentProfileDefaultModelAgentsRefreshAfterModelChanges(t *testing.T) {
	ctx := context.Background()
	stack, session := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
	assertAgentArgsContain(t, stack.runtime.Assembly.Agents, "self", "-provider", "ollama", "-model", "llama3")
	assertAgentArgsContain(t, stack.runtime.Assembly.Agents, "reviewer", "-provider", "ollama", "-model", "llama3")

	alias, err := stack.Connect(ModelConfig{
		Alias:    "review-model",
		Provider: "deepseek",
		API:      providers.APIDeepSeek,
		Model:    "deepseek-reasoner",
		TokenEnv: "DEEPSEEK_TEST_TOKEN",
	})
	if err != nil {
		t.Fatalf("Connect(review-model) error = %v", err)
	}
	assertAgentArgsContain(t, stack.runtime.Assembly.Agents, "self", "-provider", "deepseek", "-model", "deepseek-reasoner")
	assertAgentArgsContain(t, stack.runtime.Assembly.Agents, "reviewer", "-provider", "deepseek", "-model", "deepseek-reasoner")

	if err := stack.DeleteModel(ctx, session.SessionRef, alias); err != nil {
		t.Fatalf("DeleteModel(review-model) error = %v", err)
	}
	assertAgentArgsContain(t, stack.runtime.Assembly.Agents, "self", "-provider", "ollama", "-model", "llama3")
	assertAgentArgsContain(t, stack.runtime.Assembly.Agents, "reviewer", "-provider", "ollama", "-model", "llama3")
}

func TestLocalStackMaterializesBuiltInAgentProfiles(t *testing.T) {
	workdir := t.TempDir()
	storeDir := t.TempDir()
	stack, err := NewLocalStack(Config{
		AppName:        "caelis",
		UserID:         "profile-agent-test",
		StoreDir:       storeDir,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "auto-review",
		Sandbox:        SandboxConfig{RequestedType: "host"},
		Model: ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	for _, name := range []string{"explorer", "reviewer"} {
		agent, ok := agentConfigForToolTest(stack.runtime.Assembly.Agents, name)
		if !ok {
			t.Fatalf("%s profile agent missing from assembly: %#v", name, stack.runtime.Assembly.Agents)
		}
		if agent.Env["SDK_ACP_ENABLE_SPAWN"] != "0" || agent.Env["SDK_ACP_CHILD_NO_SPAWN"] != "1" {
			t.Fatalf("%s env = %#v, want SPAWN disabled", name, agent.Env)
		}
		if !slices.Contains(agent.Args, "-system-prompt") {
			t.Fatalf("%s args = %#v, want profile system prompt", name, agent.Args)
		}
		if _, err := os.Stat(filepath.Join(storeDir, "agents", name+".md")); err != nil {
			t.Fatalf("%s profile file missing: %v", name, err)
		}
	}
	if agentConfigSetHas(stack.runtime.Assembly.Agents, "guardian") {
		t.Fatalf("guardian should not materialize as a SPAWN ACP agent: %#v", stack.runtime.Assembly.Agents)
	}
	if _, err := os.Stat(filepath.Join(storeDir, "agents", "guardian.md")); !os.IsNotExist(err) {
		t.Fatalf("guardian profile file stat error = %v, want not exist", err)
	}
	status, err := stack.AgentProfiles().Status(context.Background())
	if err != nil {
		t.Fatalf("AgentProfiles.Status() error = %v", err)
	}
	if len(status.Profiles) != 3 {
		t.Fatalf("profile count = %d, want 3: %#v", len(status.Profiles), status.Profiles)
	}
	guardian, ok := agentProfileSnapshotForToolTest(status.Profiles, "guardian")
	if !ok {
		t.Fatalf("guardian virtual profile missing from status: %#v", status.Profiles)
	}
	guardianProfile := agentprofile.NormalizeProfile(guardian.Profile)
	guardianBinding := agentprofile.NormalizeBinding(guardian.Binding)
	if guardianProfile.Path != "" {
		t.Fatalf("guardian path = %q, want virtual profile without file path", guardianProfile.Path)
	}
	if value, _ := guardianProfile.Metadata["system_managed"].(bool); !value {
		t.Fatalf("guardian metadata = %#v, want system_managed", guardianProfile.Metadata)
	}
	if guardianBinding.Enabled != nil && !*guardianBinding.Enabled {
		t.Fatalf("guardian binding disabled: %#v", guardianBinding)
	}
	if guardianBinding.Model != "" {
		t.Fatalf("guardian default model = %q, want session default", guardianBinding.Model)
	}
}

func TestGuardianProfileStatusIgnoresLegacyDisabledACPBinding(t *testing.T) {
	workdir := t.TempDir()
	stack, err := NewLocalStack(Config{
		AppName:        "caelis",
		UserID:         "guardian-legacy-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "auto-review",
		Sandbox:        SandboxConfig{RequestedType: "host"},
		Model: ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("store.Load() error = %v", err)
	}
	disabled := false
	next, err := agentprofile.UpsertBinding(doc.AgentBindings, agentprofile.Binding{
		ProfileID: "guardian",
		Enabled:   &disabled,
		Target:    agentprofile.BindingTargetACP,
		ACPAgent:  "old-reviewer",
		Status:    agentprofile.BindingStatusStale,
		Warning:   "legacy disabled runtime",
	}, time.Now())
	if err != nil {
		t.Fatalf("UpsertBinding(guardian legacy) error = %v", err)
	}
	doc.AgentBindings = next
	if err := stack.store.Save(doc); err != nil {
		t.Fatalf("store.Save() error = %v", err)
	}

	status, err := stack.AgentProfiles().Status(context.Background())
	if err != nil {
		t.Fatalf("AgentProfiles.Status() error = %v", err)
	}
	guardian, ok := agentProfileSnapshotForToolTest(status.Profiles, "guardian")
	if !ok {
		t.Fatalf("guardian virtual profile missing from status: %#v", status.Profiles)
	}
	binding := agentprofile.NormalizeBinding(guardian.Binding)
	if binding.Enabled != nil && !*binding.Enabled {
		t.Fatalf("guardian binding = %#v, want enabled virtual status", binding)
	}
	if binding.Target == agentprofile.BindingTargetACP || binding.ACPAgent != "" {
		t.Fatalf("guardian binding = %#v, want session-default runtime", binding)
	}
	if binding.Status != agentprofile.BindingStatusOK || binding.Warning != "" {
		t.Fatalf("guardian binding status = %s warning %q, want ok without legacy warning", binding.Status, binding.Warning)
	}
}

func TestLocalStackIgnoresLegacyGuardianProfileFile(t *testing.T) {
	workdir := t.TempDir()
	storeDir := t.TempDir()
	agentsDir := filepath.Join(storeDir, "agents")
	if err := os.MkdirAll(agentsDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(agents) error = %v", err)
	}
	legacyGuardian := agentprofile.FormatMarkdown(agentprofile.Profile{
		ID:           "guardian",
		Name:         "Legacy Guardian",
		Description:  "Legacy file that should no longer materialize.",
		Instructions: "Do not load this as a SPAWN agent.",
	})
	if err := os.WriteFile(filepath.Join(agentsDir, "guardian.md"), []byte(legacyGuardian), 0o600); err != nil {
		t.Fatalf("WriteFile(guardian.md) error = %v", err)
	}
	stack, err := NewLocalStack(Config{
		AppName:        "caelis",
		UserID:         "guardian-file-test",
		StoreDir:       storeDir,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "auto-review",
		Sandbox:        SandboxConfig{RequestedType: "host"},
		Model: ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	if agentConfigSetHas(stack.runtime.Assembly.Agents, "guardian") {
		t.Fatalf("legacy guardian.md should not materialize as ACP agent: %#v", stack.runtime.Assembly.Agents)
	}
	status, err := stack.AgentProfiles().Status(context.Background())
	if err != nil {
		t.Fatalf("AgentProfiles.Status() error = %v", err)
	}
	count := 0
	for _, snapshot := range status.Profiles {
		if strings.EqualFold(snapshot.Profile.ID, "guardian") {
			count++
			if snapshot.Profile.Name != "Guardian" {
				t.Fatalf("guardian profile name = %q, want virtual Guardian", snapshot.Profile.Name)
			}
		}
	}
	if count != 1 {
		t.Fatalf("guardian profile count = %d, want exactly one virtual profile: %#v", count, status.Profiles)
	}
}

func TestAgentProfileBindModelUpdatesACPAgentArgs(t *testing.T) {
	workdir := t.TempDir()
	stack, err := NewLocalStack(Config{
		AppName:        "caelis",
		UserID:         "profile-bind-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "auto-review",
		Sandbox:        SandboxConfig{RequestedType: "host"},
		Model: ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	if _, err := stack.Connect(ModelConfig{
		Alias:    "review-model",
		Provider: "deepseek",
		API:      providers.APIDeepSeek,
		Model:    "deepseek-reasoner",
		TokenEnv: "DEEPSEEK_TEST_TOKEN",
	}); err != nil {
		t.Fatalf("Connect(review-model) error = %v", err)
	}
	if _, err := stack.AgentProfiles().Bind(context.Background(), AgentProfileBindingConfig{
		ProfileID: "reviewer",
		Target:    agentprofile.BindingTargetBuiltIn,
		Model:     "review-model",
	}); err != nil {
		t.Fatalf("AgentProfiles.Bind(reviewer) error = %v", err)
	}
	reviewer, ok := agentConfigForToolTest(stack.runtime.Assembly.Agents, "reviewer")
	if !ok {
		t.Fatalf("reviewer profile agent missing from assembly: %#v", stack.runtime.Assembly.Agents)
	}
	for _, want := range []string{"-model-alias", "review-model", "-provider", "deepseek", "-model", "deepseek-reasoner", "-system-prompt"} {
		if !slices.Contains(reviewer.Args, want) {
			t.Fatalf("reviewer args = %#v, missing %q", reviewer.Args, want)
		}
	}
	if !slices.Contains(reviewer.Args, "-system-prompt") || !profileSystemPromptArgContains(reviewer.Args, "code review subagent") {
		t.Fatalf("reviewer args = %#v, want reviewer system prompt", reviewer.Args)
	}
	if reviewer.Env["SDK_ACP_ENABLE_SPAWN"] != "0" || reviewer.Env["SDK_ACP_CHILD_NO_SPAWN"] != "1" {
		t.Fatalf("reviewer env = %#v, want SPAWN disabled", reviewer.Env)
	}
}

func TestAgentProfileStaleModelBindingDoesNotMaterializeDefaultAgent(t *testing.T) {
	ctx := context.Background()
	stack, session := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
	if _, err := stack.Connect(ModelConfig{
		Alias:    "review-model",
		Provider: "ollama",
		Model:    "llama3:review",
	}); err != nil {
		t.Fatalf("Connect(review-model) error = %v", err)
	}
	if _, err := stack.AgentProfiles().Bind(ctx, AgentProfileBindingConfig{
		ProfileID: "reviewer",
		Target:    agentprofile.BindingTargetBuiltIn,
		Model:     "review-model",
	}); err != nil {
		t.Fatalf("AgentProfiles.Bind(reviewer model) error = %v", err)
	}
	if !agentConfigSetHas(stack.runtime.Assembly.Agents, "reviewer") {
		t.Fatalf("reviewer profile agent missing after model bind: %#v", stack.runtime.Assembly.Agents)
	}
	if err := stack.DeleteModel(ctx, session.SessionRef, "review-model"); err != nil {
		t.Fatalf("DeleteModel(review-model) error = %v", err)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("store.Load() error = %v", err)
	}
	if err := stack.setConfiguredAgents(doc.Agents); err != nil {
		t.Fatalf("setConfiguredAgents(after stale model) error = %v", err)
	}
	if agentConfigSetHas(stack.runtime.Assembly.Agents, "reviewer") {
		t.Fatalf("reviewer profile agent materialized with stale model binding: %#v", stack.runtime.Assembly.Agents)
	}
	status, err := stack.AgentProfiles().Status(ctx)
	if err != nil {
		t.Fatalf("AgentProfiles.Status() error = %v", err)
	}
	reviewer, ok := agentProfileSnapshotForToolTest(status.Profiles, "reviewer")
	if !ok {
		t.Fatalf("reviewer profile missing from status: %#v", status.Profiles)
	}
	binding := agentprofile.NormalizeBinding(reviewer.Binding)
	if binding.Status != agentprofile.BindingStatusStale {
		t.Fatalf("reviewer binding status = %q, want stale: %#v", binding.Status, binding)
	}
}

func TestAgentProfileAssemblyReturnsProfileDirError(t *testing.T) {
	stack, _ := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
	agentsDir := filepath.Join(stack.storeDir, agentprofile.DefaultAgentsDirName)
	if err := os.RemoveAll(agentsDir); err != nil {
		t.Fatalf("RemoveAll(%s) error = %v", agentsDir, err)
	}
	if err := os.WriteFile(agentsDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", agentsDir, err)
	}

	err := stack.setConfiguredAgents(nil)
	if err == nil || !strings.Contains(err.Error(), "agent profiles") {
		t.Fatalf("setConfiguredAgents() error = %v, want agent profile directory error", err)
	}
}

func TestAgentProfileAssemblyReturnsConfigStoreLoadError(t *testing.T) {
	stack, _ := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
	poisonConfigStorePath(t, stack)

	err := stack.setConfiguredAgents(nil)
	if err == nil || !strings.Contains(err.Error(), "agent profile bindings") {
		t.Fatalf("setConfiguredAgents() error = %v, want agent profile binding store error", err)
	}
}

func TestAgentProfileAssemblyReturnsInvalidBindingTargetError(t *testing.T) {
	stack, _ := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("store.Load() error = %v", err)
	}
	doc.AgentBindings = agentprofile.BindingSet{Bindings: []agentprofile.Binding{{
		ProfileID: "reviewer",
		Target:    agentprofile.BindingTargetKind("unknown_target"),
		Enabled:   boolPtr(true),
	}}}
	if err := stack.store.Save(doc); err != nil {
		t.Fatalf("store.Save() error = %v", err)
	}

	err = stack.setConfiguredAgents(doc.Agents)
	if err == nil || !strings.Contains(err.Error(), "unsupported target") {
		t.Fatalf("setConfiguredAgents() error = %v, want invalid profile binding target error", err)
	}
}

func TestAgentProfileBindGuardianModelDoesNotMaterializeACPAgent(t *testing.T) {
	workdir := t.TempDir()
	stack, err := NewLocalStack(Config{
		AppName:        "caelis",
		UserID:         "guardian-bind-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "auto-review",
		Sandbox:        SandboxConfig{RequestedType: "host"},
		Model: ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	if _, err := stack.Connect(ModelConfig{
		Alias:    "guardian-model",
		Provider: "ollama",
		Model:    "llama3:guardian",
	}); err != nil {
		t.Fatalf("Connect(guardian-model) error = %v", err)
	}
	status, err := stack.AgentProfiles().Bind(context.Background(), AgentProfileBindingConfig{
		ProfileID:       "guardian",
		Target:          agentprofile.BindingTargetBuiltIn,
		Model:           "guardian-model",
		ReasoningEffort: "",
	})
	if err != nil {
		t.Fatalf("AgentProfiles.Bind(guardian model) error = %v", err)
	}
	if agentConfigSetHas(stack.runtime.Assembly.Agents, "guardian") {
		t.Fatalf("guardian should not materialize as a SPAWN ACP agent: %#v", stack.runtime.Assembly.Agents)
	}
	guardian, ok := agentProfileSnapshotForToolTest(status.Profiles, "guardian")
	if !ok {
		t.Fatalf("guardian virtual profile missing from status: %#v", status.Profiles)
	}
	binding := agentprofile.NormalizeBinding(guardian.Binding)
	if binding.Target != agentprofile.BindingTargetBuiltIn || binding.Model != "guardian-model" {
		t.Fatalf("guardian binding = %#v, want built_in guardian-model", binding)
	}
	_, err = stack.AgentProfiles().Bind(context.Background(), AgentProfileBindingConfig{
		ProfileID: "guardian",
		Target:    agentprofile.BindingTargetACP,
		ACPAgent:  "reviewer",
	})
	if err == nil || !strings.Contains(err.Error(), "guardian cannot bind to an external ACP agent") {
		t.Fatalf("AgentProfiles.Bind(guardian acp) error = %v, want guardian ACP rejection", err)
	}
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

func spawnToolRequiresAgent(tools []tool.Tool) bool {
	for _, tool := range tools {
		if tool == nil || !strings.EqualFold(strings.TrimSpace(tool.Definition().Name), spawn.ToolName) {
			continue
		}
		required, _ := tool.Definition().InputSchema["required"].([]string)
		for _, item := range required {
			if strings.EqualFold(strings.TrimSpace(item), "agent") {
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

func agentProfileSnapshotForToolTest(profiles []agentprofile.Snapshot, id string) (agentprofile.Snapshot, bool) {
	for _, profile := range profiles {
		if strings.EqualFold(strings.TrimSpace(profile.Profile.ID), strings.TrimSpace(id)) {
			return profile, true
		}
	}
	return agentprofile.Snapshot{}, false
}

func agentConfigForToolTest(agents []assembly.AgentConfig, name string) (assembly.AgentConfig, bool) {
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.Name), strings.TrimSpace(name)) {
			return agent, true
		}
	}
	return assembly.AgentConfig{}, false
}

func storedAgentConfigForToolTest(agents []AgentConfig, name string) (AgentConfig, bool) {
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.Name), strings.TrimSpace(name)) {
			return agent, true
		}
	}
	return AgentConfig{}, false
}

func acpAgentAddOptionForToolTest(options []ACPAgentAddOption, value string) (ACPAgentAddOption, bool) {
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option.Value), strings.TrimSpace(value)) {
			return option, true
		}
	}
	return ACPAgentAddOption{}, false
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

func assertAgentArgsContain(t *testing.T, agents []assembly.AgentConfig, name string, values ...string) {
	t.Helper()
	agent, ok := agentConfigForToolTest(agents, name)
	if !ok {
		t.Fatalf("%s agent missing from assembly: %#v", name, agents)
	}
	for _, want := range values {
		if !slices.Contains(agent.Args, want) {
			t.Fatalf("%s args = %#v, missing %q", name, agent.Args, want)
		}
	}
}

func profileSystemPromptArgContains(args []string, want string) bool {
	for i, arg := range args {
		if arg != "-system-prompt" || i+1 >= len(args) {
			continue
		}
		return strings.Contains(args[i+1], want)
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
	commandName := testenv.CommandScriptName(name)
	path := filepath.Join(dir, commandName)
	if runtime.GOOS == "windows" && commandName != name {
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
