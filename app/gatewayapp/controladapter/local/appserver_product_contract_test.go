package local

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	controlclient "github.com/caelis-labs/caelis/control/client"
	controlstatus "github.com/caelis-labs/caelis/control/status"
)

func TestBoundAppServerProjectsACPControllerAndFailsClosedWithoutRemoteModes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	host, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName: "caelis-test", UserID: "local-user", StoreDir: filepath.Join(root, "store"),
		WorkspaceKey: "workspace", WorkspaceCWD: workspace, ApprovalMode: "default",
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	clients := bindAppServerTestClients(t, host)
	sessionID := createAppServerTestSession(t, clients, "create-acp-projection", "acp-projection", workspace)
	active, err := host.Sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: active.SessionRef,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "codex-controller", AgentName: "codex",
			Label: "Codex ACP", RemoteSessionID: "remote-1",
		},
	}); err != nil {
		t.Fatal(err)
	}

	statusBefore, err := clients.Status.SessionStatus(ctx, controlclient.StatusRequest{
		SessionID: sessionID, Surface: "test", IncludeDiagnostics: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if statusBefore.ModelStatus.Provider != "acp" || statusBefore.ModelStatus.Display != "codex" {
		t.Fatalf("ACP status model = %#v, want acp/codex", statusBefore.ModelStatus)
	}
	agentStatus, err := clients.Agents.AgentStatus(ctx, controlclient.AgentRequest{SessionID: sessionID, Surface: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if agentStatus.ControllerKind != string(session.ControllerKindACP) || agentStatus.ControllerLabel != "codex" {
		t.Fatalf("Agent status controller = %#v, want durable ACP controller", agentStatus)
	}

	if _, err := clients.Configuration.ConfigureSessionMode(ctx, controlclient.SessionModeRequest{
		SessionID: sessionID, Surface: "test", Cycle: true,
	}); err == nil || !strings.Contains(err.Error(), "remote ACP controller did not declare session modes") {
		t.Fatalf("cycle ACP mode error = %v, want missing remote modes", err)
	}
	statusAfter, err := clients.Status.SessionStatus(ctx, controlclient.StatusRequest{
		SessionID: sessionID, Surface: "test", IncludeDiagnostics: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if statusAfter.Session.SessionMode != statusBefore.Session.SessionMode {
		t.Fatalf("failed ACP cycle changed local mode from %q to %q", statusBefore.Session.SessionMode, statusAfter.Session.SessionMode)
	}
}

func TestBoundAppServerProjectsSessionUsageAndParticipants(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	host, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName: "caelis-test", UserID: "local-user", StoreDir: filepath.Join(root, "store"),
		WorkspaceKey: "workspace", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	clients := bindAppServerTestClients(t, host)
	sessionID := createAppServerTestSession(t, clients, "create-projection", "projection", workspace)
	active, err := host.Sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	assistant := model.NewTextMessage(model.RoleAssistant, "answer")
	if _, err := host.Sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: active.SessionRef,
		Event: &session.Event{
			Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Message: &assistant,
			Invocation: &session.EventInvocation{Provider: "ollama", Model: "llama3"},
			Meta: map[string]any{
				"provider": "ollama", "model": "llama3", "prompt_tokens": 12600,
				"cached_input_tokens": 9000, "completion_tokens": 200,
				"reasoning_tokens": 50, "total_tokens": 12800,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	for _, binding := range []session.ParticipantBinding{
		{
			ID: "side-1", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar,
			AgentName: "codex", Label: "@side", SessionID: "child-side", Source: "test",
		},
		{
			ID: "task-1", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated,
			AgentName: "worker", Label: "@worker", SessionID: "child-worker", Source: "test", DelegationID: "task-1",
		},
	} {
		if _, err := host.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
			SessionRef: active.SessionRef, Binding: binding,
		}); err != nil {
			t.Fatal(err)
		}
	}

	status, err := clients.Status.SessionStatus(ctx, controlclient.StatusRequest{
		SessionID: sessionID, Surface: "test", IncludeDiagnostics: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantUsage := controlstatus.UsageSnapshot{
		PromptTokens: 12600, CachedInputTokens: 9000, CompletionTokens: 200,
		ReasoningTokens: 50, TotalTokens: 12800,
	}
	if status.Usage.SessionUsageTotal != wantUsage {
		t.Fatalf("Session usage = %#v, want %#v", status.Usage.SessionUsageTotal, wantUsage)
	}
	wantByModel := []controlstatus.ModelUsageSnapshot{{Provider: "ollama", Model: "llama3", Usage: wantUsage}}
	if !reflect.DeepEqual(status.Usage.SessionUsageByModel, wantByModel) {
		t.Fatalf("Session usage by model = %#v, want %#v", status.Usage.SessionUsageByModel, wantByModel)
	}

	agentStatus, err := clients.Agents.AgentStatus(ctx, controlclient.AgentRequest{SessionID: sessionID, Surface: "test"})
	if err != nil {
		t.Fatal(err)
	}
	wantDirect := []controlclient.AgentParticipantSnapshot{{
		ID: "side-1", Label: "@side", AgentName: "codex", Kind: string(session.ParticipantKindACP),
		Role: string(session.ParticipantRoleSidecar), Source: "test", SessionID: "child-side",
	}}
	wantDelegated := []controlclient.AgentParticipantSnapshot{{
		ID: "task-1", Label: "@worker", AgentName: "worker", Kind: string(session.ParticipantKindSubagent),
		Role: string(session.ParticipantRoleDelegated), Source: "test", SessionID: "child-worker",
	}}
	if !reflect.DeepEqual(agentStatus.Participants, wantDirect) || !reflect.DeepEqual(agentStatus.DelegatedParticipants, wantDelegated) {
		t.Fatalf("Agent participants = direct %#v delegated %#v, want direct %#v delegated %#v",
			agentStatus.Participants, agentStatus.DelegatedParticipants, wantDirect, wantDelegated)
	}
}

func TestBoundAppServerKeepsSessionSkillSnapshotUntilHostRebuild(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	skillRoot := filepath.Join(workspace, ".agents", "skills")
	writeAppServerTestSkill(t, skillRoot, "initial", "Initial skill.")
	config := gatewayapp.Config{
		AppName: "caelis-test", UserID: "local-user", StoreDir: filepath.Join(root, "store"),
		WorkspaceKey: "workspace", WorkspaceCWD: workspace,
		SkillDirs: []string{skillRoot}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	}
	host, err := gatewayapp.NewLocalStack(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if host != nil {
			_ = host.Close()
		}
	})
	clients := bindAppServerTestClients(t, host)
	sessionID := createAppServerTestSession(t, clients, "create-skill-snapshot", "skill-snapshot", workspace)
	lease, err := host.AcquireControlRuntime(ctx, controlclient.Principal{ID: "local-user"}, sessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(ctx); err != nil {
		t.Fatal(err)
	}

	initial, err := clients.Completion.CompleteSkill(ctx, controlclient.CompletionRequest{
		SessionID: sessionID, Surface: "test", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !completionCandidatesContain(initial, "initial") {
		t.Fatalf("initial completions = %#v, want initial skill", initial)
	}
	writeAppServerTestSkill(t, skillRoot, "late", "Late skill.")
	fixed, err := clients.Completion.CompleteSkill(ctx, controlclient.CompletionRequest{
		SessionID: sessionID, Surface: "test", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if completionCandidatesContain(fixed, "late") {
		t.Fatalf("fixed Session completions = %#v, should not include late skill", fixed)
	}

	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	host = nil
	rebuilt, err := gatewayapp.NewLocalStack(config)
	if err != nil {
		t.Fatal(err)
	}
	host = rebuilt
	rebuiltClients := bindAppServerTestClients(t, rebuilt)
	refreshed, err := rebuiltClients.Completion.CompleteSkill(ctx, controlclient.CompletionRequest{
		SessionID: sessionID, Surface: "test", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !completionCandidatesContain(refreshed, "initial") || !completionCandidatesContain(refreshed, "late") {
		t.Fatalf("rebuilt Host completions = %#v, want initial and late skills", refreshed)
	}
}

func bindAppServerTestClients(t *testing.T, host *gatewayapp.Stack) controlclient.AppServerClients {
	t.Helper()
	server, err := NewAppServer(host)
	if err != nil {
		t.Fatal(err)
	}
	clients, _, err := server.Bind(controlclient.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	return clients
}

func createAppServerTestSession(
	t *testing.T,
	clients controlclient.AppServerClients,
	operationID string,
	preferredSessionID string,
	workspace string,
) string {
	t.Helper()
	created, err := clients.Sessions.CreateSession(context.Background(), controlclient.CreateSessionRequest{
		WriteBase: controlclient.WriteBase{OperationID: operationID}, PreferredSessionID: preferredSessionID,
		WorkspaceKey: "workspace", CWD: workspace, Title: preferredSessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(created.SessionID) == "" {
		t.Fatalf("CreateSession(%q) returned no Session ID", preferredSessionID)
	}
	return created.SessionID
}

func writeAppServerTestSkill(t *testing.T, root string, name string, description string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("---\nname: " + name + "\ndescription: " + description + "\n---\n# " + name + "\n")
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func completionCandidatesContain(candidates []controlclient.CompletionCandidate, value string) bool {
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.Value) == value {
			return true
		}
	}
	return false
}
