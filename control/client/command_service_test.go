package controlclient

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
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

type allowAuthorizer struct{}

func (allowAuthorizer) Authorize(context.Context, Principal, Action, string) error {
	return nil
}

type denyAuthorizer struct{}

func (denyAuthorizer) Authorize(context.Context, Principal, Action, string) error {
	return ErrUnauthorized
}

type recordingCommandBackend struct {
	calls int
	err   error
}

type cancelAfterCommitBackend struct {
	cancel context.CancelFunc
	calls  int
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
	case PromptParticipantRequest:
		return req.OperationID
	case CancelParticipantRequest:
		return req.OperationID
	case DetachParticipantRequest:
		return req.OperationID
	case HandoffRequest:
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
