package appserver

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

func applicationTestProfile() application.Profile {
	return application.Profile{Version: "role/1", Instructions: "Only application instructions.", Model: "model", ToolsVersion: "tools/1", Execution: "tools-only"}
}
func applicationTestStore(t *testing.T) (*application.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	store, err := application.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}
func enrollTestApplication(t *testing.T, store *application.Store, owner, op, token string) Principal {
	t.Helper()
	conn, err := store.Register(t.Context(), owner, application.Registration{OperationID: op, Name: op, Credential: "app-client-" + strings.Repeat(token, 64)})
	if err != nil {
		t.Fatal(err)
	}
	return Principal{ID: owner, ApplicationID: conn.ApplicationID, ConnectionID: conn.ConnectionID}
}

func TestApplicationAuthorizerUsesDurableScopeNotMetadata(t *testing.T) {
	store, _ := applicationTestStore(t)
	a := enrollTestApplication(t, store, "owner", "a", "a")
	b := enrollTestApplication(t, store, "owner", "b", "b")
	sessions := inmemory.NewStore(inmemory.Config{})
	scope, _ := ApplicationScope(a)
	for _, id := range []string{"owned", "forged", "ordinary", "retired"} {
		metadata := map[string]any{}
		if id == "owned" || id == "forged" {
			metadata[sessionvisibility.MetadataSystemManagedAgent] = application.MetadataKind
			metadata[application.StateKey] = a.ApplicationID
		}
		if id == "retired" {
			metadata[sessionvisibility.MetadataSystemManagedAgent] = "bot"
		}
		_, err := sessions.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "owner", PreferredSessionID: id, Metadata: metadata})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutBinding(t.Context(), application.Binding{Scope: scope, SessionID: "owned", Profile: applicationTestProfile(), CreationDigest: "digest"}); err != nil {
		t.Fatal(err)
	}
	authorizer := SessionAuthorizer{Sessions: sessions, Applications: store}
	tests := []struct {
		name    string
		p       Principal
		action  Action
		id      string
		allowed bool
	}{
		{"own-read", a, ActionSessionInspect, "owned", true}, {"own-prompt", a, ActionApplicationPrompt, "owned", true},
		{"other-app", b, ActionSessionInspect, "owned", false}, {"metadata-forgery", a, ActionApplicationPrompt, "forged", false},
		{"ordinary-id", a, ActionSessionInspect, "ordinary", false}, {"ordinary-prompt-path", a, ActionPrompt, "owned", false},
		{"create-ordinary", a, ActionSessionCreate, "", false}, {"create-app", a, ActionApplicationCreate, "", true},
		{"admin-not-app", Principal{ID: "owner", Roles: []string{"admin"}}, ActionPrompt, "owned", false},
		{"ordinary-retained", Principal{ID: "owner"}, ActionPrompt, "ordinary", true},
		{"internal-observer", Principal{ID: "owner", Roles: []string{RoleSystemSessionRuntime}}, ActionSessionInspect, "owned", true},
		{"participant-escape", a, ActionParticipantStart, "owned", false}, {"steer-unsupported", a, ActionSteer, "owned", false},
		{"retired-mode", Principal{ID: "owner"}, ActionPrompt, "retired", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := authorizer.Authorize(t.Context(), test.p, test.action, test.id)
			if (err == nil) != test.allowed {
				t.Fatalf("allowed=%v err=%v", test.allowed, err)
			}
		})
	}
	if err := store.Revoke(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(t.Context(), a, ActionApplicationPrompt, "owned"); !errors.Is(err, application.ErrRevoked) {
		t.Fatalf("revoked prompt=%v", err)
	}
	if err := authorizer.Authorize(t.Context(), a, ActionSessionInspect, "owned"); err != nil {
		t.Fatalf("revoked receipt observation=%v", err)
	}
	if err := (ProductCommandAuthorizer{Sessions: authorizer}).Authorize(t.Context(), a, ActionModelDelete, ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("host config escape=%v", err)
	}
}

type applicationBoundaryBackend struct {
	effects     int
	afterEffect func()
	result      CommandResult
}

func (b *applicationBoundaryBackend) ExecuteControlCommand(context.Context, Principal, Action, any) (CommandResult, error) {
	b.effects++
	if b.afterEffect != nil {
		b.afterEffect()
	}
	return b.result, nil
}

func TestApplicationPermanentIntentFaultBoundaries(t *testing.T) {
	for _, boundary := range []string{"intent-before-dispatch", "effect-before-receipt", "receipt-before-response"} {
		t.Run(boundary, func(t *testing.T) {
			store, path := applicationTestStore(t)
			p := enrollTestApplication(t, store, "owner", "enroll", "c")
			scope, _ := ApplicationScope(p)
			req := CreateApplicationSessionRequest{WriteBase: WriteBase{OperationID: "create"}, Profile: applicationTestProfile()}
			backend := &applicationBoundaryBackend{result: CommandResult{Outcome: OutcomeCommitted, SessionID: "native-session", Target: TurnTarget{RunID: "run", TurnID: "turn"}}}
			commands := newTestCommandService(t, allowAuthorizer{}, NewMemoryOperationStore(), backend)
			svc, err := NewApplicationService(ApplicationServiceConfig{Store: store, Commands: commands, Sessions: &Client{}})
			if err != nil {
				t.Fatal(err)
			}
			if boundary == "intent-before-dispatch" {
				if _, fresh, err := store.BeginOperation(t.Context(), scope, req.OperationID, req); err != nil || !fresh {
					t.Fatalf("intent=%v %v", fresh, err)
				}
			} else {
				if boundary == "effect-before-receipt" {
					backend.afterEffect = func() {
						if err := store.Close(); err != nil {
							t.Fatal(err)
						}
					}
				}
				out, err := svc.Create(t.Context(), p, req)
				if boundary == "effect-before-receipt" {
					if errorcode.CodeOf(err) != errorcode.UnknownOutcome || out.Outcome != OutcomeUnknown {
						t.Fatalf("lost receipt=%+v %v", out, err)
					}
				} else if err != nil || out.Outcome != OutcomeCommitted {
					t.Fatalf("receipt=%+v %v", out, err)
				}
			}
			_ = store.Close()
			store, err = application.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			svc.config.Store = store
			// Shared command receipts can disappear. The permanent anchor alone must
			// forbid another effect, including after an abandoned intent-only boundary.
			commands.config.Operations = NewMemoryOperationStore()
			backend.afterEffect = nil
			result, err := svc.Create(t.Context(), p, req)
			if err != nil {
				t.Fatal(err)
			}
			wantEffects := 1
			wantOutcome := OutcomeUnknown
			if boundary == "intent-before-dispatch" {
				wantEffects = 0
			}
			if boundary == "receipt-before-response" {
				wantOutcome = OutcomeCommitted
			}
			if backend.effects != wantEffects || result.Outcome != wantOutcome {
				t.Fatalf("effects=%d outcome=%s", backend.effects, result.Outcome)
			}
			op, err := svc.Operation(t.Context(), p, req.OperationID)
			if err != nil || op.Outcome != wantOutcome {
				t.Fatalf("operation=%+v %v", op, err)
			}
			changed := req
			changed.Profile.Instructions = "changed"
			if _, err := svc.Create(t.Context(), p, changed); !errors.Is(err, application.ErrConflict) {
				t.Fatalf("changed request=%v", err)
			}
		})
	}
}

type applicationRecoverySessions struct {
	Service
	state SessionState
	reads int
}

func (s *applicationRecoverySessions) InspectSession(_ context.Context, _ Principal, req StateRequest) (SessionState, error) {
	s.reads++
	if req.SessionID != s.state.SessionID {
		return SessionState{}, application.ErrNotFound
	}
	return s.state, nil
}

func TestApplicationCreationReconcilesCanonicalSessionWithoutRedispatch(t *testing.T) {
	for _, validDigest := range []bool{true, false} {
		t.Run(map[bool]string{true: "matching-binding", false: "conflicting-binding"}[validDigest], func(t *testing.T) {
			store, path := applicationTestStore(t)
			p := enrollTestApplication(t, store, "owner", "enroll", "d")
			scope, _ := ApplicationScope(p)
			req := CreateApplicationSessionRequest{WriteBase: WriteBase{OperationID: "lost-create-receipt"}, Profile: applicationTestProfile()}
			op, fresh, err := store.BeginOperation(t.Context(), scope, req.OperationID, req)
			if err != nil || !fresh {
				t.Fatalf("intent=%v %v", fresh, err)
			}
			id := application.SessionID(scope, req.OperationID)
			digest := op.Digest
			if !validDigest {
				digest = "another-request"
			}
			if err = store.PutBinding(t.Context(), application.Binding{Scope: scope, SessionID: id, Profile: req.Profile, CreationDigest: digest}); err != nil {
				t.Fatal(err)
			}
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = application.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			backend := &applicationBoundaryBackend{}
			sessions := &applicationRecoverySessions{state: SessionState{SessionID: id, Revision: 7}}
			commands := newTestCommandService(t, allowAuthorizer{}, NewMemoryOperationStore(), backend)
			svc, err := NewApplicationService(ApplicationServiceConfig{Store: store, Commands: commands, Sessions: sessions})
			if err != nil {
				t.Fatal(err)
			}
			observed, err := svc.Operation(t.Context(), p, req.OperationID)
			if !validDigest {
				if !errors.Is(err, application.ErrConflict) || sessions.reads != 0 {
					t.Fatalf("conflicting binding: outcome=%+v reads=%d err=%v", observed, sessions.reads, err)
				}
				return
			}
			if err != nil || observed.Outcome != OutcomeCommitted || observed.Result == nil || observed.Result.SessionID != id || observed.Result.Revision != 7 {
				t.Fatalf("reconciled=%+v err=%v", observed, err)
			}
			repeated, err := svc.Create(t.Context(), p, req)
			if err != nil || repeated.Outcome != OutcomeCommitted || repeated.SessionID != id || backend.effects != 0 {
				t.Fatalf("repeat=%+v effects=%d err=%v", repeated, backend.effects, err)
			}
		})
	}
}

func TestApplicationSourceKindsAndImmutableProfileValidation(t *testing.T) {
	for _, kind := range []string{"user", "application_summary", "external_material"} {
		if err := validateApplicationPrompt(ApplicationPromptRequest{PromptRequest: PromptRequest{WriteBase: WriteBase{OperationID: "op", SessionID: "session"}, Input: "data"}, SourceKind: kind}); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	for _, kind := range []string{"", "background", "userApproved", "assistant", "reminder"} {
		if err := validateApplicationPrompt(ApplicationPromptRequest{PromptRequest: PromptRequest{WriteBase: WriteBase{OperationID: "op", SessionID: "session"}, Input: "data"}, SourceKind: kind}); errorcode.CodeOf(err) != errorcode.InvalidArgument {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}
