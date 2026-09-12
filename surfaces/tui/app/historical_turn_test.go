package tuiapp

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
)

func TestHistoricalTurnBlocksKeepsNewestTwoTurnsFull(t *testing.T) {
	t.Parallel()

	first, second, third := newHistoricalMainTurn("turn-1", "hist-answer-one"), newHistoricalMainTurn("turn-2", "hist-answer-two"), newHistoricalMainTurn("turn-3", "hist-answer-three")
	blocks := []Block{
		NewUserNarrativeBlock("one"),
		first,
		NewUserNarrativeBlock("two"),
		second,
		NewUserNarrativeBlock("three"),
		third,
	}

	compact := historicalTurnBlocks(blocks)
	if !compact[first.BlockID()] {
		t.Fatalf("first turn %q not compact: %#v", first.BlockID(), compact)
	}
	if compact[second.BlockID()] || compact[third.BlockID()] {
		t.Fatalf("newest two turns compacted: %#v", compact)
	}
	if compact[blocks[0].BlockID()] {
		t.Fatal("user narrative listed as compact")
	}
}

func TestHistoricalTurnBlocksDoesNotCountSplitBlocksAsTurns(t *testing.T) {
	t.Parallel()

	firstA := newHistoricalMainTurn("turn-1", "hist-answer-split-a")
	firstB := newHistoricalMainTurn("turn-1", "hist-answer-split-b")
	second := newHistoricalMainTurn("turn-2", "hist-answer-two")
	third := newHistoricalMainTurn("turn-3", "hist-answer-three")
	blocks := []Block{
		NewUserNarrativeBlock("one"),
		firstA,
		firstB,
		NewUserNarrativeBlock("two"),
		second,
		NewUserNarrativeBlock("three"),
		third,
	}

	compact := historicalTurnBlocks(blocks)
	if !compact[firstA.BlockID()] || !compact[firstB.BlockID()] {
		t.Fatalf("same-TurnKey split blocks not compacted together: %#v", compact)
	}
	if compact[second.BlockID()] || compact[third.BlockID()] {
		t.Fatalf("newest two turns compacted: %#v", compact)
	}
}

func TestHistoricalTurnBlocksSplitsOnUserBarrierWithSameTurnKey(t *testing.T) {
	t.Parallel()

	first := newHistoricalMainTurn("turn-1", "hist-answer-before-steer")
	steered := newHistoricalMainTurn("turn-1", "hist-answer-after-steer")
	second := newHistoricalMainTurn("turn-2", "hist-answer-two")
	third := newHistoricalMainTurn("turn-3", "hist-answer-three")
	blocks := []Block{
		NewUserNarrativeBlock("one"),
		first,
		NewUserNarrativeBlock("steer the same protocol turn"),
		steered,
		NewUserNarrativeBlock("two"),
		second,
		NewUserNarrativeBlock("three"),
		third,
	}

	compact := historicalTurnBlocks(blocks)
	if !compact[first.BlockID()] || !compact[steered.BlockID()] {
		t.Fatalf("user barrier with same TurnKey did not create older compact turns: %#v", compact)
	}
	if compact[second.BlockID()] || compact[third.BlockID()] {
		t.Fatalf("newest two turns compacted: %#v", compact)
	}
}

func TestHistoricalTurnBlocksPromotesWhenNewTurnStarts(t *testing.T) {
	t.Parallel()

	first := newHistoricalMainTurn("turn-1", "hist-answer-one")
	second := newHistoricalMainTurn("turn-2", "hist-answer-two")
	blocks := []Block{
		NewUserNarrativeBlock("one"),
		first,
		NewUserNarrativeBlock("two"),
		second,
	}
	if compact := historicalTurnBlocks(blocks); len(compact) != 0 {
		t.Fatalf("two turns compacted = %#v, want none", compact)
	}

	blocks = append(blocks, NewUserNarrativeBlock("three"))
	compact := historicalTurnBlocks(blocks)
	if !compact[first.BlockID()] {
		t.Fatalf("new user turn did not promote first turn to compact: %#v", compact)
	}
	if compact[second.BlockID()] {
		t.Fatalf("ongoing second turn compacted after promotion: %#v", compact)
	}

	third := newHistoricalMainTurn("turn-3", "hist-answer-three")
	blocks = append(blocks, third)
	compact = historicalTurnBlocks(blocks)
	if !compact[first.BlockID()] || compact[second.BlockID()] || compact[third.BlockID()] {
		t.Fatalf("after third assistant arrived compact = %#v", compact)
	}
}

func TestRenderHistoricalTurnOmitsThoughtToolsPlansAndLifecycle(t *testing.T) {
	t.Parallel()

	block := newClutteredHistoricalTurn("turn-old", "hist-thought-secret", "hist-tool-secret.go", "hist-plan-secret", "hist-answer-visible")
	fullBlock := newClutteredHistoricalTurn("turn-old", "hist-thought-secret", "hist-tool-secret.go", "hist-plan-secret", "hist-answer-visible")
	ctx := historicalTestContext()
	snapshot := snapshotHistoricalTurn(block)

	full := joinRenderedPlain(fullBlock.Render(ctx))
	if !strings.Contains(full, "hist-thought-secret") || !strings.Contains(full, "hist-tool-secret.go") ||
		!strings.Contains(full, "hist-plan-secret") || !strings.Contains(full, "hist-answer-visible") {
		t.Fatalf("full render missing clutter or answer:\n%s", full)
	}

	compact := joinRenderedPlain(renderHistoricalTurn(block, ctx))
	if !strings.Contains(compact, "hist-answer-visible") {
		t.Fatalf("compact render missing assistant:\n%s", compact)
	}
	for _, secret := range []string{"hist-thought-secret", "hist-tool-secret.go", "hist-plan-secret", "Updated Plan", "✓ completed"} {
		if strings.Contains(compact, secret) {
			t.Fatalf("compact render leaked %q:\n%s", secret, compact)
		}
	}
	assertHistoricalTurnUnchanged(t, block, snapshot)
}

func TestHistoricalTurnBlocksKeepsOlderNonterminalTurnFull(t *testing.T) {
	t.Parallel()

	live := NewMainACPTurnBlock("turn-1")
	live.Events = []SubagentEvent{
		{Kind: SEReasoning, Text: "hist-thought-secret"},
		{Kind: SEToolCall, CallID: "run-1", Name: "RunCommand", Args: "hist-running-secret", Output: "started\n", Done: false, ToolKind: "execute"},
		{Kind: SEApproval, CallID: "run-1", ApprovalTool: "RunCommand", ApprovalCommand: "hist-running-secret", ApprovalStatus: "needs_user", ApprovalText: "confirm hist-approval-secret"},
		{Kind: SEAssistant, Text: "hist-answer-visible"},
	}
	live.Status = "waiting_approval"
	second := newHistoricalMainTurn("turn-2", "hist-answer-two")
	third := newHistoricalMainTurn("turn-3", "hist-answer-three")
	blocks := []Block{
		NewUserNarrativeBlock("one"),
		live,
		NewUserNarrativeBlock("two"),
		second,
		NewUserNarrativeBlock("three"),
		third,
	}
	if compact := historicalTurnBlocks(blocks); compact[live.BlockID()] {
		t.Fatalf("older waiting_approval turn compacted: %#v", compact)
	}
	plain := joinRenderedPlain(live.Render(historicalTestContext()))
	if !strings.Contains(plain, "hist-running-secret") || !strings.Contains(plain, "needs_user") {
		t.Fatalf("full render hid active tool or approval:\n%s", plain)
	}
}

func TestRenderHistoricalTurnOmitsUnterminatedToolsOnTerminalTurn(t *testing.T) {
	t.Parallel()

	block := newClutteredHistoricalTurn("turn-old", "hist-thought-secret", "hist-tool-secret.go", "hist-plan-secret", "hist-answer-visible")
	block.Events = append(block.Events, SubagentEvent{
		Kind: SEToolCall, CallID: "stale-1", Name: "Read", Args: "hist-stale-tool.go", Output: "still open", Done: false, ToolKind: "read",
	})
	ctx := historicalTestContext()
	snapshot := snapshotHistoricalTurn(block)
	plain := joinRenderedPlain(renderHistoricalTurn(block, ctx))
	if !strings.Contains(plain, "hist-answer-visible") {
		t.Fatalf("compact render missing assistant:\n%s", plain)
	}
	if strings.Contains(plain, "hist-stale-tool.go") || strings.Contains(plain, "hist-tool-secret.go") {
		t.Fatalf("terminal historical turn kept tools:\n%s", plain)
	}
	assertHistoricalTurnUnchanged(t, block, snapshot)
}

func TestRenderHistoricalTurnLateAnchoredAssistantUpdate(t *testing.T) {
	t.Parallel()

	first := newClutteredHistoricalTurn("turn-1", "hist-thought-secret", "hist-tool-secret.go", "hist-plan-secret", "hist-answer-before")
	second := newHistoricalMainTurn("turn-2", "hist-answer-two")
	third := newHistoricalMainTurn("turn-3", "hist-answer-three")
	blocks := []Block{
		NewUserNarrativeBlock("one"),
		first,
		NewUserNarrativeBlock("two"),
		second,
		NewUserNarrativeBlock("three"),
		third,
	}
	ctx := historicalTestContext()
	if !historicalTurnBlocks(blocks)[first.BlockID()] {
		t.Fatal("first turn should be compact before the late update")
	}

	before := joinRenderedPlain(renderDocumentHistorical(blocks, ctx))
	if !strings.Contains(before, "hist-answer-before") || strings.Contains(before, "hist-answer-late") {
		t.Fatalf("before late update:\n%s", before)
	}
	if strings.Contains(before, "hist-tool-secret.go") {
		t.Fatalf("compact first turn leaked finished tool before update:\n%s", before)
	}

	first.Events = append(first.Events, SubagentEvent{Kind: SEAssistant, Text: "hist-answer-late"})
	first.Events[1].Output = "late tool output"
	snapshot := snapshotHistoricalTurn(first)
	after := joinRenderedPlain(renderDocumentHistorical(blocks, ctx))
	if !strings.Contains(after, "hist-answer-late") {
		t.Fatalf("late anchored assistant missing:\n%s", after)
	}
	if strings.Contains(after, "late tool output") {
		t.Fatalf("late finished tool leaked into compact render:\n%s", after)
	}
	assertHistoricalTurnUnchanged(t, first, snapshot)
}

func TestRenderHistoricalTurnCompactsInlineParticipantAndLeavesChildPaneFull(t *testing.T) {
	t.Parallel()

	first := newHistoricalMainTurn("turn-1", "hist-answer-one")
	inline := NewParticipantTurnBlock("child-inline", "reviewer")
	inline.Events = []SubagentEvent{
		{Kind: SEReasoning, Text: "hist-child-thought"},
		{Kind: SEToolCall, CallID: "child-tool", Name: "Read", Args: "hist-child-tool.go", Output: "child out", Done: true, ToolKind: "read"},
		{Kind: SEAssistant, Text: "hist-child-answer"},
		{Kind: SEUserInput, Text: "hist-child-user"},
	}
	inline.Status = "completed"
	inline.StartedAt = time.Unix(1, 0)
	inline.EndedAt = time.Unix(8, 0)

	var childPaneBlocks []Block
	for i := 1; i <= 3; i++ {
		pane := NewParticipantTurnBlock("child-pane-"+strconv.Itoa(i), "reviewer")
		pane.FullAgentMessages = true
		pane.Events = append([]SubagentEvent(nil), inline.Events...)
		pane.Status = "completed"
		pane.StartedAt = inline.StartedAt
		pane.EndedAt = inline.EndedAt
		childPaneBlocks = append(childPaneBlocks, pane)
	}

	second := newHistoricalMainTurn("turn-2", "hist-answer-two")
	third := newHistoricalMainTurn("turn-3", "hist-answer-three")
	blocks := []Block{
		NewUserNarrativeBlock("one"),
		first,
		inline,
		NewUserNarrativeBlock("two"),
		second,
		NewUserNarrativeBlock("three"),
		third,
	}
	compact := historicalTurnBlocks(blocks)
	if !compact[first.BlockID()] || !compact[inline.BlockID()] {
		t.Fatalf("older main/participant not compacted: %#v", compact)
	}
	if childCompact := historicalTurnBlocks(childPaneBlocks); len(childCompact) != 0 {
		t.Fatalf("standalone child pane compacted: %#v", childCompact)
	}

	ctx := historicalTestContext()
	inlineSnapshot := snapshotParticipantTurn(inline)
	inlineRows := joinRenderedPlain(renderHistoricalTurn(inline, ctx))
	if !strings.Contains(inlineRows, "hist-child-answer") || !strings.Contains(inlineRows, "hist-child-user") {
		t.Fatalf("inline participant compact missing user/assistant:\n%s", inlineRows)
	}
	if strings.Contains(inlineRows, "hist-child-thought") || strings.Contains(inlineRows, "hist-child-tool.go") {
		t.Fatalf("inline participant compact leaked clutter:\n%s", inlineRows)
	}

	childPane := childPaneBlocks[0].(*ParticipantTurnBlock)
	paneRows := joinRenderedPlain(childPane.Render(ctx))
	if !strings.Contains(paneRows, "hist-child-thought") || !strings.Contains(paneRows, "hist-child-tool.go") ||
		!strings.Contains(paneRows, "hist-child-answer") {
		t.Fatalf("child pane Render lost full detail:\n%s", paneRows)
	}
	assertParticipantTurnUnchanged(t, inline, inlineSnapshot)
}

func TestHistoricalTurnRowHintCountsFilteredNarrative(t *testing.T) {
	t.Parallel()

	block := newClutteredHistoricalTurn("turn-hint", "hist-thought-secret", "hist-tool-secret.go", "hist-plan-secret", "hist-answer-visible")
	hint := historicalTurnRowHint(block, 80)
	if hint <= 0 {
		t.Fatalf("row hint = %d, want a positive compact estimate", hint)
	}
	rendered := len(renderHistoricalTurn(block, historicalTestContext()))
	if hint > rendered+2 || rendered > hint+8 {
		t.Fatalf("row hint = %d rendered = %d, want a cheap nearby estimate", hint, rendered)
	}
}

func TestRenderDocumentHistoricalMoreThanTwoTurns(t *testing.T) {
	t.Parallel()

	first := newClutteredHistoricalTurn("turn-1", "hist-thought-one", "hist-tool-one.go", "hist-plan-one", "hist-answer-one")
	second := newClutteredHistoricalTurn("turn-2", "hist-thought-two", "hist-tool-two.go", "hist-plan-two", "hist-answer-two")
	third := newClutteredHistoricalTurn("turn-3", "hist-thought-three", "hist-tool-three.go", "hist-plan-three", "hist-answer-three")
	blocks := []Block{
		NewWelcomeBlock("test"),
		NewUserNarrativeBlock("one"),
		first,
		NewUserNarrativeBlock("two"),
		second,
		NewUserNarrativeBlock("three"),
		third,
	}
	plain := joinRenderedPlain(renderDocumentHistorical(blocks, historicalTestContext()))
	if !strings.Contains(plain, "hist-answer-one") || !strings.Contains(plain, "hist-answer-two") || !strings.Contains(plain, "hist-answer-three") {
		t.Fatalf("document missing assistant answers:\n%s", plain)
	}
	if strings.Contains(plain, "hist-thought-one") || strings.Contains(plain, "hist-tool-one.go") || strings.Contains(plain, "hist-plan-one") {
		t.Fatalf("oldest turn not compacted:\n%s", plain)
	}
	if !strings.Contains(plain, "hist-thought-two") || !strings.Contains(plain, "hist-tool-two.go") || !strings.Contains(plain, "hist-plan-two") {
		t.Fatalf("second turn lost full detail:\n%s", plain)
	}
	if !strings.Contains(plain, "hist-thought-three") || !strings.Contains(plain, "hist-tool-three.go") || !strings.Contains(plain, "hist-plan-three") {
		t.Fatalf("newest turn lost full detail:\n%s", plain)
	}
}

func historicalTestContext() BlockRenderContext {
	return BlockRenderContext{
		Width:     80,
		TermWidth: 100,
		Theme:     tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY),
	}
}

func newHistoricalMainTurn(turnKey, answer string) *MainACPTurnBlock {
	block := NewMainACPTurnBlock(turnKey)
	block.Events = []SubagentEvent{{Kind: SEAssistant, Text: answer}}
	block.Status = "completed"
	return block
}

func newClutteredHistoricalTurn(turnKey, thought, tool, plan, answer string) *MainACPTurnBlock {
	block := NewMainACPTurnBlock(turnKey)
	block.Events = []SubagentEvent{
		{Kind: SEReasoning, Text: thought},
		{Kind: SEToolCall, CallID: "tool-" + turnKey, Name: "Read", Args: tool, Output: "output of " + tool, Done: true, ToolKind: "read"},
		{Kind: SEPlan, PlanEntries: []planEntryState{{Content: plan, Status: "completed"}}},
		{Kind: SENotice, Text: "Context compacted"},
		{Kind: SEAssistant, Text: answer},
	}
	block.Status = "completed"
	return block
}

func renderDocumentHistorical(blocks []Block, ctx BlockRenderContext) []RenderedRow {
	compact := historicalTurnBlocks(blocks)
	var rows []RenderedRow
	for _, block := range blocks {
		if compact[block.BlockID()] {
			rows = append(rows, renderHistoricalTurn(block, ctx)...)
			continue
		}
		rows = append(rows, block.Render(ctx)...)
	}
	return rows
}

type historicalTurnSnapshot struct {
	events      []SubagentEvent
	status      string
	budget      compactHeightBudgetState
	containers  int
	expandedLen int
}

func snapshotHistoricalTurn(block *MainACPTurnBlock) historicalTurnSnapshot {
	return historicalTurnSnapshot{
		events:      append([]SubagentEvent(nil), block.Events...),
		status:      block.Status,
		budget:      block.compactHeightBudget,
		containers:  len(block.explorationProjection.Containers),
		expandedLen: len(block.ExpandedTools) + len(block.ExpandedThought) + len(block.ExpandedExplore),
	}
}

func snapshotParticipantTurn(block *ParticipantTurnBlock) historicalTurnSnapshot {
	return historicalTurnSnapshot{
		events:      append([]SubagentEvent(nil), block.Events...),
		status:      block.Status,
		budget:      block.compactHeightBudget,
		containers:  len(block.explorationProjection.Containers),
		expandedLen: len(block.ExpandedTools) + len(block.ExpandedThought) + len(block.ExpandedExplore),
	}
}

func assertHistoricalTurnUnchanged(t *testing.T, block *MainACPTurnBlock, snapshot historicalTurnSnapshot) {
	t.Helper()
	assertHistoricalSnapshot(t, snapshotHistoricalTurn(block), snapshot)
}

func assertParticipantTurnUnchanged(t *testing.T, block *ParticipantTurnBlock, snapshot historicalTurnSnapshot) {
	t.Helper()
	assertHistoricalSnapshot(t, snapshotParticipantTurn(block), snapshot)
}

func assertHistoricalSnapshot(t *testing.T, got, want historicalTurnSnapshot) {
	t.Helper()
	if got.status != want.status {
		t.Fatalf("status mutated: got %q want %q", got.status, want.status)
	}
	if got.budget != want.budget {
		t.Fatalf("compactHeightBudget mutated: got %#v want %#v", got.budget, want.budget)
	}
	if got.containers != want.containers {
		t.Fatalf("exploration projection mutated: got %d want %d", got.containers, want.containers)
	}
	if got.expandedLen != want.expandedLen {
		t.Fatalf("expansion maps mutated: got %d want %d", got.expandedLen, want.expandedLen)
	}
	if len(got.events) != len(want.events) {
		t.Fatalf("Events length mutated: got %d want %d", len(got.events), len(want.events))
	}
	for i := range want.events {
		if got.events[i].Kind != want.events[i].Kind || got.events[i].Text != want.events[i].Text ||
			got.events[i].Args != want.events[i].Args || got.events[i].Output != want.events[i].Output ||
			got.events[i].Done != want.events[i].Done || got.events[i].ApprovalStatus != want.events[i].ApprovalStatus {
			t.Fatalf("Events[%d] mutated: got %#v want %#v", i, got.events[i], want.events[i])
		}
	}
}
