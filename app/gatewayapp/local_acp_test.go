package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/sendmessage"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/control/agentbinding"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/acpagentenv"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestLocalStackInjectsOnlySelfUntilProfileIsBound(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{
		Agents: []assembly.AgentConfig{{
			Name: "helper", Description: "bounded ACP helper", Command: "go",
			Args: []string{"run", "./internal/acpe2eagent"}, WorkDir: repoRootForGatewayAppTest(t),
		}},
	})
	resolved, err := stack.composition.currentGateway().Resolver().ResolveTurn(ctx, kernel.TurnIntent{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn() error = %v", err)
	}
	if !toolSetHas(resolved.RunRequest.AgentSpec.Tools, spawn.ToolName) ||
		!toolSetHas(resolved.RunRequest.AgentSpec.Tools, task.ToolName) ||
		!toolSetHas(resolved.RunRequest.AgentSpec.Tools, sendmessage.ToolName) {
		t.Fatalf("tools = %#v, want explicit SPAWN, Task, and SendMessage capabilities", resolved.RunRequest.AgentSpec.Tools)
	}
	if !spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, "self") {
		t.Fatal("SPAWN Agent enum missing self")
	}
	for _, hidden := range []string{"breeze", "orbit", "zenith", "helper", string(agentbinding.HandleReviewer), guardianSceneID} {
		if spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, hidden) {
			t.Fatalf("SPAWN Agent enum exposes unbound or system Agent %q", hidden)
		}
	}
	if spawnToolRequiresAgent(resolved.RunRequest.AgentSpec.Tools) {
		t.Fatal("SPAWN Agent is required despite self default")
	}

	bindProfileToModelForToolTest(t, stack, agentbinding.HandleOrbit)
	resolved, err = stack.composition.currentGateway().Resolver().ResolveTurn(ctx, kernel.TurnIntent{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("ResolveTurn(bound Orbit) error = %v", err)
	}
	if !spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, "orbit") {
		t.Fatal("SPAWN Agent enum missing explicitly bound Orbit")
	}
	for _, hidden := range []string{"breeze", "zenith"} {
		if spawnToolHasAgent(resolved.RunRequest.AgentSpec.Tools, hidden) {
			t.Fatalf("SPAWN Agent enum exposes unbound profile %q", hidden)
		}
	}
}

func TestSpawnedSubagentSessionCannotReceiveNestedSpawn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack := newStackForToolTest(t, assembly.ResolvedAssembly{})
	child, err := stack.composition.sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: stack.composition.authorities.appName,
		UserID:  stack.composition.authorities.userID,
		Workspace: session.WorkspaceRef{
			Key: stack.composition.workspace.Key,
			CWD: stack.composition.workspace.CWD,
		},
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent:  sessionvisibility.SystemManagedAgentSubagent,
			sessionvisibility.MetadataSystemManagedParent: "parent-session",
			sessionvisibility.MetadataSystemManagedTask:   "spawn-task-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := stack.composition.currentGateway().Resolver().ResolveTurn(ctx, kernel.TurnIntent{SessionRef: child.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if toolSetHas(resolved.RunRequest.AgentSpec.Tools, spawn.ToolName) {
		t.Fatalf("spawned child tools = %#v, must not include nested Spawn", resolved.RunRequest.AgentSpec.Tools)
	}
	if !toolSetHas(resolved.RunRequest.AgentSpec.Tools, sendmessage.ToolName) {
		t.Fatalf("spawned child tools = %#v, want SendMessage", resolved.RunRequest.AgentSpec.Tools)
	}
	childPrompt := stringFromMap(resolved.RunRequest.AgentSpec.Metadata, "system_prompt")
	if strings.Contains(childPrompt, "## Delegation") {
		t.Fatalf("spawned child prompt retained nested delegation guidance:\n%s", childPrompt)
	}
	if !strings.Contains(childPrompt, "## Shared Workspace") {
		t.Fatalf("spawned child prompt missing shared workspace guidance:\n%s", childPrompt)
	}
	if strings.Index(childPrompt, "## Shared Workspace") > strings.Index(childPrompt, "</system_instructions>") {
		t.Fatalf("spawned child shared workspace guidance rendered outside system instructions:\n%s", childPrompt)
	}
}

func TestPresentationSourceAvailableCommandsExposeOnlyBoundProfilesAndHideRosterAgents(t *testing.T) {
	stack, activeSession := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{
		Agents: []assembly.AgentConfig{{
			Name: "helper", Description: "bounded ACP helper", Command: "go",
			Args: []string{"run", "./internal/acpe2eagent"}, WorkDir: repoRootForGatewayAppTest(t),
		}},
	})
	commands, err := stack.PresentationSource(nil, false, nil).AvailableCommands(context.Background(), activeSession.SessionID)
	if err != nil {
		t.Fatalf("AvailableCommands() error = %v", err)
	}
	for _, profile := range []string{"breeze", "orbit", "zenith"} {
		if acpCommandForToolTest(commands, profile) != nil {
			t.Fatalf("AvailableCommands() = %#v, should hide unbound /%s", commands, profile)
		}
	}
	bindProfileToModelForToolTest(t, stack, agentbinding.HandleOrbit)
	commands, err = stack.PresentationSource(nil, false, nil).AvailableCommands(context.Background(), activeSession.SessionID)
	if err != nil {
		t.Fatalf("AvailableCommands(bound Orbit) error = %v", err)
	}
	if acpCommandForToolTest(commands, "orbit") == nil {
		t.Fatalf("AvailableCommands() = %#v, want bound /orbit", commands)
	}
	for _, profile := range []string{"breeze", "zenith"} {
		if acpCommandForToolTest(commands, profile) != nil {
			t.Fatalf("AvailableCommands() = %#v, should hide unbound /%s", commands, profile)
		}
	}
	for _, hidden := range []string{"helper", "lead", string(agentbinding.HandleReviewer), guardianSceneID, "agent", "subagent"} {
		if acpCommandForToolTest(commands, hidden) != nil {
			t.Fatalf("AvailableCommands() exposes removed or system command %q: %#v", hidden, commands)
		}
	}
	if acpCommandForToolTest(commands, "model") != nil {
		t.Fatalf("AvailableCommands() exposes /model even though ACP has a dedicated model-selection channel: %#v", commands)
	}
	activeSession, err = stack.composition.sessions.BindController(context.Background(), session.BindControllerRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "helper", AgentName: "helper", EpochID: "epoch-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	commands, err = stack.PresentationSource(nil, false, nil).AvailableCommands(context.Background(), activeSession.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if acpCommandForToolTest(commands, "compact") != nil {
		t.Fatalf("AvailableCommands() = %#v, should hide /compact for external ACP controller", commands)
	}
}

func bindProfileToModelForToolTest(t *testing.T, stack *Stack, handle agentbinding.Handle) {
	t.Helper()
	modelProfile, err := stack.connectTestModel(ModelConfig{
		Provider: "ollama",
		API:      providers.APIOllama,
		Model:    "bound-" + string(handle),
	})
	if err != nil {
		t.Fatalf("Connect(bound %s model) error = %v", handle, err)
	}
	if _, err = stack.testAgentBindings().BindAgentBinding(context.Background(), agentbinding.Binding{
		Handle: handle, ProfileID: modelProfile.ID, Effort: modelProfile.Effort.DefaultEffort,
	}); err != nil {
		t.Fatalf("BindAgentBinding(%s) error = %v", handle, err)
	}
}

func TestACPProfileCommandDescriptionIncludesBoundModel(t *testing.T) {
	detail := availableProfileDescription(agentbinding.HandleStatus{
		Definition: agentbinding.Definition{Handle: agentbinding.HandleOrbit, Description: "General implementation."},
		Binding: agentbinding.Binding{
			Handle: agentbinding.HandleOrbit, ProfileID: "provider:sol", Effort: "high",
		},
		Profile: modelprofile.ModelProfile{ID: "provider:sol", DisplayName: "openai-codex/gpt-5.6-sol"},
	})
	for _, want := range []string{"General implementation", "openai-codex/gpt-5.6-sol", "[high]"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("availableProfileDescription() = %q, want %q", detail, want)
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
	})
	if err == nil || !strings.Contains(err.Error(), acpagentenv.EnvArgsJSON) {
		t.Fatalf("NewLocalStack() error = %v, want self-Agent env parse error", err)
	}
}

func acpCommandForToolTest(commands []appserver.PresentationCommand, name string) *appserver.PresentationCommand {
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
	activeSession, err := startGatewayAppTestSession(context.Background(), stack, "")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return stack, activeSession
}

// Kept as a semantic alias for focused catalog tests that need an initially
// empty ModelProfile set. Guardian and Reviewer are always available scenes.
func newStackForToolTestWithoutProfiles(t *testing.T, resolved assembly.ResolvedAssembly) *Stack {
	t.Helper()
	return newStackForToolTest(t, resolved)
}

func newStackForToolTest(t *testing.T, resolved assembly.ResolvedAssembly) *Stack {
	t.Helper()
	workdir := t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{
		AppName: "caelis", UserID: "tool-test", StoreDir: t.TempDir(),
		WorkspaceKey: workdir, WorkspaceCWD: workdir, ApprovalMode: "auto-review",
		Assembly: resolved, SkillDirs: []string{t.TempDir()},
		Sandbox: SandboxConfig{RequestedType: "host"},
		Model:   ModelConfig{Provider: "ollama", Model: "llama3"},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	// Close before t.TempDir cleanup. Deferred Runtime use-release can otherwise
	// schedule an idle Session writer that races Linux RemoveAll.
	t.Cleanup(func() { _ = stack.Close() })
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
