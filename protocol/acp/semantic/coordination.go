package semantic

import (
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// DecodePermissionRequest converts the ACP permission wire request into the
// normalized SDK approval semantics plus its session identity and wire meta.
func DecodePermissionRequest(wire schema.RequestPermissionRequest) (session.SessionRef, *session.ProtocolApproval, map[string]any, error) {
	update := decodeToolCallUpdate(wire.ToolCall)
	meta := metautil.Merge(update.Meta, wire.Meta)
	approval := &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{
		ID: update.ToolCallID, Name: canonicalPermissionToolName(meta, update), Kind: update.Kind, Title: update.Title, Status: update.Status,
		RawInput: session.CloneState(update.RawInput), RawOutput: session.CloneState(update.RawOutput),
		Content: session.CloneProtocolToolCallContent(session.ProtocolToolCallContentOf(&update)),
	}}
	for _, option := range wire.Options {
		approval.Options = append(approval.Options, session.ProtocolApprovalOption{ID: strings.TrimSpace(option.OptionID), Name: strings.TrimSpace(option.Name), Kind: strings.TrimSpace(option.Kind)})
	}
	return session.SessionRef{SessionID: strings.TrimSpace(wire.SessionID)}, ptrApproval(*approval), session.CloneState(wire.Meta), nil
}

// EncodePermissionRequest converts normalized SDK approval semantics into the
// standard ACP request_permission wire shape.
func EncodePermissionRequest(ref session.SessionRef, approval *session.ProtocolApproval, meta map[string]any) (schema.RequestPermissionRequest, error) {
	if approval == nil {
		return schema.RequestPermissionRequest{}, fmt.Errorf("protocol/acp/semantic: permission approval is required")
	}
	normalized := session.CloneProtocolApproval(*approval)
	title, kind, status := optionalString(normalized.ToolCall.Title), optionalString(normalized.ToolCall.Kind), optionalString(normalized.ToolCall.Status)
	wire := schema.RequestPermissionRequest{
		SessionID: strings.TrimSpace(ref.SessionID),
		ToolCall: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: normalized.ToolCall.ID,
			Title: title, Kind: kind, Status: status, RawInput: mapOrNil(normalized.ToolCall.RawInput), RawOutput: mapOrNil(normalized.ToolCall.RawOutput),
			Content: encodeToolContent(normalized.ToolCall.Content),
			Meta: metautil.WithRuntimeSection(meta, metautil.RuntimeTool, map[string]any{
				metautil.RuntimeToolName: normalized.ToolCall.Name,
			}),
		},
		Meta: session.CloneState(meta),
	}
	for _, option := range normalized.Options {
		wire.Options = append(wire.Options, schema.PermissionOption{OptionID: option.ID, Name: option.Name, Kind: option.Kind})
	}
	return wire, nil
}

// DecodePermissionResponse converts one wire outcome into the shared runtime
// approval response and derives Approved only from a selected allow option.
func DecodePermissionResponse(wire schema.RequestPermissionResponse, approval *session.ProtocolApproval) agent.ApprovalResponse {
	out := agent.ApprovalResponse{Outcome: strings.TrimSpace(wire.Outcome.Outcome), OptionID: strings.TrimSpace(wire.Outcome.OptionID)}
	if out.Outcome != "selected" || approval == nil {
		return out
	}
	for _, option := range approval.Options {
		if strings.TrimSpace(option.ID) == out.OptionID && strings.HasPrefix(strings.ToLower(strings.TrimSpace(option.Kind)), "allow") {
			out.Approved = true
			break
		}
	}
	return out
}

// EncodePermissionResponse converts the normalized runtime decision to ACP.
func EncodePermissionResponse(response agent.ApprovalResponse) schema.RequestPermissionResponse {
	return schema.RequestPermissionResponse{Outcome: schema.PermissionOutcome{Outcome: strings.TrimSpace(response.Outcome), OptionID: strings.TrimSpace(response.OptionID)}}
}

// DecodeCancelNotification normalizes the standard ACP cancellation identity.
func DecodeCancelNotification(wire schema.CancelNotification) session.SessionRef {
	return session.NormalizeSessionRef(session.SessionRef{SessionID: wire.SessionID})
}

// EncodeCancelNotification converts a normalized session identity to ACP.
func EncodeCancelNotification(ref session.SessionRef) schema.CancelNotification {
	return schema.CancelNotification{SessionID: session.NormalizeSessionRef(ref).SessionID}
}

// EncodeParticipant creates the SDK-owned normalized participant lifecycle
// protocol payload used by built-in and external adapters.
func EncodeParticipant(participant session.ProtocolParticipant) session.EventProtocol {
	return session.NewParticipantProtocol(participant)
}

// DecodeParticipant validates and decodes normalized participant lifecycle.
func DecodeParticipant(protocol session.EventProtocol) (session.ProtocolParticipant, error) {
	protocol = session.CloneEventProtocol(protocol)
	if protocol.Method != session.ProtocolMethodParticipantUpdate || protocol.Update == nil {
		return session.ProtocolParticipant{}, fmt.Errorf("protocol/acp/semantic: invalid participant lifecycle payload")
	}
	return session.ProtocolParticipant{Action: strings.TrimSpace(protocol.Update.SessionUpdate)}, nil
}

// EncodeHandoff creates the normalized handoff fact. It does not authorize or
// commit controller transfer; that authority remains in Control.
func EncodeHandoff(handoff session.ProtocolHandoff) session.EventProtocol {
	return session.NewHandoffProtocol(handoff)
}

// DecodeHandoff validates and decodes one already-authorized Control fact.
func DecodeHandoff(protocol session.EventProtocol) (session.ProtocolHandoff, error) {
	protocol = session.CloneEventProtocol(protocol)
	if protocol.Method != session.ProtocolMethodControllerHandoff || protocol.Update == nil {
		return session.ProtocolHandoff{}, fmt.Errorf("protocol/acp/semantic: invalid handoff lifecycle payload")
	}
	return session.ProtocolHandoff{Phase: strings.TrimSpace(protocol.Update.SessionUpdate)}, nil
}

func ptrApproval(in session.ProtocolApproval) *session.ProtocolApproval {
	out := session.CloneProtocolApproval(in)
	return &out
}

func canonicalPermissionToolName(meta map[string]any, update session.ProtocolUpdate) string {
	if name := metautil.String(meta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName); name != "" {
		return name
	}
	if title := strings.TrimSpace(update.Title); title != "" {
		return title
	}
	return strings.TrimSpace(update.Kind)
}
