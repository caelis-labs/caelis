package semantic

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// DecodePermissionRequest converts the ACP permission wire request into the
// normalized SDK approval semantics plus its session identity and wire meta.
func DecodePermissionRequest(wire schema.RequestPermissionRequest) (session.SessionRef, *session.ProtocolApproval, map[string]any, error) {
	toolCall := permissionToolCallFromWire(wire.ToolCall)
	meta := metautil.Merge(wire.ToolCall.Meta, wire.Meta)
	toolCall.Name = canonicalPermissionToolName(meta, toolCall)
	approval := &session.ProtocolApproval{ToolCall: toolCall}
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
	title := permissionOptionalString(normalized.ToolCall.Title)
	kind := permissionOptionalString(normalized.ToolCall.Kind)
	status := permissionOptionalString(normalized.ToolCall.Status)
	wire := schema.RequestPermissionRequest{
		SessionID: strings.TrimSpace(ref.SessionID),
		ToolCall: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: normalized.ToolCall.ID,
			Title: title, Kind: kind, Status: status,
			RawInput: permissionMapOrNil(normalized.ToolCall.RawInput), RawOutput: permissionMapOrNil(normalized.ToolCall.RawOutput),
			Content: permissionToolContentToWire(normalized.ToolCall.Content),
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

func ptrApproval(in session.ProtocolApproval) *session.ProtocolApproval {
	out := session.CloneProtocolApproval(in)
	return &out
}

func permissionToolCallFromWire(wire schema.ToolCallUpdate) session.ProtocolToolCall {
	var content []session.ProtocolToolCallContent
	if len(wire.Content) > 0 {
		content = make([]session.ProtocolToolCallContent, 0, len(wire.Content))
	}
	for _, item := range wire.Content {
		content = append(content, session.ProtocolToolCallContent{
			Type:       item.Type,
			Content:    normalizePermissionWireValue(item.Content),
			TerminalID: item.TerminalID,
			Path:       item.Path,
			OldText:    permissionStringPointer(item.OldText),
			NewText:    item.NewText,
		})
	}
	return session.ProtocolToolCall{
		ID:        wire.ToolCallID,
		Kind:      permissionStringValue(wire.Kind),
		Title:     permissionStringValue(wire.Title),
		Status:    permissionStringValue(wire.Status),
		RawInput:  schema.NormalizeRawMap(wire.RawInput),
		RawOutput: schema.NormalizeRawMap(wire.RawOutput),
		Content:   content,
	}
}

func permissionToolContentToWire(in []session.ProtocolToolCallContent) []schema.ToolCallContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.ToolCallContent, 0, len(in))
	for _, item := range in {
		out = append(out, schema.ToolCallContent{
			Type:       item.Type,
			Content:    item.Content,
			TerminalID: item.TerminalID,
			Path:       item.Path,
			OldText:    permissionStringPointer(item.OldText),
			NewText:    item.NewText,
		})
	}
	return out
}

func normalizePermissionWireValue(in any) any {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return in
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return in
	}
	return out
}

func permissionOptionalString(in string) *string {
	if in == "" {
		return nil
	}
	value := in
	return &value
}

func permissionStringValue(in *string) string {
	if in == nil {
		return ""
	}
	return *in
}

func permissionStringPointer(in *string) *string {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func permissionMapOrNil(in map[string]any) any {
	if len(in) == 0 {
		return nil
	}
	return in
}

func canonicalPermissionToolName(meta map[string]any, toolCall session.ProtocolToolCall) string {
	if name := metautil.String(meta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName); name != "" {
		return name
	}
	if title := strings.TrimSpace(toolCall.Title); title != "" {
		return title
	}
	return strings.TrimSpace(toolCall.Kind)
}
