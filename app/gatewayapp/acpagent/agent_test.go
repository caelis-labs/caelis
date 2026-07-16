package acpagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	controldelegation "github.com/caelis-labs/caelis/control/delegation"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/protocol/acp"
)

func TestNewFromStackRoutesStatusSlashThroughSharedPromptRouter(t *testing.T) {
	workdir := t.TempDir()
	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acpagent-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "auto-review",
		SkillDirs:    []string{t.TempDir()},
		Sandbox: gatewayapp.SandboxConfig{
			RequestedType: "host",
		},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	agent, err := NewFromStack(stack)
	if err != nil {
		t.Fatalf("NewFromStack() error = %v", err)
	}
	session, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: workdir})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingCallbacks{}
	resp, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: session.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/status"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/status) error = %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	if got := cb.firstAgentMessage(); !strings.Contains(got, "Model:") || !strings.Contains(got, "Session:") {
		t.Fatalf("agent message = %q, want status output", got)
	}
}

func TestNewFromStackStatusSlashUsesClientWorkspaceSession(t *testing.T) {
	ctx := context.Background()
	stackWorkspace := t.TempDir()
	clientWorkspace := t.TempDir()
	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acpagent-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: stackWorkspace,
		WorkspaceCWD: stackWorkspace,
		ApprovalMode: "auto-review",
		SkillDirs:    []string{t.TempDir()},
		Sandbox: gatewayapp.SandboxConfig{
			RequestedType: "host",
		},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	agent, err := NewFromStack(stack)
	if err != nil {
		t.Fatalf("NewFromStack() error = %v", err)
	}
	session, err := agent.NewSession(ctx, acp.NewSessionRequest{CWD: clientWorkspace})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingCallbacks{}
	resp, err := agent.Prompt(ctx, acp.PromptRequest{
		SessionID: session.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/status"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/status) error = %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	message := cb.firstAgentMessage()
	clientWorkspaceDisplay := controladapter.FormatWorkspacePathForDisplay(clientWorkspace)
	if !strings.Contains(message, "Workspace: "+clientWorkspaceDisplay) {
		t.Fatalf("status output = %q, want client workspace %q", message, clientWorkspaceDisplay)
	}
	stackWorkspaceDisplay := controladapter.FormatWorkspacePathForDisplay(stackWorkspace)
	if strings.Contains(message, "Workspace: "+stackWorkspaceDisplay) {
		t.Fatalf("status output = %q, should not use stack workspace %q", message, stackWorkspaceDisplay)
	}
}

func TestNewFromStackSetConfigOptionUsesNewSessionCWDWorkspace(t *testing.T) {
	ctx := context.Background()
	stackWorkspace := t.TempDir()
	clientWorkspace := t.TempDir()
	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acpagent-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: stackWorkspace,
		WorkspaceCWD: stackWorkspace,
		ApprovalMode: "auto-review",
		SkillDirs:    []string{t.TempDir()},
		Sandbox: gatewayapp.SandboxConfig{
			RequestedType: "host",
		},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	agent, err := NewFromStack(stack)
	if err != nil {
		t.Fatalf("NewFromStack() error = %v", err)
	}
	sessionResp, err := agent.NewSession(ctx, acp.NewSessionRequest{CWD: clientWorkspace})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := agent.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		SessionID: sessionResp.SessionID,
		ConfigID:  "mode",
		Value:     "manual",
	}); err != nil {
		t.Fatalf("SetSessionConfigOption(mode) error = %v", err)
	}
	if _, err := agent.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		SessionID: sessionResp.SessionID,
		ConfigID:  "model",
		Value:     requiredConfigOptionString(t, sessionResp.ConfigOptions, "model"),
	}); err != nil {
		t.Fatalf("SetSessionConfigOption(model) error = %v", err)
	}
	if value, ok := configOptionString(sessionResp.ConfigOptions, "reasoning_effort"); ok {
		if _, err := agent.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
			SessionID: sessionResp.SessionID,
			ConfigID:  "reasoning_effort",
			Value:     value,
		}); err != nil {
			t.Fatalf("SetSessionConfigOption(reasoning_effort) error = %v", err)
		}
	}
	state, err := stack.SessionRuntimeState(ctx, session.SessionRef{
		AppName:      stack.AppName,
		UserID:       stack.UserID,
		SessionID:    sessionResp.SessionID,
		WorkspaceKey: clientWorkspace,
	})
	if err != nil {
		t.Fatalf("SessionRuntimeState(client workspace) error = %v", err)
	}
	if state.SessionMode != "manual" {
		t.Fatalf("client workspace session mode = %q, want manual", state.SessionMode)
	}
}

func requiredConfigOptionString(t *testing.T, options []acp.SessionConfigOption, id string) string {
	t.Helper()
	value, ok := configOptionString(options, id)
	if !ok {
		t.Fatalf("config option %q not found in %#v", id, options)
	}
	return value
}

func configOptionString(options []acp.SessionConfigOption, id string) (string, bool) {
	for _, option := range options {
		if strings.TrimSpace(option.ID) != id {
			continue
		}
		value, ok := option.CurrentValue.(string)
		return strings.TrimSpace(value), ok && strings.TrimSpace(value) != ""
	}
	return "", false
}

func TestACPPromptCommandNamesExposeOnlyBoundProfilesAndHideAgentCatalog(t *testing.T) {
	workdir := t.TempDir()
	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acpagent-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		SkillDirs:    []string{t.TempDir()},
		Sandbox: gatewayapp.SandboxConfig{
			RequestedType: "host",
		},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
		Assembly: assembly.ResolvedAssembly{Agents: []assembly.AgentConfig{
			{Name: "helper", Description: "registered ACP helper", Command: "go"},
			{Name: "status", Description: "reserved collision", Command: "go"},
		}},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	if len(stack.ListACPAgents()) == 0 {
		t.Fatal("test setup did not materialize the registered helper Agent")
	}
	if _, err := stack.Connect(gatewayapp.ModelConfig{
		Provider: "ollama",
		API:      providers.APIOllama,
		Model:    "bound-orbit",
	}); err != nil {
		t.Fatalf("Connect(bound Orbit model) error = %v", err)
	}
	delegationStatus, err := stack.Delegation().DelegationStatus(context.Background())
	if err != nil {
		t.Fatalf("DelegationStatus() error = %v", err)
	}
	commands := acpPromptCommandNames(delegationStatus)
	for _, profile := range []string{"breeze", "orbit", "zenith"} {
		if containsCommand(commands, profile) {
			t.Fatalf("acpPromptCommandNames() = %#v, should hide unbound %q", commands, profile)
		}
	}
	if len(delegationStatus.Targets) == 0 {
		t.Fatal("test setup has no delegation targets")
	}
	delegationStatus, err = stack.Delegation().BindDelegation(context.Background(), controldelegation.BindRequest{
		Profile: controldelegation.ProfileOrbit,
		AgentID: delegationStatus.Targets[0].Agent.ID,
	})
	if err != nil {
		t.Fatalf("BindDelegation(Orbit) error = %v", err)
	}
	commands = acpPromptCommandNames(delegationStatus)
	if !containsCommand(commands, "orbit") {
		t.Fatalf("acpPromptCommandNames() = %#v, want bound Orbit", commands)
	}
	for _, profile := range []string{"breeze", "zenith"} {
		if containsCommand(commands, profile) {
			t.Fatalf("acpPromptCommandNames() = %#v, should hide unbound %q", commands, profile)
		}
	}
	for _, hidden := range []string{"helper", "reviewer", "self", "lead"} {
		if containsCommand(commands, hidden) {
			t.Fatalf("acpPromptCommandNames() = %#v, should not contain %q", commands, hidden)
		}
	}
	if countCommand(commands, "status") != 1 {
		t.Fatalf("acpPromptCommandNames() = %#v, want one core status command", commands)
	}
	if !acpAgentCommandAllowed("breeze") {
		t.Fatal("acpAgentCommandAllowed(breeze) = false, want true")
	}
	for _, hidden := range []string{"helper", "reviewer", "self", "status", "lead"} {
		if acpAgentCommandAllowed(hidden) {
			t.Fatalf("acpAgentCommandAllowed(%q) = true, want false", hidden)
		}
	}
}

type recordingCallbacks struct {
	notifications []acp.SessionNotification
}

func (c *recordingCallbacks) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	c.notifications = append(c.notifications, notification)
	return nil
}

func (c *recordingCallbacks) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{}, nil
}

func (c *recordingCallbacks) firstAgentMessage() string {
	for _, notification := range c.notifications {
		chunk, ok := notification.Update.(acp.ContentChunk)
		if !ok || chunk.SessionUpdate != acp.UpdateAgentMessage {
			continue
		}
		content, ok := chunk.Content.(acp.TextContent)
		if !ok {
			continue
		}
		if text := strings.TrimSpace(content.Text); text != "" {
			return text
		}
	}
	return ""
}

func containsCommand(commands []string, name string) bool {
	return countCommand(commands, name) > 0
}

func countCommand(commands []string, name string) int {
	count := 0
	for _, command := range commands {
		if strings.EqualFold(strings.TrimSpace(command), name) {
			count++
		}
	}
	return count
}
