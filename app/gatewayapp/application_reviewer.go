package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// Approval mode remains an execution boundary; model selection belongs to the
// Runtime Team, not the legacy reviewer retained in immutable creation evidence.
func (r *applicationTurnResolver) ResolveApprovalMode(_ context.Context, ref session.SessionRef) (kernel.ApprovalMode, error) {
	if ref.SessionID != r.binding.SessionID {
		return "", sessionprofile.ErrUnauthorized
	}
	return kernel.ApprovalMode(sessionprofile.EffectiveApprovalMode(r.binding.Profile)), nil
}

func (r *applicationTurnResolver) ResolveApprovalModel(ctx context.Context, ref session.SessionRef) (model.LLM, error) {
	if ref.SessionID != r.binding.SessionID {
		return nil, sessionprofile.ErrUnauthorized
	}
	resolved, bound, err := r.composition.resolveGuardianAgentModel(ctx, r.composition.activeRuntime.ContextWindow)
	if err != nil || bound {
		return resolved, err
	}
	configuration, err := r.composition.authorities.sessionProfiles.Configuration(ctx, r.binding.Scope, ref.SessionID)
	if err != nil {
		return nil, err
	}
	main, err := r.resolveProfileModel(ctx, configuration.Profile)
	if err != nil {
		return nil, err
	}
	return withSystemAgentReasoningEffort(main), nil
}

func (s *runtimeComposition) applicationReviewerState(ctx context.Context, profile sessionprofile.Profile) (sessionprofile.ReviewerState, error) {
	out := sessionprofile.ReviewerState{ApprovalMode: sessionprofile.EffectiveApprovalMode(profile), Status: "manual"}
	if err := sessionprofile.ValidateProfile(profile); err != nil {
		return out, err
	}
	if out.ApprovalMode != string(kernel.ApprovalModeAutoReview) {
		return out, nil
	}
	snapshot, err := s.placementSnapshot(ctx)
	if err != nil {
		return out, err
	}
	selected := profile
	if _, bound := agentbinding.Lookup(snapshot.placement.Bindings, agentbinding.HandleGuardian); bound {
		placement, resolveErr := controlplacement.ResolveHandle(snapshot.placement, controlplacement.HandleRequest{Handle: agentbinding.HandleGuardian, Purpose: controlplacement.PurposeGuardian})
		if resolveErr != nil {
			return unavailableReviewerState(ctx, out)
		}
		selected = sessionprofile.Profile{Model: placement.Model, ReasoningEffort: placement.ReasoningEffort, ServiceTier: placement.ServiceTier}
	}
	out.Reviewer = &sessionprofile.Reviewer{Kind: "guardian", Model: selected.Model}
	configured, err := applicationModelConfig(snapshot.lookup, selected)
	if err == nil {
		_, err = snapshot.lookup.ResolveModelConfig(ctx, configured, s.activeRuntime.ContextWindow)
	}
	if err != nil {
		return unavailableReviewerState(ctx, out)
	}
	out.Status = "ready"
	return out, nil
}

func unavailableReviewerState(ctx context.Context, out sessionprofile.ReviewerState) (sessionprofile.ReviewerState, error) {
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	// Provider/credential failures may contain secrets. Readiness is only a
	// local assembly check, not a guarantee that a model will produce a decision.
	out.Status = "unavailable"
	out.Reason = "Runtime Guardian model is unavailable; no automatic approval can be granted"
	return out, nil
}

func (r *applicationTurnResolver) SnapshotApprovalConfiguration(ctx context.Context, ref session.SessionRef) (context.Context, error) {
	if ref.SessionID != r.binding.SessionID {
		return nil, sessionprofile.ErrUnauthorized
	}
	return r.composition.captureTeamSnapshot(ctx)
}

func (r *applicationTurnResolver) ResolveApprovalJudgment(ctx context.Context, ref session.SessionRef) (judgment.Evaluator, error) {
	if ref.SessionID != r.binding.SessionID {
		return nil, sessionprofile.ErrUnauthorized
	}
	return r.composition.boundJudgment(ctx, agentbinding.HandleGuardianScreen)
}
