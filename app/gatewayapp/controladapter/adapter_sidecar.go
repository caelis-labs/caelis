package controladapter

import (
	"context"
	"fmt"
	"strings"

	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/agenthandle"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func (d *Adapter) StartAgentRun(ctx context.Context, target string, prompt string, attachments []Attachment) (Turn, error) {
	handle := agentbinding.NormalizeHandle(agentbinding.Handle(target))
	if !agentbinding.IsDirectRun(handle) {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: %q is not a direct delegation profile", strings.TrimSpace(target))
	}
	if d == nil || d.stack == nil || d.stack.AgentBinding.ResolveFn == nil {
		return nil, missingRuntimeDependency("delegation placement")
	}
	placement, err := d.stack.AgentBinding.ResolveFn(bindingContext(ctx), handle)
	if err != nil {
		return nil, err
	}
	agent := strings.TrimSpace(placement.Agent)
	if placement.Kind == sdkplacement.KindModel {
		agent = string(handle)
	}
	if agent == "" {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: delegation handle %q has no executable endpoint", handle)
	}
	return d.startSidecarTurn(ctx, startSidecarTurnRequest{
		Agent:        agent,
		Placement:    placement,
		LabelBase:    string(handle),
		Prompt:       prompt,
		DisplayInput: displayInputWithAttachments(prompt, attachments),
		Attachments:  attachments,
		Source:       controlagents.DirectRunSource(handle),
	})
}

type startSidecarTurnRequest struct {
	Agent        string
	Placement    sdkplacement.Placement
	LabelBase    string
	Prompt       string
	DisplayInput string
	DisplayTitle string
	Attachments  []Attachment
	Source       string
	Transient    bool
}

func (d *Adapter) startSidecarTurn(ctx context.Context, req startSidecarTurnRequest) (Turn, error) {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	agent := strings.TrimSpace(req.Agent)
	if agent == "" {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: agent name is required")
	}
	prompt := strings.TrimSpace(req.Prompt)
	contentParts, err := contentPartsFromSubmission(prompt, req.Attachments, d.WorkspaceDir())
	if err != nil {
		return nil, err
	}
	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "slash_" + agent
	}
	gw, err := d.gatewayControlPlane()
	if err != nil {
		return nil, err
	}
	labelBase := firstNonEmpty(req.LabelBase, agent)
	label := d.allocateSideAgentLabel(ctx, activeSession.SessionRef, labelBase)
	startReq := kernel.StartParticipantRequest{
		SessionRef:   activeSession.SessionRef,
		BindingKey:   d.bindingKey,
		Agent:        agent,
		Placement:    req.Placement,
		Role:         session.ParticipantRoleSidecar,
		Source:       source,
		Label:        label,
		Input:        prompt,
		DisplayInput: strings.TrimSpace(req.DisplayInput),
		DisplayTitle: strings.TrimSpace(req.DisplayTitle),
		ContentParts: contentParts,
	}
	if req.Transient {
		startReq.Lifecycle = kernel.ParticipantLifecycleTransient
		startReq.DetachSource = "side_agent_complete"
	}

	feedSubscription, err := d.subscribeGatewayTurn(activeSession.SessionRef)
	if err != nil {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: establish sidecar feed boundary: %w", err)
	}
	result, err := gw.StartParticipant(ctx, startReq)
	if err != nil {
		if feedSubscription != nil {
			_ = feedSubscription.Close()
		}
		return nil, err
	}
	if !req.Transient && result.Session.SessionID != "" {
		d.mu.Lock()
		d.session = result.Session
		d.hasSession = true
		d.mu.Unlock()
	}
	if result.Handle == nil {
		if feedSubscription != nil {
			_ = feedSubscription.Close()
		}
		return nil, nil
	}
	return d.newGatewayTurnWithSubscription(result.Handle, feedSubscription, true, ctx), nil
}

func (d *Adapter) allocateSideAgentLabel(ctx context.Context, ref session.SessionRef, agent string) string {
	used := map[string]struct{}{}
	if gw, err := d.gatewayControlPlane(); err == nil {
		if state, err := gw.ControlPlaneState(ctx, kernel.ControlPlaneStateRequest{SessionRef: ref}); err == nil {
			for _, participant := range state.Participants {
				label := taskapi.NormalizeHandle(participant.Label)
				if label != "" {
					used[label] = struct{}{}
				}
			}
		}
	}
	return "@" + agenthandle.Allocate(used, agent)
}

func (d *Adapter) ContinueAgentRun(ctx context.Context, handle string, prompt string, attachments []Attachment) (Turn, error) {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	prompt = strings.TrimSpace(prompt)
	contentParts, err := contentPartsFromSubmission(prompt, attachments, d.WorkspaceDir())
	if err != nil {
		return nil, err
	}
	participantID, err := d.resolveParticipantID(ctx, activeSession.SessionRef, handle)
	if err != nil {
		return nil, err
	}
	gw, err := d.gatewayControlPlane()
	if err != nil {
		return nil, err
	}
	feedSubscription, err := d.subscribeGatewayTurn(activeSession.SessionRef)
	if err != nil {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: establish sidecar feed boundary: %w", err)
	}
	result, err := gw.PromptParticipant(ctx, kernel.PromptParticipantRequest{
		SessionRef:    activeSession.SessionRef,
		BindingKey:    d.bindingKey,
		ParticipantID: participantID,
		Input:         prompt,
		DisplayInput:  displayInputWithAttachments(prompt, attachments),
		ContentParts:  contentParts,
		Source:        "user_side_agent",
	})
	if err != nil {
		if feedSubscription != nil {
			_ = feedSubscription.Close()
		}
		return nil, err
	}
	if result.Handle == nil {
		if feedSubscription != nil {
			_ = feedSubscription.Close()
		}
		return nil, nil
	}
	return d.newGatewayTurnWithSubscription(result.Handle, feedSubscription, true, ctx), nil
}
