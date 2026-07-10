package task

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestCloneEntryNormalizesMutableFields(t *testing.T) {
	t.Parallel()

	entry := &Entry{
		TaskID: " task-1 ",
		Kind:   " command ",
		Session: session.SessionRef{
			AppName:   " app ",
			UserID:    " user ",
			SessionID: " sess-1 ",
		},
		Title: " ls -la ",
		State: " running ",
		Spec: map[string]any{
			"command": "ls -la",
			"options": map[string]any{"env": []any{"A=1"}},
		},
		Result: map[string]any{
			"exit_code": 0,
			"details":   map[string]any{"files": []any{"a"}},
		},
		Metadata: map[string]any{
			"tool_name": "RUN_COMMAND",
			"trace":     map[string]any{"ids": []any{"one"}},
		},
		Terminal: sandbox.TerminalRef{
			Backend:    " host ",
			SessionID:  " exec-1 ",
			TerminalID: " term-1 ",
		},
	}

	cloned := CloneEntry(entry)
	cloned.Spec["command"] = "pwd"
	cloned.Result["exit_code"] = 1
	cloned.Metadata["tool_name"] = "TASK"
	cloned.Spec["options"].(map[string]any)["env"].([]any)[0] = "A=2"
	cloned.Result["details"].(map[string]any)["files"].([]any)[0] = "b"
	cloned.Metadata["trace"].(map[string]any)["ids"].([]any)[0] = "two"

	if got := cloned.TaskID; got != "task-1" {
		t.Fatalf("TaskID = %q, want %q", got, "task-1")
	}
	if got := cloned.Kind; got != KindCommand {
		t.Fatalf("Kind = %q, want %q", got, KindCommand)
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
	if got := entry.Metadata["tool_name"]; got != "RUN_COMMAND" {
		t.Fatalf("original metadata mutated: %v", got)
	}
	if got := entry.Spec["options"].(map[string]any)["env"].([]any)[0]; got != "A=1" {
		t.Fatalf("original nested spec mutated: %v", got)
	}
	if got := entry.Result["details"].(map[string]any)["files"].([]any)[0]; got != "a" {
		t.Fatalf("original nested result mutated: %v", got)
	}
	if got := entry.Metadata["trace"].(map[string]any)["ids"].([]any)[0]; got != "one" {
		t.Fatalf("original nested metadata mutated: %v", got)
	}
}

func TestSanitizeResultForPersistence(t *testing.T) {
	t.Parallel()

	result := map[string]any{
		"stdout":         "out\n",
		"stderr":         "err\n",
		"output":         "output\n",
		"text":           "text\n",
		"latest_output":  "latest\n",
		"output_preview": "preview\n",
		"result":         "canonical\n",
		"final_message":  "final\n",
		"state":          "completed",
	}

	canonical := SanitizeResultForPersistence(result, ResultPersistenceCanonical)
	for _, key := range TransientResultKeys() {
		if value, ok := canonical[key]; ok {
			t.Fatalf("canonical result unexpectedly contains transient %q: %#v", key, value)
		}
	}
	if got, _ := canonical["result"].(string); got != "canonical\n" {
		t.Fatalf("canonical result[result] = %q, want canonical", got)
	}
	if got, _ := canonical["final_message"].(string); got != "final\n" {
		t.Fatalf("canonical result[final_message] = %q, want final", got)
	}

	deferred := SanitizeResultForPersistence(result, ResultPersistenceDeferred)
	for _, key := range append(TransientResultKeys(), "result", "final_message") {
		if value, ok := deferred[key]; ok {
			t.Fatalf("deferred result unexpectedly contains %q: %#v", key, value)
		}
	}
	if got, _ := deferred["state"].(string); got != "completed" {
		t.Fatalf("deferred result[state] = %q, want completed", got)
	}
	if _, ok := result["stdout"]; !ok {
		t.Fatal("SanitizeResultForPersistence mutated input")
	}
}

func TestNormalizeHandle(t *testing.T) {
	t.Parallel()

	if got := NormalizeHandle(" @Reviewer "); got != "reviewer" {
		t.Fatalf("NormalizeHandle() = %q, want reviewer", got)
	}
}
