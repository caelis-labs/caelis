package application

import "fmt"

// CapabilityGuardianReview negotiates explicit Guardian composition and callback
// approval policy. It does not grant sandbox or callback execution authority.
const CapabilityGuardianReview = "application-guardian-review-v1"

// Reviewer selects the automatic reviewer independently of native sandbox policy
// and the application's main model. Model names an explicit Host-managed model;
// credentials and global Agent bindings are never part of this configuration.
type Reviewer struct {
	Kind  string `json:"kind"`
	Model string `json:"model"`
}

// ReviewerState reports the creation-bound review route and its current local
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
			return fmt.Errorf("%w: auto-review requires an explicit reviewer", ErrInvalid)
		}
		if profile.Reviewer.Kind != "guardian" {
			return fmt.Errorf("%w: unsupported application reviewer", ErrUnsupported)
		}
		if !validID(profile.Reviewer.Model) {
			return fmt.Errorf("%w: reviewer requires an explicit configured model", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported application approval mode", ErrUnsupported)
	}
	return nil
}
