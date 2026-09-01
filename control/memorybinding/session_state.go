package memorybinding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// SessionStateKey is the model-hidden Control state that pins one canonical
// Session to a Memory actor and output audience while retaining its causal
// Recall cursor. It contains no credential, capability, or private text.
const SessionStateKey = "gateway.memory.binding.v1"

// ErrSessionAdmissionConflict identifies an immutable canonical Session actor,
// audience, or binding-version conflict. Callers may safely classify it as a
// rejected concurrent/configuration choice rather than an unknown side effect.
var ErrSessionAdmissionConflict = errors.New("control/memorybinding: canonical Session Memory admission conflict")

type sessionBindingState struct {
	Version          int             `json:"version"`
	EndpointID       string          `json:"endpoint_id"`
	RuntimeActorRef  RuntimeActorRef `json:"runtime_actor_ref"`
	Audience         OutputAudience  `json:"audience"`
	ViewRef          string          `json:"view_ref"`
	BindingVersion   uint64          `json:"binding_version"`
	ConsistencyToken string          `json:"consistency_token,omitempty"`
}

const sessionBindingStateVersion = 1

// AdmitSession atomically fixes one canonical Session to one actor/audience.
// A strictly newer binding may move endpoint/View and resets the causal token;
// a downgrade or same-version drift fails closed.
func AdmitSession(
	ctx context.Context,
	store session.StateStore,
	ref session.SessionRef,
	binding RuntimeMemoryBindingSnapshot,
) error {
	if store == nil || strings.TrimSpace(ref.SessionID) == "" {
		return fmt.Errorf("control/memorybinding: Session state store and reference are required")
	}
	if err := validateRuntimeSnapshot(binding); err != nil {
		return err
	}
	state, err := store.SnapshotState(ctx, ref)
	if err != nil {
		return err
	}
	current, found, err := decodeSessionBindingState(state)
	if err != nil {
		return err
	}
	next := sessionBindingStateFromSnapshot(binding)
	if found {
		next, err = reconcileSessionBinding(current, next)
		if err != nil {
			return err
		}
		if current == next {
			return nil
		}
	}
	_, err = store.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef:    ref,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeConfiguration),
		Update: func(state map[string]any) (map[string]any, error) {
			current, found, err := decodeSessionBindingState(state)
			if err != nil {
				return nil, err
			}
			next := sessionBindingStateFromSnapshot(binding)
			if found {
				next, err = reconcileSessionBinding(current, next)
				if err != nil {
					return nil, err
				}
			}
			return encodeSessionBindingState(state, next), nil
		},
	})
	return err
}

// ValidateSessionAdmission rejects a Runtime actor or audience that conflicts
// with a previously admitted canonical Session without mutating Session state.
// A Session created before Memory was enabled is pinned before its first Memory
// call by PrepareConsistency under the Runtime fence.
func ValidateSessionAdmission(
	ctx context.Context,
	reader session.StateReader,
	ref session.SessionRef,
	binding RuntimeMemoryBindingSnapshot,
) error {
	if reader == nil || strings.TrimSpace(ref.SessionID) == "" {
		return fmt.Errorf("control/memorybinding: Session state reader and reference are required")
	}
	if err := validateRuntimeSnapshot(binding); err != nil {
		return err
	}
	state, err := reader.SnapshotState(ctx, ref)
	if err != nil {
		return err
	}
	current, found, err := decodeSessionBindingState(state)
	if err != nil || !found {
		return err
	}
	_, err = reconcileSessionBinding(current, sessionBindingStateFromSnapshot(binding))
	return err
}

// PrepareConsistency pins or migrates the complete non-secret binding before
// an external Memory call and returns its causal cursor. It runs under the
// canonical Runtime fence so a failed admission cannot follow an external
// effect.
func PrepareConsistency(
	ctx context.Context,
	store session.StateStore,
	ref session.SessionRef,
	binding RuntimeMemoryBindingSnapshot,
) (string, error) {
	if store == nil || strings.TrimSpace(ref.SessionID) == "" {
		return "", fmt.Errorf("control/memorybinding: Session state store and reference are required")
	}
	if err := validateRuntimeSnapshot(binding); err != nil {
		return "", err
	}
	state, err := store.SnapshotState(ctx, ref)
	if err != nil {
		return "", err
	}
	current, found, err := decodeSessionBindingState(state)
	if err != nil {
		return "", err
	}
	next := sessionBindingStateFromSnapshot(binding)
	if found {
		next, err = reconcileSessionBinding(current, next)
		if err != nil {
			return "", err
		}
		if current == next {
			return current.ConsistencyToken, nil
		}
	}
	var prepared sessionBindingState
	_, err = store.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef:    ref,
		MutationGuard: session.RuntimeMutationGuard(ctx),
		Update: func(state map[string]any) (map[string]any, error) {
			current, found, err := decodeSessionBindingState(state)
			if err != nil {
				return nil, err
			}
			prepared = sessionBindingStateFromSnapshot(binding)
			if found {
				prepared, err = reconcileSessionBinding(current, prepared)
				if err != nil {
					return nil, err
				}
			}
			return encodeSessionBindingState(state, prepared), nil
		},
	})
	if err != nil {
		return "", err
	}
	return prepared.ConsistencyToken, nil
}

// ConsistencyToken returns the cursor only when the complete current Runtime
// binding still matches the admitted Session state.
func ConsistencyToken(
	ctx context.Context,
	reader session.StateReader,
	ref session.SessionRef,
	binding RuntimeMemoryBindingSnapshot,
) (string, error) {
	if reader == nil {
		return "", fmt.Errorf("control/memorybinding: Session state reader is required")
	}
	state, err := reader.SnapshotState(ctx, ref)
	if err != nil {
		return "", err
	}
	current, found, err := decodeSessionBindingState(state)
	if err != nil {
		return "", err
	}
	if !found || !sessionBindingMatches(current, binding) {
		return "", fmt.Errorf("control/memorybinding: Runtime Memory binding does not match canonical Session admission")
	}
	return current.ConsistencyToken, nil
}

// AdvanceConsistency durably records a non-authorizing causal cursor under the
// Runtime fence carried by ctx. It never widens actor or audience authority.
func AdvanceConsistency(
	ctx context.Context,
	writer session.StateWriter,
	ref session.SessionRef,
	binding RuntimeMemoryBindingSnapshot,
	token string,
) error {
	if writer == nil {
		return fmt.Errorf("control/memorybinding: Session state writer is required")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	_, err := writer.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef:    ref,
		MutationGuard: session.RuntimeMutationGuard(ctx),
		Update: func(state map[string]any) (map[string]any, error) {
			current, found, err := decodeSessionBindingState(state)
			if err != nil {
				return nil, err
			}
			if !found || !sessionBindingMatches(current, binding) {
				return nil, fmt.Errorf("control/memorybinding: Runtime Memory binding does not match canonical Session admission")
			}
			current.ConsistencyToken = token
			return encodeSessionBindingState(state, current), nil
		},
	})
	return err
}

func sessionBindingStateFromSnapshot(binding RuntimeMemoryBindingSnapshot) sessionBindingState {
	return sessionBindingState{
		Version:         sessionBindingStateVersion,
		EndpointID:      binding.Endpoint.ID,
		RuntimeActorRef: binding.RuntimeActorRef,
		Audience:        binding.Audience,
		ViewRef:         binding.ViewRef,
		BindingVersion:  binding.BindingVersion,
	}
}

func sessionBindingMatches(current sessionBindingState, binding RuntimeMemoryBindingSnapshot) bool {
	return current.Version == sessionBindingStateVersion &&
		current.EndpointID == binding.Endpoint.ID &&
		current.RuntimeActorRef == binding.RuntimeActorRef &&
		current.Audience == binding.Audience &&
		current.ViewRef == binding.ViewRef &&
		current.BindingVersion == binding.BindingVersion
}

func reconcileSessionBinding(current, next sessionBindingState) (sessionBindingState, error) {
	if current.RuntimeActorRef != next.RuntimeActorRef || current.Audience != next.Audience {
		return sessionBindingState{}, fmt.Errorf("%w: actor or audience cannot change", ErrSessionAdmissionConflict)
	}
	if next.BindingVersion < current.BindingVersion {
		return sessionBindingState{}, fmt.Errorf("%w: binding cannot downgrade", ErrSessionAdmissionConflict)
	}
	if next.BindingVersion == current.BindingVersion &&
		(current.EndpointID != next.EndpointID || current.ViewRef != next.ViewRef) {
		return sessionBindingState{}, fmt.Errorf("%w: binding drifted without a version change", ErrSessionAdmissionConflict)
	}
	if current.EndpointID == next.EndpointID && current.ViewRef == next.ViewRef {
		next.ConsistencyToken = current.ConsistencyToken
	}
	return next, nil
}

func validateRuntimeSnapshot(binding RuntimeMemoryBindingSnapshot) error {
	if binding.Endpoint.ID == "" || binding.RuntimeActorRef == "" || binding.Audience == "" ||
		binding.ViewRef == "" || binding.BindingVersion == 0 {
		return fmt.Errorf("control/memorybinding: Runtime Memory binding snapshot is incomplete")
	}
	return nil
}

func decodeSessionBindingState(state map[string]any) (sessionBindingState, bool, error) {
	raw, found := state[SessionStateKey]
	if !found {
		return sessionBindingState{}, false, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return sessionBindingState{}, false, fmt.Errorf("control/memorybinding: encode canonical Session Memory state: %w", err)
	}
	var current sessionBindingState
	if err := json.Unmarshal(data, &current); err != nil {
		return sessionBindingState{}, false, fmt.Errorf("control/memorybinding: decode canonical Session Memory state: %w", err)
	}
	if current.Version != sessionBindingStateVersion || current.EndpointID == "" || current.RuntimeActorRef == "" ||
		!validAudience(current.Audience) || current.ViewRef == "" || current.BindingVersion == 0 {
		return sessionBindingState{}, false, fmt.Errorf("control/memorybinding: canonical Session Memory state is invalid")
	}
	return current, true, nil
}

func encodeSessionBindingState(state map[string]any, binding sessionBindingState) map[string]any {
	if state == nil {
		state = map[string]any{}
	}
	data, _ := json.Marshal(binding)
	var value map[string]any
	_ = json.Unmarshal(data, &value)
	state[SessionStateKey] = value
	return state
}
