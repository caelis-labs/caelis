package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/acp"
	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/session"
)

// ChildPermissionRequest describes a permission request from a child ACP agent.
type ChildPermissionRequest struct {
	ParentSessionRef   session.Ref
	ParentCallID       string
	TaskID             string
	DelegationID       string
	ChildParticipantID string
	RemoteACPSessionID string
	ToolCallID         string
	ToolName           string
	ToolKind           string
	RawInput           json.RawMessage
}

// BridgePermission bridges a child ACP request_permission to a parent
// approval request. It translates the child request into an
// agent.ApprovalRequest, calls the parent's ApprovalRequester, and
// maps the response back to an acp.PermissionOutcome.
func (o *Orchestrator) BridgePermission(ctx context.Context, req ChildPermissionRequest) (acp.PermissionOutcome, error) {
	if o.cfg.Approver == nil {
		// No approver configured — auto-approve.
		return acp.PermissionOutcome{
			Outcome:  "selected",
			OptionID: "allow_once",
		}, nil
	}

	// Build the approval request with full parent context.
	var args map[string]any
	if len(req.RawInput) > 0 {
		_ = json.Unmarshal(req.RawInput, &args)
	}

	approvalReq := agent.ApprovalRequest{
		ToolName: req.ToolName,
		CallID:   req.ToolCallID,
		Args:     args,
		Reason:   fmt.Sprintf("child agent %q requested permission", req.ChildParticipantID),
		RunID:    req.ParentCallID,
	}

	resp, err := o.cfg.Approver.RequestApproval(ctx, approvalReq)
	if err != nil {
		return acp.PermissionOutcome{}, fmt.Errorf("orchestrator: approval request failed: %w", err)
	}

	// Map approval response to permission outcome.
	if resp.Approved {
		return acp.PermissionOutcome{
			Outcome:  "selected",
			OptionID: "allow_once",
		}, nil
	}
	return acp.PermissionOutcome{
		Outcome:  "selected",
		OptionID: "reject_once",
	}, nil
}

// BuildChildPermissionRequest constructs a ChildPermissionRequest from
// an ACP permission request and the child handle context.
func BuildChildPermissionRequest(
	parentRef session.Ref,
	handle *ChildHandle,
	permReq acp.RequestPermissionRequest,
) ChildPermissionRequest {
	anchor := handle.Anchor()
	toolName := ""
	if permReq.ToolCall.Title != "" {
		toolName = permReq.ToolCall.Title
	} else if permReq.ToolCall.Kind != "" {
		toolName = permReq.ToolCall.Kind
	} else {
		toolName = strings.TrimSpace(permReq.ToolCall.ToolCallID)
	}

	return ChildPermissionRequest{
		ParentSessionRef:   parentRef,
		ParentCallID:       anchor.ParentCallID,
		TaskID:             anchor.TaskID,
		DelegationID:       anchor.AgentID,
		ChildParticipantID: anchor.AgentName,
		RemoteACPSessionID: anchor.RemoteACPSessionID,
		ToolCallID:         permReq.ToolCall.ToolCallID,
		ToolName:           toolName,
		ToolKind:           permReq.ToolCall.Kind,
		RawInput:           marshalRaw(permReq.ToolCall.RawInput),
	}
}

func marshalRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}
