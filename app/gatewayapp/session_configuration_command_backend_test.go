package gatewayapp

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestSessionConfigurationCommandUsesObservedRevisionAndSharedLedger(t *testing.T) {
	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	principal := controlclient.Principal{ID: stack.UserID}
	active = mustCurrentSession(t, stack, active.SessionID)
	revision := active.Revision
	request := controlclient.SessionModeRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:             "session-mode-ledger",
			SessionID:               active.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Mode: "manual",
	}

	first, err := stack.ConfigurationCommands().ConfigureSessionMode(ctx, principal, request)
	if err != nil || first.Outcome != controlclient.OutcomeCommitted || first.Revision != revision+1 {
		t.Fatalf("ConfigureSessionMode() = %#v, %v", first, err)
	}
	replayed, err := stack.ConfigurationCommands().ConfigureSessionMode(ctx, principal, request)
	if err != nil || replayed != first {
		t.Fatalf("ConfigureSessionMode(replay) = %#v, %v; want %#v", replayed, err, first)
	}
	current := mustCurrentSession(t, stack, active.SessionID)
	if current.Revision != first.Revision {
		t.Fatalf("replay revision = %d, want %d", current.Revision, first.Revision)
	}

	stale := request
	stale.OperationID = "session-mode-stale"
	stale.Mode = "auto-review"
	conflicted, err := stack.ConfigurationCommands().ConfigureSessionMode(ctx, principal, stale)
	if !errors.Is(err, session.ErrRevisionConflict) || conflicted.Outcome != controlclient.OutcomeConflicted || conflicted.Revision != current.Revision {
		t.Fatalf("ConfigureSessionMode(stale) = %#v, %v", conflicted, err)
	}
}

func TestSessionModelCommandDoesNotChangeHostDefaultOrConfigurationRevision(t *testing.T) {
	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	principal := controlclient.Principal{ID: stack.UserID}
	active = mustCurrentSession(t, stack, active.SessionID)
	hostRevision, err := stack.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hostDefault := stack.lookup.DefaultID()
	revision := active.Revision

	result, err := stack.ConfigurationCommands().UseSessionModel(ctx, principal, controlclient.SessionModelRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:             "session-model-only",
			SessionID:               active.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Model: hostDefault,
	})
	if err != nil || result.Outcome != controlclient.OutcomeCommitted || result.Revision != revision+1 {
		t.Fatalf("UseSessionModel() = %#v, %v", result, err)
	}
	if got := stack.lookup.DefaultID(); got != hostDefault {
		t.Fatalf("Host default = %q, want unchanged %q", got, hostDefault)
	}
	if got, err := stack.ConfigurationRevision(ctx); err != nil || got != hostRevision {
		t.Fatalf("Host configuration revision = %d, %v; want %d", got, err, hostRevision)
	}
	state, err := stack.Sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := kernel.CurrentModelAlias(state); got != hostDefault {
		t.Fatalf("Session model = %q, want %q", got, hostDefault)
	}
}

func TestSessionPresentationAndApprovalCommandsKeepIndependentStateKeys(t *testing.T) {
	ctx := context.Background()
	stack, active := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{
		Modes: []assembly.ModeConfig{{ID: "focus", Name: "Focus"}},
		Configs: []assembly.ConfigOption{{
			ID: "tone", Name: "Tone", DefaultValue: "quiet",
			Options: []assembly.ConfigSelectOption{{Value: "quiet", Name: "Quiet"}, {Value: "loud", Name: "Loud"}},
		}},
	})
	principal := controlclient.Principal{ID: stack.UserID}
	active = mustCurrentSession(t, stack, active.SessionID)

	revision := active.Revision
	mode, err := stack.ConfigurationCommands().ConfigureSessionPresentationMode(ctx, principal, controlclient.SessionPresentationModeRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:             "presentation-mode-focus",
			SessionID:               active.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Mode: "focus",
	})
	if err != nil || mode.Outcome != controlclient.OutcomeCommitted {
		t.Fatalf("ConfigureSessionPresentationMode() = %#v, %v", mode, err)
	}

	active = mustCurrentSession(t, stack, active.SessionID)
	revision = active.Revision
	config, err := stack.ConfigurationCommands().ConfigureSessionPresentation(ctx, principal, controlclient.SessionPresentationConfigRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:             "presentation-config-tone",
			SessionID:               active.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		ConfigID: "tone",
		Value:    "loud",
	})
	if err != nil || config.Outcome != controlclient.OutcomeCommitted {
		t.Fatalf("ConfigureSessionPresentation() = %#v, %v", config, err)
	}

	active = mustCurrentSession(t, stack, active.SessionID)
	revision = active.Revision
	approval, err := stack.ConfigurationCommands().ConfigureSessionMode(ctx, principal, controlclient.SessionModeRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:             "approval-mode-manual",
			SessionID:               active.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Mode: "manual",
	})
	if err != nil || approval.Outcome != controlclient.OutcomeCommitted {
		t.Fatalf("ConfigureSessionMode() = %#v, %v", approval, err)
	}

	state, err := stack.Sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := assembly.CurrentModeID(state); got != "focus" {
		t.Fatalf("presentation mode = %q, want focus", got)
	}
	if got := assembly.CurrentConfigValues(state)["tone"]; got != "loud" {
		t.Fatalf("presentation tone = %q, want loud", got)
	}
	if got := kernel.CurrentSessionModeOrDefault(state, "auto-review"); got != "manual" {
		t.Fatalf("approval mode = %q, want manual", got)
	}
}

func TestACPSelectionStateErrorTreatsPostCommitFailureAsCommitted(t *testing.T) {
	fault := errors.New("post-commit reporting failure")
	for _, selection := range []string{"mode", "model"} {
		t.Run(selection, func(t *testing.T) {
			if err := classifyACPSelectionStateError(selection, &session.CommittedError{Err: fault}); err != nil {
				t.Fatalf("classifyACPSelectionStateError(committed) = %v, want nil", err)
			}
			uncommitted := classifyACPSelectionStateError(selection, fault)
			var outcomeErr *controlclient.OutcomeError
			if !errors.As(uncommitted, &outcomeErr) || outcomeErr.Outcome != controlclient.OutcomeUnknown {
				t.Fatalf("classifyACPSelectionStateError(uncommitted) = %v, want unknown outcome", uncommitted)
			}
		})
	}
}

func mustCurrentSession(t *testing.T, stack *Stack, sessionID string) session.Session {
	t.Helper()
	active, err := stack.Sessions.Session(context.Background(), session.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	return active
}
