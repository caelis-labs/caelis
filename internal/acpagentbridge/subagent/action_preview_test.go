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
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
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
	want := "Inspect runner.go\nReasoning: I have enough evidence. Preparing the final review summary now."
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

func TestSubagentActionSummaryRetainsBoundedRecentPhases(t *testing.T) {
	t.Parallel()

	var summary subagentActionSummary
	summary.observeAction("inspect runner.go")
	summary.observeThought("thought-1", "Preparing the final review summary")
	if got, want := summary.previewOrEmpty(), "inspect runner.go\nReasoning: Preparing the final review summary"; got != want {
		t.Fatalf("finalizing preview = %q, want %q", got, want)
	}

	summary.observeAction("inspect follow-up evidence")
	if got, want := summary.previewOrEmpty(), "inspect runner.go\nReasoning: Preparing the final review summary\ninspect follow-up evidence"; got != want {
		t.Fatalf("resumed-work preview = %q, want %q", got, want)
	}
}

func TestSubagentActionSummaryAssistantKeepsRecentToolContext(t *testing.T) {
	t.Parallel()

	var summary subagentActionSummary
	summary.observeAction("inspect runner.go")
	summary.observeAssistant("assistant-1", "Here are the final findings")
	if got, want := summary.previewOrEmpty(), "inspect runner.go\nAssistant: Here are the final findings"; got != want {
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
	if err != nil || !strings.Contains(final.OutputPreview, "Reasoning: Checking the latest evidence.") ||
		!strings.HasSuffix(final.OutputPreview, "Inspect follow-up evidence") || len([]byte(final.OutputPreview)) > maxSubagentPreviewBytes {
		t.Fatalf("final snapshot = %#v, %v; want bounded reasoning and latest real action", final, err)
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
	if got.State != delegation.StateCompleted || got.Running || got.OutputPreview != "Run focused tests" {
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
	if got := len([]byte(preview)); got > maxSubagentPreviewBytes {
		t.Fatalf("preview byte count = %d, want <= %d: %q", got, maxSubagentPreviewBytes, preview)
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

func TestCompactActivityTextKeepsHeadAndTailLines(t *testing.T) {
	t.Parallel()

	text := "first evidence\n" + strings.Repeat("middle evidence ", 40) + "\nlatest status\n  \r\n"
	got := compactActivityText(text, 96)
	if !strings.HasPrefix(got, "first evidence") || !strings.HasSuffix(got, "latest status") || !strings.Contains(got, subagentPreviewTruncationMarker) {
		t.Fatalf("compactActivityText() = %q, want preserved head and tail", got)
	}
}

func TestSubagentActionSummaryUsesCurrentPlanEntry(t *testing.T) {
	t.Parallel()

	entries := []client.PlanEntry{
		{Content: "Inspect runtime", Status: "completed"},
		{Content: "Summarize findings", Status: "in_progress"},
		{Content: "Run broad checks", Status: "pending"},
	}
	if got, want := planActivity(entries), "Plan: Summarize findings"; got != want {
		t.Fatalf("planActivity() = %q, want %q", got, want)
	}
}

func TestSubagentActionSummaryIncludesCanonicalToolOutput(t *testing.T) {
	t.Parallel()

	var summary subagentActionSummary
	summary.observeTool("command-1", "Ran go test ./agent-sdk/runtime", "=== RUN TestTask\n--- PASS: TestTask\nPASS\nok package")
	got := summary.previewOrEmpty()
	for _, want := range []string{"Ran go test ./agent-sdk/runtime", "=== RUN TestTask", "... +2 lines", "ok package"} {
		if !strings.Contains(got, want) {
			t.Fatalf("preview = %q, want %q", got, want)
		}
	}
	summary.observeTool("command-2", "Ran verbose command", strings.Repeat("large output ", 10_000))
	if got := summary.previewOrEmpty(); len([]byte(got)) > maxSubagentPreviewBytes {
		t.Fatalf("large tool preview bytes = %d, want <= %d", len([]byte(got)), maxSubagentPreviewBytes)
	}
	if got := len([]byte(summary.blocks[len(summary.blocks)-1].detail)); got > maxSubagentActivityBlockBytes {
		t.Fatalf("stored tool detail bytes = %d, want <= %d", got, maxSubagentActivityBlockBytes)
	}
}

func TestRunnerActionSummaryUsesCanonicalToolContentNotRawOutput(t *testing.T) {
	t.Parallel()

	anchor := delegation.Anchor{TaskID: "task-tool-output", SessionID: "child-1", Agent: "self", AgentID: "self-1"}
	run := &childRun{
		anchor: anchor, taskID: anchor.TaskID, state: delegation.StateRunning,
		running: true, done: make(chan struct{}),
	}
	runner := &Runner{clock: time.Now, runs: map[string]*childRun{anchor.TaskID: run}}
	runner.handleUpdate(run, client.UpdateEnvelope{
		SessionID: anchor.SessionID,
		Update: client.ToolCall{
			SessionUpdate: client.UpdateToolCall,
			ToolCallID:    "command-1",
			Title:         "Ran go test ./agent-sdk/runtime",
			RawOutput:     map[string]any{"token": "raw-secret"},
			Content: []client.ToolCallContent{{
				Type:    "content",
				Content: client.TextContent{Type: "text", Text: "PASS\nok package"},
			}},
		},
	})

	got, err := runner.Wait(context.Background(), anchor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.OutputPreview, "Ran go test ./agent-sdk/runtime") || !strings.Contains(got.OutputPreview, "PASS") {
		t.Fatalf("OutputPreview = %q, want tool action and canonical content", got.OutputPreview)
	}
	if strings.Contains(got.OutputPreview, "raw-secret") {
		t.Fatalf("OutputPreview exposed RawOutput: %q", got.OutputPreview)
	}
}

func TestSubagentActionSummaryKeepsOnlyNewestActivityBlocks(t *testing.T) {
	t.Parallel()

	var summary subagentActionSummary
	for _, action := range []string{"one", "two", "three", "four", "five"} {
		summary.observeAction(action)
	}
	got := summary.previewOrEmpty()
	if strings.Contains(got, "one") || got != "two\nthree\nfour\nfive" {
		t.Fatalf("preview = %q, want newest four activity blocks", got)
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
