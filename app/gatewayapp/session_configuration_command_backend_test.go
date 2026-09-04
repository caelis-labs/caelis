package gatewayapp

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestSessionConfigurationCommandUsesObservedRevisionAndSharedLedger(t *testing.T) {
	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	active = mustCurrentSession(t, stack, active.SessionID)
	revision := active.Revision
	request := appserver.SessionModeRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "session-mode-ledger",
			SessionID:               active.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Mode: "manual",
	}

	first, err := stack.ConfigurationCommands().ConfigureSessionMode(ctx, principal, request)
	if err != nil || first.Outcome != appserver.OutcomeCommitted || first.Revision != revision+1 {
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
	if !errors.Is(err, session.ErrRevisionConflict) || conflicted.Outcome != appserver.OutcomeConflicted || conflicted.Revision != current.Revision {
		t.Fatalf("ConfigureSessionMode(stale) = %#v, %v", conflicted, err)
	}
}

func TestSessionModelCommandDoesNotChangeHostDefaultOrConfigurationRevision(t *testing.T) {
	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	active = mustCurrentSession(t, stack, active.SessionID)
	hostRevision, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hostDefault := stack.composition.lookup.DefaultID()
	revision := active.Revision

	result, err := stack.ConfigurationCommands().UseSessionModel(ctx, principal, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "session-model-only",
			SessionID:               active.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Model: hostDefault,
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != revision+1 {
		t.Fatalf("UseSessionModel() = %#v, %v", result, err)
	}
	if got := stack.composition.lookup.DefaultID(); got != hostDefault {
		t.Fatalf("Host default = %q, want unchanged %q", got, hostDefault)
	}
	if got, err := stack.ControlStatus().ConfigurationRevision(ctx); err != nil || got != hostRevision {
		t.Fatalf("Host configuration revision = %d, %v; want %d", got, err, hostRevision)
	}
	state, err := stack.composition.sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := kernel.CurrentModelAlias(state); got != hostDefault {
		t.Fatalf("Session model = %q, want %q", got, hostDefault)
	}
}

func TestSessionModelCommandPersistsExplicitFastMode(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	hostRevision, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	connected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "session-fast-connect", ExpectedRevision: &hostRevision},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "gpt-5.6-sol", BaseURL: "https://api.openai.com/v1", APIKey: "session-fast-secret",
			ReasoningEffort: "xhigh", ReasoningLevels: []string{"low", "high", "xhigh"},
		},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel() = %#v, %v", connected, err)
	}

	active = mustCurrentSession(t, stack, active.SessionID)
	revision := active.Revision
	selected, err := stack.ConfigurationCommands().UseSessionModel(ctx, principal, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID: "session-fast-select", SessionID: active.SessionID, ExpectedRevision: &revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Model: "openai/gpt-5.6-sol", ReasoningEffort: "xhigh", FastMode: true,
	})
	if err != nil || selected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("UseSessionModel(fast) = %#v, %v", selected, err)
	}
	state, err := stack.composition.sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if fast, ok := kernel.CurrentModelFastMode(state); !ok || !fast {
		t.Fatalf("persisted fast mode = %v, %v; want explicit true in %#v", fast, ok, state)
	}
	runtimeState, err := stack.ControlStatus().SessionRuntimeState(ctx, active.SessionRef)
	if err != nil || !runtimeState.FastMode {
		t.Fatalf("SessionRuntimeState() = %#v, %v; want fast mode", runtimeState, err)
	}

	active = mustCurrentSession(t, stack, active.SessionID)
	revision = active.Revision
	standard, err := stack.ConfigurationCommands().UseSessionModel(ctx, principal, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID: "session-fast-disable", SessionID: active.SessionID, ExpectedRevision: &revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Model: "openai/gpt-5.6-sol", ReasoningEffort: "xhigh",
	})
	if err != nil || standard.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("UseSessionModel(default speed) = %#v, %v", standard, err)
	}
	state, err = stack.composition.sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if fast, ok := kernel.CurrentModelFastMode(state); !ok || fast {
		t.Fatalf("persisted fast mode = %v, %v; want explicit false in %#v", fast, ok, state)
	}

	active = mustCurrentSession(t, stack, active.SessionID)
	revision = active.Revision
	rejected, err := stack.ConfigurationCommands().UseSessionModel(ctx, principal, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID: "session-fast-unsupported", SessionID: active.SessionID, ExpectedRevision: &revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Model: "ollama/llama3", ReasoningEffort: "none", FastMode: true,
	})
	if err == nil || rejected.Outcome != appserver.OutcomeRejected {
		t.Fatalf("UseSessionModel(unsupported fast) = %#v, %v", rejected, err)
	}
}

func TestSessionModelCommandCanClearLocalSelection(t *testing.T) {
	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	active = mustCurrentSession(t, stack, active.SessionID)
	revision := active.Revision

	result, err := stack.ConfigurationCommands().UseSessionModel(ctx, principal, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "session-model-clear",
			SessionID:               active.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Clear: true,
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != revision+1 {
		t.Fatalf("UseSessionModel(clear) = %#v, %v", result, err)
	}
	state, err := stack.composition.sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := kernel.CurrentModelAlias(state); got != "" {
		t.Fatalf("Session model after clear = %q, want empty", got)
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
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	active = mustCurrentSession(t, stack, active.SessionID)

	revision := active.Revision
	mode, err := stack.ConfigurationCommands().ConfigureSessionPresentationMode(ctx, principal, appserver.SessionPresentationModeRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "presentation-mode-focus",
			SessionID:               active.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Mode: "focus",
	})
	if err != nil || mode.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConfigureSessionPresentationMode() = %#v, %v", mode, err)
	}

	active = mustCurrentSession(t, stack, active.SessionID)
	revision = active.Revision
	config, err := stack.ConfigurationCommands().ConfigureSessionPresentation(ctx, principal, appserver.SessionPresentationConfigRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "presentation-config-tone",
			SessionID:               active.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		ConfigID: "tone",
		Value:    "loud",
	})
	if err != nil || config.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConfigureSessionPresentation() = %#v, %v", config, err)
	}

	active = mustCurrentSession(t, stack, active.SessionID)
	revision = active.Revision
	approval, err := stack.ConfigurationCommands().ConfigureSessionMode(ctx, principal, appserver.SessionModeRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "approval-mode-manual",
			SessionID:               active.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Mode: "manual",
	})
	if err != nil || approval.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConfigureSessionMode() = %#v, %v", approval, err)
	}

	state, err := stack.composition.sessions.SnapshotState(ctx, active.SessionRef)
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
			var outcomeErr *appserver.OutcomeError
			if !errors.As(uncommitted, &outcomeErr) || outcomeErr.Outcome != appserver.OutcomeUnknown {
				t.Fatalf("classifyACPSelectionStateError(uncommitted) = %v, want unknown outcome", uncommitted)
			}
		})
	}
}

func mustCurrentSession(t *testing.T, stack *Stack, sessionID string) session.Session {
	t.Helper()
	active, err := stack.composition.sessions.Session(context.Background(), session.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	return active
}
