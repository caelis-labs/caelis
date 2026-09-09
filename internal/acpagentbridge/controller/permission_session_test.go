package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
)

func TestControllerPermissionHandlerFailClosedBeforeRequester(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		boundID   string
		requestID string
		wantErr   bool
	}{
		{name: "unknown before remote bind", requestID: "remote-1", wantErr: true},
		{name: "mismatch", boundID: "remote-1", requestID: "other", wantErr: true},
		{name: "empty request", boundID: "remote-1", wantErr: true},
		{name: "whitespace mismatch", boundID: "remote-1", requestID: " remote-1 ", wantErr: true},
		{name: "valid", boundID: "remote-1", requestID: "remote-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requester := &recordingControllerApprovalRequester{}
			run := &controllerRun{
				remoteSessionID:   test.boundID,
				agent:             "helper",
				approvalRequester: requester,
			}
			response, err := run.permissionHandler(context.Background(), testPermissionRequest(test.requestID))
			if test.wantErr {
				if !errors.Is(err, errACPPermissionSessionMismatchEndpoint) {
					t.Fatalf("controller permission error = %v, want bound-endpoint mismatch", err)
				}
				if requester.calls != 0 {
					t.Fatalf("approval requester calls = %d, want zero", requester.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("controller permission error = %v", err)
			}
			if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "allow_once" {
				t.Fatalf("controller permission response = %#v, want selected allow_once", response)
			}
			if requester.calls != 1 || requester.last.EndpointSessionID != "remote-1" {
				t.Fatalf("approval request = %#v, want bound EndpointSessionID", requester.last)
			}
		})
	}
}

func TestParticipantPermissionHandlerFailClosedBeforeRequester(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		boundID   string
		requestID string
		wantErr   bool
	}{
		{name: "unknown before remote bind", requestID: "remote-participant", wantErr: true},
		{name: "mismatch", boundID: "remote-participant", requestID: "other", wantErr: true},
		{name: "empty request", boundID: "remote-participant", wantErr: true},
		{name: "whitespace mismatch", boundID: "remote-participant", requestID: " remote-participant ", wantErr: true},
		{name: "valid", boundID: "remote-participant", requestID: "remote-participant"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requester := &recordingControllerApprovalRequester{}
			run := &participantRun{
				remoteSessionID:   test.boundID,
				agent:             "helper",
				approvalRequester: requester,
			}
			response, err := run.permissionHandler(context.Background(), testPermissionRequest(test.requestID))
			if test.wantErr {
				if !errors.Is(err, errACPPermissionSessionMismatchEndpoint) {
					t.Fatalf("participant permission error = %v, want bound-endpoint mismatch", err)
				}
				if requester.calls != 0 {
					t.Fatalf("approval requester calls = %d, want zero", requester.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("participant permission error = %v", err)
			}
			if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "allow_once" {
				t.Fatalf("participant permission response = %#v, want selected allow_once", response)
			}
			if requester.calls != 1 || requester.last.EndpointSessionID != "remote-participant" {
				t.Fatalf("approval request = %#v, want bound EndpointSessionID", requester.last)
			}
		})
	}
}

func TestControllerActivateAndReconnectPermissionSessionBinding(t *testing.T) {
	t.Parallel()

	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{
		Name:    "helper",
		Command: "helper-acp",
	}})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Config{Registry: registry, Clock: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	requester := &recordingControllerApprovalRequester{}
	var startProbes []permissionStartProbe
	manager.startClient = func(
		_ context.Context,
		_ string,
		_ subagent.AgentConfig,
		resumeRemoteSessionID string,
		_ func(client.UpdateEnvelope),
		onPermission func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		remoteID := "remote-session"
		if resumeRemoteSessionID != "" {
			remoteID = resumeRemoteSessionID
		}
		startProbes = append(startProbes, probePermissionEntry(onPermission, remoteID))
		return nil, remoteID, controllerClientState{}, nil
	}

	parentSession := session.Session{
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws"},
		CWD:        t.TempDir(),
	}
	if _, err := manager.Activate(context.Background(), controller.HandoffRequest{
		Session: parentSession,
		Agent:   "helper",
		Source:  "test",
	}); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if len(startProbes) != 1 {
		t.Fatalf("Activate start probes = %d, want 1", len(startProbes))
	}
	if !errors.Is(startProbes[0].validErr, errACPPermissionSessionMismatchEndpoint) ||
		!errors.Is(startProbes[0].mismatchErr, errACPPermissionSessionMismatchEndpoint) ||
		!errors.Is(startProbes[0].emptyErr, errACPPermissionSessionMismatchEndpoint) {
		t.Fatalf("Activate permission before bind = %#v, want fail closed", startProbes[0])
	}
	if requester.calls != 0 {
		t.Fatalf("Activate requester calls = %d, want zero before bind", requester.calls)
	}

	manager.mu.RLock()
	run := manager.controllers[parentSession.SessionID]
	manager.mu.RUnlock()
	if run == nil {
		t.Fatal("controller run is unavailable after Activate")
	}
	run.mu.Lock()
	run.approvalRequester = requester
	run.mu.Unlock()

	if _, err := run.permissionHandler(context.Background(), testPermissionRequest("remote-session")); err != nil {
		t.Fatalf("bound controller permission error = %v", err)
	}
	if requester.calls != 1 || requester.last.EndpointSessionID != "remote-session" {
		t.Fatalf("bound controller approval = %#v, want remote-session", requester.last)
	}
	if _, err := run.permissionHandler(context.Background(), testPermissionRequest("other")); !errors.Is(err, errACPPermissionSessionMismatchEndpoint) {
		t.Fatalf("mismatch after bind = %v, want bound-endpoint mismatch", err)
	}
	if _, err := run.permissionHandler(context.Background(), testPermissionRequest("")); !errors.Is(err, errACPPermissionSessionMismatchEndpoint) {
		t.Fatalf("empty request after bind = %v, want bound-endpoint mismatch", err)
	}
	if requester.calls != 1 {
		t.Fatalf("requester calls after mismatch/empty = %d, want 1", requester.calls)
	}

	if _, err := manager.reconnectControllerRun(context.Background(), run); err != nil {
		t.Fatalf("reconnectControllerRun() error = %v", err)
	}
	if len(startProbes) != 2 {
		t.Fatalf("reconnect start probes = %d, want 2", len(startProbes))
	}
	if startProbes[1].validErr != nil {
		t.Fatalf("reconnect valid permission error = %v", startProbes[1].validErr)
	}
	if !errors.Is(startProbes[1].mismatchErr, errACPPermissionSessionMismatchEndpoint) {
		t.Fatalf("reconnect mismatch = %v, want bound-endpoint mismatch", startProbes[1].mismatchErr)
	}
	if !errors.Is(startProbes[1].emptyErr, errACPPermissionSessionMismatchEndpoint) {
		t.Fatalf("reconnect empty request = %v, want bound-endpoint mismatch", startProbes[1].emptyErr)
	}
	if requester.calls != 2 || requester.last.EndpointSessionID != "remote-session" {
		t.Fatalf("reconnect approval = %#v calls=%d, want bound remote-session", requester.last, requester.calls)
	}
}

func TestParticipantAttachAndReloadPermissionSessionBinding(t *testing.T) {
	t.Parallel()

	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{
		Name:    "helper",
		Command: "helper-acp",
	}})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Config{Registry: registry, Clock: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	requester := &recordingControllerApprovalRequester{}
	var startProbes []permissionStartProbe
	manager.startClient = func(
		_ context.Context,
		_ string,
		_ subagent.AgentConfig,
		resumeRemoteSessionID string,
		_ func(client.UpdateEnvelope),
		onPermission func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		remoteID := "remote-participant"
		if resumeRemoteSessionID != "" {
			remoteID = resumeRemoteSessionID
		}
		startProbes = append(startProbes, probePermissionEntry(onPermission, remoteID))
		return nil, remoteID, controllerClientState{}, nil
	}

	parentSession := session.Session{
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws"},
		CWD:        t.TempDir(),
	}
	binding, err := manager.Attach(context.Background(), controller.AttachRequest{
		Session:   parentSession,
		Agent:     "helper",
		Label:     "helper",
		Placement: mustParticipantPlacement(t, "helper"),
	})
	if err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	if len(startProbes) != 1 ||
		!errors.Is(startProbes[0].validErr, errACPPermissionSessionMismatchEndpoint) ||
		!errors.Is(startProbes[0].mismatchErr, errACPPermissionSessionMismatchEndpoint) ||
		!errors.Is(startProbes[0].emptyErr, errACPPermissionSessionMismatchEndpoint) ||
		requester.calls != 0 {
		t.Fatalf("Attach permission before run bind = %#v calls=%d, want fail closed", startProbes[0], requester.calls)
	}

	run := manager.participants[participantKey(parentSession.SessionID, binding.ID)]
	if run == nil {
		t.Fatal("participant run is unavailable after Attach")
	}
	run.mu.Lock()
	run.approvalRequester = requester
	run.mu.Unlock()
	if _, err := run.permissionHandler(context.Background(), testPermissionRequest("remote-participant")); err != nil {
		t.Fatalf("bound participant permission error = %v", err)
	}
	if requester.calls != 1 || requester.last.EndpointSessionID != "remote-participant" {
		t.Fatalf("bound participant approval = %#v, want remote-participant", requester.last)
	}

	if err := manager.Detach(context.Background(), controller.DetachRequest{
		Session: parentSession, ParticipantID: binding.ID, DelegationID: binding.DelegationID,
		AttachmentGeneration: binding.AttachmentGeneration,
	}); err != nil {
		t.Fatalf("Detach() error = %v", err)
	}

	reloaded, err := manager.Attach(context.Background(), controller.AttachRequest{
		Session: parentSession,
		Agent:   "helper",
		Binding: session.ParticipantBinding{
			ID:        "helper-reload",
			Kind:      session.ParticipantKindACP,
			Role:      session.ParticipantRoleSidecar,
			AgentName: "helper",
			Label:     "helper",
			Placement: mustParticipantPlacement(t, "helper"),
			SessionID: "remote-reload",
		},
	})
	if err != nil {
		t.Fatalf("Attach(reload) error = %v", err)
	}
	if len(startProbes) != 2 ||
		!errors.Is(startProbes[1].validErr, errACPPermissionSessionMismatchEndpoint) ||
		!errors.Is(startProbes[1].mismatchErr, errACPPermissionSessionMismatchEndpoint) ||
		!errors.Is(startProbes[1].emptyErr, errACPPermissionSessionMismatchEndpoint) {
		t.Fatalf("reload permission before run bind = %#v, want fail closed", startProbes[1])
	}
	reloadedRun := manager.participants[participantKey(parentSession.SessionID, reloaded.ID)]
	if reloadedRun == nil {
		t.Fatal("participant run is unavailable after reload")
	}
	reloadedRun.mu.Lock()
	reloadedRun.approvalRequester = requester
	reloadedRun.mu.Unlock()
	if _, err := reloadedRun.permissionHandler(context.Background(), testPermissionRequest("remote-reload")); err != nil {
		t.Fatalf("reloaded participant permission error = %v", err)
	}
	if requester.last.EndpointSessionID != "remote-reload" {
		t.Fatalf("reloaded approval EndpointSessionID = %q, want remote-reload", requester.last.EndpointSessionID)
	}
	if _, err := reloadedRun.permissionHandler(context.Background(), testPermissionRequest("other")); !errors.Is(err, errACPPermissionSessionMismatchEndpoint) {
		t.Fatalf("reloaded mismatch = %v, want bound-endpoint mismatch", err)
	}
	if _, err := reloadedRun.permissionHandler(context.Background(), testPermissionRequest("")); !errors.Is(err, errACPPermissionSessionMismatchEndpoint) {
		t.Fatalf("reloaded empty request = %v, want bound-endpoint mismatch", err)
	}
}

type recordingControllerApprovalRequester struct {
	calls int
	last  controller.ApprovalRequest
}

func (r *recordingControllerApprovalRequester) RequestControllerApproval(_ context.Context, req controller.ApprovalRequest) (controller.ApprovalResponse, error) {
	r.calls++
	r.last = req
	return controller.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
}

type permissionStartProbe struct {
	validErr    error
	mismatchErr error
	emptyErr    error
}

func probePermissionEntry(
	onPermission func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	remoteID string,
) permissionStartProbe {
	_, validErr := onPermission(context.Background(), testPermissionRequest(remoteID))
	_, mismatchErr := onPermission(context.Background(), testPermissionRequest("other"))
	_, emptyErr := onPermission(context.Background(), testPermissionRequest(""))
	return permissionStartProbe{
		validErr:    validErr,
		mismatchErr: mismatchErr,
		emptyErr:    emptyErr,
	}
}

func testPermissionRequest(sessionID string) client.RequestPermissionRequest {
	kind := acpsdk.ToolKindExecute
	status := acpsdk.ToolCallStatusPending
	title := "Run command"
	return client.RequestPermissionRequest{
		SessionId: acpsdk.SessionId(sessionID),
		ToolCall: acpsdk.ToolCallUpdate{
			ToolCallId: "call-1",
			Kind:       &kind,
			Title:      &title,
			Status:     &status,
			RawInput:   map[string]any{"command": "pwd"},
		},
		Options: []acpsdk.PermissionOption{{
			OptionId: "allow_once",
			Name:     "Allow once",
			Kind:     acpsdk.PermissionOptionKindAllowOnce,
		}},
	}
}
