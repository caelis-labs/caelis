package appserver

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/collaboration"
)

// SubagentInputRequest queues one user prompt for the exact observed child.
// It never occupies the main Session's foreground Turn.
type SubagentInputRequest struct {
	OperationID   string              `json:"operation_id"`
	SessionID     string              `json:"session_id"`
	ParticipantID string              `json:"participant_id"`
	TaskID        string              `json:"task_id"`
	Input         string              `json:"input"`
	ContentParts  []model.ContentPart `json:"content_parts,omitempty"`
}

// SubagentInputStatusRequest reads only the current principal's input receipts.
type SubagentInputStatusRequest struct {
	SessionID string   `json:"session_id"`
	IDs       []string `json:"ids"`
}

// SubagentInputService authorizes user input without borrowing Agent credentials.
type SubagentInputService struct {
	Authorizer Authorizer
	Mailbox    *collaboration.Service
}

// Submit authorizes and durably queues input for the selected child.
func (s *SubagentInputService) Submit(ctx context.Context, p Principal, req SubagentInputRequest) (collaboration.UserInputStatus, error) {
	if s == nil || s.Authorizer == nil || s.Mailbox == nil {
		return collaboration.UserInputStatus{}, errorcode.New(errorcode.Unavailable, "subagent input unavailable")
	}
	if err := s.Authorizer.Authorize(ctx, p, ActionSubagentInput, req.SessionID); err != nil {
		return collaboration.UserInputStatus{}, err
	}
	if err := validatePromptContent("subagent prompt", req.Input, req.ContentParts); err != nil {
		return collaboration.UserInputStatus{}, errorcode.Wrap(errorcode.InvalidArgument, "Invalid subagent input", err)
	}
	return s.Mailbox.EnqueueUserInput(ctx, req.OperationID, p.ID, req.SessionID, req.ParticipantID, req.TaskID, req.Input, req.ContentParts)
}

// Statuses reads admission receipts belonging to this user and Session.
func (s *SubagentInputService) Statuses(ctx context.Context, p Principal, req SubagentInputStatusRequest) ([]collaboration.UserInputStatus, error) {
	if s == nil || s.Authorizer == nil || s.Mailbox == nil {
		return nil, errorcode.New(errorcode.Unavailable, "subagent input unavailable")
	}
	if err := s.Authorizer.Authorize(ctx, p, ActionSessionInspect, req.SessionID); err != nil {
		return nil, err
	}
	return s.Mailbox.UserInputStatuses(ctx, p.ID, req.SessionID, req.IDs)
}

// SubagentInputClient is the principal-bound user prompt and receipt contract.
type SubagentInputClient interface {
	SubmitSubagentInput(context.Context, SubagentInputRequest) (collaboration.UserInputStatus, error)
	SubagentInputStatuses(context.Context, SubagentInputStatusRequest) ([]collaboration.UserInputStatus, error)
}

type boundSubagentInputClient struct {
	service   *SubagentInputService
	principal Principal
}

func (c *boundSubagentInputClient) SubmitSubagentInput(ctx context.Context, req SubagentInputRequest) (collaboration.UserInputStatus, error) {
	return c.service.Submit(ctx, c.principal, req)
}
func (c *boundSubagentInputClient) SubagentInputStatuses(ctx context.Context, req SubagentInputStatusRequest) ([]collaboration.UserInputStatus, error) {
	return c.service.Statuses(ctx, c.principal, req)
}
