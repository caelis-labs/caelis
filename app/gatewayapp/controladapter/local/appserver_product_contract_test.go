package local

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	controlstatus "github.com/caelis-labs/caelis/control/status"
)

func TestModelCatalogMutationsRefreshActiveSessionPickerWithoutReplacingRuntime(t *testing.T) {
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
	appServer, err := NewAppServer(host)
	if err != nil {
		t.Fatal(err)
	}
	clients, err := appServer.Bind(appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	initialStatus, err := clients.Status.SessionStatus(ctx, appserver.StatusRequest{Surface: "test"})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := clients.Configuration.ConnectModel(ctx, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "connect-initial-model", ExpectedRevision: &initialStatus.Configuration.Revision},
		Config:    appserver.ConnectConfig{Provider: "ollama", Model: "initial", BaseURL: "http://127.0.0.1:11434"},
	})
	if err != nil || initial.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel(initial) = %#v, %v", initial, err)
	}
	sessionID := createAppServerTestSession(t, clients, "create-model-refresh", "model-refresh", workspace)
	observation, err := clients.Sessions.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observation.Subscription.Close() })
	lease, err := host.AcquireControlRuntime(ctx, appserver.Principal{ID: "local-user"}, appserver.ActionSessionInspect, sessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(ctx); err != nil {
		t.Fatal(err)
	}

	connected, err := clients.Configuration.ConnectModel(ctx, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "connect-late-model", ExpectedRevision: &initial.Revision},
		Config: appserver.ConnectConfig{
			Provider: "ollama", Model: "late", BaseURL: "http://127.0.0.1:11434",
		},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel() = %#v, %v", connected, err)
	}
	assertSlashArgCandidate(t, clients.Completion, sessionID, "model use", "ollama/late", true)
	assertSlashArgCandidate(t, clients.Completion, sessionID, "model del", "ollama/late", true)
	remote := bindAppServerHTTPTestClient(t, appServer, "local-user")
	assertSlashArgCandidate(t, remote, sessionID, "model use", "ollama/late", true)

	active, err := clients.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := clients.Configuration.UseSessionModel(ctx, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "select-late-model",
			SessionID:               sessionID,
			ExpectedRevision:        &active.Revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Model: "ollama/late",
	})
	if err != nil || selected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("UseSessionModel() = %#v, %v", selected, err)
	}
	deleted, err := clients.Configuration.DeleteModel(ctx, appserver.DeleteModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "delete-late-model", ExpectedRevision: &connected.Revision},
		Model:     "ollama/late",
	})
	if err != nil || deleted.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DeleteModel() = %#v, %v", deleted, err)
	}
	assertSlashArgCandidate(t, clients.Completion, sessionID, "model use", "ollama/late", false)
	assertSlashArgCandidate(t, clients.Completion, sessionID, "model del", "ollama/late", false)
	assertSlashArgCandidate(t, remote, sessionID, "model del", "ollama/late", false)

	afterDelete, err := clients.Status.SessionStatus(ctx, appserver.StatusRequest{SessionID: sessionID, Surface: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if afterDelete.ModelStatus.Display != "ollama/late" {
		t.Fatalf("active Session model after catalog deletion = %q, want pinned ollama/late", afterDelete.ModelStatus.Display)
	}
	if afterDelete.SandboxStatus.RequestedBackend != "host" ||
		afterDelete.SandboxStatus.ResolvedBackend != "host" ||
		afterDelete.SandboxStatus.Route != "host" {
		t.Fatalf("active Session sandbox status = %#v, want pinned host Runtime", afterDelete.SandboxStatus)
	}
}

func TestHostCapabilitiesDoNotCreateSession(t *testing.T) {
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

	if _, err := clients.Status.SessionStatus(ctx, appserver.StatusRequest{Surface: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Completion.CompleteSkill(ctx, appserver.CompletionRequest{Surface: "test", Limit: 8}); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Agents.ListAgents(ctx, appserver.AgentRequest{Surface: "test", Limit: 8}); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Agents.AgentBindingStatus(ctx, appserver.AgentRequest{Surface: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Plugins.ListPlugins(ctx, appserver.PluginRequest{Surface: "test"}); err != nil {
		t.Fatal(err)
	}
	status, err := clients.Status.SessionStatus(ctx, appserver.StatusRequest{Surface: "test", IncludeDiagnostics: true})
	if err != nil {
		t.Fatal(err)
	}
	revision := status.Configuration.Revision
	if _, err := clients.Configuration.RefreshSandbox(ctx, appserver.SandboxRequest{WriteBase: appserver.WriteBase{
		OperationID: "product-contract-refresh", ExpectedRevision: &revision,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Configuration.ResetSandbox(ctx, appserver.SandboxRequest{WriteBase: appserver.WriteBase{
		OperationID: "product-contract-reset", ExpectedRevision: &revision,
	}}); err != nil {
		t.Fatal(err)
	}
	listed, err := clients.Sessions.ListSessions(ctx, appserver.ListSessionsRequest{WorkspaceKey: "workspace", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 0 {
		t.Fatalf("Host capabilities created Sessions: %#v", listed.Sessions)
	}
}

func TestAgentHandoffReplaysOnePrincipalBoundCommandReceipt(t *testing.T) {
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
	sessionID := createAppServerTestSession(t, clients, "create-handoff", "handoff", workspace)
	state, err := clients.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	revision := state.Revision
	request := appserver.HandoffAgentRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "principal-handoff-1",
			SessionID:               sessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: state.Controller.EpochID,
		},
		Target: "local",
	}
	first, err := clients.Agents.HandoffAgent(ctx, request)
	if err != nil || first.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("first HandoffAgent() = %#v, %v", first, err)
	}
	second, err := clients.Agents.HandoffAgent(ctx, request)
	if err != nil || second != first {
		t.Fatalf("replayed HandoffAgent() = %#v, %v; want %#v", second, err, first)
	}
	observed, err := clients.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if observed.Revision != first.Revision {
		t.Fatalf("Session revision after replay = %d, receipt = %d", observed.Revision, first.Revision)
	}
	stale := request
	stale.OperationID = "principal-handoff-stale"
	stale.Target = "orbit"
	conflicted, err := clients.Agents.HandoffAgent(ctx, stale)
	if conflicted.Outcome != appserver.OutcomeConflicted || err == nil {
		t.Fatalf("stale HandoffAgent() = %#v, %v", conflicted, err)
	}
	currentRevision := observed.Revision
	wrongEpoch := request
	wrongEpoch.OperationID = "principal-handoff-wrong-epoch"
	wrongEpoch.ExpectedRevision = &currentRevision
	wrongEpoch.ExpectedControllerEpoch = "wrong-epoch"
	wrongEpoch.Target = "orbit"
	conflicted, err = clients.Agents.HandoffAgent(ctx, wrongEpoch)
	if conflicted.Outcome != appserver.OutcomeConflicted || err == nil {
		t.Fatalf("epoch-mismatched HandoffAgent() = %#v, %v", conflicted, err)
	}
}

func TestAgentMessageUsesManagedChildParentBindingAndRejectsForgedSource(t *testing.T) {
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
	parentID := createAppServerTestSession(t, clients, "create-message-parent", "message-parent", workspace)
	parent, err := host.Sessions.Session(ctx, session.SessionRef{SessionID: parentID})
	if err != nil {
		t.Fatal(err)
	}
	controller := session.ControllerBinding{
		Kind: session.ControllerKindKernel, ControllerID: "parent-controller", AgentName: "main", EpochID: "parent-epoch",
	}
	parent, err = host.Sessions.BindController(ctx, session.BindControllerRequest{SessionRef: parent.SessionRef, Binding: controller})
	if err != nil {
		t.Fatal(err)
	}
	child, err := host.Sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis-test", UserID: "local-user", PreferredSessionID: "message-child",
		Workspace: session.WorkspaceRef{Key: "workspace", CWD: workspace},
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent:  sessionvisibility.SystemManagedAgentSubagent,
			sessionvisibility.MetadataSystemManagedParent: parentID,
			sessionvisibility.MetadataSystemManagedTask:   "task-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent, err = host.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: parent.SessionRef,
		Binding: session.ParticipantBinding{
			ID: "child-agent", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated,
			AgentName: "orbit", Label: "@orbit", SessionID: child.SessionID, DelegationID: "task-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	request := appserver.AgentMessageRequest{
		SessionID: child.SessionID, MessageID: "managed-message-1", Text: "continue", DisplayFrom: "@forged",
	}
	first, err := clients.AgentMessages.DeliverAgentMessage(ctx, request)
	if err != nil || !first.Accepted {
		t.Fatalf("DeliverAgentMessage() = %#v, %v", first, err)
	}
	second, err := clients.AgentMessages.DeliverAgentMessage(ctx, request)
	if err != nil || !second.Accepted {
		t.Fatalf("duplicate DeliverAgentMessage() = %#v, %v", second, err)
	}
	loaded, err := host.Sessions.Events(ctx, session.EventsRequest{SessionRef: child.SessionRef, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var messages []*session.Event
	for _, event := range loaded {
		if event != nil && event.MessageID == request.MessageID {
			messages = append(messages, event)
		}
	}
	if len(messages) != 1 {
		t.Fatalf("durable Agent messages = %#v, want one idempotent event", messages)
	}
	if messages[0].Actor != session.ControllerExecutor(controller) || messages[0].Meta["display_from"] != "@forged" {
		t.Fatalf("durable Agent message = actor %#v meta %#v, want parent controller and display-only wire source", messages[0].Actor, messages[0].Meta)
	}

	forgedChild, err := host.Sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis-test", UserID: "local-user", PreferredSessionID: "forged-message-child",
		Workspace: session.WorkspaceRef{Key: "workspace", CWD: workspace},
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent:  sessionvisibility.SystemManagedAgentSubagent,
			sessionvisibility.MetadataSystemManagedParent: parentID,
			sessionvisibility.MetadataSystemManagedTask:   "forged-task",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.AgentMessages.DeliverAgentMessage(ctx, appserver.AgentMessageRequest{
		SessionID: forgedChild.SessionID, MessageID: "forged-message", Text: "must not append", DisplayFrom: "@main",
	}); err == nil {
		t.Fatal("forged managed-child Task relation was accepted")
	}
	if _, err := clients.AgentMessages.DeliverAgentMessage(ctx, appserver.AgentMessageRequest{
		SessionID: parentID, MessageID: "unbound-message", Text: "must not append", DisplayFrom: "@main",
	}); err == nil {
		t.Fatal("unbound ordinary Session source was accepted")
	}
}

func TestAgentMessageExactBindingsAuthorizeEmbeddedAndHTTPClients(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	host, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName: "caelis-test", UserID: "owner", StoreDir: filepath.Join(root, "store"),
		WorkspaceKey: "workspace", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	appServer, err := NewAppServer(host)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := appServer.Bind(appserver.Principal{ID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := createAppServerTestSession(t, owner, "create-exact-binding", "exact-binding", workspace)
	active, err := host.Sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	active, err = host.Sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: active.SessionRef,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "controller-1", AgentName: "main", EpochID: "epoch-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err = host.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: active.SessionRef,
		Binding: session.ParticipantBinding{
			ID: "participant-1", Kind: session.ParticipantKindACP,
			Role: session.ParticipantRoleSidecar, AgentName: "reviewer", Label: "@reviewer",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	controllerClients, err := appServer.Bind(appserver.Principal{ID: "controller-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := controllerClients.AgentMessages.DeliverAgentMessage(ctx, appserver.AgentMessageRequest{
		SessionID: sessionID, MessageID: "controller-message", Text: "from controller",
	}); err != nil || !result.Accepted {
		t.Fatalf("embedded controller message = %#v, %v", result, err)
	}
	participantClients, err := appServer.Bind(appserver.Principal{ID: "participant-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := participantClients.AgentMessages.DeliverAgentMessage(ctx, appserver.AgentMessageRequest{
		SessionID: sessionID, MessageID: "participant-message", Text: "from participant",
	}); err != nil || !result.Accepted {
		t.Fatalf("embedded participant message = %#v, %v", result, err)
	}

	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	authenticator, err := controlserver.BearerTokenAuthenticator(token, appserver.Principal{ID: "participant-1"})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := controlserver.Handler(controlserver.Dependencies{
		Services: appServer.Services,
	}, controlserver.Config{Authenticator: authenticator, AllowedHosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := httpclient.New(httpclient.Config{
		BaseURL: "http://127.0.0.1", BearerToken: token,
		HTTPClient:    &http.Client{Transport: appServerHandlerRoundTripper{handler: handler}},
		Compatibility: appserver.CurrentCompatibility(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := remote.DeliverAgentMessage(ctx, appserver.AgentMessageRequest{
		SessionID: sessionID, MessageID: "http-participant-message", Text: "from HTTP participant",
	}); err != nil || !result.Accepted {
		t.Fatalf("HTTP participant message = %#v, %v", result, err)
	}

	events, err := host.Sessions.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	wantActors := map[string]string{
		"controller-message":       "controller-1",
		"participant-message":      "participant-1",
		"http-participant-message": "participant-1",
	}
	for _, event := range events {
		if event == nil {
			continue
		}
		want, ok := wantActors[event.MessageID]
		if !ok {
			continue
		}
		if event.Actor.ID != want {
			t.Fatalf("message %q Actor = %#v, want ID %q", event.MessageID, event.Actor, want)
		}
		delete(wantActors, event.MessageID)
	}
	if len(wantActors) != 0 {
		t.Fatalf("missing exact-binding Agent messages: %#v", wantActors)
	}
}

type appServerHandlerRoundTripper struct {
	handler http.Handler
}

func (transport appServerHandlerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	transport.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

func TestHostResumeCompletionUsesPrincipalVisibleSessions(t *testing.T) {
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
	visibleID := createAppServerTestSession(t, clients, "create-visible-resume", "visible-resume", workspace)
	foreign, err := host.Sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis-test", UserID: "different-owner",
		Workspace:          session.WorkspaceRef{Key: "workspace", CWD: workspace},
		PreferredSessionID: "foreign-resume", Title: "foreign resume",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := clients.Completion.CompleteResume(ctx, appserver.CompletionRequest{Surface: "test", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !resumeCandidatesContain(candidates, visibleID) || resumeCandidatesContain(candidates, foreign.SessionID) {
		t.Fatalf("principal resume candidates = %#v, want %q and not %q", candidates, visibleID, foreign.SessionID)
	}
}

func TestClosedSessionRejectsFocusedMutations(t *testing.T) {
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
	sessionID := createAppServerTestSession(t, clients, "create-closed-mutations", "closed-mutations", workspace)
	beforeClose, err := clients.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := clients.Sessions.CloseSession(ctx, appserver.CloseSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "close-before-mutations", SessionID: sessionID},
	})
	if err != nil {
		t.Fatal(err)
	}
	closedRevision := closed.Revision

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "configuration",
			run: func() error {
				_, err := clients.Configuration.ConfigureSessionMode(ctx, appserver.SessionModeRequest{
					WriteBase: appserver.WriteBase{OperationID: "closed-mode", SessionID: sessionID, ExpectedRevision: &closedRevision, ExpectedControllerEpoch: beforeClose.Controller.EpochID},
					Mode:      "manual",
				})
				return err
			},
		},
		{
			name: "presentation",
			run: func() error {
				_, err := clients.Configuration.ConfigureSessionPresentationMode(ctx, appserver.SessionPresentationModeRequest{
					WriteBase: appserver.WriteBase{OperationID: "closed-presentation-mode", SessionID: sessionID, ExpectedRevision: &closedRevision},
					Mode:      "manual",
				})
				return err
			},
		},
		{
			name: "Agent message",
			run: func() error {
				_, err := clients.AgentMessages.DeliverAgentMessage(ctx, appserver.AgentMessageRequest{
					SessionID: sessionID, MessageID: "closed-message", Text: "must not append",
				})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, appserver.ErrSessionClosed) {
				t.Fatalf("closed mutation error = %v, want ErrSessionClosed", err)
			}
		})
	}
	state, err := clients.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != closed.Revision {
		t.Fatalf("closed mutation revision = %d, want unchanged %d", state.Revision, closed.Revision)
	}
}

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

	statusBefore, err := clients.Status.SessionStatus(ctx, appserver.StatusRequest{
		SessionID: sessionID, Surface: "test", IncludeDiagnostics: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if statusBefore.ModelStatus.Provider != "acp" || statusBefore.ModelStatus.Alias != "codex" ||
		statusBefore.ModelStatus.Name != "codex" || statusBefore.ModelStatus.Display != "codex" {
		t.Fatalf("ACP status model = %#v, want coherent acp/codex identity", statusBefore.ModelStatus)
	}
	agentStatus, err := clients.Agents.AgentStatus(ctx, appserver.AgentRequest{SessionID: sessionID, Surface: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if agentStatus.ControllerKind != string(session.ControllerKindACP) || agentStatus.ControllerLabel != "codex" {
		t.Fatalf("Agent status controller = %#v, want durable ACP controller", agentStatus)
	}

	state, err := clients.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	revision := state.Revision
	modeResult, err := clients.Configuration.ConfigureSessionMode(ctx, appserver.SessionModeRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "set-local-approval-mode",
			SessionID:               sessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: state.Controller.EpochID,
		},
		Mode: "manual",
	})
	if err != nil || modeResult.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConfigureSessionMode() = %#v, %v", modeResult, err)
	}
	statusAfter, err := clients.Status.SessionStatus(ctx, appserver.StatusRequest{
		SessionID: sessionID, Surface: "test", IncludeDiagnostics: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if statusAfter.Session.SessionMode != "manual" {
		t.Fatalf("approval mode = %q, want manual", statusAfter.Session.SessionMode)
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

	status, err := clients.Status.SessionStatus(ctx, appserver.StatusRequest{
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

	agentStatus, err := clients.Agents.AgentStatus(ctx, appserver.AgentRequest{SessionID: sessionID, Surface: "test"})
	if err != nil {
		t.Fatal(err)
	}
	wantDirect := []appserver.AgentParticipantSnapshot{{
		ID: "side-1", Label: "@side", AgentName: "codex", Kind: string(session.ParticipantKindACP),
		Role: string(session.ParticipantRoleSidecar), Source: "test", SessionID: "child-side",
	}}
	wantDelegated := []appserver.AgentParticipantSnapshot{{
		ID: "task-1", Label: "@worker", AgentName: "worker", Kind: string(session.ParticipantKindSubagent),
		Role: string(session.ParticipantRoleDelegated), Source: "test", SessionID: "child-worker",
	}}
	if !reflect.DeepEqual(agentStatus.Participants, wantDirect) || !reflect.DeepEqual(agentStatus.DelegatedParticipants, wantDelegated) {
		t.Fatalf("Agent participants = direct %#v delegated %#v, want direct %#v delegated %#v",
			agentStatus.Participants, agentStatus.DelegatedParticipants, wantDirect, wantDelegated)
	}
}

func TestBoundAppServerKeepsSessionSkillSnapshotUntilLastObserverDetaches(t *testing.T) {
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
	observation, err := clients.Sessions.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer observation.Subscription.Close()
	lease, err := host.AcquireControlRuntime(ctx, appserver.Principal{ID: "local-user"}, appserver.ActionSessionInspect, sessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(ctx); err != nil {
		t.Fatal(err)
	}

	initial, err := clients.Completion.CompleteSkill(ctx, appserver.CompletionRequest{
		SessionID: sessionID, Surface: "test", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !completionCandidatesContain(initial, "initial") {
		t.Fatalf("initial completions = %#v, want initial skill", initial)
	}
	writeAppServerTestSkill(t, skillRoot, "late", "Late skill.")
	current, err := clients.Completion.CompleteSkill(ctx, appserver.CompletionRequest{Surface: "test", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !completionCandidatesContain(current, "initial") || !completionCandidatesContain(current, "late") {
		t.Fatalf("no-Session current completions = %#v, want initial and late skills", current)
	}
	fixed, err := clients.Completion.CompleteSkill(ctx, appserver.CompletionRequest{
		SessionID: sessionID, Surface: "test", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if completionCandidatesContain(fixed, "late") {
		t.Fatalf("fixed Session completions = %#v, should not include late skill", fixed)
	}
	if err := observation.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	waitForSessionSkill(t, clients.Completion, sessionID, "late")
	activation, err := host.AcquireControlRuntime(
		ctx,
		appserver.Principal{ID: "local-user"},
		appserver.ActionSessionInspect,
		sessionID,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer activation.Close(ctx)
	refreshed, err := clients.Completion.CompleteSkill(ctx, appserver.CompletionRequest{
		SessionID: sessionID, Surface: "test", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !completionCandidatesContain(refreshed, "initial") || !completionCandidatesContain(refreshed, "late") {
		t.Fatalf("reactivated Session completions = %#v, want initial and late skills", refreshed)
	}
}

func waitForSessionSkill(
	t *testing.T,
	completion appserver.CompletionClient,
	sessionID string,
	skillName string,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		candidates, err := completion.CompleteSkill(context.Background(), appserver.CompletionRequest{
			SessionID: sessionID,
			Surface:   "test",
			Limit:     10,
		})
		if err == nil && completionCandidatesContain(candidates, skillName) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Session %q did not rebuild with skill %q after its last observer detached", sessionID, skillName)
}

func TestNoSessionSkillCompletionDoesNotStartPluginMCP(t *testing.T) {
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

	pluginRoot := filepath.Join(root, "completion-side-effects")
	manifestRoot := filepath.Join(pluginRoot, ".caelis-plugin")
	skillRoot := filepath.Join(pluginRoot, "skills")
	if err := os.MkdirAll(manifestRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	writeAppServerTestSkill(t, skillRoot, "completion-side-effect-skill", "Completion-only plugin skill.")
	marker := filepath.Join(root, "mcp-started")
	manifest := fmt.Sprintf(`{
  "name": "completion-side-effects",
  "version": "1.0.0",
  "skills": [{"root": "skills", "namespace": "completion"}],
  "mcpServers": {
    "must-not-start": {
      "command": %q,
      "args": ["-test.run=^TestCompletionSkillDiscoveryMCPHelperProcess$"],
      "env": {
        "CAELIS_COMPLETION_MCP_HELPER": "1",
        "CAELIS_COMPLETION_MCP_MARKER": %q
      }
    }
  }
}`, os.Args[0], marker)
	if err := os.WriteFile(filepath.Join(manifestRoot, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := clients.Status.SessionStatus(ctx, appserver.StatusRequest{Surface: "test"})
	if err != nil {
		t.Fatal(err)
	}
	added, err := clients.Plugins.AddPluginPath(ctx, appserver.AddPluginPathRequest{
		WriteBase: appserver.WriteBase{
			OperationID:      "add-completion-side-effect-plugin",
			ExpectedRevision: &status.Configuration.Revision,
		},
		Path: pluginRoot,
	})
	if err != nil || added.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("AddPluginPath() = %#v, %v", added, err)
	}

	candidates, err := clients.Completion.CompleteSkill(ctx, appserver.CompletionRequest{
		Surface: "test", Query: "completion-side-effect", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !completionCandidatesContain(candidates, "completion:completion-side-effect-skill") {
		t.Fatalf("CompleteSkill() = %#v, want plugin skill", candidates)
	}
	resolved, err := clients.Completion.ResolveSkill(ctx, appserver.CompletionRequest{
		Surface: "test", Name: "completion:completion-side-effect-skill",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Canonical != "completion:completion-side-effect-skill" {
		t.Fatalf("ResolveSkill() = %#v, want canonical plugin skill", resolved)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Skill completion started plugin MCP; marker stat error = %v", err)
	}
}

func TestCompletionSkillDiscoveryMCPHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_COMPLETION_MCP_HELPER") != "1" {
		return
	}
	_ = os.WriteFile(os.Getenv("CAELIS_COMPLETION_MCP_MARKER"), []byte("started"), 0o600)
	os.Exit(1)
}

func bindAppServerTestClients(t *testing.T, host *gatewayapp.Stack) appserver.AppServerClients {
	t.Helper()
	server, err := NewAppServer(host)
	if err != nil {
		t.Fatal(err)
	}
	clients, err := server.Bind(appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	return clients
}

func createAppServerTestSession(
	t *testing.T,
	clients appserver.AppServerClients,
	operationID string,
	preferredSessionID string,
	workspace string,
) string {
	t.Helper()
	created, err := clients.Sessions.CreateSession(context.Background(), appserver.CreateSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: operationID}, PreferredSessionID: preferredSessionID,
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

func completionCandidatesContain(candidates []appserver.CompletionCandidate, value string) bool {
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.Value) == value {
			return true
		}
	}
	return false
}

func assertSlashArgCandidate(
	t *testing.T,
	completion appserver.CompletionClient,
	sessionID string,
	command string,
	value string,
	want bool,
) {
	t.Helper()
	candidates, err := completion.CompleteSlashArg(context.Background(), appserver.CompletionRequest{
		SessionID: sessionID,
		Surface:   "test",
		Command:   command,
		Limit:     20,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.Value) == value || strings.TrimSpace(candidate.Display) == value {
			found = true
			break
		}
	}
	if found != want {
		t.Fatalf("CompleteSlashArg(%q) candidates = %#v; contains %q = %v, want %v", command, candidates, value, found, want)
	}
}

func bindAppServerHTTPTestClient(t *testing.T, appServer *AppServer, principalID string) *httpclient.Client {
	t.Helper()
	const token = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	authenticator, err := controlserver.BearerTokenAuthenticator(token, appserver.Principal{ID: principalID})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := controlserver.Handler(controlserver.Dependencies{
		Services: appServer.Services,
	}, controlserver.Config{Authenticator: authenticator, AllowedHosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	client, err := httpclient.New(httpclient.Config{
		BaseURL: "http://127.0.0.1", BearerToken: token,
		HTTPClient:    &http.Client{Transport: appServerHandlerRoundTripper{handler: handler}},
		Compatibility: appserver.CurrentCompatibility(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func resumeCandidatesContain(candidates []appserver.ResumeCandidate, sessionID string) bool {
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.SessionID) == strings.TrimSpace(sessionID) {
			return true
		}
	}
	return false
}
