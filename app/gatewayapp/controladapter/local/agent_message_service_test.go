package local

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

func TestTrustedAgentMessageSourceUsesExactParticipantBinding(t *testing.T) {
	t.Parallel()

	binding := session.ParticipantBinding{
		ID: "participant-1", SessionID: "managed-child-1", Kind: session.ParticipantKindSubagent,
		Role: session.ParticipantRoleDelegated, AgentName: "orbit", Label: "@orbit", DelegationID: "task-1",
	}
	resolved, err := (&AgentMessageService{}).trustedAgentMessageSource(
		context.Background(),
		appserver.Principal{ID: "managed-child-1"},
		session.Session{Participants: []session.ParticipantBinding{binding}},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantActor := session.ParticipantExecutor(binding)
	if resolved.actor != wantActor {
		t.Fatalf("trusted actor = %#v, want bound participant %#v", resolved.actor, wantActor)
	}
	if resolved.scope.Participant.ID != binding.ID || resolved.scope.Participant.DelegationID != binding.DelegationID || resolved.scope.Source != "appserver_agent_message" {
		t.Fatalf("trusted scope = %#v, want participant binding", resolved.scope)
	}
}

func TestTrustedAgentMessageSourceUsesExactControllerBinding(t *testing.T) {
	t.Parallel()

	binding := session.ControllerBinding{
		Kind: session.ControllerKindACP, ControllerID: "controller-1", AgentName: "main", EpochID: "epoch-1",
	}
	resolved, err := (&AgentMessageService{}).trustedAgentMessageSource(
		context.Background(),
		appserver.Principal{ID: "controller-1"},
		session.Session{Controller: binding},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.actor != session.ControllerExecutor(binding) || resolved.scope.Controller.ID != binding.ControllerID || resolved.scope.Controller.EpochID != binding.EpochID {
		t.Fatalf("controller source = actor %#v scope %#v, want exact controller binding", resolved.actor, resolved.scope)
	}
}

func TestTrustedAgentMessageSourceFailsClosedForGenericPrincipal(t *testing.T) {
	t.Parallel()

	if _, err := (&AgentMessageService{}).trustedAgentMessageSource(
		context.Background(), appserver.Principal{ID: "guardian"}, session.Session{},
	); err == nil {
		t.Fatal("trustedAgentMessageSource() error = nil, want unbound principal rejection")
	}
}

func TestTrustedAgentMessageSourceFailsClosedWithoutPrincipal(t *testing.T) {
	t.Parallel()

	if _, err := (&AgentMessageService{}).trustedAgentMessageSource(
		context.Background(), appserver.Principal{}, session.Session{},
	); err == nil {
		t.Fatal("trustedAgentMessageSource() error = nil, want missing principal rejection")
	}
}

func TestManagedChildParentMessageSourceRequiresExactTaskParticipantRelation(t *testing.T) {
	t.Parallel()

	controller := session.ControllerBinding{
		Kind: session.ControllerKindKernel, ControllerID: "parent-controller", AgentName: "main", EpochID: "epoch-1",
	}
	parent := session.Session{
		Controller: controller,
		Participants: []session.ParticipantBinding{{
			ID: "child-agent", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated,
			SessionID: "child-session", DelegationID: "task-1", AgentName: "orbit", Label: "@orbit",
		}},
	}
	actor, scope, err := managedChildParentMessageSource("child-session", "task-1", parent)
	if err != nil {
		t.Fatal(err)
	}
	if actor != session.ControllerExecutor(controller) || scope.Source != "appserver_agent_message" {
		t.Fatalf("managed child source = actor %#v scope %#v, want parent controller", actor, scope)
	}
	if _, _, err := managedChildParentMessageSource("child-session", "forged-task", parent); err == nil {
		t.Fatal("forged managed child Task relation was accepted")
	}
	if _, _, err := managedChildParentMessageSource("forged-child", "task-1", parent); err == nil {
		t.Fatal("forged managed child Session relation was accepted")
	}
}

func TestAgentMessageFailsClosedWhenLifecycleOrBindingChangesBeforeAppend(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T, *gatewayapp.Stack, *AgentMessageService, session.Session)
	}{
		{name: "target close", run: testAgentMessageTargetCloseRace},
		{name: "target handoff", run: testAgentMessageTargetHandoffRace},
		{name: "target participant detach", run: testAgentMessageTargetDetachRace},
		{name: "managed parent detach", run: testAgentMessageManagedParentDetachRace},
	} {
		t.Run(test.name, func(t *testing.T) {
			host, service, target := newAgentMessageRaceFixture(t)
			test.run(t, host, service, target)
		})
	}
}

func newAgentMessageRaceFixture(t *testing.T) (*gatewayapp.Stack, *AgentMessageService, session.Session) {
	t.Helper()
	root := t.TempDir()
	host, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName: "caelis-test", UserID: "owner", StoreDir: filepath.Join(root, "store"),
		WorkspaceKey: "workspace", WorkspaceCWD: root,
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	target, err := host.Sessions().StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis-test", UserID: "owner",
		Workspace: session.WorkspaceRef{Key: "workspace", CWD: root},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewAgentMessageService(host)
	if err != nil {
		t.Fatal(err)
	}
	return host, service, target
}

func testAgentMessageTargetCloseRace(t *testing.T, host *gatewayapp.Stack, service *AgentMessageService, target session.Session) {
	t.Helper()
	target = bindAgentMessageRaceController(t, host, target, "controller-1")
	service.deliver = func(ctx context.Context, request gatewayapp.DeliverAgentMessageRequest) (gatewayapp.AgentMessageDelivery, error) {
		current, err := host.Sessions().Session(ctx, target.SessionRef)
		if err != nil {
			return gatewayapp.AgentMessageDelivery{}, err
		}
		if _, err := appserver.CloseSession(ctx, host.Sessions(), current, "concurrent close"); err != nil {
			return gatewayapp.AgentMessageDelivery{}, err
		}
		return host.DeliverAgentMessage(ctx, request)
	}
	assertAgentMessageRaceRejected(t, host, service, target, appserver.Principal{ID: "controller-1"}, "close-race")
}

func testAgentMessageTargetHandoffRace(t *testing.T, host *gatewayapp.Stack, service *AgentMessageService, target session.Session) {
	t.Helper()
	target = bindAgentMessageRaceController(t, host, target, "controller-1")
	service.deliver = func(ctx context.Context, request gatewayapp.DeliverAgentMessageRequest) (gatewayapp.AgentMessageDelivery, error) {
		if _, err := host.Sessions().BindController(ctx, session.BindControllerRequest{
			SessionRef: target.SessionRef,
			Binding: session.ControllerBinding{
				Kind: session.ControllerKindACP, ControllerID: "controller-2", EpochID: "epoch-2",
			},
		}); err != nil {
			return gatewayapp.AgentMessageDelivery{}, err
		}
		return host.DeliverAgentMessage(ctx, request)
	}
	assertAgentMessageRaceRejected(t, host, service, target, appserver.Principal{ID: "controller-1"}, "handoff-race")
}

func testAgentMessageTargetDetachRace(t *testing.T, host *gatewayapp.Stack, service *AgentMessageService, target session.Session) {
	t.Helper()
	var err error
	target, err = host.Sessions().PutParticipant(context.Background(), session.PutParticipantRequest{
		SessionRef: target.SessionRef,
		Binding: session.ParticipantBinding{
			ID: "participant-1", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service.deliver = func(ctx context.Context, request gatewayapp.DeliverAgentMessageRequest) (gatewayapp.AgentMessageDelivery, error) {
		if _, err := host.Sessions().RemoveParticipant(ctx, session.RemoveParticipantRequest{
			SessionRef: target.SessionRef, ParticipantID: "participant-1",
		}); err != nil {
			return gatewayapp.AgentMessageDelivery{}, err
		}
		return host.DeliverAgentMessage(ctx, request)
	}
	assertAgentMessageRaceRejected(t, host, service, target, appserver.Principal{ID: "participant-1"}, "detach-race")
}

func testAgentMessageManagedParentDetachRace(t *testing.T, host *gatewayapp.Stack, service *AgentMessageService, target session.Session) {
	t.Helper()
	ctx := context.Background()
	workspace := session.WorkspaceRef{Key: target.WorkspaceKey, CWD: target.CWD}
	parent, err := host.Sessions().StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis-test", UserID: "owner", Workspace: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	parent = bindAgentMessageRaceController(t, host, parent, "parent-controller")
	// System-managed identity is immutable Session metadata, so seed it on a
	// replacement child created specifically for this cross-Session race.
	target, err = host.Sessions().StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis-test", UserID: "owner", Workspace: workspace,
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent:  sessionvisibility.SystemManagedAgentSubagent,
			sessionvisibility.MetadataSystemManagedParent: parent.SessionID,
			sessionvisibility.MetadataSystemManagedTask:   "task-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent, err = host.Sessions().PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: parent.SessionRef,
		Binding: session.ParticipantBinding{
			ID: "child-agent-2", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated,
			SessionID: target.SessionID, DelegationID: "task-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service.deliver = func(ctx context.Context, request gatewayapp.DeliverAgentMessageRequest) (gatewayapp.AgentMessageDelivery, error) {
		if _, err := host.Sessions().RemoveParticipant(ctx, session.RemoveParticipantRequest{
			SessionRef: parent.SessionRef, ParticipantID: "child-agent-2",
		}); err != nil {
			return gatewayapp.AgentMessageDelivery{}, err
		}
		return host.DeliverAgentMessage(ctx, request)
	}
	assertAgentMessageRaceRejected(t, host, service, target, appserver.Principal{ID: "owner"}, "managed-parent-detach-race")
}

func bindAgentMessageRaceController(t *testing.T, host *gatewayapp.Stack, target session.Session, controllerID string) session.Session {
	t.Helper()
	updated, err := host.Sessions().BindController(context.Background(), session.BindControllerRequest{
		SessionRef: target.SessionRef,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: controllerID, EpochID: controllerID + "-epoch",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func assertAgentMessageRaceRejected(
	t *testing.T,
	host *gatewayapp.Stack,
	service *AgentMessageService,
	target session.Session,
	principal appserver.Principal,
	messageID string,
) {
	t.Helper()
	_, err := service.DeliverAgentMessage(context.Background(), principal, appserver.AgentMessageRequest{
		SessionID: target.SessionID, MessageID: messageID, Text: "must not append",
	})
	if err == nil {
		t.Fatal("concurrent lifecycle or binding mutation was accepted")
	}
	events, readErr := host.Sessions().Events(context.Background(), session.EventsRequest{SessionRef: target.SessionRef})
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, event := range events {
		if event != nil && event.MessageID == messageID {
			t.Fatalf("rejected Agent message became durable: %#v", event)
		}
	}
}
