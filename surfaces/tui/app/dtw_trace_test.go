package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

func desktopTraceEvents() []*session.Event {
	makeCall := func(id, pluginID string, parent *session.EventParentTool) *session.Event {
		const name = "dtw__exec"
		binding := mcp.BindingForDefinition(tool.Definition{Name: name, Metadata: map[string]any{
			mcp.MetadataKind: mcp.KindTool, mcp.MetadataPluginID: pluginID, mcp.MetadataServer: "dtw",
			mcp.MetadataTool: "exec", mcp.MetadataSourceFingerprint: "fixture",
		}})
		return &session.Event{Type: session.EventTypeToolResult, Tool: &session.EventTool{
			ID: id, Name: name, Status: "completed", Parent: parent, Binding: &binding,
			Input: map[string]any{"app": "fixture.app", "source": "if (false) await app.quit({}); return 42;"},
			Result: &tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: []byte(`{
				"app":"fixture.app","execution_id":"exe_fixture_1","status":"completed",
				"result":{"operation":"scene.quit","private":"SECRET_OUTPUT"},
				"effects":{"dispatch":{"dispatched":2,"unknown":1},"interrupted":1,
				"acknowledgements":[{"call":1,"operation":"scene.click","confirmed":"element_focused"}]}}
			`)}}},
		}}
	}
	parent := &session.EventParentTool{Kind: "code_mode", CallID: "dtw-script", Name: "js_exec"}
	script := &session.EventTool{ID: parent.CallID, Name: parent.Name, Status: "completed", Input: map[string]any{"code": "return await dtw__exec(args);"}}
	return []*session.Event{
		makeCall("dtw-native", "desktop-world", nil),
		{Type: session.EventTypeToolCall, Tool: script},
		makeCall("dtw-nested", "desktop-world", parent),
		{Type: session.EventTypeToolResult, Tool: script, Meta: map[string]any{"code_mode_script_completion": "completed"}},
		makeCall("dtw-foreign", "foreign", nil),
	}
}

func TestDesktopWorldTraceRendersForNativeAndScriptOnlyFromBoundSource(t *testing.T) {
	const summary = "dtw__exec · click, 2 dispatched, 1 unknown, 1 interrupted · fixture.app"
	for _, replay := range []bool{false, true} {
		m, block := groupedCanonicalModel(t, desktopTraceEvents(), "main", replay)
		ctx := m.blockRenderContext(110)
		text := joinRenderedPlain(block.Render(ctx))
		if strings.Count(text, summary) != 2 {
			t.Fatalf("expected compact native and nested summaries (replay=%t):\n%s", replay, text)
		}
		for _, hidden := range []string{"scene.", "element_focused", "dtw__exec · quit", "SECRET_OUTPUT"} {
			if strings.Contains(text, hidden) {
				t.Fatalf("unexpected %q in collapsed trace (replay=%t):\n%s", hidden, replay, text)
			}
		}
		clickScriptToken(t, m, block, ctx, acpToolPanelClickToken("dtw-nested"))
		if got := joinRenderedPlain(block.Render(ctx)); !strings.Contains(got, "if (false) await app.quit") {
			t.Fatalf("nested disclosure lost original source:\n%s", got)
		}
	}
}
