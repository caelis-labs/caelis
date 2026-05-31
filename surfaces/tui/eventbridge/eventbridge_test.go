package eventbridge

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/core/session"
)

func TestKernelEventFromCoreProjectsToolContent(t *testing.T) {
	event := KernelEventFromCore(session.Event{
		SessionID: "s1",
		Type:      session.EventToolResult,
		Tool: &session.ToolEvent{
			ID:     "call-1",
			Name:   "RUN_COMMAND",
			Status: session.ToolCompleted,
			Content: []session.ToolContent{
				{Type: "text", Text: "command output"},
				{Type: "terminal", Text: "terminal output", TerminalID: "term-1"},
			},
		},
	})
	if event.ToolResult == nil {
		t.Fatal("ToolResult = nil, want projected tool result")
	}
	content := event.ToolResult.Content
	if len(content) != 2 {
		t.Fatalf("content len = %d, want 2", len(content))
	}
	if content[0].Type != "content" {
		t.Fatalf("text content type = %q, want content", content[0].Type)
	}
	if got := protocolText(content[0].Content); got != "command output" {
		t.Fatalf("text content = %q, want command output", got)
	}
	if content[1].Type != "terminal" || content[1].TerminalID != "term-1" {
		t.Fatalf("terminal content = %#v, want terminal term-1", content[1])
	}
	if got := protocolText(content[1].Content); got != "terminal output" {
		t.Fatalf("terminal content = %q, want terminal output", got)
	}
}

func protocolText(raw any) string {
	value, _ := raw.(map[string]any)
	text, _ := value["text"].(string)
	return text
}
