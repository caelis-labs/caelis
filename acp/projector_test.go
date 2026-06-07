package acp

import (
	"encoding/base64"
	"testing"

	"github.com/OnslaughtSnail/caelis/session"
)

func TestProjectEvent_UserMessage(t *testing.T) {
	e := &session.Event{
		Kind: session.EventKindUser,
		UserPayload: &session.UserPayload{
			Parts: []session.EventPart{{Kind: session.PartKindText, Text: "hello"}},
		},
	}
	updates := ProjectEvent(e)
	if len(updates) != 1 {
		t.Fatalf("got %d updates, want 1", len(updates))
	}
	chunk, ok := updates[0].(ContentChunk)
	if !ok {
		t.Fatalf("expected ContentChunk, got %T", updates[0])
	}
	if chunk.SessionUpdate != UpdateUserMessage {
		t.Errorf("type: got %q", chunk.SessionUpdate)
	}
	tc, ok := chunk.Content.(TextContent)
	if !ok || tc.Text != "hello" {
		t.Errorf("content: %v", chunk.Content)
	}
}

func TestProjectEvent_UserPreservesStructuredParts(t *testing.T) {
	e := &session.Event{
		Kind: session.EventKindUser,
		UserPayload: &session.UserPayload{
			Parts: []session.EventPart{
				{Kind: session.PartKindText, Text: "see "},
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
	updates := ProjectEvent(e)
	if len(updates) != 1 {
		t.Fatalf("got %d updates, want 1", len(updates))
	}
	chunk := updates[0].(ContentChunk)
	content, ok := chunk.Content.([]any)
	if !ok || len(content) != 4 {
		t.Fatalf("content: %#v", chunk.Content)
	}
	media, ok := content[1].(map[string]any)
	if !ok {
		t.Fatalf("media content: %#v", content[1])
	}
	if media["type"] != "image" || media["mimeType"] != "image/png" || media["uri"] != "file:///tmp/a.png" {
		t.Fatalf("media: %#v", media)
	}
	if media["data"] != base64.StdEncoding.EncodeToString([]byte("png-bytes")) {
		t.Fatalf("media data: %#v", media["data"])
	}
	file, ok := content[2].(map[string]any)
	if !ok || file["type"] != "file_ref" || file["uri"] != "file:///tmp/a.txt" || file["name"] != "a.txt" {
		t.Fatalf("file ref: %#v", content[2])
	}
	jsonPart, ok := content[3].(map[string]any)
	if !ok || jsonPart["type"] != "json" {
		t.Fatalf("json part: %#v", content[3])
	}
}

func TestProjectEvent_AssistantText(t *testing.T) {
	e := &session.Event{
		Kind: session.EventKindAssistant,
		AssistantPayload: &session.AssistantPayload{
			Parts: []session.EventPart{{Kind: session.PartKindText, Text: "reply"}},
		},
	}
	updates := ProjectEvent(e)
	if len(updates) != 1 {
		t.Fatalf("got %d", len(updates))
	}
	chunk := updates[0].(ContentChunk)
	if chunk.SessionUpdate != UpdateAgentMessage {
		t.Errorf("type: %q", chunk.SessionUpdate)
	}
}

func TestProjectEvent_AssistantReasoningAndText(t *testing.T) {
	e := &session.Event{
		Kind: session.EventKindAssistant,
		AssistantPayload: &session.AssistantPayload{
			Parts: []session.EventPart{
				{Kind: session.PartKindReasoning, Text: "thinking..."},
				{Kind: session.PartKindText, Text: "answer"},
			},
		},
	}
	updates := ProjectEvent(e)
	if len(updates) != 2 {
		t.Fatalf("got %d, want 2", len(updates))
	}
	if updates[0].(ContentChunk).SessionUpdate != UpdateAgentThought {
		t.Errorf("first: %q", updates[0].(ContentChunk).SessionUpdate)
	}
	if updates[1].(ContentChunk).SessionUpdate != UpdateAgentMessage {
		t.Errorf("second: %q", updates[1].(ContentChunk).SessionUpdate)
	}
}

func TestProjectEvent_ToolCall(t *testing.T) {
	e := &session.Event{
		Kind: session.EventKindToolCall,
		ToolCallPayload: &session.ToolCallPayload{
			CallID: "c1", Name: "READ", Status: "pending",
			Args: map[string]any{"path": "/tmp"},
		},
	}
	updates := ProjectEvent(e)
	if len(updates) != 1 {
		t.Fatalf("got %d", len(updates))
	}
	tc, ok := updates[0].(ToolCallUpdate)
	if !ok {
		t.Fatalf("expected ToolCallUpdate, got %T", updates[0])
	}
	if tc.SessionUpdate != UpdateToolCall {
		t.Errorf("type: %q", tc.SessionUpdate)
	}
	if tc.ToolCallID != "c1" {
		t.Errorf("id: %q", tc.ToolCallID)
	}
	if tc.Status != "pending" {
		t.Errorf("status: %q", tc.Status)
	}
	if tc.Kind != "read" {
		t.Errorf("kind: %q", tc.Kind)
	}
}

func TestProjectEvent_ToolCallArgJSONAndLocations(t *testing.T) {
	line := 12
	e := &session.Event{
		Kind:         session.EventKindToolCall,
		ProviderMeta: map[string]any{"acp_locations": []ToolCallLocation{{Path: "src/main.go", Line: &line}}},
		ToolCallPayload: &session.ToolCallPayload{
			CallID:  "c1",
			Name:    "RUN_COMMAND",
			Status:  "pending",
			ArgJSON: `["echo","hi"]`,
		},
	}
	updates := ProjectEvent(e)
	tc := updates[0].(ToolCallUpdate)
	input, ok := tc.RawInput.([]any)
	if !ok || len(input) != 2 || input[0] != "echo" || input[1] != "hi" {
		t.Fatalf("raw input: %#v", tc.RawInput)
	}
	if len(tc.Locations) != 1 || tc.Locations[0].Path != "src/main.go" || tc.Locations[0].Line == nil || *tc.Locations[0].Line != line {
		t.Fatalf("locations: %#v", tc.Locations)
	}
}

func TestProjectEvent_ToolMetaUsesCaelisNamespace(t *testing.T) {
	e := &session.Event{
		Kind:  session.EventKindToolCall,
		RunID: "run-1",
		Actor: session.ActorRef{
			Scope:  "main",
			Source: "model",
		},
		ToolCallPayload: &session.ToolCallPayload{
			CallID: "c1", Name: "RUN_COMMAND", Status: "pending",
			Display: []session.EventPart{{Kind: session.PartKindText, Text: "display"}},
		},
	}
	updates := ProjectEvent(e)
	tc := updates[0].(ToolCallUpdate)
	if _, ok := tc.Meta["run_id"]; ok {
		t.Fatalf("run_id leaked at _meta top level: %#v", tc.Meta)
	}
	caelis, ok := tc.Meta["caelis"].(map[string]any)
	if !ok {
		t.Fatalf("_meta.caelis missing: %#v", tc.Meta)
	}
	if caelis["run_id"] != "run-1" || caelis["scope"] != "main" || caelis["source"] != "model" {
		t.Fatalf("_meta.caelis = %#v", caelis)
	}
	if _, ok := caelis["display"]; !ok {
		t.Fatalf("_meta.caelis.display missing: %#v", caelis)
	}
}

func TestProjectEvent_ToolResult(t *testing.T) {
	e := &session.Event{
		Kind: session.EventKindToolResult,
		ToolResultPayload: &session.ToolResultPayload{
			CallID: "c1", Name: "READ", Status: "completed",
			Content: []session.EventPart{{Kind: session.PartKindText, Text: "file contents"}},
		},
	}
	updates := ProjectEvent(e)
	if len(updates) != 1 {
		t.Fatalf("got %d", len(updates))
	}
	tc, ok := updates[0].(ToolCallUpdate)
	if !ok {
		t.Fatalf("expected ToolCallUpdate, got %T", updates[0])
	}
	if tc.SessionUpdate != UpdateToolCallInfo {
		t.Errorf("type: %q", tc.SessionUpdate)
	}
	if tc.ToolCallID != "c1" {
		t.Errorf("id: %q", tc.ToolCallID)
	}
	if tc.RawOutput != "file contents" {
		t.Errorf("output: %v", tc.RawOutput)
	}
}

func TestProjectEvent_ToolResultPreservesStructuredParts(t *testing.T) {
	e := &session.Event{
		Kind: session.EventKindToolResult,
		ToolResultPayload: &session.ToolResultPayload{
			CallID: "c1", Name: "READ", Status: "completed",
			Content: []session.EventPart{
				{Kind: session.PartKindJSON, JSON: map[string]any{"ok": true}},
				{Kind: session.PartKindFileRef, FileRef: &session.PartFileRef{URI: "file:///tmp/out.json", MIMEType: "application/json"}},
			},
		},
	}
	updates := ProjectEvent(e)
	tc := updates[0].(ToolCallUpdate)
	output, ok := tc.RawOutput.([]any)
	if !ok || len(output) != 2 {
		t.Fatalf("raw output: %#v", tc.RawOutput)
	}
	jsonPart, ok := output[0].(map[string]any)
	if !ok || jsonPart["type"] != "json" {
		t.Fatalf("json part: %#v", output[0])
	}
	filePart, ok := output[1].(map[string]any)
	if !ok || filePart["type"] != "file_ref" || filePart["uri"] != "file:///tmp/out.json" {
		t.Fatalf("file part: %#v", output[1])
	}
}

func TestProjectEvent_ToolResultError(t *testing.T) {
	e := &session.Event{
		Kind: session.EventKindToolResult,
		ToolResultPayload: &session.ToolResultPayload{
			CallID: "c1", Status: "completed", IsError: true,
			Content: []session.EventPart{{Kind: session.PartKindText, Text: "fail"}},
		},
	}
	updates := ProjectEvent(e)
	tc := updates[0].(ToolCallUpdate)
	if tc.Status != "failed" {
		t.Errorf("status: %q, want failed", tc.Status)
	}
}

func TestProjectEvent_Plan(t *testing.T) {
	e := &session.Event{
		Kind: session.EventKindPlan,
		PlanPayload: &session.PlanPayload{
			Entries: []session.PlanEntry{
				{Content: "step 1", Status: "completed"},
				{Content: "step 2", Status: "pending"},
			},
		},
	}
	updates := ProjectEvent(e)
	if len(updates) != 1 {
		t.Fatalf("got %d", len(updates))
	}
	pu, ok := updates[0].(PlanUpdate)
	if !ok {
		t.Fatalf("expected PlanUpdate, got %T", updates[0])
	}
	if len(pu.Entries) != 2 {
		t.Errorf("entries: %d", len(pu.Entries))
	}
}

func TestProjectEvent_NilEvent(t *testing.T) {
	if updates := ProjectEvent(nil); updates != nil {
		t.Error("expected nil for nil event")
	}
}

func TestProjectEvent_CompactionReturnsNil(t *testing.T) {
	e := &session.Event{
		Kind:              session.EventKindCompaction,
		CompactionPayload: &session.CompactionPayload{Reason: "test"},
	}
	if updates := ProjectEvent(e); updates != nil {
		t.Error("expected nil for compaction")
	}
}

func TestProjectToNotification(t *testing.T) {
	u := ContentChunk{SessionUpdate: UpdateUserMessage, Content: TextContent{Type: "text", Text: "hi"}}
	n := ProjectToNotification("sess-1", u)
	if n.SessionID != "sess-1" {
		t.Errorf("session: %q", n.SessionID)
	}
	if n.Update.SessionUpdateType() != UpdateUserMessage {
		t.Errorf("update type: %q", n.Update.SessionUpdateType())
	}
}

func TestNormalizeToolStatus(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "pending"},
		{"pending", "pending"},
		{"running", "in_progress"},
		{"in_progress", "in_progress"},
		{"completed", "completed"},
		{"failed", "failed"},
		{"cancelled", "failed"},
		{"timed_out", "failed"},
	}
	for _, tt := range tests {
		if got := NormalizeToolStatus(tt.in); got != tt.want {
			t.Errorf("NormalizeToolStatus(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestToolKindForName(t *testing.T) {
	tests := []struct{ name, want string }{
		{"READ", "read"}, {"GLOB", "read"}, {"SEARCH", "read"},
		{"WRITE", "edit"}, {"PATCH", "edit"},
		{"LIST", "search"},
		{"RUN_COMMAND", "execute"},
		{"PLAN", "think"},
		{"UNKNOWN", "other"},
	}
	for _, tt := range tests {
		if got := ToolKindForName(tt.name); got != tt.want {
			t.Errorf("ToolKindForName(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// ─── Golden round-trip test ──────────────────────────────────────────

func TestProjectGoldenRoundTrip(t *testing.T) {
	// Simulate a full tool-call cycle and verify ACP projection.
	events := []session.Event{
		{
			Kind: session.EventKindUser, Visibility: session.VisibilityCanonical,
			UserPayload: &session.UserPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "read file"}},
			},
		},
		{
			Kind: session.EventKindToolCall, Visibility: session.VisibilityCanonical,
			ToolCallPayload: &session.ToolCallPayload{
				CallID: "tc-1", Name: "READ", Status: "pending",
				Args: map[string]any{"path": "/tmp/file.txt"},
			},
		},
		{
			Kind: session.EventKindToolResult, Visibility: session.VisibilityCanonical,
			ToolResultPayload: &session.ToolResultPayload{
				CallID: "tc-1", Name: "READ", Status: "completed",
				Content: []session.EventPart{{Kind: session.PartKindText, Text: "file contents"}},
			},
		},
		{
			Kind: session.EventKindAssistant, Visibility: session.VisibilityCanonical,
			AssistantPayload: &session.AssistantPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "Here is the file."}},
			},
		},
	}

	var allUpdates []Update
	for _, e := range events {
		allUpdates = append(allUpdates, ProjectEvent(&e)...)
	}

	// Expected: user_chunk, tool_call, tool_call_update, agent_chunk
	if len(allUpdates) != 4 {
		t.Fatalf("got %d updates, want 4", len(allUpdates))
	}

	kinds := make([]UpdateKind, len(allUpdates))
	for i, u := range allUpdates {
		kinds[i] = u.SessionUpdateType()
	}

	expected := []UpdateKind{UpdateUserMessage, UpdateToolCall, UpdateToolCallInfo, UpdateAgentMessage}
	for i, want := range expected {
		if kinds[i] != want {
			t.Errorf("update[%d]: got %q, want %q", i, kinds[i], want)
		}
	}

	// Verify tool_call has correct id.
	tc := allUpdates[1].(ToolCallUpdate)
	if tc.ToolCallID != "tc-1" {
		t.Errorf("tool_call id: %q", tc.ToolCallID)
	}

	// Verify tool_call_update has correct id and output.
	tu := allUpdates[2].(ToolCallUpdate)
	if tu.ToolCallID != "tc-1" {
		t.Errorf("tool_call_update id: %q", tu.ToolCallID)
	}
	if tu.RawOutput != "file contents" {
		t.Errorf("tool_call_update output: %v", tu.RawOutput)
	}
}
