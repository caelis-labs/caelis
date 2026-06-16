package tuiapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
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

func TestModelUpdateConsumesGatewayAssistantEventIntoMainTurnBlock(t *testing.T) {
	model := newGatewayEventTestModel()

	updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:  gateway.NarrativeRoleAssistant,
				Text:  "gateway answer",
				Final: true,
				Scope: gateway.EventScopeMain,
			},
		}}))

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

func TestGatewayAssistantNarrativeBodyAlignsAfterMarker(t *testing.T) {
	model := newGatewayEventTestModel()
	block := NewMainACPTurnBlock("root-session")
	block.Status = "completed"
	block.Events = append(block.Events, SubagentEvent{
		Kind: SEAssistant,
		Text: "All clean. Here is the complete review.\n\n版本审查报告\n\n概要",
		Done: true,
	})
	ctx := BlockRenderContext{Width: 100, TermWidth: 100, Theme: model.theme}

	plain := renderedPlainRows(block.Render(ctx))
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, "· All clean. Here is the complete review.") {
		t.Fatalf("rendered rows = %q, want assistant marker on first narrative line", joined)
	}
	if strings.Contains(joined, "\n版本审查报告") || strings.Contains(joined, "\n概要") {
		t.Fatalf("rendered rows = %q, narrative continuation rows should not start in the marker column", joined)
	}
	if !strings.Contains(joined, "\n  版本审查报告") || !strings.Contains(joined, "\n  概要") {
		t.Fatalf("rendered rows = %q, want continuation rows aligned to the body column", joined)
	}
}

func TestGatewayReasoningStreamPreservesWhitespaceOnlyDeltas(t *testing.T) {
	model := NewModel(Config{NoColor: true, StreamTickInterval: 16 * time.Millisecond})
	model.viewport.SetWidth(80)
	model.viewport.SetHeight(20)

	now := time.Now()
	for _, text := range []string{"The", " ", "sandbox"} {
		updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
			Event: gateway.Event{
				Kind:       gateway.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative: &gateway.NarrativePayload{
					Role:          gateway.NarrativeRoleAssistant,
					ReasoningText: text,
					Final:         false,
					Visibility:    string(session.VisibilityUIOnly),
					UpdateType:    string(session.ProtocolUpdateTypeAgentThought),
					Scope:         gateway.EventScopeMain,
				},
			}}))

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

	updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:  gateway.NarrativeRoleAssistant,
				Text:  "partial answer",
				Final: false,
				Scope: gateway.EventScopeMain,
			},
		}}))

	m := updated.(*Model)
	updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{
		Err: &gateway.Error{
			Message: "providers: sse scanner: context canceled",
			Cause:   context.Canceled,
		}}))

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

func TestTaskResultErrorRendersSingleLineFailure(t *testing.T) {
	model := newGatewayEventTestModel()
	block := model.ensureMainACPTurnBlock("root-session")
	block.AppendStreamChunk(SEAssistant, "transient text")

	updated, _ := model.Update(TaskResultMsg{
		Err: errors.New("invalid model tool call for RUN_COMMAND: unexpected EOF\nprovider detail"),
	})
	model = updated.(*Model)
	model.syncViewportContent()

	if block.Status != "failed" {
		t.Fatalf("main turn status = %q, want failed terminal state", block.Status)
	}
	joined := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(joined, "✗ invalid model tool call for RUN_COMMAND: unexpected EOF provider detail") {
		t.Fatalf("viewport lines = %#v, want compact error line", model.viewportPlainLines)
	}
	for _, forbidden := range []string{"✗ failed", "error:", "unexpected EOF\nprovider detail"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("viewport lines = %#v, should not contain %q", model.viewportPlainLines, forbidden)
		}
	}
}

func TestModelUpdateConsumesGatewayToolEventsWithoutTranscriptRecovery(t *testing.T) {
	model := newGatewayEventTestModel()

	updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "READ",
				RawInput: map[string]any{"path": "/tmp/demo.txt"},
				Status:   "running",
				Scope:    gateway.EventScopeMain,
			},
		}}))

	m := updated.(*Model)
	updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:    "call-1",
				ToolName:  "READ",
				RawInput:  map[string]any{"path": "/tmp/demo.txt"},
				RawOutput: map[string]any{"path": "/tmp/demo.txt"},
				Content:   testToolContent("demo.txt"),
				Status:    "completed",
				Scope:     gateway.EventScopeMain,
			},
		}}))

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

	updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "RUN_COMMAND",
				RawInput: map[string]any{"command": `go test ./kernel/...`},
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
			},
		}}))

	model = updated.(*Model)
	updated, _ = model.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:    "call-1",
				ToolName:  "RUN_COMMAND",
				RawInput:  map[string]any{"command": `go test ./kernel/...`},
				RawOutput: map[string]any{"stdout": "stdout resolving packages"},
				Content:   testTerminalContent("stdout resolving packages"),
				Status:    gateway.ToolStatusRunning,
				Scope:     gateway.EventScopeMain,
			},
		}}))

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

func TestToolGroupsUseActionColorAndBlankSeparation(t *testing.T) {
	model := newGatewayEventTestModel()
	ctx := BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme}
	block := NewMainACPTurnBlock("session-1")
	block.UpdateTool("command-1", "RUN_COMMAND", "echo hi", "hi", false, false)
	block.UpdateTool("command-1", "RUN_COMMAND", "echo hi", "hi", true, false)
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

	updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:          gateway.NarrativeRoleAssistant,
				ReasoningText: "thinking through the plan",
				Final:         false,
				Scope:         gateway.EventScopeMain,
			},
		}}))

	m := updated.(*Model)
	updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:          gateway.NarrativeRoleAssistant,
				ReasoningText: "thinking through the plan",
				Text:          "final answer",
				Final:         true,
				Scope:         gateway.EventScopeMain,
			},
		}}))

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
	if !m.tryToggleACPToolPanelToken(block.BlockID(), "acp_reasoning:0") {
		t.Fatal("expected expanded reasoning click token to toggle closed")
	}
	rows = block.Render(BlockRenderContext{Width: 80, TermWidth: 80, Theme: m.theme})
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if strings.Contains(joined, "  thinking through the plan") {
		t.Fatalf("reasoning body should collapse after second click, got %q", joined)
	}
}

func TestGatewayReasoningFoldsAfterAttentionToolLoopAndTogglesInline(t *testing.T) {
	model := newGatewayEventTestModel()
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:          gateway.NarrativeRoleAssistant,
				ReasoningText: "thinking through the command choice",
				Final:         true,
				Scope:         gateway.EventScopeMain,
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:  gateway.NarrativeRoleAssistant,
				Text:  "I will run the test.",
				Final: true,
				Scope: gateway.EventScopeMain,
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "command-1",
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"command": "go test ./surfaces/tui/..."},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "command-1",
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusCompleted,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"command": "go test ./surfaces/tui/..."},
				RawOutput: map[string]any{
					"stdout":    "ok github.com/OnslaughtSnail/caelis/surfaces/tui/app\n",
					"exit_code": 0,
				},
			},
		}},
	} {
		updated, _ := model.Update(gatewayEventMsg(env))
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
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:          gateway.NarrativeRoleAssistant,
				ReasoningText: reasoning,
				Final:         true,
				Scope:         gateway.EventScopeMain,
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "command-1",
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"command": "go test ./..."},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "command-1",
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusCompleted,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"command": "go test ./..."},
				RawOutput: map[string]any{
					"stdout":    "ok\n",
					"exit_code": 0,
				},
			},
		}},
	} {
		updated, _ := model.Update(gatewayEventMsg(env))
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

func TestLiveReasoningStaysExpandedBeforePendingTaskWait(t *testing.T) {
	model := newGatewayEventTestModel()
	block := NewMainACPTurnBlock("root-session")
	block.Events = append(block.Events,
		SubagentEvent{
			Kind: SEReasoning,
			Text: "checking whether the task is still running\nreasoning live line two",
		},
		SubagentEvent{Kind: SEToolCall, CallID: "task-wait-1", Name: "TASK", Args: "Wait 5s"},
	)

	rows := block.Render(BlockRenderContext{Width: 100, TermWidth: 100, Theme: model.theme})
	joined := strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, "\n  reasoning live line two") {
		t.Fatalf("rendered rows = %q, want live reasoning body expanded before pending TASK wait", joined)
	}
	if !strings.Contains(joined, "▸ TASK Wait 5s") {
		t.Fatalf("rendered rows = %q, want pending TASK wait still visible", joined)
	}
}

func TestGatewayReasoningFoldUsesTimedDurationWhenAvailable(t *testing.T) {
	model := newGatewayEventTestModel()
	start := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			OccurredAt: start,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:          gateway.NarrativeRoleAssistant,
				ReasoningText: "first ",
				Final:         false,
				Scope:         gateway.EventScopeMain,
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			OccurredAt: start.Add(900 * time.Millisecond),
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:          gateway.NarrativeRoleAssistant,
				ReasoningText: "last",
				Final:         false,
				Scope:         gateway.EventScopeMain,
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			OccurredAt: start.Add(1500 * time.Millisecond),
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:  gateway.NarrativeRoleAssistant,
				Text:  "done thinking",
				Final: true,
				Scope: gateway.EventScopeMain,
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			OccurredAt: start.Add(1600 * time.Millisecond),
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "command-1",
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"command": "echo ok"},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			OccurredAt: start.Add(1700 * time.Millisecond),
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "command-1",
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusCompleted,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"command": "echo ok"},
				RawOutput: map[string]any{
					"stdout": "ok\n",
				},
			},
		}},
	} {
		updated, _ := model.Update(gatewayEventMsg(env))
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
