package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/ports/gateway"
)

// SetSessionMode persists one per-session approval routing mode override for
// subsequent turns and returns the normalized display label.
func (s *Stack) SetSessionMode(ctx context.Context, ref session.SessionRef, mode string) (string, error) {
	if s == nil || s.Sessions == nil {
		return "", fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	if err := s.rejectReconfigureWhileActive("change session mode"); err != nil {
		return "", err
	}
	normalized, err := normalizeSessionMode(mode)
	if err != nil {
		return "", err
	}
	_, err = s.updateSessionState(ctx, ref, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[gateway.StateCurrentApprovalMode] = normalized
		return next, nil
	})
	if err != nil {
		return "", err
	}
	return normalized, nil
}

func (s *Stack) updateSessionState(
	ctx context.Context,
	ref session.SessionRef,
	update func(map[string]any) (map[string]any, error),
) (session.Session, error) {
	current, err := s.Sessions.Session(ctx, ref)
	if err != nil {
		return session.Session{}, err
	}
	return s.Sessions.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef:       ref,
		ExpectedRevision: &current.Revision,
		MutationGuard:    session.ControlMutationGuard(session.ControlMutationPurposeConfiguration),
		Update:           update,
	})
}

func (s *Stack) replaceSessionState(ctx context.Context, ref session.SessionRef, state map[string]any) (session.Session, error) {
	current, err := s.Sessions.Session(ctx, ref)
	if err != nil {
		return session.Session{}, err
	}
	return s.Sessions.ReplaceState(ctx, session.ReplaceStateRequest{
		SessionRef:       ref,
		ExpectedRevision: &current.Revision,
		MutationGuard:    session.ControlMutationGuard(session.ControlMutationPurposeConfiguration),
		State:            state,
	})
}

func (s *Stack) CycleSessionMode(ctx context.Context, ref session.SessionRef) (string, error) {
	state, err := s.SessionRuntimeState(ctx, ref)
	if err != nil {
		return "", err
	}
	next := nextSessionMode(state.SessionMode)
	return s.SetSessionMode(ctx, ref, next)
}

// SessionRuntimeState returns the current per-session runtime overrides backed
// by session state.
func (s *Stack) SessionRuntimeState(ctx context.Context, ref session.SessionRef) (SessionRuntimeState, error) {
	if s == nil || s.Sessions == nil {
		return SessionRuntimeState{}, fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	state, err := s.Sessions.SnapshotState(ctx, ref)
	if err != nil {
		return SessionRuntimeState{}, err
	}
	if key := gateway.UnsupportedLegacyStateKey(state); key != "" {
		return SessionRuntimeState{}, fmt.Errorf("gatewayapp: %w: session state contains legacy key %q", session.ErrUnsupportedLegacyFormat, key)
	}
	modelRef := gateway.CurrentModelAlias(state)
	modelID := ""
	modelAlias := ""
	if s.lookup != nil && modelRef != "" {
		if cfg, ok := s.lookup.Config(modelRef); ok {
			modelID = cfg.ID
			modelAlias = cfg.Alias
		}
	}
	return SessionRuntimeState{
		ModelID:         modelID,
		ModelAlias:      modelAlias,
		ReasoningEffort: gateway.CurrentReasoningEffort(state),
		SessionMode:     gateway.CurrentSessionModeOrDefault(state, s.runtime.ApprovalMode),
		PolicyProfile:   firstNonEmpty(gateway.CurrentPolicyProfile(state), policyProfile(s.runtime.PolicyProfile)),
	}, nil
}

func normalizeSessionMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "manual":
		return "manual", nil
	case "", "auto", "auto-review", "auto_review", "autoreview":
		return "auto-review", nil
	default:
		return "", fmt.Errorf("gatewayapp: unknown session mode %q", strings.TrimSpace(mode))
	}
}

func normalizeSessionModeOrDefault(mode string) string {
	normalized, err := normalizeSessionMode(mode)
	if err != nil {
		return "auto-review"
	}
	return normalized
}

func nextSessionMode(mode string) string {
	switch normalizeSessionModeOrDefault(mode) {
	case "manual":
		return "auto-review"
	default:
		return "manual"
	}
}
