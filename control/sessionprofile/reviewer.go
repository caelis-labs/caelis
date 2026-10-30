package sessionprofile

import "fmt"

// CapabilityGuardianReview negotiates explicit Guardian composition and callback
// approval policy. It does not grant sandbox or callback execution authority.
const CapabilityGuardianReview = "session-guardian-review-v1"

// CapabilityRuntimeAgentTeam guarantees one live Team for ordinary and explicit
// profile Sessions, including legacy profiles with a retained reviewer field.
const CapabilityRuntimeAgentTeam = "runtime-agent-team-v1"

// Reviewer identifies the effective Guardian in ReviewerState. In a Profile it
// preserves legacy creation evidence only; it no longer selects a model. Keeping
// those bytes preserves creation digests, receipts and Session identities.
type Reviewer struct {
	Kind  string `json:"kind"`
	Model string `json:"model"`
}

// ReviewerState reports the effective Runtime Team review route and its local
// availability, not provider health or a guarantee that a future review succeeds.
// Status is manual, ready, or unavailable. Reading it does not activate a Runtime.
type ReviewerState struct {
	SessionID    string    `json:"session_id"`
	ApprovalMode string    `json:"approval_mode"`
	Reviewer     *Reviewer `json:"reviewer,omitempty"`
	Status       string    `json:"status"`
	Reason       string    `json:"reason,omitempty"`
}

// EffectiveApprovalMode applies the legacy manual default. Callers must validate
// the complete profile before using it to assemble execution authority.
func EffectiveApprovalMode(profile Profile) string {
	if profile.Permissions.ApprovalMode == "" {
		return "manual"
	}
	return profile.Permissions.ApprovalMode
}

func validateReviewer(profile Profile) error {
	switch EffectiveApprovalMode(profile) {
	case "manual":
		if profile.Reviewer != nil {
			return fmt.Errorf("%w: reviewer requires auto-review approval mode", ErrInvalid)
		}
	case "auto-review":
		if profile.Reviewer == nil {
			return nil
		}
		if profile.Reviewer.Kind != "guardian" {
			return fmt.Errorf("%w: unsupported application reviewer", ErrUnsupported)
		}
		if !validID(profile.Reviewer.Model) {
			return fmt.Errorf("%w: legacy reviewer model identifier is invalid", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported application approval mode", ErrUnsupported)
	}
	return nil
}
