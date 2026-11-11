package gatewayapp

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func TestSessionInterruptIdleRetiresDelegationAndPendingMailAcrossRestart(t *testing.T) {
	provider := newHostedChildInputTestProvider(t, false)
	host := newHostedChildInputTestStack(t, provider)
	parent, child, sender := newHostedChildInputTestTopology(t, host, "interrupt-idle")
	putInterruptOrphanTask(t, host, parent, child, "task-interrupt-idle", "orbit")
	service := host.CollaborationService()
	backend := &collaborationBackend{sessions: host.composition.sessions, tasks: host.composition.authorities.taskStore, router: &hostedChildInputRouter{runtimes: host.sessionRuntimes}}
	// Gate parent Turn admission while mail is queued. Whether the dispatcher
	// has claimed it or not, cancellation must revoke its source authority.
	parentRuntime := activateSessionRuntime(t, host, parent.SessionID)
	unblock, err := parentRuntime.instance.currentGateway().BlockSessionTurnAdmission(parent.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	defer unblock()
	mail, err := service.Send(t.Context(), collaboration.Identity{Session: parent.SessionID, Member: "orbit"}, "parent", "cancelled-mail-evidence", "")
	if err != nil {
		t.Fatal(err)
	}
	result := interruptTestSession(t, host, parent.SessionRef, "interrupt-idle")
	if result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("interrupt = %#v", result)
	}
	unblock()
	if err := sender.SendAgentInput(t.Context(), agent.AgentInput{Target: "parent", Input: "cancelled-direct-input"}); !errorcode.Is(err, errorcode.PermissionDenied) {
		t.Fatalf("old sender = %v, want permission denied", err)
	}
	if _, err := service.Send(t.Context(), collaboration.Identity{Session: parent.SessionID, Member: "orbit"}, "parent", "late mail", ""); err == nil {
		t.Fatal("cancelled child retained mailbox authority")
	}
	if err := backend.Deliver(t.Context(), parent.SessionID, []collaboration.Message{mail}); err == nil {
		t.Fatal("previously claimed mail retained Runtime admission")
	}
	entry, err := host.composition.authorities.taskStore.Get(t.Context(), "task-interrupt-idle")
	if err != nil || entry.Running || entry.State != task.StateInterrupted {
		t.Fatalf("orphan Task = %#v, %v", entry, err)
	}
	if _, err := host.composition.sessions.Session(t.Context(), child.SessionRef); err != nil {
		t.Fatalf("child history unavailable: %v", err)
	}
	storeDir, workspace := host.composition.authorities.storeDir, host.composition.workspace
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newGatewayAppTestStack(t, Config{StoreDir: storeDir, WorkspaceKey: workspace.Key, WorkspaceCWD: workspace.CWD, ResolveProviderHTTPClient: func(context.Context, ModelConfig) (*http.Client, error) { return provider.Client(), nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if _, err := runHeadlessOnceForGatewayAppTest(t.Context(), reopened, parent, parent.SessionID, "explicit new work", headless.Options{}); err != nil {
		t.Fatal(err)
	}
	payload := string(provider.LastMessages())
	if strings.Contains(payload, "cancelled-mail-evidence") || strings.Contains(payload, "cancelled-direct-input") || !strings.Contains(payload, "explicit new work") {
		t.Fatalf("rebuilt model context = %s", payload)
	}
	if provider.CallCount() != 1 {
		t.Fatalf("provider calls = %d, want explicit new work only", provider.CallCount())
	}
}

func TestSessionInterruptCancelsActiveParentAndScopesManagedDescendants(t *testing.T) {
	provider := newHostedChildInputTestProvider(t, true)
	host := newHostedChildInputTestStack(t, provider)
	parent, child, _ := newHostedChildInputTestTopology(t, host, "interrupt-tree")
	putInterruptOrphanTask(t, host, parent, child, "task-interrupt-tree", "orbit")
	grandchild, err := host.composition.sessions.StartSession(t.Context(), session.StartSessionRequest{AppName: host.composition.authorities.appName, UserID: host.composition.authorities.userID, Workspace: session.WorkspaceRef{Key: child.WorkspaceKey, CWD: child.CWD}, Metadata: map[string]any{
		sessionvisibility.MetadataSystemManagedAgent: sessionvisibility.SystemManagedAgentSubagent, sessionvisibility.MetadataSystemManagedParent: child.SessionID, sessionvisibility.MetadataSystemManagedTask: "task-grandchild",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.composition.sessions.PutParticipant(t.Context(), session.PutParticipantRequest{SessionRef: child.SessionRef, Binding: session.ParticipantBinding{ID: "grandchild", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated, AgentName: "orbit", Label: "@nested", SessionID: grandchild.SessionID, DelegationID: "task-grandchild"}}); err != nil {
		t.Fatal(err)
	}
	putInterruptOrphanTask(t, host, child, grandchild, "task-grandchild", "nested")
	unrelated, unrelatedChild, _ := newHostedChildInputTestTopology(t, host, "interrupt-unrelated")
	putInterruptOrphanTask(t, host, unrelated, unrelatedChild, "task-interrupt-unrelated", "orbit")
	// A binding whose endpoint happens to be a local Session does not grant
	// ownership unless that Session confirms the same parent and Task.
	if _, err := host.composition.sessions.PutParticipant(t.Context(), session.PutParticipantRequest{SessionRef: parent.SessionRef, Binding: session.ParticipantBinding{ID: "foreign", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated, AgentName: "orbit", Label: "@foreign", SessionID: unrelated.SessionID, DelegationID: "task-foreign"}}); err != nil {
		t.Fatal(err)
	}
	putInterruptOrphanTask(t, host, parent, unrelated, "task-foreign", "foreign")
	done := make(chan error, 1)
	go func() {
		_, err := runHeadlessOnceForGatewayAppTest(t.Context(), host, parent, "", "interrupt running parent", headless.Options{})
		done <- err
	}()
	select {
	case <-provider.firstRequest:
	case <-time.After(5 * time.Second):
		t.Fatal("parent request did not start")
	}
	interruptTestSession(t, host, parent.SessionRef, "interrupt-tree")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("parent producer did not finish after interrupt")
	}
	for _, ref := range []session.SessionRef{parent.SessionRef, child.SessionRef} {
		active := hostedChildReloadSession(t, host, ref)
		if len(active.Participants) != 0 {
			t.Fatalf("cancelled subtree retained participant input authority: %#v", active.Participants)
		}
	}
	entry, err := host.composition.authorities.taskStore.Get(t.Context(), "task-grandchild")
	if err != nil || entry.Running || entry.State != task.StateInterrupted {
		t.Fatalf("grandchild = %#v, %v", entry, err)
	}
	active := hostedChildReloadSession(t, host, unrelated.SessionRef)
	if len(active.Participants) != 1 {
		t.Fatal("unrelated Session participants changed")
	}
	entry, err = host.composition.authorities.taskStore.Get(t.Context(), "task-interrupt-unrelated")
	if err != nil || !entry.Running {
		t.Fatalf("unrelated Task changed: %#v, %v", entry, err)
	}
}

func TestSessionInterruptDormantNoopDoesNotActivateAndStaleCASRejects(t *testing.T) {
	provider := newHostedChildInputTestProvider(t, false)
	host := newHostedChildInputTestStack(t, provider)
	active, err := startGatewayAppTestSession(t.Context(), host, "interrupt-dormant")
	if err != nil {
		t.Fatal(err)
	}
	client, err := appserver.BindSessionClient(host.ControlClient(), appserver.Principal{ID: host.composition.authorities.userID})
	if err != nil {
		t.Fatal(err)
	}
	wrongRevision := active.Revision + 1
	if _, err := client.InterruptSession(t.Context(), appserver.SessionInterruptRequest{WriteBase: appserver.WriteBase{OperationID: "interrupt-stale", SessionID: active.SessionID, ExpectedRevision: &wrongRevision, ExpectedControllerEpoch: active.Controller.EpochID}}); !errorcode.Is(err, errorcode.Conflict) {
		t.Fatalf("stale interrupt = %v", err)
	}
	interruptTestSession(t, host, active.SessionRef, "interrupt-dormant")
	host.sessionRuntimes.mu.RLock()
	loaded := host.sessionRuntimes.sessions[active.SessionID]
	host.sessionRuntimes.mu.RUnlock()
	if loaded != nil || provider.CallCount() != 0 {
		t.Fatal("idle interrupt activated execution")
	}
}

func interruptTestSession(t *testing.T, host *Stack, ref session.SessionRef, operation string) appserver.CommandResult {
	t.Helper()
	active := hostedChildReloadSession(t, host, ref)
	client, err := appserver.BindSessionClient(host.ControlClient(), appserver.Principal{ID: host.composition.authorities.userID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.InterruptSession(t.Context(), appserver.SessionInterruptRequest{WriteBase: appserver.WriteBase{OperationID: operation, SessionID: ref.SessionID, ExpectedRevision: &active.Revision, ExpectedControllerEpoch: active.Controller.EpochID}, Reason: "test Session interrupt"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func putInterruptOrphanTask(t *testing.T, host *Stack, parent, child session.Session, id, handle string) {
	t.Helper()
	active := hostedChildReloadSession(t, host, parent.SessionRef)
	binding, ok := hostedChildParticipantByIdentity(active, child.SessionID, id)
	if !ok {
		t.Fatal("missing Task participant identity")
	}
	if err := host.composition.authorities.taskStore.Upsert(t.Context(), &task.Entry{TaskID: id, Handle: handle, Kind: task.KindSubagent, Session: parent.SessionRef, State: task.StateRunning, Running: true, Spec: map[string]any{"agent": "orbit", "agent_id": binding.ID, "session_id": child.SessionID, "delegation_id": id, "turn_seq": int64(1)}, Metadata: map[string]any{"spawn_status": "committed"}}); err != nil {
		t.Fatal(err)
	}
}
