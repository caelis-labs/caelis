package acppermission

import (
	"encoding/json"
	"fmt"
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	sdkapproval "github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

// DecodePermissionRequest converts the ACP permission wire request into
// normalized SDK approval semantics.
func DecodePermissionRequest(wire eventstream.RequestPermissionRequest) (*session.ProtocolApproval, error) {
	strictOptions := make([]sdkapproval.Option, 0, len(wire.Options))
	for _, option := range wire.Options {
		strictOptions = append(strictOptions, sdkapproval.Option{
			ID:   string(option.OptionId),
			Name: option.Name,
			Kind: string(option.Kind),
		})
	}
	if err := sdkapproval.ValidateACPOptions(strictOptions); err != nil {
		return nil, fmt.Errorf("control/acppermission: invalid permission options: %w", err)
	}
	toolCall := permissionToolCallFromWire(wire.ToolCall)
	meta := metautil.Merge(wire.ToolCall.Meta, wire.Meta)
	toolCall.Name = canonicalPermissionToolName(meta, toolCall)
	approval := &session.ProtocolApproval{ToolCall: toolCall}
	for _, option := range wire.Options {
		approval.Options = append(approval.Options, session.ProtocolApprovalOption{
			ID:   strings.TrimSpace(string(option.OptionId)),
			Name: strings.TrimSpace(option.Name),
			Kind: strings.TrimSpace(string(option.Kind)),
		})
	}
	return approval, nil
}

// EncodePermissionRequest converts normalized SDK approval semantics into the
// standard ACP request_permission wire shape.
func EncodePermissionRequest(ref session.SessionRef, approval *session.ProtocolApproval, meta map[string]any) (eventstream.RequestPermissionRequest, error) {
	if approval == nil {
		return eventstream.RequestPermissionRequest{}, fmt.Errorf("control/acppermission: permission approval is required")
	}
	normalized := session.CloneProtocolApproval(*approval)
	title := permissionOptionalString(normalized.ToolCall.Title)
	kind := permissionOptionalString(normalized.ToolCall.Kind)
	status := permissionOptionalString(normalized.ToolCall.Status)
	wire := eventstream.RequestPermissionRequest{
		SessionID: strings.TrimSpace(ref.SessionID),
		ToolCall: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: normalized.ToolCall.ID,
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
		wire.Options = append(wire.Options, acpsdk.PermissionOption{
			OptionId: acpsdk.PermissionOptionId(option.ID),
			Name:     option.Name,
			Kind:     acpsdk.PermissionOptionKind(option.Kind),
		})
	}
	return wire, nil
}

func permissionToolCallFromWire(wire eventstream.ToolCallUpdate) session.ProtocolToolCall {
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
		RawInput:  session.NormalizeProtocolRawMap(wire.RawInput),
		RawOutput: session.NormalizeProtocolRawMap(wire.RawOutput),
		Content:   content,
	}
}

func permissionToolContentToWire(in []session.ProtocolToolCallContent) []eventstream.ToolCallContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]eventstream.ToolCallContent, 0, len(in))
	for _, item := range in {
		out = append(out, eventstream.ToolCallContent{
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
