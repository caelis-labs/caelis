package tuiapp

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestGatewayCompletedExplorationToolDefaultsCollapsedWithoutHeaderClick(t *testing.T) {
	model := newGatewayEventTestModel()

	updated, _ := model.Update(gatewayEventMsg(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "READ",
				RawInput: map[string]any{"path": "internal/kernel/types.go"},
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
			},
		}}))

	model = updated.(*Model)
	updated, _ = model.Update(gatewayEventMsg(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:    "call-1",
				ToolName:  "READ",
				RawInput:  map[string]any{"path": "internal/kernel/types.go"},
				RawOutput: map[string]any{"text": "package core\n\ntype Event struct{}"},
				Content:   testToolContent("types.go"),
				Status:    kernel.ToolStatusCompleted,
				Scope:     kernel.EventScopeMain,
			},
		}}))

	model = updated.(*Model)

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	if block.toolPanelExpanded("call-1") {
		t.Fatalf("READ tool panel should default collapsed after completion; expanded map = %#v", block.ExpandedTools)
	}
	rows := block.Render(BlockRenderContext{Width: 100, TermWidth: 100, Theme: model.theme})
	if !rowsContainClickToken(rows, acpToolPanelClickToken("call-1")) {
		t.Fatalf("collapsed READ tool panel should expose an expand click token: %#v", renderedPlainRows(rows))
	}
	if !model.tryToggleACPToolPanelToken(block.BlockID(), "acp_tool_panel:call-1") {
		t.Fatal("collapsed READ tool panel should expand from a header click")
	}
	if !block.toolPanelExpanded("call-1") {
		t.Fatalf("READ tool panel should expand after click; expanded map = %#v", block.ExpandedTools)
	}
	if !model.tryToggleACPToolPanelToken(block.BlockID(), "acp_tool_panel:call-1") {
		t.Fatal("expanded READ tool panel should collapse on a second click")
	}
	if block.toolPanelExpanded("call-1") {
		t.Fatalf("READ tool panel should collapse after second click; expanded map = %#v", block.ExpandedTools)
	}
}

func TestGatewayCompletedExplorationToolsRenderAsCompactSummary(t *testing.T) {
	model := newGatewayEventTestModel()
	sendTool := func(id string, name string, args string, output string) {
		rawInput := map[string]any{"path": args}
		switch strings.ToUpper(name) {
		case "RG", "SEARCH", "FIND":
			rawInput = map[string]any{"query": args}
		case "LIST":
			rawInput = map[string]any{"path": args}
		case "PATCH", "WRITE":
			rawInput = map[string]any{"path": args}
		}
		updated, _ := model.Update(gatewayEventMsg(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindToolCall,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				ToolCall: &kernel.ToolCallPayload{
					CallID:   id,
					ToolName: name,
					RawInput: rawInput,
					Status:   kernel.ToolStatusRunning,
					Scope:    kernel.EventScopeMain,
				},
			}}))

		model = updated.(*Model)
		updated, _ = model.Update(gatewayEventMsg(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindToolResult,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				ToolResult: &kernel.ToolResultPayload{
					CallID:    id,
					ToolName:  name,
					RawInput:  rawInput,
					RawOutput: map[string]any{"text": output},
					Content:   testToolContent(toolResultLabel(name, rawInput)),
					Status:    kernel.ToolStatusCompleted,
					Scope:     kernel.EventScopeMain,
				},
			}}))

		model = updated.(*Model)
	}
	sendReasoning := func(text string) {
		updated, _ := model.Update(gatewayEventMsg(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative: &kernel.NarrativePayload{
					Role:          kernel.NarrativeRoleAssistant,
					ReasoningText: text,
					Final:         true,
					Scope:         kernel.EventScopeMain,
				},
			}}))

		model = updated.(*Model)
	}
	sendReasoning("Now let me explore more to understand the project structure - specifically:\n1. The service layer for config.\n2. The rbac remote client.")
	sendTool("read-1", "READ", "internal/kernel/types.go", "type Event struct{}")
	sendReasoning("Let me search the event kind references next.")
	sendTool("rg-1", "RG", "EventKind", "42 matches")
	sendTool("list-1", "LIST", "surfaces/tui/app", "transcript_event.go")
	sendReasoning("Let me patch the hook implementation next.")
	sendTool("patch-1", "PATCH", "hooks.go", "patched hooks.go")

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme})
	var plain []string
	for _, row := range rows {
		plain = append(plain, row.Plain)
	}
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, "• Explored") ||
		!strings.Contains(joined, "  └ Read types.go") ||
		!strings.Contains(joined, `    Search "EventKind"`) ||
		!strings.Contains(joined, "    List app") {
		t.Fatalf("rendered rows = %q, want compact exploration summary", joined)
	}
	if strings.Contains(joined, "type Event struct{}") || strings.Contains(joined, "42 matches") {
		t.Fatalf("rendered rows = %q, want exploration details hidden while collapsed", joined)
	}
	if strings.Contains(joined, "Now let me explore more") || strings.Contains(joined, "Let me search the event kind references next") {
		t.Fatalf("rendered rows = %q, want exploration reasoning hidden while collapsed", joined)
	}
	exploreTailIdx := indexOfRowContaining(plain, "List app")
	patchReasonIdx := indexOfRowContaining(plain, "› Let me patch the hook implementation next.")
	patchIdx := indexOfRowContaining(plain, "• Patched hooks.go")
	if exploreTailIdx < 0 || patchReasonIdx < 0 || patchIdx < 0 {
		t.Fatalf("rendered rows = %#v, want exploration summary, patch reasoning, and patch tool", plain)
	}
	if !hasBlankRowBetween(plain, exploreTailIdx, patchReasonIdx) {
		t.Fatalf("rendered rows = %#v, want blank after exploration stage", plain)
	}
	if hasBlankRowBetween(plain, patchReasonIdx, patchIdx) {
		t.Fatalf("rendered rows = %#v, want patch reasoning attached to patch tool", plain)
	}

	key := "read-1,rg-1,list-1"
	if !model.tryToggleACPToolPanelToken(block.BlockID(), "acp_exploration_stage:"+key) {
		t.Fatal("expected exploration summary click token to expand grouped stage")
	}
	rows = block.Render(BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme})
	plain = plain[:0]
	for _, row := range rows {
		plain = append(plain, row.Plain)
	}
	joined = strings.Join(plain, "\n")
	if !strings.Contains(joined, "  └ Now let me explore more to understand the project structure - specifically:") ||
		!strings.Contains(joined, "    1. The service layer for config.") ||
		!strings.Contains(joined, "    2. The rbac remote client.") ||
		!strings.Contains(joined, "    Let me search the event kind references next.") ||
		!strings.Contains(joined, "    Read types.go") ||
		!strings.Contains(joined, `Search "EventKind"`) ||
		!strings.Contains(joined, "List app") {
		t.Fatalf("expanded rows = %q, want ordered exploration stage", joined)
	}
	if strings.Contains(joined, "type Event struct{}") || strings.Contains(joined, "42 matches") {
		t.Fatalf("expanded rows = %q, should show compact calls rather than raw outputs", joined)
	}
	if !model.tryToggleACPToolPanelToken(block.BlockID(), "acp_exploration_stage:"+key) {
		t.Fatal("expected expanded exploration stage click token to collapse")
	}
	rows = block.Render(BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme})
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if strings.Contains(joined, "Now let me explore more") || !strings.Contains(joined, "Explored") {
		t.Fatalf("exploration stage should collapse after second click, got %q", joined)
	}
}

func TestGatewayCompletedExplorationSummaryCompactsAbsoluteWorkspacePaths(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	model := newGatewayEventTestModel()
	sendTool := func(id string, name string, rawInput map[string]any) {
		updated, _ := model.Update(gatewayEventMsg(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindToolCall,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				ToolCall: &kernel.ToolCallPayload{
					CallID:   id,
					ToolName: name,
					RawInput: rawInput,
					Status:   kernel.ToolStatusRunning,
					Scope:    kernel.EventScopeMain,
				},
			}}))

		model = updated.(*Model)
		updated, _ = model.Update(gatewayEventMsg(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindToolResult,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				ToolResult: &kernel.ToolResultPayload{
					CallID:   id,
					ToolName: name,
					RawInput: rawInput,
					Content:  testToolContent(rawInput["path"].(string)),
					Status:   kernel.ToolStatusCompleted,
					Scope:    kernel.EventScopeMain,
				},
			}}))

		model = updated.(*Model)
	}

	readPath := filepath.Join(root, "internal", "handler", "oss_bucket.go")
	listPath := filepath.Join(root, "internal")
	sendTool("read-abs", "READ", map[string]any{"path": readPath})
	sendTool("list-abs", "LIST", map[string]any{"path": listPath})

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	block.SetStatus("completed", "", "", time.Now())
	joined := strings.Join(renderedPlainRows(block.Render(BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme})), "\n")
	if !strings.Contains(joined, "• Explored") ||
		!strings.Contains(joined, "Read oss_bucket.go") ||
		!strings.Contains(joined, "List internal") {
		t.Fatalf("rendered rows = %q, want compact relative exploration paths", joined)
	}
	if strings.Contains(joined, root) {
		t.Fatalf("rendered rows = %q, should not contain absolute workspace root %q", joined, root)
	}
}

func TestGatewayCompletedExplorationSummaryUsesReadRangesAndBasenames(t *testing.T) {
	model := newGatewayEventTestModel()
	path := filepath.Join("internal", "task", "cluster", "sync_cluster.go")
	sendRead := func(id string, offset int, start int, end int) {
		rawInput := map[string]any{"path": path, "offset": offset, "limit": 50}
		updated, _ := model.Update(gatewayEventMsg(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindToolCall,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				ToolCall: &kernel.ToolCallPayload{
					CallID:   id,
					ToolName: "READ",
					RawInput: rawInput,
					Status:   kernel.ToolStatusRunning,
					Scope:    kernel.EventScopeMain,
				},
			}}))

		model = updated.(*Model)
		updated, _ = model.Update(gatewayEventMsg(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindToolResult,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				ToolResult: &kernel.ToolResultPayload{
					CallID:   id,
					ToolName: "READ",
					RawInput: rawInput,
					RawOutput: map[string]any{
						"path":       path,
						"start_line": start,
						"end_line":   end,
					},
					Status: kernel.ToolStatusCompleted,
					Scope:  kernel.EventScopeMain,
				},
			}}))

		model = updated.(*Model)
	}

	sendRead("read-1", 0, 1, 50)
	sendRead("read-2", 50, 51, 100)

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	block.SetStatus("completed", "", "", time.Now())
	joined := strings.Join(renderedPlainRows(block.Render(BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme})), "\n")
	if !strings.Contains(joined, "Explored") ||
		!strings.Contains(joined, "Read sync_cluster.go 1~50, sync_cluster.go 51~100") {
		t.Fatalf("rendered rows = %q, want compact read ranges with basenames", joined)
	}
	if strings.Contains(joined, filepath.Join("internal", "task", "cluster")) || strings.Contains(joined, `internal\task\cluster`) {
		t.Fatalf("rendered rows = %q, should not contain workspace-relative path prefixes", joined)
	}
}

func TestGatewaySingleExplorationStepSettlesOnNextAssistantNarrative(t *testing.T) {
	model := newGatewayEventTestModel()
	sendReasoning := func(text string) {
		updated, _ := model.Update(gatewayEventMsg(kernel.EventEnvelope{Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:          kernel.NarrativeRoleAssistant,
				ReasoningText: text,
				Final:         true,
				Scope:         kernel.EventScopeMain,
			},
		}}))

		model = updated.(*Model)
	}
	sendRead := func(id string, path string) {
		rawInput := map[string]any{"path": path}
		updated, _ := model.Update(gatewayEventMsg(kernel.EventEnvelope{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   id,
				ToolName: "READ",
				RawInput: rawInput,
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
			},
		}}))

		model = updated.(*Model)
		updated, _ = model.Update(gatewayEventMsg(kernel.EventEnvelope{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:    id,
				ToolName:  "READ",
				RawInput:  rawInput,
				RawOutput: map[string]any{"text": "config contents"},
				Content:   testToolContent("config.go"),
				Status:    kernel.ToolStatusCompleted,
				Scope:     kernel.EventScopeMain,
			},
		}}))

		model = updated.(*Model)
	}

	sendReasoning("I need to inspect the config before changing behavior.\nThis should remain readable while the READ result lands.")
	sendRead("read-live", "config.go")
	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	ctx := BlockRenderContext{Width: 96, Height: 20, TermWidth: 96, Theme: model.theme}
	liveRows := block.Render(ctx)
	liveJoined := strings.Join(renderedPlainRows(liveRows), "\n")
	if !strings.Contains(liveJoined, "I need to inspect the config before changing behavior.") {
		t.Fatalf("live rows = %q, want current reasoning to remain visible", liveJoined)
	}
	if strings.Contains(liveJoined, "• Explored") {
		t.Fatalf("live rows = %q, should not compact current exploration step before next assistant output", liveJoined)
	}
	if strings.Contains(liveJoined, "config contents") || strings.Contains(liveJoined, "╭") {
		t.Fatalf("live rows = %q, current exploration tool should not expose raw output panel", liveJoined)
	}

	sendReasoning("Now I can patch the rendering behavior.")
	settledRows := block.Render(ctx)
	settledJoined := strings.Join(renderedPlainRows(settledRows), "\n")
	if !strings.Contains(settledJoined, "• Explored") ||
		!strings.Contains(settledJoined, "  └ Read config.go") ||
		!strings.Contains(settledJoined, "Now I can patch the rendering behavior.") {
		t.Fatalf("settled rows = %q, want previous exploration compacted before new reasoning", settledJoined)
	}
	if strings.Contains(settledJoined, "· I need to inspect the config before changing behavior.") {
		t.Fatalf("settled rows = %q, previous reasoning should be hidden in collapsed Explored", settledJoined)
	}
	maxBudget := maxInt(1, ctx.Height/2)
	if trailing := countTrailingBlankRows(renderedPlainRows(settledRows)); trailing > maxBudget {
		t.Fatalf("settled trailing budget rows = %d, want <= %d; live rows = %d settled rows = %d", trailing, maxBudget, len(liveRows), len(settledRows))
	}
}

func TestGatewayAssistantExplorationStepSettlesOnlyAtStepBoundary(t *testing.T) {
	model := newGatewayEventTestModel()
	block := NewMainACPTurnBlock("root-session")
	block.Events = append(block.Events,
		SubagentEvent{Kind: SEAssistant, Text: "I will inspect the build script next."},
		SubagentEvent{Kind: SEToolCall, CallID: "read-build", Name: "READ", Args: "scripts/build.sh", Output: "script contents", Done: true},
	)
	ctx := BlockRenderContext{Width: 96, Height: 20, TermWidth: 96, Theme: model.theme}
	live := strings.Join(renderedPlainRows(block.Render(ctx)), "\n")
	if strings.Contains(live, "• Explored") {
		t.Fatalf("live rows = %q, assistant exploration step should remain expanded until next step", live)
	}
	if !strings.Contains(live, "I will inspect the build script next.") {
		t.Fatalf("live rows = %q, want assistant text visible before step boundary", live)
	}

	block.Events = append(block.Events,
		SubagentEvent{Kind: SEAssistant, Text: "Now I can run the build."},
		SubagentEvent{Kind: SEToolCall, CallID: "command-build", Name: "RUN_COMMAND", Args: "bash scripts/build.sh debug ./bin/storage"},
	)
	settled := strings.Join(renderedPlainRows(block.Render(ctx)), "\n")
	if !strings.Contains(settled, "• Explored") || !strings.Contains(settled, "Read build.sh") {
		t.Fatalf("settled rows = %q, want completed exploration step folded after next step arrives", settled)
	}
	if strings.Contains(settled, "I will inspect the build script next.") {
		t.Fatalf("settled rows = %q, folded exploration step should hide its assistant text", settled)
	}
	if !strings.Contains(settled, "Now I can run the build.") || !strings.Contains(settled, "• Ran bash scripts/build.sh debug ./bin/storage") {
		t.Fatalf("settled rows = %q, next non-exploration step should stay visible", settled)
	}
}

func TestGatewayLongExplorationHandoffBudgetKeepsSummaryAndNewStreamVisible(t *testing.T) {
	model := newGatewayEventTestModel()
	block := NewMainACPTurnBlock("root-session")
	block.Events = append(block.Events,
		SubagentEvent{
			Kind: SEReasoning,
			Text: strings.Repeat("I need to inspect enough context before patching.\n", 30),
		},
		SubagentEvent{
			Kind:   SEToolCall,
			CallID: "read-long",
			Name:   "READ",
			Args:   "long_config.go",
			Output: "done",
			Done:   true,
		},
	)
	ctx := BlockRenderContext{Width: 96, Height: 12, TermWidth: 96, Theme: model.theme}
	live := strings.Join(renderedPlainRows(block.Render(ctx)), "\n")
	if strings.Contains(live, "• Explored") {
		t.Fatalf("live rows = %q, should not compact before next assistant output", live)
	}

	block.Events = append(block.Events, SubagentEvent{Kind: SEReasoning, Text: "Now continue with the patch."})
	settledPlain := renderedPlainRows(block.Render(ctx))
	maxBudget := maxInt(1, ctx.Height/2)
	trailing := countTrailingBlankRows(settledPlain)
	if trailing == 0 || trailing > maxBudget {
		t.Fatalf("settled trailing budget rows = %d, want 1..%d; rows = %#v", trailing, maxBudget, settledPlain)
	}
	tail := strings.Join(tailPlainRows(settledPlain, ctx.Height), "\n")
	if !strings.Contains(tail, "• Explored") || !strings.Contains(tail, "Now continue with the patch.") {
		t.Fatalf("visible tail = %q, want compact summary and new stream visible", tail)
	}
}

func TestGatewayLiveExplorationCompactsAtTurnCompletion(t *testing.T) {
	model := newGatewayEventTestModel()
	block := NewMainACPTurnBlock("root-session")
	block.Events = append(block.Events,
		SubagentEvent{Kind: SEReasoning, Text: "Inspect before finishing."},
		SubagentEvent{Kind: SEToolCall, CallID: "read-final", Name: "READ", Args: "final.go", Output: "done", Done: true},
	)
	ctx := BlockRenderContext{Width: 96, Height: 20, TermWidth: 96, Theme: model.theme}
	live := strings.Join(renderedPlainRows(block.Render(ctx)), "\n")
	if strings.Contains(live, "• Explored") {
		t.Fatalf("live rows = %q, should not compact before turn completion", live)
	}
	block.SetStatus("completed", "", "", time.Now())
	settled := strings.Join(renderedPlainRows(block.Render(ctx)), "\n")
	if !strings.Contains(settled, "• Explored") || !strings.Contains(settled, "Read final.go") {
		t.Fatalf("completed rows = %q, want final exploration compaction", settled)
	}
	if strings.Contains(settled, "Inspect before finishing.") {
		t.Fatalf("completed rows = %q, collapsed completed rows should hide exploration reasoning", settled)
	}
}

func TestGatewaySettledExplorationStepsStayInSingleExploredGroup(t *testing.T) {
	model := newGatewayEventTestModel()
	block := NewMainACPTurnBlock("root-session")
	block.Events = append(block.Events,
		SubagentEvent{Kind: SEReasoning, Text: `Let me inspect the dispatch repository first.`},
		SubagentEvent{Kind: SEToolCall, CallID: "search-ready", Name: "SEARCH", Args: `"GetReadyDispatchesByRegion"`, Output: "3 hits in 2 files", Done: true},
		SubagentEvent{Kind: SEReasoning, Text: `Now I will check the busy and trigger paths.`},
		SubagentEvent{Kind: SEToolCall, CallID: "search-busy", Name: "SEARCH", Args: `"GetBusyEbsIDsByRegion"`, Output: "3 hits in 2 files", Done: true},
		SubagentEvent{Kind: SEReasoning, Text: `Now I will inspect trigger flow before patching.`},
		SubagentEvent{Kind: SEToolCall, CallID: "search-trigger", Name: "SEARCH", Args: `"TriggerEbsBackupDispatch"`},
	)

	rows := block.Render(BlockRenderContext{Width: 96, Height: 20, TermWidth: 96, Theme: model.theme})
	joined := strings.Join(renderedPlainRows(rows), "\n")
	if strings.Count(joined, "• Explored") != 1 {
		t.Fatalf("rendered rows = %q, want one live Explored group", joined)
	}
	for _, want := range []string{
		`Search "GetReadyDispatchesByRegion"`,
		`"GetBusyEbsIDsByRegion"`,
		"Now I will inspect trigger flow before patching.",
		`SEARCH "TriggerEbsBackupDispatch"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered rows = %q, want %q", joined, want)
		}
	}
	for _, hidden := range []string{"Let me inspect the dispatch repository first.", "Now I will check the busy and trigger paths."} {
		if strings.Contains(joined, hidden) {
			t.Fatalf("rendered rows = %q, settled exploration reasoning should be hidden in collapsed group", joined)
		}
	}
	for _, forbidden := range []string{"✓ SEARCH", "▾ SEARCH", "3 hits in 2 files", "╭"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("rendered rows = %q, should not expose raw exploration tool panel %q", joined, forbidden)
		}
	}
}

func TestGatewayFailedExplorationToolStaysInCompactSummary(t *testing.T) {
	model := newGatewayEventTestModel()
	block := NewMainACPTurnBlock("root-session")
	block.UpdateTool("glob-1", "GLOB", "**/public.gm_license.gen.go", "1 match", false, false)
	block.UpdateTool("glob-1", "GLOB", "**/public.gm_license.gen.go", "1 match", true, false)
	block.UpdateTool("search-1", "SEARCH", `"gm_license"`, "search failed", false, false)
	block.UpdateTool("search-1", "SEARCH", `"gm_license"`, "search failed", true, true)
	block.UpdateTool("search-2", "SEARCH", "", "failed", false, false)
	block.UpdateTool("search-2", "SEARCH", "", "failed", true, true)
	block.UpdateTool("patch-1", "PATCH", "public.gm_license.gen.go", "patched", false, false)
	block.UpdateTool("patch-1", "PATCH", "public.gm_license.gen.go", "patched", true, false)

	rows := block.Render(BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme})
	plain := renderedPlainRows(rows)
	joined := strings.Join(plain, "\n")
	for _, want := range []string{
		"• Explored",
		"  └ Glob **/public.gm_license.gen.go",
		`    Search "gm_license" failed`,
		"• Patched public.gm_license.gen.go",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered rows = %q, want %q", joined, want)
		}
	}
	if strings.Contains(joined, `✗ SEARCH`) {
		t.Fatalf("rendered rows = %q, failed exploration tool should stay inside Explored", joined)
	}
	if strings.Contains(joined, "failed failed") {
		t.Fatalf("rendered rows = %q, failed exploration detail should not duplicate status", joined)
	}

	var searchRow RenderedRow
	for _, row := range rows {
		if strings.Contains(row.Plain, `Search "gm_license" failed`) {
			searchRow = row
			break
		}
	}
	if searchRow.Plain == "" {
		t.Fatalf("rendered rows = %#v, want failed search summary row", plain)
	}
	wantStyledDetail := model.theme.SecondaryTextStyle().Render(`"gm_license" `) + model.theme.ToolErrorStyle().Render("failed")
	if !strings.Contains(searchRow.Styled, wantStyledDetail) {
		t.Fatalf("styled search row = %q, want failed status styled separately from query", searchRow.Styled)
	}
}

func TestExplorationToolDetailDoesNotDuplicateFailedStatus(t *testing.T) {
	tests := []struct {
		name string
		ev   SubagentEvent
		want string
	}{
		{
			name: "args gain failed once",
			ev:   SubagentEvent{Name: "SEARCH", Args: `"gm_license"`, Err: true},
			want: `"gm_license" failed`,
		},
		{
			name: "failed output stays single",
			ev:   SubagentEvent{Name: "SEARCH", Output: "failed", Err: true},
			want: "failed",
		},
		{
			name: "concrete error output is not suffixed",
			ev:   SubagentEvent{Name: "SEARCH", Output: "permission denied", Err: true},
			want: "permission denied",
		},
		{
			name: "error output starting with failed is not suffixed",
			ev:   SubagentEvent{Name: "SEARCH", Output: "failed to read file", Err: true},
			want: "failed to read file",
		},
		{
			name: "preduplicated failed output is normalized",
			ev:   SubagentEvent{Name: "SEARCH", Output: `"gm_license" failed failed`, Err: true},
			want: `"gm_license" failed`,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := explorationToolDetail(tt.ev); got != tt.want {
				t.Fatalf("explorationToolDetail() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExplorationToolDetailCompactsWindowsWorkspacePaths(t *testing.T) {
	workspace := `D:\repo`
	tests := []struct {
		name string
		ev   SubagentEvent
		want string
	}{
		{
			name: "read header",
			ev:   SubagentEvent{Name: "READ", Args: `D:\repo\internal\foo_test.go 1~40`},
			want: `foo_test.go 1~40`,
		},
		{
			name: "list directory",
			ev:   SubagentEvent{Name: "LIST", Args: `D:\repo\internal`},
			want: `internal`,
		},
		{
			name: "glob pattern",
			ev:   SubagentEvent{Name: "GLOB", Args: `D:\repo\*.go`},
			want: `*.go`,
		},
		{
			name: "glob output list",
			ev:   SubagentEvent{Name: "GLOB", Output: `D:\repo\internal\foo_test.go, D:\repo\cmd\main.go`},
			want: `foo_test.go, main.go`,
		},
		{
			name: "search query in path",
			ev:   SubagentEvent{Name: "SEARCH", Args: `"needle" in D:\repo\src`},
			want: `"needle" in src`,
		},
		{
			name: "workspace outside path falls back to basename",
			ev:   SubagentEvent{Name: "READ", Args: `D:\external\foo_test.go 1~12`},
			want: `foo_test.go 1~12`,
		},
		{
			name: "tagged absolute read path",
			ev:   SubagentEvent{Name: "READ", Args: `<path>D:\xue\code\system\docs\260430\002_gm_license_switch.dml.sql</path>`},
			want: `002_gm_license_switch.dml.sql`,
		},
		{
			name: "tagged relative read path strips tags",
			ev:   SubagentEvent{Name: "READ", Args: `<path>README.md</path> 1~2`},
			want: `README.md 1~2`,
		},
		{
			name: "tagged search query path",
			ev:   SubagentEvent{Name: "SEARCH", Args: `"needle" in <path>D:\repo\src</path>`},
			want: `"needle" in src`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := explorationToolDetailWithWorkspace(tt.ev, workspace); got != tt.want {
				t.Fatalf("explorationToolDetailWithWorkspace() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGatewayExploredGroupCompactsWindowsPathsWithWorkspace(t *testing.T) {
	model := newGatewayEventTestModel()
	workspace := `D:\repo`
	block := NewMainACPTurnBlock("root-session")
	for _, item := range []struct {
		callID string
		name   string
		args   string
		output string
	}{
		{callID: "read-1", name: "READ", args: `D:\repo\internal\foo_test.go`, output: `D:\repo\internal\foo_test.go 1~40`},
		{callID: "glob-1", name: "GLOB", args: `D:\repo\*.go`, output: `D:\repo\internal\foo_test.go, D:\repo\cmd\main.go`},
		{callID: "search-1", name: "SEARCH", args: `"needle" in D:\repo\src`, output: "2 matches"},
	} {
		block.UpdateTool(item.callID, item.name, item.args, item.output, false, false)
		block.UpdateTool(item.callID, item.name, item.args, item.output, true, false)
	}
	block.SetStatus("completed", "", "", time.Now())

	ctx := BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme, Workspace: workspace}
	joined := strings.Join(renderedPlainRows(block.Render(ctx)), "\n")
	for _, want := range []string{`foo_test.go`, `*.go`, `"needle" in src`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("collapsed Explored rows missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, `D:\repo`) {
		t.Fatalf("collapsed Explored rows leaked workspace path:\n%s", joined)
	}

	if !block.toggleExplorationExpanded(explorationStageKey(block.Events)) {
		t.Fatal("expected exploration group to expand")
	}
	joined = strings.Join(renderedPlainRows(block.Render(ctx)), "\n")
	for _, want := range []string{`foo_test.go`, `*.go`, `"needle" in src`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expanded Explored rows missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, `D:\repo`) {
		t.Fatalf("expanded Explored rows leaked workspace path:\n%s", joined)
	}
}

func TestGatewayCompletedExplorationSummaryWrapsAndAlignsDetails(t *testing.T) {
	model := newGatewayEventTestModel()
	block := NewMainACPTurnBlock("root-session")
	for i, name := range []string{
		"common.go 1~126",
		"register.go 1~20",
		"resource.go 1~86",
		"task.go 1~16",
		"command.go 1~121",
		"ebs.go 1~200",
		"ebs_backup.go 1~200",
	} {
		callID := fmt.Sprintf("read-%d", i+1)
		block.UpdateTool(callID, "READ", name, "ok", false, false)
		block.UpdateTool(callID, "READ", name, "ok", true, false)
	}
	block.SetStatus("completed", "", "", time.Now())

	rows := block.Render(BlockRenderContext{Width: 58, TermWidth: 58, Theme: model.theme})
	plain := renderedPlainRows(rows)
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, "• Explored") {
		t.Fatalf("rendered rows = %q, want compact exploration summary", joined)
	}
	if strings.Contains(joined, "...") {
		t.Fatalf("exploration summary should wrap instead of truncating, got\n%s", joined)
	}
	readIdx := indexOfRowContaining(plain, "  └ Read common.go")
	continuationIdx := indexOfRowContaining(plain, "task.go 1~16")
	if readIdx < 0 || continuationIdx < 0 || continuationIdx <= readIdx {
		t.Fatalf("rendered rows = %#v, want wrapped Read details", plain)
	}
	if !strings.HasPrefix(plain[continuationIdx], "       ") {
		t.Fatalf("continuation row = %q, want detail-column alignment", plain[continuationIdx])
	}
}

func TestGatewayACPExplorationNamedToolsCanRenderExploredGroup(t *testing.T) {
	model := newGatewayEventTestModel()
	sendACPTool := func(id string, name string, args string, output string) {
		rawInput := map[string]any{"path": args}
		if strings.EqualFold(name, "SEARCH") {
			rawInput = map[string]any{"query": args}
		}
		updated, _ := model.Update(gatewayEventMsg(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindToolCall,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session", Source: "acp"},
				ToolCall: &kernel.ToolCallPayload{
					CallID:   id,
					ToolName: name,
					RawInput: rawInput,
					Status:   kernel.ToolStatusRunning,
					Scope:    kernel.EventScopeMain,
				},
			}}))

		model = updated.(*Model)
		updated, _ = model.Update(gatewayEventMsg(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindToolResult,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session", Source: "acp"},
				ToolResult: &kernel.ToolResultPayload{
					CallID:   id,
					ToolName: name,
					RawInput: rawInput,
					Content:  testToolContent(toolResultLabel(name, rawInput)),
					Status:   kernel.ToolStatusCompleted,
					Scope:    kernel.EventScopeMain,
				},
			}}))

		model = updated.(*Model)
	}

	sendACPTool("read-1", "READ", "internal/kernel/types.go", "type Event struct{}")
	sendACPTool("search-1", "SEARCH", "EventKind", "42 matches")
	updated, _ := model.Update(gatewayEventMsg(kernel.EventEnvelope{Event: kernel.Event{
		Kind:       kernel.EventKindAssistantMessage,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Narrative: &kernel.NarrativePayload{
			Role:          kernel.NarrativeRoleAssistant,
			ReasoningText: "Now I can continue.",
			Final:         true,
			Scope:         kernel.EventScopeMain,
		},
	}}))

	model = updated.(*Model)

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme})
	var plain []string
	for _, row := range rows {
		plain = append(plain, row.Plain)
	}
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, "• Explored") ||
		!strings.Contains(joined, "  └ Read types.go") ||
		!strings.Contains(joined, `    Search "EventKind"`) ||
		!strings.Contains(joined, "Now I can continue.") {
		t.Fatalf("rendered rows = %q, want ACP read/search tools to fold into exploration group", joined)
	}
	if strings.Contains(joined, "type Event struct{}") || strings.Contains(joined, "42 matches") {
		t.Fatalf("rendered rows = %q, collapsed ACP exploration group should hide raw outputs", joined)
	}
}

func TestGatewayACPExplorationStatusOnlyCompletedDoesNotBecomeDetail(t *testing.T) {
	model := newGatewayEventTestModel()
	sendRead := func(id string) {
		updated, _ := model.Update(gatewayEventMsg(kernel.EventEnvelope{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session", Source: "acp"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   id,
				ToolName: "READ",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
			},
		}}))

		model = updated.(*Model)
		updated, _ = model.Update(gatewayEventMsg(kernel.EventEnvelope{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session", Source: "acp"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   id,
				ToolName: "READ",
				Content:  testToolContent("completed"),
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
			},
		}}))

		model = updated.(*Model)
	}

	sendRead("read-1")
	sendRead("read-2")
	updated, _ := model.Update(gatewayEventMsg(kernel.EventEnvelope{Event: kernel.Event{
		Kind:       kernel.EventKindAssistantMessage,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Narrative: &kernel.NarrativePayload{
			Role:          kernel.NarrativeRoleAssistant,
			ReasoningText: "continue",
			Final:         true,
			Scope:         kernel.EventScopeMain,
		},
	}}))

	model = updated.(*Model)

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	joined := strings.Join(renderedPlainRows(block.Render(BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme})), "\n")
	if !strings.Contains(joined, "• Explored") || !strings.Contains(joined, "  └ Read") {
		t.Fatalf("rendered rows = %q, want compact read exploration group", joined)
	}
	if strings.Contains(joined, "Read completed") || strings.Contains(joined, "completed, completed") {
		t.Fatalf("rendered rows = %q, status-only completions must not become exploration details", joined)
	}
}

func TestGatewayACPClaudeReadLifecycleKeepsIncrementalInput(t *testing.T) {
	model := newGatewayEventTestModel()
	path := "/Users/xueyongzhi/WorkDir/xueyongzhi/demo/a.py"
	sendUpdate := func(update session.ProtocolUpdate) {
		updated, _ := model.Update(gatewayEventMsg(kernel.EventEnvelope{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session", Source: "acp"},
			Protocol: &session.EventProtocol{
				UpdateType: update.SessionUpdate,
				Update:     &update,
			},
		}}))

		model = updated.(*Model)
	}

	sendUpdate(session.ProtocolUpdate{
		SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
		ToolCallID:    "read-1",
		Title:         "Read File",
		Kind:          "read",
		Status:        "pending",
		Meta:          map[string]any{"claudeCode": map[string]any{"toolName": "Read"}},
	})
	sendUpdate(session.ProtocolUpdate{
		SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
		ToolCallID:    "read-1",
		Title:         "Read a.py",
		Kind:          "read",
		RawInput:      map[string]any{"file_path": path},
		Locations: []session.ProtocolToolCallLocation{{
			Path: path,
		}},
		Meta: map[string]any{"claudeCode": map[string]any{"toolName": "Read"}},
	})
	sendUpdate(session.ProtocolUpdate{
		SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
		ToolCallID:    "read-1",
		Meta: map[string]any{"claudeCode": map[string]any{
			"toolName": "Read",
			"toolResponse": map[string]any{
				"type": "text",
				"file": map[string]any{"filePath": path, "numLines": 16},
			},
		}},
	})
	sendUpdate(session.ProtocolUpdate{
		SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
		ToolCallID:    "read-1",
		Status:        "completed",
		Meta:          map[string]any{"claudeCode": map[string]any{"toolName": "Read"}},
	})

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one merged Read lifecycle event", block.Events)
	}
	ev := block.Events[0]
	if !ev.Done {
		t.Fatalf("event = %#v, want completed Read event", ev)
	}
	if ev.Args != path {
		t.Fatalf("Read args = %q, want incremental file_path %q", ev.Args, path)
	}
	joined := strings.Join(renderedPlainRows(block.Render(BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme})), "\n")
	if !strings.Contains(joined, "Read "+path) {
		t.Fatalf("rendered rows = %q, want Read row with file path", joined)
	}
	if strings.Contains(joined, "Read completed") || strings.Contains(joined, "completed, completed") {
		t.Fatalf("rendered rows = %q, status-only final update must not replace Read parameters", joined)
	}
}

func TestGatewayACPThinkToolUsesTitleCaseDisplayName(t *testing.T) {
	model := newGatewayEventTestModel()
	prompt := "Quickly scan the project at /Users/xueyongzhi/WorkDir/xueyongzhi/demo and report directories and Python files."
	updated, _ := model.Update(gatewayEventMsg(kernel.EventEnvelope{Event: kernel.Event{
		Kind:       kernel.EventKindToolCall,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session", Source: "acp"},
		ToolCall: &kernel.ToolCallPayload{
			CallID:    "think-1",
			ToolName:  "think",
			ToolKind:  "think",
			ToolTitle: "Analyze demo codebase",
			RawInput: map[string]any{
				"prompt": prompt,
			},
			Status: kernel.ToolStatusRunning,
			Scope:  kernel.EventScopeMain,
		},
	}}))

	model = updated.(*Model)
	updated, _ = model.Update(gatewayEventMsg(kernel.EventEnvelope{Event: kernel.Event{
		Kind:       kernel.EventKindToolResult,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session", Source: "acp"},
		ToolResult: &kernel.ToolResultPayload{
			CallID:    "think-1",
			ToolName:  "think",
			ToolKind:  "think",
			ToolTitle: "Analyze demo codebase",
			RawInput: map[string]any{
				"prompt": prompt,
			},
			Content: testToolContent(prompt),
			Status:  kernel.ToolStatusCompleted,
			Scope:   kernel.EventScopeMain,
		},
	}}))

	model = updated.(*Model)

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	joined := strings.Join(renderedPlainRows(block.Render(BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme})), "\n")
	if !strings.Contains(joined, "• Think Analyze demo codebase") {
		t.Fatalf("rendered rows = %q, want title-case Think tool row", joined)
	}
	if strings.Contains(joined, "• think") {
		t.Fatalf("rendered rows = %q, want think mapped to Think", joined)
	}
	if !strings.Contains(joined, "Quickly scan") {
		t.Fatalf("rendered rows = %q, want think tool content still rendered", joined)
	}
}

func TestGatewayToolDisplayMetaRendersActionableSummaries(t *testing.T) {
	tests := []struct {
		name        string
		call        *kernel.ToolCallPayload
		result      *kernel.ToolResultPayload
		want        []string
		forbidden   []string
		expandPanel bool
		meta        map[string]any
		settleStep  bool
	}{
		{
			name: "read line range",
			call: &kernel.ToolCallPayload{
				CallID:   "read-1",
				ToolName: "READ",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/demo.py", "offset": 0, "limit": 100},
			},
			result: &kernel.ToolResultPayload{
				CallID:   "read-1",
				ToolName: "READ",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/demo.py", "offset": 0, "limit": 100},
				RawOutput: map[string]any{
					"path":       "/tmp/workspace/demo.py",
					"start_line": 1,
					"end_line":   100,
					"content":    "1: package main",
				},
				Content: testToolContent("demo.py 1~100"),
			},
			want:      []string{"• Read demo.py 1~100"},
			forbidden: []string{"│   /tmp/workspace/demo.py"},
		},
		{
			name: "glob count",
			call: &kernel.ToolCallPayload{
				CallID:   "glob-1",
				ToolName: "GLOB",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"pattern": "**/*.py"},
			},
			result: &kernel.ToolResultPayload{
				CallID:   "glob-1",
				ToolName: "GLOB",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"pattern": "**/*.py"},
				RawOutput: map[string]any{
					"pattern": "**/*.py",
					"count":   5,
					"matches": []any{"a.py", "b.py", "c.py", "d.py", "e.py"},
				},
				Content: testToolContent("**/*.py 5 matches"),
			},
			want: []string{"• Glob **/*.py 5 matches"},
		},
		{
			name: "command terminal panel",
			call: &kernel.ToolCallPayload{
				CallID:   "command-1",
				ToolName: "RUN_COMMAND",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": `echo "hello"`},
			},
			result: &kernel.ToolResultPayload{
				CallID:   "command-1",
				ToolName: "RUN_COMMAND",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": `echo "hello"`},
				RawOutput: map[string]any{
					"running":        false,
					"session_id":     "737bc26a-ff76-428f-8ca9-0fee8f2ae9ba",
					"state":          "completed",
					"supports_input": true,
					"stdout":         "hello\n",
					"exit_code":      0,
				},
				Content: testTerminalContent("hello"),
			},
			want:        []string{`• Ran echo "hello"`, "  └ hello"},
			forbidden:   []string{"session_id", "supports_input", "737bc26a"},
			expandPanel: true,
		},
		{
			name: "acp execute terminal panel uses ran verb",
			call: &kernel.ToolCallPayload{
				CallID:   "acp-exec-1",
				ToolName: "git",
				ToolKind: "execute",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeParticipant,
				RawInput: map[string]any{"cmd": "git diff --cached -- file.go"},
			},
			result: &kernel.ToolResultPayload{
				CallID:   "acp-exec-1",
				ToolName: "git",
				ToolKind: "execute",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeParticipant,
				RawInput: map[string]any{"cmd": "git diff --cached -- file.go"},
				RawOutput: map[string]any{
					"stdout":    "diff --git a/file.go b/file.go\n",
					"exit_code": 0,
				},
				Content: testTerminalContent("diff --git a/file.go b/file.go"),
			},
			want:        []string{"• Ran git diff --cached -- file.go", "  └ diff --git a/file.go b/file.go"},
			forbidden:   []string{"• git diff", "RUN_COMMAND diff", "╭", "╰"},
			expandPanel: true,
		},
		{
			name: "spawn terminal panel",
			call: &kernel.ToolCallPayload{
				CallID:   "spawn-1",
				ToolName: "SPAWN",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"prompt": "write fibonacci"},
			},
			result: &kernel.ToolResultPayload{
				CallID:   "spawn-1",
				ToolName: "SPAWN",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"prompt": "write fibonacci"},
				RawOutput: map[string]any{
					"agent":            "self",
					"agent_id":         "self-001",
					"handle":           "leo",
					"internal_task_id": "spawn-task-1",
					"mention":          "@leo",
					"running":          false,
					"state":            "completed",
					"task_id":          "leo",
					"stdout":           "用例| C输出欢迎 |\n_empty |\n|\n了根据），\n",
					"result":           "stale result that is not the final message\n",
					"final_message":    "child line 1\nchild line 2\n",
				},
				Content: testTerminalContent("child line 1\nchild line 2"),
			},
			want:        []string{"• Spawned", "  └ child line 1", "    child line 2"},
			forbidden:   []string{"task / running", "state completed", "spawn-task-1", "self-001", "internal_task_id", "@leo", "用例| C输出欢迎", "_empty", "了根据", "stale result"},
			expandPanel: true,
		},
		{
			name: "command task snapshot does not expose raw session json",
			call: &kernel.ToolCallPayload{
				CallID:   "command-task-1",
				ToolName: "RUN_COMMAND",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": `sleep 10`},
			},
			result: &kernel.ToolResultPayload{
				CallID:   "command-task-1",
				ToolName: "RUN_COMMAND",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": `sleep 10`},
				RawOutput: map[string]any{
					"running":        true,
					"session_id":     "556d7447-4554-4fb9-ad1c-bb5a2e0f85ac",
					"state":          "running",
					"supports_input": true,
					"task_id":        "task-9",
				},
			},
			want:        []string{`• Ran sleep 10`},
			forbidden:   []string{"task / running", "task task-9", "state running", "session_id", "supports_input", "556d7447"},
			expandPanel: true,
		},
		{
			name: "task control panel",
			call: &kernel.ToolCallPayload{
				CallID:   "task-1",
				ToolName: "TASK",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"action": "wait", "task_id": "task-9", "yield_time_ms": 5000},
			},
			result: &kernel.ToolResultPayload{
				CallID:   "task-1",
				ToolName: "TASK",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"action": "wait", "task_id": "task-9", "yield_time_ms": 5000},
				RawOutput: map[string]any{
					"running":        true,
					"session_id":     "556d7447-4554-4fb9-ad1c-bb5a2e0f85ac",
					"state":          "running",
					"supports_input": true,
					"task_id":        "task-9",
				},
			},
			want:        []string{"• Tasks", "  └ Wait 5s"},
			forbidden:   []string{"TASK", "task-9", "task / control", "state running", "session_id", "supports_input", "556d7447"},
			expandPanel: true,
			settleStep:  true,
		},
		{
			name: "write rich diff panel",
			call: &kernel.ToolCallPayload{
				CallID:   "write-1",
				ToolName: "WRITE",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/tool_demo_summary.md", "content": "one\ntwo\n"},
			},
			result: &kernel.ToolResultPayload{
				CallID:   "write-1",
				ToolName: "WRITE",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/tool_demo_summary.md", "content": "one\ntwo\n"},
				RawOutput: map[string]any{
					"path":          "/tmp/workspace/tool_demo_summary.md",
					"created":       true,
					"added_lines":   2,
					"removed_lines": 0,
				},
				Content: testToolContent("tool_demo_summary.md +2 -0\ndiff / hunk\n@@ -0,0 +1,2 @@\n+one\n+two"),
			},
			want:        []string{"• Wrote tool_demo_summary.md +2 -0", "diff / hunk", "+one", "+two"},
			forbidden:   []string{"│", "╭", "╰", "tool_demo_summary.md +2 -0\n  tool_demo_summary.md +2 -0"},
			expandPanel: true,
		},
		{
			name: "failed write does not claim success",
			call: &kernel.ToolCallPayload{
				CallID:   "write-failed-1",
				ToolName: "WRITE",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/workflow.go", "content": "package workflow\n"},
			},
			result: &kernel.ToolResultPayload{
				CallID:    "write-failed-1",
				ToolName:  "WRITE",
				Status:    kernel.ToolStatusFailed,
				Scope:     kernel.EventScopeMain,
				RawInput:  map[string]any{"path": "/tmp/workspace/workflow.go", "content": "package workflow\n"},
				RawOutput: map[string]any{"error": "Sandbox permission denied. Use a writable workspace path or request elevated permissions."},
				Content:   testToolContent("Sandbox permission denied. Use a writable workspace path or request elevated permissions."),
			},
			want:        []string{"• Write failed workflow.go", "└ Sandbox permission denied"},
			forbidden:   []string{"• Wrote workflow.go", "╭", "╰", "│ ! workflow.go"},
			expandPanel: true,
		},
		{
			name: "failed patch preserves concrete error reason",
			call: &kernel.ToolCallPayload{
				CallID:   "patch-failed-1",
				ToolName: "PATCH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/gm_license.go", "old": "licenseEntity.ESN", "new": "licenseEntity.Esn"},
			},
			result: &kernel.ToolResultPayload{
				CallID:    "patch-failed-1",
				ToolName:  "PATCH",
				Status:    kernel.ToolStatusFailed,
				Scope:     kernel.EventScopeMain,
				RawInput:  map[string]any{"path": "/tmp/workspace/gm_license.go", "old": "licenseEntity.ESN", "new": "licenseEntity.Esn"},
				RawOutput: map[string]any{"error": `tool: PATCH target "gm_license.go" did not contain an exact match for "old"`},
				Content:   testToolContent(`tool: PATCH target "gm_license.go" did not contain an exact match for "old"`),
			},
			want:        []string{"• Patch failed gm_license.go", `└ tool: PATCH target "gm_license.go" did not contain an exact match for "old"`},
			forbidden:   []string{"  └ failed", "╭", "╰"},
			expandPanel: true,
		},
		{
			name: "patch rich diff panel",
			call: &kernel.ToolCallPayload{
				CallID:   "patch-1",
				ToolName: "PATCH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/demo.py", "old": "old line", "new": "new line"},
			},
			result: &kernel.ToolResultPayload{
				CallID:   "patch-1",
				ToolName: "PATCH",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/demo.py", "old": "old line", "new": "new line"},
				RawOutput: map[string]any{
					"path":          "/tmp/workspace/demo.py",
					"hunk":          "@@ -1,1 +1,1 @@",
					"added_lines":   1,
					"removed_lines": 1,
				},
				Content: testToolContent("demo.py +1 -1\ndiff / hunk\n@@ -1,1 +1,1 @@\n-old line\n+new line"),
			},
			want:        []string{"• Patched demo.py +1 -1", "diff / hunk", "-old line", "+new line"},
			forbidden:   []string{"│", "╭", "╰", "demo.py +1 -1\n  demo.py +1 -1"},
			expandPanel: true,
		},
		{
			name: "patch replace all still renders old new diff",
			call: &kernel.ToolCallPayload{
				CallID:   "patch-all-1",
				ToolName: "PATCH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/gm_license_repo.go", "old": "entity.GMLicense", "new": "entity.GmLicense", "replace_all": true},
			},
			result: &kernel.ToolResultPayload{
				CallID:   "patch-all-1",
				ToolName: "PATCH",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/gm_license_repo.go", "old": "entity.GMLicense", "new": "entity.GmLicense", "replace_all": true},
				RawOutput: map[string]any{
					"path":          "/tmp/workspace/gm_license_repo.go",
					"replaced":      28,
					"added_lines":   28,
					"removed_lines": 28,
				},
				Content: testToolContent("gm_license_repo.go +28 -28\ndiff / hunk\n@@ repeated replacement: 28 matches @@\n-entity.GMLicense\n+entity.GmLicense"),
			},
			want:        []string{"• Patched gm_license_repo.go +28 -28", "diff / hunk", "@@ repeated replacement: 28 matches @@", "-entity.GMLicense", "+entity.GmLicense"},
			forbidden:   []string{"╭", "╰", "gm_license_repo.go +28 -28\n  gm_license_repo.go +28 -28"},
			expandPanel: true,
		},
		{
			name: "patch structured multi hunk diff panel",
			call: &kernel.ToolCallPayload{
				CallID:   "patch-hunks-1",
				ToolName: "PATCH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/gm_license_repo.go", "old": "entity.GMLicense", "new": "entity.GmLicense", "replace_all": true},
			},
			result: &kernel.ToolResultPayload{
				CallID:   "patch-hunks-1",
				ToolName: "PATCH",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/gm_license_repo.go", "old": "entity.GMLicense", "new": "entity.GmLicense", "replace_all": true},
				RawOutput: map[string]any{
					"path":         "/tmp/workspace/gm_license_repo.go",
					"replacements": 2,
				},
				Content: testToolContent("gm_license_repo.go +2 -2\ndiff / hunk\n@@ -2,3 +2,3 @@\n context-a\n-entity.GMLicense\n+entity.GmLicense\n context-b\n@@ -20,3 +20,3 @@\n context-c\n-entity.GMLicense\n+entity.GmLicense\n context-d"),
			},
			want:        []string{"• Patched gm_license_repo.go +2 -2", "diff / hunk", "@@ -2,3 +2,3 @@", "@@ -20,3 +20,3 @@", "-entity.GMLicense", "+entity.GmLicense"},
			forbidden:   []string{"@@ repeated replacement", "╭", "╰"},
			expandPanel: true,
			meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"tool": map[string]any{
							"added_lines":   2,
							"removed_lines": 2,
							"diff_hunks": []any{
								map[string]any{
									"header": "@@ -2,3 +2,3 @@",
									"lines":  []any{" context-a", "-entity.GMLicense", "+entity.GmLicense", " context-b"},
								},
								map[string]any{
									"header": "@@ -20,3 +20,3 @@",
									"lines":  []any{" context-c", "-entity.GMLicense", "+entity.GmLicense", " context-d"},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "patch meta diff panel without content",
			call: &kernel.ToolCallPayload{
				CallID:   "patch-meta-1",
				ToolName: "PATCH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/gm_license_repo.go"},
			},
			result: &kernel.ToolResultPayload{
				CallID:   "patch-meta-1",
				ToolName: "PATCH",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/gm_license_repo.go"},
				RawOutput: map[string]any{
					"path": "/tmp/workspace/gm_license_repo.go",
				},
			},
			want:        []string{"• Patched gm_license_repo.go +1 -1", "diff / hunk", "@@ -2,3 +2,3 @@", "-entity.GMLicense", "+entity.GmLicense"},
			forbidden:   []string{"╭", "╰", "completed"},
			expandPanel: true,
			meta: testRuntimeToolMeta(map[string]any{
				"path":          "/tmp/workspace/gm_license_repo.go",
				"added_lines":   1,
				"removed_lines": 1,
				"diff_hunks": []any{
					map[string]any{
						"header": "@@ -2,3 +2,3 @@",
						"lines":  []any{" context-a", "-entity.GMLicense", "+entity.GmLicense", " context-b"},
					},
				},
			}),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			model := newGatewayEventTestModel()
			updated, _ := model.Update(gatewayEventMsg(kernel.EventEnvelope{
				Event: kernel.Event{
					Kind:       kernel.EventKindToolCall,
					SessionRef: session.SessionRef{SessionID: "root-session"},
					ToolCall:   tt.call,
				}}))

			model = updated.(*Model)
			updated, _ = model.Update(gatewayEventMsg(kernel.EventEnvelope{
				Event: kernel.Event{
					Kind:       kernel.EventKindToolResult,
					SessionRef: session.SessionRef{SessionID: "root-session"},
					Meta:       tt.meta,
					ToolResult: tt.result,
				}}))

			model = updated.(*Model)
			if tt.settleStep {
				updated, _ = model.Update(gatewayEventMsg(kernel.EventEnvelope{
					Event: kernel.Event{
						Kind:       kernel.EventKindAssistantMessage,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Narrative: &kernel.NarrativePayload{
							Role:  kernel.NarrativeRoleAssistant,
							Text:  "next step",
							Final: true,
							Scope: kernel.EventScopeMain,
						},
					}}))

				model = updated.(*Model)
			}
			block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
			if !ok {
				t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
			}
			if tt.expandPanel {
				block.setToolPanelExpanded(tt.result.CallID, true)
			}
			rows := block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})
			plain := make([]string, 0, len(rows))
			for _, row := range rows {
				plain = append(plain, row.Plain)
			}
			joined := strings.Join(plain, "\n")
			for _, want := range tt.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("rendered rows = %q, want %q", joined, want)
				}
			}
			for _, forbidden := range tt.forbidden {
				if strings.Contains(joined, forbidden) {
					t.Fatalf("rendered rows = %q, should not contain %q", joined, forbidden)
				}
			}
		})
	}
}
