package acppermission

import (
	"encoding/json"
	"fmt"
	"strings"

	v2 "github.com/caelis-labs/acp-go-sdk/experimental/v2"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// RequestFromDraft normalizes an explicitly negotiated draft permission into
// the same approval owner as stable ACP. Independent command/unknown/absent
// subjects never acquire a synthesized tool call or grant scope. Exact opaque
// subject evidence stays private; clients receive bounded display facts.
func RequestFromDraft(req v2.RequestPermissionRequest, nativeReqID json.RawMessage) (*session.ProtocolApproval, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(req.SessionId)) == "" || len(req.Options) == 0 {
		return nil, fmt.Errorf("control/acppermission: draft Session and options are required")
	}
	options := make([]approval.Option, 0, len(req.Options))
	for _, option := range req.Options {
		options = append(options, approval.Option{ID: string(option.OptionId), Name: option.Name, Kind: string(option.Kind)})
	}
	if err := approval.ValidateACPOptions(options); err != nil {
		return nil, err
	}
	context := &session.PermissionContext{Title: req.Title, Description: req.Description, NativeRequestID: append(json.RawMessage(nil), nativeReqID...)}
	if hint, exists := req.Meta["caelis/approval_subject"]; exists {
		// The exact action is still derived solely from the standard subject.
		// Ignore malformed or oversized optional presentation claims.
		if raw, err := json.Marshal(hint); err == nil && len(raw) <= 32<<10 {
			var display *session.ApprovalSubject
			if err := json.Unmarshal(raw, &display); err == nil && display != nil {
				context.DisplaySubjectHint = session.CloneApprovalSubject(display)
				context.DisplaySubjectHint.Reported = true
			}
		}
	}
	out := &session.ProtocolApproval{Context: session.ClonePermissionContext(context)}
	for _, option := range options {
		out.Options = append(out.Options, session.ProtocolApprovalOption{ID: option.ID, Name: option.Name, Kind: option.Kind})
	}
	opaque := session.ApprovalField{State: session.ApprovalFieldOpaque}
	subject := &session.ApprovalSubject{Kind: "opaque", Tool: opaque, Operation: opaque, Targets: []session.ApprovalField{opaque}, Effect: opaque, Scope: opaque}
	title := session.KnownApprovalField(req.Title)
	subject.Title = &title
	if req.Description != nil {
		description := session.KnownApprovalField(*req.Description)
		subject.Description = &description
	}
	if req.Subject != nil {
		if err := req.Subject.Validate(); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(req.Subject)
		if err != nil {
			return nil, err
		}
		out.Context.RawSubject = raw
		switch {
		case req.Subject.Command != nil:
			command := req.Subject.Command
			subject.Kind = "command"
			subject.Operation = session.KnownApprovalField("execute")
			subject.Targets = []session.ApprovalField{session.KnownApprovalField(command.Command), session.KnownApprovalField(string(command.Cwd))}
			subject.Effect = session.KnownApprovalField("run the supplied command")
			if command.ToolCallId != nil {
				out.ToolCall.ID = string(*command.ToolCallId)
			}
		case req.Subject.ToolCall != nil:
			subject.OpaqueInput = true
			subject.Title, subject.Description = new(opaque), nil
			encoded, err := json.Marshal(req.Subject.ToolCall.ToolCall)
			if err != nil {
				return nil, err
			}
			var wire eventstream.ToolCallUpdate
			if err := json.Unmarshal(encoded, &wire); err != nil {
				return nil, err
			}
			out.ToolCall = permissionToolCallFromWire(wire)
			if wire.Name != nil {
				out.ToolCall.Name = *wire.Name
				subject.Tool = session.KnownApprovalField(*wire.Name)
			}
		default:
			subject.OpaqueInput = true
			subject.Title, subject.Description = new(opaque), nil
		}
	}
	for _, option := range out.Options {
		subject.Grants = append(subject.Grants, session.ApprovalGrant{OptionID: option.ID, Scope: opaque})
	}
	out.Subject = session.CloneApprovalSubject(subject)
	return out, nil
}

// ResponseToDraft preserves selection of an actually offered original option;
// no interpretation of display facts can manufacture a grant.
func ResponseToDraft(req v2.RequestPermissionRequest, response agent.ApprovalResponse) (v2.RequestPermissionResponse, error) {
	if response.Outcome == "selected" {
		for _, option := range req.Options {
			if string(option.OptionId) == response.OptionID {
				return v2.RequestPermissionResponse{Outcome: v2.RequestPermissionOutcome{Selected: &v2.RequestPermissionOutcomeSelected{Outcome: "selected", OptionId: option.OptionId}}}, nil
			}
		}
		return v2.RequestPermissionResponse{}, fmt.Errorf("control/acppermission: selected draft option was not offered")
	}
	return v2.RequestPermissionResponse{Outcome: v2.RequestPermissionOutcome{Cancelled: &v2.RequestPermissionOutcomeCancelled{Outcome: "cancelled"}}}, nil
}
