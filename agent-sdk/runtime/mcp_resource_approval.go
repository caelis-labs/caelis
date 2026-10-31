package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func mcpGrantForCall(def tool.Definition, active session.Session, call tool.Call) (MCPGrant, error) {
	grant, err := mcpGrantFor(def, active)
	if err != nil {
		return MCPGrant{}, err
	}
	raw, exists := def.Metadata[tool.MetadataMCPResourceParameter]
	if !exists {
		return grant, nil
	}
	parameter, ok := raw.(string)
	if !ok || strings.TrimSpace(parameter) == "" {
		return MCPGrant{}, errors.New("invalid Host MCP resource parameter")
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(call.Input, &args); err != nil {
		return MCPGrant{}, fmt.Errorf("invalid MCP resource arguments: %w", err)
	}
	var value string
	if err := json.Unmarshal(args[parameter], &value); err != nil || strings.TrimSpace(value) == "" {
		return MCPGrant{}, fmt.Errorf("MCP resource parameter %q must be a non-empty top-level string", parameter)
	}
	// No case folding or trimming: the tool receives this exact identity.
	grant.ResourceParameter, grant.ResourceValue = parameter, value
	return grant, nil
}

func mcpResourceApprovalDecision(input policy.ToolContext, decision policy.Decision, grant MCPGrant) (policy.Decision, error) {
	gate, err := presets.MCPApprovalDecision(input, decision.Constraints)
	if err != nil {
		return policy.Decision{}, err
	}
	resource := fmt.Sprintf("%s=%q", grant.ResourceParameter, grant.ResourceValue)
	gate.Reason = "MCP resource requires approval for this session"
	gate.Approval.ToolCall.Title += " · " + resource
	gate.Approval.Options = []session.ProtocolApprovalOption{
		{ID: "allow_session", Name: "Allow this resource for this session", Kind: "allow_once"},
		{ID: "cancel", Name: "Cancel", Kind: "reject_once"},
	}
	gate.Approval.Subject.Targets = append(gate.Approval.Subject.Targets, session.KnownApprovalField(resource))
	gate.Approval.Subject.Scope = session.KnownApprovalField("this resource, MCP tool, source and session")
	gate.Approval.Subject.Grants = []session.ApprovalGrant{
		{OptionID: "allow_session", Scope: session.KnownApprovalField("this resource for this session")},
		{OptionID: "cancel", Scope: session.KnownApprovalField("no grant")},
	}
	return gate, nil
}

func (t policyWrappedTool) callWithMCPResourceApproval(ctx context.Context, call tool.Call, input policy.ToolContext, decision policy.Decision, grant MCPGrant) (tool.Result, error) {
	gate, err := mcpResourceApprovalDecision(input, decision, grant)
	if err != nil {
		return tool.Result{}, err
	}
	response, err := t.mcpGrants.resolveResourceApproval(ctx, grant, t.sessionRef.SessionID, func() (agent.ApprovalResponse, error) {
		if t.approval.requester == nil && t.approval.runtime == nil {
			return agent.ApprovalResponse{}, nil
		}
		request := t.approvalRequest(call, gate)
		if request.Metadata == nil {
			request.Metadata = make(map[string]any)
		}
		request.Metadata[approval.MetadataMCPResourceGate] = true
		response, err := t.resolveApproval(ctx, request)
		if err != nil {
			return agent.ApprovalResponse{}, err
		}
		// Commit the grant through the same strict response and revocation checks
		// as ordinary MCP approval. Shared approval never shares tool execution.
		_, err = t.resolveMCPApproval(ctx, call, call, gate, response, grant, func(context.Context, tool.Call) (tool.Result, error) { return tool.Result{}, nil })
		return response, err
	})
	if err != nil {
		return tool.Result{}, err
	}
	if response.Outcome != "selected" || !response.Approved {
		return policyDecisionResultWithOutcome(call, input.Tool, gate, response), nil
	}
	// Every caller retains its own explicit Ask/Deny and original arguments.
	return t.applyPolicyDecision(ctx, call, decision)
}

// A pending resource approval is coordination inside the Host grant authority,
// not another grant cache. Only the existing durable grant map allows reuse.
type mcpResourceApproval struct {
	done     chan struct{}
	response agent.ApprovalResponse
	err      error
}

func (s *MCPGrantStore) resolveResourceApproval(ctx context.Context, grant MCPGrant, sessionID string, resolve func() (agent.ApprovalResponse, error)) (agent.ApprovalResponse, error) {
	if err := ctx.Err(); err != nil {
		return agent.ApprovalResponse{}, err
	}
	if s == nil {
		return agent.ApprovalResponse{}, errors.New("MCP grant store is unavailable")
	}
	key, err := grant.key("session", sessionID)
	if err != nil {
		return agent.ApprovalResponse{}, err
	}
	s.mu.Lock()
	if s.grants[key] {
		s.mu.Unlock()
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
	}
	if pending := s.resourceApprovals[key]; pending != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return agent.ApprovalResponse{}, ctx.Err()
		case <-pending.done:
			return pending.response, pending.err
		}
	}
	pending := &mcpResourceApproval{done: make(chan struct{})}
	if s.resourceApprovals == nil {
		s.resourceApprovals = make(map[string]*mcpResourceApproval)
	}
	s.resourceApprovals[key] = pending
	s.mu.Unlock()
	var response agent.ApprovalResponse
	// Release waiters even if an embedding requester panics. An incomplete
	// response cannot authorize a call, and the leader's panic still propagates.
	defer func() {
		s.mu.Lock()
		pending.response, pending.err = response, err
		delete(s.resourceApprovals, key)
		close(pending.done)
		s.mu.Unlock()
	}()
	response, err = resolve()
	return response, err
}
