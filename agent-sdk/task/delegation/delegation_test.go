package delegation

import "testing"

func TestCloneRequestAndResultPreserveMinimalLLMSurface(t *testing.T) {
	req := CloneRequest(Request{
		Agent:  "  codex  ",
		Prompt: "  inspect repo  ",
	})
	if got := req.Agent; got != "codex" {
		t.Fatalf("req.Agent = %q, want %q", got, "codex")
	}
	if got := req.Prompt; got != "inspect repo" {
		t.Fatalf("req.Prompt = %q, want %q", got, "inspect repo")
	}
	result := CloneResult(Result{
		TaskID:        "  task-1  ",
		State:         StateRunning,
		Running:       true,
		Yielded:       true,
		OutputPreview: "  search repo/**/*.go  ",
		Error:         " must not leak while running ",
	})
	if got := result.TaskID; got != "task-1" {
		t.Fatalf("result.TaskID = %q, want %q", got, "task-1")
	}
	if got := result.OutputPreview; got != "search repo/**/*.go" {
		t.Fatalf("result.OutputPreview = %q, want %q", got, "search repo/**/*.go")
	}
	if result.Error != "" {
		t.Fatalf("running result.Error = %q, want empty", result.Error)
	}
	completed := CloneResult(Result{
		TaskID: " task-2 ",
		State:  StateCompleted,
		Error:  " stale failure ",
		Result: " final answer ",
	})
	if got := completed.Result; got != "final answer" {
		t.Fatalf("completed.Result = %q, want %q", got, "final answer")
	}
	if completed.Error != "" {
		t.Fatalf("completed.Error = %q, want empty", completed.Error)
	}
	failed := CloneResult(Result{
		TaskID: " task-3 ",
		State:  StateFailed,
		Error:  "  child prompt failed  ",
	})
	if got := failed.Error; got != "child prompt failed" {
		t.Fatalf("failed.Error = %q, want %q", got, "child prompt failed")
	}
}

func TestCloneAnchorKeepsSystemIdentitySeparate(t *testing.T) {
	anchor := CloneAnchor(Anchor{
		TaskID:    "  task-1  ",
		SessionID: "  child-1  ",
		Agent:     "  codex  ",
		AgentID:   "  bob  ",
	})
	if got := anchor.TaskID; got != "task-1" {
		t.Fatalf("anchor.TaskID = %q, want %q", got, "task-1")
	}
	if got := anchor.SessionID; got != "child-1" {
		t.Fatalf("anchor.SessionID = %q, want %q", got, "child-1")
	}
	if got := anchor.Agent; got != "codex" {
		t.Fatalf("anchor.Agent = %q, want %q", got, "codex")
	}
	if got := anchor.AgentID; got != "bob" {
		t.Fatalf("anchor.AgentID = %q, want %q", got, "bob")
	}
}
