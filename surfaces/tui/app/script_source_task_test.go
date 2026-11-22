package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestScriptSourceAndZeroChildResult(t *testing.T) {
	events := []*session.Event{{Type: session.EventTypeToolCall, Tool: &session.EventTool{ID: "source", Name: "js_exec", Status: "started", Input: map[string]any{"file": "workflow.js"}}}, {Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: "source", Name: "js_exec", Status: "completed", Input: map[string]any{"file": "workflow.js"}, Output: map[string]any{"answer": 42}}, Meta: map[string]any{"code_mode_script_completion": "completed", "code_mode_source": map[string]any{"path": "workflow.js", "code": "const frozen = 42;\nreturn {answer:frozen};"}}}}
	message := model.NewMessage(model.RoleTool, model.Part{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{ToolUseID: "source", Name: "js_exec", Content: []model.Part{model.NewJSONPart([]byte(`{"answer":42}`))}}})
	events[1].Message = &message
	m, b := groupedCanonicalModel(t, events, "main", true)
	rows := b.Render(m.blockRenderContext(70))
	text := ""
	for _, r := range rows {
		text += r.Plain + "\n"
	}
	if !strings.Contains(text, "js_exec workflow.js") || !strings.Contains(text, "42") {
		t.Fatalf("missing source/result: %s", text)
	}
}

func TestScriptTaskObservationDoesNotDependOnDisclosure(t *testing.T) {
	m, b := groupedCanonicalModel(t, []*session.Event{{Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: "source", Name: "js_exec", Status: "in_progress", Output: map[string]any{"handle": "script", "state": "running"}}}}, "main", true)
	_ = b
	if !m.taskStreamPanelExpanded("source", "script") {
		t.Fatal("yielded script completion has no observation demand while collapsed")
	}
}

func TestScriptKeepsOuterTaskHandleWhenReturningInnerTask(t *testing.T) {
	meta := map[string]any{"caelis": map[string]any{"runtime": map[string]any{"task": map[string]any{"kind": "code_mode", "state": "completed", "handle": "outer"}}}, "code_mode_script_completion": "completed"}
	m, b := groupedCanonicalModel(t, []*session.Event{{Type: session.EventTypeToolCall, Tool: &session.EventTool{ID: "owner", Name: "js_exec", Status: "started", Input: map[string]any{"code": "return await RunCommand({command:'echo ok',yield_time_ms:0});"}}}, {Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: "owner", Name: "js_exec", Status: "completed", Output: map[string]any{"handle": "inner", "state": "running"}}, Meta: meta}}, "main", true)
	for _, ev := range b.(*MainACPTurnBlock).Events {
		if ev.Kind == SEToolCall && ev.CallID == "owner" && (!ev.Done || ev.TaskHandle != "outer") {
			t.Fatalf("inner Task replaced owner: %+v", ev)
		}
	}
	if m.taskStreamPanelExpanded("owner", "outer") {
		t.Fatal("settled script reopened an observer for an unrelated inner Task")
	}
}

func TestScriptTaskTerminalUpdateWithoutTaskRead(t *testing.T) {
	for _, state := range []string{"completed", "failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			m, b := groupedCanonicalModel(t, []*session.Event{{Type: session.EventTypeToolCall, Tool: &session.EventTool{ID: "owner", Name: "js_exec", Status: "started", Input: map[string]any{"code": "return await Read({path:'input'});"}}}, {Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: "owner", Name: "js_exec", Status: "in_progress", Output: map[string]any{"handle": "outer", "state": "running"}}}}, "main", false)
			completion := "completed"
			if state != "completed" {
				completion = "failed"
			}
			name := "js_exec"
			m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "fixture", TurnID: "turn", Scope: eventstream.ScopeMain, Update: eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "owner", Name: &name, Status: &state, Meta: map[string]any{"code_mode_script_completion": completion}}})
			text := ""
			for _, r := range b.Render(m.blockRenderContext(70)) {
				text += r.Plain + "\n"
			}
			if !strings.Contains(text, "• js_exec") || !b.(*MainACPTurnBlock).Events[0].Done {
				t.Fatalf("terminal Task without Task.read: %s", text)
			}
		})
	}
}

func TestScriptSourceDisclosesFrozenBytesOnFirstClick(t *testing.T) {
	events := []*session.Event{{Type: session.EventTypeToolCall, Tool: &session.EventTool{ID: "file", Name: "js_exec", Status: "started", Input: map[string]any{"file": "workflow.js"}}}, {Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: "file", Name: "js_exec", Status: "completed", Input: map[string]any{"file": "workflow.js"}}, Meta: map[string]any{"code_mode_script_completion": "completed", "code_mode_source": map[string]any{"path": "workflow.js", "code": "const frozen=42;\nreturn frozen;"}}}}
	m, b := groupedCanonicalModel(t, events, "main", true)
	ctx := m.blockRenderContext(70)
	if !m.tryToggleFoldToken(b.BlockID(), acpToolPanelClickToken("file")) {
		t.Fatal("source click has no route")
	}
	if text := joinRenderedPlain(b.Render(ctx)); !strings.Contains(text, "const frozen=42;") {
		t.Fatalf("first click failed to disclose frozen source: %s", text)
	}
	if !m.tryToggleFoldToken(b.BlockID(), acpToolPanelClickToken("file")) {
		t.Fatal("source collapse has no route")
	}
	if text := joinRenderedPlain(b.Render(ctx)); strings.Contains(text, "const frozen=42;") {
		t.Fatalf("second click did not collapse source: %s", text)
	}
}

func TestScriptExpandedSourceKeepsCommonColumnsWithoutRail(t *testing.T) {
	m := NewModel(Config{})
	ctx := m.blockRenderContext(44)
	for _, action := range []string{scriptToolLabel} {
		lines, _, ok := wrapACPTranscriptHeaderForViewport("• "+action+" const first=1;\nreturn first;", 44, ctx, acpHeaderMarkDefault, false)
		if !ok || len(lines) != 2 || strings.Contains(strings.Join(lines, "\n"), "│") || !strings.HasSuffix(lines[1], "return first;") {
			t.Fatalf("expanded source must share header columns without a rail: %q", lines)
		}
	}
}
