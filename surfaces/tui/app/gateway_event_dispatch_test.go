package tuiapp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
)

func newGatewayEventTestModel() *Model {
	return NewModel(Config{
		AppName:         "CAELIS",
		Version:         "dev",
		Workspace:       "/tmp/workspace",
		ShowWelcomeCard: true,
		Commands:        DefaultCommands(),
		Wizards:         DefaultWizards(),
	})
}

func hasBlankRowBetween(lines []string, start int, end int) bool {
	if start < 0 || end < 0 || start >= end {
		return false
	}
	for i := start + 1; i < end; i++ {
		if strings.TrimSpace(lines[i]) == "" {
			return true
		}
	}
	return false
}

func countTrailingBlankRows(lines []string) int {
	count := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			break
		}
		count++
	}
	return count
}

func tailPlainRows(lines []string, height int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	return lines[len(lines)-height:]
}

func TestRanHeaderStylesShellCommandTokens(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	plain := "• Ran GOMODCACHE=/tmp/cache git status --short --branch"
	styled := styleACPTranscriptHeader(BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme}, plain)
	if got := ansi.Strip(styled); got != plain {
		t.Fatalf("styled header strips to %q, want %q", got, plain)
	}
	if styled == plain || !strings.Contains(styled, "\x1b[") {
		t.Fatalf("styled header = %q, want ANSI token styling", styled)
	}
}

func TestRanHeaderShellCommandUsesDistinctTokenStyles(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 160, TermWidth: 160, Theme: model.theme}
	plain := `• Ran ls -la /home/xueyongzhi/WorkDir/code/demo/.venv/bin/ 2>/dev/null | head -20; echo "---"; cat /home/xueyongzhi/WorkDir/code/demo/.venv/pyvenv.cfg 2>/dev/null`
	styled := styleACPTranscriptHeader(ctx, plain)
	if got := ansi.Strip(styled); got != plain {
		t.Fatalf("styled header strips to %q, want %q", got, plain)
	}

	ranStyle := toolActionStyle(ctx, "Ran").Render("token")
	commandStyle := shellTokenStyle(ctx, shellTokenCommand).Render("token")
	operatorStyle := shellTokenStyle(ctx, shellTokenOperator).Render("token")
	redirectStyle := shellTokenStyle(ctx, shellTokenRedirect).Render("token")
	pathStyle := shellTokenStyle(ctx, shellTokenPath).Render("token")
	for name, rendered := range map[string]string{
		"ran":      ranStyle,
		"command":  commandStyle,
		"operator": operatorStyle,
		"redirect": redirectStyle,
		"path":     pathStyle,
	} {
		if rendered == "token" || !strings.Contains(rendered, "\x1b[") {
			t.Fatalf("%s style = %q, want ANSI styling", name, rendered)
		}
	}
	if ranStyle == commandStyle {
		t.Fatalf("Ran action and shell commands should not share the same style: %q", ranStyle)
	}
	if commandStyle == operatorStyle {
		t.Fatalf("shell commands and operators should not share the same style: %q", commandStyle)
	}
	if operatorStyle == redirectStyle {
		t.Fatalf("shell operators and redirects should not share the same style: %q", operatorStyle)
	}
}

func TestShellCommandTokensClassifyCompoundBashCommand(t *testing.T) {
	command := `ls -la /home/xueyongzhi/WorkDir/code/demo/.venv/bin/ 2>/dev/null | head -20; echo "---"; cat /home/xueyongzhi/WorkDir/code/demo/.venv/pyvenv.cfg 2>/dev/null`
	got := compactShellTokens(shellCommandTokens(command))
	want := []shellCommandToken{
		{Text: "ls", Class: shellTokenCommand},
		{Text: "-la", Class: shellTokenFlag},
		{Text: "/home/xueyongzhi/WorkDir/code/demo/.venv/bin/", Class: shellTokenPath},
		{Text: "2>", Class: shellTokenRedirect},
		{Text: "/dev/null", Class: shellTokenPath},
		{Text: "|", Class: shellTokenOperator},
		{Text: "head", Class: shellTokenCommand},
		{Text: "-20", Class: shellTokenFlag},
		{Text: ";", Class: shellTokenOperator},
		{Text: "echo", Class: shellTokenCommand},
		{Text: `"---"`, Class: shellTokenQuoted},
		{Text: ";", Class: shellTokenOperator},
		{Text: "cat", Class: shellTokenCommand},
		{Text: "/home/xueyongzhi/WorkDir/code/demo/.venv/pyvenv.cfg", Class: shellTokenPath},
		{Text: "2>", Class: shellTokenRedirect},
		{Text: "/dev/null", Class: shellTokenPath},
	}
	if len(got) != len(want) {
		t.Fatalf("token count = %d, want %d\ngot:  %#v\nwant: %#v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token %d = %#v, want %#v\nall: %#v", i, got[i], want[i], got)
		}
	}
}

func compactShellTokens(tokens []shellCommandToken) []shellCommandToken {
	out := make([]shellCommandToken, 0, len(tokens))
	for _, token := range tokens {
		if token.Class == shellTokenSpace {
			continue
		}
		out = append(out, token)
	}
	return out
}

func TestModelUpdateConsumesGatewayAssistantEventIntoMainTurnBlock(t *testing.T) {
	model := newGatewayEventTestModel()

	updated, _ := model.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:  kernel.NarrativeRoleAssistant,
				Text:  "gateway answer",
				Final: true,
				Scope: kernel.EventScopeMain,
			},
		},
	})
	m := updated.(*Model)

	if got := len(m.doc.Blocks()); got != 1 {
		t.Fatalf("doc.Len() = %d, want 1", got)
	}
	block, ok := m.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", m.doc.Blocks()[0])
	}
	if len(block.Events) != 1 || block.Events[0].Kind != SEAssistant || block.Events[0].Text != "gateway answer" {
		t.Fatalf("main turn events = %#v, want assistant narrative event", block.Events)
	}
}

func TestGatewayReasoningStreamPreservesWhitespaceOnlyDeltas(t *testing.T) {
	model := NewModel(Config{NoColor: true, StreamTickInterval: 16 * time.Millisecond})
	model.viewport.SetWidth(80)
	model.viewport.SetHeight(20)

	now := time.Now()
	for _, text := range []string{"The", " ", "sandbox"} {
		updated, _ := model.Update(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative: &kernel.NarrativePayload{
					Role:          kernel.NarrativeRoleAssistant,
					ReasoningText: text,
					Final:         false,
					Visibility:    string(session.VisibilityUIOnly),
					UpdateType:    string(session.ProtocolUpdateTypeAgentThought),
					Scope:         kernel.EventScopeMain,
				},
			},
		})
		model = updated.(*Model)
		updated, _ = model.Update(frameTickMsg{kind: frameTickRenderDrain, at: now})
		model = updated.(*Model)
		now = now.Add(16 * time.Millisecond)
	}

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %T, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	if got := block.Events[0].Text; got != "The sandbox" {
		t.Fatalf("reasoning stream text = %q, want whitespace-only delta preserved", got)
	}
}

func TestGatewayContextCanceledRendersUserInterrupt(t *testing.T) {
	model := newGatewayEventTestModel()

	updated, _ := model.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:  kernel.NarrativeRoleAssistant,
				Text:  "partial answer",
				Final: false,
				Scope: kernel.EventScopeMain,
			},
		},
	})
	m := updated.(*Model)
	updated, _ = m.Update(kernel.EventEnvelope{
		Err: &kernel.Error{
			Message: "providers: sse scanner: context canceled",
			Cause:   context.Canceled,
		},
	})
	m = updated.(*Model)

	var sawErrorText bool
	var renderedMain string
	for _, item := range m.doc.Blocks() {
		switch block := item.(type) {
		case *MainACPTurnBlock:
			rows := block.Render(BlockRenderContext{Width: 80, TermWidth: 80, Theme: m.theme})
			plain := make([]string, 0, len(rows))
			for _, row := range rows {
				plain = append(plain, row.Plain)
			}
			renderedMain = strings.Join(plain, "\n")
		case *TranscriptBlock:
			if strings.Contains(block.Raw, "context canceled") || strings.HasPrefix(strings.TrimSpace(block.Raw), "error:") {
				sawErrorText = true
			}
		}
	}
	if !strings.Contains(renderedMain, "⊘ interrupted") {
		t.Fatalf("main turn render = %q, want interrupted status", renderedMain)
	}
	if strings.Contains(renderedMain, "✗ failed") {
		t.Fatalf("main turn render = %q, should not show failed", renderedMain)
	}
	if strings.Contains(renderedMain, "User interrupt") {
		t.Fatalf("main turn render = %q, should not duplicate user interrupt note", renderedMain)
	}
	if sawErrorText {
		t.Fatalf("doc blocks = %#v, should not render context canceled as error", m.doc.Blocks())
	}
}

func TestModelUpdateConsumesGatewayToolEventsWithoutTranscriptRecovery(t *testing.T) {
	model := newGatewayEventTestModel()

	updated, _ := model.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "READ",
				RawInput: map[string]any{"path": "/tmp/demo.txt"},
				Status:   "running",
				Scope:    kernel.EventScopeMain,
			},
		},
	})
	m := updated.(*Model)
	updated, _ = m.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:    "call-1",
				ToolName:  "READ",
				RawInput:  map[string]any{"path": "/tmp/demo.txt"},
				RawOutput: map[string]any{"path": "/tmp/demo.txt"},
				Status:    "completed",
				Scope:     kernel.EventScopeMain,
			},
		},
	})
	m = updated.(*Model)

	if got := len(m.doc.Blocks()); got != 1 {
		t.Fatalf("doc.Len() = %d, want 1", got)
	}
	block, ok := m.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", m.doc.Blocks()[0])
	}
	if len(block.Events) != 1 {
		t.Fatalf("len(block.Events) = %d, want 1", len(block.Events))
	}
	ev := block.Events[0]
	if ev.Kind != SEToolCall || ev.CallID != "call-1" || ev.Name != "READ" || !ev.Done {
		t.Fatalf("tool event = %#v, want finalized direct tool event", ev)
	}
	for _, item := range m.doc.Blocks() {
		if _, ok := item.(*TranscriptBlock); ok {
			t.Fatalf("unexpected transcript block %#v; want direct structured tool rendering", item)
		}
	}
}

func TestGatewayRunningToolResultStreamsOutputWithoutFinalizing(t *testing.T) {
	model := newGatewayEventTestModel()

	updated, _ := model.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "BASH",
				RawInput: map[string]any{"command": `go test ./kernel/...`},
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
			},
		},
	})
	model = updated.(*Model)
	updated, _ = model.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:    "call-1",
				ToolName:  "BASH",
				RawInput:  map[string]any{"command": `go test ./kernel/...`},
				RawOutput: map[string]any{"stdout": "stdout resolving packages"},
				Status:    kernel.ToolStatusRunning,
				Scope:     kernel.EventScopeMain,
			},
		},
	})
	model = updated.(*Model)

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	if len(block.Events) != 1 {
		t.Fatalf("len(block.Events) = %d, want 1", len(block.Events))
	}
	ev := block.Events[0]
	if ev.Done {
		t.Fatalf("tool event = %#v, want running output to remain non-final", ev)
	}
	if !strings.Contains(ev.Output, "stdout resolving packages") {
		t.Fatalf("tool event = %#v, want streaming output", ev)
	}
}

func TestGatewayCompletedExplorationToolDefaultsCollapsed(t *testing.T) {
	model := newGatewayEventTestModel()

	updated, _ := model.Update(kernel.EventEnvelope{
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
		},
	})
	model = updated.(*Model)
	updated, _ = model.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:    "call-1",
				ToolName:  "READ",
				RawInput:  map[string]any{"path": "internal/kernel/types.go"},
				RawOutput: map[string]any{"text": "package core\n\ntype Event struct{}"},
				Status:    kernel.ToolStatusCompleted,
				Scope:     kernel.EventScopeMain,
			},
		},
	})
	model = updated.(*Model)

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	if block.toolPanelExpanded("call-1") {
		t.Fatalf("READ tool panel should default collapsed after completion; expanded map = %#v", block.ExpandedTools)
	}
	if !model.tryToggleACPToolPanelToken(block.BlockID(), "acp_tool_panel:call-1") {
		t.Fatal("expected READ tool panel toggle token to expand collapsed panel")
	}
	if !block.toolPanelExpanded("call-1") {
		t.Fatalf("READ tool panel should expand after click; expanded map = %#v", block.ExpandedTools)
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
		updated, _ := model.Update(kernel.EventEnvelope{
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
			},
		})
		model = updated.(*Model)
		updated, _ = model.Update(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindToolResult,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				ToolResult: &kernel.ToolResultPayload{
					CallID:    id,
					ToolName:  name,
					RawInput:  rawInput,
					RawOutput: map[string]any{"text": output},
					Status:    kernel.ToolStatusCompleted,
					Scope:     kernel.EventScopeMain,
				},
			},
		})
		model = updated.(*Model)
	}
	sendReasoning := func(text string) {
		updated, _ := model.Update(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative: &kernel.NarrativePayload{
					Role:          kernel.NarrativeRoleAssistant,
					ReasoningText: text,
					Final:         true,
					Scope:         kernel.EventScopeMain,
				},
			},
		})
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
}

func TestGatewaySingleExplorationStepSettlesOnNextAssistantNarrative(t *testing.T) {
	model := newGatewayEventTestModel()
	sendReasoning := func(text string) {
		updated, _ := model.Update(kernel.EventEnvelope{Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:          kernel.NarrativeRoleAssistant,
				ReasoningText: text,
				Final:         true,
				Scope:         kernel.EventScopeMain,
			},
		}})
		model = updated.(*Model)
	}
	sendRead := func(id string, path string) {
		rawInput := map[string]any{"path": path}
		updated, _ := model.Update(kernel.EventEnvelope{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   id,
				ToolName: "READ",
				RawInput: rawInput,
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
			},
		}})
		model = updated.(*Model)
		updated, _ = model.Update(kernel.EventEnvelope{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:    id,
				ToolName:  "READ",
				RawInput:  rawInput,
				RawOutput: map[string]any{"text": "config contents"},
				Status:    kernel.ToolStatusCompleted,
				Scope:     kernel.EventScopeMain,
			},
		}})
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
		updated, _ := model.Update(kernel.EventEnvelope{
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
			},
		})
		model = updated.(*Model)
		updated, _ = model.Update(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindToolResult,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session", Source: "acp"},
				ToolResult: &kernel.ToolResultPayload{
					CallID:    id,
					ToolName:  name,
					RawInput:  rawInput,
					RawOutput: map[string]any{"text": output},
					Status:    kernel.ToolStatusCompleted,
					Scope:     kernel.EventScopeMain,
				},
			},
		})
		model = updated.(*Model)
	}

	sendACPTool("read-1", "READ", "internal/kernel/types.go", "type Event struct{}")
	sendACPTool("search-1", "SEARCH", "EventKind", "42 matches")
	updated, _ := model.Update(kernel.EventEnvelope{Event: kernel.Event{
		Kind:       kernel.EventKindAssistantMessage,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Narrative: &kernel.NarrativePayload{
			Role:          kernel.NarrativeRoleAssistant,
			ReasoningText: "Now I can continue.",
			Final:         true,
			Scope:         kernel.EventScopeMain,
		},
	}})
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

func TestGatewayToolDisplayMetaRendersActionableSummaries(t *testing.T) {
	tests := []struct {
		name        string
		call        *kernel.ToolCallPayload
		result      *kernel.ToolResultPayload
		want        []string
		forbidden   []string
		expandPanel bool
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
			},
			want: []string{"• Glob **/*.py 5 matches"},
		},
		{
			name: "bash terminal panel",
			call: &kernel.ToolCallPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": `echo "hello"`},
			},
			result: &kernel.ToolResultPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
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
			},
			want:        []string{"• Ran git diff --cached -- file.go", "  └ diff --git a/file.go b/file.go"},
			forbidden:   []string{"• git diff", "BASH diff", "╭", "╰"},
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
			},
			want:        []string{"• Spawned", "  └ child line 1", "    child line 2"},
			forbidden:   []string{"task / running", "state completed", "spawn-task-1", "self-001", "internal_task_id", "@leo", "用例| C输出欢迎", "_empty", "了根据", "stale result"},
			expandPanel: true,
		},
		{
			name: "bash task snapshot does not expose raw session json",
			call: &kernel.ToolCallPayload{
				CallID:   "bash-task-1",
				ToolName: "BASH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": `sleep 10`},
			},
			result: &kernel.ToolResultPayload{
				CallID:   "bash-task-1",
				ToolName: "BASH",
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
					"path":          "/tmp/workspace/gm_license_repo.go",
					"replaced":      2,
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
			want:        []string{"• Patched gm_license_repo.go +2 -2", "diff / hunk", "@@ -2,3 +2,3 @@", "@@ -20,3 +20,3 @@", "-entity.GMLicense", "+entity.GmLicense"},
			forbidden:   []string{"@@ repeated replacement", "╭", "╰"},
			expandPanel: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			model := newGatewayEventTestModel()
			updated, _ := model.Update(kernel.EventEnvelope{
				Event: kernel.Event{
					Kind:       kernel.EventKindToolCall,
					SessionRef: session.SessionRef{SessionID: "root-session"},
					ToolCall:   tt.call,
				},
			})
			model = updated.(*Model)
			updated, _ = model.Update(kernel.EventEnvelope{
				Event: kernel.Event{
					Kind:       kernel.EventKindToolResult,
					SessionRef: session.SessionRef{SessionID: "root-session"},
					ToolResult: tt.result,
				},
			})
			model = updated.(*Model)
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

func TestGatewayTaskControlsMergeIntoTaskStage(t *testing.T) {
	model := newGatewayEventTestModel()
	sendReasoning := func(text string) {
		updated, _ := model.Update(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative: &kernel.NarrativePayload{
					Role:          kernel.NarrativeRoleAssistant,
					ReasoningText: text,
					Final:         true,
					Scope:         kernel.EventScopeMain,
				},
			},
		})
		model = updated.(*Model)
	}
	sendReasoning("两个子任务已启动")
	for _, item := range []struct {
		callID  string
		action  string
		input   string
		yieldMS int
		handle  string
	}{
		{callID: "task-0", action: "write", input: "Alice", handle: "task-9"},
		{callID: "task-1", action: "wait", yieldMS: 5000, handle: "ella"},
		{callID: "task-2", action: "wait", yieldMS: 8000, handle: "task-9"},
	} {
		rawInput := map[string]any{"action": item.action, "task_id": item.handle, "yield_time_ms": item.yieldMS}
		if item.input != "" {
			rawInput["input"] = item.input
		}
		updated, _ := model.Update(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindToolCall,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				ToolCall: &kernel.ToolCallPayload{
					CallID:   item.callID,
					ToolName: "TASK",
					Status:   kernel.ToolStatusRunning,
					Scope:    kernel.EventScopeMain,
					RawInput: rawInput,
				},
			},
		})
		model = updated.(*Model)
		updated, _ = model.Update(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindToolResult,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				ToolResult: &kernel.ToolResultPayload{
					CallID:   item.callID,
					ToolName: "TASK",
					Status:   kernel.ToolStatusRunning,
					Scope:    kernel.EventScopeMain,
					RawInput: rawInput,
					RawOutput: map[string]any{
						"running": true,
						"state":   "running",
						"task_id": item.handle,
					},
				},
			},
		})
		model = updated.(*Model)
	}
	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})
	plain := make([]string, 0, len(rows))
	for _, row := range rows {
		plain = append(plain, row.Plain)
	}
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, "› 两个子任务已启动") ||
		!strings.Contains(joined, "• Tasks") ||
		!strings.Contains(joined, `  └ Write "Alice"`) ||
		!strings.Contains(joined, `    Wait ella 5s`) ||
		!strings.Contains(joined, `    Wait 8s`) {
		t.Fatalf("rendered rows = %q, want live reasoning followed by merged TASK controls", joined)
	}
	if strings.Contains(joined, "TASK") || strings.Contains(joined, "task-9") {
		t.Fatalf("rendered rows = %q, should hide raw TASK tool and task id", joined)
	}
	if !model.tryToggleACPToolPanelToken(block.BlockID(), "acp_task_stage:tasks:task-0,task-1,task-2") {
		t.Fatal("expected task stage click token to expand grouped TASK controls")
	}
	rows = block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if strings.Contains(joined, `  └ 两个子任务已启动`) {
		t.Fatalf("expanded live rows = %q, should keep live reasoning outside the TASK group", joined)
	}
	if !strings.Contains(joined, `› 两个子任务已启动`) ||
		!strings.Contains(joined, `  └ Write "Alice"`) ||
		!strings.Contains(joined, `    Wait ella 5s`) {
		t.Fatalf("expanded live rows = %q, want visible reasoning and expanded controls", joined)
	}
	sendReasoning("继续处理")
	rows = block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, `  └ 两个子任务已启动`) ||
		!strings.Contains(joined, `    Write "Alice"`) ||
		!strings.Contains(joined, `    Wait ella 5s`) ||
		!strings.Contains(joined, `› 继续处理`) {
		t.Fatalf("settled rows = %q, want previous TASK step settled before new reasoning", joined)
	}
}

func TestGatewayTaskHandoffBudgetKeepsSummaryAndNewStreamVisible(t *testing.T) {
	model := newGatewayEventTestModel()
	block := NewMainACPTurnBlock("root-session")
	block.Events = append(block.Events,
		SubagentEvent{
			Kind: SEReasoning,
			Text: strings.Repeat("I am coordinating child work before continuing.\n", 30),
		},
		SubagentEvent{Kind: SEToolCall, CallID: "task-0", Name: "TASK", Args: "write Alice"},
		SubagentEvent{Kind: SEToolCall, CallID: "task-1", Name: "TASK", Args: "wait ella 5s"},
	)
	ctx := BlockRenderContext{Width: 110, Height: 12, TermWidth: 110, Theme: model.theme}
	live := strings.Join(renderedPlainRows(block.Render(ctx)), "\n")
	if !strings.Contains(live, "I am coordinating child work before continuing.") || !strings.Contains(live, "• Tasks") {
		t.Fatalf("live rows = %q, want live reasoning followed by task controls", live)
	}

	block.Events = append(block.Events, SubagentEvent{Kind: SEReasoning, Text: "继续处理"})
	settledPlain := renderedPlainRows(block.Render(ctx))
	maxBudget := maxInt(1, ctx.Height/2)
	trailing := countTrailingBlankRows(settledPlain)
	if trailing == 0 || trailing > maxBudget {
		t.Fatalf("settled trailing budget rows = %d, want 1..%d; rows = %#v", trailing, maxBudget, settledPlain)
	}
	tail := strings.Join(tailPlainRows(settledPlain, ctx.Height), "\n")
	if !strings.Contains(tail, "• Tasks") || !strings.Contains(tail, "继续处理") {
		t.Fatalf("visible tail = %q, want task summary and new stream visible", tail)
	}
}

func TestGatewayTaskControlsRenderActionDetailsWithoutTaskIDs(t *testing.T) {
	model := newGatewayEventTestModel()
	longInput := "line one\nline two\nline three with TASK_WRITE_TAIL_MARKER"
	for _, item := range []struct {
		callID string
		raw    map[string]any
	}{
		{
			callID: "task-write",
			raw: map[string]any{
				"action":  "write",
				"task_id": "task-hidden-write",
				"input":   longInput,
			},
		},
		{
			callID: "task-wait",
			raw: map[string]any{
				"action":        "wait",
				"task_id":       "task-hidden-wait",
				"yield_time_ms": 500,
			},
		},
		{
			callID: "task-cancel",
			raw: map[string]any{
				"action":  "cancel",
				"task_id": "task-hidden-cancel",
			},
		},
	} {
		updated, _ := model.Update(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindToolCall,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				ToolCall: &kernel.ToolCallPayload{
					CallID:   item.callID,
					ToolName: "TASK",
					Status:   kernel.ToolStatusRunning,
					Scope:    kernel.EventScopeMain,
					RawInput: item.raw,
				},
			},
		})
		model = updated.(*Model)
	}
	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme})
	joined := strings.Join(renderedPlainRows(rows), "\n")
	for _, want := range []string{
		`Write "line one\nline two\nline three with TASK_WRITE_TAIL_MARKER"`,
		"Wait 500ms",
		"Cancel",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered rows = %q, want %q", joined, want)
		}
	}
	for _, forbidden := range []string{"task-hidden-write", "task-hidden-wait", "task-hidden-cancel", "..."} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("rendered rows = %q, should not contain %q", joined, forbidden)
		}
	}
}

func TestAutomaticApprovalReviewUsesHintAndInlineTranscriptLocation(t *testing.T) {
	model := newGatewayEventTestModel()
	permissionInput := map[string]any{
		"reason": "need directory access",
		"permissions": map[string]any{
			"file_system": map[string]any{
				"read":  []any{"/tmp/outside"},
				"write": []any{"/tmp/outside"},
			},
		},
	}

	updated, _ := model.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "perm-1",
				ToolName: "request_permissions",
				Status:   kernel.ToolStatusRunning,
				RawInput: permissionInput,
			},
		},
	})
	model = updated.(*Model)
	updated, _ = model.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindApprovalReview,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session"},
			ApprovalPayload: &kernel.ApprovalPayload{
				ToolCallID:     "perm-1",
				ToolName:       "request_permissions",
				RawInput:       map[string]any{"reason": "need directory access"},
				ReviewStatus:   kernel.ApprovalReviewStatusInProgress,
				DecisionSource: "auto-review",
			},
		},
	})
	model = updated.(*Model)
	if got := ansi.Strip(model.buildHintText()); !strings.Contains(got, "Reviewing approval request: request_permissions") {
		t.Fatalf("approval hint = %q, want pending review hint", got)
	}

	reviewText := "Automatic approval review approved (risk: medium, authorization: high): user requested it."
	updated, _ = model.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:    "perm-1",
				ToolName:  "request_permissions",
				Status:    kernel.ToolStatusCompleted,
				RawInput:  permissionInput,
				RawOutput: map[string]any{"approved": true, "granted": permissionInput["permissions"]},
			},
		},
	})
	model = updated.(*Model)
	updated, _ = model.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:  kernel.NarrativeRoleAssistant,
				Text:  "approval-dependent work finished",
				Final: true,
			},
		},
	})
	model = updated.(*Model)
	updated, _ = model.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindApprovalReview,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session"},
			ApprovalPayload: &kernel.ApprovalPayload{
				ToolCallID:     "perm-1",
				ToolName:       "request_permissions",
				RawInput:       map[string]any{"reason": "need directory access"},
				ReviewStatus:   kernel.ApprovalReviewStatusApproved,
				DecisionSource: "auto-review",
				ReviewText:     reviewText,
				Risk:           "medium",
				Authorization:  "high",
			},
		},
	})
	model = updated.(*Model)
	if got := ansi.Strip(model.buildHintText()); strings.Contains(got, "Reviewing approval request") {
		t.Fatalf("approval hint = %q, want cleared pending review hint", got)
	}

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme})
	plain := strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(plain, "▸ Request permissions write /tmp/outside; read /tmp/outside") {
		t.Fatalf("rendered rows = %q, want request_permissions standard header", plain)
	}
	if !strings.Contains(plain, "• Automatic approval review approved (risk: medium, authorization: high)") {
		t.Fatalf("rendered rows = %q, want compact approval review header", plain)
	}
	if !strings.Contains(plain, "  └ user requested it.") {
		t.Fatalf("rendered rows = %q, want compact approval review rationale", plain)
	}
	if strings.Contains(plain, "⚠") {
		t.Fatalf("rendered rows = %q, should not use warning prefix for approval review", plain)
	}
	toolIdx := strings.Index(plain, "▸ Request permissions write /tmp/outside; read /tmp/outside")
	reviewIdx := strings.Index(plain, "• Automatic approval review approved")
	assistantIdx := strings.Index(plain, "approval-dependent work finished")
	if toolIdx < 0 || reviewIdx < 0 || assistantIdx < 0 || toolIdx >= reviewIdx || reviewIdx >= assistantIdx {
		t.Fatalf("rendered rows = %q, want approval review next to tool before later assistant text", plain)
	}
	if len(block.Events) < 3 || block.Events[0].Kind != SEToolCall || block.Events[0].CallID != "perm-1" || block.Events[1].Kind != SEApproval || block.Events[1].CallID != "perm-1" || block.Events[2].Kind != SEAssistant {
		t.Fatalf("events = %#v, want tool then matching approval then later assistant", block.Events)
	}
	if block.Events[1].ApprovalRisk != "medium" || block.Events[1].ApprovalAuth != "high" {
		t.Fatalf("approval event metadata = (%q, %q), want medium/high", block.Events[1].ApprovalRisk, block.Events[1].ApprovalAuth)
	}
	ctx := BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme}
	styledLines, plainLines, _ := model.wrapRenderedRowsForViewport(block, rows, ctx.Width, ctx)
	reviewLine := ""
	for i, line := range plainLines {
		if strings.Contains(line, "• Automatic approval review approved") {
			reviewLine = styledLines[i]
			break
		}
	}
	if reviewLine == "" {
		t.Fatalf("viewport rows = %#v, want approval review line", plainLines)
	}
	for label, want := range map[string]string{
		"approved": approvalReviewStatusStyle(ctx, "approved").Render("approved"),
		"medium":   approvalReviewValueStyle(ctx, "medium").Render("medium"),
		"high":     approvalReviewValueStyle(ctx, "high").Render("high"),
	} {
		if !strings.Contains(reviewLine, want) {
			t.Fatalf("approval review viewport styling missing %s token:\n line: %q\n want token: %q", label, reviewLine, want)
		}
	}
	for _, forbidden := range []string{`"approved":true`, `"granted"`, "Automatic approval review pending"} {
		if strings.Contains(plain, forbidden) {
			t.Fatalf("rendered rows = %q, should not contain %q", plain, forbidden)
		}
	}
}

func TestDeniedAutomaticApprovalReviewRendersInline(t *testing.T) {
	model := newGatewayEventTestModel()
	permissionInput := map[string]any{
		"permissions": map[string]any{
			"file_system": map[string]any{"write": []string{"/tmp/outside"}},
		},
	}
	for _, env := range []kernel.EventEnvelope{
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "perm-denied",
				ToolName: "request_permissions",
				Status:   kernel.ToolStatusRunning,
				RawInput: permissionInput,
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:    "perm-denied",
				ToolName:  "request_permissions",
				Status:    kernel.ToolStatusFailed,
				Error:     true,
				RawInput:  permissionInput,
				RawOutput: map[string]any{"approved": false, "error": "permission request was rejected"},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:  kernel.NarrativeRoleAssistant,
				Text:  "trying a safer path",
				Final: true,
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindApprovalReview,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session"},
			ApprovalPayload: &kernel.ApprovalPayload{
				ToolCallID:     "perm-denied",
				ToolName:       "request_permissions",
				RawInput:       map[string]any{"reason": "need broad access"},
				ReviewStatus:   kernel.ApprovalReviewStatusDenied,
				DecisionSource: "auto-review",
				ReviewText:     "Automatic approval review denied (risk: high, authorization: low): not narrow enough",
				Risk:           "high",
				Authorization:  "low",
			},
		}},
	} {
		updated, _ := model.Update(env)
		model = updated.(*Model)
	}

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme})
	plain := strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(plain, "• Automatic approval review denied (risk: high, authorization: low)") {
		t.Fatalf("rendered rows = %q, want compact denied approval review header", plain)
	}
	if !strings.Contains(plain, "  └ not narrow enough") {
		t.Fatalf("rendered rows = %q, want compact denied approval rationale", plain)
	}
	if strings.Contains(plain, "⚠") {
		t.Fatalf("rendered rows = %q, should not use warning prefix for denied review", plain)
	}
	toolIdx := strings.Index(plain, "Request permissions write /tmp/outside")
	reviewIdx := strings.Index(plain, "• Automatic approval review denied")
	assistantIdx := strings.Index(plain, "trying a safer path")
	if toolIdx < 0 || reviewIdx < 0 || assistantIdx < 0 || toolIdx >= reviewIdx || reviewIdx >= assistantIdx {
		t.Fatalf("rendered rows = %q, want denied review between tool and later assistant text", plain)
	}
	if len(block.Events) < 3 || block.Events[0].Kind != SEToolCall || block.Events[1].Kind != SEApproval || block.Events[1].ApprovalRisk != "high" || block.Events[1].ApprovalAuth != "low" || block.Events[2].Kind != SEAssistant {
		t.Fatalf("events = %#v, want tool, denied approval metadata, assistant", block.Events)
	}
}

func TestTaskControlFallbackHidesRawToolAndInternalTaskIDs(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"TASK wait task-4":       "Wait",
		"TASK wait leo":          "Wait leo",
		"TASK wait leo 10s":      "Wait leo 10s",
		"TASK wait task-4 500ms": "Wait 500ms",
		"TASK cancel task-4":     "Cancel",
	}
	for input, want := range cases {
		if got := toolDisplayArgs("TASK", nil, input); got != want {
			t.Fatalf("toolDisplayArgs(TASK, %q) = %q, want %q", input, got, want)
		}
	}
}

func TestGatewayTaskStageCleansRawTaskFallbackRows(t *testing.T) {
	model := newGatewayEventTestModel()
	for _, item := range []struct {
		callID string
		input  map[string]any
	}{
		{callID: "task-raw-1", input: map[string]any{"action": "wait", "yield_time_ms": 5000}},
		{callID: "task-raw-2", input: map[string]any{"action": "wait", "task_id": "nora", "yield_time_ms": 3000}},
		{callID: "task-raw-3", input: map[string]any{"action": "wait", "task_id": "nora", "yield_time_ms": 3000}},
		{callID: "task-raw-4", input: map[string]any{"action": "wait", "task_id": "nora", "yield_time_ms": 3000}},
	} {
		updated, _ := model.Update(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindToolCall,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				ToolCall: &kernel.ToolCallPayload{
					CallID:   item.callID,
					ToolName: "TASK",
					Status:   kernel.ToolStatusRunning,
					Scope:    kernel.EventScopeMain,
					RawInput: item.input,
				},
			},
		})
		model = updated.(*Model)
	}
	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme})
	joined := strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, "• Tasks") || !strings.Contains(joined, "  └ Wait 5s") || !strings.Contains(joined, "    Wait nora 3s") {
		t.Fatalf("rendered rows = %q, want cleaned task action rows", joined)
	}
	for _, forbidden := range []string{"TASK wait", "task-12"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("rendered rows = %q, should not contain %q", joined, forbidden)
		}
	}
	if got := strings.Count(joined, "Wait nora 3s"); got != 3 {
		t.Fatalf("rendered rows = %q, Wait nora 3s count = %d, want 3", joined, got)
	}
}

func TestGatewayTaskSnapshotRefreshesBashPanelOutput(t *testing.T) {
	model := newGatewayEventTestModel()
	for _, env := range []kernel.EventEnvelope{
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": "for i in $(seq 1 30); do echo $i; sleep 1; done"},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": "for i in $(seq 1 30); do echo $i; sleep 1; done"},
				RawOutput: map[string]any{
					"running":        true,
					"state":          "running",
					"task_id":        "task-7",
					"output_preview": "进度: 1/30\n",
				},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "task-wait-1",
				ToolName: "TASK",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"action": "wait", "task_id": "task-7", "yield_time_ms": 5000},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "task-wait-1",
				ToolName: "TASK",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"action": "wait", "task_id": "task-7", "yield_time_ms": 5000},
				RawOutput: map[string]any{
					"running":        true,
					"state":          "running",
					"task_id":        "task-7",
					"output_preview": "进度: 1/30\n进度: 2/30\n进度: 3/30\n",
				},
			},
		}},
	} {
		updated, _ := model.Update(env)
		model = updated.(*Model)
	}
	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	block.setToolPanelExpanded("bash-1", true)
	rows := block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})
	plain := make([]string, 0, len(rows))
	for _, row := range rows {
		plain = append(plain, row.Plain)
	}
	joined := strings.Join(plain, "\n")
	for _, want := range []string{"  └ 进度: 1/30", "    进度: 3/30", "• Tasks", "  └ Wait 5s"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered rows = %q, want %q", joined, want)
		}
	}
	for _, forbidden := range []string{"|_", "BASH output", "│", "task / running", "state running", "stdout 进度", "task-7"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("rendered rows = %q, should not contain %q", joined, forbidden)
		}
	}
}

func TestGatewayTerminalToolArgumentsRenderFullAndWrapIndented(t *testing.T) {
	model := NewModel(Config{NoColor: true})
	model.viewport.SetWidth(46)
	model.viewport.SetHeight(20)
	command := "printf '%s\\n' BRANCH && git branch --show-current && printf '%s\\n' TRACKED && echo TERMINAL_ARG_TAIL_MARKER"
	updated, _ := model.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "bash-full-args",
				ToolName: "BASH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": command},
			},
		},
	})
	model = updated.(*Model)
	model.syncViewportContent()

	joined := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(joined, "TERMINAL_ARG_TAIL_MARKER") {
		t.Fatalf("viewport lines = %#v, want full BASH command tail", model.viewportPlainLines)
	}
	if strings.Contains(joined, "echo ...") || strings.Contains(joined, "TERMINAL_ARG_TAIL...") {
		t.Fatalf("viewport lines = %#v, command was truncated", model.viewportPlainLines)
	}
	headerIdx := indexPlainLineContaining(model.viewportPlainLines, "• Ran ")
	tailIdx := indexPlainLineContaining(model.viewportPlainLines, "TERMINAL_ARG_TAIL_MARKER")
	if headerIdx < 0 || tailIdx <= headerIdx {
		t.Fatalf("viewport lines = %#v, want wrapped BASH header", model.viewportPlainLines)
	}
	if !strings.HasPrefix(model.viewportPlainLines[tailIdx], "  │ ") {
		t.Fatalf("wrapped tail line = %q, want terminal continuation rail", model.viewportPlainLines[tailIdx])
	}
}

func TestGatewaySpawnArgumentsRenderPromptPreviewAndExpandsFullPrompt(t *testing.T) {
	model := newGatewayEventTestModel()
	prompt := strings.Repeat("写一个完整参数展示测试。", 8) + "SPAWN_PROMPT_TAIL_MARKER"
	updated, _ := model.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "spawn-full-args",
				ToolName: "SPAWN",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{
					"agent":  "self",
					"prompt": prompt,
				},
			},
		},
	})
	model = updated.(*Model)
	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 180, TermWidth: 180, Theme: model.theme})
	joined := strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, "SPAWN_PROMPT_TAIL_MARKER") || !strings.Contains(joined, "...") {
		t.Fatalf("rendered rows = %q, want abbreviated SPAWN prompt with tail marker", joined)
	}
	if !strings.Contains(joined, "• Spawned self:") || strings.Contains(joined, "• Spawned SPAWN") {
		t.Fatalf("rendered rows = %q, want target agent after Spawned", joined)
	}
	if strings.Contains(joined, `"agent":"self"`) || strings.Contains(joined, `"prompt"`) {
		t.Fatalf("rendered rows = %q, should not show raw SPAWN JSON", joined)
	}
	if !strings.Contains(joined, "(wait subagent output)") {
		t.Fatalf("rendered rows = %q, want running SPAWN placeholder", joined)
	}
	if !model.tryToggleACPToolPanelToken(block.BlockID(), "acp_tool_panel:spawn-full-args") {
		t.Fatal("expected SPAWN header click to expand full prompt")
	}
	rows = block.Render(BlockRenderContext{Width: 220, TermWidth: 220, Theme: model.theme})
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, prompt) {
		t.Fatalf("expanded rows = %q, want full SPAWN prompt", joined)
	}
}

func TestGatewaySpawnFinalResultReplacesRunningStreamAndCleansMarkdown(t *testing.T) {
	model := newGatewayEventTestModel()
	prompt := "在当前目录创建 hello_from_spawn.txt"
	finalText := strings.Join([]string{
		"### 已完成",
		"---",
		"- ✅ 创建 `hello_from_spawn.txt`",
		"**内容：** `Hello from SPAWN child agent!`",
		"| 文件 | 状态 |",
		"| --- | --- |",
		"| `hello_from_spawn.txt` | **created** |",
		"报告位于 `spawn_report.md`",
	}, "\n")
	for _, env := range []kernel.EventEnvelope{
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "spawn-clean-final",
				ToolName: "SPAWN",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": prompt},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "spawn-clean-final",
				ToolName: "SPAWN",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": prompt},
				RawOutput: map[string]any{
					"running": true,
					"state":   "running",
					"task_id": "jack",
					"text":    "dirty process line\nls output that should not become final",
				},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "spawn-clean-final",
				ToolName: "SPAWN",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": prompt},
				RawOutput: map[string]any{
					"running":       false,
					"state":         "completed",
					"task_id":       "jack",
					"result":        "dirty result that should not become final",
					"final_message": finalText,
				},
			},
		}},
	} {
		updated, _ := model.Update(env)
		model = updated.(*Model)
	}
	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 160, TermWidth: 160, Theme: model.theme})
	joined := strings.Join(renderedPlainRows(rows), "\n")
	for _, want := range []string{"• Spawned jack[self]:", "已完成", "✅ 创建 hello_from_spawn.txt", "... +2 lines", "hello_from_spawn.txt  created", "报告位于 spawn_report.md"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered rows missing %q:\n%s", want, joined)
		}
	}
	for _, forbidden := range []string{"dirty process line", "dirty result", "ls output", "###", "**", "`", "| --- |"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("rendered rows should not contain %q:\n%s", forbidden, joined)
		}
	}
	if !model.tryToggleACPToolPanelToken(block.BlockID(), "acp_tool_panel:spawn-clean-final") {
		t.Fatal("expected SPAWN panel token to expand full cleaned result")
	}
	rows = block.Render(BlockRenderContext{Width: 160, TermWidth: 160, Theme: model.theme})
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, "文件  状态") {
		t.Fatalf("expanded rows missing cleaned table header:\n%s", joined)
	}
}

func TestGatewaySpawnRunningSnapshotUpgradesPromptAndHidesRawJSON(t *testing.T) {
	model := newGatewayEventTestModel()
	prompt := "创建一个 Python 脚本并运行"
	for _, env := range []kernel.EventEnvelope{
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "spawn-running-json",
				ToolName: "SPAWN",
				ToolKind: "execute",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"agent": "claude"},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "spawn-running-json",
				ToolName: "SPAWN",
				ToolKind: "execute",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"agent": "claude"},
				RawOutput: map[string]any{
					"agent":       "claude",
					"prompt":      prompt,
					"running":     true,
					"state":       "running",
					"tool_output": `{"agent":"claude","prompt":"创建一个 Python 脚本并运行","running":true}`,
				},
			},
		}},
	} {
		updated, _ := model.Update(env)
		model = updated.(*Model)
	}

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 160, TermWidth: 160, Theme: model.theme})
	joined := strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, "• Spawned claude: "+prompt) {
		t.Fatalf("rendered rows missing upgraded SPAWN prompt:\n%s", joined)
	}
	for _, forbidden := range []string{`{"agent"`, `"prompt"`, `"running"`} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("running SPAWN rows should not expose raw JSON %q:\n%s", forbidden, joined)
		}
	}
}

func TestSpawnFinalMessageJSONAnswerRemainsVisible(t *testing.T) {
	t.Parallel()

	for _, answer := range []string{`{"result":"ok"}`, `{"state":"done"}`} {
		got := toolDisplayOutput("SPAWN", nil, map[string]any{"result": "stale", "final_message": answer}, "", string(kernel.ToolStatusCompleted), false)
		if got != answer {
			t.Fatalf("toolDisplayOutput(SPAWN JSON final %q) = %q, want original JSON", answer, got)
		}
	}
}

func TestSpawnFinalLegacyResultFallbackRemainsVisible(t *testing.T) {
	t.Parallel()

	got := toolDisplayOutput("SPAWN", nil, map[string]any{
		"result": "### Legacy summary\n- `done`",
	}, "", string(kernel.ToolStatusCompleted), false)
	want := "Legacy summary\ndone"
	if got != want {
		t.Fatalf("toolDisplayOutput(SPAWN legacy result) = %q, want %q", got, want)
	}
}

func TestGatewaySpawnRunningStreamPreservesChunkBoundarySpaces(t *testing.T) {
	if got := toolDisplayOutput("SPAWN", nil, map[string]any{"text": " let"}, "", string(kernel.ToolStatusRunning), false); got != " let" {
		t.Fatalf("running SPAWN chunk = %q, want leading space preserved", got)
	}

	model := newGatewayEventTestModel()
	prompt := "写分析报告"
	start := kernel.EventEnvelope{Event: kernel.Event{
		Kind:       kernel.EventKindToolCall,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		ToolCall: &kernel.ToolCallPayload{
			CallID:   "spawn-space-stream",
			ToolName: "SPAWN",
			Status:   kernel.ToolStatusRunning,
			Scope:    kernel.EventScopeMain,
			RawInput: map[string]any{"agent": "self", "prompt": prompt},
		},
	}}
	updated, _ := model.Update(start)
	model = updated.(*Model)

	req := kernel.StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "spawn-space-stream",
		ToolName:   "SPAWN",
		RawInput:   map[string]any{"agent": "self", "prompt": prompt},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "child-task"},
		Scope:      kernel.EventScopeMain,
	}
	for _, chunk := range []string{"Now", " let", " me", " write", " the", " report."} {
		for _, env := range kernel.StreamFrameEvents(req, stream.Frame{
			Ref:     stream.Ref{SessionID: "root-session", TaskID: "child-task"},
			Stream:  "stdout",
			Text:    chunk,
			Running: true,
		}) {
			updated, _ = model.Update(env)
			model = updated.(*Model)
		}
	}

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one SPAWN event", block.Events)
	}
	if got, want := block.Events[0].Output, "Now let me write the report."; got != want {
		t.Fatalf("SPAWN stream output = %q, want %q", got, want)
	}
	rows := block.Render(BlockRenderContext{Width: 160, TermWidth: 160, Theme: model.theme})
	joined := strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, "Now let me write the report.") || strings.Contains(joined, "Nowletmewrite") {
		t.Fatalf("rendered rows lost chunk spaces:\n%s", joined)
	}
}

func TestGatewaySpawnClosedStreamReplacesRunningOutputWithoutTaskWait(t *testing.T) {
	model := newGatewayEventTestModel()
	prompt := "分析当前目录"
	start := kernel.EventEnvelope{Event: kernel.Event{
		Kind:       kernel.EventKindToolCall,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		ToolCall: &kernel.ToolCallPayload{
			CallID:   "spawn-stream-final",
			ToolName: "SPAWN",
			Status:   kernel.ToolStatusRunning,
			Scope:    kernel.EventScopeMain,
			RawInput: map[string]any{"agent": "self", "prompt": prompt},
		},
	}}
	updated, _ := model.Update(start)
	model = updated.(*Model)

	req := kernel.StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "spawn-stream-final",
		ToolName:   "SPAWN",
		RawInput:   map[string]any{"agent": "self", "prompt": prompt},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "liam"},
		Scope:      kernel.EventScopeMain,
	}
	for _, env := range kernel.StreamFrameEvents(req, stream.Frame{
		Ref:     stream.Ref{SessionID: "root-session", TaskID: "internal-task"},
		Stream:  "stdout",
		Text:    "ool_demo_showcase*.md(x6版本迭代)|**总文件数**|~80+|",
		Running: true,
	}) {
		updated, _ = model.Update(env)
		model = updated.(*Model)
	}
	for _, env := range kernel.StreamFrameEvents(req, stream.Frame{
		Ref:     stream.Ref{SessionID: "root-session", TaskID: "internal-task"},
		Closed:  true,
		Running: false,
		State:   "completed",
		Result: map[string]any{
			"result":        "dirty result that should not become final",
			"final_message": "### 摘要\n- `ool_demo_showcase.md` 存在\n**结论：** 目录用于 SPAWN 演示",
		},
	}) {
		updated, _ = model.Update(env)
		model = updated.(*Model)
	}

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 160, TermWidth: 160, Theme: model.theme})
	joined := strings.Join(renderedPlainRows(rows), "\n")
	for _, want := range []string{"• Spawned liam[self]:", "摘要", "ool_demo_showcase.md 存在", "结论： 目录用于 SPAWN 演示"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered rows missing %q:\n%s", want, joined)
		}
	}
	for _, forbidden := range []string{"总文件数", "###", "`", "**"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("rendered rows should not contain %q:\n%s", forbidden, joined)
		}
	}
}

func TestGatewayTaskWriteRendersOwnPanelAndAbsorbsContinuationSpawn(t *testing.T) {
	model := newGatewayEventTestModel()
	for _, env := range []kernel.EventEnvelope{
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "spawn-continue",
				ToolName: "SPAWN",
				ToolKind: "execute",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": "创建文件"},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "spawn-continue",
				ToolName: "SPAWN",
				ToolKind: "execute",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": "创建文件"},
				RawOutput: map[string]any{
					"running":       false,
					"state":         "completed",
					"task_id":       "jack",
					"final_message": "old final answer",
				},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "task-wait-before-write",
				ToolName: "TASK",
				ToolKind: "execute",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"action": "wait", "task_id": "jack", "yield_time_ms": 5000},
				RawOutput: map[string]any{
					"action":      "wait",
					"running":     false,
					"state":       "completed",
					"task_id":     "jack",
					"target_kind": "subagent",
					"result":      "old final answer",
				},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "task-write-continue",
				ToolName: "TASK",
				ToolKind: "execute",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"action": "write", "task_id": "jack", "input": "检查刚才创建的文件"},
				RawOutput: map[string]any{
					"action":         "write",
					"running":        true,
					"state":          "running",
					"task_id":        "jack",
					"target_kind":    "subagent",
					"output_preview": "正在读取 hello_from_spawn.txt",
				},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "spawn-continued-child",
				ToolName: "SPAWN",
				ToolKind: "execute",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": "检查刚才创建的文件"},
				RawOutput: map[string]any{
					"running": true,
					"state":   "running",
					"task_id": "jack",
					"text":    "正在读取 hello_from_spawn.txt",
				},
			},
		}},
	} {
		updated, _ := model.Update(env)
		model = updated.(*Model)
	}
	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 160, TermWidth: 160, Theme: model.theme})
	joined := strings.Join(renderedPlainRows(rows), "\n")
	for _, want := range []string{"• Spawned jack[self]: 创建文件", "old final answer", "• Tasks", "Wait jack", "• Write jack: 检查刚才创建的文件", "正在读取 hello_from_spawn.txt"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("running continuation rows missing %q:\n%s", want, joined)
		}
	}
	if strings.Count(joined, "• Spawned") != 1 || strings.Contains(joined, "  └ Write jack") {
		t.Fatalf("TASK write should render separately without a second SPAWN or grouped Write row:\n%s", joined)
	}

	updated, _ := model.Update(kernel.EventEnvelope{Event: kernel.Event{
		Kind:       kernel.EventKindToolResult,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		ToolResult: &kernel.ToolResultPayload{
			CallID:   "spawn-continued-child",
			ToolName: "SPAWN",
			ToolKind: "execute",
			Status:   kernel.ToolStatusCompleted,
			Scope:    kernel.EventScopeMain,
			RawInput: map[string]any{"agent": "self", "prompt": "检查刚才创建的文件"},
			RawOutput: map[string]any{
				"running":       false,
				"state":         "completed",
				"task_id":       "jack",
				"final_message": "### 检查完成\n- `hello_from_spawn.txt` 内容正确",
			},
		},
	}})
	model = updated.(*Model)
	rows = block.Render(BlockRenderContext{Width: 160, TermWidth: 160, Theme: model.theme})
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, "• Write jack: 检查刚才创建的文件") || !strings.Contains(joined, "检查完成") || !strings.Contains(joined, "hello_from_spawn.txt 内容正确") {
		t.Fatalf("completed continuation rows missing cleaned final result:\n%s", joined)
	}
	if strings.Contains(joined, "正在读取 hello_from_spawn.txt") || strings.Contains(joined, "###") || strings.Contains(joined, "`") {
		t.Fatalf("completed continuation should replace running stream with cleaned final:\n%s", joined)
	}
	if strings.Count(joined, "• Spawned") != 1 || strings.Contains(joined, "  └ Write jack") {
		t.Fatalf("completed continuation should keep one original SPAWN and direct Write panel:\n%s", joined)
	}
	spawnCount := 0
	writeCount := 0
	for _, ev := range block.Events {
		if ev.Kind == SEToolCall && strings.EqualFold(ev.Name, "SPAWN") {
			spawnCount++
		}
		if ev.Kind == SEToolCall && strings.EqualFold(ev.Name, "TASK") && taskEventAction(ev) == "write" {
			writeCount++
		}
	}
	if spawnCount != 1 || writeCount != 1 {
		t.Fatalf("block events = %#v, want one original SPAWN and one TASK write panel", block.Events)
	}
}

func TestGatewayBashTerminalDeltasPreserveLineBreaks(t *testing.T) {
	model := newGatewayEventTestModel()
	for _, env := range []kernel.EventEnvelope{
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": "for i in $(seq 1 10); do echo $i; done"},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": "for i in $(seq 1 10); do echo $i; done"},
				RawOutput: map[string]any{
					"running": true,
					"state":   "running",
					"task_id": "task-7",
					"stream":  "stdout",
					"text":    "[步骤 8/10] 正在处理... 09:05:53\n",
				},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": "for i in $(seq 1 10); do echo $i; done"},
				RawOutput: map[string]any{
					"running": true,
					"state":   "running",
					"task_id": "task-7",
					"stream":  "stdout",
					"text":    "[步骤 9/10] 正在处理... 09:05:55\n",
				},
			},
		}},
	} {
		updated, _ := model.Update(env)
		model = updated.(*Model)
	}
	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	block.setToolPanelExpanded("bash-1", true)
	rows := block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})
	plain := make([]string, 0, len(rows))
	for _, row := range rows {
		plain = append(plain, row.Plain)
	}
	joined := strings.Join(plain, "\n")
	if strings.Contains(joined, "09:05:53 [步骤 9/10]") {
		t.Fatalf("rendered rows = %q, terminal delta lines were merged", joined)
	}
	for _, want := range []string{"  └ [步骤 8/10] 正在处理... 09:05:53", "    [步骤 9/10] 正在处理... 09:05:55"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered rows = %q, want %q", joined, want)
		}
	}
}

func TestGatewayPlanToolRendersOnlyPlanEntries(t *testing.T) {
	model := newGatewayEventTestModel()
	rawInput := map[string]any{
		"entries": []any{
			map[string]any{"content": "Inspect files", "status": "completed"},
			map[string]any{"content": "Run validation", "status": "in_progress"},
		},
	}
	rawOutput := map[string]any{
		"message": "Plan updated",
		"entries": rawInput["entries"],
	}
	for _, env := range []kernel.EventEnvelope{
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "plan-1",
				ToolName: "PLAN",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: rawInput,
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:    "plan-1",
				ToolName:  "PLAN",
				Status:    kernel.ToolStatusCompleted,
				Scope:     kernel.EventScopeMain,
				RawInput:  rawInput,
				RawOutput: rawOutput,
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindPlanUpdate,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Plan: &kernel.PlanPayload{Entries: []kernel.PlanEntryPayload{
				{Content: "Inspect files", Status: "completed"},
				{Content: "Run validation", Status: "in_progress"},
			}},
		}},
	} {
		updated, _ := model.Update(env)
		model = updated.(*Model)
	}
	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})
	plain := make([]string, 0, len(rows))
	for _, row := range rows {
		plain = append(plain, row.Plain)
	}
	joined := strings.Join(plain, "\n")
	for _, want := range []string{"• Updated Plan", "  └ ✔ Inspect files", "    □ Run validation"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered rows = %q, want %q", joined, want)
		}
	}
	if len(plain) < 5 || plain[0] != "" || plain[len(plain)-1] != "" {
		t.Fatalf("rendered rows = %#v, want blank lines around plan block", plain)
	}
	for _, forbidden := range []string{"PLAN", `"entries"`, "Plan updated"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("rendered rows = %q, should not contain %q", joined, forbidden)
		}
	}
}

func TestGatewayBashPanelRendersRawTerminalOutput(t *testing.T) {
	tests := []struct {
		name      string
		status    kernel.ToolStatus
		isErr     bool
		rawOutput map[string]any
		want      []string
		forbid    []string
	}{
		{
			name:   "running preview",
			status: kernel.ToolStatusRunning,
			rawOutput: map[string]any{
				"running":        true,
				"state":          "running",
				"task_id":        "task-7",
				"supports_input": true,
				"output_preview": "进度: 1/5\n",
			},
			want:   []string{"• Ran for i in 1 2", "  └ 进度: 1/5"},
			forbid: []string{"|_", "BASH output", "│", "task / running", "task task-7", "state running", "stdout 进度", "supports_input"},
		},
		{
			name:   "failed stdout stderr",
			status: kernel.ToolStatusFailed,
			isErr:  true,
			rawOutput: map[string]any{
				"stderr":    "permission denied\n",
				"stdout":    "ignored stdout\n",
				"exit_code": 1,
			},
			want:   []string{"  └ ignored stdout", "    stderr:", "    permission denied"},
			forbid: []string{"|_", "BASH output", "│", "stderr permission denied", "exit 1"},
		},
		{
			name:   "failed stdout diagnostics",
			status: kernel.ToolStatusFailed,
			isErr:  true,
			rawOutput: map[string]any{
				"stdout":    "dangerous command is blocked even in auto-review mode\n",
				"stderr":    "",
				"error":     "exit status 1",
				"exit_code": 1,
			},
			want:   []string{"  └ dangerous command is blocked even in auto-review mode"},
			forbid: []string{"exit 1", "exit status 1"},
		},
		{
			name:   "successful empty output",
			status: kernel.ToolStatusCompleted,
			rawOutput: map[string]any{
				"exit_code": 0,
			},
			want:   []string{"  └ (no output)"},
			forbid: []string{"exit 0", "completed"},
		},
		{
			name:   "successful stdout stderr",
			status: kernel.ToolStatusCompleted,
			rawOutput: map[string]any{
				"stdout":    "line one\nline two\n",
				"stderr":    "warning\n",
				"result":    "compact stale result",
				"exit_code": 0,
			},
			want:   []string{"  └ line one", "    line two", "    stderr:", "    warning"},
			forbid: []string{"compact stale result", "exit 0", "no output"},
		},
		{
			name:   "legacy final result fallback",
			status: kernel.ToolStatusCompleted,
			rawOutput: map[string]any{
				"result": "legacy command output\n",
			},
			want:   []string{"  └ legacy command output"},
			forbid: []string{"no output"},
		},
		{
			name:   "legacy final text fallback",
			status: kernel.ToolStatusCompleted,
			rawOutput: map[string]any{
				"text": "legacy text output\n",
			},
			want:   []string{"  └ legacy text output"},
			forbid: []string{"no output"},
		},
		{
			name:   "legacy final error fallback",
			status: kernel.ToolStatusFailed,
			isErr:  true,
			rawOutput: map[string]any{
				"error": "legacy error output",
			},
			want:   []string{"  └ legacy error output"},
			forbid: []string{"no output"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			model := newGatewayEventTestModel()
			callID := "bash-" + strings.ReplaceAll(tt.name, " ", "-")
			updated, _ := model.Update(kernel.EventEnvelope{
				Event: kernel.Event{
					Kind:       kernel.EventKindToolCall,
					SessionRef: session.SessionRef{SessionID: "root-session"},
					ToolCall: &kernel.ToolCallPayload{
						CallID:   callID,
						ToolName: "BASH",
						Status:   kernel.ToolStatusRunning,
						Scope:    kernel.EventScopeMain,
						RawInput: map[string]any{"command": "for i in 1 2; do echo $i; done"},
					},
				},
			})
			model = updated.(*Model)
			updated, _ = model.Update(kernel.EventEnvelope{
				Event: kernel.Event{
					Kind:       kernel.EventKindToolResult,
					SessionRef: session.SessionRef{SessionID: "root-session"},
					ToolResult: &kernel.ToolResultPayload{
						CallID:    callID,
						ToolName:  "BASH",
						Status:    tt.status,
						Error:     tt.isErr,
						Scope:     kernel.EventScopeMain,
						RawInput:  map[string]any{"command": "for i in 1 2; do echo $i; done"},
						RawOutput: tt.rawOutput,
					},
				},
			})
			model = updated.(*Model)
			block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
			if !ok {
				t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
			}
			block.setToolPanelExpanded(callID, true)
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
			for _, forbidden := range tt.forbid {
				if strings.Contains(joined, forbidden) {
					t.Fatalf("rendered rows = %q, should not contain %q", joined, forbidden)
				}
			}
		})
	}
}

func TestGatewayBASHFinalEmptyOutputReplacesStreamedPreview(t *testing.T) {
	model := newGatewayEventTestModel()
	callID := "bash-stream-final-empty"
	for _, env := range []kernel.EventEnvelope{
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   callID,
				ToolName: "BASH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": "git log --oneline -6"},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   callID,
				ToolName: "BASH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": "git log --oneline -6"},
				RawOutput: map[string]any{
					"running":        true,
					"state":          "running",
					"task_id":        "task-7",
					"output_preview": "stale streamed preview\n",
				},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:    callID,
				ToolName:  "BASH",
				Status:    kernel.ToolStatusCompleted,
				Scope:     kernel.EventScopeMain,
				RawInput:  map[string]any{"command": "git log --oneline -6"},
				RawOutput: map[string]any{"exit_code": 0},
			},
		}},
	} {
		updated, _ := model.Update(env)
		model = updated.(*Model)
	}

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	block.setToolPanelExpanded(callID, true)
	joined := strings.Join(renderedPlainRows(block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})), "\n")
	if !strings.Contains(joined, "(no output)") {
		t.Fatalf("rendered rows = %q, want final empty output marker", joined)
	}
	if strings.Contains(joined, "stale streamed preview") {
		t.Fatalf("rendered rows = %q, should replace streamed preview with final output", joined)
	}
}

func TestToolGroupsUseActionColorAndBlankSeparation(t *testing.T) {
	model := newGatewayEventTestModel()
	ctx := BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme}
	block := NewMainACPTurnBlock("session-1")
	block.UpdateTool("bash-1", "BASH", "echo hi", "hi", false, false)
	block.UpdateTool("bash-1", "BASH", "echo hi", "hi", true, false)
	block.UpdateTool("read-1", "READ", "README.md", "README.md 1~20", false, false)
	block.UpdateTool("read-1", "READ", "README.md", "README.md 1~20", true, false)
	block.UpdateTool("write-1", "WRITE", "out.txt", "+1 -0", false, false)
	block.UpdateTool("write-1", "WRITE", "out.txt", "+1 -0", true, false)

	rows := block.Render(ctx)
	plain := renderedPlainRows(rows)
	ranIdx := indexOfRowContaining(plain, "• Ran echo hi")
	readIdx := indexOfRowContaining(plain, "READ")
	wroteIdx := indexOfRowContaining(plain, "• Wrote")
	if ranIdx < 0 || readIdx < 0 || wroteIdx < 0 {
		t.Fatalf("rendered rows = %#v, want Ran, READ, and Wrote tool groups", plain)
	}
	if !hasBlankRowBetween(plain, ranIdx, readIdx) || !hasBlankRowBetween(plain, readIdx, wroteIdx) {
		t.Fatalf("rendered rows = %#v, want blank rows between tool groups", plain)
	}

	for _, idx := range []int{ranIdx, wroteIdx} {
		if rows[idx].Styled == rows[idx].Plain || !strings.Contains(rows[idx].Styled, "\x1b[") {
			t.Fatalf("styled row %q = %q, want themed action label", rows[idx].Plain, rows[idx].Styled)
		}
	}
}

func TestGatewayAssistantFinalFoldsReasoningAndTogglesInline(t *testing.T) {
	model := newGatewayEventTestModel()

	updated, _ := model.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:          kernel.NarrativeRoleAssistant,
				ReasoningText: "thinking through the plan",
				Final:         false,
				Scope:         kernel.EventScopeMain,
			},
		},
	})
	m := updated.(*Model)
	updated, _ = m.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:          kernel.NarrativeRoleAssistant,
				ReasoningText: "thinking through the plan",
				Text:          "final answer",
				Final:         true,
				Scope:         kernel.EventScopeMain,
			},
		},
	})
	m = updated.(*Model)

	block, ok := m.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", m.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{
		Width:     80,
		TermWidth: 80,
		Theme:     m.theme,
	})
	var plain []string
	for _, row := range rows {
		plain = append(plain, row.Plain)
	}
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, "› thinking through the plan") {
		t.Fatalf("rendered rows = %q, want folded reasoning preview", joined)
	}
	if strings.Contains(joined, "  thinking through the plan") {
		t.Fatalf("rendered rows = %q, should collapse reasoning body by default", joined)
	}
	if !strings.Contains(joined, "final answer") {
		t.Fatalf("rendered rows = %q, want assistant text", joined)
	}
	if !m.tryToggleACPToolPanelToken(block.BlockID(), "acp_reasoning:0") {
		t.Fatal("expected reasoning click token to toggle")
	}
	rows = block.Render(BlockRenderContext{Width: 80, TermWidth: 80, Theme: m.theme})
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, "› thinking through the plan") {
		t.Fatalf("expanded rows = %q, want expanded reasoning preview", joined)
	}
}

func TestGatewayReasoningFoldsAfterAttentionToolLoopAndTogglesInline(t *testing.T) {
	model := newGatewayEventTestModel()
	for _, env := range []kernel.EventEnvelope{
		{Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:          kernel.NarrativeRoleAssistant,
				ReasoningText: "thinking through the command choice",
				Final:         true,
				Scope:         kernel.EventScopeMain,
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:  kernel.NarrativeRoleAssistant,
				Text:  "I will run the test.",
				Final: true,
				Scope: kernel.EventScopeMain,
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": "go test ./surfaces/tui/..."},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": "go test ./surfaces/tui/..."},
				RawOutput: map[string]any{
					"stdout":    "ok github.com/OnslaughtSnail/caelis/surfaces/tui/app\n",
					"exit_code": 0,
				},
			},
		}},
	} {
		updated, _ := model.Update(env)
		model = updated.(*Model)
	}

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})
	plain := renderedPlainRows(rows)
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, "› thinking through the command choice") {
		t.Fatalf("rendered rows = %q, want folded reasoning preview", joined)
	}
	if strings.Contains(joined, "Thought a few seconds") {
		t.Fatalf("rendered rows = %q, should not show reasoning duration", joined)
	}
	reasonIdx := indexOfRowContaining(plain, "› thinking through the command choice")
	bashIdx := indexOfRowContaining(plain, "• Ran go test ./surfaces/tui/...")
	if reasonIdx < 0 || bashIdx < 0 {
		t.Fatalf("rendered rows = %#v, want folded reasoning and attention tool", plain)
	}
	if hasBlankRowBetween(plain, reasonIdx, bashIdx) {
		t.Fatalf("rendered rows = %#v, want folded reasoning attached to attention tool", plain)
	}

	if !model.tryToggleACPToolPanelToken(block.BlockID(), "acp_reasoning:0") {
		t.Fatal("expected reasoning click token to toggle")
	}
	rows = block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, "› thinking through the command choice") {
		t.Fatalf("expanded rows = %q, want expanded reasoning preview", joined)
	}
	if strings.Count(joined, "thinking through the command choice") != 1 {
		t.Fatalf("expanded rows = %q, want single-line reasoning rendered once", joined)
	}
}

func TestGatewayExpandedReasoningReplacesFoldedPreviewInPlace(t *testing.T) {
	model := newGatewayEventTestModel()
	reasoning := "Now let me verify the DDL matches every field in the entity.\nEntity field -> DDL column\nID -> id varchar(64) NOT NULL"
	for _, env := range []kernel.EventEnvelope{
		{Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:          kernel.NarrativeRoleAssistant,
				ReasoningText: reasoning,
				Final:         true,
				Scope:         kernel.EventScopeMain,
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": "go test ./..."},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": "go test ./..."},
				RawOutput: map[string]any{
					"stdout":    "ok\n",
					"exit_code": 0,
				},
			},
		}},
	} {
		updated, _ := model.Update(env)
		model = updated.(*Model)
	}

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	if !model.tryToggleACPToolPanelToken(block.BlockID(), "acp_reasoning:0") {
		t.Fatal("expected reasoning click token to toggle")
	}
	rows := block.Render(BlockRenderContext{Width: 90, TermWidth: 90, Theme: model.theme})
	plain := strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(plain, "› Now let me verify the DDL matches every field in the entity.") {
		t.Fatalf("expanded rows = %q, want first reasoning row to replace folded preview", plain)
	}
	if strings.Contains(plain, "\n· Now let me verify the DDL matches every field in the entity.") {
		t.Fatalf("expanded rows = %q, duplicated folded preview as body first line", plain)
	}
	if strings.Contains(plain, "Nowletme") || strings.Contains(plain, "idvarchar") || strings.Contains(plain, "NOTNULL") {
		t.Fatalf("expanded rows = %q, lost token boundary spaces", plain)
	}
}

func TestConsecutiveReasoningEventsFoldAsOnePreviewBeforeAttentionTool(t *testing.T) {
	model := newGatewayEventTestModel()
	block := NewMainACPTurnBlock("root-session")
	block.Events = append(block.Events,
		SubagentEvent{Kind: SEReasoning, Text: "First I need to inspect the repository. "},
		SubagentEvent{Kind: SEReasoning, Text: "Then I will patch the failing field references."},
		SubagentEvent{Kind: SEToolCall, CallID: "patch-1", Name: "PATCH", Args: "gm_license.go +1 -1", Output: "gm_license.go +1 -1\ndiff / hunk\n@@ -1,1 +1,1 @@\n-old\n+new", Done: true},
	)

	rows := block.Render(BlockRenderContext{Width: 100, TermWidth: 100, Theme: model.theme})
	plain := renderedPlainRows(rows)
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, "› First I need to inspect the repository. Then I will patch the failing field references.") {
		t.Fatalf("rendered rows = %q, want consecutive reasoning folded into one preview", joined)
	}
	if strings.Contains(joined, "\n› First I need") || strings.Contains(joined, "\n› Then I will") {
		t.Fatalf("rendered rows = %q, consecutive reasoning should not remain expanded before PATCH", joined)
	}
	if hasBlankRowBetween(plain, indexOfRowContaining(plain, "› First I need"), indexOfRowContaining(plain, "• Patched gm_license.go")) {
		t.Fatalf("rendered rows = %#v, folded reasoning should attach to PATCH", plain)
	}
}

func TestGatewayReasoningFoldUsesTimedDurationWhenAvailable(t *testing.T) {
	model := newGatewayEventTestModel()
	start := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	for _, env := range []kernel.EventEnvelope{
		{Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			OccurredAt: start,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:          kernel.NarrativeRoleAssistant,
				ReasoningText: "first ",
				Final:         false,
				Scope:         kernel.EventScopeMain,
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			OccurredAt: start.Add(900 * time.Millisecond),
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:          kernel.NarrativeRoleAssistant,
				ReasoningText: "last",
				Final:         false,
				Scope:         kernel.EventScopeMain,
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			OccurredAt: start.Add(1500 * time.Millisecond),
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:  kernel.NarrativeRoleAssistant,
				Text:  "done thinking",
				Final: true,
				Scope: kernel.EventScopeMain,
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			OccurredAt: start.Add(1600 * time.Millisecond),
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": "echo ok"},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			OccurredAt: start.Add(1700 * time.Millisecond),
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   kernel.ToolStatusCompleted,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"command": "echo ok"},
				RawOutput: map[string]any{
					"stdout": "ok\n",
				},
			},
		}},
	} {
		updated, _ := model.Update(env)
		model = updated.(*Model)
	}

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})
	joined := strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, "› first last") {
		t.Fatalf("rendered rows = %q, want folded reasoning preview", joined)
	}
	if strings.Contains(joined, "Thought") || strings.Contains(joined, "1.5s") {
		t.Fatalf("rendered rows = %q, should not show reasoning duration", joined)
	}
}

func TestGatewayStreamingNarrativeKeepsReasoningAnswerBoundaries(t *testing.T) {
	model := newGatewayEventTestModel()

	send := func(payload *kernel.NarrativePayload) *Model {
		updated, _ := model.Update(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative:  payload,
			},
		})
		model = updated.(*Model)
		return model
	}

	send(&kernel.NarrativePayload{
		Role:          kernel.NarrativeRoleAssistant,
		ReasoningText: "think-1 ",
		Final:         false,
		Scope:         kernel.EventScopeMain,
	})
	send(&kernel.NarrativePayload{
		Role:  kernel.NarrativeRoleAssistant,
		Text:  "answer-1 ",
		Final: false,
		Scope: kernel.EventScopeMain,
	})
	send(&kernel.NarrativePayload{
		Role:          kernel.NarrativeRoleAssistant,
		ReasoningText: "think-2 ",
		Final:         false,
		Scope:         kernel.EventScopeMain,
	})
	send(&kernel.NarrativePayload{
		Role:  kernel.NarrativeRoleAssistant,
		Text:  "answer-2",
		Final: false,
		Scope: kernel.EventScopeMain,
	})

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	if got := len(block.Events); got != 2 {
		t.Fatalf("len(block.Events) = %d, want 2 active narrative streams; got %#v", got, block.Events)
	}
	wantKinds := []SubagentEventKind{SEReasoning, SEAssistant}
	wantTexts := []string{"think-1 think-2 ", "answer-1 answer-2"}
	for i := range wantKinds {
		if block.Events[i].Kind != wantKinds[i] || block.Events[i].Text != wantTexts[i] {
			t.Fatalf("block.Events[%d] = %#v, want kind=%v text=%q", i, block.Events[i], wantKinds[i], wantTexts[i])
		}
	}
}

func TestGatewayParticipantStreamingChunksAppendInsteadOfReplace(t *testing.T) {
	model := newGatewayEventTestModel()

	send := func(text string) {
		updated, _ := model.Update(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Origin: &kernel.EventOrigin{
					Scope:         kernel.EventScopeParticipant,
					ScopeID:       "codex-001",
					Actor:         "codex-001",
					ParticipantID: "codex-001",
				},
				Narrative: &kernel.NarrativePayload{
					Role:  kernel.NarrativeRoleAssistant,
					Actor: "codex-001",
					Text:  text,
					Final: false,
					Scope: kernel.EventScopeParticipant,
				},
			},
		})
		model = updated.(*Model)
	}

	send("上海今天")
	send("阴有小雨")
	send("。")

	block, ok := model.doc.Blocks()[0].(*ParticipantTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want ParticipantTurnBlock", model.doc.Blocks()[0])
	}
	if len(block.Events) != 1 || block.Events[0].Kind != SEAssistant {
		t.Fatalf("participant events = %#v, want one assistant stream", block.Events)
	}
	if got := block.Events[0].Text; got != "上海今天阴有小雨。" {
		t.Fatalf("participant assistant text = %q, want appended chunks", got)
	}
}

func TestGatewayParticipantFinalCumulativeMessagePreservesInterleavedTimeline(t *testing.T) {
	model := newGatewayEventTestModel()

	sendAssistant := func(text string, final bool) {
		updated, _ := model.Update(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Origin: &kernel.EventOrigin{
					Scope:         kernel.EventScopeParticipant,
					ScopeID:       "codex-turn-1",
					Actor:         "@codex",
					ParticipantID: "codex-001",
				},
				Narrative: &kernel.NarrativePayload{
					Role:  kernel.NarrativeRoleAssistant,
					Actor: "@codex",
					Text:  text,
					Final: final,
					Scope: kernel.EventScopeParticipant,
				},
			},
		})
		model = updated.(*Model)
	}
	sendTool := func(kind kernel.EventKind, status kernel.ToolStatus) {
		event := kernel.Event{
			Kind:       kind,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin: &kernel.EventOrigin{
				Scope:         kernel.EventScopeParticipant,
				ScopeID:       "codex-turn-1",
				Actor:         "@codex",
				ParticipantID: "codex-001",
			},
		}
		if kind == kernel.EventKindToolCall {
			event.ToolCall = &kernel.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "READ",
				Status:   status,
				Scope:    kernel.EventScopeParticipant,
			}
		} else {
			event.ToolResult = &kernel.ToolResultPayload{
				CallID:   "call-1",
				ToolName: "READ",
				Status:   status,
				Scope:    kernel.EventScopeParticipant,
			}
		}
		updated, _ := model.Update(kernel.EventEnvelope{Event: event})
		model = updated.(*Model)
	}

	sendAssistant("I will inspect first.", false)
	sendTool(kernel.EventKindToolCall, kernel.ToolStatusRunning)
	sendTool(kernel.EventKindToolResult, kernel.ToolStatusCompleted)
	sendAssistant("The final answer is ready.", false)
	sendAssistant("I will inspect first.\n\nThe final answer is ready.", true)

	block, ok := model.doc.Blocks()[0].(*ParticipantTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want ParticipantTurnBlock", model.doc.Blocks()[0])
	}
	wantKinds := []SubagentEventKind{SEAssistant, SEToolCall, SEAssistant}
	if len(block.Events) != len(wantKinds) {
		t.Fatalf("participant events = %#v, want assistant/tool/assistant timeline", block.Events)
	}
	for i, kind := range wantKinds {
		if block.Events[i].Kind != kind {
			t.Fatalf("participant events[%d] = %#v, want kind %v", i, block.Events[i], kind)
		}
	}
	if block.Events[0].Text != "I will inspect first." {
		t.Fatalf("first assistant text = %q, want original first segment", block.Events[0].Text)
	}
	if !block.Events[1].Done {
		t.Fatalf("tool event = %#v, want completed tool preserved in place", block.Events[1])
	}
	if block.Events[2].Text != "The final answer is ready." {
		t.Fatalf("second assistant text = %q, want original second segment", block.Events[2].Text)
	}
}

func TestGatewayParticipantFinalMarkdownWhitespaceReplacesSingleLiveSegment(t *testing.T) {
	model := newGatewayEventTestModel()

	sendAssistant := func(text string, final bool) {
		updated, _ := model.Update(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Origin: &kernel.EventOrigin{
					Scope:         kernel.EventScopeParticipant,
					ScopeID:       "codex-turn-1",
					Actor:         "@codex",
					ParticipantID: "codex-001",
				},
				Narrative: &kernel.NarrativePayload{
					Role:  kernel.NarrativeRoleAssistant,
					Actor: "@codex",
					Text:  text,
					Final: final,
					Scope: kernel.EventScopeParticipant,
				},
			},
		})
		model = updated.(*Model)
	}

	sendAssistant("- a - b", false)
	sendAssistant("- a\n- b", true)

	block, ok := model.doc.Blocks()[0].(*ParticipantTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want ParticipantTurnBlock", model.doc.Blocks()[0])
	}
	if len(block.Events) != 1 || block.Events[0].Kind != SEAssistant {
		t.Fatalf("participant events = %#v, want one assistant event", block.Events)
	}
	if block.Events[0].Text != "- a\n- b" {
		t.Fatalf("assistant final text = %q, want canonical Markdown line break", block.Events[0].Text)
	}
}

func TestGatewayParticipantPromptTurnsRenderAsSeparateBlocks(t *testing.T) {
	model := newGatewayEventTestModel()

	sendUser := func(text string) {
		updated, _ := model.Update(UserMessageMsg{Text: text})
		model = updated.(*Model)
	}
	sendParticipant := func(scopeID string, text string) {
		updated, _ := model.Update(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Origin: &kernel.EventOrigin{
					Scope:   kernel.EventScopeParticipant,
					ScopeID: scopeID,
					Actor:   "@kate",
				},
				Narrative: &kernel.NarrativePayload{
					Role:  kernel.NarrativeRoleAssistant,
					Actor: "codex-001",
					Text:  text,
					Final: false,
					Scope: kernel.EventScopeParticipant,
				},
			},
		})
		model = updated.(*Model)
	}

	sendUser("/codex 查询一下上海今天的天气")
	sendParticipant("task-1:1", "first")
	sendUser("@kate 帮我清理一下/tmp目录")
	sendParticipant("task-1:2", "second")
	updated, _ := model.Update(TaskResultMsg{SuppressTurnDivider: true})
	model = updated.(*Model)

	blocks := model.doc.Blocks()
	var participantBlocks []*ParticipantTurnBlock
	var secondUserIndex = -1
	var secondTurnIndex = -1
	for i, block := range blocks {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.Contains(user.Raw, "@kate 帮我清理") {
			secondUserIndex = i
		}
		if transcript, ok := block.(*TranscriptBlock); ok && strings.Contains(transcript.Raw, "@kate 帮我清理") {
			secondUserIndex = i
		}
		if turn, ok := block.(*ParticipantTurnBlock); ok {
			participantBlocks = append(participantBlocks, turn)
			if turn.SessionID == "task-1:2" {
				secondTurnIndex = i
			}
		}
	}
	if len(participantBlocks) != 2 {
		t.Fatalf("participant blocks = %#v, want two prompt turns", participantBlocks)
	}
	firstTurn := participantBlocks[0]
	secondTurn := participantBlocks[1]
	if firstTurn.SessionID == secondTurn.SessionID {
		t.Fatalf("participant turn session ids both %q, want separate prompt scopes", firstTurn.SessionID)
	}
	if secondUserIndex < 0 || secondTurnIndex < 0 || secondTurnIndex <= secondUserIndex {
		t.Fatalf("second user index=%d second turn index=%d blocks=%#v", secondUserIndex, secondTurnIndex, blocks)
	}
	if firstTurn.Actor != "@kate" || secondTurn.Actor != "@kate" {
		t.Fatalf("actors = %q/%q, want @kate", firstTurn.Actor, secondTurn.Actor)
	}
	if got := secondTurn.Events[0].Text; got != "second" {
		t.Fatalf("second turn text = %q, want second", got)
	}
	if !participantTurnIsTerminal(secondTurn.Status) {
		t.Fatalf("second turn status = %q, want terminal after task result", secondTurn.Status)
	}
}

func TestGatewayParticipantUserMessageDoesNotDuplicateDisplayedPrompt(t *testing.T) {
	model := newGatewayEventTestModel()

	updated, _ := model.Update(UserMessageMsg{Text: "/claude 总结一下工作"})
	model = updated.(*Model)
	updated, _ = model.Update(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindUserMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin: &kernel.EventOrigin{
				Scope:         kernel.EventScopeParticipant,
				ScopeID:       "participant-turn-1",
				ParticipantID: "participant-1",
				Actor:         "@jeff",
			},
			Narrative: &kernel.NarrativePayload{
				Role:  kernel.NarrativeRoleUser,
				Text:  "总结一下工作",
				Scope: kernel.EventScopeParticipant,
			},
		},
	})
	model = updated.(*Model)

	var userLines []string
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok {
			userLines = append(userLines, "▌ "+user.Raw)
			continue
		}
		if transcript, ok := block.(*TranscriptBlock); ok && strings.HasPrefix(strings.TrimSpace(transcript.Raw), ">") {
			userLines = append(userLines, transcript.Raw)
		}
	}
	if len(userLines) != 1 || !strings.Contains(userLines[0], "/claude 总结一下工作") {
		t.Fatalf("user lines = %#v, want only displayed slash prompt", userLines)
	}
	if strings.Contains(strings.Join(userLines, "\n"), "▌ 总结一下工作") || strings.Contains(strings.Join(userLines, "\n"), "> 总结一下工作") {
		t.Fatalf("user lines = %#v, should not render participant prompt echo", userLines)
	}
}

func TestParticipantTurnCompletionDoesNotRenderTwoDurationDividers(t *testing.T) {
	model := NewModel(Config{NoColor: true})
	model.viewport.SetWidth(60)
	model.viewport.SetHeight(20)
	start := time.Now().Add(-2 * time.Minute)
	end := start.Add(45 * time.Second)
	block := NewParticipantTurnBlock("task-1:1", "@codex")
	block.StartedAt = start
	block.EndedAt = end
	block.Status = "completed"
	block.Events = append(block.Events, SubagentEvent{Kind: SEAssistant, Text: "side answer", Done: true})
	model.doc.Append(block)
	model.participantTurnIDs = map[string]string{block.SessionID: block.BlockID()}
	model.activeParticipantTurnSessionID = block.SessionID
	model.showTurnDivider = true
	model.runStartedAt = time.Now().Add(-75 * time.Second)

	updated, _ := model.Update(TaskResultMsg{})
	model = updated.(*Model)
	model.syncViewportContent()

	dividerCount := 0
	for _, line := range model.viewportPlainLines {
		if strings.Contains(line, "─") {
			dividerCount++
		}
	}
	if dividerCount != 1 {
		t.Fatalf("viewport lines = %#v, want one duration divider, got %d", model.viewportPlainLines, dividerCount)
	}
}

func TestEmptyTerminalParticipantTurnDoesNotRenderArrowOrZeroDurationFooter(t *testing.T) {
	model := NewModel(Config{NoColor: true})
	start := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	block := NewParticipantTurnBlock("participant-empty", "")
	block.StartedAt = start
	block.EndedAt = start
	block.Status = "completed"

	rows := block.Render(BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme})
	if len(rows) != 0 {
		t.Fatalf("rendered rows = %#v, want empty terminal participant turn hidden", renderedPlainRows(rows))
	}
}

func TestGatewayInterleavedStreamingFinalReplacesMatchingNarrativeOnly(t *testing.T) {
	model := newGatewayEventTestModel()

	send := func(payload *kernel.NarrativePayload) *Model {
		updated, _ := model.Update(kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative:  payload,
			},
		})
		model = updated.(*Model)
		return model
	}

	send(&kernel.NarrativePayload{
		Role:          kernel.NarrativeRoleAssistant,
		ReasoningText: "r1",
		Final:         false,
		Scope:         kernel.EventScopeMain,
	})
	send(&kernel.NarrativePayload{
		Role:  kernel.NarrativeRoleAssistant,
		Text:  "a1",
		Final: false,
		Scope: kernel.EventScopeMain,
	})
	send(&kernel.NarrativePayload{
		Role:          kernel.NarrativeRoleAssistant,
		ReasoningText: "r2-partial",
		Final:         false,
		Scope:         kernel.EventScopeMain,
	})
	send(&kernel.NarrativePayload{
		Role:  kernel.NarrativeRoleAssistant,
		Text:  "a2-partial",
		Final: false,
		Scope: kernel.EventScopeMain,
	})
	send(&kernel.NarrativePayload{
		Role:          kernel.NarrativeRoleAssistant,
		ReasoningText: "r2-final",
		Text:          "a2-final",
		Final:         true,
		Scope:         kernel.EventScopeMain,
	})

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	if got := len(block.Events); got != 2 {
		t.Fatalf("len(block.Events) = %d, want 2; got %#v", got, block.Events)
	}
	wantKinds := []SubagentEventKind{SEReasoning, SEAssistant}
	wantTexts := []string{"r2-final", "a2-final"}
	for i := range wantKinds {
		if block.Events[i].Kind != wantKinds[i] || block.Events[i].Text != wantTexts[i] {
			t.Fatalf("block.Events[%d] = %#v, want kind=%v text=%q", i, block.Events[i], wantKinds[i], wantTexts[i])
		}
	}
}

func TestGatewayAnchoredSubagentNarrativeDoesNotCreateStandalonePanel(t *testing.T) {
	model := newGatewayEventTestModel()
	for _, env := range []kernel.EventEnvelope{
		{Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "spawn-1",
				ToolName: "SPAWN",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": "inspect"},
			},
		}},
		{Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin: &kernel.EventOrigin{
				Scope:   kernel.EventScopeSubagent,
				ScopeID: "jack",
			},
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"stream": map[string]any{
							"parent_call_id": "spawn-1",
							"parent_tool":    "SPAWN",
						},
					},
				},
			},
			Narrative: &kernel.NarrativePayload{
				Role: kernel.NarrativeRoleAssistant,
				Text: "child output",
			},
		}},
	} {
		updated, _ := model.Update(env)
		model = updated.(*Model)
	}
	for _, block := range model.doc.Blocks() {
		if panel, ok := block.(*SubagentPanelBlock); ok {
			t.Fatalf("anchored child stream created standalone panel: %#v", panel)
		}
	}
}
