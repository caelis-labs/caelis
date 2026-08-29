package acpingress

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func protocolUpdateFromContentChunk(chunk client.ContentChunk) (*session.ProtocolUpdate, error) {
	var content any
	if len(chunk.Content) > 0 {
		if err := json.Unmarshal(chunk.Content, &content); err != nil {
			return nil, fmt.Errorf("internal/acpagentbridge/acpingress: decode content: %w", err)
		}
	}
	return cloneProtocolUpdate(session.ProtocolUpdate{
		SessionUpdate: strings.TrimSpace(chunk.SessionUpdate),
		Content:       content,
		MessageID:     strings.TrimSpace(chunk.MessageID),
		Meta:          chunk.Meta,
	}), nil
}

func protocolUpdateFromToolCall(call client.ToolCall) *session.ProtocolUpdate {
	return cloneProtocolUpdate(session.ProtocolUpdate{
		SessionUpdate: call.SessionUpdate,
		ToolCallID:    call.ToolCallID,
		Title:         call.Title,
		Kind:          call.Kind,
		Status:        call.Status,
		RawInput:      session.NormalizeProtocolRawMap(call.RawInput),
		RawOutput:     session.NormalizeProtocolRawMap(call.RawOutput),
		Content:       protocolToolContentFromWire(call.Content),
		Locations:     protocolLocationsFromWire(call.Locations),
		Meta:          call.Meta,
	})
}

func protocolUpdateFromToolCallUpdate(update client.ToolCallUpdate) *session.ProtocolUpdate {
	return cloneProtocolUpdate(session.ProtocolUpdate{
		SessionUpdate: update.SessionUpdate,
		ToolCallID:    update.ToolCallID,
		Title:         stringValue(update.Title),
		Kind:          stringValue(update.Kind),
		Status:        stringValue(update.Status),
		RawInput:      session.NormalizeProtocolRawMap(update.RawInput),
		RawOutput:     session.NormalizeProtocolRawMap(update.RawOutput),
		Content:       protocolToolContentFromWire(update.Content),
		Locations:     protocolLocationsFromWire(update.Locations),
		Meta:          update.Meta,
	})
}

func protocolUpdateFromPlanUpdate(update client.PlanUpdate) *session.ProtocolUpdate {
	entries := make([]session.ProtocolPlanEntry, 0, len(update.Entries))
	for _, entry := range update.Entries {
		entries = append(entries, session.ProtocolPlanEntry{
			Content:  entry.Content,
			Status:   entry.Status,
			Priority: entry.Priority,
		})
	}
	return cloneProtocolUpdate(session.ProtocolUpdate{
		SessionUpdate: update.SessionUpdate,
		Entries:       entries,
	})
}

func protocolToolContentFromWire(in []client.ToolCallContent) []session.ProtocolToolCallContent {
	if in == nil {
		return nil
	}
	out := make([]session.ProtocolToolCallContent, 0, len(in))
	for _, item := range in {
		out = append(out, session.ProtocolToolCallContent{
			Type:       item.Type,
			Content:    normalizeProtocolWireValue(item.Content),
			TerminalID: item.TerminalID,
			Path:       item.Path,
			OldText:    cloneProtocolString(item.OldText),
			NewText:    item.NewText,
		})
	}
	return out
}

func protocolLocationsFromWire(in []client.ToolCallLocation) []session.ProtocolToolCallLocation {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.ProtocolToolCallLocation, 0, len(in))
	for _, item := range in {
		out = append(out, session.ProtocolToolCallLocation{Path: item.Path, Line: cloneProtocolInt(item.Line)})
	}
	return out
}

func normalizeProtocolWireValue(in any) any {
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

func cloneProtocolUpdate(update session.ProtocolUpdate) *session.ProtocolUpdate {
	return cloneProtocol(&session.EventProtocol{Update: &update}).Update
}

func cloneProtocolString(in *string) *string {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func cloneProtocolInt(in *int) *int {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func stringValue(in *string) string {
	if in == nil {
		return ""
	}
	return *in
}
