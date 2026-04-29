package task

import (
	"testing"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestCloneEntryNormalizesMutableFields(t *testing.T) {
	t.Parallel()

	entry := &Entry{
		TaskID: " task-1 ",
		Kind:   " bash ",
		Session: sdksession.SessionRef{
			AppName:   " app ",
			UserID:    " user ",
			SessionID: " sess-1 ",
		},
		Title: " ls -la ",
		State: " running ",
		Spec: map[string]any{
			"command": "ls -la",
		},
		Result: map[string]any{
			"exit_code": 0,
		},
		Metadata: map[string]any{
			"tool_name": "BASH",
		},
		Terminal: sdksandbox.TerminalRef{
			Backend:    " host ",
			SessionID:  " exec-1 ",
			TerminalID: " term-1 ",
		},
	}

	cloned := CloneEntry(entry)
	cloned.Spec["command"] = "pwd"
	cloned.Result["exit_code"] = 1
	cloned.Metadata["tool_name"] = "TASK"

	if got := cloned.TaskID; got != "task-1" {
		t.Fatalf("TaskID = %q, want %q", got, "task-1")
	}
	if got := cloned.Kind; got != KindBash {
		t.Fatalf("Kind = %q, want %q", got, KindBash)
	}
	if got := cloned.Session.SessionID; got != "sess-1" {
		t.Fatalf("SessionID = %q, want %q", got, "sess-1")
	}
	if got := cloned.Terminal.TerminalID; got != "term-1" {
		t.Fatalf("TerminalID = %q, want %q", got, "term-1")
	}
	if got := entry.Spec["command"]; got != "ls -la" {
		t.Fatalf("original spec mutated: %v", got)
	}
	if got := entry.Result["exit_code"]; got != 0 {
		t.Fatalf("original result mutated: %v", got)
	}
	if got := entry.Metadata["tool_name"]; got != "BASH" {
		t.Fatalf("original metadata mutated: %v", got)
	}
}
