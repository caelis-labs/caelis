package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/internal/acpagentenv"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp"
)

func TestLocalStackInjectsSpawnForOrdinaryAgentsAndHidesSystemScenes(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{
		Agents: []assembly.AgentConfig{{
			Name: "helper", Description: "bounded ACP helper", Command: "go",
			Args: []string{"run", "./internal/acpe2eagent"}, WorkDir: repoRootForGatewayAppTest(t),
		}},
	})
	resolved, err := stack.currentGateway().Resolver().ResolveTurn(ctx, gateway.TurnIntent{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn() error = %v", err)
	}
	if !toolSetHas(resolved.RunRequest.AgentSpec.Tools, spawn.ToolName) || !toolSetHas(resolved.RunRequest.AgentSpec.Tools, task.ToolName) {
		t.Fatalf("tools = %#v, want SPAWN and task tools", resolved.RunRequest.AgentSpec.Tools)
	}
	for _, want := range []string{"self", "helper"} {
		if !spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, want) {
			t.Fatalf("SPAWN Agent enum missing %q", want)
		}
	}
	for _, hidden := range []string{ReviewerAgentID, guardianSceneID} {
		if spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, hidden) {
			t.Fatalf("SPAWN Agent enum exposes system scene %q", hidden)
		}
	}
	if spawnToolRequiresAgent(resolved.RunRequest.AgentSpec.Tools) {
		t.Fatal("SPAWN Agent is required despite self default")
	}
}

func TestACPSurfaceAvailableCommandsExposeAgentSlashAndHideSystemScenes(t *testing.T) {
	stack, activeSession := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{
		Agents: []assembly.AgentConfig{{
			Name: "helper", Description: "bounded ACP helper", Command: "go",
			Args: []string{"run", "./internal/acpe2eagent"}, WorkDir: repoRootForGatewayAppTest(t),
		}},
	})
	commands, err := stack.ACPSurface(nil, false, nil).AvailableCommands(context.Background(), activeSession.SessionID)
	if err != nil {
		t.Fatalf("AvailableCommands() error = %v", err)
	}
	if helper := acpCommandForToolTest(commands, "helper"); helper == nil || helper.Description != "bounded ACP helper" {
		t.Fatalf("AvailableCommands() = %#v, want /helper", commands)
	}
	if acpCommandForToolTest(commands, "lead") == nil {
		t.Fatalf("AvailableCommands() = %#v, want /lead", commands)
	}
	for _, hidden := range []string{ReviewerAgentID, guardianSceneID, "agent", "subagent"} {
		if acpCommandForToolTest(commands, hidden) != nil {
			t.Fatalf("AvailableCommands() exposes removed or system command %q: %#v", hidden, commands)
		}
	}
}

func TestLocalStackFailsOnInvalidSelfAgentEnv(t *testing.T) {
	t.Setenv(acpagentenv.EnvCommand, "/opt/acp-helper")
	t.Setenv(acpagentenv.EnvArgsJSON, `{"bad":true}`)
	workdir := t.TempDir()
	_, err := NewLocalStack(Config{
		AppName: "caelis", UserID: "invalid-self-agent-env-test", StoreDir: t.TempDir(),
		WorkspaceKey: workdir, WorkspaceCWD: workdir, SkillDirs: []string{t.TempDir()},
		Sandbox: SandboxConfig{RequestedType: "host"},
		Model:   ModelConfig{Provider: "ollama", Model: "llama3"},
	})
	if err == nil || !strings.Contains(err.Error(), acpagentenv.EnvArgsJSON) {
		t.Fatalf("NewLocalStack() error = %v, want self-Agent env parse error", err)
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

func newStackWithAssemblyForToolTest(t *testing.T, resolved assembly.ResolvedAssembly) (*Stack, session.Session) {
	t.Helper()
	stack := newStackForToolTest(t, resolved)
	activeSession, err := stack.StartSession(context.Background(), "", "surface-tool-test")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return stack, activeSession
}

// Kept as a semantic alias for focused roster tests that predate fixed system
// scenes. There is no longer a switch that disables Guardian or Reviewer.
func newStackForToolTestWithoutProfiles(t *testing.T, resolved assembly.ResolvedAssembly) *Stack {
	t.Helper()
	return newStackForToolTest(t, resolved)
}

func newStackForToolTest(t *testing.T, resolved assembly.ResolvedAssembly) *Stack {
	t.Helper()
	workdir := t.TempDir()
	stack, err := NewLocalStack(Config{
		AppName: "caelis", UserID: "tool-test", StoreDir: t.TempDir(),
		WorkspaceKey: workdir, WorkspaceCWD: workdir, ApprovalMode: "auto-review",
		Assembly: resolved, SkillDirs: []string{t.TempDir()},
		Sandbox: SandboxConfig{RequestedType: "host"},
		Model:   ModelConfig{Provider: "ollama", Model: "llama3"},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	return stack
}

func toolSetHas(tools []tool.Tool, name string) bool {
	for _, candidate := range tools {
		if candidate != nil && strings.EqualFold(strings.TrimSpace(candidate.Definition().Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func spawnToolHasAgent(tools []tool.Tool, name string) bool {
	for _, candidate := range tools {
		if candidate == nil || !strings.EqualFold(strings.TrimSpace(candidate.Definition().Name), spawn.ToolName) {
			continue
		}
		properties, _ := candidate.Definition().InputSchema["properties"].(map[string]any)
		agentProperty, _ := properties["agent"].(map[string]any)
		values, _ := agentProperty["enum"].([]string)
		for _, value := range values {
			if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(name)) {
				return true
			}
		}
	}
	return false
}

func spawnToolRequiresAgent(tools []tool.Tool) bool {
	for _, candidate := range tools {
		if candidate == nil || !strings.EqualFold(strings.TrimSpace(candidate.Definition().Name), spawn.ToolName) {
			continue
		}
		required, _ := candidate.Definition().InputSchema["required"].([]string)
		for _, value := range required {
			if strings.EqualFold(strings.TrimSpace(value), "agent") {
				return true
			}
		}
	}
	return false
}

func agentConfigForToolTest(agents []assembly.AgentConfig, name string) (assembly.AgentConfig, bool) {
	for _, candidate := range agents {
		if strings.EqualFold(strings.TrimSpace(candidate.Name), strings.TrimSpace(name)) {
			return candidate, true
		}
	}
	return assembly.AgentConfig{}, false
}

func argValue(args []string, flag string) (string, bool) {
	for i, value := range args {
		if value == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
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
