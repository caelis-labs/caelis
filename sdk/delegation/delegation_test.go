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
	continuation := CloneContinueRequest(ContinueRequest{
		Agent:       "  codex  ",
		Prompt:      "  continue  ",
		YieldTimeMS: 250,
	})
	if got := continuation.Agent; got != "codex" {
		t.Fatalf("continuation.Agent = %q, want codex", got)
	}
	if got := continuation.Prompt; got != "continue" {
		t.Fatalf("continuation.Prompt = %q, want continue", got)
	}
	if got := continuation.YieldTimeMS; got != 250 {
		t.Fatalf("continuation.YieldTimeMS = %v, want %v", got, 250)
	}

	result := CloneResult(Result{
		TaskID:        "  task-1  ",
		State:         StateRunning,
		Running:       true,
		Yielded:       true,
		OutputPreview: "  search repo/**/*.go  ",
	})
	if got := result.TaskID; got != "task-1" {
		t.Fatalf("result.TaskID = %q, want %q", got, "task-1")
	}
	if got := result.OutputPreview; got != "search repo/**/*.go" {
		t.Fatalf("result.OutputPreview = %q, want %q", got, "search repo/**/*.go")
	}

	completed := CloneResult(Result{
		TaskID: " task-2 ",
		State:  StateCompleted,
		Result: " final answer ",
	})
	if got := completed.Result; got != "final answer" {
		t.Fatalf("completed.Result = %q, want %q", got, "final answer")
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
