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
// Runtime fixed to the active Session.
func (a *SessionClientAdapter) StartAgentRun(
	ctx context.Context,
	target string,
	prompt string,
	attachments []controlprompt.Attachment,
) (controlprompt.Turn, error) {
	if a == nil || a.participants == nil {
		return nil, errors.New("app/gatewayapp/controladapter: participant client is unavailable")
	}
	handle := agentbinding.NormalizeHandle(agentbinding.Handle(target))
	source := controlagents.DirectRunSource(handle)
	if source == "" {
		source = controlagents.CustomRoleRunSource(handle)
	}
	if source == "" {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: /%s is not an addressable Agent", handle)
	}
	contentParts, err := contentPartsFromSubmission(prompt, attachments, a.WorkspaceDir())
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(prompt) == "" && len(contentParts) == 0 {
		return nil, errors.New("app/gatewayapp/controladapter: direct Agent prompt input is required")
	}
	return a.startAdmittedTurn(ctx, func(startCtx context.Context) (controlclient.TargetTurn, error) {
		state, err := a.ensureSessionForParticipantStart(startCtx)
		if err != nil {
			return nil, err
		}
		label := allocateParticipantLabel(state.Participants, string(handle))
		displayAddress := "/" + string(handle)
		if runName := controlagents.FormatRunName(string(handle), label); runName != "" {
			displayAddress = "/" + runName
		}
		return a.participants.Start(startCtx, controlclient.ParticipantTurnStartRequest{
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
	})
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
		return nil, errors.New("app/gatewayapp/controladapter: participant client is unavailable")
	}
	return a.startAdmittedTurn(ctx, func(startCtx context.Context) (controlclient.TargetTurn, error) {
		state, err := a.currentClientSessionState(startCtx)
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
		return a.participants.Prompt(startCtx, controlclient.ParticipantTurnPromptRequest{
			SessionID:      state.SessionID,
			ParticipantID:  participantID,
			Input:          strings.TrimSpace(prompt),
			DisplayInput:   displayInputWithAttachments(prompt, attachments),
			DisplayAddress: "/" + strings.TrimPrefix(strings.TrimSpace(handle), "/"),
			ContentParts:   contentParts,
			Source:         "user_side_agent",
		})
	})
}

// StartReview keeps review execution in the active Session Runtime while
// preserving the existing transient participant and display semantics.
func (a *SessionClientAdapter) StartReview(
	ctx context.Context,
	instructions string,
	attachments []controlprompt.Attachment,
) (controlprompt.Turn, error) {
	if a == nil || a.participants == nil {
		return nil, errors.New("app/gatewayapp/controladapter: participant client is unavailable")
	}
	prompt, attachmentOffset := gatewayapp.ReviewPrompt(instructions)
	shiftedAttachments := shiftControlAttachments(attachments, attachmentOffset)
	contentParts, err := contentPartsFromSubmission(prompt, shiftedAttachments, a.WorkspaceDir())
	if err != nil {
		return nil, err
	}
	return a.startAdmittedTurn(ctx, func(startCtx context.Context) (controlclient.TargetTurn, error) {
		state, err := a.ensureSessionForParticipantStart(startCtx)
		if err != nil {
			return nil, err
		}
		return a.participants.Start(startCtx, controlclient.ParticipantTurnStartRequest{
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
	})
}

func (a *SessionClientAdapter) currentClientSessionState(ctx context.Context) (controlclient.SessionState, error) {
	if a == nil || a.sessionClient == nil {
		return controlclient.SessionState{}, errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	sessionID := a.clientSessionID()
	if sessionID == "" {
		return controlclient.SessionState{}, errors.New("app/gatewayapp/controladapter: no Session is selected")
	}
	return a.inspectWorkSession(ctx, sessionID)
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

func reviewDisplayTitle(instructions string) string {
	if strings.TrimSpace(instructions) != "" {
		return ""
	}
	return "Code review requested"
}

func shiftControlAttachments(items []controlprompt.Attachment, offset int) []controlprompt.Attachment {
	if len(items) == 0 || offset == 0 {
		return append([]controlprompt.Attachment(nil), items...)
	}
	out := make([]controlprompt.Attachment, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		data := strings.TrimSpace(item.Data)
		if name == "" && data == "" {
			continue
		}
		out = append(out, controlprompt.Attachment{
			Name:     name,
			Offset:   max(item.Offset, 0) + offset,
			MimeType: strings.TrimSpace(item.MimeType),
			Data:     data,
		})
	}
	return out
}
