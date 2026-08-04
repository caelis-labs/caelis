package subagent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/protocol/acp/client"
)

func TestRunnerActionSummaryKeepsFinalizingIntentAcrossSparseToolUpdate(t *testing.T) {
	t.Parallel()

	anchor := delegation.Anchor{TaskID: "task-summary", SessionID: "child-1", Agent: "self", AgentID: "self-1"}
	run := &childRun{
		anchor: anchor, taskID: anchor.TaskID, state: delegation.StateRunning,
		running: true, done: make(chan struct{}),
	}
	runner := &Runner{clock: time.Now, runs: map[string]*childRun{anchor.TaskID: run}}

	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: anchor.SessionID,
		Update: client.ToolCall{
			SessionUpdate: client.UpdateToolCall,
			ToolCallID:    "inspect-1",
			Title:         "Inspect runner.go",
			Kind:          "read",
			Status:        "in_progress",
		},
	})
	runner.handleUpdate(run, contentUpdateWithMessageID(t, client.UpdateAgentThought, "thought-1", "I have enough evidence. "))
	runner.handleUpdate(run, contentUpdateWithMessageID(t, client.UpdateAgentThought, "thought-1", "Preparing the final review summary now."))
	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: anchor.SessionID,
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "sparse-1",
		},
	})

	read, err := runner.Wait(context.Background(), anchor, 0)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	want := "I have enough evidence. Preparing the final review summary now. · inspect runner.go"
	if read.OutputPreview != want {
		t.Fatalf("read OutputPreview = %q, want %q", read.OutputPreview, want)
	}
	if strings.EqualFold(read.OutputPreview, "working") {
		t.Fatalf("read OutputPreview regressed to generic activity: %q", read.OutputPreview)
	}

	waitCtx, cancel := context.WithCancel(context.Background())
	cancel()
	waited, err := runner.Wait(waitCtx, anchor, 60_000)
	if err != nil {
		t.Fatalf("wait snapshot: %v", err)
	}
	if waited != read {
		t.Fatalf("wait snapshot = %#v, want same action summary as read %#v", waited, read)
	}
}

func TestSubagentActionSummaryExpiresIntentWhenRealWorkResumes(t *testing.T) {
	t.Parallel()

	var summary subagentActionSummary
	summary.observeAction("inspect runner.go")
	summary.observeThought("thought-1", "Preparing the final review summary")
	if got, want := summary.previewOrEmpty(), "Preparing the final review summary · inspect runner.go"; got != want {
		t.Fatalf("finalizing preview = %q, want %q", got, want)
	}

	summary.observeAction("inspect follow-up evidence")
	if got, want := summary.previewOrEmpty(), "inspect follow-up evidence"; got != want {
		t.Fatalf("resumed-work preview = %q, want %q", got, want)
	}
}

func TestSubagentActionSummaryAssistantReplacesPriorTool(t *testing.T) {
	t.Parallel()

	var summary subagentActionSummary
	summary.observeAction("inspect runner.go")
	summary.observeAssistant("Here are the final findings")
	if got, want := summary.previewOrEmpty(), "Here are the final findings"; got != want {
		t.Fatalf("assistant preview = %q, want %q", got, want)
	}
}

func TestRunnerActionSummaryConcurrentUpdateAndWait(t *testing.T) {
	t.Parallel()

	anchor := delegation.Anchor{TaskID: "task-summary-race", SessionID: "child-1", Agent: "self", AgentID: "self-1"}
	run := &childRun{
		anchor: anchor, taskID: anchor.TaskID, state: delegation.StateRunning,
		running: true, done: make(chan struct{}),
	}
	runner := &Runner{clock: time.Now, runs: map[string]*childRun{anchor.TaskID: run}}
	thought := contentUpdateWithMessageID(t, client.UpdateAgentThought, "thought-race", "Checking the latest evidence. ")
	toolUpdate := client.UpdateEnvelope{
		SessionID: anchor.SessionID,
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "read-race",
			Title:         stringPtr("Inspect follow-up evidence"),
			Status:        stringPtr("in_progress"),
		},
	}

	const iterations = 100
	start := make(chan struct{})
	invalid := make(chan delegation.Result, iterations)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for range iterations {
			runner.handleUpdate(run, thought)
			runner.handleUpdate(run, toolUpdate)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for range iterations {
			got, err := runner.Wait(context.Background(), anchor, 0)
			if err != nil || got.State != delegation.StateRunning || !got.Running || !utf8.ValidString(got.OutputPreview) {
				invalid <- got
			}
		}
	}()
	close(start)
	wg.Wait()
	close(invalid)
	if got, ok := <-invalid; ok {
		t.Fatalf("concurrent snapshot = %#v, want valid running summary", got)
	}
	final, err := runner.Wait(context.Background(), anchor, 0)
	if err != nil || final.OutputPreview != "inspect follow-up evidence" {
		t.Fatalf("final snapshot = %#v, %v; want latest real action", final, err)
	}
}

func TestRunnerCompletedResultKeepsActionSummary(t *testing.T) {
	t.Parallel()

	run := &childRun{
		taskID: "task-completed-summary", state: delegation.StateRunning,
		running: true, done: make(chan struct{}),
	}
	runner := &Runner{clock: time.Now}
	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: "child-1",
		Update: client.ToolCall{
			SessionUpdate: client.UpdateToolCall,
			ToolCallID:    "test-1",
			Title:         "Run focused tests",
			Status:        "completed",
		},
	})
	runner.finishDrive(context.Background(), run, "end_turn", nil)

	got := runner.waitRun(context.Background(), run, 0)
	if got.State != delegation.StateCompleted || got.Running || got.OutputPreview != "run focused tests" {
		t.Fatalf("completed result = %#v, want terminal action summary", got)
	}
}

func TestSubagentActionSummaryIsBoundedWhitespaceCompactedAndUTF8Safe(t *testing.T) {
	t.Parallel()

	var summary subagentActionSummary
	full := "  " + strings.Repeat("阶段  证据\t", 60) + "准备最终总结  "
	summary.observeThought("thought-1", full)
	preview := summary.previewOrEmpty()

	if !utf8.ValidString(preview) {
		t.Fatalf("preview is not valid UTF-8: %q", preview)
	}
	if got := utf8.RuneCountInString(preview); got > maxSubagentPreviewRunes {
		t.Fatalf("preview rune count = %d, want <= %d: %q", got, maxSubagentPreviewRunes, preview)
	}
	if strings.ContainsAny(preview, "\n\r\t") || strings.Contains(preview, "  ") {
		t.Fatalf("preview whitespace was not compacted: %q", preview)
	}
	if !strings.Contains(preview, "...[truncated]...") {
		t.Fatalf("preview = %q, want explicit bounded truncation", preview)
	}
	if preview == strings.TrimSpace(full) {
		t.Fatal("preview copied the full child trace")
	}
}

func TestCompactPreviewIgnoresTrailingBlankLines(t *testing.T) {
	t.Parallel()

	if got, want := compactPreview("earlier\nlatest status\n  \r\n"), "latest status"; got != want {
		t.Fatalf("compactPreview() = %q, want %q", got, want)
	}
}

func TestSubagentActionSummaryUsesCurrentPlanEntry(t *testing.T) {
	t.Parallel()

	entries := []client.PlanEntry{
		{Content: "Inspect runtime", Status: "completed"},
		{Content: "Summarize findings", Status: "in_progress"},
		{Content: "Run broad checks", Status: "pending"},
	}
	if got, want := planActivity(entries), "plan: Summarize findings"; got != want {
		t.Fatalf("planActivity() = %q, want %q", got, want)
	}
}

func TestRunnerTerminalDiagnosticOverridesActionSummary(t *testing.T) {
	t.Parallel()

	run := &childRun{
		taskID: "task-terminal-summary", state: delegation.StateRunning,
		running: true, done: make(chan struct{}),
	}
	runner := &Runner{clock: time.Now}
	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentThought, "Preparing a detailed final summary"))
	runner.finishDrive(context.Background(), run, "", errors.New("provider secret detail"))

	got := runner.waitRun(context.Background(), run, 0)
	if got.State != delegation.StateFailed || got.OutputPreview != "subagent prompt failed" || got.Error != "subagent prompt failed" {
		t.Fatalf("terminal result = %#v, want stable failure diagnostic", got)
	}

	// A late transport update must not replace an already committed terminal
	// diagnostic with transient activity.
	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentThought, "working again"))
	late := runner.waitRun(context.Background(), run, 0)
	if late.OutputPreview != got.OutputPreview || late.Error != got.Error || late.State != got.State {
		t.Fatalf("late update changed terminal result from %#v to %#v", got, late)
	}
}
