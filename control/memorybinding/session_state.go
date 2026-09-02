package memorybinding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	memoryv1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
)

// SessionStateKey is the model-hidden Control state that pins one canonical
// Session to a complete non-secret Memory delegation while retaining its
// causal Recall cursor. It contains no credential, capability, or private text.
const SessionStateKey = "gateway.memory.binding.v1"

// ErrSessionAdmissionConflict identifies an immutable canonical Session
// delegation or binding-version conflict. Callers may safely classify it as a
// rejected concurrent/configuration choice rather than an unknown side effect.
var ErrSessionAdmissionConflict = errors.New("control/memorybinding: canonical Session Memory admission conflict")

type sessionBindingState struct {
	Version             int                     `json:"version"`
	BindingRef          BindingRef              `json:"binding_ref"`
	RuntimeActorRef     RuntimeActorRef         `json:"runtime_actor_ref"`
	PrincipalRef        string                  `json:"principal_ref"`
	IssuerCredentialRef string                  `json:"issuer_credential_ref"`
	Audience            OutputAudience          `json:"audience"`
	ViewRef             string                  `json:"view_ref"`
	GrantRef            string                  `json:"grant_ref"`
	BindingVersion      uint64                  `json:"binding_version"`
	Labels              memoryv1alpha1.LabelSet `json:"labels,omitempty"`
	ConsistencyToken    string                  `json:"consistency_token,omitempty"`
}

const (
	legacyEndpointBindingStateVersion = 2
	legacyLogicalBindingStateVersion  = 3
	sessionBindingStateVersion        = 4
)

// AdmitSession atomically fixes one canonical Session to one complete
// non-secret delegation. A strictly newer binding may rotate its versioned
// View/Grant/issuer references; a downgrade, identity change, or
// same-version drift fails closed.
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
		if sessionBindingStateEqual(current, next) {
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

// ValidateSessionAdmission rejects a Runtime delegation that conflicts with a
// previously admitted canonical Session without mutating Session state.
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
		if sessionBindingStateEqual(current, next) {
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
		Version:             sessionBindingStateVersion,
		BindingRef:          binding.BindingRef,
		RuntimeActorRef:     binding.RuntimeActorRef,
		PrincipalRef:        binding.PrincipalRef,
		IssuerCredentialRef: binding.IssuerCredentialRef,
		Audience:            binding.Audience,
		ViewRef:             binding.ViewRef,
		GrantRef:            binding.GrantRef,
		BindingVersion:      binding.BindingVersion,
		Labels:              append(memoryv1alpha1.LabelSet{}, binding.Labels...),
	}
}

func sessionBindingMatches(current sessionBindingState, binding RuntimeMemoryBindingSnapshot) bool {
	return current.Version == sessionBindingStateVersion &&
		current.BindingRef == binding.BindingRef &&
		current.RuntimeActorRef == binding.RuntimeActorRef &&
		current.PrincipalRef == binding.PrincipalRef &&
		current.IssuerCredentialRef == binding.IssuerCredentialRef &&
		current.Audience == binding.Audience &&
		current.ViewRef == binding.ViewRef &&
		current.GrantRef == binding.GrantRef &&
		current.BindingVersion == binding.BindingVersion &&
		slices.Equal(current.Labels, binding.Labels)
}

func sessionBindingStateEqual(left, right sessionBindingState) bool {
	return left.Version == right.Version &&
		left.BindingRef == right.BindingRef &&
		left.RuntimeActorRef == right.RuntimeActorRef &&
		left.PrincipalRef == right.PrincipalRef &&
		left.IssuerCredentialRef == right.IssuerCredentialRef &&
		left.Audience == right.Audience &&
		left.ViewRef == right.ViewRef &&
		left.GrantRef == right.GrantRef &&
		left.BindingVersion == right.BindingVersion &&
		slices.Equal(left.Labels, right.Labels) &&
		left.ConsistencyToken == right.ConsistencyToken
}

func reconcileSessionBinding(current, next sessionBindingState) (sessionBindingState, error) {
	if current.BindingRef != next.BindingRef || current.RuntimeActorRef != next.RuntimeActorRef ||
		current.PrincipalRef != next.PrincipalRef || current.Audience != next.Audience {
		return sessionBindingState{}, fmt.Errorf("%w: binding, actor, principal, or audience cannot change", ErrSessionAdmissionConflict)
	}
	if next.BindingVersion < current.BindingVersion {
		return sessionBindingState{}, fmt.Errorf("%w: binding cannot downgrade", ErrSessionAdmissionConflict)
	}
	if next.BindingVersion == current.BindingVersion &&
		(current.ViewRef != next.ViewRef || current.GrantRef != next.GrantRef ||
			current.IssuerCredentialRef != next.IssuerCredentialRef) {
		return sessionBindingState{}, fmt.Errorf("%w: binding drifted without a version change", ErrSessionAdmissionConflict)
	}
	if current.Version == legacyLogicalBindingStateVersion && next.Version == sessionBindingStateVersion {
		// Version 3 predates capability-bound LabelSets. The first post-upgrade
		// admission fixes the current Runtime labels and clears the old cursor,
		// which belongs to the former empty partition.
		return next, nil
	}
	if current.Version != next.Version || !slices.Equal(current.Labels, next.Labels) {
		return sessionBindingState{}, fmt.Errorf("%w: Runtime labels cannot change", ErrSessionAdmissionConflict)
	}
	if current.ViewRef == next.ViewRef {
		next.ConsistencyToken = current.ConsistencyToken
	}
	return next, nil
}

func validateRuntimeSnapshot(binding RuntimeMemoryBindingSnapshot) error {
	if binding.BindingRef == "" || binding.RuntimeActorRef == "" ||
		binding.PrincipalRef == "" || binding.IssuerCredentialRef == "" || binding.Audience == "" ||
		binding.ViewRef == "" || binding.GrantRef == "" || binding.BindingVersion == 0 {
		return fmt.Errorf("control/memorybinding: Runtime Memory binding snapshot is incomplete")
	}
	if !validRuntimeLabels(binding.Labels) {
		return fmt.Errorf("control/memorybinding: Runtime Memory labels are invalid or non-canonical")
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
	if current.Version == legacyEndpointBindingStateVersion {
		current.Version = legacyLogicalBindingStateVersion
	}
	if (current.Version != legacyLogicalBindingStateVersion && current.Version != sessionBindingStateVersion) ||
		current.BindingRef == "" ||
		current.RuntimeActorRef == "" || current.PrincipalRef == "" || current.IssuerCredentialRef == "" ||
		!validAudience(current.Audience) || current.ViewRef == "" || current.GrantRef == "" || current.BindingVersion == 0 {
		return sessionBindingState{}, false, fmt.Errorf("control/memorybinding: canonical Session Memory state is invalid")
	}
	if current.Version == sessionBindingStateVersion && !validRuntimeLabels(current.Labels) {
		return sessionBindingState{}, false, fmt.Errorf("control/memorybinding: canonical Session Memory labels are invalid")
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
