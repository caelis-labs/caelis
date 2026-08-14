package tuiapp

import (
	"strings"
	"testing"
)

func TestReasoningShouldFoldKeepsActiveTailOpen(t *testing.T) {
	t.Parallel()

	text := liveExplorationReasoningBody()
	events := []SubagentEvent{{Kind: SEReasoning, Text: text}}
	if reasoningShouldFold(events, 0, "running") {
		t.Fatal("streaming tail folded before any later work")
	}

	events = append(events, SubagentEvent{Kind: SEToolCall, CallID: "read-1", Name: "Read", Args: "a.go", ToolKind: "read"})
	if reasoningShouldFold(events, 0, "running") {
		t.Fatal("active tail folded after the first exploration tool")
	}

	events = append(events, SubagentEvent{Kind: SEToolCall, CallID: "read-2", Name: "Read", Args: "b.go", ToolKind: "read"})
	if reasoningShouldFold(events, 0, "running") {
		t.Fatal("active tail folded after the second exploration tool")
	}

	events[0].narrativeFinal = true
	if !reasoningShouldFold(events, 0, "running") {
		t.Fatal("settled reasoning with later tools did not fold")
	}
}

func TestLiveExplorationReasoningStaysFiveLineUntilSettledThenExplored(t *testing.T) {
	t.Parallel()

	source := newNarrativeSourceIdentity("reason-live", "event-live", "projection-live")
	text := liveExplorationReasoningBody()
	ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(100)

	block := NewMainACPTurnBlock("turn-live-reasoning")
	block.AppendStreamEvent(SEReasoning, text, source)
	assertLiveReasoningPresentation(t, "streaming", block.Render(ctx), reasoningPresentationLiveWindow)

	block.UpdateToolWithMeta("read-1", "Read", "a.go", "", false, false, ToolUpdateMeta{ToolKind: "read"})
	assertLiveReasoningPresentation(t, "first tool", block.Render(ctx), reasoningPresentationLiveWindow)

	block.UpdateToolWithMeta("read-2", "Read", "b.go", "", false, false, ToolUpdateMeta{ToolKind: "read"})
	assertLiveReasoningPresentation(t, "second tool", block.Render(ctx), reasoningPresentationLiveWindow)

	block.ReplaceFinalStreamEvent(SEReasoning, text, source)
	block.UpdateToolWithMeta("read-1", "Read", "a.go", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	block.UpdateToolWithMeta("read-2", "Read", "b.go", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	assertLiveReasoningPresentation(t, "finalized with tools", block.Render(ctx), reasoningPresentationFoldedSummary)

	block.AppendStreamEvent(SEReasoning, "continue after the batch", newNarrativeSourceIdentity("reason-next", "event-next", "projection-next"))
	plain := joinRenderedPlain(block.Render(ctx))
	if got := countExactTrimmedLine(plain, "• Explored"); got != 1 {
		t.Fatalf("after next step, explored groups = %d, want 1:\n%s", got, plain)
	}
	if classifyLiveReasoningPresentation(plain) == reasoningPresentationLiveWindow {
		t.Fatalf("previous live window survived Explored merge:\n%s", plain)
	}
	if !strings.Contains(plain, "continue after the batch") {
		t.Fatalf("next step reasoning disappeared:\n%s", plain)
	}
}

func TestFinalizedReasoningDoesNotReopenFiveLineWindowOnSecondTool(t *testing.T) {
	t.Parallel()

	source := newNarrativeSourceIdentity("reason-final", "event-final", "projection-final")
	text := liveExplorationReasoningBody()
	ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(100)

	block := NewMainACPTurnBlock("turn-final-reasoning")
	block.ReplaceFinalStreamEvent(SEReasoning, text, source)
	assertLiveReasoningPresentation(t, "final without tools", block.Render(ctx), reasoningPresentationLiveWindow)

	block.UpdateToolWithMeta("read-1", "Read", "a.go", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	assertLiveReasoningPresentation(t, "first tool after final", block.Render(ctx), reasoningPresentationFoldedSummary)

	block.UpdateToolWithMeta("read-2", "Read", "b.go", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	assertLiveReasoningPresentation(t, "second tool after final", block.Render(ctx), reasoningPresentationFoldedSummary)
}

func liveExplorationReasoningBody() string {
	return strings.Join([]string{
		"Now I understand the problem clearly:",
		"The fold policy mixed two owners.",
		"The first tool collapsed the live window.",
		"The second tool reopened the five-line preview.",
		"That bounce is what this test forbids.",
		"Keep the tail on a fixed five-line budget.",
		"Fold only after the stream is settled.",
		"Then let the next step enter Explored.",
	}, "\n")
}

type liveReasoningPresentation int

const (
	reasoningPresentationNone liveReasoningPresentation = iota
	reasoningPresentationLiveWindow
	reasoningPresentationFoldedSummary
	reasoningPresentationFull
)

func (p liveReasoningPresentation) String() string {
	switch p {
	case reasoningPresentationLiveWindow:
		return "5-line live window"
	case reasoningPresentationFoldedSummary:
		return "1-line folded summary"
	case reasoningPresentationFull:
		return "full unwrapped reasoning"
	default:
		return "no reasoning"
	}
}

func assertLiveReasoningPresentation(t *testing.T, phase string, rows []RenderedRow, want liveReasoningPresentation) {
	t.Helper()
	plain := joinRenderedPlain(rows)
	if got := classifyLiveReasoningPresentation(plain); got != want {
		t.Fatalf("%s presentation = %s, want %s:\n%s", phase, got, want, plain)
	}
}

func classifyLiveReasoningPresentation(plain string) liveReasoningPresentation {
	if liveReasoningOmittedLine(plain) {
		return reasoningPresentationLiveWindow
	}
	if liveReasoningFoldedSummaryLine(plain) {
		return reasoningPresentationFoldedSummary
	}
	if strings.Contains(plain, "Now I understand the problem clearly:") {
		return reasoningPresentationFull
	}
	return reasoningPresentationNone
}

func liveReasoningOmittedLine(plain string) bool {
	for _, line := range strings.Split(plain, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "... +") && strings.HasSuffix(line, "lines") {
			return true
		}
	}
	return false
}

func liveReasoningFoldedSummaryLine(plain string) bool {
	for _, line := range strings.Split(plain, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "› ") && strings.Contains(line, " · ") {
			return true
		}
	}
	return false
}
