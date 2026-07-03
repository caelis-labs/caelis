package controladapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/internal/agenthandle"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
	taskapi "github.com/OnslaughtSnail/caelis/ports/task"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
)

func (d *Adapter) StartAgentSubagent(ctx context.Context, target string, prompt string, attachments []Attachment) (Turn, error) {
	agent, err := d.resolveAgentName(target)
	if err != nil {
		return nil, err
	}
	return d.startSidecarTurn(ctx, startSidecarTurnRequest{
		Agent:        agent,
		Prompt:       prompt,
		DisplayInput: displayInputWithAttachments(prompt, attachments),
		Attachments:  attachments,
		Source:       "slash_" + agent,
	})
}

type startSidecarTurnRequest struct {
	Agent            string
	LabelBase        string
	Prompt           string
	DisplayInput     string
	DisplayTitle     string
	Attachments      []Attachment
	Source           string
	DetachOnComplete bool
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
	updated, err := gw.AttachParticipant(ctx, gateway.AttachParticipantRequest{
		SessionRef: activeSession.SessionRef,
		BindingKey: d.bindingKey,
		Agent:      agent,
		Role:       session.ParticipantRoleSidecar,
		Source:     source,
		Label:      label,
	})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.session = updated
	d.hasSession = true
	d.mu.Unlock()
	participantID, err := sideAgentParticipantID(updated, agent, label)
	if err != nil {
		if rollbackErr := d.detachSideAgent(ctx, updated.SessionRef, participantID, "side_agent_prompt_rollback"); rollbackErr != nil {
			return nil, errors.Join(err, rollbackErr)
		}
		return nil, err
	}
	result, err := gw.PromptParticipant(ctx, gateway.PromptParticipantRequest{
		SessionRef:    updated.SessionRef,
		BindingKey:    d.bindingKey,
		ParticipantID: participantID,
		Input:         prompt,
		DisplayInput:  strings.TrimSpace(req.DisplayInput),
		DisplayTitle:  strings.TrimSpace(req.DisplayTitle),
		ContentParts:  contentParts,
		Source:        source,
	})
	if err != nil {
		if rollbackErr := d.detachSideAgent(ctx, updated.SessionRef, participantID, "side_agent_prompt_rollback"); rollbackErr != nil {
			return nil, errors.Join(err, rollbackErr)
		}
		return nil, err
	}
	if result.Handle == nil {
		return nil, nil
	}
	turn := &gatewayTurn{handle: result.Handle}
	if req.DetachOnComplete {
		return newDetachOnCompleteTurn(turn, func() {
			_ = d.detachSideAgent(ctx, updated.SessionRef, participantID, "side_agent_complete")
		}), nil
	}
	return turn, nil
}

func (d *Adapter) detachSideAgent(ctx context.Context, ref session.SessionRef, participantID string, source string) error {
	participantID = strings.TrimSpace(participantID)
	if participantID == "" || d == nil || d.stack == nil {
		return nil
	}
	gw, err := d.gatewayControlPlane()
	if err != nil {
		return err
	}
	updated, err := gw.DetachParticipant(context.WithoutCancel(ctx), gateway.DetachParticipantRequest{
		SessionRef:    ref,
		BindingKey:    d.bindingKey,
		ParticipantID: participantID,
		Source:        strings.TrimSpace(source),
	})
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.session = updated
	d.hasSession = true
	d.mu.Unlock()
	return nil
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
	return "@" + allocateSideAgentHandle(used, agent)
}

func allocateSideAgentHandle(used map[string]struct{}, agent string) string {
	return agenthandle.Allocate(used, agent)
}

func sideAgentParticipantID(activeSession session.Session, agent string, label string) (string, error) {
	agent = strings.TrimSpace(agent)
	label = strings.TrimSpace(label)
	for i := len(activeSession.Participants) - 1; i >= 0; i-- {
		participant := activeSession.Participants[i]
		if participant.Role != session.ParticipantRoleSidecar {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(participant.AgentName), agent) {
			continue
		}
		if label != "" && !strings.EqualFold(strings.TrimSpace(participant.Label), label) {
			continue
		}
		if id := strings.TrimSpace(participant.ID); id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("app/gatewayapp/controladapter: side ACP participant %q was not attached", agent)
}

func (d *Adapter) ContinueSubagent(ctx context.Context, handle string, prompt string, attachments []Attachment) (Turn, error) {
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
		return nil, err
	}
	if result.Handle == nil {
		return nil, nil
	}
	return &gatewayTurn{handle: result.Handle}, nil
}

type detachOnCompleteTurn struct {
	inner      Turn
	eventsOnce sync.Once
	detachOnce sync.Once
	events     <-chan eventstream.Envelope
	detach     func()
}

func newDetachOnCompleteTurn(inner Turn, detach func()) *detachOnCompleteTurn {
	return &detachOnCompleteTurn{inner: inner, detach: detach}
}

func (t *detachOnCompleteTurn) HandleID() string { return t.inner.HandleID() }
func (t *detachOnCompleteTurn) RunID() string    { return t.inner.RunID() }
func (t *detachOnCompleteTurn) TurnID() string   { return t.inner.TurnID() }

// Events is a single-consumer stream. Repeated calls return the same forwarded
// channel so the wrapped turn is consumed once and detach runs once.
func (t *detachOnCompleteTurn) Events() <-chan eventstream.Envelope {
	t.eventsOnce.Do(t.startEvents)
	return t.events
}

func (t *detachOnCompleteTurn) startEvents() {
	events := t.inner.Events()
	out := make(chan eventstream.Envelope, 32)
	t.events = out
	go func() {
		defer close(out)
		defer t.detachOnceFunc()
		for env := range events {
			out <- env
		}
	}()
}

func (t *detachOnCompleteTurn) SubmitApproval(ctx context.Context, decision ApprovalDecision) error {
	return t.inner.SubmitApproval(ctx, decision)
}

func (t *detachOnCompleteTurn) Cancel() {
	t.inner.Cancel()
}

func (t *detachOnCompleteTurn) Close() error {
	err := t.inner.Close()
	t.detachOnceFunc()
	return err
}

func (t *detachOnCompleteTurn) detachOnceFunc() {
	if t == nil || t.detach == nil {
		return
	}
	t.detachOnce.Do(t.detach)
}
