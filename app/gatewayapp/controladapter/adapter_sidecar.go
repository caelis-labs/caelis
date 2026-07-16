package controladapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/agenthandle"
	controldelegation "github.com/caelis-labs/caelis/control/delegation"
	"github.com/caelis-labs/caelis/ports/gateway"
)

func (d *Adapter) StartAgentRun(ctx context.Context, target string, prompt string, attachments []Attachment) (Turn, error) {
	profile := controldelegation.NormalizeProfile(controldelegation.Profile(target))
	if !controldelegation.IsDirectRunProfile(string(profile)) {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: %q is not a direct delegation profile", strings.TrimSpace(target))
	}
	if d == nil || d.stack == nil || d.stack.Delegation.StatusFn == nil {
		return nil, missingRuntimeDependency("delegation status")
	}
	status, err := d.stack.Delegation.StatusFn(delegationContext(ctx))
	if err != nil {
		return nil, err
	}
	agent := ""
	reasoningEffort := ""
	for _, item := range status.Profiles {
		if item.Definition.Profile != profile && item.Binding.Profile != profile {
			continue
		}
		if item.Binding.Target != controldelegation.TargetAgent || strings.TrimSpace(item.Agent.ID) == "" {
			return nil, fmt.Errorf("app/gatewayapp/controladapter: /%s is not bound; run /subagent bind %s to choose an Agent", profile, profile)
		}
		agent = strings.TrimSpace(item.Agent.ID)
		reasoningEffort = strings.TrimSpace(item.Binding.ReasoningEffort)
		break
	}
	if agent == "" {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: delegation profile %q is unavailable", profile)
	}
	return d.startSidecarTurn(ctx, startSidecarTurnRequest{
		Agent:           agent,
		ReasoningEffort: reasoningEffort,
		LabelBase:       string(profile),
		Prompt:          prompt,
		DisplayInput:    displayInputWithAttachments(prompt, attachments),
		Attachments:     attachments,
		Source:          controldelegation.DirectRunSource(profile),
	})
}

type startSidecarTurnRequest struct {
	Agent           string
	ReasoningEffort string
	LabelBase       string
	Prompt          string
	DisplayInput    string
	DisplayTitle    string
	Attachments     []Attachment
	Source          string
	Transient       bool
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
	startReq := gateway.StartParticipantRequest{
		SessionRef:      activeSession.SessionRef,
		BindingKey:      d.bindingKey,
		Agent:           agent,
		ReasoningEffort: strings.TrimSpace(req.ReasoningEffort),
		Role:            session.ParticipantRoleSidecar,
		Source:          source,
		Label:           label,
		Input:           prompt,
		DisplayInput:    strings.TrimSpace(req.DisplayInput),
		DisplayTitle:    strings.TrimSpace(req.DisplayTitle),
		ContentParts:    contentParts,
	}
	if req.Transient {
		startReq.Lifecycle = gateway.ParticipantLifecycleTransient
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
		if state, err := gw.ControlPlaneState(ctx, gateway.ControlPlaneStateRequest{SessionRef: ref}); err == nil {
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
	result, err := gw.PromptParticipant(ctx, gateway.PromptParticipantRequest{
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
