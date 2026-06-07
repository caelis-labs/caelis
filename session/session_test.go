package session

import (
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/model"
)

func TestRefValidate(t *testing.T) {
	tests := []struct {
		name    string
		ref     Ref
		wantErr bool
	}{
		{"valid", Ref{"app", "user", "ws", "sess"}, false},
		{"missing AppName", Ref{"", "user", "ws", "sess"}, true},
		{"missing UserID", Ref{"app", "", "ws", "sess"}, true},
		{"missing WorkspaceKey", Ref{"app", "user", "", "sess"}, true},
		{"missing SessionID", Ref{"app", "user", "ws", ""}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.ref.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRefEqual(t *testing.T) {
	a := Ref{"app", "user", "ws", "sess"}
	b := Ref{"app", "user", "ws", "sess"}
	c := Ref{"app", "user", "ws", "other"}
	if !a.Equal(b) {
		t.Error("expected equal refs to be equal")
	}
	if a.Equal(c) {
		t.Error("expected different refs to not be equal")
	}
}

func TestRefString(t *testing.T) {
	r := Ref{"app", "user", "ws", "sess"}
	s := r.String()
	if s != "app/user/ws/sess" {
		t.Errorf("got %q, want %q", s, "app/user/ws/sess")
	}
}

func TestStateClone(t *testing.T) {
	s := State{"a": "1", "b": "2"}
	cp := s.Clone()
	cp["a"] = "modified"
	if s["a"] == "modified" {
		t.Error("clone should not affect original")
	}
}

func TestStateCloneNil(t *testing.T) {
	var s State
	cp := s.Clone()
	if cp != nil {
		t.Error("clone of nil state should be nil")
	}
}

func TestVisibilityRules(t *testing.T) {
	tests := []struct {
		v         Visibility
		persisted bool
		model     bool
		history   bool
		transient bool
	}{
		{VisibilityCanonical, true, true, true, false},
		{VisibilityMirror, true, false, true, false},
		{VisibilityOverlay, false, true, false, true},
		{VisibilityUIOnly, false, false, false, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.v), func(t *testing.T) {
			if got := tt.v.IsPersisted(); got != tt.persisted {
				t.Errorf("IsPersisted() = %v, want %v", got, tt.persisted)
			}
			if got := tt.v.IsModelVisible(); got != tt.model {
				t.Errorf("IsModelVisible() = %v, want %v", got, tt.model)
			}
			if got := tt.v.IsHistoryVisible(); got != tt.history {
				t.Errorf("IsHistoryVisible() = %v, want %v", got, tt.history)
			}
			if got := tt.v.IsTransient(); got != tt.transient {
				t.Errorf("IsTransient() = %v, want %v", got, tt.transient)
			}
		})
	}
}

func TestEventClone(t *testing.T) {
	e := Event{
		ID:         "evt-1",
		Kind:       EventKindUser,
		Visibility: VisibilityCanonical,
		UserPayload: &UserPayload{
			Parts: []EventPart{
				{Kind: PartKindText, Text: "hello"},
			},
		},
	}
	cp := e.Clone()
	cp.UserPayload.Parts[0].Text = "modified"
	if e.UserPayload.Parts[0].Text == "modified" {
		t.Error("clone should not affect original")
	}
}

func TestEventCloneToolCall(t *testing.T) {
	e := Event{
		Kind: EventKindToolCall,
		ToolCallPayload: &ToolCallPayload{
			CallID: "c1",
			Name:   "tool",
			Status: "pending",
			Args:   map[string]any{"k": "v"},
		},
	}
	cp := e.Clone()
	cp.ToolCallPayload.Args["k"] = "modified"
	if e.ToolCallPayload.Args["k"] == "modified" {
		t.Error("clone should not affect original args")
	}
}

func TestEventCloneToolResult(t *testing.T) {
	e := Event{
		Kind: EventKindToolResult,
		ToolResultPayload: &ToolResultPayload{
			CallID:  "c1",
			Name:    "tool",
			Content: []EventPart{{Kind: PartKindText, Text: "output"}},
		},
	}
	cp := e.Clone()
	cp.ToolResultPayload.Content[0].Text = "modified"
	if e.ToolResultPayload.Content[0].Text == "modified" {
		t.Error("clone should not affect original content")
	}
}

func TestEventCloneAssistant(t *testing.T) {
	e := Event{
		Kind: EventKindAssistant,
		AssistantPayload: &AssistantPayload{
			Parts: []EventPart{
				{Kind: PartKindText, Text: "reply"},
				{Kind: PartKindToolUse, ToolUse: &PartToolUse{
					CallID: "c1", Name: "TOOL", Args: map[string]any{"a": "b"},
				}},
			},
		},
	}
	cp := e.Clone()
	cp.AssistantPayload.Parts[1].ToolUse.Args["a"] = "modified"
	if e.AssistantPayload.Parts[1].ToolUse.Args["a"] == "modified" {
		t.Error("clone should not affect original tool use args")
	}
}

func TestEventTextContent(t *testing.T) {
	e := Event{
		Kind: EventKindAssistant,
		AssistantPayload: &AssistantPayload{
			Parts: []EventPart{
				{Kind: PartKindReasoning, Text: "thinking..."},
				{Kind: PartKindText, Text: "hello"},
			},
		},
	}
	if got := e.TextContent(); got != "thinking...hello" {
		t.Errorf("got %q, want %q", got, "thinking...hello")
	}
}

func TestEventToolCallIDs(t *testing.T) {
	e := Event{
		Kind:            EventKindToolCall,
		ToolCallPayload: &ToolCallPayload{CallID: "c1"},
	}
	ids := e.ToolCallIDs()
	if len(ids) != 1 || ids[0] != "c1" {
		t.Errorf("got %v, want [c1]", ids)
	}
}

func TestSessionClone(t *testing.T) {
	s := Session{
		Ref:   Ref{"app", "user", "ws", "sess"},
		Title: "test",
		State: State{"k": "v"},
		Participants: []ParticipantBinding{
			{ID: "p1", Metadata: map[string]string{"role": "owner"}},
		},
	}
	cp := s.Clone()
	cp.State["k"] = "modified"
	cp.Participants[0].Metadata["role"] = "modified"
	if s.State["k"] == "modified" {
		t.Error("clone should not affect original state")
	}
	if s.Participants[0].Metadata["role"] == "modified" {
		t.Error("clone should not affect original participants")
	}
}

func TestSessionValidate(t *testing.T) {
	valid := Session{
		Ref:       Ref{"app", "user", "ws", "sess"},
		CreatedAt: time.Now(),
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("expected valid session, got %v", err)
	}

	noRef := Session{CreatedAt: time.Now()}
	if err := noRef.Validate(); err == nil {
		t.Error("expected error for empty ref")
	}

	noTime := Session{Ref: Ref{"app", "user", "ws", "sess"}}
	if err := noTime.Validate(); err == nil {
		t.Error("expected error for zero CreatedAt")
	}
}

func TestSessionWithDefaults(t *testing.T) {
	s := Session{Ref: Ref{"app", "user", "ws", "sess"}}
	s = s.WithDefaults()
	if s.State == nil {
		t.Error("expected State to be initialized")
	}
}

// ─── Projection tests ────────────────────────────────────────────────

func TestModelContextFromEvents(t *testing.T) {
	events := []Event{
		{
			Kind:       EventKindUser,
			Visibility: VisibilityCanonical,
			UserPayload: &UserPayload{
				Parts: []EventPart{{Kind: PartKindText, Text: "hello"}},
			},
		},
		{
			Kind:       EventKindAssistant,
			Visibility: VisibilityCanonical,
			AssistantPayload: &AssistantPayload{
				Parts: []EventPart{{Kind: PartKindText, Text: "hi there"}},
			},
		},
		{
			Kind:       EventKindToolCall,
			Visibility: VisibilityCanonical,
			ToolCallPayload: &ToolCallPayload{
				CallID: "c1", Name: "READ", Status: "pending",
				Args: map[string]any{"path": "/tmp"},
			},
		},
		{
			Kind:       EventKindToolResult,
			Visibility: VisibilityCanonical,
			ToolResultPayload: &ToolResultPayload{
				CallID: "c1", Name: "READ", Status: "completed",
				Content: []EventPart{{Kind: PartKindText, Text: "file contents"}},
			},
		},
		{
			Kind:       EventKindUser,
			Visibility: VisibilityCanonical,
			UserPayload: &UserPayload{
				Parts: []EventPart{{Kind: PartKindText, Text: "thanks"}},
			},
		},
	}

	msgs := ModelContextFromEvents(events)
	if len(msgs) != 5 {
		t.Fatalf("got %d messages, want 5", len(msgs))
	}

	// user → assistant → assistant(tool_use) → tool → user
	if msgs[0].Role != model.RoleUser {
		t.Errorf("msg 0: got %q, want %q", msgs[0].Role, model.RoleUser)
	}
	if msgs[1].Role != model.RoleAssistant {
		t.Errorf("msg 1: got %q, want %q", msgs[1].Role, model.RoleAssistant)
	}
	if msgs[2].Role != model.RoleAssistant {
		t.Errorf("msg 2: got %q, want %q", msgs[2].Role, model.RoleAssistant)
	}
	if msgs[2].Content[0].ToolUse == nil {
		t.Error("msg 2: expected tool_use part")
	}
	if msgs[3].Role != model.RoleTool {
		t.Errorf("msg 3: got %q, want %q", msgs[3].Role, model.RoleTool)
	}
	if msgs[4].Role != model.RoleUser {
		t.Errorf("msg 4: got %q, want %q", msgs[4].Role, model.RoleUser)
	}
}

func TestModelContextSkipsNonVisible(t *testing.T) {
	events := []Event{
		{
			Kind:       EventKindUser,
			Visibility: VisibilityCanonical,
			UserPayload: &UserPayload{
				Parts: []EventPart{{Kind: PartKindText, Text: "visible"}},
			},
		},
		{
			Kind:       EventKindAssistant,
			Visibility: VisibilityUIOnly, // should be skipped
			AssistantPayload: &AssistantPayload{
				Parts: []EventPart{{Kind: PartKindText, Text: "hidden"}},
			},
		},
		{
			Kind:          EventKindNotice,
			Visibility:    VisibilityCanonical,
			NoticePayload: &NoticePayload{Text: "notice"},
		},
	}
	msgs := ModelContextFromEvents(events)
	if len(msgs) != 1 {
		t.Fatalf("got %d, want 1 (notice doesn't produce model message)", len(msgs))
	}
}

func TestModelContextWithReasoning(t *testing.T) {
	events := []Event{
		{
			Kind:       EventKindAssistant,
			Visibility: VisibilityCanonical,
			AssistantPayload: &AssistantPayload{
				Parts: []EventPart{
					{Kind: PartKindReasoning, Text: "thinking..."},
					{Kind: PartKindText, Text: "answer"},
				},
			},
		},
	}
	msgs := ModelContextFromEvents(events)
	if len(msgs) != 1 {
		t.Fatalf("got %d, want 1", len(msgs))
	}
	if len(msgs[0].Content) != 2 {
		t.Fatalf("got %d parts, want 2", len(msgs[0].Content))
	}
}

func TestModelContextPreservesProviderReplayMetadata(t *testing.T) {
	events := []Event{
		{
			Kind:       EventKindAssistant,
			Visibility: VisibilityCanonical,
			AssistantPayload: &AssistantPayload{
				Parts: []EventPart{
					{
						Kind: PartKindReasoning,
						Text: "prior reasoning",
						ProviderMeta: map[string]any{
							"replay": map[string]any{
								"provider": "anthropic",
								"kind":     "thinking_signature",
								"token":    "sig-prev",
							},
						},
					},
					{
						Kind: PartKindToolUse,
						ToolUse: &PartToolUse{
							CallID: "call-1",
							Name:   "lookup",
						},
						ProviderMeta: map[string]any{
							"gemini_thought_signature": "b64:sig-call",
						},
					},
				},
			},
		},
	}

	msgs := ModelContextFromEvents(events)
	if len(msgs) != 1 || len(msgs[0].Content) != 2 {
		t.Fatalf("messages = %#v, want one assistant message with two parts", msgs)
	}
	reasoning := msgs[0].Content[0].Reasoning
	if reasoning == nil {
		t.Fatalf("reasoning part = nil, want preserved reasoning")
	}
	if reasoning.Text != "prior reasoning" {
		t.Fatalf("reasoning text = %q, want prior reasoning", reasoning.Text)
	}
	if reasoning.Replay == nil || reasoning.Replay.Provider != "anthropic" || reasoning.Replay.Token != "sig-prev" {
		t.Fatalf("reasoning replay = %#v, want anthropic sig-prev", reasoning.Replay)
	}
	if got := msgs[0].Content[1].ToolUse.ProviderMeta["gemini_thought_signature"]; got != "b64:sig-call" {
		t.Fatalf("tool provider meta signature = %#v, want b64:sig-call", got)
	}
}

func TestRoundTripModelContext(t *testing.T) {
	// Build events, project to model context, verify structure.
	events := []Event{
		{
			Kind:       EventKindUser,
			Visibility: VisibilityCanonical,
			UserPayload: &UserPayload{
				Parts: []EventPart{{Kind: PartKindText, Text: "what files?"}},
			},
		},
		{
			Kind:       EventKindAssistant,
			Visibility: VisibilityCanonical,
			AssistantPayload: &AssistantPayload{
				Parts: []EventPart{
					{Kind: PartKindText, Text: "let me check"},
				},
			},
		},
		{
			Kind:       EventKindToolCall,
			Visibility: VisibilityCanonical,
			ToolCallPayload: &ToolCallPayload{
				CallID: "tc-1", Name: "LIST", Status: "pending",
				Args: map[string]any{"path": "."},
			},
		},
		{
			Kind:       EventKindToolResult,
			Visibility: VisibilityCanonical,
			ToolResultPayload: &ToolResultPayload{
				CallID: "tc-1", Name: "LIST", Status: "completed",
				Content: []EventPart{{Kind: PartKindText, Text: "file1.go\nfile2.go"}},
			},
		},
		{
			Kind:       EventKindAssistant,
			Visibility: VisibilityCanonical,
			AssistantPayload: &AssistantPayload{
				Parts: []EventPart{
					{Kind: PartKindText, Text: "found 2 files"},
				},
			},
		},
	}

	msgs := ModelContextFromEvents(events)
	// user, assistant(text), assistant(tool_use), tool, assistant(text)
	if len(msgs) != 5 {
		t.Fatalf("got %d messages, want 5", len(msgs))
	}

	// Verify the model context matches what the LLM would see.
	if msgs[0].Role != model.RoleUser {
		t.Error("first message should be user")
	}
	if msgs[2].Content[0].ToolUse.CallID != "tc-1" {
		t.Error("third message should have tool_use with call_id tc-1")
	}
	if msgs[3].Role != model.RoleTool {
		t.Error("fourth message should be tool result")
	}
	if msgs[3].Content[0].ToolResult.CallID != "tc-1" {
		t.Error("tool result should reference tc-1")
	}

}
