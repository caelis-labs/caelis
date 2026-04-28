package tuiapp

import (
	"context"
	"strings"
	"testing"
	"time"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
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
				ArgsText: `/tmp/demo.txt`,
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
				CallID:     "call-1",
				ToolName:   "READ",
				OutputText: "/tmp/demo.txt",
				Status:     "completed",
				Scope:      appgateway.EventScopeMain,
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
				ArgsText: `go test ./gateway/...`,
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
				CallID:     "call-1",
				ToolName:   "BASH",
				OutputText: "stdout resolving packages",
				Status:     appgateway.ToolStatusRunning,
				Scope:      appgateway.EventScopeMain,
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
				ArgsText: "gateway/core/types.go",
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
				CallID:     "call-1",
				ToolName:   "READ",
				OutputText: "package core\n\ntype Event struct{}",
				Status:     appgateway.ToolStatusCompleted,
				Scope:      appgateway.EventScopeMain,
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
		updated, _ := model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindToolCall,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				ToolCall: &appgateway.ToolCallPayload{
					CallID:   id,
					ToolName: name,
					ArgsText: args,
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
					CallID:     id,
					ToolName:   name,
					OutputText: output,
					Status:     appgateway.ToolStatusCompleted,
					Scope:      appgateway.EventScopeMain,
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
		!strings.Contains(joined, "  └ Read gateway/core/types.go") ||
		!strings.Contains(joined, "    Search EventKind") ||
		!strings.Contains(joined, "    List tui/tuiapp") {
		t.Fatalf("rendered rows = %q, want compact exploration summary", joined)
	}
	if strings.Contains(joined, "type Event struct{}") || strings.Contains(joined, "42 matches") {
		t.Fatalf("rendered rows = %q, want exploration details hidden while collapsed", joined)
	}
	if strings.Contains(joined, "Now let me explore more") || strings.Contains(joined, "Let me search the event kind references next") {
		t.Fatalf("rendered rows = %q, want exploration reasoning hidden while collapsed", joined)
	}
	exploreTailIdx := indexOfRowContaining(plain, "List tui/tuiapp")
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
		!strings.Contains(joined, "    Read gateway/core/types.go") ||
		!strings.Contains(joined, "Search EventKind") ||
		!strings.Contains(joined, "List tui/tuiapp") {
		t.Fatalf("expanded rows = %q, want ordered exploration stage", joined)
	}
	if strings.Contains(joined, "type Event struct{}") || strings.Contains(joined, "42 matches") {
		t.Fatalf("expanded rows = %q, should show compact calls rather than raw outputs", joined)
	}
}

func TestGatewayACPExplorationNamedToolsDoNotRenderExploredGroup(t *testing.T) {
	model := newGatewayEventTestModel()
	sendACPTool := func(id string, name string, args string, output string) {
		updated, _ := model.Update(appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindToolCall,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session", Source: "acp"},
				ToolCall: &appgateway.ToolCallPayload{
					CallID:   id,
					ToolName: name,
					ArgsText: args,
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
					CallID:     id,
					ToolName:   name,
					OutputText: output,
					Status:     appgateway.ToolStatusCompleted,
					Scope:      appgateway.EventScopeMain,
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
	if !strings.Contains(joined, "READ gateway/core/types.go") || !strings.Contains(joined, "SEARCH EventKind") {
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
					"running": false,
					"state":   "completed",
					"task_id": "spawn-task-1",
					"result":  "child line 1\nchild line 2\n",
				},
			},
			want:        []string{"• Spawned", "  └ child line 1", "    child line 2"},
			forbidden:   []string{"task / running", "state completed", "spawn-task-1"},
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
				CallID:     "bash-task-1",
				ToolName:   "BASH",
				Status:     appgateway.ToolStatusRunning,
				Scope:      appgateway.EventScopeMain,
				OutputText: `{"running":true,"session_id":"556d7447-4554-4fb9-ad1c-bb5a2e0f85ac","state":"running","supports_input":true,"task_id":"task-9"}`,
				RawInput:   map[string]any{"command": `sleep 10`},
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
				CallID:     "task-1",
				ToolName:   "TASK",
				Status:     appgateway.ToolStatusRunning,
				Scope:      appgateway.EventScopeMain,
				OutputText: `{"running":true,"session_id":"556d7447-4554-4fb9-ad1c-bb5a2e0f85ac","state":"running","supports_input":true,"task_id":"task-9"}`,
				RawInput:   map[string]any{"action": "wait", "task_id": "task-9", "yield_time_ms": 5000},
				RawOutput: map[string]any{
					"running":        true,
					"session_id":     "556d7447-4554-4fb9-ad1c-bb5a2e0f85ac",
					"state":          "running",
					"supports_input": true,
					"task_id":        "task-9",
				},
			},
			want:        []string{"• Task WAIT 5 s"},
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
				CallID:     "write-failed-1",
				ToolName:   "WRITE",
				Status:     appgateway.ToolStatusFailed,
				Scope:      appgateway.EventScopeMain,
				OutputText: "Sandbox permission denied. Use a writable workspace path or request elevated permissions.",
				RawInput:   map[string]any{"path": "/tmp/workspace/workflow.go", "content": "package workflow\n"},
			},
			want:        []string{"• Write failed workflow.go", "└ Sandbox permission denied"},
			forbidden:   []string{"• Wrote workflow.go", "╭", "╰", "│ ! workflow.go"},
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

func TestGatewayConsecutiveTaskControlsMergeIntoOneInstructionRow(t *testing.T) {
	model := newGatewayEventTestModel()
	for _, item := range []struct {
		callID  string
		action  string
		input   string
		yieldMS int
	}{
		{callID: "task-0", action: "write", input: "Alice"},
		{callID: "task-1", action: "wait", yieldMS: 5000},
		{callID: "task-2", action: "wait", yieldMS: 8000},
	} {
		rawInput := map[string]any{"action": item.action, "task_id": "task-9", "yield_time_ms": item.yieldMS}
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
						"task_id": "task-9",
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
	if !strings.Contains(joined, `• Task WRITE "Alice" · WAIT 5 s · WAIT 8 s`) {
		t.Fatalf("rendered rows = %q, want merged TASK controls", joined)
	}
	if strings.Contains(joined, "TASK") || strings.Contains(joined, "task-9") {
		t.Fatalf("rendered rows = %q, should hide raw TASK tool and task id", joined)
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
		`WRITE "line one\nline two\nline three with TASK_WRITE_TAIL_MARKER"`,
		"WAIT 500 ms",
		"CANCEL",
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
	for _, want := range []string{"  └ 进度: 1/30", "    进度: 3/30", "• Task WAIT 5 s"} {
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
	if !strings.HasPrefix(model.viewportPlainLines[tailIdx], "      ") {
		t.Fatalf("wrapped tail line = %q, want continuation indentation", model.viewportPlainLines[tailIdx])
	}
}

func TestGatewaySpawnArgumentsRenderFullPrompt(t *testing.T) {
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
	for _, want := range []string{`"agent":"self"`, "SPAWN_PROMPT_TAIL_MARKER"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered rows = %q, want %q", joined, want)
		}
	}
	if strings.Contains(joined, "...") {
		t.Fatalf("rendered rows = %q, SPAWN args should not be truncated", joined)
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

func TestGatewayAssistantFinalKeepsReasoningVisible(t *testing.T) {
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
	if !strings.Contains(joined, "thinking through the plan") {
		t.Fatalf("rendered rows = %q, want reasoning text to remain visible", joined)
	}
	if !strings.Contains(joined, "final answer") {
		t.Fatalf("rendered rows = %q, want assistant text", joined)
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
