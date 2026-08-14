package eval

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/surfaces/tui/acpprojector"
)

func ptrStr(s string) *string { return &s }

func TestRegressionACPProjectorGoldenTerminalOutput(t *testing.T) {
	t.Parallel()

	p := projector.EventProjector{}
	updates, err := p.ProjectEvent(&session.Event{
		SessionID: "sess-1",
		Type:      session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: projector.UpdateToolCallInfo,
				ToolCallID:    "call-ls",
				Kind:          projector.ToolKindExecute,
				Status:        "completed",
				Content: []session.ProtocolToolCallContent{{
					Type:       "terminal",
					TerminalID: "runtime-term-1",
					Content:    session.ProtocolTextContent("total 0\n"),
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}

	update, ok := updates[0].(projector.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want ToolCallUpdate", updates[0])
	}
	if update.ToolCallID != "call-ls" {
		t.Fatalf("tool_call_id = %q, want call-ls", update.ToolCallID)
	}
	if update.Status == nil || *update.Status != projector.ToolStatusCompleted {
		statusStr := "<nil>"
		if update.Status != nil {
			statusStr = *update.Status
		}
		t.Fatalf("status = %q, want %q", statusStr, projector.ToolStatusCompleted)
	}
	if len(update.Content) != 1 || update.Content[0].Type != "terminal" || update.Content[0].TerminalID != "call-ls" {
		t.Fatalf("content = %#v, want one terminal anchor", update.Content)
	}
	if update.Content[0].Content != nil {
		t.Fatalf("terminal anchor content = %#v, want empty", update.Content[0].Content)
	}
	info, ok := metautil.TerminalInfo(update.Meta)
	if !ok || info.TerminalID != "call-ls" {
		t.Fatalf("terminal_info = %#v, want call-ls", update.Meta)
	}
	output, ok := metautil.TerminalOutput(update.Meta)
	if !ok || output.TerminalID != "call-ls" || output.Data != "total 0\n" {
		t.Fatalf("terminal_output = %#v, want total output for call-ls", update.Meta)
	}
}

func TestRegressionACPProjectorGoldenDiffTitle(t *testing.T) {
	t.Parallel()

	p := projector.EventProjector{}
	oldText := "line 1\nline 2\nline 3\n"
	updates, err := p.ProjectEvent(&session.Event{
		SessionID: "sess-1",
		Type:      session.EventTypeToolCall,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: projector.UpdateToolCallInfo,
				ToolCallID:    "call-write",
				Kind:          projector.ToolKindEdit,
				Status:        "completed",
				RawInput: map[string]any{
					"path": "/workspace/main.go",
				},
				Content: []session.ProtocolToolCallContent{{
					Type:    "diff",
					Path:    "/workspace/main.go",
					OldText: &oldText,
					NewText: "line 1\nnew line 2\nline 3\n",
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}

	update, ok := updates[0].(projector.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want ToolCallUpdate", updates[0])
	}
	if update.Kind == nil || *update.Kind != projector.ToolKindEdit {
		t.Fatalf("kind = %v, want %q", update.Kind, projector.ToolKindEdit)
	}
}

func TestRegressionACPProjectorGoldenApprovalRequest(t *testing.T) {
	t.Parallel()

	p := projector.EventProjector{}
	updates, err := p.ProjectEvent(&session.Event{
		SessionID: "sess-1",
		Type:      session.EventTypeToolCall,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: projector.UpdateToolCallInfo,
				ToolCallID:    "call-rm",
				Kind:          projector.ToolKindExecute,
				Status:        "waiting_approval",
				RawInput: map[string]any{
					"command": "rm -rf /tmp/demo",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}

	update, ok := updates[0].(projector.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want ToolCallUpdate", updates[0])
	}
	if update.Status == nil || *update.Status != projector.ToolStatusInProgress {
		statusStr := "<nil>"
		if update.Status != nil {
			statusStr = *update.Status
		}
		t.Fatalf("status = %q, want %q (waiting_approval maps to in_progress in ACP)", statusStr, projector.ToolStatusInProgress)
	}
}

func TestRegressionACPProjectorGoldenLifecycleStatusMapping(t *testing.T) {
	t.Parallel()

	p := projector.EventProjector{}

	statusTests := []struct {
		name       string
		status     string
		wantStatus string
	}{
		{"running", "running", projector.ToolStatusInProgress},
		{"completed", "completed", projector.ToolStatusCompleted},
		{"failed", "failed", projector.ToolStatusFailed},
		{"pending", "pending", projector.ToolStatusPending},
		{"waiting_approval", "waiting_approval", projector.ToolStatusInProgress},
		{"interrupted", "interrupted", projector.ToolStatusFailed},
		{"terminated", "terminated", projector.ToolStatusFailed},
		{"timed_out", "timed_out", projector.ToolStatusFailed},
	}

	for _, tt := range statusTests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			updates, err := p.ProjectEvent(&session.Event{
				SessionID: "sess-1",
				Type:      session.EventTypeToolResult,
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: projector.UpdateToolCallInfo,
						ToolCallID:    "call-1",
						Kind:          projector.ToolKindExecute,
						Status:        tt.status,
					},
				},
			})
			if err != nil {
				t.Fatalf("ProjectEvent() error = %v", err)
			}
			if len(updates) == 0 {
				t.Fatal("ProjectEvent() produced 0 updates")
			}
			update, ok := updates[0].(projector.ToolCallUpdate)
			if !ok {
				t.Fatalf("update = %T, want ToolCallUpdate", updates[0])
			}
			if update.Status == nil {
				t.Fatal("update.Status = nil, want non-nil")
			}
			if *update.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", *update.Status, tt.wantStatus)
			}
		})
	}
}

func TestRegressionACPProjectorGoldenAssistantMessage(t *testing.T) {
	t.Parallel()

	p := projector.EventProjector{}
	updates, err := p.ProjectEvent(&session.Event{
		SessionID: "sess-1",
		Type:      session.EventTypeAssistant,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: projector.UpdateAgentMessage,
				Content:       session.ProtocolTextContent("The answer is 42."),
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) == 0 {
		t.Fatal("ProjectEvent() produced 0 updates for assistant message")
	}

	chunk, ok := updates[0].(projector.ContentChunk)
	if !ok {
		t.Fatalf("update = %T, want ContentChunk", updates[0])
	}
	if chunk.SessionUpdate != projector.UpdateAgentMessage {
		t.Fatalf("session_update = %q, want %q", chunk.SessionUpdate, projector.UpdateAgentMessage)
	}
}

func TestRegressionACPProjectorTUIFormatGolden(t *testing.T) {
	t.Parallel()

	got := acpprojector.FormatToolContent([]acpprojector.ToolContent{{
		Type:    "content",
		Content: session.ProtocolTextContent("some output text"),
	}})
	if !strings.Contains(got, "some output text") {
		t.Fatalf("FormatToolContent(content) = %q, want contains 'some output text'", got)
	}

	oldText := "old line\n"
	got = acpprojector.FormatToolContent([]acpprojector.ToolContent{{
		Type:    "diff",
		Path:    "/workspace/file.go",
		OldText: &oldText,
		NewText: "new line\n",
	}})
	if !strings.Contains(got, "file.go") {
		t.Fatalf("FormatToolContent(diff) = %q, want contains 'file.go'", got)
	}
	if !strings.Contains(got, "+new line") {
		t.Fatalf("FormatToolContent(diff) = %q, want contains '+new line'", got)
	}
	if !strings.Contains(got, "-old line") {
		t.Fatalf("FormatToolContent(diff) = %q, want contains '-old line'", got)
	}

	got = acpprojector.FormatToolStart("RUN_COMMAND", map[string]any{"command": "go test ./..."})
	if !strings.Contains(got, "go test ./...") {
		t.Fatalf("FormatToolStart() = %q, want contains 'go test ./...'", got)
	}
}

func TestRegressionACPProjectorPermissionRequest(t *testing.T) {
	t.Parallel()

	p := projector.EventProjector{}
	req, ok, err := p.ProjectPermissionRequest(&session.Event{
		SessionID: "sess-1",
		Type:      session.EventTypeToolCall,
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodRequestPermission,
			Permission: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{
					ID:   "call-rm",
					Name: "RUN_COMMAND",
				},
				Options: []session.ProtocolApprovalOption{
					{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
					{ID: "deny", Name: "Deny", Kind: "deny"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectPermissionRequest() error = %v", err)
	}
	if !ok {
		t.Fatal("ProjectPermissionRequest() ok = false, want true for approval event")
	}
	if req.SessionID != "sess-1" {
		t.Fatalf("session_id = %q, want sess-1", req.SessionID)
	}
	if req.ToolCall.ToolCallID != "call-rm" {
		t.Fatalf("tool_call_id = %q, want call-rm", req.ToolCall.ToolCallID)
	}
	if len(req.Options) != 2 {
		t.Fatalf("options = %d, want 2", len(req.Options))
	}
	if req.Options[0].OptionID != "allow_once" {
		t.Fatalf("options[0].option_id = %q, want allow_once", req.Options[0].OptionID)
	}
}

func TestRegressionACPProjectorNotifications(t *testing.T) {
	t.Parallel()

	p := projector.EventProjector{}
	notifs, err := p.ProjectNotifications(&session.Event{
		SessionID: "sess-notif",
		Type:      session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: projector.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Kind:          projector.ToolKindExecute,
				Status:        "completed",
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectNotifications() error = %v", err)
	}
	if len(notifs) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifs))
	}
	if notifs[0].SessionID != "sess-notif" {
		t.Fatalf("notification session_id = %q, want sess-notif", notifs[0].SessionID)
	}
}
