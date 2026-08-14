package gatewayapp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

func TestHostedChildMailboxRoutesParentAndSiblingThroughParentSession(t *testing.T) {
	t.Parallel()

	var got agentmessage.Request
	var gotParent session.SessionRef
	stack := &Stack{
		hostedChildMailbox: func(_ context.Context, parentRef session.SessionRef, req agentmessage.Request) (agentmessage.Response, error) {
			gotParent = parentRef
			got = req
			return agentmessage.Response{Accepted: true, State: agentmessage.StatePending}, nil
		},
	}
	parent := session.Session{
		SessionRef: session.SessionRef{SessionID: "parent-1", AppName: "caelis", UserID: "owner"},
		Controller: session.ControllerBinding{
			Kind: session.ControllerKindKernel, ControllerID: "parent-controller", AgentName: "main", EpochID: "epoch-1",
		},
		Participants: []session.ParticipantBinding{{
			ID: "child-agent", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated,
			SessionID: "child-1", DelegationID: "task-1", AgentName: "orbit", Label: "@orbit",
		}, {
			ID: "sibling-agent", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated,
			SessionID: "child-2", DelegationID: "task-2", AgentName: "research", Label: "@research",
		}},
	}
	child := session.Session{
		SessionRef: session.SessionRef{SessionID: "child-1", AppName: "caelis", UserID: "owner"},
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent:  sessionvisibility.SystemManagedAgentSubagent,
			sessionvisibility.MetadataSystemManagedParent: parent.SessionID,
			sessionvisibility.MetadataSystemManagedTask:   "task-1",
		},
	}
	sender := hostedChildMessageSender{
		deliver: stack.hostedChildMailbox,
		parent:  parent,
		child:   child,
	}

	if _, err := sender.SendMessage(context.Background(), agentmessage.Request{
		MessageID: "msg-parent", To: agentmessage.Parent, Text: "status update",
		From: session.ActorRef{Kind: session.ActorKindController, ID: "child-controller", Name: "main"},
	}); err != nil {
		t.Fatal(err)
	}
	if gotParent.SessionID != parent.SessionID {
		t.Fatalf("parent ref = %#v, want parent Session", gotParent)
	}
	if got.To != agentmessage.Parent || got.Text != "status update" || got.From.Kind != session.ActorKindParticipant ||
		got.From.ID != "child-agent" || got.From.Name != "@orbit" {
		t.Fatalf("parent delivery = %#v, want trusted child participant identity", got)
	}

	if _, err := sender.SendMessage(context.Background(), agentmessage.Request{
		MessageID: "msg-sibling", To: "research", Text: "handoff",
	}); err != nil {
		t.Fatal(err)
	}
	if got.To != "research" || got.From.ID != "child-agent" {
		t.Fatalf("sibling delivery = %#v, want sibling handle with child identity", got)
	}

	if _, err := sender.SendMessage(context.Background(), agentmessage.Request{
		MessageID: "msg-self", To: "@orbit", Text: "loop",
	}); err == nil || !strings.Contains(err.Error(), "cannot message itself") {
		t.Fatalf("self delivery error = %v, want self-target rejection", err)
	}
}

func TestHostedChildSendMessageReachesParentSession(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	host, err := newGatewayAppTestStack(t, Config{
		AppName: "caelis-test", UserID: "owner", StoreDir: filepath.Join(root, "store"),
		WorkspaceKey: "workspace", WorkspaceCWD: root, SkillDirs: []string{t.TempDir()},
		Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })

	parent, err := startGatewayAppTestSession(ctx, host, "parent-mailbox")
	if err != nil {
		t.Fatal(err)
	}
	parent, err = host.Sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: parent.SessionRef,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindKernel, ControllerID: "parent-controller", AgentName: "main", EpochID: "epoch-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := host.Sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: host.AppName, UserID: host.UserID,
		Workspace: session.WorkspaceRef{Key: host.Workspace.Key, CWD: host.Workspace.CWD},
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent:  sessionvisibility.SystemManagedAgentSubagent,
			sessionvisibility.MetadataSystemManagedParent: parent.SessionID,
			sessionvisibility.MetadataSystemManagedTask:   "task-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: parent.SessionRef,
		Binding: session.ParticipantBinding{
			ID: "child-agent", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated,
			SessionID: child.SessionID, DelegationID: "task-1", AgentName: "orbit", Label: "@orbit",
		},
	}); err != nil {
		t.Fatal(err)
	}
	parentRuntime := activateSessionRuntime(t, host, parent.SessionID)
	childRuntime := activateSessionRuntime(t, host, child.SessionID)
	if childRuntime.stack.hostedChildMailbox == nil {
		t.Fatal("child Session Runtime did not inherit hosted child mailbox")
	}

	runtimeCtx := childRuntime.stack.controlRuntimeContext(ctx, child)
	sender := agentmessage.SenderFromContext(runtimeCtx)
	if sender == nil {
		t.Fatal("hosted child turn has no parent/sibling mailbox")
	}
	if _, err := sender.SendMessage(ctx, agentmessage.Request{
		MessageID: "child-to-parent-1", To: agentmessage.Parent, Text: "child status",
	}); err != nil {
		t.Fatal(err)
	}

	events, err := host.Sessions.Events(ctx, session.EventsRequest{SessionRef: parent.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	var message *session.Event
	for _, event := range events {
		if event != nil && event.MessageID == "child-to-parent-1" {
			message = event
		}
	}
	if message == nil || session.EventTypeOf(message) != session.EventTypeContext || message.Actor.Kind != session.ActorKindParticipant {
		t.Fatalf("parent Session event = %#v, want child-authored Agent message", message)
	}
	gotText := strings.TrimSpace(message.Text)
	if gotText == "" && message.Message != nil {
		gotText = strings.TrimSpace(message.Message.TextContent())
	}
	if gotText != "child status" {
		t.Fatalf("parent message = %#v text %q", message, gotText)
	}
	if parentRuntime.stack.engine == nil {
		t.Fatal("parent Session Runtime engine is unavailable")
	}
	if err := host.Close(); err != nil {
		t.Fatalf("host.Close() = %v", err)
	}
}
