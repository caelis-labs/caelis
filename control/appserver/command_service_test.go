package appserver

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/workspacetrust"
)

func TestEveryWriteCommandAuthorizationIdempotencyCASAndUnknownOutcome(t *testing.T) {
	revision := uint64(4)
	epoch := "epoch-1"
	target := TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"}
	cases := []struct {
		name   string
		action Action
		invoke func(*CommandService, Principal, string, bool) (CommandResult, error)
	}{
		{"session create", ActionSessionCreate, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			title := "title"
			if changed {
				title = "changed"
			}
			return s.CreateSession(context.Background(), p, CreateSessionRequest{WriteBase: WriteBase{OperationID: op}, PreferredSessionID: "created-1", WorkspaceKey: "workspace", Title: title})
		}},
		{"session close", ActionSessionClose, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			expected := revision
			if changed {
				expected++
			}
			return s.CloseSession(context.Background(), p, CloseSessionRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &expected}})
		}},
		{"session compact", ActionSessionCompact, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			expected := revision
			if changed {
				expected++
			}
			return s.CompactSession(context.Background(), p, CompactSessionRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &expected, ExpectedControllerEpoch: epoch}})
		}},
		{"prompt", ActionPrompt, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			input := "hello"
			if changed {
				input = "changed"
			}
			return s.Prompt(context.Background(), p, PromptRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch}, Input: input})
		}},
		{"steer", ActionSteer, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			input := "next"
			if changed {
				input = "changed"
			}
			return s.Steer(context.Background(), p, SteerRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch}, Target: target, Input: input})
		}},
		{"cancel", ActionCancel, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			reason := "stop"
			if changed {
				reason = "changed"
			}
			return s.Cancel(context.Background(), p, CancelRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch}, Target: target, Reason: reason})
		}},
		{"approval resolve", ActionApprovalResolve, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			option := "allow_once"
			if changed {
				option = "reject_once"
			}
			return s.ResolveApproval(context.Background(), p, ResolveApprovalRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch}, Target: target, ApprovalRequestID: "approval-1", Outcome: "selected", OptionID: option, Approved: !changed})
		}},
		{"participant attach", ActionParticipantAttach, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			profileID := "acp:reviewer"
			if changed {
				profileID = "acp:changed"
			}
			return s.AttachParticipant(context.Background(), p, AttachParticipantRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch}, ProfileID: profileID, Effort: "high"})
		}},
		{"participant start", ActionParticipantStart, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			input := "review"
			if changed {
				input = "changed"
			}
			return s.StartParticipant(context.Background(), p, StartParticipantRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch}, Handle: "reviewer", Label: "@review", Input: input})
		}},
		{"participant prompt", ActionParticipantPrompt, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			input := "review"
			if changed {
				input = "changed"
			}
			return s.PromptParticipant(context.Background(), p, PromptParticipantRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch}, ParticipantID: "participant-1", Input: input})
		}},
		{"participant cancel", ActionParticipantCancel, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			reason := "stop"
			if changed {
				reason = "changed"
			}
			return s.CancelParticipant(context.Background(), p, CancelParticipantRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch}, ParticipantID: "participant-1", Target: target, Reason: reason})
		}},
		{"participant detach", ActionParticipantDetach, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			source := "client"
			if changed {
				source = "changed"
			}
			return s.DetachParticipant(context.Background(), p, DetachParticipantRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch}, ParticipantID: "participant-1", Source: source})
		}},
		{"handoff", ActionControllerHandoff, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			agent := "codex"
			if changed {
				agent = "changed"
			}
			return s.Handoff(context.Background(), p, HandoffRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch}, Kind: session.ControllerKindACP, Agent: agent})
		}},
		{"session approval mode", ActionSessionApprovalMode, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			mode := "manual"
			if changed {
				mode = "auto-review"
			}
			return s.ConfigureSessionMode(context.Background(), p, SessionModeRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch}, Mode: mode})
		}},
		{"session model", ActionSessionModel, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			model := "mimo"
			if changed {
				model = "gpt-next"
			}
			return s.UseSessionModel(context.Background(), p, SessionModelRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch}, Model: model})
		}},
		{"session model clear", ActionSessionModel, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			request := SessionModelRequest{
				WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch},
				Clear:     true,
			}
			if changed {
				request.Clear = false
				request.Model = "mimo"
			}
			return s.UseSessionModel(context.Background(), p, request)
		}},
		{"session controller mode", ActionSessionControllerMode, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			mode := "code"
			if changed {
				mode = "plan"
			}
			return s.ConfigureSessionControllerMode(context.Background(), p, SessionControllerModeRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch}, Mode: mode})
		}},
		{"session presentation mode", ActionSessionPresentationMode, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			mode := "focus"
			if changed {
				mode = "review"
			}
			return s.ConfigureSessionPresentationMode(context.Background(), p, SessionPresentationModeRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch}, Mode: mode})
		}},
		{"session presentation config", ActionSessionPresentationConfig, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			value := "quiet"
			if changed {
				value = "loud"
			}
			return s.ConfigureSessionPresentation(context.Background(), p, SessionPresentationConfigRequest{WriteBase: WriteBase{OperationID: op, SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch}, ConfigID: "tone", Value: value})
		}},
		{"model connect", ActionModelConnect, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			model := "mimo"
			if changed {
				model = "mimo-next"
			}
			return s.ConnectModel(context.Background(), p, ConnectModelRequest{
				WriteBase: WriteBase{OperationID: op, ExpectedRevision: &revision},
				Config:    ConnectConfig{Provider: "openai", Model: model, APIKey: "never-persist-plaintext"},
			})
		}},
		{"model use", ActionModelUse, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			model := "mimo"
			if changed {
				model = "mimo-next"
			}
			return s.UseModel(context.Background(), p, UseModelRequest{
				WriteBase: WriteBase{OperationID: op, ExpectedRevision: &revision}, Model: model,
			})
		}},
		{"model delete", ActionModelDelete, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			model := "mimo"
			if changed {
				model = "mimo-next"
			}
			return s.DeleteModel(context.Background(), p, DeleteModelRequest{
				WriteBase: WriteBase{OperationID: op, ExpectedRevision: &revision}, Model: model,
			})
		}},
		{"workspace trust", ActionWorkspaceTrust, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			level := workspacetrust.Trusted
			if changed {
				level = workspacetrust.Untrusted
			}
			return s.SetWorkspaceTrust(context.Background(), p, WorkspaceTrustRequest{
				WriteBase:    WriteBase{OperationID: op, ExpectedRevision: &revision},
				WorkspaceKey: "workspace", CWD: "/tmp/workspace", TrustLevel: level,
			})
		}},
		{"sandbox backend", ActionSandboxBackend, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			backend := "host"
			if changed {
				backend = "auto"
			}
			return s.SetSandboxBackend(context.Background(), p, SandboxRequest{WriteBase: WriteBase{OperationID: op, ExpectedRevision: &revision}, Backend: backend})
		}},
		{"sandbox prepare", ActionSandboxPrepare, sandboxLifecycleCommand(ActionSandboxPrepare, revision)},
		{"sandbox repair", ActionSandboxRepair, sandboxLifecycleCommand(ActionSandboxRepair, revision)},
		{"sandbox reset", ActionSandboxReset, sandboxLifecycleCommand(ActionSandboxReset, revision)},
		{"sandbox refresh", ActionSandboxRefresh, sandboxLifecycleCommand(ActionSandboxRefresh, revision)},
		{"Agent binding bind", ActionAgentBindingBind, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			profile := "provider:one"
			if changed {
				profile = "provider:two"
			}
			return s.BindAgentBinding(context.Background(), p, BindAgentBindingRequest{
				WriteBase: WriteBase{OperationID: op, ExpectedRevision: &revision},
				Binding:   agentbinding.Binding{Handle: agentbinding.HandleOrbit, ProfileID: profile, Effort: "high"},
			})
		}},
		{"Agent binding reset", ActionAgentBindingReset, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			handle := agentbinding.HandleOrbit
			if changed {
				handle = agentbinding.HandleZenith
			}
			return s.ResetAgentBinding(context.Background(), p, ResetAgentBindingRequest{
				WriteBase: WriteBase{OperationID: op, ExpectedRevision: &revision}, Handle: handle,
			})
		}},
		{"Agent role create", ActionAgentRoleCreate, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			description := "Research systems."
			if changed {
				description = "Changed."
			}
			return s.CreateAgentRole(context.Background(), p, CreateAgentRoleRequest{
				WriteBase: WriteBase{OperationID: op, ExpectedRevision: &revision},
				Role:      agentbinding.Role{Handle: "research", Description: description},
			})
		}},
		{"Agent role delete", ActionAgentRoleDelete, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			handle := agentbinding.Handle("research")
			if changed {
				handle = "changed"
			}
			return s.DeleteAgentRole(context.Background(), p, DeleteAgentRoleRequest{
				WriteBase: WriteBase{OperationID: op, ExpectedRevision: &revision}, Handle: handle,
			})
		}},
		{"Agent binding set save", ActionAgentBindingSetSave, agentBindingSetCommand(ActionAgentBindingSetSave, revision)},
		{"Agent binding set apply", ActionAgentBindingSetApply, agentBindingSetCommand(ActionAgentBindingSetApply, revision)},
		{"Agent binding set delete", ActionAgentBindingSetDelete, agentBindingSetCommand(ActionAgentBindingSetDelete, revision)},
		{"ACP Agent disconnect", ActionACPAgentDisconnect, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			agentID := "codex"
			if changed {
				agentID = "claude"
			}
			return s.DisconnectACP(context.Background(), p, DisconnectACPRequest{
				WriteBase: WriteBase{OperationID: op, ExpectedRevision: &revision},
				AgentID:   agentID,
			})
		}},
		{"ACP Agent prepare", ActionACPAgentPrepare, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			modelID := "mimo"
			if changed {
				modelID = "mimo-next"
			}
			return s.PrepareACP(context.Background(), p, PrepareACPRequest{
				WriteBase: WriteBase{OperationID: op, ExpectedRevision: &revision},
				Request: controlagents.ACPPrepareRequest{
					AdapterID: "codex", Launcher: controlagents.LauncherChoiceNPX, ModelID: modelID, CWD: "/workspace",
				},
			})
		}},
		{"ACP Agent prepare authentication", ActionACPAgentPrepareAuth, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			methodID := "browser"
			if changed {
				methodID = "terminal"
			}
			return s.PrepareACPAuthentication(context.Background(), p, PrepareACPAuthenticationRequest{
				WriteBase:      WriteBase{OperationID: op, ExpectedRevision: &revision},
				PreparationRef: "prep-1", PreparationDigest: strings.Repeat("a", 64), MethodID: methodID,
			})
		}},
		{"ACP Agent connect", ActionACPAgentConnect, func(s *CommandService, p Principal, op string, changed bool) (CommandResult, error) {
			value := "high"
			if changed {
				value = "max"
			}
			return s.ConnectACP(context.Background(), p, ConnectACPRequest{
				WriteBase:      WriteBase{OperationID: op, ExpectedRevision: &revision},
				PreparationRef: "prep-1", PreparationDigest: strings.Repeat("a", 64),
				ConfigValues: map[string]string{"reasoning_effort": value},
			})
		}},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			principal := Principal{ID: "owner"}
			backend := &recordingCommandBackend{}
			service := newTestCommandService(t, allowAuthorizer{}, NewMemoryOperationStore(), backend)
			first, err := test.invoke(service, principal, "retry-op", false)
			if err != nil || first.Outcome != OutcomeCommitted || backend.calls != 1 {
				t.Fatalf("first = %#v, %v calls=%d", first, err, backend.calls)
			}
			retry, err := test.invoke(service, principal, "retry-op", false)
			if err != nil || retry != first || backend.calls != 1 {
				t.Fatalf("retry = %#v, %v calls=%d, want recorded result", retry, err, backend.calls)
			}
			changed, err := test.invoke(service, principal, "retry-op", true)
			if !errors.Is(err, ErrOperationConflict) || changed.Outcome != OutcomeConflicted || backend.calls != 1 {
				t.Fatalf("changed = %#v, %v calls=%d", changed, err, backend.calls)
			}

			unauthorizedBackend := &recordingCommandBackend{}
			unauthorized := newTestCommandService(t, denyAuthorizer{}, NewMemoryOperationStore(), unauthorizedBackend)
			denied, err := test.invoke(unauthorized, Principal{ID: "other"}, "unauthorized-op", false)
			if !errors.Is(err, ErrUnauthorized) || denied.Outcome != OutcomeRejected || unauthorizedBackend.calls != 0 {
				t.Fatalf("unauthorized = %#v, %v calls=%d", denied, err, unauthorizedBackend.calls)
			}

			conflictBackend := &recordingCommandBackend{err: NewOutcomeError(OutcomeConflicted, errors.New("revision or epoch conflict"))}
			conflicted := newTestCommandService(t, allowAuthorizer{}, NewMemoryOperationStore(), conflictBackend)
			conflict, err := test.invoke(conflicted, principal, "cas-op", false)
			if err == nil || conflict.Outcome != OutcomeConflicted {
				t.Fatalf("CAS conflict = %#v, %v", conflict, err)
			}

			unknownBackend := &recordingCommandBackend{err: NewOutcomeError(OutcomeUnknown, errors.New("effect outcome cannot be proven"))}
			unknownService := newTestCommandService(t, allowAuthorizer{}, NewMemoryOperationStore(), unknownBackend)
			unknown, err := test.invoke(unknownService, principal, "unknown-op", false)
			if err == nil || unknown.Outcome != OutcomeUnknown || unknownBackend.calls != 1 {
				t.Fatalf("unknown = %#v, %v calls=%d", unknown, err, unknownBackend.calls)
			}
			replayed, replayErr := test.invoke(unknownService, principal, "unknown-op", false)
			if replayErr != nil || replayed.Outcome != OutcomeUnknown || unknownBackend.calls != 1 {
				t.Fatalf("unknown retry = %#v, %v calls=%d", replayed, replayErr, unknownBackend.calls)
			}
		})
	}
}

func agentBindingSetCommand(action Action, revision uint64) func(*CommandService, Principal, string, bool) (CommandResult, error) {
	return func(service *CommandService, principal Principal, operationID string, changed bool) (CommandResult, error) {
		name := "default-set"
		if changed {
			name = "changed-set"
		}
		request := AgentBindingSetRequest{
			WriteBase: WriteBase{OperationID: operationID, ExpectedRevision: &revision},
			SetName:   name,
		}
		switch action {
		case ActionAgentBindingSetSave:
			return service.SaveAgentBindingSet(context.Background(), principal, request)
		case ActionAgentBindingSetApply:
			return service.ApplyAgentBindingSet(context.Background(), principal, request)
		case ActionAgentBindingSetDelete:
			return service.DeleteAgentBindingSet(context.Background(), principal, request)
		default:
			panic("unsupported Agent binding set action")
		}
	}
}

func sandboxLifecycleCommand(action Action, revision uint64) func(*CommandService, Principal, string, bool) (CommandResult, error) {
	return func(service *CommandService, principal Principal, operationID string, changed bool) (CommandResult, error) {
		expected := revision
		if changed {
			expected++
		}
		req := SandboxRequest{WriteBase: WriteBase{OperationID: operationID, ExpectedRevision: &expected}}
		switch action {
		case ActionSandboxPrepare:
			return service.PrepareSandbox(context.Background(), principal, req)
		case ActionSandboxRepair:
			return service.RepairSandbox(context.Background(), principal, req)
		case ActionSandboxReset:
			return service.ResetSandbox(context.Background(), principal, req)
		case ActionSandboxRefresh:
			return service.RefreshSandbox(context.Background(), principal, req)
		default:
			return CommandResult{}, errors.New("unsupported sandbox lifecycle test action")
		}
	}
}

func TestSessionAuthorizerRejectsCrossPrincipalSession(t *testing.T) {
	authorizer := SessionAuthorizer{Sessions: fixedOwnerSessionReader{owner: "owner"}}
	if err := authorizer.Authorize(context.Background(), Principal{ID: "owner"}, ActionPrompt, "session-1"); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(context.Background(), Principal{ID: "other"}, ActionPrompt, "session-1"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("cross-principal error = %v", err)
	}
	if err := authorizer.Authorize(context.Background(), Principal{ID: "other", Roles: []string{" ADMIN "}}, ActionPrompt, "session-1"); err != nil {
		t.Fatalf("admin authorization error = %v", err)
	}
}

func TestProductCommandAuthorizerSeparatesHostAndSessionAuthority(t *testing.T) {
	sessions := &recordingAuthorizer{}
	authorizer := ProductCommandAuthorizer{Sessions: sessions}
	for _, action := range []Action{
		ActionModelConnect, ActionModelUse, ActionModelDelete, ActionSandboxReset, ActionWorkspaceTrust, ActionAgentBindingBind,
		ActionACPAgentDisconnect, ActionACPAgentPrepare, ActionACPAgentPrepareAuth, ActionACPAgentConnect,
		ActionPluginMarketplaceAdd, ActionPluginInstall, ActionPluginEnable, ActionPluginRemove,
	} {
		if err := authorizer.Authorize(context.Background(), Principal{ID: "owner"}, action, ""); err != nil {
			t.Fatalf("Authorize(%s) error = %v", action, err)
		}
	}
	if sessions.calls != 0 {
		t.Fatalf("Host configuration delegated to Session authorizer %d time(s)", sessions.calls)
	}
	for _, test := range []struct {
		name      string
		principal Principal
		sessionID string
	}{
		{name: "missing principal"},
		{name: "Session address", principal: Principal{ID: "owner"}, sessionID: "session-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := authorizer.Authorize(context.Background(), test.principal, ActionSandboxReset, test.sessionID); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("Authorize() error = %v, want unauthorized", err)
			}
		})
	}
	if err := authorizer.Authorize(context.Background(), Principal{ID: "owner"}, ActionSessionModel, "session-1"); err != nil {
		t.Fatal(err)
	}
	if sessions.calls != 1 || sessions.action != ActionSessionModel || sessions.sessionID != "session-1" {
		t.Fatalf("Session delegation = %#v", sessions)
	}
}

func TestValidateWorkspaceTrustRequestRequiresHostCASAndExplicitDecision(t *testing.T) {
	revision := uint64(4)
	valid := WorkspaceTrustRequest{
		WriteBase:    WriteBase{OperationID: "workspace-trust", ExpectedRevision: &revision},
		WorkspaceKey: "workspace", CWD: "/tmp/workspace", TrustLevel: workspacetrust.Trusted,
	}
	if err := validateWorkspaceTrustRequest(valid); err != nil {
		t.Fatalf("validateWorkspaceTrustRequest(valid) error = %v", err)
	}
	cases := []WorkspaceTrustRequest{
		{WriteBase: WriteBase{OperationID: "missing-revision"}, WorkspaceKey: "workspace", CWD: "/tmp/workspace", TrustLevel: workspacetrust.Trusted},
		{WriteBase: valid.WriteBase, CWD: "/tmp/workspace", TrustLevel: workspacetrust.Trusted},
		{WriteBase: valid.WriteBase, WorkspaceKey: "workspace", TrustLevel: workspacetrust.Trusted},
		{WriteBase: valid.WriteBase, WorkspaceKey: "workspace", CWD: "/tmp/workspace", TrustLevel: workspacetrust.Unknown},
	}
	for _, request := range cases {
		if err := validateWorkspaceTrustRequest(request); err == nil {
			t.Fatalf("validateWorkspaceTrustRequest(%#v) accepted invalid request", request)
		}
	}
}

func TestHostModelCommandsRejectInvalidScopeBeforeLedger(t *testing.T) {
	revision := uint64(3)
	tests := []struct {
		name   string
		invoke func(*CommandService) (CommandResult, error)
	}{
		{name: "missing revision", invoke: func(service *CommandService) (CommandResult, error) {
			return service.ConnectModel(context.Background(), Principal{ID: "owner"}, ConnectModelRequest{
				WriteBase: WriteBase{OperationID: "missing-revision"}, Config: ConnectConfig{Provider: "openai", Model: "mimo"},
			})
		}},
		{name: "Session address", invoke: func(service *CommandService) (CommandResult, error) {
			return service.UseModel(context.Background(), Principal{ID: "owner"}, UseModelRequest{
				WriteBase: WriteBase{OperationID: "session-address", SessionID: "session-1", ExpectedRevision: &revision}, Model: "mimo",
			})
		}},
		{name: "controller epoch", invoke: func(service *CommandService) (CommandResult, error) {
			return service.DeleteModel(context.Background(), Principal{ID: "owner"}, DeleteModelRequest{
				WriteBase: WriteBase{OperationID: "controller-epoch", ExpectedRevision: &revision, ExpectedControllerEpoch: "epoch-1"}, Model: "mimo",
			})
		}},
		{name: "missing connect model", invoke: func(service *CommandService) (CommandResult, error) {
			return service.ConnectModel(context.Background(), Principal{ID: "owner"}, ConnectModelRequest{
				WriteBase: WriteBase{OperationID: "missing-model", ExpectedRevision: &revision}, Config: ConnectConfig{Provider: "openai"},
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations := &countingOperationStore{OperationStore: NewMemoryOperationStore()}
			backend := &recordingCommandBackend{}
			service := newTestCommandService(t, allowAuthorizer{}, operations, backend)
			result, err := test.invoke(service)
			if errorcode.CodeOf(err) != errorcode.InvalidArgument || result.Outcome != OutcomeRejected {
				t.Fatalf("Host model command = %#v, %v", result, err)
			}
			if operations.beginCalls != 0 || backend.calls != 0 {
				t.Fatalf("invalid request reached ledger/backend: begin=%d backend=%d", operations.beginCalls, backend.calls)
			}
		})
	}
}

func TestSandboxCommandsRejectInvalidHostScopeBeforeLedger(t *testing.T) {
	revision := uint64(3)
	tests := []struct {
		name string
		req  SandboxRequest
	}{
		{name: "missing revision", req: SandboxRequest{WriteBase: WriteBase{OperationID: "missing-revision"}}},
		{name: "Session address", req: SandboxRequest{WriteBase: WriteBase{OperationID: "session-address", SessionID: "session-1", ExpectedRevision: &revision}}},
		{name: "controller epoch", req: SandboxRequest{WriteBase: WriteBase{OperationID: "controller-epoch", ExpectedRevision: &revision, ExpectedControllerEpoch: "epoch-1"}}},
		{name: "lifecycle backend", req: SandboxRequest{WriteBase: WriteBase{OperationID: "lifecycle-backend", ExpectedRevision: &revision}, Backend: "host"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations := &countingOperationStore{OperationStore: NewMemoryOperationStore()}
			backend := &recordingCommandBackend{}
			service := newTestCommandService(t, allowAuthorizer{}, operations, backend)
			result, err := service.ResetSandbox(context.Background(), Principal{ID: "owner"}, test.req)
			if errorcode.CodeOf(err) != errorcode.InvalidArgument || result.Outcome != OutcomeRejected {
				t.Fatalf("ResetSandbox() = %#v, %v", result, err)
			}
			if operations.beginCalls != 0 || backend.calls != 0 {
				t.Fatalf("invalid request reached ledger/backend: begin=%d backend=%d", operations.beginCalls, backend.calls)
			}
		})
	}
}

func TestAgentBindingCommandsRejectInvalidHostScopeBeforeLedger(t *testing.T) {
	revision := uint64(3)
	epoch := "epoch-1"
	tests := []struct {
		name   string
		invoke func(*CommandService) (CommandResult, error)
	}{
		{name: "missing revision", invoke: func(service *CommandService) (CommandResult, error) {
			return service.BindAgentBinding(context.Background(), Principal{ID: "owner"}, BindAgentBindingRequest{
				WriteBase: WriteBase{OperationID: "missing-revision"},
				Binding:   agentbinding.Binding{Handle: agentbinding.HandleOrbit, ProfileID: "provider:one", Effort: "high"},
			})
		}},
		{name: "Session address", invoke: func(service *CommandService) (CommandResult, error) {
			return service.ResetAgentBinding(context.Background(), Principal{ID: "owner"}, ResetAgentBindingRequest{
				WriteBase: WriteBase{OperationID: "session-address", SessionID: "session-1", ExpectedRevision: &revision},
				Handle:    agentbinding.HandleOrbit,
			})
		}},
		{name: "controller epoch", invoke: func(service *CommandService) (CommandResult, error) {
			return service.DeleteAgentRole(context.Background(), Principal{ID: "owner"}, DeleteAgentRoleRequest{
				WriteBase: WriteBase{OperationID: "controller-epoch", ExpectedRevision: &revision, ExpectedControllerEpoch: epoch},
				Handle:    "research",
			})
		}},
		{name: "missing handle", invoke: func(service *CommandService) (CommandResult, error) {
			return service.CreateAgentRole(context.Background(), Principal{ID: "owner"}, CreateAgentRoleRequest{
				WriteBase: WriteBase{OperationID: "missing-handle", ExpectedRevision: &revision},
				Role:      agentbinding.Role{Description: "Research systems."},
			})
		}},
		{name: "missing set", invoke: func(service *CommandService) (CommandResult, error) {
			return service.ApplyAgentBindingSet(context.Background(), Principal{ID: "owner"}, AgentBindingSetRequest{
				WriteBase: WriteBase{OperationID: "missing-set", ExpectedRevision: &revision},
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations := &countingOperationStore{OperationStore: NewMemoryOperationStore()}
			backend := &recordingCommandBackend{}
			service := newTestCommandService(t, allowAuthorizer{}, operations, backend)
			result, err := test.invoke(service)
			if errorcode.CodeOf(err) != errorcode.InvalidArgument || result.Outcome != OutcomeRejected {
				t.Fatalf("Agent binding command = %#v, %v", result, err)
			}
			if operations.beginCalls != 0 || backend.calls != 0 {
				t.Fatalf("invalid request reached ledger/backend: begin=%d backend=%d", operations.beginCalls, backend.calls)
			}
		})
	}
}

func TestACPDisconnectRejectsInvalidHostScopeBeforeLedger(t *testing.T) {
	revision := uint64(3)
	tests := []DisconnectACPRequest{
		{WriteBase: WriteBase{OperationID: "missing-revision"}, AgentID: "codex"},
		{WriteBase: WriteBase{OperationID: "session-address", SessionID: "session-1", ExpectedRevision: &revision}, AgentID: "codex"},
		{WriteBase: WriteBase{OperationID: "controller-epoch", ExpectedRevision: &revision, ExpectedControllerEpoch: "epoch-1"}, AgentID: "codex"},
		{WriteBase: WriteBase{OperationID: "missing-agent", ExpectedRevision: &revision}},
	}
	for _, request := range tests {
		operations := &countingOperationStore{OperationStore: NewMemoryOperationStore()}
		backend := &recordingCommandBackend{}
		service := newTestCommandService(t, allowAuthorizer{}, operations, backend)
		result, err := service.DisconnectACP(context.Background(), Principal{ID: "owner"}, request)
		if errorcode.CodeOf(err) != errorcode.InvalidArgument || result.Outcome != OutcomeRejected {
			t.Fatalf("DisconnectACP(%q) = %#v, %v", request.OperationID, result, err)
		}
		if operations.beginCalls != 0 || backend.calls != 0 {
			t.Fatalf("invalid request reached ledger/backend: begin=%d backend=%d", operations.beginCalls, backend.calls)
		}
	}
}

func TestAgentBindingCommandIntentsUseCanonicalHostTargets(t *testing.T) {
	revision := uint64(9)
	tests := []struct {
		name       string
		wantAction Action
		wantTarget string
		invoke     func(*CommandService) (CommandResult, error)
	}{
		{
			name:       "handle",
			wantAction: ActionAgentBindingBind,
			wantTarget: "host/configuration/agent-bindings/handle/orbit",
			invoke: func(service *CommandService) (CommandResult, error) {
				return service.BindAgentBinding(context.Background(), Principal{ID: "owner"}, BindAgentBindingRequest{
					WriteBase: WriteBase{OperationID: "canonical-handle", ExpectedRevision: &revision},
					Binding:   agentbinding.Binding{Handle: " ORBIT ", ProfileID: "provider:one", Effort: "high"},
				})
			},
		},
		{
			name:       "set",
			wantAction: ActionAgentBindingSetSave,
			wantTarget: "host/configuration/agent-bindings/set/baseline",
			invoke: func(service *CommandService) (CommandResult, error) {
				return service.SaveAgentBindingSet(context.Background(), Principal{ID: "owner"}, AgentBindingSetRequest{
					WriteBase: WriteBase{OperationID: "canonical-set", ExpectedRevision: &revision}, SetName: " Baseline ",
				})
			},
		},
		{
			name:       "ACP disconnect",
			wantAction: ActionACPAgentDisconnect,
			wantTarget: "host/configuration/agents/acp/codex",
			invoke: func(service *CommandService) (CommandResult, error) {
				return service.DisconnectACP(context.Background(), Principal{ID: "owner"}, DisconnectACPRequest{
					WriteBase: WriteBase{OperationID: "canonical-disconnect", ExpectedRevision: &revision}, AgentID: " CoDeX ",
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations := &countingOperationStore{OperationStore: NewMemoryOperationStore()}
			service := newTestCommandService(t, allowAuthorizer{}, operations, &recordingCommandBackend{})
			if _, err := test.invoke(service); err != nil {
				t.Fatal(err)
			}
			if len(operations.intents) != 1 || operations.intents[0].Action != test.wantAction ||
				operations.intents[0].Target != test.wantTarget || operations.intents[0].SessionID != "" {
				t.Fatalf("operation intents = %#v, want %s %q", operations.intents, test.wantAction, test.wantTarget)
			}
		})
	}
}

func TestSessionConfigurationCommandsRejectInvalidScopeBeforeLedger(t *testing.T) {
	revision := uint64(3)
	tests := []struct {
		name   string
		invoke func(*CommandService) (CommandResult, error)
	}{
		{name: "missing Session", invoke: func(service *CommandService) (CommandResult, error) {
			return service.ConfigureSessionMode(context.Background(), Principal{ID: "owner"}, SessionModeRequest{
				WriteBase: WriteBase{OperationID: "missing-session", ExpectedRevision: &revision}, Mode: "manual",
			})
		}},
		{name: "missing revision", invoke: func(service *CommandService) (CommandResult, error) {
			return service.UseSessionModel(context.Background(), Principal{ID: "owner"}, SessionModelRequest{
				WriteBase: WriteBase{OperationID: "missing-revision", SessionID: "session-1"}, Model: "mimo",
			})
		}},
		{name: "clear with model", invoke: func(service *CommandService) (CommandResult, error) {
			return service.UseSessionModel(context.Background(), Principal{ID: "owner"}, SessionModelRequest{
				WriteBase: WriteBase{OperationID: "clear-with-model", SessionID: "session-1", ExpectedRevision: &revision},
				Model:     "mimo",
				Clear:     true,
			})
		}},
		{name: "missing controller epoch", invoke: func(service *CommandService) (CommandResult, error) {
			return service.ConfigureSessionControllerMode(context.Background(), Principal{ID: "owner"}, SessionControllerModeRequest{
				WriteBase: WriteBase{OperationID: "missing-epoch", SessionID: "session-1", ExpectedRevision: &revision}, Mode: "code",
			})
		}},
		{name: "empty presentation config value", invoke: func(service *CommandService) (CommandResult, error) {
			return service.ConfigureSessionPresentation(context.Background(), Principal{ID: "owner"}, SessionPresentationConfigRequest{
				WriteBase: WriteBase{OperationID: "empty-value", SessionID: "session-1", ExpectedRevision: &revision}, ConfigID: "tone",
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations := &countingOperationStore{OperationStore: NewMemoryOperationStore()}
			backend := &recordingCommandBackend{}
			service := newTestCommandService(t, allowAuthorizer{}, operations, backend)
			result, err := test.invoke(service)
			if errorcode.CodeOf(err) != errorcode.InvalidArgument || result.Outcome != OutcomeRejected {
				t.Fatalf("Session configuration = %#v, %v", result, err)
			}
			if operations.beginCalls != 0 || backend.calls != 0 {
				t.Fatalf("invalid request reached ledger/backend: begin=%d backend=%d", operations.beginCalls, backend.calls)
			}
		})
	}
}

func TestHandoffRejectsMissingFenceAndInvalidTargetBeforeLedger(t *testing.T) {
	revision := uint64(3)
	tests := []struct {
		name string
		req  HandoffRequest
	}{
		{name: "missing revision", req: HandoffRequest{
			WriteBase: WriteBase{OperationID: "missing-revision", SessionID: "session-1", ExpectedControllerEpoch: "epoch-1"},
			Kind:      session.ControllerKindACP, Agent: "orbit",
		}},
		{name: "ACP without Agent", req: HandoffRequest{
			WriteBase: WriteBase{OperationID: "missing-agent", SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: "epoch-1"},
			Kind:      session.ControllerKindACP,
		}},
		{name: "Kernel with Agent", req: HandoffRequest{
			WriteBase: WriteBase{OperationID: "kernel-agent", SessionID: "session-1", ExpectedRevision: &revision, ExpectedControllerEpoch: "epoch-1"},
			Kind:      session.ControllerKindKernel, Agent: "orbit",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations := &countingOperationStore{OperationStore: NewMemoryOperationStore()}
			backend := &recordingCommandBackend{}
			service := newTestCommandService(t, allowAuthorizer{}, operations, backend)
			result, err := service.Handoff(context.Background(), Principal{ID: "owner"}, test.req)
			if errorcode.CodeOf(err) != errorcode.InvalidArgument || result.Outcome != OutcomeRejected {
				t.Fatalf("Handoff() = %#v, %v", result, err)
			}
			if operations.beginCalls != 0 || backend.calls != 0 {
				t.Fatalf("invalid handoff reached ledger/backend: begin=%d backend=%d", operations.beginCalls, backend.calls)
			}
		})
	}
}

func TestPrincipalHasRoleRejectsEmptyRole(t *testing.T) {
	if (Principal{Roles: []string{""}}).HasRole(" ") {
		t.Fatal("empty role matched")
	}
}

func TestAttachParticipantRequiresProfileAndEffort(t *testing.T) {
	for _, test := range []struct {
		name      string
		profileID string
		effort    string
	}{
		{name: "profile", effort: "high"},
		{name: "effort", profileID: "acp:helper"},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &recordingCommandBackend{}
			service := newTestCommandService(t, allowAuthorizer{}, NewMemoryOperationStore(), backend)
			result, err := service.AttachParticipant(context.Background(), Principal{ID: "owner"}, AttachParticipantRequest{
				WriteBase: WriteBase{OperationID: "attach-" + test.name, SessionID: "session-1"},
				ProfileID: test.profileID,
				Effort:    test.effort,
			})
			if errorcode.CodeOf(err) != errorcode.InvalidArgument || result.Outcome != OutcomeRejected || backend.calls != 0 {
				t.Fatalf("AttachParticipant() = %#v, %v, calls=%d", result, err, backend.calls)
			}
		})
	}
}

func TestSessionAuthorizerDoesNotHideSessionStoreFailureAsPermissionDenied(t *testing.T) {
	storeErr := errors.New("disk checksum failure")
	authorizer := SessionAuthorizer{Sessions: faultingSessionReader{err: storeErr}}
	err := authorizer.Authorize(context.Background(), Principal{ID: "owner"}, ActionSessionInspect, "session-1")
	if errorcode.CodeOf(err) != errorcode.Internal || errors.Is(err, ErrUnauthorized) || !errors.Is(err, storeErr) {
		t.Fatalf("Authorize() error = %v (code %q), want retained internal store failure", err, errorcode.CodeOf(err))
	}

	authorizer.Sessions = faultingSessionReader{err: session.ErrSessionNotFound}
	if err := authorizer.Authorize(context.Background(), Principal{ID: "owner"}, ActionSessionInspect, "missing"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing session error = %v, want permission denied", err)
	}
}

func TestCommandServicePersistsOnlyStablePublicFailureDetail(t *testing.T) {
	backendErr := NewOutcomeError(OutcomeUnknown, errors.New("secret storage path /private/ledger"))
	operations := NewMemoryOperationStore()
	backend := &recordingCommandBackend{err: backendErr}
	service := newTestCommandService(t, allowAuthorizer{}, operations, backend)
	req := PromptRequest{
		WriteBase: WriteBase{OperationID: "unknown-op", SessionID: "session-1"},
		Input:     "hello",
	}
	first, err := service.Prompt(context.Background(), Principal{ID: "owner"}, req)
	if err == nil || first.Outcome != OutcomeUnknown || first.Detail != "effect outcome cannot be proven" {
		t.Fatalf("Prompt() = %#v, %v", first, err)
	}
	replayed, err := service.Prompt(context.Background(), Principal{ID: "owner"}, req)
	if err != nil || replayed != first || strings.Contains(replayed.Detail, "/private/ledger") {
		t.Fatalf("Prompt(replay) = %#v, %v", replayed, err)
	}
}

func TestCommandServicePreservesTurnTargetWhenReceiptWriteFails(t *testing.T) {
	t.Parallel()

	target := TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"}
	operations := &failFirstCompleteStore{OperationStore: NewMemoryOperationStore(), failLeft: 1}
	backend := &promptTargetBackend{target: target}
	service := newTestCommandService(t, allowAuthorizer{}, operations, backend)
	result, err := service.Prompt(context.Background(), Principal{ID: "owner"}, PromptRequest{
		WriteBase: WriteBase{OperationID: "prompt-op", SessionID: "session-1"},
		Input:     "hello",
	})
	if err == nil || result.Outcome != OutcomeUnknown {
		t.Fatalf("Prompt() = %#v, %v; want unknown receipt-write failure", result, err)
	}
	if result.Target != target || result.ParticipantID != "participant-1" {
		t.Fatalf("Prompt() identity = %#v, want preserved backend target", result)
	}
}

func TestFileOperationStoreSurvivesRestartAndBindsPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations")
	intent := OperationIntent{PrincipalID: "owner", OperationID: "op-1", Action: ActionPrompt, SessionID: "session-1", Target: "session-1", Digest: "digest-a"}
	first := NewFileOperationStore(path)
	if _, created, err := first.Begin(context.Background(), intent); err != nil || !created {
		t.Fatalf("Begin = created %v, %v", created, err)
	}
	want := CommandResult{OperationID: "op-1", Outcome: OutcomeCommitted, SessionID: "session-1", Revision: 2}
	if _, err := first.Complete(context.Background(), intent, want); err != nil {
		t.Fatal(err)
	}
	second := NewFileOperationStore(path)
	record, created, err := second.Begin(context.Background(), intent)
	if err != nil || created || record.Result == nil || *record.Result != want {
		t.Fatalf("restart record = %#v created=%v err=%v", record, created, err)
	}
	changed := intent
	changed.Digest = "digest-b"
	if _, _, err := second.Begin(context.Background(), changed); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("changed payload error = %v", err)
	}
}

func TestCommandServicePersistsKnownEffectResultAfterRequestCancellation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	ctx, cancel := context.WithCancel(context.Background())
	backend := &cancelAfterCommitBackend{cancel: cancel}
	service := newTestCommandService(t, allowAuthorizer{}, NewFileOperationStore(root), backend)
	principal := Principal{ID: "owner"}
	req := PromptRequest{
		WriteBase: WriteBase{OperationID: "committed-before-cancel", SessionID: "session-1"},
		Input:     "hello",
	}

	want, err := service.Prompt(ctx, principal, req)
	if err != nil || want.Outcome != OutcomeCommitted || want.Revision != 11 {
		t.Fatalf("Prompt() = %#v, %v; want known committed result", want, err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("request context error = %v, want cancelled by committed backend", ctx.Err())
	}

	replayBackend := &recordingCommandBackend{}
	reopened := newTestCommandService(t, allowAuthorizer{}, NewFileOperationStore(root), replayBackend)
	got, err := reopened.Prompt(context.Background(), principal, req)
	if err != nil || got != want {
		t.Fatalf("Prompt(retry) = %#v, %v; want durable %#v", got, err, want)
	}
	if replayBackend.calls != 0 || backend.calls != 1 {
		t.Fatalf("backend calls = original %d replay %d, want 1 and 0", backend.calls, replayBackend.calls)
	}
}

func TestCommandServicePersistsUnclassifiedBackendErrorAsUnknown(t *testing.T) {
	backend := &recordingCommandBackend{err: errors.New("effect may have committed before transport failure")}
	service := newTestCommandService(t, allowAuthorizer{}, NewMemoryOperationStore(), backend)
	principal := Principal{ID: "owner"}
	req := PromptRequest{
		WriteBase: WriteBase{OperationID: "unclassified-error", SessionID: "session-1"},
		Input:     "hello",
	}
	first, err := service.Prompt(context.Background(), principal, req)
	if err == nil || first.Outcome != OutcomeUnknown || backend.calls != 1 {
		t.Fatalf("Prompt() = %#v, %v, calls %d", first, err, backend.calls)
	}
	replayed, replayErr := service.Prompt(context.Background(), principal, req)
	if replayErr != nil || replayed != first || backend.calls != 1 {
		t.Fatalf("Prompt(retry) = %#v, %v, calls %d", replayed, replayErr, backend.calls)
	}
}

func TestCommandServiceRecoversIntentOnlyResultWithoutRepeatingEffect(t *testing.T) {
	operations := NewMemoryOperationStore()
	request := PromptRequest{
		WriteBase: WriteBase{OperationID: "recover-intent", SessionID: "session-1"},
		Input:     "hello",
	}
	digest, err := requestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	intent := OperationIntent{
		PrincipalID: "owner", OperationID: request.OperationID, Action: ActionPrompt,
		SessionID: request.SessionID, Target: request.SessionID, Digest: digest,
	}
	if _, created, err := operations.Begin(context.Background(), intent); err != nil || !created {
		t.Fatalf("Begin() = created %v, %v", created, err)
	}
	backend := &recoveringCommandBackend{result: CommandResult{
		Outcome: OutcomeCommitted, Revision: 7,
		Resource: &CommandResource{Kind: "test", Ref: "resource-1", Digest: "digest-1"},
	}}
	service := newTestCommandService(t, allowAuthorizer{}, operations, backend)

	recovered, err := service.Prompt(context.Background(), Principal{ID: "owner"}, request)
	if err != nil || recovered.Outcome != OutcomeCommitted || recovered.OperationID != request.OperationID || recovered.Resource == nil || recovered.Resource.Ref != "resource-1" {
		t.Fatalf("Prompt(recover) = %#v, %v", recovered, err)
	}
	if backend.executeCalls != 0 || backend.recoverCalls != 1 || backend.intent != intent {
		t.Fatalf("backend calls/intent = execute %d recover %d %#v", backend.executeCalls, backend.recoverCalls, backend.intent)
	}
	replayed, err := service.Prompt(context.Background(), Principal{ID: "owner"}, request)
	if err != nil || !sameCommandResult(replayed, recovered) || backend.recoverCalls != 1 || backend.executeCalls != 0 {
		t.Fatalf("Prompt(replay) = %#v, %v calls=%d/%d", replayed, err, backend.executeCalls, backend.recoverCalls)
	}
}

type allowAuthorizer struct{}

func (allowAuthorizer) Authorize(context.Context, Principal, Action, string) error {
	return nil
}

type denyAuthorizer struct{}

func (denyAuthorizer) Authorize(context.Context, Principal, Action, string) error {
	return ErrUnauthorized
}

type recordingAuthorizer struct {
	calls     int
	action    Action
	sessionID string
}

type countingOperationStore struct {
	OperationStore
	beginCalls int
	intents    []OperationIntent
}

func (s *countingOperationStore) Begin(ctx context.Context, intent OperationIntent) (OperationRecord, bool, error) {
	s.beginCalls++
	s.intents = append(s.intents, intent)
	return s.OperationStore.Begin(ctx, intent)
}

func (a *recordingAuthorizer) Authorize(_ context.Context, _ Principal, action Action, sessionID string) error {
	a.calls++
	a.action = action
	a.sessionID = sessionID
	return nil
}

type failFirstCompleteStore struct {
	OperationStore
	failLeft int
}

func (s *failFirstCompleteStore) Complete(ctx context.Context, intent OperationIntent, result CommandResult) (OperationRecord, error) {
	if s.failLeft > 0 {
		s.failLeft--
		return OperationRecord{}, errors.New("disk full")
	}
	return s.OperationStore.Complete(ctx, intent, result)
}

type promptTargetBackend struct {
	target TurnTarget
	cancel CancelRequest
	calls  int
}

func (b *promptTargetBackend) ExecuteControlCommand(_ context.Context, _ Principal, action Action, request any) (CommandResult, error) {
	b.calls++
	switch action {
	case ActionPrompt:
		return CommandResult{
			Outcome:       OutcomeCommitted,
			Revision:      8,
			SessionID:     "session-1",
			Target:        b.target,
			ParticipantID: "participant-1",
		}, nil
	case ActionCancel:
		b.cancel = request.(CancelRequest)
		return CommandResult{Outcome: OutcomeCommitted, SessionID: "session-1", Target: b.target}, nil
	default:
		return CommandResult{Outcome: OutcomeCommitted}, nil
	}
}

type recordingCommandBackend struct {
	calls int
	err   error
}

type cancelAfterCommitBackend struct {
	cancel context.CancelFunc
	calls  int
}

type recoveringCommandBackend struct {
	result       CommandResult
	intent       OperationIntent
	executeCalls int
	recoverCalls int
}

func (b *recoveringCommandBackend) CanRecoverControlCommand(Action) bool {
	return true
}

func (b *recoveringCommandBackend) ExecuteControlCommand(context.Context, Principal, Action, any) (CommandResult, error) {
	b.executeCalls++
	return CommandResult{}, errors.New("effect must not be repeated during recovery")
}

func (b *recoveringCommandBackend) RecoverControlCommand(_ context.Context, _ Principal, intent OperationIntent, _ any) (CommandResult, bool, error) {
	b.recoverCalls++
	b.intent = intent
	return b.result, true, nil
}

func (b *cancelAfterCommitBackend) ExecuteControlCommand(_ context.Context, _ Principal, _ Action, _ any) (CommandResult, error) {
	b.calls++
	if b.cancel != nil {
		b.cancel()
	}
	return CommandResult{Outcome: OutcomeCommitted, Revision: 11}, nil
}

func (b *recordingCommandBackend) ExecuteControlCommand(_ context.Context, _ Principal, _ Action, request any) (CommandResult, error) {
	b.calls++
	if operationIDOf(request) == "" {
		return CommandResult{}, errors.New("operation id was not forwarded")
	}
	return CommandResult{Outcome: OutcomeCommitted, Revision: 5}, b.err
}

func operationIDOf(request any) string {
	switch req := request.(type) {
	case CreateSessionRequest:
		return req.OperationID
	case CloseSessionRequest:
		return req.OperationID
	case CompactSessionRequest:
		return req.OperationID
	case PromptRequest:
		return req.OperationID
	case SteerRequest:
		return req.OperationID
	case CancelRequest:
		return req.OperationID
	case ResolveApprovalRequest:
		return req.OperationID
	case AttachParticipantRequest:
		return req.OperationID
	case StartParticipantRequest:
		return req.OperationID
	case PromptParticipantRequest:
		return req.OperationID
	case CancelParticipantRequest:
		return req.OperationID
	case DetachParticipantRequest:
		return req.OperationID
	case HandoffRequest:
		return req.OperationID
	case SessionModeRequest:
		return req.OperationID
	case SessionModelRequest:
		return req.OperationID
	case SessionControllerModeRequest:
		return req.OperationID
	case SessionPresentationModeRequest:
		return req.OperationID
	case SessionPresentationConfigRequest:
		return req.OperationID
	case ConnectModelRequest:
		return req.OperationID
	case UseModelRequest:
		return req.OperationID
	case DeleteModelRequest:
		return req.OperationID
	case SandboxRequest:
		return req.OperationID
	case WorkspaceTrustRequest:
		return req.OperationID
	case BindAgentBindingRequest:
		return req.OperationID
	case ResetAgentBindingRequest:
		return req.OperationID
	case CreateAgentRoleRequest:
		return req.OperationID
	case DeleteAgentRoleRequest:
		return req.OperationID
	case AgentBindingSetRequest:
		return req.OperationID
	case DisconnectACPRequest:
		return req.OperationID
	case PrepareACPRequest:
		return req.OperationID
	case PrepareACPAuthenticationRequest:
		return req.OperationID
	case ConnectACPRequest:
		return req.OperationID
	case AddMarketplaceRequest:
		return req.OperationID
	case UpdateMarketplaceRequest:
		return req.OperationID
	case RemoveMarketplaceRequest:
		return req.OperationID
	case AddPluginPathRequest:
		return req.OperationID
	case InstallPluginRequest:
		return req.OperationID
	case EnablePluginRequest:
		return req.OperationID
	case DisablePluginRequest:
		return req.OperationID
	case RemovePluginRequest:
		return req.OperationID
	default:
		return ""
	}
}

func newTestCommandService(t *testing.T, authorizer Authorizer, operations OperationStore, backend CommandBackend) *CommandService {
	t.Helper()
	service, err := NewCommandService(CommandServiceConfig{Authorizer: authorizer, Operations: operations, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type fixedOwnerSessionReader struct{ owner string }

func (r fixedOwnerSessionReader) Session(_ context.Context, ref session.SessionRef) (session.Session, error) {
	return session.Session{SessionRef: session.SessionRef{SessionID: ref.SessionID, UserID: r.owner}}, nil
}

func (fixedOwnerSessionReader) SnapshotState(context.Context, session.SessionRef) (map[string]any, error) {
	return nil, nil
}

type faultingSessionReader struct{ err error }

func (r faultingSessionReader) Session(context.Context, session.SessionRef) (session.Session, error) {
	return session.Session{}, r.err
}
