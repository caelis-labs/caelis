package tuiapp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
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

func TestModelUpdateConsumesGatewayAssistantEventIntoMainTurnBlock(t *testing.T) {
	model := newGatewayEventTestModel()

	updated, _ := model.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindAssistantMessage,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Narrative: &appgateway.NarrativePayload{
				Role:  appgateway.NarrativeRoleAssistant,
				Text:  "gateway answer",
				Final: true,
				Scope: appgateway.EventScopeMain,
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
		updated, _ := model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindAssistantMessage,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Narrative: &appgateway.NarrativePayload{
					Role:          appgateway.NarrativeRoleAssistant,
					ReasoningText: text,
					Final:         false,
					Visibility:    string(sdksession.VisibilityUIOnly),
					UpdateType:    string(sdksession.ProtocolUpdateTypeAgentThought),
					Scope:         appgateway.EventScopeMain,
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

	updated, _ := model.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindAssistantMessage,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Narrative: &appgateway.NarrativePayload{
				Role:  appgateway.NarrativeRoleAssistant,
				Text:  "partial answer",
				Final: false,
				Scope: appgateway.EventScopeMain,
			},
		},
	})
	m := updated.(*Model)
	updated, _ = m.Update(appgateway.EventEnvelope{
		Err: &appgateway.Error{
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

	updated, _ := model.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "READ",
				RawInput: map[string]any{"path": "/tmp/demo.txt"},
				Status:   "running",
				Scope:    appgateway.EventScopeMain,
			},
		},
	})
	m := updated.(*Model)
	updated, _ = m.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:    "call-1",
				ToolName:  "READ",
				RawInput:  map[string]any{"path": "/tmp/demo.txt"},
				RawOutput: map[string]any{"path": "/tmp/demo.txt"},
				Status:    "completed",
				Scope:     appgateway.EventScopeMain,
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

	updated, _ := model.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "BASH",
				RawInput: map[string]any{"command": `go test ./gateway/...`},
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
			},
		},
	})
	model = updated.(*Model)
	updated, _ = model.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:    "call-1",
				ToolName:  "BASH",
				RawInput:  map[string]any{"command": `go test ./gateway/...`},
				RawOutput: map[string]any{"stdout": "stdout resolving packages"},
				Status:    appgateway.ToolStatusRunning,
				Scope:     appgateway.EventScopeMain,
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

	updated, _ := model.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "READ",
				RawInput: map[string]any{"path": "gateway/core/types.go"},
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
			},
		},
	})
	model = updated.(*Model)
	updated, _ = model.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:    "call-1",
				ToolName:  "READ",
				RawInput:  map[string]any{"path": "gateway/core/types.go"},
				RawOutput: map[string]any{"text": "package core\n\ntype Event struct{}"},
				Status:    appgateway.ToolStatusCompleted,
				Scope:     appgateway.EventScopeMain,
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
		updated, _ := model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindToolCall,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				ToolCall: &appgateway.ToolCallPayload{
					CallID:   id,
					ToolName: name,
					RawInput: rawInput,
					Status:   appgateway.ToolStatusRunning,
					Scope:    appgateway.EventScopeMain,
				},
			},
		})
		model = updated.(*Model)
		updated, _ = model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindToolResult,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				ToolResult: &appgateway.ToolResultPayload{
					CallID:    id,
					ToolName:  name,
					RawInput:  rawInput,
					RawOutput: map[string]any{"text": output},
					Status:    appgateway.ToolStatusCompleted,
					Scope:     appgateway.EventScopeMain,
				},
			},
		})
		model = updated.(*Model)
	}
	sendReasoning := func(text string) {
		updated, _ := model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindAssistantMessage,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Narrative: &appgateway.NarrativePayload{
					Role:          appgateway.NarrativeRoleAssistant,
					ReasoningText: text,
					Final:         true,
					Scope:         appgateway.EventScopeMain,
				},
			},
		})
		model = updated.(*Model)
	}
	sendReasoning("Now let me explore more to understand the project structure - specifically:\n1. The service layer for config.\n2. The rbac remote client.")
	sendTool("read-1", "READ", "gateway/core/types.go", "type Event struct{}")
	sendReasoning("Let me search the event kind references next.")
	sendTool("rg-1", "RG", "EventKind", "42 matches")
	sendTool("list-1", "LIST", "tui/tuiapp", "transcript_event.go")
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
		!strings.Contains(joined, "    List tuiapp") {
		t.Fatalf("rendered rows = %q, want compact exploration summary", joined)
	}
	if strings.Contains(joined, "type Event struct{}") || strings.Contains(joined, "42 matches") {
		t.Fatalf("rendered rows = %q, want exploration details hidden while collapsed", joined)
	}
	if strings.Contains(joined, "Now let me explore more") || strings.Contains(joined, "Let me search the event kind references next") {
		t.Fatalf("rendered rows = %q, want exploration reasoning hidden while collapsed", joined)
	}
	exploreTailIdx := indexOfRowContaining(plain, "List tuiapp")
	patchReasonIdx := indexOfRowContaining(plain, "> Let me patch the hook implementation next.")
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
		!strings.Contains(joined, "List tuiapp") {
		t.Fatalf("expanded rows = %q, want ordered exploration stage", joined)
	}
	if strings.Contains(joined, "type Event struct{}") || strings.Contains(joined, "42 matches") {
		t.Fatalf("expanded rows = %q, should show compact calls rather than raw outputs", joined)
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

func TestGatewayACPExplorationNamedToolsDoNotRenderExploredGroup(t *testing.T) {
	model := newGatewayEventTestModel()
	sendACPTool := func(id string, name string, args string, output string) {
		rawInput := map[string]any{"path": args}
		if strings.EqualFold(name, "SEARCH") {
			rawInput = map[string]any{"query": args}
		}
		updated, _ := model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindToolCall,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session", Source: "acp"},
				ToolCall: &appgateway.ToolCallPayload{
					CallID:   id,
					ToolName: name,
					RawInput: rawInput,
					Status:   appgateway.ToolStatusRunning,
					Scope:    appgateway.EventScopeMain,
				},
			},
		})
		model = updated.(*Model)
		updated, _ = model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindToolResult,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session", Source: "acp"},
				ToolResult: &appgateway.ToolResultPayload{
					CallID:    id,
					ToolName:  name,
					RawInput:  rawInput,
					RawOutput: map[string]any{"text": output},
					Status:    appgateway.ToolStatusCompleted,
					Scope:     appgateway.EventScopeMain,
				},
			},
		})
		model = updated.(*Model)
	}

	sendACPTool("read-1", "READ", "gateway/core/types.go", "type Event struct{}")
	sendACPTool("search-1", "SEARCH", "EventKind", "42 matches")

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
	if strings.Contains(joined, "• Explored") {
		t.Fatalf("rendered rows = %q, want ACP tools to stay out of compact exploration grouping", joined)
	}
	if !strings.Contains(joined, "Read types.go") || !strings.Contains(joined, `Search "EventKind"`) {
		t.Fatalf("rendered rows = %q, want standard tool rows", joined)
	}
	if !block.toolPanelExpanded("read-1") || !block.toolPanelExpanded("search-1") {
		t.Fatalf("ACP tool panels should not default collapse; expanded map = %#v", block.ExpandedTools)
	}
}

func TestGatewayToolDisplayMetaRendersActionableSummaries(t *testing.T) {
	tests := []struct {
		name        string
		call        *appgateway.ToolCallPayload
		result      *appgateway.ToolResultPayload
		want        []string
		forbidden   []string
		expandPanel bool
	}{
		{
			name: "read line range",
			call: &appgateway.ToolCallPayload{
				CallID:   "read-1",
				ToolName: "READ",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/demo.py", "offset": 0, "limit": 100},
			},
			result: &appgateway.ToolResultPayload{
				CallID:   "read-1",
				ToolName: "READ",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/demo.py", "offset": 0, "limit": 100},
				RawOutput: map[string]any{
					"path":       "/tmp/workspace/demo.py",
					"start_line": 1,
					"end_line":   100,
					"content":    "1: package main",
				},
			},
			want:      []string{"READ demo.py 1~100"},
			forbidden: []string{"│   /tmp/workspace/demo.py"},
		},
		{
			name: "glob count",
			call: &appgateway.ToolCallPayload{
				CallID:   "glob-1",
				ToolName: "GLOB",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"pattern": "**/*.py"},
			},
			result: &appgateway.ToolResultPayload{
				CallID:   "glob-1",
				ToolName: "GLOB",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"pattern": "**/*.py"},
				RawOutput: map[string]any{
					"pattern": "**/*.py",
					"count":   5,
					"matches": []any{"a.py", "b.py", "c.py", "d.py", "e.py"},
				},
			},
			want: []string{"GLOB **/*.py 5 matches"},
		},
		{
			name: "bash terminal panel",
			call: &appgateway.ToolCallPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"command": `echo "hello"`},
			},
			result: &appgateway.ToolResultPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeMain,
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
			call: &appgateway.ToolCallPayload{
				CallID:   "acp-exec-1",
				ToolName: "git",
				ToolKind: "execute",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeParticipant,
				RawInput: map[string]any{"cmd": "git diff --cached -- file.go"},
			},
			result: &appgateway.ToolResultPayload{
				CallID:   "acp-exec-1",
				ToolName: "git",
				ToolKind: "execute",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeParticipant,
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
			call: &appgateway.ToolCallPayload{
				CallID:   "spawn-1",
				ToolName: "SPAWN",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"prompt": "write fibonacci"},
			},
			result: &appgateway.ToolResultPayload{
				CallID:   "spawn-1",
				ToolName: "SPAWN",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeMain,
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
					"result":           "child line 1\nchild line 2\n",
				},
			},
			want:        []string{"• Spawned", "  └ child line 1", "    child line 2"},
			forbidden:   []string{"task / running", "state completed", "spawn-task-1", "self-001", "internal_task_id", "@leo", "用例| C输出欢迎", "_empty", "了根据"},
			expandPanel: true,
		},
		{
			name: "bash task snapshot does not expose raw session json",
			call: &appgateway.ToolCallPayload{
				CallID:   "bash-task-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"command": `sleep 10`},
			},
			result: &appgateway.ToolResultPayload{
				CallID:   "bash-task-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
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
			call: &appgateway.ToolCallPayload{
				CallID:   "task-1",
				ToolName: "TASK",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"action": "wait", "task_id": "task-9", "yield_time_ms": 5000},
			},
			result: &appgateway.ToolResultPayload{
				CallID:   "task-1",
				ToolName: "TASK",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
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
			call: &appgateway.ToolCallPayload{
				CallID:   "write-1",
				ToolName: "WRITE",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/tool_demo_summary.md", "content": "one\ntwo\n"},
			},
			result: &appgateway.ToolResultPayload{
				CallID:   "write-1",
				ToolName: "WRITE",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeMain,
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
			call: &appgateway.ToolCallPayload{
				CallID:   "write-failed-1",
				ToolName: "WRITE",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/workflow.go", "content": "package workflow\n"},
			},
			result: &appgateway.ToolResultPayload{
				CallID:    "write-failed-1",
				ToolName:  "WRITE",
				Status:    appgateway.ToolStatusFailed,
				Scope:     appgateway.EventScopeMain,
				RawInput:  map[string]any{"path": "/tmp/workspace/workflow.go", "content": "package workflow\n"},
				RawOutput: map[string]any{"error": "Sandbox permission denied. Use a writable workspace path or request elevated permissions."},
			},
			want:        []string{"• Write failed workflow.go", "└ Sandbox permission denied"},
			forbidden:   []string{"• Wrote workflow.go", "╭", "╰", "│ ! workflow.go"},
			expandPanel: true,
		},
		{
			name: "failed patch preserves concrete error reason",
			call: &appgateway.ToolCallPayload{
				CallID:   "patch-failed-1",
				ToolName: "PATCH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/gm_license.go", "old": "licenseEntity.ESN", "new": "licenseEntity.Esn"},
			},
			result: &appgateway.ToolResultPayload{
				CallID:    "patch-failed-1",
				ToolName:  "PATCH",
				Status:    appgateway.ToolStatusFailed,
				Scope:     appgateway.EventScopeMain,
				RawInput:  map[string]any{"path": "/tmp/workspace/gm_license.go", "old": "licenseEntity.ESN", "new": "licenseEntity.Esn"},
				RawOutput: map[string]any{"error": `tool: PATCH target "gm_license.go" did not contain an exact match for "old"`},
			},
			want:        []string{"• Patch failed gm_license.go", `└ tool: PATCH target "gm_license.go" did not contain an exact match for "old"`},
			forbidden:   []string{"  └ failed", "╭", "╰"},
			expandPanel: true,
		},
		{
			name: "patch rich diff panel",
			call: &appgateway.ToolCallPayload{
				CallID:   "patch-1",
				ToolName: "PATCH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/demo.py", "old": "old line", "new": "new line"},
			},
			result: &appgateway.ToolResultPayload{
				CallID:   "patch-1",
				ToolName: "PATCH",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeMain,
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
			call: &appgateway.ToolCallPayload{
				CallID:   "patch-all-1",
				ToolName: "PATCH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/gm_license_repo.go", "old": "entity.GMLicense", "new": "entity.GmLicense", "replace_all": true},
			},
			result: &appgateway.ToolResultPayload{
				CallID:   "patch-all-1",
				ToolName: "PATCH",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeMain,
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
			call: &appgateway.ToolCallPayload{
				CallID:   "patch-hunks-1",
				ToolName: "PATCH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"path": "/tmp/workspace/gm_license_repo.go", "old": "entity.GMLicense", "new": "entity.GmLicense", "replace_all": true},
			},
			result: &appgateway.ToolResultPayload{
				CallID:   "patch-hunks-1",
				ToolName: "PATCH",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeMain,
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
			updated, _ := model.Update(appgateway.EventEnvelope{
				Event: appgateway.Event{
					Kind:       appgateway.EventKindToolCall,
					SessionRef: sdksession.SessionRef{SessionID: "root-session"},
					ToolCall:   tt.call,
				},
			})
			model = updated.(*Model)
			updated, _ = model.Update(appgateway.EventEnvelope{
				Event: appgateway.Event{
					Kind:       appgateway.EventKindToolResult,
					SessionRef: sdksession.SessionRef{SessionID: "root-session"},
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
		updated, _ := model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindAssistantMessage,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Narrative: &appgateway.NarrativePayload{
					Role:          appgateway.NarrativeRoleAssistant,
					ReasoningText: text,
					Final:         true,
					Scope:         appgateway.EventScopeMain,
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
		updated, _ := model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindToolCall,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				ToolCall: &appgateway.ToolCallPayload{
					CallID:   item.callID,
					ToolName: "TASK",
					Status:   appgateway.ToolStatusRunning,
					Scope:    appgateway.EventScopeMain,
					RawInput: rawInput,
				},
			},
		})
		model = updated.(*Model)
		updated, _ = model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindToolResult,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				ToolResult: &appgateway.ToolResultPayload{
					CallID:   item.callID,
					ToolName: "TASK",
					Status:   appgateway.ToolStatusRunning,
					Scope:    appgateway.EventScopeMain,
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
	if !strings.Contains(joined, "• Tasks") ||
		!strings.Contains(joined, `  └ Write "Alice"`) ||
		!strings.Contains(joined, `    Wait ella 5s`) ||
		!strings.Contains(joined, `    Wait 8s`) {
		t.Fatalf("rendered rows = %q, want merged TASK controls", joined)
	}
	if strings.Contains(joined, "TASK") || strings.Contains(joined, "task-9") {
		t.Fatalf("rendered rows = %q, should hide raw TASK tool and task id", joined)
	}
	if !model.tryToggleACPToolPanelToken(block.BlockID(), "acp_task_stage:tasks:task-0,task-1,task-2") {
		t.Fatal("expected task stage click token to expand grouped TASK controls")
	}
	rows = block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, `  └ 两个子任务已启动`) ||
		!strings.Contains(joined, `    Write "Alice"`) ||
		!strings.Contains(joined, `    Wait ella 5s`) {
		t.Fatalf("expanded rows = %q, want task stage narrative and controls", joined)
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
		updated, _ := model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindToolCall,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				ToolCall: &appgateway.ToolCallPayload{
					CallID:   item.callID,
					ToolName: "TASK",
					Status:   appgateway.ToolStatusRunning,
					Scope:    appgateway.EventScopeMain,
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

	updated, _ := model.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "perm-1",
				ToolName: "request_permissions",
				Status:   appgateway.ToolStatusRunning,
				RawInput: permissionInput,
			},
		},
	})
	model = updated.(*Model)
	updated, _ = model.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindApprovalReview,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
			ApprovalPayload: &appgateway.ApprovalPayload{
				ToolName:       "request_permissions",
				RawInput:       map[string]any{"reason": "need directory access"},
				ReviewStatus:   appgateway.ApprovalReviewStatusInProgress,
				DecisionSource: "auto-review",
			},
		},
	})
	model = updated.(*Model)
	if got := ansi.Strip(model.buildHintText()); !strings.Contains(got, "Reviewing approval request: request_permissions") {
		t.Fatalf("approval hint = %q, want pending review hint", got)
	}

	reviewText := "Automatic approval review approved (risk: low, authorization: high): user requested it."
	updated, _ = model.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindApprovalReview,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
			ApprovalPayload: &appgateway.ApprovalPayload{
				ToolName:       "request_permissions",
				RawInput:       map[string]any{"reason": "need directory access"},
				ReviewStatus:   appgateway.ApprovalReviewStatusApproved,
				DecisionSource: "auto-review",
				ReviewText:     reviewText,
			},
		},
	})
	model = updated.(*Model)
	if got := ansi.Strip(model.buildHintText()); strings.Contains(got, "Reviewing approval request") {
		t.Fatalf("approval hint = %q, want cleared pending review hint", got)
	}
	updated, _ = model.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:    "perm-1",
				ToolName:  "request_permissions",
				Status:    appgateway.ToolStatusCompleted,
				RawInput:  permissionInput,
				RawOutput: map[string]any{"approved": true, "granted": permissionInput["permissions"]},
			},
		},
	})
	model = updated.(*Model)

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme})
	plain := strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(plain, "▸ request_permissions write /tmp/outside; read /tmp/outside") {
		t.Fatalf("rendered rows = %q, want request_permissions standard header", plain)
	}
	if !strings.Contains(plain, "⚠ "+reviewText) {
		t.Fatalf("rendered rows = %q, want approval review result at transcript location", plain)
	}
	for _, forbidden := range []string{`"approved":true`, `"granted"`, "Automatic approval review pending"} {
		if strings.Contains(plain, forbidden) {
			t.Fatalf("rendered rows = %q, should not contain %q", plain, forbidden)
		}
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
		updated, _ := model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindToolCall,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				ToolCall: &appgateway.ToolCallPayload{
					CallID:   item.callID,
					ToolName: "TASK",
					Status:   appgateway.ToolStatusRunning,
					Scope:    appgateway.EventScopeMain,
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
	for _, env := range []appgateway.EventEnvelope{
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"command": "for i in $(seq 1 30); do echo $i; sleep 1; done"},
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"command": "for i in $(seq 1 30); do echo $i; sleep 1; done"},
				RawOutput: map[string]any{
					"running":        true,
					"state":          "running",
					"task_id":        "task-7",
					"output_preview": "进度: 1/30\n",
				},
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "task-wait-1",
				ToolName: "TASK",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"action": "wait", "task_id": "task-7", "yield_time_ms": 5000},
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "task-wait-1",
				ToolName: "TASK",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
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
	updated, _ := model.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "bash-full-args",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
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
	updated, _ := model.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "spawn-full-args",
				ToolName: "SPAWN",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
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
	for _, env := range []appgateway.EventEnvelope{
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "spawn-clean-final",
				ToolName: "SPAWN",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": prompt},
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "spawn-clean-final",
				ToolName: "SPAWN",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": prompt},
				RawOutput: map[string]any{
					"running": true,
					"state":   "running",
					"task_id": "jack",
					"text":    "dirty process line\nls output that should not become final",
				},
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "spawn-clean-final",
				ToolName: "SPAWN",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": prompt},
				RawOutput: map[string]any{
					"running": false,
					"state":   "completed",
					"task_id": "jack",
					"result":  finalText,
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
	for _, want := range []string{"• Spawned self:", "已完成", "✅ 创建 hello_from_spawn.txt", "... +2 lines", "hello_from_spawn.txt  created", "报告位于 spawn_report.md"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered rows missing %q:\n%s", want, joined)
		}
	}
	for _, forbidden := range []string{"dirty process line", "ls output", "###", "**", "`", "| --- |"} {
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
	for _, env := range []appgateway.EventEnvelope{
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "spawn-running-json",
				ToolName: "SPAWN",
				ToolKind: "execute",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"agent": "claude"},
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "spawn-running-json",
				ToolName: "SPAWN",
				ToolKind: "execute",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
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

func TestSpawnFinalJSONAnswerRemainsVisible(t *testing.T) {
	t.Parallel()

	for _, answer := range []string{`{"result":"ok"}`, `{"state":"done"}`} {
		got := toolDisplayOutput("SPAWN", nil, map[string]any{"result": answer}, "", string(appgateway.ToolStatusCompleted), false)
		if got != answer {
			t.Fatalf("toolDisplayOutput(SPAWN JSON final %q) = %q, want original JSON", answer, got)
		}
	}
}

func TestGatewaySpawnClosedStreamReplacesRunningOutputWithoutTaskWait(t *testing.T) {
	model := newGatewayEventTestModel()
	prompt := "分析当前目录"
	start := appgateway.EventEnvelope{Event: appgateway.Event{
		Kind:       appgateway.EventKindToolCall,
		SessionRef: sdksession.SessionRef{SessionID: "root-session"},
		ToolCall: &appgateway.ToolCallPayload{
			CallID:   "spawn-stream-final",
			ToolName: "SPAWN",
			Status:   appgateway.ToolStatusRunning,
			Scope:    appgateway.EventScopeMain,
			RawInput: map[string]any{"agent": "self", "prompt": prompt},
		},
	}}
	updated, _ := model.Update(start)
	model = updated.(*Model)

	req := appgateway.StreamRequest{
		SessionRef: sdksession.SessionRef{SessionID: "root-session"},
		CallID:     "spawn-stream-final",
		ToolName:   "SPAWN",
		RawInput:   map[string]any{"agent": "self", "prompt": prompt},
		Ref:        sdkstream.Ref{SessionID: "root-session", TaskID: "liam"},
		Scope:      appgateway.EventScopeMain,
	}
	for _, env := range appgateway.StreamFrameEvents(req, sdkstream.Frame{
		Ref:     sdkstream.Ref{SessionID: "root-session", TaskID: "internal-task"},
		Stream:  "stdout",
		Text:    "ool_demo_showcase*.md(x6版本迭代)|**总文件数**|~80+|",
		Running: true,
	}) {
		updated, _ = model.Update(env)
		model = updated.(*Model)
	}
	for _, env := range appgateway.StreamFrameEvents(req, sdkstream.Frame{
		Ref:     sdkstream.Ref{SessionID: "root-session", TaskID: "internal-task"},
		Closed:  true,
		Running: false,
		State:   "completed",
		Result: map[string]any{
			"result": "### 摘要\n- `ool_demo_showcase.md` 存在\n**结论：** 目录用于 SPAWN 演示",
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
	for _, want := range []string{"• Spawned self:", "摘要", "ool_demo_showcase.md 存在", "结论： 目录用于 SPAWN 演示"} {
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
	for _, env := range []appgateway.EventEnvelope{
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "spawn-continue",
				ToolName: "SPAWN",
				ToolKind: "execute",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": "创建文件"},
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "spawn-continue",
				ToolName: "SPAWN",
				ToolKind: "execute",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": "创建文件"},
				RawOutput: map[string]any{
					"running": false,
					"state":   "completed",
					"task_id": "jack",
					"result":  "old final answer",
				},
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "task-wait-before-write",
				ToolName: "TASK",
				ToolKind: "execute",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeMain,
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
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "task-write-continue",
				ToolName: "TASK",
				ToolKind: "execute",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
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
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "spawn-continued-child",
				ToolName: "SPAWN",
				ToolKind: "execute",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
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
	for _, want := range []string{"• Spawned self: 创建文件", "old final answer", "• Tasks", "Wait jack", "• Write jack: 检查刚才创建的文件", "正在读取 hello_from_spawn.txt"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("running continuation rows missing %q:\n%s", want, joined)
		}
	}
	if strings.Count(joined, "• Spawned") != 1 || strings.Contains(joined, "  └ Write jack") {
		t.Fatalf("TASK write should render separately without a second SPAWN or grouped Write row:\n%s", joined)
	}

	updated, _ := model.Update(appgateway.EventEnvelope{Event: appgateway.Event{
		Kind:       appgateway.EventKindToolResult,
		SessionRef: sdksession.SessionRef{SessionID: "root-session"},
		ToolResult: &appgateway.ToolResultPayload{
			CallID:   "spawn-continued-child",
			ToolName: "SPAWN",
			ToolKind: "execute",
			Status:   appgateway.ToolStatusCompleted,
			Scope:    appgateway.EventScopeMain,
			RawInput: map[string]any{"agent": "self", "prompt": "检查刚才创建的文件"},
			RawOutput: map[string]any{
				"running": false,
				"state":   "completed",
				"task_id": "jack",
				"result":  "### 检查完成\n- `hello_from_spawn.txt` 内容正确",
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
	for _, env := range []appgateway.EventEnvelope{
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"command": "for i in $(seq 1 10); do echo $i; done"},
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
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
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
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
	for _, env := range []appgateway.EventEnvelope{
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "plan-1",
				ToolName: "PLAN",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: rawInput,
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:    "plan-1",
				ToolName:  "PLAN",
				Status:    appgateway.ToolStatusCompleted,
				Scope:     appgateway.EventScopeMain,
				RawInput:  rawInput,
				RawOutput: rawOutput,
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindPlanUpdate,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Plan: &appgateway.PlanPayload{Entries: []appgateway.PlanEntryPayload{
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
	for _, want := range []string{"• Updated Plan", "  └ ✔ Inspect files", "    ▸ Run validation"} {
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
		status    appgateway.ToolStatus
		isErr     bool
		rawOutput map[string]any
		want      []string
		forbid    []string
	}{
		{
			name:   "running preview",
			status: appgateway.ToolStatusRunning,
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
			name:   "failed stderr",
			status: appgateway.ToolStatusFailed,
			isErr:  true,
			rawOutput: map[string]any{
				"stderr":    "permission denied\n",
				"stdout":    "ignored stdout\n",
				"exit_code": 1,
			},
			want:   []string{"  └ permission denied"},
			forbid: []string{"|_", "BASH output", "│", "stderr permission denied", "ignored stdout", "exit 1"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			model := newGatewayEventTestModel()
			callID := "bash-" + strings.ReplaceAll(tt.name, " ", "-")
			updated, _ := model.Update(appgateway.EventEnvelope{
				Event: appgateway.Event{
					Kind:       appgateway.EventKindToolCall,
					SessionRef: sdksession.SessionRef{SessionID: "root-session"},
					ToolCall: &appgateway.ToolCallPayload{
						CallID:   callID,
						ToolName: "BASH",
						Status:   appgateway.ToolStatusRunning,
						Scope:    appgateway.EventScopeMain,
						RawInput: map[string]any{"command": "for i in 1 2; do echo $i; done"},
					},
				},
			})
			model = updated.(*Model)
			updated, _ = model.Update(appgateway.EventEnvelope{
				Event: appgateway.Event{
					Kind:       appgateway.EventKindToolResult,
					SessionRef: sdksession.SessionRef{SessionID: "root-session"},
					ToolResult: &appgateway.ToolResultPayload{
						CallID:    callID,
						ToolName:  "BASH",
						Status:    tt.status,
						Error:     tt.isErr,
						Scope:     appgateway.EventScopeMain,
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

	updated, _ := model.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindAssistantMessage,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Narrative: &appgateway.NarrativePayload{
				Role:          appgateway.NarrativeRoleAssistant,
				ReasoningText: "thinking through the plan",
				Final:         false,
				Scope:         appgateway.EventScopeMain,
			},
		},
	})
	m := updated.(*Model)
	updated, _ = m.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindAssistantMessage,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Narrative: &appgateway.NarrativePayload{
				Role:          appgateway.NarrativeRoleAssistant,
				ReasoningText: "thinking through the plan",
				Text:          "final answer",
				Final:         true,
				Scope:         appgateway.EventScopeMain,
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
	if !strings.Contains(joined, "> thinking through the plan") {
		t.Fatalf("rendered rows = %q, want folded reasoning preview", joined)
	}
	if strings.Contains(joined, "· thinking through the plan") {
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
	if !strings.Contains(joined, "∨ thinking through the plan") {
		t.Fatalf("expanded rows = %q, want expanded reasoning preview", joined)
	}
}

func TestGatewayReasoningFoldsAfterAttentionToolLoopAndTogglesInline(t *testing.T) {
	model := newGatewayEventTestModel()
	for _, env := range []appgateway.EventEnvelope{
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindAssistantMessage,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Narrative: &appgateway.NarrativePayload{
				Role:          appgateway.NarrativeRoleAssistant,
				ReasoningText: "thinking through the command choice",
				Final:         true,
				Scope:         appgateway.EventScopeMain,
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindAssistantMessage,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Narrative: &appgateway.NarrativePayload{
				Role:  appgateway.NarrativeRoleAssistant,
				Text:  "I will run the test.",
				Final: true,
				Scope: appgateway.EventScopeMain,
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"command": "go test ./tui/..."},
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"command": "go test ./tui/..."},
				RawOutput: map[string]any{
					"stdout":    "ok github.com/OnslaughtSnail/caelis/tui/tuiapp\n",
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
	if !strings.Contains(joined, "> thinking through the command choice") {
		t.Fatalf("rendered rows = %q, want folded reasoning preview", joined)
	}
	if strings.Contains(joined, "Thought a few seconds") {
		t.Fatalf("rendered rows = %q, should not show reasoning duration", joined)
	}
	reasonIdx := indexOfRowContaining(plain, "> thinking through the command choice")
	bashIdx := indexOfRowContaining(plain, "• Ran go test ./tui/...")
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
	if !strings.Contains(joined, "∨ thinking through the command choice") {
		t.Fatalf("expanded rows = %q, want expanded reasoning preview", joined)
	}
	if strings.Count(joined, "thinking through the command choice") != 1 {
		t.Fatalf("expanded rows = %q, want single-line reasoning rendered once", joined)
	}
}

func TestGatewayExpandedReasoningReplacesFoldedPreviewInPlace(t *testing.T) {
	model := newGatewayEventTestModel()
	reasoning := "Now let me verify the DDL matches every field in the entity.\nEntity field -> DDL column\nID -> id varchar(64) NOT NULL"
	for _, env := range []appgateway.EventEnvelope{
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindAssistantMessage,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Narrative: &appgateway.NarrativePayload{
				Role:          appgateway.NarrativeRoleAssistant,
				ReasoningText: reasoning,
				Final:         true,
				Scope:         appgateway.EventScopeMain,
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"command": "go test ./..."},
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeMain,
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
	if !strings.Contains(plain, "∨ Now let me verify the DDL matches every field in the entity.") {
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
	if !strings.Contains(joined, "> First I need to inspect the repository. Then I will patch the failing field references.") {
		t.Fatalf("rendered rows = %q, want consecutive reasoning folded into one preview", joined)
	}
	if strings.Contains(joined, "· First I need") || strings.Contains(joined, "· Then I will") {
		t.Fatalf("rendered rows = %q, consecutive reasoning should not remain expanded before PATCH", joined)
	}
	if hasBlankRowBetween(plain, indexOfRowContaining(plain, "> First I need"), indexOfRowContaining(plain, "• Patched gm_license.go")) {
		t.Fatalf("rendered rows = %#v, folded reasoning should attach to PATCH", plain)
	}
}

func TestGatewayReasoningFoldUsesTimedDurationWhenAvailable(t *testing.T) {
	model := newGatewayEventTestModel()
	start := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	for _, env := range []appgateway.EventEnvelope{
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindAssistantMessage,
			OccurredAt: start,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Narrative: &appgateway.NarrativePayload{
				Role:          appgateway.NarrativeRoleAssistant,
				ReasoningText: "first ",
				Final:         false,
				Scope:         appgateway.EventScopeMain,
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindAssistantMessage,
			OccurredAt: start.Add(900 * time.Millisecond),
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Narrative: &appgateway.NarrativePayload{
				Role:          appgateway.NarrativeRoleAssistant,
				ReasoningText: "last",
				Final:         false,
				Scope:         appgateway.EventScopeMain,
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindAssistantMessage,
			OccurredAt: start.Add(1500 * time.Millisecond),
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Narrative: &appgateway.NarrativePayload{
				Role:  appgateway.NarrativeRoleAssistant,
				Text:  "done thinking",
				Final: true,
				Scope: appgateway.EventScopeMain,
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			OccurredAt: start.Add(1600 * time.Millisecond),
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"command": "echo ok"},
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			OccurredAt: start.Add(1700 * time.Millisecond),
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "bash-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeMain,
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
	if !strings.Contains(joined, "> first last") {
		t.Fatalf("rendered rows = %q, want folded reasoning preview", joined)
	}
	if strings.Contains(joined, "Thought") || strings.Contains(joined, "1.5s") {
		t.Fatalf("rendered rows = %q, should not show reasoning duration", joined)
	}
}

func TestGatewayStreamingNarrativeKeepsReasoningAnswerBoundaries(t *testing.T) {
	model := newGatewayEventTestModel()

	send := func(payload *appgateway.NarrativePayload) *Model {
		updated, _ := model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindAssistantMessage,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Narrative:  payload,
			},
		})
		model = updated.(*Model)
		return model
	}

	send(&appgateway.NarrativePayload{
		Role:          appgateway.NarrativeRoleAssistant,
		ReasoningText: "think-1 ",
		Final:         false,
		Scope:         appgateway.EventScopeMain,
	})
	send(&appgateway.NarrativePayload{
		Role:  appgateway.NarrativeRoleAssistant,
		Text:  "answer-1 ",
		Final: false,
		Scope: appgateway.EventScopeMain,
	})
	send(&appgateway.NarrativePayload{
		Role:          appgateway.NarrativeRoleAssistant,
		ReasoningText: "think-2 ",
		Final:         false,
		Scope:         appgateway.EventScopeMain,
	})
	send(&appgateway.NarrativePayload{
		Role:  appgateway.NarrativeRoleAssistant,
		Text:  "answer-2",
		Final: false,
		Scope: appgateway.EventScopeMain,
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
		updated, _ := model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindAssistantMessage,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Origin: &appgateway.EventOrigin{
					Scope:         appgateway.EventScopeParticipant,
					ScopeID:       "codex-001",
					Actor:         "codex-001",
					ParticipantID: "codex-001",
				},
				Narrative: &appgateway.NarrativePayload{
					Role:  appgateway.NarrativeRoleAssistant,
					Actor: "codex-001",
					Text:  text,
					Final: false,
					Scope: appgateway.EventScopeParticipant,
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

func TestGatewayParticipantPromptTurnsRenderAsSeparateBlocks(t *testing.T) {
	model := newGatewayEventTestModel()

	sendUser := func(text string) {
		updated, _ := model.Update(UserMessageMsg{Text: text})
		model = updated.(*Model)
	}
	sendParticipant := func(scopeID string, text string) {
		updated, _ := model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindAssistantMessage,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Origin: &appgateway.EventOrigin{
					Scope:   appgateway.EventScopeParticipant,
					ScopeID: scopeID,
					Actor:   "@kate",
				},
				Narrative: &appgateway.NarrativePayload{
					Role:  appgateway.NarrativeRoleAssistant,
					Actor: "codex-001",
					Text:  text,
					Final: false,
					Scope: appgateway.EventScopeParticipant,
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
	updated, _ = model.Update(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindUserMessage,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Origin: &appgateway.EventOrigin{
				Scope:         appgateway.EventScopeParticipant,
				ScopeID:       "participant-turn-1",
				ParticipantID: "participant-1",
				Actor:         "@jeff",
			},
			Narrative: &appgateway.NarrativePayload{
				Role:  appgateway.NarrativeRoleUser,
				Text:  "总结一下工作",
				Scope: appgateway.EventScopeParticipant,
			},
		},
	})
	model = updated.(*Model)

	var userLines []string
	for _, block := range model.doc.Blocks() {
		if transcript, ok := block.(*TranscriptBlock); ok && strings.HasPrefix(strings.TrimSpace(transcript.Raw), ">") {
			userLines = append(userLines, transcript.Raw)
		}
	}
	if len(userLines) != 1 || !strings.Contains(userLines[0], "/claude 总结一下工作") {
		t.Fatalf("user lines = %#v, want only displayed slash prompt", userLines)
	}
	if strings.Contains(strings.Join(userLines, "\n"), "> 总结一下工作") {
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

	send := func(payload *appgateway.NarrativePayload) *Model {
		updated, _ := model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindAssistantMessage,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Narrative:  payload,
			},
		})
		model = updated.(*Model)
		return model
	}

	send(&appgateway.NarrativePayload{
		Role:          appgateway.NarrativeRoleAssistant,
		ReasoningText: "r1",
		Final:         false,
		Scope:         appgateway.EventScopeMain,
	})
	send(&appgateway.NarrativePayload{
		Role:  appgateway.NarrativeRoleAssistant,
		Text:  "a1",
		Final: false,
		Scope: appgateway.EventScopeMain,
	})
	send(&appgateway.NarrativePayload{
		Role:          appgateway.NarrativeRoleAssistant,
		ReasoningText: "r2-partial",
		Final:         false,
		Scope:         appgateway.EventScopeMain,
	})
	send(&appgateway.NarrativePayload{
		Role:  appgateway.NarrativeRoleAssistant,
		Text:  "a2-partial",
		Final: false,
		Scope: appgateway.EventScopeMain,
	})
	send(&appgateway.NarrativePayload{
		Role:          appgateway.NarrativeRoleAssistant,
		ReasoningText: "r2-final",
		Text:          "a2-final",
		Final:         true,
		Scope:         appgateway.EventScopeMain,
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
	for _, env := range []appgateway.EventEnvelope{
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "spawn-1",
				ToolName: "SPAWN",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": "inspect"},
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindAssistantMessage,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Origin: &appgateway.EventOrigin{
				Scope:   appgateway.EventScopeSubagent,
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
			Narrative: &appgateway.NarrativePayload{
				Role: appgateway.NarrativeRoleAssistant,
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
