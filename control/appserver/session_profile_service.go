package appserver

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// SessionProfileServiceConfig binds explicit profile features to the
// existing Session command and observation owners.
type SessionProfileServiceConfig struct {
	Store    *sessionprofile.Store
	Commands *CommandService
	Sessions Service
	// ValidateProfile checks provider capabilities without modifying Host settings.
	// Configuration updates are unavailable unless this admission owner is bound.
	ValidateProfile func(context.Context, sessionprofile.Profile) error
	// ModelImageInput reads the selected model without activation or model calls.
	// Nil capability means unknown; an unbound reader leaves negotiation absent.
	ModelImageInput func(context.Context, sessionprofile.Profile) (*bool, error)
	// MCPStatus observes resident per-service health without activating a
	// Session Runtime. The common Session owner authorizes access.
	MCPStatus func(context.Context, string, uint64) sessionprofile.MCPStatus
	// ConfigurationCommitted retires resident resources after a successful
	// Store CAS. It is a best-effort notification, not a second commit gate.
	ConfigurationCommitted func(context.Context, string, sessionprofile.Configuration)
	// ReviewerState reports local reviewer readiness from the creation-bound
	// profile without activating a Runtime or contacting the provider. Nil
	// disables Guardian review admission and capability negotiation.
	ReviewerState func(context.Context, sessionprofile.Profile) (sessionprofile.ReviewerState, error)
	// NativeExecution reports the Host platform's ordinary native sandbox support.
	NativeExecution bool
	// NativeProfileChildren reports a Host assembly with scoped profile child
	// endpoints backed by the original native participant, Task and approval owners.
	NativeProfileChildren bool
}

// SessionProfileService exposes explicit profiles under the common owner. It does not execute
// models, implement business tools, or maintain a second transcript.
type SessionProfileService struct{ config SessionProfileServiceConfig }

func NewSessionProfileService(config SessionProfileServiceConfig) (*SessionProfileService, error) {
	if config.Store == nil || config.Commands == nil || config.Sessions == nil {
		return nil, errors.New("application: store, commands and sessions are required")
	}
	return &SessionProfileService{config: config}, nil
}

// Store exposes profile persistence to trusted Host composition.
func (s *SessionProfileService) Store() *sessionprofile.Store { return s.config.Store }

// SessionScope derives the common owner from authenticated adapter context.
func SessionScope(p Principal) (sessionprofile.Scope, error) {
	if strings.TrimSpace(p.ID) == "" {
		return sessionprofile.Scope{}, ErrUnauthorized
	}
	return sessionprofile.PrincipalScope(p.ID), nil
}

// requiresCallbackApproval identifies callback catalogs that need the
// negotiated Guardian review capability, including manual approval routing.
func requiresCallbackApproval(tools []sessionprofile.ToolDefinition) bool {
	for _, tool := range tools {
		if tool.ApprovalPolicy == "required" {
			return true
		}
	}
	return false
}

func validateInputSource(kind, grantID string) error {
	switch kind {
	case "user", "application_summary", "external_material":
		if grantID != "" {
			return errorcode.New(errorcode.InvalidArgument, "application: grant_id requires authorized_background source_kind")
		}
	case "authorized_background":
		if grantID == "" || strings.TrimSpace(grantID) != grantID {
			return errorcode.New(errorcode.InvalidArgument, "application: authorized_background requires grant_id")
		}
	default:
		return errorcode.New(errorcode.InvalidArgument, "application: unsupported source_kind")
	}
	return nil
}
