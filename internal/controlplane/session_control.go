package controlplane

import (
	"context"
	"fmt"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// HandoffControllerRequest switches the active controller for one Session.
type HandoffControllerRequest struct {
	SessionRef              session.SessionRef
	ExpectedRevision        *uint64
	ExpectedControllerEpoch string
	Kind                    session.ControllerKind
	Agent                   string
	Placement               placement.Placement
	Source                  string
	Reason                  string
	StateUpdate             session.AppendStateUpdate
}

// SessionControlPlane is the host-facing composition of neutral participant
// execution and the Control-owned controller handoff operation.
type SessionControlPlane interface {
	agent.ParticipantControlPlane
	HandoffController(context.Context, HandoffControllerRequest) (session.Session, error)
}

// SessionControl composes Control-owned handoff with neutral participant
// execution after both have been assembled by the product host.
type SessionControl struct {
	controllers  *Coordinator
	participants agent.ParticipantControlPlane
}

// SessionControlConfig supplies the product-owned controller and participant
// authorities. Placements are resolved from the Session Runtime's immutable
// activation snapshot before they reach this boundary.
type SessionControlConfig struct {
	Controllers  *Coordinator
	Participants agent.ParticipantControlPlane
}

// NewSessionControl returns the explicit host control surface injected into
// kernel. Runtime is never inferred as the handoff authority.
func NewSessionControl(cfg SessionControlConfig) (*SessionControl, error) {
	if cfg.Controllers == nil {
		return nil, fmt.Errorf("controlplane: controller coordinator is required")
	}
	if cfg.Participants == nil {
		return nil, fmt.Errorf("controlplane: participant execution is required")
	}
	return &SessionControl{
		controllers:  cfg.Controllers,
		participants: cfg.Participants,
	}, nil
}

func (c *SessionControl) HandoffController(ctx context.Context, req HandoffControllerRequest) (session.Session, error) {
	return c.controllers.HandoffController(ctx, req)
}

func (c *SessionControl) AttachParticipant(ctx context.Context, req agent.AttachParticipantRequest) (session.Session, error) {
	return c.participants.AttachParticipant(ctx, req)
}

func (c *SessionControl) PromptParticipant(ctx context.Context, req agent.PromptParticipantRequest) (agent.RunResult, error) {
	return c.participants.PromptParticipant(ctx, req)
}

func (c *SessionControl) DetachParticipant(ctx context.Context, req agent.DetachParticipantRequest) (session.Session, error) {
	return c.participants.DetachParticipant(ctx, req)
}
