package gatewayapp

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func resolveApplicationReviewer(ctx context.Context, lookup *modelLookup, profile application.Profile, contextWindow int) (model.LLM, error) {
	if profile.Reviewer == nil || application.EffectiveApprovalMode(profile) != string(kernel.ApprovalModeAutoReview) {
		return nil, fmt.Errorf("%w: application reviewer is not configured", application.ErrUnsupported)
	}
	configured, err := applicationModelConfig(lookup, application.Profile{Model: profile.Reviewer.Model})
	if err != nil {
		return nil, err
	}
	resolved, err := lookup.ResolveModelConfig(ctx, configured, contextWindow)
	if err != nil {
		return nil, err
	}
	return resolved.Model, nil
}

// ResolveApprovalMode makes the immutable Application binding authoritative,
// independently of ordinary Session/Worker routing overrides.
func (r *applicationTurnResolver) ResolveApprovalMode(_ context.Context, ref session.SessionRef) (kernel.ApprovalMode, error) {
	if ref.SessionID != r.binding.SessionID {
		return "", application.ErrUnauthorized
	}
	return kernel.ApprovalMode(application.EffectiveApprovalMode(r.binding.Profile)), nil
}

// ResolveApprovalModel keeps the creation-bound reviewer independent of the hot
// main-model catalog and the Host's ordinary Guardian/default model bindings.
func (r *applicationTurnResolver) ResolveApprovalModel(ctx context.Context, ref session.SessionRef) (model.LLM, error) {
	if ref.SessionID != r.binding.SessionID {
		return nil, application.ErrUnauthorized
	}
	lookup, err := r.modelLookup(ctx)
	if err != nil {
		return nil, err
	}
	return resolveApplicationReviewer(ctx, lookup, r.binding.Profile, r.composition.activeRuntime.ContextWindow)
}

func (s *runtimeComposition) applicationReviewerState(ctx context.Context, profile application.Profile) (application.ReviewerState, error) {
	out := application.ReviewerState{ApprovalMode: application.EffectiveApprovalMode(profile), Status: "manual"}
	if err := application.ValidateProfile(profile); err != nil {
		return out, err
	}
	if profile.Reviewer == nil {
		return out, nil
	}
	reviewer := *profile.Reviewer
	out.Reviewer = &reviewer
	doc, err := s.authorities.store.LoadContext(ctx)
	if err != nil {
		return out, err
	}
	lookup, err := cloneSessionModelLookup(s.lookup, doc)
	if err != nil {
		return out, err
	}
	if _, err := resolveApplicationReviewer(ctx, lookup, profile, s.activeRuntime.ContextWindow); err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		out.Status = "unavailable"
		// Provider/credential errors may contain secrets or transport details.
		// Public state exposes availability, not those private diagnostics.
		out.Reason = "configured reviewer model is unavailable; no automatic approval can be granted"
		return out, nil
	}
	out.Status = "ready"
	return out, nil
}
