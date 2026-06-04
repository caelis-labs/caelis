package tuiapp

import (
	"maps"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
)

func TestGatewayTaskControlsMergeIntoTaskStage(t *testing.T) {
	model := newGatewayEventTestModel()
	sendReasoning := func(text string) {
		updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
			Event: gateway.Event{
				Kind:       gateway.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative: &gateway.NarrativePayload{
					Role:          gateway.NarrativeRoleAssistant,
					ReasoningText: text,
					Final:         true,
					Scope:         gateway.EventScopeMain,
				},
			}}))

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
		updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
			Event: gateway.Event{
				Kind:       gateway.EventKindToolCall,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				ToolCall: &gateway.ToolCallPayload{
					CallID:   item.callID,
					ToolName: "TASK",
					Status:   gateway.ToolStatusRunning,
					Scope:    gateway.EventScopeMain,
					RawInput: rawInput,
				},
			}}))

		model = updated.(*Model)
		updated, _ = model.Update(gatewayEventMsg(gateway.EventEnvelope{
			Event: gateway.Event{
				Kind:       gateway.EventKindToolResult,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				ToolResult: &gateway.ToolResultPayload{
					CallID:   item.callID,
					ToolName: "TASK",
					Status:   gateway.ToolStatusRunning,
					Scope:    gateway.EventScopeMain,
					RawInput: rawInput,
					RawOutput: map[string]any{
						"running": true,
						"state":   "running",
						"task_id": item.handle,
					},
				},
			}}))

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
		!strings.Contains(joined, `▸ TASK Write "Alice"`) ||
		!strings.Contains(joined, `▸ TASK Wait ella 5s`) ||
		!strings.Contains(joined, `▸ TASK Wait 8s`) ||
		strings.Contains(joined, "• Tasks") {
		t.Fatalf("rendered rows = %q, want active TASK step to stay expanded until the next step", joined)
	}
	if strings.Contains(joined, "task-9") {
		t.Fatalf("rendered rows = %q, should hide raw TASK tool and task id", joined)
	}
	sendReasoning("继续处理")
	rows = block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, `› 两个子任务已启动`) ||
		!strings.Contains(joined, `▸ TASK Write "Alice"`) ||
		!strings.Contains(joined, `• Tasks`) ||
		!strings.Contains(joined, `  └ Wait ella 5s, 8s`) ||
		!strings.Contains(joined, `› 继续处理`) {
		t.Fatalf("settled rows = %q, want previous wait controls settled before new reasoning", joined)
	}
	if !model.tryToggleACPToolPanelToken(block.BlockID(), "acp_task_stage:tasks:task-1,task-2") {
		t.Fatal("expected task stage click token to expand grouped TASK controls")
	}
	rows = block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, `  └ Wait ella 5s`) ||
		!strings.Contains(joined, `    Wait 8s`) ||
		strings.Contains(joined, `  └ Write "Alice"`) {
		t.Fatalf("expanded settled rows = %q, want wait controls expanded while Write stays independent", joined)
	}
	if !model.tryToggleACPToolPanelToken(block.BlockID(), "acp_task_stage:tasks:task-1,task-2") {
		t.Fatal("expected expanded task stage click token to collapse grouped TASK controls")
	}
	rows = block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, `Tasks`) ||
		!strings.Contains(joined, `Wait ella 5s, 8s`) ||
		strings.Contains(joined, `    Wait 8s`) {
		t.Fatalf("collapsed task rows = %q, want wait controls summarized again", joined)
	}
}

type testGatewayStreamRequest struct {
	SessionRef session.SessionRef
	CallID     string
	ToolName   string
	RawInput   map[string]any
	Ref        stream.Ref
	Scope      gateway.EventScope
}

func testGatewayStreamFrameEvents(req testGatewayStreamRequest, frame stream.Frame) []gateway.EventEnvelope {
	if !strings.EqualFold(strings.TrimSpace(req.ToolName), "SPAWN") {
		return nil
	}
	if frame.Closed {
		return []gateway.EventEnvelope{testGatewayStreamFrameEvent(req, frame, gateway.ToolStatusCompleted, "content")}
	}
	if !frame.Running || frame.Text == "" {
		return nil
	}
	if strings.TrimSpace(req.Ref.TaskID) != "" {
		frame.Ref.TaskID = req.Ref.TaskID
	}
	return []gateway.EventEnvelope{testGatewayStreamFrameEvent(req, frame, gateway.ToolStatusRunning, "terminal")}
}

func testGatewayStreamFrameEvent(req testGatewayStreamRequest, frame stream.Frame, status gateway.ToolStatus, contentType string) gateway.EventEnvelope {
	text := frame.Text
	if status == gateway.ToolStatusCompleted {
		text = "摘要\n- ool_demo_showcase.md 存在\n结论： 目录用于 SPAWN 演示"
	}
	content := []session.ProtocolToolCallContent{{
		Type:    contentType,
		Content: session.ProtocolTextContent(text),
	}}
	return gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindToolResult,
		SessionRef: req.SessionRef,
		Meta: map[string]any{
			gateway.EventMetaRoot: map[string]any{
				gateway.EventMetaVersion: 1,
				gateway.EventMetaRuntime: map[string]any{
					gateway.EventMetaRuntimeTool: map[string]any{
						gateway.EventMetaRuntimeToolName:           strings.TrimSpace(req.ToolName),
						gateway.EventMetaRuntimeTargetID:           firstNonEmpty(strings.TrimSpace(req.Ref.TaskID), strings.TrimSpace(frame.Ref.TaskID)),
						gateway.EventMetaRuntimeTargetKind:         "agent",
						gateway.EventMetaRuntimeStreamParentTaskID: strings.TrimSpace(frame.Ref.TaskID),
						"agent":  stringFromAny(req.RawInput["agent"]),
						"prompt": stringFromAny(req.RawInput["prompt"]),
					},
				},
			},
		},
		Protocol: &session.EventProtocol{
			Method:     session.ProtocolMethodSessionUpdate,
			UpdateType: string(session.ProtocolUpdateTypeToolUpdate),
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
				ToolCallID:    strings.TrimSpace(req.CallID),
				Kind:          strings.TrimSpace(req.ToolName),
				Title:         strings.TrimSpace(req.ToolName),
				Status:        string(status),
				RawInput:      maps.Clone(req.RawInput),
				Content:       content,
			},
		},
		ToolResult: &gateway.ToolResultPayload{
			CallID:   strings.TrimSpace(req.CallID),
			ToolName: strings.TrimSpace(req.ToolName),
			RawInput: maps.Clone(req.RawInput),
			Content:  content,
			Status:   status,
			Scope:    req.Scope,
		},
	}}
}

func stringFromAny(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func TestGatewayTaskHandoffBudgetKeepsSummaryAndNewStreamVisible(t *testing.T) {
	model := newGatewayEventTestModel()
	block := NewMainACPTurnBlock("root-session")
	block.Events = append(block.Events,
		SubagentEvent{
			Kind: SEReasoning,
			Text: strings.Repeat("I am coordinating child work before continuing.\n", 30),
		},
		SubagentEvent{Kind: SEToolCall, CallID: "task-1", Name: "TASK", Args: "wait ella 5s"},
	)
	ctx := BlockRenderContext{Width: 110, Height: 12, TermWidth: 110, Theme: model.theme}
	live := strings.Join(renderedPlainRows(block.Render(ctx)), "\n")
	if !strings.Contains(live, "I am coordinating child work before continuing.") || strings.Contains(live, "• Tasks") {
		t.Fatalf("live rows = %q, want active task step expanded without premature Tasks group", live)
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
		updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
			Event: gateway.Event{
				Kind:       gateway.EventKindToolCall,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				ToolCall: &gateway.ToolCallPayload{
					CallID:   item.callID,
					ToolName: "TASK",
					Status:   gateway.ToolStatusRunning,
					Scope:    gateway.EventScopeMain,
					RawInput: item.raw,
				},
			}}))

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
		"path":   "outside.txt",
		"reason": "need directory access",
	}

	updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "perm-1",
				ToolName: "custom_tool",
				Status:   gateway.ToolStatusRunning,
				RawInput: permissionInput,
			},
		}}))

	model = updated.(*Model)
	updated, _ = model.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindApprovalReview,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
			ApprovalPayload: &gateway.ApprovalPayload{
				ToolCallID:     "perm-1",
				ToolName:       "custom_tool",
				RawInput:       map[string]any{"reason": "need directory access"},
				ReviewStatus:   gateway.ApprovalReviewStatusInProgress,
				DecisionSource: "auto-review",
			},
		}}))

	model = updated.(*Model)
	if got := ansi.Strip(model.buildHintText()); !strings.Contains(got, "Reviewing approval request: custom_tool") {
		t.Fatalf("approval hint = %q, want pending review hint", got)
	}

	reviewText := "Automatic approval review approved (risk: medium, authorization: high): user requested it."
	updated, _ = model.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:    "perm-1",
				ToolName:  "custom_tool",
				Status:    gateway.ToolStatusCompleted,
				RawInput:  permissionInput,
				RawOutput: map[string]any{"summary": "custom tool completed"},
			},
		}}))

	model = updated.(*Model)
	updated, _ = model.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:  gateway.NarrativeRoleAssistant,
				Text:  "approval-dependent work finished",
				Final: true,
			},
		}}))

	model = updated.(*Model)
	updated, _ = model.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindApprovalReview,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
			ApprovalPayload: &gateway.ApprovalPayload{
				ToolCallID:     "perm-1",
				ToolName:       "custom_tool",
				RawInput:       map[string]any{"reason": "need directory access"},
				ReviewStatus:   gateway.ApprovalReviewStatusApproved,
				DecisionSource: "auto-review",
				ReviewText:     reviewText,
				Risk:           "medium",
				Authorization:  "high",
			},
		}}))

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
	if !strings.Contains(plain, "custom_tool outside.txt") {
		t.Fatalf("rendered rows = %q, want custom tool header", plain)
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
	toolIdx := strings.Index(plain, "custom_tool outside.txt")
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
	permissionInput := map[string]any{"path": "outside.txt"}
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "perm-denied",
				ToolName: "custom_tool",
				Status:   gateway.ToolStatusRunning,
				RawInput: permissionInput,
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:    "perm-denied",
				ToolName:  "custom_tool",
				Status:    gateway.ToolStatusFailed,
				Error:     true,
				RawInput:  permissionInput,
				RawOutput: map[string]any{"error": "operation was rejected"},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:  gateway.NarrativeRoleAssistant,
				Text:  "trying a safer path",
				Final: true,
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindApprovalReview,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
			ApprovalPayload: &gateway.ApprovalPayload{
				ToolCallID:     "perm-denied",
				ToolName:       "custom_tool",
				RawInput:       map[string]any{"reason": "need broad access"},
				ReviewStatus:   gateway.ApprovalReviewStatusDenied,
				DecisionSource: "auto-review",
				ReviewText:     "Automatic approval review denied (risk: high, authorization: low): not narrow enough",
				Risk:           "high",
				Authorization:  "low",
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
	toolIdx := strings.Index(plain, "custom_tool outside.txt")
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
		{callID: "task-raw-5", input: map[string]any{"action": "cancel", "task_id": "nora"}},
	} {
		updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
			Event: gateway.Event{
				Kind:       gateway.EventKindToolCall,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				ToolCall: &gateway.ToolCallPayload{
					CallID:   item.callID,
					ToolName: "TASK",
					Status:   gateway.ToolStatusRunning,
					Scope:    gateway.EventScopeMain,
					RawInput: item.input,
				},
			}}))

		model = updated.(*Model)
	}
	updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:  gateway.NarrativeRoleAssistant,
				Text:  "task controls settled",
				Final: true,
				Scope: gateway.EventScopeMain,
			},
		}}))

	model = updated.(*Model)
	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme})
	joined := strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, "• Tasks") ||
		!strings.Contains(joined, "  └ Wait 5s, nora 3s, nora 3s, nora 3s") ||
		!strings.Contains(joined, "    Cancel nora") {
		t.Fatalf("rendered rows = %q, want cleaned task action rows", joined)
	}
	for _, forbidden := range []string{"TASK wait", "task-12"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("rendered rows = %q, should not contain %q", joined, forbidden)
		}
	}
	if got := strings.Count(joined, "nora 3s"); got != 3 {
		t.Fatalf("rendered rows = %q, nora 3s count = %d, want 3", joined, got)
	}
}

func TestGatewayTaskSnapshotDoesNotRefreshCommandPanelOutput(t *testing.T) {
	model := newGatewayEventTestModel()
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "command-1",
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"command": "for i in $(seq 1 30); do echo $i; sleep 1; done"},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Meta:       testRuntimeToolMeta(map[string]any{"target_id": "task-7"}),
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "command-1",
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"command": "for i in $(seq 1 30); do echo $i; sleep 1; done"},
				RawOutput: map[string]any{
					"running": true,
					"state":   "running",
					"task_id": "task-7",
				},
				Content: testTerminalContent("进度: 1/30\n"),
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "task-wait-1",
				ToolName: "TASK",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"action": "wait", "task_id": "task-7", "yield_time_ms": 5000},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Meta:       testRuntimeToolMeta(map[string]any{"target_id": "task-7", "action": "wait"}),
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "task-wait-1",
				ToolName: "TASK",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"action": "wait", "task_id": "task-7", "yield_time_ms": 5000},
				RawOutput: map[string]any{
					"running": true,
					"state":   "running",
					"task_id": "task-7",
				},
				Content: testTerminalContent("进度: 1/30\n进度: 2/30\n进度: 3/30\n"),
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
	block.setToolPanelExpanded("command-1", true)
	rows := block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})
	plain := make([]string, 0, len(rows))
	for _, row := range rows {
		plain = append(plain, row.Plain)
	}
	joined := strings.Join(plain, "\n")
	for _, want := range []string{"• Ran for i in $(seq 1 30); do echo $i; sleep 1; done", "  └ 进度: 1/30", "▸ TASK Wait 5s"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered rows = %q, want %q", joined, want)
		}
	}
	for _, forbidden := range []string{"    进度: 2/30", "    进度: 3/30"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("rendered rows = %q, TASK wait output should not refresh RUN_COMMAND panel", joined)
		}
	}
	for _, forbidden := range []string{"|_", "RUN_COMMAND output", "│", "task / running", "state running", "stdout 进度", "task-7"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("rendered rows = %q, should not contain %q", joined, forbidden)
		}
	}
}

func TestGatewayTaskWaitCompletedShowsActionWithoutResultOutput(t *testing.T) {
	model := newGatewayEventTestModel()
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "task-wait-12",
				ToolName: "TASK",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"action": "wait", "task_id": "task-7", "yield_time_ms": 12000},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Meta:       testRuntimeToolMeta(map[string]any{"target_id": "task-7", "action": "wait", "target_kind": "command"}),
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "task-wait-12",
				ToolName: "TASK",
				Status:   gateway.ToolStatusCompleted,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"action": "wait", "task_id": "task-7", "yield_time_ms": 12000},
				RawOutput: map[string]any{
					"state":  "completed",
					"result": "line 1\nline 2\nline 3\n",
				},
				Content: testTerminalContent("line 1\nline 2\nline 3\n"),
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
	if !strings.Contains(joined, "Wait 12s") {
		t.Fatalf("rendered rows = %q, want TASK wait action only", joined)
	}
	for _, forbidden := range []string{"line 1", "line 2", "line 3"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("rendered rows = %q, should not render TASK wait result %q", joined, forbidden)
		}
	}
}

func TestGatewayTerminalToolArgumentsRenderFullAndWrapIndented(t *testing.T) {
	model := NewModel(Config{NoColor: true})
	model.viewport.SetWidth(46)
	model.viewport.SetHeight(20)
	command := "printf '%s\\n' BRANCH && git branch --show-current && printf '%s\\n' TRACKED && echo TERMINAL_ARG_TAIL_MARKER"
	updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "command-full-args",
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"command": command},
			},
		}}))

	model = updated.(*Model)
	model.syncViewportContent()

	joined := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(joined, "TERMINAL_ARG_TAIL_MARKER") {
		t.Fatalf("viewport lines = %#v, want full RUN_COMMAND command tail", model.viewportPlainLines)
	}
	if strings.Contains(joined, "echo ...") || strings.Contains(joined, "TERMINAL_ARG_TAIL...") {
		t.Fatalf("viewport lines = %#v, command was truncated", model.viewportPlainLines)
	}
	headerIdx := indexPlainLineContaining(model.viewportPlainLines, "• Ran ")
	tailIdx := indexPlainLineContaining(model.viewportPlainLines, "TERMINAL_ARG_TAIL_MARKER")
	if headerIdx < 0 || tailIdx <= headerIdx {
		t.Fatalf("viewport lines = %#v, want wrapped RUN_COMMAND header", model.viewportPlainLines)
	}
	if !strings.HasPrefix(model.viewportPlainLines[tailIdx], "  │ ") {
		t.Fatalf("wrapped tail line = %q, want terminal continuation rail", model.viewportPlainLines[tailIdx])
	}
}

func TestGatewaySpawnArgumentsRenderPromptPreviewAndExpandsFullPrompt(t *testing.T) {
	model := newGatewayEventTestModel()
	prompt := strings.Repeat("写一个完整参数展示测试。", 8) + "SPAWN_PROMPT_TAIL_MARKER"
	updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "spawn-full-args",
				ToolName: "SPAWN",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{
					"agent":  "self",
					"prompt": prompt,
				},
			},
		}}))

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
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "spawn-clean-final",
				ToolName: "SPAWN",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": prompt},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Meta: testRuntimeToolMeta(map[string]any{
				"target_id": "jack",
				"agent":     "self",
				"prompt":    prompt,
			}),
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "spawn-clean-final",
				ToolName: "SPAWN",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": prompt},
				RawOutput: map[string]any{
					"running": true,
					"state":   "running",
					"task_id": "jack",
					"text":    "dirty process line\nls output that should not become final",
				},
				Content: testTerminalContent("dirty process line\nls output that should not become final"),
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Meta: testRuntimeToolMeta(map[string]any{
				"target_id": "jack",
				"agent":     "self",
				"prompt":    prompt,
			}),
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "spawn-clean-final",
				ToolName: "SPAWN",
				Status:   gateway.ToolStatusCompleted,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": prompt},
				RawOutput: map[string]any{
					"running":       false,
					"state":         "completed",
					"task_id":       "jack",
					"result":        "dirty result that should not become final",
					"final_message": finalText,
				},
				Content: testToolContent(strings.Join([]string{
					"已完成",
					"✅ 创建 hello_from_spawn.txt",
					"内容： Hello from SPAWN child agent!",
					"文件  状态",
					"hello_from_spawn.txt  created",
					"报告位于 spawn_report.md",
				}, "\n")),
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
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "spawn-running-json",
				ToolName: "SPAWN",
				ToolKind: "execute",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"agent": "claude"},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Meta: testRuntimeToolMeta(map[string]any{
				"agent":  "claude",
				"prompt": prompt,
			}),
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "spawn-running-json",
				ToolName: "SPAWN",
				ToolKind: "execute",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
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
		updated, _ := model.Update(gatewayEventMsg(env))
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

func TestGatewaySpawnRunningStreamPreservesChunkBoundarySpaces(t *testing.T) {
	model := newGatewayEventTestModel()
	prompt := "写分析报告"
	start := gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindToolCall,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		ToolCall: &gateway.ToolCallPayload{
			CallID:   "spawn-space-stream",
			ToolName: "SPAWN",
			Status:   gateway.ToolStatusRunning,
			Scope:    gateway.EventScopeMain,
			RawInput: map[string]any{"agent": "self", "prompt": prompt},
		},
	}}
	updated, _ := model.Update(gatewayEventMsg(start))
	model = updated.(*Model)

	req := testGatewayStreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "spawn-space-stream",
		ToolName:   "SPAWN",
		RawInput:   map[string]any{"agent": "self", "prompt": prompt},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "child-task"},
		Scope:      gateway.EventScopeMain,
	}
	for _, chunk := range []string{"Now", " let", " me", " write", " the", " report."} {
		for _, env := range testGatewayStreamFrameEvents(req, stream.Frame{
			Ref:     stream.Ref{SessionID: "root-session", TaskID: "child-task"},
			Text:    chunk,
			Running: true,
		}) {
			updated, _ = model.Update(gatewayEventMsg(env))
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
	start := gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindToolCall,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		ToolCall: &gateway.ToolCallPayload{
			CallID:   "spawn-stream-final",
			ToolName: "SPAWN",
			Status:   gateway.ToolStatusRunning,
			Scope:    gateway.EventScopeMain,
			RawInput: map[string]any{"agent": "self", "prompt": prompt},
		},
	}}
	updated, _ := model.Update(gatewayEventMsg(start))
	model = updated.(*Model)

	req := testGatewayStreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "spawn-stream-final",
		ToolName:   "SPAWN",
		RawInput:   map[string]any{"agent": "self", "prompt": prompt},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "liam"},
		Scope:      gateway.EventScopeMain,
	}
	for _, env := range testGatewayStreamFrameEvents(req, stream.Frame{
		Ref:     stream.Ref{SessionID: "root-session", TaskID: "internal-task"},
		Text:    "ool_demo_showcase*.md(x6版本迭代)|**总文件数**|~80+|",
		Running: true,
	}) {
		updated, _ = model.Update(gatewayEventMsg(env))
		model = updated.(*Model)
	}
	for _, env := range testGatewayStreamFrameEvents(req, stream.Frame{
		Ref:     stream.Ref{SessionID: "root-session", TaskID: "internal-task"},
		Text:    "### 摘要\n- `ool_demo_showcase.md` 存在\n**结论：** 目录用于 SPAWN 演示",
		Closed:  true,
		Running: false,
		State:   "completed",
	}) {
		updated, _ = model.Update(gatewayEventMsg(env))
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
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "spawn-continue",
				ToolName: "SPAWN",
				ToolKind: "execute",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": "创建文件"},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Meta: testRuntimeToolMeta(map[string]any{
				"target_id": "jack",
				"agent":     "self",
				"prompt":    "创建文件",
			}),
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "spawn-continue",
				ToolName: "SPAWN",
				ToolKind: "execute",
				Status:   gateway.ToolStatusCompleted,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": "创建文件"},
				RawOutput: map[string]any{
					"running":       false,
					"state":         "completed",
					"task_id":       "jack",
					"final_message": "old final answer",
				},
				Content: testToolContent("old final answer"),
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Meta: testRuntimeToolMeta(map[string]any{
				"action":      "wait",
				"target_id":   "jack",
				"target_kind": "subagent",
			}),
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "task-wait-before-write",
				ToolName: "TASK",
				ToolKind: "execute",
				Status:   gateway.ToolStatusCompleted,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"action": "wait", "task_id": "jack", "yield_time_ms": 5000},
				RawOutput: map[string]any{
					"action":      "wait",
					"running":     false,
					"state":       "completed",
					"task_id":     "jack",
					"target_kind": "subagent",
					"result":      "old final answer",
				},
				Content: testToolContent("old final answer"),
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Meta: testRuntimeToolMeta(map[string]any{
				"action":      "write",
				"target_id":   "jack",
				"target_kind": "subagent",
				"input":       "检查刚才创建的文件",
			}),
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "task-write-continue",
				ToolName: "TASK",
				ToolKind: "execute",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"action": "write", "task_id": "jack", "input": "检查刚才创建的文件"},
				RawOutput: map[string]any{
					"action":      "write",
					"running":     true,
					"state":       "running",
					"task_id":     "jack",
					"target_kind": "subagent",
				},
				Content: testTerminalContent("正在读取 hello_from_spawn.txt"),
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Meta: testRuntimeToolMeta(map[string]any{
				"target_id": "jack",
				"agent":     "self",
				"prompt":    "检查刚才创建的文件",
			}),
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "spawn-continued-child",
				ToolName: "SPAWN",
				ToolKind: "execute",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": "检查刚才创建的文件"},
				RawOutput: map[string]any{
					"running": true,
					"state":   "running",
					"task_id": "jack",
					"text":    "正在读取 hello_from_spawn.txt",
				},
				Content: testTerminalContent("正在读取 hello_from_spawn.txt"),
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
	rows := block.Render(BlockRenderContext{Width: 160, TermWidth: 160, Theme: model.theme})
	joined := strings.Join(renderedPlainRows(rows), "\n")
	for _, want := range []string{"• Spawned jack[self]: 创建文件", "• Tasks", "Wait jack", "• Write jack: 检查刚才创建的文件", "正在读取 hello_from_spawn.txt"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("running continuation rows missing %q:\n%s", want, joined)
		}
	}
	if strings.Count(joined, "• Spawned") != 1 || strings.Contains(joined, "  └ Write jack") {
		t.Fatalf("TASK write should render separately without a second SPAWN or grouped Write row:\n%s", joined)
	}

	updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindToolResult,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Meta: testRuntimeToolMeta(map[string]any{
			"target_id": "jack",
			"agent":     "self",
			"prompt":    "检查刚才创建的文件",
		}),
		ToolResult: &gateway.ToolResultPayload{
			CallID:   "spawn-continued-child",
			ToolName: "SPAWN",
			ToolKind: "execute",
			Status:   gateway.ToolStatusCompleted,
			Scope:    gateway.EventScopeMain,
			RawInput: map[string]any{"agent": "self", "prompt": "检查刚才创建的文件"},
			RawOutput: map[string]any{
				"running":       false,
				"state":         "completed",
				"task_id":       "jack",
				"final_message": "### 检查完成\n- `hello_from_spawn.txt` 内容正确",
			},
			Content: testToolContent("检查完成\nhello_from_spawn.txt 内容正确"),
		},
	}}))

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

func TestGatewayCommandTerminalDeltasPreserveLineBreaks(t *testing.T) {
	model := newGatewayEventTestModel()
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "command-1",
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"command": "for i in $(seq 1 10); do echo $i; done"},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "command-1",
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"command": "for i in $(seq 1 10); do echo $i; done"},
				RawOutput: map[string]any{
					"running": true,
					"state":   "running",
					"task_id": "task-7",
					"stream":  "stdout",
					"text":    "[步骤 8/10] 正在处理... 09:05:53\n",
				},
				Content: testTerminalContent("[步骤 8/10] 正在处理... 09:05:53\n"),
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "command-1",
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"command": "for i in $(seq 1 10); do echo $i; done"},
				RawOutput: map[string]any{
					"running": true,
					"state":   "running",
					"task_id": "task-7",
					"stream":  "stdout",
					"text":    "[步骤 9/10] 正在处理... 09:05:55\n",
				},
				Content: testTerminalContent("[步骤 9/10] 正在处理... 09:05:55\n"),
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
	block.setToolPanelExpanded("command-1", true)
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
		"updated": true,
		"entries": rawInput["entries"],
	}
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "plan-1",
				ToolName: "PLAN",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: rawInput,
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:    "plan-1",
				ToolName:  "PLAN",
				Status:    gateway.ToolStatusCompleted,
				Scope:     gateway.EventScopeMain,
				RawInput:  rawInput,
				RawOutput: rawOutput,
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindPlanUpdate,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Plan: &gateway.PlanPayload{Entries: []gateway.PlanEntryPayload{
				{Content: "Inspect files", Status: "completed"},
				{Content: "Run validation", Status: "in_progress"},
			}},
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

func TestGatewayCommandPanelRendersACPTerminalContent(t *testing.T) {
	tests := []struct {
		name      string
		status    gateway.ToolStatus
		isErr     bool
		rawOutput map[string]any
		content   string
		want      []string
		forbid    []string
	}{
		{
			name:   "running preview",
			status: gateway.ToolStatusRunning,
			rawOutput: map[string]any{
				"running":        true,
				"state":          "running",
				"task_id":        "task-7",
				"supports_input": true,
			},
			content: "进度: 1/5\n",
			want:    []string{"• Ran for i in 1 2", "  └ 进度: 1/5"},
			forbid:  []string{"|_", "RUN_COMMAND output", "│", "task / running", "task task-7", "state running", "stdout 进度", "supports_input"},
		},
		{
			name:   "failed stdout stderr",
			status: gateway.ToolStatusFailed,
			isErr:  true,
			rawOutput: map[string]any{
				"stderr":    "permission denied\n",
				"stdout":    "ignored stdout\n",
				"exit_code": 1,
			},
			content: "ignored stdout\nstderr:\npermission denied\n",
			want:    []string{"  └ ignored stdout", "    stderr:", "    permission denied"},
			forbid:  []string{"|_", "RUN_COMMAND output", "│", "stderr permission denied", "exit 1"},
		},
		{
			name:   "failed stdout diagnostics",
			status: gateway.ToolStatusFailed,
			isErr:  true,
			rawOutput: map[string]any{
				"stdout":    "dangerous command is blocked even in auto-review mode\n",
				"stderr":    "",
				"error":     "exit status 1",
				"exit_code": 1,
			},
			content: "dangerous command is blocked even in auto-review mode\n",
			want:    []string{"  └ dangerous command is blocked even in auto-review mode"},
			forbid:  []string{"exit 1", "exit status 1"},
		},
		{
			name:   "source normalized output content",
			status: gateway.ToolStatusCompleted,
			rawOutput: map[string]any{
				"stdout":    "",
				"stderr":    "",
				"output":    "go: module internal registry: network unreachable\n",
				"exit_code": 1,
			},
			content: "go: module internal registry: network unreachable\n",
			want:    []string{"  └ go: module internal registry: network unreachable"},
			forbid:  []string{"no output", "exit 1"},
		},
		{
			name:   "successful empty output",
			status: gateway.ToolStatusCompleted,
			rawOutput: map[string]any{
				"exit_code": 0,
			},
			want:   []string{"  └ (no output)"},
			forbid: []string{"exit 0", "completed"},
		},
		{
			name:   "successful stdout stderr",
			status: gateway.ToolStatusCompleted,
			rawOutput: map[string]any{
				"stdout":    "line one\nline two\n",
				"stderr":    "warning\n",
				"result":    "compact stale result",
				"exit_code": 0,
			},
			content: "line one\nline two\nstderr:\nwarning\n",
			want:    []string{"  └ line one", "    line two", "    stderr:", "    warning"},
			forbid:  []string{"compact stale result", "exit 0", "no output"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			model := newGatewayEventTestModel()
			callID := "command-" + strings.ReplaceAll(tt.name, " ", "-")
			updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
				Event: gateway.Event{
					Kind:       gateway.EventKindToolCall,
					SessionRef: session.SessionRef{SessionID: "root-session"},
					ToolCall: &gateway.ToolCallPayload{
						CallID:   callID,
						ToolName: "RUN_COMMAND",
						Status:   gateway.ToolStatusRunning,
						Scope:    gateway.EventScopeMain,
						RawInput: map[string]any{"command": "for i in 1 2; do echo $i; done"},
					},
				}}))

			model = updated.(*Model)
			var content []session.ProtocolToolCallContent
			if tt.content != "" {
				content = testTerminalContent(tt.content)
			}
			updated, _ = model.Update(gatewayEventMsg(gateway.EventEnvelope{
				Event: gateway.Event{
					Kind:       gateway.EventKindToolResult,
					SessionRef: session.SessionRef{SessionID: "root-session"},
					ToolResult: &gateway.ToolResultPayload{
						CallID:    callID,
						ToolName:  "RUN_COMMAND",
						Status:    tt.status,
						Error:     tt.isErr,
						Scope:     gateway.EventScopeMain,
						RawInput:  map[string]any{"command": "for i in 1 2; do echo $i; done"},
						RawOutput: tt.rawOutput,
						Content:   content,
					},
				}}))

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

func TestGatewayBASHContentlessFinalPreservesStreamedTerminalOutput(t *testing.T) {
	model := newGatewayEventTestModel()
	callID := "command-stream-final-empty"
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   callID,
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"command": "git log --oneline -6"},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:   callID,
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"command": "git log --oneline -6"},
				RawOutput: map[string]any{
					"running": true,
					"state":   "running",
					"task_id": "task-7",
				},
				Content: testTerminalContent("stale streamed preview\n"),
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:    callID,
				ToolName:  "RUN_COMMAND",
				Status:    gateway.ToolStatusCompleted,
				Scope:     gateway.EventScopeMain,
				RawInput:  map[string]any{"command": "git log --oneline -6"},
				RawOutput: map[string]any{"exit_code": 0},
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
	block.setToolPanelExpanded(callID, true)
	joined := strings.Join(renderedPlainRows(block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})), "\n")
	if !strings.Contains(joined, "stale streamed preview") {
		t.Fatalf("rendered rows = %q, want accumulated terminal output preserved", joined)
	}
	if strings.Contains(joined, "(no output)") {
		t.Fatalf("rendered rows = %q, should not replace accumulated terminal output with placeholder", joined)
	}
}

func TestGatewayParticipantBASHContentlessFinalPreservesStreamedTerminalOutput(t *testing.T) {
	model := newGatewayEventTestModel()
	callID := "participant-command-contentless-final"
	origin := &gateway.EventOrigin{
		Source:        "acp_participant",
		Scope:         gateway.EventScopeParticipant,
		ScopeID:       "codex-turn-1",
		Actor:         "@codex",
		ParticipantID: "codex-001",
	}
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     origin,
			ToolCall: &gateway.ToolCallPayload{
				CallID:   callID,
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeParticipant,
				RawInput: map[string]any{"command": "go test ./..."},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     origin,
			ToolResult: &gateway.ToolResultPayload{
				CallID:   callID,
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeParticipant,
				RawInput: map[string]any{"command": "go test ./..."},
				Content:  testTerminalContent("internal/service: missing mysql.default\n"),
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     origin,
			ToolResult: &gateway.ToolResultPayload{
				CallID:   callID,
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusFailed,
				Error:    true,
				Scope:    gateway.EventScopeParticipant,
				RawInput: map[string]any{"command": "go test ./..."},
			},
		}},
	} {
		updated, _ := model.Update(gatewayEventMsg(env))
		model = updated.(*Model)
	}

	block, ok := model.doc.Blocks()[0].(*ParticipantTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want ParticipantTurnBlock", model.doc.Blocks()[0])
	}
	if len(block.Events) != 1 || !block.Events[0].Done || !block.Events[0].Err {
		t.Fatalf("participant events = %#v, want failed completed RUN_COMMAND event", block.Events)
	}
	if got := strings.TrimSpace(block.Events[0].Output); got != "internal/service: missing mysql.default" {
		t.Fatalf("terminal output = %q, want streamed output preserved", got)
	}
	block.setToolPanelExpanded(callID, true)
	joined := strings.Join(renderedPlainRows(block.Render(BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme})), "\n")
	if !strings.Contains(joined, "internal/service: missing mysql.default") {
		t.Fatalf("rendered rows = %q, want streamed terminal output", joined)
	}
	if strings.Contains(joined, "  └ failed") {
		t.Fatalf("rendered rows = %q, contentless final should not replace streamed output", joined)
	}
}
