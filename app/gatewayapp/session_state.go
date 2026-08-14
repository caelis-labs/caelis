package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// updateSessionStateAtRevision applies one focused configuration mutation
// against the revision observed at the AppServer boundary. Unlike the legacy
// helper above, it never reloads and silently upgrades a stale caller intent.
func (s *Stack) updateSessionStateAtRevision(
	ctx context.Context,
	ref session.SessionRef,
	expectedRevision uint64,
	update func(map[string]any) (map[string]any, error),
) (session.Session, error) {
	if s == nil || s.Sessions == nil {
		return session.Session{}, fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	return s.Sessions.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef:       ref,
		ExpectedRevision: &expectedRevision,
		MutationGuard:    session.ControlMutationGuard(session.ControlMutationPurposeConfiguration),
		Update:           update,
	})
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
	if key := kernel.UnsupportedLegacyStateKey(state); key != "" {
		return SessionRuntimeState{}, fmt.Errorf("gatewayapp: %w: session state contains legacy key %q", session.ErrUnsupportedLegacyFormat, key)
	}
	modelRef := kernel.CurrentModelAlias(state)
	modelID := ""
	modelAlias := ""
	if s.lookup != nil && modelRef != "" {
		if cfg, ok := s.lookup.Config(modelRef); ok {
			modelID = cfg.ID
			modelAlias = cfg.Alias
		}
	}
	s.mu.RLock()
	runtimeConfig := s.runtime
	s.mu.RUnlock()
	securityPosture := resolveProcessSecurityPosture(runtimeConfig)
	sessionMode := kernel.CurrentSessionModeOrDefault(state, runtimeConfig.ApprovalMode)
	effectivePolicyProfile := firstNonEmpty(kernel.CurrentPolicyProfile(state), policyProfile(runtimeConfig.PolicyProfile))
	if securityPosture.FullAccessMode {
		sessionMode = securityPosture.DisplayMode
		effectivePolicyProfile = securityPosture.PolicyMode
	}
	return SessionRuntimeState{
		ModelID:         modelID,
		ModelAlias:      modelAlias,
		ReasoningEffort: kernel.CurrentReasoningEffort(state),
		SessionMode:     sessionMode,
		PolicyProfile:   effectivePolicyProfile,
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
