package controlplane

import (
	"context"
	"fmt"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// SessionControl composes Control-owned handoff with neutral participant
// execution after both have been assembled by the product host.
type SessionControl struct {
	controllers  *Coordinator
	participants agent.ParticipantControlPlane
}

// NewSessionControl returns the explicit host control surface injected into
// kernel. Runtime is never inferred as the handoff authority.
func NewSessionControl(controllers *Coordinator, participants agent.ParticipantControlPlane) (*SessionControl, error) {
	if controllers == nil {
		return nil, fmt.Errorf("controlplane: controller coordinator is required")
	}
	if participants == nil {
		return nil, fmt.Errorf("controlplane: participant execution is required")
	}
	return &SessionControl{controllers: controllers, participants: participants}, nil
}

func (c *SessionControl) HandoffController(ctx context.Context, req agent.HandoffControllerRequest) (session.Session, error) {
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
