package controladapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/agenthandle"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// StartAgentRun routes an execution-bearing direct Agent command through the
// Runtime fixed to the active Session. The embedded Adapter remains the
// compatibility path for callers that have not supplied a participant client.
func (a *SessionClientAdapter) StartAgentRun(
	ctx context.Context,
	target string,
	prompt string,
	attachments []controlprompt.Attachment,
) (controlprompt.Turn, error) {
	if a == nil || a.participants == nil {
		if a == nil || a.Adapter == nil {
			return nil, errors.New("app/gatewayapp/controladapter: Session client adapter is unavailable")
		}
		return a.Adapter.StartAgentRun(ctx, target, prompt, attachments)
	}
	handle := agentbinding.NormalizeHandle(agentbinding.Handle(target))
	source := controlagents.DirectRunSource(handle)
	if source == "" {
		source = controlagents.CustomRoleRunSource(handle)
	}
	if source == "" {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: /%s is not an addressable Agent", handle)
	}
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return nil, err
	}
	label := allocateParticipantLabel(state.Participants, string(handle))
	displayAddress := "/" + string(handle)
	if runName := controlagents.FormatRunName(string(handle), label); runName != "" {
		displayAddress = "/" + runName
	}
	contentParts, err := contentPartsFromSubmission(prompt, attachments, state.CWD)
	if err != nil {
		return nil, err
	}
	turn, err := a.participants.Start(ctx, controlclient.ParticipantTurnStartRequest{
		SessionID:      state.SessionID,
		Handle:         string(handle),
		Role:           session.ParticipantRoleSidecar,
		Label:          label,
		Source:         source,
		Input:          strings.TrimSpace(prompt),
		DisplayInput:   displayInputWithAttachments(prompt, attachments),
		DisplayAddress: displayAddress,
		ContentParts:   contentParts,
	})
	if err != nil {
		return nil, err
	}
	return a.wrapParticipantTurn(turn), nil
}

// ContinueAgentRun routes a follow-up to the participant attachment visible in
// the typed Session snapshot instead of consulting the default Gateway.
func (a *SessionClientAdapter) ContinueAgentRun(
	ctx context.Context,
	handle string,
	prompt string,
	attachments []controlprompt.Attachment,
) (controlprompt.Turn, error) {
	if a == nil || a.participants == nil {
		if a == nil || a.Adapter == nil {
			return nil, errors.New("app/gatewayapp/controladapter: Session client adapter is unavailable")
		}
		return a.Adapter.ContinueAgentRun(ctx, handle, prompt, attachments)
	}
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return nil, err
	}
	participants := make([]participantAddress, 0, len(state.Participants))
	for _, participant := range state.Participants {
		participants = append(participants, participantAddress{
			ID: participant.ID, Kind: participant.Kind, Role: participant.Role,
			Label: participant.Label, SessionID: participant.SessionID, Source: participant.Source,
		})
	}
	participantID, err := resolveParticipantID(participants, handle)
	if err != nil {
		return nil, err
	}
	contentParts, err := contentPartsFromSubmission(prompt, attachments, state.CWD)
	if err != nil {
		return nil, err
	}
	turn, err := a.participants.Prompt(ctx, controlclient.ParticipantTurnPromptRequest{
		SessionID:      state.SessionID,
		ParticipantID:  participantID,
		Input:          strings.TrimSpace(prompt),
		DisplayInput:   displayInputWithAttachments(prompt, attachments),
		DisplayAddress: "/" + strings.TrimPrefix(strings.TrimSpace(handle), "/"),
		ContentParts:   contentParts,
		Source:         "user_side_agent",
	})
	if err != nil {
		return nil, err
	}
	return a.wrapParticipantTurn(turn), nil
}

// StartReview keeps review execution in the active Session Runtime while
// preserving the existing transient participant and display semantics.
func (a *SessionClientAdapter) StartReview(
	ctx context.Context,
	instructions string,
	attachments []controlprompt.Attachment,
) (controlprompt.Turn, error) {
	if a == nil || a.participants == nil {
		if a == nil || a.Adapter == nil {
			return nil, errors.New("app/gatewayapp/controladapter: Session client adapter is unavailable")
		}
		return a.Adapter.StartReview(ctx, instructions, attachments)
	}
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return nil, err
	}
	prompt, attachmentOffset := gatewayapp.ReviewPrompt(instructions)
	shiftedAttachments := shiftControlAttachments(attachments, attachmentOffset)
	contentParts, err := contentPartsFromSubmission(prompt, shiftedAttachments, state.CWD)
	if err != nil {
		return nil, err
	}
	turn, err := a.participants.Start(ctx, controlclient.ParticipantTurnStartRequest{
		SessionID:      state.SessionID,
		Handle:         string(agentbinding.HandleReviewer),
		Role:           session.ParticipantRoleSidecar,
		Label:          allocateParticipantLabel(state.Participants, gatewayapp.ReviewerAgentID),
		Source:         "slash_review",
		Input:          prompt,
		DisplayInput:   displayInputWithAttachments(instructions, attachments),
		DisplayAddress: "/review",
		DisplayTitle:   reviewDisplayTitle(instructions),
		ContentParts:   contentParts,
		Transient:      true,
		DetachSource:   "side_agent_complete",
	})
	if err != nil {
		return nil, err
	}
	return a.wrapParticipantTurn(turn), nil
}

func (a *SessionClientAdapter) activeClientSessionState(ctx context.Context) (controlclient.SessionState, error) {
	if a == nil || a.sessionClient == nil {
		return controlclient.SessionState{}, errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	active, err := a.ensureSession(ctx)
	if err != nil {
		return controlclient.SessionState{}, err
	}
	return a.sessionClient.InspectSession(ctx, controlclient.StateRequest{SessionID: active.SessionID})
}

func (a *SessionClientAdapter) wrapParticipantTurn(turn controlclient.TargetTurn) controlprompt.Turn {
	wrapped := &sessionClientTurn{turn: turn}
	wrapped.onClose = func() { a.clearActiveTurn(wrapped) }
	a.setActiveTurn(wrapped)
	return wrapped
}

func allocateParticipantLabel(participants []session.ParticipantBinding, handle string) string {
	used := make(map[string]struct{}, len(participants))
	for _, participant := range participants {
		if label := taskapi.NormalizeHandle(participant.Label); label != "" {
			used[label] = struct{}{}
		}
	}
	return "@" + agenthandle.Allocate(used, handle)
}
