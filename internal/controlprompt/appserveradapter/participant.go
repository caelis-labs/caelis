package appserveradapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/agenthandle"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// StartAgentRun starts a background Task in the selected Session without
// claiming its foreground Turn. Control owns placement, approval and lifetime.
func (a *SessionClientAdapter) StartAgentRun(ctx context.Context, target, prompt string, attachments []controlprompt.Attachment) (controlprompt.AgentRunResult, error) {
	if a == nil || a.participantClient == nil {
		return controlprompt.AgentRunResult{}, errors.New("participant client is unavailable")
	}
	handle := agentbinding.NormalizeHandle(agentbinding.Handle(target))
	source := controlagents.DirectRunSource(handle)
	if source == "" {
		source = controlagents.CustomRoleRunSource(handle)
	}
	if source == "" {
		return controlprompt.AgentRunResult{}, fmt.Errorf("/%s is not an addressable Agent", handle)
	}
	parts, err := ContentPartsFromSubmission(prompt, attachments, a.WorkspaceDir())
	if err != nil {
		return controlprompt.AgentRunResult{}, err
	}
	if strings.TrimSpace(prompt) == "" && len(parts) == 0 {
		return controlprompt.AgentRunResult{}, errors.New("direct Agent prompt input is required")
	}
	return a.startBackgroundParticipant(ctx, handle, source, prompt, parts)
}

func (a *SessionClientAdapter) startBackgroundParticipant(ctx context.Context, handle agentbinding.Handle, source, prompt string, parts []model.ContentPart) (controlprompt.AgentRunResult, error) {
	if a == nil || a.participantClient == nil {
		return controlprompt.AgentRunResult{}, errors.New("participant client is unavailable")
	}
	state, err := a.ensureSessionForParticipantStart(ctx)
	if err != nil {
		return controlprompt.AgentRunResult{}, err
	}
	result, err := a.participantClient.StartParticipant(ctx, appserver.StartParticipantRequest{
		WriteBase: appserver.WriteBase{OperationID: "participant-task-" + uuid.NewString(), SessionID: state.SessionID,
			ExpectedRevision: &state.Revision, ExpectedControllerEpoch: state.Controller.EpochID},
		Background: true, Handle: string(handle), Role: session.ParticipantRoleSidecar,
		Label: allocateParticipantLabel(state.Participants, string(handle)), Source: source,
		Input: strings.TrimSpace(prompt), ContentParts: parts,
	})
	if err := appserver.CommandMutationError(result, err); err != nil {
		return controlprompt.AgentRunResult{}, err
	}
	if result.Resource == nil || result.Resource.Kind != appserver.CommandResourceParticipantTask || strings.TrimSpace(result.Resource.Ref) == "" {
		return controlprompt.AgentRunResult{}, errors.New("participant start returned no Task identity")
	}
	return controlprompt.AgentRunResult{SessionID: state.SessionID, TaskID: result.Resource.Ref}, nil
}

// ContinueAgentRun queues user input through the same child mailbox used by the
// participant pane. Legacy ACP attachments keep their foreground continuation
// until detached; newly started direct Agents always use Task identities.
func (a *SessionClientAdapter) ContinueAgentRun(ctx context.Context, handle, prompt string, attachments []controlprompt.Attachment) (controlprompt.AgentRunResult, error) {
	state, err := a.currentClientSessionState(ctx)
	if err != nil {
		return controlprompt.AgentRunResult{}, err
	}
	participants := make([]participantAddress, 0, len(state.Participants))
	for _, participant := range state.Participants {
		participants = append(participants, participantAddress{ID: participant.ID, Kind: participant.Kind, Role: participant.Role,
			Label: participant.Label, SessionID: participant.SessionID, Source: participant.Source})
	}
	participantID, err := resolveParticipantID(participants, handle)
	if err != nil {
		return controlprompt.AgentRunResult{}, err
	}
	parts, err := ContentPartsFromSubmission(prompt, attachments, state.CWD)
	if err != nil {
		return controlprompt.AgentRunResult{}, err
	}
	for _, participant := range state.Participants {
		if participant.ID != participantID || participant.Kind != session.ParticipantKindSubagent {
			continue
		}
		if a.subagentInputs == nil {
			return controlprompt.AgentRunResult{}, errors.New("subagent input client is unavailable")
		}
		receipt, err := a.subagentInputs.SubmitSubagentInput(ctx, appserver.SubagentInputRequest{
			OperationID: "participant-input-" + uuid.NewString(), SessionID: state.SessionID,
			ParticipantID: participantID, TaskID: participant.DelegationID, Input: strings.TrimSpace(prompt), ContentParts: parts,
		})
		return controlprompt.AgentRunResult{SessionID: state.SessionID, TaskID: participant.DelegationID, InputReceipt: &receipt}, err
	}
	turn, err := a.startAdmittedTurn(ctx, a.currentClientSessionState, func(startCtx context.Context, current appserver.SessionState) (appserver.TargetTurn, error) {
		return a.participants.Prompt(startCtx, appserver.ParticipantTurnPromptRequest{
			SessionID: current.SessionID, ParticipantID: participantID, Input: strings.TrimSpace(prompt),
			DisplayInput: displayInputWithAttachments(prompt, attachments), DisplayAddress: "/" + strings.TrimPrefix(strings.TrimSpace(handle), "/"),
			ContentParts: parts, Source: "user_side_agent",
		})
	})
	return controlprompt.AgentRunResult{Turn: turn}, err
}

// StartReview starts a persistent Reviewer Task with the fixed workspace review
// prompt. Follow-ups use ContinueAgentRun and retain the same child context.
func (a *SessionClientAdapter) StartReview(ctx context.Context, instructions string, attachments []controlprompt.Attachment) (controlprompt.AgentRunResult, error) {
	prompt, attachmentOffset := controlprompt.ReviewPrompt(instructions)
	parts, err := ContentPartsFromSubmission(prompt, shiftControlAttachments(attachments, attachmentOffset), a.WorkspaceDir())
	if err != nil {
		return controlprompt.AgentRunResult{}, err
	}
	return a.startBackgroundParticipant(ctx, agentbinding.HandleReviewer, "slash_review", prompt, parts)
}

func (a *SessionClientAdapter) currentClientSessionState(ctx context.Context) (appserver.SessionState, error) {
	if a == nil || a.sessionClient == nil {
		return appserver.SessionState{}, errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	sessionID := a.clientSessionID()
	if sessionID == "" {
		return appserver.SessionState{}, errors.New("app/gatewayapp/controladapter: no Session is selected")
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
