package acp

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/OnslaughtSnail/caelis/session"
)

func TestNormalizeExternalEvent_UserChunk(t *testing.T) {
	update := ContentChunk{
		SessionUpdate: UpdateUserMessage,
		Content:       TextContent{Type: "text", Text: "hello from external"},
	}
	e := NormalizeExternalEvent("sess-1", update)
	if e == nil {
		t.Fatal("expected non-nil event")
	}
	if e.Kind != session.EventKindUser {
		t.Errorf("kind: %q", e.Kind)
	}
	if e.SessionRef.SessionID != "sess-1" {
		t.Errorf("session id: %q", e.SessionRef.SessionID)
	}
	if e.UserPayload == nil || e.UserPayload.Parts[0].Text != "hello from external" {
		t.Errorf("text: %v", e.UserPayload)
	}
}

func TestNormalizeExternalEvent_AgentChunk(t *testing.T) {
	update := ContentChunk{
		SessionUpdate: UpdateAgentMessage,
		Content:       TextContent{Type: "text", Text: "reply"},
	}
	e := NormalizeExternalEvent("sess-1", update)
	if e == nil || e.Kind != session.EventKindAssistant {
		t.Fatal("expected assistant event")
	}
	if e.AssistantPayload.Parts[0].Text != "reply" {
		t.Errorf("text: %q", e.AssistantPayload.Parts[0].Text)
	}
}

func TestNormalizeExternalEvent_AgentChunkFinalFalseIsUIOnly(t *testing.T) {
	final := false
	update := ContentChunk{
		SessionUpdate: UpdateAgentMessage,
		Content:       TextContent{Type: "text", Text: "partial"},
		Final:         &final,
	}
	e := NormalizeExternalEvent("sess-1", update)
	if e == nil || e.Kind != session.EventKindAssistant {
		t.Fatal("expected assistant event")
	}
	if e.Visibility != session.VisibilityUIOnly {
		t.Fatalf("visibility = %q, want ui_only", e.Visibility)
	}
	if e.AssistantPayload.Parts[0].Text != "partial" {
		t.Fatalf("text = %q", e.AssistantPayload.Parts[0].Text)
	}
}

func TestNormalizeExternalUpdateJSON_DecodesContentChunkAndFinal(t *testing.T) {
	raw := json.RawMessage(`{
		"sessionUpdate":"agent_message_chunk",
		"content":{"type":"text","text":"partial"},
		"final":false
	}`)
	e, err := NormalizeExternalUpdateJSON("sess-1", raw)
	if err != nil {
		t.Fatalf("NormalizeExternalUpdateJSON error = %v", err)
	}
	if e == nil || e.Kind != session.EventKindAssistant {
		t.Fatal("expected assistant event")
	}
	if e.Visibility != session.VisibilityUIOnly {
		t.Fatalf("visibility = %q, want ui_only", e.Visibility)
	}
	if e.AssistantPayload.Parts[0].Text != "partial" {
		t.Fatalf("text = %q", e.AssistantPayload.Parts[0].Text)
	}
}

func TestNormalizeExternalEvent_PreservesNonTextMessageContent(t *testing.T) {
	update := ContentChunk{
		SessionUpdate: UpdateUserMessage,
		Content: []any{
			map[string]any{"type": "image", "mimeType": "image/png", "uri": "file:///tmp/a.png"},
			map[string]any{"type": "embedded_context", "value": map[string]any{"k": "v"}},
		},
	}
	e := NormalizeExternalEvent("sess-1", update)
	if e == nil || e.Kind != session.EventKindUser {
		t.Fatal("expected user event")
	}
	if len(e.UserPayload.Parts) != 2 {
		t.Fatalf("parts: %#v", e.UserPayload.Parts)
	}
	if e.UserPayload.Parts[0].Kind != session.PartKindMedia {
		t.Fatalf("first part kind: %q", e.UserPayload.Parts[0].Kind)
	}
	if e.UserPayload.Parts[0].Media.URI != "file:///tmp/a.png" || e.UserPayload.Parts[0].Media.MIMEType != "image/png" {
		t.Fatalf("media part: %#v", e.UserPayload.Parts[0].Media)
	}
	if e.UserPayload.Parts[1].Kind != session.PartKindJSON {
		t.Fatalf("second part kind: %q", e.UserPayload.Parts[1].Kind)
	}
	jsonPart, ok := e.UserPayload.Parts[1].JSON.(map[string]any)
	if !ok {
		t.Fatalf("json part: %#v", e.UserPayload.Parts[1].JSON)
	}
	value, ok := jsonPart["value"].(map[string]any)
	if !ok || value["k"] != "v" {
		t.Fatalf("json value: %#v", jsonPart)
	}
}

func TestNormalizeExternalEvent_ThoughtChunk(t *testing.T) {
	update := ContentChunk{
		SessionUpdate: UpdateAgentThought,
		Content:       TextContent{Type: "text", Text: "thinking"},
	}
	e := NormalizeExternalEvent("sess-1", update)
	if e == nil || e.Kind != session.EventKindAssistant {
		t.Fatal("expected assistant event")
	}
	if e.AssistantPayload.Parts[0].Kind != session.PartKindReasoning {
		t.Errorf("kind: %q", e.AssistantPayload.Parts[0].Kind)
	}
}

func TestNormalizeExternalEvent_ToolCallUpdate(t *testing.T) {
	update := ToolCallUpdate{
		SessionUpdate: UpdateToolCall,
		ToolCallID:    "c1",
		Title:         "READ",
		Status:        "pending",
		RawInput:      map[string]any{"path": "/tmp"},
	}
	e := NormalizeExternalEvent("sess-1", update)
	if e == nil || e.Kind != session.EventKindToolCall {
		t.Fatal("expected tool_call event")
	}
	if e.ToolCallPayload.CallID != "c1" {
		t.Errorf("callid: %q", e.ToolCallPayload.CallID)
	}
	if e.Args["path"] != "/tmp" {
		t.Errorf("args: %v", e.Args)
	}
}

func TestNormalizeExternalEvent_ToolCallPreservesRawInputAndMeta(t *testing.T) {
	line := 42
	update := ToolCallUpdate{
		SessionUpdate: UpdateToolCall,
		ToolCallID:    "c1",
		Title:         "SHELL",
		Status:        "pending",
		RawInput:      []any{"echo", "hi"},
		Locations:     []ToolCallLocation{{Path: "src/main.go", Line: &line}},
		Meta:          map[string]any{"caelis": map[string]any{"run_id": "run-1"}},
	}
	e := NormalizeExternalEvent("sess-1", update)
	if e == nil || e.Kind != session.EventKindToolCall {
		t.Fatal("expected tool_call event")
	}
	if e.SessionRef.SessionID != "sess-1" {
		t.Fatalf("session id: %q", e.SessionRef.SessionID)
	}
	if e.ArgJSON != `["echo","hi"]` {
		t.Fatalf("arg json: %q", e.ArgJSON)
	}
	if e.Args != nil {
		t.Fatalf("non-object raw input should not be coerced into args: %#v", e.Args)
	}
	if e.ProviderMeta["acp_meta"] == nil {
		t.Fatalf("expected acp meta: %#v", e.ProviderMeta)
	}
	locations, ok := e.ProviderMeta["acp_locations"].([]ToolCallLocation)
	if !ok || len(locations) != 1 || locations[0].Path != "src/main.go" || locations[0].Line == nil || *locations[0].Line != line {
		t.Fatalf("locations: %#v", e.ProviderMeta["acp_locations"])
	}
}

func TestNormalizeExternalEvent_ToolResultUpdate(t *testing.T) {
	update := ToolCallUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    "c1",
		Status:        "completed",
		RawOutput:     "file contents",
	}
	e := NormalizeExternalEvent("sess-1", update)
	if e == nil || e.Kind != session.EventKindToolResult {
		t.Fatal("expected tool_result event")
	}
	if e.ToolResultPayload.CallID != "c1" {
		t.Errorf("callid: %q", e.ToolResultPayload.CallID)
	}
}

func TestNormalizeExternalEvent_ToolResultPreservesStructuredRawOutput(t *testing.T) {
	update := ToolCallUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    "c1",
		Status:        "completed",
		RawOutput:     map[string]any{"ok": true},
	}
	e := NormalizeExternalEvent("sess-1", update)
	if e == nil || e.Kind != session.EventKindToolResult {
		t.Fatal("expected tool_result event")
	}
	if len(e.Content) != 1 {
		t.Fatalf("content: %#v", e.Content)
	}
	part := e.Content[0]
	if part.Kind != session.PartKindJSON {
		t.Fatalf("part kind: %q", part.Kind)
	}
	obj, ok := part.JSON.(map[string]any)
	if !ok || obj["ok"] != true {
		t.Fatalf("json part: %#v", part.JSON)
	}
}

func TestNormalizeExternalEvent_ToolResultUsesACPContentWhenRawOutputMissing(t *testing.T) {
	update := ToolCallUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    "c1",
		Status:        "completed",
		Content: []ToolCallContent{{
			Type:    "text",
			Content: "visible output",
		}},
	}
	e := NormalizeExternalEvent("sess-1", update)
	if e == nil || e.Kind != session.EventKindToolResult {
		t.Fatal("expected tool_result event")
	}
	if len(e.Content) != 1 || e.Content[0].Text != "visible output" {
		t.Fatalf("content: %#v", e.Content)
	}
}

func TestNormalizeExternalEvent_PlanUpdate(t *testing.T) {
	update := PlanUpdate{
		SessionUpdate: UpdatePlan,
		Entries:       []PlanEntry{{Content: "step 1", Status: "pending"}},
	}
	e := NormalizeExternalEvent("sess-1", update)
	if e == nil || e.Kind != session.EventKindPlan {
		t.Fatal("expected plan event")
	}
	if len(e.Entries) != 1 || e.Entries[0].Content != "step 1" {
		t.Errorf("entries: %v", e.Entries)
	}
}

func TestNormalizeExternalEvent_NilUpdate(t *testing.T) {
	if e := NormalizeExternalEvent("sess-1", nil); e != nil {
		t.Error("expected nil")
	}
}

func TestNormalizeExternalEvent_UnknownUpdate(t *testing.T) {
	// An update type that doesn't map to a session event.
	if e := NormalizeExternalEvent("sess-1", ContentChunk{SessionUpdate: "unknown"}); e != nil {
		t.Error("expected nil for unknown update type")
	}
}

func TestNormalizeRoundTrip(t *testing.T) {
	// Project a session event to ACP, then normalize back.
	original := &session.Event{
		Kind: session.EventKindUser,
		UserPayload: &session.UserPayload{
			Parts: []session.EventPart{{Kind: session.PartKindText, Text: "round trip"}},
		},
	}
	updates := ProjectEvent(original)
	if len(updates) == 0 {
		t.Fatal("no updates")
	}

	normalized := NormalizeExternalEvent("sess", updates[0])
	if normalized == nil {
		t.Fatal("normalization returned nil")
	}
	if normalized.Kind != session.EventKindUser {
		t.Errorf("kind: %q", normalized.Kind)
	}
	if normalized.UserPayload.Parts[0].Text != "round trip" {
		t.Errorf("text: %q", normalized.UserPayload.Parts[0].Text)
	}
}

func TestNormalizeRoundTrip_StructuredMessageContent(t *testing.T) {
	original := &session.Event{
		Kind: session.EventKindUser,
		UserPayload: &session.UserPayload{
			Parts: []session.EventPart{
				{Kind: session.PartKindMedia, Media: &session.PartMedia{
					Modality: "image",
					MIMEType: "image/png",
					Data:     []byte("png-bytes"),
					URI:      "file:///tmp/a.png",
				}},
				{Kind: session.PartKindFileRef, FileRef: &session.PartFileRef{
					URI: "file:///tmp/a.txt", MIMEType: "text/plain", Name: "a.txt",
				}},
				{Kind: session.PartKindJSON, JSON: map[string]any{"k": "v"}},
			},
		},
	}
	updates := ProjectEvent(original)
	if len(updates) != 1 {
		t.Fatalf("updates: %d", len(updates))
	}
	normalized := NormalizeExternalEvent("sess", updates[0])
	if normalized == nil || normalized.UserPayload == nil || len(normalized.UserPayload.Parts) != 3 {
		t.Fatalf("normalized: %#v", normalized)
	}
	media := normalized.UserPayload.Parts[0].Media
	if media == nil || media.URI != "file:///tmp/a.png" || media.MIMEType != "image/png" || string(media.Data) != "png-bytes" {
		t.Fatalf("media: %#v", media)
	}
	file := normalized.UserPayload.Parts[1].FileRef
	if file == nil || file.URI != "file:///tmp/a.txt" || file.MIMEType != "text/plain" || file.Name != "a.txt" {
		t.Fatalf("file: %#v", file)
	}
	value, ok := normalized.UserPayload.Parts[2].JSON.(map[string]any)
	if !ok || value["k"] != "v" {
		t.Fatalf("json: %#v", normalized.UserPayload.Parts[2].JSON)
	}
}

func TestNormalizeExternalEvent_DecodesMediaBase64(t *testing.T) {
	update := ContentChunk{
		SessionUpdate: UpdateUserMessage,
		Content: map[string]any{
			"type":     "image",
			"mimeType": "image/png",
			"data":     base64.StdEncoding.EncodeToString([]byte("png-bytes")),
		},
	}
	e := NormalizeExternalEvent("sess", update)
	if e == nil || len(e.UserPayload.Parts) != 1 {
		t.Fatalf("event: %#v", e)
	}
	media := e.UserPayload.Parts[0].Media
	if media == nil || string(media.Data) != "png-bytes" {
		t.Fatalf("media: %#v", media)
	}
}

func TestNormalizeRoundTrip_ToolArgJSONAndLocations(t *testing.T) {
	line := 7
	original := &session.Event{
		Kind:         session.EventKindToolCall,
		ProviderMeta: map[string]any{"acp_locations": []ToolCallLocation{{Path: "src/main.go", Line: &line}}},
		ToolCallPayload: &session.ToolCallPayload{
			CallID: "tc-1", Name: "RUN_COMMAND", Status: "pending", ArgJSON: `["echo","hi"]`,
		},
	}
	update := ProjectEvent(original)[0]
	normalized := NormalizeExternalEvent("sess", update)
	if normalized == nil || normalized.ToolCallPayload == nil {
		t.Fatalf("normalized: %#v", normalized)
	}
	if normalized.ArgJSON != `["echo","hi"]` || normalized.Args != nil {
		t.Fatalf("tool args: args=%#v argJSON=%q", normalized.Args, normalized.ArgJSON)
	}
	locations, ok := normalized.ProviderMeta["acp_locations"].([]ToolCallLocation)
	if !ok || len(locations) != 1 || locations[0].Path != "src/main.go" || locations[0].Line == nil || *locations[0].Line != line {
		t.Fatalf("locations: %#v", normalized.ProviderMeta["acp_locations"])
	}
}
